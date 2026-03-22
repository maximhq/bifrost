package privilege

import (
	"fmt"
	"os/exec"
)

// elevate uses pkexec (PolicyKit) to get elevated privileges on Linux.
// Falls back to sudo if pkexec is not available.
func elevate(execPath string, args ...string) error {
	// Try pkexec first (graphical sudo prompt)
	if _, err := exec.LookPath("pkexec"); err == nil {
		cmdArgs := append([]string{execPath}, args...)
		cmd := exec.Command("pkexec", cmdArgs...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("pkexec failed: %w: %s", err, string(out))
		}
		return nil
	}

	// Fall back to sudo
	cmdArgs := append([]string{execPath}, args...)
	cmd := exec.Command("sudo", cmdArgs...)
	cmd.Stdin = nil // no interactive input
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sudo failed: %w: %s", err, string(out))
	}
	return nil
}
