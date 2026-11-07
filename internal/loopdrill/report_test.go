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
	"testing"
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
	out, flips, err := Rejudge(raw, v1Verdict)
	if err != nil {
		t.Fatal(err)
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
	if _, _, err := Rejudge([]byte(`{"expect":"green","extra":1}`), "x"); err == nil {
		t.Fatal("모르는 필드를 받아들였다")
	}
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
