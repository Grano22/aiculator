package swarm

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/meta"
	"github.com/Grano22/aiculator/internal/store"
)

// ImportItem is one task from another system, already mapped.
type ImportItem struct {
	Spec  engine.TaskSpec       `json:"spec"`
	Props map[string]*meta.Prop `json:"props,omitempty"`
	Link  store.Link            `json:"link"`
}

// ImportResult says what happened to one item.
type ImportResult struct {
	ID     string `json:"id"`
	Action string `json:"action"` // created, updated, unchanged or failed.
	Error  string `json:"error,omitempty"`
	// Warning describes something the task lost on the way in.
	Warning string `json:"warning,omitempty"`
}

// importChunk bounds how long one batch holds the writer, so that an import of ten thousand
// tasks cannot delay a claim by more than a little.
const importChunk = 32

// Import stores mapped items. Re-importing is safe: the task ID comes from the item's key,
// so an item already here is left alone, or brought up to date when update is set.
// It never changes a task's progress. Items must be in dependency order.
func (s *Service) Import(ctx context.Context, items []ImportItem, update bool) ([]ImportResult, error) {
	out := make([]ImportResult, 0, len(items))
	for chunk := range slices.Chunk(items, importChunk) {
		res, err := s.importChunk(ctx, chunk, update)
		out = append(out, res...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *Service) importChunk(ctx context.Context, items []ImportItem, update bool) ([]ImportResult, error) {
	ids := make([]string, 0, len(items))
	inChunk := make(map[string]bool, len(items))
	for _, it := range items {
		ids = append(ids, it.Spec.ID)
		inChunk[it.Spec.ID] = true
		ids = append(ids, it.Spec.Deps...)
	}
	have, err := s.Engine.Existing(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]ImportResult, len(items))
	var fresh []engine.TaskSpec
	var freshAt []int
	for i, it := range items {
		out[i].ID = it.Spec.ID
		if len(it.Spec.Checks) > 0 && s.Work == nil {
			out[i].Action, out[i].Error = "failed", "tasks with checks need a repository: start the server with --repo"
			continue
		}
		cur, exists := have[it.Spec.ID]
		if exists {
			out[i].Action = "unchanged"
			if update {
				changed, err := s.updateItem(ctx, cur, it)
				if err != nil {
					out[i].Action, out[i].Error = "failed", err.Error()
				} else if changed {
					out[i].Action = "updated"
				}
			}
			continue
		}
		// An item can be blocked by something this import did not include. Failing it would
		// let one link lose the task, and keeping the dependency would block it forever, so
		// the task comes in without it, and the caller and the task's properties both say so.
		var missing []string
		it.Spec.Deps = slices.DeleteFunc(slices.Clone(it.Spec.Deps), func(d string) bool {
			_, here := have[d]
			if here || inChunk[d] {
				return false
			}
			missing = append(missing, d)
			return true
		})
		if len(missing) > 0 {
			out[i].Warning = "blocked by tasks that were not imported, so it can start without them: " + strings.Join(missing, ", ")
			if it.Props == nil {
				it.Props = map[string]*meta.Prop{}
			}
			it.Props["unresolved_deps"] = &meta.Prop{Value: missing}
		}
		// Properties first: once a task is visible, an agent may be reading them.
		if err := s.storeItem(it); err != nil {
			out[i].Action, out[i].Error = "failed", err.Error()
			continue
		}
		fresh = append(fresh, it.Spec)
		freshAt = append(freshAt, i)
	}
	if len(fresh) == 0 {
		return out, nil
	}
	res, err := s.Engine.AddMany(ctx, fresh)
	if err != nil {
		return out, err
	}
	for j, r := range res {
		i := freshAt[j]
		switch {
		case r.Err == nil:
			out[i].Action = "created"
		case errors.Is(r.Err, engine.ErrExists):
			out[i].Action = "unchanged"
		default:
			out[i].Action, out[i].Error = "failed", r.Err.Error()
		}
	}
	return out, nil
}

func (s *Service) storeItem(it ImportItem) error {
	if len(it.Props) > 0 {
		if _, err := s.Store.PatchMeta(it.Spec.ID, meta.Patch{Props: it.Props}, time.Now()); err != nil {
			return err
		}
	}
	return s.saveLink(it.Link)
}

// saveLink records a link, keeping up with anything else that writes it.
func (s *Service) saveLink(l store.Link) error {
	for range 8 {
		_, rev, err := s.Store.GetLink(l.Source, l.TaskID)
		if err != nil {
			return err
		}
		l.UpdatedAt = time.Now()
		if _, err = s.Store.PutLink(l, rev); err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return fmt.Errorf("%w: link for %s kept changing", store.ErrConflict, l.TaskID)
}

// updateItem brings a task's description up to date. It reports whether anything changed.
func (s *Service) updateItem(ctx context.Context, cur core.Task, it ImportItem) (bool, error) {
	a := Difference(cur, it.Spec)
	changed := a != nil
	if changed {
		if _, err := s.Engine.Amend(ctx, cur.ID, "system:connect", *a); err != nil {
			return false, err
		}
	}
	old, _, err := s.Store.GetLink(it.Link.Source, it.Link.TaskID)
	if err != nil {
		return false, err
	}
	if old.Version != it.Link.Version {
		// A kept copy of an attachment stays valid while its link is the same.
		m, _, err := s.Store.GetMeta(cur.ID)
		if err != nil {
			return false, err
		}
		props := maps.Clone(it.Props)
		for name, p := range props {
			if p != nil && p.Resource != nil {
				if have, ok := m.Props[name]; ok && have.Resource != nil && have.Resource.URL == p.Resource.URL {
					delete(props, name)
				}
			}
		}
		if len(props) > 0 {
			if _, err := s.Store.PatchMeta(cur.ID, meta.Patch{Props: props}, time.Now()); err != nil {
				return false, err
			}
		}
		if err := s.saveLink(it.Link); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// Difference works out the amendment that would make a task say what a spec says, or nil
// if they already agree. Labels are only added or changed, so that one added by hand stays.
func Difference(cur core.Task, want engine.TaskSpec) *core.Amendment {
	var a core.Amendment
	any := false
	if cur.Title != want.Title {
		a.Title, any = &want.Title, true
	}
	if cur.Spec != want.Spec {
		a.Spec, any = &want.Spec, true
	}
	if cur.Priority != want.Priority {
		a.Priority, any = &want.Priority, true
	}
	for k, v := range want.Labels {
		if cur.Labels[k] != v {
			if a.Labels == nil {
				a.Labels = map[string]string{}
			}
			a.Labels[k], any = v, true
		}
	}
	if !any {
		return nil
	}
	return &a
}
