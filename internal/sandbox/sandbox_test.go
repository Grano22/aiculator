package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func run(t *testing.T, s Sandbox, spec Spec) (string, error) {
	t.Helper()
	c, err := s.Command(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.CombinedOutput()
	return string(out), err
}

// These tests attack the sandbox for real; each one must be stopped.
func TestSeatbeltBlocksEscapes(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	work, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "pwned")
	env := BaseEnv(work)
	sh := func(script string) Spec {
		return Spec{Dir: work, Argv: []string{"sh", "-c", script}, Env: env}
	}
	s := Seatbelt{}
	if out, err := run(t, s, sh("echo ok > inside.txt")); err != nil {
		t.Fatalf("write inside was blocked: %v %s", err, out)
	}
	if _, err := run(t, s, sh("echo x > "+secret)); err == nil {
		t.Error("write outside the worktree succeeded")
	}
	if _, err := os.Stat(secret); err == nil {
		t.Error("escape file exists")
	}
	// A symlink out of the worktree must not help.
	link := filepath.Join(work, "door")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, s, sh("echo x > door/pwned2")); err == nil {
		t.Error("write through a symlink succeeded")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned2")); err == nil {
		t.Error("symlink escape file exists")
	}
	home, _ := os.UserHomeDir()
	if _, err := run(t, s, sh("touch "+filepath.Join(home, ".swarm-escape-test"))); err == nil {
		os.Remove(filepath.Join(home, ".swarm-escape-test"))
		t.Error("write to the real home succeeded")
	}
}

func TestSeatbeltBlocksNetworkUnlessAllowed(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl missing")
	}
	work := t.TempDir()
	out, err := run(t, Seatbelt{}, Spec{Dir: work, Env: BaseEnv(work), Argv: []string{"curl", "-sS", "--max-time", "3", "https://1.1.1.1"}})
	if err == nil {
		t.Fatalf("network call succeeded: %s", out)
	}
}

func TestEnvironmentIsNotInherited(t *testing.T) {
	t.Setenv("SWARM_TEST_SECRET", "hunter2")
	work := t.TempDir()
	out, err := run(t, None{}, Spec{Dir: work, Argv: []string{"env"}, Env: BaseEnv(work)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "hunter2") {
		t.Fatal("secret leaked into the child")
	}
	out, err = run(t, None{}, Spec{Dir: work, Argv: []string{"env"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "hunter2") {
		t.Fatal("nil env inherited the parent's secrets")
	}
}

// bwrap and docker cannot run on every machine, so their policies are checked as
// arguments. This proves the flags are right, not that isolation holds.
func TestBubblewrapArgs(t *testing.T) {
	a := Bubblewrap{}.Args("/w", nil, false, []string{"go", "test"})
	for _, want := range []string{"--unshare-net", "--unshare-pid", "--die-with-parent"} {
		if !slices.Contains(a, want) {
			t.Errorf("missing %s in %v", want, a)
		}
	}
	if slices.Contains(Bubblewrap{}.Args("/w", nil, true, []string{"x"}), "--unshare-net") {
		t.Error("network requested but still unshared")
	}
}

func TestDockerArgs(t *testing.T) {
	a := Docker{Image: "img"}.Args("/w", nil, false, []string{"PATH=/secret", "HOME=/w"}, []string{"sh"})
	for _, want := range []string{"--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges"} {
		if !slices.Contains(a, want) {
			t.Errorf("missing %s in %v", want, a)
		}
	}
	if slices.Contains(a, "PATH=/secret") {
		t.Error("host PATH passed into the container")
	}
}

func TestOpenRefusesSilentNoSandbox(t *testing.T) {
	if s, err := Open("auto"); err == nil && s.Name() == "none" {
		t.Fatal("auto fell back to no sandbox")
	}
	if _, err := Open("bogus"); err == nil {
		t.Fatal("unknown sandbox accepted")
	}
}
