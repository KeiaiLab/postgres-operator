/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// 판정 J1–J9(설계 §3.12)는 시나리오 전후 스냅숏 두 장만 보는 순수 함수다.
// 드라이버가 before 를 유발 직전에, after 를 창 끝에 찍는다 — after 는 창 동안 쌓은 표본을 싣는다.
//
//	before ── 유발(Faults) ── 수렴 ── ObserveFrom ── 관측창(LoopObservationWindow) ── after
//
// 빈 스냅숏(수집 실패)은 모든 판정이 붉다 — 못 본 것을 초록으로 읽지 않는다.

// Scenario 는 훈련장 시나리오 식별자.
type Scenario string

const (
	ScenarioS0  Scenario = "S0"  // 전환 — 기점 오퍼레이터·운영 차트에서 수정판으로 helm upgrade
	ScenarioS1  Scenario = "S1"  // primary 파드 삭제
	ScenarioS2  Scenario = "S2"  // primary 파드 강제 삭제
	ScenarioS3  Scenario = "S3"  // primary 인스턴스 매니저 SIGSTOP
	ScenarioS4  Scenario = "S4"  // control-plane docker pause
	ScenarioS5  Scenario = "S5"  // standby 워커 docker stop
	ScenarioS5b Scenario = "S5b" // S5 + nodes 읽기 권한(D2 모사)
	ScenarioS6  Scenario = "S6"  // standby postmaster SIGSTOP
	ScenarioS7  Scenario = "S7"  // S6 을 예산+1회
)

// Mode 는 판정문이 기대하는 결과(LOOPDRILL_EXPECT).
type Mode string

const (
	ModeRed   Mode = "red"   // 기점 코드 — 결함이 판정에 잡혀야 한다(양성 대조)
	ModeGreen Mode = "green" // 수정판 — 전 판정 초록
)

// CheckID 는 판정 식별자.
type CheckID string

const (
	CheckTemplate   CheckID = "J1" // STS 템플릿 불변
	CheckPVC        CheckID = "J2" // 데이터 PVC 불변 · PVC 삭제 0
	CheckPromotions CheckID = "J3" // 승격 수
	CheckStall      CheckID = "J4" // 컨트롤러 정지 0
	CheckLeak       CheckID = "J5" // Released PV 증가 0
	CheckSource     CheckID = "J6" // 풀기 전 스냅숏 readyToUse
	CheckBudget     CheckID = "J7" // 재구축 예산
	CheckFailover   CheckID = "J8" // 새 primary 쓰기 가능 · 확인한 행 유지
	CheckSurvivors  CheckID = "J9" // 생존 standby 재접속 · 재구축 0
	CheckTransition CheckID = "S0" // 전환 롤아웃
)

// StatusAdvanceFloor 는 J4 — 관측창 안 CR status 쓰기 전진 하한(설계 "전진 ≥2").
const StatusAdvanceFloor = 2

// TimelineMark 는 관측한 primary 타임라인 한 점.
type TimelineMark struct {
	At       time.Time `json:"at"`
	Timeline int64     `json:"timeline"`
}

// SnapshotMark 는 멤버 스냅숏과 readyToUse 를 처음 본 시각.
type SnapshotMark struct {
	Name    string    `json:"name"`
	Member  string    `json:"member"`
	ReadyAt time.Time `json:"readyAt"`
}

// RebuildMark 는 재구축의 풀기(rejoin·reseed) 한 번과 그것이 기대는 스냅숏.
type RebuildMark struct {
	Member     string    `json:"member"`
	Reason     string    `json:"reason"`
	ReleasedAt time.Time `json:"releasedAt"`
	Snapshot   string    `json:"snapshot"`
}

// EventMark 는 오퍼레이터가 PostgresCluster 에 낸 이벤트(증거 — 판정은 아래 필드로).
// 같은 (Type, Reason, Message) 는 첫 시각 하나와 Count 로 접는다.
type EventMark struct {
	At      time.Time `json:"at"`
	Type    string    `json:"type"`
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	Count   int       `json:"count"`
}

// GraphMark 는 primary 의 pg_stat_replication 한 표본(연결 = streaming·catchup·backup).
type GraphMark struct {
	At        time.Time `json:"at"`
	Primary   string    `json:"primary"`
	Connected []string  `json:"connected"`
}

// WriteMark 는 J8 의 쓰기 표본.
type WriteMark struct {
	WritableAt    time.Time `json:"writableAt"`    // 유발 뒤 새 타임라인 primary 에 첫 쓰기가 성공한 시각
	MarkerChecked bool      `json:"markerChecked"` // 창 끝에 표식 행을 확인할 수 있었나
	MarkerKept    bool      `json:"markerKept"`    // 유발 전 모든 멤버에서 확인한 행이 남았나
}

// ClusterSnap 은 한 시점의 클러스터 판정 재료다.
type ClusterSnap struct {
	TakenAt          time.Time         `json:"takenAt"`
	StsGeneration    int64             `json:"stsGeneration"`
	UpdateRevision   string            `json:"updateRevision"`
	Revisions        []string          `json:"revisions"`
	PVCUIDs          map[string]string `json:"pvcUIDs"`
	ReleasedPVs      int               `json:"releasedPVs"`
	Timeline         []TimelineMark    `json:"timeline"`
	StatusWrites     []time.Time       `json:"statusWrites"`
	GaugeWrites      []float64         `json:"gaugeWrites,omitempty"`
	FailedWatchLines int               `json:"failedWatchLines"`
	AuditPVCDeletes  int               `json:"auditPVCDeletes"`
	AuditPodDeletes  int               `json:"auditPodDeletes"`
	Snapshots        []SnapshotMark    `json:"snapshots,omitempty"`
	Rebuilds         []RebuildMark     `json:"rebuilds,omitempty"`
	Events           []EventMark       `json:"events,omitempty"`
	Graph            []GraphMark       `json:"graph,omitempty"`
	Writes           WriteMark         `json:"writes"`
	Faults           []time.Time       `json:"faults,omitempty"`
	ObserveFrom      time.Time         `json:"observeFrom,omitempty"`
	BudgetBlocked    bool              `json:"budgetBlocked"`
	PodsOnUpdate     int               `json:"podsOnUpdate"`
}

// Expect 는 시나리오가 허용하는 것.
type Expect struct {
	PromotionsAllowed int   `json:"promotionsAllowed"` // 유발당 1(S1–S4) · 0(그 밖)
	GenerationDelta   int64 `json:"generationDelta"`   // S0 = 1, 그 밖 0
	Budget            int   `json:"budget"`            // 창 안 재구축 상한 = spec.shards.replicas(최소 1)
	BudgetExhaust     bool  `json:"budgetExhaust"`     // S7 — MemberRebuildBlocked=True(BudgetExhausted)
	GaugeRequired     bool  `json:"gaugeRequired"`     // GREEN — status 쓰기 게이지 전진
	Members           int   `json:"members"`           // S0 — 새 revision 으로 Ready 인 파드 수
}

// Check 는 판정 하나의 결과.
type Check struct {
	ID     CheckID `json:"id"`
	Pass   bool    `json:"pass"`
	Detail string  `json:"detail"`
}

// Verdict 는 시나리오 하나의 판정.
type Verdict struct {
	Scenario Scenario `json:"scenario"`
	Checks   []Check  `json:"checks"`
}

// ExpectFor 는 시나리오·모드의 기대를 낸다.
func ExpectFor(scn Scenario, mode Mode, replicas int) Expect {
	exp := Expect{
		Budget:        max(replicas, 1),
		Members:       replicas + 1,
		GaugeRequired: mode == ModeGreen,
	}
	switch scn {
	case ScenarioS1, ScenarioS2, ScenarioS3, ScenarioS4:
		exp.PromotionsAllowed = 1
	case ScenarioS0:
		exp.GenerationDelta = 1
	case ScenarioS7:
		exp.BudgetExhaust = true
	}
	return exp
}

// Judge 는 J1–J9(S0 는 전환 판정 추가)를 낸다.
func Judge(scn Scenario, before, after ClusterSnap, exp Expect) Verdict {
	ids := checkIDs(scn)
	verdict := Verdict{Scenario: scn, Checks: make([]Check, 0, len(ids))}

	// 수집 실패 — 전부 붉다
	if before.empty() || after.empty() {
		for _, id := range ids {
			verdict.Checks = append(verdict.Checks, Check{ID: id, Detail: "빈 스냅숏"})
		}
		return verdict
	}

	verdict.Checks = append(verdict.Checks,
		judgeTemplate(before, after, exp),
		judgePVC(before, after),
		judgePromotions(before, after, exp),
		judgeStall(before, after, exp),
		judgeLeak(before, after),
		judgeSource(after),
		judgeBudget(after, exp),
		judgeFailover(before, after),
		judgeSurvivors(before, after),
	)
	if scn == ScenarioS0 {
		verdict.Checks = append(verdict.Checks, judgeTransition(before, after, exp))
	}
	return verdict
}

// Green 은 판정이 하나 이상이고 전부 초록인가.
func (v Verdict) Green() bool {
	if len(v.Checks) == 0 {
		return false
	}
	for _, c := range v.Checks {
		if !c.Pass {
			return false
		}
	}
	return true
}

// AnyRed 는 주어진 판정 중 하나라도 붉은가 — 없는 판정은 붉음으로 세지 않는다.
func (v Verdict) AnyRed(ids ...CheckID) bool {
	for _, c := range v.Checks {
		if !c.Pass && slices.Contains(ids, c.ID) {
			return true
		}
	}
	return false
}

// RedReproduced 는 양성 대조 — S4 에서 J1∨J2∨J3∨J5 붉음 ∧ S5 에서 J4 붉음.
func RedReproduced(s4, s5 Verdict) bool {
	if s4.Scenario != ScenarioS4 || s5.Scenario != ScenarioS5 {
		return false
	}
	return s4.AnyRed(CheckTemplate, CheckPVC, CheckPromotions, CheckLeak) && s5.AnyRed(CheckStall)
}

func checkIDs(scn Scenario) []CheckID {
	ids := []CheckID{
		CheckTemplate, CheckPVC, CheckPromotions, CheckStall, CheckLeak,
		CheckSource, CheckBudget, CheckFailover, CheckSurvivors,
	}
	if scn == ScenarioS0 {
		ids = append(ids, CheckTransition)
	}
	return ids
}

func (s ClusterSnap) empty() bool {
	return s.TakenAt.IsZero() || s.StsGeneration == 0 || len(s.PVCUIDs) == 0
}

// lastTimeline 은 마지막으로 본 타임라인(0 = 못 봄).
func (s ClusterSnap) lastTimeline() int64 {
	if len(s.Timeline) == 0 {
		return 0
	}
	return s.Timeline[len(s.Timeline)-1].Timeline
}

// maxTimeline 은 본 타임라인의 최댓값(0 = 못 봄).
func (s ClusterSnap) maxTimeline() int64 {
	var top int64
	for _, m := range s.Timeline {
		top = max(top, m.Timeline)
	}
	return top
}

// promotedAt 은 base 보다 높은 타임라인을 처음 본 시각.
func (s ClusterSnap) promotedAt(base int64) (time.Time, bool) {
	for _, m := range s.Timeline {
		if m.Timeline > base {
			return m.At, true
		}
	}
	return time.Time{}, false
}

// timelineAt 은 t 이전 마지막 표본의 타임라인(없으면 t 이후 첫 표본).
func (s ClusterSnap) timelineAt(t time.Time) int64 {
	var at int64
	for _, m := range s.Timeline {
		if m.At.After(t) {
			if at == 0 {
				return m.Timeline
			}
			return at
		}
		at = m.Timeline
	}
	return at
}

// observeStart 는 관측창 시작 — 없으면 첫 유발, 그것도 없으면 before 시각.
func observeStart(before, after ClusterSnap) time.Time {
	if !after.ObserveFrom.IsZero() {
		return after.ObserveFrom
	}
	if len(after.Faults) > 0 {
		return after.Faults[0]
	}
	return before.TakenAt
}

// J1 — STS generation 차이가 기대와 같고, 기대가 0 이면 revision 도 불변.
func judgeTemplate(before, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckTemplate}
	delta := after.StsGeneration - before.StsGeneration
	if delta != exp.GenerationDelta {
		check.Detail = fmt.Sprintf("STS generation %d→%d(기대 +%d)", before.StsGeneration, after.StsGeneration, exp.GenerationDelta)
		return check
	}
	if exp.GenerationDelta != 0 {
		check.Pass = true
		check.Detail = fmt.Sprintf("STS generation +%d", delta)
		return check
	}

	// 템플릿이 그대로면 revision 도 그대로다
	if after.UpdateRevision != before.UpdateRevision {
		check.Detail = fmt.Sprintf("updateRevision %s→%s", before.UpdateRevision, after.UpdateRevision)
		return check
	}
	if !sameSet(before.Revisions, after.Revisions) {
		check.Detail = fmt.Sprintf("ControllerRevision %v→%v", before.Revisions, after.Revisions)
		return check
	}
	check.Pass = true
	check.Detail = "generation·updateRevision·ControllerRevision 불변"
	return check
}

// J2 — 데이터 PVC UID 집합 불변 ∧ 오퍼레이터 SA 의 PVC delete(허용·거부) 0.
func judgePVC(before, after ClusterSnap) Check {
	check := Check{ID: CheckPVC}
	if !maps.Equal(before.PVCUIDs, after.PVCUIDs) {
		check.Detail = fmt.Sprintf("PVC UID %v→%v", before.PVCUIDs, after.PVCUIDs)
		return check
	}
	if after.AuditPVCDeletes != 0 {
		check.Detail = fmt.Sprintf("감사 PVC delete %d건", after.AuditPVCDeletes)
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("PVC %d개 UID 불변 · 감사 delete 0", len(after.PVCUIDs))
	return check
}

// J3 — 타임라인 증가 ≤ 허용 ∧ 관측창 안 추가 승격 0.
func judgePromotions(before, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckPromotions}
	base, top := before.lastTimeline(), after.maxTimeline()
	if base == 0 || top == 0 {
		check.Detail = "타임라인 미관측"
		return check
	}
	rise := top - base
	if rise > int64(exp.PromotionsAllowed) {
		check.Detail = fmt.Sprintf("타임라인 %d→%d(허용 +%d)", base, top, exp.PromotionsAllowed)
		return check
	}

	// 수렴 뒤 관측창에서 한 번이라도 오르면 루프다
	if !after.ObserveFrom.IsZero() {
		start := after.timelineAt(after.ObserveFrom)
		if top > start && start != 0 {
			check.Detail = fmt.Sprintf("관측창 추가 승격 %d→%d", start, top)
			return check
		}
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("타임라인 %d→%d(허용 +%d)", base, top, exp.PromotionsAllowed)
	return check
}

// J4 — 관측창 안 status 쓰기 전진 ≥ 하한 ∧ 'Failed to watch' 0 ∧ (GREEN) 게이지 전진.
func judgeStall(before, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckStall}
	if after.FailedWatchLines != 0 {
		check.Detail = fmt.Sprintf("'Failed to watch' %d줄", after.FailedWatchLines)
		return check
	}
	start := observeStart(before, after)
	advances := countAfter(after.StatusWrites, start, after.TakenAt)
	if advances < StatusAdvanceFloor {
		check.Detail = fmt.Sprintf("관측창 status 쓰기 전진 %d(하한 %d)", advances, StatusAdvanceFloor)
		return check
	}
	if exp.GaugeRequired && rises(after.GaugeWrites) < StatusAdvanceFloor {
		check.Detail = fmt.Sprintf("status 쓰기 게이지 전진 %d(하한 %d)", rises(after.GaugeWrites), StatusAdvanceFloor)
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("관측창 status 쓰기 전진 %d", advances)
	return check
}

// J5 — Released PV 증가 0.
func judgeLeak(before, after ClusterSnap) Check {
	check := Check{ID: CheckLeak}
	grown := after.ReleasedPVs - before.ReleasedPVs
	if grown > 0 {
		check.Detail = fmt.Sprintf("Released PV %d→%d", before.ReleasedPVs, after.ReleasedPVs)
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("Released PV %d 불변", after.ReleasedPVs)
	return check
}

// J6 — 재구축마다 참조 스냅숏의 readyToUse 시각 < 풀기 시각.
func judgeSource(after ClusterSnap) Check {
	check := Check{ID: CheckSource}
	ready := make(map[string]time.Time, len(after.Snapshots))
	for _, s := range after.Snapshots {
		ready[s.Name] = s.ReadyAt
	}
	for _, r := range after.Rebuilds {
		at, ok := ready[r.Snapshot]
		if !ok || at.IsZero() || !at.Before(r.ReleasedAt) {
			check.Detail = fmt.Sprintf("%s 풀기(%s) 전에 readyToUse 스냅숏 없음(%q)", r.Member, r.ReleasedAt.Format(time.RFC3339), r.Snapshot)
			return check
		}
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("재구축 %d건 전부 스냅숏 선행", len(after.Rebuilds))
	return check
}

// J7 — 예산 창 안 재구축 ≤ 예산 ∧ (S7) BudgetExhausted 관측.
func judgeBudget(after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckBudget}
	windowStart := after.TakenAt.Add(-RebuildBudgetWindow)
	used := 0
	for _, r := range after.Rebuilds {
		if r.ReleasedAt.After(windowStart) {
			used++
		}
	}
	if used > exp.Budget {
		check.Detail = fmt.Sprintf("창 안 재구축 %d(예산 %d)", used, exp.Budget)
		return check
	}
	if exp.BudgetExhaust && !after.BudgetBlocked {
		check.Detail = "MemberRebuildBlocked=True(BudgetExhausted) 미관측"
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("창 안 재구축 %d(예산 %d)", used, exp.Budget)
	return check
}

// J8 — 승격이 났다면 유발부터 쓰기 가능 ≤ WritableBound ∧ 유발 전 확인한 행 유지.
func judgeFailover(before, after ClusterSnap) Check {
	check := Check{ID: CheckFailover}
	if !after.Writes.MarkerChecked {
		check.Detail = "표식 행 미확인"
		return check
	}
	if !after.Writes.MarkerKept {
		check.Detail = "유발 전 확인한 표식 행 소실"
		return check
	}
	base := before.lastTimeline()
	if base == 0 {
		check.Detail = "타임라인 미관측"
		return check
	}
	if _, promoted := after.promotedAt(base); !promoted {
		check.Pass = true
		check.Detail = "승격 없음 · 표식 행 유지"
		return check
	}
	if len(after.Faults) == 0 || after.Writes.WritableAt.IsZero() {
		check.Detail = "승격 뒤 쓰기 가능 시각 미관측"
		return check
	}
	took := after.Writes.WritableAt.Sub(after.Faults[0])
	if took > WritableBound {
		check.Detail = fmt.Sprintf("쓰기 가능까지 %s(상한 %s)", took.Round(time.Second), WritableBound)
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("쓰기 가능까지 %s · 표식 행 유지", took.Round(time.Second))
	return check
}

// J9 — 승격 뒤 생존 standby 가 RejoinBound 안에 새 primary 그래프에 붙고 재구축 0.
func judgeSurvivors(before, after ClusterSnap) Check {
	check := Check{ID: CheckSurvivors}
	base := before.lastTimeline()
	if base == 0 {
		check.Detail = "타임라인 미관측"
		return check
	}
	promoted, ok := after.promotedAt(base)
	if !ok {
		check.Pass = true
		check.Detail = "승격 없음"
		return check
	}
	if len(before.Graph) == 0 {
		check.Detail = "유발 전 복제 그래프 미관측"
		return check
	}

	// 새 primary = 승격 뒤 처음 보인 다른 primary
	oldPrimary := before.Graph[len(before.Graph)-1].Primary
	newPrimary := ""
	for _, g := range after.Graph {
		if g.At.Before(promoted) || g.Primary == oldPrimary || g.Primary == "" {
			continue
		}
		newPrimary = g.Primary
		break
	}
	if newPrimary == "" {
		check.Detail = "승격 뒤 새 primary 그래프 미관측"
		return check
	}

	for _, member := range before.Graph[len(before.Graph)-1].Connected {
		if member == newPrimary {
			continue
		}
		joined, found := joinedAt(after.Graph, newPrimary, member, promoted)
		if !found || joined.Sub(promoted) > RejoinBound {
			check.Detail = fmt.Sprintf("%s 가 %s 안에 %s 에 붙지 않음", member, RejoinBound, newPrimary)
			return check
		}
		if rebuiltSince(after.Rebuilds, member, promoted) {
			check.Detail = fmt.Sprintf("생존 standby %s 재구축", member)
			return check
		}
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("생존 standby 전부 %s 에 재접속", newPrimary)
	return check
}

// S0 — 롤아웃 동안 타임라인 불변 ∧ 전 파드 새 revision Ready ∧ 옛 오퍼레이터의 pod 삭제 0.
func judgeTransition(before, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckTransition}
	if base := before.lastTimeline(); base == 0 || after.maxTimeline() != base {
		check.Detail = fmt.Sprintf("롤아웃 중 타임라인 %d→%d", base, after.maxTimeline())
		return check
	}
	if after.PodsOnUpdate != exp.Members {
		check.Detail = fmt.Sprintf("새 revision Ready 파드 %d(기대 %d)", after.PodsOnUpdate, exp.Members)
		return check
	}
	if after.AuditPodDeletes != 0 {
		check.Detail = fmt.Sprintf("옛 오퍼레이터의 pod 삭제 %d건", after.AuditPodDeletes)
		return check
	}
	check.Pass = true
	check.Detail = "타임라인 불변 · 전 파드 새 revision · 옛 오퍼레이터 삭제 0"
	return check
}

func joinedAt(graph []GraphMark, primary, member string, since time.Time) (time.Time, bool) {
	for _, g := range graph {
		if g.At.Before(since) || g.Primary != primary {
			continue
		}
		if slices.Contains(g.Connected, member) {
			return g.At, true
		}
	}
	return time.Time{}, false
}

func rebuiltSince(rebuilds []RebuildMark, member string, since time.Time) bool {
	for _, r := range rebuilds {
		if r.Member == member && !r.ReleasedAt.Before(since) {
			return true
		}
	}
	return false
}

// countAfter 는 (start, end] 안의 서로 다른 시각 수.
func countAfter(times []time.Time, start, end time.Time) int {
	seen := map[time.Time]bool{}
	for _, t := range times {
		if t.After(start) && !t.After(end) {
			seen[t.UTC()] = true
		}
	}
	return len(seen)
}

// rises 는 표본 열에서 값이 오른 횟수.
func rises(values []float64) int {
	count := 0
	for i := 1; i < len(values); i++ {
		if values[i] > values[i-1] {
			count++
		}
	}
	return count
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}
