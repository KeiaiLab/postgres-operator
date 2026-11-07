/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// i26guard checks base..HEAD against invariant I26 (ADR-0037).
//
//	go run ./cmd/i26guard --base origin/stable
//
// rc 0 clean · 1 violation · 2 git or base unresolvable.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/keiailab/postgres-operator/internal/i26guard"
)

// Exit codes.
const (
	rcOK        = 0
	rcViolation = 1
	rcGit       = 2
)

const defaultBase = "origin/stable"

func main() {
	base := flag.String("base", defaultBase, "baseline revision; checks base..HEAD")
	dir := flag.String("C", ".", "git working tree")
	flag.Parse()

	os.Exit(run(*dir, *base, os.Stdout, os.Stderr))
}

// run checks dir's base..HEAD and returns the exit code.
func run(dir, base string, stdout, stderr io.Writer) int {
	r := repo{dir: dir}

	// Unresolvable base or unreadable git fails closed.
	if err := r.verify(base); err != nil {
		fmt.Fprintf(stderr, "i26guard: base %q: %v\n", base, err)
		return rcGit
	}
	commits, err := r.commits(base)
	if err != nil {
		fmt.Fprintf(stderr, "i26guard: %v\n", err)
		return rcGit
	}
	adrs, err := r.adrs()
	if err != nil {
		fmt.Fprintf(stderr, "i26guard: %v\n", err)
		return rcGit
	}

	vs := i26guard.Check(commits, adrs)
	for _, v := range vs {
		fmt.Fprintln(stdout, v)
	}
	fmt.Fprintf(stdout, "checked %d commits, %d violations\n", len(commits), len(vs))

	if len(vs) > 0 {
		return rcViolation
	}
	return rcOK
}
