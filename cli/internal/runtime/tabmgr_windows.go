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

// ErrBackToTabs is returned by the newTabFn when the user presses Ctrl+B
// to dismiss the chooser and return to tab command mode.
var ErrBackToTabs = errors.New("back to tabs")

// ErrUpdateRequested is returned by RunTabbed when the user presses U in
// tab command mode to trigger a self-update and re-exec.
var ErrUpdateRequested = errors.New("update requested")

// NewTabFunc is called when the user requests a new tab or reopens the
// chooser for the active tab.
// stdinReader provides keyboard input; when nil the callback should read os.Stdin.
// tabBarLine returns the current tab bar content for embedding in the chooser view.
type NewTabFunc func(ctx context.Context, notify func(level TabNoticeLevel, message string), tabBarLine func() string, stdinReader io.Reader, seed *LaunchSpec) (*LaunchSpec, error)

// RunTabbed is not supported on Windows — falls back to single-session mode.
func RunTabbed(ctx context.Context, stdout, stderr io.Writer, version string, updateVersion string, newTabFn NewTabFunc) error {
	spec, err := newTabFn(ctx, func(TabNoticeLevel, string) {}, nil, nil, nil)
	if err != nil {
		return err
	}
	if spec == nil {
		return ErrQuit
	}
	return RunInteractive(ctx, stdout, stderr, *spec)
}
