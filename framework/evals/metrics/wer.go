package metrics

import (
	"strings"
	"unicode"
)

// WER is the word error rate: the word-level edit distance from the reference to the
// candidate, divided by the number of reference words. It can exceed 1.
func WER(candidate, reference string) (float64, error) {
	referenceWords := werWords(reference)
	if len(referenceWords) == 0 {
		return 0, ErrEmptyReference
	}
	return float64(editDistance(werWords(candidate), referenceWords)) / float64(len(referenceWords)), nil
}

// werWords splits text the way jiwer does by default: runs of two or more whitespace
// characters collapse to one space, the ends are trimmed, and the text is cut at single
// spaces. A lone newline or tab is therefore not a word break: "a\nb" is one word. That
// is jiwer's behaviour, kept so scores match.
func werWords(text string) []string {
	var b strings.Builder
	b.Grow(len(text))
	runes := []rune(text)
	for i := 0; i < len(runes); {
		if !unicode.IsSpace(runes[i]) {
			b.WriteRune(runes[i])
			i++
			continue
		}
		end := i
		for end < len(runes) && unicode.IsSpace(runes[end]) {
			end++
		}
		if end-i >= 2 {
			b.WriteByte(' ')
		} else {
			b.WriteRune(runes[i])
		}
		i = end
	}
	parts := strings.Split(strings.TrimSpace(b.String()), " ")
	words := parts[:0]
	for _, part := range parts {
		if part != "" {
			words = append(words, part)
		}
	}
	return words
}

// editDistance is the Levenshtein distance between two word sequences, in two rows.
func editDistance(a, b []string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}
