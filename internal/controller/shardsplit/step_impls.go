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
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// D.9.3 ~ D.9.9 — 7 step 구체 구현 skeleton.
//
// 각 step 은 Step interface 를 만족하며, 실 K8s/SQL 호출은 *주입된*
// Dependencies 인터페이스에 위임한다. 테스트는 mock Dependencies 로
// step 진행 + 에러 분기를 검증.

// ErrDependencyMissing 는 step Run 호출 시 필수 dependency 누락 시 반환.
var ErrDependencyMissing = errors.New("shardsplit: dependency injection missing")

// Dependencies 는 step 실 K8s/SQL 호출 contract — reconciler 가 주입.
//
// 본 인터페이스의 실 구현은 K8s controller-runtime client + sql.DB +
// command runner 의 thin wrapper — multi-month 사이즈. 본 turn 은
// interface freeze + step skeleton 동결.
type Dependencies interface {
	// Snapshot 은 source shard 의 시점 일관 base snapshot 을 생성하고 LSN 반환.
	Snapshot(ctx context.Context, source string) (lsn string, err error)
	// BootstrapTarget 는 target shard 의 StatefulSet + PG init 을 적용.
	BootstrapTarget(ctx context.Context, target v1alpha1.ShardSplitTarget) error
	// InitialCopy 는 base snapshot 을 target 에 적용 (pg_basebackup 또는 logical).
	InitialCopy(ctx context.Context, source, target, baseLSN string) error
	// StartCDC 는 source → target logical replication subscription 시작.
	StartCDC(ctx context.Context, source, target string) error
	// CDCLag 는 현재 lag bytes 를 조회.
	CDCLag(ctx context.Context, source, target string) (int64, error)
	// Cutover 는 source write 차단 + remaining lag flush + replication 정지.
	Cutover(ctx context.Context, source string, window time.Duration) error
	// UpdateRouting 은 ShardRange CRD 의 ranges + metadata store 를 atomic 갱신.
	UpdateRouting(ctx context.Context, job *v1alpha1.ShardSplitJob) error
	// CleanupSource 는 source 의 split-out 키 범위 데이터를 회수.
	CleanupSource(ctx context.Context, job *v1alpha1.ShardSplitJob) error
}

// runStep 은 공통 wrapper — phase 진입 시각 기록 + 에러 wrap + status patch.
func runStep(deps Dependencies, _ context.Context, job *v1alpha1.ShardSplitJob, action func() error) error {
	if deps == nil {
		return fmt.Errorf("%w: Dependencies is nil", ErrDependencyMissing)
	}
	if job == nil {
		return fmt.Errorf("%w: job is nil", ErrStepFailed)
	}
	if err := action(); err != nil {
		return fmt.Errorf("%w: %v", ErrStepFailed, err)
	}
	return nil
}

// --- D.9.3 Step 1 — Snapshot + WAL capture ---------------------------------------------

type StepSnapshotWAL struct{ Deps Dependencies }

func (s StepSnapshotWAL) Phase() v1alpha1.ShardSplitJobPhase {
	return v1alpha1.ShardSplitPhaseSnapshotWAL
}
func (s StepSnapshotWAL) CanRollback() bool { return true }
func (s StepSnapshotWAL) Run(ctx context.Context, job *v1alpha1.ShardSplitJob) error {
	return runStep(s.Deps, ctx, job, func() error {
		if len(job.Spec.Sources) == 0 {
			return fmt.Errorf("sources empty")
		}
		lsn, err := s.Deps.Snapshot(ctx, job.Spec.Sources[0])
		if err != nil {
			return err
		}
		job.Status.SnapshotLSN = lsn
		now := metav1.Now()
		if job.Status.StartedAt == nil {
			job.Status.StartedAt = &now
		}
		return nil
	})
}

// --- D.9.4 Step 2 — Bootstrap target shard ---------------------------------------------

type StepBootstrap struct{ Deps Dependencies }

func (s StepBootstrap) Phase() v1alpha1.ShardSplitJobPhase {
	return v1alpha1.ShardSplitPhaseBootstrap
}
func (s StepBootstrap) CanRollback() bool { return true }
func (s StepBootstrap) Run(ctx context.Context, job *v1alpha1.ShardSplitJob) error {
	return runStep(s.Deps, ctx, job, func() error {
		for _, t := range job.Spec.Targets {
			if err := s.Deps.BootstrapTarget(ctx, t); err != nil {
				return fmt.Errorf("target=%s: %w", t.ShardID, err)
			}
		}
		return nil
	})
}

// --- D.9.5 Step 3 — Initial copy --------------------------------------------------------

type StepInitialCopy struct{ Deps Dependencies }

func (s StepInitialCopy) Phase() v1alpha1.ShardSplitJobPhase {
	return v1alpha1.ShardSplitPhaseInitialCopy
}
func (s StepInitialCopy) CanRollback() bool { return true }
func (s StepInitialCopy) Run(ctx context.Context, job *v1alpha1.ShardSplitJob) error {
	return runStep(s.Deps, ctx, job, func() error {
		if job.Status.SnapshotLSN == "" {
			return fmt.Errorf("SnapshotLSN missing — previous SnapshotWAL step incomplete")
		}
		src := job.Spec.Sources[0]
		for _, t := range job.Spec.Targets {
			if err := s.Deps.InitialCopy(ctx, src, t.ShardID, job.Status.SnapshotLSN); err != nil {
				return fmt.Errorf("source=%s target=%s: %w", src, t.ShardID, err)
			}
		}
		return nil
	})
}

// --- D.9.6 Step 4 — CDC catch-up --------------------------------------------------------

type StepCDCCatchup struct{ Deps Dependencies }

func (s StepCDCCatchup) Phase() v1alpha1.ShardSplitJobPhase {
	return v1alpha1.ShardSplitPhaseCDCCatchup
}
func (s StepCDCCatchup) CanRollback() bool { return true }
func (s StepCDCCatchup) Run(ctx context.Context, job *v1alpha1.ShardSplitJob) error {
	return runStep(s.Deps, ctx, job, func() error {
		src := job.Spec.Sources[0]
		for _, t := range job.Spec.Targets {
			if err := s.Deps.StartCDC(ctx, src, t.ShardID); err != nil {
				return fmt.Errorf("source=%s target=%s StartCDC: %w", src, t.ShardID, err)
			}
		}
		// Lag 조회 — reconciler 가 본 step 을 반복 호출하며 CDCReadyForCutover() 도달까지 대기.
		var maxLag int64
		for _, t := range job.Spec.Targets {
			lag, err := s.Deps.CDCLag(ctx, src, t.ShardID)
			if err != nil {
				return fmt.Errorf("target=%s CDCLag: %w", t.ShardID, err)
			}
			if lag > maxLag {
				maxLag = lag
			}
		}
		job.Status.CurrentLagBytes = maxLag
		return nil
	})
}

// --- D.9.7 Step 5 — Cutover -------------------------------------------------------------

type StepCutover struct{ Deps Dependencies }

func (s StepCutover) Phase() v1alpha1.ShardSplitJobPhase {
	return v1alpha1.ShardSplitPhaseCutover
}
func (s StepCutover) CanRollback() bool { return true } // RoutingUpdate 직전까지 가능
func (s StepCutover) Run(ctx context.Context, job *v1alpha1.ShardSplitJob) error {
	return runStep(s.Deps, ctx, job, func() error {
		if !CDCReadyForCutover(job) {
			return fmt.Errorf("CDC not ready: lag=%d max=%d",
				job.Status.CurrentLagBytes, job.Spec.CDCMaxLag)
		}
		now := metav1.Now()
		job.Status.CutoverStartedAt = &now
		window := job.Spec.CutoverWindow.Duration
		if window == 0 {
			window = 60 * time.Second
		}
		if err := s.Deps.Cutover(ctx, job.Spec.Sources[0], window); err != nil {
			return err
		}
		return nil
	})
}

// --- D.9.8 Step 6 — Routing update ------------------------------------------------------

type StepRoutingUpdate struct{ Deps Dependencies }

func (s StepRoutingUpdate) Phase() v1alpha1.ShardSplitJobPhase {
	return v1alpha1.ShardSplitPhaseRoutingUpdate
}
func (s StepRoutingUpdate) CanRollback() bool { return false } // AllowForwardOnly 가 아니면 역방향 logical 로 가능, 본 step 자체는 forward
func (s StepRoutingUpdate) Run(ctx context.Context, job *v1alpha1.ShardSplitJob) error {
	return runStep(s.Deps, ctx, job, func() error {
		return s.Deps.UpdateRouting(ctx, job)
	})
}

// --- D.9.9 Step 7 — Source cleanup ------------------------------------------------------

type StepCleanup struct{ Deps Dependencies }

func (s StepCleanup) Phase() v1alpha1.ShardSplitJobPhase {
	return v1alpha1.ShardSplitPhaseCleanup
}
func (s StepCleanup) CanRollback() bool { return false }
func (s StepCleanup) Run(ctx context.Context, job *v1alpha1.ShardSplitJob) error {
	return runStep(s.Deps, ctx, job, func() error {
		if err := s.Deps.CleanupSource(ctx, job); err != nil {
			return err
		}
		now := metav1.Now()
		job.Status.CompletedAt = &now
		return nil
	})
}

// AllSteps 는 7 step 인스턴스를 phase 순서대로 반환한다.
func AllSteps(deps Dependencies) []Step {
	return []Step{
		StepSnapshotWAL{Deps: deps},
		StepBootstrap{Deps: deps},
		StepInitialCopy{Deps: deps},
		StepCDCCatchup{Deps: deps},
		StepCutover{Deps: deps},
		StepRoutingUpdate{Deps: deps},
		StepCleanup{Deps: deps},
	}
}
