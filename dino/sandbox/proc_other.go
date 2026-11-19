//go:build !unix

package sandbox

import "os/exec"

// setProcessGroup is a no-op on platforms without Unix process groups.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup terminates the child only; helpers it spawned may survive.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
