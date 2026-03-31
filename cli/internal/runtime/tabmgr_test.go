//go:build !windows

package runtime

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/vt10x"
)

func TestAddPendingTabDisablesCommandMode(t *testing.T) {
	t.Parallel()

	tm := &TabManager{
		tabs: []*Tab{{id: 1, label: "Codex"}},
		rows: 24,
		cols: 80,
	}
	tm.activeIdx = 0
	tm.nextID = 2
	tm.commandMode = true

	tab, prevActive := tm.addPendingTab()

	if prevActive != 0 {
		t.Fatalf("addPendingTab() prevActive = %d, want 0", prevActive)
	}
	if tab.label != pendingTabLabel {
		t.Fatalf("addPendingTab() label = %q, want %q", tab.label, pendingTabLabel)
	}
	if tm.activeIdx != 1 {
		t.Fatalf("activeIdx = %d, want 1", tm.activeIdx)
	}
	if tm.commandMode {
		t.Fatal("expected command mode to be disabled")
	}
	if !tm.paused {
		t.Fatal("expected tab manager to be paused while chooser is active")
	}
}

func TestRemovePendingTabRestoresPreviousActive(t *testing.T) {
	t.Parallel()

	original := &Tab{id: 1, label: "Codex"}
	tm := &TabManager{
		tabs: []*Tab{original},
		rows: 24,
		cols: 80,
	}
	tm.activeIdx = 0
	tm.nextID = 2

	pending, prevActive := tm.addPendingTab()
	active := tm.removePendingTab(pending, prevActive)

	if active != original {
		t.Fatal("expected original tab to be restored after removing pending tab")
	}
	if len(tm.tabs) != 1 {
		t.Fatalf("len(tabs) = %d, want 1", len(tm.tabs))
	}
	if tm.activeIdx != 0 {
		t.Fatalf("activeIdx = %d, want 0", tm.activeIdx)
	}
	if tm.paused {
		t.Fatal("expected paused to be cleared after removing pending tab")
	}
	if tm.commandMode {
		t.Fatal("expected command mode to stay disabled after removing pending tab")
	}
}

func TestShouldExitWithoutTabsRespectsCommandMode(t *testing.T) {
	t.Parallel()

	tm := &TabManager{}
	if !tm.shouldExitWithoutTabs() {
		t.Fatal("expected empty manager outside command mode to exit")
	}

	tm.commandMode = true
	if tm.shouldExitWithoutTabs() {
		t.Fatal("did not expect empty manager in command mode to exit")
	}

	tm.tabs = []*Tab{{id: 1, label: "Home"}}
	if tm.shouldExitWithoutTabs() {
		t.Fatal("did not expect manager with tabs to exit")
	}
}

func TestHandleCommandKeyKeepsHomeCommandModeWithoutTabs(t *testing.T) {
	t.Parallel()

	tm := &TabManager{
		stdout:      io.Discard,
		rows:        24,
		cols:        80,
		commandMode: true,
	}

	tm.handleCommandKey(nil, nil, nil, prefix)

	if !tm.commandMode {
		t.Fatal("expected command mode to stay active when there are no tabs to resume")
	}
}

func TestHandleCommandKeyEscapeReopensChooserWhenNoTabs(t *testing.T) {
	t.Parallel()

	var called bool
	var out bytes.Buffer
	tm := &TabManager{
		stdout:      &out,
		rows:        24,
		cols:        80,
		commandMode: true,
	}

	err := tm.handleCommandKey(context.Background(), func(ctx context.Context, notify func(TabNoticeLevel, string), tabBarLine func() string, stdinReader io.Reader, seed *LaunchSpec) (*LaunchSpec, error) {
		called = true
		return nil, nil
	}, nil, 0x1b)
	if err != nil {
		t.Fatalf("handleCommandKey() error = %v", err)
	}
	if !called {
		t.Fatal("expected escape with no tabs to reopen chooser")
	}
	if tm.commandMode {
		t.Fatal("expected command mode to exit when reopening chooser")
	}
}

func TestEnterCommandModeDrawsHomeTabBarWithoutTabs(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	tm := &TabManager{
		stdout: &out,
		rows:   24,
		cols:   80,
	}

	tm.enterCommandMode()

	if !tm.commandMode {
		t.Fatal("expected command mode to be enabled")
	}
	if got := out.String(); !strings.Contains(got, "\x1b[2J\x1b[H") {
		t.Fatalf("expected home command mode to clear stale chooser content, got %q", got)
	}
	if got := out.String(); got == "" || !bytes.Contains(out.Bytes(), []byte("n:new")) {
		t.Fatalf("expected home tab bar command hints to be rendered, got %q", got)
	}
}

func TestDrawTabBarResetsOriginAndScrollRegion(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	tm := &TabManager{
		stdout: &out,
		rows:   24,
		cols:   80,
	}

	tm.drawTabBar()

	got := out.String()
	if !strings.Contains(got, "\x1b[r") {
		t.Fatalf("expected drawTabBar() to reset scroll region, got %q", got)
	}
	if !strings.Contains(got, "\x1b[?6l") {
		t.Fatalf("expected drawTabBar() to reset origin mode, got %q", got)
	}
}

func TestEditSessionKey(t *testing.T) {
	t.Parallel()

	if !isEditSessionKey('e') {
		t.Fatal("expected lowercase e to edit the current session")
	}
	if !isEditSessionKey('E') {
		t.Fatal("expected uppercase E to edit the current session")
	}
	if isEditSessionKey('n') {
		t.Fatal("did not expect n to be treated as edit session")
	}
}

func TestBuildTabBarStringUsesCLIBrandAndRightSideVersion(t *testing.T) {
	t.Parallel()

	tm := &TabManager{
		version: "0.1.1-dev",
		rows:    24,
		cols:    120,
	}

	got := tm.buildTabBarString()

	if !strings.Contains(got, "Bifrost CLI") {
		t.Fatalf("expected tab bar to contain Bifrost CLI branding, got %q", got)
	}
	if strings.Contains(got, " Bifrost ") {
		t.Fatalf("did not expect legacy Bifrost branding, got %q", got)
	}
	if strings.Contains(got, "▣") {
		t.Fatalf("did not expect tab bar logo glyph, got %q", got)
	}
	if !strings.Contains(got, " v0.1.1-dev ") {
		t.Fatalf("expected tab bar to contain right-side version label, got %q", got)
	}
	if strings.Contains(got, " vv0.1.1-dev ") {
		t.Fatalf("did not expect duplicated version prefix, got %q", got)
	}
}

func TestBuildTabBarStringShowsErrorNoticeInRed(t *testing.T) {
	t.Parallel()

	tm := &TabManager{
		rows:        24,
		cols:        120,
		commandMode: true,
		noticeText:  "new tab failed",
		noticeLevel: TabNoticeError,
	}

	got := tm.buildTabBarString()

	if !strings.Contains(got, "\x1b[48;5;88m") {
		t.Fatalf("expected red error background, got %q", got)
	}
	if !strings.Contains(got, "error: new tab failed") {
		t.Fatalf("expected error message in tab bar, got %q", got)
	}
	if !strings.Contains(got, "Esc: clear") {
		t.Fatalf("expected escape-to-clear hint, got %q", got)
	}
	if strings.Contains(got, "space: resume") {
		t.Fatalf("did not expect space-to-resume hint during sticky error, got %q", got)
	}
}

func TestBuildTabBarStringShowsEditSessionHintInCommandMode(t *testing.T) {
	t.Parallel()

	tm := &TabManager{
		rows:        24,
		cols:        120,
		commandMode: true,
		tabs: []*Tab{
			{id: 1, label: "Codex"},
		},
	}

	got := tm.buildTabBarString()

	if !strings.Contains(got, "e:edit session") {
		t.Fatalf("expected command mode hint to include edit session, got %q", got)
	}
}

func TestHostInputModeSequenceEnablesMouseAndFocus(t *testing.T) {
	t.Parallel()

	got := hostInputModeSequence(vt10x.ModeMouseMotion | vt10x.ModeMouseSgr | vt10x.ModeFocus)

	if !strings.Contains(got, "\x1b[?1002h") {
		t.Fatalf("expected motion mouse tracking enable, got %q", got)
	}
	if !strings.Contains(got, "\x1b[?1006h") {
		t.Fatalf("expected sgr mouse tracking enable, got %q", got)
	}
	if !strings.Contains(got, "\x1b[?1004h") {
		t.Fatalf("expected focus tracking enable, got %q", got)
	}
}

func TestHostKeyboardResetSequenceDisablesEnhancedKeyboardModes(t *testing.T) {
	t.Parallel()

	got := hostKeyboardResetSequence()

	if got != "\x1b[<u" {
		t.Fatalf("hostKeyboardResetSequence() = %q, want %q", got, "\x1b[<u")
	}
	for _, unwanted := range []string{
		"\x1b[>0n",
		"\x1b[>1n",
		"\x1b[>2n",
		"\x1b[>3n",
		"\x1b[>4n",
		"\x1b[>6n",
		"\x1b[>7n",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("did not expect keyboard reset sequence %q in %q", unwanted, got)
		}
	}
}

func TestHostCursorResetSequenceShowsVisibleDefaultCursor(t *testing.T) {
	t.Parallel()

	got := hostCursorResetSequence()

	if got != "\x1b[0 q\x1b[?25h" {
		t.Fatalf("hostCursorResetSequence() = %q, want %q", got, "\x1b[0 q\x1b[?25h")
	}
}

func TestSyncHostInputModesReturnsSequenceOnlyOnChange(t *testing.T) {
	t.Parallel()

	tm := &TabManager{}
	mode := vt10x.ModeMouseButton | vt10x.ModeMouseSgr

	first := tm.syncHostInputModes(mode)
	if first == "" {
		t.Fatal("expected first host input mode sync to emit escape sequence")
	}
	second := tm.syncHostInputModes(mode)
	if second != "" {
		t.Fatalf("expected unchanged host input mode sync to emit nothing, got %q", second)
	}
	reset := tm.syncHostInputModes(0)
	if !strings.Contains(reset, "\x1b[?1000l") {
		t.Fatalf("expected reset sequence to disable mouse tracking, got %q", reset)
	}
}

func TestNormalizeTerminalSizeClampsTinyResize(t *testing.T) {
	t.Parallel()

	cols, rows := normalizeTerminalSize(0, 1)
	if cols != 20 || rows != 2 {
		t.Fatalf("normalizeTerminalSize(0, 1) = (%d, %d), want (20, 2)", cols, rows)
	}
}

func TestResetHostInputModesRestoresCursorVisibility(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	tm := &TabManager{stdout: &out}

	tm.resetHostInputModes()

	if got := out.String(); !strings.Contains(got, "\x1b[?25h") {
		t.Fatalf("expected resetHostInputModes() to restore visible cursor, got %q", got)
	}
}

func TestHandleCommandKeySpaceClearsNoticeAndResumes(t *testing.T) {
	t.Parallel()

	tm := &TabManager{
		stdout:      io.Discard,
		rows:        24,
		cols:        80,
		commandMode: true,
		noticeText:  "oops",
		noticeLevel: TabNoticeError,
		tabs:        []*Tab{{id: 1, label: "Codex"}},
	}

	tm.handleCommandKey(nil, nil, nil, ' ')

	if tm.commandMode {
		t.Fatal("expected command mode to exit after space resume")
	}
	if tm.noticeText != "" {
		t.Fatalf("expected notice to clear after space resume, got %q", tm.noticeText)
	}
}

func TestHandleCommandKeyEscapeClearsStickyErrorAndStaysInCommandMode(t *testing.T) {
	t.Parallel()

	tm := &TabManager{
		stdout:       io.Discard,
		rows:         24,
		cols:         80,
		commandMode:  true,
		noticeText:   "oops",
		noticeLevel:  TabNoticeError,
		noticeSticky: true,
		tabs:         []*Tab{{id: 1, label: "Codex"}},
	}

	tm.handleCommandKey(nil, nil, nil, 0x1b)

	if !tm.commandMode {
		t.Fatal("expected command mode to stay enabled after clearing sticky error")
	}
	if tm.noticeText != "" {
		t.Fatalf("expected sticky error notice to clear on escape, got %q", tm.noticeText)
	}
}

func TestHandleCommandKeyEnterDoesNotResumeWhileStickyErrorIsShown(t *testing.T) {
	t.Parallel()

	tm := &TabManager{
		stdout:       io.Discard,
		rows:         24,
		cols:         80,
		commandMode:  true,
		noticeText:   "oops",
		noticeLevel:  TabNoticeError,
		noticeSticky: true,
		tabs:         []*Tab{{id: 1, label: "Codex"}},
	}

	tm.handleCommandKey(nil, nil, nil, '\r')

	if !tm.commandMode {
		t.Fatal("expected sticky error to keep tab manager in command mode")
	}
	if tm.noticeText != "oops" {
		t.Fatalf("expected enter to leave sticky error untouched, got %q", tm.noticeText)
	}
}

func TestNoteCtrlCDoublePressRequestsClose(t *testing.T) {
	t.Parallel()

	tm := &TabManager{}
	now := time.Now()

	if tm.noteCtrlC(now) {
		t.Fatal("did not expect first ctrl+c to force close")
	}
	if !tm.noteCtrlC(now.Add(time.Second)) {
		t.Fatal("expected second ctrl+c within window to force close")
	}
}

func TestNoteCtrlCOutsideWindowDoesNotRequestClose(t *testing.T) {
	t.Parallel()

	tm := &TabManager{}
	now := time.Now()

	if tm.noteCtrlC(now) {
		t.Fatal("did not expect first ctrl+c to force close")
	}
	if tm.noteCtrlC(now.Add(tabCtrlCExitWindow + time.Millisecond)) {
		t.Fatal("did not expect ctrl+c outside window to force close")
	}
}

func TestHandleActiveCtrlCResetsOnOtherInput(t *testing.T) {
	t.Parallel()

	tm := &TabManager{}

	if tm.handleActiveCtrlC([]byte{0x03}) {
		t.Fatal("did not expect first ctrl+c to close tab")
	}
	if tm.handleActiveCtrlC([]byte("a")) {
		t.Fatal("did not expect regular input to close tab")
	}
	if tm.handleActiveCtrlC([]byte{0x03}) {
		t.Fatal("did not expect ctrl+c after other input to close tab")
	}
}

func TestModifierHasCtrlSupportsColonSuffix(t *testing.T) {
	t.Parallel()

	if !modifierHasCtrl("5:3") {
		t.Fatal("expected ctrl modifier to be detected from colon-suffixed value")
	}

	if modifierHasCtrl("1:3") {
		t.Fatal("did not expect ctrl modifier in plain colon-suffixed value")
	}
}

func TestDecodeCommandByteCSIU(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		seq  string
		want byte
	}{
		{
			name: "printable key",
			seq:  "\x1b[110;1:1u",
			want: 'n',
		},
		{
			name: "escape key",
			seq:  "\x1b[27;1:1u",
			want: 0x1b,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := decodeCommandByte([]byte(tc.seq))
			if !ok {
				t.Fatalf("expected sequence %q to decode", tc.seq)
			}
			if got != tc.want {
				t.Fatalf("decodeCommandByte(%q) = %q, want %q", tc.seq, got, tc.want)
			}
		})
	}
}

func TestIsCtrlBSequenceSupportsColonSuffix(t *testing.T) {
	t.Parallel()

	// Press event with colon-suffixed modifier (event_type 1) should match.
	if !isCtrlBSequence([]byte("\x1b[98;5:1u")) {
		t.Fatal("expected ctrl+b press event with colon-suffixed modifiers to be recognized")
	}

	// Release event (event_type 3) should NOT match — release events must be
	// silently dropped, otherwise command mode enters and exits instantly.
	if isCtrlBSequence([]byte("\x1b[98;5:3u")) {
		t.Fatal("expected ctrl+b release event to be rejected")
	}

	// Repeat event (event_type 2) should match.
	if !isCtrlBSequence([]byte("\x1b[98;5:2u")) {
		t.Fatal("expected ctrl+b repeat event to be recognized")
	}
}

func TestPrefersFullscreenChooserAppleTerminal(t *testing.T) {
	old := os.Getenv("TERM_PROGRAM")
	t.Cleanup(func() {
		if old == "" {
			os.Unsetenv("TERM_PROGRAM")
			return
		}
		os.Setenv("TERM_PROGRAM", old)
	})

	os.Setenv("TERM_PROGRAM", "Apple_Terminal")
	if !prefersFullscreenChooser() {
		t.Fatal("expected Apple Terminal to use fullscreen chooser fallback")
	}

	os.Setenv("TERM_PROGRAM", "iTerm.app")
	if prefersFullscreenChooser() {
		t.Fatal("did not expect iTerm to use fullscreen chooser fallback")
	}
}

func TestNextInputTokenParsesDCSReply(t *testing.T) {
	t.Parallel()

	seq := []byte("\x1bP>|iTerm2 3.6.664\x1b\\")

	token, consumed, isPrefix, complete := nextInputToken(seq)

	if !complete {
		t.Fatal("expected DCS reply to parse as a complete token")
	}
	if isPrefix {
		t.Fatal("did not expect DCS reply to be treated as the tab-mode prefix")
	}
	if consumed != len(seq) {
		t.Fatalf("consumed = %d, want %d", consumed, len(seq))
	}
	if string(token) != string(seq) {
		t.Fatalf("token = %q, want %q", token, seq)
	}
}

func TestNextInputTokenMarksPartialDCSIncomplete(t *testing.T) {
	t.Parallel()

	token, consumed, isPrefix, complete := nextInputToken([]byte("\x1bP>|iTerm2"))

	if complete {
		t.Fatal("expected partial DCS reply to remain incomplete")
	}
	if token != nil {
		t.Fatalf("token = %q, want nil", token)
	}
	if consumed != 0 {
		t.Fatalf("consumed = %d, want 0", consumed)
	}
	if isPrefix {
		t.Fatal("did not expect partial DCS reply to be treated as a prefix")
	}
}

func TestNextInputTokenParsesX10MouseReport(t *testing.T) {
	t.Parallel()

	token, consumed, isPrefix, complete := nextInputToken([]byte("\x1b[M !!"))

	if !complete {
		t.Fatal("expected X10 mouse report to parse as a complete token")
	}
	if isPrefix {
		t.Fatal("did not expect X10 mouse report to be treated as a prefix")
	}
	if consumed != 6 {
		t.Fatalf("consumed = %d, want 6", consumed)
	}
	if string(token) != "\x1b[M !!" {
		t.Fatalf("token = %q, want %q", token, "\x1b[M !!")
	}
}

func TestParseMouseEvent(t *testing.T) {
	t.Parallel()

	t.Run("parses sgr left click", func(t *testing.T) {
		t.Parallel()

		ev, ok := parseMouseEvent([]byte("\x1b[<0;18;24M"))
		if !ok {
			t.Fatal("expected SGR mouse event to parse")
		}
		if ev.x != 18 || ev.y != 24 || ev.button != 0 || !ev.press || ev.motion || ev.wheel {
			t.Fatalf("unexpected SGR mouse event: %+v", ev)
		}
	})

	t.Run("parses x10 left click", func(t *testing.T) {
		t.Parallel()

		ev, ok := parseMouseEvent([]byte("\x1b[M +8"))
		if !ok {
			t.Fatal("expected X10 mouse event to parse")
		}
		if ev.x != 11 || ev.y != 24 || ev.button != 0 || !ev.press || ev.motion || ev.wheel {
			t.Fatalf("unexpected X10 mouse event: %+v", ev)
		}
	})

	t.Run("rejects release events as clicks", func(t *testing.T) {
		t.Parallel()

		ev, ok := parseMouseEvent([]byte("\x1b[<0;18;24m"))
		if !ok {
			t.Fatal("expected SGR mouse release to parse")
		}
		if ev.press {
			t.Fatalf("expected release event, got %+v", ev)
		}
	})
}

func TestHandleTabBarMouseEventSwitchesTabs(t *testing.T) {
	t.Parallel()

	first := &Tab{label: "Codex", startedAt: time.Now().Add(-tabStartingWindow - time.Second), vt: vt10x.New(vt10x.WithSize(80, 24))}
	second := &Tab{label: "Gemini", startedAt: time.Now().Add(-tabStartingWindow - time.Second), vt: vt10x.New(vt10x.WithSize(80, 24))}
	tm := &TabManager{
		rows:      24,
		cols:      100,
		tabs:      []*Tab{first, second},
		activeIdx: 0,
	}

	col := tabBarBrandWidth() + len(" ✅ 1:Codex ") + 1
	if !tm.handleTabBarMouseEvent(mouseEvent{x: col, y: 24, button: 0, press: true}) {
		t.Fatal("expected tab-bar click to be handled")
	}
	if tm.activeIdx != 1 {
		t.Fatalf("activeIdx = %d, want 1", tm.activeIdx)
	}
}

func TestHandleTabBarMouseEventResumesCommandMode(t *testing.T) {
	t.Parallel()

	first := &Tab{label: "Codex", startedAt: time.Now().Add(-tabStartingWindow - time.Second), vt: vt10x.New(vt10x.WithSize(80, 24))}
	second := &Tab{label: "Gemini", startedAt: time.Now().Add(-tabStartingWindow - time.Second), vt: vt10x.New(vt10x.WithSize(80, 24))}
	tm := &TabManager{
		rows:        24,
		cols:        100,
		tabs:        []*Tab{first, second},
		activeIdx:   0,
		commandMode: true,
	}

	col := tabBarBrandWidth() + len(" ✅ 1:Codex ") + 1
	if !tm.handleTabBarMouseEvent(mouseEvent{x: col, y: 24, button: 0, press: true}) {
		t.Fatal("expected tab-bar click to be handled")
	}
	if tm.commandMode {
		t.Fatal("expected command mode to exit after tab click")
	}
	if tm.activeIdx != 1 {
		t.Fatalf("activeIdx = %d, want 1", tm.activeIdx)
	}
}

func TestResolveRenderCursorAppDrawnCursorTabsHideHostCursorEvenWithSavedPosition(t *testing.T) {
	t.Parallel()

	for _, label := range []string{"Claude Code", "Gemini CLI"} {
		tab := &Tab{label: label}
		tab.cursorSavedX.Store(17)
		tab.cursorSavedY.Store(6)
		tab.cursorSavedValid.Store(true)

		gotX, gotY, gotVisible := resolveRenderCursor(tab, 2, 19, false, false)
		if gotX != 2 || gotY != 19 || gotVisible {
			t.Fatalf("%s resolveRenderCursor() = (%d, %d, %t), want (2, 19, false)", label, gotX, gotY, gotVisible)
		}
	}
}

func TestResolveRenderCursorAppDrawnCursorTabsHideHostCursorWithoutSavedPosition(t *testing.T) {
	t.Parallel()

	for _, label := range []string{"Claude Code", "Gemini CLI"} {
		tab := &Tab{label: label}

		gotX, gotY, gotVisible := resolveRenderCursor(tab, 9, 12, false, false)
		if gotX != 9 || gotY != 12 || gotVisible {
			t.Fatalf("%s resolveRenderCursor() = (%d, %d, %t), want (9, 12, false)", label, gotX, gotY, gotVisible)
		}
	}
}

func TestResolveRenderCursorNonClaudeKeepsExistingFallback(t *testing.T) {
	t.Parallel()

	tab := &Tab{label: "Codex"}
	tab.cursorSavedX.Store(1)
	tab.cursorSavedY.Store(0)
	tab.cursorSavedValid.Store(true)

	gotX, gotY, gotVisible := resolveRenderCursor(tab, 15, 8, false, true)
	if gotX != 1 || gotY != 0 || !gotVisible {
		t.Fatalf("resolveRenderCursor() = (%d, %d, %t), want (1, 0, true)", gotX, gotY, gotVisible)
	}
}

func TestRenderVTScreenKeepsReverseDefaultSpaceVisible(t *testing.T) {
	t.Parallel()

	term := vt10x.New(vt10x.WithSize(3, 1))
	if _, err := term.Write([]byte("\x1b[7m \x1b[27m")); err != nil {
		t.Fatalf("write reverse space: %v", err)
	}

	term.Lock()
	got := renderVTScreen(term, 3, 1)
	term.Unlock()

	if !strings.Contains(got, "\x1b[0;7m ") {
		t.Fatalf("renderVTScreen() = %q, want reverse-video space to remain visible", got)
	}
}

func TestIsTerminalResponseRecognizesDCSAndCSIReplies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		seq  []byte
	}{
		{
			name: "dcs xtversion",
			seq:  []byte("\x1bP>|iTerm2 3.6.664\x1b\\"),
		},
		{
			name: "csi device attrs",
			seq:  []byte("\x1b[?1;2;4;6;17;18;21;22;52c"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if !isTerminalResponse(tc.seq) {
				t.Fatalf("expected %q to be recognized as a terminal response", tc.seq)
			}
		})
	}
}

func TestSanitizeSGR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no colons passthrough",
			input: "\x1b[38;2;100;150;200m",
			want:  "\x1b[38;2;100;150;200m",
		},
		{
			name:  "curly underline rewritten to basic",
			input: "\x1b[4:3m",
			want:  "\x1b[4m",
		},
		{
			name:  "colon fg true-color with colorspace",
			input: "\x1b[38:2:0:100:150:200m",
			want:  "\x1b[38;2;100;150;200m",
		},
		{
			name:  "colon fg true-color without colorspace",
			input: "\x1b[38:2:100:150:200m",
			want:  "\x1b[38;2;100;150;200m",
		},
		{
			name:  "colon bg 256-color",
			input: "\x1b[48:5:208m",
			want:  "\x1b[48;5;208m",
		},
		{
			name:  "underline color dropped",
			input: "\x1b[58:2:0:255:0:0m",
			want:  "\x1b[m",
		},
		{
			name:  "mixed colon and semicolon params",
			input: "\x1b[4:3;38;2;100;150;200m",
			want:  "\x1b[4;38;2;100;150;200m",
		},
		{
			name:  "plain text unmodified",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "non-SGR CSI with colon unmodified",
			input: "\x1b[?2026h",
			want:  "\x1b[?2026h",
		},
		{
			name:  "colon fg true-color with empty colorspace",
			input: "\x1b[38:2::100:150:200m",
			want:  "\x1b[38;2;100;150;200m",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := string(sanitizeSGR([]byte(tc.input)))
			if got != tc.want {
				t.Fatalf("sanitizeSGR(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestContainsStandaloneBEL(t *testing.T) {
	t.Parallel()

	t.Run("standalone bell detected", func(t *testing.T) {
		t.Parallel()

		var state belParserState
		if !containsStandaloneBEL([]byte("hello\x07world"), &state) {
			t.Fatal("expected standalone BEL to be detected")
		}
	})

	t.Run("osc terminator bell ignored", func(t *testing.T) {
		t.Parallel()

		var state belParserState
		if containsStandaloneBEL([]byte("\x1b]0;window title\x07"), &state) {
			t.Fatal("did not expect OSC terminator BEL to be treated as a notification")
		}
	})

	t.Run("split osc terminator bell ignored", func(t *testing.T) {
		t.Parallel()

		var state belParserState
		if containsStandaloneBEL([]byte("\x1b]0;window"), &state) {
			t.Fatal("did not expect partial OSC chunk to contain a notification")
		}
		if containsStandaloneBEL([]byte(" title\x07"), &state) {
			t.Fatal("did not expect BEL ending a split OSC sequence to be treated as a notification")
		}
	})

	t.Run("osc st then bell still detects real alert", func(t *testing.T) {
		t.Parallel()

		var state belParserState
		if !containsStandaloneBEL([]byte("\x1b]0;title\x1b\\\x07"), &state) {
			t.Fatal("expected standalone BEL after OSC ST terminator to be detected")
		}
	})
}

func TestTabStatusEmoji(t *testing.T) {
	t.Parallel()

	newTab := func() *Tab {
		return &Tab{
			label:     "Codex",
			startedAt: time.Now().Add(-tabStartingWindow - time.Second),
			vt:        vt10x.New(vt10x.WithSize(80, 24)),
		}
	}

	t.Run("new tabs show startup hourglass", func(t *testing.T) {
		t.Parallel()

		tab := &Tab{
			label:     "Codex",
			startedAt: time.Now(),
			bell:      true,
			vt:        vt10x.New(vt10x.WithSize(80, 24)),
		}
		if got := tabStatusEmoji(tab); got != "⏳" {
			t.Fatalf("tabStatusEmoji() = %q, want %q", got, "⏳")
		}
	})

	t.Run("notification wins", func(t *testing.T) {
		t.Parallel()

		tab := newTab()
		tab.bell = true
		if got := tabStatusEmoji(tab); got != "🔔" {
			t.Fatalf("tabStatusEmoji() = %q, want %q", got, "🔔")
		}
	})

	t.Run("consistent screen changes show progress", func(t *testing.T) {
		t.Parallel()

		tab := newTab()
		now := time.Now()
		tab.prevScreenChange = now.Add(-time.Second)
		tab.lastScreenChange = now.Add(-500 * time.Millisecond)
		if got := tabStatusEmoji(tab); got != "🧠" {
			t.Fatalf("tabStatusEmoji() = %q, want %q", got, "🧠")
		}
	})

	t.Run("recent typing suppresses progress emoji", func(t *testing.T) {
		t.Parallel()

		tab := newTab()
		now := time.Now()
		tab.prevScreenChange = now.Add(-time.Second)
		tab.lastScreenChange = now.Add(-500 * time.Millisecond)
		noteTabUserInput(tab, now.Add(-250*time.Millisecond))
		if got := tabStatusEmoji(tab); got != "✅" {
			t.Fatalf("tabStatusEmoji() = %q, want %q", got, "✅")
		}
	})

	t.Run("single screen change stays waiting", func(t *testing.T) {
		t.Parallel()

		tab := newTab()
		tab.lastScreenChange = time.Now()
		if got := tabStatusEmoji(tab); got != "✅" {
			t.Fatalf("tabStatusEmoji() = %q, want %q", got, "✅")
		}
	})

	t.Run("stale screen changes stay waiting", func(t *testing.T) {
		t.Parallel()

		tab := newTab()
		now := time.Now()
		tab.prevScreenChange = now.Add(-4 * time.Second)
		tab.lastScreenChange = now.Add(-2 * time.Second)
		if got := tabStatusEmoji(tab); got != "✅" {
			t.Fatalf("tabStatusEmoji() = %q, want %q", got, "✅")
		}
	})

	t.Run("exited tab shows no status", func(t *testing.T) {
		t.Parallel()

		tab := newTab()
		tab.exited.Store(true)
		if got := tabStatusEmoji(tab); got != "" {
			t.Fatalf("tabStatusEmoji() = %q, want empty", got)
		}
	})
}

func TestNoteTabScreenChange(t *testing.T) {
	t.Parallel()

	tab := &Tab{
		vt: vt10x.New(vt10x.WithSize(20, 4)),
	}
	now := time.Now()

	if !noteTabScreenChange(tab, now) {
		t.Fatal("expected initial screen snapshot to count as a change")
	}
	if noteTabScreenChange(tab, now.Add(100*time.Millisecond)) {
		t.Fatal("did not expect identical screen snapshot to count as a change")
	}

	_, _ = tab.vt.Write([]byte("hello"))
	if !noteTabScreenChange(tab, now.Add(200*time.Millisecond)) {
		t.Fatal("expected changed VT screen to count as a change")
	}
}
