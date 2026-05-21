/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package shardsplit

import (
	"errors"
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestStateMachine(t *testing.T) {
	t.Run("NextPhase 정상 chain Pending → Completed", func(t *testing.T) {
		seq := []v1alpha1.ShardSplitJobPhase{
			v1alpha1.ShardSplitPhasePending,
			v1alpha1.ShardSplitPhaseSnapshotWAL,
			v1alpha1.ShardSplitPhaseBootstrap,
			v1alpha1.ShardSplitPhaseInitialCopy,
			v1alpha1.ShardSplitPhaseCDCCatchup,
			v1alpha1.ShardSplitPhaseCutover,
			v1alpha1.ShardSplitPhaseRoutingUpdate,
			v1alpha1.ShardSplitPhaseCleanup,
			v1alpha1.ShardSplitPhaseCompleted,
		}
		for i := 0; i < len(seq)-1; i++ {
			got := NextPhase(seq[i])
			if got != seq[i+1] {
				t.Fatalf("NextPhase(%s) want=%s got=%s", seq[i], seq[i+1], got)
			}
		}
		// terminal: no-op
		if NextPhase(v1alpha1.ShardSplitPhaseCompleted) != v1alpha1.ShardSplitPhaseCompleted {
			t.Fatalf("terminal phase must self-loop")
		}
		if NextPhase(v1alpha1.ShardSplitPhaseFailed) != v1alpha1.ShardSplitPhaseFailed {
			t.Fatalf("Failed must self-loop")
		}
	})

	t.Run("ValidateTransition 정상 + Failed + Aborted", func(t *testing.T) {
		// 정상 chain.
		if err := ValidateTransition(
			v1alpha1.ShardSplitPhaseInitialCopy,
			v1alpha1.ShardSplitPhaseCDCCatchup,
		); err != nil {
			t.Fatalf("legal edge rejected: %v", err)
		}
		// 임의 → Failed: 항상 허용.
		if err := ValidateTransition(
			v1alpha1.ShardSplitPhaseBootstrap,
			v1alpha1.ShardSplitPhaseFailed,
		); err != nil {
			t.Fatalf("Failed transition rejected: %v", err)
		}
		// Pending → Aborted: 허용.
		if err := ValidateTransition(
			v1alpha1.ShardSplitPhasePending,
			v1alpha1.ShardSplitPhaseAborted,
		); err != nil {
			t.Fatalf("Pending → Aborted rejected: %v", err)
		}
	})

	t.Run("ValidateTransition 위반 — post-cutover Aborted 거부", func(t *testing.T) {
		err := ValidateTransition(
			v1alpha1.ShardSplitPhaseRoutingUpdate,
			v1alpha1.ShardSplitPhaseAborted,
		)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("want ErrInvalidTransition for post-cutover Aborted, got %v", err)
		}
	})

	t.Run("ValidateTransition 위반 — skip edge", func(t *testing.T) {
		err := ValidateTransition(
			v1alpha1.ShardSplitPhasePending,
			v1alpha1.ShardSplitPhaseCutover, // skip 5 steps
		)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("want ErrInvalidTransition for skip, got %v", err)
		}
	})

	t.Run("IsTerminal 3 phase", func(t *testing.T) {
		cases := map[v1alpha1.ShardSplitJobPhase]bool{
			v1alpha1.ShardSplitPhasePending:    false,
			v1alpha1.ShardSplitPhaseCompleted:  true,
			v1alpha1.ShardSplitPhaseFailed:     true,
			v1alpha1.ShardSplitPhaseAborted:    true,
			v1alpha1.ShardSplitPhaseCDCCatchup: false,
		}
		for p, want := range cases {
			if got := IsTerminal(p); got != want {
				t.Fatalf("IsTerminal(%s) want=%v got=%v", p, want, got)
			}
		}
	})

	t.Run("RollbackAllowed 정책", func(t *testing.T) {
		// 정상: < Cutover always allowed.
		j := &v1alpha1.ShardSplitJob{}
		j.Status.Phase = v1alpha1.ShardSplitPhaseBootstrap
		if !RollbackAllowed(j) {
			t.Fatalf("Bootstrap rollback must be allowed")
		}
		// Cleanup 후: 불가.
		j.Status.Phase = v1alpha1.ShardSplitPhaseCleanup
		if RollbackAllowed(j) {
			t.Fatalf("Cleanup rollback must be forbidden")
		}
		// Completed: 불가.
		j.Status.Phase = v1alpha1.ShardSplitPhaseCompleted
		if RollbackAllowed(j) {
			t.Fatalf("Completed rollback must be forbidden")
		}
		// AllowForwardOnly + Cutover: 불가.
		j.Spec.AllowForwardOnly = true
		j.Status.Phase = v1alpha1.ShardSplitPhaseCutover
		if RollbackAllowed(j) {
			t.Fatalf("forward-only Cutover rollback must be forbidden")
		}
		// nil: 불가.
		if RollbackAllowed(nil) {
			t.Fatalf("nil rollback must be forbidden")
		}
	})

	t.Run("CDCReadyForCutover lag 정책", func(t *testing.T) {
		// 잘못된 phase: 항상 false.
		j := &v1alpha1.ShardSplitJob{}
		j.Status.Phase = v1alpha1.ShardSplitPhaseBootstrap
		if CDCReadyForCutover(j) {
			t.Fatalf("wrong phase must return false")
		}
		// CDCCatchup + lag < default (16MB): ready.
		j.Status.Phase = v1alpha1.ShardSplitPhaseCDCCatchup
		j.Status.CurrentLagBytes = 1024
		if !CDCReadyForCutover(j) {
			t.Fatalf("small lag must be ready")
		}
		// CDCCatchup + lag > custom max: not ready.
		j.Spec.CDCMaxLag = 1024
		j.Status.CurrentLagBytes = 2048
		if CDCReadyForCutover(j) {
			t.Fatalf("lag > max must not be ready")
		}
		// nil: 불가.
		if CDCReadyForCutover(nil) {
			t.Fatalf("nil must not be ready")
		}
	})

	t.Run("StepNames + StepCount", func(t *testing.T) {
		names := StepNames()
		if len(names) != 7 {
			t.Fatalf("StepNames count want=7 got=%d", len(names))
		}
		if StepCount() != 7 {
			t.Fatalf("StepCount want=7 got=%d", StepCount())
		}
	})
}
