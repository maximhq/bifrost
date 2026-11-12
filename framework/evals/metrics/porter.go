// Package metrics holds deterministic text metrics, ported from the Python libraries
// Maxim runs so that scores match: sacrebleu, rouge-score, jiwer and NLTK.
package metrics

import "strings"

// porterIrregular maps the irregular forms NLTK's stemmer special-cases to their stems.
var porterIrregular = map[string]string{
	"sky": "sky", "skies": "sky",
	"dying": "die", "lying": "lie", "tying": "tie",
	"news":    "news",
	"innings": "inning", "inning": "inning",
	"outings": "outing", "outing": "outing",
	"cannings": "canning", "canning": "canning",
	"howe":    "howe",
	"proceed": "proceed", "exceed": "exceed", "succeed": "succeed",
}

// porterRule is one suffix rewrite. The suffix "*d" means "ends in a double consonant".
type porterRule struct {
	suffix      string
	replacement string
	condition   func(stem []rune) bool // nil means always
}

// PorterStem stems a word with the Porter algorithm as NLTK implements it in its
// default mode (NLTK_EXTENSIONS). That variant differs from the original algorithm in
// several rules, and it is the one rouge-score and NLTK's METEOR use, so it is ported
// rule for rule rather than replaced with a textbook Porter stemmer.
func PorterStem(word string) string {
	lower := strings.ToLower(word)
	if stem, ok := porterIrregular[word]; ok {
		return stem
	}
	runes := []rune(lower)
	if len([]rune(word)) <= 2 {
		return lower
	}
	runes = porterStep1a(runes)
	runes = porterStep1b(runes)
	runes = porterStep1c(runes)
	runes = porterStep2(runes)
	runes = porterStep3(runes)
	runes = porterStep4(runes)
	runes = porterStep5a(runes)
	runes = porterStep5b(runes)
	return string(runes)
}

func porterIsVowel(r rune) bool {
	return r == 'a' || r == 'e' || r == 'i' || r == 'o' || r == 'u'
}

// porterIsConsonant reports whether word[i] is a consonant. A "y" is a consonant at the
// start of a word and otherwise the opposite of the letter before it.
func porterIsConsonant(word []rune, i int) bool {
	if porterIsVowel(word[i]) {
		return false
	}
	if word[i] == 'y' {
		if i == 0 {
			return true
		}
		return !porterIsConsonant(word, i-1)
	}
	return true
}

// porterMeasure counts the vowel-consonant sequences in a stem.
func porterMeasure(stem []rune) int {
	count := 0
	previousVowel := false
	for i := range stem {
		consonant := porterIsConsonant(stem, i)
		if consonant && previousVowel {
			count++
		}
		previousVowel = !consonant
	}
	return count
}

func porterHasPositiveMeasure(stem []rune) bool { return porterMeasure(stem) > 0 }

func porterContainsVowel(stem []rune) bool {
	for i := range stem {
		if !porterIsConsonant(stem, i) {
			return true
		}
	}
	return false
}

func porterEndsDoubleConsonant(word []rune) bool {
	n := len(word)
	return n >= 2 && word[n-1] == word[n-2] && porterIsConsonant(word, n-1)
}

// porterEndsCVC reports whether a word ends consonant-vowel-consonant, where the last
// consonant is not w, x or y. NLTK also counts a two-letter vowel-consonant word.
func porterEndsCVC(word []rune) bool {
	n := len(word)
	if n >= 3 && porterIsConsonant(word, n-3) && !porterIsConsonant(word, n-2) && porterIsConsonant(word, n-1) {
		last := word[n-1]
		if last != 'w' && last != 'x' && last != 'y' {
			return true
		}
	}
	return n == 2 && !porterIsConsonant(word, 0) && porterIsConsonant(word, 1)
}

func porterHasSuffix(word []rune, suffix string) bool {
	s := []rune(suffix)
	if len(s) > len(word) {
		return false
	}
	offset := len(word) - len(s)
	for i, r := range s {
		if word[offset+i] != r {
			return false
		}
	}
	return true
}

func porterJoin(stem []rune, replacement string) []rune {
	out := make([]rune, 0, len(stem)+len(replacement))
	out = append(out, stem...)
	return append(out, []rune(replacement)...)
}

// porterApplyRules applies the first rule whose suffix matches. A matching rule whose
// condition fails ends the step with the word unchanged; later rules are not tried.
func porterApplyRules(word []rune, rules []porterRule) []rune {
	for _, rule := range rules {
		if rule.suffix == "*d" {
			if !porterEndsDoubleConsonant(word) {
				continue
			}
			stem := word[:len(word)-2]
			if rule.condition == nil || rule.condition(stem) {
				return porterJoin(stem, rule.replacement)
			}
			return word
		}
		if porterHasSuffix(word, rule.suffix) {
			stem := word[:len(word)-len([]rune(rule.suffix))]
			if rule.condition == nil || rule.condition(stem) {
				return porterJoin(stem, rule.replacement)
			}
			return word
		}
	}
	return word
}

func porterStep1a(word []rune) []rune {
	// NLTK: "dies" becomes "die", not "di".
	if porterHasSuffix(word, "ies") && len(word) == 4 {
		return porterJoin(word[:len(word)-3], "ie")
	}
	return porterApplyRules(word, []porterRule{
		{suffix: "sses", replacement: "ss"},
		{suffix: "ies", replacement: "i"},
		{suffix: "ss", replacement: "ss"},
		{suffix: "s", replacement: ""},
	})
}

func porterStep1b(word []rune) []rune {
	// NLTK: "died" becomes "die", and other -ied words lose the "ed".
	if porterHasSuffix(word, "ied") {
		if len(word) == 4 {
			return porterJoin(word[:len(word)-3], "ie")
		}
		return porterJoin(word[:len(word)-3], "i")
	}
	if porterHasSuffix(word, "eed") {
		stem := word[:len(word)-3]
		if porterMeasure(stem) > 0 {
			return porterJoin(stem, "ee")
		}
		return word
	}
	var intermediate []rune
	matched := false
	for _, suffix := range []string{"ed", "ing"} {
		if porterHasSuffix(word, suffix) {
			intermediate = word[:len(word)-len(suffix)]
			if porterContainsVowel(intermediate) {
				matched = true
				break
			}
		}
	}
	if !matched {
		return word
	}
	last := string(intermediate[len(intermediate)-1])
	return porterApplyRules(intermediate, []porterRule{
		{suffix: "at", replacement: "ate"},
		{suffix: "bl", replacement: "ble"},
		{suffix: "iz", replacement: "ize"},
		{suffix: "*d", replacement: last, condition: func([]rune) bool {
			return last != "l" && last != "s" && last != "z"
		}},
		{suffix: "", replacement: "e", condition: func(stem []rune) bool {
			return porterMeasure(stem) == 1 && porterEndsCVC(stem)
		}},
	})
}

func porterStep1c(word []rune) []rune {
	// NLTK: y becomes i only after a consonant, and never in a two-letter word.
	return porterApplyRules(word, []porterRule{
		{suffix: "y", replacement: "i", condition: func(stem []rune) bool {
			return len(stem) > 1 && porterIsConsonant(stem, len(stem)-1)
		}},
	})
}

func porterStep2(word []rune) []rune {
	// NLTK: -alli is reduced to -al and then stemmed again, so "rationally" loses both.
	if porterHasSuffix(word, "alli") && porterHasPositiveMeasure(word[:len(word)-4]) {
		return porterStep2(porterJoin(word[:len(word)-4], "al"))
	}
	positive := porterHasPositiveMeasure
	return porterApplyRules(word, []porterRule{
		{"ational", "ate", positive},
		{"tional", "tion", positive},
		{"enci", "ence", positive},
		{"anci", "ance", positive},
		{"izer", "ize", positive},
		{"bli", "ble", positive},
		{"alli", "al", positive},
		{"entli", "ent", positive},
		{"eli", "e", positive},
		{"ousli", "ous", positive},
		{"ization", "ize", positive},
		{"ation", "ate", positive},
		{"ator", "ate", positive},
		{"alism", "al", positive},
		{"iveness", "ive", positive},
		{"fulness", "ful", positive},
		{"ousness", "ous", positive},
		{"aliti", "al", positive},
		{"iviti", "ive", positive},
		{"biliti", "ble", positive},
		{"fulli", "ful", positive},
		// NLTK measures the stem with the "l" kept: the word minus "ogi".
		{"logi", "log", func([]rune) bool { return porterHasPositiveMeasure(word[:len(word)-3]) }},
	})
}

func porterStep3(word []rune) []rune {
	positive := porterHasPositiveMeasure
	return porterApplyRules(word, []porterRule{
		{"icate", "ic", positive},
		{"ative", "", positive},
		{"alize", "al", positive},
		{"iciti", "ic", positive},
		{"ical", "ic", positive},
		{"ful", "", positive},
		{"ness", "", positive},
	})
}

func porterStep4(word []rune) []rune {
	long := func(stem []rune) bool { return porterMeasure(stem) > 1 }
	return porterApplyRules(word, []porterRule{
		{"al", "", long},
		{"ance", "", long},
		{"ence", "", long},
		{"er", "", long},
		{"ic", "", long},
		{"able", "", long},
		{"ible", "", long},
		{"ant", "", long},
		{"ement", "", long},
		{"ment", "", long},
		{"ent", "", long},
		{"ion", "", func(stem []rune) bool {
			return porterMeasure(stem) > 1 && len(stem) > 0 && (stem[len(stem)-1] == 's' || stem[len(stem)-1] == 't')
		}},
		{"ou", "", long},
		{"ism", "", long},
		{"ate", "", long},
		{"iti", "", long},
		{"ous", "", long},
		{"ive", "", long},
		{"ize", "", long},
	})
}

func porterStep5a(word []rune) []rune {
	if porterHasSuffix(word, "e") {
		stem := word[:len(word)-1]
		measure := porterMeasure(stem)
		if measure > 1 || (measure == 1 && !porterEndsCVC(stem)) {
			return stem
		}
	}
	return word
}

func porterStep5b(word []rune) []rune {
	return porterApplyRules(word, []porterRule{
		{"ll", "l", func([]rune) bool { return porterMeasure(word[:len(word)-1]) > 1 }},
	})
}
