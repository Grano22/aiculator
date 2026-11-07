package swarm

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/pkg/observe"
)

// Sessions are the agents connected now, with what each said about itself, any limit it
// reported, and how long its waits can be held. It is kept in memory: after a restart,
// agents say hello again on their next call.
type sessions struct {
	mu   sync.Mutex
	byID map[string]*session
}

type session struct {
	info    observe.SessionInfo
	tier    int
	started time.Time
	// A limit stops work reaching the agent until it passes, such as a used-up plan.
	limit      string
	limitUntil time.Time
	// Stopping makes the agent's waits answer exit, and cancels any wait in progress.
	stopping bool
	// hidden is a session removed from the list while still stopping: a force disconnect.
	// It stays, unlisted, so that the agent keeps getting exit if its process carries on.
	hidden bool
	waits  map[*waitSlot]struct{}
	hold   holdLearner
}

type waitSlot struct{ cancel context.CancelFunc }

// SessionTimeouts bound how long a gone session stays listed. A session that went quiet
// stays this long after it stops being seen, then leaves the list.
const (
	// SessionGoneAfter is when a quiet session counts as gone.
	SessionGoneAfter = 45 * time.Second
	// SessionForgetAfter is when a gone session is dropped.
	SessionForgetAfter = 5 * time.Minute
)

func newSessions() *sessions { return &sessions{byID: map[string]*session{}} }

func (r *sessions) get(id string) *session {
	s := r.byID[id]
	if s == nil {
		s = &session{started: time.Now(), waits: map[*waitSlot]struct{}{}, hold: newHoldLearner()}
		r.byID[id] = s
	}
	return s
}

// Hello records what an agent says about itself. It replaces what an earlier hello said,
// and clears a stop from before, since a new process is starting over.
func (s *Service) Hello(a engine.Agent, info observe.SessionInfo) {
	info.Kind = cut(info.Kind, 16)
	for _, f := range []*string{&info.Harness, &info.Model, &info.Client, &info.ClientVersion, &info.Host, &info.OS, &info.Parent} {
		*f = cut(*f, 64)
	}
	info.CWD = cut(info.CWD, 512)
	s.sess.mu.Lock()
	defer s.sess.mu.Unlock()
	x := s.sess.get(a.ID)
	x.info, x.tier, x.started, x.stopping, x.hidden = info, a.Tier, time.Now(), false, false
}

// noteClient records a session the first time an agent calls a tool over MCP without having
// said hello, from what its MCP client said when it connected.
func (s *Service) noteClient(a engine.Agent, kind, client, version string) {
	s.sess.mu.Lock()
	defer s.sess.mu.Unlock()
	if x, ok := s.sess.byID[a.ID]; ok && x.info.Kind != "" {
		return
	}
	x := s.sess.get(a.ID)
	x.tier = a.Tier
	x.info = observe.SessionInfo{Kind: kind, Client: cut(client, 64), ClientVersion: cut(version, 64)}
}

// NoteMCPClient is noteClient for the MCP server.
func (s *Service) NoteMCPClient(a engine.Agent, client, version string) {
	s.noteClient(a, "mcp-http", client, version)
}

func cut(v string, n int) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
	if len(v) > n {
		return v[:n]
	}
	return v
}

// ReportLimit records that an agent cannot work until a time, such as when its plan's usage
// resets. Until then its waits answer exit, so a harness out of credit stops calling, and
// whatever it holds goes back with no penalty.
func (s *Service) ReportLimit(ctx context.Context, a engine.Agent, reason string, until time.Time) ([]string, error) {
	s.sess.mu.Lock()
	x := s.sess.get(a.ID)
	x.tier = a.Tier
	x.limit, x.limitUntil = cut(reason, 64), until
	for w := range x.waits {
		w.cancel()
	}
	s.sess.mu.Unlock()
	return s.Engine.ReleaseHeldBy(ctx, a.ID, "agent reported "+cut(reason, 64))
}

// Stop tells agents to exit: their next wait, or the one they are in, answers exit, and the
// tasks they hold go back to the queue at once. An empty list means every agent.
func (s *Service) Stop(ctx context.Context, ids []string) (stopped, released []string, err error) {
	s.sess.mu.Lock()
	var targets []string
	if len(ids) == 0 {
		for id := range s.sess.byID {
			targets = append(targets, id)
		}
	} else {
		targets = ids
	}
	for _, id := range targets {
		x := s.sess.get(id)
		x.stopping = true
		for w := range x.waits {
			w.cancel()
		}
	}
	s.sess.mu.Unlock()
	// Agents that hold work but never said hello are stopped too.
	if len(ids) == 0 {
		snap, err := s.Engine.Observe(ctx)
		if err == nil {
			for _, ag := range snap.Agents {
				if !slices.Contains(targets, ag.ID) {
					s.sess.mu.Lock()
					s.sess.get(ag.ID).stopping = true
					s.sess.mu.Unlock()
					targets = append(targets, ag.ID)
				}
			}
		}
	}
	for _, id := range targets {
		r, err := s.Engine.ReleaseHeldBy(ctx, id, "stopped by the operator")
		if err != nil {
			return targets, released, err
		}
		released = append(released, r...)
	}
	slices.Sort(targets)
	return targets, released, nil
}

// Killable lists the sessions whose processes aiculator started itself and may end: drivers
// and bridges on this host. A harness a person is typing into is never one of them.
func (s *Service) Killable(host string) []observe.SessionLive {
	s.sess.mu.Lock()
	defer s.sess.mu.Unlock()
	var out []observe.SessionLive
	for id, x := range s.sess.byID {
		if x.info.Killable && x.info.PID > 0 && x.info.Host == host {
			out = append(out, observe.SessionLive{Agent: id, SessionInfo: x.info})
		}
	}
	return out
}

// Forget drops sessions. An empty list drops every session that has gone.
func (s *Service) Forget(ids []string) []string {
	now := time.Now()
	c := s.Engine.Collector()
	s.sess.mu.Lock()
	defer s.sess.mu.Unlock()
	var out []string
	for id := range s.sess.byID {
		if len(ids) > 0 && !slices.Contains(ids, id) {
			continue
		}
		if len(ids) == 0 {
			seen, ok := c.LastSeen(id)
			if ok && now.Sub(seen) < SessionGoneAfter {
				continue
			}
		}
		x := s.sess.byID[id]
		if x.hidden {
			continue
		}
		if x.stopping {
			x.hidden = true // Off the list, but its waits still answer exit.
		} else {
			delete(s.sess.byID, id)
		}
		c.Forget(id)
		out = append(out, id)
	}
	if len(ids) == 0 {
		// Agents that never said hello leave the list the same way.
		snap, err := s.Engine.Observe(context.Background())
		if err == nil {
			for _, a := range snap.Agents {
				if a.State == "gone" && !slices.Contains(out, a.ID) {
					c.Forget(a.ID)
					out = append(out, a.ID)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// prune drops sessions gone for longer than SessionForgetAfter, so the list holds what is
// connected, not everything that ever was.
func (s *Service) prune() {
	now := time.Now()
	c := s.Engine.Collector()
	s.sess.mu.Lock()
	var drop []string
	for id, x := range s.sess.byID {
		seen, ok := c.LastSeen(id)
		if !ok {
			seen = x.started
		}
		if now.Sub(seen) > SessionForgetAfter && len(x.waits) == 0 {
			drop = append(drop, id)
		}
	}
	for _, id := range drop {
		delete(s.sess.byID, id)
	}
	s.sess.mu.Unlock()
	for _, id := range drop {
		c.Forget(id)
	}
	// And agents that never said hello.
	if snap, err := s.Engine.Observe(context.Background()); err == nil {
		for _, a := range snap.Agents {
			if a.Task == "" && time.Duration(a.SeenAgoMs)*time.Millisecond > SessionForgetAfter {
				c.Forget(a.ID)
			}
		}
	}
}

func (s *Service) pruneLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.bg.Done():
			return
		case <-t.C:
			s.prune()
		}
	}
}

// gate decides whether a wait may go ahead. A stopped or limited agent gets exit instead.
func (s *Service) gate(a engine.Agent) (engine.WaitResponse, bool) {
	s.sess.mu.Lock()
	defer s.sess.mu.Unlock()
	x, ok := s.sess.byID[a.ID]
	if !ok {
		return engine.WaitResponse{}, true
	}
	if x.stopping {
		return engine.WaitResponse{Action: engine.ActionExit, Reason: "stopped_by_operator"}, false
	}
	if x.limit != "" {
		if x.limitUntil.IsZero() || time.Now().Before(x.limitUntil) {
			r := engine.WaitResponse{Action: engine.ActionExit, Reason: "usage_limit: " + x.limit}
			if !x.limitUntil.IsZero() {
				r.Reason += ", until " + x.limitUntil.UTC().Format(time.RFC3339)
			}
			return r, false
		}
		x.limit, x.limitUntil = "", time.Time{}
	}
	return engine.WaitResponse{}, true
}

// track lets a wait be cancelled by Stop or ReportLimit.
func (s *Service) track(a engine.Agent, cancel context.CancelFunc) func() {
	w := &waitSlot{cancel: cancel}
	s.sess.mu.Lock()
	x := s.sess.get(a.ID)
	if x.tier == 0 {
		x.tier = a.Tier
	}
	x.waits[w] = struct{}{}
	s.sess.mu.Unlock()
	return func() {
		s.sess.mu.Lock()
		delete(x.waits, w)
		s.sess.mu.Unlock()
	}
}

// sessionsLive merges what sessions said with what the engine knows of each agent.
func (s *Service) sessionsLive(snap *observe.Snapshot) {
	now := time.Now()
	agents := map[string]observe.AgentLive{}
	for _, a := range snap.Agents {
		agents[a.ID] = a
	}
	s.sess.mu.Lock()
	defer s.sess.mu.Unlock()
	for id, x := range s.sess.byID {
		if x.hidden {
			delete(agents, id)
			continue
		}
		l := observe.SessionLive{Agent: id, Tier: x.tier, SessionInfo: x.info, ConnectedMs: now.Sub(x.started).Milliseconds(),
			Limit: x.limit, LimitUntil: x.limitUntil, HoldMs: x.hold.current().Milliseconds()}
		a, ok := agents[id]
		l.State = "idle"
		if ok {
			l.State, l.Task, l.SeenAgoMs = a.State, a.Task, a.SeenAgoMs
			if l.Tier == 0 {
				l.Tier = a.Tier
			}
		} else {
			l.SeenAgoMs = now.Sub(x.started).Milliseconds()
		}
		if len(x.waits) > 0 && l.Task == "" {
			l.State = "waiting"
		} else if time.Duration(l.SeenAgoMs)*time.Millisecond > SessionGoneAfter && l.Task == "" {
			l.State = "gone"
		}
		switch {
		case x.stopping:
			l.State = "stopping"
		case x.limit != "" && (x.limitUntil.IsZero() || now.Before(x.limitUntil)):
			l.State = "limited"
		}
		snap.Sessions = append(snap.Sessions, l)
		delete(agents, id)
	}
	// Agents that work without having said hello, such as an older client.
	for id, a := range agents {
		snap.Sessions = append(snap.Sessions, observe.SessionLive{Agent: id, Tier: a.Tier, State: a.State, Task: a.Task, SeenAgoMs: a.SeenAgoMs,
			SessionInfo: observe.SessionInfo{Kind: "unknown"}})
	}
	slices.SortFunc(snap.Sessions, func(p, q observe.SessionLive) int { return strings.Compare(p.Agent, q.Agent) })
	// Gone agents leave the agent list as well once their session is forgotten.
}

// ReportUsage records tokens and cost an agent spent. With a task it holds, the usage is the
// task's too, as with a heartbeat; without one, it counts for the agent alone, such as
// tokens spent waiting or planning.
func (s *Service) ReportUsage(ctx context.Context, a engine.Agent, taskID string, lease uint64, u core.Usage) (any, error) {
	if u.Tokens < 0 || u.Cost < 0 {
		return nil, fmt.Errorf("%w: usage cannot be negative", engine.ErrInvalid)
	}
	if taskID != "" {
		return s.Engine.HeartbeatUsage(ctx, a, taskID, lease, u)
	}
	c := s.Engine.Collector()
	c.Seen(a.ID, a.Tier, time.Now())
	c.Usage(a.ID, u.Tokens, u.Cost)
	return map[string]int64{"tokens": u.Tokens, "cost": u.Cost}, nil
}
