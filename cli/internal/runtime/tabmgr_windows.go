//go:build windows

package runtime

import (
	"context"
	"errors"
	"io"
)

// ErrQuit is returned by RunTabbed when the user quits the chooser
// without creating any tabs.
var ErrQuit = errors.New("user quit")

// NewTabFunc is called when the user requests a new tab.
// stdinReader provides keyboard input; when nil the callback should read os.Stdin.
type NewTabFunc func(ctx context.Context, notify func(level TabNoticeLevel, message string), stdinReader io.Reader) (*LaunchSpec, error)

// RunTabbed is not supported on Windows — falls back to single-session mode.
func RunTabbed(ctx context.Context, stdout, stderr io.Writer, version string, newTabFn NewTabFunc) error {
	spec, err := newTabFn(ctx, func(TabNoticeLevel, string) {}, nil)
	if err != nil {
		return err
	}
	if spec == nil {
		return ErrQuit
	}
	return RunInteractive(ctx, stdout, stderr, *spec)
}
