package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/client"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/swarm"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "swarm")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

type daemon struct {
	cmd *exec.Cmd
	url string
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func startDaemon(t *testing.T, bin, data, addr string, extra ...string) *daemon {
	t.Helper()
	args := append([]string{"serve", "--data", data, "--addr", addr, "--restart-grace", "1h"}, extra...)
	cmd := exec.Command(bin, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d := &daemon{cmd: cmd, url: "http://" + addr}
	c := client.New(d.url, "")
	dl := time.Now().Add(60 * time.Second)
	for time.Now().Before(dl) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := c.Get(ctx, "/v1/healthz", nil)
		cancel()
		if err == nil {
			return d
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	t.Fatal("daemon did not start")
	return nil
}

func (d *daemon) kill9() {
	_ = d.cmd.Process.Signal(syscall.SIGKILL)
	_ = d.cmd.Wait()
}

type grantRec struct {
	task  string
	lease uint64
}

// ackedState is what clients were told. Anything acknowledged must survive a
// crash; the test fails if it finds a task behind what a client saw.
type ackedState struct {
	mu     sync.Mutex
	grants map[grantRec]string
	floor  map[string]uint64 // Highest lease epoch a client has seen for each task.
	done   map[string]bool   // Tasks a client saw reach review or beyond.
	added  map[string]bool
	dups   []string
}

func newAcked() *ackedState {
	return &ackedState{grants: map[grantRec]string{}, floor: map[string]uint64{}, done: map[string]bool{}, added: map[string]bool{}}
}

func (s *ackedState) grant(task string, lease uint64, agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := grantRec{task, lease}
	if prev, dup := s.grants[key]; dup && prev != agent {
		s.dups = append(s.dups, fmt.Sprintf("%s lease %d went to %s and %s", task, lease, prev, agent))
	}
	s.grants[key] = agent
	s.floor[task] = max(s.floor[task], lease)
}

// The Phase 1 crash gate. Clients hammer a real daemon process, which is killed
// with SIGKILL at a random moment mid-write, 100 times. After every restart:
// every stored task must be valid, nothing acknowledged may be lost, and no lease
// may have been granted twice.
func TestKill9Gate(t *testing.T) {
	iterations := 100
	if v := os.Getenv("SWARM_KILL_ITERATIONS"); v != "" {
		iterations, _ = strconv.Atoi(v)
	}
	if testing.Short() {
		iterations = 5
	}
	bin := buildBinary(t)
	data := filepath.Join(t.TempDir(), "data")
	addr := freeAddr(t)
	acked := newAcked()
	var taskSeq atomic.Int64
	var totalAcks atomic.Int64

	for it := range iterations {
		d := startDaemon(t, bin, data, addr)
		tokBytes, err := os.ReadFile(filepath.Join(data, "admin.token"))
		if err != nil {
			t.Fatal(err)
		}
		admin := client.New(d.url, string(tokBytes))
		verifyAfterCrash(t, it, admin, acked)

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for w := range 6 {
			name := fmt.Sprintf("worker%d", w)
			var out map[string]string
			if err := admin.Post(ctx, "/v1/tokens", api.TokenReq{Agent: name, Tier: 1}, &out); err != nil {
				t.Fatal(err)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				hammer(ctx, admin, client.New(d.url, out["token"]), name, acked, &taskSeq, &totalAcks)
			}()
		}
		// Kill at a random moment, so the write in flight varies.
		time.Sleep(time.Duration(5+rand.IntN(120)) * time.Millisecond)
		d.kill9()
		cancel()
		wg.Wait()
	}

	d := startDaemon(t, bin, data, addr)
	defer d.kill9()
	tok, _ := os.ReadFile(filepath.Join(data, "admin.token"))
	verifyAfterCrash(t, iterations, client.New(d.url, string(tok)), acked)
	acked.mu.Lock()
	defer acked.mu.Unlock()
	t.Logf("%d kill -9 crashes, %d tasks added, %d grants, %d acknowledged writes checked",
		iterations, len(acked.added), len(acked.grants), totalAcks.Load())
	if len(acked.dups) > 0 {
		t.Fatalf("double grants: %v", acked.dups)
	}
	if len(acked.grants) < iterations {
		t.Fatalf("only %d grants: the kills happened before any real work", len(acked.grants))
	}
}

func hammer(ctx context.Context, admin, c *client.Client, name string, acked *ackedState, seq, acks *atomic.Int64) {
	for ctx.Err() == nil {
		id := fmt.Sprintf("k%d", seq.Add(1))
		var added core.Task
		if err := admin.Post(ctx, "/v1/tasks", engine.TaskSpec{ID: id, Title: "x"}, &added); err != nil {
			return
		}
		acked.mu.Lock()
		acked.added[id] = true
		acked.mu.Unlock()
		acks.Add(1)

		var g swarm.Grant
		if err := c.Post(ctx, "/v1/agent/wait", api.WaitReq{HoldMs: 0}, &g); err != nil {
			return
		}
		if g.Action != engine.ActionTask {
			continue
		}
		acked.grant(g.Task.ID, g.Lease, name)
		acks.Add(1)
		if g.Job == "review" {
			if err := c.Post(ctx, "/v1/agent/verdict", api.LeaseReq{TaskID: g.Task.ID, Lease: g.Lease, Pass: true}, nil); err != nil {
				var se *client.StatusError
				if !errors.As(err, &se) {
					return
				}
			}
			continue
		}
		q := api.LeaseReq{TaskID: g.Task.ID, Lease: g.Lease}
		if err := c.Post(ctx, "/v1/agent/heartbeat", q, nil); err != nil {
			continue
		}
		path := "/v1/agent/submit"
		if rand.IntN(3) == 0 {
			path = "/v1/agent/fail"
		}
		if err := c.Post(ctx, path, q, nil); err != nil {
			continue
		}
		if path == "/v1/agent/submit" {
			acked.mu.Lock()
			acked.done[g.Task.ID] = true
			acked.mu.Unlock()
		}
		acks.Add(1)
	}
}

func verifyAfterCrash(t *testing.T, it int, admin *client.Client, acked *ackedState) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var ts []core.Task
	if err := admin.Get(ctx, "/v1/tasks", &ts); err != nil {
		t.Fatalf("after crash %d: %v", it, err)
	}
	byID := make(map[string]core.Task, len(ts))
	for _, x := range ts {
		byID[x.ID] = x
		if err := core.Validate(x); err != nil {
			t.Fatalf("after crash %d: invalid task: %v", it, err)
		}
	}
	acked.mu.Lock()
	defer acked.mu.Unlock()
	for id := range acked.added {
		if _, ok := byID[id]; !ok {
			t.Fatalf("after crash %d: acknowledged task %s is gone", it, id)
		}
	}
	for id, lease := range acked.floor {
		if byID[id].LeaseEpoch < lease {
			t.Fatalf("after crash %d: %s lease went back from %d to %d", it, id, lease, byID[id].LeaseEpoch)
		}
	}
	for id := range acked.done {
		switch byID[id].State {
		case core.Draft, core.Claimed:
			// A submit was acknowledged, so it can only have moved forward from
			// review, possibly through rejection and a new attempt.
			if byID[id].Attempt < 2 {
				t.Fatalf("after crash %d: submitted %s fell back to %s", it, id, byID[id].State)
			}
		}
	}
}

func TestAdminTokenFileIsPrivate(t *testing.T) {
	bin := buildBinary(t)
	data := filepath.Join(t.TempDir(), "data")
	d := startDaemon(t, bin, data, freeAddr(t))
	defer d.kill9()
	for _, name := range []string{"admin.token", "signing.key"} {
		fi, err := os.Stat(filepath.Join(data, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %v", name, fi.Mode().Perm())
		}
	}
	// Reject unauthenticated and agent-level access to admin routes.
	var se *client.StatusError
	err := client.New(d.url, "").Get(context.Background(), "/v1/tasks", nil)
	if !errors.As(err, &se) || se.Code != 401 {
		t.Fatalf("no token: %v", err)
	}
	err = client.New(d.url, "garbage.garbage").Get(context.Background(), "/v1/tasks", nil)
	if !errors.As(err, &se) || se.Code != 401 {
		t.Fatalf("bad token: %v", err)
	}
	tok, _ := os.ReadFile(filepath.Join(data, "admin.token"))
	admin := client.New(d.url, string(tok))
	var out map[string]string
	if err := admin.Post(context.Background(), "/v1/tokens", api.TokenReq{Agent: "a", Tier: 1}, &out); err != nil {
		t.Fatal(err)
	}
	err = client.New(d.url, out["token"]).Get(context.Background(), "/v1/tasks", nil)
	if !errors.As(err, &se) || se.Code != 403 {
		t.Fatalf("agent on admin route: %v", err)
	}
	if err := admin.Post(context.Background(), "/v1/tokens", api.TokenReq{Agent: "system:x", Tier: 1}, nil); err == nil || !strings.Contains(err.Error(), "invalid agent") {
		t.Fatalf("issued a system identity: %v", err)
	}
}
