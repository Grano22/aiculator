package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRaiseCommandTimeout(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".claude", "settings.local.json")
	changed, err := raiseCommandTimeout(file, filepath.Join(dir, "backups"))
	if err != nil || !changed {
		t.Fatalf("new file: %v %v", changed, err)
	}
	read := func() map[string]any {
		var m map[string]any
		b, _ := os.ReadFile(file)
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	env := read()["env"].(map[string]any)
	if env["BASH_MAX_TIMEOUT_MS"] != "3600000" || env["BASH_DEFAULT_TIMEOUT_MS"] != "3600000" {
		t.Errorf("env %v", env)
	}
	// Other settings stay, and a higher value is never lowered.
	if err := os.WriteFile(file, []byte(`{"model":"opus","env":{"FOO":"bar","BASH_MAX_TIMEOUT_MS":"7200000"},"permissions":{"deny":["Bash(rm:*)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := raiseCommandTimeout(file, filepath.Join(dir, "backups")); err != nil || !changed {
		t.Fatal(changed, err)
	}
	m := read()
	env = m["env"].(map[string]any)
	if m["model"] != "opus" || m["permissions"] == nil || env["FOO"] != "bar" || env["BASH_MAX_TIMEOUT_MS"] != "7200000" || env["BASH_DEFAULT_TIMEOUT_MS"] != "3600000" {
		t.Errorf("merged %v", m)
	}
	if backups, _ := os.ReadDir(filepath.Join(dir, "backups")); len(backups) != 1 {
		t.Errorf("backups %v", backups)
	}
	if changed, _ := raiseCommandTimeout(file, ""); changed {
		t.Error("changed again with nothing to change")
	}
	// A file that is not JSON is not touched.
	if err := os.WriteFile(file, []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := raiseCommandTimeout(file, ""); err == nil {
		t.Error("rewrote a broken file")
	}
	if b, _ := os.ReadFile(file); string(b) != "{oops" {
		t.Error("the broken file was changed")
	}
}
