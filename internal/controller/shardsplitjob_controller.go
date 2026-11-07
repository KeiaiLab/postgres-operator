/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=postgresclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps;services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

const (
	// splitJobPoll 은 copy·CDC·cleanup Job 과 promote 선결 조건을 다시 볼 간격이다.
	splitJobPoll = 3 * time.Second
	// splitBootstrapPoll 은 target pod Ready 를 다시 볼 간격이다.
	splitBootstrapPoll = 5 * time.Second
)

// phaseStep 은 phase 작업 뒤 Reconcile 이 할 일이다.
type phaseStep int

const (
	// stepAdvance 는 nextPhase 전이로 진행한다.
	stepAdvance phaseStep = iota
	// stepReturn 은 phase 작업이 낸 result·err 를 그대로 반환한다.
	stepReturn
)

// splitJobWork 는 완료 여부·실패 사유를 내는 Job 기반 phase 작업이다.
type splitJobWork func(context.Context, *postgresv1alpha1.ShardSplitJob) (bool, string, error)

// Reconcile 은 ShardSplitJob 의 다음 phase 로 한 단계 전이한다 (즉시 requeue 로 진행).
func (r *ShardSplitJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var ssj postgresv1alpha1.ShardSplitJob
	if err := r.Get(ctx, req.NamespacedName, &ssj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch ssj.Status.Phase {
	case postgresv1alpha1.ShardSplitPhaseCompleted:
		return ctrl.Result{}, nil
	case postgresv1alpha1.ShardSplitPhaseFailed,
		postgresv1alpha1.ShardSplitPhaseAborted:
		if ssj.Spec.Online {
			return r.reconcileTerminalAbortCleanup(ctx, &ssj)
		}
		return ctrl.Result{}, nil
	}

	// (reshard-feature-sync) 미지원 spec 차단이 *먼저* — 구 CRD에서 생성됐거나 admission
	// 적용 전에 진행 중이던 요청은 승인 게이트·phase 부수효과보다 먼저 Failed 처리한다.
	// Failed/Aborted의 안전 cleanup은 위 terminal 분기에서 계속 허용한다.
	if reason := unsupportedSplitSpecReason(&ssj); reason != "" {
		markSplitFailed(&ssj, reason)
		if err := r.Status().Update(ctx, &ssj); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// AutoSplit 승인 게이트: 자동 생성 job 이 requireApproval(approval=required)이면
	// 운영자 승인 annotation(autosplit-approved=true) 전까지 Pending 을 유지한다 —
	// 비가역 데이터 이동 전 확인(AutoSplitSpec.RequireApproval production safety).
	// 승인 annotation 편집이 재-reconcile 을 트리거해 SnapshotWAL 로 진행한다.
	if (ssj.Status.Phase == "" || ssj.Status.Phase == postgresv1alpha1.ShardSplitPhasePending) &&
		autoSplitHoldForApproval(&ssj) {
		return r.holdForApproval(ctx, &ssj)
	}

	// phase 별 부수효과 — 완료 전이면 여기서 requeue·Failed 로 반환한다.
	if res, step, err := r.runPhaseWork(ctx, &ssj); step == stepReturn {
		return res, err
	}

	return r.advancePhase(ctx, &ssj)
}

// markSplitFailed 는 job 을 사유와 함께 Failed 종료 상태로 표시한다(저장은 호출자).
func markSplitFailed(ssj *postgresv1alpha1.ShardSplitJob, reason string) {
	ssj.Status.Phase = postgresv1alpha1.ShardSplitPhaseFailed
	ssj.Status.FailureReason = reason
	now := metav1.Now()
	ssj.Status.CompletedAt = &now
	ssj.Status.ObservedGeneration = ssj.Generation
}

// failPhase 는 phase 작업 실패를 Failed 로 저장한다. 저장 오류는 무시한다 — 다음
// reconcile 이 같은 실패를 다시 만난다.
func (r *ShardSplitJobReconciler) failPhase(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob, reason string) (ctrl.Result, phaseStep, error) {
	markSplitFailed(ssj, reason)
	_ = r.Status().Update(ctx, ssj)
	return ctrl.Result{}, stepReturn, nil
}

// holdForApproval 은 승인 대기 job 을 Pending 으로 기록하고 멈춘다.
func (r *ShardSplitJobReconciler) holdForApproval(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (ctrl.Result, error) {
	if ssj.Status.Phase == "" {
		ssj.Status.Phase = postgresv1alpha1.ShardSplitPhasePending
		ssj.Status.ObservedGeneration = ssj.Generation
		if err := r.Status().Update(ctx, ssj); err != nil {
			return ctrl.Result{}, err
		}
	}
	log.FromContext(ctx).Info("ShardSplitJob 승인 대기 (autosplit requireApproval)", "name", ssj.Name)
	return ctrl.Result{}, nil
}

// runPhaseWork 는 현재 phase 의 부수효과를 수행한다. stepAdvance 면 nextPhase 전이로 간다.
func (r *ShardSplitJobReconciler) runPhaseWork(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (ctrl.Result, phaseStep, error) {
	switch ssj.Status.Phase {
	case postgresv1alpha1.ShardSplitPhaseBootstrap:
		return r.bootstrapPhase(ctx, ssj)

	// InitialCopy phase: source→target 데이터 복사 Job 을 띄우고 완료를 기다린다. 완료
	// 전엔 phase 를 전이하지 않고 requeue 한다(가역 — Job 실패 시 Failed, target drop 으로
	// rollback). 실 데이터 이동이 끝나야 CDCCatchup/Cutover 가 의미를 가진다.
	case postgresv1alpha1.ShardSplitPhaseInitialCopy:
		return r.jobPhase(ctx, ssj, r.reconcileInitialCopy)

	// CDCCatchup phase (online): 논리복제로 라이브 쓰기를 따라잡고, 거의 catch-up 되면
	// write-block 을 켠 뒤 최종 drain·정리한다(reconcileCDC). 완료 전엔 전이하지 않는다.
	// offline 모드는 no-op(nextPhase 가 즉시 Cutover 로).
	case postgresv1alpha1.ShardSplitPhaseCDCCatchup:
		if !ssj.Spec.Online {
			return ctrl.Result{}, stepAdvance, nil
		}
		return r.jobPhase(ctx, ssj, r.reconcileCDC)

	// Cutover phase: 라우팅 전환 직전 write-block 을 켠다(라우터가 쓰기 거부, 읽기는 통과) —
	// RoutingUpdate 가 ranges 를 flip 하고 동시에 write-block 을 해제한다. forward-only(비가역)는
	// nextPhase 가 Failed 로 막으므로 write-block 을 켜지 않는다.
	case postgresv1alpha1.ShardSplitPhaseCutover:
		if ssj.Spec.AllowForwardOnly {
			return ctrl.Result{}, stepAdvance, nil
		}
		if err := r.setWriteBlock(ctx, ssj, true); err != nil {
			return ctrl.Result{}, stepReturn, err
		}

	// RoutingUpdate phase: 실 routing 전환 — 해당 keyspace 의 ShardRange CRD 의 ranges 를
	// target 으로 갱신한다. *가역* cutover 결과(rollback=ShardRange 원복, §6 L3 안전망).
	// 사용자 비가역 승인(2026-06-04) 하에 진입. write-block(운영 write freeze) + CDC
	// logical replication 은 운영 cluster 연동 후속(별 트랙).
	case postgresv1alpha1.ShardSplitPhaseRoutingUpdate:
		if err := r.applyRouting(ctx, ssj); err != nil {
			return r.failPhase(ctx, ssj, err.Error())
		}

	// Cleanup phase: cutover·라우팅 전환 후 source 에서 이동분(각 target 키)을 삭제하는 Job 을
	// 띄우고 완료를 기다린다. 라우팅이 이미 target 으로 갔으므로 안전(이동분은 더는 source 가
	// 서빙하지 않음). 완료 전엔 전이하지 않는다.
	case postgresv1alpha1.ShardSplitPhaseCleanup:
		return r.jobPhase(ctx, ssj, r.reconcileCleanup)

	case postgresv1alpha1.ShardSplitPhasePromote:
		return r.promotePhase(ctx, ssj)
	}
	return ctrl.Result{}, stepAdvance, nil
}

// bootstrapPhase 는 target shard 의 실 K8s 자원 (ConfigMap + headless Service +
// StatefulSet) 을 격리 식별로 생성한다 (ADR-0027). 가역 (rollback = target 자원
// delete). 실패 시 Failed 로 종료 — 다음 phase (InitialCopy) 가 target 부재로
// 진행 불가하므로 fail-fast.
func (r *ShardSplitJobReconciler) bootstrapPhase(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (ctrl.Result, phaseStep, error) {
	if err := r.reconcileBootstrapTargets(ctx, ssj); err != nil {
		return r.failPhase(ctx, ssj, err.Error())
	}

	// #B-28: target StatefulSet 을 upsert 했다고 곧장 InitialCopy 로 넘어가면, copy Job 이
	// target pod 부팅(및 headless DNS 등록) 전에 실행돼 "no such host" 로 실패한다(E-1 에서
	// 수동 "target Ready 후 재적용" 으로 우회했던 race). target pod 가 Ready 될 때까지
	// Bootstrap 에 머물며 requeue 한다 — nextPhase 전이 게이트.
	ready, err := r.bootstrapTargetsReady(ctx, ssj)
	if err != nil {
		return ctrl.Result{}, stepReturn, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: splitBootstrapPoll}, stepReturn, nil
	}
	return ctrl.Result{}, stepAdvance, nil
}

// jobPhase 는 Job 기반 phase 작업을 돌린다. 오류는 backoff requeue(ShardRange 부재 등
// 전이 가능), 실패 사유는 Failed, 미완료는 poll requeue, 완료면 전이로 간다.
func (r *ShardSplitJobReconciler) jobPhase(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob, work splitJobWork) (ctrl.Result, phaseStep, error) {
	done, failure, err := work(ctx, ssj)
	if err != nil {
		return ctrl.Result{}, stepReturn, err
	}
	if failure != "" {
		return r.failPhase(ctx, ssj, failure)
	}
	if !done {
		return ctrl.Result{RequeueAfter: splitJobPoll}, stepReturn, nil
	}
	return ctrl.Result{}, stepAdvance, nil
}

// promotePhase 는 선결 조건(source fence·target Ready)이 충족되면 target shard 신원을 채택한다.
func (r *ShardSplitJobReconciler) promotePhase(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (ctrl.Result, phaseStep, error) {
	ready, reason, err := r.promotePreconditionsMet(ctx, ssj)
	if err != nil {
		return ctrl.Result{}, stepReturn, err
	}
	if !ready {
		log.FromContext(ctx).Info("ShardSplitJob Promote precondition not met", "reason", reason)
		return ctrl.Result{RequeueAfter: splitJobPoll}, stepReturn, nil
	}
	if err := r.reconcilePromote(ctx, ssj); err != nil {
		return r.failPhase(ctx, ssj, err.Error())
	}
	return ctrl.Result{}, stepAdvance, nil
}

// advancePhase 는 nextPhase 로 전이해 시각·사유를 기록하고 저장한다. 종료 phase 가
// 아니면 즉시 다음 단계로 requeue 한다.
func (r *ShardSplitJobReconciler) advancePhase(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (ctrl.Result, error) {
	next, failure := r.nextPhase(ssj)
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
	terminal := next == postgresv1alpha1.ShardSplitPhaseCompleted ||
		next == postgresv1alpha1.ShardSplitPhaseFailed
	if terminal {
		now := metav1.Now()
		ssj.Status.CompletedAt = &now
	}
	ssj.Status.Phase = next
	ssj.Status.ObservedGeneration = ssj.Generation

	log.FromContext(ctx).Info("ShardSplitJob phase 전이", "name", ssj.Name, "phase", next)
	if err := r.Status().Update(ctx, ssj); err != nil {
		return ctrl.Result{}, err
	}
	if terminal {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{Requeue: true}, nil
}

// nextPhase 는 현재 phase 로부터 다음 phase 와 (Failed 시) 사유를 반환한다.
// unsupportedSplitSpecReason 은 (reshard-feature-sync) 진행 중이거나 구 CRD 에서 온 요청 중
// 현재 데이터 이동 구현이 다룰 수 없는 형태를 Reconcile 진입 즉시(어떤 phase 부수효과보다 먼저)
// 차단하기 위한 사유를 반환한다. 지원 가능하면 "". nextPhase 의 Pending 게이트와 동일 불변식
// (merge 미구현 / split 은 단일 source) — 여기서 먼저 걸러 in-flight Bootstrap 요청도 fail-closed.
func unsupportedSplitSpecReason(ssj *postgresv1alpha1.ShardSplitJob) string {
	if ssj.Spec.Direction == postgresv1alpha1.ShardSplitDirectionMerge {
		return "merge direction is not implemented"
	}
	if len(ssj.Spec.Sources) != 1 {
		return "split requires exactly one source"
	}
	return ""
}

func (r *ShardSplitJobReconciler) nextPhase(ssj *postgresv1alpha1.ShardSplitJob) (postgresv1alpha1.ShardSplitJobPhase, string) {
	switch ssj.Status.Phase {
	case "", postgresv1alpha1.ShardSplitPhasePending:
		// 현재 데이터 이동 구현은 split 전용이며 source[0]만 복사한다.
		// merge 또는 다중 source를 허용하면 routing update가 복사되지 않은 source까지
		// 제거할 수 있으므로 어떤 부수효과보다 먼저 fail-closed 한다.
		if ssj.Spec.Direction == postgresv1alpha1.ShardSplitDirectionMerge {
			return postgresv1alpha1.ShardSplitPhaseFailed, "merge direction is not implemented"
		}
		if len(ssj.Spec.Sources) != 1 {
			return postgresv1alpha1.ShardSplitPhaseFailed, "split requires exactly one source"
		}
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
		// 실 데이터 이동(복사 Job)은 Reconcile 의 InitialCopy 블록이 완료까지 게이트한 뒤
		// 이 전이에 도달한다(shardsplitjob_copy.go reconcileInitialCopy). 가역.
		return postgresv1alpha1.ShardSplitPhaseCDCCatchup, ""
	case postgresv1alpha1.ShardSplitPhaseCDCCatchup:
		return postgresv1alpha1.ShardSplitPhaseCutover, ""
	case postgresv1alpha1.ShardSplitPhaseCutover:
		// 현재 forward-only 정책은 구현하지 않았으므로 true를 명시적으로 거부한다.
		// false는 routing 진행 허용 조건일 뿐 자동 rollback 보장이 아니다.
		if ssj.Spec.AllowForwardOnly {
			return postgresv1alpha1.ShardSplitPhaseFailed,
				"allowForwardOnly=true is not implemented"
		}
		return postgresv1alpha1.ShardSplitPhaseRoutingUpdate, ""
	case postgresv1alpha1.ShardSplitPhaseRoutingUpdate:
		return postgresv1alpha1.ShardSplitPhaseCleanup, ""
	case postgresv1alpha1.ShardSplitPhaseCleanup:
		return postgresv1alpha1.ShardSplitPhasePromote, ""
	case postgresv1alpha1.ShardSplitPhasePromote:
		return postgresv1alpha1.ShardSplitPhaseCompleted, ""
	}
	return ssj.Status.Phase, ""
}

func (r *ShardSplitJobReconciler) reconcilePromote(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) error {
	for i := range ssj.Spec.Targets {
		shardID := ssj.Spec.Targets[i].ShardID
		if err := r.adoptTargetShardIdentity(ctx, ssj.Namespace, ssj.Spec.Cluster, shardID); err != nil {
			return fmt.Errorf("adopt target shard %q identity: %w", shardID, err)
		}
	}
	return nil
}

func (r *ShardSplitJobReconciler) promotePreconditionsMet(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (bool, string, error) {
	active, err := r.activeShardRangeIDs(ctx, ssj)
	if err != nil {
		return false, "", err
	}
	for _, source := range ssj.Spec.Sources {
		if _, ok := active[source]; ok {
			return false, fmt.Sprintf("source shard %q is still active in ShardRange", source), nil
		}
	}
	// P-B.6 명시적 fence gate (ADR-0029 §승격 메커니즘 2): source ordinal shard 가
	// 아직 *운영 관측*(status.shards 의 Ready primary)에 잡혀 있으면 target adopt 를
	// 보류한다. ShardRange flip(active set 제외)만으로는 cluster reconciler 가 source STS
	// 를 scale-0 하고 status 에서 제외하기 전 짧은 창이 있고, 그 사이 source·target 이
	// 같은 shard-id 로 동시 관측되면 aggregate_status 가 primary 2개(split-brain)로
	// 오판한다(#220-class). status 관측 제외를 명시 확인해 fence-vs-adopt race 를 닫는다.
	if fenced, reason, err := r.sourceObservationExcluded(ctx, ssj); err != nil {
		return false, "", err
	} else if !fenced {
		return false, reason, nil
	}
	for i := range ssj.Spec.Targets {
		shardID := ssj.Spec.Targets[i].ShardID
		if _, ok := active[shardID]; !ok {
			return false, fmt.Sprintf("target shard %q is not active in ShardRange", shardID), nil
		}
		ready, reason, err := r.targetShardReadyForPromote(ctx, ssj.Namespace, ssj.Spec.Cluster, shardID)
		if err != nil {
			return false, "", err
		}
		if !ready {
			return false, reason, nil
		}
	}
	return true, "", nil
}

// sourceObservationExcluded 는 각 source shard 가 cluster 의 운영 관측(status.shards 의
// Ready primary)에서 제외되었는지 확인한다(P-B.6 fence gate). source 가 아직 Ready
// primary 로 관측되면 target adopt 를 보류해 source·target 동시 관측(#220-class
// split-brain) 을 막는다. PostgresCluster CR 부재 시 관측 자체가 없으므로 fence 충족
// (true) — 격리 테스트 및 cluster 삭제 경로에서 안전.
func (r *ShardSplitJobReconciler) sourceObservationExcluded(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (bool, string, error) {
	var cluster postgresv1alpha1.PostgresCluster
	if err := r.Get(ctx, client.ObjectKey{Namespace: ssj.Namespace, Name: ssj.Spec.Cluster}, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return true, "", nil
		}
		return false, "", fmt.Errorf("get cluster for promote fence: %w", err)
	}
	for _, source := range ssj.Spec.Sources {
		for i := range cluster.Status.Shards {
			s := &cluster.Status.Shards[i]
			if s.Name == source && s.Primary != nil && s.Primary.Ready {
				return false, fmt.Sprintf("source shard %q still observed with a Ready primary (fence pending)", source), nil
			}
		}
	}
	return true, "", nil
}

func (r *ShardSplitJobReconciler) activeShardRangeIDs(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (map[string]struct{}, error) {
	var list postgresv1alpha1.ShardRangeList
	if err := r.List(ctx, &list, client.InNamespace(ssj.Namespace)); err != nil {
		return nil, fmt.Errorf("list ShardRange for promote precondition: %w", err)
	}
	active := map[string]struct{}{}
	matched := false
	for i := range list.Items {
		sr := &list.Items[i]
		if sr.Spec.Cluster != ssj.Spec.Cluster || sr.Spec.Keyspace != ssj.Spec.Keyspace {
			continue
		}
		matched = true
		for j := range sr.Spec.Ranges {
			shardID := sr.Spec.Ranges[j].Shard
			if shardID == "" {
				continue
			}
			active[shardID] = struct{}{}
		}
	}
	if !matched {
		return nil, fmt.Errorf("no ShardRange for cluster=%s keyspace=%s", ssj.Spec.Cluster, ssj.Spec.Keyspace)
	}
	return active, nil
}

// bootstrapTargetsReady 는 #B-28 — 모든 target shard 에 Ready(PodRunning + Ready condition)
// 인 pod 가 하나 이상 있는지 본다. copy Job 실행(InitialCopy) 전 게이트로, target pod 부팅 +
// headless DNS 등록이 끝났음을 보장해 "no such host" copy 실패를 막는다. targetShardReadyForPromote
// 와 같은 readiness 판정(podReadyForPromote)을 재사용한다.
func (r *ShardSplitJobReconciler) bootstrapTargetsReady(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (bool, error) {
	for i := range ssj.Spec.Targets {
		shardID := ssj.Spec.Targets[i].ShardID
		ready, _, err := r.targetShardReadyForPromote(ctx, ssj.Namespace, ssj.Spec.Cluster, shardID)
		if err != nil {
			return false, err
		}
		if !ready {
			return false, nil
		}
	}
	return true, nil
}

func (r *ShardSplitJobReconciler) targetShardReadyForPromote(ctx context.Context, namespace, cluster, shardID string) (bool, string, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(namespace),
		client.MatchingLabels(ReshardTargetSelectorLabels(cluster, shardID)),
	); err != nil {
		return false, "", fmt.Errorf("list target pods for promote precondition: %w", err)
	}
	if len(pods.Items) == 0 {
		return false, fmt.Sprintf("target shard %q has no pods", shardID), nil
	}
	for i := range pods.Items {
		if podReadyForPromote(&pods.Items[i]) {
			return true, "", nil
		}
	}
	return false, fmt.Sprintf("target shard %q has no Ready pods", shardID), nil
}

func podReadyForPromote(pod *corev1.Pod) bool {
	if pod == nil || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodReady {
			return pod.Status.Conditions[i].Status == corev1.ConditionTrue
		}
	}
	return false
}

func (r *ShardSplitJobReconciler) adoptTargetShardIdentity(ctx context.Context, namespace, cluster, shardID string) error {
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: TargetShardStatefulSetName(cluster, shardID)}, &sts); err != nil {
		return err
	}
	stsBefore := sts.DeepCopy()
	ensureLabel(&sts.Labels, ShardIDLabelKey, shardID)
	ensureLabel(&sts.Spec.Template.Labels, ShardIDLabelKey, shardID)
	if err := r.Patch(ctx, &sts, client.MergeFrom(stsBefore)); err != nil {
		return fmt.Errorf("patch target StatefulSet %q: %w", sts.Name, err)
	}

	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(namespace),
		client.MatchingLabels(ReshardTargetSelectorLabels(cluster, shardID)),
	); err != nil {
		return fmt.Errorf("list target pods: %w", err)
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Labels[ShardIDLabelKey] == shardID {
			continue
		}
		podBefore := pod.DeepCopy()
		ensureLabel(&pod.Labels, ShardIDLabelKey, shardID)
		if err := r.Patch(ctx, pod, client.MergeFrom(podBefore)); err != nil {
			return fmt.Errorf("patch target pod %q: %w", pod.Name, err)
		}
	}
	return nil
}

func ensureLabel(labels *map[string]string, key, value string) {
	if *labels == nil {
		*labels = map[string]string{}
	}
	(*labels)[key] = value
}

// flattenTargetRanges 는 모든 target shard 의 키 범위를 하나의 slice 로 모은다.
func flattenTargetRanges(targets []postgresv1alpha1.ShardSplitTarget) []postgresv1alpha1.ShardRangeEntry {
	var out []postgresv1alpha1.ShardRangeEntry
	for _, t := range targets {
		out = append(out, t.Ranges...)
	}
	return out
}

// mergeSplitRanges 는 기존 ranges 에서 *source shard 의 range 만* 제거하고 target ranges 를
// 더한다 — split 과 무관한 shard 의 range 는 그대로 보존한다.
//
// B-18 (4노드 라이브 실측 2026-07-14): 기존 구현은 `sr.Spec.Ranges = flattenTargetRanges(...)`
// 로 **전체 ranges 를 target 것으로 대체**했다. 그 결과 shard-1 하나를 분할했을 뿐인데
// shard-0 의 range(0x00000000~0x7fffffff)가 통째로 사라져 shard-0 의 데이터가 라우팅 불가가
// 되고(PostgresCluster.status.shards 에서도 제거 → STS 0/0), Cleanup Job 은 이미 사라진
// source 에 접속하려다 실패했다. split 은 *부분 갱신*이어야 한다.
func mergeSplitRanges(existing []postgresv1alpha1.ShardRangeEntry, sources []string, targets []postgresv1alpha1.ShardSplitTarget) []postgresv1alpha1.ShardRangeEntry {
	removed := make(map[string]bool, len(sources))
	for _, s := range sources {
		removed[s] = true
	}
	out := make([]postgresv1alpha1.ShardRangeEntry, 0, len(existing)+len(targets))
	for _, e := range existing {
		if !removed[e.Shard] {
			out = append(out, e) // split 과 무관한 shard — 보존.
		}
	}
	return append(out, flattenTargetRanges(targets)...)
}

// applyRouting 은 ShardSplitJob 의 cluster/keyspace 에 해당하는 ShardRange 에서 *source
// shard 의 range 를 target ranges 로 치환*하여 routing 을 새 shard 로 전환한다 (가역
// cutover — 원본 ShardRange 로 rollback). 다른 shard 의 range 는 건드리지 않는다(B-18).
// split plan 은 Pending phase 에서 ValidateSplitPlan 으로 이미 검증됨.
func (r *ShardSplitJobReconciler) applyRouting(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) error {
	var list postgresv1alpha1.ShardRangeList
	if err := r.List(ctx, &list, client.InNamespace(ssj.Namespace)); err != nil {
		return fmt.Errorf("list ShardRange: %w", err)
	}
	for i := range list.Items {
		sr := &list.Items[i]
		if sr.Spec.Cluster == ssj.Spec.Cluster && sr.Spec.Keyspace == ssj.Spec.Keyspace {
			sr.Spec.Ranges = mergeSplitRanges(sr.Spec.Ranges, ssj.Spec.Sources, ssj.Spec.Targets)
			sr.Spec.WriteBlocked = false // 라우팅 전환 완료 → write-block 해제(쓰기 재개, 이제 새 shard 로).
			if err := r.Update(ctx, sr); err != nil {
				return fmt.Errorf("update ShardRange %s: %w", sr.Name, err)
			}
			return nil
		}
	}
	return fmt.Errorf("no ShardRange for cluster=%s keyspace=%s", ssj.Spec.Cluster, ssj.Spec.Keyspace)
}

// setWriteBlock 은 cluster/keyspace 의 ShardRange 에 write-block 을 설정/해제한다 — Cutover
// 동안 라우터가 쓰기를 거부하게 해(읽기는 통과) 라우팅 전환 중 쓰기 유실을 막는다.
func (r *ShardSplitJobReconciler) setWriteBlock(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob, blocked bool) error {
	var list postgresv1alpha1.ShardRangeList
	if err := r.List(ctx, &list, client.InNamespace(ssj.Namespace)); err != nil {
		return fmt.Errorf("list ShardRange: %w", err)
	}
	for i := range list.Items {
		sr := &list.Items[i]
		if sr.Spec.Cluster == ssj.Spec.Cluster && sr.Spec.Keyspace == ssj.Spec.Keyspace {
			if sr.Spec.WriteBlocked == blocked {
				return nil // 멱등.
			}
			sr.Spec.WriteBlocked = blocked
			if err := r.Update(ctx, sr); err != nil {
				return fmt.Errorf("update ShardRange %s write-block: %w", sr.Name, err)
			}
			return nil
		}
	}
	return fmt.Errorf("no ShardRange for cluster=%s keyspace=%s", ssj.Spec.Cluster, ssj.Spec.Keyspace)
}

// reconcileBootstrapTargets 는 ShardSplitJob 의 각 target shard 에 대해 격리 식별
// (ADR-0027) 의 ConfigMap + headless Service + StatefulSet 을 멱등 생성한다.
//
// image 는 *기존 source shard* (`<cluster>-shard-0`) 의 컨테이너 image 에서 도출한다
// — resolvePostgresImage 는 PostgresClusterReconciler 의 메서드라 재사용 불가하고,
// source STS 에서 읽으면 라이브 운영 중인 정확한 image (digest pin 포함) 와 정합한다.
// owner 는 PostgresCluster — target 은 resharding 완료 후 영구 shard 로 승격되므로
// SSJ 가 아닌 cluster 수명을 따른다.
func (r *ShardSplitJobReconciler) reconcileBootstrapTargets(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) error {
	var cluster postgresv1alpha1.PostgresCluster
	if err := r.Get(ctx, client.ObjectKey{Namespace: ssj.Namespace, Name: ssj.Spec.Cluster}, &cluster); err != nil {
		return fmt.Errorf("get cluster %q: %w", ssj.Spec.Cluster, err)
	}

	var srcSTS appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: ShardStatefulSetName(cluster.Name, 0)}, &srcSTS); err != nil {
		return fmt.Errorf("get source shard StatefulSet for image: %w", err)
	}
	image := containerImage(&srcSTS, pgContainerName)
	if image == "" {
		return fmt.Errorf("source shard StatefulSet %s 에 %q 컨테이너 image 부재", srcSTS.Name, pgContainerName)
	}
	pgMajor := cluster.Spec.PostgresVersion

	for i := range ssj.Spec.Targets {
		shardID := ssj.Spec.Targets[i].ShardID
		cm := buildTargetShardConfigMap(&cluster, shardID, nil)
		if err := r.upsertTargetResource(ctx, &cluster, cm); err != nil {
			return fmt.Errorf("upsert target %q ConfigMap: %w", shardID, err)
		}
		if err := r.upsertTargetResource(ctx, &cluster, buildTargetHeadlessService(&cluster, shardID)); err != nil {
			return fmt.Errorf("upsert target %q Service: %w", shardID, err)
		}
		sts := buildTargetShardStatefulSet(
			&cluster, shardID, image, pgMajor,
			cluster.Spec.Shards.Storage, cluster.Spec.Shards.Resources,
			cm.Name, postgresConfigHash(cm.Data),
		)
		if err := r.upsertTargetResource(ctx, &cluster, sts); err != nil {
			return fmt.Errorf("upsert target %q StatefulSet: %w", shardID, err)
		}
	}
	return nil
}

// upsertTargetResource 는 desired 자원을 cluster owner 로 멱등 생성/갱신한다
// (PostgresClusterReconciler.upsert 와 동일 패턴 — desired spec 단일 진실).
func (r *ShardSplitJobReconciler) upsertTargetResource(ctx context.Context, owner *postgresv1alpha1.PostgresCluster, desired client.Object) error {
	if err := controllerutil.SetControllerReference(owner, desired, r.Scheme); err != nil {
		return fmt.Errorf("set controller reference: %w", err)
	}
	desiredCopy := desired.DeepCopyObject().(client.Object)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, desired, func() error {
		copySpec(desired, desiredCopy)
		return controllerutil.SetControllerReference(owner, desired, r.Scheme)
	})
	return err
}

// containerImage 는 StatefulSet pod template 에서 주어진 이름의 컨테이너 image 를
// 반환한다 (부재 시 빈 문자열).
func containerImage(sts *appsv1.StatefulSet, name string) string {
	for i := range sts.Spec.Template.Spec.Containers {
		if sts.Spec.Template.Spec.Containers[i].Name == name {
			return sts.Spec.Template.Spec.Containers[i].Image
		}
	}
	return ""
}

// SetupWithManager 는 reconciler 를 manager 에 등록한다.
func (r *ShardSplitJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&postgresv1alpha1.ShardSplitJob{}).
		Owns(&batchv1.Job{}). // InitialCopy 복사 Job 완료 시 재조정.
		Named("shardsplitjob").
		Complete(r)
}
