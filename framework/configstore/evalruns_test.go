package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ensureTestEvalDataset stores a bare live dataset with the given ID and its version 1
// unless they exist: a run is only stored on a live dataset (see CreateEvalRun).
func ensureTestEvalDataset(t *testing.T, store *RDBConfigStore, id string) {
	t.Helper()
	require.NoError(t, store.DB().Where(tables.TableEvalDataset{ID: id}).
		FirstOrCreate(&tables.TableEvalDataset{ID: id, Name: "dataset " + id}).Error)
	ensureTestEvalDatasetVersion(t, store, id, 1)
}

// ensureTestEvalDatasetVersion stores a bare version of a dataset unless it exists: a run
// is only stored on a version that exists (see CreateEvalRun).
func ensureTestEvalDatasetVersion(t *testing.T, store *RDBConfigStore, datasetID string, number int) {
	t.Helper()
	require.NoError(t, store.DB().Where(tables.TableEvalDatasetVersion{DatasetID: datasetID, Number: number}).
		Attrs(tables.TableEvalDatasetVersion{ID: fmt.Sprintf("%s-v%d", datasetID, number), ColumnsJSON: "[]"}).
		FirstOrCreate(&tables.TableEvalDatasetVersion{}).Error)
}

// newTestEvalRun stores a run with n queued entries and returns it with its entries.
func newTestEvalRun(t *testing.T, store *RDBConfigStore, datasetID string, version, n int) (*tables.TableEvalRun, []tables.TableEvalRunEntry) {
	t.Helper()
	ctx := context.Background()
	ensureTestEvalDataset(t, store, datasetID)
	ensureTestEvalDatasetVersion(t, store, datasetID, version)
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

	ensureTestEvalDataset(t, store, "ds2")
	ensureTestEvalDatasetVersion(t, store, "ds2", 4)
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
	require.NoError(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}, 0))
	require.ErrorIs(t, store.DeleteEvalRun(ctx, run.ID), ErrEvalRunActive, "an active run is not deleted")
	_, err = store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunCompleted})
	require.NoError(t, err)
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

	// Of many workers that try to start the same entry, exactly one does, and the others
	// are refused without an error.
	type outcome struct {
		claimed bool
		err     error
	}
	outcomes := make([]outcome, 8)
	var wg sync.WaitGroup
	for i := range outcomes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.TransitionEvalRunEntry(ctx, entries[0].ID, []string{tables.EvalRunEntryQueued},
				map[string]any{"status": tables.EvalRunEntryRunning, "started_at": time.Now()})
			outcomes[i] = outcome{ok, err}
		}()
	}
	wg.Wait()
	claimed := 0
	for i, got := range outcomes {
		require.NoError(t, got.err, "worker %d", i)
		if got.claimed {
			claimed++
		}
	}
	assert.Equal(t, 1, claimed, "exactly one worker claims the entry")

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
	for i, want := range []string{tables.EvalRunEntryRunning, tables.EvalRunEntryQueued, tables.EvalRunEntryQueued, tables.EvalRunEntryStopped} {
		got, err := store.GetEvalRunEntry(ctx, entries[i].ID)
		require.NoError(t, err)
		assert.Equal(t, want, got.Status, "entry %d", i)
	}
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
	require.NoError(t, store.CreateEvalRunResults(ctx, results, 0))
	// One score per entry and evaluator: a duplicate job cannot add a second row.
	require.Error(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}, 0))

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
	routed, err := store.RouteEvalRunResult(ctx, results[1].ID, "", "k1", time.Now())
	require.NoError(t, err)
	assert.True(t, routed)
	routed, err = store.RouteEvalRunResult(ctx, results[1].ID, "", "k2", time.Now())
	require.NoError(t, err)
	assert.False(t, routed, "it is on k1 now, so a move from no key loses")
	routed, err = store.RouteEvalRunEntry(ctx, entries[1].ID, "", "k1", time.Now())
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
	}, 0))
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
	_, err = store.DeleteEvalDataset(ctx, dataset.ID)
	require.ErrorIs(t, err, ErrEvalDatasetHasRuns)
	_, err = store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunCompleted})
	require.NoError(t, err)
	require.NoError(t, store.DeleteEvalRun(ctx, run.ID))
	require.NoError(t, store.WithEvalDataset(ctx, dataset.ID, func(tx EvalDatasetTx) error { return tx.DeleteVersion(1) }))
	_, err = store.DeleteEvalDataset(ctx, dataset.ID)
	require.NoError(t, err)
}

// A run created while its dataset is being deleted must not be left reading a deleted
// dataset. Here the dataset is soft-deleted inside the run's own insert, as a delete that
// commits between the run's checks and its insert would: the create fails with
// ErrNotFound and stores no run.
func TestCreateEvalRunRefusesADatasetDeletedUnderIt(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, _ := newTestEvalDataset(t, store, "deleted under the run")
	// Version 1 exists, so only the delete inside the insert can refuse the run.
	addTestRows(t, store, dataset.ID, inputCol, "q")
	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		_, err := tx.SaveVersion(EvalDatasetVersionSave{})
		return err
	})

	var deleted int64
	require.NoError(t, store.DB().Callback().Create().After("gorm:create").Register("test:delete_dataset_mid_run_create", func(tx *gorm.DB) {
		if run, ok := tx.Statement.Dest.(*tables.TableEvalRun); ok && run.DatasetID == dataset.ID {
			deleted += tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE evals_datasets SET deleted_at_unix_nano = ? WHERE id = ?", time.Now().UnixNano(), dataset.ID).RowsAffected
		}
	}))
	t.Cleanup(func() { _ = store.DB().Callback().Create().Remove("test:delete_dataset_mid_run_create") })

	run := &tables.TableEvalRun{Name: "late", Status: tables.EvalRunQueued, TargetType: "dataset", DatasetID: dataset.ID, DatasetVersion: 1}
	require.ErrorIs(t, store.CreateEvalRun(ctx, run), ErrNotFound, "the dataset is gone")
	assert.EqualValues(t, 1, deleted, "the callback deleted the dataset inside the insert")
	count, err := store.CountEvalRunsOnDataset(ctx, dataset.ID, 0)
	require.NoError(t, err)
	assert.Zero(t, count, "the run insert was rolled back")

	require.ErrorIs(t, store.CreateEvalRun(ctx, &tables.TableEvalRun{Name: "missing", Status: tables.EvalRunQueued, TargetType: "dataset", DatasetID: "no-such-dataset", DatasetVersion: 1}),
		ErrNotFound, "a dataset that never existed takes no run either")
}

// A run is pinned to a version that exists. A version removed before the create, or
// removed by a delete that commits between the create's checks and its insert (here,
// inside the insert), leaves the create with ErrNotFound and no run.
func TestCreateEvalRunRefusesAMissingPinnedVersion(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, _ := newTestEvalDataset(t, store, "pinned version gone")
	for range 3 {
		addTestRows(t, store, dataset.ID, inputCol, "q")
		saveTestVersion(t, store, dataset.ID, "")
	}
	require.NoError(t, store.WithEvalDataset(ctx, dataset.ID, func(tx EvalDatasetTx) error { return tx.DeleteVersion(1) }))
	run := &tables.TableEvalRun{Name: "late", Status: tables.EvalRunQueued, TargetType: "dataset", DatasetID: dataset.ID, DatasetVersion: 1}
	require.ErrorIs(t, store.CreateEvalRun(ctx, run), ErrNotFound, "version 1 is gone")
	count, err := store.CountEvalRunsOnDataset(ctx, dataset.ID, 0)
	require.NoError(t, err)
	assert.Zero(t, count, "no run is stored on a deleted version")

	require.NoError(t, store.DB().Callback().Create().After("gorm:create").Register("test:delete_version_mid_run_create", func(tx *gorm.DB) {
		if run, ok := tx.Statement.Dest.(*tables.TableEvalRun); ok && run.DatasetID == dataset.ID {
			tx.Session(&gorm.Session{NewDB: true}).Exec("DELETE FROM evals_dataset_versions WHERE dataset_id = ? AND number = 2", dataset.ID)
		}
	}))
	t.Cleanup(func() { _ = store.DB().Callback().Create().Remove("test:delete_version_mid_run_create") })
	run = &tables.TableEvalRun{Name: "racing", Status: tables.EvalRunQueued, TargetType: "dataset", DatasetID: dataset.ID, DatasetVersion: 2}
	require.ErrorIs(t, store.CreateEvalRun(ctx, run), ErrNotFound, "version 2 went during the create")
	count, err = store.CountEvalRunsOnDataset(ctx, dataset.ID, 0)
	require.NoError(t, err)
	assert.Zero(t, count, "the run insert was rolled back")
}

// A dataset delete counts the dataset's runs under the dataset's lock, so a run created
// after a count taken outside the delete is still seen. Here the run is inserted when the
// delete first writes the dataset row, as a create that commits between an early count and
// the soft-delete would: the delete fails with ErrEvalDatasetHasRuns and the dataset stays.
func TestDeleteEvalDatasetCountsRunsUnderItsLock(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, _, _ := newTestEvalDataset(t, store, "run arrives during delete")

	inserted := false
	require.NoError(t, store.DB().Callback().Update().Before("gorm:update").Register("test:create_run_mid_dataset_delete", func(tx *gorm.DB) {
		if tx.Statement.Table == "evals_datasets" && !inserted {
			inserted = true
			tx.Session(&gorm.Session{NewDB: true}).Create(&tables.TableEvalRun{Name: "racing", Status: tables.EvalRunQueued, TargetType: "dataset", DatasetID: dataset.ID, DatasetVersion: 1})
		}
	}))
	t.Cleanup(func() { _ = store.DB().Callback().Update().Remove("test:create_run_mid_dataset_delete") })

	_, err := store.DeleteEvalDataset(ctx, dataset.ID)
	require.True(t, inserted, "the delete wrote the dataset row")
	require.ErrorIs(t, err, ErrEvalDatasetHasRuns)
	_, err = store.GetEvalDataset(ctx, dataset.ID)
	require.NoError(t, err, "the dataset a run reads is kept")
}

// The narrow reads return what a job needs without the large columns: a run's state
// without its copied target and evaluators, entries without variables and output,
// results without reasoning.
// An entry with no variables reads back with an empty object, not null: the API schema
// types variables as an object.
func TestEvalRunEntryWithoutVariablesReadsAsAnEmptyObject(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	run, _ := newTestEvalRun(t, store, "ds1", 1, 0)
	entry := tables.TableEvalRunEntry{RunID: run.ID, Seq: 1, DatasetRowID: "tags-only", Status: tables.EvalRunEntryQueued}
	require.NoError(t, store.CreateEvalRunEntries(ctx, []tables.TableEvalRunEntry{entry}))
	listed, _, err := store.ListEvalRunEntries(ctx, EvalRunEntryListParams{RunID: run.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	data, err := json.Marshal(listed[0])
	require.NoError(t, err)
	assert.Contains(t, string(data), `"variables":{}`)
}

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
	moved, err := store.RouteEvalRunEntry(ctx, entries[3].ID, "", "key-gone", time.Now())
	require.NoError(t, err)
	require.True(t, moved)
	moved, err = store.RouteEvalRunEntry(ctx, entries[4].ID, "", "key-live", time.Now())
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
	}, 0))
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
	}, 0))
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

// A tag filter compares whole tags: a tag holding the wanted one inside its JSON text,
// or the same letters in another case, does not match.
func TestEvalRunTagFilterMatchesWholeTags(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	ensureTestEvalDataset(t, store, "ds")
	for _, tags := range [][]string{{`team"blue`}, {"BLUE"}, {"blue"}} {
		require.NoError(t, store.CreateEvalRun(ctx, &tables.TableEvalRun{Name: tags[0], Status: tables.EvalRunCompleted, TargetType: "dataset", DatasetID: "ds", DatasetVersion: 1, Tags: tags}))
	}
	runs, total, err := store.ListEvalRuns(ctx, EvalRunListParams{Tag: "blue"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, runs, 1)
	assert.Equal(t, []string{"blue"}, runs[0].Tags)
}

// Entries and results are written only for a run that is still there. A planning page or
// a result insert that loses the race with DeleteEvalRun leaves no orphan rows.
func TestEvalRunWritesAfterDeleteLeaveNothing(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	run, entries := newTestEvalRun(t, store, "ds1", 1, 1)
	_, err := store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunStopped})
	require.NoError(t, err)
	require.NoError(t, store.DeleteEvalRun(ctx, run.ID))

	err = store.CreateEvalRunEntries(ctx, []tables.TableEvalRunEntry{{RunID: run.ID, Seq: 9, DatasetRowID: "late", Status: tables.EvalRunEntryQueued}})
	require.ErrorIs(t, err, ErrNotFound)
	var count int64
	require.NoError(t, store.DB().Model(&tables.TableEvalRunEntry{}).Where("run_id = ?", run.ID).Count(&count).Error)
	assert.Zero(t, count, "no entry is stored for a deleted run")

	err = store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}, 0)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, store.DB().Model(&tables.TableEvalRunResult{}).Where("run_id = ?", run.ID).Count(&count).Error)
	assert.Zero(t, count, "no result is stored for a deleted run")
}

// DeleteEvalRun removes only a run that is not active. The check is in the same write as
// the delete, so a run that a concurrent request just marked running is kept.
func TestDeleteEvalRunRefusesAnActiveRun(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	run, _ := newTestEvalRun(t, store, "ds1", 1, 1)
	require.ErrorIs(t, store.DeleteEvalRun(ctx, run.ID), ErrEvalRunActive)
	_, err := store.GetEvalRun(ctx, run.ID)
	require.NoError(t, err)
	require.ErrorIs(t, store.DeleteEvalRun(ctx, "missing"), ErrNotFound)
}

// assertEvalRunRecoveryClaim checks the sweep's recovery claim of an entry and a result:
// it takes back a running row only at the exact start it names, as StartEvalExecution does.
func assertEvalRunRecoveryClaim(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	ctx := context.Background()
	run, entries := newTestEvalRun(t, store, "ds1", 1, 1)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}
	require.NoError(t, store.CreateEvalRunResults(ctx, results, 0))
	type claimFn func(id, claimID string, recoverStartedAt time.Time) (bool, error)
	type readFn func(id string) (*time.Time, error)
	cases := []struct {
		name  string
		id    string
		claim claimFn
		read  readFn
	}{
		{"entry", entries[0].ID,
			func(id, claimID string, at time.Time) (bool, error) {
				return store.ClaimEvalRunEntry(ctx, id, "", claimID, 1, time.Time{}, at)
			},
			func(id string) (*time.Time, error) {
				entry, err := store.GetEvalRunEntry(ctx, id)
				if err != nil {
					return nil, err
				}
				return entry.StartedAt, nil
			}},
		{"result", results[0].ID,
			func(id, claimID string, at time.Time) (bool, error) {
				return store.ClaimEvalRunResult(ctx, id, "", claimID, 1, time.Time{}, at)
			},
			func(id string) (*time.Time, error) {
				result, err := store.GetEvalRunResult(ctx, id)
				if err != nil {
					return nil, err
				}
				return result.StartedAt, nil
			}},
	}
	for _, c := range cases {
		claimed, err := c.claim(c.id, "d1", time.Time{})
		require.NoError(t, err)
		require.True(t, claimed, c.name)
		listed, err := c.read(c.id)
		require.NoError(t, err)
		require.NotNil(t, listed, c.name)
		time.Sleep(2 * time.Millisecond)
		claimed, err = c.claim(c.id, "d1", time.Time{})
		require.NoError(t, err)
		require.True(t, claimed, "%s: a redelivery restarts it", c.name)

		claimed, err = c.claim(c.id, "d1", *listed)
		require.NoError(t, err)
		assert.False(t, claimed, "%s: a recovery claim of an earlier start is refused, though the delivery matches", c.name)
		current, err := c.read(c.id)
		require.NoError(t, err)
		claimed, err = c.claim(c.id, "d2", *current)
		require.NoError(t, err)
		assert.False(t, claimed, "%s: a recovery claim names the delivery too", c.name)
		claimed, err = c.claim(c.id, "d1", *current)
		require.NoError(t, err)
		assert.True(t, claimed, "%s: the start as read is taken back", c.name)

		// The recovery claim leaves the start as it was and moves the claim to the sweep's
		// own ID. A redelivery of d1 no longer matches it, so it cannot restart the row
		// between the sweep's claim and its failure write.
		kept, err := c.read(c.id)
		require.NoError(t, err)
		require.NotNil(t, kept, c.name)
		assert.True(t, kept.Equal(*current), "%s: started_at is kept: %v, was %v", c.name, *kept, *current)
		claimed, err = c.claim(c.id, "d1", time.Time{})
		require.NoError(t, err)
		assert.False(t, claimed, "%s: a redelivery of the message that started it cannot restart it once the sweep owns it", c.name)

		// A write that fails after the claim is retried by the next sweep, which lists the
		// row under the sweep's claim and the same start.
		claimed, err = c.claim(c.id, EvalEndLostClaim, *current)
		require.NoError(t, err)
		assert.True(t, claimed, "%s: the next sweep's recovery claim of the same start holds", c.name)
	}
}

// assertEvalRunFencedEntryWrites checks the entry writes that hold the run: one at a
// status and generation, one at a review count. A run that moved on refuses the write.
func assertEvalRunFencedEntryWrites(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	ctx := context.Background()
	run, entries := newTestEvalRun(t, store, "ds1", 1, 1)
	state, err := store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)

	_, err = store.TransitionEvalRunEntryAtRun(ctx, run.ID, tables.EvalRunStopped, state.Generation, entries[0].ID, nil, map[string]any{"status": tables.EvalRunEntryStopped})
	require.ErrorIs(t, err, ErrEvalRunChanged, "the run is not stopped")
	_, err = store.TransitionEvalRunEntryAtRun(ctx, run.ID, tables.EvalRunRunning, state.Generation+1, entries[0].ID, nil, map[string]any{"status": tables.EvalRunEntryStopped})
	require.ErrorIs(t, err, ErrEvalRunChanged, "the run is at another generation")
	wrote, err := store.TransitionEvalRunEntryAtRun(ctx, run.ID, tables.EvalRunRunning, state.Generation, entries[0].ID, []string{tables.EvalRunEntryQueued}, map[string]any{"status": tables.EvalRunEntryEvaluating, "attempt": 1})
	require.NoError(t, err)
	assert.True(t, wrote)
	wrote, err = store.TransitionEvalRunEntryAtRun(ctx, run.ID, tables.EvalRunRunning, state.Generation, entries[0].ID, []string{tables.EvalRunEntryQueued}, map[string]any{"status": tables.EvalRunEntryStopped})
	require.NoError(t, err)
	assert.False(t, wrote, "the entry's status is checked too")

	_, err = store.TransitionEvalRunEntryAtReview(ctx, run.ID, state.ReviewSeq+1, state.Generation, entries[0].ID, 1, nil, map[string]any{"status": tables.EvalRunEntryCompleted})
	require.ErrorIs(t, err, ErrEvalRunChanged, "another review count")
	_, err = store.TransitionEvalRunEntryAtReview(ctx, run.ID, state.ReviewSeq, state.Generation+1, entries[0].ID, 1, nil, map[string]any{"status": tables.EvalRunEntryCompleted})
	require.ErrorIs(t, err, ErrEvalRunChanged, "another generation: an operation reopened the run since the read")
	wrote, err = store.TransitionEvalRunEntryAtReview(ctx, run.ID, state.ReviewSeq, state.Generation, entries[0].ID, 2, nil, map[string]any{"status": tables.EvalRunEntryCompleted})
	require.NoError(t, err)
	assert.False(t, wrote, "the attempt is checked")
	wrote, err = store.TransitionEvalRunEntryAtReview(ctx, run.ID, state.ReviewSeq, state.Generation, entries[0].ID, 1, []string{tables.EvalRunEntryEvaluating}, map[string]any{"status": tables.EvalRunEntryCompleted})
	require.NoError(t, err)
	assert.True(t, wrote)
	require.NoError(t, store.CountEvalRunReview(ctx, run.ID))
	_, err = store.TransitionEvalRunEntryAtReview(ctx, run.ID, state.ReviewSeq, state.Generation, entries[0].ID, 1, nil, map[string]any{"passed": true})
	require.ErrorIs(t, err, ErrEvalRunChanged, "a review counted since the read fences the write")
	after, err := store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, state.Generation, after.Generation, "holding the run moves neither count")
	assert.Equal(t, state.ReviewSeq+1, after.ReviewSeq)
}

func TestEvalRunFencedEntryWrites(t *testing.T) {
	assertEvalRunFencedEntryWrites(t, setupEvalDatasetTestStore(t))
}

func TestEvalRunFencedEntryWritesPostgres(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evalrun_fence_pg_test", &tables.TableEvalDataset{}, &tables.TableEvalDatasetVersion{}, &tables.TableEvalRun{}, &tables.TableEvalRunEntry{}, &tables.TableEvalRunResult{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	assertEvalRunFencedEntryWrites(t, store)
}

func TestEvalRunEntriesWithoutOpenResults(t *testing.T) {
	assertEvalRunEntriesWithoutOpenResults(t, setupEvalDatasetTestStore(t))
}

func TestEvalRunEntriesWithoutOpenResultsPostgres(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evalrun_open_results_pg_test", &tables.TableEvalDataset{}, &tables.TableEvalDatasetVersion{}, &tables.TableEvalRun{}, &tables.TableEvalRunEntry{}, &tables.TableEvalRunResult{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	assertEvalRunEntriesWithoutOpenResults(t, store)
}

// NoOpenResults lists the entries that have no queued or running result: an entry with
// only ended results, or none, is listed, and one with an open result is not.
func assertEvalRunEntriesWithoutOpenResults(t *testing.T, store *RDBConfigStore) {
	ctx := context.Background()
	run, entries := newTestEvalRun(t, store, "ds-open", 1, 4)
	require.NoError(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{
		{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued},
		{RunID: run.ID, EntryID: entries[1].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultRunning},
		{RunID: run.ID, EntryID: entries[2].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultCompleted},
	}, 0))
	refs, err := store.ListEvalRunEntryRefs(ctx, EvalRunEntryListParams{RunID: run.ID, NoOpenResults: true})
	require.NoError(t, err)
	var got []string
	for _, ref := range refs {
		got = append(got, ref.ID)
	}
	assert.Equal(t, []string{entries[2].ID, entries[3].ID}, got, "only entries with no queued or running result")
}

func TestEvalRunRecoveryClaim(t *testing.T) {
	assertEvalRunRecoveryClaim(t, setupEvalDatasetTestStore(t))
}

func TestEvalRunRecoveryClaimPostgres(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evalrun_claim_pg_test", &tables.TableEvalDataset{}, &tables.TableEvalDatasetVersion{}, &tables.TableEvalRun{}, &tables.TableEvalRunEntry{}, &tables.TableEvalRunResult{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	assertEvalRunRecoveryClaim(t, store)
}

// The Postgres forms of the run writes: whole-tag matching through jsonb, the run row lock
// taken before entries and results are stored, and the updated_at fence.
func TestEvalRunPostgresTagsLocksAndFence(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evalruns_pg_test", &tables.TableEvalDataset{}, &tables.TableEvalDatasetVersion{}, &tables.TableEvalRun{}, &tables.TableEvalRunEntry{}, &tables.TableEvalRunResult{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	ctx := context.Background()
	ensureTestEvalDataset(t, store, "ds")
	for _, tags := range [][]string{{`team"blue`}, {"BLUE"}, {"blue"}} {
		require.NoError(t, store.CreateEvalRun(ctx, &tables.TableEvalRun{Name: tags[0], Status: tables.EvalRunCompleted, TargetType: "dataset", DatasetID: "ds", DatasetVersion: 1, Tags: tags}))
	}
	_, total, err := store.ListEvalRuns(ctx, EvalRunListParams{Tag: "blue"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)

	run, entries := newTestEvalRun(t, store, "ds1", 1, 1)
	require.NoError(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}, 0))
	assertEvalRunGenerationFence(t, store)
	state, err := store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	moved, err := store.TransitionEvalRunAtGeneration(ctx, run.ID, []string{tables.EvalRunRunning}, state.Generation-1, map[string]any{"status": tables.EvalRunCompleted})
	require.NoError(t, err)
	assert.False(t, moved, "a stale read does not write")
	moved, err = store.TransitionEvalRunAtGeneration(ctx, run.ID, []string{tables.EvalRunRunning}, state.Generation, map[string]any{"status": tables.EvalRunCompleted})
	require.NoError(t, err)
	assert.True(t, moved, "the read it was based on is still current")

	require.NoError(t, store.DeleteEvalRun(ctx, run.ID))
	err = store.CreateEvalRunEntries(ctx, []tables.TableEvalRunEntry{{RunID: run.ID, Seq: 9, DatasetRowID: "late", Status: tables.EvalRunEntryQueued}})
	require.ErrorIs(t, err, ErrNotFound)
	err = store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev2", Status: tables.EvalRunResultQueued}}, 0)
	require.ErrorIs(t, err, ErrNotFound)
}

// assertResultsFencedOnTheEntryAttempt checks that results created for an entry's attempt
// are refused once a rerun reset the entry, and still created for the attempt that holds it.
func assertResultsFencedOnTheEntryAttempt(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	ctx := context.Background()
	run, entries := newTestEvalRun(t, store, "ds1", 1, 2)
	for _, entry := range entries {
		_, err := store.TransitionEvalRunEntry(ctx, entry.ID, nil, map[string]any{"status": tables.EvalRunEntryEvaluating, "attempt": 1, "output": "out"})
		require.NoError(t, err)
	}
	_, err := store.ResetEvalRunEntries(ctx, run.ID, run.Generation, []string{entries[0].ID}, map[string]any{"status": tables.EvalRunEntryQueued, "output": ""})
	require.NoError(t, err)

	err = store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}, 1)
	require.ErrorIs(t, err, ErrNotFound, "the entry was reset since its output was stored")
	results, err := store.ListEvalRunResults(ctx, EvalRunResultListParams{RunID: run.ID, EntryIDs: []string{entries[0].ID}})
	require.NoError(t, err)
	assert.Empty(t, results, "no result is stored on the reset entry")

	err = store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[1].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}, 2)
	require.ErrorIs(t, err, ErrNotFound, "another attempt holds the entry")
	require.NoError(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[1].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}, 1),
		"the attempt that holds the entry creates its results")
}

func TestEvalRunResultsFencedOnTheEntryAttempt(t *testing.T) {
	assertResultsFencedOnTheEntryAttempt(t, setupEvalDatasetTestStore(t))
}

func TestEvalRunResultsFencedOnTheEntryAttemptPostgres(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evalrun_fence_pg_test", &tables.TableEvalDataset{}, &tables.TableEvalDatasetVersion{}, &tables.TableEvalRun{}, &tables.TableEvalRunEntry{}, &tables.TableEvalRunResult{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	assertResultsFencedOnTheEntryAttempt(t, store)
}

// assertQueuedClaim checks the claim of a row that never started: it holds only while the
// row is queued on the key read and below the job's attempt. Once taken, it matches again
// only for a retry under the same claim ID, key and attempt (the end that followed it
// failed), never for another claim.
func assertQueuedClaim(t *testing.T, store *RDBConfigStore) {
	ctx := context.Background()
	run, entries := newTestEvalRun(t, store, "ds1", 1, 2)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued, KeyID: "k1"}}
	require.NoError(t, store.CreateEvalRunResults(ctx, results, 0))
	_, err := store.RouteEvalRunEntry(ctx, entries[0].ID, "", "k1", time.Now())
	require.NoError(t, err)

	claims := []struct {
		name    string
		claimAs func(id, keyID, claimID string, attempt int) (bool, error)
		id      string
		claim   func(id, keyID string, attempt int) (bool, error)
	}{
		{name: "entry", claimAs: func(id, keyID, claimID string, attempt int) (bool, error) {
			return store.ClaimQueuedEvalRunEntry(ctx, id, keyID, claimID, attempt)
		}, id: entries[0].ID},
		{name: "result", claimAs: func(id, keyID, claimID string, attempt int) (bool, error) {
			return store.ClaimQueuedEvalRunResult(ctx, id, keyID, claimID, attempt)
		}, id: results[0].ID},
	}
	for i := range claims {
		claimAs := claims[i].claimAs
		claims[i].claim = func(id, keyID string, attempt int) (bool, error) { return claimAs(id, keyID, "end", attempt) }
	}
	for _, c := range claims {
		claimed, err := c.claim(c.id, "k2", 1)
		require.NoError(t, err)
		assert.False(t, claimed, "%s: a row on another key is not claimed", c.name)
		claimed, err = c.claim(c.id, "k1", 0)
		require.NoError(t, err)
		assert.False(t, claimed, "%s: a job of an attempt the row is not below is refused", c.name)
		claimed, err = c.claim(c.id, "k1", 1)
		require.NoError(t, err)
		assert.True(t, claimed, "%s: the queued row on its key is claimed", c.name)
		claimed, err = c.claim(c.id, "k1", 1)
		require.NoError(t, err)
		assert.True(t, claimed, "%s: a retry under the same claim, after its end failed, matches again", c.name)
		claimed, err = c.claimAs(c.id, "k1", "other", 1)
		require.NoError(t, err)
		assert.False(t, claimed, "%s: a row running under another claim is not taken", c.name)
		claimed, err = c.claim(c.id, "k1", 2)
		require.NoError(t, err)
		assert.False(t, claimed, "%s: the retry matches only the attempt it claimed", c.name)
		claimed, err = c.claim(c.id, "k2", 1)
		require.NoError(t, err)
		assert.False(t, claimed, "%s: nor another key", c.name)
	}
	entry, err := store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryRunning, entry.Status)
	assert.Equal(t, 1, entry.Attempt)
	assert.Equal(t, "end", entry.ClaimID)
	result, err := store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultRunning, result.Status)
	assert.Equal(t, 1, result.Attempt)

	// A delivery that started the row first leaves nothing to claim.
	claimed, err := store.ClaimEvalRunEntry(ctx, entries[1].ID, "", "d1", 1, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = store.ClaimQueuedEvalRunEntry(ctx, entries[1].ID, "", "end", 1)
	require.NoError(t, err)
	assert.False(t, claimed, "a started entry is not claimed")

	// A claim whose end was never written is queued again as it was, once it is older than
	// the cutoff; a recent one and a delivery's claim are left alone.
	released, err := store.ReleaseQueuedEvalRunClaims(ctx, "end", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, released, "a claim newer than the cutoff is kept")
	released, err = store.ReleaseQueuedEvalRunClaims(ctx, "end", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.EqualValues(t, 2, released, "the entry and the result held under the claim")
	entry, err = store.GetEvalRunEntry(ctx, entries[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryQueued, entry.Status)
	assert.Equal(t, 0, entry.Attempt)
	assert.Empty(t, entry.ClaimID)
	assert.Nil(t, entry.StartedAt)
	assert.Equal(t, "k1", entry.KeyID, "it stays on its key")
	result, err = store.GetEvalRunResult(ctx, results[0].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunResultQueued, result.Status)
	assert.Equal(t, 0, result.Attempt)
	started, err := store.GetEvalRunEntry(ctx, entries[1].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryRunning, started.Status, "a delivery's claim is not released")
	claimed, err = store.ClaimQueuedEvalRunEntry(ctx, entries[0].ID, "k1", "end", 1)
	require.NoError(t, err)
	assert.True(t, claimed, "the released entry is claimed again at the same attempt")
}

func TestEvalRunQueuedClaim(t *testing.T) {
	assertQueuedClaim(t, setupEvalDatasetTestStore(t))
}

func TestEvalRunQueuedClaimPostgres(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evalrun_queued_claim_pg_test", &tables.TableEvalDataset{}, &tables.TableEvalDatasetVersion{}, &tables.TableEvalRun{}, &tables.TableEvalRunEntry{}, &tables.TableEvalRunResult{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	assertQueuedClaim(t, store)
}

// A row reset to queued keeps its attempt, and every dispatch of it publishes the stored
// attempt + 1. A delivery claiming a queued row at its stored attempt is from before the
// reset and is refused; the next attempt is claimed. A running row's own delivery still
// matches at the same attempt.
func assertClaimRefusesAStaleQueuedAttempt(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	ctx := context.Background()
	run, entries := newTestEvalRun(t, store, "ds-stale-claim", 1, 1)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}
	require.NoError(t, store.CreateEvalRunResults(ctx, results, 0))
	claims := []struct {
		name  string
		model any
		id    string
		claim func(id, claimID string, attempt int) (bool, error)
	}{
		{name: "entry", model: &tables.TableEvalRunEntry{}, id: entries[0].ID, claim: func(id, claimID string, attempt int) (bool, error) {
			return store.ClaimEvalRunEntry(ctx, id, "", claimID, attempt, time.Time{}, time.Time{})
		}},
		{name: "result", model: &tables.TableEvalRunResult{}, id: results[0].ID, claim: func(id, claimID string, attempt int) (bool, error) {
			return store.ClaimEvalRunResult(ctx, id, "", claimID, attempt, time.Time{}, time.Time{})
		}},
	}
	for _, c := range claims {
		claimed, err := c.claim(c.id, "D", 1)
		require.NoError(t, err)
		require.True(t, claimed, "%s: the first attempt is claimed", c.name)
		claimed, err = c.claim(c.id, "D", 1)
		require.NoError(t, err)
		assert.True(t, claimed, "%s: the same delivery of a running row matches again", c.name)
		require.NoError(t, store.DB().Model(c.model).Where("id = ?", c.id).
			UpdateColumns(map[string]any{"status": "queued", "claim_id": "", "started_at": nil}).Error)
		claimed, err = c.claim(c.id, "D", 1)
		require.NoError(t, err)
		assert.False(t, claimed, "%s: a delivery from before the reset is refused", c.name)
		claimed, err = c.claim(c.id, "D2", 2)
		require.NoError(t, err)
		assert.True(t, claimed, "%s: the next attempt is claimed", c.name)
	}
}

func TestEvalRunClaimRefusesAStaleQueuedAttempt(t *testing.T) {
	assertClaimRefusesAStaleQueuedAttempt(t, setupEvalDatasetTestStore(t))
}

func TestEvalRunClaimRefusesAStaleQueuedAttemptPostgres(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evalrun_stale_claim_pg_test", &tables.TableEvalDataset{}, &tables.TableEvalDatasetVersion{}, &tables.TableEvalRun{}, &tables.TableEvalRunEntry{}, &tables.TableEvalRunResult{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	assertClaimRefusesAStaleQueuedAttempt(t, store)
}

// The bulk entry write holds the run in the status and generation the caller read, and
// writes nothing once the run moved on.
func TestEvalRunEntriesAtRunHoldsTheRun(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	run, entries := newTestEvalRun(t, store, "ds1", 1, 2)
	_, err := store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunStopped})
	require.NoError(t, err)
	stop := map[string]any{"status": tables.EvalRunEntryStopped}
	_, err = store.TransitionEvalRunEntriesAtRun(ctx, run.ID, tables.EvalRunStopped, run.Generation+1, []string{tables.EvalRunEntryQueued}, stop)
	require.ErrorIs(t, err, ErrEvalRunChanged, "another generation writes nothing")
	changed, err := store.TransitionEvalRunEntriesAtRun(ctx, run.ID, tables.EvalRunStopped, run.Generation, []string{tables.EvalRunEntryQueued}, stop)
	require.NoError(t, err)
	assert.EqualValues(t, len(entries), changed)
}

// The claimed entry write needs the claim at the attempt; without a claim it only ends a
// queued attempt below the given one.
func TestEvalRunEntryClaimedTransition(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	_, entries := newTestEvalRun(t, store, "ds1", 1, 1)
	failed := map[string]any{"status": tables.EvalRunEntryFailed}
	claimed, err := store.ClaimEvalRunEntry(ctx, entries[0].ID, "", "d1", 1, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.True(t, claimed)
	running := []string{tables.EvalRunEntryQueued, tables.EvalRunEntryRunning}
	wrote, err := store.TransitionEvalRunEntryClaimed(ctx, entries[0].ID, 1, "other", running, failed)
	require.NoError(t, err)
	assert.False(t, wrote, "another delivery's claim does not write")
	wrote, err = store.TransitionEvalRunEntryClaimed(ctx, entries[0].ID, 1, "", running, failed)
	require.NoError(t, err)
	assert.False(t, wrote, "a claimed entry is not ended without the claim")
	// A rerun resets the entry to queued at the same attempt with the claim cleared.
	_, err = store.TransitionEvalRunEntry(ctx, entries[0].ID, nil, map[string]any{"status": tables.EvalRunEntryQueued, "claim_id": ""})
	require.NoError(t, err)
	wrote, err = store.TransitionEvalRunEntryClaimed(ctx, entries[0].ID, 1, "d1", running, failed)
	require.NoError(t, err)
	assert.False(t, wrote, "the old claim no longer owns the reset entry")
	wrote, err = store.TransitionEvalRunEntryClaimed(ctx, entries[0].ID, 1, "", running, failed)
	require.NoError(t, err)
	assert.False(t, wrote, "an unclaimed write at the same attempt leaves the reset entry")
	wrote, err = store.TransitionEvalRunEntryClaimed(ctx, entries[0].ID, 2, "", running, failed)
	require.NoError(t, err)
	assert.True(t, wrote, "a later attempt that never started ends it")
}

// The unstarted write ends a queued result that no delivery of the attempt, or a later
// one, has claimed, and leaves a claimed or running one alone.
func TestEvalRunResultUnstartedTransition(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	run, entries := newTestEvalRun(t, store, "ds1", 1, 1)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued}}
	require.NoError(t, store.CreateEvalRunResults(ctx, results, 0))
	failed := map[string]any{"status": tables.EvalRunResultFailed, "error": "no key"}

	claimed, err := store.ClaimEvalRunResult(ctx, results[0].ID, "", "d1", 1, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.True(t, claimed)
	wrote, err := store.TransitionEvalRunResultUnstarted(ctx, results[0].ID, 1, failed)
	require.NoError(t, err)
	assert.False(t, wrote, "a running result is not ended as unstarted")
	_, err = store.TransitionEvalRunResult(ctx, results[0].ID, nil, map[string]any{"status": tables.EvalRunResultQueued})
	require.NoError(t, err)
	wrote, err = store.TransitionEvalRunResultUnstarted(ctx, results[0].ID, 1, failed)
	require.NoError(t, err)
	assert.False(t, wrote, "a result claimed at the attempt was started by it")
	wrote, err = store.TransitionEvalRunResultUnstarted(ctx, results[0].ID, 2, failed)
	require.NoError(t, err)
	assert.True(t, wrote, "a later attempt that never started ends it")
}

// The retry write requeues a result running at the failed attempt, or queued below it,
// and leaves a later attempt's running row and an already requeued row alone.
func TestEvalRunResultRetryTransition(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	run, entries := newTestEvalRun(t, store, "ds1", 1, 1)
	results := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultRunning, Attempt: 2, KeyID: "k1"}}
	require.NoError(t, store.CreateEvalRunResults(ctx, results, 0))
	requeue := map[string]any{"status": tables.EvalRunResultQueued, "key_id": ""}

	wrote, err := store.TransitionEvalRunResultRetry(ctx, results[0].ID, 1, requeue)
	require.NoError(t, err)
	assert.False(t, wrote, "a late error of attempt 1 leaves attempt 2 running")
	wrote, err = store.TransitionEvalRunResultRetry(ctx, results[0].ID, 2, requeue)
	require.NoError(t, err)
	assert.True(t, wrote, "an error of the running attempt requeues it")
	wrote, err = store.TransitionEvalRunResultRetry(ctx, results[0].ID, 2, requeue)
	require.NoError(t, err)
	assert.False(t, wrote, "a second error of the same attempt does not requeue twice")
	wrote, err = store.TransitionEvalRunResultRetry(ctx, results[0].ID, 3, requeue)
	require.NoError(t, err)
	assert.True(t, wrote, "a later attempt that never started is requeued")

	// A result queued at attempt 1 that never started: the retry of attempt 2 stores the
	// attempt, so a second error of attempt 2 does not requeue it again.
	unstarted := []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev2", Status: tables.EvalRunResultQueued, Attempt: 1}}
	require.NoError(t, store.CreateEvalRunResults(ctx, unstarted, 0))
	withAttempt := map[string]any{"status": tables.EvalRunResultQueued, "key_id": "", "attempt": 2}
	wrote, err = store.TransitionEvalRunResultRetry(ctx, unstarted[0].ID, 2, withAttempt)
	require.NoError(t, err)
	assert.True(t, wrote, "the first error of attempt 2 requeues it")
	wrote, err = store.TransitionEvalRunResultRetry(ctx, unstarted[0].ID, 2, withAttempt)
	require.NoError(t, err)
	assert.False(t, wrote, "a second error of attempt 2 does not")
}

// assertEvalRunGenerationFence checks the writes of a multi-step operation: they hold the
// run while it is running at the operation's generation, and write nothing once a stop
// or another operation moved it on.
func assertEvalRunGenerationFence(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	ctx := context.Background()
	run, entries := newTestEvalRun(t, store, "ds-gen", 1, 2)
	state, err := store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	generation := state.Generation

	moved, err := store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"name": "renamed"})
	require.NoError(t, err)
	require.True(t, moved)
	state, err = store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, generation, state.Generation, "a rename does not move the generation")

	// A review moves the review count, and nothing else; a write fenced on the old count
	// is refused.
	reviewSeq, updatedAt := state.ReviewSeq, state.UpdatedAt
	require.NoError(t, store.CountEvalRunReview(ctx, run.ID))
	state, err = store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, reviewSeq+1, state.ReviewSeq)
	assert.Equal(t, generation, state.Generation, "a review does not move the generation")
	assert.True(t, updatedAt.Equal(state.UpdatedAt), "a review does not move updated_at")
	moved, err = store.TransitionEvalRunAtReview(ctx, run.ID, []string{tables.EvalRunRunning}, generation, reviewSeq, map[string]any{"name": "stale"})
	require.NoError(t, err)
	assert.False(t, moved, "a write fenced on the old review count is refused")
	moved, err = store.TransitionEvalRunAtReview(ctx, run.ID, []string{tables.EvalRunRunning}, generation, reviewSeq+1, map[string]any{"name": "current"})
	require.NoError(t, err)
	assert.True(t, moved, "the current count writes")

	changed, err := store.TransitionEvalRunEntriesAtGeneration(ctx, run.ID, generation, nil, []string{tables.EvalRunEntryQueued}, map[string]any{"status": tables.EvalRunEntryEvaluating})
	require.NoError(t, err)
	assert.EqualValues(t, 2, changed)
	_, err = store.ResetEvalRunEntries(ctx, run.ID, generation, []string{entries[0].ID}, map[string]any{"status": tables.EvalRunEntryQueued})
	require.NoError(t, err)

	moved, err = store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunStopped, "generation": EvalRunNextGeneration})
	require.NoError(t, err)
	require.True(t, moved)
	state, err = store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, generation+1, state.Generation, "a stop moves the generation")

	_, err = store.ResetEvalRunEntries(ctx, run.ID, generation, []string{entries[1].ID}, map[string]any{"status": tables.EvalRunEntryQueued})
	require.ErrorIs(t, err, ErrEvalRunChanged)
	_, err = store.TransitionEvalRunEntriesAtGeneration(ctx, run.ID, generation, map[string]any{"status": tables.EvalRunCompleted}, []string{tables.EvalRunEntryEvaluating}, map[string]any{"status": tables.EvalRunEntryCompleted})
	require.ErrorIs(t, err, ErrEvalRunChanged)
	entry, err := store.GetEvalRunEntry(ctx, entries[1].ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunEntryEvaluating, entry.Status, "nothing is written under a stopped run")
	state, err = store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunStopped, state.Status)

	// The stop's cleanup holds the run while it is stopped at the stop's generation.
	_, err = store.TransitionStoppedEvalRunEntries(ctx, run.ID, generation, []string{tables.EvalRunEntryEvaluating}, false, map[string]any{"status": tables.EvalRunEntryStopped})
	require.ErrorIs(t, err, ErrEvalRunChanged, "an older generation is not the stop's")
	stopped, err := store.TransitionStoppedEvalRunEntries(ctx, run.ID, generation+1, []string{tables.EvalRunEntryEvaluating}, false, map[string]any{"status": tables.EvalRunEntryStopped})
	require.NoError(t, err)
	assert.EqualValues(t, 1, stopped)
	moved, err = store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunRunning, "generation": EvalRunNextGeneration})
	require.NoError(t, err)
	require.True(t, moved)
	_, err = store.TransitionStoppedEvalRunEntries(ctx, run.ID, generation+1, nil, false, map[string]any{"status": tables.EvalRunEntryStopped})
	require.ErrorIs(t, err, ErrEvalRunChanged, "a rerun that claimed the stopped run keeps its entries")
}

func TestEvalRunGenerationFence(t *testing.T) {
	assertEvalRunGenerationFence(t, setupEvalDatasetTestStore(t))
}

// assertEvalRunSweepListing checks what the sweep's passes read: entries paged across runs
// by (run_id, seq), each ref with its queue time, and results kept to evaluators that call
// no model.
func assertEvalRunSweepListing(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	ctx := context.Background()
	first, firstEntries := newTestEvalRun(t, store, "ds-sweep", 1, 2)
	second, secondEntries := newTestEvalRun(t, store, "ds-sweep", 1, 2)
	if second.ID < first.ID {
		first, second = second, first
		firstEntries, secondEntries = secondEntries, firstEntries
	}
	refs, err := store.ListEvalRunEntryRefs(ctx, EvalRunEntryListParams{AfterRunID: first.ID, AfterSeq: 1, Statuses: []string{tables.EvalRunEntryQueued}})
	require.NoError(t, err)
	got := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.RunID == first.ID || ref.RunID == second.ID {
			got = append(got, ref.ID)
		}
		assert.False(t, ref.QueuedAt.IsZero(), "the ref carries its queue time")
	}
	assert.Equal(t, []string{firstEntries[1].ID, secondEntries[0].ID, secondEntries[1].ID}, got, "paging resumes after (run, seq)")

	for _, ev := range []struct{ id, provider string }{{"ev-local", ""}, {"ev-model", "openai"}} {
		require.NoError(t, store.DB().Create(&tables.TableEvaluatorVersion{ID: ev.id + "-v1", EvaluatorID: ev.id, Version: 1, Config: json.RawMessage(`{}`), ModelProvider: ev.provider}).Error)
	}
	require.NoError(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{
		{RunID: first.ID, EntryID: firstEntries[0].ID, EvaluatorID: "ev-local", Status: tables.EvalRunResultQueued},
		{RunID: first.ID, EntryID: firstEntries[0].ID, EvaluatorID: "ev-model", Status: tables.EvalRunResultQueued},
	}, 0))
	local, err := store.ListEvalRunResultRefs(ctx, EvalRunResultListParams{RunID: first.ID, LocalOnly: true})
	require.NoError(t, err)
	require.Len(t, local, 1)
	assert.Equal(t, "ev-local", local[0].EvaluatorID)
	assert.False(t, local[0].QueuedAt.IsZero(), "the ref carries its queue time")
}

func TestEvalRunSweepListing(t *testing.T) {
	store := setupEvalDatasetTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableEvaluatorVersion{}))
	assertEvalRunSweepListing(t, store)
}

func TestEvalRunSweepListingPostgres(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evalrun_sweep_pg_test", &tables.TableEvalDataset{}, &tables.TableEvalDatasetVersion{}, &tables.TableEvalRun{}, &tables.TableEvalRunEntry{}, &tables.TableEvalRunResult{}, &tables.TableEvaluatorVersion{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	assertEvalRunSweepListing(t, store)
}

// assertEvalRunClaimWithEntries checks that a claim of a finished run and the write to its
// entries commit together: a stale generation writes neither, a claim writes both, and a
// failing entry write undoes the claim.
func assertEvalRunClaimWithEntries(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	ctx := context.Background()
	run, entries := newTestEvalRun(t, store, "ds-claim", 1, 2)
	_, err := store.TransitionEvalRunEntries(ctx, run.ID, nil, nil, map[string]any{"status": tables.EvalRunEntryCompleted})
	require.NoError(t, err)
	_, err = store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunCompleted})
	require.NoError(t, err)
	require.NoError(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{{RunID: run.ID, EntryID: entries[0].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultCompleted}}, 0))
	state, err := store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	finished := []string{tables.EvalRunCompleted, tables.EvalRunFailed, tables.EvalRunStopped}
	claim := map[string]any{"status": tables.EvalRunRunning, "generation": EvalRunNextGeneration}
	reopen := &EvalRunEntryUpdate{IDs: []string{entries[0].ID}, Fields: map[string]any{"status": tables.EvalRunEntryQueued}, DeleteResults: true}
	countsAre := func(want map[string]int64, msg string) {
		t.Helper()
		counts, err := store.CountEvalRunEntries(ctx, run.ID)
		require.NoError(t, err)
		assert.Equal(t, want, counts, msg)
	}

	claimed, err := store.ClaimEvalRunWithEntries(ctx, run.ID, finished, state.Generation-1, claim, reopen)
	require.NoError(t, err)
	assert.False(t, claimed, "a stale generation claims nothing")
	countsAre(map[string]int64{tables.EvalRunEntryCompleted: 2}, "and reopens nothing")

	broken := &EvalRunEntryUpdate{Fields: map[string]any{"status": tables.EvalRunEntryEvaluating, "no_such_column": 1}}
	_, err = store.ClaimEvalRunWithEntries(ctx, run.ID, finished, state.Generation, claim, broken)
	require.Error(t, err)
	after, err := store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, after.Status, "a failed entry write undoes the claim")
	assert.Equal(t, state.Generation, after.Generation)

	// A status filter could leave a listed entry alone while its results are deleted.
	filtered := &EvalRunEntryUpdate{IDs: []string{entries[0].ID}, From: []string{tables.EvalRunEntryFailed}, Fields: map[string]any{"status": tables.EvalRunEntryQueued}, DeleteResults: true}
	_, err = store.ClaimEvalRunWithEntries(ctx, run.ID, finished, state.Generation, claim, filtered)
	require.ErrorContains(t, err, "no status filter")
	after, err = store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunCompleted, after.Status, "a refused update claims nothing")
	kept, err := store.ListEvalRunResults(ctx, EvalRunResultListParams{RunID: run.ID})
	require.NoError(t, err)
	assert.Len(t, kept, 1, "and deletes nothing")

	claimed, err = store.ClaimEvalRunWithEntries(ctx, run.ID, finished, state.Generation, claim, reopen)
	require.NoError(t, err)
	assert.True(t, claimed)
	countsAre(map[string]int64{tables.EvalRunEntryQueued: 1, tables.EvalRunEntryCompleted: 1}, "the listed entry is reset with the claim")
	results, err := store.ListEvalRunResults(ctx, EvalRunResultListParams{RunID: run.ID})
	require.NoError(t, err)
	assert.Empty(t, results, "its results are deleted with it")
	after, err = store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, tables.EvalRunRunning, after.Status)
	assert.Equal(t, state.Generation+1, after.Generation)
}

func TestEvalRunClaimWithEntries(t *testing.T) {
	assertEvalRunClaimWithEntries(t, setupEvalDatasetTestStore(t))
}

func TestEvalRunClaimWithEntriesPostgres(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evalrun_claimentries_pg_test", &tables.TableEvalDataset{}, &tables.TableEvalDatasetVersion{}, &tables.TableEvalRun{}, &tables.TableEvalRunEntry{}, &tables.TableEvalRunResult{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	assertEvalRunClaimWithEntries(t, store)
}

// assertStoppedCleanupEndsIdleEvaluatingEntries checks the stop's cleanup with
// idleEvaluating: an entry evaluating with no result queued or running stops, one with a
// result still queued keeps waiting for it, and a done result does not hold an entry.
func assertStoppedCleanupEndsIdleEvaluatingEntries(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	ctx := context.Background()
	run, entries := newTestEvalRun(t, store, "ds-idle", 1, 4)
	for i, status := range []string{tables.EvalRunEntryEvaluating, tables.EvalRunEntryEvaluating, tables.EvalRunEntryEvaluating, tables.EvalRunEntryQueued} {
		_, err := store.TransitionEvalRunEntry(ctx, entries[i].ID, nil, map[string]any{"status": status})
		require.NoError(t, err)
	}
	require.NoError(t, store.CreateEvalRunResults(ctx, []tables.TableEvalRunResult{
		{RunID: run.ID, EntryID: entries[1].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultQueued},
		{RunID: run.ID, EntryID: entries[2].ID, EvaluatorID: "ev1", Status: tables.EvalRunResultCompleted},
	}, 0))
	_, err := store.TransitionEvalRun(ctx, run.ID, nil, map[string]any{"status": tables.EvalRunStopped, "generation": EvalRunNextGeneration})
	require.NoError(t, err)
	state, err := store.GetEvalRunState(ctx, run.ID)
	require.NoError(t, err)

	changed, err := store.TransitionStoppedEvalRunEntries(ctx, run.ID, state.Generation, []string{tables.EvalRunEntryQueued}, true, map[string]any{"status": tables.EvalRunEntryStopped})
	require.NoError(t, err)
	assert.EqualValues(t, 3, changed)
	for i, want := range []string{tables.EvalRunEntryStopped, tables.EvalRunEntryEvaluating, tables.EvalRunEntryStopped, tables.EvalRunEntryStopped} {
		entry, err := store.GetEvalRunEntry(ctx, entries[i].ID)
		require.NoError(t, err)
		assert.Equal(t, want, entry.Status, "entry %d", i)
	}
}

func TestStoppedCleanupEndsIdleEvaluatingEntries(t *testing.T) {
	assertStoppedCleanupEndsIdleEvaluatingEntries(t, setupEvalDatasetTestStore(t))
}

func TestStoppedCleanupEndsIdleEvaluatingEntriesPostgres(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evalrun_idle_pg_test", &tables.TableEvalDataset{}, &tables.TableEvalDatasetVersion{}, &tables.TableEvalRun{}, &tables.TableEvalRunEntry{}, &tables.TableEvalRunResult{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	assertStoppedCleanupEndsIdleEvaluatingEntries(t, store)
}
