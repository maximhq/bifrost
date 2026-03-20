package platform

import (
	"os"
	"path/filepath"
)

func appDataDir() string {
	// When running as sudo, $HOME might be /var/root. Use SUDO_USER
	// to get the real user's home directory.
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		return filepath.Join("/Users", sudoUser, "Library", "Application Support", "BifrostAgent")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "BifrostAgent")
}
