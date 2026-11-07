/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// 본 파일은 Go 빌드 Dockerfile 의 golang FROM 이 모두 go.mod toolchain 과 같은 한 판본 · 한 다이제스트인지
// 본다 — 상류 동기화가 toolchain 만 올리고 포크 빌드 파일이 옛 판본에 남는 일을 막는다.
// Dockerfile.pg 는 I26 보호 대상이라 제외한다.
package chart

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
)

// goDockerfiles 는 Go 바이너리를 굽는 Dockerfile 이다(상류 + 포크).
var goDockerfiles = []string{
	"../../Dockerfile",
	"../../Dockerfile.keiailab",
	"../../Dockerfile.router",
	"../../Dockerfile.reshard",
	"../../Dockerfile.bench",
}

// golangFrom 은 `FROM [--platform=…] golang:<tag>@<digest>` 에서 tag 와 digest 를 뽑는다.
var golangFrom = regexp.MustCompile(`^FROM\s+(?:--platform=\S+\s+)?\S*golang:([^@\s]+)@(sha256:[0-9a-f]{64})`)

// toolchainLine 은 go.mod 의 `toolchain go<ver>` 줄이다.
var toolchainLine = regexp.MustCompile(`^toolchain\s+go(\S+)$`)

func TestDockerfileGoMatchesToolchain(t *testing.T) {
	want := goModToolchain(t)

	// 첫 다이제스트가 기준 — 나머지는 같아야 한다.
	digest := ""
	for _, path := range goDockerfiles {
		froms := scanGolangFroms(t, path)
		if len(froms) == 0 {
			t.Errorf("%s: golang FROM 없음(다이제스트 고정 필수)", path)
			continue
		}

		for _, m := range froms {
			if m[1] != want {
				t.Errorf("%s: golang:%s, go.mod toolchain go%s", path, m[1], want)
			}
			if digest == "" {
				digest = m[2]
			}
			if m[2] != digest {
				t.Errorf("%s: 다이제스트 %s, 기준 %s", path, m[2], digest)
			}
		}
	}
}

// goModToolchain 은 go.mod 의 toolchain 판본(예: 1.26.8)을 돌려준다.
func goModToolchain(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("go.mod: %v", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		m := toolchainLine.FindStringSubmatch(strings.TrimSpace(line))
		if m != nil {
			return m[1]
		}
	}
	t.Fatal("go.mod: toolchain 줄 없음")
	return ""
}

// scanGolangFroms 는 path 의 golang FROM 매치를 모두 돌려준다.
func scanGolangFroms(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	var out [][]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := golangFrom.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}
