/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package supervise 는 instance manager 의 PID 1 책임 — postgres 자식
// 프로세스 lifecycle 감독 + SQL 명령 (promote, replication slot) — 을 추상화한다
// (ADR 0002 PID 1 모델, RFC 0001 v2 shard 모델).
//
// 본 패키지는 Supervisor 인터페이스 + 두 구현 (Real / Mock) 만 보유한다.
// Real 은 Go 표준 os/exec 패키지의 CommandContext 헬퍼로 postgres 자식을 fork
// 하고 lib/pq 로 SQL 을 호출한다. Mock 은 단위 테스트용 — call counter +
// 시뮬레이션 ExitCh 채널만.
//
// 호출자 (cmd/instance/main) 는 다음 흐름을 가정한다:
//
//  1. NewReal(cfg) 로 인스턴스 생성.
//  2. Start(ctx) — child fork 후 즉시 반환. ExitCh 는 child 종료 통보용.
//  3. IsReady(ctx) 가 true 가 될 때까지 polling.
//  4. election callback 안에서 Promote / Stop / CreateReplicationSlot 호출.
//  5. 종료 시 Stop(ctx, fast) — fast=true 면 SIGINT (immediate),
//     fast=false 면 SIGTERM (smart).
//
// SQL 인증은 LocalDSN (Unix socket 경유 권장 — postgres user 로 trust 인증)
// 으로 수행되므로 password 가 binary 안에 잔류하지 않는다.
package supervise

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sync"
	"syscall"

	_ "github.com/lib/pq" // postgres driver — sql.Open("postgres", ...) 등록용
)

// Config 는 Real Supervisor 의 부트스트랩 매개변수.
//
// BinPath 가 비어 있으면 BinDir 와 "postgres" 를 결합하여 사용한다 — Pod 안에서
// PG major version 별 path (`/usr/lib/postgresql/18/bin`) 를 알아야 하므로
// caller (cmd/instance/main 또는 bootstrap 패키지) 가 결정한 후 주입한다.
type Config struct {
	// BinPath 는 postgres 바이너리의 절대 경로. 비어 있으면 BinDir/postgres.
	BinPath string

	// BinDir 는 postgres 바이너리가 들어있는 디렉터리.
	BinDir string

	// DataDir 는 PGDATA — `${PGDATA}/PG_VERSION` 등이 위치하는 디렉터리.
	// PVC root 에 직접 두지 않고 subdir 사용 권장 (lost+found 충돌 회피).
	DataDir string

	// ConfigFile 은 mounted ConfigMap 의 postgresql.conf 경로 (read-only).
	ConfigFile string

	// HbaFile 은 mounted ConfigMap 의 pg_hba.conf 경로 (read-only).
	HbaFile string

	// Port 는 postgres 가 listen 할 TCP 포트. 0 이면 5432.
	Port int

	// LocalDSN 은 lib/pq DSN — Promote / IsReady / replication slot 호출용.
	// unix socket 경유 권장 (예: "host=/var/run/postgresql user=postgres dbname=postgres").
	LocalDSN string

	// Stdout / Stderr 는 child 의 표준 출력 sink. nil 이면 os.Stdout / os.Stderr.
	Stdout io.Writer
	Stderr io.Writer

	// ExtraEnv 는 child 환경 변수 추가분 (KEY=VAL 형식). 부모 환경에 append.
	// 테스트 + PG 별도 env 변수 (TZ, LC_ALL 등) 주입에 사용.
	ExtraEnv []string

	// ChildKilledSignal 은 instance 가 죽으면 child 에게 보낼 시그널 (Linux Pdeathsig).
	// 0 이면 SIGTERM. Linux 외 OS 에서는 무시.
	ChildKilledSignal syscall.Signal
}

// Supervisor 는 instance manager 가 supervise 하는 postgres child 의 추상이다.
// Real 은 production 구현, Mock 은 테스트용.
type Supervisor interface {
	// Start 는 postgres child 를 fork 하고 즉시 반환. 두 번 호출 시 error.
	Start(ctx context.Context) error

	// Stop 은 fast=false 면 SIGTERM (smart shutdown), fast=true 면 SIGINT
	// (immediate). ctx 만료 시 SIGKILL 후 error.
	Stop(ctx context.Context, fast bool) error

	// Reload 는 SIGHUP 으로 postgresql.conf 재로드.
	Reload(ctx context.Context) error

	// Promote 는 SQL `pg_promote(true, 30)` 호출.
	Promote(ctx context.Context) error

	// CreateReplicationSlot 은 idempotent — 이미 존재하면 no-op.
	CreateReplicationSlot(ctx context.Context, slotName string) error

	// DropReplicationSlot 은 idempotent — 부재 시 no-op.
	DropReplicationSlot(ctx context.Context, slotName string) error

	// IsReady 는 SELECT 1 round-trip 으로 postgres 응답 확인. 실패 = false.
	IsReady(ctx context.Context) bool

	// LagBytes 는 PostgreSQL 의 WAL lag (bytes) 를 측정한다.
	//
	// primary: pg_stat_replication 의 max(pg_wal_lsn_diff(current, flush_lsn)).
	//   replica 가 미연결/0 개면 0.
	// replica: pg_wal_lsn_diff(last_receive, last_replay).
	//
	// 측정 실패 (쿼리 에러) 시 -1 반환 — 호출자가 N/A 표기. error 는 별도로
	// 반환 안 함 (status reporter 가 매 5s 호출 — error spam 회피).
	LagBytes(ctx context.Context) int64

	// ExitCh 는 child 종료 통보 — 정상 종료면 nil, 비정상이면 *exec.ExitError.
	// 채널은 한 번만 송출 후 close.
	ExitCh() <-chan error

	// PID 는 child PID. child 부재면 0.
	PID() int
}

// Real 은 production Supervisor 구현 — Go 표준 os/exec 으로 postgres child 를 fork.
type Real struct {
	cfg Config

	mu      sync.Mutex
	cmd     *osexec.Cmd
	db      *sql.DB
	started bool
	exitCh  chan error
}

// NewReal 은 Config 검증 + Real 인스턴스 생성. Start() 는 호출자 책임.
func NewReal(cfg Config) (*Real, error) {
	if cfg.BinPath == "" && cfg.BinDir == "" {
		return nil, errors.New("supervise: BinPath or BinDir must be set")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("supervise: DataDir must not be empty")
	}
	if cfg.ConfigFile == "" {
		return nil, errors.New("supervise: ConfigFile must not be empty")
	}
	if cfg.HbaFile == "" {
		return nil, errors.New("supervise: HbaFile must not be empty")
	}
	if cfg.LocalDSN == "" {
		return nil, errors.New("supervise: LocalDSN must not be empty")
	}
	if cfg.Port == 0 {
		cfg.Port = 5432
	}
	return &Real{
		cfg:    cfg,
		exitCh: make(chan error, 1),
	}, nil
}

// resolveBinPath 는 BinPath 또는 BinDir/postgres 를 결정한다.
func (r *Real) resolveBinPath() string {
	if r.cfg.BinPath != "" {
		return r.cfg.BinPath
	}
	return filepath.Join(r.cfg.BinDir, "postgres")
}

// Start 는 postgres child 를 fork 한다. 두 번 호출 시 error.
func (r *Real) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return errors.New("supervise: already started")
	}
	bin := r.resolveBinPath()
	args := []string{
		"-D", r.cfg.DataDir,
		"-c", "config_file=" + r.cfg.ConfigFile,
		"-c", "hba_file=" + r.cfg.HbaFile,
		"-p", fmt.Sprintf("%d", r.cfg.Port),
	}
	cmd := osexec.CommandContext(ctx, bin, args...)
	cmd.Stdout = r.cfg.Stdout
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = r.cfg.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if len(r.cfg.ExtraEnv) > 0 {
		cmd.Env = append(os.Environ(), r.cfg.ExtraEnv...)
	}
	sig := r.cfg.ChildKilledSignal
	if sig == 0 {
		sig = syscall.SIGTERM
	}
	setupChildProcessAttrs(cmd, sig)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("supervise: postgres start: %w", err)
	}
	r.cmd = cmd
	r.started = true

	// Watcher goroutine — child 종료 신호 broadcasting.
	go func(c *osexec.Cmd) {
		err := c.Wait()
		select {
		case r.exitCh <- err:
		default:
		}
		close(r.exitCh)
	}(cmd)

	return nil
}

// Stop 은 graceful (fast=false: SIGTERM) 또는 immediate (fast=true: SIGINT) 로
// postgres child 종료를 시도한다. ctx 만료 시 SIGKILL 후 error.
//
// PostgreSQL signal semantics:
//   - SIGTERM = smart shutdown (활성 connection 종료 후).
//   - SIGINT  = fast shutdown  (활성 transaction abort 후 즉시 종료).
//   - SIGQUIT = immediate (dirty exit — 본 함수에서 사용 안 함).
//
// SIGKILL 은 ctx 만료 시 강제 종료용. dirty 하지만 split-brain 방지가 우선.
func (r *Real) Stop(ctx context.Context, fast bool) error {
	r.mu.Lock()
	cmd := r.cmd
	started := r.started
	r.mu.Unlock()
	if !started || cmd == nil || cmd.Process == nil {
		return errors.New("supervise: not started")
	}
	sig := syscall.SIGTERM
	if fast {
		sig = syscall.SIGINT
	}
	if err := cmd.Process.Signal(sig); err != nil {
		return fmt.Errorf("supervise: signal %s: %w", sig, err)
	}
	select {
	case <-r.exitCh:
		return nil
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGKILL)
		return fmt.Errorf("supervise: stop timed out, sent SIGKILL: %w", ctx.Err())
	}
}

// Reload 는 SIGHUP 으로 postgresql.conf 재로드.
func (r *Real) Reload(_ context.Context) error {
	r.mu.Lock()
	cmd := r.cmd
	started := r.started
	r.mu.Unlock()
	if !started || cmd == nil || cmd.Process == nil {
		return errors.New("supervise: not started")
	}
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		return fmt.Errorf("supervise: SIGHUP: %w", err)
	}
	return nil
}

// PID 는 child PID. child 부재면 0.
func (r *Real) PID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil || r.cmd.Process == nil {
		return 0
	}
	return r.cmd.Process.Pid
}

// ExitCh 는 child 종료 통보 채널.
func (r *Real) ExitCh() <-chan error {
	return r.exitCh
}

// Compile-time guard.
var _ Supervisor = (*Real)(nil)
