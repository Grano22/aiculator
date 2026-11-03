// Package api exposes the service over HTTP and MCP. It holds no state.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Grano22/aiculator/internal/auth"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/swarm"
	"github.com/Grano22/aiculator/internal/vlog"
	"github.com/Grano22/aiculator/pkg/observe"
)

// Server holds what the handlers need.
type Server struct {
	S       *swarm.Service
	Auth    *auth.Authority
	Version string
}

func usage(q LeaseReq) core.Usage { return core.Usage{Cost: q.Cost, Tokens: q.Tokens} }

// Error is the JSON error body.
type Error struct {
	Error string `json:"error"`
}

func status(err error) int {
	switch {
	case errors.Is(err, engine.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, engine.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, core.ErrForbidden), errors.Is(err, core.ErrSelfVerify), errors.Is(err, core.ErrTier):
		return http.StatusForbidden
	case errors.Is(err, core.ErrFenced), errors.Is(err, core.ErrIllegal), errors.Is(err, store.ErrConflict),
		errors.Is(err, engine.ErrExists), errors.Is(err, engine.ErrNoMatch),
		errors.Is(err, engine.ErrPlanBusy), errors.Is(err, engine.ErrPlanLost):
		return http.StatusConflict
	case errors.Is(err, context.Canceled):
		return 499
	}
	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeJSON(w, status(err), Error{err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

const maxBody = 4 << 20

func decode[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil && !errors.Is(err, io.EOF) { // An empty body is an empty request.
		writeJSON(w, http.StatusBadRequest, Error{"bad request body: " + err.Error()})
		return v, false
	}
	return v, true
}

func (s *Server) claims(r *http.Request) (auth.Claims, error) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return auth.Claims{}, auth.ErrInvalidToken
	}
	return s.Auth.Verify(tok)
}

func agentOf(c auth.Claims) engine.Agent {
	return engine.Agent{ID: c.Agent, Tier: c.Tier, Tags: c.Tags}
}

type handler func(w http.ResponseWriter, r *http.Request, c auth.Claims)

func (s *Server) authed(check func(auth.Claims) bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := s.claims(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, Error{err.Error()})
			return
		}
		if !check(c) {
			writeJSON(w, http.StatusForbidden, Error{"this token may not do that"})
			return
		}
		h(w, r, c)
	}
}

func isAgent(c auth.Claims) bool   { return c.Tier >= 1 }
func isAdmin(c auth.Claims) bool   { return c.Admin }
func canView(c auth.Claims) bool   { return c.Admin || c.Viewer }
func isPlanner(c auth.Claims) bool { return c.Admin || c.Planner }
func anyone(auth.Claims) bool      { return true }

// LeaseReq is the body of the agent calls that act on a held task.
type LeaseReq struct {
	TaskID string `json:"task_id"`
	Lease  uint64 `json:"lease"`
	Note   string `json:"note,omitempty"`
	Cost   int64  `json:"cost,omitempty"`
	Tokens int64  `json:"tokens,omitempty"`
	Pass   bool   `json:"pass,omitempty"`
}

// WaitReq is the body of the wait call.
type WaitReq struct {
	HoldMs int64 `json:"hold_ms"`
}

// TokenReq asks for a new agent token.
type TokenReq struct {
	Agent   string   `json:"agent"`
	Tier    int      `json:"tier"`
	Tags    []string `json:"tags,omitempty"`
	Planner bool     `json:"planner,omitempty"`
	Viewer  bool     `json:"viewer,omitempty"`
	TTLSec  int64    `json:"ttl_sec,omitempty"`
}

// PlanReq is the body of the planning calls.
type PlanReq struct {
	Epoch    uint64          `json:"epoch,omitempty"`
	TTLMs    int64           `json:"ttl_ms,omitempty"`
	Phase    string          `json:"phase,omitempty"`
	Expected map[int]int     `json:"expected_tasks,omitempty"`
	ETAMs    int64           `json:"eta_ms,omitempty"`
	Task     engine.TaskSpec `json:"task,omitzero"`
}

// Routes returns the HTTP handler, without the MCP endpoint.
func (s *Server) Routes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	m.HandleFunc("POST /v1/agent/wait", s.authed(isAgent, func(w http.ResponseWriter, r *http.Request, c auth.Claims) {
		q, ok := decode[WaitReq](w, r)
		if !ok {
			return
		}
		g, err := s.S.Wait(r.Context(), agentOf(c), time.Duration(q.HoldMs)*time.Millisecond)
		reply(w, g, err)
	}))
	m.HandleFunc("POST /v1/agent/claim", s.authed(isAgent, func(w http.ResponseWriter, r *http.Request, c auth.Claims) {
		q, ok := decode[LeaseReq](w, r)
		if !ok {
			return
		}
		g, err := s.S.Claim(r.Context(), agentOf(c), q.TaskID)
		reply(w, g, err)
	}))
	lease := func(fn func(ctx context.Context, a engine.Agent, q LeaseReq) (core.Task, error)) http.HandlerFunc {
		return s.authed(isAgent, func(w http.ResponseWriter, r *http.Request, c auth.Claims) {
			q, ok := decode[LeaseReq](w, r)
			if !ok {
				return
			}
			t, err := fn(r.Context(), agentOf(c), q)
			reply(w, t, err)
		})
	}
	m.HandleFunc("POST /v1/agent/heartbeat", lease(func(ctx context.Context, a engine.Agent, q LeaseReq) (core.Task, error) {
		return s.S.Engine.Heartbeat(ctx, a, q.TaskID, q.Lease)
	}))
	m.HandleFunc("POST /v1/agent/progress", lease(func(ctx context.Context, a engine.Agent, q LeaseReq) (core.Task, error) {
		return s.S.Progress(ctx, a, q.TaskID, q.Lease, "", q.Note, usage(q))
	}))
	m.HandleFunc("POST /v1/agent/submit", lease(func(ctx context.Context, a engine.Agent, q LeaseReq) (core.Task, error) {
		return s.S.Submit(ctx, a, q.TaskID, q.Lease, q.Note, usage(q))
	}))
	m.HandleFunc("POST /v1/agent/fail", lease(func(ctx context.Context, a engine.Agent, q LeaseReq) (core.Task, error) {
		return s.S.Engine.Fail(ctx, a, q.TaskID, q.Lease, q.Note, usage(q))
	}))
	m.HandleFunc("POST /v1/agent/release", lease(func(ctx context.Context, a engine.Agent, q LeaseReq) (core.Task, error) {
		return s.S.Engine.Release(ctx, a, q.TaskID, q.Lease, q.Note)
	}))
	m.HandleFunc("POST /v1/agent/verdict", lease(func(ctx context.Context, a engine.Agent, q LeaseReq) (core.Task, error) {
		return s.S.Engine.Verdict(ctx, a, q.TaskID, q.Lease, q.Pass, q.Note)
	}))

	// Readiness says only pass or fail, so it is safe with no token, as probes expect.
	m.HandleFunc("GET /v1/readyz", func(w http.ResponseWriter, r *http.Request) {
		ok := true
		for _, p := range s.S.Probes(r.Context()) {
			ok = ok && p.OK
		}
		code, word := http.StatusOK, "ready"
		if !ok {
			code, word = http.StatusServiceUnavailable, "not ready"
		}
		writeJSON(w, code, map[string]string{"status": word})
	})
	m.HandleFunc("GET /v1/observe", s.authed(canView, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		snap, err := s.S.Snapshot(r.Context(), s.Version)
		reply(w, snap, err)
	}))
	m.HandleFunc("GET /metrics", s.authed(canView, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		snap, err := s.S.Snapshot(r.Context(), s.Version)
		if err != nil {
			writeJSON(w, status(err), Error{err.Error()})
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, snap.Prometheus())
	}))
	m.HandleFunc("GET /v1/events", s.authed(canView, s.streamEvents))
	m.HandleFunc("GET /v1/situation", s.authed(anyone, func(w http.ResponseWriter, r *http.Request, c auth.Claims) {
		sit, err := s.S.Engine.Situation(r.Context(), agentOf(c))
		reply(w, sit, err)
	}))

	m.HandleFunc("POST /v1/tasks", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		q, ok := decode[engine.TaskSpec](w, r)
		if !ok {
			return
		}
		if len(q.Checks) > 0 && s.S.Work == nil {
			writeJSON(w, http.StatusBadRequest, Error{"tasks with checks need a repository: start the server with --repo"})
			return
		}
		t, err := s.S.Engine.AddTask(r.Context(), q)
		reply(w, t, err)
	}))
	m.HandleFunc("GET /v1/tasks", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		ts, err := s.S.Engine.List(r.Context())
		reply(w, ts, err)
	}))
	m.HandleFunc("GET /v1/tasks/{id}", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		t, err := s.S.Engine.Get(r.Context(), r.PathValue("id"))
		reply(w, t, err)
	}))
	type opReq struct {
		Note      string `json:"note,omitempty"`
		AddBudget int64  `json:"add_budget,omitempty"`
	}
	m.HandleFunc("POST /v1/tasks/{id}/cancel", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		q, ok := decode[opReq](w, r)
		if !ok {
			return
		}
		t, err := s.S.Engine.Cancel(r.Context(), r.PathValue("id"), q.Note)
		reply(w, t, err)
	}))
	m.HandleFunc("POST /v1/tasks/{id}/unpark", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		q, ok := decode[opReq](w, r)
		if !ok {
			return
		}
		t, err := s.S.Engine.Unpark(r.Context(), r.PathValue("id"), q.AddBudget, q.Note)
		reply(w, t, err)
	}))

	m.HandleFunc("POST /v1/tokens", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		q, ok := decode[TokenReq](w, r)
		if !ok {
			return
		}
		cl := auth.Claims{Agent: q.Agent, Tier: q.Tier, Tags: q.Tags, Planner: q.Planner, Viewer: q.Viewer}
		if q.TTLSec > 0 {
			cl.Expires = time.Now().Add(time.Duration(q.TTLSec) * time.Second)
		}
		tok, err := s.Auth.Issue(cl)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, Error{err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"token": tok})
	}))

	plan := func(fn func(ctx context.Context, c auth.Claims, q PlanReq) (any, error)) http.HandlerFunc {
		return s.authed(isPlanner, func(w http.ResponseWriter, r *http.Request, c auth.Claims) {
			q, ok := decode[PlanReq](w, r)
			if !ok {
				return
			}
			v, err := fn(r.Context(), c, q)
			reply(w, v, err)
		})
	}
	m.HandleFunc("POST /v1/plan/begin", plan(func(ctx context.Context, c auth.Claims, q PlanReq) (any, error) {
		ep, err := s.S.Engine.PlanBegin(ctx, c.Agent, time.Duration(q.TTLMs)*time.Millisecond)
		return map[string]uint64{"epoch": ep}, err
	}))
	m.HandleFunc("POST /v1/plan/update", plan(func(ctx context.Context, c auth.Claims, q PlanReq) (any, error) {
		return map[string]bool{"ok": true}, s.S.Engine.PlanUpdate(ctx, c.Agent, q.Epoch, time.Duration(q.TTLMs)*time.Millisecond, q.Phase, q.Expected, q.ETAMs)
	}))
	m.HandleFunc("POST /v1/plan/add", plan(func(ctx context.Context, c auth.Claims, q PlanReq) (any, error) {
		return s.S.Engine.PlanAdd(ctx, c.Agent, q.Epoch, q.Task)
	}))
	m.HandleFunc("POST /v1/plan/commit", plan(func(ctx context.Context, c auth.Claims, q PlanReq) (any, error) {
		return map[string]bool{"ok": true}, s.S.Engine.PlanCommit(ctx, c.Agent, q.Epoch)
	}))
	s.debugRoutes(m)
	return m
}

// streamEvents sends each committed task revision as JSON lines, for as long as the
// client stays connected. Any program can follow the swarm this way.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, Error{"streaming is not supported"})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	lines := make(chan core.Task, 256)
	done := make(chan error, 1)
	go func() {
		done <- s.S.Store.TailEvents(r.Context(), func(t core.Task) {
			select {
			case lines <- t:
			case <-r.Context().Done():
			}
		})
	}()
	enc := json.NewEncoder(w)
	for {
		select {
		case t := <-lines:
			if enc.Encode(t) != nil {
				return
			}
			flusher.Flush()
		case <-done:
			return
		}
	}
}

var httpLog = vlog.For("http")

// Instrument times calls for the dashboards, and logs traffic at -v=6 and above. It leaves
// long-polls and streams out of the timings, since their duration says nothing about how
// the server is doing. At lower levels it adds nothing but a level check.
func (s *Server) Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		timed := path != "/v1/agent/wait" && path != "/v1/events" && path != "/mcp"
		start := time.Now()
		if !httpLog.V(vlog.LevelRequests).Enabled() {
			next.ServeHTTP(w, r)
		} else {
			logged(next, w, r, start)
		}
		if timed {
			c := s.S.Engine.Collector()
			c.Observe(observe.LatencyAPI, time.Since(start))
			c.Count(observe.SeriesAPI, time.Now(), 1)
		}
	})
}

func logged(next http.Handler, w http.ResponseWriter, r *http.Request, start time.Time) {
	limit := vlog.HTTPLimit("http")
	if limit > 0 && r.Body != nil {
		// Read a capped copy for the log, then hand the handler the whole body.
		head, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)))
		if err == nil {
			httpLog.V(vlog.LevelBodies).Info("request body", "method", r.Method, "path", r.URL.Path, "body", vlog.Body(head, limit))
			r.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
		}
	}
	if v := httpLog.V(vlog.LevelHeaders); v.Enabled() {
		v.Info("request headers", "method", r.Method, "path", r.URL.Path, "headers", vlog.Headers(r.Header))
	}
	// A stream's body never ends, so only its status and size are logged.
	bodyLimit := limit
	if r.URL.Path == "/v1/events" || r.URL.Path == "/mcp" {
		bodyLimit = 0
	}
	c := vlog.NewCapture(w, bodyLimit)
	next.ServeHTTP(c, r)
	httpLog.V(vlog.LevelRequests).Info("request", "method", r.Method, "path", r.URL.Path, "status", c.Status, "took", time.Since(start), "bytes", c.Bytes, "remote", r.RemoteAddr)
	if v := httpLog.V(vlog.LevelHeaders); v.Enabled() {
		v.Info("response headers", "path", r.URL.Path, "headers", vlog.Headers(w.Header()))
	}
	if bodyLimit > 0 {
		httpLog.V(vlog.LevelBodies).Info("response body", "path", r.URL.Path, "status", c.Status, "body", vlog.Body(c.Recorded(), bodyLimit))
	}
}

// VerbosityReq changes logging on a running server.
type VerbosityReq struct {
	Level      *int    `json:"level,omitempty"`
	VModule    *string `json:"vmodule,omitempty"`
	ForSeconds int64   `json:"for_seconds,omitempty"`
}

// VerbosityState is the server's logging setting.
type VerbosityState struct {
	Level     int       `json:"level"`
	VModule   string    `json:"vmodule,omitempty"`
	RevertsAt time.Time `json:"reverts_at,omitzero"`
}

func verbosityState() VerbosityState {
	return VerbosityState{Level: vlog.Level(), VModule: vlog.VModule(), RevertsAt: vlog.RevertsAt()}
}

func (s *Server) debugRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/debug/verbosity", s.authed(isAdmin, func(w http.ResponseWriter, _ *http.Request, _ auth.Claims) {
		writeJSON(w, http.StatusOK, verbosityState())
	}))
	m.HandleFunc("PUT /v1/debug/verbosity", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, c auth.Claims) {
		q, ok := decode[VerbosityReq](w, r)
		if !ok {
			return
		}
		level, mod := vlog.Level(), vlog.VModule()
		if q.Level != nil {
			level = *q.Level
		}
		if q.VModule != nil {
			mod = *q.VModule
		}
		if err := vlog.SetFor(level, mod, time.Duration(q.ForSeconds)*time.Second); err != nil {
			writeJSON(w, http.StatusBadRequest, Error{err.Error()})
			return
		}
		httpLog.V(vlog.LevelAlways).Info("verbosity changed", "level", level, "vmodule", mod, "for_seconds", q.ForSeconds, "by", c.Agent)
		writeJSON(w, http.StatusOK, verbosityState())
	}))
}

// ViewerOnly guards a handler with a viewer or admin token, for plugins.
func (s *Server) ViewerOnly(next http.Handler) http.Handler {
	return s.authed(canView, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) { next.ServeHTTP(w, r) })
}

// WriteJSON answers with a JSON body, for plugins.
func WriteJSON(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }
