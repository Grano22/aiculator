package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Grano22/aiculator/internal/api"
	"github.com/Grano22/aiculator/internal/codegraph"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/memory"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/work"
)

// startMemory runs shared memory: notes always, and the code graph when there is a
// repository. Like backups, a failure turns it off and leaves the swarm running.
func startMemory(ctx context.Context, st *store.Store, wm *work.Manager, mode string, log *slog.Logger) *memory.Service {
	cfg := memory.Config{Mode: mode, Dir: filepath.Join(dataDir, "graph"), Log: log}
	if wm != nil {
		cfg.Repo, cfg.Branch = wm.Repo(), wm.Branch()
	}
	m, err := memory.Start(ctx, cfg, st)
	if err != nil {
		log.Error("shared memory is off", "err", err)
		return nil
	}
	return m
}

// memoryAPI adapts the memory service to what the routes need, and its errors to theirs.
type memoryAPI struct{ m *memory.Service }

func memErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, memory.ErrInvalid):
		return fmt.Errorf("%w: %v", engine.ErrInvalid, err)
	case errors.Is(err, memory.ErrNoGraph):
		return fmt.Errorf("%w: %v", api.ErrUnavailable, err)
	case strings.HasPrefix(err.Error(), "no symbol") || strings.HasPrefix(err.Error(), "no note"):
		return fmt.Errorf("%w: %v", engine.ErrNotFound, err)
	case strings.Contains(err.Error(), "no code graph"):
		return fmt.Errorf("%w: %v", api.ErrUnavailable, err)
	}
	return err
}

func (a memoryAPI) MemoryStatus() any { return a.m.Status() }
func (a memoryAPI) CodeQuery(q string, budget int) (any, error) {
	if strings.TrimSpace(q) == "" {
		return nil, fmt.Errorf("%w: ask a question", engine.ErrInvalid)
	}
	r, err := a.m.CodeQuery(q, budget)
	return r, memErr(err)
}
func (a memoryAPI) CodeSymbol(name string, budget int) (any, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%w: name a symbol or file", engine.ErrInvalid)
	}
	r, err := a.m.CodeSymbol(name, budget)
	return r, memErr(err)
}
func (a memoryAPI) Recall(q string, files []string, k int) any { return a.m.Recall(q, files, k) }
func (a memoryAPI) Note(author, task string, q api.NoteReq) (any, error) {
	n, created, err := a.m.Write(author, task, memory.NoteInput{Text: q.Text, Files: q.Files, Symbols: q.Symbols, Tags: q.Tags})
	if err != nil {
		return nil, memErr(err)
	}
	return map[string]any{"note": n, "created": created}, nil
}
func (a memoryAPI) Forget(id string) error { return memErr(a.m.Forget(id)) }
func (a memoryAPI) Notes(limit int) any    { return a.m.Notes(limit) }
func (a memoryAPI) Refresh(ctx context.Context) (any, error) {
	st, err := a.m.Refresh(ctx)
	return st, memErr(err)
}
func (a memoryAPI) TaskHints(title, spec string, files []string) any {
	if h := a.m.Hints(title, spec, files); len(h) > 0 {
		return h
	}
	return nil
}

func printRecall(list []memory.Recalled) {
	if len(list) == 0 {
		fmt.Println("no note bears on that")
		return
	}
	for _, r := range list {
		stale := ""
		if !r.StaleSince.IsZero() {
			stale = "  [stale: " + strings.Join(r.StaleFiles, ", ") + " changed since]"
		}
		fmt.Printf("%s  %.1f  %s%s\n", r.ID, r.Score, r.Text, stale)
		if len(r.Files)+len(r.Symbols) > 0 {
			fmt.Printf("    %s\n", strings.Join(append(append([]string{}, r.Files...), r.Symbols...), ", "))
		}
	}
}

func memoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "What agents share about the codebase: notes, and the code graph's status",
		Long: `What agents share about the codebase, so that what one learns the others need not
learn again. Agents leave short notes with memory_note and find them with memory_recall; a
note about a file that changed since is marked stale. The code graph, which agents ask with
code_query, is built from the integration branch after each merge: by Graphify when it is
installed (uv tool install graphifyy), and by the built-in indexer otherwise.`,
	}
	var asJSON bool
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the code graph and the notes, and what they have served",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var st memory.Status
			if err := c.Get(cmd.Context(), "/v1/memory/status", &st); err != nil {
				return err
			}
			if asJSON {
				return printJSON(st)
			}
			g := st.Graph
			switch {
			case g.Nodes > 0:
				fmt.Printf("code graph: %d nodes, %d edges, %d files, built by %s from %.8s %s ago in %dms\n",
					g.Nodes, g.Edges, g.Files, g.Source, g.Commit, time.Since(g.BuiltAt).Round(time.Second), g.TookMs)
			case g.Mode == memory.ModeOff:
				fmt.Println("code graph: off")
			default:
				fmt.Println("code graph: not built yet")
			}
			if g.Err != "" {
				fmt.Println("  " + g.Err)
			}
			fmt.Printf("notes: %d, %d stale\n", st.Notes, st.Stale)
			s := st.Stats
			fmt.Printf("served: %d code questions, %d recalls, %d notes, about %d tokens read\n", s.Queries, s.Recalls, s.NotesServed, s.TokensRead)
			return nil
		},
	}
	status.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	var files []string
	var k int
	recall := &cobra.Command{
		Use:   "recall QUESTION",
		Short: "Find the notes that bear on a question, or on files with --file",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			q := url.Values{"q": {strings.Join(args, " ")}, "file": files, "k": {strconv.Itoa(k)}}
			var list []memory.Recalled
			if err := c.Get(cmd.Context(), "/v1/memory?"+q.Encode(), &list); err != nil {
				return err
			}
			printRecall(list)
			return nil
		},
	}
	recall.Flags().StringSliceVar(&files, "file", nil, "files the question is about")
	recall.Flags().IntVar(&k, "k", 5, "how many notes")
	var limit int
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List the newest notes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var list []memory.Note
			if err := c.Get(cmd.Context(), "/v1/memory?limit="+strconv.Itoa(limit), &list); err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tBY\tUSES\tSTALE\tNOTE")
			for _, n := range list {
				stale := ""
				if !n.StaleSince.IsZero() {
					stale = "yes"
				}
				text := n.Text
				if len(text) > 90 {
					text = text[:87] + "..."
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", n.ID, n.Author, n.Uses, stale, text)
			}
			return tw.Flush()
		},
	}
	ls.Flags().IntVar(&limit, "limit", 50, "how many")
	forget := &cobra.Command{
		Use:   "forget ID",
		Short: "Remove a note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			return c.Do(cmd.Context(), "DELETE", "/v1/memory/"+args[0], nil, nil)
		},
	}
	refresh := &cobra.Command{
		Use:   "refresh",
		Short: "Rebuild the code graph now, if the integration branch moved",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var g memory.GraphStatus
			if err := c.Post(cmd.Context(), "/v1/memory/refresh", nil, &g); err != nil {
				return err
			}
			return printJSON(g)
		},
	}
	cmd.AddCommand(status, recall, ls, forget, refresh)
	return cmd
}

func codeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "code", Short: "Ask the shared code graph about the codebase"}
	var budget int
	query := &cobra.Command{
		Use:   "query QUESTION",
		Short: "Answer a question from the code graph, in a few hundred tokens",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var a codegraph.Answer
			q := url.Values{"q": {strings.Join(args, " ")}, "budget": {strconv.Itoa(budget)}}
			if err := c.Get(cmd.Context(), "/v1/code?"+q.Encode(), &a); err != nil {
				return err
			}
			fmt.Print(a.Text)
			return nil
		},
	}
	symbol := &cobra.Command{
		Use:   "symbol NAME",
		Short: "Show a symbol or file and everything it connects to",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := adminClient()
			if err != nil {
				return err
			}
			var a codegraph.Answer
			q := url.Values{"name": {args[0]}, "budget": {strconv.Itoa(budget)}}
			if err := c.Get(cmd.Context(), "/v1/code/symbol?"+q.Encode(), &a); err != nil {
				return err
			}
			fmt.Print(a.Text)
			return nil
		},
	}
	for _, c := range []*cobra.Command{query, symbol} {
		c.Flags().IntVar(&budget, "budget", codegraph.DefaultBudget, "most tokens the answer may take")
	}
	cmd.AddCommand(query, symbol)
	return cmd
}
