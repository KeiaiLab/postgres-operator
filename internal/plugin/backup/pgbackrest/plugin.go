/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
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
// pgbackrestCommonArgs 는 readOnlyRootFilesystem + uid 70(pg-keiailab) + pgbackrest deb
// 가 생성한 /etc/pgbackrest.conf(mode 640) 환경에서 pgbackrest 가 동작하는 데 필요한
// 공통 옵션이다 (라이브 검증 2026-06-04 pg-backup-e2e):
//   --config=/dev/null    deb /etc/pgbackrest.conf permission denied(exit 41) 우회
//   --log-level-file=off  readOnlyRootFilesystem 의 /var/log/pgbackrest 쓰기 회피
//   --pg1-path            PGDATA (builders.go pgDataSubdir 미러)
//   --pg1-user/database   DB 연결 superuser (OS user pg-keiailab 는 DB role 부재)
const pgbackrestCommonArgs = "--config=/dev/null --log-level-file=off " +
	"--pg1-path=/var/lib/postgresql/data/pgdata --pg1-user=postgres --pg1-database=postgres"

func (p *Plugin) BackupCommand(target plugin.ClusterTarget, opts plugin.BackupOptions) ([]string, error) {
	backupType, err := normalizeBackupType(opts.Type)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(target.Name) == "" {
		return nil, errors.New("pgbackrest requires target cluster name as stanza")
	}
	// #209: pass filesystem repo config inline and ensure the stanza exists, so
	// the backup has a configured repository (mirrors the archive_command wrapper
	// in builders.go archiveConfigForCluster). Repo path = backupRepoMountPath.
	const repoEnv = "PGBACKREST_REPO1_TYPE=posix PGBACKREST_REPO1_PATH=/var/lib/pgbackrest"
	return []string{
		"sh", "-c",
		// `env VAR=val cmd` (not `exec VAR=val cmd`): exec is a POSIX special builtin
		// and rejects env-assignment prefixes ("exec: VAR=val: not found"). (#209 live fix)
		// --archive-check=n: emptyDir repo 의 초기 WAL 미archive 허용 (online backup).
		fmt.Sprintf("env %s %s %s --stanza=%s stanza-create 2>/dev/null || true; exec env %s %s %s --stanza=%s --repo=%s --type=%s --archive-check=n backup",
			repoEnv, p.command, pgbackrestCommonArgs, target.Name,
			repoEnv, p.command, pgbackrestCommonArgs, target.Name, normalizeRepo(opts.Repo), backupType),
	}, nil
}

// RestoreCommand 는 pgbackrest PITR restore argv 를 만든다.
func (p *Plugin) RestoreCommand(target plugin.ClusterTarget, ts time.Time) ([]string, error) {
	if strings.TrimSpace(target.Name) == "" {
		return nil, errors.New("pgbackrest requires target cluster name as stanza")
	}
	// #209: PITR restore also needs the repo configured (same filesystem repo as
	// archive_command / BackupCommand).
	const repoEnv = "PGBACKREST_REPO1_TYPE=posix PGBACKREST_REPO1_PATH=/var/lib/pgbackrest"
	return []string{
		"sh", "-c",
		fmt.Sprintf("exec env %s %s %s --stanza=%s --type=time --target=%s restore",
			repoEnv, p.command, pgbackrestCommonArgs, target.Name, formatTargetTime(ts)),
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
