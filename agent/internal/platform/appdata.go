// Package platform provides OS-specific helpers for the Bifrost agent.
package platform

import (
	"os"
	"path/filepath"
)

// AppDataDir returns the platform-specific directory for persistent agent data.
// It creates the directory if it does not exist.
func AppDataDir() (string, error) {
	dir := appDataDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// LogDir returns the platform-specific directory for agent log files.
func LogDir() (string, error) {
	dir := filepath.Join(appDataDir(), "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
