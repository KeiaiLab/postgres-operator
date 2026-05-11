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
	"errors"
	"fmt"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// PromotionStep 은 replica → primary 승격 시 instance manager 가 수행할 *원자
// 단계*. 각 단계는 결정적이고 순서를 보존한다.
type PromotionStep string

const (
	// StepRemoveStandbySignal — $PGDATA/standby.signal 제거.
	StepRemoveStandbySignal PromotionStep = "RemoveStandbySignal"

	// StepPgCtlPromote — `pg_ctl promote -D $PGDATA` 실행 (PG 자체 promote API).
	StepPgCtlPromote PromotionStep = "PgCtlPromote"

	// StepWaitNotInRecovery — `pg_is_in_recovery()` 가 false 가 될 때까지 polling.
	StepWaitNotInRecovery PromotionStep = "WaitNotInRecovery"

	// StepUpdateInstanceRole — instance manager 의 role annotation 을 primary 로 갱신.
	StepUpdateInstanceRole PromotionStep = "UpdateInstanceRole"
)

// PromotionTarget 은 승격 대상 pod 식별자 + 메타데이터.
type PromotionTarget struct {
	// ShardName 은 shard 식별자 (예: "shard-0"). 로그 + 추적용.
	ShardName string

	// Pod 는 K8s pod 이름.
	Pod string

	// Endpoint 는 향후 connection probe 용 hostname:port.
	Endpoint string
}

// PromotionPlan 은 *순수 결정* 으로 만들어진 단계 목록. 실행은 Promoter 가 담당.
// 분리 이유: testability — planner 는 mock 없이 단위 테스트로 검증 가능, executor
// 는 별도 interface 로 추상화하여 실제 pod-exec 의존성을 격리.
type PromotionPlan struct {
	Target PromotionTarget
	Steps  []PromotionStep
}

// ErrNoPromotionTarget — Decision.PromotionCandidate 가 nil 인데 BuildPromotionPlan
// 이 호출된 경우. 호출자가 nil-check 누락 신호.
var ErrNoPromotionTarget = errors.New("failover: promotion plan requires non-nil candidate")

// BuildPromotionPlan 은 Decision 의 PromotionCandidate 로부터 결정적 단계 목록을
// 만든다.
//
// 결정적 순서 (변경 금지 — 각 단계가 다음 단계의 전제조건):
//  1. RemoveStandbySignal — PG 의 standby 모드 해제 필요조건
//  2. PgCtlPromote — replay → primary 전환 트리거
//  3. WaitNotInRecovery — promote 완료까지 polling
//  4. UpdateInstanceRole — annotation 갱신 (operator status 합성 입력)
//
// shardName 은 로그/추적용. PromotionCandidate 의 Endpoint 정보를 그대로 보존.
func BuildPromotionPlan(shardName string, candidate *postgresv1alpha1.ShardEndpoint) (PromotionPlan, error) {
	if candidate == nil {
		return PromotionPlan{}, ErrNoPromotionTarget
	}
	return PromotionPlan{
		Target: PromotionTarget{
			ShardName: shardName,
			Pod:       candidate.Pod,
			Endpoint:  candidate.Endpoint,
		},
		Steps: []PromotionStep{
			StepRemoveStandbySignal,
			StepPgCtlPromote,
			StepWaitNotInRecovery,
			StepUpdateInstanceRole,
		},
	}, nil
}

// Promoter 는 PromotionPlan 을 실행한다. 실제 구현은 controller layer 에서 Pod
// exec / annotation patch 를 수행. 본 패키지는 interface 만 정의 — 단위 테스트는
// FakePromoter 가 plan 캡처 후 결정만 검증.
type Promoter interface {
	Execute(ctx context.Context, plan PromotionPlan) error
}

// PromoteFromDecision 은 Decision 을 받아 plan 생성 + Promoter 실행을 1-step 으로
// 묶는 helper. controller reconcile 루프의 entry point.
//
// Decision.Failed=false 면 nil 반환 (no-op). Decision.PromotionCandidate=nil 이면
// ErrNoPromotionTarget. Promoter==nil 이면 panic 회피 위해 즉시 에러.
func PromoteFromDecision(ctx context.Context, shardName string, d Decision, p Promoter) error {
	if !d.Failed {
		return nil
	}
	if p == nil {
		return errors.New("failover: nil Promoter — controller wiring error")
	}
	plan, err := BuildPromotionPlan(shardName, d.PromotionCandidate)
	if err != nil {
		return fmt.Errorf("build plan: %w", err)
	}
	if err := p.Execute(ctx, plan); err != nil {
		return fmt.Errorf("execute promotion plan for shard %q pod %q: %w", plan.Target.ShardName, plan.Target.Pod, err)
	}
	return nil
}
