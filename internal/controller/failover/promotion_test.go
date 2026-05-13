/*
Copyright 2026 Keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/*
Copyright 2026 keiailab.
*/

package failover

import (
	"context"
	"errors"
	"strings"
	"testing"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// fakePromoter — plan 을 capture 만 하고 실행은 안 한다. 단위 테스트의 plan
// 결정성 검증용.
type fakePromoter struct {
	captured []PromotionPlan
	err      error
}

func (f *fakePromoter) Execute(_ context.Context, plan PromotionPlan) error {
	f.captured = append(f.captured, plan)
	return f.err
}

func TestBuildPromotionPlan_Steps(t *testing.T) {
	t.Parallel()
	candidate := &postgresv1alpha1.ShardEndpoint{
		Pod:      "pg-1",
		Endpoint: "pg-1.svc.cluster.local:5432",
		Ready:    true,
	}
	plan, err := BuildPromotionPlan("shard-0", candidate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Target.Pod != "pg-1" {
		t.Errorf("Target.Pod=%q want pg-1", plan.Target.Pod)
	}
	if plan.Target.Endpoint != candidate.Endpoint {
		t.Errorf("Target.Endpoint=%q want %q", plan.Target.Endpoint, candidate.Endpoint)
	}
	wantSteps := []PromotionStep{
		StepRemoveStandbySignal,
		StepPgCtlPromote,
		StepWaitNotInRecovery,
		StepUpdateInstanceRole,
	}
	if len(plan.Steps) != len(wantSteps) {
		t.Fatalf("Steps len=%d want %d", len(plan.Steps), len(wantSteps))
	}
	for i, s := range plan.Steps {
		if s != wantSteps[i] {
			t.Errorf("Steps[%d]=%q want %q", i, s, wantSteps[i])
		}
	}
}

func TestBuildPromotionPlan_NilCandidate(t *testing.T) {
	t.Parallel()
	_, err := BuildPromotionPlan("shard-0", nil)
	if !errors.Is(err, ErrNoPromotionTarget) {
		t.Errorf("expected ErrNoPromotionTarget, got %v", err)
	}
}

func TestPromoteFromDecision_HealthyNoOp(t *testing.T) {
	t.Parallel()
	p := &fakePromoter{}
	err := PromoteFromDecision(context.Background(), "shard-0", Decision{Failed: false}, p)
	if err != nil {
		t.Errorf("healthy → expected nil err, got %v", err)
	}
	if len(p.captured) != 0 {
		t.Errorf("healthy → expected 0 calls, got %d", len(p.captured))
	}
}

func TestPromoteFromDecision_ExecutesPlan(t *testing.T) {
	t.Parallel()
	p := &fakePromoter{}
	d := Decision{
		Failed: true,
		Reason: ReasonNoPrimary,
		PromotionCandidate: &postgresv1alpha1.ShardEndpoint{
			Pod: "pg-1", Endpoint: "pg-1.svc:5432", Ready: true,
		},
	}
	if err := PromoteFromDecision(context.Background(), "shard-0", d, p); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(p.captured) != 1 {
		t.Fatalf("expected 1 call, got %d", len(p.captured))
	}
	if p.captured[0].Target.ShardName != "shard-0" {
		t.Errorf("ShardName=%q want shard-0", p.captured[0].Target.ShardName)
	}
}

func TestPromoteFromDecision_NilPromoter(t *testing.T) {
	t.Parallel()
	d := Decision{Failed: true, PromotionCandidate: &postgresv1alpha1.ShardEndpoint{Pod: "pg-1"}}
	err := PromoteFromDecision(context.Background(), "shard-0", d, nil)
	if err == nil || !strings.Contains(err.Error(), "nil Promoter") {
		t.Errorf("expected nil Promoter error, got %v", err)
	}
}

func TestPromoteFromDecision_NoCandidate(t *testing.T) {
	t.Parallel()
	p := &fakePromoter{}
	d := Decision{Failed: true, Reason: ReasonNoEligibleReplica, PromotionCandidate: nil}
	err := PromoteFromDecision(context.Background(), "shard-0", d, p)
	if !errors.Is(err, ErrNoPromotionTarget) {
		t.Errorf("expected ErrNoPromotionTarget wrap, got %v", err)
	}
}

func TestPromoteFromDecision_PromoterErrorWrapped(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("pod exec timeout")
	p := &fakePromoter{err: wantErr}
	d := Decision{
		Failed:             true,
		Reason:             ReasonPrimaryNotReady,
		PromotionCandidate: &postgresv1alpha1.ShardEndpoint{Pod: "pg-1", Ready: true},
	}
	err := PromoteFromDecision(context.Background(), "shard-0", d, p)
	if err == nil {
		t.Fatal("expected wrapped error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected error chain to contain %v, got %v", wantErr, err)
	}
	if !strings.Contains(err.Error(), "shard-0") || !strings.Contains(err.Error(), "pg-1") {
		t.Errorf("wrap context missing shard/pod: %v", err)
	}
}
