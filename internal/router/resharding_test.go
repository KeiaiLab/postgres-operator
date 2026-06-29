/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"errors"
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

func entry(lo, hi, shard string) v1alpha1.ShardRangeEntry {
	return v1alpha1.ShardRangeEntry{Lo: lo, Hi: hi, Shard: shard}
}

func TestValidateSplitPlan(t *testing.T) {
	full := []v1alpha1.ShardRangeEntry{entry("0x00000000", "0xffffffff", "s0")}

	cases := []struct {
		name    string
		sources []v1alpha1.ShardRangeEntry
		targets []v1alpha1.ShardRangeEntry
		wantErr error
	}{
		{
			name:    "valid 2-way split (data 보존)",
			sources: full,
			targets: []v1alpha1.ShardRangeEntry{
				entry("0x00000000", "0x7fffffff", "t0"),
				entry("0x80000000", "0xffffffff", "t1"),
			},
			wantErr: nil,
		},
		{
			name:    "overlap (키 중복)",
			sources: full,
			targets: []v1alpha1.ShardRangeEntry{
				entry("0x00000000", "0x80000000", "t0"),
				entry("0x7fffffff", "0xffffffff", "t1"),
			},
			wantErr: ErrSplitPlanOverlap,
		},
		{
			name:    "gap (키 유실)",
			sources: full,
			targets: []v1alpha1.ShardRangeEntry{
				entry("0x00000000", "0x7ffffffe", "t0"),
				entry("0x80000000", "0xffffffff", "t1"),
			},
			wantErr: ErrSplitPlanGap,
		},
		{
			name:    "coverage mismatch (target 이 source 보다 좁음)",
			sources: full,
			targets: []v1alpha1.ShardRangeEntry{
				entry("0x00000000", "0x7fffffff", "t0"),
			},
			wantErr: ErrSplitPlanCoverage,
		},
		{
			name: "valid merge (역방향: 2 → 1)",
			sources: []v1alpha1.ShardRangeEntry{
				entry("0x00000000", "0x7fffffff", "s0"),
				entry("0x80000000", "0xffffffff", "s1"),
			},
			targets: full,
			wantErr: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSplitPlan(tc.sources, tc.targets)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateSplitPlan = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateSplitPlan = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestHexSuccessor(t *testing.T) {
	cases := map[string]string{
		"0x00000000": "0x00000001",
		"0x00000009": "0x0000000a",
		"0x0000000f": "0x00000010",
		"0x7fffffff": "0x80000000",
		"0xfffffffe": "0xffffffff",
	}
	for in, want := range cases {
		if got := hexSuccessor(in); got != want {
			t.Errorf("hexSuccessor(%s) = %s, want %s", in, got, want)
		}
	}
}
