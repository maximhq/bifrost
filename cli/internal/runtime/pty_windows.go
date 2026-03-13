//go:build windows

package runtime

import (
	"context"
	"io"
	"os"
	"os/exec"
)

// runWithPTY is a no-op on Windows — falls back to direct stdin/stdout piping.
// Windows does not support POSIX pseudo-terminals; full support would require
// ConPTY which is not yet portable in Go.
func runWithPTY(_ context.Context, stdout io.Writer, cmd *exec.Cmd) error {
	cmd.Stdout = stdout
	cmd.Stderr = stdout
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
