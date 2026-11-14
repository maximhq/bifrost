package suggestrun

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDim = 8

func axis(i int) []float32 {
	v := make([]float32, testDim)
	v[i] = 1
	return v
}

// nearAxis is a unit vector close to axis i, varied by j.
func nearAxis(i, j int) []float32 {
	v := axis(i)
	v[(i+1+j%3)%testDim] = 0.05 * float32(1+j%4)
	return unit(v)
}

// fakeSpace serves captured requests from memory and embeds by keyword.
type fakeSpace struct {
	captured []complexity.CapturedRequest
	live     []complexity.SuggestionPhrase
}

func (f *fakeSpace) Namespace() string                          { return "BifrostComplexityRequests_test" }
func (f *fakeSpace) LivePhrases() []complexity.SuggestionPhrase { return f.live }
func (f *fakeSpace) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		switch {
		case strings.Contains(t, "refund"):
			out[i] = axis(0)
		case strings.Contains(t, "invoice"):
			out[i] = axis(1)
		default:
			out[i] = axis(7)
		}
	}
	return out, nil
}
func (f *fakeSpace) EachCaptured(_ context.Context, from, to time.Time, fn func([]complexity.CapturedRequest) error) error {
	var page []complexity.CapturedRequest
	for _, r := range f.captured {
		if !r.At.Before(from) && !r.At.After(to) {
			page = append(page, r)
			if len(page) == 5 {
				if err := fn(page); err != nil {
					return err
				}
				page = nil
			}
		}
	}
	if len(page) > 0 {
		return fn(page)
	}
	return nil
}

func (f *fakeSpace) add(kind string, axisIndex, n int, at time.Time) {
	for j := range n {
		f.captured = append(f.captured, complexity.CapturedRequest{LogID: fmt.Sprintf("%s-%d", kind, j), Vector: nearAxis(axisIndex, j), At: at})
	}
}

// texts reads a log ID's kind back as its text.
func texts(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		kind, _, _ := strings.Cut(id, "-")
		out[id] = "please help with my " + kind + " " + id
	}
	return out, nil
}

// kindLLM answers per request kind and counts calls.
type kindLLM struct {
	mu      sync.Mutex
	calls   map[string]int
	fail    map[string]bool
	phrases map[string][]string // phrases written for a kind; one by default
}

func (l *kindLLM) CompleteJSON(_ context.Context, messages []suggest.Message) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.calls == nil {
		l.calls = map[string]int{}
	}
	for _, kind := range []string{"refund", "invoice"} {
		if strings.Contains(messages[1].Content, kind) {
			l.calls[kind]++
			if l.fail[kind] {
				return "", errors.New("provider unavailable")
			}
			phrases := []string{"where is my " + kind}
			if p, ok := l.phrases[kind]; ok {
				phrases = p
			}
			raw, _ := providerUtils.MarshalSorted(phrases)
			return fmt.Sprintf(`{"coherent":true,"tier":"SIMPLE","confidence":"high","reason":"%s questions","phrases":%s}`, kind, raw), nil
		}
	}
	return "", errors.New("unexpected prompt")
}

// fixedTier decides every cluster's tier the same way.
type fixedTier string

func (f fixedTier) DecideTier(context.Context, suggest.TierInput) (suggest.TierVerdict, error) {
	return suggest.TierVerdict{Tier: string(f), Confidence: "high"}, nil
}

func newStore(t *testing.T) logstore.LogStore {
	t.Helper()
	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })
	return store
}

func settings(id string, now time.Time) Settings {
	return Settings{RunID: id, RouterID: "engineering", TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule, Now: now, Lookback: 7 * 24 * time.Hour,
		MaxClustered: 1000, MinClusterRequests: 6, Model: "openai/gpt-5", Seed: 1, Cost: func() float64 { return 0.02 },
		Tier: fixedTier("SIMPLE"), TierPrompt: "llm"}
}

func byTier(rows []logstore.ComplexitySuggestion) map[string]logstore.ComplexitySuggestion {
	out := map[string]logstore.ComplexitySuggestion{}
	for _, r := range rows {
		out[r.Phrases()[0]] = r
	}
	return out
}

// Over three runs: suggestions are created, a dismissed one stays dismissed
// without a model call, an open one is refreshed in place, and one whose
// traffic stopped is superseded.
func TestRunCreatesRefreshesAndSupersedes(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("refund", 0, 12, now.Add(-time.Hour))
	space.add("invoice", 1, 9, now.Add(-2*24*time.Hour))
	space.add("noise", 4, 2, now.Add(-time.Hour)) // too few to be a pattern
	llm := &kindLLM{}

	result, err := Run(ctx, space, store, texts, llm, settings("run-1", now))
	require.NoError(t, err)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, result.Run.Status)
	assert.EqualValues(t, 23, result.Run.RequestsRead)
	assert.EqualValues(t, 2, result.Run.Created)
	assert.InDelta(t, 0.02, result.Run.Cost, 1e-9)
	assert.Equal(t, PromptHash("", "llm"), result.Run.PromptHash)
	// How the window became suggestions: all of it sampled, the two noise
	// requests too few to group, and both groups asked about.
	assert.EqualValues(t, 23, result.Run.RequestsClustered)
	assert.Zero(t, result.Run.RequestsAssigned)
	assert.Zero(t, result.Run.RequestsSkipped)
	assert.EqualValues(t, 21, result.Run.RequestsGrouped)
	assert.EqualValues(t, 2, result.Run.Clusters)
	assert.EqualValues(t, 2, result.Run.ClustersAsked)
	assert.EqualValues(t, 2, result.Run.ClustersDone)
	assert.Zero(t, result.Run.ClustersDecided+result.Run.ClustersOverLimit+result.Run.ClustersFailed)
	assert.Zero(t, result.Run.ClustersCovered+result.Run.ClustersIncoherent+result.Run.ClustersUnreadable)
	assert.Empty(t, result.Run.Stage, "a finished run reports no stage")
	stored, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{IDs: []string{"run-1"}}, 1)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, result.Run.RequestsGrouped, stored[0].RequestsGrouped, "the breakdown is stored with the run")
	assert.Empty(t, stored[0].Stage)
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	first := byTier(rows)
	refund, invoice := first["where is my refund"], first["where is my invoice"]
	assert.EqualValues(t, 12, refund.RequestCount)
	assert.EqualValues(t, 9, invoice.RequestCount)
	assert.Equal(t, logstore.ComplexitySuggestionStatusOpen, refund.Status)
	assert.Equal(t, string(suggest.CategoryUncovered), refund.Category)
	assert.Equal(t, "write a poem", refund.NearestPhrase)
	assert.NotEmpty(t, refund.ExampleLogIDs())
	assert.True(t, strings.HasPrefix(refund.ExampleLogIDs()[0], "refund-"))
	assert.Equal(t, "run-1", refund.RunID)
	assert.Equal(t, map[string]int64{logstore.ComplexitySuggestionUnmatchedTier: 12}, refund.CurrentRouting(),
		"no live phrase is near a refund, so every one of them matches nothing today")

	// The user dismisses the refund suggestion; more invoices arrive.
	refund.Status = logstore.ComplexitySuggestionStatusDismissed
	require.NoError(t, store.UpsertComplexitySuggestions(ctx, []logstore.ComplexitySuggestion{refund}))
	space.add("invoice-late", 1, 3, now.Add(-30*time.Minute))
	later := now.Add(24 * time.Hour)
	result, err = Run(ctx, space, store, texts, llm, settings("run-2", later))
	require.NoError(t, err)
	assert.EqualValues(t, 0, result.Run.Created)
	assert.EqualValues(t, 1, result.Run.Refreshed)
	assert.Equal(t, 1, llm.calls["refund"], "a dismissed suggestion costs no model call")
	assert.EqualValues(t, 1, result.Run.ClustersDecided)
	assert.EqualValues(t, 1, result.Run.ClustersAsked)
	rows, err = store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 2, "refreshed in place, not duplicated")
	second := byTier(rows)
	assert.Equal(t, logstore.ComplexitySuggestionStatusDismissed, second["where is my refund"].Status, "a decision sticks")
	assert.Equal(t, "run-1", second["where is my refund"].RunID, "and its row is left exactly as it was")
	assert.Equal(t, invoice.ID, second["where is my invoice"].ID)
	assert.EqualValues(t, 12, second["where is my invoice"].RequestCount)
	assert.Equal(t, "run-2", second["where is my invoice"].RunID)

	// A week on, the invoice traffic has left the window.
	space.captured = nil
	space.add("refund", 0, 12, later.Add(7*24*time.Hour-time.Hour))
	result, err = Run(ctx, space, store, texts, llm, settings("run-3", later.Add(7*24*time.Hour)))
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.Run.Superseded)
	gone, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{IDs: []string{invoice.ID}})
	require.NoError(t, err)
	require.Len(t, gone, 1)
	assert.Equal(t, logstore.ComplexitySuggestionStatusSuperseded, gone[0].Status)
	require.NotNil(t, gone[0].ResolvedAt)

	runs, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{RouterID: "engineering"}, 10)
	require.NoError(t, err)
	assert.Len(t, runs, 3)
}

// decidingLLM answers like kindLLM, and the first time it is asked about
// kind, the user decides that suggestion: the decision lands while the run is
// still asking the model, after it read the stored suggestions.
type decidingLLM struct {
	kindLLM
	store    logstore.LogStore
	kind, id string
	once     sync.Once
}

func (l *decidingLLM) CompleteJSON(ctx context.Context, messages []suggest.Message) (string, error) {
	if strings.Contains(messages[1].Content, l.kind) {
		l.once.Do(func() {
			now := time.Now().UTC()
			_, _ = l.store.UpdateComplexitySuggestionStatus(ctx, l.id, logstore.ComplexitySuggestionStatusOpen, logstore.ComplexitySuggestionStatusAccepted, &now)
		})
	}
	return l.kindLLM.CompleteJSON(ctx, messages)
}

// A suggestion the user accepts while a run is asking the model about it
// stays accepted: the run's refresh, computed from the open row it read
// earlier, is not written over it, nor counted.
func TestRunKeepsADecisionMadeWhileItRuns(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("refund", 0, 12, now.Add(-time.Hour))
	_, err := Run(ctx, space, store, texts, &kindLLM{}, settings("run-1", now))
	require.NoError(t, err)
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	refund := rows[0]

	llm := &decidingLLM{store: store, kind: "refund", id: refund.ID}
	result, err := Run(ctx, space, store, texts, llm, settings("run-2", now.Add(time.Hour)))
	require.NoError(t, err)
	assert.Zero(t, result.Run.Refreshed, "the refresh that was not written is not counted")
	rows, err = store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{IDs: []string{refund.ID}})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, logstore.ComplexitySuggestionStatusAccepted, rows[0].Status, "the decision made during the run stands")
	assert.Equal(t, "run-1", rows[0].RunID, "the run's refresh was not written over it")
}

// A cluster whose example logs were all deleted or hide their content is
// unreadable: the run learned nothing about it, so the open suggestion it
// matches is left exactly as it was rather than superseded.
func TestRunLeavesOpenSuggestionOfUnreadableClusterAlone(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("invoice", 1, 9, now.Add(-time.Hour))
	llm := &kindLLM{}
	_, err := Run(ctx, space, store, texts, llm, settings("run-1", now))
	require.NoError(t, err)
	before, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{})
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.Equal(t, logstore.ComplexitySuggestionStatusOpen, before[0].Status)

	// Every invoice log is now gone or hidden: no text comes back.
	hidden := func(context.Context, []string) (map[string]string, error) { return map[string]string{}, nil }
	result, err := Run(ctx, space, store, hidden, llm, settings("run-2", now.Add(time.Hour)))
	require.NoError(t, err)
	assert.Zero(t, result.Run.Superseded)
	assert.Zero(t, result.Run.Refreshed)
	assert.Equal(t, 1, llm.calls["invoice"], "an unreadable cluster is not sent to the model")
	after, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{})
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, before[0], after[0], "the open suggestion is unchanged")
}

// A window larger than the cluster budget is sampled, and the rest counted
// in, so a suggestion's count covers the whole window.
func TestRunCountsTheWholeWindowPastTheBudget(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("refund", 0, 40, now.Add(-time.Hour))
	space.add("invoice", 1, 40, now.Add(-time.Hour))
	s := settings("run-1", now)
	s.MaxClustered = 30

	result, err := Run(ctx, space, store, texts, &kindLLM{}, s)
	require.NoError(t, err)
	assert.Equal(t, 30, result.Clustered)
	assert.Equal(t, 50, result.Assigned)
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{})
	require.NoError(t, err)
	var total int64
	for _, r := range rows {
		total += r.RequestCount
	}
	assert.EqualValues(t, 80-result.Unmatched, total, "every request in the window is in a count unless it matched nothing")
	assert.Zero(t, result.Unmatched)
}

// One cluster's model failure leaves it for the next run without failing the
// run; no model at all fails the run, and the failure is recorded.
func TestRunFailures(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("refund", 0, 12, now.Add(-time.Hour))
	space.add("invoice", 1, 12, now.Add(-time.Hour))

	result, err := Run(ctx, space, store, texts, &kindLLM{fail: map[string]bool{"invoice": true}}, settings("partial", now))
	require.NoError(t, err)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, result.Run.Status)
	assert.Contains(t, result.Run.Error, "1 of 2 clusters failed")
	assert.EqualValues(t, 1, result.Run.Created)

	result, err = Run(ctx, space, store, texts, &kindLLM{fail: map[string]bool{"invoice": true, "refund": true}}, settings("all-failed", now))
	require.Error(t, err)
	assert.Equal(t, logstore.ComplexitySuggestionRunFailed, result.Run.Status)

	_, err = Run(ctx, space, store, texts, nil, settings("no-model", now))
	require.ErrorContains(t, err, "no suggestion model")
	runs, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{IDs: []string{"no-model"}}, 1)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunFailed, runs[0].Status)
	assert.Contains(t, runs[0].Error, "no suggestion model")
	require.NotNil(t, runs[0].FinishedAt)

	noTier := settings("no-tier", now)
	noTier.Tier = nil
	_, err = Run(ctx, space, store, texts, &kindLLM{}, noTier)
	require.ErrorContains(t, err, "no tier model")
	runs, err = store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{IDs: []string{"no-tier"}}, 1)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunFailed, runs[0].Status)
}

// cancellingLLM ends the run's context on its first model call, as a user's
// cancel reaching the run mid-way would. With plain set it answers as the
// gateway's model adapters do, with an error formatted from the cancellation
// that no longer wraps it.
type cancellingLLM struct {
	cancel context.CancelFunc
	plain  bool
}

func (l cancellingLLM) CompleteJSON(ctx context.Context, _ []suggest.Message) (string, error) {
	l.cancel()
	if l.plain {
		return "", fmt.Errorf("chat call failed: %v", ctx.Err())
	}
	return "", ctx.Err()
}

// A run whose context ends is recorded as cancelled when a user cancelled it,
// and as failed otherwise, whatever error the stage it ended returned: a
// model call's, or a backfill's from the Weaviate client, neither of which
// wraps context.Canceled.
func TestRunStoppedByItsContext(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cancelled bool
		plain     bool
		opaque    bool
		status    string
	}{
		{"cancelled by a user", true, false, false, logstore.ComplexitySuggestionRunCancelled},
		{"cancelled by a user while a model call fails with its own error", true, true, false, logstore.ComplexitySuggestionRunCancelled},
		{"stopped for another reason", false, false, false, logstore.ComplexitySuggestionRunFailed},
		{"cancelled by a user while backfilling, behind an opaque error", true, false, true, logstore.ComplexitySuggestionRunCancelled},
		{"stopped while backfilling for another reason, behind an opaque error", false, false, true, logstore.ComplexitySuggestionRunFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(t)
			now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
			space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
			space.add("refund", 0, 12, now.Add(-time.Hour))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := settings("stopped", now)
			s.Cancelled = func(context.Context) bool { return tc.cancelled }
			if tc.opaque {
				s.Backfill = func(ctx context.Context, _, _ time.Time) (int, error) {
					cancel()
					return 0, fmt.Errorf("read captured requests: status code: -1: %s", ctx.Err())
				}
			}

			result, err := Run(ctx, space, store, texts, cancellingLLM{cancel: cancel, plain: tc.plain}, s)
			switch {
			case tc.cancelled:
				require.NoError(t, err, "a cancel is not a failure")
				assert.Empty(t, result.Run.Error)
			case tc.opaque:
				require.Error(t, err)
			default:
				require.ErrorIs(t, err, context.Canceled)
			}
			runs, err := store.ListComplexitySuggestionRuns(context.Background(), logstore.ComplexitySuggestionRunFilter{IDs: []string{"stopped"}}, 1)
			require.NoError(t, err)
			require.Len(t, runs, 1)
			assert.Equal(t, tc.status, runs[0].Status)
			assert.Empty(t, runs[0].Stage)
			require.NotNil(t, runs[0].FinishedAt)
			rows, err := store.ListComplexitySuggestions(context.Background(), logstore.ComplexitySuggestionFilter{})
			require.NoError(t, err)
			assert.Empty(t, rows, "nothing is written past the stage the run reached")
		})
	}
}

func TestPromptHashFollowsThePrompt(t *testing.T) {
	assert.Equal(t, PromptHash("", "llm"), PromptHash(suggest.DefaultInstructions(), "llm"), "empty means the shipped phrase prompt")
	assert.NotEqual(t, PromptHash("", "llm"), PromptHash("be brief", "llm"))
	assert.NotEqual(t, PromptHash("", "llm"), PromptHash("", "decision"), "the tier step is part of the prompt")
}

// A run reads and writes one router's suggestions: another router's, for the
// same request type, are neither matched nor superseded, and every row it
// writes names the router and the version it was computed against.
func TestRunIsScopedToItsRouter(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("refund", 0, 12, now.Add(-time.Hour))

	office := logstore.ComplexitySuggestion{ID: "office-refund", RouterID: "office", Status: logstore.ComplexitySuggestionStatusOpen,
		Tier: "MEDIUM", Category: "uncovered", Centroid: logstore.EncodeWarpVector(axis(0)), EmbeddingSignature: space.Namespace()}
	require.NoError(t, office.SetPhrases([]string{"refund help"}))
	require.NoError(t, store.UpsertComplexitySuggestions(ctx, []logstore.ComplexitySuggestion{office}))

	s := settings("run-eng", now)
	s.RouterVersion = "bank-v3"
	result, err := Run(ctx, space, store, texts, &kindLLM{}, s)
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.Run.Created, "the office router's row is not this router's to refresh")
	assert.Equal(t, "engineering", result.Run.RouterID)
	assert.Equal(t, "bank-v3", result.Run.RouterVersion)

	eng, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{RouterID: "engineering"})
	require.NoError(t, err)
	require.Len(t, eng, 1)
	assert.Equal(t, "bank-v3", eng[0].RouterVersion)
	kept, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{RouterID: "office"})
	require.NoError(t, err)
	require.Len(t, kept, 1)
	assert.Equal(t, logstore.ComplexitySuggestionStatusOpen, kept[0].Status, "another router's open suggestion is not superseded")

	_, err = Run(ctx, space, store, texts, &kindLLM{}, Settings{RunID: "no-router", Lookback: time.Hour, MaxClustered: 10})
	require.ErrorContains(t, err, "needs a router")
}

// A window shorter than the lookback reads only its own span, and the run
// records where it started.
func TestRunReadsItsWindow(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("refund", 0, 12, now.Add(-time.Hour))
	space.add("invoice", 1, 12, now.Add(-3*24*time.Hour))

	s := settings("run-day", now)
	s.Window = 24 * time.Hour
	result, err := Run(ctx, space, store, texts, &kindLLM{}, s)
	require.NoError(t, err)
	assert.EqualValues(t, 12, result.Run.RequestsRead, "only the last day's requests")
	assert.True(t, result.Run.WindowStart.Equal(now.Add(-24*time.Hour)))

	s = settings("run-long", now)
	s.Window = 30 * 24 * time.Hour
	result, err = Run(ctx, space, store, texts, &kindLLM{}, s)
	require.NoError(t, err)
	assert.True(t, result.Run.WindowStart.Equal(now.Add(-7*24*time.Hour)), "a window past the lookback reads the lookback")
}

// listedTraffic is a traffic source other than the capture scan, as the logs
// will be with several routers.
type listedTraffic struct{ requests []complexity.CapturedRequest }

func (l listedTraffic) EachRequest(_ context.Context, _, _ time.Time, fn func([]complexity.CapturedRequest) error) error {
	return fn(l.requests)
}

func TestRunReadsTheTrafficItIsGiven(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("refund", 0, 12, now.Add(-time.Hour)) // in the space, but not this router's
	var mine []complexity.CapturedRequest
	for j := range 9 {
		mine = append(mine, complexity.CapturedRequest{LogID: fmt.Sprintf("invoice-%d", j), Vector: nearAxis(1, j), At: now.Add(-time.Hour)})
	}
	s := settings("run-listed", now)
	s.Traffic = listedTraffic{mine}
	result, err := Run(ctx, space, store, texts, &kindLLM{}, s)
	require.NoError(t, err)
	assert.EqualValues(t, 9, result.Run.RequestsRead)
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{RouterID: "engineering"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, []string{"where is my invoice"}, rows[0].Phrases())
}

func TestEstimateRunBoundsClusteringAndModelCalls(t *testing.T) {
	s := Settings{MaxClustered: 1000, MinClusterRequests: 6}
	small := EstimateRun(600, s)
	assert.Equal(t, Estimate{Requests: 600, Clustered: 600, MaxModelCalls: 300}, small, "600 requests make at most 100 clusters of 6, three calls each")

	big := EstimateRun(15000, s)
	assert.Equal(t, 1000, big.Clustered)
	assert.Equal(t, 10000, big.Assigned, "assignment stops at ten times the clustered sample")
	assert.Equal(t, 4000, big.Skipped)
	assert.Equal(t, 3*DefaultMaxSuggestedClusters, big.MaxModelCalls, "never more clusters sent than the per-run bound")
	assert.Zero(t, EstimateRun(0, s).MaxModelCalls)

	// An unset minimum is the clusterer's own default, which is what Run uses.
	for _, unset := range []int{0, -3} {
		assert.Equal(t, small, EstimateRun(600, Settings{MaxClustered: 1000, MinClusterRequests: unset}),
			"min cluster requests %d estimates like the clusterer's default", unset)
	}
}

// The smallest cluster kept is the configured minimum, raised to the share
// of the window's requests when that is larger.
func TestClusterFloor(t *testing.T) {
	assert.Equal(t, suggest.DefaultMinClusterRequests, ClusterFloor(0, -1, 100_000), "a negative share: the default minimum alone")
	assert.Equal(t, 6, ClusterFloor(6, 0, 5_000), "a small window keeps the minimum")
	assert.Equal(t, 27, ClusterFloor(6, 0, 26_739), "0.1% of a WildChat-sized window by default")
	assert.Equal(t, 1_000, ClusterFloor(6, 0, 1_000_000))
	assert.Equal(t, 11, ClusterFloor(6, 0.0004, 26_739), "an explicit share overrides the default")
}

// A share of the window raises the floor: on 21 requests a 40% share sets it
// to 9 and keeps both clusters; 50% sets it to 11 and drops the invoice one (9).
func TestRunScalesTheClusterFloorWithTheWindow(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("refund", 0, 12, now.Add(-time.Hour))
	space.add("invoice", 1, 9, now.Add(-time.Hour))
	s := settings("scaled", now)
	s.MinClusterShare = 0.4

	result, err := Run(ctx, space, store, texts, &kindLLM{}, s)
	require.NoError(t, err)
	assert.EqualValues(t, 2, result.Run.Clusters, "ceil(0.4 x 21) = 9 beats the minimum of 6 and keeps both")
	s.MinClusterShare = 0.5
	s.RunID = "scaled-more"
	result, err = Run(ctx, space, store, texts, &kindLLM{}, s)
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.Run.Clusters, "ceil(0.5 x 21) = 11: only the 12-request cluster clears it")
}

// A run suggests no more phrases than the bank has room for: with room for
// one, only the heaviest cluster is asked about.
func TestRunStaysWithinThePhraseBank(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("refund", 0, 12, now.Add(-time.Hour))
	space.add("invoice", 1, 9, now.Add(-time.Hour))
	llm := &kindLLM{}
	s := settings("full", now)
	s.MaxPhrases = 2 // one live phrase, room for one more
	s.Workers = 1

	result, err := Run(ctx, space, store, texts, llm, s)
	require.NoError(t, err)
	assert.EqualValues(t, 2, result.Run.Clusters)
	assert.EqualValues(t, 1, result.Run.ClustersAsked)
	assert.EqualValues(t, 1, result.Run.Created)
	assert.Zero(t, llm.calls["invoice"], "the lighter cluster is not asked about")
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, []string{"where is my refund"}, rows[0].Phrases())
}

// A cluster's phrase count is only known once it is asked, so a run asks
// heaviest first and stops once the bank's room is used: here the refund
// cluster's two phrases fill a room of two, and the invoice cluster is not
// asked about at all rather than asked and thrown away.
func TestRunStopsAskingOnceTheBankIsFull(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("refund", 0, 12, now.Add(-time.Hour))
	space.add("invoice", 1, 9, now.Add(-time.Hour))
	llm := &kindLLM{phrases: map[string][]string{"refund": {"where is my refund", "refund status for my order"}}}
	s := settings("fill", now)
	s.MaxPhrases = 3 // one live phrase, room for two more
	s.Workers = 1

	result, err := Run(ctx, space, store, texts, llm, s)
	require.NoError(t, err)
	assert.Zero(t, llm.calls["invoice"], "the bank is full before the lighter cluster's turn")
	assert.EqualValues(t, 1, result.Run.ClustersAsked)
	assert.EqualValues(t, 1, result.Run.ClustersOverLimit, "a cluster the bank had no room for is past the run's limits")
	assert.EqualValues(t, 1, result.Run.Created)
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, []string{"where is my refund", "refund status for my order"}, rows[0].Phrases())
}

// The room bounds the phrases a run writes, not the clusters it asks: a
// heavier cluster that comes back covered writes none, so the room is still
// there for the next one.
func TestRunAsksPastACoveredClusterWhileThereIsRoom(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{
		{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)},
		{Tier: "SIMPLE", Text: "where is my refund", Vector: axis(0)},
	}}
	space.add("refund", 0, 12, now.Add(-time.Hour))
	space.add("invoice", 1, 9, now.Add(-time.Hour))
	llm := &kindLLM{}
	s := settings("covered-first", now)
	s.MaxPhrases = 3 // two live phrases, room for one more
	s.Workers = 1

	result, err := Run(ctx, space, store, texts, llm, s)
	require.NoError(t, err)
	assert.EqualValues(t, 1, result.Run.ClustersCovered, "the refund cluster's phrase is already live")
	assert.NotZero(t, llm.calls["invoice"], "the room the covered cluster left is used")
	assert.EqualValues(t, 2, result.Run.ClustersAsked)
	assert.EqualValues(t, 1, result.Run.Created)
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, []string{"where is my invoice"}, rows[0].Phrases())
}

// The share floor counts only requests a run can place in a cluster: on a
// window far past the assignment bound, it is a share of the clustered and
// counted requests, not of the whole window, so it never outgrows them.
func TestClusterFloorCountsOnlyRepresentedRequests(t *testing.T) {
	assert.Equal(t, 1100, representedRequests(1_000_000, 100), "the sample and ten times it")
	assert.Equal(t, 500, representedRequests(500, 100), "a window under the bound is all represented")
	// A million requests, 1,100 of them clustered or counted: 0.1% of those
	// is a floor of 2, where 0.1% of the window (1,000) would leave room for
	// one cluster in what can ever be grouped.
	e := EstimateRun(1_000_000, Settings{MaxClustered: 100, MinClusterRequests: 1})
	assert.Equal(t, 3*DefaultMaxSuggestedClusters, e.MaxModelCalls)
}

// Trimming counts phrases: a cluster whose phrases would overflow the room is
// dropped and counted; outcomes that add no phrases are always kept.
func TestWithinRoom(t *testing.T) {
	sg := func(cat suggest.Category, phrases ...string) suggest.Suggestion {
		return suggest.Suggestion{Category: cat, Phrases: phrases}
	}
	in := []suggest.Suggestion{
		sg(suggest.CategoryUncovered, "a", "b"),
		sg(suggest.CategoryCovered),
		sg(suggest.CategoryWeak, "c", "d"),
		sg(suggest.CategoryMixed, "e"),
	}
	var dropped int64
	got := WithinRoom(append([]suggest.Suggestion(nil), in...), 3, &dropped)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"a", "b"}, got[0].Phrases)
	assert.Equal(t, suggest.CategoryCovered, got[1].Category, "adds no phrases, kept")
	assert.Equal(t, []string{"e"}, got[2].Phrases, "two would overflow; the next one-phrase cluster fits")
	assert.EqualValues(t, 1, dropped)

	dropped = 0
	assert.Len(t, WithinRoom(append([]suggest.Suggestion(nil), in...), -1, &dropped), 4, "no bound")
	assert.Zero(t, dropped)
}

// Two clusters can be written the same phrase (one prompt template split in
// two, say). The heavier cluster keeps it; the lighter one, left with no
// phrase of its own, is covered by it and is not suggested twice.
func TestRunDoesNotSuggestOnePhraseTwice(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	space := &fakeSpace{live: []complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "write a poem", Vector: axis(5)}}}
	space.add("refund", 0, 12, now.Add(-time.Hour))
	// Another request type the model writes the same phrase for: its texts
	// read "refundtwo", which kindLLM answers as a refund.
	space.add("refundtwo", 2, 9, now.Add(-time.Hour))

	result, err := Run(ctx, space, store, texts, &kindLLM{}, settings("dupes", now))
	require.NoError(t, err)
	assert.EqualValues(t, 2, result.Run.ClustersAsked)
	assert.EqualValues(t, 1, result.Run.Created, "one suggestion for the phrase")
	assert.EqualValues(t, 1, result.Run.ClustersCovered, "the lighter cluster is covered by the heavier one's phrase")
	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.EqualValues(t, 12, rows[0].RequestCount, "the heavier cluster keeps it")
}

// A suggestion that never had phrases (a misrouted cluster whose phrases
// repeat a live one) is not mistaken for a repeat and stays actionable.
func TestWithoutRepeatedPhrasesKeepsSuggestionsWithoutPhrases(t *testing.T) {
	got := withoutRepeatedPhrases([]suggest.Suggestion{
		{Category: suggest.CategoryUncovered, Phrases: []string{"where is my refund"}},
		{Category: suggest.CategoryMisrouted},
		{Category: suggest.CategoryWeak, Phrases: []string{"Where is my refund "}},
	})
	assert.Equal(t, suggest.CategoryUncovered, got[0].Category)
	assert.Equal(t, suggest.CategoryMisrouted, got[1].Category, "no phrases of its own to repeat")
	assert.Equal(t, suggest.CategoryCovered, got[2].Category, "its only phrase is the first one's")
	assert.Empty(t, got[2].Phrases)
}

// A suggestion the room has no space for reserves nothing: a lighter one that
// was written one of its phrases keeps it, instead of being marked covered by
// a suggestion that is then dropped.
// A lighter suggestion whose phrases a heavier one already took for another
// tier is a conflict, not coverage: it is left out of the run, so the stored
// suggestion its cluster matches stays as it was instead of being superseded.
// Taken for the same tier, it is covered, as before.
func TestFitSuggestionsLeavesCrossTierConflictsUntouched(t *testing.T) {
	heavy := &suggest.Cluster{}
	light := &suggest.Cluster{}
	in := []suggest.Suggestion{
		{Cluster: heavy, Tier: "SIMPLE", Category: suggest.CategoryUncovered, Phrases: []string{"refund status"}},
		{Cluster: light, Tier: "MEDIUM", Category: suggest.CategoryMisrouted, Phrases: []string{"Refund status "}},
	}
	var dropped int64
	got := FitSuggestions(in, -1, &dropped)
	require.Len(t, got, 1, "the conflicting suggestion is left out")
	assert.Same(t, heavy, got[0].Cluster)
	assert.Zero(t, dropped, "a conflict is not a suggestion over the bank's room")

	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	row := logstore.ComplexitySuggestion{ID: "light-row", Status: logstore.ComplexitySuggestionStatusOpen, Tier: "MEDIUM", EmbeddingSignature: "ns"}
	plan := matchPlan{matched: map[*suggest.Cluster]*logstore.ComplexitySuggestion{light: &row}, claimed: map[string]bool{row.ID: true}}
	writes, _, _, superseded := reconcile(got, plan, []logstore.ComplexitySuggestion{row}, rowScope{namespace: "ns"}, now)
	assert.Zero(t, superseded)
	for _, w := range writes {
		assert.NotEqual(t, row.ID, w.Suggestion.ID, "the conflicting cluster's stored suggestion is not written")
	}

	in = []suggest.Suggestion{
		{Cluster: heavy, Tier: "SIMPLE", Category: suggest.CategoryUncovered, Phrases: []string{"refund status"}},
		{Cluster: light, Tier: "SIMPLE", Category: suggest.CategoryWeak, Phrases: []string{"Refund status "}},
	}
	got = FitSuggestions(in, -1, &dropped)
	require.Len(t, got, 2)
	assert.Equal(t, suggest.CategoryCovered, got[1].Category, "taken for the same tier, it is covered")
}

func TestFitSuggestionsDropsBeforeDeduplicating(t *testing.T) {
	in := []suggest.Suggestion{
		{Category: suggest.CategoryUncovered, Phrases: []string{"refund status", "refund for my order"}},
		{Category: suggest.CategoryWeak, Phrases: []string{"Refund status "}},
	}
	var dropped int64
	got := FitSuggestions(in, 1, &dropped)
	require.Len(t, got, 1)
	assert.Equal(t, suggest.CategoryWeak, got[0].Category, "the lighter suggestion fits and is kept")
	assert.Equal(t, []string{"Refund status "}, got[0].Phrases)
	assert.EqualValues(t, 1, dropped)

	// With room for both, the heavier keeps the shared phrase and the lighter
	// is covered by it, as before.
	in = []suggest.Suggestion{
		{Category: suggest.CategoryUncovered, Phrases: []string{"refund status", "refund for my order"}},
		{Category: suggest.CategoryWeak, Phrases: []string{"Refund status "}},
	}
	dropped = 0
	got = FitSuggestions(in, 5, &dropped)
	require.Len(t, got, 2)
	assert.Equal(t, suggest.CategoryCovered, got[1].Category)
	assert.Zero(t, dropped)
}
