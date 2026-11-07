package agentd

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

// A harness keeps a log of each session on disk, with the tokens every model call used.
// Reading it is the only way to learn what a session really spends: models do not know their
// own usage, and asking them to report it costs tokens and is often wrong. These formats are
// the harnesses' own and undocumented, so a reader that cannot make sense of a log reports
// nothing rather than a guess.
//
// Only token counts, the model, the effort and the plan's limits are read. The conversation
// itself is never looked at beyond finding which session belongs to an agent.

// Reading is what a session has spent since a moment, and what it says about its limits.
type Reading struct {
	Source string `json:"source"` // claude-code or codex
	File   string `json:"file,omitempty"`
	// Tokens is every token the session's model calls processed: input, cache and output.
	Tokens int64  `json:"tokens"`
	Input  int64  `json:"input"`
	Output int64  `json:"output"`
	Cache  int64  `json:"cache"`
	Calls  int    `json:"calls"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// LimitUsed is how much of the plan's limit is used, from 0 to 100, when the harness
	// says; -1 when it does not. LimitResets is when it resets.
	LimitUsed   float64   `json:"limit_used_percent"`
	LimitResets time.Time `json:"limit_resets,omitzero"`
}

// Reader finds a session's log and reads it.
type Reader interface {
	Name() string
	// Find locates the log of the session an agent runs in, by its folder and by the agent's
	// name, which the session wrote when it ran aiculator. Empty when there is none.
	Find(cwd, agent string, since time.Time) string
	// Read sums what the log records after since.
	Read(file string, since time.Time) (Reading, error)
}

// Readers are the harnesses whose logs can be read, in the order they are tried.
func Readers(home string) []Reader {
	return []Reader{claudeReader{home: home}, codexReader{home: home}}
}

// tailHas reports whether the last part of a file mentions s. Agents name themselves in the
// commands they run, so this tells which of several sessions in one folder is theirs.
func tailHas(file, s string, max int64) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	off := max64(st.Size()-max, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && len(buf) == 0 {
		return false
	}
	return strings.Contains(string(buf), s)
}

// headHas reports whether the first max bytes of a file mention s.
func headHas(file, s string, max int64) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, max)
	n, _ := f.Read(buf)
	return strings.Contains(string(buf[:n]), s)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// newest lists files under dir with an extension, changed since a moment, newest first.
func newest(dir, ext string, since time.Time, limit int) []string {
	type f struct {
		path string
		mod  time.Time
	}
	var list []f
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ext) {
			return nil
		}
		info, err := d.Info()
		if err == nil && !info.ModTime().Before(since) {
			list = append(list, f{p, info.ModTime()})
		}
		return nil
	})
	slices.SortFunc(list, func(a, b f) int { return b.mod.Compare(a.mod) })
	var out []string
	for _, x := range list[:min(len(list), limit)] {
		out = append(out, x.path)
	}
	return out
}

// --- Claude Code: ~/.claude/projects/<folder with every other character a dash>/<session>.jsonl.

type claudeReader struct{ home string }

func (claudeReader) Name() string { return "claude-code" }

// claudeDir is the folder Claude Code keeps a project's sessions in: the working directory
// with every character that is not a letter or digit turned into a dash.
func claudeDir(home, cwd string) string {
	enc := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '-'
	}, cwd)
	return filepath.Join(home, ".claude", "projects", enc)
}

func (c claudeReader) Find(cwd, agent string, since time.Time) string {
	for _, f := range newest(claudeDir(c.home, cwd), ".jsonl", since.Add(-time.Hour), 8) {
		if agent == "" || tailHas(f, agent, 4<<20) {
			return f
		}
	}
	return ""
}

func (claudeReader) Read(file string, since time.Time) (Reading, error) {
	r := Reading{Source: "claude-code", File: file, LimitUsed: -1}
	f, err := os.Open(file)
	if err != nil {
		return r, err
	}
	defer f.Close()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		// Most lines are not model calls; skip them without decoding.
		if !strings.Contains(string(line), `"usage"`) {
			continue
		}
		var d struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Effort    string    `json:"effort"`
			Message   struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					Input       int64 `json:"input_tokens"`
					CacheCreate int64 `json:"cache_creation_input_tokens"`
					CacheRead   int64 `json:"cache_read_input_tokens"`
					Output      int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &d) != nil || d.Type != "assistant" || d.Timestamp.Before(since) {
			continue
		}
		// One call is written as several lines, one per block, each with the call's usage.
		if d.Message.ID != "" {
			if seen[d.Message.ID] {
				continue
			}
			seen[d.Message.ID] = true
		}
		u := d.Message.Usage
		r.Input += u.Input
		r.Output += u.Output
		r.Cache += u.CacheCreate + u.CacheRead
		r.Tokens += u.Input + u.CacheCreate + u.CacheRead + u.Output
		r.Calls++
		if d.Message.Model != "" {
			r.Model = d.Message.Model
		}
		if d.Effort != "" {
			r.Effort = d.Effort
		}
	}
	return r, sc.Err()
}

// --- Codex: ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl, with running totals in token_count
// events, and the plan's rate limits beside them.

type codexReader struct{ home string }

func (codexReader) Name() string { return "codex" }

func (c codexReader) Find(cwd, agent string, since time.Time) string {
	for _, f := range newest(filepath.Join(c.home, ".codex", "sessions"), ".jsonl", since.Add(-time.Hour), 20) {
		// The session's folder is in its first line, so only the start is read.
		if !headHas(f, `"cwd":"`+cwd+`"`, 64<<10) && !headHas(f, `"cwd": "`+cwd+`"`, 64<<10) {
			continue
		}
		if agent == "" || tailHas(f, agent, 4<<20) {
			return f
		}
	}
	return ""
}

func (codexReader) Read(file string, since time.Time) (Reading, error) {
	r := Reading{Source: "codex", File: file, LimitUsed: -1}
	f, err := os.Open(file)
	if err != nil {
		return r, err
	}
	defer f.Close()
	type usage struct {
		Input  int64 `json:"input_tokens"`
		Cached int64 `json:"cached_input_tokens"`
		Output int64 `json:"output_tokens"`
		Total  int64 `json:"total_tokens"`
	}
	var before, last usage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		s := string(line)
		if !strings.Contains(s, `"token_count"`) && !strings.Contains(s, `"turn_context"`) {
			continue
		}
		var d struct {
			Timestamp time.Time `json:"timestamp"`
			Type      string    `json:"type"`
			Payload   struct {
				Type   string `json:"type"`
				Model  string `json:"model"`
				Effort string `json:"effort"`
				Info   *struct {
					Total usage `json:"total_token_usage"`
				} `json:"info"`
				RateLimits map[string]any `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &d) != nil {
			continue
		}
		if d.Type == "turn_context" {
			if d.Payload.Model != "" {
				r.Model = d.Payload.Model
			}
			if d.Payload.Effort != "" {
				r.Effort = d.Payload.Effort
			}
			continue
		}
		if d.Payload.Type != "token_count" || d.Payload.Info == nil {
			continue
		}
		// Totals run for the whole session: what was spent since a moment is the difference.
		if d.Timestamp.Before(since) {
			before = d.Payload.Info.Total
		} else {
			last = d.Payload.Info.Total
			r.Calls++
		}
		if used, resets, ok := codexLimit(d.Payload.RateLimits, d.Timestamp); ok {
			r.LimitUsed, r.LimitResets = used, resets
		}
	}
	if r.Calls > 0 {
		r.Input = last.Input - before.Input
		r.Output = last.Output - before.Output
		r.Cache = last.Cached - before.Cached
		r.Tokens = last.Total - before.Total
		if r.Tokens == 0 {
			r.Tokens = r.Input + r.Output
		}
	}
	return r, sc.Err()
}

// codexLimit reads the tightest of Codex's rate limit windows: how much is used and when it
// resets. The shape has changed between versions, so it is read loosely: a window is an object
// with used_percent, and resets_at (Unix seconds) or resets_in_seconds.
func codexLimit(m map[string]any, at time.Time) (float64, time.Time, bool) {
	best, ok := -1.0, false
	var resets time.Time
	for _, k := range []string{"primary", "secondary"} {
		w, _ := m[k].(map[string]any)
		if w == nil {
			continue
		}
		used, isNum := w["used_percent"].(float64)
		if !isNum || used < best {
			continue
		}
		best, ok = used, true
		resets = time.Time{}
		if v, isNum := w["resets_at"].(float64); isNum && v > 0 {
			resets = time.Unix(int64(v), 0)
		} else if v, isNum := w["resets_in_seconds"].(float64); isNum && v > 0 {
			resets = at.Add(time.Duration(v) * time.Second)
		}
	}
	if reached, _ := m["rate_limit_reached_type"].(string); reached != "" && best < 100 {
		best, ok = 100, true
	}
	return best, resets, ok
}
