package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Grano22/aiculator/internal/realm"
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
		all, live    bool
	)
	cmd := &cobra.Command{
		Use:     "top",
		Aliases: []string{"dash", "dashboard"},
		Short:   "Live terminal dashboard: flow, throughput, latency, jitter, probes, liveness and usage",
		Long: `A live view of the swarm. Keys: 1-5 or tab switch views, arrows or j and k scroll,
r turns live mode on and off, p pauses, q quits. Live mode redraws four times a second, so
a change shows as it happens; --live starts in it.

Without a terminal, such as in a pipe, it prints one frame and exits.
Rates reset when the server restarts. Task states and usage totals do not.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all {
				return topAll(cmd, interval, once, asJSON, noColor, forceColor, width, viewName)
			}
			c, err := adminClient()
			if err != nil {
				return fmt.Errorf("%w; start it with: aiculator up", err)
			}
			fetch := func(ctx context.Context) (observe.Snapshot, error) {
				var s observe.Snapshot
				err := c.Get(ctx, "/v1/observe", &s)
				return s, err
			}
			views := map[string]tui.View{"overview": tui.Overview, "tasks": tui.Tasks, "sessions": tui.Agents, "agents": tui.Agents, "flow": tui.Flow, "usage": tui.Usage}
			view, ok := views[viewName]
			if !ok {
				return fmt.Errorf("unknown view %q: use overview, tasks, sessions, flow or usage", viewName)
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
			return tui.Run(ctx, fetch, interval, color, live)
		},
	}
	f := cmd.Flags()
	f.DurationVar(&interval, "interval", time.Second, "how often to refresh")
	f.BoolVar(&all, "all", false, "show every realm on this machine: 1 realms, 2 tasks, 3 agents")
	f.BoolVar(&once, "once", false, "print one frame and exit")
	f.BoolVar(&asJSON, "json", false, "print the raw snapshot as JSON and exit")
	f.BoolVar(&noColor, "no-color", false, "no color; NO_COLOR also works")
	f.BoolVar(&forceColor, "color", false, "with --once: keep color even in a pipe")
	f.IntVar(&width, "width", 0, "with --once: width in columns")
	f.StringVar(&viewName, "view", "overview", "with --once: overview, tasks, sessions, flow or usage")
	f.BoolVar(&live, "live", false, "start in live mode, redrawing four times a second; r toggles it")
	return cmd
}

func topAll(cmd *cobra.Command, interval time.Duration, once, asJSON, noColor, forceColor bool, width int, viewName string) error {
	if asJSON {
		views, err := realm.Gather(cmd.Context(), nil)
		if err != nil {
			return err
		}
		return printJSON(views)
	}
	view := tui.View(0)
	switch viewName {
	case "overview", "realms":
	case "tasks":
		view = 1
	case "agents":
		view = 2
	default:
		return fmt.Errorf("unknown view %q with --all: use realms, tasks or agents", viewName)
	}
	color := !noColor && os.Getenv("NO_COLOR") == ""
	tty := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	if once || !tty {
		rows, err := fleet(cmd.Context())
		if err != nil {
			return err
		}
		w, _ := tui.ScreenSize(term.GetSize(int(os.Stdout.Fd())))
		if width > 0 {
			w = width
		}
		fmt.Println(tui.RenderFleet(rows, tui.Options{View: view, Width: w, Height: 60, Color: forceColor || (color && tty), Plain: true}))
		return nil
	}
	if logFile == "" {
		vlog.SetOutput(io.Discard, logFormat)
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return tui.RunFleet(ctx, fleet, interval, color)
}

// eventsCmd streams every committed task change as JSON lines, so that any program in
// any language can follow the swarm without being compiled in.
func eventsCmd() *cobra.Command {
	var since uint64
	var follow bool
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Stream every task change as JSON lines",
		Long: `Stream every task change as JSON lines. Each line has a seq. --since SEQ starts after
that one, so a consumer that saves the last seq it handled misses nothing when it starts
again. With --follow, a dropped connection is resumed from the last line printed, for as
long as this runs. Delivery is at least once: after a crash, dedupe by seq.`,
		Example: `  aiculator events
  aiculator events --since 1200 --follow
  aiculator events | jq -r 'select(.state == "done") | .id'`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			last := since
			for {
				err := streamOnce(ctx, last, func(line []byte, seq uint64) {
					_, _ = os.Stdout.Write(append(line, '\n'))
					if seq > last {
						last = seq
					}
				})
				if ctx.Err() != nil {
					return nil
				}
				if !follow {
					return err
				}
				fmt.Fprintf(os.Stderr, "events: %v; resuming after %d\n", err, last)
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(time.Second):
				}
			}
		},
	}
	cmd.Flags().Uint64Var(&since, "since", 0, "start after this event sequence; 0 means from now")
	cmd.Flags().BoolVar(&follow, "follow", false, "reconnect after a drop, resuming from the last event printed")
	return cmd
}

func streamOnce(ctx context.Context, since uint64, fn func(line []byte, seq uint64)) error {
	c, err := adminClient()
	if err != nil {
		return err
	}
	url := c.URL + "/v1/events"
	if since > 0 {
		url += "?since=" + strconv.FormatUint(since, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var head struct {
			Seq uint64 `json:"seq"`
		}
		_ = json.Unmarshal(sc.Bytes(), &head)
		fn(sc.Bytes(), head.Seq)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return errors.New("the stream ended")
}
