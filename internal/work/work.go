// Package work isolates each task in its own git worktree, saves work-in-progress
// commits under refs/swarm/, runs acceptance checks in a clean checkout, and merges
// verified work one at a time. It calls the git command itself, and it never
// touches the user's own branches: finished work lands on an integration branch.
package work

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/sandbox"
)

// DefaultBranch is where merged work lands.
const DefaultBranch = "swarm/integration"

// Manager owns the worktrees for one repository.
type Manager struct {
	repo         string
	root         string
	branch       string
	sb           sandbox.Sandbox
	checkTimeout time.Duration
	hide         []string
	mergeMu      sync.Mutex
	gitMu        sync.Mutex // Serializes commands that change worktree metadata.
}

// Options configures a Manager.
type Options struct {
	Repo         string
	Root         string // Directory for worktrees.
	Branch       string
	Sandbox      sandbox.Sandbox
	CheckTimeout time.Duration
	Hide         []string // Paths checks may not read, such as the swarm's secrets.
}

// New prepares the repository and creates the integration branch if needed.
func New(ctx context.Context, o Options) (*Manager, error) {
	if o.Branch == "" {
		o.Branch = DefaultBranch
	}
	if o.CheckTimeout == 0 {
		o.CheckTimeout = 10 * time.Minute
	}
	if o.Sandbox == nil {
		return nil, errors.New("work: a sandbox is required")
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	m := &Manager{repo: o.Repo, root: root, branch: o.Branch, sb: o.Sandbox, checkTimeout: o.CheckTimeout, hide: o.Hide}
	if _, err := m.git(ctx, m.repo, "rev-parse", "--verify", "HEAD"); err != nil {
		return nil, fmt.Errorf("repository needs at least one commit: %w", err)
	}
	if _, err := m.git(ctx, m.repo, "rev-parse", "--verify", "refs/heads/"+m.branch); err != nil {
		if _, err := m.git(ctx, m.repo, "branch", m.branch, "HEAD"); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// git runs one git command with a controlled environment. Repository hooks are
// disabled, so a task's commits cannot execute code from an agent-written hook.
func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	return m.gitEnv(ctx, dir, nil, args...)
}

func (m *Manager) gitEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	full := append([]string{
		"-c", "user.name=swarm", "-c", "user.email=swarm@localhost",
		"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(errb.String() + "\n" + out.String())
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, detail)
	}
	return strings.TrimSpace(out.String()), nil
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (m *Manager) path(kind, id string, attempt int) (string, error) {
	if !safeID.MatchString(id) {
		return "", fmt.Errorf("unsafe task id %q", id)
	}
	return filepath.Join(m.root, fmt.Sprintf("%s-%s-%d", kind, id, attempt)), nil
}

// LatestRef is the ref holding a task's newest saved work, across all attempts.
func LatestRef(id string) string { return "refs/swarm/" + id + "/latest" }

// Prepare creates the worktree for an attempt. If an earlier attempt saved work,
// the new one starts from it: that is what makes handoff and escalation lose
// nothing. Calling it twice returns the same directory.
func (m *Manager) Prepare(ctx context.Context, t core.Task) (string, error) {
	dir, err := m.path("wt", t.ID, t.Attempt)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, nil
	}
	m.salvage(ctx, t)
	branchRef := "refs/heads/" + m.branch
	start, resumed := branchRef, false
	if _, err := m.git(ctx, m.repo, "rev-parse", "--verify", LatestRef(t.ID)); err == nil {
		start, resumed = LatestRef(t.ID), true
	}
	m.gitMu.Lock()
	_, _ = m.git(ctx, m.repo, "worktree", "prune")
	_, err = m.git(ctx, m.repo, "worktree", "add", "--detach", "--force", dir, start)
	m.gitMu.Unlock()
	if err != nil {
		return "", err
	}
	if resumed {
		// Bring in what landed since the earlier attempt. Otherwise work that hit a
		// merge conflict would restart from the same stale base and conflict again.
		// A real conflict is left in the files for this attempt's agent to resolve.
		_, _ = m.git(ctx, dir, "merge", "--no-edit", "-m", "swarm: update from integration", branchRef)
	}
	return dir, nil
}

// salvage saves whatever earlier attempts left in their worktrees, including work
// that was never reported. A crashed or zombie agent therefore loses nothing.
func (m *Manager) salvage(ctx context.Context, t core.Task) {
	for a := 1; a < t.Attempt; a++ {
		prev := t.Clone()
		prev.Attempt = a
		if _, _, err := m.Snapshot(ctx, prev, "salvaged by the swarm"); err == nil {
			continue
		}
	}
}

// PrepareReview creates a clean checkout of submitted work for a reviewer.
func (m *Manager) PrepareReview(ctx context.Context, t core.Task) (string, error) {
	dir, err := m.path("review", t.ID, int(t.LeaseEpoch))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, nil
	}
	if t.Ref == "" {
		return "", fmt.Errorf("task %s has no submitted ref", t.ID)
	}
	m.gitMu.Lock()
	defer m.gitMu.Unlock()
	if _, err := m.git(ctx, m.repo, "worktree", "add", "--detach", "--force", dir, t.Ref); err != nil {
		return "", err
	}
	return dir, nil
}

// Snapshot saves the worktree as a commit without disturbing the agent: it uses a
// private index, so whatever the agent has staged stays as it was. It returns the
// tree hash, which is the evidence of progress. An agent cannot fake this, because
// the swarm computes it from the actual files.
func (m *Manager) Snapshot(ctx context.Context, t core.Task, note string) (tree, ref string, err error) {
	dir, err := m.path("wt", t.ID, t.Attempt)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(dir); err != nil {
		return "", "", fmt.Errorf("worktree for %s: %w", t.ID, err)
	}
	idx, err := os.CreateTemp(m.root, "index-*")
	if err != nil {
		return "", "", err
	}
	idxPath := idx.Name()
	idx.Close()
	os.Remove(idxPath) // Git must create this itself.
	defer os.Remove(idxPath)
	env := []string{"GIT_INDEX_FILE=" + idxPath}
	if _, err := m.gitEnv(ctx, dir, env, "add", "-A"); err != nil {
		return "", "", err
	}
	if tree, err = m.gitEnv(ctx, dir, env, "write-tree"); err != nil {
		return "", "", err
	}
	parent, err := m.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	attemptRef := fmt.Sprintf("refs/swarm/%s/%d", t.ID, t.Attempt)
	parents := []string{parent}
	if prev, err := m.git(ctx, m.repo, "rev-parse", "--verify", attemptRef); err == nil {
		parent = prev
		parents = []string{prev}
	} else if mh, err := m.git(ctx, dir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		// Recording the merge as a parent is what lets this work land cleanly later.
		parents = append(parents, mh)
	}
	if parentTree, err := m.git(ctx, m.repo, "rev-parse", parent+"^{tree}"); err == nil && parentTree == tree && len(parents) == 1 {
		// Nothing new. Still point the refs at it, so the ref is always usable.
		if _, err := m.git(ctx, m.repo, "update-ref", attemptRef, parent); err != nil {
			return "", "", err
		}
		if _, err := m.git(ctx, m.repo, "update-ref", LatestRef(t.ID), parent); err != nil {
			return "", "", err
		}
		return tree, attemptRef, nil
	}
	if note == "" {
		note = "work in progress"
	}
	msg := fmt.Sprintf("swarm wip: %s attempt %d\n\n%s", t.ID, t.Attempt, note)
	args := []string{"commit-tree", tree, "-m", msg}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	commit, err := m.git(ctx, m.repo, args...)
	if err != nil {
		return "", "", err
	}
	for _, r := range []string{attemptRef, LatestRef(t.ID)} {
		if _, err := m.git(ctx, m.repo, "update-ref", r, commit); err != nil {
			return "", "", err
		}
	}
	return tree, attemptRef, nil
}

// CheckResult is the outcome of the machine checks.
type CheckResult struct {
	Pass   bool
	Detail string
}

const maxDetail = 4000

func tail(b []byte) string {
	if len(b) > maxDetail {
		b = b[len(b)-maxDetail:]
	}
	return string(b)
}

// RunChecks tests the submitted commit in a fresh checkout, inside the sandbox.
// Files the agent left outside the commit cannot influence the result, and an
// agent saying "done" has no effect on it at all.
func (m *Manager) RunChecks(ctx context.Context, t core.Task) (CheckResult, error) {
	if t.Ref == "" {
		return CheckResult{Detail: "no work was submitted"}, nil
	}
	if !t.AllowEmpty {
		same, err := m.sameTree(ctx, t.Ref, "refs/heads/"+m.branch)
		if err != nil {
			return CheckResult{}, err
		}
		if same {
			return CheckResult{Detail: "submission changes nothing compared to the base"}, nil
		}
	}
	if files := m.newConflictMarkers(ctx, t.Ref); files != "" {
		return CheckResult{Detail: "unresolved conflict markers in: " + files}, nil
	}
	dir, err := m.path("check", t.ID, t.Attempt)
	if err != nil {
		return CheckResult{}, err
	}
	defer m.removeWorktree(context.WithoutCancel(ctx), dir)
	m.removeWorktree(ctx, dir)
	m.gitMu.Lock()
	_, err = m.git(ctx, m.repo, "worktree", "add", "--detach", "--force", dir, t.Ref)
	m.gitMu.Unlock()
	if err != nil {
		return CheckResult{}, err
	}
	return m.runIn(ctx, dir, t.Checks), nil
}

// newConflictMarkers lists files holding conflict markers that the integration
// branch does not already have, so that a file which legitimately documents
// markers is not blamed on this task.
func (m *Manager) newConflictMarkers(ctx context.Context, ref string) string {
	grep := func(rev string) []string {
		out, err := m.git(ctx, m.repo, "grep", "-lE", "^(<<<<<<<|>>>>>>>) ", rev)
		if err != nil || out == "" {
			return nil
		}
		var files []string
		for _, l := range strings.Split(out, "\n") {
			files = append(files, strings.TrimPrefix(l, rev+":"))
		}
		return files
	}
	known := grep("refs/heads/" + m.branch)
	var fresh []string
	for _, f := range grep(ref) {
		if !slices.Contains(known, f) {
			fresh = append(fresh, f)
		}
	}
	return strings.Join(fresh, ", ")
}

func (m *Manager) sameTree(ctx context.Context, a, b string) (bool, error) {
	ta, err := m.git(ctx, m.repo, "rev-parse", a+"^{tree}")
	if err != nil {
		return false, err
	}
	tb, err := m.git(ctx, m.repo, "rev-parse", b+"^{tree}")
	if err != nil {
		return false, err
	}
	return ta == tb, nil
}

func (m *Manager) runIn(ctx context.Context, dir string, checks []string) CheckResult {
	for _, check := range checks {
		cctx, cancel := context.WithTimeout(ctx, m.checkTimeout)
		home := filepath.Join(dir, ".swarm-home")
		_ = os.MkdirAll(filepath.Join(home, ".tmp"), 0o755)
		cmd, err := m.sb.Command(cctx, sandbox.Spec{
			Dir: dir, Argv: []string{"sh", "-c", check}, Env: sandbox.BaseEnv(home), Hide: m.hide,
		})
		if err != nil {
			cancel()
			return CheckResult{Detail: fmt.Sprintf("%s: %v", check, err)}
		}
		out, err := cmd.CombinedOutput()
		cancel()
		_ = os.RemoveAll(home)
		if err != nil {
			return CheckResult{Detail: fmt.Sprintf("check failed: %s: %v\n%s", check, err, tail(out))}
		}
	}
	return CheckResult{Pass: true, Detail: fmt.Sprintf("%d checks passed", len(checks))}
}

// ErrMergeConflict means the work no longer applies on top of the integration branch.
var ErrMergeConflict = errors.New("merge conflict")

// Merge lands a verified task on the integration branch, one task at a time. It
// re-runs the checks on the merged result, because each change can pass alone
// and still fail combined. It is safe to repeat: work that has already landed is
// recognized, which covers a crash between the merge and recording it.
func (m *Manager) Merge(ctx context.Context, t core.Task) (string, error) {
	m.mergeMu.Lock()
	defer m.mergeMu.Unlock()
	branchRef := "refs/heads/" + m.branch
	if _, err := m.git(ctx, m.repo, "merge-base", "--is-ancestor", t.Ref, branchRef); err == nil {
		return m.git(ctx, m.repo, "rev-parse", branchRef)
	}
	head, err := m.git(ctx, m.repo, "rev-parse", branchRef)
	if err != nil {
		return "", err
	}
	dir, err := m.path("merge", t.ID, t.Attempt)
	if err != nil {
		return "", err
	}
	defer m.removeWorktree(context.WithoutCancel(ctx), dir)
	m.removeWorktree(ctx, dir)
	m.gitMu.Lock()
	_, err = m.git(ctx, m.repo, "worktree", "add", "--detach", "--force", dir, head)
	m.gitMu.Unlock()
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("swarm: %s\n\ntask %s attempt %d, verified by %s", t.Title, t.ID, t.Attempt, t.Verifier)
	if _, err := m.git(ctx, dir, "merge", "--no-ff", "--no-edit", "-m", msg, t.Ref); err != nil {
		return "", fmt.Errorf("%w: %v", ErrMergeConflict, err)
	}
	if r := m.runIn(ctx, dir, t.Checks); !r.Pass {
		return "", fmt.Errorf("%w: merged result fails: %s", ErrMergeConflict, r.Detail)
	}
	merged, err := m.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	// Compare-and-swap on the branch, so nothing else's commit can be overwritten.
	if _, err := m.git(ctx, m.repo, "update-ref", branchRef, merged, head); err != nil {
		return "", err
	}
	return merged, nil
}

func (m *Manager) removeWorktree(ctx context.Context, dir string) {
	m.gitMu.Lock()
	defer m.gitMu.Unlock()
	_, _ = m.git(ctx, m.repo, "worktree", "remove", "--force", dir)
	_ = os.RemoveAll(dir)
}

// Cleanup removes a finished task's worktrees. Its refs stay, as the audit trail.
func (m *Manager) Cleanup(ctx context.Context, id string) {
	if !safeID.MatchString(id) {
		return
	}
	for _, kind := range []string{"wt", "review", "check", "merge"} {
		matches, _ := filepath.Glob(filepath.Join(m.root, kind+"-"+id+"-*"))
		for _, d := range matches {
			m.removeWorktree(ctx, d)
		}
	}
}

// Probe checks that the repository and integration branch are readable.
func (m *Manager) Probe(ctx context.Context) error {
	_, err := m.git(ctx, m.repo, "rev-parse", "--verify", "refs/heads/"+m.branch)
	return err
}

// SandboxName says which sandbox checks run in.
func (m *Manager) SandboxName() string { return m.sb.Name() }

// Branch returns the integration branch name.
func (m *Manager) Branch() string { return m.branch }
