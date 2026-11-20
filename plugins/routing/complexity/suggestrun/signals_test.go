package suggestrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var signalKinds = []string{"refund", "invoice", "billing"}

// signalSpace embeds each test signal on an axis of its own, so signals are
// told apart, and everything else as fakeSpace does.
type signalSpace struct{ *fakeSpace }

func (s signalSpace) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out, err := s.fakeSpace.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	for i, t := range texts {
		for j, name := range []string{"trait alpha", "trait beta", "trait gamma"} {
			if strings.Contains(strings.ToLower(t), name) {
				out[i] = axis([]int{3, 4, 6}[j])
			}
		}
	}
	return out, nil
}

// signalLLM writes one example per group, and answers signal calls from a
// script, recording the signal prompts it was sent.
type signalLLM struct {
	mu      sync.Mutex
	signals string // the signal step's answer; empty fails the call
	prompts []string
}

func (l *signalLLM) CompleteJSON(_ context.Context, messages []suggest.Message) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if strings.Contains(messages[0].Content, `{"signals"`) {
		l.prompts = append(l.prompts, messages[1].Content)
		if l.signals == "" {
			return "", errors.New("provider unavailable")
		}
		return l.signals, nil
	}
	for _, kind := range signalKinds {
		if strings.Contains(messages[1].Content, kind) {
			return fmt.Sprintf(`{"coherent":true,"examples":["Handle a %s request end to end"]}`, kind), nil
		}
	}
	return "", errors.New("unexpected prompt")
}

func signalAnswer(signals ...string) string {
	return `{"signals": [` + strings.Join(signals, ",") + `]}`
}

func signalRows(t *testing.T, store logstore.LogStore) map[string]logstore.ComplexitySuggestion {
	t.Helper()
	rows, err := store.ListComplexitySuggestions(context.Background(), logstore.ComplexitySuggestionFilter{})
	require.NoError(t, err)
	out := map[string]logstore.ComplexitySuggestion{}
	for _, r := range rows {
		if r.Category == logstore.ComplexitySuggestionCategorySignal {
			out[r.Phrases()[0]] = r
		}
	}
	return out
}

func signalRunSettings(id string, now time.Time) Settings {
	s := settings(id, now)
	s.Mode, s.MaxPerTier, s.MaxSignalsPerTier = suggest.ModeExamples, 60, 30
	s.Signals = map[string][]string{"SIMPLE": {"An existing simple trait"}}
	s.Definitions = map[string]string{"SIMPLE": "One short answer."}
	return s
}

// Three groups the decision model sends to COMPLEX belong in SIMPLE: the run
// writes their examples, then asks once for SIMPLE's signals (the other
// tiers got no groups, so they are not asked) and keeps each signal as a row
// of its own, carrying the requests of the groups it describes. Across runs
// a signal written again refreshes its row, a dismissed one is not written
// again and is shown to the model as declined, and an open one not written
// again is superseded.
func TestRunWritesSignalsForADecisionRouter(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	base := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "SIMPLE", Text: "Answer a direct everyday question", Vector: axis(5)}}}
	for i, kind := range signalKinds {
		base.addRouted(kind, i, 12+i, now.Add(-time.Hour), "COMPLEX")
	}
	space := signalSpace{base}
	alpha := `{"text": "Trait alpha of the work", "groups": [1, 2]}`
	beta := `{"text": "Trait beta of the work", "groups": [1, 3]}`
	gamma := `{"text": "Trait gamma of the work", "groups": [2, 3]}`

	llm := &signalLLM{signals: signalAnswer(alpha, beta)}
	first, err := Run(ctx, space, store, texts, llm, signalRunSettings("first", now))
	require.NoError(t, err)
	require.Len(t, llm.prompts, 1, "only SIMPLE got groups")
	assert.Contains(t, llm.prompts[0], "Tier: SIMPLE")
	assert.Contains(t, llm.prompts[0], "- An existing simple trait")
	assert.Contains(t, llm.prompts[0], "Group 3 (12 requests).", "groups come heaviest first")
	assert.EqualValues(t, 5, first.Run.Created, "three groups and two signals")
	assert.Empty(t, first.Run.Error)

	rows := signalRows(t, store)
	require.Len(t, rows, 2)
	a := rows["Trait alpha of the work"]
	assert.Equal(t, "SIMPLE", a.Tier)
	assert.Equal(t, logstore.ComplexitySuggestionStatusOpen, a.Status)
	assert.Empty(t, a.Centroid)
	assert.EqualValues(t, 14+13, a.RequestCount, "the two heaviest groups")
	assert.Equal(t, map[string]int64{"COMPLEX": 27}, a.CurrentRouting())
	ids := a.ExampleLogIDs()
	require.NotEmpty(t, ids)
	assert.True(t, strings.HasPrefix(ids[0], "billing-") && strings.HasPrefix(ids[1], "invoice-"), "each group's most typical request first: %v", ids)

	// Dismiss beta; the next run writes alpha, beta and gamma.
	_, err = store.UpdateComplexitySuggestionStatus(ctx, rows["Trait beta of the work"].ID, logstore.ComplexitySuggestionStatusOpen, logstore.ComplexitySuggestionStatusDismissed, &now)
	require.NoError(t, err)
	llm = &signalLLM{signals: signalAnswer(alpha, beta, gamma)}
	second, err := Run(ctx, space, store, texts, llm, signalRunSettings("second", now.Add(time.Hour)))
	require.NoError(t, err)
	assert.Contains(t, llm.prompts[0], "Declined signals, not to be written again:\n- Trait beta of the work")
	rows = signalRows(t, store)
	assert.Equal(t, a.ID, rows["Trait alpha of the work"].ID, "refreshed in place")
	assert.Equal(t, logstore.ComplexitySuggestionStatusDismissed, rows["Trait beta of the work"].Status)
	assert.Equal(t, logstore.ComplexitySuggestionStatusOpen, rows["Trait gamma of the work"].Status)
	assert.EqualValues(t, 1, second.Run.Created, "gamma; beta is declined and dropped as a repeat")

	// A failed signal call keeps the open signals and is noted on the run.
	llm = &signalLLM{}
	third, err := Run(ctx, space, store, texts, llm, signalRunSettings("third", now.Add(2*time.Hour)))
	require.NoError(t, err)
	assert.Contains(t, third.Run.Error, "signals for 1 of 3 tiers failed")
	assert.Equal(t, logstore.ComplexitySuggestionStatusOpen, signalRows(t, store)["Trait gamma of the work"].Status)

	// A run that writes gamma alone supersedes alpha.
	llm = &signalLLM{signals: signalAnswer(gamma)}
	_, err = Run(ctx, space, store, texts, llm, signalRunSettings("fourth", now.Add(3*time.Hour)))
	require.NoError(t, err)
	rows = signalRows(t, store)
	assert.Equal(t, logstore.ComplexitySuggestionStatusSuperseded, rows["Trait alpha of the work"].Status)
	assert.Equal(t, logstore.ComplexitySuggestionStatusOpen, rows["Trait gamma of the work"].Status)

	// A run that writes no signals (the router is semantic now) leaves none
	// open.
	off := signalRunSettings("fifth", now.Add(4*time.Hour))
	off.Mode, off.MaxSignalsPerTier = suggest.ModePhrases, 0
	_, err = Run(ctx, space, store, texts, &signalLLM{}, off)
	require.NoError(t, err)
	assert.Equal(t, logstore.ComplexitySuggestionStatusSuperseded, signalRows(t, store)["Trait gamma of the work"].Status)
}

// A tier full of signals is not asked, and a run without a signal bound, or a
// semantic router's, asks for none.
func TestRunAsksForSignalsOnlyWithRoom(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	base := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "SIMPLE", Text: "Answer a direct everyday question", Vector: axis(5)}}}
	for i, kind := range signalKinds {
		base.addRouted(kind, i, 12, now.Add(-time.Hour), "COMPLEX")
	}
	answer := signalAnswer(`{"text": "Trait alpha of the work", "groups": [1, 2]}`)

	full := signalRunSettings("full", now)
	full.MaxSignalsPerTier = 1 // SIMPLE already holds one
	off := signalRunSettings("off", now)
	off.MaxSignalsPerTier = 0
	for name, s := range map[string]Settings{"full": full, "off": off} {
		llm := &signalLLM{signals: answer}
		_, err := Run(ctx, signalSpace{base}, newStore(t), texts, llm, s)
		require.NoError(t, err)
		assert.Empty(t, llm.prompts, name)
	}

	estimate := EstimateRun(36, signalRunSettings("estimate", now))
	withoutSignals := EstimateRun(36, off)
	assert.Equal(t, withoutSignals.MaxModelCalls+3, estimate.MaxModelCalls, "one signal call per tier")
}

func TestInterleaveTakesEveryGroupFirst(t *testing.T) {
	assert.Equal(t, []string{"a1", "b1", "a2", "b2", "a3"}, interleave([][]string{{"a1", "a2", "a3", "a4"}, {"b1", "b2"}}, 5))
	assert.Equal(t, []string{"a1", "b1", "a2"}, interleave([][]string{{"a1", "a2"}, {"b1"}}, 10))
	assert.Empty(t, interleave(nil, 4))
}
