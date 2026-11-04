package promptcompression

import (
	"fmt"
	"strings"
)

// trimMiddle keeps the first head and last tail lines of text and replaces the
// lines between them with a single marker line reporting the estimated tokens
// removed. Head and tail are the parts of a tool output that usually matter
// (command echo, final result), so the middle is what we drop. When the text
// has no more than head+tail lines there is nothing to cut and it is returned
// unchanged with false; this also covers a single huge line, which a
// line-based trim cannot split.
func trimMiddle(text string, head, tail int) (string, bool) {
	lines := strings.Split(text, "\n")
	if len(lines) <= head+tail {
		return text, false
	}
	removed := strings.Join(lines[head:len(lines)-tail], "\n")
	marker := fmt.Sprintf("[… %d tokens trimmed by promptcompression …]", estimateTokens(removed))

	out := make([]string, 0, head+tail+1)
	out = append(out, lines[:head]...)
	out = append(out, marker)
	out = append(out, lines[len(lines)-tail:]...)
	return strings.Join(out, "\n"), true
}

// normalizeWhitespace strips trailing spaces and tabs from every line and
// collapses runs of three or more blank lines into one. Leading indentation is
// never touched because it is significant in code and YAML. "\r" is not
// treated specially. Runs of one or two blank lines are kept as paragraph
// separators.
func normalizeWhitespace(text string) string {
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
