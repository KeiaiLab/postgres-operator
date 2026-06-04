// Package router — SQLShardExecutor 는 ShardExecutor 의 lib/pq 실 구현이다.
//
// scatter.go 의 ScatterGather 는 ShardExecutor interface 에만 의존하고 실
// shard 호출을 외부 구현에 위임한다 (RFC-0004 §3.1). 본 file 이 그 *라이브
// consumer* — 각 shard 의 PostgreSQL DSN 으로 database/sql(lib/pq) 연결하여
// query 를 실행하고 결과를 router.Row 로 정규화한다. (G3 scatter-gather 라이브
// 경로 — 이전에는 fakeShardExecutor(테스트 stub)만 존재했다.)
package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/lib/pq" // postgres driver — sql.Open("postgres", ...) 등록용 (instance-manager 와 동일)
)

// SQLShardExecutor 는 shard 별 DSN 으로 database/sql 연결하여 query 를 실행한다.
//
// 단순성 우선 (RFC-0004 PoC 단계): 호출마다 sql.Open + Close. connection
// pooling / prepared statement 캐시는 성능 측정 후 도입 (future). DSN 은
// operator 가 ShardRange CRD 의 shard endpoint 로부터 합성하여 주입한다.
type SQLShardExecutor struct {
	// DSNs 는 shard → PostgreSQL DSN ("postgres://user:pw@host:port/db?sslmode=...").
	DSNs map[ShardID]string
}

// ErrNoDSN 은 shard 에 대응하는 DSN 이 없을 때 반환된다.
var ErrNoDSN = errors.New("router: no DSN configured for shard")

// ExecuteOne 은 단일 shard 에 query 를 실행하고 row 를 router.Row 로 정규화한다.
// context 취소 시 즉시 종료한다 (ScatterGather FailFast cancel 전파).
func (e *SQLShardExecutor) ExecuteOne(ctx context.Context, shard ShardID, query string) ([]Row, error) {
	dsn, ok := e.DSNs[shard]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoDSN, shard)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("router: open shard %s: %w", shard, err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("router: query shard %s: %w", shard, err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("router: columns shard %s: %w", shard, err)
	}
	var out []Row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("router: scan shard %s: %w", shard, err)
		}
		out = append(out, Row{Shard: shard, Values: vals})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("router: rows shard %s: %w", shard, err)
	}
	return out, nil
}

// 컴파일 타임 interface 만족 검사.
var _ ShardExecutor = (*SQLShardExecutor)(nil)
