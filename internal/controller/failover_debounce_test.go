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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// TestShouldPromoteAfterDebounce_RequiresSustainedFailure pins the #220 live-drill
// fix: a primary failure must persist for failoverDebounceThreshold before the
// operator promotes, so a single-reconcile status flicker (the spurious-promotion
// root cause that, via fenceNonTargetMembers, fenced a healthy member) is filtered.
func TestShouldPromoteAfterDebounce_RequiresSustainedFailure(t *testing.T) {
	t.Parallel()
	r := &PostgresClusterReconciler{}
	const key = "pg220/f220"
	t0 := time.Unix(1_900_000_000, 0).UTC()

	if r.shouldPromoteAfterDebounce(key, true, true, t0) {
		t.Fatal("must not promote on first failure detection (debounce window starts)")
	}
	if r.shouldPromoteAfterDebounce(key, true, true, t0.Add(failoverDebounceThreshold-time.Second)) {
		t.Fatal("must not promote while within the debounce window")
	}
	if !r.shouldPromoteAfterDebounce(key, true, true, t0.Add(failoverDebounceThreshold+time.Second)) {
		t.Fatal("must promote once the failure has persisted past the threshold")
	}
}

// TestShouldPromoteAfterDebounce_ContinuesAfterCanStartDrops mirrors the live
// failover sequence: the window starts while the cluster was Ready (canStart=true),
// and subsequent reconciles continue it even though the failure has since dropped
// the phase out of Ready (canStart=false). The original integration bug was that
// the phase==Ready gate was re-evaluated every reconcile, so the window never
// accumulated past the first detection.
func TestShouldPromoteAfterDebounce_ContinuesAfterCanStartDrops(t *testing.T) {
	t.Parallel()
	r := &PostgresClusterReconciler{}
	const key = "pg220/f220"
	t0 := time.Unix(1_900_000_000, 0).UTC()

	// Reconcile 1: cluster was Ready, failure just detected → start window.
	if r.shouldPromoteAfterDebounce(key, true, true, t0) {
		t.Fatal("must not promote on first detection")
	}
	// Reconcile 2: phase has dropped (canStart=false now), still within window.
	if r.shouldPromoteAfterDebounce(key, true, false, t0.Add(statusPollInterval)) {
		t.Fatal("must not promote mid-window")
	}
	// Reconcile 3: phase still not Ready (canStart=false), window now sustained.
	if !r.shouldPromoteAfterDebounce(key, true, false, t0.Add(failoverDebounceThreshold+time.Second)) {
		t.Fatal("must promote after sustained failure even though canStart is now false")
	}
}

// TestShouldPromoteAfterDebounce_StartGatedByCanStart verifies a failure observed
// while the cluster was never Ready (e.g. initial bootstrap churn) never starts a
// window, so no promotion happens no matter how long the failure persists.
func TestShouldPromoteAfterDebounce_StartGatedByCanStart(t *testing.T) {
	t.Parallel()
	r := &PostgresClusterReconciler{}
	const key = "pg220/f220"
	t0 := time.Unix(1_900_000_000, 0).UTC()

	if r.shouldPromoteAfterDebounce(key, true, false, t0) {
		t.Fatal("window must not start when canStart=false")
	}
	if r.shouldPromoteAfterDebounce(key, true, false, t0.Add(2*failoverDebounceThreshold)) {
		t.Fatal("with no window started, must never promote regardless of elapsed time")
	}
	// Once the cluster reaches Ready (canStart=true), the window starts.
	if r.shouldPromoteAfterDebounce(key, true, true, t0.Add(2*failoverDebounceThreshold+time.Second)) {
		t.Fatal("first detection with canStart=true starts the window, not promote")
	}
}

// TestShouldPromoteAfterDebounce_TransientFlickerFiltered verifies a failure that
// recovers within the window never triggers a promotion, and that a later genuine
// failure restarts the window from scratch (old elapsed time must not carry over).
func TestShouldPromoteAfterDebounce_TransientFlickerFiltered(t *testing.T) {
	t.Parallel()
	r := &PostgresClusterReconciler{}
	const key = "pg220/f220"
	t0 := time.Unix(1_900_000_000, 0).UTC()

	if r.shouldPromoteAfterDebounce(key, true, true, t0) {
		t.Fatal("flicker: must not promote on first detection")
	}
	if r.shouldPromoteAfterDebounce(key, false, true, t0.Add(2*time.Second)) {
		t.Fatal("recovery must never promote and must clear the window")
	}
	if r.shouldPromoteAfterDebounce(key, true, true, t0.Add(3*time.Second)) {
		t.Fatal("post-recovery failure must restart the window, not promote immediately")
	}
	if !r.shouldPromoteAfterDebounce(key, true, true, t0.Add(3*time.Second+failoverDebounceThreshold+time.Millisecond)) {
		t.Fatal("the restarted window must promote once it too is sustained")
	}
}

// TestShouldPromoteAfterDebounce_IndependentKeys ensures per-cluster windows do
// not interfere with one another.
func TestShouldPromoteAfterDebounce_IndependentKeys(t *testing.T) {
	t.Parallel()
	r := &PostgresClusterReconciler{}
	t0 := time.Unix(1_900_000_000, 0).UTC()

	r.shouldPromoteAfterDebounce("ns/a", true, true, t0)
	if r.shouldPromoteAfterDebounce("ns/b", true, true, t0.Add(failoverDebounceThreshold+time.Second)) {
		t.Fatal("cluster b must start its own window, not inherit cluster a's elapsed time")
	}
}

// 승격 시험의 exec 답 — history 의 마지막 분기점이 promotionLSN 이 된다.
const promotionTestHistory = "1\t0/3000160\tno recovery target specified\n"

// wideEventBuffer 는 승격이 되풀이될 수 있는 시나리오의 FakeRecorder 채널 크기다 — 넘치면 Eventf 가 막힌다.
const wideEventBuffer = 64

// promotingExecutor 는 승격 exec 에 promoted, history 에 분기점, pg_is_in_recovery 에 f(끊기 exec 도 primary)로 답한다.
func promotingExecutor(log *writeLog) *funcExecutor {
	return &funcExecutor{log: log, respond: func(_ BackupSidecarTarget, command []string) ([]byte, error) {
		joined := strings.Join(command, " ")
		switch {
		case strings.Contains(joined, cutResultPrefix):
			return []byte(cutResultPrefix + cutResultPrim + "\n"), nil
		case strings.Contains(joined, "pg_promote"):
			return []byte("PROMOTE_RESULT=promoted\n"), nil
		case strings.Contains(joined, "pg_read_file"):
			return []byte(promotionTestHistory), nil
		case strings.Contains(joined, recoveryQuery):
			return []byte(pgBoolFalse + "\n"), nil
		}
		return nil, nil
	}}
}

// runtimeCluster 는 CM primary 가 ordinal-0 인 epoch 1 샤드 하나의 클러스터와 그 런타임 CM 이다.
func runtimeCluster(name string) (*postgresv1alpha1.PostgresCluster, *corev1.ConfigMap) {
	primary := ShardStatefulSetName(name, 0) + "-0"
	cluster := withRecordedPrimary(withCanon(haCluster(name, postgresv1alpha1.ShardingModeNone), "shard-0",
		shardCanon{Epoch: 1, Initialized: true}), primary)
	cm := buildRuntimeConfigMap(cluster, "shard-0", shardRuntime{
		PrimaryEndpoint: podDNS(name, primary), PromotionEpoch: 1, ClusterInitialized: true,
	})
	return cluster, cm
}

// setPodReady 는 파드의 PodReady condition 을 바꾼다(kubelet 흉내 — status 하위 리소스).
func setPodReady(t *testing.T, c client.Client, pod string, status corev1.ConditionStatus) {
	t.Helper()
	var got corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: pod}, &got); err != nil {
		t.Fatalf("get pod %q: %v", pod, err)
	}
	got.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}
	if err := c.Status().Update(context.Background(), &got); err != nil {
		t.Fatalf("update pod %q status: %v", pod, err)
	}
}

// setPosition 은 상태 애노테이션의 절대 WAL 위치(replay)와 lag(receive − replay)를 쓴다.
func setPosition(t *testing.T, pod *corev1.Pod, wal, lag int64) {
	t.Helper()
	st, ok := parsePodStatus(pod)
	if !ok {
		t.Fatalf("pod %q has no instance status", pod.Name)
	}
	st.WALLSNBytes, st.LagBytes = wal, lag
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	pod.Annotations[statusapi.AnnotationKey] = string(raw)
}

// countEvents 는 채널에 쌓인 이벤트를 비우며 prefix 로 시작하는 것을 센다.
func countEvents(recorder *events.FakeRecorder, prefix string) int {
	n := 0
	for {
		select {
		case ev := <-recorder.Events:
			if strings.HasPrefix(ev, prefix) {
				n++
			}
		default:
			return n
		}
	}
}

// TestDebounceIsStaleThresh 는 디바운스가 하트비트 끊김 문턱(statusStaleThresh)과 같은지 본다
// (설계 §3.3). 8 s 는 primary 재시작보다 짧아 롤링 재시작마다 standby 를 승격했다(§2.1 루프).
func TestDebounceIsStaleThresh(t *testing.T) {
	t.Parallel()
	// 설계 §3.3 시험점 — 창 안 25 s 는 거짓, 창 밖 31 s 는 참.
	const (
		insideWindow  = 25 * time.Second
		outsideWindow = 31 * time.Second
	)
	if failoverDebounceThreshold != statusStaleThresh {
		t.Fatalf("failoverDebounceThreshold = %s, want statusStaleThresh %s", failoverDebounceThreshold, statusStaleThresh)
	}
	r := &PostgresClusterReconciler{}
	const key = "ns/debounce"
	t0 := time.Unix(1_900_000_000, 0).UTC()

	r.shouldPromoteAfterDebounce(key, true, true, t0)
	if r.shouldPromoteAfterDebounce(key, true, true, t0.Add(insideWindow)) {
		t.Fatalf("promoted %s into a primary failure", insideWindow)
	}
	if !r.shouldPromoteAfterDebounce(key, true, true, t0.Add(outsideWindow)) {
		t.Fatalf("not promoted %s into a primary failure", outsideWindow)
	}
}

// TestHeartbeatOnlyWithheld 는 CM primary 의 하트비트만 낡고 PodReady=True 면 60 s 동안 승격 0 이고
// PrimaryHeartbeatStale 은 statusStaleThresh 에 한 번인지, PodReady=False 가 되면 31 s 안에 승격이
// 정확히 한 번인지 본다(설계 §3.3 — 09-23 API 공백은 하트비트만 낡게 했다).
func TestHeartbeatOnlyWithheld(t *testing.T) {
	t.Parallel()
	const name = "hb-only"
	ctx := context.Background()
	t0 := time.Now()
	cluster, cm := runtimeCluster(name)
	primary := shardMember(name, 0, statusapi.RolePrimary)
	setMemberRole(primary, ShardServiceName(name, 0), statusapi.RolePrimary, t0.Add(-statusStaleThresh-time.Second))
	standby := shardMember(name, 1, statusapi.RoleReplica)
	log := &writeLog{}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, cm, primary, standby).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(log.funcs()).Build()
	recorder := events.NewFakeRecorder(wideEventBuffer)
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: promotingExecutor(log), Recorder: recorder}
	defer DeleteMetricsFor(runtimeTestNS, name)

	// aggregate 가 본 모습 — 하트비트가 낡은 primary 는 Ready=false, standby 는 Ready.
	shards := []postgresv1alpha1.ShardStatus{{
		Name:     "shard-0",
		Primary:  &postgresv1alpha1.ShardEndpoint{Pod: primary.Name, Endpoint: podDNS(name, primary.Name)},
		Replicas: []postgresv1alpha1.ShardEndpoint{{Pod: standby.Name, Endpoint: podDNS(name, standby.Name), Ready: true}},
	}}
	run := func(from time.Time, span time.Duration) {
		for at := time.Duration(0); at <= span; at += statusPollInterval {
			r.reconcileFailover(ctx, cluster, shards, postgresv1alpha1.ClusterPhaseReady, from.Add(at))
		}
	}

	// 하트비트만 낡음(PodReady=True) — 60 s 동안 승격 0.
	withheld := 2 * statusStaleThresh
	run(t0, withheld)
	if n := log.count("exec promote "); n != 0 {
		t.Fatalf("promotions with only a stale heartbeat = %d, want 0: %q", n, log.lines)
	}
	wantWarnings := int(withheld/statusStaleThresh) + 1
	if n := countEvents(recorder, "Warning "+primaryHeartbeatStaleReason+" "); n != wantWarnings {
		t.Fatalf("%s events over %s = %d, want %d (one per statusStaleThresh)", primaryHeartbeatStaleReason, withheld, n, wantWarnings)
	}

	// PodReady=False(노드·프로세스 사망) — 31 s 안에 승격 정확히 1.
	setPodReady(t, c, primary.Name, corev1.ConditionFalse)
	run(t0.Add(withheld+statusPollInterval), 31*time.Second)
	if n := log.count("exec promote " + standby.Name); n != 1 {
		t.Fatalf("promotions after PodReady=False = %d, want 1: %q", n, log.lines)
	}
}

// TestHeartbeatOnly 는 하트비트만 낡은 판정의 조건을 하나씩 본다 — 런타임 CM primary 파드가
// 있고 PodReady=True 이고 하트비트만 낡았고 Role≠Stopping 일 때만 참이다.
func TestHeartbeatOnly(t *testing.T) {
	t.Parallel()
	const name = "hb-cases"
	now := time.Now()
	stale := now.Add(-statusStaleThresh - time.Second)
	cases := []struct {
		name     string
		mutate   func(*corev1.Pod)
		noCM     bool
		noPod    bool
		withhold bool
	}{
		{name: "stale heartbeat, pod ready", withhold: true},
		{name: "fresh heartbeat", mutate: func(p *corev1.Pod) {
			setMemberRole(p, ShardServiceName(name, 0), statusapi.RolePrimary, now)
		}},
		{name: "pod not ready", mutate: func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
		}},
		{name: "stopping", mutate: func(p *corev1.Pod) {
			setMemberRole(p, ShardServiceName(name, 0), statusapi.RoleStopping, stale)
		}},
		{name: "no heartbeat", mutate: func(p *corev1.Pod) {
			delete(p.Annotations, statusapi.AnnotationKey)
		}},
		{name: "no runtime ConfigMap", noCM: true},
		{name: "runtime primary pod gone", noPod: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cluster, cm := runtimeCluster(name)
			primary := shardMember(name, 0, statusapi.RolePrimary)
			setMemberRole(primary, ShardServiceName(name, 0), statusapi.RolePrimary, stale)
			if tc.mutate != nil {
				tc.mutate(primary)
			}
			objs := []client.Object{cluster}
			if !tc.noCM {
				objs = append(objs, cm)
			}
			if !tc.noPod {
				objs = append(objs, primary)
			}
			scheme := runtimeScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
			recorder := events.NewFakeRecorder(eventBuffer)
			r := &PostgresClusterReconciler{Client: c, Scheme: scheme, Recorder: recorder}

			if got := r.heartbeatOnly(context.Background(), cluster, "shard-0", now); got != tc.withhold {
				t.Fatalf("heartbeatOnly = %v, want %v", got, tc.withhold)
			}
			wantEvents := 0
			if tc.withhold {
				wantEvents = 1
			}
			if n := countEvents(recorder, "Warning "+primaryHeartbeatStaleReason+" "); n != wantEvents {
				t.Fatalf("%s events = %d, want %d", primaryHeartbeatStaleReason, n, wantEvents)
			}
		})
	}
}

// TestPromotingReconcileEnds 는 승격한 reconcile 이 failoverPending 을 지우고 requeue 로 끝나는지
// 본다(설계 §3.3) — 그 reconcile 의 aggregate·fence 맵은 승격 전 값이라 라벨·Service·재시드를
// 하지 않는다.
func TestPromotingReconcileEnds(t *testing.T) {
	t.Parallel()
	const name = "promote-ends"
	cluster, cm := runtimeCluster(name)
	cluster.Status.Phase = postgresv1alpha1.ClusterPhaseReady
	survivor := shardMember(name, 1, statusapi.RoleReplica)
	log := &writeLog{}
	funcs := log.funcs()
	funcs.Delete = func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		log.add("delete " + objKind(obj) + "/" + obj.GetName())
		return cli.Delete(ctx, obj, opts...)
	}
	scheme := runtimeScheme(t)
	// 옛 primary 파드는 없다(노드 사망 뒤 지워짐) — 승격 경로의 옛 primary 재시드는 지울 것이 없다.
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, cm, survivor).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(funcs).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: promotingExecutor(log)}
	defer DeleteMetricsFor(runtimeTestNS, name)
	key := runtimeTestNS + "/" + name
	r.failoverPending = map[string]time.Time{key: time.Now().Add(-failoverDebounceThreshold - time.Second)}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cluster)})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	publish := log.first("patch cm/" + cm.Name)
	if log.count("exec promote "+survivor.Name) != 1 || publish < 0 {
		t.Fatalf("want one promotion of %s published to the runtime CM, got %q", survivor.Name, log.lines)
	}
	for _, line := range log.lines[publish+1:] {
		for _, write := range []string{"patch *v1.Pod/", "create *v1.Service/", "update *v1.Service/", "delete "} {
			if strings.HasPrefix(line, write) {
				t.Errorf("write after the promotion %q — labels, Services and reseeds wait for the next reconcile: %q",
					line, log.lines[publish:])
			}
		}
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("result = %+v, want a requeue", res)
	}
	r.failoverPendingMu.Lock()
	defer r.failoverPendingMu.Unlock()
	if since, ok := r.failoverPending[key]; ok {
		t.Errorf("failoverPending[%s] = %s kept after the promotion, want deleted", key, since)
	}
}

// TestPromotionRecorded 는 실행된 승격이 promotions_total{kind} 를 1 올리고 PromotionExecuted
// (shard·pod·epoch·kind)를 남기는지 본다(설계 §3.9 — O1 PostgresFailoverStorm 이 읽는다).
func TestPromotionRecorded(t *testing.T) {
	t.Parallel()
	for _, kind := range []promotionKind{promotionFailover, promotionSwitchover} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			name := "recorded-" + string(kind)
			cluster, cm := runtimeCluster(name)
			target := ShardStatefulSetName(name, 0) + "-1"
			log := &writeLog{}
			scheme := runtimeScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(cluster, cm, readyPromotionPod(runtimeTestNS, target)).Build()
			recorder := events.NewFakeRecorder(eventBuffer)
			r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: promotingExecutor(log), Recorder: recorder}
			defer DeleteMetricsFor(runtimeTestNS, name)
			counter := MetricPromotions.WithLabelValues(runtimeTestNS, name, "shard-0", string(kind))
			before := testutil.ToFloat64(counter)

			promoted := failover.PromotionTarget{ShardName: "shard-0", Pod: target, Endpoint: podDNS(name, target)}
			if err := r.promoteAndPublish(context.Background(), cluster, "shard-0", promoted, "", kind); err != nil {
				t.Fatalf("promoteAndPublish: %v", err)
			}

			if got := testutil.ToFloat64(counter) - before; got != 1 {
				t.Fatalf("promotions_total{kind=%s} delta = %v, want 1", kind, got)
			}
			want := fmt.Sprintf("Normal %s shard=shard-0 pod=%s epoch=2 kind=%s", promotionExecutedReason, target, kind)
			if n := countEvents(recorder, want); n != 1 {
				t.Fatalf("events %q = %d, want 1", want, n)
			}
		})
	}
}

// TestStartupSeriesAtZero 는 O1 이 읽는 api_read_denied_total{kind=Node} 가 기동 때부터 있는지
// 본다 — 첫 거부 전에 시계열이 없으면 increase() 가 첫 증분을 놓친다(설계 §3.9). 병렬 금지 —
// 전역 레지스트리를 읽는다(병렬 시험은 순차 시험이 끝난 뒤 돈다).
func TestStartupSeriesAtZero(t *testing.T) {
	family := prometheus.BuildFQName(metricNamespace, metricSubsystem, "api_read_denied_total")
	n, err := testutil.GatherAndCount(metrics.Registry, family)
	if err != nil {
		t.Fatalf("gather %s: %v", family, err)
	}
	if n == 0 {
		t.Fatalf("%s{kind=%s} absent at startup, want a zero series", family, apiReadKindNode)
	}
	counter := MetricAPIReadDenied.WithLabelValues(apiReadKindNode)
	before := testutil.ToFloat64(counter)
	initSeries()
	if got := testutil.ToFloat64(counter); got != before {
		t.Fatalf("initSeries moved %s{kind=%s} %v → %v, want it to only create the series", family, apiReadKindNode, before, got)
	}
}

// TestReceivePositions 는 수신 위치 = 상태 애노테이션 WALLSNBytes(replay) + LagBytes 이고, 모르는
// 멤버(애노테이션 없음 · 미관측 -1 · 필드 없는 옛 인스턴스 0)는 빠지는지 본다.
func TestReceivePositions(t *testing.T) {
	t.Parallel()
	const (
		name = "receive"
		gib  = int64(1) << 30
	)
	streaming := shardMember(name, 0, statusapi.RoleReplica)
	setPosition(t, streaming, 177*gib, 4096)
	disconnected := shardMember(name, 1, statusapi.RoleReplica)
	setPosition(t, disconnected, 175*gib, 0)
	unobserved := shardMember(name, 2, statusapi.RoleReplica)
	setPosition(t, unobserved, -1, -1)
	legacy := shardMember(name, 3, statusapi.RoleReplica)
	silent := shardMember(name, 4, statusapi.RoleReplica)
	delete(silent.Annotations, statusapi.AnnotationKey)
	scheme := runtimeScheme(t)
	cluster := haCluster(name, postgresv1alpha1.ShardingModeNone)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, streaming, disconnected, unobserved, legacy, silent).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	got := r.receivePositions(context.Background(), cluster, postgresv1alpha1.ShardStatus{Name: "shard-0"})

	want := map[string]int64{streaming.Name: 177*gib + 4096, disconnected.Name: 175 * gib}
	if len(got) != len(want) {
		t.Fatalf("receive positions = %v, want %v", got, want)
	}
	for pod, pos := range want {
		if got[pod] != pos {
			t.Fatalf("receive positions = %v, want %v", got, want)
		}
	}
}
