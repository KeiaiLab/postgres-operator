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
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestRunAll_HappyPath(t *testing.T) {
	deps := &fakeDeps{
		snapshotLSN: "0/1AB",
		cdcLag:      100, // < default 16MB → CDC ready
	}
	job := sampleJob()

	err := RunAll(context.Background(), deps, job)
	if err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if job.Status.Phase != v1alpha1.ShardSplitPhaseCompleted {
		t.Fatalf("final phase want=Completed got=%s", job.Status.Phase)
	}
	if job.Status.SnapshotLSN != "0/1AB" {
		t.Fatalf("SnapshotLSN passthrough")
	}
	if job.Status.CompletedAt == nil {
		t.Fatalf("CompletedAt must be set")
	}
	// 7 step 모두 실행: bootstrap 2 calls + updateRouting + cleanup.
	if deps.bootstrapCalls.Load() != 2 {
		t.Fatalf("Bootstrap calls want=2 got=%d", deps.bootstrapCalls.Load())
	}
	if deps.updateRoutingOK.Load() != 1 {
		t.Fatalf("UpdateRouting must be called once")
	}
	if deps.cleanupCalled.Load() != 1 {
		t.Fatalf("CleanupSource must be called once")
	}
}

func TestRunAll_SnapshotFailure(t *testing.T) {
	deps := &fakeDeps{snapshotErr: errors.New("WAL position unavailable")}
	job := sampleJob()
	err := RunAll(context.Background(), deps, job)
	if !errors.Is(err, ErrStepFailed) {
		t.Fatalf("want ErrStepFailed, got %v", err)
	}
	if job.Status.Phase != v1alpha1.ShardSplitPhaseFailed {
		t.Fatalf("Phase want=Failed got=%s", job.Status.Phase)
	}
	if job.Status.FailureReason == "" {
		t.Fatalf("FailureReason must be set")
	}
}

func TestRunAll_CDCNotReady(t *testing.T) {
	deps := &fakeDeps{
		snapshotLSN: "0/1",
		cdcLag:      1 << 30, // 1GB lag > default 16MB
	}
	job := sampleJob()
	job.Spec.CDCMaxLag = 16 * 1024 * 1024 // 16MB explicit
	err := RunAll(context.Background(), deps, job)
	if !errors.Is(err, ErrStepFailed) {
		t.Fatalf("want ErrStepFailed (CDC not ready), got %v", err)
	}
	if job.Status.Phase != v1alpha1.ShardSplitPhaseFailed {
		t.Fatalf("Phase want=Failed got=%s", job.Status.Phase)
	}
}

func TestRunAll_NilJob(t *testing.T) {
	err := RunAll(context.Background(), &fakeDeps{}, nil)
	if !errors.Is(err, ErrStepFailed) {
		t.Fatalf("want ErrStepFailed for nil job, got %v", err)
	}
}

func TestRunAll_PendingPhaseInit(t *testing.T) {
	deps := &fakeDeps{snapshotLSN: "0/1"}
	job := sampleJob()
	// 명시적으로 phase 비우기 — RunAll 가 Pending 으로 초기화 후 진행.
	job.Status.Phase = ""
	if err := RunAll(context.Background(), deps, job); err != nil {
		t.Fatalf("RunAll: %v", err)
	}
	if job.Status.Phase != v1alpha1.ShardSplitPhaseCompleted {
		t.Fatalf("Phase want=Completed got=%s", job.Status.Phase)
	}
}
