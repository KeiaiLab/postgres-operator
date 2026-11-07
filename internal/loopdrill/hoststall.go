/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

// 호스트 정지 시계(ADR-0039 계측기 B) — 드라이버가 HostStallSample 마다 시각을 찍고,
// 이웃 표본 차가 HostStallFloor 를 넘으면 정지 구간을 host-stalls.jsonl 에 덧붙인다.
//
//	표본  t0 ── t1 ─────────── 14 s ─────────── t2 ── t3
//	정지                 [t1 벽시계, t1 + 14 s]

// StallSpan 은 호스트가 멈춘 벽시계 구간.
type StallSpan struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// StallDetector 는 이웃 표본 차로 정지를 찾는다. 영값이 바로 쓰인다.
type StallDetector struct {
	prev   time.Time
	primed bool
}

// Observe 는 표본 now 를 받아 앞 표본과의 사이가 정지면 그 구간을 낸다. 첫 표본은 정지가 아니다.
func (d *StallDetector) Observe(now time.Time) (StallSpan, bool) {
	wall := now.Round(0).Sub(d.prev.Round(0))
	mono := now.Sub(d.prev) // 둘 다 단조 판독이 있으면 단조 차, 아니면 벽시계 차
	return d.step(now, wall, mono)
}

// step 은 벽시계 차·단조 차 중 큰 것이 문턱을 넘으면 정지로 본다.
// VM 정지는 벽시계만(재동기화) 또는 단조만(벽시계 되감기) 뛸 수 있다.
func (d *StallDetector) step(now time.Time, wall, mono time.Duration) (StallSpan, bool) {
	prev, primed := d.prev.Round(0), d.primed
	d.prev, d.primed = now, true
	if !primed {
		return StallSpan{}, false
	}

	gap := max(wall, mono)
	if gap <= HostStallFloor {
		return StallSpan{}, false
	}
	return StallSpan{From: prev, To: prev.Add(gap)}, true
}

// MergeStalls 는 구간을 정렬해 겹치거나 맞닿은 것을 합친다. 빈 구간은 버린다. 입력은 바꾸지 않는다.
// 덧붙이기 전용 파일의 재기동·중복 기록이 같은 결과가 되게 한다.
func MergeStalls(spans []StallSpan) []StallSpan {
	sorted := slices.Clone(spans)
	slices.SortFunc(sorted, func(a, b StallSpan) int { return cmp.Or(a.From.Compare(b.From), a.To.Compare(b.To)) })

	var out []StallSpan
	for _, s := range sorted {
		if !s.To.After(s.From) {
			continue
		}

		// 앞 구간과 겹치면 늘린다.
		if n := len(out); n > 0 && !s.From.After(out[n-1].To) {
			if s.To.After(out[n-1].To) {
				out[n-1].To = s.To
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// StallsWithin 은 [from, to] 창으로 자른 합집합이다. 창 밖 구간은 버린다.
func StallsWithin(spans []StallSpan, from, to time.Time) []StallSpan {
	var out []StallSpan
	for _, s := range MergeStalls(spans) {
		if s.From.Before(from) {
			s.From = from
		}
		if s.To.After(to) {
			s.To = to
		}
		if !s.To.After(s.From) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// StallLine 은 host-stalls.jsonl 의 한 줄(개행 없음).
func StallLine(s StallSpan) []byte {
	line, err := json.Marshal(StallSpan{From: s.From.Round(0).UTC(), To: s.To.Round(0).UTC()})
	if err != nil {
		panic(err) // time.Time 두 칸은 실패하지 않는다(연도 0–9999 밖만)
	}
	return line
}

// ParseStalls 는 host-stalls.jsonl 을 읽어 합집합을 낸다. 빈 줄은 건너뛴다.
// 깨진 줄·뒤집힌 구간은 오류다 — 호출자가 보정 0 으로 닫는다.
func ParseStalls(data []byte) ([]StallSpan, error) {
	var spans []StallSpan
	for i, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var s StallSpan
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("host stall line %d: %w", i+1, err)
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("host stall line %d: trailing data", i+1)
		}
		if s.From.IsZero() || s.To.Before(s.From) {
			return nil, fmt.Errorf("host stall line %d: bad span %s–%s", i+1, s.From, s.To)
		}
		spans = append(spans, StallSpan{From: s.From.UTC(), To: s.To.UTC()})
	}
	return MergeStalls(spans), nil
}
