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
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// D.8.3 Metadata store (ROADMAP G3 L151).
//
// 결정: *Postgres system catalog* (sidecar 아닌) — RFC-0002 §3 metadata
// 분리 정합. 본 패키지는 coordinator Postgres 인스턴스 또는 별도 metadata
// DB 에 `pg_keiailab_*` 스키마를 만들어 ShardRange 상태를 영구 저장한다.
//
// 분기 근거 (vs sidecar):
//   - PG 자체가 ACID + replication + backup 도구 모두 보유 — 별 운영 표면 추가 없음
//   - operator 가 이미 PG 에 SQL 발급 (PostgresDatabase / PostgresUser reconciler)
//     — 코드 path 통합
//   - sidecar 는 별 statefulset + Pod lifecycle + monitoring 추가 부담
//
// 본 패키지는 *순수 SQL DDL + DML 함수* + interface — 실 *sql.DB 주입은 별 layer.

// ErrMetadataInconsistent 는 catalog 무결성 위반 시 반환.
var ErrMetadataInconsistent = errors.New("router: shard metadata inconsistent")

// MetadataMigration 는 단일 schema migration step 이다.
type MetadataMigration struct {
	Version     int    // 순차 0, 1, 2 ...
	Description string // 사람-가독 요약
	SQL         string // 적용 DDL/DML
}

// SchemaMigrations 는 metadata store 의 정렬된 migration 목록이다.
// version 추가 시 본 slice 끝에만 append (중간 삽입 금지 — 결정성 깨짐).
var SchemaMigrations = []MetadataMigration{
	{
		Version:     1,
		Description: "Initial metadata schema (pg_keiailab namespace + shardranges + version table)",
		SQL: `
CREATE SCHEMA IF NOT EXISTS pg_keiailab;

CREATE TABLE IF NOT EXISTS pg_keiailab.schema_version (
    version       integer PRIMARY KEY,
    description   text NOT NULL,
    applied_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS pg_keiailab.shardranges (
    cluster       text NOT NULL,
    keyspace      text NOT NULL,
    range_lo      text NOT NULL,
    range_hi      text NOT NULL,
    shard_id      text NOT NULL,
    vindex_type   text NOT NULL,
    generation    bigint NOT NULL DEFAULT 1,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (cluster, keyspace, range_lo, range_hi)
);

CREATE INDEX IF NOT EXISTS shardranges_by_shard
    ON pg_keiailab.shardranges (shard_id);
`,
	},
	{
		Version:     2,
		Description: "Placement hints + drift detection columns",
		SQL: `
ALTER TABLE pg_keiailab.shardranges
    ADD COLUMN IF NOT EXISTS preferred_zone text,
    ADD COLUMN IF NOT EXISTS preferred_node text,
    ADD COLUMN IF NOT EXISTS weight integer NOT NULL DEFAULT 1;
`,
	},
}

// Store 는 ShardRange metadata 의 영속화 contract 이다.
type Store interface {
	// Migrate 는 본 store 의 schema 를 latest 까지 ensure 한다 (idempotent).
	Migrate(ctx context.Context) error
	// Upsert 는 1+ range entry 를 atomic insert/update 한다.
	Upsert(ctx context.Context, cluster, keyspace, vindexType string, entries []RangeEntry) error
	// List 는 cluster + keyspace 단위 전체 range 를 generation 순으로 반환.
	List(ctx context.Context, cluster, keyspace string) ([]RangeEntry, error)
	// Delete 는 entry 1+ 을 atomic 삭제 (resharding cutover 후).
	Delete(ctx context.Context, cluster, keyspace string, ranges []RangeEntry) error
	// CurrentVersion 은 schema_version 의 max version 반환 (0 = 미적용).
	CurrentVersion(ctx context.Context) (int, error)
}

// RangeEntry 는 Store interface 의 정규화된 entry.
type RangeEntry struct {
	Lo         string
	Hi         string
	ShardID    string
	VindexType string
	Generation int64
}

// PostgresStore 는 sql.DB 기반 Store 구현이다.
type PostgresStore struct {
	DB *sql.DB
}

// NewPostgresStore 는 *PostgresStore 를 반환한다. db 가 nil 이면 panic
// — 명시 의존성 주입 강제.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	if db == nil {
		panic("router: NewPostgresStore: db must not be nil")
	}
	return &PostgresStore{DB: db}
}

var _ Store = (*PostgresStore)(nil)

// Migrate 는 schema_version 에 따라 적용 안 된 migration 만 sequential 실행.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	current, err := s.bootstrap(ctx)
	if err != nil {
		return err
	}
	for _, m := range SchemaMigrations {
		if m.Version <= current {
			continue
		}
		if _, err := s.DB.ExecContext(ctx, m.SQL); err != nil {
			return fmt.Errorf("router: migrate v%d: %w", m.Version, err)
		}
		_, err := s.DB.ExecContext(ctx,
			`INSERT INTO pg_keiailab.schema_version (version, description) VALUES ($1, $2)`,
			m.Version, m.Description,
		)
		if err != nil {
			return fmt.Errorf("router: migrate v%d record: %w", m.Version, err)
		}
	}
	return nil
}

// bootstrap 은 schema_version 테이블 자체를 위한 minimal DDL 만 보장 후 current version 반환.
func (s *PostgresStore) bootstrap(ctx context.Context) (int, error) {
	// 첫 호출 시 schema_version 이 없을 수 있음 — version 1 의 SQL 안에 포함됨.
	// 본 함수는 *조회만* — 없으면 0.
	row := s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM pg_keiailab.schema_version`,
	)
	var v int
	if err := row.Scan(&v); err != nil {
		// 테이블 자체 미존재 — version 0 (bootstrap 필요) 로 처리.
		return 0, nil //nolint:nilerr
	}
	return v, nil
}

// CurrentVersion 은 schema_version max 를 반환.
func (s *PostgresStore) CurrentVersion(ctx context.Context) (int, error) {
	return s.bootstrap(ctx)
}

// Upsert 는 entries 를 ON CONFLICT generation+1 + updated_at=now 으로 atomic 갱신.
func (s *PostgresStore) Upsert(ctx context.Context, cluster, keyspace, vindexType string, entries []RangeEntry) error {
	if len(entries) == 0 {
		return nil
	}
	if cluster == "" || keyspace == "" {
		return fmt.Errorf("%w: empty cluster/keyspace", ErrMetadataInconsistent)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	const stmt = `
INSERT INTO pg_keiailab.shardranges
    (cluster, keyspace, range_lo, range_hi, shard_id, vindex_type, generation, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 1, now())
ON CONFLICT (cluster, keyspace, range_lo, range_hi)
DO UPDATE SET
    shard_id   = EXCLUDED.shard_id,
    vindex_type = EXCLUDED.vindex_type,
    generation = pg_keiailab.shardranges.generation + 1,
    updated_at = now()`
	for _, e := range entries {
		if e.Lo == "" || e.Hi == "" || e.ShardID == "" {
			return fmt.Errorf("%w: empty Lo/Hi/ShardID", ErrMetadataInconsistent)
		}
		if _, err := tx.ExecContext(ctx, stmt,
			cluster, keyspace, e.Lo, e.Hi, e.ShardID, vindexType,
		); err != nil {
			return fmt.Errorf("router: upsert %s..%s→%s: %w", e.Lo, e.Hi, e.ShardID, err)
		}
	}
	return tx.Commit()
}

// List 는 본 cluster + keyspace 의 모든 range 를 (lo asc, generation desc) 정렬 반환.
func (s *PostgresStore) List(ctx context.Context, cluster, keyspace string) ([]RangeEntry, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT range_lo, range_hi, shard_id, vindex_type, generation
		   FROM pg_keiailab.shardranges
		  WHERE cluster=$1 AND keyspace=$2
		  ORDER BY range_lo, generation DESC`,
		cluster, keyspace,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RangeEntry
	for rows.Next() {
		var e RangeEntry
		if err := rows.Scan(&e.Lo, &e.Hi, &e.ShardID, &e.VindexType, &e.Generation); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	// 결정성 보장 (DB ORDER BY 이미 정렬되었지만 안전망).
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Lo != out[j].Lo {
			return out[i].Lo < out[j].Lo
		}
		return out[i].Generation > out[j].Generation
	})
	return out, rows.Err()
}

// Delete 는 ranges 의 (lo, hi) 키로 1+ entry 를 단일 transaction 삭제.
func (s *PostgresStore) Delete(ctx context.Context, cluster, keyspace string, ranges []RangeEntry) error {
	if len(ranges) == 0 {
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, e := range ranges {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM pg_keiailab.shardranges
			  WHERE cluster=$1 AND keyspace=$2 AND range_lo=$3 AND range_hi=$4`,
			cluster, keyspace, e.Lo, e.Hi,
		); err != nil {
			return fmt.Errorf("router: delete %s..%s: %w", e.Lo, e.Hi, err)
		}
	}
	return tx.Commit()
}
