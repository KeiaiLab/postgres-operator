/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"errors"
	"net"
	"testing"
	"time"
)

func fakeConn() net.Conn { c, _ := net.Pipe(); return c }

// TestDialer_RetryThenSuccess 는 backoff 재시도가 실패를 흡수하고 성공하면 conn 을
// 반환함을 검증한다 (backoff=0 으로 sleep 없이).
func TestDialer_RetryThenSuccess(t *testing.T) {
	d := newBackendDialer(time.Second, 0, time.Second, 2, 3)
	calls := 0
	d.dial = func(_, _ string, _ time.Duration) (net.Conn, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("connection refused")
		}
		return fakeConn(), nil
	}
	conn, err := d.Dial("shard-a:5432")
	if err != nil || conn == nil {
		t.Fatalf("Dial = (%v,%v), want conn", conn, err)
	}
	_ = conn.Close()
	if calls != 3 {
		t.Fatalf("dial attempts = %d, want 3 (1 + 2 retries)", calls)
	}
}

// TestDialer_CircuitOpensAndCooldown 는 failThreshold 연속 실패 후 breaker 가 열려
// dial 을 호출하지 않고 빠르게 실패하며, cooldown 경과 후 다시 시도함을 검증한다.
func TestDialer_CircuitOpensAndCooldown(t *testing.T) {
	now := time.Now()
	d := newBackendDialer(time.Second, 0, 10*time.Second, 0, 2) // retries 0, threshold 2
	d.now = func() time.Time { return now }
	calls := 0
	d.dial = func(_, _ string, _ time.Duration) (net.Conn, error) {
		calls++
		return nil, errors.New("connection refused")
	}
	_, _ = d.Dial("a") // fail 1
	_, _ = d.Dial("a") // fail 2 → open
	before := calls

	_, err := d.Dial("a") // circuit open → fast fail, dial NOT called
	if !errors.Is(err, errCircuitOpen) {
		t.Fatalf("Dial(open) = %v, want errCircuitOpen", err)
	}
	if calls != before {
		t.Fatalf("dial called while circuit open: %d -> %d", before, calls)
	}

	now = now.Add(11 * time.Second) // past cooldown
	_, _ = d.Dial("a")
	if calls != before+1 {
		t.Fatalf("dial not retried after cooldown: %d -> %d", before, calls)
	}
}

// TestDialer_HalfOpenReopensOnFailure 는 cooldown 경과 후 단일 probe 만 나가고, 그
// probe 가 실패하면 즉시 재오픈되어 다음 Dial 이 다시 fast-fail 됨을 검증한다.
func TestDialer_HalfOpenReopensOnFailure(t *testing.T) {
	now := time.Now()
	d := newBackendDialer(time.Second, 0, 10*time.Second, 0, 2)
	d.now = func() time.Time { return now }
	calls := 0
	d.dial = func(_, _ string, _ time.Duration) (net.Conn, error) {
		calls++
		return nil, errors.New("refused")
	}
	_, _ = d.Dial("a")
	_, _ = d.Dial("a") // 2 fails → open
	now = now.Add(11 * time.Second)
	before := calls
	_, _ = d.Dial("a") // half-open 단일 probe (dial 1회), 실패 → 재오픈
	if calls != before+1 {
		t.Fatalf("half-open should probe exactly once: %d -> %d", before, calls)
	}
	c := calls
	_, err := d.Dial("a") // 재오픈 상태 → fast-fail, dial 호출 안 됨
	if !errors.Is(err, errCircuitOpen) {
		t.Fatalf("after failed probe should be open: %v", err)
	}
	if calls != c {
		t.Fatalf("dial called while reopened: %d -> %d", c, calls)
	}
}

// TestDialer_SuccessResetsBreaker 는 성공이 실패 카운트를 초기화함을 검증한다.
func TestDialer_SuccessResetsBreaker(t *testing.T) {
	now := time.Now()
	d := newBackendDialer(time.Second, 0, 10*time.Second, 0, 2)
	d.now = func() time.Time { return now }
	fail := true
	d.dial = func(_, _ string, _ time.Duration) (net.Conn, error) {
		if fail {
			return nil, errors.New("refused")
		}
		return fakeConn(), nil
	}
	_, _ = d.Dial("a") // fail 1
	fail = false
	c, err := d.Dial("a") // success → reset
	if err != nil {
		t.Fatalf("Dial success: %v", err)
	}
	_ = c.Close()
	fail = true
	_, _ = d.Dial("a") // fail 1 (reset 후) — threshold 2 미달
	if d.isOpen("a") {
		t.Fatal("breaker opened after reset + single failure")
	}
}
