// Package memory is what the agents of a swarm share about the codebase, so that what one
// learns the others do not pay to learn again. It has two parts:
//
//   - A code graph: files, symbols and how they connect, built once on the server from the
//     integration branch and rebuilt after each merge. Agents ask it a question and read a
//     slice of a few hundred tokens instead of opening and searching files themselves. It is
//     built by Graphify (github.com/Graphify-Labs/Graphify) when that is installed, and by
//     the built-in indexer otherwise.
//   - Notes: short facts agents leave for each other, found again by the words they share
//     with a question. A note about a file that has since changed is marked stale.
//
// It runs beside the swarm like the connectors and backups: it reads the commit stream and
// git, and never sits on the path of a claim or a commit.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Grano22/aiculator/internal/codegraph"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/store"
)

// Source is what memory reads and writes. *store.Store is one.
type Source interface {
	FollowTasks(ctx context.Context, durable string, filter []byte, fn func(t core.Task, seq uint64, d store.Delivery)) (func(), error)
	PutMemory(key string, value []byte) error
	DeleteMemory(key string) error
	ListMemory(ctx context.Context, fn func(key string, value []byte)) error
}

// Modes for the code graph.
const (
	ModeAuto     = "auto"     // Graphify when installed, the built-in indexer otherwise.
	ModeGraphify = "graphify" // Graphify only; an error if it is missing.
	ModeBuiltin  = "builtin"  // The built-in indexer only.
	ModeOff      = "off"      // No code graph; notes still work.
)

// Config sets memory up.
type Config struct {
	// Repo is the project's git repository, and Branch the one merged work lands on. With no
	// Repo there is no code graph.
	Repo, Branch string
	// Dir is where the graph's checkout and files are kept, inside the data directory.
	Dir  string
	Mode string
	// Graphify is the graphify program, when not graphify on PATH.
	Graphify string
	// Debounce waits for merges to settle before rebuilding; Every rebuilds anyway, in case
	// a merge was missed.
	Debounce, Every time.Duration
	Log             *slog.Logger
}

// GraphStatus says how the code graph is.
type GraphStatus struct {
	Mode    string    `json:"mode"`
	Source  string    `json:"source,omitempty"`
	Commit  string    `json:"commit,omitempty"`
	BuiltAt time.Time `json:"built_at,omitzero"`
	TookMs  int64     `json:"took_ms,omitempty"`
	Nodes   int       `json:"nodes"`
	Edges   int       `json:"edges"`
	Files   int       `json:"files"`
	Err     string    `json:"error,omitempty"`
	// Building is true while a rebuild runs; the previous graph keeps answering meanwhile.
	Building bool `json:"building,omitempty"`
}

// Stats count what memory has saved the agents from doing.
type Stats struct {
	Queries     int64 `json:"code_queries"`
	Recalls     int64 `json:"recalls"`
	NotesServed int64 `json:"notes_served"`
	TokensRead  int64 `json:"tokens_read"`
	NotesAdded  int64 `json:"notes_added"`
	NotesMerged int64 `json:"notes_merged"`
}

// Status is everything about memory at a glance.
type Status struct {
	Graph GraphStatus `json:"graph"`
	Notes int         `json:"notes"`
	Stale int         `json:"stale_notes"`
	Stats Stats       `json:"stats"`
}

// Service is shared memory for one project.
type Service struct {
	cfg Config
	src Source
	log *slog.Logger

	mu    sync.RWMutex
	graph *codegraph.Graph
	gs    GraphStatus
	book  *book
	stats Stats

	kick   chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// buildMu keeps one rebuild at a time, whether from a merge or from Refresh.
	buildMu sync.Mutex
	seeded  bool
}

// Start loads the notes and starts keeping the code graph. Close stops it.
func Start(ctx context.Context, cfg Config, src Source) (*Service, error) {
	if cfg.Mode == "" {
		cfg.Mode = ModeAuto
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = 3 * time.Second
	}
	if cfg.Every <= 0 {
		cfg.Every = 10 * time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	switch cfg.Mode {
	case ModeAuto, ModeGraphify, ModeBuiltin, ModeOff:
	default:
		return nil, fmt.Errorf("memory: unknown graph mode %q: use auto, graphify, builtin or off", cfg.Mode)
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Service{cfg: cfg, src: src, log: cfg.Log, book: newBook(), kick: make(chan struct{}, 1), cancel: cancel}
	s.gs.Mode = cfg.Mode
	if err := src.ListMemory(ctx, func(key string, value []byte) {
		var n Note
		if strings.HasPrefix(key, "n.") && json.Unmarshal(value, &n) == nil && n.ID != "" {
			s.book.put(&n)
		}
	}); err != nil {
		cancel()
		return nil, fmt.Errorf("memory: load notes: %w", err)
	}
	if cfg.Repo == "" || cfg.Mode == ModeOff {
		if cfg.Repo == "" && cfg.Mode != ModeOff {
			s.gs.Err = "no repository, so no code graph; notes still work"
		}
		return s, nil
	}
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.loop(ctx) }()
	go func() { defer s.wg.Done(); s.follow(ctx) }()
	s.kickNow()
	return s, nil
}

// Close stops keeping the graph, and waits for a rebuild in progress.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

func (s *Service) kickNow() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// follow rebuilds after merged work: a task reaching done changed the integration branch.
func (s *Service) follow(ctx context.Context) {
	stop, err := s.src.FollowTasks(ctx, "memory-graph", []byte(`"state":"done"`), func(t core.Task, _ uint64, d store.Delivery) {
		if t.State == core.Done {
			s.kickNow()
		}
		d.Ack()
	})
	if err != nil {
		s.log.Warn("memory: not following merges; the code graph is rebuilt on a timer only", "err", err)
		return
	}
	<-ctx.Done()
	stop()
}

func (s *Service) loop(ctx context.Context) {
	tick := time.NewTicker(s.cfg.Every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.kick:
			// Merges come in bursts; wait for a quiet moment, then build once for all of them.
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.cfg.Debounce):
			}
		}
		if _, err := s.rebuild(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("memory: code graph rebuild failed", "err", err)
		}
	}
}

// Refresh rebuilds the code graph now, if the branch moved, and returns its status.
func (s *Service) Refresh(ctx context.Context) (GraphStatus, error) {
	if s.cfg.Repo == "" || s.cfg.Mode == ModeOff {
		return s.Status().Graph, errors.New("there is no code graph here: no repository, or memory is off")
	}
	return s.rebuild(ctx)
}

func (s *Service) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// rebuild moves the graph's checkout to the branch's newest commit and builds the graph again,
// incrementally when Graphify does it. Notes about the files that changed are marked stale.
func (s *Service) rebuild(ctx context.Context) (GraphStatus, error) {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	ref := s.cfg.Branch
	if ref == "" {
		ref = "HEAD"
	}
	sha, err := s.git(ctx, s.cfg.Repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || sha == "" {
		// Before anything is merged the branch may not exist; the checkout's head will do.
		if sha, err = s.git(ctx, s.cfg.Repo, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
			return s.fail(err)
		}
	}
	s.mu.RLock()
	prev, have := s.gs.Commit, s.graph != nil
	s.mu.RUnlock()
	if have && prev == sha {
		return s.Status().Graph, nil
	}
	s.mu.Lock()
	s.gs.Building = true
	s.mu.Unlock()
	start := time.Now()
	tree := filepath.Join(s.cfg.Dir, "tree")
	if _, err := os.Stat(filepath.Join(tree, ".git")); err != nil {
		_ = os.RemoveAll(tree)
		_, _ = s.git(ctx, s.cfg.Repo, "worktree", "prune")
		if err := os.MkdirAll(s.cfg.Dir, 0o700); err != nil {
			return s.fail(err)
		}
		if _, err := s.git(ctx, s.cfg.Repo, "worktree", "add", "--detach", "--force", tree, sha); err != nil {
			return s.fail(err)
		}
	} else if _, err := s.git(ctx, tree, "checkout", "--detach", "--force", "--quiet", sha); err != nil {
		return s.fail(err)
	}
	var changed []string
	if prev != "" {
		if out, err := s.git(ctx, s.cfg.Repo, "diff", "--name-only", prev, sha); err == nil && out != "" {
			changed = strings.Split(out, "\n")
		}
	}
	g, buildErr := s.build(ctx, tree)
	if g == nil {
		return s.fail(buildErr)
	}
	took := time.Since(start)
	st := g.Stats()
	s.mu.Lock()
	s.graph = g
	s.gs = GraphStatus{Mode: s.cfg.Mode, Source: st.Source, Commit: sha, BuiltAt: time.Now(), TookMs: took.Milliseconds(),
		Nodes: st.Nodes, Edges: st.Edges, Files: st.Files}
	if buildErr != nil {
		s.gs.Err = buildErr.Error() // Graphify failed and the built-in indexer stood in.
	}
	stale := s.book.markStale(changed, time.Now())
	s.mu.Unlock()
	for _, n := range stale {
		s.persist(n)
	}
	s.log.Info("memory: code graph built", "source", st.Source, "nodes", st.Nodes, "edges", st.Edges, "took", took, "stale_notes", len(stale))
	return s.Status().Graph, nil
}

func (s *Service) fail(err error) (GraphStatus, error) {
	s.mu.Lock()
	s.gs.Building = false
	s.gs.Err = err.Error()
	s.mu.Unlock()
	return s.Status().Graph, err
}

// build makes the graph with Graphify or the built-in indexer. A Graphify that fails leaves
// the built-in graph in its place, with the error, so agents still have a map.
func (s *Service) build(ctx context.Context, tree string) (*codegraph.Graph, error) {
	bin := s.cfg.Graphify
	if bin == "" {
		bin = "graphify"
	}
	useGraphify := s.cfg.Mode == ModeGraphify
	if s.cfg.Mode == ModeAuto {
		_, err := exec.LookPath(bin)
		useGraphify = err == nil
	}
	if useGraphify {
		g, err := s.graphify(ctx, bin, tree)
		if err == nil {
			return g, nil
		}
		if s.cfg.Mode == ModeGraphify {
			return nil, err
		}
		b, berr := codegraph.Build(ctx, tree)
		if berr != nil {
			return nil, berr
		}
		return b, fmt.Errorf("graphify failed, so the built-in indexer was used: %w", err)
	}
	g, err := codegraph.Build(ctx, tree)
	if err != nil {
		return nil, err
	}
	_ = g.Write(filepath.Join(s.cfg.Dir, "graph.json"))
	return g, nil
}

// graphify runs Graphify's incremental update in the checkout. The first time, a graph the
// project already has in graphify-out, which may hold what Graphify's model pass found in the
// docs, is copied in to start from, so that it is extended rather than built again.
func (s *Service) graphify(ctx context.Context, bin, tree string) (*codegraph.Graph, error) {
	out := filepath.Join(tree, "graphify-out")
	if !s.seeded {
		s.seeded = true
		if b, err := os.ReadFile(filepath.Join(s.cfg.Repo, "graphify-out", "graph.json")); err == nil {
			if err := os.MkdirAll(out, 0o755); err == nil {
				_ = os.WriteFile(filepath.Join(out, "graph.json"), b, 0o644)
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "update", tree)
	cmd.Dir = tree
	// Its own background refresh would race this one.
	cmd.Env = append(os.Environ(), "GRAPHIFY_NO_AUTO_REFRESH=1")
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &errb, &errb
	if err := cmd.Run(); err != nil {
		tail := errb.String()
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		return nil, fmt.Errorf("graphify update: %v: %s", err, strings.TrimSpace(tail))
	}
	g, err := codegraph.Load(filepath.Join(out, "graph.json"))
	if err != nil {
		return nil, fmt.Errorf("graphify wrote no readable graph: %w", err)
	}
	return g, nil
}

// ErrNoGraph means there is no code graph to ask yet.
var ErrNoGraph = errors.New("the code graph is not built yet")

func (s *Service) currentGraph() (*codegraph.Graph, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.graph == nil {
		if s.gs.Err != "" {
			return nil, fmt.Errorf("%w: %s", ErrNoGraph, s.gs.Err)
		}
		if s.cfg.Repo == "" || s.cfg.Mode == ModeOff {
			return nil, errors.New("this swarm has no code graph: it has no repository, or memory is off")
		}
		return nil, fmt.Errorf("%w; it is being built, try again in a moment", ErrNoGraph)
	}
	return s.graph, nil
}

// CodeQuery answers a question about the code from the graph, within a token budget.
func (s *Service) CodeQuery(question string, budget int) (codegraph.Answer, error) {
	g, err := s.currentGraph()
	if err != nil {
		return codegraph.Answer{}, err
	}
	a := g.Query(question, budget)
	s.count(func(st *Stats) { st.Queries++; st.TokensRead += int64(a.Tokens) })
	return a, nil
}

// CodeSymbol describes a symbol or file and what it connects to.
func (s *Service) CodeSymbol(name string, budget int) (codegraph.Answer, error) {
	g, err := s.currentGraph()
	if err != nil {
		return codegraph.Answer{}, err
	}
	a, err := g.Symbol(name, budget)
	if err == nil {
		s.count(func(st *Stats) { st.Queries++; st.TokensRead += int64(a.Tokens) })
	}
	return a, err
}

func (s *Service) count(fn func(*Stats)) {
	s.mu.Lock()
	fn(&s.stats)
	s.mu.Unlock()
}

// Recall finds the notes most relevant to a question and, optionally, to some files.
func (s *Service) Recall(query string, files []string, k int) []Recalled {
	if k <= 0 {
		k = 5
	}
	s.mu.Lock()
	out := s.book.recall(query, files, min(k, 20))
	for _, r := range out {
		if n := s.book.notes[r.ID]; n != nil {
			n.Uses++
		}
	}
	s.stats.Recalls++
	s.stats.NotesServed += int64(len(out))
	for _, r := range out {
		s.stats.TokensRead += int64(len(r.Text)/4 + 8)
	}
	s.mu.Unlock()
	return out
}

// Write keeps a note. A note that says much the same as one already kept updates that one,
// and comes back with created false.
func (s *Service) Write(author, task string, in NoteInput) (Note, bool, error) {
	in, err := in.check()
	if err != nil {
		return Note{}, false, err
	}
	now := time.Now().UTC()
	s.mu.Lock()
	n := s.book.similar(in.Text)
	created := n == nil
	if created {
		n = &Note{ID: newID(), Created: now}
		s.stats.NotesAdded++
	} else {
		s.stats.NotesMerged++
	}
	n.Text, n.Author, n.Task, n.Updated = in.Text, author, task, now
	n.Files = mergeList(n.Files, in.Files, maxRefs)
	n.Symbols = mergeList(n.Symbols, in.Symbols, maxRefs)
	n.Tags = mergeList(n.Tags, in.Tags, maxTags)
	n.StaleSince, n.StaleFiles = time.Time{}, nil // Written now, so current now.
	s.book.put(n)
	var evicted []string
	for len(s.book.notes) > MaxNotes {
		id := s.book.evict()
		s.book.remove(id)
		evicted = append(evicted, id)
	}
	saved := *n
	s.mu.Unlock()
	if err := s.persist(&saved); err != nil {
		return saved, created, err
	}
	for _, id := range evicted {
		_ = s.src.DeleteMemory("n." + id)
	}
	return saved, created, nil
}

func mergeList(have, add []string, max int) []string {
	out := slices.Clone(have)
	for _, v := range add {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

func (s *Service) persist(n *Note) error {
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return s.src.PutMemory("n."+n.ID, b)
}

// Forget removes a note.
func (s *Service) Forget(id string) error {
	s.mu.Lock()
	_, ok := s.book.notes[id]
	s.book.remove(id)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("no note %s", id)
	}
	return s.src.DeleteMemory("n." + id)
}

// Notes lists the newest notes.
func (s *Service) Notes(limit int) []Note {
	s.mu.RLock()
	out := make([]Note, 0, len(s.book.notes))
	for _, n := range s.book.notes {
		out = append(out, *n)
	}
	s.mu.RUnlock()
	slices.SortFunc(out, func(a, b Note) int { return b.Updated.Compare(a.Updated) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Hint is a note handed out with a task, cut short: enough to know it is there.
type Hint struct {
	ID    string   `json:"id"`
	Text  string   `json:"text"`
	Files []string `json:"files,omitempty"`
	Stale bool     `json:"stale,omitempty"`
}

// hintScore is how well a note must match a task to come with it, so that a weak match does
// not cost every agent tokens for nothing.
const hintScore = 2.0

// Hints are the notes most relevant to a task, given with its grant.
func (s *Service) Hints(title, spec string, files []string) []Hint {
	s.mu.Lock()
	found := s.book.recall(title+" "+spec, files, 3)
	s.mu.Unlock()
	var out []Hint
	for _, r := range found {
		if r.Score < hintScore {
			continue
		}
		text := r.Text
		if len(text) > 280 {
			text = text[:277] + "..."
		}
		out = append(out, Hint{ID: r.ID, Text: text, Files: r.Files, Stale: !r.StaleSince.IsZero()})
	}
	if len(out) > 0 {
		s.count(func(st *Stats) { st.NotesServed += int64(len(out)) })
	}
	return out
}

// Status says how memory is.
func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Status{Graph: s.gs, Notes: len(s.book.notes), Stats: s.stats}
	for _, n := range s.book.notes {
		if !n.StaleSince.IsZero() {
			st.Stale++
		}
	}
	return st
}
