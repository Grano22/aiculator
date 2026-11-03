package engine

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/vlog"
)

// readyItem is one entry in a tier's ready heap. key is priority·agingUnit minus
// the time the task became ready. Comparing it gives the same order as comparing
// priority+waited/agingUnit at any moment, so priority aging needs no re-sorting.
type readyItem struct {
	id  string
	key int64
	seq uint64
}

type readyHeap []readyItem

func (h readyHeap) Len() int { return len(h) }
func (h readyHeap) Less(i, j int) bool {
	if h[i].key != h[j].key {
		return h[i].key > h[j].key
	}
	return h[i].seq < h[j].seq
}
func (h readyHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *readyHeap) Push(x any)   { *h = append(*h, x.(readyItem)) }
func (h *readyHeap) Pop() any {
	old := *h
	n := len(old) - 1
	it := old[n]
	*h = old[:n]
	return it
}
func (h *readyHeap) pushItem(it readyItem) { heap.Push(h, it) }

// boostItem says when a ready task next reaches an aging step. seq ties it to one heap
// entry: a task that was claimed or moved since then no longer matches, and is skipped.
type boostItem struct {
	at  time.Time
	id  string
	seq uint64
}

type boostHeap []boostItem

func (h boostHeap) Len() int           { return len(h) }
func (h boostHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h boostHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *boostHeap) Push(x any)        { *h = append(*h, x.(boostItem)) }
func (h *boostHeap) Pop() any {
	old := *h
	n := len(old) - 1
	it := old[n]
	old[n] = boostItem{}
	*h = old[:n]
	return it
}

// Wait answers an agent's wait_for_task call with one of three actions.
const (
	ActionTask = "task"
	ActionWait = "wait"
	ActionExit = "exit"
)

// Reasons an agent is told to wait or exit.
const (
	ReasonPlanning    = "planning"
	ReasonPlannerLost = "planner_lost"
	ReasonDeps        = "deps"
	ReasonTierBusy    = "tier_busy"
	ReasonIdle        = "idle"
	ReasonPlanDone    = "no_work_left"
	ReasonNoneExpect  = "no_work_expected"
)

// PlanningView is what waiting agents see of the planner.
type PlanningView struct {
	Epoch         uint64      `json:"epoch"`
	Active        bool        `json:"active"`
	Phase         string      `json:"phase,omitempty"`
	ExpectedTasks map[int]int `json:"expected_tasks,omitempty"`
	ETAMs         int64       `json:"eta_ms,omitempty"`
	AgeMs         int64       `json:"age_ms"`
	Stale         bool        `json:"stale"`
}

// WaitResponse is the result of Wait.
type WaitResponse struct {
	Action     string        `json:"action"`
	Job        string        `json:"job,omitempty"` // "work" or "review".
	Task       *core.Task    `json:"task,omitempty"`
	Lease      uint64        `json:"lease,omitempty"`
	LeaseTTLMs int64         `json:"lease_ttl_ms,omitempty"`
	Reason     string        `json:"reason,omitempty"`
	Planning   *PlanningView `json:"planning,omitempty"`
}

type waiter struct {
	agent    Agent
	since    time.Time
	ch       chan WaitResponse
	deadline time.Time
}

func (e *Engine) reply(w *waiter, r WaitResponse) {
	if v := log.V(vlog.LevelDebug); v.Enabled() {
		v.Info("wait answered", "agent", w.agent.ID, "tier", w.agent.Tier, "action", r.Action, "reason", r.Reason)
	}
	w.ch <- r // Buffered with capacity 1, and each waiter is replied to once.
	e.waiters[w.agent.Tier] = slices.DeleteFunc(e.waiters[w.agent.Tier], func(x *waiter) bool { return x == w })
}

// Wait returns a leased task, or holds for up to hold, then says wait or exit.
// A task it returns is already claimed, so there is no second round trip and
// nothing for agents to race on. hold 0 is a non-blocking try.
func (e *Engine) Wait(ctx context.Context, a Agent, hold time.Duration) (WaitResponse, error) {
	if a.ID == "" || core.IsSystem(a.ID) || a.Tier < 1 {
		return WaitResponse{}, fmt.Errorf("%w: agent %q tier %d", ErrInvalid, a.ID, a.Tier)
	}
	e.touch(a)
	hold = min(hold, e.cfg.MaxHold)
	type out struct {
		resp WaitResponse
		w    *waiter
	}
	o, err := do(ctx, e, func() (out, error) {
		since := e.markSeen(a.ID)
		if r, ok := e.tryMatch(a); ok {
			delete(e.seen, a.ID)
			return out{resp: r}, nil
		}
		if hold <= 0 {
			r := e.idleResponse(a.Tier, since)
			if v := log.V(vlog.LevelDebug); v.Enabled() {
				v.Info("wait answered", "agent", a.ID, "tier", a.Tier, "action", r.Action, "reason", r.Reason)
			}
			return out{resp: r}, nil
		}
		// An exit answer is immediate; there is nothing to hold the agent for.
		if r := e.idleResponse(a.Tier, since); r.Action == ActionExit {
			return out{resp: r}, nil
		}
		w := &waiter{agent: a, since: since, ch: make(chan WaitResponse, 1), deadline: e.now().Add(hold)}
		e.waiters[a.Tier] = append(e.waiters[a.Tier], w)
		return out{w: w}, nil
	})
	if err != nil {
		return WaitResponse{}, err
	}
	if o.w == nil {
		return o.resp, nil
	}
	select {
	case r, ok := <-o.w.ch:
		if !ok {
			return WaitResponse{}, ErrStopped
		}
		return r, nil
	case <-ctx.Done():
		e.abandon(o.w)
		return WaitResponse{}, ctx.Err()
	case <-e.dead:
		return WaitResponse{}, ErrStopped
	}
}

// markSeen returns when the agent's current stretch of waiting began. A gap of
// two holds means it went away, so its next call starts a new stretch.
func (e *Engine) markSeen(id string) time.Time {
	now := e.now()
	s, ok := e.seen[id]
	if !ok || now.Sub(s.last) > 2*e.cfg.MaxHold {
		s.first = now
	}
	s.last = now
	e.seen[id] = s
	return s.first
}

// abandon removes a waiter whose caller disconnected. If a grant raced with the
// disconnect, the lease goes straight back instead of waiting out its timeout.
func (e *Engine) abandon(w *waiter) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = do(ctx, e, func() (struct{}, error) {
		e.waiters[w.agent.Tier] = slices.DeleteFunc(e.waiters[w.agent.Tier], func(x *waiter) bool { return x == w })
		select {
		case r := <-w.ch:
			if r.Task != nil {
				_, _ = e.commit(r.Task.ID, core.Cmd{Kind: core.CmdExpire, Actor: "system:lease", Note: "caller_disconnected"})
			}
		default:
		}
		return struct{}{}, nil
	})
}

// Claim takes one specific ready task, for agents that preview before committing.
func (e *Engine) Claim(ctx context.Context, a Agent, id string) (WaitResponse, error) {
	if a.ID == "" || core.IsSystem(a.ID) || a.Tier < 1 {
		return WaitResponse{}, fmt.Errorf("%w: agent %q tier %d", ErrInvalid, a.ID, a.Tier)
	}
	e.touch(a)
	return do(ctx, e, func() (WaitResponse, error) {
		ent := e.tasks[id]
		if ent == nil {
			return WaitResponse{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		if ent.task.State != core.Ready || !e.eligible(a, ent.task) {
			return WaitResponse{}, fmt.Errorf("%w: %s is %s", ErrNoMatch, id, ent.task.State)
		}
		return e.grant(a, id, "work")
	})
}

func (e *Engine) grant(a Agent, id, job string) (WaitResponse, error) {
	t, err := e.commit(id, core.Cmd{
		Kind: core.CmdGrant, Actor: a.ID, AgentTier: a.Tier,
		LeaseTTL: e.cfg.LeaseTTL, ProgressTTL: e.cfg.ProgressTTL,
	})
	if err != nil {
		return WaitResponse{}, err
	}
	e.grants.Add(1)
	if v := log.V(vlog.LevelChanges); v.Enabled() {
		v.Info("grant", "task", id, "job", job, "agent", a.ID, "agent_tier", a.Tier, "task_tier", t.Tier, "lease", t.LeaseEpoch)
	}
	c := t.Clone()
	return WaitResponse{
		Action: ActionTask, Job: job, Task: &c, Lease: t.LeaseEpoch,
		LeaseTTLMs: e.cfg.LeaseTTL.Milliseconds(),
	}, nil
}

// eligible applies the filters: tier (with steal rules), tags and file conflicts.
func (e *Engine) eligible(a Agent, t core.Task) bool {
	if a.Tier < t.Tier {
		return false
	}
	if a.Tier > t.Tier && !e.stealAllowed(a.Tier, t) {
		return false
	}
	for _, tag := range t.Tags {
		if !slices.Contains(a.Tags, tag) {
			return false
		}
	}
	return !e.touchConflict(t)
}

func (e *Engine) stealAllowed(agentTier int, t core.Task) bool {
	for _, r := range e.cfg.Steal {
		if r.AgentTier == agentTier && r.TaskTier == t.Tier {
			return e.now().Sub(t.ReadyAt) >= r.After
		}
	}
	return false
}

func (e *Engine) touchConflict(t core.Task) bool {
	for id, paths := range e.active {
		if id == t.ID {
			continue
		}
		for _, a := range t.Touches {
			for _, b := range paths {
				if pathsOverlap(a, b) {
					return true
				}
			}
		}
	}
	return false
}

// pathsOverlap reports whether one path equals or contains the other, so "src"
// conflicts with "src/a.go" but "src2" does not.
func pathsOverlap(a, b string) bool {
	a, b = strings.TrimSuffix(a, "/"), strings.TrimSuffix(b, "/")
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// tryMatch finds work for an agent: review jobs first, then its own tier, then
// lower tiers it may steal from. It is the only place new leases are created.
func (e *Engine) tryMatch(a Agent) (WaitResponse, bool) {
	if r, ok := e.matchReview(a); ok {
		return r, true
	}
	for tier := a.Tier; tier >= 1; tier-- {
		if r, ok := e.matchTier(a, tier); ok {
			return r, true
		}
	}
	return WaitResponse{}, false
}

func (e *Engine) matchReview(a Agent) (WaitResponse, bool) {
	for i := 0; i < len(e.reviews); {
		id := e.reviews[i]
		ent := e.tasks[id]
		if ent == nil || !needsReviewer(ent.task) {
			e.reviews = slices.Delete(e.reviews, i, i+1)
			continue
		}
		t := ent.task
		if a.ID == t.Submitter || a.Tier < t.Tier {
			i++
			continue
		}
		if r, err := e.grant(a, id, "review"); err == nil {
			e.reviews = slices.Delete(e.reviews, i, i+1)
			return r, true
		}
		i++
	}
	return WaitResponse{}, false
}

func (e *Engine) matchTier(a Agent, tier int) (WaitResponse, bool) {
	h := e.ready[tier]
	if h == nil || h.Len() == 0 {
		return WaitResponse{}, false
	}
	var rejected []readyItem
	defer func() {
		for _, it := range rejected {
			heap.Push(h, it)
		}
	}()
	for h.Len() > 0 {
		it := heap.Pop(h).(readyItem)
		ent := e.tasks[it.id]
		// A task's older heap entries are skipped: only its latest seq counts.
		if ent == nil || ent.task.State != core.Ready || ent.readySeq != it.seq {
			continue
		}
		if ent.task.Tier != tier {
			continue
		}
		if !e.eligible(a, ent.task) {
			if v := log.V(vlog.LevelTrace); v.Enabled() {
				v.Info("not eligible", "task", it.id, "agent", a.ID, "agent_tier", a.Tier, "task_tier", ent.task.Tier, "needs_tags", ent.task.Tags, "file_conflict", e.touchConflict(ent.task))
			}
			rejected = append(rejected, it)
			continue
		}
		r, err := e.grant(a, it.id, "work")
		if err != nil {
			if !errors.Is(err, store.ErrConflict) {
				rejected = append(rejected, it)
			}
			continue
		}
		return r, true
	}
	return WaitResponse{}, false
}

// matchWaiters assigns available work, lowest tier first so that a task reaches
// an agent of its own tier before a higher tier can take it.
func (e *Engine) matchWaiters() {
	tiers := make([]int, 0, len(e.waiters))
	for t := range e.waiters {
		tiers = append(tiers, t)
	}
	slices.Sort(tiers)
	for _, tier := range tiers {
		for _, w := range slices.Clone(e.waiters[tier]) {
			r, ok := e.tryMatch(w.agent)
			if !ok {
				continue
			}
			delete(e.seen, w.agent.ID)
			e.reply(w, r)
		}
	}
}

// settleWaiters updates quiet timers, releases holds that ran out, and sends
// exit as soon as an agent's answer becomes exit.
func (e *Engine) settleWaiters() {
	now := e.now()
	e.updateQuiet(now)
	for _, ws := range e.waiters {
		for _, w := range slices.Clone(ws) {
			r := e.idleResponse(w.agent.Tier, w.since)
			if r.Action == ActionExit || !now.Before(w.deadline) {
				e.reply(w, r)
			}
		}
	}
}

// reach counts live tasks that could reach an agent of this tier, directly or by
// failing and escalating. Tasks above the tier can never come down to it.
func (e *Engine) reach(tier int) int {
	n := 0
	for t, states := range e.counts {
		if t > tier {
			continue
		}
		for s, c := range states {
			if s.Live() {
				n += c
			}
		}
	}
	return n
}

func (e *Engine) updateQuiet(now time.Time) {
	for tier := 1; tier <= e.cfg.Policy.MaxTier; tier++ {
		if e.planning.Active || e.reach(tier) > 0 {
			delete(e.quietSince, tier)
		} else if _, ok := e.quietSince[tier]; !ok {
			e.quietSince[tier] = now
		}
	}
}

func (e *Engine) planningView() *PlanningView {
	p := e.planning
	if p.Epoch == 0 {
		return nil
	}
	age := e.now().Sub(p.UpdatedAt)
	return &PlanningView{
		Epoch: p.Epoch, Active: p.Active, Phase: p.Phase, ExpectedTasks: p.Expected, ETAMs: p.ETAMs,
		AgeMs: age.Milliseconds(), Stale: p.Active && age > e.cfg.StaleAfter,
	}
}

// idleResponse decides wait or exit for an agent with nothing assigned. Exit is
// only ever sent when nothing alive could still produce work for this tier.
func (e *Engine) idleResponse(tier int, agentSince time.Time) WaitResponse {
	now := e.now()
	view := e.planningView()
	wait := func(reason string) WaitResponse {
		return WaitResponse{Action: ActionWait, Reason: reason, Planning: view}
	}
	if e.planning.Active {
		if e.planning.Expected != nil && e.reach(tier) == 0 {
			if expectedUpTo(e.planning.Expected, tier) == 0 {
				return WaitResponse{Action: ActionExit, Reason: ReasonNoneExpect, Planning: view}
			}
		}
		return wait(ReasonPlanning)
	}
	if !e.planning.LostAt.IsZero() && now.Sub(e.planning.LostAt) < e.cfg.PlanLostGrace {
		return wait(ReasonPlannerLost)
	}
	if e.reach(tier) > 0 {
		if e.onlyBlockedDrafts(tier) {
			return wait(ReasonDeps)
		}
		return wait(ReasonTierBusy)
	}
	since, ok := e.quietSince[tier]
	if !ok {
		since = now
	}
	if agentSince.After(since) {
		since = agentSince
	}
	if now.Sub(since) >= e.cfg.ExitGrace {
		return WaitResponse{Action: ActionExit, Reason: ReasonPlanDone, Planning: view}
	}
	return wait(ReasonIdle)
}

func (e *Engine) onlyBlockedDrafts(tier int) bool {
	drafts := 0
	for t, states := range e.counts {
		if t <= tier {
			drafts += states[core.Draft]
		}
	}
	return drafts > 0 && drafts == e.reach(tier)
}

// PlanBegin takes the planner lease BEFORE the planner reads its input, so no
// agent can be told to exit just before a plan lands.
func (e *Engine) PlanBegin(ctx context.Context, planner string, ttl time.Duration) (uint64, error) {
	return do(ctx, e, func() (uint64, error) {
		if e.planning.Active && e.now().Before(e.planning.Deadline) {
			return 0, fmt.Errorf("%w: %s", ErrPlanBusy, e.planning.Planner)
		}
		e.reconcile() // Clears orphan drafts before the committed epoch can advance past them.
		for _, ent := range e.tasks {
			if ent.task.State == core.Draft && ent.task.PlanEpoch > e.planning.Committed {
				return 0, fmt.Errorf("%w: orphan drafts still present", ErrInvalid)
			}
		}
		p := e.planning
		now := e.now()
		p.Epoch++
		p.Active, p.Planner, p.Phase, p.Expected, p.ETAMs = true, planner, "starting", nil, 0
		p.Deadline, p.UpdatedAt, p.LostAt = now.Add(ttl), now, time.Time{}
		if err := e.savePlanning(p); err != nil {
			return 0, err
		}
		if v := log.V(vlog.LevelSteady); v.Enabled() {
			v.Info("plan begun", "planner", planner, "epoch", p.Epoch, "ttl", ttl)
		}
		return p.Epoch, nil
	})
}

// PlanUpdate renews the lease and publishes hints to waiting agents.
func (e *Engine) PlanUpdate(ctx context.Context, planner string, epoch uint64, ttl time.Duration, phase string, expected map[int]int, etaMs int64) error {
	_, err := do(ctx, e, func() (struct{}, error) {
		if err := e.planFence(planner, epoch); err != nil {
			return struct{}{}, err
		}
		p := e.planning
		now := e.now()
		if expected == nil {
			expected = p.Expected // A phase-only update keeps the earlier promise.
		}
		// A hint is a promise: agents may already have left because of it. Found by
		// the Planner.tla model, whose NoExitBeforeWork fails without this rule.
		if p.Expected != nil {
			for tier := 1; tier <= e.cfg.Policy.MaxTier; tier++ {
				if expectedUpTo(p.Expected, tier) == 0 && expectedUpTo(expected, tier) > 0 {
					return struct{}{}, fmt.Errorf("%w: hints already promised no work up to tier %d", ErrInvalid, tier)
				}
			}
		}
		p.Phase, p.Expected, p.ETAMs, p.Deadline, p.UpdatedAt = phase, expected, etaMs, now.Add(ttl), now
		if v := log.V(vlog.LevelChanges); v.Enabled() {
			v.Info("plan updated", "planner", planner, "epoch", epoch, "phase", phase, "expected", expected)
		}
		return struct{}{}, e.savePlanning(p)
	})
	return err
}

// PlanAdd creates a draft task inside the plan. It stays invisible to agents
// until PlanCommit.
func (e *Engine) PlanAdd(ctx context.Context, planner string, epoch uint64, s TaskSpec) (core.Task, error) {
	return do(ctx, e, func() (core.Task, error) {
		if err := e.planFence(planner, epoch); err != nil {
			return core.Task{}, err
		}
		t, err := e.buildTask(s, epoch)
		if err != nil {
			return t, err
		}
		if e.planning.Expected != nil && expectedUpTo(e.planning.Expected, t.Tier) == 0 {
			return t, fmt.Errorf("%w: hints promised no work up to tier %d", ErrInvalid, t.Tier)
		}
		return e.create(t)
	})
}

// expectedUpTo counts hinted tasks that could reach an agent of this tier.
func expectedUpTo(expected map[int]int, tier int) int {
	n := 0
	for t, c := range expected {
		if t <= tier {
			n += c
		}
	}
	return n
}

// PlanCommit makes the whole plan visible with a single write to the planning
// key. Tasks of this epoch become publishable at that moment, not one by one.
func (e *Engine) PlanCommit(ctx context.Context, planner string, epoch uint64) error {
	_, err := do(ctx, e, func() (struct{}, error) {
		if err := e.planFence(planner, epoch); err != nil {
			return struct{}{}, err
		}
		if err := e.checkPlanGraph(epoch); err != nil {
			return struct{}{}, err
		}
		p := e.planning
		p.Committed, p.Active, p.Planner, p.Phase, p.Expected, p.ETAMs = epoch, false, "", "", nil, 0
		p.UpdatedAt = e.now()
		if err := e.savePlanning(p); err != nil {
			return struct{}{}, err
		}
		if v := log.V(vlog.LevelSteady); v.Enabled() {
			v.Info("plan committed", "planner", planner, "epoch", epoch)
		}
		e.dirty = true
		return struct{}{}, nil
	})
	return err
}

func (e *Engine) planFence(planner string, epoch uint64) error {
	if !e.planning.Active || e.planning.Epoch != epoch || e.planning.Planner != planner {
		return fmt.Errorf("%w: epoch %d is not active for %s", ErrPlanLost, epoch, planner)
	}
	return nil
}

// checkPlanGraph rejects missing dependencies and cycles, which would otherwise
// leave tasks waiting forever.
func (e *Engine) checkPlanGraph(epoch uint64) error {
	const (
		unseen = iota
		visiting
		finished
	)
	mark := map[string]int{}
	var visit func(id string) error
	visit = func(id string) error {
		switch mark[id] {
		case visiting:
			return fmt.Errorf("%w: dependency cycle through %s", ErrInvalid, id)
		case finished:
			return nil
		}
		ent := e.tasks[id]
		if ent == nil {
			return fmt.Errorf("%w: unknown dependency %s", ErrInvalid, id)
		}
		mark[id] = visiting
		for _, d := range ent.task.Deps {
			if err := visit(d); err != nil {
				return err
			}
		}
		mark[id] = finished
		return nil
	}
	for id, ent := range e.tasks {
		if ent.task.PlanEpoch == epoch {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) savePlanning(p store.Planning) error {
	rev, err := e.st.PutPlanning(p, e.planRev)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			if cur, curRev, gerr := e.st.GetPlanning(); gerr == nil {
				e.planning, e.planRev = cur, curRev
			}
		}
		return err
	}
	e.planning, e.planRev = p, rev
	e.dirty = true
	return nil
}

// Situation is a snapshot of the swarm. Every value carries its age, so a reader
// can tell fresh data from old.
type Situation struct {
	Agent        string                 `json:"agent,omitempty"`
	Counts       map[int]map[string]int `json:"counts"`
	Planning     *PlanningView          `json:"planning,omitempty"`
	Advice       string                 `json:"advice"`
	WaitingAgent map[int]int            `json:"waiting_agents"`
	OldestReady  int64                  `json:"oldest_ready_age_ms"`
	Grants       uint64                 `json:"grants"`
	AgeMs        int64                  `json:"age_ms"`
	Stale        bool                   `json:"stale"`
}

// Situation returns the snapshot for an agent of the given tier.
func (e *Engine) Situation(ctx context.Context, a Agent) (Situation, error) {
	return do(ctx, e, func() (Situation, error) {
		s := Situation{
			Agent: a.ID, Counts: map[int]map[string]int{}, WaitingAgent: map[int]int{},
			Planning: e.planningView(), Grants: e.grants.Load(),
		}
		for tier, states := range e.counts {
			m := map[string]int{}
			for st, n := range states {
				if n > 0 {
					m[string(st)] = n
				}
			}
			s.Counts[tier] = m
		}
		for tier, ws := range e.waiters {
			s.WaitingAgent[tier] = len(ws)
		}
		now := e.now()
		for _, ent := range e.tasks {
			if ent.task.State == core.Ready {
				s.OldestReady = max(s.OldestReady, now.Sub(ent.task.ReadyAt).Milliseconds())
			}
		}
		tier := a.Tier
		if tier == 0 {
			tier = e.cfg.Policy.MaxTier
		}
		r := e.idleResponse(tier, time.Time{})
		s.Advice = r.Action
		if r.Reason != "" {
			s.Advice += ": " + r.Reason
		}
		return s, nil
	})
}
