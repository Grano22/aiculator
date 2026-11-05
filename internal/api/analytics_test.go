package api_test

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/auth"
	"github.com/Grano22/aiculator/internal/client"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/pkg/observe"
)

func status(err error) int {
	var se *client.StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

func TestAnalyticsRouteChecksItsFilters(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	viewer, _ := f.auth.Issue(auth.Claims{Agent: "eye", Viewer: true})
	c := client.New(f.url, viewer)
	var a observe.Analytics
	if err := c.Get(ctx, "/v1/analytics?window=7d&tier=2&label=team%3Dweb&points=10", &a); err != nil {
		t.Fatal(err)
	}
	if len(a.CFD.Times) != 10 {
		t.Errorf("points: %d", len(a.CFD.Times))
	}
	for _, bad := range []string{"window=soon", "window=-1h", "tier=x", "points=-2"} {
		if err := c.Get(ctx, "/v1/analytics?"+bad, &a); status(err) != 400 {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if err := client.New(f.url, "").Get(ctx, "/v1/analytics", &a); status(err) != 401 {
		t.Errorf("no token: %v", err)
	}
}

func TestAnalyticsQuery(t *testing.T) {
	q, err := api.AnalyticsQuery(url.Values{"window": {"1.5d"}, "label": {"team"}})
	if err != nil || q.Window.Hours() != 36 || q.Label != "team" {
		t.Errorf("%+v %v", q, err)
	}
	if q, err := api.AnalyticsQuery(url.Values{"window": {"all"}}); err != nil || q.Window != 0 {
		t.Errorf("all: %+v %v", q, err)
	}
	long := make([]byte, 201)
	if _, err := api.AnalyticsQuery(url.Values{"label": {string(long)}}); err == nil {
		t.Error("a long label was accepted")
	}
}

// Restores and backups are for admins only, and a restore must be of the task it names.
func TestBackupAndRestoreRoutes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tok, _ := f.auth.Issue(auth.Claims{Agent: "root", Admin: true})
	admin := client.New(f.url, tok)
	agentTok, _ := f.auth.Issue(auth.Claims{Agent: "w", Tier: 1})
	agent := client.New(f.url, agentTok)

	var st []any
	if err := admin.Get(ctx, "/v1/backup", &st); err != nil || len(st) != 0 {
		t.Errorf("status with no backups: %v %v", st, err)
	}
	if err := admin.Post(ctx, "/v1/backup/snapshot", map[string]string{}, nil); status(err) != 404 {
		t.Errorf("snapshot with no backups: %v", err)
	}
	if err := agent.Get(ctx, "/v1/backup", &st); status(err) != 403 {
		t.Errorf("an agent read backups: %v", err)
	}

	if _, err := f.svc.Engine.AddTask(ctx, engine.TaskSpec{ID: "job", Title: "original"}); err != nil {
		t.Fatal(err)
	}
	cp, err := f.svc.Engine.Get(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	cp.Title = "from the backup"
	if err := admin.Post(ctx, "/v1/tasks/other/restore", cp, nil); status(err) != 400 {
		t.Errorf("a copy of another task: %v", err)
	}
	if err := agent.Post(ctx, "/v1/tasks/job/restore", cp, nil); status(err) != 403 {
		t.Errorf("an agent restored: %v", err)
	}
	var res engine.Restored
	if err := admin.Post(ctx, "/v1/tasks/job/restore", cp, &res); err != nil || !res.Replaced || res.Task.Title != "from the backup" {
		t.Errorf("restore: %+v %v", res, err)
	}
	bad := core.Task{ID: "../x"}
	if err := admin.Post(ctx, "/v1/tasks/../x/restore", bad, nil); err == nil {
		t.Error("a bad id was restored")
	}
}
