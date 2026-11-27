package configstore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupEvalDatasetTestStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(
		&tables.TableEvalDataset{}, &tables.TableEvalDatasetColumn{}, &tables.TableEvalDatasetVersion{},
		&tables.TableEvalDatasetRow{}, &tables.TableEvalDatasetSplit{}, &tables.TableEvalDatasetSplitRow{}, &tables.TableEvalDatasetFolder{}, &tables.TableEvalDatasetFile{},
	))
	return store
}

// newTestEvalDataset creates a dataset with an input and an expected_output column and
// returns it with the two column IDs.
func newTestEvalDataset(t *testing.T, store *RDBConfigStore, name string) (*tables.TableEvalDataset, string, string) {
	t.Helper()
	dataset := &tables.TableEvalDataset{Name: name, Description: "support questions", Tags: []string{"support"}}
	columns := []tables.TableEvalDatasetColumn{
		{Name: "input", Type: "input"},
		{Name: "expected_output", Type: "expected_output"},
	}
	require.NoError(t, store.CreateEvalDataset(context.Background(), dataset, columns))
	stored, err := store.ListEvalDatasetColumns(context.Background(), dataset.ID)
	require.NoError(t, err)
	require.Len(t, stored, 2)
	return dataset, stored[0].ID, stored[1].ID
}

func cellsJSON(pairs ...string) string {
	out := "{"
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%q:%q", pairs[i], pairs[i+1])
	}
	return out + "}"
}

// evalDatasetDo runs fn inside the dataset's write transaction and fails the test on error.
func evalDatasetDo(t *testing.T, store *RDBConfigStore, datasetID string, fn func(tx EvalDatasetTx) error) {
	t.Helper()
	require.NoError(t, store.WithEvalDataset(context.Background(), datasetID, fn))
}

// addTestRows adds one row per input value and returns the row IDs in order.
func addTestRows(t *testing.T, store *RDBConfigStore, datasetID, inputCol string, inputs ...string) []string {
	t.Helper()
	var ids []string
	evalDatasetDo(t, store, datasetID, func(tx EvalDatasetTx) error {
		cells := make([]string, len(inputs))
		for i, input := range inputs {
			cells[i] = cellsJSON(inputCol, input)
		}
		rows, err := tx.AddRows(cells, "")
		for _, row := range rows {
			ids = append(ids, row.RowID)
		}
		return err
	})
	return ids
}

func saveTestVersion(t *testing.T, store *RDBConfigStore, datasetID, name string) *tables.TableEvalDatasetVersion {
	t.Helper()
	var version *tables.TableEvalDatasetVersion
	evalDatasetDo(t, store, datasetID, func(tx EvalDatasetTx) error {
		var err error
		version, err = tx.SaveVersion(EvalDatasetVersionSave{Name: name, CreatedBy: "tester"})
		return err
	})
	return version
}

// rowInputs lists the input cell of every row of a version (0 is the draft), in order.
func rowInputs(t *testing.T, store *RDBConfigStore, datasetID, inputCol string, version int) []string {
	t.Helper()
	rows, err := store.ListEvalDatasetRows(context.Background(), EvalDatasetRowListParams{DatasetID: datasetID, Version: version})
	require.NoError(t, err)
	out := make([]string, 0, len(rows))
	for i := range rows {
		require.NoError(t, rows[i].DecodeCells())
		out = append(out, rows[i].Cells[inputCol])
	}
	return out
}

func TestEvalDatasetCreateGetAndSoftDelete(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)

	dataset, inputCol, _ := newTestEvalDataset(t, store, "support")
	require.NotEmpty(t, dataset.ID)

	got, err := store.GetEvalDataset(ctx, dataset.ID)
	require.NoError(t, err)
	assert.Equal(t, "support", got.Name)
	assert.Equal(t, []string{"support"}, got.Tags)
	assert.Equal(t, 0, got.HeadVersion, "nothing is saved yet")
	assert.Nil(t, got.MaxVersions)

	other := &tables.TableEvalDataset{Name: "support"}
	require.ErrorIs(t, store.CreateEvalDataset(ctx, other, nil), ErrEvalDatasetNameExists)

	addTestRows(t, store, dataset.ID, inputCol, "a", "b")
	saveTestVersion(t, store, dataset.ID, "")

	list, total, err := store.ListEvalDatasets(ctx, EvalDatasetListParams{Search: "SUPP"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, list, 1)
	counts, err := store.GetEvalDatasetCounts(ctx, []string{dataset.ID})
	require.NoError(t, err)
	assert.Equal(t, EvalDatasetCounts{Columns: 2, Rows: 2, Splits: 0}, counts[dataset.ID])

	_, err = store.DeleteEvalDataset(ctx, dataset.ID)
	require.NoError(t, err)
	_, err = store.GetEvalDataset(ctx, dataset.ID)
	require.ErrorIs(t, err, ErrNotFound)
	// An upload that lost the race with the delete is refused, so its object can be removed.
	err = store.CreateEvalDatasetFile(ctx, &tables.TableEvalDatasetFile{DatasetID: dataset.ID, Name: "late.png", ContentType: "image/png", Size: 1, ObjectKey: "k"})
	require.ErrorIs(t, err, ErrNotFound, "no file row for a deleted dataset")
	require.NoError(t, store.CreateEvalDataset(ctx, other, nil), "a deleted dataset frees its name")

	var leftover int64
	require.NoError(t, store.DB().Model(&tables.TableEvalDatasetRow{}).Where("dataset_id = ?", dataset.ID).Count(&leftover).Error)
	assert.Zero(t, leftover, "deleting a dataset removes its rows")
}

func TestEvalDatasetRowsPageBySequence(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, _ := newTestEvalDataset(t, store, "paging")
	ids := addTestRows(t, store, dataset.ID, inputCol, "one", "two", "three", "four", "five")

	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		_, err := tx.DeleteRows([]string{ids[1]})
		return err
	})

	first, err := store.ListEvalDatasetRows(ctx, EvalDatasetRowListParams{DatasetID: dataset.ID, Limit: 2})
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.EqualValues(t, 1, first[0].Seq)
	assert.EqualValues(t, 3, first[1].Seq, "a deleted row leaves a gap in the sequence")

	second, err := store.ListEvalDatasetRows(ctx, EvalDatasetRowListParams{DatasetID: dataset.ID, Limit: 2, AfterSeq: first[1].Seq})
	require.NoError(t, err)
	require.Len(t, second, 2)
	assert.EqualValues(t, 4, second[0].Seq)

	found, err := store.ListEvalDatasetRows(ctx, EvalDatasetRowListParams{DatasetID: dataset.ID, Search: "FIV", SearchColumns: []string{inputCol}})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, ids[4], found[0].RowID)

	count, err := store.CountEvalDatasetRows(ctx, EvalDatasetRowListParams{DatasetID: dataset.ID, SplitID: ""})
	require.NoError(t, err)
	assert.EqualValues(t, 4, count)
}

func TestEvalDatasetVersionsKeepWhatTheyRead(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, _ := newTestEvalDataset(t, store, "versions")
	ids := addTestRows(t, store, dataset.ID, inputCol, "a", "b")

	v1 := saveTestVersion(t, store, dataset.ID, "first")
	assert.Equal(t, 1, v1.Number)
	assert.EqualValues(t, 2, v1.RowCount)
	assert.EqualValues(t, 2, v1.RowsAdded)
	require.Len(t, v1.Columns, 2)

	// Edit b twice, add c, delete a. The two edits must leave one new record, not two.
	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		if _, err := tx.SetRowCells(ids[1], cellsJSON(inputCol, "b-draft")); err != nil {
			return err
		}
		if _, err := tx.SetRowCells(ids[1], cellsJSON(inputCol, "b2")); err != nil {
			return err
		}
		if _, err := tx.AddRows([]string{cellsJSON(inputCol, "c")}, ""); err != nil {
			return err
		}
		_, err := tx.DeleteRows([]string{ids[0]})
		return err
	})
	var records int64
	require.NoError(t, store.DB().Model(&tables.TableEvalDatasetRow{}).Where("dataset_id = ? AND row_id = ?", dataset.ID, ids[1]).Count(&records).Error)
	assert.EqualValues(t, 2, records, "an edit inside the draft updates the draft record in place")

	v2 := saveTestVersion(t, store, dataset.ID, "second")
	assert.Equal(t, 2, v2.Number)
	assert.EqualValues(t, 2, v2.RowCount)
	assert.EqualValues(t, 1, v2.RowsAdded)
	assert.EqualValues(t, 1, v2.RowsChanged)
	assert.EqualValues(t, 1, v2.RowsRemoved)

	assert.Equal(t, []string{"a", "b"}, rowInputs(t, store, dataset.ID, inputCol, 1), "version 1 still reads what it read")
	assert.Equal(t, []string{"b2", "c"}, rowInputs(t, store, dataset.ID, inputCol, 2))
	assert.Equal(t, []string{"b2", "c"}, rowInputs(t, store, dataset.ID, inputCol, 0))

	old, err := store.GetEvalDatasetRow(ctx, dataset.ID, ids[1], 1)
	require.NoError(t, err)
	require.NoError(t, old.DecodeCells())
	assert.Equal(t, "b", old.Cells[inputCol])
	_, err = store.GetEvalDatasetRow(ctx, dataset.ID, ids[0], 0)
	require.ErrorIs(t, err, ErrNotFound, "a deleted row is gone from the draft")

	err = store.WithEvalDataset(ctx, dataset.ID, func(tx EvalDatasetTx) error {
		_, err := tx.SaveVersion(EvalDatasetVersionSave{})
		return err
	})
	require.ErrorIs(t, err, ErrEvalDatasetNoChanges)

	versions, err := store.ListEvalDatasetVersions(ctx, dataset.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, 2, versions[0].Number, "newest first")

	require.NoError(t, store.UpdateEvalDatasetVersion(ctx, dataset.ID, 1, "baseline", "before the rewrite"))
	renamed, err := store.GetEvalDatasetVersion(ctx, dataset.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, "baseline", renamed.Name)
}

func TestEvalDatasetVersionDiff(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, _ := newTestEvalDataset(t, store, "diff")
	ids := addTestRows(t, store, dataset.ID, inputCol, "a", "b", "c")
	saveTestVersion(t, store, dataset.ID, "")

	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		if _, err := tx.SetRowCells(ids[1], cellsJSON(inputCol, "b2")); err != nil {
			return err
		}
		if _, err := tx.DeleteRows([]string{ids[2]}); err != nil {
			return err
		}
		_, err := tx.AddRows([]string{cellsJSON(inputCol, "d")}, "")
		return err
	})
	saveTestVersion(t, store, dataset.ID, "")

	changes, err := store.DiffEvalDatasetVersions(ctx, dataset.ID, 1, 2, 0, 100)
	require.NoError(t, err)
	require.Len(t, changes, 3)

	assert.Equal(t, ids[1], changes[0].RowID)
	require.NotNil(t, changes[0].Before)
	require.NotNil(t, changes[0].After)
	assert.JSONEq(t, cellsJSON(inputCol, "b"), changes[0].Before.CellsJSON)
	assert.JSONEq(t, cellsJSON(inputCol, "b2"), changes[0].After.CellsJSON)

	assert.Equal(t, ids[2], changes[1].RowID)
	assert.NotNil(t, changes[1].Before)
	assert.Nil(t, changes[1].After, "removed")

	assert.Nil(t, changes[2].Before, "added")
	assert.NotNil(t, changes[2].After)

	page, err := store.DiffEvalDatasetVersions(ctx, dataset.ID, 1, 2, changes[0].Seq, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, ids[2], page[0].RowID)

	fromEmpty, err := store.DiffEvalDatasetVersions(ctx, dataset.ID, 0, 1, 0, 100)
	require.NoError(t, err)
	assert.Len(t, fromEmpty, 3, "against nothing, every row is added")
}

func TestEvalDatasetRestore(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, outputCol := newTestEvalDataset(t, store, "restore")
	ids := addTestRows(t, store, dataset.ID, inputCol, "a", "b")
	saveTestVersion(t, store, dataset.ID, "")

	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		if _, err := tx.SetRowCells(ids[0], cellsJSON(inputCol, "a2")); err != nil {
			return err
		}
		if _, err := tx.DeleteRows([]string{ids[1]}); err != nil {
			return err
		}
		if _, err := tx.AddRows([]string{cellsJSON(inputCol, "c")}, ""); err != nil {
			return err
		}
		return tx.DeleteColumn(outputCol)
	})
	saveTestVersion(t, store, dataset.ID, "")

	var restored *tables.TableEvalDatasetVersion
	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		if err := tx.Restore(1); err != nil {
			return err
		}
		var err error
		restored, err = tx.SaveVersion(EvalDatasetVersionSave{RestoredFrom: new(1), Force: true})
		return err
	})
	assert.Equal(t, 3, restored.Number, "a restore is a new version; history is not rewritten")
	require.NotNil(t, restored.RestoredFrom)
	assert.Equal(t, 1, *restored.RestoredFrom)

	assert.Equal(t, []string{"a", "b"}, rowInputs(t, store, dataset.ID, inputCol, 0))
	assert.Equal(t, []string{"a2", "c"}, rowInputs(t, store, dataset.ID, inputCol, 2), "the version restored over is untouched")

	rows, err := store.ListEvalDatasetRows(ctx, EvalDatasetRowListParams{DatasetID: dataset.ID})
	require.NoError(t, err)
	assert.Equal(t, ids, []string{rows[0].RowID, rows[1].RowID}, "restored rows keep their row IDs")

	columns, err := store.ListEvalDatasetColumns(ctx, dataset.ID)
	require.NoError(t, err)
	require.Len(t, columns, 2, "the deleted column is back")
	assert.Equal(t, outputCol, columns[1].ID)
}

func TestEvalDatasetDeleteVersionAndMaxVersions(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, _ := newTestEvalDataset(t, store, "retention")
	ids := addTestRows(t, store, dataset.ID, inputCol, "a")
	saveTestVersion(t, store, dataset.ID, "")
	for _, value := range []string{"a2", "a3", "a4"} {
		evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
			_, err := tx.SetRowCells(ids[0], cellsJSON(inputCol, value))
			return err
		})
		saveTestVersion(t, store, dataset.ID, "")
	}
	recordCount := func() int64 {
		var n int64
		require.NoError(t, store.DB().Model(&tables.TableEvalDatasetRow{}).Where("dataset_id = ?", dataset.ID).Count(&n).Error)
		return n
	}
	require.EqualValues(t, 4, recordCount())

	err := store.WithEvalDataset(ctx, dataset.ID, func(tx EvalDatasetTx) error { return tx.DeleteVersion(4) })
	require.ErrorIs(t, err, ErrEvalDatasetLatestVersion)

	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error { return tx.DeleteVersion(2) })
	_, err = store.GetEvalDatasetVersion(ctx, dataset.ID, 2)
	require.ErrorIs(t, err, ErrNotFound)
	assert.EqualValues(t, 3, recordCount(), "the record only version 2 needed is gone")
	assert.Equal(t, []string{"a"}, rowInputs(t, store, dataset.ID, inputCol, 1))
	assert.Equal(t, []string{"a3"}, rowInputs(t, store, dataset.ID, inputCol, 3))

	changes, err := store.DiffEvalDatasetVersions(ctx, dataset.ID, 1, 3, 0, 10)
	require.NoError(t, err)
	require.Len(t, changes, 1, "a diff across a deleted version still works")
	assert.JSONEq(t, cellsJSON(inputCol, "a"), changes[0].Before.CellsJSON)
	assert.JSONEq(t, cellsJSON(inputCol, "a3"), changes[0].After.CellsJSON)

	// With a cap of two, the next save drops the oldest version.
	dataset.MaxVersions = new(2)
	require.NoError(t, store.UpdateEvalDataset(ctx, dataset))
	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		_, err := tx.SetRowCells(ids[0], cellsJSON(inputCol, "a5"))
		return err
	})
	v5 := saveTestVersion(t, store, dataset.ID, "")
	assert.Equal(t, 5, v5.Number, "a deleted version's number is never reused")

	versions, err := store.ListEvalDatasetVersions(ctx, dataset.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, []int{5, 4}, []int{versions[0].Number, versions[1].Number})
	assert.EqualValues(t, 2, recordCount())
	assert.Equal(t, []string{"a4"}, rowInputs(t, store, dataset.ID, inputCol, 4))

	// A prune binds a bound on the version number, not one parameter per version it deletes.
	dataset.MaxVersions = nil
	require.NoError(t, store.UpdateEvalDataset(ctx, dataset))
	for _, value := range []string{"a6", "a7", "a8"} {
		evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
			_, err := tx.SetRowCells(ids[0], cellsJSON(inputCol, value))
			return err
		})
		saveTestVersion(t, store, dataset.ID, "")
	}
	versions, err = store.ListEvalDatasetVersions(ctx, dataset.ID)
	require.NoError(t, err)
	require.Len(t, versions, 5)
	db := store.DB()
	var pruneVars int
	require.NoError(t, db.Callback().Delete().After("gorm:delete").Register("count_prune_vars", func(tx *gorm.DB) {
		if tx.Statement.Table == "evals_dataset_versions" {
			pruneVars = len(tx.Statement.Vars)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Delete().Remove("count_prune_vars") })
	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		if err := tx.UpdateMetadata(dataset.Name, dataset.Description, dataset.Tags, new(1)); err != nil {
			return err
		}
		return tx.PruneVersions()
	})
	assert.Equal(t, 2, pruneVars, "the dataset ID and the highest number pruned")
	versions, err = store.ListEvalDatasetVersions(ctx, dataset.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	assert.Equal(t, 8, versions[0].Number)
}

func TestEvalDatasetColumns(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, outputCol := newTestEvalDataset(t, store, "columns")
	addTestRows(t, store, dataset.ID, inputCol, "a")
	saveTestVersion(t, store, dataset.ID, "")

	var contextCol string
	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		column := &tables.TableEvalDatasetColumn{Name: "context", Type: "variable"}
		if err := tx.AddColumn(column); err != nil {
			return err
		}
		contextCol = column.ID
		return tx.ReorderColumns([]string{contextCol, inputCol, outputCol})
	})
	columns, err := store.ListEvalDatasetColumns(ctx, dataset.ID)
	require.NoError(t, err)
	require.Len(t, columns, 3)
	assert.Equal(t, []string{"context", "input", "expected_output"}, []string{columns[0].Name, columns[1].Name, columns[2].Name})

	err = store.WithEvalDataset(ctx, dataset.ID, func(tx EvalDatasetTx) error {
		return tx.AddColumn(&tables.TableEvalDatasetColumn{Name: "context", Type: "variable"})
	})
	require.ErrorIs(t, err, ErrEvalDatasetColumnNameExists)

	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		if err := tx.UpdateColumn(&tables.TableEvalDatasetColumn{ID: contextCol, Name: "docs", Type: "variable"}); err != nil {
			return err
		}
		return tx.DeleteColumn(outputCol)
	})
	v2 := saveTestVersion(t, store, dataset.ID, "")
	require.Len(t, v2.Columns, 2)
	assert.Equal(t, "docs", v2.Columns[0].Name)

	v1, err := store.GetEvalDatasetVersion(ctx, dataset.ID, 1)
	require.NoError(t, err)
	require.Len(t, v1.Columns, 2, "version 1 keeps the columns it was saved with")
	assert.Equal(t, "expected_output", v1.Columns[1].Name)
}

func TestEvalDatasetSplits(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, _ := newTestEvalDataset(t, store, "splits")
	ids := addTestRows(t, store, dataset.ID, inputCol, "a", "b", "c")

	split := &tables.TableEvalDatasetSplit{DatasetID: dataset.ID, Name: "smoke"}
	require.NoError(t, store.CreateEvalDatasetSplit(ctx, split))
	require.ErrorIs(t, store.CreateEvalDatasetSplit(ctx, &tables.TableEvalDatasetSplit{DatasetID: dataset.ID, Name: "smoke"}), ErrEvalDatasetSplitNameExists)

	added, err := store.AddEvalDatasetSplitRows(ctx, dataset.ID, split.ID, []string{ids[0], ids[2], "not-a-row"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, added, "only rows of the dataset join a split")
	again, err := store.AddEvalDatasetSplitRows(ctx, dataset.ID, split.ID, []string{ids[0]})
	require.NoError(t, err)
	assert.Zero(t, again, "adding a member twice is a no-op")

	rows, err := store.ListEvalDatasetRows(ctx, EvalDatasetRowListParams{DatasetID: dataset.ID, SplitID: split.ID})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, []string{ids[0], ids[2]}, []string{rows[0].RowID, rows[1].RowID})

	// Membership follows the row through an edit.
	saveTestVersion(t, store, dataset.ID, "")
	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		_, err := tx.SetRowCells(ids[0], cellsJSON(inputCol, "a2"))
		return err
	})
	counts, err := store.CountEvalDatasetSplitRows(ctx, dataset.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, counts[split.ID])

	// Rows added straight into a split.
	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		_, err := tx.AddRows([]string{cellsJSON(inputCol, "d")}, split.ID)
		return err
	})
	removed, err := store.RemoveEvalDatasetSplitRows(ctx, dataset.ID, split.ID, []string{ids[2]})
	require.NoError(t, err)
	assert.EqualValues(t, 1, removed)
	count, err := store.CountEvalDatasetRows(ctx, EvalDatasetRowListParams{DatasetID: dataset.ID, SplitID: split.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)

	require.NoError(t, store.DeleteEvalDatasetSplit(ctx, dataset.ID, split.ID))
	splits, err := store.ListEvalDatasetSplits(ctx, dataset.ID)
	require.NoError(t, err)
	assert.Empty(t, splits)
	all, err := store.CountEvalDatasetRows(ctx, EvalDatasetRowListParams{DatasetID: dataset.ID, SplitID: ""})
	require.NoError(t, err)
	assert.EqualValues(t, 4, all, "deleting a split keeps its rows")

	// One call naming a row twice, then a second call with it again, add it once.
	repeat := &tables.TableEvalDatasetSplit{DatasetID: dataset.ID, Name: "repeat"}
	require.NoError(t, store.CreateEvalDatasetSplit(ctx, repeat))
	added, err = store.AddEvalDatasetSplitRows(ctx, dataset.ID, repeat.ID, []string{ids[1], ids[1]})
	require.NoError(t, err)
	assert.EqualValues(t, 1, added)
	added, err = store.AddEvalDatasetSplitRows(ctx, dataset.ID, repeat.ID, []string{ids[1]})
	require.NoError(t, err)
	assert.Zero(t, added)

	checkEvalDatasetSplitMembershipMovesUpdatedAt(t, store)
}

// checkEvalDatasetSplitMembershipMovesUpdatedAt pins that every membership write that
// changes a split moves its updated_at, and one that changes nothing does not. A dataset
// copy reads it to tell that a split changed while it was read page by page.
func checkEvalDatasetSplitMembershipMovesUpdatedAt(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	ctx := context.Background()
	dataset, inputCol, _ := newTestEvalDataset(t, store, "split-updated-at")
	ids := addTestRows(t, store, dataset.ID, inputCol, "a", "b")
	split := &tables.TableEvalDatasetSplit{DatasetID: dataset.ID, Name: "moving"}
	require.NoError(t, store.CreateEvalDatasetSplit(ctx, split))
	updatedAt := func() time.Time {
		splits, err := store.ListEvalDatasetSplits(ctx, dataset.ID)
		require.NoError(t, err)
		require.Len(t, splits, 1)
		return splits[0].UpdatedAt
	}
	last := updatedAt()
	moved := func(what string) {
		now := updatedAt()
		assert.True(t, now.After(last), "%s moves updated_at: %v then %v", what, last, now)
		last = now
	}
	_, err := store.AddEvalDatasetSplitRows(ctx, dataset.ID, split.ID, []string{ids[0]})
	require.NoError(t, err)
	moved("an add")
	_, _, err = store.SetEvalDatasetSplitRows(ctx, dataset.ID, split.ID, []string{ids[1]}, []string{ids[0]})
	require.NoError(t, err)
	moved("a set")
	_, err = store.RemoveEvalDatasetSplitRows(ctx, dataset.ID, split.ID, []string{ids[1]})
	require.NoError(t, err)
	moved("a remove")
	_, err = store.RemoveEvalDatasetSplitRows(ctx, dataset.ID, split.ID, []string{ids[1]})
	require.NoError(t, err)
	assert.True(t, updatedAt().Equal(last), "a write that changes nothing leaves updated_at")
}

// A split membership goes with the last record of its row: pruning the only version that read
// a deleted row leaves no link to it.
func TestEvalDatasetSplitLinksFollowPrunedRows(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, _ := newTestEvalDataset(t, store, "split-prune")
	dataset.MaxVersions = new(1)
	require.NoError(t, store.UpdateEvalDataset(ctx, dataset))
	ids := addTestRows(t, store, dataset.ID, inputCol, "a", "b")
	split := &tables.TableEvalDatasetSplit{DatasetID: dataset.ID, Name: "smoke"}
	require.NoError(t, store.CreateEvalDatasetSplit(ctx, split))
	_, err := store.AddEvalDatasetSplitRows(ctx, dataset.ID, split.ID, []string{ids[0]})
	require.NoError(t, err)
	saveTestVersion(t, store, dataset.ID, "")
	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		_, err := tx.DeleteRows([]string{ids[0]})
		return err
	})
	saveTestVersion(t, store, dataset.ID, "")
	evalDatasetDo(t, store, dataset.ID, func(tx EvalDatasetTx) error {
		_, err := tx.SetRowCells(ids[1], cellsJSON(inputCol, "b2"))
		return err
	})
	saveTestVersion(t, store, dataset.ID, "")
	var records, links int64
	require.NoError(t, store.DB().Model(&tables.TableEvalDatasetRow{}).Where("dataset_id = ? AND row_id = ?", dataset.ID, ids[0]).Count(&records).Error)
	require.Zero(t, records, "no version reads the deleted row any more")
	require.NoError(t, store.DB().Model(&tables.TableEvalDatasetSplitRow{}).Where("split_id = ? AND row_id = ?", split.ID, ids[0]).Count(&links).Error)
	assert.Zero(t, links, "the split link went with the row's last record")
}

// A tag filter compares whole tags: a tag that contains the wanted one in its JSON text, such
// as x"foo for foo, does not match.
func TestEvalDatasetTagFilterMatchesWholeTags(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	require.NoError(t, store.CreateEvalDataset(ctx, &tables.TableEvalDataset{Name: "quoted", Tags: []string{`x"foo`}}, nil))
	plain := &tables.TableEvalDataset{Name: "plain", Tags: []string{"bar", "foo"}}
	require.NoError(t, store.CreateEvalDataset(ctx, plain, nil))
	list, total, err := store.ListEvalDatasets(ctx, EvalDatasetListParams{Tag: "foo"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, list, 1)
	assert.Equal(t, plain.ID, list[0].ID)
	_, total, err = store.ListEvalDatasets(ctx, EvalDatasetListParams{Tag: `x"foo`})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "a tag with a quote matches itself")
}

// A folder move that would close a loop is refused by the store, inside the move's own
// transaction, so two moves at once cannot build a cycle between them.
func TestEvalDatasetFolderMoveRefusesCycles(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	a := &tables.TableEvalDatasetFolder{Name: "a"}
	b := &tables.TableEvalDatasetFolder{Name: "b"}
	require.NoError(t, store.CreateEvalDatasetFolder(ctx, a))
	require.NoError(t, store.CreateEvalDatasetFolder(ctx, b))
	require.NoError(t, store.SetEvalDatasetFolderParent(ctx, a.ID, &b.ID))
	require.ErrorIs(t, store.SetEvalDatasetFolderParent(ctx, b.ID, &a.ID), ErrEvalDatasetFolderCycle)
	require.ErrorIs(t, store.SetEvalDatasetFolderParent(ctx, b.ID, &b.ID), ErrEvalDatasetFolderCycle)
	require.NoError(t, store.SetEvalDatasetFolderParent(ctx, a.ID, nil))
}

// Folder writes read the dataset and the folders they touch through the caller's scope. A
// dataset or folder the scope hides is not found, and nothing about it changes.
func TestEvalDatasetFolderWritesReadThroughTheCallersScope(t *testing.T) {
	checkEvalDatasetFolderWritesScope(t, setupEvalDatasetTestStore(t))
}

func TestEvalDatasetPostgresFolderWritesReadThroughTheCallersScope(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evaldatasets_scope_pg_test",
		&tables.TableEvalDatasetFolder{}, &tables.TableEvalDataset{}, &tables.TableEvalDatasetColumn{}, &tables.TableEvalDatasetRow{},
		&tables.TableEvalDatasetVersion{}, &tables.TableEvalDatasetSplit{}, &tables.TableEvalDatasetSplitRow{}, &tables.TableEvalDatasetFile{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	checkEvalDatasetFolderWritesScope(t, store)
}

func checkEvalDatasetFolderWritesScope(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	ctx := context.Background()
	dataset, _, _ := newTestEvalDataset(t, store, "scoped-move")
	dest := &tables.TableEvalDatasetFolder{Name: "scoped-dest"}
	require.NoError(t, store.CreateEvalDatasetFolder(ctx, dest))
	other := &tables.TableEvalDatasetFolder{Name: "scoped-other"}
	require.NoError(t, store.CreateEvalDatasetFolder(ctx, other))
	hiding := func(id string) context.Context {
		return queryscope.WithQueryScope(ctx, queryscope.QueryScope(func(db *gorm.DB) *gorm.DB { return db.Where("id <> ?", id) }))
	}
	folderOf := func() *string {
		got, err := store.GetEvalDataset(ctx, dataset.ID)
		require.NoError(t, err)
		return got.FolderID
	}

	assert.ErrorIs(t, store.SetEvalDatasetFolder(hiding(dataset.ID), dataset.ID, &dest.ID), ErrNotFound, "a hidden dataset is not moved")
	assert.Nil(t, folderOf(), "the hidden dataset stays where it was")
	assert.ErrorIs(t, store.SetEvalDatasetFolder(hiding(dest.ID), dataset.ID, &dest.ID), ErrNotFound, "a hidden folder takes no dataset")
	assert.Nil(t, folderOf())
	assert.ErrorIs(t, store.CreateEvalDatasetFolder(hiding(dest.ID), &tables.TableEvalDatasetFolder{Name: "child", ParentID: new(dest.ID)}), ErrNotFound, "a hidden folder takes no new folder")
	assert.ErrorIs(t, store.SetEvalDatasetFolderParent(hiding(dest.ID), other.ID, &dest.ID), ErrNotFound, "a hidden folder takes no folder")
	assert.ErrorIs(t, store.CreateEvalDataset(hiding(dest.ID), &tables.TableEvalDataset{Name: "scoped-new", FolderID: new(dest.ID)}, nil), ErrNotFound, "a hidden folder takes no new dataset")

	assert.ErrorIs(t, store.UpdateEvalDatasetFolder(hiding(other.ID), &tables.TableEvalDatasetFolder{ID: other.ID, Name: "renamed"}), ErrNotFound, "a hidden folder is not renamed")
	assert.ErrorIs(t, store.SetEvalDatasetFolderParent(hiding(other.ID), other.ID, &dest.ID), ErrNotFound, "a hidden folder is not moved")
	assert.ErrorIs(t, store.DeleteEvalDatasetFolder(hiding(other.ID), other.ID), ErrNotFound, "a hidden folder is not deleted")
	folders, err := store.ListEvalDatasetFolders(ctx)
	require.NoError(t, err)
	byID := map[string]tables.TableEvalDatasetFolder{}
	for _, folder := range folders {
		byID[folder.ID] = folder
	}
	require.Len(t, byID, 2, "no folder was created or deleted")
	assert.Equal(t, "scoped-other", byID[other.ID].Name)
	assert.Nil(t, byID[other.ID].ParentID)

	visible := hiding("none")
	require.NoError(t, store.SetEvalDatasetFolder(visible, dataset.ID, &dest.ID), "a scope that shows both still moves the dataset")
	assert.Equal(t, &dest.ID, folderOf())
	renamed := &tables.TableEvalDatasetFolder{ID: other.ID, Name: "renamed"}
	require.NoError(t, store.UpdateEvalDatasetFolder(visible, renamed))
	assert.Equal(t, "renamed", renamed.Name)
	require.NoError(t, store.SetEvalDatasetFolderParent(visible, other.ID, &dest.ID))
	require.NoError(t, store.DeleteEvalDatasetFolder(visible, other.ID))
}

// A diff page is read in one statement: rows removed while it is read cannot leave a page that
// is empty although later changes exist.
func TestEvalDatasetVersionDiffReadsOnePage(t *testing.T) {
	ctx := context.Background()
	store := setupEvalDatasetTestStore(t)
	dataset, inputCol, _ := newTestEvalDataset(t, store, "diff-race")
	addTestRows(t, store, dataset.ID, inputCol, "a", "b", "c")
	db := store.DB()
	fired := false
	var dropped int64
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("drop_first_rows", func(tx *gorm.DB) {
		// A subquery is built in a dry run pass that reads nothing; the delete belongs after the real read.
		if fired || tx.DryRun || tx.Statement.Table != "evals_dataset_rows" {
			return
		}
		fired = true
		result := tx.Session(&gorm.Session{NewDB: true}).Exec("DELETE FROM evals_dataset_rows WHERE dataset_id = ? AND seq IN (1, 2)", dataset.ID)
		_ = tx.AddError(result.Error)
		dropped = result.RowsAffected
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("drop_first_rows") })
	changes, err := store.DiffEvalDatasetVersions(ctx, dataset.ID, 0, 1, 0, 3)
	require.NoError(t, err)
	assert.EqualValues(t, 2, dropped, "the delete ran after the page was read")
	seqs := make([]int64, len(changes))
	for i := range changes {
		seqs[i] = changes[i].Seq
	}
	assert.Equal(t, []int64{1, 2, 3}, seqs, "the page holds what was there when it was read")
}

// The Postgres forms of the dataset reads and locks: the search reads cell values through
// jsonb_each_text, folder writes take row locks, and the diff reads one statement.
func TestEvalDatasetPostgresSearchLocksAndDiff(t *testing.T) {
	store := trySetupPostgresSchemaStore(t, "configstore_evaldatasets_pg_test",
		&tables.TableEvalDatasetFolder{}, &tables.TableEvalDataset{}, &tables.TableEvalDatasetColumn{}, &tables.TableEvalDatasetRow{},
		&tables.TableEvalDatasetVersion{}, &tables.TableEvalDatasetSplit{}, &tables.TableEvalDatasetSplitRow{}, &tables.TableEvalDatasetFile{})
	if store == nil {
		t.Skip("Postgres not available")
	}
	ctx := context.Background()
	dataset, inputCol, _ := newTestEvalDataset(t, store, "pg")
	addTestRows(t, store, dataset.ID, inputCol, `say "hi"`, "plain")
	found, err := store.ListEvalDatasetRows(ctx, EvalDatasetRowListParams{DatasetID: dataset.ID, Search: `"HI"`, SearchColumns: []string{inputCol}})
	require.NoError(t, err)
	assert.Len(t, found, 1)
	count, err := store.CountEvalDatasetRows(ctx, EvalDatasetRowListParams{DatasetID: dataset.ID, Search: "plain", SearchColumns: []string{"other"}})
	require.NoError(t, err)
	assert.Zero(t, count, "only the named columns are searched")
	require.NoError(t, store.CreateEvalDataset(ctx, &tables.TableEvalDataset{Name: "pg-quoted", Tags: []string{`x"support`}}, nil))
	require.NoError(t, store.CreateEvalDataset(ctx, &tables.TableEvalDataset{Name: "pg-untagged"}, nil))
	tagged, total, err := store.ListEvalDatasets(ctx, EvalDatasetListParams{Tag: "support"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "tags are compared as whole array elements")
	require.Len(t, tagged, 1)
	assert.Equal(t, dataset.ID, tagged[0].ID)

	folder := &tables.TableEvalDatasetFolder{Name: "f"}
	require.NoError(t, store.CreateEvalDatasetFolder(ctx, folder))
	require.NoError(t, store.SetEvalDatasetFolder(ctx, dataset.ID, &folder.ID))
	require.NoError(t, store.DeleteEvalDatasetFolder(ctx, folder.ID))
	got, err := store.GetEvalDataset(ctx, dataset.ID)
	require.NoError(t, err)
	assert.Nil(t, got.FolderID, "the delete moved the dataset up")

	changes, err := store.DiffEvalDatasetVersions(ctx, dataset.ID, 0, 1, 0, 1)
	require.NoError(t, err)
	assert.Len(t, changes, 1)

	checkEvalDatasetSplitMembershipMovesUpdatedAt(t, store)

	// Two folder deletes at once: deleting A, which holds B, while B is deleted. Without one lock
	// over the hierarchy, B's delete moves the dataset into A after A's sweep read it, and the
	// dataset is left in a folder that no longer exists.
	outer := &tables.TableEvalDatasetFolder{Name: "outer"}
	require.NoError(t, store.CreateEvalDatasetFolder(ctx, outer))
	inner := &tables.TableEvalDatasetFolder{Name: "inner", ParentID: new(outer.ID)}
	require.NoError(t, store.CreateEvalDatasetFolder(ctx, inner))
	require.NoError(t, store.SetEvalDatasetFolder(ctx, dataset.ID, &inner.ID))
	db := store.DB()
	var fired atomic.Bool
	var innerDeleted sync.WaitGroup
	var innerErr error
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("delete_inner_folder", func(tx *gorm.DB) {
		if tx.Statement.Table != "evals_dataset_folders" {
			return
		}
		if !fired.CompareAndSwap(false, true) {
			return
		}
		func() {
			innerDeleted.Add(1)
			done := make(chan struct{})
			go func() {
				defer innerDeleted.Done()
				defer close(done)
				innerErr = store.DeleteEvalDatasetFolder(ctx, inner.ID)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
			}
		}()
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove("delete_inner_folder") })
	require.NoError(t, store.DeleteEvalDatasetFolder(ctx, outer.ID))
	innerDeleted.Wait()
	require.NoError(t, innerErr)
	require.NoError(t, db.Callback().Update().Remove("delete_inner_folder"))
	got, err = store.GetEvalDataset(ctx, dataset.ID)
	require.NoError(t, err)
	assert.Nil(t, got.FolderID, "the dataset is not left in a deleted folder")

	// A split created while its dataset is deleted either lands first and goes with the delete,
	// or finds the dataset gone. It never outlives the dataset.
	doomed, _, _ := newTestEvalDataset(t, store, "doomed")
	var splitFired atomic.Bool
	var deleted sync.WaitGroup
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("delete_dataset_under_split", func(tx *gorm.DB) {
		if tx.Statement.Table != "evals_dataset_splits" {
			return
		}
		if !splitFired.CompareAndSwap(false, true) {
			return
		}
		func() {
			deleted.Add(1)
			done := make(chan struct{})
			go func() {
				defer deleted.Done()
				defer close(done)
				_, _ = store.DeleteEvalDataset(ctx, doomed.ID)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
			}
		}()
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove("delete_dataset_under_split") })
	splitErr := store.CreateEvalDatasetSplit(ctx, &tables.TableEvalDatasetSplit{DatasetID: doomed.ID, Name: "late"})
	deleted.Wait()
	if splitErr != nil {
		require.ErrorIs(t, splitErr, ErrNotFound)
	}
	var orphans int64
	require.NoError(t, db.Model(&tables.TableEvalDatasetSplit{}).Where("dataset_id = ?", doomed.ID).Count(&orphans).Error)
	assert.Zero(t, orphans, "no split outlives its dataset")
}
