/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"testing"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/controller/failover"
)

func TestClusterFailoverDecisionSkipsStandaloneReplicaCluster(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		Spec: postgresv1alpha1.PostgresClusterSpec{
			Replica: &postgresv1alpha1.ReplicaClusterSpec{
				Enabled: true,
				Source:  "source",
			},
		},
	}
	shards := []postgresv1alpha1.ShardStatus{{
		Name: "shard-0",
		Replicas: []postgresv1alpha1.ShardEndpoint{{
			Pod:      "replica-shard-0-0",
			Endpoint: "replica-shard-0-0.replica-shard-0-headless.default.svc.cluster.local:5432",
			Ready:    true,
		}},
	}}

	shardName, decision := clusterFailoverDecision(cluster, shards)
	if shardName != "" {
		t.Fatalf("shardName = %q, want empty for standalone replica cluster", shardName)
	}
	if decision.Failed || decision.Reason != failover.ReasonNone || decision.PromotionCandidate != nil {
		t.Fatalf("decision = %+v, want no failover for standalone replica cluster", decision)
	}
}
