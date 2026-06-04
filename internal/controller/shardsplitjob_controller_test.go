/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"testing"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

func ssjWith(phase postgresv1alpha1.ShardSplitJobPhase, fwdOnly bool, targets []postgresv1alpha1.ShardSplitTarget) *postgresv1alpha1.ShardSplitJob {
	return &postgresv1alpha1.ShardSplitJob{
		Spec:   postgresv1alpha1.ShardSplitJobSpec{AllowForwardOnly: fwdOnly, Targets: targets},
		Status: postgresv1alpha1.ShardSplitJobStatus{Phase: phase},
	}
}

func twoTargets() []postgresv1alpha1.ShardSplitTarget {
	return []postgresv1alpha1.ShardSplitTarget{
		{ShardID: "t0", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "t0"}}},
		{ShardID: "t1", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x80000000", Hi: "0xffffffff", Shard: "t1"}}},
	}
}

func overlapTargets() []postgresv1alpha1.ShardSplitTarget {
	return []postgresv1alpha1.ShardSplitTarget{
		{ShardID: "t0", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x00000000", Hi: "0x80000000", Shard: "t0"}}},
		{ShardID: "t1", Ranges: []postgresv1alpha1.ShardRangeEntry{{Lo: "0x7fffffff", Hi: "0xffffffff", Shard: "t1"}}},
	}
}

func TestShardSplitJob_nextPhase(t *testing.T) {
	r := &ShardSplitJobReconciler{}
	P := postgresv1alpha1.ShardSplitJobPhase("")
	_ = P
	cases := []struct {
		name string
		ssj  *postgresv1alpha1.ShardSplitJob
		want postgresv1alpha1.ShardSplitJobPhase
	}{
		{"pending valid → SnapshotWAL", ssjWith(postgresv1alpha1.ShardSplitPhasePending, false, twoTargets()), postgresv1alpha1.ShardSplitPhaseSnapshotWAL},
		{"pending overlap → Failed", ssjWith(postgresv1alpha1.ShardSplitPhasePending, false, overlapTargets()), postgresv1alpha1.ShardSplitPhaseFailed},
		{"initialcopy → CDCCatchup", ssjWith(postgresv1alpha1.ShardSplitPhaseInitialCopy, false, twoTargets()), postgresv1alpha1.ShardSplitPhaseCDCCatchup},
		{"cutover reversible → RoutingUpdate", ssjWith(postgresv1alpha1.ShardSplitPhaseCutover, false, twoTargets()), postgresv1alpha1.ShardSplitPhaseRoutingUpdate},
		{"cutover forward-only → Failed (비가역 거부)", ssjWith(postgresv1alpha1.ShardSplitPhaseCutover, true, twoTargets()), postgresv1alpha1.ShardSplitPhaseFailed},
		{"cleanup → Completed", ssjWith(postgresv1alpha1.ShardSplitPhaseCleanup, false, twoTargets()), postgresv1alpha1.ShardSplitPhaseCompleted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := r.nextPhase(tc.ssj)
			if got != tc.want {
				t.Fatalf("nextPhase = %q, want %q", got, tc.want)
			}
		})
	}
}
