package routing

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggestrun"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func suggestionTestStore(t *testing.T) logstore.LogStore {
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

func suggestionTestSettings() *configstore.ComplexitySuggestionsConfig {
	return &configstore.ComplexitySuggestionsConfig{
		Enabled: true, SampleRate: 1, LookbackDays: 7, MaxClusteredRequests: 1000, MinClusterRequests: 2,
		Provider: "openai", Model: "gpt-test",
	}
}

// suggestionPlugin is a ready plugin capturing every request, with
// suggestions kept in a real log store and the model answering one fixed
// suggestion.
func suggestionPlugin(t *testing.T) (*RoutingPlugin, logstore.LogStore, *[]string) {
	t.Helper()
	p := newCapturePlugin(t, suggestionTestSettings())
	p.startComplexitySuggestions(context.Background(), 0)
	store := suggestionTestStore(t)
	p.SetComplexitySuggestionStore(store, func(_ context.Context, ids []string, _ int) (map[string]string, error) {
		out := map[string]string{}
		for _, id := range ids {
			out[id] = "simple request"
		}
		return out, nil
	})
	var mu sync.Mutex
	var models []string
	p.SetChatRequestExecutor(func(_ *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		mu.Lock()
		models = append(models, string(req.Provider)+"/"+req.Model)
		mu.Unlock()
		if req.Input[0].Role != schemas.ChatMessageRoleSystem || !strings.Contains(*req.Input[1].Content.ContentStr, "simple request") {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "unexpected prompt"}}
		}
		return chatResponseWithText(`{"coherent":true,"tier":"SIMPLE","confidence":"high","reason":"short asks","phrases":["a short question"]}`), nil
	})
	for i := range 6 {
		classifyAs(t, p, fmt.Sprintf("log-%d", i))
	}
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 6 }, 2*time.Second, 5*time.Millisecond)
	return p, store, &models
}

func waitForRun(t *testing.T, store logstore.LogStore) logstore.ComplexitySuggestionRun {
	t.Helper()
	var run logstore.ComplexitySuggestionRun
	require.Eventually(t, func() bool {
		runs, err := store.ListComplexitySuggestionRuns(context.Background(), logstore.ComplexitySuggestionRunFilter{}, 1)
		if err != nil || len(runs) == 0 || runs[0].FinishedAt == nil {
			return false
		}
		run = runs[0]
		return true
	}, 10*time.Second, 10*time.Millisecond)
	return run
}

// A node with no job runner runs a due slot itself, once: the next check
// finds the slot's run and starts nothing.
func TestComplexitySuggestionsRunsADueSlotOnce(t *testing.T) {
	p, store, models := suggestionPlugin(t)
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, now))
	run := waitForRun(t, store)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status, run.Error)
	assert.Equal(t, logstore.ComplexitySuggestionTriggerSchedule, run.TriggeredBy)
	assert.EqualValues(t, 6, run.RequestsRead)
	assert.Equal(t, "openai/gpt-test", run.Model)
	slot, err := suggestionTestSettings().EffectiveSchedule().LastSlot(now)
	require.NoError(t, err)
	assert.Equal(t, complexitySuggestionSlotRunID(complexity.DefaultRouterID, slot), run.ID)
	assert.NotEmpty(t, *models)
	assert.Equal(t, "openai/gpt-test", (*models)[0], "the suggestions block's own model")

	require.NoError(t, p.scheduleComplexitySuggestions(ctx, now.Add(time.Minute)))
	p.suggestions.wg.Wait()
	runs, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{RouterID: complexity.DefaultRouterID}, 10)
	require.NoError(t, err)
	assert.Len(t, runs, 1, "the slot already has its run")
}

type recordingQueue struct {
	ids   []string
	metas []string
}

func (q *recordingQueue) EnqueuePartitioned(_ context.Context, id, kind, partition, metadata, _ string) error {
	if kind != ComplexitySuggestionsJobKind || partition != complexitySuggestionPartition(complexity.DefaultRouterID) {
		return fmt.Errorf("unexpected kind %s / partition %s", kind, partition)
	}
	q.ids = append(q.ids, id)
	q.metas = append(q.metas, metadata)
	return nil
}

type inFlightJobs struct{ job *tables.TableSidekiqJob }

func (j *inFlightJobs) GetInFlightSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return j.job, nil
}

// With a job runner, a due slot is queued under the slot's ID, and not again
// while a run is in flight; the queued job runs it.
func TestComplexitySuggestionsQueuesDueSlots(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	queue, jobs := &recordingQueue{}, &inFlightJobs{}
	p.SetComplexitySuggestionJobs(queue, jobs)
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, now))
	require.Len(t, queue.ids, 1)
	slot, err := suggestionTestSettings().EffectiveSchedule().LastSlot(now)
	require.NoError(t, err)
	assert.Equal(t, complexitySuggestionSlotRunID(complexity.DefaultRouterID, slot), queue.ids[0])

	jobs.job = &tables.TableSidekiqJob{ID: queue.ids[0]}
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, now))
	assert.Len(t, queue.ids, 1, "a run in flight is not queued twice")

	meta, err := p.RunComplexitySuggestionsJob(ctx, tables.TableSidekiqJob{ID: queue.ids[0], Metadata: queue.metas[0]}, nil)
	require.NoError(t, err)
	assert.Contains(t, meta, "Read 6 requests")
	run := waitForRun(t, store)
	assert.Equal(t, queue.ids[0], run.ID)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status)
}

// A slot's run ID is the run row's primary key, a varchar(36), and the job ID
// that keeps two nodes from queueing the same slot. So it is a UUID, and the
// same one every time the slot is due, from any plugin instance.
func TestComplexitySuggestionSlotRunIDIsAStableUUID(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	queued := func(at time.Time) string {
		p, _, _ := suggestionPlugin(t)
		queue := &recordingQueue{}
		p.SetComplexitySuggestionJobs(queue, &inFlightJobs{})
		require.NoError(t, p.scheduleComplexitySuggestions(ctx, at))
		require.Len(t, queue.ids, 1)
		return queue.ids[0]
	}
	id := queued(now)
	_, err := uuid.Parse(id)
	require.NoError(t, err, "slot run ID %q is not a UUID", id)
	assert.Len(t, id, 36)
	assert.Equal(t, id, queued(now), "the same slot gets the same ID on another instance")
	assert.NotEqual(t, id, queued(now.Add(48*time.Hour)), "a later slot gets its own ID")
	slot, err := suggestionTestSettings().EffectiveSchedule().LastSlot(now)
	require.NoError(t, err)
	assert.Equal(t, id, complexitySuggestionSlotRunID(complexity.DefaultRouterID, slot))
	assert.NotEqual(t, id, complexitySuggestionSlotRunID("support", slot), "another router due at the same slot gets its own job")
}

// A job that finds suggestions turned off by the time it runs has nothing to
// do, which is not a failure; and nothing is scheduled while they are off.
func TestComplexitySuggestionsJobWhenTurnedOff(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(nil)))
	meta, err := p.RunComplexitySuggestionsJob(context.Background(), tables.TableSidekiqJob{ID: "job-1", Metadata: `{"run_id":"job-1"}`}, nil)
	require.NoError(t, err)
	var decoded complexitySuggestionJobMeta
	require.NoError(t, sonic.UnmarshalString(meta, &decoded))
	assert.Contains(t, decoded.Message, "off")
	assert.Equal(t, decoded.Message, SummarizeComplexitySuggestionsJob(meta).Message)

	require.NoError(t, p.scheduleComplexitySuggestions(context.Background(), time.Now()))
	p.suggestions.wg.Wait()
	runs, err := store.ListComplexitySuggestionRuns(context.Background(), logstore.ComplexitySuggestionRunFilter{}, 10)
	require.NoError(t, err)
	assert.Empty(t, runs)
}

// A run started by hand runs outside the schedule under a fresh UUID, is
// recorded as manual, and is refused while another is in flight or when suggestions are off.
func TestComplexitySuggestionsRunNow(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	ctx := context.Background()
	runID, err := p.StartComplexitySuggestionRunNow(ctx, complexity.DefaultRouterID, 0)
	require.NoError(t, err)
	_, err = uuid.Parse(runID)
	require.NoError(t, err, "a manual run ID is the run row's varchar(36) key, so a bare UUID; got %q", runID)
	assert.Len(t, runID, 36)
	run := waitForRun(t, store)
	assert.Equal(t, runID, run.ID)
	assert.Equal(t, logstore.ComplexitySuggestionTriggerManual, run.TriggeredBy)
	assert.Equal(t, complexity.DefaultRouterID, run.RouterID)
	assert.Equal(t, p.ComplexitySuggestionRouterVersion(complexity.DefaultRouterID), run.RouterVersion)
	assert.NotEmpty(t, run.RouterVersion)

	queue := &recordingQueue{}
	p.SetComplexitySuggestionJobs(queue, &inFlightJobs{job: &tables.TableSidekiqJob{ID: "busy", Metadata: `{"run_id":"busy","router_id":"default"}`}})
	_, err = p.StartComplexitySuggestionRunNow(ctx, complexity.DefaultRouterID, 0)
	require.ErrorIs(t, err, ErrComplexitySuggestionRunInFlight)
	assert.Empty(t, queue.ids)

	// Another router's queued run is no reason for this one to wait.
	p.SetComplexitySuggestionJobs(queue, &inFlightJobs{job: &tables.TableSidekiqJob{ID: "other", Metadata: `{"run_id":"other","router_id":"office"}`}})
	day, err := p.StartComplexitySuggestionRunNow(ctx, complexity.DefaultRouterID, 24*time.Hour)
	require.NoError(t, err)
	require.Len(t, queue.ids, 1)
	assert.Equal(t, day, queue.ids[0])
	assert.Contains(t, queue.metas[0], `"window_seconds":86400`)

	_, err = p.StartComplexitySuggestionRunNow(ctx, complexity.DefaultRouterID, 30*24*time.Hour)
	require.ErrorIs(t, err, ErrComplexitySuggestionWindow, "a window past the 7-day lookback reads vectors that are gone")
	_, err = p.StartComplexitySuggestionRunNow(ctx, "office", 0)
	require.ErrorIs(t, err, ErrUnknownComplexityRouter)

	require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(nil)))
	_, err = p.StartComplexitySuggestionRunNow(ctx, complexity.DefaultRouterID, 0)
	require.ErrorIs(t, err, ErrComplexitySuggestionsOff)
}

// Testing a phrase uses the serving bank and the router's own floor.
func TestComplexitySuggestionsTestPhrases(t *testing.T) {
	p, _, _ := suggestionPlugin(t)
	got, err := p.TestComplexitySuggestionPhrases(context.Background(), complexity.DefaultRouterID,
		[]complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "simple request"}},
		[]suggestrun.TestExample{{LogID: "log-0", Text: "simple request"}})
	require.NoError(t, err)
	require.Len(t, got.Examples, 1)
	assert.Equal(t, "SIMPLE", got.Examples[0].Current.Tier)
	assert.Equal(t, "simple exemplar", got.Phrases[0].Nearest.Phrase, "the tested phrase duplicates a live one")
	assert.InDelta(t, 1, got.Phrases[0].Nearest.Similarity, 1e-6)
	assert.Equal(t, 1, p.ComplexitySuggestionMessageHistoryCount(complexity.DefaultRouterID))
}

// An estimate counts the window's captured requests and bounds what a run of
// the current settings would do with them.
func TestComplexitySuggestionsEstimate(t *testing.T) {
	p, _, _ := suggestionPlugin(t)
	ctx := context.Background()
	got, err := p.EstimateComplexitySuggestionRun(ctx, complexity.DefaultRouterID, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 6, got.Requests)
	assert.Equal(t, 6, got.Clustered)
	assert.Equal(t, 6, got.MaxModelCalls, "6 requests make at most 3 clusters of 2, two calls each")

	_, err = p.EstimateComplexitySuggestionRun(ctx, complexity.DefaultRouterID, 8*24*time.Hour)
	require.ErrorIs(t, err, ErrComplexitySuggestionWindow)
	_, err = p.EstimateComplexitySuggestionRun(ctx, "office", 0)
	require.ErrorIs(t, err, ErrUnknownComplexityRouter)
}
