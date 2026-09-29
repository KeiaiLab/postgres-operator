/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package controller

import (
	"io/fs"
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
		Root:       w.root,
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

// tree 는 bin 밖 파일·디렉터리의 권한·내용이다 — 파일 변화 0 단언용.
func (w bootWorld) tree(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(w.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == w.bin {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		body := ""
		if !d.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			body = string(raw)
		}
		out[path] = info.Mode().String() + "|" + body
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (w bootWorld) rootFile(t *testing.T, name string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(w.root, name))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw)), true
}

func (w bootWorld) initdbCalled() bool {
	_, err := os.Stat(filepath.Join(w.bin, "initdb.called"))
	return err == nil
}

// reseedEnv 는 bootEnv 에 hold·재시드 키를 더한다.
func reseedEnv(env map[string]string, hold, reseed string) map[string]string {
	env[runtimeKeyMemberHold] = hold
	env[runtimeKeyReseedRequests] = reseed
	return env
}

// TestBootstrapHeldTouchesNothing: MEMBER_HOLD 에 자기 → exit≠0 · 파일 변화 0(대기 중인 재시드 토큰도 안 돈다).
func TestBootstrapHeldTouchesNothing(t *testing.T) {
	t.Parallel()
	w := newBootWorld(t)
	w.seed(t, map[string]string{
		"PG_VERSION":           "18\n",
		"postmaster.pid":       "",
		promotedPrimaryMarker:  "1\n",
		"postgresql.auto.conf": "primary_conninfo = 'host=a'\nprimary_conninfo = 'host=b'\n",
	})
	if err := os.Chmod(w.data, 0o755); err != nil {
		t.Fatal(err)
	}
	before := w.tree(t)
	env := reseedEnv(bootEnv(bootReplica, bootPrimary, "1", "1"), "other-0,"+bootReplica+",other-2", bootReplica+"=tok-0")

	out, err := w.run(t, env)
	if err == nil || !strings.Contains(out, "member held by operator") {
		t.Fatalf("held member: err=%v out=%s, want exit≠0 with the hold message", err, out)
	}
	after := w.tree(t)
	if len(before) != len(after) {
		t.Fatalf("held member changed files:\nbefore=%v\nafter=%v", before, after)
	}
	for path, state := range before {
		if after[path] != state {
			t.Fatalf("held member changed %s: %q → %q", path, state, after[path])
		}
	}
}

// TestBootstrapReseedToken: 토큰 대기 → PGDATA 비움·basebackup·applied=token(pending 없음) · applied==token → 삭제 0.
func TestBootstrapReseedToken(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		applied string
		wiped   bool
	}{
		"pending token": {"", true},
		"older applied": {"tok-0", true},
		"token applied": {"tok-1", false},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			w := newBootWorld(t)
			w.seed(t, map[string]string{"PG_VERSION": "18\n", "standby.signal": "", "old-data": "x", "postgresql.auto.conf": ""})
			if tc.applied != "" {
				if err := os.WriteFile(filepath.Join(w.root, reseedAppliedMarker), []byte(tc.applied+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			env := reseedEnv(bootEnv(bootReplica, bootPrimary, "1", "1"), "", "other-9=tok-9,"+bootReplica+"=tok-1")

			if out, err := w.run(t, env); err != nil {
				t.Fatalf("bootstrap: %v\n%s", err, out)
			}
			if w.exists("old-data") == tc.wiped {
				t.Fatalf("old-data present = %v, want wiped=%v", w.exists("old-data"), tc.wiped)
			}
			if applied, _ := w.rootFile(t, reseedAppliedMarker); applied != "tok-1" {
				t.Fatalf("applied = %q, want tok-1", applied)
			}
			if _, pending := w.rootFile(t, reseedPendingMarker); pending {
				t.Fatal("pending token left behind after the basebackup")
			}
			if !w.exists("standby.signal") {
				t.Fatal("reseeded member must come back as a standby")
			}
			if tc.wiped {
				assertOneConninfo(t, w)
			}
		})
	}
}

// TestBootstrapReseedNeedsOtherPrimary: 토큰 대기인데 CM primary 가 자기이거나 비었으면 exit≠0 · PGDATA 그대로.
func TestBootstrapReseedNeedsOtherPrimary(t *testing.T) {
	t.Parallel()
	self := bootReplica + ".demo-shard-0-headless.ns.svc.cluster.local:5432"
	for label, endpoint := range map[string]string{"self": self, "empty": ""} {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			w := newBootWorld(t)
			w.seed(t, map[string]string{"PG_VERSION": "18\n", "old-data": "x"})
			env := reseedEnv(bootEnv(bootReplica, endpoint, "1", "1"), "", bootReplica+"=tok-1")

			out, err := w.run(t, env)
			if err == nil || !w.exists("old-data") {
				t.Fatalf("reseed without another primary: err=%v old-data=%v, want exit≠0 with PGDATA kept\n%s", err, w.exists("old-data"), out)
			}
			if _, pending := w.rootFile(t, reseedPendingMarker); pending {
				t.Fatal("pending written although the reseed was refused")
			}
		})
	}
}

// TestBootstrapPendingRefusesInitdb: 재시드 대기(pending) ∧ 빈 PGDATA ord-0 → initdb 0 · exit≠0(I6).
func TestBootstrapPendingRefusesInitdb(t *testing.T) {
	t.Parallel()
	w := newBootWorld(t)
	if err := os.WriteFile(filepath.Join(w.root, reseedPendingMarker), []byte("tok-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	self := bootOrdZero + ".demo-shard-0-headless.ns.svc.cluster.local:5432"

	out, err := w.run(t, reseedEnv(bootEnv(bootOrdZero, self, "0", runtimeFlagOff), "", ""))
	if err == nil || w.initdbCalled() {
		t.Fatalf("pending reseed on an empty ordinal-0: err=%v initdb=%v, want exit≠0 without initdb\n%s", err, w.initdbCalled(), out)
	}
}

// TestBootstrapPassfile: hold 가 아니면 모든 길(기존 standby · primary 유지 · 재시드 · basebackup · initdb)이
// superuser passfile 을 쓴다 — 0600 · 한 줄 *:*:*:postgres:<이스케이프한 비밀번호>. HA conninfo 에는 password·passfile
// 이 없다(libpq 가 PGPASSFILE 을 읽는다, ADR-0034).
func TestBootstrapPassfile(t *testing.T) {
	t.Parallel()
	self := bootOrdZero + ".demo-shard-0-headless.ns.svc.cluster.local:5432"
	cases := map[string]struct {
		seed     map[string]string
		env      map[string]string
		conninfo bool
	}{
		"existing standby": {
			seed:     map[string]string{"PG_VERSION": "18\n", "standby.signal": "", "postgresql.auto.conf": "primary_conninfo = 'host=a'\n"},
			env:      bootEnv(bootReplica, bootPrimary, "1", "1"),
			conninfo: true,
		},
		"primary kept": {
			seed: map[string]string{"PG_VERSION": "18\n", promotedPrimaryMarker: "2\n", "postgresql.auto.conf": ""},
			env:  bootEnv(bootReplica, bootPrimary, "1", "1"),
		},
		"reseed": {
			seed:     map[string]string{"PG_VERSION": "18\n", "standby.signal": "", "postgresql.auto.conf": ""},
			env:      reseedEnv(bootEnv(bootReplica, bootPrimary, "1", "1"), "", bootReplica+"=tok-1"),
			conninfo: true,
		},
		"basebackup": {env: bootEnv(bootReplica, bootPrimary, "1", "1"), conninfo: true},
		"initdb":     {env: bootEnv(bootOrdZero, self, "0", runtimeFlagOff)},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			w := newBootWorld(t)
			if tc.seed != nil {
				w.seed(t, tc.seed)
			}
			tc.env[superuserPasswordEnv] = testSuperuserPassword

			if out, err := w.run(t, tc.env); err != nil {
				t.Fatalf("bootstrap: %v\n%s", err, out)
			}
			path := filepath.Join(w.root, "primary.pgpass")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("passfile: %v", err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("passfile mode = %v, want 0600 (libpq ignores group or world access)", info.Mode().Perm())
			}
			raw, _ := os.ReadFile(path)
			if want := `*:*:*:postgres:plain-TEXT 7\:q\\z` + "\n"; string(raw) != want {
				t.Fatalf("passfile = %q, want %q", raw, want)
			}
			if !tc.conninfo {
				return
			}
			for _, line := range w.conninfoLines(t) {
				if strings.Contains(line, "password") || strings.Contains(line, "passfile") {
					t.Fatalf("HA primary_conninfo carries a credential: %q", line)
				}
			}
			assertOneConninfo(t, w)
		})
	}
}
