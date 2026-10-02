package configstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Jev tier keys. They mirror the complexity package's tier names; configstore
// cannot import that package, so the three values are restated here.
const (
	complexityJevTierSimple  = "SIMPLE"
	complexityJevTierMedium  = "MEDIUM"
	complexityJevTierComplex = "COMPLEX"
)

// Bounds on the administrator's Jev guidance. Every value is sent with each
// classification, so these cap the per-request token cost as much as they
// guard input size.
const (
	MaxComplexityJevDefinitionCharacters   = 500
	MaxComplexityJevCriteriaItems          = 12
	MaxComplexityJevCriteriaItemCharacters = 300
)

// ComplexityJevTierCriteria is one tier's editable Jev criteria. An empty
// definition or nil list means the shipped default for that field is sent.
type ComplexityJevTierCriteria struct {
	Definition string   `json:"definition,omitempty"`
	Signals    []string `json:"signals,omitempty"`
	Examples   []string `json:"examples,omitempty"`
}

// ComplexityJevTierDefaults is one tier's shipped Jev criteria.
type ComplexityJevTierDefaults struct {
	Definition string   `json:"definition"`
	Signals    []string `json:"signals"`
	Examples   []string `json:"examples"`
}

// ComplexityJevGuidanceDefaults is the shipped Jev guidance, served to
// configuration clients so they can seed editors and offer resets without
// holding a copy that drifts from the gateway's.
type ComplexityJevGuidanceDefaults struct {
	Criteria map[string]ComplexityJevTierDefaults `json:"criteria"`
}

// complexityJevTierOrder is the canonical tier order for validation messages.
var complexityJevTierOrder = []string{complexityJevTierSimple, complexityJevTierMedium, complexityJevTierComplex}

// DefaultComplexityJevGuidance returns an independent copy of the shipped Jev
// per-tier criteria.
func DefaultComplexityJevGuidance() ComplexityJevGuidanceDefaults {
	return ComplexityJevGuidanceDefaults{
		Criteria: map[string]ComplexityJevTierDefaults{
			complexityJevTierSimple: {
				Definition: "Direct work answerable from the request itself or common knowledge in one straightforward step, with little interpretation.",
				Signals: []string{
					"The needed information is stated in the request or is common, broadly familiar knowledge",
					"Perform one basic calculation using a familiar operation, or a simple transformation",
					"The request is clear and does not depend on specialist knowledge or meaningful interpretation",
				},
				Examples: []string{
					"Extract a value stated in a passage",
					"Answer a direct everyday question using common knowledge",
					"Reformat text or perform basic arithmetic",
				},
			},
			complexityJevTierMedium: {
				Definition: "Focused work that needs subject-specific knowledge not supplied in the request, or an established method applied across a few steps, even when the question is short or asks for one answer.",
				Signals: []string{
					"Answer a focused technical or academic question using subject knowledge not stated in the prompt",
					"Apply an established concept or method to the facts provided",
					"Combine a few dependent steps, calculations, or pieces of evidence",
					"Complete standard analysis or implementation with limited design choices",
					"Interpret moderate ambiguity or several ordinary constraints",
				},
				Examples: []string{
					"Answer a focused question that relies on established subject-matter knowledge",
					"Solve a routine multi-step word problem",
					"Apply a standard formula or method to provided facts",
					"Interpret a short technical or study summary",
					"Make a focused code change with a known approach",
				},
			},
			complexityJevTierComplex: {
				Definition: "Advanced expertise combined with substantial reasoning, derivation, design, or synthesis.",
				Signals: []string{
					"Several dependent reasoning stages, or a nontrivial derivation or proof using multiple concepts",
					"A novel approach, difficult algorithm, or difficult debugging is required",
					"Combine advanced subject knowledge with conflicting evidence or many interacting constraints",
					"A plausible mistake is hard to detect without deep analysis",
				},
				Examples: []string{
					"Derive a result from multiple conditions",
					"Design an efficient solution where tradeoffs matter",
					"Combine specialized concepts to resolve competing interpretations across several sources",
					"Find the cause of a difficult, previously unexplained failure",
				},
			},
		},
	}
}

// ResolvedCriteria returns every tier's criteria with administrator overrides
// layered over the shipped defaults, field by field.
func (c *ComplexityJevConfig) ResolvedCriteria() map[string]ComplexityJevTierDefaults {
	resolved := DefaultComplexityJevGuidance().Criteria
	if c == nil {
		return resolved
	}
	for tier, override := range c.Criteria {
		base, ok := resolved[tier]
		if !ok {
			continue
		}
		if override.Definition != "" {
			base.Definition = override.Definition
		}
		if len(override.Signals) > 0 {
			base.Signals = slices.Clone(override.Signals)
		}
		if len(override.Examples) > 0 {
			base.Examples = slices.Clone(override.Examples)
		}
		resolved[tier] = base
	}
	return resolved
}

// normalizeComplexityJevCriteria trims the definition and deduplicates every
// list, preserving order, and drops fields that equal the shipped default and
// tiers left empty.
// Unknown tier keys are kept so Validate can reject them by name.
func normalizeComplexityJevCriteria(criteria map[string]ComplexityJevTierCriteria) map[string]ComplexityJevTierCriteria {
	if len(criteria) == 0 {
		return nil
	}
	defaults := DefaultComplexityJevGuidance().Criteria
	out := make(map[string]ComplexityJevTierCriteria, len(criteria))
	for tier, tierCriteria := range criteria {
		// Tier keys are matched exactly, never case-folded: folding would let
		// "simple" and "SIMPLE" collapse onto one key, and Go's map order would
		// then pick which override survives. An unmatched key is kept so
		// Validate rejects it by name.
		definition := strings.TrimSpace(tierCriteria.Definition)
		signals := normalizeComplexityJevList(tierCriteria.Signals)
		examples := normalizeComplexityJevList(tierCriteria.Examples)
		if base, ok := defaults[tier]; ok {
			if definition == base.Definition {
				definition = ""
			}
			if slices.Equal(signals, base.Signals) {
				signals = nil
			}
			if slices.Equal(examples, base.Examples) {
				examples = nil
			}
		}
		if definition == "" && signals == nil && examples == nil {
			if _, known := defaults[tier]; known {
				continue
			}
		}
		out[tier] = ComplexityJevTierCriteria{Definition: definition, Signals: signals, Examples: examples}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeComplexityJevList trims entries and drops blanks and exact
// duplicates. Unlike semantic phrases it keeps case and order: these are
// sentences read by a model, and their order is the administrator's.
func normalizeComplexityJevList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateComplexityJevGuidance checks the tier keys, definition lengths, and
// per-list bounds of a normalized Jev config.
func validateComplexityJevGuidance(c *ComplexityJevConfig) error {
	for tier, criteria := range c.Criteria {
		if !slices.Contains(complexityJevTierOrder, tier) {
			return fmt.Errorf("jev criteria tier must be one of %s, got %q", strings.Join(complexityJevTierOrder, ", "), tier)
		}
		if n := utf8.RuneCountInString(criteria.Definition); n > MaxComplexityJevDefinitionCharacters {
			return fmt.Errorf("jev criteria %s definition must be at most %d characters, got %d", tier, MaxComplexityJevDefinitionCharacters, n)
		}
		if err := validateComplexityJevList(tier, "signals", criteria.Signals); err != nil {
			return err
		}
		if err := validateComplexityJevList(tier, "examples", criteria.Examples); err != nil {
			return err
		}
	}
	return nil
}

// validateComplexityJevList bounds one tier list's item count and item length.
func validateComplexityJevList(tier, field string, values []string) error {
	if len(values) > MaxComplexityJevCriteriaItems {
		return fmt.Errorf("jev criteria %s %s must have at most %d items, got %d", tier, field, MaxComplexityJevCriteriaItems, len(values))
	}
	for _, value := range values {
		if n := utf8.RuneCountInString(value); n > MaxComplexityJevCriteriaItemCharacters {
			return fmt.Errorf("jev criteria %s %s items must be at most %d characters, got %d", tier, field, MaxComplexityJevCriteriaItemCharacters, n)
		}
	}
	return nil
}

// UnmarshalJSON rejects unknown fields so a misspelled field name in
// config.json fails loudly instead of silently sending the default.
func (c *ComplexityJevTierCriteria) UnmarshalJSON(data []byte) error {
	type alias ComplexityJevTierCriteria
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var aux alias
	if err := decoder.Decode(&aux); err != nil {
		return fmt.Errorf("invalid jev tier criteria: %w", err)
	}
	*c = ComplexityJevTierCriteria(aux)
	return nil
}
