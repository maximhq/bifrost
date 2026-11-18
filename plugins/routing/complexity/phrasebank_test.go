package complexity

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func phraseBank() EditableKeywordConfig {
	return EditableKeywordConfig{
		SimpleKeywords:  []string{"say hello"},
		MediumKeywords:  []string{"summarize this thread"},
		ComplexKeywords: []string{"design a system"},
	}
}

// Phrases are appended to their tiers, a tier in any case; one its tier
// already holds, compared as the bank compares phrases, or repeated in the
// request is skipped and reported; the bank given is not modified.
func TestAppendPhrases(t *testing.T) {
	bank := phraseBank()
	out, result, err := AppendPhrases(bank, map[string][]string{
		"simple":  {"where is my refund", "  Say   Hello ", "where is my refund"},
		"COMPLEX": {"plan a migration"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"say hello", "where is my refund"}, out.SimpleKeywords)
	assert.Equal(t, []string{"summarize this thread"}, out.MediumKeywords)
	assert.Equal(t, []string{"design a system", "plan a migration"}, out.ComplexKeywords)
	assert.Equal(t, map[string][]string{TierSimple: {"where is my refund"}, TierComplex: {"plan a migration"}}, result.Added)
	assert.Equal(t, []SkippedPhrase{{Tier: TierSimple, Text: "Say   Hello"}, {Tier: TierSimple, Text: "where is my refund"}}, result.Skipped)
	assert.Equal(t, phraseBank(), bank, "the bank given is not modified")
}

// An append the bank cannot take is refused whole: a phrase another tier
// holds, an unknown tier, an empty or too-long phrase, nothing at all, or a
// bank past its limit.
func TestAppendPhrasesRefuses(t *testing.T) {
	full := phraseBank()
	for len(full.SimpleKeywords)+2 < configstore.MaxComplexitySemanticPhrases {
		full.SimpleKeywords = append(full.SimpleKeywords, "simple "+strings.Repeat("x", len(full.SimpleKeywords)))
	}
	for name, tc := range map[string]struct {
		bank EditableKeywordConfig
		add  map[string][]string
		want string
	}{
		"another tier holds it": {phraseBank(), map[string][]string{"MEDIUM": {"new one", "DESIGN a system"}}, "already a COMPLEX phrase"},
		"unknown tier":          {phraseBank(), map[string][]string{"HARD": {"x"}}, "unknown tier"},
		"a tier given twice":    {phraseBank(), map[string][]string{"SIMPLE": {"Refund"}, " simple": {"refund"}}, "given twice"},
		"empty phrase":          {phraseBank(), map[string][]string{"SIMPLE": {"  "}}, "empty"},
		"too long":              {phraseBank(), map[string][]string{"SIMPLE": {strings.Repeat("a", configstore.MaxComplexitySemanticPhraseCharacters+1)}}, "characters"},
		"nothing":               {phraseBank(), nil, "no phrases"},
		"tiers with no phrases": {phraseBank(), map[string][]string{"SIMPLE": {}, "MEDIUM": nil}, "no phrases"},
		"past the limit":        {full, map[string][]string{"MEDIUM": {"one more", "and another"}}, "limit"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := AppendPhrases(tc.bank, tc.add)
			require.ErrorIs(t, err, ErrPhraseAppend)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// Accepting a decision-model router's suggestion appends to the examples its
// decision model reads. A tier never edited starts from its shipped examples,
// which an override would otherwise replace; tiers given nothing are left as
// they are, and the config given is not modified.
func TestAppendDecisionExamples(t *testing.T) {
	shipped := configstore.DefaultComplexityDecisionGuidance().Criteria
	signals := []string{"Needs one lookup"}
	decision := &DecisionConfig{Model: "laya", Criteria: map[string]configstore.ComplexityDecisionTierCriteria{
		TierSimple: {Signals: signals, Examples: []string{"Answer a direct everyday question"}},
	}}
	out, result, err := AppendDecisionExamples(decision, map[string][]string{
		"simple":  {"Check the status of a refund", "answer a  DIRECT everyday question"},
		"COMPLEX": {"Plan a migration across several services"},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{TierSimple: {"Check the status of a refund"}, TierComplex: {"Plan a migration across several services"}}, result.Added)
	assert.Equal(t, []SkippedPhrase{{Tier: TierSimple, Text: "answer a  DIRECT everyday question"}}, result.Skipped)
	assert.Equal(t, []string{"Answer a direct everyday question", "Check the status of a refund"}, out.Criteria[TierSimple].Examples)
	assert.Equal(t, signals, out.Criteria[TierSimple].Signals, "the tier's other overrides are kept")
	assert.Equal(t, append(append([]string{}, shipped[TierComplex].Examples...), "Plan a migration across several services"), out.Criteria[TierComplex].Examples,
		"a tier never edited keeps its shipped examples")
	assert.NotContains(t, out.Criteria, TierMedium, "a tier given nothing is not written")
	assert.Equal(t, "laya", out.Model)
	assert.Len(t, decision.Criteria[TierSimple].Examples, 1, "the config given is not modified")
	assert.NotContains(t, decision.Criteria, TierComplex)

	fromNothing, _, err := AppendDecisionExamples(nil, map[string][]string{TierMedium: {"Solve a routine word problem"}})
	require.NoError(t, err)
	assert.Equal(t, append(append([]string{}, shipped[TierMedium].Examples...), "Solve a routine word problem"), fromNothing.Criteria[TierMedium].Examples)
}

// A decision append refuses what the decision guidance cannot hold: an
// example another tier has, one too long, and a tier past its limit.
func TestAppendDecisionExamplesRefuses(t *testing.T) {
	shipped := configstore.DefaultComplexityDecisionGuidance().Criteria
	full := make([]string, configstore.MaxComplexityDecisionExamples)
	for i := range full {
		full[i] = strings.Repeat("x", 3) + " example " + string(rune('a'+i%26)) + strings.Repeat("y", i/26)
	}
	decision := &DecisionConfig{Criteria: map[string]configstore.ComplexityDecisionTierCriteria{TierMedium: {Examples: full}}}
	for name, tc := range map[string]struct {
		add  map[string][]string
		want string
	}{
		"another tier's": {map[string][]string{TierSimple: {shipped[TierComplex].Examples[0]}}, "is already a COMPLEX example; an example describes one tier"},
		"too long":       {map[string][]string{TierSimple: {strings.Repeat("a", configstore.MaxComplexityDecisionCriteriaItemCharacters+1)}}, "the limit is 300"},
		"past the limit": {map[string][]string{TierMedium: {"one more"}}, "MEDIUM would hold 61 examples; the limit is 60 per tier"},
		"unknown tier":   {map[string][]string{"HARD": {"x"}}, "unknown tier"},
		"nothing to add": {map[string][]string{TierSimple: {}}, "no examples given"},
	} {
		out, _, err := AppendDecisionExamples(decision, tc.add)
		require.ErrorIs(t, err, ErrPhraseAppend, name)
		assert.ErrorContains(t, err, tc.want, name)
		assert.Same(t, decision, out, "%s: nothing is changed", name)
	}
}
