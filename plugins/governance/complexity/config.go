package complexity

import (
	"github.com/maximhq/bifrost/framework/configstore"
)

// ComplexityInput is the normalized input for the analyzer.
// The caller is responsible for extracting text from request payloads.
type ComplexityInput struct {
	LastUserText   string   // last user message text
	PriorUserTexts []string // previous user message texts (up to 10)
	SystemText     string   // concatenated system/developer prompt text
}

// ComplexityContributions captures the weighted contribution of each lexical
// dimension to the last-message score before conversation blending/floors.
type ComplexityContributions struct {
	Code          float64
	Reasoning     float64
	Technical     float64
	SimplePenalty float64
	TokenCount    float64
}

// ComplexityResult holds the computed complexity scores and tier classification.
type ComplexityResult struct {
	// Weighted total score (0.0–1.0)
	Score float64
	// Computed tier: "SIMPLE", "MEDIUM", "COMPLEX", or "REASONING"
	Tier string

	// Individual dimension scores used in the weighted sum (0.0–1.0 each)
	CodePresence       float64
	ReasoningMarkers   float64
	TechnicalTerms     float64
	SimpleIndicators   float64
	TokenCount         float64
	ConversationCtx    float64
	SystemPromptSignal float64 // net weighted contribution from system lexical assist
	OutputComplexity   float64

	// Weighted contributions for the last message score.
	Contributions ComplexityContributions

	// Debug info — match counts per dimension (for logging, not exposed to CEL)
	CodeMatchCount      int
	ReasoningMatchCount int
	TechnicalMatchCount int
	SimpleMatchCount    int
	OutputMatchCount    int

	// Debug internals for eval/logging
	LastMessageScore    float64
	ConversationBlend   float64
	ReferentialFollowup bool
	SimpleWeightApplied float64
	OutputFloorMinScore float64
	OutputFloorApplied  bool
	WordCount           int

	// TierOverrideReason is set when the tier was promoted past what the
	// numeric score alone would classify. Empty when tier came from boundaries.
	// Values: "strong_reasoning_count" (≥2 strong reasoning keywords) or
	// "strong_reasoning_with_signal" (≥1 strong reasoning keyword plus
	// code/technical signal). Used by the routing log so readers can see why
	// a low-score prompt still ended up in REASONING.
	TierOverrideReason string
}

// TierBoundaries defines the score thresholds for tier classification.
type TierBoundaries = configstore.ComplexityTierBoundaries

// DefaultTierBoundaries returns the default tier boundary thresholds.
func DefaultTierBoundaries() TierBoundaries {
	return TierBoundaries{
		SimpleMedium:     0.15,
		MediumComplex:    0.35,
		ComplexReasoning: 0.60,
	}
}

// EditableKeywordConfig is the user-facing subset of keyword lists that can be
// edited through the governance UI and config file. Every other keyword list
// the analyzer uses (weak reasoning, referential phrases, task-shift phrases,
// enum/comprehensiveness/elaboration/limiting markers) is an analyzer internal
// and always resolves from built-in defaults.
//
// ReasoningKeywords maps to the tier-override gate (strongReasoningKeywords),
// because that is what users actually want to control when they say "route
// these phrases to the reasoning model".
type EditableKeywordConfig = configstore.ComplexityEditableKeywordConfig

// KeywordConfig is the full internal keyword configuration used by the
// compiled matcher. It is assembled from EditableKeywordConfig + defaults at
// analyzer-build time and is not part of the persisted or API surface.
type KeywordConfig struct {
	CodeKeywords              []string
	StrongReasoningKeywords   []string
	WeakReasoningKeywords     []string
	TechnicalKeywords         []string
	SimpleKeywords            []string
	EnumTriggers              []string
	ComprehensivenessMarkers  []string
	ElaborationMarkers        []string
	LimitingQualifiers        []string
	ReferentialPhrases        []string
	ReferentialReferenceWords []string
	ReferentialActionWords    []string
	TaskShiftPhrases          []string
}

// AnalyzerConfig is the full runtime configuration for the complexity analyzer.
// Persisted to the governance config store and exchanged over the management
// API in this shape.
type AnalyzerConfig = configstore.ComplexityAnalyzerConfig

// ValidateAndNormalize normalizes and validates an AnalyzerConfig in one step.
// If cfg is nil, returns default config. This is the canonical entrypoint that
// all callers should use instead of open-coding Normalized() + Validate().
func ValidateAndNormalize(cfg *AnalyzerConfig) (*AnalyzerConfig, error) {
	if cfg == nil {
		d := DefaultAnalyzerConfig()
		return &d, nil
	}
	normalized := cfg.Normalized()
	if err := normalized.Validate(); err != nil {
		return nil, err
	}
	return &normalized, nil
}

// DecodeAndValidate decodes raw JSON into an AnalyzerConfig, normalizes, and
// validates it. Returns nil with no error when data is nil or empty (missing
// config). This is the canonical entrypoint for callers reading from the
// config store's raw JSON representation.
func DecodeAndValidate(data []byte) (*AnalyzerConfig, error) {
	return configstore.DecodeComplexityAnalyzerConfig(data)
}

// MergeOntoDefaults overlays the editable keyword lists onto the built-in
// defaults and returns the full internal KeywordConfig used by the compiled
// matcher. Empty editable lists fall through to defaults as a defensive guard;
// Validate() should have already rejected them at the API boundary.
func mergeEditableKeywordsOntoDefaults(e EditableKeywordConfig) KeywordConfig {
	kw := defaultFullKeywordConfig()
	if len(e.CodeKeywords) > 0 {
		kw.CodeKeywords = append([]string(nil), e.CodeKeywords...)
	}
	if len(e.ReasoningKeywords) > 0 {
		kw.StrongReasoningKeywords = append([]string(nil), e.ReasoningKeywords...)
	}
	if len(e.TechnicalKeywords) > 0 {
		kw.TechnicalKeywords = append([]string(nil), e.TechnicalKeywords...)
	}
	if len(e.SimpleKeywords) > 0 {
		kw.SimpleKeywords = append([]string(nil), e.SimpleKeywords...)
	}
	return kw
}

// DefaultEditableKeywordConfig returns the user-visible default keyword lists.
// ReasoningKeywords seeds from strongReasoningKeywords because that is the
// list users are actually editing when they say "reasoning keywords".
func DefaultEditableKeywordConfig() EditableKeywordConfig {
	return EditableKeywordConfig{
		CodeKeywords:      cloneStringSlice(codeKeywords),
		ReasoningKeywords: cloneStringSlice(strongReasoningKeywords),
		TechnicalKeywords: cloneStringSlice(technicalKeywords),
		SimpleKeywords:    cloneStringSlice(simpleKeywords),
	}
}

// DefaultAnalyzerConfig returns the default thresholds and user-visible
// keyword lists.
func DefaultAnalyzerConfig() AnalyzerConfig {
	return AnalyzerConfig{
		TierBoundaries: DefaultTierBoundaries(),
		Keywords:       DefaultEditableKeywordConfig(),
	}
}

// defaultFullKeywordConfig returns the complete internal keyword set, with all
// 13 lists populated from the package-level defaults. Used by the matcher and
// by MergeOntoDefaults as the base that editable lists overlay onto.
func defaultFullKeywordConfig() KeywordConfig {
	return KeywordConfig{
		CodeKeywords:              cloneStringSlice(codeKeywords),
		StrongReasoningKeywords:   cloneStringSlice(strongReasoningKeywords),
		WeakReasoningKeywords:     cloneStringSlice(weakReasoningKeywords),
		TechnicalKeywords:         cloneStringSlice(technicalKeywords),
		SimpleKeywords:            cloneStringSlice(simpleKeywords),
		EnumTriggers:              cloneStringSlice(enumTriggers),
		ComprehensivenessMarkers:  cloneStringSlice(comprehensivenessMarkers),
		ElaborationMarkers:        cloneStringSlice(elaborationMarkers),
		LimitingQualifiers:        cloneStringSlice(limitingQualifiers),
		ReferentialPhrases:        cloneStringSlice(referentialPhrases),
		ReferentialReferenceWords: cloneStringSlice(referentialReferenceWords),
		ReferentialActionWords:    cloneStringSlice(referentialActionWords),
		TaskShiftPhrases:          cloneStringSlice(taskShiftPhrases),
	}
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
