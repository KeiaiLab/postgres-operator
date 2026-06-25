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

	applyClusterConditions(cluster, 1, false, false, nil, false, false, true, decision)

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
