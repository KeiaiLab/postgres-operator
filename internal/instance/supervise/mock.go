/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package supervise

import (
	"context"
	"errors"
	"sync"
)

// Mock 은 테스트용 Supervisor 구현. 호출 횟수 + error 주입 hook + 시뮬레이션
// ExitCh 를 노출한다. 본 mock 은 test-only 가 아닌 *export 된 타입* 으로,
// supervise 를 의존하는 다른 패키지 (cmd/instance, election integration test 등)
// 가 함께 사용한다.
type Mock struct {
	mu sync.Mutex

	StartCalls   int
	StopCalls    int
	ReloadCalls  int
	PromoteCalls int

	CreateSlotCalls map[string]int
	DropSlotCalls   map[string]int

	StartErr   error
	StopErr    error
	ReloadErr  error
	PromoteErr error
	SlotErr    error

	Ready bool
	pid   int

	started bool
	exitCh  chan error
}

// NewMock 은 빈 Mock 을 만든다. PID 기본값은 1 — Start 후에만 반환.
func NewMock() *Mock {
	return &Mock{
		CreateSlotCalls: map[string]int{},
		DropSlotCalls:   map[string]int{},
		exitCh:          make(chan error, 1),
		pid:             1,
	}
}

// Start 는 호출 횟수를 증가시키고 StartErr 가 set 되어 있으면 error.
// 두 번째 호출은 "already started" error.
func (m *Mock) Start(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.StartCalls++
	if m.StartErr != nil {
		return m.StartErr
	}
	if m.started {
		return errors.New("supervise: already started")
	}
	m.started = true
	return nil
}

// Stop 은 호출 횟수를 증가시키고 StopErr 가 set 되어 있으면 error.
// 미시작 상태에서 호출 시 error.
func (m *Mock) Stop(_ context.Context, _ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.StopCalls++
	if m.StopErr != nil {
		return m.StopErr
	}
	if !m.started {
		return errors.New("supervise: not started")
	}
	m.started = false
	select {
	case m.exitCh <- nil:
	default:
	}
	return nil
}

// Reload 는 호출 횟수만 증가. ReloadErr 가 set 되어 있으면 error.
func (m *Mock) Reload(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ReloadCalls++
	return m.ReloadErr
}

// Promote 는 호출 횟수만 증가. PromoteErr 가 set 되어 있으면 error.
func (m *Mock) Promote(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PromoteCalls++
	return m.PromoteErr
}

// CreateReplicationSlot 은 slot 별 호출 횟수를 누적. SlotErr 가 set 되어 있으면 error.
func (m *Mock) CreateReplicationSlot(_ context.Context, slotName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CreateSlotCalls[slotName]++
	return m.SlotErr
}

// DropReplicationSlot 은 slot 별 호출 횟수를 누적. SlotErr 가 set 되어 있으면 error.
func (m *Mock) DropReplicationSlot(_ context.Context, slotName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DropSlotCalls[slotName]++
	return m.SlotErr
}

// IsReady 는 Ready 필드를 그대로 반환.
func (m *Mock) IsReady(_ context.Context) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Ready
}

// ExitCh 는 시뮬레이션 채널을 반환.
func (m *Mock) ExitCh() <-chan error {
	return m.exitCh
}

// PID 는 Start 후에만 pid 필드 (default 1) 를 반환. 미시작이면 0.
func (m *Mock) PID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return 0
	}
	return m.pid
}

// SimulateExit 는 child unexpected exit 를 시뮬레이션 — 테스트가 cmd/instance 의
// ExitCh consumer 를 검증할 때 사용.
func (m *Mock) SimulateExit(err error) {
	select {
	case m.exitCh <- err:
	default:
	}
}

// Compile-time guard.
var _ Supervisor = (*Mock)(nil)
