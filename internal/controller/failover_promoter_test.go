/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
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
	pod := readyPromotionPod(namespace, podName)
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
	for _, want := range []string{"standby.signal", ".keiailab-restart-primary-as-standby", "pg_promote", "promote", "pg_is_in_recovery"} {
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

func TestPostgresPromotionCommandMutatesPGDATAOnlyAfterSQLPromote(t *testing.T) {
	t.Parallel()

	command := strings.Join(postgresPromotionCommand(), "\n")
	promoteIdx := strings.Index(command, "pg_promote(true, 30)")
	if promoteIdx < 0 {
		t.Fatalf("promotion command must use PostgreSQL SQL promotion API: %s", command)
	}
	if strings.Contains(command, "pg_ctl") {
		t.Fatalf("promotion command must not use pg_ctl promote in the operator exec path: %s", command)
	}
	for _, mutation := range []string{
		`rm -f "$DATA/standby.signal"`,
		`touch "$DATA/.keiailab-promoted-primary"`,
	} {
		mutationIdx := strings.Index(command, mutation)
		if mutationIdx < 0 {
			t.Fatalf("promotion command missing PGDATA mutation %q: %s", mutation, command)
		}
		if mutationIdx < promoteIdx {
			t.Fatalf("promotion command mutates PGDATA before promote succeeds: %q", mutation)
		}
	}
}

type fakePromotionPodExecutor struct {
	called  int
	target  BackupSidecarTarget
	command []string
	out     []byte
	err     error
	onExec  func(context.Context) error
}

func (f *fakePromotionPodExecutor) Exec(
	ctx context.Context,
	target BackupSidecarTarget,
	command []string,
) ([]byte, error) {
	f.called++
	f.target = target
	f.command = append([]string{}, command...)
	if f.onExec != nil {
		if err := f.onExec(ctx); err != nil {
			return nil, err
		}
	}
	out := f.out
	if out == nil {
		// Default to a real promotion so promotion/fence tests exercise the
		// post-promotion path (fence + status patch).
		out = []byte("PROMOTE_RESULT=promoted\n")
	}
	return out, f.err
}

func readyPromotionPod(namespace, podName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  pgContainerName,
				Ready: true,
			}},
		},
	}
}

// TestPostgresClusterPromotionPreFencesFailedOldPrimaryBeforeExec pins the live
// failover race: StatefulSet can recreate the old primary ordinal before the
// standby promotion settles. The failed old primary's PVC must be fenced before
// the promotion exec is attempted, so a self-healed old-primary Pod fails closed
// at VerifyNotFenced instead of re-acquiring primary identity.
func TestPostgresClusterPromotionPreFencesFailedOldPrimaryBeforeExec(t *testing.T) {
	t.Parallel()

	const (
		namespace     = "default"
		oldPrimaryPod = "demo-shard-0-0"
		targetPod     = "demo-shard-0-1"
		oldPrimaryPVC = "data-demo-shard-0-0"
	)

	scheme := newScheme(t)
	ctx := context.Background()
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: namespace},
		Status: postgresv1alpha1.PostgresClusterStatus{
			Shards: []postgresv1alpha1.ShardStatus{{
				Name: "shard-0",
				Primary: &postgresv1alpha1.ShardEndpoint{
					Pod:      oldPrimaryPod,
					Endpoint: "demo-shard-0-0.demo-shard-0.default.svc.cluster.local:5432",
					Ready:    false,
				},
			}},
		},
	}
	target := readyPromotionPod(namespace, targetPod)
	oldPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: oldPrimaryPVC, Namespace: namespace},
	}
	targetPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-" + targetPod, Namespace: namespace},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, target, oldPVC, targetPVC).Build()
	executor := &fakePromotionPodExecutor{
		onExec: func(ctx context.Context) error {
			var got corev1.PersistentVolumeClaim
			if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: oldPrimaryPVC}, &got); err != nil {
				return fmt.Errorf("get old primary pvc before promotion exec: %w", err)
			}
			if got.Labels[fencing.FenceLabelKey] != fencing.FenceLabelValue {
				return fmt.Errorf("old primary PVC must be fenced before promotion exec, labels=%v", got.Labels)
			}
			return nil
		},
	}
	reconciler := &PostgresClusterReconciler{
		Client:               c,
		Scheme:               scheme,
		PromotionPodExecutor: executor,
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
}

func TestPostgresClusterPromotionSkipsExecWhenCandidatePodNotReady(t *testing.T) {
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
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionFalse,
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  pgContainerName,
				Ready: false,
			}},
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-" + targetPod, Namespace: namespace},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, pod, pvc).Build()
	executor := &fakePromotionPodExecutor{}
	reconciler := &PostgresClusterReconciler{
		Client:               c,
		Scheme:               scheme,
		PromotionPodExecutor: executor,
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

	err := reconciler.executeClusterPromotion(ctx, cluster, "shard-0", decision)
	if err == nil {
		t.Fatal("executeClusterPromotion must reject a Kubernetes-not-ready promotion candidate")
	}
	if executor.called != 0 {
		t.Fatalf("Exec called %d times, want 0 for a Kubernetes-not-ready candidate", executor.called)
	}
	if !strings.Contains(err.Error(), "not ready for promotion exec") {
		t.Fatalf("error = %q, want not-ready-for-exec reason", err.Error())
	}
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
	pod := readyPromotionPod(namespace, podName)
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
	pod := readyPromotionPod(namespace, targetPod)
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

// TestPostgresClusterPromotionNoopDoesNotFence verifies the #220 live-drill guard:
// a spurious promotion whose candidate is already primary (exec returns
// PROMOTE_RESULT=noop-already-primary) must NOT fence other members nor patch the
// target's status — otherwise a transient status mis-read during standby join
// would fence the healthy standby.
func TestPostgresClusterPromotionNoopDoesNotFence(t *testing.T) {
	t.Parallel()

	const (
		namespace = "default"
		targetPod = "demo-shard-0-0" // already-primary candidate (spurious)
	)

	scheme := newScheme(t)
	ctx := context.Background()
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: namespace},
	}
	pod := readyPromotionPod(namespace, targetPod)
	mkPVC := func(name string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		cluster, pod,
		mkPVC("data-demo-shard-0-0"),
		mkPVC("data-demo-shard-0-1"),
	).Build()
	reconciler := &PostgresClusterReconciler{
		Client:               c,
		Scheme:               scheme,
		PromotionPodExecutor: &fakePromotionPodExecutor{out: []byte("PROMOTE_RESULT=noop-already-primary\n")},
	}
	decision := failover.Decision{
		Failed: true,
		Reason: failover.ReasonNoPrimary,
		PromotionCandidate: &postgresv1alpha1.ShardEndpoint{
			Pod:      targetPod,
			Endpoint: "demo-shard-0-0.demo-shard-0.default.svc.cluster.local:5432",
			Ready:    true,
		},
	}

	if err := reconciler.executeClusterPromotion(ctx, cluster, "shard-0", decision); err != nil {
		t.Fatalf("executeClusterPromotion: %v", err)
	}

	for _, name := range []string{"data-demo-shard-0-0", "data-demo-shard-0-1"} {
		var got corev1.PersistentVolumeClaim
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &got); err != nil {
			t.Fatalf("get pvc %q: %v", name, err)
		}
		if got.Labels[fencing.FenceLabelKey] == fencing.FenceLabelValue {
			t.Errorf("no-op promotion must not fence %q", name)
		}
	}
	var gotPod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: targetPod}, &gotPod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if _, ok := gotPod.Annotations[statusapi.AnnotationKey]; ok {
		t.Error("no-op promotion must not patch the target instance-status annotation")
	}
}

// TestShouldSkipFencedCandidate pins the #220 failback guard: a fenced candidate
// (a known-failed primary that has returned) must be skipped while an unfenced
// member is still serving, so the operator never unfences+re-promotes it on a
// stale timeline. The #200 all-members-fenced deadlock recovery must still proceed.
func TestShouldSkipFencedCandidate(t *testing.T) {
	t.Parallel()
	const namespace = "default"
	scheme := newScheme(t)
	ctx := context.Background()

	mkPVC := func(name string, fenced bool) *corev1.PersistentVolumeClaim {
		l := map[string]string{}
		if fenced {
			l[fencing.FenceLabelKey] = fencing.FenceLabelValue
		}
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: l},
		}
	}

	cases := []struct {
		name       string
		candidate  string
		pvc0Fenced bool
		pvc1Fenced bool
		wantSkip   bool
	}{
		{"fenced candidate + unfenced member → skip", "demo-shard-0-0", true, false, true},
		{"unfenced candidate → proceed", "demo-shard-0-1", true, false, false},
		{"all members fenced → proceed (deadlock recovery)", "demo-shard-0-0", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				mkPVC("data-demo-shard-0-0", tc.pvc0Fenced),
				mkPVC("data-demo-shard-0-1", tc.pvc1Fenced),
			).Build()
			r := &PostgresClusterReconciler{Client: c, Scheme: scheme}
			skip, err := r.shouldSkipFencedCandidate(ctx, namespace, tc.candidate)
			if err != nil {
				t.Fatalf("shouldSkipFencedCandidate: %v", err)
			}
			if skip != tc.wantSkip {
				t.Errorf("skip=%v, want %v", skip, tc.wantSkip)
			}
		})
	}
}
