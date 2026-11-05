// Package backup keeps copies of chosen tasks outside the store: a journal of every change
// to the tasks a rule matches, and snapshots of all of them at an interval.
//
// It runs beside the swarm, like the connectors. It reads the store's commit stream and
// writes files, and never sits on the path of a claim or a commit. A backup that falls
// behind or fails costs only the backup.
package backup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Grano22/aiculator/internal/core"
)

// ConfigFiles are where the backup file is looked for, relative to the project root, next
// to the sources file.
var ConfigFiles = []string{".agents/backups.yaml", ".agent/backups.yaml"}

// ErrNoConfig means the project has no backup file.
var ErrNoConfig = errors.New("no backup file")

// DefaultDir is where backups go when the file does not say. It is inside the data
// directory, so removing that directory removes them too; a separate disk is better.
const DefaultDir = ".swarm/backups"

// Config is the backup file.
type Config struct {
	// Dir is where backups are written, relative to the project root unless absolute.
	Dir   string `yaml:"dir"`
	Rules []Rule `yaml:"rules"`
	// Path is the file the config came from, and Root what relative paths are taken from.
	Path string `yaml:"-"`
	Root string `yaml:"-"`
}

// Rule says which tasks to keep copies of, and how.
type Rule struct {
	Name  string `yaml:"name"`
	Match Match  `yaml:"match"`
	// Journal keeps a copy each time a matching task changes in a way that matters. A
	// heartbeat that only moves deadlines is not kept.
	Journal bool `yaml:"journal"`
	// Every takes a snapshot of all matching tasks at this interval, such as 30m or 1d.
	Every Duration `yaml:"every"`
	// Keep is how many journal copies per task, and how many snapshots, to keep.
	Keep int `yaml:"keep"`
	// KeepFor removes copies older than this, but never the newest.
	KeepFor Duration `yaml:"keep_for"`
	// Meta also keeps each task's metadata.
	Meta bool `yaml:"meta"`
}

// Match picks tasks. Each field that is set must match; within a list, any entry may.
type Match struct {
	All bool `yaml:"all"`
	// IDs are glob patterns, such as pay-* or release-?.
	IDs []string `yaml:"ids"`
	// Labels must all be present; a value is a glob pattern, so * means any value.
	Labels map[string]string `yaml:"labels"`
	Tags   []string          `yaml:"tags"`
	States []core.State      `yaml:"states"`
	// MinPriority matches tasks at or above this priority.
	MinPriority *int `yaml:"min_priority"`
}

// Duration reads Go durations, plus d for days, such as 90m, 12h or 7d.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// ParseDuration reads a duration that may end in d for days.
func ParseDuration(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("bad duration %q", s)
	}
	return d, nil
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// MinEvery stops a snapshot interval that would do little but write files.
const MinEvery = time.Minute

// DefaultKeep is how many copies are kept when a rule does not say.
const DefaultKeep = 20

// Find reads the project's backup file.
func Find(root string) (*Config, error) {
	for _, name := range ConfigFiles {
		p := filepath.Join(root, name)
		if _, err := os.Stat(p); err == nil {
			return Load(p, root)
		}
	}
	return nil, ErrNoConfig
}

// Load reads and checks a backup file. Unknown fields are errors, so that a misspelled
// rule fails loudly instead of quietly backing up nothing.
func Load(p, root string) (*Config, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return Parse(b, p, root)
}

// Parse checks a backup file's contents.
func Parse(b []byte, p, root string) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	c.Path, c.Root = p, root
	if c.Dir == "" {
		c.Dir = DefaultDir
	}
	if len(c.Rules) == 0 {
		return nil, fmt.Errorf("%s: no rules", p)
	}
	seen := map[string]bool{}
	for i := range c.Rules {
		r := &c.Rules[i]
		if err := r.check(); err != nil {
			return nil, fmt.Errorf("%s: rule %q: %w", p, r.Name, err)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("%s: rule %q appears twice", p, r.Name)
		}
		seen[r.Name] = true
	}
	return &c, nil
}

func (r *Rule) check() error {
	if !nameRE.MatchString(r.Name) {
		return errors.New("a name is lower case letters, digits, - and _")
	}
	if !r.Journal && r.Every == 0 {
		return errors.New("set journal: true, every, or both")
	}
	if r.Every != 0 && time.Duration(r.Every) < MinEvery {
		return fmt.Errorf("every must be at least %v", MinEvery)
	}
	if r.Keep < 0 {
		return errors.New("keep cannot be negative")
	}
	if r.Keep == 0 {
		r.Keep = DefaultKeep
	}
	m := r.Match
	if !m.All && len(m.IDs) == 0 && len(m.Labels) == 0 && len(m.Tags) == 0 && len(m.States) == 0 && m.MinPriority == nil {
		return errors.New("match nothing in particular: set all: true, or ids, labels, tags, states or min_priority")
	}
	for _, g := range m.IDs {
		if _, err := path.Match(g, ""); err != nil {
			return fmt.Errorf("bad id pattern %q", g)
		}
	}
	for k, g := range m.Labels {
		if _, err := path.Match(g, ""); err != nil {
			return fmt.Errorf("bad pattern %q for label %s", g, k)
		}
	}
	for _, s := range m.States {
		if !slices.Contains(core.AllStates, s) {
			return fmt.Errorf("unknown state %q", s)
		}
	}
	return nil
}

// Matches reports whether a rule covers a task.
func (m Match) Matches(t core.Task) bool {
	if m.All {
		return true
	}
	if len(m.IDs) > 0 && !slices.ContainsFunc(m.IDs, func(g string) bool { ok, _ := path.Match(g, t.ID); return ok }) {
		return false
	}
	for k, g := range m.Labels {
		v, ok := t.Labels[k]
		if !ok {
			return false
		}
		if hit, _ := path.Match(g, v); !hit {
			return false
		}
	}
	if len(m.Tags) > 0 && !slices.ContainsFunc(m.Tags, func(g string) bool { return slices.Contains(t.Tags, g) }) {
		return false
	}
	if len(m.States) > 0 && !slices.Contains(m.States, t.State) {
		return false
	}
	if m.MinPriority != nil && t.Priority < *m.MinPriority {
		return false
	}
	return true
}

// Abs returns the backup directory as an absolute path.
func (c *Config) Abs() string {
	if filepath.IsAbs(c.Dir) {
		return c.Dir
	}
	return filepath.Join(c.Root, c.Dir)
}

// Rule returns the rule with that name.
func (c *Config) Rule(name string) (Rule, bool) {
	i := slices.IndexFunc(c.Rules, func(r Rule) bool { return r.Name == name })
	if i < 0 {
		return Rule{}, false
	}
	return c.Rules[i], true
}
