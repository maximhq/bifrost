//go:build !unix

// See procgroupunix_test.go for why the build constraint is a tag rather than a
// filename suffix.

package clis

import (
	"os"
	"os/exec"
)

// isolateProcessGroup is a no-op off Unix: process groups are a POSIX concept,
// and the Windows job-object equivalent is not worth carrying for a harness
// that only ever runs on macOS and Linux CI. cmd.WaitDelay still bounds the
// hang everywhere, so the suite cannot wedge on this platform either -- it just
// leaves orphans behind when it unwedges.
func isolateProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup degrades to killing the single process.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}

// processGroupID has nothing to report where there are no process groups.
func processGroupID(cmd *exec.Cmd) int { return 0 }

// killCmdGroup ignores the recorded pgid for the same reason, and so keeps
// os.Process.Kill's own ErrProcessDone guard on every path here.
func killCmdGroup(cmd *exec.Cmd, _ int) error {
	return killProcessGroup(cmd)
}
