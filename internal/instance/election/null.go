/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package election

import "context"

// Null은 항상 Leader인 election 구현이다(RFC 0003 §6 Null).
//
// 용도:
//   - 단일 노드 development 모드 (members=1)에서 election 우회
//   - 테스트 환경에서 K8s API 의존 제거
//   - cmd/instance/main.go의 --election=disabled 플래그 시 사용
//
// Run은 OnStartedLeading 콜백을 호출하고 ctx 종료 시까지 block 한다.
type Null struct {
	*statusHolder
	identity string
	cb       Callbacks
}

// NewNull은 Null election을 만든다. identity는 보통 POD_NAME.
func NewNull(identity string, cb Callbacks) *Null {
	return &Null{
		statusHolder: newStatusHolder(StatusLeader),
		identity:     identity,
		cb:           cb,
	}
}

// Identity는 identity를 반환한다.
func (n *Null) Identity() string { return n.identity }

// Run은 즉시 leader로 전이하고 ctx 종료까지 block 한다.
func (n *Null) Run(ctx context.Context) error {
	if n.cb.OnStartedLeading != nil {
		n.cb.OnStartedLeading(ctx)
	}
	if n.cb.OnNewLeader != nil {
		n.cb.OnNewLeader(n.identity)
	}
	<-ctx.Done()
	if n.cb.OnStoppedLeading != nil {
		n.cb.OnStoppedLeading()
	}
	return ctx.Err()
}

// Compile-time guard.
var _ Election = (*Null)(nil)
