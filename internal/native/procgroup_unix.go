//go:build !windows

package native

import (
	"os/exec"
	"syscall"
)

// isolateProcessGroup starts a resident worker in its own process group, so
// stopping it can stop whatever it spawned (node's typings installer, a JVM's
// children) instead of leaving them holding the worker's pipes.
func isolateProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessTree kills a worker and its process group.
func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
