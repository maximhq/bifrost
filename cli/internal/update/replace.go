//go:build !windows

package update

import "os"

// atomicReplace replaces oldPath with newPath using os.Rename.
// On Unix systems, this is atomic if both paths are on the same filesystem.
func atomicReplace(oldPath, newPath string) error {
	return os.Rename(newPath, oldPath)
}
