package driver

import (
	"testing"
	"time"
)

func TestLimitIn(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		text, reason string
		until        time.Time
	}{
		{"Claude AI usage limit reached|1793988000", "usage_limit", time.Unix(1793988000, 0)},
		{"Your credit balance is too low to access the API", "credit_exhausted", time.Time{}},
		{"Error 429: rate limit exceeded, retry after 90 seconds", "rate_limited", now.Add(90 * time.Second)},
		{"You exceeded your current quota (insufficient_quota). Try again in 2h 30m", "quota_exceeded", now.Add(150 * time.Minute)},
		{"usage limit; resets at 2026-11-06T15:00:00Z", "usage_limit", time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC)},
		{"overloaded", "rate_limited", now.Add(time.Minute)},
		{"5-hour limit reached, try again in 40 minutes", "usage_limit", now.Add(40 * time.Minute)},
		// A reset already past, or absurdly far, is not believed.
		{"usage limit reached|1000000000", "usage_limit", time.Time{}},
	} {
		reason, until, ok := limitIn(c.text, now)
		if !ok || reason != c.reason || !until.Equal(c.until) {
			t.Errorf("%q: %v %q %v, want %q %v", c.text, ok, reason, until, c.reason, c.until)
		}
	}
	for _, text := range []string{"tests failed", "syntax error near line 429x", "permission denied"} {
		if _, _, ok := limitIn(text, now); ok {
			t.Errorf("%q read as a limit", text)
		}
	}
}

// An agent writing about its task must not read as its harness running out.
func TestLimitOfIsStrictWithAgentText(t *testing.T) {
	now := time.Now()
	if r, _ := limitOf("", "I added a rate limit and a billing page; 429 is returned now.", now); r != "" {
		t.Errorf("agent text read as %q", r)
	}
	if r, _ := limitOf("", "Claude AI usage limit reached|1893456000", now); r != "usage_limit" {
		t.Errorf("harness text: %q", r)
	}
	if r, _ := limitOf("error: 429 Too Many Requests", "", now); r != "rate_limited" {
		t.Errorf("stderr: %q", r)
	}
}
