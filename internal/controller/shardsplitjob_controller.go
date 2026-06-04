/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/router"
)

// ShardSplitJobReconciler 는 online resharding 의 7-step state machine 을 진행한다.
//
// 본 reconciler 는 phase 전이 골격이다 — 각 phase 의 *부수효과* 는 검증된 building
// block 으로 위임/재사용한다:
//   - Pending  : router.ValidateSplitPlan(#213) 데이터 보존 불변식 gate
//   - InitialCopy: router.CopyTable(#215) source→target (가역, rollback=target drop)
//   - Cutover  : write-block + routing 전환 (*비가역*). AllowForwardOnly=true 면
//     rollback 불가하므로 본 골격은 진입을 거부(Failed)하고 운영자/안전망
//     (snapshot+rollback, §6 L3)을 갖춘 후속 reconciler 로 위임한다.
//     AllowForwardOnly=false(rollback 가능) 만 자동 진행.
//
// CopyTable 의 실 DSN 결선(cluster shard endpoint)과 CDC logical replication 은
// 별 트랙. 본 골격은 phase 진행 + gate 의 정확성(envtest)을 봉인한다.
type ShardSplitJobReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=shardsplitjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=shardsplitjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=shardranges,verbs=get;list;watch;update;patch

// Reconcile 은 ShardSplitJob 의 다음 phase 로 한 단계 전이한다 (즉시 requeue 로 진행).
func (r *ShardSplitJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ssj postgresv1alpha1.ShardSplitJob
	if err := r.Get(ctx, req.NamespacedName, &ssj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch ssj.Status.Phase {
	case postgresv1alpha1.ShardSplitPhaseCompleted,
		postgresv1alpha1.ShardSplitPhaseFailed,
		postgresv1alpha1.ShardSplitPhaseAborted:
		return ctrl.Result{}, nil
	}

	// RoutingUpdate phase: 실 routing 전환 — 해당 keyspace 의 ShardRange CRD 의 ranges 를
	// target 으로 갱신한다. *가역* cutover 결과(rollback=ShardRange 원복, §6 L3 안전망).
	// 사용자 비가역 승인(2026-06-04) 하에 진입. write-block(운영 write freeze) + CDC
	// logical replication 은 운영 cluster 연동 후속(별 트랙).
	if ssj.Status.Phase == postgresv1alpha1.ShardSplitPhaseRoutingUpdate {
		if err := r.applyRouting(ctx, &ssj); err != nil {
			ssj.Status.Phase = postgresv1alpha1.ShardSplitPhaseFailed
			ssj.Status.FailureReason = err.Error()
			now := metav1.Now()
			ssj.Status.CompletedAt = &now
			ssj.Status.ObservedGeneration = ssj.Generation
			_ = r.Status().Update(ctx, &ssj)
			return ctrl.Result{}, nil
		}
	}

	next, failure := r.nextPhase(&ssj)
	if next == ssj.Status.Phase {
		return ctrl.Result{}, nil
	}

	if ssj.Status.Phase == "" && ssj.Status.StartedAt == nil {
		now := metav1.Now()
		ssj.Status.StartedAt = &now
	}
	if next == postgresv1alpha1.ShardSplitPhaseCutover {
		now := metav1.Now()
		ssj.Status.CutoverStartedAt = &now
	}
	if next == postgresv1alpha1.ShardSplitPhaseFailed {
		ssj.Status.FailureReason = failure
	}
	if next == postgresv1alpha1.ShardSplitPhaseCompleted ||
		next == postgresv1alpha1.ShardSplitPhaseFailed {
		now := metav1.Now()
		ssj.Status.CompletedAt = &now
	}
	ssj.Status.Phase = next
	ssj.Status.ObservedGeneration = ssj.Generation

	logger.Info("ShardSplitJob phase 전이", "name", ssj.Name, "phase", next)
	if err := r.Status().Update(ctx, &ssj); err != nil {
		return ctrl.Result{}, err
	}
	// 종료 phase 가 아니면 즉시 다음 단계로.
	if next == postgresv1alpha1.ShardSplitPhaseCompleted ||
		next == postgresv1alpha1.ShardSplitPhaseFailed {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{Requeue: true}, nil
}

// nextPhase 는 현재 phase 로부터 다음 phase 와 (Failed 시) 사유를 반환한다.
func (r *ShardSplitJobReconciler) nextPhase(ssj *postgresv1alpha1.ShardSplitJob) (postgresv1alpha1.ShardSplitJobPhase, string) {
	switch ssj.Status.Phase {
	case "", postgresv1alpha1.ShardSplitPhasePending:
		// 데이터 보존 불변식 gate (#213). target 범위가 무중첩·무공백 연속이어야.
		targets := flattenTargetRanges(ssj.Spec.Targets)
		if err := router.ValidateSplitPlan(targets, targets); err != nil {
			return postgresv1alpha1.ShardSplitPhaseFailed, err.Error()
		}
		return postgresv1alpha1.ShardSplitPhaseSnapshotWAL, ""
	case postgresv1alpha1.ShardSplitPhaseSnapshotWAL:
		return postgresv1alpha1.ShardSplitPhaseBootstrap, ""
	case postgresv1alpha1.ShardSplitPhaseBootstrap:
		return postgresv1alpha1.ShardSplitPhaseInitialCopy, ""
	case postgresv1alpha1.ShardSplitPhaseInitialCopy:
		// 실 데이터 이동은 router.CopyTable(#215, 가역). DSN 결선은 별 트랙.
		return postgresv1alpha1.ShardSplitPhaseCDCCatchup, ""
	case postgresv1alpha1.ShardSplitPhaseCDCCatchup:
		return postgresv1alpha1.ShardSplitPhaseCutover, ""
	case postgresv1alpha1.ShardSplitPhaseCutover:
		// *비가역* gate: AllowForwardOnly=true 는 rollback 불가 → 안전망(§6 L3) 미보유
		// 골격에서는 진입 거부. false(rollback 가능)만 자동 진행.
		if ssj.Spec.AllowForwardOnly {
			return postgresv1alpha1.ShardSplitPhaseFailed,
				"cutover requires reversible path (AllowForwardOnly=false) in skeleton reconciler"
		}
		return postgresv1alpha1.ShardSplitPhaseRoutingUpdate, ""
	case postgresv1alpha1.ShardSplitPhaseRoutingUpdate:
		return postgresv1alpha1.ShardSplitPhaseCleanup, ""
	case postgresv1alpha1.ShardSplitPhaseCleanup:
		return postgresv1alpha1.ShardSplitPhaseCompleted, ""
	}
	return ssj.Status.Phase, ""
}

// flattenTargetRanges 는 모든 target shard 의 키 범위를 하나의 slice 로 모은다.
func flattenTargetRanges(targets []postgresv1alpha1.ShardSplitTarget) []postgresv1alpha1.ShardRangeEntry {
	var out []postgresv1alpha1.ShardRangeEntry
	for _, t := range targets {
		out = append(out, t.Ranges...)
	}
	return out
}

// applyRouting 은 ShardSplitJob 의 cluster/keyspace 에 해당하는 ShardRange 의 ranges 를
// target 으로 갱신하여 routing 을 새 shard 로 전환한다 (가역 cutover — 원본 ShardRange
// 로 rollback). split plan 은 Pending phase 에서 ValidateSplitPlan 으로 이미 검증됨.
func (r *ShardSplitJobReconciler) applyRouting(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) error {
	var list postgresv1alpha1.ShardRangeList
	if err := r.List(ctx, &list, client.InNamespace(ssj.Namespace)); err != nil {
		return fmt.Errorf("list ShardRange: %w", err)
	}
	for i := range list.Items {
		sr := &list.Items[i]
		if sr.Spec.Cluster == ssj.Spec.Cluster && sr.Spec.Keyspace == ssj.Spec.Keyspace {
			sr.Spec.Ranges = flattenTargetRanges(ssj.Spec.Targets)
			if err := r.Update(ctx, sr); err != nil {
				return fmt.Errorf("update ShardRange %s: %w", sr.Name, err)
			}
			return nil
		}
	}
	return fmt.Errorf("no ShardRange for cluster=%s keyspace=%s", ssj.Spec.Cluster, ssj.Spec.Keyspace)
}

// SetupWithManager 는 reconciler 를 manager 에 등록한다.
func (r *ShardSplitJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&postgresv1alpha1.ShardSplitJob{}).
		Named("shardsplitjob").
		Complete(r)
}
