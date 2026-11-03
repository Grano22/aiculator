//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"syscall"
)

func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func stopProcess(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// processAlive reports whether a process exists. EPERM means it exists under another user.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
