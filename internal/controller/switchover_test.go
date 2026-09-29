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

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// 본 파일은 제자리 switchover(설계 2026-09-25 §3.16, ADR-0032)를 본다 — 옛 primary 를 fast shutdown 으로
// 멈추고 같은 컨테이너가 standby 로 돌아온 뒤에야 승격하는지, 그동안 자동 failover 는 창만 누적하는지, 새
// reconciler 가 CR 애노테이션에서 잇는지. 시계는 advanceSwitchover·reconcileFailover 에 넘기는 now 다.

const (
	// switchoverMembers 는 시험 샤드 멤버 수다 — 0 = 옛 primary P, 1 = 대상 t, 2 = 다른 standby u.
	switchoverMembers = 3
	// switchoverBaseLSN 은 시험 WAL 위치의 기준이다(0/5000000).
	switchoverBaseLSN uint64 = 0x5000000
	// shutdownRecordBytes 는 종료 체크포인트가 L0 뒤에 더하는 WAL 이다.
	shutdownRecordBytes uint64 = 0x98
	// behindBytes 는 옛 primary 끝보다 모자란 standby 의 거리다.
	behindBytes uint64 = 0x2000
	// checkpointTook 은 CHECKPOINT 가 걸리는 시간이다 — stopAt 이 그 뒤인지 잰다.
	checkpointTook = 50 * time.Millisecond
)

// simRole 은 시험 멤버의 postgres 상태다.
type simRole string

const (
	simPrimary simRole = "primary"
	simStandby simRole = "standby"
	simDown    simRole = "down" // 정지·재기동 중 — SQL exec 가 실패한다
)

// stopMode 는 정지 exec 의 결과다.
type stopMode string

const (
	stopOK        stopMode = "ok"         // L0 · STOP_REQUESTED 를 내고 postgres 가 내려간다
	stopFailEarly stopMode = "fail-early" // ALTER SYSTEM 에서 실패 — postgres 는 primary 그대로
)

// simMember 는 멤버 하나의 postgres 흉내다.
type simMember struct {
	role   simRole
	lsn    uint64 // primary = 현재 쓰기 위치 · standby = 수신(=재생) 끝
	marker string // 승격 마커 epoch 글자
}

// switchoverExecutor 는 멤버별 postgres 흉내로 exec 에 답하고 한 줄씩 남긴다.
type switchoverExecutor struct {
	mu         sync.Mutex
	log        *writeLog
	members    map[string]*simMember
	rows       map[string]string // sender → pg_stat_replication 출력
	stop       stopMode
	checkpoint time.Duration
	scripts    map[string][]string // "<kind> <pod>" → 스크립트 본문
}

func newSwitchoverExecutor(log *writeLog) *switchoverExecutor {
	return &switchoverExecutor{
		log: log, members: map[string]*simMember{}, rows: map[string]string{}, stop: stopOK, scripts: map[string][]string{},
	}
}

func (x *switchoverExecutor) Exec(_ context.Context, target BackupSidecarTarget, command []string) ([]byte, error) {
	joined := strings.Join(command, " ")
	x.mu.Lock()
	defer x.mu.Unlock()
	m := x.members[target.Pod]
	if m == nil {
		return nil, fmt.Errorf("exec %s: no such member", target.Pod)
	}
	switch {
	case strings.Contains(joined, "pg_promote"):
		return x.promote(target.Pod, m, command)
	case strings.Contains(joined, "pg_ctl"):
		return x.stopMember(target.Pod, m, command)
	case strings.Contains(joined, "standby.signal"):
		x.restore(target.Pod, m, command)
		return nil, nil
	case strings.Contains(joined, checkpointQuery):
		x.log.add("exec checkpoint " + target.Pod)
		time.Sleep(x.checkpoint)
		return m.answer(target.Pod, "", needPrimary)
	case strings.Contains(joined, "pg_last_wal_receive_lsn"):
		x.log.add("exec lsn " + target.Pod)
		return m.answer(target.Pod, recoveryAnswer(m)+psqlFieldSeparator+formatLSN(m.lsn), needRunning)
	case strings.Contains(joined, recoveryQuery):
		x.log.add("exec recovery " + target.Pod)
		return m.answer(target.Pod, recoveryAnswer(m), needRunning)
	case strings.Contains(joined, "pg_stat_replication"):
		x.log.add("exec graph " + target.Pod)
		if m.role == simStandby {
			return nil, nil
		}
		return m.answer(target.Pod, x.rows[target.Pod], needPrimary)
	case strings.Contains(joined, "pg_read_file"):
		return []byte(promotionTestHistory), nil
	case strings.Contains(joined, promotedPrimaryMarker):
		return m.answer(target.Pod, m.marker, needRunning)
	}
	x.log.add("exec other " + target.Pod)
	return nil, nil
}

// simNeed 는 exec 가 답하려면 멤버가 있어야 할 상태다.
type simNeed int

const (
	needRunning simNeed = iota // 도는 postgres(primary · standby)
	needPrimary                // primary 만
)

// answer 는 멤버가 need 를 채우면 text 를, 아니면 exec 실패를 준다.
func (m *simMember) answer(pod, text string, need simNeed) ([]byte, error) {
	if m.role == simDown || (need == needPrimary && m.role != simPrimary) {
		return nil, fmt.Errorf("exec %s: postgres not accepting connections", pod)
	}
	return []byte(text + "\n"), nil
}

func recoveryAnswer(m *simMember) string {
	if m.role == simPrimary {
		return pgBoolFalse
	}
	return pgBoolTrue
}

// promote 는 승격 스크립트 흉내다 — standby 만 승격하고 마커에 epoch 를 쓴다.
func (x *switchoverExecutor) promote(pod string, m *simMember, command []string) ([]byte, error) {
	epoch := command[len(command)-1]
	x.log.add("exec promote " + pod + " epoch=" + epoch)
	switch m.role {
	case simStandby:
		m.role, m.marker = simPrimary, epoch
		return []byte("PROMOTE_RESULT=promoted\n"), nil
	case simPrimary:
		return []byte("PROMOTE_RESULT=noop-already-primary\n"), nil
	}
	return nil, fmt.Errorf("exec %s: postgres not running", pod)
}

// stopMember 는 정지 스크립트 흉내다 — fast shutdown 은 종료 체크포인트를 streaming standby 에 flush 한 뒤 끝난다.
func (x *switchoverExecutor) stopMember(pod string, m *simMember, command []string) ([]byte, error) {
	x.log.add("exec stop " + pod)
	x.scripts["stop "+pod] = slices.Clone(command)
	if x.stop == stopFailEarly || m.role != simPrimary {
		return nil, fmt.Errorf("exec %s: ALTER SYSTEM rejected", pod)
	}
	l0 := m.lsn
	m.role, m.lsn, m.marker = simDown, l0+shutdownRecordBytes, ""
	for line := range strings.SplitSeq(strings.TrimSpace(x.rows[pod]), "\n") {
		follower, state, _ := strings.Cut(line, psqlFieldSeparator)
		if f := x.members[follower]; f != nil && state == walsenderStreaming {
			f.lsn = m.lsn
		}
	}
	return []byte(stopLSNPrefix + formatLSN(l0) + "\n" + stopRequestedMarker + "\n"), nil
}

// restore 는 정지 전 실패를 되돌리는 스크립트 흉내다 — 마커를 epoch 로 쓴다.
func (x *switchoverExecutor) restore(pod string, m *simMember, command []string) {
	epoch := command[len(command)-1]
	x.log.add("exec restore " + pod + " epoch=" + epoch)
	x.scripts["restore "+pod] = slices.Clone(command)
	m.marker = epoch
}

func (x *switchoverExecutor) set(pod string, role simRole, lsn uint64) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.members[pod].role, x.members[pod].lsn = role, lsn
}

func (x *switchoverExecutor) member(pod string) simMember {
	x.mu.Lock()
	defer x.mu.Unlock()
	return *x.members[pod]
}

func (x *switchoverExecutor) script(kind, pod string) []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.scripts[kind+" "+pod]
}

// switchoverWorld 는 세 멤버 샤드(0 = CM primary · epoch 1, 1·2 = streaming standby) 위의 reconciler 다.
// 쓰기·exec·CR 패치 뒤 switchover 단계("state <phase>")가 한 줄씩 남는다.
type switchoverWorld struct {
	name     string
	c        client.Client
	r        *PostgresClusterReconciler
	exec     *switchoverExecutor
	log      *writeLog
	cms      *cmHistory
	recorder *events.FakeRecorder
	events   []string
	t0       time.Time
}

// phaseLog 는 CR 패치가 성공하면 그 뒤 switchover 단계를 "state <phase>"(없으면 none)로 남긴다.
func phaseLog(log *writeLog, funcs interceptor.Funcs) interceptor.Funcs {
	patch := funcs.Patch
	funcs.Patch = func(ctx context.Context, cli client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
		if err := patch(ctx, cli, obj, p, opts...); err != nil {
			return err
		}
		cluster, ok := obj.(*postgresv1alpha1.PostgresCluster)
		if !ok {
			return nil
		}
		phase := "none"
		if state, found, err := loadSwitchoverState(cluster); err == nil && found {
			phase = string(state.Phase)
		}
		log.add("state " + phase)
		return nil
	}
	return funcs
}

// newSwitchoverWorld 는 세계를 만든다 — mutate 는 클러스터를 만들기 전에 고친다(상태·정본 심기).
func newSwitchoverWorld(t *testing.T, name string, mutate ...func(*postgresv1alpha1.PostgresCluster)) *switchoverWorld {
	t.Helper()
	cluster, cm := runtimeCluster(name)
	cluster.Spec.Shards.Replicas = switchoverMembers - 1
	cluster.Status.Phase = postgresv1alpha1.ClusterPhaseReady
	for _, m := range mutate {
		m(cluster)
	}
	log := &writeLog{}
	exec := newSwitchoverExecutor(log)
	objs := slices.Grow([]client.Object{cluster, cm}, 2*switchoverMembers)
	var rows strings.Builder
	for i := range switchoverMembers {
		pod := graphMember(name, i)
		role, sim := statusapi.RoleReplica, simStandby
		if i == 0 {
			role, sim = statusapi.RolePrimary, simPrimary
		} else {
			rows.WriteString(walRow(pod, walsenderStreaming))
		}
		p := shardMember(name, i, role)
		p.UID = types.UID(pod + "-uid-0")
		p.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
		setPosition(t, p, int64(switchoverBaseLSN), 0)
		exec.members[pod] = &simMember{role: sim, lsn: switchoverBaseLSN}
		if i == 0 {
			exec.members[pod].marker = "1"
			withMarker(t, p)
		}
		objs = append(objs, p, memberPVC(pod, false))
	}
	exec.rows[graphMember(name, 0)] = rows.String()

	cms := &cmHistory{}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(phaseLog(log, cms.watch(logDeletes(log, log.funcs())))).Build()
	recorder := events.NewFakeRecorder(wideEventBuffer)
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: exec, Recorder: recorder}
	t.Cleanup(func() { DeleteMetricsFor(runtimeTestNS, name) })
	return &switchoverWorld{name: name, c: c, r: r, exec: exec, log: log, cms: cms, recorder: recorder, t0: time.Now()}
}

// withSwitchover 는 진행 중 switchover 를 CR 에 심는다.
func withSwitchover(state switchoverState) func(*postgresv1alpha1.PostgresCluster) {
	return func(cluster *postgresv1alpha1.PostgresCluster) {
		state.Version = switchoverStateVersion
		raw, _ := json.Marshal(state)
		cluster.Annotations[switchoverStateAnnotation] = string(raw)
	}
}

func (w *switchoverWorld) member(i int) string {
	return graphMember(w.name, i)
}

// fresh 는 같은 세계의 새 reconciler 다 — 메모리(디바운스 창·그래프 기억)는 0 이다.
func (w *switchoverWorld) fresh() {
	w.r = &PostgresClusterReconciler{Client: w.c, Scheme: w.r.Scheme, PromotionPodExecutor: w.exec, Recorder: w.recorder}
}

func (w *switchoverWorld) cluster(t *testing.T) *postgresv1alpha1.PostgresCluster {
	t.Helper()
	var got postgresv1alpha1.PostgresCluster
	if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: w.name}, &got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	return &got
}

// shards 는 aggregate 가 본 샤드 0 이다.
func (w *switchoverWorld) shards(cluster *postgresv1alpha1.PostgresCluster) []postgresv1alpha1.ShardStatus {
	return []postgresv1alpha1.ShardStatus{aggregateShardStatus(context.Background(), w.c, cluster, 0, ShardServiceName(w.name, 0))}
}

// annotate 는 switchover-target 애노테이션을 단다.
func (w *switchoverWorld) annotate(t *testing.T, target string) {
	t.Helper()
	cluster := w.cluster(t)
	before := cluster.DeepCopy()
	cluster.Annotations[AnnotationSwitchoverTarget] = target
	if err := w.c.Patch(context.Background(), cluster, client.MergeFrom(before)); err != nil {
		t.Fatalf("annotate switchover target: %v", err)
	}
}

// handle 은 Reconcile 의 switchover 시작이다(aggregate → handleSwitchover).
func (w *switchoverWorld) handle(t *testing.T) {
	t.Helper()
	cluster := w.cluster(t)
	if err := w.r.handleSwitchover(context.Background(), cluster, w.shards(cluster)); err != nil {
		t.Fatalf("handleSwitchover: %v", err)
	}
	w.drain()
}

// advance 는 가짜 시계 now 의 advanceSwitchover 한 걸음이다.
func (w *switchoverWorld) advance(t *testing.T, now time.Time) switchoverProgress {
	t.Helper()
	cluster := w.cluster(t)
	progress, err := w.r.advanceSwitchover(context.Background(), cluster, w.shards(cluster), now)
	if err != nil {
		t.Logf("advanceSwitchover at +%s: %v", now.Sub(w.t0), err)
	}
	w.drain()
	return progress
}

// reconcile 은 한 번의 전체 Reconcile 이다.
func (w *switchoverWorld) reconcile(t *testing.T) ctrl.Result {
	t.Helper()
	res, err := w.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: runtimeTestNS, Name: w.name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	w.drain()
	return res
}

func (w *switchoverWorld) drain() {
	w.events = append(w.events, drainEvents(w.recorder)...)
}

// eventCount 는 지금까지의 이벤트 중 "<type> <reason> " 으로 시작하는 것의 수다.
func (w *switchoverWorld) eventCount(kind, reason string) int {
	w.drain()
	n := 0
	for _, ev := range w.events {
		if strings.HasPrefix(ev, kind+" "+reason+" ") {
			n++
		}
	}
	return n
}

func (w *switchoverWorld) state(t *testing.T) (switchoverState, bool) {
	t.Helper()
	state, found, err := loadSwitchoverState(w.cluster(t))
	if err != nil {
		t.Fatalf("load switchover state: %v", err)
	}
	return state, found
}

func (w *switchoverWorld) mustState(t *testing.T) switchoverState {
	t.Helper()
	state, found := w.state(t)
	if !found {
		t.Fatalf("switchover state absent, log %q", w.log.lines)
	}
	return state
}

// assertEnded 는 상태와 switchover-target 애노테이션이 지워졌는지 본다.
func (w *switchoverWorld) assertEnded(t *testing.T) {
	t.Helper()
	cluster := w.cluster(t)
	if raw, ok := cluster.Annotations[switchoverStateAnnotation]; ok {
		t.Fatalf("switchover state kept after the end: %s", raw)
	}
	if target, ok := cluster.Annotations[AnnotationSwitchoverTarget]; ok {
		t.Fatalf("switchover-target %q kept after the end", target)
	}
}

// pod 는 멤버 파드다.
func (w *switchoverWorld) pod(t *testing.T, name string) *corev1.Pod {
	t.Helper()
	var got corev1.Pod
	if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: name}, &got); err != nil {
		t.Fatalf("get pod %q: %v", name, err)
	}
	return &got
}

// report 는 인스턴스 매니저의 상태 애노테이션과 kubelet 의 PodReady·재시작 수를 쓴다.
func (w *switchoverWorld) report(t *testing.T, name string, role statusapi.Role, ready corev1.ConditionStatus, restarts int32) {
	t.Helper()
	ctx := context.Background()
	pod := w.pod(t, name)
	setMemberRole(pod, ShardServiceName(w.name, 0), role, time.Now())
	if err := w.c.Update(ctx, pod); err != nil {
		t.Fatalf("update pod %q: %v", name, err)
	}
	pod = w.pod(t, name)
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: pgContainerName, Ready: ready == corev1.ConditionTrue, RestartCount: restarts,
	}}
	if err := w.c.Status().Update(ctx, pod); err != nil {
		t.Fatalf("update pod %q status: %v", name, err)
	}
}

// down 은 정지 중인 옛 primary 다 — Stopping 보고 · PodReady False · SQL exec 실패.
func (w *switchoverWorld) down(t *testing.T, name string) {
	t.Helper()
	w.report(t, name, statusapi.RoleStopping, corev1.ConditionFalse, 0)
	w.exec.set(name, simDown, w.exec.member(name).lsn)
}

// restart 는 제자리 재기동이다 — 컨테이너 재시작 수 +1 · standby(Replica) 보고 · Ready · WAL 끝 그대로.
func (w *switchoverWorld) restart(t *testing.T, name string) {
	t.Helper()
	w.report(t, name, statusapi.RoleReplica, corev1.ConditionTrue, 1)
	w.exec.set(name, simStandby, w.exec.member(name).lsn)
}

// stoppedState 는 L0 에서 멈춘 옛 primary(0)의 Stopping 상태다 — 대상은 1.
func stoppedState(name string, since, stopAt time.Time) switchoverState {
	old := graphMember(name, 0)
	return switchoverState{
		Shard: "shard-0", OldPod: old, OldUID: old + "-uid-0", Target: graphMember(name, 1),
		Reason: switchoverRollout, Phase: switchoverStopping, Since: since, StopAt: stopAt, L0: formatLSN(switchoverBaseLSN),
	}
}

// position 은 멤버 상태 애노테이션의 수신 위치를 쓴다(receivePositions 가 읽는다).
func (w *switchoverWorld) position(t *testing.T, name string, lsn uint64) {
	t.Helper()
	pod := w.pod(t, name)
	setPosition(t, pod, int64(lsn), 0)
	if err := w.c.Update(context.Background(), pod); err != nil {
		t.Fatalf("update %s: %v", name, err)
	}
}

// stoppedMembers 는 fast shutdown 이 끝난 멤버들이다 — 셋 다 옛 primary 끝을 가졌다.
func (w *switchoverWorld) stoppedMembers(old simRole) {
	end := switchoverBaseLSN + shutdownRecordBytes
	w.exec.set(w.member(0), old, end)
	w.exec.set(w.member(1), simStandby, end)
	w.exec.set(w.member(2), simStandby, end)
}

// assertStopScript 는 정지 exec 가 한 스크립트로 conninfo(host=t, application_name=P) → standby.signal → 마커 rm
// → L0 → pg_ctl stop -m fast -W 순서인지 본다.
func (w *switchoverWorld) assertStopScript(t *testing.T, old, target string) {
	t.Helper()
	command := w.exec.script("stop", old)
	if len(command) == 0 {
		t.Fatalf("no stop exec on %s: %q", old, w.log.lines)
	}
	joined := strings.Join(command, "\n")
	steps := []string{
		"ALTER SYSTEM SET primary_conninfo",
		"standby.signal",
		promotedPrimaryMarker,
		"pg_current_wal_lsn",
		"pg_ctl\" stop",
	}
	script := command[2]
	last := -1
	for _, step := range steps {
		at := strings.Index(script, step)
		if step == steps[0] {
			at = strings.Index(script, `"$1"`)
		}
		if at <= last {
			t.Fatalf("stop script step %q out of order in:\n%s", step, joined)
		}
		last = at
	}
	for _, flag := range []string{"-m fast", "-W"} {
		if !strings.Contains(script, flag) {
			t.Fatalf("stop script lacks %q:\n%s", flag, script)
		}
	}
	statement := command[len(command)-1]
	host := primaryEndpointHost(podDNS(w.name, target))
	for _, want := range []string{"ALTER SYSTEM SET primary_conninfo", "host=" + host + " ", "application_name=" + old} {
		if !strings.Contains(statement, want) {
			t.Fatalf("conninfo statement %q lacks %q", statement, want)
		}
	}
}

// TestSwitchoverStopsInPlace 는 switchover-target 이 옛 primary 를 제자리에서 멈춘 뒤에야 승격하는지 본다(§3.16):
// CHECKPOINT → Stopping(stopAt·oldRestarts) 기록 → 정지 exec(conninfo=t · standby.signal · 마커 rm · L0 · pg_ctl
// stop -m fast -W) → 옛 primary 가 재기동해 in recovery 가 되기 전 승격 0 → t 승격(kind=switchover) · CM 에 hold 0 →
// 상태·애노테이션 삭제 · SwitchoverCompleted(공백) · switchover_duration_seconds. 파드 Delete 0.
func TestSwitchoverStopsInPlace(t *testing.T) {
	t.Parallel()
	w := newSwitchoverWorld(t, "so-in-place")
	old, target := w.member(0), w.member(1)
	switched := MetricPromotions.WithLabelValues(runtimeTestNS, w.name, "shard-0", string(promotionSwitchover))
	before := testutil.ToFloat64(switched)

	// 시작은 Preparing 만 쓴다.
	w.annotate(t, target)
	w.handle(t)
	if state := w.mustState(t); state.Phase != switchoverPreparing || state.Target != target || state.Reason != switchoverRequested {
		t.Fatalf("state after start = %+v, want Preparing toward %s (Requested)", state, target)
	}
	if n := w.log.count("exec promote "); n != 0 {
		t.Fatalf("promotion at start: %q", w.log.lines)
	}

	// 한 걸음 — CHECKPOINT → Stopping 기록 → 정지 exec.
	if got := w.advance(t, w.t0); got != switchoverWaiting {
		t.Fatalf("progress after the stop = %v, want waiting", got)
	}
	checkpoint := w.log.first("exec checkpoint " + old)
	stopping := w.log.first("state " + string(switchoverStopping))
	stop := w.log.first("exec stop " + old)
	if checkpoint < 0 || stopping <= checkpoint || stop <= stopping {
		t.Fatalf("order must be CHECKPOINT → Stopping recorded → stop exec, got %q", w.log.lines)
	}
	w.assertStopScript(t, old, target)
	state := w.mustState(t)
	if state.Phase != switchoverStopping || state.OldPod != old || state.OldRestarts != 0 || state.StopAt.IsZero() ||
		state.L0 != formatLSN(switchoverBaseLSN) {
		t.Fatalf("state after the stop = %+v, want Stopping of %s with stopAt, oldRestarts 0, L0 %s",
			state, old, formatLSN(switchoverBaseLSN))
	}

	// 옛 primary 가 아직 standby 로 안 돌아왔다 — 승격 0.
	w.down(t, old)
	for _, at := range []time.Duration{pgPromotePoll, 2 * pgPromotePoll} {
		if got := w.advance(t, w.t0.Add(at)); got != switchoverWaiting {
			t.Fatalf("progress while %s is down = %v, want waiting", old, got)
		}
	}
	// in recovery 로 답해도 컨테이너 재시작 수가 그대로면 아직 돌아온 것이 아니다.
	w.exec.set(old, simStandby, w.exec.member(old).lsn)
	if got := w.advance(t, w.t0.Add(3*pgPromotePoll)); got != switchoverWaiting {
		t.Fatalf("progress with %s in recovery but not restarted = %v, want waiting", old, got)
	}
	if n := w.log.count("exec promote "); n != 0 {
		t.Fatalf("promotion before %s is back in recovery: %q", old, w.log.lines)
	}

	// 제자리 재기동 — standby 로 돌아온 뒤 t 승격.
	const back = 6 * time.Second
	w.restart(t, old)
	if got := w.advance(t, w.t0.Add(back)); got != switchoverPromoted {
		t.Fatalf("progress after %s came back = %v, want promoted: %q", old, got, w.log.lines)
	}
	if w.log.count("exec promote "+target+" epoch=2") != 1 || w.log.count("exec promote ") != 1 {
		t.Fatalf("want exactly one promotion of %s at epoch 2, got %q", target, w.log.lines)
	}
	cm := runtimeCM(t, w.c, w.name)
	if cm[runtimeKeyPrimaryEndpoint] != podDNS(w.name, target) || cm[runtimeKeyMemberHold] != "" {
		t.Fatalf("runtime CM = %v, want %s published with no hold", cm, target)
	}
	w.assertEnded(t)
	gap := testutil.ToFloat64(MetricSwitchoverDuration.WithLabelValues(runtimeTestNS, w.name, "shard-0"))
	if gap < (back-time.Second).Seconds() || gap > (back+time.Second).Seconds() {
		t.Fatalf("switchover_duration_seconds = %v, want about %v", gap, back.Seconds())
	}
	if n := w.eventCount(corev1.EventTypeNormal, switchoverCompletedReason); n != 1 {
		t.Fatalf("%s events = %d, want 1: %q", switchoverCompletedReason, n, w.events)
	}
	if got := testutil.ToFloat64(switched) - before; got != 1 {
		t.Fatalf("promotions_total{kind=switchover} delta = %v, want 1", got)
	}
	if n := memberDeletes(w.log); n != 0 {
		t.Fatalf("member deletes during a switchover = %d, want 0: %q", n, w.log.lines)
	}
}

// TestSwitchoverWinner 는 승자 규칙을 본다(§3.16-5) — t 가 옛 primary 끝보다 모자라고 u 가 끝을 가졌으면 u 를
// 승격하고 SwitchoverTargetBehind 를 남긴다. 둘 다 모자라면 옛 primary 를 다시 승격한다(데이터를 전부 가졌다).
func TestSwitchoverWinner(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		target, other uint64 // 옛 primary 끝에서 모자란 바이트
		winner        int
	}{
		{name: "other standby holds the end", target: behindBytes, winner: 2},
		{name: "only the old primary holds the end", target: behindBytes, other: behindBytes, winner: 0},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newSwitchoverWorld(t, fmt.Sprintf("so-winner-%d", i))
			old, target, winner := w.member(0), w.member(1), w.member(tc.winner)
			w.annotate(t, target)
			w.handle(t)
			w.advance(t, w.t0)
			w.restart(t, old)
			end := w.exec.member(old).lsn
			w.exec.set(target, simStandby, end-tc.target)
			w.exec.set(w.member(2), simStandby, end-tc.other)

			if got := w.advance(t, w.t0.Add(statusPollInterval)); got != switchoverPromoted {
				t.Fatalf("progress = %v, want promoted: %q", got, w.log.lines)
			}
			if w.log.count("exec promote "+winner+" ") != 1 || w.log.count("exec promote ") != 1 {
				t.Fatalf("want exactly one promotion of %s, got %q", winner, w.log.lines)
			}
			if got := runtimeCM(t, w.c, w.name)[runtimeKeyPrimaryEndpoint]; got != podDNS(w.name, winner) {
				t.Fatalf("PRIMARY_ENDPOINT = %q, want %s", got, winner)
			}
			if n := w.eventCount(corev1.EventTypeWarning, switchoverTargetBehindReason); n != 1 {
				t.Fatalf("%s events = %d, want 1: %q", switchoverTargetBehindReason, n, w.events)
			}
			w.assertEnded(t)
		})
	}
}

// TestSwitchoverUndoesStop 는 정지 exec 가 STOP_REQUESTED 전에 실패하고 옛 primary 가 아직 실제 primary
// 면 standby.signal 을 지우고 승격 마커를 CR epoch 로 되쓰고 SwitchoverAborted 로 끝내는지 본다(§3.16-3) — 승격 0.
func TestSwitchoverUndoesStop(t *testing.T) {
	t.Parallel()
	w := newSwitchoverWorld(t, "so-stop-fails")
	old := w.member(0)
	w.exec.stop = stopFailEarly
	w.annotate(t, w.member(1))
	w.handle(t)

	if got := w.advance(t, w.t0); got != switchoverIdle {
		t.Fatalf("progress after a failed stop = %v, want idle (aborted): %q", got, w.log.lines)
	}
	stop, restore := w.log.first("exec stop "+old), w.log.first("exec restore "+old+" epoch=1")
	if stop < 0 || restore <= stop {
		t.Fatalf("want the stop exec then a restore of %s at CR epoch 1, got %q", old, w.log.lines)
	}
	script := strings.Join(w.exec.script("restore", old), "\n")
	for _, want := range []string{`rm -f "$DATA/standby.signal"`, promotedPrimaryMarker} {
		if !strings.Contains(script, want) {
			t.Fatalf("restore script lacks %q:\n%s", want, script)
		}
	}
	if m := w.exec.member(old); m.role != simPrimary || m.marker != "1" {
		t.Fatalf("%s after the restore = %+v, want primary with marker 1", old, m)
	}
	if n := w.log.count("exec promote "); n != 0 {
		t.Fatalf("promotions after a failed stop = %d, want 0: %q", n, w.log.lines)
	}
	if n := w.eventCount(corev1.EventTypeWarning, switchoverAbortedReason); n != 1 {
		t.Fatalf("%s events = %d, want 1: %q", switchoverAbortedReason, n, w.events)
	}
	w.assertEnded(t)
}

// TestSwitchoverOldNotBack 는 옛 primary 가 switchoverRestartWait 안에 standby 로 돌아오지 않으면
// SwitchoverAborted(OldPrimaryNotBack)로 끝내고 다음 reconcile 이 보류한 failover 를 정확히 한 번 하는지 본다 —
// 후보는 수신 최대이고 발행과 옛 primary(Demoted) hold 는 CM 쓰기 한 번이다(§3.16-4). 전체 Reconcile 이다.
func TestSwitchoverOldNotBack(t *testing.T) {
	t.Parallel()
	const name = "so-not-back"
	now := time.Now()
	w := newSwitchoverWorld(t, name, withSwitchover(stoppedState(name,
		now.Add(-switchoverRestartWait-5*time.Second), now.Add(-switchoverRestartWait-time.Second))))
	old, target := w.member(0), w.member(1)
	w.down(t, old)
	w.stoppedMembers(simDown)
	w.position(t, target, switchoverBaseLSN+shutdownRecordBytes)
	key := runtimeTestNS + "/" + w.name
	w.r.failoverPending = map[string]time.Time{key: now.Add(-switchoverRestartWait - time.Second)}

	// 첫 reconcile — 창은 문턱을 넘었지만 switchover 가 돌아 실행 보류 · 옛 primary 미복귀로 중단.
	w.reconcile(t)
	if n := w.log.count("exec promote "); n != 0 {
		t.Fatalf("promotions while the switchover runs = %d, want 0: %q", n, w.log.lines)
	}
	if _, found := w.state(t); found {
		t.Fatalf("switchover state kept after switchoverRestartWait")
	}
	aborted := slices.ContainsFunc(w.events, func(ev string) bool {
		return strings.HasPrefix(ev, corev1.EventTypeWarning+" "+switchoverAbortedReason+" ") &&
			strings.Contains(ev, string(abortOldPrimaryNotBack))
	})
	if !aborted {
		t.Fatalf("no %s(%s) event: %q", switchoverAbortedReason, abortOldPrimaryNotBack, w.events)
	}

	// 다음 reconcile — 누적된 창으로 정상 failover 정확히 한 번.
	w.reconcile(t)
	if w.log.count("exec promote "+target+" ") != 1 || w.log.count("exec promote ") != 1 {
		t.Fatalf("want exactly one failover promotion of %s, got %q", target, w.log.lines)
	}
	var published []map[string]string
	for _, data := range w.cms.all() {
		if data[runtimeKeyPrimaryEndpoint] == podDNS(w.name, target) {
			published = append(published, data)
		}
	}
	if len(published) == 0 || published[0][runtimeKeyMemberHold] != old {
		t.Fatalf("the CM write publishing %s must also hold %s, got %v", target, old, w.cms.all())
	}
	assertRebuilds(t, w.c, w.cluster(t), map[string]rebuildReason{old: rebuildDemoted})
}

// TestSwitchoverDefersFailover 는 switchover 가 도는 동안(< switchoverBudget) primary 가 40 s 없어도 자동 승격
// exec 가 0 이고 failoverPending 창은 첫 관측부터 누적되며, switchover 가 끝나면 다음 판정이 정확히 한 번 승격하는지
// 본다(§3.3 — 실행만 보류).
func TestSwitchoverDefersFailover(t *testing.T) {
	t.Parallel()
	const absent = 40 * time.Second
	now := time.Now()
	w := newSwitchoverWorld(t, "so-withhold", withSwitchover(switchoverState{
		Shard: "shard-0", Reason: switchoverRollout, Phase: switchoverStopping, Since: now, StopAt: now,
	}))
	old, target, other := w.member(0), w.member(1), w.member(2)
	w.down(t, old)
	shards := []postgresv1alpha1.ShardStatus{{
		Name:    "shard-0",
		Primary: &postgresv1alpha1.ShardEndpoint{Pod: old, Endpoint: podDNS(w.name, old)},
		Replicas: []postgresv1alpha1.ShardEndpoint{
			{Pod: target, Endpoint: podDNS(w.name, target), Ready: true},
			{Pod: other, Endpoint: podDNS(w.name, other), Ready: true},
		},
	}}
	ctx := context.Background()
	key := runtimeTestNS + "/" + w.name

	cluster := w.cluster(t)
	for at := time.Duration(0); at <= absent; at += statusPollInterval {
		if _, promoted := w.r.reconcileFailover(ctx, cluster, shards, postgresv1alpha1.ClusterPhaseReady, now.Add(at)); promoted {
			t.Fatalf("failover promoted %s into a running switchover", at)
		}
	}
	if n := w.log.count("exec promote "); n != 0 {
		t.Fatalf("promotions while the switchover runs = %d, want 0: %q", n, w.log.lines)
	}
	w.r.failoverPendingMu.Lock()
	since, pending := w.r.failoverPending[key]
	w.r.failoverPendingMu.Unlock()
	if !pending || !since.Equal(now) {
		t.Fatalf("failoverPending[%s] = %v (%v), want the first observation %v kept", key, since, pending, now)
	}
	if switchoverActive(cluster, "shard-0", now.Add(switchoverBudget+time.Second)) {
		t.Fatalf("switchover still withholds failover past switchoverBudget")
	}

	// switchover 가 끝났다 — 누적된 창으로 곧바로 한 번.
	cluster = w.cluster(t)
	before := cluster.DeepCopy()
	delete(cluster.Annotations, switchoverStateAnnotation)
	if err := w.c.Patch(ctx, cluster, client.MergeFrom(before)); err != nil {
		t.Fatalf("end switchover: %v", err)
	}
	cluster = w.cluster(t)
	if _, promoted := w.r.reconcileFailover(ctx, cluster, shards, postgresv1alpha1.ClusterPhaseDegraded, now.Add(absent+statusPollInterval)); !promoted {
		t.Fatalf("no failover after the switchover ended: %q", w.log.lines)
	}
	if n := w.log.count("exec promote "); n != 1 {
		t.Fatalf("promotions after the switchover ended = %d, want 1: %q", n, w.log.lines)
	}
}

// TestSwitchoverEndsReconcile 는 switchover 승격을 한 reconcile 이 거기서 끝나는지 본다 — 디바운스 창이
// 이미 문턱을 넘었어도 failover 승격 exec 는 0 이고, 승격은 정확히 한 번 · CM 발행 뒤 라벨·Service·삭제 쓰기 0 ·
// requeue · failoverPending 삭제(§3.3). 전체 Reconcile 이다.
func TestSwitchoverEndsReconcile(t *testing.T) {
	t.Parallel()
	const name = "so-ends"
	now := time.Now()
	w := newSwitchoverWorld(t, name, withSwitchover(stoppedState(name, now.Add(-2*statusPollInterval), now.Add(-statusPollInterval))))
	old, target := w.member(0), w.member(1)
	w.stoppedMembers(simStandby)
	w.restart(t, old)
	key := runtimeTestNS + "/" + w.name
	w.r.failoverPending = map[string]time.Time{key: now.Add(-failoverDebounceThreshold - time.Second)}
	switched := MetricPromotions.WithLabelValues(runtimeTestNS, w.name, "shard-0", string(promotionSwitchover))
	before := testutil.ToFloat64(switched)

	res := w.reconcile(t)

	if w.log.count("exec promote "+target+" ") != 1 || w.log.count("exec promote ") != 1 {
		t.Fatalf("want exactly one promotion of %s, got %q", target, w.log.lines)
	}
	if got := testutil.ToFloat64(switched) - before; got != 1 {
		t.Fatalf("promotions_total{kind=switchover} delta = %v, want 1", got)
	}
	publish := w.log.first("patch cm/" + ShardRuntimeConfigMapName(w.name, "shard-0"))
	if publish < 0 {
		t.Fatalf("no runtime CM publish: %q", w.log.lines)
	}
	for _, line := range w.log.lines[publish+1:] {
		for _, write := range []string{"patch *v1.Pod/", "create *v1.Service/", "update *v1.Service/", "delete "} {
			if strings.HasPrefix(line, write) {
				t.Errorf("write after the switchover promotion %q — the next reconcile does that: %q", line, w.log.lines[publish:])
			}
		}
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("result = %+v, want a requeue", res)
	}
	w.r.failoverPendingMu.Lock()
	defer w.r.failoverPendingMu.Unlock()
	if since, ok := w.r.failoverPending[key]; ok {
		t.Errorf("failoverPending[%s] = %s kept after the switchover promotion, want deleted", key, since)
	}
}

// TestSwitchoverResumes 는 새 reconciler(메모리 0)가 CR 애노테이션에서 잇는지 본다(§3.16 재기동 이어받기).
func TestSwitchoverResumes(t *testing.T) {
	t.Parallel()
	t.Run("preparing starts over", func(t *testing.T) {
		t.Parallel()
		const name = "so-resume-prep"
		now := time.Now()
		w := newSwitchoverWorld(t, name, withSwitchover(switchoverState{
			Shard: "shard-0", Target: graphMember(name, 1), Reason: switchoverRequested, Phase: switchoverPreparing, Since: now,
		}))
		w.fresh()
		if got := w.advance(t, now.Add(pgPromotePoll)); got != switchoverWaiting {
			t.Fatalf("progress = %v, want waiting after the stop", got)
		}
		if w.log.first("exec checkpoint "+w.member(0)) < 0 || w.log.first("exec stop "+w.member(0)) < 0 {
			t.Fatalf("Preparing did not start over with CHECKPOINT and the stop: %q", w.log.lines)
		}
		if state := w.mustState(t); state.Phase != switchoverStopping {
			t.Fatalf("phase = %s, want %s", state.Phase, switchoverStopping)
		}
	})
	t.Run("stopping with the old primary still primary aborts", func(t *testing.T) {
		t.Parallel()
		const name = "so-resume-stop"
		now := time.Now()
		w := newSwitchoverWorld(t, name, withSwitchover(stoppedState(name, now, now)))
		w.fresh()
		if got := w.advance(t, now.Add(pgPromotePoll)); got != switchoverIdle {
			t.Fatalf("progress = %v, want idle (aborted): %q", got, w.log.lines)
		}
		if w.log.first("exec restore "+w.member(0)+" epoch=1") < 0 || w.log.count("exec promote ") != 0 {
			t.Fatalf("want a restore of %s and no promotion, got %q", w.member(0), w.log.lines)
		}
		if n := w.eventCount(corev1.EventTypeWarning, switchoverAbortedReason); n != 1 {
			t.Fatalf("%s events = %d, want 1", switchoverAbortedReason, n)
		}
		w.assertEnded(t)
	})
	t.Run("stopping with the old primary in recovery promotes", func(t *testing.T) {
		t.Parallel()
		const name = "so-resume-back"
		now := time.Now()
		w := newSwitchoverWorld(t, name, withSwitchover(stoppedState(name, now, now)))
		w.stoppedMembers(simStandby)
		w.restart(t, w.member(0))
		w.fresh()
		if got := w.advance(t, now.Add(statusPollInterval)); got != switchoverPromoted {
			t.Fatalf("progress = %v, want promoted: %q", got, w.log.lines)
		}
		if w.log.count("exec promote "+w.member(1)+" ") != 1 || w.log.count("exec promote ") != 1 {
			t.Fatalf("want exactly one promotion of %s, got %q", w.member(1), w.log.lines)
		}
		w.assertEnded(t)
	})
	t.Run("past the budget aborts", func(t *testing.T) {
		t.Parallel()
		const name = "so-resume-expired"
		now := time.Now()
		w := newSwitchoverWorld(t, name, withSwitchover(stoppedState(name, now.Add(-switchoverBudget-time.Second), now)))
		w.fresh()
		if got := w.advance(t, now); got != switchoverIdle {
			t.Fatalf("progress = %v, want idle (aborted)", got)
		}
		if w.log.first("exec restore "+w.member(0)+" epoch=1") < 0 || w.log.count("exec promote ") != 0 {
			t.Fatalf("want the standing old primary restored and no promotion, got %q", w.log.lines)
		}
		expired := slices.ContainsFunc(w.events, func(ev string) bool {
			return strings.HasPrefix(ev, corev1.EventTypeWarning+" "+switchoverAbortedReason+" ") &&
				strings.Contains(ev, string(abortBudgetExceeded))
		})
		if !expired || switchoverActive(w.cluster(t), "shard-0", now) {
			t.Fatalf("want %s(%s) and no active switchover, events %q", switchoverAbortedReason, abortBudgetExceeded, w.events)
		}
		w.assertEnded(t)
	})
	t.Run("promoting adopts the promoted primary", func(t *testing.T) {
		t.Parallel()
		const name = "so-resume-adopt"
		now := time.Now()
		promoting := stoppedState(name, now, now)
		promoting.Phase = switchoverPromoting
		w := newSwitchoverWorld(t, name, withSwitchover(promoting), func(c *postgresv1alpha1.PostgresCluster) {
			withCanon(c, "shard-0", shardCanon{Epoch: 2, Initialized: true})
		})
		old, target := w.member(0), w.member(1)
		w.stoppedMembers(simStandby)
		w.restart(t, old)
		w.exec.set(target, simPrimary, switchoverBaseLSN+shutdownRecordBytes)
		w.exec.members[target].marker = "2"
		w.report(t, target, statusapi.RolePrimary, corev1.ConditionTrue, 0)
		w.fresh()

		// Reconcile 순서 — 채택이 switchover 걸음보다 먼저다.
		cluster := w.cluster(t)
		if err := w.r.adoptPrimary(context.Background(), cluster, ordinalShardRef(cluster, 0)); err != nil {
			t.Fatalf("adoptPrimary: %v", err)
		}
		if got := w.advance(t, now.Add(statusPollInterval)); got != switchoverPromoted {
			t.Fatalf("progress = %v, want promoted (completed): %q", got, w.log.lines)
		}
		if n := w.log.count("exec promote "); n != 0 {
			t.Fatalf("promotion execs = %d, want 0 — the adopted primary is the result: %q", n, w.log.lines)
		}
		if got := runtimeCM(t, w.c, name)[runtimeKeyPrimaryEndpoint]; got != podDNS(name, target) {
			t.Fatalf("PRIMARY_ENDPOINT = %q, want the adopted %s", got, target)
		}
		if n := w.eventCount(corev1.EventTypeNormal, switchoverCompletedReason); n != 1 {
			t.Fatalf("%s events = %d, want 1", switchoverCompletedReason, n)
		}
		w.assertEnded(t)
	})
}

// TestSwitchoverTargetBehind 는 애노테이션 대상이 Preparing 에서 streaming 중 수신 최대가 아니면
// SwitchoverTargetBehind 로 중단하는지 본다 — CHECKPOINT·정지 exec 0 · 상태·애노테이션 삭제.
func TestSwitchoverTargetBehind(t *testing.T) {
	t.Parallel()
	w := newSwitchoverWorld(t, "so-behind")
	target := w.member(1)
	w.position(t, w.member(2), switchoverBaseLSN+behindBytes)
	w.annotate(t, target)
	w.handle(t)

	if got := w.advance(t, w.t0); got != switchoverIdle {
		t.Fatalf("progress = %v, want idle (aborted): %q", got, w.log.lines)
	}
	if n := w.log.count("exec checkpoint ") + w.log.count("exec stop "); n != 0 {
		t.Fatalf("CHECKPOINT or stop execs = %d, want 0: %q", n, w.log.lines)
	}
	if n := w.eventCount(corev1.EventTypeWarning, switchoverTargetBehindReason); n != 1 {
		t.Fatalf("%s events = %d, want 1: %q", switchoverTargetBehindReason, n, w.events)
	}
	w.assertEnded(t)
}

// TestSwitchoverCheckpointFirst 는 CHECKPOINT 가 공백 시계 밖인지 본다 — CHECKPOINT exec 가 Stopping 기록보다
// 먼저이고 stopAt 은 CHECKPOINT 가 걸린 시간 뒤다(§3.16-1·2).
func TestSwitchoverCheckpointFirst(t *testing.T) {
	t.Parallel()
	w := newSwitchoverWorld(t, "so-checkpoint")
	old := w.member(0)
	w.exec.checkpoint = checkpointTook
	w.annotate(t, w.member(1))
	w.handle(t)

	w.advance(t, w.t0)

	checkpoint, stopping := w.log.first("exec checkpoint "+old), w.log.first("state "+string(switchoverStopping))
	if checkpoint < 0 || stopping <= checkpoint {
		t.Fatalf("CHECKPOINT must come before the Stopping record, got %q", w.log.lines)
	}
	if state := w.mustState(t); state.StopAt.Sub(w.t0) < checkpointTook {
		t.Fatalf("stopAt = t0%+v, want at least %s after t0 (CHECKPOINT is outside the gap)", state.StopAt.Sub(w.t0), checkpointTook)
	}
}
