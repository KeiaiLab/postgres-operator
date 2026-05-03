/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
	"github.com/keiailab/postgres-operator/internal/instance/statusapi"
)

// 본 파일은 RFC 0006 R2 의 회귀 차단 — Pod annotation 기반 ShardStatus aggregation
// 의 6 가지 합산 케이스 검증.

func makePod(name string, st statusapi.Status, withAnnotation bool) *corev1.Pod {
	labels := SelectorLabels("demo", "shard", 0)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    labels,
		},
	}
	if withAnnotation {
		raw, _ := json.Marshal(st)
		pod.Annotations = map[string]string{statusapi.AnnotationKey: string(raw)}
	}
	return pod
}

func newCluster() *postgresv1alpha1.PostgresCluster {
	return &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
	}
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	if err := postgresv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add postgres v1alpha1: %v", err)
	}
	return s
}

func TestAggregateShardStatus_PrimaryAndReplica(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	primary := makePod("demo-shard-0-0", statusapi.Status{
		Role: statusapi.RolePrimary, Ready: true,
		Endpoint: "demo-shard-0-0.svc:5432", LagBytes: 0, LastUpdate: now,
	}, true)
	rep := makePod("demo-shard-0-1", statusapi.Status{
		Role: statusapi.RoleReplica, Ready: true,
		Endpoint: "demo-shard-0-1.svc:5432", LagBytes: 1024, LastUpdate: now,
	}, true)
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(primary, rep).Build()

	out := aggregateShardStatus(context.Background(), c, newCluster(), 0, "demo-shard-0-headless")
	if out.Primary == nil || out.Primary.Pod != "demo-shard-0-0" {
		t.Fatalf("Primary mis-elected: %+v", out.Primary)
	}
	if !out.Primary.Ready {
		t.Errorf("Primary should be Ready=true")
	}
	if len(out.Replicas) != 1 || out.Replicas[0].Pod != "demo-shard-0-1" {
		t.Errorf("Replicas mismatch: %+v", out.Replicas)
	}
	if out.Replicas[0].LagBytes != 1024 {
		t.Errorf("Replica LagBytes = %d, want 1024", out.Replicas[0].LagBytes)
	}
}

func TestAggregateShardStatus_StaleHeartbeat_ForcesNotReady(t *testing.T) {
	t.Parallel()
	old := time.Now().Add(-2 * time.Minute).UTC() // > 30s thresh
	pod := makePod("demo-shard-0-0", statusapi.Status{
		Role: statusapi.RolePrimary, Ready: true,
		Endpoint: "demo-shard-0-0.svc:5432", LastUpdate: old,
	}, true)
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(pod).Build()
	out := aggregateShardStatus(context.Background(), c, newCluster(), 0, "demo-shard-0-headless")
	if out.Primary == nil {
		t.Fatal("expected Primary to be present (stale != absent)")
	}
	if out.Primary.Ready {
		t.Errorf("stale heartbeat must force Ready=false, got %+v", out.Primary)
	}
}

func TestAggregateShardStatus_NoAnnotation_FallsBack(t *testing.T) {
	t.Parallel()
	pod := makePod("demo-shard-0-0", statusapi.Status{}, false)
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(pod).Build()
	out := aggregateShardStatus(context.Background(), c, newCluster(), 0, "demo-shard-0-headless")
	// annotation 부재 → primary 미할당 + Pod 가 replicas 로 떨어짐 (Ready=false).
	if out.Primary != nil {
		t.Errorf("no-annotation: Primary should be nil for caller fallback, got %+v", out.Primary)
	}
	if len(out.Replicas) != 1 || out.Replicas[0].Ready {
		t.Errorf("no-annotation Pod should be Replica with Ready=false, got %+v", out.Replicas)
	}
}

func TestAggregateShardStatus_SplitBrain_KeepsFirstPrimary(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	p1 := makePod("demo-shard-0-0", statusapi.Status{
		Role: statusapi.RolePrimary, Ready: true, LastUpdate: now,
	}, true)
	p2 := makePod("demo-shard-0-1", statusapi.Status{
		Role: statusapi.RolePrimary, Ready: true, LastUpdate: now,
	}, true)
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(p1, p2).Build()
	out := aggregateShardStatus(context.Background(), c, newCluster(), 0, "demo-shard-0-headless")
	if out.Primary == nil {
		t.Fatal("split-brain: Primary should still resolve to first candidate")
	}
	// 두 번째 primary 는 replicas 로 강등 — 운영자가 split-brain log 로 인지.
	if len(out.Replicas) != 1 {
		t.Errorf("split-brain: second primary should fall to replicas, got %d replicas", len(out.Replicas))
	}
}

func TestAggregateShardStatus_NoPods_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	out := aggregateShardStatus(context.Background(), c, newCluster(), 0, "demo-shard-0-headless")
	if out.Primary != nil || len(out.Replicas) != 0 {
		t.Errorf("no pods: should be empty, got %+v", out)
	}
}
