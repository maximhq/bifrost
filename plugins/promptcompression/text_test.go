package promptcompression

import (
	"fmt"
	"strings"
	"testing"
)

func numberedLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("L%d", i+1)
	}
	return strings.Join(lines, "\n")
}

func TestTrimMiddleKeepsHeadTail(t *testing.T) {
	got, ok := trimMiddle(numberedLines(100), 20, 20)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.HasPrefix(got, "L1\n") || !strings.HasSuffix(got, "L100") {
		t.Errorf("unexpected head/tail: %q", got)
	}
	if !strings.Contains(got, "tokens trimmed by promptcompression") {
		t.Errorf("missing marker: %q", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 41 || lines[19] != "L20" || lines[21] != "L81" {
		t.Errorf("wrong structure: %d lines", len(lines))
	}
	removed := strings.Join(strings.Split(numberedLines(100), "\n")[20:80], "\n")
	want := fmt.Sprintf("[… %d tokens trimmed by promptcompression …]", estimateTokens(removed))
	if lines[20] != want {
		t.Errorf("marker = %q want %q", lines[20], want)
	}
}

func TestTrimMiddleShortUnchanged(t *testing.T) {
	in := numberedLines(40)
	got, ok := trimMiddle(in, 20, 20)
	if ok || got != in {
		t.Errorf("expected unchanged, ok=false; ok=%v", ok)
	}
}

func TestTrimMiddleSingleLine(t *testing.T) {
	in := strings.Repeat("x", 10000)
	got, ok := trimMiddle(in, 20, 20)
	if ok || got != in {
		t.Errorf("expected unchanged, ok=false; ok=%v", ok)
	}
}

func TestTrimMiddleZeroHead(t *testing.T) {
	got, ok := trimMiddle(numberedLines(20), 0, 5)
	if !ok {
		t.Fatal("expected ok=true")
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 6 || !strings.HasPrefix(lines[0], "[… ") || lines[1] != "L16" || lines[5] != "L20" {
		t.Errorf("unexpected result: %q", got)
	}
}

func TestNormalizeWhitespace(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a  \t\nb", "a\nb"},
		{"a\n\n\n\nb", "a\n\nb"},
		{"a\n\nb", "a\n\nb"},
		{"a\n\n\nb", "a\n\n\nb"}, // two blank lines are kept
		{"a\n  \n\t\n \nb", "a\n\nb"},
		{"    indented\n\tx", "    indented\n\tx"},
		{"a \r\nb", "a \r\nb"}, // \r is never stripped
	}
	for _, c := range cases {
		if got := normalizeWhitespace(c.in); got != c.want {
			t.Errorf("normalizeWhitespace(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
