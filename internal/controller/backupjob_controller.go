/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package controller의 BackupJob reconciler. RFC 0004 §3 구현 (phase 1 골격).
//
// Phase 1 (본 PR): Spec 검증 + Phase 전이 placeholder. BackupPlugin 실제 호출은
// phase 2(별도 PR)에서. Plugin Registry에 BackupPlugin이 등록되어야 reconcile
// 진행 가능.
//
// Phase 2 (별도 PR): plugin.PerformBackup() 실호출 + Job/Sidecar lifecycle 추적
// + retention 정책 + 결과(BackupResult) → Status 표면화.
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
	BackupJobConditionReady            = "Ready"
)

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=backupjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=backupjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=backupjobs/finalizers,verbs=update

// Reconcile은 BackupJob CR 변화에 반응한다 (RFC 0004 §3 phase 1).
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
	if _, ok := r.Plugins.Backup(bj.Spec.Tool); !ok {
		r.markFailed(&bj, BackupJobReasonPluginNotRegistered,
			"BackupPlugin "+bj.Spec.Tool+" is not registered (RFC 0004 §4 — pgbackrest 1차)")
		return ctrl.Result{}, r.statusUpdate(ctx, &bj)
	}

	// 3. Phase 1 placeholder: Pending 마킹 + ObservedGeneration.
	// Phase 2(별도 PR)에서 plugin.PerformBackup 호출 + Phase 전이.
	if bj.Status.Phase == "" {
		bj.Status.Phase = postgresv1alpha1.BackupJobPending
	}
	bj.Status.ObservedGeneration = bj.Generation
	setBackupJobCondition(&bj, BackupJobConditionReady, metav1.ConditionFalse,
		BackupJobReasonAwaitingInvocation,
		"Phase 1 placeholder — BackupPlugin invocation pending (P1-1 phase 2, RFC 0004 §3)")

	return ctrl.Result{}, r.statusUpdate(ctx, &bj)
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
