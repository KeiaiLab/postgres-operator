/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// BackupJob phase 전이 회귀 보호 (ROADMAP G1 §Backup/Restore).
//
// 전이 모델 검증:
//   - "" → Pending (cluster + plugin OK)
//   - Pending → Running (StartedAt 기록)
//   - Running → Succeeded (BackupID + Bytes + EndedAt 기록)
//   - Running → Failed (plugin 에러)
//   - 터미널 상태 no-op
//   - ClusterNotFound / PluginNotRegistered → Failed (기존 동작 회귀 가드)

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/plugin"
)

// stubBackupPlugin — PerformBackup 호출을 캡처하고 미리 지정한 결과/에러를 반환.
type stubBackupPlugin struct {
	name   string
	result plugin.BackupResult
	err    error
	called int
}

func (s *stubBackupPlugin) Name() string { return s.name }
func (s *stubBackupPlugin) PerformBackup(_ context.Context, _ plugin.ClusterTarget, _ plugin.BackupOptions) (plugin.BackupResult, error) {
	s.called++
	return s.result, s.err
}
func (s *stubBackupPlugin) RestorePIT(_ context.Context, _ plugin.ClusterTarget, _ time.Time) error {
	return nil
}
func (s *stubBackupPlugin) Validate(_ *plugin.BackupSpec) error { return nil }

func newBackupJob(name string, phase postgresv1alpha1.BackupJobPhase) *postgresv1alpha1.BackupJob {
	return &postgresv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			Generation: 1,
		},
		Spec: postgresv1alpha1.BackupJobSpec{
			Cluster: postgresv1alpha1.BackupClusterRef{Name: "demo"},
			Tool:    "pgbackrest",
			Repo:    "repo1",
			Type:    "full",
		},
		Status: postgresv1alpha1.BackupJobStatus{Phase: phase},
	}
}

func newBackupJobCluster() *postgresv1alpha1.PostgresCluster {
	return &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
	}
}

// reconcileOnce — fake client 로 단일 reconcile 호출 후 갱신된 BackupJob 반환.
func reconcileOnce(t *testing.T, r *BackupJobReconciler, c client.Client, bj *postgresv1alpha1.BackupJob) *postgresv1alpha1.BackupJob {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: bj.Namespace, Name: bj.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}
	var got postgresv1alpha1.BackupJob
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: bj.Namespace, Name: bj.Name}, &got); err != nil {
		t.Fatalf("Get back: %v", err)
	}
	return &got
}

func TestBackupJobReconcile_EmptyToPending(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-1", "")
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	reg := plugin.NewRegistry()
	reg.RegisterBackup(&stubBackupPlugin{name: "pgbackrest"})

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobPending {
		t.Errorf("Phase: got %q, want Pending", got.Status.Phase)
	}
	if got.Status.ObservedGeneration != 1 {
		t.Errorf("ObservedGeneration: got %d, want 1", got.Status.ObservedGeneration)
	}
}

func TestBackupJobReconcile_PendingToRunning_RecordsStartedAt(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-2", postgresv1alpha1.BackupJobPending)
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	reg := plugin.NewRegistry()
	reg.RegisterBackup(&stubBackupPlugin{name: "pgbackrest"})

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobRunning {
		t.Errorf("Phase: got %q, want Running", got.Status.Phase)
	}
	if got.Status.StartedAt == nil {
		t.Error("StartedAt must be non-nil after Pending → Running transition")
	}
}

func TestBackupJobReconcile_RunningToSucceeded_RecordsResult(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-3", postgresv1alpha1.BackupJobRunning)
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	stub := &stubBackupPlugin{
		name:   "pgbackrest",
		result: plugin.BackupResult{BackupID: "backup-001", Bytes: 4096, Repo: "repo1"},
	}
	reg := plugin.NewRegistry()
	reg.RegisterBackup(stub)

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg}
	got := reconcileOnce(t, r, c, bj)

	if stub.called != 1 {
		t.Errorf("PerformBackup called %d times, want 1", stub.called)
	}
	if got.Status.Phase != postgresv1alpha1.BackupJobSucceeded {
		t.Errorf("Phase: got %q, want Succeeded", got.Status.Phase)
	}
	if got.Status.BackupID != "backup-001" {
		t.Errorf("BackupID: got %q, want backup-001", got.Status.BackupID)
	}
	if got.Status.Bytes != 4096 {
		t.Errorf("Bytes: got %d, want 4096", got.Status.Bytes)
	}
	if got.Status.EndedAt == nil {
		t.Error("EndedAt must be non-nil after terminal transition")
	}
}

func TestBackupJobReconcile_RunningToFailed_RecordsError(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-4", postgresv1alpha1.BackupJobRunning)
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	stub := &stubBackupPlugin{name: "pgbackrest", err: errors.New("s3 timeout")}
	reg := plugin.NewRegistry()
	reg.RegisterBackup(stub)

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobFailed {
		t.Errorf("Phase: got %q, want Failed", got.Status.Phase)
	}
	if got.Status.EndedAt == nil {
		t.Error("EndedAt must be non-nil after Failed terminal")
	}
}

func TestBackupJobReconcile_Terminal_NoOp(t *testing.T) {
	t.Parallel()
	cases := []postgresv1alpha1.BackupJobPhase{
		postgresv1alpha1.BackupJobSucceeded,
		postgresv1alpha1.BackupJobFailed,
	}
	for _, phase := range cases {
		t.Run(string(phase), func(t *testing.T) {
			t.Parallel()
			scheme := newScheme(t)
			bj := newBackupJob("bj-term", phase)
			bj.Status.BackupID = "preserved"
			cluster := newBackupJobCluster()
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(bj, cluster).
				WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
				Build()
			stub := &stubBackupPlugin{name: "pgbackrest"}
			reg := plugin.NewRegistry()
			reg.RegisterBackup(stub)

			r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg}
			got := reconcileOnce(t, r, c, bj)

			if stub.called != 0 {
				t.Errorf("terminal %q → plugin must not be invoked, called=%d", phase, stub.called)
			}
			if got.Status.Phase != phase {
				t.Errorf("terminal phase mutated: got %q, want %q", got.Status.Phase, phase)
			}
			if got.Status.BackupID != "preserved" {
				t.Errorf("BackupID mutated: got %q, want preserved", got.Status.BackupID)
			}
		})
	}
}

func TestBackupJobReconcile_ClusterNotFound_Failed(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-orphan", "")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	reg := plugin.NewRegistry()
	reg.RegisterBackup(&stubBackupPlugin{name: "pgbackrest"})

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobFailed {
		t.Errorf("Phase: got %q, want Failed", got.Status.Phase)
	}
}

func TestBackupJobReconcile_PluginNotRegistered_Failed(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-noplugin", "")
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	reg := plugin.NewRegistry() // 비어 있음

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobFailed {
		t.Errorf("Phase: got %q, want Failed", got.Status.Phase)
	}
}
