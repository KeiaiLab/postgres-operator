/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"context"
	"slices"
	"testing"
)

func TestScatterMergeAggregate_ScalarCountSum(t *testing.T) {
	ctx := context.Background()
	sg := NewScatterGather()
	sg.Merge = MergeAggregate
	sg.Aggregates = []AggregateFunc{AggCount, AggSum}
	// 두 shard 의 부분 (count, sum): (3, 30) + (2, 20) → (5, 50).
	sg.Shard = &fakeShardExecutor{responses: map[ShardID][]Row{
		"s-0": {{Values: []any{int64(3), int64(30)}}},
		"s-1": {{Values: []any{int64(2), int64(20)}}},
	}}

	rows, err := sg.Execute(ctx, "SELECT count(*), sum(x) FROM t", []ShardID{"s-0", "s-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 aggregate row, got %d", len(rows))
	}
	if got := rows[0].Values; got[0] != int64(5) || got[1] != int64(50) {
		t.Fatalf("aggregate = %v, want [5 50]", got)
	}
}

func TestScatterMergeAggregate_MinMax(t *testing.T) {
	ctx := context.Background()
	sg := NewScatterGather()
	sg.Merge = MergeAggregate
	sg.Aggregates = []AggregateFunc{AggMin, AggMax}
	sg.Shard = &fakeShardExecutor{responses: map[ShardID][]Row{
		"s-0": {{Values: []any{int64(5), int64(8)}}},
		"s-1": {{Values: []any{int64(2), int64(9)}}},
		"s-2": {{Values: []any{int64(7), int64(3)}}},
	}}
	rows, err := sg.Execute(ctx, "SELECT min(x), max(x) FROM t", []ShardID{"s-0", "s-1", "s-2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || rows[0].Values[0] != int64(2) || rows[0].Values[1] != int64(9) {
		t.Fatalf("min/max = %v, want [2 9]", rows[0].Values)
	}
}

func TestScatterMergeAggregate_GroupBy(t *testing.T) {
	ctx := context.Background()
	sg := NewScatterGather()
	sg.Merge = MergeAggregate
	// SELECT region, count(*) ... GROUP BY region → 컬럼0=key, 컬럼1=count.
	sg.Aggregates = []AggregateFunc{AggNone, AggCount}
	sg.Shard = &fakeShardExecutor{responses: map[ShardID][]Row{
		"s-0": {{Values: []any{"us", int64(3)}}, {Values: []any{"eu", int64(1)}}},
		"s-1": {{Values: []any{"us", int64(2)}}, {Values: []any{"asia", int64(4)}}},
	}}
	rows, err := sg.Execute(ctx, "SELECT region, count(*) FROM t GROUP BY region", []ShardID{"s-0", "s-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Values[0].(string)] = r.Values[1].(int64)
	}
	want := map[string]int64{"us": 5, "eu": 1, "asia": 4}
	if len(got) != len(want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("group %q count = %d, want %d", k, got[k], v)
		}
	}
}

func TestScatterMergeAggregate_SumNullWhenNoRows(t *testing.T) {
	ctx := context.Background()
	sg := NewScatterGather()
	sg.Merge = MergeAggregate
	sg.Aggregates = []AggregateFunc{AggCount, AggSum}
	// 두 shard 모두 매칭 행 0 → 부분 count=0, sum=NULL.
	sg.Shard = &fakeShardExecutor{responses: map[ShardID][]Row{
		"s-0": {{Values: []any{int64(0), nil}}},
		"s-1": {{Values: []any{int64(0), nil}}},
	}}
	rows, err := sg.Execute(ctx, "SELECT count(*), sum(x) FROM t WHERE 1=0", []ShardID{"s-0", "s-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	// COUNT = 0 (NULL 아님), SUM = NULL(nil).
	if rows[0].Values[0] != int64(0) {
		t.Fatalf("count = %v, want 0", rows[0].Values[0])
	}
	if rows[0].Values[1] != nil {
		t.Fatalf("sum = %v, want nil (SQL NULL)", rows[0].Values[1])
	}
}

func TestScatterMergeAggregate_FloatPromotion(t *testing.T) {
	ctx := context.Background()
	sg := NewScatterGather()
	sg.Merge = MergeAggregate
	sg.Aggregates = []AggregateFunc{AggSum}
	sg.Shard = &fakeShardExecutor{responses: map[ShardID][]Row{
		"s-0": {{Values: []any{int64(10)}}},
		"s-1": {{Values: []any{2.5}}},
	}}
	rows, err := sg.Execute(ctx, "SELECT sum(x) FROM t", []ShardID{"s-0", "s-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f, ok := rows[0].Values[0].(float64); !ok || f != 12.5 {
		t.Fatalf("sum = %v, want 12.5 (float promotion)", rows[0].Values[0])
	}
}

func TestScatterMergeAggregate_GroupByWithLimit(t *testing.T) {
	ctx := context.Background()
	sg := NewScatterGather()
	sg.Merge = MergeAggregate
	sg.Aggregates = []AggregateFunc{AggNone, AggSum}
	sg.Limit = 2 // 그룹 수 제한.
	sg.Shard = &fakeShardExecutor{responses: map[ShardID][]Row{
		"s-0": {{Values: []any{"a", int64(1)}}, {Values: []any{"b", int64(2)}}, {Values: []any{"c", int64(3)}}},
	}}
	rows, err := sg.Execute(ctx, "SELECT k, sum(v) FROM t GROUP BY k", []ShardID{"s-0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows after Limit, got %d", len(rows))
	}
}

// aggregate 미지정(Aggregates 빈)이면 MergeAggregate 여도 concat 으로 안전 fallback.
func TestScatterMergeAggregate_EmptyAggregatesFallsBackToConcat(t *testing.T) {
	ctx := context.Background()
	sg := NewScatterGather()
	sg.Merge = MergeAggregate // Aggregates 미설정.
	sg.Shard = &fakeShardExecutor{responses: map[ShardID][]Row{
		"s-0": {{Values: []any{int64(1)}}},
		"s-1": {{Values: []any{int64(2)}}},
	}}
	rows, err := sg.Execute(ctx, "SELECT x FROM t", []ShardID{"s-0", "s-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("empty Aggregates should concat (2 rows), got %d", len(rows))
	}
	// 순서 무관 확인.
	vals := []int64{rows[0].Values[0].(int64), rows[1].Values[0].(int64)}
	slices.Sort(vals)
	if vals[0] != 1 || vals[1] != 2 {
		t.Fatalf("concat values = %v, want [1 2]", vals)
	}
}
