/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// 본 파일은 재시드 경로가 전부 재구축 엔진을 지나는지 본다(설계 2026-09-25 §3.2·§3.5·§3.10) — PVC 삭제
// 0 · failover 는 발행과 옛 primary hold 가 CM 쓰기 한 번 · hold 만 남은 멤버는 Demoted 로 복원 · aggregate
// 사유(FencedPrimary · Rogue)와 그래프 끊김(Stale)이 요청이 된다 · primary 둘은 hold 0.

const (
	// chainMembers 는 연쇄 시험 샤드의 멤버 수다(0 = CM primary).
	chainMembers = 4
	// pvcDeletePrefix·podDeletePrefix 는 writeLog 의 멤버 PVC·파드 삭제 줄이다.
	pvcDeletePrefix = "delete *v1.PersistentVolumeClaim/"
	podDeletePrefix = "delete *v1.Pod/"
)

// memberDeletes 는 멤버 파드·PVC 삭제 수다(라우터 HPA 정리 같은 다른 삭제는 세지 않는다).
func memberDeletes(log *writeLog) int {
	return log.count(podDeletePrefix) + log.count(pvcDeletePrefix)
}

// logDeletes 는 funcs 에 Delete 기록을 더한다.
func logDeletes(log *writeLog, funcs interceptor.Funcs) interceptor.Funcs {
	funcs.Delete = func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		log.add("delete " + objKind(obj) + "/" + obj.GetName())
		return cli.Delete(ctx, obj, opts...)
	}
	return funcs
}

// reportRole 은 인스턴스 매니저 흉내다 — 신선한 상태 애노테이션으로 role 을 보고한다(마커 없음).
func reportRole(t *testing.T, c client.Client, cluster, pod string, role statusapi.Role) {
	t.Helper()
	var got corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: pod}, &got); err != nil {
		t.Fatalf("get pod %q: %v", pod, err)
	}
	setMemberRole(&got, ShardServiceName(cluster, 0), role, time.Now())
	if err := c.Update(context.Background(), &got); err != nil {
		t.Fatalf("update pod %q: %v", pod, err)
	}
}

// reportMarker 는 인스턴스 매니저가 승격 마커를 보고한 상태다(Promoted).
func reportMarker(t *testing.T, c client.Client, pod string) {
	t.Helper()
	var got corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: pod}, &got); err != nil {
		t.Fatalf("get pod %q: %v", pod, err)
	}
	withMarker(t, &got)
	if err := c.Update(context.Background(), &got); err != nil {
		t.Fatalf("update pod %q: %v", pod, err)
	}
}

// withMarker 는 파드 상태 애노테이션에 승격 마커(Promoted)를 싣는다.
func withMarker(t *testing.T, pod *corev1.Pod) *corev1.Pod {
	t.Helper()
	st, ok := parsePodStatus(pod)
	if !ok {
		t.Fatalf("pod %q has no instance status", pod.Name)
	}
	st.Promoted = true
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	pod.Annotations[statusapi.AnnotationKey] = string(raw)
	return pod
}

// withLedger 는 재구축 원장을 CR 에 심는다.
func withLedger(t *testing.T, cluster *postgresv1alpha1.PostgresCluster, entries ...rebuildEntry) {
	t.Helper()
	raw, err := json.Marshal(rebuildState{Version: rebuildStateVersion, Active: map[string]memberRebuild{}, Ledger: entries})
	if err != nil {
		t.Fatalf("marshal ledger: %v", err)
	}
	if cluster.Annotations == nil {
		cluster.Annotations = map[string]string{}
	}
	cluster.Annotations[memberRebuildAnnotation] = string(raw)
}

// storedRebuilds 는 CR 에 저장된 재구축 상태다.
func storedRebuilds(t *testing.T, c client.Client, cluster *postgresv1alpha1.PostgresCluster) rebuildState {
	t.Helper()
	var got postgresv1alpha1.PostgresCluster
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), &got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	state, err := loadRebuildState(&got)
	if err != nil {
		t.Fatalf("load rebuild state: %v", err)
	}
	return state
}

// assertRebuilds 는 진행 중 항목이 want(파드 → 사유)와 같고 전부 hold 인지 본다.
func assertRebuilds(t *testing.T, c client.Client, cluster *postgresv1alpha1.PostgresCluster, want map[string]rebuildReason) {
	t.Helper()
	state := storedRebuilds(t, c, cluster)
	got := map[string]rebuildReason{}
	for pod, e := range state.Active {
		got[pod] = e.Reason
	}
	if !maps.Equal(got, want) {
		t.Fatalf("active rebuilds = %v, want %v", got, want)
	}
	hold := runtimeFromData(runtimeCM(t, c, cluster.Name)).Hold
	for pod := range want {
		if !slices.Contains(hold, pod) {
			t.Fatalf("MEMBER_HOLD = %v, want %s held", hold, pod)
		}
	}
}

// cmHistory 는 런타임 CM 쓰기마다 쓴 뒤의 데이터다 — "CM 쓰기 한 번" 단언용.
type cmHistory struct {
	mu     sync.Mutex
	writes []map[string]string
}

func (h *cmHistory) record(obj client.Object) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writes = append(h.writes, maps.Clone(cm.Data))
}

func (h *cmHistory) all() []map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.writes)
}

// watch 는 funcs 의 CM 쓰기(Create·Patch)가 성공하면 그 뒤 데이터를 남기게 감싼다.
func (h *cmHistory) watch(funcs interceptor.Funcs) interceptor.Funcs {
	create, patch := funcs.Create, funcs.Patch
	funcs.Create = func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if err := create(ctx, cli, obj, opts...); err != nil {
			return err
		}
		h.record(obj)
		return nil
	}
	funcs.Patch = func(ctx context.Context, cli client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
		if err := patch(ctx, cli, obj, p, opts...); err != nil {
			return err
		}
		h.record(obj)
		return nil
	}
	return funcs
}

// chainWorld 는 네 멤버 샤드(0 = CM primary, 1..3 standby) 위의 전체 Reconcile 이다. 파드는 한 시간 전에
// 떴고 PVC 는 모두 있다. 쓰기·exec 는 한 줄씩 기록된다.
type chainWorld struct {
	name     string
	cluster  *postgresv1alpha1.PostgresCluster
	c        client.Client
	r        *PostgresClusterReconciler
	exec     *graphExecutor
	log      *writeLog
	recorder *events.FakeRecorder
}

func newChainWorld(t *testing.T, name string) *chainWorld {
	t.Helper()
	cluster, cm := runtimeCluster(name)
	cluster.Spec.Shards.Replicas = chainMembers - 1
	cluster.Status.Phase = postgresv1alpha1.ClusterPhaseReady
	log := &writeLog{}
	exec := newGraphExecutor(log)
	// 멤버마다 파드·PVC.
	objs := slices.Grow([]client.Object{cluster, cm}, 2*chainMembers)
	var rows strings.Builder
	for i := range chainMembers {
		pod := graphMember(name, i)
		role, recovery := statusapi.RoleReplica, pgBoolTrue
		if i == 0 {
			role, recovery = statusapi.RolePrimary, pgBoolFalse
		} else {
			rows.WriteString(walRow(pod, walsenderStreaming))
		}
		exec.recovery[pod] = recovery
		p := shardMember(name, i, role)
		p.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
		objs = append(objs, p, memberPVC(pod, false))
	}
	exec.rows[graphMember(name, 0)] = rows.String()

	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(logDeletes(log, log.funcs())).Build()
	recorder := events.NewFakeRecorder(wideEventBuffer)
	r := &PostgresClusterReconciler{
		Client: c, Scheme: scheme, PromotionPodExecutor: exec, Recorder: recorder, MemberSnapshotClass: rebuildTestClass,
	}
	t.Cleanup(func() { DeleteMetricsFor(runtimeTestNS, name) })
	return &chainWorld{name: name, cluster: cluster, c: c, r: r, exec: exec, log: log, recorder: recorder}
}

// reconcile 은 한 번의 전체 Reconcile 이다 — 이벤트 버퍼를 비워 둔다(차면 Eventf 가 막힌다).
func (w *chainWorld) reconcile(t *testing.T) {
	t.Helper()
	reconcileCluster(t, w.r, w.cluster)
	drainEvents(w.recorder)
}

// restore 는 StatefulSet 흉내다 — 지워진 멤버 파드·PVC 를 다시 만든다(옛 재시드는 둘 다 지웠다).
func (w *chainWorld) restore(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for i := range chainMembers {
		pod := graphMember(w.name, i)
		var pvc corev1.PersistentVolumeClaim
		if err := w.c.Get(ctx, client.ObjectKey{Namespace: runtimeTestNS, Name: fencing.PVCName(pod)}, &pvc); err != nil {
			if err := w.c.Create(ctx, memberPVC(pod, false)); err != nil {
				t.Fatalf("recreate pvc of %s: %v", pod, err)
			}
		}
		var p corev1.Pod
		if err := w.c.Get(ctx, client.ObjectKey{Namespace: runtimeTestNS, Name: pod}, &p); err != nil {
			fresh := shardMember(w.name, i, statusapi.RoleReplica)
			fresh.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
			if err := w.c.Create(ctx, fresh); err != nil {
				t.Fatalf("recreate %s: %v", pod, err)
			}
		}
	}
}

// absentSince 는 복제 그래프가 pod 의 끊김을 처음 본 시각을 심는다.
func (w *chainWorld) absentSince(pod string, since time.Time) {
	w.r.graphMu.Lock()
	defer w.r.graphMu.Unlock()
	key := runtimeTestNS + "/" + w.name
	if w.r.memberAbsentSince == nil {
		w.r.memberAbsentSince = map[string]map[string]time.Time{}
	}
	if w.r.memberAbsentSince[key] == nil {
		w.r.memberAbsentSince[key] = map[string]time.Time{}
	}
	w.r.memberAbsentSince[key][pod] = since
}

// TestNoPVCDeleteAcrossLoop 는 2026-09-24 사슬의 재시드 발화 셋 — failover 뒤 옛 primary · 옛 primary
// 복귀 · 8분 끊긴 standby · rogue — 을 전체 Reconcile 로 차례로 돌려도 PVC 삭제가 0 인지 본다. 각 발화는
// 재구축 요청(hold + 항목)이 된다: 옛 primary = Demoted · 끊긴 standby = Stale · rogue = Rogue.
func TestNoPVCDeleteAcrossLoop(t *testing.T) {
	t.Parallel()
	w := newChainWorld(t, "chain")
	m0, m1, m2, m3 := graphMember(w.name, 0), graphMember(w.name, 1), graphMember(w.name, 2), graphMember(w.name, 3)

	// failover — 0 이 죽고(PodReady False) 디바운스가 지나 1 이 승격된다.
	setPodReady(t, w.c, m0, corev1.ConditionFalse)
	w.r.failoverPending = map[string]time.Time{
		runtimeTestNS + "/" + w.name: time.Now().Add(-failoverDebounceThreshold - time.Second),
	}
	w.reconcile(t)
	if n := w.log.count("exec promote " + m1); n != 1 {
		t.Fatalf("promotions of %s = %d, want 1: %q", m1, n, w.log.lines)
	}
	// 승격된 1 은 마커를 가진 primary 로 보고하고 2·3 이 그를 따른다.
	w.exec.setRecovery(m1, pgBoolFalse)
	w.exec.setRows(m1, walRow(m2, walsenderStreaming)+walRow(m3, walsenderStreaming))
	reportRole(t, w.c, w.name, m1, statusapi.RolePrimary)
	reportMarker(t, w.c, m1)
	w.restore(t)

	// 옛 primary 복귀 — 낡은 env 로 primary 를 보고하지만 PVC 는 fence 돼 있다.
	setPodReady(t, w.c, m0, corev1.ConditionTrue)
	reportRole(t, w.c, w.name, m0, statusapi.RolePrimary)
	w.reconcile(t)
	w.restore(t)

	// standby 8분 끊김 — 2 가 새 primary 의 pg_stat_replication 에서 빠지고 Ready 도 아니다.
	w.exec.setRows(m1, walRow(m3, walsenderStreaming))
	w.absentSince(m2, time.Now().Add(-staleStandbyReseedTimeout-time.Minute))
	setPodReady(t, w.c, m2, corev1.ConditionFalse)
	w.reconcile(t)
	w.restore(t)

	// rogue — 마커를 가진 새 primary 가 있는데 3 이 마커 없이 primary 를 보고한다.
	reportRole(t, w.c, w.name, m3, statusapi.RolePrimary)
	w.exec.setRows(m1, "")
	w.reconcile(t)

	if n := w.log.count(pvcDeletePrefix); n != 0 {
		t.Fatalf("PVC deletes = %d, want 0: %q", n, w.log.lines)
	}
	assertRebuilds(t, w.c, w.cluster, map[string]rebuildReason{m0: rebuildDemoted, m2: rebuildStale, m3: rebuildRogue})
}

// TestPublishHoldsOldPrimary 는 failover 발행이 PRIMARY_ENDPOINT 와 옛 primary hold 를 CM 쓰기 한 번에
// 싣고(I9) 그 뒤 Demoted 항목을 남기는지 · switchover 는 hold 0 · 그 항목 쓰기가 실패해도 다음 reconcile 이
// hold 만 남은 멤버를 Demoted 로 복원하는지 · 채택 쓰기도 옛 CM primary 를 hold 하는지(그 파드가 unfenced
// primary 면 빼고) 본다.
func TestPublishHoldsOldPrimary(t *testing.T) {
	t.Parallel()
	t.Run("one write", func(t *testing.T) {
		t.Parallel()
		p := newPublishRun(t, "hold-once")
		p.promote(t)

		writes := p.history.all()
		if len(writes) != 1 {
			t.Fatalf("runtime CM writes = %d, want 1: %v", len(writes), writes)
		}
		first := writes[0]
		if first[runtimeKeyPrimaryEndpoint] != podDNS(p.name, p.target) || !slices.Contains(parseHold(first[runtimeKeyMemberHold]), p.old) {
			t.Fatalf("first CM write = endpoint %q hold %q, want %s and %s held", first[runtimeKeyPrimaryEndpoint],
				first[runtimeKeyMemberHold], podDNS(p.name, p.target), p.old)
		}
		e, ok := storedRebuilds(t, p.c, p.cluster).Active[p.old]
		if !ok || e.Reason != rebuildDemoted || e.Phase != rebuildHolding {
			t.Fatalf("rebuild of %s = %+v (present %v), want Demoted Holding", p.old, e, ok)
		}
		if n := memberDeletes(p.log); n != 0 {
			t.Fatalf("member deletes during the promotion = %d, want 0: %q", n, p.log.lines)
		}
	})

	t.Run("switchover holds nothing", func(t *testing.T) {
		t.Parallel()
		p := newPublishRun(t, "hold-switchover")
		target := failover.PromotionTarget{ShardName: "shard-0", Pod: p.target, Endpoint: podDNS(p.name, p.target)}
		if err := p.r.promoteAndPublish(context.Background(), p.cluster, "shard-0", target, p.old, promotionSwitchover); err != nil {
			t.Fatalf("promoteAndPublish: %v", err)
		}

		if writes := p.history.all(); len(writes) != 1 || writes[0][runtimeKeyMemberHold] != "" {
			t.Fatalf("runtime CM writes = %v, want one without a hold (the old primary is a healthy standby)", writes)
		}
		if active := storedRebuilds(t, p.c, p.cluster).Active; len(active) != 0 {
			t.Fatalf("rebuilds after a switchover = %v, want none", active)
		}
	})

	t.Run("lost request restored", func(t *testing.T) {
		t.Parallel()
		p := newPublishRun(t, "hold-restore")
		p.dropRebuilds.Store(true)
		p.promote(t)
		if _, ok := storedRebuilds(t, p.c, p.cluster).Active[p.old]; ok {
			t.Fatal("setup: the rebuild entry write should have been dropped")
		}
		if !slices.Contains(runtimeFromData(runtimeCM(t, p.c, p.name)).Hold, p.old) {
			t.Fatalf("setup: %s must stay held without its entry", p.old)
		}

		p.dropRebuilds.Store(false)
		reconcileCluster(t, p.r, p.cluster)
		if e, ok := storedRebuilds(t, p.c, p.cluster).Active[p.old]; !ok || e.Reason != rebuildDemoted {
			t.Fatalf("held %s without an entry not restored as Demoted: %+v (present %v)", p.old, e, ok)
		}
	})

	cases := []struct {
		name     string
		oldAlive bool // 옛 CM primary 가 아직 unfenced primary 를 보고한다 — 그러면 hold 하지 않는다
	}{
		{"adoption holds the old primary", false},
		{"adoption spares a live primary", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name := "adopt-hold-" + strconv.FormatBool(tc.oldAlive)
			old, adopted := graphMember(name, 0), graphMember(name, 1)
			want := []string{old}
			cluster := withCanon(haCluster(name, postgresv1alpha1.ShardingModeNone), "shard-0", shardCanon{Epoch: 2, Initialized: true})
			cm := buildRuntimeConfigMap(cluster, "shard-0", shardRuntime{
				PrimaryEndpoint: podDNS(name, old), PromotionEpoch: 1, ClusterInitialized: true,
			})
			objs := []client.Object{cluster, cm, shardMember(name, 1, statusapi.RolePrimary), memberPVC(old, false), memberPVC(adopted, false)}
			if tc.oldAlive {
				objs = append(objs, shardMember(name, 0, statusapi.RolePrimary))
				want = nil
			}
			log, history := &writeLog{}, &cmHistory{}
			scheme := runtimeScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
				WithInterceptorFuncs(history.watch(log.funcs())).Build()
			exec := recoveryExecutor(log, map[string]string{old: pgBoolFalse, adopted: pgBoolFalse},
				map[string]string{old: "1\n", adopted: "2\n"})
			r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: exec}
			defer DeleteMetricsFor(runtimeTestNS, name)

			if err := r.adoptPrimary(context.Background(), cluster, ordinalShardRef(cluster, 0)); err != nil {
				t.Fatalf("adoptPrimary: %v", err)
			}

			writes := history.all()
			if len(writes) != 1 || writes[0][runtimeKeyPrimaryEndpoint] != podDNS(name, adopted) {
				t.Fatalf("runtime CM writes = %v, want one publishing %s", writes, adopted)
			}
			if got := parseHold(writes[0][runtimeKeyMemberHold]); !slices.Equal(got, want) {
				t.Fatalf("MEMBER_HOLD in the adoption write = %v, want %v", got, want)
			}
		})
	}
}

// publishRun 은 CM primary 0 이 죽고(파드 없음) 1 이 승격되는 샤드다 — CM 쓰기 기록과 재구축 항목 쓰기
// 실패 주입을 단다.
type publishRun struct {
	name, old, target string
	cluster           *postgresv1alpha1.PostgresCluster
	c                 client.Client
	r                 *PostgresClusterReconciler
	log               *writeLog
	history           *cmHistory
	dropRebuilds      atomic.Bool
}

func newPublishRun(t *testing.T, name string) *publishRun {
	t.Helper()
	p := &publishRun{name: name, old: graphMember(name, 0), target: graphMember(name, 1), log: &writeLog{}, history: &cmHistory{}}
	cluster, cm := runtimeCluster(name)
	p.cluster = cluster
	funcs := p.history.watch(logDeletes(p.log, p.log.funcs()))
	patch := funcs.Patch
	funcs.Patch = func(ctx context.Context, cli client.WithWatch, obj client.Object, pt client.Patch, opts ...client.PatchOption) error {
		if _, ok := obj.(*postgresv1alpha1.PostgresCluster); ok && p.dropRebuilds.Load() {
			if raw, err := pt.Data(obj); err == nil && strings.Contains(string(raw), memberRebuildAnnotation) {
				return errors.New("injected: rebuild entry write lost")
			}
		}
		return patch(ctx, cli, obj, pt, opts...)
	}
	scheme := runtimeScheme(t)
	p.c = fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, cm, shardMember(name, 1, statusapi.RoleReplica), memberPVC(p.old, false), memberPVC(p.target, false)).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(funcs).Build()
	p.r = &PostgresClusterReconciler{Client: p.c, Scheme: scheme, PromotionPodExecutor: promotingExecutor(p.log)}
	t.Cleanup(func() { DeleteMetricsFor(runtimeTestNS, name) })
	return p
}

// promote 는 0 의 failover 승격이다(후보 1).
func (p *publishRun) promote(t *testing.T) {
	t.Helper()
	err := p.r.executeClusterPromotion(context.Background(), p.cluster, "shard-0", failover.Decision{
		Failed: true,
		Reason: failover.ReasonNoPrimary,
		PromotionCandidate: &postgresv1alpha1.ShardEndpoint{
			Pod: p.target, Endpoint: podDNS(p.name, p.target), Ready: true,
		},
	})
	if err != nil {
		t.Fatalf("executeClusterPromotion: %v", err)
	}
}

// TestFencedAndRogueHeld 는 aggregate 의 두 사유가 재구축 요청이 되는지 본다 — fence 된 PVC 로 primary 를
// 보고하는 멤버 = FencedPrimary · 마커 없이 primary 를 보고하는 멤버(마커 있는 primary 가 있을 때) = Rogue.
// 예산이 다 찼어도 hold 는 즉시다(예산은 해제만 막는다).
func TestFencedAndRogueHeld(t *testing.T) {
	t.Parallel()
	const name = "fenced-rogue"
	fencedPrimary, rogue := graphMember(name, 2), graphMember(name, 3)
	cluster, cm := runtimeCluster(name)
	cluster.Spec.Shards.Replicas = chainMembers - 1
	released := time.Now().Add(-time.Minute)
	ledger := make([]rebuildEntry, 0, rebuildBudget(cluster))
	for i := range rebuildBudget(cluster) {
		ledger = append(ledger, rebuildEntry{
			Pod: "gone-" + strconv.Itoa(i), Shard: rebuildTestShard, Token: "t-" + strconv.Itoa(i),
			Reason: rebuildStale, Outcome: outcomeReleased, Admission: admitBudget, Released: released,
		})
	}
	withLedger(t, cluster, ledger...)

	log := &writeLog{}
	exec := newGraphExecutor(log)
	roles := []statusapi.Role{statusapi.RolePrimary, statusapi.RoleReplica, statusapi.RolePrimary, statusapi.RolePrimary}
	// 멤버마다 파드·PVC.
	objs := slices.Grow([]client.Object{cluster, cm}, 2*len(roles))
	for i, role := range roles {
		pod := shardMember(name, i, role)
		if i == 0 {
			withMarker(t, pod)
		}
		exec.recovery[pod.Name] = pgBoolFalse
		objs = append(objs, pod, memberPVC(pod.Name, pod.Name == fencedPrimary))
	}
	exec.recovery[graphMember(name, 1)] = pgBoolTrue
	exec.rows[graphMember(name, 0)] = walRow(graphMember(name, 1), walsenderStreaming)
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(logDeletes(log, log.funcs())).Build()
	r := &PostgresClusterReconciler{
		Client: c, Scheme: scheme, PromotionPodExecutor: exec, Recorder: events.NewFakeRecorder(wideEventBuffer),
		MemberSnapshotClass: rebuildTestClass,
	}
	defer DeleteMetricsFor(runtimeTestNS, name)

	reconcileCluster(t, r, cluster)

	if got := testutil.ToFloat64(MetricMemberRebuildBudget.WithLabelValues(runtimeTestNS, name)); got != 0 {
		t.Fatalf("setup: budget remaining = %v, want exhausted", got)
	}
	assertRebuilds(t, c, cluster, map[string]rebuildReason{fencedPrimary: rebuildFencedPrimary, rogue: rebuildRogue})
	if n := log.count(pvcDeletePrefix); n != 0 {
		t.Fatalf("PVC deletes = %d, want 0: %q", n, log.lines)
	}
}

// TestStaleFromGraphAbsence 는 그래프가 staleStandbyReseedTimeout 이상 끊김으로 아는 standby(파드 나이도
// 그 이상)만 Stale 로 요청하는지 본다 — 끊긴 지 10 s 면 파드가 20분 됐어도 0 · 막 뜬 파드는 0 · 그래프를
// 모르면 0 · 방금 재구축이 끝났으면(reseedCooldown 안) 0 · hold 만 남고 항목을 잃었으면 Demoted 가 아니라 Stale.
func TestStaleFromGraphAbsence(t *testing.T) {
	t.Parallel()
	const agedPod = 20 * time.Minute
	at := rebuildT0
	cases := []struct {
		name    string
		absent  time.Duration
		age     time.Duration // 파드 나이
		known   bool
		ended   time.Duration // 마지막 재구축 끝 — 0 이면 원장 없음
		held    bool          // 이미 MEMBER_HOLD 에 있고 항목이 없다(항목 쓰기 실패)
		wantReq bool
	}{
		{"absent for the timeout", staleStandbyReseedTimeout, agedPod, true, 0, false, true},
		{"absent for 10s", 10 * time.Second, agedPod, true, 0, false, false},
		{"young pod", staleStandbyReseedTimeout + time.Minute, staleStandbyReseedTimeout - time.Minute, true, 0, false, false},
		{"graph unknown", staleStandbyReseedTimeout + time.Minute, agedPod, false, 0, false, false},
		{"member cooldown", staleStandbyReseedTimeout + time.Minute, agedPod, true, time.Minute, false, false},
		{"held without an entry", staleStandbyReseedTimeout + time.Minute, agedPod, true, 0, true, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRebuildWorld(t, "stale-graph-"+strconv.Itoa(i))
			target := graphMember(w.name, 1)
			var pod corev1.Pod
			if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: target}, &pod); err != nil {
				t.Fatalf("get pod: %v", err)
			}
			pod.CreationTimestamp = metav1.NewTime(at.Add(-tc.age))
			if err := w.c.Update(context.Background(), &pod); err != nil {
				t.Fatalf("age pod: %v", err)
			}
			if tc.ended > 0 {
				w.seedLedger(t, rebuildEntry{Pod: target, Shard: rebuildTestShard, Token: "t-0", Reason: rebuildStale,
					Outcome: outcomeReleased, Admission: admitBudget, Released: at.Add(-time.Hour / 2), Ended: at.Add(-tc.ended)})
			}
			if tc.held {
				patchData(t, w.c, buildRuntimeConfigMap(w.cluster(t), rebuildTestShard, shardRuntime{
					PrimaryEndpoint: podDNS(w.name, graphMember(w.name, 0)), PromotionEpoch: 1, ClusterInitialized: true,
					Hold: []string{target},
				}))
			}
			key := runtimeTestNS + "/" + w.name
			w.r.memberAbsentSince = map[string]map[string]time.Time{key: {target: at.Add(-tc.absent)}}
			w.graph.known = tc.known
			shards := []postgresv1alpha1.ShardStatus{{
				Name: rebuildTestShard, Ordinal: 0,
				Replicas: []postgresv1alpha1.ShardEndpoint{{Pod: target}, {Pod: graphMember(w.name, 2)}},
			}}

			w.r.requestRebuilds(context.Background(), w.cluster(t), shards, map[string]replicationGraph{rebuildTestShard: w.graph}, at)

			e, active := w.entry(t, target)
			if tc.wantReq != active || tc.wantReq != w.held(t, target) {
				t.Fatalf("entry %+v (active %v) held %v, want requested=%v", e, active, w.held(t, target), tc.wantReq)
			}
			if active && e.Reason != rebuildStale {
				t.Fatalf("reason = %s, want %s", e.Reason, rebuildStale)
			}
			if _, other := w.entry(t, graphMember(w.name, 2)); other {
				t.Fatal("member 2 has no absence record but was requested")
			}
		})
	}
}

// TestRestoreSkipsJustEnded 는 hold 만 남은 멤버의 Demoted 복원이 runtimePropagationWait 안에 끝난 재구축의
// hold 는 건너뛰는지 본다 — 끝내는 쓰기는 CM(hold 해제)이 먼저라 캐시는 CR 만 새로 볼 수 있다.
func TestRestoreSkipsJustEnded(t *testing.T) {
	t.Parallel()
	at := rebuildT0
	cases := []struct {
		name  string
		ended time.Duration
		want  bool
	}{
		{"ended within the propagation wait", runtimePropagationWait - time.Second, false},
		{"ended a propagation wait ago", runtimePropagationWait, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRebuildWorld(t, "restore-ended-"+strconv.Itoa(i))
			held := graphMember(w.name, 1)
			patchData(t, w.c, buildRuntimeConfigMap(w.cluster(t), rebuildTestShard, shardRuntime{
				PrimaryEndpoint: podDNS(w.name, graphMember(w.name, 0)), PromotionEpoch: 1, ClusterInitialized: true,
				Hold: []string{held},
			}))
			w.seedLedger(t, rebuildEntry{Pod: held, Shard: rebuildTestShard, Token: "t-0", Reason: rebuildDemoted,
				Outcome: outcomeReleased, Admission: admitBudget, Released: at.Add(-time.Hour / 2), Ended: at.Add(-tc.ended)})
			shards := []postgresv1alpha1.ShardStatus{{Name: rebuildTestShard, Ordinal: 0}}

			w.r.requestRebuilds(context.Background(), w.cluster(t), shards, map[string]replicationGraph{rebuildTestShard: w.graph}, at)

			e, active := w.entry(t, held)
			if active != tc.want || (active && e.Reason != rebuildDemoted) {
				t.Fatalf("held %s, rebuild ended %s ago: entry %+v (active %v), want restored=%v", held, tc.ended, e, active, tc.want)
			}
		})
	}
}

// TestTwoPrimariesHoldNothing 는 unfenced primary 가 둘이면 어느 쪽도 hold 하지 않고 Warning 만 내는지
// 본다 — 둘 다 마커가 있든, CM primary 쪽이 마커 없이 rogue 로 보이든 · 끊긴 지 오래된 쪽도 Stale 이
// 아니다(primary 를 보고한다).
func TestTwoPrimariesHoldNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		runtimeMarker bool // CM primary(0)가 마커를 보고한다 — 상대(1)는 늘 보고한다
	}{
		{"both marked", true},
		{"runtime primary unmarked", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name := "two-primaries-" + strconv.FormatBool(tc.runtimeMarker)
			runtimePrimary, rival := graphMember(name, 0), graphMember(name, 1)
			cluster, cm := runtimeCluster(name)
			first, second := shardMember(name, 0, statusapi.RolePrimary), withMarker(t, shardMember(name, 1, statusapi.RolePrimary))
			if tc.runtimeMarker {
				withMarker(t, first)
			}
			log := &writeLog{}
			exec := newGraphExecutor(log)
			exec.recovery[runtimePrimary], exec.recovery[rival] = pgBoolFalse, pgBoolFalse
			scheme := runtimeScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(cluster, cm, first, second, memberPVC(runtimePrimary, false), memberPVC(rival, false)).
				WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
				WithInterceptorFuncs(logDeletes(log, log.funcs())).Build()
			recorder := events.NewFakeRecorder(wideEventBuffer)
			r := &PostgresClusterReconciler{
				Client: c, Scheme: scheme, PromotionPodExecutor: exec, Recorder: recorder, MemberSnapshotClass: rebuildTestClass,
			}
			r.memberAbsentSince = map[string]map[string]time.Time{
				runtimeTestNS + "/" + name: {rival: time.Now().Add(-staleStandbyReseedTimeout - time.Minute)},
			}
			defer DeleteMetricsFor(runtimeTestNS, name)

			reconcileCluster(t, r, cluster)

			if active := storedRebuilds(t, c, cluster).Active; len(active) != 0 {
				t.Fatalf("rebuilds with two unfenced primaries = %v, want none", active)
			}
			if hold := runtimeCM(t, c, name)[runtimeKeyMemberHold]; hold != "" {
				t.Fatalf("MEMBER_HOLD = %q, want empty", hold)
			}
			if n := memberDeletes(log); n != 0 {
				t.Fatalf("member deletes = %d, want 0: %q", n, log.lines)
			}
			if !slices.ContainsFunc(drainEvents(recorder), func(ev string) bool {
				return strings.HasPrefix(ev, "Warning "+ReasonPrimaryEndpointDisagrees+" ") && strings.Contains(ev, rival)
			}) {
				t.Fatalf("no Warning %s naming %s", ReasonPrimaryEndpointDisagrees, rival)
			}
		})
	}
}
