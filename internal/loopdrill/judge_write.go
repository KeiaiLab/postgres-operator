/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// 쓰기 탐침(testdata/write-probe.yaml · 운영 L1 같은 파일) 판정 — J10 공백 · J15 유실.
// 탐침은 -rw Service 에 한 연결로 WriteProbeInterval 마다 INSERT 하고, commit 을 받은 행을 한 줄씩 남긴다.
//
//	ack <id> <epoch>        commit 을 받은 행(RETURNING — PQexec 는 commit 뒤에 결과를 준다)
//	ack <id> <epoch> <due> <sent>   같은 행 + 예정 시각 · 실제로 낸 시각(ADR-0039 탐침 맹점)
//	connect · disconnect    연결 수명(증거 — 판정은 ack 만 본다)
//
// J10 은 [창 시작, 첫 ack] · ack 사이 · [마지막 ack, 창 끝] 공백을, J15 는 ack 된 id 가 최종 primary 에 있는지를 본다.
// ack 사이 공백은 탐침 맹점과 호스트 정지가 겹친 시간만큼 줄인다(stallCredit).

// ackPrefix 는 탐침 로그의 ack 줄 머리다.
const ackPrefix = "ack"

// lostShown 은 판정 글에 싣는 소실 id 수 상한이다(RED 는 수백 개일 수 있다).
const lostShown = 5

// ack 줄 필드 수 — 옛 탐침(보정 0) · due·sent 를 싣는 탐침.
const (
	ackFields     = 3
	ackFieldsSent = 5
)

// creditShown 은 판정 글에 싣는 보정 해상도(HostStallSample 단위 오차).
const creditShown = HostStallSample

// ProbeAck 은 쓰기 탐침이 ack 받은 행 하나.
type ProbeAck struct {
	ID   int64     `json:"id"`
	At   time.Time `json:"at"`            // 서버 commit 시각(exec)
	Due  time.Time `json:"due,omitzero"`  // 탐침이 이 문장을 낼 예정 시각 — 3-필드면 영값
	Sent time.Time `json:"sent,omitzero"` // 탐침이 실제로 낸 시각
}

// IDRange 는 최종 primary 에 있는 탐침 id 의 닫힌 구간.
type IDRange struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// ProbeRecord 는 판정 창 동안 쓰기 탐침의 기록.
type ProbeRecord struct {
	From    time.Time  `json:"from,omitzero"`     // 판정 창 시작(= before 시각)
	To      time.Time  `json:"to,omitzero"`       // 판정 창 끝(= after 시각)
	Acks    []ProbeAck `json:"acks,omitempty"`    // 창 안 ack(시각순)
	Stored  []IDRange  `json:"stored,omitempty"`  // 최종 primary 에 있는 id(ack 범위 안)
	Primary string     `json:"primary,omitempty"` // 대조한 최종 primary — 비면 대조 못 함
}

// within 은 [From, To] 안의 ack 를 시각순으로 낸다.
func (p ProbeRecord) within() []ProbeAck {
	var acks []ProbeAck
	for _, a := range p.Acks {
		if a.At.Before(p.From) || a.At.After(p.To) {
			continue
		}
		acks = append(acks, a)
	}
	slices.SortFunc(acks, func(a, b ProbeAck) int { return a.At.Compare(b.At) })
	return acks
}

// stored 는 id 가 최종 primary 에 있는가.
func (p ProbeRecord) stored(id int64) bool {
	return slices.ContainsFunc(p.Stored, func(r IDRange) bool { return r.From <= id && id <= r.To })
}

// gaps 는 창 가장자리를 포함한 공백 — ack n 개면 n+1 개.
func (p ProbeRecord) gaps(acks []ProbeAck) []time.Duration {
	gaps := make([]time.Duration, 0, len(acks)+1)
	prev := p.From
	for _, a := range acks {
		gaps = append(gaps, a.At.Sub(prev))
		prev = a.At
	}
	return append(gaps, p.To.Sub(prev))
}

// sampleFloor 는 표본 하한 = ⌊창 ÷ 간격⌋ − ⌈공백 상한 ÷ 간격⌉(설계 부록 A) — 빈 표본이 공백 판정을 통과하지 않게.
func sampleFloor(window, bound time.Duration) int {
	return int(window/WriteProbeInterval) - int((bound+WriteProbeInterval-1)/WriteProbeInterval)
}

// J10 — 창 안 ack ≥ 표본 하한 ∧ 가장 긴 공백 ≤ GapBound ∧ ReplicaGapBudget 을 넘는 공백 ≤ LongGaps.
func judgeWriteGap(_, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckWriteGap}
	p := after.Probe
	if p.From.IsZero() || !p.To.After(p.From) {
		check.Detail = "탐침 창 미관측"
		return check
	}
	acks := p.within()
	if len(acks) == 0 {
		check.Detail = "창 안 ack 0"
		return check
	}
	floor := sampleFloor(p.To.Sub(p.From), exp.GapBound)
	if len(acks) < floor {
		check.Detail = fmt.Sprintf("창 안 ack %d(하한 %d)", len(acks), floor)
		return check
	}

	// 가장 긴 공백 · ReplicaGapBudget 을 넘는 공백 수(primary 단계 하나만 길 수 있다) — 보정 공백으로 센다
	gaps := p.gaps(acks)
	credit := reduceGaps(gaps, acks, after.HostStalls)
	longest := slices.Max(gaps)
	if longest > exp.GapBound {
		check.Detail = fmt.Sprintf("가장 긴 쓰기 공백 %s(상한 %s)%s", longest.Round(time.Second), exp.GapBound, credit)
		return check
	}
	long := 0
	for _, g := range gaps {
		if g > ReplicaGapBudget {
			long++
		}
	}
	if long > exp.LongGaps {
		check.Detail = fmt.Sprintf("%s 넘는 공백 %d(허용 %d)%s", ReplicaGapBudget, long, exp.LongGaps, credit)
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("ack %d(하한 %d) · 가장 긴 공백 %s%s", len(acks), floor, longest.Round(time.Second), credit)
	return check
}

// reduceGaps 는 ack 사이 공백(gaps[1:n])에서 보정을 뺀다 — 창 가장자리 공백은 그대로.
// 판정 글에 붙일 보정 요약을 낸다(보정 0 이면 빈 글).
//
//	gaps  [From→a0] [a0→a1] … [a(n−2)→a(n−1)] [a(n−1)→To]
//	        그대로    보정                        그대로
func reduceGaps(gaps []time.Duration, acks []ProbeAck, stalls []StallSpan) string {
	var total time.Duration
	credited := 0
	for i := 1; i < len(acks); i++ {
		c := stallCredit(acks[i-1], acks[i], stalls)
		if c == 0 {
			continue
		}
		gaps[i] -= c
		total += c
		credited++
	}
	if credited == 0 {
		return ""
	}
	return fmt.Sprintf(" · 호스트 정지 보정 %s(공백 %d)", total.Round(creditShown), credited)
}

// stallCredit 는 ack prev → cur 공백에서 뺄 시간 = |[exec_prev, exec_cur] ∩ [due_cur, sent_cur] ∩ ∪stalls|.
// 탐침 맹점(sent − due > HostStallFloor)과 호스트 정지가 둘 다 있을 때만 0 이 아니다.
//
//	exec   prev ─────────────────────────── cur
//	맹점             due ──────────── sent
//	정지        [────────────────]
//	보정             [──────────]
func stallCredit(prev, cur ProbeAck, stalls []StallSpan) time.Duration {
	if cur.Due.IsZero() || cur.Sent.Sub(cur.Due) <= HostStallFloor {
		return 0
	}

	// 공백 ∩ 맹점
	from, to := laterOf(prev.At, cur.Due), earlierOf(cur.At, cur.Sent)
	if !to.After(from) {
		return 0
	}

	// ∩ 정지 합집합(겹친 기록을 두 번 세지 않게)
	var credit time.Duration
	for _, s := range StallsWithin(stalls, from, to) {
		credit += s.To.Sub(s.From)
	}
	return credit
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlierOf(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// H1 — 판정 창 [before, after] 안 호스트 정지 구간 수 · J10 보정에 쓴 수 · 보정 합 · 가장 긴 정지.
// 판정이 아니라 관측이다 — 정지가 없으면 관측도 없다(옛 판정문과 같은 꼴).
func observeHostStall(before, after ClusterSnap) (Observation, bool) {
	stalls := StallsWithin(after.HostStalls, before.TakenAt, after.TakenAt)
	if len(stalls) == 0 {
		return Observation{}, false
	}
	obs := Observation{ID: ObsHostStall, Count: len(stalls)}
	for _, s := range stalls {
		obs.Longest = max(obs.Longest, s.To.Sub(s.From))
	}

	// 보정은 J10 과 같은 창 · 같은 ack 로 — 정지마다 보정에 쓰였나
	acks := after.Probe.within()
	for _, s := range stalls {
		var used time.Duration
		for i := 1; i < len(acks); i++ {
			used += stallCredit(acks[i-1], acks[i], []StallSpan{s})
		}
		if used == 0 {
			continue
		}
		obs.Credited++
		obs.Credit += used
	}
	obs.Detail = fmt.Sprintf("호스트 정지 %d · 보정 %d곳 %s · 가장 긴 정지 %s",
		obs.Count, obs.Credited, obs.Credit.Round(creditShown), obs.Longest.Round(creditShown))
	return obs, true
}

// J15 — 창 안 ack 된 id 가 최종 primary 에 있다(소실 ≤ LossAllowed). ack 0 은 붉다.
func judgeLossless(_, after ClusterSnap, exp Expect) Check {
	check := Check{ID: CheckLossless}
	p := after.Probe
	acks := p.within()
	if len(acks) == 0 {
		check.Detail = "창 안 ack 0"
		return check
	}
	if p.Primary == "" {
		check.Detail = "최종 primary 에서 대조 못 함"
		return check
	}
	var lost []int64
	for _, a := range acks {
		if !p.stored(a.ID) {
			lost = append(lost, a.ID)
		}
	}
	if len(lost) > exp.LossAllowed {
		check.Detail = fmt.Sprintf("ack %d개 중 %d개가 %s 에 없음 %v(허용 %d)", len(acks), len(lost), p.Primary, lost[:min(len(lost), lostShown)], exp.LossAllowed)
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("ack %d개 중 소실 %d(허용 %d) · %s", len(acks), len(lost), exp.LossAllowed, p.Primary)
	return check
}

// ParseAcks 는 탐침 로그에서 ack 줄(3 · 5 필드)을 읽는다 — 다른 줄과 깨진 줄은 통째로 건너뛴다.
func ParseAcks(log string) []ProbeAck {
	var acks []ProbeAck
	for line := range strings.SplitSeq(log, "\n") {
		a, ok := parseAck(strings.Fields(line))
		if !ok {
			continue
		}
		acks = append(acks, a)
	}
	return acks
}

// parseAck 은 ack 줄 하나 — 필드 하나라도 못 읽으면 줄 전체를 버린다(due 만 남은 반쪽 ack 은 없다).
func parseAck(fields []string) (ProbeAck, bool) {
	if (len(fields) != ackFields && len(fields) != ackFieldsSent) || fields[0] != ackPrefix {
		return ProbeAck{}, false
	}
	id, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return ProbeAck{}, false
	}

	// exec · due · sent 순 epoch
	var times []time.Time
	for _, f := range fields[2:] {
		t, err := parseEpoch(f)
		if err != nil {
			return ProbeAck{}, false
		}
		times = append(times, t)
	}
	a := ProbeAck{ID: id, At: times[0]}
	if len(fields) == ackFieldsSent {
		a.Due, a.Sent = times[1], times[2]
	}
	return a, true
}

// parseEpoch 는 "초.소수" epoch 를 읽는다 — 부동소수 반올림 없이 나노초까지.
func parseEpoch(s string) (time.Time, error) {
	whole, frac, _ := strings.Cut(s, ".")
	sec, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	const nanoDigits = 9
	frac = (frac + strings.Repeat("0", nanoDigits))[:nanoDigits]
	nsec, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, nsec).UTC(), nil
}

// IDRanges 는 id 들을 닫힌 구간으로 접는다(판정문을 작게 — 탐침 id 는 거의 연속이다).
func IDRanges(ids []int64) []IDRange {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	var ranges []IDRange
	for _, id := range sorted {
		if n := len(ranges); n > 0 && ranges[n-1].To+1 == id {
			ranges[n-1].To = id
			continue
		}
		ranges = append(ranges, IDRange{From: id, To: id})
	}
	return ranges
}

// AckSpan 은 ack id 의 최소·최대다(최종 primary 에서 읽을 범위) — ack 이 없으면 ok=false.
func AckSpan(acks []ProbeAck) (lo, hi int64, ok bool) {
	if len(acks) == 0 {
		return 0, 0, false
	}
	lo = slices.MinFunc(acks, func(a, b ProbeAck) int { return cmp.Compare(a.ID, b.ID) }).ID
	hi = slices.MaxFunc(acks, func(a, b ProbeAck) int { return cmp.Compare(a.ID, b.ID) }).ID
	return lo, hi, true
}

// WarmedUp 은 끝에서부터 연속 n 개 ack 사이 공백이 모두 ReplicaGapBudget 이하인가 — 유발 전 탐침 준비(부록 A writeProbeWarmup).
func WarmedUp(acks []ProbeAck, n int) bool {
	if len(acks) < n {
		return false
	}
	tail := slices.Clone(acks)
	slices.SortFunc(tail, func(a, b ProbeAck) int { return a.At.Compare(b.At) })
	tail = tail[len(tail)-n:]
	for i := 1; i < len(tail); i++ {
		if tail[i].At.Sub(tail[i-1].At) > ReplicaGapBudget {
			return false
		}
	}
	return true
}
