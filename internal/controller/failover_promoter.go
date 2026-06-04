/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// executeClusterPromotion 은 controller-layer failover 의 mutation 지점이다.
// failover 패키지의 순수 decision/plan 을 유지하면서, 실제 K8s Pod exec 와
// annotation patch 는 controller package 에 격리한다.
func (r *PostgresClusterReconciler) executeClusterPromotion(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shardName string,
	decision failover.Decision,
) error {
	if !decision.Failed {
		return nil
	}
	if cluster == nil {
		return errors.New("postgres cluster is nil")
	}
	if r.PromotionPodExecutor == nil {
		return errors.New("promotion pod executor is not configured")
	}
	promoter := &clusterPodPromoter{
		Namespace:   cluster.Namespace,
		Client:      r.Client,
		PodExecutor: r.PromotionPodExecutor,
		Now:         time.Now,
	}
	return failover.PromoteFromDecision(ctx, shardName, decision, promoter)
}

type clusterPodPromoter struct {
	Namespace   string
	Client      client.Client
	PodExecutor BackupSidecarExecutor
	Now         func() time.Time
}

func (p *clusterPodPromoter) Execute(ctx context.Context, plan failover.PromotionPlan) error {
	if p == nil || p.PodExecutor == nil {
		return errors.New("promotion pod executor is not configured")
	}
	if p.Namespace == "" || plan.Target.Pod == "" {
		return fmt.Errorf("invalid promotion target: namespace=%q pod=%q", p.Namespace, plan.Target.Pod)
	}
	// Clear any fence on the target PVC before promoting. An all-members-fenced
	// state (after split-brain churn) otherwise deadlocks — the in-container
	// promote exec can never succeed against a fenced, crash-looping container.
	// The operator is the promotion authority, so it unfences exactly the chosen
	// target; other members stay fenced, guaranteeing a single primary.
	if err := p.unfenceTargetPVC(ctx, plan.Target.Pod); err != nil {
		return fmt.Errorf("unfence promotion target %q: %w", plan.Target.Pod, err)
	}
	out, err := p.PodExecutor.Exec(ctx, BackupSidecarTarget{
		Namespace: p.Namespace,
		Pod:       plan.Target.Pod,
		Container: pgContainerName,
	}, postgresPromotionCommand())
	if err != nil {
		return err
	}
	if p.Client == nil {
		return nil
	}
	// Only fence other members + record the new primary when a REAL promotion
	// happened. A no-op exec (the candidate was already primary — a spurious
	// promotion from a transient status mis-read during the standby-join /
	// election-settle window, where the running primary momentarily reports a
	// non-Primary role and is mis-listed as a Ready replica candidate) must NOT
	// fence: it would fence the healthy standby (#220 live-drill RCA).
	if !promotionActuallyHappened(out) {
		return nil
	}
	// Fence every other shard member so a former primary that boots back before
	// the operator propagates the new PRIMARY_ENDPOINT finds its PVC fenced and
	// fails closed at VerifyNotFenced (exit 2) instead of re-acquiring the lease
	// and rewinding away the new primary's post-failover writes (#220 failback
	// data loss). Pairs with unfenceTargetPVC to realize the "all members fenced
	// except the single promoted primary" model.
	if err := p.fenceNonTargetMembers(ctx, plan.Target.Pod); err != nil {
		return fmt.Errorf("fence non-target members of %q: %w", plan.Target.Pod, err)
	}
	return p.patchPromotedPodStatus(ctx, plan)
}

func (p *clusterPodPromoter) patchPromotedPodStatus(ctx context.Context, plan failover.PromotionPlan) error {
	var pod corev1.Pod
	key := client.ObjectKey{Namespace: p.Namespace, Name: plan.Target.Pod}
	if err := p.Client.Get(ctx, key, &pod); err != nil {
		return fmt.Errorf("get promoted pod: %w", err)
	}

	before := pod.DeepCopy()
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	raw, err := json.Marshal(statusapi.Status{
		Role:       statusapi.RolePrimary,
		Ready:      true,
		Endpoint:   plan.Target.Endpoint,
		LagBytes:   0,
		LastUpdate: now,
	})
	if err != nil {
		return err
	}
	pod.Annotations[statusapi.AnnotationKey] = string(raw)
	if err := p.Client.Patch(ctx, &pod, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("patch promoted pod status annotation: %w", err)
	}
	return nil
}

// unfenceTargetPVC clears the fence label on the promotion target's PVC
// (`data-<pod>`, per the StatefulSet volumeClaimTemplate). Idempotent: a no-op
// when the PVC is absent or already unfenced. See issue #200 (all-members-fenced
// recovery deadlock).
func (p *clusterPodPromoter) unfenceTargetPVC(ctx context.Context, podName string) error {
	if p.Client == nil {
		return nil
	}
	pvcName := "data-" + podName
	var pvc corev1.PersistentVolumeClaim
	if err := p.Client.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: pvcName}, &pvc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get target pvc %q: %w", pvcName, err)
	}
	if pvc.Labels[fencing.FenceLabelKey] != fencing.FenceLabelValue {
		return nil
	}
	before := pvc.DeepCopy()
	delete(pvc.Labels, fencing.FenceLabelKey)
	return p.Client.Patch(ctx, &pvc, client.MergeFrom(before))
}

// fenceNonTargetMembers fences the data PVC of every member of the target's
// StatefulSet except the promotion target. The fence is inert for a healthy
// standby (its Follower election never reaches the promote path) and is cleared
// by unfenceTargetPVC if that member is later chosen as a promotion target, so
// the steady state remains "exactly one un-fenced primary". See #220.
func (p *clusterPodPromoter) fenceNonTargetMembers(ctx context.Context, targetPod string) error {
	stsName := statefulSetNameFromPod(targetPod)
	if stsName == "" {
		return fmt.Errorf("cannot derive statefulset name from target pod %q", targetPod)
	}
	pvcPrefix := "data-" + stsName + "-"
	targetPVCName := "data-" + targetPod

	var pvcs corev1.PersistentVolumeClaimList
	if err := p.Client.List(ctx, &pvcs, client.InNamespace(p.Namespace)); err != nil {
		return fmt.Errorf("list pvcs: %w", err)
	}
	for i := range pvcs.Items {
		pvc := &pvcs.Items[i]
		if pvc.Name == targetPVCName || !strings.HasPrefix(pvc.Name, pvcPrefix) {
			continue
		}
		if pvc.Labels[fencing.FenceLabelKey] == fencing.FenceLabelValue {
			continue
		}
		before := pvc.DeepCopy()
		if pvc.Labels == nil {
			pvc.Labels = map[string]string{}
		}
		pvc.Labels[fencing.FenceLabelKey] = fencing.FenceLabelValue
		if err := p.Client.Patch(ctx, pvc, client.MergeFrom(before)); err != nil {
			return fmt.Errorf("fence non-target pvc %q: %w", pvc.Name, err)
		}
	}
	return nil
}

// statefulSetNameFromPod strips the trailing "-<ordinal>" from a StatefulSet pod
// name (e.g. "demo-shard-0-1" → "demo-shard-0"). Returns "" when the suffix is
// not a non-negative integer.
func statefulSetNameFromPod(pod string) string {
	idx := strings.LastIndex(pod, "-")
	if idx <= 0 || idx == len(pod)-1 {
		return ""
	}
	for _, c := range pod[idx+1:] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return pod[:idx]
}

// shouldSkipFencedCandidate reports whether automatic promotion of the chosen
// candidate must be skipped because the candidate's PVC is fenced (a known-failed
// primary) while another member is still unfenced and serving. This closes the
// #220 failback: a returned former primary, re-selected during the post-failover
// status churn, would otherwise be unfenced by unfenceTargetPVC and promoted on
// its stale timeline — rewinding away the current primary's post-failover writes
// (live-drill: shard-0-0 re-took, shard-0-1 lost row-2). The #200 all-members-
// fenced deadlock recovery is preserved: when EVERY member is fenced, this returns
// false so unfenceTargetPVC can still break the deadlock.
func (r *PostgresClusterReconciler) shouldSkipFencedCandidate(ctx context.Context, namespace, candidatePod string) (bool, error) {
	stsName := statefulSetNameFromPod(candidatePod)
	if stsName == "" {
		return false, nil
	}
	pvcPrefix := "data-" + stsName + "-"
	candidatePVCName := "data-" + candidatePod

	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs, client.InNamespace(namespace)); err != nil {
		return false, err
	}
	candidateFenced := false
	unfencedMemberExists := false
	for i := range pvcs.Items {
		pvc := &pvcs.Items[i]
		if !strings.HasPrefix(pvc.Name, pvcPrefix) {
			continue
		}
		fenced := pvc.Labels[fencing.FenceLabelKey] == fencing.FenceLabelValue
		if pvc.Name == candidatePVCName {
			candidateFenced = fenced
		}
		if !fenced {
			unfencedMemberExists = true
		}
	}
	return candidateFenced && unfencedMemberExists, nil
}

// promotionActuallyHappened reports whether the promotion exec performed a REAL
// promotion (vs. a no-op because the target was already primary). The exec script
// prints PROMOTE_RESULT=promoted only after pg_ctl promote succeeds; a target that
// was already primary prints PROMOTE_RESULT=noop-already-primary. Gating the fence
// on a real promotion neutralizes spurious promotions of an already-primary
// candidate (#220 live-drill RCA).
func promotionActuallyHappened(execOutput []byte) bool {
	return strings.Contains(string(execOutput), "PROMOTE_RESULT=promoted")
}

func postgresPromotionCommand() []string {
	const script = `set -eu
BIN="${POSTGRES_BIN_DIR:-/usr/lib/postgresql/18/bin}"
DATA="${POSTGRES_DATA_DIR:-/var/lib/postgresql/data/pgdata}"
DSN="${POSTGRES_LOCAL_DSN:-host=/var/run/postgresql user=postgres dbname=postgres}"

is_primary() {
  "$BIN/psql" "$DSN" -Atqc "SELECT NOT pg_is_in_recovery()" | grep -qx t
}

if is_primary; then
  echo "PROMOTE_RESULT=noop-already-primary"
  exit 0
fi

rm -f "$DATA/standby.signal"
"$BIN/pg_ctl" promote -D "$DATA"

i=0
while [ "$i" -lt 30 ]; do
  if is_primary; then
    echo "PROMOTE_RESULT=promoted"
    exit 0
  fi
  i=$((i + 1))
  sleep 1
done

echo "promotion did not reach primary state within 30s" >&2
exit 1
`
	return []string{"sh", "-ec", script}
}

const AnnotationSwitchoverTarget = "postgres.keiailab.io/switchover-target"

func (r *PostgresClusterReconciler) handleSwitchover(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shardStatuses []postgresv1alpha1.ShardStatus,
) error {
	if cluster.Annotations == nil {
		return nil
	}
	targetPod, ok := cluster.Annotations[AnnotationSwitchoverTarget]
	if !ok || targetPod == "" {
		return nil
	}
	if r.PromotionPodExecutor == nil {
		return errors.New("promotion pod executor not configured for switchover")
	}

	var targetEndpoint string
	for _, ss := range shardStatuses {
		if ss.Primary != nil && ss.Primary.Pod == targetPod {
			return fmt.Errorf("switchover target %s is already primary", targetPod)
		}
		for _, rep := range ss.Replicas {
			if rep.Pod == targetPod && rep.Ready {
				targetEndpoint = rep.Endpoint
			}
		}
	}
	if targetEndpoint == "" {
		return fmt.Errorf("switchover target %s not found or not ready", targetPod)
	}

	promoter := &clusterPodPromoter{
		Namespace:   cluster.Namespace,
		Client:      r.Client,
		PodExecutor: r.PromotionPodExecutor,
		Now:         time.Now,
	}
	plan := failover.PromotionPlan{
		Target: failover.PromotionTarget{
			Pod:      targetPod,
			Endpoint: targetEndpoint,
		},
	}
	if err := promoter.Execute(ctx, plan); err != nil {
		return fmt.Errorf("switchover promotion of %s failed: %w", targetPod, err)
	}

	before := cluster.DeepCopy()
	delete(cluster.Annotations, AnnotationSwitchoverTarget)
	if err := r.Patch(ctx, cluster, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("failed to clear switchover annotation: %w", err)
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(cluster, nil, corev1.EventTypeNormal, "SwitchoverCompleted", "SwitchoverCompleted",
			"Switchover to %s completed successfully", targetPod)
	}
	return nil
}
