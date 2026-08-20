//go:build unix

// The build constraint lives in the tag above rather than in the filename so
// this file can keep the repo's no-underscores-in-Go-filenames convention (see
// AGENTS.md, Code Style); the `_test.go` suffix is the sole exception.

package clis

import (
	"os"
	"os/exec"
	"syscall"
)

// isolateProcessGroup puts cmd in its own process group and redirects the
// context-cancel kill at that whole group.
//
// Every CLI under test is a launcher, not a leaf: `opencode run` re-execs
// itself as a background server, and the npm-installed CLIs shell out freely.
// The default exec.CommandContext cancel signals only the direct child, which
// leaves those grandchildren alive holding the write end of the stdout pipe --
// and cmd.Wait cannot return until every writer closes it, so the cell hangs
// past its turn deadline, past the 15-minute cell budget, forever. Killing the
// group reaps the tree instead, and the survivors stop billing tokens.
//
// A side effect worth knowing: the children no longer share the terminal's
// foreground process group, so a Ctrl-C typed at the shell reaches only the
// test binary. That is what we want -- TestMain's handler kills them
// deliberately and records which ones it reaped, which is what lets
// cellWasInterrupted tell an interruption apart from a regression. The old
// behaviour raced that bookkeeping against the kernel's own signal delivery.
func isolateProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	// Only exec.CommandContext installs a Cancel; leave a context-less command
	// alone rather than inventing cancellation semantics it never had.
	if cmd.Cancel != nil {
		cmd.Cancel = func() error { return killProcessGroup(cmd) }
	}
}

// killProcessGroup SIGKILLs cmd's entire process group, falling back to the
// single process when the group cannot be determined.
//
// The pgid > 1 guard is not paranoia: syscall.Kill with a negative pid signals
// a whole group, and -1 means "every process this uid may signal". A Getpgid
// race on an already-reaped child returning 0 or 1 would otherwise turn a test
// timeout into killing the developer's session.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil || pgid <= 1 {
		return cmd.Process.Kill()
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

// processGroupID reads cmd's process group while the child is certainly still
// ours, so the signal handler never has to look it up from a pid that may since
// have been reaped and recycled. 0 means "unknown", which callers degrade to a
// single-process kill.
func processGroupID(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return 0
	}
	return pgid
}

// killCmdGroup SIGKILLs the process group captured at Start, falling back to
// os.Process.Kill when no group was recorded. The fallback matters: it is the
// path that still carries Go's own ErrProcessDone guard.
func killCmdGroup(cmd *exec.Cmd, pgid int) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if pgid <= 1 {
		return cmd.Process.Kill()
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
