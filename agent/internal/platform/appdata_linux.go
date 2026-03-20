package platform

import (
	"os"
	"path/filepath"
)

func appDataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "bifrost-agent")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "bifrost-agent")
}
