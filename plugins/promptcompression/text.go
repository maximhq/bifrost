package promptcompression

import (
	"fmt"
	"strings"
)

// trimMarkerPrefix and trimMarkerInfix identify a marker line written by this
// plugin (or by a previous gateway running it).
const (
	trimMarkerPrefix = "[… "
	trimMarkerInfix  = "tokens trimmed by promptcompression …]"
)

// trimMiddle keeps the first head and last tail lines of text and replaces the
// lines between them with a single marker line reporting the estimated tokens
// removed. Head and tail are the parts of a tool output that usually matter
// (command echo, final result), so the middle is what we drop. When the text
// has no more than head+tail lines there is nothing to cut and it is returned
// unchanged with false; this also covers a single huge line, which a
// line-based trim cannot split. Text whose line at index head is already a
// marker was trimmed before (possibly by an upstream gateway); trimming it
// again would nest markers and change bytes, which breaks provider prompt
// caching, so it is returned unchanged too.
//
// It locates the cut points with byte searches instead of strings.Split: this
// runs on every eligible output of every request, and the common outcome is
// "nothing to do", which must not allocate.
func trimMiddle(text string, head, tail int) (string, bool) {
	if strings.Count(text, "\n")+1 <= head+tail {
		return text, false
	}
	// midStart is the offset of line `head`; the n > head+tail check above
	// guarantees every newline searched for below exists.
	midStart := 0
	for i := 0; i < head; i++ {
		midStart += strings.IndexByte(text[midStart:], '\n') + 1
	}
	lineEnd := len(text)
	if i := strings.IndexByte(text[midStart:], '\n'); i >= 0 {
		lineEnd = midStart + i
	}
	if line := text[midStart:lineEnd]; strings.HasPrefix(line, trimMarkerPrefix) && strings.Contains(line, trimMarkerInfix) {
		return text, false
	}
	// tailStart is the offset of the first kept tail line (len(text)+1 when
	// tail is 0, so the removed range runs to the end).
	tailStart := len(text) + 1
	end := len(text)
	for i := 0; i < tail; i++ {
		end = strings.LastIndexByte(text[:end], '\n')
		tailStart = end + 1
	}
	removed := text[midStart : tailStart-1]
	marker := fmt.Sprintf("[… %d tokens trimmed by promptcompression …]", estimateTokens(removed))

	var b strings.Builder
	b.Grow(midStart + len(marker) + len(text) - tailStart + 1)
	b.WriteString(text[:midStart])
	b.WriteString(marker)
	if tail > 0 {
		b.WriteByte('\n')
		b.WriteString(text[tailStart:])
	}
	return b.String(), true
}

// normalizeWhitespace strips trailing spaces and tabs from every line and
// collapses runs of three or more blank lines into one. Leading indentation is
// never touched because it is significant in code and YAML. "\r" is not
// treated specially. Runs of one or two blank lines are kept as paragraph
// separators.
func normalizeWhitespace(text string) string {
	if !needsWhitespaceNormalization(text) {
		return text
	}
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

// needsWhitespaceNormalization reports whether normalizeWhitespace would
// change text, so clean outputs (the common case) skip the split-and-join. It
// must never answer false when the slow path would change something. Triggers:
// a space or tab before a newline or at the very end, and a run of three
// blank lines. Splitting on "\n" counts the empty piece before the first or
// after the last newline as a blank line, so such a run is "\n\n\n\n" inside
// the text but only "\n\n\n" at its start or end. A text that is exactly
// "\n\n" touches both edges at once and also qualifies.
func needsWhitespaceNormalization(text string) bool {
	if text == "" {
		return false
	}
	if last := text[len(text)-1]; last == ' ' || last == '\t' {
		return true
	}
	return strings.Contains(text, " \n") || strings.Contains(text, "\t\n") ||
		strings.Contains(text, "\n\n\n\n") ||
		strings.HasPrefix(text, "\n\n\n") || strings.HasSuffix(text, "\n\n\n") ||
		text == "\n\n"
}
