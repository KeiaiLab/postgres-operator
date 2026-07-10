/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
)

func TestApplyClusterConditionsDegradesWhenPreviouslyReadyPrimaryFails(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		Status: postgresv1alpha1.PostgresClusterStatus{
			Phase: postgresv1alpha1.ClusterPhaseReady,
		},
	}
	decision := failover.Decision{
		Failed:  true,
		Reason:  failover.ReasonPrimaryNotReady,
		Message: `shard "shard-0" primary pod "demo-0" readiness=false`,
		PromotionCandidate: &postgresv1alpha1.ShardEndpoint{
			Pod: "demo-1",
		},
	}

	applyClusterConditions(cluster, 1, false, false, nil, false, false, true, decision, true)

	if cluster.Status.Phase != postgresv1alpha1.ClusterPhaseDegraded {
		t.Fatalf("Phase = %q, want Degraded", cluster.Status.Phase)
	}
	ready := meta.FindStatusCondition(cluster.Status.Conditions, ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != string(failover.ReasonPrimaryNotReady) {
		t.Fatalf("Ready condition = %+v, want PrimaryNotReady false", ready)
	}
	failoverReady := meta.FindStatusCondition(cluster.Status.Conditions, ConditionFailoverReady)
	if failoverReady == nil || failoverReady.Status != metav1.ConditionFalse {
		t.Fatalf("FailoverReady condition = %+v, want false", failoverReady)
	}
	if !strings.Contains(failoverReady.Message, "demo-1") {
		t.Fatalf("FailoverReady message = %q, want candidate pod", failoverReady.Message)
	}
}

// TestApplyClusterConditions_PrimaryRoleSyncedReflectsLabelState pins #226 F3:
// ConditionPrimaryRoleSynced must independently expose the "CR status/lease
// says primary is Ready but the instance-role=primary label has not converged"
// contradiction, without being folded into (or changing the meaning of)
// ShardsReady/Ready.
func TestApplyClusterConditions_PrimaryRoleSyncedReflectsLabelState(t *testing.T) {
	t.Parallel()

	decision := failover.Decision{Reason: failover.ReasonNone}

	synced := &postgresv1alpha1.PostgresCluster{}
	applyClusterConditions(synced, 1, true, false, nil, false, false, false, decision, true)
	if c := meta.FindStatusCondition(synced.Status.Conditions, ConditionPrimaryRoleSynced); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("PrimaryRoleSynced = %+v, want True when primaryRoleLabelsSynced=true", c)
	}
	if shardsReady := meta.FindStatusCondition(synced.Status.Conditions, ConditionShardsReady); shardsReady == nil || shardsReady.Status != metav1.ConditionTrue {
		t.Fatalf("ShardsReady = %+v, want unaffected True (allShardPrimaryReady=true)", shardsReady)
	}

	pending := &postgresv1alpha1.PostgresCluster{}
	applyClusterConditions(pending, 1, true, false, nil, false, false, false, decision, false)
	c := meta.FindStatusCondition(pending.Status.Conditions, ConditionPrimaryRoleSynced)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != ReasonRoleLabelPending {
		t.Fatalf("PrimaryRoleSynced = %+v, want False/RoleLabelPending when primaryRoleLabelsSynced=false", c)
	}
	// ShardsReady must stay True here — the label gap is a distinct signal, not
	// a redefinition of what ShardsReady already means for existing callers.
	if shardsReady := meta.FindStatusCondition(pending.Status.Conditions, ConditionShardsReady); shardsReady == nil || shardsReady.Status != metav1.ConditionTrue {
		t.Fatalf("ShardsReady = %+v, want unaffected True even when PrimaryRoleSynced=False", shardsReady)
	}
}
