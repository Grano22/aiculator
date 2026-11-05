package swarm

import (
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/pkg/observe"
)

func link(ls []observe.Link, from, to string) int {
	for _, l := range ls {
		if l.From == from && l.To == to {
			return l.Count
		}
	}
	return 0
}

// The analytics come from the durable event log: they see every move, and a restart reads
// them all back.
func TestAnalyticsFollowTheEventLogAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	open := func() (*Service, *store.Store) {
		st, err := store.Open(bg, store.Options{Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.StartProjector(bg); err != nil {
			t.Fatal(err)
		}
		s, err := New(bg, Options{Store: st, Engine: engine.Config{RestartGrace: time.Nanosecond}})
		if err != nil {
			t.Fatal(err)
		}
		return s, st
	}
	s, st := open()
	a := engine.Agent{ID: "w", Tier: 1}
	for _, id := range []string{"one", "two"} {
		if _, err := s.Engine.AddTask(bg, engine.TaskSpec{ID: id, Title: id, Labels: map[string]string{"team": "web"}}); err != nil {
			t.Fatal(err)
		}
	}
	// One fails and is tried again; the other is canceled.
	r, err := s.Engine.Claim(bg, a, "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.Fail(bg, a, "one", r.Lease, "", core.Usage{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.Cancel(bg, "two", "not needed"); err != nil {
		t.Fatal(err)
	}
	wait := func(s *Service, what string, ok func(observe.Analytics) bool) observe.Analytics {
		t.Helper()
		var got observe.Analytics
		for range 400 {
			if got = s.Analytics(observe.Query{}); got.Ready && ok(got) {
				return got
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s: %+v", what, got)
		return got
	}
	check := func(a observe.Analytics) bool {
		return link(a.Reasons, "failed", "agent_reported_failure") == 1 && link(a.States, "ready", "canceled") == 1 &&
			link(a.Reasons, "claimed", "failed") == 1
	}
	got := wait(s, "live", check)
	if got.Tasks != 2 || len(got.Labels) != 1 {
		t.Errorf("tasks %d labels %v", got.Tasks, got.Labels)
	}
	if f := s.Analytics(observe.Query{Label: "team=nobody"}); f.Tasks != 0 {
		t.Errorf("filter: %d", f.Tasks)
	}
	s.Close()
	st.Close()

	s2, st2 := open()
	defer func() { s2.Close(); st2.Close() }()
	wait(s2, "after restart", check)
}
