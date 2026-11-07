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

// 판정 J1–J19(설계 §3.12)는 시나리오 전후 스냅숏 두 장만 보는 순수 함수다.
// 드라이버가 before 를 유발 직전에, after 를 창 끝에 찍는다 — after 는 창 동안 쌓은 표본을 싣는다.
//
//	before ── 유발(Faults) ── 풀림(ReleasedAt) ── 수렴(ConvergedAt) ── 관측창(ObserveFrom~) ── after
//
// 빈 스냅숏(수집 실패)은 모든 판정이 붉다 — 못 본 것을 초록으로 읽지 않는다.
// 어느 판정이 서는지는 시나리오만으로 정한다(checkIDs) — 표본이 비었다고 판정이 빠지지 않는다.

// Scenario 는 훈련장 시나리오 식별자.
type Scenario string

const (
	ScenarioS0  Scenario = "S0"  // 전환 — 사전(beta.11 · ST0 부모 트리 · 운영 누적물) → ST0 → C1 → C2 → 단계화
	ScenarioS1  Scenario = "S1"  // primary 파드 삭제
	ScenarioS2  Scenario = "S2"  // primary 파드 강제 삭제
	ScenarioS3  Scenario = "S3"  // primary 인스턴스 매니저 SIGSTOP
	ScenarioS4  Scenario = "S4"  // control-plane docker pause
	ScenarioS5  Scenario = "S5"  // standby 워커 docker stop(nodes 권한은 차트 그대로)
	ScenarioS5a Scenario = "S5a" // nodes 규칙을 뗀 뒤 S5 — 정지 0 · NodeReadDenied · api_read_denied_total
	ScenarioS6  Scenario = "S6"  // standby postmaster SIGSTOP
	ScenarioS6b Scenario = "S6b" // standby 넷ns 에서 primary IP 차단 → DetachLead 뒤 primary 트리 SIGSTOP
	ScenarioS7  Scenario = "S7"  // S6 을 예산+1회
	ScenarioS8  Scenario = "S8"  // 템플릿 변경 뒤 첫 새 replica postmaster SIGSTOP — 롤아웃 막힘·회복
	ScenarioS9  Scenario = "S9"  // primary 워커 docker stop(> NodeDeathBound)
	ScenarioS10 Scenario = "S10" // switchover Stopping 에서 오퍼레이터 SIGSTOP → pg-stop hold → recover-primary
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
	CheckTemplate    CheckID = "J1"  // STS 템플릿 불변(S0·S8 +1)
	CheckPVC         CheckID = "J2"  // 데이터 PVC 불변 · PVC 삭제 0
	CheckPromotions  CheckID = "J3"  // 승격 수
	CheckStall       CheckID = "J4"  // 컨트롤러 정지 0
	CheckLeak        CheckID = "J5"  // Released PV 증가 0
	CheckSource      CheckID = "J6"  // 풀기 전 스냅숏 readyToUse
	CheckBudget      CheckID = "J7"  // 재구축 예산
	CheckFailover    CheckID = "J8"  // PodReady False 부터 승격 시각 · 확인한 행 유지
	CheckSurvivors   CheckID = "J9"  // 생존 standby 재접속 · 재구축 0
	CheckWriteGap    CheckID = "J10" // 쓰기 공백 · 표본 하한
	CheckPlacement   CheckID = "J11" // 서로 다른 적격 노드 · W4 0
	CheckOrder       CheckID = "J12" // 단계화 순서
	CheckOnDelete    CheckID = "J13" // OnDelete · statefulset-controller 삭제 0
	CheckConverge    CheckID = "J14" // 수렴 회복
	CheckLossless    CheckID = "J15" // ack 된 탐침 행이 최종 primary 에
	CheckDataBearing CheckID = "J16" // data-bearing 스냅숏 보존 · VAP 거부
	CheckHBA         CheckID = "J17" // 적재 기준 network trust 0 · 비멤버 거부
	CheckInPlace     CheckID = "J18" // 제자리 switchover
	CheckEmergency   CheckID = "J19" // 비상 정지
	CheckTransition  CheckID = "S0"  // 전환 롤아웃 · 운영 누적물 정리
)

// checkOrder 는 판정문에 싣는 판정 순서다.
var checkOrder = []CheckID{
	CheckTemplate, CheckPVC, CheckPromotions, CheckStall, CheckLeak, CheckSource, CheckBudget, CheckFailover,
	CheckSurvivors, CheckWriteGap, CheckPlacement, CheckOrder, CheckOnDelete, CheckConverge, CheckLossless,
	CheckDataBearing, CheckHBA, CheckInPlace, CheckEmergency, CheckTransition,
}

// Promotion 은 시나리오가 부를 수 있는 승격이다 — 승격 수·유실·쓰기 공백의 기대가 여기서 갈린다.
type Promotion string

const (
	PromotionNone       Promotion = "none"       // 승격 0
	PromotionFailover   Promotion = "failover"   // 자동 failover ≤ 1 — J8 시각식
	PromotionSwitchover Promotion = "switchover" // 계획 switchover 정확히 1 — J12·J18
	PromotionEmergency  Promotion = "emergency"  // pg-stop recover-primary ≤ 1 — J19
)

// StatusAdvanceFloor 는 J4 — 관측창 안 CR status 쓰기 전진 하한(설계 "전진 ≥2").
const StatusAdvanceFloor = 2

// timelineUnseen 은 타임라인 표본이 없는 판정 글이다 — 못 본 것은 붉다.
const timelineUnseen = "타임라인 미관측"

// 오퍼레이터가 내는 값 — 이벤트 사유 · 스냅숏 라벨 · 재구축 사유(member_rebuild.go · stale_replica_reseed.go).
const (
	EventNodeReadDenied  = "NodeReadDenied"
	LabelTrue            = "true"
	LabelFalse           = "false"
	RebuildDemoted       = "Demoted"
	RebuildFencedPrimary = "FencedPrimary"
	RebuildRogue         = "Rogue"
	RebuildStale         = "Stale"
)

// TimelineMark 는 관측한 primary 타임라인 한 점 — 가장 높은 타임라인과 그것을 보고한 파드.
type TimelineMark struct {
	At       time.Time `json:"at"`
	Timeline int64     `json:"timeline"`
	Primary  string    `json:"primary,omitempty"`
}

// SnapshotMark 는 창에서 본 멤버 스냅숏 — readyToUse 를 처음 본 시각과 재구축 라벨.
type SnapshotMark struct {
	Name        string    `json:"name"`
	Member      string    `json:"member"`
	ReadyAt     time.Time `json:"readyAt,omitzero"`
	Reason      string    `json:"reason,omitempty"`      // rebuild-reason 라벨
	DataBearing string    `json:"dataBearing,omitempty"` // data-bearing 라벨 값
	Gone        bool      `json:"gone,omitempty"`        // 창에서 봤는데 끝에 없다
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

// GraphMark 는 primary 에서 닿는 복제 그래프 한 표본 — 연쇄 standby 포함(설계 §3.4).
type GraphMark struct {
	At        time.Time `json:"at"`
	Primary   string    `json:"primary"`
	Connected []string  `json:"connected"`           // streaming·catchup·backup 으로 닿는 멤버
	Streaming []string  `json:"streaming,omitempty"` // streaming 만으로 닿는 멤버
}

// WriteMark 는 J8 의 표식 행.
type WriteMark struct {
	MarkerChecked bool `json:"markerChecked"` // 창 끝에 표식 행을 확인할 수 있었나
	MarkerKept    bool `json:"markerKept"`    // 유발 전 모든 멤버에서 확인한 행이 남았나
}

// ClusterSnap 은 한 시점의 클러스터 판정 재료다.
type ClusterSnap struct {
	TakenAt          time.Time         `json:"takenAt"`
	StsGeneration    int64             `json:"stsGeneration"`
	UpdateRevision   string            `json:"updateRevision"`
	Revisions        []string          `json:"revisions"`
	UpdateStrategy   string            `json:"updateStrategy,omitempty"`
	PVCUIDs          map[string]string `json:"pvcUIDs"`
	ReleasedPVs      int               `json:"releasedPVs"`
	Timeline         []TimelineMark    `json:"timeline"`
	MaxPrimaries     int               `json:"maxPrimaries"`
	StatusWrites     []time.Time       `json:"statusWrites"`
	GaugeWrites      []float64         `json:"gaugeWrites,omitempty"`
	DeniedReads      []float64         `json:"deniedReads,omitempty"`
	FailedWatchLines int               `json:"failedWatchLines"`
	AuditPVCDeletes  int               `json:"auditPVCDeletes"`
	AuditPodDeletes  int               `json:"auditPodDeletes"`
	AuditStsDeletes  int               `json:"auditStsDeletes"`
	Snapshots        []SnapshotMark    `json:"snapshots,omitempty"`
	Rebuilds         []RebuildMark     `json:"rebuilds,omitempty"`
	HeldReasons      map[string]int    `json:"heldReasons,omitempty"`
	Events           []EventMark       `json:"events,omitempty"`
	Graph            []GraphMark       `json:"graph,omitempty"`
	Writes           WriteMark         `json:"writes"`
	Faults           []time.Time       `json:"faults,omitempty"`
	UnreadyAt        time.Time         `json:"unreadyAt,omitzero"`
	ReleasedAt       time.Time         `json:"releasedAt,omitzero"`
	ConvergedAt      time.Time         `json:"convergedAt,omitzero"`
	ObserveFrom      time.Time         `json:"observeFrom,omitempty"`
	BudgetBlocked    bool              `json:"budgetBlocked"`
	PodsOnUpdate     int               `json:"podsOnUpdate"`
	Probe            ProbeRecord       `json:"probe"`
	HostStalls       []StallSpan       `json:"hostStalls,omitempty"` // 드라이버 정지 시계(ADR-0039 계측기 B) — after 만
	Placement        PlacementRecord   `json:"placement"`
	Rollout          RolloutRecord     `json:"rollout"`
	VAP              VAPOutcome        `json:"vap,omitempty"`
	HBA              HBARecord         `json:"hba"`
	Switchover       SwitchoverRecord  `json:"switchover"`
	Emergency        EmergencyRecord   `json:"emergency"`
	Settings         []MemberSettings  `json:"settings,omitempty"`
	C1Faults         []string          `json:"c1Faults,omitempty"`
}

// Expect 는 시나리오가 허용하는 것.
type Expect struct {
	Promotion          Promotion     `json:"promotion"`
	PromotionsAllowed  int           `json:"promotionsAllowed"`  // J3 상한
	PromotionsRequired int           `json:"promotionsRequired"` // J3 하한 — 계획 switchover 는 정확히 1
	GenerationDelta    int64         `json:"generationDelta"`    // S0·S8 = 1, 그 밖 0
	Budget             int           `json:"budget"`             // 창 안 재구축 상한 = spec.shards.replicas(최소 1)
	BudgetExhaust      bool          `json:"budgetExhaust"`      // S7 — MemberRebuildBlocked=True(BudgetExhausted)
	GaugeRequired      bool          `json:"gaugeRequired"`      // GREEN — status 쓰기 게이지 전진
	DeniedReads        bool          `json:"deniedReads"`        // S5a — NodeReadDenied · api_read_denied_total 전진
	Members            int           `json:"members"`            // 1 + replicas
	GapBound           time.Duration `json:"gapBound"`           // J10 가장 긴 공백 상한
	LongGaps           int           `json:"longGaps"`           // J10 ReplicaGapBudget 를 넘어도 되는 공백 수
	LossAllowed        int           `json:"lossAllowed"`        // J15 유실 허용 ack 수
	ConvergeWithin     time.Duration `json:"convergeWithin"`     // J14 풀림 뒤 수렴 상한
	RolloutBlock       bool          `json:"rolloutBlock"`       // S8 — RolloutBlocked 발생·해제
	TrustControl       bool          `json:"trustControl"`       // S0 — J17 사전 trust 접속 양성 대조
	DetachedData       bool          `json:"detachedData"`       // S6b — J16 Demoted = true · Stale = false · VAP 거부
}

// Check 는 판정 하나의 결과.
type Check struct {
	ID     CheckID `json:"id"`
	Pass   bool    `json:"pass"`
	Detail string  `json:"detail"`
}

// ObsID 는 관측 식별자 — 관측은 판정이 아니다(Green 이 보지 않는다).
type ObsID string

// ObsHostStall 은 호스트 정지 관측(ADR-0039 H1).
const ObsHostStall ObsID = "H1"

// Observation 은 관측 하나 — H1 은 창 안 정지 수 · J10 보정에 쓴 수 · 보정 합 · 가장 긴 정지.
type Observation struct {
	ID       ObsID         `json:"id"`
	Count    int           `json:"count"`
	Credited int           `json:"credited"`
	Credit   time.Duration `json:"credit"`
	Longest  time.Duration `json:"longest"`
	Detail   string        `json:"detail"`
}

// Verdict 는 시나리오 하나의 판정.
type Verdict struct {
	Scenario     Scenario      `json:"scenario"`
	Checks       []Check       `json:"checks"`
	Observations []Observation `json:"observations,omitempty"`
}

// ExpectFor 는 시나리오·모드의 기대를 낸다.
func ExpectFor(scn Scenario, mode Mode, replicas int) Expect {
	members := replicas + 1
	exp := Expect{
		Promotion:      promotionOf(scn),
		Budget:         max(replicas, 1),
		Members:        members,
		GaugeRequired:  mode == ModeGreen,
		GapBound:       ReplicaGapBudget,
		ConvergeWithin: ConvergeBound,
	}

	// 승격 종류 — 승격 수 · 유실 · 긴 공백 하나
	switch exp.Promotion {
	case PromotionFailover:
		exp.PromotionsAllowed, exp.LossAllowed, exp.LongGaps, exp.GapBound = 1, 1, 1, FailoverGapBound
	case PromotionSwitchover:
		exp.PromotionsAllowed, exp.PromotionsRequired, exp.LongGaps, exp.GapBound = 1, 1, 1, WriteGapBudget
		exp.GenerationDelta = 1
		exp.ConvergeWithin = ConvergeBound + RolloutBound(members)
	case PromotionEmergency:
		exp.PromotionsAllowed, exp.LongGaps, exp.GapBound = 1, 1, EmergencyGapBound
	}

	// 시나리오 고유 — 노드 사망 감지 · 예산 소진 · 양성 대조
	switch scn {
	case ScenarioS0:
		exp.TrustControl = true
	case ScenarioS5a:
		exp.DeniedReads = true
	case ScenarioS6b:
		exp.DetachedData = true
		exp.PromotionsRequired = 1 // 승격 0 은 유발이 무효다(ADR-0036 §B)
	case ScenarioS7:
		exp.BudgetExhaust = true
		exp.ConvergeWithin = ExhaustConvergeBound
	case ScenarioS8:
		exp.RolloutBlock = true
	case ScenarioS9:
		exp.GapBound = NodeDeathGapBound
	}
	return exp
}

// promotionOf 는 시나리오의 승격 종류다(설계 §3.12 J3 — S1–S3·S9 ≤ 1 · S0·S6b·S8 = 1 · 그 밖 0).
func promotionOf(scn Scenario) Promotion {
	switch scn {
	case ScenarioS1, ScenarioS2, ScenarioS3, ScenarioS6b, ScenarioS9:
		return PromotionFailover
	case ScenarioS0, ScenarioS8:
		return PromotionSwitchover
	case ScenarioS10:
		return PromotionEmergency
	}
	return PromotionNone
}

// judgeFunc 는 판정 하나 — 모든 판정이 같은 입력을 받는다.
type judgeFunc func(before, after ClusterSnap, exp Expect) Check

var judges = map[CheckID]judgeFunc{
	CheckTemplate:    judgeTemplate,
	CheckPVC:         judgePVC,
	CheckPromotions:  judgePromotions,
	CheckStall:       judgeStall,
	CheckLeak:        judgeLeak,
	CheckSource:      judgeSource,
	CheckBudget:      judgeBudget,
	CheckFailover:    judgeFailover,
	CheckSurvivors:   judgeSurvivors,
	CheckWriteGap:    judgeWriteGap,
	CheckPlacement:   judgePlacement,
	CheckOrder:       judgeOrder,
	CheckOnDelete:    judgeOnDelete,
	CheckConverge:    judgeConverge,
	CheckLossless:    judgeLossless,
	CheckDataBearing: judgeDataBearing,
	CheckHBA:         judgeHBA,
	CheckInPlace:     judgeInPlace,
	CheckEmergency:   judgeEmergency,
	CheckTransition:  judgeTransition,
}

// Judge 는 시나리오에 서는 판정을 낸다(checkIDs).
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

	for _, id := range ids {
		verdict.Checks = append(verdict.Checks, judges[id](before, after, exp))
	}
	if obs, ok := observeHostStall(before, after); ok {
		verdict.Observations = append(verdict.Observations, obs)
	}
	return verdict
}

// Green 은 판정이 하나 이상이고 전부 초록인가 — 관측(Observations)은 보지 않는다.
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

// RedRounds 는 RED 양성 대조가 시나리오마다 요구하는 판 수(설계 부록 A 개정 — beta.11 로 각 2판).
const RedRounds = 2

// redTargets 는 RED 양성 대조의 시나리오와 붉어야 할 판정 — S3 루프(J1∨J2∨J3∨J5) · S5 정지(J4).
// S4 는 kind 에서 기점도 초록이고(DR1 판정문) S1 은 판마다 갈려 뺐다.
var redTargets = map[Scenario][]CheckID{
	ScenarioS3: {CheckTemplate, CheckPVC, CheckPromotions, CheckLeak},
	ScenarioS5: {CheckStall},
}

// RedScenarios 는 RED 판에 도는 시나리오 — S3·S5 를 RedRounds 번 번갈아.
func RedScenarios() []Scenario {
	plan := make([]Scenario, 0, len(redTargets)*RedRounds)
	for range RedRounds {
		plan = append(plan, ScenarioS3, ScenarioS5)
	}
	return plan
}

// RedTargets 는 RED 에서 scn 이 붉혀야 할 판정이다(양성 대조 밖 시나리오면 nil).
func RedTargets(scn Scenario) []CheckID {
	return slices.Clone(redTargets[scn])
}

// RedReproduced 는 양성 대조 — S3·S5 를 각각 RedRounds 판 이상 돌았고 모든 판이 제 판정을 붉혔다.
// 한 판이라도 초록이면 거짓이다(설계: 기준을 다시 본다 — 추정 금지).
func RedReproduced(runs []Verdict) bool {
	for scn, targets := range redTargets {
		rounds := 0
		for _, v := range runs {
			if v.Scenario != scn {
				continue
			}
			if !v.AnyRed(targets...) {
				return false
			}
			rounds++
		}
		if rounds < RedRounds {
			return false
		}
	}
	return true
}

func checkIDs(scn Scenario) []CheckID {
	var ids []CheckID
	for _, id := range checkOrder {
		if applies(id, scn) {
			ids = append(ids, id)
		}
	}
	return ids
}

// applies 는 판정이 시나리오에 서는가 — 단계화·제자리는 계획 switchover, 비상 정지는 S10, 전환은 S0 만.
func applies(id CheckID, scn Scenario) bool {
	switch id {
	case CheckOrder, CheckInPlace:
		return promotionOf(scn) == PromotionSwitchover
	case CheckEmergency:
		return promotionOf(scn) == PromotionEmergency
	case CheckTransition:
		return scn == ScenarioS0
	}
	return true
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

// promotedAt 은 base 보다 높은 타임라인을 처음 본 표본 — 시각과 승격한 파드.
func (s ClusterSnap) promotedAt(base int64) (TimelineMark, bool) {
	for _, m := range s.Timeline {
		if m.Timeline > base {
			return m, true
		}
	}
	return TimelineMark{}, false
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
func judgePVC(before, after ClusterSnap, _ Expect) Check {
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

// J3 — 하한 ≤ 타임라인 증가 ≤ 상한 ∧ 관측창 안 추가 승격 0.
func judgePromotions(before, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckPromotions}
	base, top := before.lastTimeline(), after.maxTimeline()
	if base == 0 || top == 0 {
		check.Detail = timelineUnseen
		return check
	}
	rise := top - base
	if rise > int64(exp.PromotionsAllowed) || rise < int64(exp.PromotionsRequired) {
		check.Detail = fmt.Sprintf("타임라인 %d→%d(기대 +%d~+%d)", base, top, exp.PromotionsRequired, exp.PromotionsAllowed)
		return check
	}

	// 수렴 뒤 관측창에서 오르면 루프다 — 창 안에 든 계획 switchover 의 승격은 뺀다
	if !after.ObserveFrom.IsZero() {
		start := after.timelineAt(after.ObserveFrom)
		if top-start > plannedInWindow(after, exp, start) && start != 0 {
			check.Detail = fmt.Sprintf("관측창 추가 승격 %d→%d", start, top)
			return check
		}
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("타임라인 %d→%d(기대 +%d~+%d)", base, top, exp.PromotionsRequired, exp.PromotionsAllowed)
	return check
}

// plannedInWindow 는 관측창 안 오름 가운데 계획 switchover 몫이다 — switchover 가 창 안에서 시작·완료했고
// 창 안 첫 오름이 그 시작 뒤일 때만 PromotionsRequired, 아니면 0.
func plannedInWindow(after ClusterSnap, exp Expect, start int64) int64 {
	r := after.Rollout
	if exp.Promotion != PromotionSwitchover || r.SwitchStart.IsZero() || r.SwitchDone.IsZero() {
		return 0
	}
	if r.SwitchStart.Before(after.ObserveFrom) || r.SwitchDone.After(after.TakenAt) {
		return 0
	}
	rise, ok := after.promotedAt(start)
	if !ok || rise.At.Before(r.SwitchStart) {
		return 0
	}
	return int64(exp.PromotionsRequired)
}

// J4 — 관측창 안 status 쓰기 전진 ≥ 하한 ∧ 'Failed to watch' 0 ∧ (GREEN) 게이지 전진 ∧
// (S5a) NodeReadDenied 이벤트와 api_read_denied_total 전진 — nodes 권한이 없어도 멈추지 않고 드러낸다.
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
	if exp.DeniedReads && !hasEvent(after.Events, EventNodeReadDenied) {
		check.Detail = EventNodeReadDenied + " 이벤트 미관측(양성 대조)"
		return check
	}
	if exp.DeniedReads && rises(after.DeniedReads) == 0 {
		check.Detail = "api_read_denied_total 전진 0(양성 대조)"
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("관측창 status 쓰기 전진 %d", advances)
	return check
}

// J5 — Released PV 증가 0.
func judgeLeak(before, after ClusterSnap, _ Expect) Check {
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
func judgeSource(_, after ClusterSnap, _ Expect) Check {
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
func judgeBudget(_, after ClusterSnap, exp Expect) Check {
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

// J8 — 유발 전 확인한 행 유지 ∧ failover 면 옛 primary 의 PodReady False 부터 승격까지 ≤ PromotionBound.
// PodReady False 전의 승격은 하트비트만 낡은 승격이라 붉다(설계 §3.3). 계획·비상 승격의 시각은 J18·J19 가 본다.
func judgeFailover(before, after ClusterSnap, exp Expect) Check {
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
		check.Detail = timelineUnseen
		return check
	}
	mark, ok := after.promotedAt(base)
	if !ok {
		check.Pass = true
		check.Detail = "승격 없음 · 표식 행 유지"
		return check
	}
	promoted := mark.At
	if exp.Promotion == PromotionSwitchover || exp.Promotion == PromotionEmergency {
		check.Pass = true
		check.Detail = fmt.Sprintf("%s 승격 — 시각은 J18·J19 · 표식 행 유지", exp.Promotion)
		return check
	}

	// PodReady False 기준 — 먼저 오지 않았으면 하트비트만으로 승격한 것이다
	if after.UnreadyAt.IsZero() || after.UnreadyAt.After(promoted) {
		check.Detail = fmt.Sprintf("옛 primary PodReady False(%s) 전에 승격(%s)", stamp(after.UnreadyAt), stamp(promoted))
		return check
	}
	took := promoted.Sub(after.UnreadyAt)
	if took > PromotionBound {
		check.Detail = fmt.Sprintf("PodReady False 부터 승격까지 %s(상한 %s)", took.Round(time.Second), PromotionBound)
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("PodReady False 부터 승격까지 %s · 표식 행 유지", took.Round(time.Second))
	return check
}

// J9 — 승격 뒤 생존 standby 가 RejoinBound 안에 승격한 파드의 그래프에 붙고(연쇄 포함) 재구축 0.
// 승격한 파드는 새 타임라인을 보고한 파드다 — S10 처럼 옛 primary 가 다시 승격해도 같다.
func judgeSurvivors(before, after ClusterSnap, _ Expect) Check {
	check := Check{ID: CheckSurvivors}
	base := before.lastTimeline()
	if base == 0 {
		check.Detail = timelineUnseen
		return check
	}
	mark, ok := after.promotedAt(base)
	if !ok {
		check.Pass = true
		check.Detail = "승격 없음"
		return check
	}
	if len(before.Graph) == 0 {
		check.Detail = "유발 전 복제 그래프 미관측"
		return check
	}
	if mark.Primary == "" {
		check.Detail = "승격한 파드 미관측"
		return check
	}

	for _, member := range before.Graph[len(before.Graph)-1].Connected {
		if member == mark.Primary {
			continue
		}
		joined, found := joinedAt(after.Graph, mark.Primary, member, mark.At)
		if !found || joined.Sub(mark.At) > RejoinBound {
			check.Detail = fmt.Sprintf("%s 가 %s 안에 %s 에 붙지 않음", member, RejoinBound, mark.Primary)
			return check
		}
		if rebuiltSince(after.Rebuilds, member, mark.At) {
			check.Detail = fmt.Sprintf("생존 standby %s 재구축", member)
			return check
		}
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("생존 standby 전부 %s 에 재접속", mark.Primary)
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

func hasEvent(events []EventMark, reason string) bool {
	return slices.ContainsFunc(events, func(e EventMark) bool { return e.Reason == reason })
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

// stamp 는 판정 글의 시각(없으면 "없음").
func stamp(t time.Time) string {
	if t.IsZero() {
		return "없음"
	}
	return t.UTC().Format(time.RFC3339)
}
