/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package router — SQLShardExecutor 는 ShardExecutor 의 lib/pq 실 구현이다.
//
// scatter.go 의 ScatterGather 는 ShardExecutor interface 에만 의존하고 실
// shard 호출을 외부 구현에 위임한다 (RFC-0004 §3.1). 본 file 이 그 *라이브
// consumer* — 각 shard 의 PostgreSQL DSN 으로 database/sql(lib/pq) 연결하여
// query 를 실행하고 결과를 router.Row 로 정규화한다.
//
// *연결 풀링*: shard 별 *sql.DB(자체가 커넥션 풀)를 캐시하여 재사용한다. 호출마다
// sql.Open/Close 하면 fan-out 부하에서 연결 폭주·지연이 발생하므로, lazy 하게 풀을
// 열고 유지한다. *sql.DB 는 동시성 안전하다.
package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "github.com/lib/pq" // postgres driver — sql.Open("postgres", ...) 등록용 (instance-manager 와 동일)
)

// 기본 풀 매개변수 (필드가 0 일 때 적용). 라우터 fan-out 에 보수적 기본값.
const (
	defaultMaxOpenConns    = 10
	defaultMaxIdleConns    = 5
	defaultConnMaxIdleTime = 5 * time.Minute
	defaultConnMaxLifetime = 30 * time.Minute
)

// SQLShardExecutor 는 shard 별 DSN 으로 *sql.DB 풀을 캐시·재사용하여 query 를 실행한다.
type SQLShardExecutor struct {
	// DSNs 는 shard → PostgreSQL DSN ("postgres://user:pw@host:port/db?sslmode=...").
	DSNs map[ShardID]string
	// MaxOpenConns / MaxIdleConns / ConnMaxIdleTime / ConnMaxLifetime 은 shard 풀 튜닝
	// (0 = 기본값). ConnMaxLifetime 은 재시작된 backend(예: failover 후)로의 stale 연결을
	// 주기적으로 폐기한다.
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxIdleTime time.Duration
	ConnMaxLifetime time.Duration

	mu    sync.Mutex
	pools map[ShardID]*sql.DB
}

// ErrNoDSN 은 shard 에 대응하는 DSN 이 없을 때 반환된다.
var ErrNoDSN = errors.New("router: no DSN configured for shard")

// pool 은 shard 의 *sql.DB 를 lazy 하게 열어 캐시·반환한다 (재호출 시 동일 풀).
func (e *SQLShardExecutor) pool(shard ShardID) (*sql.DB, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if db, ok := e.pools[shard]; ok {
		return db, nil
	}
	dsn, ok := e.DSNs[shard]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoDSN, shard)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("router: open shard %s: %w", shard, err)
	}
	db.SetMaxOpenConns(orDefault(e.MaxOpenConns, defaultMaxOpenConns))
	db.SetMaxIdleConns(orDefault(e.MaxIdleConns, defaultMaxIdleConns))
	if e.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(e.ConnMaxIdleTime)
	} else {
		db.SetConnMaxIdleTime(defaultConnMaxIdleTime)
	}
	if e.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(e.ConnMaxLifetime)
	} else {
		db.SetConnMaxLifetime(defaultConnMaxLifetime)
	}
	if e.pools == nil {
		e.pools = make(map[ShardID]*sql.DB)
	}
	e.pools[shard] = db
	return db, nil
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// ExecuteOne 은 단일 shard 의 풀에서 query 를 실행하고 row 를 router.Row 로 정규화한다.
// context 취소 시 즉시 종료한다 (ScatterGather FailFast cancel 전파).
func (e *SQLShardExecutor) ExecuteOne(ctx context.Context, shard ShardID, query string) ([]Row, error) {
	db, err := e.pool(shard)
	if err != nil {
		return nil, err
	}

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

// Close 는 모든 shard 풀을 닫는다 (graceful shutdown).
func (e *SQLShardExecutor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var firstErr error
	for shard, db := range e.pools {
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("router: close shard %s pool: %w", shard, err)
		}
	}
	e.pools = nil
	return firstErr
}

// 컴파일 타임 interface 만족 검사.
var _ ShardExecutor = (*SQLShardExecutor)(nil)
