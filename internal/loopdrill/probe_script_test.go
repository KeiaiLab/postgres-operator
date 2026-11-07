//go:build unix

/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package loopdrill

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// 탐침 공급기(write-probe.yaml 의 bash)를 가짜 psql 아래 돌려 due · sent 격자를 잰다(ADR-0039 HS4).

const (
	probeManifest   = "../../test/e2e/loopdrill/testdata/write-probe.yaml"
	scriptInterval  = time.Second             // 시험 간격 — 매니페스트 5 s 대신 짧게
	scriptStall     = 1500 * time.Millisecond // 첫 sleep 에 더하는 정지 — 밀린 틱을 만든다
	scriptStatement = 4                       // 읽을 INSERT 수
	scriptBound     = 20 * time.Second
)

// dueSent 는 INSERT 문 끝의 ' <due> <sent>' 리터럴.
var dueSent = regexp.MustCompile(`' ([0-9]+\.[0-9]{9}) ([0-9]+\.[0-9]{9})';$`)

// probeScript 는 매니페스트 컨테이너 args[0] — 파드가 bash -c 로 도는 그 글자.
func probeScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(probeManifest)
	if err != nil {
		t.Fatal(err)
	}
	var pod struct {
		Spec struct {
			Containers []struct {
				Args []string `json:"args"`
			} `json:"containers"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &pod); err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.Containers) != 1 || len(pod.Spec.Containers[0].Args) != 1 {
		t.Fatalf("탐침 컨테이너 args %+v", pod.Spec.Containers)
	}
	return pod.Spec.Containers[0].Args[0]
}

// fakeBin 은 가짜 psql(입력을 그대로 낸다)과 첫 호출만 scriptStall 더 자는 sleep 을 둔 디렉터리.
func fakeBin(t *testing.T) string {
	t.Helper()
	realSleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "stalled")
	files := map[string]string{
		"psql": "#!/bin/sh\nexec cat\n",
		"sleep": fmt.Sprintf("#!/bin/sh\nif [ ! -e %q ]; then : > %q; %q %s; fi\nexec %q \"$@\"\n",
			marker, marker, realSleep, fmt.Sprintf("%.3f", scriptStall.Seconds()), realSleep),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runProbe 는 공급기를 돌려 처음 scriptStatement 개 INSERT 의 due · sent 를 거둔다.
func runProbe(t *testing.T) []ProbeAck {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("bash 없음 — 탐침 공급기를 잴 수 없다")
	}
	cmd := exec.Command(bash, "-c", probeScript(t))
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin(t)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PROBE_HOST=localhost",
		fmt.Sprintf("PROBE_INTERVAL_SECONDS=%d", int(scriptInterval.Seconds())))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// 공급기·가짜 psql·sleep 을 프로세스 그룹째 거둔다
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	timer := time.AfterFunc(scriptBound, func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	defer timer.Stop()

	// 가짜 psql 이 낸 INSERT 끝의 due · sent → 서버 exec 대신 sent 를 넣은 5-필드 ack 줄
	var acks []ProbeAck
	lines := bufio.NewScanner(out)
	for id := 1; len(acks) < scriptStatement && lines.Scan(); {
		m := dueSent.FindStringSubmatch(lines.Text())
		if m == nil {
			continue
		}
		got := ParseAcks(fmt.Sprintf("ack %d %s %s %s", id, m[2], m[1], m[2]))
		if len(got) != 1 || got[0].Due.IsZero() || got[0].Sent.IsZero() {
			t.Fatalf("5-필드 ack 못 읽음: %q → %+v", lines.Text(), got)
		}
		acks = append(acks, got[0])
		id++
	}
	if len(acks) < scriptStatement {
		t.Fatalf("INSERT %d개(기대 %d) — 시한 %s", len(acks), scriptStatement, scriptBound)
	}
	return acks
}

func TestProbeScriptDueSent(t *testing.T) {
	acks := runProbe(t)

	// due 는 정확히 간격씩, sent 는 due 이후
	for i, a := range acks {
		if a.Sent.Before(a.Due) {
			t.Fatalf("ack %d sent %s < due %s", a.ID, a.Sent, a.Due)
		}
		if i == 0 {
			continue
		}
		if step := a.Due.Sub(acks[i-1].Due); step != scriptInterval {
			t.Fatalf("ack %d due 간격 %s(기대 %s)", a.ID, step, scriptInterval)
		}
	}

	// 정지 뒤 밀린 틱은 건너뛰지 않고 늦게 나간다(sent − due ≥ 정지의 대부분)
	if late := acks[1].Sent.Sub(acks[1].Due); late < scriptStall/2 {
		t.Fatalf("정지 뒤 틱 지연 %s(기대 ≥ %s) — 밀린 틱을 다시 잡았다", late, scriptStall/2)
	}
}
