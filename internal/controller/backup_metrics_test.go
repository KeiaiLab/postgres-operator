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

package controller

import (
	"testing"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const metricsDeleteNamespace = "metrics-delete-ns"

func TestObserveBackupJobMetricsSetsOnlyCurrentPhase(t *testing.T) {
	bj := &postgresv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "metrics-ns",
			Name:      "metrics-backup",
		},
		Spec: postgresv1alpha1.BackupJobSpec{
			Cluster: postgresv1alpha1.BackupClusterRef{Name: "metrics-cluster"},
			Tool:    "pgbackrest",
			Type:    "full",
		},
		Status: postgresv1alpha1.BackupJobStatus{Phase: postgresv1alpha1.BackupJobFailed},
	}
	defer DeleteBackupJobMetricsFor(bj.Namespace, bj.Name)

	ObserveBackupJobMetrics(bj)

	if got := testutil.ToFloat64(MetricBackupJobPhase.WithLabelValues(
		"metrics-ns", "metrics-backup", "metrics-cluster", "pgbackrest", "full", "Failed",
	)); got != 1 {
		t.Fatalf("Failed phase gauge = %v, want 1", got)
	}
	if got := testutil.ToFloat64(MetricBackupJobPhase.WithLabelValues(
		"metrics-ns", "metrics-backup", "metrics-cluster", "pgbackrest", "full", "Succeeded",
	)); got != 0 {
		t.Fatalf("Succeeded phase gauge = %v, want 0", got)
	}
}

func TestDeleteBackupJobMetricsForRemovesPhaseSeries(t *testing.T) {
	namespace := metricsDeleteNamespace
	name := "metrics-delete-backup"
	defer DeleteBackupJobMetricsFor(namespace, name)
	bj := &postgresv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: postgresv1alpha1.BackupJobSpec{
			Cluster: postgresv1alpha1.BackupClusterRef{Name: "metrics-cluster"},
			Tool:    "pgbackrest",
			Type:    "full",
		},
		Status: postgresv1alpha1.BackupJobStatus{Phase: postgresv1alpha1.BackupJobSucceeded},
	}

	ObserveBackupJobMetrics(bj)
	DeleteBackupJobMetricsFor(namespace, name)

	if got := testutil.ToFloat64(MetricBackupJobPhase.WithLabelValues(
		namespace, name, "metrics-cluster", "pgbackrest", "full", "Succeeded",
	)); got != 0 {
		t.Fatalf("deleted phase gauge = %v, want 0", got)
	}
}

func TestObservePoolerMetricsSetsOnlyCurrentPhase(t *testing.T) {
	pooler := &postgresv1alpha1.Pooler{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "metrics-ns",
			Name:      "metrics-pooler",
		},
		Spec: postgresv1alpha1.PoolerSpec{
			Cluster: postgresv1alpha1.PoolerClusterRef{Name: "metrics-cluster"},
			Type:    postgresv1alpha1.PoolerTypeRW,
		},
		Status: postgresv1alpha1.PoolerStatus{Phase: postgresv1alpha1.PoolerFailed},
	}
	defer DeletePoolerMetricsFor(pooler.Namespace, pooler.Name)

	ObservePoolerMetrics(pooler)

	if got := testutil.ToFloat64(MetricPoolerPhase.WithLabelValues(
		"metrics-ns", "metrics-pooler", "metrics-cluster", "rw", "Failed",
	)); got != 1 {
		t.Fatalf("Failed phase gauge = %v, want 1", got)
	}
	if got := testutil.ToFloat64(MetricPoolerPhase.WithLabelValues(
		"metrics-ns", "metrics-pooler", "metrics-cluster", "rw", "Ready",
	)); got != 0 {
		t.Fatalf("Ready phase gauge = %v, want 0", got)
	}
}

func TestDeletePoolerMetricsForRemovesPhaseSeries(t *testing.T) {
	namespace := metricsDeleteNamespace
	name := "metrics-delete-pooler"
	defer DeletePoolerMetricsFor(namespace, name)
	pooler := &postgresv1alpha1.Pooler{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: postgresv1alpha1.PoolerSpec{
			Cluster: postgresv1alpha1.PoolerClusterRef{Name: "metrics-cluster"},
			Type:    postgresv1alpha1.PoolerTypeRO,
		},
		Status: postgresv1alpha1.PoolerStatus{Phase: postgresv1alpha1.PoolerReady},
	}

	ObservePoolerMetrics(pooler)
	DeletePoolerMetricsFor(namespace, name)

	if got := testutil.ToFloat64(MetricPoolerPhase.WithLabelValues(
		namespace, name, "metrics-cluster", "ro", "Ready",
	)); got != 0 {
		t.Fatalf("deleted phase gauge = %v, want 0", got)
	}
}

func TestObservePostgresClusterMetricsExportsReplicationLagBytes(t *testing.T) {
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: "metrics-ns", Name: "metrics-cluster"},
		Status: postgresv1alpha1.PostgresClusterStatus{
			Shards: []postgresv1alpha1.ShardStatus{{
				Name: "shard-0",
				Primary: &postgresv1alpha1.ShardEndpoint{
					Pod:      "metrics-cluster-shard-0-0",
					LagBytes: 0,
				},
				Replicas: []postgresv1alpha1.ShardEndpoint{{
					Pod:      "metrics-cluster-shard-0-1",
					LagBytes: 4096,
				}},
			}},
		},
	}
	defer DeleteMetricsFor(cluster.Namespace, cluster.Name)

	ObservePostgresClusterMetrics(cluster)

	if got := testutil.ToFloat64(MetricPostgresClusterReplicationLagBytes.WithLabelValues(
		"metrics-ns", "metrics-cluster", "shard-0", "metrics-cluster-shard-0-1", "replica",
	)); got != 4096 {
		t.Fatalf("replica lag gauge = %v, want 4096", got)
	}
	if got := testutil.ToFloat64(MetricPostgresClusterReplicationLagBytes.WithLabelValues(
		"metrics-ns", "metrics-cluster", "shard-0", "metrics-cluster-shard-0-0", "primary",
	)); got != 0 {
		t.Fatalf("primary lag gauge = %v, want 0", got)
	}
}

func TestDeleteMetricsForRemovesReplicationLagSeries(t *testing.T) {
	namespace := metricsDeleteNamespace
	name := "metrics-delete-cluster"
	defer DeleteMetricsFor(namespace, name)
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Status: postgresv1alpha1.PostgresClusterStatus{
			Shards: []postgresv1alpha1.ShardStatus{{
				Name: "shard-0",
				Replicas: []postgresv1alpha1.ShardEndpoint{{
					Pod:      "metrics-delete-cluster-shard-0-1",
					LagBytes: 8192,
				}},
			}},
		},
	}

	ObservePostgresClusterMetrics(cluster)
	DeleteMetricsFor(namespace, name)

	if got := testutil.ToFloat64(MetricPostgresClusterReplicationLagBytes.WithLabelValues(
		namespace, name, "shard-0", "metrics-delete-cluster-shard-0-1", "replica",
	)); got != 0 {
		t.Fatalf("deleted lag gauge = %v, want 0", got)
	}
}
