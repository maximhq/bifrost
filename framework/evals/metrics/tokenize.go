package metrics

import (
	"regexp"
	"strings"
)

// Tokenize13a splits text the way mteval-v13a does, as sacrebleu's "13a" tokenizer
// implements it. BLEU scores are only comparable when tokenized this way.
func Tokenize13a(text string) []string {
	text = strings.ReplaceAll(text, "<skipped>", "")
	text = strings.ReplaceAll(text, "-\n", "")
	text = strings.ReplaceAll(text, "\n", " ")
	if strings.Contains(text, "&") {
		text = strings.ReplaceAll(text, "&quot;", `"`)
		text = strings.ReplaceAll(text, "&amp;", "&")
		text = strings.ReplaceAll(text, "&lt;", "<")
		text = strings.ReplaceAll(text, "&gt;", ">")
	}
	text = " " + text + " "
	for _, rule := range tokenizer13aRules {
		text = rule.pattern.ReplaceAllString(text, rule.replacement)
	}
	return strings.Fields(text)
}

var tokenizer13aRules = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	// Punctuation and symbols become their own tokens.
	{regexp.MustCompile(`([\{-\~\[-\x60 -\&\(-\+\:-\@\/])`), " $1 "},
	// A period or comma is split off unless a digit comes before it...
	{regexp.MustCompile(`([^0-9])([\.,])`), "$1 $2 "},
	// ...or after it.
	{regexp.MustCompile(`([\.,])([^0-9])`), " $1 $2"},
	// A dash is split off when a digit comes before it.
	{regexp.MustCompile(`([0-9])(-)`), "$1 $2 "},
}

var rougeNonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// TokenizeRouge lowercases text, keeps runs of ASCII letters and digits, and stems
// tokens longer than three characters, as rouge-score does with its stemmer on.
func TokenizeRouge(text string) []string {
	text = rougeNonAlphanumeric.ReplaceAllString(strings.ToLower(text), " ")
	fields := strings.Fields(text)
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) > 3 {
			field = PorterStem(field)
		}
		if field != "" {
			tokens = append(tokens, field)
		}
	}
	return tokens
}

// TokenizeWords splits text into words and punctuation in the style of the Penn
// Treebank tokenizer that NLTK's word_tokenize is built on. METEOR is computed over
// these tokens. It covers the common rules (punctuation, quotes, brackets and English
// contractions) and is not a full port of the Treebank rule set.
func TokenizeWords(text string) []string {
	for _, rule := range treebankRules {
		text = rule.pattern.ReplaceAllString(text, rule.replacement)
	}
	// The closing-quote and contraction rules look for a following space, so the text is
	// padded before they run.
	text = " " + text + " "
	for _, rule := range treebankClosingRules {
		text = rule.pattern.ReplaceAllString(text, rule.replacement)
	}
	return strings.Fields(text)
}

type tokenRule struct {
	pattern     *regexp.Regexp
	replacement string
}

var treebankRules = []tokenRule{
	// Opening quotes.
	{regexp.MustCompile(`^"`), "`` "},
	{regexp.MustCompile("(``)"), " $1 "},
	{regexp.MustCompile(`([ \(\[{<])("|'{2})`), "$1 `` "},
	// Punctuation.
	{regexp.MustCompile(`([:,])([^\d])`), " $1 $2"},
	{regexp.MustCompile(`([:,])$`), " $1 "},
	{regexp.MustCompile(`\.\.\.`), " ... "},
	{regexp.MustCompile(`[;@#$%&]`), " $0 "},
	// A period is split off only at the end of the text, with any closing quotes or brackets.
	{regexp.MustCompile(`([^\.])(\.)([\]\)}>"']*)\s*$`), "$1 $2$3 "},
	{regexp.MustCompile(`[?!]`), " $0 "},
	{regexp.MustCompile(`([^'])' `), "$1 ' "},
	// Brackets.
	{regexp.MustCompile(`[\]\[\(\)\{\}<>]`), " $0 "},
	{regexp.MustCompile(`--`), " -- "},
}

var treebankClosingRules = []tokenRule{
	// Closing quotes.
	{regexp.MustCompile(`''`), " '' "},
	{regexp.MustCompile(`"`), " '' "},
	{regexp.MustCompile(`([^' ])('[sS]|'[mM]|'[dD]|') `), "$1 $2 "},
	{regexp.MustCompile(`([^' ])('ll|'LL|'re|'RE|'ve|'VE|n't|N'T) `), "$1 $2 "},
}
