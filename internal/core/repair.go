package core

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// Repair returns a task that satisfies Validate, and says what it changed. A record breaks
// the invariants only through damage, a manual edit or an older version, and the swarm must
// still start and carry on.
//
// It never invents progress. Work that was in flight goes back to the queue, a review loses
// its reviewer, and anything whose truth cannot be known goes to Parked for a person, with the
// reason. A valid task comes back unchanged. now is the repair time, passed in because core
// reads no clock.
func Repair(t Task, now time.Time) (Task, []string) {
	if Validate(t) == nil && ValidateAttrs(t) == nil {
		return t, nil
	}
	n := t.Clone()
	var did []string
	note := func(format string, args ...any) { did = append(did, fmt.Sprintf(format, args...)) }
	park := func(why string) {
		n.State, n.Reason = Parked, why
		n.Owner, n.LeaseDeadline, n.ProgressDeadline = "", time.Time{}, time.Time{}
		note("parked: %s", why)
	}
	requeue := func(why string) {
		n.State, n.ReadyAt, n.Reason = Ready, now, why
		n.Owner, n.LeaseDeadline, n.ProgressDeadline = "", time.Time{}, time.Time{}
		note("back to ready: %s", why)
	}
	if n.Tier < 1 {
		note("tier %d raised to 1", n.Tier)
		n.Tier = 1
	}
	if n.Failures < 0 || n.Attempt < 0 {
		note("negative counters set to 0")
		n.Failures, n.Attempt = max(n.Failures, 0), max(n.Attempt, 0)
	}
	if n.Priority > MaxPriority || n.Priority < -MaxPriority {
		note("priority %d clamped", n.Priority)
		n.Priority = min(max(n.Priority, -MaxPriority), MaxPriority)
	}
	if len(n.Labels) > 0 {
		keep := map[string]string{}
		for _, k := range slices.Sorted(maps.Keys(n.Labels)) {
			v := n.Labels[k]
			if !labelKeyRE.MatchString(k) || len(v) > MaxLabelValue || len(keep) == MaxLabels {
				note("label %q dropped", k)
				continue
			}
			keep[k] = v
		}
		n.Labels = keep
	}
	if ValidateAttrs(Task{ID: n.ID, Aging: n.Aging}) != nil {
		note("invalid aging steps dropped")
		n.Aging = nil
	}
	if !slices.Contains(AllStates, n.State) {
		park(fmt.Sprintf("unknown state %q", n.State))
	}
	switch n.State {
	case Claimed, Running:
		if n.Owner == "" || n.LeaseEpoch == 0 || n.LeaseDeadline.IsZero() {
			requeue(fmt.Sprintf("%s without a lease", t.State))
		}
	case Review:
		switch {
		case n.Submitter == "":
			requeue("review without a submitter")
		case n.Owner != "" && n.Owner == n.Submitter:
			n.Owner, n.LeaseDeadline = "", time.Time{}
			note("the submitter was reviewing its own work, and was removed")
		}
	case Merging:
		if n.Verifier == "" || n.Verifier == n.Submitter {
			// Not verified after all, so it goes back to be.
			n.State, n.Verifier, n.ChecksRun, n.ChecksPassed = Review, "", false, false
			n.Owner, n.LeaseDeadline = "", time.Time{}
			note("merging without independent verification, so back to review")
			if n.Submitter == "" {
				requeue("merging without a submitter")
			}
		} else if n.Owner != "" {
			n.Owner, n.LeaseDeadline = "", time.Time{}
			note("owner removed from a merging task")
		}
	case Done:
		// It says done, and may already be merged, so redoing it could repeat work. That is for
		// a person to judge.
		if n.Verifier == "" || n.Verifier == n.Submitter {
			park("done without independent verification, so a person must decide")
		} else if n.Owner != "" {
			n.Owner = ""
			note("owner removed from a done task")
		}
	default:
		if n.Owner != "" {
			n.Owner, n.LeaseDeadline, n.ProgressDeadline = "", time.Time{}, time.Time{}
			note("owner removed from a %s task", n.State)
		}
	}
	if n.State == Ready && n.ReadyAt.IsZero() {
		n.ReadyAt = now
		note("ready time set")
	}
	if n.LeaseEpoch < t.LeaseEpoch {
		n.LeaseEpoch = t.LeaseEpoch // A lease number never goes back, or a zombie could come back.
	}
	n.UpdatedAt = now
	return n, did
}
