/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package router

import (
	"context"
	"errors"
	"testing"
)

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
