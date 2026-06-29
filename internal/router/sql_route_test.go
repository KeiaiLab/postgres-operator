/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// twoShardHashSpec 은 2-shard murmur3 hash vindex (cmd/scatter-poc, cmd/pg-router 와 동일).
func twoShardHashSpec() v1alpha1.ShardRangeSpec {
	return v1alpha1.ShardRangeSpec{
		Vindex: v1alpha1.VindexSpec{Type: v1alpha1.VindexTypeHash, Column: "id", Function: "murmur3"},
		Ranges: []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-1"},
		},
	}
}

func TestExtractRoutingKey(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		wantKey string
		wantOK  bool
	}{
		{"insert values", "INSERT INTO t (id, v) VALUES ('alice', 1)", "alice", true},
		{"select where eq", "SELECT v FROM t WHERE id = 'bob'", "bob", true},
		{"where no space", "SELECT v FROM t WHERE id='carol'", "carol", true},
		{"lowercase", "select v from t where user_id = 'dave'", "dave", true},
		{"no key (full scan)", "SELECT v FROM t", "", false},
		{"no key (range)", "SELECT v FROM t WHERE n > 5", "", false},
		{"empty literal", "SELECT v FROM t WHERE id = ''", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := ExtractRoutingKey(tc.query)
			if ok != tc.wantOK || key != tc.wantKey {
				t.Fatalf("ExtractRoutingKey(%q) = (%q,%v), want (%q,%v)", tc.query, key, ok, tc.wantKey, tc.wantOK)
			}
		})
	}
}

// TestExtractRoutingKey_RoutesToShard 는 추출한 key 가 vindex(ResolveShard)로 단일
// shard 에 결정적으로 매핑됨을 확인한다 (single-shard fast-path 결선).
func TestExtractRoutingKey_RoutesToShard(t *testing.T) {
	spec := twoShardHashSpec()
	key, ok := ExtractRoutingKey("SELECT v FROM t WHERE id = 'alice'")
	if !ok {
		t.Fatal("expected routing key")
	}
	sh, err := ResolveShard(spec, key)
	if err != nil {
		t.Fatalf("ResolveShard(%q): %v", key, err)
	}
	if sh != "shard-0" && sh != "shard-1" {
		t.Fatalf("key %q → unexpected shard %q", key, sh)
	}
	// 결정성: 같은 key 는 항상 같은 shard.
	sh2, _ := ResolveShard(spec, key)
	if sh != sh2 {
		t.Fatalf("non-deterministic routing: %q vs %q", sh, sh2)
	}
}
