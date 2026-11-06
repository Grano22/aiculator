package harness

import "testing"

func TestAutoTier(t *testing.T) {
	for _, c := range []struct {
		model, effort string
		want          int
	}{
		{"claude-opus-5-5", "", 3},
		{"claude-fable-5-1", "low", 2},
		{"claude-sonnet-5-5", "", 2},
		{"claude-sonnet-5-5", "high", 3},
		{"claude-sonnet-5-5", "32000", 3},
		{"claude-haiku-5-5", "", 1},
		{"claude-haiku-5-5", "max", 2},
		{"gpt-5", "minimal", 1},
		{"gpt-5-pro", "", 3},
		{"gpt-5-mini", "", 1},
		{"o3", "", 2},
		{"gemini-3-pro", "", 3},
		{"gemini-2.5-flash", "", 1},
		{"glm-4.6", "", 2},
		{"qwen3-coder-480b", "", 2},
		{"llama-3.1-8b", "", 1},
		{"something-new", "", 1},
		{"", "high", 2},
		{"claude-opus-5-5", "ultrathink", 3}, // Already at the top.
		{"claude-haiku-5-5", "low", 1},       // Already at the bottom.
	} {
		got, why := AutoTier(c.model, c.effort)
		if got != c.want || why == "" {
			t.Errorf("%s at %q: tier %d (%s), want %d", c.model, c.effort, got, why, c.want)
		}
	}
}

func TestParseTier(t *testing.T) {
	for in, want := range map[string]int{"": 0, "auto": 0, " 2 ": 2, "ultra": 3, "Standard": 1} {
		if got, err := ParseTier(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	if _, err := ParseTier("4"); err == nil {
		t.Error("tier 4 accepted")
	}
}
