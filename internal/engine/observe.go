package engine

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/pkg/observe"
)

// maxLiveTasks caps the task list, so a huge queue cannot make every dashboard refresh
// heavy. Tasks with problems sort first, so they are what stays in view.
const maxLiveTasks = 200

// atRiskFraction marks a lease as at risk when less than this share of it is left.
const atRiskFraction = 4

func remaining(deadline, now time.Time) *int64 {
	if deadline.IsZero() {
		return nil
	}
	ms := deadline.Sub(now).Milliseconds()
	return &ms
}

func (e *Engine) liveTask(t core.Task, now time.Time) observe.TaskLive {
	l := observe.TaskLive{
		ID: t.ID, Title: t.Title, State: string(t.State), Tier: t.Tier, Owner: t.Owner, Attempt: t.Attempt,
		Failures: t.Failures, AgeMs: now.Sub(t.UpdatedAt).Milliseconds(), Reason: t.Reason,
		Tokens: t.Tokens, Spent: t.Spent, Budget: t.Budget, Health: observe.HealthQueued,
	}
	if t.Owner == "" {
		return l
	}
	l.LeaseRemainingMs = remaining(t.LeaseDeadline, now)
	if t.State == core.Running || t.State == core.Claimed {
		l.ProgressRemaining = remaining(t.ProgressDeadline, now)
	}
	l.Health = observe.HealthOK
	check := func(left *int64, ttl time.Duration) {
		switch {
		case left == nil:
		case *left <= 0:
			l.Health = observe.HealthExpired
		case *left < ttl.Milliseconds()/atRiskFraction && l.Health == observe.HealthOK:
			l.Health = observe.HealthAtRisk
		}
	}
	check(l.LeaseRemainingMs, e.cfg.LeaseTTL)
	check(l.ProgressRemaining, e.cfg.ProgressTTL)
	return l
}

func healthRank(h, state string) int {
	switch {
	case h == observe.HealthExpired:
		return 0
	case h == observe.HealthAtRisk:
		return 1
	case state == string(core.Parked):
		return 2
	case state == string(core.Failed):
		return 3
	case h == observe.HealthOK:
		return 4
	}
	return 5
}

// Observe builds the snapshot on the writer goroutine, which makes it consistent. It
// reads every task, so TestPerfObserve holds it to a budget.
func (e *Engine) Observe(ctx context.Context) (observe.Snapshot, error) {
	return do(ctx, e, func() (observe.Snapshot, error) {
		now := e.now()
		wall := time.Now()
		m := e.obs.Read(wall)
		s := observe.Snapshot{
			Time: wall, UptimeMs: m.Uptime.Milliseconds(), States: map[string]int{},
			Throughput: m.Throughput, Latency: m.Latency, Flow: m.Flow,
			Planning: observe.Planning{Epoch: e.planning.Epoch, Active: e.planning.Active, Phase: e.planning.Phase,
				Lost: !e.planning.LostAt.IsZero() && now.Sub(e.planning.LostAt) < e.cfg.PlanLostGrace},
		}
		s.Timeouts = observe.Timeouts{LeaseMs: e.cfg.LeaseTTL.Milliseconds(), ProgressMs: e.cfg.ProgressTTL.Milliseconds(),
			PresenceMs: e.cfg.PresenceTTL.Milliseconds(), RestartGraceMs: e.cfg.RestartGrace.Milliseconds()}
		byTier := map[int]*observe.TierLive{}
		tierOf := func(n int) *observe.TierLive {
			if byTier[n] == nil {
				byTier[n] = &observe.TierLive{Tier: n}
			}
			return byTier[n]
		}
		tiers := map[int]*observe.TierUsage{}
		var live []observe.TaskLive
		var tight []observe.BudgetLine
		working := map[string]string{}
		for _, ent := range e.tasks {
			t := ent.task
			s.States[string(t.State)]++
			tl := tierOf(t.Tier)
			switch t.State {
			case core.Ready:
				tl.Ready++
			case core.Claimed, core.Running:
				tl.Working++
			case core.Review, core.Merging:
				tl.Review++
			case core.Parked:
				tl.Parked++
			case core.Failed:
				tl.Failed++
			case core.Done:
				tl.Done++
			}
			tu := tiers[t.Tier]
			if tu == nil {
				tu = &observe.TierUsage{Tier: t.Tier}
				tiers[t.Tier] = tu
			}
			tu.Tasks++
			tu.Tokens += t.Tokens
			tu.Spent += t.Spent
			tu.Budget += t.Budget
			s.Usage.Tokens += t.Tokens
			s.Usage.Spent += t.Spent
			s.Usage.Budgeted += t.Budget
			if t.Budget > 0 && !t.State.Terminal() {
				if t.Spent > 0 { // A budget nothing has touched is not worth a row.
					tight = append(tight, observe.BudgetLine{ID: t.ID, Title: t.Title, Spent: t.Spent, Budget: t.Budget, State: string(t.State)})
				}
				if t.Spent >= t.Budget {
					s.Usage.OverBudget++
				}
			}
			switch t.State {
			case core.Ready:
				s.OldestMs = max(s.OldestMs, now.Sub(t.ReadyAt).Milliseconds())
			case core.Claimed, core.Running, core.Review, core.Merging, core.Failed, core.Parked:
				live = append(live, e.liveTask(t, now))
				if t.Owner != "" {
					working[t.Owner] = t.ID
				}
			}
		}
		slices.SortFunc(live, func(a, b observe.TaskLive) int {
			return cmp.Or(cmp.Compare(healthRank(a.Health, a.State), healthRank(b.Health, b.State)), strings.Compare(a.ID, b.ID))
		})
		if len(live) > maxLiveTasks {
			s.Truncated = len(live) - maxLiveTasks
			live = live[:maxLiveTasks]
		}
		s.Tasks = live
		for _, tu := range tiers {
			s.Usage.ByTier = append(s.Usage.ByTier, *tu)
		}
		slices.SortFunc(s.Usage.ByTier, func(a, b observe.TierUsage) int { return cmp.Compare(a.Tier, b.Tier) })
		slices.SortFunc(tight, func(a, b observe.BudgetLine) int {
			// Compare spent/budget without division, so the order is exact.
			return cmp.Or(cmp.Compare(b.Spent*a.Budget, a.Spent*b.Budget), strings.Compare(a.ID, b.ID))
		})
		s.Usage.Tightest = tight[:min(len(tight), 10)]

		waiting := map[string]bool{}
		for _, ws := range e.waiters {
			for _, w := range ws {
				waiting[w.agent.ID] = true
				s.Waiting++
				tierOf(w.agent.Tier).Waiting++
			}
		}
		for id, a := range m.Agents {
			seen := m.LastSeen[id]
			if seen.IsZero() {
				continue // Only usage so far, from before it was last seen.
			}
			ago := wall.Sub(seen)
			state := "idle"
			switch {
			case working[id] != "":
				state = "working"
			case waiting[id]:
				state = "waiting"
			case ago > e.cfg.PresenceTTL:
				state = "gone"
			}
			if state != "gone" && a.Tier > 0 {
				tierOf(a.Tier).Agents++
			}
			s.Agents = append(s.Agents, observe.AgentLive{
				ID: id, Tier: a.Tier, State: state, Task: working[id], SeenAgoMs: ago.Milliseconds(),
				Tokens: a.Tokens, Cost: a.Cost, Done: a.Done, Failed: a.Failed,
			})
		}
		slices.SortFunc(s.Agents, func(a, b observe.AgentLive) int { return strings.Compare(a.ID, b.ID) })
		for _, tl := range byTier {
			s.Tiers = append(s.Tiers, *tl)
		}
		slices.SortFunc(s.Tiers, func(a, b observe.TierLive) int { return cmp.Compare(a.Tier, b.Tier) })
		return s, nil
	})
}
