package verify

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

func VerifyShell(ctx context.Context, workDir, shellCmd string) (ok bool, reason string, exitCode *int) {
	shellCmd = strings.TrimSpace(shellCmd)
	if shellCmd == "" {
		return true, "", nil
	}
	dir := workDir
	if dir == "" {
		dir = "."
	}
	dir = filepath.Clean(dir)
	c := exec.CommandContext(ctx, "sh", "-c", shellCmd)
	c.Dir = dir
	out, err := c.CombinedOutput()
	exit := 0
	if err != nil {
		if x, ok := err.(*exec.ExitError); ok && x.ExitCode() >= 0 {
			exit = x.ExitCode()
		} else {
			exit = -1
		}
	}
	code := exit
	exitPtr := &code
	if exit != 0 {
		r := strings.TrimSpace(string(out))
		if r == "" {
			r = "command exited with non-zero status"
		}
		return false, r, exitPtr
	}
	return true, "", exitPtr
}
