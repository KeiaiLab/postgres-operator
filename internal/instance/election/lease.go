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
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
)

// Real은 client-go의 leaderelection 패키지를 사용하는 production 구현이다
// (RFC 0003 §6 Real).
//
// 인스턴스 부트스트랩(cmd/instance/main):
//
//	cfg := election.RealConfig{
//	    Client:    clientset,
//	    LeaseName: election.PrimaryLeaseName(cluster, role, shardOrdinal),
//	    Namespace: namespace,
//	    Identity:  podName,
//	    Callbacks: election.Callbacks{...},
//	}
//	r, err := election.NewReal(cfg)
//	go r.Run(ctx)
type Real struct {
	*statusHolder
	identity  string
	leaseName string
	namespace string
	client    kubernetes.Interface
	cb        Callbacks
	durations Durations
}

// Durations는 lease 매개변수 묶음이다. nil 또는 0 값 필드는 디폴트로 대체된다.
type Durations struct {
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
}

// withDefaults는 0 값 필드를 디폴트로 채운다.
func (d Durations) withDefaults() Durations {
	if d.LeaseDuration == 0 {
		d.LeaseDuration = DefaultLeaseDuration
	}
	if d.RenewDeadline == 0 {
		d.RenewDeadline = DefaultRenewDeadline
	}
	if d.RetryPeriod == 0 {
		d.RetryPeriod = DefaultRetryPeriod
	}
	return d
}

// Validate는 RenewDeadline < LeaseDuration 같은 sanity 조건을 검사한다.
// client-go도 동일 검사를 수행하나, 본 함수는 cmd/instance가 시작 전 실패하도록
// 명시적 분기를 제공한다.
func (d Durations) Validate() error {
	if d.RenewDeadline >= d.LeaseDuration {
		return fmt.Errorf("election: RenewDeadline(%s) must be < LeaseDuration(%s)", d.RenewDeadline, d.LeaseDuration)
	}
	if d.RetryPeriod >= d.RenewDeadline {
		return fmt.Errorf("election: RetryPeriod(%s) should be < RenewDeadline(%s)", d.RetryPeriod, d.RenewDeadline)
	}
	return nil
}

// RealConfig는 NewReal의 입력이다.
type RealConfig struct {
	Client    kubernetes.Interface
	LeaseName string
	Namespace string
	Identity  string
	Callbacks Callbacks
	Durations Durations // 비어 있으면 디폴트
}

// NewReal은 RealConfig 검증 + Real 인스턴스 생성. Run은 호출자가 별도로.
func NewReal(cfg RealConfig) (*Real, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("election: Client must not be nil")
	}
	if cfg.LeaseName == "" {
		return nil, fmt.Errorf("election: LeaseName must not be empty")
	}
	if cfg.Namespace == "" {
		return nil, fmt.Errorf("election: Namespace must not be empty")
	}
	if cfg.Identity == "" {
		return nil, fmt.Errorf("election: Identity must not be empty")
	}
	d := cfg.Durations.withDefaults()
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &Real{
		statusHolder: newStatusHolder(StatusStarting),
		identity:     cfg.Identity,
		leaseName:    cfg.LeaseName,
		namespace:    cfg.Namespace,
		client:       cfg.Client,
		cb:           cfg.Callbacks,
		durations:    d,
	}, nil
}

// Identity는 본 Pod의 lease identity를 반환한다.
func (r *Real) Identity() string { return r.identity }

// Run은 ctx 종료 시까지 blocking으로 election 루프를 실행한다.
//
// client-go leaderelection.LeaderElector를 wrapper 한다 — 직접 위임하지 않고
// statusHolder를 통해 동시성 안전한 Status 갱신을 추가로 수행한다.
func (r *Real) Run(ctx context.Context) error {
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      r.leaseName,
			Namespace: r.namespace,
		},
		Client: r.client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity:      r.identity,
			EventRecorder: &record.FakeRecorder{}, // P6 통합 시 실 EventRecorder
		},
	}

	le, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   r.durations.LeaseDuration,
		RenewDeadline:   r.durations.RenewDeadline,
		RetryPeriod:     r.durations.RetryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(c context.Context) {
				r.set(StatusLeader)
				if r.cb.OnStartedLeading != nil {
					r.cb.OnStartedLeading(c)
				}
			},
			OnStoppedLeading: func() {
				r.set(StatusFollower)
				if r.cb.OnStoppedLeading != nil {
					r.cb.OnStoppedLeading()
				}
			},
			OnNewLeader: func(identity string) {
				if identity != r.identity {
					r.set(StatusFollower)
				}
				if r.cb.OnNewLeader != nil {
					r.cb.OnNewLeader(identity)
				}
			},
		},
	})
	if err != nil {
		return fmt.Errorf("election: NewLeaderElector: %w", err)
	}

	le.Run(ctx)
	return ctx.Err()
}

// Compile-time guard.
var _ Election = (*Real)(nil)

// PrimaryLeaseName은 RFC 0001 PostgresCluster CRD v2 의 shard 모델 위에서
// lease 명명 규약을 단일 출처로 제공한다.
//
//	role="shard", shardOrdinal>=0 → "<cluster>-shard-<ordinal>-primary"
//
// router 는 stateless 이므로 lease 가 존재하지 않는다 — role 이 "shard" 가
// 아니면 panic 하여 호출자 측 misuse 를 즉시 노출한다(단일 진실 강제).
func PrimaryLeaseName(cluster, role string, shardOrdinal int32) string {
	if role != "shard" {
		panic(fmt.Sprintf("PrimaryLeaseName: only role=\"shard\" is supported, got %q (router has no lease)", role))
	}
	if shardOrdinal < 0 {
		panic(fmt.Sprintf("PrimaryLeaseName: shardOrdinal must be >=0, got %d", shardOrdinal))
	}
	return fmt.Sprintf("%s-shard-%d-primary", cluster, shardOrdinal)
}
