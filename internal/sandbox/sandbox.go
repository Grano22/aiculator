// Package sandbox runs commands with restricted file and network access. Running
// unsandboxed must be asked for by name; there is no silent fallback to it.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Grano22/aiculator/internal/vlog"
)

// Spec describes one sandboxed command.
type Spec struct {
	Dir        string   // Working directory; always writable.
	Argv       []string // Command and arguments.
	WritePaths []string // Extra writable paths.
	Network    bool     // Whether network access is allowed.
	Hide       []string // Paths the command may not read at all, such as secrets.
	Env        []string // Complete environment. The parent's is never inherited.
}

// Sandbox builds a command that runs under some isolation.
type Sandbox interface {
	Name() string
	Command(ctx context.Context, s Spec) (*exec.Cmd, error)
}

// Open returns the named sandbox. "auto" picks the native one for this platform
// and fails if there is none, rather than running without isolation.
func Open(name string) (Sandbox, error) {
	switch name {
	case "auto":
		switch runtime.GOOS {
		case "linux":
			if _, err := exec.LookPath("bwrap"); err == nil {
				return Bubblewrap{}, nil
			}
		case "darwin":
			if _, err := exec.LookPath("sandbox-exec"); err == nil {
				return Seatbelt{}, nil
			}
		}
		if _, err := exec.LookPath("docker"); err == nil {
			return Docker{Image: DefaultDockerImage}, nil
		}
		return nil, errors.New("no sandbox available: install bubblewrap or Docker, or pass --sandbox none to accept the risk")
	case "seatbelt":
		return Seatbelt{}, nil
	case "bwrap":
		return Bubblewrap{}, nil
	case "docker":
		return Docker{Image: DefaultDockerImage}, nil
	case "none":
		return None{}, nil
	}
	return nil, fmt.Errorf("unknown sandbox %q", name)
}

// BaseEnv is a minimal environment holding no credentials from the parent.
func BaseEnv(home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"TMPDIR=" + filepath.Join(home, ".tmp"),
		"LANG=C.UTF-8",
	}
}

// SecretHome lists credential stores in the home directory. Harnesses that use the
// real home for their own login still have no reason to read these.
func SecretHome() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range []string{".aiculator", ".ssh", ".aws", ".gnupg", ".kube", ".docker/config.json", ".netrc", ".config/gh", ".config/gcloud"} {
		out = append(out, filepath.Join(home, d))
	}
	return out
}

func finish(ctx context.Context, s Spec, argv []string) *exec.Cmd {
	if v := vlog.For("sandbox").V(vlog.LevelTrace); v.Enabled() {
		v.Info("sandbox command", "argv", argv, "dir", s.Dir)
	} else if v := vlog.For("sandbox").V(vlog.LevelDebug); v.Enabled() {
		v.Info("sandbox command", "program", s.Argv, "dir", s.Dir, "network", s.Network)
	}
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = s.Dir
	c.Env = s.Env
	if c.Env == nil {
		c.Env = []string{} // A nil Env would inherit the parent's, secrets included.
	}
	return c
}

func validate(s Spec) (string, error) {
	if len(s.Argv) == 0 {
		return "", errors.New("sandbox: empty command")
	}
	if s.Dir == "" {
		return "", errors.New("sandbox: empty directory")
	}
	return realPath(s.Dir), nil
}

// realPath resolves symlinks, because Seatbelt matches resolved paths: on macOS
// /tmp and /var are symlinks into /private.
func realPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}

// None provides no isolation. It exists for trusted setups and is never chosen
// automatically.
type None struct{}

// Name implements Sandbox.
func (None) Name() string { return "none" }

// Command implements Sandbox.
func (None) Command(ctx context.Context, s Spec) (*exec.Cmd, error) {
	if _, err := validate(s); err != nil {
		return nil, err
	}
	return finish(ctx, s, s.Argv), nil
}

// Seatbelt uses macOS sandbox-exec. Apple marks it deprecated, though it still
// works; Docker is the alternative on macOS.
type Seatbelt struct{}

// Name implements Sandbox.
func (Seatbelt) Name() string { return "seatbelt" }

func seatbeltQuote(p string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
}

// Profile returns the policy: reads allowed, writes only in the listed paths,
// and no network unless asked for.
func (Seatbelt) Profile(dir string, writes, hide []string, network bool) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n(deny file-write*)\n")
	b.WriteString(`(allow file-write* (literal "/dev/null") (literal "/dev/zero") (literal "/dev/dtracehelper") (regex #"^/dev/tty") (regex #"^/dev/fd/"))` + "\n")
	for _, p := range append([]string{dir}, writes...) {
		b.WriteString("(allow file-write* (subpath " + seatbeltQuote(realPath(p)) + "))\n")
	}
	if !network {
		b.WriteString("(deny network*)\n")
	}
	// Last match wins in a profile, so these beat every allow above.
	for _, p := range hide {
		q := seatbeltQuote(realPath(p))
		b.WriteString("(deny file-read* file-write* (subpath " + q + "))\n")
	}
	return b.String()
}

// Command implements Sandbox.
func (x Seatbelt) Command(ctx context.Context, s Spec) (*exec.Cmd, error) {
	dir, err := validate(s)
	if err != nil {
		return nil, err
	}
	argv := append([]string{"sandbox-exec", "-p", x.Profile(dir, s.WritePaths, s.Hide, s.Network)}, s.Argv...)
	return finish(ctx, s, argv), nil
}

// Bubblewrap uses Linux namespaces, with a read-only root and new PID space.
type Bubblewrap struct{}

// Name implements Sandbox.
func (Bubblewrap) Name() string { return "bwrap" }

// Args returns the bwrap arguments, separated out so they can be tested anywhere.
func (Bubblewrap) Args(dir string, writes, hide []string, network bool, argv []string) []string {
	out := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--die-with-parent", "--new-session"}
	if !network {
		out = append(out, "--unshare-net")
	}
	for _, p := range append([]string{dir}, writes...) {
		out = append(out, "--bind", p, p)
	}
	// /tmp is replaced by an empty one above, so a program installed under it would
	// vanish. Bind just that program back, read-only.
	if len(argv) > 0 && filepath.IsAbs(argv[0]) && strings.HasPrefix(filepath.Clean(argv[0]), "/tmp/") {
		out = append(out, "--ro-bind", argv[0], argv[0])
	}
	for _, p := range hide {
		fi, err := os.Stat(p)
		switch {
		case err != nil:
			// Nothing there to hide.
		case fi.IsDir():
			out = append(out, "--tmpfs", p)
		default:
			out = append(out, "--ro-bind", "/dev/null", p)
		}
	}
	out = append(out, "--chdir", dir, "--")
	return append(out, argv...)
}

// Command implements Sandbox.
func (x Bubblewrap) Command(ctx context.Context, s Spec) (*exec.Cmd, error) {
	dir, err := validate(s)
	if err != nil {
		return nil, err
	}
	return finish(ctx, s, append([]string{"bwrap"}, x.Args(dir, s.WritePaths, s.Hide, s.Network, s.Argv)...)), nil
}

// DefaultDockerImage has a shell and git; projects will usually set their own.
const DefaultDockerImage = "alpine/git:latest"

// Docker runs the command in a throwaway container. Spec.Hide needs no handling:
// a container sees only what is mounted, and nothing else is.
type Docker struct{ Image string }

// Name implements Sandbox.
func (Docker) Name() string { return "docker" }

// Args returns the docker arguments, separated out so they can be tested anywhere.
func (d Docker) Args(dir string, writes []string, network bool, env, argv []string) []string {
	out := []string{"run", "--rm", "-i", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--workdir", dir}
	if !network {
		out = append(out, "--network=none")
	}
	for _, p := range append([]string{dir}, writes...) {
		out = append(out, "-v", p+":"+p)
	}
	for _, e := range env {
		if !strings.HasPrefix(e, "PATH=") { // PATH belongs to the image.
			out = append(out, "-e", e)
		}
	}
	// Some utility images (including alpine/git) define a non-shell
	// entrypoint. Clear it so argv is always the command requested by the
	// caller rather than arguments to the image's default executable.
	out = append(out, "--entrypoint", "", d.Image)
	return append(out, argv...)
}

// Command implements Sandbox.
func (d Docker) Command(ctx context.Context, s Spec) (*exec.Cmd, error) {
	dir, err := validate(s)
	if err != nil {
		return nil, err
	}
	argv := append([]string{"docker"}, d.Args(dir, s.WritePaths, s.Network, s.Env, s.Argv)...)
	c := finish(ctx, Spec{Dir: dir, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}}, argv)
	return c, nil
}
