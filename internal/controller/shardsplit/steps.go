/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package shardsplit implements the G4 online resharding 7-step orchestrator
// (D.9.2-D.9.10).
//
// 본 패키지는 *state machine + step interface + 결정 함수* 만 노출. 실 K8s
// API + SQL 호출은 reconciler layer (`internal/controller/shardsplit_controller.go`,
// 본 turn scope 외) 에 위임된다.
package shardsplit

import (
	"context"
	"errors"
	"fmt"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// ErrStepFailed 는 단일 step 실행 실패 시 wrap 되는 sentinel.
var ErrStepFailed = errors.New("shardsplit: step failed")

// ErrInvalidTransition 는 phase 순서 위반 시 반환.
var ErrInvalidTransition = errors.New("shardsplit: invalid phase transition")

// ErrCutoverWindowExceeded 는 cutover window 초과 시 abort 결정.
var ErrCutoverWindowExceeded = errors.New("shardsplit: cutover window exceeded — auto abort + rollback")

// Step 은 7-step state machine 의 단일 단계 contract 이다.
//
// 실 구현체 (7개) 는 internal/controller/shardsplit/ + internal/router/ 에
// 산재 — 본 패키지는 interface freeze 만 담당.
type Step interface {
	// Phase 는 본 step 의 target phase. state machine 의 다음 phase 진입.
	Phase() v1alpha1.ShardSplitJobPhase
	// Run 은 본 step 을 실행. 성공 시 nil + status 갱신. 실패 시 wrapped error.
	Run(ctx context.Context, job *v1alpha1.ShardSplitJob) error
	// CanRollback 은 본 phase 에서 rollback 가능한지 반환. false 면 forward-only.
	CanRollback() bool
}

// NextPhase 는 현재 phase 의 다음 정상 phase 를 반환한다. 종결 phase 는
// 그대로 반환 (no-op transition).
func NextPhase(current v1alpha1.ShardSplitJobPhase) v1alpha1.ShardSplitJobPhase {
	switch current {
	case v1alpha1.ShardSplitPhasePending:
		return v1alpha1.ShardSplitPhaseSnapshotWAL
	case v1alpha1.ShardSplitPhaseSnapshotWAL:
		return v1alpha1.ShardSplitPhaseBootstrap
	case v1alpha1.ShardSplitPhaseBootstrap:
		return v1alpha1.ShardSplitPhaseInitialCopy
	case v1alpha1.ShardSplitPhaseInitialCopy:
		return v1alpha1.ShardSplitPhaseCDCCatchup
	case v1alpha1.ShardSplitPhaseCDCCatchup:
		return v1alpha1.ShardSplitPhaseCutover
	case v1alpha1.ShardSplitPhaseCutover:
		return v1alpha1.ShardSplitPhaseRoutingUpdate
	case v1alpha1.ShardSplitPhaseRoutingUpdate:
		return v1alpha1.ShardSplitPhaseCleanup
	case v1alpha1.ShardSplitPhaseCleanup:
		return v1alpha1.ShardSplitPhaseCompleted
	default:
		// Completed / Failed / Aborted — 종결.
		return current
	}
}

// ValidateTransition 은 from → to 가 정상 state machine edge 인지 검사한다.
//
// 허용된 edge:
//
//	Pending → SnapshotWAL → Bootstrap → InitialCopy → CDCCatchup → Cutover
//	→ RoutingUpdate → Cleanup → Completed
//
//	임의 phase → Failed (오류 발생 시)
//	임의 phase (≤ Cutover) → Aborted (rollback 가능 시)
func ValidateTransition(from, to v1alpha1.ShardSplitJobPhase) error {
	// 임의 → Failed 허용 (오류 처리).
	if to == v1alpha1.ShardSplitPhaseFailed {
		return nil
	}
	// 임의 (≤ Cutover) → Aborted 허용 (rollback).
	if to == v1alpha1.ShardSplitPhaseAborted {
		if isPastCutover(from) {
			return fmt.Errorf("%w: %s → Aborted (post-cutover, rollback impossible if AllowForwardOnly)",
				ErrInvalidTransition, from)
		}
		return nil
	}
	// 정상 진행: NextPhase(from) == to.
	if NextPhase(from) == to {
		return nil
	}
	return fmt.Errorf("%w: %s → %s (illegal edge)", ErrInvalidTransition, from, to)
}

func isPastCutover(p v1alpha1.ShardSplitJobPhase) bool {
	switch p {
	case v1alpha1.ShardSplitPhaseRoutingUpdate,
		v1alpha1.ShardSplitPhaseCleanup,
		v1alpha1.ShardSplitPhaseCompleted:
		return true
	default:
		return false
	}
}

// IsTerminal 은 종결 phase (재실행 불가) 인지 반환한다.
func IsTerminal(p v1alpha1.ShardSplitJobPhase) bool {
	switch p {
	case v1alpha1.ShardSplitPhaseCompleted,
		v1alpha1.ShardSplitPhaseFailed,
		v1alpha1.ShardSplitPhaseAborted:
		return true
	default:
		return false
	}
}

// RollbackAllowed 는 현재 phase 와 job 정책으로 rollback 가능한지 판정.
//
// 규칙:
//   - phase == Cleanup / Completed: rollback 불가 (forward-only)
//   - AllowForwardOnly=true 이고 phase ≥ Cutover: rollback 불가 (D.9.10)
//   - phase < Cutover: 항상 rollback 가능 (역방향 logical replication 불요)
//   - phase == Cutover 진입 직후 (RoutingUpdate 전): rollback 가능
//   - phase == RoutingUpdate: forward-only 가 아니면 역방향 replication 으로 rollback 가능
func RollbackAllowed(job *v1alpha1.ShardSplitJob) bool {
	if job == nil {
		return false
	}
	p := job.Status.Phase
	if p == v1alpha1.ShardSplitPhaseCleanup ||
		p == v1alpha1.ShardSplitPhaseCompleted {
		return false
	}
	if job.Spec.AllowForwardOnly && (p == v1alpha1.ShardSplitPhaseCutover ||
		p == v1alpha1.ShardSplitPhaseRoutingUpdate) {
		return false
	}
	return true
}

// CDCReadyForCutover 는 cutover 진입 허용 여부 판정.
//
// 정책:
//   - currentLag < CDCMaxLag → ready
//   - currentLag == 0 (sync_state=streaming 도달) → ready
//   - CDCMaxLag 가 0 (default 미설정) → 16MB 기본 적용
//   - phase 는 CDCCatchup 또는 Cutover 양쪽 허용 (state transition 직전/직후 모두 평가 가능)
func CDCReadyForCutover(job *v1alpha1.ShardSplitJob) bool {
	if job == nil {
		return false
	}
	if job.Status.Phase != v1alpha1.ShardSplitPhaseCDCCatchup &&
		job.Status.Phase != v1alpha1.ShardSplitPhaseCutover {
		return false
	}
	maxLag := job.Spec.CDCMaxLag
	if maxLag == 0 {
		maxLag = 16 * 1024 * 1024
	}
	return job.Status.CurrentLagBytes < maxLag
}

// StepNames 는 7-step 진행 phase 의 사람-가독 이름.
func StepNames() []string {
	return []string{
		"1. Snapshot + WAL capture",
		"2. Bootstrap target shard",
		"3. Initial copy",
		"4. CDC catch-up",
		"5. Cutover (write-block window)",
		"6. Routing update",
		"7. Source cleanup",
	}
}

// StepCount 는 7 (불변).
func StepCount() int { return 7 }
