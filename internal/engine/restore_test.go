package engine

import (
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/core"
)

// A restore puts the copy back, but never in flight, never as an unverified Done, and with
// a lease no agent holds, while keeping what it replaced.
func TestRestore(t *testing.T) {
	st := open(t, t.TempDir())
	defer st.Close()
	e, err := New(bg, st, Config{RestartGrace: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	add(t, e, TaskSpec{ID: "job", Title: "the original title"})
	copyOf, err := e.Get(bg, "job")
	if err != nil {
		t.Fatal(err)
	}

	// An agent takes it, and the title is changed under it.
	a := Agent{ID: "w", Tier: 1}
	r, err := e.Wait(bg, a, 0)
	if err != nil || r.Action != ActionTask {
		t.Fatalf("claim: %+v %v", r, err)
	}
	held := r.Task.LeaseEpoch
	inFlight := *r.Task
	inFlight.Title = "the copy taken while it was claimed"

	res, err := e.Restore(bg, inFlight)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replaced || res.Task.State != core.Ready || res.Task.Owner != "" || len(res.Did) == 0 {
		t.Errorf("an in-flight copy: %+v", res)
	}
	if res.Task.LeaseEpoch <= held {
		t.Errorf("lease %d did not move past %d", res.Task.LeaseEpoch, held)
	}
	if _, err := e.Heartbeat(bg, a, "job", held); err == nil {
		t.Error("the old lease still works after a restore")
	}
	q, err := st.Quarantined(bg)
	if err != nil || len(q) != 1 {
		t.Errorf("the replaced record was not kept: %v %v", q, err)
	}

	// The original copy goes back, and is handed out again.
	if res, err = e.Restore(bg, copyOf); err != nil || res.Task.Title != "the original title" {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if r, err := e.Wait(bg, Agent{ID: "v", Tier: 1}, 0); err != nil || r.Action != ActionTask || r.Task.ID != "job" {
		t.Errorf("the restored task is not handed out: %+v %v", r, err)
	}

	// A Done copy with nobody who verified it goes to a person.
	fake := copyOf
	fake.ID, fake.State = "gone", core.Done
	if res, err = e.Restore(bg, fake); err != nil || res.Replaced || res.Task.State != core.Parked {
		t.Errorf("an unverified done copy of a task that no longer exists: %+v %v", res, err)
	}
	if _, err := e.Restore(bg, core.Task{ID: "../x"}); err == nil {
		t.Error("a bad id was restored")
	}
}
