/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package walg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keiailab/postgres-operator/internal/plugin"
)

type stubRunner struct {
	gotCommand string
	gotArgs    []string
	output     []byte
	err        error
}

func (s *stubRunner) Run(_ context.Context, command string, args ...string) ([]byte, error) {
	s.gotCommand = command
	s.gotArgs = args
	return s.output, s.err
}

func TestWALG(t *testing.T) {
	t.Run("Name + BackupPlugin interface", func(t *testing.T) {
		var _ plugin.BackupPlugin = New()
		if New().Name() != "walg" {
			t.Fatalf("Name want=walg got=%s", New().Name())
		}
	})

	t.Run("Validate empty Settings 거부", func(t *testing.T) {
		err := New().Validate(&plugin.BackupSpec{Tool: "walg"})
		if err == nil || !strings.Contains(err.Error(), "settings") {
			t.Fatalf("want settings error, got %v", err)
		}
	})

	t.Run("Validate WALG_* prefix 강제", func(t *testing.T) {
		err := New().Validate(&plugin.BackupSpec{
			Tool: "walg", Settings: map[string]string{"S3_PREFIX": "s3://b/p"},
		})
		if err == nil || !strings.Contains(err.Error(), "WALG_") {
			t.Fatalf("want WALG_ prefix error, got %v", err)
		}
	})

	t.Run("Validate 성공", func(t *testing.T) {
		err := New().Validate(&plugin.BackupSpec{
			Tool: "walg", Settings: map[string]string{"WALG_S3_PREFIX": "s3://b/p"},
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("Validate tool mismatch", func(t *testing.T) {
		err := New().Validate(&plugin.BackupSpec{
			Tool: "pgbackrest", Settings: map[string]string{"WALG_S3_PREFIX": "x"},
		})
		if err == nil {
			t.Fatalf("want tool mismatch error")
		}
	})

	t.Run("BackupCommand default PGDATA", func(t *testing.T) {
		argv, err := New().BackupCommand(
			plugin.ClusterTarget{Namespace: "ns", Name: "cl"},
			plugin.BackupOptions{Type: "full"},
		)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		want := []string{"wal-g", "backup-push", "/var/lib/postgresql/data"}
		if len(argv) != len(want) {
			t.Fatalf("argv len mismatch want=%d got=%d", len(want), len(argv))
		}
		for i := range argv {
			if argv[i] != want[i] {
				t.Fatalf("argv[%d] want=%s got=%s", i, want[i], argv[i])
			}
		}
	})

	t.Run("BackupCommand PGDATA override via Labels", func(t *testing.T) {
		argv, _ := New().BackupCommand(
			plugin.ClusterTarget{Name: "cl"},
			plugin.BackupOptions{Labels: map[string]string{"pgdata": "/data/pg"}},
		)
		if argv[2] != "/data/pg" {
			t.Fatalf("PGDATA override 실패: %v", argv)
		}
	})

	t.Run("RestoreCommand zero time 거부", func(t *testing.T) {
		_, err := New().RestoreCommand(
			plugin.ClusterTarget{Name: "cl"}, time.Time{},
		)
		if err == nil {
			t.Fatalf("zero time must be rejected")
		}
	})

	t.Run("PerformBackup runner 호출 + parse", func(t *testing.T) {
		s := &stubRunner{
			output: []byte("INFO: Wrote backup with name base_000000010000000000000003\n"),
		}
		p := New(WithRunner(s))
		res, err := p.PerformBackup(context.Background(),
			plugin.ClusterTarget{Name: "cl"},
			plugin.BackupOptions{Type: "full", Repo: "s3://b/p"})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if res.BackupID != "base_000000010000000000000003" {
			t.Fatalf("BackupID 추출 실패: %q", res.BackupID)
		}
		if res.Repo != "s3://b/p" {
			t.Fatalf("Repo passthrough 실패: %q", res.Repo)
		}
	})

	t.Run("PerformBackup 실행 실패", func(t *testing.T) {
		s := &stubRunner{output: []byte("S3 error"), err: errors.New("exit 1")}
		p := New(WithRunner(s))
		_, err := p.PerformBackup(context.Background(),
			plugin.ClusterTarget{Name: "cl"}, plugin.BackupOptions{Type: "full"})
		if err == nil || !strings.Contains(err.Error(), "S3 error") {
			t.Fatalf("want wrapped error with stderr, got %v", err)
		}
	})

	t.Run("BackupCommand unsupported type", func(t *testing.T) {
		_, err := New().BackupCommand(plugin.ClusterTarget{Name: "cl"},
			plugin.BackupOptions{Type: "weird"})
		if err == nil {
			t.Fatalf("unsupported type must error")
		}
	})

	t.Run("WithCommand override", func(t *testing.T) {
		s := &stubRunner{output: []byte("Wrote backup with name x")}
		p := New(WithRunner(s), WithCommand("/usr/local/bin/wal-g"))
		_, _ = p.PerformBackup(context.Background(),
			plugin.ClusterTarget{Name: "cl"}, plugin.BackupOptions{Type: "full"})
		if s.gotCommand != "/usr/local/bin/wal-g" {
			t.Fatalf("command override 실패: %s", s.gotCommand)
		}
	})
}
