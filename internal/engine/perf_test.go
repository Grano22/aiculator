//go:build !race

package engine

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/store"
)

// These tests hold the engine to performance budgets, with large cases that would
// expose work that grows badly with the number of tasks. They are left out under the
// race detector, which distorts timing. Budgets are set loosely enough for a busy CI
// machine, so a failure means a real regression rather than a slow day.

func budget(t *testing.T, name string, got, limit time.Duration) {
	t.Helper()
	t.Logf("%-40s %12v  (budget %v)", name, got.Round(time.Microsecond), limit)
	if got > limit {
		t.Errorf("%s took %v, over the %v budget", name, got, limit)
	}
}

func perfEngine(t *testing.T, cfg Config) (*Engine, *store.Store, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("performance test")
	}
	dir := t.TempDir()
	st := open(t, dir)
	if cfg.RestartGrace == 0 {
		cfg.RestartGrace = time.Nanosecond
	}
	e, err := New(bg, st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close(); st.Close() })
	return e, st, dir
}

func percentile(d []time.Duration, p int) time.Duration {
	slices.Sort(d)
	return d[min(len(d)*p/100, len(d)-1)]
}

// The plan's targets: 5 ms at p99 from ready to grant, 50 ms as the hard limit.
func TestPerfWakeLatency(t *testing.T) {
	e, _, _ := perfEngine(t, Config{})
	const n = 500
	lat := make([]time.Duration, 0, n)
	for i := range n {
		got := make(chan time.Time, 1)
		go func() {
			if _, err := e.Wait(bg, a1, 5*time.Second); err != nil {
				t.Error(err)
			}
			got <- time.Now()
		}()
		for {
			s, err := e.Situation(bg, a1)
			if err != nil {
				t.Fatal(err)
			}
			if s.WaitingAgent[1] == 1 {
				break
			}
			runtime.Gosched()
		}
		start := time.Now()
		add(t, e, TaskSpec{ID: fmt.Sprintf("w%d", i), Title: "x"})
		lat = append(lat, (<-got).Sub(start))
	}
	limit := 5 * time.Millisecond
	if os.Getenv("CI") != "" {
		limit = 50 * time.Millisecond // Shared runners stall; the hard limit still holds.
	}
	t.Logf("p50 %v", percentile(lat, 50))
	budget(t, "add-to-wake p99", percentile(lat, 99), limit)
}

func TestPerfSequentialThroughput(t *testing.T) {
	e, _, _ := perfEngine(t, Config{})
	const n = 5000
	start := time.Now()
	for i := range n {
		add(t, e, TaskSpec{ID: fmt.Sprintf("s%d", i), Title: "x"})
		if r, err := e.Wait(bg, a1, 0); err != nil || r.Action != ActionTask {
			t.Fatalf("%v %+v", err, r)
		}
	}
	took := time.Since(start)
	t.Logf("%.0f add-and-grant cycles per second", float64(n)/took.Seconds())
	budget(t, "one add-and-grant cycle", took/n, 2*time.Millisecond)
}

func TestPerfConcurrentAgents(t *testing.T) {
	e, _, _ := perfEngine(t, Config{})
	const tasks, agents = 5000, 32
	for i := range tasks {
		add(t, e, TaskSpec{ID: fmt.Sprintf("c%d", i), Title: "x"})
	}
	var granted atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
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
				granted.Add(1)
			}
		})
	}
	wg.Wait()
	if granted.Load() != tasks {
		t.Fatalf("granted %d of %d", granted.Load(), tasks)
	}
	budget(t, "one grant among 32 agents", time.Since(start)/tasks, 2*time.Millisecond)
}

// A grant must not get slower because the queue is long.
func TestPerfLargeBacklog(t *testing.T) {
	e, _, _ := perfEngine(t, Config{})
	const n = 20000
	start := time.Now()
	for i := range n {
		add(t, e, TaskSpec{ID: fmt.Sprintf("b%d", i), Title: "x", Priority: i % 7})
	}
	budget(t, "one add with a growing backlog", time.Since(start)/n, 2*time.Millisecond)
	var lat []time.Duration
	for range 1000 {
		s := time.Now()
		if r, err := e.Wait(bg, a1, 0); err != nil || r.Action != ActionTask {
			t.Fatalf("%v %+v", err, r)
		}
		lat = append(lat, time.Since(s))
	}
	budget(t, "grant p99 with 20k waiting", percentile(lat, 99), 10*time.Millisecond)
}

// A large plan with a dependency chain, which stresses draft handling.
func TestPerfLargePlan(t *testing.T) {
	e, _, _ := perfEngine(t, Config{})
	const n = 5000
	ep, err := e.PlanBegin(bg, "p", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := range n {
		s := TaskSpec{ID: fmt.Sprintf("p%d", i), Title: "x"}
		if i > 0 && i%2 == 0 {
			s.Deps = []string{fmt.Sprintf("p%d", i-1)}
		}
		if _, err := e.PlanAdd(bg, "p", ep, s); err != nil {
			t.Fatal(err)
		}
	}
	budget(t, "one plan_add with 5k drafts", time.Since(start)/n, 3*time.Millisecond)
	start = time.Now()
	if err := e.PlanCommit(bg, "p", ep); err != nil {
		t.Fatal(err)
	}
	budget(t, "commit a 5k-task plan", time.Since(start), time.Second)
}

func TestPerfManyWaiters(t *testing.T) {
	e, _, _ := perfEngine(t, Config{MaxHold: time.Minute})
	const n = 2000
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	woken := make(chan struct{}, n)
	for i := range n {
		go func() {
			r, err := e.Wait(ctx, Agent{ID: fmt.Sprintf("w%d", i), Tier: 1}, time.Minute)
			if err == nil && r.Action == ActionTask {
				woken <- struct{}{}
			}
		}()
	}
	for {
		s, err := e.Situation(bg, a1)
		if err != nil {
			t.Fatal(err)
		}
		if s.WaitingAgent[1] == n {
			break
		}
		time.Sleep(time.Millisecond)
	}
	var lat []time.Duration
	for i := range 200 {
		start := time.Now()
		add(t, e, TaskSpec{ID: fmt.Sprintf("m%d", i), Title: "x"})
		<-woken
		lat = append(lat, time.Since(start))
	}
	budget(t, "wake p99 with 2k agents waiting", percentile(lat, 99), 25*time.Millisecond)
}

// Restart reads every task, so its cost is part of how fast a crash heals.
func TestPerfRecovery(t *testing.T) {
	e, st, dir := perfEngine(t, Config{})
	const n = 20000
	for i := range n {
		add(t, e, TaskSpec{ID: fmt.Sprintf("r%d", i), Title: "x"})
	}
	e.Close()
	st.Close()
	start := time.Now()
	st2 := open(t, dir)
	e2, err := New(bg, st2, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { e2.Close(); st2.Close() }()
	if err := e2.Sync(bg); err != nil {
		t.Fatal(err)
	}
	budget(t, "restart with 20k tasks", time.Since(start), 5*time.Second)
	ts, err := e2.List(bg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != n {
		t.Fatalf("recovered %d of %d", len(ts), n)
	}
	for _, x := range ts {
		if x.State != core.Ready {
			t.Fatalf("%s is %s", x.ID, x.State)
		}
	}
}

func TestPerfMemoryPerTask(t *testing.T) {
	e, _, _ := perfEngine(t, Config{})
	const n = 20000
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := range n {
		add(t, e, TaskSpec{ID: fmt.Sprintf("x%d", i), Title: "a task title of ordinary length", Spec: "A spec of ordinary length that says what to do."})
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	per := int64(after.HeapAlloc-before.HeapAlloc) / n
	t.Logf("%d bytes of heap per task", per)
	if per > 16*1024 {
		t.Errorf("%d bytes per task, over the 16 KiB budget", per)
	}
}

// Dashboards call this every second, on the writer goroutine, so it must stay cheap.
func TestPerfObserve(t *testing.T) {
	e, _, _ := perfEngine(t, Config{})
	for i := range 20000 {
		add(t, e, TaskSpec{ID: fmt.Sprintf("o%d", i), Title: "x", Budget: 100})
	}
	var lat []time.Duration
	for range 50 {
		s := time.Now()
		if _, err := e.Observe(bg); err != nil {
			t.Fatal(err)
		}
		lat = append(lat, time.Since(s))
	}
	budget(t, "snapshot with 20k tasks, p99", percentile(lat, 99), 100*time.Millisecond)
}
