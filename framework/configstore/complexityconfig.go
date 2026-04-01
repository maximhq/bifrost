package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ComplexityTierBoundaries defines the score thresholds for complexity tier classification.
type ComplexityTierBoundaries struct {
	SimpleMedium     float64 `json:"simple_medium"`
	MediumComplex    float64 `json:"medium_complex"`
	ComplexReasoning float64 `json:"complex_reasoning"`
}

// Validate checks that tier boundaries satisfy the required ordering.
func (b *ComplexityTierBoundaries) Validate() error {
	if b == nil {
		return nil
	}
	if !(0 < b.SimpleMedium &&
		b.SimpleMedium < b.MediumComplex &&
		b.MediumComplex < b.ComplexReasoning &&
		b.ComplexReasoning < 1) {
		return fmt.Errorf(
			"tier boundaries must satisfy 0 < simple_medium (%.4f) < medium_complex (%.4f) < complex_reasoning (%.4f) < 1",
			b.SimpleMedium, b.MediumComplex, b.ComplexReasoning,
		)
	}
	return nil
}

// ComplexityEditableKeywordConfig contains the user-editable keyword lists for
// the complexity analyzer.
type ComplexityEditableKeywordConfig struct {
	CodeKeywords      []string `json:"code_keywords"`
	ReasoningKeywords []string `json:"reasoning_keywords"`
	TechnicalKeywords []string `json:"technical_keywords"`
	SimpleKeywords    []string `json:"simple_keywords"`
}

// ComplexityAnalyzerConfig is the persisted/user-facing runtime configuration
// for the complexity analyzer. It is stored as JSON in governance_config.
type ComplexityAnalyzerConfig struct {
	TierBoundaries ComplexityTierBoundaries        `json:"tier_boundaries"`
	Keywords       ComplexityEditableKeywordConfig `json:"keywords"`
}

// Validate checks that the config is internally consistent.
func (c *ComplexityAnalyzerConfig) Validate() error {
	if c == nil {
		return nil
	}
	if err := c.TierBoundaries.Validate(); err != nil {
		return err
	}
	var missing []string
	if len(c.Keywords.CodeKeywords) == 0 {
		missing = append(missing, "code_keywords")
	}
	if len(c.Keywords.ReasoningKeywords) == 0 {
		missing = append(missing, "reasoning_keywords")
	}
	if len(c.Keywords.TechnicalKeywords) == 0 {
		missing = append(missing, "technical_keywords")
	}
	if len(c.Keywords.SimpleKeywords) == 0 {
		missing = append(missing, "simple_keywords")
	}
	if len(missing) > 0 {
		return fmt.Errorf("keyword lists must be non-empty: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Normalized returns a canonical copy suitable for persistence and runtime use.
func (c *ComplexityAnalyzerConfig) Normalized() ComplexityAnalyzerConfig {
	if c == nil {
		return ComplexityAnalyzerConfig{}
	}
	return ComplexityAnalyzerConfig{
		TierBoundaries: c.TierBoundaries,
		Keywords: ComplexityEditableKeywordConfig{
			CodeKeywords:      normalizeComplexityKeywordList(c.Keywords.CodeKeywords),
			ReasoningKeywords: normalizeComplexityKeywordList(c.Keywords.ReasoningKeywords),
			TechnicalKeywords: normalizeComplexityKeywordList(c.Keywords.TechnicalKeywords),
			SimpleKeywords:    normalizeComplexityKeywordList(c.Keywords.SimpleKeywords),
		},
	}
}

// DecodeComplexityAnalyzerConfig decodes raw JSON into a normalized, validated typed config.
func DecodeComplexityAnalyzerConfig(data []byte) (*ComplexityAnalyzerConfig, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var cfg ComplexityAnalyzerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal complexity analyzer config: %w", err)
	}

	normalized := cfg.Normalized()
	if err := normalized.Validate(); err != nil {
		return nil, fmt.Errorf("invalid complexity analyzer config: %w", err)
	}
	return &normalized, nil
}

// GetComplexityAnalyzerConfig retrieves the typed complexity analyzer config
// from governance_config. Returns nil with no error when the key is absent.
func GetComplexityAnalyzerConfig(ctx context.Context, store ConfigStore) (*ComplexityAnalyzerConfig, error) {
	raw, err := GetComplexityAnalyzerConfigRaw(ctx, store)
	if err != nil {
		return nil, err
	}
	return DecodeComplexityAnalyzerConfig(raw)
}

// UpdateComplexityAnalyzerConfig normalizes, validates, and persists the typed
// complexity analyzer config into governance_config.
func UpdateComplexityAnalyzerConfig(ctx context.Context, store ConfigStore, cfg *ComplexityAnalyzerConfig) error {
	if cfg == nil {
		return fmt.Errorf("complexity analyzer config is nil")
	}

	normalized := cfg.Normalized()
	if err := normalized.Validate(); err != nil {
		return err
	}

	raw, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("failed to marshal complexity analyzer config: %w", err)
	}
	return UpdateComplexityAnalyzerConfigRaw(ctx, store, raw)
}

func normalizeComplexityKeywordList(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}
