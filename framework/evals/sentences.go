package evals

import (
	"strings"
	"unicode"
)

// Abbreviation lists, taken from the English rules of pysbd, the sentence splitter
// Maxim uses. A period after one of these does not always end a sentence.
var (
	// sentencePrepositive abbreviations come before a name, so their period never ends a sentence.
	sentencePrepositive = wordSetOf("adm attys brig capt cmdr col cpl det dr gen gov ing lt maj mr mrs ms mt messrs mssrs prof ph rep reps rev sen sens sgt st supt v vs fig")
	// sentenceNumberAbbreviations keep their period when a number or a bracket follows.
	sentenceNumberAbbreviations = wordSetOf("art ext no nos p pp")
	// sentenceAbbreviations keep their period when the sentence visibly continues.
	sentenceAbbreviations = wordSetOf("adj adm adv al ala alta apr arc ariz ark art assn asst attys aug ave bart bld bldg blvd brig bros btw cal calif capt cl cmdr co col colo comdr con conn corp cpl cres ct dak dec del dept det dist dr drs ens esp esq etc exp expy ext feb fed fla ft fwy fy ga gen gov hon hosp hr hway hwy ia id ida ill inc ind ing insp is jan jr jul jun kan kans ken ky la lt ltd maj man mar mass may md me med messrs mex mfg mich min minn miss mlle mm mme mo mont mr mrs ms msgr mssrs mt mtn neb nebr nev no nos nov nr oct ok okla ont op ord ore p pa pd pde penn penna pfc ph pl plz pp prof pvt que rd rs ref rep reps res rev rt sask sec sen sens sep sept sfc sgt sr st supt surg tce tenn tex univ usafa ut va v ver viz vs vt wash wis wisc wy wyo yuk fig")
)

func wordSetOf(words string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, word := range strings.Fields(words) {
		set[word] = struct{}{}
	}
	return set
}

// SplitSentences splits text into sentences. Each sentence keeps its trailing
// whitespace, so the pieces join back into the original text.
//
// It follows the rules of pysbd for the cases that matter in model output: titles and
// other abbreviations, initials, a.m. and p.m., decimals, numbered lists, ellipses,
// quoted sentences and line breaks. It is not a full port. Two deliberate differences:
// pysbd splits inside a URL at a question mark, and it does not split after a run of
// three exclamation marks. This does neither.
func SplitSentences(text string) []string {
	runes := []rune(text)
	var sentences []string
	start := 0
	emit := func(end int) {
		if strings.TrimSpace(string(runes[start:end])) != "" {
			sentences = append(sentences, string(runes[start:end]))
		}
		start = end
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\n' {
			emit(i + 1)
			continue
		}
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		// Take the whole run of terminators, then any closing quotes or brackets.
		end := i
		for end+1 < len(runes) && isTerminator(runes[end+1]) {
			end++
		}
		closed := end
		for closed+1 < len(runes) && isClosing(runes[closed+1]) {
			closed++
		}
		// A sentence ends only before whitespace or at the end of the text.
		if closed+1 < len(runes) && !unicode.IsSpace(runes[closed+1]) {
			i = closed
			continue
		}
		next := closed + 1
		for next < len(runes) && unicode.IsSpace(runes[next]) && runes[next] != '\n' {
			next++
		}
		if !endsSentence(runes, start, i, end, closed, next) {
			i = closed
			continue
		}
		// A line break right after the sentence belongs to it.
		if next < len(runes) && runes[next] == '\n' {
			next++
		}
		emit(next)
		i = next - 1
	}
	emit(len(runes))
	return sentences
}

func isTerminator(r rune) bool { return r == '.' || r == '!' || r == '?' }

func isClosing(r rune) bool {
	switch r {
	case '"', '\'', ')', ']', '}', '”', '’', '»':
		return true
	}
	return false
}

// endsSentence decides whether the terminator run runes[first:last+1] ends a sentence.
// closed is the last closing quote or bracket after it and next is the first character
// of what follows, past the spaces.
func endsSentence(runes []rune, start, first, last, closed, next int) bool {
	atEnd := next >= len(runes)
	var following rune
	if !atEnd {
		following = runes[next]
	}
	continues := !atEnd && unicode.IsLower(following)

	// A quoted or bracketed sentence that the outer sentence carries on from: "Why?" she asked.
	if closed > last && continues {
		return false
	}
	if runes[first] != '.' {
		return true
	}
	// An ellipsis ends a sentence only when a new one clearly starts.
	if last > first {
		return atEnd || unicode.IsUpper(following) || following == '\n'
	}

	word := strings.ToLower(wordBefore(runes, start, first))
	if word == "" {
		return true
	}
	// A list number at the start of a sentence: "1. Install the package."
	if isDigits(word) && strings.TrimSpace(string(runes[start:first-len([]rune(word))])) == "" {
		return false
	}
	// Initials and dotted abbreviations: "C.", "U.S.", "e.g.". Only a.m. and p.m. can
	// end a sentence, and only when a capital letter follows.
	if isDottedLetters(word) {
		if word == "a.m" || word == "p.m" {
			return atEnd || unicode.IsUpper(following)
		}
		return atEnd
	}
	if _, ok := sentencePrepositive[word]; ok {
		return atEnd
	}
	if _, ok := sentenceNumberAbbreviations[word]; ok && !atEnd && (unicode.IsDigit(following) || following == '(') {
		return false
	}
	if _, ok := sentenceAbbreviations[word]; ok && !atEnd {
		if unicode.IsLower(following) || unicode.IsDigit(following) || following == '(' {
			return false
		}
	}
	return true
}

// wordBefore returns the run of letters, digits and inner periods that ends at pos.
func wordBefore(runes []rune, start, pos int) string {
	begin := pos
	for begin > start {
		r := runes[begin-1]
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' {
			begin--
			continue
		}
		break
	}
	return string(runes[begin:pos])
}

func isDigits(word string) bool {
	for _, r := range word {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return word != ""
}

// isDottedLetters reports whether word is single letters joined by periods: "c", "u.s", "e.g".
func isDottedLetters(word string) bool {
	parts := strings.Split(word, ".")
	for _, part := range parts {
		runes := []rune(part)
		if len(runes) != 1 || !unicode.IsLetter(runes[0]) {
			return false
		}
	}
	return true
}
