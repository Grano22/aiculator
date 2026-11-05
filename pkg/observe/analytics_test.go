package observe

import (
	"testing"
	"time"
)

var a0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

type step struct {
	min     int
	state   string
	attempt int
	reason  string
}

func feed(x *FlowIndex, id string, tier int, labels map[string]string, steps ...step) {
	for _, s := range steps {
		x.Observe(Revision{ID: id, State: s.state, Tier: tier, Attempt: s.attempt, Reason: s.reason, Labels: labels, At: a0.Add(time.Duration(s.min) * time.Minute)})
	}
}

func linkCount(ls []Link, from, to string) int {
	for _, l := range ls {
		if l.From == from && l.To == to {
			return l.Count
		}
	}
	return 0
}

func sample() *FlowIndex {
	x := NewFlowIndex()
	crit := map[string]string{"severity": "critical"}
	// Straight through on the first try.
	feed(x, "a", 1, crit, step{0, "ready", 0, ""}, step{1, "claimed", 1, ""}, step{2, "running", 1, ""},
		step{2, "running", 1, ""}, // A heartbeat: no move.
		step{3, "review", 1, ""}, step{4, "merging", 1, ""}, step{5, "done", 1, ""})
	// Fails its checks, then makes it on the second try.
	feed(x, "b", 1, nil, step{0, "draft", 0, ""}, step{1, "ready", 0, ""}, step{2, "claimed", 1, ""}, step{3, "running", 1, ""},
		step{4, "review", 1, ""}, step{5, "failed", 1, "checks_failed"}, step{6, "ready", 1, "retry"},
		step{7, "claimed", 2, ""}, step{8, "running", 2, ""}, step{9, "review", 2, ""}, step{10, "merging", 2, ""}, step{11, "done", 2, ""})
	// Stalls, and is parked after its budget runs out.
	feed(x, "c", 2, nil, step{0, "ready", 0, ""}, step{1, "claimed", 1, ""}, step{2, "running", 1, ""},
		step{30, "failed", 1, "no_progress"}, step{31, "parked", 1, "budget_exhausted"})
	// Canceled while waiting, with a note a person wrote.
	feed(x, "d", 2, crit, step{0, "ready", 0, ""}, step{40, "canceled", 0, "not needed after all"})
	x.SetReady()
	return x
}

func TestStateFlowIsAPathWithExits(t *testing.T) {
	a := sample().Analyze(Query{}, a0.Add(time.Hour))
	if !a.Ready || a.Tasks != 4 {
		t.Fatalf("ready %v tasks %d", a.Ready, a.Tasks)
	}
	for _, c := range []struct {
		from, to string
		n        int
	}{
		{"ready", "claimed", 3}, {"claimed", "running", 3}, {"running", "review", 2}, {"review", "merging", 2},
		{"merging", "done", 2}, {"draft", "ready", 1}, {"running", "parked", 1}, {"ready", "canceled", 1},
		{"review", "failed", 0}, // b failed once but finished: the path shows where it ended up.
	} {
		if got := linkCount(a.States, c.from, c.to); got != c.n {
			t.Errorf("%s -> %s: %d, want %d", c.from, c.to, got, c.n)
		}
	}
}

func TestFailureFlowAndRetries(t *testing.T) {
	a := sample().Analyze(Query{}, a0.Add(time.Hour))
	for _, c := range []struct {
		from, to string
		n        int
	}{
		{"review", "failed", 1}, {"running", "failed", 1}, {"failed", "parked", 1},
		{"failed", "checks_failed", 1}, {"failed", "no_progress", 1}, {"parked", "budget_exhausted", 1},
	} {
		if got := linkCount(a.Reasons, c.from, c.to); got != c.n {
			t.Errorf("%s -> %s: %d, want %d", c.from, c.to, got, c.n)
		}
	}
	if len(a.Attempts) != 2 || a.Attempts[0] != (AttemptStep{1, 3, 1}) || a.Attempts[1] != (AttemptStep{2, 1, 1}) {
		t.Errorf("funnel %+v", a.Attempts)
	}
	if len(a.Retries) != 1 || a.Retries[0] != (Count{"checks_failed", 1}) {
		t.Errorf("retry causes %+v", a.Retries)
	}
}

func TestFilters(t *testing.T) {
	x := sample()
	now := a0.Add(time.Hour)
	if a := x.Analyze(Query{Tier: 2}, now); a.Tasks != 2 || linkCount(a.States, "merging", "done") != 0 {
		t.Errorf("tier 2: %+v", a)
	}
	if a := x.Analyze(Query{Label: "severity=critical"}, now); a.Tasks != 2 || linkCount(a.States, "ready", "canceled") != 1 {
		t.Errorf("label: %d", a.Tasks)
	}
	if a := x.Analyze(Query{Label: "severity"}, now); a.Tasks != 2 {
		t.Errorf("label key: %d", a.Tasks)
	}
	// The last 35 minutes: only c's fall and d's cancel happened then.
	a := x.Analyze(Query{Window: 35 * time.Minute}, now)
	if a.Tasks != 2 || linkCount(a.Reasons, "failed", "no_progress") != 1 || linkCount(a.Reasons, "failed", "checks_failed") != 0 {
		t.Errorf("window: tasks %d %+v", a.Tasks, a.Reasons)
	}
	if len(a.Tiers) != 2 || len(a.Labels) != 1 || a.Labels[0] != "severity=critical" {
		t.Errorf("choices %v %v", a.Tiers, a.Labels)
	}
}

func TestCumulativeFlow(t *testing.T) {
	a := sample().Analyze(Query{Window: time.Hour, Points: 61}, a0.Add(time.Hour))
	c := a.CFD
	if len(c.Times) != 61 || !c.Times[0].Equal(a0) {
		t.Fatalf("times %d from %v", len(c.Times), c.Times[0])
	}
	at := func(state string, minute int) int { return c.Series[state][minute] }
	if at("ready", 0) != 3 || at("draft", 0) != 1 {
		t.Errorf("at 0: ready %d draft %d", at("ready", 0), at("draft", 0))
	}
	if at("done", 20) != 2 || at("running", 20) != 1 || at("ready", 20) != 1 {
		t.Errorf("at 20: %d %d %d", at("done", 20), at("running", 20), at("ready", 20))
	}
	if at("done", 60) != 2 || at("parked", 60) != 1 || at("canceled", 60) != 1 {
		t.Errorf("at 60: %d %d %d", at("done", 60), at("parked", 60), at("canceled", 60))
	}
	for k := range c.Times {
		total := 0
		for _, s := range c.States {
			total += c.Series[s][k]
		}
		if total != 4 {
			t.Fatalf("point %d holds %d tasks, want 4", k, total)
		}
	}
}

func TestReasonCodes(t *testing.T) {
	for in, want := range map[string]string{
		"checks_failed": "checks_failed", "no_progress": "no_progress", "": "",
		"done without independent verification, so a person must decide": "done_without_verification",
		"unreadable record: unexpected end":                              "unreadable_record",
		"I could not log in <script>":                                    "agent_note",
		"remote_status: Done":                                            "remote_status",
	} {
		if got := ReasonCode(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func BenchmarkAnalyze(b *testing.B) {
	x := NewFlowIndex()
	for i := range 20000 {
		id := "t" + string(rune('a'+i%26)) + time.Duration(i).String()
		feed(x, id, 1+i%3, map[string]string{"team": string(rune('a' + i%5))},
			step{i % 600, "ready", 0, ""}, step{i%600 + 1, "claimed", 1, ""}, step{i%600 + 2, "running", 1, ""},
			step{i%600 + 3, "review", 1, ""}, step{i%600 + 4, "done", 1, ""})
	}
	b.ReportAllocs()
	for b.Loop() {
		x.Analyze(Query{Window: 24 * time.Hour}, a0.Add(12*time.Hour))
	}
}

func TestCumulativeFlowStartsWithTheHistory(t *testing.T) {
	a := sample().Analyze(Query{Window: 30 * 24 * time.Hour}, a0.Add(time.Hour))
	if !a.CFD.Times[0].Equal(a0) || !a.From.Equal(a0) {
		t.Errorf("starts at %v, want %v", a.CFD.Times[0], a0)
	}
}
