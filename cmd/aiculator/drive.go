package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"aiculation/internal/driver"
	"aiculation/internal/sandbox"
)

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// mcpCmd bridges stdio to the server's HTTP endpoint, for harnesses that can only
// launch MCP servers as subprocesses. It reads the token from the environment, so
// the token does not appear in process listings.
func mcpCmd() *cobra.Command {
	var url string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the swarm's MCP tools over stdio (token in SWARM_TOKEN)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			token := os.Getenv("SWARM_TOKEN")
			if token == "" {
				return fmt.Errorf("SWARM_TOKEN is not set")
			}
			ctx := cmd.Context()
			cl := mcp.NewClient(&mcp.Implementation{Name: "swarm-bridge", Version: "0.1.0"}, nil)
			session, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
				Endpoint:   url + "/mcp",
				HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}},
			}, nil)
			if err != nil {
				return err
			}
			defer session.Close()
			srv := mcp.NewServer(&mcp.Implementation{Name: "swarm", Version: "0.1.0"},
				&mcp.ServerOptions{Instructions: session.InitializeResult().Instructions})
			for t, err := range session.Tools(ctx, nil) {
				if err != nil {
					return err
				}
				srv.AddTool(t, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return session.CallTool(ctx, &mcp.CallToolParams{Name: r.Params.Name, Arguments: r.Params.Arguments})
				})
			}
			return srv.Run(ctx, &mcp.StdioTransport{})
		},
	}
	cmd.Flags().StringVar(&url, "url", "http://127.0.0.1:7777", "server URL")
	return cmd
}

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
	m, ok := driver.Presets()[harness]
	if !ok {
		return m, fmt.Errorf("unknown harness %q: use gemini, claude, codex or --manifest", harness)
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
	f.StringVar(&harness, "harness", "", "preset: gemini, claude or codex")
	f.StringVar(&manifest, "manifest", "", "YAML manifest file, instead of a preset")
	f.StringVar(&url, "url", "http://127.0.0.1:7777", "server URL")
	f.StringVar(&agent, "agent", "driver", "agent name, for review notes")
	f.StringVar(&sb, "sandbox", "auto", "sandbox: auto, seatbelt, bwrap, docker or none")
	f.IntVar(&maxTasks, "max-tasks", 0, "stop after this many tasks; 0 means no limit")
	f.DurationVar(&hold, "hold", 30*time.Second, "how long each wait call holds")
	f.DurationVar(&heartbeat, "heartbeat", 15*time.Second, "how often to save work and renew the lease")
	return cmd
}
