// Command swarm runs the agent swarm: a single static binary with the embedded
// state store, the HTTP and MCP endpoints, the ACP driver and the CLI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/auth"
	"github.com/Grano22/aiculator/internal/client"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/memory"
	"github.com/Grano22/aiculator/internal/procinfo"
	"github.com/Grano22/aiculator/internal/realm"
	"github.com/Grano22/aiculator/internal/sandbox"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/swarm"
	"github.com/Grano22/aiculator/internal/tui"
	"github.com/Grano22/aiculator/internal/vlog"
	"github.com/Grano22/aiculator/internal/work"
)

var dataDir string

// dataDirFlagSet means --data was given, so nothing should search for the state directory.
var dataDirFlagSet bool

// realmFlag names the realm to act on, instead of the one this folder belongs to.
var realmFlag string

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

// findDataDir walks up from the working directory, so that commands work from a subdirectory
// and from inside a task's worktree, which lives under the data directory.
func findDataDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ".swarm"
	}
	return findDataDirFrom(dir)
}

// findDataDirFrom stops at the edge of the project. Without that, a repository nested inside
// another would silently use the outer one's server, and an agent would be given work from
// the wrong project. The home directory is an edge too, so that a stray ~/.swarm cannot
// claim every folder under it.
func findDataDirFrom(dir string) string {
	sep := string(filepath.Separator)
	// A task's worktree has its own .git, and it belongs to the data directory it is inside.
	if i := strings.LastIndex(dir+sep, sep+".swarm"+sep); i >= 0 {
		return dir[:i] + sep + ".swarm"
	}
	home, _ := os.UserHomeDir()
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	start := dir
	for {
		if dir == home && start != home {
			return ".swarm" // Working in the home directory itself is the one way to use it.
		}
		candidate := filepath.Join(dir, ".swarm")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return candidate
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return ".swarm"
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ".swarm"
		}
		dir = parent
	}
}

func main() {
	root := rootCmd()
	if handled, code := runExternal(root, os.Args[1:]); handled {
		os.Exit(code)
	}
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use: "aiculator", Short: "Run and control an agent swarm across coding harnesses",
		Version: version, SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			dataDirFlagSet = cmd.Flags().Changed("data")
			if realmFlag != "" {
				if dataDirFlagSet {
					return errors.New("use --realm or --data, not both")
				}
				r, err := realm.Get(realmFlag)
				if err != nil {
					return err
				}
				// Everything then acts as if it had been run in that project.
				if err := os.Chdir(r.Path); err != nil {
					return fmt.Errorf("realm %s: %w", r.Name, err)
				}
			}
			if !dataDirFlagSet {
				dataDir = findDataDir()
			}
			return setupLogging(cmd)
		},
	}
	root.PersistentFlags().StringVar(&realmFlag, "realm", "", "act on this realm, from any folder; see aiculator realm ls")
	root.PersistentFlags().StringVar(&dataDir, "data", ".swarm", "state directory; found by searching upward when not set")
	addLoggingFlags(root)
	// Declared here so that cobra does not claim -v for it, since -v is the verbosity.
	root.Flags().Bool("version", false, "print the version")
	root.AddCommand(memoryCmd(), codeCmd(), agentsCmd(), sessionsCmd(), backupCmd(), doctorCmd(), exportCmd(), parallelCmd(), realmCmd(), initCmd(), importCmd(), sourceCmd(), syncCmd(), serveCmd(), upCmd(), downCmd(), taskCmd(), tokenCmd(), situationCmd(), planCmd(),
		mcpCmd(), driveCmd(), joinCmd(), unjoinCmd(), workCmd(), harnessCmd(), skillCmd(), templateCmd(), topCmd(), eventsCmd(), pluginCmd(), debugCmd(), logsCmd(), genDocsCmd())
	addPluginCommands(root)
	return root
}

func serveCmd() *cobra.Command {
	var (
		addr, repo, branch, sb string
		syncAlways             bool
		leaseTTL, progressTTL  time.Duration
		exitGrace              time.Duration
		restartGrace           time.Duration
		agingUnit              time.Duration
		agingArgs              []string
		standby                bool
		codeGraph              string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the swarm server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			aging, err := core.ParseAging(agingArgs)
			if err != nil {
				return err
			}
			return serve(ctx, serveOpts{addr, repo, branch, sb, syncAlways, leaseTTL, progressTTL, exitGrace, restartGrace, agingUnit, aging, standby, codeGraph})
		},
	}
	f := cmd.Flags()
	f.StringVar(&addr, "addr", "127.0.0.1:7777", "listen address; port 0 picks a free one")
	f.StringVar(&repo, "repo", "", "git repository to work on; empty disables the work layer")
	f.StringVar(&branch, "branch", work.DefaultBranch, "branch that receives merged work")
	f.StringVar(&sb, "sandbox", "auto", "sandbox: auto, seatbelt, bwrap, docker or none")
	f.BoolVar(&syncAlways, "sync-always", false, "fsync every write; survives power loss but is slower")
	f.DurationVar(&leaseTTL, "lease-ttl", 2*time.Minute, "lease length")
	f.DurationVar(&progressTTL, "progress-ttl", 10*time.Minute, "time without file changes before an agent counts as a zombie")
	f.DurationVar(&exitGrace, "exit-grace", 30*time.Second, "quiet time before agents are told to exit")
	f.DurationVar(&restartGrace, "restart-grace", 15*time.Second, "no lease expires this soon after a restart")
	f.DurationVar(&agingUnit, "aging-unit", time.Minute, "waiting this long equals one priority point")
	f.StringSliceVar(&agingArgs, "aging", nil, "raise priority after a wait, as wait=boost, such as 1h=5,4h=20")
	f.BoolVar(&standby, "standby", false, "if another server holds this data directory, wait and take over when it stops")
	f.StringVar(&codeGraph, "code-graph", memory.ModeAuto, "shared code graph for agents: auto (Graphify if installed, else built in), graphify, builtin or off")
	return cmd
}

type serveOpts struct {
	addr, repo, branch, sb string
	syncAlways             bool
	leaseTTL, progressTTL  time.Duration
	exitGrace              time.Duration
	restartGrace           time.Duration
	agingUnit              time.Duration
	aging                  []core.AgingStep
	standby                bool
	codeGraph              string
}

func serve(ctx context.Context, o serveOpts) error {
	log := vlog.Slog("server")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	// The design relies on a single writer, so a second server must not start here, unless
	// it is a standby, which waits for the first to go.
	unlock, term, err := lead(ctx, o.standby, log)
	if err != nil {
		if o.standby && ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer unlock()
	authority, err := auth.LoadOrCreate(filepath.Join(dataDir, "signing.key"))
	if err != nil {
		return err
	}
	admin, err := authority.Issue(auth.Claims{Agent: "admin", Admin: true})
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(dataDir, "admin.token"), []byte(admin), 0o600); err != nil {
		return err
	}
	st, err := store.Open(ctx, store.Options{Dir: filepath.Join(dataDir, "nats"), SyncAlways: o.syncAlways})
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.StartProjector(ctx); err != nil {
		return err
	}

	var wm *work.Manager
	if o.repo != "" {
		sb, err := sandbox.Open(o.sb)
		if err != nil {
			return err
		}
		repo, err := filepath.Abs(o.repo)
		if err != nil {
			return err
		}
		if wm, err = work.New(ctx, work.Options{Repo: repo, Root: filepath.Join(dataDir, "worktrees"), Branch: o.branch, Sandbox: sb, Hide: secretPaths()}); err != nil {
			return err
		}
		log.Info("work layer ready", "repo", repo, "branch", wm.Branch(), "sandbox", sb.Name())
	}

	realmID := registerRealm(log)
	cfg := engine.Config{LeaseTTL: o.leaseTTL, ProgressTTL: o.progressTTL, ExitGrace: o.exitGrace, RestartGrace: o.restartGrace, AgingUnit: o.agingUnit, Aging: o.aging}
	svc, err := swarm.New(ctx, swarm.Options{Store: st, Work: wm, Engine: cfg, Log: log, Realm: realmID.name, RealmPath: realmID.path})
	if err != nil {
		return err
	}
	defer svc.Close()

	srv := &api.Server{S: svc, Auth: authority, Version: version, Instance: realmID.instance, Term: term,
		Host: procinfo.Host(), Kill: procinfo.Terminate}
	// Connectors are a side effect, so a bad sources file costs them, not the swarm.
	if y := startSync(ctx, svc, o.repo, log); y != nil {
		srv.Sync = y
		defer y.Close()
	}
	// Backups read the store, so they stop before it closes, which is deferred earlier.
	if b := startBackups(ctx, st, o.repo, log); b != nil {
		srv.Backup = b
		defer b.Close()
	}
	// Shared memory reads the store and git beside the swarm, so it stops before the store.
	if mem := startMemory(ctx, st, wm, o.codeGraph, log); mem != nil {
		srv.Memory = memoryAPI{mem}
		defer mem.Close()
	}
	mux := srv.Routes()
	mux.Handle("/mcp", srv.MCPHandler())
	stopPlugins, err := startPlugins(ctx, srv, mux, log)
	if err != nil {
		return err
	}
	defer stopPlugins()
	ln, err := net.Listen("tcp", o.addr)
	if err != nil && o.standby {
		// The old leader's port may still be taken by something else after it died. Clients
		// read the address file, so a new port only costs open sessions a reconnect.
		log.Warn("leader address taken, picking another", "addr", o.addr, "err", err)
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: srv.Instrument(mux), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	url := "http://" + ln.Addr().String()
	if err := atomicWrite(filepath.Join(dataDir, "addr"), []byte(url), 0o600); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(dataDir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(dataDir, "pid"))
	log.Info("swarm listening", "url", url)

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(shut)
	return nil
}

// secretPaths lists what sandboxed commands may not read. With the admin token or
// signing key, an agent could mint any identity and approve its own work.
func secretPaths() []string {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		abs = dataDir
	}
	paths := sandbox.SecretHome()
	for _, name := range []string{"admin.token", "signing.key", "agents", "nats"} {
		paths = append(paths, filepath.Join(abs, name))
	}
	return paths
}

func atomicWrite(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func adminClient() (*client.Client, error) {
	url, err := os.ReadFile(filepath.Join(dataDir, "addr"))
	if err != nil {
		return nil, fmt.Errorf("no running server found in %s: %w", dataDir, err)
	}
	tok, err := os.ReadFile(filepath.Join(dataDir, "admin.token"))
	if err != nil {
		return nil, err
	}
	return client.New(string(url), string(tok)), nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func estimateCmd() *cobra.Command {
	var spec engine.TaskSpec
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "estimate [ID]",
		Short: "Predict the tokens a task will take, from finished tasks like it",
		Long: `Predict the tokens a task will take, from finished tasks like it: same tier, shared
tags and labels, and words in common. Tokens count every try. With no ID, describe a task
you are thinking of adding with --title, --spec, --tier, --tag and --label.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var est core.Estimate
			if len(args) == 1 {
				err = c.Get(cmd.Context(), "/v1/tasks/"+args[0]+"/estimate", &est)
			} else {
				if spec.Title == "" {
					return errors.New("name a task, or describe one with --title")
				}
				err = c.Post(cmd.Context(), "/v1/estimate", spec, &est)
			}
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(est)
			}
			if est.Method == "none" {
				fmt.Println("no finished task has reported tokens yet, so there is nothing to go on")
				return nil
			}
			fmt.Printf("about %s tokens (most like it took %s to %s), from %d finished tasks (%s)\n",
				tui.Count(est.Tokens), tui.Count(est.Low), tui.Count(est.High), est.Basis, est.Method)
			if len(est.Similar) > 0 {
				fmt.Println("most like it: " + strings.Join(est.Similar, ", "))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&spec.Title, "title", "", "title of a task not added yet")
	f.StringVar(&spec.Spec, "spec", "", "its spec")
	f.IntVar(&spec.Tier, "tier", 1, "its tier")
	f.StringSliceVar(&spec.Tags, "tag", nil, "its tags")
	f.StringToStringVar(&spec.Labels, "label", nil, "its labels, as key=value")
	f.BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func taskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Manage tasks"}
	var s engine.TaskSpec
	var labelArgs, agingArgs []string
	add := &cobra.Command{
		Use:   "add",
		Short: "Add a task",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			if s.Labels, err = parseLabels(labelArgs); err != nil {
				return err
			}
			if s.Aging, err = core.ParseAging(agingArgs); err != nil {
				return err
			}
			if !cmd.Flags().Changed("aging") {
				s.Aging = nil // No flag means the server's steps apply, not "none".
			}
			c, err := adminClient()
			if err != nil {
				return err
			}
			var t core.Task
			if err := c.Post(cmd.Context(), "/v1/tasks", s, &t); err != nil {
				return err
			}
			fmt.Println(t.ID)
			return nil
		},
	}
	f := add.Flags()
	f.StringVar(&s.ID, "id", "", "task id; generated if empty")
	f.StringVar(&s.Title, "title", "", "short title")
	f.StringVar(&s.Spec, "spec", "", "full instructions")
	f.IntVar(&s.Tier, "tier", 1, "tier: 1 standard, 2 advanced, 3 ultra")
	f.StringSliceVar(&s.Tags, "tag", nil, "required capability tag")
	f.IntVar(&s.Priority, "priority", 0, "higher runs sooner")
	f.StringArrayVar(&labelArgs, "label", nil, "label as name=value")
	f.StringSliceVar(&agingArgs, "aging", nil, "raise priority after a wait, as wait=boost, such as 1h=5,4h=20")
	f.StringSliceVar(&s.Deps, "dep", nil, "task id this depends on")
	f.StringSliceVar(&s.Touches, "touches", nil, "path this task will change")
	f.StringArrayVar(&s.Checks, "check", nil, "acceptance command")
	f.BoolVar(&s.RequireReview, "review", false, "also require an independent agent review")
	f.BoolVar(&s.AllowEmpty, "allow-empty", false, "allow a submission that changes no files")
	f.Int64Var(&s.Budget, "budget", 0, "budget in micro-dollars; 0 means no limit")

	ls := &cobra.Command{
		Use:   "ls",
		Short: "List tasks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var ts []core.Task
			if err := c.Get(cmd.Context(), "/v1/tasks", &ts); err != nil {
				return err
			}
			fmt.Printf("%-24s %-9s %-4s %-5s %-14s %s\n", "ID", "STATE", "TIER", "TRIES", "OWNER", "TITLE")
			for _, t := range ts {
				fmt.Printf("%-24s %-9s %-4d %-5d %-14s %s\n", t.ID, t.State, t.Tier, t.Attempt, t.Owner, t.Title)
			}
			return nil
		},
	}
	show := &cobra.Command{
		Use:   "show ID",
		Short: "Show one task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var t core.Task
			if err := c.Get(cmd.Context(), "/v1/tasks/"+args[0], &t); err != nil {
				return err
			}
			return printJSON(t)
		},
	}
	var note string
	var addBudget int64
	cancelCmd := &cobra.Command{
		Use:   "cancel ID",
		Short: "Cancel a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			return c.Post(cmd.Context(), "/v1/tasks/"+args[0]+"/cancel", map[string]string{"note": note}, nil)
		},
	}
	cancelCmd.Flags().StringVar(&note, "note", "", "reason")
	unpark := &cobra.Command{
		Use:   "unpark ID",
		Short: "Return a parked task to the queue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			return c.Post(cmd.Context(), "/v1/tasks/"+args[0]+"/unpark", map[string]any{"note": note, "add_budget": addBudget}, nil)
		},
	}
	unpark.Flags().StringVar(&note, "note", "", "reason")
	unpark.Flags().Int64Var(&addBudget, "add-budget", 0, "budget to add, in micro-dollars")
	cmd.AddCommand(add, ls, show, cancelCmd, unpark, amendCmd(), priorityCmd(), estimateCmd())
	cmd.AddCommand(metaCmds()...)
	return cmd
}

func tokenCmd() *cobra.Command {
	var q api.TokenReq
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Issue an agent token",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var out map[string]string
			if err := c.Post(cmd.Context(), "/v1/tokens", q, &out); err != nil {
				return err
			}
			fmt.Println(out["token"])
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&q.Agent, "agent", "", "agent name")
	f.IntVar(&q.Tier, "tier", 1, "tier")
	f.StringSliceVar(&q.Tags, "tag", nil, "capability tag")
	f.BoolVar(&q.Planner, "planner", false, "allow this agent to plan")
	f.Int64Var(&q.TTLSec, "ttl", 0, "lifetime in seconds; 0 never expires")
	return cmd
}

func situationCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "situation",
		Short: "Show the state of the swarm",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
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
}

func planCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "plan", Short: "Drive the planner lease by hand"}
	var q api.PlanReq
	var ttl time.Duration
	var expected []string
	post := func(path string) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			q.TTLMs = ttl.Milliseconds()
			if len(expected) > 0 {
				q.Expected = map[int]int{}
				for _, kv := range expected {
					var tier, n int
					if _, err := fmt.Sscanf(strings.Replace(kv, "=", " ", 1), "%d %d", &tier, &n); err != nil {
						return fmt.Errorf("--expected %q: want TIER=COUNT", kv)
					}
					q.Expected[tier] = n
				}
			}
			var out any
			if err := c.Post(cmd.Context(), path, q, &out); err != nil {
				return err
			}
			return printJSON(out)
		}
	}
	begin := &cobra.Command{Use: "begin", Short: "Take the planner lease", RunE: post("/v1/plan/begin")}
	begin.Flags().DurationVar(&ttl, "ttl", time.Minute, "lease length")
	update := &cobra.Command{Use: "update", Short: "Publish hints to waiting agents", RunE: post("/v1/plan/update")}
	update.Flags().Uint64Var(&q.Epoch, "epoch", 0, "planning epoch")
	update.Flags().DurationVar(&ttl, "ttl", time.Minute, "lease length")
	update.Flags().StringVar(&q.Phase, "phase", "", "what the planner is doing")
	update.Flags().StringArrayVar(&expected, "expected", nil, "tasks expected, as TIER=COUNT")
	update.Flags().Int64Var(&q.ETAMs, "eta-ms", 0, "estimated time to commit")
	commit := &cobra.Command{Use: "commit", Short: "Publish the plan", RunE: post("/v1/plan/commit")}
	commit.Flags().Uint64Var(&q.Epoch, "epoch", 0, "planning epoch")
	add := &cobra.Command{Use: "add", Short: "Add a draft task to the plan", RunE: post("/v1/plan/add")}
	add.Flags().Uint64Var(&q.Epoch, "epoch", 0, "planning epoch")
	add.Flags().StringVar(&q.Task.ID, "id", "", "task id; generated if empty")
	add.Flags().StringVar(&q.Task.Title, "title", "", "short title")
	add.Flags().StringVar(&q.Task.Spec, "spec", "", "full instructions")
	add.Flags().IntVar(&q.Task.Tier, "tier", 1, "tier: 1 standard, 2 advanced, 3 ultra")
	add.Flags().IntVar(&q.Task.Priority, "priority", 0, "higher runs sooner")
	add.Flags().StringSliceVar(&q.Task.Deps, "dep", nil, "task id this depends on")
	add.Flags().StringSliceVar(&q.Task.Tags, "tag", nil, "required capability tag")
	add.Flags().StringSliceVar(&q.Task.Touches, "touches", nil, "path this task will change")
	add.Flags().StringArrayVar(&q.Task.Checks, "check", nil, "acceptance command")
	add.Flags().BoolVar(&q.Task.RequireReview, "review", false, "also require an independent agent review")
	cmd.AddCommand(begin, update, add, commit)
	return cmd
}
