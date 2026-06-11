/*
Copyright 2026 Keiailab.
*/

// Package controller — Prometheus metrics 정의.
//
// controller-runtime 의 글로벌 metrics registry 자동 등록.
// SLO 추적 (p50/p95/p99 reconcile latency).
//
// reconcile SLO trio (reconcile_total / reconcile_duration_seconds /
// reconcile_errors_total) 는 keiailab-commons pkg/reconcilemetrics 로 위임
// (v0.11.0) — subsystem "postgrescluster" 주입으로 기존 시계열 이름
// (postgrescluster_reconcile_*) 이 byte-동일하게 보존된다. 도메인 메트릭
// (BackupJob / Pooler phase + WAL lag) 은 본 파일 잔류.
package controller

import (
	"fmt"

	"github.com/keiailab/keiailab-commons/pkg/reconcilemetrics"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

const metricSubsystem = "postgrescluster"

// reconMetrics — commons reconcile SLO trio. 등록은 init() 에서
// controller-runtime metrics.Registry 주입 (commons 는 registry 비결합).
var reconMetrics = reconcilemetrics.New(metricSubsystem)

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
	// commons trio alias — 콜사이트 무변경 마이그레이션 (fqName 보존).
	MetricReconcileTotal = reconMetrics.Total

	// MetricReconcileLatency — wall-clock duration. SLO p50/p95/p99 산출.
	// Buckets 5ms~30s 는 commons reconcilemetrics 가 byte-동일 보존.
	MetricReconcileLatency = reconMetrics.Latency

	// MetricReconcileErrors — component 별 reconcile 실패.
	MetricReconcileErrors = reconMetrics.Errors

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
	// trio 는 commons 가, 도메인 메트릭은 본 파일이 등록 — 중복 등록 panic 방지를
	// 위해 자체 trio 정의는 제거됨 (alias 만 잔존).
	reconMetrics.MustRegister(metrics.Registry)
	metrics.Registry.MustRegister(
		MetricBackupJobPhase,
		MetricPoolerPhase,
		MetricPostgresClusterReplicationLagBytes,
	)
}

// DeleteMetricsFor — CR 삭제 시 cardinality 누적 방지.
// trio 는 commons DeleteFor 위임, 도메인 메트릭은 이어서 자체 삭제.
func DeleteMetricsFor(namespace, name string) {
	reconMetrics.DeleteFor(namespace, name)
	MetricPostgresClusterReplicationLagBytes.DeletePartialMatch(prometheus.Labels{
		"namespace": namespace, "name": name,
	})
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
