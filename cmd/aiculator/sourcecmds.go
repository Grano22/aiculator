package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/connect"
	"github.com/Grano22/aiculator/internal/swarm"
)

func loadSources(path string) (*connect.Config, error) {
	if path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		return connect.Load(abs, filepath.Dir(abs))
	}
	return connect.Find(projectRoot())
}

// startSync reads the sources file and starts whatever has a sync section. It returns nil
// when there is nothing to do.
func startSync(ctx context.Context, svc *swarm.Service, repo string, log *slog.Logger) *connect.Syncer {
	root := repo
	if root == "" {
		root = projectRoot()
	}
	cfg, err := connect.Find(root)
	if err != nil {
		if !errors.Is(err, connect.ErrNoConfig) {
			log.Error("connectors are off: the sources file has a problem", "err", err)
		}
		return nil
	}
	svc.Connections = cfg.Downloaders()
	return connect.Start(ctx, cfg, svc, connect.Options{})
}

func syncCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sync", Short: "See how connected systems are being kept in step"}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show each source's sync: what was sent and received, latency and errors",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var st []connect.Status
			if err := c.Get(cmd.Context(), "/v1/sync", &st); err != nil {
				return err
			}
			if len(st) == 0 {
				fmt.Println("no source has a sync section")
				return nil
			}
			return printJSON(st)
		},
	}
	token := &cobra.Command{
		Use:   "hook-token SOURCE",
		Short: "Show the token Notion sent when its webhook was set up",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var out map[string]string
			if err := c.Get(cmd.Context(), "/v1/sync/"+args[0]+"/hook-token", &out); err != nil {
				return err
			}
			if out["token"] == "" {
				return errors.New("no token has arrived yet")
			}
			fmt.Println(out["token"])
			return nil
		},
	}
	cmd.AddCommand(status, token)
	return cmd
}

func importCmd() *cobra.Command {
	var (
		config, query string
		limit         int
		dryRun        bool
		update        bool
	)
	cmd := &cobra.Command{
		Use:   "import SOURCE",
		Short: "Load tasks once from a configured source, such as Jira, Notion or a spreadsheet",
		Long: "Reads a source named in .agents/sources.yaml, once, and creates a task for each item.\n" +
			"Importing again is safe: an item that was imported before is left alone, or brought up to date with --update.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadSources(config)
			if err != nil {
				return err
			}
			src, err := cfg.Get(args[0])
			if err != nil {
				return err
			}
			conn, err := cfg.Open(src)
			if err != nil {
				return err
			}
			rep, err := connect.Read(cmd.Context(), conn, src, connect.ImportOptions{Query: query, Limit: limit})
			if err != nil {
				return err
			}
			for _, f := range rep.Failed {
				fmt.Fprintln(os.Stderr, "skipped:", f)
			}
			for _, id := range rep.Cyclic {
				fmt.Fprintln(os.Stderr, "skipped: in a dependency cycle:", id)
			}
			if dryRun {
				fmt.Printf("%-32s %-4s %-8s %s\n", "ID", "TIER", "PRIORITY", "TITLE")
				for _, m := range rep.Mapped {
					fmt.Printf("%-32s %-4d %-8d %s\n", m.Spec.ID, max(m.Spec.Tier, 1), m.Spec.Priority, m.Spec.Title)
				}
				fmt.Printf("%d read, %d would import, %d skipped by rules, %d could not be read. Nothing was changed.\n",
					rep.Read, len(rep.Mapped), rep.Skipped, len(rep.Failed)+len(rep.Cyclic))
				return nil
			}
			c, err := adminClient()
			if err != nil {
				return err
			}
			items := make([]swarm.ImportItem, 0, len(rep.Mapped))
			for _, m := range rep.Mapped {
				items = append(items, swarm.ImportItem{Spec: m.Spec, Props: m.Props, Link: m.Link})
			}
			counts := map[string]int{}
			failed := 0
			// Requests stay small, and each one holds the server's writer only briefly.
			for batch := range slices.Chunk(items, 256) {
				var res []swarm.ImportResult
				if err := c.Post(cmd.Context(), "/v1/import", api.ImportReq{Items: batch, Update: update}, &res); err != nil {
					return err
				}
				for _, r := range res {
					counts[r.Action]++
					if r.Error != "" {
						failed++
						fmt.Fprintf(os.Stderr, "failed: %s: %s\n", r.ID, r.Error)
					}
					if r.Warning != "" {
						fmt.Fprintf(os.Stderr, "warning: %s: %s\n", r.ID, r.Warning)
					}
				}
			}
			fmt.Printf("%d read, %d created, %d updated, %d unchanged, %d skipped by rules, %d failed\n",
				rep.Read, counts["created"], counts["updated"], counts["unchanged"], rep.Skipped, failed+len(rep.Failed)+len(rep.Cyclic))
			if failed > 0 {
				return fmt.Errorf("%d items failed", failed)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&config, "config", "", "sources file; .agents/sources.yaml if empty")
	f.StringVar(&query, "query", "", "use this query instead of the configured one: JQL for Jira, a JSON filter for Notion")
	f.IntVar(&limit, "limit", 0, "stop after this many items; 0 means all")
	f.BoolVar(&dryRun, "dry-run", false, "show what would be imported, and change nothing")
	f.BoolVar(&update, "update", false, "bring items that were imported before up to date")
	return cmd
}

func sourceCmd() *cobra.Command {
	var config string
	cmd := &cobra.Command{Use: "source", Short: "List and check the configured sources"}
	cmd.PersistentFlags().StringVar(&config, "config", "", "sources file; .agents/sources.yaml if empty")
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List the configured sources",
		RunE: func(*cobra.Command, []string) error {
			cfg, err := loadSources(config)
			if err != nil {
				return err
			}
			fmt.Printf("%-20s %-8s %s\n", "NAME", "KIND", "SYNC")
			for _, s := range cfg.Sources {
				fmt.Printf("%-20s %-8s %s\n", s.Name, s.Kind, s.SyncSummary())
			}
			return nil
		},
	}
	check := &cobra.Command{
		Use:   "check NAME",
		Short: "Check a source's settings and credentials by reading from it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadSources(config)
			if err != nil {
				return err
			}
			src, err := cfg.Get(args[0])
			if err != nil {
				return err
			}
			conn, err := cfg.Open(src)
			if err != nil {
				return err
			}
			rep, err := connect.Read(cmd.Context(), conn, src, connect.ImportOptions{Limit: 5})
			if err != nil {
				return err
			}
			fmt.Printf("%s: reachable. Read %d items; %d would map, %d could not be read.\n", src.Name, rep.Read, len(rep.Mapped), len(rep.Failed))
			for _, m := range rep.Mapped {
				fmt.Printf("  %s: %s\n", m.Spec.ID, m.Spec.Title)
			}
			for _, f := range rep.Failed {
				fmt.Println("  problem:", f)
			}
			return nil
		},
	}
	cmd.AddCommand(ls, check)
	return cmd
}
