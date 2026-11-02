package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ServerName is the name the swarm's MCP server is registered under.
const ServerName = "swarm"

// Entry is the command that starts the MCP bridge. It holds no secrets: the bridge
// gets its token locally when it starts, so these files are safe to commit.
type Entry struct {
	Name    string // Server name; empty means the swarm's own.
	Command string
	Args    []string
	Env     map[string]string
}

func (e Entry) name() string {
	if e.Name == "" {
		return ServerName
	}
	return e.Name
}

// toolTimeoutMs leaves room above the longest wait_for_task hold.
const toolTimeoutMs = 60000

// Result reports what a config change did.
type Result struct {
	Path    string
	Changed bool
}

func (h Harness) mcpPath(root string) string {
	if filepath.IsAbs(h.MCPFile) {
		return h.MCPFile
	}
	return filepath.Join(root, h.MCPFile)
}

// At returns the same format pointed at a specific config file.
func At(format MCPFormat, path string) Harness {
	return Harness{ID: "custom", MCP: format, MCPFile: path}
}

// FormatForPath infers a config format from a file name.
func FormatForPath(path string) MCPFormat {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(base, ".toml"):
		return MCPCodexTOML
	case strings.HasPrefix(base, "opencode"):
		return MCPOpenCode
	}
	return MCPServersJSON
}

func jsonEntry(e Entry) map[string]any {
	m := map[string]any{
		"type": "stdio", "command": e.Command, "args": e.Args,
		"timeout": toolTimeoutMs, "tools": []string{"*"},
	}
	if len(e.Env) > 0 {
		m["env"] = e.Env
	}
	return m
}

// Snippet returns the config piece to paste by hand, in this harness's format.
func (h Harness) Snippet(e Entry) string {
	name := e.name()
	switch h.MCP {
	case MCPOpenCode:
		b, _ := encodeJSON(map[string]any{"mcp": map[string]any{"servers": map[string]any{name: openCodeEntry(e)}}})
		return string(b)
	case MCPCodexTOML:
		return tomlBlockFor(e)
	}
	b, _ := encodeJSON(map[string]any{"mcpServers": map[string]any{name: jsonEntry(e)}})
	return string(b)
}

// WriteMCP registers the swarm's server in the harness's project config. Other
// settings are preserved, and the previous file is copied to backupDir first.
func (h Harness) WriteMCP(root string, e Entry, backupDir string) (Result, error) {
	switch h.MCP {
	case MCPServersJSON:
		return h.writeJSON(root, "mcpServers", e.name(), jsonEntry(e), backupDir)
	case MCPOpenCode:
		if _, err := os.Stat(filepath.Join(filepath.Dir(h.mcpPath(root)), "opencode.jsonc")); err == nil {
			return Result{}, fmt.Errorf("opencode.jsonc may hold comments that rewriting would lose; add this by hand:\n%s", h.Snippet(e))
		}
		return h.writeOpenCode(root, e, backupDir)
	case MCPCodexTOML:
		return h.writeTOML(root, e, backupDir)
	}
	return Result{}, nil
}

// openCodeEntry uses the v2 shape, the one opencode mcp add writes in 2.0.32. Its docs
// page still shows the deprecated v1 shape, with "enabled" and a numeric timeout.
func openCodeEntry(e Entry) map[string]any {
	m := map[string]any{
		"type": "local", "command": append([]string{e.Command}, e.Args...),
		"timeout": map[string]any{"execution": toolTimeoutMs},
	}
	if len(e.Env) > 0 {
		m["environment"] = e.Env
	}
	return m
}

func (h Harness) writeOpenCode(root string, e Entry, backupDir string) (Result, error) {
	path := h.mcpPath(root)
	doc, old, err := readJSON(path)
	if err != nil {
		return Result{}, err
	}
	mcp, ok := doc["mcp"].(map[string]any)
	if !ok {
		if doc["mcp"] != nil {
			return Result{}, fmt.Errorf("%s: \"mcp\" is not an object, so the file was left alone", path)
		}
		mcp = map[string]any{}
	}
	servers, ok := mcp["servers"].(map[string]any)
	if !ok {
		if mcp["servers"] != nil {
			return Result{}, fmt.Errorf("%s: \"mcp.servers\" is not an object, so the file was left alone", path)
		}
		servers = map[string]any{}
	}
	delete(mcp, e.name()) // An entry in the old shape must not linger beside the new one.
	servers[e.name()] = openCodeEntry(e)
	mcp["servers"] = servers
	doc["mcp"] = mcp
	data, err := encodeJSON(doc)
	if err != nil {
		return Result{}, err
	}
	return commit(path, old, data, backupDir)
}

func (h Harness) removeOpenCode(root, name, backupDir string) (Result, error) {
	path := h.mcpPath(root)
	doc, old, err := readJSON(path)
	if err != nil || old == nil {
		return Result{Path: path}, err
	}
	mcp, _ := doc["mcp"].(map[string]any)
	servers, _ := mcp["servers"].(map[string]any)
	_, inServers := servers[name]
	_, inOld := mcp[name]
	if !inServers && !inOld {
		return Result{Path: path}, nil
	}
	delete(servers, name)
	delete(mcp, name)
	if len(servers) == 0 {
		delete(mcp, "servers")
	}
	if len(mcp) == 0 {
		delete(doc, "mcp")
	}
	if len(doc) == 0 {
		if err := backup(path, old, backupDir); err != nil {
			return Result{}, err
		}
		return Result{Path: path, Changed: true}, os.Remove(path)
	}
	data, err := encodeJSON(doc)
	if err != nil {
		return Result{}, err
	}
	return commit(path, old, data, backupDir)
}

// RemoveMCP removes the swarm's server and nothing else.
func (h Harness) RemoveMCP(root, backupDir string) (Result, error) {
	return h.RemoveNamed(root, ServerName, backupDir)
}

// RemoveNamed removes one named server and nothing else.
func (h Harness) RemoveNamed(root, name, backupDir string) (Result, error) {
	switch h.MCP {
	case MCPServersJSON:
		return h.removeJSON(root, "mcpServers", name, backupDir)
	case MCPOpenCode:
		return h.removeOpenCode(root, name, backupDir)
	case MCPCodexTOML:
		return h.removeTOML(root, name, backupDir)
	}
	return Result{}, nil
}

// HasMCP reports whether the swarm's server is registered.
func (h Harness) HasMCP(root string) bool { return h.HasNamed(root, ServerName) }

// HasNamed reports whether a named server is registered.
func (h Harness) HasNamed(root, name string) bool {
	b, err := os.ReadFile(h.mcpPath(root))
	if err != nil {
		return false
	}
	switch h.MCP {
	case MCPServersJSON, MCPOpenCode:
		var doc map[string]any
		if json.Unmarshal(b, &doc) != nil {
			return false
		}
		servers, _ := doc["mcpServers"].(map[string]any)
		if h.MCP == MCPOpenCode {
			mcp, _ := doc["mcp"].(map[string]any)
			servers, _ = mcp["servers"].(map[string]any)
		}
		_, ok := servers[name]
		return ok
	case MCPCodexTOML:
		return tomlTable(name).Match(b)
	}
	return false
}

func readJSON(path string) (map[string]any, []byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	doc := map[string]any{}
	if len(bytes.TrimSpace(b)) == 0 {
		return doc, b, nil
	}
	// Never rewrite a file that cannot be understood: that would destroy it.
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, fmt.Errorf("%s is not valid JSON, so it was left alone: %w", path, err)
	}
	return doc, b, nil
}

func encodeJSON(doc map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (h Harness) writeJSON(root, key, name string, entry map[string]any, backupDir string) (Result, error) {
	path := h.mcpPath(root)
	doc, old, err := readJSON(path)
	if err != nil {
		return Result{}, err
	}
	var servers map[string]any
	switch v := doc[key].(type) {
	case nil:
		servers = map[string]any{}
	case map[string]any:
		servers = v
	default:
		return Result{}, fmt.Errorf("%s: %q is not an object, so the file was left alone", path, key)
	}
	servers[name] = entry
	doc[key] = servers
	data, err := encodeJSON(doc)
	if err != nil {
		return Result{}, err
	}
	return commit(path, old, data, backupDir)
}

func (h Harness) removeJSON(root, key, name, backupDir string) (Result, error) {
	path := h.mcpPath(root)
	doc, old, err := readJSON(path)
	if err != nil || old == nil {
		return Result{Path: path}, err
	}
	servers, _ := doc[key].(map[string]any)
	if _, ok := servers[name]; !ok {
		return Result{Path: path}, nil
	}
	delete(servers, name)
	if len(servers) == 0 {
		delete(doc, key)
	}
	if len(doc) == 0 { // Nothing else was in it, so the swarm made this file.
		if err := backup(path, old, backupDir); err != nil {
			return Result{}, err
		}
		return Result{Path: path, Changed: true}, os.Remove(path)
	}
	data, err := encodeJSON(doc)
	if err != nil {
		return Result{}, err
	}
	return commit(path, old, data, backupDir)
}

func tomlBegin(name string) string { return "# BEGIN " + name + " (managed by aiculator; do not edit)" }
func tomlEnd(name string) string   { return "# END " + name }

func tomlTable(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^\[mcp_servers\.` + regexp.QuoteMeta(name) + `\]`)
}

func tomlBlockRE(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)\n?` + regexp.QuoteMeta(tomlBegin(name)) + `.*?` + regexp.QuoteMeta(tomlEnd(name)) + `\n?`)
}

// tomlString writes a TOML basic string, whose escapes differ from Go's for
// control characters.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

func tomlBlockFor(e Entry) string {
	name := e.name()
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		args[i] = tomlString(a)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n[mcp_servers.%s]\ncommand = %s\nargs = [%s]\ntool_timeout_sec = %d\n",
		tomlBegin(name), name, tomlString(e.Command), strings.Join(args, ", "), toolTimeoutMs/1000)
	if len(e.Env) > 0 {
		fmt.Fprintf(&b, "\n[mcp_servers.%s.env]\n", name)
		keys := make([]string, 0, len(e.Env))
		for k := range e.Env {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s = %s\n", k, tomlString(e.Env[k]))
		}
	}
	b.WriteString(tomlEnd(name) + "\n")
	return b.String()
}

// writeTOML works on text inside clearly marked lines, so the rest of the file
// keeps its exact comments and formatting without needing a TOML library.
func (h Harness) writeTOML(root string, e Entry, backupDir string) (Result, error) {
	path := h.mcpPath(root)
	name := e.name()
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	rest := old
	if re := tomlBlockRE(name); re.Match(old) {
		rest = re.ReplaceAll(old, []byte("\n"))
	}
	if tomlTable(name).Match(rest) {
		return Result{}, fmt.Errorf("%s already defines [mcp_servers.%s] by hand, so it was left alone", path, name)
	}
	block := tomlBlockFor(e)
	body := bytes.TrimRight(rest, "\n")
	var data []byte
	if len(body) > 0 {
		data = append(append(slices.Clone(body), "\n\n"...), block...)
	} else {
		data = []byte(block)
	}
	return commit(path, old, data, backupDir)
}

func (h Harness) removeTOML(root, name, backupDir string) (Result, error) {
	path := h.mcpPath(root)
	old, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Result{Path: path}, nil
	}
	if err != nil {
		return Result{}, err
	}
	re := tomlBlockRE(name)
	if !re.Match(old) {
		return Result{Path: path}, nil
	}
	body := bytes.TrimRight(re.ReplaceAll(old, []byte("\n")), "\n")
	if len(bytes.TrimSpace(body)) == 0 {
		if err := backup(path, old, backupDir); err != nil {
			return Result{}, err
		}
		return Result{Path: path, Changed: true}, os.Remove(path)
	}
	return commit(path, old, append(slices.Clone(body), '\n'), backupDir)
}

func commit(path string, old, data []byte, backupDir string) (Result, error) {
	if bytes.Equal(old, data) {
		return Result{Path: path}, nil
	}
	if err := backup(path, old, backupDir); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := path + ".swarm-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return Result{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return Result{}, err
	}
	return Result{Path: path, Changed: true}, nil
}

func backup(path string, old []byte, dir string) error {
	if old == nil || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := strings.ReplaceAll(strings.TrimLeft(path, "/"), "/", "_")
	stamp := time.Now().Format("20060102-150405.000000000")
	return os.WriteFile(filepath.Join(dir, name+"."+stamp), old, 0o600)
}
