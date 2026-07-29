/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

const (
	// staleStandbyReseedTimeout 은 정상 부팅 + pg_basebackup 시간을 충분히 넘기는
	// 기준. 이 시간 이상 not-ready 인 standby 는 rejoin 실패로 간주한다 (#205).
	staleStandbyReseedTimeout = 8 * time.Minute
	// reseedCooldown 은 동일 standby 의 연속 re-seed 사이 최소 간격. fresh basebackup
	// 이 반복 실패할 때 무한 re-seed 루프를 막는다.
	reseedCooldown = 10 * time.Minute
	// reseedAnnotationPrefix + <pod> 는 cluster annotation 에 마지막 re-seed 시각
	// (RFC3339) 을 기록해 cooldown 을 추적한다.
	reseedAnnotationPrefix = "postgres.keiailab.io/last-reseed-"

	// stuckTerminatingForceDeleteThreshold: 이미 Delete가 호출돼(DeletionTimestamp
	// 존재) 이 시간 이상 사라지지 않은 Pod는 kubelet이 응답 불가능한 노드에 hang된
	// 것으로 간주해 force delete(gracePeriodSeconds=0)로 승격한다 (#226). 기본
	// terminationGracePeriodSeconds(30s)+API 지연을 넉넉히 넘기되, reseedCooldown
	// (10분)보다 훨씬 짧게 잡아 다음 reseed 시도에서 반드시 감지되도록 한다.
	stuckTerminatingForceDeleteThreshold = 2 * time.Minute
)

// reconcileStaleReplicas re-seeds a standby that failed to rejoin after a
// primary restart/failover (#205). A standby that stays not-ready for
// staleStandbyReseedTimeout while its shard already has a ready primary is
// treated as stuck (e.g. startup recovery "waiting for WAL", streaming never
// established). The fix mirrors the verified manual recovery: delete the pod +
// its PVC so the StatefulSet recreates it with a fresh pg_basebackup from the
// current primary.
//
// Conservative by design:
//   - requires a ready primary in the same shard (the replication source);
//   - only acts after staleStandbyReseedTimeout (well past a normal boot);
//   - a per-pod cooldown prevents re-seed loops if basebackup keeps failing.
//
// Best-effort: errors are returned to the caller which logs and continues.
func (r *PostgresClusterReconciler) reconcileStaleReplicas(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shardStatuses []postgresv1alpha1.ShardStatus,
	now time.Time,
) error {
	for _, ss := range shardStatuses {
		if ss.Primary == nil || !ss.Primary.Ready {
			continue // no ready replication source — never re-seed
		}
		for _, rep := range ss.Replicas {
			if rep.Ready || rep.Pod == "" {
				continue
			}
			var pod corev1.Pod
			if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: rep.Pod}, &pod); err != nil {
				continue // pod gone or unreadable — skip
			}
			if now.Sub(pod.CreationTimestamp.Time) < staleStandbyReseedTimeout {
				continue // still within the normal boot/basebackup window
			}
			cdKey := reseedAnnotationPrefix + rep.Pod
			if last := cluster.Annotations[cdKey]; last != "" {
				if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < reseedCooldown {
					continue // re-seeded recently; let it settle
				}
			}
			if err := r.reseedStandby(ctx, cluster, rep.Pod, now); err != nil {
				return err
			}
			before := cluster.DeepCopy()
			if cluster.Annotations == nil {
				cluster.Annotations = map[string]string{}
			}
			cluster.Annotations[cdKey] = now.UTC().Format(time.RFC3339)
			if err := r.Patch(ctx, cluster, client.MergeFrom(before)); err != nil {
				return fmt.Errorf("record re-seed cooldown for %q: %w", rep.Pod, err)
			}
			commonsevents.EmitWarningf(r.Recorder, cluster, "StandbyReseeded",
				"standby %s not ready for %s with a ready primary; re-seeding (delete pod+PVC → fresh pg_basebackup)",
				rep.Pod, staleStandbyReseedTimeout)
		}
	}
	return nil
}

// reseedStandby deletes the standby pod and its data PVC (`data-<pod>`). The
// StatefulSet controller recreates both, and the init container performs a
// fresh pg_basebackup from the current primary. Idempotent w.r.t. absent
// objects.
//
// #226: if podName is already Terminating (a prior reconcile already called
// Delete) and has been stuck that way past stuckTerminatingForceDeleteThreshold
// while its node is independently NotReady, the delete is escalated to a force
// delete (gracePeriodSeconds=0, finalizers cleared) — mirroring the manual
// `kubectl delete pod --grace-period=0 --force` recovery already documented in
// docs/runbooks/pvc-fence.md §3. A normal Delete on a pod whose node's kubelet
// never acknowledges the deletion hangs forever (dead node), which previously
// left reconcileStaleReplicas re-emitting StandbyReseeded every reseedCooldown
// without ever completing. now is threaded in for deterministic tests.
func (r *PostgresClusterReconciler) reseedStandby(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	podName string,
	now time.Time,
) error {
	pvcName := "data-" + podName
	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: pvcName}, &pvc); err == nil {
		if err := r.Delete(ctx, &pvc); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stale standby pvc %q: %w", pvcName, err)
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get stale standby pvc %q: %w", pvcName, err)
	}
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: podName}, &pod); err == nil {
		// 안전 게이트: 죽은 노드 신호(stuckTerminatingOnDeadNode)가 있어도, 대상이
		// 실제로 확인 가능한 정상/서빙 상태라면(다시 Ready가 됐다거나) force delete
		// 로 승격하지 않는다 — podAbsentOrNotReady 재확인이 이 함수 호출 시점의
		// "genuinely down" 불변식을 한 번 더 강제한다(promotionCandidateReadyForExec
		// 와 동일한 방어적 재확인 스타일).
		if r.stuckTerminatingOnDeadNode(ctx, &pod, now) && r.podAbsentOrNotReady(ctx, cluster.Namespace, podName) {
			if err := r.forceDeleteStuckPod(ctx, &pod); err != nil {
				return fmt.Errorf("force delete stuck standby pod %q: %w", podName, err)
			}
		} else if err := r.Delete(ctx, &pod); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stale standby pod %q: %w", podName, err)
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get stale standby pod %q: %w", podName, err)
	}
	return nil
}

// stuckTerminatingOnDeadNode reports whether pod is safe to escalate to a
// force delete: it must already be Terminating well past the normal grace
// period, AND its node must independently report NotReady. Both signals are
// required — a pod merely slow to terminate on a healthy node must never be
// force-deleted, since the StatefulSet could recreate it on another node
// while the healthy node still holds the volume attached (multi-attach risk,
// see internal/controller/failover/pvc_fence_runbook.go MultiAttach).
func (r *PostgresClusterReconciler) stuckTerminatingOnDeadNode(ctx context.Context, pod *corev1.Pod, now time.Time) bool {
	if pod.DeletionTimestamp == nil || now.Sub(pod.DeletionTimestamp.Time) < stuckTerminatingForceDeleteThreshold {
		return false
	}
	return r.nodeNotReady(ctx, pod.Spec.NodeName)
}

// nodeNotReady reports whether the named Node reports its Ready condition as
// anything other than True. An unresolvable node (not found, or the Ready
// condition absent) returns false — conservative default, never force-delete
// on an unconfirmed signal.
func (r *PostgresClusterReconciler) nodeNotReady(ctx context.Context, nodeName string) bool {
	if nodeName == "" {
		return false
	}
	var node corev1.Node
	if err := r.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		return false
	}
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == corev1.NodeReady {
			return node.Status.Conditions[i].Status != corev1.ConditionTrue
		}
	}
	return false
}

// forceDeleteStuckPod mirrors `kubectl delete pod --grace-period=0 --force`:
// it clears any finalizers (so the object can actually leave etcd instead of
// only ever re-stamping DeletionTimestamp) and issues a zero-grace-period
// delete. Idempotent w.r.t. an already-absent pod.
func (r *PostgresClusterReconciler) forceDeleteStuckPod(ctx context.Context, pod *corev1.Pod) error {
	if len(pod.Finalizers) > 0 {
		before := pod.DeepCopy()
		pod.Finalizers = nil
		if err := r.Patch(ctx, pod, client.MergeFrom(before)); err != nil {
			return fmt.Errorf("clear finalizers on stuck pod %q: %w", pod.Name, err)
		}
	}
	if err := r.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("force delete pod %q: %w", pod.Name, err)
	}
	return nil
}

// reconcileRoguePrimaries reseeds any shard member flagged as a rogue primary
// (reports Primary but lacks the operator-promote marker while a real promoted
// primary exists — see aggregateShardStatus). Reseeding deletes the rogue's
// pod+PVC so the StatefulSet recreates it and the init container performs a fresh
// pg_basebackup from the current promoted primary, turning the rogue into a clean
// standby. The promoted (data-holding) primary is never flagged, so its data is
// never at risk (#220 clean-rejoin).
func (r *PostgresClusterReconciler) reconcileRoguePrimaries(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
	now time.Time,
) error {
	logger := log.FromContext(ctx)
	for i := range shards {
		shard := &shards[i]
		// Only act once a legitimate (ready) primary is established for the shard.
		if shard.Primary == nil || !shard.Primary.Ready {
			continue
		}
		for j := range shard.Replicas {
			rep := &shard.Replicas[j]
			if rep.Reason != roguePrimaryReason || rep.Pod == "" || rep.Pod == shard.Primary.Pod {
				continue
			}
			// #220 후속: reseed 는 pod+PVC 삭제/재생성이라 파괴적이다. reconcileStaleReplicas
			// 와 동일한 per-pod backoff 를 적용 — reseedCooldown 내 동일 pod 재reseed 를 막아
			// pg_basebackup 반복 실패 시 무한 삭제 루프를 차단하고, 매 reseed 를 Warning
			// Event 로 감사한다(원 사고: rogue reseed 경로에 backoff·감사 부재).
			cdKey := reseedAnnotationPrefix + rep.Pod
			if last := cluster.Annotations[cdKey]; last != "" {
				if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < reseedCooldown {
					continue // reseeded recently; let it settle
				}
			}
			logger.Info("reseeding rogue primary into clean standby",
				"pod", rep.Pod, "primary", shard.Primary.Pod)
			if err := r.reseedStandby(ctx, cluster, rep.Pod, time.Now()); err != nil {
				return fmt.Errorf("reseed rogue primary %q: %w", rep.Pod, err)
			}
			before := cluster.DeepCopy()
			if cluster.Annotations == nil {
				cluster.Annotations = map[string]string{}
			}
			cluster.Annotations[cdKey] = now.UTC().Format(time.RFC3339)
			if err := r.Patch(ctx, cluster, client.MergeFrom(before)); err != nil {
				return fmt.Errorf("record rogue-primary reseed cooldown for %q: %w", rep.Pod, err)
			}
			commonsevents.EmitWarningf(r.Recorder, cluster, "RoguePrimaryReseeded",
				"rogue primary %s (no operator-promote marker while %s is the promoted primary) reseeded into a clean standby (delete pod+PVC → fresh pg_basebackup); backoff %s",
				rep.Pod, shard.Primary.Pod, reseedCooldown)
		}
	}
	return nil
}
