/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"errors"
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

func testQueryRouter() QueryRouter {
	topo := Topology{Spec: v1alpha1.ShardRangeSpec{
		Cluster:         "demo",
		Keyspace:        "default",
		Vindex:          v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Column: "tenant_id", Function: "murmur3"},
		ReferenceTables: []string{"countries"},
		Ranges: []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-1"},
		},
	}}
	write := func(s string) (string, error) { return s + "-primary:5432", nil }
	read := func(s string) (string, error) { return s + "-replica:5432", nil }
	parser, _ := NewRouteKeyExtractor(ExtractorParser)
	return QueryRouter{Topology: topo, Extractor: parser, Write: write, Read: read}
}

func TestQueryRouter_WriteRoutesToPrimaryShard(t *testing.T) {
	qr := testQueryRouter()
	d, err := qr.Route("UPDATE t SET v = 1 WHERE tenant_id = 'alice'")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if d.Read {
		t.Fatal("UPDATE should not be Read")
	}
	if d.Shard == "" || d.Backend != d.Shard+"-primary:5432" {
		t.Fatalf("write decision = %+v, want primary backend", d)
	}
}

func TestQueryRouter_WriteBlockedRejectsWritesAllowsReads(t *testing.T) {
	qr := testQueryRouter()
	qr.Topology.Spec.WriteBlocked = true // cutover write-block.

	// 쓰기는 ErrWriteBlocked.
	if _, err := qr.Route("UPDATE t SET v = 1 WHERE tenant_id = 'alice'"); !errors.Is(err, ErrWriteBlocked) {
		t.Fatalf("blocked write err = %v, want ErrWriteBlocked", err)
	}
	if _, err := qr.Route("INSERT INTO t (tenant_id) VALUES ('alice')"); !errors.Is(err, ErrWriteBlocked) {
		t.Fatalf("blocked insert err = %v, want ErrWriteBlocked", err)
	}
	// 읽기는 통과(차단 중에도 SELECT 정상).
	d, err := qr.Route("SELECT v FROM t WHERE tenant_id = 'alice'")
	if err != nil {
		t.Fatalf("blocked read err = %v, want nil (reads allowed)", err)
	}
	if !d.Read {
		t.Fatal("SELECT should be Read")
	}
}

func TestQueryRouter_ReadRoutesToReplica(t *testing.T) {
	qr := testQueryRouter()
	d, err := qr.Route("SELECT v FROM t WHERE tenant_id = 'alice'")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !d.Read {
		t.Fatal("SELECT should be Read")
	}
	if d.Backend != d.Shard+"-replica:5432" {
		t.Fatalf("read decision = %+v, want replica backend", d)
	}
}

func TestQueryRouter_ReferenceOnlyUsesAnyShard(t *testing.T) {
	qr := testQueryRouter()
	d, err := qr.Route("SELECT name FROM countries")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if d.Shard != "shard-0" { // AnyShard = 결정적 첫 샤드
		t.Fatalf("reference query shard = %q, want shard-0", d.Shard)
	}
	if !d.Read {
		t.Fatal("SELECT countries should be Read")
	}
}

func TestQueryRouter_NoKeySignalsScatter(t *testing.T) {
	qr := testQueryRouter()
	d, err := qr.Route("SELECT * FROM t") // 키 없음, reference 아님
	if !errors.Is(err, ErrNoRoutingKey) {
		t.Fatalf("Route(no key) err = %v, want ErrNoRoutingKey", err)
	}
	if !d.Scatter {
		t.Fatal("no-key decision should set Scatter")
	}
}

func TestQueryRouter_BackendErrorPropagates(t *testing.T) {
	qr := testQueryRouter()
	qr.Write = func(string) (string, error) { return "", errors.New("shard down") }
	// 쓰기 쿼리 → Write resolver 에러 전파.
	if _, err := qr.Route("UPDATE t SET v=1 WHERE tenant_id='bob'"); err == nil {
		t.Fatal("expected backend error to propagate")
	}
}
