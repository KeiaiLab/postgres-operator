/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// rejudge 는 저장된 loopdrill 판정문을 지금 판정 함수로 다시 판정한다 — 클러스터가 필요 없다.
//
//	go run ./test/e2e/loopdrill/cmd/rejudge <in.json> <out.json>
//
// 기록(입력 · 전후 스냅숏 · 기대)은 그대로 두고 checks · result 를 새로 내며 rejudgedFrom 에 원본을 적는다.
// 바뀐 판정과 호스트 정지 관측(H1)은 표준 출력에 한 줄씩.
package main

import (
	"fmt"
	"os"

	"github.com/keiailab/postgres-operator/internal/loopdrill"
)

const usage = "사용: rejudge <in.json> <out.json>"

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "rejudge:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	re, err := loopdrill.Rejudge(raw, in)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, re.Report, 0o644); err != nil {
		return err
	}

	// 바뀐 판정 — 없으면 그렇다고 적는다
	if len(re.Flips) == 0 {
		fmt.Println("바뀐 판정 없음")
	}
	for _, f := range re.Flips {
		fmt.Println(f)
	}

	// 호스트 정지 관측(ADR-0039 H1) — 있는 판만
	for _, line := range re.Stalls {
		fmt.Println(line)
	}
	return nil
}
