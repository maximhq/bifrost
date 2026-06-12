package configstore

import (
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

// ComplexityAnalyzerConfigHashes tracks the config.json hash for each editable
// analyzer section. It is persisted with the config row, but not exposed through
// API responses or config.json.
type ComplexityAnalyzerConfigHashes struct {
	TierBoundaries    string `json:"tier_boundaries,omitempty"`
	CodeKeywords      string `json:"code_keywords,omitempty"`
	ReasoningKeywords string `json:"reasoning_keywords,omitempty"`
	TechnicalKeywords string `json:"technical_keywords,omitempty"`
	SimpleKeywords    string `json:"simple_keywords,omitempty"`
}

// Empty reports whether no file-backed section hashes are present.
func (h ComplexityAnalyzerConfigHashes) Empty() bool {
	return h.TierBoundaries == "" &&
		h.CodeKeywords == "" &&
		h.ReasoningKeywords == "" &&
		h.TechnicalKeywords == "" &&
		h.SimpleKeywords == ""
}

// Equal reports whether all section hashes match.
func (h ComplexityAnalyzerConfigHashes) Equal(other ComplexityAnalyzerConfigHashes) bool {
	return h.TierBoundaries == other.TierBoundaries &&
		h.CodeKeywords == other.CodeKeywords &&
		h.ReasoningKeywords == other.ReasoningKeywords &&
		h.TechnicalKeywords == other.TechnicalKeywords &&
		h.SimpleKeywords == other.SimpleKeywords
}

// ComplexityAnalyzerConfig is the persisted runtime configuration for the complexity analyzer.
type ComplexityAnalyzerConfig struct {
	TierBoundaries ComplexityTierBoundaries        `json:"tier_boundaries"`
	Keywords       ComplexityEditableKeywordConfig `json:"keywords"`
	ConfigHashes   ComplexityAnalyzerConfigHashes  `json:"-"`
}

type complexityAnalyzerConfigRecord struct {
	TierBoundaries ComplexityTierBoundaries        `json:"tier_boundaries"`
	Keywords       ComplexityEditableKeywordConfig `json:"keywords"`
	ConfigHashes   ComplexityAnalyzerConfigHashes  `json:"_config_hashes,omitempty"`
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
		ConfigHashes: c.ConfigHashes,
	}
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
		ConfigHashes: normalizedFile.ConfigHashes,
	}
	if err := merged.Validate(); err != nil {
		return nil, err
	}
	return &merged, nil
}

// MergeComplexityAnalyzerConfigByHashes overlays only file-backed sections whose
// config.json hash changed. Keyword sections are additive; tier boundaries replace.
func MergeComplexityAnalyzerConfigByHashes(base, file *ComplexityAnalyzerConfig) (*ComplexityAnalyzerConfig, error) {
	if file == nil {
		return MergeComplexityAnalyzerConfig(base, nil)
	}

	normalizedFile := file.Normalized()
	if err := normalizedFile.Validate(); err != nil {
		return nil, err
	}

	var merged ComplexityAnalyzerConfig
	if base != nil {
		merged = base.Normalized()
		if err := merged.Validate(); err != nil {
			return nil, err
		}
	}

	if merged.ConfigHashes.TierBoundaries != normalizedFile.ConfigHashes.TierBoundaries {
		merged.TierBoundaries = normalizedFile.TierBoundaries
		merged.ConfigHashes.TierBoundaries = normalizedFile.ConfigHashes.TierBoundaries
	}
	if merged.ConfigHashes.CodeKeywords != normalizedFile.ConfigHashes.CodeKeywords {
		merged.Keywords.CodeKeywords = mergeComplexityKeywordLists(merged.Keywords.CodeKeywords, normalizedFile.Keywords.CodeKeywords)
		merged.ConfigHashes.CodeKeywords = normalizedFile.ConfigHashes.CodeKeywords
	}
	if merged.ConfigHashes.ReasoningKeywords != normalizedFile.ConfigHashes.ReasoningKeywords {
		merged.Keywords.ReasoningKeywords = mergeComplexityKeywordLists(merged.Keywords.ReasoningKeywords, normalizedFile.Keywords.ReasoningKeywords)
		merged.ConfigHashes.ReasoningKeywords = normalizedFile.ConfigHashes.ReasoningKeywords
	}
	if merged.ConfigHashes.TechnicalKeywords != normalizedFile.ConfigHashes.TechnicalKeywords {
		merged.Keywords.TechnicalKeywords = mergeComplexityKeywordLists(merged.Keywords.TechnicalKeywords, normalizedFile.Keywords.TechnicalKeywords)
		merged.ConfigHashes.TechnicalKeywords = normalizedFile.ConfigHashes.TechnicalKeywords
	}
	if merged.ConfigHashes.SimpleKeywords != normalizedFile.ConfigHashes.SimpleKeywords {
		merged.Keywords.SimpleKeywords = mergeComplexityKeywordLists(merged.Keywords.SimpleKeywords, normalizedFile.Keywords.SimpleKeywords)
		merged.ConfigHashes.SimpleKeywords = normalizedFile.ConfigHashes.SimpleKeywords
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

	var record complexityAnalyzerConfigRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("failed to unmarshal complexity analyzer config: %w", err)
	}

	cfg := ComplexityAnalyzerConfig{
		TierBoundaries: record.TierBoundaries,
		Keywords:       record.Keywords,
		ConfigHashes:   record.ConfigHashes,
	}
	normalized := cfg.Normalized()
	if err := normalized.Validate(); err != nil {
		return nil, fmt.Errorf("invalid complexity analyzer config: %w", err)
	}
	return &normalized, nil
}

func encodeComplexityAnalyzerConfig(config ComplexityAnalyzerConfig) ([]byte, error) {
	record := complexityAnalyzerConfigRecord{
		TierBoundaries: config.TierBoundaries,
		Keywords:       config.Keywords,
		ConfigHashes:   config.ConfigHashes,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal complexity analyzer config: %w", err)
	}
	return data, nil
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
