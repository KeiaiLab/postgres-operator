/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo builds a temp repo whose first commit (tagged "base") holds
// an Accepted ADR-0037.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	sh(t, dir, "init", "-q")
	sh(t, dir, "config", "user.email", "t@example.com")
	sh(t, dir, "config", "user.name", "t")
	write(t, dir, "docs/kb/adr/0037-x.md", "# ADR-0037\n\n- **Status**: Accepted\n")
	sh(t, dir, "add", "-A")
	sh(t, dir, "commit", "-q", "-m", "Base")
	sh(t, dir, "tag", "base")
	return dir
}

func sh(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runIn(dir, base string) (int, string) {
	var out, errb bytes.Buffer
	rc := run(dir, base, &out, &errb)
	return rc, out.String() + errb.String()
}

// TestGitTwoCommits: a trailered guarded commit passes, an
// untrailered one is reported with rc 1.
func TestGitTwoCommits(t *testing.T) {
	dir := gitRepo(t)

	write(t, dir, "cmd/instance/main.go", "package main\n")
	sh(t, dir, "add", "-A")
	sh(t, dir, "commit", "-q", "-m", "Good\n\nI26-ADR: 0037")
	if rc, out := runIn(dir, "base"); rc != rcOK {
		t.Fatalf("trailered commit: rc %d\n%s", rc, out)
	}

	write(t, dir, "internal/instance/a.go", "package instance\n")
	sh(t, dir, "add", "-A")
	sh(t, dir, "commit", "-q", "-m", "Bad")
	rc, out := runIn(dir, "base")
	if rc != rcViolation || !strings.Contains(out, "internal/instance/a.go") ||
		!strings.Contains(out, "checked 2 commits") {
		t.Fatalf("untrailered commit: rc %d\n%s", rc, out)
	}
}

func TestGitEmptyRange(t *testing.T) {
	dir := gitRepo(t)
	rc, out := runIn(dir, "base")
	if rc != rcOK || !strings.Contains(out, "checked 0 commits") {
		t.Fatalf("rc %d\n%s", rc, out)
	}
}

func TestGitBadBase(t *testing.T) {
	dir := gitRepo(t)
	if rc, out := runIn(dir, "no-such-ref"); rc != rcGit {
		t.Fatalf("rc %d\n%s", rc, out)
	}
	if rc, out := runIn(t.TempDir(), "base"); rc != rcGit {
		t.Fatalf("non-repo: rc %d\n%s", rc, out)
	}
}
