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
		if out = p.run("backup", "versions", "pay"); strings.Contains(out, "journal") {
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
