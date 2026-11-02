package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"aiculation/internal/swarm"
)

type project struct {
	t    *testing.T
	bin  string
	root string
	home string
}

func newProject(t *testing.T) *project {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "root"},
	} {
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	p := &project{t, buildBinary(t), root, home}
	t.Cleanup(func() { _, _ = p.tryIn(root, "down") })
	return p
}

func (p *project) tryIn(dir string, args ...string) (string, error) {
	c := exec.Command(p.bin, args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "HOME="+p.home)
	out, err := c.CombinedOutput()
	return string(out), err
}

func (p *project) run(args ...string) string {
	p.t.Helper()
	out, err := p.tryIn(p.root, args...)
	if err != nil {
		p.t.Fatalf("aiculator %v: %v\n%s", args, err, out)
	}
	return out
}

func (p *project) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(p.root, rel))
	return err == nil
}

func TestSkillInstallFromTheCLI(t *testing.T) {
	p := newProject(t)
	p.run("skill", "--install", "*", "--harness", "claude,opencode,pi")
	for _, rel := range []string{".agents/skills/swarm-join/SKILL.md", ".agents/skills/swarm-plan/SKILL.md", ".agents/skills/swarm-operate/SKILL.md", ".claude/skills/swarm-join/SKILL.md"} {
		if !p.exists(rel) {
			t.Errorf("%s is missing", rel)
		}
	}
	if p.exists(".opencode") || p.exists(".pi") {
		t.Error("made a directory for a harness that reads .agents/skills")
	}
	if out := p.run("skill", "--lint"); strings.Contains(out, "error") || !strings.Contains(out, "swarm-join") {
		t.Errorf("lint: %s", out)
	}
	if out := p.run("skill", "--list", "--harness", "claude,pi"); !strings.Contains(out, "claude,pi") || !strings.Contains(out, "worker") {
		t.Errorf("list: %s", out)
	}
}

// What the user literally types: ./bin/aiculator skill --install *
func TestUnquotedStarThatTheShellExpanded(t *testing.T) {
	p := newProject(t)
	for _, f := range []string{"README.md", "main.go", "docs"} {
		if err := os.WriteFile(filepath.Join(p.root, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command("sh", "-c", p.bin+" skill --install * --harness claude")
	c.Dir = p.root
	c.Env = append(os.Environ(), "HOME="+p.home)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !p.exists(".claude/skills/swarm-join/SKILL.md") {
		t.Fatal("the expanded star did not install everything")
	}
}

func TestGlobPatternsPathsAndGlobal(t *testing.T) {
	p := newProject(t)
	custom := filepath.Join(t.TempDir(), "vendor")
	p.run("skill", "--install", "*-plan", "--path", custom, "--harness", "none")
	if _, err := os.Stat(filepath.Join(custom, "swarm-plan/SKILL.md")); err != nil {
		t.Errorf("custom path: %v", err)
	}
	if p.exists(".agents/skills/swarm-join") {
		t.Error("the pattern selected too much")
	}
	p.run("skill", "--install", "swarm-?oin", "--global", "--harness", "claude")
	if _, err := os.Stat(filepath.Join(p.home, ".claude/skills/swarm-join/SKILL.md")); err != nil {
		t.Errorf("global: %v", err)
	}
	if out, err := p.tryIn(p.root, "skill", "--install", "nothing-*"); err == nil || !strings.Contains(out, "available") {
		t.Errorf("no match: %v %s", err, out)
	}
	p.run("skill", "--new", "my-tool", "--description", "Does a thing. Use when the user asks for that thing.")
	p.run("skill", "--install", "my-*", "--harness", "claude")
	if !p.exists(".claude/skills/my-tool/SKILL.md") {
		t.Error("a user skill was not linked")
	}
	p.run("skill", "--uninstall", "my-*", "--harness", "claude", "--purge")
	if p.exists(".claude/skills/my-tool") || p.exists(".agents/skills/my-tool") {
		t.Error("uninstall left files")
	}
}

func TestJoinWritesEverythingWithoutSecrets(t *testing.T) {
	p := newProject(t)
	out := p.run("join", "--harness", "claude,opencode,codex,gemini,cursor,pi,goose", "--tier", "2")
	if !strings.Contains(out, "swarm-join/SKILL.md") {
		t.Errorf("no instructions for an open session:\n%s", out)
	}
	for _, f := range []string{".mcp.json", "opencode.json", ".codex/config.toml", ".gemini/settings.json", ".cursor/mcp.json"} {
		b, err := os.ReadFile(filepath.Join(p.root, f))
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		s := string(b)
		if !strings.Contains(s, "--auto") || !strings.Contains(s, `"2"`) {
			t.Errorf("%s:\n%s", f, s)
		}
		tok, _ := os.ReadFile(filepath.Join(p.root, ".swarm/admin.token"))
		if strings.Contains(s, string(tok)) || strings.Contains(strings.ToLower(s), "bearer") {
			t.Errorf("%s holds a secret", f)
		}
	}
	// The data directory stays out of git without a tracked file changing.
	c := exec.Command("git", "status", "--porcelain")
	c.Dir = p.root
	st, _ := c.Output()
	if strings.Contains(string(st), ".swarm") {
		t.Errorf("data dir shows in git: %s", st)
	}
	// Joining twice changes nothing and does not break.
	p.run("join", "--harness", "claude,opencode,codex,gemini,cursor,pi,goose", "--tier", "2")
	p.run("unjoin", "--harness", "claude,opencode,codex,gemini,cursor,pi,goose")
	if p.exists(".mcp.json") || p.exists(".claude/skills/swarm-join") {
		t.Error("unjoin left things behind")
	}
}

// The route for a session already open, and for harnesses with no MCP: real commands,
// a real server, real checks and a real merge.
func TestShellRouteFullWorkerLoop(t *testing.T) {
	p := newProject(t)
	p.run("up")
	p.run("task", "add", "--id", "shell1", "--title", "x", "--check", "test -f done.txt")
	name := strings.TrimSpace(p.run("work", "join", "--harness", "pi", "--tier", "1"))
	if !strings.HasPrefix(name, "pi-") {
		t.Fatalf("name: %s", name)
	}
	var g swarm.Grant
	if err := json.Unmarshal([]byte(p.run("work", "wait", "--agent", name, "--hold", "5s")), &g); err != nil {
		t.Fatal(err)
	}
	if g.Action != "task" || g.Workdir == "" {
		t.Fatalf("grant: %+v", g)
	}
	lease := strconv.FormatUint(g.Lease, 10)
	// Commands must also work from inside the worktree, which is where the agent is.
	if err := os.WriteFile(filepath.Join(g.Workdir, "done.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := p.tryIn(g.Workdir, "work", "progress", "--agent", name, "--task", "shell1", "--lease", lease, "--note", "halfway"); err != nil {
		t.Fatalf("progress from the worktree: %v\n%s", err, out)
	}
	if out, err := p.tryIn(g.Workdir, "work", "situation", "--agent", name); err != nil || !strings.Contains(out, "counts") {
		t.Fatalf("situation: %v\n%s", err, out)
	}
	p.run("work", "submit", "--agent", name, "--task", "shell1", "--lease", lease)
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(p.run("task", "show", "shell1"), `"state": "done"`) {
		if time.Now().After(deadline) {
			t.Fatalf("not done:\n%s", p.run("task", "show", "shell1"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	c := exec.Command("git", "show", "swarm/integration:done.txt")
	c.Dir = p.root
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("not merged: %v %s", err, out)
	}
	// A stale lease is refused, and the agent file cannot be used to escape a path.
	if out, err := p.tryIn(p.root, "work", "submit", "--agent", name, "--task", "shell1", "--lease", lease); err == nil {
		t.Fatalf("stale submit accepted: %s", out)
	}
	if out, err := p.tryIn(p.root, "work", "wait", "--agent", "../../etc/x"); err == nil || !strings.Contains(out, "invalid agent") {
		t.Fatalf("path in agent name: %v %s", err, out)
	}
}

func mcpSession(t *testing.T, p *project, args ...string) *mcp.ClientSession {
	t.Helper()
	cmd := exec.Command(p.bin, args...)
	cmd.Dir = p.root
	cmd.Env = append(os.Environ(), "HOME="+p.home)
	cl := mcp.NewClient(&mcp.Implementation{Name: "fake-harness", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := cl.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !r.IsError {
		b, _ := json.Marshal(r.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// Exactly what a harness does with the written config: launch the bridge over stdio
// with no token in sight, then use the tools, including after the server restarts.
func TestMCPBridgeWithoutSecretsSurvivesARestart(t *testing.T) {
	p := newProject(t)
	p.run("up")
	s := mcpSession(t, p, "mcp", "--auto", "--tier", "1", "--harness", "fake")
	var names []string
	for tool, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	for _, want := range []string{"wait_for_task", "progress", "submit", "fail", "release", "review_verdict", "situation", "plan_begin"} {
		if !strings.Contains(strings.Join(names, " "), want) {
			t.Errorf("tool %s is missing: %v", want, names)
		}
	}
	p.run("task", "add", "--id", "m1", "--title", "x", "--check", "test -f m.txt")
	var g swarm.Grant
	callTool(t, s, "wait_for_task", map[string]any{"hold_seconds": 5}, &g)
	if g.Action != "task" || g.Task.ID != "m1" || !strings.HasPrefix(g.Task.Owner, "fake-") {
		t.Fatalf("grant: %+v", g)
	}
	if r := callTool(t, s, "plan_begin", nil, nil); !r.IsError {
		t.Error("a worker without --planner took the planner lease")
	}
	// The server restarts under an open session.
	p.run("down")
	p.run("up")
	if r := callTool(t, s, "heartbeat", map[string]any{"task_id": "m1", "lease": g.Lease}, nil); r.IsError {
		t.Fatalf("heartbeat after restart: %+v", r.Content)
	}
	if err := os.WriteFile(filepath.Join(g.Workdir, "m.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := callTool(t, s, "submit", map[string]any{"task_id": "m1", "lease": g.Lease}, nil); r.IsError {
		t.Fatalf("submit: %+v", r.Content)
	}
}

func TestPlannerJoinCanPlanOverMCP(t *testing.T) {
	p := newProject(t)
	p.run("up")
	planner := mcpSession(t, p, "mcp", "--auto", "--tier", "3", "--planner", "--harness", "plan")
	worker := mcpSession(t, p, "mcp", "--auto", "--tier", "1", "--harness", "work")

	var begun struct {
		Epoch uint64 `json:"epoch"`
	}
	callTool(t, planner, "plan_begin", map[string]any{"ttl_seconds": 60}, &begun)
	if begun.Epoch == 0 {
		t.Fatal("no epoch")
	}
	if r := callTool(t, planner, "plan_update", map[string]any{"epoch": begun.Epoch, "phase": "splitting", "expected_tasks": map[string]int{"1": 1}}, nil); r.IsError {
		t.Fatalf("update: %+v", r.Content)
	}
	var g swarm.Grant
	callTool(t, worker, "wait_for_task", map[string]any{"hold_seconds": 1}, &g)
	if g.Action != "wait" || g.Reason != "planning" || g.Planning.Phase != "splitting" {
		t.Fatalf("worker was not informed: %+v", g)
	}
	if r := callTool(t, planner, "plan_add", map[string]any{"epoch": begun.Epoch, "task": map[string]any{"id": "pl1", "title": "x", "tier": 1}}, nil); r.IsError {
		t.Fatalf("add: %+v", r.Content)
	}
	if r := callTool(t, planner, "plan_commit", map[string]any{"epoch": begun.Epoch}, nil); r.IsError {
		t.Fatalf("commit: %+v", r.Content)
	}
	callTool(t, worker, "wait_for_task", map[string]any{"hold_seconds": 5}, &g)
	if g.Action != "task" || g.Task.ID != "pl1" {
		t.Fatalf("after commit: %+v", g)
	}
}

func TestHarnessListShowsEveryone(t *testing.T) {
	p := newProject(t)
	out := p.run("harness")
	for _, id := range []string{"claude", "opencode", "pi", "codex", "gemini", "cursor", "copilot", "goose", "cline", "kiro"} {
		if !strings.Contains(out, id) {
			t.Errorf("%s is missing:\n%s", id, out)
		}
	}
}

func TestMCPAddAndPrintCommands(t *testing.T) {
	p := newProject(t)
	// A harness by id, then by the path to its executable.
	p.run("mcp", "add", "--harness", "opencode", "--tier", "2")
	if !strings.Contains(p.read("opencode.json"), `"servers"`) {
		t.Fatalf("opencode: %s", p.read("opencode.json"))
	}
	fake := filepath.Join(t.TempDir(), "cursor-agent")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := p.run("mcp", "add", "--harness", fake); !strings.Contains(out, "cursor") {
		t.Fatalf("binary path: %s", out)
	}
	if !p.exists(".cursor/mcp.json") {
		t.Fatal("found the harness but did not write its config")
	}
	// A path to a config file, with the format inferred from its name.
	custom := filepath.Join(t.TempDir(), "agent.toml")
	p.run("mcp", "add", "--harness", custom)
	b, _ := os.ReadFile(custom)
	if !strings.Contains(string(b), "[mcp_servers.swarm]") {
		t.Fatalf("toml: %s", b)
	}
	// A custom server, beside the swarm's.
	p.run("mcp", "add", "--harness", "opencode", "--name", "docs", "--", "docs-server", "--x")
	got := p.read("opencode.json")
	if !strings.Contains(got, "docs-server") || !strings.Contains(got, `"swarm"`) {
		t.Fatalf("custom: %s", got)
	}
	p.run("mcp", "remove", "--harness", "opencode", "--name", "docs")
	if strings.Contains(p.read("opencode.json"), "docs-server") {
		t.Fatal("remove failed")
	}

	before := p.read("opencode.json")
	if out := p.run("mcp", "add", "--harness", "opencode", "--tier", "3", "--dry-run"); !strings.Contains(out, "would write") {
		t.Fatalf("dry run: %s", out)
	}
	if p.read("opencode.json") != before {
		t.Fatal("dry run changed the file")
	}
	// Print changes nothing, shows the harness's own command, and never a token.
	out := p.run("mcp", "print", "--harness", "claude")
	if !strings.Contains(out, "mcpServers") || !strings.Contains(out, "claude mcp add") || strings.Contains(strings.ToLower(out), "bearer") {
		t.Fatalf("print: %s", out)
	}
	if p.exists(".mcp.json") {
		t.Fatal("print wrote a file")
	}
	if out := p.run("mcp", "print", "--harness", "pi"); !strings.Contains(out, "no confirmed config") {
		t.Fatalf("print for pi: %s", out)
	}
	if out, err := p.tryIn(p.root, "mcp", "add", "--harness", "nosuchthing"); err == nil {
		t.Fatalf("unknown harness accepted: %s", out)
	}
	// A custom server must not take the swarm's own name.
	if out, err := p.tryIn(p.root, "mcp", "add", "--harness", "opencode", "--", "evil"); err == nil {
		t.Fatalf("replaced the swarm's entry: %s", out)
	}
}

func (p *project) read(rel string) string {
	p.t.Helper()
	b, err := os.ReadFile(filepath.Join(p.root, rel))
	if err != nil {
		p.t.Fatal(err)
	}
	return string(b)
}
