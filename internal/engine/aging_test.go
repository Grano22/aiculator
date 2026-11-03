package engine

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/core"
)

func grantedID(t *testing.T, e *Engine) string {
	t.Helper()
	r, err := e.Wait(bg, a1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != ActionTask {
		t.Fatalf("no task: %+v", r)
	}
	return r.Task.ID
}

// The tick applies boosts on real time, so a test waits for the boost to land.
func awaitPriority(t *testing.T, e *Engine, id string, want int) {
	t.Helper()
	eventually(t, "priority of "+id, func() bool {
		p, err := e.Priority(bg, id)
		return err == nil && p.Effective == want
	})
	if err := e.Sync(bg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * e.cfg.Tick)
}

func TestAgingStepRaisesAWaitingTask(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, AgingUnit: 24 * time.Hour})
	add(t, e, TaskSpec{ID: "slow", Title: "x", Priority: 1, Aging: []core.AgingStep{{AfterSec: 3600, Boost: 50}}})
	clk.Advance(time.Second)
	add(t, e, TaskSpec{ID: "fast", Title: "x", Priority: 10})
	clk.Advance(time.Hour)
	awaitPriority(t, e, "slow", 51)
	if got := grantedID(t, e); got != "slow" {
		t.Fatalf("the waiting task should be first, got %s", got)
	}
}

func TestServerAgingStepsAndTaskOverride(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, AgingUnit: 24 * time.Hour, Aging: []core.AgingStep{{AfterSec: 60, Boost: 5}, {AfterSec: 120, Boost: 20}}})
	add(t, e, TaskSpec{ID: "server", Title: "x"})
	none := []core.AgingStep{}
	add(t, e, TaskSpec{ID: "own", Title: "x", Aging: none})
	clk.Advance(time.Minute)
	awaitPriority(t, e, "server", 5)
	clk.Advance(time.Minute)
	awaitPriority(t, e, "server", 20) // The highest step counts: not 25.
	if p, _ := e.Priority(bg, "own"); p.Effective != 0 {
		t.Fatalf("a task's own empty list should turn aging off: %+v", p)
	}
}

func TestRestartRebuildsBoostsWithoutWrites(t *testing.T) {
	clk := newClock()
	dir := t.TempDir()
	st := open(t, dir)
	e, err := New(bg, st, Config{Clock: clk, RestartGrace: time.Nanosecond, AgingUnit: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	add(t, e, TaskSpec{ID: "slow", Title: "x", Priority: 1, Aging: []core.AgingStep{{AfterSec: 60, Boost: 50}}})
	clk.Advance(time.Second)
	x := add(t, e, TaskSpec{ID: "fast", Title: "x", Priority: 10})
	e.Close()
	st.Close()
	clk.Advance(time.Hour) // The step passes while the server is down.
	st2 := open(t, dir)
	e2, err := New(bg, st2, Config{Clock: clk, RestartGrace: time.Nanosecond, AgingUnit: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { e2.Close(); st2.Close() }()
	if got := grantedID(t, e2); got != "slow" {
		t.Fatalf("got %s", got)
	}
	if after, _ := e2.Get(bg, "fast"); after.UpdatedAt != clk.Now() && after.UpdatedAt.After(x.UpdatedAt.Add(2*time.Second)) {
		t.Fatalf("aging should write nothing: %+v", after)
	}
}

func TestBoostForAClaimedTaskIsDropped(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, AgingUnit: 24 * time.Hour, LeaseTTL: 24 * time.Hour, ProgressTTL: 24 * time.Hour})
	add(t, e, TaskSpec{ID: "t", Title: "x", Aging: []core.AgingStep{{AfterSec: 60, Boost: 50}}})
	if got := grantedID(t, e); got != "t" {
		t.Fatal(got)
	}
	clk.Advance(time.Hour)
	if err := e.Sync(bg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * e.cfg.Tick)
	if r, err := e.Wait(bg, Agent{ID: "b", Tier: 1}, 0); err != nil || r.Action == ActionTask {
		t.Fatalf("a claimed task came back through its boost: %+v %v", r, err)
	}
}

func TestAmendChangesPriorityAndKeepsTheLease(t *testing.T) {
	clk := newClock()
	e, _ := setup(t, Config{Clock: clk, AgingUnit: 24 * time.Hour})
	add(t, e, TaskSpec{ID: "a", Title: "x", Priority: 5})
	add(t, e, TaskSpec{ID: "b", Title: "x", Priority: 1, Labels: map[string]string{"keep": "1", "drop": "2"}})
	hi, title := 50, "new title"
	n, err := e.Amend(bg, "b", "system:test", core.Amendment{Priority: &hi, Title: &title, Labels: map[string]string{"drop": "", "add": "3"}})
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != title || n.Labels["keep"] != "1" || n.Labels["add"] != "3" || n.Labels["drop"] != "" {
		t.Fatalf("%+v", n)
	}
	r, err := e.Wait(bg, a1, 0)
	if err != nil || r.Task == nil || r.Task.ID != "b" {
		t.Fatalf("the new priority should win: %+v %v", r, err)
	}
	spec := "changed"
	if _, err := e.Amend(bg, "b", "system:test", core.Amendment{Spec: &spec}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Heartbeat(bg, a1, "b", r.Lease); err != nil {
		t.Fatalf("amending must not cost the holder its lease: %v", err)
	}
	if _, err := e.Amend(bg, "a", "agent-1", core.Amendment{Title: &title}); err == nil {
		t.Fatal("an agent amended a task")
	}
	bad := core.MaxPriority + 1
	if _, err := e.Amend(bg, "a", "system:test", core.Amendment{Priority: &bad}); err == nil {
		t.Fatal("an out of range priority was accepted")
	}
}

func TestBadAttributesAreRefused(t *testing.T) {
	e, _ := setup(t, Config{})
	long := make([]byte, core.MaxLabelValue+1)
	for i, s := range []TaskSpec{
		{Labels: map[string]string{"bad key": "x"}},
		{Labels: map[string]string{"k": string(long)}},
		{Priority: core.MaxPriority + 1},
		{Aging: []core.AgingStep{{AfterSec: 0, Boost: 1}}},
		{Aging: []core.AgingStep{{AfterSec: 60, Boost: 1}, {AfterSec: 30, Boost: 2}}},
	} {
		s.Title = "x"
		if _, err := e.AddTask(bg, s); err == nil {
			t.Errorf("case %d was accepted", i)
		}
	}
}

// The order agents see must match a brute-force sort over every waiting task, whatever
// mix of priorities, steps and waits there is. This is what makes the lazy heap safe.
func TestGrantOrderMatchesBruteForce(t *testing.T) {
	for seed := range uint64(6) {
		r := rand.New(rand.NewPCG(seed, 7))
		clk := newClock()
		unit := time.Minute
		e, st := setup(t, Config{Clock: clk, AgingUnit: unit, Tick: time.Millisecond, LeaseTTL: 1000 * time.Hour, ProgressTTL: 1000 * time.Hour})
		type want struct {
			id    string
			prio  int
			aging []core.AgingStep
			ready time.Time
		}
		var tasks []want
		for i := range 60 {
			w := want{id: fmt.Sprintf("t%d", i), prio: r.IntN(20)}
			at := int64(0)
			for range r.IntN(4) {
				at += 1 + r.Int64N(1800)
				w.aging = append(w.aging, core.AgingStep{AfterSec: at, Boost: r.IntN(40)})
			}
			add(t, e, TaskSpec{ID: w.id, Title: "x", Priority: w.prio, Aging: w.aging})
			w.ready = clk.Now()
			tasks = append(tasks, w)
			clk.Advance(time.Duration(r.IntN(120)) * time.Second)
		}
		for len(tasks) > 0 {
			clk.Advance(time.Duration(r.IntN(600)) * time.Second)
			if err := e.Sync(bg); err != nil {
				t.Fatal(err)
			}
			time.Sleep(3 * e.cfg.Tick) // Let the writer's tick apply what has come due.
			now := clk.Now()
			key := func(w want) int64 {
				b, _ := core.AgingBoost(w.aging, now.Sub(w.ready))
				return int64(w.prio+b)*int64(unit) - w.ready.UnixNano()
			}
			best := slices.MaxFunc(tasks, func(a, b want) int {
				switch ka, kb := key(a), key(b); {
				case ka < kb:
					return -1
				case ka > kb:
					return 1
				}
				return 0
			})
			got := grantedID(t, e)
			if key(tasks[slices.IndexFunc(tasks, func(w want) bool { return w.id == got })]) != key(best) {
				t.Fatalf("seed %d: granted %s, but %s ranks first", seed, got, best.id)
			}
			tasks = slices.DeleteFunc(tasks, func(w want) bool { return w.id == got })
		}
		e.Close()
		st.Close()
	}
}
