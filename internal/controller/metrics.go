/*
Copyright 2026 Keiailab.
*/

// Package controller — Prometheus metrics 정의.
//
// controller-runtime 의 글로벌 metrics registry 자동 등록. valkey-operator PR #47
// cross-operator 이식 — SLO 추적 (p50/p95/p99 reconcile latency).
package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const metricSubsystem = "postgrescluster"

var labelNamespaceName = []string{"namespace", "name"}

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
)

func init() {
	metrics.Registry.MustRegister(
		MetricReconcileTotal,
		MetricReconcileLatency,
		MetricReconcileErrors,
	)
}

// DeleteMetricsFor — CR 삭제 시 cardinality 누적 방지.
func DeleteMetricsFor(namespace, name string) {
	MetricReconcileTotal.DeleteLabelValues(namespace, name)
	MetricReconcileErrors.DeletePartialMatch(prometheus.Labels{
		"namespace": namespace, "name": name,
	})
	for _, r := range []string{"success", "error"} {
		MetricReconcileLatency.DeleteLabelValues(namespace, name, r)
	}
}
