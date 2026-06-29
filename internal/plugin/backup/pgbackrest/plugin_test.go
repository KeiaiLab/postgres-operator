/*
Copyright 2026 Keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package pgbackrest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keiailab/postgres-operator/internal/plugin"
)

type recordingRunner struct {
	command string
	args    []string
	output  []byte
	err     error
	called  int
}

func (r *recordingRunner) Run(_ context.Context, command string, args ...string) ([]byte, error) {
	r.called++
	r.command = command
	r.args = append([]string{}, args...)
	return r.output, r.err
}

func TestPluginMetadataAndValidate(t *testing.T) {
	t.Parallel()
	p := New()

	if p.Name() != "pgbackrest" {
		t.Fatalf("Name: got %q, want pgbackrest", p.Name())
	}
	if err := p.Validate(&plugin.BackupSpec{Tool: "pgbackrest", Repo: "repo1"}); err != nil {
		t.Fatalf("Validate accepted spec: %v", err)
	}
	if err := p.Validate(&plugin.BackupSpec{Tool: "walg", Repo: "repo1"}); err == nil {
		t.Fatal("Validate should reject non-pgbackrest tool")
	}
	if err := p.Validate(&plugin.BackupSpec{Tool: "pgbackrest"}); err == nil {
		t.Fatal("Validate should reject empty repo")
	}
}

func TestRegisterAddsPluginToRegistry(t *testing.T) {
	t.Parallel()
	registry := plugin.NewRegistry()

	Register(registry)

	p, ok := registry.Backup("pgbackrest")
	if !ok {
		t.Fatal("pgbackrest BackupPlugin should be registered")
	}
	if p.Name() != "pgbackrest" {
		t.Fatalf("BackupPlugin name: got %q, want pgbackrest", p.Name())
	}
}

func TestPerformBackupRunsPgBackRestCommand(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{
		output: []byte("P00   INFO: new backup label = 20260512-010203F\n"),
	}
	p := New(WithRunner(runner), WithCommand("pgbackrest-test"))

	result, err := p.PerformBackup(context.Background(), plugin.ClusterTarget{
		Namespace: "default",
		Name:      "demo",
	}, plugin.BackupOptions{
		Type: "incremental",
		Repo: "repo1",
	})
	if err != nil {
		t.Fatalf("PerformBackup error: %v", err)
	}

	// #209: backup now runs via a `sh -c` wrapper (stanza-create + repo env + backup).
	if runner.command != "sh" || len(runner.args) != 2 || runner.args[0] != "-c" {
		t.Fatalf("command should be sh -c wrapper: command=%q args=%v", runner.command, runner.args)
	}
	for _, want := range []string{"pgbackrest-test", "--stanza=demo", "--repo=1", "--type=incr", "backup", "stanza-create", "exec env ", "PGBACKREST_REPO1_PATH=/var/lib/postgresql/data/pgbackrest"} {
		if !strings.Contains(runner.args[1], want) {
			t.Fatalf("backup wrapper missing %q in %q", want, runner.args[1])
		}
	}
	if strings.Contains(runner.args[1], "--spool-path=") {
		t.Fatalf("backup wrapper must not include restore-only spool-path option: %q", runner.args[1])
	}
	if strings.Contains(runner.args[1], "--archive-check") {
		t.Fatalf("backup wrapper must not bypass WAL archive validation: %q", runner.args[1])
	}
	if result.BackupID != "20260512-010203F" {
		t.Fatalf("BackupID: got %q, want parsed label", result.BackupID)
	}
	if result.Repo != "repo1" {
		t.Fatalf("Repo: got %q, want repo1", result.Repo)
	}
}

func TestPerformBackupMapsDifferentialType(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	p := New(WithRunner(runner))

	_, err := p.PerformBackup(context.Background(), plugin.ClusterTarget{Name: "demo"}, plugin.BackupOptions{
		Type: "differential",
		Repo: "2",
	})
	if err != nil {
		t.Fatalf("PerformBackup error: %v", err)
	}

	for _, want := range []string{"--stanza=demo", "--repo=2", "--type=diff", "backup"} {
		if !strings.Contains(runner.args[1], want) {
			t.Fatalf("backup wrapper missing %q in %q", want, runner.args[1])
		}
	}
}

func TestRestorePITRunsPgBackRestTimeRestore(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	p := New(WithRunner(runner))
	targetTime := time.Date(2026, 5, 12, 1, 2, 3, 123456000, time.UTC)

	if err := p.RestorePIT(context.Background(), plugin.ClusterTarget{Name: "demo"}, targetTime); err != nil {
		t.Fatalf("RestorePIT error: %v", err)
	}

	for _, want := range []string{"rm -f /var/lib/postgresql/data/pgdata/postmaster.pid", "--stanza=demo", "--type=time", "--target='2026-05-12 01:02:03.123456+00:00'", "--delta", "restore", "env PGBACKREST_REPO1_TYPE=posix", "PGBACKREST_REPO1_PATH=/var/lib/postgresql/data/pgbackrest"} {
		if !strings.Contains(runner.args[1], want) {
			t.Fatalf("restore wrapper missing %q in %q", want, runner.args[1])
		}
	}
	for _, want := range []string{"sed -i", "postgresql.auto.conf", "restore_command = 'env PGBACKREST_REPO1_TYPE=posix PGBACKREST_REPO1_PATH=/var/lib/postgresql/data/pgbackrest", "--pg1-path=/var/lib/postgresql/data/pgdata --stanza=demo archive-get %f \\\"%p\\\""} {
		if !strings.Contains(runner.args[1], want) {
			t.Fatalf("restore wrapper missing recovery command override %q in %q", want, runner.args[1])
		}
	}
	if strings.Contains(runner.args[1], "--spool-path=") {
		t.Fatalf("restore wrapper must rely on writable /var/spool/pgbackrest mount, not pgBackRest spool-path option: %q", runner.args[1])
	}
	if strings.Contains(runner.args[1], "--target=2026-05-12 01:02:03.123456+00:00") {
		t.Fatalf("restore wrapper must quote target time with spaces: %q", runner.args[1])
	}
	for _, invalid := range []string{"--pg1-user=", "--pg1-database="} {
		if strings.Contains(runner.args[1], invalid) {
			t.Fatalf("restore wrapper must not include backup-only option %q: %q", invalid, runner.args[1])
		}
	}
}

func TestRunnerErrorsIncludeOutput(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{
		output: []byte("permission denied"),
		err:    errors.New("exit status 56"),
	}
	p := New(WithRunner(runner))

	_, err := p.PerformBackup(context.Background(), plugin.ClusterTarget{Name: "demo"}, plugin.BackupOptions{
		Type: "full",
		Repo: "repo1",
	})
	if err == nil {
		t.Fatal("PerformBackup should return runner error")
	}
	if got := err.Error(); got != "pgbackrest backup failed: exit status 56: permission denied" {
		t.Fatalf("error: got %q", got)
	}
}
