//go:build !windows

package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/maximhq/vt10x"
	"golang.org/x/term"
)

// ErrQuit is returned by RunTabbed when the user quits the chooser
// without creating any tabs.
var ErrQuit = errors.New("user quit")

// ErrBackToTabs is returned by the newTabFn when the user presses Ctrl+B
// to dismiss the chooser and return to tab command mode.
var ErrBackToTabs = errors.New("back to tabs")

const pendingTabLabel = "Bifrost"

// Tab represents a single CLI session running in a PTY.
type Tab struct {
	id               int
	label            string
	spec             LaunchSpec
	ptmx             *os.File
	cmd              *PreparedCmd
	done             chan struct{} // closed when the process exits
	exited           atomic.Bool
	exitErr          error
	startedAt        time.Time
	vt               vt10x.Terminal // virtual terminal emulator for this tab's screen state
	normalizer       vtStreamNormalizer
	statusMu         sync.RWMutex // guards bell, screenHash, lastScreenChange, prevScreenChange
	bell             bool         // set when BEL received on inactive tab, cleared on switch
	cursorShape      atomic.Int32 // DECSCUSR value (0=default, 1-6 per spec)
	cursorVisible    atomic.Bool  // last cursor visibility from raw PTY (\x1b[?25h/l)
	cursorSavedX     atomic.Int32 // cursor X captured when child sends \x1b[?25h
	cursorSavedY     atomic.Int32 // cursor Y captured when child sends \x1b[?25h
	cursorSavedValid atomic.Bool  // true once we've captured a cursor-show position
	screenHash       uint64       // latest visible VT screen fingerprint
	lastScreenChange time.Time
	prevScreenChange time.Time
	lastUserInputAt  atomic.Int64
	bellState        belParserState
}

type mouseEvent struct {
	x      int
	y      int
	button int
	press  bool
	motion bool
	wheel  bool
}

type belParserState struct {
	inOSC      bool
	escPending bool
}

// NewTabFunc is called when the user requests a new tab or reopens the
// chooser for the active tab. It should present any UI needed (e.g. the
// harness chooser) and return the launch spec. Return a nil spec to cancel.
// When seed is non-nil, the chooser should use it to prefill the current tab.
// stdinReader provides keyboard input; when nil the callback should read os.Stdin.
type NewTabFunc func(ctx context.Context, notify func(level TabNoticeLevel, message string), stdinReader io.Reader, seed *LaunchSpec) (*LaunchSpec, error)

// TabManager multiplexes multiple CLI sessions. Each session runs in its own PTY,
// with a virtual terminal emulator capturing output. A 30fps render loop composites
// the active tab's screen content with the tab bar into atomic frames.
type TabManager struct {
	stdout  io.Writer
	stderr  io.Writer
	version string

	mu           sync.Mutex
	outputMu     sync.Mutex
	tabs         []*Tab
	activeIdx    int
	nextID       int
	rows         uint16
	cols         uint16
	paused       bool // true while chooser overlay is active
	commandMode  bool // true while the tab-mode overlay owns the terminal
	needsRender  bool // set by readPTY/switchTab/resize, cleared by renderFrame
	lastCtrlCAt  time.Time
	hostVTMode   vt10x.ModeFlag
	noticeText   string
	noticeLevel  TabNoticeLevel
	noticeUntil  time.Time
	noticeSticky bool

	closeCh     chan struct{} // closed when all tabs are gone
	closeOnce   sync.Once
	stdinCh     chan stdinResult
	stdinPaused atomic.Bool // true while chooser owns os.Stdin
	stdinPollFd *os.File    // dup'd non-blocking stdin for the reader goroutine

	cursorTraceMu sync.Mutex
	cursorTrace   io.WriteCloser
}

// stdinResult carries data from the dedicated stdin-reading goroutine.
type stdinResult struct {
	data []byte
	err  error
}

// RunTabbed enters the tabbed multiplexer. It starts with a tab bar,
// immediately opens the chooser via newTabFn for the first tab, then enters
// the main event loop. Returns ErrQuit if the user quits the initial chooser
// without creating any tabs.
func RunTabbed(ctx context.Context, stdout, stderr io.Writer, version string, newTabFn NewTabFunc) error {
	tm := &TabManager{
		stdout:  stdout,
		stderr:  stderr,
		version: version,
		closeCh: make(chan struct{}),
	}

	// Get terminal size
	if c, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		tm.rows, tm.cols = uint16(r), uint16(c)
	} else {
		tm.rows, tm.cols = 24, 80
	}

	tm.initCursorTrace()
	defer tm.closeCursorTrace()

	// Enter raw mode so the home screen and tab mode render cleanly.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		// Can't go raw — fall back: open chooser directly, then single-session
		spec, err := newTabFn(ctx, tm.emitNotice, nil, nil)
		if err != nil {
			return err
		}
		if spec == nil {
			return ErrQuit
		}
		return RunInteractive(ctx, stdout, stderr, *spec)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	defer func() {
		tm.resetHostInputModes()
		tm.writeString("\x1b[2J\x1b[H")
	}()

	// Draw the tab bar so the user starts in tabbed mode immediately.
	tm.drawTabBar()

	// Start the frame render loop.
	go tm.renderLoop()
	defer tm.signalClose()

	// Open the chooser within the tab content area.
	tm.openNewTab(ctx, newTabFn, oldState)

	// If the user quit the chooser without creating a tab, exit
	if tm.shouldExitWithoutTabs() {
		return ErrQuit
	}

	// Handle SIGWINCH (terminal resize)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for {
			select {
			case <-sigCh:
				tm.handleResize()
			case <-tm.closeCh:
				return
			}
		}
	}()

	// Open a separate non-blocking fd to the controlling terminal for the
	// reader goroutine. We can't use syscall.Dup because dup'd fds share
	// file status flags — SetNonblock would also make os.Stdin non-blocking.
	// Opening /dev/tty creates an independent file description, so its
	// O_NONBLOCK flag is separate from stdin's. This lets Go's runtime
	// poller register the fd, making SetReadDeadline work — which lets
	// openNewTab interrupt a blocked Read so the goroutine yields the
	// terminal to the chooser.
	tm.stdinPollFd, err = os.OpenFile("/dev/tty", os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open /dev/tty: %w", err)
	}
	defer tm.stdinPollFd.Close()

	tm.stdinCh = make(chan stdinResult, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			// Yield stdin to the chooser while paused.
			for tm.stdinPaused.Load() {
				time.Sleep(10 * time.Millisecond)
			}
			n, err := tm.stdinPollFd.Read(buf)
			// Deadline-induced timeout — just retry.
			if err != nil && os.IsTimeout(err) {
				continue
			}
			// Re-check: may have been paused while blocked on Read.
			if tm.stdinPaused.Load() {
				if err != nil {
					return
				}
				continue // discard — chooser owns stdin
			}
			res := stdinResult{}
			if n > 0 {
				res.data = make([]byte, n)
				copy(res.data, buf[:n])
			}
			if err != nil {
				res.err = err
			}
			tm.stdinCh <- res
			if err != nil {
				return
			}
		}
	}()

	// Main input loop
	return tm.inputLoop(ctx, newTabFn, oldState)
}

// addTab creates a new PTY session for the given spec, initializes a virtual
// terminal emulator, and starts reading PTY output into it.
func (tm *TabManager) addTab(ctx context.Context, spec LaunchSpec) error {
	tab, err := tm.createTab(ctx, spec)
	if err != nil {
		return err
	}

	tm.mu.Lock()
	tm.tabs = append(tm.tabs, tab)
	tm.activeIdx = len(tm.tabs) - 1
	tm.needsRender = true
	tm.mu.Unlock()

	tm.startTab(tab)
	return nil
}

func (tm *TabManager) createTab(ctx context.Context, spec LaunchSpec) (*Tab, error) {
	p, err := PrepareCommand(ctx, spec)
	if err != nil {
		return nil, err
	}

	contentRows := tm.contentRows()
	cols := tm.cols

	// Reserve the bottom row for the tab bar.
	ptmx, err := pty.StartWithSize(p.Cmd, &pty.Winsize{
		Rows: contentRows,
		Cols: cols,
	})
	if err != nil {
		if p.Cleanup != nil {
			p.Cleanup()
		}
		return nil, fmt.Errorf("start pty: %w", err)
	}

	// Build label: "harness" or "harness:worktree"
	label := spec.Harness.Label
	if wt := strings.TrimSpace(spec.Worktree); wt != "" {
		label += ":" + wt
	}

	tab := &Tab{
		id:        tm.nextID,
		label:     label,
		spec:      spec,
		ptmx:      ptmx,
		cmd:       p,
		done:      make(chan struct{}),
		startedAt: time.Now(),
		vt: vt10x.New(
			vt10x.WithWriter(ptmx),
			vt10x.WithSize(int(cols), int(contentRows)),
		),
	}
	tab.cursorVisible.Store(true)
	tm.nextID++

	return tab, nil
}

func (tm *TabManager) startTab(tab *Tab) {
	// Read PTY output into the VT emulator
	go tm.readPTY(tab)

	// Wait for process exit
	go func() {
		tab.exitErr = tab.cmd.Cmd.Wait()
		tab.ptmx.Close()
		tab.exited.Store(true)
		close(tab.done)
		if tab.cmd.Cleanup != nil {
			tab.cmd.Cleanup()
		}
		tm.removeTab(tab)
	}()
}

func (tm *TabManager) addPendingTab() (*Tab, int) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	prevActive := tm.activeIdx
	tab := &Tab{
		id:    tm.nextID,
		label: pendingTabLabel,
		done:  make(chan struct{}),
	}
	tm.nextID++
	tm.tabs = append(tm.tabs, tab)
	tm.activeIdx = len(tm.tabs) - 1
	tm.paused = true
	tm.commandMode = false

	return tab, prevActive
}

func (tm *TabManager) removePendingTab(tab *Tab, restoreActive int) *Tab {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	idx := -1
	for i, t := range tm.tabs {
		if t == tab {
			idx = i
			break
		}
	}
	if idx >= 0 {
		tm.tabs = append(tm.tabs[:idx], tm.tabs[idx+1:]...)
	}

	tm.paused = false
	tm.commandMode = false

	if len(tm.tabs) == 0 {
		return nil
	}

	if restoreActive >= 0 && restoreActive < len(tm.tabs) {
		tm.activeIdx = restoreActive
	} else if tm.activeIdx >= len(tm.tabs) {
		tm.activeIdx = len(tm.tabs) - 1
	}

	return tm.tabs[tm.activeIdx]
}

// readPTY reads from a tab's PTY master and writes into its VT emulator.
// When the tab is active, it sets the dirty flag so the render loop
// composites a new frame.
func (tm *TabManager) readPTY(tab *Tab) {
	buf := make([]byte, 4096)
	for {
		n, err := tab.ptmx.Read(buf)
		if n > 0 {
			raw := buf[:n]
			tm.traceClaudeCursorBytes(tab, "pty_raw", raw)

			// Extract cursor shape (DECSCUSR) from raw PTY data before the
			// normalizer/vt10x swallow it.
			if shape := extractCursorShape(raw); shape >= 0 {
				tab.cursorShape.Store(shape)
			}
			if vis := extractCursorVisible(raw); vis >= 0 {
				tab.cursorVisible.Store(vis == 1)
			}

			// Normalize PTY output before it reaches vt10x so split CSI chunks
			// and colon-style SGR don't poison the emulator state.
			data := tab.normalizer.Normalize(raw)
			tm.traceClaudeCursorBytes(tab, "pty_normalized", data)
			screenChanged := false
			if len(data) > 0 {
				if idx := lastCursorShowIndex(data); idx >= 0 {
					showEnd := idx + len("\x1b[?25h")
					if showEnd > len(data) {
						showEnd = len(data)
					}

					// Capture the child's intended cursor position at the exact
					// moment it shows the cursor, before any later bytes in the
					// same chunk can move the live VT cursor elsewhere.
					tab.vt.Write(data[:showEnd])
					tab.vt.Lock()
					cursor := tab.vt.Cursor()
					tab.vt.Unlock()
					tab.cursorSavedX.Store(int32(cursor.X))
					tab.cursorSavedY.Store(int32(cursor.Y))
					tab.cursorSavedValid.Store(true)
					tm.traceClaudeCursorf(tab, "cursor_saved_from_show vt_cursor=(%d,%d)", cursor.X, cursor.Y)

					if showEnd < len(data) {
						tab.vt.Write(data[showEnd:])
					}
				} else {
					// Write to VT emulator — self-locking, parses ANSI sequences.
					tab.vt.Write(data)
				}

				if tab.cursorVisible.Load() {
					tab.vt.Lock()
					cursor := tab.vt.Cursor()
					vtVisible := tab.vt.CursorVisible()
					tab.vt.Unlock()
					tab.cursorSavedX.Store(int32(cursor.X))
					tab.cursorSavedY.Store(int32(cursor.Y))
					tab.cursorSavedValid.Store(true)
					tm.traceClaudeCursorf(tab, "cursor_saved_visible_chunk vt_cursor=(%d,%d) vt_visible=%t raw_visible=%t",
						cursor.X, cursor.Y, vtVisible, tab.cursorVisible.Load())
				}

				tab.vt.Lock()
				vtCursor := tab.vt.Cursor()
				vtVisible := tab.vt.CursorVisible()
				tab.vt.Unlock()
				tm.traceClaudeCursorf(tab, "post_write vt_cursor=(%d,%d) vt_visible=%t raw_visible=%t saved_valid=%t saved=(%d,%d)",
					vtCursor.X, vtCursor.Y, vtVisible, tab.cursorVisible.Load(),
					tab.cursorSavedValid.Load(), tab.cursorSavedX.Load(), tab.cursorSavedY.Load())

				screenChanged = noteTabScreenChange(tab, time.Now())
			}

			// Only count standalone BEL bytes. OSC/title sequences commonly use
			// BEL as a terminator, and those should not surface as notifications.
			hasBEL := containsStandaloneBEL(raw, &tab.bellState)

			tm.mu.Lock()
			isActive := !tm.paused &&
				tm.activeIdx < len(tm.tabs) && tm.tabs[tm.activeIdx] == tab
			if isActive && screenChanged {
				tm.needsRender = true
			} else if hasBEL {
				tab.statusMu.Lock()
				tab.bell = true
				tab.statusMu.Unlock()
				tm.needsRender = true // redraw tab bar to show bell
			} else if screenChanged {
				tm.needsRender = true
			}
			tm.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// prefix is the bifrost prefix key: Ctrl+B (0x02).
// When pressed, PTY output is frozen and bifrost enters tab mode.
const prefix = 0x02

// inputLoop is the main event loop that reads stdin and dispatches to tabs or hotkeys.
//
// Global keybindings (always active):
//
//	Ctrl+1…9    — jump to tab N
//	Ctrl+Tab    — cycle to next tab
//	Ctrl+B      — toggle tab command mode
//
// Keybindings while in tab command mode (^B prefix):
//
//	N               — open new tab (shows chooser)
//	E               — edit the current session
//	X               — close current tab
//	H/L or J/K      — move left/right
//	1…9             — jump to tab N
//	Esc/Enter/Ctrl+B — resume the active tab
func (tm *TabManager) inputLoop(ctx context.Context, newTabFn NewTabFunc, termState *term.State) error {
	pending := make([]byte, 0, 4096)

	for {
		if tm.shouldExitWithoutTabs() {
			return nil
		}

		if len(pending) == 0 {
			if err := tm.waitForInput(ctx, &pending); err != nil {
				return err
			}
		}

		for len(pending) > 0 {
			token, consumed, isPrefix, complete := nextInputToken(pending)
			if !complete {
				if err := tm.waitForInput(ctx, &pending); err != nil {
					return err
				}
				break
			}
			pending = pending[consumed:]

			if isTerminalResponse(token) {
				continue
			}

			if isPrefix {
				if tm.isCommandMode() {
					tm.handleCommandKey(ctx, newTabFn, termState, prefix)
				} else {
					tm.enterCommandMode()
				}
				continue
			}

			// Ctrl+1..9 — jump to tab (works in any mode)
			if idx := parseCtrlDigit(token); idx >= 0 {
				if tm.isCommandMode() {
					tm.exitCommandMode()
				}
				tm.switchTab(idx)
				continue
			}

			// Ctrl+Tab — cycle to next tab (works in any mode)
			if isCtrlTab(token) {
				if tm.isCommandMode() {
					tm.exitCommandMode()
				}
				tm.cycleTab()
				continue
			}

			if ev, ok := parseMouseEvent(token); ok {
				if tm.handleTabBarMouseEvent(ev) {
					continue
				}
				if tm.isCommandMode() {
					continue
				}
			}

			if tm.isCommandMode() {
				if b, ok := decodeCommandByte(token); ok {
					tm.handleCommandKey(ctx, newTabFn, termState, b)
				}
				continue
			}

			// Decode kitty keyboard protocol CSI u sequences into standard
			// terminal input before forwarding. This handles key release
			// events (dropped) and regular keys that were encoded as CSI u
			// because a child process enabled the kitty protocol.
			if isCSIu(token) {
				if decoded := decodeCSIu(token); decoded != nil {
					if tm.handleActiveCtrlC(decoded) {
						continue
					}
					tm.forwardToActive(decoded)
				}
				continue
			}

			if tm.handleActiveCtrlC(token) {
				continue
			}
			tm.forwardToActive(token)
		}
	}
}

// waitForInput blocks until stdin data arrives, the context is cancelled,
// or all tabs have closed — whichever comes first.
func (tm *TabManager) waitForInput(ctx context.Context, pending *[]byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-tm.closeCh:
		return nil
	case res := <-tm.stdinCh:
		if len(res.data) > 0 {
			*pending = append(*pending, res.data...)
		}
		return res.err
	}
}

func nextInputToken(buf []byte) ([]byte, int, bool, bool) {
	if len(buf) == 0 {
		return nil, 0, false, false
	}

	if buf[0] == prefix {
		return buf[:1], 1, true, true
	}

	if buf[0] != 0x1b {
		return buf[:1], 1, false, true
	}

	if len(buf) == 1 {
		return buf[:1], 1, false, true
	}

	if buf[1] == ']' || buf[1] == 'P' || buf[1] == '^' || buf[1] == '_' {
		for i := 2; i < len(buf); i++ {
			if buf[i] == 0x07 {
				return buf[:i+1], i + 1, false, true
			}
			if buf[i] == 0x1b && i+1 < len(buf) && buf[i+1] == '\\' {
				return buf[:i+2], i + 2, false, true
			}
		}
		return nil, 0, false, false
	}

	if buf[1] != '[' && buf[1] != 'O' {
		return buf[:1], 1, false, true
	}

	// X10 mouse tracking reports use CSI M followed by three data bytes.
	if buf[1] == '[' && len(buf) >= 3 && buf[2] == 'M' {
		if len(buf) < 6 {
			return nil, 0, false, false
		}
		return buf[:6], 6, false, true
	}

	for i := 2; i < len(buf); i++ {
		if buf[i] >= 0x40 && buf[i] <= 0x7e {
			token := buf[:i+1]
			return token, i + 1, isCtrlBSequence(token), true
		}
	}

	return nil, 0, false, false
}

func isTerminalResponse(seq []byte) bool {
	if len(seq) < 2 || seq[0] != 0x1b {
		return false
	}

	switch seq[1] {
	case ']', 'P', '^', '_':
		return true
	case '[':
		final := seq[len(seq)-1]
		switch final {
		case 'R', 'c', 'n', 't':
			return true
		}
	}

	return false
}

func containsStandaloneBEL(data []byte, state *belParserState) bool {
	if state == nil {
		return false
	}

	found := false
	for _, b := range data {
		if state.inOSC {
			if state.escPending {
				state.escPending = false
				if b == '\\' {
					state.inOSC = false
					continue
				}
				if b == 0x1b {
					state.escPending = true
				}
				continue
			}

			switch b {
			case 0x07:
				state.inOSC = false
			case 0x1b:
				state.escPending = true
			}
			continue
		}

		if state.escPending {
			state.escPending = false
			if b == ']' {
				state.inOSC = true
				continue
			}
			if b == 0x1b {
				state.escPending = true
				continue
			}
		}

		if b == 0x1b {
			state.escPending = true
			continue
		}
		if b == 0x07 {
			found = true
		}
	}

	return found
}

func isCtrlBSequence(seq []byte) bool {
	if len(seq) < 4 || seq[0] != 0x1b || seq[1] != '[' {
		return false
	}

	final := seq[len(seq)-1]
	body := string(seq[2 : len(seq)-1])

	switch final {
	case 'u':
		parts := strings.Split(body, ";")
		if len(parts) < 2 {
			return false
		}
		if isReleaseEvent(parts[1]) {
			return false
		}
		return isCtrlBCode(parts[0]) && modifierHasCtrl(parts[1])
	case '~':
		parts := strings.Split(body, ";")
		if len(parts) < 3 || parts[0] != "27" {
			return false
		}
		if isReleaseEvent(parts[1]) {
			return false
		}
		return isCtrlBCode(parts[2]) && modifierHasCtrl(parts[1])
	default:
		return false
	}
}

// isReleaseEvent checks if a kitty keyboard protocol modifier field indicates
// a key release event (event_type 3, encoded as ":3" suffix).
func isReleaseEvent(modField string) bool {
	if idx := strings.IndexByte(modField, ':'); idx >= 0 {
		return modField[idx+1:] == "3"
	}
	return false
}

func isCtrlBCode(s string) bool {
	return s == "98" || s == "66"
}

// parseCtrlDigit checks if a CSI sequence is Ctrl+1..9 and returns the
// 0-based tab index (0..8). Returns -1 if not a Ctrl+digit sequence.
func parseCtrlDigit(seq []byte) int {
	if len(seq) < 4 || seq[0] != 0x1b || seq[1] != '[' {
		return -1
	}
	final := seq[len(seq)-1]
	body := string(seq[2 : len(seq)-1])

	switch final {
	case 'u': // CSI u: \x1b[codepoint;modifiers u
		parts := strings.Split(body, ";")
		if len(parts) < 2 || !modifierHasCtrl(parts[1]) || isReleaseEvent(parts[1]) {
			return -1
		}
		cp, err := strconv.Atoi(parts[0])
		if err != nil {
			return -1
		}
		if cp >= '1' && cp <= '9' {
			return cp - '1'
		}
	case '~': // xterm modkeys: \x1b[27;modifier;codepoint ~
		parts := strings.Split(body, ";")
		if len(parts) < 3 || parts[0] != "27" || !modifierHasCtrl(parts[1]) || isReleaseEvent(parts[1]) {
			return -1
		}
		cp, err := strconv.Atoi(parts[2])
		if err != nil {
			return -1
		}
		if cp >= '1' && cp <= '9' {
			return cp - '1'
		}
	}
	return -1
}

// isCtrlTab checks if a CSI sequence is Ctrl+Tab.
func isCtrlTab(seq []byte) bool {
	if len(seq) < 4 || seq[0] != 0x1b || seq[1] != '[' {
		return false
	}
	final := seq[len(seq)-1]
	body := string(seq[2 : len(seq)-1])

	switch final {
	case 'u': // CSI u: \x1b[9;5u
		parts := strings.Split(body, ";")
		if len(parts) < 2 || isReleaseEvent(parts[1]) {
			return false
		}
		return parts[0] == "9" && modifierHasCtrl(parts[1])
	case '~': // xterm: \x1b[27;5;9~
		parts := strings.Split(body, ";")
		if len(parts) < 3 || parts[0] != "27" || isReleaseEvent(parts[1]) {
			return false
		}
		return parts[2] == "9" && modifierHasCtrl(parts[1])
	}
	return false
}

func modifierHasCtrl(s string) bool {
	mod := parseModifierValue(s)
	if mod <= 0 {
		return false
	}
	return ((mod - 1) & 4) != 0
}

func parseModifierValue(s string) int {
	if idx := strings.IndexByte(s, ':'); idx >= 0 {
		s = s[:idx]
	}

	mod, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}

	return mod
}

func decodeCommandByte(token []byte) (byte, bool) {
	if len(token) == 1 {
		return token[0], true
	}

	if len(token) < 4 || token[0] != 0x1b || token[1] != '[' {
		return 0, false
	}

	final := token[len(token)-1]
	body := string(token[2 : len(token)-1])

	switch final {
	case 'u':
		parts := strings.Split(body, ";")
		if len(parts) < 1 || (len(parts) >= 2 && isReleaseEvent(parts[1])) {
			return 0, false
		}
		cp, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, false
		}
		return decodeASCIICommandCodepoint(cp)
	case '~':
		parts := strings.Split(body, ";")
		if len(parts) < 3 || parts[0] != "27" || isReleaseEvent(parts[1]) {
			return 0, false
		}
		cp, err := strconv.Atoi(parts[2])
		if err != nil {
			return 0, false
		}
		return decodeASCIICommandCodepoint(cp)
	default:
		return 0, false
	}
}

// isCSIu checks if a token is a kitty keyboard protocol CSI u sequence.
func isCSIu(seq []byte) bool {
	return len(seq) >= 4 && seq[0] == 0x1b && seq[1] == '[' && seq[len(seq)-1] == 'u'
}

// decodeCSIu translates a kitty keyboard protocol CSI u sequence into standard
// terminal input bytes suitable for forwarding to a PTY. Returns nil for key
// release events (event_type 3) which should be silently dropped.
func decodeCSIu(seq []byte) []byte {
	body := string(seq[2 : len(seq)-1])
	parts := strings.Split(body, ";")
	if len(parts) < 1 {
		return nil
	}

	cpStr := parts[0]
	// Strip sub-parameters (e.g. shifted-key, base-layout-key)
	if idx := strings.IndexByte(cpStr, ':'); idx >= 0 {
		cpStr = cpStr[:idx]
	}
	cp, err := strconv.Atoi(cpStr)
	if err != nil || cp <= 0 || cp > 0x10FFFF {
		return nil
	}

	mod := 1
	if len(parts) >= 2 {
		modField := parts[1]
		// Check for release event type (:3)
		if idx := strings.IndexByte(modField, ':'); idx >= 0 {
			if evtStr := modField[idx+1:]; evtStr == "3" {
				return nil // drop release events
			}
		}
		if m := parseModifierValue(modField); m > 0 {
			mod = m
		}
	}

	hasCtrl := ((mod - 1) & 4) != 0
	hasAlt := ((mod - 1) & 2) != 0

	var ch []byte
	switch {
	case hasCtrl && cp >= 'a' && cp <= 'z':
		ch = []byte{byte(cp - 'a' + 1)}
	case hasCtrl && cp >= 'A' && cp <= 'Z':
		ch = []byte{byte(cp - 'A' + 1)}
	case hasCtrl && cp == ' ':
		ch = []byte{0}
	case cp == 0x1b:
		ch = []byte{0x1b}
	case cp == '\r' || cp == '\n' || cp == '\t' || cp == 0x7f:
		ch = []byte{byte(cp)}
	case cp < 128:
		ch = []byte{byte(cp)}
	default:
		// Unicode codepoint — encode as UTF-8
		ch = []byte(string(rune(cp)))
	}

	if hasAlt {
		ch = append([]byte{0x1b}, ch...)
	}

	return ch
}

func decodeASCIICommandCodepoint(cp int) (byte, bool) {
	switch {
	case cp == 0x1b || cp == '\r' || cp == '\n' || cp == '\t':
		return byte(cp), true
	case cp >= 0x20 && cp <= 0x7e:
		return byte(cp), true
	default:
		return 0, false
	}
}

func parseMouseEvent(seq []byte) (mouseEvent, bool) {
	if len(seq) < 6 || seq[0] != 0x1b || seq[1] != '[' {
		return mouseEvent{}, false
	}

	if seq[2] == 'M' {
		cb := int(seq[3]) - 32
		x := int(seq[4]) - 32
		y := int(seq[5]) - 32
		if cb < 0 || x < 1 || y < 1 {
			return mouseEvent{}, false
		}
		return mouseEvent{
			x:      x,
			y:      y,
			button: cb & 0x03,
			press:  true,
			motion: cb&0x20 != 0,
			wheel:  cb&0x40 != 0,
		}, true
	}

	if seq[2] != '<' {
		return mouseEvent{}, false
	}

	final := seq[len(seq)-1]
	if final != 'M' && final != 'm' {
		return mouseEvent{}, false
	}

	body := string(seq[3 : len(seq)-1])
	parts := strings.Split(body, ";")
	if len(parts) != 3 {
		return mouseEvent{}, false
	}

	cb, err := strconv.Atoi(parts[0])
	if err != nil {
		return mouseEvent{}, false
	}
	x, err := strconv.Atoi(parts[1])
	if err != nil {
		return mouseEvent{}, false
	}
	y, err := strconv.Atoi(parts[2])
	if err != nil || x < 1 || y < 1 {
		return mouseEvent{}, false
	}

	return mouseEvent{
		x:      x,
		y:      y,
		button: cb & 0x03,
		press:  final == 'M',
		motion: cb&0x20 != 0,
		wheel:  cb&0x40 != 0,
	}, true
}

func (tm *TabManager) isCommandMode() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.commandMode
}

func (tm *TabManager) shouldExitWithoutTabs() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return len(tm.tabs) == 0 && !tm.commandMode
}

func (tm *TabManager) emitNotice(level TabNoticeLevel, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}

	tm.mu.Lock()
	tm.noticeText = message
	tm.noticeLevel = level
	tm.noticeSticky = level == TabNoticeError
	if level == TabNoticeError {
		tm.noticeUntil = time.Now().Add(8 * time.Second)
		tm.commandMode = true
	} else {
		tm.noticeUntil = time.Now().Add(3 * time.Second)
	}
	tm.needsRender = true
	hasTabs := len(tm.tabs) > 0
	tm.mu.Unlock()

	if !hasTabs {
		tm.drawTabBar()
	}
}

func (tm *TabManager) clearNotice() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.noticeText == "" {
		return false
	}
	tm.noticeText = ""
	tm.noticeLevel = TabNoticeInfo
	tm.noticeUntil = time.Time{}
	tm.noticeSticky = false
	tm.needsRender = true
	return true
}

func (tm *TabManager) hasStickyErrorNotice() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.noticeText != "" && tm.noticeSticky && tm.noticeLevel == TabNoticeError
}

func (tm *TabManager) clearNoticeAndStayInCommandMode() {
	hadNotice := tm.clearNotice()
	if tm.hasTabs() {
		tm.enterCommandMode()
		return
	}
	if hadNotice {
		tm.drawTabBar()
	}
}

func (tm *TabManager) clearNoticeAndResume() {
	hadNotice := tm.clearNotice()
	if tm.hasTabs() {
		tm.exitCommandMode()
		return
	}
	if hadNotice {
		tm.drawTabBar()
	}
}

func (tm *TabManager) hasTabs() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return len(tm.tabs) > 0
}

func (tm *TabManager) enterCommandMode() {
	tm.mu.Lock()
	tm.commandMode = true
	tm.needsRender = true
	hasTabs := len(tm.tabs) > 0
	tm.mu.Unlock()

	if !hasTabs {
		tm.drawTabBar()
	}
}

func (tm *TabManager) exitCommandMode() {
	tm.mu.Lock()
	tm.commandMode = false
	tm.needsRender = true
	tm.mu.Unlock()
}

func (tm *TabManager) handleCommandKey(ctx context.Context, newTabFn NewTabFunc, termState *term.State, b byte) {
	switch {
	case b == ' ':
		if tm.hasStickyErrorNotice() {
			tm.clearNoticeAndStayInCommandMode()
			return
		}
		tm.clearNoticeAndResume()
	case b == prefix || b == 0x1b || b == '\r' || b == '\n':
		if tm.hasStickyErrorNotice() {
			if b == 0x1b {
				tm.clearNoticeAndStayInCommandMode()
			}
			return
		}
		if !tm.hasTabs() {
			tm.drawTabBar()
			return
		}
		tm.exitCommandMode()
	case b >= '1' && b <= '9':
		tm.switchTabAndResume(int(b - '1'))
	case b == 'n' || b == 'N':
		tm.openNewTab(ctx, newTabFn, termState)
	case isEditSessionKey(b):
		tm.openCurrentTabChooser(ctx, newTabFn, termState)
	case b == 'x' || b == 'X' || b == 'w' || b == 'W':
		tm.closeCurrentTab()
	case b == 'l' || b == 'L' || b == 'j' || b == 'J' || b == '\t':
		tm.moveTabSelection(1)
	case b == 'h' || b == 'H' || b == 'k' || b == 'K' || b == 'p' || b == 'P':
		tm.moveTabSelection(-1)
	}
}

func isEditSessionKey(b byte) bool {
	return b == 'e' || b == 'E'
}

// switchTabAndResume exits command mode and activates the selected tab.
func (tm *TabManager) switchTabAndResume(idx int) {
	tm.mu.Lock()
	if idx < 0 || idx >= len(tm.tabs) {
		tm.mu.Unlock()
		return
	}
	tm.commandMode = false
	tm.activeIdx = idx
	tab := tm.tabs[idx]
	tab.statusMu.Lock()
	tab.bell = false
	tab.statusMu.Unlock()
	tm.needsRender = true
	tm.mu.Unlock()
}

// forwardToActive writes bytes to the active tab's PTY.
func (tm *TabManager) forwardToActive(data []byte) {
	tm.mu.Lock()
	var ptmx *os.File
	var tab *Tab
	if tm.activeIdx >= 0 && tm.activeIdx < len(tm.tabs) {
		tab = tm.tabs[tm.activeIdx]
		ptmx = tab.ptmx
		if isLikelyTypingInput(data) {
			noteTabUserInput(tab, time.Now())
		}
	}
	tm.mu.Unlock()
	tm.traceClaudeCursorBytes(tab, "stdin_forward", data)
	if ptmx != nil {
		_, _ = ptmx.Write(data)
	}
}

func isLikelyTypingInput(data []byte) bool {
	if len(data) == 0 {
		return false
	}

	if data[0] == 0x1b {
		return len(data) > 1 && isLikelyTypingInput(data[1:])
	}

	if len(data) == 1 {
		switch data[0] {
		case '\r', '\n', '\t', 0x7f:
			return true
		}
		return data[0] >= 0x20 && data[0] != 0x7f
	}

	r, _ := utf8.DecodeRune(data)
	return r != utf8.RuneError && !unicode.IsControl(r) && unicode.IsPrint(r)
}

func (tm *TabManager) handleActiveCtrlC(data []byte) bool {
	if len(data) == 1 && data[0] == 0x03 {
		// If the active tab's process has exited, one Ctrl+C is enough.
		tm.mu.Lock()
		var tab *Tab
		if tm.activeIdx >= 0 && tm.activeIdx < len(tm.tabs) {
			tab = tm.tabs[tm.activeIdx]
		}
		tm.mu.Unlock()
		if tab != nil && tab.exited.Load() {
			tm.resetCtrlC()
			tm.closeCurrentTab()
			return true
		}

		if tm.noteCtrlC(time.Now()) {
			tm.resetCtrlC()
			tm.closeCurrentTab()
			return true
		}
		return false
	}

	tm.resetCtrlC()
	return false
}

func (tm *TabManager) noteCtrlC(now time.Time) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	forceClose := !tm.lastCtrlCAt.IsZero() && now.Sub(tm.lastCtrlCAt) <= tabCtrlCExitWindow
	tm.lastCtrlCAt = now
	return forceClose
}

func (tm *TabManager) resetCtrlC() {
	tm.mu.Lock()
	tm.lastCtrlCAt = time.Time{}
	tm.mu.Unlock()
}

// switchTab activates the tab at the given index.
func (tm *TabManager) switchTab(idx int) {
	tm.mu.Lock()
	if idx < 0 || idx >= len(tm.tabs) || idx == tm.activeIdx {
		tm.mu.Unlock()
		return
	}
	tm.activeIdx = idx
	clickedTab := tm.tabs[idx]
	clickedTab.statusMu.Lock()
	clickedTab.bell = false
	clickedTab.statusMu.Unlock()
	tm.needsRender = true
	tm.mu.Unlock()
}

func (tm *TabManager) handleTabBarMouseEvent(ev mouseEvent) bool {
	if !ev.press || ev.motion || ev.wheel || ev.button != 0 {
		return false
	}

	tm.mu.Lock()
	row := int(tm.rows)
	commandMode := tm.commandMode
	tabs := make([]*Tab, len(tm.tabs))
	copy(tabs, tm.tabs)
	tm.mu.Unlock()

	if ev.y != row {
		return false
	}

	idx := tabBarTabIndexAtColumn(tabs, ev.x)
	if idx < 0 {
		return false
	}

	if commandMode {
		tm.switchTabAndResume(idx)
	} else {
		tm.switchTab(idx)
	}

	return true
}

// cycleTab moves to the next tab, wrapping around.
func (tm *TabManager) cycleTab() {
	tm.mu.Lock()
	if len(tm.tabs) <= 1 {
		tm.mu.Unlock()
		return
	}
	next := (tm.activeIdx + 1) % len(tm.tabs)
	tm.activeIdx = next
	tm.needsRender = true
	tm.mu.Unlock()
}

func (tm *TabManager) moveTabSelection(delta int) {
	tm.mu.Lock()
	if len(tm.tabs) == 0 {
		tm.mu.Unlock()
		return
	}
	next := (tm.activeIdx + delta + len(tm.tabs)) % len(tm.tabs)
	tm.activeIdx = next
	tm.needsRender = true
	tm.mu.Unlock()
}

// closeAllTabs sends SIGHUP to every tab's process for a clean exit.
func (tm *TabManager) closeAllTabs() {
	tm.mu.Lock()
	tabs := make([]*Tab, len(tm.tabs))
	copy(tabs, tm.tabs)
	tm.mu.Unlock()

	for _, tab := range tabs {
		if tab.cmd != nil && tab.cmd.Cmd.Process != nil && !tab.exited.Load() {
			tab.cmd.Cmd.Process.Signal(syscall.SIGHUP)
		}
	}

	// Wait briefly for processes to exit gracefully
	timeout := time.After(500 * time.Millisecond)
	for _, tab := range tabs {
		select {
		case <-tab.done:
		case <-timeout:
			return
		}
	}
}

// closeCurrentTab sends SIGHUP to the active tab's process.
func (tm *TabManager) closeCurrentTab() {
	tm.mu.Lock()
	if tm.activeIdx >= len(tm.tabs) {
		tm.mu.Unlock()
		return
	}
	tab := tm.tabs[tm.activeIdx]
	tm.mu.Unlock()

	if tab.cmd != nil && tab.cmd.Cmd.Process != nil && !tab.exited.Load() {
		tab.cmd.Cmd.Process.Signal(syscall.SIGHUP)
	}
}

func (tm *TabManager) signalClose() {
	tm.closeOnce.Do(func() { close(tm.closeCh) })
}

// removeTab removes a dead tab and switches to an adjacent one.
func (tm *TabManager) removeTab(tab *Tab) {
	tm.mu.Lock()
	tm.lastCtrlCAt = time.Time{}
	idx := -1
	for i, t := range tm.tabs {
		if t == tab {
			idx = i
			break
		}
	}
	if idx < 0 {
		tm.mu.Unlock()
		return
	}
	tm.tabs = append(tm.tabs[:idx], tm.tabs[idx+1:]...)

	if len(tm.tabs) == 0 {
		tm.mu.Unlock()
		tm.signalClose()
		return
	}

	// Adjust active index
	if tm.activeIdx >= len(tm.tabs) {
		tm.activeIdx = len(tm.tabs) - 1
	}
	tm.needsRender = true
	tm.mu.Unlock()
}

// openNewTab pauses PTY rendering, restores the terminal, runs the chooser,
// then resumes the multiplexer with the new tab.
func (tm *TabManager) openNewTab(ctx context.Context, newTabFn NewTabFunc, termState *term.State) {
	if newTabFn == nil {
		return
	}

	pendingTab, prevActive := tm.addPendingTab()
	spec, err := tm.runChooser(ctx, newTabFn, termState, nil)

	if err != nil || spec == nil {
		// Cancelled — remove the placeholder tab and resume the previous session.
		activeTab := tm.removePendingTab(pendingTab, prevActive)

		// Ctrl+B → enter command mode so the user lands on the tab bar.
		if errors.Is(err, ErrBackToTabs) {
			tm.enterCommandMode()
			return
		}
		if err != nil {
			tm.emitNotice(TabNoticeError, err.Error())
		}

		if activeTab != nil {
			tm.mu.Lock()
			tm.needsRender = true
			tm.mu.Unlock()
		} else {
			tm.drawTabBar()
		}
		return
	}

	activeTab := tm.removePendingTab(pendingTab, prevActive)
	_ = activeTab

	// Add the new tab
	if err := tm.addTab(ctx, *spec); err != nil {
		tm.emitNotice(TabNoticeError, fmt.Sprintf("new tab failed: %v", err))
		if activeTab != nil {
			tm.mu.Lock()
			tm.needsRender = true
			tm.mu.Unlock()
		} else {
			tm.drawTabBar()
		}
		return
	}

	// Resume — always exit command mode so the new tab renders.
	tm.mu.Lock()
	tm.paused = false
	tm.commandMode = false
	tm.needsRender = true
	tm.mu.Unlock()
}

func (tm *TabManager) openCurrentTabChooser(ctx context.Context, newTabFn NewTabFunc, termState *term.State) {
	if newTabFn == nil {
		return
	}

	currentTab, current, ok := tm.activeTabSeed()
	if !ok {
		return
	}

	tm.mu.Lock()
	tm.paused = true
	tm.commandMode = false
	tm.needsRender = true
	tm.mu.Unlock()

	spec, err := tm.runChooser(ctx, newTabFn, termState, &current)
	if err != nil || spec == nil {
		tm.mu.Lock()
		tm.paused = false
		tm.needsRender = true
		tm.mu.Unlock()

		if errors.Is(err, ErrBackToTabs) {
			tm.enterCommandMode()
			return
		}
		if err != nil {
			tm.emitNotice(TabNoticeError, err.Error())
		}
		return
	}

	if err := tm.replaceTab(ctx, currentTab, *spec); err != nil {
		tm.mu.Lock()
		tm.paused = false
		tm.needsRender = true
		tm.mu.Unlock()
		tm.emitNotice(TabNoticeError, fmt.Sprintf("tab relaunch failed: %v", err))
		return
	}

	tm.mu.Lock()
	tm.paused = false
	tm.commandMode = false
	tm.needsRender = true
	tm.mu.Unlock()
}

func (tm *TabManager) activeTabSeed() (*Tab, LaunchSpec, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.activeIdx < 0 || tm.activeIdx >= len(tm.tabs) {
		return nil, LaunchSpec{}, false
	}
	tab := tm.tabs[tm.activeIdx]
	return tab, tab.spec, true
}

func (tm *TabManager) replaceTab(ctx context.Context, target *Tab, spec LaunchSpec) error {
	newTab, err := tm.createTab(ctx, spec)
	if err != nil {
		return err
	}

	var oldTab *Tab
	tm.mu.Lock()
	replaceIdx := -1
	for i, tab := range tm.tabs {
		if tab == target {
			replaceIdx = i
			break
		}
	}
	if replaceIdx >= 0 {
		oldTab = tm.tabs[replaceIdx]
		tm.tabs[replaceIdx] = newTab
		tm.activeIdx = replaceIdx
	} else {
		tm.tabs = append(tm.tabs, newTab)
		tm.activeIdx = len(tm.tabs) - 1
	}
	tm.needsRender = true
	tm.mu.Unlock()

	tm.startTab(newTab)

	if oldTab != nil && oldTab.cmd != nil && oldTab.cmd.Cmd.Process != nil && !oldTab.exited.Load() {
		_ = oldTab.cmd.Cmd.Process.Signal(syscall.SIGHUP)
	}

	return nil
}

func (tm *TabManager) runChooser(ctx context.Context, newTabFn NewTabFunc, termState *term.State, seed *LaunchSpec) (*LaunchSpec, error) {
	fullscreenChooser := prefersFullscreenChooser()

	// Clear screen for the chooser. On most terminals, show the tab bar
	// above the chooser; Apple Terminal gets a full-screen render.
	tm.resetHostInputModes()
	tm.writeString("\x1b[2J\x1b[H")
	if !fullscreenChooser {
		tm.drawTabBar()
	}
	if termState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), termState)
	}

	// Pause the stdinCh goroutine so Bubble Tea can own os.Stdin exclusively.
	// SetReadDeadline on the dup'd non-blocking fd forces the blocked Read
	// to return immediately, so the goroutine enters its sleep loop without
	// eating the user's next keystroke.
	tm.stdinPaused.Store(true)
	if tm.stdinPollFd != nil {
		_ = tm.stdinPollFd.SetReadDeadline(time.Now())
	}
	time.Sleep(20 * time.Millisecond) // let goroutine wake and enter sleep loop
	for {
		select {
		case <-tm.stdinCh:
		default:
			goto drained
		}
	}
drained:

	spec, err := newTabFn(ctx, tm.emitNotice, nil, seed)

	if tm.stdinPollFd != nil {
		_ = tm.stdinPollFd.SetReadDeadline(time.Time{})
	}
	tm.stdinPaused.Store(false)

	if termState != nil {
		_, _ = term.MakeRaw(int(os.Stdin.Fd()))
	}
	tm.writeString("\x1b[r")

	return spec, err
}

func prefersFullscreenChooser() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TERM_PROGRAM")), "Apple_Terminal")
}

// ─────────────────────────────────────────────────────────────────────────────
// SGR sanitizer — rewrites colon sub-parameters for vt10x compatibility
// ─────────────────────────────────────────────────────────────────────────────

// sanitizeSGR rewrites SGR sequences that contain colon-separated sub-parameters
// (e.g. \x1b[4:3m for curly underline, \x1b[38:2::100:150:200m for true-color)
// into semicolon-separated equivalents that vt10x's CSI parser can handle.
//
// vt10x uses strconv.Atoi on each semicolon-delimited parameter. A colon in any
// parameter causes Atoi to fail and the parser to BREAK, discarding all remaining
// parameters in the sequence — including colors that follow.
func sanitizeSGR(data []byte) []byte {
	// Fast path: no colons at all → nothing to fix.
	if !bytes.ContainsRune(data, ':') {
		return data
	}

	result := make([]byte, 0, len(data)+32)
	i := 0
	for i < len(data) {
		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '[' {
			// Found CSI start — find the final byte.
			start := i
			j := i + 2
			for j < len(data) && data[j] < 0x40 {
				j++
			}
			if j >= len(data) {
				// Partial CSI at end of buffer — pass through as-is.
				result = append(result, data[i:]...)
				return result
			}
			if data[j] == 'm' && bytes.ContainsRune(data[i+2:j], ':') {
				// SGR with colon params — rewrite.
				result = append(result, 0x1b, '[')
				result = append(result, rewriteSGRParams(data[i+2:j])...)
				result = append(result, 'm')
			} else {
				// Not SGR or no colons — pass through.
				result = append(result, data[start:j+1]...)
			}
			i = j + 1
		} else {
			result = append(result, data[i])
			i++
		}
	}
	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// Frame-based rendering
// ─────────────────────────────────────────────────────────────────────────────

// renderLoop drives frame rendering at ~30fps. It reads the active tab's VT
// emulator state and composites it with the tab bar into a single atomic frame.
// A slower status tick (~2s) forces redraws so tab status indicators update
// when output stops (e.g. generating → waiting transition).
func (tm *TabManager) renderLoop() {
	frameTicker := time.NewTicker(33 * time.Millisecond)
	statusTicker := time.NewTicker(2 * time.Second)
	defer frameTicker.Stop()
	defer statusTicker.Stop()
	for {
		select {
		case <-frameTicker.C:
			tm.expireNoticeIfNeeded()
			tm.renderFrame()
		case <-statusTicker.C:
			tm.mu.Lock()
			tm.needsRender = true
			tm.mu.Unlock()
		case <-tm.closeCh:
			return
		}
	}
}

func (tm *TabManager) expireNoticeIfNeeded() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.noticeText == "" || tm.noticeSticky || tm.noticeUntil.IsZero() || time.Now().Before(tm.noticeUntil) {
		return
	}
	tm.noticeText = ""
	tm.noticeLevel = TabNoticeInfo
	tm.noticeUntil = time.Time{}
	tm.needsRender = true
}

// renderFrame composites the active tab's VT screen content with the tab bar
// into a single write, using synchronized output to prevent tearing.
func (tm *TabManager) renderFrame() {
	tm.mu.Lock()
	if !tm.needsRender || tm.paused {
		tm.mu.Unlock()
		return
	}
	tm.needsRender = false
	if tm.activeIdx >= len(tm.tabs) {
		tm.mu.Unlock()
		return
	}
	tab := tm.tabs[tm.activeIdx]
	if tab.vt == nil {
		tm.mu.Unlock()
		return
	}
	rows := int(tm.rows)
	cols := int(tm.cols)
	tm.mu.Unlock()

	contentRows := rows - 1
	if contentRows < 1 {
		contentRows = 1
	}

	// Read screen state from VT emulator under its lock.
	// When cursor is visible, save its position — this is the child's
	// intended cursor location (after CUP + \x1b[?25h). We'll use this
	// saved position for rendering even when a subsequent hide+content
	// write has moved the live cursor to end-of-content.
	tab.vt.Lock()
	screenContent := renderVTScreen(tab.vt, cols, contentRows)
	cursor := tab.vt.Cursor()
	vtCursorVisible := tab.vt.CursorVisible()
	vtMode := tab.vt.Mode()
	tab.vt.Unlock()

	curX, curY, showCursor := resolveRenderCursor(
		tab,
		cursor.X,
		cursor.Y,
		vtCursorVisible,
		tab.cursorVisible.Load(),
	)
	tm.traceClaudeCursorf(tab, "render_decision vt_cursor=(%d,%d) vt_visible=%t raw_visible=%t saved_valid=%t saved=(%d,%d) render=(%d,%d) show=%t",
		cursor.X, cursor.Y, vtCursorVisible, tab.cursorVisible.Load(),
		tab.cursorSavedValid.Load(), tab.cursorSavedX.Load(), tab.cursorSavedY.Load(),
		curX, curY, showCursor)
	if tm.isCommandMode() && vtMode&vt10x.ModeMouseMask == 0 {
		vtMode |= vt10x.ModeMouseX10
	}

	tabBar := tm.buildTabBarString()

	// Composite the full frame
	var frame strings.Builder
	frame.Grow(len(screenContent) + len(tabBar) + 128)
	frame.WriteString(tm.syncHostInputModes(vtMode))
	frame.WriteString("\x1b[?2026h")        // begin synchronized update
	frame.WriteString("\x1b[?25l")          // hide cursor during render
	frame.WriteString("\x1b[r")             // reset scroll region (clear any DECSTBM left by child/chooser)
	frame.WriteString("\x1b[H")             // cursor to home (top-left)
	frame.WriteString(screenContent)        // VT emulator content (rows-1 lines)
	fmt.Fprintf(&frame, "\x1b[%d;1H", rows) // position on last row
	frame.WriteString(tabBar)               // tab bar

	// Position cursor using the render-resolution policy for the active tab.
	fmt.Fprintf(&frame, "\x1b[%d;%dH", curY+1, curX+1)
	if showCursor {
		if shape := tab.cursorShape.Load(); shape >= 0 {
			fmt.Fprintf(&frame, "\x1b[%d q", shape)
		}
		frame.WriteString("\x1b[?25h")
	}
	frame.WriteString("\x1b[?2026l") // end synchronized update

	tm.writeString(frame.String())
}

// VT attribute flags — mirrors unexported vt10x constants.
const (
	vtAttrReverse   int16 = 1 << 0
	vtAttrUnderline int16 = 1 << 1
	vtAttrBold      int16 = 1 << 2
	vtAttrItalic    int16 = 1 << 4
	vtAttrBlink     int16 = 1 << 5
)

// renderVTScreen extracts styled content from a VT emulator's cell grid,
// producing ANSI-escaped output suitable for writing to the real terminal.
// The caller must hold the VT emulator's lock.
func renderVTScreen(vt vt10x.View, cols, rows int) string {
	var b strings.Builder
	b.Grow(cols * rows * 3)

	vtCols, vtRows := vt.Size()

	var prevFG, prevBG vt10x.Color
	var prevMode int16
	firstCell := true

	for y := 0; y < rows; y++ {
		if y > 0 {
			b.WriteString("\x1b[0m\r\n")
			prevFG, prevBG, prevMode = 0, 0, 0
			firstCell = true
		}
		for x := 0; x < cols; x++ {
			if y < vtRows && x < vtCols {
				g := vt.Cell(x, y)

				if firstCell || g.FG != prevFG || g.BG != prevBG || g.Mode != prevMode {
					writeStyleSequence(&b, g)
					prevFG, prevBG, prevMode = g.FG, g.BG, g.Mode
					firstCell = false
				}

				ch := g.Char
				if ch == 0 {
					ch = ' '
				}
				b.WriteRune(ch)
			} else {
				// Outside VT grid — emit a blank with default style
				if firstCell || prevFG != vt10x.DefaultFG || prevBG != vt10x.DefaultBG || prevMode != 0 {
					b.WriteString("\x1b[0m")
					prevFG, prevBG, prevMode = vt10x.DefaultFG, vt10x.DefaultBG, 0
					firstCell = false
				}
				b.WriteByte(' ')
			}
		}
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// writeStyleSequence emits an SGR reset + attribute/color sequence for a glyph.
// vt10x pre-applies reverse to stored colors, except the default-FG/default-BG
// case where a reverse-video space must still emit SGR 7 to stay visible.
func writeStyleSequence(b *strings.Builder, g vt10x.Glyph) {
	b.WriteString("\x1b[0")

	if g.Mode&vtAttrReverse != 0 && g.FG == vt10x.DefaultBG && g.BG == vt10x.DefaultFG {
		b.WriteString(";7")
	}
	if g.Mode&vtAttrBold != 0 {
		b.WriteString(";1")
	}
	if g.Mode&vtAttrItalic != 0 {
		b.WriteString(";3")
	}
	if g.Mode&vtAttrUnderline != 0 {
		b.WriteString(";4")
	}
	if g.Mode&vtAttrBlink != 0 {
		b.WriteString(";5")
	}

	writeColor(b, g.FG, false)
	writeColor(b, g.BG, true)

	b.WriteByte('m')
}

// writeColor appends an SGR color parameter for a vt10x Color value.
func writeColor(b *strings.Builder, c vt10x.Color, bg bool) {
	if c == vt10x.DefaultFG || c == vt10x.DefaultBG || c == vt10x.DefaultCursor {
		return // default — omit, reset already handles it
	}
	if bg {
		switch {
		case c < 8:
			fmt.Fprintf(b, ";%d", 40+c)
		case c < 16:
			fmt.Fprintf(b, ";%d", 100+c-8)
		case c < 256:
			fmt.Fprintf(b, ";48;5;%d", c)
		default:
			fmt.Fprintf(b, ";48;2;%d;%d;%d", (c>>16)&0xFF, (c>>8)&0xFF, c&0xFF)
		}
	} else {
		switch {
		case c < 8:
			fmt.Fprintf(b, ";%d", 30+c)
		case c < 16:
			fmt.Fprintf(b, ";%d", 90+c-8)
		case c < 256:
			fmt.Fprintf(b, ";38;5;%d", c)
		default:
			fmt.Fprintf(b, ";38;2;%d;%d;%d", (c>>16)&0xFF, (c>>8)&0xFF, c&0xFF)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tab bar rendering
// ─────────────────────────────────────────────────────────────────────────────

const tabProgressWindow = 1500 * time.Millisecond
const tabProgressConsistencyWindow = 3 * time.Second
const tabStartingWindow = 5 * time.Second
const tabCtrlCExitWindow = 1500 * time.Millisecond
const tabTypingWindow = time.Second

// buildTabBarString returns the styled tab bar content (one row of ANSI-styled
// text) without any cursor positioning. Used by both renderFrame and drawTabBar.
// tabStatusEmoji returns a status indicator emoji for the given tab.
// Priority: ⏳ startup > 🔔 notification > 🧠 progress > ✅ idle/ready.
// The tab's VT lock must NOT be held by the caller.
func tabStatusEmoji(tab *Tab) string {
	now := time.Now()

	if tab.exited.Load() || tab.vt == nil {
		return ""
	}
	if !tab.startedAt.IsZero() && now.Sub(tab.startedAt) < tabStartingWindow {
		return "⏳"
	}
	tab.statusMu.RLock()
	bell := tab.bell
	tab.statusMu.RUnlock()
	if bell {
		return "🔔"
	}
	if tabHasRecentScreenProgress(tab, now) && !tabHasRecentUserInput(tab, now) {
		return "🧠"
	}
	return "✅"
}

func noteTabScreenChange(tab *Tab, now time.Time) bool {
	if tab == nil || tab.vt == nil {
		return false
	}

	hash := hashVTScreen(tab.vt)
	tab.statusMu.Lock()
	defer tab.statusMu.Unlock()
	if hash == tab.screenHash {
		return false
	}

	if tab.screenHash != 0 {
		tab.prevScreenChange = tab.lastScreenChange
	}
	tab.screenHash = hash
	tab.lastScreenChange = now
	return true
}

func tabHasRecentScreenProgress(tab *Tab, now time.Time) bool {
	if tab == nil {
		return false
	}
	tab.statusMu.RLock()
	defer tab.statusMu.RUnlock()
	if tab.lastScreenChange.IsZero() || tab.prevScreenChange.IsZero() {
		return false
	}
	return now.Sub(tab.lastScreenChange) <= tabProgressWindow &&
		now.Sub(tab.prevScreenChange) <= tabProgressConsistencyWindow
}

func noteTabUserInput(tab *Tab, now time.Time) {
	if tab == nil {
		return
	}
	tab.lastUserInputAt.Store(now.UnixNano())
}

func tabHasRecentUserInput(tab *Tab, now time.Time) bool {
	if tab == nil {
		return false
	}

	last := tab.lastUserInputAt.Load()
	if last == 0 {
		return false
	}

	return now.Sub(time.Unix(0, last)) <= tabTypingWindow
}

func hashVTScreen(vt vt10x.Terminal) uint64 {
	if vt == nil {
		return 0
	}

	vt.Lock()
	defer vt.Unlock()

	h := fnv.New64a()
	cols, rows := vt.Size()
	cursor := vt.Cursor()
	cursorVisible := vt.CursorVisible()

	var buf [8]byte
	writeUint64 := func(v uint64) {
		for i := 0; i < 8; i++ {
			buf[i] = byte(v >> (8 * i))
		}
		_, _ = h.Write(buf[:])
	}

	writeUint64(uint64(cols))
	writeUint64(uint64(rows))
	writeUint64(uint64(cursor.X))
	writeUint64(uint64(cursor.Y))
	if cursorVisible {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}

	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			g := vt.Cell(x, y)
			writeUint64(uint64(g.Char))
			writeUint64(uint64(g.FG))
			writeUint64(uint64(g.BG))
			writeUint64(uint64(g.Mode))
		}
	}

	return h.Sum64()
}

func (tm *TabManager) buildTabBarString() string {
	tm.mu.Lock()
	tabs := make([]*Tab, len(tm.tabs))
	copy(tabs, tm.tabs)
	active := tm.activeIdx
	cols := int(tm.cols)
	cmdMode := tm.commandMode
	noticeText := tm.noticeText
	noticeLevel := tm.noticeLevel
	tm.mu.Unlock()

	var b strings.Builder

	// Background color: blue in command mode, dark gray normally.
	bg := "\x1b[48;5;236m"
	if cmdMode {
		bg = "\x1b[48;5;22m"
	}
	if noticeText != "" && noticeLevel == TabNoticeError {
		bg = "\x1b[48;5;88m"
	}
	reset := "\x1b[0m"
	versionLabel := ""
	if tm.version != "" && noticeText == "" {
		versionLabel = " " + normalizedVersionLabel(tm.version) + " "
	}

	b.WriteString("\x1b[2K")
	b.WriteString(bg)
	b.WriteString(tabBarBrandString(bg))

	if len(tabs) == 0 {
		activeBg := "\x1b[0;48;5;238;1;37m"
		if cmdMode {
			activeBg = "\x1b[0;48;5;28;1;37m"
		}
		b.WriteString(activeBg + " Home ")
	} else {
		for i, tab := range tabs {
			status := tabStatusEmoji(tab)
			var label string
			if status != "" {
				label = fmt.Sprintf(" %s %d:%s ", status, i+1, tab.label)
			} else {
				label = fmt.Sprintf(" %d:%s ", i+1, tab.label)
			}
			if i == active {
				activeBg := "\x1b[0;48;5;238;1;37m"
				if cmdMode {
					activeBg = "\x1b[0;48;5;28;1;37m"
				}
				b.WriteString(activeBg)
			} else if tab.exited.Load() {
				b.WriteString(bg + "\x1b[2;37m")
			} else {
				b.WriteString(bg + "\x1b[37m")
			}
			b.WriteString(label)
		}
	}

	hint := " ^B tab mode "
	if noticeText != "" {
		if noticeLevel == TabNoticeError {
			hint = " error: " + noticeText + "  Esc: clear "
		} else {
			hint = " " + noticeText + " "
		}
	} else if cmdMode {
		hint = " n:new e:edit session x:close h/l:move 1-9:jump Esc:resume "
	}
	used := tm.tabBarContentWidth(tabs) + len(hint) + len(versionLabel)
	if cols > used {
		b.WriteString(bg + strings.Repeat(" ", cols-used))
	}
	b.WriteString(bg + "\x1b[2m" + hint)
	if versionLabel != "" {
		b.WriteString(bg + "\x1b[36m" + versionLabel)
	}
	b.WriteString(reset)

	return b.String()
}

// drawTabBar renders the tab bar on the last terminal row as a standalone
// operation (with cursor save/restore). Used during the chooser flow and
// initial startup before the render loop takes over.
func (tm *TabManager) drawTabBar() {
	tm.mu.Lock()
	rows := tm.rows
	tm.mu.Unlock()

	content := tm.buildTabBarString()

	var b strings.Builder
	b.WriteString(tm.syncHostInputModes(0))
	b.WriteString("\x1b[s")
	fmt.Fprintf(&b, "\x1b[%d;1H", rows)
	b.WriteString(content)
	b.WriteString("\x1b[u")

	tm.writeString(b.String())
}

// handleResize updates dimensions, resizes all VT emulators and PTYs,
// and triggers a re-render.
func (tm *TabManager) handleResize() {
	c, r, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return
	}

	tm.mu.Lock()
	tm.rows, tm.cols = uint16(r), uint16(c)
	tabs := make([]*Tab, len(tm.tabs))
	copy(tabs, tm.tabs)
	tm.needsRender = true
	tm.mu.Unlock()

	contentRows := int(tm.contentRows())
	sz := &pty.Winsize{Rows: uint16(contentRows), Cols: uint16(c)}
	for _, tab := range tabs {
		if tab.vt != nil {
			// Resize is self-locking
			tab.vt.Resize(c, contentRows)
		}
		if !tab.exited.Load() && tab.ptmx != nil {
			pty.Setsize(tab.ptmx, sz)
		}
	}
}

func (tm *TabManager) redrawActiveTab() {
	tm.mu.Lock()
	tm.needsRender = true
	tm.mu.Unlock()
}

func (tm *TabManager) redrawTab(tab *Tab) {
	tm.mu.Lock()
	tm.needsRender = true
	tm.mu.Unlock()
}

func (tm *TabManager) tabBarContentWidth(tabs []*Tab) int {
	used := tabBarBrandWidth()
	if len(tabs) == 0 {
		return used + len(" Home ")
	}
	for i, tab := range tabs {
		status := tabStatusEmoji(tab)
		if status != "" {
			used += 4 // emoji (2 cells) + space + space
		}
		used += len(fmt.Sprintf(" %d:%s ", i+1, tab.label))
	}
	return used
}

func tabBarTabIndexAtColumn(tabs []*Tab, col int) int {
	if len(tabs) == 0 || col <= tabBarBrandWidth() {
		return -1
	}

	pos := tabBarBrandWidth() + 1
	for i, tab := range tabs {
		width := len(fmt.Sprintf(" %d:%s ", i+1, tab.label))
		if status := tabStatusEmoji(tab); status != "" {
			width += 4
		}
		if col >= pos && col < pos+width {
			return i
		}
		pos += width
	}

	return -1
}

func tabBarBrandString(bg string) string {
	var b strings.Builder
	b.WriteString(bg)
	b.WriteString(" ")
	b.WriteString("\x1b[1;36mBifrost CLI ")
	return b.String()
}

func usesAppDrawnCursor(tab *Tab) bool {
	if tab == nil {
		return false
	}
	return strings.HasPrefix(tab.label, "Claude Code") || strings.HasPrefix(tab.label, "Gemini CLI")
}

func resolveRenderCursor(tab *Tab, vtCursorX, vtCursorY int, vtCursorVisible, rawCursorVisible bool) (int, int, bool) {
	if usesAppDrawnCursor(tab) {
		// Claude and Gemini render their own prompt cursor in-band (for
		// example via a reverse-video space) and can park the real terminal
		// cursor elsewhere after redraws. Rendering a separate host cursor on
		// top of that VT content causes the visible block to drift or pick up
		// the wrong terminal-default color.
		return vtCursorX, vtCursorY, false
	}

	showCursor := vtCursorVisible || rawCursorVisible
	curX, curY := vtCursorX, vtCursorY
	if !vtCursorVisible && rawCursorVisible && tab != nil && tab.cursorSavedValid.Load() {
		curX = int(tab.cursorSavedX.Load())
		curY = int(tab.cursorSavedY.Load())
	}
	return curX, curY, showCursor
}

func tabBarBrandWidth() int {
	return 1 + len("Bifrost CLI ")
}

func (tm *TabManager) initCursorTrace() {
	path := strings.TrimSpace(os.Getenv("BIFROST_CLAUDE_CURSOR_TRACE"))
	if path == "" {
		return
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		if tm.stderr != nil {
			fmt.Fprintf(tm.stderr, "bifrost: failed to open Claude cursor trace %q: %v\n", path, err)
		}
		return
	}
	tm.cursorTrace = f
	tm.traceSystemf("cursor_trace_enabled path=%q", path)
}

func (tm *TabManager) closeCursorTrace() {
	if tm == nil || tm.cursorTrace == nil {
		return
	}
	tm.cursorTraceMu.Lock()
	defer tm.cursorTraceMu.Unlock()
	_ = tm.cursorTrace.Close()
	tm.cursorTrace = nil
}

func (tm *TabManager) traceSystemf(format string, args ...any) {
	if tm == nil || tm.cursorTrace == nil {
		return
	}
	tm.cursorTraceMu.Lock()
	defer tm.cursorTraceMu.Unlock()
	fmt.Fprintf(tm.cursorTrace, "%s system "+format+"\n", append([]any{time.Now().Format(time.RFC3339Nano)}, args...)...)
}

func (tm *TabManager) traceClaudeCursorf(tab *Tab, format string, args ...any) {
	if tm == nil || tm.cursorTrace == nil || !strings.HasPrefix(tab.label, "Claude Code") {
		return
	}
	tm.cursorTraceMu.Lock()
	defer tm.cursorTraceMu.Unlock()
	prefixArgs := []any{time.Now().Format(time.RFC3339Nano), tab.label}
	fmt.Fprintf(tm.cursorTrace, "%s tab=%q "+format+"\n", append(prefixArgs, args...)...)
}

func (tm *TabManager) traceClaudeCursorBytes(tab *Tab, kind string, data []byte) {
	if tm == nil || tm.cursorTrace == nil || !strings.HasPrefix(tab.label, "Claude Code") {
		return
	}
	tm.traceClaudeCursorf(tab, "%s %s", kind, summarizeTraceBytes(data))
}

func summarizeTraceBytes(data []byte) string {
	if len(data) == 0 {
		return "len=0"
	}

	const maxDump = 96
	dump := data
	if len(dump) > maxDump {
		dump = dump[:maxDump]
	}

	suffix := ""
	if len(dump) < len(data) {
		suffix = "..."
	}

	return fmt.Sprintf("len=%d sample_hex=%x%s sample_q=%q%s", len(data), dump, suffix, dump, suffix)
}

func normalizedVersionLabel(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func (tm *TabManager) contentRows() uint16 {
	if tm.rows <= 1 {
		return 1
	}
	return tm.rows - 1 // bottom tab bar
}

func (tm *TabManager) writeBytes(data []byte) {
	tm.outputMu.Lock()
	defer tm.outputMu.Unlock()
	_, _ = tm.stdout.Write(data)
}

func (tm *TabManager) writeString(s string) {
	tm.outputMu.Lock()
	defer tm.outputMu.Unlock()
	_, _ = io.WriteString(tm.stdout, s)
}

func (tm *TabManager) resetHostInputModes() {
	seq := tm.syncHostInputModes(0) + hostKeyboardResetSequence() + hostCursorResetSequence()
	if seq == "" {
		return
	}
	tm.writeString(seq)
}

func (tm *TabManager) syncHostInputModes(mode vt10x.ModeFlag) string {
	desired := mode & hostTrackedVTModeMask

	tm.mu.Lock()
	current := tm.hostVTMode
	if current == desired {
		tm.mu.Unlock()
		return ""
	}
	tm.hostVTMode = desired
	tm.mu.Unlock()

	return hostInputModeSequence(desired)
}

func hostInputModeSequence(mode vt10x.ModeFlag) string {
	var b strings.Builder

	// Always clear tracked modes first so switching between mouse protocols
	// can't leave stale tracking enabled on the host terminal.
	b.WriteString("\x1b[?1006l\x1b[?1004l\x1b[?1003l\x1b[?1002l\x1b[?1000l\x1b[?9l")

	if mode&vt10x.ModeFocus != 0 {
		b.WriteString("\x1b[?1004h")
	}
	if mode&vt10x.ModeMouseSgr != 0 {
		b.WriteString("\x1b[?1006h")
	}

	switch {
	case mode&vt10x.ModeMouseMany != 0:
		b.WriteString("\x1b[?1003h")
	case mode&vt10x.ModeMouseMotion != 0:
		b.WriteString("\x1b[?1002h")
	case mode&vt10x.ModeMouseButton != 0:
		b.WriteString("\x1b[?1000h")
	case mode&vt10x.ModeMouseX10 != 0:
		b.WriteString("\x1b[?9h")
	}

	return b.String()
}

func hostKeyboardResetSequence() string {
	// Pop kitty keyboard protocol enhancements before handing control back to
	// Bubble Tea or the host shell. This fixes chooser key handling without
	// spraying broader terminal-keyboard mode resets into every terminal.
	return "\x1b[<u"
}

func hostCursorResetSequence() string {
	// Restore a visible default cursor in case the child CLI hid it or left a
	// custom DECSCUSR shape behind.
	return "\x1b[0 q\x1b[?25h"
}
