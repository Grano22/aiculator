// Command fakeagent is an ACP agent for tests. FAKE_MODE makes it misbehave on
// purpose: slow, crash, zombie, liar, and a few probes that record what the driver
// allowed. It never calls a model, so tests that use it are free and deterministic.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

type agent struct {
	conn *acp.AgentSideConnection
	mode string
	cwd  string
	mcp  []acp.McpServer
	http bool
}

func (a *agent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *agent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{McpCapabilities: acp.McpCapabilities{Http: a.http}},
		AuthMethods:       []acp.AuthMethod{},
	}, nil
}

func (a *agent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}
func (a *agent) Cancel(context.Context, acp.CancelNotification) error { return nil }
func (a *agent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}
func (a *agent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}
func (a *agent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}
func (a *agent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}
func (a *agent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func (a *agent) NewSession(_ context.Context, r acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.cwd, a.mcp = r.Cwd, r.McpServers
	return acp.NewSessionResponse{SessionId: "s1"}, nil
}

func (a *agent) say(ctx context.Context, s string) {
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: "s1", Update: acp.UpdateAgentMessageText(s)})
}

func (a *agent) record(name string, v any) {
	b, _ := json.Marshal(v)
	_ = os.WriteFile(filepath.Join(a.cwd, name), b, 0o644)
}

// writeWork makes a real change, unique to this task, from the "FILE: name" line.
func (a *agent) writeWork(text string) {
	name := "work.txt"
	for _, l := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "FILE: "); ok {
			name = strings.TrimSpace(rest)
		}
	}
	_ = os.WriteFile(filepath.Join(a.cwd, name), []byte("written by "+a.mode+"\n"), 0o644)
}

func (a *agent) ask(ctx context.Context, kind acp.ToolKind, path string) string {
	loc := []acp.ToolCallLocation{}
	if path != "" {
		loc = append(loc, acp.ToolCallLocation{Path: path})
	}
	r, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: "s1",
		ToolCall:  acp.ToolCallUpdate{ToolCallId: "c1", Kind: &kind, Locations: loc},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "allow", OptionId: "yes"},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "reject", OptionId: "no"},
		},
	})
	if err != nil {
		return "error"
	}
	if r.Outcome.Selected != nil {
		return string(r.Outcome.Selected.OptionId)
	}
	return "cancelled"
}

func (a *agent) Prompt(ctx context.Context, r acp.PromptRequest) (acp.PromptResponse, error) {
	var text strings.Builder
	for _, c := range r.Prompt {
		if c.Text != nil {
			text.WriteString(c.Text.Text)
		}
	}
	prompt := text.String()
	endTurn := acp.PromptResponse{StopReason: acp.StopReasonEndTurn, Usage: &acp.Usage{InputTokens: 100, OutputTokens: 23, TotalTokens: 123}}

	if strings.Contains(prompt, "job: review") {
		switch a.mode {
		case "rejecter":
			a.say(ctx, "Looks wrong.\nVERDICT: FAIL\n")
		case "mumbler":
			a.say(ctx, "It seems fine, I guess.\n")
		default:
			a.say(ctx, "Reviewed.\nVERDICT: PASS\n")
		}
		return endTurn, nil
	}

	switch a.mode {
	case "ok", "rejecter", "mumbler":
		a.writeWork(prompt)
		a.say(ctx, "All done.")
		return endTurn, nil
	case "slow":
		// Keeps changing files, so it is slow but clearly not a zombie.
		steps := 6
		if n, err := strconv.Atoi(os.Getenv("FAKE_STEPS")); err == nil {
			steps = n
		}
		for i := range steps {
			_ = os.WriteFile(filepath.Join(a.cwd, "slow.txt"), fmt.Appendf(nil, "%d", i), 0o644)
			select {
			case <-time.After(200 * time.Millisecond):
			case <-ctx.Done():
				return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
			}
		}
		a.writeWork(prompt)
		return endTurn, nil
	case "crash":
		// Partial work is left behind on purpose; the next attempt must inherit it.
		_ = os.WriteFile(filepath.Join(a.cwd, "partial.txt"), []byte("half done\n"), 0o644)
		time.Sleep(50 * time.Millisecond)
		os.Exit(3)
	case "zombie":
		<-ctx.Done()
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	case "liar":
		a.say(ctx, "All tests pass, everything is done, trust me!")
		return endTurn, nil
	case "noauth":
		return acp.PromptResponse{}, &acp.RequestError{Code: -32000, Message: "Authentication required"}
	case "refuse":
		return acp.PromptResponse{StopReason: acp.StopReasonRefusal}, nil
	case "probe":
		a.record("probe.json", map[string]any{
			"mcp":          a.mcp,
			"read":         a.ask(ctx, acp.ToolKindRead, ""),
			"execute":      a.ask(ctx, acp.ToolKindExecute, ""),
			"other":        a.ask(ctx, acp.ToolKindOther, ""),
			"edit_inside":  a.ask(ctx, acp.ToolKindEdit, filepath.Join(a.cwd, "x")),
			"edit_outside": a.ask(ctx, acp.ToolKindEdit, "/etc/passwd"),
		})
		_, inErr := a.conn.WriteTextFile(ctx, acp.WriteTextFileRequest{SessionId: "s1", Path: filepath.Join(a.cwd, "via-acp.txt"), Content: "ok"})
		outside := os.Getenv("FAKE_OUTSIDE")
		_, outErr := a.conn.WriteTextFile(ctx, acp.WriteTextFileRequest{SessionId: "s1", Path: outside, Content: "pwned"})
		_, trvErr := a.conn.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: "s1", Path: "../../../../../../etc/hosts"})
		// The sandbox must stop a direct write too, with the driver not involved.
		directErr := os.WriteFile(outside+".direct", []byte("pwned"), 0o644)
		// Neither may it plant a hook in the repository, which would run as the user.
		hookErr := os.WriteFile(filepath.Join(os.Getenv("FAKE_REPO"), ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\n"), 0o755)
		_, secretErr := os.ReadFile(os.Getenv("FAKE_SECRET"))
		a.record("fs.json", map[string]bool{
			"secret_refused":    secretErr != nil,
			"inside_ok":         inErr == nil,
			"outside_refused":   outErr != nil,
			"traversal_refused": trvErr != nil,
			"direct_refused":    directErr != nil,
			"hook_refused":      hookErr != nil,
		})
		return endTurn, nil
	}
	return acp.PromptResponse{}, fmt.Errorf("unknown mode %q", a.mode)
}

func main() {
	mode := os.Getenv("FAKE_MODE")
	if mode == "" {
		mode = "ok"
	}
	a := &agent{mode: mode, http: os.Getenv("FAKE_MCP_HTTP") == "1"}
	a.conn = acp.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	<-a.conn.Done()
}
