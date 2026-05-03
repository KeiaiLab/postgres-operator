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
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func newRealWithMockDB(t *testing.T) (*Real, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	cfg := Config{
		BinPath:    "/usr/bin/postgres",
		DataDir:    "/data",
		ConfigFile: "/c",
		HbaFile:    "/h",
		LocalDSN:   "host=/tmp",
	}
	r, err := NewReal(cfg)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	r.setDB(db)
	t.Cleanup(func() { _ = db.Close() })
	return r, mock
}

func TestReal_Promote_Success(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT pg_is_in_recovery\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"pg_is_in_recovery"}).AddRow(true))
	mock.ExpectQuery(`SELECT pg_promote\(true, 30\)`).
		WillReturnRows(sqlmock.NewRows([]string{"pg_promote"}).AddRow(true))
	if err := r.Promote(context.Background()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestReal_Promote_AlreadyPrimary(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT pg_is_in_recovery\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"pg_is_in_recovery"}).AddRow(false))
	if err := r.Promote(context.Background()); err != nil {
		t.Fatalf("Promote should be no-op when already primary, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestReal_Promote_ReturnsFalse(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT pg_is_in_recovery\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"pg_is_in_recovery"}).AddRow(true))
	mock.ExpectQuery(`SELECT pg_promote\(true, 30\)`).
		WillReturnRows(sqlmock.NewRows([]string{"pg_promote"}).AddRow(false))
	if err := r.Promote(context.Background()); err == nil {
		t.Errorf("expected error when pg_promote returns false")
	}
}

func TestReal_Promote_QueryError(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT pg_is_in_recovery\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"pg_is_in_recovery"}).AddRow(true))
	mock.ExpectQuery(`SELECT pg_promote\(true, 30\)`).
		WillReturnError(errors.New("conn refused"))
	if err := r.Promote(context.Background()); err == nil {
		t.Errorf("expected error from query failure")
	}
}

func TestReal_CreateReplicationSlot_AlreadyExists(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT 1 FROM pg_replication_slots WHERE slot_name = \$1`).
		WithArgs("standby0").
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))
	if err := r.CreateReplicationSlot(context.Background(), "standby0"); err != nil {
		t.Fatalf("CreateReplicationSlot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestReal_CreateReplicationSlot_CreatesIfMissing(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT 1 FROM pg_replication_slots WHERE slot_name = \$1`).
		WithArgs("standby0").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`SELECT pg_create_physical_replication_slot\(\$1, true, false\)`).
		WithArgs("standby0").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := r.CreateReplicationSlot(context.Background(), "standby0"); err != nil {
		t.Fatalf("CreateReplicationSlot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestReal_CreateReplicationSlot_EmptyName(t *testing.T) {
	r, _ := newRealWithMockDB(t)
	if err := r.CreateReplicationSlot(context.Background(), ""); err == nil {
		t.Errorf("expected error for empty slot name")
	}
}

func TestReal_DropReplicationSlot_NoOpWhenAbsent(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT 1 FROM pg_replication_slots WHERE slot_name = \$1`).
		WithArgs("ghost").
		WillReturnError(sql.ErrNoRows)
	if err := r.DropReplicationSlot(context.Background(), "ghost"); err != nil {
		t.Fatalf("DropReplicationSlot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestReal_DropReplicationSlot_Drops(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT 1 FROM pg_replication_slots WHERE slot_name = \$1`).
		WithArgs("standby0").
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))
	mock.ExpectExec(`SELECT pg_drop_replication_slot\(\$1\)`).
		WithArgs("standby0").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := r.DropReplicationSlot(context.Background(), "standby0"); err != nil {
		t.Fatalf("DropReplicationSlot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestReal_DropReplicationSlot_EmptyName(t *testing.T) {
	r, _ := newRealWithMockDB(t)
	if err := r.DropReplicationSlot(context.Background(), ""); err == nil {
		t.Errorf("expected error for empty slot name")
	}
}

func TestReal_IsReady_OK(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT 1$`).
		WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))
	if !r.IsReady(context.Background()) {
		t.Errorf("expected ready=true")
	}
}

func TestReal_IsReady_QueryFail(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT 1$`).
		WillReturnError(errors.New("conn refused"))
	if r.IsReady(context.Background()) {
		t.Errorf("expected ready=false on query error")
	}
}

func TestReal_LagBytes_Primary_NoReplicas(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT pg_is_in_recovery\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"pg_is_in_recovery"}).AddRow(false))
	mock.ExpectQuery(`pg_stat_replication`).
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(int64(0)))
	if got := r.LagBytes(context.Background()); got != 0 {
		t.Errorf("expected 0 lag for primary with no replicas, got %d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestReal_LagBytes_Primary_WithLag(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT pg_is_in_recovery\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"pg_is_in_recovery"}).AddRow(false))
	mock.ExpectQuery(`pg_stat_replication`).
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(int64(1024)))
	if got := r.LagBytes(context.Background()); got != 1024 {
		t.Errorf("expected lag=1024, got %d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestReal_LagBytes_Replica(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT pg_is_in_recovery\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"pg_is_in_recovery"}).AddRow(true))
	mock.ExpectQuery(`pg_last_wal_receive_lsn`).
		WillReturnRows(sqlmock.NewRows([]string{"pg_wal_lsn_diff"}).AddRow(int64(2048)))
	if got := r.LagBytes(context.Background()); got != 2048 {
		t.Errorf("expected replica lag=2048, got %d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestReal_LagBytes_QueryError(t *testing.T) {
	r, mock := newRealWithMockDB(t)
	mock.ExpectQuery(`SELECT pg_is_in_recovery\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"pg_is_in_recovery"}).AddRow(false))
	mock.ExpectQuery(`pg_stat_replication`).
		WillReturnError(errors.New("conn refused"))
	if got := r.LagBytes(context.Background()); got != -1 {
		t.Errorf("expected -1 on query error, got %d", got)
	}
}
