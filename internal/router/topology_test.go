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
