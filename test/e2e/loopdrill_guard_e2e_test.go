//go:build e2e

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package e2e

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// 훈련장 오조준 차단 시험 — 클러스터 없이 돈다: go test -tags=e2e ./test/e2e/ -run '^TestLoopDrillGuard$'
// 드라이버 층은 훈련장 kubeconfig 없이 클러스터 명령을 부르지 않고, 서버가 kind 루프백이 아니면 시작·유발 직전에
// Fatal 로 멈추며 파괴 동작(파드 삭제 · docker stop · SIGSTOP · pg-stop)은 명령 전에 오류를 낸다.

// fatalRecorder 는 Fatalf 를 받아 적고 그 고루틴만 끝낸다(testing.T 처럼).
type fatalRecorder struct {
	testing.TB
	mu     sync.Mutex
	fatals []string
}

func (f *fatalRecorder) Fatalf(format string, args ...any) {
	f.mu.Lock()
	f.fatals = append(f.fatals, fmt.Sprintf(format, args...))
	f.mu.Unlock()
	runtime.Goexit()
}

// fatalCount 는 fn 을 고루틴에서 돌려 Fatalf 수를 센다.
func (f *fatalRecorder) fatalCount(fn func()) int {
	var wg sync.WaitGroup
	wg.Go(fn)
	wg.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fatals)
}

func writeKubeconfig(t *testing.T, dir, name, server string) string {
	t.Helper()
	raw := fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: %s\n"+
		"clusters:\n- name: c\n  cluster:\n    server: %s\n"+
		"contexts:\n- name: %s\n  context:\n    cluster: c\n    user: u\n"+
		"users:\n- name: u\n  user: {}\n", kindContext, server, kindContext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoopDrillGuard(t *testing.T) {
	dir := t.TempDir()
	kindCfg := writeKubeconfig(t, dir, "kind", "https://127.0.0.1:43215")
	// .invalid 는 해석되지 않는다 — 가드가 깨져도 운영으로 요청이 나가지 않는다(RFC 6761)
	prodCfg := writeKubeconfig(t, dir, "prod", "https://kube.keiailab.invalid:6443")
	saved := drillKubeconfig
	t.Cleanup(func() { drillKubeconfig = saved })

	// 1. kubeconfig 없음 — 클러스터 명령을 부르지 않는다
	drillKubeconfig = ""
	calls := map[string]func() error{
		"kubectl":     func() error { _, err := kubectl("get", "nodes"); return err },
		"kubectlLong": func() error { _, err := kubectlLong("get", "nodes"); return err },
		"kubectlIn":   func() error { _, err := kubectlIn("x", "apply", "-f", "-"); return err },
		"helm":        func() error { _, err := helm("list"); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, errNoKubeconfig) {
			t.Fatalf("%s 가 kubeconfig 없이 불렸다: %v", name, err)
		}
	}
	if err := guardTarget(); !errors.Is(err, errNoKubeconfig) {
		t.Fatalf("kubeconfig 없음 판정 %v", err)
	}

	// 2. kind 루프백 — 통과, Fatal 0
	drillKubeconfig = kindCfg
	if err := guardTarget(); err != nil {
		t.Fatalf("kind kubeconfig 가 막혔다: %v", err)
	}
	green := &fatalRecorder{TB: t}
	if n := green.fatalCount((&drill{t: green}).mustTarget); n != 0 {
		t.Fatalf("kind kubeconfig 에서 Fatal %d: %v", n, green.fatals)
	}

	// 3. 운영으로 바뀐 kubeconfig — 시작·유발 직전 Fatal, 파괴 동작은 명령 전에 오류
	drillKubeconfig = prodCfg
	red := &fatalRecorder{TB: t}
	if n := red.fatalCount((&drill{t: red}).mustTarget); n != 1 {
		t.Fatalf("운영 kubeconfig 에서 Fatal %d(기대 1)", n)
	}
	primary := member{Pod: memberName(0), Node: "loopdrill-worker", ContainerID: "containerd://x", Role: rolePrimary}
	standby := member{Pod: memberName(1), Node: "loopdrill-worker2", ContainerID: "containerd://y", Role: roleStandby}
	obs := observation{Members: []member{primary, standby}}
	faults := map[string]func() error{
		"파드 삭제":         func() error { _, err := deletePrimary(forceDelete)(obs, 0); return err },
		"매니저 SIGSTOP":   func() error { _, err := stopPrimaryManager(obs, 0); return err },
		"control-plane": func() error { _, err := pauseControlPlane(obs, 0); return err },
		"standby 노드":    func() error { _, err := stopStandbyNode(nodeRBACAsIs)(obs, 0); return err },
		"primary 노드":    func() error { _, err := stopPrimaryNode(obs, 0); return err },
		"postmaster":    func() error { _, err := freezeStandby(obs, 0); return err },
		"S6b":           func() error { _, err := detachThenKill(obs, 0); return err },
		"S8":            func() error { _, err := stallRollout(obs, 0); return err },
		"pg-stop":       func() error { _, _, err := pgStop("hold", "--apply", "--no-flux"); return err },
		"teardown":      func() error { return teardownCluster(prodCfg) },
	}
	for name, fault := range faults {
		if err := fault(); !errors.Is(err, errMistarget) {
			t.Fatalf("%s 가 운영 kubeconfig 에서 멈추지 않았다: %v", name, err)
		}
	}
}
