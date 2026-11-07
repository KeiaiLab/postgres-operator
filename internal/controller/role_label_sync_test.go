/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// TestReconcilePrimaryRoleLabels_LabelsPrimaryAndReplica pins #226 F1: a
// freshly-observed shard (no role labels on either pod yet) converges both
// pods to the correct label in one call — on *both* primaryRoleLabelKeys.
// RwRoutingRoleLabelKey (postgres.keiailab.io/role) is the key the live
// postgres-prod rw Service selector was measured to actually use
// (team-lead kubectl measurement, 2026-07-11); InstanceRoleLabelKey
// (postgres.keiailab.io/instance-role) is the key this repo's own fencing
// decision logic/runbook already document. Neither key's Service builder
// lives in this repo (confirmed via full grep + `git log -S` across all
// history), so both are synced defensively.
func TestReconcilePrimaryRoleLabels_LabelsPrimaryAndReplica(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()

	primaryPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "demo-shard-0-1", Namespace: ns}}
	replicaPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "demo-shard-0-0", Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(primaryPod, replicaPod).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	authorities := []shardAuthority{{
		shard:   "shard-0",
		members: []string{"demo-shard-0-1", "demo-shard-0-0"},
		primary: "demo-shard-0-1",
		check:   primaryConfirmed,
	}}

	synced, err := r.reconcilePrimaryRoleLabels(ctx, &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
	}, authorities)
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
	for _, key := range []string{InstanceRoleLabelKey, RwRoutingRoleLabelKey} {
		if gotPrimary.Labels[key] != InstanceRoleLabelPrimary {
			t.Fatalf("primary pod label[%s] = %q, want %q", key, gotPrimary.Labels[key], InstanceRoleLabelPrimary)
		}
	}

	var gotReplica corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-0"}, &gotReplica); err != nil {
		t.Fatalf("get replica pod: %v", err)
	}
	for _, key := range []string{InstanceRoleLabelKey, RwRoutingRoleLabelKey} {
		if gotReplica.Labels[key] != InstanceRoleLabelReplica {
			t.Fatalf("replica pod label[%s] = %q, want %q", key, gotReplica.Labels[key], InstanceRoleLabelReplica)
		}
	}
}

// TestReconcilePrimaryRoleLabels_FlipsEvenWhenDemotedPrimaryIsStuckTerminating
// reproduces the INC 2026-07-11 core mechanism exactly as measured live
// (team-lead kubectl on postgres-prod): the old primary carries a stale
// postgres.keiailab.io/role=primary label — the key the rw Service selector
// actually consumes — and is wedged Terminating (dead node, finalizer never
// clears), while the newly-promoted primary carries no role labels yet. F1
// requires *both* primaryRoleLabelKeys to converge to the confirmed runtime
// primary, regardless of whether the stuck pod's deletion ever completes.
func TestReconcilePrimaryRoleLabels_FlipsEvenWhenDemotedPrimaryIsStuckTerminating(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()

	oldPrimary := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "demo-shard-0-0", Namespace: ns,
		Labels: map[string]string{
			InstanceRoleLabelKey:  InstanceRoleLabelPrimary,
			RwRoutingRoleLabelKey: InstanceRoleLabelPrimary,
		},
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
	authorities := []shardAuthority{{
		shard:   "shard-0",
		members: []string{"demo-shard-0-1", "demo-shard-0-0"},
		primary: "demo-shard-0-1",
		check:   primaryConfirmed,
	}}

	synced, err := r.reconcilePrimaryRoleLabels(ctx, &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
	}, authorities)
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
	for _, key := range []string{InstanceRoleLabelKey, RwRoutingRoleLabelKey} {
		if gotNewPrimary.Labels[key] != InstanceRoleLabelPrimary {
			t.Fatalf("new primary label[%s] = %q, want %q (must not be blocked by the stuck old primary)",
				key, gotNewPrimary.Labels[key], InstanceRoleLabelPrimary)
		}
	}

	var gotOldPrimary corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "demo-shard-0-0"}, &gotOldPrimary); err != nil {
		t.Fatalf("get old (stuck terminating) primary: %v", err)
	}
	for _, key := range []string{InstanceRoleLabelKey, RwRoutingRoleLabelKey} {
		if gotOldPrimary.Labels[key] != InstanceRoleLabelReplica {
			t.Fatalf("demoted pod label[%s] = %q, want %q even though it is stuck Terminating — this is exactly the label (postgres.keiailab.io/role) the live rw Service selector consumes",
				key, gotOldPrimary.Labels[key], InstanceRoleLabelReplica)
		}
	}
}

// TestReconcilePrimaryRoleLabels_SyncedFalseWhenPrimaryPatchFails pins #226 F3:
// when the confirmed primary's label patch itself fails, reconcilePrimaryRoleLabels
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
				if obj.GetName() == bootReplica {
					return injectedErr
				}
				return cli.Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	authorities := []shardAuthority{{
		shard:   "shard-0",
		members: []string{"demo-shard-0-1"},
		primary: "demo-shard-0-1",
		check:   primaryConfirmed,
	}}

	synced, err := r.reconcilePrimaryRoleLabels(ctx, &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
	}, authorities)
	if err == nil {
		t.Fatal("expected error to be surfaced from the failed primary label patch")
	}
	if synced {
		t.Fatal("synced = true, want false when the confirmed primary's label patch failed")
	}
}

// TestReconcilePrimaryRoleLabels_NoReadyPrimaryIsVacuouslySynced verifies that
// a shard with no confirmed runtime primary yet (e.g. still bootstrapping) does not count
// against synced — there is nothing to converge yet, so this must not be
// mistaken for the #226 gap.
func TestReconcilePrimaryRoleLabels_NoReadyPrimaryIsVacuouslySynced(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	authorities := []shardAuthority{{shard: "shard-0", check: primaryUnknown}}
	synced, err := r.reconcilePrimaryRoleLabels(ctx, &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
	}, authorities)
	if err != nil {
		t.Fatalf("reconcilePrimaryRoleLabels: %v", err)
	}
	if !synced {
		t.Fatal("synced = false, want true (no confirmed primary yet ⇒ vacuously nothing to sync)")
	}
}

// rwPrimary·rwReplica 는 rw 라벨 두 키(instance-role·role)의 기대값이다.
var (
	rwPrimary = [2]string{InstanceRoleLabelPrimary, InstanceRoleLabelPrimary}
	rwReplica = [2]string{InstanceRoleLabelReplica, InstanceRoleLabelReplica}
)

// rwLabels 는 파드의 rw 라벨 두 키 값이다.
func rwLabels(t *testing.T, c client.Client, pod string) [2]string {
	t.Helper()
	var got corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: pod}, &got); err != nil {
		t.Fatalf("get pod %q: %v", pod, err)
	}
	return [2]string{got.Labels[InstanceRoleLabelKey], got.Labels[RwRoutingRoleLabelKey]}
}

// primaryServiceHost 는 shard-0 -primary Service 의 ExternalName 이다(없으면 빈 문자열).
func primaryServiceHost(t *testing.T, c client.Client, cluster string) string {
	t.Helper()
	var svc corev1.Service
	key := client.ObjectKey{Namespace: runtimeTestNS, Name: ShardPrimaryServiceName(cluster, "shard-0")}
	err := c.Get(context.Background(), key, &svc)
	if apierrors.IsNotFound(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("get primary Service: %v", err)
	}
	return svc.Spec.ExternalName
}

func conditionOf(t *testing.T, c client.Client, cluster *postgresv1alpha1.PostgresCluster, condType string) *metav1.Condition {
	t.Helper()
	var got postgresv1alpha1.PostgresCluster
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), &got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	return meta.FindStatusCondition(got.Status.Conditions, condType)
}

// drainEvents 는 FakeRecorder 에 쌓인 이벤트를 모두 꺼낸다.
func drainEvents(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case ev := <-rec.Events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// TestPrimaryLabelsFollowRuntime 은 unfenced Role=Primary 두 파드가 reconcile 마다 순서를 바꿔
// 보여도 rw 라벨·-primary Service·런타임 CM 이 CM 파드에 머무는지 본다(§3.2). aggregate 첫
// 후보를 따르면 목록 순서대로 요동한다(DS-2).
func TestPrimaryLabelsFollowRuntime(t *testing.T) {
	t.Parallel()
	const name = "authority"
	cluster := withCanon(haCluster(name, postgresv1alpha1.ShardingModeNone),
		shardCanon{Epoch: 1, Initialized: true})
	first, second := shardMember(name, 0, statusapi.RolePrimary), shardMember(name, 1, statusapi.RolePrimary)
	cm := buildRuntimeConfigMap(cluster, "shard-0", shardRuntime{
		PrimaryEndpoint: podDNS(name, first.Name), PromotionEpoch: 1, ClusterInitialized: true,
	})

	// 파드 목록 순서를 reconcile 마다 뒤집는다 — aggregate 가 고르는 첫 primary 가 바뀐다.
	var reversed atomic.Bool
	log := &writeLog{}
	funcs := log.funcs()
	funcs.List = func(ctx context.Context, cli client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if err := cli.List(ctx, list, opts...); err != nil {
			return err
		}
		if pods, ok := list.(*corev1.PodList); ok && reversed.Load() {
			slices.Reverse(pods.Items)
		}
		return nil
	}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, cm, first, second).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(funcs).Build()
	exec := recoveryExecutor(log, map[string]string{first.Name: pgBoolFalse, second.Name: pgBoolFalse}, nil)
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: exec}
	defer DeleteMetricsFor(runtimeTestNS, name)

	wantHost := primaryEndpointHost(podDNS(name, first.Name))
	for i := range 4 {
		reversed.Store(i%2 == 1)
		reconcileCluster(t, r, cluster)

		if got := rwLabels(t, c, first.Name); got != rwPrimary {
			t.Fatalf("reconcile %d: runtime primary %s rw labels = %v, want %v", i, first.Name, got, rwPrimary)
		}
		if got := rwLabels(t, c, second.Name); got != rwReplica {
			t.Fatalf("reconcile %d: other primary %s rw labels = %v, want %v", i, second.Name, got, rwReplica)
		}
		if got := primaryServiceHost(t, c, name); got != wantHost {
			t.Fatalf("reconcile %d: -primary Service = %q, want runtime primary %q", i, got, wantHost)
		}
	}
	if got := runtimeCM(t, c, name); !equality.Semantic.DeepEqual(got, cm.Data) {
		t.Fatalf("runtime CM changed: got %v, want %v", got, cm.Data)
	}
	if n := log.count("patch cm/"+cm.Name) + log.count("update cm/"+cm.Name); n != 0 {
		t.Fatalf("runtime CM writes = %d, want 0: %q", n, log.lines)
	}
}

// TestPrimaryLabelsFailClosed 는 CM 파드가 실제 primary 가 아니면(in recovery) rw 라벨이 0 이고
// Warning PrimaryEndpointDisagrees · PrimaryEndpointConsistent=False 인지 본다 — aggregate 가 다른
// 파드를 primary 로 보여도 따르지 않는다(쓰기 fail-closed, §3.2).
func TestPrimaryLabelsFailClosed(t *testing.T) {
	t.Parallel()
	const name = "failclosed"
	cluster := withCanon(haCluster(name, postgresv1alpha1.ShardingModeNone),
		shardCanon{Epoch: 1, Initialized: true})
	demoted := shardMember(name, 0, statusapi.RoleReplica)
	demoted.Labels[InstanceRoleLabelKey] = InstanceRoleLabelPrimary
	demoted.Labels[RwRoutingRoleLabelKey] = InstanceRoleLabelPrimary
	other := shardMember(name, 1, statusapi.RolePrimary)
	cm := buildRuntimeConfigMap(cluster, "shard-0", shardRuntime{
		PrimaryEndpoint: podDNS(name, demoted.Name), PromotionEpoch: 1, ClusterInitialized: true,
	})
	log := &writeLog{}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, cm, demoted, other).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(log.funcs()).Build()
	recorder := events.NewFakeRecorder(eventBuffer)
	exec := recoveryExecutor(log, map[string]string{demoted.Name: pgBoolTrue, other.Name: pgBoolFalse}, nil)
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: exec, Recorder: recorder}
	defer DeleteMetricsFor(runtimeTestNS, name)

	reconcileCluster(t, r, cluster)

	for _, pod := range []string{demoted.Name, other.Name} {
		if got := rwLabels(t, c, pod); got != rwReplica {
			t.Fatalf("%s rw labels = %v, want %v (no rw label while the runtime primary is in recovery)", pod, got, rwReplica)
		}
	}
	if got := primaryServiceHost(t, c, name); got != "" {
		t.Fatalf("-primary Service = %q, want none while the runtime primary is in recovery", got)
	}
	cond := conditionOf(t, c, cluster, ConditionPrimaryEndpointConsistent)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonPrimaryEndpointDisagrees {
		t.Fatalf("%s = %+v, want False/%s", ConditionPrimaryEndpointConsistent, cond, ReasonPrimaryEndpointDisagrees)
	}
	emitted := drainEvents(recorder)
	if !slices.ContainsFunc(emitted, func(ev string) bool {
		return strings.HasPrefix(ev, "Warning "+ReasonPrimaryEndpointDisagrees+" ")
	}) {
		t.Fatalf("events = %q, want a Warning %s", emitted, ReasonPrimaryEndpointDisagrees)
	}
	if got := runtimeCM(t, c, name); !equality.Semantic.DeepEqual(got, cm.Data) {
		t.Fatalf("runtime CM changed on disagreement: got %v, want %v", got, cm.Data)
	}
}

// TestShardAuthorityDisagreement 는 rw 권위가 흔들리는 이유를 본다 — 첫 primary 전(부트스트랩)의
// 비확인은 이견이 아니고, 두 번째 primary 는 aggregate 가 어느 쪽을 골랐든 같은 문구다.
func TestShardAuthorityDisagreement(t *testing.T) {
	t.Parallel()
	const cmPod, rival = "demo-shard-0-0", "demo-shard-0-1"
	agreed := shardAuthority{endpoint: "e", primary: cmPod, check: primaryConfirmed, initialized: true, observed: cmPod}
	cases := map[string]struct {
		edit func(*shardAuthority)
		want string
	}{
		"agrees":           {edit: func(*shardAuthority) {}, want: ""},
		"bootstrapping":    {edit: func(a *shardAuthority) { a.check, a.initialized = primaryAbsent, false }, want: ""},
		"in recovery":      {edit: func(a *shardAuthority) { a.check = primaryInRecovery }, want: "runtime primary " + cmPod + " in recovery"},
		"no runtime CM":    {edit: func(a *shardAuthority) { a.endpoint, a.primary, a.check = "", "", primaryUnknown }, want: "runtime primary unknown"},
		"observed other":   {edit: func(a *shardAuthority) { a.observed = rival }, want: "observed primary " + rival},
		"rival":            {edit: func(a *shardAuthority) { a.rivals = []string{rival} }, want: rival + " also primary"},
		"rival observed":   {edit: func(a *shardAuthority) { a.rivals, a.observed = []string{rival}, rival }, want: rival + " also primary"},
		"rival bootstraps": {edit: func(a *shardAuthority) { a.initialized, a.rivals = false, []string{rival} }, want: rival + " also primary"},
	}
	for name, tc := range cases {
		a := agreed
		tc.edit(&a)
		if got := a.disagreement(); got != tc.want {
			t.Errorf("%s: disagreement = %q, want %q", name, got, tc.want)
		}
	}
}
