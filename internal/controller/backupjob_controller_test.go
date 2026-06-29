/*
Copyright 2026 keiailab.

SPDX-License-Identifier: MIT
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
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
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
	name                 string
	result               plugin.BackupResult
	err                  error
	restoreErr           error
	command              []string
	restoreCommand       []string
	parsedResult         plugin.BackupResult
	called               int
	restoreCalled        int
	commandCalled        int
	restoreCommandCalled int
	restoreAt            time.Time
}

func (s *stubBackupPlugin) Name() string { return s.name }
func (s *stubBackupPlugin) PerformBackup(_ context.Context, _ plugin.ClusterTarget, _ plugin.BackupOptions) (plugin.BackupResult, error) {
	s.called++
	return s.result, s.err
}
func (s *stubBackupPlugin) RestorePIT(_ context.Context, _ plugin.ClusterTarget, ts time.Time) error {
	s.restoreCalled++
	s.restoreAt = ts
	return s.restoreErr
}
func (s *stubBackupPlugin) Validate(_ *plugin.BackupSpec) error { return nil }
func (s *stubBackupPlugin) BackupCommand(_ plugin.ClusterTarget, _ plugin.BackupOptions) ([]string, error) {
	s.commandCalled++
	return append([]string{}, s.command...), s.err
}
func (s *stubBackupPlugin) RestoreCommand(_ plugin.ClusterTarget, _ time.Time) ([]string, error) {
	s.restoreCommandCalled++
	return append([]string{}, s.restoreCommand...), s.restoreErr
}
func (s *stubBackupPlugin) ParseBackupResult(_ []byte, _ plugin.BackupOptions) plugin.BackupResult {
	return s.parsedResult
}

type fakeBackupSidecarExecutor struct {
	output  []byte
	err     error
	called  int
	target  BackupSidecarTarget
	command []string
}

func (f *fakeBackupSidecarExecutor) Exec(
	_ context.Context,
	target BackupSidecarTarget,
	command []string,
) ([]byte, error) {
	f.called++
	f.target = target
	f.command = append([]string{}, command...)
	return f.output, f.err
}

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

func TestBackupJobReconcile_RunningRestoreToSucceeded_CallsRestorePIT(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-restore", postgresv1alpha1.BackupJobRunning)
	targetTime := metav1.NewTime(time.Date(2026, 5, 12, 1, 0, 0, 0, time.UTC))
	bj.Spec.Type = backupJobTypeRestore
	bj.Spec.Restore = &postgresv1alpha1.BackupRestoreSpec{
		TargetTime: &targetTime,
		BackupID:   "backup-restore-source",
	}
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
		t.Errorf("PerformBackup called %d times, want 0 for restore", stub.called)
	}
	if stub.restoreCalled != 1 {
		t.Errorf("RestorePIT called %d times, want 1", stub.restoreCalled)
	}
	if !stub.restoreAt.Equal(targetTime.Time) {
		t.Errorf("RestorePIT target time: got %s, want %s", stub.restoreAt, targetTime.Time)
	}
	if got.Status.Phase != postgresv1alpha1.BackupJobSucceeded {
		t.Errorf("Phase: got %q, want Succeeded", got.Status.Phase)
	}
	if got.Status.BackupID != "backup-restore-source" {
		t.Errorf("BackupID: got %q, want backup-restore-source", got.Status.BackupID)
	}
	if got.Status.EndedAt == nil {
		t.Error("EndedAt must be non-nil after restore terminal transition")
	}
}

func TestBackupJobReconcile_RestoreRequiresTargetTime(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-restore-invalid", "")
	bj.Spec.Type = backupJobTypeRestore
	bj.Spec.Restore = &postgresv1alpha1.BackupRestoreSpec{BackupID: "backup-only"}
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

	if stub.called != 0 || stub.restoreCalled != 0 {
		t.Errorf("plugin should not be called for invalid restore, backup=%d restore=%d", stub.called, stub.restoreCalled)
	}
	if got.Status.Phase != postgresv1alpha1.BackupJobFailed {
		t.Errorf("Phase: got %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, BackupJobConditionReady)
	if cond == nil || cond.Reason != BackupJobReasonInvalidSpec {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestBackupJobReconcile_JobModeRequiresJobTemplate(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-job-missing-template", "")
	bj.Spec.ExecutionMode = backupJobExecutionModeJob
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()

	r := &BackupJobReconciler{Client: c, Scheme: scheme}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobFailed {
		t.Errorf("Phase: got %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, BackupJobConditionReady)
	if cond == nil || cond.Reason != BackupJobReasonInvalidSpec {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestBackupJobReconcile_RunningJobModeCreatesRunnerJob(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-job", postgresv1alpha1.BackupJobRunning)
	bj.Spec.ExecutionMode = backupJobExecutionModeJob
	bj.Spec.JobTemplate = backupJobRunnerTemplate()
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()

	r := &BackupJobReconciler{Client: c, Scheme: scheme}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobRunning {
		t.Errorf("Phase: got %q, want Running", got.Status.Phase)
	}
	if got.Status.RunnerJobName != "bj-job-runner" {
		t.Errorf("RunnerJobName: got %q, want bj-job-runner", got.Status.RunnerJobName)
	}

	var runner batchv1.Job
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: bj.Namespace, Name: "bj-job-runner"}, &runner); err != nil {
		t.Fatalf("runner Job get: %v", err)
	}
	if len(runner.OwnerReferences) != 1 || runner.OwnerReferences[0].Kind != "BackupJob" || runner.OwnerReferences[0].Name != bj.Name {
		t.Fatalf("ownerReferences mismatch: %+v", runner.OwnerReferences)
	}
	if runner.Labels["postgres.keiailab.io/backupjob"] != bj.Name {
		t.Errorf("backupjob label: got %q, want %q", runner.Labels["postgres.keiailab.io/backupjob"], bj.Name)
	}
	if runner.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy: got %q, want Never", runner.Spec.Template.Spec.RestartPolicy)
	}
	env := runner.Spec.Template.Spec.Containers[0].Env
	assertEnv(t, env, "POSTGRES_CLUSTER_NAME", "demo")
	assertEnv(t, env, "POSTGRES_CLUSTER_NAMESPACE", "default")
	assertEnv(t, env, "BACKUP_JOB_NAME", "bj-job")
	assertEnv(t, env, "BACKUP_REPO", "repo1")
	assertEnv(t, env, "BACKUP_TYPE", "full")
}

func TestBackupJobReconcile_RunningJobModeRestoreAllowsBackupIDOnly(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-job-restore", postgresv1alpha1.BackupJobRunning)
	bj.Spec.ExecutionMode = backupJobExecutionModeJob
	bj.Spec.Type = backupJobTypeRestore
	bj.Spec.Restore = &postgresv1alpha1.BackupRestoreSpec{BackupID: "backup-20260512"}
	bj.Spec.JobTemplate = backupJobRunnerTemplate()
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()

	r := &BackupJobReconciler{Client: c, Scheme: scheme}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobRunning {
		t.Errorf("Phase: got %q, want Running", got.Status.Phase)
	}
	if got.Status.RunnerJobName != "bj-job-restore-runner" {
		t.Errorf("RunnerJobName: got %q, want bj-job-restore-runner", got.Status.RunnerJobName)
	}

	var runner batchv1.Job
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: bj.Namespace,
		Name:      "bj-job-restore-runner",
	}, &runner); err != nil {
		t.Fatalf("runner Job get: %v", err)
	}
	env := runner.Spec.Template.Spec.Containers[0].Env
	assertEnv(t, env, "BACKUP_TYPE", backupJobTypeRestore)
	assertEnv(t, env, "BACKUP_ID", "backup-20260512")
}

func TestBackupJobReconcile_RunningJobModeCompleteMarksSucceeded(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-job-complete", postgresv1alpha1.BackupJobRunning)
	bj.Spec.ExecutionMode = backupJobExecutionModeJob
	bj.Spec.JobTemplate = backupJobRunnerTemplate()
	bj.Status.RunnerJobName = "bj-job-complete-runner"
	cluster := newBackupJobCluster()
	runner := backupJobRunner("bj-job-complete-runner", bj)
	runner.Status.Conditions = []batchv1.JobCondition{{
		Type:   batchv1.JobComplete,
		Status: corev1.ConditionTrue,
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster, runner).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()

	r := &BackupJobReconciler{Client: c, Scheme: scheme}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobSucceeded {
		t.Errorf("Phase: got %q, want Succeeded", got.Status.Phase)
	}
	if got.Status.BackupID != "bj-job-complete-runner" {
		t.Errorf("BackupID: got %q, want runner job name", got.Status.BackupID)
	}
	if got.Status.EndedAt == nil {
		t.Error("EndedAt must be non-nil after runner Job completion")
	}
}

func TestBackupJobReconcile_RunningJobModeFailedMarksFailed(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-job-failed", postgresv1alpha1.BackupJobRunning)
	bj.Spec.ExecutionMode = backupJobExecutionModeJob
	bj.Spec.JobTemplate = backupJobRunnerTemplate()
	bj.Status.RunnerJobName = "bj-job-failed-runner"
	cluster := newBackupJobCluster()
	runner := backupJobRunner("bj-job-failed-runner", bj)
	runner.Status.Conditions = []batchv1.JobCondition{{
		Type:    batchv1.JobFailed,
		Status:  corev1.ConditionTrue,
		Reason:  "BackoffLimitExceeded",
		Message: "pod failed repeatedly",
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster, runner).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()

	r := &BackupJobReconciler{Client: c, Scheme: scheme}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobFailed {
		t.Errorf("Phase: got %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, BackupJobConditionReady)
	if cond == nil || cond.Reason != BackupJobReasonRunnerJobFailed {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
}

func TestBackupJobReconcile_RunningSidecarBackupExecutesPrimaryPod(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-sidecar", postgresv1alpha1.BackupJobRunning)
	bj.Spec.ExecutionMode = backupJobExecutionModeSidecar
	cluster := newBackupJobCluster()
	cluster.Status.Shards = []postgresv1alpha1.ShardStatus{{
		Name:    "shard-0",
		Ordinal: 0,
		Primary: &postgresv1alpha1.ShardEndpoint{
			Pod:   "demo-shard-0-0",
			Ready: true,
		},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	stub := &stubBackupPlugin{
		name:         "pgbackrest",
		command:      []string{"pgbackrest", "--stanza=demo", "backup"},
		parsedResult: plugin.BackupResult{BackupID: "20260512-010203F", Bytes: 8192, Repo: "repo1"},
	}
	reg := plugin.NewRegistry()
	reg.RegisterBackup(stub)
	exec := &fakeBackupSidecarExecutor{output: []byte("backup label")}

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg, SidecarExecutor: exec}
	got := reconcileOnce(t, r, c, bj)

	if stub.called != 0 {
		t.Errorf("sidecar mode must not call PerformBackup directly, called=%d", stub.called)
	}
	if stub.commandCalled != 1 {
		t.Errorf("BackupCommand called %d times, want 1", stub.commandCalled)
	}
	if exec.called != 1 {
		t.Fatalf("sidecar Exec called %d times, want 1", exec.called)
	}
	if exec.target.Pod != "demo-shard-0-0" || exec.target.Container != "postgres" {
		t.Fatalf("sidecar target mismatch: %+v", exec.target)
	}
	if got.Status.Phase != postgresv1alpha1.BackupJobSucceeded {
		t.Errorf("Phase: got %q, want Succeeded", got.Status.Phase)
	}
	if got.Status.BackupID != "20260512-010203F" {
		t.Errorf("BackupID: got %q, want parsed sidecar backup label", got.Status.BackupID)
	}
	if got.Status.Bytes != 8192 {
		t.Errorf("Bytes: got %d, want 8192", got.Status.Bytes)
	}
}

func TestBackupJobReconcile_RunningSidecarRestoreScalesShardStatefulSetDown(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-sidecar-restore", postgresv1alpha1.BackupJobRunning)
	targetTime := metav1.NewTime(time.Date(2026, 5, 12, 2, 0, 0, 0, time.UTC))
	bj.Spec.ExecutionMode = backupJobExecutionModeSidecar
	bj.Spec.Type = backupJobTypeRestore
	bj.Spec.Restore = &postgresv1alpha1.BackupRestoreSpec{TargetTime: &targetTime}
	cluster := newBackupJobCluster()
	cluster.Status.Shards = []postgresv1alpha1.ShardStatus{{
		Name:    "shard-0",
		Ordinal: 0,
		Primary: &postgresv1alpha1.ShardEndpoint{
			Pod:   "demo-shard-0-0",
			Ready: true,
		},
	}}
	sts := buildPGStatefulSet(
		cluster,
		"demo-shard-0", "demo-shard-0-headless",
		0,
		"example.com/postgres:18", "demo-shard-0-config", "18",
		1,
		postgresv1alpha1.StorageSpec{},
		corev1.ResourceRequirements{},
		"",
		"test-config-hash",
		"",
	)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster, sts).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	stub := &stubBackupPlugin{
		name:           "pgbackrest",
		restoreCommand: []string{"pgbackrest", "--stanza=demo", "--type=time", "restore"},
	}
	reg := plugin.NewRegistry()
	reg.RegisterBackup(stub)
	exec := &fakeBackupSidecarExecutor{}

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg, SidecarExecutor: exec}
	got := reconcileOnce(t, r, c, bj)

	if stub.restoreCalled != 0 {
		t.Errorf("sidecar restore must not call RestorePIT directly, called=%d", stub.restoreCalled)
	}
	if stub.restoreCommandCalled != 0 {
		t.Errorf("RestoreCommand called %d times, want 0 while StatefulSet is stopping", stub.restoreCommandCalled)
	}
	if exec.called != 0 {
		t.Fatalf("sidecar Exec called %d times, want 0 for PITR restore orchestration", exec.called)
	}
	if got.Status.Phase != postgresv1alpha1.BackupJobRunning {
		t.Errorf("Phase: got %q, want Running while restore is orchestrating", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, BackupJobConditionReady)
	if cond == nil || cond.Reason != "RestoreClusterStopping" {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
	var observed appsv1.StatefulSet
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(sts), &observed); err != nil {
		t.Fatalf("get StatefulSet: %v", err)
	}
	if observed.Spec.Replicas == nil || *observed.Spec.Replicas != 0 {
		t.Fatalf("StatefulSet replicas = %v, want 0 for offline restore", observed.Spec.Replicas)
	}
	var observedCluster postgresv1alpha1.PostgresCluster
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), &observedCluster); err != nil {
		t.Fatalf("get PostgresCluster: %v", err)
	}
	if got := observedCluster.Annotations["postgres.keiailab.io/restore-in-progress"]; got != bj.Name {
		t.Fatalf("restore annotation = %q, want BackupJob name %q", got, bj.Name)
	}
}

func TestBackupJobReconcile_RunningSidecarRestoreCreatesRestoreJobAfterStatefulSetStopped(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-sidecar-restore-job", postgresv1alpha1.BackupJobRunning)
	targetTime := metav1.NewTime(time.Date(2026, 5, 12, 2, 0, 0, 0, time.UTC))
	bj.Spec.ExecutionMode = backupJobExecutionModeSidecar
	bj.Spec.Type = backupJobTypeRestore
	bj.Spec.Restore = &postgresv1alpha1.BackupRestoreSpec{TargetTime: &targetTime}
	cluster := newBackupJobCluster()
	sts := buildPGStatefulSet(
		cluster,
		"demo-shard-0", "demo-shard-0-headless",
		0,
		"example.com/postgres:18", "demo-shard-0-config", "18",
		1,
		postgresv1alpha1.StorageSpec{},
		corev1.ResourceRequirements{},
		"",
		"test-config-hash",
		"",
	)
	stopped := int32(0)
	sts.Spec.Replicas = &stopped
	restoreCommand := []string{"sh", "-c", "pgbackrest --stanza=demo --type=time restore"}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster, sts).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	stub := &stubBackupPlugin{
		name:           "pgbackrest",
		restoreCommand: restoreCommand,
	}
	reg := plugin.NewRegistry()
	reg.RegisterBackup(stub)
	exec := &fakeBackupSidecarExecutor{}

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg, SidecarExecutor: exec}
	got := reconcileOnce(t, r, c, bj)

	if exec.called != 0 {
		t.Fatalf("sidecar Exec called %d times, want 0; restore must run in a Job", exec.called)
	}
	if stub.restoreCommandCalled != 1 {
		t.Fatalf("RestoreCommand called %d times, want 1", stub.restoreCommandCalled)
	}
	if got.Status.Phase != postgresv1alpha1.BackupJobRunning {
		t.Fatalf("Phase = %q, want Running while restore Job executes", got.Status.Phase)
	}
	if got.Status.RunnerJobName != "bj-sidecar-restore-job-runner" {
		t.Fatalf("RunnerJobName = %q, want bj-sidecar-restore-job-runner", got.Status.RunnerJobName)
	}

	var job batchv1.Job
	if err := c.Get(context.Background(), client.ObjectKey{
		Namespace: bj.Namespace,
		Name:      "bj-sidecar-restore-job-runner",
	}, &job); err != nil {
		t.Fatalf("restore Job get: %v", err)
	}
	if len(job.OwnerReferences) != 1 || job.OwnerReferences[0].Kind != "BackupJob" || job.OwnerReferences[0].Name != bj.Name {
		t.Fatalf("ownerReferences mismatch: %+v", job.OwnerReferences)
	}
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != "example.com/postgres:18" {
		t.Fatalf("restore Job image = %q, want source StatefulSet postgres image", container.Image)
	}
	if gotCommand := strings.Join(container.Command, " "); gotCommand != strings.Join(restoreCommand, " ") {
		t.Fatalf("restore Job command = %q, want %q", gotCommand, strings.Join(restoreCommand, " "))
	}
	assertVolumeMount(t, container.VolumeMounts, "data", pgDataMountPath, "")
	assertVolumeMount(t, container.VolumeMounts, "ephemeral-pgbackrest-spool", "/var/spool/pgbackrest", "")
	assertPVCVolume(t, job.Spec.Template.Spec.Volumes, "data", "data-demo-shard-0-0")
	assertEmptyDirVolume(t, job.Spec.Template.Spec.Volumes, "ephemeral-pgbackrest-spool")
}

func TestBackupJobReconcile_RunningSidecarRestoreWaitsForShardPodsToStopBeforeCreatingJob(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-sidecar-restore-wait-pod", postgresv1alpha1.BackupJobRunning)
	targetTime := metav1.NewTime(time.Date(2026, 5, 12, 2, 0, 0, 0, time.UTC))
	bj.Spec.ExecutionMode = backupJobExecutionModeSidecar
	bj.Spec.Type = backupJobTypeRestore
	bj.Spec.Restore = &postgresv1alpha1.BackupRestoreSpec{TargetTime: &targetTime}
	cluster := newBackupJobCluster()
	sts := buildPGStatefulSet(
		cluster,
		"demo-shard-0", "demo-shard-0-headless",
		0,
		"example.com/postgres:18", "demo-shard-0-config", "18",
		1,
		postgresv1alpha1.StorageSpec{},
		corev1.ResourceRequirements{},
		"",
		"test-config-hash",
		"",
	)
	stopped := int32(0)
	sts.Spec.Replicas = &stopped
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-shard-0-0",
			Namespace: "default",
			Labels:    SelectorLabels("demo", "shard", 0),
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster, sts, pod).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	reg := plugin.NewRegistry()
	reg.RegisterBackup(&stubBackupPlugin{name: "pgbackrest", restoreCommand: []string{"sh", "-c", "restore"}})

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.RunnerJobName != "" {
		t.Fatalf("RunnerJobName = %q, want empty while shard pod still exists", got.Status.RunnerJobName)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, BackupJobConditionReady)
	if cond == nil || cond.Reason != "RestoreWaitingForPodsToStop" {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
	var jobs batchv1.JobList
	if err := c.List(context.Background(), &jobs, client.InNamespace(bj.Namespace)); err != nil {
		t.Fatalf("list Jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("restore Job count = %d, want 0 while shard pod still exists", len(jobs.Items))
	}
}

func TestBackupJobReconcile_RunningSidecarRestoreCompleteReleasesRestoreLockAndMarksSucceeded(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-sidecar-restore-complete", postgresv1alpha1.BackupJobRunning)
	targetTime := metav1.NewTime(time.Date(2026, 5, 12, 2, 0, 0, 0, time.UTC))
	bj.Spec.ExecutionMode = backupJobExecutionModeSidecar
	bj.Spec.Type = backupJobTypeRestore
	bj.Spec.Restore = &postgresv1alpha1.BackupRestoreSpec{TargetTime: &targetTime}
	bj.Status.RunnerJobName = "bj-sidecar-restore-complete-runner"
	cluster := newBackupJobCluster()
	cluster.Spec.Shards.Replicas = 1
	cluster.Annotations = map[string]string{
		"postgres.keiailab.io/restore-in-progress": bj.Name,
	}
	sts := buildPGStatefulSet(
		cluster,
		"demo-shard-0", "demo-shard-0-headless",
		0,
		"example.com/postgres:18", "demo-shard-0-config", "18",
		2,
		postgresv1alpha1.StorageSpec{},
		corev1.ResourceRequirements{},
		"",
		"test-config-hash",
		"",
	)
	stopped := int32(0)
	sts.Spec.Replicas = &stopped
	runner := backupJobRunner("bj-sidecar-restore-complete-runner", bj)
	runner.Status.Conditions = []batchv1.JobCondition{{
		Type:   batchv1.JobComplete,
		Status: corev1.ConditionTrue,
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster, sts, runner).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	reg := plugin.NewRegistry()
	reg.RegisterBackup(&stubBackupPlugin{name: "pgbackrest"})

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobSucceeded {
		t.Fatalf("Phase = %q, want Succeeded", got.Status.Phase)
	}
	if got.Status.EndedAt == nil {
		t.Fatal("EndedAt must be recorded after restore completion")
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, BackupJobConditionReady)
	if cond == nil || cond.Reason != BackupJobReasonRestoreSucceeded {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
	var observed appsv1.StatefulSet
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(sts), &observed); err != nil {
		t.Fatalf("get StatefulSet: %v", err)
	}
	if observed.Spec.Replicas == nil || *observed.Spec.Replicas != 0 {
		t.Fatalf("StatefulSet replicas = %v, want 0 until PostgresCluster reconciler restores desired template", observed.Spec.Replicas)
	}
	var observedCluster postgresv1alpha1.PostgresCluster
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), &observedCluster); err != nil {
		t.Fatalf("get PostgresCluster: %v", err)
	}
	if _, ok := observedCluster.Annotations["postgres.keiailab.io/restore-in-progress"]; ok {
		t.Fatal("restore annotation must be released after successful restore")
	}
	if observedCluster.Spec.Shards.Replicas != 1 {
		t.Fatalf("PostgresCluster spec.shards.replicas = %d, want preserved", observedCluster.Spec.Shards.Replicas)
	}
}

func TestBackupJobReconcile_RunningSidecarRestoreFailedLeavesStatefulSetStoppedAndMarksFailed(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-sidecar-restore-failed", postgresv1alpha1.BackupJobRunning)
	targetTime := metav1.NewTime(time.Date(2026, 5, 12, 2, 0, 0, 0, time.UTC))
	bj.Spec.ExecutionMode = backupJobExecutionModeSidecar
	bj.Spec.Type = backupJobTypeRestore
	bj.Spec.Restore = &postgresv1alpha1.BackupRestoreSpec{TargetTime: &targetTime}
	bj.Status.RunnerJobName = "bj-sidecar-restore-failed-runner"
	cluster := newBackupJobCluster()
	cluster.Annotations = map[string]string{
		"postgres.keiailab.io/restore-in-progress": bj.Name,
	}
	sts := buildPGStatefulSet(
		cluster,
		"demo-shard-0", "demo-shard-0-headless",
		0,
		"example.com/postgres:18", "demo-shard-0-config", "18",
		1,
		postgresv1alpha1.StorageSpec{},
		corev1.ResourceRequirements{},
		"",
		"test-config-hash",
		"",
	)
	stopped := int32(0)
	sts.Spec.Replicas = &stopped
	runner := backupJobRunner("bj-sidecar-restore-failed-runner", bj)
	runner.Status.Conditions = []batchv1.JobCondition{{
		Type:    batchv1.JobFailed,
		Status:  corev1.ConditionTrue,
		Reason:  "BackoffLimitExceeded",
		Message: "restore exited 56",
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster, sts, runner).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	reg := plugin.NewRegistry()
	reg.RegisterBackup(&stubBackupPlugin{name: "pgbackrest"})

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg}
	got := reconcileOnce(t, r, c, bj)

	if got.Status.Phase != postgresv1alpha1.BackupJobFailed {
		t.Fatalf("Phase = %q, want Failed", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, BackupJobConditionReady)
	if cond == nil || cond.Reason != BackupJobReasonRestoreFailed {
		t.Fatalf("Ready condition mismatch: %+v", cond)
	}
	var observed appsv1.StatefulSet
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(sts), &observed); err != nil {
		t.Fatalf("get StatefulSet: %v", err)
	}
	if observed.Spec.Replicas == nil || *observed.Spec.Replicas != 0 {
		t.Fatalf("StatefulSet replicas = %v, want 0 after failed restore", observed.Spec.Replicas)
	}
	var observedCluster postgresv1alpha1.PostgresCluster
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), &observedCluster); err != nil {
		t.Fatalf("get PostgresCluster: %v", err)
	}
	if got := observedCluster.Annotations["postgres.keiailab.io/restore-in-progress"]; got != bj.Name {
		t.Fatalf("restore annotation = %q, want failed BackupJob name %q for manual intervention", got, bj.Name)
	}
}

func TestBackupJobReconcile_RunningSidecarRequiresReadyPrimary(t *testing.T) {
	t.Parallel()
	scheme := newScheme(t)
	bj := newBackupJob("bj-sidecar-no-primary", postgresv1alpha1.BackupJobRunning)
	bj.Spec.ExecutionMode = backupJobExecutionModeSidecar
	cluster := newBackupJobCluster()
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bj, cluster).
		WithStatusSubresource(&postgresv1alpha1.BackupJob{}).
		Build()
	reg := plugin.NewRegistry()
	reg.RegisterBackup(&stubBackupPlugin{name: "pgbackrest", command: []string{"pgbackrest", "backup"}})
	exec := &fakeBackupSidecarExecutor{}

	r := &BackupJobReconciler{Client: c, Scheme: scheme, Plugins: reg, SidecarExecutor: exec}
	got := reconcileOnce(t, r, c, bj)

	if exec.called != 0 {
		t.Fatalf("sidecar Exec should not run without ready primary, called=%d", exec.called)
	}
	if got.Status.Phase != postgresv1alpha1.BackupJobRunning {
		t.Errorf("Phase: got %q, want Running while waiting for ready primary", got.Status.Phase)
	}
	if got.Status.EndedAt != nil {
		t.Errorf("EndedAt = %v, want nil for transient sidecar target wait", got.Status.EndedAt)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, BackupJobConditionReady)
	if cond == nil || cond.Reason != BackupJobReasonSidecarTargetNotFound {
		t.Fatalf("Ready condition mismatch: %+v", cond)
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

func backupJobRunnerTemplate() *batchv1.JobTemplateSpec {
	backoffLimit := int32(1)
	return &batchv1.JobTemplateSpec{
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "pgbackrest",
						Image:   "pgbackrest:dev",
						Command: []string{"/bin/sh", "-c"},
						Args:    []string{"pgbackrest backup"},
					}},
				},
			},
		},
	}
}

func backupJobRunner(name string, bj *postgresv1alpha1.BackupJob) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: bj.Namespace,
			Labels: map[string]string{
				"postgres.keiailab.io/backupjob": bj.Name,
			},
		},
	}
}

func assertEnv(t *testing.T, env []corev1.EnvVar, name, want string) {
	t.Helper()
	for _, item := range env {
		if item.Name == name {
			if item.Value != want {
				t.Fatalf("env %s: got %q, want %q", name, item.Value, want)
			}
			return
		}
	}
	t.Fatalf("env %s not found in %+v", name, env)
}

func assertVolumeMount(t *testing.T, mounts []corev1.VolumeMount, name, mountPath, subPath string) {
	t.Helper()
	for _, mount := range mounts {
		if mount.Name == name && mount.MountPath == mountPath && mount.SubPath == subPath {
			return
		}
	}
	t.Fatalf("VolumeMount name=%q mountPath=%q subPath=%q not found in %+v", name, mountPath, subPath, mounts)
}

func assertPVCVolume(t *testing.T, volumes []corev1.Volume, name, claimName string) {
	t.Helper()
	for _, volume := range volumes {
		if volume.Name != name || volume.PersistentVolumeClaim == nil {
			continue
		}
		if volume.PersistentVolumeClaim.ClaimName != claimName {
			t.Fatalf("PVC volume %q claimName = %q, want %q", name, volume.PersistentVolumeClaim.ClaimName, claimName)
		}
		return
	}
	t.Fatalf("PVC volume %q not found in %+v", name, volumes)
}

func assertEmptyDirVolume(t *testing.T, volumes []corev1.Volume, name string) {
	t.Helper()
	for _, volume := range volumes {
		if volume.Name == name && volume.EmptyDir != nil {
			return
		}
	}
	t.Fatalf("EmptyDir volume %q not found in %+v", name, volumes)
}
