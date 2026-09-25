/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// 본 파일은 복제 그래프(설계 2026-09-25 §3.4)를 본다 — pg_stat_replication 으로 연결을 확인한
// 멤버만 fence 를 풀고, 끊긴 standby 는 CM primary 로 재지정하고, 모르면 아무것도 하지 않는다.

// graphExecutor 는 파드별로 답한다 — pg_stat_replication 은 rows, pg_is_in_recovery 는 recovery,
// ALTER SYSTEM 재지정은 기록한 뒤 onRetarget 을 부른다. 승격 exec 도 받는다.
type graphExecutor struct {
	mu         sync.Mutex
	log        *writeLog
	rows       map[string]string   // sender 파드 → psql -At 출력
	broken     map[string]bool     // pg_stat_replication exec 가 실패하는 sender
	recovery   map[string]string   // 파드 → t|f (없으면 exec 실패)
	retargets  map[string][]string // 파드 → 재지정 exec 명령(합친 문자열)
	onRetarget func(pod string)
	// failRetargets 는 앞으로 실패시킬 재지정 exec 수다.
	failRetargets int
}

func newGraphExecutor(log *writeLog) *graphExecutor {
	return &graphExecutor{
		log:       log,
		rows:      map[string]string{},
		broken:    map[string]bool{},
		recovery:  map[string]string{},
		retargets: map[string][]string{},
	}
}

func (g *graphExecutor) setRows(pod, out string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rows[pod] = out
}

func (g *graphExecutor) setRecovery(pod, answer string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recovery[pod] = answer
}

func (g *graphExecutor) retargetsOf(pod string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.retargets[pod]...)
}

func (g *graphExecutor) Exec(_ context.Context, target BackupSidecarTarget, command []string) ([]byte, error) {
	joined := strings.Join(command, " ")
	switch {
	case strings.Contains(joined, "pg_promote"):
		g.log.add("exec promote " + target.Pod)
		return []byte("PROMOTE_RESULT=promoted\n"), nil
	case strings.Contains(joined, "pg_read_file"):
		return []byte(promotionTestHistory), nil
	case strings.Contains(joined, "ALTER SYSTEM SET primary_conninfo"):
		g.log.add("exec retarget " + target.Pod)
		g.mu.Lock()
		g.retargets[target.Pod] = append(g.retargets[target.Pod], joined)
		hook := g.onRetarget
		fail := g.failRetargets > 0
		if fail {
			g.failRetargets--
		}
		g.mu.Unlock()
		if fail {
			return nil, fmt.Errorf("exec %s: ALTER SYSTEM rejected", target.Pod)
		}
		if hook != nil {
			hook(target.Pod)
		}
		return nil, nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case strings.Contains(joined, "pg_stat_replication"):
		g.log.add("exec graph " + target.Pod)
		if g.broken[target.Pod] {
			return nil, fmt.Errorf("exec %s: unreachable", target.Pod)
		}
		return []byte(g.rows[target.Pod]), nil
	case strings.Contains(joined, recoveryQuery):
		g.log.add("exec recovery " + target.Pod)
		answer, ok := g.recovery[target.Pod]
		if !ok {
			return nil, fmt.Errorf("exec %s: unreachable", target.Pod)
		}
		return []byte(answer + "\n"), nil
	}
	g.log.add("exec other " + target.Pod)
	return nil, nil
}

// graphWorld 는 샤드 0 멤버 파드·PVC·런타임 CM(primary = ordinal-0, epoch 1) 위의 reconciler 다.
type graphWorld struct {
	name     string
	cluster  *postgresv1alpha1.PostgresCluster
	c        client.Client
	r        *PostgresClusterReconciler
	exec     *graphExecutor
	log      *writeLog
	recorder *events.FakeRecorder
}

// graphMember 는 샤드 0 의 ordinal 번째 멤버 파드 이름이다.
func graphMember(name string, ordinal int) string {
	return fmt.Sprintf("%s-%d", ShardStatefulSetName(name, 0), ordinal)
}

// memberPVC 는 멤버 데이터 PVC 다.
func memberPVC(pod string, fenced bool) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: fencing.PVCName(pod), Namespace: runtimeTestNS, Labels: map[string]string{},
	}}
	if fenced {
		pvc.Labels[fencing.FenceLabelKey] = fencing.FenceLabelValue
	}
	return pvc
}

// newGraphWorld 는 members 개 멤버(0 = primary, 나머지 standby)와 fenced 서수의 PVC 를 만든다.
// skip 서수는 파드가 없다(PVC 는 있다). ordinal-0 은 in recovery 가 아니고 나머지는 in recovery 다.
func newGraphWorld(t *testing.T, name string, members int, fenced, skip []int) graphWorld {
	t.Helper()
	cluster, cm := runtimeCluster(name)
	objs := []client.Object{cluster, cm}
	log := &writeLog{}
	exec := newGraphExecutor(log)
	for i := range members {
		pod := graphMember(name, i)
		objs = append(objs, memberPVC(pod, slices.Contains(fenced, i)))
		exec.recovery[pod] = pgBoolTrue
		if i == 0 {
			exec.recovery[pod] = pgBoolFalse
		}
		if slices.Contains(skip, i) {
			continue
		}
		role := statusapi.RoleReplica
		if i == 0 {
			role = statusapi.RolePrimary
		}
		objs = append(objs, shardMember(name, i, role))
	}
	funcs := log.funcs()
	funcs.Delete = func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		log.add("delete " + objKind(obj) + "/" + obj.GetName())
		return cli.Delete(ctx, obj, opts...)
	}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(funcs).Build()
	recorder := events.NewFakeRecorder(wideEventBuffer)
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: exec, Recorder: recorder}
	return graphWorld{name: name, cluster: cluster, c: c, r: r, exec: exec, log: log, recorder: recorder}
}

// shards 는 관측·행동이 읽는 샤드 상태다(이름·서수).
func (w graphWorld) shards() []postgresv1alpha1.ShardStatus {
	return []postgresv1alpha1.ShardStatus{{Name: ShardIDForOrdinal(0), Ordinal: 0}}
}

// step 은 한 reconcile 의 그래프 관측과 행동이다.
func (w graphWorld) step(now time.Time) replicationGraph {
	graphs := w.r.observeGraphs(context.Background(), w.cluster, w.shards())
	w.r.reconcileGraphActions(context.Background(), w.cluster, w.shards(), graphs, now)
	return graphs[ShardIDForOrdinal(0)]
}

// fenced 는 멤버 PVC 의 fence 라벨 여부다.
func (w graphWorld) fenced(t *testing.T, pod string) bool {
	t.Helper()
	var pvc corev1.PersistentVolumeClaim
	if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: fencing.PVCName(pod)}, &pvc); err != nil {
		t.Fatalf("get pvc of %s: %v", pod, err)
	}
	return pvc.Labels[fencing.FenceLabelKey] == fencing.FenceLabelValue
}

// absences 는 기록된 끊김(0 이 아닌 값)이다.
func (w graphWorld) absences() map[string]time.Time {
	w.r.graphMu.Lock()
	defer w.r.graphMu.Unlock()
	out := map[string]time.Time{}
	for pod, since := range w.r.memberAbsentSince[runtimeTestNS+"/"+w.name] {
		if !since.IsZero() {
			out[pod] = since
		}
	}
	return out
}

// walRow 는 psql -At 의 pg_stat_replication 한 줄이다.
func walRow(pod, state string) string {
	return pod + "|" + state + "\n"
}

// TestGraphUnfencesLinkedMember 는 fenced 멤버가 primary 의 pg_stat_replication 에 연결 state 로
// 보이면 fence 를 떼고 MemberUnfenced 를 남기는지 본다 — streaming·catchup·backup 이 연결이다
// (backup 은 진척이 아니지만 연결이다). startup·stopping 은 연결이 아니다.
func TestGraphUnfencesLinkedMember(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state  string
		linked bool
	}{
		{walsenderStreaming, true},
		{walsenderCatchup, true},
		{walsenderBackup, true},
		{walsenderStartup, false},
		{walsenderStopping, false},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			t.Parallel()
			w := newGraphWorld(t, "unfence-"+tc.state, 2, []int{1}, nil)
			primary, standby := graphMember(w.name, 0), graphMember(w.name, 1)
			w.exec.setRows(primary, walRow(standby, tc.state))

			g := w.step(time.Now())

			if !g.known || g.linked[standby] != tc.linked {
				t.Fatalf("graph = %+v, want known with %s linked=%v", g, standby, tc.linked)
			}
			if tc.linked && g.upstream[standby] != primary {
				t.Fatalf("upstream[%s] = %q, want %s", standby, g.upstream[standby], primary)
			}
			if got := w.fenced(t, standby); got == tc.linked {
				t.Fatalf("%s fenced = %v after state %s, want %v", standby, got, tc.state, !tc.linked)
			}
			wantEvents := 0
			if tc.linked {
				wantEvents = 1
			}
			if n := countEvents(w.recorder, "Normal "+memberUnfencedReason+" "); n != wantEvents {
				t.Fatalf("%s events = %d, want %d", memberUnfencedReason, n, wantEvents)
			}
			if n := w.log.count("delete "); n != 0 {
				t.Fatalf("deletes = %d, want 0: %q", n, w.log.lines)
			}
			if since, ok := w.absences()[primary]; ok {
				t.Fatalf("primary %s recorded absent since %s, want no record for the graph root", primary, since)
			}
		})
	}
}

// TestGraphFollowsCascade 는 직결이 전부가 아니면 확인된 standby 의 pg_stat_replication 으로
// 내려가는지(0-2 가 0-1 아래) 보고, 직결이 전부면 standby 에 exec 하지 않는지 본다.
func TestGraphFollowsCascade(t *testing.T) {
	t.Parallel()
	t.Run("chain", func(t *testing.T) {
		t.Parallel()
		w := newGraphWorld(t, "cascade", 3, []int{2}, nil)
		p, s1, s2 := graphMember(w.name, 0), graphMember(w.name, 1), graphMember(w.name, 2)
		w.exec.setRows(p, walRow(s1, walsenderStreaming))
		w.exec.setRows(s1, walRow(s2, walsenderStreaming))

		g := w.step(time.Now())

		if !g.known || !g.linked[s1] || !g.linked[s2] {
			t.Fatalf("graph = %+v, want %s and %s linked", g, s1, s2)
		}
		if g.upstream[s1] != p || g.upstream[s2] != s1 {
			t.Fatalf("upstream = %v, want %s→%s, %s→%s", g.upstream, s1, p, s2, s1)
		}
		if w.fenced(t, s2) {
			t.Fatalf("%s streams from %s but stays fenced", s2, s1)
		}
		for pod, want := range map[string]int{p: 1, s1: 1, s2: 0} {
			if n := w.log.count("exec graph " + pod + ""); n != want {
				t.Fatalf("graph execs on %s = %d, want %d: %q", pod, n, want, w.log.lines)
			}
		}
	})
	t.Run("all direct", func(t *testing.T) {
		t.Parallel()
		w := newGraphWorld(t, "direct", 3, nil, nil)
		p, s1, s2 := graphMember(w.name, 0), graphMember(w.name, 1), graphMember(w.name, 2)
		w.exec.setRows(p, walRow(s1, walsenderStreaming)+walRow(s2, walsenderStreaming))

		if g := w.step(time.Now()); !g.known || !g.linked[s1] || !g.linked[s2] {
			t.Fatalf("graph = %+v, want both standbys linked", g)
		}
		if n := w.log.count("exec graph "); n != 1 {
			t.Fatalf("graph execs = %d, want 1 (primary only): %q", n, w.log.lines)
		}
	})
}

// TestGraphUnknownActsOnNothing 는 exec 오류·파싱 불가를 모름으로 두는지 본다 — 모름은 없음이
// 아니다(I10): fence 해제·재지정·끊김 기록이 0 이다.
func TestGraphUnknownActsOnNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(w graphWorld)
	}{
		{"primary exec fails", func(w graphWorld) { w.exec.broken[graphMember(w.name, 0)] = true }},
		{"unparseable row", func(w graphWorld) { w.exec.setRows(graphMember(w.name, 0), "garbage\n") }},
		{"unknown state", func(w graphWorld) {
			w.exec.setRows(graphMember(w.name, 0), walRow(graphMember(w.name, 1), "rewinding"))
		}},
		{"runtime primary in recovery", func(w graphWorld) {
			w.exec.setRecovery(graphMember(w.name, 0), pgBoolTrue)
			w.exec.setRows(graphMember(w.name, 0), walRow(graphMember(w.name, 2), walsenderStreaming))
		}},
		{"cascade exec fails", func(w graphWorld) {
			w.exec.setRows(graphMember(w.name, 0), walRow(graphMember(w.name, 1), walsenderStreaming))
			w.exec.broken[graphMember(w.name, 1)] = true
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newGraphWorld(t, fmt.Sprintf("unknown-%d", i), 3, []int{2}, nil)
			tc.setup(w)
			stranded := graphMember(w.name, 2)
			t0 := time.Now()

			for _, at := range []time.Duration{0, 2 * memberRetargetAfter, 4 * memberRetargetAfter} {
				if g := w.step(t0.Add(at)); g.known {
					t.Fatalf("graph = %+v, want unknown", g)
				}
			}

			if !w.fenced(t, stranded) {
				t.Fatalf("%s unfenced without a confirmed connection", stranded)
			}
			if n := w.log.count("exec retarget "); n != 0 {
				t.Fatalf("retargets = %d, want 0 while the graph is unknown: %q", n, w.log.lines)
			}
			if got := w.absences(); len(got) != 0 {
				t.Fatalf("absence records = %v, want none while the graph is unknown", got)
			}
		})
	}
}

// TestGraphPartialUnfencesConfirmed 는 연쇄 exec 만 실패한 그래프(부분)에서 primary 가 확인한 연결만
// fence 를 풀고, 확인되지 않은 멤버는 모름으로 두는지 본다 — 끊김 기록·재지정 0.
func TestGraphPartialUnfencesConfirmed(t *testing.T) {
	t.Parallel()
	w := newGraphWorld(t, "partial", 3, []int{1, 2}, nil)
	p, confirmed, unknown := graphMember(w.name, 0), graphMember(w.name, 1), graphMember(w.name, 2)
	w.exec.setRows(p, walRow(confirmed, walsenderStreaming))
	w.exec.broken[confirmed] = true
	t0 := time.Now()

	for _, at := range []time.Duration{0, 2 * memberRetargetAfter} {
		if g := w.step(t0.Add(at)); g.known || !g.linked[confirmed] {
			t.Fatalf("graph = %+v, want partial (unknown) with %s linked", g, confirmed)
		}
	}

	if w.fenced(t, confirmed) {
		t.Fatalf("%s streams from %s but stays fenced", confirmed, p)
	}
	if !w.fenced(t, unknown) {
		t.Fatalf("%s unfenced without a confirmed connection", unknown)
	}
	if n := w.log.count("exec retarget "); n != 0 {
		t.Fatalf("retargets = %d, want 0 on a partial graph: %q", n, w.log.lines)
	}
	if got := w.absences(); len(got) != 0 {
		t.Fatalf("absence records = %v, want none on a partial graph", got)
	}
}

// TestGraphRetargetsFencedSurvivor 는 승격(promoteAndPublish + fenceNonTargetMembers) 직후 fenced
// 생존 standby 가 memberRetargetAfter 넘게 끊기면 primary_conninfo 를 CM 호스트로 재지정하고
// (PodReady ∧ in recovery, fence 무관), streaming 뒤 fence 를 푸는지 본다 — 재구축(삭제) 0 · 같은
// 대상 두 번째 exec 0 · 대상이 바뀌면 다시.
func TestGraphRetargetsFencedSurvivor(t *testing.T) {
	t.Parallel()
	const name = "survivor"
	ctx := context.Background()
	w := newGraphWorld(t, name, 3, nil, []int{0})
	defer DeleteMetricsFor(runtimeTestNS, name)
	oldPrimary, winner, survivor := graphMember(name, 0), graphMember(name, 1), graphMember(name, 2)
	w.exec.setRecovery(winner, pgBoolFalse)

	// failover: 0-0 이 죽고 0-1 승격 — 생존자 0-2 는 fenced, 옛 primary 를 가리킨 채 끊겼다.
	target := failover.PromotionTarget{ShardName: "shard-0", Pod: winner, Endpoint: podDNS(name, winner)}
	if err := w.r.promoteAndPublish(ctx, w.cluster, "shard-0", target, oldPrimary, promotionFailover); err != nil {
		t.Fatalf("promoteAndPublish: %v", err)
	}
	if !w.fenced(t, survivor) {
		t.Fatalf("setup: survivor %s must be fenced by the promotion", survivor)
	}

	t0 := time.Now()
	for at := time.Duration(0); at <= memberRetargetAfter; at += statusPollInterval {
		w.step(t0.Add(at))
	}
	if got := w.exec.retargetsOf(survivor); len(got) != 0 {
		t.Fatalf("retargeted within memberRetargetAfter: %q", got)
	}

	// 넘으면 재지정 — 부트스트랩 형식 conninfo(host = CM 호스트, application_name = 파드).
	w.step(t0.Add(memberRetargetAfter + statusPollInterval))
	got := w.exec.retargetsOf(survivor)
	wantConninfo := standbyConninfo(podDNS(name, winner), survivor)
	if len(got) != 1 || !strings.Contains(got[0], "ALTER SYSTEM SET primary_conninfo = "+sqlLiteral(wantConninfo)) ||
		!strings.Contains(got[0], "SELECT pg_reload_conf()") {
		t.Fatalf("retarget execs = %q, want one ALTER SYSTEM SET primary_conninfo = %s + pg_reload_conf()", got, sqlLiteral(wantConninfo))
	}
	if n := countEvents(w.recorder, "Normal "+memberRetargetedReason+" "); n != 1 {
		t.Fatalf("%s events = %d, want 1", memberRetargetedReason, n)
	}

	// 붙기 전 — 같은 대상에 두 번째 exec 없음.
	for at := 2 * statusPollInterval; at <= 2*memberRetargetAfter; at += statusPollInterval {
		w.step(t0.Add(memberRetargetAfter + at))
	}
	if got := w.exec.retargetsOf(survivor); len(got) != 1 {
		t.Fatalf("retarget execs for the same target = %d, want 1", len(got))
	}

	// streaming → fence 해제.
	w.exec.setRows(winner, walRow(survivor, walsenderStreaming))
	t1 := t0.Add(3 * memberRetargetAfter)
	if g := w.step(t1); !g.linked[survivor] {
		t.Fatalf("graph = %+v, want %s linked", g, survivor)
	}
	if w.fenced(t, survivor) {
		t.Fatalf("%s streams from %s but stays fenced", survivor, winner)
	}
	if !w.fenced(t, oldPrimary) {
		t.Fatalf("old primary %s unfenced without a connection", oldPrimary)
	}
	if n := countEvents(w.recorder, "Normal "+memberUnfencedReason+" "); n != 1 {
		t.Fatalf("%s events = %d, want 1", memberUnfencedReason, n)
	}
	if n := w.log.count("delete "); n != 0 {
		t.Fatalf("deletes = %d, want 0 (no rebuild): %q", n, w.log.lines)
	}

	// 대상이 바뀌면(다음 primary 0-3) 다시 — 0-1 은 in recovery 가 아니라 재지정하지 않는다.
	next := graphMember(name, 3)
	if err := w.c.Create(ctx, shardMember(name, 3, statusapi.RolePrimary)); err != nil {
		t.Fatalf("create %s: %v", next, err)
	}
	if err := w.c.Create(ctx, memberPVC(next, false)); err != nil {
		t.Fatalf("create pvc of %s: %v", next, err)
	}
	w.exec.setRecovery(next, pgBoolFalse)
	if err := w.r.publishRuntime(ctx, w.cluster, "shard-0", shardRuntime{
		PrimaryEndpoint: podDNS(name, next), PromotionEpoch: 3, ClusterInitialized: true,
	}); err != nil {
		t.Fatalf("publish %s: %v", next, err)
	}
	t2 := t1.Add(statusPollInterval)
	for at := time.Duration(0); at <= memberRetargetAfter+statusPollInterval; at += statusPollInterval {
		w.step(t2.Add(at))
	}
	got = w.exec.retargetsOf(survivor)
	if len(got) != 2 || !strings.Contains(got[1], sqlLiteral(standbyConninfo(podDNS(name, next), survivor))) {
		t.Fatalf("retarget execs after the primary moved = %q, want a second one to %s", got, next)
	}
	if got := w.exec.retargetsOf(winner); len(got) != 0 {
		t.Fatalf("retargeted %s which is not in recovery: %q", winner, got)
	}
}

// TestGraphRetargetPreconditions 는 재지정 조건을 본다 — PodReady 가 아닌 standby 에는 exec 0 이고,
// 재지정 exec 가 실패하면 memberRetargetAfter 안에는 다시 하지 않고 지난 뒤 한 번 더 한다.
func TestGraphRetargetPreconditions(t *testing.T) {
	t.Parallel()
	t.Run("pod not ready", func(t *testing.T) {
		t.Parallel()
		w := newGraphWorld(t, "retarget-notready", 2, nil, nil)
		standby := graphMember(w.name, 1)
		setPodReady(t, w.c, standby, corev1.ConditionFalse)
		t0 := time.Now()
		for at := time.Duration(0); at <= 2*memberRetargetAfter; at += statusPollInterval {
			w.step(t0.Add(at))
		}
		if n := w.log.count("exec retarget ") + w.log.count("exec recovery "+standby); n != 0 {
			t.Fatalf("execs on a not-ready standby = %d, want 0: %q", n, w.log.lines)
		}
	})
	t.Run("failed retarget retried after the window", func(t *testing.T) {
		t.Parallel()
		w := newGraphWorld(t, "retarget-retry", 2, nil, nil)
		standby := graphMember(w.name, 1)
		w.exec.failRetargets = 1
		t0 := time.Now()
		first := memberRetargetAfter + statusPollInterval
		for at := time.Duration(0); at <= first+memberRetargetAfter; at += statusPollInterval {
			w.step(t0.Add(at))
		}
		if got := w.exec.retargetsOf(standby); len(got) != 1 {
			t.Fatalf("retarget execs within memberRetargetAfter of a failure = %d, want 1", len(got))
		}
		for at := first + memberRetargetAfter + statusPollInterval; at <= first+3*memberRetargetAfter; at += statusPollInterval {
			w.step(t0.Add(at))
		}
		if got := w.exec.retargetsOf(standby); len(got) != 2 {
			t.Fatalf("retarget execs after a failure and a success = %d, want 2", len(got))
		}
		if n := countEvents(w.recorder, "Normal "+memberRetargetedReason+" "); n != 1 {
			t.Fatalf("%s events = %d, want 1 (the success)", memberRetargetedReason, n)
		}
	})
}

// TestReplicationHealthSeesGraph 는 readyz 가 Ready 이고 WAL 도 가까운 replica 라도 그래프가
// 연결을 못 찾으면 ReplicationHealthy=False 이고 사유에 그래프 끊김이 있는지 본다 — 그래프를
// 모르면 판정은 그대로다.
func TestReplicationHealthSeesGraph(t *testing.T) {
	t.Parallel()
	const name = "health-graph"
	primary, standby := graphMember(name, 0), graphMember(name, 1)
	root := podDNS(name, primary)
	cases := []struct {
		name       string
		graph      *replicationGraph
		wantStatus metav1.ConditionStatus
	}{
		{"detached in graph", &replicationGraph{root: root, known: true, linked: map[string]bool{}}, metav1.ConditionFalse},
		{"linked in graph", &replicationGraph{root: root, known: true, linked: map[string]bool{standby: true}}, metav1.ConditionTrue},
		{"graph unknown", &replicationGraph{root: root}, metav1.ConditionTrue},
		{"no graph", nil, metav1.ConditionTrue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cluster := haCluster(name, postgresv1alpha1.ShardingModeNone)
			// 두 멤버의 WAL 위치가 같다 — 옛 판정(Ready · WAL 뒤처짐)으로는 건강하다.
			members := []string{primary, standby}
			objs := make([]client.Object, 0, 1+len(members))
			objs = append(objs, cluster)
			for _, pod := range members {
				p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: pod, Namespace: runtimeTestNS}}
				raw, err := json.Marshal(statusapi.Status{Role: statusapi.RoleReplica, WALLSNBytes: 1 << 30})
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				p.Annotations = map[string]string{statusapi.AnnotationKey: string(raw)}
				objs = append(objs, p)
			}
			scheme := runtimeScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
			r := &PostgresClusterReconciler{Client: c, Scheme: scheme}
			shards := []postgresv1alpha1.ShardStatus{{
				Name:     ShardIDForOrdinal(0),
				Primary:  &postgresv1alpha1.ShardEndpoint{Pod: primary, Ready: true},
				Replicas: []postgresv1alpha1.ShardEndpoint{{Pod: standby, Ready: true}},
			}}
			var graphs map[string]replicationGraph
			if tc.graph != nil {
				graphs = map[string]replicationGraph{ShardIDForOrdinal(0): *tc.graph}
			}

			r.reconcileReplicationHealth(context.Background(), cluster, shards, graphs)

			cond := meta.FindStatusCondition(cluster.Status.Conditions, ConditionReplicationHealthy)
			if cond == nil || cond.Status != tc.wantStatus {
				t.Fatalf("%s = %+v, want %s", ConditionReplicationHealthy, cond, tc.wantStatus)
			}
			if tc.wantStatus == metav1.ConditionFalse &&
				(cond.Reason != ReasonReplicaDisconnected || !strings.Contains(cond.Message, standby) ||
					!strings.Contains(cond.Message, "replication graph")) {
				t.Fatalf("%s = %+v, want %s naming %s and the replication graph", ConditionReplicationHealthy, cond, ReasonReplicaDisconnected, standby)
			}
		})
	}
}

// TestParseReplicationRows 는 psql -At 의 pg_stat_replication 출력을 읽는다 — 이름은 마지막
// 구분자 앞 전부, 같은 이름이 둘이면 연결 state 가 이기고, 모르는 state·구분자 없는 줄은 오류다.
func TestParseReplicationRows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		out     string
		want    map[string]string
		wantErr bool
	}{
		{"empty", "", map[string]string{}, false},
		{"rows", "a|streaming\nb|backup\n\n", map[string]string{"a": walsenderStreaming, "b": walsenderBackup}, false},
		{"separator in name", "we|ird|catchup\n", map[string]string{"we|ird": walsenderCatchup}, false},
		{"linked state wins", "a|streaming\na|startup\n", map[string]string{"a": walsenderStreaming}, false},
		{"no separator", "garbage\n", nil, true},
		{"unknown state", "a|rewinding\n", nil, true},
	}
	for _, tc := range cases {
		got, err := parseReplicationRows([]byte(tc.out))
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
		if tc.wantErr {
			continue
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: rows = %v, want %v", tc.name, got, tc.want)
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Fatalf("%s: rows = %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}

// TestLinkedCandidateFilter 는 memberRetargetAfter 넘게 끊긴 후보를 연결 기록이 있는 후보가 있을
// 때만 빼는지 본다(설계 §3.3). 기록이 없는 후보는 빼지도 세지도 않고, 원본은 그대로다.
func TestLinkedCandidateFilter(t *testing.T) {
	t.Parallel()
	now := time.Now()
	long := now.Add(-memberRetargetAfter - time.Second)
	cases := []struct {
		name    string
		aReady  bool
		records map[string]time.Time
		wantB   bool
	}{
		{"linked candidate excludes detached", true, map[string]time.Time{"a": {}, "b": long}, false},
		{"recently absent counts as linked", true, map[string]time.Time{"a": now.Add(-10 * time.Second), "b": long}, false},
		{"no record is not a link", true, map[string]time.Time{"b": long}, true},
		{"both detached", true, map[string]time.Time{"a": long, "b": long.Add(-time.Minute)}, true},
		{"at the threshold stays", true, map[string]time.Time{"a": {}, "b": now.Add(-memberRetargetAfter)}, true},
		{"linked member not ready", false, map[string]time.Time{"a": {}, "b": long}, true},
	}
	for _, tc := range cases {
		shard := postgresv1alpha1.ShardStatus{Name: "shard-0", Replicas: []postgresv1alpha1.ShardEndpoint{
			{Pod: "a", Ready: tc.aReady}, {Pod: "b", Ready: true},
		}}
		got := linkedCandidateFilter(shard, tc.records, now)
		if got.Replicas[1].Ready != tc.wantB {
			t.Errorf("%s: candidate b ready = %v, want %v", tc.name, got.Replicas[1].Ready, tc.wantB)
		}
		if !shard.Replicas[1].Ready {
			t.Errorf("%s: filter changed the input shard status", tc.name)
		}
	}
}

// TestFailoverSkipsDetachedStandby 는 수신 위치가 더 앞선 standby 라도 memberRetargetAfter 넘게
// 끊겼으면 연결 기록이 있는 후보를 승격하는지 본다 — 연결 기록이 없으면 OP3 순서 그대로다.
func TestFailoverSkipsDetachedStandby(t *testing.T) {
	t.Parallel()
	const gib = int64(1) << 30
	cases := []struct {
		name    string
		linked  bool
		promote int
	}{
		{"linked candidate wins", true, 1},
		{"no connection record", false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name := "skip-detached"
			if !tc.linked {
				name = "skip-unrecorded"
			}
			ctx := context.Background()
			cluster, cm := runtimeCluster(name)
			linked, detached := shardMember(name, 1, statusapi.RoleReplica), shardMember(name, 2, statusapi.RoleReplica)
			setPosition(t, linked, 100*gib, 0)
			setPosition(t, detached, 101*gib, 0)
			log := &writeLog{}
			scheme := runtimeScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(cluster, cm, linked, detached).
				WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).Build()
			r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: newGraphExecutor(log)}
			defer DeleteMetricsFor(runtimeTestNS, name)
			now := time.Now()
			key := runtimeTestNS + "/" + name
			r.failoverPending = map[string]time.Time{key: now.Add(-failoverDebounceThreshold - time.Second)}
			records := map[string]time.Time{detached.Name: now.Add(-memberRetargetAfter - time.Second)}
			if tc.linked {
				records[linked.Name] = time.Time{}
			}
			r.memberAbsentSince = map[string]map[string]time.Time{key: records}
			shards := []postgresv1alpha1.ShardStatus{{
				Name:    ShardIDForOrdinal(0),
				Primary: &postgresv1alpha1.ShardEndpoint{Pod: graphMember(name, 0), Endpoint: podDNS(name, graphMember(name, 0))},
				Replicas: []postgresv1alpha1.ShardEndpoint{
					{Pod: linked.Name, Endpoint: podDNS(name, linked.Name), Ready: true},
					{Pod: detached.Name, Endpoint: podDNS(name, detached.Name), Ready: true},
				},
			}}

			if _, promoted := r.reconcileFailover(ctx, cluster, shards, postgresv1alpha1.ClusterPhaseReady, now); !promoted {
				t.Fatalf("no promotion: %q", log.lines)
			}
			want := graphMember(name, tc.promote)
			if n := log.count("exec promote " + want); n != 1 {
				t.Fatalf("promotions of %s = %d, want 1: %q", want, n, log.lines)
			}
			if !shards[0].Replicas[1].Ready {
				t.Fatal("candidate filter changed the shard status written to the CR")
			}
		})
	}
}

// TestStandbyConninfoMatchesBootstrap 는 재지정 conninfo 가 부트스트랩 스크립트가 쓰는 줄과 같은지
// 본다 — 같은 형식이어야 재기동 뒤 부트스트랩이 쓴 값과 요동하지 않는다.
func TestStandbyConninfoMatchesBootstrap(t *testing.T) {
	t.Parallel()
	w := newBootWorld(t)
	w.seed(t, map[string]string{"PG_VERSION": "18\n", "standby.signal": "", "postgresql.auto.conf": ""})
	if out, err := w.run(t, bootEnv(bootReplica, bootPrimary, "1", "1")); err != nil {
		t.Fatalf("bootstrap: %v\n%s", err, out)
	}
	want := "primary_conninfo = " + sqlLiteral(standbyConninfo(bootPrimary, bootReplica))
	if got := w.conninfoLines(t); len(got) != 1 || got[0] != want {
		t.Fatalf("bootstrap primary_conninfo = %q, want %q", got, want)
	}
}
