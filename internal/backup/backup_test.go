package backup

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/meta"
	"github.com/Grano22/aiculator/internal/store"
)

type fakeSource struct {
	mu    sync.Mutex
	fn    func(core.Task, uint64, store.Delivery)
	tasks []store.Stored
	acks  int
	naks  int
	meta  map[string]meta.Meta
	ready chan struct{}
}

func newFake(tasks ...core.Task) *fakeSource {
	f := &fakeSource{ready: make(chan struct{}), meta: map[string]meta.Meta{}}
	for _, t := range tasks {
		f.tasks = append(f.tasks, store.Stored{Task: t})
	}
	return f
}

func (f *fakeSource) FollowTasks(_ context.Context, _ string, _ []byte, fn func(core.Task, uint64, store.Delivery)) (func(), error) {
	f.mu.Lock()
	f.fn = fn
	f.mu.Unlock()
	close(f.ready)
	return func() {}, nil
}

func (f *fakeSource) LoadTasks(context.Context) ([]store.Stored, []store.Unreadable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Stored(nil), f.tasks...), nil, nil
}

func (f *fakeSource) GetMeta(id string) (meta.Meta, uint64, error) { return f.meta[id], 0, nil }

func (f *fakeSource) commit(t core.Task, seq uint64) {
	<-f.ready
	f.mu.Lock()
	fn := f.fn
	f.mu.Unlock()
	fn(t, seq, store.Delivery{
		Ack: func() { f.mu.Lock(); f.acks++; f.mu.Unlock() },
		Nak: func() { f.mu.Lock(); f.naks++; f.mu.Unlock() },
	})
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func task(id string, st core.State) core.Task {
	return core.Task{ID: id, Title: id, Tier: 1, State: st, Labels: map[string]string{}}
}

func TestParseRejectsWhatWouldBackUpNothing(t *testing.T) {
	for name, src := range map[string]string{
		"no rules":       "dir: x\n",
		"no match":       "rules:\n- name: a\n  journal: true\n",
		"no mode":        "rules:\n- name: a\n  match: {all: true}\n",
		"too often":      "rules:\n- name: a\n  every: 10s\n  match: {all: true}\n",
		"bad state":      "rules:\n- name: a\n  journal: true\n  match: {states: [finished]}\n",
		"bad glob":       "rules:\n- name: a\n  journal: true\n  match: {ids: ['[']}\n",
		"bad name":       "rules:\n- name: A B\n  journal: true\n  match: {all: true}\n",
		"twice":          "rules:\n- name: a\n  journal: true\n  match: {all: true}\n- name: a\n  journal: true\n  match: {all: true}\n",
		"misspelled key": "rules:\n- name: a\n  jurnal: true\n  match: {all: true}\n",
	} {
		if _, err := Parse([]byte(src), "backups.yaml", "/p"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c, err := Parse([]byte("rules:\n- name: crit\n  every: 1d\n  keep_for: 7d\n  match: {labels: {severity: critical}}\n"), "b.yaml", "/p")
	if err != nil {
		t.Fatal(err)
	}
	r := c.Rules[0]
	if r.Keep != DefaultKeep || time.Duration(r.Every) != 24*time.Hour || time.Duration(r.KeepFor) != 7*24*time.Hour {
		t.Errorf("defaults and durations: %+v", r)
	}
	if c.Abs() != filepath.Join("/p", DefaultDir) {
		t.Errorf("dir %s", c.Abs())
	}
}

func TestMatch(t *testing.T) {
	two := 2
	pay := task("pay-1", core.Ready)
	pay.Labels["severity"] = "critical-p0"
	pay.Tags = []string{"billing"}
	pay.Priority = 3
	other := task("docs-1", core.Done)
	for name, c := range map[string]struct {
		m        Match
		pay, doc bool
	}{
		"all":            {Match{All: true}, true, true},
		"id glob":        {Match{IDs: []string{"pay-*"}}, true, false},
		"label glob":     {Match{Labels: map[string]string{"severity": "critical*"}}, true, false},
		"label any":      {Match{Labels: map[string]string{"severity": "*"}}, true, false},
		"tag":            {Match{Tags: []string{"billing", "x"}}, true, false},
		"state":          {Match{States: []core.State{core.Done}}, false, true},
		"priority":       {Match{MinPriority: &two}, true, false},
		"and of fields":  {Match{IDs: []string{"pay-*"}, States: []core.State{core.Done}}, false, false},
		"or within list": {Match{IDs: []string{"docs-*", "pay-*"}}, true, true},
	} {
		if got := c.m.Matches(pay); got != c.pay {
			t.Errorf("%s: pay %v", name, got)
		}
		if got := c.m.Matches(other); got != c.doc {
			t.Errorf("%s: docs %v", name, got)
		}
	}
}

func start(t *testing.T, src *fakeSource, yaml string, now func() time.Time) (*Runner, string) {
	t.Helper()
	root := t.TempDir()
	cfg, err := Parse([]byte(yaml), "b.yaml", root)
	if err != nil {
		t.Fatal(err)
	}
	r := Start(context.Background(), cfg, src, quiet, Options{Now: now})
	t.Cleanup(r.Close)
	return r, cfg.Abs()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 400 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The journal keeps changes that matter, skips heartbeats and redeliveries, catches up
// on tasks that existed before it started, and keeps only as many copies as asked.
func TestJournal(t *testing.T) {
	old := task("pay-0", core.Ready)
	src := newFake(old, task("docs-0", core.Ready))
	src.meta["pay-1"] = meta.Meta{Props: map[string]meta.Prop{"k": {}}}
	r, dir := start(t, src, "rules:\n- name: crit\n  journal: true\n  meta: true\n  keep: 3\n  match: {ids: ['pay-*']}\n", nil)

	waitFor(t, "catch-up", func() bool { _, err := os.Stat(journalDir(dir, "crit", "pay-0")); return err == nil })
	if _, err := os.Stat(journalDir(dir, "crit", "docs-0")); err == nil {
		t.Error("a task the rule does not match was kept")
	}

	p := task("pay-1", core.Ready)
	src.commit(p, 10)
	hb := p
	hb.LeaseDeadline, hb.UpdatedAt = time.Now(), time.Now()
	src.commit(hb, 11) // Only deadlines moved.
	src.commit(task("docs-1", core.Ready), 12)
	for i, st := range []core.State{core.Parked, core.Ready, core.Parked, core.Ready} {
		n := p
		n.State = st
		n.Attempt = i
		src.commit(n, uint64(20+i))
	}
	vs, err := Versions(dir, "pay-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 {
		t.Fatalf("kept %d copies, want 3 (keep): %+v", len(vs), vs)
	}
	if vs[2].Seq != 23 || vs[0].Seq != 21 {
		t.Errorf("kept the wrong ones: %+v", vs)
	}
	e, err := vs[2].Entry("pay-1")
	if err != nil || e.Meta == nil || e.Task.Attempt != 3 {
		t.Errorf("newest copy: %+v %v", e, err)
	}
	st := r.Status()[0]
	if st.Kept != 6 || st.Skipped != 1 || st.Pruned != 2 {
		t.Errorf("status %+v", st)
	}
	if src.acks != 7 || src.naks != 0 {
		t.Errorf("acks %d naks %d", src.acks, src.naks)
	}

	// After a restart, a copy delivered again is recognised from disk.
	r.Close()
	src2 := newFake()
	cfg, _ := Parse([]byte("rules:\n- name: crit\n  journal: true\n  keep: 3\n  match: {ids: ['pay-*']}\n"), "b.yaml", filepath.Dir(filepath.Dir(dir)))
	r2 := Start(context.Background(), cfg, src2, quiet, Options{})
	defer r2.Close()
	n := p
	n.State, n.Attempt = core.Parked, 2
	src2.commit(n, 22)
	if s := r2.Status()[0]; s.Kept != 0 || s.Skipped != 1 {
		t.Errorf("redelivery after restart: %+v", s)
	}
}

func TestJournalNaksWhenItCannotWrite(t *testing.T) {
	src := newFake()
	_, dir := start(t, src, "rules:\n- name: crit\n  journal: true\n  match: {all: true}\n", nil)
	if err := os.MkdirAll(filepath.Join(dir, "crit"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A file where the journal directory should be.
	if err := os.WriteFile(filepath.Join(dir, "crit", "journal"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	src.commit(task("a", core.Ready), 1)
	if src.naks != 1 || src.acks != 0 {
		t.Errorf("acks %d naks %d", src.acks, src.naks)
	}
}

func TestSnapshots(t *testing.T) {
	src := newFake(task("pay-1", core.Ready), task("pay-2", core.Done), task("docs", core.Ready))
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	r, dir := start(t, src, "rules:\n- name: crit\n  every: 1h\n  keep: 2\n  match: {ids: ['pay-*']}\n", now)

	// No snapshot yet, so one is taken at once.
	waitFor(t, "first snapshot", func() bool { return len(Snapshots(dir, "crit")) == 1 })
	for i := range 3 {
		mu.Lock()
		clock = clock.Add(time.Duration(i+1) * time.Minute)
		mu.Unlock()
		if _, err := r.SnapshotNow(context.Background(), "crit"); err != nil {
			t.Fatal(err)
		}
	}
	snaps := Snapshots(dir, "crit")
	if len(snaps) != 2 {
		t.Fatalf("%d snapshots, want 2", len(snaps))
	}
	s, err := ReadSnapshot(snaps[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tasks) != 2 || s.Tasks[0].Task.ID != "pay-1" || s.Tasks[1].Task.ID != "pay-2" {
		t.Errorf("snapshot holds %+v", s.Tasks)
	}
	vs, _ := Versions(dir, "pay-2")
	if len(vs) != 2 || vs[1].Kind != "snapshot" || vs[1].State != core.Done {
		t.Errorf("versions %+v", vs)
	}
	if v, ok := Pick(vs, vs[0].At); !ok || v.Path != vs[0].Path {
		t.Errorf("pick before: %+v", v)
	}
	if _, ok := Pick(vs, vs[0].At.Add(-time.Second)); ok {
		t.Error("picked a copy newer than asked")
	}
	if _, err := r.SnapshotNow(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "no backup rule") {
		t.Errorf("unknown rule: %v", err)
	}
}

func TestPruneKeepsTheNewestEvenWhenOld(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	for _, n := range []string{"1.json", "2.json", "3.json"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
	}
	if n := prune(dir, ".json", 10, time.Hour, time.Now()); n != 2 {
		t.Errorf("removed %d", n)
	}
	if got := files(dir, ".json"); len(got) != 1 || got[0] != "3.json" {
		t.Errorf("left %v", got)
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"90m": 90 * time.Minute, "1.5d": 36 * time.Hour, "7d": 168 * time.Hour} {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	for _, in := range []string{"x", "-1d", "-5m", "d"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
}
