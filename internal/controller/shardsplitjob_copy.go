/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// shardsplitjob_copy.go 는 ShardSplitJob 의 *InitialCopy* phase 실 결선이다 — source shard 의
// 데이터에서 각 target shard 의 키 범위에 속하는 row 를 target 으로 복사하는 K8s Job 을
// 생성하고 완료를 감시한다.
//
// 실행 모델: 컨트롤러가 직접 PG 에 접속하지 않고(오퍼레이터는 in-pod local DSN 모델),
// 클러스터 내부에 *Job* 을 띄워 그 Pod 가 source/target shard 에 접속해 복사한다. pg_hba 가
// `postgres` superuser 를 클러스터 내부 사설 IP 에서 trust 하므로(builders.go renderPGHBAConf)
// Job 은 *자격증명 없이* 접속한다. Job 이미지(reshard-copy-poc)는 router.CopyShardRange 로
// vindex(라우팅과 동일) 기준 부분집합만 복사한다 — 가역(rollback=target drop).
//
// Cutover 후 source 정리(이동 row 삭제)는 별도(Cleanup) 트랙. CDC 증분 catch-up 도 후속.

// reshardCopyImage 는 InitialCopy Job 이 실행할 이미지다 (RESHARD_COPY_IMAGE 로 주입,
// 미설정 시 기본값 — kind 등에서 load 한 로컬 태그).
func reshardCopyImage() string {
	if v := os.Getenv("RESHARD_COPY_IMAGE"); v != "" {
		return v
	}
	return "ghcr.io/keiailab/reshard-copy:dev"
}

// reshardJobName 은 target shard 별 데이터이동 Job 이름(결정적 — 멱등 생성). deleteOnly 면
// Cleanup(삭제) Job, 아니면 InitialCopy(복사) Job — 이름이 분리돼 두 phase 의 Job 이 공존한다.
func reshardJobName(cluster, shardID string, deleteOnly bool) string {
	verb := "copy"
	if deleteOnly {
		verb = "del"
	}
	return fmt.Sprintf("%s-rsd-%s-%s", cluster, verb, shardID)
}

// internalShardDSN 은 클러스터 내부 trust DSN 을 만든다(postgres superuser, 무비밀번호 —
// pg_hba 가 내부 사설 IP 의 postgres 를 trust).
func internalShardDSN(podDNS string) string {
	return fmt.Sprintf("host=%s port=%d user=postgres dbname=postgres sslmode=disable", podDNS, pgPort)
}

// sourceShardPodDNS 는 ordinal source shard(`shard-N`)의 primary pod(-0) 안정 DNS 를 만든다.
func sourceShardPodDNS(cluster, ns, shardID string) (string, error) {
	if !strings.HasPrefix(shardID, "shard-") {
		return "", fmt.Errorf("source shard %q is not ordinal (want shard-N)", shardID)
	}
	ord, err := strconv.Atoi(strings.TrimPrefix(shardID, "shard-"))
	if err != nil {
		return "", fmt.Errorf("source shard %q: %w", shardID, err)
	}
	pod := ShardStatefulSetName(cluster, int32(ord)) + "-0"
	return fmt.Sprintf("%s.%s.%s.svc.cluster.local", pod, ShardServiceName(cluster, int32(ord)), ns), nil
}

// targetShardPodDNS 는 resharding target shard 의 primary pod(-0) 안정 DNS 를 만든다.
func targetShardPodDNS(cluster, ns, shardID string) string {
	pod := TargetShardStatefulSetName(cluster, shardID) + "-0"
	return fmt.Sprintf("%s.%s.%s.svc.cluster.local", pod, TargetShardServiceName(cluster, shardID), ns)
}

// rangesEnvValue 는 target 들의 키 범위를 `shard:lo:hi,...` 로 직렬화한다(Job 의
// PGROUTER_RANGES — post-split 토폴로지를 reshard-copy-poc 에 전달).
func rangesEnvValue(targets []postgresv1alpha1.ShardSplitTarget) string {
	var parts []string
	for _, t := range targets {
		for _, r := range t.Ranges {
			parts = append(parts, fmt.Sprintf("%s:%s:%s", r.Shard, r.Lo, r.Hi))
		}
	}
	return strings.Join(parts, ",")
}

// keyspaceVindex 는 cluster/keyspace 의 ShardRange 에서 vindex 컬럼·함수·reference 테이블을
// 읽는다 (copy 가 라우팅과 동일 vindex 를 쓰도록).
func (r *ShardSplitJobReconciler) keyspaceVindex(ctx context.Context, ns, cluster, keyspace string) (col, fn string, refTables []string, err error) {
	var list postgresv1alpha1.ShardRangeList
	if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return "", "", nil, fmt.Errorf("list ShardRange: %w", err)
	}
	for i := range list.Items {
		sr := &list.Items[i]
		if sr.Spec.Cluster == cluster && sr.Spec.Keyspace == keyspace {
			return sr.Spec.Vindex.Column, string(sr.Spec.Vindex.Function), sr.Spec.ReferenceTables, nil
		}
	}
	return "", "", nil, fmt.Errorf("no ShardRange for cluster=%s keyspace=%s", cluster, keyspace)
}

// buildReshardJob 은 한 target shard 의 데이터이동 Job 을 만든다. deleteOnly=false 면
// InitialCopy(source→target 복사), true 면 Cleanup(cutover 후 source 에서 이동분 삭제 —
// target 접속 불요). 두 경우 모두 모든 user 테이블 자동 발견(COPY_TABLE 미설정).
func (r *ShardSplitJobReconciler) buildReshardJob(ssj *postgresv1alpha1.ShardSplitJob, sourceDSN, targetDSN, targetShard, col, fn, ranges string, refTables []string, deleteOnly bool) *batchv1.Job {
	backoff := int32(2)
	env := []corev1.EnvVar{
		{Name: "PGROUTER_SOURCE_DSN", Value: sourceDSN},
		{Name: "PGROUTER_RESHARD_TARGET_SHARD", Value: targetShard},
		{Name: "PGROUTER_VINDEX_COLUMN", Value: col},
		{Name: "PGROUTER_VINDEX_FUNCTION", Value: fn},
		{Name: "PGROUTER_RANGES", Value: ranges},
		{Name: "PGROUTER_REFERENCE_TABLES", Value: strings.Join(refTables, ",")},
	}
	if deleteOnly {
		env = append(env, corev1.EnvVar{Name: "PGROUTER_RESHARD_DELETE_ONLY", Value: "1"})
	} else {
		env = append(env, corev1.EnvVar{Name: "PGROUTER_TARGET_DSN", Value: targetDSN})
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      reshardJobName(ssj.Spec.Cluster, targetShard, deleteOnly),
			Namespace: ssj.Namespace,
			Labels:    ReshardTargetSelectorLabels(ssj.Spec.Cluster, targetShard),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: ReshardTargetSelectorLabels(ssj.Spec.Cluster, targetShard)},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "reshard-copy",
						Image: reshardCopyImage(),
						Env:   env,
					}},
				},
			},
		},
	}
}

// reconcileInitialCopy 는 각 target 의 source→target 복사 Job 을 띄우고 완료를 집계한다.
func (r *ShardSplitJobReconciler) reconcileInitialCopy(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (done bool, failure string, err error) {
	return r.reconcileReshardJobs(ctx, ssj, false)
}

// reconcileCleanup 은 cutover 후 각 target 키를 source 에서 삭제하는 Job 을 띄우고 완료를
// 집계한다(source 정리 — 이동분 회수). InitialCopy 가 끝나고 라우팅이 전환된 *뒤* 이므로
// 안전하다.
func (r *ShardSplitJobReconciler) reconcileCleanup(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (done bool, failure string, err error) {
	return r.reconcileReshardJobs(ctx, ssj, true)
}

// reconcileReshardJobs 는 각 target 의 데이터이동 Job(deleteOnly=복사/삭제)을 멱등 생성하고
// 완료를 집계한다. 반환: done(전부 성공), failure(한 Job 이라도 실패 시 사유), err(전이 가능
// — requeue).
func (r *ShardSplitJobReconciler) reconcileReshardJobs(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob, deleteOnly bool) (done bool, failure string, err error) {
	phase := "InitialCopy"
	if deleteOnly {
		phase = "Cleanup"
	}
	if len(ssj.Spec.Sources) == 0 {
		return false, phase + ": no source shard", nil
	}
	col, fn, refTables, err := r.keyspaceVindex(ctx, ssj.Namespace, ssj.Spec.Cluster, ssj.Spec.Keyspace)
	if err != nil {
		return false, "", err // ShardRange 아직 부재 등 — requeue.
	}
	srcDNS, err := sourceShardPodDNS(ssj.Spec.Cluster, ssj.Namespace, ssj.Spec.Sources[0])
	if err != nil {
		return false, phase + ": " + err.Error(), nil // 설정 오류 — Failed.
	}
	ranges := rangesEnvValue(ssj.Spec.Targets)

	allDone := true
	for i := range ssj.Spec.Targets {
		t := &ssj.Spec.Targets[i]
		name := reshardJobName(ssj.Spec.Cluster, t.ShardID, deleteOnly)
		var job batchv1.Job
		getErr := r.Get(ctx, client.ObjectKey{Namespace: ssj.Namespace, Name: name}, &job)
		switch {
		case apierrors.IsNotFound(getErr):
			targetDSN := ""
			if !deleteOnly {
				targetDSN = internalShardDSN(targetShardPodDNS(ssj.Spec.Cluster, ssj.Namespace, t.ShardID))
			}
			j := r.buildReshardJob(ssj, internalShardDSN(srcDNS), targetDSN, t.ShardID, col, fn, ranges, refTables, deleteOnly)
			if err := controllerutil.SetControllerReference(ssj, j, r.Scheme); err != nil {
				return false, "", err
			}
			if err := r.Create(ctx, j); err != nil {
				return false, "", err
			}
			allDone = false
		case getErr != nil:
			return false, "", getErr
		case job.Status.Succeeded > 0:
			// 이 target 완료.
		case jobFailed(&job):
			return false, fmt.Sprintf("%s: job %s failed", phase, name), nil
		default:
			allDone = false // 진행 중.
		}
	}
	return allDone, "", nil
}

// jobFailed 는 Job 이 backoffLimit 소진으로 Failed condition 인지 본다.
func jobFailed(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
