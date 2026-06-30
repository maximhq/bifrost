package complexity

import (
	"strings"

	"github.com/blevesearch/go-porterstemmer"
)

type compiledKeywordMask uint16

const (
	maskCode compiledKeywordMask = 1 << iota
	maskReasoning
	maskStrongReasoning
	maskTechnical
	maskSimple
	maskContinuation
)

const (
	lastTextBaseScanMask = maskCode | maskReasoning | maskStrongReasoning | maskTechnical | maskSimple | maskContinuation
	systemTextScanMask   = maskCode | maskTechnical
	contextTextScanMask  = maskCode | maskReasoning | maskTechnical
)

const (
	// matchedKeywordStackPrealloc keeps the common dedupe path allocation-free.
	// It is not a match limit; the slice grows if a prompt matches more keywords.
	matchedKeywordStackPrealloc = 16

	// Stems are built from word tokens, so NUL cannot appear in a token. This
	// keeps compiled phrase keys unambiguous without escaping.
	stemmedKeywordKeySeparator = "\x00"
)

type keywordMatchMode uint8

const (
	matchModeWholeWord keywordMatchMode = iota
	matchModeBoundarySubstring
	matchModePlainSubstring
)

type compiledKeyword struct {
	id        int
	text      string
	mask      compiledKeywordMask
	matchMode keywordMatchMode
}

type compiledStemmedKeyword struct {
	matches []compiledStemmedKeywordMatch
	stems   []string
	mask    compiledKeywordMask
}

// compiledStemmedKeywordMatch keeps the original keyword identity behind a
// stem sequence. Multiple configured keywords can collapse to the same stems
// but still need separate dedupe and scoring masks.
type compiledStemmedKeywordMatch struct {
	id   int
	mask compiledKeywordMask
}

// compiledKeywordMatcher groups keywords by match strategy so request-time
// scans can skip repeated per-keyword boundary-mode decisions.
type compiledKeywordMatcher struct {
	wholeWordKeywords         []compiledKeyword
	boundarySubstringKeywords []compiledKeyword
	plainSubstringKeywords    []compiledKeyword
	stemmedKeywordIndex       map[string][]compiledStemmedKeyword
}

type textSignalCounts struct {
	wordCount               int
	codeCount               int
	reasoningCount          int
	strongReasoningCount    int
	technicalCount          int
	simpleCount             int
	continuationPhraseCount int
}

func newCompiledKeywordMatcher(keywords KeywordConfig) *compiledKeywordMatcher {
	entries := make(map[string]compiledKeyword)
	addKeywords := func(keywords []string, mask compiledKeywordMask) {
		for _, kw := range keywords {
			text := strings.TrimSpace(strings.ToLower(kw))
			if text == "" {
				continue
			}
			entry, ok := entries[text]
			if !ok {
				entry = compiledKeyword{
					id:        len(entries),
					text:      text,
					mask:      mask,
					matchMode: keywordMatchModeFor(text),
				}
			} else {
				entry.mask |= mask
			}
			entries[text] = entry
		}
	}

	addKeywords(keywords.CodeKeywords, maskCode)
	addKeywords(keywords.StrongReasoningKeywords, maskReasoning|maskStrongReasoning)
	addKeywords(keywords.TechnicalKeywords, maskTechnical)
	addKeywords(keywords.SimpleKeywords, maskSimple)
	addKeywords(keywords.ContinuationPhrases, maskContinuation)

	matcher := &compiledKeywordMatcher{}
	var stemmedKeywords []compiledStemmedKeyword
	stemmedByKey := make(map[string]int)
	for _, entry := range entries {
		switch entry.matchMode {
		case matchModeWholeWord:
			matcher.wholeWordKeywords = append(matcher.wholeWordKeywords, entry)
		case matchModeBoundarySubstring:
			matcher.boundarySubstringKeywords = append(matcher.boundarySubstringKeywords, entry)
		case matchModePlainSubstring:
			matcher.plainSubstringKeywords = append(matcher.plainSubstringKeywords, entry)
		}

		// Stem matching is additive to the exact matcher above. Only normal
		// word-token keywords and phrases are indexed; punctuation-heavy terms
		// such as "ci/cd" stay on the literal matching path.
		if stems, ok := stemKeywordTokens(entry.text); ok {
			key := strings.Join(stems, stemmedKeywordKeySeparator)
			if idx, exists := stemmedByKey[key]; exists {
				stemmedKeywords[idx].matches = append(stemmedKeywords[idx].matches, compiledStemmedKeywordMatch{
					id:   entry.id,
					mask: entry.mask,
				})
				stemmedKeywords[idx].mask |= entry.mask
				continue
			}
			stemmedByKey[key] = len(stemmedKeywords)
			stemmedKeywords = append(stemmedKeywords, compiledStemmedKeyword{
				matches: []compiledStemmedKeywordMatch{{
					id:   entry.id,
					mask: entry.mask,
				}},
				stems: stems,
				mask:  entry.mask,
			})
		}
	}
	if len(stemmedKeywords) > 0 {
		matcher.stemmedKeywordIndex = make(map[string][]compiledStemmedKeyword, len(stemmedKeywords))
		for _, keyword := range stemmedKeywords {
			// Index by the first stem so request-time matching checks only
			// candidates that can start at the current request token.
			matcher.stemmedKeywordIndex[keyword.stems[0]] = append(matcher.stemmedKeywordIndex[keyword.stems[0]], keyword)
		}
	}
	return matcher
}

func keywordMatchModeFor(keyword string) keywordMatchMode {
	if strings.Contains(keyword, " ") {
		return matchModePlainSubstring
	}
	for _, r := range keyword {
		if !isWordChar(r) {
			return matchModeBoundarySubstring
		}
	}
	return matchModeWholeWord
}

// analyzeText lowercases once, then takes a cheaper whole-word lookup path for
// larger texts where a single tokenization pass beats repeated boundary scans.
func (m *compiledKeywordMatcher) analyzeText(text string, scanMask compiledKeywordMask) textSignalCounts {
	if text == "" {
		return textSignalCounts{}
	}

	lowerText := strings.ToLower(text)
	signals := textSignalCounts{
		wordCount: countWordsNoAlloc(text),
	}
	var matchedKeywordIDs [matchedKeywordStackPrealloc]int
	matchedIDs := matchedKeywordIDs[:0]
	recordMatch := func(keyword compiledKeyword) {
		signals.addMask(keyword.mask)
		matchedIDs = append(matchedIDs, keyword.id)
	}

	if len(lowerText) >= wordPresenceSetMinBytes {
		wordPresence := buildWordPresenceSet(lowerText, signals.wordCount)
		for _, keyword := range m.wholeWordKeywords {
			if keyword.mask&scanMask == 0 {
				continue
			}
			if _, ok := wordPresence[keyword.text]; ok {
				recordMatch(keyword)
			}
		}
	} else {
		for _, keyword := range m.wholeWordKeywords {
			if keyword.mask&scanMask == 0 {
				continue
			}
			if containsWord(lowerText, keyword.text) {
				recordMatch(keyword)
			}
		}
	}
	for _, keyword := range m.boundarySubstringKeywords {
		if keyword.mask&scanMask == 0 {
			continue
		}
		if containsWord(lowerText, keyword.text) {
			recordMatch(keyword)
		}
	}
	for _, keyword := range m.plainSubstringKeywords {
		if keyword.mask&scanMask == 0 {
			continue
		}
		if strings.Contains(lowerText, keyword.text) {
			recordMatch(keyword)
		}
	}
	m.addStemmedMatches(lowerText, scanMask, matchedIDs, &signals)

	return signals
}

func (m *compiledKeywordMatcher) addStemmedMatches(lowerText string, scanMask compiledKeywordMask, exactMatchedIDs []int, signals *textSignalCounts) {
	if len(m.stemmedKeywordIndex) == 0 {
		return
	}

	requestStems := stemTextTokens(lowerText, signals.wordCount)
	if len(requestStems) == 0 {
		return
	}

	var stemMatchedKeywordIDs [matchedKeywordStackPrealloc]int
	stemMatchedIDs := stemMatchedKeywordIDs[:0]
	for idx, stem := range requestStems {
		for _, keyword := range m.stemmedKeywordIndex[stem] {
			if keyword.mask&scanMask == 0 {
				continue
			}
			if stemSequenceMatchesAt(requestStems, idx, keyword.stems) {
				unmatchedMask := compiledKeywordMask(0)
				for _, match := range keyword.matches {
					if match.mask&scanMask == 0 {
						continue
					}
					if keywordIDMatched(match.id, exactMatchedIDs) || keywordIDMatched(match.id, stemMatchedIDs) {
						continue
					}
					// Exact matches win first; the stem path only contributes
					// configured keyword IDs that have not already matched.
					unmatchedMask |= match.mask
					stemMatchedIDs = append(stemMatchedIDs, match.id)
				}
				if unmatchedMask != 0 {
					signals.addMask(unmatchedMask)
				}
			}
		}
	}
}

// addMask increments every scoring bucket a matched keyword contributes to.
func (s *textSignalCounts) addMask(mask compiledKeywordMask) {
	if mask&maskCode != 0 {
		s.codeCount++
	}
	if mask&maskReasoning != 0 {
		s.reasoningCount++
	}
	if mask&maskStrongReasoning != 0 {
		s.strongReasoningCount++
	}
	if mask&maskTechnical != 0 {
		s.technicalCount++
	}
	if mask&maskSimple != 0 {
		s.simpleCount++
	}
	if mask&maskContinuation != 0 {
		s.continuationPhraseCount++
	}
}

// buildWordPresenceSet tokenizes large inputs once so whole-word matches become
// set lookups instead of repeated boundary-aware scans.
func buildWordPresenceSet(text string, capacityHint int) map[string]struct{} {
	words := make(map[string]struct{}, capacityHint)
	start := -1
	for i, r := range text {
		if isWordChar(r) {
			if start == -1 {
				start = i
			}
			continue
		}
		if start != -1 {
			words[text[start:i]] = struct{}{}
			start = -1
		}
	}
	if start != -1 {
		words[text[start:]] = struct{}{}
	}
	return words
}

func stemKeywordTokens(keyword string) ([]string, bool) {
	tokens, ok := tokenizeStemEligibleText(keyword)
	if !ok {
		return nil, false
	}
	return stemTokens(tokens), true
}

// stemTextTokens tokenizes request text with the same word-character rules used
// by exact whole-word matching, then stems those tokens for the additive pass.
func stemTextTokens(text string, capacityHint int) []string {
	tokens := tokenizeWordText(text, capacityHint)
	if len(tokens) == 0 {
		return nil
	}
	return stemTokens(tokens)
}

// stemTokens receives already-lowercased tokens. StemWithoutLowerCasing avoids
// redoing lowercase work that analyzeText has already performed.
func stemTokens(tokens []string) []string {
	stems := make([]string, 0, len(tokens))
	var runeBuffer []rune
	for _, token := range tokens {
		stem := token
		if hasMoreThanTwoRunes(token) {
			runeBuffer = runeBuffer[:0]
			for _, r := range token {
				runeBuffer = append(runeBuffer, r)
			}
			stemRunes := porterstemmer.StemWithoutLowerCasing(runeBuffer)
			if !runesEqualString(stemRunes, token) {
				stem = string(stemRunes)
			}
		}
		if stem == "" {
			continue
		}
		stems = append(stems, stem)
	}
	return stems
}

func hasMoreThanTwoRunes(text string) bool {
	count := 0
	for range text {
		count++
		if count > 2 {
			return true
		}
	}
	return false
}

// tokenizeStemEligibleText accepts only space-separated word tokens. This keeps
// symbol/punctuation terms on the existing literal matcher instead of inventing
// surprising stemmed behavior for terms like "ci/cd".
func tokenizeStemEligibleText(text string) ([]string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil, false
	}
	for _, field := range fields {
		for _, r := range field {
			if !isWordChar(r) {
				return nil, false
			}
		}
	}
	return fields, true
}

// tokenizeWordText extracts the request tokens used for stem matching. The
// capacity hint is the word count already computed for scoring.
func tokenizeWordText(text string, capacityHint int) []string {
	tokens := make([]string, 0, capacityHint)
	start := -1
	for i, r := range text {
		if isWordChar(r) {
			if start == -1 {
				start = i
			}
			continue
		}
		if start != -1 {
			tokens = append(tokens, text[start:i])
			start = -1
		}
	}
	if start != -1 {
		tokens = append(tokens, text[start:])
	}
	return tokens
}

func stemSequenceMatchesAt(tokens []string, start int, sequence []string) bool {
	if len(sequence) == 0 || start+len(sequence) > len(tokens) {
		return false
	}
	for idx, stem := range sequence {
		if tokens[start+idx] != stem {
			return false
		}
	}
	return true
}

func runesEqualString(runes []rune, text string) bool {
	idx := 0
	for _, r := range text {
		if idx >= len(runes) || runes[idx] != r {
			return false
		}
		idx++
	}
	return idx == len(runes)
}

func keywordIDMatched(id int, matchedIDs []int) bool {
	for _, matchedID := range matchedIDs {
		if id == matchedID {
			return true
		}
	}
	return false
}
