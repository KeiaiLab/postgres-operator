/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package election은 instance manager의 K8s lease 기반 leader election을
// 추상화한다(ADR 0002 + RFC 0003).
//
// 본 패키지는 인터페이스 + 3개 구현(Real, Null, Mock) + lease 매개변수 상수
// 만 보유한다. 실 PG 프로세스 promote/demote는 P2-T3+T4 후속 작업이며, 본
// 패키지는 콜백 시그니처만 동결한다.
package election

import (
	"context"
	"sync/atomic"
	"time"
)

// Status는 instance manager의 election 상태다(RFC 0003 §5).
type Status string

const (
	// StatusStarting은 election 부트스트랩 중이다(아직 leader/follower 결정 전).
	StatusStarting Status = "Starting"
	// StatusLeader는 본 Pod가 lease holder다(PG primary로 promote 대상).
	StatusLeader Status = "Leader"
	// StatusFollower는 다른 Pod가 lease holder다.
	StatusFollower Status = "Follower"
)

// 본 패키지의 lease 매개변수 디폴트 상수(RFC 0003 §2). cmd/instance/main.go가
// CLI 플래그로 override 가능.
const (
	DefaultLeaseDuration = 15 * time.Second
	DefaultRenewDeadline = 10 * time.Second
	DefaultRetryPeriod   = 2 * time.Second
)

// Callbacks는 leadership 전이 시 호출된다. 모든 콜백은 election goroutine
// 컨텍스트에서 호출되며, 호출자는 빠르게 반환해야 한다(차단 동작은 별도
// goroutine으로 위임).
type Callbacks struct {
	// OnStartedLeading은 본 Pod가 lease를 획득해 leader가 된 직후 호출된다.
	OnStartedLeading func(ctx context.Context)
	// OnStoppedLeading은 본 Pod가 leader였다가 lease를 잃은 직후 호출된다.
	OnStoppedLeading func()
	// OnNewLeader는 임의의 leader 변경(본 Pod 포함) 시 호출된다.
	// identity가 본 Pod의 Identity()와 같으면 본 Pod가 leader임을 의미.
	OnNewLeader func(identity string)
}

// Election은 K8s lease 기반 leader election의 추상이다(RFC 0003 §5).
type Election interface {
	// Run은 ctx 종료 시까지 blocking으로 election 루프를 실행한다.
	// 호출자(cmd/instance/main)는 goroutine에서 실행해야 한다.
	Run(ctx context.Context) error

	// Status는 현재 상태를 동시성 안전하게 반환한다.
	Status() Status

	// Identity는 본 인스턴스의 lease identity(POD_NAME 등)다.
	Identity() string
}

// statusHolder는 atomic.Value로 Status를 갱신·조회하는 공통 헬퍼다.
// Real/Null/Mock 모두 본 헬퍼를 임베드해 동일 동시성 보장을 가진다.
type statusHolder struct {
	v atomic.Value // holds Status
}

func newStatusHolder(initial Status) *statusHolder {
	h := &statusHolder{}
	h.v.Store(initial)
	return h
}

// Status는 atomic 조회.
func (h *statusHolder) Status() Status {
	return h.v.Load().(Status)
}

// set은 atomic 갱신.
func (h *statusHolder) set(s Status) {
	h.v.Store(s)
}
