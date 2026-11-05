package swarm

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
)

// Finding is one thing the doctor noticed.
type Finding struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	// Fixable means doctor --fix can deal with it.
	Fixable bool `json:"fixable,omitempty"`
}

// Health is what the server knows about its own state.
type Health struct {
	Findings    []Finding         `json:"findings"`
	Repairs     []engine.Repair   `json:"repairs,omitempty"`
	Quarantined []json.RawMessage `json:"quarantined,omitempty"`
	Orphans     []string          `json:"orphan_worktrees,omitempty"`
	Overdue     []string          `json:"overdue_leases,omitempty"`
}

// Doctor examines the swarm's state. It changes nothing.
func (s *Service) Doctor(ctx context.Context) (Health, error) {
	var h Health
	var err error
	if h.Repairs, err = s.Engine.Repairs(ctx); err != nil {
		return h, err
	}
	h.Findings = append(h.Findings, Finding{Check: "records", OK: len(h.Repairs) == 0,
		Detail: plural(len(h.Repairs), "record was", "records were") + " repaired at startup, with the originals kept"})
	if h.Quarantined, err = s.Store.Quarantined(ctx); err != nil {
		return h, err
	}
	tasks, err := s.Engine.List(ctx)
	if err != nil {
		return h, err
	}
	byID := make(map[string]core.Task, len(tasks))
	now := time.Now()
	for _, t := range tasks {
		byID[t.ID] = t
		// The sweep expires a lease soon after its deadline. One long past it means the writer
		// is not keeping up, or is stuck.
		if t.Owner != "" && !t.LeaseDeadline.IsZero() && now.Sub(t.LeaseDeadline) > time.Minute {
			h.Overdue = append(h.Overdue, t.ID)
		}
	}
	h.Findings = append(h.Findings, Finding{Check: "leases", OK: len(h.Overdue) == 0,
		Detail: plural(len(h.Overdue), "lease is", "leases are") + " more than a minute past their deadline"})
	if s.Work != nil {
		ids, err := s.Work.WorktreeTasks()
		if err != nil {
			return h, err
		}
		for _, id := range ids {
			t, ok := byID[id]
			if !ok || t.State == core.Done || t.State == core.Canceled {
				h.Orphans = append(h.Orphans, id)
			}
		}
		h.Findings = append(h.Findings, Finding{Check: "worktrees", OK: len(h.Orphans) == 0, Fixable: len(h.Orphans) > 0,
			Detail: plural(len(h.Orphans), "worktree belongs", "worktrees belong") + " to finished or missing tasks"})
	}
	lag, err := s.Store.ProjectorLag(ctx)
	if err == nil {
		h.Findings = append(h.Findings, Finding{Check: "event log", OK: lag <= maxProjectorLag, Detail: plural(int(lag), "event", "events") + " not yet recorded"})
	}
	parked := 0
	for _, t := range tasks {
		if t.State == core.Parked {
			parked++
		}
	}
	h.Findings = append(h.Findings, Finding{Check: "parked", OK: true, Detail: plural(parked, "task waits", "tasks wait") + " for a person: aiculator task unpark"})
	return h, nil
}

// DoctorFix removes worktrees of finished or missing tasks. Their refs stay, as the record.
func (s *Service) DoctorFix(ctx context.Context) ([]string, error) {
	h, err := s.Doctor(ctx)
	if err != nil || s.Work == nil {
		return nil, err
	}
	for _, id := range h.Orphans {
		s.Work.Cleanup(ctx, id)
	}
	if err := s.Work.Prune(ctx); err != nil {
		return h.Orphans, err
	}
	return h.Orphans, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
