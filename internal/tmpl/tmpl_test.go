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

func forHarness(id string) Context {
	h, _ := harness.Get(id)
	return Context{Harness: &h}
}

func TestSelectTemplateNamesTheRightToolAndMode(t *testing.T) {
	root := project(t)
	cases := map[string][]string{
		"claude":   {"`AskUserQuestion`", "`multiSelect: false`", "at most 12 characters"},
		"gemini":   {"`ask_user`", "`multiSelect: false`", "at most 16 characters", `type: "choice"`},
		"opencode": {"`question` tool", "turn it off"},
		"pi":       {"Use your harness's question tool if it has one", "write a numbered list"},
	}
	for id, wants := range cases {
		out := render(t, root, "ui-select", forHarness(id))
		for _, w := range wants {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q in:\n%s", id, w, out)
			}
		}
	}
	// Pi has no such built-in, so it must not be told to call a specific tool as its own.
	if out := render(t, root, "ui-select", forHarness("claude")); strings.Contains(out, "`ask_user`") {
		t.Error("the Claude copy names another harness's tool")
	}
	// The shared copy cannot know the harness, so it names them all.
	shared := render(t, root, "ui-select", Context{})
	for _, w := range []string{"`AskUserQuestion` in Claude Code", "`question` in OpenCode", "`ask_user` in Gemini CLI"} {
		if !strings.Contains(shared, w) {
			t.Errorf("shared: missing %q", w)
		}
	}
}

func TestCheckboxAndTabsUseTheirOwnModes(t *testing.T) {
	root := project(t)
	box := render(t, root, "ui-checkbox", forHarness("claude"))
	if !strings.Contains(box, "`multiSelect: true`") || strings.Contains(box, "`multiSelect: false`") {
		t.Errorf("checkbox:\n%s", box)
	}
	tabs := render(t, root, "ui-tabs", forHarness("claude"))
	for _, w := range []string{"single call", "`questions` array", "**Language**", "**Extras**", "*(several can be chosen)*", "*(one can be chosen)*"} {
		if !strings.Contains(tabs, w) {
			t.Errorf("tabs: missing %q in:\n%s", w, tabs)
		}
	}
}

func TestEveryQuestionTypeCoversSubagentsAndNoTool(t *testing.T) {
	root := project(t)
	for _, name := range []string{"ui-select", "ui-checkbox", "ui-tabs"} {
		out := render(t, root, name, Context{})
		for _, w := range []string{"If you are a subagent", "If you have no question tool", "stop and wait"} {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q", name, w)
			}
		}
	}
	put(t, root, ".agents/templates/c.mdx", fmt.Sprintf(validFM, "c")+`<AskOne question="Pick" subagent="Use SQLite.">
- A
- B
</AskOne>
`)
	out := render(t, root, "c", Context{})
	if !strings.Contains(out, "**Ask the user:** Pick") || !strings.Contains(out, "- A\n- B") || !strings.Contains(out, "Use SQLite.") {
		t.Errorf("custom question:\n%s", out)
	}
}

func TestSubagentFlowRenders(t *testing.T) {
	out := render(t, project(t), "subagent-flow", Context{Vars: map[string]string{"parallel": "5"}})
	for _, w := range []string{"You are the orchestrator", "### Stage 1: Research", "Run up to 5 subagents at once", "### Stage 3: Review", "a fresh subagent", "**Done when:**", "cannot reach the user", "cannot run subagents", "- Stage 2: Build: line"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestDynamicSectionSurvivesReRenderAndCheck(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/d.md.j2", fmt.Sprintf(validFM, "d")+`# D

<Toc />

<DynamicSection id="state" title="State" update="after every commit">Nothing yet.</DynamicSection>

## After
Text.
`)
	s := NewSet(root, nil)
	o := opts(root)
	o.Skill.Patterns = []string{"d"}
	if _, err := s.Install(o); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".agents/skills/d/SKILL.md")
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "Nothing yet.") || !strings.Contains(string(b), "after every commit") {
		t.Fatalf("first render:\n%s", b)
	}
	// A model updates the section, with several more lines than before.
	edited := strings.Replace(string(b), "Nothing yet.", "Line one.\nLine two.\nLine three.", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if stale, err := s.Check(o); err != nil || len(stale) != 0 {
		t.Fatalf("a model's edit counted as drift: %v %v", stale, err)
	}
	// The template changes elsewhere: the check notices, and a re-render keeps the edit.
	put(t, root, ".agents/templates/d.md.j2", fmt.Sprintf(validFM, "d")+`# D

<Toc />

<DynamicSection id="state" title="State" update="after every commit">Nothing yet.</DynamicSection>

## After
Different text.
`)
	if stale, _ := s.Check(o); len(stale) != 1 {
		t.Fatalf("real drift was missed: %v", stale)
	}
	if _, err := s.Install(o); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	out := string(b)
	if !strings.Contains(out, "Line three.") || !strings.Contains(out, "Different text.") || strings.Contains(out, "Nothing yet.") {
		t.Fatalf("after re-render:\n%s", out)
	}
	// The contents list must still point at the right lines, after the section grew.
	lines := strings.Split(out, "\n")
	re := regexp.MustCompile(`^- After: line (\d+)`)
	found := false
	for _, l := range lines {
		if m := re.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			found = lines[n-1] == "## After"
		}
	}
	if !found {
		t.Fatalf("the contents list is wrong after a re-render:\n%s", out)
	}
	if stale, _ := s.Check(o); len(stale) != 0 {
		t.Fatalf("still stale after re-render: %v", stale)
	}
}

func TestLivingSectionInAFileGivesInstructionsOnly(t *testing.T) {
	out := render(t, project(t), "living-section", Context{Vars: map[string]string{"file": "NOTES.md"}})
	if !strings.Contains(out, "`NOTES.md`") || !strings.Contains(out, "<!-- dynamic:status:start -->") {
		t.Errorf("file form:\n%s", out)
	}
	// The example in a code fence must be left out of what a re-render treats as live.
	if got := keepDynamic(out, map[string]string{"status": "OVERWRITTEN"}); strings.Contains(got, "OVERWRITTEN") {
		t.Errorf("a re-render rewrote the example in the code fence:\n%s", got)
	}
}

func TestAllBundledTemplatesPassLintForEveryHarness(t *testing.T) {
	root := project(t)
	s := NewSet(root, nil)
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 12 {
		t.Fatalf("only %d bundled templates", len(list))
	}
	for _, tp := range list {
		for _, c := range []Context{{}, forHarness("claude"), forHarness("opencode"), forHarness("gemini"), forHarness("pi")} {
			text, err := s.Render(tp, c)
			if err != nil {
				t.Fatalf("%s: %v", tp.Name, err)
			}
			if strings.Contains(text, "<Ask") || strings.Contains(text, "</") && !strings.Contains(text, "```") {
				t.Errorf("%s: a tag was left over:\n%s", tp.Name, text)
			}
			dir := filepath.Join(t.TempDir(), tp.Name)
			put(t, dir, "SKILL.md", text)
			for _, i := range skillpack.Lint(dir) {
				if i.Level != skillpack.LevelInfo {
					t.Errorf("%s: %s: %s", tp.Name, i.Level, i.Msg)
				}
			}
		}
	}
}

// The demo promises to show every feature, so a new component has to be added to it.
func TestDemoUsesEveryComponent(t *testing.T) {
	root := filepath.Join("..", "..")
	src, err := os.ReadFile(filepath.Join(root, ".agent", "template", "demo.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	names := BundledComponents()
	if len(names) < 15 {
		t.Fatalf("only %d components: %v", len(names), names)
	}
	for _, name := range names {
		if !strings.Contains(text, "<"+name) {
			t.Errorf("demo.mdx does not use <%s>", name)
		}
	}
	for _, feature := range []string{"{% if ", "{% for ", "{% include ", "{% macro ", "{# ", "| capitalize", "template:\n  vars:", "<DemoBadge"} {
		if !strings.Contains(text, feature) {
			t.Errorf("demo.mdx does not show %q", feature)
		}
	}
	s := NewSet(root, nil)
	tp, err := s.Get("demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []Context{{}, forHarness("claude"), forHarness("opencode"), forHarness("gemini"), forHarness("pi")} {
		out, err := s.Render(tp, c)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"{{", "{%", "{#", "<AskOne", "<Tab", "<Stage", "<Flow", "<DemoBadge", "<Toc"} {
			if strings.Contains(out, bad) {
				t.Errorf("%q was left in the rendered demo", bad)
			}
		}
		dir := filepath.Join(t.TempDir(), "demo")
		put(t, dir, "SKILL.md", out)
		for _, i := range skillpack.Lint(dir) {
			if i.Level == skillpack.LevelError || i.Level == skillpack.LevelWarn {
				t.Errorf("%s: %s", i.Level, i.Msg)
			}
		}
	}
}

var renderKinds = []string{"code", "inline", "shell", "diff", "table", "list", "checklist", "headings", "callout", "quote", "fileref", "tree", "link", "diagram", "image", "details", "math", "footnote", "rule"}

func TestRenderCoversEveryKindOnBothSurfaces(t *testing.T) {
	for _, kind := range renderKinds {
		for _, surface := range []string{"terminal", "rich"} {
			root := project(t)
			put(t, root, ".agents/templates/k.mdx", fmt.Sprintf(validFM, "k")+`<Render kind="`+kind+`" surface="`+surface+`">Extra advice.</Render>`+"\n")
			out := render(t, root, "k", Context{})
			if !strings.Contains(out, "<!-- anchor: render-"+kind+" -->") || !strings.Contains(out, "Extra advice.") {
				t.Errorf("%s/%s:\n%s", kind, surface, out)
			}
		}
	}
	root := project(t)
	put(t, root, ".agents/templates/k.mdx", fmt.Sprintf(validFM, "k")+`<Render kind="nonsense" />`+"\n")
	s := NewSet(root, nil)
	tp, _ := s.Get("k")
	if _, err := s.Render(tp, Context{}); err == nil {
		t.Fatal("an unknown kind rendered silently")
	}
}

func TestRichMarkdownAdaptsToTheSurface(t *testing.T) {
	root := project(t)
	term := render(t, root, "rich-markdown", Context{})
	rich := render(t, root, "rich-markdown", Context{Vars: map[string]string{"surface": "rich"}})
	for _, kind := range renderKinds {
		if !strings.Contains(term, "anchor: render-"+kind) {
			t.Errorf("rich-markdown leaves out %s", kind)
		}
	}
	if !strings.Contains(rich, "```mermaid") || strings.Contains(term, "```mermaid") {
		t.Error("only the rich surface should get a Mermaid example")
	}
	if !strings.Contains(term, "A terminal may show") || !strings.Contains(rich, "[!WARNING]") || strings.Contains(term, "[!WARNING]") {
		t.Error("the rules should differ by surface")
	}
	// Component names in the examples must survive, since they sit in code.
	if !strings.Contains(term, "## Contents") || !strings.Contains(term, "### Code: line") && !strings.Contains(term, "- Code: line") {
		t.Errorf("contents list:\n%.600s", term)
	}
}

func TestToolComponentsNameTheRightToolPerHarness(t *testing.T) {
	root := project(t)
	cases := []struct {
		template, harness string
		wants, rejects    []string
	}{
		{"ui-todos", "claude", []string{"`TaskCreate`", "`TaskUpdate`", "must be switched on"}, []string{"`write_todos`"}},
		{"ui-todos", "gemini", []string{"`write_todos`"}, []string{"`TaskCreate`"}},
		{"ui-todos", "opencode", []string{"`todowrite`"}, []string{"`TaskCreate`"}},
		{"ui-plan", "claude", []string{"`EnterPlanMode`", "`ExitPlanMode`"}, []string{"`enter_plan_mode`"}},
		{"ui-plan", "gemini", []string{"`enter_plan_mode`", "`exit_plan_mode`"}, []string{"`EnterPlanMode`"}},
		{"ui-plan", "opencode", []string{"numbered list in your reply"}, []string{"EnterPlanMode", "enter_plan_mode"}},
		{"ui-confirm", "gemini", []string{`type: "yesno"`}, nil},
		{"ui-confirm", "claude", []string{"`multiSelect: false`", "Delete 3"}, nil},
		{"ui-text", "gemini", []string{`type: "text"`, "my-service"}, nil},
		{"ui-text", "opencode", []string{"`question` tool", "type their own"}, nil},
		{"ui-text", "pi", []string{"Ask in your reply"}, []string{"`ask_user`"}},
		{"ui-preview", "claude", []string{"`preview` field", "single-choice"}, nil},
	}
	for _, c := range cases {
		out := render(t, root, c.template, forHarness(c.harness))
		for _, w := range c.wants {
			if !strings.Contains(out, w) {
				t.Errorf("%s for %s: missing %q in:\n%s", c.template, c.harness, w, out)
			}
		}
		for _, r := range c.rejects {
			if strings.Contains(out, r) {
				t.Errorf("%s for %s: should not mention %q", c.template, c.harness, r)
			}
		}
	}
	shared := render(t, root, "ui-plan", Context{})
	if !strings.Contains(shared, "`EnterPlanMode`") || !strings.Contains(shared, "`enter_plan_mode`") {
		t.Errorf("the shared copy should name both:\n%s", shared)
	}
}

// In Claude Code a skill's allowed-tools skip the permission prompt, and the prompt on
// ExitPlanMode is the user's approval of the plan.
func TestPlanTemplateDoesNotPreApproveItsOwnApproval(t *testing.T) {
	out := render(t, project(t), "ui-plan", forHarness("claude"))
	if strings.Contains(out, "allowed-tools") {
		t.Fatalf("ui-plan must not list allowed-tools:\n%.500s", out)
	}
}

func TestRichMarkdownCanCoverASubset(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/k.mdx", fmt.Sprintf(validFM, "k")+`<RichMarkdown only="code, diff" />`+"\n")
	out := render(t, root, "k", Context{})
	if !strings.Contains(out, "render-code") || !strings.Contains(out, "render-diff") || strings.Contains(out, "render-table") {
		t.Fatalf("subset:\n%s", out)
	}
	put(t, root, ".agents/templates/k.mdx", fmt.Sprintf(validFM, "k")+`<RichMarkdown only="code,nonsense" />`+"\n")
	s := NewSet(root, nil)
	tp, _ := s.Get("k")
	if _, err := s.Render(tp, Context{}); err == nil {
		t.Fatal("an unknown kind rendered silently")
	}
}

func TestTocDepthListsOnlyTopSections(t *testing.T) {
	root := project(t)
	put(t, root, ".agents/templates/k.mdx", fmt.Sprintf(validFM, "k")+"<Toc depth=\"2\" />\n\n## Top\n\n### Inner\n\n## Last\n")
	out := render(t, root, "k", Context{})
	if strings.Contains(out, "- Inner") || strings.Contains(out, "  - ") {
		t.Fatalf("depth 2 listed an inner section:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	for _, name := range []string{"Top", "Last"} {
		var n int
		for _, l := range lines {
			if _, err := fmt.Sscanf(l, "- "+name+": line %d", &n); err == nil {
				break
			}
		}
		if n == 0 || lines[n-1] != "## "+name {
			t.Errorf("%s: line %d is wrong:\n%s", name, n, out)
		}
	}
}
