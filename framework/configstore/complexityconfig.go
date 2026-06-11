package configstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ComplexityTierBoundaries defines score thresholds for complexity tier classification.
type ComplexityTierBoundaries struct {
	SimpleMedium     float64 `json:"simple_medium"`
	MediumComplex    float64 `json:"medium_complex"`
	ComplexReasoning float64 `json:"complex_reasoning"`
}

// Validate checks that tier boundaries are ordered and inside the analyzer score range.
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

// ComplexityEditableKeywordConfig contains the user-editable keyword lists.
type ComplexityEditableKeywordConfig struct {
	CodeKeywords      []string `json:"code_keywords"`
	ReasoningKeywords []string `json:"reasoning_keywords"`
	TechnicalKeywords []string `json:"technical_keywords"`
	SimpleKeywords    []string `json:"simple_keywords"`
}

// ComplexityAnalyzerConfig is the persisted runtime configuration for the complexity analyzer.
type ComplexityAnalyzerConfig struct {
	TierBoundaries ComplexityTierBoundaries        `json:"tier_boundaries"`
	Keywords       ComplexityEditableKeywordConfig `json:"keywords"`
	ConfigHash     string                          `json:"-"`
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
		ConfigHash: c.ConfigHash,
	}
}

// GenerateComplexityAnalyzerConfigHash returns a stable hash for config.json-sourced analyzer config.
func GenerateComplexityAnalyzerConfigHash(config *ComplexityAnalyzerConfig) (string, error) {
	if config == nil {
		return "", fmt.Errorf("complexity analyzer config is nil")
	}

	normalized := config.Normalized()
	normalized.ConfigHash = ""
	if err := normalized.Validate(); err != nil {
		return "", err
	}

	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("failed to marshal complexity analyzer config for hash: %w", err)
	}

	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// MergeComplexityAnalyzerConfig overlays file boundaries and additively merges keyword lists.
func MergeComplexityAnalyzerConfig(base, file *ComplexityAnalyzerConfig) (*ComplexityAnalyzerConfig, error) {
	if file == nil {
		if base == nil {
			return nil, nil
		}
		normalized := base.Normalized()
		if err := normalized.Validate(); err != nil {
			return nil, err
		}
		return &normalized, nil
	}

	normalizedFile := file.Normalized()
	if err := normalizedFile.Validate(); err != nil {
		return nil, err
	}

	var normalizedBase ComplexityAnalyzerConfig
	if base != nil {
		normalizedBase = base.Normalized()
		if err := normalizedBase.Validate(); err != nil {
			return nil, err
		}
	}

	merged := ComplexityAnalyzerConfig{
		TierBoundaries: normalizedFile.TierBoundaries,
		Keywords: ComplexityEditableKeywordConfig{
			CodeKeywords:      mergeComplexityKeywordLists(normalizedBase.Keywords.CodeKeywords, normalizedFile.Keywords.CodeKeywords),
			ReasoningKeywords: mergeComplexityKeywordLists(normalizedBase.Keywords.ReasoningKeywords, normalizedFile.Keywords.ReasoningKeywords),
			TechnicalKeywords: mergeComplexityKeywordLists(normalizedBase.Keywords.TechnicalKeywords, normalizedFile.Keywords.TechnicalKeywords),
			SimpleKeywords:    mergeComplexityKeywordLists(normalizedBase.Keywords.SimpleKeywords, normalizedFile.Keywords.SimpleKeywords),
		},
		ConfigHash: normalizedFile.ConfigHash,
	}
	if err := merged.Validate(); err != nil {
		return nil, err
	}
	return &merged, nil
}

// DecodeComplexityAnalyzerConfig decodes raw JSON into a normalized, validated config.
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
	sort.Strings(out)
	return out
}

func mergeComplexityKeywordLists(base, overlay []string) []string {
	values := make([]string, 0, len(base)+len(overlay))
	values = append(values, base...)
	values = append(values, overlay...)
	return normalizeComplexityKeywordList(values)
}
