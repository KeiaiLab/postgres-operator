/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package barman

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

func TestBarman(t *testing.T) {
	t.Run("Name + BackupPlugin interface", func(t *testing.T) {
		var _ plugin.BackupPlugin = New()
		if New().Name() != "barman" {
			t.Fatalf("Name want=barman got=%s", New().Name())
		}
	})

	t.Run("Validate empty server 거부", func(t *testing.T) {
		err := New().Validate(&plugin.BackupSpec{Tool: "barman"})
		if err == nil || !strings.Contains(err.Error(), "server") {
			t.Fatalf("want server error, got %v", err)
		}
	})

	t.Run("Validate server via Repo", func(t *testing.T) {
		if err := New().Validate(&plugin.BackupSpec{Tool: "barman", Repo: "srv-a"}); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("Validate server via Settings", func(t *testing.T) {
		err := New().Validate(&plugin.BackupSpec{
			Tool: "barman", Settings: map[string]string{"server": "srv-a"},
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("Validate tool mismatch", func(t *testing.T) {
		err := New().Validate(&plugin.BackupSpec{Tool: "pgbackrest", Repo: "srv"})
		if err == nil {
			t.Fatalf("want tool mismatch error")
		}
	})

	t.Run("BackupCommand 결정성", func(t *testing.T) {
		argv, err := New().BackupCommand(
			plugin.ClusterTarget{Name: "cl"},
			plugin.BackupOptions{Type: "full", Repo: "srv-prod"},
		)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		want := []string{"barman", "backup", "srv-prod"}
		for i := range want {
			if argv[i] != want[i] {
				t.Fatalf("argv[%d] want=%s got=%s", i, want[i], argv[i])
			}
		}
	})

	t.Run("BackupCommand Repo 없으면 target.Name", func(t *testing.T) {
		argv, _ := New().BackupCommand(
			plugin.ClusterTarget{Name: "cl-fallback"},
			plugin.BackupOptions{Type: "full"},
		)
		if argv[2] != "cl-fallback" {
			t.Fatalf("target.Name fallback 실패: %+v", argv)
		}
	})

	t.Run("BackupCommand empty server error", func(t *testing.T) {
		_, err := New().BackupCommand(plugin.ClusterTarget{}, plugin.BackupOptions{})
		if err == nil {
			t.Fatalf("empty server must error")
		}
	})

	t.Run("RestoreCommand zero time + empty name 거부", func(t *testing.T) {
		_, err := New().RestoreCommand(plugin.ClusterTarget{Name: "cl"}, time.Time{})
		if err == nil {
			t.Fatalf("zero time must error")
		}
		_, err = New().RestoreCommand(plugin.ClusterTarget{}, time.Now())
		if err == nil {
			t.Fatalf("empty name must error")
		}
	})

	t.Run("RestoreCommand timestamp UTC format", func(t *testing.T) {
		ts := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
		argv, _ := New().RestoreCommand(plugin.ClusterTarget{Name: "srv"}, ts)
		// argv: barman recover --target-time "2026-05-19 12:00:00+00:00" srv latest /var/lib/postgresql/data
		if argv[3] != "2026-05-19 12:00:00+00:00" {
			t.Fatalf("target-time format mismatch: %q", argv[3])
		}
		if argv[len(argv)-1] != "/var/lib/postgresql/data" {
			t.Fatalf("PGDATA target missing: %+v", argv)
		}
	})

	t.Run("PerformBackup + ParseBackupResult", func(t *testing.T) {
		s := &stubRunner{output: []byte("Starting backup\nBackup ID: 20260519T120000\nDone\n")}
		p := New(WithRunner(s))
		res, err := p.PerformBackup(context.Background(),
			plugin.ClusterTarget{Name: "cl"},
			plugin.BackupOptions{Type: "full", Repo: "srv"})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if res.BackupID != "20260519T120000" {
			t.Fatalf("BackupID 추출 실패: %q", res.BackupID)
		}
	})

	t.Run("PerformBackup 실행 실패", func(t *testing.T) {
		s := &stubRunner{output: []byte("disk full"), err: errors.New("exit 2")}
		p := New(WithRunner(s))
		_, err := p.PerformBackup(context.Background(),
			plugin.ClusterTarget{Name: "cl"}, plugin.BackupOptions{Repo: "srv"})
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("want wrapped error, got %v", err)
		}
	})

	t.Run("WithCommand override", func(t *testing.T) {
		s := &stubRunner{output: []byte("Backup ID: x")}
		p := New(WithRunner(s), WithCommand("/usr/bin/barman"))
		_, _ = p.PerformBackup(context.Background(),
			plugin.ClusterTarget{Name: "cl"}, plugin.BackupOptions{Repo: "srv"})
		if s.gotCommand != "/usr/bin/barman" {
			t.Fatalf("command override 실패: %s", s.gotCommand)
		}
	})
}
