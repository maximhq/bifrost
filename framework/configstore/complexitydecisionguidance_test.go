package configstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComplexityDecisionGuidanceDecoding checks the guidance keys are accepted and
// that misspelled tier-list names are rejected rather than silently ignored.
func TestComplexityDecisionGuidanceDecoding(t *testing.T) {
	var cfg ComplexityDecisionConfig
	require.NoError(t, json.Unmarshal([]byte(`{"criteria":{"MEDIUM":{"definition":"d","signals":["a"],"examples":["b"]}}}`), &cfg))
	assert.Equal(t, ComplexityDecisionTierCriteria{Definition: "d", Signals: []string{"a"}, Examples: []string{"b"}}, cfg.Criteria["MEDIUM"])

	cfg = ComplexityDecisionConfig{}
	assert.Error(t, json.Unmarshal([]byte(`{"criteria":{"MEDIUM":{"signal":["a"]}}}`), &cfg))
	cfg = ComplexityDecisionConfig{}
	assert.Error(t, json.Unmarshal([]byte(`{"criteria":{"MEDIUM":{"not_for":["a"]}}}`), &cfg), "not_for is no longer part of the criteria")
	cfg = ComplexityDecisionConfig{}
	assert.Error(t, json.Unmarshal([]byte(`{"criteria":{"MEDIUM":{"what":"a"}}}`), &cfg), "what was renamed to definition")
	cfg = ComplexityDecisionConfig{}
	assert.Error(t, json.Unmarshal([]byte(`{"decision_rule":"rule"}`), &cfg), "the decision rule is fixed, not configurable")
}

// TestComplexityDecisionGuidanceNormalization pins that only real overrides are
// stored: values equal to the shipped defaults, blanks, and duplicates are
// dropped, while case and order of administrator entries are kept.
func TestComplexityDecisionGuidanceNormalization(t *testing.T) {
	defaults := DefaultComplexityDecisionGuidance()

	untouched := (&ComplexityDecisionConfig{
		Criteria: map[string]ComplexityDecisionTierCriteria{
			"SIMPLE": {Definition: " " + defaults.Criteria["SIMPLE"].Definition + " ", Signals: defaults.Criteria["SIMPLE"].Signals, Examples: defaults.Criteria["SIMPLE"].Examples},
		},
	}).normalized()
	assert.Nil(t, untouched.Criteria, "default definitions and lists must not be persisted as overrides")

	edited := (&ComplexityDecisionConfig{
		Criteria: map[string]ComplexityDecisionTierCriteria{
			"MEDIUM": {Definition: "  Custom medium  ", Signals: []string{" Second ", "", "First", "Second"}, Examples: defaults.Criteria["MEDIUM"].Examples},
		},
	}).normalized()
	require.Contains(t, edited.Criteria, "MEDIUM")
	assert.Equal(t, "Custom medium", edited.Criteria["MEDIUM"].Definition)
	assert.Equal(t, []string{"Second", "First"}, edited.Criteria["MEDIUM"].Signals)
	assert.Nil(t, edited.Criteria["MEDIUM"].Examples, "a default list next to an edited one is still dropped")
}

// TestComplexityDecisionGuidanceValidation pins the bounds shared with the UI.
func TestComplexityDecisionGuidanceValidation(t *testing.T) {
	valid := &ComplexityDecisionConfig{Criteria: map[string]ComplexityDecisionTierCriteria{"COMPLEX": {Signals: []string{"ok"}}}}
	assert.NoError(t, valid.normalized().Validate())

	tooMany := make([]string, MaxComplexityDecisionCriteriaItems+1)
	for i := range tooMany {
		tooMany[i] = strings.Repeat("x", i+1)
	}
	for name, cfg := range map[string]*ComplexityDecisionConfig{
		"unknown tier":    {Criteria: map[string]ComplexityDecisionTierCriteria{"REASONING": {Signals: []string{"a"}}}},
		"lowercase tier":  {Criteria: map[string]ComplexityDecisionTierCriteria{"simple": {Signals: []string{"a"}}, "SIMPLE": {Signals: []string{"b"}}}},
		"too many items":  {Criteria: map[string]ComplexityDecisionTierCriteria{"SIMPLE": {Examples: tooMany}}},
		"long item":       {Criteria: map[string]ComplexityDecisionTierCriteria{"SIMPLE": {Signals: []string{strings.Repeat("x", MaxComplexityDecisionCriteriaItemCharacters+1)}}}},
		"long definition": {Criteria: map[string]ComplexityDecisionTierCriteria{"MEDIUM": {Definition: strings.Repeat("x", MaxComplexityDecisionDefinitionCharacters+1)}}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, cfg.normalized().Validate())
		})
	}
}

// TestComplexityDecisionGuidanceResolution checks overrides replace defaults list
// by list and that resolved values never alias the config or the defaults.
func TestComplexityDecisionGuidanceResolution(t *testing.T) {
	var nilConfig *ComplexityDecisionConfig
	assert.Equal(t, DefaultComplexityDecisionGuidance().Criteria, nilConfig.ResolvedCriteria())

	cfg := &ComplexityDecisionConfig{
		Criteria: map[string]ComplexityDecisionTierCriteria{"SIMPLE": {Definition: "custom definition", Examples: []string{"custom example"}}},
	}
	resolved := cfg.ResolvedCriteria()
	defaults := DefaultComplexityDecisionGuidance().Criteria
	assert.Equal(t, []string{"custom example"}, resolved["SIMPLE"].Examples)
	assert.Equal(t, defaults["SIMPLE"].Signals, resolved["SIMPLE"].Signals)
	assert.Equal(t, "custom definition", resolved["SIMPLE"].Definition)
	assert.Equal(t, defaults["MEDIUM"], resolved["MEDIUM"])

	resolved["SIMPLE"].Examples[0] = "mutated"
	assert.Equal(t, "custom example", cfg.Criteria["SIMPLE"].Examples[0])
}
