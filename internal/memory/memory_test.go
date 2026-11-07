package memory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Grano22/aiculator/internal/store"
)

var bg = context.Background()

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(bg, store.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	write(t, dir, files)
	run("add", ".")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "first")
	return dir
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func commit(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	write(t, dir, files)
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "more"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestNotesAreSharedAndFoundAgain(t *testing.T) {
	dir := t.TempDir()
	st := openStore(t, dir)
	m, err := Start(bg, Config{Mode: ModeOff}, st)
	if err != nil {
		t.Fatal(err)
	}
	n1, created, err := m.Write("claude-1", "t1", NoteInput{Text: "Session tokens are checked in Authority.Verify; expiry comes from the exp claim.", Files: []string{"internal/auth/token.go"}, Symbols: []string{"Authority.Verify"}})
	if err != nil || !created {
		t.Fatal(n1, err)
	}
	if _, _, err := m.Write("codex", "", NoteInput{Text: "The web dashboard is plain JS with no build step; keep it that way.", Files: []string{"plugins/webdash/static/app.js"}, Tags: []string{"web"}}); err != nil {
		t.Fatal(err)
	}
	// Much the same note again updates the first one, so no agent reads it twice.
	n2, created, err := m.Write("claude-2", "t9", NoteInput{Text: "Session tokens are checked in Authority.Verify; expiry comes from the exp claim!", Files: []string{"internal/auth/cache.go"}})
	if err != nil || created || n2.ID != n1.ID || len(n2.Files) != 2 || n2.Author != "claude-2" {
		t.Fatalf("a repeat made a new note: %+v %v", n2, err)
	}
	got := m.Recall("where are tokens verified", nil, 5)
	if len(got) == 0 || got[0].ID != n1.ID {
		t.Fatalf("recall: %+v", got)
	}
	// Naming a file brings up the notes about it, even with few words in common.
	if got := m.Recall("conventions", []string{"plugins/webdash/static/app.js"}, 5); len(got) == 0 || !strings.Contains(got[0].Text, "dashboard") {
		t.Errorf("recall by file: %+v", got)
	}
	if got := m.Recall("kubernetes helm chart", nil, 5); len(got) != 0 {
		t.Errorf("unrelated recall: %+v", got)
	}
	if h := m.Hints("Fix token verification", "Expired session tokens are accepted", []string{"internal/auth/token.go"}); len(h) != 1 || h[0].ID != n1.ID {
		t.Errorf("hints: %+v", h)
	}
	if h := m.Hints("Add a CSV export", "", nil); len(h) != 0 {
		t.Errorf("a weak match came with a task: %+v", h)
	}
	for _, bad := range []NoteInput{{Text: "short"}, {Text: strings.Repeat("x", MaxNoteText+1)}, {Text: "a perfectly fine note text", Files: []string{"../outside"}}, {Text: "a perfectly fine note text", Files: []string{"/etc/passwd"}}} {
		if _, _, err := m.Write("a", "", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %+v: %v", bad, err)
		}
	}
	st0 := m.Status()
	if st0.Notes != 2 || st0.Stats.NotesAdded != 2 || st0.Stats.NotesMerged != 1 || st0.Stats.Recalls < 3 {
		t.Errorf("status %+v", st0)
	}
	m.Close()
	st.Close()

	// Notes outlive the server.
	st2 := openStore(t, dir)
	defer st2.Close()
	m2, err := Start(bg, Config{Mode: ModeOff}, st2)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if notes := m2.Notes(10); len(notes) != 2 {
		t.Fatalf("after restart: %+v", notes)
	}
	if err := m2.Forget(n1.ID); err != nil {
		t.Fatal(err)
	}
	if err := m2.Forget(n1.ID); err == nil {
		t.Error("forgot a note twice")
	}
	if _, err := m2.CodeQuery("anything", 0); err == nil {
		t.Error("a code query worked with memory off")
	}
}

func TestEvictionKeepsWhatIsUsed(t *testing.T) {
	b := newBook()
	now := time.Now()
	for i, uses := range []int{5, 0, 3, 9} {
		b.put(&Note{ID: string(rune('a' + i)), Text: "note", Uses: uses, Updated: now.Add(time.Duration(i) * time.Hour)})
	}
	if id := b.evict(); id != "b" {
		t.Errorf("evicted %s, want b: old and never used", id)
	}
}

func TestCodeGraphFollowsTheBranchAndMarksNotesStale(t *testing.T) {
	repo := gitRepo(t, map[string]string{
		"auth/token.go": "package auth\n\ntype Authority struct{}\n\nfunc (a *Authority) Verify(tok string) error { return nil }\n",
		"api/login.go":  "package api\n\nfunc Login() { var a Authority; a.Verify(\"x\") }\n",
	})
	st := openStore(t, t.TempDir())
	defer st.Close()
	m, err := Start(bg, Config{Repo: repo, Branch: "main", Dir: filepath.Join(t.TempDir(), "graph"), Mode: ModeBuiltin, Debounce: time.Millisecond}, st)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	deadline := time.Now().Add(20 * time.Second)
	for m.Status().Graph.Nodes == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	gs := m.Status().Graph
	if gs.Nodes == 0 || gs.Source != "builtin" || gs.Commit == "" {
		t.Fatalf("graph %+v", gs)
	}
	a, err := m.CodeQuery("where is a token verified", 400)
	if err != nil || !strings.Contains(a.Text, "Authority.Verify") || !strings.Contains(a.Text, "auth/token.go") {
		t.Fatalf("query: %v\n%s", err, a.Text)
	}
	if s, err := m.CodeSymbol("Authority", 0); err != nil || !strings.Contains(s.Text, "method_of Authority.Verify") {
		t.Errorf("symbol: %v\n%s", err, s.Text)
	}
	note, _, err := m.Write("a", "", NoteInput{Text: "Verify accepts any token for now; real checks come later.", Files: []string{"auth/token.go"}})
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := m.Write("a", "", NoteInput{Text: "Login is the only caller of Verify today.", Files: []string{"api/login.go"}})

	commit(t, repo, map[string]string{"auth/token.go": "package auth\n\ntype Authority struct{}\n\nfunc (a *Authority) Verify(tok string) error { return check(tok) }\n\nfunc check(tok string) error { return nil }\n"})
	gs, err = m.Refresh(bg)
	if err != nil || gs.Commit == "" {
		t.Fatalf("refresh: %+v %v", gs, err)
	}
	if a, _ := m.CodeQuery("check", 300); !strings.Contains(a.Text, "check") {
		t.Errorf("the rebuilt graph lacks the new function:\n%s", a.Text)
	}
	stale := map[string]bool{}
	for _, n := range m.Notes(10) {
		stale[n.ID] = !n.StaleSince.IsZero()
	}
	if !stale[note.ID] || stale[other.ID] {
		t.Errorf("stale: %v (only the note about the changed file should be)", stale)
	}
	if got := m.Recall("verify token", []string{"auth/token.go"}, 5); len(got) == 0 || got[0].StaleFiles[0] != "auth/token.go" {
		t.Errorf("recall of a stale note: %+v", got)
	}
	// Writing it again makes it current.
	if n, _, _ := m.Write("b", "", NoteInput{Text: "Verify accepts any token for now; real checks come later.", Files: []string{"auth/token.go"}}); !n.StaleSince.IsZero() {
		t.Errorf("a rewritten note is still stale: %+v", n)
	}
	// Nothing new: no rebuild.
	before := m.Status().Graph.BuiltAt
	if _, err := m.Refresh(bg); err != nil || !m.Status().Graph.BuiltAt.Equal(before) {
		t.Errorf("rebuilt with nothing new: %v", err)
	}
}

// With Graphify installed, its graph is used; when it fails, the built-in one stands in.
func TestGraphifyIsUsedWhenThereAndFallsBack(t *testing.T) {
	repo := gitRepo(t, map[string]string{"main.go": "package main\n\nfunc Run() {}\n"})
	bin := t.TempDir()
	good := filepath.Join(bin, "graphify-good")
	if err := os.WriteFile(good, []byte(`#!/bin/sh
[ "$1" = update ] || exit 2
mkdir -p "$2/graphify-out"
cat > "$2/graphify-out/graph.json" <<'JSON'
{"nodes":[{"id":"run","label":"Run","file_type":"code","source_file":"main.go","source_location":"L3"},{"id":"doc","label":"Design notes","file_type":"document","source_file":"README.md"}],
 "links":[{"source":"doc","target":"run","relation":"describes","confidence":"INFERRED","source_file":"README.md"}]}
JSON
`), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(bin, "graphify-bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho 'no API key' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, bin, source string
		errs              bool
	}{{"good", good, "graphify", false}, {"bad", bad, "builtin", true}} {
		st := openStore(t, t.TempDir())
		m, err := Start(bg, Config{Repo: repo, Branch: "main", Dir: filepath.Join(t.TempDir(), "g"), Mode: ModeAuto, Graphify: c.bin, Debounce: time.Millisecond}, st)
		if err != nil {
			t.Fatal(err)
		}
		gs, err := m.Refresh(bg)
		if err != nil || gs.Source != c.source || (gs.Err != "") != c.errs {
			t.Errorf("%s: %+v %v", c.name, gs, err)
		}
		if a, err := m.CodeQuery("Run", 0); err != nil || !strings.Contains(a.Text, "Run") {
			t.Errorf("%s: query %v", c.name, err)
		}
		m.Close()
		st.Close()
	}
	// Asked for Graphify by name, a failure is an error, not a quiet fallback.
	st := openStore(t, t.TempDir())
	defer st.Close()
	m, err := Start(bg, Config{Repo: repo, Branch: "main", Dir: filepath.Join(t.TempDir(), "g"), Mode: ModeGraphify, Graphify: bad, Debounce: time.Hour}, st)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.Refresh(bg); err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Errorf("graphify mode: %v", err)
	}
	if _, err := Start(bg, Config{Mode: "magic"}, st); err == nil {
		t.Error("an unknown mode was accepted")
	}
}
