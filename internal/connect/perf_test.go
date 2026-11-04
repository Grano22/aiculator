//go:build !race

package connect

import (
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/engine"
)

func pct(d []time.Duration, p int) time.Duration {
	slices.Sort(d)
	return d[min(len(d)*p/100, len(d)-1)]
}

// How fast a commit reaches the other system, leaving out the other system's own time.
func TestPerfRealtimeSyncLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("performance test")
	}
	f := fix(t, &SyncConfig{Status: statuses, Direction: "push"})
	f.start()
	const n = 500
	lat := make([]time.Duration, 0, n)
	for i := range n {
		id := f.imp(fmt.Sprintf("K-%d", i), "To Do")
		start := time.Now()
		if _, err := f.svc.Engine.Cancel(bg, id, "x"); err != nil {
			t.Fatal(err)
		}
		wait(t, "the push", func() bool { return len(f.fake.pushed()) == i+1 })
		lat = append(lat, time.Since(start))
	}
	limit := 10 * time.Millisecond
	if os.Getenv("CI") != "" {
		limit = 100 * time.Millisecond
	}
	t.Logf("commit to remote call: p50 %v, p99 %v (budget %v)", pct(lat, 50), pct(lat, 99), limit)
	if got := pct(lat, 99); got > limit {
		t.Errorf("p99 %v is over %v", got, limit)
	}
}

// Syncing must add nothing measurable to a claim.
func TestPerfSyncCostsClaimsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("performance test")
	}
	measure := func(on bool) time.Duration {
		f := fix(t, &SyncConfig{Status: statuses, Direction: "push"})
		if on {
			f.start()
		}
		const n = 3000
		for i := range n {
			if _, err := f.svc.Engine.AddTask(bg, engine.TaskSpec{ID: fmt.Sprintf("c%d", i), Title: "x", Labels: map[string]string{LabelSource: "fake"}}); err != nil {
				t.Fatal(err)
			}
		}
		lat := make([]time.Duration, 0, n)
		for range n {
			at := time.Now()
			if r, err := f.svc.Engine.Wait(bg, engine.Agent{ID: "a", Tier: 1}, 0); err != nil || r.Task == nil {
				t.Fatalf("%+v %v", r, err)
			}
			lat = append(lat, time.Since(at))
		}
		return pct(lat, 50)
	}
	off, on := measure(false), measure(true)
	t.Logf("claim p50: %v without sync, %v with sync", off, on)
	if on > off*3+200*time.Microsecond {
		t.Errorf("sync made a claim much slower: %v against %v", on, off)
	}
}
