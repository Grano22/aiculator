package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/client"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/swarm"
	"github.com/Grano22/aiculator/internal/vlog"
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

func TestTemplateCommands(t *testing.T) {
	p := newProject(t)
	if out := p.run("template", "--list"); !strings.Contains(out, "agent-discipline") {
		t.Fatalf("list: %s", out)
	}
	if out := p.run("template", "--render", "agent-discipline", "--set", "retries=1"); !strings.Contains(out, "| 2 | 2s |") || strings.Contains(out, "| 3 |") {
		t.Fatalf("render: %s", out)
	}
	if p.exists(".agents") {
		t.Fatal("render wrote files")
	}
	p.run("template", "--new", "my-rules")
	if !p.exists(".agents/templates/my-rules.md.j2") {
		t.Fatal("new: no file")
	}
	// The starter's description holds a TODO, which is a warning and not an error.
	p.run("template", "--install", "my-*", "agent-*", "--harness", "claude")
	for _, rel := range []string{".claude/skills/my-rules/SKILL.md", ".claude/skills/agent-discipline/SKILL.md"} {
		if !p.exists(rel) {
			t.Errorf("%s is missing", rel)
		}
	}
	p.run("template", "--check")
	tpl := filepath.Join(p.root, ".agents/templates/my-rules.md.j2")
	b, _ := os.ReadFile(tpl)
	if err := os.WriteFile(tpl, append(b, []byte("\nchanged\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := p.tryIn(p.root, "template", "--check"); err == nil || !strings.Contains(out, "my-rules") {
		t.Fatalf("stale not caught: %v %s", err, out)
	}
	p.run("template", "--install", "my-rules", "--per-harness", "--harness", "opencode")
	if !p.exists(".opencode/skills/my-rules/SKILL.md") {
		t.Error("per-harness: no copy in the harness's own directory")
	}
	custom := filepath.Join(t.TempDir(), "out")
	p.run("template", "--install", "agent-*", "--per-harness", "--harness", "none", "--path", custom)
	if _, err := os.Stat(filepath.Join(custom, "agent-discipline/SKILL.md")); err != nil {
		t.Errorf("custom path: %v", err)
	}
}

func TestReferenceDocsAreCurrent(t *testing.T) {
	for file, want := range map[string]string{"cli.md": cliReference(), "harnesses.md": harnessReference()} {
		got, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", file))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("docs/reference/%s is out of date; run: go run ./cmd/aiculator gen-docs", file)
		}
	}
}

// The exact commands from docs/guides/planning.md.
func TestPlanCommandsFromTheDocs(t *testing.T) {
	p := newProject(t)
	p.run("up")
	if out := p.run("plan", "begin", "--ttl", "5m"); !strings.Contains(out, `"epoch": 1`) {
		t.Fatalf("begin: %s", out)
	}
	p.run("plan", "update", "--epoch", "1", "--ttl", "5m", "--phase", "reading code")
	p.run("plan", "update", "--epoch", "1", "--ttl", "5m", "--phase", "splitting", "--expected", "1=12", "--expected", "2=3")
	if out := p.run("situation"); !strings.Contains(out, "splitting") || !strings.Contains(out, `"12"`) && !strings.Contains(out, ": 12") {
		t.Fatalf("hints are not visible: %s", out)
	}
	p.run("plan", "add", "--epoch", "1", "--id", "api", "--title", "Add the API", "--tier", "2", "--check", "true")
	p.run("plan", "add", "--epoch", "1", "--id", "ui", "--title", "Add the UI", "--dep", "api", "--check", "true")
	if strings.Contains(p.run("task", "ls"), "ready") {
		t.Fatal("a draft became visible before the commit")
	}
	p.run("plan", "commit", "--epoch", "1")
	out := p.run("task", "ls")
	if !strings.Contains(out, "ready") || !strings.Contains(out, "draft") {
		t.Fatalf("after commit, api should be ready and ui should wait for it:\n%s", out)
	}
}

// The manifest format shown in docs/guides/workers.md.
func TestManifestFromTheDocs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "my-harness.yaml")
	body := "name: my-harness\ncommand: [my-harness, --acp]\npass_env: [MY_API_KEY]\nreal_home: true\nwrite_paths: [\"~/.my-harness\"]\ndenied_tools: [fetch]\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := loadManifest("", path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "my-harness" || len(m.Command) != 2 || m.PassEnv[0] != "MY_API_KEY" || !m.RealHome || m.WritePaths[0] != "~/.my-harness" || m.DeniedTools[0] != "fetch" {
		t.Fatalf("manifest: %+v", m)
	}
}

func TestWebDashboardAndViewerToken(t *testing.T) {
	p := newProject(t)
	p.run("up")
	if out := p.run("plugin"); !strings.Contains(out, "webdash") || !strings.Contains(out, "compiled in") {
		t.Fatalf("plugin list: %s", out)
	}
	link := strings.TrimSpace(p.run("web", "--no-open"))
	base, token, ok := strings.Cut(link, "/plugins/webdash/#token=")
	if !ok || token == "" {
		t.Fatalf("link: %s", link)
	}
	ctx := context.Background()
	anon, viewer := client.New(base, ""), client.New(base, token)
	var se *client.StatusError

	// The page itself holds no data, but the data needs the token.
	if err := anon.Get(ctx, "/plugins/webdash/app.js", nil); err != nil && !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("assets should be open: %v", err)
	}
	if err := anon.Get(ctx, "/v1/observe", nil); !errors.As(err, &se) || se.Code != 401 {
		t.Fatalf("data without a token: %v", err)
	}
	var snap struct {
		Ready  bool `json:"ready"`
		Probes []struct {
			Name string `json:"name"`
		} `json:"probes"`
	}
	if err := viewer.Get(ctx, "/v1/observe", &snap); err != nil || !snap.Ready || len(snap.Probes) < 4 {
		t.Fatalf("viewer: %v %+v", err, snap)
	}
	// Read-only means read-only: it cannot add tasks, mint tokens or take work.
	if err := viewer.Post(ctx, "/v1/tasks", engine.TaskSpec{Title: "x"}, nil); !errors.As(err, &se) || se.Code != 403 {
		t.Fatalf("viewer added a task: %v", err)
	}
	if err := viewer.Post(ctx, "/v1/tokens", api.TokenReq{Agent: "x", Tier: 1}, nil); !errors.As(err, &se) || se.Code != 403 {
		t.Fatalf("viewer minted a token: %v", err)
	}
	if err := viewer.Post(ctx, "/v1/agent/wait", api.WaitReq{}, nil); !errors.As(err, &se) || se.Code != 403 {
		t.Fatalf("viewer took work: %v", err)
	}
	if err := viewer.Post(ctx, "/v1/tasks/x/cancel", map[string]string{}, nil); !errors.As(err, &se) || se.Code != 403 {
		t.Fatalf("viewer canceled: %v", err)
	}
	var statuses []struct {
		Name    string `json:"name"`
		Running bool   `json:"running"`
	}
	if err := viewer.Get(ctx, "/v1/plugins", &statuses); err != nil || len(statuses) != 1 || !statuses[0].Running {
		t.Fatalf("statuses: %v %+v", err, statuses)
	}
	// Prometheus and readiness.
	req, _ := http.NewRequest(http.MethodGet, base+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "aiculator_probe_ok{probe=\"store\"} 1") {
		t.Fatalf("metrics: %s", body)
	}
	if err := anon.Get(ctx, "/v1/readyz", nil); err != nil {
		t.Fatalf("readiness should be open: %v", err)
	}
}

func TestTopPrintsAFrameAndJSON(t *testing.T) {
	p := newProject(t)
	p.run("up")
	p.run("task", "add", "--id", "seen", "--title", "A visible task", "--check", "true")
	name := strings.TrimSpace(p.run("work", "join", "--tier", "1"))
	p.run("work", "wait", "--agent", name, "--hold", "1s")
	out := p.run("top", "--once")
	for _, want := range []string{"READY", "claimed 1", "A visible task", "Latency", "Probes", name} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Error("escape codes written to a pipe")
	}
	if out := p.run("top", "--once", "--view", "agents"); !strings.Contains(out, name) {
		t.Errorf("agents view: %s", out)
	}
	var s struct {
		Agents []struct {
			ID string `json:"id"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(p.run("top", "--json")), &s); err != nil || len(s.Agents) != 1 {
		t.Fatalf("json: %v %+v", err, s)
	}
	if out, err := p.tryIn(p.root, "top", "--once", "--view", "nope"); err == nil {
		t.Fatalf("bad view accepted: %s", out)
	}
}

// aiculator events follows task changes live, and any program can read it.
func TestEventsCommandStreams(t *testing.T) {
	p := newProject(t)
	p.run("up")
	cmd := exec.Command(p.bin, "events")
	cmd.Dir = p.root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			if strings.Contains(sc.Text(), `"id":"ev1"`) {
				got <- sc.Text()
				return
			}
		}
	}()
	time.Sleep(300 * time.Millisecond) // Let it connect; the stream starts from now.
	p.run("task", "add", "--id", "ev1", "--title", "x", "--check", "true")
	select {
	case line := <-got:
		if !strings.Contains(line, `"state":`) {
			t.Fatalf("line: %s", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no event arrived")
	}
}

func TestExternalPluginOnPath(t *testing.T) {
	p := newProject(t)
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"external args=$* data=$AICULATOR_DATA\"\nexit 7\n"
	if err := os.WriteFile(filepath.Join(dir, "aiculator-greet"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// A program must not be able to replace a built-in.
	if err := os.WriteFile(filepath.Join(dir, "aiculator-task"), []byte("#!/bin/sh\necho HIJACKED\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		c := exec.Command(p.bin, args...)
		c.Dir = p.root
		c.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := c.CombinedOutput()
		return string(out), err
	}
	out, err := run("greet", "a", "b")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 || !strings.Contains(out, "external args=a b") || !strings.Contains(out, "data=") {
		t.Fatalf("external: %v %s", err, out)
	}
	if out, _ := run("task", "--help"); strings.Contains(out, "HIJACKED") {
		t.Fatal("a program on PATH replaced a built-in command")
	}
	if out, _ := run("plugin"); !strings.Contains(out, "greet") || !strings.Contains(out, "on PATH") {
		t.Fatalf("list: %s", out)
	}
	// A program in the project is not on PATH, so cloning a repository adds no command.
	if err := os.WriteFile(filepath.Join(p.root, "aiculator-evil"), []byte("#!/bin/sh\necho EVIL\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, _ := run("evil"); strings.Contains(out, "EVIL") {
		t.Fatal("ran a program from the project directory")
	}
}

func buildWithTags(t *testing.T, tags string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "aiculator")
	if out, err := exec.Command("go", "build", "-tags", tags, "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestBuildTagsChooseThePlugins(t *testing.T) {
	bare := buildWithTags(t, "noweb")
	out, err := exec.Command(bare, "plugin").CombinedOutput()
	if err != nil || strings.Contains(string(out), "webdash") {
		t.Fatalf("noweb still has the dashboard: %v %s", err, out)
	}
	if out, _ := exec.Command(bare, "web", "--no-open").CombinedOutput(); !strings.Contains(string(out), "unknown command") {
		t.Fatalf("noweb still has the command: %s", out)
	}
	// The rest of the binary must not depend on the plugin.
	if out, err := exec.Command(bare, "top", "--help").CombinedOutput(); err != nil {
		t.Fatalf("top without the plugin: %v %s", err, out)
	}

	withHello := buildWithTags(t, "example_hello")
	p := newProject(t)
	p.bin = withHello
	p.run("up")
	if out := p.run("plugin"); !strings.Contains(out, "hello") || !strings.Contains(out, "webdash") {
		t.Fatalf("plugin list: %s", out)
	}
	p.run("task", "add", "--id", "h1", "--title", "x", "--check", "true")
	if out := p.run("hello"); !strings.Contains(out, "0 tasks are done") {
		t.Fatalf("hello: %s", out)
	}
}

// runSplit returns stdout and stderr apart, because a script reading stdout must never
// see a log line, however high the verbosity.
func (p *project) runSplit(args ...string) (string, string, error) {
	c := exec.Command(p.bin, args...)
	c.Dir = p.root
	c.Env = append(os.Environ(), "HOME="+p.home)
	var out, errb strings.Builder
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	return out.String(), errb.String(), err
}

func TestVerbosityFlagForms(t *testing.T) {
	p := newProject(t)
	p.run("up")
	for _, form := range [][]string{{"-v=6"}, {"-v", "6"}, {"--v=6"}, {"--v", "6"}} {
		out, errOut, err := p.runSplit(append([]string{"task", "ls"}, form...)...)
		if err != nil || !strings.Contains(errOut, "GET") || !strings.Contains(errOut, "status=200") {
			t.Errorf("%v: %v\nstderr: %s", form, err, errOut)
		}
		if strings.Contains(out, "level=INFO") {
			t.Errorf("%v: logs reached stdout: %s", form, out)
		}
	}
	if _, errOut, _ := p.runSplit("task", "ls"); strings.Contains(errOut, "status=200") {
		t.Errorf("the default level should be quiet: %s", errOut)
	}
	// The version flag must still work, now that -v means verbosity.
	if out := p.run("--version"); !strings.Contains(out, "aiculator version") {
		t.Errorf("version: %s", out)
	}
	if out, err := p.tryIn(p.root, "task", "ls", "--v=10"); err == nil {
		t.Errorf("level 10 accepted: %s", out)
	}
	c := exec.Command(p.bin, "task", "ls")
	c.Dir = p.root
	c.Env = append(os.Environ(), "AICULATOR_V=6")
	if out, _ := c.CombinedOutput(); !strings.Contains(string(out), "status=200") {
		t.Errorf("AICULATOR_V: %s", out)
	}
}

func TestClientLevelsShowGrowingDetail(t *testing.T) {
	p := newProject(t)
	p.run("up")
	args := []string{"task", "add", "--id", "v1", "--title", "Visible title", "--check", "true"}
	_, six, _ := p.runSplit(append(args, "-v=6")...)
	if !strings.Contains(six, "POST") || strings.Contains(six, "request headers") || strings.Contains(six, "Visible title") {
		t.Errorf("level 6: %s", six)
	}
	args[2] = "v2"
	_, seven, _ := p.runSplit(append(args, "-v=7")...)
	if !strings.Contains(seven, "request headers") || strings.Contains(seven, "Visible title") {
		t.Errorf("level 7: %s", seven)
	}
	args[2] = "v3"
	_, eight, _ := p.runSplit(append(args, "-v=8")...)
	if !strings.Contains(eight, "Visible title") || !strings.Contains(eight, "response body") {
		t.Errorf("level 8: %s", eight)
	}
}

// Asking for a token at the highest level, on both sides: the token must be printed for
// the caller on stdout, and never appear in any log.
func TestNoTokenInAnyLogAtTopVerbosity(t *testing.T) {
	p := newProject(t)
	if out, err := p.tryIn(p.root, "up", "--v=9"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	stdout, stderr, err := p.runSplit("token", "--agent", "a1", "--tier", "1", "-v=9")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(stdout)
	if !strings.Contains(token, ".") || len(token) < 40 {
		t.Fatalf("stdout should be just the token: %q", stdout)
	}
	_, sig, _ := strings.Cut(token, ".")
	admin := strings.TrimSpace(p.read(".swarm/admin.token"))
	_, adminSig, _ := strings.Cut(admin, ".")
	// Use the token for real, over HTTP and MCP, so all paths have logged something.
	joined, joinLog, err := p.runSplit("work", "join", "--tier", "1", "-v=9")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimSpace(joined) // Stdout stays clean for scripts, even at level 9.
	stderr += joinLog
	p.run("task", "add", "--id", "t1", "--title", "x", "--check", "true", "-v=9")
	p.run("work", "wait", "--agent", name, "--hold", "1s", "-v=9")
	s := mcpSession(t, p, "mcp", "--auto", "--tier", "1", "--harness", "fake", "-v=9")
	callTool(t, s, "situation", nil, nil)
	time.Sleep(200 * time.Millisecond)

	server := p.read(".swarm/server.log")
	if !strings.Contains(server, "request body") || !strings.Contains(server, "/v1/tokens") {
		t.Fatalf("the server was not logging at level 9:\n%.500s", server)
	}
	for where, text := range map[string]string{"server": server, "client": stderr} {
		for label, secret := range map[string]string{"agent token": sig, "admin token": adminSig} {
			if strings.Contains(text, secret) {
				t.Errorf("%s log holds the %s", where, label)
			}
		}
		if !strings.Contains(text, vlog.Redacted) {
			t.Errorf("%s log shows no sign of redaction", where)
		}
	}
	if key := p.read(".swarm/signing.key"); strings.Contains(server, key) {
		t.Error("the signing key is in the log")
	}
}

func TestChangeVerbosityOnARunningServer(t *testing.T) {
	p := newProject(t)
	p.run("up")
	if out := p.run("debug", "verbosity"); !strings.Contains(out, "level 2") {
		t.Fatalf("a server should default to 2: %s", out)
	}
	p.run("task", "add", "--id", "before", "--title", "x", "--check", "true")
	if strings.Contains(p.read(".swarm/server.log"), `msg=commit`) {
		t.Fatal("level 2 should not log every commit")
	}
	if out := p.run("debug", "verbosity", "3", "--for", "1s"); !strings.Contains(out, "level 3") || !strings.Contains(out, "until") {
		t.Fatalf("set: %s", out)
	}
	p.run("task", "add", "--id", "during", "--title", "x", "--check", "true")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(p.read(".swarm/server.log"), `msg=commit`) {
		if time.Now().After(deadline) {
			t.Fatal("the new level did not take effect")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(p.read(".swarm/server.log"), "verbosity changed") {
		t.Error("the change itself should be logged")
	}
	time.Sleep(1200 * time.Millisecond)
	if out := p.run("debug", "verbosity"); !strings.Contains(out, "level 2") || strings.Contains(out, "until") {
		t.Fatalf("it should have gone back: %s", out)
	}
	if out := p.run("debug", "verbosity", "--vmodule", "engine=5"); !strings.Contains(out, "engine=5") {
		t.Fatalf("vmodule: %s", out)
	}
	if out, err := p.tryIn(p.root, "debug", "verbosity", "12"); err == nil {
		t.Fatalf("level 12 accepted: %s", out)
	}
	// Only an admin may change it; a viewer token can read dashboards, and no more.
	link := strings.TrimSpace(p.run("web", "--no-open"))
	base, token, _ := strings.Cut(link, "/plugins/webdash/#token=")
	var se *client.StatusError
	if err := client.New(base, token).Do(context.Background(), http.MethodPut, "/v1/debug/verbosity", api.VerbosityReq{}, nil); !errors.As(err, &se) || se.Code != 403 {
		t.Fatalf("a viewer changed the level: %v", err)
	}
	if out := p.run("debug", "levels"); !strings.Contains(out, "Trace") || !strings.Contains(out, "engine") {
		t.Fatalf("levels: %s", out)
	}
}

func TestServerStepsThroughLevels(t *testing.T) {
	p := newProject(t)
	p.run("up", "--v=5")
	p.run("task", "add", "--id", "t", "--title", "x", "--check", "test -f f")
	name := strings.TrimSpace(p.run("work", "join", "--tier", "1"))
	var g swarm.Grant
	if err := json.Unmarshal([]byte(p.run("work", "wait", "--agent", name, "--hold", "1s")), &g); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.Workdir, "f"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p.run("work", "submit", "--agent", name, "--task", "t", "--lease", strconv.FormatUint(g.Lease, 10))
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(p.read(".swarm/server.log"), `msg=merged`) {
		if time.Now().After(deadline) {
			t.Fatalf("never merged:\n%s", p.read(".swarm/server.log"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	log := p.read(".swarm/server.log")
	for _, want := range []string{"task created", "task transition", "msg=grant", "msg=commit", "compare-and-swap", "msg=git", "running check", "check output", "checks finished", "worktree ready", "component=engine", "component=work"} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q at level 5", want)
		}
	}
	if out := p.run("logs", "--tail", "2", "--component", "engine"); strings.Count(out, "\n") != 2 || strings.Contains(out, "component=work") {
		t.Errorf("logs --tail: %s", out)
	}
	if out := p.run("logs", "--grep", "compare-and-swap"); !strings.Contains(out, "from_rev") {
		t.Errorf("logs --grep: %s", out)
	}
}

func TestJSONLogsAreValid(t *testing.T) {
	p := newProject(t)
	p.run("up", "--log-format", "json", "--v=3")
	p.run("task", "add", "--id", "t", "--title", "x", "--check", "true")
	time.Sleep(100 * time.Millisecond)
	lines := strings.Split(strings.TrimSpace(p.read(".swarm/server.log")), "\n")
	if len(lines) < 3 {
		t.Fatalf("too few lines: %v", lines)
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
	}
}

func TestLogFileFlag(t *testing.T) {
	p := newProject(t)
	p.run("up")
	path := filepath.Join(t.TempDir(), "out.log")
	_, errOut, err := p.runSplit("task", "ls", "-v=6", "--log-file", path)
	if err != nil || strings.Contains(errOut, "status=200") {
		t.Fatalf("%v %s", err, errOut)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "status=200") {
		t.Fatalf("file: %s", b)
	}
}

// Examples in the docs are captured from real runs, so a token could be pasted in by accident.
func TestDocsHoldNoTokens(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	for _, pattern := range []string{"README.md", "CHANGELOG.md", "docs/*.md", "docs/*/*.md"} {
		m, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) < 15 {
		t.Fatalf("found only %d files", len(files))
	}
	shape := regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{16,}`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := shape.FindString(string(b)); m != "" {
			t.Errorf("%s holds a token: %.20s...", f, m)
		}
	}
}

// The docs include the demo's rendered form from these files, so they must stay current.
func TestDemoSnippetsAreCurrent(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, id := range demoHarnesses {
		want, err := renderDemo(root, id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(root, "docs", "snippets", demoSnippetName(id)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s is out of date; run: go run ./cmd/aiculator gen-docs", demoSnippetName(id))
		}
	}
}

// What the docs tell people to run, in a project with its own copy of the demo.
func TestDemoTemplateInstalls(t *testing.T) {
	p := newProject(t)
	src := filepath.Join("..", "..", ".agent", "template")
	if err := os.CopyFS(filepath.Join(p.root, ".agent", "template"), os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	if out := p.run("template", "--list"); !strings.Contains(out, "demo") {
		t.Fatalf("list: %s", out)
	}
	if out := p.run("template", "--render", "demo", "--for", "claude", "--set", "retries=1"); !strings.Contains(out, "| 2 | 2s |") || strings.Contains(out, "| 3 |") {
		t.Fatalf("render: %s", out)
	}
	p.run("template", "--install", "demo", "--harness", "claude")
	if !p.exists(".claude/skills/demo/SKILL.md") {
		t.Fatal("the demo was not linked")
	}
	p.run("template", "--check")
	if out := p.run("skill", "--lint"); strings.Contains(out, "demo: error") || strings.Contains(out, "demo: warn") {
		t.Fatalf("lint: %s", out)
	}
	p.run("template", "--install", "demo", "--per-harness", "--harness", "opencode")
	if !strings.Contains(p.read(".opencode/skills/demo/SKILL.md"), "`question`") {
		t.Fatal("the OpenCode copy does not name its own tool")
	}
}

// A snippet holding a fence as long as the page's own would close it early, and the rest
// of the snippet would become live page content. A strict docs build does not notice.
func TestDemoPageFencesOutlastItsSnippets(t *testing.T) {
	page, err := os.ReadFile("../../docs/guides/template-demo.md")
	if err != nil {
		t.Fatal(err)
	}
	fence := regexp.MustCompile("(?m)^ *(`{3,})")
	longest := func(text string) int {
		n := 0
		for _, m := range fence.FindAllStringSubmatch(text, -1) {
			n = max(n, len(m[1]))
		}
		return n
	}
	files, _ := filepath.Glob("../../docs/snippets/demo.*.md")
	if len(files) == 0 {
		t.Fatal("no snippets found")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := longest(string(page)), longest(string(b)); got <= want {
			t.Errorf("%s has a fence of %d, and the page's longest is %d", filepath.Base(f), want, got)
		}
	}
}

// Anything run after down, such as a new up or removing .swarm, must not meet a server
// that is still writing. The HTTP endpoint closes before the store has finished.
func TestDownReturnsOnlyAfterTheServerExits(t *testing.T) {
	p := newProject(t)
	p.run("up")
	p.run("task", "add", "--id", "d1", "--title", "x", "--check", "true")
	b, err := os.ReadFile(filepath.Join(p.root, ".swarm", "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	p.run("down")
	if processAlive(pid) {
		t.Fatalf("down returned while server %d was still running", pid)
	}
}

func TestSecondServerOnTheSameDataIsRefused(t *testing.T) {
	p := newProject(t)
	p.run("up")
	data := filepath.Join(p.root, ".swarm")
	before, err := os.ReadFile(filepath.Join(data, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	addr, err := os.ReadFile(filepath.Join(data, "addr"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.bin, "serve", "--data", data, "--addr", "127.0.0.1:0", "--repo", p.root)
	cmd.Dir = p.root
	cmd.Env = append(os.Environ(), "HOME="+p.home)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "another server is already using") {
		t.Fatalf("second server: %v\n%s", err, out)
	}
	// It must leave the running server's files alone, so that down can still find it.
	for name, want := range map[string][]byte{"pid": before, "addr": addr} {
		got, err := os.ReadFile(filepath.Join(data, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s changed: %q %v", name, got, err)
		}
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(before)))
	if err != nil {
		t.Fatal(err)
	}
	p.run("down")
	if processAlive(pid) {
		t.Fatal("down did not stop the first server")
	}
	// The lock goes with the server.
	p.run("up")
}

// The server can die while down waits, without removing its pid file.
func TestDownSucceedsWhenTheServerDiesWhileWaiting(t *testing.T) {
	p := newProject(t)
	p.run("up")
	b, err := os.ReadFile(filepath.Join(p.root, ".swarm", "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(50*time.Millisecond, func() { _ = proc.Kill() })
	start := time.Now()
	if out, err := p.tryIn(p.root, "down"); err != nil {
		t.Fatalf("down failed: %v\n%s", err, out)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("down took %s", took)
	}
	// A killed server releases its lock, so the project can start again.
	p.run("up")
}

func TestLabelsAgingAndAmendFromTheCLI(t *testing.T) {
	p := newProject(t)
	p.run("up")
	p.run("task", "add", "--id", "l1", "--title", "old", "--label", "team=core", "--label", "drop=me", "--aging", "1h=5,30m=2")
	out := p.run("task", "show", "l1")
	for _, want := range []string{`"team": "core"`, `"after_sec": 1800`, `"boost": 5`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	p.run("task", "amend", "l1", "--title", "new", "--label", "drop=", "--priority", "7")
	out = p.run("task", "show", "l1")
	if !strings.Contains(out, `"title": "new"`) || strings.Contains(out, "drop") || !strings.Contains(out, `"priority": 7`) {
		t.Errorf("amend: %s", out)
	}
	if out := p.run("task", "priority", "l1"); !strings.Contains(out, `"effective": 7`) {
		t.Errorf("priority: %s", out)
	}
	if out, err := p.tryIn(p.root, "task", "add", "--title", "x", "--aging", "soon=5"); err == nil {
		t.Errorf("a bad aging step was accepted: %s", out)
	}
}

func TestPropsBlobsAndResourcesFromTheCLI(t *testing.T) {
	p := newProject(t)
	p.run("up")
	p.run("task", "add", "--id", "m1", "--title", "x")
	p.run("task", "prop", "m1", "points=5", "team=core", `tags=["a","b"]`, "gone=1")
	p.run("task", "prop", "m1", "gone=")
	file := filepath.Join(p.root, "design.txt")
	if err := os.WriteFile(file, []byte("the design"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.run("task", "attach", "m1", file)
	p.run("task", "resource", "m1", "spec-page", "https://example.com/spec", "--kind", "jira.attachment")
	out := p.run("task", "meta", "m1")
	for _, want := range []string{`"points"`, `"value": 5`, `"design.txt"`, `"sha256"`, `"kind": "jira.attachment"`, `"url": "https://example.com/spec"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, "gone") {
		t.Errorf("a removed property is still there: %s", out)
	}
	saved := filepath.Join(p.root, "out.txt")
	p.run("task", "fetch", "m1", "design.txt", "-o", saved)
	if b, err := os.ReadFile(saved); err != nil || string(b) != "the design" {
		t.Fatalf("%q %v", b, err)
	}
	// The task record itself stays small, whatever is attached.
	if show := p.run("task", "show", "m1"); strings.Contains(show, "design") || strings.Contains(show, "points") {
		t.Errorf("metadata leaked into the task record: %s", show)
	}
	// A link from outside must not reach the server's own network.
	p.run("task", "resource", "m1", "inner", "http://127.0.0.1:1/secret")
	if out, err := p.tryIn(p.root, "task", "fetch", "m1", "inner"); err == nil || !strings.Contains(out, "not reachable") {
		t.Errorf("fetch of a private address: %v %s", err, out)
	}
	if out, err := p.tryIn(p.root, "task", "resource", "m1", "f", "file:///etc/passwd"); err == nil {
		t.Errorf("a file URL was accepted: %s", out)
	}
}

func TestImportFromASpreadsheet(t *testing.T) {
	p := newProject(t)
	p.run("up")
	src, err := os.ReadFile("../../internal/connect/testdata/eisenhower.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.root, "plan.xlsx"), src, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p.root, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `
sources:
  - name: plan
    kind: xlsx
    path: plan.xlsx
    sheet: Tasks
    fields:
      key: ID
      title: Task
      spec: Notes
      priority:
        rules:
          - when: {Urgent: "yes", Important: "yes"}
            value: 100
        default: 1
      props:
        due: Due
`
	if err := os.WriteFile(filepath.Join(p.root, ".agents/sources.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out := p.run("import", "plan", "--dry-run")
	if !strings.Contains(out, "plan-E-1") || !strings.Contains(out, "Nothing was changed") {
		t.Errorf("dry run: %s", out)
	}
	if ls := p.run("task", "ls"); strings.Contains(ls, "plan-E-1") {
		t.Errorf("a dry run created tasks: %s", ls)
	}
	out = p.run("import", "plan")
	if !strings.Contains(out, "5 created") {
		t.Errorf("import: %s", out)
	}
	show := p.run("task", "show", "plan-E-1")
	for _, want := range []string{`"priority": 100`, `"source": "plan"`, `"external": "E-1"`, "Customers are affected"} {
		if !strings.Contains(show, want) {
			t.Errorf("missing %s in %s", want, show)
		}
	}
	if m := p.run("task", "meta", "plan-E-1"); !strings.Contains(m, "2026-11-10") {
		t.Errorf("meta: %s", m)
	}
	if out := p.run("import", "plan"); !strings.Contains(out, "0 created") || !strings.Contains(out, "5 unchanged") {
		t.Errorf("a second import should change nothing: %s", out)
	}
	if out := p.run("source", "ls"); !strings.Contains(out, "plan") || !strings.Contains(out, "import only") {
		t.Errorf("source ls: %s", out)
	}
	if out := p.run("source", "check", "plan"); !strings.Contains(out, "reachable") {
		t.Errorf("source check: %s", out)
	}
	if out, err := p.tryIn(p.root, "import", "nope"); err == nil || !strings.Contains(out, "plan") {
		t.Errorf("an unknown source should list the real ones: %v %s", err, out)
	}
}
