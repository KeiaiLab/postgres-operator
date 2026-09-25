/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

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
	d := DetectPrimaryFailure(shard, nil)
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
	d := DetectPrimaryFailure(shard, nil)
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
	d := DetectPrimaryFailure(shard, nil)
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
	d := DetectPrimaryFailure(shard, nil)
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
			got := SelectPromotionCandidate(tc.replicas, nil)
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

// TestFreshestCandidate 는 후보가 수신 위치(WALLSNBytes + LagBytes) 최대인지 본다(설계 §3.3).
// 끊긴 standby 는 receive == replay 라 LagBytes 가 0 이다 — lag 최소를 고르면 가장 낡은
// standby 가 1순위가 된다(2026-09-14 사고).
func TestFreshestCandidate(t *testing.T) {
	t.Parallel()
	const (
		gib    = int64(1) << 30
		latest = 177 * gib // 운영 primary 근처의 절대 위치
		lag    = 4096
	)
	cases := []struct {
		name     string
		replicas []postgresv1alpha1.ShardEndpoint
		receive  map[string]int64
		want     string
	}{
		{
			"streaming beats a disconnected standby with zero lag",
			[]postgresv1alpha1.ShardEndpoint{
				{Pod: "pg-1", Ready: true, LagBytes: 0},
				{Pod: "pg-2", Ready: true, LagBytes: lag},
			},
			map[string]int64{"pg-1": latest - 2*gib, "pg-2": latest + lag},
			"pg-2",
		},
		{
			"both streaming: larger receive position",
			[]postgresv1alpha1.ShardEndpoint{
				{Pod: "pg-1", Ready: true, LagBytes: 0},
				{Pod: "pg-2", Ready: true, LagBytes: lag},
			},
			map[string]int64{"pg-1": latest, "pg-2": latest + lag},
			"pg-2",
		},
		{
			"unknown position loses to a known one",
			[]postgresv1alpha1.ShardEndpoint{
				{Pod: "pg-1", Ready: true, LagBytes: 0},
				{Pod: "pg-2", Ready: true, LagBytes: lag},
				{Pod: "pg-3", Ready: true, LagBytes: 0},
			},
			map[string]int64{"pg-1": -1, "pg-2": latest - 2*gib},
			"pg-2",
		},
		{
			"all unknown: lag then name",
			[]postgresv1alpha1.ShardEndpoint{
				{Pod: "pg-2", Ready: true, LagBytes: 0},
				{Pod: "pg-1", Ready: true, LagBytes: 0},
				{Pod: "pg-0", Ready: true, LagBytes: lag},
			},
			nil,
			"pg-1",
		},
		{
			"tie: lag then name",
			[]postgresv1alpha1.ShardEndpoint{
				{Pod: "pg-3", Ready: true, LagBytes: 0},
				{Pod: "pg-2", Ready: true, LagBytes: 0},
				{Pod: "pg-1", Ready: true, LagBytes: lag},
			},
			map[string]int64{"pg-1": latest, "pg-2": latest, "pg-3": latest},
			"pg-2",
		},
		{
			"not ready excluded even when freshest",
			[]postgresv1alpha1.ShardEndpoint{
				{Pod: "pg-1", Ready: false, LagBytes: 0},
				{Pod: "pg-2", Ready: true, LagBytes: 0},
			},
			map[string]int64{"pg-1": latest, "pg-2": latest - gib},
			"pg-2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := DetectPrimaryFailure(postgresv1alpha1.ShardStatus{Name: "shard-0", Replicas: tc.replicas}, tc.receive)
			if d.PromotionCandidate == nil || d.PromotionCandidate.Pod != tc.want {
				t.Fatalf("candidate = %v, want %s", d.PromotionCandidate, tc.want)
			}
		})
	}
}
