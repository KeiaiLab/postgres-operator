/*
Copyright 2026 Keiailab.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package pgbackrest 는 pgBackRest 기반 BackupPlugin reference 구현이다.
package pgbackrest

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
	pluginName     = "pgbackrest"
	defaultCommand = "pgbackrest"
)

var backupLabelPattern = regexp.MustCompile(`(?m)(?:new )?backup label = ([A-Za-z0-9_.:-]+)`)

// Runner 는 pgBackRest 프로세스 실행 지점이다. 운영 기본값은 OS command 실행이고,
// 테스트는 이 인터페이스로 명령 계약만 검증한다.
type Runner interface {
	Run(ctx context.Context, command string, args ...string) ([]byte, error)
}

// ExecRunner 는 exec.CommandContext 기반 실제 runner 다.
type ExecRunner struct{}

// Run 은 command 를 실행하고 stdout/stderr 를 합친 출력을 반환한다.
func (ExecRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, command, args...).CombinedOutput()
}

// Option 은 Plugin 생성 옵션이다.
type Option func(*Plugin)

// WithRunner 는 명령 실행 runner 를 교체한다.
func WithRunner(runner Runner) Option {
	return func(p *Plugin) {
		if runner != nil {
			p.runner = runner
		}
	}
}

// WithCommand 는 실행할 pgBackRest command path/name 을 교체한다.
func WithCommand(command string) Option {
	return func(p *Plugin) {
		if strings.TrimSpace(command) != "" {
			p.command = command
		}
	}
}

// Plugin 은 pgBackRest BackupPlugin 구현체다.
type Plugin struct {
	runner  Runner
	command string
}

var _ plugin.BackupPlugin = (*Plugin)(nil)
var _ plugin.BackupCommandPlugin = (*Plugin)(nil)

// New 는 pgBackRest plugin 을 생성한다.
func New(opts ...Option) *Plugin {
	p := &Plugin{
		runner:  ExecRunner{},
		command: defaultCommand,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Register 는 registry 에 pgBackRest BackupPlugin 을 등록한다.
func Register(registry *plugin.Registry, opts ...Option) {
	registry.RegisterBackup(New(opts...))
}

// Name 은 BackupSpec.Tool 과 매칭되는 plugin 이름이다.
func (p *Plugin) Name() string {
	return pluginName
}

// Validate 는 pgBackRest 관점의 최소 BackupSpec 계약을 검증한다.
func (p *Plugin) Validate(spec *plugin.BackupSpec) error {
	if spec == nil {
		return errors.New("pgbackrest BackupSpec is nil")
	}
	if spec.Tool != "" && spec.Tool != pluginName {
		return fmt.Errorf("pgbackrest plugin cannot validate tool %q", spec.Tool)
	}
	if strings.TrimSpace(spec.Repo) == "" {
		return errors.New("pgbackrest requires repo")
	}
	return nil
}

// PerformBackup 은 pgbackrest backup 명령을 실행한다.
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
		return plugin.BackupResult{}, fmt.Errorf("pgbackrest backup failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	result := p.ParseBackupResult(out, opts)
	result.StartedAt = startedAt
	result.EndedAt = time.Now().UTC()
	return result, nil
}

// RestorePIT 은 pgbackrest restore --type=time 명령을 실행한다.
func (p *Plugin) RestorePIT(ctx context.Context, target plugin.ClusterTarget, ts time.Time) error {
	args, err := p.RestoreCommand(target, ts)
	if err != nil {
		return err
	}
	out, err := p.runner.Run(ctx, args[0], args[1:]...)
	if err != nil {
		return fmt.Errorf("pgbackrest restore failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// BackupCommand 는 pgbackrest backup argv 를 만든다.
func (p *Plugin) BackupCommand(target plugin.ClusterTarget, opts plugin.BackupOptions) ([]string, error) {
	backupType, err := normalizeBackupType(opts.Type)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(target.Name) == "" {
		return nil, errors.New("pgbackrest requires target cluster name as stanza")
	}
	return []string{
		p.command,
		"--stanza=" + target.Name,
		"--repo=" + normalizeRepo(opts.Repo),
		"--type=" + backupType,
		"backup",
	}, nil
}

// RestoreCommand 는 pgbackrest PITR restore argv 를 만든다.
func (p *Plugin) RestoreCommand(target plugin.ClusterTarget, ts time.Time) ([]string, error) {
	if strings.TrimSpace(target.Name) == "" {
		return nil, errors.New("pgbackrest requires target cluster name as stanza")
	}
	return []string{
		p.command,
		"--stanza=" + target.Name,
		"--type=time",
		"--target=" + formatTargetTime(ts),
		"restore",
	}, nil
}

// ParseBackupResult 는 pgbackrest 출력에서 backup label 을 추출한다.
func (p *Plugin) ParseBackupResult(output []byte, opts plugin.BackupOptions) plugin.BackupResult {
	return plugin.BackupResult{
		BackupID: parseBackupLabel(output),
		Repo:     opts.Repo,
	}
}

func normalizeBackupType(backupType string) (string, error) {
	switch backupType {
	case "", "full":
		return "full", nil
	case "incremental", "incr":
		return "incr", nil
	case "differential", "diff":
		return "diff", nil
	default:
		return "", fmt.Errorf("unsupported pgbackrest backup type %q", backupType)
	}
}

func normalizeRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	if trimmed, ok := strings.CutPrefix(repo, "repo"); ok {
		if trimmed != "" {
			return trimmed
		}
	}
	return repo
}

func parseBackupLabel(output []byte) string {
	match := backupLabelPattern.FindSubmatch(output)
	if len(match) < 2 {
		return ""
	}
	return string(match[1])
}

func formatTargetTime(ts time.Time) string {
	return ts.UTC().Format("2006-01-02 15:04:05-07:00")
}
