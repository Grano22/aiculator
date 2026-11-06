package tui_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/swarm"
	"github.com/Grano22/aiculator/internal/tui"
	"github.com/Grano22/aiculator/pkg/observe"
)

// go test ./internal/tui -run Golden -update rewrites the files after a change on purpose.
var update = flag.Bool("update", false, "rewrite the golden files")

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// steady takes out what depends on the wall clock or the machine's speed: uptime, rates,
// latencies, how long ago an agent was seen. What is left is what the actions decided, and
// that must render the same way every time.
func steady(s observe.Snapshot) observe.Snapshot {
	s.UptimeMs, s.OldestMs = 42_000, 0
	s.Version = "test"
	s.Throughput = observe.Throughput{TotalGrants: s.Throughput.TotalGrants, TotalDone: s.Throughput.TotalDone,
		TotalFailed: s.Throughput.TotalFailed, TotalZombies: s.Throughput.TotalZombies}
	s.Latency = map[string]observe.Stats{}
	s.Flow = nil
	for i := range s.Probes {
		s.Probes[i].LatencyUs = 0
	}
	for i := range s.Agents {
		s.Agents[i].SeenAgoMs = 0
	}
	for i := range s.Sessions {
		s.Sessions[i].SeenAgoMs, s.Sessions[i].ConnectedMs = 1000, 60_000
		if !s.Sessions[i].LimitUntil.IsZero() {
			s.Sessions[i].LimitUntil = time.Date(2026, 11, 6, 15, 0, 0, 0, time.Local)
		}
	}
	for i := range s.Tasks {
		s.Tasks[i].AgeMs = 0
	}
	return s
}

type scene struct {
	t   *testing.T
	svc *swarm.Service
	clk *clock
}

func (sc *scene) snap() observe.Snapshot {
	sc.t.Helper()
	s, err := sc.svc.Snapshot(context.Background(), "test")
	if err != nil {
		sc.t.Fatal(err)
	}
	return steady(s)
}

func (sc *scene) wait(a engine.Agent) swarm.Grant {
	sc.t.Helper()
	g, err := sc.svc.Wait(context.Background(), a, 0)
	if err != nil {
		sc.t.Fatal(err)
	}
	return g
}

// check compares every view of the current snapshot with its golden file.
func (sc *scene) check(step string) {
	sc.t.Helper()
	s := sc.snap()
	views := []struct {
		name string
		v    tui.View
	}{{"overview", tui.Overview}, {"tasks", tui.Tasks}, {"sessions", tui.Agents}, {"flow", tui.Flow}, {"usage", tui.Usage}}
	for _, v := range views {
		golden(sc.t, step+"-"+v.name, tui.Render(s, tui.Options{View: v.v, Width: 120, Height: 50}))
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", "golden", name+".txt")
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%s: %v (run with -update to create it)", name, err)
	}
	if strings.TrimSuffix(string(want), "\n") != got {
		t.Errorf("%s changed; run with -update if that was intended\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

// Every screen of top, after each step of a swarm's life: empty, filling up, agents joining
// and working, leases at risk and expired, usage and budgets, failures, a parked task, an
// agent out of usage, and everyone stopped. A change to any screen shows up here.
func TestGoldenScreensFollowTheSwarm(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.StartProjector(ctx); err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	svc, err := swarm.New(ctx, swarm.Options{Store: st, Realm: "demo", Engine: engine.Config{
		Clock: clk, RestartGrace: time.Nanosecond, LeaseTTL: 2 * time.Minute, ProgressTTL: 10 * time.Minute,
		Tick: time.Hour, // Expiry runs only when this test advances the clock and asks.
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close(); st.Close() })
	sc := &scene{t: t, svc: svc, clk: clk}

	sc.check("01-empty")

	add := func(s engine.TaskSpec) {
		t.Helper()
		if _, err := svc.Engine.AddTask(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	add(engine.TaskSpec{ID: "api-health", Title: "Add the /healthz endpoint", Tier: 1, Budget: 2_000_000})
	add(engine.TaskSpec{ID: "auth-tokens", Title: "Validate session tokens", Tier: 1, Priority: 2})
	add(engine.TaskSpec{ID: "search-idx", Title: "Rebuild the search index", Tier: 2})
	add(engine.TaskSpec{ID: "storage", Title: "Redesign the storage layer", Tier: 3, Budget: 1_000_000})
	add(engine.TaskSpec{ID: "docs", Title: "Document the webhooks \x1b[31mred\x1b[0m", Tier: 1}) // An escape in a title stays harmless.
	sc.check("02-queued")

	claude := engine.Agent{ID: "claude-sonnet", Tier: 1}
	opus := engine.Agent{ID: "claude-opus", Tier: 3}
	codex := engine.Agent{ID: "codex", Tier: 1}
	svc.Hello(claude, observe.SessionInfo{Kind: "acp", Harness: "claude-code", Model: "sonnet", Host: "box", PID: 4101, Parent: "aiculator", CWD: "/work/shop", Killable: true})
	svc.Hello(opus, observe.SessionInfo{Kind: "mcp", Harness: "claude-code", Host: "box", PID: 4102, Parent: "claude", CWD: "/work/shop"})
	svc.NoteMCPClient(codex, "codex-mcp-client", "0.40")
	g1 := sc.wait(claude)
	g2 := sc.wait(opus)
	sc.check("03-working")

	// Usage comes in, and time passes: one lease is close to its end.
	if _, err := svc.ReportUsage(ctx, claude, g1.Task.ID, g1.Lease, core.Usage{Tokens: 48_000, Cost: 1_250_000}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReportUsage(ctx, opus, g2.Task.ID, g2.Lease, core.Usage{Tokens: 21_000, Cost: 1_100_000}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(100 * time.Second)
	if _, err := svc.Engine.Heartbeat(ctx, opus, g2.Task.ID, g2.Lease); err != nil {
		t.Fatal(err)
	}
	sc.check("04-at-risk")

	// The lease runs out: the task fails and goes back, and the over-budget one is parked.
	clk.Advance(40 * time.Second)
	if err := svc.Engine.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Engine.Fail(ctx, opus, g2.Task.ID, g2.Lease, "Needs a smaller first step", core.Usage{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Engine.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	sc.check("05-failures")

	// One agent runs out of usage; then everyone is stopped.
	g3 := sc.wait(codex)
	if _, err := svc.ReportLimit(ctx, codex, "usage_limit", clk.Now().Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = g3
	sc.check("06-limited")
	if _, _, err := svc.Stop(ctx, nil); err != nil {
		t.Fatal(err)
	}
	sc.check("07-stopped")

	// The frame itself: a narrow terminal, live and paused, and color.
	s := sc.snap()
	golden(t, "08-narrow", tui.Render(s, tui.Options{View: tui.Overview, Width: 60, Height: 30}))
	golden(t, "08-live", tui.Render(s, tui.Options{View: tui.Tasks, Width: 100, Height: 20, Live: true}))
	golden(t, "08-paused", tui.Render(s, tui.Options{View: tui.Tasks, Width: 100, Height: 20, Paused: true, Live: true}))
	golden(t, "08-color", tui.Render(s, tui.Options{View: tui.Overview, Width: 120, Height: 50, Color: true}))
	golden(t, "08-plain", tui.Render(s, tui.Options{View: tui.Agents, Width: 120, Plain: true}))
	golden(t, "08-stale", tui.Render(s, tui.Options{View: tui.Usage, Width: 100, Height: 30, Stale: "connection refused"}))
}

// A view that cannot fit must still never draw past the screen.
func TestGoldenFramesFitTheScreen(t *testing.T) {
	for _, name := range []string{"03-working-overview", "05-failures-tasks", "08-narrow"} {
		b, err := os.ReadFile(filepath.Join("testdata", "golden", name+".txt"))
		if err != nil {
			t.Skip("no golden files yet")
		}
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if name == "08-narrow" && len(lines) != 30 {
			t.Errorf("%s: %d lines, want 30", name, len(lines))
		}
		for i, l := range lines {
			if w := len([]rune(l)); name == "08-narrow" && w > 60 {
				t.Errorf("%s line %d is %d wide: %q", name, i+1, w, l)
			}
		}
	}
}
