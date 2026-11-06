package observe

import (
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Unix(1_800_000_000, 0)

func TestRatesAndHistory(t *testing.T) {
	c := New(t0)
	for i := range 30 { // 2 per second for 30 seconds.
		c.Count(SeriesDone, t0.Add(time.Duration(i)*time.Second), 2)
	}
	m := c.Read(t0.Add(30 * time.Second))
	if m.Throughput.TotalDone != 60 {
		t.Fatalf("total: %d", m.Throughput.TotalDone)
	}
	if got := m.Throughput.DonePerMin; got < 100 || got > 130 {
		t.Fatalf("rate: %v, want about 120", got)
	}
	h := m.Throughput.Done
	if len(h) != 60 || h[59] != 2 || h[0] != 0 {
		t.Fatalf("history: %v", h)
	}
	// Old data drops out of the rate but stays in the total.
	later := c.Read(t0.Add(10 * time.Minute))
	if later.Throughput.DonePerMin != 0 || later.Throughput.TotalDone != 60 {
		t.Fatalf("after ten minutes: %+v", later.Throughput)
	}
}

// A server a second old must not turn two events into a rate of 120 a minute.
func TestRatesOfAYoungServerAreNotBlownUp(t *testing.T) {
	c := New(t0)
	c.Count(SeriesGrants, t0, 2)
	if got := c.Read(t0.Add(time.Second)).Throughput.GrantsPerMin; got != 4 {
		t.Fatalf("rate after one second: %v, want 4 (2 over 30 seconds)", got)
	}
}

func TestRingDoesNotConfuseOldBucketsWithNew(t *testing.T) {
	c := New(t0)
	c.Count(SeriesDone, t0, 5)
	// Exactly one ring length later, the same slot must not still hold the old count.
	at := t0.Add(Window * time.Second)
	c.Count(SeriesDone, at, 1)
	if h := c.Read(at.Add(time.Second)).Throughput.Done; h[59] != 1 {
		t.Fatalf("history: %v", h)
	}
}

func TestLatencyPercentilesAndJitter(t *testing.T) {
	c := New(t0)
	for i := 1; i <= 100; i++ {
		c.Observe(LatencyCommit, time.Duration(i)*time.Millisecond)
	}
	st := c.Read(t0).Latency[LatencyCommit]
	if st.Count != 100 || st.P50 != 51000 || st.P99 != 100000 || st.Max != 100000 {
		t.Fatalf("stats: %+v", st)
	}
	// Steady samples have almost no jitter; alternating ones have a lot.
	steady, wobbly := New(t0), New(t0)
	for i := range 200 {
		steady.Observe("x", 10*time.Millisecond)
		wobbly.Observe("x", time.Duration(10+i%2*20)*time.Millisecond)
	}
	if j := steady.Read(t0).Latency["x"].Jitter; j != 0 {
		t.Fatalf("steady jitter: %d", j)
	}
	if j := wobbly.Read(t0).Latency["x"].Jitter; j < 15000 || j > 21000 {
		t.Fatalf("wobbly jitter: %d, want near 20000", j)
	}
}

func TestReservoirStaysBounded(t *testing.T) {
	c := New(t0)
	for range reservoir * 3 {
		c.Observe("x", time.Millisecond)
	}
	if n := len(c.lat["x"].samples); n != reservoir {
		t.Fatalf("kept %d samples", n)
	}
	if c.Read(t0).Latency["x"].Count != reservoir*3 {
		t.Fatal("count was lost")
	}
}

func TestFlowAndAgents(t *testing.T) {
	c := New(t0)
	c.Transition("ready", "claimed")
	c.Transition("ready", "claimed")
	c.Transition("claimed", "running")
	c.Seen("a", 2, t0)
	c.Usage("a", 100, 5)
	c.Outcome("a", true)
	c.Outcome("a", false)
	m := c.Read(t0)
	if m.Flow[0] != (FlowEdge{"ready", "claimed", 2}) {
		t.Fatalf("flow: %+v", m.Flow)
	}
	a := m.Agents["a"]
	if a.Tier != 2 || a.Tokens != 100 || a.Cost != 5 || a.Done != 1 || a.Failed != 1 || !m.LastSeen["a"].Equal(t0) {
		t.Fatalf("agent: %+v", a)
	}
}

func TestConcurrentUse(t *testing.T) {
	c := New(t0)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for i := range 500 {
				c.Count(SeriesCommits, t0, 1)
				c.Observe(LatencyCommit, time.Duration(i))
				c.Transition("a", "b")
				c.Usage("x", 1, 1)
				_ = c.Read(t0)
			}
		})
	}
	wg.Wait()
	if c.Read(t0).Throughput.Commits == nil || c.Read(t0).Agents["x"].Tokens != 4000 {
		t.Fatal("lost updates")
	}
}

func TestPrometheusFormat(t *testing.T) {
	s := Snapshot{States: map[string]int{"done": 3}, Latency: map[string]Stats{"commit": {P99: 1500}}, Probes: []Probe{{Name: "store", OK: true}, {Name: "git"}}}
	s.Throughput.TotalDone = 7
	out := s.Prometheus()
	for _, want := range []string{`aiculator_tasks{state="done"} 3`, "aiculator_done_total 7", `aiculator_latency_seconds{op="commit",stat="p99"} 0.0015`, `aiculator_probe_ok{probe="store"} 1`, `aiculator_probe_ok{probe="git"} 0`, "# TYPE aiculator_done_total counter"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
