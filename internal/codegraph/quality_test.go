package codegraph

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The built-in graph must find the answer to real questions about this repository, in far
// fewer tokens than reading the file that holds it. The bar is loose, so that ordinary code
// changes do not trip it, and the numbers are logged to watch over time: go test -v -run Quality.
func TestQualityOnThisRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("indexes the repository")
	}
	g, err := Build(context.Background(), "../..")
	if err != nil {
		t.Skip("not in a git checkout:", err)
	}
	cases := []struct{ q, want, file string }{
		{"where are session tokens verified", "Authority.Verify", "internal/auth/token.go"},
		{"how are leases fenced against zombie agents", "fence", "internal/core/task.go"},
		{"what hands back tasks held by a stopped agent", "ReleaseHeldBy", "internal/engine/engine.go"},
		{"how does the driver detect usage limits", "limitIn", "internal/driver/limits.go"},
		{"where is the token estimate computed", "EstimateTokens", "internal/core/estimate.go"},
		{"how is the tier chosen automatically", "AutoTier", "internal/harness/tier.go"},
		{"which function repairs damaged task records", "Repair", "internal/core/repair.go"},
		{"how are notes ranked on recall", "recall", "internal/memory/notes.go"},
		{"ReleaseHeldBy", "Engine.ReleaseHeldBy", "internal/engine/engine.go"},
		{"how does the standby take over", "lead", "cmd/aiculator/leader.go"},
	}
	var answer, read, found int
	for _, c := range cases {
		a := g.Query(c.q, 0)
		b, err := os.ReadFile("../../" + c.file)
		if err != nil {
			t.Skip("the repository changed shape:", err)
		}
		file := int(float64(len(b)) / charsPerToken)
		hit := strings.Contains(a.Text, c.want)
		if hit {
			found++
		}
		answer += a.Tokens
		read += file
		t.Logf("%-48s answer %4d tokens, the file %6d, found %v", c.q, a.Tokens, file, hit)
	}
	ratio := float64(read) / float64(answer)
	t.Logf("answers took %d tokens, reading the files %d: %.1fx fewer; %d of %d found", answer, read, ratio, found, len(cases))
	if found*10 < len(cases)*8 {
		t.Errorf("only %d of %d answers hold what was asked for", found, len(cases))
	}
	if ratio < 3 {
		t.Errorf("answers cost %.1fx fewer tokens than the files, want at least 3x", ratio)
	}
}
