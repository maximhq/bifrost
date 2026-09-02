package complexity

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

//go:generate go run genunsegmentedscripts.go

// containsWord checks if a word appears in text delimited by word boundaries.
func containsWord(text, word string) bool {
	if word == "" {
		return false
	}

	idx := 0
	for {
		pos := strings.Index(text[idx:], word)
		if pos == -1 {
			return false
		}
		start := idx + pos
		end := start + len(word)

		startOk := start == 0 || isWordBoundary(lastRune(text[:start]))
		endOk := end == len(text) || isWordBoundary(firstRune(text[end:]))

		if startOk && endOk {
			return true
		}
		idx = start + 1
		if idx >= len(text) {
			return false
		}
	}
}

func firstRune(text string) rune {
	r, _ := utf8.DecodeRuneInString(text)
	return r
}

func lastRune(text string) rune {
	r, _ := utf8.DecodeLastRuneInString(text)
	return r
}

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// isWordBoundary reports whether the rune next to a candidate match still
// allows it to count as a whole word.
func isWordBoundary(neighbour rune) bool {
	return !isWordChar(neighbour) || isUnsegmentedLetter(neighbour)
}

// isUnsegmentedLetter reports whether r is a letter of a script that does not
// separate words with spaces. Hangul is included although UAX #29 groups it
// with the space-separating scripts, because Korean particles attach directly.
func isUnsegmentedLetter(r rune) bool {
	if r < 0x80 { // no unsegmented script lies in ASCII
		return false
	}
	return unicode.Is(unsegmentedScriptLetters, r) || unicode.Is(unicode.Hangul, r)
}

func containsUnsegmentedLetter(text string) bool {
	for _, r := range text {
		if isUnsegmentedLetter(r) {
			return true
		}
	}
	return false
}

func countWordsNoAlloc(text string) int {
	count := 0
	inWord := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			inWord = false
			continue
		}
		if !inWord {
			count++
			inWord = true
		}
	}
	return count
}

func scoreCount(count, capAt int) float64 {
	if capAt <= 0 {
		return 0.0
	}
	return math.Min(1.0, float64(count)/float64(capAt))
}

// scoreTokenCount scores based on word count of the text.
func scoreTokenCount(words int) float64 {
	switch {
	case words < 15:
		return float64(words) / 15.0 * 0.3
	case words <= 400:
		return 0.3 + float64(words-15)/385.0*0.4
	default:
		extra := math.Min(0.3, float64(words-400)/600.0*0.3)
		return 0.7 + extra
	}
}

func clamp(val, min, max float64) float64 {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}
