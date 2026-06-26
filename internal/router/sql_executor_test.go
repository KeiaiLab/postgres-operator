/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"context"
	"errors"
	"testing"
)

// TestSQLShardExecutor_PoolReuse 는 동일 shard 가 같은 *sql.DB 풀을 재사용하고
// (호출마다 sql.Open 하지 않음), Close 가 풀을 비움을 검증한다. sql.Open 은 lazy 라
// 라이브 PG 없이 검증 가능.
func TestSQLShardExecutor_PoolReuse(t *testing.T) {
	e := &SQLShardExecutor{DSNs: map[ShardID]string{"shard-0": "postgres://u@h:5432/db?sslmode=disable"}}
	db1, err := e.pool("shard-0")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	db2, err := e.pool("shard-0")
	if err != nil {
		t.Fatalf("pool(2): %v", err)
	}
	if db1 != db2 {
		t.Fatal("pool not reused: different *sql.DB per call")
	}
	if _, err := e.pool("shard-9"); !errors.Is(err, ErrNoDSN) {
		t.Fatalf("pool(missing) = %v, want ErrNoDSN", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if e.pools != nil {
		t.Fatal("Close did not clear pools")
	}
}

// TestSQLShardExecutor_NoDSN 은 shard 에 대응하는 DSN 이 없으면 ErrNoDSN 을
// 반환함을 검증한다 (라이브 PG 없이 검증 가능한 경로 — 실 query 는 라이브 e2e).
func TestSQLShardExecutor_NoDSN(t *testing.T) {
	e := &SQLShardExecutor{DSNs: map[ShardID]string{"shard-0": "postgres://x"}}
	_, err := e.ExecuteOne(context.Background(), "shard-1", "SELECT 1")
	if !errors.Is(err, ErrNoDSN) {
		t.Fatalf("ExecuteOne(missing shard) = %v, want ErrNoDSN", err)
	}
}

// TestSQLShardExecutor_SatisfiesInterface 는 ScatterGather 의 ShardExecutor 로
// 주입 가능함을 컴파일+런타임에서 확인한다 (라이브 consumer 결선).
func TestSQLShardExecutor_SatisfiesInterface(t *testing.T) {
	var _ ShardExecutor = &SQLShardExecutor{}
	sg := &ScatterGather{Shard: &SQLShardExecutor{DSNs: map[ShardID]string{}}, Policy: FailFast, Merge: MergeConcat}
	// shard 0개 → ErrNoShards (executor 호출 전 단축).
	if _, err := sg.Execute(context.Background(), "SELECT 1", nil); !errors.Is(err, ErrNoShards) {
		t.Fatalf("Execute(no shards) = %v, want ErrNoShards", err)
	}
}
