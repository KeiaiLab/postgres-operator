/*
Copyright 2026 keiailab.

Licensed under the MIT License. See the LICENSE file for details.
*/

package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/keiailab/postgres-operator/internal/i26guard"
)

// adrDir holds the ADRs that I26-ADR trailers name.
const adrDir = "docs/kb/adr"

// adrFile matches "0037-some-title.md" and captures the number.
var adrFile = regexp.MustCompile(`^(\d{4})-.*\.md$`)

// repo reads commits and ADRs from one git working tree.
type repo struct {
	dir string
}

func (r repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// verify fails when rev does not name a commit.
func (r repo) verify(rev string) error {
	_, err := r.git("rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return err
}

// commits returns base..HEAD oldest first, with changed paths and
// I26-ADR trailer values.
func (r repo) commits(base string) ([]i26guard.Commit, error) {
	out, err := r.git("rev-list", "--reverse", base+"..HEAD")
	if err != nil {
		return nil, err
	}

	var cs []i26guard.Commit
	for _, sha := range strings.Fields(out) {
		c, err := r.commit(sha)
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return cs, nil
}

func (r repo) commit(sha string) (i26guard.Commit, error) {
	// Renames show as delete + add, so both old and new paths count.
	paths, err := r.git("diff-tree", "--no-commit-id", "-r", "--name-only", "--no-renames",
		"--root", "-m", "--first-parent", sha)
	if err != nil {
		return i26guard.Commit{}, err
	}

	trailers, err := r.git("log", "-1",
		"--format=%(trailers:key="+i26guard.TrailerKey+",valueonly,unfold)", sha)
	if err != nil {
		return i26guard.Commit{}, err
	}

	return i26guard.Commit{
		SHA:   sha,
		Paths: strings.Fields(paths),
		ADRs:  strings.Fields(trailers),
	}, nil
}

// adrs returns the status of every ADR in the HEAD tree.
func (r repo) adrs() (map[string]i26guard.ADRStatus, error) {
	out, err := r.git("ls-tree", "--name-only", "HEAD", adrDir+"/")
	if err != nil {
		return nil, err
	}

	m := map[string]i26guard.ADRStatus{}
	for _, path := range strings.Fields(out) {
		name := strings.TrimPrefix(path, adrDir+"/")
		mt := adrFile.FindStringSubmatch(name)
		if mt == nil {
			continue
		}

		doc, err := r.git("show", "HEAD:"+path)
		if err != nil {
			return nil, err
		}
		m[mt[1]] = i26guard.ParseStatus(doc)
	}
	return m, nil
}
