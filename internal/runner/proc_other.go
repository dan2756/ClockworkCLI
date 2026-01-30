//go:build !unix

package runner

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {}

// signalGroup kills the command itself; process groups are a Unix notion.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
