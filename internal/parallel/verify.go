package parallel

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func git(dir string, env []string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Snapshot records the state of the working tree, including uncommitted and new files, and
// returns a name for it. It uses a private index, so the real staging area is left untouched,
// and it makes no commit and moves no branch.
func Snapshot(dir string) (string, error) {
	idx, err := git(dir, nil, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "aiculator-index-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	tmp.Close()
	defer os.Remove(name)
	defer os.Remove(name + ".lock")
	// Starting from the real index means only changed files have to be read again.
	if b, err := os.ReadFile(idx); err == nil {
		if err := os.WriteFile(name, b, 0o600); err != nil {
			return "", err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		os.Remove(name)
	} else {
		return "", err
	}
	env := []string{"GIT_INDEX_FILE=" + name}
	if _, err := git(dir, env, "add", "-A"); err != nil {
		return "", err
	}
	return git(dir, env, "write-tree")
}

// Report says what a wave really did.
type Report struct {
	OK bool `json:"ok"`
	// Changed lists every file that differs since the snapshot.
	Changed []string `json:"changed"`
	// Unclaimed lists files no job in this wave was allowed to change.
	Unclaimed []string `json:"unclaimed,omitempty"`
	// Idle lists jobs that changed nothing, which may mean a subagent did not do its work.
	Idle []string `json:"idle,omitempty"`
	// Note says what this check cannot see.
	Note string `json:"note"`
}

const verifyNote = "Checked per file, so it cannot tell which job in a wave changed a file, or whether a job stayed inside a symbol or line range. Files ignored by git are not seen."

// Verify compares the working tree with a snapshot taken before a wave began, and reports any
// change that the wave's jobs did not claim.
func Verify(dir string, plan *Plan, wave int, since string) (*Report, error) {
	if wave < 1 || wave > len(plan.Waves) {
		return nil, fmt.Errorf("the plan has waves 1 to %d, not %d", len(plan.Waves), wave)
	}
	now, err := Snapshot(dir)
	if err != nil {
		return nil, err
	}
	out, err := git(dir, nil, "diff-tree", "-r", "--name-only", "--no-renames", since, now)
	if err != nil {
		return nil, fmt.Errorf("comparing with snapshot %s: %w", since, err)
	}
	r := &Report{OK: true, Note: verifyNote, Changed: []string{}}
	if out != "" {
		r.Changed = strings.Split(out, "\n")
	}
	type claimOf struct {
		job string
		c   claim
	}
	var claims []claimOf
	jobs := plan.Waves[wave-1].Jobs
	for _, id := range jobs {
		for _, s := range plan.Writes[id] {
			c, err := parseClaim(s)
			if err != nil {
				return nil, err
			}
			claims = append(claims, claimOf{id, c})
		}
	}
	busy := map[string]bool{}
	for _, f := range r.Changed {
		lower := strings.ToLower(filepath.ToSlash(f))
		claimed := false
		for _, c := range claims {
			if pathsOverlap(c.c.path, lower) && (c.c.path == lower || c.c.isDir || c.c.path == ".") {
				claimed = true
				busy[c.job] = true
			}
		}
		if !claimed {
			r.Unclaimed = append(r.Unclaimed, f)
		}
	}
	for _, id := range jobs {
		if !busy[id] {
			r.Idle = append(r.Idle, id)
		}
	}
	slices.Sort(r.Idle)
	r.OK = len(r.Unclaimed) == 0
	return r, nil
}
