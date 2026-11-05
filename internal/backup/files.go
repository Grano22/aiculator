package backup

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/meta"
)

// The layout, under the backup directory:
//
//	<rule>/journal/<task>/<seq>.json     one copy of one task, named by commit sequence
//	<rule>/snapshots/<time>.json.gz      every matching task at one moment
//
// Every file is written to a temporary name and renamed, so a crash leaves either the whole
// file or none of it.

// Entry is one kept copy of a task.
type Entry struct {
	Rule string     `json:"rule"`
	At   time.Time  `json:"at"`
	Seq  uint64     `json:"seq,omitempty"`
	Task core.Task  `json:"task"`
	Meta *meta.Meta `json:"meta,omitempty"`
}

// Snapshot is every matching task at one moment.
type Snapshot struct {
	Rule  string    `json:"rule"`
	At    time.Time `json:"at"`
	Tasks []Entry   `json:"tasks"`
}

// Version is a copy of a task that can be put back.
type Version struct {
	Rule  string     `json:"rule"`
	Kind  string     `json:"kind"` // journal or snapshot
	At    time.Time  `json:"at"`
	Seq   uint64     `json:"seq,omitempty"`
	State core.State `json:"state"`
	Path  string     `json:"path"`
}

const snapTime = "20060102T150405.000Z"

func journalDir(dir, rule, id string) string { return filepath.Join(dir, rule, "journal", id) }
func snapshotDir(dir, rule string) string    { return filepath.Join(dir, rule, "snapshots") }

func writeAtomic(p string, write func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := write(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func writeEntry(dir string, e Entry) (string, error) {
	p := filepath.Join(journalDir(dir, e.Rule, e.Task.ID), fmt.Sprintf("%020d.json", e.Seq))
	return p, writeAtomic(p, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(e)
	})
}

func writeSnapshot(dir string, s Snapshot) (string, error) {
	p := filepath.Join(snapshotDir(dir, s.Rule), s.At.UTC().Format(snapTime)+".json.gz")
	return p, writeAtomic(p, func(w io.Writer) error {
		z := gzip.NewWriter(w)
		if err := json.NewEncoder(z).Encode(s); err != nil {
			return err
		}
		return z.Close()
	})
}

// ReadEntry reads one journal copy.
func ReadEntry(p string) (Entry, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(b, &e); err != nil {
		return Entry{}, fmt.Errorf("%s: %w", p, err)
	}
	return e, nil
}

// ReadSnapshot reads one snapshot.
func ReadSnapshot(p string) (Snapshot, error) {
	f, err := os.Open(p)
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", p, err)
	}
	var s Snapshot
	if err := json.NewDecoder(z).Decode(&s); err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", p, err)
	}
	return s, nil
}

// ReadAny reads the copies of tasks in a file, journal or snapshot, chosen by its name.
func ReadAny(p string) ([]Entry, error) {
	if strings.HasSuffix(p, ".json.gz") {
		s, err := ReadSnapshot(p)
		return s.Tasks, err
	}
	e, err := ReadEntry(p)
	if err != nil {
		return nil, err
	}
	return []Entry{e}, nil
}

// files lists the names in a directory that end in suffix, oldest first. Names sort by
// time because they are a zero-padded sequence or a UTC timestamp.
func files(dir, suffix string) []string {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range des {
		if !d.IsDir() && strings.HasSuffix(d.Name(), suffix) && !strings.HasPrefix(d.Name(), ".") {
			out = append(out, d.Name())
		}
	}
	slices.Sort(out)
	return out
}

// prune keeps the newest keep files, and of those removes any older than keepFor, but
// never the newest one: an old copy is better than none.
func prune(dir, suffix string, keep int, keepFor time.Duration, now time.Time) int {
	names := files(dir, suffix)
	removed := 0
	for i, n := range names {
		newest := i == len(names)-1
		old := false
		if keepFor > 0 && !newest {
			if fi, err := os.Stat(filepath.Join(dir, n)); err == nil && now.Sub(fi.ModTime()) > keepFor {
				old = true
			}
		}
		if i < len(names)-keep || old {
			if os.Remove(filepath.Join(dir, n)) == nil {
				removed++
			}
		}
	}
	return removed
}

// Versions lists every kept copy of a task, oldest first, from journals and snapshots.
func Versions(dir, id string) ([]Version, error) {
	rules, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Version
	for _, r := range rules {
		if !r.IsDir() {
			continue
		}
		jd := journalDir(dir, r.Name(), id)
		for _, n := range files(jd, ".json") {
			e, err := ReadEntry(filepath.Join(jd, n))
			if err != nil {
				continue
			}
			out = append(out, Version{Rule: r.Name(), Kind: "journal", At: e.At, Seq: e.Seq, State: e.Task.State, Path: filepath.Join(jd, n)})
		}
		sd := snapshotDir(dir, r.Name())
		for _, n := range files(sd, ".json.gz") {
			s, err := ReadSnapshot(filepath.Join(sd, n))
			if err != nil {
				continue
			}
			for _, e := range s.Tasks {
				if e.Task.ID == id {
					out = append(out, Version{Rule: r.Name(), Kind: "snapshot", At: s.At, State: e.Task.State, Path: filepath.Join(sd, n)})
				}
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Version) int { return a.At.Compare(b.At) })
	return out, nil
}

// Snapshots lists a rule's snapshots, oldest first.
func Snapshots(dir, rule string) []string {
	sd := snapshotDir(dir, rule)
	var out []string
	for _, n := range files(sd, ".json.gz") {
		out = append(out, filepath.Join(sd, n))
	}
	return out
}

// Pick chooses the copy to restore: the newest at or before a time, when one is given.
func Pick(vs []Version, before time.Time) (Version, bool) {
	for i := len(vs) - 1; i >= 0; i-- {
		if before.IsZero() || !vs[i].At.After(before) {
			return vs[i], true
		}
	}
	return Version{}, false
}

// Find a task in the file a version points to.
func (v Version) Entry(id string) (Entry, error) {
	es, err := ReadAny(v.Path)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range es {
		if e.Task.ID == id {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("%s holds no copy of %s", v.Path, id)
}

func seqOf(name string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64)
	return n
}
