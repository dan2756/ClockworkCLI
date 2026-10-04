//go:build unix

package runner

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the command in a new process group, so that it
// and everything it spawns can be signalled together, and so that a Ctrl-C
// in the terminal reaches cronlite rather than the task.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup sends sig to the command's whole process group.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, sig)
	}
}

// groupAlive reports whether anything is left in the command's process
// group, which can outlive the shell that leads it.
func groupAlive(cmd *exec.Cmd) bool {
	return cmd.Process != nil && syscall.Kill(-cmd.Process.Pid, 0) == nil
}
