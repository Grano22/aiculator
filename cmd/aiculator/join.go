package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/client"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/harness"
	"github.com/Grano22/aiculator/internal/skillpack"
	"github.com/Grano22/aiculator/internal/swarm"
)

// projectRoot is the git top level, or the working directory outside a repository.
func projectRoot() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err == nil {
		// Inside a task's worktree this is the worktree. The project owns the data dir.
		root := strings.TrimSpace(string(out))
		if !strings.Contains(root, string(filepath.Separator)+".swarm"+string(filepath.Separator)) {
			return root
		}
		if abs, err := filepath.Abs(dataDir); err == nil {
			return filepath.Dir(abs)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func healthy(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return client.New(url, "").Get(ctx, "/v1/healthz", nil) == nil
}

func serverURL() (string, bool) {
	b, err := os.ReadFile(filepath.Join(dataDir, "addr"))
	if err != nil {
		return "", false
	}
	url := strings.TrimSpace(string(b))
	return url, healthy(url)
}

// excludeData keeps the data directory out of git without changing a tracked file.
func excludeData(root string) {
	cmd := exec.Command("git", "rev-parse", "--git-path", "info/exclude")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	b, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == ".swarm/" {
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(b) > 0 && b[len(b)-1] != '\n' {
		_, _ = f.WriteString("\n")
	}
	_, _ = f.WriteString(".swarm/\n")
}

// startServer launches the server in the background unless one is already running.
// It reuses the previous address when it can, so open sessions can reconnect.
func startServer(sb string) (string, error) {
	if url, ok := serverURL(); ok {
		return url, nil
	}
	root := projectRoot()
	if dataDir == ".swarm" {
		dataDir = filepath.Join(root, ".swarm")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", err
	}
	excludeData(root)
	addr := "127.0.0.1:0"
	if b, err := os.ReadFile(filepath.Join(abs, "addr")); err == nil {
		addr = strings.TrimPrefix(strings.TrimSpace(string(b)), "http://")
	}
	_ = os.Remove(filepath.Join(abs, "addr"))
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	logPath := filepath.Join(abs, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer logFile.Close()
	cmd := exec.Command(self, "serve", "--data", abs, "--addr", addr, "--repo", root, "--sandbox", sb)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = logFile, logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if url, ok := serverURL(); ok {
			return url, nil
		}
		select {
		case <-exited:
			tail, _ := os.ReadFile(logPath)
			if len(tail) > 800 {
				tail = tail[len(tail)-800:]
			}
			return "", fmt.Errorf("the server exited during startup:\n%s", tail)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("the server did not start in time; see %s", logPath)
}

func upCmd() *cobra.Command {
	var sb string
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start the swarm server for this project in the background",
		RunE: func(*cobra.Command, []string) error {
			url, err := startServer(sb)
			if err != nil {
				return err
			}
			fmt.Println(url)
			return nil
		},
	}
	cmd.Flags().StringVar(&sb, "sandbox", "auto", "sandbox: auto, seatbelt, bwrap, docker or none")
	return cmd
}

func downCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Stop the swarm server for this project",
		RunE: func(*cobra.Command, []string) error {
			url, ok := serverURL()
			if !ok {
				fmt.Println("no server is running")
				return nil
			}
			b, err := os.ReadFile(filepath.Join(dataDir, "pid"))
			if err != nil {
				return err
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				return fmt.Errorf("bad pid file: %w", err)
			}
			if err := stopProcess(pid); err != nil {
				return err
			}
			for range 100 {
				if !healthy(url) {
					fmt.Println("stopped")
					return nil
				}
				time.Sleep(50 * time.Millisecond)
			}
			return errors.New("the server did not stop")
		},
	}
}

func selectHarnesses(arg string) ([]harness.Harness, error) {
	switch arg {
	case "", "installed", "detected":
		return harness.Detected(), nil
	case "all":
		return harness.All(), nil
	}
	var out []harness.Harness
	for _, id := range strings.Split(arg, ",") {
		h, ok := harness.Get(strings.TrimSpace(id))
		if !ok {
			return nil, fmt.Errorf("unknown harness %q: see aiculator harness", id)
		}
		out = append(out, h)
	}
	return out, nil
}

// selfCommand prefers the bare name, so a committed config works on any machine.
func selfCommand() string {
	self, err := os.Executable()
	if err != nil {
		return "aiculator"
	}
	if found, err := exec.LookPath("aiculator"); err == nil {
		a, _ := filepath.EvalSymlinks(found)
		b, _ := filepath.EvalSymlinks(self)
		if a == b {
			return "aiculator"
		}
	}
	return self
}

func joinCmd() *cobra.Command {
	var (
		list, sb string
		tier     int
		planner  bool
		noStart  bool
	)
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Connect installed harnesses to the swarm: server, skills and MCP config",
		Long: `Sets a project up so any harness can join the swarm, including one already open.

It starts the server, installs the bundled skills into .agents/skills, links them to
harnesses that need it, and registers the MCP bridge. No secrets are written.`,
		RunE: func(*cobra.Command, []string) error {
			hs, err := selectHarnesses(list)
			if err != nil {
				return err
			}
			if len(hs) == 0 {
				return errors.New("no known harness found on PATH; name one with --harness, or see aiculator harness")
			}
			if !noStart {
				if _, err := startServer(sb); err != nil {
					return err
				}
			}
			root := projectRoot()
			acts, err := skillpack.Install(skillpack.Options{Root: root, Patterns: []string{"swarm-*"}, Harnesses: hs})
			if err != nil {
				return err
			}
			for _, a := range acts {
				if a.What == "conflict" || a.What == "invalid" {
					fmt.Printf("warning: %s for %s: %s\n", a.Skill, a.Target, a.Detail)
				}
			}
			args := []string{"mcp", "--auto", "--tier", strconv.Itoa(tier)}
			if planner {
				args = append(args, "--planner")
			}
			backups := filepath.Join(dataDir, "backups")
			fmt.Printf("Skills are in %s. Edit them only there.\n\n", skillpack.CanonicalDir)
			for _, h := range hs {
				route := "shell (no restart needed)"
				if h.MCP != harness.MCPNone {
					e := harness.Entry{Command: selfCommand(), Args: append(args, "--harness", h.ID)}
					r, err := h.WriteMCP(root, e, backups)
					if err != nil {
						fmt.Printf("%-10s could not write its MCP config: %v\n", h.ID, err)
						continue
					}
					route = "MCP in " + h.MCPFile + " after a restart; shell works now"
					if r.Path == "" {
						route = "shell (no restart needed)"
					}
				}
				fmt.Printf("%-10s %s\n", h.ID, route)
				if h.Source != harness.SourceDocs {
					fmt.Printf("%-10s paths come from a community table, not %s's docs; run aiculator skill --list to check\n", "", h.Name)
				}
				if h.Note != "" {
					fmt.Printf("%-10s %s\n", "", h.Note)
				}
			}
			fmt.Printf("\nIn a NEW session, ask it to use the swarm-join skill (/swarm-join in Claude Code).\n")
			fmt.Printf("In a session ALREADY OPEN, paste this; it needs no restart:\n\n")
			fmt.Printf("  Read %s/swarm-join/SKILL.md and follow it. Join as tier %d.\n\n", skillpack.CanonicalDir, tier)
			if selfCommand() != "aiculator" {
				fmt.Printf("Note: aiculator is not on PATH. The skills call it by name, so add %s to PATH.\n", filepath.Dir(selfCommand()))
			}
			fmt.Println("A joined session runs outside the sandbox, under its harness's own permissions.")
			fmt.Println("For sandboxed workers, use aiculator drive.")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&list, "harness", "installed", "installed, all, or comma-separated ids")
	f.IntVar(&tier, "tier", 1, "tier: 1 standard, 2 advanced, 3 ultra")
	f.BoolVar(&planner, "planner", false, "also allow these sessions to plan")
	f.BoolVar(&noStart, "no-start", false, "do not start the server")
	f.StringVar(&sb, "sandbox", "auto", "sandbox for the server: auto, seatbelt, bwrap, docker or none")
	return cmd
}

func unjoinCmd() *cobra.Command {
	var list string
	var purge bool
	cmd := &cobra.Command{
		Use:   "unjoin",
		Short: "Remove the swarm's MCP config and skill links from harnesses",
		RunE: func(*cobra.Command, []string) error {
			hs, err := selectHarnesses(list)
			if err != nil {
				return err
			}
			root := projectRoot()
			backups := filepath.Join(dataDir, "backups")
			for _, h := range hs {
				r, err := h.RemoveMCP(root, backups)
				if err != nil {
					fmt.Printf("%s: %v\n", h.ID, err)
					continue
				}
				if r.Changed {
					fmt.Printf("%s: removed from %s\n", h.ID, h.MCPFile)
				}
			}
			acts, err := skillpack.Uninstall(skillpack.Options{Root: root, Patterns: []string{"swarm-*"}, Harnesses: hs, Purge: purge})
			if err != nil {
				return err
			}
			for _, a := range acts {
				fmt.Printf("%s: %s %s\n", a.Target, a.What, a.Skill)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&list, "harness", "all", "installed, all, or comma-separated ids")
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete the swarm skills from .agents/skills")
	return cmd
}

func harnessCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "harness",
		Aliases: []string{"harnesses"},
		Short:   "List supported harnesses and how each connects",
		RunE: func(*cobra.Command, []string) error {
			root := projectRoot()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tINSTALLED\tMCP\tSKILLS\tACP\tFACTS FROM")
			for _, h := range harness.All() {
				installed := "-"
				if h.Installed() {
					installed = "yes"
				}
				mcpState := "shell only"
				if h.MCP != harness.MCPNone {
					mcpState = h.MCPFile
					if !h.HasMCP(root) {
						mcpState += " (not set)"
					}
				}
				skills := h.SkillsDir + " (link)"
				if h.ReadsAgents {
					skills = skillpack.CanonicalDir
				}
				acp := "-"
				if len(h.ACP) > 0 {
					acp = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", h.ID, h.Name, installed, mcpState, skills, acp, h.Source)
			}
			return w.Flush()
		},
	}
}

func skillCmd() *cobra.Command {
	var (
		install, uninstall             []string
		listFlag, lint, global, copyIt bool
		force, purge                   bool
		harnesses, newName, desc       string
		paths, sources                 []string
	)
	cmd := &cobra.Command{
		Use:   "skill [--install PATTERN...]",
		Short: "Manage skills in one place: .agents/skills",
		Long: `Skills live in .agents/skills. Installing puts them where each harness looks: harnesses
that scan .agents/skills need nothing, and the others get a link. Skills in .agent/skills
are picked up too. An edit in one place reaches every harness.

Patterns use * for anything, ? for one character and [abc] for a set. Quote any pattern
with a wildcard: an unquoted * alone also works, but zsh stops at an unmatched *-sth.`,
		Example: `  aiculator skill --install '*'                  all skills, to every installed harness
  aiculator skill --install 'swarm-*' 'lint-?'   by pattern
  aiculator skill --install '*-tool' --global    into ~/.agents/skills and home directories
  aiculator skill --install '*' --harness all    every supported harness, installed or not
  aiculator skill --install x --harness claude,pi
  aiculator skill --install x --path ./vendor/skills --harness none
  aiculator skill --install x --source ~/my-skills
  aiculator skill --list
  aiculator skill --lint
  aiculator skill --new my-skill --description "..."
  aiculator skill --uninstall 'swarm-*' --purge`,
		Args: cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			root := projectRoot()
			var hs []harness.Harness
			if harnesses != "none" {
				var err error
				if hs, err = selectHarnesses(harnesses); err != nil {
					return err
				}
			}
			o := skillpack.Options{Root: root, Global: global, Harnesses: hs, Paths: paths, Sources: sources, Copy: copyIt, Force: force, Purge: purge}
			wd, _ := os.Getwd()
			switch {
			case c.Flags().Changed("install"):
				// Words after --install are more patterns, including an expanded *.
				o.Patterns = skillpack.NormalizeShellStar(append(install, args...), wd)
				acts, err := skillpack.Install(o)
				printActions(acts)
				return err
			case c.Flags().Changed("uninstall"):
				o.Patterns = skillpack.NormalizeShellStar(append(uninstall, args...), wd)
				acts, err := skillpack.Uninstall(o)
				printActions(acts)
				return err
			case newName != "":
				dir, err := skillpack.New(o, newName, desc)
				if err != nil {
					return err
				}
				fmt.Printf("Created %s. Edit it, then run: aiculator skill --install %s\n", dir, newName)
				return nil
			case lint:
				return lintAll(o)
			case listFlag, len(args) == 0:
				return listSkills(o)
			}
			return c.Help()
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&install, "install", nil, "install skills matching these patterns")
	f.StringSliceVar(&uninstall, "uninstall", nil, "remove skills matching these patterns from harnesses")
	f.BoolVar(&listFlag, "list", false, "list skills")
	f.BoolVar(&lint, "lint", false, "check skills against the Agent Skills spec")
	f.StringVar(&newName, "new", "", "create a skill with full metadata")
	f.StringVar(&desc, "description", "", "description for --new")
	f.StringVar(&harnesses, "harness", "installed", "installed, all, none, or comma-separated ids")
	f.StringSliceVar(&paths, "path", nil, "also install to this directory")
	f.StringSliceVar(&sources, "source", nil, "also read skills from this directory")
	f.BoolVar(&global, "global", false, "use ~/.agents/skills and home directories")
	f.BoolVar(&copyIt, "copy", false, "copy instead of linking, for tools that do not follow links")
	f.BoolVar(&force, "force", false, "refresh bundled skills and replace links; your own directories are never replaced")
	f.BoolVar(&purge, "purge", false, "with --uninstall, also delete from .agents/skills")
	return cmd
}

func printActions(acts []skillpack.Action) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, a := range acts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.Skill, a.What, a.Target, a.Detail)
	}
	_ = w.Flush()
}

func listSkills(o skillpack.Options) error {
	rows, err := skillpack.List(o)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SKILL\tVERSION\tKIND\tINSTALLED\tLINT\tHARNESSES\tSOURCE")
	for _, r := range rows {
		state := "-"
		if r.Installed {
			state = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%de %dw\t%s\t%s\n", r.Name, r.Version, r.Kind, state, r.Errors, r.Warnings, strings.Join(r.Harnesses, ","), r.Source)
	}
	return w.Flush()
}

func lintAll(o skillpack.Options) error {
	dir := filepath.Join(o.Root, skillpack.CanonicalDir)
	if o.Global {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, skillpack.CanonicalDir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	failed := false
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
			continue
		}
		issues := skillpack.Lint(p)
		if len(issues) == 0 {
			fmt.Printf("%s: ok\n", e.Name())
		}
		for _, i := range issues {
			fmt.Printf("%s: %s: %s\n", e.Name(), i.Level, i.Msg)
		}
		failed = failed || skillpack.IssuesHaveErrors(issues)
	}
	if failed {
		return errors.New("some skills have errors")
	}
	return nil
}

func randomSuffix() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// issueLocal mints a token with the local admin token. Reaching that file already
// means being this user, and the sandbox hides it from sandboxed agents.
func issueLocal(ctx context.Context, q api.TokenReq) (url, token string, err error) {
	c, err := adminClient()
	if err != nil {
		return "", "", fmt.Errorf("%w; start the server with: aiculator up", err)
	}
	var out map[string]string
	if err := c.Post(ctx, "/v1/tokens", q, &out); err != nil {
		return "", "", fmt.Errorf("no server answered at %s: %w; start it with: aiculator up", c.URL, err)
	}
	return c.URL, out["token"], nil
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// bridge forwards tool calls to the server, reconnecting if the server restarted.
type bridge struct {
	url, token string
	mu         sync.Mutex
	session    *mcp.ClientSession
}

func (b *bridge) connect(ctx context.Context) (*mcp.ClientSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session != nil {
		return b.session, nil
	}
	cl := mcp.NewClient(&mcp.Implementation{Name: "aiculator-bridge", Version: version}, nil)
	s, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   b.url + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{b.token, http.DefaultTransport}},
	}, nil)
	if err != nil {
		return nil, err
	}
	b.session = s
	return s, nil
}

func (b *bridge) drop(s *mcp.ClientSession) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session == s {
		b.session = nil
		_ = s.Close()
	}
}

func (b *bridge) call(ctx context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	s, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.CallTool(ctx, p)
	if err == nil || ctx.Err() != nil {
		return r, err
	}
	// The tool itself never ran or its reply was lost, and the lease model makes a
	// repeat safe: a lost grant simply expires. Reconnect once in case of a restart.
	b.drop(s)
	s, err = b.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("the swarm server is unreachable: %w", err)
	}
	return s.CallTool(ctx, p)
}

// mcpCmd bridges stdio to the server, for harnesses that launch MCP servers as
// subprocesses. --auto gets its own token locally, so harness config holds no secret.
func mcpCmd() *cobra.Command {
	var (
		url, harnessID, agent string
		auto, planner         bool
		tier                  int
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the swarm's MCP tools over stdio (--auto, or a token in SWARM_TOKEN)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			token := os.Getenv("SWARM_TOKEN")
			if auto {
				if agent == "" {
					name := harnessID
					if name == "" {
						name = "session"
					}
					agent = name + "-" + randomSuffix()
				}
				var err error
				url, token, err = issueLocal(ctx, api.TokenReq{Agent: agent, Tier: tier, Planner: planner})
				if err != nil {
					return err
				}
			}
			if token == "" {
				return errors.New("SWARM_TOKEN is not set; or pass --auto")
			}
			b := &bridge{url: url, token: token}
			session, err := b.connect(ctx)
			if err != nil {
				return err
			}
			defer func() { b.drop(session) }()
			srv := mcp.NewServer(&mcp.Implementation{Name: "swarm", Version: version},
				&mcp.ServerOptions{Instructions: session.InitializeResult().Instructions})
			for t, err := range session.Tools(ctx, nil) {
				if err != nil {
					return err
				}
				srv.AddTool(t, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return b.call(ctx, &mcp.CallToolParams{Name: r.Params.Name, Arguments: r.Params.Arguments})
				})
			}
			return srv.Run(ctx, &mcp.StdioTransport{})
		},
	}
	f := cmd.Flags()
	f.StringVar(&url, "url", "http://127.0.0.1:7777", "server URL; --auto finds it")
	f.BoolVar(&auto, "auto", false, "find the local server and get a token by itself")
	f.StringVar(&harnessID, "harness", "", "harness id, used in the agent's name")
	f.StringVar(&agent, "agent", "", "agent name; generated if empty")
	f.IntVar(&tier, "tier", 1, "tier, with --auto")
	f.BoolVar(&planner, "planner", false, "allow planning, with --auto")
	cmd.AddCommand(mcpAddCmd(), mcpPrintCmd(), mcpRemoveCmd())
	return cmd
}

// workCmd is the shell route: every worker capability as a command, for harnesses with
// no MCP and for sessions that were open before MCP was configured.
func workCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "work",
		Short: "Work the queue from a shell: the route for any harness, no restart needed",
	}
	var harnessID string
	var tier int
	var plannerFlag bool
	var agentName string
	join := &cobra.Command{
		Use:   "join",
		Short: "Register this session as a worker and print its agent name",
		RunE: func(cmd *cobra.Command, _ []string) error {
			name := agentName
			if name == "" {
				prefix := harnessID
				if prefix == "" {
					prefix = "session"
				}
				name = prefix + "-" + randomSuffix()
			}
			_, tok, err := issueLocal(cmd.Context(), api.TokenReq{Agent: name, Tier: tier, Planner: plannerFlag})
			if err != nil {
				return err
			}
			dir := filepath.Join(dataDir, "agents")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, name+".token"), []byte(tok), 0o600); err != nil {
				return err
			}
			fmt.Println(name)
			return nil
		},
	}
	join.Flags().StringVar(&harnessID, "harness", "", "harness id, used in the agent's name")
	join.Flags().IntVar(&tier, "tier", 1, "tier: 1 standard, 2 advanced, 3 ultra")
	join.Flags().BoolVar(&plannerFlag, "planner", false, "also allow this session to plan")
	join.Flags().StringVar(&agentName, "agent", "", "agent name; generated if empty")

	var agent string
	agentClient := func() (*client.Client, error) {
		if agent == "" {
			return nil, errors.New("--agent is required; aiculator work join prints your name")
		}
		// The name becomes a path, so it must not be able to leave the directory.
		if filepath.Base(agent) != agent || strings.HasPrefix(agent, ".") {
			return nil, fmt.Errorf("invalid agent name %q", agent)
		}
		tok, err := os.ReadFile(filepath.Join(dataDir, "agents", agent+".token"))
		if err != nil {
			return nil, fmt.Errorf("agent %s is not registered; run aiculator work join: %w", agent, err)
		}
		url, err := os.ReadFile(filepath.Join(dataDir, "addr"))
		if err != nil {
			return nil, fmt.Errorf("no running server found: %w", err)
		}
		return client.New(strings.TrimSpace(string(url)), string(tok)), nil
	}

	var hold time.Duration
	wait := &cobra.Command{
		Use:   "wait",
		Short: "Get the next task, or be told to wait or exit",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := agentClient()
			if err != nil {
				return err
			}
			var g swarm.Grant
			if err := c.Post(cmd.Context(), "/v1/agent/wait", api.WaitReq{HoldMs: hold.Milliseconds()}, &g); err != nil {
				return err
			}
			return printJSON(g)
		},
	}
	wait.Flags().DurationVar(&hold, "hold", api.DefaultHold, "how long to hold if no work is available")

	var taskID string
	claim := &cobra.Command{
		Use:   "claim",
		Short: "Claim one specific ready task",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := agentClient()
			if err != nil {
				return err
			}
			var g swarm.Grant
			if err := c.Post(cmd.Context(), "/v1/agent/claim", api.LeaseReq{TaskID: taskID}, &g); err != nil {
				return err
			}
			return printJSON(g)
		},
	}
	claim.Flags().StringVar(&taskID, "task", "", "task id")

	var lease uint64
	var note string
	var cost, tokens int64
	var pass bool
	leased := func(use, short, path string) *cobra.Command {
		sub := &cobra.Command{
			Use: use, Short: short,
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := agentClient()
				if err != nil {
					return err
				}
				var t any
				q := api.LeaseReq{TaskID: taskID, Lease: lease, Note: note, Cost: cost, Tokens: tokens, Pass: pass}
				if err := c.Post(cmd.Context(), path, q, &t); err != nil {
					return err
				}
				return printJSON(t)
			},
		}
		sub.Flags().StringVar(&taskID, "task", "", "task id")
		sub.Flags().Uint64Var(&lease, "lease", 0, "lease number from wait")
		sub.Flags().StringVar(&note, "note", "", "note saved for later attempts")
		sub.Flags().Int64Var(&cost, "cost", 0, "cost since your last report, in micro-dollars")
		sub.Flags().Int64Var(&tokens, "tokens", 0, "tokens used since your last report")
		return sub
	}
	verdict := leased("verdict", "Approve (--pass) or reject another agent's work", "/v1/agent/verdict")
	verdict.Flags().BoolVar(&pass, "pass", false, "approve; leave out to reject")

	situation := &cobra.Command{
		Use:   "situation",
		Short: "Show the swarm as this agent sees it",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := agentClient()
			if err != nil {
				return err
			}
			var s engine.Situation
			if err := c.Get(cmd.Context(), "/v1/situation", &s); err != nil {
				return err
			}
			return printJSON(s)
		},
	}
	cmd.PersistentFlags().StringVar(&agent, "agent", "", "agent name from aiculator work join")
	cmd.AddCommand(join, wait, claim,
		leased("heartbeat", "Extend your lease; this does not count as progress", "/v1/agent/heartbeat"),
		leased("progress", "Save your work; only real file changes count", "/v1/agent/progress"),
		leased("submit", "Submit finished work for verification", "/v1/agent/submit"),
		leased("fail", "Give up; saved work and note go to the next attempt", "/v1/agent/fail"),
		leased("release", "Hand back with no penalty, when the harness cannot do it", "/v1/agent/release"),
		verdict, situation)
	return cmd
}
