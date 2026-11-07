package codegraph

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestReadsGraphifyOutput(t *testing.T) {
	g, err := Load("testdata/graphify.json")
	if err != nil {
		t.Fatal(err)
	}
	if st := g.Stats(); st.Nodes != 4 || st.Edges != 3 || st.Files != 4 || st.Source != "graphify" {
		t.Errorf("stats %+v (an edge to a missing node must be dropped)", st)
	}
	a := g.Query("how is a token validated at login", 0)
	if !strings.Contains(a.Text, "* validate_token") || !strings.Contains(a.Text, "auth/tokens.py:12") || !strings.Contains(a.Text, "<- calls login_handler") {
		t.Errorf("query:\n%s", a.Text)
	}
	if !strings.Contains(a.Text, "uses SessionStore (auth/session.py)  ?") {
		t.Errorf("an inferred edge is not marked:\n%s", a.Text)
	}
	s, err := g.Symbol("login_handler", 0)
	if err != nil || !strings.Contains(s.Text, "api/login.py:40") {
		t.Errorf("symbol: %v\n%s", err, s.Text)
	}
	if _, err := g.Symbol("nothing_like_this", 0); err == nil {
		t.Error("an unknown symbol was found")
	}
	if a := g.Query("zzz qqq", 0); len(a.Nodes) != 0 || !strings.Contains(a.Text, "Nothing in the code graph") {
		t.Errorf("no match: %+v", a)
	}
}

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	return dir
}

var sample = map[string]string{
	"auth/token.go": `package auth

import (
	"errors"
	"time"
)

// ErrExpired means the token is too old.
var ErrExpired = errors.New("expired")

type Authority struct{ ttl time.Duration }

func (a *Authority) VerifyToken(tok string) error { return nil }

func NewAuthority() *Authority { return &Authority{} }
`,
	"auth/cache.go": "package auth\n\nfunc (a *Authority) remember(tok string) {}\n",
	"api/server.go": "package api\n\nimport \"example.com/x/auth\"\n\nfunc Login(a *auth.Authority) error { return a.VerifyToken(\"t\") }\n",
	"web/app.ts":    "import { api } from './client'\nexport async function renderDashboard() {}\nexport class SessionView {}\n",
	"jobs/run.py":   "from auth.tokens import check\n\nclass Runner:\n    def run_job(self):\n        pass\n",
	"docs/auth.md":  "# Authentication\n\n## Token lifetime\n",
	"ignored.bin":   "\x00\x01",
}

func TestBuildIndexesSeveralLanguages(t *testing.T) {
	dir := repo(t, sample)
	g, err := Build(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]Node{}
	for _, n := range g.Nodes {
		labels[n.Label] = n
	}
	for _, want := range []string{"Authority", "Authority.VerifyToken", "Authority.remember", "NewAuthority", "ErrExpired", "Login",
		"renderDashboard", "SessionView", "Runner", "run_job", "Token lifetime", "example.com/x/auth", "./client"} {
		if _, ok := labels[want]; !ok {
			t.Errorf("no node %q", want)
		}
	}
	if n := labels["Authority.VerifyToken"]; n.File != "auth/token.go" || n.Location != "L13" || n.Kind != "method" {
		t.Errorf("method node %+v", n)
	}
	edge := func(from, rel, to string) bool {
		for _, e := range g.Edges {
			if g.Nodes[g.byID[e.Source]].Label == from && e.Relation == rel && g.Nodes[g.byID[e.Target]].Label == to {
				return true
			}
		}
		return false
	}
	for _, c := range [][3]string{
		{"auth/token.go", "defines", "Authority"},
		{"Authority.remember", "method_of", "Authority"}, // From another file of the package.
		{"api/server.go", "imports", "example.com/x/auth"},
		{"api/server.go", "uses", "Authority"},
		{"auth/token.go", "imports", "time"},
	} {
		if !edge(c[0], c[1], c[2]) {
			t.Errorf("no edge %s -%s-> %s", c[0], c[1], c[2])
		}
	}
	a := g.Query("where is the token verified", 400)
	if !strings.Contains(a.Text, "Authority.VerifyToken") {
		t.Errorf("query:\n%s", a.Text)
	}
	// What one agent built, another reads back unchanged, in Graphify's format.
	out := filepath.Join(t.TempDir(), "graph.json")
	if err := g.Write(out); err != nil {
		t.Fatal(err)
	}
	back, err := Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Stats().Nodes != g.Stats().Nodes || back.Stats().Edges != g.Stats().Edges {
		t.Errorf("round trip: %+v vs %+v", back.Stats(), g.Stats())
	}
}

func TestAnswersStayWithinTheBudget(t *testing.T) {
	files := map[string]string{}
	for i := range 200 {
		files["pkg/f"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+".go"] = "package pkg\n\nfunc HandleRequest" + string(rune('A'+i%26)) + "() {}\n"
	}
	g, err := Build(context.Background(), repo(t, files))
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{60, 100, 300, 1000} {
		a := g.Query("handle request", budget)
		if a.Tokens > budget+20 {
			t.Errorf("budget %d: %d tokens", budget, a.Tokens)
		}
		if budget == 60 && !a.Truncated {
			t.Errorf("budget %d: not cut, %d tokens", budget, a.Tokens)
		}
	}
}

func TestTermsAndStems(t *testing.T) {
	got := strings.Join(Terms("ValidateToken validate_tokens HTTPServer the verified"), " ")
	if got != "valid token valid token http serve verif" {
		t.Errorf("terms %q", got)
	}
	for a, b := range map[string]string{"tokens": "token", "verified": "verify", "fenced": "fence", "leases": "lease"} {
		if stem(a) != stem(b) {
			t.Errorf("%s and %s do not meet: %s %s", a, b, stem(a), stem(b))
		}
	}
}

// Every agent asks the same graph, so it must answer many at once.
func TestConcurrentQueries(t *testing.T) {
	g, err := Load("testdata/graphify.json")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := []string{"token", "session", "login"}[i%3]
			if a := g.Query(q, 200); len(a.Nodes) == 0 {
				t.Errorf("%s: nothing", q)
			}
		}()
	}
	wg.Wait()
}
