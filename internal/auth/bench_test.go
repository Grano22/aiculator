package auth

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkVerify(b *testing.B) {
	a, err := New()
	if err != nil {
		b.Fatal(err)
	}
	tok, err := a.Issue(Claims{Agent: "w1", Tier: 2})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := a.Verify(tok); err != nil {
			b.Fatal(err)
		}
	}
}

// The cache must give the same answers as checking every time: forged, altered and expired
// tokens are refused even right after the real one was accepted.
func TestVerifyCacheChangesNoAnswer(t *testing.T) {
	a, _ := New()
	other, _ := New()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	tok, _ := a.Issue(Claims{Agent: "w1", Tier: 1, Tags: []string{"go"}, Expires: now.Add(time.Hour)})
	for range 2 {
		c, err := a.Verify(tok)
		if err != nil || c.Agent != "w1" {
			t.Fatal(c, err)
		}
		c.Tags[0] = "changed" // A caller changing its copy must not change the cache.
	}
	if c, _ := a.Verify(tok); c.Tags[0] != "go" {
		t.Errorf("the cache shares its slices: %v", c.Tags)
	}
	forged, _ := other.Issue(Claims{Agent: "w1", Tier: 1, Tags: []string{"go"}, Expires: now.Add(time.Hour)})
	if _, err := a.Verify(forged); err == nil {
		t.Error("a token from another key was accepted")
	}
	// A character well inside the signature: the last ones carry padding bits that decoding
	// ignores, so changing them can leave the signature as it was.
	i := len(tok) - 20
	flip := byte('A')
	if tok[i] == 'A' {
		flip = 'B'
	}
	if _, err := a.Verify(tok[:i] + string(flip) + tok[i+1:]); err == nil {
		t.Error("an altered signature was accepted")
	}
	now = now.Add(2 * time.Hour)
	if _, err := a.Verify(tok); err == nil {
		t.Error("an expired token was accepted from the cache")
	}
	// Filling the cache only empties it.
	for i := range maxVerified + 10 {
		x, _ := a.Issue(Claims{Agent: fmt.Sprintf("a%d", i), Tier: 1})
		if _, err := a.Verify(x); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(a.verified); n > maxVerified {
		t.Errorf("cache holds %d", n)
	}
}
