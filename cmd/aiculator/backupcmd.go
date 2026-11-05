package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/backup"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/store"
)

// startBackups runs the project's backup rules, if it has any. Like connectors, a bad file
// turns backups off and leaves the swarm running.
func startBackups(ctx context.Context, st *store.Store, repo string, log *slog.Logger) *backup.Runner {
	root := repo
	if root == "" {
		root = projectRoot()
	}
	cfg, err := backup.Find(root)
	if err != nil {
		if !errors.Is(err, backup.ErrNoConfig) {
			log.Error("backups are off: the backup file has a problem", "err", err)
		}
		return nil
	}
	log.Info("backups on", "dir", cfg.Abs(), "rules", len(cfg.Rules))
	return backup.Start(ctx, cfg, st, log, backup.Options{})
}

func backupConfig() (*backup.Config, error) {
	cfg, err := backup.Find(projectRoot())
	if errors.Is(err, backup.ErrNoConfig) {
		return nil, fmt.Errorf("no backup file: create %s; see the recovery guide", backup.ConfigFiles[0])
	}
	return cfg, err
}

func backupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Keep and restore copies of chosen tasks",
		Long: `Keep and restore copies of chosen tasks.

Rules in ` + backup.ConfigFiles[0] + ` choose tasks by id pattern, label, tag, state or
priority, or all of them, and keep a journal of each change, snapshots at an interval,
or both. The server keeps them while it runs; these commands read them.`,
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show what each backup rule has kept",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var st []backup.Status
			if err := c.Get(cmd.Context(), "/v1/backup", &st); err != nil {
				return err
			}
			if len(st) == 0 {
				fmt.Println("no backups are configured")
				return nil
			}
			return printJSON(st)
		},
	}
	now := &cobra.Command{
		Use:   "now [RULE]",
		Short: "Take a snapshot now, for one rule or all of them",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			rule := ""
			if len(args) == 1 {
				rule = args[0]
			}
			var out struct {
				Files []string `json:"files"`
			}
			if err := c.Post(cmd.Context(), "/v1/backup/snapshot", map[string]string{"rule": rule}, &out); err != nil {
				return err
			}
			for _, f := range out.Files {
				fmt.Println(f)
			}
			return nil
		},
	}
	var jsonOut bool
	versions := &cobra.Command{
		Use:   "versions TASK",
		Short: "List the kept copies of a task, oldest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := backupConfig()
			if err != nil {
				return err
			}
			vs, err := backup.Versions(cfg.Abs(), args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(vs)
			}
			if len(vs) == 0 {
				fmt.Printf("no copies of %s in %s\n", args[0], cfg.Abs())
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "AT\tRULE\tKIND\tSTATE\tFILE")
			for _, v := range vs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", v.At.Local().Format(time.DateTime), v.Rule, v.Kind, v.State, v.Path)
			}
			return tw.Flush()
		},
	}
	versions.Flags().BoolVar(&jsonOut, "json", false, "print JSON")

	var (
		from, before string
		all, dry     bool
	)
	restore := &cobra.Command{
		Use:   "restore [TASK...]",
		Short: "Put kept copies of tasks back",
		Long: `Put kept copies of tasks back.

By default each task gets its newest copy that differs from the task as it is now. --before takes the newest copy at or before a
time, and --from takes the copies in one journal or snapshot file; with --all, every task
in that file. Work that was in flight in a copy goes back to ready, the lease moves on so
no agent with an old lease can write, and the record being replaced is kept in quarantine.`,
		RunE: func(cmd *cobra.Command, ids []string) error {
			if len(ids) == 0 && !all {
				return errors.New("name the tasks to restore, or use --from FILE --all")
			}
			if all && from == "" {
				return errors.New("--all needs --from: the snapshot to restore")
			}
			var cut time.Time
			if before != "" {
				t, err := parseWhen(before)
				if err != nil {
					return err
				}
				cut = t
			}
			current := func(id string) (core.Task, bool) {
				c, err := adminClient()
				if err != nil {
					return core.Task{}, false
				}
				var t core.Task
				return t, c.Get(cmd.Context(), "/v1/tasks/"+id, &t) == nil
			}
			picks, err := pickCopies(ids, from, cut, all, current)
			if err != nil {
				return err
			}
			if dry {
				for _, e := range picks {
					fmt.Printf("%s: %s copy from %s (%s)\n", e.Task.ID, e.Task.State, e.At.Local().Format(time.DateTime), e.Rule)
				}
				return nil
			}
			c, err := adminClient()
			if err != nil {
				return err
			}
			for _, e := range picks {
				var res engine.Restored
				if err := c.Post(cmd.Context(), "/v1/tasks/"+e.Task.ID+"/restore", e.Task, &res); err != nil {
					return fmt.Errorf("%s: %w", e.Task.ID, err)
				}
				line := fmt.Sprintf("%s: restored as %s, from %s", res.Task.ID, res.Task.State, e.At.Local().Format(time.DateTime))
				if len(res.Did) > 0 {
					line += "; " + strings.Join(res.Did, "; ")
				}
				fmt.Println(line)
			}
			return nil
		},
	}
	f := restore.Flags()
	f.StringVar(&from, "from", "", "restore from this journal or snapshot file")
	f.StringVar(&before, "before", "", "newest copy at or before this time: RFC 3339, a date, or a duration ago such as 2h")
	f.BoolVar(&all, "all", false, "with --from, every task in the file")
	f.BoolVar(&dry, "dry-run", false, "say what would be restored, and change nothing")
	cmd.AddCommand(status, now, versions, restore)
	return cmd
}

// pickCopies chooses what to put back. Without a file or a time, a task gets its newest
// copy that differs from the task as it is now: the newest copy of a task that went wrong
// is usually of it going wrong.
func pickCopies(ids []string, from string, cut time.Time, all bool, current func(string) (core.Task, bool)) ([]backup.Entry, error) {
	if from != "" {
		es, err := backup.ReadAny(from)
		if err != nil {
			return nil, err
		}
		if all {
			return es, nil
		}
		var out []backup.Entry
		for _, id := range ids {
			i := -1
			for j, e := range es {
				if e.Task.ID == id {
					i = j
				}
			}
			if i < 0 {
				return nil, fmt.Errorf("%s holds no copy of %s", from, id)
			}
			out = append(out, es[i])
		}
		return out, nil
	}
	cfg, err := backupConfig()
	if err != nil {
		return nil, err
	}
	var out []backup.Entry
	for _, id := range ids {
		vs, err := backup.Versions(cfg.Abs(), id)
		if err != nil {
			return nil, err
		}
		if !cut.IsZero() {
			v, ok := backup.Pick(vs, cut)
			if !ok {
				return nil, fmt.Errorf("no copy of %s to restore from before %s", id, cut.Local().Format(time.DateTime))
			}
			e, err := v.Entry(id)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
			continue
		}
		now, have := current(id)
		found := false
		for i := len(vs) - 1; i >= 0 && !found; i-- {
			e, err := vs[i].Entry(id)
			if err != nil {
				return nil, err
			}
			if have && backup.Same(e.Task, now) {
				continue
			}
			out, found = append(out, e), true
		}
		if !found {
			if len(vs) > 0 {
				return nil, fmt.Errorf("every copy of %s is the same as the task now; nothing to restore", id)
			}
			return nil, fmt.Errorf("no copy of %s to restore", id)
		}
	}
	return out, nil
}

// parseWhen reads a time as RFC 3339, a date, or a duration ago.
func parseWhen(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation(time.DateTime, s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, s, time.Local); err == nil {
		return t, nil
	}
	if d, err := backup.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("bad time %q: use RFC 3339, 2006-01-02, or a duration such as 2h", s)
}
