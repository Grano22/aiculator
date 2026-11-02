// Package harness is the registry of supported coding agents: how to launch each
// one over ACP, where it looks for skills, and how to give it the swarm's MCP tools.
//
// Confidence is recorded per entry, because these paths change often and a wrong
// one fails silently. Update Source when you re-verify an entry.
package harness

import (
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Source says how an entry's facts were established.
type Source string

const (
	// SourceDocs means the skill and MCP facts come from the harness's own docs.
	SourceDocs Source = "docs"
	// SourceTable means the skill path comes from the vercel-labs/skills agent
	// table and the ACP command from the ACP registry, not from the harness's docs.
	SourceTable Source = "table"
)

// MCPFormat is how a harness stores MCP servers.
type MCPFormat string

const (
	// MCPNone means there is no project config to write; use the shell route.
	MCPNone MCPFormat = ""
	// MCPServersJSON is a JSON file with an "mcpServers" object.
	MCPServersJSON MCPFormat = "mcpServers-json"
	// MCPOpenCode is opencode.json with an "mcp" object.
	MCPOpenCode MCPFormat = "opencode-json"
	// MCPCodexTOML is a TOML file with [mcp_servers.NAME] tables.
	MCPCodexTOML MCPFormat = "codex-toml"
)

// Harness describes one coding agent.
type Harness struct {
	ID   string
	Name string
	// Bins are executables that show the harness is installed.
	Bins []string
	// ACP launches the harness as an ACP agent. Empty means it has no ACP mode.
	// npx commands are pinned to the version in the ACP registry on purpose, so a
	// new upstream release cannot change what runs without review.
	ACP []string
	// PassEnv names API key variables the driver forwards. Nothing else is inherited.
	PassEnv []string
	// Env sets extra variables for the driver; {private} is the session's temp directory.
	Env []string
	// StateDirs are writable from the sandbox, for the harness's login and state.
	StateDirs []string
	// SkillsDir is the project skill directory, relative to the project root.
	SkillsDir string
	// GlobalSkillsDir is the user skill directory, starting with ~/.
	GlobalSkillsDir string
	// ReadsAgents means it scans <project>/.agents/skills, so it needs no link.
	ReadsAgents bool
	// ReadsGlobalAgents means it scans ~/.agents/skills.
	ReadsGlobalAgents bool
	MCP               MCPFormat
	MCPFile           string
	Source            Source
	// Note is shown to the user when joining.
	Note string
}

// All returns the registry, sorted by ID.
func All() []Harness {
	out := slices.Clone(registry)
	slices.SortFunc(out, func(a, b Harness) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// Get finds a harness by ID.
func Get(id string) (Harness, bool) {
	for _, h := range registry {
		if h.ID == id {
			return h, true
		}
	}
	return Harness{}, false
}

// Installed reports whether one of the harness's executables is on PATH.
func (h Harness) Installed() bool {
	for _, b := range h.Bins {
		if _, err := exec.LookPath(b); err == nil {
			return true
		}
	}
	return false
}

// Detected returns the harnesses installed on this machine.
func Detected() []Harness {
	var out []Harness
	for _, h := range All() {
		if h.Installed() {
			out = append(out, h)
		}
	}
	return out
}

// ExpandHome turns a leading ~/ into the user's home directory.
func ExpandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}

const npm = "~/.npm"

var registry = []Harness{
	{
		ID: "claude", Name: "Claude Code", Bins: []string{"claude"},
		ACP:       []string{"npx", "-y", "@agentclientprotocol/claude-agent-acp@0.100.0"},
		PassEnv:   []string{"ANTHROPIC_API_KEY"},
		Env:       []string{"CLAUDE_CODE_TMPDIR={private}"},
		StateDirs: []string{"~/.claude", "~/.claude.json", npm},
		SkillsDir: ".claude/skills", GlobalSkillsDir: "~/.claude/skills",
		MCP: MCPServersJSON, MCPFile: ".mcp.json", Source: SourceDocs,
		Note: "Claude Code does not read .agents/skills, so it gets a link. It asks you to approve .mcp.json in interactive sessions.",
	},
	{
		ID: "codex", Name: "Codex CLI", Bins: []string{"codex"},
		ACP:       []string{"npx", "-y", "@agentclientprotocol/codex-acp@2.5.0"},
		PassEnv:   []string{"OPENAI_API_KEY"},
		StateDirs: []string{"~/.codex", npm},
		SkillsDir: ".agents/skills", GlobalSkillsDir: "~/.agents/skills", ReadsAgents: true, ReadsGlobalAgents: true,
		MCP: MCPCodexTOML, MCPFile: ".codex/config.toml", Source: SourceDocs,
		Note: "Codex reads .codex/config.toml only in projects you have marked as trusted.",
	},
	{
		ID: "gemini", Name: "Gemini CLI", Bins: []string{"gemini"},
		ACP:       []string{"npx", "-y", "@google/gemini-cli@0.66.0", "--acp"},
		PassEnv:   []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		StateDirs: []string{"~/.gemini", npm},
		SkillsDir: ".gemini/skills", GlobalSkillsDir: "~/.gemini/skills", ReadsAgents: true, ReadsGlobalAgents: true,
		MCP: MCPServersJSON, MCPFile: ".gemini/settings.json", Source: SourceDocs,
	},
	{
		ID: "opencode", Name: "OpenCode", Bins: []string{"opencode"},
		ACP:       []string{"opencode", "acp"},
		PassEnv:   []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY"},
		StateDirs: []string{"~/.config/opencode", "~/.local/share/opencode", "~/.local/state/opencode", "~/.cache/opencode"},
		SkillsDir: ".opencode/skills", GlobalSkillsDir: "~/.config/opencode/skills", ReadsAgents: true, ReadsGlobalAgents: true,
		MCP: MCPOpenCode, MCPFile: "opencode.json", Source: SourceDocs,
		Note: "OpenCode's default MCP timeout is 30 seconds, so the config sets 60.",
	},
	{
		ID: "pi", Name: "Pi", Bins: []string{"pi"},
		ACP:       []string{"npx", "-y", "pi-acp@0.0.34"},
		PassEnv:   []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY"},
		StateDirs: []string{"~/.pi", npm},
		SkillsDir: ".pi/skills", GlobalSkillsDir: "~/.pi/agent/skills", ReadsAgents: true, ReadsGlobalAgents: true,
		Source: SourceDocs,
		Note:   "Pi has no built-in MCP, so it joins through the swarm work shell commands.",
	},
	{
		ID: "cursor", Name: "Cursor CLI", Bins: []string{"cursor-agent", "cursor"},
		ACP:       []string{"cursor-agent", "acp"},
		StateDirs: []string{"~/.cursor"},
		SkillsDir: ".cursor/skills", GlobalSkillsDir: "~/.cursor/skills", ReadsAgents: true, ReadsGlobalAgents: true,
		MCP: MCPServersJSON, MCPFile: ".cursor/mcp.json", Source: SourceDocs,
	},
	{
		ID: "copilot", Name: "GitHub Copilot CLI", Bins: []string{"copilot"},
		ACP:       []string{"npx", "-y", "@github/copilot@1.0.105", "--acp"},
		PassEnv:   []string{"GH_TOKEN", "GITHUB_TOKEN"},
		StateDirs: []string{"~/.copilot", npm},
		SkillsDir: ".github/skills", GlobalSkillsDir: "~/.copilot/skills", ReadsAgents: true, ReadsGlobalAgents: true,
		MCP: MCPServersJSON, MCPFile: ".mcp.json", Source: SourceDocs,
		Note: "Copilot loads project MCP servers only after you confirm folder trust.",
	},
	{
		ID: "goose", Name: "goose", Bins: []string{"goose"},
		ACP:       []string{"goose", "acp"},
		PassEnv:   []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY"},
		StateDirs: []string{"~/.config/goose", "~/.local/share/goose", "~/.local/state/goose"},
		SkillsDir: ".goose/skills", GlobalSkillsDir: "~/.agents/skills", ReadsAgents: true, ReadsGlobalAgents: true,
		Source: SourceDocs,
		Note:   "goose keeps MCP extensions in a user-wide file, which the swarm does not edit, so it joins through the shell commands.",
	},

	// The entries below come from the ACP registry and the vercel-labs/skills table.
	// They join through the shell route, because their MCP files are not confirmed.
	table("amp", "Amp", []string{"amp"}, []string{"amp-acp"}, ".agents/skills", "~/.config/agents/skills", true),
	table("auggie", "Auggie", []string{"auggie"}, []string{"npx", "-y", "@augmentcode/auggie@0.36.0", "--acp"}, ".augment/skills", "~/.augment/skills", false),
	table("cline", "Cline", []string{"cline"}, []string{"npx", "-y", "cline@3.0.74", "--acp"}, ".agents/skills", "~/.agents/skills", true),
	table("devin", "Devin", []string{"devin"}, []string{"devin", "acp"}, ".devin/skills", "~/.config/devin/skills", false),
	table("droid", "Factory Droid", []string{"droid"}, []string{"npx", "-y", "droid@1.1.0", "exec", "--output-format", "acp-daemon"}, ".factory/skills", "~/.factory/skills", false),
	table("grok", "Grok Build", []string{"grok"}, []string{"npx", "-y", "@xai-official/grok@1.0.73", "agent", "stdio"}, ".grok/skills", "~/.grok/skills", false),
	table("junie", "Junie", []string{"junie"}, []string{"junie", "--acp=true"}, ".junie/skills", "~/.junie/skills", false),
	table("kilo", "Kilo", []string{"kilo", "kilocode"}, []string{"npx", "-y", "@kilocode/cli@7.8.18", "acp"}, ".agents/skills", "~/.kilo/skills", true),
	table("kimi", "Kimi CLI", []string{"kimi"}, []string{"kimi", "acp"}, ".agents/skills", "~/.agents/skills", true),
	table("kiro", "Kiro CLI", []string{"kiro-cli"}, []string{"kiro-cli", "acp", "--agent-engine=v3"}, ".kiro/skills", "~/.kiro/skills", false),
	table("qwen", "Qwen Code", []string{"qwen"}, []string{"npx", "-y", "@qwen-code/qwen-code@0.26.5", "--acp", "--experimental-skills"}, ".qwen/skills", "~/.qwen/skills", false),
	table("vibe", "Mistral Vibe", []string{"vibe"}, []string{"vibe-acp"}, ".vibe/skills", "~/.vibe/skills", false),

	// These have no ACP mode in the registry: skills and the shell route only.
	table("continue", "Continue", []string{"cn"}, nil, ".continue/skills", "~/.continue/skills", false),
	table("crush", "Crush", []string{"crush"}, nil, ".crush/skills", "~/.config/crush/skills", false),
	table("roo", "Roo Code", nil, nil, ".roo/skills", "~/.roo/skills", false),
	table("windsurf", "Windsurf", []string{"windsurf"}, nil, ".windsurf/skills", "~/.codeium/windsurf/skills", false),
}

func table(id, name string, bins, acp []string, skills, global string, readsAgents bool) Harness {
	state := []string{"~/." + id}
	if len(acp) > 0 && acp[0] == "npx" {
		state = append(state, npm)
	}
	return Harness{
		ID: id, Name: name, Bins: bins, ACP: acp, StateDirs: state,
		SkillsDir: skills, GlobalSkillsDir: global, ReadsAgents: readsAgents,
		ReadsGlobalAgents: global == "~/.agents/skills", Source: SourceTable,
	}
}
