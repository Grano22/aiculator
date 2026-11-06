package harness

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Tiers say what work an agent can take: 1 standard, 2 advanced, 3 ultra. AutoTier picks one
// from what runs the session: the model's capability class first, then the effort or variant
// it runs at, which moves it one step up or down.
//
// The table is by family, not by version, so a new release of a known family lands where its
// family does. A model it does not know gets tier 1, and says so: taking work below its
// ability costs less than failing work above it.
var modelTiers = []struct {
	re   *regexp.Regexp
	tier int
}{
	// Checked in order; the first match wins, so small variants come before their families.
	{regexp.MustCompile(`(?i)haiku|\bmini\b|-mini|nano|flash-lite|\blite\b|small|tiny|\b[1-9]b\b|-[1-9]b\b`), 1},
	{regexp.MustCompile(`(?i)opus|fable|-pro\b|o\d-pro|\bpro\b.*reason|ultra|deep-?think|gpt-5(\.\d)?-pro`), 3},
	{regexp.MustCompile(`(?i)sonnet|gpt-5|gpt-4\.1|\bo[34]\b|o[34]-|glm-4\.[5-9]|glm-[5-9]|kimi-k2|qwen3-coder|grok-4|deepseek-(v3|r1)|devstral|codestral|mistral-large`), 2},
	{regexp.MustCompile(`(?i)flash|gpt-4o|gpt-4|llama|qwen|mistral|gemma|phi`), 1},
}

// Efforts move a tier: high reasoning makes a model fit for harder work, low makes it less so.
var effortSteps = map[string]int{
	"minimal": -1, "low": -1, "fast": -1,
	"medium": 0, "default": 0, "normal": 0, "standard": 0,
	"high": 1, "xhigh": 1, "max": 1, "maximum": 1, "ultra": 1, "ultrathink": 1, "think-hard": 1, "thinking": 1, "reasoning": 1,
}

// AutoTier picks a tier for a model at an effort, and says why.
func AutoTier(model, effort string) (int, string) {
	model, effort = strings.TrimSpace(model), strings.ToLower(strings.TrimSpace(effort))
	tier, why := 1, "unknown model, so the safe default"
	if model == "" {
		why = "no model given, so the safe default"
	}
	for _, m := range modelTiers {
		if model != "" && m.re.MatchString(model) {
			tier, why = m.tier, fmt.Sprintf("%s is a tier %d model", model, m.tier)
			break
		}
	}
	if step, ok := effortSteps[effort]; ok && step != 0 {
		next := min(max(tier+step, 1), 3)
		if next != tier {
			why += fmt.Sprintf(", %s effort moves it to %d", effort, next)
			tier = next
		}
	} else if n, err := strconv.Atoi(effort); err == nil {
		// A thinking budget in tokens, as Claude Code's MAX_THINKING_TOKENS gives it.
		switch {
		case n >= 16000 && tier < 3:
			tier++
			why += fmt.Sprintf(", a %d-token thinking budget moves it to %d", n, tier)
		case n > 0 && n < 2000 && tier > 1:
			tier--
			why += fmt.Sprintf(", a %d-token thinking budget moves it to %d", n, tier)
		}
	}
	return tier, why
}

// ModelFromEnv finds the model and effort a harness runs with, from the variables harnesses
// and their launchers set. A flag given on the command line should come first.
func ModelFromEnv() (model, effort string) {
	for _, k := range []string{"AICULATOR_MODEL", "ANTHROPIC_MODEL", "CLAUDE_MODEL", "OPENAI_MODEL", "CODEX_MODEL", "GEMINI_MODEL", "OPENCODE_MODEL"} {
		if v := os.Getenv(k); v != "" {
			model = v
			break
		}
	}
	for _, k := range []string{"AICULATOR_EFFORT", "CLAUDE_CODE_EFFORT_LEVEL", "OPENAI_REASONING_EFFORT", "CODEX_REASONING_EFFORT", "MAX_THINKING_TOKENS"} {
		if v := os.Getenv(k); v != "" {
			effort = v
			break
		}
	}
	return model, effort
}

// ParseTier reads a tier as given by a person: 1 to 3, a name, or auto, which is 0.
func ParseTier(s string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return 0, nil
	case "1", "standard":
		return 1, nil
	case "2", "advanced":
		return 2, nil
	case "3", "ultra":
		return 3, nil
	}
	return 0, fmt.Errorf("tier %q: use 1 standard, 2 advanced, 3 ultra, or auto", s)
}
