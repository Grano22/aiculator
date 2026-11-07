package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/agentd"
	"github.com/Grano22/aiculator/internal/vlog"
)

// Exit codes of aiculator work next, so a skill or a script can branch without reading JSON.
const (
	nextTask  = 0
	nextAgain = 3
	nextExit  = 4
)

// exitCode ends the process with a code once the command has printed what it had to.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func agentsDir() string { return filepath.Join(dataDir, "agents") }

// ensureHelper starts the agent's helper if none answers, and waits until it does.
func ensureHelper(ctx context.Context, agent string, budget int64) error {
	var st map[string]any
	if err := agentd.Call(ctx, agentsDir(), agent, "GET", "/status", nil, &st); err == nil {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	args := []string{"work", "daemon", "--data", abs, "--agent", agent, "--cwd", cwd}
	if budget > 0 {
		args = append(args, "--token-budget", fmt.Sprint(budget))
	}
	logFile, err := os.OpenFile(filepath.Join(abs, "agents", agent+".daemon.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(self, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	for range 100 {
		if err := agentd.Call(ctx, agentsDir(), agent, "GET", "/status", nil, &st); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("the helper for %s did not start; see %s", agent, logFile.Name())
}

func checkAgent(agent string) error {
	if agent == "" {
		return errors.New("--agent is required; aiculator work join prints your name")
	}
	if filepath.Base(agent) != agent || strings.HasPrefix(agent, ".") {
		return fmt.Errorf("invalid agent name %q", agent)
	}
	if _, err := os.Stat(filepath.Join(agentsDir(), agent+".token")); err != nil {
		return fmt.Errorf("agent %s is not registered; run aiculator work join", agent)
	}
	return nil
}

func nextCmds(agent *string) []*cobra.Command {
	var waitArg string
	var budget int64
	next := &cobra.Command{
		Use:   "next",
		Short: "Wait for the next task in a helper process, at no cost to the model, and print it",
		Long: `Wait for the next task and print it. The waiting happens in a small helper process that
keeps the connection to the swarm, so the model spends nothing while it waits: one call per
task, instead of one call every half minute.

It returns when there is a task (exit 0), when the swarm says to stop (exit 4), or just before
your harness would cut the command off (exit 3: run it again). How long that is, it learns
from your harness; --wait sets it instead. If the harness cuts it off anyway, nothing is lost:
the helper keeps the task, and the next call returns it at once.

While you work, the helper keeps your lease, saves your progress, and reports the tokens your
harness's own log says the session used. Finish with aiculator work done.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkAgent(*agent); err != nil {
				return err
			}
			if err := ensureHelper(cmd.Context(), *agent, budget); err != nil {
				return err
			}
			var max time.Duration
			if waitArg != "auto" {
				d, err := time.ParseDuration(waitArg)
				if err != nil {
					return fmt.Errorf("--wait is a duration or auto: %w", err)
				}
				max = d
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			var rep agentd.Reply
			err := agentd.Call(ctx, agentsDir(), *agent, "POST", "/next", map[string]int64{"max_ms": max.Milliseconds()}, &rep)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(rep, "", "  ")
			fmt.Println(string(b))
			switch rep.Action {
			case "task":
				return nil
			case "again":
				return exitCode(nextAgain)
			}
			return exitCode(nextExit)
		},
	}
	next.Flags().StringVar(&waitArg, "wait", "auto", "longest to wait before returning to run again; auto learns it from your harness")
	next.Flags().Int64Var(&budget, "token-budget", 0, "tokens this session may spend; a task expected to cost more than is left is handed back. Set when the helper starts")

	var note string
	var pass bool
	done := &cobra.Command{
		Use:       "done submit|fail|release|verdict",
		Short:     "Finish the task from work next, with the tokens it took",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"submit", "fail", "release", "verdict"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkAgent(*agent); err != nil {
				return err
			}
			var out any
			if err := agentd.Call(cmd.Context(), agentsDir(), *agent, "POST", "/done", agentd.DoneReq{Result: args[0], Note: note, Pass: pass}, &out); err != nil {
				if errors.Is(err, agentd.ErrNoDaemon) {
					return errors.New("no helper is running for this agent: use aiculator work submit, fail or release with --task and --lease")
				}
				return err
			}
			return printJSON(out)
		},
	}
	done.Flags().StringVar(&note, "note", "", "a note for later attempts, or the reasoning of a verdict")
	done.Flags().BoolVar(&pass, "pass", false, "with verdict: approve; leave out to reject")

	helper := &cobra.Command{Use: "helper", Short: "See or stop the helper that waits for work for an agent"}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show what the helper holds, how long it may wait, and the tokens it has seen spent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkAgent(*agent); err != nil {
				return err
			}
			var st any
			if err := agentd.Call(cmd.Context(), agentsDir(), *agent, "GET", "/status", nil, &st); err != nil {
				return err
			}
			return printJSON(st)
		},
	}
	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the helper; a task it took but never handed over goes back",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkAgent(*agent); err != nil {
				return err
			}
			err := agentd.Call(cmd.Context(), agentsDir(), *agent, "POST", "/stop", nil, nil)
			if errors.Is(err, agentd.ErrNoDaemon) {
				fmt.Println("no helper is running")
				return nil
			}
			return err
		},
	}
	helper.AddCommand(status, stopCmd)

	var cwd string
	daemon := &cobra.Command{
		Use:    "daemon",
		Short:  "Run the helper for an agent; aiculator work next starts it",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkAgent(*agent); err != nil {
				return err
			}
			tok, err := os.ReadFile(filepath.Join(agentsDir(), *agent+".token"))
			if err != nil {
				return err
			}
			home, _ := os.UserHomeDir()
			d, err := agentd.New(agentd.Options{
				Agent: *agent, Token: strings.TrimSpace(string(tok)), Dir: agentsDir(), CWD: cwd, Home: home,
				TokenBudget: budget, Version: version, Log: vlog.Slog("agentd"),
				Resolve: func() (string, error) {
					b, err := os.ReadFile(filepath.Join(dataDir, "addr"))
					if err != nil {
						return "", err
					}
					return strings.TrimSpace(string(b)), nil
				},
			})
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return d.Run(ctx)
		},
	}
	daemon.Flags().StringVar(&cwd, "cwd", "", "the folder the harness runs in, where its log is found")
	daemon.Flags().Int64Var(&budget, "token-budget", 0, "tokens this session may spend")
	return []*cobra.Command{next, done, helper, daemon}
}
