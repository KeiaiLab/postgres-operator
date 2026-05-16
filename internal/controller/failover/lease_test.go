/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package failover

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/keiailab/postgres-operator/internal/instance/election"
)

// 본 파일은 D.2.2 단위 게이트 — failover.Lease 두 인스턴스가 동일 lease 를 두고
// 경합할 때 정확히 한 쪽만 leader 가 되고, leader cancel 시 다른 쪽이 승계함을
// fake clientset 으로 검증한다.
//
// e2e multi-replica failover (실 K8s cluster 에서 manager 두 instance) 검증은
// cluster mesh 복원 후 별 turn (envtest 또는 라이브 chaos drill 로 promote).

// shortDurations 는 단위 테스트 회귀 시간 단축용. client-go leaderelection 의
// RenewDeadline < LeaseDuration / RetryPeriod < RenewDeadline 제약 준수.
var shortDurations = election.Durations{
	LeaseDuration: 1500 * time.Millisecond,
	RenewDeadline: 1000 * time.Millisecond,
	RetryPeriod:   200 * time.Millisecond,
}

// TestLeaseElection 은 fake clientset 으로 두 contender 간 leader 단일성 +
// handoff 를 검증한다.
func TestLeaseElection(t *testing.T) {
	t.Parallel()

	const (
		ns       = "postgres-operator-system"
		podA     = "operator-pod-a"
		podB     = "operator-pod-b"
		testName = "test-failover-lease"
	)

	client := fake.NewSimpleClientset()

	var (
		mu        sync.Mutex
		leaderLog []string // identity 순서대로 OnStartedLeading 기록
		startedA  atomic.Bool
		startedB  atomic.Bool
		stoppedA  atomic.Bool
	)

	newCfg := func(identity string, onStart func(context.Context), onStop func()) LeaseConfig {
		return LeaseConfig{
			Client:    client,
			Namespace: ns,
			Identity:  identity,
			LeaseName: testName,
			Durations: shortDurations,
			OnStartedLeading: func(ctx context.Context) {
				mu.Lock()
				leaderLog = append(leaderLog, identity)
				mu.Unlock()
				if onStart != nil {
					onStart(ctx)
				}
			},
			OnStoppedLeading: onStop,
		}
	}

	// contender A 시작
	ctxA, cancelA := context.WithCancel(context.Background())
	leaseA, err := NewLease(newCfg(podA,
		func(ctx context.Context) { startedA.Store(true) },
		func() { stoppedA.Store(true) },
	))
	if err != nil {
		t.Fatalf("NewLease(A): %v", err)
	}

	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		_ = leaseA.Run(ctxA)
	}()

	// A 가 leader 가 될 시간 부여 (LeaseDuration 1.5s + 여유)
	if !waitFor(3*time.Second, func() bool { return startedA.Load() }) {
		cancelA()
		<-doneA
		t.Fatalf("A 가 LeaseDuration 안에 leader 가 되지 못함")
	}

	// 이 시점: A 만 leader. B 시작 → 단일성 검증 (B 는 follower 로 대기).
	ctxB, cancelB := context.WithCancel(context.Background())
	leaseB, err := NewLease(newCfg(podB,
		func(ctx context.Context) { startedB.Store(true) },
		nil,
	))
	if err != nil {
		cancelA()
		<-doneA
		t.Fatalf("NewLease(B): %v", err)
	}

	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		_ = leaseB.Run(ctxB)
	}()

	// B 가 leader 가 *되면 안 됨* — A 가 active 인 동안.
	// LeaseDuration * 2 동안 startedB 가 false 유지를 검증.
	deadline := time.Now().Add(2 * shortDurations.LeaseDuration)
	for time.Now().Before(deadline) {
		if startedB.Load() {
			cancelA()
			cancelB()
			<-doneA
			<-doneB
			t.Fatalf("단일성 위반: A 가 leader 인 동안 B 가 leader 가 됨")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !leaseA.IsLeader() {
		t.Errorf("A.IsLeader() == false (기대: true)")
	}
	if leaseB.IsLeader() {
		t.Errorf("B.IsLeader() == true (기대: false)")
	}

	// A cancel → B 가 lease 를 승계해야 함.
	cancelA()
	<-doneA

	// LeaseDuration 만큼 대기 후 B 승계 검증 (lease expiry + B acquire).
	if !waitFor(5*time.Second, func() bool { return startedB.Load() }) {
		cancelB()
		<-doneB
		t.Fatalf("A cancel 후 B 가 lease 를 승계하지 못함")
	}

	// 정리.
	cancelB()
	<-doneB

	mu.Lock()
	defer mu.Unlock()
	if len(leaderLog) < 2 {
		t.Fatalf("OnStartedLeading 호출 횟수 = %d (기대: >=2, log=%v)", len(leaderLog), leaderLog)
	}
	if leaderLog[0] != podA {
		t.Errorf("첫 leader = %q (기대: %q)", leaderLog[0], podA)
	}
	if leaderLog[1] != podB {
		t.Errorf("두 번째 leader = %q (기대: %q)", leaderLog[1], podB)
	}
	if !stoppedA.Load() {
		t.Errorf("A.OnStoppedLeading 미호출 (cancel 후 호출 기대)")
	}
}

// TestNewLeaseValidation 은 LeaseConfig 검증 분기를 cover.
func TestNewLeaseValidation(t *testing.T) {
	t.Parallel()
	client := fake.NewSimpleClientset()
	cases := []struct {
		name string
		cfg  LeaseConfig
	}{
		{"nil client", LeaseConfig{Namespace: "ns", Identity: "id"}},
		{"empty ns", LeaseConfig{Client: client, Identity: "id"}},
		{"empty identity", LeaseConfig{Client: client, Namespace: "ns"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewLease(tc.cfg); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}

	// LeaseName 미지정 시 FailoverLeaseName 기본값 적용 (성공 케이스).
	l, err := NewLease(LeaseConfig{
		Client:    client,
		Namespace: "ns",
		Identity:  "id",
		Durations: shortDurations,
	})
	if err != nil {
		t.Fatalf("default LeaseName 케이스에서 err = %v (기대: nil)", err)
	}
	if l.Identity() != "id" {
		t.Errorf("Identity() = %q (기대: id)", l.Identity())
	}
}

// waitFor 는 cond 가 true 가 될 때까지 또는 timeout 까지 50ms 간격으로 polling.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}
