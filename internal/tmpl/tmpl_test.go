package tmpl

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Grano22/aiculator/internal/harness"
	"github.com/Grano22/aiculator/internal/skillpack"
)

const validFM = "---\nname: %s\ndescription: A skill made for a test, long enough for the linter.\nmetadata:\n  version: \"1.0\"\n---\n\n"

func project(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func put(t *testing.T, base, rel, content string) {
	t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func render(t *testing.T, root, name string, c Context) string {
	t.Helper()
	s := NewSet(root, nil)
	tp, err := s.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Render(tp, c)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestJinjaVariablesConditionsAndLoops(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/x.md.j2", `---
template:
  vars:
    who: world
    items: [a, b]
name: x
description: A skill made for a test, long enough for the linter.
---

Hello {{ who }}.
{% for i in items %}
- {{ i | upper }}
{% endfor %}
{% if harness.id == "claude" %}
claude only
{% else %}
other
{% endif %}
`)
	out := render(t, root, "x", Context{})
	for _, want := range []string{"Hello world.", "- A\n- B", "other", `aiculator.generated-from: "x"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "template:") || strings.Contains(out, "vars:") {
		t.Errorf("template settings leaked into the skill:\n%s", out)
	}
	h, _ := harness.Get("claude")
	out = render(t, root, "x", Context{Vars: map[string]string{"who": "there"}, Harness: &h})
	for _, want := range []string{"Hello there.", "claude only", `aiculator.harness: "claude"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestUndefinedVariableIsAnError(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/x.md.j2", fmt.Sprintf(validFM, "x")+"{{ typo_here }}\n")
	s := NewSet(root, nil)
	tp, _ := s.Get("x")
	if _, err := s.Render(tp, Context{}); err == nil {
		t.Fatal("a typo in a variable name rendered silently")
	}
}

func TestIncludesProjectOverridesAndSingularDirs(t *testing.T) {
	root := project(t)
	put(t, root, ".agent/template/partials/rule.md", "shared rule\n")
	put(t, root, ".agent/template/x.j2", fmt.Sprintf(validFM, "x")+"{% include \"partials/rule.md\" %}\n")
	if out := render(t, root, "x", Context{}); !strings.Contains(out, "shared rule") {
		t.Errorf("include:\n%s", out)
	}
	// A project component replaces the bundled one of the same name.
	put(t, root, ".agents/templates/components/Note.md", "OVERRIDE {{ children }}\n")
	put(t, root, ".agents/templates/y.mdx", fmt.Sprintf(validFM, "y")+"<Note>hi</Note>\n")
	if out := render(t, root, "y", Context{}); !strings.Contains(out, "OVERRIDE hi") {
		t.Errorf("override:\n%s", out)
	}
}

func TestPathsCannotEscape(t *testing.T) {
	root := project(t)
	put(t, root, "secret.txt", "TOPSECRET")
	for i, inc := range []string{"../../secret.txt", "/etc/hosts", "partials/../../../secret.txt"} {
		name := "t" + strconv.Itoa(i)
		put(t, root, ".agents/templates/"+name+".j2", fmt.Sprintf(validFM, name)+"{% include \""+inc+"\" %}\n")
		s := NewSet(root, nil)
		tp, err := s.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		out, err := s.Render(tp, Context{})
		if err == nil || strings.Contains(out, "TOPSECRET") {
			t.Errorf("%s: escaped: %v\n%s", inc, err, out)
		}
	}
	// A component name is not a path either: the tag only accepts letters and digits.
	put(t, root, ".agents/templates/u.j2", fmt.Sprintf(validFM, "u")+"<NoSuchThing />\n")
	s := NewSet(root, nil)
	tp, _ := s.Get("u")
	if _, err := s.Render(tp, Context{}); err == nil || !strings.Contains(err.Error(), "unknown component") {
		t.Errorf("unknown component: %v", err)
	}
}

func TestComponentsNestAndCodeIsLeftAlone(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/x.mdx", fmt.Sprintf(validFM, "x")+"<Flow name=\"f\">\n<Step n=\"1\" title=\"A\" ask=\"ok?\" subagent=\"skip it\">do a</Step>\n\n<Step n=\"2\" title=\"B\">do <Note>inner</Note></Step>\n</Flow>\n\nExample: `<Backoff />` stays.\n\n```text\n<Flow>\nnot a component\n```\n")
	out := render(t, root, "x", Context{})
	for _, want := range []string{"### Step 1: A", "**Ask the user:** ok?", "**As a subagent:** skip it", "### Step 2: B", "> **Note:** inner", "`<Backoff />` stays", "<Flow>\nnot a component"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "</Flow>") || strings.Contains(out, "<Step") {
		t.Errorf("tags left over:\n%s", out)
	}
}

func TestMalformedComponentsAreErrors(t *testing.T) {
	cases := []struct{ content, want string }{
		{"<Note>open", "never closed"},
		{"text</Note>", "no opening tag"},
		{"<Note><Step></Note>", "closes"},
	}
	for i, c := range cases {
		root := project(t)
		name := "m" + strconv.Itoa(i)
		put(t, root, ".agents/templates/"+name+".mdx", fmt.Sprintf(validFM, name)+c.content+"\n")
		s := NewSet(root, nil)
		tp, _ := s.Get(name)
		_, err := s.Render(tp, Context{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v", c.content, err)
		}
	}
}

func TestSelfIncludingComponentStops(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/components/Loop.md", "<Loop />\n")
	put(t, root, ".agents/templates/x.mdx", fmt.Sprintf(validFM, "x")+"<Loop />\n")
	s := NewSet(root, nil)
	tp, _ := s.Get("x")
	if _, err := s.Render(tp, Context{}); err == nil || !strings.Contains(err.Error(), "nested too deeply") {
		t.Fatalf("got %v", err)
	}
}

func TestBackoffTableIsComputed(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/x.mdx", fmt.Sprintf(validFM, "x")+"<Backoff timeout=\"30s\" retries=\"4\" base=\"5s\" factor=\"3\" limit=\"20s\" />\n")
	out := render(t, root, "x", Context{})
	for _, want := range []string{"**30s**", "| 2 | 5s |", "| 3 | 15s |", "| 4 | 20s |", "| 5 | 20s |"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	put(t, root, ".agents/templates/bad.mdx", fmt.Sprintf(validFM, "bad")+"<Backoff base=\"soon\" />\n")
	s := NewSet(root, nil)
	tp, _ := s.Get("bad")
	if _, err := s.Render(tp, Context{}); err == nil {
		t.Fatal("a bad duration rendered silently")
	}
}

// The table of contents is only useful if its line numbers are exact.
func TestTocLineNumbersAreExact(t *testing.T) {
	out := render(t, project(t), "agent-discipline", Context{})
	lines := strings.Split(out, "\n")
	re := regexp.MustCompile(`^\s*- (.+): line (\d+)`)
	checked := 0
	for _, l := range lines {
		m := re.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		if n < 1 || n > len(lines) || !strings.HasSuffix(lines[n-1], m[1]) || !strings.HasPrefix(lines[n-1], "#") {
			t.Errorf("%q points at line %d", m[1], n)
		}
		checked++
	}
	if checked < 6 {
		t.Fatalf("only %d entries:\n%s", checked, out)
	}
	if !strings.Contains(out, "anchor `backoff`") {
		t.Errorf("anchors are missing:\n%s", out)
	}
	if strings.Contains(out, "- Contents:") {
		t.Error("the table lists itself")
	}
}

func opts(root string, hs ...string) InstallOptions {
	var list []harness.Harness
	for _, id := range hs {
		h, _ := harness.Get(id)
		list = append(list, h)
	}
	return InstallOptions{Skill: skillpack.Options{Root: root, Home: filepath.Join(root, "home"), Patterns: []string{"*"}, Harnesses: list}}
}

func TestBundledTemplateInstallsAndPassesLint(t *testing.T) {
	root := project(t)
	s := NewSet(root, nil)
	if _, err := s.Install(opts(root, "claude", "opencode")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".agents/skills/agent-discipline")
	for _, i := range skillpack.Lint(dir) {
		if i.Level != skillpack.LevelInfo {
			t.Errorf("%s: %s", i.Level, i.Msg)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".claude/skills/agent-discipline/SKILL.md")); err != nil {
		t.Errorf("not linked for claude: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".opencode")); err == nil {
		t.Error("made a directory for a harness that reads .agents/skills")
	}
	// Installing again is safe, and re-rendering reflects new values.
	o := opts(root, "claude")
	o.Vars = map[string]string{"retries": "7"}
	if _, err := s.Install(o); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if !strings.Contains(string(b), "| 8 |") {
		t.Errorf("--set was not applied:\n%s", b)
	}
}

func TestUserSkillIsNeverOverwritten(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/skills/agent-discipline/SKILL.md", "mine")
	acts, err := NewSet(root, nil).Install(opts(root, "claude"))
	if err != nil {
		t.Fatal(err)
	}
	if acts[0].What != "conflict" {
		t.Errorf("actions: %v", acts)
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".agents/skills/agent-discipline/SKILL.md")); string(b) != "mine" {
		t.Fatal("overwrote the user's skill")
	}
}

func TestInvalidResultDoesNotReplaceTheInstalledSkill(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/x.md.j2", fmt.Sprintf(validFM, "x")+"good\n")
	s := NewSet(root, nil)
	one := opts(root)
	one.Skill.Patterns = []string{"x"}
	if _, err := s.Install(one); err != nil {
		t.Fatal(err)
	}
	put(t, root, ".agents/templates/x.md.j2", "---\nname: WRONG_Name\ndescription: A skill made for a test, long enough.\n---\n\nbad\n")
	if _, err := s.Install(one); err == nil {
		t.Fatal("an invalid skill was accepted")
	}
	b, _ := os.ReadFile(filepath.Join(root, ".agents/skills/x/SKILL.md"))
	if !strings.Contains(string(b), "good") {
		t.Fatalf("the installed skill was damaged: %s", b)
	}
	if m, _ := filepath.Glob(filepath.Join(root, ".agents/skills/.render-*")); len(m) != 0 {
		t.Errorf("temporary files left behind: %v", m)
	}
}

func TestPerHarnessRendersSeparateCopies(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/x.md.j2", fmt.Sprintf(validFM, "x")+"for {{ harness.id }}\n")
	o := opts(root, "claude", "opencode", "cline")
	o.Skill.Patterns = []string{"x"}
	o.PerHarness = true
	if _, err := NewSet(root, nil).Install(o); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		".claude/skills/x/SKILL.md":   "for claude",
		".opencode/skills/x/SKILL.md": "for opencode",
		".agents/skills/x/SKILL.md":   "for generic", // Cline shares .agents/skills.
	} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || !strings.Contains(string(b), want) {
			t.Errorf("%s: %v %s", rel, err, b)
		}
	}
}

func TestSiblingFilesAreCopied(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/x/SKILL.md.j2", fmt.Sprintf(validFM, "x")+"body\n")
	put(t, root, ".agents/templates/x/references/guide.md", "guide")
	o := opts(root)
	o.Skill.Patterns = []string{"x"}
	if _, err := NewSet(root, nil).Install(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents/skills/x/references/guide.md")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents/skills/x/SKILL.md.j2")); err == nil {
		t.Error("the template file was copied along with the skill")
	}
}

func TestCheckFindsStaleSkills(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/x.md.j2", fmt.Sprintf(validFM, "x")+"one\n")
	s := NewSet(root, nil)
	if _, err := s.Install(opts(root)); err != nil {
		t.Fatal(err)
	}
	if stale, err := s.Check(opts(root)); err != nil || len(stale) != 0 {
		t.Fatalf("fresh: %v %v", stale, err)
	}
	put(t, root, ".agents/templates/x.md.j2", fmt.Sprintf(validFM, "x")+"two\n")
	if stale, err := s.Check(opts(root)); err != nil || len(stale) != 1 || stale[0] != "x" {
		t.Fatalf("stale: %v %v", stale, err)
	}
}

func TestStarterTemplateRenders(t *testing.T) {
	root := project(t)
	if _, err := New(root, "starter"); err != nil {
		t.Fatal(err)
	}
	out := render(t, root, "starter", Context{})
	if !strings.Contains(out, "## Contents") || !strings.Contains(out, "### Step 1: First") {
		t.Errorf("starter:\n%s", out)
	}
	if _, err := New(root, "../evil"); err == nil {
		t.Error("accepted a path as a name")
	}
}
