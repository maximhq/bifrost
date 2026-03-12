//go:build !windows

package runtime

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"

	"os/exec"
)

// runWithPTY starts cmd attached to a new pseudo-terminal and relays I/O
// between the outer terminal and the PTY master. This lets TUI apps render
// correctly while bifrost retains control of the process.
func runWithPTY(ctx context.Context, stdout io.Writer, cmd *exec.Cmd) error {
	// Get the initial terminal size from the real terminal
	sz, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		// Fallback to a reasonable default if stdin isn't a terminal
		sz = &pty.Winsize{Rows: 24, Cols: 80}
	}

	// Start the command with a PTY attached, sized to match the outer terminal
	ptmx, err := pty.StartWithSize(cmd, sz)
	if err != nil {
		return err
	}
	defer ptmx.Close()

	// Handle SIGWINCH — propagate terminal resizes to the PTY
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	done := make(chan struct{})
	defer func() {
		signal.Stop(sigCh)
		close(done)
	}()
	go func() {
		for {
			select {
			case <-sigCh:
				if newSz, err := pty.GetsizeFull(os.Stdin); err == nil {
					_ = pty.Setsize(ptmx, newSz)
				}
			case <-done:
				return
			}
		}
	}()

	// Put the outer terminal into raw mode so keystrokes (Ctrl-C, etc.)
	// are forwarded as bytes to the PTY rather than handled by the OS.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		// If we can't go raw (e.g., piped input), continue without it
		oldState = nil
	}
	if oldState != nil {
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	// Relay stdout: PTY master → caller's stdout
	// This goroutine exits when the child process dies and the PTY master
	// returns EOF.
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		_, _ = io.Copy(stdout, ptmx)
	}()

	// Relay stdin: outer terminal → PTY master
	// This goroutine will block on os.Stdin.Read after the child exits;
	// that's expected and harmless — it unblocks on the next keystroke.
	go func() {
		_, _ = io.Copy(ptmx, os.Stdin)
	}()

	// Wait for the command to finish
	err = cmd.Wait()

	// Drain any remaining PTY output
	<-outDone

	return err
}
