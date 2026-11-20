package routing

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggest"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggestrun"
	"github.com/maximhq/bifrost/plugins/routing/rules"
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
	return suggestionPluginWith(t, suggestionTestSettings())
}

func suggestionPluginWith(t *testing.T, settings *configstore.ComplexitySuggestionsConfig) (*RoutingPlugin, logstore.LogStore, *[]string) {
	t.Helper()
	p := newCapturePlugin(t, settings)
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
	serveCaptureRouter(t, p, captureRouterID, settings)
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
	runs, err := store.ListComplexitySuggestionRuns(context.Background(), logstore.ComplexitySuggestionRunFilter{}, 50)
	require.NoError(t, err)
	return runs
}

// claimThenCancel claims a slot in a real schedule store and then ends the
// scheduler's context, as a shutdown landing just after the claim does.
type claimThenCancel struct {
	ComplexitySuggestionScheduleStore
	cancel context.CancelFunc
}

func (s *claimThenCancel) ClaimComplexitySuggestionRun(ctx context.Context, routerID string, version int64, dueAt, nextRunAt time.Time, runID string) (bool, error) {
	claimed, err := s.ComplexitySuggestionScheduleStore.ClaimComplexitySuggestionRun(ctx, routerID, version, dueAt, nextRunAt, runID)
	if claimed {
		s.cancel()
	}
	return claimed, err
}

// A claimed slot always gets a record of its run: what follows the claim is
// written on a context of its own, so a scheduler context ending just after
// the claim cannot leave the slot taken by a run nothing recorded.
func TestComplexitySuggestionsScheduleRecordsTheRunAfterItsContextEnds(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, store := scheduledSuggestionPlugin(t, start, &claimThenCancel{ComplexitySuggestionScheduleStore: suggestionScheduleStore(t), cancel: cancel})

	require.NoError(t, p.scheduleComplexitySuggestions(ctx, start.Add(90*time.Minute)))
	assert.Empty(t, runsOf(t, store), "a new schedule waits for its next run")
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, start.Add(2*time.Hour+20*time.Second)))
	run := waitForRun(t, store)
	assert.Equal(t, logstore.ComplexitySuggestionTriggerSchedule, run.TriggeredBy)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status, run.Error)
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
	assert.Equal(t, complexitySuggestionSlotRunID(captureRouterID, due), run.ID)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status, run.Error)
	assert.Equal(t, logstore.ComplexitySuggestionTriggerSchedule, run.TriggeredBy)
	assert.Equal(t, captureRouterID, run.RouterID)
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
	assert.Equal(t, complexitySuggestionSlotRunID(captureRouterID, due), queue.ids[0])
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunQueued, runs[0].Status, "visible as queued before a job runner picks it up")
	assert.Equal(t, captureRouterID, runs[0].RouterID)

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
	assert.Equal(t, complexitySuggestionSlotRunID(captureRouterID, start.Add(time.Hour)), queue.ids[0], "the run due first")
	row, err := schedules.GetComplexitySuggestionSchedule(ctx, captureRouterID)
	require.NoError(t, err)
	assert.True(t, row.NextRunAt.Equal(start.Add(6*time.Hour)), "next run counted from now: %s", row.NextRunAt)
	assert.Len(t, runsOf(t, store), 1)

	// A new schedule: its next run replaces the old one, and nothing runs at once.
	settings := suggestionTestSettings()
	settings.Schedule = &configstore.ComplexitySuggestionsSchedule{Every: "1d", StartAt: &start}
	serveCaptureRouter(t, p, captureRouterID, settings)
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, back.Add(time.Minute)))
	row, err = schedules.GetComplexitySuggestionSchedule(ctx, captureRouterID)
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

	p.suggestions.inline[captureRouterID] = true // a run of this router's is still going here
	due := start.Add(time.Hour)
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, due.Add(time.Second)))
	delete(p.suggestions.inline, captureRouterID)
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status)
	assert.Contains(t, runs[0].Error, "still in progress")
	assert.Equal(t, complexitySuggestionSlotRunID(captureRouterID, due), runs[0].ID)

	warming := complexity.NewSemanticClassifier(ctx, bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(func() { _ = warming.Close() })
	// A router's classifier has the gateway's vector store from startup,
	// warming or not; without one it would read as having nowhere to capture.
	warming.SetConfiguredStore(&sharedTestStore{})
	rt := captureRouter(t, p)
	serving := rt.semantic
	rt.semantic = warming
	t.Cleanup(func() { rt.semantic = serving })
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, due.Add(time.Hour+time.Second)))
	var skipped *logstore.ComplexitySuggestionRun
	for _, run := range runsOf(t, store) {
		if run.ID == complexitySuggestionSlotRunID(captureRouterID, due.Add(time.Hour)) {
			skipped = &run
		}
	}
	require.NotNil(t, skipped)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, skipped.Status)
	assert.Contains(t, skipped.Error, "warming")
}

// A router whose suggestions are set up but off never runs on a schedule,
// yet a run, an estimate and an impact replay started on demand work, the run
// backfilling from the router's logs. A scheduled run queued before they were
// turned off does not run; a router with no suggestions settings has nothing
// to run with.
func TestComplexitySuggestionsOffStillRunOnDemand(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	p, store, _ := suggestionPlugin(t) // six requests captured live
	live := p.ComplexityCaptureStats().Written
	settings := suggestionTestSettings()
	settings.Enabled = false
	settings.Schedule = hourlyFrom(start)
	serveCaptureRouter(t, p, captureRouterID, settings)
	schedules := suggestionScheduleStore(t)
	p.SetComplexitySuggestionSchedules(schedules)
	ctx := context.Background()

	for _, at := range []time.Duration{10 * time.Minute, time.Hour + time.Second, 25 * time.Hour} {
		require.NoError(t, p.scheduleComplexitySuggestions(ctx, start.Add(at)))
	}
	p.suggestions.wg.Wait()
	assert.Empty(t, runsOf(t, store), "no scheduled runs while suggestions are off")
	row, err := schedules.GetComplexitySuggestionSchedule(ctx, captureRouterID)
	require.NoError(t, err)
	assert.Nil(t, row, "nor any schedule state")

	now := time.Now().UTC().Truncate(time.Second)
	for _, id := range []string{"before-1", "before-2", "before-3"} {
		addRouterLog(t, store, id, captureRouterID, now.Add(-3*24*time.Hour))
	}
	estimate, err := p.EstimateComplexitySuggestionRun(ctx, captureRouterID, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, estimate.Backfill)
	id, err := p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	require.NoError(t, err)
	run := waitForRun(t, store)
	assert.Equal(t, id, run.ID)
	assert.Equal(t, logstore.ComplexitySuggestionTriggerManual, run.TriggeredBy)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status, run.Error)
	assert.EqualValues(t, live+3, run.RequestsRead, "read with the requests backfilled from logs")
	phrases := []complexity.SuggestionPhrase{{Tier: "SIMPLE", Text: "simple exemplar"}, {Tier: "MEDIUM", Text: "medium exemplar"}, {Tier: "COMPLEX", Text: "complex exemplar"}}
	_, _, err = p.ComplexitySuggestionImpact(ctx, captureRouterID, 0, phrases, nil)
	require.NoError(t, err)

	_, err = p.runComplexitySuggestions(ctx, complexitySuggestionJobMeta{RunID: "slot", RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule})
	assert.ErrorIs(t, err, errSuggestionsUnavailable, "a scheduled run left queued from before they were turned off")

	serveCaptureRouter(t, p, captureRouterID, nil)
	_, err = p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	assert.ErrorIs(t, err, ErrComplexitySuggestionsOff)
	_, err = p.EstimateComplexitySuggestionRun(ctx, captureRouterID, 0)
	assert.ErrorIs(t, err, ErrComplexitySuggestionsOff)
	_, _, err = p.ComplexitySuggestionImpact(ctx, captureRouterID, 0, phrases, nil)
	assert.ErrorIs(t, err, ErrComplexitySuggestionsOff)
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
	assert.Equal(t, complexitySuggestionSlotRunID(captureRouterID, start.Add(time.Hour)), run.ID)
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, start.Add(time.Hour+time.Minute)))
	p.suggestions.wg.Wait()
	assert.Len(t, runsOf(t, store), 1)
}

// The next run served to clients is the schedule state's once a node has set
// it for the current settings (catch-ups and changes move it), the settings'
// own before that, and now when a run is already due.
func TestNextComplexitySuggestionRun(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	schedule := *hourlyFrom(start)
	now := start.Add(90 * time.Minute)
	ctx := context.Background()
	assert.True(t, NextComplexitySuggestionRun(ctx, nil, captureRouterID, schedule, now).Equal(start.Add(2*time.Hour)), "no state: from the settings")

	schedules := suggestionScheduleStore(t)
	stored := start.Add(3 * time.Hour)
	set, err := schedules.SetComplexitySuggestionSchedule(ctx, captureRouterID, complexitySuggestionScheduleHash(schedule), stored, nil)
	require.NoError(t, err)
	require.True(t, set)
	assert.True(t, NextComplexitySuggestionRun(ctx, schedules, captureRouterID, schedule, now).Equal(stored), "the stored next run")

	daily := configstore.ComplexitySuggestionsSchedule{Every: "1d", StartAt: &start}
	assert.True(t, NextComplexitySuggestionRun(ctx, schedules, captureRouterID, daily, now).Equal(start.Add(24*time.Hour)), "state for other settings is ignored")

	assert.True(t, NextComplexitySuggestionRun(ctx, schedules, captureRouterID, schedule, stored.Add(time.Minute)).Equal(stored.Add(time.Minute)), "a due run is due now")
}

type recordingQueue struct {
	mu        sync.Mutex
	ids       []string
	metas     []string
	cancelled []string
}

// Cancel records the cancel and reports it took, as the job runner does for
// a job still queued or running.
func (q *recordingQueue) Cancel(_ context.Context, id string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelled = append(q.cancelled, id)
	return true, nil
}

func (q *recordingQueue) EnqueuePartitioned(_ context.Context, id, kind, partition, metadata, _ string) error {
	if kind != ComplexitySuggestionsJobKind || partition != complexitySuggestionPartition(captureRouterID) {
		return fmt.Errorf("unexpected kind %s / partition %s", kind, partition)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ids = append(q.ids, id)
	q.metas = append(q.metas, metadata)
	return nil
}

type inFlightJobs struct {
	job, latest *tables.TableSidekiqJob
	byID        map[string]*tables.TableSidekiqJob
}

func (j *inFlightJobs) GetInFlightSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return j.job, nil
}

func (j *inFlightJobs) GetLatestSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return j.latest, nil
}

// uniqueQueue is a job table: a job ID it already holds is refused as a
// duplicate key, as the config store's primary key refuses it.
type uniqueQueue struct {
	recordingQueue
	seen map[string]bool
}

func (q *uniqueQueue) EnqueuePartitioned(ctx context.Context, id, kind, partition, metadata, createdBy string) error {
	q.mu.Lock()
	if q.seen[id] {
		q.mu.Unlock()
		return errors.New("UNIQUE constraint failed: sidekiq_jobs.id")
	}
	if q.seen == nil {
		q.seen = map[string]bool{}
	}
	q.seen[id] = true
	q.mu.Unlock()
	return q.recordingQueue.EnqueuePartitioned(ctx, id, kind, partition, metadata, createdBy)
}

// gatedRunsStore holds the first n reads of the run list, once read, until
// all n are done, so n starts all pass the busy check before any records its run:
// the race two clicks or two nodes hit, made certain.
type gatedRunsStore struct {
	logstore.LogStore
	mu      sync.Mutex
	waiting int
	open    chan struct{}
}

func newGatedRunsStore(store logstore.LogStore, n int) *gatedRunsStore {
	return &gatedRunsStore{LogStore: store, waiting: n, open: make(chan struct{})}
}

func (g *gatedRunsStore) ListComplexitySuggestionRuns(ctx context.Context, filter logstore.ComplexitySuggestionRunFilter, limit int) ([]logstore.ComplexitySuggestionRun, error) {
	// Read first, then wait: every start has seen the list before any acts.
	runs, err := g.LogStore.ListComplexitySuggestionRuns(ctx, filter, limit)
	g.mu.Lock()
	if g.waiting > 0 {
		g.waiting--
		if g.waiting == 0 {
			close(g.open)
		}
	}
	g.mu.Unlock()
	<-g.open
	return runs, err
}

// Manual starts that race past the busy check, as two clicks or two nodes
// can, queue one job between them and are all handed its run, never a
// second job. Once that run is over, the next start names a new one.
func TestComplexitySuggestionsSimultaneousManualStartsQueueOneRun(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	queue := &uniqueQueue{}
	jobs := &inFlightJobs{} // nothing in flight yet, as each start sees it
	p.SetComplexitySuggestionJobs(queue, jobs)
	ctx := context.Background()

	const starts = 8
	p.SetComplexitySuggestionStore(newGatedRunsStore(store, starts), func(context.Context, []string, int) (map[string]string, error) { return nil, nil })
	ids := make([]string, starts)
	errs := make([]error, starts)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range starts {
		wg.Go(func() {
			<-gate
			ids[i], errs[i] = p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
		})
	}
	close(gate)
	wg.Wait()
	require.Len(t, queue.ids, 1, "one job queued")
	runID := queue.ids[0]
	_, err := uuid.Parse(runID)
	require.NoError(t, err)
	for i := range starts {
		require.NoError(t, errs[i])
		assert.Equal(t, runID, ids[i], "every start is handed the one queued run")
	}

	// The run finished; the next start is a new run.
	finished := time.Now().UTC()
	require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, &logstore.ComplexitySuggestionRun{ID: runID, RouterID: captureRouterID,
		Status: logstore.ComplexitySuggestionRunSucceeded, StartedAt: finished, FinishedAt: &finished}))
	jobs.latest = &tables.TableSidekiqJob{ID: runID}
	next, err := p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	require.NoError(t, err)
	assert.NotEqual(t, runID, next)
	assert.Equal(t, []string{runID, next}, queue.ids)
}

func (j *inFlightJobs) GetSidekiqJob(_ context.Context, id string) (*tables.TableSidekiqJob, error) {
	return j.byID[id], nil // nil, nil for no such job, as the config store answers
}

// A slot's run ID is the run row's primary key, a varchar(36), the same on
// every node for the same router and slot, and different for another router.
func TestComplexitySuggestionSlotRunIDIsAStableUUID(t *testing.T) {
	slot := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	id := complexitySuggestionSlotRunID(captureRouterID, slot)
	_, err := uuid.Parse(id)
	require.NoError(t, err, "slot run ID %q is not a UUID", id)
	assert.Len(t, id, 36)
	assert.Equal(t, id, complexitySuggestionSlotRunID(captureRouterID, slot.In(time.FixedZone("IST", 5*3600+1800))), "the same instant is the same slot")
	assert.NotEqual(t, id, complexitySuggestionSlotRunID(captureRouterID, slot.Add(time.Hour)), "a later slot gets its own ID")
	assert.NotEqual(t, id, complexitySuggestionSlotRunID("support", slot), "another router due at the same slot gets its own job")
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
		job := tables.TableSidekiqJob{ID: "job-1", Metadata: `{"run_id":"` + uuid.NewString() + `","router_id":"` + captureRouterID + `"}`}
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
	require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, complexitySuggestionJobMeta{
		RunID: runID, RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule}))
	p.closeComplexitySuggestions()
	job := tables.TableSidekiqJob{ID: "job-1", Metadata: `{"run_id":"` + runID + `","router_id":"` + captureRouterID + `","triggered_by":"schedule"}`}
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

func (failingQueue) Cancel(context.Context, string) (bool, error) {
	return false, errors.New("job store unavailable")
}

// A run the job runner refuses is closed as skipped with the reason, not left
// queued for a job that does not exist, and the refusal is still returned.
func TestComplexitySuggestionsEnqueueFailureSkipsTheRun(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	p.SetComplexitySuggestionJobs(failingQueue{}, &inFlightJobs{})
	runID := uuid.NewString()
	started, err := p.startComplexitySuggestionRun(context.Background(), complexitySuggestionJobMeta{
		RunID: runID, RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule})
	require.ErrorContains(t, err, "job store unavailable")
	assert.False(t, started)
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, runID, runs[0].ID)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status)
	assert.Contains(t, runs[0].Error, "could not be queued")
}

// scriptedJobs answers the in-flight check with inFlight, and the lookup of
// one job by ID with job and err.
type scriptedJobs struct {
	inFlight *tables.TableSidekiqJob
	job      *tables.TableSidekiqJob
	err      error
}

func (j *scriptedJobs) GetLatestSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return nil, nil
}

func (j *scriptedJobs) GetInFlightSidekiqJobByKind(context.Context, string) (*tables.TableSidekiqJob, error) {
	return j.inFlight, nil
}

func (j *scriptedJobs) GetSidekiqJob(context.Context, string) (*tables.TableSidekiqJob, error) {
	return j.job, j.err
}

// An enqueue error is not proof that no job was written. When the job store
// shows the run's job, the run is queued after all; when it cannot say, the
// run is left as recorded rather than closed under a job that may run.
func TestComplexitySuggestionsUncertainEnqueueLeavesTheRunQueued(t *testing.T) {
	t.Run("the job was written", func(t *testing.T) {
		p, store, _ := suggestionPlugin(t)
		runID := uuid.NewString()
		p.SetComplexitySuggestionJobs(failingQueue{}, &scriptedJobs{job: &tables.TableSidekiqJob{ID: runID}})
		started, err := p.startComplexitySuggestionRun(context.Background(), complexitySuggestionJobMeta{
			RunID: runID, RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule})
		require.NoError(t, err)
		assert.True(t, started)
		runs := runsOf(t, store)
		require.Len(t, runs, 1)
		assert.Equal(t, logstore.ComplexitySuggestionRunQueued, runs[0].Status)
	})
	t.Run("the job store cannot say", func(t *testing.T) {
		p, store, _ := suggestionPlugin(t)
		p.SetComplexitySuggestionJobs(failingQueue{}, &scriptedJobs{err: errors.New("job store unavailable")})
		runID := uuid.NewString()
		started, err := p.startComplexitySuggestionRun(context.Background(), complexitySuggestionJobMeta{
			RunID: runID, RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule})
		require.ErrorContains(t, err, "could not confirm")
		assert.False(t, started)
		runs := runsOf(t, store)
		require.Len(t, runs, 1)
		assert.Equal(t, logstore.ComplexitySuggestionRunQueued, runs[0].Status, "not closed under a job that may run")
	})
	t.Run("another router's job is in flight", func(t *testing.T) {
		// The run's own job is looked up, so another router's job in flight
		// says nothing about it: no job of its own means it was not queued.
		p, store, _ := suggestionPlugin(t)
		p.SetComplexitySuggestionJobs(failingQueue{}, &scriptedJobs{
			inFlight: &tables.TableSidekiqJob{ID: "someone-else", Metadata: `{"run_id":"someone-else","router_id":"office"}`}})
		runID := uuid.NewString()
		_, err := p.startComplexitySuggestionRun(context.Background(), complexitySuggestionJobMeta{
			RunID: runID, RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule})
		require.ErrorContains(t, err, "job store unavailable")
		runs := runsOf(t, store)
		require.Len(t, runs, 1)
		assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status)
	})
}

// A run that cannot read the router's space is recorded as failed even when
// the run's own context has already ended, which may be why it failed.
func TestComplexitySuggestionsRunFailureIsRecordedAfterItsContextEnds(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	_ = captureRouter(t, p).semantic.Close() // no serving space to read
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runID := uuid.NewString()
	_, err := p.runComplexitySuggestions(ctx, complexitySuggestionJobMeta{
		RunID: runID, RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule})
	require.Error(t, err)
	runs := runsOf(t, store)
	require.Len(t, runs, 1, "the failure is recorded: %v", err)
	assert.Equal(t, logstore.ComplexitySuggestionRunFailed, runs[0].Status)
	// The window it would have read, not a zero time: the whole lookback here.
	require.NotNil(t, runs[0].FinishedAt)
	lookback := time.Duration(captureRouter(t, p).config.Load().Suggestions.LookbackDays) * 24 * time.Hour
	assert.WithinDuration(t, runs[0].FinishedAt.Add(-lookback), runs[0].WindowStart, time.Second)

	// A run over a window records that window.
	windowedID := uuid.NewString()
	_, err = p.runComplexitySuggestions(ctx, complexitySuggestionJobMeta{
		RunID: windowedID, RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerManual, WindowSeconds: 3600})
	require.Error(t, err)
	for _, run := range runsOf(t, store) {
		if run.ID == windowedID {
			require.NotNil(t, run.FinishedAt)
			assert.WithinDuration(t, run.FinishedAt.Add(-time.Hour), run.WindowStart, time.Second)
		}
	}
}

// A run this node starts itself that finds suggestions turned off when it
// begins closes the run it was recorded as as skipped, as a job does.
func TestComplexitySuggestionsInlineRunWhenTurnedOff(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	serveCaptureRouter(t, p, captureRouterID, nil)
	runID := uuid.NewString()
	started, err := p.startComplexitySuggestionRun(context.Background(), complexitySuggestionJobMeta{
		RunID: runID, RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule})
	require.NoError(t, err)
	require.True(t, started)
	p.suggestions.wg.Wait()
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, runID, runs[0].ID)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status, "a queued run is never left queued")
	assert.Equal(t, suggestionsTurnedOffReason, runs[0].Error)
}

// Two starts that race to the same run ID both register it as queued; the
// later registration, landing after the run ended, must not put it back to
// queued, or the router would read busy and the outcome would be lost.
func TestComplexitySuggestionsLateRegistrationKeepsTheOutcome(t *testing.T) {
	store := suggestionTestStore(t)
	ctx := context.Background()
	meta := complexitySuggestionJobMeta{RunID: uuid.NewString(), RouterID: complexity.DefaultRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerManual}
	require.NoError(t, recordQueuedSuggestionRun(ctx, store, meta))
	finished := time.Now().UTC()
	require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, &logstore.ComplexitySuggestionRun{
		ID: meta.RunID, RouterID: meta.RouterID, TriggeredBy: meta.TriggeredBy, Status: logstore.ComplexitySuggestionRunSucceeded,
		StartedAt: finished.Add(-time.Minute), FinishedAt: &finished, Created: 2,
	}))

	require.NoError(t, recordQueuedSuggestionRun(ctx, store, meta))
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, runs[0].Status, "the outcome is kept")
	assert.EqualValues(t, 2, runs[0].Created)
}

// A job that finds no routing plugin to run it closes its queued run as
// skipped, and is still not a job failure.
func TestSkipComplexitySuggestionsJob(t *testing.T) {
	store := suggestionTestStore(t)
	ctx := context.Background()
	runID := uuid.NewString()
	require.NoError(t, recordQueuedSuggestionRun(ctx, store, complexitySuggestionJobMeta{
		RunID: runID, RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerManual}))
	// Queued before runs named their router: the default router's.
	job := tables.TableSidekiqJob{ID: runID, Metadata: `{"run_id":"` + runID + `","router_id":"` + captureRouterID + `","triggered_by":"manual"}`}

	meta, err := SkipComplexitySuggestionsJob(ctx, store, job, "no routing plugin")
	require.NoError(t, err)
	assert.Equal(t, "no routing plugin", SummarizeComplexitySuggestionsJob(meta).Message)
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, runs[0].Status)
	assert.Equal(t, logstore.ComplexitySuggestionTriggerManual, runs[0].TriggeredBy)
	assert.Equal(t, captureRouterID, runs[0].RouterID)
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
	require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, complexitySuggestionJobMeta{RunID: runID, RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule}))
	serveCaptureRouter(t, p, captureRouterID, nil)
	meta, err := p.RunComplexitySuggestionsJob(context.Background(), tables.TableSidekiqJob{ID: runID, Metadata: `{"run_id":"` + runID + `","router_id":"` + captureRouterID + `","triggered_by":"schedule"}`}, nil)
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
		require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, complexitySuggestionJobMeta{RunID: runID, RouterID: complexity.DefaultRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule}))
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
		require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, complexitySuggestionJobMeta{RunID: runID, RouterID: complexity.DefaultRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule}))
		require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(nil)))
		p.SetComplexitySuggestionStore(refusingRunStore{store}, func(context.Context, []string, int) (map[string]string, error) { return nil, nil })
		meta, err := p.RunComplexitySuggestionsJob(context.Background(), job(runID), nil)
		require.ErrorContains(t, err, "log store unavailable")
		assert.Contains(t, SummarizeComplexitySuggestionsJob(meta).Message, "off", "the message still says why nothing ran")
	})
}

// A run started by hand runs outside the schedule under a fresh UUID, is
// recorded as manual, and is refused while another is in flight or when suggestions are off.
func TestComplexitySuggestionsRunNow(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	ctx := context.Background()
	runID, err := p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	require.NoError(t, err)
	_, err = uuid.Parse(runID)
	require.NoError(t, err, "a manual run ID is the run row's varchar(36) key, so a bare UUID; got %q", runID)
	assert.Len(t, runID, 36)
	run := waitForRun(t, store)
	assert.Equal(t, runID, run.ID)
	assert.Equal(t, logstore.ComplexitySuggestionTriggerManual, run.TriggeredBy)
	assert.Equal(t, captureRouterID, run.RouterID)
	assert.Equal(t, p.ComplexitySuggestionRouterVersion(captureRouterID), run.RouterVersion)
	assert.NotEmpty(t, run.RouterVersion)

	queue := &recordingQueue{}
	p.SetComplexitySuggestionJobs(queue, &inFlightJobs{job: &tables.TableSidekiqJob{ID: "busy", Metadata: `{"run_id":"busy","router_id":"` + captureRouterID + `"}`}})
	_, err = p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	require.ErrorIs(t, err, ErrComplexitySuggestionRunInFlight)
	assert.Empty(t, queue.ids)

	// Another router's queued run is no reason for this one to wait.
	p.SetComplexitySuggestionJobs(queue, &inFlightJobs{job: &tables.TableSidekiqJob{ID: "other", Metadata: `{"run_id":"other","router_id":"office"}`}})
	day, err := p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 24*time.Hour)
	require.NoError(t, err)
	require.Len(t, queue.ids, 1)
	assert.Equal(t, day, queue.ids[0])
	assert.Contains(t, queue.metas[0], `"window_seconds":86400`)

	// That run is queued now, so the router is busy until it runs, even
	// while the job store reports another router's job.
	_, err = p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	require.ErrorIs(t, err, ErrComplexitySuggestionRunInFlight, "the router's own queued run")
	require.Len(t, queue.ids, 1)

	_, err = p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 30*24*time.Hour)
	require.ErrorIs(t, err, ErrComplexitySuggestionWindow, "a window past the 7-day lookback reads vectors that are gone")
	_, err = p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 500*time.Millisecond)
	require.ErrorIs(t, err, ErrComplexitySuggestionWindow, "a sub-second window would be sent as 0 seconds, the whole lookback")
	_, err = p.StartComplexitySuggestionRunNow(ctx, "office", 0)
	require.ErrorIs(t, err, ErrUnknownComplexityRouter)

	serveCaptureRouter(t, p, captureRouterID, nil)
	_, err = p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	require.ErrorIs(t, err, ErrComplexitySuggestionsOff)
}

// A manual run asked for while no phrase bank serves is refused up front,
// rather than accepted and then recorded as a failed run.
func TestComplexitySuggestionsRunNowNeedsAServingBank(t *testing.T) {
	ctx := context.Background()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	p := &RoutingPlugin{logger: logger, semanticClassifier: complexity.NewSemanticClassifier(ctx, logger)}
	matcher, err := rules.NewModelRouterMatcher()
	require.NoError(t, err)
	p.routerMatcher = matcher
	p.startComplexityCapture(ctx, time.Hour, time.Hour)
	p.startComplexitySuggestions(ctx, 0)
	t.Cleanup(func() { _ = p.Cleanup() })
	store := suggestionTestStore(t)
	p.SetComplexitySuggestionStore(store, func(context.Context, []string, int) (map[string]string, error) { return nil, nil })
	require.NoError(t, p.ReloadModelRouter(&configstore.ModelRouter{ID: captureRouterID, Name: captureRouterID, ComplexityAnalyzerConfig: *captureTestConfig(suggestionTestSettings())}))
	rt := captureRouter(t, p)
	rt.semantic.SetEmbeddingFunc(func(context.Context, *complexity.SemanticConfig, string) ([]float32, error) {
		return nil, errors.New("embedding provider down")
	})
	require.Eventually(t, func() bool { return rt.semantic.Status().State == complexity.SemanticStatusFailed },
		2*time.Second, 5*time.Millisecond)

	_, err = p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	require.ErrorIs(t, err, complexity.ErrNoServingGeneration)
	p.suggestions.wg.Wait()
	runs, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{}, 10)
	require.NoError(t, err)
	assert.Empty(t, runs)
}

// A decision-model router's suggestions run like a semantic router's, as
// examples: a scheduled slot before the embedding path is up is recorded as
// skipped, and once it is, a run embeds the router's logged requests, and a
// cluster its decision model sends to a tier the judge disagrees with gets
// examples, kept against the router's examples version. The estimate sizes
// what was backfilled. Phrase tests and impact replays are for semantic
// routers, and an llm-classifier router has no suggestions at all.
func TestComplexitySuggestionsDecisionRouterRuns(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	p, store := scheduledSuggestionPlugin(t, start, suggestionScheduleStore(t))
	settings := suggestionTestSettings()
	settings.Schedule = hourlyFrom(start)
	settings.Embedding = &configstore.ComplexitySuggestionsEmbedding{Provider: "openai", Model: "test-embedding-model"}
	const routerID = "decision-router"
	serveDecisionRouter(t, p, routerID, settings, nil)
	ctx := context.Background()

	for _, at := range []time.Time{start.Add(10 * time.Minute), start.Add(time.Hour + time.Second)} {
		require.NoError(t, p.scheduleComplexitySuggestions(ctx, at))
	}
	p.suggestions.wg.Wait()
	var skipped []string
	for _, run := range runsOf(t, store) {
		if run.RouterID == routerID {
			skipped = append(skipped, run.Status+": "+run.Error)
		}
	}
	assert.Equal(t, []string{logstore.ComplexitySuggestionRunSkipped + ": the embedding path was not ready yet"}, skipped)
	_, err := p.StartComplexitySuggestionRunNow(ctx, routerID, 0)
	require.ErrorIs(t, err, ErrEmbeddingRequestExecutorNotConfigured, "refused up front, not recorded as a failed run")

	p.SetEmbeddingRequestExecutor((&decisionEmbeddings{}).execute)
	now := time.Now().UTC()
	for i := range 6 {
		addDecisionLog(t, store, fmt.Sprintf("decided-%d", i), routerID, now.Add(-time.Hour), complexity.TierComplex, complexity.MechanismDecision)
	}
	runID, err := p.StartComplexitySuggestionRunNow(ctx, routerID, 0)
	require.NoError(t, err)
	var run logstore.ComplexitySuggestionRun
	require.Eventually(t, func() bool {
		for _, r := range runsOf(t, store) {
			if r.ID == runID && r.FinishedAt != nil {
				run = r
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond)
	require.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status, run.Error)
	assert.EqualValues(t, 6, run.RequestsRead, "the router's logged requests, embedded from its logs")
	assert.Equal(t, PromptHashOf(t, p, routerID), run.PromptHash)

	rows, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{RouterID: routerID})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, []string{"a short question"}, rows[0].Phrases(), "the judge's example, as written")
	assert.Equal(t, complexity.TierSimple, rows[0].Tier)
	assert.Equal(t, string(suggest.CategoryMisrouted), rows[0].Category)
	assert.Equal(t, map[string]int64{complexity.TierComplex: 6}, rows[0].CurrentRouting())
	assert.Equal(t, p.ComplexitySuggestionRouterVersion(routerID), rows[0].RouterVersion)
	assert.NotEmpty(t, rows[0].RouterVersion)

	estimate, err := p.EstimateComplexitySuggestionRun(ctx, routerID, 0)
	require.NoError(t, err)
	assert.Equal(t, 6, estimate.Requests)
	assert.Zero(t, estimate.Backfill, "the run embedded them already")

	_, err = p.TestComplexitySuggestionPhrases(ctx, routerID, []complexity.SuggestionPhrase{{Tier: complexity.TierSimple, Text: "x"}}, nil)
	require.ErrorIs(t, err, ErrComplexitySuggestionsClassifier)
	_, _, err = p.ComplexitySuggestionImpact(ctx, routerID, 0, nil, nil)
	require.ErrorIs(t, err, ErrComplexitySuggestionsClassifier)

	llm := captureTestConfig(&configstore.ComplexitySuggestionsConfig{Provider: "openai", Model: "gpt-test"})
	llm.Classifier, llm.Semantic, llm.LLM = complexity.ClassifierLLM, nil, &complexity.LLMConfig{Provider: "openai", Model: "gpt-test"}
	require.NoError(t, p.ReloadModelRouter(&configstore.ModelRouter{ID: "llm-router", Name: "llm-router", ComplexityAnalyzerConfig: *llm}))
	_, err = p.StartComplexitySuggestionRunNow(ctx, "llm-router", 0)
	require.ErrorIs(t, err, ErrComplexitySuggestionsClassifier)
	_, err = p.EstimateComplexitySuggestionRun(ctx, "llm-router", 0)
	require.ErrorIs(t, err, ErrComplexitySuggestionsClassifier)
}

// A decision-model router's run writes signals too: it is given each tier's
// room for them and the signals and definitions its decision model reads,
// the administrator's where they are set and the shipped ones elsewhere. A
// semantic router's run writes none.
func TestComplexitySuggestionDecisionRunSettingsCarrySignals(t *testing.T) {
	p, _, _ := suggestionPlugin(t)
	settings := suggestionTestSettings()
	settings.Embedding = &configstore.ComplexitySuggestionsEmbedding{Provider: "openai", Model: "test-embedding-model"}
	serveDecisionRouter(t, p, "decision-router", settings, &complexity.DecisionConfig{Criteria: map[string]configstore.ComplexityDecisionTierCriteria{
		complexity.TierSimple: {Definition: "One short answer.", Signals: []string{"An administrator's signal"}},
	}})
	router, ok := p.suggestionRouter("decision-router")
	require.True(t, ok)
	s := runSettings(router, "run", logstore.ComplexitySuggestionTriggerManual, 0)
	assert.Equal(t, suggest.ModeExamples, s.Mode)
	assert.Equal(t, configstore.MaxComplexityDecisionSignals, s.MaxSignalsPerTier)
	assert.Equal(t, []string{"An administrator's signal"}, s.Signals[complexity.TierSimple])
	assert.Equal(t, "One short answer.", s.Definitions[complexity.TierSimple])
	shipped := configstore.DefaultComplexityDecisionGuidance().Criteria[complexity.TierMedium]
	assert.Equal(t, shipped.Signals, s.Signals[complexity.TierMedium])
	assert.Equal(t, shipped.Definition, s.Definitions[complexity.TierMedium])

	semantic, ok := p.suggestionRouter(captureRouterID)
	require.True(t, ok)
	assert.Zero(t, runSettings(semantic, "run", logstore.ComplexitySuggestionTriggerManual, 0).MaxSignalsPerTier)
}

// PromptHashOf is the prompt hash a router's run records.
func PromptHashOf(t *testing.T, p *RoutingPlugin, routerID string) string {
	t.Helper()
	router, ok := p.suggestionRouter(routerID)
	require.True(t, ok)
	_, tierPrompt, _, err := p.newSuggestionTierDecider(router)
	require.NoError(t, err)
	return suggestrun.PromptHash(suggest.DefaultExampleInstructions(), tierPrompt)
}

// serveDecisionRouter serves a decision-model router with the given
// suggestions settings and previous-message count (the default when nil).
func serveDecisionRouter(t *testing.T, p *RoutingPlugin, id string, settings *configstore.ComplexitySuggestionsConfig, decision *complexity.DecisionConfig) *complexity.AnalyzerConfig {
	t.Helper()
	cfg := captureTestConfig(settings)
	cfg.Classifier, cfg.Semantic, cfg.Decision = complexity.ClassifierDecision, nil, decision
	if cfg.Decision == nil {
		cfg.Decision = &complexity.DecisionConfig{}
	}
	require.NoError(t, p.ReloadModelRouter(&configstore.ModelRouter{ID: id, Name: id, ComplexityAnalyzerConfig: *cfg}))
	_, ok := p.suggestionRouter(id)
	require.True(t, ok)
	return cfg
}

// A scheduled run that comes due while a manual one is queued or running is
// recorded as skipped, not queued behind it, and the schedule moves on; a
// manual run never moves the schedule.
func TestComplexitySuggestionsScheduledRunDuringAManualOne(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	schedules := suggestionScheduleStore(t)
	p, store := scheduledSuggestionPlugin(t, start, schedules)
	ctx := context.Background()
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, start.Add(10*time.Minute)))

	queue := &recordingQueue{}
	manual := &inFlightJobs{}
	p.SetComplexitySuggestionJobs(queue, manual)
	runID, err := p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	require.NoError(t, err)
	require.Equal(t, []string{runID}, queue.ids)
	row, err := schedules.GetComplexitySuggestionSchedule(ctx, captureRouterID)
	require.NoError(t, err)
	assert.True(t, row.NextRunAt.Equal(start.Add(time.Hour)), "a manual run leaves the schedule alone")

	manual.job = &tables.TableSidekiqJob{ID: runID} // still running
	due := start.Add(time.Hour)
	require.NoError(t, p.scheduleComplexitySuggestions(ctx, due.Add(time.Second)))
	assert.Equal(t, []string{runID}, queue.ids, "nothing queued behind the manual run")
	var scheduled *logstore.ComplexitySuggestionRun
	for _, run := range runsOf(t, store) {
		if run.ID == complexitySuggestionSlotRunID(captureRouterID, due) {
			scheduled = &run
		}
	}
	require.NotNil(t, scheduled)
	assert.Equal(t, logstore.ComplexitySuggestionRunSkipped, scheduled.Status)
	assert.Contains(t, scheduled.Error, "still in progress")
	row, err = schedules.GetComplexitySuggestionSchedule(ctx, captureRouterID)
	require.NoError(t, err)
	assert.True(t, row.NextRunAt.Equal(due.Add(time.Hour)), "the schedule moved on")
}

// Cancelling a run that has not started cancels its job and records it as
// cancelled here; a running one is left for its handler to record; one that
// ended, or another router's, cannot be cancelled.
func TestComplexitySuggestionsCancelRun(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	ctx := context.Background()
	queue := &recordingQueue{}
	p.SetComplexitySuggestionJobs(queue, &inFlightJobs{})
	runID, err := p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	require.NoError(t, err)

	run, err := p.CancelComplexitySuggestionRun(ctx, captureRouterID, runID)
	require.NoError(t, err)
	assert.Equal(t, []string{runID}, queue.cancelled, "the job is cancelled on whichever node holds it")
	assert.Equal(t, logstore.ComplexitySuggestionRunCancelled, run.Status)
	stored := runsOf(t, store)
	require.Len(t, stored, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunCancelled, stored[0].Status, "a queued run never starts, so it is recorded here")
	require.NotNil(t, stored[0].FinishedAt)

	_, err = p.CancelComplexitySuggestionRun(ctx, captureRouterID, runID)
	require.ErrorIs(t, err, ErrComplexitySuggestionRunEnded)
	_, err = p.CancelComplexitySuggestionRun(ctx, captureRouterID, "missing")
	require.ErrorIs(t, err, ErrUnknownComplexitySuggestionRun)
	_, err = p.CancelComplexitySuggestionRun(ctx, "office", runID)
	require.ErrorIs(t, err, ErrUnknownComplexityRouter)

	// Another router's run is not this router's to cancel: it is not found,
	// and stays as it was.
	foreign := &logstore.ComplexitySuggestionRun{ID: uuid.NewString(), RouterID: "office", Status: logstore.ComplexitySuggestionRunRunning, StartedAt: time.Now().UTC()}
	require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, foreign))
	_, err = p.CancelComplexitySuggestionRun(ctx, captureRouterID, foreign.ID)
	require.ErrorIs(t, err, ErrUnknownComplexitySuggestionRun)
	assert.Equal(t, []string{runID}, queue.cancelled, "no job is cancelled for it")
	stillRunning, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{IDs: []string{foreign.ID}}, 1)
	require.NoError(t, err)
	require.Len(t, stillRunning, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunRunning, stillRunning[0].Status)

	// A running run's handler records its own outcome when it stops, and the
	// cancel answers with that outcome once it is written.
	defaultWait := suggestionCancelSettleWait
	t.Cleanup(func() { suggestionCancelSettleWait = defaultWait })
	running := logstore.ComplexitySuggestionRun{ID: "running", RouterID: captureRouterID, Status: logstore.ComplexitySuggestionRunRunning,
		Stage: logstore.ComplexitySuggestionRunStageSuggesting, StartedAt: time.Now()}
	require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, &running))
	// The handler records its outcome only once the cancel has reached the
	// queue, as a real one stops after its job is cancelled.
	reached := make(chan struct{}, 1)
	p.SetComplexitySuggestionJobs(hookQueue{cancel: func(context.Context, string) (bool, error) {
		select {
		case reached <- struct{}{}:
		default:
		}
		return true, nil
	}}, &inFlightJobs{})
	recorded := make(chan error, 1)
	go func() {
		<-reached
		ended := running
		finished := time.Now().UTC()
		ended.Status, ended.Stage, ended.FinishedAt = logstore.ComplexitySuggestionRunCancelled, "", &finished
		recorded <- store.UpsertComplexitySuggestionRun(context.Background(), &ended)
	}()
	run, err = p.CancelComplexitySuggestionRun(ctx, captureRouterID, "running")
	require.NoError(t, err)
	require.NoError(t, <-recorded)
	assert.Equal(t, logstore.ComplexitySuggestionRunCancelled, run.Status, "the handler's own record, not the one read before the cancel")
	require.NotNil(t, run.FinishedAt)

	// A handler that has not stopped within the wait leaves the record as it
	// stands, still running.
	suggestionCancelSettleWait = 100 * time.Millisecond
	slow := logstore.ComplexitySuggestionRun{ID: "slow", RouterID: captureRouterID, Status: logstore.ComplexitySuggestionRunRunning,
		Stage: logstore.ComplexitySuggestionRunStageSuggesting, StartedAt: time.Now()}
	require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, &slow))
	run, err = p.CancelComplexitySuggestionRun(ctx, captureRouterID, "slow")
	require.NoError(t, err)
	assert.Equal(t, logstore.ComplexitySuggestionRunRunning, run.Status, "left for the handler, which keeps what it wrote")
}

// slowRunReads is a store whose run reads after the first block until their
// context ends, as a stalled database would.
type slowRunReads struct {
	logstore.LogStore
	reads atomic.Int32
}

func (s *slowRunReads) ListComplexitySuggestionRuns(ctx context.Context, filter logstore.ComplexitySuggestionRunFilter, limit int) ([]logstore.ComplexitySuggestionRun, error) {
	if s.reads.Add(1) == 1 {
		return s.LogStore.ListComplexitySuggestionRuns(ctx, filter, limit)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// A cancel waits for a running run's outcome no longer than the settle wait,
// even when a read of the run stalls and the caller set no deadline.
func TestComplexitySuggestionsCancelWaitIsBoundedWhenReadsStall(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	defaultWait := suggestionCancelSettleWait
	suggestionCancelSettleWait = 100 * time.Millisecond
	t.Cleanup(func() { suggestionCancelSettleWait = defaultWait })
	run := logstore.ComplexitySuggestionRun{ID: uuid.NewString(), RouterID: captureRouterID, Status: logstore.ComplexitySuggestionRunRunning,
		Stage: logstore.ComplexitySuggestionRunStageSuggesting, StartedAt: time.Now()}
	require.NoError(t, store.UpsertComplexitySuggestionRun(context.Background(), &run))
	slow := &slowRunReads{LogStore: store}
	p.SetComplexitySuggestionStore(slow, func(context.Context, []string, int) (map[string]string, error) { return nil, nil })
	p.SetComplexitySuggestionJobs(&recordingQueue{}, &inFlightJobs{})

	done := make(chan logstore.ComplexitySuggestionRun, 1)
	go func() {
		got, err := p.CancelComplexitySuggestionRun(context.Background(), captureRouterID, run.ID)
		assert.NoError(t, err)
		done <- got
	}()
	select {
	case got := <-done:
		assert.Equal(t, logstore.ComplexitySuggestionRunRunning, got.Status, "the record as it stood")
	case <-time.After(5 * time.Second):
		t.Fatal("the cancel waited past its settle wait on a stalled read")
	}
}

// An inline run started before the job runner was wired (a scheduled run due
// at start-up, say) is still stopped by a cancel once the runner is there:
// the cancel reaches the inline run, not a job that does not exist.
func TestComplexitySuggestionsCancelStopsAnInlineRunOnceJobsAreWired(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	ctx := context.Background()
	defaultWait := suggestionCancelSettleWait
	suggestionCancelSettleWait = 50 * time.Millisecond
	t.Cleanup(func() { suggestionCancelSettleWait = defaultWait })
	run := logstore.ComplexitySuggestionRun{ID: uuid.NewString(), RouterID: captureRouterID, Status: logstore.ComplexitySuggestionRunRunning,
		Stage: logstore.ComplexitySuggestionRunStageSuggesting, StartedAt: time.Now()}
	require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, &run))
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	p.suggestions.inlineMu.Lock()
	p.suggestions.inline[captureRouterID] = true
	p.suggestions.inlineCancels[run.ID] = stop
	p.suggestions.inlineMu.Unlock()
	queue := &recordingQueue{}
	p.SetComplexitySuggestionJobs(queue, &inFlightJobs{})

	_, err := p.CancelComplexitySuggestionRun(ctx, captureRouterID, run.ID)
	require.NoError(t, err)
	require.ErrorIs(t, runCtx.Err(), context.Canceled, "the inline run is told to stop")
	assert.True(t, p.suggestionRunCancelled(ctx, run.ID), "and records itself as cancelled when it does")
	assert.Empty(t, queue.cancelled, "there is no job to cancel")
}

// A run that stopped early reads as cancelled only when a user cancelled it:
// its job row says so on any node, or this node cancelled its inline run.
func TestComplexitySuggestionRunCancelled(t *testing.T) {
	p, _, _ := suggestionPlugin(t)
	ctx := context.Background()
	assert.False(t, p.suggestionRunCancelled(ctx, "a"), "no job runner and no cancel")

	p.suggestions.inlineMu.Lock()
	p.suggestions.cancelled["inline"] = true
	p.suggestions.inlineMu.Unlock()
	assert.True(t, p.suggestionRunCancelled(ctx, "inline"))

	p.SetComplexitySuggestionJobs(&recordingQueue{}, &inFlightJobs{byID: map[string]*tables.TableSidekiqJob{
		"cancelled": {ID: "cancelled", Status: tables.SidekiqStatusCancelled},
		"lost":      {ID: "lost", Status: tables.SidekiqStatusRunning},
	}})
	assert.True(t, p.suggestionRunCancelled(ctx, "cancelled"))
	assert.False(t, p.suggestionRunCancelled(ctx, "lost"), "a run stopped by a shutdown or a lost claim failed")
	assert.False(t, p.suggestionRunCancelled(ctx, "unknown"))
}

// A replay routes the router's captured requests against a draft bank:
// dropping the simple phrase moves every captured simple request.
func TestComplexitySuggestionsImpact(t *testing.T) {
	p, _, _ := suggestionPlugin(t)
	ctx := context.Background()
	live := []complexity.SuggestionPhrase{{Tier: "SIMPLE", Text: "simple exemplar"}, {Tier: "MEDIUM", Text: "medium exemplar"}, {Tier: "COMPLEX", Text: "complex exemplar"}}
	got, window, err := p.ComplexitySuggestionImpact(ctx, captureRouterID, 0, live, nil)
	require.NoError(t, err)
	assert.Equal(t, 7*24*time.Hour, window, "no window reads the whole lookback")
	assert.Equal(t, 6, got.Requests)
	assert.Equal(t, map[string]int{"SIMPLE": 6}, got.Live)
	assert.Equal(t, got.Live, got.Draft, "the live bank as a draft changes nothing")
	assert.Empty(t, got.Changed)

	got, _, err = p.ComplexitySuggestionImpact(ctx, captureRouterID, time.Hour, live[1:], nil)
	require.NoError(t, err)
	assert.Zero(t, got.Draft["SIMPLE"])
	assert.Len(t, got.Changed, 6)

	_, _, err = p.ComplexitySuggestionImpact(ctx, captureRouterID, 30*24*time.Hour, live, nil)
	require.ErrorIs(t, err, ErrComplexitySuggestionWindow)
	_, _, err = p.ComplexitySuggestionImpact(ctx, "office", 0, live, nil)
	require.ErrorIs(t, err, ErrUnknownComplexityRouter)
}

// Testing a phrase uses the serving bank and the router's own floor.
func TestComplexitySuggestionsTestPhrases(t *testing.T) {
	p, _, _ := suggestionPlugin(t)
	got, err := p.TestComplexitySuggestionPhrases(context.Background(), captureRouterID,
		[]complexity.SuggestionPhrase{{Tier: "COMPLEX", Text: "simple request"}},
		[]suggestrun.TestExample{{LogID: "log-0", Text: "simple request"}})
	require.NoError(t, err)
	require.Len(t, got.Examples, 1)
	assert.Equal(t, "SIMPLE", got.Examples[0].Current.Tier)
	assert.Equal(t, "simple exemplar", got.Phrases[0].Nearest.Phrase, "the tested phrase duplicates a live one")
	assert.InDelta(t, 1, got.Phrases[0].Nearest.Similarity, 1e-6)
	assert.Equal(t, 1, p.ComplexitySuggestionMessageHistoryCount(captureRouterID))
}

// An estimate counts the window's captured requests and bounds what a run of
// the current settings would do with them.
func TestComplexitySuggestionsEstimate(t *testing.T) {
	p, _, _ := suggestionPlugin(t)
	ctx := context.Background()
	got, err := p.EstimateComplexitySuggestionRun(ctx, captureRouterID, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 6, got.Requests)
	assert.Equal(t, 6, got.Clustered)
	assert.Equal(t, 9, got.MaxModelCalls, "6 requests make at most 3 clusters of 2, three calls each")

	_, err = p.EstimateComplexitySuggestionRun(ctx, captureRouterID, 8*24*time.Hour)
	require.ErrorIs(t, err, ErrComplexitySuggestionWindow)
	_, err = p.EstimateComplexitySuggestionRun(ctx, "office", 0)
	require.ErrorIs(t, err, ErrUnknownComplexityRouter)
}

type recordedChat struct {
	model, system, user string
}

// recordChats answers phrase calls with one phrase and tier calls (to
// tierModel) with tier, recording every call.
func recordChats(p *RoutingPlugin, tierModel, tier string) *[]recordedChat {
	var mu sync.Mutex
	calls := &[]recordedChat{}
	p.SetChatRequestExecutor(func(_ *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		mu.Lock()
		*calls = append(*calls, recordedChat{model: string(req.Provider) + "/" + req.Model, system: *req.Input[0].Content.ContentStr, user: *req.Input[1].Content.ContentStr})
		mu.Unlock()
		if req.Model == tierModel {
			return chatResponseWithText(fmt.Sprintf(`{"tier":%q,"confidence":"high","reason":"tier step"}`, tier)), nil
		}
		return chatResponseWithText(`{"coherent":true,"phrases":["a short question"]}`), nil
	})
	return calls
}

// An llm tier step runs on its own model after the phrase step. Unedited, it
// sends the prompt rendered from the router, with the router's own phrases as
// examples of each tier; that is exactly what the editor is shown. The phrase
// step is no longer asked about tiers.
func TestComplexitySuggestionsTierStepWithLLM(t *testing.T) {
	settings := suggestionTestSettings()
	settings.Tier = &configstore.ComplexitySuggestionsTierConfig{Classifier: "llm", Provider: "openai", Model: "gpt-tier"}
	p, store, _ := suggestionPluginWith(t, settings)
	calls := recordChats(p, "gpt-tier", "MEDIUM")
	_, err := p.StartComplexitySuggestionRunNow(context.Background(), captureRouterID, 0)
	require.NoError(t, err)
	run := waitForRun(t, store)
	require.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status, run.Error)

	var phrase, tier []recordedChat
	for _, c := range *calls {
		if c.model == "openai/gpt-tier" {
			tier = append(tier, c)
		} else {
			phrase = append(phrase, c)
		}
	}
	require.NotEmpty(t, phrase)
	require.Len(t, tier, 1, "one tier call for the one cluster")
	assert.Equal(t, "openai/gpt-test", phrase[0].model)
	assert.NotContains(t, phrase[0].system, "Tier definitions")
	rendered, _ := SuggestionTierDefaults(captureTestConfig(settings))
	assert.True(t, strings.HasPrefix(tier[0].system, rendered), "an unedited run sends the rendered prompt the editor shows")
	for _, want := range []string{suggest.DefaultTierInstructions(), "Tier definitions:", "SIMPLE:\n- simple exemplar", "MEDIUM:\n- medium exemplar", "COMPLEX:\n- complex exemplar"} {
		assert.Contains(t, tier[0].system, want)
	}
	assert.Contains(t, tier[0].user, "simple request")

	rows, err := store.ListComplexitySuggestions(context.Background(), logstore.ComplexitySuggestionFilter{RouterID: captureRouterID})
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		assert.Equal(t, "MEDIUM", row.Tier, "the tier comes from the tier step")
	}
}

// A decision tier step asks the decision model one choice question: the tier
// prompt as its instructions, and the router's definitions with its own
// phrases (never the shipped defaults) as each tier's examples. The chat model
// only writes phrases.
func TestComplexitySuggestionsTierStepWithDecisionModel(t *testing.T) {
	settings := suggestionTestSettings()
	settings.Tier = &configstore.ComplexitySuggestionsTierConfig{Classifier: "decision", Provider: "typesafe", Model: "jev-latest",
		Instructions: "Which tier does this request type need?",
		Criteria: map[string]configstore.ComplexityDecisionTierCriteria{
			"COMPLEX": {Definition: "Multi-step design work.", Examples: []string{"design our failover"}},
		}}
	p, store, _ := suggestionPluginWith(t, settings)
	calls := recordChats(p, "never", "")
	var mu sync.Mutex
	var asked []*schemas.BifrostDecisionRequest
	p.SetDecisionRequestExecutor(func(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		mu.Lock()
		asked = append(asked, req)
		mu.Unlock()
		resp := decisionChoiceResponse("COMPLEX")
		confidence := 0.9
		resp.Answers[0].Confidence = &confidence
		return resp, nil
	})
	_, err := p.StartComplexitySuggestionRunNow(context.Background(), captureRouterID, 0)
	require.NoError(t, err)
	run := waitForRun(t, store)
	require.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status, run.Error)

	for _, c := range *calls {
		assert.Equal(t, "openai/gpt-test", c.model, "only the phrase step uses the chat model")
	}
	require.Len(t, asked, 1)
	req := asked[0]
	assert.Equal(t, schemas.ModelProvider("typesafe"), req.Provider)
	assert.Equal(t, "jev-latest", req.Model)
	require.Len(t, req.Questions, 1)
	q := req.Questions[0]
	assert.Equal(t, schemas.DecisionTypeChoice, q.Type)
	require.NotNil(t, q.Instructions)
	assert.Equal(t, "Which tier does this request type need?", *q.Instructions.Text, "edited instructions are sent as written")
	require.Len(t, q.Choices, 3)
	for i, tier := range []string{"SIMPLE", "MEDIUM", "COMPLEX"} {
		assert.Equal(t, tier, *q.Choices[i].Value.Str)
		described := q.Choices[i].Description.Structured.(map[string]interface{})
		if tier == "COMPLEX" {
			assert.Equal(t, "Multi-step design work.", described["definition"], "an edited tier is sent as written")
			assert.Equal(t, []string{"design our failover"}, described["examples"])
			assert.Empty(t, described["signals"])
			continue
		}
		assert.Equal(t, []string{strings.ToLower(tier) + " exemplar"}, described["examples"], "unedited tiers carry the router's own phrases")
	}
	assert.Contains(t, *req.Input.Messages[0].Content.Text, "simple request")

	rows, err := store.ListComplexitySuggestions(context.Background(), logstore.ComplexitySuggestionFilter{RouterID: captureRouterID})
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		assert.Equal(t, "COMPLEX", row.Tier)
		assert.Equal(t, "high", row.Confidence)
	}
}

// An edited llm prompt is sent as written, in place of everything the router
// would generate; only the fixed output rules follow it.
func TestComplexitySuggestionsTierStepSendsAnEditedPrompt(t *testing.T) {
	settings := suggestionTestSettings()
	edited := "Support requests are SIMPLE.\nExamples:\n- where is my refund"
	settings.Tier = &configstore.ComplexitySuggestionsTierConfig{Classifier: "llm", Provider: "openai", Model: "gpt-tier", Prompt: edited}
	p, store, _ := suggestionPluginWith(t, settings)
	calls := recordChats(p, "gpt-tier", "SIMPLE")
	_, err := p.StartComplexitySuggestionRunNow(context.Background(), captureRouterID, 0)
	require.NoError(t, err)
	run := waitForRun(t, store)
	require.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status, run.Error)
	var tier []recordedChat
	for _, c := range *calls {
		if c.model == "openai/gpt-tier" {
			tier = append(tier, c)
		}
	}
	require.Len(t, tier, 1)
	assert.True(t, strings.HasPrefix(tier[0].system, edited+"\n\n"), tier[0].system)
	assert.NotContains(t, tier[0].system, "Tier definitions", "nothing generated is added to an edited prompt")
	assert.NotContains(t, tier[0].system, "simple exemplar")
}

// The rendered defaults carry the router's own phrases as examples, never the
// shipped defaults, and the decision criteria keep the router's definitions
// and signals.
func TestSuggestionTierDefaults(t *testing.T) {
	shipped := complexity.DefaultEditableKeywordConfig()
	cfg := captureTestConfig(nil)
	cfg.Keywords = complexity.EditableKeywordConfig{
		SimpleKeywords:  []string{shipped.SimpleKeywords[0], "check my order status"},
		MediumKeywords:  []string{shipped.MediumKeywords[0]},
		ComplexKeywords: []string{"plan our data migration"},
	}
	cfg.Decision = &complexity.DecisionConfig{Criteria: map[string]configstore.ComplexityDecisionTierCriteria{"SIMPLE": {Definition: "Our own simple definition."}}}
	prompt, criteria := SuggestionTierDefaults(cfg)
	assert.Contains(t, prompt, "SIMPLE:\n- check my order status")
	assert.Contains(t, prompt, "COMPLEX:\n- plan our data migration")
	assert.NotContains(t, prompt, shipped.SimpleKeywords[0])
	assert.NotContains(t, prompt, "MEDIUM:\n-", "a tier with only shipped phrases gets no examples")
	assert.Equal(t, "Our own simple definition.", criteria["SIMPLE"].Definition)
	assert.Equal(t, []string{"check my order status"}, criteria["SIMPLE"].Examples)
	assert.Empty(t, criteria["MEDIUM"].Examples)
	assert.NotEmpty(t, criteria["MEDIUM"].Signals, "signals stay the router's")
}

// A decision-model router's judge is told the router's definitions and
// signals, and as examples only those its administrator wrote, at most
// MaxTierExamples per tier: a judge's criteria, once edited, are stored where
// released gateways read them, and every tier call sends them.
func TestSuggestionTierDefaultsForADecisionRouter(t *testing.T) {
	shipped := configstore.DefaultComplexityDecisionGuidance().Criteria
	written := make([]string, 20)
	for i := range written {
		written[i] = fmt.Sprintf("Handle routine case %d", i)
	}
	cfg := captureTestConfig(nil)
	cfg.Classifier, cfg.Semantic = complexity.ClassifierDecision, nil
	cfg.Decision = &complexity.DecisionConfig{Criteria: map[string]configstore.ComplexityDecisionTierCriteria{
		"SIMPLE": {Examples: append([]string{shipped["SIMPLE"].Examples[0]}, written...)},
	}}
	prompt, criteria := SuggestionTierDefaults(cfg)
	assert.Equal(t, written[:suggest.MaxTierExamples], criteria["SIMPLE"].Examples, "the administrator's examples, shipped ones left out, bounded")
	assert.Empty(t, criteria["MEDIUM"].Examples, "a tier with only shipped examples gets none")
	assert.Equal(t, shipped["MEDIUM"].Signals, criteria["MEDIUM"].Signals)
	assert.Contains(t, prompt, "- Handle routine case 0")
	assert.NotContains(t, prompt, written[suggest.MaxTierExamples])
	for tier, c := range criteria {
		assert.LessOrEqual(t, len(c.Examples), configstore.MaxComplexityDecisionReleasedItems, tier)
	}
}

func TestDecisionConfidenceLabel(t *testing.T) {
	for value, want := range map[float64]string{0.95: "high", 0.75: "high", 0.5: "medium", 0.1: "low", 1.5: "", -0.2: ""} {
		assert.Equal(t, want, decisionConfidenceLabel(value), "%v", value)
	}
}

// With Chromem as Bifrost's vector store nothing is captured, so a suggestion
// run has nothing to read: estimating and starting one are refused with the
// reason, and the schedule neither claims its runs nor records one, failed or
// skipped, every interval.
func TestComplexitySuggestionsRefusedOnChromem(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	settings := suggestionTestSettings()
	settings.Schedule = hourlyFrom(start)
	p := newCapturePluginWithStore(t, settings, false)
	p.startComplexitySuggestions(context.Background(), 0)
	store := suggestionTestStore(t)
	p.SetComplexitySuggestionStore(store, func(context.Context, []string, int) (map[string]string, error) { return nil, nil })
	ctx := context.Background()

	_, err := p.EstimateComplexitySuggestionRun(ctx, captureRouterID, 0)
	require.ErrorIs(t, err, complexity.ErrNoCaptureStore)
	_, err = p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	require.ErrorIs(t, err, complexity.ErrNoCaptureStore)

	schedules := suggestionScheduleStore(t)
	p.SetComplexitySuggestionSchedules(schedules)
	for _, at := range []time.Time{start.Add(10 * time.Minute), start.Add(time.Hour + time.Second), start.Add(2*time.Hour + time.Second)} {
		require.NoError(t, p.scheduleComplexitySuggestions(ctx, at))
	}
	p.suggestions.wg.Wait()
	runs, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{}, 10)
	require.NoError(t, err)
	assert.Empty(t, runs)
	row, err := schedules.GetComplexitySuggestionSchedule(ctx, captureRouterID)
	require.NoError(t, err)
	assert.Nil(t, row, "nothing claimed, so a capture store added later starts the schedule fresh")

	// A run started anyway reads no space.
	_, err = p.runComplexitySuggestions(ctx, complexitySuggestionJobMeta{
		RunID: uuid.NewString(), RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule})
	require.ErrorIs(t, err, complexity.ErrNoCaptureStore)
}

// A user's cancel that reaches an inline run before it starts leaves the run
// recorded as cancelled, not stuck as queued or recorded as failed.
func TestComplexitySuggestionsCancelledBeforeStartIsRecordedCancelled(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	meta := complexitySuggestionJobMeta{RunID: uuid.NewString(), RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerManual}
	require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, meta))
	p.suggestions.inlineMu.Lock()
	p.suggestions.cancelled[meta.RunID] = true
	p.suggestions.inlineMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.runComplexitySuggestions(ctx, meta)
	require.NoError(t, err, "a cancel is not a failure")
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunCancelled, runs[0].Status)
	require.NotNil(t, runs[0].FinishedAt)
}

// hookQueue cancels a job with a callback, to put something between the
// queue's cancel and the run record's write.
type hookQueue struct {
	cancel func(ctx context.Context, id string) (bool, error)
}

func (hookQueue) EnqueuePartitioned(context.Context, string, string, string, string, string) error {
	return nil
}

func (q hookQueue) Cancel(ctx context.Context, id string) (bool, error) { return q.cancel(ctx, id) }

// The cancelled record is written even when the caller's context ends between
// the queue's cancel and the write: a request that went away must not leave
// the run reading queued.
func TestComplexitySuggestionsCancelIsRecordedAfterTheCallerLeaves(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	meta := complexitySuggestionJobMeta{RunID: uuid.NewString(), RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerManual}
	require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, meta))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.SetComplexitySuggestionJobs(hookQueue{cancel: func(context.Context, string) (bool, error) {
		cancel()
		return true, nil
	}}, &inFlightJobs{})

	_, err := p.CancelComplexitySuggestionRun(ctx, captureRouterID, meta.RunID)
	require.NoError(t, err)
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunCancelled, runs[0].Status)
	require.NotNil(t, runs[0].FinishedAt)
}

// A run that ended while its cancel was on the way keeps its outcome: the
// cancel does not overwrite it with the earlier queued snapshot.
func TestComplexitySuggestionsCancelKeepsAnOutcomeThatLandedFirst(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	meta := complexitySuggestionJobMeta{RunID: uuid.NewString(), RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerManual}
	require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, meta))
	p.SetComplexitySuggestionJobs(hookQueue{cancel: func(ctx context.Context, id string) (bool, error) {
		// The job finishes before the cancel reaches it.
		finished := time.Now().UTC()
		require.NoError(t, store.UpsertComplexitySuggestionRun(ctx, &logstore.ComplexitySuggestionRun{ID: id, RouterID: meta.RouterID,
			TriggeredBy: meta.TriggeredBy, Status: logstore.ComplexitySuggestionRunSucceeded, StartedAt: finished, WindowStart: finished,
			FinishedAt: &finished, Created: 3, Cost: 0.25}))
		return false, nil
	}}, &inFlightJobs{})

	run, err := p.CancelComplexitySuggestionRun(context.Background(), captureRouterID, meta.RunID)
	require.ErrorIs(t, err, ErrComplexitySuggestionRunEnded)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, run.Status)
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, runs[0].Status, "the outcome is kept")
	assert.EqualValues(t, 3, runs[0].Created)
	assert.InDelta(t, 0.25, runs[0].Cost, 1e-9)
}

// The tier step's prompt hash key is the same on every run of the same
// settings: the decision criteria are a map, serialized in key order.
func TestComplexitySuggestionTierKeyIsStable(t *testing.T) {
	p := &RoutingPlugin{}
	router := suggestionRouter{id: "r", config: &complexity.AnalyzerConfig{Suggestions: &configstore.ComplexitySuggestionsConfig{
		Enabled: true, Tier: &configstore.ComplexitySuggestionsTierConfig{Classifier: configstore.ComplexitySuggestionsTierDecision, Provider: "typesafe", Model: "jev-latest"},
	}}}
	_, first, _, err := p.newSuggestionTierDecider(router)
	require.NoError(t, err)
	require.NotEmpty(t, first)
	for range 50 {
		_, key, _, err := p.newSuggestionTierDecider(router)
		require.NoError(t, err)
		require.Equal(t, first, key)
	}
}

// Testing phrases reads only the serving bank, so it works on a router whose
// gateway has no shared store to capture traffic in.
func TestComplexitySuggestionPhrasesNeedNoCaptureStore(t *testing.T) {
	p := semanticClassifyTestPlugin(t) // no configured vector store
	result, err := p.TestComplexitySuggestionPhrases(t.Context(), classifyTestRouterID,
		[]complexity.SuggestionPhrase{{Tier: complexity.TierSimple, Text: "a casual greeting, again"}},
		[]suggestrun.TestExample{{LogID: "1", Text: "hey, quick question"}})
	require.NoError(t, err)
	require.Len(t, result.Examples, 1)
}

// runOutcomeRace writes a run's outcome right after the cancel path's first
// read of it, before the cancel is written, as a run finishing on another node
// at that moment would.
type runOutcomeRace struct {
	logstore.LogStore
	reads   int
	outcome *logstore.ComplexitySuggestionRun
}

func (s *runOutcomeRace) ListComplexitySuggestionRuns(ctx context.Context, filter logstore.ComplexitySuggestionRunFilter, limit int) ([]logstore.ComplexitySuggestionRun, error) {
	runs, err := s.LogStore.ListComplexitySuggestionRuns(ctx, filter, limit)
	s.reads++
	if s.reads == 1 && s.outcome != nil {
		if writeErr := s.LogStore.UpsertComplexitySuggestionRun(ctx, s.outcome); writeErr != nil {
			return nil, writeErr
		}
	}
	return runs, err
}

// A run that finishes however close to its cancel keeps its outcome: the
// cancel only applies to a run that is still queued or running when it is
// written, in one conditional write, so nothing can land between a check and
// the write.
func TestComplexitySuggestionsCancelNeverOverwritesAFinishedRun(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	meta := complexitySuggestionJobMeta{RunID: uuid.NewString(), RouterID: captureRouterID, TriggeredBy: logstore.ComplexitySuggestionTriggerManual}
	require.NoError(t, recordQueuedSuggestionRun(context.Background(), store, meta))
	finished := time.Now().UTC()
	race := &runOutcomeRace{LogStore: store, outcome: &logstore.ComplexitySuggestionRun{ID: meta.RunID, RouterID: meta.RouterID,
		TriggeredBy: meta.TriggeredBy, Status: logstore.ComplexitySuggestionRunSucceeded, StartedAt: finished, WindowStart: finished,
		FinishedAt: &finished, Created: 2, Cost: 0.1}}
	p.SetComplexitySuggestionStore(race, func(context.Context, []string, int) (map[string]string, error) { return nil, nil })
	p.SetComplexitySuggestionJobs(hookQueue{cancel: func(context.Context, string) (bool, error) { return false, nil }}, &inFlightJobs{})

	_, err := p.CancelComplexitySuggestionRun(context.Background(), captureRouterID, meta.RunID)
	require.ErrorIs(t, err, ErrComplexitySuggestionRunEnded, "the conditional write finds the run already ended")
	runs := runsOf(t, store)
	require.Len(t, runs, 1)
	assert.Equal(t, logstore.ComplexitySuggestionRunSucceeded, runs[0].Status, "the outcome that landed is kept")
	assert.EqualValues(t, 2, runs[0].Created)
}

// decisionEmbeddings answers embedding requests in two dimensions and
// records every text it was asked to embed.
type decisionEmbeddings struct {
	mu    sync.Mutex
	texts []string
}

func (e *decisionEmbeddings) execute(_ *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if req.Provider != "openai" || req.Model != "test-embedding-model" {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "unexpected embedding model " + req.Model}}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	resp := &schemas.BifrostEmbeddingResponse{Usage: &schemas.BifrostLLMUsage{TotalTokens: len(req.Input)}}
	for i, item := range req.Input {
		text := *item.Content[0].Text
		e.texts = append(e.texts, text)
		vector := []float64{1, 0}
		switch {
		case strings.Contains(text, "hard"):
			vector = []float64{0, 1}
		case strings.Contains(text, "short question"):
			vector = []float64{0.6, 0.8}
		}
		resp.Data = append(resp.Data, schemas.EmbeddingData{Index: i, Embedding: schemas.EmbeddingStruct{EmbeddingArray: vector}})
	}
	return resp, nil
}

func (e *decisionEmbeddings) take() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	texts := e.texts
	e.texts = nil
	return texts
}

// countedRouterLogs counts the passes over a router's logs.
type countedRouterLogs struct {
	logstore.LogStore
	reads atomic.Int64
}

func (l *countedRouterLogs) EachComplexityRouterLog(ctx context.Context, routerID string, from, to time.Time, fn func([]logstore.ComplexityRouterLog) error) error {
	l.reads.Add(1)
	return l.LogStore.EachComplexityRouterLog(ctx, routerID, from, to, fn)
}

// addDecisionLog writes a request the decision router took, with the tier
// and mechanism it logged (none when empty).
func addDecisionLog(t *testing.T, store logstore.LogStore, id, routerID string, at time.Time, tier, mechanism string) {
	t.Helper()
	entry := &logstore.Log{ID: id, Timestamp: at, CreatedAt: at, Object: "chat_completion", Provider: "openai", Model: "gpt-4o", Status: "success", ModelRouterID: &routerID}
	if tier != "" {
		entry.ComplexityTier = &tier
	}
	if mechanism != "" {
		entry.ComplexityMechanism = &mechanism
	}
	require.NoError(t, store.Create(context.Background(), entry))
}

// A decision-model router's suggestion source: its examples, as its decision
// model reads them, embedded with its suggestions embedding model; a backfill
// from its logs of only the requests its decision model logged a tier for,
// with the turns that model reads; and those requests read back with that
// tier. Examples embedded once are not embedded again until edited, and are
// forgotten with the router.
func TestComplexitySuggestionDecisionRouterSource(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	reads := &textReads{}
	var historyCounts []int
	logs := &countedRouterLogs{LogStore: store}
	p.SetComplexitySuggestionStore(logs, func(ctx context.Context, ids []string, count int) (map[string]string, error) {
		historyCounts = append(historyCounts, count)
		return reads.read(ctx, ids, count)
	})
	embeddings := &decisionEmbeddings{}
	p.SetEmbeddingRequestExecutor(embeddings.execute)
	ctx := context.Background()

	settings := suggestionTestSettings()
	settings.Embedding = &configstore.ComplexitySuggestionsEmbedding{Provider: "openai", Model: "test-embedding-model"}
	previous := 2
	const routerID = "decision-router"
	cfg := serveDecisionRouter(t, p, routerID, settings, &complexity.DecisionConfig{PreviousMessageCount: &previous, Criteria: map[string]configstore.ComplexityDecisionTierCriteria{
		complexity.TierComplex: {Examples: []string{"a hard proof"}},
	}})
	router, _ := p.suggestionRouter(routerID)
	assert.Equal(t, 3, p.ComplexitySuggestionMessageHistoryCount(routerID), "the decision model reads two previous turns and the current one")

	space, err := p.decisionSuggestionSpace(ctx, router)
	require.NoError(t, err)
	criteria := cfg.Decision.ResolvedCriteria()
	examples := len(criteria[complexity.TierSimple].Examples) + len(criteria[complexity.TierMedium].Examples) + 1
	require.Len(t, space.LivePhrases(), examples, "the shipped examples of the tiers not edited, and the edited one")
	assert.Contains(t, space.LivePhrases(), complexity.SuggestionPhrase{Tier: complexity.TierComplex, Text: "a hard proof", Vector: []float32{0, 1}})
	assert.Len(t, embeddings.take(), examples)
	assert.Equal(t, complexity.CaptureNamespace(routerID, "openai", "test-embedding-model", 2, 3), space.Namespace())

	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-time.Hour)
	addDecisionLog(t, store, "decided-simple", routerID, at, complexity.TierSimple, complexity.MechanismDecision)
	addDecisionLog(t, store, "decided-complex", routerID, at, complexity.TierComplex, complexity.MechanismDecision)
	addDecisionLog(t, store, "session-pinned", routerID, at, complexity.TierComplex, complexity.MechanismSession)
	addDecisionLog(t, store, "no-tier", routerID, at, "", complexity.MechanismSkipped)
	addDecisionLog(t, store, "semantic-before", routerID, at, complexity.TierMedium, complexity.MechanismSemantic)
	addDecisionLog(t, store, "theirs", captureRouterID, at, complexity.TierSimple, complexity.MechanismDecision)

	written, err := p.backfillWindow(ctx, router, space, p.suggestionDeps(), now.Add(-24*time.Hour), now)
	require.NoError(t, err)
	assert.Equal(t, 2, written)
	assert.Equal(t, []string{"decided-complex", "decided-simple"}, reads.take(), "only requests the decision model logged a tier for are read")
	assert.Equal(t, []int{3}, historyCounts, "with the turns the decision model reads")
	assert.Len(t, embeddings.take(), 2)

	got := map[string]string{}
	require.NoError(t, space.EachCaptured(ctx, now.Add(-24*time.Hour), now, func(page []complexity.CapturedRequest) error {
		for _, r := range page {
			got[r.LogID] = r.Tier
		}
		return nil
	}))
	assert.Equal(t, map[string]string{"decided-simple": complexity.TierSimple, "decided-complex": complexity.TierComplex}, got)
	require.NoError(t, space.EachCaptured(ctx, now.Add(-24*time.Hour), now, func([]complexity.CapturedRequest) error { return nil }))
	assert.EqualValues(t, 1, logs.reads.Load(), "the backfill's pass over the logs is the only one: reading the window twice reads them no more")

	// A second space embeds nothing it already has; an edited example alone.
	_, err = p.decisionSuggestionSpace(ctx, router)
	require.NoError(t, err)
	assert.Empty(t, embeddings.take())
	cfg.Decision.Criteria = map[string]configstore.ComplexityDecisionTierCriteria{complexity.TierComplex: {Examples: []string{"a hard proof", "another hard one"}}}
	require.NoError(t, p.ReloadModelRouter(&configstore.ModelRouter{ID: routerID, Name: routerID, ComplexityAnalyzerConfig: *cfg}))
	router, _ = p.suggestionRouter(routerID)
	_, err = p.decisionSuggestionSpace(ctx, router)
	require.NoError(t, err)
	assert.Equal(t, []string{"another hard one"}, embeddings.take())

	require.NoError(t, p.RemoveModelRouter(routerID))
	_, cached := p.suggestions.examples.Load(routerID)
	assert.False(t, cached, "a removed router's examples are forgotten")
}

// A decision-model router's space needs its embedding model and the
// embedding path; a semantic router has no decision space.
func TestComplexitySuggestionDecisionSpaceNeedsAnEmbeddingModel(t *testing.T) {
	p, _, _ := suggestionPlugin(t)
	ctx := context.Background()
	semantic, _ := p.suggestionRouter(captureRouterID)
	_, err := p.decisionSuggestionSpace(ctx, semantic)
	require.ErrorIs(t, err, complexity.ErrNoDecisionEmbedding)

	settings := suggestionTestSettings()
	settings.Embedding = &configstore.ComplexitySuggestionsEmbedding{Provider: "openai", Model: "test-embedding-model"}
	serveDecisionRouter(t, p, "decision-router", settings, nil)
	router, _ := p.suggestionRouter("decision-router")
	_, err = p.decisionSuggestionSpace(ctx, router)
	require.ErrorIs(t, err, ErrEmbeddingRequestExecutorNotConfigured, "no embedding path wired yet")
}

// The examples test asks the router's own decision model about each request
// twice, exactly as routing asks, with the router's examples and with the
// draft added, and reports which requests change tier. The draft is checked
// as accepting it would be; the test is for decision-model routers only.
func TestComplexitySuggestionExamplesTest(t *testing.T) {
	p, _, _ := suggestionPlugin(t)
	ctx := context.Background()
	settings := suggestionTestSettings()
	settings.Embedding = &configstore.ComplexitySuggestionsEmbedding{Provider: "openai", Model: "test-embedding-model"}
	serveDecisionRouter(t, p, "decision-router", settings, &complexity.DecisionConfig{Provider: "typesafe", Model: "jev-latest"})
	requests := []suggestrun.TestExample{{LogID: "a", Text: "where is my refund"}, {LogID: "b", Text: "plan our migration"}}
	draft := []complexity.SuggestionPhrase{{Tier: complexity.TierSimple, Text: "Check the status of a refund"}}

	_, err := p.TestComplexitySuggestionExamples(ctx, "decision-router", draft, nil, requests)
	require.ErrorIs(t, err, ErrDecisionRequestExecutorNotConfigured)

	var mu sync.Mutex
	var asked []*schemas.BifrostDecisionRequest
	p.SetDecisionRequestExecutor(func(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		mu.Lock()
		asked = append(asked, req)
		mu.Unlock()
		// The refund request is SIMPLE once the draft example describes it.
		simple := req.Questions[0].Choices[0].Description.Structured.(map[string]interface{})
		text := *req.Input.Messages[0].Content.Text
		if strings.Contains(text, "refund") && strings.Contains(strings.Join(simple["examples"].([]string), "\n"), "Check the status of a refund") {
			return decisionChoiceResponse(complexity.TierSimple), nil
		}
		if signals, _ := simple["signals"].([]string); strings.Contains(text, "migration") && slices.Contains(signals, "Recall one documented procedure") {
			return decisionChoiceResponse(complexity.TierSimple), nil
		}
		return decisionChoiceResponse(complexity.TierComplex), nil
	})
	result, err := p.TestComplexitySuggestionExamples(ctx, "decision-router", draft, nil, requests)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Changed)
	require.Len(t, result.Requests, 2)
	assert.Equal(t, suggestrun.TestedRequest{LogID: "a", Text: "where is my refund", Current: suggestrun.DecisionRouting{Tier: complexity.TierComplex},
		With: suggestrun.DecisionRouting{Tier: complexity.TierSimple}, Changed: true}, result.Requests[0])
	assert.False(t, result.Requests[1].Changed)
	require.Len(t, asked, 4, "two calls per request")
	for _, req := range asked {
		assert.Equal(t, schemas.ModelProvider("typesafe"), req.Provider, "the router's own decision model")
		assert.Equal(t, "jev-latest", req.Model)
		assert.Equal(t, routerDecisionRequest(req.Provider, req.Model, nil, nil).Questions[0].Instructions, req.Questions[0].Instructions, "asked as routing asks")
		assert.Equal(t, []string{complexity.TierSimple, complexity.TierMedium, complexity.TierComplex}, choiceTiers(req.Questions[0]))
	}

	_, err = p.TestComplexitySuggestionExamples(ctx, "decision-router", []complexity.SuggestionPhrase{{Tier: complexity.TierSimple, Text: configstore.DefaultComplexityDecisionGuidance().Criteria[complexity.TierComplex].Examples[0]}}, nil, requests)
	require.ErrorIs(t, err, suggestrun.ErrInvalidPhraseTest, "another tier's example")
	_, err = p.TestComplexitySuggestionExamples(ctx, "decision-router", nil, nil, requests)
	require.ErrorIs(t, err, suggestrun.ErrInvalidPhraseTest)

	// A draft signal is tried the same way.
	asked = nil
	result, err = p.TestComplexitySuggestionExamples(ctx, "decision-router", nil, []complexity.SuggestionPhrase{{Tier: complexity.TierSimple, Text: "Recall one documented procedure"}}, requests)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Changed)
	assert.True(t, result.Requests[1].Changed, "the migration request moves once the signal describes it")
	assert.False(t, result.Requests[0].Changed)
	_, err = p.TestComplexitySuggestionExamples(ctx, "decision-router", nil, []complexity.SuggestionPhrase{{Tier: complexity.TierSimple, Text: configstore.DefaultComplexityDecisionGuidance().Criteria[complexity.TierComplex].Signals[0]}}, requests)
	require.ErrorIs(t, err, suggestrun.ErrInvalidPhraseTest, "another tier's signal")
	_, err = p.TestComplexitySuggestionExamples(ctx, captureRouterID, draft, nil, requests)
	require.ErrorIs(t, err, ErrComplexitySuggestionsClassifier, "a semantic router tests phrases")
}

// A router's runs cluster with the same seed every time, so a scheduled run
// over the same traffic refreshes its suggestions instead of replacing them;
// routers do not share one.
func TestComplexitySuggestionRunsKeepTheirSeed(t *testing.T) {
	p, _, _ := suggestionPlugin(t)
	router, ok := p.suggestionRouter(captureRouterID)
	require.True(t, ok)
	first := runSettings(router, "run-1", logstore.ComplexitySuggestionTriggerSchedule, 0)
	time.Sleep(time.Millisecond)
	second := runSettings(router, "run-2", logstore.ComplexitySuggestionTriggerManual, time.Hour)
	assert.NotZero(t, first.Seed)
	assert.Equal(t, first.Seed, second.Seed)
	other := router
	other.id = "another-router"
	assert.NotEqual(t, first.Seed, runSettings(other, "run-3", logstore.ComplexitySuggestionTriggerSchedule, 0).Seed)
}
