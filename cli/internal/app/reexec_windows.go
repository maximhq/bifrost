//go:build windows

package app

import "fmt"

// reexecSelf on Windows cannot replace the running process (syscall.Exec is a
// stub that returns EWINDOWS). Instead, inform the user to restart manually.
func reexecSelf(_ string, _ []string, _ []string) error {
	fmt.Println("Updated successfully. Please restart bifrost.")
	return nil
}
