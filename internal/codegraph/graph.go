// Package codegraph is a map of a codebase that agents ask instead of reading files: which
// symbols exist, where, and how they connect. One graph serves every agent in the swarm, so
// the exploring is done once and each agent reads only the slice it asks for.
//
// It reads the graph.json that Graphify writes (github.com/Graphify-Labs/Graphify), a
// NetworkX node-link graph built with tree-sitter, and can build the same structure itself
// with a simpler indexer when Graphify is not installed.
package codegraph

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Node is a file, symbol, concept or document in the graph.
type Node struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	FileType  string `json:"file_type,omitempty"`
	File      string `json:"source_file,omitempty"`
	Location  string `json:"source_location,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Community int    `json:"community,omitempty"`
	// Doc is what the code says about the symbol: its doc comment, shortened.
	Doc string `json:"doc,omitempty"`
}

// Edge connects two nodes, such as a file that defines a function or one that imports a module.
type Edge struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	Relation   string `json:"relation"`
	Confidence string `json:"confidence,omitempty"`
}

// Graph is a loaded graph with its indexes. It is read-only once built, so many agents can
// ask at once.
type Graph struct {
	Nodes []Node
	Edges []Edge
	// Source says what built it: graphify or builtin.
	Source string

	byID  map[string]int
	out   [][]int // Edge indexes by source node.
	in    [][]int // Edge indexes by target node.
	terms []map[string]float64
	df    map[string]int
	label map[string][]int // Lower-case label to nodes.
}

func isTest(file string) bool {
	return strings.Contains(file, "_test.") || strings.Contains(file, ".test.") || strings.Contains(file, ".spec.") ||
		strings.HasPrefix(file, "test/") || strings.Contains(file, "/test/") || strings.Contains(file, "/tests/")
}

// raw is graph.json as NetworkX writes it, read loosely: values Graphify adds over time are
// ignored, and either "links" or "edges" may hold the edges.
type raw struct {
	Nodes []map[string]any `json:"nodes"`
	Links []map[string]any `json:"links"`
	Edges []map[string]any `json:"edges"`
}

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// Load reads a graph.json.
func Load(path string) (*Graph, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads graph.json content.
func Parse(b []byte) (*Graph, error) {
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("graph.json: %w", err)
	}
	g := &Graph{Source: "graphify"}
	for _, m := range r.Nodes {
		n := Node{ID: str(m, "id"), Label: str(m, "label"), FileType: str(m, "file_type"), File: str(m, "source_file"),
			Location: str(m, "source_location"), Kind: str(m, "kind"),
			Doc: cmp.Or(str(m, "doc"), str(m, "docstring"), str(m, "description"), str(m, "rationale"))}
		if n.ID == "" {
			continue
		}
		if n.Label == "" {
			n.Label = n.ID
		}
		if c, ok := m["community"].(float64); ok {
			n.Community = int(c)
		}
		g.Nodes = append(g.Nodes, n)
	}
	links := r.Links
	if len(links) == 0 {
		links = r.Edges
	}
	for _, m := range links {
		e := Edge{Source: str(m, "source"), Target: str(m, "target"), Relation: str(m, "relation"), Confidence: str(m, "confidence")}
		if e.Source == "" || e.Target == "" {
			continue
		}
		if e.Relation == "" {
			e.Relation = "related"
		}
		g.Edges = append(g.Edges, e)
	}
	g.index()
	return g, nil
}

// index builds the lookups, and the term weights for search.
func (g *Graph) index() {
	g.byID = make(map[string]int, len(g.Nodes))
	g.label = map[string][]int{}
	for i, n := range g.Nodes {
		g.byID[n.ID] = i
		l := strings.ToLower(n.Label)
		g.label[l] = append(g.label[l], i)
	}
	g.out = make([][]int, len(g.Nodes))
	g.in = make([][]int, len(g.Nodes))
	kept := g.Edges[:0]
	for _, e := range g.Edges {
		s, ok1 := g.byID[e.Source]
		t, ok2 := g.byID[e.Target]
		if !ok1 || !ok2 {
			continue // An edge to nothing helps no one.
		}
		kept = append(kept, e)
		g.out[s] = append(g.out[s], len(kept)-1)
		g.in[t] = append(g.in[t], len(kept)-1)
	}
	g.Edges = kept
	g.terms = make([]map[string]float64, len(g.Nodes))
	g.df = map[string]int{}
	for i, n := range g.Nodes {
		w := map[string]float64{}
		for _, t := range Terms(n.Label) {
			w[t] += 3 // A word in the name counts most.
		}
		for _, t := range Terms(n.File) {
			w[t] += 1
		}
		for _, t := range Terms(n.Doc) {
			w[t] += 1.5 // What a symbol does, in words, is what a question asks about.
		}
		g.terms[i] = w
		for t := range w {
			g.df[t]++
		}
	}
}

var stop = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "what": true, "where": true, "which": true, "how": true,
	"does": true, "this": true, "that": true, "from": true, "into": true, "are": true, "is": true, "of": true,
	"to": true, "in": true, "on": true, "a": true, "an": true, "it": true, "be": true, "do": true, "by": true,
	"go": false, "func": true, "function": true, "file": true, "code": true, "who": true, "calls": true, "uses": true,
}

// Terms splits text into search words: lower case, split at punctuation and at camelCase and
// snake_case boundaries, so ValidateToken and validate_token both give validate and token.
func Terms(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) >= 2 {
			w := strings.ToLower(string(cur))
			if !stop[w] {
				out = append(out, stem(w))
			}
		}
		cur = cur[:0]
	}
	rs := []rune(text)
	for i, r := range rs {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// A capital after a lower-case letter starts a word, validateToken, and so does the
			// last capital of an acronym followed by lower case: HTTPServer is HTTP and Server.
			if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(rs[i-1]) ||
				(unicode.IsUpper(rs[i-1]) && i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
				flush()
			}
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// stem folds word forms together by keeping the first five letters, after dropping a plural
// s: tokens and token, verified and verify, fenced and fence, validate and validation all
// meet. Crude, but identifiers are short, and the rarity weighting keeps a loose match from
// counting for much.
func stem(w string) string {
	if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
		w = w[:len(w)-1]
	}
	if r := []rune(w); len(r) > 5 {
		return string(r[:5])
	}
	return w
}

// Stats sums the graph up.
type Stats struct {
	Source string `json:"source"`
	Nodes  int    `json:"nodes"`
	Edges  int    `json:"edges"`
	Files  int    `json:"files"`
}

// Stats sums the graph up.
func (g *Graph) Stats() Stats {
	files := map[string]bool{}
	for _, n := range g.Nodes {
		if n.File != "" {
			files[n.File] = true
		}
	}
	return Stats{Source: g.Source, Nodes: len(g.Nodes), Edges: len(g.Edges), Files: len(files)}
}

// Answer is what a question gets: a compact text slice of the graph, small enough to read in
// one go, and the nodes it covers.
type Answer struct {
	Text      string   `json:"text"`
	Nodes     []string `json:"nodes"`
	Matched   int      `json:"matched"`
	Truncated bool     `json:"truncated,omitempty"`
	// Tokens is about how many tokens Text costs to read.
	Tokens int `json:"tokens"`
}

// DefaultBudget is how many tokens an answer may cost when the caller does not say.
const DefaultBudget = 1500

// charsPerToken is a rough average for code and identifiers.
const charsPerToken = 3.5

func (g *Graph) score(terms []string) []float64 {
	n := float64(len(g.Nodes))
	scores := make([]float64, len(g.Nodes))
	covered := make([]int, len(g.Nodes))
	terms = slices.Compact(slices.Sorted(slices.Values(terms)))
	for _, t := range terms {
		df := g.df[t]
		if df == 0 {
			continue
		}
		idf := math.Log(1 + n/float64(df))
		for i, w := range g.terms {
			if v, ok := w[t]; ok {
				scores[i] += v * idf
				covered[i]++
			}
		}
	}
	// A node that matches more of the question's words is more likely what it is about than
	// one that matches a single word strongly: "session tokens verified" is about the method
	// that verifies tokens, not the one with Token in its name three times.
	for i := range scores {
		if covered[i] > 0 {
			scores[i] *= float64(1+covered[i]) / float64(1+len(terms))
		}
	}
	return scores
}

// Query answers a question with the part of the graph it is about: the best matching nodes,
// and around them what they connect to, nearest first, cut to the token budget.
func (g *Graph) Query(question string, budget int) Answer {
	if budget <= 0 {
		budget = DefaultBudget
	}
	terms := Terms(question)
	scores := g.score(terms)
	// An exact name in the question is what it is about, unless it is an ordinary word.
	for _, w := range strings.FieldsFunc(question, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.') }) {
		// Only a word written like code: ReleaseHeldBy, Store.Put or snake_case. An ordinary
		// word that happens to name a type, such as usage, is matched by its meaning instead.
		if len(w) < 3 || stop[strings.ToLower(w)] || (strings.ToLower(w) == w && !strings.ContainsAny(w, "_.")) {
			continue
		}
		for _, i := range g.label[strings.ToLower(w)] {
			scores[i] += 50
		}
	}
	for i, n := range g.Nodes {
		if scores[i] > 0 && isTest(n.File) {
			scores[i] *= 0.7 // The code itself is usually what is asked about, not its tests.
		}
	}
	var seeds []int
	for i, s := range scores {
		if s > 0 {
			seeds = append(seeds, i)
		}
	}
	slices.SortFunc(seeds, func(a, b int) int {
		return cmp.Or(cmp.Compare(scores[b], scores[a]), cmp.Compare(g.degree(b), g.degree(a)), strings.Compare(g.Nodes[a].ID, g.Nodes[b].ID))
	})
	matched := len(seeds)
	// Only seeds close to the best one: a long tail of weak matches costs tokens and says little.
	if len(seeds) > 0 {
		top := scores[seeds[0]]
		cut := 0
		for cut < len(seeds) && cut < 6 && scores[seeds[cut]] >= top*0.45 {
			cut++
		}
		seeds = seeds[:max(cut, 1)]
	}
	if len(seeds) == 0 {
		return Answer{Text: fmt.Sprintf("Nothing in the code graph matches %q. Try a symbol, file or package name.", question)}
	}
	return g.render(fmt.Sprintf("Code graph: %d matches for %q, nearest first.", matched, question), seeds, 1, budget, matched, scores)
}

func (g *Graph) degree(i int) int { return len(g.out[i]) + len(g.in[i]) }

// Symbol describes one symbol or file by name: where it is, and everything it connects to.
func (g *Graph) Symbol(name string, budget int) (Answer, error) {
	if budget <= 0 {
		budget = DefaultBudget
	}
	idx := g.find(name)
	if len(idx) == 0 {
		return Answer{}, fmt.Errorf("no symbol or file named %q in the code graph", name)
	}
	if len(idx) > 5 {
		idx = idx[:5]
	}
	return g.render(fmt.Sprintf("%q in the code graph:", name), idx, 1, budget, len(idx), nil), nil
}

// find looks a name up: by id, then label, then the end of a dotted or path name.
func (g *Graph) find(name string) []int {
	if i, ok := g.byID[name]; ok {
		return []int{i}
	}
	if idx := g.label[strings.ToLower(name)]; len(idx) > 0 {
		return slices.Clone(idx)
	}
	low := strings.ToLower(name)
	var out []int
	for i, n := range g.Nodes {
		l := strings.ToLower(n.Label)
		if strings.HasSuffix(l, "."+low) || strings.HasSuffix(l, "/"+low) || strings.HasSuffix(strings.ToLower(n.File), low) {
			out = append(out, i)
		}
	}
	slices.SortFunc(out, func(a, b int) int { return cmp.Compare(g.degree(b), g.degree(a)) })
	return out
}

// render walks out from the seeds, breadth first, and writes what it finds until the budget
// is spent: seeds first, then their neighbours, then theirs. Within a level, what the question
// is about comes first, then symbols before files and modules, then the busiest.
func (g *Graph) render(head string, seeds []int, depth, budget, matched int, scores []float64) Answer {
	limit := int(float64(budget) * charsPerToken)
	var b strings.Builder
	b.WriteString(head)
	b.WriteString("\n")
	seen := map[int]bool{}
	var covered []string
	level := seeds
	truncated := false
	rank := func(i int) float64 {
		r := 0.0
		if scores != nil {
			r = scores[i] * 100
		}
		switch g.Nodes[i].Kind {
		case "module":
			r -= 50
		case "file":
			r -= 20
		}
		return r + math.Log1p(float64(g.degree(i)))
	}
	for d := 0; d <= depth && len(level) > 0 && !truncated; d++ {
		var next []int
		for _, i := range level {
			if seen[i] {
				continue
			}
			block := g.nodeBlock(i, d == 0)
			if b.Len()+len(block) > limit {
				truncated = true
				break
			}
			seen[i] = true
			b.WriteString(block)
			covered = append(covered, g.Nodes[i].ID)
			for _, e := range g.out[i] {
				next = append(next, g.byID[g.Edges[e].Target])
			}
			for _, e := range g.in[i] {
				next = append(next, g.byID[g.Edges[e].Source])
			}
		}
		slices.Sort(next)
		next = slices.Compact(next)
		// A file or module already shows on the lines of what it defines or is imported by;
		// a block of its own would mostly list imports.
		next = slices.DeleteFunc(next, func(j int) bool { return seen[j] || g.Nodes[j].Kind == "module" || g.Nodes[j].Kind == "file" })
		slices.SortStableFunc(next, func(a, c int) int { return cmp.Compare(rank(c), rank(a)) })
		// Each level further out matters less, so fewer of it are shown.
		if keep := []int{8, 5}[min(d, 1)]; len(next) > keep {
			next = next[:keep]
		}
		level = next
	}
	if truncated {
		b.WriteString("… more in the graph; ask about a name above to go deeper.\n")
	}
	text := b.String()
	return Answer{Text: text, Nodes: covered, Matched: matched, Truncated: truncated, Tokens: int(math.Ceil(float64(len(text)) / charsPerToken))}
}

// groupAt is how many edges of one relation are listed one per line; beyond it they are
// summed up on one line, which is what keeps a busy file from flooding the answer.
const groupAt = 4

func (g *Graph) nodeBlock(i int, seed bool) string {
	n := g.Nodes[i]
	var b strings.Builder
	mark := "-"
	if seed {
		mark = "*"
	}
	fmt.Fprintf(&b, "%s %s", mark, n.Label)
	if kind := cmp.Or(n.Kind, n.FileType); kind != "" && kind != "code" {
		fmt.Fprintf(&b, " (%s)", kind)
	}
	if n.File != "" && n.File != n.Label {
		fmt.Fprintf(&b, "  %s", n.File)
		if n.Location != "" {
			fmt.Fprintf(&b, ":%s", strings.TrimPrefix(n.Location, "L"))
		}
	}
	b.WriteString("\n")
	if seed && n.Doc != "" {
		doc := n.Doc
		if r := []rune(doc); len(r) > 160 {
			doc = string(r[:157]) + "..."
		}
		fmt.Fprintf(&b, "    %s\n", doc)
	}
	type group struct {
		arrow, relation string
		others          []int
		inferred        bool
	}
	var groups []*group
	find := func(arrow, rel string) *group {
		for _, gr := range groups {
			if gr.arrow == arrow && gr.relation == rel {
				return gr
			}
		}
		gr := &group{arrow: arrow, relation: rel}
		groups = append(groups, gr)
		return gr
	}
	for _, e := range g.out[i] {
		gr := find("->", g.Edges[e].Relation)
		gr.others = append(gr.others, g.byID[g.Edges[e].Target])
		gr.inferred = gr.inferred || g.Edges[e].Confidence == "INFERRED" || g.Edges[e].Confidence == "AMBIGUOUS"
	}
	for _, e := range g.in[i] {
		gr := find("<-", g.Edges[e].Relation)
		gr.others = append(gr.others, g.byID[g.Edges[e].Source])
		gr.inferred = gr.inferred || g.Edges[e].Confidence == "INFERRED" || g.Edges[e].Confidence == "AMBIGUOUS"
	}
	name := func(j int) string {
		o := g.Nodes[j]
		if o.File != "" && o.File != n.File && o.File != o.Label {
			return o.Label + " (" + o.File + ")"
		}
		return o.Label
	}
	for _, gr := range groups {
		guess := ""
		if gr.inferred {
			guess = "  ?"
		}
		if len(gr.others) <= groupAt {
			for _, j := range gr.others {
				fmt.Fprintf(&b, "    %s %s %s%s\n", gr.arrow, gr.relation, name(j), guess)
			}
			continue
		}
		slices.SortStableFunc(gr.others, func(a, c int) int { return cmp.Compare(g.degree(c), g.degree(a)) })
		labels := make([]string, 0, groupAt)
		for _, j := range gr.others[:groupAt] {
			labels = append(labels, g.Nodes[j].Label)
		}
		fmt.Fprintf(&b, "    %s %s %d: %s, …%s\n", gr.arrow, gr.relation, len(gr.others), strings.Join(labels, ", "), guess)
	}
	return b.String()
}

// Write saves the graph in Graphify's format, so its own tools can read it too.
func (g *Graph) Write(path string) error {
	nodes := make([]map[string]any, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		m := map[string]any{"id": n.ID, "label": n.Label, "file_type": cmp.Or(n.FileType, "code"), "source_file": n.File}
		if n.Location != "" {
			m["source_location"] = n.Location
		}
		if n.Kind != "" {
			m["kind"] = n.Kind
		}
		if n.Doc != "" {
			m["doc"] = n.Doc
		}
		nodes = append(nodes, m)
	}
	links := make([]map[string]any, 0, len(g.Edges))
	for _, e := range g.Edges {
		links = append(links, map[string]any{"source": e.Source, "target": e.Target, "relation": e.Relation,
			"confidence": cmp.Or(e.Confidence, "EXTRACTED"), "source_file": g.Nodes[g.byID[e.Source]].File})
	}
	b, err := json.Marshal(map[string]any{"directed": true, "multigraph": false, "graph": map[string]any{"built_by": g.Source},
		"nodes": nodes, "links": links})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
