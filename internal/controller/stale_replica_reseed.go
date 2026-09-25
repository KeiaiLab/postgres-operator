/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

// 재시드는 재구축 엔진(member_rebuild.go, ADR-0031)이 한다 — PVC 는 지우지 않는다(I2). 여기 남은 것은 그
// 엔진이 쓰는 문턱 상수와 죽은 노드에 걸린 파드의 force delete 판정(#226)이다.

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

	// stuckTerminatingForceDeleteThreshold: 이미 Delete가 호출돼(DeletionTimestamp
	// 존재) 이 시간 이상 사라지지 않은 Pod는 kubelet이 응답 불가능한 노드에 hang된
	// 것으로 간주해 force delete(gracePeriodSeconds=0)로 승격한다 (#226). 기본
	// terminationGracePeriodSeconds(30s)+API 지연을 넉넉히 넘기되, reseedCooldown
	// (10분)보다 훨씬 짧게 잡아 다음 reseed 시도에서 반드시 감지되도록 한다.
	stuckTerminatingForceDeleteThreshold = 2 * time.Minute

	// nodeReadDeniedReason 은 Node 를 못 읽었을 때(거부·시한) 클러스터에 남기는
	// Warning 이벤트 사유다.
	nodeReadDeniedReason = "NodeReadDenied"
)

// stuckTerminatingOnDeadNode reports whether pod is safe to escalate to a
// force delete: it must already be Terminating well past the normal grace
// period, AND its node must independently report NotReady. Both signals are
// required — a pod merely slow to terminate on a healthy node must never be
// force-deleted, since the StatefulSet could recreate it on another node
// while the healthy node still holds the volume attached (multi-attach risk,
// see internal/controller/failover/pvc_fence_runbook.go MultiAttach).
func (r *PostgresClusterReconciler) stuckTerminatingOnDeadNode(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster, pod *corev1.Pod, now time.Time) bool {
	if pod.DeletionTimestamp == nil || now.Sub(pod.DeletionTimestamp.Time) < stuckTerminatingForceDeleteThreshold {
		return false
	}
	return r.nodeNotReady(ctx, cluster, pod.Spec.NodeName, now)
}

// nodeNotReady reports whether the named Node reports its Ready condition as
// anything other than True. An unresolvable node (not found, or the Ready
// condition absent) returns false — conservative default, never force-delete
// on an unconfirmed signal.
//
// Node 는 캐시 밖 live GET 이다(cmd/main.go managerClientOptions). 무응답은
// nodeReadTimeout 에서 끊고, 거부·시한도 false 로 두되 지표·이벤트로 드러낸다.
func (r *PostgresClusterReconciler) nodeNotReady(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster, nodeName string, now time.Time) bool {
	if nodeName == "" {
		return false
	}

	readCtx, cancel := context.WithTimeout(ctx, nodeReadTimeout)
	defer cancel()
	var node corev1.Node
	if err := r.Get(readCtx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		if !apierrors.IsNotFound(err) {
			r.recordNodeReadDenied(cluster, nodeName, err, now)
		}
		return false
	}
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == corev1.NodeReady {
			return node.Status.Conditions[i].Status != corev1.ConditionTrue
		}
	}
	return false
}

// recordNodeReadDenied 는 Node 를 못 읽은 사실(거부·시한)을 세고, 클러스터당
// statusStaleThresh 에 한 번 Warning 이벤트로 남긴다 — 매 reconcile 반복 방지.
func (r *PostgresClusterReconciler) recordNodeReadDenied(cluster *postgresv1alpha1.PostgresCluster, nodeName string, err error, now time.Time) {
	MetricAPIReadDenied.WithLabelValues(apiReadKindNode).Inc()
	if !r.nodeReadDeniedDue(cluster.Namespace+"/"+cluster.Name, now) {
		return
	}
	commonsevents.EmitWarningf(r.Recorder, cluster, nodeReadDeniedReason,
		"node %s unreadable (%v); stuck pod force delete withheld", nodeName, err)
}

// nodeReadDeniedDue 는 key 클러스터의 NodeReadDenied 이벤트를 지금 내도 되는지 본다.
func (r *PostgresClusterReconciler) nodeReadDeniedDue(key string, now time.Time) bool {
	r.nodeReadDeniedMu.Lock()
	defer r.nodeReadDeniedMu.Unlock()
	if last, ok := r.nodeReadDeniedAt[key]; ok && now.Sub(last) < statusStaleThresh {
		return false
	}
	if r.nodeReadDeniedAt == nil {
		r.nodeReadDeniedAt = map[string]time.Time{}
	}
	r.nodeReadDeniedAt[key] = now
	return true
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
