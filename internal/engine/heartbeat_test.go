package engine

import (
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/core"
)

// Heartbeats that only push a deadline stay in memory while the stored one is more than half
// a lease away, and are written once it is not. The lease stays enforced either way, and a
// restart starts from a deadline that is never in the past.
func TestHeartbeatsSkipTheStoreUntilHalfALeaseIsLeft(t *testing.T) {
	for _, durable := range []bool{false, true} {
		clock := newClock()
		dir := t.TempDir()
		st := open(t, dir)
		cfg := Config{Clock: clock, Tick: time.Millisecond, LeaseTTL: time.Minute, RestartGrace: time.Nanosecond, DurableHeartbeats: durable}
		e, err := New(bg, st, cfg)
		if err != nil {
			t.Fatal(err)
		}
		add(t, e, TaskSpec{ID: "job", Title: "x"})
		a := Agent{ID: "w", Tier: 1}
		r, err := e.Wait(bg, a, 0)
		if err != nil || r.Action != ActionTask {
			t.Fatal(r, err)
		}
		lease := r.Task.LeaseEpoch
		beat := func() core.Task {
			t.Helper()
			n, err := e.Heartbeat(bg, a, "job", lease)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		stored := func() (core.Task, uint64) {
			x, rev, err := st.GetTask("job")
			if err != nil {
				t.Fatal(err)
			}
			return x, rev
		}
		// The first moves Claimed to Running, so it is written.
		if n := beat(); n.State != core.Running {
			t.Fatalf("first heartbeat: %s", n.State)
		}
		_, rev := stored()
		writes := 0
		for range 10 {
			clock.Advance(10 * time.Second)
			mem := beat()
			got, r2 := stored()
			if r2 != rev {
				writes++
				rev = r2
			}
			if !mem.LeaseDeadline.Equal(clock.Now().Add(time.Minute)) {
				t.Errorf("memory deadline %v", mem.LeaseDeadline)
			}
			// What a restart would load always leaves at least half a lease.
			if got.LeaseDeadline.Sub(clock.Now()) < 30*time.Second-time.Nanosecond {
				t.Errorf("stored deadline %v is too close at %v", got.LeaseDeadline, clock.Now())
			}
		}
		if durable && writes != 10 {
			t.Errorf("durable: %d writes, want 10", writes)
		}
		if !durable && (writes == 0 || writes > 4) {
			t.Errorf("in memory: %d writes of 10 heartbeats, want 1 to 4", writes)
		}
		if !durable && e.Collector().Read(time.Now()).Throughput.HeartbeatsInMemory == 0 {
			t.Error("the metric did not count")
		}
		// A lease kept in memory still expires.
		clock.Advance(2 * time.Minute)
		eventually(t, "expiry", func() bool { x, _ := e.Get(bg, "job"); return x.Owner == "" && x.State != core.Running })
		e.Close()
		st.Close()
	}
}
