package agentd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, body string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mod.IsZero() {
		_ = os.Chtimes(path, mod, mod)
	}
}

func TestClaudeCodeLog(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/me/work/shop_app"
	dir := claudeDir(home, cwd)
	if !strings.HasSuffix(dir, "-Users-me-work-shop-app") {
		t.Fatalf("dir %s", dir)
	}
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	line := func(at time.Time, id string, in, cc, cr, out int) string {
		return `{"type":"assistant","timestamp":"` + at.Format(time.RFC3339) + `","effort":"high","message":{"id":"` + id + `","model":"claude-sonnet-5-5","usage":{"input_tokens":` +
			itoa(in) + `,"cache_creation_input_tokens":` + itoa(cc) + `,"cache_read_input_tokens":` + itoa(cr) + `,"output_tokens":` + itoa(out) + `}}}`
	}
	mine := strings.Join([]string{
		line(t0.Add(-time.Hour), "old", 1000, 0, 0, 1000), // Before the moment asked about.
		line(t0.Add(time.Minute), "m1", 10, 100, 1000, 50),
		line(t0.Add(time.Minute), "m1", 10, 100, 1000, 50), // The same call, another block.
		`{"type":"user","message":{"content":"run aiculator work next --agent claude-7a"}}`,
		line(t0.Add(2*time.Minute), "m2", 5, 0, 2000, 30),
		`not json at all`,
	}, "\n")
	writeFile(t, filepath.Join(dir, "mine.jsonl"), mine, time.Now())
	writeFile(t, filepath.Join(dir, "other.jsonl"), line(t0.Add(time.Minute), "x", 9, 9, 9, 9)+"\nagent other-agent", time.Now().Add(time.Second))
	r := claudeReader{home: home}
	f := r.Find(cwd, "claude-7a", t0)
	if filepath.Base(f) != "mine.jsonl" {
		t.Fatalf("found %q: another session in the same folder must not be taken", f)
	}
	got, err := r.Read(f, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Calls != 2 || got.Tokens != 10+100+1000+50+5+2000+30 || got.Output != 80 || got.Model != "claude-sonnet-5-5" || got.Effort != "high" || got.LimitUsed != -1 {
		t.Errorf("reading %+v", got)
	}
	if r.Find("/elsewhere", "claude-7a", t0) != "" {
		t.Error("a log was found for another folder")
	}
}

func TestCodexLog(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/me/work/shop"
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	tc := func(at time.Time, in, out, total int, rl string) string {
		return `{"timestamp":"` + at.Format(time.RFC3339) + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":` + itoa(in) +
			`,"cached_input_tokens":0,"output_tokens":` + itoa(out) + `,"total_tokens":` + itoa(total) + `}},"rate_limits":` + rl + `}}`
	}
	body := strings.Join([]string{
		`{"timestamp":"2026-11-07T09:00:00Z","type":"session_meta","payload":{"cwd":"` + cwd + `","id":"s1"}}`,
		`{"timestamp":"2026-11-07T09:00:01Z","type":"turn_context","payload":{"model":"gpt-5.6","effort":"high","cwd":"` + cwd + `"}}`,
		tc(t0.Add(-time.Minute), 1000, 100, 1100, `{"primary":null}`),
		`{"type":"response_item","payload":{"type":"function_call","arguments":"{\"command\":[\"aiculator\",\"work\",\"next\",\"--agent\",\"codex-1\"]}"}}`,
		tc(t0.Add(time.Minute), 3000, 400, 3400, `{"primary":{"used_percent":42.5,"window_minutes":300,"resets_in_seconds":3600},"secondary":{"used_percent":100,"resets_at":1893456000}}`),
	}, "\n")
	writeFile(t, filepath.Join(home, ".codex", "sessions", "2026", "11", "07", "rollout-a.jsonl"), body, time.Now())
	r := codexReader{home: home}
	f := r.Find(cwd, "codex-1", t0)
	if f == "" {
		t.Fatal("not found")
	}
	got, err := r.Read(f, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tokens != 2300 || got.Input != 2000 || got.Output != 300 || got.Model != "gpt-5.6" || got.Effort != "high" {
		t.Errorf("reading %+v", got)
	}
	if got.LimitUsed != 100 || got.LimitResets.Unix() != 1893456000 {
		t.Errorf("limit: %v until %v", got.LimitUsed, got.LimitResets)
	}
	if r.Find("/other", "codex-1", t0) != "" || r.Find(cwd, "someone-else", t0) != "" {
		t.Error("found a session that is not this agent's")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
