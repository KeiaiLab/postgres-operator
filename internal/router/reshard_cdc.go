/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package router — reshard_cdc.go 는 online resharding 의 *CDC 증분 catch-up* 빌딩블록이다.
//
// 무중단 resharding: InitialCopy(bulk)만으로는 복사 중 source 에 들어온 라이브 쓰기를 놓친다.
// PostgreSQL 논리복제(PUBLICATION/SUBSCRIPTION)로 그 변경분을 target 에 따라잡게 한다.
//
// 접근(정합성 우선·단순): subscription 을 copy_data=true 로 만들면 PG 가 *일관된 초기 복사 +
// 지속 스트림* 을 모두 보장한다(exported-snapshot 조율 불요). 단 hash vindex 는 PG row-filter
// 로 표현 불가하므로 target 은 *전 테이블* 을 받는다 — cutover 시 lag→0 확인 후 subscription 을
// 끊고 `DeleteForeignRange` 로 자기 범위 밖 row 를 지운다(2× 복사 비용↔정합성·무중단 trade).
//
// 전제: source 의 wal_level=logical (builders.go 기본값). 자격은 클러스터 내부 trust(pg_hba).
package router

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/lib/pq" // postgres driver

	"github.com/keiailab/postgres-operator/api/v1alpha1"
)

// pubSubNamePattern 은 publication/subscription 식별자 화이트리스트(injection 차단 — 이름은
// placeholder 바인딩 불가).
var pubSubNamePattern = tableNamePattern

// EnsureSchema 는 각 table 을 source 의 정의로 target 에 만든다(subscription copy_data=true 는
// target 에 테이블이 이미 있어야 하므로 — DDL 우선).
func EnsureSchema(ctx context.Context, sourceDSN, targetDSN string, tables []string) error {
	src, err := sql.Open("postgres", sourceDSN)
	if err != nil {
		return fmt.Errorf("router: open source: %w", err)
	}
	defer func() { _ = src.Close() }()
	tgt, err := sql.Open("postgres", targetDSN)
	if err != nil {
		return fmt.Errorf("router: open target: %w", err)
	}
	defer func() { _ = tgt.Close() }()
	for _, t := range tables {
		if !tableNamePattern.MatchString(t) {
			return fmt.Errorf("%w: %q", ErrInvalidTable, t)
		}
		if err := ensureTargetTable(ctx, src, tgt, t); err != nil {
			return err
		}
	}
	return nil
}

// CreatePublication 은 source 에 지정 테이블(빈 목록이면 전 테이블)의 publication 을 멱등
// 생성한다. *이미 있으면 skip* — Job 재시도 시 DROP 하면 활성 subscription 이 의존하는
// publication 을 떨어뜨려 스트림이 깨지므로 drop 하지 않는다(CREATE PUBLICATION 은 IF NOT
// EXISTS 미지원이라 명시 확인).
func CreatePublication(ctx context.Context, sourceDSN, pubName string, tables []string) error {
	if !pubSubNamePattern.MatchString(pubName) {
		return fmt.Errorf("%w: publication %q", ErrInvalidTable, pubName)
	}
	for _, t := range tables {
		if !tableNamePattern.MatchString(t) {
			return fmt.Errorf("%w: table %q", ErrInvalidTable, t)
		}
	}
	db, err := sql.Open("postgres", sourceDSN)
	if err != nil {
		return fmt.Errorf("router: open source: %w", err)
	}
	defer func() { _ = db.Close() }()

	var exists int
	if err := db.QueryRowContext(ctx, "SELECT 1 FROM pg_publication WHERE pubname = $1", pubName).Scan(&exists); err == nil {
		return nil // 이미 존재 — 재사용(활성 sub 보존).
	} else if err != sql.ErrNoRows {
		return fmt.Errorf("router: check publication: %w", err)
	}
	target := "FOR ALL TABLES"
	if len(tables) > 0 {
		target = "FOR TABLE " + strings.Join(tables, ", ")
	}
	if _, err := db.ExecContext(ctx, "CREATE PUBLICATION "+pubName+" "+target); err != nil { //nolint:gosec // 화이트리스트 검증됨
		return fmt.Errorf("router: create publication: %w", err)
	}
	return nil
}

// CreateSubscription 은 target 에 source publication 을 구독하는 subscription 을 멱등 생성한다.
// copyData=true 면 PG 가 일관 초기복사+스트림, false 면 (InitialCopy 가 이미 bulk 한 경우)
// 슬롯 시점 이후 변경분만 스트림한다. sourceConnInfo 는 libpq conninfo(target→source 접속).
func CreateSubscription(ctx context.Context, targetDSN, sourceConnInfo, subName, pubName string, copyData bool) error {
	if !pubSubNamePattern.MatchString(subName) || !pubSubNamePattern.MatchString(pubName) {
		return fmt.Errorf("%w: subscription/publication name", ErrInvalidTable)
	}
	db, err := sql.Open("postgres", targetDSN)
	if err != nil {
		return fmt.Errorf("router: open target: %w", err)
	}
	defer func() { _ = db.Close() }()

	// 멱등: 이미 있으면 skip(Job 재시도 시 스트림 진행 보존 — CREATE SUBSCRIPTION 은
	// IF NOT EXISTS 미지원이라 명시 확인).
	var exists int
	if err := db.QueryRowContext(ctx, "SELECT 1 FROM pg_subscription WHERE subname = $1", subName).Scan(&exists); err == nil {
		return nil
	} else if err != sql.ErrNoRows {
		return fmt.Errorf("router: check subscription: %w", err)
	}

	// conninfo 는 SQL 문자열 리터럴로 들어가므로 작은따옴표를 이스케이프.
	conn := strings.ReplaceAll(sourceConnInfo, "'", "''")
	stmt := fmt.Sprintf("CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION %s WITH (copy_data = %t)",
		subName, conn, pubName, copyData)
	if _, err := db.ExecContext(ctx, stmt); err != nil { //nolint:gosec // 이름 화이트리스트 + conninfo 이스케이프
		return fmt.Errorf("router: create subscription: %w", err)
	}
	return nil
}

// SubscriptionLagBytes 는 source 측에서 subscription 슬롯의 미반영 WAL(bytes)을 잰다. 0 에
// 가까우면 catch-up 완료. 슬롯이 없으면 -1.
func SubscriptionLagBytes(ctx context.Context, sourceDSN, slotName string) (int64, error) {
	if !pubSubNamePattern.MatchString(slotName) {
		return -1, fmt.Errorf("%w: slot %q", ErrInvalidTable, slotName)
	}
	db, err := sql.Open("postgres", sourceDSN)
	if err != nil {
		return -1, fmt.Errorf("router: open source: %w", err)
	}
	defer func() { _ = db.Close() }()

	var lag sql.NullInt64
	err = db.QueryRowContext(ctx, `
		SELECT pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn)::bigint
		FROM pg_replication_slots WHERE slot_name = $1`, slotName).Scan(&lag)
	if err == sql.ErrNoRows {
		return -1, nil
	}
	if err != nil {
		return -1, fmt.Errorf("router: slot lag: %w", err)
	}
	if !lag.Valid {
		return -1, nil
	}
	return lag.Int64, nil
}

// DropSubscription 은 target 의 subscription 을 제거한다(원격 슬롯도 정리 시도). 슬롯 정리가
// 실패해도(원격 미도달) subscription 은 끊고 진행한다.
func DropSubscription(ctx context.Context, targetDSN, subName string) error {
	if !pubSubNamePattern.MatchString(subName) {
		return fmt.Errorf("%w: subscription %q", ErrInvalidTable, subName)
	}
	db, err := sql.Open("postgres", targetDSN)
	if err != nil {
		return fmt.Errorf("router: open target: %w", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, "DROP SUBSCRIPTION IF EXISTS "+subName); err != nil { //nolint:gosec // 화이트리스트
		return fmt.Errorf("router: drop subscription: %w", err)
	}
	return nil
}

// DropPublication 은 source 의 publication 을 제거한다.
func DropPublication(ctx context.Context, sourceDSN, pubName string) error {
	if !pubSubNamePattern.MatchString(pubName) {
		return fmt.Errorf("%w: publication %q", ErrInvalidTable, pubName)
	}
	db, err := sql.Open("postgres", sourceDSN)
	if err != nil {
		return fmt.Errorf("router: open source: %w", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, "DROP PUBLICATION IF EXISTS "+pubName); err != nil { //nolint:gosec // 화이트리스트
		return fmt.Errorf("router: drop publication: %w", err)
	}
	return nil
}

// DeleteForeignRange 는 target 테이블에서 keepShard 에 *속하지 않는* row 를 삭제한다 — 전
// 테이블 subscription(copy_data=true) 후 자기 범위만 남기는 정리. 삭제 row 수 반환.
func DeleteForeignRange(ctx context.Context, targetDSN, table string, spec v1alpha1.ShardRangeSpec, keepShard string) (int, error) {
	if !tableNamePattern.MatchString(table) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidTable, table)
	}
	keyCol := spec.Vindex.Column
	if !tableNamePattern.MatchString(keyCol) {
		return 0, fmt.Errorf("%w: vindex column %q", ErrInvalidTable, keyCol)
	}
	db, err := sql.Open("postgres", targetDSN)
	if err != nil {
		return 0, fmt.Errorf("router: open target: %w", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, "SELECT DISTINCT "+keyCol+" FROM "+table) //nolint:gosec // 화이트리스트
	if err != nil {
		return 0, fmt.Errorf("router: distinct keys %s: %w", table, err)
	}
	var foreign []any
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("router: scan key: %w", err)
		}
		shard, err := ResolveShard(spec, keyString(v))
		if err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("router: resolve key: %w", err)
		}
		if shard != keepShard {
			foreign = append(foreign, v)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("router: rows %s: %w", table, err)
	}

	deleted := 0
	del := "DELETE FROM " + table + " WHERE " + keyCol + " = $1" //nolint:gosec // 화이트리스트
	for _, k := range foreign {
		res, err := db.ExecContext(ctx, del, k)
		if err != nil {
			return deleted, fmt.Errorf("router: delete foreign key in %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		deleted += int(n)
	}
	return deleted, nil
}
