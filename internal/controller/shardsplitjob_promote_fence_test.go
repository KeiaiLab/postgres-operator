/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// TestSourceObservationExcluded 는 P-B.6 Promote fence gate 를 결정론(fake client) 으로
// 검증한다 — envtest 매니저 경합 없이 sourceObservationExcluded 로직만 격리.
func TestSourceObservationExcluded(t *testing.T) {
	scheme := newScheme(t)
	ns := metav1.NamespaceDefault
	ssj := &postgresv1alpha1.ShardSplitJob{
		ObjectMeta: metav1.ObjectMeta{Name: "ssj", Namespace: ns},
		Spec: postgresv1alpha1.ShardSplitJobSpec{
			Cluster: "demo",
			Sources: []string{"shard-0"},
		},
	}
	clusterWith := func(shards []postgresv1alpha1.ShardStatus) *postgresv1alpha1.PostgresCluster {
		return &postgresv1alpha1.PostgresCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
			Status:     postgresv1alpha1.PostgresClusterStatus{Shards: shards},
		}
	}

	tests := []struct {
		name    string
		cluster *postgresv1alpha1.PostgresCluster
		want    bool
	}{
		{
			name:    "source observed with Ready primary → fence pending",
			cluster: clusterWith([]postgresv1alpha1.ShardStatus{{Name: "shard-0", Primary: &postgresv1alpha1.ShardEndpoint{Ready: true}}}),
			want:    false,
		},
		{
			name:    "source primary not Ready → fence satisfied",
			cluster: clusterWith([]postgresv1alpha1.ShardStatus{{Name: "shard-0", Primary: &postgresv1alpha1.ShardEndpoint{Ready: false}}}),
			want:    true,
		},
		{
			name:    "source absent from status → fence satisfied",
			cluster: clusterWith([]postgresv1alpha1.ShardStatus{{Name: "t1", Primary: &postgresv1alpha1.ShardEndpoint{Ready: true}}}),
			want:    true,
		},
		{
			name:    "empty status → fence satisfied",
			cluster: clusterWith(nil),
			want:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.cluster).Build()
			r := &ShardSplitJobReconciler{Client: c, Scheme: scheme}
			got, reason, err := r.sourceObservationExcluded(context.Background(), ssj)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("excluded = %v (reason=%q), want %v", got, reason, tc.want)
			}
			if !got && reason == "" {
				t.Fatalf("fence-pending must carry a reason")
			}
		})
	}
}

// TestSourceObservationExcluded_ClusterNotFound 는 PostgresCluster CR 부재 시 fence 가
// 충족(true)됨을 확인한다 — 관측 대상 부재 = split-brain 위험 없음(격리 테스트 안전).
func TestSourceObservationExcluded_ClusterNotFound(t *testing.T) {
	scheme := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build() // cluster 미생성.
	r := &ShardSplitJobReconciler{Client: c, Scheme: scheme}
	ssj := &postgresv1alpha1.ShardSplitJob{
		ObjectMeta: metav1.ObjectMeta{Name: "ssj", Namespace: "default"},
		Spec:       postgresv1alpha1.ShardSplitJobSpec{Cluster: "absent", Sources: []string{"shard-0"}},
	}
	got, _, err := r.sourceObservationExcluded(context.Background(), ssj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("cluster not found should satisfy the fence (true)")
	}
}
