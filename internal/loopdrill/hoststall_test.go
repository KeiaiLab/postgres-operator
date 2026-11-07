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

// stallAt 은 drillStart 기준 초 단위 정지 구간.
func stallAt(from, to float64) StallSpan {
	return StallSpan{From: atSec(from), To: atSec(to)}
}

func atSec(s float64) time.Time {
	return drillStart.Add(time.Duration(s * float64(time.Second)))
}

func TestStallDetectorWallJump(t *testing.T) {
	var d StallDetector
	t0 := time.Now()
	if _, ok := d.Observe(t0); ok {
		t.Fatal("첫 표본에서 정지")
	}

	// 정상 표본 간격은 정지가 아니다.
	t1 := t0.Add(HostStallSample)
	if _, ok := d.Observe(t1); ok {
		t.Fatal("표본 간격에서 정지")
	}

	// 판 5 S6b 꼴 — 벽시계 14 s 점프(단조 판독 없음 = 벽시계만).
	t2 := t1.Round(0).Add(14 * time.Second)
	got, ok := d.Observe(t2)
	want := StallSpan{From: t1.Round(0), To: t2}
	if !ok || !got.From.Equal(want.From) || !got.To.Equal(want.To) {
		t.Fatalf("정지 %+v %v(기대 %+v)", got, ok, want)
	}
}

func TestStallDetectorAtFloor(t *testing.T) {
	var d StallDetector
	t0 := time.Now()
	d.Observe(t0)
	if s, ok := d.Observe(t0.Add(HostStallFloor)); ok {
		t.Fatalf("정확히 문턱인데 정지 %+v", s)
	}
}

func TestStallDetectorMonotonicJump(t *testing.T) {
	var d StallDetector
	t0 := time.Now().Round(0)
	d.Observe(t0)

	// 벽시계는 표본 간격만, 단조 시계는 5 s — 큰 쪽이 정지다.
	t1 := t0.Add(HostStallSample)
	got, ok := d.step(t1, HostStallSample, 5*time.Second)
	want := StallSpan{From: t0, To: t0.Add(5 * time.Second)}
	if !ok || got != want {
		t.Fatalf("정지 %+v %v(기대 %+v)", got, ok, want)
	}
}

func TestMergeStalls(t *testing.T) {
	in := []StallSpan{stallAt(20, 25), stallAt(1, 3), stallAt(2, 5), stallAt(1, 3), stallAt(5, 6), stallAt(30, 30)}
	got := MergeStalls(in)
	want := []StallSpan{stallAt(1, 6), stallAt(20, 25)}
	if !slices.Equal(got, want) {
		t.Fatalf("합집합 %v(기대 %v)", got, want)
	}
	if in[0] != stallAt(20, 25) {
		t.Fatal("입력을 바꿨다")
	}
	if MergeStalls(nil) != nil {
		t.Fatal("빈 입력에서 구간")
	}
}

func TestStallsWithin(t *testing.T) {
	in := []StallSpan{stallAt(0, 2), stallAt(8, 12), stallAt(14, 16), stallAt(18, 22), stallAt(30, 40)}
	got := StallsWithin(in, atSec(10), atSec(20))
	want := []StallSpan{stallAt(10, 12), stallAt(14, 16), stallAt(18, 20)}
	if !slices.Equal(got, want) {
		t.Fatalf("창 안 %v(기대 %v)", got, want)
	}
	if StallsWithin(in, atSec(3), atSec(7)) != nil {
		t.Fatal("창 밖 구간이 남았다")
	}
}

func TestParseStallsBroken(t *testing.T) {
	// 끊긴 덧붙이기 — 개행 없이 붙은 줄과 따로 선 반쪽 줄.
	for _, tail := range []string{"{\"from\":", "}", "\n{\"from\":\n"} {
		data := append(StallLine(stallAt(1, 3)), tail...)
		if _, err := ParseStalls(data); err == nil {
			t.Fatalf("깨진 줄 %q 을 받았다", tail)
		}
	}
	inverted := StallLine(StallSpan{From: atSec(5), To: atSec(1)})
	if _, err := ParseStalls(inverted); err == nil {
		t.Fatal("뒤집힌 구간을 받았다")
	}
}

func TestStallLineRoundTrip(t *testing.T) {
	spans := []StallSpan{stallAt(20, 25), stallAt(1, 3.25), stallAt(2, 5)}
	data := make([]byte, 0, 4096)
	for _, s := range spans {
		data = append(data, StallLine(s)...)
		data = append(data, '\n') // 빈 줄은 건너뛴다
	}
	got, err := ParseStalls(data)
	if err != nil {
		t.Fatal(err)
	}
	want := MergeStalls(spans)
	if !slices.Equal(got, want) {
		t.Fatalf("왕복 %v(기대 %v)", got, want)
	}
	if got, err := ParseStalls(nil); err != nil || got != nil {
		t.Fatalf("빈 파일 %v %v", got, err)
	}
}
