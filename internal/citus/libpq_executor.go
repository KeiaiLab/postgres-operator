/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package citus

import (
	"context"
	"database/sql"
	"fmt"

	// PostgreSQL driver (lib/pq, MIT). database/sql에 "postgres" driver 등록.
	_ "github.com/lib/pq"
)

// citusSyncTopologyLockID는 LibPQExecutor가 동시 reconcile 직렬화에 사용하는
// pg_advisory_xact_lock ID다.
//
// RFC 0002 §위험 §완화 — 동시 reconcile loop가 같은 cluster의 pg_dist_node를
// 동시 수정하면 split-brain (coordinator는 N+1 노드 인지, worker는 N 노드만)이
// 발생 가능. advisory_xact_lock은 트랜잭션 커밋/롤백과 함께 자동 해제되므로
// reconciler가 crash해도 lock leak 없음.
//
// 값: "citussync"의 ASCII bytes를 hex int64로. operator 외 다른 도구가 동일 ID를
// 잡지 않을 정도의 충돌 회피.
const citusSyncTopologyLockID int64 = 0x6369_7475_5f73_796e

// LibPQExecutor는 lib/pq driver로 actions를 실 PostgreSQL에 적용하는 SQLExecutor.
// RFC 0002 §6 production path. NullExecutor (P11-M0 spike default)의 production 대체.
//
// 사용 (cmd/main.go에서 PostgresClusterReconciler.CitusExec 주입 예정, P0-6 phase 2):
//
//	exec := &citus.LibPQExecutor{
//	    DSNFunc: func(ctx context.Context) (string, error) {
//	        // P2 election holder 식별 + Secret에서 인증 정보 → DSN 합성
//	        return "host=... port=5432 user=postgres dbname=postgres sslmode=require ...", nil
//	    },
//	}
//	reconciler.CitusExec = exec
//
// 트랜잭션 모델:
//  1. tx 시작
//  2. pg_advisory_xact_lock(citusSyncTopologyLockID) — 동시 reconcile 직렬화
//  3. actions 입력 순서대로 적용 (ComputeActions가 remove→update→add 정렬 보장)
//  4. 부분 실패 시 첫 실패에서 rollback + error 반환. reconciler가 다음 reconcile
//     주기에 ComputeActions를 재계산해 잔여 Action을 자동 적용 (멱등성, RFC 0002 §위험)
type LibPQExecutor struct {
	// DSNFunc는 매 Apply 호출마다 fresh DSN을 반환한다.
	//
	// 매번 lookup하는 이유: coordinator primary가 P2 election lease holder에 의해
	// 변경 가능. fresh DSN은 항상 *현재* primary를 가리킴. nil이면 Apply가
	// 즉시 error 반환.
	DSNFunc func(ctx context.Context) (string, error)

	// open은 sql.Open의 testable 시점이다. nil이면 표준 sql.Open 사용.
	// 단위 테스트가 fake driver를 주입할 때 사용.
	open func(driverName, dsn string) (*sql.DB, error)
}

// Apply는 actions를 단일 트랜잭션 내에서 입력 순서대로 적용한다.
//
// 멱등성 / 부분 실패 회복:
//   - len(actions) == 0이면 즉시 nil 반환 (DB open 시도 안 함)
//   - 부분 실패 시 첫 실패에서 rollback. 다음 reconcile이 ComputeActions로 잔여
//     계산 후 재시도. ComputeActions가 결정적이므로 멱등.
//
// 동시성:
//   - pg_advisory_xact_lock은 트랜잭션 종료(commit/rollback) 시 자동 해제.
//   - reconciler crash 시에도 lock leak 없음.
func (e *LibPQExecutor) Apply(ctx context.Context, actions []Action) error {
	if len(actions) == 0 {
		return nil
	}
	if e.DSNFunc == nil {
		return fmt.Errorf("LibPQExecutor.DSNFunc must be set")
	}

	dsn, err := e.DSNFunc(ctx)
	if err != nil {
		return fmt.Errorf("DSNFunc: %w", err)
	}

	openFn := e.open
	if openFn == nil {
		openFn = sql.Open
	}

	db, err := openFn("postgres", dsn)
	if err != nil {
		return fmt.Errorf("sql.Open postgres: %w", err)
	}
	defer func() { _ = db.Close() }()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 동시 reconcile 직렬화 (RFC 0002 §위험 §완화).
	if _, err := tx.ExecContext(ctx,
		"SELECT pg_advisory_xact_lock($1)", citusSyncTopologyLockID); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}

	for i, action := range actions {
		query, args, qerr := actionToSQL(action)
		if qerr != nil {
			return fmt.Errorf("action[%d] %s %s: %w", i, action.Op, action.Node.Name, qerr)
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("action[%d] %s %s exec: %w",
				i, action.Op, action.Node.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// actionToSQL은 Action을 PostgreSQL Citus SQL 함수 호출 + lib/pq parameter로
// 변환한다.
//
// 매핑 (RFC 0002 §6, Citus 11+ positional 시그니처 사용):
//   - OpAdd    → SELECT citus_add_node($1, $2, %d, 'primary', 'default')
//   - OpRemove → SELECT citus_remove_node($1, $2)
//   - OpUpdate → SELECT citus_set_node_property($1, $2, 'shouldhaveshards', %t)
//     (현 phase 1은 ShouldHaveShards 1 property만. phase 2에서 nodecluster /
//     noderole 등 확장.)
//
// SQL injection 방지:
//   - nodename(string)/nodeport(int) → lib/pq parameter binding ($1, $2)
//   - groupid(int) / ShouldHaveShards(bool) → controlled value (CR 또는 internal
//     계산 결과)이므로 sprintf %d/%t inline. 외부 입력 없음.
//
// 본 phase 1 simplification: OpAdd는 ShouldHaveShards를 *Citus 기본값*으로 두고,
// 같은 노드의 ShouldHaveShards가 desired와 다르면 다음 reconcile에서 OpUpdate가
// 정확히 set. ComputeActions의 결정성 + 멱등성이 이 전략을 보장.
func actionToSQL(a Action) (string, []any, error) {
	n := a.Node
	switch a.Op {
	case OpAdd:
		query := fmt.Sprintf(
			"SELECT citus_add_node($1, $2, %d, 'primary', 'default')",
			n.Group,
		)
		return query, []any{n.Name, int(n.Port)}, nil
	case OpRemove:
		return "SELECT citus_remove_node($1, $2)",
			[]any{n.Name, int(n.Port)}, nil
	case OpUpdate:
		query := fmt.Sprintf(
			"SELECT citus_set_node_property($1, $2, 'shouldhaveshards', %t)",
			n.ShouldHaveShards,
		)
		return query, []any{n.Name, int(n.Port)}, nil
	default:
		return "", nil, fmt.Errorf("unknown Op: %q", a.Op)
	}
}

// 컴파일 가드 — LibPQExecutor가 SQLExecutor 인터페이스를 만족함을 빌드 시점 보장.
var _ SQLExecutor = (*LibPQExecutor)(nil)
