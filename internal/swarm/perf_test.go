//go:build !race

package swarm

import (
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/engine"
)

// A big import must not hold up agents already at work.
func TestPerfClaimsStayFastDuringALargeImport(t *testing.T) {
	if testing.Short() {
		t.Skip("performance test")
	}
	s := service(t)
	var items []ImportItem
	const n = 5000
	for i := range n {
		items = append(items, item(fmt.Sprintf("i%d", i)))
	}
	for i := range 20000 {
		if _, err := s.Engine.AddTask(bg, engine.TaskSpec{ID: fmt.Sprintf("q%d", i), Title: "x", Priority: 100}); err != nil {
			t.Fatal(err)
		}
	}
	var lat []time.Duration
	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			at := time.Now()
			if r, err := s.Engine.Wait(bg, engine.Agent{ID: "a", Tier: 1}, 0); err != nil || r.Action != engine.ActionTask {
				t.Errorf("%v %+v", err, r)
				return
			}
			lat = append(lat, time.Since(at))
			time.Sleep(200 * time.Microsecond)
		}
	}()
	start := time.Now()
	res, err := s.Import(bg, items, false)
	took := time.Since(start)
	stop.Store(true)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if res[n-1].Action != "created" {
		t.Fatalf("%+v", res[n-1])
	}
	slices.Sort(lat)
	p50, p99 := lat[len(lat)/2], lat[min(len(lat)*99/100, len(lat)-1)]
	t.Logf("%.0f tasks imported per second; claim during import: p50 %v, p99 %v, over %d claims", float64(n)/took.Seconds(), p50, p99, len(lat))
	if limit := 25 * time.Millisecond; p99 > limit {
		t.Errorf("claim p99 %v during an import, over %v", p99, limit)
	}
}
