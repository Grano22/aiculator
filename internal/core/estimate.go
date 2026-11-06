package core

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"unicode"
)

// Estimate predicts how many tokens a task will take, from the tasks like it that finished.
type Estimate struct {
	// Tokens is the likely amount, with Low and High the range most such tasks fell in.
	Tokens int64 `json:"tokens"`
	Low    int64 `json:"low"`
	High   int64 `json:"high"`
	// Basis is how many finished tasks it rests on, and Method how they were chosen:
	// similar (tier, tags, labels and words in common), tier (the same tier), all, or none.
	Basis   int      `json:"basis"`
	Method  string   `json:"method"`
	Similar []string `json:"similar,omitempty"`
}

const (
	estimateK       = 8
	minSimilar      = 3
	similarEnough   = 3.0
	maxSizeScale    = 2.0
	estimateSimilar = 5
)

// EstimateTokens predicts t's tokens from history, the tasks that reached Done with tokens
// recorded. Tokens count every try, since retries are part of what a task costs. It is a pure
// function of its inputs, so the same history always gives the same answer.
func EstimateTokens(t Task, history []Task) Estimate {
	var done []Task
	for _, h := range history {
		if h.State == Done && h.Tokens > 0 && h.ID != t.ID {
			done = append(done, h)
		}
	}
	if len(done) == 0 {
		return Estimate{Method: "none"}
	}
	words := wordSet(t.Title + " " + t.Spec)
	type cand struct {
		t     Task
		score float64
	}
	cands := make([]cand, 0, len(done))
	for _, h := range done {
		cands = append(cands, cand{h, similarity(t, h, words)})
	}
	slices.SortFunc(cands, func(a, b cand) int {
		return cmp.Or(cmp.Compare(b.score, a.score), b.t.UpdatedAt.Compare(a.t.UpdatedAt), strings.Compare(a.t.ID, b.t.ID))
	})
	var picked []cand
	for _, c := range cands {
		if c.score >= similarEnough && len(picked) < estimateK {
			picked = append(picked, c)
		}
	}
	method := "similar"
	if len(picked) < minSimilar {
		picked, method = nil, "tier"
		for _, c := range cands {
			if c.t.Tier == t.Tier {
				picked = append(picked, cand{c.t, 1})
			}
		}
		if len(picked) == 0 {
			method = "all"
			for _, c := range cands {
				picked = append(picked, cand{c.t, 1})
			}
		}
	}
	// A longer spec costs more, but far less than in proportion.
	sizes := make([]float64, 0, len(picked))
	for _, c := range picked {
		sizes = append(sizes, float64(len(c.t.Title)+len(c.t.Spec)))
	}
	slices.Sort(sizes)
	scale := 1.0
	if mid := sizes[len(sizes)/2]; mid > 0 {
		r := float64(len(t.Title)+len(t.Spec)) / mid
		scale = math.Sqrt(min(max(r, 1/maxSizeScale), maxSizeScale))
	}
	vals := make([]weighted, 0, len(picked))
	for _, c := range picked {
		vals = append(vals, weighted{float64(c.t.Tokens) * scale, max(c.score, 1)})
	}
	e := Estimate{Basis: len(picked), Method: method,
		Tokens: int64(quantile(vals, 0.5)), Low: int64(quantile(vals, 0.25)), High: int64(quantile(vals, 0.9))}
	if method == "similar" {
		for _, c := range picked[:min(len(picked), estimateSimilar)] {
			e.Similar = append(e.Similar, c.t.ID)
		}
	}
	return e
}

func similarity(t, h Task, words map[string]bool) float64 {
	s := 0.0
	if t.Tier == h.Tier {
		s += 3
	}
	for _, tag := range t.Tags {
		if slices.Contains(h.Tags, tag) {
			s += 2
		}
	}
	for k, v := range t.Labels {
		if hv, ok := h.Labels[k]; ok {
			if hv == v {
				s += 2
			} else {
				s++
			}
		}
	}
	if len(words) > 0 {
		hw := wordSet(h.Title + " " + h.Spec)
		common := 0
		for w := range words {
			if hw[w] {
				common++
			}
		}
		if union := len(words) + len(hw) - common; union > 0 {
			s += 4 * float64(common) / float64(union)
		}
	}
	return s
}

// wordSet is the words of a text worth comparing: lower case, three letters or more.
func wordSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 3 {
			out[w] = true
		}
	}
	return out
}

type weighted struct{ v, w float64 }

// quantile is the weighted q-quantile of vals.
func quantile(vals []weighted, q float64) float64 {
	vs := slices.Clone(vals)
	slices.SortFunc(vs, func(a, b weighted) int { return cmp.Compare(a.v, b.v) })
	total := 0.0
	for _, x := range vs {
		total += x.w
	}
	acc := 0.0
	for _, x := range vs {
		acc += x.w
		if acc >= q*total {
			return x.v
		}
	}
	return vs[len(vs)-1].v
}
