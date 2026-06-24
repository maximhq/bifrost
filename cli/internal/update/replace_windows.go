//go:build windows

package update

import (
	"fmt"
	"os"
	"path/filepath"
)

// atomicReplace replaces oldPath with newPath. On Windows, we cannot rename
// over a running executable directly, so we move the old binary aside first.
func atomicReplace(oldPath, newPath string) error {
	backupPath := filepath.Join(filepath.Dir(oldPath), ".bifrost-old.exe")
	os.Remove(backupPath) // clean up any previous backup

	if err := os.Rename(oldPath, backupPath); err != nil {
		return err
	}
	if err := os.Rename(newPath, oldPath); err != nil {
		// Rollback — report if rollback also fails
		if rbErr := os.Rename(backupPath, oldPath); rbErr != nil {
			return fmt.Errorf("rename failed: %w; rollback also failed: %v (backup at %s)", err, rbErr, backupPath)
		}
		return err
	}
	os.Remove(backupPath)
	return nil
}
