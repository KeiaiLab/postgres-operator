/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// 본 파일은 멤버 재구축 엔진(설계 2026-09-25 §3.5, ADR-0031)을 본다 — hold 는 요청 즉시, 파괴(해제)는
// readyToUse 스냅숏·재검증·예산 뒤에만, data-bearing 스냅숏은 GC 가 지우지 않는다. 시계·client·exec 는 가짜다.

const (
	rebuildTestClass = "ceph-rbd-snapclass"
	// rebuildTestFork 는 샤드 promotionLSN(새 타임라인 분기점)이고 Behind·Ahead 는 그 앞·뒤 끝 위치다.
	rebuildTestFork   = "0/5000000"
	rebuildTestBehind = "0/4000000"
	rebuildTestAhead  = "0/6000000"
	rebuildTestShard  = "shard-0"
)

// rebuildT0 은 가짜 시계의 시작이다.
var rebuildT0 = time.Unix(1_900_000_000, 0).UTC()

// rebuildExecutor 는 파드별로 답한다 — 끝 위치(endPositionQuery), pg_is_in_recovery, 재시드 applied 파일.
// 답이 없는 끝 위치·recovery exec 는 실패한다.
type rebuildExecutor struct {
	mu       sync.Mutex
	log      *writeLog
	ends     map[string]string // 파드 → "t|0/4000000"
	recovery map[string]string // 파드 → t|f
	applied  map[string]string // 파드 → applied 파일 내용
}

func (x *rebuildExecutor) set(answers map[string]string, pod, answer string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	answers[pod] = answer
}

func (x *rebuildExecutor) Exec(_ context.Context, target BackupSidecarTarget, command []string) ([]byte, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	joined := strings.Join(command, " ")
	answer, ok := "", true
	switch {
	case strings.Contains(joined, endPositionQuery):
		x.log.add("exec end " + target.Pod)
		answer, ok = x.ends[target.Pod]
	case strings.Contains(joined, reseedAppliedMarker):
		x.log.add("exec applied " + target.Pod)
		answer = x.applied[target.Pod]
	case strings.Contains(joined, recoveryQuery):
		x.log.add("exec recovery " + target.Pod)
		answer, ok = x.recovery[target.Pod]
	default:
		x.log.add("exec other " + target.Pod)
	}
	if !ok {
		return nil, fmt.Errorf("exec %s: unreachable", target.Pod)
	}
	return []byte(answer + "\n"), nil
}

// rebuildFaults 는 가짜 API 의 고장이다.
type rebuildFaults struct {
	mu            sync.Mutex
	noSnapshotAPI bool
}

func (f *rebuildFaults) snapshotAPIGone() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.noSnapshotAPI
}

// rebuildTestReplicas 는 시험 샤드의 standby 수다 — 예산 = 2, 스냅숏 상한 = 6.
const rebuildTestReplicas = 2

// rebuildWorld 는 샤드 0(primary = ordinal-0, standby 1..2)과 그 PVC·런타임 CM(분기점
// rebuildTestFork) 위의 엔진이다. 파드는 한 시간 전에 떴다. 그래프는 CM primary 에서 관측됐고 연결은
// 시험이 정한다(기본 0 — 재구축 대상은 끊겨 있다).
// 엔진은 쓰기를 기록하는 client 를, 시험 흉내(kubelet·스냅숏 컨트롤러)는 기록하지 않는 c 를 쓴다.
type rebuildWorld struct {
	name     string
	c        client.Client
	r        *PostgresClusterReconciler
	exec     *rebuildExecutor
	log      *writeLog
	recorder *events.FakeRecorder
	faults   *rebuildFaults
	graph    replicationGraph
}

func rebuildKind(obj client.Object) string {
	switch obj.(type) {
	case *corev1.Pod:
		return "pod"
	case *corev1.PersistentVolumeClaim:
		return "pvc"
	case *corev1.ConfigMap:
		return "cm"
	case *postgresv1alpha1.PostgresCluster:
		return "cluster"
	case *unstructured.Unstructured:
		return "snapshot"
	}
	return fmt.Sprintf("%T", obj)
}

func newRebuildWorld(t *testing.T, name string, extra ...client.Object) *rebuildWorld {
	t.Helper()
	cluster, cm := runtimeCluster(name)
	cluster.Spec.Shards.Replicas = rebuildTestReplicas
	withCanon(cluster, rebuildTestShard, shardCanon{Epoch: 1, Initialized: true, PromotionLSN: rebuildTestFork})
	log := &writeLog{}
	exec := &rebuildExecutor{log: log, ends: map[string]string{}, recovery: map[string]string{}, applied: map[string]string{}}
	graph := replicationGraph{root: podDNS(name, graphMember(name, 0)), known: true, linked: map[string]bool{}, upstream: map[string]string{}}
	objs := append([]client.Object{cluster, cm}, extra...)
	for i := range rebuildTestReplicas + 1 {
		pod := graphMember(name, i)
		role := statusapi.RoleReplica
		exec.recovery[pod] = pgBoolTrue
		if i == 0 {
			role = statusapi.RolePrimary
			exec.recovery[pod] = pgBoolFalse
		}
		p := shardMember(name, i, role)
		p.UID = types.UID(pod + "-uid-0")
		p.CreationTimestamp = metav1.NewTime(rebuildT0.Add(-time.Hour))
		objs = append(objs, p, memberPVC(pod, false))
	}

	faults := &rebuildFaults{}
	funcs := interceptor.Funcs{
		Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, snapshot := obj.(*unstructured.Unstructured); snapshot && faults.snapshotAPIGone() {
				return &meta.NoKindMatchError{GroupKind: volumeSnapshotGVK.GroupKind(), SearchedVersions: []string{volumeSnapshotGVK.Version}}
			}
			log.add("create " + rebuildKind(obj) + "/" + obj.GetName())
			return cli.Create(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			log.add("patch " + rebuildKind(obj) + "/" + obj.GetName())
			return cli.Patch(ctx, obj, patch, opts...)
		},
		Delete: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			log.add("delete " + rebuildKind(obj) + "/" + obj.GetName())
			return cli.Delete(ctx, obj, opts...)
		},
	}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).Build()
	recorder := events.NewFakeRecorder(wideEventBuffer)
	r := &PostgresClusterReconciler{
		Client: interceptor.NewClient(c, funcs), Scheme: scheme, PromotionPodExecutor: exec, Recorder: recorder,
		MemberSnapshotClass: rebuildTestClass,
	}
	t.Cleanup(func() { DeleteMetricsFor(runtimeTestNS, name) })
	return &rebuildWorld{name: name, c: c, r: r, exec: exec, log: log, recorder: recorder, faults: faults, graph: graph}
}

// cluster 는 저장된 클러스터다 — reconcile 처럼 매번 새로 읽는다.
func (w *rebuildWorld) cluster(t *testing.T) *postgresv1alpha1.PostgresCluster {
	t.Helper()
	var got postgresv1alpha1.PostgresCluster
	if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: w.name}, &got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	return &got
}

// step 은 한 reconcile 의 엔진 걸음이다 — condition 을 단 클러스터를 돌려준다.
func (w *rebuildWorld) step(t *testing.T, at time.Time) *postgresv1alpha1.PostgresCluster {
	t.Helper()
	cluster := w.cluster(t)
	shards := []postgresv1alpha1.ShardStatus{{Name: rebuildTestShard, Ordinal: 0}}
	w.r.advanceRebuilds(context.Background(), cluster, shards, map[string]replicationGraph{rebuildTestShard: w.graph}, at)
	return cluster
}

func (w *rebuildWorld) request(t *testing.T, pod string, reason rebuildReason, at time.Time) {
	t.Helper()
	if err := w.r.requestRebuild(context.Background(), w.cluster(t), rebuildTestShard, pod, reason, at); err != nil {
		t.Fatalf("requestRebuild(%s, %s): %v", pod, reason, err)
	}
}

func (w *rebuildWorld) state(t *testing.T) rebuildState {
	t.Helper()
	state, err := loadRebuildState(w.cluster(t))
	if err != nil {
		t.Fatalf("load rebuild state: %v", err)
	}
	return state
}

func (w *rebuildWorld) entry(t *testing.T, pod string) (memberRebuild, bool) {
	t.Helper()
	e, ok := w.state(t).Active[pod]
	return e, ok
}

func (w *rebuildWorld) mustEntry(t *testing.T, pod string) memberRebuild {
	t.Helper()
	e, ok := w.entry(t, pod)
	if !ok {
		t.Fatalf("no active rebuild for %s: %+v", pod, w.state(t))
	}
	return e
}

// runtime 은 샤드 0 런타임 CM 의 값이다.
func (w *rebuildWorld) runtime(t *testing.T) shardRuntime {
	t.Helper()
	return runtimeFromData(runtimeCM(t, w.c, w.name))
}

func (w *rebuildWorld) held(t *testing.T, pod string) bool {
	t.Helper()
	return slices.Contains(w.runtime(t).Hold, pod)
}

// seedLedger 는 원장을 심는다.
func (w *rebuildWorld) seedLedger(t *testing.T, entries ...rebuildEntry) {
	t.Helper()
	cluster := w.cluster(t)
	state, err := loadRebuildState(cluster)
	if err != nil {
		t.Fatalf("load rebuild state: %v", err)
	}
	state.Ledger = append(state.Ledger, entries...)
	if err := w.r.saveRebuildState(context.Background(), cluster, state); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
}

func (w *rebuildWorld) fence(t *testing.T, pod string) {
	t.Helper()
	var pvc corev1.PersistentVolumeClaim
	key := client.ObjectKey{Namespace: runtimeTestNS, Name: fencing.PVCName(pod)}
	if err := w.c.Get(context.Background(), key, &pvc); err != nil {
		t.Fatalf("get pvc: %v", err)
	}
	if pvc.Labels == nil {
		pvc.Labels = map[string]string{}
	}
	pvc.Labels[fencing.FenceLabelKey] = fencing.FenceLabelValue
	if err := w.c.Update(context.Background(), &pvc); err != nil {
		t.Fatalf("fence pvc: %v", err)
	}
}

func (w *rebuildWorld) fenced(t *testing.T, pod string) bool {
	t.Helper()
	var pvc corev1.PersistentVolumeClaim
	if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: fencing.PVCName(pod)}, &pvc); err != nil {
		t.Fatalf("get pvc: %v", err)
	}
	return pvc.Labels[fencing.FenceLabelKey] == fencing.FenceLabelValue
}

// podFate 는 StatefulSet 이 다시 만든 파드의 모습이다.
type podFate string

const (
	podHeld    podFate = "held"    // bootstrap 이 hold 로 비0 종료, postgres 는 돈 적 없음
	podStarted podFate = "started" // hold 가 반영되기 전 — postgres 가 돈다
	podBooting podFate = "booting" // bootstrap 이 아직 도는 중
	podServing podFate = "serving" // 재합류해 도는 standby
)

// recreate 는 StatefulSet·kubelet 흉내다 — 지워진 멤버 1 을 새 UID 로 fate 모습대로 만든다.
func (w *rebuildWorld) recreate(t *testing.T, uid string, fate podFate) {
	t.Helper()
	pod := shardMember(w.name, 1, statusapi.RoleReplica)
	pod.UID = types.UID(uid)
	pod.CreationTimestamp = metav1.NewTime(rebuildT0)
	initStatus := corev1.ContainerStatus{Name: bootstrapContainerName}
	pgStatus := corev1.ContainerStatus{Name: pgContainerName}
	switch fate {
	case podHeld:
		pod.Status.Phase = corev1.PodPending
		initStatus.State.Waiting = &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}
		initStatus.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ExitCode: 1}
		pgStatus.State.Waiting = &corev1.ContainerStateWaiting{Reason: "PodInitializing"}
		delete(pod.Annotations, statusapi.AnnotationKey)
	case podBooting:
		pod.Status.Phase = corev1.PodPending
		initStatus.State.Running = &corev1.ContainerStateRunning{}
		pgStatus.State.Waiting = &corev1.ContainerStateWaiting{Reason: "PodInitializing"}
		delete(pod.Annotations, statusapi.AnnotationKey)
	case podStarted, podServing:
		initStatus.State.Terminated = &corev1.ContainerStateTerminated{ExitCode: 0}
		pgStatus.State.Running = &corev1.ContainerStateRunning{}
		pgStatus.Ready = true
	}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{initStatus}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{pgStatus}
	if err := w.c.Create(context.Background(), pod); err != nil {
		t.Fatalf("recreate %s: %v", pod.Name, err)
	}
}

func (w *rebuildWorld) snapshots(t *testing.T) []unstructured.Unstructured {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(volumeSnapshotGVK.GroupVersion().WithKind(volumeSnapshotGVK.Kind + "List"))
	if err := w.c.List(context.Background(), list, client.InNamespace(runtimeTestNS)); err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	return list.Items
}

// markSnapshot 은 스냅숏 컨트롤러 흉내다 — status 를 바꾼다.
func (w *rebuildWorld) markSnapshot(t *testing.T, name string, status map[string]any) {
	t.Helper()
	snap := &unstructured.Unstructured{}
	snap.SetGroupVersionKind(volumeSnapshotGVK)
	if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: name}, snap); err != nil {
		t.Fatalf("get snapshot %s: %v", name, err)
	}
	snap.Object["status"] = status
	if err := w.c.Update(context.Background(), snap); err != nil {
		t.Fatalf("update snapshot %s: %v", name, err)
	}
}

var (
	snapshotReadyStatus  = map[string]any{"readyToUse": true}
	snapshotFailedStatus = map[string]any{"readyToUse": false, "error": map[string]any{"message": "csi: rbd snap create failed"}}
)

// toSnapshot 은 요청 뒤 멤버 1 을 멈추고 새 파드가 hold 로 멈춘 뒤 스냅숏이 생기기까지 몬다 — 마지막 걸음 시각을 준다.
func (w *rebuildWorld) toSnapshot(t *testing.T, at time.Time) time.Time {
	t.Helper()
	pod := graphMember(w.name, 1)
	at = at.Add(runtimePropagationWait)
	w.step(t, at)
	if e := w.mustEntry(t, pod); e.StoppedAt.IsZero() {
		t.Fatalf("%s not stopped after the propagation wait: %+v", pod, e)
	}
	w.recreate(t, pod+"-uid-held", podHeld)
	for range 2 {
		at = at.Add(statusPollInterval)
		w.step(t, at)
	}
	if e := w.mustEntry(t, pod); e.Phase != rebuildSnapshotting || e.Snapshot == "" {
		t.Fatalf("%s has no snapshot after the held pod: %+v", pod, e)
	}
	return at
}

func rebuildCount(w *rebuildWorld, reason rebuildReason, outcome rebuildOutcome) float64 {
	return testutil.ToFloat64(MetricMemberRebuilds.WithLabelValues(runtimeTestNS, w.name, rebuildTestShard, string(reason), string(outcome)))
}

func blockedCondition(cluster *postgresv1alpha1.PostgresCluster) *metav1.Condition {
	return meta.FindStatusCondition(cluster.Status.Conditions, ConditionMemberRebuildBlocked)
}

// TestRebuildStateMachine 은 한 재구축의 전 과정을 본다: 요청 즉시 hold → 전파 뒤 끝 위치·삭제 → hold 가
// 반영되기 전에 뜬 postgres 는 다시 삭제(스냅숏 0) → 새 파드가 hold 로 멈추면 스냅숏 → readyToUse 전 해제 0
// → 원장 뒤 hold 해제(Stale 은 재시드 토큰과 한 CM 쓰기) → 전파 뒤 재삭제 → 그래프 연결(재시드면 applied ==
// token) → RESEED 제거·unfence·원장·이벤트·지표.
func TestRebuildStateMachine(t *testing.T) {
	t.Parallel()
	cases := []smCase{
		{rebuildDemoted, "", true, outcomeUnproven},
		{rebuildStale, pgBoolTrue + psqlFieldSeparator + rebuildTestBehind, false, outcomeReleased},
	}
	for _, tc := range cases {
		t.Run(string(tc.reason), func(t *testing.T) {
			t.Parallel()
			s := newSMRun(t, tc)
			s.holdAndStop()
			s.redeleteHoldless()
			s.release(s.snapshotHeld())
			s.redelete()
			s.verify()
			s.completed()
		})
	}
}

// smCase 는 상태기계 시험 한 판이다.
type smCase struct {
	reason  rebuildReason
	end     string // 끝 위치 exec 답 — 빈 문자열이면 exec 실패(옛 primary 는 죽었다)
	bearing bool
	outcome rebuildOutcome
}

// smRun 은 한 판의 진행이다 — 단계마다 메서드 하나.
type smRun struct {
	t      *testing.T
	w      *rebuildWorld
	tc     smCase
	target string
	token  string
	at     time.Time
}

func newSMRun(t *testing.T, tc smCase) *smRun {
	w := newRebuildWorld(t, "sm-"+strings.ToLower(string(tc.reason)))
	target := graphMember(w.name, 1)
	w.graph.linked[graphMember(w.name, 2)] = true
	w.fence(t, target)
	if tc.end != "" {
		w.exec.set(w.exec.ends, target, tc.end)
	}
	return &smRun{t: t, w: w, tc: tc, target: target, token: rebuildToken(rebuildT0, 0), at: rebuildT0}
}

// tick 은 다음 reconcile(statusPollInterval 뒤)이다.
func (s *smRun) tick() {
	s.at = s.at.Add(statusPollInterval)
	s.w.step(s.t, s.at)
}

// propagate 는 런타임 CM 쓰기 뒤 전파 대기를 넘긴 reconcile 이다.
func (s *smRun) propagate() {
	s.at = s.at.Add(runtimePropagationWait)
	s.w.step(s.t, s.at)
}

func (s *smRun) deletes() int {
	return s.w.log.count("delete pod/" + s.target)
}

// holdAndStop: 요청 즉시 hold → 전파 전 행동 0 → 전파 뒤 끝 위치 exec 뒤 삭제.
func (s *smRun) holdAndStop() {
	t, w := s.t, s.w
	w.request(t, s.target, s.tc.reason, s.at)
	if !w.held(t, s.target) {
		t.Fatalf("MEMBER_HOLD = %v right after the request, want %s", w.runtime(t).Hold, s.target)
	}
	w.step(t, s.at.Add(runtimePropagationWait-time.Second))
	if n := s.deletes() + w.log.count("exec end "+s.target); n != 0 {
		t.Fatalf("acted before runtimePropagationWait: %q", w.log.lines)
	}
	s.propagate()
	del, end := w.log.first("delete pod/"+s.target), w.log.first("exec end "+s.target)
	if del < 0 || end < 0 || end > del {
		t.Fatalf("want end-position exec then pod delete, got %q", w.log.lines)
	}
	if e := w.mustEntry(t, s.target); e.DataBearing != s.tc.bearing || e.PromotionLSN != rebuildTestFork {
		t.Fatalf("entry after stop = %+v, want dataBearing=%v promotionLSN=%s", e, s.tc.bearing, rebuildTestFork)
	}
}

// redeleteHoldless: hold 가 반영되기 전에 뜬 postgres 는 다시 지운다(스냅숏 0) · bootstrap 판정 전엔 기다린다.
func (s *smRun) redeleteHoldless() {
	t, w := s.t, s.w
	w.recreate(t, "uid-started", podStarted)
	s.tick()
	if n := s.deletes(); n != 2 || len(w.snapshots(t)) != 0 {
		t.Fatalf("after a hold-less start: deletes=%d snapshots=%d, want 2 and 0", n, len(w.snapshots(t)))
	}
	w.recreate(t, "uid-booting", podBooting)
	s.tick()
	if e := w.mustEntry(t, s.target); e.Phase != rebuildHolding {
		t.Fatalf("phase with bootstrap still running = %s, want Holding", e.Phase)
	}
	if err := w.c.Delete(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.target, Namespace: runtimeTestNS}}); err != nil {
		t.Fatalf("delete booting pod: %v", err)
	}
}

// snapshotHeld: 새 파드가 hold 로 멈추면 라벨·클래스·원천을 단 스냅숏(소유 참조 0)을 뜬다.
func (s *smRun) snapshotHeld() string {
	t, w := s.t, s.w
	w.recreate(t, "uid-held", podHeld)
	s.tick()
	s.tick()
	snaps := w.snapshots(t)
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snaps))
	}
	snap := snaps[0]
	wantLabels := map[string]string{
		memberSnapshotLabel: labelTrue,
		clusterLabelKey:     w.name,
		ShardIDLabelKey:     rebuildTestShard,
		rebuildReasonLabel:  string(s.tc.reason),
		dataBearingLabel:    strconv.FormatBool(s.tc.bearing),
	}
	class, _, _ := unstructured.NestedString(snap.Object, "spec", "volumeSnapshotClassName")
	source, _, _ := unstructured.NestedString(snap.Object, "spec", "source", "persistentVolumeClaimName")
	if snap.GetName() != s.target+memberSnapshotInfix+s.token || !maps.Equal(snap.GetLabels(), wantLabels) ||
		class != rebuildTestClass || source != fencing.PVCName(s.target) || len(snap.GetOwnerReferences()) != 0 {
		t.Fatalf("snapshot = %s labels=%v class=%s source=%s owners=%v", snap.GetName(), snap.GetLabels(), class, source, snap.GetOwnerReferences())
	}
	return snap.GetName()
}

// release: readyToUse 전 hold 유지 → ready 면 원장에 해제(CM 은 아직 hold) → 다음 걸음이 hold 해제와 재시드
// 토큰을 한 CM 쓰기로.
func (s *smRun) release(snapshot string) {
	t, w := s.t, s.w
	s.tick()
	if !w.held(t, s.target) || w.mustEntry(t, s.target).Phase != rebuildSnapshotting {
		t.Fatalf("released before readyToUse: hold=%v entry=%+v", w.runtime(t).Hold, w.mustEntry(t, s.target))
	}
	w.markSnapshot(t, snapshot, snapshotReadyStatus)
	s.tick()
	if e := w.mustEntry(t, s.target); e.Phase != rebuildReleasing || !w.held(t, s.target) {
		t.Fatalf("after readyToUse: entry=%+v hold=%v, want Releasing with the CM still holding", e, w.runtime(t).Hold)
	}
	if ledger := w.state(t).Ledger; len(ledger) != 1 || ledger[0].Released.IsZero() {
		t.Fatalf("ledger after admission = %+v, want one release", ledger)
	}
	patches := w.log.count("patch cm/")
	s.tick()
	if n := w.log.count("patch cm/") - patches; n != 1 {
		t.Fatalf("CM writes for the release = %d, want 1: %q", n, w.log.lines)
	}
	rt := w.runtime(t)
	if slices.Contains(rt.Hold, s.target) || rt.Reseed[s.target] != s.wantToken() {
		t.Fatalf("runtime after release = hold %v reseed %v, want no hold and token %q", rt.Hold, rt.Reseed, s.wantToken())
	}
}

// wantToken 은 해제 뒤 RESEED_REQUESTS 에 있어야 할 토큰이다 — 재시드 사유만.
func (s *smRun) wantToken() string {
	if s.tc.reason.reseeds() {
		return s.token
	}
	return ""
}

// redelete: 전파 뒤에만 파드를 다시 지우고 Verifying 으로 간다.
func (s *smRun) redelete() {
	t, w := s.t, s.w
	w.step(t, s.at.Add(runtimePropagationWait-time.Second))
	if n := s.deletes(); n != 2 {
		t.Fatalf("re-deleted before runtimePropagationWait: deletes=%d", n)
	}
	s.propagate()
	if n := s.deletes(); n != 3 || w.mustEntry(t, s.target).Phase != rebuildVerifying {
		t.Fatalf("after propagation: deletes=%d entry=%+v, want 3 and Verifying", n, w.mustEntry(t, s.target))
	}
}

// verify: 그래프에 붙기 전엔(토큰이 적용됐어도), 붙었어도 재시드 토큰이 적용되기 전엔 끝나지 않는다.
func (s *smRun) verify() {
	t, w := s.t, s.w
	w.recreate(t, "uid-serving", podServing)
	w.exec.set(w.exec.applied, s.target, s.token)
	s.tick()
	if _, active := w.entry(t, s.target); !active {
		t.Fatal("rebuild finished before the member rejoined the replication graph")
	}
	w.graph.linked[s.target] = true
	if s.tc.reason.reseeds() {
		w.exec.set(w.exec.applied, s.target, "older-token")
		s.tick()
		if _, active := w.entry(t, s.target); !active {
			t.Fatal("rebuild finished before the reseed token was applied")
		}
		w.exec.set(w.exec.applied, s.target, s.token)
	}
	s.tick()
}

// completed: 항목 없음 · hold·RESEED 없음 · unfence · 원장 끝 · 지표 · 이벤트.
func (s *smRun) completed() {
	t, w := s.t, s.w
	if e, active := w.entry(t, s.target); active {
		t.Fatalf("rebuild still active after rejoin: %+v", e)
	}
	rt := w.runtime(t)
	if slices.Contains(rt.Hold, s.target) || rt.Reseed[s.target] != "" || w.fenced(t, s.target) {
		t.Fatalf("after completion: hold=%v reseed=%v fenced=%v, want all cleared", rt.Hold, rt.Reseed, w.fenced(t, s.target))
	}
	ledger := w.state(t).Ledger
	if len(ledger) != 1 || ledger[0].Ended.IsZero() || ledger[0].Outcome != s.tc.outcome || ledger[0].Token != s.token {
		t.Fatalf("ledger = %+v, want one ended entry with outcome %s", ledger, s.tc.outcome)
	}
	if got := rebuildCount(w, s.tc.reason, s.tc.outcome); got != 1 {
		t.Fatalf("member_rebuilds_total{%s,%s} = %v, want 1", s.tc.reason, s.tc.outcome, got)
	}
	released := memberRejoinReleasedReason
	if s.tc.reason.reseeds() {
		released = memberReseedRequestedReason
	}
	seen := drainEvents(w.recorder)
	for _, reason := range []string{memberHeldReason, memberSnapshotReadyReason, released, memberRebuildCompletedReason} {
		if !slices.ContainsFunc(seen, func(ev string) bool { return strings.Contains(ev, " "+reason+" ") }) {
			t.Errorf("event %s missing: %q", reason, seen)
		}
	}
}

// TestRebuildDataBearing 은 data-bearing = lead > 0 ∨ 모름 을 본다(lead = 끝 위치 − 분기점). standby exec 는
// 정확하고, primary exec·상태 애노테이션은 하한이라 분기점을 넘을 때만 증거다. 해제 결과는 양성 증거면
// data_bearing, 모르면 data_bearing_unproven, 뒤처졌으면 released.
func TestRebuildDataBearing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		end        string // 끝 위치 exec 답 — 빈 문자열이면 실패
		annotation int64  // 상태 애노테이션 WALLSNBytes(0 = 모름)
		fork       string
		endLSN     string
		bearing    bool
		outcome    rebuildOutcome
	}{
		{"standby behind", "t|" + rebuildTestBehind, 0, rebuildTestFork, rebuildTestBehind, false, outcomeReleased},
		{"standby ahead", "t|" + rebuildTestAhead, 0, rebuildTestFork, rebuildTestAhead, true, outcomeDataBearing},
		{"primary behind is a lower bound", "f|" + rebuildTestBehind, 0, rebuildTestFork, "", true, outcomeUnproven},
		{"primary ahead", "f|" + rebuildTestAhead, 0, rebuildTestFork, rebuildTestAhead, true, outcomeDataBearing},
		{"exec fails, annotation ahead", "", 0x6000000, rebuildTestFork, rebuildTestAhead, true, outcomeDataBearing},
		{"exec fails, annotation behind", "", 0x4000000, rebuildTestFork, "", true, outcomeUnproven},
		{"fork unknown", "t|" + rebuildTestBehind, 0, "", rebuildTestBehind, true, outcomeUnproven},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRebuildWorld(t, "bearing-"+strconv.Itoa(i))
			if tc.fork != rebuildTestFork {
				cluster := w.cluster(t)
				withCanon(cluster, rebuildTestShard, shardCanon{Epoch: 1, Initialized: true, PromotionLSN: tc.fork})
				if err := w.c.Update(context.Background(), cluster); err != nil {
					t.Fatalf("update canon: %v", err)
				}
			}
			target := graphMember(w.name, 1)
			if tc.end != "" {
				w.exec.set(w.exec.ends, target, tc.end)
			}
			if tc.annotation != 0 {
				var pod corev1.Pod
				if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: target}, &pod); err != nil {
					t.Fatalf("get pod: %v", err)
				}
				setPosition(t, &pod, tc.annotation, 0)
				if err := w.c.Update(context.Background(), &pod); err != nil {
					t.Fatalf("update pod: %v", err)
				}
			}

			w.request(t, target, rebuildDemoted, rebuildT0)
			w.step(t, rebuildT0.Add(runtimePropagationWait))

			e := w.mustEntry(t, target)
			if e.EndLSN != tc.endLSN || e.DataBearing != tc.bearing {
				t.Fatalf("entry = endLSN %q dataBearing %v, want %q %v", e.EndLSN, e.DataBearing, tc.endLSN, tc.bearing)
			}
			if got := releaseOutcome(e, admitBudget); got != tc.outcome {
				t.Fatalf("release outcome = %s, want %s", got, tc.outcome)
			}
		})
	}
}

// TestRebuildDiscardsNewerDataWarns 는 분기점을 넘은 멤버를 해제할 때 outcome data_bearing 과 Warning
// RebuildDiscardsNewerData 를 남기고 스냅숏 라벨이 data-bearing=true 인지 본다.
func TestRebuildDiscardsNewerDataWarns(t *testing.T) {
	t.Parallel()
	w := newRebuildWorld(t, "discards")
	target := graphMember(w.name, 1)
	w.exec.set(w.exec.ends, target, "f|"+rebuildTestAhead)
	w.request(t, target, rebuildFencedPrimary, rebuildT0)
	at := w.toSnapshot(t, rebuildT0)
	snap := w.mustEntry(t, target).Snapshot
	w.markSnapshot(t, snap, snapshotReadyStatus)
	drainEvents(w.recorder)
	w.step(t, at.Add(statusPollInterval))

	if got := rebuildCount(w, rebuildFencedPrimary, outcomeDataBearing); got != 1 {
		t.Fatalf("member_rebuilds_total{FencedPrimary,data_bearing} = %v, want 1", got)
	}
	warned := slices.ContainsFunc(drainEvents(w.recorder), func(ev string) bool {
		return strings.HasPrefix(ev, "Warning "+rebuildDiscardsNewerDataReason+" ")
	})
	if !warned {
		t.Fatalf("Warning %s missing", rebuildDiscardsNewerDataReason)
	}
	if labels := w.snapshots(t)[0].GetLabels(); labels[dataBearingLabel] != labelTrue {
		t.Fatalf("snapshot data-bearing label = %q, want true", labels[dataBearingLabel])
	}
}

// TestRebuildBudget 은 replicas 2 에서 1h 창 해제 2 뒤 세 번째 해제가 막히는지 본다 — MemberRebuildBlocked
// (BudgetExhausted) · hold 유지 · 새 reconciler 도 CR 원장으로 막힌다 · 창이 지나면 허가된다.
func TestRebuildBudget(t *testing.T) {
	t.Parallel()
	w := newRebuildWorld(t, "budget")
	target := graphMember(w.name, 1)
	w.exec.set(w.exec.ends, target, "t|"+rebuildTestBehind)
	spent := rebuildT0.Add(-30 * time.Minute)
	w.seedLedger(t,
		rebuildEntry{Pod: "budget-x-1", Shard: rebuildTestShard, Token: "a-0", Reason: rebuildStale, Outcome: outcomeReleased, Admission: admitBudget, Released: spent, Ended: spent},
		rebuildEntry{Pod: "budget-x-2", Shard: rebuildTestShard, Token: "b-0", Reason: rebuildStale, Outcome: outcomeReleased, Admission: admitBudget, Released: spent, Ended: spent},
	)

	// 요청은 예산이 없어도 즉시 hold 다.
	w.request(t, target, rebuildStale, rebuildT0)
	if !w.held(t, target) {
		t.Fatal("request with an exhausted budget did not hold")
	}
	at := w.toSnapshot(t, rebuildT0)
	w.markSnapshot(t, w.mustEntry(t, target).Snapshot, snapshotReadyStatus)
	at = at.Add(statusPollInterval)
	cluster := w.step(t, at)

	e := w.mustEntry(t, target)
	cond := blockedCondition(cluster)
	if e.Phase != rebuildSnapshotting || e.Blocked != ReasonBudgetExhausted || !w.held(t, target) {
		t.Fatalf("third release in the window: entry=%+v hold=%v, want blocked Snapshotting still held", e, w.runtime(t).Hold)
	}
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ReasonBudgetExhausted {
		t.Fatalf("MemberRebuildBlocked = %+v, want True/%s", cond, ReasonBudgetExhausted)
	}
	if got := testutil.ToFloat64(MetricMemberRebuildBudget.WithLabelValues(runtimeTestNS, w.name)); got != 0 {
		t.Fatalf("budget remaining = %v, want 0", got)
	}

	// 재기동한 오퍼레이터 — 원장이 CR 에 있어 여전히 막힌다.
	w.r = &PostgresClusterReconciler{Client: w.c, Scheme: w.r.Scheme, PromotionPodExecutor: w.exec,
		Recorder: w.recorder, MemberSnapshotClass: rebuildTestClass}
	at = at.Add(statusPollInterval)
	w.step(t, at)
	if e := w.mustEntry(t, target); e.Phase != rebuildSnapshotting {
		t.Fatalf("new reconciler released past the budget: %+v", e)
	}

	// 창이 지나면 허가.
	at = spent.Add(rebuildBudgetWindow + time.Second)
	cluster = w.step(t, at)
	if e := w.mustEntry(t, target); e.Phase != rebuildReleasing || e.Blocked != "" {
		t.Fatalf("after the window: %+v, want Releasing", e)
	}
	if cond := blockedCondition(cluster); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("MemberRebuildBlocked after admission = %+v, want False", cond)
	}
	if n := countEvents(w.recorder, "Warning "+ConditionMemberRebuildBlocked+" "); n != 1 {
		t.Fatalf("MemberRebuildBlocked warnings = %d, want 1 (only on the transition)", n)
	}
}

// TestRebuildAvailabilityException 은 예산이 다 찼어도 샤드 streaming standby 0 ∧ 옛 primary 스냅숏 ready 면
// 1건을 예산 밖으로 허가하는지(outcome availability) 본다 — streaming ≥ 1 이거나 창 안 예외를 이미 썼으면 0.
func TestRebuildAvailabilityException(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		reason    rebuildReason
		streaming bool
		used      bool
		admitted  bool
	}{
		{"no streaming standby", rebuildDemoted, false, false, true},
		{"fenced primary", rebuildFencedPrimary, false, false, true},
		{"streaming standby left", rebuildDemoted, true, false, false},
		{"exception already used", rebuildDemoted, false, true, false},
		{"not an old primary", rebuildStale, false, false, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRebuildWorld(t, "avail-"+strconv.Itoa(i))
			target, other := graphMember(w.name, 1), graphMember(w.name, 2)
			if tc.streaming {
				w.graph.linked[other] = true
			}
			spent := rebuildT0.Add(-10 * time.Minute)
			ledger := []rebuildEntry{
				{Pod: "p-1", Shard: rebuildTestShard, Token: "a-0", Reason: rebuildStale, Outcome: outcomeReleased, Admission: admitBudget, Released: spent},
				{Pod: "p-2", Shard: rebuildTestShard, Token: "b-0", Reason: rebuildStale, Outcome: outcomeReleased, Admission: admitBudget, Released: spent},
			}
			if tc.used {
				ledger = append(ledger, rebuildEntry{Pod: "p-3", Shard: rebuildTestShard, Token: "c-0", Reason: rebuildDemoted,
					Outcome: outcomeAvailability, Admission: admitAvailability, Released: spent})
			}
			w.seedLedger(t, ledger...)

			w.request(t, target, tc.reason, rebuildT0)
			at := w.toSnapshot(t, rebuildT0)
			w.markSnapshot(t, w.mustEntry(t, target).Snapshot, snapshotReadyStatus)
			w.step(t, at.Add(statusPollInterval))

			e := w.mustEntry(t, target)
			if admitted := e.Phase == rebuildReleasing; admitted != tc.admitted {
				t.Fatalf("admitted = %v (entry %+v), want %v", admitted, e, tc.admitted)
			}
			want := 0.0
			if tc.admitted {
				want = 1
			}
			if got := rebuildCount(w, tc.reason, outcomeAvailability); got != want {
				t.Fatalf("member_rebuilds_total{%s,availability} = %v, want %v", tc.reason, got, want)
			}
		})
	}
}

// TestRebuildPacing 은 멤버 쿨다운(마지막 끝 + 10분)과 샤드당 1 을 본다 — 둘 다 hold 는 막지 않고 멈추기만 늦춘다.
func TestRebuildPacing(t *testing.T) {
	t.Parallel()
	t.Run("cooldown", func(t *testing.T) {
		t.Parallel()
		w := newRebuildWorld(t, "cooldown")
		target := graphMember(w.name, 1)
		ended := rebuildT0.Add(-5 * time.Minute)
		w.seedLedger(t, rebuildEntry{Pod: target, Shard: rebuildTestShard, Token: "old-0", Reason: rebuildStale,
			Outcome: outcomeReleased, Admission: admitBudget, Released: ended, Ended: ended})

		w.request(t, target, rebuildDemoted, rebuildT0)
		for at := rebuildT0.Add(runtimePropagationWait); at.Before(ended.Add(reseedCooldown)); at = at.Add(time.Minute) {
			w.step(t, at)
		}
		if n := w.log.count("delete pod/" + target); n != 0 || !w.held(t, target) {
			t.Fatalf("stopped inside the cooldown: deletes=%d hold=%v", n, w.runtime(t).Hold)
		}
		w.step(t, ended.Add(reseedCooldown))
		if n := w.log.count("delete pod/" + target); n != 1 {
			t.Fatalf("deletes after the cooldown = %d, want 1", n)
		}
	})

	t.Run("one per shard", func(t *testing.T) {
		t.Parallel()
		w := newRebuildWorld(t, "pershard")
		first, second := graphMember(w.name, 1), graphMember(w.name, 2)
		w.request(t, first, rebuildDemoted, rebuildT0)
		w.request(t, second, rebuildDemoted, rebuildT0)
		if !w.held(t, first) || !w.held(t, second) {
			t.Fatalf("both requests must hold at once: %v", w.runtime(t).Hold)
		}
		at := rebuildT0.Add(runtimePropagationWait)
		for range 3 {
			w.step(t, at)
			at = at.Add(statusPollInterval)
		}
		if n1, n2 := w.log.count("delete pod/"+first), w.log.count("delete pod/"+second); n1 != 1 || n2 != 0 {
			t.Fatalf("deletes first=%d second=%d, want 1 and 0 (one rebuild per shard)", n1, n2)
		}

		// 먼저 온 재구축이 끝나면 다음이 멈춘다 — 재검증 취소로 끝낸다.
		w.recreate(t, "uid-held", podHeld)
		for range 2 {
			w.step(t, at)
			at = at.Add(statusPollInterval)
		}
		cm := buildRuntimeConfigMap(w.cluster(t), rebuildTestShard, shardRuntime{
			PrimaryEndpoint: podDNS(w.name, first), PromotionEpoch: 2, ClusterInitialized: true, Hold: w.runtime(t).Hold,
		})
		patchData(t, w.c, cm)
		w.markSnapshot(t, w.mustEntry(t, first).Snapshot, snapshotReadyStatus)
		w.step(t, at)
		if _, active := w.entry(t, first); active {
			t.Fatal("first rebuild should have been cancelled (target became the runtime primary)")
		}
		w.step(t, at.Add(statusPollInterval))
		if n := w.log.count("delete pod/" + second); n != 1 {
			t.Fatalf("second member deletes after the first ended = %d, want 1", n)
		}
	})
}

// patchData 는 CM 데이터를 통째로 바꾼다(시험 흉내 — 승격 발행).
func patchData(t *testing.T, c client.Client, cm *corev1.ConfigMap) {
	t.Helper()
	var got corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cm), &got); err != nil {
		t.Fatalf("get cm: %v", err)
	}
	got.Data = cm.Data
	if err := c.Update(context.Background(), &got); err != nil {
		t.Fatalf("update cm: %v", err)
	}
}

// TestStaleEligibility 는 파드 나이 < 8분이거나 이미지를 못 받는 멤버의 Stale 요청이 hold 0 · 항목 0 인지 본다.
func TestStaleEligibility(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*corev1.Pod)
		want   bool
	}{
		{"old enough", func(*corev1.Pod) {}, true},
		{"young pod", func(p *corev1.Pod) {
			p.CreationTimestamp = metav1.NewTime(rebuildT0.Add(-staleStandbyReseedTimeout + time.Second))
		}, false},
		{"image pull backoff", func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].State.Waiting = &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}
		}, false},
		{"init image pull", func(p *corev1.Pod) {
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{
				Name: bootstrapContainerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull"}},
			}}
		}, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRebuildWorld(t, "elig-"+strconv.Itoa(i))
			target := graphMember(w.name, 1)
			var pod corev1.Pod
			if err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: target}, &pod); err != nil {
				t.Fatalf("get pod: %v", err)
			}
			tc.mutate(&pod)
			if err := w.c.Delete(context.Background(), &pod); err != nil {
				t.Fatalf("delete pod: %v", err)
			}
			pod.ResourceVersion = ""
			if err := w.c.Create(context.Background(), &pod); err != nil {
				t.Fatalf("create pod: %v", err)
			}

			w.request(t, target, rebuildStale, rebuildT0)
			_, active := w.entry(t, target)
			if held := w.held(t, target); held != tc.want || active != tc.want {
				t.Fatalf("Stale request: held=%v active=%v, want %v", held, active, tc.want)
			}
		})
	}
}

// TestRebuildSnapshotClassUnset 은 스냅숏 클래스가 비었을 때를 본다 — 옛 primary 는 hold 를 둔 채
// Blocked(SnapshotClassUnset), Stale 은 멤버를 멈추지 않고 hold 를 풀고 끝낸다.
func TestRebuildSnapshotClassUnset(t *testing.T) {
	t.Parallel()
	t.Run("demoted keeps the hold", func(t *testing.T) {
		t.Parallel()
		w := newRebuildWorld(t, "noclass-demoted")
		w.r.MemberSnapshotClass = ""
		target := graphMember(w.name, 1)
		w.request(t, target, rebuildDemoted, rebuildT0)
		at := rebuildT0.Add(runtimePropagationWait)
		w.step(t, at)
		w.recreate(t, "uid-held", podHeld)
		var cluster *postgresv1alpha1.PostgresCluster
		for range 3 {
			at = at.Add(statusPollInterval)
			cluster = w.step(t, at)
		}
		e := w.mustEntry(t, target)
		cond := blockedCondition(cluster)
		if e.Blocked != ReasonSnapshotClassUnset || !w.held(t, target) || len(w.snapshots(t)) != 0 {
			t.Fatalf("entry=%+v hold=%v snapshots=%d, want blocked with the hold kept and no snapshot", e, w.runtime(t).Hold, len(w.snapshots(t)))
		}
		if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != ReasonSnapshotClassUnset {
			t.Fatalf("MemberRebuildBlocked = %+v, want True/%s", cond, ReasonSnapshotClassUnset)
		}
	})

	t.Run("stale is released", func(t *testing.T) {
		t.Parallel()
		w := newRebuildWorld(t, "noclass-stale")
		w.r.MemberSnapshotClass = ""
		target := graphMember(w.name, 1)
		w.request(t, target, rebuildStale, rebuildT0)
		w.step(t, rebuildT0.Add(runtimePropagationWait))
		if _, active := w.entry(t, target); active || w.held(t, target) || w.log.count("delete pod/"+target) != 0 {
			t.Fatalf("Stale without a snapshot class: active=%v hold=%v deletes=%d, want released without a delete",
				active, w.runtime(t).Hold, w.log.count("delete pod/"+target))
		}
		if got := rebuildCount(w, rebuildStale, outcomeAbandoned); got != 1 {
			t.Fatalf("member_rebuilds_total{Stale,abandoned} = %v, want 1", got)
		}
	})
}

// TestRebuildSnapshotFailure 는 status.error·snapshotReadyTimeout·API 없음에서 시도를 버리고(attempt+1, 새 토큰)
// reseedCooldown 뒤 다시 뜨는지(hold 유지) 본다 — Stale 은 해제·종료.
func TestRebuildSnapshotFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		fail   func(w *rebuildWorld, t *testing.T, snapshot string, at time.Time) time.Time
		reason string
	}{
		{"status error", func(w *rebuildWorld, t *testing.T, snapshot string, at time.Time) time.Time {
			w.markSnapshot(t, snapshot, snapshotFailedStatus)
			return at.Add(statusPollInterval)
		}, ReasonSnapshotFailed},
		{"ready timeout", func(_ *rebuildWorld, _ *testing.T, _ string, at time.Time) time.Time {
			return at.Add(snapshotReadyTimeout + time.Second)
		}, ReasonSnapshotTimeout},
		{"snapshot deleted", func(w *rebuildWorld, t *testing.T, snapshot string, at time.Time) time.Time {
			snap := &unstructured.Unstructured{}
			snap.SetGroupVersionKind(volumeSnapshotGVK)
			snap.SetNamespace(runtimeTestNS)
			snap.SetName(snapshot)
			if err := w.c.Delete(context.Background(), snap); err != nil {
				t.Fatalf("delete snapshot: %v", err)
			}
			return at.Add(statusPollInterval)
		}, ReasonSnapshotFailed},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRebuildWorld(t, "snapfail-"+strconv.Itoa(i))
			target := graphMember(w.name, 1)
			w.request(t, target, rebuildRogue, rebuildT0)
			at := w.toSnapshot(t, rebuildT0)
			first := w.mustEntry(t, target)

			at = tc.fail(w, t, first.Snapshot, at)
			w.step(t, at)
			e := w.mustEntry(t, target)
			if e.Attempt != 1 || e.Snapshot != "" || e.Token == first.Token || e.Blocked != tc.reason || !w.held(t, target) {
				t.Fatalf("after failure: %+v hold=%v, want attempt 1, new token, blocked %s, hold kept", e, w.runtime(t).Hold, tc.reason)
			}

			// reseedCooldown 전엔 다시 뜨지 않고, 뒤엔 새 토큰으로 뜬다.
			w.step(t, at.Add(reseedCooldown-time.Second))
			if got := w.mustEntry(t, target).Snapshot; got != "" {
				t.Fatalf("retried inside reseedCooldown: %s", got)
			}
			w.step(t, at.Add(reseedCooldown))
			if got := w.mustEntry(t, target).Snapshot; got != memberSnapshotName(target, rebuildToken(rebuildT0, 1)) {
				t.Fatalf("retry snapshot = %q, want attempt-1 token", got)
			}
		})
	}

	t.Run("api absent", func(t *testing.T) {
		t.Parallel()
		w := newRebuildWorld(t, "snapfail-noapi")
		w.faults.noSnapshotAPI = true
		target := graphMember(w.name, 1)
		w.request(t, target, rebuildDemoted, rebuildT0)
		at := rebuildT0.Add(runtimePropagationWait)
		w.step(t, at)
		w.recreate(t, "uid-held", podHeld)
		for range 2 {
			at = at.Add(statusPollInterval)
			w.step(t, at)
		}
		if e := w.mustEntry(t, target); e.Blocked != ReasonSnapshotUnavailable || e.Attempt != 1 || !w.held(t, target) {
			t.Fatalf("without the snapshot API: %+v, want blocked %s attempt 1 held", e, ReasonSnapshotUnavailable)
		}
	})

	t.Run("stale is released", func(t *testing.T) {
		t.Parallel()
		w := newRebuildWorld(t, "snapfail-stale")
		target := graphMember(w.name, 1)
		w.exec.set(w.exec.ends, target, "t|"+rebuildTestBehind)
		w.request(t, target, rebuildStale, rebuildT0)
		at := w.toSnapshot(t, rebuildT0)
		w.markSnapshot(t, w.mustEntry(t, target).Snapshot, snapshotFailedStatus)
		w.step(t, at.Add(statusPollInterval))
		if _, active := w.entry(t, target); active || w.held(t, target) {
			t.Fatalf("Stale after a failed snapshot: active=%v hold=%v, want released and ended", active, w.runtime(t).Hold)
		}
	})
}

// TestRebuildRevalidation 은 재검증을 본다 — 대상이 CM primary 이거나 in recovery 로 그래프에 붙어 있으면
// RebuildCancelled · hold 해제 · 해제(파괴) 0. 멈추기 전에도 같은 검사를 한다.
func TestRebuildRevalidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		beforeStop  bool
		makePrimary bool
	}{
		{"runtime primary before release", false, true},
		{"replicating before release", false, false},
		{"runtime primary before stop", true, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRebuildWorld(t, "reval-"+strconv.Itoa(i))
			target := graphMember(w.name, 1)
			w.request(t, target, rebuildDemoted, rebuildT0)
			at := rebuildT0
			if !tc.beforeStop {
				at = w.toSnapshot(t, rebuildT0)
				w.markSnapshot(t, w.mustEntry(t, target).Snapshot, snapshotReadyStatus)
			}
			if tc.makePrimary {
				patchData(t, w.c, buildRuntimeConfigMap(w.cluster(t), rebuildTestShard, shardRuntime{
					PrimaryEndpoint: podDNS(w.name, target), PromotionEpoch: 2, ClusterInitialized: true, Hold: w.runtime(t).Hold,
				}))
			} else {
				if err := w.c.Delete(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: target, Namespace: runtimeTestNS}}); err != nil {
					t.Fatalf("delete held pod: %v", err)
				}
				w.recreate(t, "uid-serving", podServing)
				w.graph.linked[target] = true
			}
			deletes := w.log.count("delete pod/" + target)
			w.step(t, at.Add(runtimePropagationWait))

			if _, active := w.entry(t, target); active || w.held(t, target) {
				t.Fatalf("revalidation: active=%v hold=%v, want cancelled and released", active, w.runtime(t).Hold)
			}
			if n := w.log.count("delete pod/" + target); n != deletes {
				t.Fatalf("pod deletes during cancellation = %d, want %d", n, deletes)
			}
			if got := rebuildCount(w, rebuildDemoted, outcomeCancelled); got != 1 {
				t.Fatalf("member_rebuilds_total{Demoted,cancelled} = %v, want 1", got)
			}
			ledger := w.state(t).Ledger
			if len(ledger) != 1 || !ledger[0].Released.IsZero() || ledger[0].Outcome != outcomeCancelled {
				t.Fatalf("ledger = %+v, want one cancelled entry without a release", ledger)
			}
		})
	}
}

// memberSnapshot 은 GC 시험의 VolumeSnapshot 이다.
func memberSnapshot(name string, labels map[string]string, created time.Time) *unstructured.Unstructured {
	snap := &unstructured.Unstructured{}
	snap.SetGroupVersionKind(volumeSnapshotGVK)
	snap.SetNamespace(runtimeTestNS)
	snap.SetName(name)
	snap.SetLabels(labels)
	snap.SetCreationTimestamp(metav1.NewTime(created))
	return snap
}

func snapshotLabels(cluster, bearing string) map[string]string {
	labels := map[string]string{memberSnapshotLabel: labelTrue, clusterLabelKey: cluster}
	if bearing != "" {
		labels[dataBearingLabel] = bearing
	}
	return labels
}

// TestMemberSnapshotGC 는 GC 가 이 클러스터의 비 data-bearing 재구축 스냅숏 중 7일을 넘거나 상한(2 × members)
// 밖인 것만 지우는지 본다 — data-bearing(라벨이 false 가 아닌 것)·진행 중 항목·다른 클러스터·접두 밖·
// 수동(pg-preincident-primary-tl43)·pvc-snapshot-ops 산출물은 남는다.
func TestMemberSnapshotGC(t *testing.T) {
	t.Parallel()
	const name = "gc"
	old := rebuildT0.Add(-memberSnapshotRetention - time.Hour)
	const fresh = 8
	objs := make([]client.Object, 0, fresh+8)
	objs = append(objs,
		memberSnapshot("gc-shard-0-1-rebuild-old-0", snapshotLabels(name, labelFalse), old),
		memberSnapshot("gc-shard-0-1-rebuild-bearing-0", snapshotLabels(name, labelTrue), old),
		memberSnapshot("gc-shard-0-2-rebuild-unlabeled-0", snapshotLabels(name, ""), old),
		memberSnapshot("gc-shard-0-2-rebuild-active-0", snapshotLabels(name, labelFalse), old),
		memberSnapshot("other-shard-0-1-rebuild-x-0", snapshotLabels("other", labelFalse), old),
		memberSnapshot("gcx-shard-0-1-rebuild-x-0", snapshotLabels(name, labelFalse), old),
		memberSnapshot("pg-preincident-primary-tl43", map[string]string{}, old),
		memberSnapshot("gc-shard-0-0-ops-20260901", map[string]string{"app.kubernetes.io/name": "pvc-snapshot-ops", clusterLabelKey: name}, old),
	)
	// 상한 6(replicas 2) — 새 비 data-bearing 8개 중 오래된 2개가 상한 밖이다.
	for i := range fresh {
		objs = append(objs, memberSnapshot(fmt.Sprintf("gc-shard-0-1-rebuild-fresh-%d", i),
			snapshotLabels(name, labelFalse), rebuildT0.Add(-time.Duration(i+1)*time.Hour)))
	}
	w := newRebuildWorld(t, name, objs...)
	state := rebuildState{Version: rebuildStateVersion, Active: map[string]memberRebuild{
		graphMember(name, 2): {Pod: graphMember(name, 2), Shard: rebuildTestShard, Phase: rebuildSnapshotting,
			Snapshot: "gc-shard-0-2-rebuild-active-0", Since: rebuildT0, PhaseSince: rebuildT0},
	}}
	if err := w.r.saveRebuildState(context.Background(), w.cluster(t), state); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	w.r.gcMemberSnapshots(context.Background(), w.cluster(t), rebuildT0)

	snaps := w.snapshots(t)
	left := make([]string, 0, len(snaps))
	for _, snap := range snaps {
		left = append(left, snap.GetName())
	}
	slices.Sort(left)
	gone := []string{"gc-shard-0-1-rebuild-fresh-6", "gc-shard-0-1-rebuild-fresh-7", "gc-shard-0-1-rebuild-old-0"}
	for _, name := range gone {
		if slices.Contains(left, name) {
			t.Errorf("%s should have been collected", name)
		}
	}
	if want := len(objs) - len(gone); len(left) != want {
		t.Fatalf("snapshots left = %d %v, want %d", len(left), left, want)
	}
	if got := testutil.ToFloat64(MetricMemberSnapshots.WithLabelValues(runtimeTestNS, name, labelTrue)); got != 2 {
		t.Fatalf("member_snapshots{data_bearing=true} = %v, want 2 (labelled true and unlabelled)", got)
	}
	if got := testutil.ToFloat64(MetricMemberSnapshots.WithLabelValues(runtimeTestNS, name, labelFalse)); got != 7 {
		t.Fatalf("member_snapshots{data_bearing=false} = %v, want 7 (6 under the cap + active)", got)
	}
}

// TestMemberSnapshotGCRetention 은 상한 안이어도 7일을 넘은 비 data-bearing 스냅숏은 지우는지 본다.
func TestMemberSnapshotGCRetention(t *testing.T) {
	t.Parallel()
	const name = "gcret"
	w := newRebuildWorld(t, name,
		memberSnapshot("gcret-shard-0-1-rebuild-old-0", snapshotLabels(name, labelFalse), rebuildT0.Add(-memberSnapshotRetention-time.Second)),
		memberSnapshot("gcret-shard-0-1-rebuild-new-0", snapshotLabels(name, labelFalse), rebuildT0.Add(-memberSnapshotRetention+time.Hour)),
	)
	w.r.gcMemberSnapshots(context.Background(), w.cluster(t), rebuildT0)
	left := w.snapshots(t)
	if len(left) != 1 || left[0].GetName() != "gcret-shard-0-1-rebuild-new-0" {
		t.Fatalf("snapshots left = %d, want only the one inside the retention", len(left))
	}
}

// seriesPresent 는 레지스트리에 이름·라벨이 맞는 시계열이 있는지다(값을 만들지 않고 본다).
func seriesPresent(t *testing.T, name string, labels map[string]string) bool {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, m := range family.GetMetric() {
			matched := 0
			for _, pair := range m.GetLabel() {
				if labels[pair.GetName()] == pair.GetValue() {
					matched++
				}
			}
			if matched == len(labels) {
				return true
			}
		}
	}
	return false
}

// TestRebuildSeriesAndGauges 는 첫 걸음이 카운터 시계열을 0 으로 만들고(O1 increase), hold 게이지가 CM 목록을,
// 예산 게이지가 남은 해제 수를 싣고, 막힌 것이 없으면 condition 이 False/RebuildClear 인지 본다.
func TestRebuildSeriesAndGauges(t *testing.T) {
	t.Parallel()
	w := newRebuildWorld(t, "series")
	const family = "postgres_operator_postgrescluster_member_rebuilds_total"
	labels := map[string]string{"namespace": runtimeTestNS, "name": w.name, "shard": rebuildTestShard,
		"reason": string(rebuildDemoted), "outcome": string(outcomeDataBearing)}
	if seriesPresent(t, family, labels) {
		t.Fatal("series present before the first step")
	}
	cluster := w.step(t, rebuildT0)
	if !seriesPresent(t, family, labels) {
		t.Fatal("member_rebuilds_total{outcome=data_bearing} not initialised by the first step")
	}
	if cond := blockedCondition(cluster); cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonRebuildClear {
		t.Fatalf("MemberRebuildBlocked = %+v, want False/%s", cond, ReasonRebuildClear)
	}
	if got := testutil.ToFloat64(MetricMemberRebuildBudget.WithLabelValues(runtimeTestNS, w.name)); got != 2 {
		t.Fatalf("budget remaining = %v, want 2 (replicas)", got)
	}

	w.request(t, graphMember(w.name, 1), rebuildDemoted, rebuildT0)
	w.step(t, rebuildT0.Add(time.Second))
	if got := testutil.ToFloat64(MetricMembersHeld.WithLabelValues(runtimeTestNS, w.name, rebuildTestShard)); got != 1 {
		t.Fatalf("members_held = %v, want 1", got)
	}
}

// TestRebuildEngineInert 는 요청이 없으면 전체 Reconcile 이 엔진 쪽으로 아무것도 쓰지 않는지 본다 — 재구축
// 애노테이션 0 · 파드 삭제 0 · 스냅숏 0 · hold·재시드 키는 빈 값(위험 경로 불변).
func TestRebuildEngineInert(t *testing.T) {
	t.Parallel()
	const name = "inert"
	cluster, cm := runtimeCluster(name)
	log := &writeLog{}
	funcs := log.funcs()
	funcs.Delete = func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		log.add("delete " + rebuildKind(obj) + "/" + obj.GetName())
		return cli.Delete(ctx, obj, opts...)
	}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, cm, shardMember(name, 0, statusapi.RolePrimary), shardMember(name, 1, statusapi.RoleReplica)).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(funcs).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: promotingExecutor(log), MemberSnapshotClass: rebuildTestClass}
	defer DeleteMetricsFor(runtimeTestNS, name)

	reconcileCluster(t, r, cluster)
	reconcileCluster(t, r, cluster)

	var got postgresv1alpha1.PostgresCluster
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(cluster), &got); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	if raw, ok := got.Annotations[memberRebuildAnnotation]; ok {
		t.Fatalf("rebuild annotation written without a request: %s", raw)
	}
	if n := log.count("delete pod/") + log.count("create *unstructured.Unstructured/"); n != 0 {
		t.Fatalf("engine writes without a request = %d: %q", n, log.lines)
	}
	data := runtimeCM(t, c, name)
	if hold, reseed := data[runtimeKeyMemberHold], data[runtimeKeyReseedRequests]; hold != "" || reseed != "" {
		t.Fatalf("runtime hold=%q reseed=%q, want empty", hold, reseed)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, ConditionMemberRebuildBlocked)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("MemberRebuildBlocked = %+v, want False", cond)
	}
}

// TestRuntimeRebuildKeys 는 두 재구축 키가 늘 쓰이는지 본다 — 새 CM 에 빈 값, init env 가 Optional=false 로
// 참조(main 은 안 받음), 옛(3키) CM 에 채워 넣되 있는 값은 두고, 승격 발행이 hold·재시드를 덮지 않는다.
func TestRuntimeRebuildKeys(t *testing.T) {
	t.Parallel()
	cluster := haCluster("keys", postgresv1alpha1.ShardingModeNone)
	built := buildRuntimeConfigMap(cluster, rebuildTestShard, shardRuntime{
		Hold: []string{"b", "a", "a"}, Reseed: map[string]string{"z": "t-1", "a": "t-0"},
	})
	if got := built.Data[runtimeKeyMemberHold]; got != "a,b" {
		t.Fatalf("MEMBER_HOLD = %q, want a,b", got)
	}
	if got := built.Data[runtimeKeyReseedRequests]; got != "a=t-0,z=t-1" {
		t.Fatalf("RESEED_REQUESTS = %q, want a=t-0,z=t-1", got)
	}
	empty := buildRuntimeConfigMap(cluster, rebuildTestShard, shardRuntime{})
	for _, key := range memberRuntimeKeys {
		if value, ok := empty.Data[key]; !ok || value != "" {
			t.Fatalf("%s = %q (present %v), want always written empty", key, value, ok)
		}
	}
	if got := parseReseed("a=1,,broken,=x,b=2"); len(got) != 2 || got["a"] != "1" || got["b"] != "2" {
		t.Fatalf("parseReseed = %v, want a=1 b=2", got)
	}

	cmName := ShardRuntimeConfigMapName("keys", rebuildTestShard)
	sts := buildPGStatefulSet(cluster, ShardStatefulSetName("keys", 0), ShardServiceName("keys", 0), 0,
		"example.com/postgres:18", ShardConfigMapName("keys", 0), "18", 2,
		cluster.Spec.Shards.Storage, corev1.ResourceRequirements{}, cmName, "hash", "")
	assertRuntimeEnv(t, envByNameOf(sts.Spec.Template.Spec.InitContainers[0]), cmName, runtimeKeyMemberHold, runtimeKeyReseedRequests)
	for _, key := range memberRuntimeKeys {
		if _, ok := envByNameOf(sts.Spec.Template.Spec.Containers[0])[key]; ok {
			t.Errorf("main container must not carry %s", key)
		}
	}

	t.Run("fill old ConfigMap", func(t *testing.T) {
		t.Parallel()
		const name = "keys-fill"
		cluster, cm := runtimeCluster(name)
		delete(cm.Data, runtimeKeyReseedRequests)
		cm.Data[runtimeKeyMemberHold] = graphMember(name, 1)
		scheme := runtimeScheme(t)
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(cluster, cm, shardMember(name, 0, statusapi.RolePrimary), shardMember(name, 1, statusapi.RoleReplica)).
			WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).Build()
		r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: promotingExecutor(&writeLog{})}
		defer DeleteMetricsFor(runtimeTestNS, name)

		reconcileCluster(t, r, cluster)

		data := runtimeCM(t, c, name)
		if reseed, ok := data[runtimeKeyReseedRequests]; !ok || reseed != "" {
			t.Fatalf("RESEED_REQUESTS = %q (present %v), want filled empty", reseed, ok)
		}
		if got := data[runtimeKeyMemberHold]; got != graphMember(name, 1) {
			t.Fatalf("MEMBER_HOLD = %q, want the existing hold kept", got)
		}
	})

	t.Run("promotion keeps the hold", func(t *testing.T) {
		t.Parallel()
		const name = "keys-promote"
		target := graphMember(name, 1)
		cluster, cm := runtimeCluster(name)
		cm.Data[runtimeKeyMemberHold] = graphMember(name, 2)
		cm.Data[runtimeKeyReseedRequests] = graphMember(name, 2) + "=tok-0"
		scheme := runtimeScheme(t)
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(cluster, cm, readyPromotionPod(runtimeTestNS, target)).Build()
		r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: promotingExecutor(&writeLog{})}
		defer DeleteMetricsFor(runtimeTestNS, name)

		err := r.promoteAndPublish(context.Background(), cluster, rebuildTestShard,
			failover.PromotionTarget{ShardName: rebuildTestShard, Pod: target, Endpoint: podDNS(name, target)},
			graphMember(name, 0), promotionFailover)
		if err != nil {
			t.Fatalf("promoteAndPublish: %v", err)
		}
		data := runtimeCM(t, c, name)
		if data[runtimeKeyPrimaryEndpoint] != podDNS(name, target) {
			t.Fatalf("PRIMARY_ENDPOINT = %q, want the promoted target", data[runtimeKeyPrimaryEndpoint])
		}
		// failover 는 옛 primary(0)를 더할 뿐 기존 hold·재시드는 그대로다.
		wantHold := formatHold([]string{graphMember(name, 0), graphMember(name, 2)})
		if data[runtimeKeyMemberHold] != wantHold || data[runtimeKeyReseedRequests] != graphMember(name, 2)+"=tok-0" {
			t.Fatalf("promotion overwrote rebuild keys: hold=%q reseed=%q, want hold %q", data[runtimeKeyMemberHold],
				data[runtimeKeyReseedRequests], wantHold)
		}
	})
}

// TestParseLSN 은 LSN 표기를 절대 바이트로 읽고 되쓰는지 본다.
func TestParseLSN(t *testing.T) {
	t.Parallel()
	cases := map[string]uint64{"0/0": 0, "0/5000060": 0x5000060, "1A/5000060": 0x1A_05000060, "ffffffff/ffffffff": 1<<64 - 1}
	for lsn, want := range cases {
		got, err := parseLSN(lsn)
		if err != nil || got != want {
			t.Errorf("parseLSN(%q) = %#x, %v; want %#x", lsn, got, err, want)
		}
	}
	for _, bad := range []string{"", "0/", "x/1", "1/2/3", "123456789/0"} {
		if _, err := parseLSN(bad); err == nil {
			t.Errorf("parseLSN(%q) accepted a malformed LSN", bad)
		}
	}
	if got := formatLSN(0x1A_05000060); got != "1A/5000060" {
		t.Errorf("formatLSN = %q, want 1A/5000060", got)
	}
	if got, want := rebuildToken(rebuildT0, 1), strconv.FormatInt(rebuildT0.Unix(), rebuildTokenBase)+"-1"; got != want {
		t.Errorf("rebuildToken = %q, want %q", got, want)
	}
}
