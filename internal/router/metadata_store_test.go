/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package router

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStore(t *testing.T) {
	t.Run("NewPostgresStore nil panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("expected panic on nil DB")
			}
		}()
		_ = NewPostgresStore(nil)
	})

	t.Run("Migrate from scratch applies all", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer func() { _ = db.Close() }()

		// bootstrap MAX query → 0 (테이블 미존재 등) — sqlmock 에서 scan error 처리.
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM pg_keiailab.schema_version`).
			WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(0))
		// v1: SCHEMA + tables — Exec
		mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS pg_keiailab`).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`INSERT INTO pg_keiailab.schema_version`).
			WithArgs(1, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		// v2: ALTER TABLE
		mock.ExpectExec(`ALTER TABLE pg_keiailab.shardranges`).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`INSERT INTO pg_keiailab.schema_version`).
			WithArgs(2, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))

		store := NewPostgresStore(db)
		if err := store.Migrate(context.Background()); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("Migrate idempotent — current v2 → no exec", func(t *testing.T) {
		db, mock, _ := sqlmock.New()
		defer func() { _ = db.Close() }()
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\)`).
			WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(2))
		// 추가 Exec 없음 — 모두 skip.
		store := NewPostgresStore(db)
		if err := store.Migrate(context.Background()); err != nil {
			t.Fatalf("Migrate idempotent: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("Upsert transaction + ON CONFLICT", func(t *testing.T) {
		db, mock, _ := sqlmock.New()
		defer func() { _ = db.Close() }()
		mock.ExpectBegin()
		mock.ExpectExec(`INSERT INTO pg_keiailab.shardranges`).
			WithArgs("c", "ks", "0x00", "0x7f", "sh-a", "hash").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`INSERT INTO pg_keiailab.shardranges`).
			WithArgs("c", "ks", "0x80", "0xff", "sh-b", "hash").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		store := NewPostgresStore(db)
		err := store.Upsert(context.Background(), "c", "ks", "hash", []RangeEntry{
			{Lo: "0x00", Hi: "0x7f", ShardID: "sh-a"},
			{Lo: "0x80", Hi: "0xff", ShardID: "sh-b"},
		})
		if err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("Upsert empty entries no-op", func(t *testing.T) {
		db, _, _ := sqlmock.New()
		defer func() { _ = db.Close() }()
		store := NewPostgresStore(db)
		if err := store.Upsert(context.Background(), "c", "ks", "hash", nil); err != nil {
			t.Fatalf("empty Upsert: %v", err)
		}
	})

	t.Run("Upsert validation: empty cluster/Lo/Hi/ShardID", func(t *testing.T) {
		db, _, _ := sqlmock.New()
		defer func() { _ = db.Close() }()
		store := NewPostgresStore(db)
		if err := store.Upsert(context.Background(), "", "ks", "hash",
			[]RangeEntry{{Lo: "0", Hi: "1", ShardID: "s"}}); !errors.Is(err, ErrMetadataInconsistent) {
			t.Fatalf("empty cluster must error, got %v", err)
		}
	})

	t.Run("List 정렬", func(t *testing.T) {
		db, mock, _ := sqlmock.New()
		defer func() { _ = db.Close() }()
		mock.ExpectQuery(`SELECT range_lo, range_hi, shard_id, vindex_type, generation`).
			WithArgs("c", "ks").
			WillReturnRows(sqlmock.NewRows([]string{
				"range_lo", "range_hi", "shard_id", "vindex_type", "generation",
			}).
				AddRow("0x00", "0x7f", "sh-a", "hash", 3).
				AddRow("0x80", "0xff", "sh-b", "hash", 1),
			)
		store := NewPostgresStore(db)
		got, err := store.List(context.Background(), "c", "ks")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("List count want=2 got=%d", len(got))
		}
		if got[0].Lo != "0x00" || got[1].Lo != "0x80" {
			t.Fatalf("sorted order broken: %+v", got)
		}
	})

	t.Run("Delete 다중 entry 단일 transaction", func(t *testing.T) {
		db, mock, _ := sqlmock.New()
		defer func() { _ = db.Close() }()
		mock.ExpectBegin()
		mock.ExpectExec(`DELETE FROM pg_keiailab.shardranges`).
			WithArgs("c", "ks", "0x00", "0x7f").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		store := NewPostgresStore(db)
		err := store.Delete(context.Background(), "c", "ks",
			[]RangeEntry{{Lo: "0x00", Hi: "0x7f"}})
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("CurrentVersion 직접 조회", func(t *testing.T) {
		db, mock, _ := sqlmock.New()
		defer func() { _ = db.Close() }()
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\)`).
			WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(2))
		store := NewPostgresStore(db)
		v, err := store.CurrentVersion(context.Background())
		if err != nil || v != 2 {
			t.Fatalf("want=2 got=%d err=%v", v, err)
		}
	})

	t.Run("SchemaMigrations 순서 검증", func(t *testing.T) {
		for i := 1; i < len(SchemaMigrations); i++ {
			if SchemaMigrations[i].Version <= SchemaMigrations[i-1].Version {
				t.Fatalf("SchemaMigrations[%d].Version (%d) must be > prev (%d)",
					i, SchemaMigrations[i].Version, SchemaMigrations[i-1].Version)
			}
		}
	})
}
