/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

func fixedNow(t *testing.T, ts time.Time) {
	t.Helper()
	old := nowFunc
	nowFunc = func() metav1.Time { return metav1.NewTime(ts) }
	t.Cleanup(func() { nowFunc = old })
}

func newScheduledBackup(name string, schedule string) *postgresv1alpha1.ScheduledBackup {
	return &postgresv1alpha1.ScheduledBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			Generation:        1,
			CreationTimestamp: metav1.NewTime(time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)),
		},
		Spec: postgresv1alpha1.ScheduledBackupSpec{
			Schedule: schedule,
			Cluster:  postgresv1alpha1.BackupClusterRef{Name: "demo"},
			Tool:     "pgbackrest",
			Repo:     "repo1",
		},
	}
}

func reconcileScheduledBackupOnce(
	t *testing.T,
	r *ScheduledBackupReconciler,
	sb *postgresv1alpha1.ScheduledBackup,
) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: sb.Namespace, Name: sb.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	return result
}

func getScheduledBackup(
	t *testing.T,
	c client.Client,
	sb *postgresv1alpha1.ScheduledBackup,
) *postgresv1alpha1.ScheduledBackup {
	t.Helper()
	var got postgresv1alpha1.ScheduledBackup
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: sb.Namespace, Name: sb.Name}, &got); err != nil {
		t.Fatalf("Get ScheduledBackup: %v", err)
	}
	return &got
}

func listBackupJobs(t *testing.T, c client.Client) []postgresv1alpha1.BackupJob {
	t.Helper()
	var jobs postgresv1alpha1.BackupJobList
	if err := c.List(context.Background(), &jobs, client.InNamespace("default")); err != nil {
		t.Fatalf("List BackupJobs: %v", err)
	}
	return jobs.Items
}

func TestScheduledBackupReconcile_ImmediateCreatesBackupJob(t *testing.T) {
	now := time.Date(2026, 5, 12, 1, 30, 0, 0, time.UTC)
	fixedNow(t, now)

	scheme := newScheme(t)
	sb := newScheduledBackup("nightly", "0 0 2 * * *")
	sb.Spec.Immediate = true
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sb, cluster).
		WithStatusSubresource(&postgresv1alpha1.ScheduledBackup{}, &postgresv1alpha1.BackupJob{}).
		Build()

	r := &ScheduledBackupReconciler{Client: c, Scheme: scheme}
	reconcileScheduledBackupOnce(t, r, sb)

	jobs := listBackupJobs(t, c)
	if len(jobs) != 1 {
		t.Fatalf("BackupJob count: got %d, want 1", len(jobs))
	}
	job := jobs[0]
	if job.Name != "nightly-1778549400" {
		t.Errorf("BackupJob name: got %q, want nightly-1778549400", job.Name)
	}
	if job.Spec.Type != "full" {
		t.Errorf("BackupJob type default: got %q, want full", job.Spec.Type)
	}
	if job.Labels["postgres.keiailab.io/scheduled-backup"] != "nightly" {
		t.Errorf("scheduled-backup label missing: %v", job.Labels)
	}
	if len(job.OwnerReferences) != 1 || job.OwnerReferences[0].Kind != "ScheduledBackup" {
		t.Errorf("OwnerReferences: got %+v, want ScheduledBackup controller ref", job.OwnerReferences)
	}

	got := getScheduledBackup(t, c, sb)
	if got.Status.LastBackupJobName != job.Name {
		t.Errorf("LastBackupJobName: got %q, want %q", got.Status.LastBackupJobName, job.Name)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, ScheduledBackupConditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ScheduledBackupReasonBackupJobCreated {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestScheduledBackupReconcile_DueScheduleCreatesDeterministicBackupJob(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 10, 0, 0, time.UTC)
	fixedNow(t, now)

	scheme := newScheme(t)
	sb := newScheduledBackup("every-five", "0 */5 * * * *")
	last := metav1.NewTime(time.Date(2026, 5, 12, 0, 5, 0, 0, time.UTC))
	sb.Status.LastScheduleTime = &last
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sb, cluster).
		WithStatusSubresource(&postgresv1alpha1.ScheduledBackup{}, &postgresv1alpha1.BackupJob{}).
		Build()

	r := &ScheduledBackupReconciler{Client: c, Scheme: scheme}
	reconcileScheduledBackupOnce(t, r, sb)

	jobs := listBackupJobs(t, c)
	if len(jobs) != 1 {
		t.Fatalf("BackupJob count: got %d, want 1", len(jobs))
	}
	if jobs[0].Name != "every-five-1778544600" {
		t.Errorf("BackupJob name: got %q, want every-five-1778544600", jobs[0].Name)
	}
	got := getScheduledBackup(t, c, sb)
	if got.Status.LastScheduleTime == nil || !got.Status.LastScheduleTime.Time.Equal(now) {
		t.Errorf("LastScheduleTime: got %+v, want %s", got.Status.LastScheduleTime, now)
	}
}

func TestScheduledBackupReconcile_SuspendDoesNotCreateBackupJob(t *testing.T) {
	fixedNow(t, time.Date(2026, 5, 12, 1, 30, 0, 0, time.UTC))

	scheme := newScheme(t)
	sb := newScheduledBackup("paused", "0 0 2 * * *")
	sb.Spec.Suspend = true
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sb, cluster).
		WithStatusSubresource(&postgresv1alpha1.ScheduledBackup{}, &postgresv1alpha1.BackupJob{}).
		Build()

	r := &ScheduledBackupReconciler{Client: c, Scheme: scheme}
	reconcileScheduledBackupOnce(t, r, sb)

	if jobs := listBackupJobs(t, c); len(jobs) != 0 {
		t.Fatalf("BackupJob count: got %d, want 0", len(jobs))
	}
	got := getScheduledBackup(t, c, sb)
	cond := meta.FindStatusCondition(got.Status.Conditions, ScheduledBackupConditionReady)
	if cond == nil || cond.Reason != ScheduledBackupReasonSuspended {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestScheduledBackupReconcile_InvalidScheduleSetsCondition(t *testing.T) {
	fixedNow(t, time.Date(2026, 5, 12, 1, 30, 0, 0, time.UTC))

	scheme := newScheme(t)
	sb := newScheduledBackup("bad", "* * *")
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sb, cluster).
		WithStatusSubresource(&postgresv1alpha1.ScheduledBackup{}, &postgresv1alpha1.BackupJob{}).
		Build()

	r := &ScheduledBackupReconciler{Client: c, Scheme: scheme}
	reconcileScheduledBackupOnce(t, r, sb)

	if jobs := listBackupJobs(t, c); len(jobs) != 0 {
		t.Fatalf("BackupJob count: got %d, want 0", len(jobs))
	}
	got := getScheduledBackup(t, c, sb)
	cond := meta.FindStatusCondition(got.Status.Conditions, ScheduledBackupConditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ScheduledBackupReasonInvalidSchedule {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestScheduledBackupReconcile_ForbidConcurrencyBlocksNewBackupJob(t *testing.T) {
	now := time.Date(2026, 5, 12, 0, 10, 0, 0, time.UTC)
	fixedNow(t, now)

	scheme := newScheme(t)
	sb := newScheduledBackup("serial", "0 */5 * * * *")
	last := metav1.NewTime(time.Date(2026, 5, 12, 0, 5, 0, 0, time.UTC))
	sb.Status.LastScheduleTime = &last
	cluster := newBackupJobCluster()
	active := newBackupJob("serial-active", postgresv1alpha1.BackupJobRunning)
	active.Labels = map[string]string{"postgres.keiailab.io/scheduled-backup": "serial"}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(sb, cluster, active).
		WithStatusSubresource(&postgresv1alpha1.ScheduledBackup{}, &postgresv1alpha1.BackupJob{}).
		Build()

	r := &ScheduledBackupReconciler{Client: c, Scheme: scheme}
	reconcileScheduledBackupOnce(t, r, sb)

	if jobs := listBackupJobs(t, c); len(jobs) != 1 {
		t.Fatalf("BackupJob count: got %d, want existing 1 only", len(jobs))
	}
	got := getScheduledBackup(t, c, sb)
	cond := meta.FindStatusCondition(got.Status.Conditions, ScheduledBackupConditionReady)
	if cond == nil || cond.Reason != ScheduledBackupReasonConcurrencyBlocked {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}
