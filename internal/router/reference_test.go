/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"testing"

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

func TestExtractTables(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		{"SELECT * FROM users WHERE id = 'a'", []string{"users"}},
		{"SELECT * FROM orders o JOIN countries c ON o.cc = c.id", []string{"orders", "countries"}},
		{"INSERT INTO events (id) VALUES ('x')", []string{"events"}},
		{"UPDATE accounts SET v = 1 WHERE id = 'a'", []string{"accounts"}},
		{"SELECT * FROM public.users", []string{"users"}},
		{"SELECT 1", nil},
	}
	for _, c := range cases {
		got := ExtractTables(c.query)
		if len(got) != len(c.want) {
			t.Errorf("ExtractTables(%q) = %v, want %v", c.query, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ExtractTables(%q)[%d] = %q, want %q", c.query, i, got[i], c.want[i])
			}
		}
	}
}

func TestReferenceRouting(t *testing.T) {
	topo := Topology{Spec: v1alpha1.ShardRangeSpec{
		Cluster:         "demo",
		Keyspace:        "default",
		ReferenceTables: []string{"countries", "currencies"},
		Ranges: []v1alpha1.ShardRangeEntry{
			{Lo: "0x00000000", Hi: "0x7fffffff", Shard: "shard-0"},
			{Lo: "0x80000000", Hi: "0xffffffff", Shard: "shard-1"},
		},
	}}

	if !topo.IsReferenceTable("countries") || !topo.IsReferenceTable("CURRENCIES") {
		t.Fatal("IsReferenceTable should match (case-insensitive)")
	}
	if topo.IsReferenceTable("users") {
		t.Fatal("users is not a reference table")
	}

	// reference 테이블만 → reference-only true.
	if !topo.ReferenceOnly("SELECT name FROM countries") {
		t.Fatal("SELECT FROM countries should be reference-only")
	}
	if !topo.ReferenceOnly("SELECT * FROM countries c JOIN currencies x ON c.cur = x.id") {
		t.Fatal("countries JOIN currencies should be reference-only")
	}
	// 샤딩 테이블 섞이면 false.
	if topo.ReferenceOnly("SELECT * FROM users u JOIN countries c ON u.cc = c.id") {
		t.Fatal("users JOIN countries should NOT be reference-only")
	}
	// 테이블 못 뽑으면 false.
	if topo.ReferenceOnly("SELECT 1") {
		t.Fatal("SELECT 1 should not be reference-only")
	}

	// AnyShard 는 결정적으로 첫 샤드.
	if s, err := topo.AnyShard(); err != nil || s != "shard-0" {
		t.Fatalf("AnyShard = (%q,%v), want shard-0", s, err)
	}
}
