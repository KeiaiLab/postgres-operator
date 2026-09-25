/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestPrimaryEndpointHost(t *testing.T) {
	cases := map[string]string{
		"demo-shard-0-0.demo-shard-0-headless.default.svc.cluster.local:5432": "demo-shard-0-0.demo-shard-0-headless.default.svc.cluster.local",
		"host:5432": "host",
		"host":      "host",
		"":          "",
	}
	for in, want := range cases {
		if got := primaryEndpointHost(in); got != want {
			t.Errorf("primaryEndpointHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildShardPrimaryService(t *testing.T) {
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
	}
	svc := buildShardPrimaryService(cluster, ShardPrimaryServiceName("demo", "shard-0"), "demo-shard-0-0.hdl.default.svc.cluster.local")
	if svc.Name != "demo-shard-0-primary" {
		t.Fatalf("name = %q, want demo-shard-0-primary", svc.Name)
	}
	if svc.Spec.Type != corev1.ServiceTypeExternalName {
		t.Fatalf("type = %q, want ExternalName", svc.Spec.Type)
	}
	if svc.Spec.ExternalName != "demo-shard-0-0.hdl.default.svc.cluster.local" {
		t.Fatalf("externalName = %q", svc.Spec.ExternalName)
	}
	// ExternalName Service 는 selector/ports 를 갖지 않는다.
	if svc.Spec.Selector != nil || len(svc.Spec.Ports) != 0 {
		t.Fatalf("ExternalName Service must have no selector/ports: %+v", svc.Spec)
	}
}

func TestReconcileShardPrimaryServices_PublishAndFailoverUpdate(t *testing.T) {
	scheme := newScheme(t)
	ns := "default"
	cluster := &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	svcKey := client.ObjectKey{Namespace: ns, Name: ShardPrimaryServiceName("demo", "shard-0")}

	// 1) 초기 런타임 primary(pod-0, 실제 primary 확인) → ExternalName Service publish.
	authorities := []shardAuthority{{
		shard: "shard-0", primary: "demo-shard-0-0", check: primaryConfirmed,
		endpoint: "demo-shard-0-0.hdl.default.svc.cluster.local:5432",
	}}
	if err := r.reconcileShardPrimaryServices(context.Background(), cluster, authorities); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var svc corev1.Service
	if err := c.Get(context.Background(), svcKey, &svc); err != nil {
		t.Fatalf("primary Service not created: %v", err)
	}
	if svc.Spec.ExternalName != "demo-shard-0-0.hdl.default.svc.cluster.local" {
		t.Fatalf("initial externalName = %q", svc.Spec.ExternalName)
	}

	// 2) failover: 승격이 CM 을 pod-1 로 바꿈 → ExternalName 이 새 primary 로 갱신되어야 함.
	authorities[0].primary = "demo-shard-0-1"
	authorities[0].endpoint = "demo-shard-0-1.hdl.default.svc.cluster.local:5432"
	if err := r.reconcileShardPrimaryServices(context.Background(), cluster, authorities); err != nil {
		t.Fatalf("reconcile (failover): %v", err)
	}
	if err := c.Get(context.Background(), svcKey, &svc); err != nil {
		t.Fatalf("get after failover: %v", err)
	}
	if svc.Spec.ExternalName != "demo-shard-0-1.hdl.default.svc.cluster.local" {
		t.Fatalf("failover externalName = %q, want pod-1 (DNS failover-follow)", svc.Spec.ExternalName)
	}
}

func TestReconcileShardPrimaryServices_SkipNotReadyOrNoPrimary(t *testing.T) {
	scheme := newScheme(t)
	ns := "default"
	cluster := &postgresv1alpha1.PostgresCluster{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	r := &PostgresClusterReconciler{Client: c, Scheme: scheme}

	authorities := []shardAuthority{
		{shard: "shard-0", check: primaryUnknown},                                      // 런타임 CM 없음.
		{shard: "shard-1", primary: "p", endpoint: "p:5432", check: primaryInRecovery}, // 실제 primary 아님.
	}
	if err := r.reconcileShardPrimaryServices(context.Background(), cluster, authorities); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var list corev1.ServiceList
	if err := c.List(context.Background(), &list, client.InNamespace(ns)); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("expected no primary Service (skip unconfirmed/no runtime primary), got %d", len(list.Items))
	}
}

// TestObservedAuthorities 는 복제 클러스터(런타임 CM 없음)의 -primary Service 권위가 관측된
// Ready primary 그대로인지 본다(무변) — not-ready·부재 primary 는 빠진다.
func TestObservedAuthorities(t *testing.T) {
	shards := []postgresv1alpha1.ShardStatus{
		{Name: "shard-0", Primary: &postgresv1alpha1.ShardEndpoint{Pod: "a", Endpoint: "a.hdl:5432", Ready: true}},
		{Name: "shard-1", Primary: &postgresv1alpha1.ShardEndpoint{Pod: "b", Endpoint: "b.hdl:5432", Ready: false}},
		{Name: "shard-2"},
	}
	got := observedAuthorities(shards)
	if len(got) != 1 || got[0].shard != "shard-0" || got[0].writable() != "a" || got[0].endpoint != "a.hdl:5432" {
		t.Fatalf("observedAuthorities = %+v, want only shard-0 → a", got)
	}
}
