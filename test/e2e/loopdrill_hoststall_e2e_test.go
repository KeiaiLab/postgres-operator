//go:build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keiailab/postgres-operator/internal/loopdrill"
)

// 드라이버 정지 시계(ADR-0039 계측기 B) — kind 밖 호스트 프로세스가 HostStallSample 마다 시각을 찍고
// 정지 구간을 상태 디렉터리 host-stalls.jsonl 에 덧붙인다. 판정 창 끝에 그 창으로 잘라 after.hostStalls 에 싣는다.
//
//	ticker ─ Observe ─ StallLine ─▶ host-stalls.jsonl ─ ParseStalls ─ StallsWithin(before, after) ─▶ snap.HostStalls

// hostStallsFile 은 상태 디렉터리 안 정지 기록(덧붙이기 전용 JSONL).
const hostStallsFile = "host-stalls.jsonl"

// stallClock 은 정지 시계 goroutine 하나.
type stallClock struct {
	stop chan struct{}
	done chan struct{}
}

// stallsPath 는 정지 기록 경로다.
func (d *drill) stallsPath() string {
	return filepath.Join(d.cfg.stateDir, hostStallsFile)
}

// startStallClock 은 정지 시계를 띄우고 테스트 정리 때 멈춘다.
// 새 훈련장은 기록을 비우고, KEEP=1 재개는 있던 기록을 이어 쓴다(읽을 때 합집합이라 중복은 무해).
func (d *drill) startStallClock() error {
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if !d.cfg.keep {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(d.stallsPath(), flags, 0o644)
	if err != nil {
		return err
	}
	if d.cfg.keep {
		d.t.Logf("호스트 정지 기록 이어 씀: %d 구간", len(d.readStalls()))
	}

	c := &stallClock{stop: make(chan struct{}), done: make(chan struct{})}
	go c.loop(d.t, f)
	d.t.Cleanup(func() {
		close(c.stop)
		<-c.done
	})
	return nil
}

// loop 는 표본마다 정지를 찾아 한 줄씩 덧붙인다. 쓰기 실패는 기록하고 계속한다(그 구간은 보정 0).
func (c *stallClock) loop(t testing.TB, f *os.File) {
	defer close(c.done)
	defer func() { _ = f.Close() }()

	tick := time.NewTicker(loopdrill.HostStallSample)
	defer tick.Stop()

	var det loopdrill.StallDetector
	det.Observe(time.Now())
	for {
		select {
		case <-c.stop:
			return
		case now := <-tick.C:
			span, stalled := det.Observe(now)
			if !stalled {
				continue
			}
			if _, err := f.Write(append(loopdrill.StallLine(span), '\n')); err != nil {
				t.Logf("호스트 정지 기록 쓰기: %v", err)
			}
		}
	}
}

// readStalls 는 정지 기록 전부다. 못 읽으면 기록하고 정지 없음으로 닫는다(보정 0, fail-closed).
func (d *drill) readStalls() []loopdrill.StallSpan {
	data, err := os.ReadFile(d.stallsPath())
	if err != nil {
		d.t.Logf("호스트 정지 기록 읽기: %v — 보정 0", err)
		return nil
	}
	spans, err := loopdrill.ParseStalls(data)
	if err != nil {
		d.t.Logf("호스트 정지 기록 해석: %v — 보정 0", err)
		return nil
	}
	return spans
}

// hostStalls 는 [from, to] 판정 창 안 정지 구간이다.
func (d *drill) hostStalls(from, to time.Time) []loopdrill.StallSpan {
	return loopdrill.StallsWithin(d.readStalls(), from, to)
}
