/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// TestReconcileTimeoutSet 은 컨트롤러가 reconcile 마다 시한을 거는지 본다. 시한이
// 없으면(기본 0) 알 수 없는 대기 하나가 worker 를 영구히 잡아 컨트롤러가 선다.
func TestReconcileTimeoutSet(t *testing.T) {
	t.Parallel()
	if got := controllerOptions().ReconciliationTimeout; got != reconcileTimeout {
		t.Fatalf("ReconciliationTimeout = %s, want %s", got, reconcileTimeout)
	}
}

// TestStatusWriteGauge 는 Reconcile 한 번이 status 쓰기 성공 시각을 게이지에 남기고
// CR 삭제 정리(DeleteMetricsFor)가 그 시계열을 지우는지 본다. 게이지가 멈추면
// 컨트롤러가 멈춘 것이다.
func TestStatusWriteGauge(t *testing.T) {
	t.Parallel()
	const ns, name = "status-gauge", "gauge"

	// Reconcile 이 쓰는 종류(instance Role·RoleBinding, router HPA 정리)까지 등록한다.
	scheme := newScheme(t)
	for _, add := range []func(*runtime.Scheme) error{rbacv1.AddToScheme, autoscalingv2.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("add to scheme: %v", err)
		}
	}
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: postgresv1alpha1.PostgresClusterSpec{
			PostgresVersion: "18",
			ShardingMode:    postgresv1alpha1.ShardingModeNone,
			Shards: postgresv1alpha1.ShardsSpec{
				InitialCount: 1,
				Storage:      postgresv1alpha1.StorageSpec{Size: resource.MustParse("1Gi")},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).
		WithStatusSubresource(&postgresv1alpha1.PostgresCluster{}).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}
	defer DeleteMetricsFor(ns, name)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cluster)}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := testutil.ToFloat64(MetricStatusWriteTimestamp.WithLabelValues(ns, name)); got <= 0 {
		t.Fatalf("status_write_timestamp_seconds = %v, want > 0 after a status write", got)
	}

	// 삭제 정리 뒤 같은 라벨은 새 시계열(0)이다.
	DeleteMetricsFor(ns, name)
	if got := testutil.ToFloat64(MetricStatusWriteTimestamp.WithLabelValues(ns, name)); got != 0 {
		t.Fatalf("gauge after DeleteMetricsFor = %v, want 0 (series removed)", got)
	}
}
