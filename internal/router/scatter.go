// Package router implements the pg-router multi-shard query path.
//
// 본 file 은 RFC-0004 §2.2 Scenario 2 (scatter-gather) 의 fan-out + gather
// + merge orchestration 이다. 실 wire-protocol forwarding (libpq passthrough)
// 은 ShardExecutor interface 의 *외부 구현체* 로 위임 — 본 패키지는 그것을
// 호출하고 결과를 정책에 따라 merge 만 한다.
//
// Cross-references:
//   - RFC-0004 (pg-router architecture) — §3.1 component decomposition,
//     §2.2 Scenario 2 scatter-gather example, §3.4 partial failure policy.
//   - ADR-0015 (distributed transactions / 2PC primary) — write scatter 는
//     본 path 가 아닌 tx.TwoPhaseCommit coordinator 경로로 위임.
//   - docs/sql/isolation-matrix.md — cross-shard `READ COMMITTED + 2PC` 진본 기준.
package router

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ShardID 는 라우팅 대상 shard 의 논리 식별자. ShardRange CRD `.spec.shardID` 와 1:1.
type ShardID string

// ErrShardFailure 는 1+ shard 가 실패하고 정책이 fail-fast 일 때 반환.
// errors.Is 비교 가능하며, 원인 error 는 fmt.Errorf("...: %w", ErrShardFailure) 로 wrap.
var ErrShardFailure = errors.New("router: one or more shards failed (fail-fast policy)")

// ErrNoShards 는 shards slice 가 빈 경우 반환. caller (planner) 가 단축 처리 시그널.
var ErrNoShards = errors.New("router: no shards specified")

// Row 는 scatter-gather 가 모든 shard 로부터 수집한 결과의 *normalized* 단위.
type Row struct {
	// Shard 는 본 row 가 유래한 shard ID. trace / debug 용도.
	Shard ShardID
	// Values 는 column-ordered raw values.
	Values []any
}

// Executor 는 multi-shard query 실행 contract.
type Executor interface {
	Execute(ctx context.Context, query string, shards []ShardID) ([]Row, error)
}

// ShardExecutor 는 단일 shard 에 query 를 forward 하고 row 응답을 받는 contract.
// 실 구현은 pgconn / libpq passthrough — 본 패키지는 interface 만 의존.
type ShardExecutor interface {
	// ExecuteOne 은 단일 shard 에 query 를 forward 하고 row stream 을 slice 로 반환한다.
	// context 취소 시 즉시 종료, 원인 err 포함.
	ExecuteOne(ctx context.Context, shard ShardID, query string) ([]Row, error)
}

// FailurePolicy 는 부분 shard 실패 시 정책이다.
type FailurePolicy int

const (
	// FailFast — 1+ shard 실패 시 즉시 전체 abort, 다른 in-flight 도 cancel.
	FailFast FailurePolicy = iota
	// BestEffort — 실패한 shard 는 skip, 성공한 shard 의 결과만 반환.
	// caller 가 `application_name=allow_partial` 또는 명시 옵션 설정 시 사용.
	BestEffort
)

// MergeStrategy 는 N shard 응답을 1 결과로 합치는 방식이다.
type MergeStrategy int

const (
	// MergeConcat — 단순 concat (default). UNION ALL 의미.
	MergeConcat MergeStrategy = iota
	// MergeOrderBy — 첫 column 기준 k-way merge (사전식 비교).
	// 실 구현 시 planner 가 ORDER BY column index + direction 을 전달.
	MergeOrderBy
)

// ScatterGather 는 동일 query 를 모든 지정 shard 에 fan-out 하고 gather + merge.
type ScatterGather struct {
	// Shard 는 단일 shard 호출 구현체. nil 이면 NewScatterGather default (no-op stub) 사용.
	Shard ShardExecutor
	// Policy 는 부분 실패 정책 (default FailFast).
	Policy FailurePolicy
	// Merge 는 결과 합치기 전략 (default MergeConcat).
	Merge MergeStrategy
	// Limit 은 >0 이면 merge 결과의 행 수를 제한한다 (LIMIT pushdown 근사 — 각 샤드는
	// 전량 반환하고 merge 후 자른다; 진짜 per-shard LIMIT 주입은 planner 책임).
	Limit int
}

// NewScatterGather 는 ScatterGather 인스턴스를 반환한다. Shard 가 nil 이면
// Execute 호출 시 ErrShardFailure 반환 — 명시 의존성 주입 강제.
func NewScatterGather() *ScatterGather {
	return &ScatterGather{Policy: FailFast, Merge: MergeConcat}
}

// Execute 는 query 를 모든 shards 에 scatter + merge 정책에 따라 합쳐 반환한다.
func (s *ScatterGather) Execute(ctx context.Context, query string, shards []ShardID) ([]Row, error) {
	if len(shards) == 0 {
		return nil, ErrNoShards
	}
	if s.Shard == nil {
		return nil, fmt.Errorf("router: ShardExecutor not configured")
	}

	// Cancellation: FailFast 에서 1 fail 발견 시 다른 in-flight 도 즉시 cancel.
	fanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		shard ShardID
		rows  []Row
		err   error
	}
	resCh := make(chan result, len(shards))
	var wg sync.WaitGroup
	for _, sh := range shards {
		wg.Add(1)
		go func(shard ShardID) {
			defer wg.Done()
			rows, err := s.Shard.ExecuteOne(fanCtx, shard, query)
			resCh <- result{shard: shard, rows: rows, err: err}
		}(sh)
	}
	go func() { wg.Wait(); close(resCh) }()

	collected := make(map[ShardID][]Row, len(shards))
	var failed []ShardID
	for r := range resCh {
		if r.err != nil {
			failed = append(failed, r.shard)
			if s.Policy == FailFast {
				cancel() // 나머지 in-flight cancel
				// drain 중지하지 않고 모두 받기 — 그러나 결과는 무시.
				for range resCh { //nolint:revive
				}
				return nil, fmt.Errorf("%w: shard=%s: %v", ErrShardFailure, r.shard, r.err)
			}
			continue
		}
		collected[r.shard] = r.rows
	}
	if s.Policy == BestEffort && len(collected) == 0 {
		return nil, fmt.Errorf("%w: all %d shards failed", ErrShardFailure, len(failed))
	}
	merged := s.merge(shards, collected)
	if s.Limit > 0 && len(merged) > s.Limit {
		merged = merged[:s.Limit]
	}
	return merged, nil
}

func (s *ScatterGather) merge(order []ShardID, collected map[ShardID][]Row) []Row {
	switch s.Merge {
	case MergeOrderBy:
		return mergeOrderBy(order, collected)
	default:
		return mergeConcat(order, collected)
	}
}

// mergeConcat 는 shards 순서대로 단순 concat — UNION ALL.
func mergeConcat(order []ShardID, collected map[ShardID][]Row) []Row {
	total := 0
	for _, rows := range collected {
		total += len(rows)
	}
	out := make([]Row, 0, total)
	for _, sh := range order {
		out = append(out, collected[sh]...)
	}
	return out
}

// mergeOrderBy 는 첫 column 기준 사전식 k-way merge.
// 각 shard 의 row slice 가 *이미 정렬된* 상태라고 가정 (PG 가 정렬한 결과).
func mergeOrderBy(order []ShardID, collected map[ShardID][]Row) []Row {
	// flatten 후 사전 정렬 — k-way streaming merge 는 future optimization.
	// 정확성 우선, 성능은 후속 turn (large result 시 k-way heap 도입).
	flat := mergeConcat(order, collected)
	sort.SliceStable(flat, func(i, j int) bool {
		return cmpFirstValue(flat[i], flat[j]) < 0
	})
	return flat
}

func cmpFirstValue(a, b Row) int {
	switch {
	case len(a.Values) == 0 && len(b.Values) == 0:
		return 0
	case len(a.Values) == 0:
		return -1
	case len(b.Values) == 0:
		return 1
	}
	return compareValues(a.Values[0], b.Values[0])
}

// compareValues 는 두 값을 *타입 인지* 비교한다 — 숫자는 수치로 비교(문자열 비교 시
// "10" < "9" 가 되는 버그 회피), []byte/문자열은 사전식, 그 외는 %v fallback.
func compareValues(a, b any) int {
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			switch {
			case af < bf:
				return -1
			case af > bf:
				return 1
			default:
				return 0
			}
		}
	}
	return strings.Compare(toStr(a), toStr(b))
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func toStr(v any) string {
	switch s := v.(type) {
	case []byte:
		return string(s)
	case string:
		return s
	}
	return fmt.Sprintf("%v", v)
}

// 컴파일 타임 interface 만족 검사.
var _ Executor = (*ScatterGather)(nil)
