package memory

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Grano22/aiculator/internal/codegraph"
)

// Note is one thing an agent learned about the codebase and left for the others: where
// something lives, why it is the way it is, a trap to avoid. It is short on purpose: it is
// read by every agent it is relevant to, so each word is paid for many times.
type Note struct {
	ID      string    `json:"id"`
	Text    string    `json:"text"`
	Files   []string  `json:"files,omitempty"`
	Symbols []string  `json:"symbols,omitempty"`
	Tags    []string  `json:"tags,omitempty"`
	Author  string    `json:"author"`
	Task    string    `json:"task,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// Uses counts how often it was recalled, which decides what goes when the book is full.
	Uses int `json:"uses"`
	// StaleSince is set when a file it is about changed after it was written. It may still
	// be right, but is worth checking.
	StaleSince time.Time `json:"stale_since,omitzero"`
	StaleFiles []string  `json:"stale_files,omitempty"`
}

// Limits keep notes short and the book bounded.
const (
	MinNoteText = 12
	MaxNoteText = 1500
	maxRefs     = 20
	maxTags     = 10
	// MaxNotes is how many notes are kept. Past it, the least used of the oldest go first.
	MaxNotes = 5000
	// sameNote is how much two notes' words must overlap to count as the same note.
	sameNote = 0.85
)

// ErrInvalid means a note was refused.
var ErrInvalid = errors.New("invalid note")

// NoteInput is what an agent writes.
type NoteInput struct {
	Text    string   `json:"text"`
	Files   []string `json:"files,omitempty"`
	Symbols []string `json:"symbols,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

func clean(list []string, max int, file bool) ([]string, error) {
	var out []string
	for _, v := range list {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if file {
			v = path.Clean(strings.ReplaceAll(v, "\\", "/"))
			if path.IsAbs(v) || v == ".." || strings.HasPrefix(v, "../") {
				return nil, fmt.Errorf("%w: %q is not a path inside the project", ErrInvalid, v)
			}
		}
		if len(v) > 300 {
			return nil, fmt.Errorf("%w: %q is too long", ErrInvalid, v[:40])
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if len(out) > max {
		return nil, fmt.Errorf("%w: at most %d", ErrInvalid, max)
	}
	return out, nil
}

func (in NoteInput) check() (NoteInput, error) {
	in.Text = strings.Join(strings.Fields(in.Text), " ")
	if n := len([]rune(in.Text)); n < MinNoteText || n > MaxNoteText {
		return in, fmt.Errorf("%w: text must be %d to %d characters, not %d", ErrInvalid, MinNoteText, MaxNoteText, n)
	}
	var err error
	if in.Files, err = clean(in.Files, maxRefs, true); err != nil {
		return in, err
	}
	if in.Symbols, err = clean(in.Symbols, maxRefs, false); err != nil {
		return in, err
	}
	if in.Tags, err = clean(in.Tags, maxTags, false); err != nil {
		return in, err
	}
	return in, nil
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "n-" + hex.EncodeToString(b[:])
}

// terms are what a note is found by: the words of its text, files, symbols and tags.
func (n Note) terms() map[string]int {
	tf := map[string]int{}
	add := func(s string, w int) {
		for _, t := range codegraph.Terms(s) {
			tf[t] += w
		}
	}
	add(n.Text, 1)
	for _, f := range n.Files {
		add(f, 2)
	}
	for _, s := range n.Symbols {
		add(s, 3)
	}
	for _, t := range n.Tags {
		add(t, 2)
	}
	return tf
}

// book is the notes with a search index, rebuilt as notes come and go. It is not safe for
// concurrent use; the Service guards it.
type book struct {
	notes map[string]*Note
	tf    map[string]map[string]int // Note id to its term counts.
	df    map[string]int
	total int // Sum of note lengths in terms, for BM25.
}

func newBook() *book {
	return &book{notes: map[string]*Note{}, tf: map[string]map[string]int{}, df: map[string]int{}}
}

func (b *book) put(n *Note) {
	b.remove(n.ID)
	b.notes[n.ID] = n
	tf := n.terms()
	b.tf[n.ID] = tf
	for t, c := range tf {
		b.df[t]++
		b.total += c
	}
}

func (b *book) remove(id string) {
	tf, ok := b.tf[id]
	if !ok {
		return
	}
	for t, c := range tf {
		b.df[t]--
		if b.df[t] <= 0 {
			delete(b.df, t)
		}
		b.total -= c
	}
	delete(b.tf, id)
	delete(b.notes, id)
}

// similar finds a note saying much the same, so that a repeat updates it instead of adding
// another copy for every agent to read.
func (b *book) similar(text string) *Note {
	want := map[string]bool{}
	for _, t := range codegraph.Terms(text) {
		want[t] = true
	}
	if len(want) == 0 {
		return nil
	}
	for _, n := range b.notes {
		have := map[string]bool{}
		for _, t := range codegraph.Terms(n.Text) {
			have[t] = true
		}
		common := 0
		for t := range want {
			if have[t] {
				common++
			}
		}
		if union := len(want) + len(have) - common; union > 0 && float64(common)/float64(union) >= sameNote {
			return n
		}
	}
	return nil
}

// Recalled is a note found for a question, with how well it matched.
type Recalled struct {
	Note
	Score float64 `json:"score"`
}

// recall ranks notes for a question with BM25, adds weight for notes about the files the
// asker names, and takes some off a note gone stale.
func (b *book) recall(query string, files []string, k int) []Recalled {
	if len(b.notes) == 0 {
		return nil
	}
	q := map[string]bool{}
	for _, t := range codegraph.Terms(query) {
		q[t] = true
	}
	for _, f := range files {
		for _, t := range codegraph.Terms(f) {
			q[t] = true
		}
	}
	const k1, bb = 1.2, 0.75
	n := float64(len(b.notes))
	avg := math.Max(float64(b.total)/n, 1)
	var out []Recalled
	for id, tf := range b.tf {
		note := b.notes[id]
		length := 0
		for _, c := range tf {
			length += c
		}
		score := 0.0
		for t := range q {
			f := float64(tf[t])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (n-float64(b.df[t])+0.5)/(float64(b.df[t])+0.5))
			score += idf * f * (k1 + 1) / (f + k1*(1-bb+bb*float64(length)/avg))
		}
		for _, f := range files {
			f = path.Clean(strings.ReplaceAll(f, "\\", "/"))
			for _, nf := range note.Files {
				switch {
				case nf == f:
					score += 3
				case path.Dir(nf) == path.Dir(f) || strings.HasPrefix(f, nf+"/") || strings.HasPrefix(nf, f+"/"):
					score += 1
				}
			}
		}
		if score <= 0 {
			continue
		}
		if !note.StaleSince.IsZero() {
			score *= 0.6
		}
		out = append(out, Recalled{Note: *note, Score: math.Round(score*100) / 100})
	}
	slices.SortFunc(out, func(a, c Recalled) int {
		return cmp.Or(cmp.Compare(c.Score, a.Score), c.Updated.Compare(a.Updated), strings.Compare(a.ID, c.ID))
	})
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}

// evict picks what goes when the book is full: of the older half, the least used.
func (b *book) evict() string {
	list := make([]*Note, 0, len(b.notes))
	for _, n := range b.notes {
		list = append(list, n)
	}
	slices.SortFunc(list, func(a, c *Note) int { return a.Updated.Compare(c.Updated) })
	old := list[:max(len(list)/2, 1)]
	slices.SortStableFunc(old, func(a, c *Note) int { return cmp.Compare(a.Uses, c.Uses) })
	return old[0].ID
}

// markStale flags the notes about any of the files that changed.
func (b *book) markStale(changed []string, now time.Time) []*Note {
	if len(changed) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, f := range changed {
		set[path.Clean(f)] = true
	}
	var touched []*Note
	for _, n := range b.notes {
		var hit []string
		for _, f := range n.Files {
			if set[f] {
				hit = append(hit, f)
			}
		}
		if len(hit) == 0 {
			continue
		}
		if n.StaleSince.IsZero() {
			n.StaleSince = now
		}
		for _, f := range hit {
			if !slices.Contains(n.StaleFiles, f) {
				n.StaleFiles = append(n.StaleFiles, f)
			}
		}
		touched = append(touched, n)
	}
	return touched
}
