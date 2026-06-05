/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
// primary that booted as an empty rogue from a stale env. reconcileRoguePrimaries
// reseeds these into clean standbys (#220 clean-rejoin).
const roguePrimaryReason = "rogue-primary"

// aggregateShardStatus 는 단일 shard 의 모든 Pod (StatefulSet replicas) 를 list 한 뒤
// 각 Pod 의 statusapi annotation 을 parse 해 ShardStatus 를 합성한다 (RFC 0006 R2).
//
// Selection: app.kubernetes.io/instance=<cluster> + postgres.keiailab.io/shard=<ord>.
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
	logger := log.FromContext(ctx).WithValues("shard", ord)
	out := postgresv1alpha1.ShardStatus{
		Name:    fmt.Sprintf("shard-%d", ord),
		Ordinal: ord,
	}

	sel := labels.SelectorFromSet(labels.Set(SelectorLabels(cluster.Name, "shard", ord)))
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
	fencedPVC := map[string]bool{}
	{
		var pvcs corev1.PersistentVolumeClaimList
		if err := c.List(ctx, &pvcs, &client.ListOptions{Namespace: cluster.Namespace}); err != nil {
			logger.V(1).Info("aggregateShardStatus: pvc list for fence check failed", "error", err)
		} else {
			for i := range pvcs.Items {
				if pvcs.Items[i].Labels[fencing.FenceLabelKey] == fencing.FenceLabelValue {
					fencedPVC[pvcs.Items[i].Name] = true
				}
			}
		}
	}

	// #220: detect whether a *promoted* primary (carries the operator-promote
	// marker) exists. If so, any other Primary-reporting pod is a rogue old primary
	// that booted empty from a stale env — never the shard primary, and flagged for
	// reseed. This protects the real (data-holding) primary from ever being reseeded.
	hasPromotedPrimary := false
	for i := range pods.Items {
		if st, ok := parsePodStatus(&pods.Items[i]); ok &&
			st.Role == statusapi.RolePrimary && st.Promoted &&
			!fencedPVC["data-"+pods.Items[i].Name] {
			hasPromotedPrimary = true
			break
		}
	}

	now := time.Now().UTC()
	var primaryCandidate *postgresv1alpha1.ShardEndpoint
	var replicas []postgresv1alpha1.ShardEndpoint

	for i := range pods.Items {
		pod := &pods.Items[i]
		st, ok := parsePodStatus(pod)
		if !ok {
			// annotation 부재 — Pod 부팅 직후. fallback 표기.
			ep := postgresv1alpha1.ShardEndpoint{
				Pod:      pod.Name,
				Endpoint: defaultEndpoint(pod.Name, svcName, cluster.Namespace),
				Ready:    false,
			}
			replicas = append(replicas, ep)
			continue
		}
		ready := st.Ready
		if st.IsStale(now, statusStaleThresh) {
			logger.Info("instance status stale (heartbeat lost)",
				"pod", pod.Name, "lastUpdate", st.LastUpdate)
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
		switch {
		case st.Role == statusapi.RolePrimary && fencedPVC["data-"+pod.Name]:
			// #220: fenced known-failed primary (e.g. a returning old primary that
			// self-reports Primary before its fence stops it) — never the shard primary.
			logger.Info("ignoring Primary self-report from fenced member", "pod", pod.Name)
			replicas = append(replicas, ep)
		case st.Role == statusapi.RolePrimary && !st.Promoted && hasPromotedPrimary:
			// #220: a Primary-reporting pod without the promoted marker, while a
			// promoted primary exists, is a rogue old primary that booted empty from a
			// stale env. Never the shard primary; flag Ready=false + reason so
			// reconcileRoguePrimaries reseeds it into a clean standby.
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
			}
		default:
			replicas = append(replicas, ep)
		}
	}

	out.Primary = primaryCandidate
	out.Replicas = replicas
	return out
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
