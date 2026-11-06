package api_test

import (
	"context"
	"testing"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/auth"
	"github.com/Grano22/aiculator/internal/client"
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
