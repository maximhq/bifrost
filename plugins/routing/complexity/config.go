// Package complexity provides request-complexity scoring for governance routing.
package complexity

import (
	"strings"

	"github.com/maximhq/bifrost/framework/configstore"
)

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
// (e.g. "llm").
//
// "lexical" is not here: the keyword scorer no longer publishes a tier, so
// nothing writes that value. It never reached a log either — the
// complexity_mechanism column ships alongside the semantic classifier — so
// there are no historical rows carrying it and nothing offers it as a filter.
const (
	MechanismSemantic = "semantic"
	MechanismSkipped  = "skipped"
)

// Default boundaries are retained for the dormant lexical analyzer and its
// tests. Semantic classification does not read them.
const (
	simpleMediumBoundary  = configstore.DefaultComplexitySimpleMediumBoundary
	mediumComplexBoundary = configstore.DefaultComplexityMediumComplexBoundary
)

// TierBoundaries contains deprecated lexical score thresholds.
type TierBoundaries = configstore.ComplexityTierBoundaries

// EditableKeywordConfig is the user-facing subset of analyzer keyword lists.
type EditableKeywordConfig = configstore.ComplexityEditableKeywordConfig

// SemanticConfig is the embedding-based classifier configuration. Its
// exemplars are the shared per-tier keyword lists in EditableKeywordConfig.
type SemanticConfig = configstore.ComplexitySemanticConfig

// AnalyzerConfig is the runtime configuration for the complexity analyzer.
type AnalyzerConfig = configstore.ComplexityAnalyzerConfig

// KeywordConfig is the full internal keyword set used by the compiled matcher.
type KeywordConfig struct {
	MediumKeywords      []string
	ComplexKeywords     []string
	SimpleKeywords      []string
	ContinuationPhrases []string
}

// DefaultTierBoundaries returns the deprecated lexical thresholds retained for
// config compatibility.
func DefaultTierBoundaries() TierBoundaries {
	return configstore.DefaultComplexityTierBoundaries()
}

// DefaultEditableKeywordConfig returns the user-visible default phrase lists.
//
// These are the reference phrases the semantic classifier embeds, and they are
// the only per-tier lists an administrator sees. The lexical matcher's built-in
// keyword vocabulary is deliberately left out: single-word scoring signals like
// "refactor" make poor reference phrases, and the lexical classifier is no
// longer user-facing. It still gets those keywords from
// defaultFullKeywordConfig when a tier list is empty.
func DefaultEditableKeywordConfig() EditableKeywordConfig {
	exemplars := DefaultComplexityExemplars()
	return EditableKeywordConfig{
		SimpleKeywords:  cloneStringSlice(exemplars.SimpleKeywords),
		MediumKeywords:  cloneStringSlice(exemplars.MediumKeywords),
		ComplexKeywords: cloneStringSlice(exemplars.ComplexKeywords),
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

// mergeEditableKeywordsOntoDefaults layers the administrator's per-tier phrases
// on top of the matcher's built-in vocabulary.
//
// The editable lists no longer carry that vocabulary; they are semantic
// reference phrases, so an edited list adds to the built-ins instead of
// replacing them. Replacing would leave the lexical scorer holding only
// sentence-length exemplars, which its substring matching almost never hits,
// reducing every request to a length-only score. That scorer no longer
// publishes a tier, so this keeps a dormant path coherent rather than
// protecting live routing.
func mergeEditableKeywordsOntoDefaults(editable EditableKeywordConfig) KeywordConfig {
	keywords := defaultFullKeywordConfig()
	keywords.SimpleKeywords = sharedTierDefaults(keywords.SimpleKeywords, editable.SimpleKeywords)
	keywords.MediumKeywords = sharedTierDefaults(keywords.MediumKeywords, editable.MediumKeywords)
	keywords.ComplexKeywords = sharedTierDefaults(keywords.ComplexKeywords, editable.ComplexKeywords)
	return keywords
}

func defaultFullKeywordConfig() KeywordConfig {
	return KeywordConfig{
		MediumKeywords:      cloneStringSlice(mediumKeywords),
		ComplexKeywords:     cloneStringSlice(complexKeywords),
		SimpleKeywords:      cloneStringSlice(simpleKeywords),
		ContinuationPhrases: cloneStringSlice(continuationPhrases),
	}
}

// sharedTierDefaults appends extra phrases to a tier's built-in keywords,
// skipping entries the tier already carries.
func sharedTierDefaults(keywords, extra []string) []string {
	if len(extra) == 0 {
		return keywords
	}
	seen := make(map[string]struct{}, len(keywords)+len(extra))
	for _, keyword := range keywords {
		seen[strings.ToLower(strings.TrimSpace(keyword))] = struct{}{}
	}
	combined := make([]string, 0, len(keywords)+len(extra))
	combined = append(combined, keywords...)
	for _, phrase := range extra {
		key := strings.ToLower(strings.TrimSpace(phrase))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		combined = append(combined, phrase)
	}
	return combined
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
