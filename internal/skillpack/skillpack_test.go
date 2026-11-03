package skillpack

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Grano22/aiculator/internal/harness"
)

func harnesses(ids ...string) []harness.Harness {
	var out []harness.Harness
	for _, id := range ids {
		h, ok := harness.Get(id)
		if !ok {
			panic(id)
		}
		out = append(out, h)
	}
	return out
}

func opts(t *testing.T, patterns ...string) Options {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Options{Root: root, Home: home, Patterns: patterns, Harnesses: harnesses("claude", "opencode", "auggie")}
}

func what(acts []Action, skill, target string) string {
	for _, a := range acts {
		if a.Skill == skill && a.Target == target {
			return a.What
		}
	}
	return ""
}

func writeSkill(t *testing.T, dir, name, fm string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: A test skill that does something useful for testing.\n" + fm + "---\n\n# Hi\n"
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMatchWildcards(t *testing.T) {
	names := []string{"deploy-prod", "deploy-staging", "lint", "swarm-join", "swarm-plan"}
	cases := map[string][]string{
		"*":            names,
		"swarm-*":      {"swarm-join", "swarm-plan"},
		"*-prod":       {"deploy-prod"},
		"*-*":          {"deploy-prod", "deploy-staging", "swarm-join", "swarm-plan"},
		"lin?":         {"lint"},
		"deploy-[ps]*": {"deploy-prod", "deploy-staging"},
		"lint":         {"lint"},
	}
	for p, want := range cases {
		got, err := Match([]string{p}, names)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s = %v, %v; want %v", p, got, err, want)
		}
	}
	if _, err := Match([]string{"nope-*"}, names); err == nil || !strings.Contains(err.Error(), "available") {
		t.Errorf("no match: %v", err)
	}
	if _, err := Match([]string{"[bad"}, names); err == nil {
		t.Error("bad pattern accepted")
	}
	if got, _ := Match([]string{"lint", "lin*", "*t"}, names); !slices.Equal(got, []string{"lint"}) {
		t.Errorf("overlapping patterns gave %v", got)
	}
}

// An unquoted * arrives as the directory listing. It must mean "all", not those files.
func TestNormalizeShellStar(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"README.md", "bin", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := NormalizeShellStar([]string{"README.md", "bin"}, dir); !slices.Equal(got, []string{"*"}) {
		t.Errorf("expanded star: %v", got)
	}
	for _, args := range [][]string{{"README.md"}, {"swarm-join", "swarm-plan"}, {"README.md", "bin", "extra"}, {"*"}} {
		if got := NormalizeShellStar(args, dir); !slices.Equal(got, args) {
			t.Errorf("%v became %v", args, got)
		}
	}
}

func TestInstallLinksOnlyWhereNeeded(t *testing.T) {
	o := opts(t, "*")
	acts, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if what(acts, "swarm-join", "opencode") != "native" {
		t.Errorf("opencode reads .agents/skills: %v", acts)
	}
	if what(acts, "swarm-join", "claude") != "linked" {
		t.Errorf("claude: %v", acts)
	}
	link := filepath.Join(o.Root, ".claude/skills/swarm-join")
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected a symlink: %v", err)
	}
	if dest, _ := os.Readlink(link); filepath.IsAbs(dest) {
		t.Errorf("link should be relative so the repository can move: %s", dest)
	}
	b, err := os.ReadFile(filepath.Join(link, "SKILL.md"))
	if err != nil || !strings.Contains(string(b), "name: swarm-join") {
		t.Fatalf("link does not resolve: %v", err)
	}
	// One place: editing the canonical file shows up through every link.
	canonical := filepath.Join(o.Root, ".agents/skills/swarm-join/SKILL.md")
	if err := os.WriteFile(canonical, append(b, []byte("EDITED\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(filepath.Join(link, "SKILL.md")); !strings.Contains(string(b2), "EDITED") {
		t.Error("link does not reflect the canonical file")
	}
	// Reinstalling keeps the edit and is idempotent.
	acts, err = Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if what(acts, "swarm-join", "claude") != "ok" {
		t.Errorf("reinstall: %v", acts)
	}
	if b3, _ := os.ReadFile(canonical); !strings.Contains(string(b3), "EDITED") {
		t.Error("reinstall overwrote a user edit")
	}
	// --force refreshes from the bundle.
	o.Force = true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if b4, _ := os.ReadFile(canonical); strings.Contains(string(b4), "EDITED") {
		t.Error("--force did not refresh")
	}
}

func TestWildcardSelectsUserSkillsToo(t *testing.T) {
	o := opts(t, "*-tool")
	writeSkill(t, filepath.Join(o.Root, CanonicalDir), "my-tool", "")
	writeSkill(t, filepath.Join(o.Root, CanonicalDir), "other", "")
	acts, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if what(acts, "my-tool", "claude") != "linked" || what(acts, "other", "claude") != "" || what(acts, "swarm-join", "claude") != "" {
		t.Fatalf("wrong selection: %v", acts)
	}
}

func TestSingularAgentDirIsReadAndReachesHarnesses(t *testing.T) {
	o := opts(t, "from-singular")
	writeSkill(t, filepath.Join(o.Root, ".agent/skills"), "from-singular", "")
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	// Native harnesses scan .agents/skills, so it must appear there as well.
	for _, p := range []string{".agents/skills/from-singular/SKILL.md", ".claude/skills/from-singular/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(o.Root, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestCustomPathAndCustomSource(t *testing.T) {
	o := opts(t, "ext")
	src, dst := t.TempDir(), t.TempDir()
	writeSkill(t, src, "ext", "")
	o.Sources, o.Paths, o.Harnesses = []string{src}, []string{dst}, nil
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "ext/SKILL.md")); err != nil {
		t.Fatalf("custom path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(o.Root, ".claude/skills/ext")); err == nil {
		t.Error("touched a harness that was not selected")
	}
}

func TestGlobalScopeUsesHome(t *testing.T) {
	o := opts(t, "swarm-join")
	o.Global = true
	acts, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(o.Home, ".agents/skills/swarm-join/SKILL.md")); err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if _, err := os.Stat(filepath.Join(o.Home, ".claude/skills/swarm-join/SKILL.md")); err != nil {
		t.Fatalf("claude: %v", err)
	}
	if _, err := os.Stat(filepath.Join(o.Root, ".agents")); err == nil {
		t.Error("global install touched the project")
	}
	if what(acts, "swarm-join", "opencode") != "native" {
		t.Errorf("opencode: %v", acts)
	}
}

func TestUserDirectoryIsNeverReplaced(t *testing.T) {
	o := opts(t, "swarm-join")
	mine := filepath.Join(o.Root, ".claude/skills/swarm-join")
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, force := range []bool{false, true} {
		o.Force = force
		acts, err := Install(o)
		if err != nil {
			t.Fatal(err)
		}
		if what(acts, "swarm-join", "claude") != "conflict" {
			t.Errorf("force=%v: %v", force, acts)
		}
		if b, _ := os.ReadFile(filepath.Join(mine, "SKILL.md")); string(b) != "mine" {
			t.Fatalf("force=%v destroyed the user's directory", force)
		}
	}
	u := o
	if _, err := Uninstall(u); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(mine, "SKILL.md")); string(b) != "mine" {
		t.Fatal("uninstall destroyed the user's directory")
	}
}

func TestCopyModeRefreshesManagedCopies(t *testing.T) {
	o := opts(t, "swarm-join")
	o.Copy = true
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	at := filepath.Join(o.Root, ".claude/skills/swarm-join")
	fi, err := os.Lstat(at)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected a real directory: %v", err)
	}
	canonical := filepath.Join(o.Root, ".agents/skills/swarm-join/SKILL.md")
	b, _ := os.ReadFile(canonical)
	if err := os.WriteFile(canonical, append(b, []byte("NEW\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(at, "SKILL.md")); !strings.Contains(string(b), "NEW") {
		t.Fatal("copy was not refreshed")
	}
}

func TestBrokenSkillDoesNotSpread(t *testing.T) {
	o := opts(t, "bad")
	writeSkill(t, filepath.Join(o.Root, CanonicalDir), "bad", "metadata:\n  version: 1.0\n")
	acts, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(acts, func(a Action) bool { return a.Skill == "bad" && a.What == "invalid" }) {
		t.Fatalf("not reported invalid: %v", acts)
	}
	if _, err := os.Lstat(filepath.Join(o.Root, ".claude/skills/bad")); err == nil {
		t.Fatal("a broken skill reached a harness")
	}
}

func TestUninstallAndPurge(t *testing.T) {
	o := opts(t, "swarm-*")
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(o.Root, ".claude/skills/swarm-join")); err == nil {
		t.Error("link remains")
	}
	if _, err := os.Stat(filepath.Join(o.Root, ".agents/skills/swarm-join/SKILL.md")); err != nil {
		t.Error("uninstall without --purge removed the source")
	}
	o.Purge = true
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(o.Root, ".agents/skills/swarm-join")); err == nil {
		t.Error("purge left the source")
	}
}

func TestLintRules(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, content string
		want          string
	}{
		{"Bad-Name", "---\nname: Bad-Name\ndescription: A long enough description for the linter to accept.\n---\nx", "lowercase"},
		{"wrong", "---\nname: other\ndescription: A long enough description for the linter to accept.\n---\nx", "must match"},
		{"nodesc", "---\nname: nodesc\n---\nx", "description is required"},
		{"long", "---\nname: long\ndescription: " + strings.Repeat("x", 1025) + "\n---\nx", "over 1024"},
		{"meta", "---\nname: meta\ndescription: A long enough description for the linter to accept.\nmetadata:\n  version: 1.0\n---\nx", "must be a string"},
		{"tools", "---\nname: tools\ndescription: A long enough description for the linter to accept.\nallowed-tools:\n  - Read\n---\nx", "space-separated"},
		{"yaml", "---\nname: yaml\ndescription: Use when: the user asks\n---\nx", "not valid YAML"},
		{"nofm", "# no frontmatter", "must start"},
		{"a--b", "---\nname: a--b\ndescription: A long enough description for the linter to accept.\n---\nx", "lowercase"},
	}
	for _, c := range cases {
		p := filepath.Join(dir, c.name)
		_ = os.MkdirAll(p, 0o755)
		_ = os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(c.content), 0o644)
		issues := Lint(p)
		found := false
		for _, i := range issues {
			if i.Level == LevelError && strings.Contains(i.Msg, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: wanted an error containing %q, got %v", c.name, c.want, issues)
		}
	}
}

func TestLintExplainsHarnessSpecificFields(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "extra", "disable-model-invocation: true\nmystery: 1\n")
	var info, warn bool
	for _, i := range Lint(filepath.Join(dir, "extra")) {
		if i.Level == LevelInfo && strings.Contains(i.Msg, "Cursor") {
			info = true
		}
		if i.Level == LevelWarn && strings.Contains(i.Msg, "mystery") {
			warn = true
		}
		if i.Level == LevelError {
			t.Errorf("should not be an error: %v", i)
		}
	}
	if !info || !warn {
		t.Error("harness-specific and unknown fields were not explained")
	}
}

// The skills this binary ships must pass its own linter, with no warnings.
func TestBundledSkillsAreClean(t *testing.T) {
	root := t.TempDir()
	entries, err := fs.ReadDir(bundled, "skills")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 3 {
		t.Fatalf("only %d bundled skills", len(entries))
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if err := writeBundled(e.Name(), dir); err != nil {
			t.Fatal(err)
		}
		for _, i := range Lint(dir) {
			if i.Level != LevelInfo {
				t.Errorf("%s: %s: %s", e.Name(), i.Level, i.Msg)
			}
		}
		b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if !strings.Contains(string(b), "version:") {
			t.Errorf("%s has no version", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(root, "swarm-join/agents/openai.yaml")); err != nil {
		t.Errorf("Codex metadata was not written: %v", err)
	}
}

func TestNewSkillPassesLint(t *testing.T) {
	o := opts(t)
	dir, err := New(o, "fresh-skill", "Does a fresh thing. Use when the user asks for a fresh thing.")
	if err != nil {
		t.Fatal(err)
	}
	if IssuesHaveErrors(Lint(dir)) {
		t.Fatalf("scaffold fails lint: %v", Lint(dir))
	}
	if _, err := New(o, "fresh-skill", ""); err == nil {
		t.Error("overwrote an existing skill")
	}
	if _, err := New(o, "../evil", ""); err == nil {
		t.Error("accepted a path as a name")
	}
}
