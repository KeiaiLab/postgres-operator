/*
Copyright 2026 keiailab.
*/

package failover

import (
	"strings"
	"testing"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestDetectPrimaryFailure_HealthyPrimary(t *testing.T) {
	t.Parallel()
	shard := postgresv1alpha1.ShardStatus{
		Name: "shard-0",
		Primary: &postgresv1alpha1.ShardEndpoint{
			Pod: "pg-0", Ready: true,
		},
		Replicas: []postgresv1alpha1.ShardEndpoint{
			{Pod: "pg-1", Ready: true, LagBytes: 100},
		},
	}
	d := DetectPrimaryFailure(shard)
	if d.Failed {
		t.Errorf("healthy primary should not be Failed, got %+v", d)
	}
	if d.Reason != ReasonNone {
		t.Errorf("Reason=%q want %q", d.Reason, ReasonNone)
	}
	if d.PromotionCandidate != nil {
		t.Errorf("healthy primary should yield nil candidate, got %v", d.PromotionCandidate)
	}
}

func TestDetectPrimaryFailure_NoPrimary(t *testing.T) {
	t.Parallel()
	shard := postgresv1alpha1.ShardStatus{
		Name:    "shard-0",
		Primary: nil,
		Replicas: []postgresv1alpha1.ShardEndpoint{
			{Pod: "pg-1", Ready: true, LagBytes: 50},
			{Pod: "pg-2", Ready: true, LagBytes: 20},
		},
	}
	d := DetectPrimaryFailure(shard)
	if !d.Failed {
		t.Fatal("missing primary should be Failed")
	}
	if d.Reason != ReasonNoPrimary {
		t.Errorf("Reason=%q want %q", d.Reason, ReasonNoPrimary)
	}
	if d.PromotionCandidate == nil || d.PromotionCandidate.Pod != "pg-2" {
		t.Errorf("expected pg-2 (lag=20) as candidate, got %v", d.PromotionCandidate)
	}
}

func TestDetectPrimaryFailure_NotReady(t *testing.T) {
	t.Parallel()
	shard := postgresv1alpha1.ShardStatus{
		Name: "shard-0",
		Primary: &postgresv1alpha1.ShardEndpoint{
			Pod: "pg-0", Ready: false,
		},
		Replicas: []postgresv1alpha1.ShardEndpoint{
			{Pod: "pg-1", Ready: true, LagBytes: 0},
		},
	}
	d := DetectPrimaryFailure(shard)
	if !d.Failed {
		t.Fatal("not-ready primary should be Failed")
	}
	if d.Reason != ReasonPrimaryNotReady {
		t.Errorf("Reason=%q want %q", d.Reason, ReasonPrimaryNotReady)
	}
	if !strings.Contains(d.Message, "pg-0") {
		t.Errorf("message should mention pod name, got %q", d.Message)
	}
}

func TestDetectPrimaryFailure_NoEligibleReplica(t *testing.T) {
	t.Parallel()
	shard := postgresv1alpha1.ShardStatus{
		Name:    "shard-0",
		Primary: nil,
		Replicas: []postgresv1alpha1.ShardEndpoint{
			{Pod: "pg-1", Ready: false}, // not ready
		},
	}
	d := DetectPrimaryFailure(shard)
	if !d.Failed {
		t.Fatal("missing primary should be Failed even without candidate")
	}
	if d.Reason != ReasonNoEligibleReplica {
		t.Errorf("Reason=%q want %q", d.Reason, ReasonNoEligibleReplica)
	}
	if d.PromotionCandidate != nil {
		t.Errorf("no ready replica → Candidate must be nil, got %v", d.PromotionCandidate)
	}
	if !strings.Contains(d.Message, "manual intervention") {
		t.Errorf("message should hint manual intervention, got %q", d.Message)
	}
}

func TestSelectPromotionCandidate_OrdersByLagThenName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		replicas []postgresv1alpha1.ShardEndpoint
		want     string // empty = nil expected
	}{
		{
			"empty → nil",
			nil,
			"",
		},
		{
			"all not ready → nil",
			[]postgresv1alpha1.ShardEndpoint{
				{Pod: "a", Ready: false},
				{Pod: "b", Ready: false},
			},
			"",
		},
		{
			"lag 우선",
			[]postgresv1alpha1.ShardEndpoint{
				{Pod: "a", Ready: true, LagBytes: 500},
				{Pod: "b", Ready: true, LagBytes: 100},
				{Pod: "c", Ready: true, LagBytes: 300},
			},
			"b",
		},
		{
			"동률 lag → 사전순",
			[]postgresv1alpha1.ShardEndpoint{
				{Pod: "z", Ready: true, LagBytes: 0},
				{Pod: "a", Ready: true, LagBytes: 0},
				{Pod: "m", Ready: true, LagBytes: 0},
			},
			"a",
		},
		{
			"not-ready 제외",
			[]postgresv1alpha1.ShardEndpoint{
				{Pod: "lowlag", Ready: false, LagBytes: 0},
				{Pod: "ready", Ready: true, LagBytes: 999},
			},
			"ready",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SelectPromotionCandidate(tc.replicas)
			if tc.want == "" {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected pod %q, got nil", tc.want)
			}
			if got.Pod != tc.want {
				t.Errorf("Pod=%q want %q", got.Pod, tc.want)
			}
		})
	}
}
