package evals

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeExecutionStore is an in-memory ExecutionStore.
type fakeExecutionStore struct {
	rows map[string]*tables.TableEvalExecution
}

func (f *fakeExecutionStore) GetEvalExecution(_ context.Context, id string) (*tables.TableEvalExecution, error) {
	row, ok := f.rows[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return row, nil
}

func (f *fakeExecutionStore) StartEvalExecution(_ context.Context, id, keyID string) error {
	f.rows[id].Status, f.rows[id].KeyID = tables.EvalExecutionRunning, keyID
	return nil
}

func (f *fakeExecutionStore) CompleteEvalExecution(_ context.Context, id string, result json.RawMessage) error {
	f.rows[id].Status, f.rows[id].Result = tables.EvalExecutionCompleted, result
	return nil
}

func (f *fakeExecutionStore) RouteEvalExecution(_ context.Context, id, fromKey, toKey string) (bool, error) {
	row, ok := f.rows[id]
	if !ok || row.Status == tables.EvalExecutionRunning || row.Status == tables.EvalExecutionCompleted || row.KeyID != fromKey {
		return false, nil
	}
	row.KeyID = toKey
	return true, nil
}

func (f *fakeExecutionStore) ListQueuedEvalExecutions(_ context.Context, _ time.Time, _ int) ([]tables.TableEvalExecution, error) {
	var out []tables.TableEvalExecution
	for _, id := range []string{"exec-1", "exec-bad"} {
		if row, ok := f.rows[id]; ok && row.Status != tables.EvalExecutionRunning && row.Status != tables.EvalExecutionCompleted {
			out = append(out, *row)
		}
	}
	return out, nil
}

func (f *fakeExecutionStore) ListUnfinishedEvalExecutions(context.Context) ([]tables.TableEvalExecution, error) {
	var out []tables.TableEvalExecution
	for _, id := range []string{"exec-1", "exec-bad"} {
		if row, ok := f.rows[id]; ok && row.Status != tables.EvalExecutionCompleted {
			out = append(out, *row)
		}
	}
	return out, nil
}

func (f *fakeExecutionStore) DeleteCompletedEvalExecutions(_ context.Context, _ time.Time) (int64, error) {
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
	rows := &fakeExecutionStore{rows: map[string]*tables.TableEvalExecution{
		"exec-1":   {ID: "exec-1", Definition: json.RawMessage(`{"type":"programmatic","config":{}}`), Variables: json.RawMessage(`{}`)},
		"exec-bad": {ID: "exec-bad", Definition: json.RawMessage(`{"type":`), Variables: json.RawMessage(`{}`)},
	}}
	store := NewExecutionJobStore(rows)

	moved, err := store.RouteJob(ctx, "exec-1", "", "key-a")
	require.NoError(t, err)
	require.True(t, moved)

	stale, err := store.StaleJobs(ctx, time.Now(), nil, 10)
	require.NoError(t, err)
	require.Len(t, stale, 1, "the row that does not decode is left out")
	assert.Equal(t, "key-a", stale[0].KeyID)
	assert.False(t, stale[0].Done)
	stale, err = store.StaleJobs(ctx, time.Now(), []string{"key-a"}, 10)
	require.NoError(t, err)
	assert.Empty(t, stale, "a job waiting on a key that is being consumed is left out")

	// A queue that forgets its messages is refilled from every execution without a
	// result, the started ones included.
	require.NoError(t, store.StartJob(ctx, "exec-1", "key-a"))
	unfinished, err := store.(JobResumer).UnfinishedJobs(ctx)
	require.NoError(t, err)
	require.Len(t, unfinished, 1)
	assert.True(t, unfinished[0].Started)
	assert.Equal(t, "key-a", unfinished[0].KeyID)

	require.NoError(t, store.CompleteJob(ctx, "exec-1", Result{Score: BoolScore(true)}))
	job, err := store.LoadJob(ctx, "exec-1")
	require.NoError(t, err)
	assert.True(t, job.Done)
	unfinished, err = store.(JobResumer).UnfinishedJobs(ctx)
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

	require.NoError(t, store.StartJob(ctx, "exec-1", "key-evals"))
	assert.Equal(t, "key-evals", rows.rows["exec-1"].KeyID)

	pass := true
	require.NoError(t, store.CompleteJob(ctx, "exec-1", Result{Score: BoolScore(true), Reasoning: "ok", Pass: &pass, LatencyMs: 12}))
	assert.JSONEq(t, `{"score":{"status":"ok","bool":true},"reasoning":"ok","pass":true,"latency_ms":12}`, string(rows.rows["exec-1"].Result))

	decoded, err := DecodeResult(rows.rows["exec-1"].Result)
	require.NoError(t, err)
	assert.Equal(t, BoolScore(true), decoded.Score)
}
