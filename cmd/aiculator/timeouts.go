package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Grano22/aiculator/internal/harness"
)

// commandTimeout is what init offers to let a shell command run in Claude Code, so that
// aiculator work next can wait an hour per call instead of two minutes. It is a ceiling, not a
// wait: every other command still ends when it is done.
const commandTimeout = time.Hour

// Claude Code reads these from the env block of its settings. They are its own settings,
// written up by its users rather than documented by its makers, so nothing in the swarm relies
// on them: work next learns the real limit either way, and only waits less without them.
var claudeTimeoutVars = []string{"BASH_DEFAULT_TIMEOUT_MS", "BASH_MAX_TIMEOUT_MS"}

// claudeSettingsFile is where the timeout goes: the user's settings for every project, or the
// project's local settings, which are not committed, for one.
func claudeSettingsFile(global bool, root string) string {
	if global {
		return harness.ExpandHome("~/.claude/settings.json")
	}
	return filepath.Join(root, ".claude", "settings.local.json")
}

// raiseCommandTimeout sets Claude Code's command timeouts, keeping everything else in the
// file, and never lowering a value already higher. A file that is not valid JSON is left alone.
func raiseCommandTimeout(file, backups string) (bool, error) {
	settings := map[string]json.RawMessage{}
	b, err := os.ReadFile(file)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &settings); err != nil {
			return false, fmt.Errorf("%s is not valid JSON, so it was left alone: %w", file, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return false, err
	}
	env := map[string]string{}
	if raw, ok := settings["env"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil {
			return false, fmt.Errorf("%s has an env block that is not text values, so it was left alone", file)
		}
	}
	want := commandTimeout.Milliseconds()
	changed := false
	for _, k := range claudeTimeoutVars {
		if have, err := strconv.ParseInt(env[k], 10, 64); err == nil && have >= want {
			continue
		}
		env[k] = strconv.FormatInt(want, 10)
		changed = true
	}
	if !changed {
		return false, nil
	}
	if len(b) > 0 && backups != "" {
		if err := os.MkdirAll(backups, 0o700); err == nil {
			_ = os.WriteFile(filepath.Join(backups, fmt.Sprintf("claude-settings-%d.json", time.Now().Unix())), b, 0o600)
		}
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return false, err
	}
	settings["env"] = raw
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return false, err
	}
	return true, atomicWrite(file, append(out, '\n'), 0o644)
}
