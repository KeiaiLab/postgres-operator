/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

	got := primaryEndpointForShard(cluster, 0, "failover-shard-0-headless")
	want := "failover-shard-0-0.failover-shard-0-headless.pg-failover-e2e.svc.cluster.local:5432"
	if got != want {
		t.Fatalf("primary endpoint = %q, want %q", got, want)
	}
}

// 단일 멤버 shard 도 ordinal-0 DNS 로 시작한다 — 나중에 replica 를 늘려도 새 멤버가
// basebackup 원천을 안다(런타임 CM 첫 값은 승격 전까지 그대로다).
func TestPrimaryEndpointForShard_SingleMemberUsesOrdinalZeroDNS(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "solo", Namespace: "pg"},
	}

	got := primaryEndpointForShard(cluster, 0, "solo-shard-0-headless")
	want := "solo-shard-0-0.solo-shard-0-headless.pg.svc.cluster.local:5432"
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

	got := primaryEndpointForShard(cluster, 0, "failover-shard-0-headless")
	want := "failover-shard-0-1.failover-shard-0-headless.pg-failover-e2e.svc.cluster.local:5432"
	if got != want {
		t.Fatalf("primary endpoint = %q, want observed promoted endpoint %q", got, want)
	}
}

// 복제 클러스터는 로컬 승격이 없어 외부 원천을 템플릿에 인라인한다(런타임 CM 없음).
func TestReplicaClusterInlinesExternalEndpoint(t *testing.T) {
	t.Parallel()

	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "replica", Namespace: "pg-external-e2e"},
		Spec: postgresv1alpha1.PostgresClusterSpec{
			ExternalClusters: []postgresv1alpha1.ExternalClusterSpec{{
				Name:                 "source",
				ConnectionParameters: map[string]string{"host": "source-rw.data.svc", "port": "5432"},
			}},
			Bootstrap: &postgresv1alpha1.BootstrapSpec{
				PgBaseBackup: &postgresv1alpha1.PgBaseBackupBootstrapSpec{Source: "source"},
			},
			Replica: &postgresv1alpha1.ReplicaClusterSpec{Enabled: true, Source: "source"},
		},
	}
	sts := buildPGStatefulSet(
		cluster, "replica-shard-0", "replica-shard-0-headless", 0,
		"example.com/postgres:18", "replica-shard-0-config", "18", 1,
		postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
		corev1.ResourceRequirements{}, "", "test-config-hash", "",
	)

	const want = "source-rw.data.svc:5432"
	pod := sts.Spec.Template.Spec
	for _, ctr := range []corev1.Container{pod.InitContainers[0], pod.Containers[0]} {
		env := envByNameOf(ctr)
		got := env[runtimeKeyPrimaryEndpoint]
		if got.ValueFrom != nil || got.Value != want {
			t.Errorf("%s PRIMARY_ENDPOINT = %+v, want inline %q", ctr.Name, got, want)
		}
		if _, ok := env[runtimeKeyPromotionEpoch]; ok {
			t.Errorf("%s must not reference a runtime ConfigMap in replica cluster mode", ctr.Name)
		}
	}
}
