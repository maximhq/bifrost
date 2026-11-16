package complexity

import "math"

// ComplexityAnalyzer computes complexity scores from normalized text input by
// keyword matching. It holds immutable tierBoundaries and matcher configuration
// after construction, so it is safe for concurrent use.
//
// Nothing on the request path calls it any more: the semantic classifier is the
// only mechanism that publishes a tier (see the compute closure in
// plugins/governance/main.go), and a request semantic cannot serve is recorded
// as "skipped" rather than scored here. Retained because stored configs still
// carry its tier boundaries and keyword lists.
type ComplexityAnalyzer struct {
	tierBoundaries TierBoundaries
	matcher        *compiledKeywordMatcher
}

// NewComplexityAnalyzer creates an analyzer with built-in defaults.
func NewComplexityAnalyzer() *ComplexityAnalyzer {
	return NewComplexityAnalyzerWithConfig(nil)
}

// NewComplexityAnalyzerWithConfig creates an analyzer with runtime config.
func NewComplexityAnalyzerWithConfig(config *AnalyzerConfig) *ComplexityAnalyzer {
	resolved, err := ValidateAndNormalize(config)
	if err != nil || resolved == nil {
		defaults := DefaultAnalyzerConfig()
		resolved = &defaults
	}
	keywords := mergeEditableKeywordsOntoDefaults(resolved.Keywords)
	return &ComplexityAnalyzer{
		tierBoundaries: resolved.TierBoundaries,
		matcher:        newCompiledKeywordMatcher(keywords),
	}
}

// Analyze computes complexity scores from the normalized input.
func (a *ComplexityAnalyzer) Analyze(input ComplexityInput) *ComplexityResult {
	// Extract lexical signals from last user message and system prompt.
	lastSignals := a.matcher.analyzeText(input.LastUserText, lastTextBaseScanMask)
	wordCount := lastSignals.wordCount
	hasPositiveSignal := hasPositiveSignal(lastSignals)
	hasSimpleSignal := lastSignals.simpleCount > 0

	var convScore float64
	if len(input.PriorUserTexts) > 0 {
		convScore = a.scoreConversationContext(input.PriorUserTexts)
	}
	isContinuation := isContinuationFollowup(lastSignals, convScore)
	if !hasPositiveSignal && !hasSimpleSignal && !isContinuation {
		return nil
	}

	systemSignals := textSignalCounts{}
	if hasPositiveSignal {
		systemSignals = a.matcher.analyzeText(input.SystemText, systemTextScanMask)
	}

	// Score primary message signals.
	userMediumScore := scoreCount(lastSignals.mediumCount, mediumMatchSaturation)
	complexScore := scoreCount(lastSignals.complexCount, complexMatchSaturation)
	userSimpleScore := scoreCount(lastSignals.simpleCount, simpleMatchSaturation)
	tokenScore := 0.0
	if hasPositiveSignal || isContinuation {
		tokenScore = scoreTokenCount(wordCount)
	}

	// System prompts provide soft Medium evidence, but never drive the Complex
	// override or token count.
	systemMediumScore := scoreCount(systemSignals.mediumCount, mediumMatchSaturation)
	mediumScore := clamp(userMediumScore+(systemMediumScore*systemPromptAssistFactor), 0.0, 1.0)

	mediumContribution := mediumScore * mediumWeight
	complexContribution := complexScore * complexWeight
	simplePenalty := -(userSimpleScore * simpleWeight)
	tokenContribution := tokenScore * tokenCountWeight

	// Weighted sum for last message.
	lastMsgScore := mediumContribution +
		complexContribution +
		simplePenalty +
		tokenContribution
	lastMsgScore = clamp(lastMsgScore, 0.0, 1.0)

	// Conversation context blending (prior user turns only).
	var blended float64
	if len(input.PriorUserTexts) > 0 && (hasPositiveSignal || isContinuation) {
		lastWeight := defaultLastMessageBlendWeight
		contextWeight := defaultConversationBlendWeight
		if isContinuation {
			lastWeight = referentialLastMessageBlendWeight
			contextWeight = referentialConversationBlendWeight
			if !hasPositiveSignal {
				// The follow-up adds no content of its own ("why?", "fix it"),
				// so blending against it would only dilute the conversation
				// score. The conversation is the request; inherit its score.
				lastWeight = 0.0
				contextWeight = 1.0
			}
		}

		weightedBlend := (lastMsgScore * lastWeight) + (convScore * contextWeight)
		blended = math.Max(lastMsgScore, weightedBlend)
	} else {
		blended = lastMsgScore
	}

	finalScore := clamp(blended, 0.0, 1.0)

	// Complex evidence has explicit product-level guarantees in addition to the
	// numeric score: one match guarantees at least MEDIUM and two force COMPLEX.
	complexCount := lastSignals.complexCount
	tier := a.classifyTier(finalScore)
	if complexCount >= complexOverrideMatchCount {
		tier = TierComplex
	} else if complexCount >= 1 && tier == TierSimple {
		tier = TierMedium
	}

	return &ComplexityResult{
		Score:     finalScore,
		Tier:      tier,
		WordCount: wordCount,
	}
}

func (a *ComplexityAnalyzer) scoreConversationContext(priorUserTexts []string) float64 {
	if len(priorUserTexts) == 0 {
		return 0.0
	}

	texts := priorUserTexts
	if len(texts) > 10 {
		texts = texts[len(texts)-10:]
	}

	var weightedTotal float64
	var totalWeight float64
	lastIdx := len(texts) - 1
	for idx, text := range texts {
		signals := a.matcher.analyzeText(text, contextTextScanMask)
		medium := scoreCount(signals.mediumCount, mediumMatchSaturation)
		complex := scoreCount(signals.complexCount, complexMatchSaturation)
		msgScore := (medium*mediumWeight + complex*complexWeight) /
			(mediumWeight + complexWeight)
		weight := 1.0
		if lastIdx > 0 {
			weight = 1.0 + (2.0 * float64(idx) / float64(lastIdx))
		}
		weightedTotal += msgScore * weight
		totalWeight += weight
	}

	if totalWeight == 0 {
		return 0.0
	}

	return math.Min(1.0, weightedTotal/totalWeight)
}

func hasPositiveSignal(signals textSignalCounts) bool {
	return signals.mediumCount > 0 || signals.complexCount > 0
}

// isContinuationFollowup reports whether the last message should lean on
// conversation context. Two triggers, both gated on the conversation actually
// carrying meaningful complexity context (convScore): an explicit continuation
// phrase, or a message short enough that brevity itself is the signal ("yes
// but make it faster"). The short-message path defers to simple-keyword
// matches so conversation closers like "thanks!" keep classifying as SIMPLE
// instead of inheriting the conversation's complexity.
func isContinuationFollowup(signals textSignalCounts, convScore float64) bool {
	if convScore < referentialMinContextScore {
		return false
	}
	if signals.continuationPhraseCount > 0 {
		return true
	}
	return signals.wordCount > 0 &&
		signals.wordCount <= referentialShortMessageMaxWords &&
		signals.simpleCount == 0
}

func (a *ComplexityAnalyzer) classifyTier(score float64) string {
	switch {
	case score < a.tierBoundaries.SimpleMedium:
		return TierSimple
	case score < a.tierBoundaries.MediumComplex:
		return TierMedium
	default:
		return TierComplex
	}
}
