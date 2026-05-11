/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package controller의 BackupJob reconciler. RFC 0004 §3 구현 (phase 2 in-process 동기 호출).
//
// 본 reconciler 의 BackupJob.Phase 전이 모델 (ROADMAP G1 §Backup/Restore):
//
//	""        → 신규 CR. cluster + plugin 검증 통과 후 Pending 으로 전이.
//	Pending   → StartedAt 기록 + Running 으로 전이. 다음 reconcile 에서 plugin 호출.
//	Running   → plugin.PerformBackup 동기 호출. 결과에 따라 Succeeded/Failed.
//	Succeeded → 터미널 (no-op). BackupID/Bytes/EndedAt 보존.
//	Failed    → 터미널 (no-op). 사용자가 새 CR 생성으로 재시도.
//
// 본 단계의 한계 (별도 PR 에서 다룬다):
//   - Job/Sidecar lifecycle 분기 추적 (현재는 단일 in-process 호출).
//   - Retention 정책 적용 (Bytes 기록만, 보존 cleanup 미구현).
//   - PITR 복구 (Type=restore) — Type 검증만, RestorePIT 호출 미통합.
package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/plugin"
)

// nowFunc 는 metav1.Now 의 테스트 주입 지점 — deterministic StartedAt/EndedAt
// 검증을 위해 단위 테스트에서 override.
var nowFunc = func() metav1.Time { return metav1.Now() }

// BackupJobReconciler는 BackupJob CR을 reconcile한다 (RFC 0004 §3).
type BackupJobReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Plugins *plugin.Registry

	// Recorder 는 K8s Event 발행 용 (RFC-0017 §3.4). SetupWithManager 가 주입.
	Recorder events.EventRecorder
}

// BackupJob Conditions reason 상수 (status.go의 SOT 패턴 차용).
const (
	BackupJobReasonAwaitingInvocation  = "AwaitingPluginInvocation"
	BackupJobReasonClusterNotFound     = "ClusterNotFound"
	BackupJobReasonPluginNotRegistered = "PluginNotRegistered"
	BackupJobReasonInvalidSpec         = "InvalidSpec"
	BackupJobReasonBackupInProgress    = "BackupInProgress"
	BackupJobReasonBackupSucceeded     = "BackupSucceeded"
	BackupJobReasonBackupFailed        = "BackupFailed"
	BackupJobConditionReady            = "Ready"
)

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=backupjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=backupjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=backupjobs/finalizers,verbs=update

// Reconcile은 BackupJob CR 변화에 반응한다 (RFC 0004 §3).
//
// 전이 단계는 package doc 의 phase 모델 참조. 한 turn 에서 최대 1 단계 전이만
// 수행하고 requeue 로 다음 단계를 끌어온다 — status update 와 plugin 호출을
// 같은 reconcile 에 묶지 않아 conflict 발생 시 자연스러운 재시도가 일어난다.
func (r *BackupJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("backupjob", req.NamespacedName)

	var bj postgresv1alpha1.BackupJob
	if err := r.Get(ctx, req.NamespacedName, &bj); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to fetch BackupJob")
		return ctrl.Result{}, err
	}

	// 터미널 상태는 reconcile 진행 자체를 skip — 재시도는 새 CR 으로.
	if bj.Status.Phase == postgresv1alpha1.BackupJobSucceeded ||
		bj.Status.Phase == postgresv1alpha1.BackupJobFailed {
		return ctrl.Result{}, nil
	}

	// 1. Spec 검증: 참조 PostgresCluster가 같은 namespace에 존재
	var cluster postgresv1alpha1.PostgresCluster
	clusterKey := client.ObjectKey{Namespace: bj.Namespace, Name: bj.Spec.Cluster.Name}
	if err := r.Get(ctx, clusterKey, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			r.markFailed(&bj, BackupJobReasonClusterNotFound,
				"Referenced PostgresCluster "+bj.Spec.Cluster.Name+" not found in namespace "+bj.Namespace)
			return ctrl.Result{}, r.statusUpdate(ctx, &bj)
		}
		return ctrl.Result{}, err
	}

	// 2. Plugin 등록 여부
	if r.Plugins == nil {
		r.markFailed(&bj, BackupJobReasonPluginNotRegistered,
			"Plugin Registry is not configured (operator misconfiguration)")
		return ctrl.Result{}, r.statusUpdate(ctx, &bj)
	}
	backupPlugin, ok := r.Plugins.Backup(bj.Spec.Tool)
	if !ok {
		r.markFailed(&bj, BackupJobReasonPluginNotRegistered,
			"BackupPlugin "+bj.Spec.Tool+" is not registered (RFC 0004 §4 — pgbackrest 1차)")
		return ctrl.Result{}, r.statusUpdate(ctx, &bj)
	}

	// 3. Phase 전이: "" → Pending → Running → Succeeded/Failed.
	switch bj.Status.Phase {
	case "":
		// 신규 CR. Pending 으로 전이 + requeue.
		bj.Status.Phase = postgresv1alpha1.BackupJobPending
		bj.Status.ObservedGeneration = bj.Generation
		setBackupJobCondition(&bj, BackupJobConditionReady, metav1.ConditionFalse,
			BackupJobReasonAwaitingInvocation,
			"BackupJob accepted — awaiting plugin invocation")
		return ctrl.Result{Requeue: true}, r.statusUpdate(ctx, &bj)

	case postgresv1alpha1.BackupJobPending:
		// Pending → Running. StartedAt 기록 + requeue 로 다음 turn 에서 plugin 호출.
		now := nowFunc()
		bj.Status.Phase = postgresv1alpha1.BackupJobRunning
		bj.Status.StartedAt = &now
		bj.Status.ObservedGeneration = bj.Generation
		setBackupJobCondition(&bj, BackupJobConditionReady, metav1.ConditionFalse,
			BackupJobReasonBackupInProgress,
			"BackupPlugin "+bj.Spec.Tool+" invocation in progress")
		return ctrl.Result{Requeue: true}, r.statusUpdate(ctx, &bj)

	case postgresv1alpha1.BackupJobRunning:
		// plugin.PerformBackup 동기 호출. 결과로 terminal 전이.
		result, err := backupPlugin.PerformBackup(ctx, plugin.ClusterTarget{
			Namespace: bj.Namespace,
			Name:      bj.Spec.Cluster.Name,
		}, plugin.BackupOptions{
			Type:          bj.Spec.Type,
			Repo:          bj.Spec.Repo,
			Labels:        bj.Spec.Labels,
			ExecutionMode: bj.Spec.ExecutionMode,
		})
		endedAt := nowFunc()
		bj.Status.EndedAt = &endedAt
		bj.Status.ObservedGeneration = bj.Generation
		if err != nil {
			bj.Status.Phase = postgresv1alpha1.BackupJobFailed
			setBackupJobCondition(&bj, BackupJobConditionReady, metav1.ConditionFalse,
				BackupJobReasonBackupFailed,
				"BackupPlugin "+bj.Spec.Tool+" failed: "+err.Error())
			if r.Recorder != nil {
				r.Recorder.Eventf(&bj, nil, corev1.EventTypeWarning,
					BackupJobReasonBackupFailed, BackupJobReasonBackupFailed,
					"BackupPlugin %s failed: %v", bj.Spec.Tool, err)
			}
			return ctrl.Result{}, r.statusUpdate(ctx, &bj)
		}
		bj.Status.Phase = postgresv1alpha1.BackupJobSucceeded
		bj.Status.BackupID = result.BackupID
		bj.Status.Bytes = result.Bytes
		setBackupJobCondition(&bj, BackupJobConditionReady, metav1.ConditionTrue,
			BackupJobReasonBackupSucceeded,
			"BackupPlugin "+bj.Spec.Tool+" succeeded: backupID="+result.BackupID)
		if r.Recorder != nil {
			r.Recorder.Eventf(&bj, nil, corev1.EventTypeNormal,
				BackupJobReasonBackupSucceeded, BackupJobReasonBackupSucceeded,
				"BackupPlugin %s succeeded: backupID=%s bytes=%d", bj.Spec.Tool, result.BackupID, result.Bytes)
		}
		return ctrl.Result{}, r.statusUpdate(ctx, &bj)
	}

	// 알 수 없는 phase — defensive (CRD enum 으로 차단되지만 reconciler 측 가드).
	return ctrl.Result{}, nil
}

// markFailed는 BackupJob을 Failed로 마킹한다.
//
// RFC-0017 §3.4: Recorder 가 nil 이 아니면 Warning event 도 발행. SetupWithManager
// 가 자동 주입하므로 nil 가드는 *방어적 안전망* (테스트에서 직접 reconciler 인스턴스
// 생성 시 Recorder 미주입 가능성).
func (r *BackupJobReconciler) markFailed(bj *postgresv1alpha1.BackupJob, reason, message string) {
	bj.Status.Phase = postgresv1alpha1.BackupJobFailed
	bj.Status.ObservedGeneration = bj.Generation
	setBackupJobCondition(bj, BackupJobConditionReady, metav1.ConditionFalse, reason, message)
	if r.Recorder != nil {
		r.Recorder.Eventf(bj, nil, corev1.EventTypeWarning, reason, reason, "%s", message)
	}
}

// statusUpdate는 conflict를 requeue로 처리하는 표준 패턴.
func (r *BackupJobReconciler) statusUpdate(ctx context.Context, bj *postgresv1alpha1.BackupJob) error {
	if err := r.Status().Update(ctx, bj); err != nil {
		if apierrors.IsConflict(err) {
			// reconcile은 곧 재호출되므로 conflict는 정상.
			return nil
		}
		return err
	}
	return nil
}

// setBackupJobCondition은 K8s 표준 meta.SetStatusCondition 패턴을 사용한다
// (status.go의 setCondition과 동일 동작).
func setBackupJobCondition(bj *postgresv1alpha1.BackupJob, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&bj.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: bj.Generation,
	})
}

// SetupWithManager는 본 reconciler를 controller-runtime Manager에 등록한다.
func (r *BackupJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// RFC-0017 §3.4: EventRecorder 자동 주입.
	if r.Recorder == nil {
		// events API 마이그레이션 완료 (RFC-0023 Phase 2, 2026-05-11).
		r.Recorder = mgr.GetEventRecorder("backupjob-controller")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&postgresv1alpha1.BackupJob{}).
		Named("backupjob").
		Complete(r)
}
