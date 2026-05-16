package installer

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/maximhq/bifrost/cli/internal/harness"
)

// IsInstalled reports whether the harness binary exists in the system PATH.
func IsInstalled(h harness.Harness) bool {
	_, err := exec.LookPath(h.Binary)
	return err == nil
}

// InstallCommand returns the command and arguments needed to install the harness globally via npm.
func InstallCommand(h harness.Harness) (string, []string) {
	return "npm", []string{"install", "-g", h.InstallPkg}
}

// EnsureNPM checks that npm is available in the system PATH.
func EnsureNPM() error {
	_, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("npm not found in path: %w", err)
	}
	return nil
}

// RunInstall executes the npm install command for the given harness,
// streaming output to the provided writers.
func RunInstall(ctx context.Context, stdout, stderr io.Writer, h harness.Harness) error {
	cmdName, args := InstallCommand(h)
	cmd := exec.CommandContext(ctx, cmdName, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install %s: %w", h.Label, err)
	}
	return nil
}
