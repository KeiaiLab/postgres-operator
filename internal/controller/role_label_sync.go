/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// InstanceRoleLabelKey는 각 PG Pod의 *현재* primary/replica 역할을 반영하는 live
// label이다. rw 트래픽을 primary로 라우팅하는 Service selector가 이 label에
// 의존한다 — internal/controller/failover/pvc_fence_runbook.go의
// PVCFenceMountedPod.InstanceRole 및 docs/runbooks/pvc-fence.md §5.2가 이미
// 참조하는 것과 동일한 키다.
//
// #226 (2026-07-11 INC): 이 키는 오래 전부터 fencing 결정 로직 + 운영 runbook에서
// 참조돼 왔고 한때 e2e 테스트도 이 label로 primary를 selector했지만, 실제로 Pod에
// patch하는 코드는 존재한 적이 없었다 — commit 65d5819가 그 gap("PG Pod 에
// 부착되지 않는 label")을 발견해 e2e를 CR-status 기반으로 우회시켰을 뿐, 근본
// 원인(label을 아무도 patch하지 않음)은 미해결로 남아 있었다. 그 결과 failover가
// CR status/lease 수준에서는 완료돼도, 죽은 노드의 구 primary Pod가 stuck
// Terminating 상태로 label을 영원히 들고 있는 동안 새 primary는 label을 받지
// 못해 rw Service의 endpoints가 0개가 되는 사고(INC 2026-07-11)로 이어졌다. 본
// 파일이 그 gap을 채운다.
const (
	InstanceRoleLabelKey     = "postgres.keiailab.io/instance-role"
	InstanceRoleLabelPrimary = "primary"
	InstanceRoleLabelReplica = "replica"
)

// reconcilePrimaryRoleLabels는 각 shard의 관측된(aggregateShardStatus 산출)
// primary/replica를 Pod label로 투영한다.
//
// #226: standby cleanup/재시딩(reconcileStaleReplicas/reconcileRoguePrimaries)
// 보다 *먼저*, 그리고 *독립적으로* 호출되어야 한다 — 그래야 죽은 노드의 pod
// delete가 영원히 hang해도(kubelet 무응답) rw 트래픽 라우팅은 항상 CR
// status/lease가 가리키는 실제 primary로 수렴한다. 강등 대상 Pod가 이미
// Terminating이어도 label patch 자체는 성공한다 — Kubernetes는 실제 삭제
// (finalizer 해소 + etcd 제거) 완료 전까지 metadata patch를 허용하므로, 이
// 함수는 재시딩 delete의 진행 상태와 무관하게 항상 label을 correct 상태로
// 되돌릴 수 있다.
//
// 반환값 primaryLabelsSynced는 Ready인 primary를 가진 *모든* shard가 그 Pod에
// instance-role=primary label을 실제로 보유하게 됐는지를 보고한다 — F3
// (ConditionPrimaryRoleSynced)가 이 값으로 "CR 상태는 정상인데 라벨/라우팅은
// 아직 수렴하지 않음"이라는 모순을 노출한다. Ready primary가 없는 shard는
// 집계에서 제외한다(아직 아무것도 sync할 대상이 없으므로 정상 상태).
//
// best-effort: 개별 Pod patch 실패(NotFound 포함)는 첫 에러만 반환하고 계속
// 진행한다 — 한 shard/pod의 실패가 다른 shard의 label 수렴을 막지 않는다.
func (r *PostgresClusterReconciler) reconcilePrimaryRoleLabels(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shardStatuses []postgresv1alpha1.ShardStatus,
) (primaryLabelsSynced bool, err error) {
	primaryLabelsSynced = true
	var firstErr error

	for i := range shardStatuses {
		ss := &shardStatuses[i]
		if ss.Primary != nil && ss.Primary.Pod != "" {
			labelErr := r.setInstanceRoleLabel(ctx, cluster.Namespace, ss.Primary.Pod, InstanceRoleLabelPrimary)
			if ss.Primary.Ready && labelErr != nil {
				primaryLabelsSynced = false
			}
			if labelErr != nil && firstErr == nil {
				firstErr = fmt.Errorf("label primary pod %q: %w", ss.Primary.Pod, labelErr)
			}
		}
		for j := range ss.Replicas {
			pod := ss.Replicas[j].Pod
			if pod == "" {
				continue
			}
			if labelErr := r.setInstanceRoleLabel(ctx, cluster.Namespace, pod, InstanceRoleLabelReplica); labelErr != nil && firstErr == nil {
				firstErr = fmt.Errorf("label replica pod %q: %w", pod, labelErr)
			}
		}
	}
	return primaryLabelsSynced, firstErr
}

// setInstanceRoleLabel은 InstanceRoleLabelKey를 원하는 값으로 patch한다. 이미
// 그 값이면 no-op(불필요한 API 호출 회피). Pod가 이미 완전히 삭제됐으면 정리할
// 대상이 없으므로 정상 처리한다.
func (r *PostgresClusterReconciler) setInstanceRoleLabel(
	ctx context.Context,
	namespace, podName, role string,
) error {
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if pod.Labels[InstanceRoleLabelKey] == role {
		return nil
	}
	before := pod.DeepCopy()
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[InstanceRoleLabelKey] = role
	return r.Patch(ctx, &pod, client.MergeFrom(before))
}
