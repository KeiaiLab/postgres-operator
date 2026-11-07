/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runMainEnv 가 설정되면 이 시험 바이너리는 main() 을 실행하는 자식 프로세스가 된다.
const runMainEnv = "RESHARD_COPY_POC_RUN_MAIN"

// unreachableDSN 은 즉시 connection refused 로 끝나는 source/target DSN 이다.
const unreachableDSN = "postgres://u@127.0.0.1:1/db?sslmode=disable&connect_timeout=2"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) != "" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestMainCLI_Characterization 은 main 의 env 검증·종료 코드·출력을 고정한다 — 리팩터
// 뒤에도 같은 입력이 같은 종료 코드와 메시지를 내야 한다.
func TestMainCLI_Characterization(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		wantCode int
		wantErr  []string
		wantOut  []string
	}{
		{
			name:     "missing source",
			env:      map[string]string{},
			wantCode: 2,
			wantErr:  []string{"reshard-copy-poc: PGROUTER_SOURCE_DSN required\n"},
		},
		{
			name:     "cdc without target shard",
			env:      map[string]string{"PGROUTER_SOURCE_DSN": "s", "PGROUTER_RESHARD_MODE": "cdc-setup"},
			wantCode: 2,
			wantErr:  []string{"reshard-copy-poc: CDC requires PGROUTER_RESHARD_TARGET_SHARD\n"},
		},
		{
			name: "cdc without target dsn",
			env: map[string]string{"PGROUTER_SOURCE_DSN": "s", "PGROUTER_RESHARD_MODE": "cdc-abort",
				"PGROUTER_RESHARD_TARGET_SHARD": "shard-1"},
			wantCode: 2,
			wantErr:  []string{"reshard-copy-poc: CDC requires PGROUTER_TARGET_DSN\n"},
		},
		{
			name:     "delete only without target shard",
			env:      map[string]string{"PGROUTER_SOURCE_DSN": "s", "PGROUTER_RESHARD_DELETE_ONLY": "1"},
			wantCode: 2,
			wantErr:  []string{"reshard-copy-poc: DELETE_ONLY requires PGROUTER_RESHARD_TARGET_SHARD\n"},
		},
		{
			name:     "missing target dsn",
			env:      map[string]string{"PGROUTER_SOURCE_DSN": "s"},
			wantCode: 2,
			wantErr:  []string{"reshard-copy-poc: PGROUTER_TARGET_DSN required (unless DELETE_ONLY)\n"},
		},
		{
			name:     "full copy without table",
			env:      map[string]string{"PGROUTER_SOURCE_DSN": "s", "PGROUTER_TARGET_DSN": "t"},
			wantCode: 2,
			wantErr:  []string{"reshard-copy-poc: full copy requires PGROUTER_COPY_TABLE\n"},
		},
		{
			name: "full copy bad timeout unreachable",
			env: map[string]string{"PGROUTER_SOURCE_DSN": unreachableDSN, "PGROUTER_TARGET_DSN": unreachableDSN,
				"PGROUTER_COPY_TABLE": "t", "RESHARD_COPY_TIMEOUT": "bogus"},
			wantCode: 1,
			wantErr: []string{
				"reshard-copy-poc: invalid RESHARD_COPY_TIMEOUT=\"bogus\" (using 15m0s)\n",
				"(copied 0 before error)\n",
			},
			wantOut: []string{"reshard-copy-poc: InitialCopy (full) table=\"t\" source→target\n"},
		},
		{
			name: "range copy unreachable",
			env: map[string]string{"PGROUTER_SOURCE_DSN": unreachableDSN, "PGROUTER_TARGET_DSN": unreachableDSN,
				"PGROUTER_RESHARD_TARGET_SHARD": "shard-1", "PGROUTER_COPY_TABLE": "orders"},
			wantCode: 1,
			wantErr:  []string{"(copied 0/0 before error)\n"},
			wantOut: []string{
				"reshard-copy-poc: InitialCopy (range) table=\"orders\" vindex=id target=shard-1 source→target\n",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runMain(t, tc.env)
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d\nstdout=%q\nstderr=%q", code, tc.wantCode, stdout, stderr)
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr %q missing %q", stderr, w)
				}
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(stdout, w) {
					t.Errorf("stdout %q missing %q", stdout, w)
				}
			}
		})
	}
}

// runMain 은 main 을 깨끗한 env 의 자식 프로세스로 돌려 종료 코드와 출력을 반환한다.
func runMain(t *testing.T, env map[string]string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = []string{runMainEnv + "=1", "PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, stdout.String(), stderr.String()
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), stdout.String(), stderr.String()
	}
	t.Fatalf("run main: %v", err)
	return 0, "", ""
}
