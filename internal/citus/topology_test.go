/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package citus

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	postgresv1alpha1 "github.com/keiailab/postgres-operator/api/v1alpha1"
)

// 본 파일은 Pillar P11-T1 spike의 단위 회귀다. RFC 0002 §1~§6 결정의
// 코드 차원 강제.

// ----------------------------------------------------------------------------
// PodDNS
// ----------------------------------------------------------------------------

func TestPodDNS_StableFormat(t *testing.T) {
	got := PodDNS("orders-coordinator", "orders-coordinator", "default", 0)
	want := "orders-coordinator-0.orders-coordinator.default.svc.cluster.local"
	if got != want {
		t.Errorf("PodDNS = %q, want %q", got, want)
	}
}

// ----------------------------------------------------------------------------
// DesiredNodes
// ----------------------------------------------------------------------------

func miniCluster(coordMembers int32, pools ...struct {
	name    string
	members int32
}) *postgresv1alpha1.PostgresCluster {
	workers := make([]postgresv1alpha1.WorkerPoolSpec, len(pools))
	for i, p := range pools {
		workers[i] = postgresv1alpha1.WorkerPoolSpec{
			Name:    p.name,
			Members: p.members,
			Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("10Gi")},
		}
	}
	return &postgresv1alpha1.PostgresCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "default"},
		Spec: postgresv1alpha1.PostgresClusterSpec{
			Coordinator: postgresv1alpha1.CoordinatorSpec{
				Members: coordMembers,
				Storage: postgresv1alpha1.StorageSpec{Size: resource.MustParse("10Gi")},
			},
			Workers: workers,
			Routers: postgresv1alpha1.RouterSpec{Replicas: 1},
		},
	}
}

func TestDesiredNodes_CoordinatorOnly(t *testing.T) {
	c := miniCluster(1)
	got := DesiredNodes(c)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	n := got[0]
	if n.Group != CoordinatorGroup {
		t.Errorf("Group = %d, want %d", n.Group, CoordinatorGroup)
	}
	if n.Role != "coordinator" {
		t.Errorf("Role = %q, want 'coordinator'", n.Role)
	}
	if n.ShouldHaveShards {
		t.Error("coordinator ShouldHaveShards default must be false (RFC 0002 §3)")
	}
	if n.Port != PGPort {
		t.Errorf("Port = %d, want %d", n.Port, PGPort)
	}
	if n.Name != "orders-coordinator-0.orders-coordinator.default.svc.cluster.local" {
		t.Errorf("Name = %q", n.Name)
	}
}

func TestDesiredNodes_CoordinatorShouldHaveShards_Override(t *testing.T) {
	c := miniCluster(1)
	yes := true
	c.Spec.Coordinator.ShouldHaveShards = &yes
	got := DesiredNodes(c)
	if !got[0].ShouldHaveShards {
		t.Error("user override true must be honored")
	}
}

func TestDesiredNodes_OnePool(t *testing.T) {
	const poolName = "pool-a"
	c := miniCluster(3, struct {
		name    string
		members int32
	}{poolName, 3})

	got := DesiredNodes(c)
	if len(got) != 6 { // 3 coord + 3 worker
		t.Fatalf("len = %d, want 6", len(got))
	}

	// coordinator 3개 모두 group=0
	for i := range 3 {
		if got[i].Group != 0 {
			t.Errorf("got[%d].Group = %d, want 0", i, got[i].Group)
		}
		if got[i].Role != "coordinator" {
			t.Errorf("got[%d].Role = %q", i, got[i].Role)
		}
	}
	// worker 3개 모두 group=1, ShouldHaveShards=true
	for i := 3; i < 6; i++ {
		if got[i].Group != 1 {
			t.Errorf("got[%d].Group = %d, want 1", i, got[i].Group)
		}
		if got[i].Pool != poolName {
			t.Errorf("got[%d].Pool = %q", i, got[i].Pool)
		}
		if !got[i].ShouldHaveShards {
			t.Errorf("got[%d].ShouldHaveShards = false (worker default must be true)", i)
		}
	}
}

func TestDesiredNodes_MultiPool_GroupAssignment(t *testing.T) {
	c := miniCluster(1,
		struct {
			name    string
			members int32
		}{"pool-a", 2},
		struct {
			name    string
			members int32
		}{"pool-b", 1},
	)
	got := DesiredNodes(c)
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	// pool-a → group=1 (members=2)
	if got[1].Group != 1 || got[1].Pool != "pool-a" {
		t.Errorf("got[1] = %+v, want pool-a/group=1", got[1])
	}
	if got[2].Group != 1 || got[2].Pool != "pool-a" {
		t.Errorf("got[2] = %+v, want pool-a/group=1", got[2])
	}
	// pool-b → group=2
	if got[3].Group != 2 || got[3].Pool != "pool-b" {
		t.Errorf("got[3] = %+v, want pool-b/group=2", got[3])
	}
}

func TestDesiredNodes_Determinism(t *testing.T) {
	// 동일 입력에 대해 두 번 호출하면 동일 출력.
	c := miniCluster(3,
		struct {
			name    string
			members int32
		}{"pool-a", 3},
		struct {
			name    string
			members int32
		}{"pool-b", 3},
	)
	a := DesiredNodes(c)
	b := DesiredNodes(c)
	if len(a) != len(b) {
		t.Fatalf("non-deterministic length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("position %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// ----------------------------------------------------------------------------
// ComputeActions
// ----------------------------------------------------------------------------

// nd는 테스트 가독성을 위한 Node 빌더다. 모든 호출이 PGPort를 사용하므로
// port 파라미터는 두지 않는다(unparam 회피). 다른 포트가 필요하면 직접 Node{}
// 리터럴로 작성한다.
func nd(group int32, name string, opts ...func(*Node)) Node {
	n := Node{Group: group, Name: name, Port: PGPort, Role: "worker", ShouldHaveShards: true}
	for _, o := range opts {
		o(&n)
	}
	return n
}

func TestComputeActions_AllAdds(t *testing.T) {
	got := ComputeActions(nil, []Node{
		nd(2, "b"),
		nd(1, "a"),
	})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// Group 오름차순 정렬 보장
	if got[0].Op != OpAdd || got[0].Node.Group != 1 {
		t.Errorf("got[0] = %+v, want add group=1", got[0])
	}
	if got[1].Op != OpAdd || got[1].Node.Group != 2 {
		t.Errorf("got[1] = %+v, want add group=2", got[1])
	}
}

func TestComputeActions_RemoveBeforeAdd(t *testing.T) {
	current := []Node{nd(1, "old")}
	desired := []Node{nd(2, "new")}

	got := ComputeActions(current, desired)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Op != OpRemove {
		t.Errorf("first op = %v, want remove (gauge availability)", got[0].Op)
	}
	if got[1].Op != OpAdd {
		t.Errorf("second op = %v, want add", got[1].Op)
	}
}

func TestComputeActions_UpdateOnFieldChange(t *testing.T) {
	current := []Node{nd(1, "a", func(n *Node) { n.ShouldHaveShards = false })}
	desired := []Node{nd(1, "a")}

	got := ComputeActions(current, desired)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Op != OpUpdate {
		t.Errorf("op = %v, want update", got[0].Op)
	}
}

func TestComputeActions_NoChange_EmptyResult(t *testing.T) {
	n := []Node{nd(1, "a"), nd(2, "b")}
	got := ComputeActions(n, n)
	if len(got) != 0 {
		t.Errorf("expected 0 actions for identical state, got %d", len(got))
	}
}

func TestComputeActions_InputOrderIndependence(t *testing.T) {
	// 입력 슬라이스 순서를 셔플해도 결과 동일.
	desired1 := []Node{nd(1, "a"), nd(2, "b"), nd(3, "c")}
	desired2 := []Node{nd(3, "c"), nd(1, "a"), nd(2, "b")}

	a := ComputeActions(nil, desired1)
	b := ComputeActions(nil, desired2)

	if len(a) != len(b) {
		t.Fatalf("len mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("position %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// ----------------------------------------------------------------------------
// SQLExecutor
// ----------------------------------------------------------------------------

func TestNullExecutor_AlwaysNil(t *testing.T) {
	if err := (NullExecutor{}).Apply(context.Background(), []Action{
		{Op: OpAdd, Node: nd(1, "x")},
	}); err != nil {
		t.Errorf("NullExecutor.Apply must always return nil, got %v", err)
	}
}

func TestMockExecutor_RecordsCalls(t *testing.T) {
	m := &MockExecutor{}
	actions := []Action{{Op: OpAdd, Node: nd(1, "x")}}
	if err := m.Apply(context.Background(), actions); err != nil {
		t.Fatal(err)
	}
	if m.Calls() != 1 {
		t.Errorf("Calls = %d, want 1", m.Calls())
	}
	if len(m.Applied[0]) != 1 || m.Applied[0][0].Op != OpAdd {
		t.Errorf("Applied[0] = %+v", m.Applied[0])
	}
}

func TestMockExecutor_ReturnsError(t *testing.T) {
	wantErr := errors.New("boom")
	m := &MockExecutor{Err: wantErr}
	if err := m.Apply(context.Background(), nil); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if m.Calls() != 0 {
		t.Errorf("failed Apply must not record (got %d calls)", m.Calls())
	}
}
