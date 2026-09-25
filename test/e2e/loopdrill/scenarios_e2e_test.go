//go:build e2e
// +build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// 시나리오 층 — 유발(fault)과 창(수렴 상한 + 관측창) 동안의 표본.
//
//	prepare ─ before ─ inject ── hold ── release ── 수렴(≤ ConvergeBound) ── LoopObservationWindow ── after
//	                                                 └ RED 는 기대한 붉음이 보이면 여기서 끝낸다(양성 대조가 섰다)

const (
	// slowSampleEvery 는 무거운 표본(로그·감사·스냅숏·게이지·RED 중간 판정)을 몇 틱마다 뜰지.
	slowSampleEvery = 6
	markerBound     = 2 * time.Minute
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
	hold    time.Duration                                       // 유발을 유지하는 시간
	rounds  int                                                 // S7 = 예산+1
}

func faultFor(scn Scenario, budget int) (fault, error) {
	switch scn {
	case ScenarioS1:
		return fault{inject: deletePrimary(gracefulDelete), rounds: 1}, nil
	case ScenarioS2:
		return fault{inject: deletePrimary(forceDelete), rounds: 1}, nil
	case ScenarioS3:
		return fault{inject: stopPrimaryManager, hold: FaultPause, rounds: 1}, nil
	case ScenarioS4:
		return fault{inject: pauseControlPlane, hold: FaultPause, rounds: 1}, nil
	case ScenarioS5:
		return fault{prepare: waitStandbyAge, inject: stopStandbyNode(nodeRBACAsIs), hold: StallStopDuration, rounds: 1}, nil
	case ScenarioS5b:
		return fault{prepare: waitStandbyAge, inject: stopStandbyNode(nodeRBACGranted), hold: StallStopDuration, rounds: 1}, nil
	case ScenarioS6:
		return fault{inject: freezeStandby, hold: StandbyFreeze, rounds: 1}, nil
	case ScenarioS7:
		return fault{inject: freezeStandby, hold: StandbyFreeze, rounds: budget + 1}, nil
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
	node := controlPlaneNode()
	if err := pauseNode(node); err != nil {
		return injection{}, err
	}
	return injection{undo: func() error { return unpauseNode(node) }}, nil
}

// standbyTarget 은 primary 와 다른 노드의 standby 하나(anti-affinity 로 노드당 멤버 1).
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
	if wait := StandbyAgeFloor - time.Since(target.Created); wait > 0 {
		time.Sleep(wait)
	}
	return nil
}

type nodeRBAC string

const (
	nodeRBACAsIs    nodeRBAC = "as-is"
	nodeRBACGranted nodeRBAC = "granted" // S5b — D2 모사: 오퍼레이터 ClusterRole 에 nodes get·list·watch
)

// stopStandbyNode 는 S5 — standby 가 있는 워커를 세운다(노드 사망 모사).
func stopStandbyNode(rbac nodeRBAC) func(observation, int) (injection, error) {
	return func(obs observation, round int) (injection, error) {
		target, err := standbyTarget(obs, round)
		if err != nil {
			return injection{}, err
		}
		revoke := func() error { return nil }
		if rbac == nodeRBACGranted {
			if revoke, err = grantNodeReads(); err != nil {
				return injection{}, err
			}
		}
		if err := stopNode(target.Node); err != nil {
			return injection{}, errors.Join(err, revoke())
		}
		return injection{undo: func() error { return errors.Join(startNode(target.Node), revoke()) }}, nil
	}
}

const managerRole = releaseName + "-manager-role"

// grantNodeReads 는 오퍼레이터 ClusterRole 끝에 nodes 읽기 규칙을 붙이고 떼는 함수를 낸다.
func grantNodeReads() (func() error, error) {
	var role struct {
		Rules []any `json:"rules"`
	}
	if err := kubectlJSON(&role, "get", "clusterrole", managerRole); err != nil {
		return nil, err
	}
	index := len(role.Rules)
	rule := `[{"op":"add","path":"/rules/-","value":{"apiGroups":[""],"resources":["nodes"],"verbs":["get","list","watch"]}}]`
	if _, err := kubectl("patch", "clusterrole", managerRole, "--type=json", "-p", rule); err != nil {
		return nil, err
	}
	return func() error {
		_, err := kubectl("patch", "clusterrole", managerRole, "--type=json",
			"-p", fmt.Sprintf(`[{"op":"remove","path":"/rules/%d"}]`, index))
		return err
	}, nil
}

// freezeStandby 는 S6 — standby 의 postmaster 와 자식(walreceiver 포함)을 세운다.
func freezeStandby(obs observation, round int) (injection, error) {
	target, err := standbyTarget(obs, round)
	if err != nil {
		return injection{}, err
	}
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
	// 재구축이 파드를 지웠으면 프로세스는 이미 없다 — 되살릴 것도 없다
	return injection{undo: func() error {
		_ = signalTree(target.Node, "CONT", postmaster)
		return nil
	}}, nil
}

// ── 창 ──

// recorder 는 창 동안의 표본을 쌓는다(바뀐 것만 — 판정문을 작게).
type recorder struct {
	start         time.Time
	base          int64
	members       int
	faults        []time.Time
	observeFrom   time.Time
	timeline      []TimelineMark
	statusWrites  []time.Time
	gauge         []float64
	graph         []GraphMark
	ready         map[string]SnapshotMark
	budgetBlocked bool
	writableAt    time.Time
	last          observation
}

func newRecorder(base int64, members int) *recorder {
	return &recorder{start: time.Now(), base: base, members: members, ready: map[string]SnapshotMark{}}
}

// sample 은 가벼운 표본 — 멤버·타임라인·그래프·하트비트·CR status 쓰기.
func (r *recorder) sample() {
	if obs, err := probeCluster(); err == nil {
		r.last = obs
		r.addTimeline(obs)
		if g, ok := obs.graphMark(); ok {
			r.addGraph(g)
		}
		r.beat(obs)
	}
	if lastWrite, blocked, err := readCluster(); err == nil {
		r.budgetBlocked = r.budgetBlocked || blocked
		if !lastWrite.IsZero() && (len(r.statusWrites) == 0 || !lastWrite.Equal(r.statusWrites[len(r.statusWrites)-1])) {
			r.statusWrites = append(r.statusWrites, lastWrite)
		}
	}
}

// addTimeline 은 primary 들 중 가장 높은 타임라인(split-brain 도 승격으로 센다).
func (r *recorder) addTimeline(obs observation) {
	var top int64
	for _, p := range obs.primaries() {
		top = max(top, p.Timeline)
	}
	if top == 0 {
		return
	}
	if n := len(r.timeline); n > 0 && r.timeline[n-1].Timeline == top {
		return
	}
	r.timeline = append(r.timeline, TimelineMark{At: obs.At, Timeline: top})
}

func (r *recorder) addGraph(g GraphMark) {
	if n := len(r.graph); n > 0 && r.graph[n-1].Primary == g.Primary && slices.Equal(r.graph[n-1].Connected, g.Connected) {
		return
	}
	r.graph = append(r.graph, g)
}

// beat 는 하트비트 행을 쓴다 — 유발 뒤 새 타임라인 primary 에서 첫 성공이 J8 의 쓰기 가능 시각.
func (r *recorder) beat(obs observation) {
	primary, ok := obs.primary()
	if !ok || heartbeat(primary.Pod) != nil {
		return
	}
	if r.writableAt.IsZero() && len(r.faults) > 0 && primary.Timeline > r.base {
		r.writableAt = time.Now()
	}
}

// sampleSlow 는 무거운 표본 — 멤버 스냅숏 readyToUse · (GREEN) 게이지.
func (r *recorder) sampleSlow(gauge *gaugeScraper) {
	if snapshots, err := listSnapshots(); err == nil {
		for _, s := range snapshots {
			if _, seen := r.ready[s.Metadata.Name]; seen || s.Status == nil || s.Status.ReadyToUse == nil || !*s.Status.ReadyToUse {
				continue
			}
			r.ready[s.Metadata.Name] = SnapshotMark{Name: s.Metadata.Name, Member: snapshotMember(s.Metadata.Name), ReadyAt: time.Now()}
		}
	}
	if gauge == nil {
		return
	}
	if v, err := gauge.read(); err == nil && (len(r.gauge) == 0 || r.gauge[len(r.gauge)-1] != v) {
		r.gauge = append(r.gauge, v)
	}
}

// window 는 유발부터 관측창 끝까지 돈다. redTargets 가 있으면(RED) 풀린 뒤 그 붉음이 보일 때 끝낸다.
func (d *drill) window(scn Scenario, f fault, obs observation, before ClusterSnap, rec *recorder) error {
	tick := 0
	step := func() {
		rec.sample()
		if tick%slowSampleEvery == 0 {
			rec.sampleSlow(d.gauge)
		}
		tick++
		time.Sleep(StatusPollInterval)
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

		// 되돌림은 타이머가 제시각에 — 표본이 API 끊김으로 늘어져도 유발 길이는 hold 그대로다
		undone := make(chan error, 1)
		time.AfterFunc(f.hold, func() { undone <- inj.revert() })
		for waiting := true; waiting; {
			select {
			case err := <-undone:
				if err != nil {
					return fmt.Errorf("%s 되돌림: %w", scn, err)
				}
				waiting = false
			default:
				step()
			}
		}
		for settle := time.Now().Add(ConvergeBound); round < f.rounds-1 && time.Now().Before(settle) && !rec.last.converged(rec.members); {
			step()
		}
	}

	// 수렴(상한 ConvergeBound) 뒤 관측창
	released := time.Now()
	for {
		step()
		if rec.observeFrom.IsZero() && (rec.last.converged(rec.members) || time.Since(released) >= ConvergeBound) {
			rec.observeFrom = time.Now()
		}
		if !rec.observeFrom.IsZero() && time.Since(rec.observeFrom) >= LoopObservationWindow {
			return nil
		}
		if d.cfg.mode == ModeRed && tick%slowSampleEvery == 0 && d.redShown(scn, before, rec) {
			return nil
		}
	}
}

// redShown 은 RED 의 중간 판정 — 이 시나리오에서 기대한 붉음이 이미 섰나.
func (d *drill) redShown(scn Scenario, before ClusterSnap, rec *recorder) bool {
	after, err := d.collect(rec, nil)
	if err != nil {
		return false
	}
	verdict := Judge(scn, before, after, ExpectFor(scn, d.cfg.mode, rec.members-1))
	return verdict.AnyRed(redTargets(scn)...)
}

// redTargets 는 RED 에서 붉어야 할 판정 — 정지는 J4, 그 밖(루프·누수)은 J1·J2·J3·J5.
func redTargets(scn Scenario) []CheckID {
	if scn == ScenarioS5 || scn == ScenarioS5b {
		return []CheckID{CheckStall}
	}
	return []CheckID{CheckTemplate, CheckPVC, CheckPromotions, CheckLeak}
}

// collect 는 recorder 의 표본과 지금 클러스터로 ClusterSnap 을 만든다. oldPods = S0 의 옛 오퍼레이터 파드.
func (d *drill) collect(rec *recorder, oldPods []string) (ClusterSnap, error) {
	snap := ClusterSnap{
		TakenAt:       time.Now(),
		Timeline:      slices.Clone(rec.timeline),
		StatusWrites:  slices.Clone(rec.statusWrites),
		GaugeWrites:   slices.Clone(rec.gauge),
		Graph:         slices.Clone(rec.graph),
		Faults:        slices.Clone(rec.faults),
		ObserveFrom:   rec.observeFrom,
		BudgetBlocked: rec.budgetBlocked,
		Writes:        WriteMark{WritableAt: rec.writableAt},
	}
	obs, err := probeCluster()
	if err != nil {
		return ClusterSnap{}, err
	}
	snap.StsGeneration, snap.UpdateRevision = obs.Generation, obs.UpdateRevision
	for _, m := range obs.Members {
		if m.Ready && m.Revision == obs.UpdateRevision {
			snap.PodsOnUpdate++
		}
	}
	if snap.Revisions, err = stsRevisions(); err != nil {
		return ClusterSnap{}, err
	}
	if snap.PVCUIDs, err = pvcUIDs(); err != nil {
		return ClusterSnap{}, err
	}
	if snap.ReleasedPVs, err = releasedPVs(); err != nil {
		return ClusterSnap{}, err
	}
	if snap.FailedWatchLines, err = d.operator.failedWatchLines(rec.start); err != nil {
		return ClusterSnap{}, err
	}
	if snap.AuditPVCDeletes, snap.AuditPodDeletes, err = auditDeletes(d.operator.ServiceAccount, rec.start, oldPods); err != nil {
		return ClusterSnap{}, err
	}
	events, err := clusterEvents(rec.start)
	if err != nil {
		return ClusterSnap{}, err
	}
	snapshots, err := listSnapshots()
	if err != nil {
		return ClusterSnap{}, err
	}
	snap.Rebuilds = rebuildMarks(events, snapshots)
	snap.Events = foldEvents(events)
	for _, mark := range rec.ready {
		snap.Snapshots = append(snap.Snapshots, mark)
	}
	slices.SortFunc(snap.Snapshots, func(a, b SnapshotMark) int { return strings.Compare(a.Name, b.Name) })
	return snap, nil
}

// checkMarker 는 창 끝에 표식 행을 본다 — primary 가 여럿이면 전부에 있어야 유지다.
func checkMarker(id int64) WriteMark {
	obs, err := probeCluster()
	if err != nil {
		return WriteMark{}
	}
	mark := WriteMark{MarkerKept: true}
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
