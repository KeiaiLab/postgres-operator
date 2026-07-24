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

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
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
	oldPrimary := shardPrimaryPod(cluster, shardName)
	if oldPrimary != "" && decision.PromotionCandidate != nil &&
		oldPrimary != decision.PromotionCandidate.Pod &&
		r.podAbsentOrNotReady(ctx, cluster.Namespace, oldPrimary) {
		if err := r.fencePodPVC(ctx, cluster.Namespace, oldPrimary); err != nil {
			return fmt.Errorf("pre-fence failed old primary %q: %w", oldPrimary, err)
		}
	}
	if decision.PromotionCandidate != nil {
		if err := r.promotionCandidateReadyForExec(ctx, cluster.Namespace, decision.PromotionCandidate.Pod); err != nil {
			return err
		}
	}
	if err := failover.PromoteFromDecision(ctx, shardName, decision, promoter); err != nil {
		return err
	}
	// #220: reseed the FAILED old primary so it can only return as a fresh
	// pg_basebackup standby of the new primary — never as a rogue primary booting
	// from stale PGDATA (split-brain). Gated on the old primary being genuinely
	// down/not-ready so a spurious promotion can never destroy a healthy primary.
	if oldPrimary != "" && decision.PromotionCandidate != nil &&
		oldPrimary != decision.PromotionCandidate.Pod &&
		r.podAbsentOrNotReady(ctx, cluster.Namespace, oldPrimary) {
		if err := r.reseedStandby(ctx, cluster, oldPrimary); err != nil {
			return fmt.Errorf("reseed failed old primary %q: %w", oldPrimary, err)
		}
	}
	return nil
}

func (r *PostgresClusterReconciler) fencePodPVC(ctx context.Context, namespace, podName string) error {
	pvcName := "data-" + podName
	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: pvcName}, &pvc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get pvc %q: %w", pvcName, err)
	}
	if pvc.Labels[fencing.FenceLabelKey] == fencing.FenceLabelValue {
		return nil
	}
	before := pvc.DeepCopy()
	if pvc.Labels == nil {
		pvc.Labels = map[string]string{}
	}
	pvc.Labels[fencing.FenceLabelKey] = fencing.FenceLabelValue
	if err := r.Patch(ctx, &pvc, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("fence pvc %q: %w", pvcName, err)
	}
	return nil
}

// shardPrimaryPod returns the recorded primary pod for the named shard (the pod
// that was primary before this reconcile's promotion), or "" if none.
func shardPrimaryPod(cluster *postgresv1alpha1.PostgresCluster, shardName string) string {
	for i := range cluster.Status.Shards {
		if cluster.Status.Shards[i].Name == shardName {
			if cluster.Status.Shards[i].Primary != nil {
				return cluster.Status.Shards[i].Primary.Pod
			}
			return ""
		}
	}
	return ""
}

// podAbsentOrNotReady reports whether the pod is gone or not Ready — the signal
// that a recorded primary has genuinely failed (vs. a transient status flicker).
func (r *PostgresClusterReconciler) podAbsentOrNotReady(ctx context.Context, namespace, podName string) bool {
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, &pod); err != nil {
		return apierrors.IsNotFound(err)
	}
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodReady {
			return pod.Status.Conditions[i].Status != corev1.ConditionTrue
		}
	}
	return true
}

func (r *PostgresClusterReconciler) promotionCandidateReadyForExec(ctx context.Context, namespace, podName string) error {
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, &pod); err != nil {
		return fmt.Errorf("promotion candidate pod %q not ready for promotion exec: %w", podName, err)
	}
	if pod.DeletionTimestamp != nil {
		return fmt.Errorf("promotion candidate pod %q not ready for promotion exec: deleting", podName)
	}
	if pod.Status.Phase != "" && pod.Status.Phase != corev1.PodRunning {
		return fmt.Errorf("promotion candidate pod %q not ready for promotion exec: phase=%s", podName, pod.Status.Phase)
	}

	podReadySeen := false
	podReady := false
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodReady {
			podReadySeen = true
			podReady = pod.Status.Conditions[i].Status == corev1.ConditionTrue
			break
		}
	}
	if !podReadySeen || !podReady {
		return fmt.Errorf("promotion candidate pod %q not ready for promotion exec: PodReady=%t", podName, podReady)
	}

	containerSeen := false
	containerReady := false
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == pgContainerName {
			containerSeen = true
			containerReady = pod.Status.ContainerStatuses[i].Ready
			break
		}
	}
	if !containerSeen || !containerReady {
		return fmt.Errorf("promotion candidate pod %q not ready for promotion exec: container %q Ready=%t", podName, pgContainerName, containerReady)
	}
	return nil
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

// promotionFreshnessLeadBytes 는 fenced 후보가 서빙(unfenced) 멤버보다 이 바이트
// 이상 앞선 WAL 을 보유할 때 #220 failback guard 를 뒤집는(승격 허용) 임계값이다.
// 1 GiB — "명백히 앞섬"의 보수적 하한. 원 사고(7일 stale 서빙자가 fresh 후보 복귀를
// 차단)에서 lead 는 수 GiB~수십 GiB 규모라 이 임계를 크게 상회한다.
const promotionFreshnessLeadBytes int64 = 1 << 30

// shouldSkipFencedCandidate reports whether automatic promotion of the chosen
// candidate must be skipped because the candidate's PVC is fenced (a known-failed
// primary) while another member is still unfenced and serving. This closes the
// #220 failback: a returned former primary, re-selected during the post-failover
// status churn, would otherwise be unfenced by unfenceTargetPVC and promoted on
// its stale timeline — rewinding away the current primary's post-failover writes
// (live-drill: shard-0-0 re-took, shard-0-1 lost row-2). The #200 all-members-
// fenced deadlock recovery is preserved: when EVERY member is fenced, this returns
// false so unfenceTargetPVC can still break the deadlock.
//
// Freshness override (postgres-prod 2026-07-24 RCA): the guard is NOT absolute.
// When the fenced candidate's last-reported WAL position leads the freshest
// unfenced serving member by >= promotionFreshnessLeadBytes, the serving member is
// the STALE one — blocking the candidate would keep serving a frozen replica and
// lose the candidate's committed WAL (the incident: a 7-day-stale replica served
// while a fresh fenced ex-primary was blocked, destroying keycloak/forgejo data).
// In that case the guard is overridden (skip=false) and a Warning event records why.
// The override is fail-safe: it fires ONLY on positive evidence the candidate is
// ahead; if either side's WAL position is unknown (-1/absent), the guard is kept.
func (r *PostgresClusterReconciler) shouldSkipFencedCandidate(ctx context.Context, cluster *postgresv1alpha1.PostgresCluster, candidatePod string) (bool, error) {
	stsName := statefulSetNameFromPod(candidatePod)
	if stsName == "" {
		return false, nil
	}
	namespace := cluster.Namespace
	pvcPrefix := "data-" + stsName + "-"
	candidatePVCName := "data-" + candidatePod

	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs, client.InNamespace(namespace)); err != nil {
		return false, err
	}
	candidateFenced := false
	unfencedMembers := make([]string, 0, len(pvcs.Items))
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
			unfencedMembers = append(unfencedMembers, strings.TrimPrefix(pvc.Name, "data-"))
		}
	}
	if !candidateFenced || len(unfencedMembers) == 0 {
		// #200 all-members-fenced deadlock recovery, or an unfenced candidate → proceed.
		return false, nil
	}

	// Freshness override: compare the candidate's reported WAL position against the
	// freshest serving member. Only override on positive evidence of a lead.
	candWAL := r.reportedWALPosition(ctx, namespace, candidatePod)
	if candWAL < 0 {
		return true, nil // candidate position unknown → cannot prove it leads → keep guard
	}
	servingWAL := int64(-1)
	servingPod := ""
	for _, pod := range unfencedMembers {
		if w := r.reportedWALPosition(ctx, namespace, pod); w > servingWAL {
			servingWAL, servingPod = w, pod
		}
	}
	if servingWAL < 0 {
		return true, nil // no serving member reports a position → cannot compare → keep guard
	}
	// ponytail: absolute WAL-byte lead is the freshness comparator; the "or 1h" time
	// arm is unnecessary — a frozen replica's reported position stops advancing, so
	// the byte lead already grows past the threshold. Add a time comparator only if a
	// member can serve stale data while reporting a fresh position (it cannot: the
	// reported position IS its replay/write LSN). Cross-timeline LSN compare is a
	// heuristic, sound at the >=1GiB "clearly ahead" scale.
	if lead := candWAL - servingWAL; lead >= promotionFreshnessLeadBytes {
		commonsevents.EmitWarningf(r.Recorder, cluster, "FailbackGuardOverridden",
			"promoting fenced candidate %s: leads serving member %s by %d bytes of WAL (>= %d) — serving member is stale; guard overridden to prevent fresh-data loss (#220)",
			candidatePod, servingPod, lead, promotionFreshnessLeadBytes)
		return false, nil
	}
	return true, nil
}

// reportedWALPosition returns the pod's last-reported absolute WAL position
// (statusapi WALLSNBytes) or -1 when the pod, its status annotation, or the
// measurement is absent — the safe "cannot compare" sentinel for the #220
// freshness guard. A fenced/crash-looping candidate still carries the annotation
// it last patched before failing, i.e. its fresh pre-failure WAL position.
func (r *PostgresClusterReconciler) reportedWALPosition(ctx context.Context, namespace, podName string) int64 {
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, &pod); err != nil {
		return -1
	}
	st, ok := parsePodStatus(&pod)
	if !ok {
		return -1
	}
	return st.WALLSNBytes
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

PROMOTED="$("$BIN/psql" "$DSN" -v ON_ERROR_STOP=1 -Atqc "SELECT pg_promote(true, 30)")"
PROMOTED="$(printf "%s" "$PROMOTED" | tr -d '[:space:]')"
if [ "$PROMOTED" != "t" ] && ! is_primary; then
  echo "pg_promote did not reach primary state within 30s (result=$PROMOTED)" >&2
  exit 1
fi

# #220: mutate PGDATA only after promotion succeeds. A failed exec must leave
# standby.signal and the promoted marker untouched; otherwise a restarted standby
# enters the Real elector branch and can rejoin the lease race before promotion.
rm -f "$DATA/standby.signal" "$DATA/.keiailab-restart-primary-as-standby"
# #220: durable marker — this PGDATA is now an operator-promoted primary. The
# bootstrap init container (builders.go) reads it on restart and refuses to restore
# standby.signal from a stale PRIMARY_ENDPOINT, so the new primary never rewinds
# itself back to the old timeline. A former primary that must leave the role is
# fenced (fail-closed) and reseeded, never silently demoted. Name must match
# builders.go promotedPrimaryMarker.
touch "$DATA/.keiailab-promoted-primary"

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

	commonsevents.Emitf(r.Recorder, cluster, "SwitchoverCompleted",
		"Switchover to %s completed successfully", targetPod)
	return nil
}
