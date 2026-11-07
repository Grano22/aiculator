package codegraph

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The built-in indexer is what the swarm uses when Graphify is not installed. It reads
// definitions with one pattern per language rather than a parser, so it is less exact than
// Graphify's tree-sitter, but it needs nothing else installed, takes about a second for a
// large repository, and gives the same kind of graph: files, the symbols they define, what
// they import, and which files use a symbol defined elsewhere.

type lang struct {
	defs    []*regexp.Regexp // Group 1 is the name; a group named recv is a Go receiver type.
	kinds   []string
	imports []*regexp.Regexp // Group 1 is the module.
}

var (
	goLang = lang{
		defs: []*regexp.Regexp{
			regexp.MustCompile(`^func \((?:\w+ )?\*?(?P<recv>\w+)(?:\[[^\]]*\])?\) (\w+)`),
			regexp.MustCompile(`^func (\w+)`),
			regexp.MustCompile(`^type (\w+)`),
			regexp.MustCompile(`^(?:var|const) (\w+)\s`), // Top level only: not variables inside functions.
		},
		kinds:   []string{"method", "function", "type", "value"},
		imports: []*regexp.Regexp{regexp.MustCompile(`^\s*(?:\w+\s+)?"([^"]+)"\s*$`), regexp.MustCompile(`^import (?:\w+ )?"([^"]+)"`)},
	}
	pyLang = lang{
		defs:    []*regexp.Regexp{regexp.MustCompile(`^\s*(?:async\s+)?def (\w+)`), regexp.MustCompile(`^\s*class (\w+)`)},
		kinds:   []string{"function", "class"},
		imports: []*regexp.Regexp{regexp.MustCompile(`^from ([\w.]+) import`), regexp.MustCompile(`^import ([\w.]+)`)},
	}
	jsLang = lang{
		defs: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\*?\s+(\w+)`),
			regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+(\w+)`),
			regexp.MustCompile(`^\s*(?:export\s+)?(?:interface|type|enum)\s+(\w+)`),
			regexp.MustCompile(`^\s*(?:export\s+)?const\s+(\w+)\s*=\s*(?:async\s*)?(?:\(|function|\w+\s*=>)`),
		},
		kinds:   []string{"function", "class", "type", "function"},
		imports: []*regexp.Regexp{regexp.MustCompile(`from\s+['"]([^'"]+)['"]`), regexp.MustCompile(`require\(\s*['"]([^'"]+)['"]\s*\)`)},
	}
	rustLang = lang{
		defs:    []*regexp.Regexp{regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn (\w+)`), regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum|trait|type) (\w+)`)},
		kinds:   []string{"function", "type"},
		imports: []*regexp.Regexp{regexp.MustCompile(`^\s*use ([\w:]+)`)},
	}
	javaLang = lang{
		defs: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:public |private |protected |internal |static |final |abstract |sealed |data |open )*(?:class|interface|enum|record|object) (\w+)`),
			regexp.MustCompile(`^\s+(?:public |private |protected |internal |static |final |override |suspend |async |virtual )+[\w<>\[\], ?]*\s(\w+)\s*\(`),
			regexp.MustCompile(`^\s*fun (\w+)`),
		},
		kinds:   []string{"class", "method", "function"},
		imports: []*regexp.Regexp{regexp.MustCompile(`^import ([\w.]+)`), regexp.MustCompile(`^using ([\w.]+);`)},
	}
	rubyLang = lang{
		defs:    []*regexp.Regexp{regexp.MustCompile(`^\s*def (?:self\.)?(\w+[?!]?)`), regexp.MustCompile(`^\s*(?:class|module) ([\w:]+)`)},
		kinds:   []string{"method", "class"},
		imports: []*regexp.Regexp{regexp.MustCompile(`^\s*require(?:_relative)? ['"]([^'"]+)['"]`)},
	}
	phpLang = lang{
		defs:    []*regexp.Regexp{regexp.MustCompile(`function (\w+)\s*\(`), regexp.MustCompile(`^\s*(?:abstract |final )?(?:class|interface|trait) (\w+)`)},
		kinds:   []string{"function", "class"},
		imports: []*regexp.Regexp{regexp.MustCompile(`^use ([\w\\]+)`)},
	}
	cLang = lang{
		defs:    []*regexp.Regexp{regexp.MustCompile(`^(?:static |inline |extern )*[\w*]+[\s*]+(\w+)\s*\([^;]*$`), regexp.MustCompile(`^(?:typedef )?(?:struct|class|enum) (\w+)`)},
		kinds:   []string{"function", "type"},
		imports: []*regexp.Regexp{regexp.MustCompile(`^#include [<"]([^>"]+)[>"]`)},
	}
	mdLang = lang{
		defs:  []*regexp.Regexp{regexp.MustCompile(`^#{1,3} (.+)$`)},
		kinds: []string{"section"},
	}
)

var langs = map[string]*lang{
	".go": &goLang, ".py": &pyLang, ".js": &jsLang, ".jsx": &jsLang, ".mjs": &jsLang, ".cjs": &jsLang, ".ts": &jsLang,
	".tsx": &jsLang, ".rs": &rustLang, ".java": &javaLang, ".kt": &javaLang, ".cs": &javaLang, ".scala": &javaLang,
	".rb": &rubyLang, ".php": &phpLang, ".c": &cLang, ".h": &cLang, ".cc": &cLang, ".cpp": &cLang, ".hpp": &cLang,
	".md": &mdLang,
}

// Words too common to link files by, whatever the language.
var commonNames = map[string]bool{
	"main": true, "init": true, "new": true, "get": true, "set": true, "run": true, "test": true, "string": true,
	"error": true, "close": true, "start": true, "stop": true, "value": true, "values": true, "data": true, "name": true,
	"type": true, "list": true, "load": true, "save": true, "open": true, "read": true, "write": true, "len": true,
	"self": true, "this": true, "config": true, "options": true, "result": true, "context": true, "handler": true,
}

const (
	maxFileBytes  = 1 << 20
	maxUsesByFile = 200
)

// Build indexes the files git tracks under root.
func Build(ctx context.Context, root string) (*Graph, error) {
	files, err := trackedFiles(ctx, root)
	if err != nil {
		return nil, err
	}
	g := &Graph{Source: "builtin"}
	add := func(n Node) {
		g.Nodes = append(g.Nodes, n)
	}
	type def struct {
		id, name, file string
	}
	var defs []def
	modules := map[string]bool{}
	fileWords := map[string]map[string]bool{}
	recvLinks := map[[2]string]string{} // dir and type name to the method ids pending a type node.
	_ = recvLinks
	var methodOf [][3]string // method id, dir, receiver type name

	for _, rel := range files {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		l := langs[strings.ToLower(filepath.Ext(rel))]
		if l == nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || len(b) > maxFileBytes || bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		fileID := "file:" + rel
		add(Node{ID: fileID, Label: rel, FileType: fileType(rel), File: rel, Kind: "file"})
		words := map[string]bool{}
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		lineNo := 0
		inGoImport := false
		var doc []string // The comment lines just above the current line.
		pyDocFor := -1   // A Python definition whose docstring may come next.
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			trim := strings.TrimSpace(line)
			if pyDocFor >= 0 {
				if d, ok := strings.CutPrefix(trim, `"""`); ok {
					g.Nodes[pyDocFor].Doc = shortDoc([]string{strings.TrimSuffix(d, `"""`)})
				}
				pyDocFor = -1
			}
			if c, ok := comment(trim); ok && l != &mdLang { // A Markdown heading is not a comment.
				doc = append(doc, c)
				if len(doc) > 6 {
					doc = doc[1:]
				}
			}
			if l != &mdLang {
				for _, w := range identRE.FindAllString(line, -1) {
					words[w] = true
				}
			}
			if l == &goLang {
				switch {
				case strings.HasPrefix(line, "import ("):
					inGoImport = true
					continue
				case inGoImport && strings.HasPrefix(line, ")"):
					inGoImport = false
					continue
				}
			}
			for i, re := range l.imports {
				if l == &goLang && i == 0 && !inGoImport {
					continue
				}
				if m := re.FindStringSubmatch(line); m != nil {
					mod := m[1]
					id := "module:" + mod
					if !modules[mod] {
						modules[mod] = true
						add(Node{ID: id, Label: mod, FileType: "code", Kind: "module"})
					}
					g.Edges = append(g.Edges, Edge{Source: fileID, Target: id, Relation: "imports", Confidence: "EXTRACTED"})
					break
				}
			}
			for i, re := range l.defs {
				m := re.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				name := m[len(m)-1]
				if l == &mdLang {
					name = strings.TrimSpace(name)
				}
				label := name
				kind := l.kinds[i]
				var recv string
				if ri := re.SubexpIndex("recv"); ri > 0 {
					recv = m[ri]
					label = recv + "." + name
				}
				id := rel + "#" + label
				if _, dup := g.byIDLookup(id, len(g.Nodes)); dup {
					break
				}
				ft := "code"
				if l == &mdLang {
					ft = "document"
				}
				add(Node{ID: id, Label: label, FileType: ft, File: rel, Location: fmt.Sprintf("L%d", lineNo), Kind: kind, Doc: shortDoc(doc)})
				if l == &pyLang {
					pyDocFor = len(g.Nodes) - 1
				}
				g.Edges = append(g.Edges, Edge{Source: fileID, Target: id, Relation: "defines", Confidence: "EXTRACTED"})
				if recv != "" {
					methodOf = append(methodOf, [3]string{id, path.Dir(rel), recv})
				}
				if l != &mdLang && kind != "method" {
					defs = append(defs, def{id, name, rel})
				}
				break
			}
			if _, isComment := comment(trim); !isComment {
				doc = doc[:0] // A comment only describes what directly follows it.
			}
		}
		fileWords[rel] = words
	}

	// Methods belong to their type, which may be in another file of the same package.
	typeIn := map[[2]string]string{}
	for _, n := range g.Nodes {
		if n.Kind == "type" || n.Kind == "class" {
			typeIn[[2]string{path.Dir(n.File), n.Label}] = n.ID
		}
	}
	for _, m := range methodOf {
		if t, ok := typeIn[[2]string{m[1], m[2]}]; ok {
			g.Edges = append(g.Edges, Edge{Source: m[0], Target: t, Relation: "method_of", Confidence: "EXTRACTED"})
		}
	}

	// A file that mentions a name defined in another file probably uses it. This is a guess
	// from words alone, so it is marked INFERRED, and names that are too short or too common
	// to mean anything are left out.
	byName := map[string][]def{}
	for _, d := range defs {
		if distinctive(d.name) {
			byName[d.name] = append(byName[d.name], d)
		}
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, rel := range files {
		words := fileWords[rel]
		if words == nil {
			continue
		}
		uses := 0
		for _, name := range names {
			if !words[name] {
				continue
			}
			ds := byName[name]
			if len(ds) > 3 {
				continue // Defined in many places: too ambiguous to link.
			}
			for _, d := range ds {
				if d.file == rel || uses >= maxUsesByFile {
					continue
				}
				g.Edges = append(g.Edges, Edge{Source: "file:" + rel, Target: d.id, Relation: "uses", Confidence: "INFERRED"})
				uses++
			}
		}
	}
	g.index()
	return g, nil
}

// distinctive says whether a name is unusual enough that a file mentioning it probably means
// it: one with a capital, an underscore or a digit, or a long one. Plain words such as state
// or commit turn up everywhere.
func distinctive(name string) bool {
	if len(name) < 4 || commonNames[strings.ToLower(name)] {
		return false
	}
	return len(name) >= 7 || strings.ContainsAny(name, "_0123456789") || strings.ToLower(name) != name
}

var identRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,}`)

// byIDLookup reports whether an id is already among the first n nodes. Only used while
// building, before the index exists; a duplicate definition, such as an overload, is kept once.
func (g *Graph) byIDLookup(id string, n int) (int, bool) {
	if g.byID == nil {
		g.byID = map[string]int{}
	}
	if i, ok := g.byID[id]; ok {
		return i, true
	}
	g.byID[id] = n
	return n, false
}

// comment reads a line of a comment, in the styles the indexed languages use.
func comment(trim string) (string, bool) {
	for _, p := range []string{"///", "//", "/**", "*/", "* ", "#"} {
		if rest, ok := strings.CutPrefix(trim, p); ok {
			if p == "#" && (strings.HasPrefix(rest, "include") || strings.HasPrefix(rest, "!")) {
				return "", false // A C include or a shebang, not a comment.
			}
			return strings.TrimSpace(rest), true
		}
	}
	if trim == "*" {
		return "", true
	}
	return "", false
}

// shortDoc joins comment lines into one short line: the first sentence or so is enough to
// know what a symbol is for.
func shortDoc(lines []string) string {
	text := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	if r := []rune(text); len(r) > 240 {
		text = string(r[:240])
	}
	return text
}

func fileType(rel string) string {
	if strings.EqualFold(filepath.Ext(rel), ".md") {
		return "document"
	}
	return "code"
}

// trackedFiles lists what git tracks under root, so ignored and generated files stay out.
func trackedFiles(ctx context.Context, root string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("list files in %s: %w", root, err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, filepath.ToSlash(f))
		}
	}
	slices.Sort(files)
	return files, nil
}
