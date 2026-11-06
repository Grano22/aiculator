package main

import (
	"os"
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
