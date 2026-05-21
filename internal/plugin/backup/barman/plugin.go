/*
Copyright 2026 keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package barman 는 Barman 기반 BackupPlugin 구현이다 (D.3.1).
//
// Barman 은 별도 host 또는 sidecar 에 daemon 으로 동작하며, operator 가
// `barman` CLI 로 server 식별 + backup/recover 명령을 발행한다.
package barman

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
	pluginName     = "barman"
	defaultCommand = "barman"
)

// Barman 출력 `Starting backup using ...` 다음 `Backup ID: 20251020T123456` 추출.
var backupIDPattern = regexp.MustCompile(`(?m)Backup ID:\s*(\S+)`)

// Runner 는 barman 프로세스 실행 지점이다.
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

// WithCommand 는 실행할 barman command path 를 교체한다.
func WithCommand(c string) Option {
	return func(p *Plugin) {
		if strings.TrimSpace(c) != "" {
			p.command = c
		}
	}
}

// Plugin 은 Barman BackupPlugin 구현체.
type Plugin struct {
	runner  Runner
	command string
}

var _ plugin.BackupPlugin = (*Plugin)(nil)
var _ plugin.BackupCommandPlugin = (*Plugin)(nil)

// New 는 Barman plugin 을 생성한다.
func New(opts ...Option) *Plugin {
	p := &Plugin{runner: ExecRunner{}, command: defaultCommand}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Register 는 registry 에 Barman BackupPlugin 을 등록한다.
func Register(registry *plugin.Registry, opts ...Option) {
	registry.RegisterBackup(New(opts...))
}

// Name 은 BackupSpec.Tool 과 매칭되는 plugin 이름이다.
func (p *Plugin) Name() string { return pluginName }

// Validate 는 Barman BackupSpec 의 최소 계약을 검증한다.
//
// Barman 의 server name 은 settings["server"] 또는 BackupSpec.Repo 로 식별.
func (p *Plugin) Validate(spec *plugin.BackupSpec) error {
	if spec == nil {
		return errors.New("barman BackupSpec is nil")
	}
	if spec.Tool != "" && spec.Tool != pluginName {
		return fmt.Errorf("barman plugin cannot validate tool %q", spec.Tool)
	}
	server := strings.TrimSpace(spec.Repo)
	if server == "" {
		server = spec.Settings["server"]
	}
	if strings.TrimSpace(server) == "" {
		return errors.New("barman requires server name (BackupSpec.Repo or settings[\"server\"])")
	}
	return nil
}

// PerformBackup 은 barman backup <server> 명령을 실행한다.
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
		return plugin.BackupResult{}, fmt.Errorf("barman backup failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	res := p.ParseBackupResult(out, opts)
	res.StartedAt = startedAt
	res.EndedAt = time.Now().UTC()
	return res, nil
}

// RestorePIT 은 barman recover --target-time 명령을 실행한다.
func (p *Plugin) RestorePIT(ctx context.Context, target plugin.ClusterTarget, ts time.Time) error {
	args, err := p.RestoreCommand(target, ts)
	if err != nil {
		return err
	}
	out, err := p.runner.Run(ctx, args[0], args[1:]...)
	if err != nil {
		return fmt.Errorf("barman restore failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// BackupCommand 는 barman backup argv 를 만든다.
//
// `barman backup <server>` — server 식별자는 BackupOptions.Repo 또는
// ClusterTarget.Name 으로 결정.
func (p *Plugin) BackupCommand(target plugin.ClusterTarget, opts plugin.BackupOptions) ([]string, error) {
	if _, err := normalizeBackupType(opts.Type); err != nil {
		return nil, err
	}
	server := strings.TrimSpace(opts.Repo)
	if server == "" {
		server = target.Name
	}
	if server == "" {
		return nil, errors.New("barman requires server name (Repo or target.Name)")
	}
	return []string{p.command, "backup", server}, nil
}

// RestoreCommand 는 barman recover --target-time argv 를 만든다.
//
// `barman recover --target-time "2026-05-19 12:00:00+00:00" <server> latest /var/lib/postgresql/data`
func (p *Plugin) RestoreCommand(target plugin.ClusterTarget, ts time.Time) ([]string, error) {
	if ts.IsZero() {
		return nil, errors.New("barman restore requires non-zero target time for PITR")
	}
	server := target.Name
	if server == "" {
		return nil, errors.New("barman restore requires target.Name as server identifier")
	}
	return []string{
		p.command,
		"recover",
		"--target-time", ts.UTC().Format("2006-01-02 15:04:05-07:00"),
		server,
		"latest",
		"/var/lib/postgresql/data",
	}, nil
}

// ParseBackupResult 는 barman 출력에서 BackupID 를 추출한다.
func (p *Plugin) ParseBackupResult(output []byte, opts plugin.BackupOptions) plugin.BackupResult {
	return plugin.BackupResult{
		BackupID: parseBackupID(output),
		Repo:     opts.Repo,
	}
}

func normalizeBackupType(t string) (string, error) {
	switch t {
	case "", "full":
		return "full", nil
	case "incremental", "incr":
		return "incr", nil
	default:
		return "", fmt.Errorf("unsupported barman backup type %q (only full/incremental)", t)
	}
}

func parseBackupID(out []byte) string {
	m := backupIDPattern.FindSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return string(m[1])
}
