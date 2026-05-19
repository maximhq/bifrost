//go:build !windows

package runtime

import (
	"strings"
	"sync"

	"github.com/maximhq/vt10x"
)

// defaultScrollbackCap is the per-tab maximum number of rows retained in
// scrollback. At 200 cols and ~16 B/glyph this caps memory at ~16 MB/tab.
const defaultScrollbackCap = 5000

// scrollback is a fixed-capacity, append-only ring of evicted VT rows that
// powers the tab manager's scroll-mode history view. It's fed by
// vt10x.WithOnScrollUp and read by the renderer when the user enters
// scroll mode.
//
// Consecutive blank rows are deduplicated so a TUI emitting a run of
// blank-line LFs at the bottom of the screen (e.g. clearing trailing
// rows by scrolling them off) doesn't bloat history. Non-blank repeats
// are kept as-is because they often represent real user-visible output.
type scrollback struct {
	mu   sync.Mutex
	rows [][]vt10x.Glyph // newest at the tail
	cap  int
}

func newScrollback(capRows int) *scrollback {
	if capRows <= 0 {
		capRows = defaultScrollbackCap
	}
	return &scrollback{
		rows: make([][]vt10x.Glyph, 0, capRows),
		cap:  capRows,
	}
}

// push appends the given rows to the ring, dropping the oldest entries
// when the cap is exceeded. Each row is appended by reference; vt10x's
// WithOnScrollUp already hands us defensive copies, so we don't copy
// again. Consecutive blank rows are deduplicated so trailing blank-line
// LFs from TUIs don't bloat history; non-blank repeats are preserved
// because they often represent real user-visible output. Returns the
// number of rows actually added — the caller needs this to keep a
// viewer's scroll position stable across evictions.
func (s *scrollback) push(rows [][]vt10x.Glyph, _ bool) int {
	if len(rows) == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	added := 0
	for _, row := range rows {
		if len(s.rows) > 0 &&
			isBlankRow(s.rows[len(s.rows)-1]) &&
			isBlankRow(row) &&
			rowsEqual(s.rows[len(s.rows)-1], row) {
			continue
		}
		if len(s.rows) >= s.cap {
			// Drop oldest via O(n) slice shift; acceptable because the cap
			// is bounded at ~5000 rows and terminal scroll rates are low.
			copy(s.rows, s.rows[1:])
			s.rows = s.rows[:len(s.rows)-1]
		}
		s.rows = append(s.rows, row)
		added++
	}
	return added
}

// length returns the number of rows currently in scrollback.
func (s *scrollback) length() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

// snapshotTail returns the last n rows in top-to-bottom order, or all rows
// if there are fewer than n. The returned slice is a fresh shallow copy
// safe to read without holding the scrollback lock.
func (s *scrollback) snapshotTail(n int) [][]vt10x.Glyph {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) == 0 {
		return nil
	}
	if n > len(s.rows) {
		n = len(s.rows)
	}
	out := make([][]vt10x.Glyph, n)
	copy(out, s.rows[len(s.rows)-n:])
	return out
}

// isBlankRow reports whether every cell in row is whitespace (NUL or space).
// Used to gate dedup so we only drop runs of empty rows, not legitimately
// repeated output.
func isBlankRow(row []vt10x.Glyph) bool {
	for _, g := range row {
		if g.Char != 0 && g.Char != ' ' {
			return false
		}
	}
	return true
}

// rowsEqual returns true if two glyph rows have identical content,
// attributes, and colors. Cheap O(cols) compare used for dedup.
func rowsEqual(a, b []vt10x.Glyph) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// composeScrollbackView renders the active scroll-mode view into ANSI
// bytes. The visible window of contentRows is taken from the virtual
// stream (scrollback ++ liveRows), positioned so that offset == 0 shows
// the live grid at the bottom and offset > 0 shifts the window upward by
// that many lines.
//
// liveRows is the current vt10x grid snapshot (one glyph row per terminal
// row). scrollLen is the current scrollback length; we don't take rows
// from the scrollback directly to keep this function lock-free — callers
// pass a pre-snapped scrollback tail in sbRows.
//
// The window is clamped so it never positions above the top of
// scrollback. Rows that would fall above the available stream are emitted
// as blank.
func composeScrollbackView(sbRows [][]vt10x.Glyph, liveRows [][]vt10x.Glyph, cols, contentRows, offset int) string {
	totalAvail := len(sbRows) + len(liveRows)
	maxOffset := totalAvail - contentRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		offset = 0
	}

	bottomIdx := totalAvail - offset  // exclusive
	topIdx := bottomIdx - contentRows // can be negative
	padTop := 0
	if topIdx < 0 {
		padTop = -topIdx
		topIdx = 0
	}

	var b strings.Builder
	b.Grow(cols * contentRows * 3)

	for y := 0; y < contentRows; y++ {
		if y > 0 {
			b.WriteString("\x1b[0m\r\n")
		}
		if y < padTop {
			writeBlankRow(&b, cols)
			continue
		}
		idx := topIdx + (y - padTop)
		var row []vt10x.Glyph
		if idx < len(sbRows) {
			row = sbRows[idx]
		} else {
			row = liveRows[idx-len(sbRows)]
		}
		writeRowGlyphs(&b, row, cols)
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// snapshotLiveGrid extracts the current vt10x grid into a glyph matrix
// suitable for feeding composeScrollbackView. Caller must hold vt.Lock().
func snapshotLiveGrid(vt vt10x.View, cols, rows int) [][]vt10x.Glyph {
	vtCols, vtRows := vt.Size()
	out := make([][]vt10x.Glyph, rows)
	for y := 0; y < rows; y++ {
		rowWidth := cols
		if vtCols < rowWidth {
			rowWidth = vtCols
		}
		var row []vt10x.Glyph
		if y < vtRows && rowWidth > 0 {
			row = make([]vt10x.Glyph, rowWidth)
			for x := 0; x < rowWidth; x++ {
				row[x] = vt.Cell(x, y)
			}
		}
		out[y] = row
	}
	return out
}

// writeRowGlyphs emits one row of glyphs with style-diffed SGR sequences,
// padding to cols with default-style spaces when the row is shorter than
// the current display width, or truncating with a "…" indicator when
// it's wider. Trailing default-style padding cells are stripped before
// width comparison so resize-grow doesn't show absurd trailing whitespace
// from when the original grid was narrower.
func writeRowGlyphs(b *strings.Builder, row []vt10x.Glyph, cols int) {
	effectiveLen := effectiveRowLen(row)

	var prevFG, prevBG vt10x.Color
	var prevMode int16
	firstCell := true

	// If the stored row is wider than the display, truncate at cols-1 and
	// emit "…" so the user knows content was clipped by a resize-shrink.
	truncate := effectiveLen > cols
	contentEnd := effectiveLen
	if truncate {
		contentEnd = cols - 1
		if contentEnd < 0 {
			contentEnd = 0
		}
	}

	for x := 0; x < cols; x++ {
		switch {
		case x < contentEnd:
			g := row[x]
			if firstCell || g.FG != prevFG || g.BG != prevBG || g.Mode != prevMode {
				writeStyleSequence(b, g)
				prevFG, prevBG, prevMode = g.FG, g.BG, g.Mode
				firstCell = false
			}
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			b.WriteRune(ch)
		case truncate && x == cols-1:
			if firstCell || prevFG != vt10x.DefaultFG || prevBG != vt10x.DefaultBG || prevMode != 0 {
				b.WriteString("\x1b[0m\x1b[2m")
				prevFG, prevBG, prevMode = vt10x.DefaultFG, vt10x.DefaultBG, 0
				firstCell = false
			}
			b.WriteRune('…')
		default:
			if firstCell || prevFG != vt10x.DefaultFG || prevBG != vt10x.DefaultBG || prevMode != 0 {
				b.WriteString("\x1b[0m")
				prevFG, prevBG, prevMode = vt10x.DefaultFG, vt10x.DefaultBG, 0
				firstCell = false
			}
			b.WriteByte(' ')
		}
	}
}

// effectiveRowLen returns the row index just past the last cell that
// carries visible content. A "padding" cell is one with the default fg/bg
// and no attributes and either a NUL or space character — the state a
// vt10x grid leaves untouched cells in.
func effectiveRowLen(row []vt10x.Glyph) int {
	for i := len(row) - 1; i >= 0; i-- {
		g := row[i]
		isPadChar := g.Char == 0 || g.Char == ' '
		if isPadChar && g.FG == vt10x.DefaultFG && g.BG == vt10x.DefaultBG && g.Mode == 0 {
			continue
		}
		return i + 1
	}
	return 0
}

func writeBlankRow(b *strings.Builder, cols int) {
	b.WriteString("\x1b[0m")
	for x := 0; x < cols; x++ {
		b.WriteByte(' ')
	}
}
