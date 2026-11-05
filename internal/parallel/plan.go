// Package parallel decides what can safely run at the same time in one working tree.
//
// Worktrees keep tasks apart, each in its own checkout. This is the level below: one task
// split among subagents in one checkout, with no merge afterwards to catch a collision. So it
// is deliberately conservative. Jobs share a wave only if what they claim cannot overlap, a
// job that claims nothing runs alone, and Verify checks with git what each wave really did.
//
// It holds no state, and neither reads nor changes the swarm's own.
package parallel

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Granularity is how finely claims are compared. Coarser is safer, and finer runs more at once.
// Symbol lets two jobs share a file, so it relies on them keeping to their parts.
type Granularity string

// The levels, coarsest first. Auto picks between Dir and File, and Symbol is only by request.
const (
	Repo   Granularity = "repo"
	Dir    Granularity = "dir"
	File   Granularity = "file"
	Symbol Granularity = "symbol"
	Auto   Granularity = "auto"
)

// ExactLimit is how many jobs are solved exactly. It takes moments at this size, and the work
// grows too fast to go much higher. Beyond it, a greedy schedule is used.
const ExactLimit = 14

// Job is one piece of work to give a subagent.
type Job struct {
	ID string `json:"id" yaml:"id"`
	// Touches are what it writes: a file, a directory ending in /, file#symbol or file:10-40.
	Touches []string `json:"touches,omitempty" yaml:"touches,omitempty"`
	// Reads are what it depends on staying the same while it works.
	Reads []string `json:"reads,omitempty" yaml:"reads,omitempty"`
	// Deps must finish, in an earlier wave, before this starts.
	Deps []string `json:"deps,omitempty" yaml:"deps,omitempty"`
	// Exclusive runs it alone, as for a formatter or a dependency install.
	Exclusive bool `json:"exclusive,omitempty" yaml:"exclusive,omitempty"`
	// Weight is its rough size, used to run long chains first. 1 if empty.
	Weight int `json:"weight,omitempty" yaml:"weight,omitempty"`
}

// Input is a set of jobs and how to plan them.
type Input struct {
	Jobs        []Job       `json:"jobs" yaml:"jobs"`
	Granularity Granularity `json:"granularity,omitempty" yaml:"granularity,omitempty"`
	Max         int         `json:"max,omitempty" yaml:"max,omitempty"` // Most at once.
}

// Conflict says why two jobs cannot share a wave.
type Conflict struct {
	A   string `json:"a"`
	B   string `json:"b"`
	Why string `json:"why"`
}

// Wave is a set of jobs to run at the same time.
type Wave struct {
	N    int      `json:"n"`
	Jobs []string `json:"jobs"`
}

// Plan is the schedule.
type Plan struct {
	// Verdict is parallel if anything runs at once, and sequential if nothing can.
	Verdict     string      `json:"verdict"`
	Granularity Granularity `json:"granularity"`
	Waves       []Wave      `json:"waves"`
	Conflicts   []Conflict  `json:"conflicts,omitempty"`
	// Alone lists jobs that claim nothing, or asked to be exclusive.
	Alone  []string `json:"alone,omitempty"`
	Method string   `json:"method"` // exact or greedy.
	// Optimal is true if no schedule could use fewer waves.
	Optimal bool `json:"optimal"`
	// Tried says how many waves each level took, when the level was chosen automatically.
	Tried map[Granularity]int `json:"tried,omitempty"`
	// Writes is what each job may change, which Verify checks against.
	Writes map[string][]string `json:"writes"`
	Max    int                 `json:"max"`
}

// claim is one parsed path, with an optional part of a file.
type claim struct {
	raw      string
	path     string
	isDir    bool
	symbol   string
	from, to int
}

var linesRE = regexp.MustCompile(`^(.+):(\d+)-(\d+)$`)

func parseClaim(s string) (claim, error) {
	c := claim{raw: s}
	p := strings.TrimSpace(strings.ReplaceAll(s, `\`, "/"))
	if p == "" {
		return c, errors.New("an empty path")
	}
	if m := linesRE.FindStringSubmatch(p); m != nil {
		from, err1 := strconv.Atoi(m[2])
		to, err2 := strconv.Atoi(m[3])
		if err1 != nil || err2 != nil || from < 1 || to < from {
			return c, fmt.Errorf("%q: bad line range", s)
		}
		p, c.from, c.to = m[1], from, to
	} else if before, sym, ok := strings.Cut(p, "#"); ok {
		if sym == "" {
			return c, fmt.Errorf("%q: no symbol after #", s)
		}
		p, c.symbol = before, sym
	}
	// A wildcard could match anything below it, so it claims that whole directory.
	if i := strings.IndexAny(p, "*?["); i >= 0 {
		p = p[:strings.LastIndex(p[:i], "/")+1]
		if p == "" {
			p = "./"
		}
		c.symbol, c.from, c.to = "", 0, 0
	}
	c.isDir = strings.HasSuffix(p, "/")
	if strings.HasPrefix(p, "/") {
		return c, fmt.Errorf("%q: use a path relative to the project", s)
	}
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") {
		return c, fmt.Errorf("%q: outside the project", s)
	}
	if p == "." {
		c.isDir = true
	}
	// Compared without case, since on macOS and Windows these can be one file.
	c.path = strings.ToLower(p)
	return c, nil
}

// at reduces a claim to a granularity.
func (c claim) at(g Granularity) claim {
	out := claim{raw: c.raw, path: c.path, isDir: c.isDir}
	switch g {
	case Repo:
		out.path, out.isDir = ".", true
	case Dir:
		if !c.isDir {
			out.path, out.isDir = path.Dir(c.path), true
		}
	case File:
	default:
		out.symbol, out.from, out.to = c.symbol, c.from, c.to
	}
	return out
}

func pathsOverlap(a, b string) bool {
	if a == "." || b == "." || a == b {
		return true
	}
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func overlap(a, b claim) bool {
	if !pathsOverlap(a.path, b.path) {
		return false
	}
	if a.path != b.path {
		return true
	}
	switch {
	case a.symbol != "" && b.symbol != "":
		return a.symbol == b.symbol
	case a.from != 0 && b.from != 0:
		return a.from <= b.to && b.from <= a.to
	}
	// A whole file against a part, or a symbol against lines, which could be the same place.
	return true
}

type parsed struct {
	job           Job
	writes, reads []claim
	alone         bool
}

func parse(jobs []Job) ([]parsed, error) {
	if len(jobs) == 0 {
		return nil, errors.New("no jobs")
	}
	seen := make(map[string]bool, len(jobs))
	out := make([]parsed, len(jobs))
	for i, j := range jobs {
		if j.ID == "" {
			return nil, fmt.Errorf("job %d has no id", i+1)
		}
		if seen[j.ID] {
			return nil, fmt.Errorf("job %s is listed twice", j.ID)
		}
		seen[j.ID] = true
		p := parsed{job: j, alone: j.Exclusive || len(j.Touches) == 0}
		for _, s := range j.Touches {
			c, err := parseClaim(s)
			if err != nil {
				return nil, fmt.Errorf("job %s: %w", j.ID, err)
			}
			p.writes = append(p.writes, c)
		}
		for _, s := range j.Reads {
			c, err := parseClaim(s)
			if err != nil {
				return nil, fmt.Errorf("job %s: %w", j.ID, err)
			}
			p.reads = append(p.reads, c)
		}
		out[i] = p
	}
	for _, p := range out {
		for _, d := range p.job.Deps {
			if !seen[d] {
				return nil, fmt.Errorf("job %s depends on %s, which is not listed", p.job.ID, d)
			}
		}
	}
	return out, nil
}

// conflictBetween says why two jobs cannot run together, or "" if they can.
func conflictBetween(a, b parsed, g Granularity) string {
	if a.alone {
		return a.job.ID + " must run alone"
	}
	if b.alone {
		return b.job.ID + " must run alone"
	}
	for _, x := range a.writes {
		for _, y := range b.writes {
			if overlap(x.at(g), y.at(g)) {
				return fmt.Sprintf("both write: %s and %s", x.raw, y.raw)
			}
		}
		for _, y := range b.reads {
			if overlap(x.at(g), y.at(g)) {
				return fmt.Sprintf("%s writes %s, which %s reads as %s", a.job.ID, x.raw, b.job.ID, y.raw)
			}
		}
	}
	for _, x := range b.writes {
		for _, y := range a.reads {
			if overlap(x.at(g), y.at(g)) {
				return fmt.Sprintf("%s writes %s, which %s reads as %s", b.job.ID, x.raw, a.job.ID, y.raw)
			}
		}
	}
	return ""
}

// graph is the problem in the form the schedulers use.
type graph struct {
	n         int
	conflict  [][]bool
	depMask   []uint64
	deps      [][]int
	dependent [][]int
	weight    []int
	priority  []int
	ids       []string
}

func build(ps []parsed, g Granularity) (*graph, []Conflict, error) {
	n := len(ps)
	gr := &graph{n: n, conflict: make([][]bool, n), depMask: make([]uint64, n), deps: make([][]int, n), dependent: make([][]int, n), weight: make([]int, n), priority: make([]int, n), ids: make([]string, n)}
	index := make(map[string]int, n)
	for i, p := range ps {
		index[p.job.ID] = i
		gr.ids[i] = p.job.ID
		gr.conflict[i] = make([]bool, n)
		gr.weight[i] = max(p.job.Weight, 1)
	}
	for i, p := range ps {
		for _, d := range p.job.Deps {
			j := index[d]
			if j == i {
				return nil, nil, fmt.Errorf("job %s depends on itself", p.job.ID)
			}
			gr.deps[i] = append(gr.deps[i], j)
			gr.dependent[j] = append(gr.dependent[j], i)
			if n <= 64 {
				gr.depMask[i] |= 1 << j
			}
		}
	}
	if err := gr.computePriority(); err != nil {
		return nil, nil, err
	}
	var conflicts []Conflict
	for i := range n {
		for j := i + 1; j < n; j++ {
			if why := conflictBetween(ps[i], ps[j], g); why != "" {
				gr.conflict[i][j], gr.conflict[j][i] = true, true
				conflicts = append(conflicts, Conflict{ps[i].job.ID, ps[j].job.ID, why})
			}
		}
	}
	return gr, conflicts, nil
}

// computePriority works out how much must still follow each job, and refuses a cycle.
func (g *graph) computePriority() error {
	const (
		unseen = iota
		visiting
		finished
	)
	mark := make([]int, g.n)
	var visit func(i int) error
	visit = func(i int) error {
		switch mark[i] {
		case visiting:
			return fmt.Errorf("a dependency cycle goes through %s", g.ids[i])
		case finished:
			return nil
		}
		mark[i] = visiting
		tail := 0
		for _, d := range g.dependent[i] {
			if err := visit(d); err != nil {
				return err
			}
			tail = max(tail, g.priority[d])
		}
		g.priority[i] = g.weight[i] + tail
		mark[i] = finished
		return nil
	}
	for i := range g.n {
		if err := visit(i); err != nil {
			return err
		}
	}
	return nil
}

// order ranks jobs: the longest chain first, then the most contested, then by name, so that
// the same input always gives the same plan.
func (g *graph) order() []int {
	degree := make([]int, g.n)
	for i := range g.n {
		for j := range g.n {
			if g.conflict[i][j] {
				degree[i]++
			}
		}
	}
	idx := make([]int, g.n)
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(a, b int) int {
		return cmp.Or(cmp.Compare(g.priority[b], g.priority[a]), cmp.Compare(degree[b], degree[a]), cmp.Compare(g.ids[a], g.ids[b]))
	})
	return idx
}

func (g *graph) greedy(limit int) [][]int {
	order := g.order()
	done := make([]bool, g.n)
	var waves [][]int
	for left := g.n; left > 0; {
		var wave []int
		for _, i := range order {
			if done[i] || len(wave) == limit {
				continue
			}
			ready := !slices.ContainsFunc(g.deps[i], func(d int) bool { return !done[d] })
			clash := slices.ContainsFunc(wave, func(w int) bool { return g.conflict[i][w] })
			if ready && !clash {
				wave = append(wave, i)
			}
		}
		for _, i := range wave {
			done[i] = true
		}
		left -= len(wave)
		waves = append(waves, wave)
	}
	return waves
}

// exact finds a schedule with the fewest waves, by search over which jobs are finished. Only
// sets that cannot take another job need trying, because running a job sooner never hurts:
// it can always be taken back out of a later wave without breaking anything.
func (g *graph) exact(limit int) [][]int {
	order := g.order()
	all := uint64(1)<<g.n - 1
	type answer struct {
		waves int
		next  uint64
	}
	memo := map[uint64]answer{all: {0, 0}}
	var solve func(done uint64) int
	solve = func(done uint64) int {
		if a, ok := memo[done]; ok {
			return a.waves
		}
		var ready []int
		for _, i := range order {
			if done&(1<<i) == 0 && g.depMask[i]&^done == 0 {
				ready = append(ready, i)
			}
		}
		best := answer{waves: g.n + 1}
		var pick func(k int, chosen uint64, count int)
		pick = func(k int, chosen uint64, count int) {
			if count == limit || k == len(ready) {
				if chosen == 0 {
					return
				}
				if count < limit && !g.maximal(ready, chosen) {
					return
				}
				if w := 1 + solve(done|chosen); w < best.waves {
					best = answer{w, chosen}
				}
				return
			}
			i := ready[k]
			fits := true
			for j := range g.n {
				if chosen&(1<<j) != 0 && g.conflict[i][j] {
					fits = false
					break
				}
			}
			if fits {
				pick(k+1, chosen|1<<i, count+1)
			}
			pick(k+1, chosen, count)
		}
		pick(0, 0, 0)
		memo[done] = best
		return best.waves
	}
	solve(0)
	var waves [][]int
	for done := uint64(0); done != all; {
		next := memo[done].next
		var wave []int
		for _, i := range order {
			if next&(1<<i) != 0 {
				wave = append(wave, i)
			}
		}
		waves = append(waves, wave)
		done |= next
	}
	return waves
}

// maximal reports whether no ready job could be added to this set.
func (g *graph) maximal(ready []int, chosen uint64) bool {
	for _, i := range ready {
		if chosen&(1<<i) != 0 {
			continue
		}
		fits := true
		for j := range g.n {
			if chosen&(1<<j) != 0 && g.conflict[i][j] {
				fits = false
				break
			}
		}
		if fits {
			return false
		}
	}
	return true
}

// lowerBound is a number of waves no schedule can beat, which can prove a greedy one optimal.
func (g *graph) lowerBound(limit int) int {
	chain := make([]int, g.n)
	var depth func(i int) int
	depth = func(i int) int {
		if chain[i] != 0 {
			return chain[i]
		}
		d := 1
		for _, x := range g.dependent[i] {
			d = max(d, 1+depth(x))
		}
		chain[i] = d
		return d
	}
	best := (g.n + limit - 1) / limit
	for i := range g.n {
		best = max(best, depth(i))
	}
	// Jobs that all conflict with one another need a wave each.
	var clique []int
	for _, i := range g.order() {
		if !slices.ContainsFunc(clique, func(c int) bool { return !g.conflict[i][c] }) {
			clique = append(clique, i)
		}
	}
	return max(best, len(clique))
}

func makePlan(ps []parsed, level Granularity, limit int) (*Plan, error) {
	g, conflicts, err := build(ps, level)
	if err != nil {
		return nil, err
	}
	p := &Plan{Granularity: level, Conflicts: conflicts, Max: limit, Writes: map[string][]string{}}
	var waves [][]int
	if g.n <= ExactLimit {
		waves, p.Method, p.Optimal = g.exact(limit), "exact", true
	} else {
		waves, p.Method = g.greedy(limit), "greedy"
		p.Optimal = len(waves) == g.lowerBound(limit)
	}
	for n, w := range waves {
		ids := make([]string, len(w))
		for i, j := range w {
			ids[i] = g.ids[j]
		}
		slices.Sort(ids)
		p.Waves = append(p.Waves, Wave{N: n + 1, Jobs: ids})
	}
	p.Verdict = "sequential"
	if len(p.Waves) < g.n {
		p.Verdict = "parallel"
	}
	for _, x := range ps {
		p.Writes[x.job.ID] = x.job.Touches
		if x.alone {
			p.Alone = append(p.Alone, x.job.ID)
		}
	}
	return p, nil
}

// Schedule plans the jobs. Auto uses the coarser of directory and file level that loses no
// parallelism, and never goes below whole files. Two subagents in one file can overwrite each
// other, and a symbol is only a name someone wrote down: a real model, told to split one file,
// made up symbols to fit. So working inside one file has to be asked for by name.
func Schedule(in Input) (*Plan, error) {
	ps, err := parse(in.Jobs)
	if err != nil {
		return nil, err
	}
	limit := in.Max
	if limit <= 0 {
		limit = 4
	}
	level := in.Granularity
	switch level {
	case "":
		level = Auto
	case Auto, Repo, Dir, File, Symbol:
	default:
		return nil, fmt.Errorf("granularity %q: use repo, dir, file, symbol or auto", level)
	}
	if level != Auto {
		return makePlan(ps, level, limit)
	}
	finest, err := makePlan(ps, File, limit)
	if err != nil {
		return nil, err
	}
	coarse, err := makePlan(ps, Dir, limit)
	if err != nil {
		return nil, err
	}
	tried := map[Granularity]int{File: len(finest.Waves), Dir: len(coarse.Waves)}
	best := finest
	if len(coarse.Waves) <= len(finest.Waves) {
		best = coarse
	}
	best.Tried = tried
	return best, nil
}

// CheckClaims confirms that the parts of files a plan relies on exist. It catches a claim made
// up to fit, such as a symbol that appears nowhere in the file. A file that does not exist yet
// passes, since a job may be creating it. It says nothing about whether a job stays inside.
func CheckClaims(dir string, jobs []Job) error {
	for _, j := range jobs {
		for _, s := range append(slices.Clone(j.Touches), j.Reads...) {
			c, err := parseClaim(s)
			if err != nil {
				return err
			}
			if c.symbol == "" && c.from == 0 {
				continue
			}
			raw, _, _ := strings.Cut(strings.ReplaceAll(s, `\`, "/"), "#")
			if m := linesRE.FindStringSubmatch(raw); m != nil {
				raw = m[1]
			}
			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(raw)))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf("job %s: %w", j.ID, err)
			}
			if c.symbol != "" && !bytes.Contains(b, []byte(c.symbol)) {
				return fmt.Errorf("job %s claims %s, but %q appears nowhere in that file", j.ID, s, c.symbol)
			}
			if lines := bytes.Count(b, []byte("\n")) + 1; c.to > lines {
				return fmt.Errorf("job %s claims %s, but that file has %d lines", j.ID, s, lines)
			}
		}
	}
	return nil
}
