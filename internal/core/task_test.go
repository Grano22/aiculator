package core

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func newTask() Task {
	return Task{ID: "t1", Title: "x", Tier: 1, State: Draft, CreatedAt: t0, UpdatedAt: t0, Checks: []string{"true"}}
}

func must(t *testing.T, task Task, c Cmd) Task {
	t.Helper()
	n, err := Apply(task, c, t0, DefaultPolicy())
	if err != nil {
		t.Fatalf("%s from %s: %v", c.Kind, task.State, err)
	}
	return n
}

func grant(actor string) Cmd {
	return Cmd{Kind: CmdGrant, Actor: actor, AgentTier: 3, LeaseTTL: time.Minute, ProgressTTL: time.Minute}
}

func running(t *testing.T, actor string) Task {
	t.Helper()
	x := must(t, must(t, newTask(), Cmd{Kind: CmdPublish}), grant(actor))
	return must(t, x, Cmd{Kind: CmdHeartbeat, Actor: actor, Lease: x.LeaseEpoch, LeaseTTL: time.Minute})
}

func TestHappyPathReachesDone(t *testing.T) {
	x := running(t, "a")
	x = must(t, x, Cmd{Kind: CmdSubmit, Actor: "a", Lease: x.LeaseEpoch})
	x = must(t, x, Cmd{Kind: CmdChecks, Actor: "system:checks", Pass: true})
	if x.State != Merging {
		t.Fatalf("state = %s", x.State)
	}
	x = must(t, x, Cmd{Kind: CmdMerged, Actor: "system:merge"})
	if x.State != Done || x.Verifier != "system:checks" {
		t.Fatalf("got %+v", x)
	}
}

func TestDoubleGrantIsIllegal(t *testing.T) {
	x := must(t, must(t, newTask(), Cmd{Kind: CmdPublish}), grant("a"))
	if _, err := Apply(x, grant("b"), t0, DefaultPolicy()); !errors.Is(err, ErrIllegal) {
		t.Fatalf("err = %v", err)
	}
}

func TestZombieIsFencedAfterRegrant(t *testing.T) {
	x := running(t, "a")
	old := x.LeaseEpoch
	x = must(t, x, Cmd{Kind: CmdExpire, Actor: "system:lease"})
	x = must(t, x, Cmd{Kind: CmdRecover})
	x = must(t, x, grant("a")) // Same agent, new lease: the old token must still die.
	if _, err := Apply(x, Cmd{Kind: CmdSubmit, Actor: "a", Lease: old}, t0, DefaultPolicy()); !errors.Is(err, ErrFenced) {
		t.Fatalf("err = %v", err)
	}
}

func TestSubmitterCannotVerifyOwnWork(t *testing.T) {
	x := running(t, "a")
	x.RequireReview = true
	x = must(t, x, Cmd{Kind: CmdSubmit, Actor: "a", Lease: x.LeaseEpoch})
	if _, err := Apply(x, grant("a"), t0, DefaultPolicy()); !errors.Is(err, ErrSelfVerify) {
		t.Fatalf("grant err = %v", err)
	}
	if _, err := Apply(x, Cmd{Kind: CmdVerdict, Actor: "a", Pass: true}, t0, DefaultPolicy()); !errors.Is(err, ErrSelfVerify) {
		t.Fatalf("verdict err = %v", err)
	}
}

func TestReviewerCannotApproveBeforeChecksPass(t *testing.T) {
	x := running(t, "a")
	x.RequireReview = true
	x = must(t, x, Cmd{Kind: CmdSubmit, Actor: "a", Lease: x.LeaseEpoch})
	x = must(t, x, grant("b"))
	if _, err := Apply(x, Cmd{Kind: CmdVerdict, Actor: "b", Lease: x.LeaseEpoch, Pass: true}, t0, DefaultPolicy()); !errors.Is(err, ErrIllegal) {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentCannotActAsSystem(t *testing.T) {
	x := running(t, "a")
	for _, k := range []CmdKind{CmdChecks, CmdMerged, CmdExpire} {
		if _, err := Apply(x, Cmd{Kind: k, Actor: "a", Pass: true}, t0, DefaultPolicy()); err == nil {
			t.Errorf("%s accepted from an agent", k)
		}
	}
}

func TestRepeatedEvidenceDoesNotExtendProgress(t *testing.T) {
	x := running(t, "a")
	later := t0.Add(30 * time.Second)
	c := Cmd{Kind: CmdProgress, Actor: "a", Lease: x.LeaseEpoch, LeaseTTL: time.Minute, ProgressTTL: time.Minute, Evidence: "tree1"}
	x, _ = Apply(x, c, later, DefaultPolicy())
	dl := x.ProgressDeadline
	x, _ = Apply(x, c, later.Add(10*time.Second), DefaultPolicy())
	if !x.ProgressDeadline.Equal(dl) {
		t.Fatal("same evidence extended the progress deadline")
	}
	c.Evidence = "tree2"
	x, _ = Apply(x, c, later.Add(10*time.Second), DefaultPolicy())
	if !x.ProgressDeadline.After(dl) {
		t.Fatal("new evidence did not extend the progress deadline")
	}
}

func TestEscalatesThenParks(t *testing.T) {
	x := must(t, newTask(), Cmd{Kind: CmdPublish})
	for wantTier := 1; wantTier <= 3; wantTier++ {
		for i := range 2 {
			if x.Tier != wantTier {
				t.Fatalf("round %d: tier = %d, want %d", i, x.Tier, wantTier)
			}
			x = must(t, x, grant("a"))
			x = must(t, x, Cmd{Kind: CmdFail, Actor: "a", Lease: x.LeaseEpoch})
			x = must(t, x, Cmd{Kind: CmdRecover})
		}
	}
	if x.State != Parked || x.Reason != "needs_resplit" {
		t.Fatalf("got %s/%s", x.State, x.Reason)
	}
	if x = must(t, x, Cmd{Kind: CmdUnpark}); x.State != Ready {
		t.Fatalf("unpark gave %s", x.State)
	}
}

func TestBudgetParksAndRaiseUnparks(t *testing.T) {
	x := must(t, newTask(), Cmd{Kind: CmdPublish})
	x.Budget = 100
	x = must(t, x, grant("a"))
	x = must(t, x, Cmd{Kind: CmdFail, Actor: "a", Lease: x.LeaseEpoch, Cost: 100})
	x = must(t, x, Cmd{Kind: CmdRecover})
	if x.State != Parked || x.Reason != "budget_exhausted" {
		t.Fatalf("got %s/%s", x.State, x.Reason)
	}
	if x = must(t, x, Cmd{Kind: CmdUnpark, AddBudget: 500}); x.Budget != 600 {
		t.Fatalf("budget = %d", x.Budget)
	}
}

// Every non-terminal state must have a way out; otherwise tasks can get stuck.
func TestEveryNonTerminalStateHasAnExit(t *testing.T) {
	for _, s := range AllStates {
		x := Task{ID: "t", Tier: 1, State: s, ReadyAt: t0, Owner: "a", LeaseEpoch: 1, LeaseDeadline: t0, Submitter: "s", Verifier: "v"}
		if s != Claimed && s != Running {
			x.Owner = ""
		}
		n, err := Apply(x, Cmd{Kind: CmdCancel, Note: "stop"}, t0, DefaultPolicy())
		if s.Terminal() {
			if err == nil {
				t.Errorf("%s: terminal state allowed an exit", s)
			}
			continue
		}
		if err != nil || n.State != Canceled {
			t.Errorf("%s: no exit: %v", s, err)
		}
	}
}

// FuzzApply drives random command sequences and checks properties that must hold
// for any input: results stay valid, rejected commands change nothing, lease
// epochs never go backwards, and done always means independently verified.
func FuzzApply(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	f.Add([]byte{0, 1, 4, 10, 11, 1, 4, 10, 11, 1, 4, 10})
	f.Fuzz(func(t *testing.T, ops []byte) {
		kinds := []CmdKind{CmdPublish, CmdGrant, CmdHeartbeat, CmdProgress, CmdSubmit, CmdChecks, CmdVerdict, CmdMerged, CmdFail, CmdExpire, CmdRecover, CmdUnpark, CmdCancel, CmdRelease}
		actors := []string{"a", "b", "system:x", ""}
		x := newTask()
		x.RequireReview = len(ops)%2 == 0
		var maxLease uint64
		for i := 0; i+1 < len(ops); i += 2 {
			c := Cmd{
				Kind:        kinds[int(ops[i])%len(kinds)],
				Actor:       actors[int(ops[i+1])%len(actors)],
				Lease:       x.LeaseEpoch,
				AgentTier:   1 + int(ops[i+1]>>4)%3,
				Pass:        ops[i+1]&2 != 0,
				LeaseTTL:    time.Minute,
				ProgressTTL: time.Minute,
				Evidence:    string(rune('a' + ops[i+1]%4)),
				Cost:        int64(ops[i+1]) - 100,
			}
			if ops[i+1]&64 != 0 {
				c.Lease = x.LeaseEpoch + 1 // A forged or stale lease.
			}
			before, _ := json.Marshal(x)
			n, err := Apply(x, c, t0.Add(time.Duration(i)*time.Second), DefaultPolicy())
			after, _ := json.Marshal(x)
			if string(before) != string(after) {
				t.Fatalf("Apply mutated its input on %s", c.Kind)
			}
			if err != nil {
				continue
			}
			if verr := Validate(n); verr != nil {
				t.Fatalf("%s produced invalid task: %v", c.Kind, verr)
			}
			if n.LeaseEpoch < maxLease {
				t.Fatalf("lease went backwards: %d < %d", n.LeaseEpoch, maxLease)
			}
			maxLease = n.LeaseEpoch
			if x.State.Terminal() {
				t.Fatalf("left terminal state %s via %s", x.State, c.Kind)
			}
			if n.State == Done && (n.Verifier == "" || n.Verifier == n.Submitter) {
				t.Fatalf("done without independent verifier: %+v", n)
			}
			x = n
		}
	})
}

func TestReleaseCarriesNoPenalty(t *testing.T) {
	x := running(t, "a")
	lease := x.LeaseEpoch
	x = must(t, x, Cmd{Kind: CmdRelease, Actor: "a", Lease: lease, Note: "no login"})
	if x.State != Ready || x.Failures != 0 || x.Tier != 1 || x.Owner != "" {
		t.Fatalf("got %+v", x)
	}
	if _, err := Apply(x, Cmd{Kind: CmdRelease, Actor: "a", Lease: lease}, t0, DefaultPolicy()); !errors.Is(err, ErrFenced) {
		t.Fatalf("double release: %v", err)
	}
}

func TestApplySharesNothingAPreviousTaskCanSee(t *testing.T) {
	base := Task{ID: "x", Tier: 1, State: Running, Owner: "a", LeaseEpoch: 1, LeaseDeadline: t0, ReadyAt: t0,
		Notes: make([]Note, 1, 8), Labels: map[string]string{"k": "v"}}
	n, err := Apply(base, Cmd{Kind: CmdProgress, Actor: "a", Lease: 1, Note: "one", LeaseTTL: time.Minute, ProgressTTL: time.Minute}, t0, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Notes) != 1 || base.Notes[:2][1].Text != "" {
		t.Fatalf("the old task can see the new note: %+v", base.Notes[:2])
	}
	m, err := Apply(n, Cmd{Kind: CmdAmend, Actor: "system:x", Amend: &Amendment{Labels: map[string]string{"k": "changed"}}}, t0, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if n.Labels["k"] != "v" || m.Labels["k"] != "changed" {
		t.Fatalf("amend wrote into the old task's labels: %v %v", n.Labels, m.Labels)
	}
}

func TestAgingBoostSteps(t *testing.T) {
	steps := []AgingStep{{AfterSec: 60, Boost: 5}, {AfterSec: 120, Boost: 20}}
	for _, c := range []struct {
		waited time.Duration
		boost  int
		next   time.Duration
	}{{0, 0, time.Minute}, {59 * time.Second, 0, time.Second}, {time.Minute, 5, time.Minute}, {2 * time.Minute, 20, 0}, {time.Hour, 20, 0}} {
		if b, n := AgingBoost(steps, c.waited); b != c.boost || n != c.next {
			t.Errorf("%v: got %d %v", c.waited, b, n)
		}
	}
	if b, n := AgingBoost(nil, time.Hour); b != 0 || n != 0 {
		t.Errorf("no steps: %d %v", b, n)
	}
}
