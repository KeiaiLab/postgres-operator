/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// v1Verdict 는 cgroup v1 kind 판(17369c33 · data ea0d0fe4) 판정문 — 교체 6(kubelet 확인 delete) · S0 창 안 switchover.
const v1Verdict = "testdata/verdict-17369c33-ea0d0fe4.json.gz"

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// 판정만 고친 뒤 기록을 다시 판정하면 S0 J3 만 초록으로 바뀌고 나머지 판정은 그대로다.
// S0·S8 J12 는 교체 수(kubelet 확인 delete)를 지나 다음 규칙 "하나씩"에서 붉다 — 이 판의 ReadyAt 은 표본 시각이라
// 다음 삭제가 한 표본 간격(StatusPollInterval) 안쪽으로 이르다. Ready 전이 시각(ReturnedAt)은 기록에 없어
// 다시 판정으로는 못 고친다 — 다시 돈 판만 그것을 싣는다.
func TestRejudgeV1Verdict(t *testing.T) {
	raw := readFixture(t, v1Verdict)
	re1, err := Rejudge(raw, v1Verdict)
	if err != nil {
		t.Fatal(err)
	}
	out, flips := re1.Report, re1.Flips

	// 옛 판정문에는 정지 기록이 없다 — 관측 0
	if len(re1.Stalls) != 0 {
		t.Fatalf("정지 관측 %v", re1.Stalls)
	}

	got := make([]string, 0, len(flips))
	for _, f := range flips {
		got = append(got, f.String())
	}
	want := []string{"S0(1판) J3 false→true"}
	if !slices.Equal(got, want) {
		t.Fatalf("바뀐 판정 %v(기대 %v)", got, want)
	}

	// 교체 수는 3 이 됐다 — J12 는 다른 까닭으로 붉다
	var report storedReport
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	for _, run := range report.Scenarios {
		if len(run.Observations) != 0 {
			t.Fatalf("%s 관측 %v", run.Scenario, run.Observations)
		}
		if !applies(CheckOrder, run.Scenario) {
			continue
		}
		v := Verdict{Scenario: run.Scenario, Checks: run.Checks}
		wantReason(t, v, CheckOrder, "하나씩 아님")
	}

	// 기록은 그대로 · 결과는 다시 · 출처를 남긴다
	var in, re map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &re); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"expect", "inputs", "probes", "startedAt", "finishedAt"} {
		if !jsonEqual(t, in[key], re[key]) {
			t.Fatalf("%s 가 바뀌었다", key)
		}
	}
	var from, result string
	_ = json.Unmarshal(re["rejudgedFrom"], &from)
	_ = json.Unmarshal(re["result"], &result)
	if from != v1Verdict {
		t.Fatalf("rejudgedFrom %q", from)
	}
	// S10 을 못 돈 판이라 여전히 incomplete
	if result != ResultScenarioMissed {
		t.Fatalf("result %q(기대 %s)", result, ResultScenarioMissed)
	}

	// 전후 스냅숏·기대는 기록 그대로
	var inRuns, reRuns []map[string]json.RawMessage
	_ = json.Unmarshal(in["scenarios"], &inRuns)
	_ = json.Unmarshal(re["scenarios"], &reRuns)
	if len(inRuns) != len(reRuns) {
		t.Fatalf("시나리오 %d→%d", len(inRuns), len(reRuns))
	}
	for i := range inRuns {
		for _, key := range []string{"scenario", "round", "expect", "before", "after"} {
			if !jsonEqual(t, inRuns[i][key], reRuns[i][key]) {
				t.Fatalf("%d 번째 시나리오 %s 가 바뀌었다", i, key)
			}
		}
	}
}

// 모르는 필드는 잃지 않도록 거부한다.
func TestRejudgeUnknownField(t *testing.T) {
	if _, err := Rejudge([]byte(`{"expect":"green","extra":1}`), "x"); err == nil {
		t.Fatal("모르는 필드를 받아들였다")
	}
	if _, err := Rejudge([]byte(`{"expect":"green","scenarios":[{"scenario":"S0","extra":1}]}`), "x"); err == nil {
		t.Fatal("시나리오의 모르는 필드를 받아들였다")
	}
}

// stalledReport 는 GREEN 전 시나리오 초록 짝에 S6b 만 판 5 꼴 정지(보정 9.2 s)를 넣은 판정문이다.
func stalledReport(t *testing.T) []byte {
	t.Helper()
	pairs := greenPairs()
	pairs[ScenarioS6b] = run5Pair

	report := storedReport{Expect: ModeGreen}
	for _, scn := range GreenScenarios() {
		before, after := pairs[scn]()
		exp := ExpectFor(scn, ModeGreen, fixtureReplicas)
		v := Judge(scn, before, after, exp)
		report.Scenarios = append(report.Scenarios, storedRun{
			Scenario: scn, Checks: v.Checks, Round: 1, Expect: exp, Observations: v.Observations,
			Before: mustJSON(t, before), After: mustJSON(t, after),
		})
	}
	return mustJSON(t, report)
}

// 정지 기록(after.hostStalls · ack due/sent)만으로 같은 보정을 다시 하고, observations 를 받아들인다.
func TestRejudgeHostStalls(t *testing.T) {
	re, err := Rejudge(stalledReport(t), "stalled")
	if err != nil {
		t.Fatal(err)
	}
	if len(re.Flips) != 0 {
		t.Fatalf("바뀐 판정 %v", re.Flips)
	}
	if len(re.Stalls) != 1 || !strings.HasPrefix(re.Stalls[0], "S6b(1판) H1 ") || !strings.Contains(re.Stalls[0], "보정 1곳 9.2s") {
		t.Fatalf("정지 관측 %v", re.Stalls)
	}

	var report storedReport
	if err := json.Unmarshal(re.Report, &report); err != nil {
		t.Fatal(err)
	}
	if report.Result != ResultGreenHostStalls {
		t.Fatalf("result %q(기대 %s)", report.Result, ResultGreenHostStalls)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOutcome(t *testing.T) {
	green := Verdict{Scenario: ScenarioS0, Checks: []Check{{ID: CheckTemplate, Pass: true}}}
	all := make([]Verdict, 0, len(GreenScenarios()))
	for _, scn := range GreenScenarios() {
		v := green
		v.Scenario = scn
		all = append(all, v)
	}
	if got := Outcome(ModeGreen, len(all), all); got != ResultGreen {
		t.Fatalf("전부 초록 %s", got)
	}
	if got := Outcome(ModeGreen, len(all)+1, all); got != ResultScenarioMissed {
		t.Fatalf("계획보다 적음 %s", got)
	}
	if got := Outcome(ModeGreen, len(all)-1, all[1:]); got != ResultScenarioMissed {
		t.Fatalf("S0 빠짐 %s", got)
	}
	all[3].Checks = []Check{{ID: CheckTemplate}}
	if got := Outcome(ModeGreen, len(all), all); got != ResultNotGreen {
		t.Fatalf("하나 붉음 %s", got)
	}
	if got := Outcome(ModeRed, 0, nil); got != ResultRedMissing {
		t.Fatalf("RED 판 없음 %s", got)
	}
}

// ADR-0039 결과 순서 — incomplete → host-stalled → RED·not-green → green-host-stalls → green.
func TestOutcomeHostStalls(t *testing.T) {
	credited := Observation{ID: ObsHostStall, Count: 1, Credited: 1, Credit: 9 * time.Second, Longest: 14 * time.Second}
	seen := Observation{ID: ObsHostStall, Count: 1, Longest: 14 * time.Second}
	stalled := Observation{ID: ObsHostStall, Count: 1, Longest: StatusStaleThresh}

	// all 은 GREEN 전 시나리오 초록, i 번째 판에 obs 를 단 판들이다.
	all := func(i int, obs ...Observation) []Verdict {
		runs := make([]Verdict, 0, len(greenScenarios))
		for _, scn := range greenScenarios {
			runs = append(runs, Verdict{Scenario: scn, Checks: []Check{{ID: CheckTemplate, Pass: true}}})
		}
		runs[i].Observations = obs
		return runs
	}
	n := len(greenScenarios)

	cases := []struct {
		name string
		mode Mode
		plan int
		runs func() []Verdict
		want string
	}{
		{"초록 · H1 보정", ModeGreen, n, func() []Verdict { return all(8, credited) }, ResultGreenHostStalls},
		{"초록 · H1 보정 0", ModeGreen, n, func() []Verdict { return all(8, seen) }, ResultGreen},
		{"붉음 · H1 보정", ModeGreen, n, func() []Verdict {
			runs := all(8, credited)
			runs[3].Checks = []Check{{ID: CheckTemplate}}
			return runs
		}, ResultNotGreen},
		{"30 s 정지", ModeGreen, n, func() []Verdict { return all(8, stalled) }, ResultHostStalled},
		{"30 s 정지 · 붉음", ModeGreen, n, func() []Verdict {
			runs := all(8, stalled)
			runs[3].Checks = []Check{{ID: CheckTemplate}}
			return runs
		}, ResultHostStalled},
		{"30 s 정지 · RED", ModeRed, 1, func() []Verdict { return all(0, stalled)[:1] }, ResultHostStalled},
		{"29 s 정지 · RED", ModeRed, 0, func() []Verdict {
			obs := stalled
			obs.Longest -= time.Second
			return all(0, obs)[:1]
		}, ResultRedMissing},
		{"계획보다 적음 · 30 s 정지", ModeGreen, n + 1, func() []Verdict { return all(8, stalled) }, ResultScenarioMissed},
		{"S0 빠짐 · 30 s 정지", ModeGreen, n - 1, func() []Verdict { return all(8, stalled)[1:] }, ResultScenarioMissed},
		{"RED 계획보다 적음 · 30 s 정지", ModeRed, 2, func() []Verdict { return all(0, stalled)[:1] }, ResultScenarioMissed},
	}
	for _, c := range cases {
		if got := Outcome(c.mode, c.plan, c.runs()); got != c.want {
			t.Errorf("%s: %s(기대 %s)", c.name, got, c.want)
		}
	}
}

// jsonEqual 은 두 JSON 값이 공백을 빼고 같은가.
func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var ca, cb bytes.Buffer
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	if err := json.Compact(&ca, a); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&cb, b); err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}
