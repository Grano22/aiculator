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
	Command string
	Args    []string
}

// toolTimeoutMs leaves room above the longest wait_for_task hold.
const toolTimeoutMs = 60000

// Result reports what a config change did.
type Result struct {
	Path    string
	Changed bool
}

func (h Harness) mcpPath(root string) string { return filepath.Join(root, h.MCPFile) }

// WriteMCP registers the swarm's server in the harness's project config. Other
// settings are preserved, and the previous file is copied to backupDir first.
func (h Harness) WriteMCP(root string, e Entry, backupDir string) (Result, error) {
	switch h.MCP {
	case MCPServersJSON:
		return h.writeJSON(root, "mcpServers", map[string]any{
			"type": "stdio", "command": e.Command, "args": e.Args,
			"timeout": toolTimeoutMs, "tools": []string{"*"},
		}, backupDir)
	case MCPOpenCode:
		if _, err := os.Stat(filepath.Join(root, "opencode.jsonc")); err == nil {
			return Result{}, fmt.Errorf("opencode.jsonc may hold comments that rewriting would lose; add this to its \"mcp\" object by hand:\n%s", openCodeSnippet(e))
		}
		return h.writeJSON(root, "mcp", openCodeEntry(e), backupDir)
	case MCPCodexTOML:
		return h.writeTOML(root, e, backupDir)
	}
	return Result{}, nil
}

func openCodeEntry(e Entry) map[string]any {
	return map[string]any{
		"type": "local", "command": append([]string{e.Command}, e.Args...),
		"enabled": true, "timeout": toolTimeoutMs,
	}
}

func openCodeSnippet(e Entry) string {
	b, _ := json.MarshalIndent(map[string]any{ServerName: openCodeEntry(e)}, "", "  ")
	return string(b)
}

// RemoveMCP removes the swarm's server and nothing else.
func (h Harness) RemoveMCP(root, backupDir string) (Result, error) {
	switch h.MCP {
	case MCPServersJSON:
		return h.removeJSON(root, "mcpServers", backupDir)
	case MCPOpenCode:
		return h.removeJSON(root, "mcp", backupDir)
	case MCPCodexTOML:
		return h.removeTOML(root, backupDir)
	}
	return Result{}, nil
}

// HasMCP reports whether the swarm's server is registered.
func (h Harness) HasMCP(root string) bool {
	b, err := os.ReadFile(h.mcpPath(root))
	if err != nil {
		return false
	}
	switch h.MCP {
	case MCPServersJSON, MCPOpenCode:
		key := "mcpServers"
		if h.MCP == MCPOpenCode {
			key = "mcp"
		}
		var doc map[string]any
		if json.Unmarshal(b, &doc) != nil {
			return false
		}
		servers, _ := doc[key].(map[string]any)
		_, ok := servers[ServerName]
		return ok
	case MCPCodexTOML:
		return tomlTable.Match(b)
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

func (h Harness) writeJSON(root, key string, entry map[string]any, backupDir string) (Result, error) {
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
	servers[ServerName] = entry
	doc[key] = servers
	data, err := encodeJSON(doc)
	if err != nil {
		return Result{}, err
	}
	return commit(path, old, data, backupDir)
}

func (h Harness) removeJSON(root, key, backupDir string) (Result, error) {
	path := h.mcpPath(root)
	doc, old, err := readJSON(path)
	if err != nil || old == nil {
		return Result{Path: path}, err
	}
	servers, _ := doc[key].(map[string]any)
	if _, ok := servers[ServerName]; !ok {
		return Result{Path: path}, nil
	}
	delete(servers, ServerName)
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

const (
	tomlBegin = "# BEGIN swarm (managed by swarm join; do not edit)"
	tomlEnd   = "# END swarm"
)

var (
	tomlTable = regexp.MustCompile(`(?m)^\[mcp_servers\.swarm\]`)
	tomlBlock = regexp.MustCompile(`(?s)\n?` + regexp.QuoteMeta(tomlBegin) + `.*?` + regexp.QuoteMeta(tomlEnd) + `\n?`)
)

// tomlString writes a TOML basic string, whose escapes differ from Go's for
// control characters.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// writeTOML works on text inside clearly marked lines, so the rest of the file
// keeps its exact comments and formatting without needing a TOML library.
func (h Harness) writeTOML(root string, e Entry, backupDir string) (Result, error) {
	path := h.mcpPath(root)
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	rest := old
	if tomlBlock.Match(old) {
		rest = tomlBlock.ReplaceAll(old, []byte("\n"))
	}
	if tomlTable.Match(rest) {
		return Result{}, fmt.Errorf("%s already defines [mcp_servers.swarm] by hand, so it was left alone", path)
	}
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		args[i] = tomlString(a)
	}
	block := fmt.Sprintf("%s\n[mcp_servers.swarm]\ncommand = %s\nargs = [%s]\ntool_timeout_sec = %d\n%s\n",
		tomlBegin, tomlString(e.Command), strings.Join(args, ", "), toolTimeoutMs/1000, tomlEnd)
	body := bytes.TrimRight(rest, "\n")
	var data []byte
	if len(body) > 0 {
		data = append(append(slices.Clone(body), "\n\n"...), block...)
	} else {
		data = []byte(block)
	}
	return commit(path, old, data, backupDir)
}

func (h Harness) removeTOML(root, backupDir string) (Result, error) {
	path := h.mcpPath(root)
	old, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Result{Path: path}, nil
	}
	if err != nil {
		return Result{}, err
	}
	if !tomlBlock.Match(old) {
		return Result{Path: path}, nil
	}
	body := bytes.TrimRight(tomlBlock.ReplaceAll(old, []byte("\n")), "\n")
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
