/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWriteGapJudge(t *testing.T) {
	runRed(t, CheckWriteGap, []redCase{
		{"ack 0", ScenarioS5, func(_, a *ClusterSnap) { a.Probe.Acks = nil }},
		{"ack 1", ScenarioS5, func(_, a *ClusterSnap) { a.Probe.Acks = a.Probe.Acks[:1] }},
		{"짧은 창 ack 0 — 하한이 0 이하여도", ScenarioS5, func(_, a *ClusterSnap) {
			a.Probe = ProbeRecord{From: a.TakenAt.Add(-7 * time.Second), To: a.TakenAt, Primary: "p-0"}
		}},
		{"창 미관측", ScenarioS5, func(_, a *ClusterSnap) { a.Probe.From = time.Time{} }},
		{"창 뒤집힘", ScenarioS5, func(_, a *ClusterSnap) { a.Probe.To = a.Probe.From }},
		{"표본 하한 — 간격 둘에 한 번", ScenarioS5, func(_, a *ClusterSnap) { a.Probe.Acks = everyOther(a.Probe.Acks) }},
		{"replica 단계 11 s 공백", ScenarioS6, func(_, a *ClusterSnap) {
			a.Probe = probeFor(drillStart, a.TakenAt, "p-0", span{at(3 * time.Minute), at(3*time.Minute + 11*time.Second)})
		}},
		{"switchover 공백 35 s", ScenarioS8, func(_, a *ClusterSnap) {
			a.Probe = probeFor(drillStart, a.TakenAt, "p-1", span{at(8 * time.Minute), at(8*time.Minute + 35*time.Second)})
		}},
		{"switchover 에 긴 공백 둘", ScenarioS0, func(_, a *ClusterSnap) {
			a.Probe = probeFor(drillStart, a.TakenAt, "p-1",
				span{at(3 * time.Minute), at(3*time.Minute + 15*time.Second)},
				span{at(8 * time.Minute), at(8*time.Minute + 20*time.Second)})
		}},
		{"창 시작부터 공백", ScenarioS5, func(_, a *ClusterSnap) {
			a.Probe = probeFor(drillStart, a.TakenAt, "p-0", span{drillStart.Add(-time.Second), at(15 * time.Second)})
		}},
		{"창 끝까지 공백", ScenarioS5, func(_, a *ClusterSnap) {
			a.Probe = probeFor(drillStart, a.TakenAt, "p-0", span{at(19*time.Minute + 45*time.Second), a.TakenAt.Add(time.Second)})
		}},
		{"failover 공백 상한 초과", ScenarioS1, func(_, a *ClusterSnap) {
			a.Probe = probeFor(drillStart, a.TakenAt, "p-1", span{at(time.Minute), at(time.Minute).Add(FailoverGapBound + WriteProbeInterval)})
		}},
		{"비상 정지 공백 상한 초과", ScenarioS10, func(_, a *ClusterSnap) {
			a.Probe = probeFor(drillStart, a.TakenAt, "p-0", span{at(time.Minute), at(time.Minute).Add(EmergencyGapBound + WriteProbeInterval)})
		}},
	})

	// 표본 하한 경계 — 창 1200 s · 상한 10 s 면 하한 238. 떨어진 ack 셋을 빼면 238(초록), 넷이면 237(붉음)
	before, after := quietPair()
	after.Probe.Acks = dropEvery(after.Probe.Acks, 40, 3)
	wantPass(t, judgeGreen(ScenarioS5, before, after), CheckWriteGap)
	after.Probe.Acks = dropEvery(after.Probe.Acks, 45, 1)
	wantFail(t, judgeGreen(ScenarioS5, before, after), CheckWriteGap)

	// 창 밖 ack 은 세지 않는다 — 창 앞 ack 만 많아도 붉다
	before, after = quietPair()
	after.Probe.Acks = append(after.Probe.Acks, probeFor(drillStart.Add(-time.Hour), drillStart.Add(-time.Minute), "p-0").Acks...)
	wantPass(t, judgeGreen(ScenarioS5, before, after), CheckWriteGap)
	after.Probe.Acks = probeFor(drillStart.Add(-time.Hour), drillStart.Add(-time.Minute), "p-0").Acks
	wantFail(t, judgeGreen(ScenarioS5, before, after), CheckWriteGap)

	// 노드 사망은 감지가 길다 — failover 상한을 넘어도 S9 상한 안이면 초록
	before, after = failoverPair()
	after.Probe = probeFor(drillStart, after.TakenAt, "p-1", span{at(time.Minute), at(time.Minute).Add(FailoverGapBound + WriteProbeInterval)})
	wantPass(t, judgeGreen(ScenarioS9, before, after), CheckWriteGap)
}

// dropEvery 는 stride 번째마다 ack 을 n 개까지 뺀다(떨어진 자리 — 공백은 2 간격씩).
func dropEvery(acks []ProbeAck, stride, n int) []ProbeAck {
	var out []ProbeAck
	dropped := 0
	for i, a := range acks {
		if i > 0 && i%stride == 0 && dropped < n {
			dropped++
			continue
		}
		out = append(out, a)
	}
	return out
}

func everyOther(acks []ProbeAck) []ProbeAck {
	var out []ProbeAck
	for i, a := range acks {
		if i%2 == 0 {
			out = append(out, a)
		}
	}
	return out
}

func TestLosslessJudge(t *testing.T) {
	runRed(t, CheckLossless, []redCase{
		{"ack 0", ScenarioS8, func(_, a *ClusterSnap) { a.Probe.Acks = nil }},
		{"대조 못 함", ScenarioS8, func(_, a *ClusterSnap) { a.Probe.Primary = "" }},
		{"switchover 1개 소실", ScenarioS8, func(_, a *ClusterSnap) { a.Probe.Stored = dropID(a.Probe, 40) }},
		{"전환 1개 소실", ScenarioS0, func(_, a *ClusterSnap) { a.Probe.Stored = dropID(a.Probe, 100) }},
		{"standby 장애 1개 소실", ScenarioS6, func(_, a *ClusterSnap) { a.Probe.Stored = dropID(a.Probe, 7) }},
		{"비상 정지 1개 소실", ScenarioS10, func(_, a *ClusterSnap) { a.Probe.Stored = dropID(a.Probe, 7) }},
		{"failover 2개 소실", ScenarioS1, func(_, a *ClusterSnap) {
			a.Probe.Stored = dropID(ProbeRecord{Stored: dropID(a.Probe, 7)}, 8)
		}},
		{"최종 primary 가 비었다", ScenarioS5, func(_, a *ClusterSnap) { a.Probe.Stored = nil }},
	})

	// failover 는 탐침 1회 소실까지 초록(설계 §3.12 J15)
	before, after := failoverPair()
	after.Probe.Stored = dropID(after.Probe, 7)
	wantPass(t, judgeGreen(ScenarioS1, before, after), CheckLossless)
}

// dropID 는 저장 구간에서 id 하나를 뺀다.
func dropID(p ProbeRecord, id int64) []IDRange {
	var ids []int64
	for _, r := range p.Stored {
		for i := r.From; i <= r.To; i++ {
			if i != id {
				ids = append(ids, i)
			}
		}
	}
	return IDRanges(ids)
}

func TestParseAcks(t *testing.T) {
	log := "connect 1790000000.5\n" +
		"ack 1 1790000001.123456\n" +
		"ack 2 1790000006\n" +
		"ack x 1790000007.1\n" +
		"ack 3\n" +
		"ERROR:  cannot execute INSERT in a read-only transaction\n" +
		"noise 5 1790000009.0\n" +
		"  ack 4 1790000011.000000001  \n" +
		"disconnect 1790000012.0 rc=2\n"
	got := ParseAcks(log)
	want := []ProbeAck{
		{ID: 1, At: time.Unix(1790000001, 123456000).UTC()},
		{ID: 2, At: time.Unix(1790000006, 0).UTC()},
		{ID: 4, At: time.Unix(1790000011, 1).UTC()},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ack %+v(기대 %+v)", got, want)
	}
	if ParseAcks("") != nil {
		t.Fatal("빈 로그에서 ack")
	}
}

func TestIDRanges(t *testing.T) {
	got := IDRanges([]int64{7, 3, 4, 5, 9, 9, 8, 1})
	want := []IDRange{{From: 1, To: 1}, {From: 3, To: 5}, {From: 7, To: 9}}
	if !slices.Equal(got, want) {
		t.Fatalf("구간 %v(기대 %v)", got, want)
	}
	if IDRanges(nil) != nil {
		t.Fatal("빈 id 에서 구간")
	}
}

func TestAckSpan(t *testing.T) {
	lo, hi, ok := AckSpan([]ProbeAck{{ID: 9}, {ID: 3}, {ID: 5}})
	if !ok || lo != 3 || hi != 9 {
		t.Fatalf("범위 %d–%d %v", lo, hi, ok)
	}
	if _, _, ok := AckSpan(nil); ok {
		t.Fatal("빈 ack 에서 범위")
	}
}

func TestWarmedUp(t *testing.T) {
	acks := probeFor(drillStart, at(time.Minute), "p-0").Acks // 13개, 5 s 간격
	if !WarmedUp(acks, WriteProbeWarmup) {
		t.Fatal("연속 13개인데 준비 안 됨")
	}
	if WarmedUp(acks[:WriteProbeWarmup-1], WriteProbeWarmup) {
		t.Fatal("11개로 준비됨")
	}
	gapped := probeFor(drillStart, at(2*time.Minute), "p-0", span{at(90 * time.Second), at(101 * time.Second)}).Acks
	if len(gapped) < WriteProbeWarmup || WarmedUp(gapped, WriteProbeWarmup) {
		t.Fatalf("끝 %d개 안에 15 s 공백인데 준비됨(ack %d)", WriteProbeWarmup, len(gapped))
	}
	older := probeFor(drillStart, at(2*time.Minute), "p-0", span{at(5 * time.Second), at(17 * time.Second)}).Acks
	if !WarmedUp(older, WriteProbeWarmup) {
		t.Fatal("공백이 끝 12개 앞인데 준비 안 됨")
	}
}

func TestSampleFloor(t *testing.T) {
	// 설계 부록 A — ⌊창 ÷ 간격⌋ − ⌈상한 ÷ 간격⌉
	cases := []struct {
		window, bound time.Duration
		want          int
	}{
		{10 * time.Minute, WriteGapBudget, 114},
		{10 * time.Minute, ReplicaGapBudget, 118},
		{10 * time.Minute, 31 * time.Second, 113},
		{7 * time.Second, WriteGapBudget, -5},
	}
	for _, c := range cases {
		if got := sampleFloor(c.window, c.bound); got != c.want {
			t.Fatalf("sampleFloor(%s, %s) = %d(기대 %d)", c.window, c.bound, got, c.want)
		}
	}
}

func TestWriteGapStallCredit(t *testing.T) {
	// 판 5 S6b — 두 계측기가 함께 멈춘 9.2 s 를 빼면 5.1 s 라 초록, 보정이 Detail 에
	before, after := run5Pair()
	v := judgeGreen(ScenarioS6b, before, after)
	wantPass(t, v, CheckWriteGap)
	if c := checkOf(t, v, CheckWriteGap); !strings.Contains(c.Detail, "호스트 정지 보정 9.2s") {
		t.Fatalf("보정이 Detail 에 없음: %s", c.Detail)
	}

	// 보정 0 인 자리 — 기존 한도 그대로라 붉다
	runRed(t, CheckWriteGap, []redCase{
		{"5-필드 ack · 정지 기록 없음", ScenarioS6b, func(b, a *ClusterSnap) {
			*b, *a = run5Pair()
			a.HostStalls = nil
		}},
		{"정지 기록 · 3-필드 ack", ScenarioS6b, func(b, a *ClusterSnap) {
			*b, *a = run5Pair()
			for i := range a.Probe.Acks {
				a.Probe.Acks[i].Due, a.Probe.Acks[i].Sent = time.Time{}, time.Time{}
			}
		}},
		{"DB 20 s 정지 안 탐침 9.3 s 정지", ScenarioS6b, func(b, a *ClusterSnap) {
			*b, *a = run5Pair()
			a.Probe.Acks = slices.DeleteFunc(a.Probe.Acks, func(k ProbeAck) bool { return k.At.After(ms(28370)) && k.At.Before(ms(50000)) })
			a.Probe.Acks = append(a.Probe.Acks, probeAck(1850, ms(48370), ms(33370), ms(42670)))
			a.HostStalls = []StallSpan{{From: ms(33370), To: ms(42670)}}
		}},
		{"창 끝 공백 안 정지", ScenarioS5, func(_, a *ClusterSnap) {
			a.Probe = probeFor(drillStart, a.TakenAt, "p-0", span{at(19*time.Minute + 45*time.Second), a.TakenAt.Add(time.Second)})
			last := len(a.Probe.Acks) - 1
			a.Probe.Acks[last].Due, a.Probe.Acks[last].Sent = a.Probe.Acks[last].At, a.Probe.Acks[last].At
			a.HostStalls = []StallSpan{{From: at(19*time.Minute + 46*time.Second), To: a.TakenAt}}
		}},
		{"창 시작 공백 안 정지", ScenarioS5, func(_, a *ClusterSnap) {
			a.Probe = probeFor(drillStart, a.TakenAt, "p-0", span{drillStart.Add(-time.Second), at(15 * time.Second)})
			a.Probe.Acks[0].Due, a.Probe.Acks[0].Sent = drillStart, a.Probe.Acks[0].At
			a.HostStalls = []StallSpan{{From: drillStart, To: at(15 * time.Second)}}
		}},
	})
}

func TestStallCredit(t *testing.T) {
	prev := ProbeAck{ID: 1, At: ms(0)}
	cases := []struct {
		name   string
		cur    ProbeAck
		stalls []StallSpan
		want   time.Duration
	}{
		{"맹점 ∩ 정지", probeAck(2, ms(14000), ms(5000), ms(13000)), []StallSpan{{From: ms(2000), To: ms(12000)}}, 7 * time.Second},
		{"공백 밖은 빼지 않는다", probeAck(2, ms(14000), ms(-3000), ms(13000)), []StallSpan{{From: ms(-5000), To: ms(20000)}}, 13 * time.Second},
		{"겹친 정지를 두 번 세지 않는다", probeAck(2, ms(14000), ms(5000), ms(13000)),
			[]StallSpan{{From: ms(6000), To: ms(9000)}, {From: ms(7000), To: ms(10000)}, {From: ms(11000), To: ms(12000)}}, 5 * time.Second},
		{"지연이 문턱과 같다", probeAck(2, ms(14000), ms(5000), ms(5000).Add(HostStallFloor)), []StallSpan{{From: ms(0), To: ms(14000)}}, 0},
		{"3-필드", ProbeAck{ID: 2, At: ms(14000)}, []StallSpan{{From: ms(0), To: ms(14000)}}, 0},
		{"정지 없음", probeAck(2, ms(14000), ms(5000), ms(13000)), nil, 0},
		{"맹점 밖 정지", probeAck(2, ms(14000), ms(5000), ms(13000)), []StallSpan{{From: ms(0), To: ms(4000)}}, 0},
	}
	for _, c := range cases {
		if got := stallCredit(prev, c.cur, c.stalls); got != c.want {
			t.Errorf("%s: 보정 %s(기대 %s)", c.name, got, c.want)
		}
	}
}

func TestHostStallObservation(t *testing.T) {
	// 판 5 — 창 안 정지 셋(하나는 보정에 씀), 창 밖 정지는 세지 않는다
	before, after := run5Pair()
	after.HostStalls = append(after.HostStalls,
		StallSpan{From: at(5 * time.Minute), To: at(5*time.Minute + 2*time.Second)},
		StallSpan{From: at(17 * time.Minute), To: at(17*time.Minute + 20*time.Second)},
		StallSpan{From: drillStart.Add(-time.Hour), To: drillStart.Add(-50 * time.Minute)})
	v := judgeGreen(ScenarioS6b, before, after)
	if len(v.Observations) != 1 {
		t.Fatalf("관측 %+v", v.Observations)
	}
	o := v.Observations[0]
	if o.ID != ObsHostStall || o.Count != 3 || o.Credited != 1 || o.Longest != 20*time.Second ||
		o.Credit.Round(100*time.Millisecond) != 9200*time.Millisecond || o.Detail == "" {
		t.Fatalf("H1 %+v", o)
	}

	// 정지가 없으면 관측도 없다(옛 판정문과 같다)
	before, after = quietPair()
	if v := judgeGreen(ScenarioS5, before, after); len(v.Observations) != 0 {
		t.Fatalf("정지 없이 관측 %+v", v.Observations)
	}
}

func TestGreenIgnoresObservations(t *testing.T) {
	v := Verdict{
		Checks:       []Check{{ID: CheckWriteGap, Pass: true}},
		Observations: []Observation{{ID: ObsHostStall, Count: 2, Credited: 1, Credit: 9 * time.Second, Longest: 40 * time.Second}},
	}
	if !v.Green() {
		t.Fatal("H1 관측이 초록을 막음")
	}
}

func TestParseAckFields(t *testing.T) {
	log := "ack 1 1790000001.5 1790000001.0 1790000001.4\n" +
		"ack 2 1790000006.0 1790000006.0\n" +
		"ack 3 1790000011.0 x 1790000011.0\n" +
		"ack 4 1790000016.0\n" +
		"ack 5 1790000021.0 1790000021.0 1790000021.0 9\n"
	got := ParseAcks(log)
	want := []ProbeAck{
		{ID: 1, At: time.Unix(1790000001, 5e8).UTC(), Due: time.Unix(1790000001, 0).UTC(), Sent: time.Unix(1790000001, 4e8).UTC()},
		{ID: 4, At: time.Unix(1790000016, 0).UTC()},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ack %+v(기대 %+v)", got, want)
	}
}
