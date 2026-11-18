package suggest

import (
	"regexp"
	"strings"
)

// A phrase bank entry is configuration: everyone's requests are matched
// against it and anyone with config access can read it. A phrase that keeps
// one customer's order number, email or pasted document matches that customer
// and nobody else, and copies their data into config. These checks keep that
// material out of suggested phrases.

// maxPhraseWords bounds a suggested phrase. Phrases are meant to be short and
// prototypical; a longer one is almost always a pasted request.
const maxPhraseWords = 20

// minPhraseWords is the shortest phrase sanitize will keep: below it, what is
// left after removing the specifics no longer says what was asked. Two words
// is enough for an instruction like "summarise this".
const minPhraseWords = 2

// pastedTailWords is how many words after a colon mark the rest of a phrase as
// pasted content ("summarise this: <article>").
const pastedTailWords = 4

var (
	// leakToken matches identifiers and amounts: 3+ digit runs (order and
	// ticket numbers, years), dates, times and decimals, money, percentages,
	// emails, URLs and domains. Short digits inside vocabulary ("3-statement",
	// "k8s", "a/b test") are kept on purpose: they are how people name the work.
	leakToken = regexp.MustCompile(`\d{3,}|\d+[./:]\d+|[$€£¥]\s*\d|\d+\s*%|\S+@\S+|https?://\S+|\b\S+\.(com|io|org|net|dev|ai)\b`)
	// leakMarks matches quoting and code punctuation, the sign of pasted text.
	leakMarks = regexp.MustCompile("[\"“”`{}<>\\[\\]|]")
)

// pastedTail reports whether a phrase carries pasted content after an
// instruction ending in a colon, and returns the instruction before it.
func pastedTail(p string) (head string, ok bool) {
	i := strings.IndexByte(p, ':')
	if i < 0 {
		return "", false
	}
	head = strings.TrimSpace(p[:i])
	if len(strings.Fields(head)) == 0 || len(strings.Fields(p[i+1:])) < pastedTailWords {
		return "", false
	}
	return head, true
}

// leaks reports whether a phrase still carries request-specific material.
func leaks(p string) bool {
	if strings.ContainsAny(p, "\n\r") || leakToken.MatchString(p) || leakMarks.MatchString(p) {
		return true
	}
	if _, ok := pastedTail(p); ok {
		return true
	}
	return len(strings.Fields(p)) > maxPhraseWords
}

// filterLeaks splits phrases into those safe to suggest and those that leak.
func filterLeaks(phrases []string) (kept, dropped []string) {
	for _, p := range phrases {
		if leaks(p) {
			dropped = append(dropped, p)
			continue
		}
		kept = append(kept, p)
	}
	return kept, dropped
}

// sanitize is the deterministic fallback when the LLM keeps leaking: cut
// pasted content after a colon, drop every word carrying an identifier, strip
// quote and code marks, and cap the length. It returns "" when too little is
// left to say what was asked.
func sanitize(p string) string {
	p = strings.Join(strings.Fields(p), " ")
	if head, ok := pastedTail(p); ok {
		p = head
	}
	var words []string
	for _, w := range strings.Fields(p) {
		if leakToken.MatchString(w) {
			continue
		}
		if w = leakMarks.ReplaceAllString(w, ""); w != "" {
			words = append(words, w)
		}
	}
	if len(words) < minPhraseWords {
		return ""
	}
	return strings.Join(words[:min(len(words), maxPhraseWords)], " ")
}

// normalizePhrase matches how the config store compares phrases: lowercase,
// whitespace collapsed.
func normalizePhrase(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
