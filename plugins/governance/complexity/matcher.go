package complexity

import "strings"

type compiledKeywordMask uint16

const (
	maskCode compiledKeywordMask = 1 << iota
	maskReasoning
	maskStrongReasoning
	maskTechnical
	maskSimple
	maskEnum
	maskComprehensive
	maskElaboration
	maskLimiter
	maskReferentialPhrase
	maskReferentialReference
	maskReferentialAction
	maskTaskShift
)

const (
	lastTextBaseScanMask = maskCode | maskReasoning | maskStrongReasoning | maskTechnical | maskSimple | maskEnum | maskComprehensive | maskElaboration | maskLimiter
	lastTextFullScanMask = lastTextBaseScanMask | maskReferentialPhrase | maskReferentialReference | maskReferentialAction | maskTaskShift
	systemTextScanMask   = maskCode | maskTechnical | maskSimple
	contextTextScanMask  = maskCode | maskReasoning | maskTechnical
)

type keywordMatchMode uint8

const (
	matchModeWholeWord keywordMatchMode = iota
	matchModeBoundarySubstring
	matchModePlainSubstring
)

type compiledKeyword struct {
	text      string
	mask      compiledKeywordMask
	matchMode keywordMatchMode
}

// compiledKeywordMatcher groups keywords by match strategy so request-time
// scans can skip repeated per-keyword boundary-mode decisions.
type compiledKeywordMatcher struct {
	wholeWordKeywords         []compiledKeyword
	boundarySubstringKeywords []compiledKeyword
	plainSubstringKeywords    []compiledKeyword
}

type textSignalCounts struct {
	wordCount                 int
	codeCount                 int
	reasoningCount            int
	strongReasoningCount      int
	technicalCount            int
	simpleCount               int
	enumCount                 int
	comprehensiveCount        int
	elaborationCount          int
	limitingQualifierCount    int
	referentialPhraseCount    int
	referentialReferenceCount int
	referentialActionCount    int
	taskShiftCount            int
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
	addKeywords(keywords.WeakReasoningKeywords, maskReasoning)
	addKeywords(keywords.TechnicalKeywords, maskTechnical)
	addKeywords(keywords.SimpleKeywords, maskSimple)
	addKeywords(keywords.EnumTriggers, maskEnum)
	addKeywords(keywords.ComprehensivenessMarkers, maskComprehensive)
	addKeywords(keywords.ElaborationMarkers, maskElaboration)
	addKeywords(keywords.LimitingQualifiers, maskLimiter)
	addKeywords(keywords.ReferentialPhrases, maskReferentialPhrase)
	addKeywords(keywords.ReferentialReferenceWords, maskReferentialReference)
	addKeywords(keywords.ReferentialActionWords, maskReferentialAction)
	addKeywords(keywords.TaskShiftPhrases, maskTaskShift)

	matcher := &compiledKeywordMatcher{}
	for _, entry := range entries {
		switch entry.matchMode {
		case matchModeWholeWord:
			matcher.wholeWordKeywords = append(matcher.wholeWordKeywords, entry)
		case matchModeBoundarySubstring:
			matcher.boundarySubstringKeywords = append(matcher.boundarySubstringKeywords, entry)
		case matchModePlainSubstring:
			matcher.plainSubstringKeywords = append(matcher.plainSubstringKeywords, entry)
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

	if len(lowerText) >= wordPresenceSetMinBytes {
		wordPresence := buildWordPresenceSet(lowerText)
		for _, keyword := range m.wholeWordKeywords {
			if keyword.mask&scanMask == 0 {
				continue
			}
			if _, ok := wordPresence[keyword.text]; ok {
				signals.addMask(keyword.mask)
			}
		}
	} else {
		for _, keyword := range m.wholeWordKeywords {
			if keyword.mask&scanMask == 0 {
				continue
			}
			if containsWord(lowerText, keyword.text) {
				signals.addMask(keyword.mask)
			}
		}
	}
	for _, keyword := range m.boundarySubstringKeywords {
		if keyword.mask&scanMask == 0 {
			continue
		}
		if containsWord(lowerText, keyword.text) {
			signals.addMask(keyword.mask)
		}
	}
	for _, keyword := range m.plainSubstringKeywords {
		if keyword.mask&scanMask == 0 {
			continue
		}
		if strings.Contains(lowerText, keyword.text) {
			signals.addMask(keyword.mask)
		}
	}

	return signals
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
	if mask&maskEnum != 0 {
		s.enumCount++
	}
	if mask&maskComprehensive != 0 {
		s.comprehensiveCount++
	}
	if mask&maskElaboration != 0 {
		s.elaborationCount++
	}
	if mask&maskLimiter != 0 {
		s.limitingQualifierCount++
	}
	if mask&maskReferentialPhrase != 0 {
		s.referentialPhraseCount++
	}
	if mask&maskReferentialReference != 0 {
		s.referentialReferenceCount++
	}
	if mask&maskReferentialAction != 0 {
		s.referentialActionCount++
	}
	if mask&maskTaskShift != 0 {
		s.taskShiftCount++
	}
}

// buildWordPresenceSet tokenizes large inputs once so whole-word matches become
// set lookups instead of repeated boundary-aware scans.
func buildWordPresenceSet(text string) map[string]struct{} {
	words := make(map[string]struct{}, 64)
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
