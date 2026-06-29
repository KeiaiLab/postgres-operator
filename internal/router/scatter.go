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
	// Limit 은 >0 이면 merge 결과의 행 수를 제한한다.
	Limit int
	// OrderByCol 은 MergeOrderBy 의 정렬 기준 컬럼 index (default 0). planner 가 ORDER BY
	// 컬럼 위치를 전달.
	OrderByCol int
	// OrderByDesc 는 MergeOrderBy 를 내림차순으로 정렬한다 (default 오름차순).
	OrderByDesc bool
	// PushDownLimit 가 true + Limit>0 이면 각 샤드 query 에 `LIMIT n` 을 주입해 샤드별
	// 전송량을 줄인다 (이미 LIMIT 가 있거나 다중문이면 건드리지 않음). merge 후 Limit 가
	// 최종 cap 으로 다시 적용된다.
	PushDownLimit bool
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

	// LIMIT pushdown: 각 샤드 전송량을 줄인다 (보수적 — 기존 LIMIT/다중문은 건드리지 않음).
	if s.PushDownLimit && s.Limit > 0 {
		query = withLimitPushdown(query, s.Limit)
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
	if s.Merge != MergeOrderBy {
		return mergeConcat(order, collected)
	}
	// 각 shard 의 row 는 이미 정렬(PG 가 ORDER BY 처리)되어 있다고 가정 — flatten 후
	// 지정 컬럼/방향으로 안정 정렬. (k-way streaming merge 는 large result 시 future.)
	flat := mergeConcat(order, collected)
	sort.SliceStable(flat, func(i, j int) bool {
		c := cmpAtCol(flat[i], flat[j], s.OrderByCol)
		if s.OrderByDesc {
			return c > 0
		}
		return c < 0
	})
	return flat
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

// cmpAtCol 은 두 Row 를 col 번째 컬럼 값으로 비교한다. NULL 은 *가장 큼* 으로 취급해
// PostgreSQL 기본 정렬과 일치시킨다 — ASC 면 NULLS LAST, DESC 면 NULLS FIRST.
func cmpAtCol(a, b Row, col int) int {
	av, bv := valueAt(a, col), valueAt(b, col)
	switch {
	case av == nil && bv == nil:
		return 0
	case av == nil:
		return 1 // nil 이 더 큼.
	case bv == nil:
		return -1
	}
	return compareValues(av, bv)
}

// valueAt 은 Row 의 col 번째 값을 반환한다 (범위 밖이면 nil).
func valueAt(r Row, col int) any {
	if col >= 0 && col < len(r.Values) {
		return r.Values[col]
	}
	return nil
}

// withLimitPushdown 은 query 에 `LIMIT n` 을 주입한다. 이미 LIMIT 가 있거나 다중문
// (top-level `;`)이면 안전을 위해 그대로 둔다. ORDER BY 가 있으면 `... ORDER BY x
// LIMIT n` 이 되어 각 샤드의 top-n 만 받고 merge 후 다시 cap 하므로 정확하다.
func withLimitPushdown(query string, n int) string {
	q := strings.TrimRight(strings.TrimSpace(query), "; \t\n")
	if strings.Contains(q, ";") || hasLimitClause(q) {
		return query
	}
	return fmt.Sprintf("%s LIMIT %d", q, n)
}

// hasLimitClause 는 query 에 top-level LIMIT 키워드가 있는지 토큰 단위로 본다
// (문자열/주석 내부 'limit' 오인 방지).
func hasLimitClause(query string) bool {
	for _, t := range tokenize(query) {
		if t.kind == tokIdent && strings.EqualFold(t.text, "limit") {
			return true
		}
	}
	return false
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
