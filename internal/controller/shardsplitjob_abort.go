/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

const (
	shardSplitConditionAbortCleanup = "AbortCleanup"
	shardSplitReasonCleanupRunning  = "AbortCleanupRunning"
	shardSplitReasonCleanupComplete = "AbortCleanupComplete"
	shardSplitReasonCleanupFailed   = "AbortCleanupFailed"
)

func (r *ShardSplitJobReconciler) reconcileTerminalAbortCleanup(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (ctrl.Result, error) {
	if abortCleanupCompleted(ssj) {
		return ctrl.Result{}, nil
	}

	done, failure, err := r.reconcileAbortCleanup(ctx, ssj)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case failure != "":
		setAbortCleanupCondition(ssj, metav1.ConditionFalse, shardSplitReasonCleanupFailed, failure)
		if err := r.Status().Update(ctx, ssj); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	case !done:
		setAbortCleanupCondition(ssj, metav1.ConditionFalse, shardSplitReasonCleanupRunning, "waiting for cdc-abort cleanup jobs")
		if err := r.Status().Update(ctx, ssj); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	default:
		setAbortCleanupCondition(ssj, metav1.ConditionTrue, shardSplitReasonCleanupComplete, "online resharding CDC artifacts are cleaned up")
		if err := r.Status().Update(ctx, ssj); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
}

func (r *ShardSplitJobReconciler) reconcileAbortCleanup(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (done bool, failure string, err error) {
	if !ssj.Spec.Online {
		return true, "", nil
	}
	started, err := r.cdcJobsStarted(ctx, ssj)
	if err != nil {
		return false, "", err
	}
	if !started {
		return true, "", nil
	}

	done, failure, err = r.reconcileModeJobs(ctx, ssj, "cdc-abort")
	if err != nil || failure != "" || !done {
		return done, failure, err
	}
	if err := r.setWriteBlock(ctx, ssj, false); err != nil {
		return false, fmt.Sprintf("release write-block: %v", err), nil
	}
	return true, "", nil
}

func (r *ShardSplitJobReconciler) cdcJobsStarted(ctx context.Context, ssj *postgresv1alpha1.ShardSplitJob) (bool, error) {
	for _, mode := range []string{"cdc-setup", "cdc-finalize", "cdc-abort"} {
		for i := range ssj.Spec.Targets {
			name := reshardJobName(ssj.Spec.Cluster, ssj.Spec.Targets[i].ShardID, mode)
			var job batchv1.Job
			err := r.Get(ctx, client.ObjectKey{Namespace: ssj.Namespace, Name: name}, &job)
			switch {
			case err == nil:
				return true, nil
			case apierrors.IsNotFound(err):
				continue
			default:
				return false, err
			}
		}
	}
	return false, nil
}

func abortCleanupCompleted(ssj *postgresv1alpha1.ShardSplitJob) bool {
	cond := apimeta.FindStatusCondition(ssj.Status.Conditions, shardSplitConditionAbortCleanup)
	return cond != nil && cond.Status == metav1.ConditionTrue && cond.Reason == shardSplitReasonCleanupComplete
}

func setAbortCleanupCondition(ssj *postgresv1alpha1.ShardSplitJob, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&ssj.Status.Conditions, metav1.Condition{
		Type:               shardSplitConditionAbortCleanup,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ssj.Generation,
	})
}
