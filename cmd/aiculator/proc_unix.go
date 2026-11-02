//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func stopProcess(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
