/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package supervise

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// connect 는 lib/pq DB 핸들을 lazy-open 한다. *sql.DB 는 connection pool 을
// 내장하므로 단일 핸들을 재사용 — 모든 SQL 메서드가 본 헬퍼를 통해 동일 핸들을
// 받는다.
func (r *Real) connect() (*sql.DB, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		return r.db, nil
	}
	db, err := sql.Open("postgres", r.cfg.LocalDSN)
	if err != nil {
		return nil, fmt.Errorf("supervise: sql.Open: %w", err)
	}
	r.db = db
	return db, nil
}

// setDB 는 테스트 hook — sqlmock DB 를 주입한다. production 코드에서는 호출하지 않는다.
func (r *Real) setDB(db *sql.DB) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.db = db
}

// Promote 는 standby 를 primary 로 promote 한다.
//
// SQL: SELECT pg_promote(wait => true, wait_seconds => 30)
//
// PG14+ 에서 wait 옵션이 동기 검증을 제공한다. boolean false 는 timeout 또는
// 이미 primary 임을 의미 — 두 경우 모두 *호출자가 의도한 promote 가 일어나지
// 않은 상태* 이므로 error 로 보고한다.
func (r *Real) Promote(ctx context.Context) error {
	db, err := r.connect()
	if err != nil {
		return err
	}
	// pg_is_in_recovery() == false 면 이미 primary — fresh initdb 직후의 경로이며
	// pg_promote 호출 시 false 반환되어 error 처리되는 함정 회피. 멱등성 보장.
	var inRecovery bool
	if err := db.QueryRowContext(ctx, "SELECT pg_is_in_recovery()").Scan(&inRecovery); err != nil {
		return fmt.Errorf("supervise: pg_is_in_recovery: %w", err)
	}
	if !inRecovery {
		return nil
	}
	var ok bool
	if err := db.QueryRowContext(ctx, "SELECT pg_promote(true, 30)").Scan(&ok); err != nil {
		return fmt.Errorf("supervise: pg_promote: %w", err)
	}
	if !ok {
		return errors.New("supervise: pg_promote returned false (timeout)")
	}
	return nil
}

// CreateReplicationSlot 은 standby 별 physical replication slot 을 생성한다.
// 이미 존재하면 no-op (idempotent).
//
// 시퀀스:
//  1. SELECT 1 FROM pg_replication_slots WHERE slot_name = $1
//  2. row 1 이면 return nil (이미 존재).
//  3. ErrNoRows 이면 SELECT pg_create_physical_replication_slot($1, true, false).
//
// 매개변수 (immediately_reserve=true, temporary=false):
//   - immediately_reserve: WAL position 즉시 보존. standby 가 늦게 붙어도
//     primary 가 WAL 을 보존하기 시작 (slot lifecycle 가 결정한다).
//   - temporary=false: 영구 slot (Pod 재시작 후에도 보존).
func (r *Real) CreateReplicationSlot(ctx context.Context, slotName string) error {
	if slotName == "" {
		return errors.New("supervise: slotName must not be empty")
	}
	db, err := r.connect()
	if err != nil {
		return err
	}
	var exists int
	err = db.QueryRowContext(ctx,
		"SELECT 1 FROM pg_replication_slots WHERE slot_name = $1", slotName).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("supervise: pg_replication_slots query: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		"SELECT pg_create_physical_replication_slot($1, true, false)", slotName); err != nil {
		return fmt.Errorf("supervise: pg_create_physical_replication_slot: %w", err)
	}
	return nil
}

// DropReplicationSlot 은 slot 을 회수한다. 부재 시 no-op (idempotent).
func (r *Real) DropReplicationSlot(ctx context.Context, slotName string) error {
	if slotName == "" {
		return errors.New("supervise: slotName must not be empty")
	}
	db, err := r.connect()
	if err != nil {
		return err
	}
	var exists int
	err = db.QueryRowContext(ctx,
		"SELECT 1 FROM pg_replication_slots WHERE slot_name = $1", slotName).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("supervise: pg_replication_slots query: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		"SELECT pg_drop_replication_slot($1)", slotName); err != nil {
		return fmt.Errorf("supervise: pg_drop_replication_slot: %w", err)
	}
	return nil
}

// IsReady 는 SELECT 1 round-trip 으로 postgres 응답 확인. connection / SQL
// 실패는 false 반환 — readyz handler 가 boolean 만 사용하므로 error 표면화 불요.
func (r *Real) IsReady(ctx context.Context) bool {
	db, err := r.connect()
	if err != nil {
		return false
	}
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return false
	}
	return one == 1
}

// LagBytes 는 WAL lag 를 bytes 단위로 측정한다. primary / replica 분기는
// pg_is_in_recovery() 결과로 결정.
//
// primary: pg_stat_replication 의 max replication lag — flush_lsn 기준.
//
//	replica 가 0 개거나 미연결이면 COALESCE 로 0 반환.
//
// replica: 자기가 받은 WAL (last_receive) 과 적용한 WAL (last_replay) 의 차이.
//
// 쿼리 실패 시 -1 반환 — 호출자 (status reporter) 가 N/A 로 표기.
func (r *Real) LagBytes(ctx context.Context) int64 {
	db, err := r.connect()
	if err != nil {
		return -1
	}
	var inRecovery bool
	if err := db.QueryRowContext(ctx, "SELECT pg_is_in_recovery()").Scan(&inRecovery); err != nil {
		return -1
	}
	var lag int64
	if inRecovery {
		const q = `SELECT pg_wal_lsn_diff(COALESCE(pg_last_wal_receive_lsn(), '0/0'), COALESCE(pg_last_wal_replay_lsn(), '0/0'))::bigint`
		if err := db.QueryRowContext(ctx, q).Scan(&lag); err != nil {
			return -1
		}
		return lag
	}
	const q = `SELECT COALESCE(MAX(pg_wal_lsn_diff(pg_current_wal_lsn(), flush_lsn))::bigint, 0) FROM pg_stat_replication`
	if err := db.QueryRowContext(ctx, q).Scan(&lag); err != nil {
		return -1
	}
	return lag
}
