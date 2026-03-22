package platform

import (
	"os"
	"path/filepath"
)

func appDataDir() string {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "BifrostAgent")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "AppData", "Local", "BifrostAgent")
}
