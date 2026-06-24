package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/maximhq/vt10x"
)

type replayFixture struct {
	Cols      int              `json:"cols"`
	Rows      int              `json:"rows"`
	Chunks    []string         `json:"chunks"`
	Snapshots []replaySnapshot `json:"snapshots"`
}

type replaySnapshot struct {
	AfterChunk int      `json:"after_chunk"`
	Screen     []string `json:"screen"`
}

func TestOpencodeScrollReplayFixture(t *testing.T) {
	t.Parallel()

	fixture := loadReplayFixture(t, "testdata/opencode_scroll_replay.json")
	term := vt10x.New(vt10x.WithSize(fixture.Cols, fixture.Rows))
	var normalizer vtStreamNormalizer

	checkpoints := make(map[int][]string, len(fixture.Snapshots))
	for _, snapshot := range fixture.Snapshots {
		checkpoints[snapshot.AfterChunk] = snapshot.Screen
	}

	for i, chunk := range fixture.Chunks {
		data := normalizer.Normalize([]byte(chunk))
		if len(data) > 0 {
			if _, err := term.Write(data); err != nil {
				t.Fatalf("write chunk %d: %v", i+1, err)
			}
		}

		if want, ok := checkpoints[i+1]; ok {
			if got := snapshotScreen(term, fixture.Cols, fixture.Rows); !equalLines(got, want) {
				t.Fatalf("snapshot after chunk %d mismatch\nwant:\n%s\n\ngot:\n%s", i+1, strings.Join(want, "\n"), strings.Join(got, "\n"))
			}
		}
	}
}

func TestVTStreamNormalizerHandlesSplitTrueColorSGR(t *testing.T) {
	t.Parallel()

	term := vt10x.New(vt10x.WithSize(8, 1))
	var normalizer vtStreamNormalizer

	if data := normalizer.Normalize([]byte("\x1b[38:2::100")); len(data) != 0 {
		t.Fatalf("expected incomplete chunk to be buffered, got %q", string(data))
	}
	data := normalizer.Normalize([]byte(":150:200mHi"))
	if _, err := term.Write(data); err != nil {
		t.Fatalf("write normalized chunk: %v", err)
	}

	term.Lock()
	defer term.Unlock()

	cell := term.Cell(0, 0)
	if cell.Char != 'H' {
		t.Fatalf("expected first cell to contain H, got %q", cell.Char)
	}
	if want := vt10x.Color(100<<16 | 150<<8 | 200); cell.FG != want {
		t.Fatalf("expected truecolor fg %v, got %v", want, cell.FG)
	}
}

func TestVTAlternateScreenRestore(t *testing.T) {
	t.Parallel()

	term := vt10x.New(vt10x.WithSize(10, 3))
	var normalizer vtStreamNormalizer

	writeReplayChunk(t, term, &normalizer, "main")
	writeReplayChunk(t, term, &normalizer, "\x1b[?1049h\x1b[2J\x1b[Halt")
	writeReplayChunk(t, term, &normalizer, "\x1b[?1049l")

	got := snapshotScreen(term, 10, 3)
	if got[0] != "main      " {
		t.Fatalf("expected main screen to be restored, got %q", got[0])
	}
}

func TestVTWritesCursorPositionRepliesToConfiguredWriter(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	term := vt10x.New(
		vt10x.WithWriter(&out),
		vt10x.WithSize(10, 3),
	)

	if _, err := term.Write([]byte("\x1b[6n")); err != nil {
		t.Fatalf("write cpr request: %v", err)
	}

	if got := out.String(); got != "\x1b[1;1R" {
		t.Fatalf("expected CPR reply %q, got %q", "\x1b[1;1R", got)
	}
}

func TestExtractCursorVisible(t *testing.T) {
	t.Parallel()

	if got := extractCursorVisible([]byte("hello")); got != -1 {
		t.Fatalf("extractCursorVisible(no toggle) = %d, want -1", got)
	}
	if got := extractCursorVisible([]byte("\x1b[?25h")); got != 1 {
		t.Fatalf("extractCursorVisible(show) = %d, want 1", got)
	}
	if got := extractCursorVisible([]byte("\x1b[?25l")); got != 0 {
		t.Fatalf("extractCursorVisible(hide) = %d, want 0", got)
	}
	if got := extractCursorVisible([]byte("\x1b[?25h...\x1b[?25l")); got != 0 {
		t.Fatalf("extractCursorVisible(last wins) = %d, want 0", got)
	}
}

func TestNormalizerDropsKittyKeyboardProtocol(t *testing.T) {
	t.Parallel()

	term := vt10x.New(vt10x.WithSize(20, 5))
	var normalizer vtStreamNormalizer

	// Write text, move cursor to row 3, then send Kitty keyboard sequences.
	// Without the fix, \x1b[>1u would be misinterpreted as DECRC (cursor
	// restore) and snap the cursor back to (0,0).
	writeReplayChunk(t, term, &normalizer, "hello")      // cursor at (5, 0)
	writeReplayChunk(t, term, &normalizer, "\x1b[4;1H")  // move to row 4, col 1
	writeReplayChunk(t, term, &normalizer, "world")      // cursor at (5, 3)
	writeReplayChunk(t, term, &normalizer, "\x1b[>1u")   // push kitty keyboard mode
	writeReplayChunk(t, term, &normalizer, "\x1b[?u")    // query kitty keyboard mode
	writeReplayChunk(t, term, &normalizer, "\x1b[<u")    // pop kitty keyboard mode
	writeReplayChunk(t, term, &normalizer, "\x1b[=1;2u") // kitty with flags

	term.Lock()
	cursor := term.Cursor()
	term.Unlock()

	// Cursor should still be at (5, 3) — kitty sequences must not move it.
	if cursor.X != 5 || cursor.Y != 3 {
		t.Fatalf("expected cursor at (5, 3), got (%d, %d) — kitty sequences corrupted cursor state", cursor.X, cursor.Y)
	}

	// Verify the text was written correctly.
	got := snapshotScreen(term, 20, 5)
	if !strings.HasPrefix(got[0], "hello") {
		t.Fatalf("expected 'hello' on row 0, got %q", got[0])
	}
	if !strings.HasPrefix(got[3], "world") {
		t.Fatalf("expected 'world' on row 3, got %q", got[3])
	}
}

func TestNormalizerPassesThroughNormalCSI(t *testing.T) {
	t.Parallel()

	var normalizer vtStreamNormalizer

	// Regular CSI sequences (\x1b[?1049h, \x1b[2J, \x1b[H) must pass through.
	data := normalizer.Normalize([]byte("\x1b[?1049h\x1b[2J\x1b[H"))
	if string(data) != "\x1b[?1049h\x1b[2J\x1b[H" {
		t.Fatalf("expected normal CSI to pass through, got %q", string(data))
	}

	// Kitty keyboard protocol must be stripped.
	data = normalizer.Normalize([]byte("A\x1b[>1uB"))
	if string(data) != "AB" {
		t.Fatalf("expected kitty sequence to be stripped, got %q", string(data))
	}
}

func loadReplayFixture(t *testing.T, path string) replayFixture {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replay fixture: %v", err)
	}

	var fixture replayFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("unmarshal replay fixture: %v", err)
	}
	return fixture
}

func writeReplayChunk(t *testing.T, term vt10x.Terminal, normalizer *vtStreamNormalizer, chunk string) {
	t.Helper()

	data := normalizer.Normalize([]byte(chunk))
	if len(data) == 0 {
		return
	}
	if _, err := term.Write(data); err != nil {
		t.Fatalf("write replay chunk: %v", err)
	}
}

func snapshotScreen(term vt10x.Terminal, cols, rows int) []string {
	term.Lock()
	defer term.Unlock()

	lines := make([]string, rows)
	for y := 0; y < rows; y++ {
		var line []rune
		for x := 0; x < cols; x++ {
			ch := term.Cell(x, y).Char
			if ch == 0 {
				ch = ' '
			}
			line = append(line, ch)
		}
		lines[y] = string(line)
	}
	return lines
}

func equalLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
