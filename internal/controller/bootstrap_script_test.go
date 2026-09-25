/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 본 파일은 bootstrap init 스크립트를 실제로 돌린다(설계 §3.6) — 임시 PGDATA 와
// 스텁 pg_basebackup·initdb 위에서 sh 로 실행하고 파일 결과를 본다.

const (
	bootPrimary = "demo-shard-0-0.demo-shard-0-headless.ns.svc.cluster.local:5432"
	bootReplica = "demo-shard-0-1"
	bootOrdZero = "demo-shard-0-0"
)

// stubBasebackup 은 원천의 승격 마커와 conninfo 두 줄까지 복사하는 척한다.
const stubBasebackup = `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    -D) DIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$DIR"
echo 18 > "$DIR/PG_VERSION"
echo 5 > "$DIR/.keiailab-promoted-primary"
printf "primary_conninfo = 'host=old-a'\nwal_keep_size = '5GB'\nprimary_conninfo = 'host=old-b'\n" > "$DIR/postgresql.auto.conf"
`

// stubInitdb 는 호출 흔적을 binDir 에 남긴다.
const stubInitdb = `#!/bin/sh
touch "$(dirname "$0")/initdb.called"
while [ $# -gt 0 ]; do
  case "$1" in
    -D) DIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$DIR"
echo 18 > "$DIR/PG_VERSION"
`

type bootWorld struct {
	root string
	data string
	bin  string
}

func newBootWorld(t *testing.T) bootWorld {
	t.Helper()
	root := t.TempDir()
	w := bootWorld{root: root, data: filepath.Join(root, "pgdata"), bin: filepath.Join(root, "bin")}
	if err := os.MkdirAll(w.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"pg_basebackup": stubBasebackup, "initdb": stubInitdb} {
		if err := os.WriteFile(filepath.Join(w.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// seed 는 기존 PGDATA 파일을 만든다(이름 → 내용).
func (w bootWorld) seed(t *testing.T, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(w.data, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(w.data, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// run 은 스크립트를 env 로 돌린다.
func (w bootWorld) run(t *testing.T, env map[string]string) (string, error) {
	t.Helper()
	paths := bootstrapPaths{
		Data:       w.data,
		PGPass:     filepath.Join(w.root, "primary.pgpass"),
		ClientKey:  filepath.Join(w.root, "client.key"),
		ClientCert: filepath.Join(w.root, "client.crt"),
		RootCert:   filepath.Join(w.root, "root.crt"),
	}
	cmd := exec.Command("sh", "-c", renderBootstrapScript(paths, w.bin))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (w bootWorld) exists(name string) bool {
	_, err := os.Stat(filepath.Join(w.data, name))
	return err == nil
}

func (w bootWorld) read(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(w.data, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// conninfoLines 는 auto.conf 의 primary_conninfo 줄이다.
func (w bootWorld) conninfoLines(t *testing.T) []string {
	t.Helper()
	var out []string
	for line := range strings.SplitSeq(w.read(t, "postgresql.auto.conf"), "\n") {
		if strings.HasPrefix(line, "primary_conninfo") {
			out = append(out, line)
		}
	}
	return out
}

// bootEnv 는 런타임 CM 값을 받은 HA 멤버 env 다.
func bootEnv(pod, endpoint, epoch, initialized string) map[string]string {
	return map[string]string{
		"POD_NAME":              pod,
		"PRIMARY_ENDPOINT":      endpoint,
		"PROMOTION_EPOCH":       epoch,
		"CLUSTER_INITIALIZED":   initialized,
		"POSTGRES_MEMBER_COUNT": "3",
	}
}

func assertOneConninfo(t *testing.T, w bootWorld) {
	t.Helper()
	lines := w.conninfoLines(t)
	if len(lines) != 1 || !strings.Contains(lines[0], "host=demo-shard-0-0.demo-shard-0-headless.ns.svc.cluster.local ") {
		t.Fatalf("primary_conninfo lines = %q, want exactly one pointing at the runtime ConfigMap primary", lines)
	}
}

// TestBootstrapStandbyFollowsRuntime: standby.signal + 마커 → 마커 삭제, conninfo 한 줄(CM 호스트).
func TestBootstrapStandbyFollowsRuntime(t *testing.T) {
	t.Parallel()
	w := newBootWorld(t)
	w.seed(t, map[string]string{
		"PG_VERSION":           "18\n",
		"standby.signal":       "",
		promotedPrimaryMarker:  "3\n",
		"postgresql.auto.conf": "primary_conninfo = 'host=a'\nwal_keep_size = '5GB'\nprimary_conninfo = 'host=b'\nprimary_conninfo = 'host=c'\n",
	})
	if out, err := w.run(t, bootEnv(bootReplica, bootPrimary, "1", "1")); err != nil {
		t.Fatalf("bootstrap: %v\n%s", err, out)
	}
	if w.exists(promotedPrimaryMarker) {
		t.Fatal("a standby must not keep the promoted marker")
	}
	assertOneConninfo(t, w)
	if !strings.Contains(w.read(t, "postgresql.auto.conf"), "wal_keep_size = '5GB'") {
		t.Fatal("rewrite must keep other auto.conf settings")
	}
}

// TestBootstrapNewerMarkerKeepsPrimary: 마커 epoch 2 > CM 1, 다른 endpoint, standby.signal 없음 → primary 유지.
func TestBootstrapNewerMarkerKeepsPrimary(t *testing.T) {
	t.Parallel()
	w := newBootWorld(t)
	w.seed(t, map[string]string{"PG_VERSION": "18\n", promotedPrimaryMarker: "2\n", "postgresql.auto.conf": ""})
	if out, err := w.run(t, bootEnv(bootReplica, bootPrimary, "1", "1")); err != nil {
		t.Fatalf("bootstrap: %v\n%s", err, out)
	}
	if !w.exists(promotedPrimaryMarker) || w.exists("standby.signal") || w.exists(restartPrimaryAsStandbyMarker) {
		t.Fatal("a primary promoted after the ConfigMap epoch must stay primary (#220)")
	}
}

// TestBootstrapOlderMarkerDemotes: 마커 epoch 1 ≤ CM 1, 빈 옛 마커 = 0 ≤ 0 → 강등(마커 삭제·standby.signal·restart 마커).
func TestBootstrapOlderMarkerDemotes(t *testing.T) {
	t.Parallel()
	cases := map[string][2]string{
		"equal epoch":       {"1\n", "1"},
		"empty legacy is 0": {"", "0"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			w := newBootWorld(t)
			w.seed(t, map[string]string{"PG_VERSION": "18\n", promotedPrimaryMarker: tc[0], "postgresql.auto.conf": "primary_conninfo = 'host=x'\n"})
			if out, err := w.run(t, bootEnv(bootReplica, bootPrimary, tc[1], "1")); err != nil {
				t.Fatalf("bootstrap: %v\n%s", err, out)
			}
			if w.exists(promotedPrimaryMarker) || !w.exists("standby.signal") || !w.exists(restartPrimaryAsStandbyMarker) {
				t.Fatal("a superseded primary must be demoted: marker removed, standby.signal + restart marker")
			}
			assertOneConninfo(t, w)
		})
	}
}

// TestBootstrapInitializedRefusesInitdb: 빈 PGDATA ord-0 — CLUSTER_INITIALIZED=1 이면 exit≠0·initdb 0, =0 이면 initdb.
func TestBootstrapInitializedRefusesInitdb(t *testing.T) {
	t.Parallel()
	self := bootOrdZero + ".demo-shard-0-headless.ns.svc.cluster.local:5432"
	cases := map[string]struct {
		initialized string
		wantInitdb  bool
	}{
		"initialized":     {runtimeFlagOn, false},
		"not initialized": {runtimeFlagOff, true},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			w := newBootWorld(t)
			out, err := w.run(t, bootEnv(bootOrdZero, self, "0", tc.initialized))
			_, statErr := os.Stat(filepath.Join(w.bin, "initdb.called"))
			if called := statErr == nil; called != tc.wantInitdb {
				t.Fatalf("initdb called = %v, want %v\n%s", called, tc.wantInitdb, out)
			}
			if (err == nil) != tc.wantInitdb {
				t.Fatalf("exit error = %v, want failure only when initialized\n%s", err, out)
			}
		})
	}
}

// TestBootstrapBasebackupDropsCopiedMarker: basebackup 이 마커를 복사해도 끝나면 없다(I11), conninfo 한 줄.
func TestBootstrapBasebackupDropsCopiedMarker(t *testing.T) {
	t.Parallel()
	w := newBootWorld(t)
	if out, err := w.run(t, bootEnv(bootReplica, bootPrimary, "5", "1")); err != nil {
		t.Fatalf("bootstrap: %v\n%s", err, out)
	}
	if w.exists(promotedPrimaryMarker) || !w.exists("standby.signal") {
		t.Fatal("a fresh standby must have standby.signal and no copied promoted marker")
	}
	assertOneConninfo(t, w)
}
