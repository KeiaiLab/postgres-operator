/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// statusStaleThresh 는 instance manager 의 reporter 주기 (5s) 보다 충분히 큰
// staleness 기준. 본 thresh 초과 시 Pod heartbeat 끊김으로 간주 (failover 준비 신호).
const statusStaleThresh = 30 * time.Second

// roguePrimaryReason flags a ShardEndpoint that reports Primary but lacks the
// operator-promote marker while a promoted primary exists — a returning old
// primary that booted as an empty rogue from a stale env (#220 clean-rejoin).
// 재구축 엔진이 hold 한 뒤 스냅숏을 떠 재시드한다(Rogue, ADR-0031).
const roguePrimaryReason = "rogue-primary"

// fencedMemberReason marks a shard member whose PVC is fenced. Fenced members
// must not be promotion candidates even if their last instance-status heartbeat
// still says Ready=true.
const fencedMemberReason = "fenced"

// fencedPrimaryReason 은 fence 된 PVC 로 primary 를 보고하는 멤버다 — 밀려난 옛 primary 라 재구축 엔진이
// hold 한 뒤 스냅숏을 떠 되감기로 합류시킨다(FencedPrimary, ADR-0031).
const fencedPrimaryReason = "fenced-primary"

// podNotReadyReason marks a member whose instance-status heartbeat still says
// Ready=true but the Kubernetes Pod/container is not ready for exec/probes.
const podNotReadyReason = "pod-not-ready"

// aggregateShardStatus 는 단일 shard 의 모든 Pod (StatefulSet replicas) 를 list 한 뒤
// 각 Pod 의 statusapi annotation 을 parse 해 ShardStatus 를 합성한다 (RFC 0006 R2).
//
// Selection: app.kubernetes.io/instance=<cluster> 로 넓게 list 한 뒤
// postgres.keiailab.io/shard=<ord> 또는 postgres.keiailab.io/shard-id=shard-<ord>
// 를 in-code OR 필터링한다. Kubernetes selector 는 OR 를 지원하지 않으므로 Promote
// 전환 중 selector mutation 없이 shard-id additive label 을 관측하려면 이 방식이 필요하다.
// Aggregation 규칙:
//   - Role=primary 이고 not-stale 인 Pod 1 개 → ShardStatus.Primary.
//     (election 합의가 *유일한 leader* 를 보장 — 2개 이상 발견되면 split-brain 신호로
//     log warning + 첫 Pod 선택. 운영자 개입 필요.)
//   - Role=replica/starting/stopping/unknown — ShardStatus.Replicas.
//   - Stale (LastUpdate > 30s 부재) Pod 는 ShardEndpoint.Ready=false 로 강제 + 별도
//     warning log (heartbeat 끊김).
//
// Pod 가 0 개 또는 annotation parse 실패만 → ShardStatus.Primary = nil 반환.
// 호출자가 reconcile-time 근사값 (STS readyReplicas 기반) 으로 fallback.
func aggregateShardStatus(
	ctx context.Context,
	c client.Client,
	cluster *postgresv1alpha1.PostgresCluster,
	ord int32,
	svcName string,
) postgresv1alpha1.ShardStatus {
	shardID := ShardIDForOrdinal(ord)
	return aggregateShardStatusMatching(ctx, c, cluster, shardID, ord, svcName, func(pod *corev1.Pod) bool {
		return podMatchesShardIdentity(pod, ord)
	})
}

func aggregateNamedShardStatus(
	ctx context.Context,
	c client.Client,
	cluster *postgresv1alpha1.PostgresCluster,
	shardID string,
	svcName string,
) postgresv1alpha1.ShardStatus {
	return aggregateShardStatusMatching(ctx, c, cluster, shardID, -1, svcName, func(pod *corev1.Pod) bool {
		return podMatchesNamedShardIdentity(pod, shardID)
	})
}

func aggregateShardStatusMatching(
	ctx context.Context,
	c client.Client,
	cluster *postgresv1alpha1.PostgresCluster,
	shardID string,
	ordinal int32,
	svcName string,
	matches func(*corev1.Pod) bool,
) postgresv1alpha1.ShardStatus {
	logger := log.FromContext(ctx).WithValues("shard", shardID)
	out := postgresv1alpha1.ShardStatus{
		Name:    shardID,
		Ordinal: ordinal,
	}

	sel := labels.SelectorFromSet(statusAggregationSelectorLabels(cluster.Name))
	var pods corev1.PodList
	if err := c.List(ctx, &pods, &client.ListOptions{
		Namespace:     cluster.Namespace,
		LabelSelector: sel,
	}); err != nil {
		logger.V(1).Info("aggregateShardStatus: pod list failed (fallback to STS-time approx)", "error", err)
		return out
	}

	// #220: a fenced member is a known-failed primary. It must never be selected as
	// ShardStatus.Primary — otherwise its stale endpoint propagates into the
	// StatefulSet PRIMARY_ENDPOINT, and on a restart the bootstrap init container
	// restores standby.signal pointing at it, rewinding the real primary's
	// post-failover writes. (A returning old primary briefly self-reports Primary
	// before the fence stops it; this keeps it out of the status primary slot.)
	fencedPVC := fencedPVCNames(log.IntoContext(ctx, logger), c, cluster.Namespace)

	// #220: detect whether a *promoted* primary (carries the operator-promote
	// marker) exists. If so, any other Primary-reporting pod is a rogue old primary
	// that booted empty from a stale env — never the shard primary, and flagged for
	// reseed. This protects the real (data-holding) primary from ever being reseeded.
	hasPromotedPrimary := anyPromotedPrimary(pods.Items, matches, fencedPVC)

	now := time.Now().UTC()
	var primaryCandidate *postgresv1alpha1.ShardEndpoint
	var primarySizeBytes int64 // 선택된 primary 가 보고한 shard DB 크기 (AutoSplit 관측).
	var replicas []postgresv1alpha1.ShardEndpoint

	for i := range pods.Items {
		pod := &pods.Items[i]
		if !matches(pod) {
			continue
		}
		st, ok := parsePodStatus(pod)
		if !ok {
			// annotation 부재. 두 경우가 있다:
			//
			//  (a) 일반 shard Pod 부팅 직후 — instance manager 가 아직 status 를 안 붙였다.
			//      곧 붙으므로 replica(미준비)로 표기하고 넘어간다.
			//  (b) **reshard target Pod** — 이 STS 는 instance manager 없이 PG 만 띄우므로
			//      status annotation 을 *영원히* 발행하지 않는다. 그래서 split 이 끝나도
			//      ShardStatus.Primary 가 계속 비었고, 라우터(PGROUTER_BACKEND=status)가 새
			//      샤드의 백엔드를 해석하지 못해 접속이 끊겼다
			//      (B-19, 4노드 라이브 실측 2026-07-14: `connection to server was lost`).
			//      target 은 단일 인스턴스 primary 가 구조적으로 보장되므로(replica 없음),
			//      Kubernetes readiness 를 근거로 primary 로 인정한다.
			ep := postgresv1alpha1.ShardEndpoint{
				Pod:      pod.Name,
				Endpoint: defaultEndpoint(pod.Name, svcName, cluster.Namespace),
				Ready:    false,
			}
			if pod.Labels[ReshardTargetLabelKey] != "" && !kubernetesPodNotReady(pod) {
				ep.Ready = true
				if primaryCandidate == nil {
					primaryCandidate = &ep
				}
				continue
			}
			replicas = append(replicas, ep)
			continue
		}
		ep := statusEndpoint(logger, pod, st, now, fencedPVC)
		podFenced := fencedPVC["data-"+pod.Name]
		switch {
		case st.Role == statusapi.RolePrimary && podFenced:
			// #220: fenced known-failed primary (e.g. a returning old primary that
			// self-reports Primary before its fence stops it) — never the shard primary.
			// 사유 FencedPrimary 로 재구축 엔진이 hold 한다.
			ep.Reason = fencedPrimaryReason
			logger.Info("ignoring Primary self-report from fenced member", "pod", pod.Name)
			replicas = append(replicas, ep)
		case st.Role == statusapi.RolePrimary && !st.Promoted && hasPromotedPrimary:
			// #220: a Primary-reporting pod without the promoted marker, while a
			// promoted primary exists, is a rogue old primary that booted empty from a
			// stale env. Never the shard primary; flag Ready=false + reason.
			// 재구축 엔진이 hold 한 뒤 재시드한다(Rogue).
			ep.Ready = false
			ep.Reason = roguePrimaryReason
			logger.Info("rogue primary detected (no promoted marker); flagging for reseed", "pod", pod.Name)
			replicas = append(replicas, ep)
		case st.Role == statusapi.RolePrimary:
			if primaryCandidate != nil {
				// split-brain 신호 — election 합의가 깨졌거나 patch race. 첫 후보 유지 + 경고.
				logger.Info("multiple primaries detected (split-brain signal)",
					"first", primaryCandidate.Pod, "second", pod.Name)
				replicas = append(replicas, ep)
			} else {
				p := ep
				primaryCandidate = &p
				primarySizeBytes = maxInt64(0, st.SizeBytes)
			}
		default:
			replicas = append(replicas, ep)
		}
	}

	out.Primary = primaryCandidate
	out.Replicas = replicas
	if primaryCandidate != nil {
		out.SizeBytes = primarySizeBytes
	}
	return out
}

// fencedPVCNames 는 namespace 에서 fence 라벨이 붙은 PVC 이름 집합을 반환한다. 목록
// 실패는 빈 집합(fence 미관측)으로 둔다.
func fencedPVCNames(ctx context.Context, c client.Client, namespace string) map[string]bool {
	fencedPVC := map[string]bool{}
	var pvcs corev1.PersistentVolumeClaimList
	if err := c.List(ctx, &pvcs, &client.ListOptions{Namespace: namespace}); err != nil {
		log.FromContext(ctx).V(1).Info("aggregateShardStatus: pvc list for fence check failed", "error", err)
		return fencedPVC
	}
	for i := range pvcs.Items {
		if pvcs.Items[i].Labels[fencing.FenceLabelKey] == fencing.FenceLabelValue {
			fencedPVC[pvcs.Items[i].Name] = true
		}
	}
	return fencedPVC
}

// anyPromotedPrimary 는 fence 되지 않은 matching pod 중 promote marker 를 단 primary 가
// 있는지 본다.
func anyPromotedPrimary(pods []corev1.Pod, matches func(*corev1.Pod) bool, fencedPVC map[string]bool) bool {
	for i := range pods {
		if !matches(&pods[i]) {
			continue
		}
		if st, ok := parsePodStatus(&pods[i]); ok &&
			st.Role == statusapi.RolePrimary && st.Promoted &&
			!fencedPVC["data-"+pods[i].Name] {
			return true
		}
	}
	return false
}

// statusEndpoint 는 status annotation 이 있는 pod 의 endpoint 를 만든다. heartbeat
// stale · Kubernetes NotReady · fence 는 Ready=false 로 내리고 빈 사유를 채운다.
func statusEndpoint(
	logger logr.Logger,
	pod *corev1.Pod,
	st statusapi.Status,
	now time.Time,
	fencedPVC map[string]bool,
) postgresv1alpha1.ShardEndpoint {
	ready := st.Ready
	if st.IsStale(now, statusStaleThresh) {
		logger.Info("instance status stale (heartbeat lost)",
			"pod", pod.Name, "lastUpdate", st.LastUpdate)
		ready = false
	}
	podNotReady := kubernetesPodNotReady(pod)
	if podNotReady {
		ready = false
	}
	ep := postgresv1alpha1.ShardEndpoint{
		Pod:      pod.Name,
		Endpoint: st.Endpoint,
		Ready:    ready,
		LagBytes: maxInt64(0, st.LagBytes), // -1 (unknown) → 0 표기 (status schema 가 음수 부재).
		Reason:   st.Reason,
		Message:  st.Message,
	}
	if podNotReady {
		if ep.Reason == "" {
			ep.Reason = podNotReadyReason
		}
		if ep.Message == "" {
			ep.Message = "Kubernetes Pod or postgres container is not ready"
		}
	}
	podFenced := fencedPVC["data-"+pod.Name]
	if podFenced {
		ep.Ready = false
		if ep.Reason == "" {
			ep.Reason = fencedMemberReason
		}
		if ep.Message == "" {
			ep.Message = "PVC is fenced; member is excluded from promotion candidates"
		}
	}
	return ep
}

func activeNamedShardStatuses(
	ctx context.Context,
	c client.Client,
	cluster *postgresv1alpha1.PostgresCluster,
) ([]postgresv1alpha1.ShardStatus, bool, error) {
	active, hasTopology, err := activeShardTopology(ctx, c, cluster)
	if err != nil {
		return nil, false, err
	}
	if !hasTopology {
		return nil, true, nil
	}
	ids := make([]string, 0, len(active))
	for id := range active {
		if !isOrdinalShardID(cluster, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, true, nil
	}
	sort.Strings(ids)

	statuses := make([]postgresv1alpha1.ShardStatus, 0, len(ids))
	allReady := true
	for _, shardID := range ids {
		status := aggregateNamedShardStatus(ctx, c, cluster, shardID, TargetShardServiceName(cluster.Name, shardID))
		if status.Primary == nil || !status.Primary.Ready {
			allReady = false
		}
		statuses = append(statuses, status)
	}
	return statuses, allReady, nil
}

func activeShardTopology(
	ctx context.Context,
	c client.Client,
	cluster *postgresv1alpha1.PostgresCluster,
) (map[string]struct{}, bool, error) {
	if cluster == nil || cluster.Spec.ShardingMode != postgresv1alpha1.ShardingModeNative {
		return nil, false, nil
	}
	var ranges postgresv1alpha1.ShardRangeList
	if err := c.List(ctx, &ranges, client.InNamespace(cluster.Namespace)); err != nil {
		return nil, false, fmt.Errorf("list ShardRange for active shard topology: %w", err)
	}
	seen := map[string]struct{}{}
	for i := range ranges.Items {
		sr := &ranges.Items[i]
		if sr.Spec.Cluster != cluster.Name {
			continue
		}
		for j := range sr.Spec.Ranges {
			shardID := sr.Spec.Ranges[j].Shard
			if shardID == "" {
				continue
			}
			seen[shardID] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil, false, nil
	}
	return seen, true, nil
}

func isOrdinalShardID(cluster *postgresv1alpha1.PostgresCluster, shardID string) bool {
	if cluster == nil {
		return false
	}
	for ord := int32(0); ord < cluster.Spec.Shards.InitialCount; ord++ {
		if shardID == ShardIDForOrdinal(ord) {
			return true
		}
	}
	return false
}

func statusAggregationSelectorLabels(cluster string) labels.Set {
	out := labels.Set(SelectorLabels(cluster, "shard", -1))
	delete(out, "app.kubernetes.io/component")
	return out
}

func podMatchesShardIdentity(pod *corev1.Pod, ord int32) bool {
	if pod == nil {
		return false
	}
	if pod.Labels["postgres.keiailab.io/shard"] == fmt.Sprintf("%d", ord) {
		return true
	}
	return pod.Labels[ShardIDLabelKey] == ShardIDForOrdinal(ord)
}

func podMatchesNamedShardIdentity(pod *corev1.Pod, shardID string) bool {
	if pod == nil || shardID == "" {
		return false
	}
	// reshard 의 copy/delete **Job Pod** 도 대상 shard 를 가리키는 reshard-target 라벨을
	// 달고 있다. 이들은 DB 인스턴스가 아니라 *일회성 작업* 이므로 shard 멤버로 세면 안 된다
	// (B-20, 4노드 라이브 실측 2026-07-14: status.shards[shard-1a].replicas 에 copy Job Pod
	// 들이 들어가 primary 선출을 오염시켰다).
	if pod.Labels[jobNameLabelKey] != "" {
		return false
	}
	return pod.Labels[ShardIDLabelKey] == shardID || pod.Labels[ReshardTargetLabelKey] == shardID
}

// jobNameLabelKey 는 batch/v1 Job 이 자기 Pod 에 붙이는 표준 라벨이다.
const jobNameLabelKey = "batch.kubernetes.io/job-name"

func kubernetesPodNotReady(pod *corev1.Pod) bool {
	if pod == nil {
		return true
	}
	if pod.DeletionTimestamp != nil {
		return true
	}
	switch pod.Status.Phase {
	case corev1.PodPending, corev1.PodFailed, corev1.PodSucceeded:
		return true
	}
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodReady {
			return pod.Status.Conditions[i].Status != corev1.ConditionTrue
		}
	}
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == pgContainerName {
			return !pod.Status.ContainerStatuses[i].Ready
		}
	}
	return false
}

// parsePodStatus 는 Pod annotation 에서 statusapi.Status 를 디코드한다.
// 부재 / 깨진 JSON 은 ok=false (호출자가 fallback).
func parsePodStatus(pod *corev1.Pod) (statusapi.Status, bool) {
	if pod.Annotations == nil {
		return statusapi.Status{}, false
	}
	raw, ok := pod.Annotations[statusapi.AnnotationKey]
	if !ok || raw == "" {
		return statusapi.Status{}, false
	}
	var st statusapi.Status
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return statusapi.Status{}, false
	}
	return st, true
}

// defaultEndpoint 는 annotation 부재 시 fallback DNS endpoint 를 만든다.
func defaultEndpoint(podName, svcName, namespace string) string {
	return fmt.Sprintf("%s.%s.%s.svc.cluster.local:%d", podName, svcName, namespace, pgPort)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
