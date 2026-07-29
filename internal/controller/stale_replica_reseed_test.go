/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// TestReconcileStaleReplicas_ReseedsStuckStandby pins #205: a standby that is
// not-ready well past the boot window, with a ready primary, is re-seeded
// (pod + PVC deleted so the StatefulSet rebuilds it via fresh pg_basebackup).
func TestReconcileStaleReplicas_ReseedsStuckStandby(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()
	now := time.Now()

	cluster := &postgresv1alpha1.PostgresCluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns}}
	stalePod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "demo-shard-0-1", Namespace: ns,
		CreationTimestamp: metav1.NewTime(now.Add(-20 * time.Minute)),
	}}
	stalePVC := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-demo-shard-0-1", Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, stalePod, stalePVC).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	shardStatuses := []postgresv1alpha1.ShardStatus{{
		Name: "shard-0", Ordinal: 0,
		Primary:  &postgresv1alpha1.ShardEndpoint{Pod: "demo-shard-0-0", Ready: true},
		Replicas: []postgresv1alpha1.ShardEndpoint{{Pod: "demo-shard-0-1", Ready: false}},
	}}

	if err := r.reconcileStaleReplicas(ctx, cluster, shardStatuses, now); err != nil {
		t.Fatalf("reconcileStaleReplicas: %v", err)
	}

	var pod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-1"}, &pod); err == nil {
		t.Fatal("stale standby pod should have been deleted for re-seed")
	}
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "data-demo-shard-0-1"}, &pvc); err == nil {
		t.Fatal("stale standby PVC should have been deleted for re-seed")
	}
	var got postgresv1alpha1.PostgresCluster
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo"}, &got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	if got.Annotations[reseedAnnotationPrefix+"demo-shard-0-1"] == "" {
		t.Fatal("re-seed cooldown annotation not recorded")
	}
}

// TestReconcileStaleReplicas_SkipsYoungAndNoPrimary verifies the conservative
// guards: a not-ready standby within the boot window is left alone, and nothing
// is re-seeded without a ready primary.
func TestReconcileStaleReplicas_SkipsYoungAndNoPrimary(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()
	now := time.Now()

	cluster := &postgresv1alpha1.PostgresCluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns}}
	youngPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "demo-shard-0-1", Namespace: ns,
		CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
	}}
	oldPodNoPrimary := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "demo-shard-1-1", Namespace: ns,
		CreationTimestamp: metav1.NewTime(now.Add(-20 * time.Minute)),
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, youngPod, oldPodNoPrimary).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	shardStatuses := []postgresv1alpha1.ShardStatus{
		{ // young not-ready standby (still booting) — must survive
			Name:     "shard-0",
			Primary:  &postgresv1alpha1.ShardEndpoint{Pod: "demo-shard-0-0", Ready: true},
			Replicas: []postgresv1alpha1.ShardEndpoint{{Pod: "demo-shard-0-1", Ready: false}},
		},
		{ // old not-ready standby but NO ready primary — must survive
			Name:     "shard-1",
			Primary:  nil,
			Replicas: []postgresv1alpha1.ShardEndpoint{{Pod: "demo-shard-1-1", Ready: false}},
		},
	}

	if err := r.reconcileStaleReplicas(ctx, cluster, shardStatuses, now); err != nil {
		t.Fatalf("reconcileStaleReplicas: %v", err)
	}
	for _, name := range []string{"demo-shard-0-1", "demo-shard-1-1"} {
		var pod corev1.Pod
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &pod); err != nil {
			t.Fatalf("pod %s must NOT be re-seeded (conservative guard)", name)
		}
	}
}

// TestReconcileRoguePrimaries_ReseedWithBackoff pins the #220 follow-up: the rogue-
// primary reseed path (destructive pod+PVC delete) must apply the same per-pod
// backoff + audit as the stale-standby path. A rogue primary is reseeded once, then
// the cooldown annotation blocks a second reseed within reseedCooldown (so a
// basebackup that keeps failing can't drive an unbounded delete loop).
func TestReconcileRoguePrimaries_ReseedWithBackoff(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()
	now := time.Now()

	cluster := &postgresv1alpha1.PostgresCluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns}}
	roguePod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "demo-shard-0-1", Namespace: ns}}
	roguePVC := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-demo-shard-0-1", Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, roguePod, roguePVC).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	shardStatuses := []postgresv1alpha1.ShardStatus{{
		Name: "shard-0", Ordinal: 0,
		Primary: &postgresv1alpha1.ShardEndpoint{Pod: "demo-shard-0-0", Ready: true},
		Replicas: []postgresv1alpha1.ShardEndpoint{
			{Pod: "demo-shard-0-1", Ready: false, Reason: roguePrimaryReason},
		},
	}}

	// First pass: reseed (pod+PVC deleted) + cooldown annotation recorded.
	if err := r.reconcileRoguePrimaries(ctx, cluster, shardStatuses, now); err != nil {
		t.Fatalf("reconcileRoguePrimaries: %v", err)
	}
	var pod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-1"}, &pod); err == nil {
		t.Fatal("rogue primary pod should have been deleted for re-seed")
	}
	var got postgresv1alpha1.PostgresCluster
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo"}, &got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	if got.Annotations[reseedAnnotationPrefix+"demo-shard-0-1"] == "" {
		t.Fatal("reseed cooldown annotation must be recorded for the rogue primary")
	}

	// Recreate the pod (as the StatefulSet would) and run again within cooldown:
	// the backoff must skip the reseed, leaving the pod intact.
	recreated := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "demo-shard-0-1", Namespace: ns}}
	if err := c.Create(ctx, recreated); err != nil {
		t.Fatalf("recreate pod: %v", err)
	}
	if err := r.reconcileRoguePrimaries(ctx, &got, shardStatuses, now.Add(time.Minute)); err != nil {
		t.Fatalf("reconcileRoguePrimaries (cooldown): %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-1"}, &pod); err != nil {
		t.Fatal("rogue primary pod must survive a second reseed within cooldown (backoff)")
	}
}

// TestReseedStandby_ForceDeletesStuckPodOnDeadNode pins #226 F2: a pod that is
// already Terminating (finalizer present) well past
// stuckTerminatingForceDeleteThreshold, on a Node that independently reports
// NotReady, and that is itself confirmed not-Ready, must be escalated to a
// force delete — mirroring the manual `kubectl delete pod --grace-period=0
// --force` recovery this previously required. Without this, a dead node's
// kubelet never acknowledging the delete left reconcileStaleReplicas
// re-emitting StandbyReseeded every reseedCooldown forever.
func TestReseedStandby_ForceDeletesStuckPodOnDeadNode(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()
	now := time.Now()

	deadNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-dead"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}},
		},
	}
	stuckPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-shard-0-0", Namespace: ns,
			Finalizers:        []string{"keiailab.io/test-stuck"},
			DeletionTimestamp: &metav1.Time{Time: now.Add(-5 * time.Minute)},
		},
		Spec: corev1.PodSpec{NodeName: "node-dead"},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
		},
	}
	cluster := &postgresv1alpha1.PostgresCluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deadNode, stuckPod, cluster).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	if err := r.reseedStandby(ctx, cluster, "demo-shard-0-0", now); err != nil {
		t.Fatalf("reseedStandby: %v", err)
	}

	var got corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-0"}, &got); err == nil {
		t.Fatalf("stuck pod on a NotReady node should have been force-deleted, still present: %+v", got)
	}
}

// TestReseedStandby_DoesNotForceDeleteWhenNodeIsReady verifies the safety gate:
// a pod stuck Terminating past the threshold must NOT be force-deleted if its
// node independently reports Ready — a merely-slow (not dead) node could still
// hold the volume attached, and force-deleting the API object risks the
// StatefulSet recreating the pod elsewhere while the healthy node still has it
// mounted (multi-attach).
func TestReseedStandby_DoesNotForceDeleteWhenNodeIsReady(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()
	now := time.Now()

	readyNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-ready"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	stuckPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-shard-0-0", Namespace: ns,
			Finalizers:        []string{"keiailab.io/test-stuck"},
			DeletionTimestamp: &metav1.Time{Time: now.Add(-5 * time.Minute)},
		},
		Spec: corev1.PodSpec{NodeName: "node-ready"},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
		},
	}
	cluster := &postgresv1alpha1.PostgresCluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(readyNode, stuckPod, cluster).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	if err := r.reseedStandby(ctx, cluster, "demo-shard-0-0", now); err != nil {
		t.Fatalf("reseedStandby: %v", err)
	}

	var got corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-0"}, &got); err != nil {
		t.Fatalf("pod should still exist (finalizer holds it, safety gate must not force-delete on a Ready node): %v", err)
	}
	if len(got.Finalizers) == 0 {
		t.Fatal("finalizers must not have been cleared — force delete must not have been attempted on a Ready node")
	}
}

// TestReseedStandby_DoesNotForceDeleteFreshDeletionTimestamp verifies the
// stuckTerminatingForceDeleteThreshold gate: a Delete issued moments ago must
// not be escalated to a force delete even on a NotReady node — normal
// graceful termination needs a chance to complete first.
func TestReseedStandby_DoesNotForceDeleteFreshDeletionTimestamp(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()
	now := time.Now()

	deadNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-dead"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}},
		},
	}
	freshPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-shard-0-0", Namespace: ns,
			Finalizers:        []string{"keiailab.io/test-stuck"},
			DeletionTimestamp: &metav1.Time{Time: now.Add(-10 * time.Second)},
		},
		Spec: corev1.PodSpec{NodeName: "node-dead"},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
		},
	}
	cluster := &postgresv1alpha1.PostgresCluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deadNode, freshPod, cluster).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	if err := r.reseedStandby(ctx, cluster, "demo-shard-0-0", now); err != nil {
		t.Fatalf("reseedStandby: %v", err)
	}

	var got corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-0"}, &got); err != nil {
		t.Fatalf("pod should still exist — DeletionTimestamp is too fresh to escalate: %v", err)
	}
	if len(got.Finalizers) == 0 {
		t.Fatal("finalizers must not have been cleared — force delete must not fire before the stuck-terminating threshold")
	}
}
