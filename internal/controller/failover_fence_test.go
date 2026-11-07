/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
	"github.com/keiailab/postgres-operator/internal/loopdrill"
)

// 본 파일은 failover 의 펜스·수신 끊기·승자·재지정(ADR-0036)을 본다 — S3(매니저만 얼고 postgres 는 도는
// 옛 primary)에서 생존 standby 가 분기점을 넘지 않아야 한다.
//
//	0-0 옛 primary(NotReady) ── fence ──▶ 정지
//	0-1 판정 후보 ─┐ cut ─▶ 끝 위치 재기 ─▶ 승자 승격 ─▶ 나머지 재지정
//	0-2 생존자 ────┘

// fenceWorld 는 멤버 셋의 샤드 0 이다 — CM primary 0-0 은 PodReady False, 0-1·0-2 는 in recovery.
type fenceWorld struct {
	name    string
	old     string
	cand    string
	other   string
	cluster *postgresv1alpha1.PostgresCluster
	c       client.Client
	r       *PostgresClusterReconciler
	exec    *fakePromotionPodExecutor
	rec     *events.FakeRecorder
}

// fenceSetup 은 세계를 짓기 전에 파드·PVC 를 바꾼다.
type fenceSetup func(pods map[string]*corev1.Pod, pvcs map[string]*corev1.PersistentVolumeClaim)

func newFenceWorld(t *testing.T, name string, setup fenceSetup) *fenceWorld {
	t.Helper()
	cluster, cm := runtimeCluster(name)
	w := &fenceWorld{name: name, cluster: cluster}
	pods := map[string]*corev1.Pod{}
	pvcs := map[string]*corev1.PersistentVolumeClaim{}
	for i, role := range []statusapi.Role{statusapi.RolePrimary, statusapi.RoleReplica, statusapi.RoleReplica} {
		pod := shardMember(name, i, role)
		pods[pod.Name] = pod
		pvcs[pod.Name] = memberPVC(pod.Name, false)
	}
	w.old, w.cand, w.other = graphMember(name, 0), graphMember(name, 1), graphMember(name, 2)
	pods[w.old].Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	if setup != nil {
		setup(pods, pvcs)
	}

	objs := []client.Object{cluster, cm}
	for _, pod := range pods {
		objs = append(objs, pod)
	}
	for _, pvc := range pvcs {
		objs = append(objs, pvc)
	}
	scheme := runtimeScheme(t)
	w.c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).Build()
	w.exec = &fakePromotionPodExecutor{
		recovery: map[string]string{w.cand: pgBoolTrue, w.other: pgBoolTrue},
		lsn:      map[string]string{w.cand: "0/3000200", w.other: "0/3000100"},
		fence:    fenceResultPrefix + string(fenceStopped) + "\n",
		cutFail:  map[string]bool{},
	}
	w.rec = events.NewFakeRecorder(wideEventBuffer)
	w.r = &PostgresClusterReconciler{Client: w.c, Scheme: scheme, PromotionPodExecutor: w.exec, Recorder: w.rec}
	t.Cleanup(func() { DeleteMetricsFor(runtimeTestNS, name) })
	return w
}

func (w *fenceWorld) decision() failover.Decision {
	return failover.Decision{
		Failed: true,
		Reason: failover.ReasonPrimaryNotReady,
		PromotionCandidate: &postgresv1alpha1.ShardEndpoint{
			Pod: w.cand, Endpoint: podDNS(w.name, w.cand), Ready: true,
		},
	}
}

func (w *fenceWorld) promote() error {
	return w.r.executeClusterPromotion(context.Background(), w.cluster, "shard-0", w.decision())
}

// events 는 쌓인 이벤트를 비우며 돌려준다.
func (w *fenceWorld) events() []string {
	var out []string
	for {
		select {
		case e := <-w.rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// fenceCount 는 failover_fence_total 의 outcome 값이다.
func (w *fenceWorld) fenceCount(outcome fenceOutcome) float64 {
	return testutil.ToFloat64(MetricFailoverFence.WithLabelValues(runtimeTestNS, w.name, "shard-0", string(outcome)))
}

// publishedPrimary 는 런타임 CM 의 PRIMARY_ENDPOINT 다.
func (w *fenceWorld) publishedPrimary(t *testing.T) string {
	t.Helper()
	rt, _, err := w.r.readRuntime(context.Background(), w.cluster, "shard-0")
	if err != nil {
		t.Fatalf("read runtime: %v", err)
	}
	return rt.PrimaryEndpoint
}

// follows 는 pod 의 마지막 재지정이 to 의 호스트를 가리키는지다.
func (w *fenceWorld) follows(pod, to string) bool {
	w.exec.mu.Lock()
	defer w.exec.mu.Unlock()
	return strings.Contains(w.exec.conninfo[pod], "host="+primaryEndpointHost(podDNS(w.name, to))+" ")
}

// retargetRecord 는 그래프가 다시 하지 않도록 남긴 재지정 기록이다.
func (w *fenceWorld) retargetRecord(pod string) (retargetAttempt, bool) {
	w.r.graphMu.Lock()
	defer w.r.graphMu.Unlock()
	got, ok := w.r.lastRetarget[runtimeTestNS+"/"+w.name][pod]
	return got, ok
}

// assertLeftCut 은 승격하지 않은 뒤 생존자가 끊긴 채 남았는지 본다 — CM primary 로 되돌리면 S3 분기가 다시 생긴다.
func (w *fenceWorld) assertLeftCut(t *testing.T) []string {
	t.Helper()
	calls := w.exec.callLog()
	for _, pod := range []string{w.cand, w.other} {
		if indexOf(calls, "cut "+pod) < 0 || indexOf(calls, "retarget "+pod) >= 0 {
			t.Errorf("calls = %v, want %s cut and not retargeted", calls, pod)
		}
	}
	events := w.events()
	if !hasEvent(events, "Warning "+failoverWinnerUnavailable) {
		t.Errorf("events = %v, want %s", events, failoverWinnerUnavailable)
	}
	return events
}

func indexOf(calls []string, call string) int {
	return slices.Index(calls, call)
}

func hasEvent(events []string, prefix string) bool {
	return slices.ContainsFunc(events, func(e string) bool { return strings.HasPrefix(e, prefix) })
}

// TestFailoverFenceCutOrder 는 펜스 → 끊기 → 재기 → 승격 → 재지정 순서와 그 기록을 본다(ADR-0036 F1–F5).
func TestFailoverFenceCutOrder(t *testing.T) {
	t.Parallel()
	w := newFenceWorld(t, "fence-order", nil)

	if err := w.promote(); err != nil {
		t.Fatalf("executeClusterPromotion: %v", err)
	}

	calls := w.exec.callLog()
	fence := indexOf(calls, "fence "+w.old)
	promote := indexOf(calls, "promote "+w.cand)
	retarget := indexOf(calls, "retarget "+w.other)
	if fence < 0 || promote < 0 || retarget < 0 {
		t.Fatalf("calls = %v, want fence %s, promote %s, retarget %s", calls, w.old, w.cand, w.other)
	}
	for _, pod := range []string{w.cand, w.other} {
		cut, lsn := indexOf(calls, "cut "+pod), indexOf(calls, "lsn "+pod)
		if cut < fence || lsn < cut || promote < lsn {
			t.Errorf("calls = %v, want fence < cut %s < lsn %s < promote", calls, pod, pod)
		}
	}
	if promote > retarget {
		t.Errorf("calls = %v, want promote before retarget", calls)
	}
	if indexOf(calls, "cut "+w.old) >= 0 {
		t.Errorf("calls = %v, old primary must not be cut", calls)
	}
	if indexOf(calls, "retarget "+w.cand) >= 0 {
		t.Errorf("calls = %v, the winner must not be retargeted", calls)
	}

	if !w.follows(w.other, w.cand) {
		t.Errorf("%s conninfo = %q, want winner %s", w.other, w.exec.conninfo[w.other], w.cand)
	}
	got, ok := w.retargetRecord(w.other)
	if !ok || got.target != podDNS(w.name, w.cand) || !got.done {
		t.Errorf("retarget record = %+v (%t), want done to %s", got, ok, podDNS(w.name, w.cand))
	}
	if got := w.publishedPrimary(t); got != podDNS(w.name, w.cand) {
		t.Errorf("published primary = %q, want %q", got, podDNS(w.name, w.cand))
	}
	events := w.events()
	if !hasEvent(events, "Normal "+primaryFencedReason) || !hasEvent(events, "Normal "+memberRetargetedReason) {
		t.Errorf("events = %v, want %s and %s", events, primaryFencedReason, memberRetargetedReason)
	}
	if got := w.fenceCount(fenceStopped); got != 1 {
		t.Errorf("failover_fence_total{outcome=Stopped} = %v, want 1", got)
	}
}

// TestFailoverWinnerAhead 는 판정 후보보다 더 받은 생존자가 승격되고 후보가 그 승자로 재지정되는지 본다.
func TestFailoverWinnerAhead(t *testing.T) {
	t.Parallel()
	w := newFenceWorld(t, "fence-ahead", nil)
	w.exec.lsn[w.other] = "0/3000300"

	if err := w.promote(); err != nil {
		t.Fatalf("executeClusterPromotion: %v", err)
	}

	calls := w.exec.callLog()
	if indexOf(calls, "promote "+w.other) < 0 || indexOf(calls, "promote "+w.cand) >= 0 {
		t.Fatalf("calls = %v, want promote %s only", calls, w.other)
	}
	if got := w.publishedPrimary(t); got != podDNS(w.name, w.other) {
		t.Errorf("published primary = %q, want %q", got, podDNS(w.name, w.other))
	}
	if !w.follows(w.cand, w.other) {
		t.Errorf("%s conninfo = %q, want winner %s", w.cand, w.exec.conninfo[w.cand], w.other)
	}
	if got, ok := w.retargetRecord(w.cand); !ok || got.target != podDNS(w.name, w.other) || !got.done {
		t.Errorf("retarget record = %+v (%t), want done to %s", got, ok, podDNS(w.name, w.other))
	}
	if events := w.events(); !hasEvent(events, "Normal "+failoverWinnerChangedReason) {
		t.Errorf("events = %v, want %s", events, failoverWinnerChangedReason)
	}
}

// TestFailoverTieKeepsCandidate 는 끝 위치 동률이면 판정 후보가 이기는지 본다.
func TestFailoverTieKeepsCandidate(t *testing.T) {
	t.Parallel()
	w := newFenceWorld(t, "fence-tie", nil)
	w.exec.lsn[w.other] = w.exec.lsn[w.cand]

	if err := w.promote(); err != nil {
		t.Fatalf("executeClusterPromotion: %v", err)
	}
	if calls := w.exec.callLog(); indexOf(calls, "promote "+w.cand) < 0 {
		t.Fatalf("calls = %v, want promote %s", calls, w.cand)
	}
	if events := w.events(); hasEvent(events, "Normal "+failoverWinnerChangedReason) {
		t.Errorf("events = %v, a tie must not change the winner", events)
	}
}

// TestFailoverUnreachableFenceCuts 는 펜스를 증명하지 못해도 끊기·승격이 도는지 본다 — 생존자 보호는 끊기가 한다.
func TestFailoverUnreachableFenceCuts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		setup     fenceSetup
		fenceErr  error
		wantFence bool
	}{
		{"pod-not-running", func(pods map[string]*corev1.Pod, _ map[string]*corev1.PersistentVolumeClaim) {
			pods["fence-unr-pod-shard-0-0"].Status.Phase = corev1.PodUnknown
		}, nil, false},
		{"exec-error", nil, errors.New("exec: kubelet unreachable"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name := "fence-unr-" + strings.Split(tc.name, "-")[0]
			w := newFenceWorld(t, name, tc.setup)
			w.exec.fenceErr = tc.fenceErr

			if err := w.promote(); err != nil {
				t.Fatalf("executeClusterPromotion: %v", err)
			}

			calls := w.exec.callLog()
			if got := indexOf(calls, "fence "+w.old) >= 0; got != tc.wantFence {
				t.Errorf("calls = %v, fence exec = %t, want %t", calls, got, tc.wantFence)
			}
			if indexOf(calls, "cut "+w.cand) < 0 || indexOf(calls, "cut "+w.other) < 0 || indexOf(calls, "promote "+w.cand) < 0 {
				t.Errorf("calls = %v, want both survivors cut and %s promoted", calls, w.cand)
			}
			if events := w.events(); !hasEvent(events, "Warning "+primaryFenceUnprovenReason) {
				t.Errorf("events = %v, want %s", events, primaryFenceUnprovenReason)
			}
			if got := w.fenceCount(fenceUnreachable); got != 1 {
				t.Errorf("failover_fence_total{outcome=Unreachable} = %v, want 1", got)
			}
		})
	}
}

// TestFailoverAbsentPrimaryDown 는 옛 primary 파드가 없으면 exec 없이 Down 인지 본다.
func TestFailoverAbsentPrimaryDown(t *testing.T) {
	t.Parallel()
	w := newFenceWorld(t, "fence-down", func(pods map[string]*corev1.Pod, _ map[string]*corev1.PersistentVolumeClaim) {
		delete(pods, "fence-down-shard-0-0")
	})

	if err := w.promote(); err != nil {
		t.Fatalf("executeClusterPromotion: %v", err)
	}
	if calls := w.exec.callLog(); indexOf(calls, "fence "+w.old) >= 0 {
		t.Errorf("calls = %v, an absent pod must not be exec'd", calls)
	}
	if got := w.fenceCount(fenceDown); got != 1 {
		t.Errorf("failover_fence_total{outcome=Down} = %v, want 1", got)
	}
}

// TestFailoverCutFailureExcluded 는 끊지 못한 생존자가 더 앞서도 승자 후보에서 빠지는지 본다.
func TestFailoverCutFailureExcluded(t *testing.T) {
	t.Parallel()
	w := newFenceWorld(t, "fence-cutfail", nil)
	w.exec.lsn[w.other] = "0/3000300"
	w.exec.cutFail[w.other] = true

	if err := w.promote(); err != nil {
		t.Fatalf("executeClusterPromotion: %v", err)
	}
	calls := w.exec.callLog()
	if indexOf(calls, "promote "+w.cand) < 0 || indexOf(calls, "lsn "+w.other) >= 0 {
		t.Errorf("calls = %v, want %s promoted and %s not measured", calls, w.cand, w.other)
	}
	if events := w.events(); !hasEvent(events, "Warning "+standbyCutFailedReason) {
		t.Errorf("events = %v, want %s", events, standbyCutFailedReason)
	}
}

// TestFailoverNoMeasurableSurvivor 는 잰 생존자가 없으면 승격하지 않고 끊긴 채 두며 창을 남기는지 본다.
func TestFailoverNoMeasurableSurvivor(t *testing.T) {
	t.Parallel()
	w := newFenceWorld(t, "fence-nolsn", nil)
	w.exec.lsn = map[string]string{}
	key := runtimeTestNS + "/" + w.name
	w.r.failoverPending = map[string]time.Time{key: time.Now().Add(-failoverDebounceThreshold - time.Second)}
	shards := []postgresv1alpha1.ShardStatus{{
		Name:    "shard-0",
		Primary: &postgresv1alpha1.ShardEndpoint{Pod: w.old, Endpoint: podDNS(w.name, w.old), Ready: false},
		Replicas: []postgresv1alpha1.ShardEndpoint{
			{Pod: w.cand, Endpoint: podDNS(w.name, w.cand), Ready: true},
			{Pod: w.other, Endpoint: podDNS(w.name, w.other), Ready: true},
		},
	}}

	_, promoted := w.r.reconcileFailover(context.Background(), w.cluster, shards, postgresv1alpha1.ClusterPhaseReady, time.Now())

	if promoted {
		t.Fatal("promoted without a measured survivor")
	}
	if calls := w.exec.callLog(); slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "promote ") }) {
		t.Fatalf("calls = %v, want no promote exec", calls)
	}
	w.assertLeftCut(t)
	w.r.failoverPendingMu.Lock()
	_, kept := w.r.failoverPending[key]
	w.r.failoverPendingMu.Unlock()
	if !kept {
		t.Error("failover window cleared after a refused promotion; the next reconcile must retry at once")
	}
	if got := w.publishedPrimary(t); got != podDNS(w.name, w.old) {
		t.Errorf("published primary = %q, want unchanged %q", got, podDNS(w.name, w.old))
	}
}

// TestFailoverFencedWinnerLeftCut 는 앞선 승자가 #220 가드에 걸리면 승격하지 않고 끊긴 채 두는지 본다.
func TestFailoverFencedWinnerLeftCut(t *testing.T) {
	t.Parallel()
	w := newFenceWorld(t, "fence-guard", func(_ map[string]*corev1.Pod, pvcs map[string]*corev1.PersistentVolumeClaim) {
		pvcs["fence-guard-shard-0-2"] = memberPVC("fence-guard-shard-0-2", true)
	})
	w.exec.lsn[w.other] = "0/3000300"

	err := w.promote()
	if err == nil {
		t.Fatal("promoted a fenced winner while an unfenced member serves")
	}
	if calls := w.exec.callLog(); slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "promote ") }) {
		t.Fatalf("calls = %v, want no promote exec", calls)
	}
	if events := w.assertLeftCut(t); !hasEvent(events, "Normal "+failoverWinnerChangedReason) {
		t.Errorf("events = %v, want %s", events, failoverWinnerChangedReason)
	}
}

// TestFailoverFenceWithinPromotionBound 는 S3 최악(디바운스 + reconcile + 펜스 + 끊기 + 재기)이 J8 경계 안인지 본다.
func TestFailoverFenceWithinPromotionBound(t *testing.T) {
	t.Parallel()
	if fenceExecBudget != fenceFastWait+fenceImmediateWait+statusPollInterval {
		t.Errorf("fenceExecBudget = %s, want fast + immediate + statusPollInterval", fenceExecBudget)
	}
	if cutWait != replicationProbeTimeout {
		t.Errorf("cutWait = %s, want replicationProbeTimeout", cutWait)
	}
	if cutBudget != cutWait+statusPollInterval {
		t.Errorf("cutBudget = %s, want cutWait + statusPollInterval", cutBudget)
	}
	// F1 15 s + F2 10 s(동시) + F3 5 s(동시) + 디바운스 30 s + reconcile 5 s = 65 s ≤ 70 s.
	added := fenceExecBudget + cutBudget + rebuildExecTimeout
	if worst := failoverDebounceThreshold + statusPollInterval + added; worst > loopdrill.PromotionBound {
		t.Errorf("S3 worst = %s > PromotionBound %s", worst, loopdrill.PromotionBound)
	}
}

// fenceScript 는 펜스 스크립트를 가짜 /proc · pg_ctl 로 돌린다.
type fenceScript struct {
	t     *testing.T
	proc  string
	bin   string
	data  string
	pgLog string
}

func newFenceScript(t *testing.T, pgCtl string) *fenceScript {
	t.Helper()
	root := t.TempDir()
	s := &fenceScript{t: t, proc: filepath.Join(root, "proc"), bin: filepath.Join(root, "bin"),
		data: filepath.Join(root, "data"), pgLog: filepath.Join(root, "pg_ctl.log")}
	for _, dir := range []string{s.proc, s.bin, s.data} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub := "#!/bin/sh\necho \"$*\" >> \"" + s.pgLog + "\"\n" + pgCtl + "\n"
	if err := os.WriteFile(filepath.Join(s.bin, "pg_ctl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.data, promotedPrimaryMarker), []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

// process 는 가짜 /proc 항목이다(comm · stat 상태).
func (s *fenceScript) process(pid, comm, state string) {
	s.t.Helper()
	dir := filepath.Join(s.proc, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.t.Fatal(err)
	}
	stat := pid + " (" + comm + ") " + state + " 1 1 1 0 -1\n"
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *fenceScript) run() fenceOutcome {
	s.t.Helper()
	argv := fenceCommand()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "POSTGRES_BIN_DIR="+s.bin, "POSTGRES_DATA_DIR="+s.data, "PROC_DIR="+s.proc)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("fence script: %v\n%s", err, out)
	}
	return parseFenceOutput(out)
}

// exists 는 PGDATA 파일이 있는지다.
func (s *fenceScript) exists(name string) bool {
	_, err := os.Stat(filepath.Join(s.data, name))
	return err == nil
}

// touch 는 PGDATA 파일을 만든다.
func (s *fenceScript) touch(name, body string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.data, name), []byte(body), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// assertFenced 는 펜스 표식(standby.signal 있음 · 승격 마커 없음)과 pending 표식을 본다.
func (s *fenceScript) assertFenced(pending bool) {
	s.t.Helper()
	if !s.exists(standbySignalFile) {
		s.t.Error("standby.signal missing after fence")
	}
	if s.exists(promotedPrimaryMarker) {
		s.t.Error("promoted marker kept after fence")
	}
	if got := s.exists(fencePendingMarker); got != pending {
		s.t.Errorf("fence pending marker = %t, want %t", got, pending)
	}
}

func (s *fenceScript) stops() []string {
	raw, _ := os.ReadFile(s.pgLog)
	var modes []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if _, mode, ok := strings.Cut(line, "-m "); ok {
			modes = append(modes, strings.Fields(mode)[0])
		}
	}
	return modes
}

// TestFenceCommandStages 는 펜스 스크립트의 단계와 결과 분류를 본다 — 좀비는 멈춘 것이고 Stuck 은 pending 표식을 남긴다.
func TestFenceCommandStages(t *testing.T) {
	t.Parallel()
	clear := `rm -rf "$PROC_DIR/100"; exit 0`
	cases := []struct {
		name    string
		pgCtl   string
		procs   [][3]string
		pending bool // 앞선 펜스가 남긴 pending 표식 + standby.signal
		want    fenceOutcome
		stages  []string
	}{
		{"fast-ok", clear, [][3]string{{"100", "postgres", "S"}}, false, fenceStopped, []string{"fast"}},
		{"zombie-left", clear, [][3]string{{"100", "postgres", "S"}, {"101", "postgres", "Z"}}, false, fenceStopped, []string{"fast"}},
		{"not-running", "exit 0", [][3]string{{"100", "sh", "S"}, {"101", "postgres", "Z"}}, false, fenceDown, nil},
		{"immediate-ok", `case "$*" in *"-m immediate"*) rm -rf "$PROC_DIR/100";; esac; exit 1`,
			[][3]string{{"100", "postgres", "S"}}, false, fenceStopped, []string{"fast", "immediate"}},
		{"stuck", "exit 1", [][3]string{{"999999999", "postgres", "T"}}, false, fenceStuck, []string{"fast", "immediate"}},
		{"rerun-after-own-signal", clear, [][3]string{{"100", "postgres", "S"}}, true, fenceStopped, []string{"fast"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newFenceScript(t, tc.pgCtl)
			for _, p := range tc.procs {
				s.process(p[0], p[1], p[2])
			}
			if tc.pending {
				s.touch(fencePendingMarker, "")
				s.touch(standbySignalFile, "")
			}
			if got := s.run(); got != tc.want {
				t.Errorf("outcome = %s, want %s", got, tc.want)
			}
			if got := s.stops(); !slices.Equal(got, tc.stages) {
				t.Errorf("pg_ctl stops = %v, want %v", got, tc.stages)
			}
			s.assertFenced(tc.want == fenceStuck)
		})
	}
}

// TestFenceCommandStandbyNoop 는 이미 standby 로 도는 멤버(펜스가 아닌 standby.signal)는 멈추지 않는지 본다 —
// postmaster.pid 의 pid 가 살아 있다.
func TestFenceCommandStandbyNoop(t *testing.T) {
	t.Parallel()
	s := newFenceScript(t, "exit 0")
	s.process("200", "postgres", "S")
	s.touch("postmaster.pid", "200\n/var/lib/postgresql/data/pgdata\n")
	s.touch(standbySignalFile, "")

	if got := s.run(); got != fenceStandby {
		t.Errorf("outcome = %s, want %s", got, fenceStandby)
	}
	if got := s.stops(); len(got) != 0 {
		t.Errorf("pg_ctl stops = %v, want none", got)
	}
	if !s.exists(promotedPrimaryMarker) || s.exists(fencePendingMarker) {
		t.Error("Standby rung must not touch PGDATA")
	}
}

// TestFenceCommandKills 는 얼어 있는 postmaster(SIGSTOP — fast·immediate 가 시한을 넘긴다)를 SIGKILL 단까지 가서
// 멈추고 그 결과(거두지 않은 좀비)를 Killed 로 보는지 본다. 실제 프로세스(comm=postgres)를 띄우고 postmaster.pid 에
// 적는다 — 시험이 Wait 하지 않아 얼어 있는 매니저를 흉내 낸다.
func TestFenceCommandKills(t *testing.T) {
	t.Parallel()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	s := newFenceScript(t, "exit 1")
	raw, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	fakePG := filepath.Join(t.TempDir(), "postgres")
	if err := os.WriteFile(fakePG, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(fakePG, "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	if err := child.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(child.Process.Pid)
	if err := os.Symlink(filepath.Join("/proc", pid), filepath.Join(s.proc, pid)); err != nil {
		t.Fatal(err)
	}
	s.touch("postmaster.pid", pid+"\n")

	if got := s.run(); got != fenceKilled {
		t.Errorf("outcome = %s, want %s", got, fenceKilled)
	}
	if got := s.stops(); !slices.Equal(got, []string{"fast", "immediate"}) {
		t.Errorf("pg_ctl stops = %v, want fast then immediate", got)
	}
	s.assertFenced(false)
}

// TestCutCommand 는 끊기 스크립트를 가짜 psql 로 돌린다 — standby 만 끊고, walreceiver 가 남으면 실패다.
func TestCutCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		recovery  string
		receivers string
		want      string
		fails     bool
	}{
		{"standby", pgBoolTrue, "0", cutResultPrefix + cutResultCut, false},
		{"primary", pgBoolFalse, "0", cutResultPrefix + cutResultPrim, false},
		{"receiver-stays", pgBoolTrue, "1", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.fails && testing.Short() {
				t.Skip("waits cutWait")
			}
			bin := t.TempDir()
			calls := filepath.Join(bin, "calls")
			stub := "#!/bin/sh\necho \"$*\" >> \"" + calls + "\"\ncase \"$*\" in\n" +
				"*pg_is_in_recovery*) echo " + tc.recovery + " ;;\n" +
				"*pg_stat_wal_receiver*) echo " + tc.receivers + " ;;\n" +
				"esac\n"
			if err := os.WriteFile(filepath.Join(bin, "psql"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			argv := cutCommand()
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Env = append(os.Environ(), "POSTGRES_BIN_DIR="+bin)
			out, err := cmd.Output()
			if (err != nil) != tc.fails {
				t.Fatalf("cut script err = %v, want failure %t\n%s", err, tc.fails, out)
			}
			if !tc.fails && strings.TrimSpace(string(out)) != tc.want {
				t.Errorf("output = %q, want %q", out, tc.want)
			}
			raw, _ := os.ReadFile(calls)
			altered := strings.Contains(string(raw), "primary_conninfo = ''")
			if altered != (tc.recovery == pgBoolTrue) {
				t.Errorf("psql calls = %q, ALTER only on a standby", raw)
			}
		})
	}
}

// TestParseFenceOutput 는 결과 줄이 없거나 모르는 값이면 Unreachable(증명 없음)인지 본다.
func TestParseFenceOutput(t *testing.T) {
	t.Parallel()
	for out, want := range map[string]fenceOutcome{
		"noise\nFENCE_RESULT=Killed\n": fenceKilled,
		"FENCE_RESULT=Down":            fenceDown,
		"":                             fenceUnreachable,
		"FENCE_RESULT=Maybe\n":         fenceUnreachable,
		"FENCE_RESULT=Unreachable\n":   fenceUnreachable,
	} {
		if got := parseFenceOutput([]byte(out)); got != want {
			t.Errorf("parseFenceOutput(%q) = %s, want %s", out, got, want)
		}
	}
}
