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
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile 은 ShardSplitJob 의 다음 phase 로 한 단계 전이한다 (즉시 requeue 로 진행).
func (r *ShardSplitJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

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

	// RoutingUpdate phase: 실 routing 전환 — 해당 keyspace 의 ShardRange CRD 의 ranges 를
	// target 으로 갱신한다. *가역* cutover 결과(rollback=ShardRange 원복, §6 L3 안전망).
	// 사용자 비가역 승인(2026-06-04) 하에 진입. write-block(운영 write freeze) + CDC
	// logical replication 은 운영 cluster 연동 후속(별 트랙).
	// Bootstrap phase: target shard 의 실 K8s 자원 (ConfigMap + headless Service +
	// StatefulSet) 을 격리 식별로 생성한다 (ADR-0027). 가역 (rollback = target 자원
	// delete). 실패 시 Failed 로 종료 — 다음 phase (InitialCopy) 가 target 부재로
	// 진행 불가하므로 fail-fast.
	if ssj.Status.Phase == postgresv1alpha1.ShardSplitPhaseBootstrap {
		if err := r.reconcileBootstrapTargets(ctx, &ssj); err != nil {
			ssj.Status.Phase = postgresv1alpha1.ShardSplitPhaseFailed
			ssj.Status.FailureReason = err.Error()
			now := metav1.Now()
			ssj.Status.CompletedAt = &now
			ssj.Status.ObservedGeneration = ssj.Generation
			_ = r.Status().Update(ctx, &ssj)
			return ctrl.Result{}, nil
		}
	}

	// InitialCopy phase: source→target 데이터 복사 Job 을 띄우고 완료를 기다린다. 완료
	// 전엔 phase 를 전이하지 않고 requeue 한다(가역 — Job 실패 시 Failed, target drop 으로
	// rollback). 실 데이터 이동이 끝나야 CDCCatchup/Cutover 가 의미를 가진다.
	if ssj.Status.Phase == postgresv1alpha1.ShardSplitPhaseInitialCopy {
		done, failure, err := r.reconcileInitialCopy(ctx, &ssj)
		if err != nil {
			return ctrl.Result{}, err // 전이 가능(ShardRange 부재 등) — backoff requeue.
		}
		if failure != "" {
			ssj.Status.Phase = postgresv1alpha1.ShardSplitPhaseFailed
			ssj.Status.FailureReason = failure
			now := metav1.Now()
			ssj.Status.CompletedAt = &now
			ssj.Status.ObservedGeneration = ssj.Generation
			_ = r.Status().Update(ctx, &ssj)
			return ctrl.Result{}, nil
		}
		if !done {
			return ctrl.Result{RequeueAfter: 3 * time.Second}, nil // 복사 Job 대기.
		}
		// 복사 완료 → 아래 nextPhase 가 CDCCatchup 으로 전이.
	}

	// CDCCatchup phase (online): 논리복제로 라이브 쓰기를 따라잡고, 거의 catch-up 되면
	// write-block 을 켠 뒤 최종 drain·정리한다(reconcileCDC). 완료 전엔 전이하지 않는다.
	// offline 모드는 no-op(아래 nextPhase 가 즉시 Cutover 로).
	if ssj.Status.Phase == postgresv1alpha1.ShardSplitPhaseCDCCatchup && ssj.Spec.Online {
		done, failure, err := r.reconcileCDC(ctx, &ssj)
		if err != nil {
			return ctrl.Result{}, err
		}
		if failure != "" {
			ssj.Status.Phase = postgresv1alpha1.ShardSplitPhaseFailed
			ssj.Status.FailureReason = failure
			now := metav1.Now()
			ssj.Status.CompletedAt = &now
			ssj.Status.ObservedGeneration = ssj.Generation
			_ = r.Status().Update(ctx, &ssj)
			return ctrl.Result{}, nil
		}
		if !done {
			return ctrl.Result{RequeueAfter: 3 * time.Second}, nil // CDC catch-up 대기.
		}
		// CDC 완료(write-block 켜짐) → nextPhase 가 Cutover 로.
	}

	// Cutover phase: 라우팅 전환 직전 write-block 을 켠다(라우터가 쓰기 거부, 읽기는 통과) —
	// RoutingUpdate 가 ranges 를 flip 하고 동시에 write-block 을 해제한다. forward-only(비가역)는
	// nextPhase 가 Failed 로 막으므로 write-block 을 켜지 않는다.
	if ssj.Status.Phase == postgresv1alpha1.ShardSplitPhaseCutover && !ssj.Spec.AllowForwardOnly {
		if err := r.setWriteBlock(ctx, &ssj, true); err != nil {
			return ctrl.Result{}, err
		}
	}

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

	// Cleanup phase: cutover·라우팅 전환 후 source 에서 이동분(각 target 키)을 삭제하는 Job 을
	// 띄우고 완료를 기다린다. 라우팅이 이미 target 으로 갔으므로 안전(이동분은 더는 source 가
	// 서빙하지 않음). 완료 전엔 전이하지 않는다.
	if ssj.Status.Phase == postgresv1alpha1.ShardSplitPhaseCleanup {
		done, failure, err := r.reconcileCleanup(ctx, &ssj)
		if err != nil {
			return ctrl.Result{}, err
		}
		if failure != "" {
			ssj.Status.Phase = postgresv1alpha1.ShardSplitPhaseFailed
			ssj.Status.FailureReason = failure
			now := metav1.Now()
			ssj.Status.CompletedAt = &now
			ssj.Status.ObservedGeneration = ssj.Generation
			_ = r.Status().Update(ctx, &ssj)
			return ctrl.Result{}, nil
		}
		if !done {
			return ctrl.Result{RequeueAfter: 3 * time.Second}, nil // 삭제 Job 대기.
		}
		// 정리 완료 → 아래 nextPhase 가 Completed 로 전이.
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
		// 실 데이터 이동(복사 Job)은 Reconcile 의 InitialCopy 블록이 완료까지 게이트한 뒤
		// 이 전이에 도달한다(shardsplitjob_copy.go reconcileInitialCopy). 가역.
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
