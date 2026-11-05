package core

import (
	"strings"
	"testing"
	"time"
)

func TestRepairLeavesValidTasksAlone(t *testing.T) {
	x := Task{ID: "t", Tier: 1, State: Ready, ReadyAt: t0}
	n, did := Repair(x, t0.Add(time.Hour))
	if len(did) != 0 || n.UpdatedAt != x.UpdatedAt {
		t.Fatalf("%v %+v", did, n)
	}
}

func TestRepairCases(t *testing.T) {
	later := t0.Add(time.Hour)
	for name, c := range map[string]struct {
		in    Task
		state State
		says  string
	}{
		"claimed with no lease":    {Task{ID: "t", Tier: 1, State: Claimed, Owner: "a"}, Ready, "without a lease"},
		"running with no owner":    {Task{ID: "t", Tier: 1, State: Running, LeaseEpoch: 2, LeaseDeadline: t0}, Ready, "without a lease"},
		"review with no submitter": {Task{ID: "t", Tier: 1, State: Review}, Ready, "submitter"},
		"self review":              {Task{ID: "t", Tier: 1, State: Review, Submitter: "a", Owner: "a"}, Review, "own work"},
		"merging unverified":       {Task{ID: "t", Tier: 1, State: Merging, Submitter: "a"}, Review, "back to review"},
		"merging verified by self": {Task{ID: "t", Tier: 1, State: Merging, Submitter: "a", Verifier: "a"}, Review, "back to review"},
		"done unverified":          {Task{ID: "t", Tier: 1, State: Done, Submitter: "a"}, Parked, "person"},
		"done with an owner":       {Task{ID: "t", Tier: 1, State: Done, Submitter: "a", Verifier: "b", Owner: "c"}, Done, "owner"},
		"unknown state":            {Task{ID: "t", Tier: 1, State: "exploded"}, Parked, "unknown state"},
		"ready with no time":       {Task{ID: "t", Tier: 1, State: Ready}, Ready, "ready time"},
		"failed with an owner":     {Task{ID: "t", Tier: 1, State: Failed, Owner: "a"}, Failed, "owner removed"},
		"zero tier":                {Task{ID: "t", Tier: 0, State: Draft}, Draft, "tier"},
		"negative counters":        {Task{ID: "t", Tier: 1, State: Draft, Failures: -2}, Draft, "negative"},
		"bad label and priority":   {Task{ID: "t", Tier: 1, State: Draft, Priority: 1 << 30, Labels: map[string]string{"bad key": "x", "ok": "y"}}, Draft, "label"},
		"bad aging":                {Task{ID: "t", Tier: 1, State: Draft, Aging: []AgingStep{{AfterSec: -1}}}, Draft, "aging"},
	} {
		n, did := Repair(c.in, later)
		if err := Validate(n); err != nil {
			t.Errorf("%s: still invalid: %v", name, err)
		}
		if err := ValidateAttrs(n); err != nil {
			t.Errorf("%s: attributes still invalid: %v", name, err)
		}
		if n.State != c.state {
			t.Errorf("%s: state %s, want %s (%v)", name, n.State, c.state, did)
		}
		if !strings.Contains(strings.Join(did, "; ")+" "+n.Reason, c.says) {
			t.Errorf("%s: %v, %q", name, did, n.Reason)
		}
	}
}

// For any damaged record: the result is valid, a second repair changes nothing, the lease
// number never goes back, and a task never becomes done that was not done before.
func FuzzRepair(f *testing.F) {
	f.Add("claimed", "a", "", "", uint64(3), int64(0), -1, 0, 0, false)
	f.Add("done", "", "a", "a", uint64(1), int64(5), 2, 1, 7, true)
	f.Add("bogus", "x", "y", "z", uint64(0), int64(-3), -5, -1, -9, false)
	f.Fuzz(func(t *testing.T, state, owner, submitter, verifier string, lease uint64, deadline int64, tier, failures, attempt int, readyAt bool) {
		in := Task{ID: "t", State: State(state), Owner: owner, Submitter: submitter, Verifier: verifier, LeaseEpoch: lease,
			Tier: tier, Failures: failures, Attempt: attempt}
		if deadline != 0 {
			in.LeaseDeadline = t0.Add(time.Duration(deadline) * time.Second)
		}
		if readyAt {
			in.ReadyAt = t0
		}
		n, _ := Repair(in, t0.Add(time.Hour))
		if err := Validate(n); err != nil {
			t.Fatalf("still invalid after repair: %v: %+v", err, n)
		}
		if again, did := Repair(n, t0.Add(2*time.Hour)); len(did) != 0 || again.State != n.State {
			t.Fatalf("a second repair changed things: %v", did)
		}
		if n.LeaseEpoch < in.LeaseEpoch {
			t.Fatalf("the lease went back from %d to %d", in.LeaseEpoch, n.LeaseEpoch)
		}
		if n.State == Done && in.State != Done {
			t.Fatalf("repair made a task done: %+v", in)
		}
	})
}
