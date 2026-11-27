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
