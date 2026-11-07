/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package i26guard

import (
	"os"
	"strings"
	"testing"
)

// adrs is the ADR state most cases run against.
var adrs = map[string]ADRStatus{
	"0037": StatusAccepted,
	"0038": "Proposed",
}

func commit(paths []string, trailers ...string) Commit {
	return Commit{SHA: "abcdef0123456789", Paths: paths, ADRs: trailers}
}

func TestMissingTrailerFails(t *testing.T) {
	got := Check([]Commit{commit([]string{"cmd/instance/main.go"})}, adrs)
	if len(got) != 1 || !strings.Contains(got[0].Reason, TrailerKey) {
		t.Fatalf("want one missing-trailer violation, got %v", got)
	}
}

func TestTrailerToMissingADRFails(t *testing.T) {
	got := Check([]Commit{commit([]string{"internal/instance/x.go"}, "0999")}, adrs)
	if len(got) != 1 || !strings.Contains(got[0].Reason, "0999 names no ADR") {
		t.Fatalf("want one missing-ADR violation, got %v", got)
	}
}

func TestTrailerToProposedADRFails(t *testing.T) {
	got := Check([]Commit{commit([]string{"Dockerfile.pg"}, "0038")}, adrs)
	if len(got) != 1 || !strings.Contains(got[0].Reason, "Proposed") {
		t.Fatalf("want one not-accepted violation, got %v", got)
	}
}

func TestAcceptedTrailerPasses(t *testing.T) {
	got := Check([]Commit{commit([]string{"cmd/instance/main.go"}, "0037")}, adrs)
	if len(got) != 0 {
		t.Fatalf("want no violation, got %v", got)
	}
}

func TestUnguardedCommitPasses(t *testing.T) {
	got := Check([]Commit{commit([]string{"internal/controller/a.go", "Dockerfile"})}, adrs)
	if len(got) != 0 {
		t.Fatalf("want no violation, got %v", got)
	}
}

func TestSegmentPrefixOnly(t *testing.T) {
	// Sibling names that share a string prefix are not guarded.
	cases := map[string]bool{
		"cmd/instance":             true,
		"cmd/instance/main.go":     true,
		"internal/instance/a/b.go": true,
		"Dockerfile.pg":            true,
		"cmd/instancefoo/main.go":  false,
		"internal/instances/a.go":  false,
		"Dockerfile.pgbouncer":     false,
		"Dockerfile.pg/x":          true,
		"x/cmd/instance/main.go":   false,
	}
	for path, want := range cases {
		if got := Guarded(path); got != want {
			t.Errorf("Guarded(%q) = %v, want %v", path, got, want)
		}
	}

	got := Check([]Commit{commit([]string{"cmd/instancefoo/main.go"})}, adrs)
	if len(got) != 0 {
		t.Fatalf("cmd/instancefoo must not be guarded, got %v", got)
	}
}

func TestParseStatus(t *testing.T) {
	cases := map[string]ADRStatus{
		"# ADR\n\n- **Status**: Accepted\n":            StatusAccepted,
		"# ADR\n\n- Status: Accepted\n":                StatusAccepted,
		"# ADR\n\n- **Status**: Proposed\n":            "Proposed",
		"# ADR\n\n- Status: Superseded by ADR-0002\n":  "Superseded",
		"# ADR\n\nno header\n\n## Status\n\nAccepted.": "",
	}
	for doc, want := range cases {
		if got := ParseStatus(doc); got != want {
			t.Errorf("ParseStatus(%q) = %q, want %q", doc, got, want)
		}
	}
}

// TestSelfCheckKnownBad runs the guard over a fixture holding one
// commit of each bad kind plus one good; every bad one must be caught.
func TestSelfCheckKnownBad(t *testing.T) {
	raw, err := os.ReadFile("testdata/adr-proposed.md")
	if err != nil {
		t.Fatal(err)
	}
	fixtureADRs := map[string]ADRStatus{
		"0037": StatusAccepted,
		"0038": ParseStatus(string(raw)),
	}

	commits := []Commit{
		{SHA: "bad1", Paths: []string{"cmd/instance/main.go"}},
		{SHA: "bad2", Paths: []string{"internal/instance/a.go"}, ADRs: []string{"0999"}},
		{SHA: "bad3", Paths: []string{"Dockerfile.pg"}, ADRs: []string{"0038"}},
		{SHA: "good", Paths: []string{"cmd/instance/main.go"}, ADRs: []string{"0037"}},
		{SHA: "free", Paths: []string{"cmd/instancefoo/main.go"}},
	}
	got := Check(commits, fixtureADRs)

	caught := map[string]bool{}
	for _, v := range got {
		caught[v.SHA] = true
	}
	for _, sha := range []string{"bad1", "bad2", "bad3"} {
		if !caught[sha] {
			t.Errorf("known-bad %s not caught: %v", sha, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("want 3 violations, got %v", got)
	}
}
