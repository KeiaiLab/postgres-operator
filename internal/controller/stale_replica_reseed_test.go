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
