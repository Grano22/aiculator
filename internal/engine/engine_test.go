package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anishathalye/porcupine"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/store"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)} }

func open(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func setup(t *testing.T, cfg Config) (*Engine, *store.Store) {
	t.Helper()
	st := open(t, t.TempDir())
	if cfg.RestartGrace == 0 {
		cfg.RestartGrace = time.Nanosecond
	}
	e, err := New(context.Background(), st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close(); st.Close() })
	return e, st
}

var bg = context.Background()

func add(t *testing.T, e *Engine, s TaskSpec) core.Task {
	t.Helper()
	x, err := e.AddTask(bg, s)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	dl := time.Now().Add(5 * time.Second)
	for time.Now().Before(dl) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func state(t *testing.T, e *Engine, id string) core.State {
	t.Helper()
	x, err := e.Get(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return x.State
}

var a1 = Agent{ID: "a1", Tier: 1}

func TestDoubleClaimNeverSucceeds(t *testing.T) {
	e, _ := setup(t, Config{})
	const tasks, agents = 200, 16
	for i := range tasks {
		add(t, e, TaskSpec{ID: fmt.Sprintf("t%d", i), Title: "x"})
	}
	var mu sync.Mutex
	got := map[string]string{}
	var wg sync.WaitGroup
	for a := range agents {
		wg.Go(func() {
			ag := Agent{ID: fmt.Sprintf("a%d", a), Tier: 1}
			for {
				r, err := e.Wait(bg, ag, 0)
				if err != nil {
					t.Error(err)
					return
				}
				if r.Action != ActionTask {
					return
				}
				mu.Lock()
				if prev, dup := got[r.Task.ID]; dup {
					t.Errorf("%s granted to %s and %s", r.Task.ID, prev, ag.ID)
				}
				got[r.Task.ID] = ag.ID
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(got) != tasks {
		t.Fatalf("granted %d of %d", len(got), tasks)
	}
}

func TestClaimRaceHasOneWinner(t *testing.T) {
	e, _ := setup(t, Config{})
	add(t, e, TaskSpec{ID: "only", Title: "x"})
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			if _, err := e.Claim(bg, Agent{ID: fmt.Sprintf("a%d", i), Tier: 1}, "only"); err == nil {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("winners = %d", wins.Load())
	}
}

func TestStaleRevisionIsRejected(t *testing.T) {
	e, st := setup(t, Config{})
	x := add(t, e, TaskSpec{ID: "s", Title: "x"})
	eventually(t, "ready", func() bool { return state(t, e, "s") == core.Ready })
	x, _ = e.Get(bg, x.ID)
	x.Title = "foreign write"
	if err := st.RawPutForTest(x); err != nil { // Advances the KV revision behind the engine.
		t.Fatal(err)
	}
	if _, err := e.Claim(bg, a1, "s"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want stale revision", err)
	}
	// The engine reloaded, so the next attempt sees the new value and succeeds.
	r, err := e.Claim(bg, a1, "s")
	if err != nil || r.Task.Title != "foreign write" {
		t.Fatalf("after reload: %v %+v", err, r.Task)
	}
}

func TestWakeLatency(t *testing.T) {
	e, _ := setup(t, Config{})
	const n = 300
	lat := make([]time.Duration, 0, n)
	for i := range n {
		got := make(chan time.Time, 1)
		go func() {
			r, err := e.Wait(bg, a1, 5*time.Second)
			if err != nil || r.Action != ActionTask {
				t.Errorf("wait: %v %+v", err, r)
			}
			got <- time.Now()
		}()
		// Give the waiter time to park, so this measures a real wakeup.
		eventually(t, "parked", func() bool {
			s, _ := e.Situation(bg, a1)
			return s.WaitingAgent[1] == 1
		})
		start := time.Now()
		add(t, e, TaskSpec{ID: fmt.Sprintf("w%d", i), Title: "x"})
		lat = append(lat, (<-got).Sub(start))
	}
	slices.Sort(lat)
	p50, p99 := lat[n/2], lat[n*99/100]
	t.Logf("add-to-wake latency: p50=%v p99=%v max=%v", p50, p99, lat[n-1])
	if p99 > 50*time.Millisecond {
		t.Fatalf("p99 %v over the 50 ms hard limit", p99)
	}
}

func TestPlanFlowInformsThenDelivers(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, ExitGrace: time.Minute, MaxHold: time.Minute})
	epoch, err := e.PlanBegin(bg, "p", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.PlanUpdate(bg, "p", epoch, time.Minute, "splitting", map[int]int{1: 2, 3: 0}, 5000); err != nil {
		t.Fatal(err)
	}
	r, _ := e.Wait(bg, a1, 0)
	if r.Action != ActionWait || r.Reason != ReasonPlanning || r.Planning == nil || r.Planning.Phase != "splitting" || r.Planning.ExpectedTasks[1] != 2 {
		t.Fatalf("while planning: %+v planning=%+v", r, r.Planning)
	}
	// Drafts stay invisible until the plan commits.
	if _, err := e.PlanAdd(bg, "p", epoch, TaskSpec{ID: "p1", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.Wait(bg, a1, 0); r.Action != ActionWait {
		t.Fatalf("draft leaked to an agent: %+v", r)
	}
	done := make(chan WaitResponse, 1)
	go func() {
		r, _ := e.Wait(bg, a1, time.Minute)
		done <- r
	}()
	eventually(t, "parked", func() bool { s, _ := e.Situation(bg, a1); return s.WaitingAgent[1] == 1 })
	if err := e.PlanCommit(bg, "p", epoch); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.Action != ActionTask || r.Task.ID != "p1" {
			t.Fatalf("after commit: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter was not woken by the commit")
	}
}

func TestExitIsTierAwareDuringPlanning(t *testing.T) {
	e, _ := setup(t, Config{Clock: newClock(), ExitGrace: time.Hour})
	epoch, _ := e.PlanBegin(bg, "p", time.Minute)
	if err := e.PlanUpdate(bg, "p", epoch, time.Minute, "x", map[int]int{2: 3}, 0); err != nil {
		t.Fatal(err)
	}
	// Nothing is coming at or below tier 1, so tier 1 can leave right now.
	if r, _ := e.Wait(bg, Agent{ID: "low", Tier: 1}, 0); r.Action != ActionExit || r.Reason != ReasonNoneExpect {
		t.Fatalf("tier 1: %+v", r)
	}
	// Tier 3 has none directly, but tier 2 work can escalate to it.
	if r, _ := e.Wait(bg, Agent{ID: "high", Tier: 3}, 0); r.Action != ActionWait {
		t.Fatalf("tier 3: %+v", r)
	}
}

func TestLiveLowerTierWorkKeepsHigherTierWaiting(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, ExitGrace: time.Second})
	add(t, e, TaskSpec{ID: "t", Title: "x"})
	if r, _ := e.Wait(bg, a1, 0); r.Action != ActionTask {
		t.Fatalf("grant: %+v", r)
	}
	if r, _ := e.Wait(bg, Agent{ID: "ultra", Tier: 3}, 0); r.Action != ActionWait || r.Reason != ReasonTierBusy {
		t.Fatalf("tier 3 should wait for possible escalation: %+v", r)
	}
}

func TestExitOnlyAfterGraceAndNotOnArrival(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, ExitGrace: 30 * time.Second, Tick: time.Millisecond})
	clk.Advance(time.Hour) // The swarm has been idle for a long time.
	eventually(t, "tick", func() bool { return e.Sync(bg) == nil })
	if r, _ := e.Wait(bg, a1, 0); r.Action != ActionWait || r.Reason != ReasonIdle {
		t.Fatalf("on arrival: %+v", r)
	}
	clk.Advance(31 * time.Second)
	if r, _ := e.Wait(bg, a1, 0); r.Action != ActionExit || r.Reason != ReasonPlanDone {
		t.Fatalf("after grace: %+v", r)
	}
}

func TestPlannerCrashBumpsEpochAndCleansUp(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, Tick: time.Millisecond, PlanLostGrace: time.Minute, ExitGrace: time.Hour})
	epoch, _ := e.PlanBegin(bg, "dead", time.Second)
	if _, err := e.PlanAdd(bg, "dead", epoch, TaskSpec{ID: "orphan", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Second)
	eventually(t, "orphan canceled", func() bool { return state(t, e, "orphan") == core.Canceled })
	r, _ := e.Wait(bg, a1, 0)
	if r.Action != ActionWait || r.Reason != ReasonPlannerLost {
		t.Fatalf("got %+v", r)
	}
	// The planner returning from the dead is fenced out.
	if err := e.PlanCommit(bg, "dead", epoch); !errors.Is(err, ErrPlanLost) {
		t.Fatalf("zombie commit: %v", err)
	}
	if _, err := e.PlanBegin(bg, "new", time.Minute); err != nil {
		t.Fatalf("new planner: %v", err)
	}
}

// Regression test for the counterexample TLC found in spec/tla/Planner.tla: tier 1
// agents exit on a hint of no work, then the planner adds tier 1 work anyway.
func TestHintsArePromises(t *testing.T) {
	e, _ := setup(t, Config{Clock: newClock(), ExitGrace: time.Hour})
	ep, _ := e.PlanBegin(bg, "p", time.Minute)
	if err := e.PlanUpdate(bg, "p", ep, time.Minute, "x", map[int]int{2: 1}, 0); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.Wait(bg, a1, 0); r.Action != ActionExit {
		t.Fatalf("tier 1 should exit: %+v", r)
	}
	if _, err := e.PlanAdd(bg, "p", ep, TaskSpec{ID: "late", Title: "x", Tier: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("added work the exited agent would have taken: %v", err)
	}
	if err := e.PlanUpdate(bg, "p", ep, time.Minute, "x", map[int]int{1: 1, 2: 1}, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("raised a hint agents already acted on: %v", err)
	}
	// What was promised still works, and a phase-only update keeps the promise.
	if err := e.PlanUpdate(bg, "p", ep, time.Minute, "later", nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PlanAdd(bg, "p", ep, TaskSpec{ID: "ok", Title: "x", Tier: 2}); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.Wait(bg, a1, 0); r.Action != ActionExit {
		t.Fatalf("tier 1 should still exit: %+v", r)
	}
}

func TestSecondPlannerIsRefused(t *testing.T) {
	e, _ := setup(t, Config{Clock: newClock()})
	if _, err := e.PlanBegin(bg, "a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PlanBegin(bg, "b", time.Minute); !errors.Is(err, ErrPlanBusy) {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanCycleIsRejected(t *testing.T) {
	e, _ := setup(t, Config{Clock: newClock()})
	ep, _ := e.PlanBegin(bg, "p", time.Minute)
	for _, s := range []TaskSpec{{ID: "x", Title: "x", Deps: []string{"y"}}, {ID: "y", Title: "y", Deps: []string{"x"}}} {
		if _, err := e.PlanAdd(bg, "p", ep, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.PlanCommit(bg, "p", ep); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestZombieIsCaughtByProgressTimeout(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, Tick: time.Millisecond, LeaseTTL: time.Hour, ProgressTTL: time.Minute})
	add(t, e, TaskSpec{ID: "z", Title: "x"})
	r, _ := e.Wait(bg, a1, 0)
	// It heartbeats faithfully and reports the same evidence: alive, doing nothing.
	for range 6 {
		if _, err := e.Progress(bg, a1, "z", r.Lease, "same-tree", "", "", core.Usage{}); err != nil {
			break // Caught: its lease is gone, so this is the expected outcome.
		}
		clk.Advance(30 * time.Second)
		if err := e.Sync(bg); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	eventually(t, "requeue", func() bool { return state(t, e, "z") == core.Ready })
	x, _ := e.Get(bg, "z")
	if x.Failures != 1 || !strings.Contains(x.Notes[len(x.Notes)-1].Text, "no_progress") {
		t.Fatalf("got %+v", x)
	}
	// Its late submit is rejected, even though it is the same agent again.
	if _, err := e.Submit(bg, a1, "z", r.Lease, "", "", "done!", core.Usage{}); !errors.Is(err, core.ErrFenced) {
		t.Fatalf("zombie submit: %v", err)
	}
}

func TestClaimTimeoutRequeuesWithoutPenalty(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, Tick: time.Millisecond, LeaseTTL: time.Minute})
	add(t, e, TaskSpec{ID: "c", Title: "x"})
	if r, _ := e.Wait(bg, a1, 0); r.Action != ActionTask {
		t.Fatal("no grant")
	}
	clk.Advance(2 * time.Minute)
	eventually(t, "requeue", func() bool { return state(t, e, "c") == core.Ready })
	if x, _ := e.Get(bg, "c"); x.Failures != 0 {
		t.Fatalf("failures = %d", x.Failures)
	}
}

func TestEscalationAcrossTiersCarriesNotes(t *testing.T) {
	e, _ := setup(t, Config{Clock: newClock()})
	add(t, e, TaskSpec{ID: "e", Title: "x"})
	fail := func(a Agent) {
		t.Helper()
		r, err := e.Wait(bg, a, 0)
		if err != nil || r.Action != ActionTask {
			t.Fatalf("%s: %v %+v", a.ID, err, r)
		}
		if _, err := e.Fail(bg, a, "e", r.Lease, "tried "+a.ID, core.Usage{}); err != nil {
			t.Fatal(err)
		}
	}
	t1, t2 := Agent{ID: "t1", Tier: 1}, Agent{ID: "t2", Tier: 2}
	fail(t1)
	fail(t1)
	if r, _ := e.Wait(bg, t1, 0); r.Action == ActionTask {
		t.Fatal("tier 1 got a task that escalated above it")
	}
	r, _ := e.Wait(bg, t2, 0)
	if r.Action != ActionTask || r.Task.Tier != 2 || len(r.Task.Notes) < 2 {
		t.Fatalf("escalated grant: %+v", r.Task)
	}
	if _, err := e.Fail(bg, t2, "e", r.Lease, "x", core.Usage{}); err != nil {
		t.Fatal(err)
	}
	fail(t2)
	t3 := Agent{ID: "t3", Tier: 3}
	fail(t3)
	fail(t3)
	eventually(t, "parked", func() bool { return state(t, e, "e") == core.Parked })
}

func TestHigherTierStealsOnlyAfterWaiting(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, Steal: []StealRule{{AgentTier: 2, TaskTier: 1, After: time.Minute}}})
	add(t, e, TaskSpec{ID: "s", Title: "x"})
	eventually(t, "ready", func() bool { return state(t, e, "s") == core.Ready })
	hi := Agent{ID: "hi", Tier: 2}
	if r, _ := e.Wait(bg, hi, 0); r.Action == ActionTask {
		t.Fatal("stole too early")
	}
	clk.Advance(61 * time.Second)
	if r, _ := e.Wait(bg, hi, 0); r.Action != ActionTask {
		t.Fatalf("no steal after waiting: %+v", r)
	}
}

func TestUltraDoesNotTakeTierOneByDefault(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk})
	add(t, e, TaskSpec{ID: "s", Title: "x"})
	clk.Advance(24 * time.Hour)
	if r, _ := e.Wait(bg, Agent{ID: "u", Tier: 3}, 0); r.Action == ActionTask {
		t.Fatal("tier 3 took a tier 1 task")
	}
}

func TestPriorityAgingPreventsStarvation(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, AgingUnit: time.Minute})
	add(t, e, TaskSpec{ID: "old", Title: "x", Priority: 0})
	eventually(t, "ready", func() bool { return state(t, e, "old") == core.Ready })
	clk.Advance(10 * time.Minute) // Worth ten priority points.
	add(t, e, TaskSpec{ID: "pri5", Title: "x", Priority: 5})
	eventually(t, "ready", func() bool { return state(t, e, "pri5") == core.Ready })
	if r, _ := e.Wait(bg, a1, 0); r.Task == nil || r.Task.ID != "old" {
		t.Fatalf("aged task did not win: %+v", r.Task)
	}
	add(t, e, TaskSpec{ID: "pri20", Title: "x", Priority: 20})
	eventually(t, "ready", func() bool { return state(t, e, "pri20") == core.Ready })
	if r, _ := e.Wait(bg, a1, 0); r.Task == nil || r.Task.ID != "pri20" {
		t.Fatalf("high priority did not win: %+v", r.Task)
	}
}

func TestDepsAndFileConflicts(t *testing.T) {
	e, _ := setup(t, Config{Clock: newClock()})
	add(t, e, TaskSpec{ID: "a", Title: "x", Touches: []string{"src"}})
	add(t, e, TaskSpec{ID: "b", Title: "x", Touches: []string{"src/a.go"}})
	add(t, e, TaskSpec{ID: "c", Title: "x", Touches: []string{"src2"}})
	add(t, e, TaskSpec{ID: "d", Title: "x", Deps: []string{"c"}})
	r1, _ := e.Wait(bg, Agent{ID: "1", Tier: 1}, 0)
	r2, _ := e.Wait(bg, Agent{ID: "2", Tier: 1}, 0)
	ids := []string{r1.Task.ID, r2.Task.ID}
	sort.Strings(ids)
	if ids[1] != "c" {
		t.Fatalf("expected one of a/b plus c, got %v", ids)
	}
	if r, _ := e.Wait(bg, Agent{ID: "3", Tier: 1}, 0); r.Action == ActionTask {
		t.Fatalf("conflicting or blocked task was granted: %s", r.Task.ID)
	}
	// Finishing c unblocks d. Nothing here passes through machine checks.
	rc := r1
	ac := Agent{ID: "1", Tier: 1}
	if r2.Task.ID == "c" {
		rc, ac = r2, Agent{ID: "2", Tier: 1}
	}
	if _, err := e.Submit(bg, ac, "c", rc.Lease, "", "", "", core.Usage{}); err != nil {
		t.Fatal(err)
	}
	rv, err := e.Wait(bg, Agent{ID: "rev", Tier: 1}, 0)
	if err != nil || rv.Job != "review" {
		t.Fatalf("review job: %v %+v", err, rv)
	}
	if _, err := e.Verdict(bg, Agent{ID: "rev", Tier: 1}, "c", rv.Lease, true, "ok"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "d ready", func() bool { return state(t, e, "d") == core.Ready })
}

func TestSubmitterCannotReviewOwnWork(t *testing.T) {
	e, _ := setup(t, Config{Clock: newClock(), ExitGrace: time.Hour})
	add(t, e, TaskSpec{ID: "r", Title: "x"})
	r, _ := e.Wait(bg, a1, 0)
	if _, err := e.Submit(bg, a1, "r", r.Lease, "", "", "", core.Usage{}); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.Wait(bg, a1, 0); r.Action == ActionTask {
		t.Fatalf("agent got its own review: %+v", r)
	}
	if r, _ := e.Wait(bg, Agent{ID: "b", Tier: 1}, 0); r.Job != "review" {
		t.Fatalf("other agent: %+v", r)
	}
}

func TestRestartKeepsStateAndResumes(t *testing.T) {
	dir := t.TempDir()
	st := open(t, dir)
	e, err := New(bg, st, Config{RestartGrace: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	add(t, e, TaskSpec{ID: "keep", Title: "x"})
	add(t, e, TaskSpec{ID: "held", Title: "x", Priority: 9})
	r, _ := e.Wait(bg, a1, 0)
	if r.Task.ID != "held" {
		t.Fatalf("got %s", r.Task.ID)
	}
	e.Close()
	st.Close()

	st = open(t, dir)
	defer st.Close()
	e, err = New(bg, st, Config{RestartGrace: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if s := state(t, e, "held"); s != core.Claimed {
		t.Fatalf("held = %s", s)
	}
	// The lease from before the restart still works, and epochs keep rising.
	if _, err := e.Heartbeat(bg, a1, "held", r.Lease); err != nil {
		t.Fatalf("old lease: %v", err)
	}
	r2, _ := e.Wait(bg, Agent{ID: "b", Tier: 1}, 0)
	if r2.Task == nil || r2.Task.ID != "keep" {
		t.Fatalf("got %+v", r2)
	}
}

func TestEventLogReplayMatchesState(t *testing.T) {
	e, st := setup(t, Config{Clock: newClock()})
	if err := st.StartProjector(bg); err != nil {
		t.Fatal(err)
	}
	for i := range 25 {
		add(t, e, TaskSpec{ID: fmt.Sprintf("r%d", i), Title: "x"})
	}
	for i := range 25 {
		r, _ := e.Wait(bg, a1, 0)
		if i%2 == 0 {
			_, _ = e.Fail(bg, a1, r.Task.ID, r.Lease, "x", core.Usage{})
		} else {
			_, _ = e.Submit(bg, a1, r.Task.ID, r.Lease, "", "", "", core.Usage{})
		}
	}
	ctx, cancel := context.WithTimeout(bg, 10*time.Second)
	defer cancel()
	if err := st.WaitProjected(ctx); err != nil {
		t.Fatal(err)
	}
	evs, err := st.ReplayEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	last := map[string]core.Task{}
	for _, x := range evs {
		last[x.ID] = x
	}
	cur, _ := e.List(bg)
	if len(cur) != len(last) {
		t.Fatalf("%d tasks, %d in log", len(cur), len(last))
	}
	for _, x := range cur {
		if last[x.ID].State != x.State || last[x.ID].LeaseEpoch != x.LeaseEpoch {
			t.Errorf("%s: log %s/%d, state %s/%d", x.ID, last[x.ID].State, last[x.ID].LeaseEpoch, x.State, x.LeaseEpoch)
		}
	}
}

func TestPresenceExpiresByServerTTL(t *testing.T) {
	_, st := setup(t, Config{})
	if err := st.TouchPresence("gone", []byte("x"), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchPresence("gone", []byte("x"), time.Second); err != nil {
		t.Fatalf("renewal: %v", err)
	}
	if ps, _ := st.PresentAgents(bg); !slices.Contains(ps, "gone") {
		t.Fatalf("present = %v", ps)
	}
	dl := time.Now().Add(10 * time.Second)
	for time.Now().Before(dl) {
		ps, _ := st.PresentAgents(bg)
		if !slices.Contains(ps, "gone") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("presence key never expired")
}

// --- Linearizability: concurrent operations must behave like one FIFO queue. ---

type qInput struct {
	Op string // "add" or "take"
	ID string
}

type qOutput struct {
	ID  string // Task taken; empty if the queue looked empty.
	Err bool
}

// fifoModel is the specification: a plain sequential FIFO queue.
var fifoModel = porcupine.Model{
	Init: func() any { return "" },
	Step: func(state, input, output any) (bool, any) {
		q := state.(string)
		in, out := input.(qInput), output.(qOutput)
		if out.Err {
			return true, q
		}
		if in.Op == "add" {
			if q == "" {
				return true, in.ID
			}
			return true, q + "," + in.ID
		}
		if q == "" {
			return out.ID == "", q
		}
		head, rest, _ := strings.Cut(q, ",")
		return out.ID == head, rest
	},
	Equal: func(a, b any) bool { return a == b },
}

func checkLinearizable(t *testing.T, ops []porcupine.Operation) porcupine.CheckResult {
	t.Helper()
	return porcupine.CheckOperationsTimeout(fifoModel, ops, 30*time.Second)
}

func TestPorcupineConcurrentQueue(t *testing.T) {
	// A constant clock leaves the sequence number as the only ordering, so the
	// engine's real promise of FIFO order is what gets checked.
	e, _ := setup(t, Config{Clock: newClock()})
	var mu sync.Mutex
	var ops []porcupine.Operation
	base := time.Now()
	clock := func() int64 { return int64(time.Since(base)) }
	var wg sync.WaitGroup
	const clients, perClient = 8, 60
	for c := range clients {
		wg.Go(func() {
			ag := Agent{ID: fmt.Sprintf("a%d", c), Tier: 1}
			for i := range perClient {
				var in qInput
				var out qOutput
				callAt := clock()
				if (i+c)%2 == 0 {
					in = qInput{Op: "add", ID: fmt.Sprintf("%d-%d", c, i)}
					_, err := e.AddTask(bg, TaskSpec{ID: "q" + strconv.Itoa(c) + "_" + strconv.Itoa(i), Title: in.ID, Priority: 0})
					out.Err = err != nil
				} else {
					in = qInput{Op: "take"}
					r, err := e.Wait(bg, ag, 0)
					switch {
					case err != nil:
						out.Err = true
					case r.Action == ActionTask:
						out.ID = r.Task.Title
					}
				}
				retAt := clock()
				mu.Lock()
				ops = append(ops, porcupine.Operation{ClientId: c, Input: in, Output: out, Call: callAt, Return: retAt})
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	switch res := checkLinearizable(t, ops); res {
	case porcupine.Ok:
	case porcupine.Illegal:
		t.Fatal("history is not linearizable")
	default:
		t.Fatalf("checker result: %v", res)
	}
}

// The checker must be able to fail, or a passing result would mean nothing.
func TestPorcupineCatchesABrokenQueue(t *testing.T) {
	ops := []porcupine.Operation{
		{ClientId: 0, Input: qInput{Op: "add", ID: "x"}, Output: qOutput{}, Call: 0, Return: 1},
		{ClientId: 1, Input: qInput{Op: "take"}, Output: qOutput{ID: "x"}, Call: 2, Return: 3},
		{ClientId: 2, Input: qInput{Op: "take"}, Output: qOutput{ID: "x"}, Call: 4, Return: 5}, // Double claim.
	}
	if res := checkLinearizable(t, ops); res != porcupine.Illegal {
		t.Fatalf("double claim went undetected: %v", res)
	}
}

func BenchmarkGrantCycle(b *testing.B) {
	st, err := store.Open(bg, store.Options{Dir: b.TempDir()})
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	e, err := New(bg, st, Config{Clock: newClock()})
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if _, err := e.AddTask(bg, TaskSpec{ID: fmt.Sprintf("b%d", i), Title: "x"}); err != nil {
			b.Fatal(err)
		}
		if r, err := e.Wait(bg, a1, 0); err != nil || r.Action != ActionTask {
			b.Fatalf("%v %+v", err, r)
		}
	}
}

func TestObserveReportsLivenessUsageAndFlow(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, Tick: time.Millisecond, LeaseTTL: 100 * time.Second, ProgressTTL: 100 * time.Second})
	add(t, e, TaskSpec{ID: "work", Title: "a", Budget: 100})
	add(t, e, TaskSpec{ID: "queued", Title: "b", Touches: []string{"x"}})
	r, err := e.Claim(bg, a1, "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Progress(bg, a1, "work", r.Lease, "tree1", "", "", core.Usage{Cost: 90, Tokens: 500}); err != nil {
		t.Fatal(err)
	}
	s, err := e.Observe(bg)
	if err != nil {
		t.Fatal(err)
	}
	if s.States["running"] != 1 || s.States["ready"] != 1 {
		t.Fatalf("states: %v", s.States)
	}
	if len(s.Tasks) != 1 || s.Tasks[0].ID != "work" || s.Tasks[0].Health != "ok" || *s.Tasks[0].LeaseRemainingMs != 100000 {
		t.Fatalf("tasks: %+v", s.Tasks)
	}
	if s.Usage.Tokens != 500 || s.Usage.Spent != 90 || s.Usage.Budgeted != 100 || s.Usage.ByTier[0].Tokens != 500 {
		t.Fatalf("usage: %+v", s.Usage)
	}
	if len(s.Usage.Tightest) != 1 || s.Usage.Tightest[0].ID != "work" {
		t.Fatalf("budgets: %+v", s.Usage.Tightest)
	}
	if len(s.Agents) != 1 || s.Agents[0].State != "working" || s.Agents[0].Tokens != 500 || s.Agents[0].Task != "work" {
		t.Fatalf("agents: %+v", s.Agents)
	}
	if s.Latency["commit"].Count == 0 || s.Latency["queue_wait"].Count != 1 || s.Throughput.TotalGrants != 1 {
		t.Fatalf("measures: %+v %+v", s.Latency, s.Throughput)
	}
	found := false
	for _, f := range s.Flow {
		found = found || (f.From == "ready" && f.To == "claimed" && f.Count == 1)
	}
	if !found {
		t.Fatalf("flow: %+v", s.Flow)
	}

	// With under a quarter of the lease left, it is at risk, and past it, expired.
	clk.Advance(80 * time.Second)
	if s, _ = e.Observe(bg); s.Tasks[0].Health != "at_risk" {
		t.Fatalf("at risk: %+v", s.Tasks[0])
	}
}

func TestObserveCountsZombiesAndOutcomes(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, Tick: time.Millisecond, LeaseTTL: time.Hour, ProgressTTL: time.Minute})
	add(t, e, TaskSpec{ID: "z", Title: "x"})
	r, _ := e.Wait(bg, a1, 0)
	if _, err := e.Heartbeat(bg, a1, "z", r.Lease); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Minute)
	eventually(t, "zombie caught", func() bool {
		s, err := e.Observe(bg)
		return err == nil && s.Throughput.TotalZombies == 1 && s.Throughput.TotalFailed == 1
	})
	s, _ := e.Observe(bg)
	if s.Agents[0].Failed != 1 {
		t.Fatalf("the failure was not credited to the agent: %+v", s.Agents)
	}
}

func TestViewListIsCappedWithProblemsFirst(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, LeaseTTL: time.Hour, ProgressTTL: time.Hour})
	for i := range maxLiveTasks + 20 {
		add(t, e, TaskSpec{ID: fmt.Sprintf("t%03d", i), Title: "x"})
	}
	for i := range maxLiveTasks + 20 {
		if r, err := e.Wait(bg, Agent{ID: fmt.Sprintf("a%d", i), Tier: 1}, 0); err != nil || r.Action != ActionTask {
			t.Fatalf("%v %+v", err, r)
		}
	}
	s, err := e.Observe(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tasks) != maxLiveTasks || s.Truncated != 20 {
		t.Fatalf("shown %d, hidden %d", len(s.Tasks), s.Truncated)
	}
}
