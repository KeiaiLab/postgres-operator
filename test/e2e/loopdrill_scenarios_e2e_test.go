//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/keiailab/postgres-operator/internal/loopdrill"
)

// 시나리오 층 — 유발(fault)과 창(수렴 상한 + 관측창) 동안의 표본.
//
//	prepare ─ before ─ inject ── hold(또는 until) ── release ── 수렴(≤ ConvergeWithin) ── LoopObservationWindow ── after
//	                                                              └ RED 는 기대한 붉음이 보이면 여기서 끝낸다(양성 대조가 섰다)

const (
	// slowSampleEvery 는 무거운 표본(스냅숏·게이지·RED 중간 판정)을 몇 틱마다 뜰지.
	slowSampleEvery = 6
	markerBound     = 2 * time.Minute

	// switchoverAttempts 는 S10 이 switchover-target 을 다시 다는 상한 — 수신 위치는 statusPollInterval 마다 바뀌어
	// 오퍼레이터가 TargetBehind 로 거절할 수 있다(그 메시지가 수신 최대 파드를 알려 준다).
	switchoverAttempts = 5

	// detachedHost 는 S6b 가 standby primary_conninfo 에 넣는 무효 호스트(.invalid — RFC 6761 은 해석되지 않는다).
	detachedHost = "loopdrill-detached.invalid"

	// cpuMilli 는 S8 템플릿 변경 — CPU 요청을 1m 올린다(파드 템플릿 해시만 바뀐다).
	cpuMilliSuffix = "m"
	milliPerCore   = 1000
)

// injection 은 유발 하나 — undo 는 hold 가 끝나면 부른다(없으면 nil).
type injection struct {
	undo func() error
}

func (i injection) revert() error {
	if i.undo == nil {
		return nil
	}
	return i.undo()
}

// fault 는 시나리오의 유발 방식.
type fault struct {
	prepare func(obs observation) error                         // before 전에 — 대상 조건 맞추기
	inject  func(obs observation, round int) (injection, error) // 유발
	hold    time.Duration                                       // 유발을 유지하는 상한
	until   func(rec *recorder) bool                            // 서면 hold 전에 푼다(S8 — RolloutBlocked 관측)
	rounds  int                                                 // S7 = 예산+1
}

func (d *drill) faultFor(scn loopdrill.Scenario, budget int) (fault, error) {
	switch scn {
	case loopdrill.ScenarioS1:
		return fault{inject: deletePrimary(gracefulDelete), rounds: 1}, nil
	case loopdrill.ScenarioS2:
		return fault{inject: deletePrimary(forceDelete), rounds: 1}, nil
	case loopdrill.ScenarioS3:
		return fault{inject: stopPrimaryManager, hold: loopdrill.FaultPause, rounds: 1}, nil
	case loopdrill.ScenarioS4:
		return fault{inject: pauseControlPlane, hold: loopdrill.FaultPause, rounds: 1}, nil
	case loopdrill.ScenarioS5:
		return fault{prepare: waitStandbyAge, inject: stopStandbyNode(nodeRBACAsIs), hold: loopdrill.StallStopDuration, rounds: 1}, nil
	case loopdrill.ScenarioS5a:
		revoked := stopStandbyNode(nodeRBACRevoked)
		return fault{prepare: waitStandbyAge, inject: revoked, hold: loopdrill.StallStopDuration, rounds: 1}, nil
	case loopdrill.ScenarioS6:
		return fault{inject: freezeStandby, hold: loopdrill.StandbyFreeze, rounds: 1}, nil
	case loopdrill.ScenarioS6b:
		return fault{inject: detachThenKill, rounds: 1}, nil
	case loopdrill.ScenarioS7:
		return fault{inject: freezeStandby, hold: loopdrill.StandbyFreeze, rounds: budget + 1}, nil
	case loopdrill.ScenarioS8:
		blocked := func(rec *recorder) bool { return rec.blockedSeen }
		limit := loopdrill.RolloutBlockHold + loopdrill.StatusStaleThresh
		return fault{inject: stallRollout, hold: limit, until: blocked, rounds: 1}, nil
	case loopdrill.ScenarioS9:
		return fault{inject: stopPrimaryNode, hold: loopdrill.NodeDeathHold, rounds: 1}, nil
	case loopdrill.ScenarioS10:
		return fault{inject: d.emergencyStop, hold: loopdrill.SwitchoverBudget, rounds: 1}, nil
	}
	return fault{}, fmt.Errorf("유발이 없는 시나리오 %s", scn)
}

// ── 유발 ──

type deleteMode string

const (
	gracefulDelete deleteMode = "graceful"
	forceDelete    deleteMode = "force"
)

func deletePrimary(mode deleteMode) func(observation, int) (injection, error) {
	return func(obs observation, _ int) (injection, error) {
		if err := guardTarget(); err != nil {
			return injection{}, err
		}
		primary, ok := obs.primary()
		if !ok {
			return injection{}, errors.New("primary 가 하나가 아니다")
		}
		args := []string{"delete", "pod", primary.Pod, "-n", dataNamespace, "--wait=false"}
		if mode == forceDelete {
			args = append(args, "--grace-period=0", "--force")
		}
		_, err := kubectl(args...)
		return injection{}, err
	}
}

// stopPrimaryManager 는 S3 — primary 컨테이너 PID 1(인스턴스 매니저)만 세운다. postgres 는 돈다.
func stopPrimaryManager(obs observation, _ int) (injection, error) {
	if err := guardTarget(); err != nil {
		return injection{}, err
	}
	primary, ok := obs.primary()
	if !ok {
		return injection{}, errors.New("primary 가 하나가 아니다")
	}
	pid, err := containerPID(primary.Node, primary.ContainerID)
	if err != nil {
		return injection{}, err
	}
	if _, err := nodeExec(primary.Node, "kill", "-STOP", pid); err != nil {
		return injection{}, err
	}
	return injection{undo: func() error {
		_, err := nodeExec(primary.Node, "kill", "-CONT", pid)
		return err
	}}, nil
}

// pauseControlPlane 는 S4 — apiserver·etcd·controller-manager 를 한꺼번에 얼린다(k3s 재시작 모사).
func pauseControlPlane(_ observation, _ int) (injection, error) {
	if err := guardTarget(); err != nil {
		return injection{}, err
	}
	node := controlPlaneNode()
	if err := pauseNode(node); err != nil {
		return injection{}, err
	}
	return injection{undo: func() error { return unpauseNode(node) }}, nil
}

// stopPrimaryNode 는 S9 — primary 가 있는 워커를 세운다(노드 사망 — PodReady False 는 node-monitor-grace 뒤).
func stopPrimaryNode(obs observation, _ int) (injection, error) {
	if err := guardTarget(); err != nil {
		return injection{}, err
	}
	primary, ok := obs.primary()
	if !ok || primary.Node == "" {
		return injection{}, errors.New("primary 노드를 모른다")
	}
	if err := stopNode(primary.Node); err != nil {
		return injection{}, err
	}
	return injection{undo: func() error { return startNode(primary.Node) }}, nil
}

// standbyTarget 은 primary 와 다른 노드의 standby 하나.
func standbyTarget(obs observation, round int) (member, error) {
	primary, ok := obs.primary()
	if !ok {
		return member{}, errors.New("primary 가 하나가 아니다")
	}
	var candidates []member
	for _, s := range obs.standbys() {
		if s.Node != primary.Node && s.Node != "" {
			candidates = append(candidates, s)
		}
	}
	if len(candidates) == 0 {
		return member{}, errors.New("primary 와 다른 노드의 standby 없음")
	}
	return candidates[round%len(candidates)], nil
}

// waitStandbyAge 는 S5 대상의 나이가 기점 재시드 조건(StandbyAgeFloor)을 넘을 때까지 기다린다.
func waitStandbyAge(obs observation) error {
	target, err := standbyTarget(obs, 0)
	if err != nil {
		return err
	}
	if wait := loopdrill.StandbyAgeFloor - time.Since(target.Created); wait > 0 {
		time.Sleep(wait)
	}
	return nil
}

type nodeRBAC string

const (
	nodeRBACAsIs    nodeRBAC = "as-is"   // 차트 그대로(RED 기점 = nodes 없음 · GREEN C1 = nodes 있음)
	nodeRBACRevoked nodeRBAC = "revoked" // S5a — 오퍼레이터 ClusterRole 에서 nodes 를 뗀다
)

// stopStandbyNode 는 S5 — standby 가 있는 워커를 세운다(노드 사망 모사).
func stopStandbyNode(rbac nodeRBAC) func(observation, int) (injection, error) {
	return func(obs observation, round int) (injection, error) {
		if err := guardTarget(); err != nil {
			return injection{}, err
		}
		target, err := standbyTarget(obs, round)
		if err != nil {
			return injection{}, err
		}
		restore := func() error { return nil }
		if rbac == nodeRBACRevoked {
			if restore, err = revokeNodeReads(); err != nil {
				return injection{}, err
			}
		}
		if err := stopNode(target.Node); err != nil {
			return injection{}, errors.Join(err, restore())
		}
		return injection{undo: func() error { return errors.Join(startNode(target.Node), restore()) }}, nil
	}
}

const managerRole = releaseName + "-manager-role"

// revokeNodeReads 는 오퍼레이터 ClusterRole 의 core nodes 권한을 떼고 되돌리는 함수를 낸다(S5a).
func revokeNodeReads() (func() error, error) {
	out, err := kubectl("get", "clusterrole", managerRole, "-o", "json")
	if err != nil {
		return nil, err
	}
	var role map[string]any
	if err := decodeJSON(out, &role); err != nil {
		return nil, err
	}
	original, err := replaceable(role)
	if err != nil {
		return nil, err
	}
	rules, _ := role["rules"].([]any)
	role["rules"] = withoutNodes(rules)
	revoked, err := replaceable(role)
	if err != nil {
		return nil, err
	}
	if _, err := kubectlIn(revoked, "replace", "-f", "-"); err != nil {
		return nil, err
	}
	return func() error {
		_, err := kubectlIn(original, "replace", "-f", "-")
		return err
	}, nil
}

// replaceable 은 resourceVersion 을 뺀 JSON 이다(replace 가 낡은 버전으로 거부되지 않게).
func replaceable(obj map[string]any) (string, error) {
	if meta, ok := obj["metadata"].(map[string]any); ok {
		delete(meta, "resourceVersion")
	}
	raw, err := json.Marshal(obj)
	return string(raw), err
}

// withoutNodes 는 core API 규칙에서 nodes 를 뺀다 — 남은 자원이 없으면 규칙째.
func withoutNodes(rules []any) []any {
	var kept []any
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok || !slices.Contains(anyStrings(rule["apiGroups"]), "") {
			kept = append(kept, r)
			continue
		}
		resources := slices.DeleteFunc(anyStrings(rule["resources"]), func(s string) bool { return s == "nodes" })
		if len(resources) == 0 {
			continue
		}
		rule["resources"] = resources
		kept = append(kept, rule)
	}
	return kept
}

func anyStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// freezeStandby 는 S6 — standby 의 postmaster 와 자식(walreceiver 포함)을 세운다.
func freezeStandby(obs observation, round int) (injection, error) {
	if err := guardTarget(); err != nil {
		return injection{}, err
	}
	target, err := standbyTarget(obs, round)
	if err != nil {
		return injection{}, err
	}
	return freezeMember(target)
}

// freezeMember 는 멤버의 postmaster 트리를 세운다 — 재구축이 파드를 지웠으면 되살릴 프로세스가 없다.
func freezeMember(target member) (injection, error) {
	pid1, err := containerPID(target.Node, target.ContainerID)
	if err != nil {
		return injection{}, err
	}
	out, err := nodeExec(target.Node, "pgrep", "-P", pid1)
	if err != nil {
		return injection{}, fmt.Errorf("%s postmaster 없음: %w", target.Pod, err)
	}
	postmaster := firstLine(out)
	if err := signalTree(target.Node, "STOP", postmaster); err != nil {
		return injection{}, err
	}
	return injection{undo: func() error {
		_ = signalTree(target.Node, "CONT", postmaster)
		return nil
	}}, nil
}

// detachThenKill 은 S6b — standby 하나의 primary_conninfo 를 무효 호스트로 바꿔(끊김·Ready·LagBytes 0)
// DetachLead 뒤 primary 를 지운다. 기점은 LagBytes 0 인 끊긴 쪽을 올린다 — 수정판은 수신 최대를 올린다(설계 §3.3).
func detachThenKill(obs observation, _ int) (injection, error) {
	if err := guardTarget(); err != nil {
		return injection{}, err
	}
	target, err := standbyTarget(obs, 0)
	if err != nil {
		return injection{}, err
	}
	conninfo := fmt.Sprintf("host=%s port=5432 user=%s application_name=%s", detachedHost, superuserName, target.Pod)
	alter := fmt.Sprintf("ALTER SYSTEM SET primary_conninfo = '%s'", conninfo)
	if _, err := psql(target.Pod, alter, "SELECT pg_reload_conf()"); err != nil {
		return injection{}, err
	}
	time.Sleep(loopdrill.DetachLead)
	now, err := probeCluster()
	if err != nil {
		return injection{}, err
	}
	return deletePrimary(gracefulDelete)(now, 0)
}

// stallRollout 은 S8 — CR 파드 템플릿(CPU 요청 +1m)을 바꿔 단계화 롤아웃을 열고, 첫 교체 replica 의 새 postgres 가
// 뜨면 postmaster 를 세운다. 수렴이 memberConvergeBudget 을 넘어 RolloutBlocked 가 서면 창이 푼다(fault.until).
// 교체가 안 보이면 멈추지 않고 창을 돈다 — J12·J14 가 붉게 남긴다(판정문이 수정자의 입력이다).
func stallRollout(obs observation, _ int) (injection, error) {
	if err := guardTarget(); err != nil {
		return injection{}, err
	}
	old := map[string]string{}
	for _, m := range obs.Members {
		old[m.Pod] = m.UID
	}
	if err := bumpTemplate(); err != nil {
		return injection{}, err
	}
	target, err := waitReplacement(old, loopdrill.MemberConvergeBudget)
	if err != nil {
		return injection{}, nil
	}
	return freezeMember(target)
}

// bumpTemplate 은 CR spec.shards.resources.requests.cpu 를 1m 올린다.
func bumpTemplate() error {
	path := "jsonpath={.spec.shards.resources.requests.cpu}"
	out, err := kubectl("get", "postgrescluster", clusterCR, "-n", dataNamespace, "-o", path)
	if err != nil {
		return err
	}
	next, err := nextCPU(strings.TrimSpace(out))
	if err != nil {
		return err
	}
	patch := fmt.Sprintf(`{"spec":{"shards":{"resources":{"requests":{"cpu":%q}}}}}`, next)
	_, err = kubectl("patch", "postgrescluster", clusterCR, "-n", dataNamespace, "--type=merge", "-p", patch)
	return err
}

// nextCPU 는 CPU 양(코어 또는 m)에 1m 를 더한 값이다.
func nextCPU(q string) (string, error) {
	if milli, ok := strings.CutSuffix(q, cpuMilliSuffix); ok {
		n, err := strconv.Atoi(milli)
		return strconv.Itoa(n+1) + cpuMilliSuffix, err
	}
	cores, err := strconv.ParseFloat(q, 64)
	return strconv.Itoa(int(cores*milliPerCore)+1) + cpuMilliSuffix, err
}

// waitReplacement 는 UID 가 바뀐(오퍼레이터가 교체한) 멤버의 postgres 가 돌 때까지 기다린다.
func waitReplacement(old map[string]string, bound time.Duration) (member, error) {
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if obs, err := probeCluster(); err == nil {
			for _, m := range obs.Members {
				if uid, known := old[m.Pod]; known && uid != m.UID && m.ContainerID != "" && !m.Terminating && m.Role != "" {
					return m, nil
				}
			}
		}
		time.Sleep(loopdrill.SwitchoverPoll)
	}
	return member{}, fmt.Errorf("%s 안에 교체된 replica 가 뜨지 않았다", bound)
}

// emergencyStop 은 S10 — switchover 를 열어 Stopping 을 보면 오퍼레이터 전부를 세우고 pg-stop hold → recover-primary.
// 되돌림은 GitOps 뒤처리 흉내(Deployment 를 원래 복제로) — switchover 상태가 예산을 넘긴 뒤라 새 오퍼레이터가 이어받지 않고 지운다.
// 못 본 단계(Stopping · 세움 · pg-stop 실행)는 멈추지 않고 d.emergency 에 빈 채로 남긴다 — J19 가 붉게 판정한다.
func (d *drill) emergencyStop(obs observation, _ int) (injection, error) {
	if err := guardTarget(); err != nil {
		return injection{}, err
	}
	e := &d.emergency
	state, stopping, err := requestSwitchover(obs)
	if err != nil {
		d.t.Logf("S10 switchover Stopping 미관측: %v", err)
		return injection{}, nil
	}
	e.StoppingAt = stopping

	// 오퍼레이터 전부를 세운다 — 되돌림에서 남은 프로세스가 있으면 푼다
	replicas, pods, frozen, err := freezeOperators(d.operator)
	e.Operators, e.Frozen = pods, len(frozen)
	thaw := func() {
		for _, f := range frozen {
			_, _ = nodeExec(f.node, "kill", "-CONT", f.pid)
		}
	}
	restore := func() error {
		thaw()
		count := "--replicas=" + strconv.Itoa(int(replicas))
		_, err := kubectl("scale", "deployment", d.operator.Deployment, "-n", dataNamespace, count)
		return err
	}
	if err != nil {
		d.t.Logf("S10 오퍼레이터 세우기: %v", err)
		return injection{undo: restore}, nil
	}

	// pg-stop 실행 실패는 종료 코드 -1 로 남는다
	e.HoldExit = d.runPGStop("hold", "--apply", "--no-flux")
	e.RecoverAt = time.Now()
	e.RecoverExit = d.runPGStop("recover-primary", "--apply")
	restoreAt := state.Since.Add(loopdrill.SwitchoverBudget + loopdrill.StatusPollInterval)
	return injection{undo: func() error {
		if wait := time.Until(restoreAt); wait > 0 {
			time.Sleep(wait)
		}
		return restore()
	}}, nil
}

// runPGStop 은 pg-stop 을 부르고 종료 코드를 낸다(실행 실패 = -1) — 출력은 드라이버 로그에만.
func (d *drill) runPGStop(args ...string) int {
	code, out, err := pgStop(args...)
	d.t.Logf("pg-stop %s → %d\n%s", strings.Join(args, " "), code, out)
	if err != nil {
		d.t.Logf("pg-stop %s: %v", args[0], err)
	}
	return code
}

// requestSwitchover 는 수신 최대 standby 를 switchover-target 으로 달고 Stopping 을 기다린다.
// TargetBehind 면 그 메시지가 가리킨 파드로 다시 단다.
func requestSwitchover(obs observation) (switchoverJSON, time.Time, error) {
	target := loopdrill.FreshestStandby(obs.positions())
	if standbys := obs.standbys(); target == "" && len(standbys) > 0 {
		target = standbys[0].Pod // 수신 위치를 모르면 낮은 서수(오퍼레이터 동률 규칙) — 뒤처졌으면 TargetBehind 가 고친다
	}
	if target == "" {
		return switchoverJSON{}, time.Time{}, errors.New("switchover 대상 standby 없음")
	}
	for range switchoverAttempts {
		attempt := time.Now().Truncate(time.Second)
		request := switchoverTargetKey + "=" + target
		args := []string{"annotate", "postgrescluster", clusterCR, "-n", dataNamespace, "--overwrite", request}
		if _, err := kubectl(args...); err != nil {
			return switchoverJSON{}, time.Time{}, err
		}
		next := ""
		for deadline := time.Now().Add(loopdrill.SwitchoverBudget); time.Now().Before(deadline) && next == ""; {
			if st, err := readCluster(); err == nil && st.switchover.Phase == phaseStopping {
				return st.switchover, time.Now(), nil
			}
			next = behindSince(attempt)
			time.Sleep(loopdrill.SwitchoverPoll)
		}
		if next == "" {
			break
		}
		target = next
	}
	return switchoverJSON{}, time.Time{}, errors.New("switchover Stopping 을 보지 못했다")
}

// behindSince 는 since 이후 SwitchoverTargetBehind 가 가리킨 수신 최대 파드다(없으면 "").
func behindSince(since time.Time) string {
	events, err := clusterEvents(since)
	if err != nil {
		return ""
	}
	for _, e := range events {
		if e.Reason == switchoverBehind {
			return loopdrill.BehindTarget(e.Message)
		}
	}
	return ""
}

// frozenProcess 는 세운 오퍼레이터 프로세스 하나.
type frozenProcess struct {
	node string
	pid  string
}

// managerContainer 는 오퍼레이터 파드의 컨테이너 이름(벤더 차트 deployment.yaml).
const managerContainer = "manager"

// freezeOperators 는 오퍼레이터 파드마다 매니저 프로세스(PID 1)를 세운다 — 원래 복제 수 · 파드 수 · 세운 목록.
func freezeOperators(op operatorRef) (int32, int, []frozenProcess, error) {
	out, err := kubectl("get", "deployment", op.Deployment, "-n", dataNamespace, "-o", "jsonpath={.spec.replicas}")
	if err != nil {
		return 0, 0, nil, err
	}
	replicas, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, 0, nil, err
	}
	pods, err := op.running()
	if err != nil {
		return 0, 0, nil, err
	}
	var frozen []frozenProcess
	for _, p := range pods {
		i := slices.IndexFunc(p.Status.ContainerStatuses, func(c containerStatus) bool { return c.Name == managerContainer })
		if i < 0 || p.Metadata.DeletionTimestamp != nil {
			continue
		}
		pid, err := containerPID(p.Spec.NodeName, p.Status.ContainerStatuses[i].ContainerID)
		if err != nil {
			return int32(replicas), len(pods), frozen, err
		}
		if _, err := nodeExec(p.Spec.NodeName, "kill", "-STOP", pid); err != nil {
			return int32(replicas), len(pods), frozen, err
		}
		frozen = append(frozen, frozenProcess{node: p.Spec.NodeName, pid: pid})
	}
	return int32(replicas), len(pods), frozen, nil
}

// ── 창 ──

// recorder 는 창 동안의 표본을 쌓는다(바뀐 것만 — 판정문을 작게). history 는 판정 재료를 만들 때만 쓴다.
type recorder struct {
	start        time.Time
	base         int64
	members      int
	oldPrimary   member // 유발 전 primary — J8 PodReady 기준 · J12 옛 primary
	follow       bool   // switchover 를 SwitchoverPoll 로 좇는다(S0·S8·S10)
	faults       []time.Time
	releasedAt   time.Time
	convergedAt  time.Time
	observeFrom  time.Time
	unreadyAt    time.Time
	timeline     []loopdrill.TimelineMark
	maxPrimaries int
	statusWrites []time.Time
	gauge        []float64
	denied       []float64
	graph        []loopdrill.GraphMark
	snapshots    map[string]loopdrill.SnapshotMark
	endpoints    []endpointMark
	budgetBlock  bool
	blockedSeen  bool
	blockedNow   bool
	switchover   loopdrill.SwitchoverRecord
	restarts     map[string]int32 // 파드가 primary 로 보인 마지막 표본의 재시작 수
	history      []observation
	last         observation
}

// endpointMark 는 -rw Service ready 주소 수가 바뀐 시각.
type endpointMark struct {
	at    time.Time
	count int
}

func newRecorder(base int64, members int, oldPrimary member) *recorder {
	return &recorder{
		start: time.Now(), base: base, members: members, oldPrimary: oldPrimary,
		snapshots: map[string]loopdrill.SnapshotMark{}, restarts: map[string]int32{},
	}
}

// sample 은 가벼운 표본 — 멤버·타임라인·그래프·하트비트·CR·-rw Endpoints.
func (r *recorder) sample() {
	if obs, err := probeCluster(); err == nil {
		r.observe(obs)
		r.beat(obs)
	}
	if st, err := readCluster(); err == nil {
		r.budgetBlock = r.budgetBlock || st.budgetBlocked
		r.blockedSeen = r.blockedSeen || st.rolloutBlocked
		r.blockedNow = st.rolloutBlocked
		if n := len(r.statusWrites); !st.lastWrite.IsZero() && (n == 0 || !st.lastWrite.Equal(r.statusWrites[n-1])) {
			r.statusWrites = append(r.statusWrites, st.lastWrite)
		}
		r.noteSwitchover(st)
	}
	if n, err := rwEndpoints(); err == nil && (len(r.endpoints) == 0 || r.endpoints[len(r.endpoints)-1].count != n) {
		r.endpoints = append(r.endpoints, endpointMark{at: time.Now(), count: n})
	}
}

// observe 는 멤버 표본 하나를 쌓는다.
func (r *recorder) observe(obs observation) {
	r.last = obs
	r.history = append(r.history, obs)
	r.addTimeline(obs)
	r.maxPrimaries = max(r.maxPrimaries, len(obs.primaries()))
	for _, p := range obs.primaries() {
		r.restarts[p.Pod] = p.Restarts
	}
	if g, ok := obs.graphMark(); ok {
		r.addGraph(g)
	}
	r.trackUnready(obs)
	r.trackReturn(obs)
	if !r.releasedAt.IsZero() && r.convergedAt.IsZero() && obs.converged(r.members) {
		r.convergedAt = obs.At
	}
}

// addTimeline 은 primary 들 중 가장 높은 타임라인과 그 파드(split-brain 도 승격으로 센다).
func (r *recorder) addTimeline(obs observation) {
	var top member
	for _, p := range obs.primaries() {
		if p.Timeline > top.Timeline {
			top = p
		}
	}
	if top.Timeline == 0 {
		return
	}
	if n := len(r.timeline); n > 0 && r.timeline[n-1].Timeline == top.Timeline {
		return
	}
	r.timeline = append(r.timeline, loopdrill.TimelineMark{At: obs.At, Timeline: top.Timeline, Primary: top.Pod})
}

func (r *recorder) addGraph(g loopdrill.GraphMark) {
	if n := len(r.graph); n > 0 && sameGraph(r.graph[n-1], g) {
		return
	}
	r.graph = append(r.graph, g)
}

func sameGraph(a, b loopdrill.GraphMark) bool {
	return a.Primary == b.Primary && slices.Equal(a.Connected, b.Connected) && slices.Equal(a.Streaming, b.Streaming)
}

// trackUnready 는 유발 뒤 옛 primary 가 PodReady 를 처음 잃은 시각(없어짐·재생성·Terminating 포함, J8).
func (r *recorder) trackUnready(obs observation) {
	if len(r.faults) == 0 || !r.unreadyAt.IsZero() || r.oldPrimary.Pod == "" {
		return
	}
	m, ok := obs.memberNamed(r.oldPrimary.Pod)
	if !ok || m.UID != r.oldPrimary.UID || m.Terminating || !m.Ready {
		r.unreadyAt = obs.At
	}
}

// noteSwitchover 는 switchover 상태가 보이면 옛 primary 를 적는다 — 재시작 수는 그 파드가 primary 로 보인 마지막 표본의 것.
func (r *recorder) noteSwitchover(st clusterState) {
	sw := st.switchover
	if sw.OldPod == "" || r.switchover.OldPrimary != "" {
		return
	}
	r.switchover.OldPrimary, r.switchover.OldUID = sw.OldPod, sw.OldUID
	r.switchover.OldRestarts = sw.OldRestarts
	if n, seen := r.restarts[sw.OldPod]; seen {
		r.switchover.OldRestarts = n
	}
}

// trackReturn 은 옛 primary 가 재시작해 standby 로 처음 보인 표본을 적는다 — 그때의 checkpoint_lsn 이 정지 체크포인트,
// 그때까지의 컨테이너 로그가 되감기 여부다(J18).
func (r *recorder) trackReturn(obs observation) {
	s := &r.switchover
	if s.OldPrimary == "" || s.BackUID != "" {
		return
	}
	m, ok := obs.memberNamed(s.OldPrimary)
	if !ok || m.Role != roleStandby || m.Restarts <= s.OldRestarts {
		return
	}
	s.BackUID, s.BackRestarts, s.BackInRecovery = m.UID, m.Restarts, true
	if lsn, inRecovery := checkpointLSN(m.Pod); inRecovery {
		s.OldEnd = lsn
	}

	// 되감기는 복귀한 컨테이너 기동에서 일어난다 — 롤아웃이 이 파드를 교체하면 로그가 사라지니 지금 센다
	s.RewindLines = rewindLines(m.Pod, r.start)
}

// followSwitchover 는 switchover 동안 SwitchoverPoll 로 CR 과 옛 primary 만 본다 — 5 s 표본은 복귀 순간을 놓친다.
func (r *recorder) followSwitchover() {
	st, err := readCluster()
	if err != nil {
		return
	}
	r.noteSwitchover(st)
	if r.switchover.OldPrimary == "" || r.switchover.BackUID != "" {
		return
	}
	if obs, err := probeCluster(); err == nil {
		r.observe(obs)
	}
}

// beat 는 하트비트 행을 쓴다 — pg_database_size 가 커져 CR status 가 바뀐다(J4 재료).
func (r *recorder) beat(obs observation) {
	if primary, ok := obs.primary(); ok {
		_ = heartbeat(primary.Pod)
	}
}

// sampleSlow 는 무거운 표본 — 멤버 스냅숏(라벨 · readyToUse · 사라짐) · (GREEN) 게이지·거부 계수.
func (r *recorder) sampleSlow(gauge *gaugeScraper) {
	if snapshots, err := listSnapshots(); err == nil {
		r.addSnapshots(snapshots)
	}
	if gauge == nil {
		return
	}
	v, err := gauge.read()
	if err != nil {
		return
	}
	if len(r.gauge) == 0 || r.gauge[len(r.gauge)-1] != v.gauge {
		r.gauge = append(r.gauge, v.gauge)
	}
	if len(r.denied) == 0 || r.denied[len(r.denied)-1] != v.denied {
		r.denied = append(r.denied, v.denied)
	}
}

// addSnapshots 는 지금 목록으로 스냅숏 표본을 고친다 — 처음 ready 로 본 시각을 남기고, 목록에서 빠지면 사라짐.
func (r *recorder) addSnapshots(snapshots []snapshotJSON) {
	present := map[string]bool{}
	for _, s := range snapshots {
		present[s.Metadata.Name] = true
		mark, seen := r.snapshots[s.Metadata.Name]
		if !seen {
			mark = snapshotMark(s)
		}
		if mark.ReadyAt.IsZero() && s.ready() {
			mark.ReadyAt = time.Now()
		}
		r.snapshots[s.Metadata.Name] = mark
	}
	for name, mark := range r.snapshots {
		mark.Gone = !present[name]
		r.snapshots[name] = mark
	}
}

// pause 는 다음 표본까지 기다린다 — switchover 를 좇는 창이면 SwitchoverPoll 마다 CR·옛 primary 를 본다.
func (r *recorder) pause() {
	deadline := time.Now().Add(loopdrill.StatusPollInterval)
	for r.follow && time.Now().Add(loopdrill.SwitchoverPoll).Before(deadline) {
		time.Sleep(loopdrill.SwitchoverPoll)
		r.followSwitchover()
	}
	if wait := time.Until(deadline); wait > 0 {
		time.Sleep(wait)
	}
}

// window 는 유발부터 관측창 끝까지 돈다. RED 는 풀린 뒤 기대한 붉음이 보이면 끝낸다.
func (d *drill) window(scn loopdrill.Scenario, f fault, obs observation, before loopdrill.ClusterSnap, rec *recorder) error {
	tick := 0
	step := func() {
		rec.sample()
		if tick%slowSampleEvery == 0 {
			rec.sampleSlow(d.gauge)
		}
		tick++
		rec.pause()
	}

	// 유발 · 유지 · 되돌림(S7 은 대상 바꿔 되풀이, 사이마다 수렴을 기다린다)
	for round := range f.rounds {
		if round > 0 {
			if next, err := probeCluster(); err == nil {
				obs = next
			}
		}
		inj, err := f.inject(obs, round)
		if err != nil {
			return fmt.Errorf("%s 유발: %w", scn, err)
		}
		rec.faults = append(rec.faults, time.Now())
		if err := hold(f, inj, rec, step); err != nil {
			return fmt.Errorf("%s 되돌림: %w", scn, err)
		}
		for settle := time.Now().Add(loopdrill.ConvergeBound); round < f.rounds-1 && time.Now().Before(settle) && !rec.last.converged(rec.members); {
			step()
		}
	}

	// 수렴(상한 ConvergeWithin) 뒤 관측창
	rec.releasedAt = time.Now()
	within := loopdrill.ExpectFor(scn, d.cfg.mode, rec.members-1).ConvergeWithin
	for {
		step()
		if rec.observeFrom.IsZero() && (!rec.convergedAt.IsZero() || time.Since(rec.releasedAt) >= within) {
			rec.observeFrom = time.Now()
		}
		if !rec.observeFrom.IsZero() && time.Since(rec.observeFrom) >= loopdrill.LoopObservationWindow {
			return nil
		}
		if d.cfg.mode == loopdrill.ModeRed && tick%slowSampleEvery == 0 && d.redShown(scn, before, rec) {
			return nil
		}
	}
}

// hold 는 유발을 hold 동안(또는 until 이 설 때까지) 유지하고 되돌린다.
// 되돌림은 타이머가 제시각에 — 표본이 API 끊김으로 늘어져도 유발 길이는 hold 그대로다.
func hold(f fault, inj injection, rec *recorder, step func()) error {
	undone := make(chan error, 1)
	var once sync.Once
	release := func() { once.Do(func() { undone <- inj.revert() }) }
	timer := time.AfterFunc(f.hold, release)
	defer timer.Stop()
	for {
		select {
		case err := <-undone:
			return err
		default:
			step()
			if f.until != nil && f.until(rec) {
				go release()
			}
		}
	}
}

// redShown 은 RED 의 중간 판정 — 이 시나리오에서 기대한 붉음이 이미 섰나.
func (d *drill) redShown(scn loopdrill.Scenario, before loopdrill.ClusterSnap, rec *recorder) bool {
	after, err := d.collect(scn, rec, nil, before, collectInterim)
	if err != nil {
		return false
	}
	verdict := loopdrill.Judge(scn, before, after, loopdrill.ExpectFor(scn, d.cfg.mode, rec.members-1))
	return verdict.AnyRed(loopdrill.RedTargets(scn)...)
}

// collectMode 는 판정 재료를 얼마나 모을지 — before·RED 중간 판정은 창 끝 재료(탐침 대조·hba·설정)를 뺀다.
type collectMode string

const (
	collectBefore  collectMode = "before"
	collectInterim collectMode = "interim"
	collectAfter   collectMode = "after"
)

// collect 는 recorder 의 표본과 지금 클러스터로 ClusterSnap 을 만든다. oldPods = S0 의 옛 오퍼레이터 파드.
func (d *drill) collect(
	scn loopdrill.Scenario,
	rec *recorder,
	oldPods []string,
	before loopdrill.ClusterSnap,
	mode collectMode,
) (loopdrill.ClusterSnap, error) {
	snap := loopdrill.ClusterSnap{
		TakenAt:       time.Now(),
		Timeline:      slices.Clone(rec.timeline),
		MaxPrimaries:  rec.maxPrimaries,
		StatusWrites:  slices.Clone(rec.statusWrites),
		GaugeWrites:   slices.Clone(rec.gauge),
		DeniedReads:   slices.Clone(rec.denied),
		Graph:         slices.Clone(rec.graph),
		Faults:        slices.Clone(rec.faults),
		UnreadyAt:     rec.unreadyAt,
		ReleasedAt:    rec.releasedAt,
		ConvergedAt:   rec.convergedAt,
		ObserveFrom:   rec.observeFrom,
		BudgetBlocked: rec.budgetBlock,
		Switchover:    rec.switchover,
	}
	obs, err := probeCluster()
	if err != nil {
		return loopdrill.ClusterSnap{}, err
	}
	snap.StsGeneration, snap.UpdateRevision, snap.UpdateStrategy = obs.Generation, obs.UpdateRevision, obs.UpdateStrategy
	for _, m := range obs.Members {
		if m.Ready && m.Revision == obs.UpdateRevision {
			snap.PodsOnUpdate++
		}
	}
	if snap.Revisions, err = stsRevisions(); err != nil {
		return loopdrill.ClusterSnap{}, err
	}
	if snap.PVCUIDs, err = pvcUIDs(); err != nil {
		return loopdrill.ClusterSnap{}, err
	}
	if snap.ReleasedPVs, err = releasedPVs(); err != nil {
		return loopdrill.ClusterSnap{}, err
	}
	if snap.FailedWatchLines, err = d.operator.failedWatchLines(rec.start); err != nil {
		return loopdrill.ClusterSnap{}, err
	}
	audit, err := readAudit(d.operator.ServiceAccount, rec.start, oldPods)
	if err != nil {
		return loopdrill.ClusterSnap{}, err
	}
	snap.AuditPVCDeletes, snap.AuditPodDeletes, snap.AuditStsDeletes = audit.pvcDeletes, audit.oldDeletes, audit.stsDeletes
	events, err := clusterEvents(rec.start)
	if err != nil {
		return loopdrill.ClusterSnap{}, err
	}
	snapshots, err := listSnapshots()
	if err != nil {
		return loopdrill.ClusterSnap{}, err
	}
	rec.addSnapshots(snapshots)
	snap.Snapshots = sortedSnapshots(rec.snapshots)
	snap.Rebuilds = rebuildMarks(events, snapshots)
	snap.HeldReasons = heldReasons(events)
	snap.Events = foldEvents(events)
	snap.Placement = d.placement(obs)
	if mode != collectAfter {
		return snap, nil
	}

	// 창 끝 — 롤아웃 순서 · switchover 완료 · 탐침 대조 · hba · 비상 정지 · (S0) 설정 · (S6b) VAP
	snap.Rollout = rec.rollout(audit.podDeletes, events, d.operator.user())
	finishSwitchover(&snap, events)
	snap.Probe = probeRecord(before.TakenAt, snap.TakenAt, obs)
	snap.HBA = d.hbaRecord(obs)
	snap.Emergency = d.emergencyRecord(rec, snap.Probe)
	if scn == loopdrill.ScenarioS0 {
		snap.Settings = settingsOf(obs)
		snap.C1Faults = slices.Clone(d.c1Faults)
	}
	if scn == loopdrill.ScenarioS6b {
		snap.VAP = vapProbe(freshDataBearing(before, snap), d.operator.ServiceAccount)
	}
	return snap, nil
}

func sortedSnapshots(marks map[string]loopdrill.SnapshotMark) []loopdrill.SnapshotMark {
	out := make([]loopdrill.SnapshotMark, 0, len(marks))
	for _, m := range marks {
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b loopdrill.SnapshotMark) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// freshDataBearing 은 창에서 새로 생긴 data-bearing 스냅숏 하나(VAP 시험 대상) — 없으면 "".
func freshDataBearing(before, after loopdrill.ClusterSnap) string {
	old := map[string]bool{}
	for _, s := range before.Snapshots {
		old[s.Name] = true
	}
	for _, s := range after.Snapshots {
		if s.DataBearing == loopdrill.LabelTrue && !s.Gone && !old[s.Name] {
			return s.Name
		}
	}
	return ""
}

// placement 는 창 끝 멤버 노드와 노드 분류(J11).
func (d *drill) placement(obs observation) loopdrill.PlacementRecord {
	pl := loopdrill.PlacementRecord{Nodes: map[string]string{}, Eligible: d.eligible, Avoid: d.avoid}
	for _, m := range obs.Members {
		pl.Nodes[m.Pod] = m.Node
	}
	return pl
}

// rollout 은 창 안 멤버 pod delete 마다 그때의 그래프와 교체 복귀 시각을 붙인다(J12).
func (r *recorder) rollout(deletes []podDelete, events []loopdrill.EventMark, operator string) loopdrill.RolloutRecord {
	rec := loopdrill.RolloutRecord{
		Operator: operator, OldPrimary: r.oldPrimary.Pod, BlockedSeen: r.blockedSeen, BlockedAtEnd: r.blockedNow,
		SwitchStart: firstEvent(events, switchoverStarted), SwitchDone: firstEvent(events, switchoverCompleted),
	}
	for _, del := range deletes {
		rec.Steps = append(rec.Steps, r.step(del))
	}
	return rec
}

// step 은 pod delete 하나 — 직전 표본의 그래프로 끊김·대상 밖 streaming 을, 뒤 표본으로 새 파드 복귀를 본다.
func (r *recorder) step(del podDelete) loopdrill.RolloutStep {
	s := loopdrill.RolloutStep{Pod: del.Pod, DeletedAt: del.At, By: del.User}
	oldUID := ""
	for _, obs := range r.history {
		if !obs.At.Before(del.At) {
			break
		}
		g, ok := obs.graphMark()
		m, found := obs.memberNamed(del.Pod)
		if !ok || !found {
			continue
		}
		oldUID = m.UID
		s.Detached = del.Pod != g.Primary && !slices.Contains(g.Connected, del.Pod)
		s.Streaming = othersStreaming(obs, g, del.Pod)
	}
	for _, obs := range r.history {
		if obs.At.Before(del.At) {
			continue
		}
		g, ok := obs.graphMark()
		m, found := obs.memberNamed(del.Pod)
		if !ok || !found || m.UID == oldUID || !m.Ready || m.Revision != obs.UpdateRevision {
			continue
		}
		if m.Pod == g.Primary || slices.Contains(g.Streaming, m.Pod) {
			s.ReadyAt = obs.At
			break
		}
	}
	return s
}

// othersStreaming 은 pod·primary 밖 멤버가 전부 streaming 인가.
func othersStreaming(obs observation, g loopdrill.GraphMark, pod string) bool {
	for _, m := range obs.Members {
		if m.Pod == pod || m.Pod == g.Primary {
			continue
		}
		if !slices.Contains(g.Streaming, m.Pod) {
			return false
		}
	}
	return true
}

// finishSwitchover 는 창 끝 switchover 기록 — 완료 이벤트 · 새 분기점(되감기 줄은 복귀 때 센다).
func finishSwitchover(snap *loopdrill.ClusterSnap, events []loopdrill.EventMark) {
	s := &snap.Switchover
	s.Completed = !firstEvent(events, switchoverCompleted).IsZero()
	if st, err := readCluster(); err == nil {
		s.Fork = st.forkLSN
	}
}

// probeRecord 는 [from, to] 탐침 ack 과 최종 primary 에 남은 id 다(J10 · J15).
func probeRecord(from, to time.Time, obs observation) loopdrill.ProbeRecord {
	p := loopdrill.ProbeRecord{From: from, To: to}
	acks, err := probeAcks()
	if err != nil {
		return p
	}
	for _, a := range acks {
		if !a.At.Before(from) && !a.At.After(to) {
			p.Acks = append(p.Acks, a)
		}
	}
	primary, ok := obs.primary()
	lo, hi, spanned := loopdrill.AckSpan(p.Acks)
	if !ok || !spanned {
		return p
	}
	if stored, err := storedIDs(primary.Pod, lo, hi); err == nil {
		p.Stored, p.Primary = stored, primary.Pod
	}
	return p
}

// hbaRecord 는 창 끝 멤버별 적재 기준 hba 판정과 비멤버 접속 셋(J17).
func (d *drill) hbaRecord(obs observation) loopdrill.HBARecord {
	var h loopdrill.HBARecord
	for _, m := range obs.Members {
		h.Members = append(h.Members, hbaJudgment(m.Pod))
	}
	h.NoPassword = connectAs(connectNoPassword, "")
	h.Probe = connectAs(connectProbe, "")
	h.Superuser = connectAs(connectSuperuser, d.secrets.superuser)
	return h
}

// emergencyRecord 는 S10 기록에 recover 뒤 -rw Endpoints 1 · 첫 ack 시각을 붙인다(J19).
func (d *drill) emergencyRecord(rec *recorder, probe loopdrill.ProbeRecord) loopdrill.EmergencyRecord {
	e := d.emergency
	if e.RecoverAt.IsZero() {
		return e
	}
	for _, m := range rec.endpoints {
		if !m.at.Before(e.RecoverAt) && m.count == 1 {
			e.EndpointsAt = m.at
			break
		}
	}
	for _, a := range probe.Acks {
		if !a.At.Before(e.RecoverAt) {
			e.WriteAt = a.At
			break
		}
	}
	return e
}

// settingsOf 는 멤버마다 운영 누적물 정리 상태(S0).
func settingsOf(obs observation) []loopdrill.MemberSettings {
	out := make([]loopdrill.MemberSettings, 0, len(obs.Members))
	for _, m := range obs.Members {
		out = append(out, memberSettings(m.Pod))
	}
	return out
}

// checkMarker 는 창 끝에 표식 행을 본다 — primary 가 여럿이면 전부에 있어야 유지다.
func checkMarker(id int64) loopdrill.WriteMark {
	obs, err := probeCluster()
	if err != nil {
		return loopdrill.WriteMark{}
	}
	mark := loopdrill.WriteMark{MarkerKept: true}
	for _, p := range obs.primaries() {
		n, err := markerCount(p.Pod, id)
		if err != nil {
			continue
		}
		mark.MarkerChecked = true
		mark.MarkerKept = mark.MarkerKept && n == 1
	}
	mark.MarkerKept = mark.MarkerKept && mark.MarkerChecked
	return mark
}
