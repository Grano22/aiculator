package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/pkg/observe"
	"github.com/Grano22/aiculator/pkg/plugin"
)

// startPlugins runs the compiled-in plugins inside the server.
func startPlugins(ctx context.Context, srv *api.Server, mux *http.ServeMux, log *slog.Logger) (func(), error) {
	m := plugin.Start(plugin.All(), plugin.Config{
		Router:  mux,
		Guard:   srv.ViewerOnly,
		Version: version,
		Log:     log,
		Snapshot: func(ctx context.Context) (observe.Snapshot, error) {
			return srv.S.Snapshot(ctx, version)
		},
	})
	mux.Handle("GET /v1/plugins", srv.ViewerOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		api.WriteJSON(w, m.Statuses())
	})))
	tailCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := srv.S.Store.TailEvents(tailCtx, func(t core.Task) {
			m.Publish(plugin.Event{TaskID: t.ID, Title: t.Title, State: string(t.State), Tier: t.Tier, Owner: t.Owner, Reason: t.Reason, Time: t.UpdatedAt})
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error("plugin event stream stopped", "err", err)
		}
	}()
	return func() {
		cancel()
		<-done
		m.Stop()
	}, nil
}

// pluginCLI gives plugin commands what they need, without handing them the admin token.
func pluginCLI() *plugin.CLI {
	return &plugin.CLI{
		ViewerToken: func(ctx context.Context, ttl time.Duration) (string, string, error) {
			name := "viewer-" + randomSuffix()
			url, tok, err := issueLocal(ctx, api.TokenReq{Agent: name, Viewer: true, TTLSec: int64(ttl.Seconds())})
			return url, tok, err
		},
		Snapshot: func(ctx context.Context) (observe.Snapshot, error) {
			var s observe.Snapshot
			c, err := adminClient()
			if err != nil {
				return s, fmt.Errorf("%w; start it with: aiculator up", err)
			}
			return s, c.Get(ctx, "/v1/observe", &s)
		},
	}
}

func addPluginCommands(root *cobra.Command) {
	cli := pluginCLI()
	for _, p := range plugin.All() {
		c, ok := p.(plugin.Commander)
		if !ok {
			continue
		}
		for _, sub := range c.Commands(cli) {
			if _, _, err := root.Find([]string{sub.Name()}); err == nil && sub.Name() != "" && commandExists(root, sub.Name()) {
				fmt.Fprintf(os.Stderr, "plugin %s: command %q is already taken, so it was skipped\n", p.Name(), sub.Name())
				continue
			}
			root.AddCommand(sub)
		}
	}
}

func commandExists(root *cobra.Command, name string) bool {
	return slices.ContainsFunc(root.Commands(), func(c *cobra.Command) bool { return c.Name() == name || c.HasAlias(name) })
}

const externalPrefix = "aiculator-"

// externalPlugins lists programs on PATH that work as commands.
func externalPlugins() map[string]string {
	out := map[string]string{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name, ok := strings.CutPrefix(e.Name(), externalPrefix)
			name = strings.TrimSuffix(name, ".exe")
			if !ok || name == "" {
				continue
			}
			path := filepath.Join(dir, e.Name())
			fi, err := os.Stat(path)
			if err != nil || fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
				continue
			}
			if _, seen := out[name]; !seen { // PATH order decides, as it does for any command.
				out[name] = path
			}
		}
	}
	return out
}

// runExternal runs aiculator-NAME when NAME is not a built-in command. Built-ins always
// win, so a program on PATH can never replace one. Only PATH is searched, never the
// project, so cloning a repository cannot add a command.
func runExternal(root *cobra.Command, args []string) (handled bool, code int) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || args[0] == "help" || commandExists(root, args[0]) {
		return false, 0
	}
	path, err := exec.LookPath(externalPrefix + args[0])
	if err != nil {
		return false, 0
	}
	cmd := exec.Command(path, args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	data, _ := filepath.Abs(findDataDir())
	cmd.Env = append(os.Environ(), "AICULATOR_DATA="+data, "AICULATOR_VERSION="+version, "AICULATOR_V="+os.Getenv("AICULATOR_V"))
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return true, ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return true, 1
	}
	return true, 0
}

func pluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "plugin",
		Aliases: []string{"plugins"},
		Short:   "List plugins, both compiled in and on PATH",
		Long: `Plugins compiled into this binary add commands, server routes and event handlers.
Build one in with a blank import in cmd/aiculator; the web dashboard is one, and go build
-tags noweb leaves it out.

Any program on PATH named aiculator-NAME also runs as aiculator NAME.`,
		RunE: func(*cobra.Command, []string) error {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tKIND\tDESCRIPTION")
			for _, p := range plugin.All() {
				fmt.Fprintf(w, "%s\tcompiled in\t%s\n", p.Name(), p.Description())
			}
			ext := externalPlugins()
			names := make([]string, 0, len(ext))
			for n := range ext {
				names = append(names, n)
			}
			slices.Sort(names)
			for _, n := range names {
				fmt.Fprintf(w, "%s\ton PATH\t%s\n", n, ext[n])
			}
			return w.Flush()
		},
	}
	return cmd
}
