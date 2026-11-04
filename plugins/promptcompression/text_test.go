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
		{"a\n\n\n", "a\n"},     // edge runs: the empty piece after the last "\n" is a blank line
		{"\n\n\nx", "\nx"},
		{"\n\n\n", ""},
	}
	for _, c := range cases {
		if got := normalizeWhitespace(c.in); got != c.want {
			t.Errorf("normalizeWhitespace(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestTrimMiddleIdempotent(t *testing.T) {
	big := numberedLines(300)
	r1, ok := trimMiddle(big, 20, 20)
	if !ok {
		t.Fatal("first pass should trim")
	}
	r2, ok := trimMiddle(r1, 20, 20)
	if ok || r2 != r1 {
		t.Errorf("second pass must be a no-op; ok=%v", ok)
	}
}

func TestTrimMiddleMarkerElsewhereIsNotTrimmed(t *testing.T) {
	lines := strings.Split(numberedLines(100), "\n")
	lines[3] = "[… 5 tokens trimmed by promptcompression …]"
	_, ok := trimMiddle(strings.Join(lines, "\n"), 20, 20)
	if !ok {
		t.Error("a marker outside the cut position must not block trimming")
	}
}

func TestTrimMiddleZeroTail(t *testing.T) {
	got, ok := trimMiddle(numberedLines(10), 3, 0)
	if !ok {
		t.Fatal("expected ok=true")
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 4 || lines[2] != "L3" || !strings.HasPrefix(lines[3], "[… ") {
		t.Errorf("unexpected result: %q", got)
	}
}

func TestTrimMiddleNoSplitAllocs(t *testing.T) {
	in := numberedLines(200)
	allocs := testing.AllocsPerRun(100, func() {
		trimMiddle(in, 100, 100)
	})
	if allocs != 0 {
		t.Errorf("allocs = %v want 0", allocs)
	}
}

func TestNormalizeWhitespaceUnchangedNoAlloc(t *testing.T) {
	in := "a\n\nb\n\n\nc\n  indented\nend"
	if got := normalizeWhitespace(in); got != in {
		t.Fatalf("changed: %q", got)
	}
	allocs := testing.AllocsPerRun(100, func() {
		normalizeWhitespace(in)
	})
	if allocs != 0 {
		t.Errorf("allocs = %v want 0", allocs)
	}
}

// slowNormalizeWhitespace is the reference split-and-join implementation that
// normalizeWhitespace's fast path must agree with on every input.
func slowNormalizeWhitespace(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		line := strings.TrimRight(lines[i], " \t")
		if line != "" {
			out = append(out, line)
			i++
			continue
		}
		j := i
		for j < len(lines) && strings.TrimRight(lines[j], " \t") == "" {
			j++
		}
		if j-i >= 3 {
			out = append(out, "")
		} else {
			for k := i; k < j; k++ {
				out = append(out, "")
			}
		}
		i = j
	}
	return strings.Join(out, "\n")
}

func TestNormalizeWhitespaceFastPathMatchesSlowPath(t *testing.T) {
	cases := []string{
		"a\n\n\n", "\n\n\nx", "\n\n\n", " \n\n\n", "\n\n \n", "a\t", "", "\n",
		"a\n\n\nb", "a\n\n\n\nb", "a\n", "a\n\n", "\n\n", "\n\nx", "x\n\n",
		"a\n\n\n\n", "\n\n\n\nx", "clean\ntext",
	}
	for _, c := range cases {
		if got, want := normalizeWhitespace(c), slowNormalizeWhitespace(c); got != want {
			t.Errorf("normalizeWhitespace(%q)=%q slow=%q", c, got, want)
		}
	}
}

func TestTrimMiddleAlreadyTrimmedNoAlloc(t *testing.T) {
	r1, _ := trimMiddle(numberedLines(300), 20, 20)
	allocs := testing.AllocsPerRun(100, func() {
		trimMiddle(r1, 20, 20)
	})
	if allocs != 0 {
		t.Errorf("allocs = %v want 0", allocs)
	}
}
