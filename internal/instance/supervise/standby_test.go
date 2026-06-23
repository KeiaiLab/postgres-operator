/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package supervise

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsStandby_FileAbsent_ReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	if IsStandby(dir) {
		t.Fatalf("IsStandby(empty dir) = true, want false")
	}
}

func TestIsStandby_FileExists_ReturnsTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "standby.signal")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !IsStandby(dir) {
		t.Fatalf("IsStandby(dir with signal) = false, want true")
	}
}

func TestRemoveStandbySignal_Idempotent(t *testing.T) {
	dir := t.TempDir()
	// 1차: 부재 상태 — nil 반환해야 idempotent.
	if err := RemoveStandbySignal(dir); err != nil {
		t.Fatalf("RemoveStandbySignal (absent #1): %v", err)
	}
	// 2차: 여전히 부재 — 마찬가지로 nil.
	if err := RemoveStandbySignal(dir); err != nil {
		t.Fatalf("RemoveStandbySignal (absent #2): %v", err)
	}
}

func TestCreateStandbySignal_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := CreateStandbySignal(dir); err != nil {
		t.Fatalf("CreateStandbySignal #1: %v", err)
	}
	if err := CreateStandbySignal(dir); err != nil {
		t.Fatalf("CreateStandbySignal #2 (idempotent): %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "standby.signal"))
	if err != nil {
		t.Fatalf("Stat after Create: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file perms = %o, want 0600", mode)
	}
}

func TestPrepareRestartedPrimaryAsStandby_NoMarker(t *testing.T) {
	dir := t.TempDir()
	prepared, err := PrepareRestartedPrimaryAsStandby(dir, "primary.svc:5432")
	if err != nil {
		t.Fatalf("PrepareRestartedPrimaryAsStandby: %v", err)
	}
	if prepared {
		t.Fatal("prepared = true, want false without marker")
	}
}

// TestPrepareRestartedPrimaryAsStandby_SelfPrimarySkips 는 INC-2026-06-23 회귀 가드:
// PrimaryEndpoint 가 SelfEndpoint 와 같으면(operator 가 본 노드를 primary 로 지정)
// marker 가 있어도 standby 화를 skip 하고 marker 를 제거해야 한다 (self pg_rewind →
// connection refused 무한 crashloop 차단). 본 guard 제거 시 본 테스트 실패.
func TestPrepareRestartedPrimaryAsStandby_SelfPrimarySkips(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, RestartPrimaryAsStandbyMarker)
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	self := "postgres-prod-shard-0-0.svc.cluster.local:5432"
	prepared, err := PrepareRestartedPrimaryAsStandbyWithRewind(context.Background(), RejoinOptions{
		DataDir:         dir,
		PrimaryEndpoint: self, // operator 가 본 노드를 primary 로 지정
		SelfEndpoint:    self,
	})
	if err != nil {
		t.Fatalf("PrepareRestartedPrimaryAsStandbyWithRewind: %v", err)
	}
	if prepared {
		t.Fatal("prepared = true; self-primary 는 standby 화 skip(false) 해야")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("marker 가 self-primary skip 후 잔존(stat err=%v) — 제거 기대", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "standby.signal")); !os.IsNotExist(err) {
		t.Error("standby.signal 이 self-primary 인데 생성됨 — primary 부팅 차단")
	}
}

func TestPrepareRestartedPrimaryAsStandby_ConfiguresStandby(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, RestartPrimaryAsStandbyMarker)
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	prepared, err := PrepareRestartedPrimaryAsStandby(dir, "primary.svc.cluster.local:5432")
	if err != nil {
		t.Fatalf("PrepareRestartedPrimaryAsStandby: %v", err)
	}
	if !prepared {
		t.Fatal("prepared = false, want true")
	}
	if _, err := os.Stat(filepath.Join(dir, "standby.signal")); err != nil {
		t.Fatalf("standby.signal missing: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "postgresql.auto.conf"))
	if err != nil {
		t.Fatalf("read postgresql.auto.conf: %v", err)
	}
	if !strings.Contains(string(raw), "primary_conninfo = 'host=primary.svc.cluster.local port=5432 user=postgres'") {
		t.Fatalf("primary_conninfo not configured, got:\n%s", raw)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker still exists or unexpected stat error: %v", err)
	}
}

func TestPrepareRestartedPrimaryAsStandbyWithRewind_RunsPgRewindBeforeStandbyConfig(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, RestartPrimaryAsStandbyMarker)
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	runner := &recordingCommandRunner{}

	prepared, err := PrepareRestartedPrimaryAsStandbyWithRewind(t.Context(), RejoinOptions{
		DataDir:         dir,
		PrimaryEndpoint: "primary.svc.cluster.local:5444",
		BinDir:          "/postgres/bin",
		Runner:          runner,
		ApplicationName: "demo-shard-0-2",
	})
	if err != nil {
		t.Fatalf("PrepareRestartedPrimaryAsStandbyWithRewind: %v", err)
	}
	if !prepared {
		t.Fatal("prepared = false, want true")
	}
	if len(runner.calls) != 1 {
		t.Fatalf("pg_rewind calls = %d, want 1", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "/postgres/bin/pg_rewind" {
		t.Fatalf("command = %q, want /postgres/bin/pg_rewind", call.name)
	}
	gotArgs := strings.Join(call.args, "\n")
	for _, want := range []string{
		"--target-pgdata",
		dir,
		"--source-server=host=primary.svc.cluster.local port=5444 user=postgres dbname=postgres",
	} {
		if !strings.Contains(gotArgs, want) {
			t.Fatalf("pg_rewind args missing %q, got:\n%s", want, gotArgs)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "standby.signal")); err != nil {
		t.Fatalf("standby.signal missing: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "postgresql.auto.conf"))
	if err != nil {
		t.Fatalf("read postgresql.auto.conf: %v", err)
	}
	if !strings.Contains(string(raw), "primary_conninfo = 'host=primary.svc.cluster.local port=5444 user=postgres application_name=demo-shard-0-2'") {
		t.Fatalf("primary_conninfo not configured, got:\n%s", raw)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker still exists or unexpected stat error: %v", err)
	}
}

func TestPrepareRestartedPrimaryAsStandbyWithRewind_FailureKeepsMarker(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, RestartPrimaryAsStandbyMarker)
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	runner := &recordingCommandRunner{err: errors.New("rewind failed")}

	prepared, err := PrepareRestartedPrimaryAsStandbyWithRewind(t.Context(), RejoinOptions{
		DataDir:         dir,
		PrimaryEndpoint: "primary.svc:5432",
		BinDir:          "/postgres/bin",
		Runner:          runner,
	})
	if err == nil {
		t.Fatal("err = nil, want pg_rewind failure")
	}
	if prepared {
		t.Fatal("prepared = true, want false on pg_rewind failure")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker should remain after rewind failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "standby.signal")); !os.IsNotExist(err) {
		t.Fatalf("standby.signal should not be created after rewind failure: %v", err)
	}
}

func TestPrepareRestartedPrimaryAsStandbyWithRewind_FallsBackToBasebackup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, RestartPrimaryAsStandbyMarker)
	if err := os.WriteFile(filepath.Join(dir, "PG_VERSION"), []byte("18"), 0o600); err != nil {
		t.Fatalf("write PG_VERSION: %v", err)
	}
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	runner := &recordingCommandRunner{
		errByName: map[string]error{"/postgres/bin/pg_rewind": errors.New("missing WAL")},
	}

	prepared, err := PrepareRestartedPrimaryAsStandbyWithRewind(t.Context(), RejoinOptions{
		DataDir:                   dir,
		PrimaryEndpoint:           "primary.svc.cluster.local:5444",
		BinDir:                    "/postgres/bin",
		Runner:                    runner,
		BasebackupOnRewindFailure: true,
	})
	if err != nil {
		t.Fatalf("PrepareRestartedPrimaryAsStandbyWithRewind: %v", err)
	}
	if !prepared {
		t.Fatal("prepared = false, want true")
	}
	if len(runner.calls) != 2 {
		t.Fatalf("command calls = %d, want pg_rewind + pg_basebackup", len(runner.calls))
	}
	if runner.calls[0].name != "/postgres/bin/pg_rewind" {
		t.Fatalf("first command = %q, want pg_rewind", runner.calls[0].name)
	}
	call := runner.calls[1]
	if call.name != "/postgres/bin/pg_basebackup" {
		t.Fatalf("second command = %q, want pg_basebackup", call.name)
	}
	gotArgs := strings.Join(call.args, "\n")
	for _, want := range []string{
		"-D",
		dir,
		"-h",
		"primary.svc.cluster.local",
		"-p",
		"5444",
		"-U",
		"postgres",
		"--no-password",
		"--wal-method=stream",
		"--checkpoint=fast",
	} {
		if !strings.Contains(gotArgs, want) {
			t.Fatalf("pg_basebackup args missing %q, got:\n%s", want, gotArgs)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "standby.signal")); err != nil {
		t.Fatalf("standby.signal missing: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker should be removed after basebackup fallback: %v", err)
	}
}

func TestPrepareRestartedPrimaryAsStandbyWithRewind_BasebackupFailureRestoresDataDir(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, RestartPrimaryAsStandbyMarker)
	if err := os.WriteFile(filepath.Join(dir, "PG_VERSION"), []byte("18"), 0o600); err != nil {
		t.Fatalf("write PG_VERSION: %v", err)
	}
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	runner := &recordingCommandRunner{
		errByName: map[string]error{
			"/postgres/bin/pg_rewind":     errors.New("missing WAL"),
			"/postgres/bin/pg_basebackup": errors.New("network down"),
		},
	}

	prepared, err := PrepareRestartedPrimaryAsStandbyWithRewind(t.Context(), RejoinOptions{
		DataDir:                   dir,
		PrimaryEndpoint:           "primary.svc:5432",
		BinDir:                    "/postgres/bin",
		Runner:                    runner,
		BasebackupOnRewindFailure: true,
	})
	if err == nil {
		t.Fatal("err = nil, want basebackup failure")
	}
	if prepared {
		t.Fatal("prepared = true, want false")
	}
	if _, err := os.Stat(filepath.Join(dir, "PG_VERSION")); err != nil {
		t.Fatalf("original data dir was not restored: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker should remain after fallback failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "standby.signal")); !os.IsNotExist(err) {
		t.Fatalf("standby.signal should not be created after fallback failure: %v", err)
	}
}

type recordingCommandRunner struct {
	calls     []recordedCommand
	err       error
	errByName map[string]error
}

type recordedCommand struct {
	name string
	args []string
}

func (r *recordingCommandRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, recordedCommand{name: name, args: append([]string(nil), args...)})
	if err := r.errByName[name]; err != nil {
		return err
	}
	return r.err
}
