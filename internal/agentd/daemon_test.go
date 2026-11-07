package agentd

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/auth"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/swarm"
)

var bg = context.Background()

type rig struct {
	t   *testing.T
	url string
	svc *swarm.Service
	au  *auth.Authority
	dir string
}

func newRig(t *testing.T, cfg engine.Config, seed ...core.Task) *rig {
	t.Helper()
	st, err := store.Open(bg, store.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range seed {
		if err := st.RawPutForTest(s); err != nil {
			t.Fatal(err)
		}
	}
	if cfg.RestartGrace == 0 {
		cfg.RestartGrace = time.Nanosecond
	}
	svc, err := swarm.New(bg, swarm.Options{Store: st, Engine: cfg})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := auth.New()
	srv := &api.Server{S: svc, Auth: a}
	hs := httptest.NewServer(srv.Routes())
	t.Cleanup(func() { hs.Close(); svc.Close(); st.Close() })
	return &rig{t: t, url: hs.URL, svc: svc, au: a, dir: t.TempDir()}
}

func (r *rig) daemon(agent string, edit func(*Options)) *Daemon {
	r.t.Helper()
	tok, _ := r.au.Issue(auth.Claims{Agent: agent, Tier: 1})
	o := Options{Agent: agent, Token: tok, Dir: r.dir, Resolve: func() (string, error) { return r.url, nil },
		Heartbeat: 100 * time.Millisecond, Progress: time.Hour, PendingTTL: time.Hour, IdleExit: time.Hour, Readers: []Reader{}}
	if edit != nil {
		edit(&o)
	}
	d, err := New(o)
	if err != nil {
		r.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	r.t.Cleanup(func() { cancel(); <-done })
	for range 200 {
		if _, err := ReadState(r.dir, agent); err == nil {
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatal("the daemon did not start")
	return nil
}

func (r *rig) add(id string) {
	r.t.Helper()
	if _, err := r.svc.Engine.AddTask(bg, engine.TaskSpec{ID: id, Title: "job " + id, AllowEmpty: true}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) next(ctx context.Context, agent string, maxMs int64) (Reply, error) {
	var rep Reply
	err := Call(ctx, r.dir, agent, "POST", "/next", map[string]int64{"max_ms": maxMs}, &rep)
	return rep, err
}

func (r *rig) state(id string) core.Task {
	r.t.Helper()
	x, err := r.svc.Engine.Get(bg, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return x
}

func TestNextHandsOverATaskAndDoneFinishesIt(t *testing.T) {
	r := newRig(t, engine.Config{})
	r.daemon("w", nil)
	r.add("a")
	rep, err := r.next(bg, "w", 5000)
	if err != nil || rep.Action != "task" || rep.Grant.Task.ID != "a" {
		t.Fatalf("next: %+v %v", rep, err)
	}
	// A second next while holding a task is refused, so a forgotten done shows.
	if _, err := r.next(bg, "w", 1000); err == nil {
		t.Error("next while holding a task worked")
	}
	var out map[string]any
	if err := Call(bg, r.dir, "w", "POST", "/done", DoneReq{Result: "submit", Note: "did it"}, &out); err != nil {
		t.Fatal(err)
	}
	if s := r.state("a").State; s == core.Claimed || s == core.Running || s == core.Ready {
		t.Errorf("after done: %s", s)
	}
	if err := Call(bg, r.dir, "w", "POST", "/done", DoneReq{Result: "submit"}, &out); err == nil {
		t.Error("done with no task worked")
	}
	if err := Call(bg, r.dir, "w", "POST", "/done", DoneReq{Result: "nonsense"}, &out); err == nil {
		t.Error("an unknown result was taken")
	}
}

func TestNextSaysAgainBeforeTheHarnessWould(t *testing.T) {
	r := newRig(t, engine.Config{})
	r.daemon("w", nil)
	start := time.Now()
	rep, err := r.next(bg, "w", 2500)
	if err != nil || rep.Action != "again" {
		t.Fatalf("%+v %v", rep, err)
	}
	if took := time.Since(start); took > 4*time.Second || took < 1500*time.Millisecond {
		t.Errorf("waited %v for a 2.5s limit", took)
	}
}

// The harness kills work next just as a task arrives: the task is kept, its lease alive, and
// handed over by the next call. Nothing is lost and nothing waits for a lease to run out.
func TestATaskArrivingAsTheCommandIsKilledIsKept(t *testing.T) {
	r := newRig(t, engine.Config{LeaseTTL: time.Second})
	d := r.daemon("w", nil)
	ctx, cancel := context.WithTimeout(bg, 400*time.Millisecond)
	defer cancel()
	go func() { time.Sleep(700 * time.Millisecond); r.add("late") }()
	if _, err := r.next(ctx, "w", 8000); err == nil {
		t.Fatal("the killed call returned normally")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		p := d.pending
		d.mu.Unlock()
		if p != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(1500 * time.Millisecond) // Longer than the lease: heartbeats must keep it.
	if x := r.state("late"); x.State != core.Claimed && x.State != core.Running {
		t.Fatalf("the kept task is %s", x.State)
	}
	rep, err := r.next(bg, "w", 3000)
	if err != nil || rep.Action != "task" || rep.Grant.Task.ID != "late" || rep.WaitedMs > 100 {
		t.Fatalf("next after the kill: %+v %v", rep, err)
	}
	d.mu.Lock()
	learned := d.wait.max
	d.mu.Unlock()
	if learned != waitStart {
		t.Logf("learned %v (a kill under %v is not taken as the harness limit)", learned, waitMin)
	}
}

func TestATaskNobodyComesForGoesBack(t *testing.T) {
	r := newRig(t, engine.Config{})
	d := r.daemon("w", func(o *Options) { o.PendingTTL = 300 * time.Millisecond })
	r.add("t")
	g, err := r.svc.Wait(bg, engine.Agent{ID: "w", Tier: 1}, 0)
	if err != nil || g.Action != engine.ActionTask {
		t.Fatal(g, err)
	}
	d.mu.Lock()
	d.pending, d.pendAt = &g, time.Now()
	d.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for r.state("t").State != core.Ready && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if x := r.state("t"); x.State != core.Ready || x.Failures != 0 {
		t.Errorf("not handed back cleanly: %+v", x)
	}
}

func TestWorkingKeepsTheLease(t *testing.T) {
	r := newRig(t, engine.Config{LeaseTTL: 800 * time.Millisecond})
	r.daemon("w", nil)
	r.add("long")
	if rep, err := r.next(bg, "w", 3000); err != nil || rep.Action != "task" {
		t.Fatal(rep, err)
	}
	time.Sleep(2 * time.Second) // The model works; it calls nothing.
	if x := r.state("long"); x.Owner != "w" {
		t.Errorf("the lease lapsed while the model worked: %+v", x)
	}
}

func TestBudgetHandsBackWhatCannotBeAfforded(t *testing.T) {
	now := time.Now()
	var seed []core.Task
	for _, id := range []string{"h1", "h2", "h3"} {
		seed = append(seed, core.Task{ID: id, Title: "job big", Tier: 1, State: core.Done, Tokens: 500_000, Submitter: "x", Verifier: "y",
			ChecksRun: true, ChecksPassed: true, CreatedAt: now, UpdatedAt: now})
	}
	r := newRig(t, engine.Config{}, seed...)
	r.daemon("w", func(o *Options) {
		o.TokenBudget = 10_000
		o.Readers = []Reader{fakeReader{tokens: 2_000}}
	})
	r.add("big")
	rep, err := r.next(bg, "w", 3000)
	if err != nil || rep.Action != "exit" {
		t.Fatalf("over budget: %+v %v", rep, err)
	}
	if x := r.state("big"); x.State != core.Ready || x.Failures != 0 {
		t.Errorf("the task was not handed back cleanly: %+v", x)
	}
}

func TestWaitLearner(t *testing.T) {
	l := newWaitLearner()
	l.completed(l.max)
	if l.max != waitStart+waitStep {
		t.Errorf("grew to %v", l.max)
	}
	l.cutShort(2 * time.Minute)
	if l.max != 2*time.Minute-waitMargin {
		t.Errorf("after a cut at 2m: %v", l.max)
	}
	l.completed(l.max)
	if l.max != 2*time.Minute-waitMargin {
		t.Errorf("grew past the cut: %v", l.max)
	}
	l.cutShort(5 * time.Second)
	if l.max != 2*time.Minute-waitMargin {
		t.Errorf("a quick kill moved it: %v", l.max)
	}
}

func TestCallWithoutADaemon(t *testing.T) {
	if err := Call(bg, t.TempDir(), "nobody", "GET", "/status", nil, nil); !errors.Is(err, ErrNoDaemon) {
		t.Errorf("%v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(StatePath(dir, "x"), []byte(`{"addr":"127.0.0.1:1","secret":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Call(bg, dir, "x", "GET", "/status", nil, nil); !errors.Is(err, ErrNoDaemon) {
		t.Errorf("a dead daemon: %v", err)
	}
	_ = filepath.Join
}

type fakeReader struct{ tokens int64 }

func (fakeReader) Name() string                          { return "fake" }
func (fakeReader) Find(string, string, time.Time) string { return "fake.jsonl" }
func (f fakeReader) Read(string, time.Time) (Reading, error) {
	return Reading{Source: "fake", Tokens: f.tokens, LimitUsed: -1}, nil
}
