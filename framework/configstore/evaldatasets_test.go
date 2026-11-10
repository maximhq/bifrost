package configstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
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

	require.NoError(t, store.DeleteEvalDataset(ctx, dataset.ID))
	_, err = store.GetEvalDataset(ctx, dataset.ID)
	require.ErrorIs(t, err, ErrNotFound)
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

	found, err := store.ListEvalDatasetRows(ctx, EvalDatasetRowListParams{DatasetID: dataset.ID, Search: "FIV"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, ids[4], found[0].RowID)

	count, err := store.CountEvalDatasetRows(ctx, dataset.ID, 0, "", "")
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
	count, err := store.CountEvalDatasetRows(ctx, dataset.ID, 0, split.ID, "")
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)

	require.NoError(t, store.DeleteEvalDatasetSplit(ctx, dataset.ID, split.ID))
	splits, err := store.ListEvalDatasetSplits(ctx, dataset.ID)
	require.NoError(t, err)
	assert.Empty(t, splits)
	all, err := store.CountEvalDatasetRows(ctx, dataset.ID, 0, "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 4, all, "deleting a split keeps its rows")
}
