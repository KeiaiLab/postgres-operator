// Package router implements the pg-router multi-shard query path.
//
// 본 file 은 RFC-0004 §2.2 Scenario 2 (scatter-gather, P3+) 의 *skeleton* 이다.
// 실 wire-protocol forwarding + result merge 는 future phase (P3 ~ v0.6.0) 에서
// pg_query_go planner + per-shard connection pool 위에 구현된다.
//
// Cross-references:
//   - RFC-0004 (pg-router architecture) — §3.1 component decomposition,
//     §2.2 Scenario 2 scatter-gather example.
//   - ADR-0015 (distributed transactions / 2PC primary) — multi-shard write 경로
//     의 2PC coordinator 정합. 본 skeleton 은 read-only scatter 만 다루며 write
//     scatter 는 ADR-0015 의 2PC prepare/commit 경로에 위임한다.
//   - docs/sql/isolation-matrix.md — cross-shard `READ COMMITTED + 2PC` 진본
//     기준. 본 path 는 *non-transactional read* 또는 *single-snapshot read* 이며,
//     repeatable-read 류 isolation 은 ADR-0015 coordinator 경로로 강등 routing.
package router

import (
	"context"
	"errors"
)

// ErrNotImplemented 는 scatter-gather 의 실 구현이 future phase 임을 표시하는
// sentinel error 이다. compile-time interface freeze 용도로 test 에서 직접
// 비교한다 (`errors.Is`).
var ErrNotImplemented = errors.New("router: scatter-gather not implemented (RFC-0004 P3+)")

// ShardID 는 라우팅 대상 shard 의 논리 식별자다. 실 구현 시 ShardRange CRD 의
// `.spec.shardID` 와 1:1 대응한다 (RFC-0002 §3).
type ShardID string

// Row 는 scatter-gather 가 모든 shard 로부터 수집한 결과의 *normalized* 단위다.
// 실 구현 시 pgproto3.DataRow 또는 driver-neutral []any 로 확장된다.
type Row struct {
	// Shard 는 본 row 가 유래한 shard ID 다. 동일 query 가 N shard 에 scatter 될
	// 때 trace / debug 용도. merge 시점에 stripped 되거나 metadata 로 attach 된다.
	Shard ShardID

	// Values 는 column-ordered raw values. 실 구현 시 wire format (text/binary)
	// 보존을 위해 []byte 또는 pgtype 으로 변경 예정.
	Values []any
}

// Executor 는 multi-shard query 실행 contract 다. compile-time interface freeze
// 를 위해 본 type 을 노출한다. 실 구현은 *ScatterGather 가 본 interface 를 만족.
type Executor interface {
	Execute(ctx context.Context, query string, shards []ShardID) ([]Row, error)
}

// ScatterGather 는 *동일 query* 를 모든 지정 shard 에 fan-out 하고, 응답을
// gather + merge 하는 executor 다.
//
// 실 구현 단계 (future):
//  1. wire frontend 가 SQL 을 parse → planner 가 multi-shard 판정 → 본 type 으로 위임.
//  2. per-shard connection pool 에서 N goroutine fan-out (`errgroup`).
//  3. 각 shard 응답을 streaming merge — `ORDER BY` 시 k-way merge, aggregate 시
//     re-aggregation (SUM/COUNT/MIN/MAX), 일반 시 그대로 concat.
//  4. 부분 shard failure 정책: RFC-0004 §3.4 에 따라 fail-fast (default) 또는
//     `application_name=allow_partial` 시 best-effort.
//  5. ADR-0015 정합: write scatter 는 본 path 가 직접 처리하지 않고 dtxn
//     coordinator (2PC) 로 위임. 본 type 은 read-only 만.
type ScatterGather struct {
	// 실 구현 시 per-shard connection pool / planner / metrics sink 주입.
	// 현 skeleton 단계는 의도적으로 empty — 향후 dependency-injection point.
}

// NewScatterGather 는 skeleton constructor 다. future 단계에서 pool / planner /
// observability 의존성을 매개변수로 받는다.
func NewScatterGather() *ScatterGather {
	return &ScatterGather{}
}

// Execute 는 query 를 모든 shards 에 scatter 하고 결과를 gather + merge 한다.
//
// 현 skeleton 은 *항상* ErrNotImplemented 를 반환한다. 본 method 시그니처는
// interface freeze 용도이며, 실 구현 전까지 caller (planner) 가 본 path 로
// route 하지 않도록 RFC-0004 §3.3 의 vindex 평가 단계에서 차단된다.
func (s *ScatterGather) Execute(ctx context.Context, query string, shards []ShardID) ([]Row, error) {
	return nil, ErrNotImplemented
}

// 컴파일 타임 interface 만족 검사 — *ScatterGather 가 Executor 를 만족하지
// 못하면 build fail.
var _ Executor = (*ScatterGather)(nil)
