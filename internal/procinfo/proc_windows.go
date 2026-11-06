//go:build windows

package procinfo

import (
	"errors"
	"os"
)

func name(int) string { return "" }

// ErrNotOurs means the process is gone, or could not be checked as aiculator's own.
var ErrNotOurs = errors.New("not an aiculator process")

// Terminate ends an aiculator process. Windows cannot check what a process id runs without
// more machinery, so it only ends processes this one started; others are told to exit.
func Terminate(pid int) error {
	if pid != os.Getpid() {
		return ErrNotOurs
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
