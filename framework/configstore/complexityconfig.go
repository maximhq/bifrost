package configstore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ComplexityTierBoundaries defines score thresholds for complexity tier classification.
type ComplexityTierBoundaries struct {
	SimpleMedium  float64 `json:"simple_medium"`
	MediumComplex float64 `json:"medium_complex"`
}

// Validate checks that tier boundaries are ordered and inside the analyzer score range.
func (b *ComplexityTierBoundaries) Validate() error {
	if b == nil {
		return nil
	}
	if !(0 < b.SimpleMedium &&
		b.SimpleMedium < b.MediumComplex &&
		b.MediumComplex < 1) {
		return fmt.Errorf(
			"tier boundaries must satisfy 0 < simple_medium (%.4f) < medium_complex (%.4f) < 1",
			b.SimpleMedium, b.MediumComplex,
		)
	}
	return nil
}

// ComplexityEditableKeywordConfig contains the user-editable keyword lists.
type ComplexityEditableKeywordConfig struct {
	SimpleKeywords  []string `json:"simple_keywords"`
	MediumKeywords  []string `json:"medium_keywords"`
	ComplexKeywords []string `json:"complex_keywords"`
}

type legacyComplexityEditableKeywordConfig struct {
	CodeKeywords      []string `json:"code_keywords"`
	ReasoningKeywords []string `json:"reasoning_keywords"`
	TechnicalKeywords []string `json:"technical_keywords"`
	SimpleKeywords    []string `json:"simple_keywords"`
}

// UnmarshalJSON accepts the canonical three-list shape and the deprecated
// four-list shape. Runtime and API callers always receive the canonical shape.
func (c *ComplexityEditableKeywordConfig) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	allowed := map[string]struct{}{
		"simple_keywords":    {},
		"medium_keywords":    {},
		"complex_keywords":   {},
		"code_keywords":      {},
		"reasoning_keywords": {},
		"technical_keywords": {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unknown complexity keyword field %q", field)
		}
	}

	hasCanonical := hasAnyComplexityField(fields, "medium_keywords", "complex_keywords")
	hasLegacy := hasAnyComplexityField(fields, "code_keywords", "reasoning_keywords", "technical_keywords")
	if hasCanonical && hasLegacy {
		return fmt.Errorf("complexity keyword config cannot mix canonical and legacy fields")
	}

	if hasLegacy {
		var legacy legacyComplexityEditableKeywordConfig
		if err := json.Unmarshal(data, &legacy); err != nil {
			return err
		}
		*c = ComplexityEditableKeywordConfig{
			SimpleKeywords:  legacy.SimpleKeywords,
			MediumKeywords:  mergeComplexityKeywordLists(legacy.CodeKeywords, legacy.TechnicalKeywords),
			ComplexKeywords: legacy.ReasoningKeywords,
		}
		return nil
	}

	type canonicalComplexityEditableKeywordConfig ComplexityEditableKeywordConfig
	var canonical canonicalComplexityEditableKeywordConfig
	if err := json.Unmarshal(data, &canonical); err != nil {
		return err
	}
	*c = ComplexityEditableKeywordConfig(canonical)
	return nil
}

func hasAnyComplexityField(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		if _, ok := fields[name]; ok {
			return true
		}
	}
	return false
}

// ComplexityAnalyzerConfigHashes tracks the config.json hash for each editable
// analyzer section. It is persisted with the config row, but not exposed through
// API responses or config.json.
type ComplexityAnalyzerConfigHashes struct {
	TierBoundaries  string `json:"tier_boundaries,omitempty"`
	SimpleKeywords  string `json:"simple_keywords,omitempty"`
	MediumKeywords  string `json:"medium_keywords,omitempty"`
	ComplexKeywords string `json:"complex_keywords,omitempty"`
}

type legacyComplexityAnalyzerConfigHashes struct {
	TierBoundaries    string `json:"tier_boundaries,omitempty"`
	CodeKeywords      string `json:"code_keywords,omitempty"`
	ReasoningKeywords string `json:"reasoning_keywords,omitempty"`
	TechnicalKeywords string `json:"technical_keywords,omitempty"`
	SimpleKeywords    string `json:"simple_keywords,omitempty"`
}

// UnmarshalJSON translates persisted legacy section hashes into the canonical
// three-list representation without hashing the runtime DB keyword values.
func (h *ComplexityAnalyzerConfigHashes) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	hasCanonical := hasAnyComplexityField(fields, "medium_keywords", "complex_keywords")
	hasLegacy := hasAnyComplexityField(fields, "code_keywords", "reasoning_keywords", "technical_keywords")
	if hasCanonical && hasLegacy {
		return fmt.Errorf("complexity config hashes cannot mix canonical and legacy fields")
	}

	if hasLegacy {
		var legacy legacyComplexityAnalyzerConfigHashes
		if err := json.Unmarshal(data, &legacy); err != nil {
			return err
		}
		mediumHash, err := legacyMediumKeywordsHashFromSectionHashes(legacy.CodeKeywords, legacy.TechnicalKeywords)
		if err != nil {
			return err
		}
		*h = ComplexityAnalyzerConfigHashes{
			TierBoundaries:  legacy.TierBoundaries,
			SimpleKeywords:  legacy.SimpleKeywords,
			MediumKeywords:  mediumHash,
			ComplexKeywords: legacy.ReasoningKeywords,
		}
		return nil
	}

	type canonicalComplexityAnalyzerConfigHashes ComplexityAnalyzerConfigHashes
	var canonical canonicalComplexityAnalyzerConfigHashes
	if err := json.Unmarshal(data, &canonical); err != nil {
		return err
	}
	*h = ComplexityAnalyzerConfigHashes(canonical)
	return nil
}

// Empty reports whether no file-backed section hashes are present.
func (h ComplexityAnalyzerConfigHashes) Empty() bool {
	return h.TierBoundaries == "" &&
		h.SimpleKeywords == "" &&
		h.MediumKeywords == "" &&
		h.ComplexKeywords == ""
}

// Equal reports whether all section hashes match.
func (h ComplexityAnalyzerConfigHashes) Equal(other ComplexityAnalyzerConfigHashes) bool {
	return h.TierBoundaries == other.TierBoundaries &&
		h.SimpleKeywords == other.SimpleKeywords &&
		h.MediumKeywords == other.MediumKeywords &&
		h.ComplexKeywords == other.ComplexKeywords
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
	if len(c.Keywords.SimpleKeywords) == 0 {
		missing = append(missing, "simple_keywords")
	}
	if len(c.Keywords.MediumKeywords) == 0 {
		missing = append(missing, "medium_keywords")
	}
	if len(c.Keywords.ComplexKeywords) == 0 {
		missing = append(missing, "complex_keywords")
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
			SimpleKeywords:  normalizeComplexityKeywordList(c.Keywords.SimpleKeywords),
			MediumKeywords:  normalizeComplexityKeywordList(c.Keywords.MediumKeywords),
			ComplexKeywords: normalizeComplexityKeywordList(c.Keywords.ComplexKeywords),
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
			SimpleKeywords:  mergeComplexityKeywordLists(normalizedBase.Keywords.SimpleKeywords, normalizedFile.Keywords.SimpleKeywords),
			MediumKeywords:  mergeComplexityKeywordLists(normalizedBase.Keywords.MediumKeywords, normalizedFile.Keywords.MediumKeywords),
			ComplexKeywords: mergeComplexityKeywordLists(normalizedBase.Keywords.ComplexKeywords, normalizedFile.Keywords.ComplexKeywords),
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
	if merged.ConfigHashes.SimpleKeywords != normalizedFile.ConfigHashes.SimpleKeywords {
		merged.Keywords.SimpleKeywords = mergeComplexityKeywordLists(merged.Keywords.SimpleKeywords, normalizedFile.Keywords.SimpleKeywords)
		merged.ConfigHashes.SimpleKeywords = normalizedFile.ConfigHashes.SimpleKeywords
	}
	if merged.ConfigHashes.MediumKeywords != normalizedFile.ConfigHashes.MediumKeywords {
		merged.Keywords.MediumKeywords = mergeComplexityKeywordLists(merged.Keywords.MediumKeywords, normalizedFile.Keywords.MediumKeywords)
		merged.ConfigHashes.MediumKeywords = normalizedFile.ConfigHashes.MediumKeywords
	}
	if merged.ConfigHashes.ComplexKeywords != normalizedFile.ConfigHashes.ComplexKeywords {
		merged.Keywords.ComplexKeywords = mergeComplexityKeywordLists(merged.Keywords.ComplexKeywords, normalizedFile.Keywords.ComplexKeywords)
		merged.ConfigHashes.ComplexKeywords = normalizedFile.ConfigHashes.ComplexKeywords
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
