package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/client"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/harness"
	"github.com/Grano22/aiculator/internal/procinfo"
	"github.com/Grano22/aiculator/internal/realm"
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
	return url, ownServer(url)
}

// ownServer checks that the server answering there is this project's. After a crash, the
// address file can point at a port that another project's server has since taken.
func ownServer(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var h map[string]string
	if client.New(url, "").Get(ctx, "/v1/healthz", &h) != nil {
		return false
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return true
	}
	// A server from an older version does not say, and it is given the benefit of the doubt.
	return h["instance"] == "" || h["instance"] == realm.Instance(abs)
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
		// Reusing the port lets open sessions reconnect, but only if it is still free: another
		// project's server may have taken it since this one last ran.
		last := strings.TrimPrefix(strings.TrimSpace(string(b)), "http://")
		if ln, err := net.Listen("tcp", last); err == nil {
			_ = ln.Close()
			addr = last
		}
	}
	_ = os.Remove(filepath.Join(abs, "addr"))
	cmd, logPath, err := spawnServe(root, abs, addr, sb, false)
	if err != nil {
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

// spawnServe starts a detached server writing to server.log.
func spawnServe(root, abs, addr, sb string, standby bool) (*exec.Cmd, string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, "", err
	}
	logPath := filepath.Join(abs, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, "", err
	}
	defer logFile.Close()
	args := append([]string{"serve", "--data", abs, "--addr", addr, "--repo", root, "--sandbox", sb}, loggingArgs()...)
	if len(upAging) > 0 {
		args = append(args, "--aging", strings.Join(upAging, ","))
	}
	if standby {
		args = append(args, "--standby")
	}
	cmd := exec.Command(self, args...)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = logFile, logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	return cmd, logPath, nil
}

// startStandby starts a server that waits on the data directory and takes over when the
// leader stops, on the leader's address so that sessions reconnect to the same place.
func startStandby(sb string) (int, error) {
	url, err := startServer(sb)
	if err != nil {
		return 0, err
	}
	pidPath := filepath.Join(dataDir, "standby.pid")
	if pid := pidFile(pidPath); pid != 0 && processAlive(pid) {
		return pid, nil
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return 0, err
	}
	cmd, logPath, err := spawnServe(projectRoot(), abs, strings.TrimPrefix(url, "http://"), sb, true)
	if err != nil {
		return 0, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	for range 200 {
		if pidFile(pidPath) == cmd.Process.Pid {
			return cmd.Process.Pid, nil
		}
		select {
		case <-exited:
			return 0, fmt.Errorf("the standby exited during startup; see %s", logPath)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return 0, fmt.Errorf("the standby did not start in time; see %s", logPath)
}

// stopPid stops a process and waits for it to exit.
func stopPid(pid int) (bool, error) {
	// A process that died just now needs no signal.
	if err := stopProcess(pid); err != nil && processAlive(pid) {
		return false, err
	}
	// The endpoint closes before the store finishes writing, so wait for the process
	// itself. Then a new up, or removing .swarm, cannot meet it.
	for range 400 {
		if !processAlive(pid) {
			return true, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false, nil
}

// upAging is passed on to the server that up starts.
var upAging []string

func upCmd() *cobra.Command {
	var sb string
	var standby bool
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start the swarm server for this project in the background",
		Long: `Start the swarm server for this project in the background.

With --standby, also start a second server that waits and takes over if the first one
stops or dies. The leader is whoever holds the lock on .swarm, so the two never write at
once. Both must run on this machine.`,
		RunE: func(*cobra.Command, []string) error {
			if standby {
				pid, err := startStandby(sb)
				if err != nil {
					return err
				}
				url, _ := serverURL()
				fmt.Printf("%s\nstandby %d\n", url, pid)
				return nil
			}
			url, err := startServer(sb)
			if err != nil {
				return err
			}
			fmt.Println(url)
			return nil
		},
	}
	cmd.Flags().StringVar(&sb, "sandbox", "auto", "sandbox: auto, seatbelt, bwrap, docker or none")
	cmd.Flags().StringSliceVar(&upAging, "aging", nil, "raise priority after a wait, as wait=boost, such as 1h=5,4h=20")
	cmd.Flags().BoolVar(&standby, "standby", false, "also start a standby server that takes over if this one dies")
	return cmd
}

func downCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop the swarm server for this project, or with --all for every realm",
		RunE: func(*cobra.Command, []string) error {
			if all {
				return stopRealms(nil)
			}
			msg, err := stopServer()
			if msg != "" {
				fmt.Println(msg)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "stop the server of every realm on this machine")
	return cmd
}

// stopServer stops the server using dataDir, and its standby first, so that a clean stop
// does not hand over.
func stopServer() (string, error) {
	var said []string
	if pid := pidFile(filepath.Join(dataDir, "standby.pid")); pid != 0 && processAlive(pid) {
		if ok, err := stopPid(pid); err != nil {
			return "", err
		} else if ok {
			said = append(said, "standby stopped")
		}
	}
	url, ok := serverURL()
	if !ok {
		return strings.Join(append(said, "no server is running"), "\n"), nil
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "pid"))
	if err != nil {
		return "", err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return "", fmt.Errorf("bad pid file: %w", err)
	}
	stopped, err := stopPid(pid)
	if err != nil {
		return "", err
	}
	if stopped {
		return strings.Join(append(said, "stopped"), "\n"), nil
	}
	if healthy(url) {
		return "", errors.New("the server did not stop")
	}
	// A container with no init never reaps the process, so it looks alive forever.
	return strings.Join(append(said, fmt.Sprintf("stopped answering, but process %d has not exited", pid)), "\n"), nil
}

// stopRealms stops the servers of the realms named, or of every realm. One that fails does
// not keep the others running.
func stopRealms(names []string) error {
	all, err := realm.List()
	if err != nil {
		return err
	}
	saved := dataDir
	defer func() { dataDir = saved }()
	var failed []string
	found := 0
	for _, r := range all {
		if len(names) > 0 && !slices.Contains(names, r.Name) {
			continue
		}
		found++
		if _, err := os.Stat(filepath.Join(r.DataDir, "pid")); err != nil {
			fmt.Printf("%-20s not running\n", r.Name)
			continue
		}
		dataDir = r.DataDir
		msg, err := stopServer()
		if err != nil {
			failed = append(failed, r.Name)
			fmt.Printf("%-20s %v\n", r.Name, err)
			continue
		}
		fmt.Printf("%-20s %s\n", r.Name, strings.ReplaceAll(msg, "\n", ", "))
	}
	if len(names) > 0 && found < len(names) {
		return fmt.Errorf("some realms were not found; see aiculator realm ls")
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not stop: %s", strings.Join(failed, ", "))
	}
	return nil
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

// setupPlan is what join and init have decided to do.
type setupPlan struct {
	harnesses          []harness.Harness
	skills, mcp, start bool
	tier               int
	planner            bool
	sandbox            string
	// global sets up the whole machine rather than this project.
	global bool
	// reuse leaves alone what a harness can already find, at either level.
	reuse bool
	// timeouts raises Claude Code's command timeout, so work next can wait longer per call.
	timeouts bool
}

const swarmSkills = "swarm-*"

// bundledSwarmSkills lists the skills a project needs a harness to find.
var bundledSwarmSkills = []string{"swarm-join", "swarm-work", "swarm-operate", "swarm-plan"}

// globalBackups is where config is copied before a user-level file is changed.
func globalBackups() string { return harness.ExpandHome("~/.aiculator/backups") }

// atUserLevel returns the harness with its MCP config pointed at the user-level file, or
// false if it has none.
func atUserLevel(h harness.Harness) (harness.Harness, bool) {
	if h.GlobalMCPFile == "" {
		return h, false
	}
	h.MCPFile = harness.ExpandHome(h.GlobalMCPFile)
	return h, true
}

// skillsFound reports whether a harness can already find all the swarm's skills, and where
// the nearest one is.
func skillsFound(h harness.Harness, root string) (string, bool) {
	level := ""
	for _, name := range bundledSwarmSkills {
		w := skillpack.Where(name, h, root)
		if w == "" {
			return "", false
		}
		if level == "" || w == "project" {
			level = w
		}
	}
	return level, true
}

// installSkills puts skills where the plan says, and returns how each harness finds them.
func installSkills(p setupPlan, root string) (map[string]string, error) {
	levels := map[string]string{}
	var need []harness.Harness
	for _, h := range p.harnesses {
		if p.global && !skillpack.HasGlobal(h) {
			levels[h.ID] = "none"
			continue
		}
		if p.reuse {
			if level, ok := skillsFound(h, root); ok {
				levels[h.ID] = level
				continue
			}
		}
		need = append(need, h)
		levels[h.ID] = "project"
		if p.global {
			levels[h.ID] = "global"
		}
	}
	if len(need) == 0 {
		return levels, nil
	}
	acts, err := skillpack.Install(skillpack.Options{Root: root, Global: p.global, Patterns: []string{swarmSkills}, Harnesses: need})
	if err != nil {
		return nil, err
	}
	for _, a := range acts {
		if a.What == "conflict" || a.What == "invalid" {
			fmt.Printf("warning: %s for %s: %s\n", a.Skill, a.Target, a.Detail)
		}
	}
	return levels, nil
}

// writeMCPFor adds a harness's MCP config, and says how it can reach the swarm.
func writeMCPFor(p setupPlan, h harness.Harness, root string, args []string) (string, error) {
	if h.MCP == harness.MCPNone {
		return "", nil
	}
	target, backups := h, filepath.Join(dataDir, "backups")
	if p.global {
		var ok bool
		if target, ok = atUserLevel(h); !ok {
			return "no user-level MCP config, so use aiculator init --scoped in a project", nil
		}
		backups = globalBackups()
	} else if p.reuse {
		if user, ok := atUserLevel(h); ok && user.HasMCP(root) {
			return "MCP is already set for every project, in " + h.GlobalMCPFile + ", at the tier chosen then", nil
		}
	}
	e := harness.Entry{Command: selfCommand(), Args: append(slices.Clone(args), "--harness", h.ID)}
	r, err := target.WriteMCP(root, e, backups)
	if err != nil {
		return "", err
	}
	if r.Path == "" {
		return "", nil
	}
	shown := h.MCPFile
	if p.global {
		shown = h.GlobalMCPFile
	}
	return "MCP in " + shown + " after a restart", nil
}

// runSetup does the writing for join and init. Each step can be run again safely.
func runSetup(p setupPlan) error {
	hs, tier := p.harnesses, p.tier
	if p.start {
		if _, err := startServer(p.sandbox); err != nil {
			return err
		}
	}
	root := projectRoot()
	if p.timeouts {
		file := claudeSettingsFile(p.global, root)
		backups := filepath.Join(dataDir, "backups")
		if p.global {
			backups = globalBackups()
		}
		switch changed, err := raiseCommandTimeout(file, backups); {
		case err != nil:
			fmt.Println("Command timeout not changed:", err)
		case changed:
			fmt.Printf("Claude Code may now run a command for up to %v (%s), so aiculator work next waits longer per call.\n", commandTimeout, file)
		}
	}
	levels := map[string]string{}
	if p.skills {
		var err error
		if levels, err = installSkills(p, root); err != nil {
			return err
		}
		if p.global {
			fmt.Println("Skills are in ~/.agents/skills for every project. Edit them only there.")
			fmt.Println()
		} else if slices.Contains(slices.Collect(maps.Values(levels)), "project") {
			fmt.Printf("Skills are in %s. Edit them only there.\n\n", skillpack.CanonicalDir)
		} else {
			fmt.Println("Skills were already installed for every project, so they were left as they are.")
			fmt.Println()
		}
	}
	args := []string{"mcp", "--auto", "--tier", strconv.Itoa(tier)}
	if p.planner {
		args = append(args, "--planner")
	}
	for _, h := range hs {
		var parts []string
		switch {
		case !p.skills:
		case levels[h.ID] == "none":
			parts = append(parts, "no user-level skills, so use aiculator init --scoped in a project")
		case p.global:
			parts = append(parts, "skills for every project")
		default:
			parts = append(parts, "shell (no restart needed)")
		}
		if p.mcp {
			route, err := writeMCPFor(p, h, root, args)
			if err != nil {
				fmt.Printf("%-10s could not write its MCP config: %v\n", h.ID, err)
				continue
			}
			if route != "" {
				parts = append(parts, route)
			}
		}
		if len(parts) == 0 {
			parts = append(parts, "nothing installed")
		}
		fmt.Printf("%-10s %s\n", h.ID, strings.Join(parts, "; "))
		if h.Source != harness.SourceDocs {
			fmt.Printf("%-10s paths come from a community table, not %s's docs; run aiculator skill --list to check\n", "", h.Name)
		}
		if h.Note != "" {
			fmt.Printf("%-10s %s\n", "", h.Note)
		}
	}
	fmt.Println()
	switch {
	case p.global:
		fmt.Println("This machine is set up. In a project, start the swarm with: aiculator join")
	case p.skills:
		dir := skillpack.CanonicalDir
		if !slices.Contains(slices.Collect(maps.Values(levels)), "project") {
			dir = "~/" + skillpack.CanonicalDir
		}
		fmt.Printf("In a NEW session, ask it to use the swarm-join skill (/swarm-join in Claude Code).\n")
		fmt.Printf("In a session ALREADY OPEN, paste this; it needs no restart:\n\n")
		fmt.Printf("  Read %s/swarm-join/SKILL.md and follow it. Join as tier %d.\n\n", dir, tier)
	case p.mcp:
		fmt.Println("Restart your harness, and ask it to use the swarm's wait_for_task tool.")
		fmt.Println()
	}
	if !p.global && !p.start {
		fmt.Println("The server is not running. Start it with: aiculator up")
	}
	if !p.skills && !p.mcp {
		fmt.Println("No harness was connected. To add that later, run aiculator init again.")
	}
	if selfCommand() != "aiculator" {
		fmt.Printf("Note: aiculator is not on PATH. The skills call it by name, so add %s to PATH.\n", filepath.Dir(selfCommand()))
	}
	if !p.global {
		fmt.Println("A joined session runs outside the sandbox, under its harness's own permissions.")
		fmt.Println("For sandboxed workers, use aiculator drive.")
	}
	return nil
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

It starts the server, and works on its own on a machine with nothing set up: it installs the
bundled skills into .agents/skills, links them to harnesses that need it, and registers the
MCP bridge. Anything a harness can already find is left alone, at either level, so after
aiculator init it only has to start the server. No secrets are written.`,
		RunE: func(*cobra.Command, []string) error {
			hs, err := selectHarnesses(list)
			if err != nil {
				return err
			}
			if len(hs) == 0 {
				return errors.New("no known harness found on PATH; name one with --harness, or see aiculator harness")
			}
			return runSetup(setupPlan{harnesses: hs, skills: true, mcp: true, start: !noStart, tier: tier, planner: planner, sandbox: sb, reuse: true})
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
	// harness names what this bridge serves, for the session it opens.
	harness string
	// issue gets a token when one is needed, for a bridge that finds its server by itself.
	issue   func(ctx context.Context) (url, token string, err error)
	mu      sync.Mutex
	session *mcp.ClientSession
}

func (b *bridge) connect(ctx context.Context) (*mcp.ClientSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session != nil {
		return b.session, nil
	}
	if b.issue != nil {
		// Looked up each time, so a server started after this session began is found, and
		// one that moved to a new port after a restart is found again.
		url, token, err := b.issue(ctx)
		if err != nil {
			return nil, err
		}
		b.url, b.token = url, token
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
	// Said again on each connection, so a server that restarted knows this session too.
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = client.New(b.url, b.token).Post(hctx, "/v1/agent/hello", procinfo.Self("mcp", b.harness, "aiculator-bridge", version, false), nil)
	return s, nil
}

// reset forgets the connection, so the next call finds the server again.
func (b *bridge) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session != nil {
		_ = b.session.Close()
		b.session = nil
	}
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
var tierNames = map[int]string{1: "standard", 2: "advanced", 3: "ultra"}

// resolveTier reads a tier given by a person, choosing one from the model and effort when
// it is auto. Values not given are looked for in the harness's environment.
func resolveTier(arg, model, effort string) (int, string, error) {
	tier, err := harness.ParseTier(arg)
	if err != nil {
		return 0, "", err
	}
	if tier > 0 {
		return tier, "as asked", nil
	}
	envModel, envEffort := harness.ModelFromEnv()
	if model == "" {
		model = envModel
	}
	if effort == "" {
		effort = envEffort
	}
	tier, why := harness.AutoTier(model, effort)
	return tier, "auto: " + why, nil
}

type joinSwarmInput struct {
	Tier   string `json:"tier,omitempty" jsonschema:"1 standard, 2 advanced, 3 ultra, or auto; auto when empty"`
	Model  string `json:"model,omitempty" jsonschema:"for auto: the model you run as, such as claude-sonnet-5-5"`
	Effort string `json:"effort,omitempty" jsonschema:"for auto: the effort or variant you run at, such as low, high or max"`
}

// noSwarmHere is what a tool answers in a folder that has not joined a swarm. It says what
// to do, because the model reading it can pass that on.
const noSwarmHere = "This folder has no swarm server running. To start one, run: aiculator join. Then call this tool again; the session does not need a restart."

type realmUseInput struct {
	Name string `json:"name" jsonschema:"the realm to work in, as listed by realm_list"`
}

// serveAutoBridge offers the swarm's tools from any folder, and looks for a server only when
// one is called. So a config that applies to every project cannot show up as a broken MCP
// server in the ones that do not use the swarm. No server here is an answer, not a failure.
//
// A session stays in the realm of the folder it started in. With switching allowed it can
// also list the others and move between them, but never while it holds a task.
func serveAutoBridge(ctx context.Context, q api.TokenReq, harnessID string, switching bool) error {
	if q.Agent == "" {
		name := harnessID
		if name == "" {
			name = "session"
		}
		q.Agent = name + "-" + randomSuffix()
	}
	tools, instructions, err := api.ToolDefinitions(ctx)
	if err != nil {
		return err
	}
	var from string
	if from, err = os.Getwd(); err != nil {
		return err
	}
	var heldMu sync.Mutex
	held := ""
	b := &bridge{harness: harnessID}
	b.issue = func(ctx context.Context) (string, string, error) {
		heldMu.Lock()
		if !dataDirFlagSet {
			dataDir = findDataDirFrom(from)
		}
		req := q // join_swarm may change the tier; read it under the same lock.
		heldMu.Unlock()
		return issueLocal(ctx, req)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "swarm", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	for _, t := range tools {
		name := t.Name
		srv.AddTool(t, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res, err := b.call(ctx, &mcp.CallToolParams{Name: r.Params.Name, Arguments: r.Params.Arguments})
			if err != nil && ctx.Err() == nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: noSwarmHere}}}, nil
			}
			if err == nil && switching {
				heldMu.Lock()
				held = trackHeld(held, name, res)
				heldMu.Unlock()
			}
			return res, err
		})
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "join_swarm", Description: "Choose the tier this session works at: 1 standard, 2 advanced, 3 ultra, or auto to choose from your model and effort. Call it once before wait_for_task when the user named a tier or wants auto. Refused while you hold a task."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in joinSwarmInput) (*mcp.CallToolResult, any, error) {
			heldMu.Lock()
			defer heldMu.Unlock()
			if held != "" {
				return nil, nil, fmt.Errorf("you hold task %s: submit, fail or release it before changing tier", held)
			}
			tier, why, err := resolveTier(in.Tier, in.Model, in.Effort)
			if err != nil {
				return nil, nil, err
			}
			q.Tier = tier
			b.reset() // The next call gets a token at the new tier.
			return nil, map[string]any{"agent": q.Agent, "tier": tier, "tier_name": tierNames[tier], "why": why}, nil
		})
	if switching {
		mcp.AddTool(srv, &mcp.Tool{Name: "realm_list", Description: "List the projects on this machine that use the swarm, with which ones are running and which one this session is in."},
			func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
				all, err := realm.List()
				if err != nil {
					return nil, nil, err
				}
				heldMu.Lock()
				current := findDataDirFrom(from)
				heldMu.Unlock()
				views := make([]map[string]any, 0, len(all))
				for _, r := range all {
					_, statErr := os.Stat(filepath.Join(r.DataDir, "addr"))
					views = append(views, map[string]any{"name": r.Name, "path": r.Path, "running": statErr == nil, "current": r.DataDir == current})
				}
				return nil, map[string]any{"realms": views}, nil
			})
		mcp.AddTool(srv, &mcp.Tool{Name: "realm_use", Description: "Move this session to another project's swarm. Refused while you hold a task: finish or release it first, because a lease belongs to one realm."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in realmUseInput) (*mcp.CallToolResult, any, error) {
				heldMu.Lock()
				defer heldMu.Unlock()
				if held != "" {
					return nil, nil, fmt.Errorf("you hold task %s here: submit, fail or release it before moving", held)
				}
				r, err := realm.Get(in.Name)
				if err != nil {
					return nil, nil, err
				}
				from = r.Path
				b.reset()
				return nil, map[string]any{"realm": r.Name, "path": r.Path}, nil
			})
	}
	defer b.reset()
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// trackHeld works out which task this session holds after a tool call.
func trackHeld(held, tool string, r *mcp.CallToolResult) string {
	if r == nil || r.IsError {
		return held
	}
	switch tool {
	case "wait_for_task", "claim":
		b, err := json.Marshal(r.StructuredContent)
		if err != nil {
			return held
		}
		var g struct {
			Action string `json:"action"`
			Task   struct {
				ID string `json:"id"`
			} `json:"task"`
		}
		if json.Unmarshal(b, &g) == nil && g.Action == "task" {
			return g.Task.ID
		}
	case "submit", "fail", "release", "review_verdict":
		return ""
	}
	return held
}

func mcpCmd() *cobra.Command {
	var (
		url, harnessID, agent string
		auto, planner         bool
		realmSwitch           bool
		tierArg, model        string
		effort                string
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the swarm's MCP tools over stdio (--auto, or a token in SWARM_TOKEN)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if auto {
				tier, why, err := resolveTier(tierArg, model, effort)
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "aiculator mcp: joining at tier %d: %s\n", tier, why) // stdout is the MCP channel.
				return serveAutoBridge(ctx, api.TokenReq{Agent: agent, Tier: tier, Planner: planner}, harnessID, realmSwitch)
			}
			token := os.Getenv("SWARM_TOKEN")
			if token == "" {
				return errors.New("SWARM_TOKEN is not set; or pass --auto")
			}
			b := &bridge{url: url, token: token, harness: harnessID}
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
	f.StringVar(&tierArg, "tier", "auto", "with --auto: 1 standard, 2 advanced, 3 ultra, or auto to choose from the model and effort")
	f.StringVar(&model, "model", "", "with --tier auto: the model the harness runs; found from its environment if empty")
	f.StringVar(&effort, "effort", "", "with --tier auto: the effort or variant, such as low, high or max")
	f.BoolVar(&planner, "planner", false, "allow planning, with --auto")
	f.BoolVar(&realmSwitch, "realm-switch", false, "with --auto: let the session list other realms and move between them")
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
	var harnessID, tierArg, joinModel, joinEffort string
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
			tier, why, err := resolveTier(tierArg, joinModel, joinEffort)
			if err != nil {
				return err
			}
			url, tok, err := issueLocal(cmd.Context(), api.TokenReq{Agent: name, Tier: tier, Planner: plannerFlag})
			if err != nil {
				return err
			}
			// The name is the only thing on stdout, as scripts and the skill read it from there.
			// Why a tier was chosen is worth saying only when it was chosen here.
			if t, _ := harness.ParseTier(tierArg); t == 0 {
				fmt.Fprintf(os.Stderr, "joined at tier %d (%s): %s\n", tier, tierNames[tier], why)
			}
			// What and where this session is, for the dashboards. Only the harness that runs
			// this shell knows more, and it is this process's parent.
			info := procinfo.Self("shell", harnessID, "aiculator-work", version, false)
			if joinModel != "" {
				info.Model = joinModel
			}
			_ = client.New(url, tok).Post(cmd.Context(), "/v1/agent/hello", info, nil)
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
	join.Flags().StringVar(&tierArg, "tier", "auto", "tier: 1 standard, 2 advanced, 3 ultra, or auto to choose from the model and effort")
	join.Flags().StringVar(&joinModel, "model", "", "the model this session runs, for --tier auto; found from the environment if empty")
	join.Flags().StringVar(&joinEffort, "effort", "", "the effort or variant it runs at, such as low, high or max, for --tier auto")
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

	var holdArg string
	wait := &cobra.Command{
		Use:   "wait",
		Short: "Get the next task, or be told to wait or exit",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := agentClient()
			if err != nil {
				return err
			}
			q := api.WaitReq{HoldMs: -1} // Learned by the server from how long calls may run.
			if holdArg != "auto" {
				d, err := time.ParseDuration(holdArg)
				if err != nil {
					return fmt.Errorf("--hold is a duration or auto: %w", err)
				}
				q.HoldMs = d.Milliseconds()
			}
			var g swarm.Grant
			if err := c.Post(cmd.Context(), "/v1/agent/wait", q, &g); err != nil {
				return err
			}
			return printJSON(g)
		},
	}
	wait.Flags().StringVar(&holdArg, "hold", "auto", "how long to hold if no work is available; auto learns how long your harness lets a command run")

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
	usage := &cobra.Command{
		Use:   "usage",
		Short: "Report tokens and cost used, for the task you hold (--task, --lease) or for yourself",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := agentClient()
			if err != nil {
				return err
			}
			var out any
			if err := c.Post(cmd.Context(), "/v1/agent/usage", api.UsageReq{TaskID: taskID, Lease: lease, Tokens: tokens, Cost: cost}, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	usage.Flags().StringVar(&taskID, "task", "", "task id, if the usage was for one you hold")
	usage.Flags().Uint64Var(&lease, "lease", 0, "lease number, with --task")
	usage.Flags().Int64Var(&tokens, "tokens", 0, "tokens used since your last report")
	usage.Flags().Int64Var(&cost, "cost", 0, "cost since your last report, in micro-dollars")

	var reason, until string
	var retryAfter time.Duration
	limit := &cobra.Command{
		Use:   "limit",
		Short: "Say your harness ran out of usage, credit or rate allowance, and until when",
		Long: `Say your harness ran out of usage, credit or rate allowance. What you hold goes back
with no penalty, and wait answers exit until the limit resets, so you stop spending turns.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := agentClient()
			if err != nil {
				return err
			}
			q := api.LimitReq{Reason: reason, RetryAfterSec: int64(retryAfter.Seconds())}
			if until != "" {
				t, err := time.Parse(time.RFC3339, until)
				if err != nil {
					return fmt.Errorf("--until is RFC 3339: %w", err)
				}
				q.Until = t
			}
			var out any
			if err := c.Post(cmd.Context(), "/v1/agent/limit", q, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	limit.Flags().StringVar(&reason, "reason", "usage_limit", "what ran out: usage_limit, credit_exhausted, rate_limited")
	limit.Flags().StringVar(&until, "until", "", "when it resets, in RFC 3339, if your harness said")
	limit.Flags().DurationVar(&retryAfter, "retry-after", 0, "or how long from now")

	estimate := &cobra.Command{
		Use:   "estimate",
		Short: "Predict the tokens a task will take, from finished tasks like it",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := agentClient()
			if err != nil {
				return err
			}
			var est core.Estimate
			if err := c.Get(cmd.Context(), "/v1/tasks/"+taskID+"/estimate", &est); err != nil {
				return err
			}
			return printJSON(est)
		},
	}
	estimate.Flags().StringVar(&taskID, "task", "", "task id")

	var memFiles, memSymbols, memTags []string
	var memK, memBudget int
	recallCmd := &cobra.Command{
		Use:   "recall QUESTION",
		Short: "Recall what other agents learned that bears on a question, or on files with --file",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := agentClient()
			if err != nil {
				return err
			}
			q := url.Values{"q": {strings.Join(args, " ")}, "file": memFiles, "k": {strconv.Itoa(memK)}}
			var out any
			if err := c.Get(cmd.Context(), "/v1/memory?"+q.Encode(), &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	recallCmd.Flags().StringSliceVar(&memFiles, "file", nil, "files you are working on")
	recallCmd.Flags().IntVar(&memK, "k", 5, "how many notes")
	noteCmd := &cobra.Command{
		Use:   "note TEXT",
		Short: "Leave a short note for the other agents about something non-obvious you learned",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := agentClient()
			if err != nil {
				return err
			}
			var out any
			q := api.NoteReq{Text: strings.Join(args, " "), Files: memFiles, Symbols: memSymbols, Tags: memTags, TaskID: taskID}
			if err := c.Post(cmd.Context(), "/v1/memory", q, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	noteCmd.Flags().StringSliceVar(&memFiles, "file", nil, "files it is about")
	noteCmd.Flags().StringSliceVar(&memSymbols, "symbol", nil, "symbols it is about")
	noteCmd.Flags().StringSliceVar(&memTags, "tag", nil, "words to group it by")
	noteCmd.Flags().StringVar(&taskID, "task", "", "the task you learned it on")
	var symbolName string
	codeQ := &cobra.Command{
		Use:   "code [QUESTION]",
		Short: "Ask the shared code graph, or look up one symbol with --symbol",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := agentClient()
			if err != nil {
				return err
			}
			path := "/v1/code?" + url.Values{"q": {strings.Join(args, " ")}, "budget": {strconv.Itoa(memBudget)}}.Encode()
			if symbolName != "" {
				path = "/v1/code/symbol?" + url.Values{"name": {symbolName}, "budget": {strconv.Itoa(memBudget)}}.Encode()
			}
			var a struct {
				Text string `json:"text"`
			}
			if err := c.Get(cmd.Context(), path, &a); err != nil {
				return err
			}
			fmt.Print(a.Text)
			return nil
		},
	}
	codeQ.Flags().StringVar(&symbolName, "symbol", "", "a symbol or file to look up")
	codeQ.Flags().IntVar(&memBudget, "budget", 1500, "most tokens the answer may take")

	cmd.PersistentFlags().StringVar(&agent, "agent", "", "agent name from aiculator work join")
	cmd.AddCommand(join, wait, claim,
		leased("heartbeat", "Extend your lease; this does not count as progress", "/v1/agent/heartbeat"),
		leased("progress", "Save your work; only real file changes count", "/v1/agent/progress"),
		leased("submit", "Submit finished work for verification", "/v1/agent/submit"),
		leased("fail", "Give up; saved work and note go to the next attempt", "/v1/agent/fail"),
		leased("release", "Hand back with no penalty, when the harness cannot do it", "/v1/agent/release"),
		verdict, situation, usage, limit, estimate, recallCmd, noteCmd, codeQ)
	cmd.AddCommand(nextCmds(&agent)...)
	return cmd
}
