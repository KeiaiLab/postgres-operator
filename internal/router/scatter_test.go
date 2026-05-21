package router

import (
	"context"
	"errors"
	"sort"
	"sync/atomic"
	"testing"
)

// fakeShardExecutor 는 in-memory ShardExecutor — 테스트용.
type fakeShardExecutor struct {
	responses map[ShardID][]Row
	errs      map[ShardID]error
	calls     atomic.Int32
}

func (f *fakeShardExecutor) ExecuteOne(_ context.Context, shard ShardID, _ string) ([]Row, error) {
	f.calls.Add(1)
	if err, ok := f.errs[shard]; ok {
		return nil, err
	}
	return f.responses[shard], nil
}

func TestScatterGather(t *testing.T) {
	ctx := context.Background()

	t.Run("ErrNoShards 빈 list", func(t *testing.T) {
		sg := NewScatterGather()
		sg.Shard = &fakeShardExecutor{}
		_, err := sg.Execute(ctx, "SELECT 1", nil)
		if !errors.Is(err, ErrNoShards) {
			t.Fatalf("want ErrNoShards, got %v", err)
		}
	})

	t.Run("Shard nil 시 명시 error", func(t *testing.T) {
		sg := NewScatterGather()
		_, err := sg.Execute(ctx, "SELECT 1", []ShardID{"s-0"})
		if err == nil {
			t.Fatalf("expected error when Shard not configured")
		}
	})

	t.Run("MergeConcat 정상 fan-out", func(t *testing.T) {
		sg := NewScatterGather()
		sg.Shard = &fakeShardExecutor{
			responses: map[ShardID][]Row{
				"s-0": {{Shard: "s-0", Values: []any{1}}, {Shard: "s-0", Values: []any{3}}},
				"s-1": {{Shard: "s-1", Values: []any{2}}, {Shard: "s-1", Values: []any{4}}},
			},
		}
		rows, err := sg.Execute(ctx, "SELECT id FROM users", []ShardID{"s-0", "s-1"})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if len(rows) != 4 {
			t.Fatalf("rows count want=4 got=%d", len(rows))
		}
		// shard order 보존: s-0 먼저, s-1 뒤.
		if rows[0].Shard != "s-0" || rows[2].Shard != "s-1" {
			t.Fatalf("shard order not preserved: %+v", rows)
		}
	})

	t.Run("MergeOrderBy 첫 column 기준 정렬", func(t *testing.T) {
		sg := NewScatterGather()
		sg.Merge = MergeOrderBy
		sg.Shard = &fakeShardExecutor{
			responses: map[ShardID][]Row{
				"s-0": {{Values: []any{"banana"}}, {Values: []any{"date"}}},
				"s-1": {{Values: []any{"apple"}}, {Values: []any{"cherry"}}},
			},
		}
		rows, err := sg.Execute(ctx, "SELECT name FROM fruits ORDER BY name", []ShardID{"s-0", "s-1"})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got := make([]string, len(rows))
		for i, r := range rows {
			got[i] = r.Values[0].(string)
		}
		want := []string{"apple", "banana", "cherry", "date"}
		if !sort.StringsAreSorted(got) {
			t.Fatalf("not sorted: %v", got)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("sorted result mismatch want=%v got=%v", want, got)
			}
		}
	})

	t.Run("FailFast 1 shard 실패 즉시 ErrShardFailure", func(t *testing.T) {
		sg := NewScatterGather()
		sg.Shard = &fakeShardExecutor{
			responses: map[ShardID][]Row{"s-0": {{Values: []any{1}}}},
			errs:      map[ShardID]error{"s-1": errors.New("simulated")},
		}
		_, err := sg.Execute(ctx, "SELECT 1", []ShardID{"s-0", "s-1"})
		if !errors.Is(err, ErrShardFailure) {
			t.Fatalf("want ErrShardFailure, got %v", err)
		}
	})

	t.Run("BestEffort 부분 실패 성공 shard 결과만", func(t *testing.T) {
		sg := NewScatterGather()
		sg.Policy = BestEffort
		sg.Shard = &fakeShardExecutor{
			responses: map[ShardID][]Row{"s-0": {{Shard: "s-0", Values: []any{1}}}},
			errs:      map[ShardID]error{"s-1": errors.New("network")},
		}
		rows, err := sg.Execute(ctx, "SELECT 1", []ShardID{"s-0", "s-1"})
		if err != nil {
			t.Fatalf("BestEffort 부분 실패 무시 expected, got %v", err)
		}
		if len(rows) != 1 || rows[0].Shard != "s-0" {
			t.Fatalf("BestEffort partial 결과 mismatch: %+v", rows)
		}
	})

	t.Run("BestEffort 전체 실패는 ErrShardFailure", func(t *testing.T) {
		sg := NewScatterGather()
		sg.Policy = BestEffort
		sg.Shard = &fakeShardExecutor{
			errs: map[ShardID]error{"s-0": errors.New("e0"), "s-1": errors.New("e1")},
		}
		_, err := sg.Execute(ctx, "SELECT 1", []ShardID{"s-0", "s-1"})
		if !errors.Is(err, ErrShardFailure) {
			t.Fatalf("want ErrShardFailure on all-fail, got %v", err)
		}
	})

	t.Run("결정성: 동일 입력 → 동일 출력 (MergeConcat shard order)", func(t *testing.T) {
		sg := NewScatterGather()
		sg.Shard = &fakeShardExecutor{
			responses: map[ShardID][]Row{
				"a": {{Values: []any{1}}}, "b": {{Values: []any{2}}}, "c": {{Values: []any{3}}},
			},
		}
		shards := []ShardID{"a", "b", "c"}
		r1, _ := sg.Execute(ctx, "q", shards)
		r2, _ := sg.Execute(ctx, "q", shards)
		if len(r1) != len(r2) {
			t.Fatalf("non-deterministic length")
		}
		for i := range r1 {
			if r1[i].Shard != r2[i].Shard {
				t.Fatalf("non-deterministic order at %d", i)
			}
		}
	})

	t.Run("Executor 인터페이스 만족 compile-time", func(t *testing.T) {
		var _ Executor = (*ScatterGather)(nil)
	})
}
