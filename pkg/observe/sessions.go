package observe

import "time"

// SessionInfo is what a connecting agent says about itself: what kind of connection it is,
// what runs it, and where. It helps a person find the process behind an agent name. It is
// reported by the agent, so it is shown, never trusted for any decision.
type SessionInfo struct {
	// Kind is how it connects: acp (aiculator drive), mcp (the stdio bridge), mcp-http (a
	// harness talking to the server directly) or shell (aiculator work).
	Kind    string `json:"kind"`
	Harness string `json:"harness,omitempty"`
	Model   string `json:"model,omitempty"`
	// Client and ClientVersion name the program that connected.
	Client        string `json:"client,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
	Host          string `json:"host,omitempty"`
	OS            string `json:"os,omitempty"`
	CWD           string `json:"cwd,omitempty"`
	PID           int    `json:"pid,omitempty"`
	// Parent is the process that started it, usually the harness, by name and id.
	Parent    string `json:"parent,omitempty"`
	ParentPID int    `json:"parent_pid,omitempty"`
	// Killable says the process is aiculator's own and can be stopped on request: a driver
	// or a bridge, never the harness a person is typing into.
	Killable bool `json:"killable,omitempty"`
}

// SessionLive is one connected agent, with what it said about itself.
type SessionLive struct {
	Agent string `json:"agent"`
	Tier  int    `json:"tier"`
	SessionInfo
	ConnectedMs int64 `json:"connected_ms"`
	SeenAgoMs   int64 `json:"seen_ago_ms"`
	// State is working, waiting, idle, limited, stopping or gone.
	State string `json:"state"`
	Task  string `json:"task,omitempty"`
	// Limit says why the agent cannot work now, such as usage_limit, and until when.
	Limit      string    `json:"limit,omitempty"`
	LimitUntil time.Time `json:"limit_until,omitzero"`
	// HoldMs is how long its waits are held now, learned from its harness's tool timeout.
	HoldMs int64 `json:"hold_ms,omitempty"`
}
