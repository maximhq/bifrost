//go:build !windows

package runtime

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

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
		defer func() {
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
			_, _ = io.WriteString(stdout, hostCursorResetSequence())
		}()
	}

	// Relay stdout: PTY master → caller's stdout
	// This goroutine exits when the child process dies and the PTY master
	// returns EOF.
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		_, _ = io.Copy(stdout, ptmx)
	}()

	// Relay stdin: outer terminal → PTY master. Use an independently opened
	// controlling-terminal descriptor when possible so it can be closed after
	// the child exits. This matters to the launcher, which returns to its chooser
	// and must not leave an old reader behind to steal the next keystroke.
	input := io.Reader(os.Stdin)
	var terminalInput *os.File
	if term.IsTerminal(int(os.Stdin.Fd())) {
		if tty, openErr := os.OpenFile("/dev/tty", os.O_RDONLY|syscall.O_NONBLOCK, 0); openErr == nil {
			terminalInput = tty
			input = tty
		}
	}
	inputDone := make(chan struct{})
	stopInput := make(chan struct{})
	if terminalInput != nil {
		go relayTerminalInput(ptmx, terminalInput, stopInput, inputDone)
	} else {
		go func() {
			defer close(inputDone)
			_, _ = io.Copy(ptmx, input)
		}()
	}

	// Wait for the command to finish
	err = cmd.Wait()
	if terminalInput != nil {
		close(stopInput)
		_ = terminalInput.SetReadDeadline(time.Now())
		<-inputDone
		_ = terminalInput.Close()
	}

	// Drain any remaining PTY output
	<-outDone

	return err
}

// relayTerminalInput copies keyboard input into a child PTY while remaining
// cancellable when the child exits. A short read deadline avoids leaving a
// goroutine blocked on /dev/tty when the launcher needs stdin back.
func relayTerminalInput(destination io.Writer, source *os.File, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	buffer := make([]byte, 4096)
	for {
		select {
		case <-stop:
			return
		default:
		}

		_ = source.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := source.Read(buffer)
		if n > 0 {
			if _, writeErr := destination.Write(buffer[:n]); writeErr != nil {
				return
			}
		}
		if err == nil {
			continue
		}
		if os.IsTimeout(err) {
			continue
		}
		return
	}
}
