//go:build !windows

package app

import "syscall"

// reexecSelf replaces the current process with the updated binary.
// On Unix, this uses execve(2) via syscall.Exec.
func reexecSelf(execPath string, args []string, env []string) error {
	return syscall.Exec(execPath, args, env)
}
