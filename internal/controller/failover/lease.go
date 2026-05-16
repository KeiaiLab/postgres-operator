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
	"fmt"

	"k8s.io/client-go/kubernetes"

	"github.com/keiailab/postgres-operator/internal/instance/election"
)

// HA election 분산락 (ROADMAP G1 §자동 failover).
//
// 책임: 다중 replica 로 배포된 operator manager 중 *오직 한 인스턴스* 만
// failover decision/promotion 을 실 수행하도록 K8s coordination.k8s.io/v1
// Lease 기반 leader-election 을 적용한다. 일반 controller-runtime 의 manager
// leader-election 과 별도 lease — failover-only 책임을 분리하여
// reconciler/finalizer 비차단 + failover hot-path 격리.
//
// 본 파일은 의도적으로 *thin adapter* — 실 client-go leaderelection wrapper
// 는 `internal/instance/election.Real` 가 단일 출처 (§2 Simplicity). 본
// adapter 는 failover scope 의 lease 명명 규약 + 기본 매개변수 + 콜백
// 시그니처 만 노출한다.
//
// 사용처: cmd/main.go 에서 manager 시작 후 별 goroutine 에서 Run.
//
//	cfg := failover.LeaseConfig{
//	    Client:    clientset,
//	    Namespace: operatorNS,
//	    Identity:  podName,
//	    OnStartedLeading: func(ctx context.Context) { failoverCtrl.Enable() },
//	    OnStoppedLeading: func() { failoverCtrl.Disable() },
//	}
//	lease, err := failover.NewLease(cfg)
//	go lease.Run(ctx)

// FailoverLeaseName 은 operator 단위 failover-controller lease 의 표준 명칭이다.
// instance-단위 lease (election.PrimaryLeaseName) 와 *별도 lease key* — 이름
// 충돌 방지 + 책임 분리.
const FailoverLeaseName = "postgres-operator-failover-leader"

// LeaseConfig 는 NewLease 의 입력이다. 0 값 Durations 는 election.Default* 로
// 대체된다 (15s / 10s / 2s, RFC 0003 §2 표준).
type LeaseConfig struct {
	// Client 는 K8s clientset. coordination.k8s.io/v1 Lease 권한 필요.
	Client kubernetes.Interface
	// Namespace 는 lease 가 거주할 ns (보통 operator 가 배포된 ns).
	Namespace string
	// Identity 는 본 manager Pod 의 고유 identifier (POD_NAME 권장).
	Identity string
	// LeaseName 이 비어 있으면 FailoverLeaseName 사용.
	LeaseName string
	// OnStartedLeading 은 본 Pod 가 leader 가 된 직후 호출.
	OnStartedLeading func(ctx context.Context)
	// OnStoppedLeading 은 본 Pod 가 leader 였다가 lease 를 잃은 직후 호출.
	OnStoppedLeading func()
	// OnNewLeader 는 임의의 leader 변경 시 호출. identity == cfg.Identity 면
	// 본 Pod 가 leader.
	OnNewLeader func(identity string)
	// Durations 가 zero 값이면 election.Default* 사용.
	Durations election.Durations
}

// Lease 는 failover-controller scope 의 HA election 핸들이다.
type Lease struct {
	inner *election.Real
}

// NewLease 는 LeaseConfig 를 검증하고 election.Real adapter 를 구성한다.
//
// 검증 실패 시 nil + error 반환 — 호출자(cmd/main) 는 이를 기동 실패로 처리.
func NewLease(cfg LeaseConfig) (*Lease, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("failover: Lease.Client must not be nil")
	}
	if cfg.Namespace == "" {
		return nil, fmt.Errorf("failover: Lease.Namespace must not be empty")
	}
	if cfg.Identity == "" {
		return nil, fmt.Errorf("failover: Lease.Identity must not be empty")
	}
	name := cfg.LeaseName
	if name == "" {
		name = FailoverLeaseName
	}
	r, err := election.NewReal(election.RealConfig{
		Client:    cfg.Client,
		LeaseName: name,
		Namespace: cfg.Namespace,
		Identity:  cfg.Identity,
		Callbacks: election.Callbacks{
			OnStartedLeading: cfg.OnStartedLeading,
			OnStoppedLeading: cfg.OnStoppedLeading,
			OnNewLeader:      cfg.OnNewLeader,
		},
		Durations: cfg.Durations,
	})
	if err != nil {
		return nil, fmt.Errorf("failover: NewLease: %w", err)
	}
	return &Lease{inner: r}, nil
}

// Run 은 ctx 종료 시까지 blocking 으로 election 루프를 실행한다.
// 호출자는 별도 goroutine 에서 호출해야 한다.
func (l *Lease) Run(ctx context.Context) error {
	return l.inner.Run(ctx)
}

// Identity 는 본 Pod 의 lease identity 를 반환한다.
func (l *Lease) Identity() string {
	return l.inner.Identity()
}

// IsLeader 는 현재 본 Pod 가 leader 인지 동시성 안전하게 반환한다.
func (l *Lease) IsLeader() bool {
	return l.inner.Status() == election.StatusLeader
}
