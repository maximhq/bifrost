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
	mu    sync.Mutex
	calls map[string]int
	fail  map[string]bool
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
			return fmt.Sprintf(`{"coherent":true,"tier":"SIMPLE","confidence":"high","reason":"%s questions","phrases":["where is my %s"]}`, kind, kind), nil
		}
	}
	return "", errors.New("unexpected prompt")
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
		MaxClustered: 1000, MinClusterRequests: 6, Model: "openai/gpt-5", Seed: 1, Cost: func() float64 { return 0.02 }}
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
	assert.Equal(t, PromptHash("", ""), result.Run.PromptHash)
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
}

func TestPromptHashFollowsThePrompt(t *testing.T) {
	assert.Equal(t, PromptHash("", ""), PromptHash(suggest.DefaultInstructions(), complexity.DefaultLLMClassifierGuidance()), "empty means the shipped prompt")
	assert.NotEqual(t, PromptHash("", ""), PromptHash("be brief", ""))
	assert.NotEqual(t, PromptHash("", ""), PromptHash("", "SIMPLE means short"))
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
	assert.Equal(t, Estimate{Requests: 600, Clustered: 600, MaxModelCalls: 200}, small, "600 requests make at most 100 clusters of 6")

	big := EstimateRun(15000, s)
	assert.Equal(t, 1000, big.Clustered)
	assert.Equal(t, 10000, big.Assigned, "assignment stops at ten times the clustered sample")
	assert.Equal(t, 4000, big.Skipped)
	assert.Equal(t, 2*DefaultMaxSuggestedClusters, big.MaxModelCalls, "never more clusters sent than the per-run bound")
	assert.Zero(t, EstimateRun(0, s).MaxModelCalls)

	// An unset minimum is the clusterer's own default, which is what Run uses.
	for _, unset := range []int{0, -3} {
		assert.Equal(t, small, EstimateRun(600, Settings{MaxClustered: 1000, MinClusterRequests: unset}),
			"min cluster requests %d estimates like the clusterer's default", unset)
	}
}
