/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// TestReconcilePrimaryRoleLabels_LabelsPrimaryAndReplica pins #226 F1: a
// freshly-observed shard (no instance-role label on either pod yet) converges
// both pods to the correct label in one call.
func TestReconcilePrimaryRoleLabels_LabelsPrimaryAndReplica(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()

	primaryPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "demo-shard-0-1", Namespace: ns}}
	replicaPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "demo-shard-0-0", Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(primaryPod, replicaPod).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	shardStatuses := []postgresv1alpha1.ShardStatus{{
		Name: "shard-0", Ordinal: 0,
		Primary:  &postgresv1alpha1.ShardEndpoint{Pod: "demo-shard-0-1", Ready: true},
		Replicas: []postgresv1alpha1.ShardEndpoint{{Pod: "demo-shard-0-0", Ready: true}},
	}}

	synced, err := r.reconcilePrimaryRoleLabels(ctx, &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
	}, shardStatuses)
	if err != nil {
		t.Fatalf("reconcilePrimaryRoleLabels: %v", err)
	}
	if !synced {
		t.Fatal("synced = false, want true (both patches should have succeeded)")
	}

	var gotPrimary corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-1"}, &gotPrimary); err != nil {
		t.Fatalf("get primary pod: %v", err)
	}
	if gotPrimary.Labels[InstanceRoleLabelKey] != InstanceRoleLabelPrimary {
		t.Fatalf("primary pod instance-role label = %q, want %q", gotPrimary.Labels[InstanceRoleLabelKey], InstanceRoleLabelPrimary)
	}

	var gotReplica corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-0"}, &gotReplica); err != nil {
		t.Fatalf("get replica pod: %v", err)
	}
	if gotReplica.Labels[InstanceRoleLabelKey] != InstanceRoleLabelReplica {
		t.Fatalf("replica pod instance-role label = %q, want %q", gotReplica.Labels[InstanceRoleLabelKey], InstanceRoleLabelReplica)
	}
}

// TestReconcilePrimaryRoleLabels_FlipsEvenWhenDemotedPrimaryIsStuckTerminating
// reproduces the INC 2026-07-11 core mechanism: the old primary carries a
// stale instance-role=primary label and is wedged Terminating (dead node,
// finalizer never clears), while the newly-promoted primary carries no label
// yet. F1 requires the label state to converge to what the CR status/lease
// already say regardless of whether the stuck pod's deletion ever completes.
func TestReconcilePrimaryRoleLabels_FlipsEvenWhenDemotedPrimaryIsStuckTerminating(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()

	oldPrimary := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "demo-shard-0-0", Namespace: ns,
		Labels:     map[string]string{InstanceRoleLabelKey: InstanceRoleLabelPrimary},
		Finalizers: []string{"keiailab.io/test-stuck"}, // keeps the pod around after Delete()
	}}
	newPrimary := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "demo-shard-0-1", Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(oldPrimary, newPrimary).Build()

	// Simulate "a prior reconcile already tried to delete the dead node's old
	// primary pod" — the fake client honors finalizers, so this only stamps
	// DeletionTimestamp and leaves the object Get-able, exactly like a pod
	// wedged on an unreachable kubelet.
	if err := c.Delete(ctx, oldPrimary); err != nil {
		t.Fatalf("simulate stuck delete: %v", err)
	}
	var stuck corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-0"}, &stuck); err != nil {
		t.Fatalf("get stuck pod: %v", err)
	}
	if stuck.DeletionTimestamp == nil {
		t.Fatal("test setup invalid: stuck pod must have DeletionTimestamp set")
	}

	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}
	shardStatuses := []postgresv1alpha1.ShardStatus{{
		Name: "shard-0", Ordinal: 0,
		Primary:  &postgresv1alpha1.ShardEndpoint{Pod: "demo-shard-0-1", Ready: true},
		Replicas: []postgresv1alpha1.ShardEndpoint{{Pod: "demo-shard-0-0", Ready: false}},
	}}

	synced, err := r.reconcilePrimaryRoleLabels(ctx, &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
	}, shardStatuses)
	if err != nil {
		t.Fatalf("reconcilePrimaryRoleLabels: %v", err)
	}
	if !synced {
		t.Fatal("synced = false, want true (label patch does not require the stuck delete to finish)")
	}

	var gotNewPrimary corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-1"}, &gotNewPrimary); err != nil {
		t.Fatalf("get new primary: %v", err)
	}
	if gotNewPrimary.Labels[InstanceRoleLabelKey] != InstanceRoleLabelPrimary {
		t.Fatalf("new primary instance-role label = %q, want %q (must not be blocked by the stuck old primary)",
			gotNewPrimary.Labels[InstanceRoleLabelKey], InstanceRoleLabelPrimary)
	}

	var gotOldPrimary corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-0"}, &gotOldPrimary); err != nil {
		t.Fatalf("get old (stuck terminating) primary: %v", err)
	}
	if gotOldPrimary.Labels[InstanceRoleLabelKey] != InstanceRoleLabelReplica {
		t.Fatalf("demoted pod instance-role label = %q, want %q even though it is stuck Terminating",
			gotOldPrimary.Labels[InstanceRoleLabelKey], InstanceRoleLabelReplica)
	}
}

// TestReconcilePrimaryRoleLabels_SyncedFalseWhenPrimaryPatchFails pins #226 F3:
// when the Ready primary's label patch itself fails, reconcilePrimaryRoleLabels
// must report synced=false so applyClusterConditions can surface
// ConditionPrimaryRoleSynced=False instead of silently hiding the gap.
func TestReconcilePrimaryRoleLabels_SyncedFalseWhenPrimaryPatchFails(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()

	primaryPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "demo-shard-0-1", Namespace: ns}}
	injectedErr := errors.New("injected patch failure")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(primaryPod).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if obj.GetName() == "demo-shard-0-1" {
					return injectedErr
				}
				return cli.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	shardStatuses := []postgresv1alpha1.ShardStatus{{
		Name: "shard-0", Ordinal: 0,
		Primary: &postgresv1alpha1.ShardEndpoint{Pod: "demo-shard-0-1", Ready: true},
	}}

	synced, err := r.reconcilePrimaryRoleLabels(ctx, &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
	}, shardStatuses)
	if err == nil {
		t.Fatal("expected error to be surfaced from the failed primary label patch")
	}
	if synced {
		t.Fatal("synced = true, want false when the Ready primary's label patch failed")
	}
}

// TestReconcilePrimaryRoleLabels_NoReadyPrimaryIsVacuouslySynced verifies that
// a shard with no Ready primary yet (e.g. still bootstrapping) does not count
// against synced — there is nothing to converge yet, so this must not be
// mistaken for the #226 gap.
func TestReconcilePrimaryRoleLabels_NoReadyPrimaryIsVacuouslySynced(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	shardStatuses := []postgresv1alpha1.ShardStatus{{Name: "shard-0", Ordinal: 0, Primary: nil}}
	synced, err := r.reconcilePrimaryRoleLabels(ctx, &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
	}, shardStatuses)
	if err != nil {
		t.Fatalf("reconcilePrimaryRoleLabels: %v", err)
	}
	if !synced {
		t.Fatal("synced = false, want true (no Ready primary yet ⇒ vacuously nothing to sync)")
	}
}
