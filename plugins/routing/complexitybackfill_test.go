package routing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addRouterLog writes a request a model router took to the log store.
func addRouterLog(t *testing.T, store logstore.LogStore, id, routerID string, at time.Time) {
	t.Helper()
	router := &routerID
	if routerID == "" {
		router = nil
	}
	require.NoError(t, store.Create(context.Background(), &logstore.Log{
		ID: id, Timestamp: at, CreatedAt: at, Object: "chat_completion", Provider: "openai", Model: "gpt-4o", Status: "success", ModelRouterID: router,
	}))
}

// textReads records the logs whose text was read, and answers for all but
// the hidden ones.
type textReads struct {
	mu     sync.Mutex
	ids    []string
	hidden map[string]bool
	// during, when set, runs before each read answers, as a log deleted or a
	// second backfill started while the text is read would.
	during func(ids []string)
}

func (r *textReads) read(_ context.Context, ids []string, _ int) (map[string]string, error) {
	if r.during != nil {
		r.during(ids)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]string{}
	for _, id := range ids {
		r.ids = append(r.ids, id)
		if !r.hidden[id] {
			out[id] = "simple request"
		}
	}
	return out, nil
}

func (r *textReads) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := r.ids
	r.ids = nil
	slices.Sort(ids)
	return ids
}

// backfillSpace is the test router's suggestion space, closed with the test.
func backfillSpace(t *testing.T, p *RoutingPlugin) *complexity.SuggestionSpace {
	t.Helper()
	space, err := captureRouter(t, p).semantic.SuggestionSpace(context.Background())
	require.NoError(t, err)
	t.Cleanup(space.Close)
	return space
}

// capturedIDs lists the requests captured in the test router's space.
func capturedIDs(t *testing.T, space *complexity.SuggestionSpace, from, to time.Time) map[string]time.Time {
	t.Helper()
	got := map[string]time.Time{}
	require.NoError(t, space.EachCaptured(context.Background(), from, to, func(page []complexity.CapturedRequest) error {
		for _, r := range page {
			got[r.LogID] = r.At
		}
		return nil
	}))
	return got
}

// failingRouterLogs is a log store whose router-log reads fail.
type failingRouterLogs struct{ logstore.LogStore }

func (failingRouterLogs) EachComplexityRouterLog(context.Context, string, time.Time, time.Time, func([]logstore.ComplexityRouterLog) error) error {
	return errors.New("router logs unavailable")
}

// An estimate whose backfill cannot be planned still sizes what is captured,
// and says the backfill is unknown, rather than failing: the run dialog
// starts a run only on an estimate.
func TestComplexitySuggestionEstimateWithUnknownBackfill(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	p.SetComplexitySuggestionStore(failingRouterLogs{store}, (&textReads{}).read)
	estimate, err := p.EstimateComplexitySuggestionRun(context.Background(), captureRouterID, 0)
	require.NoError(t, err)
	assert.True(t, estimate.BackfillUnknown)
	assert.Zero(t, estimate.Backfill)
	assert.Equal(t, 6, estimate.Requests, "what is captured is still counted")
}

// A router's window is filled from its logs before it is read: the requests
// it took before it captured them are embedded and stored under their log
// time, while those it captured live, other routers' requests and logs
// without text are left alone. A second backfill finds nothing to do, so no
// request is embedded twice.
func TestComplexitySuggestionBackfillFillsTheWindowFromLogs(t *testing.T) {
	p, store, _ := suggestionPlugin(t) // samples every request, and has captured six
	reads := &textReads{hidden: map[string]bool{"old-hidden": true}}
	p.SetComplexitySuggestionStore(store, reads.read)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	live := p.ComplexityCaptureStats().Written

	classifyAs(t, p, "live-1")
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == live+1 }, 2*time.Second, 5*time.Millisecond)
	addRouterLog(t, store, "live-1", captureRouterID, now.Add(-time.Minute))
	for _, id := range []string{"old-1", "old-2", "old-hidden"} {
		addRouterLog(t, store, id, captureRouterID, now.Add(-2*24*time.Hour))
	}
	addRouterLog(t, store, "theirs", "other-router", now.Add(-2*24*time.Hour))
	addRouterLog(t, store, "unrouted", "", now.Add(-2*24*time.Hour))

	estimate, err := p.EstimateComplexitySuggestionRun(ctx, captureRouterID, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, estimate.Backfill, "the estimate counts what a run would embed from logs first")
	assert.EqualValues(t, live+1+3, estimate.Requests, "and counts it in the window")
	reads.take()

	space := backfillSpace(t, p)
	router, _ := p.suggestionRouter(captureRouterID)
	from := now.Add(-7 * 24 * time.Hour)
	written, err := p.backfillWindow(ctx, router, space, p.suggestionDeps(), from, now)
	require.NoError(t, err)
	assert.Equal(t, 2, written, "old-1 and old-2; old-hidden has no text")
	assert.Equal(t, []string{"old-1", "old-2", "old-hidden"}, reads.take(), "only the missing requests are read")

	captured := capturedIDs(t, space, from, now.Add(time.Minute))
	assert.Contains(t, captured, "live-1")
	assert.Contains(t, captured, "old-1")
	assert.Contains(t, captured, "old-2")
	assert.NotContains(t, captured, "theirs")
	assert.NotContains(t, captured, "unrouted")
	assert.WithinDuration(t, now.Add(-2*24*time.Hour), captured["old-1"], time.Second, "stored under the log's time")

	written, err = p.backfillWindow(ctx, router, space, p.suggestionDeps(), from, now)
	require.NoError(t, err)
	assert.Zero(t, written)
	assert.Equal(t, []string{"old-hidden"}, reads.take(), "only the request still without text is tried again")
}

// filteredReadStore is a vector store whose filtered reads cannot be relied
// on past a result ceiling, here zero: fail is Weaviate, which refuses the
// read; otherwise it is Redis, which answers with nothing more.
type filteredReadStore struct {
	vectorstore.VectorStore
	fail bool
}

func (s filteredReadStore) GetAll(ctx context.Context, namespace string, queries []vectorstore.Query, selectFields []string, cursor *string, limit int64) ([]vectorstore.SearchResult, *string, error) {
	if len(queries) == 0 {
		return s.VectorStore.GetAll(ctx, namespace, queries, selectFields, cursor, limit)
	}
	if s.fail {
		return nil, nil, errors.New("query maximum results exceeded")
	}
	return nil, nil, nil
}

// A backfill finds which requests are already captured through the store's
// whole walk, never a filtered read: past a backend's result ceiling a
// filtered read fails the run (Weaviate) or ends early (Redis), and a request
// read as missing is embedded again.
func TestComplexitySuggestionBackfillPlansPastAFilteredReadCeiling(t *testing.T) {
	for name, fail := range map[string]bool{"a filtered read that fails": true, "a filtered read that ends early": false} {
		t.Run(name, func(t *testing.T) {
			p, store, _ := suggestionPlugin(t) // samples every request, and has captured six
			p.SetComplexitySuggestionStore(store, (&textReads{}).read)
			now := time.Now().UTC().Truncate(time.Second)
			for i := range 6 {
				addRouterLog(t, store, fmt.Sprintf("log-%d", i), captureRouterID, now.Add(-time.Minute))
			}
			addRouterLog(t, store, "old-1", captureRouterID, now.Add(-2*24*time.Hour))

			space := backfillSpace(t, p)
			space.Store = filteredReadStore{VectorStore: space.Store, fail: fail}
			router, _ := p.suggestionRouter(captureRouterID)
			logs, _ := p.suggestionDeps().store.(ComplexitySuggestionRouterLogs)
			plan, err := p.planBackfill(context.Background(), router, space, logs, now.Add(-7*24*time.Hour), now.Add(time.Minute), backfillLimit(router))
			require.NoError(t, err)
			assert.Equal(t, 1, plan.missing, "only the request never captured is missing")
			require.Len(t, plan.candidates, 1)
			assert.Equal(t, "old-1", plan.candidates[0].ID)
		})
	}
}

// A backfill plans the newest missing requests first, keeping only as many
// candidates as twice its limit however many the window holds, and counts
// every missing request; what is left is filled by a later run.
func TestComplexitySuggestionBackfillTakesTheNewestFirst(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	order := []string{"6-oldest", "5", "4", "3", "2", "1-newest"}
	for i, id := range order {
		addRouterLog(t, store, id, captureRouterID, now.Add(-time.Duration(len(order)-i)*time.Hour))
	}
	space := backfillSpace(t, p)
	router, _ := p.suggestionRouter(captureRouterID)
	plan, err := p.planBackfill(ctx, router, space, store, now.Add(-24*time.Hour), now, 2)
	require.NoError(t, err)
	var ids []string
	for _, l := range plan.candidates {
		ids = append(ids, l.ID)
	}
	assert.Equal(t, []string{"1-newest", "2", "3", "4"}, ids, "the newest four, newest first")
	assert.Equal(t, 6, plan.missing)
}

// The backfill keeps the requests live capture would have sampled at the
// router's rate, and no others.
func TestComplexitySuggestionBackfillSamplesLikeLiveCapture(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	settings := suggestionTestSettings()
	settings.SampleRate = 0.5
	serveCaptureRouter(t, p, captureRouterID, settings)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	var want []string
	for i := range 40 {
		id := "11111111-2222-4333-8444-" + time.Duration(i).String() + "00000000000"
		id = id[:36]
		addRouterLog(t, store, id, captureRouterID, now.Add(-time.Duration(i+1)*time.Minute))
		if complexity.CaptureSampled(id, 0.5) {
			want = append(want, id)
		}
	}
	require.NotEmpty(t, want)
	space := backfillSpace(t, p)
	router, _ := p.suggestionRouter(captureRouterID)
	plan, err := p.planBackfill(ctx, router, space, store, now.Add(-24*time.Hour), now, 1000)
	require.NoError(t, err)
	var got []string
	for _, l := range plan.candidates {
		got = append(got, l.ID)
	}
	assert.ElementsMatch(t, want, got)
}

// A run backfills its window before it reads it, so a router's first run sees
// the requests from before its suggestions were turned on.
func TestComplexitySuggestionRunBackfillsBeforeReading(t *testing.T) {
	p, store, _ := suggestionPlugin(t) // six requests captured live
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	live := p.ComplexityCaptureStats().Written
	for _, id := range []string{"before-1", "before-2", "before-3"} {
		addRouterLog(t, store, id, captureRouterID, now.Add(-3*24*time.Hour))
	}
	_, err := p.StartComplexitySuggestionRunNow(ctx, captureRouterID, 0)
	require.NoError(t, err)
	run := waitForRun(t, store)
	assert.EqualValues(t, live+3, run.RequestsRead, "the run read the backfilled requests with the live ones: %+v", run)
	captured := capturedIDs(t, backfillSpace(t, p), now.Add(-7*24*time.Hour), now.Add(time.Minute))
	for _, id := range []string{"before-1", "before-2", "before-3"} {
		assert.Contains(t, captured, id)
	}
}

// withBackfillLimit is router with its backfill bounded to limit requests.
func withBackfillLimit(router suggestionRouter, limit int) suggestionRouter {
	config := *router.config
	suggestions := *config.Suggestions
	suggestions.MaxClusteredRequests = limit
	config.Suggestions = &suggestions
	router.config = &config
	return router
}

// Logs whose text cannot be read do not hold the backfill's places: when the
// newest are hidden, the older readable ones behind them are written instead,
// and the hidden ones stay candidates for the next backfill.
func TestComplexitySuggestionBackfillPassesOverUnreadableLogs(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	reads := &textReads{hidden: map[string]bool{"hidden-1": true, "hidden-2": true}}
	p.SetComplexitySuggestionStore(store, reads.read)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"hidden-1", "hidden-2", "readable-1", "readable-2"} {
		addRouterLog(t, store, id, captureRouterID, now.Add(-time.Duration(i+1)*time.Hour))
	}
	space := backfillSpace(t, p)
	base, _ := p.suggestionRouter(captureRouterID)
	router := withBackfillLimit(base, 2)
	from := now.Add(-24 * time.Hour)

	written, err := p.backfillWindow(ctx, router, space, p.suggestionDeps(), from, now)
	require.NoError(t, err)
	assert.Equal(t, 2, written, "the two readable requests behind the hidden ones")
	captured := capturedIDs(t, space, from, now.Add(time.Minute))
	assert.Contains(t, captured, "readable-1")
	assert.Contains(t, captured, "readable-2")
	reads.take()

	_, err = p.backfillWindow(ctx, router, space, p.suggestionDeps(), from, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"hidden-1", "hidden-2"}, reads.take(), "a hidden log is tried again")
}

// Two backfills of one router at once embed each request once: the second
// plans after the first has written.
func TestComplexitySuggestionBackfillsOfOneRouterDoNotOverlap(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	var once sync.Once
	second := make(chan error, 1)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	from := now.Add(-24 * time.Hour)
	space := backfillSpace(t, p)
	router, _ := p.suggestionRouter(captureRouterID)
	reads := &textReads{}
	// While the first backfill reads its text, a second one starts.
	reads.during = func([]string) {
		once.Do(func() {
			go func() {
				_, err := p.backfillWindow(ctx, router, space, p.suggestionDeps(), from, now)
				second <- err
			}()
			time.Sleep(50 * time.Millisecond)
		})
	}
	p.SetComplexitySuggestionStore(store, reads.read)
	for _, id := range []string{"old-1", "old-2", "old-3"} {
		addRouterLog(t, store, id, captureRouterID, now.Add(-time.Hour))
	}

	written, err := p.backfillWindow(ctx, router, space, p.suggestionDeps(), from, now)
	require.NoError(t, err)
	assert.Equal(t, 3, written)
	require.NoError(t, <-second)
	assert.Equal(t, []string{"old-1", "old-2", "old-3"}, reads.take(), "each request read, and embedded, once")
}

// A log deleted while its request is being backfilled leaves no vector: the
// delete reaches the backfill however the two interleave.
func TestComplexitySuggestionBackfillOfADeletedLogLeavesNoVector(t *testing.T) {
	p, store, _ := suggestionPlugin(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	from := now.Add(-24 * time.Hour)
	reads := &textReads{during: func(ids []string) {
		if slices.Contains(ids, "deleted") {
			p.ForgetComplexityCapturedRequests(ctx, []string{"deleted"})
		}
	}}
	p.SetComplexitySuggestionStore(store, reads.read)
	addRouterLog(t, store, "deleted", captureRouterID, now.Add(-time.Hour))
	addRouterLog(t, store, "kept", captureRouterID, now.Add(-2*time.Hour))
	space := backfillSpace(t, p)
	router, _ := p.suggestionRouter(captureRouterID)

	_, err := p.backfillWindow(ctx, router, space, p.suggestionDeps(), from, now)
	require.NoError(t, err)
	captured := capturedIDs(t, space, from, now.Add(time.Minute))
	assert.NotContains(t, captured, "deleted", "deleted while its text was read: not written")
	assert.Contains(t, captured, "kept")
}

// The plan's candidate list keeps the newest it is offered, counts a log
// offered twice once, and orders ties by ID.
func TestNewestLogsKeepsTheNewest(t *testing.T) {
	now := time.Now()
	keep := newestLogs{max: 2, held: map[string]struct{}{}}
	for _, l := range []logstore.ComplexityRouterLog{
		{ID: "b", Timestamp: now}, {ID: "old", Timestamp: now.Add(-time.Hour)}, {ID: "a", Timestamp: now}, {ID: "b", Timestamp: now},
	} {
		keep.offer(l)
	}
	var ids []string
	for _, l := range keep.newestFirst() {
		ids = append(ids, l.ID)
	}
	assert.Equal(t, []string{"a", "b"}, ids)
}
