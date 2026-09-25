/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"maps"
	"slices"
	"testing"
	"time"
)

var drillStart = time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return drillStart.Add(d) }

// quietPair 는 유발 1회 뒤 아무 일도 없던 시나리오의 전후(전 판정 초록).
func quietPair() (ClusterSnap, ClusterSnap) {
	before := ClusterSnap{
		TakenAt:        drillStart,
		StsGeneration:  3,
		UpdateRevision: "sts-a",
		Revisions:      []string{"sts-a", "sts-0"},
		PVCUIDs:        map[string]string{"data-p-0": "u0", "data-p-1": "u1", "data-p-2": "u2"},
		ReleasedPVs:    1,
		Timeline:       []TimelineMark{{At: drillStart, Timeline: 5}},
		Graph:          []GraphMark{{At: drillStart, Primary: "p-0", Connected: []string{"p-1", "p-2"}}},
	}
	after := before
	after.TakenAt = at(20 * time.Minute)
	after.Revisions = []string{"sts-0", "sts-a"}
	after.PVCUIDs = maps.Clone(before.PVCUIDs)
	after.Timeline = []TimelineMark{{At: at(time.Minute), Timeline: 5}, {At: at(19 * time.Minute), Timeline: 5}}
	after.Faults = []time.Time{at(time.Minute)}
	after.ObserveFrom = at(10 * time.Minute)
	after.StatusWrites = []time.Time{at(2 * time.Minute), at(11 * time.Minute), at(15 * time.Minute)}
	after.Writes = WriteMark{MarkerChecked: true, MarkerKept: true}
	after.Graph = []GraphMark{{At: at(19 * time.Minute), Primary: "p-0", Connected: []string{"p-1", "p-2"}}}
	return before, after
}

// promotedPair 는 S1 처럼 유발 한 번에 승격 한 번이 정상으로 끝난 전후.
func promotedPair() (ClusterSnap, ClusterSnap) {
	before, after := quietPair()
	after.Timeline = []TimelineMark{{At: at(time.Minute), Timeline: 5}, {At: at(90 * time.Second), Timeline: 6}}
	after.Writes.WritableAt = at(100 * time.Second)
	after.Graph = []GraphMark{
		{At: at(95 * time.Second), Primary: "p-1", Connected: nil},
		{At: at(110 * time.Second), Primary: "p-1", Connected: []string{"p-2"}},
	}
	return before, after
}

func checkOf(t *testing.T, v Verdict, id CheckID) Check {
	t.Helper()
	for _, c := range v.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("판정 %s 없음: %+v", id, v.Checks)
	return Check{}
}

func wantPass(t *testing.T, v Verdict, id CheckID) {
	t.Helper()
	if c := checkOf(t, v, id); !c.Pass {
		t.Fatalf("%s 붉음(초록 기대): %s", id, c.Detail)
	}
}

func wantFail(t *testing.T, v Verdict, id CheckID) {
	t.Helper()
	if c := checkOf(t, v, id); c.Pass {
		t.Fatalf("%s 초록(붉음 기대): %s", id, c.Detail)
	}
}

func TestQuietScenarioIsGreen(t *testing.T) {
	before, after := quietPair()
	v := Judge(ScenarioS6, before, after, ExpectFor(ScenarioS6, ModeRed, 2))
	if !v.Green() {
		t.Fatalf("조용한 시나리오가 붉다: %+v", v.Checks)
	}
	if len(v.Checks) != 9 {
		t.Fatalf("판정 수 %d(기대 9)", len(v.Checks))
	}
}

func TestEmptySnapshotIsRed(t *testing.T) {
	before, after := quietPair()
	for name, pair := range map[string][2]ClusterSnap{
		"before": {{}, after},
		"after":  {before, {}},
	} {
		v := Judge(ScenarioS0, pair[0], pair[1], ExpectFor(ScenarioS0, ModeGreen, 2))
		if len(v.Checks) != 10 {
			t.Fatalf("%s 빈 스냅숏 판정 수 %d(기대 10)", name, len(v.Checks))
		}
		for _, c := range v.Checks {
			if c.Pass {
				t.Fatalf("%s 빈 스냅숏에서 %s 초록", name, c.ID)
			}
		}
	}
}

func TestTemplateJudge(t *testing.T) {
	before, after := quietPair()
	after.StsGeneration++
	wantFail(t, Judge(ScenarioS4, before, after, ExpectFor(ScenarioS4, ModeRed, 2)), CheckTemplate)

	before, after = quietPair()
	after.UpdateRevision = "sts-b"
	wantFail(t, Judge(ScenarioS4, before, after, ExpectFor(ScenarioS4, ModeRed, 2)), CheckTemplate)

	before, after = quietPair()
	after.Revisions = append(after.Revisions, "sts-b")
	wantFail(t, Judge(ScenarioS4, before, after, ExpectFor(ScenarioS4, ModeRed, 2)), CheckTemplate)

	// S0 는 정확히 +1
	before, after = quietPair()
	after.StsGeneration++
	after.UpdateRevision = "sts-b"
	wantPass(t, Judge(ScenarioS0, before, after, ExpectFor(ScenarioS0, ModeGreen, 2)), CheckTemplate)
	after.StsGeneration++
	wantFail(t, Judge(ScenarioS0, before, after, ExpectFor(ScenarioS0, ModeGreen, 2)), CheckTemplate)
}

func TestPVCJudge(t *testing.T) {
	before, after := quietPair()
	after.PVCUIDs["data-p-1"] = "u9"
	wantFail(t, Judge(ScenarioS5, before, after, ExpectFor(ScenarioS5, ModeRed, 2)), CheckPVC)

	before, after = quietPair()
	after.AuditPVCDeletes = 1
	wantFail(t, Judge(ScenarioS5, before, after, ExpectFor(ScenarioS5, ModeRed, 2)), CheckPVC)
}

func TestPromotionsJudge(t *testing.T) {
	before, after := promotedPair()
	wantPass(t, Judge(ScenarioS1, before, after, ExpectFor(ScenarioS1, ModeGreen, 2)), CheckPromotions)
	wantFail(t, Judge(ScenarioS5, before, after, ExpectFor(ScenarioS5, ModeGreen, 2)), CheckPromotions)

	// 루프 — 유발 1회에 승격 2회
	before, after = promotedPair()
	after.Timeline = append(after.Timeline, TimelineMark{At: at(3 * time.Minute), Timeline: 7})
	wantFail(t, Judge(ScenarioS1, before, after, ExpectFor(ScenarioS1, ModeGreen, 2)), CheckPromotions)

	// 허용 안이어도 수렴 뒤 관측창에서 오르면 붉다
	before, after = quietPair()
	after.Timeline = append(after.Timeline, TimelineMark{At: at(12 * time.Minute), Timeline: 6})
	wantFail(t, Judge(ScenarioS1, before, after, ExpectFor(ScenarioS1, ModeGreen, 2)), CheckPromotions)

	before, after = quietPair()
	after.Timeline = nil
	wantFail(t, Judge(ScenarioS1, before, after, ExpectFor(ScenarioS1, ModeGreen, 2)), CheckPromotions)
}

func TestStallJudge(t *testing.T) {
	before, after := quietPair()
	after.FailedWatchLines = 3
	wantFail(t, Judge(ScenarioS5, before, after, ExpectFor(ScenarioS5, ModeRed, 2)), CheckStall)

	// 관측창(10m~) 밖의 쓰기는 세지 않는다
	before, after = quietPair()
	after.StatusWrites = []time.Time{at(2 * time.Minute), at(3 * time.Minute), at(11 * time.Minute)}
	wantFail(t, Judge(ScenarioS5, before, after, ExpectFor(ScenarioS5, ModeRed, 2)), CheckStall)

	// 같은 시각은 한 번이다
	before, after = quietPair()
	after.StatusWrites = []time.Time{at(11 * time.Minute), at(11 * time.Minute)}
	wantFail(t, Judge(ScenarioS5, before, after, ExpectFor(ScenarioS5, ModeRed, 2)), CheckStall)

	// GREEN 은 게이지도 전진해야 한다
	before, after = quietPair()
	wantFail(t, Judge(ScenarioS5, before, after, ExpectFor(ScenarioS5, ModeGreen, 2)), CheckStall)
	after.GaugeWrites = []float64{100, 105, 105, 110}
	wantPass(t, Judge(ScenarioS5, before, after, ExpectFor(ScenarioS5, ModeGreen, 2)), CheckStall)
}

func TestLeakJudge(t *testing.T) {
	before, after := quietPair()
	after.ReleasedPVs = before.ReleasedPVs + 1
	wantFail(t, Judge(ScenarioS4, before, after, ExpectFor(ScenarioS4, ModeRed, 2)), CheckLeak)
}

func TestSourceJudge(t *testing.T) {
	before, after := quietPair()
	after.Snapshots = []SnapshotMark{{Name: "p-1-rebuild-a", Member: "p-1", ReadyAt: at(5 * time.Minute)}}
	after.Rebuilds = []RebuildMark{{Member: "p-1", Reason: "Stale", ReleasedAt: at(6 * time.Minute), Snapshot: "p-1-rebuild-a"}}
	wantPass(t, Judge(ScenarioS6, before, after, ExpectFor(ScenarioS6, ModeGreen, 2)), CheckSource)

	after.Snapshots[0].ReadyAt = at(7 * time.Minute)
	wantFail(t, Judge(ScenarioS6, before, after, ExpectFor(ScenarioS6, ModeGreen, 2)), CheckSource)

	after.Snapshots = nil
	wantFail(t, Judge(ScenarioS6, before, after, ExpectFor(ScenarioS6, ModeGreen, 2)), CheckSource)
}

func TestBudgetJudge(t *testing.T) {
	before, after := quietPair()
	after.Rebuilds = []RebuildMark{
		{Member: "p-1", ReleasedAt: at(5 * time.Minute)},
		{Member: "p-2", ReleasedAt: at(8 * time.Minute)},
		{Member: "p-1", ReleasedAt: at(18 * time.Minute)},
	}
	wantFail(t, Judge(ScenarioS7, before, after, ExpectFor(ScenarioS7, ModeGreen, 2)), CheckBudget)

	after.Rebuilds = after.Rebuilds[:2]
	wantFail(t, Judge(ScenarioS7, before, after, ExpectFor(ScenarioS7, ModeGreen, 2)), CheckBudget)
	after.BudgetBlocked = true
	wantPass(t, Judge(ScenarioS7, before, after, ExpectFor(ScenarioS7, ModeGreen, 2)), CheckBudget)
}

func TestFailoverJudge(t *testing.T) {
	before, after := promotedPair()
	wantPass(t, Judge(ScenarioS1, before, after, ExpectFor(ScenarioS1, ModeGreen, 2)), CheckFailover)

	after.Writes.WritableAt = after.Faults[0].Add(WritableBound + time.Second)
	wantFail(t, Judge(ScenarioS1, before, after, ExpectFor(ScenarioS1, ModeGreen, 2)), CheckFailover)

	before, after = promotedPair()
	after.Writes.WritableAt = time.Time{}
	wantFail(t, Judge(ScenarioS1, before, after, ExpectFor(ScenarioS1, ModeGreen, 2)), CheckFailover)

	before, after = quietPair()
	after.Writes.MarkerKept = false
	wantFail(t, Judge(ScenarioS6, before, after, ExpectFor(ScenarioS6, ModeGreen, 2)), CheckFailover)

	before, after = quietPair()
	after.Writes.MarkerChecked = false
	wantFail(t, Judge(ScenarioS6, before, after, ExpectFor(ScenarioS6, ModeGreen, 2)), CheckFailover)
}

func TestSurvivorsJudge(t *testing.T) {
	before, after := promotedPair()
	wantPass(t, Judge(ScenarioS1, before, after, ExpectFor(ScenarioS1, ModeGreen, 2)), CheckSurvivors)

	// 생존자가 늦게 붙는다
	before, after = promotedPair()
	after.Graph[1].At = at(90*time.Second + RejoinBound + time.Second)
	wantFail(t, Judge(ScenarioS1, before, after, ExpectFor(ScenarioS1, ModeGreen, 2)), CheckSurvivors)

	// 생존자를 재구축했다
	before, after = promotedPair()
	after.Rebuilds = []RebuildMark{{Member: "p-2", ReleasedAt: at(2 * time.Minute)}}
	wantFail(t, Judge(ScenarioS1, before, after, ExpectFor(ScenarioS1, ModeGreen, 2)), CheckSurvivors)

	before, after = promotedPair()
	before.Graph = nil
	wantFail(t, Judge(ScenarioS1, before, after, ExpectFor(ScenarioS1, ModeGreen, 2)), CheckSurvivors)
}

func TestTransitionJudge(t *testing.T) {
	before, after := quietPair()
	after.StsGeneration++
	after.UpdateRevision = "sts-b"
	after.PodsOnUpdate = 3
	exp := ExpectFor(ScenarioS0, ModeGreen, 2)
	wantPass(t, Judge(ScenarioS0, before, after, exp), CheckTransition)

	after.PodsOnUpdate = 2
	wantFail(t, Judge(ScenarioS0, before, after, exp), CheckTransition)

	after.PodsOnUpdate = 3
	after.AuditPodDeletes = 1
	wantFail(t, Judge(ScenarioS0, before, after, exp), CheckTransition)

	after.AuditPodDeletes = 0
	after.Timeline = append(after.Timeline, TimelineMark{At: at(5 * time.Minute), Timeline: 6})
	wantFail(t, Judge(ScenarioS0, before, after, exp), CheckTransition)
}

func TestRedReproduced(t *testing.T) {
	before, after := quietPair()
	loop := after
	loop.StsGeneration += 4
	stall := after
	stall.FailedWatchLines = 1

	s4Red := Judge(ScenarioS4, before, loop, ExpectFor(ScenarioS4, ModeRed, 2))
	s4Green := Judge(ScenarioS4, before, after, ExpectFor(ScenarioS4, ModeRed, 2))
	s5Red := Judge(ScenarioS5, before, stall, ExpectFor(ScenarioS5, ModeRed, 2))
	s5Green := Judge(ScenarioS5, before, after, ExpectFor(ScenarioS5, ModeRed, 2))

	if !RedReproduced(s4Red, s5Red) {
		t.Fatal("S4 J1 붉음 ∧ S5 J4 붉음 인데 재현 아님")
	}
	if RedReproduced(s4Green, s5Red) {
		t.Fatal("S4 초록인데 재현")
	}
	if RedReproduced(s4Red, s5Green) {
		t.Fatal("S5 초록인데 재현")
	}
	if RedReproduced(s5Red, s4Red) {
		t.Fatal("시나리오 뒤바뀜을 재현으로 읽음")
	}
}

func TestExpectFor(t *testing.T) {
	cases := map[Scenario]Expect{
		ScenarioS0: {GenerationDelta: 1, Budget: 2, Members: 3, GaugeRequired: true},
		ScenarioS1: {PromotionsAllowed: 1, Budget: 2, Members: 3, GaugeRequired: true},
		ScenarioS4: {PromotionsAllowed: 1, Budget: 2, Members: 3, GaugeRequired: true},
		ScenarioS5: {Budget: 2, Members: 3, GaugeRequired: true},
		ScenarioS7: {BudgetExhaust: true, Budget: 2, Members: 3, GaugeRequired: true},
	}
	for scn, want := range cases {
		if got := ExpectFor(scn, ModeGreen, 2); got != want {
			t.Fatalf("%s 기대 %+v, 얻음 %+v", scn, want, got)
		}
	}
	if got := ExpectFor(ScenarioS5, ModeRed, 0); got.Budget != 1 || got.GaugeRequired {
		t.Fatalf("replicas 0 · RED 기대 예산 1·게이지 불요, 얻음 %+v", got)
	}
}

func TestWindowsCoverSpec(t *testing.T) {
	// 설계 §3.12 — 창 = 수렴 상한 + 관측 reseedCooldown, S5 정지 > force delete 문턱 + Stale 판정
	if ConvergeBound != StaleBound+SnapshotReadyBound+CooldownBound {
		t.Fatalf("ConvergeBound %s", ConvergeBound)
	}
	if StallStopDuration <= StuckTerminatingBound+StaleBound {
		t.Fatalf("StallStopDuration %s ≤ 설계 하한", StallStopDuration)
	}
	if !slices.IsSorted([]time.Duration{StatusPollInterval, StatusStaleThresh, FaultPause, StaleBound, CooldownBound}) {
		t.Fatal("창 크기 순서가 뒤집혔다")
	}
}
