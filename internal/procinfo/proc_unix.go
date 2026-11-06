//go:build !windows

package procinfo

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func name(pid int) string {
	if pid <= 1 {
		return ""
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(string(out)))
}

// ErrNotOurs means the process is gone, or is no longer an aiculator process: a process id
// can be reused, and this never signals a process it did not start.
var ErrNotOurs = errors.New("not an aiculator process")

// Terminate asks an aiculator process to stop, after checking that it still is one.
func Terminate(pid int) error {
	if pid <= 1 {
		return ErrNotOurs
	}
	if n := name(pid); !strings.Contains(n, "aiculator") {
		return ErrNotOurs
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}
