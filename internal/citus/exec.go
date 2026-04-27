/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package citus

import (
	"context"
	"sync"
)

// SQLExecutor는 ComputeActions가 만든 Action 리스트를 실 PG에 적용한다
// (RFC 0002 §6). 본 인터페이스의 변경은 RFC 갱신 필수.
type SQLExecutor interface {
	// Apply는 actions를 입력된 순서대로 적용한다.
	// 부분 실패 시 첫 실패 시점에 error 반환 — 호출자(reconciler)는 다음 reconcile
	// 매 회 재계산하여 잔여 Action을 자동 적용한다(멱등성).
	Apply(ctx context.Context, actions []Action) error
}

// NullExecutor는 Action을 적용하지 않는다(M0 spike 기본값).
//
// 본 구현은 P11-M0(현재) 시점에 reconciler에 주입되어 desired state 계산과
// Status 반영만 활성화한다. 실 SQL 실행은 P11-M1의 LibPQExecutor가 담당한다.
//
// envtest 통합 테스트에도 본 구현을 사용한다 — envtest는 PG 컨테이너를 띄우지
// 않으므로 SQL 실행은 단위 테스트 + 향후 kind 기반 e2e의 책임이다.
type NullExecutor struct{}

// Apply는 no-op이다.
func (NullExecutor) Apply(_ context.Context, _ []Action) error {
	return nil
}

// MockExecutor는 호출된 Action을 모두 기록한다(단위 테스트 보조).
type MockExecutor struct {
	mu      sync.Mutex
	Applied [][]Action
	// Err가 nil 아니면 Apply가 즉시 그 error를 반환 (실패 경로 테스트용).
	Err error
}

// Apply는 actions를 기록 후 (또는 Err 설정 시) 반환한다.
func (m *MockExecutor) Apply(_ context.Context, actions []Action) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	// 입력 슬라이스 변경 회피 위해 복사.
	cp := make([]Action, len(actions))
	copy(cp, actions)
	m.Applied = append(m.Applied, cp)
	return nil
}

// Calls는 누적 호출 횟수다.
func (m *MockExecutor) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Applied)
}

// Compile-time interface satisfaction guards. 본 어셔션이 시그니처 동결을 보장.
var (
	_ SQLExecutor = (*NullExecutor)(nil)
	_ SQLExecutor = (*MockExecutor)(nil)
)
