package configstore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestEvalRun stores a run with n queued entries and returns it with its entries.
func newTestEvalRun(t *testing.T, store *RDBConfigStore, datasetID string, version, n int) (*tables.TableEvalRun, []tables.TableEvalRunEntry) {
	t.Helper()
	ctx := context.Background()
	run := &tables.TableEvalRun{
		Name: "nightly", Status: tables.EvalRunRunning, TargetType: "dataset", Tags: []string{"ci"},
		DatasetID: datasetID, DatasetVersion: version, Evaluators: []byte(`[{"id":"ev1"}]`), Total: int64(n),
	}
	require.NoError(t, store.CreateEvalRun(ctx, run))
	entries := make([]tables.TableEvalRunEntry, n)
	for i := range entries {
		entries[i] = tables.TableEvalRunEntry{
			RunID: run.ID, Seq: int64(i + 1), DatasetRowID: fmt.Sprintf("row-%d", i), Status: tables.EvalRunEntryQueued,
			Variables: map[string]string{"input": fmt.Sprintf("question %d", i)},
		}
	}
	require.NoError(t, store.CreateEvalRunEntries(ctx, entries))
	return run, entries
}

func TestEvalRunCreateListAndDelete(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	run, entries := newTestEvalRun(t, store, "ds1", 1, 3)

	got, err := store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"ci"}, got.Tags)
	assert.JSONEq(t, `[{"id":"ev1"}]`, string(got.Evaluators))
	assert.JSONEq(t, `{}`, string(got.Target))

	other := &tables.TableEvalRun{Name: "smoke", Status: tables.EvalRunCompleted, TargetType: "model", DatasetID: "ds2", DatasetVersion: 4}
	require.NoError(t, store.CreateEvalRun(ctx, other))

	list, total, err := store.ListEvalRuns(ctx, EvalRunListParams{})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	require.Len(t, list, 2)
	list, total, err = store.ListEvalRuns(ctx, EvalRunListParams{Search: "NIGHT", Status: tables.EvalRunRunning, DatasetID: "ds1", Tag: "ci"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, run.ID, list[0].ID)

	page, total, err := store.ListEvalRunEntries(ctx, EvalRunEntryListParams{RunID: run.ID, Offset: 1, Limit: 1})
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, page, 1)
	assert.Equal(t, entries[1].ID, page[0].ID)
	assert.Equal(t, "question 1", page[0].Variables["input"])
	found, total, err := store.ListEvalRunEntries(ctx, EvalRunEntryListParams{RunID: run.ID, Search: "QUESTION 2"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, found, 1)

	// Deleting a run removes its entries and results, and hides the run.
	require.NoError(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}))
	require.NoError(t, store.DeleteEvalRun(ctx, run.ID))
	_, err = store.GetEvalRun(ctx, run.ID)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, store.DeleteEvalRun(ctx, run.ID), ErrNotFound)
	_, total, err = store.ListEvalRunEntries(ctx, EvalRunEntryListParams{RunID: run.ID})
	require.NoError(t, err)
	assert.Zero(t, total)
	results, err := store.ListEvalRunResults(ctx, EvalRunResultListParams{RunID: run.ID})
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestEvalRunTransitionsAreClaims(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	// The test database is in memory, which is one database per connection.
	sqlDB, err := store.DB().DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	run, entries := newTestEvalRun(t, store, "ds1", 1, 4)

	// Of many workers that try to start the same entry, exactly one does.
	var claimed atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.TransitionEvalRunEntry(ctx, entries[0].ID, []string{tables.EvalRunEntryQueued},
				map[string]any{"status": tables.EvalRunEntryRunning, "started_at": time.Now()})
			if err == nil && ok {
				claimed.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, claimed.Load())

	// The same holds for finishing a run: only one caller moves it out of running.
	first, err := store.TransitionEvalRun(ctx, run.ID, []string{tables.EvalRunRunning}, map[string]any{"status": tables.EvalRunCompleted, "summary_json": `{"ok":true}`})
	require.NoError(t, err)
	second, err := store.TransitionEvalRun(ctx, run.ID, []string{tables.EvalRunRunning}, map[string]any{"status": tables.EvalRunFailed})
	require.NoError(t, err)
	assert.True(t, first)
	assert.False(t, second)
	got, err := store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, got.Status)
	assert.JSONEq(t, `{"ok":true}`, string(got.Summary))

	// A stop flips every queued entry at once and leaves the one in flight alone.
	stopped, err := store.TransitionEvalRunEntries(ctx, run.ID, []string{tables.EvalRunEntryQueued}, nil, map[string]any{"status": tables.EvalRunEntryStopped})
	require.NoError(t, err)
	assert.EqualValues(t, 3, stopped)
	counts, err := store.CountEvalRunEntries(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunEntryRunning: 1, tables.EvalRunEntryStopped: 3}, counts)

	// A rerun of chosen entries resets only those.
	reset, err := store.TransitionEvalRunEntries(ctx, run.ID, []string{tables.EvalRunEntryStopped}, []string{entries[1].ID, entries[2].ID},
		map[string]any{"status": tables.EvalRunEntryQueued})
	require.NoError(t, err)
	assert.EqualValues(t, 2, reset)
}

func TestEvalRunResults(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	run, entries := newTestEvalRun(t, store, "ds1", 1, 2)

	results := []tables.TableEvalRunResult{
		{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued},
		{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev2", Status: tables.EvalRunResultQueued},
		{RunID: run.ID, EntryID: entries[1].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued},
	}
	require.NoError(t, store.CreateEvalRunResults(ctx, results))
	// One score per entry and evaluator: a duplicate job cannot add a second row.
	require.Error(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}))

	done, err := store.TransitionEvalRunResult(ctx, results[0].ID, []string{tables.EvalRunResultQueued, tables.EvalRunResultRunning}, map[string]any{
		"status": tables.EvalRunResultCompleted, "score_json": `{"status":"ok","number":4}`, "pass": true, "total_tokens": 30, "cost": 0.002,
	})
	require.NoError(t, err)
	assert.True(t, done)
	again, err := store.TransitionEvalRunResult(ctx, results[0].ID, []string{tables.EvalRunResultQueued, tables.EvalRunResultRunning}, map[string]any{"status": tables.EvalRunResultFailed})
	require.NoError(t, err)
	assert.False(t, again, "a result that completed is not written twice")

	got, err := store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"ok","number":4}`, string(got.Score))
	require.NotNil(t, got.Pass)
	assert.True(t, *got.Pass)

	ofEntry, err := store.ListEvalRunResults(ctx, EvalRunResultListParams{RunID: run.ID, EntryIDs: []string{entries[0].ID}})
	require.NoError(t, err)
	assert.Len(t, ofEntry, 2)
	ofEvaluator, err := store.ListEvalRunResults(ctx, EvalRunResultListParams{RunID: run.ID, EvaluatorID: "ev1"})
	require.NoError(t, err)
	assert.Len(t, ofEvaluator, 2)
	// Paging by ID walks every result once.
	firstPage, err := store.ListEvalRunResults(ctx, EvalRunResultListParams{RunID: run.ID, Limit: 2})
	require.NoError(t, err)
	require.Len(t, firstPage, 2)
	rest, err := store.ListEvalRunResults(ctx, EvalRunResultListParams{RunID: run.ID, AfterID: firstPage[1].ID, Limit: 2})
	require.NoError(t, err)
	assert.Len(t, rest, 1)

	byEvaluator, err := store.CountEvalRunResultsByEvaluator(ctx, run.ID)
	require.NoError(t, err)
	seen := map[string]int64{}
	for _, count := range byEvaluator {
		key := count.EvaluatorID + ":" + count.Status
		if count.Pass != nil && *count.Pass {
			key += ":pass"
		}
		seen[key] += count.Count
	}
	assert.Equal(t, map[string]int64{"ev1:completed:pass": 1, "ev1:queued": 1, "ev2:queued": 1}, seen)

	// Routing a waiting result to a key is a claim on the key it is on now.
	routed, err := store.RouteEvalRunResult(ctx, results[1].ID, "", "k1")
	require.NoError(t, err)
	assert.True(t, routed)
	routed, err = store.RouteEvalRunResult(ctx, results[1].ID, "", "k2")
	require.NoError(t, err)
	assert.False(t, routed, "it is on k1 now, so a move from no key loses")
	routed, err = store.RouteEvalRunEntry(ctx, entries[1].ID, "", "k1")
	require.NoError(t, err)
	assert.True(t, routed)

	last, err := store.GetEvalRunLastSeq(ctx, run.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, last)
	rowIDs, rowTotal, err := store.ListEvalRunRowIDs(ctx, []string{run.ID}, 1, 5)
	require.NoError(t, err)
	assert.EqualValues(t, 2, rowTotal)
	assert.Equal(t, []string{"row-1"}, rowIDs)

	pending, err := store.CountEvalRunResults(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{tables.EvalRunResultCompleted: 1, tables.EvalRunResultQueued: 1}, pending)

	_, err = store.TransitionEvalRunEntry(ctx, entries[0].ID, []string{tables.EvalRunEntryQueued}, map[string]any{
		"status": tables.EvalRunEntryCompleted, "target_latency_ms": 120, "target_total_tokens": 50, "target_cost": 0.01,
	})
	require.NoError(t, err)
	totals, err := store.GetEvalRunTotals(ctx, run.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 50, totals.TargetTokens)
	assert.InDelta(t, 0.01, totals.TargetCost, 1e-9)
	assert.EqualValues(t, 30, totals.EvaluatorTokens)
	assert.InDelta(t, 0.002, totals.EvaluatorCost, 1e-9)
	latencies, err := store.ListEvalRunTargetLatencies(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, []int64{120}, latencies)

	// A second run on the same rows, scored lower on the one row both have a score for.
	other, otherEntries := newTestEvalRun(t, store, "ds1", 1, 2)
	require.NoError(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{
		{RunID: other.ID, EntryID: otherEntries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultCompleted, ScoreNumber: new(2.0)},
		{RunID: other.ID, EntryID: otherEntries[1].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultCompleted, ScoreNumber: new(5.0)},
	}))
	_, err = store.TransitionEvalRunResult(ctx, results[0].ID, nil, map[string]any{"score_number": 4.0})
	require.NoError(t, err)
	changes, err := store.CountEvalRunScoreChanges(ctx, run.ID, other.ID)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, EvalRunScoreChange{EvaluatorID: "ev1", Improved: 0, Regressed: 1, Compared: 1}, changes[0])

	removed, err := store.DeleteEvalRunResults(ctx, run.ID, []string{entries[0].ID}, "")
	require.NoError(t, err)
	assert.EqualValues(t, 2, removed)
}

func TestEvalRunKeepsItsDatasetVersion(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, _ := newTestEvalDataset(t, store, "pinned")
	save := func() {
		addTestRows(t, store, dataset.ID, inputCol, "q")
		evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
			_, err := tx.SaveVersion(EvalDatasetVersionSave{})
			return err
		})
	}
	save() // v1
	save() // v2
	save() // v3
	run, _ := newTestEvalRun(t, store, dataset.ID, 1, 1)

	// The version a run reads cannot be deleted, and pruning steps over it.
	err := store.WithEvalDataset(ctx, dataset.ID, func(tx EvalDatasetTx) error { return tx.DeleteVersion(1) })
	require.ErrorIs(t, err, ErrEvalDatasetVersionInUse)
	require.NoError(t, store.UpdateEvalDataset(ctx, &tables.TableEvalDataset{ID: dataset.ID, Name: dataset.Name, MaxVersions: new(1)}))
	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error { return tx.PruneVersions() })
	versions, err := store.ListEvalDatasetVersions(ctx, dataset.ID)
	require.NoError(t, err)
	numbers := make([]int, len(versions))
	for i, version := range versions {
		numbers[i] = version.Number
	}
	assert.ElementsMatch(t, []int{1, 3}, numbers, "v2 is pruned, v1 is kept for the run, v3 is the latest")

	// A dataset with runs cannot be deleted. Deleting the run frees both.
	require.ErrorIs(t, store.DeleteEvalDataset(ctx, dataset.ID), ErrEvalDatasetHasRuns)
	require.NoError(t, store.DeleteEvalRun(ctx, run.ID))
	require.NoError(t, store.WithEvalDataset(ctx, dataset.ID, func(tx EvalDatasetTx) error { return tx.DeleteVersion(1) }))
	require.NoError(t, store.DeleteEvalDataset(ctx, dataset.ID))
}

// The narrow reads return what a job needs without the large columns: a run's state
// without its copied target and evaluators, entries without variables and output,
// results without reasoning.
func TestEvalRunNarrowReads(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	run, entries := newTestEvalRun(t, store, "ds1", 1, 5)

	state, err := store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, state.Status)
	assert.False(t, state.UpdatedAt.IsZero())
	assert.Nil(t, state.StartedAt)
	_, err = store.GetEvalRunState(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)

	// Entries are paged by position, and filtered by the key they wait on.
	moved, err := store.RouteEvalRunEntry(ctx, entries[3].ID, "", "key-gone")
	require.NoError(t, err)
	require.True(t, moved)
	moved, err = store.RouteEvalRunEntry(ctx, entries[4].ID, "", "key-live")
	require.NoError(t, err)
	require.True(t, moved)
	refs, err := store.ListEvalRunEntryRefs(ctx, EvalRunEntryListParams{RunID: run.ID, AfterSeq: 1, Limit: 2})
	require.NoError(t, err)
	require.Len(t, refs, 2)
	assert.Equal(t, []int64{2, 3}, []int64{refs[0].Seq, refs[1].Seq})
	assert.Equal(t, entries[1].ID, refs[0].ID)
	stranded, err := store.ListEvalRunEntryRefs(ctx, EvalRunEntryListParams{Statuses: []string{tables.EvalRunEntryQueued}, ExcludeKeyIDs: []string{"", "key-live"}})
	require.NoError(t, err)
	require.Len(t, stranded, 1)
	assert.Equal(t, "key-gone", stranded[0].KeyID)
	unrouted, err := store.ListEvalRunEntryRefs(ctx, EvalRunEntryListParams{RunID: run.ID, KeyID: new("")})
	require.NoError(t, err)
	assert.Len(t, unrouted, 3)

	// A run has open entries until the last one ends.
	open, err := store.HasOpenEvalRunEntries(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, open)
	_, err = store.TransitionEvalRunEntries(ctx, run.ID, nil, nil, map[string]any{"status": tables.EvalRunEntryCompleted})
	require.NoError(t, err)
	open, err = store.HasOpenEvalRunEntries(ctx, run.ID)
	require.NoError(t, err)
	assert.False(t, open)

	require.NoError(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{
		{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultCompleted, Score: []byte(`{"status":"ok","number":4}`), Pass: new(true), Reasoning: "long text"},
		{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev2", Status: tables.EvalRunResultQueued},
	}))
	results, err := store.ListEvalRunResultRefs(ctx, EvalRunResultListParams{RunID: run.ID, EntryIDs: []string{entries[0].ID}, EvaluatorID: "ev1"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, entries[0].ID, results[0].EntryID)
	require.NotNil(t, results[0].Pass)
	assert.True(t, *results[0].Pass)
	require.NotNil(t, results[0].ScoreJSON)
	assert.JSONEq(t, `{"status":"ok","number":4}`, *results[0].ScoreJSON)
}

// Every write to a run moves its updated_at, whether or not the caller names it. Jobs
// tell that a run changed by that timestamp.
func TestTransitionEvalRunMovesUpdatedAt(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	run, _ := newTestEvalRun(t, store, "ds1", 1, 1)
	before, err := store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)

	time.Sleep(5 * time.Millisecond)
	moved, err := store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"started_at": time.Now()})
	require.NoError(t, err)
	require.True(t, moved)
	after, err := store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, after.UpdatedAt.After(before.UpdatedAt), "a write that does not name updated_at still moves it")

	// A caller that names it keeps its own value.
	fixed := time.Now().Add(time.Hour).Truncate(time.Second)
	_, err = store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"name": "renamed", "updated_at": fixed})
	require.NoError(t, err)
	named, err := store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, named.UpdatedAt.Equal(fixed))
}

// With a baseline run, a run's entries can be narrowed to the rows it scored lower, or
// higher, than the baseline on any evaluator. Rows are matched on the dataset row.
func TestEvalRunEntriesThatMovedAgainstABaseline(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	base, baseEntries := newTestEvalRun(t, store, "ds1", 1, 4)
	run, entries := newTestEvalRun(t, store, "ds1", 1, 4)
	score := func(runID, entryID, evaluatorID string, value float64) tables.TableEvalRunResult {
		return tables.TableEvalRunResult{RunID: runID, EntryID: entryID, EvaluatorID: evaluatorID, Status: tables.EvalRunResultCompleted, ScoreNumber: new(value)}
	}
	// Row 0 is worse, row 1 is better, row 2 is the same, row 3 is worse on one
	// evaluator and better on another.
	require.NoError(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{
		score(base.ID, baseEntries[0].ID, "ev1", 1), score(run.ID, entries[0].ID, "ev1", 0),
		score(base.ID, baseEntries[1].ID, "ev1", 0), score(run.ID, entries[1].ID, "ev1", 1),
		score(base.ID, baseEntries[2].ID, "ev1", 1), score(run.ID, entries[2].ID, "ev1", 1),
		score(base.ID, baseEntries[3].ID, "ev1", 1), score(run.ID, entries[3].ID, "ev1", 0),
		score(base.ID, baseEntries[3].ID, "ev2", 2), score(run.ID, entries[3].ID, "ev2", 5),
	}))
	seqs := func(change string) []int64 {
		page, total, err := store.ListEvalRunEntries(ctx, EvalRunEntryListParams{RunID: run.ID, BaselineRunID: base.ID, Change: change})
		require.NoError(t, err)
		require.EqualValues(t, len(page), total)
		out := make([]int64, len(page))
		for i := range page {
			out[i] = page[i].Seq
		}
		return out
	}
	assert.Equal(t, []int64{1, 4}, seqs(EvalRunChangeWorse))
	assert.Equal(t, []int64{2, 4}, seqs(EvalRunChangeBetter))
	assert.Len(t, seqs(""), 4, "without a change, the baseline does not narrow anything")
}
