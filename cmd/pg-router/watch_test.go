/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/watch"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestWatchReloader_ForwardsEventsAndReconnects(t *testing.T) {
	ctx := t.Context()

	fw1 := watch.NewFakeWithChanSize(4, false)
	fw2 := watch.NewFakeWithChanSize(4, false)
	fakes := make(chan *watch.FakeWatcher, 2)
	fakes <- fw1
	fakes <- fw2

	var connCount atomic.Int32
	connect := func(ctx context.Context) (watch.Interface, error) {
		connCount.Add(1)
		select {
		case f := <-fakes:
			return f, nil
		default:
			<-ctx.Done() // 두 fake 소진 후엔 재접속 불필요 — ctx 대기.
			return nil, ctx.Err()
		}
	}

	notify := make(chan struct{}, 8)
	r := watchReloader{connect: connect, backoff: 5 * time.Millisecond, name: "test"}
	go r.run(ctx, notify)

	// 1) 첫 세션(fw1) 이벤트 → notify.
	fw1.Add(&v1alpha1.ShardRange{})
	select {
	case <-notify:
	case <-time.After(2 * time.Second):
		t.Fatal("이벤트 후 notify 없음")
	}

	// 2) fw1 닫힘 → 재접속(fw2). backoff 후 fw2 로 전환될 시간을 준 뒤 이벤트.
	fw1.Stop()
	time.Sleep(60 * time.Millisecond)
	fw2.Add(&v1alpha1.ShardRange{})
	select {
	case <-notify:
	case <-time.After(2 * time.Second):
		t.Fatal("재접속 후 notify 없음")
	}
	if connCount.Load() < 2 {
		t.Fatalf("재접속 기대(connect >= 2), got %d", connCount.Load())
	}
}

func TestWatchReloader_CtxCancelExits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fw := watch.NewFakeWithChanSize(1, false)
	connect := func(context.Context) (watch.Interface, error) { return fw, nil }

	done := make(chan struct{})
	r := watchReloader{connect: connect, backoff: 5 * time.Millisecond, name: "test"}
	go func() { r.run(ctx, make(chan struct{}, 1)); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 취소 후 run 이 종료되지 않음")
	}
}

func TestWatchReloader_ReconnectsOnConnectError(t *testing.T) {
	ctx := t.Context()

	var connCount atomic.Int32
	fw := watch.NewFakeWithChanSize(4, false)
	connect := func(context.Context) (watch.Interface, error) {
		n := connCount.Add(1)
		if n == 1 {
			return nil, context.DeadlineExceeded // 첫 접속 실패 → backoff 후 재시도.
		}
		return fw, nil
	}

	notify := make(chan struct{}, 4)
	r := watchReloader{connect: connect, backoff: 5 * time.Millisecond, name: "test"}
	go r.run(ctx, notify)

	time.Sleep(60 * time.Millisecond)
	fw.Add(&v1alpha1.ShardRange{})
	select {
	case <-notify:
	case <-time.After(2 * time.Second):
		t.Fatal("connect 실패 재시도 후 notify 없음")
	}
	if connCount.Load() < 2 {
		t.Fatalf("connect 실패 후 재시도 기대(>= 2), got %d", connCount.Load())
	}
}

func TestCoalesce_AbsorbsBurst(t *testing.T) {
	ch := make(chan struct{}, 10)
	for range 5 {
		ch <- struct{}{}
	}
	start := time.Now()
	coalesce(context.Background(), ch, 30*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("coalesce 가 debounce 창 전에 반환: %s", elapsed)
	}
	if len(ch) != 0 {
		t.Fatalf("coalesce 후 신호 %d 잔존, want 0", len(ch))
	}
}

func TestCoalesce_CtxCancelReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// ctx 취소 상태면 즉시 반환(무한 대기 없음).
	done := make(chan struct{})
	go func() { coalesce(ctx, make(chan struct{}), time.Hour); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coalesce 가 ctx 취소로 반환하지 않음")
	}
}
