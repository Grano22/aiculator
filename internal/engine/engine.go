// Package engine is the single writer. One goroutine owns all task state, checks
// each command with core.Apply, commits it with a compare-and-swap, and only then
// wakes whoever is waiting. Tasks are assigned here rather than pulled from a
// queue, so the dispatcher can choose by tier, tags and file conflicts.
package engine

import (
	"container/heap"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/vlog"
	"github.com/Grano22/aiculator/pkg/observe"
)

var log = vlog.For("engine")

// Clock is swappable so that lease tests do not need to sleep.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Agent is an authenticated agent identity.
type Agent struct {
	ID   string   `json:"id"`
	Tier int      `json:"tier"`
	Tags []string `json:"tags,omitempty"`
}

// StealRule lets idle agents of a higher tier take a lower tier's tasks once
// those have waited for After. Pairs with no rule never steal.
type StealRule struct {
	AgentTier, TaskTier int
	After               time.Duration
}

// Hooks hand tasks to the work layer. They must return quickly and be safe to
// call more than once, because recovery calls them again after a restart.
type Hooks struct {
	OnChecks  func(core.Task) // A review task needs machine checks.
	OnMerging func(core.Task) // A verified task needs merging.
}

// Config tunes the engine. Zero values take the defaults from DefaultConfig.
type Config struct {
	Policy        core.Policy
	Clock         Clock
	LeaseTTL      time.Duration
	ProgressTTL   time.Duration
	MaxHold       time.Duration // Longest wait_for_task hold.
	ExitGrace     time.Duration // Quiet time before an agent is told to exit.
	PlanLostGrace time.Duration // How long waiters see planner_lost.
	RestartGrace  time.Duration // No lease expires this soon after a restart.
	StaleAfter    time.Duration // Age at which a read value is flagged stale.
	Tick          time.Duration
	AgingUnit     time.Duration    // Waiting this long equals one priority point.
	Aging         []core.AgingStep // Extra boosts after a wait. A task's own steps replace these.
	Steal         []StealRule
	PresenceTTL   time.Duration
	Hooks         Hooks
}

// DefaultConfig holds production defaults.
func DefaultConfig() Config {
	return Config{
		Policy:        core.DefaultPolicy(),
		Clock:         realClock{},
		LeaseTTL:      2 * time.Minute,
		ProgressTTL:   10 * time.Minute,
		MaxHold:       40 * time.Second,
		ExitGrace:     30 * time.Second,
		PlanLostGrace: 30 * time.Second,
		RestartGrace:  15 * time.Second,
		StaleAfter:    30 * time.Second,
		Tick:          25 * time.Millisecond,
		AgingUnit:     time.Minute,
		Steal: []StealRule{
			{AgentTier: 2, TaskTier: 1, After: 2 * time.Minute},
			{AgentTier: 3, TaskTier: 2, After: 5 * time.Minute},
		},
		PresenceTTL: 30 * time.Second,
	}
}

// Errors returned by the engine, on top of those from core.
var (
	ErrNotFound = errors.New("task not found")
	ErrExists   = errors.New("task already exists")
	ErrInvalid  = errors.New("invalid request")
	ErrPlanBusy = errors.New("another planner holds the lease")
	ErrPlanLost = errors.New("planning lease lost")
	ErrStopped  = errors.New("engine stopped")
	ErrNoMatch  = errors.New("task is not available to this agent")
)

type entry struct {
	task     core.Task
	rev      uint64
	readySeq uint64
}

type op struct {
	fn   func()
	done chan struct{}
}

// Engine is the single writer. All fields below ops are owned by run.
type Engine struct {
	cfg   Config
	st    *store.Store
	ops   chan op
	stop  chan struct{}
	dead  chan struct{}
	start time.Time

	presence chan Agent
	obs      *observe.Collector
	grants   atomic.Uint64

	tasks      map[string]*entry
	ready      map[int]*readyHeap
	boosts     boostHeap
	seq        uint64
	reviews    []string
	drafts     map[string]bool
	failed     map[string]bool
	leased     map[string]bool
	merging    map[string]bool
	active     map[string][]string // Task ID → touched paths, while being worked on.
	counts     map[int]map[core.State]int
	waiters    map[int][]*waiter
	dirty      bool
	planning   store.Planning
	planRev    uint64
	quietSince map[int]time.Time
	seen       map[string]seenAgent
}

// seenAgent tracks when an agent started its current stretch of waiting, so that
// one arriving at a long-idle swarm still gets the full grace before an exit.
type seenAgent struct {
	first, last time.Time
}

// New starts the engine: it loads all tasks, rebuilds the indexes, and resumes
// any work that a crash interrupted.
func New(ctx context.Context, st *store.Store, cfg Config) (*Engine, error) {
	d := DefaultConfig()
	if cfg.Policy.MaxTier == 0 {
		cfg.Policy = d.Policy
	}
	if cfg.Clock == nil {
		cfg.Clock = d.Clock
	}
	setDur := func(p *time.Duration, v time.Duration) {
		if *p == 0 {
			*p = v
		}
	}
	setDur(&cfg.LeaseTTL, d.LeaseTTL)
	setDur(&cfg.ProgressTTL, d.ProgressTTL)
	setDur(&cfg.MaxHold, d.MaxHold)
	setDur(&cfg.ExitGrace, d.ExitGrace)
	setDur(&cfg.PlanLostGrace, d.PlanLostGrace)
	setDur(&cfg.RestartGrace, d.RestartGrace)
	setDur(&cfg.StaleAfter, d.StaleAfter)
	setDur(&cfg.Tick, d.Tick)
	setDur(&cfg.AgingUnit, d.AgingUnit)
	setDur(&cfg.PresenceTTL, d.PresenceTTL)
	if cfg.Steal == nil {
		cfg.Steal = d.Steal
	}

	e := &Engine{
		cfg: cfg, st: st,
		ops:        make(chan op, 1024),
		stop:       make(chan struct{}),
		dead:       make(chan struct{}),
		start:      cfg.Clock.Now(),
		presence:   make(chan Agent, 256),
		obs:        observe.New(time.Now()),
		tasks:      map[string]*entry{},
		ready:      map[int]*readyHeap{},
		drafts:     map[string]bool{},
		failed:     map[string]bool{},
		leased:     map[string]bool{},
		merging:    map[string]bool{},
		active:     map[string][]string{},
		counts:     map[int]map[core.State]int{},
		waiters:    map[int][]*waiter{},
		quietSince: map[int]time.Time{},
		seen:       map[string]seenAgent{},
	}
	stored, err := st.LoadTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("load tasks: %w", err)
	}
	for _, s := range stored {
		e.index(core.Task{}, s.Task, s.Rev)
		// Hooks are not fired from index during load; resume them here.
		e.resume(s.Task)
	}
	if e.planning, e.planRev, err = st.GetPlanning(); err != nil {
		return nil, fmt.Errorf("load planning: %w", err)
	}
	e.dirty = true
	log.V(vlog.LevelDefault).Info("engine started", "tasks", len(stored), "planning_epoch", e.planning.Epoch)
	go e.run()
	go e.presenceLoop()
	return e, nil
}

func (e *Engine) resume(t core.Task) {
	switch {
	case t.State == core.Review && len(t.Checks) > 0 && !t.ChecksRun && e.cfg.Hooks.OnChecks != nil:
		e.cfg.Hooks.OnChecks(t)
	case t.State == core.Merging && e.cfg.Hooks.OnMerging != nil:
		e.cfg.Hooks.OnMerging(t)
	}
}

// Close stops the writer goroutine and waits for it.
func (e *Engine) Close() {
	select {
	case <-e.stop:
	default:
		close(e.stop)
	}
	<-e.dead
}

func (e *Engine) run() {
	defer close(e.dead)
	tick := time.NewTicker(e.cfg.Tick)
	defer tick.Stop()
	e.pump(true)
	for {
		select {
		case o := <-e.ops:
			o.fn()
			e.pump(false)
			close(o.done)
		case <-tick.C:
			e.pump(true)
		case <-e.stop:
			for _, ws := range e.waiters {
				for _, w := range ws {
					close(w.ch)
				}
			}
			return
		}
	}
}

// do runs fn on the writer goroutine and waits for it.
func do[T any](ctx context.Context, e *Engine, fn func() (T, error)) (T, error) {
	var (
		res T
		err error
	)
	o := op{fn: func() { res, err = fn() }, done: make(chan struct{})}
	select {
	case e.ops <- o:
	case <-e.stop:
		return res, ErrStopped
	case <-ctx.Done():
		return res, ctx.Err()
	}
	select {
	case <-o.done:
		return res, err
	case <-e.dead:
		return res, ErrStopped
	}
}

func (e *Engine) now() time.Time { return e.cfg.Clock.Now() }

// index moves a task between the in-memory indexes. Everything it maintains can
// be rebuilt from KV, which is how recovery works.
func (e *Engine) index(old, t core.Task, rev uint64) {
	ent := e.tasks[t.ID]
	if ent == nil {
		ent = &entry{}
		e.tasks[t.ID] = ent
	}
	if old.ID != "" && e.counts[old.Tier] != nil {
		e.counts[old.Tier][old.State]--
	}
	ent.task, ent.rev = t, rev
	if e.counts[t.Tier] == nil {
		e.counts[t.Tier] = map[core.State]int{}
	}
	e.counts[t.Tier][t.State]++

	setIf := func(m map[string]bool, on bool) {
		if on {
			m[t.ID] = true
		} else {
			delete(m, t.ID)
		}
	}
	setIf(e.drafts, t.State == core.Draft)
	setIf(e.failed, t.State == core.Failed)
	setIf(e.merging, t.State == core.Merging)
	setIf(e.leased, t.Owner != "")

	// A new priority needs a new heap position too. The old entry is skipped later.
	if t.State == core.Ready && (old.State != core.Ready || old.ID == "" ||
		old.Priority != t.Priority || !slices.Equal(old.Aging, t.Aging)) {
		e.enqueue(ent)
	}
	if t.State == core.Review && needsReviewer(t) && !slices.Contains(e.reviews, t.ID) {
		e.reviews = append(e.reviews, t.ID)
	}
	switch t.State {
	case core.Claimed, core.Running, core.Review, core.Merging:
		if len(t.Touches) > 0 {
			e.active[t.ID] = t.Touches
		}
	default:
		delete(e.active, t.ID)
	}
	// Only a change that could make work available is worth waking waiters for. A heartbeat
	// is by far the most common command, and is not one of those.
	if old.ID == "" || old.State != t.State || old.Owner != t.Owner || old.Tier != t.Tier || old.Priority != t.Priority {
		e.dirty = true
	}
}

// agingOf returns the steps that apply to a task.
func (e *Engine) agingOf(t core.Task) []core.AgingStep {
	if t.Aging != nil {
		return t.Aging
	}
	return e.cfg.Aging
}

// enqueue gives a ready task its place in the heap, and schedules its next boost. Every
// value comes from the task and the clock, so restarting rebuilds exactly the same queue.
func (e *Engine) enqueue(ent *entry) {
	t := ent.task
	boost, next := core.AgingBoost(e.agingOf(t), e.now().Sub(t.ReadyAt))
	e.seq++
	ent.readySeq = e.seq
	e.heap(t.Tier).pushItem(readyItem{
		id:  t.ID,
		key: int64(t.Priority+boost)*int64(e.cfg.AgingUnit) - t.ReadyAt.UnixNano(),
		seq: e.seq,
	})
	if next > 0 {
		heap.Push(&e.boosts, boostItem{at: e.now().Add(next), id: t.ID, seq: e.seq})
	}
}

// applyBoosts moves tasks that have just reached an aging step. The cost is per boost,
// and nothing is re-sorted, so a claim stays as fast with aging on as with it off.
func (e *Engine) applyBoosts() {
	now := e.now()
	for len(e.boosts) > 0 && !e.boosts[0].at.After(now) {
		it := heap.Pop(&e.boosts).(boostItem)
		ent := e.tasks[it.id]
		if ent == nil || ent.task.State != core.Ready || ent.readySeq != it.seq {
			continue // It was claimed or moved since, so this boost no longer applies.
		}
		e.enqueue(ent)
		if v := log.V(vlog.LevelChanges); v.Enabled() {
			v.Info("priority raised by waiting", "task", it.id, "waited", now.Sub(ent.task.ReadyAt))
		}
		e.dirty = true
	}
}

// PriorityInfo says how a task's place in the queue is worked out right now.
type PriorityInfo struct {
	Base      int   `json:"base"`
	Boost     int   `json:"boost"`
	Effective int   `json:"effective"`
	WaitedMs  int64 `json:"waited_ms"`
	NextInMs  int64 `json:"next_in_ms,omitempty"`
	// AgingPoints is the steady rise on top, one point per aging unit.
	AgingPoints int `json:"aging_points"`
}

// Priority explains a task's current priority.
func (e *Engine) Priority(ctx context.Context, id string) (PriorityInfo, error) {
	return do(ctx, e, func() (PriorityInfo, error) {
		ent := e.tasks[id]
		if ent == nil {
			return PriorityInfo{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		t := ent.task
		p := PriorityInfo{Base: t.Priority, Effective: t.Priority}
		if t.State != core.Ready {
			return p, nil
		}
		waited := e.now().Sub(t.ReadyAt)
		boost, next := core.AgingBoost(e.agingOf(t), waited)
		p.Boost, p.Effective = boost, t.Priority+boost
		p.WaitedMs, p.NextInMs = waited.Milliseconds(), next.Milliseconds()
		p.AgingPoints = int(waited / e.cfg.AgingUnit)
		return p, nil
	})
}

func needsReviewer(t core.Task) bool {
	if t.State != core.Review || t.Owner != "" {
		return false
	}
	if len(t.Checks) == 0 {
		return true
	}
	return t.RequireReview && t.ChecksRun && t.ChecksPassed
}

func (e *Engine) heap(tier int) *readyHeap {
	h := e.ready[tier]
	if h == nil {
		h = &readyHeap{}
		e.ready[tier] = h
	}
	return h
}

// commit is the only path to a state change: legality check, compare-and-swap,
// then memory. If the swap loses, memory is reloaded and the command is rejected.
func (e *Engine) commit(id string, c core.Cmd) (core.Task, error) {
	ent := e.tasks[id]
	if ent == nil {
		return core.Task{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	n, err := core.Apply(ent.task, c, e.now(), e.cfg.Policy)
	if err != nil {
		if v := log.V(vlog.LevelChanges); v.Enabled() {
			v.Info("command rejected", "task", id, "kind", c.Kind, "actor", c.Actor, "state", ent.task.State, "err", err)
		}
		return ent.task, err
	}
	began := time.Now()
	rev, err := e.st.UpdateTask(n, ent.rev)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			log.V(vlog.LevelDefault).Warn("stale revision: another writer changed this task", "task", id, "rev", ent.rev)
			e.reload(id)
		} else {
			log.Error("commit failed", "task", id, "kind", c.Kind, "err", err)
		}
		return ent.task, err
	}
	took := time.Since(began)
	e.obs.Observe(observe.LatencyCommit, took)
	if v := log.V(vlog.LevelChanges); v.Enabled() {
		v.Info("commit", "task", id, "kind", c.Kind, "actor", c.Actor, "took", took)
	}
	if v := log.V(vlog.LevelTrace); v.Enabled() {
		v.Info("compare-and-swap", "task", id, "from_rev", ent.rev, "to_rev", rev, "lease", n.LeaseEpoch)
	}
	old := ent.task
	e.index(old, n, rev)
	e.record(old, n, c)
	e.fireHooks(old, n)
	return n, nil
}

// record feeds the dashboards. Rates use real time. Task ages use the engine's clock,
// which is what their timestamps came from.
func (e *Engine) record(old, n core.Task, c core.Cmd) {
	wall := time.Now()
	e.obs.Count(observe.SeriesCommits, wall, 1)
	if !core.IsSystem(c.Actor) && c.Actor != "" && (c.Cost > 0 || c.Tokens > 0) {
		e.obs.Usage(c.Actor, max(c.Tokens, 0), max(c.Cost, 0))
	}
	if old.State == n.State {
		return
	}
	e.obs.Transition(string(old.State), string(n.State))
	if v := log.V(vlog.LevelSteady); v.Enabled() {
		v.Info("task transition", "task", n.ID, "from", old.State, "to", n.State, "tier", n.Tier, "attempt", n.Attempt, "actor", c.Actor, "reason", n.Reason)
	}
	now := e.now()
	switch n.State {
	case core.Claimed:
		e.obs.Count(observe.SeriesGrants, wall, 1)
		e.obs.Observe(observe.LatencyQueue, now.Sub(old.ReadyAt))
	case core.Done:
		e.obs.Count(observe.SeriesDone, wall, 1)
		e.obs.Observe(observe.LatencyCycle, now.Sub(n.CreatedAt))
		if n.Submitter != "" {
			e.obs.Outcome(n.Submitter, true)
		}
	case core.Failed:
		e.obs.Count(observe.SeriesFailed, wall, 1)
		who := old.Owner
		if who == "" {
			who = n.Submitter
		}
		if who != "" {
			e.obs.Outcome(who, false)
		}
		if n.Reason == "no_progress" {
			e.obs.Count(observe.SeriesZombies, wall, 1)
		}
	}
}

// Collector exposes the metrics, so the API can add its own request timings.
func (e *Engine) Collector() *observe.Collector { return e.obs }

func (e *Engine) reload(id string) {
	t, rev, err := e.st.GetTask(id)
	if err == nil {
		e.index(e.tasks[id].task, t, rev)
	}
}

func (e *Engine) fireHooks(old, n core.Task) {
	if n.State == core.Review && old.State != core.Review && len(n.Checks) > 0 && e.cfg.Hooks.OnChecks != nil {
		e.cfg.Hooks.OnChecks(n)
	}
	if n.State == core.Merging && old.State != core.Merging && e.cfg.Hooks.OnMerging != nil {
		e.cfg.Hooks.OnMerging(n)
	}
}

// pump runs after every command and on every tick, until nothing is left to do.
func (e *Engine) pump(tick bool) {
	if tick {
		e.sweep()
		e.dirty = true
	}
	changed := e.dirty
	for range 64 { // Bounded, so a bug cannot hang the writer.
		if !e.dirty {
			break
		}
		e.dirty = false
		e.reconcile()
		e.matchWaiters()
	}
	if changed {
		e.settleWaiters()
	}
}

// sweep enforces deadlines. It runs on the writer's clock, so tests can drive it.
func (e *Engine) sweep() {
	e.applyBoosts()
	now := e.now()
	if e.planning.Active && now.After(e.planning.Deadline) {
		log.V(vlog.LevelDefault).Warn("planner lease expired", "planner", e.planning.Planner, "epoch", e.planning.Epoch)
		p := e.planning
		p.Active, p.Planner, p.Phase, p.Expected, p.ETAMs = false, "", "", nil, 0
		p.Epoch++ // A planner returning from the dead now holds an old epoch.
		p.LostAt, p.UpdatedAt = now, now
		e.savePlanning(p)
	}
	if now.Sub(e.start) < e.cfg.RestartGrace {
		// Agents could not reach us while we were down, so give them time to
		// come back before their leases count against them.
		return
	}
	for id := range e.leased {
		t := e.tasks[id].task
		switch {
		case now.After(t.LeaseDeadline):
			_, _ = e.commit(id, core.Cmd{Kind: core.CmdExpire, Actor: "system:lease", Note: "lease_expired"})
		case t.State == core.Running && !t.ProgressDeadline.IsZero() && now.After(t.ProgressDeadline):
			// Heartbeats are alive but nothing has changed: a zombie.
			_, _ = e.commit(id, core.Cmd{Kind: core.CmdExpire, Actor: "system:lease", Note: "no_progress"})
		}
	}
}

// reconcile drives every automatic transition. Each is computed from durable
// state, so a crash between two steps is simply finished on restart.
func (e *Engine) reconcile() {
	for id := range e.failed {
		_, _ = e.commit(id, core.Cmd{Kind: core.CmdRecover, Actor: "system:recover"})
	}
	for id := range e.drafts {
		t := e.tasks[id].task
		switch e.draftFate(t) {
		case fatePublish:
			_, _ = e.commit(id, core.Cmd{Kind: core.CmdPublish, Actor: "system:plan"})
		case fateOrphan:
			_, _ = e.commit(id, core.Cmd{Kind: core.CmdCancel, Actor: "system:plan", Note: "plan_abandoned"})
		case fateDepCanceled:
			_, _ = e.commit(id, core.Cmd{Kind: core.CmdCancel, Actor: "system:plan", Note: "dependency_canceled"})
		}
	}
	if e.cfg.Hooks.OnMerging == nil {
		for id := range e.merging {
			_, _ = e.commit(id, core.Cmd{Kind: core.CmdMerged, Actor: "system:merge"})
		}
	}
}

type draftFate int

const (
	fateWait draftFate = iota
	fatePublish
	fateOrphan
	fateDepCanceled
)

func (e *Engine) draftFate(t core.Task) draftFate {
	if t.PlanEpoch != 0 && t.PlanEpoch > e.planning.Committed {
		if !e.planning.Active || t.PlanEpoch != e.planning.Epoch {
			return fateOrphan
		}
		return fateWait
	}
	for _, d := range t.Deps {
		dep := e.tasks[d]
		if dep == nil {
			return fateWait
		}
		switch dep.task.State {
		case core.Done:
		case core.Canceled:
			return fateDepCanceled
		default:
			return fateWait
		}
	}
	return fatePublish
}

// TaskSpec is the input for creating a task.
type TaskSpec struct {
	ID            string            `json:"id,omitempty"`
	Title         string            `json:"title"`
	Spec          string            `json:"spec,omitempty"`
	Tier          int               `json:"tier,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Priority      int               `json:"priority,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Aging         []core.AgingStep  `json:"aging,omitempty"`
	Deps          []string          `json:"deps,omitempty"`
	Touches       []string          `json:"touches,omitempty"`
	Checks        []string          `json:"checks,omitempty"`
	RequireReview bool              `json:"require_review,omitempty"`
	AllowEmpty    bool              `json:"allow_empty,omitempty"`
	Budget        int64             `json:"budget,omitempty"`
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "t-" + hex.EncodeToString(b[:])
}

func (e *Engine) buildTask(s TaskSpec, epoch uint64) (core.Task, error) {
	if s.ID == "" {
		s.ID = newID()
	}
	if !idRE.MatchString(s.ID) {
		return core.Task{}, fmt.Errorf("%w: task id %q", ErrInvalid, s.ID)
	}
	if strings.TrimSpace(s.Title) == "" {
		return core.Task{}, fmt.Errorf("%w: task needs a title", ErrInvalid)
	}
	if s.Tier == 0 {
		s.Tier = 1
	}
	if s.Tier < 1 || s.Tier > e.cfg.Policy.MaxTier {
		return core.Task{}, fmt.Errorf("%w: tier %d", ErrInvalid, s.Tier)
	}
	now := e.now()
	t := core.Task{
		ID: s.ID, Title: s.Title, Spec: s.Spec, Tier: s.Tier, Tags: s.Tags, Priority: s.Priority,
		Labels: s.Labels, Aging: s.Aging,
		Deps: s.Deps, Touches: s.Touches, Checks: s.Checks, RequireReview: s.RequireReview,
		AllowEmpty: s.AllowEmpty, Budget: s.Budget, PlanEpoch: epoch, State: core.Draft,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := core.ValidateAttrs(t); err != nil {
		return core.Task{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return t, nil
}

func (e *Engine) create(t core.Task) (core.Task, error) {
	if e.tasks[t.ID] != nil {
		return core.Task{}, fmt.Errorf("%w: %s", ErrExists, t.ID)
	}
	rev, err := e.st.CreateTask(t)
	if errors.Is(err, store.ErrConflict) {
		return core.Task{}, fmt.Errorf("%w: %s", ErrExists, t.ID)
	}
	if err != nil {
		return core.Task{}, err
	}
	e.index(core.Task{}, t, rev)
	e.obs.Count(observe.SeriesCommits, time.Now(), 1)
	e.obs.Transition("new", string(t.State))
	if v := log.V(vlog.LevelSteady); v.Enabled() {
		v.Info("task created", "task", t.ID, "title", t.Title, "tier", t.Tier, "plan_epoch", t.PlanEpoch)
	}
	return t, nil
}

// AddTask creates a task outside any plan. Its dependencies must already exist,
// which also makes cycles impossible on this path.
func (e *Engine) AddTask(ctx context.Context, s TaskSpec) (core.Task, error) {
	return do(ctx, e, func() (core.Task, error) {
		t, err := e.buildTask(s, 0)
		if err != nil {
			return t, err
		}
		for _, d := range t.Deps {
			if e.tasks[d] == nil {
				return t, fmt.Errorf("%w: unknown dependency %s", ErrInvalid, d)
			}
		}
		return e.create(t)
	})
}

// Get returns one task.
func (e *Engine) Get(ctx context.Context, id string) (core.Task, error) {
	return do(ctx, e, func() (core.Task, error) {
		if ent := e.tasks[id]; ent != nil {
			return ent.task.Clone(), nil
		}
		return core.Task{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	})
}

// List returns every task, sorted by ID.
func (e *Engine) List(ctx context.Context) ([]core.Task, error) {
	return do(ctx, e, func() ([]core.Task, error) {
		out := make([]core.Task, 0, len(e.tasks))
		for _, ent := range e.tasks {
			out = append(out, ent.task.Clone())
		}
		slices.SortFunc(out, func(a, b core.Task) int { return strings.Compare(a.ID, b.ID) })
		return out, nil
	})
}

// Heartbeat extends a lease. It proves the agent is alive, not that it is working.
func (e *Engine) Heartbeat(ctx context.Context, a Agent, id string, lease uint64) (core.Task, error) {
	e.touch(a)
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{Kind: core.CmdHeartbeat, Actor: a.ID, Lease: lease, LeaseTTL: e.cfg.LeaseTTL})
	})
}

// Progress records evidence of work. Only changed evidence resets the zombie timer.
func (e *Engine) Progress(ctx context.Context, a Agent, id string, lease uint64, evidence, ref, note string, u core.Usage) (core.Task, error) {
	e.touch(a)
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{
			Kind: core.CmdProgress, Actor: a.ID, Lease: lease, LeaseTTL: e.cfg.LeaseTTL,
			ProgressTTL: e.cfg.ProgressTTL, Evidence: evidence, Ref: ref, Note: note, Cost: u.Cost, Tokens: u.Tokens,
		})
	})
}

// Submit moves the task to review. It is a claim of completion, nothing more.
func (e *Engine) Submit(ctx context.Context, a Agent, id string, lease uint64, evidence, ref, note string, u core.Usage) (core.Task, error) {
	e.touch(a)
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{Kind: core.CmdSubmit, Actor: a.ID, Lease: lease, Evidence: evidence, Ref: ref, Note: note, Cost: u.Cost, Tokens: u.Tokens})
	})
}

// Fail reports that the agent gave up on the task.
func (e *Engine) Fail(ctx context.Context, a Agent, id string, lease uint64, note string, u core.Usage) (core.Task, error) {
	e.touch(a)
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{Kind: core.CmdFail, Actor: a.ID, Lease: lease, Note: note, Cost: u.Cost, Tokens: u.Tokens})
	})
}

// Release hands a task back with no penalty, because the harness was at fault.
func (e *Engine) Release(ctx context.Context, a Agent, id string, lease uint64, note string) (core.Task, error) {
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{Kind: core.CmdRelease, Actor: a.ID, Lease: lease, Note: note})
	})
}

// Verdict records an independent review.
func (e *Engine) Verdict(ctx context.Context, a Agent, id string, lease uint64, pass bool, note string) (core.Task, error) {
	e.touch(a)
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{Kind: core.CmdVerdict, Actor: a.ID, Lease: lease, Pass: pass, Note: note})
	})
}

// ReportChecks records a machine check result. Only the work layer calls it.
func (e *Engine) ReportChecks(ctx context.Context, id string, pass bool, note string) (core.Task, error) {
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{Kind: core.CmdChecks, Actor: "system:checks", Pass: pass, Note: note})
	})
}

// ReportMerged marks a task done. Only the merge queue calls it.
func (e *Engine) ReportMerged(ctx context.Context, id, ref string) (core.Task, error) {
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{Kind: core.CmdMerged, Actor: "system:merge", Ref: ref})
	})
}

// SystemFail fails a task on the swarm's own authority, for example on a merge conflict.
func (e *Engine) SystemFail(ctx context.Context, id, note string) (core.Task, error) {
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{Kind: core.CmdFail, Actor: "system:work", Note: note})
	})
}

// Unpark returns a parked task to the queue, optionally raising its budget.
func (e *Engine) Unpark(ctx context.Context, id string, addBudget int64, note string) (core.Task, error) {
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{Kind: core.CmdUnpark, Actor: "system:operator", AddBudget: addBudget, Note: note})
	})
}

// Amend changes what a task says, without touching its progress. An operator or a
// connector calls it, never an agent. A task that is being worked on keeps its lease.
func (e *Engine) Amend(ctx context.Context, id, actor string, a core.Amendment) (core.Task, error) {
	if !core.IsSystem(actor) {
		return core.Task{}, fmt.Errorf("%w: actor %q", core.ErrForbidden, actor)
	}
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{Kind: core.CmdAmend, Actor: actor, Amend: &a})
	})
}

// Cancel stops a task.
func (e *Engine) Cancel(ctx context.Context, id, note string) (core.Task, error) {
	return do(ctx, e, func() (core.Task, error) {
		return e.commit(id, core.Cmd{Kind: core.CmdCancel, Actor: "system:operator", Note: note})
	})
}

func (e *Engine) touch(a Agent) {
	e.obs.Seen(a.ID, a.Tier, time.Now())
	select {
	case e.presence <- a:
	default: // Presence is informational; never block work on it.
	}
}

func (e *Engine) presenceLoop() {
	last := map[string]time.Time{}
	for {
		select {
		case <-e.stop:
			return
		case a := <-e.presence:
			now := time.Now()
			if now.Sub(last[a.ID]) < e.cfg.PresenceTTL/2 {
				continue
			}
			last[a.ID] = now
			if err := e.st.TouchPresence(a.ID, []byte(fmt.Sprintf("tier=%d", a.Tier)), e.cfg.PresenceTTL); err != nil {
				log.V(vlog.LevelDebug).Warn("presence update failed", "agent", a.ID, "err", err)
			}
		}
	}
}

// GrantCount returns how many grants have been committed since start.
func (e *Engine) GrantCount() uint64 { return e.grants.Load() }

// Sync waits until the writer has processed everything queued before the call.
func (e *Engine) Sync(ctx context.Context) error {
	_, err := do(ctx, e, func() (struct{}, error) { return struct{}{}, nil })
	return err
}
