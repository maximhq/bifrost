// Package complexity provides request-complexity scoring for governance routing.
package complexity

import "github.com/maximhq/bifrost/framework/configstore"

// ComplexityInput is the normalized input for the analyzer.
// The caller is responsible for extracting text from request payloads.
type ComplexityInput struct {
	LastUserText   string   // last user message text
	PriorUserTexts []string // previous user message texts (up to 10)
	SystemText     string   // concatenated system/developer prompt text
}

// ComplexityResult holds the computed complexity scores and tier classification.
type ComplexityResult struct {
	Score     float64
	Tier      string
	WordCount int
}

const (
	TierSimple  = "SIMPLE"
	TierMedium  = "MEDIUM"
	TierComplex = "COMPLEX"
)

// Routing mechanism values recorded when a routing rule demands a complexity
// tier. They surface in request logs (complexity_mechanism column) so admins can
// see how each routing decision was classified. "skipped" means classification
// was demanded but produced no tier (unsupported input, no signal, or the
// analyzer is disabled). Future classifiers add their own values here
// (e.g. "semantic", "llm").
const (
	MechanismLexical = "lexical"
	MechanismSkipped = "skipped"
)

// Default boundaries are tuned to the 1.00 positive-weight scale in
// keywords.go; retune them together with the dimension weights.
const (
	simpleMediumBoundary  = 0.17
	mediumComplexBoundary = 0.39
)

// TierBoundaries defines the score thresholds for tier classification.
type TierBoundaries = configstore.ComplexityTierBoundaries

// EditableKeywordConfig is the user-facing subset of analyzer keyword lists.
type EditableKeywordConfig = configstore.ComplexityEditableKeywordConfig

// AnalyzerConfig is the runtime configuration for the complexity analyzer.
type AnalyzerConfig = configstore.ComplexityAnalyzerConfig

// KeywordConfig is the full internal keyword set used by the compiled matcher.
type KeywordConfig struct {
	CodeKeywords            []string
	StrongReasoningKeywords []string
	TechnicalKeywords       []string
	SimpleKeywords          []string
	ContinuationPhrases     []string
}

// DefaultTierBoundaries returns the built-in classification thresholds.
func DefaultTierBoundaries() TierBoundaries {
	return TierBoundaries{
		SimpleMedium:  simpleMediumBoundary,
		MediumComplex: mediumComplexBoundary,
	}
}

// DefaultEditableKeywordConfig returns the user-visible default keyword lists.
func DefaultEditableKeywordConfig() EditableKeywordConfig {
	return EditableKeywordConfig{
		CodeKeywords:      cloneStringSlice(codeKeywords),
		ReasoningKeywords: cloneStringSlice(strongReasoningKeywords),
		TechnicalKeywords: cloneStringSlice(technicalKeywords),
		SimpleKeywords:    cloneStringSlice(simpleKeywords),
	}
}

// DefaultAnalyzerConfig returns the built-in analyzer config.
func DefaultAnalyzerConfig() AnalyzerConfig {
	return AnalyzerConfig{
		TierBoundaries: DefaultTierBoundaries(),
		Keywords:       DefaultEditableKeywordConfig(),
	}
}

// ValidateAndNormalize normalizes and validates analyzer config.
func ValidateAndNormalize(cfg *AnalyzerConfig) (*AnalyzerConfig, error) {
	if cfg == nil {
		defaults := DefaultAnalyzerConfig()
		return &defaults, nil
	}
	normalized := cfg.Normalized()
	if err := normalized.Validate(); err != nil {
		return nil, err
	}
	return &normalized, nil
}

func mergeEditableKeywordsOntoDefaults(editable EditableKeywordConfig) KeywordConfig {
	keywords := defaultFullKeywordConfig()
	if len(editable.CodeKeywords) > 0 {
		keywords.CodeKeywords = cloneStringSlice(editable.CodeKeywords)
	}
	if len(editable.ReasoningKeywords) > 0 {
		keywords.StrongReasoningKeywords = cloneStringSlice(editable.ReasoningKeywords)
	}
	if len(editable.TechnicalKeywords) > 0 {
		keywords.TechnicalKeywords = cloneStringSlice(editable.TechnicalKeywords)
	}
	if len(editable.SimpleKeywords) > 0 {
		keywords.SimpleKeywords = cloneStringSlice(editable.SimpleKeywords)
	}
	return keywords
}

func defaultFullKeywordConfig() KeywordConfig {
	return KeywordConfig{
		CodeKeywords:            cloneStringSlice(codeKeywords),
		StrongReasoningKeywords: cloneStringSlice(strongReasoningKeywords),
		TechnicalKeywords:       cloneStringSlice(technicalKeywords),
		SimpleKeywords:          cloneStringSlice(simpleKeywords),
		ContinuationPhrases:     cloneStringSlice(continuationPhrases),
	}
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
