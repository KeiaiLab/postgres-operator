/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"context"
	"errors"
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// fakeLister 는 ShardRangeLister 의 테스트 더블이다.
type fakeLister struct {
	items []v1alpha1.ShardRange
	err   error
}

func (f fakeLister) ListShardRanges(context.Context, string) ([]v1alpha1.ShardRange, error) {
	return f.items, f.err
}

func twoShardSpec() v1alpha1.ShardRangeSpec {
	return v1alpha1.ShardRangeSpec{
		Cluster:  "demo",
		Keyspace: "default",
		Vindex:   v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
		Ranges: []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-1"},
		},
	}
}

func TestTopologyShard(t *testing.T) {
	topo := Topology{Cluster: "demo", Keyspace: "default", Spec: twoShardSpec()}
	for _, key := range []string{"alice", "bob", "carol", "dave", "eve"} {
		shard, err := topo.Shard(key)
		if err != nil {
			t.Fatalf("Shard(%q): %v", key, err)
		}
		if shard != "shard-0" && shard != "shard-1" {
			t.Fatalf("Shard(%q) = %q, unexpected", key, shard)
		}
	}
}

func TestCRDTopologyProvider(t *testing.T) {
	sr := v1alpha1.ShardRange{Spec: twoShardSpec()}
	other := v1alpha1.ShardRange{Spec: v1alpha1.ShardRangeSpec{Cluster: "other", Keyspace: "default"}}

	p := &CRDTopologyProvider{
		Lister:    fakeLister{items: []v1alpha1.ShardRange{other, sr}},
		Namespace: "ns",
		Cluster:   "demo",
		Keyspace:  "default",
	}
	topo, err := p.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if topo.Cluster != "demo" || len(topo.Spec.Ranges) != 2 {
		t.Fatalf("topo = %+v", topo)
	}
	// 두 번째 Current 는 캐시에서 (lister 가 에러를 내도 캐시 반환).
	p.Lister = fakeLister{err: errors.New("api down")}
	if _, err := p.Current(context.Background()); err != nil {
		t.Fatalf("cached Current should not hit lister: %v", err)
	}

	// 매칭 없음 → 에러.
	p2 := &CRDTopologyProvider{
		Lister:   fakeLister{items: []v1alpha1.ShardRange{other}},
		Cluster:  "demo",
		Keyspace: "default",
	}
	if _, err := p2.Current(context.Background()); err == nil {
		t.Fatal("no matching ShardRange: expected error")
	}
}

func TestStatusBackendResolver(t *testing.T) {
	r := NewStatusBackendResolver()

	// Update 전: 모든 shard 에러.
	if _, err := r.Resolve("shard-0"); err == nil {
		t.Fatal("pre-update: expected error")
	}

	ready := func(pod, ep string, rdy bool) *v1alpha1.ShardEndpoint {
		return &v1alpha1.ShardEndpoint{Pod: pod, Endpoint: ep, Ready: rdy}
	}
	r.Update([]v1alpha1.ShardStatus{
		{Name: "shard-0", Primary: ready("demo-shard-0-0", "demo-shard-0-0.svc:5432", true)},
		{Name: "shard-1", Primary: ready("demo-shard-1-1", "demo-shard-1-1.svc:5432", false)}, // not ready
		{Name: "shard-2", Primary: nil}, // no primary (down)
	})

	// Ready primary → endpoint.
	if ep, err := r.Resolve("shard-0"); err != nil || ep != "demo-shard-0-0.svc:5432" {
		t.Fatalf("shard-0 = (%q,%v), want demo-shard-0-0.svc:5432", ep, err)
	}
	// primary not ready → error (failover 중).
	if _, err := r.Resolve("shard-1"); err == nil {
		t.Fatal("shard-1 (not ready): expected error")
	}
	// primary 부재 → error (down).
	if _, err := r.Resolve("shard-2"); err == nil {
		t.Fatal("shard-2 (no primary): expected error")
	}

	// failover 시뮬: shard-1 의 새 primary 가 Ready 로 갱신되면 따라간다.
	r.Update([]v1alpha1.ShardStatus{
		{Name: "shard-1", Primary: ready("demo-shard-1-0", "demo-shard-1-0.svc:5432", true)},
	})
	if ep, err := r.Resolve("shard-1"); err != nil || ep != "demo-shard-1-0.svc:5432" {
		t.Fatalf("shard-1 after failover = (%q,%v), want demo-shard-1-0.svc:5432", ep, err)
	}
	// shard-0 은 이제 status 에 없으니 에러(스냅샷 통째 교체).
	if _, err := r.Resolve("shard-0"); err == nil {
		t.Fatal("shard-0 absent after update: expected error")
	}
}

func TestStatusBackendResolver_ResolveRead(t *testing.T) {
	r := NewStatusBackendResolver()
	ep := func(pod, e string, rdy bool) v1alpha1.ShardEndpoint {
		return v1alpha1.ShardEndpoint{Pod: pod, Endpoint: e, Ready: rdy}
	}
	p := func(e string) *v1alpha1.ShardEndpoint { x := ep("p", e, true); return &x }
	r.Update([]v1alpha1.ShardStatus{
		{Name: "shard-0", Primary: p("p0:5432"), Replicas: []v1alpha1.ShardEndpoint{
			ep("r0a", "r0a:5432", true), ep("r0b", "r0b:5432", true), ep("r0c", "r0c:5432", false), // not ready 제외
		}},
		{Name: "shard-1", Primary: p("p1:5432")}, // replica 없음 → primary 폴백
		{Name: "shard-2"},                        // 아무것도 없음 → 에러
	})

	// 읽기: 두 Ready replica 를 round-robin (primary 아님).
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		got, err := r.ResolveRead("shard-0")
		if err != nil {
			t.Fatalf("ResolveRead shard-0: %v", err)
		}
		if got == "p0:5432" {
			t.Fatalf("read routed to primary, want a replica: %s", got)
		}
		seen[got]++
	}
	if len(seen) != 2 || seen["r0a:5432"] != 2 || seen["r0b:5432"] != 2 {
		t.Fatalf("round-robin distribution = %v, want 2x each replica", seen)
	}
	// replica 없으면 primary 폴백.
	if got, err := r.ResolveRead("shard-1"); err != nil || got != "p1:5432" {
		t.Fatalf("ResolveRead shard-1 = (%q,%v), want primary fallback p1:5432", got, err)
	}
	// 아무것도 없으면 에러.
	if _, err := r.ResolveRead("shard-2"); err == nil {
		t.Fatal("ResolveRead shard-2 (none): expected error")
	}
}

// TestStatusBackendResolver_LagBound 는 MaxReplicaLagBytes 초과 replica 가 읽기에서
// 제외됨을 검증한다 (bounded staleness).
func TestStatusBackendResolver_LagBound(t *testing.T) {
	r := NewStatusBackendResolver()
	r.MaxReplicaLagBytes = 1000
	rep := func(ep string, lag int64) v1alpha1.ShardEndpoint {
		return v1alpha1.ShardEndpoint{Pod: ep, Endpoint: ep, Ready: true, LagBytes: lag}
	}
	prim := v1alpha1.ShardEndpoint{Pod: "p", Endpoint: "p:5432", Ready: true}
	r.Update([]v1alpha1.ShardStatus{
		{Name: "shard-0", Primary: &prim, Replicas: []v1alpha1.ShardEndpoint{
			rep("fresh:5432", 500),  // 임계 이하 → 허용
			rep("stale:5432", 5000), // 임계 초과 → 제외
		}},
	})
	// 읽기는 fresh 만 (round-robin 해도 항상 fresh).
	for i := 0; i < 4; i++ {
		got, err := r.ResolveRead("shard-0")
		if err != nil || got != "fresh:5432" {
			t.Fatalf("ResolveRead = (%q,%v), want only fresh:5432 (stale 제외)", got, err)
		}
	}
}
