package connect

import (
	"context"
	"errors"
	"fmt"
)

// ImportOptions shapes one import.
type ImportOptions struct {
	Query string
	Limit int
}

// Report says what an import did, or would do.
type Report struct {
	Mapped  []Mapped `json:"-"`
	Read    int      `json:"read"`
	Skipped int      `json:"skipped"`
	Failed  []string `json:"failed,omitempty"`
	Cyclic  []string `json:"cyclic,omitempty"`
}

var errLimit = errors.New("limit reached")

// Read pulls and maps once, without touching the swarm. An item that cannot be mapped is
// reported rather than stopping the rest: one bad row should not lose a thousand good ones.
// A failure to reach the system still stops it, since then the list would be incomplete.
func Read(ctx context.Context, c Connector, s *Source, o ImportOptions) (Report, error) {
	var r Report
	var items []Mapped
	err := c.Pull(ctx, PullOptions{Query: o.Query}, func(it Item) error {
		r.Read++
		if s.Skip(it) {
			r.Skipped++
			return nil
		}
		m, err := s.Map(it)
		if err != nil {
			r.Failed = append(r.Failed, err.Error())
			return nil
		}
		items = append(items, m)
		if o.Limit > 0 && len(items) >= o.Limit {
			return errLimit
		}
		return nil
	})
	if err != nil && !errors.Is(err, errLimit) {
		return r, fmt.Errorf("reading %s: %w", s.Name, err)
	}
	// Two rows with one key would race for one task, so the later one is refused.
	seen := make(map[string]bool, len(items))
	unique := items[:0]
	for _, m := range items {
		if seen[m.Spec.ID] {
			r.Failed = append(r.Failed, fmt.Sprintf("%s: the key %q appears twice", s.Name, m.Link.Key))
			continue
		}
		seen[m.Spec.ID] = true
		unique = append(unique, m)
	}
	r.Mapped, r.Cyclic = Order(unique)
	return r, nil
}
