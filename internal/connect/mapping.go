package connect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/engine"
	"github.com/Grano22/aiculator/internal/meta"
	"github.com/Grano22/aiculator/internal/store"
)

// Labels every imported task carries. The syncer finds its tasks by them, and an agent
// is told that a task with a source holds text from outside.
const (
	LabelSource   = "source"
	LabelExternal = "external"
)

// Attachment is a file on an item, by reference.
type Attachment struct {
	Name     string
	Resource meta.Resource
}

// Item is one thing from another system, before any mapping.
type Item struct {
	Key         string
	Version     string // Changes when the item changes.
	URL         string
	Status      string
	Fields      map[string]any
	Attachments []Attachment
	BlockedBy   []string // Keys of the items this one depends on.
}

// lookup finds a field by exact name, then ignoring case, then as a dotted path.
func (it Item) lookup(path string) (any, bool) {
	if v, ok := it.Fields[path]; ok {
		return v, v != nil
	}
	for k, v := range it.Fields {
		if strings.EqualFold(k, path) {
			return v, v != nil
		}
	}
	var cur any = map[string]any(it.Fields)
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, cur != nil
}

// Text renders any field value as text.
func Text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []string:
		return strings.Join(x, ", ")
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, Text(e))
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		for _, k := range []string{"name", "value", "displayName", "key"} {
			if s, ok := x[k]; ok {
				return Text(s)
			}
		}
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

func (w When) matches(it Item) bool {
	for field, want := range w {
		v, _ := it.lookup(field)
		if !strings.EqualFold(strings.TrimSpace(Text(v)), strings.TrimSpace(want)) {
			return false
		}
	}
	return true
}

// resolve works out one attribute. Rules are tried in order, then the named field through
// its map, then the default.
func (f FieldSpec) resolve(it Item) (any, bool) {
	for _, r := range f.Rules {
		if r.When.matches(it) {
			return r.Value, true
		}
	}
	if f.From != "" {
		if v, ok := it.lookup(f.From); ok {
			if f.Map == nil {
				return v, true
			}
			text := Text(v)
			for k, out := range f.Map {
				if strings.EqualFold(k, text) {
					return out, true
				}
			}
		}
	}
	return f.Default, f.Default != nil
}

func resolveText(f *FieldSpec, it Item) string {
	if f == nil {
		return ""
	}
	v, _ := f.resolve(it)
	return strings.TrimSpace(Text(v))
}

func resolveInt(f *FieldSpec, it Item) (int, bool, error) {
	if f == nil {
		return 0, false, nil
	}
	v, ok := f.resolve(it)
	if !ok {
		return 0, false, nil
	}
	if n, isInt := v.(int); isInt {
		return n, true, nil
	}
	s := strings.TrimSpace(Text(v))
	if s == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%q is not a number", s)
	}
	return int(n), true, nil
}

func resolveList(f *FieldSpec, it Item) []string {
	if f == nil {
		return nil
	}
	v, ok := f.resolve(it)
	if !ok {
		return nil
	}
	var out []string
	switch x := v.(type) {
	case []string:
		out = x
	case []any:
		for _, e := range x {
			out = append(out, Text(e))
		}
	default:
		for _, p := range strings.Split(Text(v), ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// TaskID derives a stable ID from a source and a key, so importing twice finds the same
// task. Keys that do not survive cleaning get a short hash, so two can never collide.
func TaskID(source, key string) string {
	clean := strings.Trim(unsafeID.ReplaceAllString(key, "-"), "-")
	if clean == key && len(source)+1+len(clean) <= 128 {
		return source + "-" + clean
	}
	h := sha256.Sum256([]byte(key))
	suffix := hex.EncodeToString(h[:6])
	if room := 128 - len(source) - len(suffix) - 2; len(clean) > room {
		clean = clean[:room]
	}
	if clean == "" {
		return source + "-" + suffix
	}
	return source + "-" + clean + "-" + suffix
}

// Mapped is an item turned into what the swarm stores.
type Mapped struct {
	Spec  engine.TaskSpec       `json:"spec"`
	Props map[string]*meta.Prop `json:"props,omitempty"`
	Link  store.Link            `json:"link"`
}

// Skip reports whether the source's skip rules leave this item out.
func (s *Source) Skip(it Item) bool {
	return slices.ContainsFunc(s.SkipWhen, func(w When) bool { return w.matches(it) })
}

// Map turns an item into a task. Everything an outside system controls goes through here,
// and none of it can reach checks, touches, budget or review.
func (s *Source) Map(it Item) (Mapped, error) {
	title := resolveText(s.Fields.Title, it)
	if title == "" {
		return Mapped{}, fmt.Errorf("%s: no title", it.Key)
	}
	if len(title) > 512 {
		title = title[:512]
	}
	d := s.Defaults
	spec := engine.TaskSpec{
		ID: TaskID(s.Name, it.Key), Title: title, Spec: d.Spec, Tier: d.Tier, Priority: d.Priority,
		Tags: slices.Clone(d.Tags), Checks: slices.Clone(d.Checks), Touches: slices.Clone(d.Touches),
		RequireReview: d.RequireReview, AllowEmpty: d.AllowEmpty, Budget: d.Budget,
		Labels: maps.Clone(d.Labels),
	}
	if text := resolveText(s.Fields.Spec, it); text != "" {
		spec.Spec = text
	}
	var err error
	if n, ok, e := resolveInt(s.Fields.Priority, it); e != nil {
		return Mapped{}, fmt.Errorf("%s: priority: %w", it.Key, e)
	} else if ok {
		spec.Priority = n
	}
	if n, ok, e := resolveInt(s.Fields.Tier, it); e != nil {
		return Mapped{}, fmt.Errorf("%s: tier: %w", it.Key, e)
	} else if ok {
		spec.Tier = n
	}
	if tags := resolveList(s.Fields.Tags, it); tags != nil {
		spec.Tags = append(spec.Tags, tags...)
	}
	if spec.Aging, err = core.ParseAging(d.Aging); err != nil {
		return Mapped{}, err
	}
	if len(d.Aging) == 0 {
		spec.Aging = nil // No steps here means the server's steps apply.
	}
	if spec.Labels == nil {
		spec.Labels = map[string]string{}
	}
	for name, f := range s.Fields.Labels {
		if v, ok := f.resolve(it); ok {
			if text := strings.TrimSpace(Text(v)); text != "" {
				if len(text) > core.MaxLabelValue {
					text = text[:core.MaxLabelValue]
				}
				spec.Labels[name] = text
			}
		}
	}
	// These two come last, so a mapping cannot point a task at another source's item.
	spec.Labels[LabelSource] = s.Name
	spec.Labels[LabelExternal] = trimLabel(it.Key)

	for _, dep := range it.BlockedBy {
		spec.Deps = append(spec.Deps, TaskID(s.Name, dep))
	}
	props := map[string]*meta.Prop{}
	for name, f := range s.Fields.Props {
		if v, ok := f.resolve(it); ok {
			props[name] = &meta.Prop{Value: v}
		}
	}
	if it.URL != "" {
		props["external_url"] = &meta.Prop{Value: it.URL}
	}
	for _, a := range it.Attachments {
		r := a.Resource
		props[attachmentProp(a.Name)] = &meta.Prop{Resource: &r}
	}
	status := it.Status
	if s.Fields.Status != nil {
		status = resolveText(s.Fields.Status, it)
	}
	return Mapped{
		Spec: spec, Props: props,
		Link: store.Link{Source: s.Name, Key: it.Key, TaskID: spec.ID, URL: it.URL, Version: it.Version, RemoteStatus: status},
	}, nil
}

func trimLabel(s string) string {
	if len(s) > core.MaxLabelValue {
		return s[:core.MaxLabelValue]
	}
	return s
}

var unsafeProp = regexp.MustCompile(`[^A-Za-z0-9._/-]+`)

func attachmentProp(name string) string {
	p := "attachment/" + strings.Trim(unsafeProp.ReplaceAllString(name, "-"), "-./")
	if len(p) > 128 {
		p = p[:128]
	}
	return p
}

// Order puts tasks after what they depend on, and reports what cannot be imported: items in
// a dependency cycle, and items depending on them. A dependency on something outside this
// import stays for the server to judge, since it may already have that task.
func Order(items []Mapped) (ordered []Mapped, cyclic []string) {
	byID := make(map[string]int, len(items))
	for i, m := range items {
		byID[m.Spec.ID] = i
	}
	const (
		unseen = iota
		visiting
		finished
		broken
	)
	mark := make([]int, len(items))
	var visit func(i int) bool
	visit = func(i int) bool {
		switch mark[i] {
		case visiting, broken:
			return false
		case finished:
			return true
		}
		mark[i] = visiting
		for _, d := range items[i].Spec.Deps {
			if j, ok := byID[d]; ok && !visit(j) {
				mark[i] = broken
				cyclic = append(cyclic, items[i].Spec.ID)
				return false
			}
		}
		mark[i] = finished
		ordered = append(ordered, items[i])
		return true
	}
	for i := range items {
		visit(i)
	}
	slices.Sort(cyclic)
	return ordered, slices.Compact(cyclic)
}
