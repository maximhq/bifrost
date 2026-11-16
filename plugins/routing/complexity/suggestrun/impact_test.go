package suggestrun

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const none = logstore.ComplexitySuggestionUnmatchedTier

func impactSettings(now time.Time, draft ...complexity.SuggestionPhrase) ImpactSettings {
	return ImpactSettings{From: now.Add(-time.Hour), To: now, MinSimilarity: 0.5, Draft: draft, Seed: 1}
}

// A draft phrase routes the requests nothing matched today, and leaves the
// rest where they were.
func TestReplayImpactRoutesUnmatchedRequestsToADraftPhrase(t *testing.T) {
	now := time.Now()
	poem := complexity.SuggestionPhrase{Tier: "COMPLEX", Text: "write a poem", Vector: axis(7)}
	space := &fakeSpace{live: []complexity.SuggestionPhrase{poem}}
	space.add("refund", 0, 6, now)
	space.add("poem", 7, 4, now)

	got, err := ReplayImpact(context.Background(), space, impactSettings(now, poem, complexity.SuggestionPhrase{Tier: "SIMPLE", Text: "where is my refund"}))
	require.NoError(t, err)
	assert.Equal(t, 10, got.Requests)
	assert.Equal(t, 10, got.Total)
	assert.Equal(t, map[string]int{none: 6, "COMPLEX": 4}, got.Live)
	assert.Equal(t, map[string]int{"SIMPLE": 6, "COMPLEX": 4}, got.Draft)
	require.Len(t, got.Changed, 6)
	for _, c := range got.Changed {
		assert.Equal(t, none, c.From)
		assert.Equal(t, "SIMPLE", c.To)
		assert.Equal(t, "where is my refund", c.Phrase)
		assert.Greater(t, c.Similarity, 0.9)
	}
	assert.Empty(t, got.Attention, "requests nothing matched are not taken from anywhere")
}

// A draft phrase is embedded as the gateway will embed it once saved,
// lowercased, so the replay routes as the saved router will; the change still
// names the phrase as it was written.
func TestReplayImpactEmbedsDraftPhrasesAsTheyWillBeSaved(t *testing.T) {
	now := time.Now()
	poem := complexity.SuggestionPhrase{Tier: "COMPLEX", Text: "write a poem", Vector: axis(7)}
	space := &fakeSpace{live: []complexity.SuggestionPhrase{poem}}
	space.add("refund", 0, 3, now)

	got, err := ReplayImpact(context.Background(), space, impactSettings(now, poem, complexity.SuggestionPhrase{Tier: "SIMPLE", Text: "Where is my REFUND"}))
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"SIMPLE": 3}, got.Draft)
	require.Len(t, got.Changed, 3)
	assert.Equal(t, "Where is my REFUND", got.Changed[0].Phrase)
}

// A new phrase that pulls requests the live bank placed confidently in
// another tier is flagged; dropping a live phrase leaves its requests
// unmatched.
func TestReplayImpactFlagsConfidentRequestsMovedByANewPhrase(t *testing.T) {
	now := time.Now()
	// Off axis 1 along an axis no request leans on, so every invoice request
	// is nearer the draft's exact axis-1 phrase.
	offAxis := axis(1)
	offAxis[6] = 0.2
	invoice := complexity.SuggestionPhrase{Tier: "MEDIUM", Text: "explain this invoice", Vector: unit(offAxis)}
	poem := complexity.SuggestionPhrase{Tier: "COMPLEX", Text: "write a poem", Vector: axis(7)}
	space := &fakeSpace{live: []complexity.SuggestionPhrase{invoice, poem}}
	space.add("invoice", 1, 4, now)
	space.add("poem", 7, 3, now)

	got, err := ReplayImpact(context.Background(), space, impactSettings(now,
		invoice, complexity.SuggestionPhrase{Tier: "SIMPLE", Text: "invoice copy"}))
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"MEDIUM": 4, "COMPLEX": 3}, got.Live)
	assert.Equal(t, map[string]int{"SIMPLE": 4, none: 3}, got.Draft, "the poem phrase is not in the draft")
	require.Len(t, got.Attention, 1)
	assert.Equal(t, ImpactAttention{Phrase: "invoice copy", Tier: "SIMPLE", FromTier: "MEDIUM", Requests: 4, LogID: got.Attention[0].LogID}, got.Attention[0])
	assert.NotEmpty(t, got.Attention[0].LogID)
	assert.Len(t, got.Changed, 7)
}

// Phrases are matched to the live bank by tier and normalized text, so a
// draft that only restates the live bank changes nothing and embeds nothing.
func TestReplayImpactOfTheLiveBankChangesNothing(t *testing.T) {
	now := time.Now()
	poem := complexity.SuggestionPhrase{Tier: "COMPLEX", Text: "write a poem", Vector: axis(7)}
	space := &fakeSpace{live: []complexity.SuggestionPhrase{poem}}
	space.add("poem", 7, 3, now)

	got, err := ReplayImpact(context.Background(), space, impactSettings(now, complexity.SuggestionPhrase{Tier: "COMPLEX", Text: "  Write a POEM "}))
	require.NoError(t, err)
	assert.Equal(t, got.Live, got.Draft)
	assert.Empty(t, got.Changed)
}

func TestReplayImpactRefusesABadDraft(t *testing.T) {
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(7)}}}
	_, err := ReplayImpact(context.Background(), space, impactSettings(time.Now(), complexity.SuggestionPhrase{Tier: "HARD", Text: "x"}))
	require.ErrorIs(t, err, ErrInvalidPhraseTest)

	_, err = ReplayImpact(context.Background(), &fakeSpace{}, impactSettings(time.Now()))
	require.ErrorIs(t, err, complexity.ErrNoServingGeneration)
}

// A live match that only just clears the floor is not confident: a new
// phrase written for those requests taking them over is what a suggestion is
// for, so it is not flagged. A strong live match being overridden is.
func TestReplayImpactFlagsOnlyStrongLiveMatches(t *testing.T) {
	now := time.Now()
	// A live MEDIUM phrase about 0.45 from axis 1: above floor+0.10 (0.40),
	// well below a confident match.
	loose := axis(1)
	loose[6] = 2
	weak := complexity.SuggestionPhrase{Tier: "MEDIUM", Text: "should this branch have a comment", Vector: unit(loose)}
	space := &fakeSpace{live: []complexity.SuggestionPhrase{weak}}
	space.add("invoice", 1, 4, now)

	got, err := ReplayImpact(context.Background(), space, ImpactSettings{From: now.Add(-time.Hour), To: now, MinSimilarity: 0.3, Seed: 1,
		Draft: []complexity.SuggestionPhrase{weak, {Tier: "COMPLEX", Text: "invoice copy"}}})
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"COMPLEX": 4}, got.Draft, "the new phrase takes them")
	assert.Empty(t, got.Attention, "a 0.45 live match is not a confident one")
}

// A draft that also changes the router's floor is replayed at that floor; the
// live side keeps the serving one.
func TestReplayImpactJudgesTheDraftAtItsOwnFloor(t *testing.T) {
	now := time.Now()
	poem := complexity.SuggestionPhrase{Tier: "COMPLEX", Text: "write a poem", Vector: axis(7)}
	space := &fakeSpace{live: []complexity.SuggestionPhrase{poem}}
	space.add("refund", 0, 6, now)
	space.add("poem", 7, 4, now)
	s := impactSettings(now, poem, complexity.SuggestionPhrase{Tier: "SIMPLE", Text: "where is my refund"})
	strict := 0.9999 // no captured request is this close to any phrase
	s.DraftMinSimilarity = &strict

	got, err := ReplayImpact(context.Background(), space, s)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{none: 6, "COMPLEX": 4}, got.Live, "the live side keeps the serving floor")
	assert.Equal(t, map[string]int{none: 10}, got.Draft, "the draft side is judged at its own floor")
	require.Len(t, got.Changed, 4)
	for _, c := range got.Changed {
		assert.Equal(t, "COMPLEX", c.From)
		assert.Equal(t, none, c.To)
	}
}
