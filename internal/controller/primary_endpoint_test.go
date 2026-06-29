/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestPrimaryEndpointForShard_InitialHAUsesOrdinalZeroDNS(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "failover", Namespace: "pg-failover-e2e"},
		Spec: postgresv1alpha1.PostgresClusterSpec{
			Shards: postgresv1alpha1.ShardsSpec{Replicas: 1},
		},
	}

	got := primaryEndpointForShard(cluster, 0, "failover-shard-0-headless", nil, false)
	want := "failover-shard-0-0.failover-shard-0-headless.pg-failover-e2e.svc.cluster.local:5432"
	if got != want {
		t.Fatalf("primary endpoint = %q, want %q", got, want)
	}
}

func TestPrimaryEndpointForShard_PreservesObservedPromotedPrimary(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "failover", Namespace: "pg-failover-e2e"},
		Spec: postgresv1alpha1.PostgresClusterSpec{
			Shards: postgresv1alpha1.ShardsSpec{Replicas: 1},
		},
		Status: postgresv1alpha1.PostgresClusterStatus{
			Shards: []postgresv1alpha1.ShardStatus{{
				Ordinal: 0,
				Primary: &postgresv1alpha1.ShardEndpoint{
					Pod:      "failover-shard-0-1",
					Endpoint: "failover-shard-0-1.failover-shard-0-headless.pg-failover-e2e.svc.cluster.local:5432",
					Ready:    true,
				},
			}},
		},
	}

	got := primaryEndpointForShard(cluster, 0, "failover-shard-0-headless", nil, false)
	want := "failover-shard-0-1.failover-shard-0-headless.pg-failover-e2e.svc.cluster.local:5432"
	if got != want {
		t.Fatalf("primary endpoint = %q, want observed promoted endpoint %q", got, want)
	}
}

func TestPrimaryEndpointForShard_StandaloneReplicaUsesExternalEndpoint(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "replica", Namespace: "pg-external-e2e"},
	}
	replicaBootstrap := &replicaBootstrapConfig{
		Endpoint: "source-rw.data.svc:5432",
	}

	got := primaryEndpointForShard(cluster, 0, "replica-shard-0-headless", replicaBootstrap, false)
	if got != replicaBootstrap.Endpoint {
		t.Fatalf("primary endpoint = %q, want external endpoint %q", got, replicaBootstrap.Endpoint)
	}
}
