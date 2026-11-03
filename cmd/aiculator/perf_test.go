//go:build !race

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPerfBinaryStartup(t *testing.T) {
	if testing.Short() {
		t.Skip("performance test")
	}
	bin := buildBinary(t)
	var best time.Duration
	for range 5 {
		start := time.Now()
		if out, err := exec.Command(bin, "--version").CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		if d := time.Since(start); best == 0 || d < best {
			best = d
		}
	}
	t.Logf("--version: %v", best.Round(time.Millisecond))
	if best > 200*time.Millisecond {
		t.Errorf("the CLI took %v to start, over the 200 ms budget", best)
	}
}

func TestPerfServerStartup(t *testing.T) {
	if testing.Short() {
		t.Skip("performance test")
	}
	bin := buildBinary(t)
	data := filepath.Join(t.TempDir(), "data")
	var best time.Duration
	for range 3 {
		addr := freeAddr(t)
		start := time.Now()
		d := startDaemon(t, bin, data, addr)
		took := time.Since(start)
		d.kill9()
		if best == 0 || took < best {
			best = took
		}
	}
	t.Logf("server ready: %v", best.Round(time.Millisecond))
	if best > 3*time.Second {
		t.Errorf("the server took %v to be ready, over the 3 s budget", best)
	}
}

func TestPerfSkillAndTemplateCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("performance test")
	}
	p := newProject(t)
	for name, args := range map[string][]string{
		"skill install":    {"skill", "--install", "*", "--harness", "all"},
		"template render":  {"template", "--render", "agent-discipline"},
		"template install": {"template", "--install", "*", "--harness", "all"},
		"mcp print":        {"mcp", "print", "--harness", "all"},
	} {
		start := time.Now()
		p.run(args...)
		took := time.Since(start)
		t.Logf("%-18s %v", name, took.Round(time.Millisecond))
		if took > 2*time.Second {
			t.Errorf("%s took %v, over the 2 s budget", name, took)
		}
	}
	if !strings.Contains(p.read(".agents/skills/agent-discipline/SKILL.md"), "Contents") {
		t.Error("the template did not render")
	}
}
