package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/auth"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/swarm"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

type fixture struct {
	url  string
	svc  *swarm.Service
	auth *auth.Authority
}

func setup(t testing.TB) *fixture { return setupWith(t, nil) }

// setupWith is setup, with a chance to configure the server first.
func setupWith(t testing.TB, configure func(*api.Server)) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := swarm.New(ctx, swarm.Options{Store: st, Engine: engine.Config{RestartGrace: time.Nanosecond, ExitGrace: 200 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New()
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{S: svc, Auth: a}
	if configure != nil {
		configure(srv)
	}
	mux := srv.Routes()
	mux.Handle("/mcp", srv.MCPHandler())
	hs := httptest.NewServer(mux)
	t.Cleanup(func() { hs.Close(); svc.Close(); st.Close() })
	return &fixture{hs.URL, svc, a}
}

func (f *fixture) session(t *testing.T, agent string, tier int) *mcp.ClientSession {
	t.Helper()
	tok, err := f.auth.Issue(auth.Claims{Agent: agent, Tier: tier})
	if err != nil {
		t.Fatal(err)
	}
	cl := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	s, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: f.url + "/mcp", HTTPClient: &http.Client{Transport: bearer{tok}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	r, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !r.IsError {
		b, _ := json.Marshal(r.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s decode: %v", name, err)
		}
	}
	return r
}

func errText(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolsAreListedWithDescriptions(t *testing.T) {
	f := setup(t)
	s := f.session(t, "a", 1)
	want := map[string]bool{"wait_for_task": false, "claim": false, "heartbeat": false, "progress": false, "submit": false, "fail": false, "review_verdict": false, "situation": false}
	for tool, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %s is missing", name)
		}
	}
	if !strings.Contains(s.InitializeResult().Instructions, "wait_for_task") {
		t.Error("server instructions do not explain the loop")
	}
}

func TestFullLoopOverMCP(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	worker, reviewer := f.session(t, "worker", 1), f.session(t, "reviewer", 1)
	for _, id := range []string{"m1", "m2", "m3"} {
		if _, err := f.svc.Engine.AddTask(ctx, engine.TaskSpec{ID: id, Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		var g swarm.Grant
		call(t, worker, "wait_for_task", map[string]any{"hold_seconds": 1}, &g)
		if g.Action != "task" {
			t.Fatalf("got %+v", g)
		}
		args := map[string]any{"task_id": g.Task.ID, "lease": g.Lease}
		call(t, worker, "heartbeat", args, nil)
		call(t, worker, "progress", map[string]any{"task_id": g.Task.ID, "lease": g.Lease, "note": "halfway"}, nil)
		call(t, worker, "submit", args, nil)

		var rv swarm.Grant
		call(t, reviewer, "wait_for_task", map[string]any{"hold_seconds": 1}, &rv)
		if rv.Job != "review" {
			t.Fatalf("review: %+v", rv)
		}
		call(t, reviewer, "review_verdict", map[string]any{"task_id": rv.Task.ID, "lease": rv.Lease, "pass": true}, nil)
	}
	ts, _ := f.svc.Engine.List(ctx)
	for _, x := range ts {
		if x.State != core.Done {
			t.Errorf("%s is %s", x.ID, x.State)
		}
	}
	var sit engine.Situation
	call(t, worker, "situation", nil, &sit)
	if sit.Agent != "worker" || sit.Counts[1]["done"] != 3 {
		t.Fatalf("situation: %+v", sit)
	}
}

func TestWaitNeverExceedsTheLimitAndSaysWhy(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ep, err := f.svc.Engine.PlanBegin(ctx, "p", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Engine.PlanUpdate(ctx, "p", ep, time.Minute, "reading spec", map[int]int{1: 4}, 1000); err != nil {
		t.Fatal(err)
	}
	s := f.session(t, "a", 1)
	start := time.Now()
	var g swarm.Grant
	call(t, s, "wait_for_task", map[string]any{"hold_seconds": 1}, &g)
	if g.Action != "wait" || g.Reason != "planning" || g.Planning.Phase != "reading spec" {
		t.Fatalf("got %+v", g)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("hold ran %v", time.Since(start))
	}
	// The server clamps the request, whatever the agent asks for.
	if api.MaxToolHold >= 45*time.Second {
		t.Fatal("hold limit is not below the 45-second gate")
	}
}

func TestExitReachesAgentOverMCP(t *testing.T) {
	f := setup(t)
	s := f.session(t, "a", 1)
	var g swarm.Grant
	call(t, s, "wait_for_task", map[string]any{"hold_seconds": 5}, &g)
	if g.Action != "exit" {
		t.Fatalf("got %+v", g)
	}
}

func TestIdentityComesFromTheToken(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.svc.Engine.AddTask(ctx, engine.TaskSpec{ID: "x", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	owner, thief := f.session(t, "owner", 1), f.session(t, "thief", 1)
	var g swarm.Grant
	call(t, owner, "wait_for_task", map[string]any{"hold_seconds": 1}, &g)
	// The thief knows the lease number but holds a different token.
	r := call(t, thief, "submit", map[string]any{"task_id": "x", "lease": g.Lease}, nil)
	if !r.IsError || !strings.Contains(errText(r), "stale lease") {
		t.Fatalf("submit by another agent: error=%v %s", r.IsError, errText(r))
	}
	// Self-review is refused too.
	call(t, owner, "submit", map[string]any{"task_id": "x", "lease": g.Lease}, nil)
	r = call(t, owner, "review_verdict", map[string]any{"task_id": "x", "lease": g.Lease, "pass": true}, nil)
	if !r.IsError {
		t.Fatal("agent approved its own work")
	}
}

func TestTierComesFromTheToken(t *testing.T) {
	f := setup(t)
	if _, err := f.svc.Engine.AddTask(context.Background(), engine.TaskSpec{ID: "hard", Title: "x", Tier: 3}); err != nil {
		t.Fatal(err)
	}
	s := f.session(t, "low", 1)
	if r := call(t, s, "claim", map[string]any{"task_id": "hard"}, nil); !r.IsError {
		t.Fatal("tier 1 token claimed a tier 3 task")
	}
}

func TestMCPRejectsBadTokens(t *testing.T) {
	f := setup(t)
	other, _ := auth.New()
	forged, _ := other.Issue(auth.Claims{Agent: "evil", Tier: 3})
	for name, tok := range map[string]string{"forged": forged, "garbage": "a.b", "empty": ""} {
		cl := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: f.url + "/mcp", HTTPClient: &http.Client{Transport: bearer{tok}},
		}, nil)
		cancel()
		if err == nil {
			s.Close()
			t.Errorf("%s token was accepted", name)
		}
	}
	// An admin token has no tier, so it cannot act as a worker.
	admin, _ := f.auth.Issue(auth.Claims{Agent: "root", Admin: true})
	cl := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	s, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: f.url + "/mcp", HTTPClient: &http.Client{Transport: bearer{admin}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if r := call(t, s, "wait_for_task", map[string]any{"hold_seconds": 1}, nil); !r.IsError {
		t.Fatal("admin token took a task")
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	f := setup(t)
	tok, _ := f.auth.Issue(auth.Claims{Agent: "old", Tier: 1, Expires: time.Now().Add(-time.Minute)})
	if _, err := f.auth.Verify(tok); err == nil {
		t.Fatal("expired token verified")
	}
}
