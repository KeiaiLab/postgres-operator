/*
Copyright 2026 Keiailab.
*/

// Package controller — Prometheus metrics 정의.
//
// controller-runtime 의 글로벌 metrics registry 자동 등록.
// SLO 추적 (p50/p95/p99 reconcile latency).
package controller

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

const metricSubsystem = "postgrescluster"

var labelNamespaceName = []string{"namespace", "name"}
var backupJobPhaseLabelValues = []postgresv1alpha1.BackupJobPhase{
	postgresv1alpha1.BackupJobPending,
	postgresv1alpha1.BackupJobRunning,
	postgresv1alpha1.BackupJobSucceeded,
	postgresv1alpha1.BackupJobFailed,
}
var poolerPhaseLabelValues = []postgresv1alpha1.PoolerPhase{
	postgresv1alpha1.PoolerPending,
	postgresv1alpha1.PoolerReady,
	postgresv1alpha1.PoolerFailed,
}

var (
	// MetricReconcileTotal — Reconcile 호출 횟수.
	MetricReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: metricSubsystem,
			Name:      "reconcile_total",
			Help:      "Total Reconcile invocations",
		},
		labelNamespaceName,
	)

	// MetricReconcileLatency — wall-clock duration. SLO p50/p95/p99 산출.
	// Buckets 5ms~30s — typical reconcile + STS/PVC API roundtrip 범위.
	MetricReconcileLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: metricSubsystem,
			Name:      "reconcile_duration_seconds",
			Help:      "Reconcile function wall-clock duration in seconds",
			Buckets: []float64{
				0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0,
			},
		},
		[]string{"namespace", "name", "result"}, // result: success | error
	)

	// MetricReconcileErrors — component 별 reconcile 실패.
	MetricReconcileErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: metricSubsystem,
			Name:      "reconcile_errors_total",
			Help:      "Total Reconcile component failures",
		},
		[]string{"namespace", "name", "component"},
	)

	// MetricBackupJobPhase — BackupJob phase 를 one-hot gauge 로 노출한다.
	// PrometheusRule 의 backup failure alert 가 실제 operator metric 을 보게 한다.
	MetricBackupJobPhase = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "postgres_operator",
			Subsystem: "backupjob",
			Name:      "phase",
			Help:      "BackupJob phase as a one-hot gauge labeled by phase",
		},
		[]string{"namespace", "name", "cluster", "tool", "type", "phase"},
	)

	// MetricPoolerPhase — Pooler phase 를 one-hot gauge 로 노출한다.
	// PgBouncer exporter 자체가 죽었을 때도 CR reconcile 상태를 별도로 본다.
	MetricPoolerPhase = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "postgres_operator",
			Subsystem: "pooler",
			Name:      "phase",
			Help:      "Pooler phase as a one-hot gauge labeled by phase",
		},
		[]string{"namespace", "name", "cluster", "type", "phase"},
	)

	// MetricPostgresClusterReplicationLagBytes 는 instance status annotation 에서
	// 합산된 WAL lag 를 operator metrics endpoint 로 노출한다.
	MetricPostgresClusterReplicationLagBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "postgres_operator",
			Subsystem: "postgrescluster",
			Name:      "replication_lag_bytes",
			Help:      "Observed PostgreSQL WAL replication lag in bytes from PostgresCluster status",
		},
		[]string{"namespace", "name", "shard", "pod", "role"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		MetricReconcileTotal,
		MetricReconcileLatency,
		MetricReconcileErrors,
		MetricBackupJobPhase,
		MetricPoolerPhase,
		MetricPostgresClusterReplicationLagBytes,
	)
}

// DeleteMetricsFor — CR 삭제 시 cardinality 누적 방지.
func DeleteMetricsFor(namespace, name string) {
	MetricReconcileTotal.DeleteLabelValues(namespace, name)
	MetricReconcileErrors.DeletePartialMatch(prometheus.Labels{
		"namespace": namespace, "name": name,
	})
	MetricPostgresClusterReplicationLagBytes.DeletePartialMatch(prometheus.Labels{
		"namespace": namespace, "name": name,
	})
	for _, r := range []string{"success", "error"} {
		MetricReconcileLatency.DeleteLabelValues(namespace, name, r)
	}
}

// ObservePostgresClusterMetrics 는 PostgresCluster status 기반 운영 metric 을 반영한다.
func ObservePostgresClusterMetrics(cluster *postgresv1alpha1.PostgresCluster) {
	if cluster == nil {
		return
	}
	for _, shard := range cluster.Status.Shards {
		shardName := shard.Name
		if shardName == "" {
			shardName = fmt.Sprintf("shard-%d", shard.Ordinal)
		}
		if shard.Primary != nil && shard.Primary.Pod != "" {
			MetricPostgresClusterReplicationLagBytes.WithLabelValues(
				cluster.Namespace,
				cluster.Name,
				shardName,
				shard.Primary.Pod,
				"primary",
			).Set(float64(shard.Primary.LagBytes))
		}
		for _, replica := range shard.Replicas {
			if replica.Pod == "" {
				continue
			}
			MetricPostgresClusterReplicationLagBytes.WithLabelValues(
				cluster.Namespace,
				cluster.Name,
				shardName,
				replica.Pod,
				"replica",
			).Set(float64(replica.LagBytes))
		}
	}
}

// ObserveBackupJobMetrics 는 BackupJob status phase 를 scrape 가능한 gauge 로 반영한다.
func ObserveBackupJobMetrics(bj *postgresv1alpha1.BackupJob) {
	if bj == nil {
		return
	}
	for _, phase := range backupJobPhaseLabelValues {
		value := 0.0
		if bj.Status.Phase == phase {
			value = 1
		}
		MetricBackupJobPhase.WithLabelValues(
			bj.Namespace,
			bj.Name,
			bj.Spec.Cluster.Name,
			bj.Spec.Tool,
			bj.Spec.Type,
			string(phase),
		).Set(value)
	}
}

// DeleteBackupJobMetricsFor 는 삭제된 BackupJob 의 phase series 를 제거한다.
func DeleteBackupJobMetricsFor(namespace, name string) {
	MetricBackupJobPhase.DeletePartialMatch(prometheus.Labels{
		"namespace": namespace,
		"name":      name,
	})
}

// ObservePoolerMetrics 는 Pooler status phase 를 scrape 가능한 gauge 로 반영한다.
func ObservePoolerMetrics(pooler *postgresv1alpha1.Pooler) {
	if pooler == nil {
		return
	}
	poolerType := defaultPoolerType(pooler.Spec.Type)
	for _, phase := range poolerPhaseLabelValues {
		value := 0.0
		if pooler.Status.Phase == phase {
			value = 1
		}
		MetricPoolerPhase.WithLabelValues(
			pooler.Namespace,
			pooler.Name,
			pooler.Spec.Cluster.Name,
			string(poolerType),
			string(phase),
		).Set(value)
	}
}

// DeletePoolerMetricsFor 는 삭제된 Pooler 의 phase series 를 제거한다.
func DeletePoolerMetricsFor(namespace, name string) {
	MetricPoolerPhase.DeletePartialMatch(prometheus.Labels{
		"namespace": namespace,
		"name":      name,
	})
}
