/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"context"
	"testing"
)

// TestScatterGather_OrderByNumeric 는 MergeOrderBy 가 *수치* 비교를 함을 검증한다
// — 과거 fmt.Sprintf 문자열 비교는 9 보다 10 을 앞에 두는 버그가 있었다("10" < "9").
func TestScatterGather_OrderByNumeric(t *testing.T) {
	sg := &ScatterGather{
		Merge: MergeOrderBy,
		Shard: &fakeShardExecutor{responses: map[ShardID][]Row{
			"s0": {{Shard: "s0", Values: []any{int64(10)}}},
			"s1": {{Shard: "s1", Values: []any{int64(9)}}},
			"s2": {{Shard: "s2", Values: []any{int64(100)}}},
		}},
	}
	rows, err := sg.Execute(context.Background(), "q", []ShardID{"s0", "s1", "s2"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := []int64{9, 10, 100}
	if len(rows) != 3 {
		t.Fatalf("len(rows)=%d, want 3", len(rows))
	}
	for i, w := range want {
		if rows[i].Values[0].(int64) != w {
			t.Fatalf("rows[%d]=%v, want %d (numeric order)", i, rows[i].Values[0], w)
		}
	}
}

// TestScatterGather_OrderByColAndDesc 는 지정 컬럼 + 내림차순 정렬을 검증한다.
func TestScatterGather_OrderByColAndDesc(t *testing.T) {
	sg := &ScatterGather{
		Merge:       MergeOrderBy,
		OrderByCol:  1,
		OrderByDesc: true,
		Shard: &fakeShardExecutor{responses: map[ShardID][]Row{
			"s0": {{Values: []any{"a", int64(10)}}},
			"s1": {{Values: []any{"b", int64(30)}}, {Values: []any{"c", int64(20)}}},
		}},
	}
	rows, err := sg.Execute(context.Background(), "q", []ShardID{"s0", "s1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := []int64{30, 20, 10} // col 1 내림차순
	for i, w := range want {
		if rows[i].Values[1].(int64) != w {
			t.Fatalf("rows[%d].col1=%v, want %d", i, rows[i].Values[1], w)
		}
	}
}

// TestWithLimitPushdown 는 LIMIT 주입의 보수적 규칙을 검증한다.
func TestWithLimitPushdown(t *testing.T) {
	cases := []struct {
		query string
		n     int
		want  string
	}{
		{"SELECT * FROM t", 3, "SELECT * FROM t LIMIT 3"},
		{"SELECT * FROM t;", 3, "SELECT * FROM t LIMIT 3"},      // 후행 ; trim
		{"SELECT * FROM t LIMIT 5", 3, "SELECT * FROM t LIMIT 5"}, // 기존 LIMIT 유지
		{"SELECT 1; SELECT 2", 3, "SELECT 1; SELECT 2"},          // 다중문 → 안 건드림
		{"SELECT * FROM t ORDER BY x", 3, "SELECT * FROM t ORDER BY x LIMIT 3"},
	}
	for _, c := range cases {
		if got := withLimitPushdown(c.query, c.n); got != c.want {
			t.Errorf("withLimitPushdown(%q,%d) = %q, want %q", c.query, c.n, got, c.want)
		}
	}
}

// TestScatterGather_Limit 는 Limit 가 merge 결과를 자름을 검증한다.
func TestScatterGather_Limit(t *testing.T) {
	sg := &ScatterGather{
		Merge: MergeConcat,
		Limit: 3,
		Shard: &fakeShardExecutor{responses: map[ShardID][]Row{
			"s0": {{Values: []any{1}}, {Values: []any{2}}},
			"s1": {{Values: []any{3}}, {Values: []any{4}}},
		}},
	}
	rows, err := sg.Execute(context.Background(), "q", []ShardID{"s0", "s1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len(rows)=%d, want 3 (Limit)", len(rows))
	}
}
