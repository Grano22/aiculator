//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	errorSharingViolation = syscall.Errno(32)
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

func stopProcess(pid int) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// processAlive reports whether a process is still running.
func processAlive(pid int) bool {
	const synchronize = 0x00100000
	h, err := syscall.OpenProcess(synchronize, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	state, err := syscall.WaitForSingleObject(h, 0)
	return err == nil && state == syscall.WAIT_TIMEOUT
}

// lockDataDir holds the lock file open with no sharing for as long as the process runs.
// Windows closes it however the process ends, so a crash cannot leave a stale lock.
func lockDataDir(dir string) (func(), error) {
	path, err := syscall.UTF16PtrFromString(filepath.Join(dir, "lock"))
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(path, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errorSharingViolation) {
			return nil, fmt.Errorf("another server is already using %s", dir)
		}
		return nil, err
	}
	return func() { _ = syscall.CloseHandle(h) }, nil
}
