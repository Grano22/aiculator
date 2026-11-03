// Package tmpl turns templates into skills, so that instructions which shape model
// behavior can be written once and reused. Rendering has two passes: Jinja2 for
// variables, conditions and includes, then MDX-style components such as
// <Backoff retries="3" />. A generated table of contents comes last.
//
// Templates can only produce text. They cannot run code or read outside their own
// directories, so a template from an untrusted repository cannot harm the machine that
// renders it. What the resulting skill tells a model to do still deserves review.
package tmpl

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nikolalohinski/gonja/v2"
	"github.com/nikolalohinski/gonja/v2/config"
	"github.com/nikolalohinski/gonja/v2/exec"
	"github.com/nikolalohinski/gonja/v2/loaders"
	"go.yaml.in/yaml/v3"

	"aiculation/internal/harness"
)

//go:embed all:library
var library embed.FS

// Dirs are the project directories searched for templates, in rising priority.
var Dirs = []string{".agent/template", ".agent/templates", ".agents/template", ".agents/templates"}

// Template is one template that can be rendered.
type Template struct {
	Name   string
	Source string // Display path, or "bundled".
	file   string // Slash path inside a root, or the embedded library.
	dir    string // Directory holding sibling files; empty when there are none.
	root   string // Root directory; empty for bundled.
}

// fileSuffixes are the accepted single-file forms, longest first.
var fileSuffixes = []string{".md.jinja2", ".md.jinja", ".md.j2", ".jinja2", ".jinja", ".mdx", ".j2"}

// skillNames are the accepted main files in the directory form.
var skillNames = []string{"SKILL.md.j2", "SKILL.md.jinja", "SKILL.md.jinja2", "SKILL.j2", "SKILL.mdx", "SKILL.md"}

// reserved directories hold pieces of templates, not templates.
var reserved = []string{"partials", "components", "macros", "includes"}

// Set finds templates, and renders them with includes and components resolved
// from the project first and the bundled library second.
type Set struct {
	roots []string
}

// NewSet searches base for template directories. Extra directories win over them.
func NewSet(base string, extra []string) *Set {
	s := &Set{}
	for _, d := range Dirs {
		s.roots = append(s.roots, filepath.Join(base, filepath.FromSlash(d)))
	}
	s.roots = append(s.roots, extra...)
	return s
}

// List returns all templates, sorted by name. Later roots override earlier ones.
func (s *Set) List() ([]Template, error) {
	found := map[string]Template{}
	bundled, err := fs.ReadDir(library, "library/templates")
	if err != nil {
		return nil, err
	}
	for _, e := range bundled {
		found[e.Name()] = Template{Name: e.Name(), Source: "bundled", file: "templates/" + e.Name() + "/SKILL.md.j2"}
	}
	for _, root := range s.roots {
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if slices.Contains(reserved, e.Name()) || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if e.IsDir() {
				for _, n := range skillNames {
					if _, err := os.Stat(filepath.Join(root, e.Name(), n)); err == nil {
						found[e.Name()] = Template{Name: e.Name(), Source: filepath.Join(root, e.Name(), n), file: e.Name() + "/" + n, dir: filepath.Join(root, e.Name()), root: root}
						break
					}
				}
				continue
			}
			for _, suf := range fileSuffixes {
				if name, ok := strings.CutSuffix(e.Name(), suf); ok && name != "" {
					found[name] = Template{Name: name, Source: filepath.Join(root, e.Name()), file: e.Name(), root: root}
					break
				}
			}
		}
	}
	out := make([]Template, 0, len(found))
	for _, t := range found {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Get finds one template.
func (s *Set) Get(name string) (Template, error) {
	list, err := s.List()
	if err != nil {
		return Template{}, err
	}
	for _, t := range list {
		if t.Name == name {
			return t, nil
		}
	}
	return Template{}, fmt.Errorf("no template named %q", name)
}

// read loads a slash path from the project roots, then the library. It refuses any
// path that could climb out of them.
func (s *Set) read(p string) ([]byte, error) {
	clean := path.Clean(strings.ReplaceAll(p, `\`, "/"))
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, fmt.Errorf("template path %q is outside the template directories", p)
	}
	for i := len(s.roots) - 1; i >= 0; i-- {
		b, err := os.ReadFile(filepath.Join(s.roots[i], filepath.FromSlash(clean)))
		if err == nil {
			return b, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	b, err := library.ReadFile("library/" + clean)
	if err != nil {
		return nil, fmt.Errorf("template file %q not found", p)
	}
	return b, nil
}

// loader adapts Set for Jinja's include, import and extends.
type loader struct{ s *Set }

func (l loader) Read(p string) (io.Reader, error) {
	b, err := l.s.read(p)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}
func (l loader) Resolve(p string) (string, error)       { return path.Clean(p), nil }
func (l loader) Inherit(string) (loaders.Loader, error) { return l, nil }

var engineConfig = func() *config.Config {
	c := config.New()
	c.AutoEscape = false // The output is Markdown, not HTML.
	c.StrictUndefined = true
	c.TrimBlocks = true
	c.LeftStripBlocks = true
	c.KeepTrailingNewline = true
	return c
}()

// jinja renders one source text.
func (s *Set) jinja(id, source string, vars map[string]any) (string, error) {
	shifted, err := loaders.NewShiftedLoader(id, strings.NewReader(source), loader{s})
	if err != nil {
		return "", err
	}
	env := *gonja.DefaultEnvironment
	env.Context = gonja.DefaultContext.Update(exec.NewContext(map[string]any{"backoff_rows": backoffRows}))
	t, err := exec.NewTemplate(id, engineConfig, shifted, &env)
	if err != nil {
		return "", fmt.Errorf("%s: %w", id, err)
	}
	out, err := t.ExecuteToString(exec.NewContext(maps.Clone(vars)))
	if err != nil {
		return "", fmt.Errorf("%s: %w", id, err)
	}
	return out, nil
}

// backoffRows computes the retry table in code, because a model given only "double
// it each time" tends to get the numbers wrong.
func backoffRows(_ *exec.Evaluator, params *exec.VarArgs) *exec.Value {
	if len(params.Args) != 4 {
		return exec.AsValue(errors.New("backoff_rows needs retries, base, factor and limit"))
	}
	retries, err := strconv.Atoi(params.Args[0].String())
	if err != nil || retries < 0 || retries > 20 {
		return exec.AsValue(fmt.Errorf("retries must be 0 to 20, not %q", params.Args[0].String()))
	}
	base, err := time.ParseDuration(params.Args[1].String())
	if err != nil {
		return exec.AsValue(fmt.Errorf("base: %w", err))
	}
	factor, err := strconv.ParseFloat(params.Args[2].String(), 64)
	if err != nil || factor < 1 {
		return exec.AsValue(fmt.Errorf("factor must be at least 1, not %q", params.Args[2].String()))
	}
	limit, err := time.ParseDuration(params.Args[3].String())
	if err != nil {
		return exec.AsValue(fmt.Errorf("limit: %w", err))
	}
	rows := make([]map[string]any, 0, retries)
	wait := base
	for i := range retries {
		rows = append(rows, map[string]any{"attempt": i + 2, "wait": min(wait, limit).String()})
		wait = time.Duration(float64(wait) * factor)
	}
	return exec.AsValue(rows)
}

// Context is what a template can see beyond its variables.
type Context struct {
	Vars    map[string]string // From --set; these beat the template's defaults.
	Harness *harness.Harness  // Nil renders the shared, harness-neutral form.
	Version string
}

func harnessVars(h *harness.Harness) map[string]any {
	if h == nil {
		return map[string]any{"id": "generic", "name": "your harness", "has_mcp": false, "reads_agents": true, "has_acp": false}
	}
	return map[string]any{"id": h.ID, "name": h.Name, "has_mcp": h.MCP != harness.MCPNone, "reads_agents": h.ReadsAgents, "has_acp": len(h.ACP) > 0}
}

var templateBlock = regexp.MustCompile(`(?m)^template:[ \t]*\n((?:[ \t]+.*\n|[ \t]*\n)*)`)

// extractSettings pulls the template: block out of the frontmatter. It is read before
// rendering because its values feed the render, and it must not reach the skill.
func extractSettings(src string) (string, map[string]any, error) {
	if !strings.HasPrefix(src, "---") {
		return src, nil, nil
	}
	end := strings.Index(src[3:], "\n---")
	if end < 0 {
		return src, nil, errors.New("frontmatter has no closing ---")
	}
	fm := src[3 : 3+end+1]
	m := templateBlock.FindStringSubmatch(fm)
	if m == nil {
		return src, nil, nil
	}
	var wrap struct {
		Template struct {
			Vars map[string]any `yaml:"vars"`
		} `yaml:"template"`
	}
	if err := yaml.Unmarshal([]byte(m[0]), &wrap); err != nil {
		return src, nil, fmt.Errorf("template: block: %w", err)
	}
	return "---" + strings.Replace(fm, m[0], "", 1) + src[3+end+1:], wrap.Template.Vars, nil
}

// Render produces the final SKILL.md text.
func (s *Set) Render(t Template, c Context) (string, error) {
	raw, err := s.read(t.file)
	if err != nil {
		return "", err
	}
	src, defaults, err := extractSettings(string(raw))
	if err != nil {
		return "", fmt.Errorf("%s: %w", t.Name, err)
	}
	vars := map[string]any{}
	maps.Copy(vars, defaults)
	for k, v := range c.Vars {
		vars[k] = v
	}
	vars["harness"] = harnessVars(c.Harness)
	vars["skill"] = map[string]any{"name": t.Name}
	vars["aiculator"] = map[string]any{"version": c.Version}

	out, err := s.jinja(t.Name, src, vars)
	if err != nil {
		return "", err
	}
	fm, body := splitFrontmatter(out)
	body, err = s.expandComponents(t.Name, body, vars, 0)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(fm) != "" {
		if fm, err = stampMetadata(fm, t.Name, c.Harness); err != nil {
			return "", fmt.Errorf("%s: %w", t.Name, err)
		}
	}
	return finish(fm, body), nil
}

func splitFrontmatter(s string) (fm, body string) {
	if !strings.HasPrefix(s, "---") {
		return "", s
	}
	end := strings.Index(s[3:], "\n---")
	if end < 0 {
		return "", s
	}
	cut := 3 + end + 4
	return s[:cut], s[cut:]
}

// stampMetadata records where the skill came from, on the spec's metadata map.
func stampMetadata(fm, name string, h *harness.Harness) (string, error) {
	inner := strings.TrimSuffix(strings.TrimPrefix(fm, "---"), "---")
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(inner), &doc); err != nil {
		return "", fmt.Errorf("rendered frontmatter is not valid YAML: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("rendered frontmatter is not a mapping")
	}
	top := doc.Content[0]
	var meta *yaml.Node
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "metadata" {
			meta = top.Content[i+1]
		}
	}
	if meta == nil {
		meta = &yaml.Node{Kind: yaml.MappingNode}
		top.Content = append(top.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "metadata"}, meta)
	}
	add := func(k, v string) {
		meta.Content = append(meta.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle})
	}
	add("aiculator.generated-from", name)
	if h != nil {
		add("aiculator.harness", h.ID)
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", err
	}
	_ = enc.Close()
	return "---\n" + b.String() + "---", nil
}

const maxComponentDepth = 16

var (
	componentTag = regexp.MustCompile(`<(/?)([A-Z][A-Za-z0-9]*)((?:\s+[a-zA-Z_][\w-]*(?:=(?:"[^"]*"|'[^']*'|\{[^}]*\}))?)*)\s*(/?)>`)
	propRE       = regexp.MustCompile(`([a-zA-Z_][\w-]*)(?:=(?:"([^"]*)"|'([^']*)'|\{([^}]*)\}))?`)
	fenceRE      = regexp.MustCompile("(?m)^ {0,3}(```|~~~).*$")
	inlineCode   = regexp.MustCompile("`[^`\n]+`")
)

// protectedRanges finds code, where a <Tag> is an example and not a component.
func protectedRanges(s string) [][2]int {
	var out [][2]int
	fences := fenceRE.FindAllStringIndex(s, -1)
	for i := 0; i+1 < len(fences); i += 2 {
		out = append(out, [2]int{fences[i][0], fences[i+1][1]})
	}
	if len(fences)%2 == 1 { // An unclosed fence runs to the end, as Markdown treats it.
		out = append(out, [2]int{fences[len(fences)-1][0], len(s)})
	}
	for _, m := range inlineCode.FindAllStringIndex(s, -1) {
		out = append(out, [2]int{m[0], m[1]})
	}
	return out
}

func inRanges(pos int, ranges [][2]int) bool {
	for _, r := range ranges {
		if pos >= r[0] && pos < r[1] {
			return true
		}
	}
	return false
}

func parseProps(s string) map[string]any {
	props := map[string]any{}
	for _, m := range propRE.FindAllStringSubmatch(s, -1) {
		switch {
		case strings.Contains(m[0], "={"):
			props[m[1]] = strings.TrimSpace(m[4])
		case strings.Contains(m[0], "="):
			props[m[1]] = m[2] + m[3]
		default:
			props[m[1]] = "true"
		}
	}
	return props
}

// expandComponents replaces component tags, innermost first, until none remain.
func (s *Set) expandComponents(owner, body string, vars map[string]any, depth int) (string, error) {
	if depth > maxComponentDepth {
		return "", fmt.Errorf("%s: components are nested too deeply, or include each other", owner)
	}
	for {
		protected := protectedRanges(body)
		var tags [][]int
		for _, m := range componentTag.FindAllStringSubmatchIndex(body, -1) {
			if !inRanges(m[0], protected) {
				tags = append(tags, m)
			}
		}
		// The first closing tag closes the nearest opening tag before it, and a
		// self-closing tag met first is complete already. Either way, what gets
		// expanded has no tags inside it.
		target := -1
		for i, m := range tags {
			closing := m[2] != m[3]
			selfClosing := m[8] != m[9]
			if body[m[4]:m[5]] == "Toc" && !closing {
				continue // The table of contents is filled in last.
			}
			if selfClosing || closing {
				target = i
				break
			}
		}
		if target < 0 {
			for _, m := range tags {
				if name := body[m[4]:m[5]]; name != "Toc" {
					return "", fmt.Errorf("%s: <%s> is never closed", owner, name)
				}
			}
			return body, nil
		}
		m := tags[target]
		name := body[m[4]:m[5]]
		start, end := m[0], m[1]
		props := parseProps(body[m[6]:m[7]])
		if m[2] != m[3] { // A closing tag: its opener is the previous open tag.
			open := -1
			for i := target - 1; i >= 0; i-- {
				if body[tags[i][4]:tags[i][5]] != "Toc" {
					open = i
					break
				}
			}
			if open < 0 {
				return "", fmt.Errorf("%s: </%s> has no opening tag", owner, name)
			}
			o := tags[open]
			if openName := body[o[4]:o[5]]; openName != name {
				return "", fmt.Errorf("%s: </%s> closes <%s>", owner, name, openName)
			}
			props = parseProps(body[o[6]:o[7]])
			props["children"] = strings.Trim(body[o[1]:m[0]], "\n")
			start = o[0]
		}
		rendered, err := s.component(owner, name, props, vars, depth)
		if err != nil {
			return "", err
		}
		body = body[:start] + rendered + body[end:]
	}
}

func (s *Set) component(owner, name string, props, vars map[string]any, depth int) (string, error) {
	src, err := s.read("components/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("%s: unknown component <%s>: add components/%s.md", owner, name, name)
	}
	ctx := maps.Clone(vars)
	maps.Copy(ctx, props)
	out, err := s.jinja("components/"+name+".md", string(src), ctx)
	if err != nil {
		return "", err
	}
	out, err = s.expandComponents(owner, out, ctx, depth+1)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

var (
	tocTag     = regexp.MustCompile(`<Toc\s*/>`)
	headingRE  = regexp.MustCompile(`^(#{2,3})\s+(.+?)\s*$`)
	anchorLine = regexp.MustCompile(`^<!-- anchor: ([^ ]+) -->$`)
	blankRuns  = regexp.MustCompile(`\n{3,}`)
)

type tocEntry struct {
	level        int
	title, label string
	line         int
}

func scanHeadings(text string) []tocEntry {
	var out []tocEntry
	lines := strings.Split(text, "\n")
	fenced := false
	for i, l := range lines {
		if fenceRE.MatchString(l) {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		m := headingRE.FindStringSubmatch(l)
		if m == nil || m[2] == "Contents" {
			continue
		}
		e := tocEntry{level: len(m[1]), title: m[2], line: i + 1}
		for j := i + 1; j < len(lines) && j <= i+2; j++ {
			if a := anchorLine.FindStringSubmatch(lines[j]); a != nil {
				e.label = a[1]
			}
		}
		out = append(out, e)
	}
	return out
}

func renderToc(entries []tocEntry) string {
	var b strings.Builder
	b.WriteString("## Contents\n\n")
	for _, e := range entries {
		b.WriteString(strings.Repeat("  ", e.level-2))
		fmt.Fprintf(&b, "- %s: line %d", e.title, e.line)
		if e.label != "" {
			fmt.Fprintf(&b, ", anchor `%s`", e.label)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// finish tidies the body and fills in the table of contents. It lists real line
// numbers, which lets a model read just the part it needs.
func finish(fm, body string) string {
	body = blankRuns.ReplaceAllString(strings.TrimLeft(body, "\n"), "\n\n")
	head := ""
	if fm != "" {
		head = fm + "\n\n"
	}
	loc := tocTag.FindStringIndex(body)
	if loc == nil {
		return head + strings.TrimRight(body, "\n") + "\n"
	}
	assemble := func(toc string) string {
		return head + strings.TrimRight(body[:loc[0]]+toc+body[loc[1]:], "\n") + "\n"
	}
	// The table's own length shifts the lines after it, so measure, then fill in.
	sized := renderToc(scanHeadings(assemble(renderToc(nil))))
	return assemble(renderToc(scanHeadings(assemble(sized))))
}
