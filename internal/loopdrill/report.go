/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// 판정문 결과 — 훈련 드라이버와 다시 판정(Rejudge)이 같은 규칙으로 낸다.
const (
	ResultRedAsExpected  = "red-as-expected"
	ResultRedMissing     = "red-not-reproduced"
	ResultGreen          = "green"
	ResultNotGreen       = "not-green"
	ResultScenarioMissed = "incomplete"

	// ResultHostStalled 는 창 안 호스트 정지 ≥ StatusStaleThresh — 판정이 오퍼레이터를 재지 못해 재실행한다(ADR-0039).
	ResultHostStalled = "host-stalled"
	// ResultGreenHostStalls 는 전부 초록이고 J10 이 호스트 정지 보정을 썼다 — V1 통과(ADR-0039).
	ResultGreenHostStalls = "green-host-stalls"
)

// greenScenarios 는 GREEN 판에 도는 시나리오다(S0–S10).
var greenScenarios = []Scenario{
	ScenarioS0, ScenarioS1, ScenarioS2, ScenarioS3, ScenarioS4, ScenarioS5, ScenarioS5a,
	ScenarioS6, ScenarioS6b, ScenarioS7, ScenarioS8, ScenarioS9, ScenarioS10,
}

// GreenScenarios 는 GREEN 판의 시나리오 계획이다.
func GreenScenarios() []Scenario {
	return slices.Clone(greenScenarios)
}

// Outcome 은 모드의 통과 조건으로 판정문 결과를 낸다 — planned 는 계획한 시나리오 수다.
// 순서대로 첫 해당(ADR-0039): incomplete → host-stalled → RED 결과 · not-green → green-host-stalls → green.
func Outcome(mode Mode, planned int, runs []Verdict) string {
	if len(runs) < planned {
		return ResultScenarioMissed
	}

	// GREEN — 시나리오마다 마지막 판
	byScenario := map[Scenario]Verdict{}
	for _, v := range runs {
		byScenario[v.Scenario] = v
	}
	if mode != ModeRed && missing(byScenario) {
		return ResultScenarioMissed
	}

	// 30 s 넘는 정지는 어느 모드든 판 전체를 무효로 한다
	if hostStalled(runs) {
		return ResultHostStalled
	}

	if mode == ModeRed {
		if RedReproduced(runs) {
			return ResultRedAsExpected
		}
		return ResultRedMissing
	}

	for _, scn := range greenScenarios {
		if !byScenario[scn].Green() {
			return ResultNotGreen
		}
	}
	if stallCredited(runs) {
		return ResultGreenHostStalls
	}
	return ResultGreen
}

// missing 은 GREEN 계획의 시나리오가 하나라도 빠졌나.
func missing(byScenario map[Scenario]Verdict) bool {
	for _, scn := range greenScenarios {
		if _, ok := byScenario[scn]; !ok {
			return true
		}
	}
	return false
}

// hostStalled 는 어느 판이든 창 안 정지가 StatusStaleThresh 이상인가.
func hostStalled(runs []Verdict) bool {
	return slices.ContainsFunc(runs, func(v Verdict) bool {
		return slices.ContainsFunc(v.Observations, func(o Observation) bool {
			return o.ID == ObsHostStall && o.Longest >= StatusStaleThresh
		})
	})
}

// stallCredited 는 어느 판이든 J10 이 호스트 정지 보정을 썼나.
func stallCredited(runs []Verdict) bool {
	return slices.ContainsFunc(runs, func(v Verdict) bool {
		return slices.ContainsFunc(v.Observations, func(o Observation) bool {
			return o.ID == ObsHostStall && o.Credit > 0
		})
	})
}

// ── 다시 판정 ──
//
//	판정문(전후 스냅숏 · 기대) ─▶ 수집 규칙 다시 적용 ─▶ Judge ─▶ checks · result 새로 · rejudgedFrom
//
// 판정은 기록만 보는 순수 함수라 판정 함수만 고쳤으면 클러스터 없이 다시 잴 수 있다.

// storedReport 는 판정문 — 판정 밖 필드는 받은 그대로 둔다.
type storedReport struct {
	Expect       Mode            `json:"expect"`
	Result       string          `json:"result"`
	RejudgedFrom string          `json:"rejudgedFrom,omitempty"`
	Inputs       json.RawMessage `json:"inputs"`
	Probes       json.RawMessage `json:"probes"`
	Scenarios    []storedRun     `json:"scenarios"`
	StartedAt    json.RawMessage `json:"startedAt"`
	FinishedAt   json.RawMessage `json:"finishedAt"`
}

// storedRun 은 시나리오 한 판 — 전후 스냅숏은 기록 그대로 싣는다.
type storedRun struct {
	Scenario     Scenario        `json:"scenario"`
	Checks       []Check         `json:"checks"`
	Observations []Observation   `json:"observations,omitempty"`
	Round        int             `json:"round,omitempty"`
	Expect       Expect          `json:"expect"`
	Before       json.RawMessage `json:"before"`
	After        json.RawMessage `json:"after"`
}

// Flip 은 다시 판정에서 바뀐 판정 하나다.
type Flip struct {
	Scenario Scenario
	Round    int
	ID       CheckID
	Was      bool
	Now      bool
}

func (f Flip) String() string {
	return fmt.Sprintf("%s(%d판) %s %v→%v", f.Scenario, f.Round, f.ID, f.Was, f.Now)
}

// Rejudged 는 다시 판정의 결과 — 새 판정문 · 바뀐 판정 · 호스트 정지 관측(H1) 줄.
type Rejudged struct {
	Report []byte
	Flips  []Flip
	Stalls []string
}

// Rejudge 는 판정문 raw 를 지금 판정 함수로 다시 판정한다 — 기록(입력 · 전후 스냅숏 · 기대)은 그대로다.
// 호스트 정지 보정도 기록(after.hostStalls · ack due/sent)만 쓴다. 모르는 필드가 있으면 잃지 않도록 거부한다.
func Rejudge(raw []byte, from string) (Rejudged, error) {
	var report storedReport
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil {
		return Rejudged{}, fmt.Errorf("판정문 읽기: %w", err)
	}

	var re Rejudged
	verdicts := make([]Verdict, 0, len(report.Scenarios))
	for i := range report.Scenarios {
		run := &report.Scenarios[i]
		v, err := run.judge()
		if err != nil {
			return Rejudged{}, fmt.Errorf("%s(%d판): %w", run.Scenario, run.Round, err)
		}
		re.Flips = append(re.Flips, run.flips(v)...)
		re.Stalls = append(re.Stalls, run.stalls(v)...)
		run.Checks = v.Checks
		run.Observations = v.Observations
		verdicts = append(verdicts, v)
	}

	// 계획은 모드의 기본 계획이다(시나리오를 골라 돈 판은 incomplete 로 읽힌다)
	planned := len(greenScenarios)
	if report.Expect == ModeRed {
		planned = len(RedScenarios())
	}
	report.Result = Outcome(report.Expect, planned, verdicts)
	report.RejudgedFrom = from

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return Rejudged{}, err
	}
	re.Report = append(out, '\n')
	return re, nil
}

// judge 는 기록한 전후 스냅숏에 수집 규칙을 다시 적용하고 판정한다.
func (r storedRun) judge() (Verdict, error) {
	var before, after ClusterSnap
	if err := json.Unmarshal(r.Before, &before); err != nil {
		return Verdict{}, fmt.Errorf("before: %w", err)
	}
	if err := json.Unmarshal(r.After, &after); err != nil {
		return Verdict{}, fmt.Errorf("after: %w", err)
	}

	// 기록 뒤 고친 수집 규칙 — kubelet 종료 확인 delete 는 교체가 아니다
	after.Rollout.Steps = Replacements(after.Rollout.Steps)
	return Judge(r.Scenario, before, after, r.Expect), nil
}

// flips 는 기록한 판정과 v 가 갈린 자리다(없던 판정은 붉었던 것으로 센다).
func (r storedRun) flips(v Verdict) []Flip {
	was := make(map[CheckID]bool, len(r.Checks))
	for _, c := range r.Checks {
		was[c.ID] = c.Pass
	}
	var flips []Flip
	for _, c := range v.Checks {
		if was[c.ID] != c.Pass {
			flips = append(flips, Flip{Scenario: r.Scenario, Round: r.Round, ID: c.ID, Was: was[c.ID], Now: c.Pass})
		}
	}
	return flips
}

// stalls 는 v 의 호스트 정지 관측 줄이다 — 예: "S6b(1판) H1 호스트 정지 1 · 보정 1곳 9.2s · 가장 긴 정지 14.2s".
func (r storedRun) stalls(v Verdict) []string {
	var lines []string
	for _, o := range v.Observations {
		if o.ID != ObsHostStall {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s(%d판) %s %s", r.Scenario, r.Round, o.ID, o.Detail))
	}
	return lines
}
