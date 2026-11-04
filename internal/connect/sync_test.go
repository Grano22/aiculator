package connect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/swarm"
)

// fakeRemote is another system, held in memory.
type fakeRemote struct {
	mu     sync.Mutex
	items  map[string]Item
	pushes []string
	gets   int
	pulls  int
	failN  int
	refuse bool
	block  chan struct{}
	secret string
	pushAt []time.Time
}

func newRemote() *fakeRemote { return &fakeRemote{items: map[string]Item{}} }

func (f *fakeRemote) put(key, title, status, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[key] = Item{Key: key, Version: version, Status: status, Fields: map[string]any{"title": title}}
}

func (f *fakeRemote) Pull(_ context.Context, _ PullOptions, yield func(Item) error) error {
	f.mu.Lock()
	f.pulls++
	items := make([]Item, 0, len(f.items))
	for _, it := range f.items {
		items = append(items, it)
	}
	f.mu.Unlock()
	for _, it := range items {
		if err := yield(it); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeRemote) Get(_ context.Context, key string) (Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	it, ok := f.items[key]
	if !ok {
		return Item{}, errors.New("no such item")
	}
	return it, nil
}

func (f *fakeRemote) SetStatus(ctx context.Context, key, status string) error {
	f.mu.Lock()
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse {
		return ErrPermanent
	}
	if f.failN > 0 {
		f.failN--
		return errors.New("temporarily down")
	}
	f.pushes = append(f.pushes, key+"="+status)
	f.pushAt = append(f.pushAt, time.Now())
	if it, ok := f.items[key]; ok {
		it.Status = status
		f.items[key] = it
	}
	return nil
}

func (f *fakeRemote) VerifyHook(secret string, h http.Header, _ []byte) error {
	if h.Get("X-Test-Signature") != secret {
		return errors.New("wrong signature")
	}
	return nil
}

func (f *fakeRemote) HookKeys(body []byte) ([]string, error) { return []string{string(body)}, nil }

func (f *fakeRemote) pushed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.pushes)
}

type fixture struct {
	t    *testing.T
	svc  *swarm.Service
	st   *store.Store
	src  *Source
	fake *fakeRemote
	y    *Syncer
}

func testSource(sc *SyncConfig) *Source {
	return &Source{Name: "fake", Kind: "jira", Fields: Fields{Title: &FieldSpec{From: "title"}}, Sync: sc}
}

func fix(t *testing.T, sc *SyncConfig) *fixture {
	t.Helper()
	st, err := store.Open(bg, store.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := swarm.New(bg, swarm.Options{Store: st, Engine: engine.Config{RestartGrace: time.Nanosecond, LeaseTTL: time.Hour, ProgressTTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, svc: svc, st: st, src: testSource(sc), fake: newRemote()}
	t.Cleanup(func() {
		if f.y != nil {
			f.y.Close()
		}
		svc.Close()
		st.Close()
	})
	return f
}

func (f *fixture) start() {
	f.t.Helper()
	f.y = Start(bg, &Config{Sources: []*Source{f.src}}, f.svc, Options{Open: func(*Source) (Connector, error) { return f.fake, nil }})
	// The consumer starts from "now", so wait until it exists before making commits.
	time.Sleep(50 * time.Millisecond)
}

// imp stores an item as an import would.
func (f *fixture) imp(key, status string) string {
	f.t.Helper()
	f.fake.put(key, "title "+key, status, "1")
	m, err := f.src.Map(f.fake.items[key])
	if err != nil {
		f.t.Fatal(err)
	}
	res, err := f.svc.Import(bg, []swarm.ImportItem{{Spec: m.Spec, Props: m.Props, Link: m.Link}}, false)
	if err != nil || res[0].Error != "" {
		f.t.Fatalf("%v %+v", err, res)
	}
	return m.Spec.ID
}

func wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (f *fixture) waitPushes(want ...string) {
	f.t.Helper()
	wait(f.t, "pushes "+strings.Join(want, " "), func() bool { return slices.Equal(f.fake.pushed(), want) })
}

// quiet checks that nothing more arrives.
func (f *fixture) quiet(want ...string) {
	f.t.Helper()
	time.Sleep(150 * time.Millisecond)
	if got := f.fake.pushed(); !slices.Equal(got, want) {
		f.t.Fatalf("pushes: got %v, want %v", got, want)
	}
}

var agent = engine.Agent{ID: "ag", Tier: 1}

var statuses = map[string]string{"claimed": "In Progress", "running": "In Progress", "review": "In Review", "done": "Done", "canceled": "Cancelled"}

func TestRealtimePushOfEachStatusChange(t *testing.T) {
	f := fix(t, &SyncConfig{Status: statuses, Direction: "push"})
	f.start()
	id := f.imp("K-1", "To Do")
	r, err := f.svc.Engine.Wait(bg, agent, 0)
	if err != nil || r.Task == nil {
		t.Fatalf("%+v %v", r, err)
	}
	f.waitPushes("K-1=In Progress")
	// A heartbeat changes nothing the other side cares about, and running maps to the same status.
	for range 5 {
		if _, err := f.svc.Engine.Heartbeat(bg, agent, id, r.Lease); err != nil {
			t.Fatal(err)
		}
	}
	f.quiet("K-1=In Progress")
	if _, err := f.svc.Engine.Submit(bg, agent, id, r.Lease, "", "", "", core.Usage{}); err != nil {
		t.Fatal(err)
	}
	f.waitPushes("K-1=In Progress", "K-1=In Review")
	l, _, err := f.st.GetLink("fake", id)
	if err != nil || l.RemoteStatus != "In Review" {
		t.Fatalf("%+v %v", l, err)
	}
	st := f.y.Statuses()[0]
	if st.Pushed != 2 || st.Pending != 0 || st.LastError != "" {
		t.Errorf("%+v", st)
	}
}

func TestLastModeSendsOnlyTheEnd(t *testing.T) {
	f := fix(t, &SyncConfig{Status: statuses, Direction: "push", Push: PushConfig{Updates: "last"}})
	f.start()
	id := f.imp("K-1", "To Do")
	if _, err := f.svc.Engine.Wait(bg, agent, 0); err != nil {
		t.Fatal(err)
	}
	f.quiet()
	if _, err := f.svc.Engine.Cancel(bg, id, "stop"); err != nil {
		t.Fatal(err)
	}
	f.waitPushes("K-1=Cancelled")
}

func TestIntervalModeCoalescesABurst(t *testing.T) {
	f := fix(t, &SyncConfig{Status: statuses, Direction: "push", Push: PushConfig{Mode: "interval", Every: 300 * time.Millisecond}})
	f.start()
	id := f.imp("K-1", "To Do")
	r, err := f.svc.Engine.Wait(bg, agent, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Engine.Submit(bg, agent, id, r.Lease, "", "", "", core.Usage{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Engine.Cancel(bg, id, "stop"); err != nil {
		t.Fatal(err)
	}
	f.waitPushes("K-1=Cancelled") // Three changes, one call, and the last one wins.
	f.quiet("K-1=Cancelled")
}

func TestTemporaryFailureIsRetried(t *testing.T) {
	f := fix(t, &SyncConfig{Status: statuses, Direction: "push"})
	f.fake.failN = 1
	f.start()
	f.imp("K-1", "To Do")
	if _, err := f.svc.Engine.Wait(bg, agent, 0); err != nil {
		t.Fatal(err)
	}
	wait(t, "the error to show", func() bool { return f.y.Statuses()[0].LastError != "" })
	f.waitPushes("K-1=In Progress")
	wait(t, "the error to clear", func() bool { return f.y.Statuses()[0].LastError == "" })
}

func TestRefusalIsReportedAndDoesNotBlockOthers(t *testing.T) {
	f := fix(t, &SyncConfig{Status: statuses, Direction: "push"})
	f.fake.refuse = true
	f.start()
	f.imp("K-1", "To Do")
	if _, err := f.svc.Engine.Wait(bg, agent, 0); err != nil {
		t.Fatal(err)
	}
	wait(t, "the refusal", func() bool { return f.y.Statuses()[0].Failed == 1 })
	if p := f.y.Statuses()[0].Pending; p != 0 {
		t.Errorf("a refused push is still pending: %d", p)
	}
	f.fake.mu.Lock()
	f.fake.refuse = false
	f.fake.mu.Unlock()
	f.imp("K-2", "To Do")
	if _, err := f.svc.Engine.Wait(bg, agent, 0); err != nil {
		t.Fatal(err)
	}
	f.waitPushes("K-2=In Progress")
}

// A dead remote must never slow the swarm, and what it missed must arrive afterwards.
func TestStoppedRemoteDoesNotSlowClaimsAndCatchesUpAfterRestart(t *testing.T) {
	f := fix(t, &SyncConfig{Status: statuses, Direction: "push"})
	f.fake.block = make(chan struct{})
	f.start()
	f.imp("K-1", "To Do")
	var worst time.Duration
	for i := range 200 {
		id := "plain-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if _, err := f.svc.Engine.AddTask(bg, engine.TaskSpec{ID: id, Title: "x"}); err != nil {
			t.Fatal(err)
		}
		at := time.Now()
		if r, err := f.svc.Engine.Wait(bg, engine.Agent{ID: "b", Tier: 1}, 0); err != nil || r.Task == nil {
			t.Fatalf("%+v %v", r, err)
		}
		worst = max(worst, time.Since(at))
	}
	if worst > 100*time.Millisecond {
		t.Errorf("a claim took %v while the remote was stuck", worst)
	}
	wait(t, "the stuck push to be pending", func() bool { return f.y.Statuses()[0].Pending == 1 })
	f.y.Close() // The server goes down with the push unsent.
	f.fake.mu.Lock()
	f.fake.block = nil
	f.fake.mu.Unlock()
	f.start()
	f.waitPushes("K-1=In Progress")
}

func TestOnlyThisSourcesTasksAreSent(t *testing.T) {
	f := fix(t, &SyncConfig{Status: statuses, Direction: "push"})
	f.start()
	if _, err := f.svc.Engine.AddTask(bg, engine.TaskSpec{ID: "plain", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Engine.AddTask(bg, engine.TaskSpec{ID: "other", Title: "x", Labels: map[string]string{LabelSource: "another", LabelExternal: "O-1"}}); err != nil {
		t.Fatal(err)
	}
	// Same label, but it was never imported from here: nothing links it to an item.
	if _, err := f.svc.Engine.AddTask(bg, engine.TaskSpec{ID: "forged", Title: "x", Labels: map[string]string{LabelSource: "fake", LabelExternal: "K-9"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"plain", "other", "forged"} {
		if _, err := f.svc.Engine.Cancel(bg, id, "x"); err != nil {
			t.Fatal(err)
		}
	}
	f.quiet()
}

func TestPullUpdatesTheWordsAndNotTheProgress(t *testing.T) {
	f := fix(t, &SyncConfig{Status: statuses, Direction: "pull", Pull: PullConfig{Mode: "interval", Every: 30 * time.Millisecond}})
	id := f.imp("K-1", "To Do")
	f.start()
	r, err := f.svc.Engine.Wait(bg, agent, 0)
	if err != nil || r.Task == nil {
		t.Fatalf("%+v %v", r, err)
	}
	f.fake.put("K-1", "a new title", "To Do", "2")
	wait(t, "the new title", func() bool {
		x, err := f.svc.Engine.Get(bg, id)
		return err == nil && x.Title == "a new title"
	})
	x, _ := f.svc.Engine.Get(bg, id)
	if x.State != core.Claimed || x.Owner != "ag" {
		t.Errorf("a pull changed progress: %+v", x)
	}
	if got := f.fake.pushed(); len(got) != 0 {
		t.Errorf("direction pull should send nothing: %v", got)
	}
}

func TestCreateBringsInNewItemsOnlyWhenAsked(t *testing.T) {
	f := fix(t, &SyncConfig{Direction: "pull", Pull: PullConfig{Mode: "once"}})
	f.fake.put("K-1", "new", "To Do", "1")
	f.start()
	wait(t, "the pull", func() bool {
		f.fake.mu.Lock()
		defer f.fake.mu.Unlock()
		return f.fake.pulls == 1
	})
	time.Sleep(50 * time.Millisecond)
	if ts, _ := f.svc.Engine.Existing(bg, []string{"fake-K-1"}); len(ts) != 0 {
		t.Fatalf("created without create: %+v", ts)
	}
	f.y.Close()
	f.src.Sync.Create = true
	f.start()
	wait(t, "the task", func() bool {
		ts, _ := f.svc.Engine.Existing(bg, []string{"fake-K-1"})
		return len(ts) == 1
	})
	f.fake.mu.Lock()
	pulls := f.fake.pulls
	f.fake.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	if f.fake.pulls != pulls {
		t.Errorf("mode once read again: %d then %d", pulls, f.fake.pulls)
	}
}

// The heart of two-way sync. The remote cancels, the task is canceled, and that must not
// be sent back as if it were news.
func TestRemoteCancelDoesNotEcho(t *testing.T) {
	f := fix(t, &SyncConfig{Status: statuses, OnRemote: map[string]string{"Cancelled": "cancel"}, Pull: PullConfig{Mode: "interval", Every: 30 * time.Millisecond}})
	id := f.imp("K-1", "To Do")
	f.start()
	r, err := f.svc.Engine.Wait(bg, agent, 0)
	if err != nil || r.Task == nil {
		t.Fatalf("%+v %v", r, err)
	}
	f.waitPushes("K-1=In Progress")
	f.fake.put("K-1", "title K-1", "Cancelled", "2")
	wait(t, "the cancel", func() bool {
		x, err := f.svc.Engine.Get(bg, id)
		return err == nil && x.State == core.Canceled
	})
	f.quiet("K-1=In Progress") // Not K-1=Cancelled.
	if st := f.y.Statuses()[0]; st.Canceled != 1 || st.Echoes != 1 {
		t.Errorf("%+v", st)
	}
	// The agent holding it is fenced out, as with any other cancel.
	if _, err := f.svc.Engine.Heartbeat(bg, agent, id, r.Lease); err == nil {
		t.Error("the former holder could still heartbeat")
	}
}

// Our own push comes back from the remote as a change. It must settle there.
func TestOwnPushComingBackChangesNothing(t *testing.T) {
	f := fix(t, &SyncConfig{Status: statuses, OnRemote: map[string]string{"Cancelled": "cancel"}, Pull: PullConfig{Mode: "interval", Every: 20 * time.Millisecond}})
	id := f.imp("K-1", "To Do")
	f.start()
	if _, err := f.svc.Engine.Wait(bg, agent, 0); err != nil {
		t.Fatal(err)
	}
	f.waitPushes("K-1=In Progress")
	time.Sleep(200 * time.Millisecond)
	f.quiet("K-1=In Progress")
	if x, _ := f.svc.Engine.Get(bg, id); x.State != core.Claimed {
		t.Errorf("%+v", x)
	}
}

func hookFixture(t *testing.T) (*fixture, *httptest.Server) {
	t.Helper()
	t.Setenv("T_HOOK", "sekret")
	f := fix(t, &SyncConfig{Status: statuses, HookSecretEnv: "T_HOOK", OnRemote: map[string]string{"Cancelled": "cancel"}, Pull: PullConfig{Mode: "webhook"}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.y.ServeHook(w, r, "fake") }))
	t.Cleanup(srv.Close)
	return f, srv
}

func post(t *testing.T, url, body, sig string) int {
	t.Helper()
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if sig != "" {
		req.Header.Set("X-Test-Signature", sig)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestWebhookReadsTheItemOnceAfterCheckingTheSignature(t *testing.T) {
	f, srv := hookFixture(t)
	id := f.imp("K-1", "To Do")
	f.start()
	f.fake.put("K-1", "changed by hook", "To Do", "2")
	for _, bad := range []string{"", "wrong"} {
		if code := post(t, srv.URL, "K-1", bad); code != http.StatusUnauthorized {
			t.Errorf("signature %q: %d", bad, code)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if x, _ := f.svc.Engine.Get(bg, id); x.Title == "changed by hook" {
		t.Fatal("an unsigned delivery changed a task")
	}
	f.fake.mu.Lock()
	before := f.fake.gets
	f.fake.mu.Unlock()
	if code := post(t, srv.URL, "K-1", "sekret"); code != http.StatusAccepted {
		t.Fatalf("%d", code)
	}
	wait(t, "the change", func() bool {
		x, err := f.svc.Engine.Get(bg, id)
		return err == nil && x.Title == "changed by hook"
	})
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	if f.fake.gets != before+1 {
		t.Errorf("fetched %d times", f.fake.gets-before)
	}
}

func TestWebhookSetupKeepsNotionsToken(t *testing.T) {
	t.Setenv("T_HOOK", "")
	f := fix(t, &SyncConfig{HookSecretEnv: "T_HOOK", Pull: PullConfig{Mode: "webhook"}})
	f.src.Kind = "notion"
	f.start()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.y.ServeHook(w, r, "fake") }))
	defer srv.Close()
	if code := post(t, srv.URL, `{"not":"a token"}`, ""); code != http.StatusServiceUnavailable {
		t.Errorf("with no secret, nothing else is accepted: %d", code)
	}
	if code := post(t, srv.URL, `{"verification_token":"abc"}`, ""); code != http.StatusOK {
		t.Fatalf("%d", code)
	}
	if tok, err := f.y.HookToken("fake"); err != nil || tok != "abc" {
		t.Fatalf("%q %v", tok, err)
	}
}

func TestSourceThatCannotStartDoesNotStopTheOthers(t *testing.T) {
	f := fix(t, &SyncConfig{Status: statuses, Direction: "push"})
	bad := testSource(&SyncConfig{Direction: "push"})
	bad.Name = "broken"
	f.y = Start(bg, &Config{Sources: []*Source{bad, f.src}}, f.svc, Options{Open: func(s *Source) (Connector, error) {
		if s.Name == "broken" {
			return nil, errors.New("missing credentials")
		}
		return f.fake, nil
	}})
	time.Sleep(50 * time.Millisecond)
	f.imp("K-1", "To Do")
	if _, err := f.svc.Engine.Wait(bg, agent, 0); err != nil {
		t.Fatal(err)
	}
	f.waitPushes("K-1=In Progress")
	if st := f.y.Statuses(); st[0].Name != "broken" || !strings.Contains(st[0].LastError, "credentials") {
		t.Errorf("%+v", st[0])
	}
}
