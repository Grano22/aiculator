// Package procinfo describes the process an agent runs in, for the session it opens, and
// stops processes that aiculator started, when asked to.
package procinfo

import (
	"os"
	"runtime"

	"github.com/Grano22/aiculator/pkg/observe"
)

// Self describes this process. kind is how it connects, harness what it runs, and killable
// whether stopping the agent may end this process: true for a driver, which runs headless,
// and false for anything a person may be using.
func Self(kind, harness, client, version string, killable bool) observe.SessionInfo {
	info := observe.SessionInfo{Kind: kind, Harness: harness, Client: client, ClientVersion: version,
		OS: runtime.GOOS + "/" + runtime.GOARCH, PID: os.Getpid(), ParentPID: os.Getppid(), Killable: killable}
	info.Host, _ = os.Hostname()
	info.CWD, _ = os.Getwd()
	info.Parent = name(info.ParentPID)
	for _, k := range []string{"AICULATOR_MODEL", "ANTHROPIC_MODEL", "CLAUDE_MODEL", "OPENAI_MODEL", "OPENCODE_MODEL"} {
		if v := os.Getenv(k); v != "" {
			info.Model = v
			break
		}
	}
	return info
}

// Host is this machine's name, as Self reports it.
func Host() string {
	h, _ := os.Hostname()
	return h
}
