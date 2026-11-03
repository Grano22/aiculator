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
	a := Bubblewrap{}.Args("/w", nil, nil, false, []string{"go", "test"})
	for _, want := range []string{"--unshare-net", "--unshare-pid", "--die-with-parent"} {
		if !slices.Contains(a, want) {
			t.Errorf("missing %s in %v", want, a)
		}
	}
	if slices.Contains(Bubblewrap{}.Args("/w", nil, nil, true, []string{"x"}), "--unshare-net") {
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
	// The default image's entrypoint must not take the command as its arguments.
	i := slices.Index(a, "--entrypoint")
	img := slices.Index(a, "img")
	if i < 0 || i+1 >= len(a) || a[i+1] != "" || img < i {
		t.Errorf("the entrypoint is not cleared before the image: %v", a)
	}
	if !slices.Equal(a[img+1:], []string{"sh"}) {
		t.Errorf("the command should follow the image: %v", a)
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

func TestSeatbeltHidesSecretsFromReads(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	work, data := t.TempDir(), t.TempDir()
	secret := filepath.Join(data, "admin.token")
	if err := os.WriteFile(secret, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Dir: work, Env: BaseEnv(work), Argv: []string{"cat", secret}}
	if out, err := run(t, Seatbelt{}, spec); err != nil || !strings.Contains(out, "hunter2") {
		t.Fatalf("control: unhidden file should be readable: %v %s", err, out)
	}
	spec.Hide = []string{secret}
	if out, err := run(t, Seatbelt{}, spec); err == nil || strings.Contains(out, "hunter2") {
		t.Fatalf("hidden file was read: %s", out)
	}
	// A symlink inside the worktree must not get around it.
	if err := os.Symlink(secret, filepath.Join(work, "door")); err != nil {
		t.Fatal(err)
	}
	spec.Argv = []string{"cat", "door"}
	if out, err := run(t, Seatbelt{}, spec); err == nil || strings.Contains(out, "hunter2") {
		t.Fatalf("hidden file was read through a symlink: %s", out)
	}
}

func TestBubblewrapHidesFilesAndDirectories(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a := strings.Join(Bubblewrap{}.Args("/w", nil, []string{dir, file, "/nonexistent"}, false, []string{"x"}), " ")
	if !strings.Contains(a, "--tmpfs "+dir) || !strings.Contains(a, "--ro-bind /dev/null "+file) || strings.Contains(a, "/nonexistent") {
		t.Fatalf("args: %s", a)
	}
}

func TestBubblewrapKeepsAProgramUnderTmp(t *testing.T) {
	a := strings.Join(Bubblewrap{}.Args("/w", nil, nil, false, []string{"/tmp/x/agent"}), " ")
	if !strings.Contains(a, "--ro-bind /tmp/x/agent /tmp/x/agent") {
		t.Fatalf("args: %s", a)
	}
	if b := strings.Join(Bubblewrap{}.Args("/w", nil, nil, false, []string{"/usr/bin/agent"}), " "); strings.Contains(b, "/usr/bin/agent /usr/bin/agent") {
		t.Fatalf("bound a program that needed no binding: %s", b)
	}
}

// Real attacks on bubblewrap, which run wherever it works, such as in CI. Everything
// sits under the home directory, because /tmp is replaced and would prove nothing.
func TestBubblewrapBlocksEscapes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap is not installed")
	}
	if out, err := exec.Command("bwrap", "--ro-bind", "/", "/", "--unshare-pid", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot run here: %v %s", err, out)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp(home, "bwrap-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	work, outside := filepath.Join(base, "work"), filepath.Join(base, "outside")
	for _, d := range []string{work, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(outside, "admin.token")
	if err := os.WriteFile(secret, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	sh := func(script string, hide ...string) Spec {
		return Spec{Dir: work, Argv: []string{"sh", "-c", script}, Env: BaseEnv(work), Hide: hide}
	}
	b := Bubblewrap{}
	if out, err := run(t, b, sh("echo ok > inside.txt")); err != nil {
		t.Fatalf("write inside was blocked: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(work, "inside.txt")); err != nil {
		t.Fatalf("write inside did not land: %v", err)
	}
	if _, err := run(t, b, sh("echo x > "+filepath.Join(outside, "pwned"))); err == nil {
		t.Error("write outside the worktree succeeded")
	}
	if err := os.Symlink(outside, filepath.Join(work, "door")); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, b, sh("echo x > door/pwned2")); err == nil {
		t.Error("write through a symlink succeeded")
	}
	for _, f := range []string{"pwned", "pwned2"} {
		if _, err := os.Stat(filepath.Join(outside, f)); err == nil {
			t.Errorf("escape file %s exists", f)
		}
	}
	if out, err := run(t, b, sh("cat "+secret)); err != nil || !strings.Contains(out, "hunter2") {
		t.Fatalf("control: an unhidden file should be readable: %v %s", err, out)
	}
	if out, _ := run(t, b, sh("cat "+secret, secret)); strings.Contains(out, "hunter2") {
		t.Errorf("a hidden file was read: %s", out)
	}
	if out, _ := run(t, b, sh("cat "+secret, outside)); strings.Contains(out, "hunter2") {
		t.Errorf("a file in a hidden directory was read: %s", out)
	}
	if _, err := exec.LookPath("curl"); err == nil {
		if out, err := run(t, b, sh("curl -sS --max-time 3 https://1.1.1.1")); err == nil {
			t.Errorf("network call succeeded: %s", out)
		}
	}
}
