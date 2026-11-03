package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/driver"
	"github.com/Grano22/aiculator/internal/sandbox"
)

func loadManifest(harness, path string) (driver.Manifest, error) {
	if path != "" {
		k := koanf.New(".")
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return driver.Manifest{}, fmt.Errorf("manifest %s: %w", path, err)
		}
		var m driver.Manifest
		if err := k.Unmarshal("", &m); err != nil {
			return driver.Manifest{}, fmt.Errorf("manifest %s: %w", path, err)
		}
		return m, nil
	}
	m, ok := driver.Preset(harness)
	if !ok {
		return m, fmt.Errorf("harness %q is unknown or has no ACP mode: see aiculator harness, or use --manifest", harness)
	}
	return m, nil
}

func driveCmd() *cobra.Command {
	var (
		harness, manifest, url, agent, sb string
		maxTasks                          int
		hold, heartbeat                   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "drive",
		Short: "Launch an ACP harness and feed it tasks (token in SWARM_TOKEN)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			token := os.Getenv("SWARM_TOKEN")
			if token == "" {
				return fmt.Errorf("SWARM_TOKEN is not set; create one with: swarm token --agent NAME --tier N")
			}
			m, err := loadManifest(harness, manifest)
			if err != nil {
				return err
			}
			s, err := sandbox.Open(sb)
			if err != nil {
				return err
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			d, err := driver.New(driver.Options{
				Manifest: m, URL: url, Token: token, AgentID: agent, Sandbox: s, SwarmBin: self, Hide: secretPaths(),
				Hold: hold, Heartbeat: heartbeat, MaxTasks: maxTasks, Log: log, Stderr: os.Stderr,
			})
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			st, err := d.Run(ctx)
			log.Info("driver finished", "tasks", st.Tasks, "submitted", st.Submitted, "failed", st.Failed, "reviews", st.Reviews, "waits", st.Waits, "exit", st.ExitReason)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&harness, "harness", "", "harness id, from aiculator harness")
	f.StringVar(&manifest, "manifest", "", "YAML manifest file, instead of a preset")
	f.StringVar(&url, "url", "http://127.0.0.1:7777", "server URL")
	f.StringVar(&agent, "agent", "driver", "agent name, for review notes")
	f.StringVar(&sb, "sandbox", "auto", "sandbox: auto, seatbelt, bwrap, docker or none")
	f.IntVar(&maxTasks, "max-tasks", 0, "stop after this many tasks; 0 means no limit")
	f.DurationVar(&hold, "hold", 30*time.Second, "how long each wait call holds")
	f.DurationVar(&heartbeat, "heartbeat", 15*time.Second, "how often to save work and renew the lease")
	return cmd
}
