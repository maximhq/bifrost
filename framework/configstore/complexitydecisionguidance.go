package configstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// decision-model tier keys. They mirror the complexity package's tier names; configstore
// cannot import that package, so the three values are restated here.
const (
	complexityDecisionTierSimple  = "SIMPLE"
	complexityDecisionTierMedium  = "MEDIUM"
	complexityDecisionTierComplex = "COMPLEX"
)

// Bounds on the administrator's decision-model guidance. Every value is sent with each
// classification, so these cap the per-request token cost as much as they
// guard input size.
const (
	MaxComplexityDecisionDefinitionCharacters   = 500
	MaxComplexityDecisionCriteriaItems          = 12
	MaxComplexityDecisionCriteriaItemCharacters = 300
)

// ComplexityDecisionTierCriteria is one tier's editable decision-model criteria. An empty
// definition or nil list means the shipped default for that field is sent.
type ComplexityDecisionTierCriteria struct {
	Definition string   `json:"definition,omitempty"`
	Signals    []string `json:"signals,omitempty"`
	Examples   []string `json:"examples,omitempty"`
}

// ComplexityDecisionTierDefaults is one tier's shipped decision-model criteria.
type ComplexityDecisionTierDefaults struct {
	Definition string   `json:"definition"`
	Signals    []string `json:"signals"`
	Examples   []string `json:"examples"`
}

// ComplexityDecisionGuidanceDefaults is the shipped decision-model guidance, served to
// configuration clients so they can seed editors and offer resets without
// holding a copy that drifts from the gateway's.
type ComplexityDecisionGuidanceDefaults struct {
	Criteria map[string]ComplexityDecisionTierDefaults `json:"criteria"`
}

// complexityDecisionTierOrder is the canonical tier order for validation messages.
var complexityDecisionTierOrder = []string{complexityDecisionTierSimple, complexityDecisionTierMedium, complexityDecisionTierComplex}

// DefaultComplexityDecisionGuidance returns an independent copy of the shipped decision-model
// per-tier criteria.
func DefaultComplexityDecisionGuidance() ComplexityDecisionGuidanceDefaults {
	return ComplexityDecisionGuidanceDefaults{
		Criteria: map[string]ComplexityDecisionTierDefaults{
			complexityDecisionTierSimple: {
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
			complexityDecisionTierMedium: {
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
			complexityDecisionTierComplex: {
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
func (c *ComplexityDecisionConfig) ResolvedCriteria() map[string]ComplexityDecisionTierDefaults {
	resolved := DefaultComplexityDecisionGuidance().Criteria
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

// normalizeComplexityDecisionCriteria trims the definition and deduplicates every
// list, preserving order, and drops fields that equal the shipped default and
// tiers left empty.
// Unknown tier keys are kept so Validate can reject them by name.
func normalizeComplexityDecisionCriteria(criteria map[string]ComplexityDecisionTierCriteria) map[string]ComplexityDecisionTierCriteria {
	if len(criteria) == 0 {
		return nil
	}
	defaults := DefaultComplexityDecisionGuidance().Criteria
	out := make(map[string]ComplexityDecisionTierCriteria, len(criteria))
	for tier, tierCriteria := range criteria {
		// Tier keys are matched exactly, never case-folded: folding would let
		// "simple" and "SIMPLE" collapse onto one key, and Go's map order would
		// then pick which override survives. An unmatched key is kept so
		// Validate rejects it by name.
		definition := strings.TrimSpace(tierCriteria.Definition)
		signals := normalizeComplexityDecisionList(tierCriteria.Signals)
		examples := normalizeComplexityDecisionList(tierCriteria.Examples)
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
		out[tier] = ComplexityDecisionTierCriteria{Definition: definition, Signals: signals, Examples: examples}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeComplexityDecisionList trims entries and drops blanks and exact
// duplicates. Unlike semantic phrases it keeps case and order: these are
// sentences read by a model, and their order is the administrator's.
func normalizeComplexityDecisionList(values []string) []string {
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

// validateComplexityDecisionGuidance checks the tier keys, definition lengths, and
// per-list bounds of a normalized decision-model config.
func validateComplexityDecisionGuidance(c *ComplexityDecisionConfig) error {
	for tier, criteria := range c.Criteria {
		if !slices.Contains(complexityDecisionTierOrder, tier) {
			return fmt.Errorf("decision criteria tier must be one of %s, got %q", strings.Join(complexityDecisionTierOrder, ", "), tier)
		}
		if n := utf8.RuneCountInString(criteria.Definition); n > MaxComplexityDecisionDefinitionCharacters {
			return fmt.Errorf("decision criteria %s definition must be at most %d characters, got %d", tier, MaxComplexityDecisionDefinitionCharacters, n)
		}
		if err := validateComplexityDecisionList(tier, "signals", criteria.Signals); err != nil {
			return err
		}
		if err := validateComplexityDecisionList(tier, "examples", criteria.Examples); err != nil {
			return err
		}
	}
	return nil
}

// validateComplexityDecisionList bounds one tier list's item count and item length.
func validateComplexityDecisionList(tier, field string, values []string) error {
	if len(values) > MaxComplexityDecisionCriteriaItems {
		return fmt.Errorf("decision criteria %s %s must have at most %d items, got %d", tier, field, MaxComplexityDecisionCriteriaItems, len(values))
	}
	for _, value := range values {
		if n := utf8.RuneCountInString(value); n > MaxComplexityDecisionCriteriaItemCharacters {
			return fmt.Errorf("decision criteria %s %s items must be at most %d characters, got %d", tier, field, MaxComplexityDecisionCriteriaItemCharacters, n)
		}
	}
	return nil
}

// UnmarshalJSON rejects unknown fields so a misspelled field name in
// config.json fails loudly instead of silently sending the default.
func (c *ComplexityDecisionTierCriteria) UnmarshalJSON(data []byte) error {
	type alias ComplexityDecisionTierCriteria
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var aux alias
	if err := decoder.Decode(&aux); err != nil {
		return fmt.Errorf("invalid decision tier criteria: %w", err)
	}
	*c = ComplexityDecisionTierCriteria(aux)
	return nil
}
