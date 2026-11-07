/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

// 본 파일은 샤드 단계화 롤아웃(설계 2026-09-25 §3.16, ADR-0033)을 본다 — STS 는 OnDelete 이고, 오퍼레이터가 옛
// replica 를 하나씩(streaming 아닌 것 → 서수 높은 것) 지우고, 대상 밖 replica 가 Ready ∧ streaming 일 때만 다음으로
// 가고, 옛 replica 가 없으면 switchover(Rollout) 뒤 옛 primary 를 교체하는지. 선결이 memberConvergeBudget 넘게 안
// 서면 RolloutBlocked 와 Warning 한 번, 끝나면 ShardRolloutCompleted 한 번. STS 컨트롤러·kubelet 은 시험이 흉내 낸다.

const (
	// rolloutMembers 는 시험 샤드 멤버 수다 — primary 1 + replica 2.
	rolloutMembers = 3
	// oldRevision·newRevision 은 시험 STS 의 옛·새 controller-revision-hash 다.
	oldRevision = "rollout-old"
	newRevision = "rollout-new"
	// slotsFullError 는 연결 슬롯이 없을 때 psql 이 내는 오류다(PG 원문).
	slotsFullError = `psql: error: connection to server on socket "/var/run/postgresql/.s.PGSQL.5432" failed: ` +
		`FATAL:  sorry, too many clients already`
	// settingsCommand 는 롤아웃 설정 exec 를 가르는 글자다.
	settingsCommand = "pg_settings"
)

// primaryDefaults 는 primary 의 현재 설정이다 — PG 기본값과 렌더한 max_wal_senders. wal_keep_size 는 MB 다.
func primaryDefaults() map[string]string {
	return map[string]string{
		"wal_keep_size":             "0",
		"max_connections":           "100",
		"max_worker_processes":      "8",
		"max_wal_senders":           "10",
		"max_prepared_transactions": "0",
		"max_locks_per_transaction": "64",
	}
}

// rolloutExecutor 는 switchover 흉내(멤버별 postgres)에 설정 질의와 연쇄 standby 를 더한다. slotsFull 이면 모든
// psql 이 연결 슬롯이 없어 실패하고, settingsDown 이면 설정 질의만 실패한다.
type rolloutExecutor struct {
	*switchoverExecutor
	settings     map[string]string // pg_settings 이름 → setting(모든 멤버가 같다)
	slotsFull    bool
	settingsDown bool
	cascade      map[string]string // standby sender → 그 pg_stat_replication 출력
	graphDown    map[string]bool   // sender → pg_stat_replication exec 실패
}

func (x *rolloutExecutor) Exec(ctx context.Context, target BackupSidecarTarget, command []string) ([]byte, error) {
	joined := strings.Join(command, " ")
	x.mu.Lock()
	full, down, settings := x.slotsFull, x.settingsDown, maps.Clone(x.settings)
	rows, cascades := x.cascade[target.Pod]
	broken := x.graphDown[target.Pod]
	x.mu.Unlock()
	if full && strings.Contains(joined, "psql") {
		x.log.add("exec refused " + target.Pod)
		return nil, fmt.Errorf("exec %s: command terminated with exit code 2: %s", target.Pod, slotsFullError)
	}
	if strings.Contains(joined, "pg_stat_replication") && (broken || cascades) {
		x.log.add("exec graph " + target.Pod)
		if broken {
			return nil, fmt.Errorf("exec %s: connection reset", target.Pod)
		}
		return []byte(rows), nil
	}
	if !strings.Contains(joined, settingsCommand) {
		return x.switchoverExecutor.Exec(ctx, target, command)
	}
	x.log.add("exec settings " + target.Pod)
	if down {
		return nil, fmt.Errorf("exec %s: command terminated with exit code 2: psql: error: server closed the connection", target.Pod)
	}
	var out strings.Builder
	for _, name := range slices.Sorted(maps.Keys(settings)) {
		out.WriteString(name + psqlFieldSeparator + settings[name] + "\n")
	}
	return []byte(out.String()), nil
}

func (x *rolloutExecutor) set(name, value string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.settings[name] = value
}

func (x *rolloutExecutor) refuse(full bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.slotsFull = full
}

// chain 은 standby sender 의 pg_stat_replication 이다 — broken 이면 그 exec 가 실패한다.
func (x *rolloutExecutor) chain(sender, rows string, broken bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.cascade[sender] = rows
	x.graphDown[sender] = broken
}

// rolloutWorld 는 OnDelete STS(updateRevision = 새 revision) 위 세 멤버 샤드다 — 파드는 옛 revision, primary 는
// 주어진 서수, 나머지는 primary 에 streaming 이다.
type rolloutWorld struct {
	*switchoverWorld
	pg       *rolloutExecutor
	primary  string            // 지금 CM primary — pg_stat_replication 을 답한다
	states   map[string]string // primary 의 pg_stat_replication(멤버 → state)
	replaced map[string]int    // 멤버 → 다시 만든 횟수(UID)
}

// rolloutSTS 는 새 템플릿의 OnDelete STS 다 — STS 컨트롤러가 새 revision 을 봤고 파드는 아직 옛 것이다.
func rolloutSTS(cluster *postgresv1alpha1.PostgresCluster) *appsv1.StatefulSet {
	sts := buildPGStatefulSet(
		cluster, ShardStatefulSetName(cluster.Name, 0), ShardServiceName(cluster.Name, 0), 0,
		"example.com/postgres:18", ShardConfigMapName(cluster.Name, 0), "18", rolloutMembers,
		cluster.Spec.Shards.Storage, corev1.ResourceRequirements{},
		ShardRuntimeConfigMapName(cluster.Name, "shard-0"), "new-hash", "",
	)
	sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	sts.Status = appsv1.StatefulSetStatus{
		Replicas: rolloutMembers, ReadyReplicas: rolloutMembers,
		CurrentRevision: oldRevision, UpdateRevision: newRevision,
	}
	return sts
}

// rolloutPod 는 revision 을 단 멤버 파드다(수신 위치 = 기준 LSN).
func rolloutPod(t *testing.T, name string, ordinal int, role statusapi.Role, revision string, generation int) *corev1.Pod {
	t.Helper()
	p := shardMember(name, ordinal, role)
	p.Labels[appsv1.StatefulSetRevisionLabel] = revision
	p.UID = types.UID(fmt.Sprintf("%s-uid-%d", p.Name, generation))
	p.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	setPosition(t, p, int64(switchoverBaseLSN), 0)
	return p
}

// newRolloutWorld 는 세계를 만든다 — mutate 는 클러스터를 만들기 전에 고친다(애노테이션 심기).
func newRolloutWorld(t *testing.T, name string, primary int, mutate ...func(*postgresv1alpha1.PostgresCluster)) *rolloutWorld {
	t.Helper()
	primaryPod := graphMember(name, primary)
	cluster := withRecordedPrimary(withCanon(haCluster(name, postgresv1alpha1.ShardingModeNone),
		shardCanon{Epoch: 1, Initialized: true}), primaryPod)
	cluster.Spec.Shards.Replicas = rolloutMembers - 1
	cluster.Status.Phase = postgresv1alpha1.ClusterPhaseReady
	for _, m := range mutate {
		m(cluster)
	}
	runtimeCM := buildRuntimeConfigMap(cluster, "shard-0", shardRuntime{
		PrimaryEndpoint: podDNS(name, primaryPod), PromotionEpoch: 1, ClusterInitialized: true,
	})
	conf := buildConfigMap(cluster, ShardConfigMapName(name, 0), "shard", 0, nil)

	log := &writeLog{}
	sim := newSwitchoverExecutor(log)
	objs := slices.Grow([]client.Object{cluster, runtimeCM, conf, rolloutSTS(cluster)}, 2*rolloutMembers)
	states := map[string]string{}
	for i := range rolloutMembers {
		pod := graphMember(name, i)
		role, simRole := statusapi.RoleReplica, simStandby
		if i == primary {
			role, simRole = statusapi.RolePrimary, simPrimary
		} else {
			states[pod] = walsenderStreaming
		}
		p := rolloutPod(t, name, i, role, oldRevision, 0)
		sim.members[pod] = &simMember{role: simRole, lsn: switchoverBaseLSN}
		if i == primary {
			sim.members[pod].marker = "1"
			withMarker(t, p)
		}
		objs = append(objs, p, memberPVC(pod, false))
	}

	cms := &cmHistory{}
	scheme := runtimeScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(phaseLog(log, cms.watch(logDeletes(log, log.funcs())))).Build()
	recorder := events.NewFakeRecorder(wideEventBuffer)
	pg := &rolloutExecutor{
		switchoverExecutor: sim, settings: primaryDefaults(), cascade: map[string]string{}, graphDown: map[string]bool{},
	}
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: pg, Recorder: recorder}
	t.Cleanup(func() { DeleteMetricsFor(runtimeTestNS, name) })

	sw := &switchoverWorld{name: name, c: c, r: r, exec: sim, log: log, cms: cms, recorder: recorder, t0: time.Now()}
	w := &rolloutWorld{switchoverWorld: sw, pg: pg, primary: primaryPod, states: states, replaced: map[string]int{}}
	w.writeRows()
	return w
}

// writeRows 는 primary 의 pg_stat_replication 을 states 로 다시 쓴다.
func (w *rolloutWorld) writeRows() {
	var rows strings.Builder
	for _, member := range slices.Sorted(maps.Keys(w.states)) {
		rows.WriteString(walRow(member, w.states[member]))
	}
	w.exec.mu.Lock()
	defer w.exec.mu.Unlock()
	w.exec.rows = map[string]string{w.primary: rows.String()}
}

// setState 는 primary 가 보는 멤버의 walsender state 다 — 빈 문자열은 행 없음이다.
func (w *rolloutWorld) setState(member, state string) {
	if state == "" {
		delete(w.states, member)
	} else {
		w.states[member] = state
	}
	w.writeRows()
}

// roll 은 가짜 시계 now 의 그래프 관측 → advanceRollouts 한 걸음이다 — condition 은 status 에 저장한다.
func (w *rolloutWorld) roll(t *testing.T, now time.Time) {
	t.Helper()
	ctx := context.Background()
	cluster := w.cluster(t)
	shards := w.shards(cluster)
	graphs := w.r.observeGraphs(ctx, cluster, shards)
	w.r.advanceRollouts(ctx, cluster, shards, graphs, now)
	if err := w.c.Status().Update(ctx, cluster); err != nil {
		t.Fatalf("status update: %v", err)
	}
	w.drain()
}

// deleted 는 오퍼레이터가 지운 멤버 파드다(순서대로).
func (w *rolloutWorld) deleted() []string {
	w.log.mu.Lock()
	defer w.log.mu.Unlock()
	var out []string
	for _, line := range w.log.lines {
		if pod, ok := strings.CutPrefix(line, podDeletePrefix); ok {
			out = append(out, pod)
		}
	}
	return out
}

// assertDeleted 는 지운 멤버가 서수 순서 want 인지 본다.
func (w *rolloutWorld) assertDeleted(t *testing.T, want ...int) {
	t.Helper()
	names := make([]string, 0, len(want))
	for _, i := range want {
		names = append(names, w.member(i))
	}
	if got := w.deleted(); !slices.Equal(got, names) {
		t.Fatalf("deleted members = %q, want %q; log %q", got, names, w.log.lines)
	}
}

// replace 는 STS 컨트롤러·kubelet 흉내다 — 오퍼레이터가 지운 멤버를 새 revision standby 로 다시 만든다. ready 면
// PodReady 이고 primary 에 streaming 한다, 아니면 아직 뜨는 중이다.
func (w *rolloutWorld) replace(t *testing.T, ordinal int, ready bool) {
	t.Helper()
	name := w.member(ordinal)
	var old corev1.Pod
	err := w.c.Get(context.Background(), client.ObjectKey{Namespace: runtimeTestNS, Name: name}, &old)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("%s still exists (err %v); the operator did not delete it: %q", name, err, w.log.lines)
	}
	w.replaced[name]++
	p := rolloutPod(t, w.name, ordinal, statusapi.RoleReplica, newRevision, w.replaced[name])
	if !ready {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
		p.Status.ContainerStatuses[0].Ready = false
	}
	if err := w.c.Create(context.Background(), p); err != nil {
		t.Fatalf("recreate %s: %v", name, err)
	}
	w.exec.set(name, simStandby, w.exec.member(w.primary).lsn)
	w.syncUpdated(t)
	state := ""
	if ready {
		state = walsenderStreaming
	}
	w.setState(name, state)
}

// settle 은 다시 만든 멤버가 떠서 Ready 이고 primary 에 streaming 하는 상태다(수신 위치 = 기준 LSN).
func (w *rolloutWorld) settle(t *testing.T, ordinal int) {
	t.Helper()
	w.report(t, w.member(ordinal), statusapi.RoleReplica, corev1.ConditionTrue, 0)
	w.position(t, w.member(ordinal), switchoverBaseLSN)
	w.setState(w.member(ordinal), walsenderStreaming)
}

// revise 는 멤버 파드를 새 revision 으로 둔다(앞서 교체된 멤버 흉내).
func (w *rolloutWorld) revise(t *testing.T, ordinal int) {
	t.Helper()
	pod := w.pod(t, w.member(ordinal))
	pod.Labels[appsv1.StatefulSetRevisionLabel] = newRevision
	if err := w.c.Update(context.Background(), pod); err != nil {
		t.Fatalf("revise %s: %v", pod.Name, err)
	}
	w.syncUpdated(t)
}

// syncUpdated 는 STS status.updatedReplicas 를 새 revision 파드 수로 맞춘다(STS 컨트롤러 흉내).
func (w *rolloutWorld) syncUpdated(t *testing.T) {
	t.Helper()
	var pods corev1.PodList
	if err := w.c.List(context.Background(), &pods, client.InNamespace(runtimeTestNS)); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	n := int32(0)
	for i := range pods.Items {
		if pods.Items[i].Labels[appsv1.StatefulSetRevisionLabel] == newRevision {
			n++
		}
	}
	w.setUpdated(t, n)
}

func (w *rolloutWorld) setUpdated(t *testing.T, n int32) {
	t.Helper()
	var sts appsv1.StatefulSet
	key := client.ObjectKey{Namespace: runtimeTestNS, Name: ShardStatefulSetName(w.name, 0)}
	if err := w.c.Get(context.Background(), key, &sts); err != nil {
		t.Fatalf("get sts: %v", err)
	}
	sts.Status.UpdatedReplicas = n
	if err := w.c.Status().Update(context.Background(), &sts); err != nil {
		t.Fatalf("update sts status: %v", err)
	}
}

// promoted 는 switchover 뒤다 — 승자가 승격 마커와 함께 primary 를 보고하고 나머지가 그것에 streaming 한다.
func (w *rolloutWorld) promoted(t *testing.T, ordinal int) {
	t.Helper()
	winner := w.member(ordinal)
	w.report(t, winner, statusapi.RolePrimary, corev1.ConditionTrue, 0)
	reportMarker(t, w.c, winner)
	w.primary = winner
	w.states = map[string]string{}
	for i := range rolloutMembers {
		if i != ordinal {
			w.states[w.member(i)] = walsenderStreaming
		}
	}
	w.writeRows()
}

// appendConf 는 멤버가 마운트하는 postgresql.conf 에 줄을 더한다(뒤 줄이 이긴다).
func (w *rolloutWorld) appendConf(t *testing.T, line string) {
	t.Helper()
	var cm corev1.ConfigMap
	key := client.ObjectKey{Namespace: runtimeTestNS, Name: ShardConfigMapName(w.name, 0)}
	if err := w.c.Get(context.Background(), key, &cm); err != nil {
		t.Fatalf("get shard ConfigMap: %v", err)
	}
	cm.Data["postgresql.conf"] += line + "\n"
	if err := w.c.Update(context.Background(), &cm); err != nil {
		t.Fatalf("update shard ConfigMap: %v", err)
	}
}

// pullFails 는 멤버 컨테이너가 이미지를 못 받아 기다리는 상태다.
func (w *rolloutWorld) pullFails(t *testing.T, ordinal int) {
	t.Helper()
	pod := w.pod(t, w.member(ordinal))
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: pgContainerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
	}}
	if err := w.c.Status().Update(context.Background(), pod); err != nil {
		t.Fatalf("update %s status: %v", pod.Name, err)
	}
}

// clearAnnotation 은 CR 애노테이션을 지운다(재구축·switchover 가 끝난 흉내).
func (w *rolloutWorld) clearAnnotation(t *testing.T, key string) {
	t.Helper()
	cluster := w.cluster(t)
	before := cluster.DeepCopy()
	delete(cluster.Annotations, key)
	if err := w.c.Patch(context.Background(), cluster, client.MergeFrom(before)); err != nil {
		t.Fatalf("clear %s: %v", key, err)
	}
}

// forgetLog 는 시험 준비가 남긴 쓰기 기록을 지운다 — 이후 삭제만 오퍼레이터 것이다.
func (w *rolloutWorld) forgetLog() {
	w.log.mu.Lock()
	defer w.log.mu.Unlock()
	w.log.lines = nil
}

// editSTS 는 시험 STS 의 spec(Update)과 status(Status().Update)를 고친다.
func (w *rolloutWorld) editSTS(t *testing.T, spec, status func(*appsv1.StatefulSet)) {
	t.Helper()
	ctx := context.Background()
	key := client.ObjectKey{Namespace: runtimeTestNS, Name: ShardStatefulSetName(w.name, 0)}
	var sts appsv1.StatefulSet
	if err := w.c.Get(ctx, key, &sts); err != nil {
		t.Fatalf("get sts: %v", err)
	}
	spec(&sts)
	if err := w.c.Update(ctx, &sts); err != nil {
		t.Fatalf("update sts: %v", err)
	}
	if err := w.c.Get(ctx, key, &sts); err != nil {
		t.Fatalf("get sts: %v", err)
	}
	status(&sts)
	if err := w.c.Status().Update(ctx, &sts); err != nil {
		t.Fatalf("update sts status: %v", err)
	}
}

// shrink 는 샤드를 members 멤버로 줄인다 — 넘는 서수의 파드와 행을 지우고 STS replicas 를 맞춘다.
func (w *rolloutWorld) shrink(t *testing.T, members int32) {
	t.Helper()
	for i := int(members); i < rolloutMembers; i++ {
		if err := w.c.Delete(context.Background(), w.pod(t, w.member(i))); err != nil {
			t.Fatalf("delete %s: %v", w.member(i), err)
		}
		w.setState(w.member(i), "")
	}
	w.editSTS(t, func(sts *appsv1.StatefulSet) { sts.Spec.Replicas = &members },
		func(sts *appsv1.StatefulSet) { sts.Status.Replicas, sts.Status.ReadyReplicas = members, members })
	w.forgetLog()
}

// condition 은 RolloutBlocked condition 이다.
func (w *rolloutWorld) condition(t *testing.T) *metav1.Condition {
	t.Helper()
	return meta.FindStatusCondition(w.cluster(t).Status.Conditions, ConditionRolloutBlocked)
}

// assertBlocked 는 RolloutBlocked 가 True/reason 이고 메시지에 detail 이 있는지 본다.
func (w *rolloutWorld) assertBlocked(t *testing.T, reason, detail string) {
	t.Helper()
	cond := w.condition(t)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reason || !strings.Contains(cond.Message, detail) {
		t.Fatalf("RolloutBlocked = %+v, want True/%s mentioning %q", cond, reason, detail)
	}
}

// assertClear 는 RolloutBlocked 가 False/RolloutClear 인지 본다.
func (w *rolloutWorld) assertClear(t *testing.T) {
	t.Helper()
	if cond := w.condition(t); cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonRolloutClear {
		t.Fatalf("RolloutBlocked = %+v, want False/%s", cond, ReasonRolloutClear)
	}
}

// stepCount 는 rollout_steps_total 한 칸이다.
func stepCount(w *rolloutWorld, step rolloutStep, outcome rolloutOutcome) float64 {
	return testutil.ToFloat64(MetricRolloutSteps.WithLabelValues(runtimeTestNS, w.name, "shard-0", string(step), string(outcome)))
}

// withRebuilding 은 샤드에 진행 중인 재구축(멈추기 전 Holding)을 CR 에 심는다.
func withRebuilding(pod string, since time.Time) func(*postgresv1alpha1.PostgresCluster) {
	return func(cluster *postgresv1alpha1.PostgresCluster) {
		state := rebuildState{Version: rebuildStateVersion, Active: map[string]memberRebuild{pod: {
			Pod: pod, Shard: "shard-0", Reason: rebuildStale, Phase: rebuildHolding,
			Token: rebuildToken(since, 0), Since: since, PhaseSince: since,
		}}}
		raw, _ := json.Marshal(state)
		cluster.Annotations[memberRebuildAnnotation] = string(raw)
	}
}

// TestRolloutOnDelete 는 RollingUpdate STS(서버 기본값 partition 0)에 새 템플릿을 올리는 upsert 가 Update 한
// 요청에 OnDelete(rollingUpdate 없음 — API 검증은 OnDelete 와 rollingUpdate 를 같이 받지 않는다)와 새 템플릿을
// 싣는지 본다(§3.16). 따로 오면 템플릿이 먼저 바뀐 순간 STS 컨트롤러가 primary 까지 재시작한다. 새로 만드는 STS
// (리샤드 타깃 포함)도 OnDelete 다.
func TestRolloutOnDelete(t *testing.T) {
	t.Parallel()
	const name = "ro-ondelete"
	cluster, cm := runtimeCluster(name)
	secret := withSuperuserRef(cluster)
	old := oldTemplateSTS(cluster)
	partition := int32(0)
	old.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
		Type:          appsv1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
	}
	var updates []appsv1.StatefulSet
	scheme := runtimeScheme(t)
	primary, replica := shardMember(name, 0, statusapi.RolePrimary), shardMember(name, 1, statusapi.RoleReplica)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, secret, cm, old, primary, replica).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if sts, ok := obj.(*appsv1.StatefulSet); ok {
					updates = append(updates, *sts.DeepCopy())
				}
				return cli.Update(ctx, obj, opts...)
			},
		}).Build()
	// postgres 비밀번호는 이미 Secret 것이다 — 템플릿 upsert 앞 대조(ADR-0034)가 통과한다.
	pg := newPGSim(&writeLog{}, primary.Name, replica.Name)
	pg.stored = knownVerifier(t, testSuperuserPassword)
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme, PromotionPodExecutor: pg}
	t.Cleanup(func() { DeleteMetricsFor(runtimeTestNS, name) })

	reconcileCluster(t, r, cluster)

	if len(updates) != 1 {
		t.Fatalf("StatefulSet updates = %d, want exactly 1", len(updates))
	}
	got := updates[0]
	if got.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType || got.Spec.UpdateStrategy.RollingUpdate != nil {
		t.Fatalf("update strategy sent with the template = %+v, want OnDelete without rollingUpdate", got.Spec.UpdateStrategy)
	}
	if got.Spec.Template.Annotations[postgresConfigHashAnnotation] == old.Spec.Template.Annotations[postgresConfigHashAnnotation] {
		t.Fatalf("the update did not carry the new template (config hash %q kept)", old.Spec.Template.Annotations[postgresConfigHashAnnotation])
	}
	assertRuntimeEnv(t, envByNameOf(got.Spec.Template.Spec.Containers[0]),
		ShardRuntimeConfigMapName(name, "shard-0"), runtimeKeyPrimaryEndpoint)

	fresh := []*appsv1.StatefulSet{
		oldTemplateSTS(cluster),
		buildTargetShardStatefulSet(cluster, "t1", "example.com/postgres:18", "18",
			cluster.Spec.Shards.Storage, corev1.ResourceRequirements{}, TargetShardConfigMapName(name, "t1"), "hash"),
	}
	for _, sts := range fresh {
		if sts.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType {
			t.Fatalf("new StatefulSet %s update strategy = %+v, want OnDelete", sts.Name, sts.Spec.UpdateStrategy)
		}
	}

	// 오퍼레이터는 OnDelete 이고 상태가 현재 spec 을 본 STS 만 교체한다 — RollingUpdate 는 STS 컨트롤러 몫이다.
	unmanaged := []struct {
		name   string
		spec   func(*appsv1.StatefulSet)
		status func(*appsv1.StatefulSet)
	}{
		{name: "rolling update", spec: func(sts *appsv1.StatefulSet) {
			sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType}
		}, status: func(*appsv1.StatefulSet) {}},
		{name: "status not observed", spec: func(sts *appsv1.StatefulSet) { sts.Generation = 2 },
			status: func(sts *appsv1.StatefulSet) { sts.Status.ObservedGeneration = 1 }},
		{name: "no update revision", spec: func(*appsv1.StatefulSet) {},
			status: func(sts *appsv1.StatefulSet) { sts.Status.UpdateRevision = "" }},
	}
	for _, tc := range unmanaged {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRolloutWorld(t, "ro-unmanaged-"+strings.ReplaceAll(tc.name, " ", "-"), 0)
			w.editSTS(t, tc.spec, tc.status)
			w.forgetLog()
			w.roll(t, w.t0)
			w.assertDeleted(t)
		})
	}
}

// rolloutSequence 는 전체 Reconcile 로 옛 replica(order) → switchover(Rollout, 승자 winner) → 옛 primary 교체 →
// 완료를 돌린다(§3.16 · J12). 멤버를 지우는 것은 오퍼레이터뿐이고 한 번에 하나다.
func rolloutSequence(t *testing.T, name string, primary int, order [2]int, winner int) {
	t.Helper()
	w := newRolloutWorld(t, name, primary)
	old := w.member(primary)

	// 첫 단계 — 서수가 가장 높은 옛 replica 하나.
	w.reconcile(t)
	w.assertDeleted(t, order[0])
	w.replace(t, order[0], false)

	// 새 revision 이 Ready ∧ streaming 이 되기 전엔 다음 replica 를 안 지운다.
	w.reconcile(t)
	w.reconcile(t)
	w.assertDeleted(t, order[0])
	w.settle(t, order[0])

	w.reconcile(t)
	w.assertDeleted(t, order[0], order[1])
	w.replace(t, order[1], false)

	// 옛 replica 가 없다 — standby 전원이 Ready ∧ streaming 이 된 뒤에야 switchover(Rollout) 를 연다. 옛 primary 는
	// 지우지 않는다.
	w.reconcile(t)
	if state, found := w.state(t); found {
		t.Fatalf("switchover opened before %s streams: %+v", w.member(order[1]), state)
	}
	w.settle(t, order[1])
	w.reconcile(t)
	w.assertDeleted(t, order[0], order[1])
	state := w.mustState(t)
	if state.Reason != switchoverRollout || state.Phase != switchoverPreparing || state.Shard != shard0 || state.Target != "" {
		t.Fatalf("switchover state = %+v, want Preparing(Rollout) of shard-0 with no target", state)
	}

	// switchover 가 도는 동안(정지 · 제자리 재기동 · 승격) 멤버 삭제 0.
	w.reconcile(t)
	if state := w.mustState(t); state.Phase != switchoverStopping {
		t.Fatalf("switchover phase = %s, want %s: %q", state.Phase, switchoverStopping, w.log.lines)
	}
	w.reconcile(t)
	w.assertDeleted(t, order[0], order[1])
	w.restart(t, old)
	w.reconcile(t)
	if w.log.count("exec promote "+w.member(winner)+" ") != 1 || w.log.count("exec promote ") != 1 {
		t.Fatalf("want exactly one promotion of %s, got %q", w.member(winner), w.log.lines)
	}
	w.assertEnded(t)
	w.promoted(t, winner)

	// 옛 primary(이제 옛 revision replica)를 교체한다.
	w.reconcile(t)
	w.assertDeleted(t, order[0], order[1], primary)
	w.replace(t, primary, true)

	// 완료 — 한 번.
	w.reconcile(t)
	w.reconcile(t)
	if n := w.eventCount(corev1.EventTypeNormal, shardRolloutCompletedReason); n != 1 {
		t.Fatalf("%s events = %d, want 1: %q", shardRolloutCompletedReason, n, w.events)
	}
	if n := w.eventCount(corev1.EventTypeNormal, shardRolloutStepReason); n != 4 {
		t.Fatalf("%s events = %d, want 4 (3 replicas + 1 switchover): %q", shardRolloutStepReason, n, w.events)
	}
	if got := stepCount(w, rolloutReplica, rolloutStarted); got != 3 {
		t.Fatalf("rollout_steps_total{replica,started} = %v, want 3", got)
	}
	if got := stepCount(w, rolloutPrimary, rolloutStarted); got != 1 {
		t.Fatalf("rollout_steps_total{primary,started} = %v, want 1", got)
	}
	pending := MetricRolloutPendingMembers.WithLabelValues(runtimeTestNS, w.name, "shard-0")
	if got := testutil.ToFloat64(pending); got != 0 {
		t.Fatalf("rollout_pending_members = %v, want 0", got)
	}
	w.assertClear(t)
}

// TestRolloutPrimaryLast 는 primary 0-0 샤드가 0-2 → (0-2 수렴) → 0-1 → switchover(승자 0-1) → 0-0 순서로
// 교체되는지 본다.
func TestRolloutPrimaryLast(t *testing.T) {
	t.Parallel()
	t.Run("sequence", func(t *testing.T) {
		t.Parallel()
		rolloutSequence(t, "ro-primary-0", 0, [2]int{2, 1}, 1)
	})
	// 줄어드는 서수(옛 revision)가 아직 streaming 이면 standby 수가 members−1 이 아니다 — switchover 도, 그 파드
	// 삭제도 하지 않는다(STS 가 지운다).
	t.Run("shrinking member", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-shrinking", 0)
		w.revise(t, 1)
		w.revise(t, 2)
		extra := rolloutPod(t, w.name, rolloutMembers, statusapi.RoleReplica, oldRevision, 0)
		if err := w.c.Create(context.Background(), extra); err != nil {
			t.Fatalf("create %s: %v", extra.Name, err)
		}
		w.setState(extra.Name, walsenderStreaming)
		w.roll(t, w.t0)
		if _, found := w.state(t); found {
			t.Fatal("switchover opened while a shrinking member still streams")
		}
		w.assertDeleted(t)

		if err := w.c.Delete(context.Background(), extra); err != nil {
			t.Fatalf("delete %s: %v", extra.Name, err)
		}
		w.setState(extra.Name, "")
		w.roll(t, w.t0.Add(statusPollInterval))
		if state := w.mustState(t); state.Reason != switchoverRollout {
			t.Fatalf("switchover state = %+v, want Rollout", state)
		}
	})
	// standby 가 streaming 이어도 PodReady 가 아니면 switchover 를 열지 않는다.
	t.Run("standby not ready", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-standby-unready", 0)
		w.revise(t, 1)
		w.revise(t, 2)
		setPodReady(t, w.c, w.member(1), corev1.ConditionFalse)
		w.roll(t, w.t0)
		if _, found := w.state(t); found {
			t.Fatalf("switchover opened while %s is not Ready", w.member(1))
		}
		setPodReady(t, w.c, w.member(1), corev1.ConditionTrue)
		w.roll(t, w.t0.Add(statusPollInterval))
		w.mustState(t)
	})
	// switchover 가 primary 를 못 바꾸고 끝나면(중단) memberConvergeBudget 뒤에야 다시 연다.
	t.Run("aborted switchover retried after the budget", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-retry", 0)
		w.revise(t, 1)
		w.revise(t, 2)
		w.roll(t, w.t0)
		w.mustState(t)
		w.clearAnnotation(t, switchoverStateAnnotation)

		w.roll(t, w.t0.Add(statusPollInterval))
		if _, found := w.state(t); found {
			t.Fatal("switchover reopened within memberConvergeBudget")
		}
		w.roll(t, w.t0.Add(memberConvergeBudget+statusPollInterval))
		w.mustState(t)
		if n := w.eventCount(corev1.EventTypeNormal, shardRolloutStepReason); n != 2 {
			t.Fatalf("%s events = %d, want 2", shardRolloutStepReason, n)
		}
	})
	// 멤버가 하나면 switchover 할 standby 가 없다 — 지운다(사람 게이트 0).
	t.Run("single member", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-single", 0)
		w.shrink(t, 1)
		w.roll(t, w.t0)
		w.assertDeleted(t, 0)
		if _, found := w.state(t); found {
			t.Fatal("switchover opened for a single-member shard")
		}
	})
}

// TestRolloutPrimaryOrdinal2 는 primary 가 0-2 이면 0-1 → 0-0 → switchover(승자 0-0 — 동률은 낮은 서수) → 0-2
// 순서인지 본다.
func TestRolloutPrimaryOrdinal2(t *testing.T) {
	t.Parallel()
	rolloutSequence(t, "ro-primary-2", 2, [2]int{1, 0}, 0)
}

// TestRolloutDetachedFirst 는 고르는 순서를 본다(§3.16) — streaming 아닌 옛 replica(catchup 은 streaming 이
// 아니다)를 서수가 높은 옛 replica 보다 먼저 지우고, 대상 밖 replica 가 streaming 이 아니면 streaming 인 옛
// replica 도 지우지 않는다.
func TestRolloutDetachedFirst(t *testing.T) {
	t.Parallel()
	t.Run("catchup replica first", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-catchup-first", 0)
		w.setState(w.member(1), walsenderCatchup)
		w.roll(t, w.t0)
		w.assertDeleted(t, 1)
	})
	t.Run("absent replica first", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-absent-first", 0)
		w.setState(w.member(1), "")
		w.roll(t, w.t0)
		w.assertDeleted(t, 1)
	})
	t.Run("other replica not streaming", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-other-lagging", 0)
		w.revise(t, 1)
		w.setState(w.member(1), walsenderCatchup)
		w.roll(t, w.t0)
		w.roll(t, w.t0.Add(statusPollInterval))
		w.assertDeleted(t)

		w.setState(w.member(1), walsenderStreaming)
		w.roll(t, w.t0.Add(2*statusPollInterval))
		w.assertDeleted(t, 2)
	})
	t.Run("deleted replica still cached", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-stale-cache", 0)
		w.roll(t, w.t0)
		w.assertDeleted(t, 2)

		// 캐시가 지운 파드(같은 UID · 옛 revision · streaming)를 아직 보이고 1 이 끊겼다 — 1 을 지우면 둘이 내려간다.
		stale := rolloutPod(t, w.name, 2, statusapi.RoleReplica, oldRevision, 0)
		if err := w.c.Create(context.Background(), stale); err != nil {
			t.Fatalf("recreate cached %s: %v", stale.Name, err)
		}
		w.setState(w.member(1), walsenderCatchup)
		w.roll(t, w.t0.Add(statusPollInterval))
		w.assertDeleted(t, 2)
	})
	t.Run("evicted replica", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-evicted", 0)
		pod := w.pod(t, w.member(2))
		pod.Finalizers = []string{"test.keiailab.io/hold"}
		if err := w.c.Update(context.Background(), pod); err != nil {
			t.Fatalf("add finalizer: %v", err)
		}
		if err := w.c.Delete(context.Background(), pod); err != nil {
			t.Fatalf("evict %s: %v", pod.Name, err)
		}
		w.setState(w.member(2), "")
		w.forgetLog()

		// 지워지는 중인 멤버는 고르지 않고, 대상 밖에 있으니 다른 멤버도 지우지 않는다.
		w.roll(t, w.t0)
		w.assertDeleted(t)
		if n := w.eventCount(corev1.EventTypeNormal, shardRolloutStepReason); n != 0 {
			t.Fatalf("%s events = %d, want 0", shardRolloutStepReason, n)
		}
	})
	t.Run("other replica not ready", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-other-unready", 0)
		w.revise(t, 1)
		setPodReady(t, w.c, w.member(1), corev1.ConditionFalse)
		w.roll(t, w.t0)
		w.assertDeleted(t)

		setPodReady(t, w.c, w.member(1), corev1.ConditionTrue)
		w.roll(t, w.t0.Add(statusPollInterval))
		w.assertDeleted(t, 2)
	})
}

// TestRolloutBlockedBudget 는 선결이 memberConvergeBudget 안에 안 서면 기다리기만 하고(RolloutClear), 넘으면
// RolloutBlocked(MemberNotConverged) 와 Warning 을 한 번만 내고, 서면 RolloutClear 로 돌아와 다음 단계를 하는지 본다.
func TestRolloutBlockedBudget(t *testing.T) {
	t.Parallel()
	w := newRolloutWorld(t, "ro-budget", 0)
	blocked := stepCount(w, rolloutReplica, rolloutStalled)
	w.roll(t, w.t0)
	w.assertDeleted(t, 2)
	w.roll(t, w.t0.Add(time.Second))
	w.assertDeleted(t, 2)
	w.replace(t, 2, false)

	w.roll(t, w.t0.Add(memberConvergeBudget))
	w.assertClear(t)
	if n := w.eventCount(corev1.EventTypeWarning, shardRolloutBlockedReason); n != 0 {
		t.Fatalf("%s within the budget = %d, want 0: %q", shardRolloutBlockedReason, n, w.events)
	}

	for i := 1; i <= 3; i++ {
		w.roll(t, w.t0.Add(memberConvergeBudget+time.Duration(i)*statusPollInterval))
	}
	w.assertBlocked(t, ReasonMemberNotConverged, w.member(2))
	if n := w.eventCount(corev1.EventTypeWarning, shardRolloutBlockedReason); n != 1 {
		t.Fatalf("%s events = %d, want 1: %q", shardRolloutBlockedReason, n, w.events)
	}
	if got := stepCount(w, rolloutReplica, rolloutStalled) - blocked; got != 1 {
		t.Fatalf("rollout_steps_total{replica,blocked} delta = %v, want 1", got)
	}
	w.assertDeleted(t, 2)

	w.settle(t, 2)
	w.roll(t, w.t0.Add(memberConvergeBudget+5*statusPollInterval))
	w.assertClear(t)
	w.assertDeleted(t, 2, 1)
}

// TestRolloutRebuildActive 는 샤드에 재구축이나 switchover 가 돌면 멤버를 지우지 않고 예산 뒤
// RolloutBlocked(RebuildActive)이고, 끝나면 다음 걸음이 지우는지 본다.
func TestRolloutRebuildActive(t *testing.T) {
	t.Parallel()
	later := time.Now().Add(memberConvergeBudget)
	cases := []struct {
		name       string
		annotation string
		mutate     func(*postgresv1alpha1.PostgresCluster)
	}{
		{name: "rebuild", annotation: memberRebuildAnnotation, mutate: withRebuilding(graphMember("ro-rebuild", 1), time.Now())},
		{name: "switchover", annotation: switchoverStateAnnotation, mutate: withSwitchover(switchoverState{
			Shard: "shard-0", Reason: switchoverRequested, Phase: switchoverPreparing, Since: later,
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRolloutWorld(t, "ro-"+tc.name, 0, tc.mutate)
			w.roll(t, w.t0)
			w.roll(t, w.t0.Add(memberConvergeBudget+time.Second))
			w.assertDeleted(t)
			w.assertBlocked(t, ReasonRebuildActive, "")

			w.clearAnnotation(t, tc.annotation)
			w.roll(t, w.t0.Add(memberConvergeBudget+2*time.Second))
			w.assertDeleted(t, 2)
			w.assertClear(t)
		})
	}
	t.Run("unreadable rebuild state", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-rebuild-garbled", 0, func(cluster *postgresv1alpha1.PostgresCluster) {
			cluster.Annotations[memberRebuildAnnotation] = "{"
		})
		w.roll(t, w.t0)
		w.roll(t, w.t0.Add(memberConvergeBudget+time.Second))
		w.assertDeleted(t)
		w.assertBlocked(t, ReasonRebuildActive, "unreadable")
	})
	// 다른 샤드의 switchover 가 돌면(클러스터당 하나) 이 샤드의 switchover 단계는 기다린다.
	t.Run("switchover of another shard", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-other-shard", 0, withSwitchover(switchoverState{
			Shard: "shard-9", Reason: switchoverRequested, Phase: switchoverPreparing, Since: time.Now(),
		}))
		w.revise(t, 1)
		w.revise(t, 2)
		w.roll(t, w.t0)
		if n := w.eventCount(corev1.EventTypeNormal, shardRolloutStepReason); n != 0 {
			t.Fatalf("%s events = %d, want 0", shardRolloutStepReason, n)
		}
		if state := w.mustState(t); state.Shard != "shard-9" {
			t.Fatalf("switchover state = %+v, want the other shard's", state)
		}
	})
}

// TestRolloutConfigPending 는 첫 단계 전 primary 의 wal_keep_size(SHOW)가 렌더 값과 다르면 지우지 않고 예산 뒤
// RolloutBlocked(PrimaryConfigPending)이고, 같아지면 지우고, 첫 단계 뒤에는 다시 보지 않는지 본다.
func TestRolloutConfigPending(t *testing.T) {
	t.Parallel()
	w := newRolloutWorld(t, "ro-walkeep", 0)
	w.appendConf(t, "wal_keep_size = '5GB'")
	w.pg.set("wal_keep_size", "1024")

	w.roll(t, w.t0)
	w.roll(t, w.t0.Add(memberConvergeBudget+time.Second))
	w.assertDeleted(t)
	w.assertBlocked(t, ReasonPrimaryConfigPending, "wal_keep_size")

	w.pg.set("wal_keep_size", "5120")
	w.roll(t, w.t0.Add(memberConvergeBudget+2*time.Second))
	w.assertDeleted(t, 2)
	w.assertClear(t)

	w.pg.set("wal_keep_size", "1024")
	w.replace(t, 2, true)
	w.roll(t, w.t0.Add(memberConvergeBudget+3*time.Second))
	w.assertDeleted(t, 2, 1)

	// 설정을 못 읽으면 모른다 — 지우지 않는다.
	t.Run("settings unknown", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-settings-down", 0)
		w.pg.mu.Lock()
		w.pg.settingsDown = true
		w.pg.mu.Unlock()
		w.roll(t, w.t0)
		w.roll(t, w.t0.Add(memberConvergeBudget+time.Second))
		w.assertDeleted(t)
		w.assertBlocked(t, ReasonPrimaryConfigPending, w.member(0))
	})
}

// TestRolloutParamDecrease 는 렌더한 hot standby 필수 키가 primary 현재값보다 작으면 지우지 않고 예산 뒤
// RolloutBlocked(ParameterDecrease)인지 본다 — 그런 standby 는 복구를 멈춘다(I24). 같으면 지운다.
func TestRolloutParamDecrease(t *testing.T) {
	t.Parallel()
	keys := []string{
		"max_connections", "max_worker_processes", "max_wal_senders", "max_prepared_transactions", "max_locks_per_transaction",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			w := newRolloutWorld(t, "ro-dec-"+strings.ReplaceAll(key, "_", "-"), 0)
			w.appendConf(t, key+" = 100")
			w.pg.set(key, "200")
			w.roll(t, w.t0)
			w.roll(t, w.t0.Add(memberConvergeBudget+time.Second))
			w.assertDeleted(t)
			w.assertBlocked(t, ReasonParameterDecrease, key)
		})
	}
	t.Run("equal", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-dec-equal", 0)
		w.appendConf(t, "max_connections = 200")
		w.pg.set("max_connections", "200")
		w.roll(t, w.t0)
		w.assertDeleted(t, 2)
	})
}

// TestRolloutCompleted 는 완료 판정을 본다 — 전 파드 해시 = updateRevision ∧ updatedReplicas = replicas 일 때만
// ShardRolloutCompleted 를 한 번 낸다(OnDelete 에서 안 움직이는 currentRevision 은 안 본다).
func TestRolloutCompleted(t *testing.T) {
	t.Parallel()
	w := newRolloutWorld(t, "ro-complete", 0)
	w.pg.refuse(true)
	w.roll(t, w.t0)
	w.assertDeleted(t)
	pending := MetricRolloutPendingMembers.WithLabelValues(runtimeTestNS, w.name, "shard-0")
	if got := testutil.ToFloat64(pending); got != rolloutMembers {
		t.Fatalf("rollout_pending_members = %v, want %d", got, rolloutMembers)
	}
	labels := map[string]string{"namespace": runtimeTestNS, "name": w.name, "shard": "shard-0",
		"step": string(rolloutPrimary), "outcome": string(rolloutStalled)}
	if !seriesPresent(t, "postgres_operator_postgrescluster_rollout_steps_total", labels) {
		t.Fatal("rollout_steps_total{primary,blocked} not initialised by the first step")
	}

	// 상태가 앞서도(updatedReplicas = replicas) 옛 파드가 남으면 끝이 아니다.
	w.setUpdated(t, rolloutMembers)
	w.roll(t, w.t0.Add(statusPollInterval/2))
	if n := w.eventCount(corev1.EventTypeNormal, shardRolloutCompletedReason); n != 0 {
		t.Fatalf("%s with outdated pods = %d, want 0", shardRolloutCompletedReason, n)
	}

	for i := range rolloutMembers {
		w.revise(t, i)
	}
	w.setUpdated(t, rolloutMembers-1)
	w.roll(t, w.t0.Add(statusPollInterval))
	if n := w.eventCount(corev1.EventTypeNormal, shardRolloutCompletedReason); n != 0 {
		t.Fatalf("%s with updatedReplicas < replicas = %d, want 0", shardRolloutCompletedReason, n)
	}

	w.setUpdated(t, rolloutMembers)
	w.roll(t, w.t0.Add(2*statusPollInterval))
	w.roll(t, w.t0.Add(3*statusPollInterval))
	if n := w.eventCount(corev1.EventTypeNormal, shardRolloutCompletedReason); n != 1 {
		t.Fatalf("%s events = %d, want 1: %q", shardRolloutCompletedReason, n, w.events)
	}
	if got := testutil.ToFloat64(pending); got != 0 {
		t.Fatalf("rollout_pending_members = %v, want 0", got)
	}
	w.assertDeleted(t)
}

// TestRolloutImagePullSlots 는 막힌 까닭을 가른다 — 기다리는 멤버가 이미지를 못 받으면 ImagePull, primary exec 가
// 연결 슬롯이 없어 실패하면 모름(ConnectionSlots)이고 둘 다 삭제 0 이다. 슬롯이 돌아오면 지운다.
func TestRolloutImagePullSlots(t *testing.T) {
	t.Parallel()
	t.Run("image pull", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-image-pull", 0)
		w.roll(t, w.t0)
		w.assertDeleted(t, 2)
		w.replace(t, 2, false)
		w.pullFails(t, 2)
		w.roll(t, w.t0.Add(memberConvergeBudget+time.Second))
		w.assertDeleted(t, 2)
		w.assertBlocked(t, ReasonImagePull, w.member(2))
	})
	// 연쇄 standby 의 exec 가 실패하면 그래프는 모름이다 — 끊겼는지 모르는 멤버는 지우지 않는다.
	t.Run("graph unknown", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-graph-unknown", 0)
		w.setState(w.member(2), "")
		w.pg.chain(w.member(1), walRow(w.member(2), walsenderStreaming), true)
		w.roll(t, w.t0)
		w.roll(t, w.t0.Add(memberConvergeBudget+time.Second))
		w.assertDeleted(t)
		w.assertBlocked(t, ReasonMemberNotConverged, "unknown")

		w.pg.chain(w.member(1), walRow(w.member(2), walsenderStreaming), false)
		w.roll(t, w.t0.Add(memberConvergeBudget+2*time.Second))
		w.assertDeleted(t, 2)
	})
	// 다른 primary 를 뿌리로 본 그래프(런타임 CM 과 어긋남)로는 지우지 않는다.
	t.Run("graph of another primary", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-graph-other-root", 0)
		ctx := context.Background()
		cluster := w.cluster(t)
		all := map[string]bool{w.member(0): true, w.member(1): true, w.member(2): true}
		stale := map[string]replicationGraph{"shard-0": {
			root: podDNS(w.name, w.member(1)), known: true, linked: all, streaming: all, upstream: map[string]string{},
		}}
		w.r.advanceRollouts(ctx, cluster, w.shards(cluster), stale, w.t0)
		w.assertDeleted(t)
	})
	t.Run("connection slots", func(t *testing.T) {
		t.Parallel()
		w := newRolloutWorld(t, "ro-slots", 0)
		w.pg.refuse(true)
		w.roll(t, w.t0)
		w.roll(t, w.t0.Add(memberConvergeBudget+time.Second))
		w.assertDeleted(t)
		w.assertBlocked(t, ReasonConnectionSlots, w.member(0))

		w.pg.refuse(false)
		w.roll(t, w.t0.Add(memberConvergeBudget+2*time.Second))
		w.assertDeleted(t, 2)
		w.assertClear(t)
	})
}
