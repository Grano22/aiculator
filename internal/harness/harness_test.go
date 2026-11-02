package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var entry = Entry{Command: "swarm", Args: []string{"mcp", "--auto", "--tier", "1"}}

func TestRegistryIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, h := range All() {
		if seen[h.ID] {
			t.Errorf("duplicate id %s", h.ID)
		}
		seen[h.ID] = true
		if h.Name == "" || h.SkillsDir == "" || h.GlobalSkillsDir == "" {
			t.Errorf("%s is incomplete: %+v", h.ID, h)
		}
		if filepath.IsAbs(h.SkillsDir) || strings.Contains(h.SkillsDir, "..") {
			t.Errorf("%s: unsafe skills dir %s", h.ID, h.SkillsDir)
		}
		if !strings.HasPrefix(h.GlobalSkillsDir, "~/") {
			t.Errorf("%s: global dir must start with ~/: %s", h.ID, h.GlobalSkillsDir)
		}
		if (h.MCP == MCPNone) != (h.MCPFile == "") {
			t.Errorf("%s: MCP format and file disagree", h.ID)
		}
		if h.SkillsDir == ".agents/skills" && !h.ReadsAgents {
			t.Errorf("%s: skills dir is .agents/skills but ReadsAgents is false", h.ID)
		}
		// Unpinned npx would run whatever was published most recently.
		if len(h.ACP) > 2 && h.ACP[0] == "npx" && !regexp.MustCompile(`@[0-9]`).MatchString(h.ACP[2]) {
			t.Errorf("%s: npx package is not pinned: %v", h.ID, h.ACP)
		}
		for _, d := range h.StateDirs {
			if !strings.HasPrefix(d, "~/") {
				t.Errorf("%s: state dir %s", h.ID, d)
			}
		}
	}
	for _, want := range []string{"claude", "codex", "gemini", "opencode", "pi", "cursor", "copilot", "goose"} {
		if h, ok := Get(want); !ok || h.Source != SourceDocs {
			t.Errorf("%s must be present and verified against its docs", want)
		}
	}
}

func TestClaudeNeedsALinkButOthersDoNot(t *testing.T) {
	c, _ := Get("claude")
	if c.ReadsAgents {
		t.Fatal("Claude Code's docs list only .claude/skills")
	}
	for _, id := range []string{"codex", "gemini", "opencode", "pi", "cursor", "copilot", "goose"} {
		if h, _ := Get(id); !h.ReadsAgents {
			t.Errorf("%s reads .agents/skills per its docs", id)
		}
	}
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return d
}

func TestJSONMergeKeepsEverythingElse(t *testing.T) {
	root := t.TempDir()
	h, _ := Get("claude")
	original := `{"other": 1, "mcpServers": {"mine": {"command": "x"}}}`
	path := filepath.Join(root, ".mcp.json")
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	bk := filepath.Join(root, "backups")
	if r, err := h.WriteMCP(root, entry, bk); err != nil || !r.Changed {
		t.Fatalf("write: %+v %v", r, err)
	}
	d := readDoc(t, path)
	servers := d["mcpServers"].(map[string]any)
	if d["other"] != float64(1) || servers["mine"] == nil || servers[ServerName] == nil {
		t.Fatalf("merge lost data: %v", d)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %v", fi.Mode().Perm())
	}
	files, _ := os.ReadDir(bk)
	if len(files) != 1 {
		t.Fatalf("expected one backup, got %d", len(files))
	}
	if b, _ := os.ReadFile(filepath.Join(bk, files[0].Name())); string(b) != original {
		t.Fatalf("backup is wrong: %s", b)
	}
	// A second run changes nothing and makes no second backup.
	if r, err := h.WriteMCP(root, entry, bk); err != nil || r.Changed {
		t.Fatalf("not idempotent: %+v %v", r, err)
	}
	if !h.HasMCP(root) {
		t.Fatal("HasMCP is false after writing")
	}
	if _, err := h.RemoveMCP(root, bk); err != nil {
		t.Fatal(err)
	}
	d = readDoc(t, path)
	if d["mcpServers"].(map[string]any)["mine"] == nil || d["mcpServers"].(map[string]any)[ServerName] != nil {
		t.Fatalf("remove was wrong: %v", d)
	}
}

func TestSwarmCreatedFileIsRemovedCleanly(t *testing.T) {
	for _, id := range []string{"claude", "codex", "opencode", "gemini", "cursor"} {
		root := t.TempDir()
		h, _ := Get(id)
		if _, err := h.WriteMCP(root, entry, ""); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if _, err := h.RemoveMCP(root, ""); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if _, err := os.Stat(h.mcpPath(root)); !os.IsNotExist(err) {
			t.Errorf("%s left its file behind", id)
		}
	}
}

func TestBrokenJSONIsLeftAlone(t *testing.T) {
	root := t.TempDir()
	h, _ := Get("cursor")
	path := h.mcpPath(root)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	broken := "{ not json, // has comments\n}"
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.WriteMCP(root, entry, ""); err == nil {
		t.Fatal("wrote over a file it could not parse")
	}
	if b, _ := os.ReadFile(path); string(b) != broken {
		t.Fatal("broken file was changed")
	}
}

func TestOpenCodeFormatAndTimeout(t *testing.T) {
	root := t.TempDir()
	h, _ := Get("opencode")
	path := filepath.Join(root, "opencode.json")
	// An old-shape entry and the user's own server, both in the existing file.
	old := `{"model": "m", "mcp": {"swarm": {"type": "local", "command": ["x"]}, "servers": {"mine": {"type": "local", "command": ["y"]}}}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.WriteMCP(root, entry, ""); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	mcp := doc["mcp"].(map[string]any)
	if mcp[ServerName] != nil {
		t.Error("the old-shape entry lingered")
	}
	servers := mcp["servers"].(map[string]any)
	if servers["mine"] == nil || doc["model"] != "m" {
		t.Fatalf("merge lost data: %v", doc)
	}
	s := servers[ServerName].(map[string]any)
	if s["type"] != "local" || s["command"].([]any)[0] != "swarm" {
		t.Fatalf("format: %v", s)
	}
	// The v2 schema rejects unknown keys here, so "enabled" must not appear.
	if _, bad := s["enabled"]; bad {
		t.Error(`"enabled" is not in the v2 schema`)
	}
	if s["timeout"].(map[string]any)["execution"].(float64) <= 40000 {
		t.Fatalf("timeout too short for wait_for_task: %v", s["timeout"])
	}
	if !h.HasMCP(root) {
		t.Error("HasMCP is false after writing")
	}
	if _, err := h.RemoveMCP(root, ""); err != nil {
		t.Fatal(err)
	}
	mcp = readDoc(t, path)["mcp"].(map[string]any)
	if mcp["servers"].(map[string]any)["mine"] == nil || mcp["servers"].(map[string]any)[ServerName] != nil {
		t.Fatalf("remove: %v", mcp)
	}
}

func TestOpenCodeJSONCIsNotRewritten(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte("// mine\n{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, _ := Get("opencode")
	_, err := h.WriteMCP(root, entry, "")
	if err == nil || !strings.Contains(err.Error(), `"type": "local"`) || !strings.Contains(err.Error(), "servers") {
		t.Fatalf("expected a snippet to paste: %v", err)
	}
}

func TestTOMLKeepsCommentsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	h, _ := Get("codex")
	path := h.mcpPath(root)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	original := "# my comment\nmodel = \"x\"\n\n[mcp_servers.other]\ncommand = \"y\"\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.WriteMCP(root, Entry{Command: `/odd "path"/swarm`, Args: []string{"mcp"}}, ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	for _, want := range []string{"# my comment", "[mcp_servers.other]", "[mcp_servers.swarm]", `command = "/odd \"path\"/swarm"`, "tool_timeout_sec = 60"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if r, err := h.WriteMCP(root, Entry{Command: `/odd "path"/swarm`, Args: []string{"mcp"}}, ""); err != nil || r.Changed {
		t.Fatalf("not idempotent: %+v %v", r, err)
	}
	if _, err := h.RemoveMCP(root, ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != original {
		t.Fatalf("remove did not restore the file:\n%q\nwant\n%q", b, original)
	}
}

func TestTOMLRefusesAHandWrittenTable(t *testing.T) {
	root := t.TempDir()
	h, _ := Get("codex")
	path := h.mcpPath(root)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte("[mcp_servers.swarm]\ncommand = \"mine\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.WriteMCP(root, entry, ""); err == nil {
		t.Fatal("overwrote a hand-written table")
	}
}

func TestConfigHoldsNoSecrets(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"claude", "opencode", "codex"} {
		h, _ := Get(id)
		if _, err := h.WriteMCP(root, entry, ""); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(h.mcpPath(root))
		if strings.Contains(strings.ToLower(string(b)), "token") || strings.Contains(string(b), "Bearer") {
			t.Errorf("%s config mentions a token:\n%s", id, b)
		}
	}
}

func TestSnippetsMatchWhatWriteMCPProduces(t *testing.T) {
	for _, id := range []string{"claude", "opencode", "codex", "gemini", "cursor"} {
		h, _ := Get(id)
		root := t.TempDir()
		if _, err := h.WriteMCP(root, entry, ""); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(h.mcpPath(root))
		if string(b) != h.Snippet(entry) {
			t.Errorf("%s: pasting the snippet would differ from writing it:\n%s\nvs\n%s", id, b, h.Snippet(entry))
		}
	}
}

func TestCustomServerNameAndConfigPath(t *testing.T) {
	dir := t.TempDir()
	e := Entry{Name: "docs", Command: "docs-server", Args: []string{"--x"}, Env: map[string]string{"K": "v"}}
	for file, format := range map[string]MCPFormat{"a.toml": MCPCodexTOML, "opencode.json": MCPOpenCode, "my.json": MCPServersJSON} {
		path := filepath.Join(dir, file)
		if got := FormatForPath(path); got != format {
			t.Errorf("%s: format %s", file, got)
		}
		h := At(format, path)
		for _, name := range []Entry{e, entry} { // Two servers side by side.
			if _, err := h.WriteMCP(dir, name, ""); err != nil {
				t.Fatal(err)
			}
		}
		if !h.HasNamed(dir, "docs") || !h.HasMCP(dir) {
			t.Errorf("%s: a server is missing", file)
		}
		b, _ := os.ReadFile(path)
		if !strings.Contains(string(b), "docs-server") || !strings.Contains(string(b), `"v"`) {
			t.Errorf("%s:\n%s", file, b)
		}
		if _, err := h.RemoveNamed(dir, "docs", ""); err != nil {
			t.Fatal(err)
		}
		if h.HasNamed(dir, "docs") || !h.HasMCP(dir) {
			t.Errorf("%s: remove took the wrong server", file)
		}
	}
}
