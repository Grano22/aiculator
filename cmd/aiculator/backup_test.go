package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// From the backup file to a restore: a canceled critical task comes back as it was.
func TestBackupAndRestoreFromTheCLI(t *testing.T) {
	p := newProject(t)
	if err := os.MkdirAll(filepath.Join(p.root, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `dir: backups
rules:
  - name: critical
    journal: true
    every: 1h
    match:
      labels: {severity: critical}
`
	if err := os.WriteFile(filepath.Join(p.root, ".agents/backups.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	p.run("up")
	p.run("task", "add", "--id", "pay", "--title", "keep me", "--label", "severity=critical", "--allow-empty")
	p.run("task", "add", "--id", "docs", "--title", "not critical", "--allow-empty")

	var out string
	for range 200 {
		// A new task is a draft for a moment, so the journal may hold that copy first.
		if out = p.run("backup", "versions", "pay"); strings.Contains(out, "journal") && strings.Contains(out, "ready") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !strings.Contains(out, "journal") || !strings.Contains(out, "ready") {
		t.Fatalf("versions: %s", out)
	}
	if out := p.run("backup", "versions", "docs"); !strings.Contains(out, "no copies") {
		t.Errorf("an unmatched task was kept: %s", out)
	}
	snap := strings.TrimSpace(p.run("backup", "now", "critical"))
	if !strings.HasSuffix(snap, ".json.gz") || !exists(snap) {
		t.Fatalf("backup now: %q", snap)
	}
	if out := p.run("backup", "status"); !strings.Contains(out, `"rule": "critical"`) || !strings.Contains(out, `"snapshot_tasks": 1`) {
		t.Errorf("status: %s", out)
	}

	p.run("task", "cancel", "pay")
	if out := p.run("backup", "restore", "pay", "--from", snap, "--dry-run"); !strings.Contains(out, "pay: ready copy") {
		t.Errorf("dry run: %s", out)
	}
	if out := p.run("task", "show", "pay"); !strings.Contains(out, "canceled") {
		t.Fatalf("a dry run changed the task: %s", out)
	}
	if out, err := p.tryIn(p.root, "backup", "restore", "pay", "--before", "1h"); err == nil || !strings.Contains(out, "no copy of pay to restore from before") {
		t.Fatalf("nothing is that old, so this should have failed: %v %s", err, out)
	}
	if out := p.run("backup", "restore", "pay"); !strings.Contains(out, "pay: restored as ready") {
		t.Fatalf("restore: %s", out)
	}
	if out := p.run("task", "show", "pay"); !strings.Contains(out, "ready") || !strings.Contains(out, "keep me") {
		t.Errorf("after restore: %s", out)
	}
}

// Joining with auto picks the tier from the model and effort, says why on stderr, and keeps
// stdout to the agent name for scripts.
func TestWorkJoinAutoTier(t *testing.T) {
	p := newProject(t)
	p.run("up")
	name, errOut, err := p.runSplit("work", "join", "--tier", "auto", "--model", "claude-opus-5-5")
	if err != nil {
		t.Fatal(err, errOut)
	}
	name = strings.TrimSpace(name)
	if strings.Contains(name, " ") || !strings.Contains(errOut, "tier 3 (ultra)") {
		t.Fatalf("stdout %q stderr %q", name, errOut)
	}
	low, errOut, err := p.runSplit("work", "join", "--model", "claude-haiku-5-5", "--effort", "low")
	if err != nil || !strings.Contains(errOut, "tier 1") {
		t.Fatalf("haiku at low effort: %v %q", err, errOut)
	}
	out := p.run("sessions")
	for _, want := range []string{strings.TrimSpace(name), strings.TrimSpace(low), "shell"} {
		if !strings.Contains(out, want) {
			t.Errorf("sessions lack %q:\n%s", want, out)
		}
	}
	if _, _, err := p.runSplit("work", "join", "--tier", "4"); err == nil {
		t.Error("tier 4 was accepted")
	}
}

// The operator commands from end to end: usage and limits from a worker, estimates,
// sessions, stopping and killing agents, and stopping every realm.
func TestOperatorCommands(t *testing.T) {
	p := newProject(t)
	p.run("up")
	p.run("task", "add", "--id", "op1", "--title", "first job", "--allow-empty")
	p.run("task", "add", "--id", "op2", "--title", "second job", "--allow-empty")
	name := strings.TrimSpace(p.run("work", "join", "--tier", "1", "--harness", "claude"))
	out := p.run("work", "wait", "--agent", name, "--hold", "0s")
	var g struct {
		Task  struct{ ID string } `json:"task"`
		Lease uint64              `json:"lease"`
	}
	if err := json.Unmarshal([]byte(out), &g); err != nil || g.Task.ID == "" {
		t.Fatalf("wait: %v %s", err, out)
	}
	lease := fmt.Sprint(g.Lease)
	p.run("work", "usage", "--agent", name, "--task", g.Task.ID, "--lease", lease, "--tokens", "1500", "--cost", "20")
	p.run("work", "usage", "--agent", name, "--tokens", "100")
	if out := p.run("work", "estimate", "--agent", name, "--task", g.Task.ID); !strings.Contains(out, `"method": "none"`) {
		t.Errorf("work estimate: %s", out)
	}
	if out := p.run("task", "estimate", g.Task.ID); !strings.Contains(out, "nothing to go on") {
		t.Errorf("task estimate: %s", out)
	}
	if out := p.run("task", "estimate", "--title", "a new job", "--json"); !strings.Contains(out, `"method"`) {
		t.Errorf("estimate of a new task: %s", out)
	}
	if _, err := p.tryIn(p.root, "task", "estimate"); err == nil {
		t.Error("estimate with nothing to estimate worked")
	}
	if out := p.run("work", "limit", "--agent", name, "--reason", "credit_exhausted", "--retry-after", "1h"); !strings.Contains(out, g.Task.ID) {
		t.Errorf("limit did not hand back the task: %s", out)
	}
	if out := p.run("sessions"); !strings.Contains(out, "limited (credit_exhausted until") || !strings.Contains(out, "claude") {
		t.Errorf("sessions: %s", out)
	}
	if out := p.run("sessions", "--json"); !strings.Contains(out, `"kind": "shell"`) {
		t.Errorf("sessions json: %s", out)
	}
	if out := p.run("agents", "stop", "--all"); !strings.Contains(out, "stopped 1") {
		t.Errorf("stop: %s", out)
	}
	if out := p.run("agents", "kill", "--all"); !strings.Contains(out, "killed 0") {
		t.Errorf("kill: %s", out)
	}
	if out := p.run("agents", "stop", "--all", "--realms"); !strings.Contains(out, "stopped") {
		t.Errorf("stop across realms: %s", out)
	}
	if _, err := p.tryIn(p.root, "agents", "stop"); err == nil {
		t.Error("stop with no agents named worked")
	}
	if out := p.run("sessions", "forget", name); !strings.Contains(out, "removed 1") {
		t.Errorf("forget: %s", out)
	}
	if out := p.run("sessions", "forget"); !strings.Contains(out, "removed 0") {
		t.Errorf("forget gone: %s", out)
	}
	// Events from the start of the log: the stream runs until it is stopped, so give it a
	// moment and then end it.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ev := exec.CommandContext(ctx, p.bin, "events", "--since", "1")
	ev.Dir = p.root
	ev.Env = append(os.Environ(), "HOME="+p.home, "AICULATOR_REALMS="+filepath.Join(p.home, "realms"))
	evOut, _ := ev.Output()
	if !strings.Contains(string(evOut), `"seq":2`) {
		t.Errorf("events --since 1: %.300s", evOut)
	}
	if _, err := p.tryIn(p.root, "realm", "stop"); err == nil {
		t.Error("realm stop with nothing named worked")
	}
	if _, err := p.tryIn(p.root, "realm", "stop", "no-such-realm"); err == nil {
		t.Error("stopping a realm that does not exist worked")
	}
	if out := p.run("down", "--all"); !strings.Contains(out, "stopped") {
		t.Errorf("down --all: %s", out)
	}
	if out := p.run("down", "--all"); !strings.Contains(out, "not running") {
		t.Errorf("down --all again: %s", out)
	}
}
