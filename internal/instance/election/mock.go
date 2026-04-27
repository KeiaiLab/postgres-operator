/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package election

import "context"

// Mock은 단위 테스트용 Election 구현이다. 외부 호출(SetStatus)로 임의 전이
// 시뮬레이션 가능하며 Run은 ctx 종료까지 block 한다.
type Mock struct {
	*statusHolder
	identity string
	cb       Callbacks
}

// NewMock은 Starting 상태로 시작하는 Mock을 만든다.
func NewMock(identity string, cb Callbacks) *Mock {
	return &Mock{
		statusHolder: newStatusHolder(StatusStarting),
		identity:     identity,
		cb:           cb,
	}
}

// Identity는 identity를 반환한다.
func (m *Mock) Identity() string { return m.identity }

// Run은 ctx 종료까지 block 한다. Status 전이는 SetStatus를 통해 시험자가 직접
// 트리거한다.
func (m *Mock) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// SetStatus는 시험자가 임의 상태로 전이시킨다. 이전 → 다음에 따라 적절한
// 콜백을 호출한다.
func (m *Mock) SetStatus(ctx context.Context, next Status) {
	prev := m.Status()
	m.set(next)

	switch {
	case prev != StatusLeader && next == StatusLeader:
		if m.cb.OnStartedLeading != nil {
			m.cb.OnStartedLeading(ctx)
		}
		if m.cb.OnNewLeader != nil {
			m.cb.OnNewLeader(m.identity)
		}
	case prev == StatusLeader && next != StatusLeader:
		if m.cb.OnStoppedLeading != nil {
			m.cb.OnStoppedLeading()
		}
	}
}

// SetExternalLeader는 다른 Pod가 leader가 됐음을 시뮬레이트한다.
// 본 Pod는 follower로 전이.
func (m *Mock) SetExternalLeader(otherIdentity string) {
	if m.Status() == StatusLeader && m.cb.OnStoppedLeading != nil {
		m.cb.OnStoppedLeading()
	}
	m.set(StatusFollower)
	if m.cb.OnNewLeader != nil {
		m.cb.OnNewLeader(otherIdentity)
	}
}

// Compile-time guard.
var _ Election = (*Mock)(nil)
