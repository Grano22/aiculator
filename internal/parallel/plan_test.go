package parallel

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func waves(p *Plan) [][]string {
	out := make([][]string, len(p.Waves))
	for i, w := range p.Waves {
		out[i] = w.Jobs
	}
	return out
}

func sched(t *testing.T, g Granularity, jobs ...Job) *Plan {
	t.Helper()
	p, err := Schedule(Input{Jobs: jobs, Granularity: g, Max: 8})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOverlapRules(t *testing.T) {
	for _, c := range []struct {
		a, b string
		g    Granularity
		want bool
	}{
		{"src/a.go", "src/b.go", File, false},
		{"src/a.go", "src/a.go", File, true},
		{"src/", "src/a.go", File, true},
		{"src2/a.go", "src/a.go", File, false},
		{"src/a.go", "src/b.go", Dir, true},
		{"src/a.go", "lib/b.go", Dir, false},
		{"src/a.go", "lib/b.go", Repo, true},
		{"a.go#Foo", "a.go#Bar", Symbol, false},
		{"a.go#Foo", "a.go#Foo", Symbol, true},
		{"a.go#Foo", "a.go#Bar", File, true},
		{"a.go:1-10", "a.go:20-30", Symbol, false},
		{"a.go:1-10", "a.go:10-30", Symbol, true},
		{"a.go:1-10", "a.go#Foo", Symbol, true}, // Could be the same place.
		{"a.go", "a.go#Foo", Symbol, true},
		{"A.GO", "a.go", File, true}, // One file on macOS and Windows.
		{"src/*.go", "src/a.go", File, true},
		{"**/*.go", "x/a.go", File, true},
		{"./src/../a.go", "a.go", File, true},
		{`src\a.go`, "src/a.go", File, true},
	} {
		x, err1 := parseClaim(c.a)
		y, err2 := parseClaim(c.b)
		if err1 != nil || err2 != nil {
			t.Fatalf("%v %v", err1, err2)
		}
		if got := overlap(x.at(c.g), y.at(c.g)); got != c.want {
			t.Errorf("%s and %s at %s: got %v", c.a, c.b, c.g, got)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", "a.go#", "a.go:9-1", "a.go:0-3"} {
		if _, err := parseClaim(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestDisjointFilesRunTogether(t *testing.T) {
	p := sched(t, File, Job{ID: "a", Touches: []string{"a.go"}}, Job{ID: "b", Touches: []string{"b.go"}}, Job{ID: "c", Touches: []string{"c.go"}})
	if p.Verdict != "parallel" || len(p.Waves) != 1 || !p.Optimal || p.Method != "exact" {
		t.Fatalf("%+v", p)
	}
}

func TestSameFileIsSplit(t *testing.T) {
	p := sched(t, File, Job{ID: "a", Touches: []string{"x.go"}}, Job{ID: "b", Touches: []string{"x.go"}})
	if p.Verdict != "sequential" || len(p.Waves) != 2 || len(p.Conflicts) != 1 || !strings.Contains(p.Conflicts[0].Why, "x.go") {
		t.Fatalf("%+v", p)
	}
}

func TestFinerGranularityUnlocksMore(t *testing.T) {
	a, b := Job{ID: "a", Touches: []string{"x.go#Foo"}}, Job{ID: "b", Touches: []string{"x.go#Bar"}}
	if p := sched(t, File, a, b); len(p.Waves) != 2 {
		t.Errorf("file level: %+v", waves(p))
	}
	if p := sched(t, Symbol, a, b); len(p.Waves) != 1 {
		t.Errorf("symbol level: %+v", waves(p))
	}
}

func TestAutoChoosesTheCoarsestThatLosesNothing(t *testing.T) {
	// Different directories: directory level is enough, so it needs no finer promise.
	p := sched(t, Auto, Job{ID: "a", Touches: []string{"x/a.go"}}, Job{ID: "b", Touches: []string{"y/b.go"}})
	if p.Granularity != Dir || len(p.Waves) != 1 {
		t.Errorf("%+v", p)
	}
	// One directory, two files: file level is what it takes.
	p = sched(t, Auto, Job{ID: "a", Touches: []string{"x/a.go"}}, Job{ID: "b", Touches: []string{"x/b.go"}})
	if p.Granularity != File || len(p.Waves) != 1 {
		t.Errorf("%+v", p)
	}
}

// A real model, told to split one file, wrote same.txt#line1 and same.txt#line2. Auto believed
// it, and the two subagents collided. A symbol is only a name, so it must not be enough.
func TestAutoNeverPutsTwoJobsInOneFile(t *testing.T) {
	p := sched(t, Auto, Job{ID: "a", Touches: []string{"same.txt#line1"}}, Job{ID: "b", Touches: []string{"same.txt#line2"}})
	if len(p.Waves) != 2 || p.Verdict != "sequential" {
		t.Fatalf("%+v", p)
	}
	p = sched(t, Auto, Job{ID: "a", Touches: []string{"same.txt:1-1"}}, Job{ID: "b", Touches: []string{"same.txt:2-2"}})
	if len(p.Waves) != 2 {
		t.Fatalf("%+v", p)
	}
	// It can still be asked for by name.
	p = sched(t, Symbol, Job{ID: "a", Touches: []string{"same.txt#line1"}}, Job{ID: "b", Touches: []string{"same.txt#line2"}})
	if len(p.Waves) != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestCheckClaimsCatchesPartsThatDoNotExist(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", "func Charge() {}\nfunc Refund() {}\n")
	ok := []Job{{ID: "a", Touches: []string{"x.go#Charge"}}, {ID: "b", Touches: []string{"x.go:1-2", "new.go#Later", "whole.go"}}}
	if err := CheckClaims(dir, ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"x.go#line1", "x.go:1-99"} {
		if err := CheckClaims(dir, []Job{{ID: "a", Touches: []string{bad}}}); err == nil {
			t.Errorf("%s was accepted", bad)
		}
		if err := CheckClaims(dir, []Job{{ID: "a", Touches: []string{"y.go"}, Reads: []string{bad}}}); err == nil {
			t.Errorf("reading %s was accepted", bad)
		}
	}
}

func TestAJobThatClaimsNothingRunsAlone(t *testing.T) {
	p := sched(t, File, Job{ID: "a", Touches: []string{"a.go"}}, Job{ID: "vague"}, Job{ID: "b", Touches: []string{"b.go"}})
	if len(p.Waves) != 2 || !slices.Contains(p.Alone, "vague") {
		t.Fatalf("%+v", p)
	}
	for _, w := range p.Waves {
		if slices.Contains(w.Jobs, "vague") && len(w.Jobs) != 1 {
			t.Errorf("it shared a wave: %v", w.Jobs)
		}
	}
	p = sched(t, File, Job{ID: "a", Touches: []string{"a.go"}}, Job{ID: "fmt", Touches: []string{"f.go"}, Exclusive: true})
	if len(p.Waves) != 2 {
		t.Errorf("exclusive: %+v", waves(p))
	}
}

func TestReadsConflictWithWritesButNotReads(t *testing.T) {
	p := sched(t, File, Job{ID: "w", Touches: []string{"api.go"}}, Job{ID: "r", Touches: []string{"x.go"}, Reads: []string{"api.go"}})
	if len(p.Waves) != 2 || !strings.Contains(p.Conflicts[0].Why, "reads") {
		t.Errorf("%+v", p)
	}
	p = sched(t, File, Job{ID: "a", Touches: []string{"a.go"}, Reads: []string{"api.go"}}, Job{ID: "b", Touches: []string{"b.go"}, Reads: []string{"api.go"}})
	if len(p.Waves) != 1 {
		t.Errorf("two readers: %+v", waves(p))
	}
}

func TestDependenciesComeFirst(t *testing.T) {
	p := sched(t, File, Job{ID: "c", Touches: []string{"c"}, Deps: []string{"b"}}, Job{ID: "b", Touches: []string{"b"}, Deps: []string{"a"}}, Job{ID: "a", Touches: []string{"a"}}, Job{ID: "z", Touches: []string{"z"}})
	if len(p.Waves) != 3 || !slices.Contains(p.Waves[0].Jobs, "a") || !slices.Contains(p.Waves[2].Jobs, "c") {
		t.Fatalf("%+v", waves(p))
	}
}

func TestBadInput(t *testing.T) {
	for name, in := range map[string]Input{
		"none":      {},
		"no id":     {Jobs: []Job{{Touches: []string{"a"}}}},
		"twice":     {Jobs: []Job{{ID: "a"}, {ID: "a"}}},
		"bad dep":   {Jobs: []Job{{ID: "a", Deps: []string{"x"}}}},
		"self":      {Jobs: []Job{{ID: "a", Deps: []string{"a"}}}},
		"cycle":     {Jobs: []Job{{ID: "a", Deps: []string{"b"}}, {ID: "b", Deps: []string{"a"}}}},
		"outside":   {Jobs: []Job{{ID: "a", Touches: []string{"../x"}}}},
		"bad level": {Jobs: []Job{{ID: "a"}}, Granularity: "line"},
	} {
		if _, err := Schedule(in); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestSameInputGivesTheSamePlan(t *testing.T) {
	jobs := []Job{{ID: "a", Touches: []string{"x"}}, {ID: "b", Touches: []string{"x"}}, {ID: "c", Touches: []string{"y"}}, {ID: "d", Touches: []string{"z"}, Deps: []string{"c"}}}
	first, _ := Schedule(Input{Jobs: jobs})
	for range 20 {
		p, _ := Schedule(Input{Jobs: jobs})
		if !slices.EqualFunc(p.Waves, first.Waves, func(a, b Wave) bool { return slices.Equal(a.Jobs, b.Jobs) }) {
			t.Fatalf("%+v and %+v", first.Waves, p.Waves)
		}
	}
}

func randomJobs(r *rand.Rand, n int) []Job {
	jobs := make([]Job, n)
	for i := range jobs {
		j := Job{ID: fmt.Sprintf("j%d", i)}
		switch r.IntN(12) {
		case 0:
		case 1:
			j.Touches, j.Exclusive = []string{"e"}, true
		default:
			for range 1 + r.IntN(2) {
				j.Touches = append(j.Touches, fmt.Sprintf("d%d/f%d.go", r.IntN(3), r.IntN(5)))
			}
			if r.IntN(3) == 0 {
				j.Reads = []string{fmt.Sprintf("d%d/f%d.go", r.IntN(3), r.IntN(5))}
			}
		}
		for d := range i {
			if r.IntN(7) == 0 {
				j.Deps = append(j.Deps, fmt.Sprintf("j%d", d))
			}
		}
		jobs[i] = j
	}
	return jobs
}

// The promise of this package. No two jobs in a wave conflict, every job runs exactly once,
// and nothing starts before what it depends on, whatever the input.
func checkPlan(t *testing.T, jobs []Job, p *Plan, g Granularity, limit int) {
	t.Helper()
	ps, err := parse(jobs)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]parsed{}
	for _, x := range ps {
		byID[x.job.ID] = x
	}
	done := map[string]bool{}
	for _, w := range p.Waves {
		if len(w.Jobs) > limit {
			t.Fatalf("wave of %d, over %d", len(w.Jobs), limit)
		}
		for i, a := range w.Jobs {
			if done[a] {
				t.Fatalf("%s ran twice", a)
			}
			for _, d := range byID[a].job.Deps {
				if !done[d] {
					t.Fatalf("%s started before %s", a, d)
				}
			}
			for _, b := range w.Jobs[i+1:] {
				if why := conflictBetween(byID[a], byID[b], g); why != "" {
					t.Fatalf("%s and %s share a wave: %s", a, b, why)
				}
			}
		}
		for _, a := range w.Jobs {
			done[a] = true
		}
	}
	if len(done) != len(jobs) {
		t.Fatalf("%d of %d jobs ran", len(done), len(jobs))
	}
}

func TestEveryPlanIsSafe(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 400 {
		jobs := randomJobs(r, 1+r.IntN(30))
		limit := 1 + r.IntN(6)
		for _, g := range []Granularity{Dir, File, Symbol} {
			p, err := Schedule(Input{Jobs: jobs, Granularity: g, Max: limit})
			if err != nil {
				t.Fatal(err)
			}
			checkPlan(t, jobs, p, g, limit)
		}
	}
}

// Checks the search against trying every possible assignment of jobs to waves.
func TestExactMatchesBruteForce(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range 150 {
		jobs := randomJobs(r, 1+r.IntN(7))
		limit := 1 + r.IntN(4)
		p, err := Schedule(Input{Jobs: jobs, Granularity: File, Max: limit})
		if err != nil {
			t.Fatal(err)
		}
		if want := bruteForce(t, jobs, limit); len(p.Waves) != want {
			t.Fatalf("exact found %d waves, but %d is possible: %+v (limit %d)", len(p.Waves), want, jobs, limit)
		}
	}
}

func bruteForce(t *testing.T, jobs []Job, limit int) int {
	t.Helper()
	ps, err := parse(jobs)
	if err != nil {
		t.Fatal(err)
	}
	g, _, err := build(ps, File)
	if err != nil {
		t.Fatal(err)
	}
	for k := 1; k <= g.n; k++ {
		assign := make([]int, g.n)
		var try func(i int) bool
		try = func(i int) bool {
			if i == g.n {
				for a := range g.n {
					size := 0
					for b := range g.n {
						if assign[a] == assign[b] {
							size++
							if a != b && g.conflict[a][b] {
								return false
							}
						}
					}
					if size > limit {
						return false
					}
					for _, d := range g.deps[a] {
						if assign[d] >= assign[a] {
							return false
						}
					}
				}
				return true
			}
			for w := range k {
				assign[i] = w
				if try(i + 1) {
					return true
				}
			}
			return false
		}
		if try(0) {
			return k
		}
	}
	return g.n
}

func TestGreedyIsCloseAndItsLowerBoundIsTrue(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	worse, total := 0, 0
	for range 200 {
		jobs := randomJobs(r, 2+r.IntN(11))
		limit := 1 + r.IntN(5)
		ps, _ := parse(jobs)
		g, _, err := build(ps, File)
		if err != nil {
			t.Fatal(err)
		}
		exact, greedy, low := len(g.exact(limit)), len(g.greedy(limit)), g.lowerBound(limit)
		if low > exact {
			t.Fatalf("the lower bound %d is above the true answer %d", low, exact)
		}
		if greedy < exact {
			t.Fatalf("greedy %d beat exact %d", greedy, exact)
		}
		if greedy > exact {
			worse++
		}
		total++
	}
	t.Logf("greedy used more waves than the best in %d of %d cases", worse, total)
}

func TestManyJobsAreSolvedQuickly(t *testing.T) {
	var jobs []Job
	for i := range 2000 {
		jobs = append(jobs, Job{ID: fmt.Sprintf("j%d", i), Touches: []string{fmt.Sprintf("d%d/f%d.go", i%40, i%7)}})
	}
	p, err := Schedule(Input{Jobs: jobs, Granularity: File, Max: 8})
	if err != nil {
		t.Fatal(err)
	}
	if p.Method != "greedy" {
		t.Errorf("%s", p.Method)
	}
	checkPlan(t, jobs, p, File, 8)
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("init", "-q")
	write(t, dir, "a.go", "a")
	write(t, dir, "src/b.go", "b")
	write(t, dir, ".gitignore", "ignored.txt\n")
	run("add", "-A")
	run("commit", "-q", "-m", "r")
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyFindsWorkOutsideTheClaims(t *testing.T) {
	dir := repo(t)
	plan := &Plan{Waves: []Wave{{N: 1, Jobs: []string{"j1", "j2", "lazy"}}}, Writes: map[string][]string{
		"j1": {"a.go"}, "j2": {"src/"}, "lazy": {"never.go"},
	}}
	before, err := Snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Verify(dir, plan, 1, before)
	if err != nil || !r.OK || len(r.Changed) != 0 {
		t.Fatalf("no changes: %+v %v", r, err)
	}
	write(t, dir, "a.go", "changed")
	write(t, dir, "src/new.go", "new, inside a claimed directory")
	if r, err = Verify(dir, plan, 1, before); err != nil || !r.OK || !slices.Equal(r.Idle, []string{"lazy"}) {
		t.Fatalf("claimed changes: %+v %v", r, err)
	}
	write(t, dir, "stray.go", "nobody claimed this")
	write(t, dir, "ignored.txt", "git does not track this")
	r, err = Verify(dir, plan, 1, before)
	if err != nil {
		t.Fatal(err)
	}
	if r.OK || !slices.Equal(r.Unclaimed, []string{"stray.go"}) {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.Note, "ignored") {
		t.Errorf("it should say what it cannot see: %s", r.Note)
	}
	if _, err := Verify(dir, plan, 9, before); err == nil {
		t.Error("a wave that does not exist was accepted")
	}
	if _, err := Verify(dir, plan, 1, "notatree"); err == nil {
		t.Error("a bad snapshot was accepted")
	}
}

// Taking a snapshot must leave the person's own work exactly as it was.
func TestSnapshotLeavesTheStagingAreaAlone(t *testing.T) {
	dir := repo(t)
	write(t, dir, "unstaged.go", "x")
	status := func() string {
		c := exec.Command("git", "status", "--porcelain")
		c.Dir = dir
		out, err := c.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	head := func() string {
		c := exec.Command("git", "rev-parse", "HEAD")
		c.Dir = dir
		out, _ := c.Output()
		return string(out)
	}
	s, h := status(), head()
	if _, err := Snapshot(dir); err != nil {
		t.Fatal(err)
	}
	if status() != s || head() != h {
		t.Errorf("before %q, after %q", s, status())
	}
}
