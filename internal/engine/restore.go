package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/vlog"
)

// Restored says what a restore did to one task.
type Restored struct {
	Task core.Task `json:"task"`
	// Did lists what had to change for the copy to be safe to put back.
	Did []string `json:"did,omitempty"`
	// Replaced is true when a task with that ID existed. Its record is then in quarantine.
	Replaced bool `json:"replaced"`
}

// Restore puts an earlier copy of a task back, from a backup.
//
// A copy is history, not a command, so it cannot be put back exactly as it was:
//   - Work that was in flight in the copy is not in flight now. A claimed, running, review or
//     merging copy goes back to Ready, keeping what it was, and its owner is cleared.
//   - The lease number moves past both the copy's and the current record's, so an agent
//     still holding a lease on the current record can write nothing more to it.
//   - Anything else that breaks the rules is fixed by the same repair as at startup. A Done
//     copy without an independent check is parked, never put back as Done.
//
// The record being replaced goes to quarantine first, so a restore can itself be undone.
func (e *Engine) Restore(ctx context.Context, t core.Task) (Restored, error) {
	if !idRE.MatchString(t.ID) {
		return Restored{}, fmt.Errorf("%w: task id %q", ErrInvalid, t.ID)
	}
	return do(ctx, e, func() (Restored, error) {
		now := e.now()
		n := t.Clone()
		var did []string
		switch n.State {
		case core.Claimed, core.Running, core.Review, core.Merging:
			did = append(did, fmt.Sprintf("was %s in the copy; back to ready", n.State))
			n.State, n.Owner, n.Submitter, n.Ref, n.Verifier = core.Ready, "", "", "", ""
			n.ChecksRun, n.ChecksPassed = false, false
			n.LeaseDeadline, n.ProgressDeadline = time.Time{}, time.Time{}
			n.ReadyAt = now
		}
		ent := e.tasks[n.ID]
		var old core.Task
		var rev uint64
		if ent != nil {
			old, rev = ent.task, ent.rev
		}
		n.LeaseEpoch = max(n.LeaseEpoch, old.LeaseEpoch) + 1
		fixed, more := core.Repair(n, now)
		did = append(did, more...)
		if err := core.Validate(fixed); err != nil {
			return Restored{}, fmt.Errorf("%w: the copy cannot be made valid: %v", ErrInvalid, err)
		}
		var err error
		if ent != nil {
			raw, _ := json.Marshal(old)
			if err := e.st.Quarantine(old.ID, rev, raw, "replaced by a restore"); err != nil {
				return Restored{}, fmt.Errorf("keep the record being replaced: %w", err)
			}
			rev, err = e.st.UpdateTask(fixed, rev)
		} else {
			rev, err = e.st.CreateTask(fixed)
		}
		if err != nil {
			if errors.Is(err, store.ErrConflict) && ent != nil {
				e.reload(n.ID)
			}
			return Restored{}, err
		}
		e.index(old, fixed, rev)
		if ent != nil && old.State != fixed.State {
			e.obs.Transition(string(old.State), string(fixed.State))
		}
		log.V(vlog.LevelDefault).Warn("restored a task from a backup", "task", fixed.ID, "state", fixed.State, "lease", fixed.LeaseEpoch, "did", did)
		return Restored{Task: fixed, Did: did, Replaced: ent != nil}, nil
	})
}
