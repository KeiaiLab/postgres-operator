/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package supervise

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer 는 *exec.Cmd 의 stderr writer goroutine 과 테스트 메인 goroutine
// 사이의 동시 접근으로부터 bytes.Buffer 를 보호한다 (race detector 친화).
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (lb *lockedBuffer) Write(p []byte) (int, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.b.Write(p)
}

func (lb *lockedBuffer) String() string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.b.String()
}

// waitForStderr 는 stderr buffer 에 substr 이 나타날 때까지 polling 대기한다.
// fake-postgres 의 trap 설치 race 회피 — PID 출력 (= trap 설치 완료 신호) 을
// 기다린 후 signal 을 보내야 trap 이 의도대로 발동한다.
func waitForStderr(t *testing.T, lb *lockedBuffer, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(lb.String(), substr) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("stderr did not contain %q within %s; got=%q", substr, timeout, lb.String())
}

// fakePostgresPath 는 testdata/fake-postgres.sh 의 절대 경로를 반환한다.
func fakePostgresPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/fake-postgres.sh")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return abs
}

func newRealForTest(t *testing.T) *Real {
	t.Helper()
	cfg := Config{
		BinPath:    fakePostgresPath(t),
		DataDir:    t.TempDir(),
		ConfigFile: "/etc/postgresql.conf",
		HbaFile:    "/etc/pg_hba.conf",
		Port:       5432,
		LocalDSN:   "host=/tmp/nonexistent user=postgres dbname=postgres",
	}
	r, err := NewReal(cfg)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	return r
}

func TestNewReal_RejectsEmptyFields(t *testing.T) {
	base := Config{
		BinPath:    "/usr/bin/postgres",
		DataDir:    "/data",
		ConfigFile: "/etc/postgresql.conf",
		HbaFile:    "/etc/pg_hba.conf",
		LocalDSN:   "host=/tmp",
	}
	if _, err := NewReal(base); err != nil {
		t.Fatalf("base config valid: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"BinPath+BinDir empty", func(c *Config) { c.BinPath = ""; c.BinDir = "" }},
		{"DataDir empty", func(c *Config) { c.DataDir = "" }},
		{"ConfigFile empty", func(c *Config) { c.ConfigFile = "" }},
		{"HbaFile empty", func(c *Config) { c.HbaFile = "" }},
		{"LocalDSN empty", func(c *Config) { c.LocalDSN = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := base
			c.mutate(&cfg)
			if _, err := NewReal(cfg); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

func TestNewReal_DefaultPort(t *testing.T) {
	cfg := Config{
		BinPath: "/usr/bin/postgres", DataDir: "/data",
		ConfigFile: "/c", HbaFile: "/h", LocalDSN: "x",
	}
	r, err := NewReal(cfg)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	if r.cfg.Port != 5432 {
		t.Errorf("default Port = %d, want 5432", r.cfg.Port)
	}
}

func TestReal_PIDZeroBeforeStart(t *testing.T) {
	r := newRealForTest(t)
	if pid := r.PID(); pid != 0 {
		t.Errorf("PID before Start = %d, want 0", pid)
	}
}

// TestReal_cleanStaleLocks_RemovesPostmasterPid 는 INC-2026-06-22 회귀 가드:
// 컨테이너 PID 재활용 환경에서 직전 crash 가 남긴 postmaster.pid 를
// cleanStaleLocks (Start 의 fork 직전) 가 제거해야 "FATAL: lock file already
// exists" 무한 CrashLoopBackOff 를 차단한다. 본 라인 제거 시 본 테스트 실패.
func TestReal_cleanStaleLocks_RemovesPostmasterPid(t *testing.T) {
	r := newRealForTest(t)
	pidPath := filepath.Join(r.cfg.DataDir, "postmaster.pid")
	// 직전 crash 가 남긴 stale postmaster.pid 모사 (PID 15 = 컨테이너 재활용 PID).
	if err := os.WriteFile(pidPath, []byte("15\n/var/lib/postgresql/data/pgdata\n"), 0o600); err != nil {
		t.Fatalf("seed postmaster.pid: %v", err)
	}
	r.cleanStaleLocks()
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("postmaster.pid 가 cleanStaleLocks 후 잔존 (stat err=%v) — 제거 기대", err)
	}
}

// TestReal_cleanStaleLocks_NoPidIsNoError 는 cold start (pid 부재) best-effort
// 안전성 — 파일 부재 시 panic/생성 없이 no-op.
func TestReal_cleanStaleLocks_NoPidIsNoError(t *testing.T) {
	r := newRealForTest(t)
	r.cleanStaleLocks() // pid 부재 상태 — error/panic 없어야.
	pidPath := filepath.Join(r.cfg.DataDir, "postmaster.pid")
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("부재 pid 가 생성됨: stat err=%v", err)
	}
}

func TestReal_StartStop(t *testing.T) {
	r := newRealForTest(t)
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if pid := r.PID(); pid <= 0 {
		t.Errorf("PID after Start = %d, want > 0", pid)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Stop(stopCtx, false); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestReal_StartTwiceErrors(t *testing.T) {
	r := newRealForTest(t)
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = r.Stop(stopCtx, true)
	}()
	if err := r.Start(context.Background()); err == nil {
		t.Errorf("second Start should error")
	}
}

func TestReal_StopBeforeStartErrors(t *testing.T) {
	r := newRealForTest(t)
	if err := r.Stop(context.Background(), false); err == nil {
		t.Errorf("Stop before Start should error")
	}
}

func TestReal_StopFastImmediate(t *testing.T) {
	r := newRealForTest(t)
	r.cfg.ExtraEnv = []string{"INT_DELAY=0"}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Stop(stopCtx, true); err != nil {
		t.Fatalf("Stop fast: %v", err)
	}
}

func TestReal_StopTimeoutSendsSIGKILL(t *testing.T) {
	stderr := &lockedBuffer{}
	r := newRealForTest(t)
	r.cfg.Stderr = stderr
	// fake-postgres 가 SIGTERM 을 받고 2초 sleep 후 종료 — 1초 ctx 만료로 SIGKILL
	// 을 유도. TERM_DELAY 를 너무 크게 잡으면 SIGKILL 후에도 trap 의 sleep 자식이
	// orphan 되어 stderr pipe 를 잡고 있어 cmd.Wait() 가 그 시간만큼 block 한다.
	r.cfg.ExtraEnv = []string{"TERM_DELAY=2"}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// trap 설치 race 회피 — child 가 PID 를 stderr 에 출력했다는 것은 trap 설치
	// 직후 라인이므로, SIGTERM 도착 시 trap 이 발동하여 sleep 10 이 작동함을 보장.
	waitForStderr(t, stderr, "FAKE_POSTGRES_PID=", 2*time.Second)
	stopCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	err := r.Stop(stopCtx, false)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should mention timeout, got: %v", err)
	}
	// SIGKILL 후 child 가 결국 죽었는지 확인 — exitCh 가 close 되었을 것.
	select {
	case <-r.ExitCh():
		// OK
	case <-time.After(3 * time.Second):
		t.Errorf("ExitCh did not signal after SIGKILL within 3s")
	}
}

func TestReal_Reload_DoesNotError(t *testing.T) {
	stderr := &lockedBuffer{}
	r := newRealForTest(t)
	r.cfg.Stderr = stderr
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = r.Stop(stopCtx, true)
	}()
	// child 가 trap 을 install 하기 전에 SIGHUP 을 보내면 무시될 수 있음 — 잠시 대기
	// 후 PID 를 stderr 에 출력했는지로 trap 준비 완료를 확인.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stderr.String(), "FAKE_POSTGRES_PID=") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := r.Reload(context.Background()); err != nil {
		t.Errorf("Reload: %v", err)
	}
	// fake-postgres.sh 가 SIGHUP 시 stderr 에 "RELOADED" 출력 — 짧은 wait 후 검증.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stderr.String(), "RELOADED") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("fake-postgres did not log RELOADED within 2s; stderr=%q", stderr.String())
}

func TestReal_ReloadBeforeStartErrors(t *testing.T) {
	r := newRealForTest(t)
	if err := r.Reload(context.Background()); err == nil {
		t.Errorf("Reload before Start should error")
	}
}

func TestReal_StartFailsWithBadBinary(t *testing.T) {
	cfg := Config{
		BinPath:    "/nonexistent/path/postgres",
		DataDir:    t.TempDir(),
		ConfigFile: "/etc/postgresql.conf",
		HbaFile:    "/etc/pg_hba.conf",
		LocalDSN:   "host=/tmp",
	}
	r, err := NewReal(cfg)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	if err := r.Start(context.Background()); err == nil {
		t.Errorf("Start with nonexistent binary should error")
	}
}

func TestReal_ExitCh_BroadcastsExit(t *testing.T) {
	r := newRealForTest(t)
	r.cfg.ExtraEnv = []string{"EXIT_AFTER=0.2"}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-r.ExitCh():
		// OK — child exited within 0.2s + scheduling.
	case <-time.After(3 * time.Second):
		t.Errorf("ExitCh did not signal within 3s")
	}
}

func TestReal_StartFailsImmediately(t *testing.T) {
	r := newRealForTest(t)
	r.cfg.ExtraEnv = []string{"FAIL_ON_START=1"}
	// Start 자체는 fork 성공 후 child 가 즉시 exit — Start 자체 error 는 아님.
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start (fork) should succeed: %v", err)
	}
	// 즉시 ExitCh 로 비정상 종료 신호.
	select {
	case err := <-r.ExitCh():
		if err == nil {
			t.Errorf("expected non-nil ExitCh error (FAIL_ON_START=1)")
		}
	case <-time.After(3 * time.Second):
		t.Errorf("ExitCh did not signal within 3s")
	}
}

func TestMock_StartStop(t *testing.T) {
	m := NewMock()
	if pid := m.PID(); pid != 0 {
		t.Errorf("PID before Start = %d, want 0", pid)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if m.StartCalls != 1 {
		t.Errorf("StartCalls = %d, want 1", m.StartCalls)
	}
	if pid := m.PID(); pid != 1 {
		t.Errorf("PID after Start = %d, want 1", pid)
	}
	if err := m.Stop(context.Background(), false); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if m.StopCalls != 1 {
		t.Errorf("StopCalls = %d, want 1", m.StopCalls)
	}
}

func TestMock_StartTwiceErrors(t *testing.T) {
	m := NewMock()
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := m.Start(context.Background()); err == nil {
		t.Errorf("second Start should error")
	}
}

func TestMock_ErrorInjection(t *testing.T) {
	m := NewMock()
	want := errors.New("inject")
	m.StartErr = want
	if err := m.Start(context.Background()); !errors.Is(err, want) {
		t.Errorf("expected inject error, got %v", err)
	}
	m.StartErr = nil
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("clean Start: %v", err)
	}
	m.PromoteErr = errors.New("promote-fail")
	if err := m.Promote(context.Background()); err == nil {
		t.Errorf("expected PromoteErr injection")
	}
}

func TestMock_SlotCallTracking(t *testing.T) {
	m := NewMock()
	_ = m.CreateReplicationSlot(context.Background(), "standby0")
	_ = m.CreateReplicationSlot(context.Background(), "standby0")
	_ = m.CreateReplicationSlot(context.Background(), "standby1")
	_ = m.DropReplicationSlot(context.Background(), "ghost")
	if m.CreateSlotCalls["standby0"] != 2 {
		t.Errorf("CreateSlotCalls[standby0] = %d, want 2", m.CreateSlotCalls["standby0"])
	}
	if m.CreateSlotCalls["standby1"] != 1 {
		t.Errorf("CreateSlotCalls[standby1] = %d, want 1", m.CreateSlotCalls["standby1"])
	}
	if m.DropSlotCalls["ghost"] != 1 {
		t.Errorf("DropSlotCalls[ghost] = %d, want 1", m.DropSlotCalls["ghost"])
	}
}

func TestMock_SimulateExit(t *testing.T) {
	m := NewMock()
	want := errors.New("crashed")
	m.SimulateExit(want)
	select {
	case got := <-m.ExitCh():
		if !errors.Is(got, want) {
			t.Errorf("ExitCh got %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Errorf("ExitCh timed out")
	}
}

func TestMock_IsReady(t *testing.T) {
	m := NewMock()
	if m.IsReady(context.Background()) {
		t.Errorf("default Ready should be false")
	}
	m.Ready = true
	if !m.IsReady(context.Background()) {
		t.Errorf("Ready=true should report true")
	}
}
