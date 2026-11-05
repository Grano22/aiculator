//go:build !windows

package main

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/client"
)

func health(t *testing.T, url string) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var h map[string]string
	if err := client.New(url, "").Get(ctx, "/v1/healthz", &h); err != nil {
		return nil
	}
	return h
}

// The standby must take over when the leader dies without warning, keep its tasks and its
// address, and move the term on. down then stops both, the standby first.
func TestStandbyTakesOverAfterKill9(t *testing.T) {
	if testing.Short() {
		t.Skip("starts servers")
	}
	p := newProject(t)
	out := p.run("up", "--standby")
	url := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	if !strings.Contains(out, "standby ") {
		t.Fatalf("up --standby: %s", out)
	}
	swarm := filepath.Join(p.root, ".swarm")
	if h := health(t, url); h["term"] != "1" {
		t.Fatalf("first leader: %v", h)
	}
	// A second up --standby finds the one already waiting.
	if again := p.run("up", "--standby"); again != out {
		t.Errorf("second up --standby: %q, want %q", again, out)
	}
	p.run("task", "add", "--id", "kept", "--title", "survives the failover", "--allow-empty")

	leader := pidFile(filepath.Join(swarm, "pid"))
	standby := pidFile(filepath.Join(swarm, "standby.pid"))
	if err := syscall.Kill(leader, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for time.Since(start) < 20*time.Second {
		if h := health(t, url); h["term"] == "2" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if h := health(t, url); h["term"] != "2" {
		t.Fatalf("no takeover within 20s: %v\n%s", h, p.read(".swarm/server.log"))
	}
	t.Logf("takeover in %v", time.Since(start).Round(time.Millisecond))
	if got := pidFile(filepath.Join(swarm, "pid")); got != standby {
		t.Errorf("leader is %d, want the standby %d", got, standby)
	}
	if exists(filepath.Join(swarm, "standby.pid")) {
		t.Error("the new leader left its standby pid file")
	}
	if out := p.run("task", "show", "kept"); !strings.Contains(out, "survives the failover") {
		t.Errorf("task after takeover: %s", out)
	}
	if !strings.Contains(p.read(".swarm/server.log"), "became leader") {
		t.Error("the log does not say who became leader")
	}

	// Stopping cleanly with a standby waiting must stop both, not hand over.
	p.run("up", "--standby")
	next := pidFile(filepath.Join(swarm, "standby.pid"))
	out = p.run("down")
	if !strings.Contains(out, "standby stopped") || !strings.Contains(out, "stopped") {
		t.Fatalf("down: %s", out)
	}
	for _, pid := range []int{standby, next} {
		if processAlive(pid) {
			t.Errorf("process %d still runs after down", pid)
		}
	}
	if health(t, url) != nil {
		t.Error("a server still answers after down")
	}
}
