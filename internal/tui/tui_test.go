package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/Grano22/aiculator/pkg/observe"
)

func ms(v int64) *int64 { return &v }

func sample() observe.Snapshot {
	s := observe.Snapshot{
		Version: "v1.2.3", UptimeMs: 3_725_000, Ready: true,
		States:  map[string]int{"ready": 4, "running": 2, "done": 40, "parked": 1},
		Waiting: 3, OldestMs: 65000,
		Throughput: observe.Throughput{DonePerMin: 12.5, GrantsPerMin: 14, CommitsPerSec: 3.2, TotalDone: 40, TotalZombies: 1,
			Commits: []int64{0, 1, 2, 3, 8}, Done: []int64{0, 0, 1, 0, 1}},
		Latency: map[string]observe.Stats{
			observe.LatencyCommit: {Count: 500, P50: 80, P90: 150, P99: 900, Max: 2500, Jitter: 40},
			observe.LatencyQueue:  {Count: 40, P50: 1500, P90: 20000, P99: 90_000_000, Max: 90_000_000, Jitter: 7000},
		},
		Flow: []observe.FlowEdge{{From: "ready", To: "claimed", Count: 44}, {From: "running", To: "failed", Count: 2}},
		Tasks: []observe.TaskLive{
			{ID: "zombie", Title: "Stuck one", State: "running", Tier: 1, Owner: "pi-1", LeaseRemainingMs: ms(-500), ProgressRemaining: ms(-1), Health: observe.HealthExpired},
			{ID: "close", Title: "Close call", State: "running", Tier: 2, Owner: "claude-1", LeaseRemainingMs: ms(20000), ProgressRemaining: ms(30000), Health: observe.HealthAtRisk},
			{ID: "park", Title: "Needs a human", State: "parked", Tier: 3, Reason: "needs_resplit", Health: observe.HealthQueued},
		},
		Agents: []observe.AgentLive{{ID: "claude-1", Tier: 2, State: "working", Task: "close", SeenAgoMs: 1500, Tokens: 12345, Cost: 2_500_000, Done: 7, Failed: 1}},
		Usage: observe.Usage{Tokens: 12345, Spent: 2_500_000, Budgeted: 5_000_000, OverBudget: 1,
			ByTier:   []observe.TierUsage{{Tier: 1, Tasks: 10, Tokens: 12345, Spent: 2_500_000, Budget: 5_000_000}},
			Tightest: []observe.BudgetLine{{ID: "big", Spent: 6_000_000, Budget: 5_000_000, State: "parked"}, {ID: "fine", Spent: 1, Budget: 100, State: "ready"}}},
		Planning: observe.Planning{Epoch: 2, Active: true, Phase: "splitting"},
		Probes:   []observe.Probe{{Name: "writer", OK: true, LatencyUs: 12}, {Name: "git", OK: false, LatencyUs: 3000, Detail: "no such branch"}},
	}
	return s
}

func has(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestOverviewShowsEverything(t *testing.T) {
	out := Render(sample(), Options{Width: 120, Height: 50})
	has(t, out, "v1.2.3", "up 1h02m", "planning: splitting",
		"ready 4 → claimed 0 → running 2", "parked 1", "agents waiting 3", "oldest ready 1m05s",
		"done 12.5/min", "commits 3.2/s", "1 zombies caught",
		"queue wait", "commit", "900µs", "jitter", "1.5ms", "1m30s",
		"writer ok 12µs", "git FAIL", "no such branch",
		"tokens 12.3k", "spent $2.50", "1 over budget",
		"zombie", "expired", "at risk", "Needs a human [needs_resplit]")
}

func TestOtherViews(t *testing.T) {
	has(t, Render(sample(), Options{View: Tasks, Width: 120, Height: 30}), "Tasks with work", "pi-1", "-1s")
	has(t, Render(sample(), Options{View: Agents, Width: 120, Height: 30}), "claude-1", "working", "close", "12.3k", "$2.50")
	has(t, Render(sample(), Options{View: Flow, Width: 100, Height: 30}), "ready    → claimed", "44", "█")
	has(t, Render(sample(), Options{View: Usage, Width: 100, Height: 30}), "By tier", "Tightest budgets", "$6.00 of $5.00", "By agent", "████████████████████")
}

func TestEmptySnapshotDoesNotPanic(t *testing.T) {
	for v := range viewNames {
		_ = Render(observe.Snapshot{}, Options{View: View(v), Width: 80, Height: 24})
		_ = Render(observe.Snapshot{}, Options{View: View(v), Width: 1, Height: 1})
		_ = Render(sample(), Options{View: View(v), Width: 3, Height: 2, Scroll: 999})
	}
}

// An agent controls task titles. Nothing it writes may steer the terminal.
func TestAgentTextCannotControlTheTerminal(t *testing.T) {
	evil := "\x1b]0;pwned\x07\x1b[2J\x1b[31mred‮flip\r\nnext\x9b31m"
	s := sample()
	s.Tasks[0].Title, s.Tasks[0].ID, s.Tasks[0].Owner, s.Tasks[0].State = evil, evil, evil, evil
	s.Agents[0].ID = evil
	s.Agents[0].Task = evil
	s.Planning.Phase = evil
	s.Version = evil
	s.Probes[1].Detail = evil
	s.Probes[1].Name = evil
	s.Usage.Tightest[0].ID, s.Usage.Tightest[0].State = evil, evil
	s.Flow[0].From = evil
	for v := range viewNames {
		out := Render(s, Options{View: View(v), Width: 200, Height: 60}) // No color: any ESC is an injection.
		for _, bad := range []string{"\x1b", "\x07", "\r", "‮", "\x9b"} {
			if strings.Contains(out, bad) {
				t.Errorf("view %d let %q through:\n%q", v, bad, out)
			}
		}
	}
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestFrameFitsTheScreen(t *testing.T) {
	s := sample()
	s.Tasks = nil
	for i := range 100 {
		s.Tasks = append(s.Tasks, observe.TaskLive{ID: fmt.Sprintf("t%d", i), Title: strings.Repeat("long title ", 30), State: "running", Health: observe.HealthOK})
	}
	for _, size := range [][2]int{{80, 24}, {120, 40}, {40, 10}} {
		for v := range viewNames {
			out := Render(s, Options{View: View(v), Width: size[0], Height: size[1], Color: true})
			lines := strings.Split(out, "\n")
			if len(lines) != size[1] {
				t.Errorf("%v view %d: %d lines", size, v, len(lines))
			}
			for _, l := range lines {
				if got := len([]rune(ansi.ReplaceAllString(fit(l, size[0]), ""))); got > size[0] {
					t.Errorf("%v view %d: a line is %d wide", size, v, got)
				}
			}
		}
	}
}

func TestFitKeepsColorCodesOutOfTheCount(t *testing.T) {
	line := "\x1b[31mabcdef\x1b[0mghij"
	got := fit(line, 8)
	if plain := ansi.ReplaceAllString(got, ""); plain != "abcdefgh" {
		t.Fatalf("plain: %q", plain)
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatal("color is left on after a cut")
	}
	if fit("short", 80) != "short" {
		t.Fatal("changed a line that fit")
	}
}

func TestStatusIsNotOnlyColor(t *testing.T) {
	out := Render(sample(), Options{Width: 120, Height: 50, Color: false})
	if strings.Contains(out, "\x1b") {
		t.Fatal("color was off, but codes were written")
	}
	has(t, out, "expired", "at risk", "FAIL")
}

func TestScrollAndStaleAndPaused(t *testing.T) {
	out := Render(sample(), Options{View: Tasks, Width: 120, Height: 30, Scroll: 1, Paused: true, Stale: "connection refused"})
	has(t, out, "PAUSED", "STALE: connection refused", "close")
	if strings.Contains(out, "zombie") {
		t.Error("scrolling did not move past the first row")
	}
}

func TestKeys(t *testing.T) {
	o := Options{}
	for _, k := range []string{"q", "\x03"} {
		if !handleKey(k, &o) {
			t.Errorf("%q should quit", k)
		}
	}
	handleKey("3", &o)
	if o.View != Agents {
		t.Fatal("3")
	}
	handleKey("\t", &o)
	if o.View != Flow {
		t.Fatal("tab")
	}
	handleKey("j", &o)
	handleKey("j", &o)
	handleKey("k", &o)
	if o.Scroll != 1 {
		t.Fatal("scroll")
	}
	handleKey("1", &o)
	if o.Scroll != 0 {
		t.Fatal("switching view should reset scroll")
	}
	handleKey("\x1b[Z", &o)
	if o.View != Usage {
		t.Fatal("shift-tab wraps")
	}
	handleKey("p", &o)
	if !o.Paused {
		t.Fatal("pause")
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[int64]string{500: "500µs", 1500: "1.5ms", 2_500_000: "2.5s", 90_000_000: "1m30s"} {
		if got := Micros(in); got != want {
			t.Errorf("Micros(%d) = %s", in, got)
		}
	}
	if Millis(-1500) != "-1s" || Millis(90_000_000) != "1d01h" || Count(1500) != "1500" || Count(2_500_000) != "2.5M" || Money(1_500_000) != "$1.50" {
		t.Error("formatting")
	}
	if s := Spark([]int64{0, 1, 100}, 10); []rune(s)[0] != ' ' || []rune(s)[2] != '█' {
		t.Errorf("spark: %q", s)
	}
	if Spark(nil, 10) != "" || Spark([]int64{1}, 0) != "" {
		t.Error("empty spark")
	}
}

func TestZeroSizedTerminalStillDraws(t *testing.T) {
	if w, h := ScreenSize(0, 0, nil); w != 100 || h != 40 {
		t.Fatalf("got %dx%d", w, h)
	}
	if w, h := ScreenSize(80, 24, nil); w != 80 || h != 24 {
		t.Fatalf("got %dx%d", w, h)
	}
	if w, _ := ScreenSize(80, 24, fmt.Errorf("no tty")); w != 100 {
		t.Fatal("an error should use the fallback")
	}
}

func fleetRows() []FleetRow {
	return []FleetRow{
		{Name: "shop", Path: "/code/shop", Live: true, Snapshot: observe.Snapshot{
			Ready: true, States: map[string]int{"ready": 3, "running": 2, "done": 7, "parked": 1},
			Tasks:  []observe.TaskLive{{ID: "fix-login", Title: "Fix login", State: "running", Owner: "claude-1", Tier: 1}},
			Agents: []observe.AgentLive{{ID: "claude-1", Tier: 1, State: "working", Task: "fix-login"}},
		}},
		{Name: "docs", Path: "/code/docs"},
		{Name: "old", Path: "/code/old", Missing: true},
	}
}

func TestFleetShowsEveryRealmAndWhoseTasksAreWhose(t *testing.T) {
	realms := RenderFleet(fleetRows(), Options{Plain: true, Width: 140})
	for _, want := range []string{"3 realms, 1 running", "shop", "running", "docs", "stopped", "old", "missing", "/code/shop", "1/1"} {
		if !strings.Contains(realms, want) {
			t.Errorf("missing %q in:\n%s", want, realms)
		}
	}
	tasks := RenderFleet(fleetRows(), Options{View: 1, Plain: true, Width: 140})
	if !strings.Contains(tasks, "shop") || !strings.Contains(tasks, "fix-login") || !strings.Contains(tasks, "claude-1") {
		t.Errorf("tasks:\n%s", tasks)
	}
	agents := RenderFleet(fleetRows(), Options{View: 2, Plain: true, Width: 140})
	if !strings.Contains(agents, "shop") || !strings.Contains(agents, "claude-1") {
		t.Errorf("agents:\n%s", agents)
	}
	// The keys for five views still land somewhere sensible with three.
	if got := RenderFleet(fleetRows(), Options{View: 4, Plain: true, Width: 140}); !strings.Contains(got, "Tasks in progress") {
		t.Errorf("view wrap:\n%s", got)
	}
}

func TestFleetTextCannotControlTheTerminal(t *testing.T) {
	rows := fleetRows()
	rows[0].Name = "evil\x1b[2J"
	rows[0].Path = "/x\x1b]0;owned\x07"
	rows[0].Snapshot.Tasks[0].Title = "t\x1b[31m"
	rows[0].Snapshot.Tasks[0].ID = "i\x1b[H"
	for v := range 3 {
		if got := RenderFleet(rows, Options{View: View(v), Plain: true, Width: 140}); strings.Contains(got, "\x1b") {
			t.Errorf("view %d let an escape through: %q", v, got)
		}
	}
}

func TestFleetWithNoRealms(t *testing.T) {
	if got := RenderFleet(nil, Options{Plain: true}); !strings.Contains(got, "aiculator join") {
		t.Errorf("%s", got)
	}
}
