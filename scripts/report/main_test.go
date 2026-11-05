package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMannWhitneyMatchesBenchstat(t *testing.T) {
	a := []float64{1, 2, 3, 4, 5, 6}
	b := []float64{7, 8, 9, 10, 11, 12}
	// Completely separated samples of six: 2 of the 924 orderings are this extreme.
	if p := mannWhitney(a, b); math.Abs(p-2.0/924) > 1e-9 {
		t.Errorf("separated: p=%v", p)
	}
	if p := mannWhitney(a, a); p != 1 {
		t.Errorf("identical: p=%v", p)
	}
	if p := mannWhitney([]float64{1, 3, 5, 7, 9, 11}, []float64{2, 4, 6, 8, 10, 12}); p < 0.5 {
		t.Errorf("interleaved: p=%v", p)
	}
	if p := mannWhitney(nil, a); p != 1 {
		t.Errorf("empty: p=%v", p)
	}
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPerfVerdictsAndBudgets(t *testing.T) {
	var base, head strings.Builder
	base.WriteString("pkg: github.com/Grano22/aiculator/internal/engine\n")
	head.WriteString("pkg: github.com/Grano22/aiculator/internal/engine\n")
	for i := range 6 {
		f := float64(i)
		base.WriteString("BenchmarkSlow-16 100 " + num(1000+f) + " ns/op 10 B/op 5 allocs/op\n")
		head.WriteString("BenchmarkSlow-16 100 " + num(2000+f) + " ns/op 10 B/op 9 allocs/op\n")
		base.WriteString("BenchmarkFast-16 100 " + num(2000+f) + " ns/op\n")
		head.WriteString("BenchmarkFast-16 100 " + num(1000+f) + " ns/op\n")
		head.WriteString("BenchmarkNew-16 100 50 ns/op\n")
	}
	budgets := write(t, "budgets.txt", "# name unit max\ninternal/engine.Fast ns/op 900\ninternal/engine.Gone ns/op 1\n")
	p, err := readPerf(write(t, "base.txt", base.String()), write(t, "head.txt", head.String()), budgets, 25, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]BenchRow{}
	for _, r := range p.Rows {
		got[r.Name+" "+r.Unit] = r
	}
	if r := got["internal/engine.Slow ns/op"]; r.Verdict != "slower" || len(r.Problems) != 1 || r.Delta < 99 {
		t.Errorf("slow: %+v", r)
	}
	if r := got["internal/engine.Slow allocs/op"]; len(r.Problems) != 1 {
		t.Errorf("allocations: %+v", r)
	}
	if r := got["internal/engine.Slow B/op"]; r.Verdict != "same" || len(r.Problems) != 0 {
		t.Errorf("bytes: %+v", r)
	}
	if r := got["internal/engine.Fast ns/op"]; r.Verdict != "faster" || len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "budget") {
		t.Errorf("fast but over budget: %+v", r)
	}
	if r := got["internal/engine.New ns/op"]; r.Verdict != "new" || r.Base != nil {
		t.Errorf("new: %+v", r)
	}
	if r := got["internal/engine.Gone "]; r.Verdict != "missing" {
		t.Errorf("a budget for a benchmark that did not run: %+v", got)
	}
}

const testJSON = `{"Action":"run","Package":"github.com/Grano22/aiculator/internal/a","Test":"TestOK"}
{"Action":"pass","Package":"github.com/Grano22/aiculator/internal/a","Test":"TestOK","Elapsed":0.5}
{"Action":"output","Package":"github.com/Grano22/aiculator/internal/a","Test":"TestBad","Output":"boom\n"}
{"Action":"fail","Package":"github.com/Grano22/aiculator/internal/a","Test":"TestBad/sub","Elapsed":0.1}
{"Action":"fail","Package":"github.com/Grano22/aiculator/internal/a","Test":"TestBad","Elapsed":0.2}
{"Action":"output","Package":"github.com/Grano22/aiculator/internal/a","Test":"TestWobbly","Output":"timing\n"}
{"Action":"fail","Package":"github.com/Grano22/aiculator/internal/a","Test":"TestWobbly","Elapsed":3}
{"Action":"skip","Package":"github.com/Grano22/aiculator/internal/a","Test":"TestSkip","Elapsed":0}
{"Action":"fail","Package":"github.com/Grano22/aiculator/internal/a","Elapsed":4}
# github.com/Grano22/aiculator/internal/b
{"Action":"output","Package":"github.com/Grano22/aiculator/internal/b","Output":"build failed: x.go:1: syntax\n"}
{"Action":"fail","Package":"github.com/Grano22/aiculator/internal/b","Elapsed":0}
`

func TestSuiteCountsFlakyAndBuildFailures(t *testing.T) {
	main := write(t, "t.json", testJSON)
	rerun := write(t, "r.json", `{"Action":"pass","Package":"github.com/Grano22/aiculator/internal/a","Test":"TestWobbly","Elapsed":1}
{"Action":"fail","Package":"github.com/Grano22/aiculator/internal/a","Test":"TestBad","Elapsed":1}
`)
	flaky, err := passedTests(rerun)
	if err != nil {
		t.Fatal(err)
	}
	s, err := readSuite("unit", main, flaky)
	if err != nil {
		t.Fatal(err)
	}
	if s.Passed != 1 || s.Failed != 1 || s.Flaky != 1 || s.Skipped != 1 || s.BuildFailed != 1 {
		t.Errorf("counts: %+v", s)
	}
	if s.Slowest[0].Name != "TestWobbly" {
		t.Errorf("slowest: %+v", s.Slowest)
	}
	var outputs []string
	for _, f := range s.Failures {
		outputs = append(outputs, f.Name+":"+f.Output)
	}
	if !strings.Contains(strings.Join(outputs, "|"), "TestBad:boom") {
		t.Errorf("failure output: %v", outputs)
	}
	r := Report{Suites: []Suite{s}}
	r.summarize()
	if r.OK || len(r.Problems) != 2 {
		t.Errorf("problems: %v", r.Problems)
	}
}

func TestCoverageTakesAnyRunThatReachedABlock(t *testing.T) {
	p := write(t, "c.out", `mode: atomic
github.com/Grano22/aiculator/internal/a/x.go:1.1,2.1 3 0
github.com/Grano22/aiculator/internal/a/x.go:1.1,2.1 3 1
github.com/Grano22/aiculator/internal/a/x.go:3.1,4.1 1 0
github.com/Grano22/aiculator/internal/b/y.go:1.1,2.1 4 2
`)
	c, err := readCoverage(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 87.5 || len(c.Packages) != 2 || c.Packages[0].Name != "internal/a" || c.Packages[0].Percent != 75 {
		t.Errorf("%+v", c)
	}
}

func TestWholeReportRenders(t *testing.T) {
	dir := t.TempDir()
	r := Report{Title: "t", SHA: "0123456789abcdef"}
	s, _ := readSuite("unit", write(t, "t.json", testJSON), nil)
	r.Suites = []Suite{s}
	r.Coverage = &Coverage{Total: 50, Packages: []CoverGroup{{Name: "internal/a", Percent: 50, Files: []CoverGroup{{Name: "x.go"}}}}}
	r.Perf = &Perf{Rows: []BenchRow{{Name: "x", Unit: "ns/op", Head: summarizeSample([]float64{1, 2, 3})}}}
	h := appendHistory(appendHistory(nil, "a", r.Generated, r.Perf), "b", r.Generated, r.Perf)
	r.Perf.Charts = charts(h)
	r.summarize()
	if err := writeHTML(filepath.Join(dir, "index.html"), r); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "index.html"))
	for _, want := range []string{"TestBad", "internal/a", "<svg", "boom", "prefers-color-scheme"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the page has no %q", want)
		}
	}
	if md := markdown(r); !strings.Contains(md, "| unit | 1 | 2 | 0 | 1 |") {
		t.Errorf("markdown:\n%s", md)
	}
}
