//go:build windows

package driver

import (
	"os/exec"
	"strconv"
	"time"
)

// Windows has no process groups in the Unix sense, so taskkill stops the tree.
func killGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		killTree(cmd)
		return nil
	}
	cmd.WaitDelay = 3 * time.Second
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}
