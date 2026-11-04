package routing

import (
	"context"
	"errors"
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

// hourlyFrom is a schedule that runs every hour from start.
func hourlyFrom(start time.Time) *configstore.ComplexitySuggestionsSchedule {
	return &configstore.ComplexitySuggestionsSchedule{Every: "1h", StartAt: &start}
}

// scheduledSuggestionPlugin is a suggestion plugin on an hourly schedule from
// start, with schedule state in a real config store when one is given.
func scheduledSuggestionPlugin(t *testing.T, start time.Time, schedules ComplexitySuggestionScheduleStore) (*RoutingPlugin, logstore.LogStore) {
	t.Helper()
	p, store, _ := suggestionPlugin(t)
	settings := suggestionTestSettings()
	settings.Schedule = hourlyFrom(start)
	require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(settings)))
	require.Eventually(t, func() bool { return p.semanticClassifier.Status().State == complexity.SemanticStatusReady }, 2*time.Second, 5*time.Millisecond)
	p.SetComplexitySuggestionSchedules(schedules)
	return p, store
}

// suggestionScheduleStore is a real SQLite config store, which keeps schedule
// state for every plugin given it, as one shared by gateway nodes does.
func suggestionScheduleStore(t *testing.T) ComplexitySuggestionScheduleStore {
	t.Helper()
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })
	schedules, ok := store.(ComplexitySuggestionScheduleStore)
	require.True(t, ok, "the config store keeps suggestion schedules")
	return schedules
}

func runsOf(t *testing.T, store logstore.LogStore) []logstore.ComplexitySuggestionRun {
	t.Helper()
	runs, err := store.ListComplexitySuggestionRuns(context.Background(), nil, 50)
	require.NoError(t, err)
	return runs
}

// A new schedule waits for its next run rather than running at once; the
// node then runs it itself (no job runner), once, and waits for the next.
func TestComplexitySuggestionsScheduleRunsAtItsNextRun(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	p, store := scheduledSuggestionPlugin(t, start, suggestionScheduleStore(t))
	ctx := context.Background()

	require.NoError(t, p.scheduleComplexitySuggestions(ctx, start.Add(90*time.Minute)))
	assert.Empty(t, runsOf(t, store), "a new schedule waits for its next run")

	due := start.Add(2 * time.Hour)
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, due.Add(-time.Minute)))
	assert.Empty(t, runsOf(t, store), "not due yet")
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, due.Add(20*time.Second)))
	run := waitForRun(t, store)
	assert.Equal(t, complexitySuggestionSlotRunID(due), run.ID)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status, run.Error)
	assert.Equal(t, logstore.ComplexitySuggestionTriggerSchedule, run.TriggeredBy)
	assert.EqualValues(t, 6, run.RequestsRead)

	require.NoError(t, p.scheduleComplexitySuggestions(ctx, due.Add(time.Minute)))
	p.suggestions.wg.Wait()
	assert.Len(t, runsOf(t, store), 1, "the run is taken; the next one is an hour on")
}

// Nodes sharing a config store claim a due run once between them: one records
// it as queued and queues it under the slot's ID; the queued job runs it.
func TestComplexitySuggestionsDueRunIsClaimedOnce(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	schedules := suggestionScheduleStore(t)
	queue := &recordingQueue{}
	var nodes []*RoutingPlugin
	var store logstore.LogStore
	for i := range 3 {
		p, s := scheduledSuggestionPlugin(t, start, schedules)
		p.SetComplexitySuggestionJobs(queue, &inFlightJobs{})
		nodes = append(nodes, p)
		if i == 0 {
			store = s
		}
	}
	// Every node shares the first node's log store, as nodes share one.
	for _, p := range nodes[1:] {
		p.SetComplexitySuggestionStore(store, func(context.Context, []string, int) (map[string]string, error) { return nil, nil })
	}
	ctx := context.Background()
	require.NoError(t, nodes[0].scheduleComplexitySuggestions(ctx, start.Add(30*time.Minute)))

	due := start.Add(time.Hour)
	var wg sync.WaitGroup
	for _, p := range nodes {
		wg.Go(func() { assert.NoError(t, p.scheduleComplexitySuggestions(ctx, due.Add(5*time.Second))) })
	}
	wg.Wait()
	require.Len(t, queue.ids, 1, "exactly one node takes the run")
	assert.Equal(t, complexitySuggestionSlotRunID(due), queue.ids[0])
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunQueued, runs[0].Status, "visible as queued before a job runner picks it up")

	meta, err := nodes[0].RunComplexitySuggestionsJob(ctx, tables.TableSidekiqJob{ID: queue.ids[0], Metadata: queue.metas[0]}, nil)
	require.NoError(t, err)
	assert.Contains(t, meta, "Read 6 requests")
	run := waitForRun(t, store)
	assert.Equal(t, queue.ids[0], run.ID)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status)
}

// A gateway that was down for several runs catches up with one, and the next
// run is counted from now; a changed schedule moves the next run.
func TestComplexitySuggestionsScheduleCatchesUpOnceAndFollowsChanges(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	schedules := suggestionScheduleStore(t)
	p, store := scheduledSuggestionPlugin(t, start, schedules)
	queue := &recordingQueue{}
	p.SetComplexitySuggestionJobs(queue, &inFlightJobs{})
	ctx := context.Background()
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, start.Add(10*time.Minute)))

	back := start.Add(5*time.Hour + 20*time.Minute) // runs at 1:00 to 5:00 were missed
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, back))
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, back.Add(time.Second)))
	require.Len(t, queue.ids, 1, "one catch-up run for the whole outage")
	assert.Equal(t, complexitySuggestionSlotRunID(start.Add(time.Hour)), queue.ids[0], "the run due first")
	row, err := schedules.GetComplexitySuggestionSchedule(ctx, complexitySuggestionScheduleKey)
	require.NoError(t, err)
	assert.True(t, row.NextRunAt.Equal(start.Add(6*time.Hour)), "next run counted from now: %s", row.NextRunAt)
	assert.Len(t, runsOf(t, store), 1)

	// A new schedule: its next run replaces the old one, and nothing runs at once.
	settings := suggestionTestSettings()
	settings.Schedule = &configstore.ComplexitySuggestionsSchedule{Every: "1d", StartAt: &start}
	require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(settings)))
	require.Eventually(t, func() bool { return p.semanticClassifier.Status().State == complexity.SemanticStatusReady }, 2*time.Second, 5*time.Millisecond)
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, back.Add(time.Minute)))
	row, err = schedules.GetComplexitySuggestionSchedule(ctx, complexitySuggestionScheduleKey)
	require.NoError(t, err)
	assert.True(t, row.NextRunAt.Equal(start.Add(24*time.Hour)), "next run from the new schedule: %s", row.NextRunAt)
	assert.Len(t, queue.ids, 1)
}

// A run due while the router is still warming, or while this node is still
// running one (no job runner), is recorded as skipped with the reason, and
// the schedule moves on.
func TestComplexitySuggestionsDueRunThatCannotStartIsSkipped(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	p, store := scheduledSuggestionPlugin(t, start, suggestionScheduleStore(t))
	ctx := context.Background()
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, start.Add(10*time.Minute)))

	p.suggestions.inline.Store(true) // a run of this node's is still going
	due := start.Add(time.Hour)
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, due.Add(time.Second)))
	p.suggestions.inline.Store(false)
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status)
	assert.Contains(t, runs[0].Error, "still in progress")
	assert.Equal(t, complexitySuggestionSlotRunID(due), runs[0].ID)

	warming := complexity.NewSemanticClassifier(ctx, bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(func() { _ = warming.Close() })
	// A router's classifier has the gateway's vector store from startup,
	// warming or not; without one it would read as having nowhere to capture.
	warming.SetConfiguredStore(&sharedTestStore{})
	serving := p.semanticClassifier
	p.semanticClassifier = warming
	t.Cleanup(func() { p.semanticClassifier = serving })
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, due.Add(time.Hour+time.Second)))
	var skipped *logstore.ComplexitySuggestionRun
	for _, run := range runsOf(t, store) {
		if run.ID == complexitySuggestionSlotRunID(due.Add(time.Hour)) {
			skipped = &run
		}
	}
	require.NotNil(t, skipped)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, skipped.Status)
	assert.Contains(t, skipped.Error, "warming")
}

// Without a config store a node keeps the schedule in memory and runs it
// itself, at its next run and not again before the one after.
func TestComplexitySuggestionsScheduleWithoutAConfigStore(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	p, store := scheduledSuggestionPlugin(t, start, nil)
	ctx := context.Background()
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, start.Add(10*time.Minute)))
	assert.Empty(t, runsOf(t, store))
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, start.Add(time.Hour)))
	run := waitForRun(t, store)
	assert.Equal(t, complexitySuggestionSlotRunID(start.Add(time.Hour)), run.ID)
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, start.Add(time.Hour+time.Minute)))
	p.suggestions.wg.Wait()
	assert.Len(t, runsOf(t, store), 1)
}

type recordingQueue struct {
	mu    sync.Mutex
	ids   []string
	metas []string
}

func (q *recordingQueue) EnqueuePartitioned(_ context.Context, id, kind, partition, metadata, _ string) error {
	if kind != ComplexitySuggestionsJobKind || partition != ComplexitySuggestionsJobKind {
		return fmt.Errorf("unexpected kind %s / partition %s", kind, partition)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ids = append(q.ids, id)
	q.metas = append(q.metas, metadata)
	return nil
}

type inFlightJobs struct{ job *tables.TableSidekiqJob }

func (j *inFlightJobs) GetInFlightSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return j.job, nil
}

// A slot's run ID is the run row's primary key, a varchar(36), and the same on
// every node for the same slot.
func TestComplexitySuggestionSlotRunIDIsAStableUUID(t *testing.T) {
	slot := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	id := complexitySuggestionSlotRunID(slot)
	_, err := uuid.Parse(id)
	require.NoError(t, err, "slot run ID %q is not a UUID", id)
	assert.Len(t, id, 36)
	assert.Equal(t, id, complexitySuggestionSlotRunID(slot.In(time.FixedZone("IST", 5*3600+1800))), "the same instant is the same slot")
	assert.NotEqual(t, id, complexitySuggestionSlotRunID(slot.Add(time.Hour)), "a later slot gets its own ID")
}

// Cleanup closes the classifier whose serving space a run reads, so a run
// that arrived as a queued job must be stopped and waited for, as one the
// node runs itself is.
func TestComplexitySuggestionsCleanupStopsAQueuedRun(t *testing.T) {
	p, _, _ := suggestionPlugin(t)
	called := make(chan struct{})
	var once sync.Once
	p.SetChatRequestExecutor(func(ctx *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		once.Do(func() { close(called) })
		<-ctx.Done() // a model call that only ends when the run is cancelled
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "cancelled"}}
	})
	jobDone := make(chan struct{})
	go func() {
		defer close(jobDone)
		job := tables.TableSidekiqJob{ID: "job-1", Metadata: `{"run_id":"` + uuid.NewString() + `"}`}
		_, _ = p.RunComplexitySuggestionsJob(context.Background(), job, nil)
	}()
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued run never reached the model")
	}

	cleaned := make(chan struct{})
	go func() {
		p.closeComplexitySuggestions()
		close(cleaned)
	}()
	select {
	case <-cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not stop the queued run")
	}
	// The run ended before cleanup returned; the job then only formats its
	// result, so it returns promptly.
	select {
	case <-jobDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued job did not return after cleanup stopped its run")
	}
}

// A queued job that reaches a plugin already cleaned up must not start a run
// against its closed classifier, and closes the run it was queued as as
// skipped rather than leaving it queued.
func TestComplexitySuggestionsJobAfterCleanupIsRefused(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	runID := uuid.NewString()
	require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, runID, logstore.ComplexitySuggestionTriggerSchedule))
	p.closeComplexitySuggestions()
	job := tables.TableSidekiqJob{ID: "job-1", Metadata: `{"run_id":"` + runID + `","triggered_by":"schedule"}`}
	_, err := p.RunComplexitySuggestionsJob(context.Background(), job, nil)
	assert.ErrorIs(t, err, errSuggestionsClosing)
	runs := runsOf(t, store)
	require.Len(t, runs, 1, "no run starts once the plugin is closing")
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status, "a queued run is never left queued")
	assert.Contains(t, runs[0].Error, "shutting down")
}

// failingQueue refuses every job, as a job store that is down does.
type failingQueue struct{}

func (failingQueue) EnqueuePartitioned(context.Context, string, string, string, string, string) error {
	return errors.New("job store unavailable")
}

// A run the job runner refuses is closed as skipped with the reason, not left
// queued for a job that does not exist, and the refusal is still returned.
func TestComplexitySuggestionsEnqueueFailureSkipsTheRun(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	p.SetComplexitySuggestionJobs(failingQueue{}, &inFlightJobs{})
	runID := uuid.NewString()
	err := p.startComplexitySuggestionRun(context.Background(), runID, logstore.ComplexitySuggestionTriggerSchedule)
	require.ErrorContains(t, err, "job store unavailable")
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, runID, runs[0].ID)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status)
	assert.Contains(t, runs[0].Error, "could not be queued")
}

// A run that cannot read the router's space is recorded as failed even when
// the run's own context has already ended, which may be why it failed.
func TestComplexitySuggestionsRunFailureIsRecordedAfterItsContextEnds(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	p.semanticClassifier.Close() // no serving space to read
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runID := uuid.NewString()
	_, err := p.runComplexitySuggestions(ctx, runID, logstore.ComplexitySuggestionTriggerSchedule)
	require.Error(t, err)
	runs := runsOf(t, store)
	require.Len(t, runs, 1, "the failure is recorded: %v", err)
	assert.Equal(t, logstore.ComplexitySuggestionRunFailed, runs[0].Status)
}

// With Chromem as Bifrost's vector store nothing is captured: each node would
// hold only its own share, and a run reconciling on one share would supersede
// suggestions on traffic it never saw. The schedule neither claims its runs
// nor records one every interval, and a run started anyway reads no space.
func TestComplexitySuggestionsRefusedOnChromem(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	settings := suggestionTestSettings()
	settings.Schedule = hourlyFrom(start)
	p := newCapturePluginWithStore(t, settings, false)
	p.startComplexitySuggestions(context.Background(), 0)
	store := suggestionTestStore(t)
	p.SetComplexitySuggestionStore(store, func(context.Context, []string, int) (map[string]string, error) { return nil, nil })
	ctx := context.Background()
	assert.False(t, p.semanticClassifier.Status().CaptureAvailable)

	schedules := suggestionScheduleStore(t)
	p.SetComplexitySuggestionSchedules(schedules)
	for _, at := range []time.Time{start.Add(10 * time.Minute), start.Add(time.Hour + time.Second), start.Add(2*time.Hour + time.Second)} {
		require.NoError(t, p.scheduleComplexitySuggestions(ctx, at))
	}
	p.suggestions.wg.Wait()
	assert.Empty(t, runsOf(t, store))
	row, err := schedules.GetComplexitySuggestionSchedule(ctx, complexitySuggestionScheduleKey)
	require.NoError(t, err)
	assert.Nil(t, row, "nothing claimed, so a capture store added later starts the schedule fresh")

	_, err = p.runComplexitySuggestions(ctx, uuid.NewString(), logstore.ComplexitySuggestionTriggerSchedule)
	require.ErrorIs(t, err, complexity.ErrNoCaptureStore)
}

// A run this node starts itself that finds suggestions turned off when it
// begins closes the run it was recorded as as skipped, as a job does.
func TestComplexitySuggestionsInlineRunWhenTurnedOff(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(nil)))
	runID := uuid.NewString()
	require.NoError(t, p.startComplexitySuggestionRun(context.Background(), runID, logstore.ComplexitySuggestionTriggerSchedule))
	require.Eventually(t, func() bool { return !p.suggestions.inline.Load() }, 5*time.Second, 5*time.Millisecond)
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, runID, runs[0].ID)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status, "a queued run is never left queued")
	assert.Equal(t, suggestionsTurnedOffReason, runs[0].Error)
}

// A job that finds no routing plugin to run it closes its queued run as
// skipped, and is still not a job failure.
func TestSkipComplexitySuggestionsJob(t *testing.T) {
	store := suggestionTestStore(t)
	ctx := context.Background()
	runID := uuid.NewString()
	require.NoError(t, recordQueuedSuggestionRun(ctx, store, runID, logstore.ComplexitySuggestionTriggerManual))
	job := tables.TableSidekiqJob{ID: runID, Metadata: `{"run_id":"` + runID + `","triggered_by":"manual"}`}

	meta, err := SkipComplexitySuggestionsJob(ctx, store, job, "no routing plugin")
	require.NoError(t, err)
	assert.Equal(t, "no routing plugin", SummarizeComplexitySuggestionsJob(meta).Message)
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status)
	assert.Equal(t, logstore.ComplexitySuggestionTriggerManual, runs[0].TriggeredBy)
	assert.Equal(t, "no routing plugin", runs[0].Error)

	meta, err = SkipComplexitySuggestionsJob(ctx, nil, job, "no routing plugin")
	require.NoError(t, err, "without a log store there is nothing to close")
	assert.Equal(t, "no routing plugin", SummarizeComplexitySuggestionsJob(meta).Message)
}

// A job that finds suggestions turned off by the time it runs has nothing to
// do, which is not a failure, and closes the run it was queued as as skipped;
// nothing is scheduled while they are off.
func TestComplexitySuggestionsJobWhenTurnedOff(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	runID := uuid.NewString()
	require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, runID, logstore.ComplexitySuggestionTriggerSchedule))
	require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(nil)))
	meta, err := p.RunComplexitySuggestionsJob(context.Background(), tables.TableSidekiqJob{ID: runID, Metadata: `{"run_id":"` + runID + `","triggered_by":"schedule"}`}, nil)
	require.NoError(t, err)
	var decoded complexitySuggestionJobMeta
	require.NoError(t, sonic.UnmarshalString(meta, &decoded))
	assert.Contains(t, decoded.Message, "off")
	assert.Equal(t, decoded.Message, SummarizeComplexitySuggestionsJob(meta).Message)
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status, "a queued run is never left queued")

	require.NoError(t, p.scheduleComplexitySuggestions(context.Background(), time.Now()))
	p.suggestions.wg.Wait()
	assert.Len(t, runsOf(t, store), 1)
}

// refusingRunStore refuses to record runs, as a log store that is down does.
type refusingRunStore struct{ logstore.LogStore }

func (refusingRunStore) UpsertComplexitySuggestionRun(context.Context, *logstore.ComplexitySuggestionRun) error {
	return errors.New("log store unavailable")
}

// A job that finds suggestions turned off records the skip on a context of
// its own, so a job whose context already ended still closes its run; and a
// skip that cannot be recorded fails the job instead of leaving the run
// queued behind a success.
func TestComplexitySuggestionsJobWhenTurnedOffRecordsTheSkip(t *testing.T) {
	job := func(runID string) tables.TableSidekiqJob {
		return tables.TableSidekiqJob{ID: runID, Metadata: `{"run_id":"` + runID + `","triggered_by":"schedule"}`}
	}

	t.Run("after the job's context ended", func(t *testing.T) {
		p, store, _ := suggestionPlugin(t)
		runID := uuid.NewString()
		require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, runID, logstore.ComplexitySuggestionTriggerSchedule))
		require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(nil)))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := p.RunComplexitySuggestionsJob(ctx, job(runID), nil)
		require.NoError(t, err)
		runs := runsOf(t, store)
		require.Len(t, runs, 1)
		assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status, "recorded though the job's context had ended")
	})

	t.Run("a skip that cannot be recorded fails the job", func(t *testing.T) {
		p, store, _ := suggestionPlugin(t)
		runID := uuid.NewString()
		require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, runID, logstore.ComplexitySuggestionTriggerSchedule))
		require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(nil)))
		p.SetComplexitySuggestionStore(refusingRunStore{store}, func(context.Context, []string, int) (map[string]string, error) { return nil, nil })
		meta, err := p.RunComplexitySuggestionsJob(context.Background(), job(runID), nil)
		require.ErrorContains(t, err, "log store unavailable")
		assert.Contains(t, SummarizeComplexitySuggestionsJob(meta).Message, "off", "the message still says why nothing ran")
	})
}
