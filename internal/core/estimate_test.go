package core

import (
	"testing"
	"time"
)

func done(id string, tier int, tokens int64, title string, labels map[string]string, tags ...string) Task {
	return Task{ID: id, Title: title, Tier: tier, State: Done, Tokens: tokens, Labels: labels, Tags: tags, UpdatedAt: time.Unix(1, 0)}
}

func TestEstimateUsesSimilarTasksFirst(t *testing.T) {
	web := map[string]string{"team": "web"}
	history := []Task{
		done("w1", 1, 10_000, "Add the login form validation", web, "frontend"),
		done("w2", 1, 12_000, "Add the signup form validation", web, "frontend"),
		done("w3", 1, 11_000, "Fix the form validation messages", web, "frontend"),
		done("db1", 3, 300_000, "Migrate the invoices table", map[string]string{"team": "data"}, "db"),
		done("db2", 3, 280_000, "Migrate the ledger table", map[string]string{"team": "data"}, "db"),
		{ID: "open", Tier: 1, State: Ready, Tokens: 999_999}, // Not finished: ignored.
	}
	e := EstimateTokens(Task{ID: "new", Tier: 1, Title: "Add the password form validation", Labels: web, Tags: []string{"frontend"}}, history)
	if e.Method != "similar" || e.Basis != 3 || e.Tokens < 10_000 || e.Tokens > 12_000 || e.Low > e.Tokens || e.High < e.Tokens {
		t.Errorf("similar: %+v", e)
	}
	if len(e.Similar) == 0 || e.Similar[0][0] != 'w' {
		t.Errorf("similar tasks: %v", e.Similar)
	}
	db := EstimateTokens(Task{ID: "m", Tier: 3, Title: "Migrate the payments table", Labels: map[string]string{"team": "data"}, Tags: []string{"db"}}, history)
	if db.Tokens < 200_000 {
		t.Errorf("a migration should cost like migrations: %+v", db)
	}
}

func TestEstimateFallsBackAndScales(t *testing.T) {
	history := []Task{done("a", 2, 50_000, "alpha", nil), done("b", 2, 70_000, "beta", nil), done("c", 1, 5_000, "gamma", nil)}
	e := EstimateTokens(Task{ID: "x", Tier: 2, Title: "something unrelated"}, history)
	if e.Method != "tier" || e.Basis != 2 {
		t.Errorf("tier fallback: %+v", e)
	}
	if e := EstimateTokens(Task{ID: "x", Tier: 3, Title: "zzz"}, history); e.Method != "all" || e.Basis != 3 {
		t.Errorf("all fallback: %+v", e)
	}
	if e := EstimateTokens(Task{ID: "x", Tier: 1}, nil); e.Method != "none" || e.Tokens != 0 {
		t.Errorf("no history: %+v", e)
	}
	// A much longer spec costs more, but less than in proportion, and never past double.
	short := EstimateTokens(Task{ID: "s", Tier: 2, Title: "tiny"}, history)
	long := EstimateTokens(Task{ID: "l", Tier: 2, Title: "tiny", Spec: string(make([]byte, 5000))}, history)
	if long.Tokens <= short.Tokens || float64(long.Tokens) > 2*float64(short.Tokens)*1.5 {
		t.Errorf("scaling: short %d long %d", short.Tokens, long.Tokens)
	}
	// The same inputs always give the same answer.
	if a, b := EstimateTokens(Task{ID: "x", Tier: 2}, history), EstimateTokens(Task{ID: "x", Tier: 2}, history); a.Tokens != b.Tokens || a.Basis != b.Basis {
		t.Error("not deterministic")
	}
}
