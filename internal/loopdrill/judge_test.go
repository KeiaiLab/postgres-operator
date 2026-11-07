/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"slices"
	"testing"
	"time"
)

// 양성 대조 — 시나리오마다 정상 기록은 서는 판정이 전부 초록이다.
func TestGreenPairs(t *testing.T) {
	for scn, pair := range greenPairs() {
		t.Run(string(scn), func(t *testing.T) {
			before, after := pair()
			v := judgeGreen(scn, before, after)
			if !v.Green() {
				t.Fatalf("정상 기록이 붉다: %+v", v.Checks)
			}
			if len(v.Checks) != len(checkIDs(scn)) {
				t.Fatalf("판정 수 %d(기대 %d)", len(v.Checks), len(checkIDs(scn)))
			}
		})
	}
}

// 시나리오만으로 판정 목록이 정해진다 — 표본이 비어 판정이 빠지지 않는다.
func TestCheckSets(t *testing.T) {
	base := 16
	cases := map[Scenario][]CheckID{
		ScenarioS0:  {CheckOrder, CheckInPlace, CheckTransition},
		ScenarioS8:  {CheckOrder, CheckInPlace},
		ScenarioS10: {CheckEmergency},
		ScenarioS1:  nil,
		ScenarioS6b: nil,
	}
	for scn, extra := range cases {
		ids := checkIDs(scn)
		if len(ids) != base+len(extra) {
			t.Fatalf("%s 판정 %v(기대 기본 %d + %v)", scn, ids, base, extra)
		}
		for _, id := range extra {
			if !slices.Contains(ids, id) {
				t.Fatalf("%s 에 %s 없음", scn, id)
			}
		}
	}
	if slices.Contains(checkIDs(ScenarioS3), CheckEmergency) || slices.Contains(checkIDs(ScenarioS3), CheckInPlace) {
		t.Fatal("S3 에 비상·제자리 판정")
	}
}

func TestEmptySnapshotIsRed(t *testing.T) {
	before, after := transitionPair()
	for name, pair := range map[string][2]ClusterSnap{
		"before": {{}, after},
		"after":  {before, {}},
	} {
		v := judgeGreen(ScenarioS0, pair[0], pair[1])
		if len(v.Checks) != len(checkIDs(ScenarioS0)) {
			t.Fatalf("%s 빈 스냅숏 판정 수 %d", name, len(v.Checks))
		}
		for _, c := range v.Checks {
			if c.Pass {
				t.Fatalf("%s 빈 스냅숏에서 %s 초록", name, c.ID)
			}
		}
	}
}

func TestTemplateJudge(t *testing.T) {
	runRed(t, CheckTemplate, []redCase{
		{"generation 변화", ScenarioS4, func(_, a *ClusterSnap) { a.StsGeneration++ }},
		{"updateRevision 변화", ScenarioS4, func(_, a *ClusterSnap) { a.UpdateRevision = "sts-b" }},
		{"ControllerRevision 증가", ScenarioS4, func(_, a *ClusterSnap) { a.Revisions = append(a.Revisions, "sts-b") }},
		{"S8 +2", ScenarioS8, func(_, a *ClusterSnap) { a.StsGeneration++ }},
		{"S0 +0", ScenarioS0, func(b, a *ClusterSnap) { a.StsGeneration = b.StsGeneration }},
	})
}

func TestPVCJudge(t *testing.T) {
	runRed(t, CheckPVC, []redCase{
		{"UID 변화", ScenarioS5, func(_, a *ClusterSnap) { a.PVCUIDs["data-p-1"] = "u9" }},
		{"감사 delete", ScenarioS5, func(_, a *ClusterSnap) { a.AuditPVCDeletes = 1 }},
	})
}

func TestPromotionsJudge(t *testing.T) {
	runRed(t, CheckPromotions, []redCase{
		{"S5 승격", ScenarioS5, func(_, a *ClusterSnap) {
			a.Timeline = append(a.Timeline, TimelineMark{At: at(3 * time.Minute), Timeline: 6, Primary: "p-1"})
		}},
		{"S4 승격(설계 = 0)", ScenarioS4, func(_, a *ClusterSnap) {
			a.Timeline = append(a.Timeline, TimelineMark{At: at(3 * time.Minute), Timeline: 6, Primary: "p-1"})
		}},
		{"루프 — 유발 1회에 승격 2회", ScenarioS1, func(_, a *ClusterSnap) {
			a.Timeline = append(a.Timeline, TimelineMark{At: at(3 * time.Minute), Timeline: 7, Primary: "p-2"})
		}},
		{"관측창 추가 승격", ScenarioS1, func(_, a *ClusterSnap) {
			a.Timeline = []TimelineMark{{At: at(time.Minute), Timeline: 5}, {At: at(12 * time.Minute), Timeline: 6, Primary: "p-1"}}
		}},
		{"계획 switchover 0", ScenarioS8, func(_, a *ClusterSnap) { a.Timeline = a.Timeline[:1] }},
		{"S6b 승격 0(유발 무효)", ScenarioS6b, func(_, a *ClusterSnap) { a.Timeline = a.Timeline[:1] }},
		{"타임라인 미관측", ScenarioS1, func(_, a *ClusterSnap) { a.Timeline = nil }},
		{"유발 전 타임라인 미관측", ScenarioS1, func(b, a *ClusterSnap) {
			b.Timeline = nil
			a.Timeline = []TimelineMark{{At: at(time.Minute), Timeline: 1, Primary: "p-0"}}
		}},
	})
}

// 계획 switchover 가 관측창 안에 들면 그 승격 하나는 루프가 아니다 — 그 밖의 오름은 여전히 붉다.
func TestPromotionsPlannedInWindow(t *testing.T) {
	// switchoverPair 의 switchover(8m~8m20s)·승격(8m15s)을 관측창(7m~) 안에 둔다
	inWindow := func(a *ClusterSnap) { a.ObserveFrom = at(7 * time.Minute) }

	before, after := switchoverPair()
	inWindow(&after)
	wantPass(t, judgeGreen(ScenarioS8, before, after), CheckPromotions)

	before, after = transitionPair()
	inWindow(&after)
	wantPass(t, judgeGreen(ScenarioS0, before, after), CheckPromotions)

	runRed(t, CheckPromotions, []redCase{
		{"SwitchDone 뒤 추가 승격", ScenarioS8, func(_, a *ClusterSnap) {
			inWindow(a)
			a.Timeline = append(a.Timeline, TimelineMark{At: at(10 * time.Minute), Timeline: 7, Primary: "p-2"})
		}},
		{"switchover 시작 전 창 안 승격", ScenarioS8, func(_, a *ClusterSnap) {
			inWindow(a)
			a.Timeline[1].At = at(7*time.Minute + 30*time.Second)
		}},
		{"switchover 미관측", ScenarioS8, func(_, a *ClusterSnap) {
			inWindow(a)
			a.Rollout.SwitchStart, a.Rollout.SwitchDone = time.Time{}, time.Time{}
		}},
		{"승격 0 시나리오의 창 안 switchover", ScenarioS5, func(_, a *ClusterSnap) {
			a.Rollout.SwitchStart, a.Rollout.SwitchDone = at(11*time.Minute), at(11*time.Minute+20*time.Second)
			a.Timeline = append(a.Timeline, TimelineMark{At: at(11*time.Minute + 15*time.Second), Timeline: 6, Primary: "p-1"})
		}},
	})

	// 붉은 까닭이 관측창 규칙인가
	before, after = switchoverPair()
	inWindow(&after)
	after.Timeline[1].At = at(7*time.Minute + 30*time.Second)
	wantReason(t, judgeGreen(ScenarioS8, before, after), CheckPromotions, "관측창 추가 승격")
}

func TestStallJudge(t *testing.T) {
	runRed(t, CheckStall, []redCase{
		{"Failed to watch", ScenarioS5, func(_, a *ClusterSnap) { a.FailedWatchLines = 3 }},
		{"관측창 밖 쓰기만", ScenarioS5, func(_, a *ClusterSnap) {
			a.StatusWrites = []time.Time{at(2 * time.Minute), at(3 * time.Minute), at(11 * time.Minute)}
		}},
		{"같은 시각 두 번", ScenarioS5, func(_, a *ClusterSnap) {
			a.StatusWrites = []time.Time{at(11 * time.Minute), at(11 * time.Minute)}
		}},
		{"GREEN 게이지 정지", ScenarioS5, func(_, a *ClusterSnap) { a.GaugeWrites = []float64{100, 100} }},
		{"S5a NodeReadDenied 없음", ScenarioS5a, func(_, a *ClusterSnap) { a.Events = nil }},
		{"S5a 거부 계수 정지", ScenarioS5a, func(_, a *ClusterSnap) { a.DeniedReads = []float64{2, 2} }},
	})

	// RED 는 게이지를 요구하지 않는다
	before, after := quietPair()
	after.GaugeWrites = nil
	wantPass(t, Judge(ScenarioS5, before, after, ExpectFor(ScenarioS5, ModeRed, fixtureReplicas)), CheckStall)
}

func TestLeakJudge(t *testing.T) {
	runRed(t, CheckLeak, []redCase{
		{"Released 증가", ScenarioS4, func(b, a *ClusterSnap) { a.ReleasedPVs = b.ReleasedPVs + 1 }},
	})
}

func TestSourceJudge(t *testing.T) {
	runRed(t, CheckSource, []redCase{
		{"풀기 뒤 ready", ScenarioS1, func(_, a *ClusterSnap) { a.Snapshots[0].ReadyAt = at(6 * time.Minute) }},
		{"ready 못 봄", ScenarioS1, func(_, a *ClusterSnap) { a.Snapshots[0].ReadyAt = time.Time{} }},
		{"스냅숏 없음", ScenarioS1, func(_, a *ClusterSnap) { a.Snapshots = nil }},
	})
}

func TestBudgetJudge(t *testing.T) {
	runRed(t, CheckBudget, []redCase{
		{"예산 초과", ScenarioS7, func(_, a *ClusterSnap) {
			a.Rebuilds = []RebuildMark{
				{Member: "p-1", ReleasedAt: at(5 * time.Minute)},
				{Member: "p-2", ReleasedAt: at(8 * time.Minute)},
				{Member: "p-1", ReleasedAt: at(18 * time.Minute)},
			}
		}},
		{"S7 소진 미관측", ScenarioS7, func(_, a *ClusterSnap) { a.BudgetBlocked = false }},
	})
}

func TestFailoverJudge(t *testing.T) {
	runRed(t, CheckFailover, []redCase{
		{"승격이 PodReady False 뒤 상한 초과", ScenarioS1, func(_, a *ClusterSnap) {
			a.UnreadyAt = a.Timeline[1].At.Add(-PromotionBound - time.Second)
		}},
		{"PodReady False 미관측(하트비트만 승격)", ScenarioS1, func(_, a *ClusterSnap) { a.UnreadyAt = time.Time{} }},
		{"PodReady False 가 승격 뒤", ScenarioS9, func(_, a *ClusterSnap) { a.UnreadyAt = a.Timeline[1].At.Add(time.Second) }},
		{"표식 행 소실", ScenarioS6, func(_, a *ClusterSnap) { a.Writes.MarkerKept = false }},
		{"표식 행 미확인", ScenarioS6, func(_, a *ClusterSnap) { a.Writes.MarkerChecked = false }},
		{"S1 타임라인 미관측", ScenarioS1, func(b, _ *ClusterSnap) { b.Timeline = nil }},
	})

	// 하트비트만 낡은 승격은 그 까닭으로 붉다(시각이 아니라)
	before, after := failoverPair()
	after.UnreadyAt = time.Time{}
	wantReason(t, judgeGreen(ScenarioS1, before, after), CheckFailover, "전에 승격")

	// 경계 — 정확히 상한이면 초록, 계획 승격은 PodReady 를 보지 않는다
	before, after = failoverPair()
	after.UnreadyAt = after.Timeline[1].At.Add(-PromotionBound)
	wantPass(t, judgeGreen(ScenarioS1, before, after), CheckFailover)
	before, after = switchoverPair()
	wantPass(t, judgeGreen(ScenarioS8, before, after), CheckFailover)
}

func TestSurvivorsJudge(t *testing.T) {
	runRed(t, CheckSurvivors, []redCase{
		{"생존자가 늦게 붙는다", ScenarioS1, func(_, a *ClusterSnap) {
			a.Graph[1].At = a.Timeline[1].At.Add(RejoinBound + time.Second)
		}},
		{"생존자 재구축", ScenarioS1, func(_, a *ClusterSnap) {
			a.Rebuilds = append(a.Rebuilds, RebuildMark{Member: "p-2", ReleasedAt: at(2 * time.Minute)})
		}},
		{"유발 전 그래프 없음", ScenarioS1, func(b, _ *ClusterSnap) { b.Graph = nil }},
		{"승격한 파드 모름", ScenarioS1, func(_, a *ClusterSnap) { a.Timeline[1].Primary = "" }},
		{"다른 primary 에 붙음", ScenarioS10, func(_, a *ClusterSnap) {
			a.Graph = []GraphMark{{At: at(2*time.Minute + 20*time.Second), Primary: "p-1", Connected: []string{"p-2"}}}
		}},
	})
}

func TestExpectFor(t *testing.T) {
	members := fixtureMembers
	cases := map[Scenario]Expect{
		ScenarioS0: {Promotion: PromotionSwitchover, PromotionsAllowed: 1, PromotionsRequired: 1, GenerationDelta: 1, Budget: 2, Members: members,
			GaugeRequired: true, GapBound: WriteGapBudget, LongGaps: 1, ConvergeWithin: ConvergeBound + RolloutBound(members), TrustControl: true},
		ScenarioS1: {Promotion: PromotionFailover, PromotionsAllowed: 1, Budget: 2, Members: members, GaugeRequired: true,
			GapBound: FailoverGapBound, LongGaps: 1, LossAllowed: 1, ConvergeWithin: ConvergeBound},
		ScenarioS4: {Promotion: PromotionNone, Budget: 2, Members: members, GaugeRequired: true, GapBound: ReplicaGapBudget, ConvergeWithin: ConvergeBound},
		ScenarioS5a: {Promotion: PromotionNone, Budget: 2, Members: members, GaugeRequired: true, DeniedReads: true,
			GapBound: ReplicaGapBudget, ConvergeWithin: ConvergeBound},
		ScenarioS6b: {Promotion: PromotionFailover, PromotionsAllowed: 1, PromotionsRequired: 1, Budget: 2, Members: members, GaugeRequired: true,
			GapBound: FailoverGapBound, LongGaps: 1, LossAllowed: 1, ConvergeWithin: ConvergeBound, DetachedData: true},
		ScenarioS7: {Promotion: PromotionNone, BudgetExhaust: true, Budget: 2, Members: members, GaugeRequired: true,
			GapBound: ReplicaGapBudget, ConvergeWithin: ExhaustConvergeBound},
		ScenarioS8: {Promotion: PromotionSwitchover, PromotionsAllowed: 1, PromotionsRequired: 1, GenerationDelta: 1, Budget: 2, Members: members,
			GaugeRequired: true, GapBound: WriteGapBudget, LongGaps: 1, ConvergeWithin: ConvergeBound + RolloutBound(members), RolloutBlock: true},
		ScenarioS9: {Promotion: PromotionFailover, PromotionsAllowed: 1, Budget: 2, Members: members, GaugeRequired: true,
			GapBound: NodeDeathGapBound, LongGaps: 1, LossAllowed: 1, ConvergeWithin: ConvergeBound},
		ScenarioS10: {Promotion: PromotionEmergency, PromotionsAllowed: 1, Budget: 2, Members: members, GaugeRequired: true,
			GapBound: EmergencyGapBound, LongGaps: 1, ConvergeWithin: ConvergeBound},
	}
	for scn, want := range cases {
		if got := ExpectFor(scn, ModeGreen, fixtureReplicas); got != want {
			t.Fatalf("%s 기대 %+v, 얻음 %+v", scn, want, got)
		}
	}
	if got := ExpectFor(ScenarioS5, ModeRed, 0); got.Budget != 1 || got.GaugeRequired {
		t.Fatalf("replicas 0 · RED 기대 예산 1·게이지 불요, 얻음 %+v", got)
	}
}

func TestRedPlan(t *testing.T) {
	want := []Scenario{ScenarioS3, ScenarioS5, ScenarioS3, ScenarioS5}
	if got := RedScenarios(); !slices.Equal(got, want) {
		t.Fatalf("RED 판 %v(기대 %v)", got, want)
	}
	if got := RedTargets(ScenarioS3); !slices.Equal(got, []CheckID{CheckTemplate, CheckPVC, CheckPromotions, CheckLeak}) {
		t.Fatalf("S3 붉힐 판정 %v", got)
	}
	if got := RedTargets(ScenarioS5); !slices.Equal(got, []CheckID{CheckStall}) {
		t.Fatalf("S5 붉힐 판정 %v", got)
	}
	if RedTargets(ScenarioS4) != nil {
		t.Fatal("S4 는 양성 대조 밖")
	}
}

func TestRedReproduced(t *testing.T) {
	loopBefore, loop := failoverPair()
	loop.StsGeneration += 4
	stallBefore, stall := quietPair()
	stall.FailedWatchLines = 1
	cleanBefore, clean := quietPair()

	s3Red := Judge(ScenarioS3, loopBefore, loop, ExpectFor(ScenarioS3, ModeRed, fixtureReplicas))
	s3Green := Judge(ScenarioS3, cleanBefore, clean, ExpectFor(ScenarioS3, ModeRed, fixtureReplicas))
	s5Red := Judge(ScenarioS5, stallBefore, stall, ExpectFor(ScenarioS5, ModeRed, fixtureReplicas))
	s5Green := Judge(ScenarioS5, cleanBefore, clean, ExpectFor(ScenarioS5, ModeRed, fixtureReplicas))
	s4Red := Judge(ScenarioS4, loopBefore, loop, ExpectFor(ScenarioS4, ModeRed, fixtureReplicas))

	cases := []struct {
		name string
		runs []Verdict
		want bool
	}{
		{"각 2판 전부 붉음", []Verdict{s3Red, s5Red, s3Red, s5Red}, true},
		{"S3 한 판 초록", []Verdict{s3Red, s5Red, s3Green, s5Red}, false},
		{"S5 한 판 초록", []Verdict{s3Red, s5Green, s3Red, s5Red}, false},
		{"한 판씩뿐", []Verdict{s3Red, s5Red}, false},
		{"S3 셋 중 하나 초록", []Verdict{s3Red, s3Red, s3Green, s5Red, s5Red}, false},
		{"S3 대신 S4", []Verdict{s4Red, s5Red, s4Red, s5Red}, false},
		{"없음", nil, false},
	}
	for _, c := range cases {
		if got := RedReproduced(c.runs); got != c.want {
			t.Fatalf("%s: %v(기대 %v)", c.name, got, c.want)
		}
	}
}

func TestWindowsCoverSpec(t *testing.T) {
	// 설계 §3.12 — 창 = 수렴 상한 + 관측 reseedCooldown, S5 정지 > force delete 문턱 + Stale 판정
	if ConvergeBound != StaleBound+SnapshotReadyBound+CooldownBound {
		t.Fatalf("ConvergeBound %s", ConvergeBound)
	}
	// S7 — 예산 밖 재구축은 창 안 가장 오랜 해제가 rebuildBudgetWindow 를 넘길 때까지 막힌다
	if ExhaustConvergeBound != RebuildBudgetWindow+ConvergeBound {
		t.Fatalf("ExhaustConvergeBound %s", ExhaustConvergeBound)
	}
	if StallStopDuration <= StuckTerminatingBound+StaleBound {
		t.Fatalf("StallStopDuration %s ≤ 설계 하한", StallStopDuration)
	}
	if !slices.IsSorted([]time.Duration{StatusPollInterval, StatusStaleThresh, FaultPause, StaleBound, CooldownBound}) {
		t.Fatal("창 크기 순서가 뒤집혔다")
	}

	// 부록 A 값 — switchoverBudget 100 s · writeProbeWarmup 12 · rolloutBound ≈42분 · J8 70 s · S6b 60 s
	exact := []struct {
		name      string
		got, want time.Duration
	}{
		{"SwitchoverBudget", SwitchoverBudget, 100 * time.Second},
		{"PromotionBound", PromotionBound, 70 * time.Second},
		{"DetachLead", DetachLead, time.Minute},
		{"PrimaryFreezeHold", PrimaryFreezeHold, 130 * time.Second},
		{"RolloutBound(3)", RolloutBound(fixtureMembers), 41*time.Minute + 40*time.Second},
		{"WriteGapBudget", WriteGapBudget, StatusStaleThresh},
		{"WriteProbeInterval", WriteProbeInterval, StatusPollInterval},
	}
	for _, e := range exact {
		if e.got != e.want {
			t.Fatalf("%s = %s(기대 %s)", e.name, e.got, e.want)
		}
	}
	if WriteProbeWarmup != 12 {
		t.Fatalf("WriteProbeWarmup = %d(기대 12)", WriteProbeWarmup)
	}

	// 유지 시간은 그 시나리오가 부르는 판정 문턱을 넘어야 한다
	if NodeDeathHold <= NodeDeathBound || RolloutBlockHold <= MemberConvergeBudget || DetachLead <= RetargetBound {
		t.Fatal("S9·S8·S6b 유지가 문턱을 넘지 않는다")
	}
	if PrimaryFreezeHold <= PromotionBound {
		t.Fatal("S6b primary 정지가 승격 경계를 넘지 않는다")
	}
	if !slices.IsSorted([]time.Duration{ReplicaGapBudget, WriteGapBudget, FailoverGapBound, NodeDeathGapBound, EmergencyGapBound}) {
		t.Fatal("쓰기 공백 상한 순서가 뒤집혔다")
	}
}
