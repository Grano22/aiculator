package work

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/sandbox"
)

var ctx = context.Background()

func repoFixture(t *testing.T) (*Manager, string) {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "root"},
	} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	sb, err := sandbox.Open("auto")
	if err != nil {
		t.Skip(err)
	}
	m, err := New(ctx, Options{Repo: repo, Root: filepath.Join(t.TempDir(), "wt"), Sandbox: sb})
	if err != nil {
		t.Fatal(err)
	}
	return m, repo
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func task(id string, attempt int, checks ...string) core.Task {
	return core.Task{ID: id, Title: id, Attempt: attempt, Checks: checks, Tier: 1, Verifier: "system:checks"}
}

func revParse(t *testing.T, m *Manager, ref string) string {
	t.Helper()
	h, err := m.git(ctx, m.repo, "rev-parse", ref)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestEvidenceChangesOnlyWhenFilesChange(t *testing.T) {
	m, _ := repoFixture(t)
	x := task("a", 1)
	dir, err := m.Prepare(ctx, x)
	if err != nil {
		t.Fatal(err)
	}
	t1, _, err := m.Snapshot(ctx, x, "")
	if err != nil {
		t.Fatal(err)
	}
	t2, _, _ := m.Snapshot(ctx, x, "")
	if t1 != t2 {
		t.Fatal("evidence changed with no file change")
	}
	write(t, dir, "f.txt", "hello")
	t3, ref, err := m.Snapshot(ctx, x, "")
	if err != nil {
		t.Fatal(err)
	}
	if t3 == t2 {
		t.Fatal("evidence did not change after a real edit")
	}
	if s, err := m.git(ctx, m.repo, "show", ref+":f.txt"); err != nil || s != "hello" {
		t.Fatalf("ref content: %q %v", s, err)
	}
}

func TestSnapshotLeavesAgentIndexAlone(t *testing.T) {
	m, _ := repoFixture(t)
	x := task("idx", 1)
	dir, _ := m.Prepare(ctx, x)
	write(t, dir, "staged.txt", "s")
	if _, err := m.git(ctx, dir, "add", "staged.txt"); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "unstaged.txt", "u")
	if _, _, err := m.Snapshot(ctx, x, ""); err != nil {
		t.Fatal(err)
	}
	st, _ := m.git(ctx, dir, "status", "--porcelain")
	if !strings.Contains(st, "A  staged.txt") || !strings.Contains(st, "?? unstaged.txt") {
		t.Fatalf("index was disturbed: %q", st)
	}
}

// The handoff gate: a new attempt starts from the old attempt's saved work.
func TestHandoffContinuesFromSavedWork(t *testing.T) {
	m, _ := repoFixture(t)
	a1 := task("h", 1)
	dir1, _ := m.Prepare(ctx, a1)
	write(t, dir1, "part1.txt", "from attempt one")
	if _, _, err := m.Snapshot(ctx, a1, "halfway"); err != nil {
		t.Fatal(err)
	}
	a2 := task("h", 2)
	dir2, err := m.Prepare(ctx, a2)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir2, "part1.txt"))
	if err != nil || string(b) != "from attempt one" {
		t.Fatalf("work lost in handoff: %q %v", b, err)
	}
}

func TestChecksIgnoreUncommittedFilesAndRejectEmpty(t *testing.T) {
	m, _ := repoFixture(t)
	x := task("c", 1, "test -f real.txt")
	dir, _ := m.Prepare(ctx, x)
	_, ref, _ := m.Snapshot(ctx, x, "")
	x.Ref = ref
	r, err := m.RunChecks(ctx, x)
	if err != nil {
		t.Fatal(err)
	}
	if r.Pass || !strings.Contains(r.Detail, "changes nothing") {
		t.Fatalf("empty submission passed: %+v", r)
	}
	write(t, dir, "real.txt", "x")
	_, x.Ref, _ = m.Snapshot(ctx, x, "")
	if r, err := m.RunChecks(ctx, x); err != nil || !r.Pass {
		t.Fatalf("real work failed: %+v %v", r, err)
	}
	// A file created after the submission must not affect the verdict.
	y := task("c2", 1, "test -f later.txt")
	dy, _ := m.Prepare(ctx, y)
	write(t, dy, "other.txt", "x")
	_, y.Ref, _ = m.Snapshot(ctx, y, "")
	write(t, dy, "later.txt", "x")
	if r, _ := m.RunChecks(ctx, y); r.Pass {
		t.Fatal("check saw a file that was not submitted")
	}
}

func TestChecksRunInsideSandbox(t *testing.T) {
	m, _ := repoFixture(t)
	if m.sb.Name() == "none" {
		t.Skip("no sandbox")
	}
	outside := filepath.Join(t.TempDir(), "escaped")
	x := task("sb", 1, "echo x > "+outside)
	dir, _ := m.Prepare(ctx, x)
	write(t, dir, "f", "x")
	_, x.Ref, _ = m.Snapshot(ctx, x, "")
	r, err := m.RunChecks(ctx, x)
	if err != nil {
		t.Fatal(err)
	}
	if r.Pass {
		t.Error("check that writes outside its checkout passed")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("check escaped the sandbox")
	}
}

func TestMergeLandsOnIntegrationAndLeavesUserBranch(t *testing.T) {
	m, _ := repoFixture(t)
	main0 := revParse(t, m, "refs/heads/main")
	x := task("m", 1, "test -f a.txt")
	dir, _ := m.Prepare(ctx, x)
	write(t, dir, "a.txt", "a")
	_, x.Ref, _ = m.Snapshot(ctx, x, "")
	merged, err := m.Merge(ctx, x)
	if err != nil {
		t.Fatal(err)
	}
	if revParse(t, m, "refs/heads/"+DefaultBranch) != merged {
		t.Fatal("integration branch did not move")
	}
	if revParse(t, m, "refs/heads/main") != main0 {
		t.Fatal("the user's branch was changed")
	}
	// Repeating, as recovery would after a crash, changes nothing.
	again, err := m.Merge(ctx, x)
	if err != nil || again != merged {
		t.Fatalf("repeat merge: %s %v", again, err)
	}
}

func TestMergeConflictIsReportedAndBranchUnmoved(t *testing.T) {
	m, _ := repoFixture(t)
	a, b := task("x1", 1), task("x2", 1)
	da, _ := m.Prepare(ctx, a)
	db, _ := m.Prepare(ctx, b)
	write(t, da, "same.txt", "AAA")
	write(t, db, "same.txt", "BBB")
	_, a.Ref, _ = m.Snapshot(ctx, a, "")
	_, b.Ref, _ = m.Snapshot(ctx, b, "")
	first, err := m.Merge(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Merge(ctx, b); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("err = %v", err)
	}
	if revParse(t, m, "refs/heads/"+DefaultBranch) != first {
		t.Fatal("failed merge moved the branch")
	}
}

// Two changes can each pass alone and still break together.
func TestMergeRerunsChecksOnCombinedResult(t *testing.T) {
	m, _ := repoFixture(t)
	a := task("g1", 1, "test -f a.txt")
	da, _ := m.Prepare(ctx, a)
	write(t, da, "a.txt", "x")
	_, a.Ref, _ = m.Snapshot(ctx, a, "")
	if _, err := m.Merge(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := task("g2", 1, "test ! -f a.txt") // Only true without the other change.
	db, _ := m.Prepare(ctx, core.Task{ID: "g2", Attempt: 1, Title: "g2", Tier: 1})
	write(t, db, "b.txt", "x")
	if _, err := m.git(ctx, db, "rm", "-q", "--cached", "--ignore-unmatch", "a.txt"); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(db, "a.txt"))
	_, b.Ref, _ = m.Snapshot(ctx, b, "")
	// g2 started after g1 landed and deleted its file, so merging is clean and passes.
	if _, err := m.Merge(ctx, b); err != nil {
		t.Fatal(err)
	}
	d := task("g4", 1, "test -f a.txt") // Fails on the current head, where a.txt is gone.
	dd, _ := m.Prepare(ctx, d)
	write(t, dd, "d.txt", "x")
	_, d.Ref, _ = m.Snapshot(ctx, d, "")
	if _, err := m.Merge(ctx, d); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("combined result should fail its checks: %v", err)
	}
}

func TestUnsafeTaskIDIsRefused(t *testing.T) {
	m, _ := repoFixture(t)
	if _, err := m.Prepare(ctx, core.Task{ID: "../../evil", Attempt: 1}); err == nil {
		t.Fatal("path traversal id accepted")
	}
}

func TestAgentWrittenHookDoesNotRun(t *testing.T) {
	m, repo := repoFixture(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(repo, ".git", "hooks", "post-merge")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	x := task("hk", 1)
	dir, _ := m.Prepare(ctx, x)
	write(t, dir, "f", "x")
	_, x.Ref, _ = m.Snapshot(ctx, x, "")
	if _, err := m.Merge(ctx, x); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repository hook ran during merge")
	}
}

// A retry after a conflict must see what landed, or it can never succeed.
func TestRetryAfterConflictCanLand(t *testing.T) {
	m, _ := repoFixture(t)
	a, b := task("r1", 1), task("r2", 1, "test -f same.txt")
	da, _ := m.Prepare(ctx, a)
	db, _ := m.Prepare(ctx, b)
	write(t, da, "same.txt", "AAA\n")
	write(t, db, "same.txt", "BBB\n")
	_, a.Ref, _ = m.Snapshot(ctx, a, "")
	_, b.Ref, _ = m.Snapshot(ctx, b, "")
	if _, err := m.Merge(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Merge(ctx, b); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("first merge: %v", err)
	}
	b2 := task("r2", 2, "test -f same.txt")
	d2, err := m.Prepare(ctx, b2)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(d2, "same.txt"))
	if !strings.Contains(string(got), "<<<<<<<") {
		t.Fatalf("the agent was not shown the conflict: %q", got)
	}
	// An agent that ignores the conflict is stopped by the checks.
	_, b2.Ref, err = m.Snapshot(ctx, b2, "")
	if err != nil {
		t.Fatal(err)
	}
	if r, err := m.RunChecks(ctx, b2); err != nil || r.Pass || !strings.Contains(r.Detail, "conflict markers") {
		t.Fatalf("markers passed the checks: %+v %v", r, err)
	}
	// An agent that resolves it lands cleanly.
	write(t, d2, "same.txt", "AAA and BBB\n")
	_, b2.Ref, _ = m.Snapshot(ctx, b2, "")
	if r, err := m.RunChecks(ctx, b2); err != nil || !r.Pass {
		t.Fatalf("resolved work failed: %+v %v", r, err)
	}
	if _, err := m.Merge(ctx, b2); err != nil {
		t.Fatalf("resolved work did not land: %v", err)
	}
	if s, _ := m.git(ctx, m.repo, "show", DefaultBranch+":same.txt"); s != "AAA and BBB" {
		t.Fatalf("branch content: %q", s)
	}
}
