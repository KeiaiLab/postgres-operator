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
	if d.Shard != shard0 { // AnyShard = 결정적 첫 샤드
		t.Fatalf("reference query shard = %q, want shard-0", d.Shard)
	}
	if !d.Read {
		t.Fatal("SELECT countries should be Read")
	}
}

func TestQueryRouter_ReferenceWriteDoesNotUseAnyShard(t *testing.T) {
	qr := testQueryRouter()
	d, err := qr.Route("UPDATE countries SET name = 'Korea'")
	if !errors.Is(err, ErrNoRoutingKey) {
		t.Fatalf("reference write err = %v, want ErrNoRoutingKey", err)
	}
	if !d.Scatter || d.Read {
		t.Fatalf("reference write decision = %+v, want write scatter signal", d)
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

func TestQueryRouter_NilExtractorReturnsError(t *testing.T) {
	qr := testQueryRouter()
	qr.Extractor = nil
	if _, err := qr.Route("SELECT v FROM t WHERE tenant_id = 'alice'"); err == nil {
		t.Fatal("expected nil extractor error")
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

// TestQueryRouter_MultiRowInsert 는 #B-30 — 다중행 INSERT 라우팅을 검증한다.
func TestQueryRouter_MultiRowInsert(t *testing.T) {
	qr := testQueryRouter()
	// 같은 키가 여러 튜플이면 단일 shard → 정상 라우팅.
	single, err := qr.Route("INSERT INTO t (tenant_id, v) VALUES ('alice',1),('alice',2),('alice',3)")
	if err != nil {
		t.Fatalf("same-key multi-row INSERT should route: %v", err)
	}
	if single.Shard == "" || single.Backend != single.Shard+"-primary:5432" {
		t.Fatalf("single-shard multi-row decision = %+v", single)
	}

	// 서로 다른 shard 로 가는 키가 섞이면 ErrCrossShardInsert (조용한 오배치 방지).
	// alice/bob 이 다른 shard 인지 먼저 확인해 케이스를 구성.
	sa, _ := qr.Topology.Shard("alice")
	sb, _ := qr.Topology.Shard("bob")
	if sa == sb {
		// 둘이 같은 shard 면 이 케이스를 못 만드므로, 다른 shard 키를 탐색.
		for _, k := range []string{"carol", "dave", "eve", "frank", "grace", "heidi"} {
			if s, _ := qr.Topology.Shard(k); s != sa {
				_, err := qr.Route("INSERT INTO t (tenant_id, v) VALUES ('alice',1),('" + k + "',2)")
				if !errors.Is(err, ErrCrossShardInsert) {
					t.Fatalf("cross-shard INSERT (alice+%s) err = %v, want ErrCrossShardInsert", k, err)
				}
				return
			}
		}
		t.Skip("no distinct-shard key pair found among test keys")
	}
	_, err = qr.Route("INSERT INTO t (tenant_id, v) VALUES ('alice',1),('bob',2)")
	if !errors.Is(err, ErrCrossShardInsert) {
		t.Fatalf("cross-shard INSERT (alice+bob) err = %v, want ErrCrossShardInsert", err)
	}
}

// TestMatchInsertColumnAll 은 다중 튜플 키 추출 순수함수를 검증한다.
func TestMatchInsertColumnAll(t *testing.T) {
	// 컬럼 위치 매칭 + 문자열/숫자 리터럴.
	keys, ok := matchInsertColumnAll("INSERT INTO t (tenant_id, v) VALUES ('a',1),('b',2),('c',3)", "tenant_id")
	if !ok || len(keys) != 3 || keys[0] != "a" || keys[2] != "c" {
		t.Fatalf("string keys = %v ok=%v", keys, ok)
	}
	// 숫자 키(B-13).
	nkeys, ok := matchInsertColumnAll("INSERT INTO orders (tenant_id, amt) VALUES (10,1),(20,2)", "tenant_id")
	if !ok || len(nkeys) != 2 || nkeys[0] != "10" || nkeys[1] != "20" {
		t.Fatalf("numeric keys = %v ok=%v", nkeys, ok)
	}
	// 두 번째 컬럼이 키.
	c2, ok := matchInsertColumnAll("INSERT INTO t (v, tenant_id) VALUES (1,'x'),(2,'y')", "tenant_id")
	if !ok || len(c2) != 2 || c2[0] != "x" || c2[1] != "y" {
		t.Fatalf("2nd-col keys = %v ok=%v", c2, ok)
	}
	// 단일행도 처리(len 1).
	one, ok := matchInsertColumnAll("INSERT INTO t (tenant_id) VALUES ('solo')", "tenant_id")
	if !ok || len(one) != 1 || one[0] != "solo" {
		t.Fatalf("single-row = %v ok=%v", one, ok)
	}
	// 컬럼 부재 → false.
	if _, ok := matchInsertColumnAll("INSERT INTO t (other) VALUES ('a'),('b')", "tenant_id"); ok {
		t.Fatal("missing column should be ok=false")
	}
}
