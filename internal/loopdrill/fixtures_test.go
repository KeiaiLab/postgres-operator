/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"maps"
	"strings"
	"testing"
	"time"
)

// 시험 기록 — 시나리오 종류마다 모든 판정이 초록인 짝(양성 대조)을 두고, 붉은 경우는 그 짝을 한 곳만 어긴다.
//
//	quietPair       승격 0(S4·S5·S6·S7 계열)       p-0 primary · p-1·p-2 streaming
//	failoverPair    S1 계열 — 65 s PodReady False · 90 s p-1 승격 · p-0 Demoted 재구축
//	detachedPair    S6b — failoverPair + Demoted data-bearing · VAP 거부
//	switchoverPair  S8 — p-2 → p-1 교체 · switchover(p-1) · p-0 교체
//	transitionPair  S0 — switchoverPair + 사전 trust · 운영 누적물 정리
//	emergencyPair   S10 — Stopping · 오퍼레이터 둘 세움 · pg-stop 이 p-0 를 다시 승격

const (
	fixtureReplicas = 2
	fixtureMembers  = fixtureReplicas + 1
	operatorUser    = "system:serviceaccount:data:op"
	podP1           = "p-1"
	podP2           = "p-2"
)

var drillStart = time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return drillStart.Add(d) }

// span 은 탐침이 ack 을 못 받은 구간(열린 구간 — 경계의 ack 은 남는다).
type span struct{ from, to time.Time }

// probeFor 는 [from, to] 동안 WriteProbeInterval 마다 ack 하고(skips 안은 빼고) 전부 primary 에 남은 기록이다.
func probeFor(from, to time.Time, primary string, skips ...span) ProbeRecord {
	p := ProbeRecord{From: from, To: to, Primary: primary}
	var ids []int64
	id := int64(0)
	for t := from; !t.After(to); t = t.Add(WriteProbeInterval) {
		skipped := false
		for _, s := range skips {
			if t.After(s.from) && t.Before(s.to) {
				skipped = true
			}
		}
		if skipped {
			continue
		}
		id++
		p.Acks = append(p.Acks, ProbeAck{ID: id, At: t})
		ids = append(ids, id)
	}
	p.Stored = IDRanges(ids)
	return p
}

func greenPlacement() PlacementRecord {
	return PlacementRecord{
		Nodes:    map[string]string{"p-0": "w1", "p-1": "w2", "p-2": "w3"},
		Eligible: []string{"w1", "w2", "w3"},
		Avoid:    []string{"w4"},
	}
}

func greenHBA() HBARecord {
	return HBARecord{
		Members: []HBAMember{
			{Pod: "p-0", Fresh: true},
			{Pod: "p-1", Fresh: true},
			{Pod: "p-2", Fresh: true},
		},
		NoPassword: ConnAuthRejected,
		Probe:      ConnAccepted,
		Superuser:  ConnAccepted,
	}
}

// quietPair 는 유발 1회 뒤 아무 일도 없던 시나리오의 전후(전 판정 초록).
func quietPair() (ClusterSnap, ClusterSnap) {
	before := ClusterSnap{
		TakenAt:        drillStart,
		StsGeneration:  3,
		UpdateRevision: "sts-a",
		Revisions:      []string{"sts-a", "sts-0"},
		UpdateStrategy: OnDeleteStrategy,
		PVCUIDs:        map[string]string{"data-p-0": "u0", "data-p-1": "u1", "data-p-2": "u2"},
		ReleasedPVs:    1,
		Timeline:       []TimelineMark{{At: drillStart, Timeline: 5, Primary: "p-0"}},
		Graph:          []GraphMark{{At: drillStart, Primary: "p-0", Connected: []string{"p-1", "p-2"}, Streaming: []string{"p-1", "p-2"}}},
	}
	after := before
	after.TakenAt = at(20 * time.Minute)
	after.Revisions = []string{"sts-0", "sts-a"}
	after.PVCUIDs = maps.Clone(before.PVCUIDs)
	after.Timeline = []TimelineMark{{At: at(time.Minute), Timeline: 5, Primary: "p-0"}, {At: at(19 * time.Minute), Timeline: 5, Primary: "p-0"}}
	after.MaxPrimaries = 1
	after.Faults = []time.Time{at(time.Minute)}
	after.ReleasedAt = at(2 * time.Minute)
	after.ConvergedAt = at(3 * time.Minute)
	after.ObserveFrom = at(10 * time.Minute)
	after.StatusWrites = []time.Time{at(2 * time.Minute), at(13 * time.Minute), at(15 * time.Minute)}
	after.GaugeWrites = []float64{100, 105, 110}
	after.Writes = WriteMark{MarkerChecked: true, MarkerKept: true}
	after.Graph = []GraphMark{{At: at(19 * time.Minute), Primary: "p-0", Connected: []string{"p-1", "p-2"}, Streaming: []string{"p-1", "p-2"}}}
	after.PodsOnUpdate = fixtureMembers
	after.Probe = probeFor(drillStart, after.TakenAt, "p-0")
	after.Placement = greenPlacement()
	after.HBA = greenHBA()
	return before, after
}

// failoverPair 는 S1 처럼 유발 한 번에 승격 한 번이 정상으로 끝난 전후.
func failoverPair() (ClusterSnap, ClusterSnap) {
	before, after := quietPair()
	after.UnreadyAt = at(65 * time.Second)
	after.ReleasedAt = at(time.Minute)
	after.ConvergedAt = at(6 * time.Minute)
	after.Timeline = []TimelineMark{{At: at(time.Minute), Timeline: 5, Primary: "p-0"}, {At: at(90 * time.Second), Timeline: 6, Primary: "p-1"}}
	after.Graph = []GraphMark{
		{At: at(95 * time.Second), Primary: "p-1"},
		{At: at(110 * time.Second), Primary: "p-1", Connected: []string{"p-2"}, Streaming: []string{"p-2"}},
		{At: at(19 * time.Minute), Primary: "p-1", Connected: []string{"p-0", "p-2"}, Streaming: []string{"p-0", "p-2"}},
	}
	after.Probe = probeFor(drillStart, after.TakenAt, "p-1", span{at(time.Minute), at(110 * time.Second)})
	after.Snapshots = []SnapshotMark{{Name: "p-0-rebuild-a", Member: "p-0", ReadyAt: at(4 * time.Minute), Reason: RebuildDemoted, DataBearing: LabelTrue}}
	after.Rebuilds = []RebuildMark{{Member: "p-0", Reason: "MemberRejoinReleased", ReleasedAt: at(5 * time.Minute), Snapshot: "p-0-rebuild-a"}}
	return before, after
}

// detachedPair 는 S6b — failover 에 옛 primary Demoted(data-bearing) 스냅숏과 VAP 거부가 섰다.
func detachedPair() (ClusterSnap, ClusterSnap) {
	before, after := failoverPair()
	after.VAP = VAPDenied
	return before, after
}

// switchoverPair 는 S8 — 템플릿 변경 뒤 옛 replica 둘 → switchover → 옛 primary 교체(롤아웃 막힘을 거쳤다).
func switchoverPair() (ClusterSnap, ClusterSnap) {
	before, after := quietPair()
	after.StsGeneration = before.StsGeneration + 1
	after.UpdateRevision = "sts-b"
	after.Revisions = []string{"sts-0", "sts-a", "sts-b"}
	after.ReleasedAt = at(time.Minute)
	after.ConvergedAt = at(12 * time.Minute)
	after.ObserveFrom = at(12 * time.Minute)
	after.Timeline = []TimelineMark{{At: at(time.Minute), Timeline: 5, Primary: "p-0"}, {At: at(8*time.Minute + 15*time.Second), Timeline: 6, Primary: "p-1"}}
	after.Graph = []GraphMark{
		{At: at(8*time.Minute + 30*time.Second), Primary: "p-1", Connected: []string{"p-0", "p-2"}, Streaming: []string{"p-0", "p-2"}},
		{At: at(19 * time.Minute), Primary: "p-1", Connected: []string{"p-0", "p-2"}, Streaming: []string{"p-0", "p-2"}},
	}
	after.Probe = probeFor(drillStart, after.TakenAt, "p-1", span{at(8 * time.Minute), at(8*time.Minute + 20*time.Second)})
	after.Rollout = RolloutRecord{
		Operator:   operatorUser,
		OldPrimary: "p-0",
		Steps: []RolloutStep{
			{Pod: "p-2", DeletedAt: at(2 * time.Minute), By: operatorUser, Streaming: true, ReadyAt: at(4 * time.Minute)},
			{Pod: "p-1", DeletedAt: at(5 * time.Minute), By: operatorUser, Streaming: true, ReadyAt: at(7 * time.Minute)},
			{Pod: "p-0", DeletedAt: at(9 * time.Minute), By: operatorUser, Streaming: true, ReadyAt: at(11 * time.Minute)},
		},
		SwitchStart: at(8 * time.Minute),
		SwitchDone:  at(8*time.Minute + 20*time.Second),
		BlockedSeen: true,
	}
	after.Switchover = SwitchoverRecord{
		OldPrimary: "p-0", OldUID: "uid-p0", OldRestarts: 0,
		BackUID: "uid-p0", BackRestarts: 1, BackInRecovery: true,
		OldEnd: "0/3000060", Fork: "0/30000D8", Completed: true,
	}
	return before, after
}

// transitionPair 는 S0 — switchoverPair 에 사전 trust 양성 대조와 운영 누적물 정리가 섰다.
func transitionPair() (ClusterSnap, ClusterSnap) {
	before, after := switchoverPair()
	after.Rollout.BlockedSeen = false
	before.HBA = HBARecord{PreTrust: ConnAccepted}
	before.Settings = []MemberSettings{{Pod: "p-0", Primary: true, LogConnections: "on", ConninfoLines: 300, WalKeepSource: "/var/lib/postgresql/data/pgdata/" + AutoConfFile}}
	after.Settings = []MemberSettings{
		{Pod: "p-0", WalKeepSource: "/etc/postgresql/postgresql.conf", LogConnections: "on", ConninfoLines: 1},
		{Pod: "p-1", Primary: true, WalKeepSource: "/etc/postgresql/postgresql.conf", LogConnections: "on", Marker: true},
		{Pod: "p-2", WalKeepSource: "/etc/postgresql/postgresql.conf", LogConnections: "on", ConninfoLines: 1},
	}
	return before, after
}

// emergencyPair 는 S10 — Stopping 에서 오퍼레이터 둘을 세우고 pg-stop 이 옛 primary p-0 를 다시 승격했다.
func emergencyPair() (ClusterSnap, ClusterSnap) {
	before, after := quietPair()
	after.ReleasedAt = at(2 * time.Minute)
	after.ConvergedAt = at(4 * time.Minute)
	after.Timeline = []TimelineMark{{At: at(time.Minute), Timeline: 5, Primary: "p-0"}, {At: at(2*time.Minute + 10*time.Second), Timeline: 6, Primary: "p-0"}}
	after.Graph = []GraphMark{
		{At: at(2*time.Minute + 20*time.Second), Primary: "p-0", Connected: []string{"p-1", "p-2"}, Streaming: []string{"p-1", "p-2"}},
		{At: at(19 * time.Minute), Primary: "p-0", Connected: []string{"p-1", "p-2"}, Streaming: []string{"p-1", "p-2"}},
	}
	after.Probe = probeFor(drillStart, after.TakenAt, "p-0", span{at(time.Minute), at(2*time.Minute + 25*time.Second)})
	after.Emergency = EmergencyRecord{
		StoppingAt: at(time.Minute), Operators: EmergencyOperators, Frozen: EmergencyOperators,
		RecoverAt: at(2 * time.Minute), EndpointsAt: at(2*time.Minute + 20*time.Second), WriteAt: at(2*time.Minute + 25*time.Second),
	}
	return before, after
}

// deniedPair 는 S5a — nodes 권한 없이 정지 0 · NodeReadDenied · api_read_denied_total 전진.
func deniedPair() (ClusterSnap, ClusterSnap) {
	before, after := quietPair()
	after.Events = []EventMark{{At: at(3 * time.Minute), Type: "Warning", Reason: EventNodeReadDenied, Count: 1}}
	after.DeniedReads = []float64{0, 2}
	return before, after
}

// exhaustedPair 는 S7 — 예산 안 재구축 둘과 MemberRebuildBlocked(BudgetExhausted).
func exhaustedPair() (ClusterSnap, ClusterSnap) {
	before, after := quietPair()
	after.BudgetBlocked = true
	return before, after
}

// greenPairs 는 시나리오마다 모든 판정이 초록인 기록이다(양성 대조).
func greenPairs() map[Scenario]func() (ClusterSnap, ClusterSnap) {
	return map[Scenario]func() (ClusterSnap, ClusterSnap){
		ScenarioS0: transitionPair, ScenarioS1: failoverPair, ScenarioS2: failoverPair, ScenarioS3: failoverPair,
		ScenarioS4: quietPair, ScenarioS5: quietPair, ScenarioS5a: deniedPair, ScenarioS6: quietPair,
		ScenarioS6b: detachedPair, ScenarioS7: exhaustedPair, ScenarioS8: switchoverPair, ScenarioS9: failoverPair,
		ScenarioS10: emergencyPair,
	}
}

func judgeGreen(scn Scenario, before, after ClusterSnap) Verdict {
	return Judge(scn, before, after, ExpectFor(scn, ModeGreen, fixtureReplicas))
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

// wantReason 은 판정이 붉고 그 까닭이 reason 인지 본다 — 다른 규칙이 대신 붉히면 수정자가 엉뚱한 곳을 본다.
func wantReason(t *testing.T, v Verdict, id CheckID, reason string) {
	t.Helper()
	c := checkOf(t, v, id)
	if c.Pass || !strings.Contains(c.Detail, reason) {
		t.Fatalf("%s 기대 붉음(%q), 얻음 pass=%v %q", id, reason, c.Pass, c.Detail)
	}
}

// redCase 는 초록 짝을 한 곳만 어긴 기록 — 그 판정만 붉어야 한다.
type redCase struct {
	name  string
	scn   Scenario
	spoil func(before, after *ClusterSnap)
}

// runRed 는 경우마다 시나리오의 초록 짝을 spoil 로 어기고 id 가 붉은지 본다.
func runRed(t *testing.T, id CheckID, cases []redCase) {
	t.Helper()
	pairs := greenPairs()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before, after := pairs[c.scn]()
			c.spoil(&before, &after)
			wantFail(t, judgeGreen(c.scn, before, after), id)
		})
	}
}
