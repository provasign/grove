//go:build windows

package native

import "os/exec"

// isolateProcessGroup is a no-op on Windows; cmd.WaitDelay bounds the wait
// for pipes a descendant still holds.
func isolateProcessGroup(cmd *exec.Cmd) {}

// killProcessTree kills the worker process.
func killProcessTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
