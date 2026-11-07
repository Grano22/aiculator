package swarm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/pkg/observe"
)

func sessionOf(t *testing.T, s *Service, id string) observe.SessionLive {
	t.Helper()
	snap, err := s.Snapshot(bg, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range snap.Sessions {
		if x.Agent == id {
			return x
		}
	}
	t.Fatalf("no session %s in %+v", id, snap.Sessions)
	return observe.SessionLive{}
}

func TestHelloIsShownWithWhatTheAgentDoes(t *testing.T) {
	s := service(t)
	a := engine.Agent{ID: "drv", Tier: 1}
	s.Hello(a, observe.SessionInfo{Kind: "acp", Harness: "claude-code", Host: "box", PID: 42, CWD: "/work/x", Parent: "zsh\x1b[31m", Killable: true})
	x := sessionOf(t, s, "drv")
	if x.Kind != "acp" || x.Harness != "claude-code" || x.PID != 42 || x.Tier != 1 || x.Parent != "zsh[31m" {
		t.Errorf("session %+v", x)
	}
	g, err := s.Wait(bg, a, 0)
	if err != nil || g.Action != engine.ActionTask {
		t.Fatal(g, err)
	}
	if x := sessionOf(t, s, "drv"); x.State != "working" || x.Task != "t" {
		t.Errorf("working: %+v", x)
	}
	if k := s.Killable("box"); len(k) != 1 || k[0].PID != 42 {
		t.Errorf("killable: %+v", k)
	}
	if k := s.Killable("other-host"); len(k) != 0 {
		t.Errorf("a process on another host is killable: %+v", k)
	}
}

// Stopping reaches an agent in the middle of a long wait, and what it held goes back.
func TestStopEndsWaitsAndHandsBackWork(t *testing.T) {
	s := service(t)
	holder := engine.Agent{ID: "holder", Tier: 1}
	g, err := s.Wait(bg, holder, 0)
	if err != nil || g.Action != engine.ActionTask {
		t.Fatal(g, err)
	}
	waiter := engine.Agent{ID: "waiter", Tier: 1}
	got := make(chan Grant, 1)
	go func() {
		g, _ := s.Wait(bg, waiter, time.Minute)
		got <- g
	}()
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	stopped, released, err := s.Stop(bg, nil)
	if err != nil || len(released) != 1 || released[0] != "t" || len(stopped) < 2 {
		t.Fatalf("stop: %v %v %v", stopped, released, err)
	}
	select {
	case g := <-got:
		if g.Action != engine.ActionExit || g.Reason != "stopped_by_operator" {
			t.Errorf("the waiting agent got %+v", g)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait in progress did not end")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("stopping took too long")
	}
	if task, _ := s.Engine.Get(bg, "t"); task.State != core.Ready || task.Failures != 0 {
		t.Errorf("the held task: %s, %d failures", task.State, task.Failures)
	}
	if g, _ := s.Wait(bg, holder, 0); g.Action != engine.ActionExit {
		t.Errorf("a stopped agent got %+v", g)
	}
	// A new process saying hello starts over.
	s.Hello(holder, observe.SessionInfo{Kind: "acp"})
	if g, _ := s.Wait(bg, holder, 0); g.Action != engine.ActionTask {
		t.Errorf("after hello: %+v", g)
	}
}

func TestALimitKeepsWorkAwayUntilItResets(t *testing.T) {
	s := service(t)
	a := engine.Agent{ID: "out", Tier: 1}
	g, err := s.Wait(bg, a, 0)
	if err != nil || g.Action != engine.ActionTask {
		t.Fatal(g, err)
	}
	until := time.Now().Add(300 * time.Millisecond)
	released, err := s.ReportLimit(bg, a, "usage_limit", until)
	if err != nil || len(released) != 1 {
		t.Fatalf("limit: %v %v", released, err)
	}
	g, _ = s.Wait(bg, a, 0)
	if g.Action != engine.ActionExit || !strings.HasPrefix(g.Reason, "usage_limit") || !strings.Contains(g.Reason, "until") {
		t.Errorf("while limited: %+v", g)
	}
	if x := sessionOf(t, s, "out"); x.State != "limited" || x.Limit != "usage_limit" {
		t.Errorf("session: %+v", x)
	}
	time.Sleep(400 * time.Millisecond)
	if g, _ := s.Wait(bg, a, 0); g.Action != engine.ActionTask {
		t.Errorf("after the reset: %+v", g)
	}
	if task, _ := s.Engine.Get(bg, "t"); task.Failures != 0 {
		t.Errorf("a limit cost the task a failure: %+v", task)
	}
}

func TestUsageReachesTheTaskOrTheAgent(t *testing.T) {
	s := service(t)
	a := engine.Agent{ID: "spender", Tier: 1}
	g, _ := s.Wait(bg, a, 0)
	if _, err := s.ReportUsage(bg, a, "t", g.Lease, core.Usage{Tokens: 1200, Cost: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReportUsage(bg, a, "", 0, core.Usage{Tokens: 300}); err != nil {
		t.Fatal(err)
	}
	if task, _ := s.Engine.Get(bg, "t"); task.Tokens != 1200 || task.Spent != 30 {
		t.Errorf("task usage: %d %d", task.Tokens, task.Spent)
	}
	snap, _ := s.Snapshot(bg, "")
	for _, ag := range snap.Agents {
		if ag.ID == "spender" && ag.Tokens != 1500 {
			t.Errorf("agent tokens %d, want 1500", ag.Tokens)
		}
	}
	if _, err := s.ReportUsage(bg, a, "", 0, core.Usage{Tokens: -5}); err == nil {
		t.Error("negative usage was accepted")
	}
	// Usage in a heartbeat must survive: such a heartbeat is always written.
	if _, err := s.Engine.HeartbeatUsage(bg, a, "t", g.Lease, core.Usage{Tokens: 7}); err != nil {
		t.Fatal(err)
	}
	if stored, _, _ := s.Store.GetTask("t"); stored.Tokens != 1207 {
		t.Errorf("stored tokens %d, want 1207", stored.Tokens)
	}
}

func TestHoldsLearnTheHarnessTimeout(t *testing.T) {
	h := newHoldLearner()
	h.completed(HoldStart)
	if h.current() != HoldStart+holdStep {
		t.Errorf("after a full hold: %v", h.current())
	}
	for range 10 {
		h.completed(h.current())
	}
	if h.current() != HoldMax {
		t.Errorf("grows to %v, want %v", h.current(), HoldMax)
	}
	h.cutShort(30 * time.Second)
	if h.current() != 27*time.Second {
		t.Errorf("after a cut at 30s: %v", h.current())
	}
	for range 5 {
		h.completed(h.current())
	}
	if h.current() != 27*time.Second {
		t.Errorf("grew past the limit it saw: %v", h.current())
	}
	h.cutShort(time.Second) // Too short to be a timeout.
	if h.current() != 27*time.Second {
		t.Errorf("a quick hang-up moved it: %v", h.current())
	}

	// In the service: a caller that hangs up early teaches the session.
	s := service(t)
	a := engine.Agent{ID: "short", Tier: 2} // Tier 2: the tier 1 task t is not for it.
	ctx, cancel := context.WithTimeout(bg, 6*time.Second)
	defer cancel()
	if _, err := s.Wait(ctx, a, AutoHold); err == nil {
		t.Fatal("the wait outlived its caller")
	}
	// 6s less the margin is under the floor, so the hold floors.
	if x := sessionOf(t, s, "short"); x.HoldMs != HoldMin.Milliseconds() {
		t.Errorf("hold after a 6s cut: %dms, want %v", x.HoldMs, HoldMin)
	}
}

func TestGoneSessionsCanBeForgotten(t *testing.T) {
	s := service(t)
	s.Hello(engine.Agent{ID: "old", Tier: 1}, observe.SessionInfo{Kind: "mcp"})
	s.sess.mu.Lock()
	s.sess.byID["old"].started = time.Now().Add(-time.Hour)
	s.sess.mu.Unlock()
	s.Hello(engine.Agent{ID: "live", Tier: 1}, observe.SessionInfo{Kind: "mcp"})
	s.Engine.Collector().Seen("live", 1, time.Now())
	if got := s.Forget(nil); len(got) != 1 || got[0] != "old" {
		t.Errorf("forgot %v", got)
	}
	s.sess.mu.Lock()
	s.sess.get("live").started = time.Now().Add(-time.Hour)
	s.sess.mu.Unlock()
	s.Engine.Collector().Seen("live", 1, time.Now().Add(-time.Hour))
	s.prune()
	snap, _ := s.Snapshot(bg, "")
	for _, x := range snap.Sessions {
		if x.Agent == "live" {
			t.Errorf("a session gone an hour was kept: %+v", x)
		}
	}
}

func TestMCPClientIsNotedOnce(t *testing.T) {
	s := service(t)
	a := engine.Agent{ID: "direct", Tier: 1}
	s.NoteMCPClient(a, "claude-code", "2.1")
	s.NoteMCPClient(a, "other", "9")
	if x := sessionOf(t, s, "direct"); x.Kind != "mcp-http" || x.Client != "claude-code" {
		t.Errorf("%+v", x)
	}
	s.Hello(a, observe.SessionInfo{Kind: "mcp", Harness: "claude-code"})
	s.NoteMCPClient(a, "other", "9")
	if x := sessionOf(t, s, "direct"); x.Kind != "mcp" {
		t.Errorf("hello was overwritten: %+v", x)
	}
}

// A force disconnect, stop then forget, takes the session off the list, but an agent whose
// process carries on still gets exit rather than new work.
func TestForceDisconnectKeepsTheAgentOut(t *testing.T) {
	s := service(t)
	a := engine.Agent{ID: "rogue", Tier: 1}
	s.Hello(a, observe.SessionInfo{Kind: "mcp"})
	if _, _, err := s.Stop(bg, []string{"rogue"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Forget([]string{"rogue"}); len(got) != 1 {
		t.Fatalf("forgot %v", got)
	}
	snap, _ := s.Snapshot(bg, "")
	for _, x := range snap.Sessions {
		if x.Agent == "rogue" {
			t.Errorf("a disconnected session is still listed: %+v", x)
		}
	}
	if g, _ := s.Wait(bg, a, 0); g.Action != engine.ActionExit {
		t.Errorf("a disconnected agent got %+v", g)
	}
	snap, _ = s.Snapshot(bg, "")
	for _, x := range snap.Sessions {
		if x.Agent == "rogue" {
			t.Errorf("its wait brought it back to the list: %+v", x)
		}
	}
	// A new process that says hello is let back in.
	s.Hello(a, observe.SessionInfo{Kind: "mcp"})
	if g, _ := s.Wait(bg, a, 0); g.Action != engine.ActionTask {
		t.Errorf("after hello: %+v", g)
	}
}
