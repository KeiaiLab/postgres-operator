/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package failover

import (
	"testing"
	"time"
)

// TestPVCFenceRunbook 은 DecidePVCFence 의 4 결정 분기를 cover 한다 (D.1.1).
func TestPVCFenceRunbook(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	renewFresh := now.Add(-5 * time.Second)
	renewStale := now.Add(-60 * time.Second)

	t.Run("CSI multi-attach 즉시 fence", func(t *testing.T) {
		decisions := DecidePVCFence(PVCFenceInput{
			PVCName: "data-cl-0",
			MountedPods: []PVCFenceMountedPod{
				{Name: "cl-0-0", InstanceRole: "primary", Ready: true},
			},
			LeaseHolderIdentity:             "cl-0-0",
			LeaseRenewTime:                  renewFresh,
			LeaseDurationSeconds:            15,
			Now:                             now,
			CSIControllerReportsMultiAttach: true,
		})
		if len(decisions) != 1 || !decisions[0].ShouldFence ||
			decisions[0].Reason != PVCFenceReasonMultiAttach {
			t.Fatalf("expected MultiAttach fence, got %+v", decisions)
		}
	})

	t.Run("split-brain 2 primary 중 holder 보존 + 나머지 fence", func(t *testing.T) {
		decisions := DecidePVCFence(PVCFenceInput{
			PVCName: "data-cl-0",
			MountedPods: []PVCFenceMountedPod{
				{Name: "cl-0-0", InstanceRole: "primary", Ready: true},
				{Name: "cl-0-1", InstanceRole: "primary", Ready: true},
			},
			LeaseHolderIdentity:  "cl-0-0",
			LeaseRenewTime:       renewFresh,
			LeaseDurationSeconds: 15,
			Now:                  now,
		})
		if len(decisions) != 2 {
			t.Fatalf("expected 2 decisions, got %d", len(decisions))
		}
		var holderD, otherD *PVCFenceDecision
		for i := range decisions {
			if decisions[i].PodName == "cl-0-0" {
				holderD = &decisions[i]
			} else {
				otherD = &decisions[i]
			}
		}
		if holderD == nil || holderD.ShouldFence {
			t.Fatalf("holder pod must be preserved, got %+v", holderD)
		}
		if otherD == nil || !otherD.ShouldFence || otherD.Reason != PVCFenceReasonSplitBrain {
			t.Fatalf("non-holder primary must be fenced (SplitBrain), got %+v", otherD)
		}
	})

	t.Run("lease stale + primary 불일치 PromotionRace fence", func(t *testing.T) {
		decisions := DecidePVCFence(PVCFenceInput{
			PVCName: "data-cl-0",
			MountedPods: []PVCFenceMountedPod{
				{Name: "cl-0-1", InstanceRole: "primary", Ready: true},
			},
			LeaseHolderIdentity:  "cl-0-0",
			LeaseRenewTime:       renewStale,
			LeaseDurationSeconds: 15,
			Now:                  now,
		})
		if len(decisions) != 1 || !decisions[0].ShouldFence ||
			decisions[0].Reason != PVCFenceReasonStaleLease {
			t.Fatalf("expected StaleLease fence, got %+v", decisions)
		}
	})

	t.Run("정상 1 primary holder 일치 fence 없음", func(t *testing.T) {
		decisions := DecidePVCFence(PVCFenceInput{
			PVCName: "data-cl-0",
			MountedPods: []PVCFenceMountedPod{
				{Name: "cl-0-0", InstanceRole: "primary", Ready: true},
				{Name: "cl-0-1", InstanceRole: "replica", Ready: true},
			},
			LeaseHolderIdentity:  "cl-0-0",
			LeaseRenewTime:       renewFresh,
			LeaseDurationSeconds: 15,
			Now:                  now,
		})
		for _, d := range decisions {
			if d.ShouldFence {
				t.Fatalf("no fence expected, got %+v", d)
			}
		}
	})

	t.Run("leaseStale helper renewTime zero 이면 false", func(t *testing.T) {
		if leaseStale(PVCFenceInput{LeaseRenewTime: time.Time{}, LeaseDurationSeconds: 15, Now: now}) {
			t.Fatalf("zero renewTime must not be considered stale")
		}
	})
}
