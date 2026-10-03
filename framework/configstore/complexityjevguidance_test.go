package configstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComplexityJevGuidanceDecoding checks the guidance keys are accepted and
// that misspelled tier-list names are rejected rather than silently ignored.
func TestComplexityJevGuidanceDecoding(t *testing.T) {
	var cfg ComplexityJevConfig
	require.NoError(t, json.Unmarshal([]byte(`{"criteria":{"MEDIUM":{"definition":"d","signals":["a"],"examples":["b"]}}}`), &cfg))
	assert.Equal(t, ComplexityJevTierCriteria{Definition: "d", Signals: []string{"a"}, Examples: []string{"b"}}, cfg.Criteria["MEDIUM"])

	cfg = ComplexityJevConfig{}
	assert.Error(t, json.Unmarshal([]byte(`{"criteria":{"MEDIUM":{"signal":["a"]}}}`), &cfg))
	cfg = ComplexityJevConfig{}
	assert.Error(t, json.Unmarshal([]byte(`{"criteria":{"MEDIUM":{"not_for":["a"]}}}`), &cfg), "not_for is no longer part of the criteria")
	cfg = ComplexityJevConfig{}
	assert.Error(t, json.Unmarshal([]byte(`{"criteria":{"MEDIUM":{"what":"a"}}}`), &cfg), "what was renamed to definition")
	cfg = ComplexityJevConfig{}
	assert.Error(t, json.Unmarshal([]byte(`{"decision_rule":"rule"}`), &cfg), "the decision rule is fixed, not configurable")
}

// TestComplexityJevGuidanceNormalization pins that only real overrides are
// stored: values equal to the shipped defaults, blanks, and duplicates are
// dropped, while case and order of administrator entries are kept.
func TestComplexityJevGuidanceNormalization(t *testing.T) {
	defaults := DefaultComplexityJevGuidance()

	untouched := (&ComplexityJevConfig{
		Criteria: map[string]ComplexityJevTierCriteria{
			"SIMPLE": {Definition: " " + defaults.Criteria["SIMPLE"].Definition + " ", Signals: defaults.Criteria["SIMPLE"].Signals, Examples: defaults.Criteria["SIMPLE"].Examples},
		},
	}).normalized()
	assert.Nil(t, untouched.Criteria, "default definitions and lists must not be persisted as overrides")

	edited := (&ComplexityJevConfig{
		Criteria: map[string]ComplexityJevTierCriteria{
			"MEDIUM": {Definition: "  Custom medium  ", Signals: []string{" Second ", "", "First", "Second"}, Examples: defaults.Criteria["MEDIUM"].Examples},
		},
	}).normalized()
	require.Contains(t, edited.Criteria, "MEDIUM")
	assert.Equal(t, "Custom medium", edited.Criteria["MEDIUM"].Definition)
	assert.Equal(t, []string{"Second", "First"}, edited.Criteria["MEDIUM"].Signals)
	assert.Nil(t, edited.Criteria["MEDIUM"].Examples, "a default list next to an edited one is still dropped")
}

// TestComplexityJevGuidanceValidation pins the bounds shared with the UI.
func TestComplexityJevGuidanceValidation(t *testing.T) {
	valid := &ComplexityJevConfig{Criteria: map[string]ComplexityJevTierCriteria{"COMPLEX": {Signals: []string{"ok"}}}}
	assert.NoError(t, valid.normalized().Validate())

	tooMany := make([]string, MaxComplexityJevCriteriaItems+1)
	for i := range tooMany {
		tooMany[i] = strings.Repeat("x", i+1)
	}
	for name, cfg := range map[string]*ComplexityJevConfig{
		"unknown tier":    {Criteria: map[string]ComplexityJevTierCriteria{"REASONING": {Signals: []string{"a"}}}},
		"lowercase tier":  {Criteria: map[string]ComplexityJevTierCriteria{"simple": {Signals: []string{"a"}}, "SIMPLE": {Signals: []string{"b"}}}},
		"too many items":  {Criteria: map[string]ComplexityJevTierCriteria{"SIMPLE": {Examples: tooMany}}},
		"long item":       {Criteria: map[string]ComplexityJevTierCriteria{"SIMPLE": {Signals: []string{strings.Repeat("x", MaxComplexityJevCriteriaItemCharacters+1)}}}},
		"long definition": {Criteria: map[string]ComplexityJevTierCriteria{"MEDIUM": {Definition: strings.Repeat("x", MaxComplexityJevDefinitionCharacters+1)}}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, cfg.normalized().Validate())
		})
	}
}

// TestComplexityJevGuidanceResolution checks overrides replace defaults list
// by list and that resolved values never alias the config or the defaults.
func TestComplexityJevGuidanceResolution(t *testing.T) {
	var nilConfig *ComplexityJevConfig
	assert.Equal(t, DefaultComplexityJevGuidance().Criteria, nilConfig.ResolvedCriteria())

	cfg := &ComplexityJevConfig{
		Criteria: map[string]ComplexityJevTierCriteria{"SIMPLE": {Definition: "custom definition", Examples: []string{"custom example"}}},
	}
	resolved := cfg.ResolvedCriteria()
	defaults := DefaultComplexityJevGuidance().Criteria
	assert.Equal(t, []string{"custom example"}, resolved["SIMPLE"].Examples)
	assert.Equal(t, defaults["SIMPLE"].Signals, resolved["SIMPLE"].Signals)
	assert.Equal(t, "custom definition", resolved["SIMPLE"].Definition)
	assert.Equal(t, defaults["MEDIUM"], resolved["MEDIUM"])

	resolved["SIMPLE"].Examples[0] = "mutated"
	assert.Equal(t, "custom example", cfg.Criteria["SIMPLE"].Examples[0])
}
