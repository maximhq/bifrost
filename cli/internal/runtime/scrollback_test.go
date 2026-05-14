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
