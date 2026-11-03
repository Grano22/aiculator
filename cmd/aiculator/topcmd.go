package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Grano22/aiculator/internal/tui"
	"github.com/Grano22/aiculator/internal/vlog"
	"github.com/Grano22/aiculator/pkg/observe"
)

func topCmd() *cobra.Command {
	var (
		interval     time.Duration
		once, asJSON bool
		noColor      bool
		forceColor   bool
		width        int
		viewName     string
	)
	cmd := &cobra.Command{
		Use:     "top",
		Aliases: []string{"dash", "dashboard"},
		Short:   "Live terminal dashboard: flow, throughput, latency, jitter, probes, liveness and usage",
		Long: `A live view of the swarm. Keys: 1-5 or tab switch views, arrows or j and k scroll,
p pauses, q quits.

Without a terminal, such as in a pipe, it prints one frame and exits.
Rates reset when the server restarts. Task states and usage totals do not.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
			if err != nil {
				return fmt.Errorf("%w; start it with: aiculator up", err)
			}
			fetch := func(ctx context.Context) (observe.Snapshot, error) {
				var s observe.Snapshot
				err := c.Get(ctx, "/v1/observe", &s)
				return s, err
			}
			views := map[string]tui.View{"overview": tui.Overview, "tasks": tui.Tasks, "agents": tui.Agents, "flow": tui.Flow, "usage": tui.Usage}
			view, ok := views[viewName]
			if !ok {
				return fmt.Errorf("unknown view %q: use overview, tasks, agents, flow or usage", viewName)
			}
			if asJSON {
				s, err := fetch(cmd.Context())
				if err != nil {
					return err
				}
				return printJSON(s)
			}
			color := !noColor && os.Getenv("NO_COLOR") == ""
			tty := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
			if once || !tty {
				s, err := fetch(cmd.Context())
				if err != nil {
					return err
				}
				w, _ := tui.ScreenSize(term.GetSize(int(os.Stdout.Fd())))
				if width > 0 {
					w = width
				}
				fmt.Println(tui.Render(s, tui.Options{View: view, Width: w, Height: 60, Color: forceColor || (color && tty), Plain: true}))
				return nil
			}
			// A log line on stderr would corrupt the screen, so logs go only to a file.
			if logFile == "" {
				vlog.SetOutput(io.Discard, logFormat)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return tui.Run(ctx, fetch, interval, color)
		},
	}
	f := cmd.Flags()
	f.DurationVar(&interval, "interval", time.Second, "how often to refresh")
	f.BoolVar(&once, "once", false, "print one frame and exit")
	f.BoolVar(&asJSON, "json", false, "print the raw snapshot as JSON and exit")
	f.BoolVar(&noColor, "no-color", false, "no color; NO_COLOR also works")
	f.BoolVar(&forceColor, "color", false, "with --once: keep color even in a pipe")
	f.IntVar(&width, "width", 0, "with --once: width in columns")
	f.StringVar(&viewName, "view", "overview", "with --once: overview, tasks, agents, flow or usage")
	return cmd
}

// eventsCmd streams every committed task change as JSON lines, so that any program in
// any language can follow the swarm without being compiled in.
func eventsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "events",
		Short: "Stream every task change as JSON lines",
		Example: `  aiculator events
  aiculator events | jq -r 'select(.state == "done") | .id'`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/v1/events", nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+c.Token)
			resp, err := c.HTTP.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("server answered %s", resp.Status)
			}
			_, err = io.Copy(os.Stdout, resp.Body)
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
}
