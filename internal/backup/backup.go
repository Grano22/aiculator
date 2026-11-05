package backup

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/meta"
	"github.com/Grano22/aiculator/internal/store"
)

// Source is what backups read from. *store.Store is one.
type Source interface {
	FollowTasks(ctx context.Context, durable string, filter []byte, fn func(t core.Task, seq uint64, d store.Delivery)) (func(), error)
	LoadTasks(ctx context.Context) ([]store.Stored, []store.Unreadable, error)
	GetMeta(id string) (meta.Meta, uint64, error)
}

// Status is how one rule is doing.
type Status struct {
	Rule         string    `json:"rule"`
	Journal      bool      `json:"journal"`
	Every        string    `json:"every,omitempty"`
	Kept         int64     `json:"kept"`
	Skipped      int64     `json:"skipped"`
	Pruned       int64     `json:"pruned"`
	LastCopy     time.Time `json:"last_copy,omitzero"`
	LastSnapshot time.Time `json:"last_snapshot,omitzero"`
	SnapshotSize int       `json:"snapshot_tasks,omitempty"`
	Err          string    `json:"error,omitempty"`
}

// Runner keeps the backups a config asks for.
type Runner struct {
	cfg    *Config
	src    Source
	log    *slog.Logger
	now    func() time.Time
	cancel context.CancelFunc
	wg     sync.WaitGroup
	rules  []*rule
}

type rule struct {
	Rule
	dir string
	mu  sync.Mutex
	// last is what was last kept of each task, without the fields a heartbeat moves.
	last map[string]uint64
	st   Status
}

// Options are for tests.
type Options struct {
	Now func() time.Time
}

// Start begins keeping backups. Close stops it.
func Start(ctx context.Context, cfg *Config, src Source, log *slog.Logger, o Options) *Runner {
	ctx, cancel := context.WithCancel(ctx)
	r := &Runner{cfg: cfg, src: src, log: log, now: o.Now, cancel: cancel}
	if r.now == nil {
		r.now = time.Now
	}
	for _, c := range cfg.Rules {
		ru := &rule{Rule: c, dir: cfg.Abs(), last: map[string]uint64{}}
		ru.st = Status{Rule: c.Name, Journal: c.Journal}
		if c.Every > 0 {
			ru.st.Every = time.Duration(c.Every).String()
		}
		r.rules = append(r.rules, ru)
	}
	for _, ru := range r.rules {
		if ru.Journal {
			r.wg.Add(1)
			go func() { defer r.wg.Done(); r.journal(ctx, ru) }()
		}
		if ru.Every > 0 {
			r.wg.Add(1)
			go func() { defer r.wg.Done(); r.snapshots(ctx, ru) }()
		}
	}
	return r
}

// Close stops the backups and waits for writes in progress.
func (r *Runner) Close() {
	r.cancel()
	r.wg.Wait()
}

// BackupStatus is Status for the API.
func (r *Runner) BackupStatus() any { return r.Status() }

// Status reports on every rule.
func (r *Runner) Status() []Status {
	out := make([]Status, 0, len(r.rules))
	for _, ru := range r.rules {
		ru.mu.Lock()
		out = append(out, ru.st)
		ru.mu.Unlock()
	}
	return out
}

// Dir is where the backups are.
func (r *Runner) Dir() string { return r.cfg.Abs() }

func (ru *rule) fail(err error) {
	ru.st.Err = err.Error()
}

// fingerprint ignores what a heartbeat changes, so that a task being worked on is copied
// when something about it changes, not every few seconds.
func fingerprint(t core.Task) uint64 {
	t.LeaseDeadline, t.ProgressDeadline, t.UpdatedAt = time.Time{}, time.Time{}, time.Time{}
	b, _ := json.Marshal(t)
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}

// Same reports whether two copies of a task differ only in what a heartbeat moves.
func Same(a, b core.Task) bool { return fingerprint(a) == fingerprint(b) }

// keep writes a copy of a task if it changed since the last one. It is called from the
// commit stream and from the catch-up at start, so it takes the rule's lock.
func (r *Runner) keep(ru *rule, t core.Task, seq uint64) error {
	ru.mu.Lock()
	defer ru.mu.Unlock()
	fp := fingerprint(t)
	if _, ok := ru.last[t.ID]; !ok {
		// After a restart, compare with the newest copy on disk.
		jd := journalDir(ru.dir, ru.Name, t.ID)
		if names := files(jd, ".json"); len(names) > 0 {
			if e, err := ReadEntry(filepath.Join(jd, names[len(names)-1])); err == nil {
				ru.last[t.ID] = fingerprint(e.Task)
				if seq != 0 && seq <= e.Seq {
					ru.st.Skipped++
					return nil // Delivered again after a crash; already kept.
				}
			}
		}
	}
	if prev, ok := ru.last[t.ID]; ok && prev == fp {
		ru.st.Skipped++
		return nil
	}
	e := Entry{Rule: ru.Name, At: r.now().UTC(), Seq: seq, Task: t}
	if ru.Meta {
		if m, _, err := r.src.GetMeta(t.ID); err == nil && len(m.Props) > 0 {
			e.Meta = &m
		}
	}
	if seq == 0 {
		// A copy taken at start has no commit to be named by; it goes after what is there.
		jd := journalDir(ru.dir, ru.Name, t.ID)
		if names := files(jd, ".json"); len(names) > 0 {
			e.Seq = seqOf(names[len(names)-1]) + 1
		}
	}
	if _, err := writeEntry(ru.dir, e); err != nil {
		ru.fail(err)
		return err
	}
	ru.last[t.ID] = fp
	ru.st.Kept++
	ru.st.LastCopy = e.At
	ru.st.Err = ""
	ru.st.Pruned += int64(prune(journalDir(ru.dir, ru.Name, t.ID), ".json", ru.Keep, time.Duration(ru.KeepFor), r.now()))
	return nil
}

func (r *Runner) journal(ctx context.Context, ru *rule) {
	// Follow first, then catch up, so nothing committed in between is missed. A task seen by
	// both is kept once, since the second copy matches the first.
	stop, err := r.src.FollowTasks(ctx, "backup-"+ru.Name, nil, func(t core.Task, seq uint64, d store.Delivery) {
		if !ru.Match.Matches(t) {
			d.Ack()
			return
		}
		if err := r.keep(ru, t, seq); err != nil {
			r.log.Error("backup could not keep a copy; it will be tried again", "rule", ru.Name, "task", t.ID, "err", err)
			d.Nak()
			return
		}
		d.Ack()
	})
	if err != nil {
		ru.mu.Lock()
		ru.fail(err)
		ru.mu.Unlock()
		r.log.Error("backup journal is off", "rule", ru.Name, "err", err)
		return
	}
	defer stop()
	tasks, _, err := r.src.LoadTasks(ctx)
	if err != nil && ctx.Err() == nil {
		r.log.Error("backup could not catch up", "rule", ru.Name, "err", err)
	}
	for _, s := range tasks {
		if ru.Match.Matches(s.Task) {
			_ = r.keep(ru, s.Task, 0)
		}
	}
	<-ctx.Done()
}

func (r *Runner) snapshots(ctx context.Context, ru *rule) {
	every := time.Duration(ru.Every)
	wait := every
	if names := Snapshots(ru.dir, ru.Name); len(names) > 0 {
		last, err := time.Parse(snapTime, trimSnap(filepath.Base(names[len(names)-1])))
		if err == nil {
			ru.mu.Lock()
			ru.st.LastSnapshot = last
			ru.mu.Unlock()
			wait = max(0, every-r.now().Sub(last))
		}
	} else {
		wait = 0
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := r.snapshot(ctx, ru); err != nil && ctx.Err() == nil {
			r.log.Error("backup snapshot failed", "rule", ru.Name, "err", err)
		}
		t.Reset(every)
	}
}

func trimSnap(name string) string {
	return name[:len(name)-len(".json.gz")]
}

func (r *Runner) snapshot(ctx context.Context, ru *rule) (string, error) {
	tasks, _, err := r.src.LoadTasks(ctx)
	if err != nil {
		ru.mu.Lock()
		ru.fail(err)
		ru.mu.Unlock()
		return "", err
	}
	s := Snapshot{Rule: ru.Name, At: r.now().UTC()}
	for _, st := range tasks {
		if !ru.Match.Matches(st.Task) {
			continue
		}
		e := Entry{Rule: ru.Name, At: s.At, Task: st.Task}
		if ru.Meta {
			if m, _, err := r.src.GetMeta(st.Task.ID); err == nil && len(m.Props) > 0 {
				e.Meta = &m
			}
		}
		s.Tasks = append(s.Tasks, e)
	}
	slices.SortFunc(s.Tasks, func(a, b Entry) int { return strings.Compare(a.Task.ID, b.Task.ID) })
	p, err := writeSnapshot(ru.dir, s)
	ru.mu.Lock()
	defer ru.mu.Unlock()
	if err != nil {
		ru.fail(err)
		return "", err
	}
	ru.st.LastSnapshot, ru.st.SnapshotSize, ru.st.Err = s.At, len(s.Tasks), ""
	ru.st.Pruned += int64(prune(snapshotDir(ru.dir, ru.Name), ".json.gz", ru.Keep, time.Duration(ru.KeepFor), r.now()))
	return p, nil
}

// SnapshotNow takes a snapshot for one rule, or for every rule with snapshots when name
// is empty, and returns the files written.
func (r *Runner) SnapshotNow(ctx context.Context, name string) ([]string, error) {
	var out []string
	found := false
	for _, ru := range r.rules {
		if name != "" && ru.Name != name {
			continue
		}
		found = true
		p, err := r.snapshot(ctx, ru)
		if err != nil {
			return out, err
		}
		out = append(out, p)
	}
	if !found {
		return nil, errors.New("no backup rule named " + name)
	}
	return out, nil
}
