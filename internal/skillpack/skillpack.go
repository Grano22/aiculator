// Package skillpack manages skills in one place. Skills live in .agents/skills, the
// cross-client convention from agentskills.io. Harnesses that scan it need nothing;
// the rest get a symlink, so there is never a second copy to keep in sync.
package skillpack

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"aiculation/internal/harness"
)

//go:embed all:skills
var bundled embed.FS

// CanonicalDir is where skills live, relative to the project or home directory.
const CanonicalDir = ".agents/skills"

// aliasDir is also read as a source, for projects that use the singular name. Skills
// found there still reach .agents/skills, because that is the path harnesses scan.
const aliasDir = ".agent/skills"

// managedMarker identifies a copy this tool made, and so may replace or delete.
const managedMarker = ".aiculator-managed"

// Options configures an operation.
type Options struct {
	Root      string // Project directory.
	Home      string // Home directory; empty means the real one.
	Global    bool   // Use the home directory instead of the project.
	Patterns  []string
	Harnesses []harness.Harness
	Paths     []string // Extra target directories.
	Sources   []string // Extra source directories.
	Copy      bool     // Copy rather than link.
	Force     bool     // Replace links and managed copies, and refresh bundled skills.
	Purge     bool     // Uninstall also deletes the skill from .agents/skills.
}

func (o Options) base() string {
	if !o.Global {
		return o.Root
	}
	if o.Home != "" {
		return o.Home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return o.Root
	}
	return home
}

func (o Options) canonical() string { return filepath.Join(o.base(), filepath.FromSlash(CanonicalDir)) }

func (o Options) expand(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home := o.Home
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		return filepath.Join(home, rest)
	}
	if !filepath.IsAbs(p) {
		return filepath.Join(o.Root, p)
	}
	return p
}

// target is where a harness wants skills. Empty means it reads .agents/skills itself.
func (o Options) target(h harness.Harness) string {
	if o.Global {
		if h.ReadsGlobalAgents {
			return ""
		}
		return o.expand(h.GlobalSkillsDir)
	}
	if h.ReadsAgents {
		return ""
	}
	return filepath.Join(o.Root, filepath.FromSlash(h.SkillsDir))
}

// Action reports one thing an operation did.
type Action struct {
	Skill  string
	Target string
	What   string // created, kept, refreshed, linked, copied, ok, native, conflict, invalid, removed.
	Detail string
}

// available maps every known skill to its source directory; "" means bundled.
func (o Options) available() (map[string]string, error) {
	out := map[string]string{}
	entries, err := fs.ReadDir(bundled, "skills")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		out[e.Name()] = ""
	}
	sources := []string{filepath.Join(o.base(), filepath.FromSlash(aliasDir)), o.canonical()}
	for _, s := range o.Sources {
		sources = append(sources, o.expand(s))
	}
	// Later sources win: a custom source beats .agents/skills, which beats .agent/skills.
	for _, dir := range sources {
		list, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range list {
			p := filepath.Join(dir, e.Name())
			if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err == nil {
				out[e.Name()] = p
			}
		}
	}
	return out, nil
}

// Match returns the skills a pattern set selects. Patterns use shell wildcards:
// * for any run of characters, ? for one character, and [abc] for a set.
func Match(patterns, names []string) ([]string, error) {
	seen := map[string]bool{}
	for _, p := range patterns {
		matched := false
		for _, n := range names {
			ok, err := path.Match(p, n)
			if err != nil {
				return nil, fmt.Errorf("bad pattern %q: %w", p, err)
			}
			if ok {
				seen[n] = true
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("no skill matches %q; available: %s", p, strings.Join(names, ", "))
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// NormalizeShellStar undoes shell expansion. An unquoted * reaches the program as the
// list of files in the working directory, so when the arguments are exactly that
// list, the user meant "all". Anything else is left as written.
func NormalizeShellStar(args []string, dir string) []string {
	if len(args) < 2 {
		return args
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return args
	}
	var visible []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			visible = append(visible, e.Name())
		}
	}
	got := slices.Clone(args)
	sort.Strings(got)
	sort.Strings(visible)
	if slices.Equal(got, visible) {
		return []string{"*"}
	}
	return args
}

// Install brings the selected skills into .agents/skills and links them to each harness.
func Install(o Options) ([]Action, error) {
	if runtime.GOOS == "windows" {
		o.Copy = true // Creating symlinks needs special rights on Windows.
	}
	avail, err := o.available()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(avail))
	for n := range avail {
		names = append(names, n)
	}
	sort.Strings(names)
	selected, err := Match(o.Patterns, names)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(o.canonical(), 0o755); err != nil {
		return nil, err
	}
	var acts []Action
	for _, name := range selected {
		a, err := o.ensureCanonical(name, avail[name])
		if err != nil {
			return acts, err
		}
		acts = append(acts, a)
		dir := filepath.Join(o.canonical(), name)
		// A broken skill must not spread to every harness.
		if IssuesHaveErrors(Lint(dir)) {
			acts = append(acts, Action{name, dir, "invalid", "fix it, then run aiculator skill --lint"})
			continue
		}
		done := map[string]bool{}
		for _, h := range o.Harnesses {
			tdir := o.target(h)
			if tdir == "" {
				acts = append(acts, Action{name, h.ID, "native", "reads " + CanonicalDir})
				continue
			}
			if done[tdir] {
				continue
			}
			done[tdir] = true
			acts = append(acts, o.place(name, dir, tdir, h.ID))
		}
		for _, p := range o.Paths {
			tdir := o.expand(p)
			if !done[tdir] {
				done[tdir] = true
				acts = append(acts, o.place(name, dir, tdir, tdir))
			}
		}
	}
	return acts, nil
}

func (o Options) ensureCanonical(name, src string) (Action, error) {
	dir := filepath.Join(o.canonical(), name)
	if src == dir {
		// It exists already. Only an explicit --force replaces a bundled skill, since
		// the user may have edited it; a skill of their own is never touched.
		if _, err := bundled.ReadFile(path.Join("skills", name, "SKILL.md")); err != nil || !o.Force {
			return Action{name, dir, "kept", ""}, nil
		}
		if err := writeBundled(name, dir); err != nil {
			return Action{}, err
		}
		return Action{name, dir, "refreshed", ""}, nil
	}
	if src != "" { // It lives elsewhere, such as .agent/skills or a custom source.
		if _, err := os.Lstat(dir); err == nil {
			if o.isLinkTo(dir, src) {
				return Action{name, dir, "kept", ""}, nil
			}
			return Action{name, dir, "conflict", "already exists and is not a link to " + src}, nil
		}
		return Action{name, dir, "linked", "from " + src}, link(src, dir)
	}
	_, statErr := os.Lstat(dir)
	exists := statErr == nil
	if exists && !o.Force {
		return Action{name, dir, "kept", ""}, nil
	}
	if err := writeBundled(name, dir); err != nil {
		return Action{}, err
	}
	if exists {
		return Action{name, dir, "refreshed", ""}, nil
	}
	return Action{name, dir, "created", ""}, nil
}

func writeBundled(name, dir string) error {
	root := path.Join("skills", name)
	return fs.WalkDir(bundled, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(p))
		if err != nil {
			return err
		}
		out := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := bundled.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
}

func (o Options) isLinkTo(linkPath, dest string) bool {
	got, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		return false
	}
	want, err := filepath.EvalSymlinks(dest)
	return err == nil && got == want
}

func link(dest, at string) error {
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		return err
	}
	// A relative link keeps working when the repository is moved or cloned.
	realDir, err := filepath.EvalSymlinks(filepath.Dir(at))
	if err != nil {
		return err
	}
	realDest, err := filepath.EvalSymlinks(dest)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(realDir, realDest)
	if err != nil {
		rel = realDest
	}
	return os.Symlink(rel, at)
}

// place puts one skill into one target directory. It never removes anything it
// did not make.
func (o Options) place(name, src, tdir, label string) Action {
	at := filepath.Join(tdir, name)
	fi, err := os.Lstat(at)
	switch {
	case err == nil && fi.Mode()&os.ModeSymlink != 0:
		if !o.Copy && o.isLinkTo(at, src) {
			return Action{name, label, "ok", at}
		}
		if !o.Force {
			return Action{name, label, "conflict", at + " links elsewhere; use --force to replace it"}
		}
		if err := os.Remove(at); err != nil {
			return Action{name, label, "conflict", err.Error()}
		}
	case err == nil:
		if _, merr := os.Stat(filepath.Join(at, managedMarker)); merr != nil {
			return Action{name, label, "conflict", at + " is your own directory and was left alone"}
		}
		if err := os.RemoveAll(at); err != nil {
			return Action{name, label, "conflict", err.Error()}
		}
	}
	if o.Copy {
		if err := copyTree(src, at); err != nil {
			return Action{name, label, "conflict", err.Error()}
		}
		return Action{name, label, "copied", at}
	}
	if err := link(src, at); err != nil {
		return Action{name, label, "conflict", err.Error()}
	}
	return Action{name, label, "linked", at}
}

func copyTree(src, dst string) error {
	realSrc, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	err = filepath.WalkDir(realSrc, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(realSrc, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // A symlink in a skill could point anywhere, so it is not copied.
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, info.Mode().Perm())
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, managedMarker), []byte("Copied by aiculator from "+realSrc+"\n"), 0o644)
}

// Uninstall removes the selected skills from harnesses and extra paths, touching only
// links and managed copies. With Purge it also deletes them from .agents/skills.
func Uninstall(o Options) ([]Action, error) {
	avail, err := o.available()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(avail))
	for n := range avail {
		names = append(names, n)
	}
	sort.Strings(names)
	selected, err := Match(o.Patterns, names)
	if err != nil {
		return nil, err
	}
	var acts []Action
	for _, name := range selected {
		dirs := map[string]string{}
		for _, h := range o.Harnesses {
			if t := o.target(h); t != "" {
				dirs[t] = h.ID
			}
		}
		for _, p := range o.Paths {
			dirs[o.expand(p)] = p
		}
		for dir, label := range dirs {
			at := filepath.Join(dir, name)
			fi, err := os.Lstat(at)
			if err != nil {
				continue
			}
			_, merr := os.Stat(filepath.Join(at, managedMarker))
			isLink := fi.Mode()&os.ModeSymlink != 0
			if !isLink && merr != nil {
				acts = append(acts, Action{name, label, "conflict", at + " is your own directory and was left alone"})
				continue
			}
			if err := os.RemoveAll(at); err != nil {
				return acts, err
			}
			acts = append(acts, Action{name, label, "removed", at})
		}
		if o.Purge {
			at := filepath.Join(o.canonical(), name)
			if _, err := os.Lstat(at); err == nil {
				if err := os.RemoveAll(at); err != nil {
					return acts, err
				}
				acts = append(acts, Action{name, at, "removed", "deleted from " + CanonicalDir})
			}
		}
	}
	return acts, nil
}

// Row is one skill in a listing.
type Row struct {
	Name, Source, Version, Kind string
	Installed                   bool
	Errors, Warnings            int
	Harnesses                   []string // Harnesses that can see it.
}

// List describes every known skill.
func List(o Options) ([]Row, error) {
	avail, err := o.available()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(avail))
	for n := range avail {
		names = append(names, n)
	}
	sort.Strings(names)
	var rows []Row
	for _, n := range names {
		dir := filepath.Join(o.canonical(), n)
		_, statErr := os.Stat(filepath.Join(dir, "SKILL.md"))
		row := Row{Name: n, Source: "bundled", Installed: statErr == nil}
		check := dir
		switch {
		case avail[n] != "":
			row.Source = avail[n]
			check = avail[n]
		case statErr != nil:
			check = ""
		}
		if check != "" {
			for _, is := range Lint(check) {
				switch is.Level {
				case LevelError:
					row.Errors++
				case LevelWarn:
					row.Warnings++
				}
			}
			if fm, err := parseSkill(filepath.Join(check, "SKILL.md")); err == nil {
				row.Version, _ = fm.meta["version"].(string)
				row.Kind, _ = fm.meta["aiculator.kind"].(string)
			}
		} else if fm, err := parseBundled(n); err == nil {
			row.Version, _ = fm.meta["version"].(string)
			row.Kind, _ = fm.meta["aiculator.kind"].(string)
		}
		if row.Installed {
			for _, h := range o.Harnesses {
				t := o.target(h)
				if t == "" || o.isLinkTo(filepath.Join(t, n), dir) || hasManaged(filepath.Join(t, n)) {
					row.Harnesses = append(row.Harnesses, h.ID)
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func hasManaged(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, managedMarker))
	return err == nil
}

// New creates a skill in .agents/skills with full metadata.
func New(o Options, name, description string) (string, error) {
	if !nameRE.MatchString(name) || strings.Contains(name, "--") || len(name) > 64 {
		return "", fmt.Errorf("invalid skill name %q: use lowercase letters, numbers and single hyphens", name)
	}
	dir := filepath.Join(o.canonical(), name)
	if _, err := os.Lstat(dir); err == nil {
		return "", fmt.Errorf("%s already exists", dir)
	}
	if description == "" {
		description = "TODO: say what this skill does and when to use it, with keywords that match how people ask."
	}
	body := fmt.Sprintf(`---
name: %s
description: %q
# license: MIT
# compatibility: Requires git and internet access.
# allowed-tools: Read Bash(git:*)
# disable-model-invocation: true
# argument-hint: "[argument]"
metadata:
  version: "0.1.0"
---

# %s

Step-by-step instructions go here. Keep this file under 500 lines and move long
reference material into references/.
`, name, description, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644)
}
