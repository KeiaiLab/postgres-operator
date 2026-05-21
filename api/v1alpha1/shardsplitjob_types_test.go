/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShardSplitJob(t *testing.T) {
	t.Run("Phase 전체 11 값 stringify", func(t *testing.T) {
		phases := []ShardSplitJobPhase{
			ShardSplitPhasePending, ShardSplitPhaseSnapshotWAL,
			ShardSplitPhaseBootstrap, ShardSplitPhaseInitialCopy,
			ShardSplitPhaseCDCCatchup, ShardSplitPhaseCutover,
			ShardSplitPhaseRoutingUpdate, ShardSplitPhaseCleanup,
			ShardSplitPhaseCompleted, ShardSplitPhaseFailed,
			ShardSplitPhaseAborted,
		}
		if len(phases) != 11 {
			t.Fatalf("phase count want=11 got=%d", len(phases))
		}
		for _, p := range phases {
			if string(p) == "" {
				t.Fatalf("phase string empty: %v", p)
			}
		}
	})

	t.Run("Direction 2 enum", func(t *testing.T) {
		if ShardSplitDirectionSplit != "split" || ShardSplitDirectionMerge != "merge" {
			t.Fatalf("Direction stringify mismatch")
		}
	})

	t.Run("Spec round-trip", func(t *testing.T) {
		spec := ShardSplitJobSpec{
			Cluster:   "cl-0",
			Keyspace:  "ks",
			Direction: ShardSplitDirectionSplit,
			Sources:   []string{"sh-0"},
			Targets: []ShardSplitTarget{
				{
					ShardID: "sh-0a",
					Ranges:  []ShardRangeEntry{{Lo: "0x0", Hi: "0x7f", Shard: "sh-0a"}},
					Placement: &ShardSplitPlacement{
						PreferredZone: "z-1",
					},
				},
				{
					ShardID: "sh-0b",
					Ranges:  []ShardRangeEntry{{Lo: "0x80", Hi: "0xff", Shard: "sh-0b"}},
				},
			},
			CutoverWindow:    metav1.Duration{Duration: 30 * 1e9},
			CDCMaxLag:        16 * 1024 * 1024,
			AllowForwardOnly: false,
		}
		j := ShardSplitJob{
			ObjectMeta: metav1.ObjectMeta{Name: "split-1", Namespace: "ns"},
			Spec:       spec,
		}
		if j.Spec.Cluster != "cl-0" || j.Spec.Keyspace != "ks" {
			t.Fatalf("round-trip basic field mismatch")
		}
		if len(j.Spec.Sources) != 1 || j.Spec.Sources[0] != "sh-0" {
			t.Fatalf("Sources round-trip mismatch")
		}
		if len(j.Spec.Targets) != 2 {
			t.Fatalf("Targets count round-trip mismatch")
		}
		if j.Spec.Targets[0].Placement == nil ||
			j.Spec.Targets[0].Placement.PreferredZone != "z-1" {
			t.Fatalf("Placement round-trip mismatch")
		}
	})

	t.Run("List wraps Items", func(t *testing.T) {
		list := ShardSplitJobList{
			Items: []ShardSplitJob{
				{ObjectMeta: metav1.ObjectMeta{Name: "j1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "j2"}},
			},
		}
		if len(list.Items) != 2 {
			t.Fatalf("list Items round-trip")
		}
	})

	t.Run("Status round-trip", func(t *testing.T) {
		st := ShardSplitJobStatus{
			Phase:           ShardSplitPhaseCDCCatchup,
			CurrentLagBytes: 8 * 1024 * 1024,
			SnapshotLSN:     "0/3DA43A0",
		}
		if st.Phase != "CDCCatchup" {
			t.Fatalf("Phase round-trip")
		}
		if st.CurrentLagBytes != 8*1024*1024 {
			t.Fatalf("Lag round-trip")
		}
	})
}
