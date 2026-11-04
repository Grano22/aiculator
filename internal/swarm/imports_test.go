package swarm

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/meta"
	"github.com/Grano22/aiculator/internal/store"
)

// emptyQueue takes away the fixture's own task, so that an import's tasks are next.
func emptyQueue(t *testing.T) *Service {
	t.Helper()
	s := service(t)
	if _, err := s.Engine.Cancel(bg, "t", "not part of this test"); err != nil {
		t.Fatal(err)
	}
	return s
}

func item(id string, deps ...string) ImportItem {
	return ImportItem{
		Spec:  engine.TaskSpec{ID: id, Title: "title " + id, Deps: deps, Labels: map[string]string{"source": "x", "external": id}},
		Props: map[string]*meta.Prop{"p": {Value: id}},
		Link:  store.Link{Source: "x", Key: id, TaskID: id, Version: "1", RemoteStatus: "Open"},
	}
}

func actions(res []ImportResult) map[string]string {
	out := map[string]string{}
	for _, r := range res {
		out[r.ID] = r.Action
	}
	return out
}

func TestImportIsSafeToRepeat(t *testing.T) {
	s := emptyQueue(t)
	res, err := s.Import(bg, []ImportItem{item("a"), item("b", "a")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if a := actions(res); a["a"] != "created" || a["b"] != "created" {
		t.Fatalf("%+v", res)
	}
	if m, _ := s.GetMeta(bg, "a"); m.Props["p"].Value != "a" {
		t.Errorf("%+v", m)
	}
	if l, rev, err := s.Store.GetLink("x", "a"); err != nil || rev == 0 || l.RemoteStatus != "Open" {
		t.Errorf("%+v %v", l, err)
	}
	// An agent takes it, then the same import runs again.
	r, err := s.Engine.Wait(bg, engine.Agent{ID: "ag", Tier: 1}, 0)
	if err != nil || r.Task == nil || r.Task.ID != "a" {
		t.Fatalf("%+v %v", r, err)
	}
	if res, err = s.Import(bg, []ImportItem{item("a"), item("b", "a")}, false); err != nil {
		t.Fatal(err)
	}
	if a := actions(res); a["a"] != "unchanged" {
		t.Errorf("%+v", res)
	}
	if x, _ := s.Engine.Get(bg, "a"); x.State != core.Claimed || x.Owner != "ag" {
		t.Errorf("a repeat import disturbed work in progress: %+v", x)
	}
}

func TestUpdateChangesTheWordsButNotTheProgress(t *testing.T) {
	s := emptyQueue(t)
	if _, err := s.Import(bg, []ImportItem{item("a")}, false); err != nil {
		t.Fatal(err)
	}
	r, err := s.Engine.Wait(bg, engine.Agent{ID: "ag", Tier: 1}, 0)
	if err != nil || r.Task == nil {
		t.Fatalf("%+v %v", r, err)
	}
	it := item("a")
	it.Spec.Title, it.Spec.Priority, it.Link.Version = "new title", 9, "2"
	it.Spec.Labels["team"] = "core"
	it.Props["p"] = &meta.Prop{Value: "changed"}
	res, err := s.Import(bg, []ImportItem{it}, true)
	if err != nil || res[0].Action != "updated" {
		t.Fatalf("%+v %v", res, err)
	}
	x, _ := s.Engine.Get(bg, "a")
	if x.Title != "new title" || x.Priority != 9 || x.Labels["team"] != "core" || x.State != core.Claimed || x.Owner != "ag" {
		t.Errorf("%+v", x)
	}
	if m, _ := s.GetMeta(bg, "a"); m.Props["p"].Value != "changed" {
		t.Errorf("%+v", m)
	}
	if _, err := s.Engine.Heartbeat(bg, engine.Agent{ID: "ag", Tier: 1}, "a", r.Lease); err != nil {
		t.Errorf("the holder lost its lease: %v", err)
	}
	if res, err = s.Import(bg, []ImportItem{it}, true); err != nil || res[0].Action != "unchanged" {
		t.Errorf("no change should report none: %+v %v", res, err)
	}
}

// An update must not lose a file that was already fetched.
func TestUpdateKeepsAFetchedAttachment(t *testing.T) {
	s := service(t)
	it := item("a")
	it.Props["att"] = &meta.Prop{Resource: &meta.Resource{Kind: "http", URL: "https://x.example/f", Name: "f"}}
	if _, err := s.Import(bg, []ImportItem{it}, false); err != nil {
		t.Fatal(err)
	}
	ref, err := s.PutBlob(bg, strings.NewReader("kept"), "f", "")
	if err != nil {
		t.Fatal(err)
	}
	kept := meta.Resource{Kind: "http", URL: "https://x.example/f", Name: "f", Blob: &ref}
	if _, err := s.PatchMeta(bg, "a", meta.Patch{Props: map[string]*meta.Prop{"att": {Resource: &kept}}}); err != nil {
		t.Fatal(err)
	}
	it.Link.Version = "2"
	if _, err := s.Import(bg, []ImportItem{it}, true); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.GetMeta(bg, "a"); m.Props["att"].Resource.Blob == nil {
		t.Fatal("the kept copy was lost")
	}
}

func TestDependencyOutsideTheImportIsReported(t *testing.T) {
	s := service(t)
	res, err := s.Import(bg, []ImportItem{item("a", "not-imported")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Action != "created" || !strings.Contains(res[0].Warning, "not-imported") {
		t.Fatalf("%+v", res)
	}
	x, _ := s.Engine.Get(bg, "a")
	if len(x.Deps) != 0 {
		t.Errorf("it would wait forever: %+v", x)
	}
	if m, _ := s.GetMeta(bg, "a"); m.Props["unresolved_deps"].Value == nil {
		t.Errorf("%+v", m)
	}
}

func TestImportWithChecksNeedsARepository(t *testing.T) {
	s := service(t)
	it := item("a")
	it.Spec.Checks = []string{"true"}
	res, err := s.Import(bg, []ImportItem{it}, false)
	if err != nil || res[0].Action != "failed" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestAgentIsWarnedAboutOutsideText(t *testing.T) {
	s := emptyQueue(t)
	if _, err := s.Import(bg, []ImportItem{item("a")}, false); err != nil {
		t.Fatal(err)
	}
	g, err := s.Wait(bg, engine.Agent{ID: "ag", Tier: 1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.Notice, "outside system") {
		t.Fatalf("notice: %q", g.Notice)
	}
}

func TestLargeImportAcrossChunks(t *testing.T) {
	s := service(t)
	var items []ImportItem
	for i := range importChunk*3 + 5 {
		id := fmt.Sprintf("t%d", i)
		if i == 0 {
			items = append(items, item(id))
		} else {
			items = append(items, item(id, "t0")) // Depends across chunk boundaries.
		}
	}
	res, err := s.Import(bg, items, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Action != "created" || r.Warning != "" {
			t.Fatalf("%+v", r)
		}
	}
}
