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
	"sync/atomic"
	"testing"
	"time"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// fakeDeps 는 테스트용 in-memory Dependencies.
type fakeDeps struct {
	snapshotLSN     string
	snapshotErr     error
	bootstrapCalls  atomic.Int32
	bootstrapErr    error
	initialCopyErr  error
	startCDCErr     error
	cdcLag          int64
	cdcLagErr       error
	cutoverErr      error
	updateRoutingOK atomic.Int32
	cleanupCalled   atomic.Int32
}

func (f *fakeDeps) Snapshot(_ context.Context, _ string) (string, error) {
	return f.snapshotLSN, f.snapshotErr
}
func (f *fakeDeps) BootstrapTarget(_ context.Context, _ v1alpha1.ShardSplitTarget) error {
	f.bootstrapCalls.Add(1)
	return f.bootstrapErr
}
func (f *fakeDeps) InitialCopy(_ context.Context, _, _, _ string) error {
	return f.initialCopyErr
}
func (f *fakeDeps) StartCDC(_ context.Context, _, _ string) error { return f.startCDCErr }
func (f *fakeDeps) CDCLag(_ context.Context, _, _ string) (int64, error) {
	return f.cdcLag, f.cdcLagErr
}
func (f *fakeDeps) Cutover(_ context.Context, _ string, _ time.Duration) error {
	return f.cutoverErr
}
func (f *fakeDeps) UpdateRouting(_ context.Context, _ *v1alpha1.ShardSplitJob) error {
	f.updateRoutingOK.Add(1)
	return nil
}
func (f *fakeDeps) CleanupSource(_ context.Context, _ *v1alpha1.ShardSplitJob) error {
	f.cleanupCalled.Add(1)
	return nil
}

func sampleJob() *v1alpha1.ShardSplitJob {
	return &v1alpha1.ShardSplitJob{
		Spec: v1alpha1.ShardSplitJobSpec{
			Cluster:  "cl",
			Keyspace: "ks",
			Sources:  []string{"sh-0"},
			Targets: []v1alpha1.ShardSplitTarget{
				{ShardID: "sh-0a", Ranges: []v1alpha1.ShardRangeEntry{{Lo: "0", Hi: "1", Shard: "sh-0a"}}},
				{ShardID: "sh-0b", Ranges: []v1alpha1.ShardRangeEntry{{Lo: "2", Hi: "3", Shard: "sh-0b"}}},
			},
		},
	}
}

func TestStepRun(t *testing.T) {
	ctx := context.Background()

	t.Run("Snapshot 정상 LSN 기록", func(t *testing.T) {
		deps := &fakeDeps{snapshotLSN: "0/3DA43A0"}
		job := sampleJob()
		step := StepSnapshotWAL{Deps: deps}
		if err := step.Run(ctx, job); err != nil {
			t.Fatalf("Snapshot Run: %v", err)
		}
		if job.Status.SnapshotLSN != "0/3DA43A0" {
			t.Fatalf("SnapshotLSN want=0/3DA43A0 got=%s", job.Status.SnapshotLSN)
		}
		if job.Status.StartedAt == nil {
			t.Fatalf("StartedAt must be set")
		}
	})

	t.Run("Snapshot empty Sources 거부", func(t *testing.T) {
		deps := &fakeDeps{}
		job := sampleJob()
		job.Spec.Sources = nil
		err := StepSnapshotWAL{Deps: deps}.Run(ctx, job)
		if !errors.Is(err, ErrStepFailed) {
			t.Fatalf("want ErrStepFailed, got %v", err)
		}
	})

	t.Run("Bootstrap N target 모두 호출", func(t *testing.T) {
		deps := &fakeDeps{}
		job := sampleJob()
		if err := (StepBootstrap{Deps: deps}).Run(ctx, job); err != nil {
			t.Fatalf("Bootstrap Run: %v", err)
		}
		if deps.bootstrapCalls.Load() != 2 {
			t.Fatalf("BootstrapTarget calls want=2 got=%d", deps.bootstrapCalls.Load())
		}
	})

	t.Run("InitialCopy SnapshotLSN missing 거부", func(t *testing.T) {
		deps := &fakeDeps{}
		job := sampleJob()
		err := StepInitialCopy{Deps: deps}.Run(ctx, job)
		if !errors.Is(err, ErrStepFailed) {
			t.Fatalf("want ErrStepFailed (no SnapshotLSN), got %v", err)
		}
	})

	t.Run("InitialCopy 정상", func(t *testing.T) {
		deps := &fakeDeps{}
		job := sampleJob()
		job.Status.SnapshotLSN = "0/3DA43A0"
		if err := (StepInitialCopy{Deps: deps}).Run(ctx, job); err != nil {
			t.Fatalf("InitialCopy: %v", err)
		}
	})

	t.Run("CDCCatchup lag 측정", func(t *testing.T) {
		deps := &fakeDeps{cdcLag: 1024}
		job := sampleJob()
		if err := (StepCDCCatchup{Deps: deps}).Run(ctx, job); err != nil {
			t.Fatalf("CDCCatchup: %v", err)
		}
		if job.Status.CurrentLagBytes != 1024 {
			t.Fatalf("Lag want=1024 got=%d", job.Status.CurrentLagBytes)
		}
	})

	t.Run("Cutover CDC not ready 거부", func(t *testing.T) {
		deps := &fakeDeps{}
		job := sampleJob()
		job.Status.Phase = v1alpha1.ShardSplitPhaseCDCCatchup
		job.Spec.CDCMaxLag = 1024
		job.Status.CurrentLagBytes = 2048 // 초과
		err := StepCutover{Deps: deps}.Run(ctx, job)
		if !errors.Is(err, ErrStepFailed) {
			t.Fatalf("want ErrStepFailed (CDC not ready), got %v", err)
		}
	})

	t.Run("Cutover 정상 + window 기본 60s", func(t *testing.T) {
		deps := &fakeDeps{}
		job := sampleJob()
		job.Status.Phase = v1alpha1.ShardSplitPhaseCDCCatchup
		job.Status.CurrentLagBytes = 100 // < default 16MB
		if err := (StepCutover{Deps: deps}).Run(ctx, job); err != nil {
			t.Fatalf("Cutover: %v", err)
		}
		if job.Status.CutoverStartedAt == nil {
			t.Fatalf("CutoverStartedAt must be set")
		}
	})

	t.Run("RoutingUpdate 호출", func(t *testing.T) {
		deps := &fakeDeps{}
		job := sampleJob()
		if err := (StepRoutingUpdate{Deps: deps}).Run(ctx, job); err != nil {
			t.Fatalf("RoutingUpdate: %v", err)
		}
		if deps.updateRoutingOK.Load() != 1 {
			t.Fatalf("UpdateRouting must be called once")
		}
	})

	t.Run("Cleanup 완료 시각 기록", func(t *testing.T) {
		deps := &fakeDeps{}
		job := sampleJob()
		if err := (StepCleanup{Deps: deps}).Run(ctx, job); err != nil {
			t.Fatalf("Cleanup: %v", err)
		}
		if deps.cleanupCalled.Load() != 1 {
			t.Fatalf("CleanupSource must be called once")
		}
		if job.Status.CompletedAt == nil {
			t.Fatalf("CompletedAt must be set")
		}
	})

	t.Run("nil deps 거부", func(t *testing.T) {
		err := StepSnapshotWAL{Deps: nil}.Run(ctx, sampleJob())
		if !errors.Is(err, ErrDependencyMissing) {
			t.Fatalf("want ErrDependencyMissing, got %v", err)
		}
	})

	t.Run("AllSteps 7 step phase 순서", func(t *testing.T) {
		steps := AllSteps(&fakeDeps{})
		if len(steps) != 7 {
			t.Fatalf("AllSteps len want=7 got=%d", len(steps))
		}
		expectedPhases := []v1alpha1.ShardSplitJobPhase{
			v1alpha1.ShardSplitPhaseSnapshotWAL,
			v1alpha1.ShardSplitPhaseBootstrap,
			v1alpha1.ShardSplitPhaseInitialCopy,
			v1alpha1.ShardSplitPhaseCDCCatchup,
			v1alpha1.ShardSplitPhaseCutover,
			v1alpha1.ShardSplitPhaseRoutingUpdate,
			v1alpha1.ShardSplitPhaseCleanup,
		}
		for i, s := range steps {
			if s.Phase() != expectedPhases[i] {
				t.Fatalf("step[%d] phase want=%s got=%s", i, expectedPhases[i], s.Phase())
			}
		}
	})

	t.Run("CanRollback policy 5 step true / 2 step false", func(t *testing.T) {
		deps := &fakeDeps{}
		canRollback := map[v1alpha1.ShardSplitJobPhase]bool{}
		for _, s := range AllSteps(deps) {
			canRollback[s.Phase()] = s.CanRollback()
		}
		// 1~5 (SnapshotWAL ~ Cutover) rollback 가능
		for _, p := range []v1alpha1.ShardSplitJobPhase{
			v1alpha1.ShardSplitPhaseSnapshotWAL,
			v1alpha1.ShardSplitPhaseBootstrap,
			v1alpha1.ShardSplitPhaseInitialCopy,
			v1alpha1.ShardSplitPhaseCDCCatchup,
			v1alpha1.ShardSplitPhaseCutover,
		} {
			if !canRollback[p] {
				t.Fatalf("%s rollback must be allowed", p)
			}
		}
		// 6~7 (RoutingUpdate, Cleanup) rollback 불가 (step 자체 기준)
		for _, p := range []v1alpha1.ShardSplitJobPhase{
			v1alpha1.ShardSplitPhaseRoutingUpdate,
			v1alpha1.ShardSplitPhaseCleanup,
		} {
			if canRollback[p] {
				t.Fatalf("%s rollback must be forbidden at step layer", p)
			}
		}
	})
}
