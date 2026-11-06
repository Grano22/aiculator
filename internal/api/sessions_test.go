package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/auth"
	"github.com/Grano22/aiculator/internal/client"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/pkg/observe"
)

// Only a control link or admin may stop agents, and a kill only reaches processes that
// aiculator started, on the server's own machine.
func TestStoppingNeedsControlAndKillsOnlyOurOwn(t *testing.T) {
	var killed []int
	f := setupWith(t, func(s *api.Server) {
		s.Host = "box"
		s.Kill = func(pid int) error { killed = append(killed, pid); return nil }
	})
	ctx := context.Background()
	issue := func(c auth.Claims) *client.Client {
		tok, err := f.auth.Issue(c)
		if err != nil {
			t.Fatal(err)
		}
		return client.New(f.url, tok)
	}
	viewer := issue(auth.Claims{Agent: "eye", Viewer: true})
	ctl := issue(auth.Claims{Agent: "ctl", Viewer: true, Control: true})
	drv := issue(auth.Claims{Agent: "drv", Tier: 1})
	bridge := issue(auth.Claims{Agent: "br", Tier: 1})
	far := issue(auth.Claims{Agent: "far", Tier: 1})
	for c, info := range map[*client.Client]observe.SessionInfo{
		drv:    {Kind: "acp", Host: "box", PID: 101, Killable: true},
		bridge: {Kind: "mcp", Host: "box", PID: 102},
		far:    {Kind: "acp", Host: "elsewhere", PID: 103, Killable: true},
	} {
		if err := c.Post(ctx, "/v1/agent/hello", info, nil); err != nil {
			t.Fatal(err)
		}
	}
	var me map[string]any
	if err := ctl.Get(ctx, "/v1/whoami", &me); err != nil || me["control"] != true {
		t.Errorf("whoami: %v %v", me, err)
	}
	if err := viewer.Get(ctx, "/v1/whoami", &me); err != nil || me["control"] != false {
		t.Errorf("viewer whoami: %v %v", me, err)
	}
	if err := viewer.Post(ctx, "/v1/agents/stop", api.StopReq{}, nil); status(err) != 403 {
		t.Errorf("a viewer stopped agents: %v", err)
	}
	if err := ctl.Post(ctx, "/v1/tokens", api.TokenReq{Agent: "x", Tier: 3}, nil); status(err) != 403 {
		t.Errorf("a control link minted a token: %v", err)
	}
	var out api.StopReply
	if err := ctl.Post(ctx, "/v1/agents/stop", api.StopReq{Kill: true}, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Stopped) != 3 || len(killed) != 1 || killed[0] != 101 {
		t.Errorf("stopped %v, killed pids %v", out.Stopped, killed)
	}
	var list []observe.SessionLive
	if err := viewer.Get(ctx, "/v1/sessions", &list); err != nil || len(list) != 3 || list[0].State != "stopping" {
		t.Errorf("sessions: %+v %v", list, err)
	}
	// A control token can only be had with viewer.
	admin := issue(auth.Claims{Agent: "root", Admin: true})
	var tok map[string]string
	if err := admin.Post(ctx, "/v1/tokens", api.TokenReq{Agent: "sneaky", Tier: 1, Control: true}, &tok); err != nil {
		t.Fatal(err)
	}
	if c, err := f.auth.Verify(tok["token"]); err != nil || c.Control {
		t.Errorf("an agent token got control: %+v %v", c, err)
	}
}

// A client that reconnects with the last sequence it handled gets everything after it, once.
func TestEventStreamResumesWithoutLosingAnything(t *testing.T) {
	f := setupProjected(t)
	ctx := context.Background()
	viewer, _ := f.auth.Issue(auth.Claims{Agent: "eye", Viewer: true})
	read := func(since uint64, n int) []api.Event {
		t.Helper()
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, fmt.Sprintf("%s/v1/events?since=%d", f.url, since), nil)
		req.Header.Set("Authorization", "Bearer "+viewer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out []api.Event
		dec := json.NewDecoder(resp.Body)
		for len(out) < n {
			var e api.Event
			if err := dec.Decode(&e); err != nil {
				t.Fatalf("after %d events: %v", len(out), err)
			}
			if e.Seq == 0 {
				continue // A gap note, not an event.
			}
			out = append(out, e)
		}
		return out
	}
	for i := range 6 {
		if _, err := f.svc.Engine.AddTask(ctx, engine.TaskSpec{ID: fmt.Sprintf("e%d", i), Title: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.Store.WaitProjected(ctx); err != nil {
		t.Fatal(err)
	}
	first := read(1, 3) // From the start of the log.
	rest := read(first[len(first)-1].Seq, 3)
	seen := map[uint64]bool{}
	for _, e := range append(first, rest...) {
		if seen[e.Seq] {
			t.Errorf("event %d came twice", e.Seq)
		}
		seen[e.Seq] = true
	}
	for i := 1; i < len(rest); i++ {
		if rest[i].Seq <= rest[i-1].Seq {
			t.Errorf("out of order: %d then %d", rest[i-1].Seq, rest[i].Seq)
		}
	}
	if rest[0].Seq != first[2].Seq+1 || rest[0].ID == "" {
		t.Errorf("resumed at %d (%s), want %d", rest[0].Seq, rest[0].ID, first[2].Seq+1)
	}
	// Bad input is refused.
	req, _ := http.NewRequest(http.MethodGet, f.url+"/v1/events?since=x", nil)
	req.Header.Set("Authorization", "Bearer "+viewer)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 400 {
		t.Errorf("since=x: %v %v", resp, err)
	}
}

// setupProjected is setup with the event log running, as in a real server.
func setupProjected(t *testing.T) *fixture {
	f := setup(t)
	if err := f.svc.Store.StartProjector(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}
