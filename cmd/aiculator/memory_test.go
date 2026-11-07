package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Shared memory from end to end: the server builds a code graph of the project, agents ask it
// and leave notes, and a later task comes with the notes that bear on it.
func TestSharedMemoryFromTheCLI(t *testing.T) {
	p := newProject(t)
	files := map[string]string{
		"auth/token.go": "package auth\n\n// Authority signs and checks session tokens.\ntype Authority struct{}\n\nfunc (a *Authority) Verify(tok string) error { return nil }\n",
		"api/login.go":  "package api\n\nimport \"example.com/shop/auth\"\n\nfunc Login(a *auth.Authority) error { return a.Verify(\"t\") }\n",
	}
	for name, body := range files {
		path := filepath.Join(p.root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "code"}} {
		c := exec.Command("git", args...)
		c.Dir = p.root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	p.run("up")
	var status string
	for range 200 {
		if status = p.run("memory", "status"); strings.Contains(status, "code graph: ") && strings.Contains(status, "nodes") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(status, "nodes") || !strings.Contains(status, "notes: 0") {
		t.Fatalf("memory status: %s", status)
	}
	if out := p.run("code", "query", "where are session tokens verified"); !strings.Contains(out, "Authority.Verify") || !strings.Contains(out, "auth/token.go") {
		t.Errorf("code query:\n%s", out)
	}
	if out := p.run("code", "symbol", "Login"); !strings.Contains(out, "api/login.go") {
		t.Errorf("code symbol:\n%s", out)
	}
	if _, err := p.tryIn(p.root, "code", "symbol", "NoSuchThing"); err == nil {
		t.Error("an unknown symbol was found")
	}

	name := strings.TrimSpace(p.run("work", "join", "--tier", "1"))
	if out := p.run("work", "code", "--agent", name, "--symbol", "Authority"); !strings.Contains(out, "method_of Authority.Verify") {
		t.Errorf("work code --symbol:\n%s", out)
	}
	out := p.run("work", "note", "--agent", name, "--file", "auth/token.go", "--symbol", "Authority.Verify",
		"Verify accepts every token for now; the expiry check is still to be written.")
	var written struct {
		Note    struct{ ID string } `json:"note"`
		Created bool                `json:"created"`
	}
	if err := json.Unmarshal([]byte(out), &written); err != nil || !written.Created {
		t.Fatalf("note: %v %s", err, out)
	}
	if out := p.run("work", "recall", "--agent", name, "token expiry"); !strings.Contains(out, written.Note.ID) {
		t.Errorf("recall: %s", out)
	}
	if out, err := p.tryIn(p.root, "work", "note", "--agent", name, "too short"); err == nil {
		t.Errorf("a too-short note was taken: %s", out)
	}

	// A task about the same thing comes with the note.
	p.run("task", "add", "--id", "expiry", "--title", "Check token expiry in Verify", "--touches", "auth/token.go", "--allow-empty")
	other := strings.TrimSpace(p.run("work", "join", "--tier", "1"))
	grant := p.run("work", "wait", "--agent", other, "--hold", "0s")
	if !strings.Contains(grant, `"memory"`) || !strings.Contains(grant, written.Note.ID) {
		t.Errorf("the grant has no memory:\n%s", grant)
	}

	if out := p.run("memory", "ls"); !strings.Contains(out, written.Note.ID) {
		t.Errorf("memory ls: %s", out)
	}
	if out := p.run("memory", "recall", "--file", "auth/token.go", "anything"); !strings.Contains(out, written.Note.ID) {
		t.Errorf("memory recall by file: %s", out)
	}
	if out := p.run("memory", "status", "--json"); !strings.Contains(out, `"notes": 1`) || !strings.Contains(out, `"code_queries"`) {
		t.Errorf("status json: %s", out)
	}
	if out := p.run("memory", "refresh"); !strings.Contains(out, `"nodes"`) {
		t.Errorf("refresh: %s", out)
	}
	p.run("memory", "forget", written.Note.ID)
	if out := p.run("memory", "ls"); strings.Contains(out, written.Note.ID) {
		t.Errorf("a forgotten note is listed: %s", out)
	}
}
