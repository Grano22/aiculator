package swarm

import "time"

// Adaptive holds. A waiting agent pays for every call it makes: each wait_for_task is a model
// turn that spends tokens. So a wait should be held as long as the harness lets a tool call
// run, and no longer, because a call the harness cuts off costs the turn and returns nothing.
//
// Tool timeouts differ between harnesses and are rarely documented, so each session learns
// its own. A hold that ran to the end without the caller leaving means the limit is at least
// that long, and the next hold is a little longer, up to HoldMax. A caller that left before
// the hold ended shows where its limit is, and holds then stay safely under it.
const (
	HoldStart = 25 * time.Second
	HoldMax   = 40 * time.Second
	HoldMin   = 5 * time.Second
	holdStep  = 5 * time.Second
	// holdMargin is kept below a limit that was seen, for slow networks and clocks.
	holdMargin = 3 * time.Second
)

// AutoHold asks Wait to use what it learned of this session.
const AutoHold time.Duration = -1

type holdLearner struct {
	hold, limit time.Duration
}

func newHoldLearner() holdLearner { return holdLearner{hold: HoldStart, limit: HoldMax} }

func (h *holdLearner) current() time.Duration {
	if h.hold == 0 {
		return HoldStart
	}
	return h.hold
}

// completed records a hold that ran to its end with the caller still there.
func (h *holdLearner) completed(held time.Duration) {
	if h.limit == 0 {
		*h = newHoldLearner()
	}
	if held >= h.current() {
		h.hold = min(h.current()+holdStep, h.limit)
	}
}

// cutShort records a caller that left after elapsed, before its hold ended.
func (h *holdLearner) cutShort(elapsed time.Duration) {
	if elapsed < HoldMin {
		return // Too soon to be a timeout; the caller left for its own reasons.
	}
	h.limit = max(elapsed-holdMargin, HoldMin)
	h.hold = h.limit
}
