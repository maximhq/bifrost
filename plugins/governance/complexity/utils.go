package complexity

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// containsWord checks if a word appears in text delimited by non-alphanumeric boundaries.
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

		startOk := start == 0 || !isWordChar(lastRune(text[:start]))
		endOk := end == len(text) || !isWordChar(firstRune(text[end:]))

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

func scoreOutputComplexity(signals textSignalCounts) (float64, int) {
	totalCount := signals.enumCount + signals.comprehensiveCount + signals.elaborationCount
	if totalCount == 0 {
		return 0.0, 0
	}

	enumScore := math.Min(1.0, float64(signals.enumCount))
	compScore := math.Min(1.0, float64(signals.comprehensiveCount))
	elabScore := math.Min(1.0, float64(signals.elaborationCount))

	rawScore := (enumScore * 0.4) + (compScore * 0.3) + (elabScore * 0.3)
	if signals.limitingQualifierCount > 0 {
		rawScore *= 0.3
	}

	return math.Min(1.0, rawScore), totalCount
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
