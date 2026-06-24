//go:build !windows

package runtime

import (
	"strings"
	"testing"

	"github.com/maximhq/vt10x"
)

// glyphRow is a tiny helper to build a row of glyphs with default style
// from a string. Used by tests below.
func glyphRow(s string, cols int) []vt10x.Glyph {
	row := make([]vt10x.Glyph, cols)
	for i, r := range s {
		if i >= cols {
			break
		}
		row[i] = vt10x.Glyph{Char: r, FG: vt10x.DefaultFG, BG: vt10x.DefaultBG}
	}
	for i := len([]rune(s)); i < cols; i++ {
		row[i] = vt10x.Glyph{Char: ' ', FG: vt10x.DefaultFG, BG: vt10x.DefaultBG}
	}
	return row
}

func TestScrollbackPushDedupsConsecutiveBlankRows(t *testing.T) {
	sb := newScrollback(10)
	blank := glyphRow("", 5)
	sb.push([][]vt10x.Glyph{blank}, false)
	sb.push([][]vt10x.Glyph{blank}, false)
	sb.push([][]vt10x.Glyph{blank}, false)

	if got := sb.length(); got != 1 {
		t.Fatalf("expected blank-row dedup to collapse to 1, got %d", got)
	}
}

func TestScrollbackPushKeepsConsecutiveNonBlankRepeats(t *testing.T) {
	sb := newScrollback(10)
	row := glyphRow("hello", 5)
	sb.push([][]vt10x.Glyph{row}, false)
	sb.push([][]vt10x.Glyph{row}, false)
	sb.push([][]vt10x.Glyph{row}, false)

	if got := sb.length(); got != 3 {
		t.Fatalf("expected non-blank repeats to be preserved, got %d", got)
	}
}

func TestHandleWheelEventEntersScrollModeOnWheelUp(t *testing.T) {
	tab := &Tab{sb: newScrollback(20)}
	// Seed scrollback so the offset has somewhere to go.
	for i := 0; i < 10; i++ {
		tab.sb.push([][]vt10x.Glyph{glyphRow(string(rune('a'+i)), 1)}, false)
	}
	tm := &TabManager{
		tabs:      []*Tab{tab},
		activeIdx: 0,
		rows:      24,
		cols:      80,
	}

	consumed := tm.handleWheelEvent(mouseEvent{wheel: true, button: 0, press: true})
	if !consumed {
		t.Fatalf("wheel-up should be consumed by host scroll, got !consumed")
	}
	if !tm.scrollMode {
		t.Fatalf("wheel-up should enter scroll mode")
	}
	if tm.scrollOffset != wheelLinesPerNotch {
		t.Fatalf("wheel-up offset = %d, want %d", tm.scrollOffset, wheelLinesPerNotch)
	}
}

func TestHandleWheelEventExitsScrollModeAtBottom(t *testing.T) {
	tab := &Tab{sb: newScrollback(20)}
	for i := 0; i < 5; i++ {
		tab.sb.push([][]vt10x.Glyph{glyphRow(string(rune('a'+i)), 1)}, false)
	}
	tm := &TabManager{
		tabs:         []*Tab{tab},
		activeIdx:    0,
		rows:         24,
		cols:         80,
		scrollMode:   true,
		scrollOffset: 5, // > wheelLinesPerNotch so two notches are needed to exit
	}

	tm.handleWheelEvent(mouseEvent{wheel: true, button: 1, press: true})
	if !tm.scrollMode {
		t.Fatalf("after first wheel-down offset should still be > 0, got %d", tm.scrollOffset)
	}
	if tm.scrollOffset != 5-wheelLinesPerNotch {
		t.Fatalf("after first wheel-down offset = %d, want %d", tm.scrollOffset, 5-wheelLinesPerNotch)
	}
	tm.handleWheelEvent(mouseEvent{wheel: true, button: 1, press: true})
	if tm.scrollMode {
		t.Fatalf("after reaching bottom, scroll mode should exit; offset=%d", tm.scrollOffset)
	}
}

func TestHandleWheelEventIgnoresWheelDownWhenNotScrolling(t *testing.T) {
	tab := &Tab{sb: newScrollback(20)}
	tm := &TabManager{tabs: []*Tab{tab}, activeIdx: 0, rows: 24, cols: 80}

	consumed := tm.handleWheelEvent(mouseEvent{wheel: true, button: 1, press: true})
	if !consumed {
		t.Fatalf("wheel-down outside scroll mode should be consumed (not forwarded)")
	}
	if tm.scrollMode {
		t.Fatalf("wheel-down should never enter scroll mode")
	}
	if tm.scrollOffset != 0 {
		t.Fatalf("wheel-down outside scroll mode should not move offset, got %d", tm.scrollOffset)
	}
}

func TestHandleWheelEventIgnoresReleaseEvents(t *testing.T) {
	tab := &Tab{sb: newScrollback(20)}
	tm := &TabManager{tabs: []*Tab{tab}, activeIdx: 0, rows: 24, cols: 80}

	// SGR mouse releases come with press=false. We still mark them as
	// consumed (so the harness doesn't see them when host owns mouse),
	// but they must not move the scroll offset.
	tm.handleWheelEvent(mouseEvent{wheel: true, button: 0, press: false})
	if tm.scrollMode || tm.scrollOffset != 0 {
		t.Fatalf("wheel release moved state: scrollMode=%v offset=%d", tm.scrollMode, tm.scrollOffset)
	}
}

func TestOnScrollbackGrowBumpsOffsetForActiveScrollingTab(t *testing.T) {
	tab := &Tab{sb: newScrollback(20)}
	other := &Tab{sb: newScrollback(20)}
	// Pre-fill both scrollbacks so the active-tab bump isn't clipped by
	// the sbLen clamp added in onScrollbackGrow.
	for i := 0; i < 10; i++ {
		tab.sb.push([][]vt10x.Glyph{glyphRow(string(rune('a'+i)), 1)}, false)
		other.sb.push([][]vt10x.Glyph{glyphRow(string(rune('a'+i)), 1)}, false)
	}
	tm := &TabManager{
		tabs:         []*Tab{tab, other},
		activeIdx:    0,
		scrollMode:   true,
		scrollOffset: 5,
		rows:         24,
		cols:         80,
	}

	tm.onScrollbackGrow(tab, 3)
	if tm.scrollOffset != 8 {
		t.Fatalf("active scroll-mode tab: scrollOffset=%d, want 8", tm.scrollOffset)
	}

	// Eviction on a non-active tab must not move the offset.
	tm.onScrollbackGrow(other, 10)
	if tm.scrollOffset != 8 {
		t.Fatalf("non-active tab evicted: scrollOffset=%d, want 8 unchanged", tm.scrollOffset)
	}

	// Not in scroll mode → no bump.
	tm.scrollMode = false
	tm.onScrollbackGrow(tab, 4)
	if tm.scrollOffset != 8 {
		t.Fatalf("scroll mode off: scrollOffset=%d, want 8 unchanged", tm.scrollOffset)
	}
}

func TestOnScrollbackGrowClampsOffsetToScrollbackLength(t *testing.T) {
	tab := &Tab{sb: newScrollback(20)}
	// 3 rows in scrollback; an inflated scrollOffset+added must be
	// clamped back to len so adjustScrollOffset doesn't have to unwind
	// phantom scroll distance before exiting scroll mode.
	for i := 0; i < 3; i++ {
		tab.sb.push([][]vt10x.Glyph{glyphRow(string(rune('a'+i)), 1)}, false)
	}
	tm := &TabManager{
		tabs:         []*Tab{tab},
		activeIdx:    0,
		scrollMode:   true,
		scrollOffset: 2,
		rows:         24,
		cols:         80,
	}

	tm.onScrollbackGrow(tab, 5)
	if tm.scrollOffset != 3 {
		t.Fatalf("scrollOffset should be clamped to sbLen=3, got %d", tm.scrollOffset)
	}
}

func TestScrollbackPushKeepsDistinctRowsAndEvictsAtCap(t *testing.T) {
	sb := newScrollback(3)
	for i := 0; i < 5; i++ {
		row := glyphRow(string(rune('a'+i)), 1)
		sb.push([][]vt10x.Glyph{row}, false)
	}

	if got := sb.length(); got != 3 {
		t.Fatalf("expected cap=3 to hold 3 rows, got %d", got)
	}

	tail := sb.snapshotTail(3)
	want := []rune{'c', 'd', 'e'}
	for i, r := range want {
		if tail[i][0].Char != r {
			t.Fatalf("tail[%d] = %q, want %q", i, tail[i][0].Char, r)
		}
	}
}

func TestSnapshotTailReturnsAllRowsWhenNExceedsLength(t *testing.T) {
	sb := newScrollback(10)
	for i := 0; i < 3; i++ {
		sb.push([][]vt10x.Glyph{glyphRow(string(rune('x'+i)), 1)}, false)
	}
	tail := sb.snapshotTail(100)
	if len(tail) != 3 {
		t.Fatalf("expected all 3 rows back, got %d", len(tail))
	}
}

func TestWriteRowGlyphsTruncatesWithEllipsisOnShrink(t *testing.T) {
	row := glyphRow("the quick brown fox", 19) // 19 visible cells
	var b strings.Builder
	writeRowGlyphs(&b, row, 10) // shrink to 10 cols
	got := b.String()

	if !strings.Contains(got, "…") {
		t.Fatalf("expected ellipsis indicator when row > cols, got %q", got)
	}
	// First 9 chars of original content should still be present.
	if !strings.Contains(got, "the quick") {
		t.Fatalf("expected truncated row to start with %q, got %q", "the quick", got)
	}
	// "brown fox" should be clipped away.
	if strings.Contains(got, "brown") {
		t.Fatalf("expected clipped content to be removed, got %q", got)
	}
}

func TestWriteRowGlyphsTrimsPaddingOnGrow(t *testing.T) {
	// Build a 10-col row whose visible content is just "hi"; the other 8
	// cells are default-style spaces (what vt10x leaves in unfilled cells).
	row := glyphRow("hi", 10)
	var b strings.Builder
	writeRowGlyphs(&b, row, 40) // grow to 40 cols
	got := b.String()

	if !strings.Contains(got, "hi") {
		t.Fatalf("expected content preserved on grow, got %q", got)
	}
	// No ellipsis indicator should appear on grow.
	if strings.Contains(got, "…") {
		t.Fatalf("did not expect ellipsis on grow, got %q", got)
	}
}

func TestEffectiveRowLenIgnoresTrailingPadding(t *testing.T) {
	row := glyphRow("ab", 20)
	if got := effectiveRowLen(row); got != 2 {
		t.Fatalf("effectiveRowLen with 2 chars + padding = %d, want 2", got)
	}
	empty := glyphRow("", 10)
	if got := effectiveRowLen(empty); got != 0 {
		t.Fatalf("effectiveRowLen on all-padding row = %d, want 0", got)
	}
}

func TestComposeScrollbackViewClampsAndPositionsWindow(t *testing.T) {
	sb := [][]vt10x.Glyph{
		glyphRow("OLD-1", 5),
		glyphRow("OLD-2", 5),
		glyphRow("OLD-3", 5),
		glyphRow("OLD-4", 5),
	}
	live := [][]vt10x.Glyph{
		glyphRow("LIVE1", 5),
		glyphRow("LIVE2", 5),
	}

	// offset = 0 -> bottom of stream visible: last 3 rows = OLD-4, LIVE1, LIVE2
	got := composeScrollbackView(sb, live, 5, 3, 0)
	if !strings.Contains(got, "OLD-4") || !strings.Contains(got, "LIVE1") || !strings.Contains(got, "LIVE2") {
		t.Fatalf("offset=0 expected to show OLD-4/LIVE1/LIVE2, got: %q", got)
	}
	if strings.Contains(got, "OLD-1") || strings.Contains(got, "OLD-2") {
		t.Fatalf("offset=0 should not show OLD-1/OLD-2, got: %q", got)
	}

	// offset = 2 -> window shifted 2 up: OLD-2, OLD-3, OLD-4
	got = composeScrollbackView(sb, live, 5, 3, 2)
	if !strings.Contains(got, "OLD-2") || !strings.Contains(got, "OLD-3") || !strings.Contains(got, "OLD-4") {
		t.Fatalf("offset=2 expected OLD-2/OLD-3/OLD-4, got: %q", got)
	}
	if strings.Contains(got, "LIVE1") {
		t.Fatalf("offset=2 should not show LIVE1, got: %q", got)
	}

	// offset > maxOffset (4 scrollback rows + 2 live - 3 view = 3) should
	// clamp at 3 -> top of scrollback visible plus one row pad above.
	got = composeScrollbackView(sb, live, 5, 3, 100)
	if !strings.Contains(got, "OLD-1") {
		t.Fatalf("offset=clamped expected OLD-1 visible, got: %q", got)
	}
}
