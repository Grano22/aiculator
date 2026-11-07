// Package agentd is the helper that waits for work on an agent's behalf, so the model does
// not have to. A model that waits through tool calls pays a turn, its whole context again,
// for every call, and a call can only last as long as its harness allows: about 40 seconds
// over MCP. The daemon holds the connection to the swarm instead. The model runs one command,
// aiculator work next, which returns when there is a task, and spends nothing while it waits.
//
// It also does the routine work of a lease while the model works: heartbeats, saving
// progress, and reporting the tokens the harness's own log says the session used, so none of
// that costs the model a turn either.
package agentd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/client"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/swarm"
	"github.com/Grano22/aiculator/pkg/observe"
)

// Options configure a daemon.
type Options struct {
	Agent string
	Token string
	// Resolve finds the server's address, again whenever it cannot be reached.
	Resolve func() (string, error)
	// Dir is where the daemon's state file goes, beside the agent's token.
	Dir string
	// CWD is the folder the harness runs in, which is how its log is found; Home is the
	// user's home, where harnesses keep their logs.
	CWD, Home string
	// TokenBudget is how many tokens this session may spend; 0 means no limit. A task
	// expected to cost more than what is left is handed back rather than started.
	TokenBudget int64
	Version     string
	Log         *slog.Logger
	// Timings, for tests.
	Heartbeat, Progress, PendingTTL, IdleExit, UsageEvery time.Duration
	Readers                                               []Reader
}

// State is what the daemon writes for the commands that talk to it.
type State struct {
	Addr   string `json:"addr"`
	PID    int    `json:"pid"`
	Secret string `json:"secret"`
	// MaxWaitMs is how long a work next may wait, as learned from the harness: just under
	// the longest a command was seen to run there.
	MaxWaitMs int64 `json:"max_wait_ms"`
}

// StatePath is where an agent's daemon writes its state.
func StatePath(dir, agent string) string { return filepath.Join(dir, agent+".daemon.json") }

// Reply is what work next prints.
type Reply struct {
	// Action is task, again (nothing yet: run work next again) or exit.
	Action string       `json:"action"`
	Reason string       `json:"reason,omitempty"`
	Grant  *swarm.Grant `json:"grant,omitempty"`
	// WaitedMs is how long this call waited; NextWaitMs how long the next one may.
	WaitedMs   int64  `json:"waited_ms"`
	NextWaitMs int64  `json:"next_wait_ms"`
	Notice     string `json:"notice,omitempty"`
}

// DoneReq is what work done sends.
type DoneReq struct {
	// Result is submit, fail, release or verdict.
	Result string `json:"result"`
	Note   string `json:"note,omitempty"`
	Pass   bool   `json:"pass,omitempty"`
}

// Daemon waits for work for one agent.
type Daemon struct {
	o      Options
	secret string
	srv    *http.Server

	mu      sync.Mutex
	c       *client.Client
	url     string
	pending *swarm.Grant // Taken from the server, not yet given to the model.
	pendAt  time.Time
	current *swarm.Grant // Given to the model, not yet done.
	curAt   time.Time
	lost    string // Why the last task was taken back, said at the next call.
	busy    bool   // A work next is waiting.
	last    time.Time
	wait    waitLearner
	low     bool // Out of token budget.
	started time.Time
	usage   usageTrack
	stop    chan struct{}
	once    sync.Once
}

type usageTrack struct {
	reader       Reader
	file         string
	lastReported time.Time
	spent        int64 // Since the daemon started, for the budget.
	reading      Reading
}

// New makes a daemon. Run starts it.
func New(o Options) (*Daemon, error) {
	if o.Agent == "" || o.Token == "" || o.Resolve == nil || o.Dir == "" {
		return nil, errors.New("agentd: agent, token, resolve and dir are required")
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.Heartbeat, 20*time.Second)
	def(&o.Progress, 2*time.Minute)
	def(&o.PendingTTL, 3*time.Minute)
	def(&o.IdleExit, 2*time.Hour)
	def(&o.UsageEvery, 5*time.Minute)
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Readers == nil && o.Home != "" {
		o.Readers = Readers(o.Home)
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	d := &Daemon{o: o, secret: hex.EncodeToString(b[:]), wait: newWaitLearner(), stop: make(chan struct{}), last: time.Now(), started: time.Now()}
	d.usage.lastReported = d.started
	if st, err := ReadState(o.Dir, o.Agent); err == nil && st.MaxWaitMs > 0 {
		d.wait.max = time.Duration(st.MaxWaitMs) * time.Millisecond // What was learned before.
	}
	return d, nil
}

// ReadState reads an agent's daemon state.
func ReadState(dir, agent string) (State, error) {
	var st State
	b, err := os.ReadFile(StatePath(dir, agent))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

func (d *Daemon) saveState(addr string) error {
	d.mu.Lock()
	st := State{Addr: addr, PID: os.Getpid(), Secret: d.secret, MaxWaitMs: d.wait.max.Milliseconds()}
	d.mu.Unlock()
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := StatePath(d.o.Dir, d.o.Agent) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, StatePath(d.o.Dir, d.o.Agent))
}

// Run serves until ctx ends, the swarm says exit with nothing in hand, or no one has asked
// for a while.
func (d *Daemon) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /next", d.guard(d.handleNext))
	mux.HandleFunc("POST /done", d.guard(d.handleDone))
	mux.HandleFunc("GET /status", d.guard(d.handleStatus))
	mux.HandleFunc("POST /stop", d.guard(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"stopping": d.o.Agent})
		d.shutdown()
	}))
	d.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := d.saveState(ln.Addr().String()); err != nil {
		return err
	}
	defer os.Remove(StatePath(d.o.Dir, d.o.Agent))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.connect(ctx)
	go d.keepalive(ctx)
	go func() {
		select {
		case <-ctx.Done():
		case <-d.stop:
		}
		sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer scancel()
		_ = d.srv.Shutdown(sctx)
	}()
	d.o.Log.Info("agentd: waiting for work", "agent", d.o.Agent, "addr", ln.Addr().String())
	if err := d.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	d.handBack("the agent's helper stopped")
	return nil
}

func (d *Daemon) shutdown() { d.once.Do(func() { close(d.stop) }) }

func (d *Daemon) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agentd-Secret") != d.secret {
			http.Error(w, "wrong secret", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// connect finds the server and says hello. It is called again after the server is lost.
func (d *Daemon) connect(ctx context.Context) {
	url, err := d.o.Resolve()
	if err != nil || url == "" {
		return
	}
	d.mu.Lock()
	if url != d.url {
		d.url, d.c = url, client.New(url, d.o.Token)
	}
	c := d.c
	d.mu.Unlock()
	info := observe.SessionInfo{Kind: "daemon", Client: "aiculator-agentd", ClientVersion: d.o.Version, PID: os.Getpid(), Killable: true, CWD: d.o.CWD}
	info.Host, _ = os.Hostname()
	if r := d.reading(time.Time{}); r.Source != "" {
		info.Harness, info.Model = r.Source, r.Model
	}
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = c.Post(hctx, "/v1/agent/hello", info, nil)
}

func (d *Daemon) cl() *client.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.c
}

// handleNext waits for work. It hands over a task the daemon already holds at once, and
// otherwise waits on the swarm until there is one, the swarm says exit, or the time this
// harness lets a command run is nearly up.
func (d *Daemon) handleNext(w http.ResponseWriter, r *http.Request) {
	var q struct {
		MaxMs int64 `json:"max_ms"`
	}
	_ = json.NewDecoder(r.Body).Decode(&q)
	d.mu.Lock()
	d.last = time.Now()
	if d.busy {
		d.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "another work next is already waiting for this agent"})
		return
	}
	if d.current != nil {
		cur := d.current
		d.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("you still hold task %s: finish it with aiculator work done", cur.Task.ID)})
		return
	}
	notice := d.lost
	d.lost = ""
	if d.pending != nil {
		g := d.pending
		d.current, d.pending, d.curAt = g, nil, time.Now()
		d.usage.lastReported = d.curAt
		max := d.wait.max
		d.mu.Unlock()
		writeJSON(w, http.StatusOK, Reply{Action: "task", Grant: g, NextWaitMs: max.Milliseconds(), Notice: notice})
		return
	}
	if d.low {
		d.mu.Unlock()
		writeJSON(w, http.StatusOK, Reply{Action: "exit", Reason: "token_budget: this session has spent its token budget"})
		return
	}
	max := d.wait.max
	if q.MaxMs > 0 {
		max = time.Duration(q.MaxMs) * time.Millisecond
	}
	d.busy = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.busy = false
		d.mu.Unlock()
	}()

	start := time.Now()
	deadline := start.Add(max)
	for {
		left := time.Until(deadline)
		if left <= time.Second {
			d.learn(func(l *waitLearner) { l.completed(time.Since(start)) })
			_ = d.saveState(d.addrNow())
			d.mu.Lock()
			next := d.wait.max
			d.mu.Unlock()
			writeJSON(w, http.StatusOK, Reply{Action: "again", Reason: "nothing yet", WaitedMs: time.Since(start).Milliseconds(), NextWaitMs: next.Milliseconds(), Notice: notice})
			return
		}
		hold := min(left-time.Second, 25*time.Second)
		var g swarm.Grant
		// The wait to the server outlives the caller on purpose: if the harness kills this
		// command just as a task arrives, the task is kept, not lost with the connection.
		wctx, cancel := context.WithTimeout(context.Background(), hold+15*time.Second)
		err := d.cl().Post(wctx, "/v1/agent/wait", api.WaitReq{HoldMs: hold.Milliseconds()}, &g)
		cancel()
		gone := r.Context().Err() != nil
		if err != nil {
			var se *client.StatusError
			if errors.As(err, &se) && se.Code < 500 && se.Code != http.StatusRequestTimeout {
				if !gone {
					writeJSON(w, http.StatusOK, Reply{Action: "exit", Reason: "the swarm refused this agent: " + se.Msg})
				}
				return
			}
			d.connect(context.Background()) // The server may have moved.
			if gone {
				d.learn(func(l *waitLearner) { l.cutShort(time.Since(start)) })
				_ = d.saveState(d.addrNow())
				return
			}
			select {
			case <-r.Context().Done():
				d.learn(func(l *waitLearner) { l.cutShort(time.Since(start)) })
				_ = d.saveState(d.addrNow())
				return
			case <-time.After(time.Second):
			}
			continue
		}
		switch g.Action {
		case engine.ActionTask:
			if d.overBudget(&g) {
				if !gone {
					writeJSON(w, http.StatusOK, Reply{Action: "exit", Reason: "token_budget: the next task is expected to cost more than this session has left", WaitedMs: time.Since(start).Milliseconds()})
				}
				return
			}
			d.mu.Lock()
			if gone {
				// The caller was killed while the task came: keep it for the next call.
				d.pending, d.pendAt = &g, time.Now()
				d.mu.Unlock()
				d.learn(func(l *waitLearner) { l.cutShort(time.Since(start)) })
				_ = d.saveState(d.addrNow())
				return
			}
			d.current, d.curAt = &g, time.Now()
			d.usage.lastReported = d.curAt
			next := d.wait.max
			d.mu.Unlock()
			writeJSON(w, http.StatusOK, Reply{Action: "task", Grant: &g, WaitedMs: time.Since(start).Milliseconds(), NextWaitMs: next.Milliseconds(), Notice: notice})
			return
		case engine.ActionExit:
			if !gone {
				writeJSON(w, http.StatusOK, Reply{Action: "exit", Reason: g.Reason, WaitedMs: time.Since(start).Milliseconds()})
			}
			return
		}
		if gone {
			d.learn(func(l *waitLearner) { l.cutShort(time.Since(start)) })
			_ = d.saveState(d.addrNow())
			return
		}
	}
}

func (d *Daemon) addrNow() string {
	st, err := ReadState(d.o.Dir, d.o.Agent)
	if err != nil {
		return ""
	}
	return st.Addr
}

func (d *Daemon) learn(fn func(*waitLearner)) {
	d.mu.Lock()
	fn(&d.wait)
	d.mu.Unlock()
}

// overBudget hands a task back when this session cannot afford it, by its estimate.
func (d *Daemon) overBudget(g *swarm.Grant) bool {
	if d.o.TokenBudget <= 0 || g.Task == nil || g.Job == "review" {
		return false
	}
	d.refreshUsage()
	d.mu.Lock()
	left := d.o.TokenBudget - d.usage.spent
	d.mu.Unlock()
	var est core.Estimate
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.cl().Get(ctx, "/v1/tasks/"+g.Task.ID+"/estimate", &est); err != nil || est.Method == "none" || est.Tokens <= left {
		return false
	}
	_ = d.cl().Post(ctx, "/v1/agent/release", api.LeaseReq{TaskID: g.Task.ID, Lease: g.Lease, Note: fmt.Sprintf("expected to take about %d tokens; this session has %d left", est.Tokens, left)}, nil)
	d.mu.Lock()
	d.low = true
	d.mu.Unlock()
	return true
}

// handleDone finishes the task the model holds, with the tokens the harness's log says it took.
func (d *Daemon) handleDone(w http.ResponseWriter, r *http.Request) {
	var q DoneReq
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	d.mu.Lock()
	d.last = time.Now()
	g := d.current
	lost := d.lost
	d.mu.Unlock()
	if g == nil {
		msg := "you hold no task"
		if lost != "" {
			msg += ": " + lost
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": msg})
		return
	}
	path := map[string]string{"submit": "/v1/agent/submit", "fail": "/v1/agent/fail", "release": "/v1/agent/release", "verdict": "/v1/agent/verdict"}[q.Result]
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "result is submit, fail, release or verdict"})
		return
	}
	tokens := d.unreported()
	lq := api.LeaseReq{TaskID: g.Task.ID, Lease: g.Lease, Note: q.Note, Pass: q.Pass, Tokens: tokens}
	var out any
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	err := d.cl().Post(ctx, path, lq, &out)
	if err != nil {
		var se *client.StatusError
		if errors.As(err, &se) && se.Code == http.StatusConflict {
			d.mu.Lock()
			d.current = nil
			d.mu.Unlock()
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	d.mu.Lock()
	d.current = nil
	d.usage.lastReported = time.Now()
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"result": q.Result, "task": g.Task.ID, "tokens_reported": tokens, "reply": out})
}

func (d *Daemon) handleStatus(w http.ResponseWriter, _ *http.Request) {
	d.refreshUsage()
	d.mu.Lock()
	defer d.mu.Unlock()
	st := map[string]any{"agent": d.o.Agent, "waiting": d.busy, "max_wait_ms": d.wait.max.Milliseconds(), "spent_tokens": d.usage.spent, "harness": d.usage.reading}
	if d.current != nil {
		st["task"] = d.current.Task.ID
	}
	if d.pending != nil {
		st["pending"] = d.pending.Task.ID
	}
	if d.o.TokenBudget > 0 {
		st["token_budget"] = d.o.TokenBudget
	}
	writeJSON(w, http.StatusOK, st)
}

// keepalive does the routine work of a lease while the model works, and gives back a task
// nobody came for.
func (d *Daemon) keepalive(ctx context.Context) {
	tick := time.NewTicker(d.o.Heartbeat)
	defer tick.Stop()
	lastProgress := time.Now()
	lastIdleUsage := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stop:
			return
		case <-tick.C:
		}
		d.mu.Lock()
		pending, current, pendAt := d.pending, d.current, d.pendAt
		idle := time.Since(d.last) > d.o.IdleExit && current == nil && pending == nil && !d.busy
		d.mu.Unlock()
		if idle {
			d.o.Log.Info("agentd: no one asked for work for a while; stopping", "agent", d.o.Agent)
			d.shutdown()
			return
		}
		if pending != nil && time.Since(pendAt) > d.o.PendingTTL {
			d.release(pending, "taken for the agent, but it did not come for it")
			d.mu.Lock()
			d.pending = nil
			d.mu.Unlock()
			continue
		}
		g := current
		if g == nil {
			g = pending
		}
		if g == nil {
			if time.Since(lastIdleUsage) > d.o.UsageEvery {
				lastIdleUsage = time.Now()
				if t := d.unreported(); t > 0 { // Turns spent between tasks count for the agent.
					_ = d.cl().Post(ctx, "/v1/agent/usage", api.UsageReq{Tokens: t}, nil)
				}
			}
			continue
		}
		// While the model works, its files are saved now and then, which renews the lease
		// and lets the swarm see real progress; otherwise a heartbeat renews it.
		path, q := "/v1/agent/heartbeat", api.LeaseReq{TaskID: g.Task.ID, Lease: g.Lease}
		if g == current && g.Job != "review" && time.Since(lastProgress) > d.o.Progress {
			path, lastProgress = "/v1/agent/progress", time.Now()
			q.Tokens = d.unreported()
		}
		err := d.cl().Post(ctx, path, q, nil)
		var se *client.StatusError
		switch {
		case errors.As(err, &se) && se.Code == http.StatusConflict:
			d.mu.Lock()
			if d.current == g {
				d.current = nil
			}
			if d.pending == g {
				d.pending = nil
			}
			d.lost = fmt.Sprintf("task %s was taken back by the swarm (%s)", g.Task.ID, se.Msg)
			d.mu.Unlock()
		case err != nil:
			d.connect(ctx)
		}
		d.checkLimit(ctx)
	}
}

func (d *Daemon) release(g *swarm.Grant, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if g.Job == "review" {
		return // A review lease simply lapses.
	}
	_ = d.cl().Post(ctx, "/v1/agent/release", api.LeaseReq{TaskID: g.Task.ID, Lease: g.Lease, Note: why}, nil)
}

// handBack returns a task taken for the model but never handed to it.
func (d *Daemon) handBack(why string) {
	d.mu.Lock()
	g := d.pending
	d.pending = nil
	d.mu.Unlock()
	if g != nil {
		d.release(g, why)
	}
}

// reading reads the harness's log since a moment, finding it first if need be.
func (d *Daemon) reading(since time.Time) Reading {
	d.mu.Lock()
	rd, file := d.usage.reader, d.usage.file
	d.mu.Unlock()
	if rd == nil {
		for _, cand := range d.o.Readers {
			for _, cwd := range []string{d.o.CWD, filepath.Dir(d.o.CWD)} {
				if f := cand.Find(cwd, d.o.Agent, d.started); f != "" {
					rd, file = cand, f
					break
				}
			}
			if rd != nil {
				break
			}
		}
		if rd == nil {
			return Reading{LimitUsed: -1}
		}
		d.mu.Lock()
		d.usage.reader, d.usage.file = rd, file
		d.mu.Unlock()
	}
	r, err := rd.Read(file, since)
	if err != nil {
		return Reading{LimitUsed: -1}
	}
	return r
}

func (d *Daemon) refreshUsage() {
	r := d.reading(d.started)
	d.mu.Lock()
	d.usage.reading = r
	d.usage.spent = r.Tokens
	d.mu.Unlock()
}

// unreported is the tokens spent since the last report, which it marks reported.
func (d *Daemon) unreported() int64 {
	d.mu.Lock()
	since := d.usage.lastReported
	d.mu.Unlock()
	r := d.reading(since)
	d.mu.Lock()
	d.usage.lastReported = time.Now()
	d.mu.Unlock()
	d.refreshUsage()
	return r.Tokens
}

// checkLimit tells the swarm when the harness's log says the plan's limit is used up.
func (d *Daemon) checkLimit(ctx context.Context) {
	d.mu.Lock()
	r := d.usage.reading
	d.mu.Unlock()
	if r.LimitUsed < 100 {
		return
	}
	_ = d.cl().Post(ctx, "/v1/agent/limit", api.LimitReq{Reason: "usage_limit", Until: r.LimitResets}, nil)
}

// --- How long work next may wait, learned from the harness.

// waitLearner starts under the shortest common command limit, two minutes in Claude Code,
// grows while commands are allowed to run to the end, and settles just under the point where
// the harness was seen to cut one off.
type waitLearner struct {
	max, limit time.Duration
}

const (
	waitStart  = 110 * time.Second
	waitStep   = 2 * time.Minute
	waitMax    = 55 * time.Minute
	waitMin    = 20 * time.Second
	waitMargin = 5 * time.Second
)

func newWaitLearner() waitLearner { return waitLearner{max: waitStart, limit: waitMax} }

func (l *waitLearner) completed(ran time.Duration) {
	if l.limit == 0 {
		*l = newWaitLearner()
	}
	if ran >= l.max-2*time.Second {
		l.max = min(l.max+waitStep, l.limit)
	}
}

func (l *waitLearner) cutShort(ran time.Duration) {
	if ran < waitMin {
		return // Too soon to be a timeout.
	}
	l.limit = max(ran-waitMargin, waitMin)
	l.max = l.limit
}

// ErrNoDaemon means no daemon answers for the agent.
var ErrNoDaemon = errors.New("no helper running for this agent")

// Call talks to an agent's daemon.
func Call(ctx context.Context, dir, agent, method, path string, in, out any) error {
	st, err := ReadState(dir, agent)
	if err != nil {
		return ErrNoDaemon
	}
	c := client.New("http://"+st.Addr, "")
	c.Header = map[string]string{"X-Agentd-Secret": st.Secret}
	if err := c.Do(ctx, method, path, in, out); err != nil {
		var se *client.StatusError
		if errors.As(err, &se) {
			return err
		}
		if strings.Contains(err.Error(), "connection refused") {
			return ErrNoDaemon
		}
		return err
	}
	return nil
}
