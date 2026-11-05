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
		runs, err := store.ListComplexitySuggestionRuns(context.Background(), nil, 1)
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
	assert.Equal(t, complexitySuggestionSlotRunID(slot), run.ID)
	assert.NotEmpty(t, *models)
	assert.Equal(t, "openai/gpt-test", (*models)[0], "the suggestions block's own model")

	require.NoError(t, p.scheduleComplexitySuggestions(ctx, now.Add(time.Minute)))
	p.suggestions.wg.Wait()
	runs, err := store.ListComplexitySuggestionRuns(ctx, nil, 10)
	require.NoError(t, err)
	assert.Len(t, runs, 1, "the slot already has its run")
}

type recordingQueue struct {
	ids   []string
	metas []string
}

func (q *recordingQueue) EnqueuePartitioned(_ context.Context, id, kind, partition, metadata, _ string) error {
	if kind != ComplexitySuggestionsJobKind || partition != ComplexitySuggestionsJobKind {
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
	assert.Equal(t, complexitySuggestionSlotRunID(slot), queue.ids[0])

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
	runs, err := store.ListComplexitySuggestionRuns(context.Background(), nil, 10)
	require.NoError(t, err)
	assert.Empty(t, runs)
}
