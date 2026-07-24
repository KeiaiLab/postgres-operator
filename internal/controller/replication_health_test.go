/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// TestReconcileReplicationHealth pins the #220 replication observability fix: a
// disconnected/frozen replica must be surfaced as ReplicationHealthy=False (the
// incident: a 7-day-frozen replica never appeared in status or any alert). A
// replica far behind the primary's WAL position, or a not-ready replica, flips the
// condition; a healthy set keeps it True; a replica-less cluster is NotApplicable.
func TestReconcileReplicationHealth(t *testing.T) {
	t.Parallel()
	const ns = "default"
	scheme := newScheme(t)
	ctx := context.Background()
	const oneGiB = int64(1) << 30

	mkPod := func(name string, walPos int64) *corev1.Pod {
		raw, err := json.Marshal(statusapi.Status{Role: statusapi.RoleReplica, WALLSNBytes: walPos})
		if err != nil {
			t.Fatalf("marshal status: %v", err)
		}
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Annotations: map[string]string{statusapi.AnnotationKey: string(raw)},
		}}
	}

	cases := []struct {
		name       string
		shards     []postgresv1alpha1.ShardStatus
		pods       []client.Object
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name: "replica following primary → healthy",
			shards: []postgresv1alpha1.ShardStatus{{
				Name:     "shard-0",
				Primary:  &postgresv1alpha1.ShardEndpoint{Pod: "demo-shard-0-0", Ready: true},
				Replicas: []postgresv1alpha1.ShardEndpoint{{Pod: "demo-shard-0-1", Ready: true}},
			}},
			pods:       []client.Object{mkPod("demo-shard-0-0", 10*oneGiB), mkPod("demo-shard-0-1", 10*oneGiB)},
			wantStatus: metav1.ConditionTrue,
			wantReason: ReasonReplicasHealthy,
		},
		{
			name: "replica frozen far behind primary → degraded",
			shards: []postgresv1alpha1.ShardStatus{{
				Name:     "shard-0",
				Primary:  &postgresv1alpha1.ShardEndpoint{Pod: "demo-shard-0-0", Ready: true},
				Replicas: []postgresv1alpha1.ShardEndpoint{{Pod: "demo-shard-0-1", Ready: true}},
			}},
			pods:       []client.Object{mkPod("demo-shard-0-0", 10*oneGiB), mkPod("demo-shard-0-1", 2*oneGiB)},
			wantStatus: metav1.ConditionFalse,
			wantReason: ReasonReplicaDisconnected,
		},
		{
			name: "replica not ready (fenced) → degraded",
			shards: []postgresv1alpha1.ShardStatus{{
				Name:    "shard-0",
				Primary: &postgresv1alpha1.ShardEndpoint{Pod: "demo-shard-0-0", Ready: true},
				Replicas: []postgresv1alpha1.ShardEndpoint{
					{Pod: "demo-shard-0-1", Ready: false, Reason: fencedMemberReason},
				},
			}},
			pods:       []client.Object{mkPod("demo-shard-0-0", 10*oneGiB)},
			wantStatus: metav1.ConditionFalse,
			wantReason: ReasonReplicaDisconnected,
		},
		{
			name: "no replicas configured → not applicable",
			shards: []postgresv1alpha1.ShardStatus{{
				Name:    "shard-0",
				Primary: &postgresv1alpha1.ShardEndpoint{Pod: "demo-shard-0-0", Ready: true},
			}},
			pods:       []client.Object{mkPod("demo-shard-0-0", 10*oneGiB)},
			wantStatus: metav1.ConditionTrue,
			wantReason: ReasonReplicationNotApplic,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &postgresv1alpha1.PostgresCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
			}
			objs := append([]client.Object{cluster}, tc.pods...)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
			r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

			r.reconcileReplicationHealth(ctx, cluster, tc.shards)

			cond := meta.FindStatusCondition(cluster.Status.Conditions, ConditionReplicationHealthy)
			if cond == nil {
				t.Fatal("ReplicationHealthy condition not set")
			}
			if cond.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s (message=%q)", cond.Status, tc.wantStatus, cond.Message)
			}
			if cond.Reason != tc.wantReason {
				t.Errorf("reason = %s, want %s", cond.Reason, tc.wantReason)
			}
		})
	}
}
