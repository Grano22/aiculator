package observe

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// The flow analytics: how tasks move through the states, why they fall out, how many tries
// they take, and how many sat in each state over time. They are computed from the durable
// event log, so they survive restarts, unlike the rates above.

// Pipeline is the path a task takes when all goes well.
var Pipeline = []string{"draft", "ready", "claimed", "running", "review", "merging", "done"}

// Exits are the states a task leaves the path for.
var Exits = []string{"failed", "parked", "canceled"}

// Revision is what the index needs of one committed task revision.
type Revision struct {
	ID      string
	State   string
	Tier    int
	Attempt int
	Reason  string
	Labels  map[string]string
	At      time.Time
}

type move struct {
	at      int64 // Unix milliseconds.
	state   uint8
	attempt uint16
	reason  uint16
}

type taskHistory struct {
	tier   int
	labels map[string]string
	moves  []move
}

// FlowIndex keeps each task's state changes, compactly, for the analytics. A heartbeat or
// any other revision that leaves the state and attempt as they were adds nothing.
type FlowIndex struct {
	mu      sync.RWMutex
	tasks   map[string]*taskHistory
	states  []string
	stateID map[string]uint8
	reasons []string
	reasonI map[string]uint16
	moves   int
	ready   bool
	// Per state: whether it leaves the path, and where on the path it is.
	exit [256]bool
	rank [256]uint8
}

// NewFlowIndex returns an empty index.
func NewFlowIndex() *FlowIndex {
	x := &FlowIndex{tasks: map[string]*taskHistory{}, stateID: map[string]uint8{}, reasonI: map[string]uint16{"": 0}, reasons: []string{""}}
	for _, s := range append(slices.Clone(Pipeline), Exits...) {
		x.state(s)
	}
	return x
}

func (x *FlowIndex) state(s string) uint8 {
	if i, ok := x.stateID[s]; ok {
		return i
	}
	if len(x.states) >= 255 {
		return 0
	}
	i := uint8(len(x.states))
	x.states = append(x.states, s)
	x.stateID[s] = i
	x.exit[i] = slices.Contains(Exits, s)
	x.rank[i] = uint8(len(Pipeline))
	if r := slices.Index(Pipeline, s); r >= 0 {
		x.rank[i] = uint8(r)
	}
	return i
}

func (x *FlowIndex) reason(r string) uint16 {
	r = ReasonCode(r)
	if i, ok := x.reasonI[r]; ok {
		return i
	}
	if len(x.reasons) >= 65535 {
		return 0
	}
	i := uint16(len(x.reasons))
	x.reasons = append(x.reasons, r)
	x.reasonI[r] = i
	return i
}

var codeRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// ReasonCode turns a reason into a short code for charts. The swarm's own reasons are codes
// already. A note an agent wrote when it gave up is free text, which could hold anything, so
// it becomes "agent_note" instead of a category of its own.
func ReasonCode(r string) string {
	r = strings.TrimSpace(r)
	if r == "" {
		return ""
	}
	if code, _, ok := strings.Cut(r, ":"); ok && codeRE.MatchString(code) {
		return code
	}
	if codeRE.MatchString(r) {
		return r
	}
	low := strings.ToLower(r)
	switch {
	case strings.Contains(low, "without independent verification"), strings.Contains(low, "done without verification"):
		return "done_without_verification"
	case strings.Contains(low, "unreadable"):
		return "unreadable_record"
	}
	return "agent_note"
}

// Observe adds one revision. Revisions must come in commit order for each task.
func (x *FlowIndex) Observe(r Revision) {
	x.mu.Lock()
	defer x.mu.Unlock()
	h := x.tasks[r.ID]
	if h == nil {
		h = &taskHistory{}
		x.tasks[r.ID] = h
	}
	h.tier, h.labels = r.Tier, r.Labels
	st := x.state(r.State)
	att := uint16(min(max(r.Attempt, 0), 65535))
	if n := len(h.moves); n > 0 && h.moves[n-1].state == st && h.moves[n-1].attempt == att {
		return
	}
	h.moves = append(h.moves, move{at: r.At.UnixMilli(), state: st, attempt: att, reason: x.reason(r.Reason)})
	x.moves++
}

// SetReady marks the index as having read the whole log, so that its numbers are complete.
func (x *FlowIndex) SetReady() {
	x.mu.Lock()
	x.ready = true
	x.mu.Unlock()
}

// Query picks the tasks and time the analytics cover.
type Query struct {
	// Window is how far back to look; 0 means everything kept.
	Window time.Duration
	// Tier, when above 0, keeps tasks at that tier now.
	Tier int
	// Label keeps tasks with this label now: key=value, or key alone for any value.
	Label string
	// Points is how many samples the cumulative flow has; 0 means 60.
	Points int
}

// Analytics is what the charts draw.
type Analytics struct {
	Ready    bool          `json:"ready"`
	From     time.Time     `json:"from"`
	To       time.Time     `json:"to"`
	Tasks    int           `json:"tasks"`
	Moves    int           `json:"moves"`
	Tiers    []int         `json:"tiers"`
	Labels   []string      `json:"labels"`
	States   []Link        `json:"state_flow"`
	Reasons  []Link        `json:"failure_flow"`
	Attempts []AttemptStep `json:"attempts"`
	Retries  []Count       `json:"retry_causes"`
	CFD      CFD           `json:"cumulative_flow"`
}

// Link is a band in a Sankey diagram.
type Link struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Count int    `json:"count"`
}

// AttemptStep is one step of the retry funnel.
type AttemptStep struct {
	Attempt int `json:"attempt"`
	// Reached is how many tasks got at least this many tries.
	Reached int `json:"reached"`
	// Done is how many finished on exactly this try.
	Done int `json:"done"`
}

// Count is a labelled number.
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// CFD is a cumulative flow diagram: tasks in each state at each moment.
type CFD struct {
	Times  []time.Time      `json:"times"`
	States []string         `json:"states"`
	Series map[string][]int `json:"series"`
}

const maxLabelChoices = 50

// Analyze computes the analytics.
func (x *FlowIndex) Analyze(q Query, now time.Time) Analytics {
	x.mu.RLock()
	defer x.mu.RUnlock()
	to := now.UnixMilli()
	from := int64(0)
	if q.Window > 0 {
		from = to - q.Window.Milliseconds()
	}
	points := q.Points
	if points <= 0 {
		points = 60
	}
	points = min(points, 500)
	a := Analytics{Ready: x.ready, To: now, Moves: x.moves}
	labelKey, labelVal, withVal := strings.Cut(q.Label, "=")

	tiers := map[int]bool{}
	labels := map[string]map[string]bool{} // Key, then value, so no string is built per task.
	nLabels := 0
	stateLinks := map[[2]string]int{}
	failLinks := map[[2]string]int{}
	reachedAt := map[int]int{}
	doneAt := map[int]int{}
	retries := map[string]int{}
	picked := make([]*taskHistory, 0, len(x.tasks))
	pathBuf := make([]uint8, 0, 16)
	earliest := to
	for _, h := range x.tasks {
		tiers[h.tier] = true
		for k, v := range h.labels {
			vs := labels[k]
			if vs == nil && nLabels < maxLabelChoices {
				vs = map[string]bool{}
				labels[k] = vs
			}
			if vs != nil && !vs[v] && nLabels < maxLabelChoices {
				vs[v] = true
				nLabels++
			}
		}
		if q.Tier > 0 && h.tier != q.Tier {
			continue
		}
		if q.Label != "" {
			v, ok := h.labels[labelKey]
			if !ok || (withVal && v != labelVal) {
				continue
			}
		}
		if len(h.moves) == 0 || h.moves[len(h.moves)-1].at < from {
			// Nothing happened to it in the window. It still counts in the cumulative flow,
			// as the state it sat in.
			picked = append(picked, h)
			continue
		}
		picked = append(picked, h)
		a.Tasks++
		earliest = min(earliest, h.moves[0].at)

		// State flow: each state on the path a task reached for the first time in the
		// window, in path order, then where it left the path, if it did. A path, not every
		// move, so the diagram has no loops: the retries are in the funnel instead.
		var seen [4]uint64 // A bit per state.
		path := pathBuf[:0]
		last := h.moves[len(h.moves)-1]
		for _, m := range h.moves {
			if m.at < from || seen[m.state/64]&(1<<(m.state%64)) != 0 || x.isExit(m.state) {
				continue
			}
			seen[m.state/64] |= 1 << (m.state % 64)
			path = append(path, m.state)
		}
		slices.SortFunc(path, func(p, r uint8) int { return cmp.Compare(x.order(p), x.order(r)) })
		for i := 1; i < len(path); i++ {
			stateLinks[[2]string{x.states[path[i-1]], x.states[path[i]]}]++
		}
		if x.isExit(last.state) && last.at >= from && len(path) > 0 {
			stateLinks[[2]string{x.states[path[len(path)-1]], x.states[last.state]}]++
		}

		// Failure flow: every time a task fell out in the window, from where, and why.
		maxAttempt := 0
		for i, m := range h.moves {
			maxAttempt = max(maxAttempt, int(m.attempt))
			if m.at < from {
				continue
			}
			st := x.states[m.state]
			if st == "failed" || st == "parked" {
				prev := "new"
				if i > 0 {
					prev = x.states[h.moves[i-1].state]
				}
				reason := x.reasons[m.reason]
				if reason == "" {
					reason = "unknown"
				}
				failLinks[[2]string{prev, st}]++
				failLinks[[2]string{st, reason}]++
			}
			if st == "done" {
				doneAt[max(int(m.attempt), 1)]++
			}
			// A new try: count what ended the one before it.
			if i > 0 && m.attempt > h.moves[i-1].attempt && h.moves[i-1].attempt > 0 {
				// Why the last try fell out, if it failed or was parked; otherwise the latest
				// reason it had, such as an expired claim or a release by the harness. Moving
				// back to ready says only "retry", which explains nothing.
				cause, fallback := "", ""
				for j := i - 1; j >= 0 && h.moves[j].attempt == h.moves[i-1].attempt; j-- {
					r := x.reasons[h.moves[j].reason]
					if st := x.states[h.moves[j].state]; (st == "failed" || st == "parked") && r != "" {
						cause = r
						break
					}
					if fallback == "" && r != "" && r != "retry" && r != "escalated" {
						fallback = r
					}
				}
				cause = cmp.Or(cause, fallback)
				if cause == "" {
					cause = "unknown"
				}
				retries[cause]++
			}
		}
		for k := 1; k <= maxAttempt; k++ {
			reachedAt[k]++
		}
	}

	a.States = links(stateLinks)
	a.Reasons = links(failLinks)
	for k := 1; ; k++ {
		if reachedAt[k] == 0 && doneAt[k] == 0 {
			break
		}
		a.Attempts = append(a.Attempts, AttemptStep{Attempt: k, Reached: reachedAt[k], Done: doneAt[k]})
	}
	for name, n := range retries {
		a.Retries = append(a.Retries, Count{name, n})
	}
	slices.SortFunc(a.Retries, func(p, r Count) int { return cmp.Or(cmp.Compare(r.Count, p.Count), cmp.Compare(p.Name, r.Name)) })
	for t := range tiers {
		a.Tiers = append(a.Tiers, t)
	}
	slices.Sort(a.Tiers)
	for k, vs := range labels {
		for v := range vs {
			a.Labels = append(a.Labels, k+"="+v)
		}
	}
	slices.Sort(a.Labels)

	// The cumulative flow starts at the first change it can show, and never before the
	// window: a long window over a short history would squeeze it all against one edge.
	from = max(from, earliest)
	if from >= to {
		from = to - 1
	}
	a.From = time.UnixMilli(from)
	a.CFD = x.cfd(picked, from, to, points)
	return a
}

func (x *FlowIndex) isExit(s uint8) bool { return x.exit[s] }

func (x *FlowIndex) order(s uint8) int { return int(x.rank[s]) }

func links(m map[[2]string]int) []Link {
	out := make([]Link, 0, len(m))
	for k, n := range m {
		out = append(out, Link{k[0], k[1], n})
	}
	slices.SortFunc(out, func(p, r Link) int {
		return cmp.Or(cmp.Compare(r.Count, p.Count), cmp.Compare(p.From, r.From), cmp.Compare(p.To, r.To))
	})
	return out
}

func (x *FlowIndex) cfd(picked []*taskHistory, from, to int64, points int) CFD {
	c := CFD{States: append(slices.Clone(Pipeline), Exits...), Series: map[string][]int{}}
	idx := map[uint8]int{}
	for i, s := range c.States {
		idx[x.stateID[s]] = i
		c.Series[s] = make([]int, points)
	}
	counts := make([][]int, len(c.States))
	for i := range counts {
		counts[i] = c.Series[c.States[i]]
	}
	step := float64(to-from) / float64(max(points-1, 1))
	for k := range points {
		c.Times = append(c.Times, time.UnixMilli(from+int64(float64(k)*step)))
	}
	for _, h := range picked {
		j := 0
		for k := range points {
			at := c.Times[k].UnixMilli()
			for j < len(h.moves) && h.moves[j].at <= at {
				j++
			}
			if j == 0 {
				continue // Not created yet.
			}
			if i, ok := idx[h.moves[j-1].state]; ok {
				counts[i][k]++
			}
		}
	}
	return c
}
