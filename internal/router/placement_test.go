/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package router

import (
	"errors"
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestPlacementDrift(t *testing.T) {
	t.Run("Missing + Extra + NotReady drift 모두", func(t *testing.T) {
		spec := []PlacementSpec{
			{ShardID: "shard-a", PreferredZone: "zone-1"},
			{ShardID: "shard-b", PreferredZone: "zone-2"},
		}
		observed := []ObservedShard{
			// shard-a 누락 → Missing
			// shard-b 관찰되었으나 ready=false → NotReady
			{ShardID: "shard-b", Zone: "zone-2", Ready: false},
			// shard-c 는 spec 외 → Extra
			{ShardID: "shard-c", Zone: "zone-3", Ready: true},
		}
		drifts := DetectPlacementDrift(spec, observed, nil)
		reasons := map[PlacementDriftReason]bool{}
		for _, d := range drifts {
			reasons[d.Reason] = true
		}
		for _, want := range []PlacementDriftReason{
			PlacementDriftMissing, PlacementDriftNotReady, PlacementDriftExtra,
		} {
			if !reasons[want] {
				t.Fatalf("expected %s drift, got reasons=%+v", want, reasons)
			}
		}
	})

	t.Run("Zone + Node mismatch", func(t *testing.T) {
		spec := []PlacementSpec{
			{ShardID: "s", PreferredZone: "zone-1", PreferredNode: "node-a"},
		}
		observed := []ObservedShard{
			{ShardID: "s", Zone: "zone-2", Node: "node-b", Ready: true},
		}
		drifts := DetectPlacementDrift(spec, observed, nil)
		reasons := map[PlacementDriftReason]bool{}
		for _, d := range drifts {
			reasons[d.Reason] = true
		}
		if !reasons[PlacementDriftZoneMismatch] || !reasons[PlacementDriftNodeMismatch] {
			t.Fatalf("expected ZoneMismatch + NodeMismatch, got %+v", reasons)
		}
	})

	t.Run("RangeUncovered shardrange ranges[].shard 가 spec 에 없음", func(t *testing.T) {
		spec := []PlacementSpec{{ShardID: "a"}, {ShardID: "b"}}
		observed := []ObservedShard{
			{ShardID: "a", Ready: true}, {ShardID: "b", Ready: true},
		}
		ranges := []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "a"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "PHANTOM"},
		}
		drifts := DetectPlacementDrift(spec, observed, ranges)
		var found bool
		for _, d := range drifts {
			if d.Reason == PlacementDriftRangeUncovered && d.ShardID == "PHANTOM" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected RangeUncovered for PHANTOM, got %+v", drifts)
		}
	})

	t.Run("정상 cluster 0 drift", func(t *testing.T) {
		spec := []PlacementSpec{{ShardID: "a", PreferredZone: "z1"}}
		observed := []ObservedShard{{ShardID: "a", Zone: "z1", Ready: true}}
		drifts := DetectPlacementDrift(spec, observed, nil)
		if HasDrift(drifts) {
			t.Fatalf("expected 0 drift, got %+v", drifts)
		}
	})

	t.Run("결정성: 동일 입력 → 동일 정렬 출력", func(t *testing.T) {
		spec := []PlacementSpec{{ShardID: "z"}, {ShardID: "a"}}
		observed := []ObservedShard{
			{ShardID: "z", Ready: false}, {ShardID: "a", Ready: false},
		}
		a := DetectPlacementDrift(spec, observed, nil)
		b := DetectPlacementDrift(spec, observed, nil)
		if len(a) != len(b) {
			t.Fatalf("len mismatch %d vs %d", len(a), len(b))
		}
		for i := range a {
			if a[i].ShardID != b[i].ShardID || a[i].Reason != b[i].Reason {
				t.Fatalf("non-deterministic at %d: %+v vs %+v", i, a[i], b[i])
			}
		}
	})
}

func TestValidatePlacement(t *testing.T) {
	t.Run("정상", func(t *testing.T) {
		err := ValidatePlacement([]PlacementSpec{
			{ShardID: "a", Weight: 1}, {ShardID: "b", Weight: 2},
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("empty ShardID 거부", func(t *testing.T) {
		err := ValidatePlacement([]PlacementSpec{{ShardID: ""}})
		if !errors.Is(err, ErrPlacementInvalid) {
			t.Fatalf("want ErrPlacementInvalid, got %v", err)
		}
	})

	t.Run("중복 ShardID 거부", func(t *testing.T) {
		err := ValidatePlacement([]PlacementSpec{
			{ShardID: "a"}, {ShardID: "a"},
		})
		if !errors.Is(err, ErrPlacementInvalid) {
			t.Fatalf("want ErrPlacementInvalid, got %v", err)
		}
	})

	t.Run("negative weight 거부", func(t *testing.T) {
		err := ValidatePlacement([]PlacementSpec{{ShardID: "a", Weight: -1}})
		if !errors.Is(err, ErrPlacementInvalid) {
			t.Fatalf("want ErrPlacementInvalid, got %v", err)
		}
	})
}
