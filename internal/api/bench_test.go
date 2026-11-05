package api_test

import (
	"context"
	"testing"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/auth"
	"github.com/Grano22/aiculator/internal/client"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/swarm"
)

// BenchmarkHTTPHeartbeat is what an agent pays for its most common call, end to end: HTTP,
// token check, the engine and, when needed, the store.
func BenchmarkHTTPHeartbeat(b *testing.B) {
	f := setup(b)
	ctx := context.Background()
	if _, err := f.svc.Engine.AddTask(ctx, engine.TaskSpec{ID: "job", Title: "x", AllowEmpty: true}); err != nil {
		b.Fatal(err)
	}
	tok, err := f.auth.Issue(auth.Claims{Agent: "w", Tier: 1})
	if err != nil {
		b.Fatal(err)
	}
	c := client.New(f.url, tok)
	var g swarm.Grant
	if err := c.Post(ctx, "/v1/agent/wait", api.WaitReq{HoldMs: 0}, &g); err != nil || g.Action != engine.ActionTask {
		b.Fatal(g, err)
	}
	q := api.LeaseReq{TaskID: g.Task.ID, Lease: g.Lease}
	b.ReportAllocs()
	for b.Loop() {
		if err := c.Post(ctx, "/v1/agent/heartbeat", q, nil); err != nil {
			b.Fatal(err)
		}
	}
}
