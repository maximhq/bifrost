package suggestrun

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A tested phrase routes the examples it describes and leaves the rest as
// they were; the phrase's nearest live neighbour is reported.
func TestTestPhrasesRoutesWithAndWithout(t *testing.T) {
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(7)}}}
	got, err := TestPhrases(context.Background(), space, 0.5,
		[]complexity.SuggestionPhrase{{Tier: "SIMPLE", Text: "where is my refund"}},
		[]TestExample{{LogID: "a", Text: "refund for order 12"}, {LogID: "b", Text: "a haiku about rain"}})
	require.NoError(t, err)
	require.Len(t, got.Phrases, 1)
	assert.Equal(t, "write a poem", got.Phrases[0].Nearest.Phrase)
	require.Len(t, got.Examples, 2)
	refund, poem := got.Examples[0], got.Examples[1]
	assert.Equal(t, "COMPLEX", refund.Current.Tier, "before: only the poem phrase exists")
	assert.False(t, refund.Current.Accepted, "and it is not similar enough to accept")
	assert.Equal(t, "SIMPLE", refund.With.Tier)
	assert.True(t, refund.With.Candidate && refund.With.Accepted && refund.Changed)
	assert.Equal(t, "COMPLEX", poem.With.Tier, "an example the phrase does not describe keeps its routing")
	assert.False(t, poem.Changed)
	assert.InDelta(t, 0.5, got.TakenOver, 1e-9)

	_, err = TestPhrases(context.Background(), space, 0, nil, nil)
	require.Error(t, err)
	_, err = TestPhrases(context.Background(), space, 0, []complexity.SuggestionPhrase{{Tier: "HARD", Text: "x"}}, nil)
	require.ErrorContains(t, err, "tier")
}
