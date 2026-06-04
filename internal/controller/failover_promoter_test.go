/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

func TestPostgresClusterPromotionExecutorExecsPodAndPatchesStatus(t *testing.T) {
	t.Parallel()

	const (
		namespace = "default"
		podName   = "demo-shard-0-1"
	)

	scheme := newScheme(t)
	ctx := context.Background()
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: namespace},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, pod).Build()
	executor := &fakePromotionPodExecutor{}
	reconciler := &PostgresClusterReconciler{
		Client:               c,
		Scheme:               scheme,
		PromotionPodExecutor: executor,
	}
	decision := failover.Decision{
		Failed: true,
		Reason: failover.ReasonNoPrimary,
		PromotionCandidate: &postgresv1alpha1.ShardEndpoint{
			Pod:      podName,
			Endpoint: "demo-shard-0-1.demo-shard-0.default.svc.cluster.local:5432",
			Ready:    true,
		},
	}

	if err := reconciler.executeClusterPromotion(ctx, cluster, "shard-0", decision); err != nil {
		t.Fatalf("executeClusterPromotion: %v", err)
	}

	if executor.called != 1 {
		t.Fatalf("Exec called %d times, want 1", executor.called)
	}
	if executor.target.Namespace != namespace || executor.target.Pod != podName || executor.target.Container != pgContainerName {
		t.Fatalf("target = %+v, want promotion candidate postgres container", executor.target)
	}
	command := strings.Join(executor.command, " ")
	for _, want := range []string{"standby.signal", "pg_ctl", "promote", "pg_is_in_recovery"} {
		if !strings.Contains(command, want) {
			t.Fatalf("promotion command %q missing %q", command, want)
		}
	}

	var got corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, &got); err != nil {
		t.Fatalf("get patched pod: %v", err)
	}
	raw := got.Annotations[statusapi.AnnotationKey]
	if raw == "" {
		t.Fatal("instance-status annotation missing after promotion")
	}
	var st statusapi.Status
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("decode instance status: %v", err)
	}
	if st.Role != statusapi.RolePrimary || !st.Ready {
		t.Fatalf("status role/ready = %s/%v, want primary/true", st.Role, st.Ready)
	}
	if st.Endpoint != decision.PromotionCandidate.Endpoint {
		t.Fatalf("status endpoint = %q, want %q", st.Endpoint, decision.PromotionCandidate.Endpoint)
	}
}

type fakePromotionPodExecutor struct {
	called  int
	target  BackupSidecarTarget
	command []string
	err     error
}

func (f *fakePromotionPodExecutor) Exec(
	_ context.Context,
	target BackupSidecarTarget,
	command []string,
) ([]byte, error) {
	f.called++
	f.target = target
	f.command = append([]string{}, command...)
	return nil, f.err
}

// TestPostgresClusterPromotionUnfencesTargetPVC pins the fix for the
// all-members-fenced recovery deadlock (#200): the operator must unfence the
// chosen promotion target's PVC so its crash-looping container can recover.
func TestPostgresClusterPromotionUnfencesTargetPVC(t *testing.T) {
	t.Parallel()

	const (
		namespace = "default"
		podName   = "demo-shard-0-1"
		pvcName   = "data-demo-shard-0-1"
	)

	scheme := newScheme(t)
	ctx := context.Background()
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: namespace},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
			Labels:    map[string]string{fencing.FenceLabelKey: fencing.FenceLabelValue},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, pod, pvc).Build()
	reconciler := &PostgresClusterReconciler{
		Client:               c,
		Scheme:               scheme,
		PromotionPodExecutor: &fakePromotionPodExecutor{},
	}
	decision := failover.Decision{
		Failed: true,
		Reason: failover.ReasonNoPrimary,
		PromotionCandidate: &postgresv1alpha1.ShardEndpoint{
			Pod:      podName,
			Endpoint: "demo-shard-0-1.demo-shard-0.default.svc.cluster.local:5432",
			Ready:    true,
		},
	}

	if err := reconciler.executeClusterPromotion(ctx, cluster, "shard-0", decision); err != nil {
		t.Fatalf("executeClusterPromotion: %v", err)
	}

	var got corev1.PersistentVolumeClaim
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pvcName}, &got); err != nil {
		t.Fatalf("get pvc: %v", err)
	}
	if v, ok := got.Labels[fencing.FenceLabelKey]; ok {
		t.Fatalf("target PVC still fenced (label=%q); promotion must unfence the target", v)
	}
}

// TestPostgresClusterPromotionFencesNonTargetMembers pins the fix for #220
// (failback data loss): on promotion the operator must fence every shard member
// except the new primary, completing the "all members fenced except the single
// promoted primary" model. A former primary that boots back before the operator
// propagates the new PRIMARY_ENDPOINT then finds its PVC fenced and fails closed
// at VerifyNotFenced (exit 2) instead of re-acquiring the lease and rewinding
// away the new primary's post-failover writes.
func TestPostgresClusterPromotionFencesNonTargetMembers(t *testing.T) {
	t.Parallel()

	const (
		namespace = "default"
		targetPod = "demo-shard-0-1"
	)

	scheme := newScheme(t)
	ctx := context.Background()
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: namespace},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: targetPod, Namespace: namespace},
	}
	mkPVC := func(name string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		}
	}
	// data-demo-shard-0-0 = former primary, -1 = promotion target, -2 = healthy
	// standby, and a different shard's PVC that must be left untouched.
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		cluster, pod,
		mkPVC("data-demo-shard-0-0"),
		mkPVC("data-demo-shard-0-1"),
		mkPVC("data-demo-shard-0-2"),
		mkPVC("data-demo-shard-1-0"),
	).Build()
	reconciler := &PostgresClusterReconciler{
		Client:               c,
		Scheme:               scheme,
		PromotionPodExecutor: &fakePromotionPodExecutor{},
	}
	decision := failover.Decision{
		Failed: true,
		Reason: failover.ReasonPrimaryNotReady,
		PromotionCandidate: &postgresv1alpha1.ShardEndpoint{
			Pod:      targetPod,
			Endpoint: "demo-shard-0-1.demo-shard-0.default.svc.cluster.local:5432",
			Ready:    true,
		},
	}

	if err := reconciler.executeClusterPromotion(ctx, cluster, "shard-0", decision); err != nil {
		t.Fatalf("executeClusterPromotion: %v", err)
	}

	fenced := func(name string) bool {
		var got corev1.PersistentVolumeClaim
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &got); err != nil {
			t.Fatalf("get pvc %q: %v", name, err)
		}
		return got.Labels[fencing.FenceLabelKey] == fencing.FenceLabelValue
	}

	if !fenced("data-demo-shard-0-0") {
		t.Error("former-primary member PVC data-demo-shard-0-0 must be fenced after promotion (#220)")
	}
	if !fenced("data-demo-shard-0-2") {
		t.Error("non-target member PVC data-demo-shard-0-2 must be fenced after promotion (#220)")
	}
	if fenced("data-demo-shard-0-1") {
		t.Error("promotion target PVC data-demo-shard-0-1 must NOT be fenced")
	}
	if fenced("data-demo-shard-1-0") {
		t.Error("PVC of a different shard must NOT be fenced by a shard-0 promotion")
	}
}
