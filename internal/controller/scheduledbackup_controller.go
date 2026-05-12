/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package controller 의 ScheduledBackup reconciler.
//
// 본 컨트롤러는 CNPG 의 ScheduledBackup 운영 표면을 본 프로젝트의 atomic
// BackupJob 모델로 이식한다. schedule 한 번은 BackupJob 하나로 추적되며,
// 실패/재시도/보존 정책은 생성된 BackupJob 과 백업 플러그인 계층에서 처리한다.
package controller

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

const (
	ScheduledBackupConditionReady = "Ready"

	ScheduledBackupReasonActive             = "ScheduleActive"
	ScheduledBackupReasonBackupJobCreated   = "BackupJobCreated"
	ScheduledBackupReasonClusterNotFound    = "ClusterNotFound"
	ScheduledBackupReasonConcurrencyBlocked = "ConcurrencyBlocked"
	ScheduledBackupReasonInvalidSchedule    = "InvalidSchedule"
	ScheduledBackupReasonSuspended          = "Suspended"
)

var sixFieldCronParser = cron.NewParser(
	cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

// ScheduledBackupReconciler 는 ScheduledBackup CR 을 reconcile 한다.
type ScheduledBackupReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=scheduledbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=scheduledbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=scheduledbackups/finalizers,verbs=update
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=backupjobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile 은 cron schedule 이 due 되었을 때 BackupJob 을 생성한다.
func (r *ScheduledBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("scheduledbackup", req.NamespacedName)

	var sb postgresv1alpha1.ScheduledBackup
	if err := r.Get(ctx, req.NamespacedName, &sb); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to fetch ScheduledBackup")
		return ctrl.Result{}, err
	}

	schedule, err := sixFieldCronParser.Parse(sb.Spec.Schedule)
	if err != nil {
		r.markNotReady(&sb, ScheduledBackupReasonInvalidSchedule, "Invalid 6-field cron schedule: "+err.Error())
		return ctrl.Result{RequeueAfter: time.Minute}, r.statusUpdate(ctx, &sb)
	}

	var cluster postgresv1alpha1.PostgresCluster
	clusterKey := client.ObjectKey{Namespace: sb.Namespace, Name: sb.Spec.Cluster.Name}
	if err := r.Get(ctx, clusterKey, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			r.markNotReady(&sb, ScheduledBackupReasonClusterNotFound,
				"Referenced PostgresCluster "+sb.Spec.Cluster.Name+" not found in namespace "+sb.Namespace)
			return ctrl.Result{RequeueAfter: time.Minute}, r.statusUpdate(ctx, &sb)
		}
		return ctrl.Result{}, err
	}

	now := nowFunc().UTC()
	if sb.Spec.Suspend {
		r.markNotReady(&sb, ScheduledBackupReasonSuspended, "ScheduledBackup is suspended")
		sb.Status.NextScheduleTime = nil
		sb.Status.ObservedGeneration = sb.Generation
		return ctrl.Result{}, r.statusUpdate(ctx, &sb)
	}

	dueAt, nextAt, due := nextScheduledRun(&sb, schedule, now)
	if !due {
		nextMeta := metav1.NewTime(nextAt)
		sb.Status.NextScheduleTime = &nextMeta
		sb.Status.ObservedGeneration = sb.Generation
		setScheduledBackupCondition(&sb, metav1.ConditionTrue, ScheduledBackupReasonActive,
			"Next BackupJob scheduled at "+nextAt.Format(time.RFC3339))
		return ctrl.Result{RequeueAfter: nextAt.Sub(now)}, r.statusUpdate(ctx, &sb)
	}

	if shouldForbidConcurrent(sb.Spec.ConcurrencyPolicy) {
		active, err := r.hasActiveBackupJob(ctx, &sb)
		if err != nil {
			return ctrl.Result{}, err
		}
		if active {
			nextMeta := metav1.NewTime(nextAt)
			sb.Status.NextScheduleTime = &nextMeta
			sb.Status.ObservedGeneration = sb.Generation
			setScheduledBackupCondition(&sb, metav1.ConditionFalse, ScheduledBackupReasonConcurrencyBlocked,
				"Previous BackupJob is still Pending or Running")
			return ctrl.Result{RequeueAfter: time.Minute}, r.statusUpdate(ctx, &sb)
		}
	}

	jobName := scheduledBackupJobName(sb.Name, dueAt)
	var existing postgresv1alpha1.BackupJob
	err = r.Get(ctx, client.ObjectKey{Namespace: sb.Namespace, Name: jobName}, &existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if apierrors.IsNotFound(err) {
		job := buildBackupJobFromSchedule(&sb, jobName)
		if err := applyScheduledBackupOwner(r.Scheme, &sb, &cluster, job); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
	}

	lastMeta := metav1.NewTime(dueAt)
	nextMeta := metav1.NewTime(nextAt)
	sb.Status.LastScheduleTime = &lastMeta
	sb.Status.LastBackupJobName = jobName
	sb.Status.NextScheduleTime = &nextMeta
	sb.Status.ObservedGeneration = sb.Generation
	setScheduledBackupCondition(&sb, metav1.ConditionTrue, ScheduledBackupReasonBackupJobCreated,
		"Created BackupJob "+jobName)

	requeueAfter := nextAt.Sub(now)
	requeueAfter = max(requeueAfter, time.Second)
	return ctrl.Result{RequeueAfter: requeueAfter}, r.statusUpdate(ctx, &sb)
}

func nextScheduledRun(
	sb *postgresv1alpha1.ScheduledBackup,
	schedule cron.Schedule,
	now time.Time,
) (dueAt time.Time, nextAt time.Time, due bool) {
	if sb.Spec.Immediate && sb.Status.LastScheduleTime == nil {
		dueAt = now
		return dueAt, schedule.Next(now), true
	}

	anchor := sb.CreationTimestamp.UTC()
	if sb.Status.LastScheduleTime != nil {
		anchor = sb.Status.LastScheduleTime.UTC()
	}
	dueAt = schedule.Next(anchor)
	if dueAt.After(now) {
		return dueAt, dueAt, false
	}
	return dueAt, schedule.Next(dueAt), true
}

func shouldForbidConcurrent(policy postgresv1alpha1.BackupConcurrencyPolicy) bool {
	return policy == "" || policy == postgresv1alpha1.BackupConcurrencyForbid
}

func (r *ScheduledBackupReconciler) hasActiveBackupJob(
	ctx context.Context,
	sb *postgresv1alpha1.ScheduledBackup,
) (bool, error) {
	var jobs postgresv1alpha1.BackupJobList
	if err := r.List(ctx, &jobs, client.InNamespace(sb.Namespace), client.MatchingLabels{
		"postgres.keiailab.io/scheduled-backup": sb.Name,
	}); err != nil {
		return false, err
	}
	for _, job := range jobs.Items {
		if job.Status.Phase == "" ||
			job.Status.Phase == postgresv1alpha1.BackupJobPending ||
			job.Status.Phase == postgresv1alpha1.BackupJobRunning {
			return true, nil
		}
	}
	return false, nil
}

func scheduledBackupJobName(scheduleName string, scheduledAt time.Time) string {
	suffix := fmt.Sprintf("%d", scheduledAt.UTC().Unix())
	maxPrefix := 63 - len(suffix) - 1
	prefix := strings.Trim(scheduleName, "-")
	if len(prefix) > maxPrefix {
		prefix = strings.Trim(prefix[:maxPrefix], "-")
	}
	if prefix == "" {
		prefix = "scheduled-backup"
	}
	return prefix + "-" + suffix
}

func buildBackupJobFromSchedule(sb *postgresv1alpha1.ScheduledBackup, name string) *postgresv1alpha1.BackupJob {
	backupType := sb.Spec.Type
	if backupType == "" {
		backupType = "full"
	}

	labels := map[string]string{}
	maps.Copy(labels, sb.Spec.Labels)
	labels["postgres.keiailab.io/cluster"] = sb.Spec.Cluster.Name
	labels["postgres.keiailab.io/scheduled-backup"] = sb.Name

	return &postgresv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: sb.Namespace,
			Labels:    labels,
		},
		Spec: postgresv1alpha1.BackupJobSpec{
			Cluster:       sb.Spec.Cluster,
			Tool:          sb.Spec.Tool,
			Repo:          sb.Spec.Repo,
			Type:          backupType,
			Retention:     sb.Spec.Retention,
			ExecutionMode: sb.Spec.ExecutionMode,
			JobTemplate:   sb.Spec.JobTemplate.DeepCopy(),
			Labels:        labels,
		},
	}
}

func applyScheduledBackupOwner(
	scheme *runtime.Scheme,
	sb *postgresv1alpha1.ScheduledBackup,
	cluster *postgresv1alpha1.PostgresCluster,
	job *postgresv1alpha1.BackupJob,
) error {
	switch sb.Spec.BackupOwnerReference {
	case postgresv1alpha1.ScheduledBackupOwnerReferenceNone:
		return nil
	case postgresv1alpha1.ScheduledBackupOwnerReferenceCluster:
		return ctrl.SetControllerReference(cluster, job, scheme)
	default:
		return ctrl.SetControllerReference(sb, job, scheme)
	}
}

func (r *ScheduledBackupReconciler) markNotReady(
	sb *postgresv1alpha1.ScheduledBackup,
	reason string,
	message string,
) {
	sb.Status.ObservedGeneration = sb.Generation
	setScheduledBackupCondition(sb, metav1.ConditionFalse, reason, message)
}

func (r *ScheduledBackupReconciler) statusUpdate(ctx context.Context, sb *postgresv1alpha1.ScheduledBackup) error {
	if err := r.Status().Update(ctx, sb); err != nil {
		if apierrors.IsConflict(err) {
			return nil
		}
		return err
	}
	return nil
}

func setScheduledBackupCondition(
	sb *postgresv1alpha1.ScheduledBackup,
	status metav1.ConditionStatus,
	reason string,
	message string,
) {
	meta.SetStatusCondition(&sb.Status.Conditions, metav1.Condition{
		Type:               ScheduledBackupConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: sb.Generation,
	})
}

// SetupWithManager 는 본 reconciler 를 controller-runtime Manager 에 등록한다.
func (r *ScheduledBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&postgresv1alpha1.ScheduledBackup{}).
		Owns(&postgresv1alpha1.BackupJob{}).
		Named("scheduledbackup").
		Complete(r)
}
