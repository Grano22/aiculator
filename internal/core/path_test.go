package core

import (
	"fmt"
	"testing"
	"time"
)

// key is what matters about a task for which commands it accepts. Two tasks with the same
// key behave the same, which is what makes the search below finish.
func key(t Task) string {
	return fmt.Sprintf("%s|%s|%d|%s|%s|%t%t%t|%d|%d|%d|%t%t", t.State, t.Owner, min(t.LeaseEpoch, 4), t.Submitter, t.Verifier,
		t.ChecksRun, t.ChecksPassed, t.RequireReview, t.Failures, t.Tier, min(t.Attempt, 4), t.Budget > 0 && t.Spent >= t.Budget, t.Budget > 0)
}

// TestEveryReachablePathIsSafe explores the state machine itself, the way TLC explores the
// model: from a new task, apply every command from every actor, in every state reached, until
// nothing new turns up. Every state on every path must be valid, every transition in the spec
// must be reachable from the start, and no reachable state may be a dead end.
//
// This is path coverage of the real code, not of a model of it.
func TestEveryReachablePathIsSafe(t *testing.T) {
	kinds := []CmdKind{CmdPublish, CmdGrant, CmdHeartbeat, CmdProgress, CmdSubmit, CmdChecks, CmdVerdict, CmdMerged, CmdFail, CmdRelease, CmdExpire, CmdRecover, CmdUnpark, CmdCancel, CmdAmend}
	actors := []string{"a", "b", "system:x"}
	p := DefaultPolicy()
	seen := map[string]bool{}
	taken := map[string]bool{}
	type node struct {
		task  Task
		depth int
	}
	var queue []node
	// With and without a budget: one small enough to run out reaches the parking paths, and
	// none at all reaches the retries that a spent budget would cut short.
	for _, review := range []bool{false, true} {
		for _, checks := range [][]string{nil, {"go test"}} {
			for _, budget := range []int64{0, 3} {
				start := Task{ID: "t", Tier: 1, State: Draft, RequireReview: review, Checks: checks, Budget: budget}
				queue = append(queue, node{start, 0})
				seen[key(start)] = true
			}
		}
	}
	deadEnds := map[string]bool{}
	maxDepth, edges := 0, 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		maxDepth = max(maxDepth, n.depth)
		moved := false
		for _, k := range kinds {
			for _, a := range actors {
				for _, pass := range []bool{true, false} {
					c := Cmd{Kind: k, Actor: a, Lease: n.task.LeaseEpoch, Pass: pass, AgentTier: 3, Cost: 1,
						LeaseTTL: time.Minute, ProgressTTL: time.Minute, Evidence: fmt.Sprint(n.depth), Amend: &Amendment{}}
					next, err := Apply(n.task, c, t0.Add(time.Duration(n.depth)*time.Second), p)
					if err != nil {
						continue
					}
					edges++
					if err := Validate(next); err != nil {
						t.Fatalf("after %s by %s from %s: %v", k, a, key(n.task), err)
					}
					if next.State == Done && next.Verifier == next.Submitter {
						t.Fatalf("done without independent verification: %+v", next)
					}
					if next.State != n.task.State {
						moved = true
					}
					taken[fmt.Sprintf("%s %s %s", n.task.State, k, next.State)] = true
					if kk := key(next); !seen[kk] {
						seen[kk] = true
						queue = append(queue, node{next, n.depth + 1})
					}
				}
			}
		}
		if !moved && !n.task.State.Terminal() && n.task.State != Parked {
			deadEnds[key(n.task)] = true
		}
	}
	// Parked waits for a person by design; anything else that cannot move is stuck.
	for k := range deadEnds {
		t.Errorf("a reachable state has no way out: %s", k)
	}
	spec := specTransitions(t)
	for tr := range spec {
		if !taken[tr] {
			t.Errorf("the spec allows %q, but no path from a new task reaches it", tr)
		}
	}
	for tr := range taken {
		if !spec[tr] {
			t.Errorf("a path takes %q, which the spec does not model", tr)
		}
	}
	t.Logf("%d distinct states, %d transitions taken, %d kinds of transition, paths up to %d long", len(seen), edges, len(taken), maxDepth)
}
