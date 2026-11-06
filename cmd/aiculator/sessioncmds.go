package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/realm"
	"github.com/Grano22/aiculator/pkg/observe"
)

func realmStopCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "stop [NAME...]",
		Short: "Stop the servers of the realms named, or of every realm with --all",
		RunE: func(_ *cobra.Command, names []string) error {
			if len(names) == 0 && !all {
				return errors.New("name the realms to stop, or use --all")
			}
			return stopRealms(names)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "stop every realm on this machine")
	return cmd
}

func since(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func where(s observe.SessionLive) string {
	var parts []string
	if s.Host != "" {
		parts = append(parts, s.Host)
	}
	if s.PID > 0 {
		parts = append(parts, fmt.Sprintf("pid %d", s.PID))
	}
	if s.Parent != "" {
		parts = append(parts, "in "+s.Parent)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func printSessions(list []observe.SessionLive) error {
	if len(list) == 0 {
		fmt.Println("no sessions")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tTIER\tKIND\tHARNESS\tSTATE\tTASK\tSEEN\tCONNECTED\tWHERE\tFOLDER")
	for _, s := range list {
		state := s.State
		if s.Limit != "" {
			state += " (" + s.Limit
			if !s.LimitUntil.IsZero() {
				state += " until " + s.LimitUntil.Local().Format("15:04")
			}
			state += ")"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s ago\t%s\t%s\t%s\n", s.Agent, s.Tier, dash(s.Kind), dash(s.Harness), state, dash(s.Task),
			since(s.SeenAgoMs), since(s.ConnectedMs), where(s), dash(s.CWD))
	}
	return tw.Flush()
}

func dash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func sessionsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List the agents connected now: what they run, where, and what they are doing",
		Long: `List the agents connected now: how each connects (acp, mcp, mcp-http or shell), the
harness and model, its host, process and folder, what it is doing, and any usage limit it
reported. A session that has gone quiet is listed as gone, and dropped after five minutes.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var list []observe.SessionLive
			if err := c.Get(cmd.Context(), "/v1/sessions", &list); err != nil {
				return err
			}
			if asJSON {
				return printJSON(list)
			}
			return printSessions(list)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	forget := &cobra.Command{
		Use:   "forget [AGENT...]",
		Short: "Remove sessions from the list: those named, or every one that has gone",
		RunE: func(cmd *cobra.Command, names []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var out struct {
				Forgotten []string `json:"forgotten"`
			}
			if err := c.Post(cmd.Context(), "/v1/sessions/forget", api.StopReq{Agents: names}, &out); err != nil {
				return err
			}
			fmt.Printf("removed %d: %s\n", len(out.Forgotten), strings.Join(out.Forgotten, ", "))
			return nil
		},
	}
	cmd.AddCommand(forget)
	return cmd
}

func agentsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agents", Short: "Stop or kill the agents working on the swarm"}
	var all, realms bool
	run := func(kill bool) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, names []string) error {
			if len(names) == 0 && !all {
				return errors.New("name the agents, or use --all")
			}
			q := api.StopReq{Agents: names, Kill: kill}
			if !realms {
				c, err := adminClient()
				if err != nil {
					return err
				}
				var out api.StopReply
				if err := c.Post(cmd.Context(), "/v1/agents/stop", q, &out); err != nil {
					return err
				}
				printStop("", out, kill)
				return nil
			}
			list, err := realm.List()
			if err != nil {
				return err
			}
			for _, r := range list {
				c, err := realm.Local(r)
				if err != nil {
					continue // Not running.
				}
				var out api.StopReply
				if err := c.Post(cmd.Context(), "/v1/agents/stop", q, &out); err != nil {
					fmt.Printf("%s: %v\n", r.Name, err)
					continue
				}
				printStop(r.Name+": ", out, kill)
			}
			return nil
		}
	}
	stop := &cobra.Command{
		Use:   "stop [AGENT...]",
		Short: "Tell agents to exit, and hand back what they hold",
		Long: `Tell agents to exit. Each gets exit from its next wait, or from the one it is in now,
and what it holds goes back to the queue at once with no penalty. Its process keeps running
until it reads the exit; kill also ends the processes aiculator started.`,
		RunE: run(false),
	}
	kill := &cobra.Command{
		Use:   "kill [AGENT...]",
		Short: "Stop agents, and end the processes aiculator started for them",
		Long: `Stop agents, as stop does, and end the processes that aiculator itself started for
them on this machine: aiculator drive and the harness it runs. A harness you started
yourself, such as an open Claude Code session, is told to exit and never killed.`,
		RunE: run(true),
	}
	for _, c := range []*cobra.Command{stop, kill} {
		c.Flags().BoolVar(&all, "all", false, "every agent")
		c.Flags().BoolVar(&realms, "realms", false, "in every running realm on this machine, not just this project")
	}
	cmd.AddCommand(stop, kill)
	return cmd
}

func printStop(prefix string, out api.StopReply, kill bool) {
	fmt.Printf("%sstopped %d", prefix, len(out.Stopped))
	if kill {
		fmt.Printf(", killed %d", len(out.Killed))
	}
	if len(out.Released) > 0 {
		fmt.Printf(", handed back %s", strings.Join(out.Released, ", "))
	}
	fmt.Println()
}
