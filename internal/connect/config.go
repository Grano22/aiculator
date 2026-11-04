// Package connect brings work in from other platforms and keeps it in step with them. It
// sits entirely beside the core: importing goes through the same API as any other client,
// and syncing reads the commit stream after the fact, so neither can slow a claim.
package connect

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Grano22/aiculator/internal/core"
)

// ConfigFiles are where the sources file is looked for, relative to the project root.
var ConfigFiles = []string{".agents/sources.yaml", ".agent/sources.yaml"}

// Config is the sources file.
type Config struct {
	Sources []*Source `yaml:"sources"`
	// Base is the directory that relative file paths start from.
	Base string `yaml:"-"`
}

// Source describes one external system and how its items become tasks. It has no field for
// a secret: credentials are named by environment variable, so this file can be committed.
type Source struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"` // jira, notion, xlsx or csv.

	URL          string `yaml:"url,omitempty"`
	Auth         Auth   `yaml:"auth,omitempty"`
	AllowPrivate bool   `yaml:"allow_private,omitempty"` // For an on-premises service.

	Query      string `yaml:"query,omitempty"`       // JQL.
	DataSource string `yaml:"data_source,omitempty"` // Notion data source ID.
	Filter     any    `yaml:"filter,omitempty"`      // Notion filter object.
	APIVersion string `yaml:"api_version,omitempty"` // Notion-Version header.
	FetchBody  bool   `yaml:"fetch_body,omitempty"`  // Read each Notion page's text too.

	Path      string   `yaml:"path,omitempty"`       // xlsx or csv file.
	Sheet     string   `yaml:"sheet,omitempty"`      // xlsx sheet; the first if empty.
	HeaderRow int      `yaml:"header_row,omitempty"` // 1-based; 1 if empty.
	Regions   []Region `yaml:"regions,omitempty"`    // Treat cell ranges as lists.

	Fields   Fields      `yaml:"fields"`
	SkipWhen []When      `yaml:"skip_when,omitempty"`
	Defaults Defaults    `yaml:"defaults,omitempty"`
	Sync     *SyncConfig `yaml:"sync,omitempty"`
}

// Auth names the environment variables that hold credentials.
type Auth struct {
	EmailEnv string `yaml:"email_env,omitempty"` // With a token, means HTTP Basic, as Jira Cloud uses.
	TokenEnv string `yaml:"token_env,omitempty"`
}

// Region treats a range of cells as a list of items, for a sheet laid out as a grid, such
// as an Eisenhower matrix with one column per quadrant.
type Region struct {
	Name  string `yaml:"name"`
	Range string `yaml:"range"` // Such as A3:A40.
}

// When matches an item when every named field has the given value, ignoring case.
type When map[string]string

// Rule gives a value when its condition holds.
type Rule struct {
	When  When `yaml:"when"`
	Value any  `yaml:"value"`
}

// FieldSpec says where one attribute comes from: a field name, optionally translated by
// map, or a list of rules that can look at several fields at once.
type FieldSpec struct {
	From    string         `yaml:"from,omitempty"`
	Map     map[string]any `yaml:"map,omitempty"`
	Rules   []Rule         `yaml:"rules,omitempty"`
	Default any            `yaml:"default,omitempty"`
}

// UnmarshalYAML lets a plain string stand for just a field name.
func (f *FieldSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&f.From)
	}
	type plain FieldSpec
	dec := plain{}
	if err := n.Decode(&dec); err != nil {
		return err
	}
	*f = FieldSpec(dec)
	return nil
}

// Fields maps an item onto a task. There is deliberately no entry for checks, touches,
// budget or review: checks are commands the swarm runs, so whoever can edit a ticket must
// not be able to choose them. Those come only from Defaults, in this reviewed file.
type Fields struct {
	Key      *FieldSpec           `yaml:"key,omitempty"`
	Title    *FieldSpec           `yaml:"title,omitempty"`
	Spec     *FieldSpec           `yaml:"spec,omitempty"`
	Priority *FieldSpec           `yaml:"priority,omitempty"`
	Tier     *FieldSpec           `yaml:"tier,omitempty"`
	Tags     *FieldSpec           `yaml:"tags,omitempty"`
	Status   *FieldSpec           `yaml:"status,omitempty"`
	Labels   map[string]FieldSpec `yaml:"labels,omitempty"`
	Props    map[string]FieldSpec `yaml:"props,omitempty"`
}

// Defaults apply to every task from this source.
type Defaults struct {
	Spec          string            `yaml:"spec,omitempty"`
	Tier          int               `yaml:"tier,omitempty"`
	Priority      int               `yaml:"priority,omitempty"`
	Tags          []string          `yaml:"tags,omitempty"`
	Labels        map[string]string `yaml:"labels,omitempty"`
	Checks        []string          `yaml:"checks,omitempty"`
	Touches       []string          `yaml:"touches,omitempty"`
	RequireReview bool              `yaml:"require_review,omitempty"`
	AllowEmpty    bool              `yaml:"allow_empty,omitempty"`
	Budget        int64             `yaml:"budget,omitempty"`
	Aging         []string          `yaml:"aging,omitempty"` // Such as 1h=5.
}

// SyncConfig turns on keeping a source in step after the import. See sync.go.
type SyncConfig struct {
	Direction string     `yaml:"direction,omitempty"` // both, push or pull. both if empty.
	Create    bool       `yaml:"create,omitempty"`    // Pull in new items too.
	Push      PushConfig `yaml:"push,omitempty"`
	Pull      PullConfig `yaml:"pull,omitempty"`
	// Status says which remote status each task state becomes. A state left out is not sent.
	Status map[string]string `yaml:"status,omitempty"`
	// OnRemote says what a remote status does here. The only action is cancel.
	OnRemote map[string]string `yaml:"on_remote,omitempty"`
	// HookSecretEnv names the variable that holds the webhook signing secret.
	HookSecretEnv string `yaml:"hook_secret_env,omitempty"`
}

// PushConfig says how changes reach the other system.
type PushConfig struct {
	Mode    string        `yaml:"mode,omitempty"`    // realtime or interval. realtime if empty.
	Every   time.Duration `yaml:"every,omitempty"`   // For interval.
	Updates string        `yaml:"updates,omitempty"` // partial sends each change; last sends only the end.
}

// PullConfig says how changes come back.
type PullConfig struct {
	Mode  string        `yaml:"mode,omitempty"` // once, interval or webhook. interval if empty.
	Every time.Duration `yaml:"every,omitempty"`
}

var (
	sourceNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	envNameRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ErrNoConfig means no sources file exists.
var ErrNoConfig = errors.New("no sources file: create .agents/sources.yaml")

// Find loads the sources file under root. A missing file is ErrNoConfig.
func Find(root string) (*Config, error) {
	for _, name := range ConfigFiles {
		p := filepath.Join(root, name)
		if _, err := os.Stat(p); err == nil {
			return Load(p, root)
		}
	}
	return nil, ErrNoConfig
}

// Load reads and checks one sources file. Unknown fields are errors, so a misspelling, or
// an attempt to map checks from outside, fails loudly rather than being ignored.
func Load(path, base string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Base = base
	seen := map[string]bool{}
	for _, s := range c.Sources {
		if err := s.check(); err != nil {
			return nil, fmt.Errorf("%s: source %q: %w", path, s.Name, err)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("%s: source %q is listed twice", path, s.Name)
		}
		seen[s.Name] = true
	}
	return &c, nil
}

// Get finds a source by name.
func (c *Config) Get(name string) (*Source, error) {
	for _, s := range c.Sources {
		if s.Name == name {
			return s, nil
		}
	}
	names := make([]string, 0, len(c.Sources))
	for _, s := range c.Sources {
		names = append(names, s.Name)
	}
	return nil, fmt.Errorf("no source named %q; these exist: %s", name, strings.Join(names, ", "))
}

func (s *Source) check() error {
	if !sourceNameRE.MatchString(s.Name) {
		return errors.New("name: use lowercase letters, digits and hyphens")
	}
	switch s.Kind {
	case "jira":
		if s.URL == "" {
			return errors.New("jira needs url")
		}
		if s.Query == "" {
			return errors.New("jira needs query, in JQL; a search with no limit could import a whole site")
		}
	case "notion":
		if s.DataSource == "" {
			return errors.New("notion needs data_source")
		}
	case "xlsx", "csv":
		if s.Path == "" {
			return fmt.Errorf("%s needs path", s.Kind)
		}
	default:
		return fmt.Errorf("unknown kind %q: use jira, notion, xlsx or csv", s.Kind)
	}
	for _, e := range []string{s.Auth.EmailEnv, s.Auth.TokenEnv} {
		if e != "" && !envNameRE.MatchString(e) {
			return fmt.Errorf("auth must name an environment variable, not hold a value: %q", redactEnv(e))
		}
	}
	if s.Fields.Title == nil && len(s.Regions) == 0 {
		return errors.New("fields.title is required")
	}
	if _, err := core.ParseAging(s.Defaults.Aging); err != nil {
		return fmt.Errorf("defaults.aging: %w", err)
	}
	if s.Sync != nil {
		return s.Sync.check(s.Kind)
	}
	return nil
}

func redactEnv(string) string { return "[hidden]" }

func (c *SyncConfig) check(kind string) error {
	if !slices.Contains([]string{"", "both", "push", "pull"}, c.Direction) {
		return fmt.Errorf("sync.direction %q", c.Direction)
	}
	if !slices.Contains([]string{"", "realtime", "interval"}, c.Push.Mode) {
		return fmt.Errorf("sync.push.mode %q", c.Push.Mode)
	}
	if c.Push.Mode == "interval" && c.Push.Every <= 0 {
		return errors.New("sync.push.every is required for interval")
	}
	if !slices.Contains([]string{"", "partial", "last"}, c.Push.Updates) {
		return fmt.Errorf("sync.push.updates %q: use partial or last", c.Push.Updates)
	}
	if !slices.Contains([]string{"", "once", "interval", "webhook"}, c.Pull.Mode) {
		return fmt.Errorf("sync.pull.mode %q", c.Pull.Mode)
	}
	if c.Pull.Mode == "webhook" && c.HookSecretEnv == "" {
		return errors.New("sync.hook_secret_env is required for webhook: an unsigned hook could cancel tasks")
	}
	if (kind == "xlsx" || kind == "csv") && c.Direction != "pull" {
		return fmt.Errorf("%s files can only be read: set sync.direction to pull", kind)
	}
	for state := range c.Status {
		if !slices.Contains(core.AllStates, core.State(state)) {
			return fmt.Errorf("sync.status: unknown state %q", state)
		}
	}
	for remote, action := range c.OnRemote {
		if action != "cancel" {
			return fmt.Errorf("sync.on_remote[%s]: the only action is cancel", remote)
		}
	}
	return nil
}

// AbsPath resolves a file source's path against the project root.
func (c *Config) AbsPath(s *Source) string {
	if filepath.IsAbs(s.Path) {
		return s.Path
	}
	return filepath.Join(c.Base, s.Path)
}

// SyncSummary says in a few words how a source is kept in step.
func (s *Source) SyncSummary() string {
	if s.Sync == nil {
		return "import only"
	}
	dir := s.Sync.Direction
	if dir == "" {
		dir = "both"
	}
	return dir
}
