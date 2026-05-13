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

package pgbackrest

import (
	"context"
	"errors"
	"reflect"
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

	wantArgs := []string{"--stanza=demo", "--repo=1", "--type=incr", "backup"}
	if runner.command != "pgbackrest-test" || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("command mismatch: command=%q args=%v want %v", runner.command, runner.args, wantArgs)
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

	wantArgs := []string{"--stanza=demo", "--repo=2", "--type=diff", "backup"}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("args=%v want %v", runner.args, wantArgs)
	}
}

func TestRestorePITRunsPgBackRestTimeRestore(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	p := New(WithRunner(runner))
	targetTime := time.Date(2026, 5, 12, 1, 2, 3, 0, time.UTC)

	if err := p.RestorePIT(context.Background(), plugin.ClusterTarget{Name: "demo"}, targetTime); err != nil {
		t.Fatalf("RestorePIT error: %v", err)
	}

	wantArgs := []string{"--stanza=demo", "--type=time", "--target=2026-05-12 01:02:03+00:00", "restore"}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("args=%v want %v", runner.args, wantArgs)
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
