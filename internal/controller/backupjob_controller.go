/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package controller의 BackupJob reconciler. RFC 0004 §3 구현.
//
// 본 reconciler 의 BackupJob.Phase 전이 모델 (ROADMAP G1 §Backup/Restore):
//
//	""        → 신규 CR. cluster + plugin 검증 통과 후 Pending 으로 전이.
//	Pending   → StartedAt 기록 + Running 으로 전이. 다음 reconcile 에서 plugin 호출.
//	Running   → in-process 모드는 plugin.PerformBackup/RestorePIT 호출.
//	            executionMode=job 은 owned batch/v1 Job 생성 후 Job 상태 관찰.
//	            결과에 따라 Succeeded/Failed.
//	Succeeded → 터미널 (no-op). BackupID/Bytes/EndedAt 보존.
//	Failed    → 터미널 (no-op). 사용자가 새 CR 생성으로 재시도.
//
// 본 단계의 한계 (별도 PR 에서 다룬다):
//   - Sidecar exec 는 동기 pod/exec 1차 경로이며, 장기 실행/재시도 추적은 후속.
//   - Retention 정책 적용 (Bytes 기록만, 보존 cleanup 미구현).
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/plugin"
)

// nowFunc 는 metav1.Now 의 테스트 주입 지점 — deterministic StartedAt/EndedAt
// 검증을 위해 단위 테스트에서 override.
var nowFunc = func() metav1.Time { return metav1.Now() }

// BackupJobReconciler는 BackupJob CR을 reconcile한다 (RFC 0004 §3).
type BackupJobReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Plugins         *plugin.Registry
	SidecarExecutor BackupSidecarExecutor

	// Recorder 는 K8s Event 발행 용 (RFC-0017 §3.4). SetupWithManager 가 주입.
	Recorder events.EventRecorder
}

// BackupJob Conditions reason 상수 (status.go의 SOT 패턴 차용).
const (
	backupJobTypeRestore          = "restore"
	backupJobExecutionModeJob     = "job"
	backupJobExecutionModeSidecar = "sidecar"
	backupJobRunnerLabelKey       = "postgres.keiailab.io/backupjob"
	backupJobClusterLabelKey      = "postgres.keiailab.io/cluster"
	backupJobRunnerNameMaxLen     = 63
	backupJobRunnerNameSuffix     = "-runner"
	backupJobRunnerRequeueWait    = 15 * time.Second

	BackupJobReasonAwaitingInvocation           = "AwaitingPluginInvocation"
	BackupJobReasonClusterNotFound              = "ClusterNotFound"
	BackupJobReasonPluginNotRegistered          = "PluginNotRegistered"
	BackupJobReasonInvalidSpec                  = "InvalidSpec"
	BackupJobReasonBackupInProgress             = "BackupInProgress"
	BackupJobReasonBackupSucceeded              = "BackupSucceeded"
	BackupJobReasonBackupFailed                 = "BackupFailed"
	BackupJobReasonRestoreInProgress            = "RestoreInProgress"
	BackupJobReasonRestoreSucceeded             = "RestoreSucceeded"
	BackupJobReasonRestoreFailed                = "RestoreFailed"
	BackupJobReasonRunnerJobCreated             = "RunnerJobCreated"
	BackupJobReasonRunnerJobRunning             = "RunnerJobRunning"
	BackupJobReasonRunnerJobSucceeded           = "RunnerJobSucceeded"
	BackupJobReasonRunnerJobFailed              = "RunnerJobFailed"
	BackupJobReasonRunnerJobMissing             = "RunnerJobMissing"
	BackupJobReasonSidecarTargetNotFound        = "SidecarTargetNotFound"
	BackupJobReasonSidecarExecutorNotConfigured = "SidecarExecutorNotConfigured"
	BackupJobConditionReady                     = "Ready"
)

// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=backupjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=backupjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=postgres.keiailab.io,resources=backupjobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create

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
			DeleteBackupJobMetricsFor(req.Namespace, req.Name)
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

	if invalid := validateBackupJobSpecForExecution(&bj); invalid != "" {
		r.markFailed(&bj, BackupJobReasonInvalidSpec, invalid)
		return ctrl.Result{}, r.statusUpdate(ctx, &bj)
	}

	// 2. Plugin 등록 여부. executionMode=job 은 사용자가 제공한 batch/v1
	// JobTemplate이 실제 실행 계약이므로 in-process BackupPlugin 등록이 필요 없다.
	var backupPlugin plugin.BackupPlugin
	if bj.Spec.ExecutionMode != backupJobExecutionModeJob {
		if r.Plugins == nil {
			r.markFailed(&bj, BackupJobReasonPluginNotRegistered,
				"Plugin Registry is not configured (operator misconfiguration)")
			return ctrl.Result{}, r.statusUpdate(ctx, &bj)
		}
		var ok bool
		backupPlugin, ok = r.Plugins.Backup(bj.Spec.Tool)
		if !ok {
			r.markFailed(&bj, BackupJobReasonPluginNotRegistered,
				"BackupPlugin "+bj.Spec.Tool+" is not registered (RFC 0004 §4 — pgbackrest 1차)")
			return ctrl.Result{}, r.statusUpdate(ctx, &bj)
		}
	}

	// 3. Phase 전이: "" → Pending → Running → Succeeded/Failed.
	switch bj.Status.Phase {
	case "":
		// 신규 CR. Pending 으로 전이 + requeue.
		bj.Status.Phase = postgresv1alpha1.BackupJobPending
		bj.Status.ObservedGeneration = bj.Generation
		setBackupJobCondition(&bj, metav1.ConditionFalse,
			BackupJobReasonAwaitingInvocation,
			"BackupJob accepted — awaiting plugin invocation")
		return ctrl.Result{Requeue: true}, r.statusUpdate(ctx, &bj)

	case postgresv1alpha1.BackupJobPending:
		// Pending → Running. StartedAt 기록 + requeue 로 다음 turn 에서 plugin 호출.
		now := nowFunc()
		bj.Status.Phase = postgresv1alpha1.BackupJobRunning
		bj.Status.StartedAt = &now
		bj.Status.ObservedGeneration = bj.Generation
		reason := BackupJobReasonBackupInProgress
		message := "BackupPlugin " + bj.Spec.Tool + " invocation in progress"
		if bj.Spec.Type == backupJobTypeRestore {
			reason = BackupJobReasonRestoreInProgress
			message = "BackupPlugin " + bj.Spec.Tool + " PITR restore in progress"
		}
		setBackupJobCondition(&bj, metav1.ConditionFalse, reason, message)
		return ctrl.Result{Requeue: true}, r.statusUpdate(ctx, &bj)

	case postgresv1alpha1.BackupJobRunning:
		if bj.Spec.ExecutionMode == backupJobExecutionModeJob {
			return r.reconcileRunnerJob(ctx, &bj)
		}
		if bj.Spec.ExecutionMode == backupJobExecutionModeSidecar {
			return r.reconcileSidecar(ctx, &bj, &cluster, backupPlugin)
		}

		// in-process plugin 동기 호출. 결과로 terminal 전이.
		target := plugin.ClusterTarget{
			Namespace: bj.Namespace,
			Name:      bj.Spec.Cluster.Name,
		}
		if bj.Spec.Type == backupJobTypeRestore {
			return r.reconcileRestore(ctx, &bj, backupPlugin, target)
		}
		result, err := backupPlugin.PerformBackup(ctx, target, plugin.BackupOptions{
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
			setBackupJobCondition(&bj, metav1.ConditionFalse,
				BackupJobReasonBackupFailed,
				"BackupPlugin "+bj.Spec.Tool+" failed: "+err.Error())
			commonsevents.EmitWarningf(r.Recorder, &bj, BackupJobReasonBackupFailed,
				"BackupPlugin %s failed: %v", bj.Spec.Tool, err)
			return ctrl.Result{}, r.statusUpdate(ctx, &bj)
		}
		bj.Status.Phase = postgresv1alpha1.BackupJobSucceeded
		bj.Status.BackupID = result.BackupID
		bj.Status.Bytes = result.Bytes
		setBackupJobCondition(&bj, metav1.ConditionTrue,
			BackupJobReasonBackupSucceeded,
			"BackupPlugin "+bj.Spec.Tool+" succeeded: backupID="+result.BackupID)
		commonsevents.Emitf(r.Recorder, &bj, BackupJobReasonBackupSucceeded,
			"BackupPlugin %s succeeded: backupID=%s bytes=%d", bj.Spec.Tool, result.BackupID, result.Bytes)
		return ctrl.Result{}, r.statusUpdate(ctx, &bj)
	}

	// 알 수 없는 phase — defensive (CRD enum 으로 차단되지만 reconciler 측 가드).
	return ctrl.Result{}, nil
}

func validateBackupJobSpecForExecution(bj *postgresv1alpha1.BackupJob) string {
	if bj.Spec.ExecutionMode == backupJobExecutionModeJob {
		if bj.Spec.JobTemplate == nil {
			return "executionMode=job requires spec.jobTemplate with at least one runnable container"
		}
		containers := bj.Spec.JobTemplate.Spec.Template.Spec.Containers
		if len(containers) == 0 {
			return "executionMode=job requires spec.jobTemplate.spec.template.spec.containers"
		}
		for _, container := range containers {
			if strings.TrimSpace(container.Name) == "" {
				return "executionMode=job requires every jobTemplate container to have a name"
			}
			if strings.TrimSpace(container.Image) == "" {
				return "executionMode=job requires every jobTemplate container to have an image"
			}
		}
		if bj.Spec.Type == backupJobTypeRestore {
			if bj.Spec.Restore == nil ||
				(bj.Spec.Restore.TargetTime == nil && strings.TrimSpace(bj.Spec.Restore.BackupID) == "") {
				return "executionMode=job restore requires spec.restore.targetTime or spec.restore.backupID"
			}
		}
		return ""
	}
	if bj.Spec.Type != backupJobTypeRestore {
		return ""
	}
	if bj.Spec.Restore == nil || bj.Spec.Restore.TargetTime == nil {
		return "restore BackupJob requires spec.restore.targetTime; backupID-only restore is not implemented by BackupPlugin.RestorePIT"
	}
	return ""
}

func (r *BackupJobReconciler) reconcileRunnerJob(
	ctx context.Context,
	bj *postgresv1alpha1.BackupJob,
) (ctrl.Result, error) {
	if bj.Status.RunnerJobName == "" {
		jobName := backupRunnerJobName(bj.Name)
		runner := buildBackupRunnerJob(bj, jobName)
		if err := controllerutil.SetControllerReference(bj, runner, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, runner); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		bj.Status.RunnerJobName = jobName
		bj.Status.ObservedGeneration = bj.Generation
		setBackupJobCondition(bj, metav1.ConditionFalse,
			BackupJobReasonRunnerJobCreated,
			"Runner Job "+jobName+" created for executionMode=job")
		return ctrl.Result{Requeue: true}, r.statusUpdate(ctx, bj)
	}

	var runner batchv1.Job
	key := client.ObjectKey{Namespace: bj.Namespace, Name: bj.Status.RunnerJobName}
	if err := r.Get(ctx, key, &runner); err != nil {
		if apierrors.IsNotFound(err) {
			endedAt := nowFunc()
			bj.Status.EndedAt = &endedAt
			bj.Status.Phase = postgresv1alpha1.BackupJobFailed
			bj.Status.ObservedGeneration = bj.Generation
			setBackupJobCondition(bj, metav1.ConditionFalse,
				BackupJobReasonRunnerJobMissing,
				"Runner Job "+bj.Status.RunnerJobName+" is missing before terminal status")
			return ctrl.Result{}, r.statusUpdate(ctx, bj)
		}
		return ctrl.Result{}, err
	}

	if jobConditionTrue(&runner, batchv1.JobComplete) {
		endedAt := nowFunc()
		bj.Status.EndedAt = &endedAt
		bj.Status.Phase = postgresv1alpha1.BackupJobSucceeded
		bj.Status.ObservedGeneration = bj.Generation
		if bj.Status.BackupID == "" {
			bj.Status.BackupID = runner.Name
		}
		setBackupJobCondition(bj, metav1.ConditionTrue,
			BackupJobReasonRunnerJobSucceeded,
			"Runner Job "+runner.Name+" completed successfully")
		commonsevents.Emitf(r.Recorder, bj, BackupJobReasonRunnerJobSucceeded,
			"Runner Job %s completed successfully", runner.Name)
		return ctrl.Result{}, r.statusUpdate(ctx, bj)
	}

	if failed := findJobCondition(&runner, batchv1.JobFailed); failed != nil && failed.Status == corev1.ConditionTrue {
		endedAt := nowFunc()
		bj.Status.EndedAt = &endedAt
		bj.Status.Phase = postgresv1alpha1.BackupJobFailed
		bj.Status.ObservedGeneration = bj.Generation
		message := "Runner Job " + runner.Name + " failed"
		if failed.Reason != "" || failed.Message != "" {
			message = fmt.Sprintf("Runner Job %s failed: %s %s",
				runner.Name, strings.TrimSpace(failed.Reason), strings.TrimSpace(failed.Message))
		}
		setBackupJobCondition(bj, metav1.ConditionFalse, BackupJobReasonRunnerJobFailed, strings.TrimSpace(message))
		commonsevents.EmitWarningf(r.Recorder, bj, BackupJobReasonRunnerJobFailed,
			"Runner Job %s failed", runner.Name)
		return ctrl.Result{}, r.statusUpdate(ctx, bj)
	}

	bj.Status.ObservedGeneration = bj.Generation
	setBackupJobCondition(bj, metav1.ConditionFalse,
		BackupJobReasonRunnerJobRunning,
		"Runner Job "+runner.Name+" is still running")
	return ctrl.Result{RequeueAfter: backupJobRunnerRequeueWait}, r.statusUpdate(ctx, bj)
}

func buildBackupRunnerJob(bj *postgresv1alpha1.BackupJob, name string) *batchv1.Job {
	template := bj.Spec.JobTemplate.DeepCopy()
	labels := map[string]string{}
	maps.Copy(labels, template.Labels)
	maps.Copy(labels, bj.Spec.Labels)
	labels[backupJobRunnerLabelKey] = bj.Name
	labels[backupJobClusterLabelKey] = bj.Spec.Cluster.Name

	podLabels := map[string]string{}
	maps.Copy(podLabels, template.Spec.Template.Labels)
	maps.Copy(podLabels, labels)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   bj.Namespace,
			Labels:      labels,
			Annotations: template.Annotations,
		},
		Spec: template.Spec,
	}
	job.Spec.Template.Labels = podLabels
	if job.Spec.Template.Spec.RestartPolicy == "" {
		job.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyNever
	}
	injectBackupRunnerEnv(bj, name, &job.Spec.Template.Spec)
	return job
}

func injectBackupRunnerEnv(bj *postgresv1alpha1.BackupJob, runnerName string, podSpec *corev1.PodSpec) {
	env := []corev1.EnvVar{
		{Name: "POSTGRES_CLUSTER_NAME", Value: bj.Spec.Cluster.Name},
		{Name: "POSTGRES_CLUSTER_NAMESPACE", Value: bj.Namespace},
		{Name: "BACKUP_JOB_NAME", Value: bj.Name},
		{Name: "BACKUP_RUNNER_JOB_NAME", Value: runnerName},
		{Name: "BACKUP_TOOL", Value: bj.Spec.Tool},
		{Name: "BACKUP_REPO", Value: bj.Spec.Repo},
		{Name: "BACKUP_TYPE", Value: bj.Spec.Type},
	}
	if bj.Spec.Type == backupJobTypeRestore && bj.Spec.Restore != nil {
		if bj.Spec.Restore.TargetTime != nil {
			env = append(env, corev1.EnvVar{
				Name:  "BACKUP_TARGET_TIME",
				Value: bj.Spec.Restore.TargetTime.UTC().Format(time.RFC3339),
			})
		}
		if bj.Spec.Restore.BackupID != "" {
			env = append(env, corev1.EnvVar{Name: "BACKUP_ID", Value: bj.Spec.Restore.BackupID})
		}
	}

	for i := range podSpec.Containers {
		for _, item := range env {
			upsertEnvVar(&podSpec.Containers[i], item)
		}
	}
}

func upsertEnvVar(container *corev1.Container, item corev1.EnvVar) {
	for i := range container.Env {
		if container.Env[i].Name == item.Name {
			container.Env[i] = item
			return
		}
	}
	container.Env = append(container.Env, item)
}

func backupRunnerJobName(backupJobName string) string {
	if len(backupJobName)+len(backupJobRunnerNameSuffix) <= backupJobRunnerNameMaxLen {
		return backupJobName + backupJobRunnerNameSuffix
	}

	sum := sha256.Sum256([]byte(backupJobName))
	hash := hex.EncodeToString(sum[:])[:8]
	maxPrefix := backupJobRunnerNameMaxLen - len(backupJobRunnerNameSuffix) - len(hash) - 1
	prefix := strings.Trim(backupJobName[:maxPrefix], "-")
	if prefix == "" {
		prefix = "backupjob"
	}
	return prefix + "-" + hash + backupJobRunnerNameSuffix
}

func jobConditionTrue(job *batchv1.Job, conditionType batchv1.JobConditionType) bool {
	condition := findJobCondition(job, conditionType)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

func findJobCondition(job *batchv1.Job, conditionType batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == conditionType {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

func (r *BackupJobReconciler) reconcileSidecar(
	ctx context.Context,
	bj *postgresv1alpha1.BackupJob,
	cluster *postgresv1alpha1.PostgresCluster,
	backupPlugin plugin.BackupPlugin,
) (ctrl.Result, error) {
	commandPlugin, ok := backupPlugin.(plugin.BackupCommandPlugin)
	if !ok {
		r.markFailed(bj, BackupJobReasonInvalidSpec,
			"BackupPlugin "+bj.Spec.Tool+" does not support sidecar command planning")
		return ctrl.Result{}, r.statusUpdate(ctx, bj)
	}
	if r.SidecarExecutor == nil {
		r.markFailed(bj, BackupJobReasonSidecarExecutorNotConfigured,
			"Backup sidecar executor is not configured")
		return ctrl.Result{}, r.statusUpdate(ctx, bj)
	}
	target, ok := backupSidecarTarget(cluster)
	if !ok {
		r.markFailed(bj, BackupJobReasonSidecarTargetNotFound,
			"Ready primary pod not found for sidecar BackupJob")
		return ctrl.Result{}, r.statusUpdate(ctx, bj)
	}

	clusterTarget := plugin.ClusterTarget{
		Namespace: bj.Namespace,
		Name:      bj.Spec.Cluster.Name,
	}
	var command []string
	var err error
	if bj.Spec.Type == backupJobTypeRestore {
		command, err = commandPlugin.RestoreCommand(clusterTarget, bj.Spec.Restore.TargetTime.Time)
	} else {
		command, err = commandPlugin.BackupCommand(clusterTarget, plugin.BackupOptions{
			Type:          bj.Spec.Type,
			Repo:          bj.Spec.Repo,
			Labels:        bj.Spec.Labels,
			ExecutionMode: bj.Spec.ExecutionMode,
		})
	}
	if err != nil {
		r.markFailed(bj, BackupJobReasonInvalidSpec, err.Error())
		return ctrl.Result{}, r.statusUpdate(ctx, bj)
	}

	output, err := r.SidecarExecutor.Exec(ctx, target, command)
	endedAt := nowFunc()
	bj.Status.EndedAt = &endedAt
	bj.Status.ObservedGeneration = bj.Generation
	if err != nil {
		reason := BackupJobReasonBackupFailed
		message := "BackupPlugin " + bj.Spec.Tool + " sidecar backup failed: " + err.Error()
		if bj.Spec.Type == backupJobTypeRestore {
			reason = BackupJobReasonRestoreFailed
			message = "BackupPlugin " + bj.Spec.Tool + " sidecar restore failed: " + err.Error()
		}
		bj.Status.Phase = postgresv1alpha1.BackupJobFailed
		setBackupJobCondition(bj, metav1.ConditionFalse, reason, message)
		return ctrl.Result{}, r.statusUpdate(ctx, bj)
	}

	bj.Status.Phase = postgresv1alpha1.BackupJobSucceeded
	if bj.Spec.Type == backupJobTypeRestore {
		setBackupJobCondition(bj, metav1.ConditionTrue,
			BackupJobReasonRestoreSucceeded,
			"BackupPlugin "+bj.Spec.Tool+" sidecar PITR restore succeeded")
		return ctrl.Result{}, r.statusUpdate(ctx, bj)
	}

	result := commandPlugin.ParseBackupResult(output, plugin.BackupOptions{
		Type:          bj.Spec.Type,
		Repo:          bj.Spec.Repo,
		Labels:        bj.Spec.Labels,
		ExecutionMode: bj.Spec.ExecutionMode,
	})
	bj.Status.BackupID = result.BackupID
	bj.Status.Bytes = result.Bytes
	setBackupJobCondition(bj, metav1.ConditionTrue,
		BackupJobReasonBackupSucceeded,
		"BackupPlugin "+bj.Spec.Tool+" sidecar backup succeeded")
	return ctrl.Result{}, r.statusUpdate(ctx, bj)
}

func backupSidecarTarget(cluster *postgresv1alpha1.PostgresCluster) (BackupSidecarTarget, bool) {
	for _, shard := range cluster.Status.Shards {
		if shard.Ordinal != 0 || shard.Primary == nil {
			continue
		}
		if shard.Primary.Ready && shard.Primary.Pod != "" {
			return BackupSidecarTarget{
				Namespace: cluster.Namespace,
				Pod:       shard.Primary.Pod,
				Container: pgContainerName,
			}, true
		}
	}
	return BackupSidecarTarget{}, false
}

func (r *BackupJobReconciler) reconcileRestore(
	ctx context.Context,
	bj *postgresv1alpha1.BackupJob,
	backupPlugin plugin.BackupPlugin,
	target plugin.ClusterTarget,
) (ctrl.Result, error) {
	err := backupPlugin.RestorePIT(ctx, target, bj.Spec.Restore.TargetTime.Time)
	endedAt := nowFunc()
	bj.Status.EndedAt = &endedAt
	bj.Status.ObservedGeneration = bj.Generation
	if err != nil {
		bj.Status.Phase = postgresv1alpha1.BackupJobFailed
		setBackupJobCondition(bj, metav1.ConditionFalse,
			BackupJobReasonRestoreFailed,
			"BackupPlugin "+bj.Spec.Tool+" PITR restore failed: "+err.Error())
		commonsevents.EmitWarningf(r.Recorder, bj, BackupJobReasonRestoreFailed,
			"BackupPlugin %s PITR restore failed: %v", bj.Spec.Tool, err)
		return ctrl.Result{}, r.statusUpdate(ctx, bj)
	}
	bj.Status.Phase = postgresv1alpha1.BackupJobSucceeded
	if bj.Spec.Restore.BackupID != "" {
		bj.Status.BackupID = bj.Spec.Restore.BackupID
	}
	setBackupJobCondition(bj, metav1.ConditionTrue,
		BackupJobReasonRestoreSucceeded,
		"BackupPlugin "+bj.Spec.Tool+" PITR restore succeeded")
	commonsevents.Emitf(r.Recorder, bj, BackupJobReasonRestoreSucceeded,
		"BackupPlugin %s PITR restore succeeded", bj.Spec.Tool)
	return ctrl.Result{}, r.statusUpdate(ctx, bj)
}

// markFailed는 BackupJob을 Failed로 마킹한다.
//
// RFC-0017 §3.4: Recorder 가 nil 이 아니면 Warning event 도 발행. SetupWithManager
// 가 자동 주입하므로 nil 가드는 *방어적 안전망* (테스트에서 직접 reconciler 인스턴스
// 생성 시 Recorder 미주입 가능성).
func (r *BackupJobReconciler) markFailed(bj *postgresv1alpha1.BackupJob, reason, message string) {
	bj.Status.Phase = postgresv1alpha1.BackupJobFailed
	bj.Status.ObservedGeneration = bj.Generation
	setBackupJobCondition(bj, metav1.ConditionFalse, reason, message)
	commonsevents.EmitWarningf(r.Recorder, bj, reason, "%s", message)
}

// statusUpdate persists the in-memory BackupJob status. On a transient
// conflict (HTTP 409) we re-fetch the resource, replay the desired
// status snapshot, and retry once. The original "silently swallow
// conflict and rely on the next reconcile" pattern dropped status
// updates whose follow-up reconcile never fired (PG18 kind smoke
// iter#3 root cause for PostgresDatabase / PostgresUser); mirroring
// the retry pattern here makes BackupJob equally robust.
func (r *BackupJobReconciler) statusUpdate(ctx context.Context, bj *postgresv1alpha1.BackupJob) error {
	desired := bj.Status.DeepCopy()
	err := r.Status().Update(ctx, bj)
	if err == nil {
		ObserveBackupJobMetrics(bj)
		return nil
	}
	if !apierrors.IsConflict(err) {
		return err
	}
	var fresh postgresv1alpha1.BackupJob
	if getErr := r.Get(ctx, client.ObjectKeyFromObject(bj), &fresh); getErr != nil {
		return getErr
	}
	fresh.Status = *desired
	if retryErr := r.Status().Update(ctx, &fresh); retryErr != nil {
		if apierrors.IsConflict(retryErr) {
			return nil // give up after one retry; the next reconcile will refresh.
		}
		return retryErr
	}
	bj.ResourceVersion = fresh.ResourceVersion
	ObserveBackupJobMetrics(bj)
	return nil
}

// setBackupJobCondition은 K8s 표준 meta.SetStatusCondition 패턴을 사용한다
// (status.go의 setCondition과 동일 동작).
func setBackupJobCondition(bj *postgresv1alpha1.BackupJob, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&bj.Status.Conditions, metav1.Condition{
		Type:               BackupJobConditionReady,
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
	if r.SidecarExecutor == nil {
		executor, err := NewKubernetesBackupSidecarExecutor(mgr.GetConfig())
		if err != nil {
			return err
		}
		r.SidecarExecutor = executor
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&postgresv1alpha1.BackupJob{}).
		Owns(&batchv1.Job{}).
		Named("backupjob").
		Complete(r)
}
