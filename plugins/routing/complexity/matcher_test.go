package complexity

import (
	"strings"
	"testing"
	"unicode"
)

func TestCompiledKeywordMatcher_StemmedSingleWordMatchesInflection(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"debug"},
	})

	signals := matcher.analyzeText("We are debugging the handler before release.", lastTextBaseScanMask)
	if signals.codeCount != 1 {
		t.Fatalf("expected stemmed debug keyword to match debugging once, got codeCount=%d", signals.codeCount)
	}
}

func TestCompiledKeywordMatcher_CustomInflectedKeywordMatchesRootForm(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"debugging"},
	})

	signals := matcher.analyzeText("Please debug the handler before release.", lastTextBaseScanMask)
	if signals.codeCount != 1 {
		t.Fatalf("expected custom debugging keyword to match debug once, got codeCount=%d", signals.codeCount)
	}
}

func TestCompiledKeywordMatcher_ExactAndStemmedMatchDoesNotDoubleCount(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"debug"},
	})

	signals := matcher.analyzeText("Please debug the handler before release.", lastTextBaseScanMask)
	if signals.codeCount != 1 {
		t.Fatalf("expected exact debug match not to be double-counted by stemming, got codeCount=%d", signals.codeCount)
	}
}

func TestCompiledKeywordMatcher_RepeatedStemmedFormsDoNotDoubleCount(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"debug"},
	})

	signals := matcher.analyzeText("Please debugged and debugging the handler before release.", lastTextBaseScanMask)
	if signals.codeCount != 1 {
		t.Fatalf("expected repeated stemmed forms of debug to count once, got codeCount=%d", signals.codeCount)
	}
}

func TestCompiledKeywordMatcher_StemDedupePreservesDistinctSameStemKeywords(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords:      []string{"debug"},
		TechnicalKeywords: []string{"debugging"},
	})

	signals := matcher.analyzeText("Please debug the handler before release.", lastTextBaseScanMask)
	if signals.codeCount != 1 {
		t.Fatalf("expected exact debug keyword to contribute code once, got codeCount=%d", signals.codeCount)
	}
	if signals.technicalCount != 1 {
		t.Fatalf("expected distinct same-stem debugging keyword to contribute technical once, got technicalCount=%d", signals.technicalCount)
	}
}

func TestCompiledKeywordMatcher_StemmedPhraseMatchesContiguousVariant(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"analyzing this function"},
	})

	signals := matcher.analyzeText("Please analyze this function before merging.", lastTextBaseScanMask)
	if signals.codeCount != 1 {
		t.Fatalf("expected stemmed phrase to match contiguous variant once, got codeCount=%d", signals.codeCount)
	}
}

func TestCompiledKeywordMatcher_StemmedPhraseDoesNotMatchInsertedWords(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"analyzing this function"},
	})

	signals := matcher.analyzeText("Please analyze this Python function before merging.", lastTextBaseScanMask)
	if signals.codeCount != 0 {
		t.Fatalf("expected stemmed phrase not to match with inserted words, got codeCount=%d", signals.codeCount)
	}
}

func TestCompiledKeywordMatcher_PunctuationKeywordRemainsLiteral(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"ci/cd"},
	})

	literalSignals := matcher.analyzeText("The ci/cd pipeline is failing.", lastTextBaseScanMask)
	if literalSignals.codeCount != 1 {
		t.Fatalf("expected literal ci/cd keyword to match, got codeCount=%d", literalSignals.codeCount)
	}

	tokenSignals := matcher.analyzeText("The ci cd pipeline is failing.", lastTextBaseScanMask)
	if tokenSignals.codeCount != 0 {
		t.Fatalf("expected ci cd not to match literal ci/cd keyword, got codeCount=%d", tokenSignals.codeCount)
	}
}

// TestCompiledKeywordMatcher_StemmedMatchInsideUnsegmentedRun covers a Latin
// keyword whose inflected form sits inside a run of text written without
// spaces. Stem tokens end at unsegmented letters, so the inflection is a token
// of its own and reaches the stemmer, as the exact form already does.
func TestCompiledKeywordMatcher_StemmedMatchInsideUnsegmentedRun(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"debug"},
	})

	tests := []struct {
		name string
		text string
	}{
		{"english", "please debugging this"},
		{"korean with a space", "이 debugging 을 고쳐줘"},
		{"korean with an attached particle", "이 debugging을 고쳐줘"},
		{"japanese, exact form", "このdebugを直して"},
		{"japanese, inflected form", "このdebuggingを直して"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if signals := matcher.analyzeText(tt.text, lastTextBaseScanMask); signals.codeCount != 1 {
				t.Fatalf("expected %q to match the keyword once, got codeCount=%d", tt.text, signals.codeCount)
			}
		})
	}
}

// TestCompiledKeywordMatcher_UnsegmentedKeywordCountsOnce guards the exact and
// stem paths against both reporting the same keyword.
func TestCompiledKeywordMatcher_UnsegmentedKeywordCountsOnce(t *testing.T) {
	tests := []struct {
		name    string
		keyword string
		text    string
	}{
		{"japanese", "コード", "このコードを直して"},
		{"korean", "코드", "이 코드를 고쳐줘"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := newCompiledKeywordMatcher(KeywordConfig{CodeKeywords: []string{tt.keyword}})
			if signals := matcher.analyzeText(tt.text, lastTextBaseScanMask); signals.codeCount != 1 {
				t.Fatalf("expected %q to match %q exactly once, got codeCount=%d", tt.text, tt.keyword, signals.codeCount)
			}
		})
	}
}

// --- Large-text scan path ---
//
// analyzeText switches whole-word keywords from a boundary-aware scan to a
// tokenized presence set once the text reaches wordPresenceSetMinBytes. The
// tests below cover that path, which has to reach the same verdict as the
// scan path for text written in scripts that do not separate words with spaces.

// englishBodyText returns English prose of at least minBytes bytes. It contains
// the default-style keywords "database", "error" and "pipeline".
func englishBodyText(minBytes int) string {
	const paragraph = "The deployment pipeline failed during the nightly run. " +
		"We inspected the error logs and traced the failure to a database " +
		"migration that ran out of order. Please describe what happened. "
	var builder strings.Builder
	for builder.Len() < minBytes {
		builder.WriteString(paragraph)
	}
	return builder.String()
}

// japaneseSentence is a short Japanese question containing the keywords
// "コード" (code), "バグ" (bug) and "設計" (design). None of its words are
// separated by spaces.
const japaneseSentence = "このコードのバグを修正して設計を見直して。"

func TestAnalyzeText_LargeUnsegmentedTextMatchesKeywords(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"コード", "バグ", "設計"},
	})

	var builder strings.Builder
	for builder.Len() < wordPresenceSetMinBytes {
		builder.WriteString(japaneseSentence)
	}
	text := builder.String()
	if len(text) < wordPresenceSetMinBytes {
		t.Fatalf("fixture is %d bytes, need at least %d to exercise the presence-set path", len(text), wordPresenceSetMinBytes)
	}

	signals := matcher.analyzeText(text, lastTextBaseScanMask)
	if signals.codeCount != 3 {
		t.Fatalf("expected all 3 Japanese keywords to match in a %d byte text, got codeCount=%d", len(text), signals.codeCount)
	}
}

func TestAnalyzeText_LargeMixedTextMatchesBothScripts(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"database", "コード"},
	})

	// An English body with a short Japanese question appended is a common
	// shape: a stack trace or log excerpt followed by the user's actual ask.
	text := englishBodyText(wordPresenceSetMinBytes) + japaneseSentence
	if len(text) < wordPresenceSetMinBytes {
		t.Fatalf("fixture is %d bytes, need at least %d to exercise the presence-set path", len(text), wordPresenceSetMinBytes)
	}

	signals := matcher.analyzeText(text, lastTextBaseScanMask)
	if signals.codeCount != 2 {
		t.Fatalf("expected the English and the Japanese keyword to match, got codeCount=%d", signals.codeCount)
	}
}

func TestAnalyzeText_LargeTextMatchesLatinKeywordInsideUnsegmentedRun(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"api"},
	})

	text := englishBodyText(wordPresenceSetMinBytes) + "このapiを直して。"
	signals := matcher.analyzeText(text, lastTextBaseScanMask)
	if signals.codeCount != 1 {
		t.Fatalf("expected the Latin keyword embedded in Japanese text to match, got codeCount=%d", signals.codeCount)
	}
}

func TestAnalyzeText_LargeEnglishTextMatchesSameAsScanPath(t *testing.T) {
	keywords := KeywordConfig{
		CodeKeywords: []string{"database", "pipeline", "error", "migration", "deployment"},
	}
	matcher := newCompiledKeywordMatcher(keywords)

	large := englishBodyText(wordPresenceSetMinBytes)
	if len(large) < wordPresenceSetMinBytes {
		t.Fatalf("fixture is %d bytes, need at least %d to exercise the presence-set path", len(large), wordPresenceSetMinBytes)
	}
	small := "The deployment pipeline failed during the nightly run. " +
		"We inspected the error logs and traced the failure to a database migration."
	if len(small) >= wordPresenceSetMinBytes {
		t.Fatalf("fixture is %d bytes, must stay under %d to exercise the scan path", len(small), wordPresenceSetMinBytes)
	}

	largeSignals := matcher.analyzeText(large, lastTextBaseScanMask)
	smallSignals := matcher.analyzeText(small, lastTextBaseScanMask)

	// Both texts contain each keyword, so the two paths must agree. English is
	// tokenized identically either way; only the lookup strategy differs.
	if largeSignals.codeCount != len(keywords.CodeKeywords) {
		t.Errorf("presence-set path matched %d of %d English keywords", largeSignals.codeCount, len(keywords.CodeKeywords))
	}
	if largeSignals.codeCount != smallSignals.codeCount {
		t.Errorf("presence-set path matched %d keywords, scan path matched %d; the two paths must agree for English", largeSignals.codeCount, smallSignals.codeCount)
	}
}

// TestUnsegmentedScriptLettersAreNonASCII pins the precondition for the ASCII
// fast path in isUnsegmentedLetter. Every script without space-separated words
// lives above U+007F, so the predicate can reject ASCII with one comparison.
func TestUnsegmentedScriptLettersAreNonASCII(t *testing.T) {
	for r := rune(0); r < 0x80; r++ {
		if unicode.Is(unsegmentedScriptLetters, r) || unicode.Is(unicode.Hangul, r) {
			t.Fatalf("U+%04X is ASCII but classified as an unsegmented letter; the fast path in isUnsegmentedLetter is invalid", r)
		}
	}
}

func BenchmarkAnalyzeText_LargeEnglish(b *testing.B) {
	matcher := newCompiledKeywordMatcher(defaultFullKeywordConfig())
	text := englishBodyText(8500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matcher.analyzeText(text, lastTextBaseScanMask)
	}
}

func BenchmarkAnalyzeText_LargeMixed(b *testing.B) {
	matcher := newCompiledKeywordMatcher(defaultFullKeywordConfig())
	text := englishBodyText(8500) + japaneseSentence
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matcher.analyzeText(text, lastTextBaseScanMask)
	}
}
