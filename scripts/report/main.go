// Command report turns CI's raw results into one self-contained HTML report and a Markdown
// summary for the job page: test results from go test -json, coverage from a profile, and
// benchmarks compared between a base and a head build run on the same machine.
//
//	go run ./scripts/report \
//	  --tests "ubuntu race=ubuntu.json" --tests "macos race=macos.json" --rerun rerun.json \
//	  --cover coverage.out --cover-html coverage.html \
//	  --bench-base base.txt --bench-head head.txt --budgets scripts/perf-budgets.txt \
//	  --history history.json --sha "$GITHUB_SHA" --out report
//
// It exits 1 when a test failed for real, a benchmark broke its budget, or a benchmark got
// significantly slower or allocates more than its base. A test that failed and then passed
// when run again is reported as flaky and does not fail the run.
//
// go run ./scripts/report failed test.json   prints "package regexp" for each failed test,
// for rerunning just those.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if len(os.Args) > 1 && os.Args[1] == "failed" {
		if err := printFailed(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	var (
		tests, reruns                         multi
		cover, coverHTML, base, head, budgets string
		history, sha, ref, out, title         string
		maxSlow, maxAllocs                    float64
		record                                bool
	)
	flag.Var(&tests, "tests", "NAME=FILE of go test -json output; repeatable")
	flag.Var(&reruns, "rerun", "go test -json output of failed tests run again; repeatable")
	flag.StringVar(&cover, "cover", "", "coverage profile")
	flag.StringVar(&coverHTML, "cover-html", "", "go tool cover -html output, linked from the report")
	flag.StringVar(&base, "bench-base", "", "benchmark output of the base build")
	flag.StringVar(&head, "bench-head", "", "benchmark output of the head build")
	flag.StringVar(&budgets, "budgets", "", "budget file: NAME UNIT MAX per line")
	flag.StringVar(&history, "history", "", "JSON history of benchmark medians, read and, with --record, appended to")
	flag.BoolVar(&record, "record", false, "append this run to the history")
	flag.StringVar(&sha, "sha", "", "commit being reported on")
	flag.StringVar(&ref, "ref", "", "branch or pull request")
	flag.StringVar(&out, "out", "report", "output directory")
	flag.StringVar(&title, "title", "aiculator CI report", "report title")
	flag.Float64Var(&maxSlow, "max-slowdown", 25, "percent slower than base, significant at p<0.05, that fails")
	flag.Float64Var(&maxAllocs, "max-alloc-growth", 10, "percent more allocations than base that fails")
	flag.Parse()

	r := Report{Title: title, SHA: sha, Ref: ref, Generated: time.Now().UTC()}
	var err error
	flaky := map[string]bool{}
	for _, f := range reruns {
		passed, err := passedTests(f)
		if err != nil {
			die(err)
		}
		for k := range passed {
			flaky[k] = true
		}
	}
	for _, t := range tests {
		name, file, ok := strings.Cut(t, "=")
		if !ok {
			die(fmt.Errorf("--tests wants NAME=FILE, got %q", t))
		}
		s, err := readSuite(name, file, flaky)
		if err != nil {
			die(err)
		}
		r.Suites = append(r.Suites, s)
	}
	if cover != "" {
		if r.Coverage, err = readCoverage(cover); err != nil {
			die(err)
		}
		if coverHTML != "" {
			if err := copyFile(coverHTML, filepath.Join(out, "coverage.html")); err != nil {
				die(err)
			}
			r.Coverage.HTML = "coverage.html"
		}
	}
	if head != "" {
		if r.Perf, err = readPerf(base, head, budgets, maxSlow, maxAllocs); err != nil {
			die(err)
		}
		if history != "" {
			h, err := readHistory(history)
			if err != nil {
				die(err)
			}
			if record {
				h = appendHistory(h, sha, r.Generated, r.Perf)
				if err := writeJSON(history, h); err != nil {
					die(err)
				}
			}
			r.Perf.Charts = charts(h)
		}
	}
	r.summarize()
	if err := os.MkdirAll(out, 0o755); err != nil {
		die(err)
	}
	if err := writeHTML(filepath.Join(out, "index.html"), r); err != nil {
		die(err)
	}
	md := markdown(r)
	if err := os.WriteFile(filepath.Join(out, "summary.md"), []byte(md), 0o644); err != nil {
		die(err)
	}
	if err := writeJSON(filepath.Join(out, "report.json"), r); err != nil {
		die(err)
	}
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(md)
			_ = f.Close()
		}
	}
	fmt.Print(md)
	if !r.OK {
		os.Exit(1)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "report:", err)
	os.Exit(2)
}

// Report is everything the page shows.
type Report struct {
	Title     string    `json:"title"`
	SHA       string    `json:"sha,omitempty"`
	Ref       string    `json:"ref,omitempty"`
	Generated time.Time `json:"generated"`
	Suites    []Suite   `json:"suites,omitempty"`
	Coverage  *Coverage `json:"coverage,omitempty"`
	Perf      *Perf     `json:"perf,omitempty"`
	OK        bool      `json:"ok"`
	Problems  []string  `json:"problems,omitempty"`
}

func (r *Report) summarize() {
	for _, s := range r.Suites {
		if s.Failed > 0 {
			r.Problems = append(r.Problems, fmt.Sprintf("%s: %d tests failed", s.Name, s.Failed))
		}
		if s.BuildFailed > 0 {
			r.Problems = append(r.Problems, fmt.Sprintf("%s: %d packages failed to build or run", s.Name, s.BuildFailed))
		}
	}
	if r.Perf != nil {
		for _, b := range r.Perf.Rows {
			for _, p := range b.Problems {
				r.Problems = append(r.Problems, b.Name+": "+p)
			}
		}
	}
	r.OK = len(r.Problems) == 0
}

// ---- tests

type event struct {
	Time    time.Time
	Action  string
	Package string
	Test    string
	Elapsed float64
	Output  string
}

// Suite is one go test -json run.
type Suite struct {
	Name        string    `json:"name"`
	Packages    []Package `json:"packages"`
	Passed      int       `json:"passed"`
	Failed      int       `json:"failed"`
	Flaky       int       `json:"flaky"`
	Skipped     int       `json:"skipped"`
	BuildFailed int       `json:"build_failed"`
	Seconds     float64   `json:"seconds"`
	Slowest     []Test    `json:"slowest"`
	Failures    []Test    `json:"failures,omitempty"`
}

// Package is one package's tests.
type Package struct {
	Name    string  `json:"name"`
	Result  string  `json:"result"`
	Seconds float64 `json:"seconds"`
	Tests   []Test  `json:"tests"`
	Passed  int     `json:"passed"`
	Failed  int     `json:"failed"`
	Skipped int     `json:"skipped"`
	Output  string  `json:"output,omitempty"`
}

// Test is one test's result.
type Test struct {
	Package string  `json:"package"`
	Name    string  `json:"name"`
	Result  string  `json:"result"` // pass, fail, skip or flaky
	Seconds float64 `json:"seconds"`
	Output  string  `json:"output,omitempty"`
}

const maxOutput = 24 << 10

func shortPkg(p string) string {
	if i := strings.Index(p, "/aiculator/"); i >= 0 {
		return p[i+len("/aiculator/"):]
	}
	if strings.HasSuffix(p, "/aiculator") {
		return "."
	}
	return p
}

func readEvents(file string, fn func(event)) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue // Build errors and the like are printed as text around the JSON.
		}
		var e event
		if json.Unmarshal(line, &e) == nil {
			fn(e)
		}
	}
	return sc.Err()
}

func readSuite(name, file string, flaky map[string]bool) (Suite, error) {
	s := Suite{Name: name}
	type key struct{ pkg, test string }
	tests := map[key]*Test{}
	out := map[key]*strings.Builder{}
	pkgs := map[string]*Package{}
	var order []string
	err := readEvents(file, func(e event) {
		if e.Package == "" {
			return
		}
		p := pkgs[e.Package]
		if p == nil {
			p = &Package{Name: shortPkg(e.Package)}
			pkgs[e.Package] = p
			order = append(order, e.Package)
		}
		k := key{e.Package, e.Test}
		if e.Action == "output" {
			b := out[k]
			if b == nil {
				b = &strings.Builder{}
				out[k] = b
			}
			if b.Len() < maxOutput {
				b.WriteString(e.Output)
			}
			return
		}
		switch e.Action {
		case "pass", "fail", "skip":
		default:
			return
		}
		if e.Test == "" {
			p.Result, p.Seconds = e.Action, e.Elapsed
			return
		}
		tests[k] = &Test{Package: p.Name, Name: e.Test, Result: e.Action, Seconds: e.Elapsed}
	})
	if err != nil {
		return s, err
	}
	// Which packages had a test fail, before any is found to be flaky: a package that failed
	// with none failed did not build, panicked or timed out, and no rerun excuses that.
	testFailed := map[string]bool{}
	for k, t := range tests {
		if t.Result == "fail" {
			testFailed[k.pkg] = true
		}
	}
	for k, t := range tests {
		p := pkgs[k.pkg]
		top, _, _ := strings.Cut(t.Name, "/")
		if t.Result == "fail" && (flaky[k.pkg+" "+t.Name] || flaky[k.pkg+" "+top]) {
			t.Result = "flaky"
		}
		if t.Result == "fail" || t.Result == "flaky" {
			if b := out[k]; b != nil {
				t.Output = b.String()
			}
		}
		p.Tests = append(p.Tests, *t)
		switch t.Result {
		case "pass":
			p.Passed++
		case "fail":
			p.Failed++
		case "skip":
			p.Skipped++
		}
	}
	for _, name := range order {
		p := pkgs[name]
		slices.SortFunc(p.Tests, func(a, b Test) int { return strings.Compare(a.Name, b.Name) })
		// A package that failed with no failing test did not build, panicked or timed out.
		if p.Result == "fail" && !testFailed[name] {
			s.BuildFailed++
			if b := out[key{name, ""}]; b != nil {
				p.Output = b.String()
			}
		}
		if p.Result == "" && len(p.Tests) == 0 {
			continue // A package with no test files.
		}
		s.Packages = append(s.Packages, *p)
		s.Seconds += p.Seconds
		for _, t := range p.Tests {
			if strings.Contains(t.Name, "/") {
				continue // Subtests are counted through their parents.
			}
			switch t.Result {
			case "pass":
				s.Passed++
			case "fail":
				s.Failed++
			case "flaky":
				s.Flaky++
			case "skip":
				s.Skipped++
			}
			s.Slowest = append(s.Slowest, t)
		}
		for _, t := range p.Tests {
			if t.Result == "fail" || t.Result == "flaky" {
				s.Failures = append(s.Failures, t)
			}
		}
	}
	slices.SortFunc(s.Packages, func(a, b Package) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(s.Slowest, func(a, b Test) int { return cmpFloat(b.Seconds, a.Seconds) })
	if len(s.Slowest) > 25 {
		s.Slowest = s.Slowest[:25]
	}
	return s, nil
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// passedTests lists "package test" for every test that passed in a run.
func passedTests(file string) (map[string]bool, error) {
	out := map[string]bool{}
	err := readEvents(file, func(e event) {
		if e.Test != "" && e.Action == "pass" {
			out[e.Package+" "+e.Test] = true
		}
	})
	return out, err
}

// printFailed prints each package with the top-level tests that failed in it, as a -run
// pattern, so that just those can be run again.
func printFailed(files []string) error {
	byPkg := map[string][]string{}
	var order []string
	for _, f := range files {
		err := readEvents(f, func(e event) {
			if e.Test == "" || e.Action != "fail" {
				return
			}
			top, _, _ := strings.Cut(e.Test, "/")
			if _, ok := byPkg[e.Package]; !ok {
				order = append(order, e.Package)
			}
			if !slices.Contains(byPkg[e.Package], top) {
				byPkg[e.Package] = append(byPkg[e.Package], top)
			}
		})
		if err != nil {
			return err
		}
	}
	for _, p := range order {
		fmt.Printf("%s ^(%s)$\n", p, strings.Join(byPkg[p], "|"))
	}
	return nil
}

// ---- coverage

// Coverage is statement coverage by package and file.
type Coverage struct {
	Total    float64      `json:"total"`
	Packages []CoverGroup `json:"packages"`
	HTML     string       `json:"html,omitempty"`
}

// CoverGroup is coverage of a package or a file.
type CoverGroup struct {
	Name    string       `json:"name"`
	Stmts   int          `json:"statements"`
	Covered int          `json:"covered"`
	Percent float64      `json:"percent"`
	Files   []CoverGroup `json:"files,omitempty"`
}

func readCoverage(file string) (*Coverage, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	type block struct{ stmts, count int }
	// The same block can appear once per test binary; it is covered if any run reached it.
	blocks := map[string]block{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "mode:") || line == "" {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		j := strings.LastIndexByte(line[:max(i, 0)], ' ')
		if i < 0 || j < 0 {
			continue
		}
		stmts, err1 := strconv.Atoi(line[j+1 : i])
		count, err2 := strconv.Atoi(line[i+1:])
		if err1 != nil || err2 != nil {
			continue
		}
		b := blocks[line[:j]]
		b.stmts = stmts
		b.count += count
		blocks[line[:j]] = b
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	files := map[string]*CoverGroup{}
	for k, b := range blocks {
		name, _, _ := strings.Cut(k, ":")
		g := files[name]
		if g == nil {
			g = &CoverGroup{Name: shortPkg(name)}
			files[name] = g
		}
		g.Stmts += b.stmts
		if b.count > 0 {
			g.Covered += b.stmts
		}
	}
	pkgs := map[string]*CoverGroup{}
	c := &Coverage{}
	var stmts, covered int
	for _, g := range files {
		dir := filepath.Dir(g.Name)
		p := pkgs[dir]
		if p == nil {
			p = &CoverGroup{Name: dir}
			pkgs[dir] = p
		}
		g.Percent = pct(g.Covered, g.Stmts)
		g.Name = filepath.Base(g.Name)
		p.Files = append(p.Files, *g)
		p.Stmts += g.Stmts
		p.Covered += g.Covered
		stmts += g.Stmts
		covered += g.Covered
	}
	for _, p := range pkgs {
		p.Percent = pct(p.Covered, p.Stmts)
		slices.SortFunc(p.Files, func(a, b CoverGroup) int { return cmpFloat(a.Percent, b.Percent) })
		c.Packages = append(c.Packages, *p)
	}
	slices.SortFunc(c.Packages, func(a, b CoverGroup) int { return strings.Compare(a.Name, b.Name) })
	c.Total = pct(covered, stmts)
	return c, nil
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return math.Round(1000*float64(a)/float64(b)) / 10
}

// ---- benchmarks

// Perf compares benchmarks between base and head.
type Perf struct {
	Rows   []BenchRow `json:"rows"`
	Base   bool       `json:"base"`
	Charts []Chart    `json:"-"`
}

// BenchRow is one benchmark and unit.
type BenchRow struct {
	Name     string   `json:"name"`
	Unit     string   `json:"unit"`
	Base     *Sample  `json:"base,omitempty"`
	Head     Sample   `json:"head"`
	Delta    float64  `json:"delta_percent"`
	P        float64  `json:"p"`
	Verdict  string   `json:"verdict"` // faster, slower, same, new
	Budget   float64  `json:"budget,omitempty"`
	Problems []string `json:"problems,omitempty"`
}

// Sample summarizes the runs of one benchmark.
type Sample struct {
	Values []float64 `json:"values"`
	Median float64   `json:"median"`
	Spread float64   `json:"spread_percent"` // largest distance from the median
}

func summarizeSample(v []float64) Sample {
	s := slices.Clone(v)
	slices.Sort(s)
	m := median(s)
	spread := 0.0
	if m != 0 {
		for _, x := range s {
			spread = math.Max(spread, math.Abs(x-m)/m*100)
		}
	}
	return Sample{Values: v, Median: m, Spread: math.Round(spread*10) / 10}
}

func median(s []float64) float64 {
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// readBench reads go test -bench output: name and unit to the values of each run.
func readBench(file string) (map[string]map[string][]float64, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]map[string][]float64{}
	pkg := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if p, ok := strings.CutPrefix(line, "pkg: "); ok {
			pkg = shortPkg(strings.TrimSpace(p))
			continue
		}
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) < 4 {
			continue
		}
		name := fs[0]
		if i := strings.LastIndexByte(name, '-'); i > 0 {
			if _, err := strconv.Atoi(name[i+1:]); err == nil {
				name = name[:i]
			}
		}
		name = pkg + "." + strings.TrimPrefix(name, "Benchmark")
		for i := 2; i+1 < len(fs); i += 2 {
			v, err := strconv.ParseFloat(fs[i], 64)
			if err != nil {
				continue
			}
			if out[name] == nil {
				out[name] = map[string][]float64{}
			}
			out[name][fs[i+1]] = append(out[name][fs[i+1]], v)
		}
	}
	return out, sc.Err()
}

type budget struct {
	unit string
	max  float64
}

func readBudgets(file string) (map[string][]budget, error) {
	out := map[string][]budget{}
	if file == "" {
		return out, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	for n, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) != 3 {
			return nil, fmt.Errorf("%s:%d: want NAME UNIT MAX", file, n+1)
		}
		v, err := strconv.ParseFloat(fs[2], 64)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", file, n+1, err)
		}
		out[fs[0]] = append(out[fs[0]], budget{fs[1], v})
	}
	return out, nil
}

var units = []string{"ns/op", "B/op", "allocs/op"}

func readPerf(baseFile, headFile, budgetFile string, maxSlow, maxAllocs float64) (*Perf, error) {
	head, err := readBench(headFile)
	if err != nil {
		return nil, err
	}
	base := map[string]map[string][]float64{}
	if baseFile != "" {
		if base, err = readBench(baseFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	budgets, err := readBudgets(budgetFile)
	if err != nil {
		return nil, err
	}
	p := &Perf{Base: len(base) > 0}
	var names []string
	for n := range head {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		var us []string
		for u := range head[n] {
			us = append(us, u)
		}
		slices.SortFunc(us, func(a, b string) int {
			ia, ib := slices.Index(units, a), slices.Index(units, b)
			if ia < 0 {
				ia = 99
			}
			if ib < 0 {
				ib = 99
			}
			if ia != ib {
				return ia - ib
			}
			return strings.Compare(a, b)
		})
		for _, u := range us {
			row := BenchRow{Name: n, Unit: u, Head: summarizeSample(head[n][u]), Verdict: "new", P: 1}
			if bv := base[n][u]; len(bv) > 0 {
				bs := summarizeSample(bv)
				row.Base = &bs
				if bs.Median != 0 {
					row.Delta = math.Round((row.Head.Median-bs.Median)/bs.Median*1000) / 10
				}
				row.P = mannWhitney(bv, head[n][u])
				row.Verdict = "same"
				if bs.Spread == 0 && row.Head.Spread == 0 {
					// Every run gave the same number on both sides, as allocation counts do,
					// so any difference is real and needs no test.
					row.P = map[bool]float64{true: 1, false: 0}[row.Delta == 0]
				}
				if row.P < 0.05 && row.Delta != 0 {
					row.Verdict = map[bool]string{true: "slower", false: "faster"}[row.Delta > 0]
				}
				switch {
				case u == "ns/op" && row.Verdict == "slower" && row.Delta > maxSlow:
					row.Problems = append(row.Problems, fmt.Sprintf("%.1f%% slower than base (p=%.3f, limit %.0f%%)", row.Delta, row.P, maxSlow))
				case u == "allocs/op" && row.Delta > maxAllocs && row.Head.Median-bs.Median >= 1:
					// Allocation counts barely vary, so this needs no significance test.
					row.Problems = append(row.Problems, fmt.Sprintf("%.0f allocations, up %.1f%% from %.0f (limit %.0f%%)", row.Head.Median, row.Delta, bs.Median, maxAllocs))
				}
			}
			for _, b := range budgets[n] {
				if b.unit == u {
					row.Budget = b.max
					if row.Head.Median > b.max {
						row.Problems = append(row.Problems, fmt.Sprintf("median %s %s is over the budget of %s", num(row.Head.Median), u, num(b.max)))
					}
				}
			}
			p.Rows = append(p.Rows, row)
		}
	}
	for n := range budgets {
		if head[n] == nil {
			p.Rows = append(p.Rows, BenchRow{Name: n, Verdict: "missing", Problems: []string{"has a budget but did not run"}})
		}
	}
	return p, nil
}

// mannWhitney returns the exact two-sided p-value of the Mann-Whitney U test, the test
// benchstat uses: it asks whether one sample tends to be larger, without assuming the
// timings are normally distributed, which they are not.
func mannWhitney(a, b []float64) float64 {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return 1
	}
	// U counts pairs where a beats b, with ties as halves; doubled to stay in integers.
	u2 := 0
	for _, x := range a {
		for _, y := range b {
			switch {
			case x > y:
				u2 += 2
			case x == y:
				u2++
			}
		}
	}
	if n+m > 40 {
		// Normal approximation for large samples.
		mu := float64(n*m) / 2
		sigma := math.Sqrt(float64(n*m*(n+m+1)) / 12)
		if sigma == 0 {
			return 1
		}
		z := math.Abs(float64(u2)/2-mu) / sigma
		return math.Erfc(z / math.Sqrt2)
	}
	// Exact distribution of U without ties: ways[i][j][u] by recurrence, counted in halves
	// by ignoring ties, which only makes the test a little more cautious.
	maxU := n * m
	prev := make([][]float64, m+1)
	for j := range prev {
		prev[j] = make([]float64, maxU+1)
		prev[j][0] = 1
	}
	for i := 1; i <= n; i++ {
		cur := make([][]float64, m+1)
		cur[0] = make([]float64, maxU+1)
		cur[0][0] = 1
		for j := 1; j <= m; j++ {
			cur[j] = make([]float64, maxU+1)
			for u := 0; u <= maxU; u++ {
				// The largest value is from a (adds j to U) or from b (adds nothing).
				if u-j >= 0 {
					cur[j][u] += prev[j][u-j]
				}
				cur[j][u] += cur[j-1][u]
			}
		}
		prev = cur
	}
	dist := prev[m]
	total := 0.0
	for _, w := range dist {
		total += w
	}
	u := float64(u2) / 2
	lo := math.Min(u, float64(maxU)-u)
	tail := 0.0
	for k := 0; float64(k) <= lo; k++ {
		tail += dist[k]
	}
	return math.Min(1, 2*tail/total)
}

func num(v float64) string {
	switch {
	case v >= 100:
		return strconv.FormatFloat(math.Round(v), 'f', -1, 64)
	case v >= 1:
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	return strconv.FormatFloat(v, 'g', 3, 64)
}

// ---- history

// Point is one recorded run.
type Point struct {
	SHA     string                        `json:"sha"`
	At      time.Time                     `json:"at"`
	Medians map[string]map[string]float64 `json:"medians"`
}

const keepHistory = 200

func readHistory(file string) ([]Point, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var h []Point
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return h, nil
}

func appendHistory(h []Point, sha string, at time.Time, p *Perf) []Point {
	pt := Point{SHA: sha, At: at, Medians: map[string]map[string]float64{}}
	for _, r := range p.Rows {
		if r.Verdict == "missing" {
			continue
		}
		if pt.Medians[r.Name] == nil {
			pt.Medians[r.Name] = map[string]float64{}
		}
		pt.Medians[r.Name][r.Unit] = r.Head.Median
	}
	h = append(h, pt)
	if len(h) > keepHistory {
		h = h[len(h)-keepHistory:]
	}
	return h
}

// Chart is an SVG line of one benchmark's time across recorded runs.
type Chart struct {
	Name   string
	Path   string
	Dots   []Dot
	Max    string
	Min    string
	Latest string
}

// Dot is a point on a chart, with what its tooltip says.
type Dot struct {
	X, Y  float64
	Label string
}

func charts(h []Point) []Chart {
	names := map[string]bool{}
	for _, p := range h {
		for n := range p.Medians {
			names[n] = true
		}
	}
	var out []Chart
	for _, n := range slices.Sorted(func(yield func(string) bool) {
		for k := range names {
			if !yield(k) {
				return
			}
		}
	}) {
		var xs []Point
		for _, p := range h {
			if _, ok := p.Medians[n]["ns/op"]; ok {
				xs = append(xs, p)
			}
		}
		if len(xs) < 2 {
			continue
		}
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, p := range xs {
			v := p.Medians[n]["ns/op"]
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
		lo = 0 // Time charts start at zero, so a small wobble does not look like a cliff.
		if hi == lo {
			hi = lo + 1
		}
		const w, ht, pad = 300.0, 80.0, 4.0
		c := Chart{Name: n, Max: num(hi), Min: num(lo), Latest: num(xs[len(xs)-1].Medians[n]["ns/op"])}
		var b strings.Builder
		for i, p := range xs {
			x := pad + (w-2*pad)*float64(i)/float64(len(xs)-1)
			y := ht - pad - (ht-2*pad)*(p.Medians[n]["ns/op"]-lo)/(hi-lo)
			if i == 0 {
				fmt.Fprintf(&b, "M%.1f %.1f", x, y)
			} else {
				fmt.Fprintf(&b, " L%.1f %.1f", x, y)
			}
			sha := p.SHA
			if len(sha) > 7 {
				sha = sha[:7]
			}
			c.Dots = append(c.Dots, Dot{x, y, fmt.Sprintf("%s · %s · %s ns/op", sha, p.At.Format("2006-01-02"), num(p.Medians[n]["ns/op"]))})
		}
		c.Path = b.String()
		out = append(out, c)
	}
	return out
}

// ---- output

func writeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func markdown(r Report) string {
	var b strings.Builder
	status := "✅ all checks passed"
	if !r.OK {
		status = "❌ " + strconv.Itoa(len(r.Problems)) + " problems"
	}
	fmt.Fprintf(&b, "## %s: %s\n\n", r.Title, status)
	for _, p := range r.Problems {
		fmt.Fprintf(&b, "- %s\n", p)
	}
	if len(r.Problems) > 0 {
		b.WriteString("\n")
	}
	if len(r.Suites) > 0 {
		b.WriteString("| Tests | Passed | Failed | Flaky | Skipped | Time |\n|---|---:|---:|---:|---:|---:|\n")
		for _, s := range r.Suites {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %.0fs |\n", s.Name, s.Passed, s.Failed, s.Flaky, s.Skipped, s.Seconds)
		}
		b.WriteString("\n")
		for _, s := range r.Suites {
			for _, t := range s.Failures {
				fmt.Fprintf(&b, "<details><summary>%s %s · %s · %s</summary>\n\n```\n%s\n```\n</details>\n\n", map[string]string{"fail": "❌", "flaky": "⚠️ flaky"}[t.Result], t.Name, t.Package, s.Name, tail(t.Output, 4000))
			}
		}
	}
	if r.Coverage != nil {
		fmt.Fprintf(&b, "**Coverage:** %.1f%% of statements, unit and end-to-end tests together.\n\n", r.Coverage.Total)
	}
	if r.Perf != nil {
		b.WriteString("| Benchmark | Unit | Base | Head | Change | p | Budget |\n|---|---|---:|---:|---:|---:|---:|\n")
		for _, row := range r.Perf.Rows {
			if row.Verdict == "missing" {
				fmt.Fprintf(&b, "| %s | | | | missing | | |\n", row.Name)
				continue
			}
			base, change, p := "", row.Verdict, ""
			if row.Base != nil {
				base = num(row.Base.Median)
				p = fmt.Sprintf("%.3f", row.P)
				change = fmt.Sprintf("%+.1f%%", row.Delta)
				if row.Verdict == "same" {
					change = "~"
				}
			}
			budget := ""
			if row.Budget > 0 {
				budget = num(row.Budget)
			}
			mark := ""
			if len(row.Problems) > 0 {
				mark = " ❌"
			} else if row.Verdict == "faster" {
				mark = " 🟢"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s ±%.0f%% | %s%s | %s | %s |\n", row.Name, row.Unit, base, num(row.Head.Median), row.Head.Spread, change, mark, p, budget)
		}
		b.WriteString("\nBase and head ran interleaved on the same runner; a change counts only at p < 0.05 (Mann-Whitney U), which needs at least 4 runs of each.\n")
	}
	return b.String()
}

func tail(s string, n int) string {
	if len(s) <= n {
		return strings.TrimRight(s, "\n")
	}
	return "…" + strings.TrimRight(s[len(s)-n:], "\n")
}

func writeHTML(p string, r Report) error {
	t, err := template.New("report").Funcs(template.FuncMap{
		"num":   num,
		"short": func(s string) string { return s[:min(len(s), 12)] },
		"pctw":  func(v float64) string { return strconv.FormatFloat(math.Max(0, math.Min(100, v)), 'f', 1, 64) },
		"band": func(v float64) string {
			switch {
			case v >= 80:
				return "good"
			case v >= 60:
				return "warn"
			}
			return "bad"
		},
		"secs": func(v float64) string {
			if v < 1 {
				return strconv.FormatFloat(v*1000, 'f', 0, 64) + "ms"
			}
			return strconv.FormatFloat(v, 'f', 1, 64) + "s"
		},
		"signed": func(v float64) string { return fmt.Sprintf("%+.1f%%", v) },
		"p3":     func(v float64) string { return fmt.Sprintf("%.3f", v) },
		"vals": func(v []float64) string {
			s := make([]string, len(v))
			for i, x := range v {
				s[i] = num(x)
			}
			return strings.Join(s, ", ")
		},
	}).Parse(page)
	if err != nil {
		return err
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	if err := t.Execute(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
