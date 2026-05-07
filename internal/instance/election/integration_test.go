/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package election

import (
	"context"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const (
	identityPodA = "pod-a"
	identityPodB = "pod-b"
)

// 본 파일은 Pillar P2-M1 통과 게이트 — Real(client-go leaderelection 기반) 두
// 인스턴스가 동일 lease를 두고 경합할 때 정확히 한 쪽만 leader가 되며,
// leader가 종료되면 follower가 단일 LeaseDuration 안에 승계함을 envtest의
// 실제 K8s API server에 대해 검증한다.
//
// envtest 바이너리는 Makefile의 `test` 타겟이 KUBEBUILDER_ASSETS로 주입한다.
// 바이너리 부재 시 `make setup-envtest` 한 번 실행.

// integrationLease는 본 패키지 통합 테스트가 사용하는 매개변수.
// 짧은 LeaseDuration으로 회귀 시간을 단축한다.
var integrationLease = Durations{
	LeaseDuration: 2 * time.Second,
	RenewDeadline: 1 * time.Second,
	RetryPeriod:   200 * time.Millisecond,
}

// startEnvtestKubernetes는 envtest 환경을 부팅하고 in-cluster K8s clientset을
// 반환한다. cleanup은 t.Cleanup으로 등록된다.
func startEnvtestKubernetes(t *testing.T) *kubernetes.Clientset {
	t.Helper()
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Skipf("envtest unavailable (KUBEBUILDER_ASSETS 미설정 가능성): %v", err)
		return nil
	}
	t.Cleanup(func() {
		_ = env.Stop()
	})
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("NewForConfig: %v", err)
	}
	return cs
}

// runReal은 Real election을 새 goroutine에서 실행하고, leader/follower 전이
// 콜백을 채널로 노출한다. WaitGroup은 ctx 종료 후 Run goroutine 회수를 보장.
type harness struct {
	r        *Real
	leaderCh chan string   // OnNewLeader identity
	startCh  chan struct{} // OnStartedLeading
	stopCh   chan struct{} // OnStoppedLeading
	runErr   error
	wg       sync.WaitGroup
}

func newHarness(t *testing.T, cs *kubernetes.Clientset, leaseName, identity string) *harness {
	t.Helper()
	h := &harness{
		leaderCh: make(chan string, 16),
		startCh:  make(chan struct{}, 4),
		stopCh:   make(chan struct{}, 4),
	}
	r, err := NewReal(RealConfig{
		Client:    cs,
		LeaseName: leaseName,
		Namespace: "default",
		Identity:  identity,
		Durations: integrationLease,
		Callbacks: Callbacks{
			OnStartedLeading: func(_ context.Context) { h.startCh <- struct{}{} },
			OnStoppedLeading: func() { h.stopCh <- struct{}{} },
			OnNewLeader:      func(id string) { h.leaderCh <- id },
		},
	})
	if err != nil {
		t.Fatalf("NewReal(%s): %v", identity, err)
	}
	h.r = r
	return h
}

func (h *harness) start(ctx context.Context) {
	h.wg.Go(func() {
		h.runErr = h.r.Run(ctx)
	})
}

// waitLeaderIdentity는 leaderCh에서 deadline 안에 expected identity 알림이
// 도달하기를 기다린다. 다른 identity 알림은 무시(soft notification).
func (h *harness) waitLeaderIdentity(t *testing.T, expected string, deadline time.Duration) {
	t.Helper()
	timer := time.After(deadline)
	for {
		select {
		case id := <-h.leaderCh:
			if id == expected {
				return
			}
		case <-timer:
			t.Fatalf("identity=%s waited %s but never observed leader=%s", h.r.Identity(), deadline, expected)
		}
	}
}

// TestIntegration_TwoInstances_OneLeader는 두 Real instance가 같은 lease를
// 두고 경합할 때 정확히 한 쪽만 Leader가 됨을 검증한다(RFC 0003 §8 시나리오 A).
func TestIntegration_TwoInstances_OneLeader(t *testing.T) {
	cs := startEnvtestKubernetes(t)
	if cs == nil {
		return
	}

	const leaseName = "p2-int-twoinst-primary"

	a := newHarness(t, cs, leaseName, identityPodA)
	b := newHarness(t, cs, leaseName, identityPodB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a.start(ctx)
	b.start(ctx)

	// LeaseDuration(2s) + 약간의 여유. 둘 중 하나가 OnStartedLeading을 받아야.
	deadline := time.After(6 * time.Second)
	gotLeader := ""
	for gotLeader == "" {
		select {
		case <-a.startCh:
			gotLeader = identityPodA
		case <-b.startCh:
			gotLeader = identityPodB
		case <-deadline:
			t.Fatal("neither instance became leader within 6s")
		}
	}

	// 또 다른 한 쪽이 동시에 leader가 되어선 안 된다(상호배제).
	select {
	case <-a.startCh:
		if gotLeader == identityPodA {
			break // 같은 인스턴스 재호출은 K8s leaderelection이 막지만 안전망.
		}
		t.Fatal("both pod-a and pod-b became leader simultaneously")
	case <-b.startCh:
		if gotLeader == identityPodB {
			break
		}
		t.Fatal("both pod-a and pod-b became leader simultaneously")
	case <-time.After(2 * time.Second):
		// 정상 — 단 하나만 leader.
	}

	// Status 검증.
	switch gotLeader {
	case identityPodA:
		if a.r.Status() != StatusLeader {
			t.Errorf("pod-a Status = %v, want Leader", a.r.Status())
		}
	case identityPodB:
		if b.r.Status() != StatusLeader {
			t.Errorf("pod-b Status = %v, want Leader", b.r.Status())
		}
	}

	cancel()
	a.wg.Wait()
	b.wg.Wait()
}

// TestIntegration_LeaderHandover는 leader가 종료되면 follower가 LeaseDuration
// 만료 후 lease를 승계함을 검증한다(RFC 0003 §8 시나리오 B + C).
func TestIntegration_LeaderHandover(t *testing.T) {
	cs := startEnvtestKubernetes(t)
	if cs == nil {
		return
	}

	const leaseName = "p2-int-handover-primary"

	a := newHarness(t, cs, leaseName, identityPodA)
	b := newHarness(t, cs, leaseName, identityPodB)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// pod-a를 먼저 시작해 의도적으로 leader 우위를 부여.
	ctxA, cancelA := context.WithCancel(rootCtx)
	a.start(ctxA)

	select {
	case <-a.startCh:
	case <-time.After(6 * time.Second):
		t.Fatal("pod-a did not become leader within 6s")
	}

	// pod-b를 추가 시작. lease가 점유 중이므로 follower 유지.
	b.start(rootCtx)
	// pod-b가 lease holder 변화를 감지할 시간을 준다.
	b.waitLeaderIdentity(t, identityPodA, 4*time.Second)
	if b.r.Status() != StatusFollower {
		t.Errorf("pod-b Status before handover = %v, want Follower", b.r.Status())
	}

	// pod-a 종료(graceful) → lease duration 만료 후 pod-b 가 승계.
	cancelA()
	a.wg.Wait()

	// pod-b가 LeaseDuration(2s) + RetryPeriod 안에 leader가 돼야.
	select {
	case <-b.startCh:
	case <-time.After(8 * time.Second):
		t.Fatal("pod-b did not take over leadership within 8s")
	}
	if b.r.Status() != StatusLeader {
		t.Errorf("pod-b Status after handover = %v, want Leader", b.r.Status())
	}

	rootCancel()
	b.wg.Wait()
}
