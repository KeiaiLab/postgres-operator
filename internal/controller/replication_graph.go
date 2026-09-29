/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

// 본 파일은 샤드의 복제 그래프를 관측하고 그것으로 fence 를 풀고 끊긴 standby 를 재지정한다
// (설계 2026-09-25 §3.4). readyz 는 단절을 못 본다 — 연결 판정은 pg_stat_replication 이다.
//
//	CM primary(실제 primary) ─ exec pg_stat_replication ─▶ 직결 standby
//	                                                        │ 직결이 전부가 아니면(연쇄)
//	                                                        ▼
//	                                          확인된 standby 의 pg_stat_replication
//	replicationGraph{known, linked, upstream}
//	  ├─▶ 연결 ∧ fenced                                        → fence 제거 · MemberUnfenced
//	  ├─▶ 끊김 > memberRetargetAfter ∧ PodReady ∧ in recovery → primary_conninfo = CM 호스트 · reload
//	  └─▶ ReplicationHealthy 사유 · 승격 후보 필터(§3.3)
//
// 모름(exec·파싱 실패)은 없음이 아니다(I10) — 모르면 fence 해제·재지정·끊김 기록이 0 이다.
// 쓰는 것은 fence 라벨과 standby 의 primary_conninfo(재시작 없음)뿐이다 — PGDATA·파드·PVC 삭제 0.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	commonsevents "github.com/keiailab/keiailab-commons/pkg/events"
	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/instance/fencing"
)

const (
	// memberRetargetAfter 는 끊긴 standby 를 CM primary 로 재지정하기 전 기다리는 시간이다 —
	// 하트비트 끊김 문턱과 같다(부록 A).
	memberRetargetAfter = statusStaleThresh

	// replicationProbeTimeout 은 그래프 exec 1회의 상한이다 — 무응답 exec 가 reconcile 을 붙잡지 않는다.
	replicationProbeTimeout = statusPollInterval

	// replicationQuery 는 sender 한 곳의 walsender 목록이다 — application_name 은 멤버 파드 이름이다
	// (부트스트랩·인스턴스 매니저 conninfo).
	replicationQuery = "SELECT application_name, state FROM pg_stat_replication"

	// psqlFieldSeparator 는 psql -A 의 열 구분자다.
	psqlFieldSeparator = "|"

	memberUnfencedReason   = "MemberUnfenced"
	memberRetargetedReason = "MemberRetargeted"
)

// pg_stat_replication.state 값이다.
const (
	walsenderStartup   = "startup"
	walsenderCatchup   = "catchup"
	walsenderStreaming = "streaming"
	walsenderBackup    = "backup"
	walsenderStopping  = "stopping"
)

// walsenderLinked 는 state 가 연결인지다 — streaming·catchup·backup(backup 은 진척이 아니지만
// 연결이다). 없는 state 는 파싱 실패다.
var walsenderLinked = map[string]bool{
	walsenderStartup:   false,
	walsenderCatchup:   true,
	walsenderStreaming: true,
	walsenderBackup:    true,
	walsenderStopping:  false,
}

// replicationGraph 는 샤드 하나의 복제 관측이다. root 가 비면 관측하지 않았다(CM primary 가 실제
// primary 가 아니다). linked 는 확인된 연결이고 known 은 그것이 전부인지다 — exec·파싱이 하나라도
// 실패하면 거짓이다. streaming 은 linked 중 따라잡은(state streaming) 멤버다 — 단계화 롤아웃의 수렴.
type replicationGraph struct {
	root      string            // 관측을 시작한 CM PRIMARY_ENDPOINT
	known     bool              // linked 밖의 멤버는 연결이 없다
	linked    map[string]bool   // 연결된 멤버 파드
	streaming map[string]bool   // linked 중 streaming 인 멤버 파드(catchup·backup 아님)
	upstream  map[string]string // 멤버 → 그 멤버에게 WAL 을 보내는 파드(primary 또는 연쇄 standby)
}

// detached 는 그래프가 pod 의 연결이 없음을 아는지다 — 모르거나 root 면 거짓이다.
func (g replicationGraph) detached(pod string) bool {
	return g.known && pod != endpointPod(g.root) && !g.linked[pod]
}

// retargetAttempt 는 멤버 재지정의 마지막 시도다 — 같은 대상은 성공하면 다시 하지 않고, 실패하면
// memberRetargetAfter 뒤에 다시 한다.
type retargetAttempt struct {
	target string
	done   bool
	at     time.Time
}

// observeGraphs 는 샤드마다 복제 그래프다(샤드 이름 키).
func (r *PostgresClusterReconciler) observeGraphs(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
) map[string]replicationGraph {
	graphs := make(map[string]replicationGraph, len(shards))
	for i := range shards {
		graphs[shards[i].Name] = r.observeReplication(ctx, cluster, shards[i])
	}
	return graphs
}

// observeReplication 은 CM primary 가 실제 primary 인 샤드의 복제 그래프다 — 그 primary 의
// pg_stat_replication 에서 시작해 직결이 전부가 아니면 확인된 standby 로 내려간다(exec ≤ members−1).
func (r *PostgresClusterReconciler) observeReplication(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard postgresv1alpha1.ShardStatus,
) replicationGraph {
	if standaloneReplicaEnabled(cluster) {
		return replicationGraph{}
	}
	logger := log.FromContext(ctx).WithValues("shard", shard.Name)
	ref := shardRefFor(cluster, &shard)

	// 실제 primary 인 CM 파드 — 아니면 관측하지 않는다(그래프의 뿌리가 없다).
	rt, found, err := r.readRuntime(ctx, cluster, ref.name)
	if err != nil || !found {
		return replicationGraph{}
	}
	pods, err := r.shardMembers(ctx, cluster, ref.member)
	if err != nil {
		logger.V(1).Info("replication graph not observed", "error", err)
		return replicationGraph{}
	}
	fenced, err := r.fencedPVCs(ctx, cluster.Namespace)
	if err != nil {
		logger.V(1).Info("replication graph not observed", "error", err)
		return replicationGraph{}
	}
	root := podNamed(pods, endpointPod(rt.PrimaryEndpoint))
	if r.checkPrimary(ctx, root, fenced, time.Now()) != primaryConfirmed {
		return replicationGraph{}
	}

	// 너비 우선 — 연결을 확인한 standby 만 다음 sender 가 된다.
	g := replicationGraph{
		root: rt.PrimaryEndpoint, known: true,
		linked: map[string]bool{}, streaming: map[string]bool{}, upstream: map[string]string{},
	}
	standbys := len(pods) - 1
	senders := []*corev1.Pod{root}
	for len(senders) > 0 && len(g.linked) < standbys {
		sender := senders[0]
		senders = senders[1:]
		rows, err := r.replicationRows(ctx, sender)
		if err != nil {
			logger.Info("replication graph unknown", "sender", sender.Name, "error", err)
			g.known = false
			return g
		}
		for _, name := range slices.Sorted(maps.Keys(rows)) {
			member := podNamed(pods, name)
			if member == nil || name == root.Name || g.linked[name] || !walsenderLinked[rows[name]] {
				continue
			}
			g.linked[name] = true
			g.upstream[name] = sender.Name
			if rows[name] == walsenderStreaming {
				g.streaming[name] = true
			}
			senders = append(senders, member)
		}
	}
	return g
}

// replicationRows 는 sender 의 pg_stat_replication 이다(application_name → state).
func (r *PostgresClusterReconciler) replicationRows(ctx context.Context, sender *corev1.Pod) (map[string]string, error) {
	if r.PromotionPodExecutor == nil {
		return nil, errors.New("pod executor is not configured")
	}
	// 죽은 노드의 파드에 exec 하면 시한까지 붙잡힌다.
	if sender.DeletionTimestamp != nil || sender.Status.Phase != corev1.PodRunning {
		return nil, fmt.Errorf("sender %s is not running", sender.Name)
	}
	probeCtx, cancel := context.WithTimeout(ctx, replicationProbeTimeout)
	defer cancel()
	out, err := r.PromotionPodExecutor.Exec(probeCtx, BackupSidecarTarget{
		Namespace: sender.Namespace,
		Pod:       sender.Name,
		Container: pgContainerName,
	}, []string{"psql", "-U", "postgres", "-d", "postgres", "-Atqc", replicationQuery})
	if err != nil {
		return nil, err
	}
	return parseReplicationRows(out)
}

// parseReplicationRows 는 psql -At 출력(application_name|state 줄들)을 읽는다. 이름은 마지막 구분자
// 앞 전부다(state 에는 구분자가 없다). 같은 이름이 둘이면(basebackup 의 backup + WAL streaming)
// 연결 state 가 이긴다. 구분자가 없거나 모르는 state 면 오류 — 모름이다.
func parseReplicationRows(out []byte) (map[string]string, error) {
	rows := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cut := strings.LastIndex(line, psqlFieldSeparator)
		if cut < 0 {
			return nil, fmt.Errorf("pg_stat_replication row %q has no separator", line)
		}
		name, state := line[:cut], line[cut+len(psqlFieldSeparator):]
		if _, ok := walsenderLinked[state]; !ok {
			return nil, fmt.Errorf("pg_stat_replication row %q has unknown state", line)
		}
		if walsenderLinked[rows[name]] {
			continue
		}
		rows[name] = state
	}
	return rows, nil
}

// reconcileGraphActions 는 그래프로 움직인다(§3.4): 연결 기록 → 연결된 fenced 멤버의 fence 제거
// → 끊긴 standby 재지정. 오래 끊긴 standby 의 재시드는 재구축 엔진이 한다(requestRebuilds).
func (r *PostgresClusterReconciler) reconcileGraphActions(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
	graphs map[string]replicationGraph,
	now time.Time,
) {
	key := cluster.Namespace + "/" + cluster.Name
	for i := range shards {
		g := graphs[shards[i].Name]
		if g.root == "" {
			continue
		}
		ref := shardRefFor(cluster, &shards[i])
		pods, err := r.shardMembers(ctx, cluster, ref.member)
		if err != nil {
			log.FromContext(ctx).Info("replication graph actions skipped", "shard", ref.name, "error", err)
			continue
		}
		r.recordLinks(key, g, pods, now)
		r.unfenceLinked(ctx, cluster, ref.name, g)
		r.retargetDetached(ctx, cluster, ref.name, g, pods, now)
	}
}

// recordLinks 는 멤버마다 연결 기록을 남긴다 — 연결 = 0, 끊김 = 처음 본 시각. 끊김은 그래프가
// 전부를 알 때만 기록한다. root(primary)는 기록이 없다.
func (r *PostgresClusterReconciler) recordLinks(key string, g replicationGraph, pods []corev1.Pod, now time.Time) {
	r.graphMu.Lock()
	defer r.graphMu.Unlock()
	if r.memberAbsentSince == nil {
		r.memberAbsentSince = map[string]map[string]time.Time{}
	}
	records := r.memberAbsentSince[key]
	if records == nil {
		records = map[string]time.Time{}
		r.memberAbsentSince[key] = records
	}

	root := endpointPod(g.root)
	for i := range pods {
		name := pods[i].Name
		switch {
		case name == root:
			delete(records, name)
		case g.linked[name]:
			records[name] = time.Time{}
		case !g.known:
			// 모름 — 끊김을 기록하지 않는다.
		case records[name].IsZero():
			records[name] = now
		}
	}
}

// unfenceLinked 는 연결을 확인한 fenced 멤버의 fence 를 뗀다(I10 — 확인 뒤에만).
func (r *PostgresClusterReconciler) unfenceLinked(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard string,
	g replicationGraph,
) {
	if len(g.linked) == 0 {
		return
	}
	logger := log.FromContext(ctx).WithValues("shard", shard)
	fenced, err := r.fencedPVCs(ctx, cluster.Namespace)
	if err != nil {
		logger.Info("fence state unknown; linked members left fenced", "error", err)
		return
	}
	for _, pod := range slices.Sorted(maps.Keys(g.linked)) {
		if !fenced[fencing.PVCName(pod)] {
			continue
		}
		if err := unfencePVC(ctx, r.Client, cluster.Namespace, pod); err != nil {
			logger.Info("linked member unfence failed", "pod", pod, "error", err)
			continue
		}
		commonsevents.Emitf(r.Recorder, cluster, memberUnfencedReason,
			"shard %s: %s replicates from %s; PVC fence removed", shard, pod, g.upstream[pod])
	}
}

// retargetDetached 는 memberRetargetAfter 넘게 끊긴 standby 의 primary_conninfo 를 CM primary 로 다시
// 쓴다 — PodReady ∧ in recovery 인 멤버만, fence 와 무관하다(승격 직후 생존자는 fenced 가 정상이다).
func (r *PostgresClusterReconciler) retargetDetached(
	ctx context.Context,
	cluster *postgresv1alpha1.PostgresCluster,
	shard string,
	g replicationGraph,
	pods []corev1.Pod,
	now time.Time,
) {
	key := cluster.Namespace + "/" + cluster.Name
	logger := log.FromContext(ctx).WithValues("shard", shard)
	for i := range pods {
		pod := &pods[i]
		if !g.detached(pod.Name) || kubernetesPodNotReady(pod) || !r.retargetDue(key, pod.Name, g.root, now) {
			continue
		}
		err := r.retargetStandby(ctx, pod, g.root)
		r.recordRetarget(key, pod.Name, g.root, err == nil, now)
		if err != nil {
			logger.Info("member retarget failed", "pod", pod.Name, "error", err)
			continue
		}
		commonsevents.Emitf(r.Recorder, cluster, memberRetargetedReason,
			"shard %s: %s primary_conninfo now follows %s", shard, pod.Name, primaryEndpointHost(g.root))
	}
}

// retargetDue 는 pod 를 target 으로 지금 재지정할지다 — 끊김이 memberRetargetAfter 를 넘었고, 같은
// 대상 시도가 없거나 실패한 지 memberRetargetAfter 가 지났을 때.
func (r *PostgresClusterReconciler) retargetDue(key, pod, target string, now time.Time) bool {
	r.graphMu.Lock()
	defer r.graphMu.Unlock()
	if !absentLong(r.memberAbsentSince[key][pod], now) {
		return false
	}
	last, ok := r.lastRetarget[key][pod]
	if !ok || last.target != target {
		return true
	}
	return !last.done && now.Sub(last.at) > memberRetargetAfter
}

// recordRetarget 은 재지정 시도를 남긴다.
func (r *PostgresClusterReconciler) recordRetarget(key, pod, target string, done bool, now time.Time) {
	r.graphMu.Lock()
	defer r.graphMu.Unlock()
	if r.lastRetarget == nil {
		r.lastRetarget = map[string]map[string]retargetAttempt{}
	}
	if r.lastRetarget[key] == nil {
		r.lastRetarget[key] = map[string]retargetAttempt{}
	}
	r.lastRetarget[key][pod] = retargetAttempt{target: target, done: done, at: now}
}

// retargetStandby 는 in recovery 인 pod 의 primary_conninfo 를 endpoint 로 바꾸고 설정을 다시 읽힌다
// — primary_conninfo 는 reload 로 반영된다(walreceiver 재기동, postgres 재시작 없음). ALTER SYSTEM 은
// auto.conf 의 같은 키 줄을 하나로 다시 쓴다.
func (r *PostgresClusterReconciler) retargetStandby(ctx context.Context, pod *corev1.Pod, endpoint string) error {
	inRecovery, err := r.inRecovery(ctx, pod)
	if err != nil {
		return err
	}
	if !inRecovery {
		return fmt.Errorf("%s is not in recovery", pod.Name)
	}
	probeCtx, cancel := context.WithTimeout(ctx, replicationProbeTimeout)
	defer cancel()
	_, err = r.PromotionPodExecutor.Exec(probeCtx, BackupSidecarTarget{
		Namespace: pod.Namespace,
		Pod:       pod.Name,
		Container: pgContainerName,
	}, retargetCommand(standbyConninfo(endpoint, pod.Name)))
	return err
}

// retargetCommand 는 primary_conninfo 를 바꾸고 reload 한다 — ALTER SYSTEM 은 트랜잭션 블록 안에서
// 못 돌아 -c 를 나누고, 실패하면 reload 전에 멈춘다.
func retargetCommand(conninfo string) []string {
	return []string{
		"psql", "-U", "postgres", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-Atq",
		"-c", "ALTER SYSTEM SET primary_conninfo = " + sqlLiteral(conninfo),
		"-c", "SELECT pg_reload_conf()",
	}
}

// standbyConninfo 는 부트스트랩 prepare_primary_conninfo 와 같은 형식의 HA primary_conninfo 다
// (예: host=db-0.svc port=5432 user=postgres dbname=postgres sslmode=prefer application_name=db-1).
func standbyConninfo(endpoint, pod string) string {
	port := strconv.Itoa(int(pgPort))
	if i := strings.LastIndex(endpoint, ":"); i > 0 {
		port = endpoint[i+1:]
	}
	return fmt.Sprintf("host=%s port=%s user=%s dbname=%s sslmode=%s application_name=%s",
		primaryEndpointHost(endpoint), port, primaryConninfoUser, primaryConninfoDBName, primaryConninfoSSLMode, pod)
}

// absentLong 은 끊김 기록이 memberRetargetAfter 를 넘었는지다(0 = 연결).
func absentLong(since, now time.Time) bool {
	return !since.IsZero() && now.Sub(since) > memberRetargetAfter
}

// linkedCandidates 는 샤드마다 후보 필터를 거친 사본이다 — 승격 판정만 읽는다(CR status 는 원본).
func (r *PostgresClusterReconciler) linkedCandidates(
	cluster *postgresv1alpha1.PostgresCluster,
	shards []postgresv1alpha1.ShardStatus,
	now time.Time,
) []postgresv1alpha1.ShardStatus {
	r.graphMu.Lock()
	records := maps.Clone(r.memberAbsentSince[cluster.Namespace+"/"+cluster.Name])
	r.graphMu.Unlock()

	out := make([]postgresv1alpha1.ShardStatus, len(shards))
	for i := range shards {
		out[i] = linkedCandidateFilter(shards[i], records, now)
	}
	return out
}

// linkedCandidateFilter 는 memberRetargetAfter 넘게 끊긴 Ready 후보를 후보에서 뺀다 — 연결 기록(연결로
// 관측 · 끊긴 지 memberRetargetAfter 안)이 있는 Ready 후보가 있을 때만(§3.3). 기록이 없는 후보는
// 빼지도 세지도 않는다. 원본은 바꾸지 않는다.
func linkedCandidateFilter(
	shard postgresv1alpha1.ShardStatus,
	records map[string]time.Time,
	now time.Time,
) postgresv1alpha1.ShardStatus {
	cut := map[string]bool{}
	linked := false
	for _, rep := range shard.Replicas {
		since, recorded := records[rep.Pod]
		switch {
		case !rep.Ready || !recorded:
		case absentLong(since, now):
			cut[rep.Pod] = true
		default:
			linked = true
		}
	}
	if !linked || len(cut) == 0 {
		return shard
	}

	out := shard
	out.Replicas = slices.Clone(shard.Replicas)
	for i := range out.Replicas {
		if cut[out.Replicas[i].Pod] {
			out.Replicas[i].Ready = false
		}
	}
	return out
}
