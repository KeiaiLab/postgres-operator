/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package shardsplit

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// D.9.2 7-step e2e orchestrator — state machine 위의 단순 sequencer.
//
// 본 함수는 *순수 in-process* — 실 reconciler 가 본 호출을 *각 phase 마다*
// 별 Reconcile cycle 로 분할 호출. 본 함수는 unit test 시뮬레이션 용도
// (Mock Dependencies 로 7 step 전체 실행).

// RunAll 은 job.Status.Phase 가 Completed 또는 Failed/Aborted 에 도달할 때까지
// 7 step 을 sequential 실행한다. CDCCatchup 단계는 1회만 수행 — 실 reconciler
// 는 CDCReadyForCutover() 가 true 가 될 때까지 반복 호출.
//
// 반환:
//   - nil = Completed 도달
//   - error = step 실패 후 자동 Failed transition
//
// 본 함수는 *시뮬레이션 모드* — 실 K8s Reconcile loop 는 phase 별 분리 호출.
func RunAll(ctx context.Context, deps Dependencies, job *v1alpha1.ShardSplitJob) error {
	if job == nil {
		return fmt.Errorf("%w: job is nil", ErrStepFailed)
	}
	if job.Status.Phase == "" {
		job.Status.Phase = v1alpha1.ShardSplitPhasePending
	}
	for _, step := range AllSteps(deps) {
		// 종결 phase 면 즉시 중단.
		if IsTerminal(job.Status.Phase) {
			break
		}
		// Phase transition 검증.
		if err := ValidateTransition(job.Status.Phase, step.Phase()); err != nil {
			job.Status.Phase = v1alpha1.ShardSplitPhaseFailed
			job.Status.FailureReason = err.Error()
			return err
		}
		job.Status.Phase = step.Phase()
		if err := step.Run(ctx, job); err != nil {
			job.Status.Phase = v1alpha1.ShardSplitPhaseFailed
			job.Status.FailureReason = err.Error()
			return err
		}
	}
	if !IsTerminal(job.Status.Phase) {
		job.Status.Phase = v1alpha1.ShardSplitPhaseCompleted
		if job.Status.CompletedAt == nil {
			now := metav1.Now()
			job.Status.CompletedAt = &now
		}
	}
	return nil
}
