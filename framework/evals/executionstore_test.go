package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeExecutionStore is an in-memory ExecutionStore that filters and orders the way
// the SQL store does.
type fakeExecutionStore struct {
	mu     sync.Mutex
	rows   map[string]*tables.TableEvalExecution
	claims []fakeClaim // the StartEvalExecution calls that held
}

// fakeClaim is one claim that StartEvalExecution granted.
type fakeClaim struct {
	id, claimID          string
	reclaimStartedBefore time.Time
}

func (f *fakeExecutionStore) GetEvalExecution(_ context.Context, id string) (*tables.TableEvalExecution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	copied := *row
	return &copied, nil
}

// StartEvalExecution claims the way the SQL store does: only on the key the execution is
// on, and only a queued one, one running under the same delivery, or one running since
// before reclaimStartedBefore; a recovery claim (recoverStartedAt set) takes only the run
// that started at that instant under that delivery, and keeps that start. It never moves the execution to another key. A row with
// no status stands for a queued one, as CreateEvalExecution stores it.
func (f *fakeExecutionStore) StartEvalExecution(_ context.Context, id, keyID, claimID string, reclaimStartedBefore, recoverStartedAt time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.KeyID != keyID {
		return false, nil
	}
	queued := row.Status == "" || row.Status == tables.EvalExecutionQueued
	reclaimed := row.Status == tables.EvalExecutionRunning && (row.ClaimID == claimID || (row.StartedAt != nil && row.StartedAt.Before(reclaimStartedBefore)))
	if !recoverStartedAt.IsZero() {
		queued = false
		reclaimed = row.Status == tables.EvalExecutionRunning && row.ClaimID == claimID && row.StartedAt != nil && row.StartedAt.Equal(recoverStartedAt)
	}
	if !queued && !reclaimed {
		return false, nil
	}
	row.Status, row.ClaimID = tables.EvalExecutionRunning, claimID
	if recoverStartedAt.IsZero() {
		row.StartedAt = new(time.Now()) // a recovery claim keeps the start it matched
	} else {
		row.ClaimID = configstore.EvalEndLostClaim // and moves the claim to the sweep's own ID
	}
	f.claims = append(f.claims, fakeClaim{id: id, claimID: claimID, reclaimStartedBefore: reclaimStartedBefore})
	return true, nil
}

func (f *fakeExecutionStore) CompleteEvalExecution(_ context.Context, id string, result json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return configstore.ErrNotFound
	}
	if row.Status == tables.EvalExecutionCompleted {
		return nil // the first result stands, as in the SQL store
	}
	row.Status, row.Result = tables.EvalExecutionCompleted, result
	return nil
}

// CompleteQueuedEvalExecution completes a row only while it is queued on keyID, as the SQL
// store does.
func (f *fakeExecutionStore) CompleteQueuedEvalExecution(_ context.Context, id, keyID string, result json.RawMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return false, configstore.ErrNotFound
	}
	if (row.Status != "" && row.Status != tables.EvalExecutionQueued) || row.KeyID != keyID {
		return false, nil
	}
	row.Status, row.Result = tables.EvalExecutionCompleted, result
	return true, nil
}

func (f *fakeExecutionStore) RouteEvalExecution(_ context.Context, id, fromKey, toKey string, routedAt time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok || row.Status == tables.EvalExecutionRunning || row.Status == tables.EvalExecutionCompleted || row.KeyID != fromKey {
		return false, nil
	}
	row.KeyID, row.RoutedAt = toKey, new(routedAt)
	return true, nil
}

// ordered returns the rows oldest first, by creation time and then ID.
func (f *fakeExecutionStore) ordered() []*tables.TableEvalExecution {
	out := make([]*tables.TableEvalExecution, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b *tables.TableEvalExecution) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func (f *fakeExecutionStore) ListQueuedEvalExecutions(_ context.Context, createdBefore, routedBefore time.Time, skipKeys []string, afterCreatedAt time.Time, afterID string, limit int) ([]tables.TableEvalExecution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tables.TableEvalExecution
	for _, row := range f.ordered() {
		if afterID != "" && (row.CreatedAt.Before(afterCreatedAt) || (row.CreatedAt.Equal(afterCreatedAt) && row.ID <= afterID)) {
			continue
		}
		if row.Status == tables.EvalExecutionRunning && row.KeyID != "" && (!slices.Contains(skipKeys, row.KeyID) || row.ClaimID == configstore.EvalEndLostClaim) && row.StartedAt != nil && row.StartedAt.Before(routedBefore) {
			if limit > 0 && len(out) == limit {
				break
			}
			out = append(out, *row)
			continue
		}
		if row.Status == tables.EvalExecutionRunning || row.Status == tables.EvalExecutionCompleted || !row.CreatedAt.Before(createdBefore) {
			continue
		}
		if slices.Contains(skipKeys, row.KeyID) && (row.RoutedAt == nil || !row.RoutedAt.Before(routedBefore)) {
			continue
		}
		if limit > 0 && len(out) == limit {
			break
		}
		out = append(out, *row)
	}
	return out, nil
}

func (f *fakeExecutionStore) ListUnfinishedEvalExecutions(_ context.Context, afterCreatedAt time.Time, afterID string, limit int) ([]tables.TableEvalExecution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tables.TableEvalExecution
	for _, row := range f.ordered() {
		if row.Status == tables.EvalExecutionCompleted {
			continue
		}
		if afterID != "" && (row.CreatedAt.Before(afterCreatedAt) || (row.CreatedAt.Equal(afterCreatedAt) && row.ID <= afterID)) {
			continue
		}
		if limit > 0 && len(out) == limit {
			break
		}
		out = append(out, *row)
	}
	return out, nil
}

func (f *fakeExecutionStore) DeleteCompletedEvalExecutions(_ context.Context, _ time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var deleted int64
	for id, row := range f.rows {
		if row.Status == tables.EvalExecutionCompleted {
			delete(f.rows, id)
			deleted++
		}
	}
	return deleted, nil
}

// The sweep reads jobs through the adapter, so it must carry the routed key and skip
// rows that cannot be decoded.
func TestExecutionJobStore_SweepSupport(t *testing.T) {
	ctx := context.Background()
	created := time.Now().Add(-time.Hour)
	rows := &fakeExecutionStore{rows: map[string]*tables.TableEvalExecution{
		"exec-1":   {ID: "exec-1", CreatedAt: created, Definition: json.RawMessage(`{"type":"programmatic","config":{}}`), Variables: json.RawMessage(`{}`)},
		"exec-bad": {ID: "exec-bad", CreatedAt: created.Add(time.Second), Definition: json.RawMessage(`{"type":`), Variables: json.RawMessage(`{}`)},
	}}
	store := NewExecutionJobStore(rows)

	// A full page moves the cursor to its last row, even one that does not decode, so
	// recovery neither re-reads the page nor stops at a row it had to leave out.
	page, next, err := store.(JobResumer).UnfinishedJobs(ctx, JobCursor{}, 2)
	require.NoError(t, err)
	assert.Len(t, page, 1)
	assert.Equal(t, "exec-bad", next.ID)
	page, next, err = store.(JobResumer).UnfinishedJobs(ctx, next, 2)
	require.NoError(t, err)
	assert.Empty(t, page)
	assert.Equal(t, JobCursor{}, next, "a short page is the last")

	moved, err := store.RouteJob(ctx, "exec-1", "", "key-a", time.Now())
	require.NoError(t, err)
	require.True(t, moved)

	stale, _, err := store.StaleJobs(ctx, time.Now(), time.Time{}, nil, JobCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, stale, 1, "the row that does not decode is left out")
	assert.Equal(t, "key-a", stale[0].KeyID)
	assert.False(t, stale[0].Done)
	stale, _, err = store.StaleJobs(ctx, time.Now(), time.Time{}, []string{"key-a"}, JobCursor{}, 10)
	require.NoError(t, err)
	assert.Empty(t, stale, "a job waiting on a key that is being consumed is left out")

	// Routed to a consumed key long ago: its message may have been lost, so the store
	// returns it and the adapter passes it on with its routing time.
	routed := time.Now().Add(-time.Hour)
	rows.rows["exec-1"].RoutedAt = &routed
	stale, _, err = store.StaleJobs(ctx, time.Now(), time.Now(), []string{"key-a"}, JobCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, stale, 1, "a job whose message may be lost reaches the sweep")
	assert.WithinDuration(t, routed, stale[0].RoutedAt, time.Millisecond)

	// A queue that forgets its messages is refilled from every execution without a
	// result, the started ones included.
	_, err = store.StartJob(ctx, "exec-1", "key-a", JobClaim{DeliveryID: "d"})
	require.NoError(t, err)
	unfinished, _, err := store.(JobResumer).UnfinishedJobs(ctx, JobCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, unfinished, 1)
	assert.True(t, unfinished[0].Started)
	assert.Equal(t, "key-a", unfinished[0].KeyID)

	require.NoError(t, store.CompleteJob(ctx, "exec-1", Result{Score: BoolScore(true)}))
	job, err := store.LoadJob(ctx, "exec-1")
	require.NoError(t, err)
	assert.True(t, job.Done)
	unfinished, _, err = store.(JobResumer).UnfinishedJobs(ctx, JobCursor{}, 10)
	require.NoError(t, err)
	assert.Empty(t, unfinished)

	purged, err := store.PurgeJobs(ctx, time.Now())
	require.NoError(t, err)
	assert.EqualValues(t, 1, purged)
}

func TestExecutionJobStore(t *testing.T) {
	ctx := context.Background()
	rows := &fakeExecutionStore{rows: map[string]*tables.TableEvalExecution{
		"exec-1": {
			ID:         "exec-1",
			Definition: json.RawMessage(`{"id":"ev-1","name":"length","type":"programmatic","version":4,"config":{"code":"def validate(output):\n    return True\n","response_type":"yes_no"}}`),
			Variables:  json.RawMessage(`{"output":"hello","input":"hi"}`),
		},
		"exec-bad": {ID: "exec-bad", Definition: json.RawMessage(`{"type":`), Variables: json.RawMessage(`{}`)},
	}}
	store := NewExecutionJobStore(rows)

	job, err := store.LoadJob(ctx, "exec-1")
	require.NoError(t, err)
	assert.Equal(t, "exec-1", job.ID)
	assert.Equal(t, "ev-1", job.Definition.ID)
	assert.Equal(t, EvaluatorTypeProgrammatic, job.Definition.Type)
	assert.Equal(t, 4, job.Definition.Version)
	assert.Equal(t, map[string]string{"output": "hello", "input": "hi"}, job.Input.Variables)

	_, err = store.LoadJob(ctx, "missing")
	require.ErrorIs(t, err, ErrJobNotFound, "a missing row is reported in the runner's terms")

	_, err = store.LoadJob(ctx, "exec-bad")
	require.ErrorIs(t, err, ErrJobNotFound, "a row that cannot be decoded will never run, so it is dropped rather than retried")

	// The claim is on the key the job waits on: a delivery on another key's topic does not
	// start it, and starting it never moves it.
	started, err := store.StartJob(ctx, "exec-1", "key-evals", JobClaim{DeliveryID: "d"})
	require.NoError(t, err)
	assert.False(t, started, "an execution not routed to the key is not claimed on it")
	assert.Empty(t, rows.rows["exec-1"].KeyID)
	moved, err := store.RouteJob(ctx, "exec-1", "", "key-evals", time.Now())
	require.NoError(t, err)
	require.True(t, moved)
	cutoff := time.Now().Add(-time.Minute)
	started, err = store.StartJob(ctx, "exec-1", "key-evals", JobClaim{DeliveryID: "d", StartedBefore: cutoff})
	require.NoError(t, err)
	assert.True(t, started)
	assert.Equal(t, "key-evals", rows.rows["exec-1"].KeyID)
	require.Len(t, rows.claims, 1)
	assert.Equal(t, fakeClaim{id: "exec-1", claimID: "d", reclaimStartedBefore: cutoff}, rows.claims[0], "the claim carries the delivery and the cutoff")

	pass := true
	require.NoError(t, store.CompleteJob(ctx, "exec-1", Result{Score: BoolScore(true), Reasoning: "ok", Pass: &pass, LatencyMs: 12}))
	assert.JSONEq(t, `{"score":{"status":"ok","bool":true},"reasoning":"ok","pass":true,"latency_ms":12}`, string(rows.rows["exec-1"].Result))

	decoded, err := DecodeResult(rows.rows["exec-1"].Result)
	require.NoError(t, err)
	assert.Equal(t, BoolScore(true), decoded.Score)

	// Delivery is at least once: a repeat completion keeps the first result, as the SQL
	// store does, and completing a row that does not exist is an error, not a panic.
	require.NoError(t, store.CompleteJob(ctx, "exec-1", Result{Score: BoolScore(false), Reasoning: "late"}))
	decoded, err = DecodeResult(rows.rows["exec-1"].Result)
	require.NoError(t, err)
	assert.Equal(t, BoolScore(true), decoded.Score, "the first result stands")
	require.ErrorIs(t, store.CompleteJob(ctx, "missing", Result{Score: BoolScore(true)}), configstore.ErrNotFound)
}

// Through the real adapter, a job routed long ago to a key that is being consumed, with
// no message left on that key's queue, is published again by the sweep and runs.
func TestExecutionJobStore_SweepRepublishesALostMessage(t *testing.T) {
	routed := time.Now().Add(-2 * runnerLostMessageFactor * defaultRunnerStrandedAfter)
	rows := &fakeExecutionStore{rows: map[string]*tables.TableEvalExecution{}}
	h := newRunnerHarnessOn(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0, NewExecutionJobStore(rows))
	definition, err := json.Marshal(aiJob("exec-lost", "openai", "gpt-4o-mini").Definition)
	require.NoError(t, err)
	rows.mu.Lock()
	rows.rows["exec-lost"] = &tables.TableEvalExecution{
		ID: "exec-lost", Status: tables.EvalExecutionQueued, KeyID: "key-a", CreatedAt: routed, RoutedAt: &routed,
		Definition: definition, Variables: json.RawMessage(`{}`),
	}
	rows.mu.Unlock()

	require.NoError(t, h.runner.Sweep(context.Background()))
	require.Eventually(t, func() bool {
		row, err := rows.GetEvalExecution(context.Background(), "exec-lost")
		return err == nil && row.Status == tables.EvalExecutionCompleted
	}, 3*time.Second, 5*time.Millisecond, "the job whose message was lost never ran")
}

// Through the real adapter, an execution that started on a key that was then removed is
// listed with its claim and start time once that claim is long expired, and the sweep
// ends it with an error. One that started recently may still have a live owner and is
// left running.
func TestExecutionJobStore_SweepEndsAStartedExecutionOnARemovedKey(t *testing.T) {
	ctx := context.Background()
	longAgo := time.Now().Add(-2 * runnerLostMessageFactor * defaultRunnerStrandedAfter)
	recent := time.Now()
	rows := &fakeExecutionStore{rows: map[string]*tables.TableEvalExecution{}}
	jobs := NewExecutionJobStore(rows)
	h := newRunnerHarnessOn(t, []EvalKey{{ID: "key-b", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0, jobs)
	definition, err := json.Marshal(aiJob("exec-lost", "openai", "gpt-4o-mini").Definition)
	require.NoError(t, err)
	rows.mu.Lock()
	rows.rows["exec-lost"] = &tables.TableEvalExecution{
		ID: "exec-lost", Status: tables.EvalExecutionRunning, KeyID: "key-a", ClaimID: "owner", CreatedAt: longAgo, RoutedAt: &longAgo, StartedAt: &longAgo,
		Definition: definition, Variables: json.RawMessage(`{}`),
	}
	rows.rows["exec-fresh"] = &tables.TableEvalExecution{
		ID: "exec-fresh", Status: tables.EvalExecutionRunning, KeyID: "key-a", ClaimID: "owner", CreatedAt: longAgo, RoutedAt: &longAgo, StartedAt: &recent,
		Definition: definition, Variables: json.RawMessage(`{}`),
	}
	rows.mu.Unlock()

	lostBefore := time.Now().Add(-runnerLostMessageFactor * defaultRunnerStrandedAfter)
	stale, _, err := jobs.StaleJobs(ctx, time.Now(), lostBefore, []string{"key-b"}, JobCursor{}, 10)
	require.NoError(t, err)
	require.Len(t, stale, 1, "only the execution whose claim is long expired is listed")
	assert.Equal(t, "exec-lost", stale[0].ID)
	assert.True(t, stale[0].Started)
	assert.Equal(t, "owner", stale[0].ClaimID)
	assert.WithinDuration(t, longAgo, stale[0].StartedAt, time.Millisecond)

	require.NoError(t, h.runner.Sweep(ctx))
	lost, err := rows.GetEvalExecution(ctx, "exec-lost")
	require.NoError(t, err)
	require.Equal(t, tables.EvalExecutionCompleted, lost.Status, "the execution on the removed key was ended")
	result, err := DecodeResult(lost.Result)
	require.NoError(t, err)
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "no longer enabled for evaluations")
	fresh, err := rows.GetEvalExecution(ctx, "exec-fresh")
	require.NoError(t, err)
	assert.Equal(t, tables.EvalExecutionRunning, fresh.Status, "an execution whose claim may be live is left running")
}

// A page of rows that do not decode must not pin every sweep. The cursor is built from
// the last row read, decodable or not, so the next page reaches the valid row behind them.
func TestExecutionJobStore_StaleJobsCursorPassesUndecodableRows(t *testing.T) {
	ctx := context.Background()
	created := time.Now().Add(-time.Hour)
	rows := &fakeExecutionStore{rows: map[string]*tables.TableEvalExecution{}}
	for i := range 3 {
		id := fmt.Sprintf("exec-bad-%d", i)
		rows.rows[id] = &tables.TableEvalExecution{ID: id, Status: tables.EvalExecutionQueued, CreatedAt: created.Add(time.Duration(i) * time.Second), Definition: json.RawMessage(`{"type":`), Variables: json.RawMessage(`{}`)}
	}
	rows.rows["exec-good"] = &tables.TableEvalExecution{ID: "exec-good", Status: tables.EvalExecutionQueued, CreatedAt: created.Add(time.Minute), Definition: json.RawMessage(`{"type":"programmatic","config":{}}`), Variables: json.RawMessage(`{}`)}
	store := NewExecutionJobStore(rows)

	page, next, err := store.StaleJobs(ctx, time.Now(), time.Time{}, nil, JobCursor{}, 3)
	require.NoError(t, err)
	assert.Empty(t, page, "none of the first page decodes")
	require.NotEqual(t, JobCursor{}, next, "a full page moves the cursor even when nothing in it decodes")
	assert.Equal(t, "exec-bad-2", next.ID)

	page, next, err = store.StaleJobs(ctx, time.Now(), time.Time{}, nil, next, 3)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "exec-good", page[0].ID)
	assert.Equal(t, JobCursor{}, next, "a short page is the last")
}
