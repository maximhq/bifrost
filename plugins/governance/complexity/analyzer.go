package complexity

import "math"

// ComplexityAnalyzer computes complexity scores from normalized text input.
// It holds immutable tierBoundaries and matcher configuration after construction,
// so it is safe for concurrent use.
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
	userCodeScore := scoreCount(lastSignals.codeCount, 3)
	reasoningScore := scoreCount(lastSignals.reasoningCount, 2)
	userTechnicalScore := scoreCount(lastSignals.technicalCount, 3)
	userSimpleScore := scoreCount(lastSignals.simpleCount, 2)
	tokenScore := 0.0
	if hasPositiveSignal || isContinuation {
		tokenScore = scoreTokenCount(wordCount)
	}

	// System prompt provides soft lexical context for code/technical signals,
	// but never drives reasoning override or token count.
	systemCodeScore := scoreCount(systemSignals.codeCount, 3)
	systemTechnicalScore := scoreCount(systemSignals.technicalCount, 3)

	codeScore := clamp(userCodeScore+(systemCodeScore*systemPromptAssistFactor), 0.0, 1.0)
	technicalScore := clamp(userTechnicalScore+(systemTechnicalScore*systemPromptAssistFactor), 0.0, 1.0)

	codeContribution := codeScore * codeWeight
	reasoningContribution := reasoningScore * reasoningWeight
	technicalContribution := technicalScore * technicalWeight
	simplePenalty := -(userSimpleScore * simpleWeight)
	tokenContribution := tokenScore * tokenCountWeight

	// Weighted sum for last message.
	lastMsgScore := codeContribution +
		reasoningContribution +
		technicalContribution +
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

	// Tier classification with reasoning override: strong reasoning signals
	// promote the request to COMPLEX even when the blended score falls below
	// the medium/complex boundary.
	strongCount := lastSignals.strongReasoningCount
	tier := a.classifyTier(finalScore)
	if strongCount >= 2 {
		tier = TierComplex
	} else if strongCount >= 1 && (userCodeScore > 0.5 || userTechnicalScore > 0.5) {
		tier = TierComplex
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
		code := scoreCount(signals.codeCount, 3)
		tech := scoreCount(signals.technicalCount, 3)
		reasoning := scoreCount(signals.reasoningCount, 2)
		msgScore := (code*codeWeight + tech*technicalWeight + reasoning*reasoningWeight) /
			(codeWeight + technicalWeight + reasoningWeight)
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
	return signals.codeCount > 0 || signals.reasoningCount > 0 || signals.technicalCount > 0
}

// isContinuationFollowup reports whether the last message should lean on
// conversation context. Two triggers, both gated on the conversation actually
// carrying meaningful technical context (convScore): an explicit continuation
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
