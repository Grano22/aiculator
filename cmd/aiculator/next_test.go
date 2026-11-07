package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The v2 loop from end to end: work next waits in a helper, hands over a task, work done
// finishes it, and a next killed by its harness loses nothing.
func TestWorkNextAndDone(t *testing.T) {
	p := newProject(t)
	p.run("up")
	name := strings.TrimSpace(p.run("work", "join", "--tier", "1"))
	t.Cleanup(func() { _, _ = p.tryIn(p.root, "work", "helper", "stop", "--agent", name) })

	run := func(ctx context.Context, args ...string) (string, int) {
		c := exec.CommandContext(ctx, p.bin, args...)
		c.Dir = p.root
		c.Env = append(os.Environ(), "HOME="+p.home, "AICULATOR_REALMS="+filepath.Join(p.home, "realms"))
		out, err := c.Output()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode()
		}
		if err != nil {
			return string(out), -1
		}
		return string(out), 0
	}
	start := time.Now()
	out, code := run(context.Background(), "work", "next", "--agent", name, "--wait", "2s")
	if code != 3 || !strings.Contains(out, `"action": "again"`) || time.Since(start) > 8*time.Second {
		t.Fatalf("nothing queued: code %d after %v\n%s", code, time.Since(start), out)
	}
	p.run("task", "add", "--id", "n1", "--title", "first", "--allow-empty")
	out, code = run(context.Background(), "work", "next", "--agent", name, "--wait", "10s")
	var rep struct {
		Action string `json:"action"`
		Grant  struct {
			Task struct{ ID string } `json:"task"`
		} `json:"grant"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != 0 || rep.Action != "task" || rep.Grant.Task.ID != "n1" {
		t.Fatalf("a task: code %d %v\n%s", code, err, out)
	}
	if out := p.run("work", "done", "submit", "--agent", name, "--note", "done"); !strings.Contains(out, `"task": "n1"`) {
		t.Errorf("done: %s", out)
	}
	if out := p.run("work", "helper", "status", "--agent", name); !strings.Contains(out, `"max_wait_ms"`) {
		t.Errorf("status: %s", out)
	}

	// The harness kills work next; a task arrives meanwhile. The next call gets it at once.
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	go func() {
		time.Sleep(900 * time.Millisecond)
		p.run("task", "add", "--id", "n2", "--title", "second", "--allow-empty")
	}()
	_, _ = run(ctx, "work", "next", "--agent", name, "--wait", "20s")
	cancel()
	time.Sleep(3 * time.Second)
	start = time.Now()
	out, code = run(context.Background(), "work", "next", "--agent", name, "--wait", "10s")
	if code != 0 || !strings.Contains(out, `"n2"`) {
		t.Fatalf("after the kill: code %d\n%s", code, out)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("the kept task took %v to come back", time.Since(start))
	}
	p.run("work", "done", "release", "--agent", name)
	if _, err := p.tryIn(p.root, "work", "done", "submit", "--agent", name); err == nil {
		t.Error("done with nothing held worked")
	}
	if out := p.run("work", "helper", "stop", "--agent", name); strings.Contains(out, "Error") {
		t.Errorf("stop: %s", out)
	}
}
