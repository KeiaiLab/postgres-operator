/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package supervise 의 본 파일 (standby.go) 은 RFC 0006 R3 — PostgreSQL 표준
// `$PGDATA/standby.signal` 파일 lifecycle 헬퍼를 제공한다.
//
// PG 표준 의미론:
//   - standby.signal 존재 → postgres 가 standby 로 부팅 (WAL replay, read-only).
//   - 부재 → primary 로 부팅 (write 가능).
//
// 본 헬퍼는 controller 측 bootstrap init container (RFC 0006 R3 Task B) 와
// 계약 관계다 — bootstrap 이 pg_basebackup 으로 seed 한 경우 standby.signal 을
// 생성해 두고, instance manager 는 election 결과에 따라 다음과 같이 조작한다:
//
//   - OnStartedLeading: RemoveStandbySignal (primary 로 promote 직전).
//   - OnStoppedLeading: CreateStandbySignal (Pod 재시작 후 standby 로 부팅하도록).
//
// postgres 의존이 없는 순수 file ops 함수만 두어 테스트 격리가 용이하도록 설계
// — supervise.go / sql.go 와 분리한 이유.
//
// 알려진 알파 한계 (RFC 0007 후속): instance manager 가 panic / SIGKILL 으로
// 종료되어 OnStoppedLeading 이 실행되지 않으면 standby.signal 이 남지 않아 다음
// 부팅 시 옛 leader 가 primary 로 부팅 가능. PVC fence 가 promote 측에서 검사하므로
// 데이터 분기는 회피되나, 일시적 ambiguity 가 있다. 후속: bootstrap 이 PG_VERSION
// 존재 + clear-primary marker 부재 시 standby.signal 자동 생성.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// standbySignalFile 은 PG 가 인식하는 standby 모드 sentinel 파일명 (PG 12+).
const standbySignalFile = "standby.signal"

// RestartPrimaryAsStandbyMarker 는 HA 클러스터에서 ordinal-0 이 기존 primary
// PGDATA 로 재시작했음을 bootstrap 이 instance manager 에 전달하는 marker 다.
const RestartPrimaryAsStandbyMarker = ".keiailab-restart-primary-as-standby"

// CommandRunner 는 pg_rewind 같은 외부 PostgreSQL 유틸리티 실행을 테스트에서
// 대체 가능하게 하는 최소 인터페이스다.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// ExecCommandRunner 는 실제 OS process 로 PostgreSQL 유틸리티를 실행한다.
type ExecCommandRunner struct{}

// Run 은 command output 을 error 에 포함해 Pod log 에 실패 원인을 남긴다.
func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run %s %v: %w: %s", name, args, err, string(output))
	}
	return nil
}

// RejoinOptions 는 failover 후 옛 primary PGDATA 를 current primary 기준 standby 로
// 되감기 위한 입력값이다.
type RejoinOptions struct {
	DataDir                   string
	PrimaryEndpoint           string
	ApplicationName           string
	BinDir                    string
	Runner                    CommandRunner
	BasebackupOnRewindFailure bool
}

// IsStandby 는 dataDir 안에 standby.signal 파일이 존재하는지 검사한다.
// stat 오류가 ErrNotExist 이외인 경우에도 false 로 보수적 판정 (호출 측이
// boolean 만 보므로 별도 error 반환 없음 — promote 결정의 부수 게이트로만 쓰임).
func IsStandby(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, standbySignalFile))
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	// 그 외 stat 오류 (권한 등) — promote 결정의 부수 게이트라 false 반환.
	return false
}

// RemoveStandbySignal 은 dataDir 의 standby.signal 을 삭제한다. 이미 부재 시
// idempotent — nil 반환. 다른 IO 오류는 그대로 wrap 하여 반환한다.
func RemoveStandbySignal(dataDir string) error {
	path := filepath.Join(dataDir, standbySignalFile)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// CreateStandbySignal 은 dataDir 에 빈 standby.signal 파일 (mode 0600) 을 생성
// 한다. 이미 존재하면 그대로 둔다 (idempotent) — PG 는 파일 내용이 아니라 존재
// 자체만 보므로 재생성/덮어쓰기 불필요.
func CreateStandbySignal(dataDir string) error {
	path := filepath.Join(dataDir, standbySignalFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// PrepareRestartedPrimaryAsStandby 는 marker 가 있을 때 기존 ordinal-0 PGDATA 를
// standby 로 부팅하도록 standby.signal + primary_conninfo 를 구성한다.
func PrepareRestartedPrimaryAsStandby(dataDir, primaryEndpoint string) (bool, error) {
	return PrepareRestartedPrimaryAsStandbyWithRewind(context.Background(), RejoinOptions{
		DataDir:         dataDir,
		PrimaryEndpoint: primaryEndpoint,
	})
}

// PrepareRestartedPrimaryAsStandbyWithRewind 는 marker 가 있을 때 pg_rewind 로
// current primary 의 timeline 에 맞춘 뒤 standby.signal + primary_conninfo 를
// 구성한다. pg_rewind 실패 시 marker 를 남겨 다음 restart 에서 다시 시도한다.
func PrepareRestartedPrimaryAsStandbyWithRewind(ctx context.Context, opts RejoinOptions) (bool, error) {
	marker := filepath.Join(opts.DataDir, RestartPrimaryAsStandbyMarker)
	if _, err := os.Stat(marker); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", marker, err)
	}
	if opts.PrimaryEndpoint == "" {
		return false, errors.New("primaryEndpoint must not be empty")
	}
	if opts.BinDir != "" {
		runner := opts.Runner
		if runner == nil {
			runner = ExecCommandRunner{}
		}
		if err := runPgRewind(ctx, runner, opts.BinDir, opts.DataDir, opts.PrimaryEndpoint); err != nil {
			if !opts.BasebackupOnRewindFailure {
				return false, err
			}
			if fallbackErr := replaceDataDirWithBasebackup(ctx, runner, opts.BinDir, opts.DataDir, opts.PrimaryEndpoint); fallbackErr != nil {
				return false, fmt.Errorf("%w; basebackup fallback failed: %w", err, fallbackErr)
			}
		}
	}
	if err := CreateStandbySignal(opts.DataDir); err != nil {
		return false, err
	}
	if err := appendPrimaryConninfo(opts.DataDir, opts.PrimaryEndpoint, opts.ApplicationName); err != nil {
		return false, err
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove %s: %w", marker, err)
	}
	return true, nil
}

func replaceDataDirWithBasebackup(ctx context.Context, runner CommandRunner, binDir, dataDir, endpoint string) error {
	if dataDir == "" || dataDir == string(filepath.Separator) {
		return fmt.Errorf("refuse to replace unsafe dataDir %q", dataDir)
	}
	quarantine := dataDir + ".rewind-failed"
	if err := os.RemoveAll(quarantine); err != nil {
		return fmt.Errorf("remove old quarantine %s: %w", quarantine, err)
	}
	if err := os.Rename(dataDir, quarantine); err != nil {
		return fmt.Errorf("quarantine %s to %s: %w", dataDir, quarantine, err)
	}
	restored := false
	defer func() {
		if restored {
			return
		}
		if rmErr := os.RemoveAll(dataDir); rmErr != nil {
			slog.Warn("quarantine rollback: RemoveAll failed", "path", dataDir, "error", rmErr)
		}
		if mvErr := os.Rename(quarantine, dataDir); mvErr != nil {
			slog.Warn("quarantine rollback: Rename failed", "from", quarantine, "to", dataDir, "error", mvErr)
		}
	}()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create fresh dataDir %s: %w", dataDir, err)
	}
	if err := runPgBasebackup(ctx, runner, binDir, dataDir, endpoint); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dataDir, RestartPrimaryAsStandbyMarker), []byte("basebackup-fallback"), 0o600); err != nil {
		return fmt.Errorf("write fallback marker: %w", err)
	}
	restored = true
	_ = os.RemoveAll(quarantine)
	return nil
}

func runPgRewind(ctx context.Context, runner CommandRunner, binDir, dataDir, endpoint string) error {
	host, port := splitEndpoint(endpoint)
	sourceServer := fmt.Sprintf("host=%s port=%s user=postgres dbname=postgres", host, port)
	if err := runner.Run(
		ctx,
		filepath.Join(binDir, "pg_rewind"),
		"--target-pgdata", dataDir,
		"--source-server="+sourceServer,
	); err != nil {
		return fmt.Errorf("pg_rewind target=%s source=%s: %w", dataDir, endpoint, err)
	}
	return nil
}

func runPgBasebackup(ctx context.Context, runner CommandRunner, binDir, dataDir, endpoint string) error {
	host, port := splitEndpoint(endpoint)
	if err := runner.Run(
		ctx,
		filepath.Join(binDir, "pg_basebackup"),
		"-D", dataDir,
		"-h", host,
		"-p", port,
		"-U", "postgres",
		"--no-password",
		"--wal-method=stream",
		"--checkpoint=fast",
	); err != nil {
		return fmt.Errorf("pg_basebackup target=%s source=%s: %w", dataDir, endpoint, err)
	}
	return nil
}

func appendPrimaryConninfo(dataDir, endpoint, applicationName string) error {
	host, port := splitEndpoint(endpoint)
	path := filepath.Join(dataDir, "postgresql.auto.conf")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	applicationNameFragment := ""
	if applicationName != "" {
		applicationNameFragment = " application_name=" + applicationName
	}
	if _, err := fmt.Fprintf(f, "primary_conninfo = 'host=%s port=%s user=postgres%s'\n", host, port, applicationNameFragment); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

func splitEndpoint(endpoint string) (string, string) {
	for i := len(endpoint) - 1; i >= 0; i-- {
		if endpoint[i] == ':' {
			return endpoint[:i], endpoint[i+1:]
		}
	}
	return endpoint, "5432"
}
