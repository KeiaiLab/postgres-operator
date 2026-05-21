/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package walg 는 WAL-G 기반 BackupPlugin 구현이다 (D.3.1).
//
// pgBackRest 와 달리 WAL-G 는 *standalone process* — executionMode=job
// 으로 배포 가능. operator manager 또는 K8s Job 안에서 환경변수
// (`WALG_S3_PREFIX` 등) 기반 백업 + restore 실행.
package walg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/keiailab/postgres-operator/internal/plugin"
)

const (
	pluginName     = "walg"
	defaultCommand = "wal-g"
)

// WAL-G backup output 에서 `Wrote backup with name <name>` 추출 (BackupID).
var backupNamePattern = regexp.MustCompile(`(?m)Wrote backup with name (\S+)`)

// Runner 는 wal-g 프로세스 실행 지점이다.
type Runner interface {
	Run(ctx context.Context, command string, args ...string) ([]byte, error)
}

// ExecRunner 는 exec.CommandContext 기반 실 runner.
type ExecRunner struct{}

// Run 은 command 를 실행하고 합쳐진 stdout/stderr 를 반환한다.
func (ExecRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, command, args...).CombinedOutput()
}

// Option 은 Plugin 생성 옵션이다.
type Option func(*Plugin)

// WithRunner 는 명령 실행 runner 를 교체한다.
func WithRunner(r Runner) Option {
	return func(p *Plugin) {
		if r != nil {
			p.runner = r
		}
	}
}

// WithCommand 는 실행할 wal-g command path 를 교체한다.
func WithCommand(c string) Option {
	return func(p *Plugin) {
		if strings.TrimSpace(c) != "" {
			p.command = c
		}
	}
}

// Plugin 은 WAL-G BackupPlugin 구현체.
type Plugin struct {
	runner  Runner
	command string
}

var _ plugin.BackupPlugin = (*Plugin)(nil)
var _ plugin.BackupCommandPlugin = (*Plugin)(nil)

// New 는 WAL-G plugin 을 생성한다.
func New(opts ...Option) *Plugin {
	p := &Plugin{runner: ExecRunner{}, command: defaultCommand}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Register 는 registry 에 WAL-G BackupPlugin 을 등록한다.
func Register(registry *plugin.Registry, opts ...Option) {
	registry.RegisterBackup(New(opts...))
}

// Name 은 BackupSpec.Tool 과 매칭되는 plugin 이름이다.
func (p *Plugin) Name() string { return pluginName }

// Validate 는 WAL-G 관점의 BackupSpec 최소 계약을 검증한다.
//
// WAL-G 는 `WALG_S3_PREFIX` 또는 `WALG_FILE_PREFIX` 등 환경변수 기반
// 저장소 구성 — 본 plugin 은 settings 가 1+ 환경변수를 명시했는지 검증.
func (p *Plugin) Validate(spec *plugin.BackupSpec) error {
	if spec == nil {
		return errors.New("walg BackupSpec is nil")
	}
	if spec.Tool != "" && spec.Tool != pluginName {
		return fmt.Errorf("walg plugin cannot validate tool %q", spec.Tool)
	}
	if len(spec.Settings) == 0 {
		return errors.New("walg requires settings with at least one WALG_* prefix environment variable")
	}
	hasPrefix := false
	for k := range spec.Settings {
		if strings.HasPrefix(k, "WALG_") {
			hasPrefix = true
			break
		}
	}
	if !hasPrefix {
		return errors.New("walg settings must include at least one WALG_* key (e.g. WALG_S3_PREFIX)")
	}
	return nil
}

// PerformBackup 은 wal-g backup-push 명령을 실행한다.
func (p *Plugin) PerformBackup(
	ctx context.Context,
	target plugin.ClusterTarget,
	opts plugin.BackupOptions,
) (plugin.BackupResult, error) {
	args, err := p.BackupCommand(target, opts)
	if err != nil {
		return plugin.BackupResult{}, err
	}
	startedAt := time.Now().UTC()
	out, err := p.runner.Run(ctx, args[0], args[1:]...)
	if err != nil {
		return plugin.BackupResult{}, fmt.Errorf("walg backup failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	res := p.ParseBackupResult(out, opts)
	res.StartedAt = startedAt
	res.EndedAt = time.Now().UTC()
	return res, nil
}

// RestorePIT 은 wal-g backup-fetch + recovery target 으로 PITR 수행.
func (p *Plugin) RestorePIT(ctx context.Context, target plugin.ClusterTarget, ts time.Time) error {
	args, err := p.RestoreCommand(target, ts)
	if err != nil {
		return err
	}
	out, err := p.runner.Run(ctx, args[0], args[1:]...)
	if err != nil {
		return fmt.Errorf("walg restore failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// BackupCommand 는 wal-g backup-push argv 를 만든다.
//
// 예: `wal-g backup-push /var/lib/postgresql/data` — PGDATA path 는
// BackupOptions.Labels["pgdata"] 또는 default `/var/lib/postgresql/data`.
func (p *Plugin) BackupCommand(target plugin.ClusterTarget, opts plugin.BackupOptions) ([]string, error) {
	if strings.TrimSpace(target.Name) == "" {
		return nil, errors.New("walg requires target cluster name")
	}
	if _, err := normalizeBackupType(opts.Type); err != nil {
		return nil, err
	}
	pgdata := "/var/lib/postgresql/data"
	if v, ok := opts.Labels["pgdata"]; ok && v != "" {
		pgdata = v
	}
	return []string{
		p.command,
		"backup-push",
		pgdata,
	}, nil
}

// RestoreCommand 는 wal-g backup-fetch LATEST + PITR recovery 안내 argv.
//
// 예: `wal-g backup-fetch /var/lib/postgresql/data LATEST` — recovery.conf
// (`recovery_target_time`) 작성은 호출자 (operator) 가 담당.
func (p *Plugin) RestoreCommand(target plugin.ClusterTarget, ts time.Time) ([]string, error) {
	if strings.TrimSpace(target.Name) == "" {
		return nil, errors.New("walg requires target cluster name")
	}
	if ts.IsZero() {
		return nil, errors.New("walg restore requires non-zero target time for PITR")
	}
	return []string{
		p.command,
		"backup-fetch",
		"/var/lib/postgresql/data",
		"LATEST",
	}, nil
}

// ParseBackupResult 는 wal-g 출력에서 BackupID 추출.
func (p *Plugin) ParseBackupResult(output []byte, opts plugin.BackupOptions) plugin.BackupResult {
	return plugin.BackupResult{
		BackupID: parseBackupName(output),
		Repo:     opts.Repo,
	}
}

func normalizeBackupType(t string) (string, error) {
	switch t {
	case "", "full":
		return "full", nil
	case "delta", "incremental", "incr":
		return "delta", nil
	default:
		return "", fmt.Errorf("unsupported walg backup type %q (only full/delta)", t)
	}
}

func parseBackupName(out []byte) string {
	m := backupNamePattern.FindSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return string(m[1])
}
