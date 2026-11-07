/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

// Package i26guard judges invariant I26 (ADR-0037): every commit that
// touches the instance manager or the pg image must carry an
// "I26-ADR: NNNN" trailer naming an Accepted ADR. Pure — git reading
// lives in cmd/i26guard.
package i26guard

import (
	"fmt"
	"strings"
)

// TrailerKey is the git trailer that names the approving ADR.
const TrailerKey = "I26-ADR"

// guardedPaths are matched on whole path segments.
var guardedPaths = []string{"cmd/instance", "internal/instance", "Dockerfile.pg"}

// ADRStatus is the value of an ADR's "Status:" header line.
type ADRStatus string

// StatusAccepted is the only status that approves an I26 change.
const StatusAccepted ADRStatus = "Accepted"

// Commit is one commit in the checked range.
type Commit struct {
	SHA   string
	Paths []string
	ADRs  []string // I26-ADR trailer values
}

// Violation is one commit that breaks I26.
type Violation struct {
	SHA    string
	Path   string
	Reason string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s %s: %s", v.SHA, v.Path, v.Reason)
}

// Guarded reports whether path is under a guarded prefix.
// "cmd/instance/x" is guarded, "cmd/instancefoo/x" is not.
func Guarded(path string) bool {
	for _, p := range guardedPaths {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// Check returns the violations in commits. adrs maps ADR number
// ("0037") to its status in the checked tree.
func Check(commits []Commit, adrs map[string]ADRStatus) []Violation {
	var out []Violation
	for _, c := range commits {
		path := firstGuarded(c.Paths)
		if path == "" {
			continue
		}

		// No trailer: the change has no approving ADR at all.
		if len(c.ADRs) == 0 {
			out = append(out, Violation{c.SHA, path, "missing " + TrailerKey + " trailer"})
			continue
		}

		// Every named ADR must exist and be Accepted.
		for _, n := range c.ADRs {
			reason := adrProblem(n, adrs)
			if reason == "" {
				continue
			}
			out = append(out, Violation{c.SHA, path, reason})
		}
	}
	return out
}

func firstGuarded(paths []string) string {
	for _, p := range paths {
		if Guarded(p) {
			return p
		}
	}
	return ""
}

func adrProblem(n string, adrs map[string]ADRStatus) string {
	st, ok := adrs[n]
	if !ok {
		return fmt.Sprintf("%s: %s names no ADR in tree", TrailerKey, n)
	}
	if st != StatusAccepted {
		return fmt.Sprintf("%s: ADR %s is %q, not %s", TrailerKey, n, st, StatusAccepted)
	}
	return ""
}

// ParseStatus reads the first "Status:" header line of an ADR, in
// either form "- **Status**: Accepted" or "- Status: Accepted", and
// returns its first word. Empty when there is none.
func ParseStatus(doc string) ADRStatus {
	for _, line := range strings.Split(doc, "\n") {
		s := strings.TrimSpace(line)
		s = strings.TrimPrefix(s, "- ")
		s = strings.ReplaceAll(s, "**", "")

		rest, ok := strings.CutPrefix(s, "Status:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return ""
		}
		return ADRStatus(f[0])
	}
	return ""
}
