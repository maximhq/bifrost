package datasets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"gorm.io/gorm"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLogs is a log store holding a fixed set of logs.
type fakeLogs struct {
	logs     map[string]*logstore.Log
	sessions map[string][]string // session ID to log IDs, oldest first
	errs     map[string]error    // log ID to the error its lookup fails with
	listed   func()              // runs after each listing page is served
	stamps   map[string]int      // log ID to its listing timestamp, given on first sight
}

// stamp gives a listed log a stable, unique timestamp: a log listed for the first time
// is newer than every log listed before it, as a session's next request is.
func (f *fakeLogs) stamp(id string) time.Time {
	if f.stamps == nil {
		f.stamps = map[string]int{}
	}
	n, ok := f.stamps[id]
	if !ok {
		n = len(f.stamps) + 1
		f.stamps[id] = n
	}
	return time.Unix(int64(n), 0).UTC()
}

func (f *fakeLogs) FindByID(_ context.Context, id string) (*logstore.Log, error) {
	if err := f.errs[id]; err != nil {
		return nil, err
	}
	log, ok := f.logs[id]
	if !ok {
		return nil, fmt.Errorf("log %s: %w", id, logstore.ErrNotFound)
	}
	return log, nil
}

func (f *fakeLogs) SearchLogs(_ context.Context, filters logstore.SearchFilters, pagination logstore.PaginationOptions) (*logstore.SearchResult, error) {
	ids := slices.Clone(f.sessions[filters.SessionID])
	for _, id := range ids {
		f.stamp(id)
	}
	if pagination.Order == "desc" {
		slices.Reverse(ids)
	}
	// The keyset cursor, as the RDB store applies it: only logs strictly after
	// (AfterTimestamp, AfterID) in the requested order. Stamps are unique, so the
	// timestamp alone orders them.
	if pagination.AfterTimestamp != nil && pagination.AfterID != "" {
		bound := *pagination.AfterTimestamp
		ids = slices.DeleteFunc(ids, func(id string) bool {
			if pagination.Order == "desc" {
				return !f.stamp(id).Before(bound)
			}
			return !f.stamp(id).After(bound)
		})
	}
	result := &logstore.SearchResult{}
	for i := pagination.Offset; i < len(ids) && i < pagination.Offset+pagination.Limit; i++ {
		// A listing carries no content, as the real one does not.
		result.Logs = append(result.Logs, logstore.Log{ID: ids[i], Timestamp: f.stamp(ids[i])})
	}
	if f.listed != nil {
		f.listed()
	}
	return result, nil
}

func newTestService(tb testing.TB, logs LogReader) *Service {
	tb.Helper()
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(tb.TempDir(), "datasets.db")},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(tb, err)
	return NewService(store, logs)
}

func columnID(t *testing.T, view *DatasetView, name string) string {
	t.Helper()
	for _, column := range view.Columns {
		if column.Name == name {
			return column.ID
		}
	}
	t.Fatalf("no column named %q", name)
	return ""
}

func TestServiceCreateAndEditRows(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)

	_, err := svc.Create(ctx, DatasetInput{Name: "bad", Template: "nope"})
	require.True(t, evals.IsInvalid(err))

	view, err := svc.Create(ctx, DatasetInput{Name: "support", Template: "retrieval", Columns: []ColumnSpec{{Name: "labels", Type: ColumnTags}}})
	require.NoError(t, err)
	require.Len(t, view.Columns, 4)
	input, contextCol := columnID(t, view, "input"), columnID(t, view, "context")

	_, err = svc.Create(ctx, DatasetInput{Name: "support"})
	require.ErrorIs(t, err, configstore.ErrEvalDatasetNameExists)

	// Cells may be keyed by column name or column ID.
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{
		{"input": "how do I reset my password", "context": "account docs"},
		{input: "where is my invoice", "context": "billing docs"},
	}, "", "tester")
	require.NoError(t, err)
	require.Len(t, added.Rows, 2)
	assert.Nil(t, added.Version, "two rows stay in the draft")

	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"nope": "x"}}, "", "tester")
	require.True(t, evals.IsInvalid(err), "an unknown column is rejected")
	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"labels": "not json"}}, "", "tester")
	require.True(t, evals.IsInvalid(err), "a structured column takes JSON only")
	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"context": "a\x00b"}}, "", "tester")
	require.True(t, evals.IsInvalid(err), "a NUL character is rejected: Postgres cannot read it back as JSON: %v", err)
	// Tags are read back as JSON by the tag filter on Postgres, so a NUL in a tag is refused too.
	_, err = svc.Create(ctx, DatasetInput{Name: "nul tag", Tags: []string{"a\x00b"}})
	require.True(t, evals.IsInvalid(err), "a NUL character in a tag is rejected on create: %v", err)
	_, err = svc.Update(ctx, view.ID, DatasetInput{Name: "support", Tags: []string{"a\x00b"}})
	require.True(t, evals.IsInvalid(err), "a NUL character in a tag is rejected on update: %v", err)
	// The row bounds the spec documents (minItems 1, maxItems MaxRowsPerRequest) are enforced here.
	_, err = svc.AddRows(ctx, view.ID, nil, "", "tester")
	require.True(t, evals.IsInvalid(err), "an add with no rows is rejected: %v", err)
	_, err = svc.AddRows(ctx, view.ID, make([]map[string]string, MaxRowsPerRequest+1), "", "tester")
	require.True(t, evals.IsInvalid(err), "an add past the row cap is rejected: %v", err)
	_, err = svc.DeleteRows(ctx, view.ID, nil, "tester")
	require.True(t, evals.IsInvalid(err), "a delete with no row IDs is rejected: %v", err)
	_, err = svc.DeleteRows(ctx, view.ID, make([]string, MaxRowsPerRequest+1), "tester")
	require.True(t, evals.IsInvalid(err), "a delete past the row cap is rejected: %v", err)
	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{input: "a", "input": "b"}}, "", "tester")
	require.True(t, evals.IsInvalid(err), "a column set twice, by ID and by name, to different values is rejected: %v", err)
	same, err := svc.AddRows(ctx, view.ID, []map[string]string{{input: "same", "input": "same"}}, "", "tester")
	require.NoError(t, err, "the same value by ID and by name is one cell")
	_, err = svc.DeleteRows(ctx, view.ID, []string{same.Rows[0].RowID}, "tester")
	require.NoError(t, err)
	// An empty cell is not stored, so the add answers without it, as the next list does.
	blank, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "", "expected_output": "x"}}, "", "tester")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{columnID(t, view, "expected_output"): "x"}, blank.Rows[0].Cells, "an empty cell is left out of the answer")
	_, err = svc.DeleteRows(ctx, view.ID, []string{blank.Rows[0].RowID}, "tester")
	require.NoError(t, err)

	rowID := added.Rows[0].RowID
	updated, err := svc.UpdateRow(ctx, view.ID, rowID, map[string]string{"context": "", "expected_output": "use the reset link"}, "tester")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{input: "how do I reset my password", columnID(t, view, "expected_output"): "use the reset link"}, updated.Rows[0].Cells,
		"a patch changes the named cells only, and an empty value clears a cell")

	variables, err := svc.RowVariables(ctx, view.ID, rowID, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"input": "how do I reset my password", "expected_output": "use the reset link"}, variables)

	// A deleted column's cells stop showing up without any row being rewritten.
	page, err := svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, page.Rows, 2)
	assert.Equal(t, "billing docs", page.Rows[1].Cells[contextCol], "the second row has a context cell before the delete")
	require.NoError(t, svc.DeleteColumn(ctx, view.ID, contextCol, "tester"))
	page, err = svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, page.Rows, 2)
	assert.EqualValues(t, 2, page.Total)
	assert.Zero(t, page.NextAfter)
	_, present := page.Rows[1].Cells[contextCol]
	assert.False(t, present)

	_, err = svc.AddColumn(ctx, view.ID, ColumnSpec{Name: "input2", Type: ColumnInput}, "tester")
	require.True(t, evals.IsInvalid(err), "a second input column is rejected")
	_, err = svc.AddColumn(ctx, view.ID, ColumnSpec{Name: "output", Type: ColumnVariable}, "tester")
	require.True(t, evals.IsInvalid(err), "a variable column cannot shadow a typed variable")

	paged, err := svc.Rows(ctx, view.ID, RowQuery{Limit: 1})
	require.NoError(t, err)
	require.Len(t, paged.Rows, 1)
	assert.Equal(t, paged.Rows[0].Seq, paged.NextAfter, "a full page returns the cursor for the next one")

	// The grid reads any page by row position, so a jump to the end is one call.
	jumped, err := svc.Rows(ctx, view.ID, RowQuery{Offset: 1, Limit: 1})
	require.NoError(t, err)
	require.Len(t, jumped.Rows, 1)
	assert.Equal(t, added.Rows[1].RowID, jumped.Rows[0].RowID)
	assert.EqualValues(t, 2, jumped.Total)
	_, err = svc.Rows(ctx, view.ID, RowQuery{Offset: -1})
	require.True(t, evals.IsInvalid(err))

	// With a search, the total counts the matching rows only: the grid sizes itself by it.
	found, err := svc.Rows(ctx, view.ID, RowQuery{Search: "INVOICE"})
	require.NoError(t, err)
	require.Len(t, found.Rows, 1)
	assert.EqualValues(t, 1, found.Total)

	// A paste or a range delete changes many rows and may add rows, all in one call.
	secondRow := added.Rows[1].RowID
	edited, err := svc.EditCells(ctx, view.ID, CellEdits{
		Updates: []RowPatch{
			{RowID: rowID, Cells: map[string]string{"input": "pasted one"}},
			{RowID: secondRow, Cells: map[string]string{"input": "", "expected_output": "pasted two"}},
		},
		Rows: []map[string]string{{"input": "pasted three"}},
	}, "tester")
	require.NoError(t, err)
	require.Len(t, edited.Updated, 2)
	require.Len(t, edited.Added, 1)
	expected := columnID(t, view, "expected_output")
	assert.Equal(t, map[string]string{input: "pasted one", expected: "use the reset link"}, edited.Updated[0].Cells)
	assert.Equal(t, map[string]string{expected: "pasted two"}, edited.Updated[1].Cells)
	assert.Equal(t, map[string]string{input: "pasted three"}, edited.Added[0].Cells)

	// One bad cell rejects the whole call: nothing is half applied.
	_, err = svc.EditCells(ctx, view.ID, CellEdits{
		Updates: []RowPatch{{RowID: rowID, Cells: map[string]string{"input": "must not stick"}}, {RowID: secondRow, Cells: map[string]string{"labels": "not json"}}},
	}, "tester")
	require.True(t, evals.IsInvalid(err))
	_, err = svc.EditCells(ctx, view.ID, CellEdits{}, "tester")
	require.True(t, evals.IsInvalid(err), "an empty edit is rejected")
	page, err = svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, page.Rows, 3)
	assert.Equal(t, "pasted one", page.Rows[0].Cells[input])

	// A reader that only walks the rows, as run planning does, skips the count.
	db := svc.store.(*configstore.RDBConfigStore).DB()
	counts := 0
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("count_row_counts", func(tx *gorm.DB) {
		if tx.Statement.Table == "evals_dataset_rows" && strings.Contains(strings.ToLower(tx.Statement.SQL.String()), "count(") {
			counts++
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("count_row_counts") })
	walked, err := svc.Rows(ctx, view.ID, RowQuery{SkipTotal: true})
	require.NoError(t, err)
	require.Len(t, walked.Rows, 3)
	assert.Zero(t, walked.Total)
	assert.Zero(t, counts, "SkipTotal reads no count")
}

func TestServiceSaveRules(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "rules", Template: "qa"})
	require.NoError(t, err)

	_, err = svc.SaveVersion(ctx, view.ID, "empty", "", "tester")
	require.ErrorIs(t, err, configstore.ErrEvalDatasetNoChanges)

	// Small edits collect in the draft until the threshold, then save on their own.
	var last *WriteResult
	for i := range AutoSaveThreshold {
		last, err = svc.AddRows(ctx, view.ID, []map[string]string{{"input": fmt.Sprintf("q%d", i)}}, "", "tester")
		require.NoError(t, err)
		if i < AutoSaveThreshold-1 {
			require.Nil(t, last.Version, "edit %d stays in the draft", i+1)
		}
	}
	require.NotNil(t, last.Version)
	assert.Equal(t, 1, *last.Version)

	// A bulk change saves the waiting edits first, then itself: two versions.
	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"input": "one more"}}, "", "tester")
	require.NoError(t, err)
	imported, err := svc.Import(ctx, view.ID, strings.NewReader("input,expected_output\nfrom csv,yes\n"), ImportOptions{HasHeader: true, Actor: "tester"})
	require.NoError(t, err)
	assert.Equal(t, 1, imported.RowsAdded)
	require.NotNil(t, imported.Version)
	assert.Equal(t, 3, *imported.Version)

	versions, err := svc.Versions(ctx, view.ID)
	require.NoError(t, err)
	require.Len(t, versions, 3)
	assert.Equal(t, "Imported from CSV", versions[0].Description)
	assert.EqualValues(t, 1, versions[0].RowsAdded, "the import stands alone in its version")
	assert.EqualValues(t, 1, versions[1].RowsAdded)
	assert.Equal(t, "tester", versions[0].CreatedBy)

	named, err := svc.UpdateVersion(ctx, view.ID, 3, "with csv", "first import")
	require.NoError(t, err)
	assert.Equal(t, "with csv", named.Name)
}

func TestServiceImport(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "import", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "calls", Type: ColumnExpectedToolCalls},
	}})
	require.NoError(t, err)

	file := "\xEF\xBB\xBFInput,Calls,Context,ignored\n\"multi\nline\",\"[{\"\"name\"\":\"\"x\"\"}]\",docs,zzz\n\nsecond,,,\n"
	preview, err := svc.PreviewImport(ctx, view.ID, strings.NewReader(file[:60]))
	require.NoError(t, err)
	assert.Equal(t, []string{"Input", "Calls", "Context", "ignored"}, preview.Header)
	assert.Equal(t, []string{columnID(t, view, "input"), columnID(t, view, "calls"), "", ""}, preview.Mapping)
	assert.Len(t, preview.Rows, 1, "a row cut off by the preview window is ignored")
	cut, err := svc.PreviewImport(ctx, view.ID, strings.NewReader("input,calls\nfirst,[]\n\"second row, cut"))
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"first", "[]"}}, cut.Rows, "a row cut inside a quoted field is ignored")
	whole, err := svc.PreviewImport(ctx, view.ID, strings.NewReader("input,calls\nfirst,[]\n\"second, whole\",[]"))
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"first", "[]"}, {"second, whole", "[]"}}, whole.Rows, "a last row with its quotes closed is kept")
	lazyPreview, err := svc.PreviewImport(ctx, view.ID, strings.NewReader("input,calls\nfirst,[]\n5\" screen,[]"))
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"first", "[]"}, {`5" screen`, "[]"}}, lazyPreview.Rows, "a bare quote in an unquoted field does not cut the last row")

	// One CSV field mapped twice is refused before anything is written.
	_, err = svc.Import(ctx, view.ID, strings.NewReader(file), ImportOptions{HasHeader: true, Mapping: []ImportColumn{
		{Index: 0, ColumnID: columnID(t, view, "input")},
		{Index: 0, Name: "copy", Type: ColumnVariable},
	}})
	require.True(t, evals.IsInvalid(err), "a field mapped twice: %v", err)
	// A position past the header, or past any CSV's field limit, is refused before a slice of
	// that size is made.
	_, err = svc.Import(ctx, view.ID, strings.NewReader(file), ImportOptions{HasHeader: true, Mapping: []ImportColumn{
		{Index: 4, ColumnID: columnID(t, view, "input")},
	}})
	require.True(t, evals.IsInvalid(err), "a position past the header: %v", err)
	_, err = svc.Import(ctx, view.ID, strings.NewReader("a\n"), ImportOptions{Mapping: []ImportColumn{
		{Index: 1 << 20, ColumnID: columnID(t, view, "input")},
	}})
	require.True(t, evals.IsInvalid(err), "a position past the field limit: %v", err)
	unchanged, err := svc.Get(ctx, view.ID)
	require.NoError(t, err)
	assert.Zero(t, unchanged.RowCount)

	// An explicit mapping: two existing columns, one new column, one field left out.
	result, err := svc.Import(ctx, view.ID, strings.NewReader(file), ImportOptions{HasHeader: true, Actor: "tester", Mapping: []ImportColumn{
		{Index: 0, ColumnID: columnID(t, view, "input")},
		{Index: 1, ColumnID: columnID(t, view, "calls")},
		{Index: 2, Name: "context", Type: ColumnVariable},
	}})
	require.NoError(t, err)
	assert.Equal(t, 2, result.RowsAdded, "the blank line is skipped")
	assert.Equal(t, []string{"context"}, result.ColumnsCreated)

	view, err = svc.Get(ctx, view.ID)
	require.NoError(t, err)
	page, err := svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, page.Rows, 2)
	assert.Equal(t, map[string]string{
		columnID(t, view, "input"): "multi\nline", columnID(t, view, "calls"): `[{"name":"x"}]`, columnID(t, view, "context"): "docs",
	}, page.Rows[0].Cells)

	// A bad row fails the whole import, with its line number, and adds nothing.
	_, err = svc.Import(ctx, view.ID, strings.NewReader("input,calls\nok,[]\nbad,{not json\n"), ImportOptions{HasHeader: true})
	require.Error(t, err)
	assert.True(t, evals.IsInvalid(err))
	assert.Contains(t, err.Error(), "line 3")
	after, err := svc.Get(ctx, view.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, after.RowCount, "all or nothing")
	assert.Equal(t, 1, after.HeadVersion)

	// Unmatched header fields become variable columns when asked.
	created, err := svc.Import(ctx, view.ID, strings.NewReader("input,persona\nhello,admin\n"), ImportOptions{HasHeader: true, CreateColumns: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"persona"}, created.ColumnsCreated)

	_, err = svc.Import(ctx, view.ID, strings.NewReader("a,b\n"), ImportOptions{})
	require.True(t, evals.IsInvalid(err), "no header and no mapping")
	_, err = svc.Import(ctx, view.ID, strings.NewReader("input\n"), ImportOptions{HasHeader: true})
	require.True(t, evals.IsInvalid(err), "no rows")

	// A mapping position past every field gives no value, so no record has a mapped value
	// and nothing is imported, instead of empty rows.
	before, err := svc.Get(ctx, view.ID)
	require.NoError(t, err)
	_, err = svc.Import(ctx, view.ID, strings.NewReader("a,b\nc,d\n"), ImportOptions{Mapping: []ImportColumn{{Index: 2, ColumnID: columnID(t, view, "input")}}})
	require.True(t, evals.IsInvalid(err), "a headerless position past the last field imports no empty rows: %v", err)
	_, err = svc.Import(ctx, view.ID, strings.NewReader("x,y\na,b\n"), ImportOptions{HasHeader: true, Mapping: []ImportColumn{{Index: 5, ColumnID: columnID(t, view, "input")}}})
	require.True(t, evals.IsInvalid(err), "a position past the header's last field is refused: %v", err)
	_, err = svc.Import(ctx, view.ID, strings.NewReader("a,b\n"), ImportOptions{Mapping: []ImportColumn{{Index: 20000, ColumnID: columnID(t, view, "input")}}})
	require.True(t, evals.IsInvalid(err), "a huge position is refused before any slice is sized by it: %v", err)
	after, err = svc.Get(ctx, view.ID)
	require.NoError(t, err)
	assert.Equal(t, before.RowCount, after.RowCount, "no row is added by a refused import")

	// A record whose only mapped value normalizes to an empty cell (an empty file list) is blank.
	files, err := svc.Create(ctx, DatasetInput{Name: "import-files", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "picture", Type: ColumnFile},
	}})
	require.NoError(t, err)
	imported, err := svc.Import(ctx, files.ID, strings.NewReader("picture,input\n[],\nhttps://x/a.png,q\n"), ImportOptions{HasHeader: true})
	require.NoError(t, err)
	assert.Equal(t, 1, imported.RowsAdded, "a record that normalizes to no cells is skipped")
	filePage, err := svc.Rows(ctx, files.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, filePage.Rows, 1)
	assert.Equal(t, "q", filePage.Rows[0].Cells[columnID(t, files, "input")])

	// A file that ends inside a quoted field is cut, so the import fails and adds nothing,
	// also when whole batches were already written before the open quote.
	quoted, err := svc.Create(ctx, DatasetInput{Name: "import-quotes", Columns: []ColumnSpec{{Name: "input", Type: ColumnInput}}})
	require.NoError(t, err)
	_, err = svc.Import(ctx, quoted.ID, strings.NewReader("input\nok\n\"unfinished\nnext\n"), ImportOptions{HasHeader: true})
	require.True(t, evals.IsInvalid(err), "a file ending inside a quoted field: %v", err)
	// The error names the line the open field starts on, as every bad row does.
	_, err = svc.Import(ctx, quoted.ID, strings.NewReader("input\nfirst\n\"second, open"), ImportOptions{HasHeader: true})
	require.True(t, evals.IsInvalid(err), "a file ending inside a quoted field: %v", err)
	assert.Contains(t, err.Error(), "line 3")
	var long strings.Builder
	long.WriteString("input\n")
	for i := range writeBatch + 1 {
		fmt.Fprintf(&long, "row %d\n", i)
	}
	long.WriteString("\"unfinished\nnext\n")
	_, err = svc.Import(ctx, quoted.ID, strings.NewReader(long.String()), ImportOptions{HasHeader: true})
	require.True(t, evals.IsInvalid(err), "a cut file after a full batch: %v", err)
	cutView, err := svc.Get(ctx, quoted.ID)
	require.NoError(t, err)
	assert.Zero(t, cutView.RowCount, "the batches written before the open quote are rolled back")

	// A bare carriage return after a closing quote does not close the field for the parser
	// (only a CR before a line feed does), so the field runs on to the end of the file. The
	// tracker follows that, also when the CR and the next byte arrive in separate reads.
	crRows := strings.TrimSuffix(long.String(), "\"unfinished\nnext\n")
	for _, oneByte := range []bool{false, true} {
		reader := func(text string) io.Reader {
			if oneByte {
				return iotest.OneByteReader(strings.NewReader(text))
			}
			return strings.NewReader(text)
		}
		_, err = svc.Import(ctx, quoted.ID, reader(crRows+"\"x\"\r,y\n"), ImportOptions{HasHeader: true})
		require.True(t, evals.IsInvalid(err), "a CR after a quote keeps the field open (one byte %v): %v", oneByte, err)
		crView, err := svc.Get(ctx, quoted.ID)
		require.NoError(t, err)
		assert.Zero(t, crView.RowCount, "nothing is added (one byte %v)", oneByte)
		for _, closed := range []string{"input\n\"x\"\r\n", "input\n\"x\"\r"} {
			crScratch, err := svc.Create(ctx, DatasetInput{Name: fmt.Sprintf("import-cr-%v-%d", oneByte, len(closed)), Columns: []ColumnSpec{{Name: "input", Type: ColumnInput}}})
			require.NoError(t, err)
			imported, err := svc.Import(ctx, crScratch.ID, reader(closed), ImportOptions{HasHeader: true})
			require.NoError(t, err, "%q imports (one byte %v)", closed, oneByte)
			assert.Equal(t, 1, imported.RowsAdded)
			crPage, err := svc.Rows(ctx, crScratch.ID, RowQuery{})
			require.NoError(t, err)
			require.Len(t, crPage.Rows, 1)
			assert.Equal(t, "x", crPage.Rows[0].Cells[columnID(t, crScratch, "input")])
		}
	}

	// A bare quote inside an unquoted field and a closed multi-line field still import.
	lazy, err := svc.Import(ctx, quoted.ID, strings.NewReader("input\n5\" screen\n"), ImportOptions{HasHeader: true})
	require.NoError(t, err)
	assert.Equal(t, 1, lazy.RowsAdded)
	multi, err := svc.Import(ctx, quoted.ID, strings.NewReader("input\n\"multi\nline\"\n"), ImportOptions{HasHeader: true})
	require.NoError(t, err)
	assert.Equal(t, 1, multi.RowsAdded)
	quotedPage, err := svc.Rows(ctx, quoted.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, quotedPage.Rows, 2)
	assert.Equal(t, `5" screen`, quotedPage.Rows[0].Cells[columnID(t, quoted, "input")])
	assert.Equal(t, "multi\nline", quotedPage.Rows[1].Cells[columnID(t, quoted, "input")])
}

func TestServiceRetypeNormalizesStoredCells(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "retype", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "picture", Type: ColumnVariable},
	}})
	require.NoError(t, err)
	picture := columnID(t, view, "picture")
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "q", "picture": "https://example.com/a.png"}, {"input": "no picture"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "before", "", "tester")
	require.NoError(t, err)

	_, err = svc.UpdateColumn(ctx, view.ID, picture, ColumnSpec{Name: "picture", Type: ColumnFile}, "tester")
	require.NoError(t, err)

	page, err := svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, page.Rows, 2)
	assert.Equal(t, added.Rows[0].RowID, page.Rows[0].RowID)
	assert.JSONEq(t, `[{"name":"a.png","url":"https://example.com/a.png"}]`, page.Rows[0].Cells[picture], "the draft cell is stored in the file shape")
	assert.Equal(t, "q", page.Rows[0].Cells[columnID(t, view, "input")], "the other cells of the row are kept")
	_, has := page.Rows[1].Cells[picture]
	assert.False(t, has, "an empty cell stays empty")

	saved, err := svc.Rows(ctx, view.ID, RowQuery{Version: 1})
	require.NoError(t, err)
	require.Len(t, saved.Rows, 2)
	assert.Equal(t, "https://example.com/a.png", saved.Rows[0].Cells[picture], "a saved version keeps the value it had")
}

// A retype writes each changed row as the scan reaches it, so the request holds one page of
// rows, not every changed row. A later cell that does not fit still rolls the earlier writes back.
func TestServiceRetypeWritesRowsDuringTheScan(t *testing.T) {
	ctx := context.Background()
	const rowCount = 501
	setup := func(t *testing.T, last string) (*Service, *DatasetView, string, *recordingTxStore) {
		svc := newTestService(t, nil)
		view, err := svc.Create(ctx, DatasetInput{Name: "retype", Columns: []ColumnSpec{
			{Name: "input", Type: ColumnInput}, {Name: "picture", Type: ColumnVariable},
		}})
		require.NoError(t, err)
		rows := make([]map[string]string, rowCount)
		for i := range rows {
			rows[i] = map[string]string{"input": fmt.Sprint(i), "picture": fmt.Sprintf("https://example.com/%d.png", i)}
		}
		rows[rowCount-1]["picture"] = last
		_, err = svc.AddRows(ctx, view.ID, rows, "", "tester")
		require.NoError(t, err)
		recorder := &recordingTxStore{Store: svc.store}
		svc.store = recorder
		return svc, view, columnID(t, view, "picture"), recorder
	}

	t.Run("writes before the scan ends", func(t *testing.T) {
		svc, view, picture, recorder := setup(t, "https://example.com/last.png")
		_, err := svc.UpdateColumn(ctx, view.ID, picture, ColumnSpec{Name: "picture", Type: ColumnFile}, "tester")
		require.NoError(t, err)
		firstWrite := slices.Index(recorder.events, "set")
		require.GreaterOrEqual(t, firstWrite, 0, "the URL cells are rewritten")
		callbacksBefore := 0
		for _, event := range recorder.events[:firstWrite] {
			if event == "scan" {
				callbacksBefore++
			}
		}
		assert.Less(t, callbacksBefore, rowCount, "the first row is written before the last row is scanned")
	})

	t.Run("a bad cell rolls back the rows already written", func(t *testing.T) {
		svc, view, picture, _ := setup(t, "not a URL")
		_, err := svc.UpdateColumn(ctx, view.ID, picture, ColumnSpec{Name: "picture", Type: ColumnFile}, "tester")
		require.True(t, evals.IsInvalid(err), "got %v", err)
		page, err := svc.Rows(ctx, view.ID, RowQuery{Limit: 10})
		require.NoError(t, err)
		require.NotEmpty(t, page.Rows)
		for _, row := range page.Rows {
			assert.True(t, strings.HasPrefix(row.Cells[picture], "https://example.com/"), "row keeps its original text, got %q", row.Cells[picture])
		}
	})
}

// recordingTxStore records, in order, every scan callback and row write of a dataset transaction.
type recordingTxStore struct {
	Store
	events []string
}

func (s *recordingTxStore) WithEvalDataset(ctx context.Context, datasetID string, fn func(tx configstore.EvalDatasetTx) error) error {
	return s.Store.WithEvalDataset(ctx, datasetID, func(tx configstore.EvalDatasetTx) error {
		return fn(recordingTx{EvalDatasetTx: tx, store: s})
	})
}

type recordingTx struct {
	configstore.EvalDatasetTx
	store *recordingTxStore
}

func (r recordingTx) ScanDraftCells(fn func(rowID string, cells map[string]string) error) error {
	return r.EvalDatasetTx.ScanDraftCells(func(rowID string, cells map[string]string) error {
		r.store.events = append(r.store.events, "scan")
		return fn(rowID, cells)
	})
}

func (r recordingTx) SetRowCells(rowID, cellsJSON string) (*tables.TableEvalDatasetRow, error) {
	r.store.events = append(r.store.events, "set")
	return r.EvalDatasetTx.SetRowCells(rowID, cellsJSON)
}

func TestServiceExportRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "export", Template: "qa"})
	require.NoError(t, err)
	file := "input,expected_output\n\"a, \"\"quoted\"\"\",one\n\"two\nlines\",\n"
	_, err = svc.Import(ctx, view.ID, strings.NewReader(file), ImportOptions{HasHeader: true})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, svc.Export(ctx, view.ID, &out, FormatCSV, 0, ""))
	assert.Equal(t, file, out.String(), "a CSV survives import and export unchanged")

	// Version 1 still exports the same after the draft changes.
	page, err := svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	_, err = svc.DeleteRows(ctx, view.ID, []string{page.Rows[0].RowID}, "tester")
	require.NoError(t, err)
	out.Reset()
	require.NoError(t, svc.Export(ctx, view.ID, &out, FormatCSV, 1, ""))
	assert.Equal(t, file, out.String())
	out.Reset()
	require.NoError(t, svc.Export(ctx, view.ID, &out, FormatJSONL, 0, ""))
	assert.JSONEq(t, `{"input":"two\nlines","expected_output":""}`, strings.TrimSpace(out.String()))

	require.ErrorIs(t, svc.Export(ctx, view.ID, io.Discard, FormatCSV, 9, ""), configstore.ErrNotFound)

	// A source that goes away after the first page fails the export, so a short file is not
	// passed off as complete.
	var many strings.Builder
	many.WriteString("input\n")
	for i := range writeBatch + 1 {
		fmt.Fprintf(&many, "q%d\n", i)
	}
	real := svc.store
	t.Run("dataset deleted mid-export", func(t *testing.T) {
		gone, err := svc.Create(ctx, DatasetInput{Name: "deleted mid-export", Template: "qa"})
		require.NoError(t, err)
		_, err = svc.Import(ctx, gone.ID, strings.NewReader(many.String()), ImportOptions{HasHeader: true})
		require.NoError(t, err)
		svc.store = &firstPageHookStore{Store: real, hook: func() error { return svc.Delete(ctx, gone.ID) }}
		defer func() { svc.store = real }()
		require.ErrorIs(t, svc.Export(ctx, gone.ID, io.Discard, FormatCSV, 1, ""), configstore.ErrNotFound)
	})
	t.Run("version pruned mid-export", func(t *testing.T) {
		pruned, err := svc.Create(ctx, DatasetInput{Name: "pruned mid-export", Template: "qa", MaxVersions: new(1)})
		require.NoError(t, err)
		_, err = svc.Import(ctx, pruned.ID, strings.NewReader(many.String()), ImportOptions{HasHeader: true})
		require.NoError(t, err)
		svc.store = &firstPageHookStore{Store: real, hook: func() error {
			if _, err := svc.AddRows(ctx, pruned.ID, []map[string]string{{"input": "later"}}, "", "tester"); err != nil {
				return err
			}
			_, err := svc.SaveVersion(ctx, pruned.ID, "v2", "", "tester")
			return err
		}}
		defer func() { svc.store = real }()
		require.ErrorIs(t, svc.Export(ctx, pruned.ID, io.Discard, FormatCSV, 1, ""), configstore.ErrNotFound)
	})

	// A cell a spreadsheet would run as a formula is written as text in CSV; JSONL keeps it.
	formula, err := svc.Create(ctx, DatasetInput{Name: "formula", Template: "qa"})
	require.NoError(t, err)
	_, err = svc.AddRows(ctx, formula.ID, []map[string]string{{"input": "=1+1", "expected_output": "-2"}}, "", "tester")
	require.NoError(t, err)
	out.Reset()
	require.NoError(t, svc.Export(ctx, formula.ID, &out, FormatCSV, 0, ""))
	assert.Equal(t, "input,expected_output\n'=1+1,'-2\n", out.String())
	out.Reset()
	require.NoError(t, svc.Export(ctx, formula.ID, &out, FormatJSONL, 0, ""))
	assert.JSONEq(t, `{"input":"=1+1","expected_output":"-2"}`, strings.TrimSpace(out.String()))

	// A header escaped on export maps back to its column on import, and a column created from
	// it takes the name without the quote.
	dashed, err := svc.Create(ctx, DatasetInput{Name: "dashed", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "-id", Type: ColumnVariable},
	}})
	require.NoError(t, err)
	_, err = svc.AddRows(ctx, dashed.ID, []map[string]string{{"input": "q", "-id": "x"}}, "", "tester")
	require.NoError(t, err)
	out.Reset()
	require.NoError(t, svc.Export(ctx, dashed.ID, &out, FormatCSV, 0, ""))
	exported := out.String()
	require.True(t, strings.HasPrefix(exported, "input,'-id\n"), exported)
	dashedCopy, err := svc.Create(ctx, DatasetInput{Name: "dashed copy", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "-id", Type: ColumnVariable},
	}})
	require.NoError(t, err)
	dashedPreview, err := svc.PreviewImport(ctx, dashedCopy.ID, strings.NewReader(exported))
	require.NoError(t, err)
	assert.Equal(t, []string{columnID(t, dashedCopy, "input"), columnID(t, dashedCopy, "-id")}, dashedPreview.Mapping)
	dashedImport, err := svc.Import(ctx, dashedCopy.ID, strings.NewReader(exported), ImportOptions{HasHeader: true, CreateColumns: true})
	require.NoError(t, err)
	assert.Empty(t, dashedImport.ColumnsCreated, "the escaped header maps to -id")
	dashedPage, err := svc.Rows(ctx, dashedCopy.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, dashedPage.Rows, 1)
	assert.Equal(t, "x", dashedPage.Rows[0].Cells[columnID(t, dashedCopy, "-id")])
	bare, err := svc.Create(ctx, DatasetInput{Name: "bare", Columns: []ColumnSpec{{Name: "input", Type: ColumnInput}}})
	require.NoError(t, err)
	bareImport, err := svc.Import(ctx, bare.ID, strings.NewReader(exported), ImportOptions{HasHeader: true, CreateColumns: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"-id"}, bareImport.ColumnsCreated, "a created column drops the escape quote")

	// A structured cell typed with line breaks is written compact, so each row stays one line.
	pretty, err := svc.Create(ctx, DatasetInput{Name: "pretty", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "calls", Type: ColumnExpectedToolCalls},
	}})
	require.NoError(t, err)
	_, err = svc.AddRows(ctx, pretty.ID, []map[string]string{
		{"input": "a", "calls": "[\n  {\"name\": \"a\", \"arguments\": {}}\n]"}, {"input": "b"},
	}, "", "tester")
	require.NoError(t, err)
	out.Reset()
	require.NoError(t, svc.Export(ctx, pretty.ID, &out, FormatJSONL, 0, ""))
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 2, "one line per row: %q", out.String())
	var first struct {
		Calls []map[string]any `json:"calls"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.Len(t, first.Calls, 1)
	assert.Equal(t, "a", first.Calls[0]["name"])

	// A structured cell stored with leading JSON whitespace is exported without it, so the
	// CSV cell is not quoted as a formula and imports back as the same JSON.
	spaced, err := svc.Create(ctx, DatasetInput{Name: "spaced", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "calls", Type: ColumnExpectedToolCalls},
	}})
	require.NoError(t, err)
	_, err = svc.AddRows(ctx, spaced.ID, []map[string]string{{"input": "a", "calls": "\t[]"}}, "", "tester")
	require.NoError(t, err)
	out.Reset()
	require.NoError(t, svc.Export(ctx, spaced.ID, &out, FormatCSV, 0, ""))
	spacedCopy, err := svc.Create(ctx, DatasetInput{Name: "spaced copy", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "calls", Type: ColumnExpectedToolCalls},
	}})
	require.NoError(t, err)
	_, err = svc.Import(ctx, spacedCopy.ID, strings.NewReader(out.String()), ImportOptions{HasHeader: true})
	require.NoError(t, err, "our own CSV export imports: %q", out.String())
	spacedPage, err := svc.Rows(ctx, spacedCopy.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, spacedPage.Rows, 1)
	assert.Equal(t, "[]", spacedPage.Rows[0].Cells[columnID(t, spacedCopy, "calls")])

	// A draft export reads the columns once and the rows page by page, so a column change
	// between pages would write cells of the new shape under the old type. It fails instead.
	t.Run("draft columns changed mid-export", func(t *testing.T) {
		moving, err := svc.Create(ctx, DatasetInput{Name: "retyped mid-export", Columns: []ColumnSpec{
			{Name: "input", Type: ColumnInput}, {Name: "picture", Type: ColumnVariable},
		}})
		require.NoError(t, err)
		rows := make([]map[string]string, writeBatch+1)
		for i := range rows {
			rows[i] = map[string]string{"input": fmt.Sprint(i), "picture": "https://example.com/a.png"}
		}
		_, err = svc.AddRows(ctx, moving.ID, rows, "", "tester")
		require.NoError(t, err)
		picture := columnID(t, moving, "picture")
		svc.store = &firstPageHookStore{Store: real, hook: func() error {
			_, err := svc.UpdateColumn(ctx, moving.ID, picture, ColumnSpec{Name: "picture", Type: ColumnFile}, "tester")
			return err
		}}
		defer func() { svc.store = real }()
		require.ErrorIs(t, svc.Export(ctx, moving.ID, io.Discard, FormatCSV, 0, ""), ErrEvalDatasetChangedDuringExport)
	})

	// A split export pages over the split's current members, so a member replaced between
	// pages would export a membership that never existed. It fails instead.
	t.Run("split members changed mid-export", func(t *testing.T) {
		members, err := svc.Create(ctx, DatasetInput{Name: "split mid-export", Template: "qa"})
		require.NoError(t, err)
		rows := make([]map[string]string, writeBatch+2)
		for i := range rows {
			rows[i] = map[string]string{"input": fmt.Sprint(i)}
		}
		added, err := svc.AddRows(ctx, members.ID, rows, "", "tester")
		require.NoError(t, err)
		split, err := svc.CreateSplit(ctx, members.ID, SplitInput{Name: "most"})
		require.NoError(t, err)
		ids := make([]string, 0, writeBatch+1)
		for _, row := range added.Rows[:writeBatch+1] {
			ids = append(ids, row.RowID)
		}
		_, _, err = svc.SetSplitRows(ctx, members.ID, split.ID, ids, nil)
		require.NoError(t, err)
		svc.store = &firstPageHookStore{Store: real, hook: func() error {
			// Take out the first page's last row, already written, and add a later row.
			_, _, err := svc.SetSplitRows(ctx, members.ID, split.ID,
				[]string{added.Rows[writeBatch+1].RowID}, []string{added.Rows[writeBatch-1].RowID})
			return err
		}}
		defer func() { svc.store = real }()
		require.ErrorIs(t, svc.Export(ctx, members.ID, io.Discard, FormatCSV, 0, split.ID), ErrEvalDatasetChangedDuringExport)
	})
}

func TestServiceAddFromLogsAndSessions(t *testing.T) {
	ctx := context.Background()
	logs := &fakeLogs{logs: map[string]*logstore.Log{}, sessions: map[string][]string{}}
	logs.logs["log-1"] = chatLog("log-1", []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "weather?")}, "Sunny.")
	logs.logs["empty"] = &logstore.Log{ID: "empty"}
	// A session longer than one listing page.
	var history []schemas.ChatMessage
	for i := range 130 {
		id := fmt.Sprintf("turn-%d", i)
		history = append(history, chatMessage(schemas.ChatMessageRoleUser, fmt.Sprintf("request %d", i)))
		logs.logs[id] = chatLog(id, append([]schemas.ChatMessage(nil), history...), fmt.Sprintf("reply %d", i))
		history = append(history, chatMessage(schemas.ChatMessageRoleAssistant, fmt.Sprintf("reply %d", i)))
		logs.sessions["sess-1"] = append(logs.sessions["sess-1"], id)
	}
	logs.sessions["sess-empty"] = []string{"empty"}

	without := newTestService(t, nil)
	bare, err := without.Create(ctx, DatasetInput{Name: "no-logs", Template: "qa"})
	require.NoError(t, err)
	_, err = without.AddFromLogs(ctx, bare.ID, []string{"log-1"}, "", "tester")
	require.ErrorIs(t, err, ErrNoLogStore)

	svc := newTestService(t, logs)
	view, err := svc.Create(ctx, DatasetInput{Name: "curated", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "output", Type: ColumnOutput},
	}})
	require.NoError(t, err)
	split, err := svc.CreateSplit(ctx, view.ID, SplitInput{Name: "from-prod"})
	require.NoError(t, err)

	fromLogs, err := svc.AddFromLogs(ctx, view.ID, []string{"log-1", "empty", "missing", "log-1"}, split.ID, "tester")
	require.NoError(t, err)
	assert.Equal(t, 1, fromLogs.RowsAdded, "a repeated ID is added once")
	require.NotNil(t, fromLogs.Version)
	assert.Equal(t, 1, *fromLogs.Version)
	require.Len(t, fromLogs.Skipped, 2)
	assert.Equal(t, "missing", fromLogs.Skipped[0].ID)
	assert.Equal(t, "empty", fromLogs.Skipped[1].ID)

	fromSessions, err := svc.AddFromSessions(ctx, view.ID, []string{"sess-1", "sess-empty", "sess-none"}, "", "tester")
	require.NoError(t, err)
	assert.Equal(t, 1, fromSessions.RowsAdded)
	require.Len(t, fromSessions.Skipped, 2)
	require.NotNil(t, fromSessions.Version)
	assert.Equal(t, 2, *fromSessions.Version)

	view, err = svc.Get(ctx, view.ID)
	require.NoError(t, err)
	require.Len(t, view.Columns, 3, "the session column is created")
	assert.EqualValues(t, 1, view.Splits[0].RowCount)

	page, err := svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, page.Rows, 2)
	assert.Equal(t, "weather?", page.Rows[0].Cells[columnID(t, view, "input")])
	variables, err := svc.RowVariables(ctx, view.ID, page.Rows[1].RowID, 0)
	require.NoError(t, err)
	assert.Equal(t, "request 0", variables["input"])
	assert.Equal(t, "reply 129", variables["output"])
	assert.Contains(t, variables["session"], `{"turn":129,"request":"request 129","response":"reply 129"}`, "every page of the session is read")

	// Log text is not typed by the user: a NUL character in it is dropped, not refused.
	logs.logs["nul"] = chatLog("nul", []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "a\x00b")}, "c\x00d")
	withNUL, err := svc.AddFromLogs(ctx, view.ID, []string{"nul"}, "", "tester")
	require.NoError(t, err)
	require.Equal(t, 1, withNUL.RowsAdded)
	page, err = svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	last := page.Rows[len(page.Rows)-1]
	assert.Equal(t, "ab", last.Cells[columnID(t, view, "input")])
	assert.Equal(t, "cd", last.Cells[columnID(t, view, "output")])

	// A logged call whose arguments are not a JSON object cannot be an expected tool call, so
	// a dataset that copies tool calls skips that log and names the call.
	logs.logs["broken-call"] = chatLog("broken-call", []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "weather?")}, "",
		schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: new("broken"), Arguments: `{"city":`}})
	calls, err := svc.Create(ctx, DatasetInput{Name: "calls", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "calls", Type: ColumnExpectedToolCalls},
	}})
	require.NoError(t, err)
	brokenCall, err := svc.AddFromLogs(ctx, calls.ID, []string{"broken-call"}, "", "tester")
	require.NoError(t, err)
	assert.Zero(t, brokenCall.RowsAdded)
	require.Len(t, brokenCall.Skipped, 1)
	assert.Equal(t, "broken-call", brokenCall.Skipped[0].ID)
	assert.Contains(t, brokenCall.Skipped[0].Reason, `"broken"`)

	// A curation call that adds nothing leaves the draft and the history alone: the small edit
	// waiting in the draft is not saved on its own.
	_, err = svc.UpdateRow(ctx, view.ID, page.Rows[0].RowID, map[string]string{"input": "edited"}, "tester")
	require.NoError(t, err)
	before, err := svc.Versions(ctx, view.ID)
	require.NoError(t, err)
	nothing, err := svc.AddFromLogs(ctx, view.ID, []string{"missing"}, "", "tester")
	require.NoError(t, err)
	assert.Zero(t, nothing.RowsAdded)
	assert.Nil(t, nothing.Version)
	none, err := svc.AddFromSessions(ctx, view.ID, []string{"sess-none"}, "", "tester")
	require.NoError(t, err)
	assert.Zero(t, none.RowsAdded)
	assert.Nil(t, none.Version)
	after, err := svc.Versions(ctx, view.ID)
	require.NoError(t, err)
	assert.Len(t, after, len(before), "no version is saved for a call that adds nothing")

	// A log store that fails is not a missing log: the call fails and adds nothing.
	logs.errs = map[string]error{"slow": context.DeadlineExceeded}
	_, err = svc.AddFromLogs(ctx, view.ID, []string{"log-1", "slow"}, "", "tester")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	logs.errs = nil
	rowsBefore := page.Total
	page, err = svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	assert.Equal(t, rowsBefore, page.Total, "nothing is added when a lookup fails")

	// A session longer than the turn limit is skipped and reported, not cut short.
	for i := range maxSessionLogs + 1 {
		id := fmt.Sprintf("long-%d", i)
		logs.logs[id] = chatLog(id, []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "q")}, "a")
		logs.sessions["sess-long"] = append(logs.sessions["sess-long"], id)
	}
	long, err := svc.AddFromSessions(ctx, view.ID, []string{"sess-long"}, "", "tester")
	require.NoError(t, err)
	assert.Zero(t, long.RowsAdded)
	require.Len(t, long.Skipped, 1)
	assert.Equal(t, fmt.Sprintf("the session has more than %d requests", maxSessionLogs), long.Skipped[0].Reason)

	// A session over the limit that gains a page of requests while it is read repeats IDs
	// across the shifted pages. It is still too long, not saved without its oldest request.
	for i := range maxSessionLogs + 1 {
		id := fmt.Sprintf("growing-%d", i)
		logs.logs[id] = chatLog(id, []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "q")}, "a")
		logs.sessions["sess-growing"] = append(logs.sessions["sess-growing"], id)
	}
	logs.listed = func() {
		logs.listed = nil
		for i := range 100 {
			id := fmt.Sprintf("growing-new-%d", i)
			logs.logs[id] = chatLog(id, []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "q")}, "a")
			logs.sessions["sess-growing"] = append(logs.sessions["sess-growing"], id)
		}
	}
	growing, err := svc.AddFromSessions(ctx, view.ID, []string{"sess-growing"}, "", "tester")
	logs.listed = nil
	require.NoError(t, err)
	assert.Zero(t, growing.RowsAdded, "the session is not saved short")
	require.Len(t, growing.Skipped, 1)
	assert.Equal(t, fmt.Sprintf("the session has more than %d requests", maxSessionLogs), growing.Skipped[0].Reason)

	// A turn that fails to read is not a missing turn: the session is skipped, not saved short.
	logs.logs["broken-1"] = chatLog("broken-1", []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "q1")}, "a1")
	logs.logs["broken-2"] = chatLog("broken-2", []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "q2")}, "a2")
	logs.sessions["sess-broken"] = []string{"broken-1", "broken-2"}
	logs.errs = map[string]error{"broken-2": errors.New("connection refused")}
	rowsBefore = page.Total
	broken, err := svc.AddFromSessions(ctx, view.ID, []string{"sess-broken"}, "", "tester")
	logs.errs = nil
	require.NoError(t, err)
	assert.Zero(t, broken.RowsAdded)
	require.Len(t, broken.Skipped, 1)
	assert.Equal(t, "sess-broken", broken.Skipped[0].ID)
	assert.Equal(t, "a request of the session could not be read", broken.Skipped[0].Reason)
	page, err = svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	assert.Equal(t, rowsBefore, page.Total, "no partial transcript is saved")

	// Retention deletes the oldest request of a session while it is read. The requests after
	// it are still listed, and the session is saved with the ones that remain.
	for i := 1; i <= 101; i++ {
		id := fmt.Sprintf("kept-%d", i)
		logs.logs[id] = chatLog(id, []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, fmt.Sprintf("r-%d", i))}, fmt.Sprintf("a-%d", i))
		logs.sessions["sess-retention"] = append(logs.sessions["sess-retention"], id)
	}
	logs.listed = func() {
		logs.listed = nil
		delete(logs.logs, "kept-1")
		logs.sessions["sess-retention"] = logs.sessions["sess-retention"][1:]
	}
	retained, err := svc.Create(ctx, DatasetInput{Name: "retention", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "output", Type: ColumnOutput},
	}})
	require.NoError(t, err)
	fromRetained, err := svc.AddFromSessions(ctx, retained.ID, []string{"sess-retention"}, "", "tester")
	logs.listed = nil
	require.NoError(t, err)
	require.Equal(t, 1, fromRetained.RowsAdded)
	assert.Empty(t, fromRetained.Skipped)
	page, err = svc.Rows(ctx, retained.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	variables, err = svc.RowVariables(ctx, retained.ID, page.Rows[0].RowID, 0)
	require.NoError(t, err)
	assert.Equal(t, "r-2", variables["input"], "the first remaining request fills the input")
	assert.Equal(t, "a-101", variables["output"], "the newest request is not lost to the deleted one")
	assert.NotContains(t, variables["session"], `"request":"r-1"`)
	assert.Contains(t, variables["session"], `"request":"r-101"`)

	// The newest request is deleted after the first page is read. Pages follow the keyset
	// cursor, so the deletion shifts nothing and the oldest request is still read.
	for i := 1; i <= 101; i++ {
		id := fmt.Sprintf("newest-gone-%d", i)
		logs.logs[id] = chatLog(id, []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, fmt.Sprintf("n-%d", i))}, fmt.Sprintf("m-%d", i))
		logs.sessions["sess-newest-gone"] = append(logs.sessions["sess-newest-gone"], id)
	}
	logs.listed = func() {
		logs.listed = nil
		delete(logs.logs, "newest-gone-101")
		ids := logs.sessions["sess-newest-gone"]
		logs.sessions["sess-newest-gone"] = ids[:len(ids)-1]
	}
	newestGone, err := svc.Create(ctx, DatasetInput{Name: "newest-gone", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "output", Type: ColumnOutput},
	}})
	require.NoError(t, err)
	fromNewestGone, err := svc.AddFromSessions(ctx, newestGone.ID, []string{"sess-newest-gone"}, "", "tester")
	logs.listed = nil
	require.NoError(t, err)
	require.Equal(t, 1, fromNewestGone.RowsAdded)
	page, err = svc.Rows(ctx, newestGone.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	variables, err = svc.RowVariables(ctx, newestGone.ID, page.Rows[0].RowID, 0)
	require.NoError(t, err)
	assert.Equal(t, "n-1", variables["input"], "the oldest request is read")
	assert.Equal(t, "m-100", variables["output"])
	assert.Equal(t, 100, strings.Count(variables["session"], `"request":`), "every surviving request is a turn")

	// A request whose content cannot be read is a failed read, not a request without content:
	// the session is skipped as unreadable, and the log is listed with the read reason.
	logs.logs["strict-1"] = chatLog("strict-1", []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "s1")}, "t1")
	logs.logs["strict-2"] = chatLog("strict-2", []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, "s2")}, "t2")
	logs.sessions["sess-strict"] = []string{"strict-1", "strict-2"}
	strict := &strictFakeLogs{fakeLogs: logs, unreadable: map[string]bool{"strict-2": true}}
	strictSvc := newTestService(t, strict)
	strictView, err := strictSvc.Create(ctx, DatasetInput{Name: "strict", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "output", Type: ColumnOutput},
	}})
	require.NoError(t, err)
	strictSessions, err := strictSvc.AddFromSessions(ctx, strictView.ID, []string{"sess-strict"}, "", "tester")
	require.NoError(t, err)
	assert.Zero(t, strictSessions.RowsAdded, "the session is not saved short")
	require.Len(t, strictSessions.Skipped, 1)
	assert.Equal(t, "a request of the session could not be read", strictSessions.Skipped[0].Reason)
	strictLogs, err := strictSvc.AddFromLogs(ctx, strictView.ID, []string{"strict-1", "strict-2"}, "", "tester")
	require.NoError(t, err)
	assert.Equal(t, 1, strictLogs.RowsAdded)
	require.Len(t, strictLogs.Skipped, 1)
	assert.Equal(t, Skipped{ID: "strict-2", Reason: "the log's content could not be read"}, strictLogs.Skipped[0])

	// The text of every session in one call is bounded: past the budget the call fails and
	// adds nothing, rather than holding it all before the write.
	saved := maxCurateBytes
	maxCurateBytes = 1000
	t.Cleanup(func() { maxCurateBytes = saved })
	for _, id := range []string{"big-a", "big-b"} {
		logs.logs[id] = chatLog(id, []schemas.ChatMessage{chatMessage(schemas.ChatMessageRoleUser, strings.Repeat("q", 600))}, "a")
		logs.sessions["sess-"+id] = []string{id}
	}
	single, err := svc.AddFromSessions(ctx, retained.ID, []string{"sess-big-a"}, "", "tester")
	require.NoError(t, err, "one session fits the budget")
	require.Equal(t, 1, single.RowsAdded)
	page, err = svc.Rows(ctx, retained.ID, RowQuery{})
	require.NoError(t, err)
	rowsBefore = page.Total
	_, err = svc.AddFromSessions(ctx, retained.ID, []string{"sess-big-a", "sess-big-b"}, "", "tester")
	require.True(t, evals.IsInvalid(err), "two sessions over the budget: %v", err)
	page, err = svc.Rows(ctx, retained.ID, RowQuery{})
	require.NoError(t, err)
	assert.Equal(t, rowsBefore, page.Total, "nothing is added past the budget")

	// The logs one AddFromLogs call reads share the same budget: past it the call fails
	// before anything is written, and no version is saved.
	single, err = svc.AddFromLogs(ctx, retained.ID, []string{"big-a"}, "", "tester")
	require.NoError(t, err, "one log fits the budget")
	require.Equal(t, 1, single.RowsAdded)
	page, err = svc.Rows(ctx, retained.ID, RowQuery{})
	require.NoError(t, err)
	rowsBefore = page.Total
	versionsBefore, err := svc.Versions(ctx, retained.ID)
	require.NoError(t, err)
	fromBig, err := svc.AddFromLogs(ctx, retained.ID, []string{"big-a", "big-b"}, "", "tester")
	require.True(t, evals.IsInvalid(err), "two logs over the budget: %v, %+v", err, fromBig)
	page, err = svc.Rows(ctx, retained.ID, RowQuery{})
	require.NoError(t, err)
	assert.Equal(t, rowsBefore, page.Total, "no log is added past the budget")
	versionsAfter, err := svc.Versions(ctx, retained.ID)
	require.NoError(t, err)
	assert.Len(t, versionsAfter, len(versionsBefore), "no version is saved past the budget")
}

// strictFakeLogs is a log store with a strict read, as the hybrid store has: an object
// read that fails comes back from FindByID as the metadata-only row, and from
// FindByIDStrict as an error.
type strictFakeLogs struct {
	*fakeLogs
	unreadable map[string]bool
}

func (f *strictFakeLogs) FindByID(ctx context.Context, id string) (*logstore.Log, error) {
	if f.unreadable[id] {
		return &logstore.Log{ID: id}, nil
	}
	return f.fakeLogs.FindByID(ctx, id)
}

func (f *strictFakeLogs) FindByIDStrict(ctx context.Context, id string) (*logstore.Log, error) {
	if f.unreadable[id] {
		return nil, fmt.Errorf("%w: object fetch failed", logstore.ErrPayloadUnreadable)
	}
	return f.fakeLogs.FindByID(ctx, id)
}

// A restore with unsaved edits saves them first. Under a version cap that save prunes
// the oldest version, which may be the very version being restored. The pre-save must
// not prune; the restore's own save then applies the cap.
func TestServiceRestoreUnderTheVersionCap(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "capped", Template: "qa", MaxVersions: new(2)})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "a"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v1", "", "tester")
	require.NoError(t, err)
	_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": "b"}, "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v2", "", "tester")
	require.NoError(t, err)
	_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": "c"}, "tester")
	require.NoError(t, err)

	restored, err := svc.Restore(ctx, view.ID, 1, "tester")
	require.NoError(t, err, "the pre-save must not prune the version being restored")
	require.NotNil(t, restored.RestoredFrom)
	assert.Equal(t, 1, *restored.RestoredFrom)
	page, err := svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	assert.Equal(t, "a", page.Rows[0].Cells[columnID(t, view, "input")])
	versions, err := svc.Versions(ctx, view.ID)
	require.NoError(t, err)
	assert.Len(t, versions, 2, "the restore's own save keeps the cap")

	// Restoring the head again, with nothing in the draft, saves no version.
	head := versions[0].Number
	for _, v := range versions {
		head = max(head, v.Number)
	}
	again, err := svc.Restore(ctx, view.ID, head, "tester")
	require.NoError(t, err)
	assert.Equal(t, head, again.Number)
	after, err := svc.Versions(ctx, view.ID)
	require.NoError(t, err)
	assert.Len(t, after, 2, "nothing new was saved")

	// A version a run reads is stepped over, and the next oldest goes instead, so the
	// count still comes down to the cap.
	pinned, err := svc.Create(ctx, DatasetInput{Name: "pinned", Template: "qa"})
	require.NoError(t, err)
	row, err := svc.AddRows(ctx, pinned.ID, []map[string]string{{"input": "a"}}, "", "tester")
	require.NoError(t, err)
	for i, value := range []string{"", "b", "c"} {
		if value != "" {
			_, err = svc.UpdateRow(ctx, pinned.ID, row.Rows[0].RowID, map[string]string{"input": value}, "tester")
			require.NoError(t, err)
		}
		_, err = svc.SaveVersion(ctx, pinned.ID, fmt.Sprintf("v%d", i+1), "", "tester")
		require.NoError(t, err)
	}
	db := svc.store.(*configstore.RDBConfigStore).DB()
	require.NoError(t, db.Create(&tables.TableEvalRun{
		ID: "pinning-run", Name: "pins v1", Status: "completed", TargetType: "dataset",
		DatasetID: pinned.ID, DatasetVersion: 1,
	}).Error)
	_, err = svc.Update(ctx, pinned.ID, DatasetInput{Name: "pinned", MaxVersions: new(2)})
	require.NoError(t, err)
	kept, err := svc.Versions(ctx, pinned.ID)
	require.NoError(t, err)
	numbers := make([]int, len(kept))
	for i := range kept {
		numbers[i] = kept[i].Number
	}
	assert.ElementsMatch(t, []int{1, 3}, numbers, "the pinned v1 stays and v2 goes in its place")
}

// The target is checked against the head before the waiting edits are saved. Saving
// them first would create the version the request names, and the restore would then
// succeed for a version that did not exist when the request arrived.
func TestServiceRestoreRefusesAVersionAboveTheHead(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "above-head", Template: "qa"})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "a"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v1", "", "tester")
	require.NoError(t, err)
	_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": "b"}, "tester")
	require.NoError(t, err)

	_, err = svc.Restore(ctx, view.ID, 2, "tester")
	require.ErrorIs(t, err, configstore.ErrNotFound)
	versions, err := svc.Versions(ctx, view.ID)
	require.NoError(t, err)
	assert.Len(t, versions, 1, "the waiting edits were not saved")
	got, err := svc.Get(ctx, view.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.HeadVersion)
	assert.NotZero(t, got.DraftChanges, "the draft still holds its edits")
}

// The draft is named symbolically, so Diff resolves it from the same read it compares
// against. A caller that worked out HeadVersion+1 itself could compare a version saved
// in between instead, and drop the edits made since.
func TestServiceDiffResolvesTheDraftItself(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "draft-diff", Template: "qa"})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "a"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v1", "", "tester")
	require.NoError(t, err)
	_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": "b"}, "tester")
	require.NoError(t, err)

	diff, err := svc.Diff(ctx, view.ID, DiffDraft, nil, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, diff.To, "the draft is the version after the head")
	assert.Equal(t, 1, diff.From)
	require.Len(t, diff.Rows, 1)
	assert.Equal(t, "b", diff.Rows[0].After[columnID(t, view, "input")])

	// A row edited and edited back is no change, and it does not use up a page: a page of one
	// still holds the next real change.
	more, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "c"}, {"input": "d"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v2", "", "tester")
	require.NoError(t, err)
	for _, value := range []string{"c2", "c"} {
		_, err = svc.UpdateRow(ctx, view.ID, more.Rows[0].RowID, map[string]string{"input": value}, "tester")
		require.NoError(t, err)
	}
	_, err = svc.UpdateRow(ctx, view.ID, more.Rows[1].RowID, map[string]string{"input": "d2"}, "tester")
	require.NoError(t, err)
	paged, err := svc.Diff(ctx, view.ID, DiffDraft, nil, 0, 1)
	require.NoError(t, err)
	require.Len(t, paged.Rows, 1, "the page skips the unchanged row and holds the next change")
	assert.Equal(t, more.Rows[1].RowID, paged.Rows[0].RowID)
	assert.Zero(t, paged.NextAfter, "no change follows")
}

// A record written after a column was deleted no longer holds that column's cell. It is
// still the same row to the versions that do not have the column, so it is no change.
func TestServiceDiffIgnoresADeletedColumnsCell(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "dropped-column", Columns: []ColumnSpec{{Name: "a", Type: ColumnVariable}, {Name: "b", Type: ColumnVariable}}})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"a": "1", "b": "2"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v1", "", "tester")
	require.NoError(t, err)
	require.NoError(t, svc.DeleteColumn(ctx, view.ID, columnID(t, view, "b"), "tester"))
	_, err = svc.SaveVersion(ctx, view.ID, "v2", "", "tester")
	require.NoError(t, err)
	_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"a": "1"}, "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v3", "", "tester")
	require.NoError(t, err)

	diff, err := svc.Diff(ctx, view.ID, 3, new(2), 0, 0)
	require.NoError(t, err)
	assert.Empty(t, diff.Rows, "the row reads the same in versions 2 and 3")
}

// columnsSavingStore runs hook once, before the first column listing is read, as a save
// and a column delete landing between a draft diff's head read and its column read would.
type columnsSavingStore struct {
	Store
	hook func() error
	done bool
}

func (s *columnsSavingStore) ListEvalDatasetColumns(ctx context.Context, datasetID string) ([]tables.TableEvalDatasetColumn, error) {
	if !s.done {
		s.done = true
		if err := s.hook(); err != nil {
			return nil, err
		}
	}
	return s.Store.ListEvalDatasetColumns(ctx, datasetID)
}

// A draft diff whose draft is saved, and whose new draft drops a column, between the head
// read and the column read reads the saved version's columns, so the saved change shows.
func TestServiceDiffOfADraftSavedMidRead(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "saved-mid-diff", Columns: []ColumnSpec{{Name: "input", Type: ColumnInput}, {Name: "notes", Type: ColumnVariable}}})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "q", "notes": "old"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v1", "", "tester")
	require.NoError(t, err)
	_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"notes": "new"}, "tester")
	require.NoError(t, err)

	real := svc.store
	svc.store = &columnsSavingStore{Store: real, hook: func() error {
		if _, err := svc.SaveVersion(ctx, view.ID, "v2", "", "tester"); err != nil {
			return err
		}
		return svc.DeleteColumn(ctx, view.ID, columnID(t, view, "notes"), "tester")
	}}
	diff, err := svc.Diff(ctx, view.ID, DiffDraft, nil, 0, 0)
	svc.store = real
	require.NoError(t, err)
	assert.Equal(t, 2, diff.To)
	assert.Empty(t, diff.Columns, "versions 1 and 2 have the same columns")
	require.Len(t, diff.Rows, 1, "the saved edit to notes shows")
	assert.Equal(t, "new", diff.Rows[0].After[columnID(t, view, "notes")])
}

// diffHookStore runs hook once, before the first row page of a diff is read, as a version
// prune or a save landing between a diff's column reads and its row reads would.
type diffHookStore struct {
	Store
	hook func() error
	done bool
}

func (s *diffHookStore) DiffEvalDatasetVersions(ctx context.Context, datasetID string, from, to int, afterSeq int64, limit int) ([]configstore.EvalDatasetRowChange, error) {
	if !s.done {
		s.done = true
		if err := s.hook(); err != nil {
			return nil, err
		}
	}
	return s.Store.DiffEvalDatasetVersions(ctx, datasetID, from, to, afterSeq, limit)
}

// diffReadThenHookStore reads the first row page of a diff, then runs hook once and returns
// what it read, as a save landing after a diff's row query would.
type diffReadThenHookStore struct {
	Store
	hook func() error
	done bool
}

func (s *diffReadThenHookStore) DiffEvalDatasetVersions(ctx context.Context, datasetID string, from, to int, afterSeq int64, limit int) ([]configstore.EvalDatasetRowChange, error) {
	changes, err := s.Store.DiffEvalDatasetVersions(ctx, datasetID, from, to, afterSeq, limit)
	if err != nil || s.done {
		return changes, err
	}
	s.done = true
	if err := s.hook(); err != nil {
		return nil, err
	}
	return changes, nil
}

// A row-only edit that lands after a draft diff's row query, and is then saved, leaves the
// columns equal but makes `to` a saved version whose cells differ from what was read, so the
// diff runs again against that version.
func TestServiceDiffOfADraftRowEditSavedAfterTheRowRead(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "row-saved-after-diff", Columns: []ColumnSpec{{Name: "input", Type: ColumnInput}}})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "a"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v1", "", "tester")
	require.NoError(t, err)
	_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": "b"}, "tester")
	require.NoError(t, err)

	real := svc.store
	svc.store = &diffReadThenHookStore{Store: real, hook: func() error {
		if _, err := svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": "c"}, "tester"); err != nil {
			return err
		}
		_, err := svc.SaveVersion(ctx, view.ID, "v2", "", "tester")
		return err
	}}
	diff, err := svc.Diff(ctx, view.ID, DiffDraft, nil, 0, 0)
	svc.store = real
	require.NoError(t, err)
	assert.Equal(t, 2, diff.To)
	require.Len(t, diff.Rows, 1)
	assert.Equal(t, "c", diff.Rows[0].After[columnID(t, view, "input")], "version 2 holds c")
}

// A version removed while a diff reads its rows leaves rows the response cannot vouch for,
// so the diff answers not found instead of comparing against a version that is gone.
func TestServiceDiffOfAVersionPrunedMidRead(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "pruned-mid-diff", Template: "qa"})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "a"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v1", "", "tester")
	require.NoError(t, err)
	_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": "b"}, "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v2", "", "tester")
	require.NoError(t, err)
	_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": "c"}, "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v3", "", "tester")
	require.NoError(t, err)

	real := svc.store
	svc.store = &diffHookStore{Store: real, hook: func() error { return svc.DeleteVersion(ctx, view.ID, 1) }}
	_, err = svc.Diff(ctx, view.ID, 2, new(1), 0, 0)
	svc.store = real
	require.ErrorIs(t, err, configstore.ErrNotFound, "version 1 is gone, so the diff cannot name it")
}

// A draft saved with a new column between a draft diff's column reads and its row reads
// makes `to` a saved version with that column, so the diff carries the column and its cells.
func TestServiceDiffOfADraftSavedWithANewColumnMidRead(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "column-mid-diff", Columns: []ColumnSpec{{Name: "input", Type: ColumnInput}}})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "q"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "v1", "", "tester")
	require.NoError(t, err)
	_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": "q2"}, "tester")
	require.NoError(t, err)

	real := svc.store
	svc.store = &diffHookStore{Store: real, hook: func() error {
		if _, err := svc.AddColumn(ctx, view.ID, ColumnSpec{Name: "notes", Type: ColumnVariable}, "tester"); err != nil {
			return err
		}
		if _, err := svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": "q2", "notes": "kept"}, "tester"); err != nil {
			return err
		}
		_, err := svc.SaveVersion(ctx, view.ID, "v2", "", "tester")
		return err
	}}
	diff, err := svc.Diff(ctx, view.ID, DiffDraft, nil, 0, 0)
	svc.store = real
	require.NoError(t, err)
	assert.Equal(t, 2, diff.To)
	current, err := svc.Get(ctx, view.ID)
	require.NoError(t, err)
	notes := columnID(t, current, "notes")
	require.True(t, slices.ContainsFunc(diff.Columns, func(c ColumnChange) bool { return c.After != nil && c.After.ID == notes }),
		"the saved version's new column is listed")
	require.Len(t, diff.Rows, 1)
	assert.Equal(t, "kept", diff.Rows[0].After[notes], "the new column's cell is kept")
}

func TestServiceRestoreDiffAndRetention(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "history", Template: "qa"})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "a"}, {"input": "b"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.SaveVersion(ctx, view.ID, "baseline", "", "tester")
	require.NoError(t, err)
	// A saved version is read under the dataset's read scope, as the draft is.
	hidden := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("1 = 0") })
	_, err = svc.Rows(hidden, view.ID, RowQuery{Version: 1})
	require.ErrorIs(t, err, configstore.ErrNotFound)

	_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": "a2"}, "tester")
	require.NoError(t, err)
	_, err = svc.AddColumn(ctx, view.ID, ColumnSpec{Name: "context", Type: ColumnVariable}, "tester")
	require.NoError(t, err)

	// The draft can be compared before it is saved.
	draft, err := svc.Diff(ctx, view.ID, 2, nil, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, draft.From)
	require.Len(t, draft.Rows, 1)
	assert.Equal(t, ChangeChanged, draft.Rows[0].Change)
	assert.Equal(t, "a", draft.Rows[0].Before[columnID(t, view, "input")])
	assert.Equal(t, "a2", draft.Rows[0].After[columnID(t, view, "input")])
	require.Len(t, draft.Columns, 1)
	assert.Equal(t, ChangeAdded, draft.Columns[0].Change)

	// Restoring saves the waiting edits as version 2, then the restore as version 3.
	restored, err := svc.Restore(ctx, view.ID, 1, "tester")
	require.NoError(t, err)
	assert.Equal(t, 3, restored.Number)
	assert.Equal(t, "Restored from version 1", restored.Description)
	page, err := svc.Rows(ctx, view.ID, RowQuery{})
	require.NoError(t, err)
	assert.Equal(t, "a", page.Rows[0].Cells[columnID(t, view, "input")])
	current, err := svc.Get(ctx, view.ID)
	require.NoError(t, err)
	assert.Len(t, current.Columns, 2, "the restore brings the columns back too")

	again, err := svc.Restore(ctx, view.ID, 3, "tester")
	require.NoError(t, err)
	assert.Equal(t, 3, again.Number, "restoring the current version does nothing")

	first, err := svc.Diff(ctx, view.ID, 1, nil, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, first.From)
	assert.Len(t, first.Rows, 2)
	same, err := svc.Diff(ctx, view.ID, 3, new(1), 0, 0)
	require.NoError(t, err)
	assert.Empty(t, same.Rows, "version 3 reads what version 1 reads")
	_, err = svc.Diff(ctx, view.ID, 1, new(2), 0, 0)
	require.True(t, evals.IsInvalid(err))

	require.ErrorIs(t, svc.DeleteVersion(ctx, view.ID, 3), configstore.ErrEvalDatasetLatestVersion)
	_, err = svc.Update(ctx, view.ID, DatasetInput{Name: "history", MaxVersions: new(0)})
	require.True(t, evals.IsInvalid(err))
	_, err = svc.Update(ctx, view.ID, DatasetInput{Name: "history", MaxVersions: new(1)})
	require.NoError(t, err)
	versions, err := svc.Versions(ctx, view.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1, "lowering the cap removes the oldest versions right away")
	assert.Equal(t, 3, versions[0].Number)
}

func TestServiceDuplicate(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "source", Template: "qa", Tags: []string{"golden"}})
	require.NoError(t, err)
	split, err := svc.CreateSplit(ctx, view.ID, SplitInput{Name: "smoke"})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "a", "expected_output": "1"}, {"input": "b"}}, "", "tester")
	require.NoError(t, err)
	joined, _, err := svc.SetSplitRows(ctx, view.ID, split.ID, []string{added.Rows[1].RowID}, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, joined)

	dup, err := svc.Duplicate(ctx, view.ID, "", "tester")
	require.NoError(t, err)
	assert.Equal(t, "source (copy)", dup.Name)
	assert.Equal(t, 1, dup.HeadVersion, "the copy starts at version 1")
	assert.EqualValues(t, 2, dup.RowCount)
	require.Len(t, dup.Splits, 1)
	assert.EqualValues(t, 1, dup.Splits[0].RowCount)
	assert.NotEqual(t, view.Columns[0].ID, dup.Columns[0].ID)

	page, err := svc.Rows(ctx, dup.ID, RowQuery{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{columnID(t, dup, "input"): "a", columnID(t, dup, "expected_output"): "1"}, page.Rows[0].Cells,
		"cells follow the copy's new column IDs")
	inSplit, err := svc.Rows(ctx, dup.ID, RowQuery{SplitID: dup.Splits[0].ID})
	require.NoError(t, err)
	require.Len(t, inSplit.Rows, 1)
	assert.Equal(t, "b", inSplit.Rows[0].Cells[columnID(t, dup, "input")])

	// A split that fails to copy removes the half-made copy.
	svc.store = failingSplitStore{svc.store}
	_, err = svc.Duplicate(ctx, view.ID, "broken copy", "tester")
	require.ErrorIs(t, err, errSplitCreate)
	_, total, err := svc.List(ctx, configstore.EvalDatasetListParams{Search: "broken copy"})
	require.NoError(t, err)
	assert.Zero(t, total, "the copy is removed")
}

// A copy reads the schema first and the rows after, page by page. A column change in between
// would put cells of the new shape under the old schema, so the copy is refused and removed.
func TestServiceDuplicateRefusesASourceThatChanged(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "moving", Columns: []ColumnSpec{
		{Name: "input", Type: ColumnInput}, {Name: "picture", Type: ColumnVariable},
	}})
	require.NoError(t, err)
	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"input": "q", "picture": "https://example.com/a.png"}}, "", "tester")
	require.NoError(t, err)
	picture := columnID(t, view, "picture")

	svc.store = &retypingRowsStore{Store: svc.store, sourceID: view.ID, retype: func() error {
		_, err := svc.UpdateColumn(ctx, view.ID, picture, ColumnSpec{Name: "picture", Type: ColumnFile}, "tester")
		return err
	}}
	_, err = svc.Duplicate(ctx, view.ID, "", "tester")
	require.ErrorIs(t, err, ErrEvalDatasetChangedDuringCopy)
	_, total, err := svc.List(ctx, configstore.EvalDatasetListParams{Search: "moving (copy)"})
	require.NoError(t, err)
	assert.Zero(t, total, "the copy is removed")
}

// Split membership is not versioned, so a member replaced between two pages of a split's
// copy would give the copy a membership the source never had. The copy is refused instead.
func TestServiceDuplicateRefusesASplitThatChanged(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "members", Template: "qa"})
	require.NoError(t, err)
	rows := make([]map[string]string, writeBatch+2)
	for i := range rows {
		rows[i] = map[string]string{"input": fmt.Sprint(i)}
	}
	added, err := svc.AddRows(ctx, view.ID, rows, "", "tester")
	require.NoError(t, err)
	split, err := svc.CreateSplit(ctx, view.ID, SplitInput{Name: "most"})
	require.NoError(t, err)
	members := make([]string, 0, writeBatch+1)
	for _, row := range added.Rows[1:] {
		members = append(members, row.RowID)
	}
	_, _, err = svc.SetSplitRows(ctx, view.ID, split.ID, members, nil)
	require.NoError(t, err)

	real := svc.store
	svc.store = &splitEditingStore{Store: real, splitID: split.ID, edit: func() error {
		// Take out a member already copied, and add a row the first page did not list.
		_, _, err := svc.SetSplitRows(ctx, view.ID, split.ID, []string{added.Rows[0].RowID}, []string{added.Rows[1].RowID})
		return err
	}}
	_, err = svc.Duplicate(ctx, view.ID, "", "tester")
	svc.store = real
	require.ErrorIs(t, err, ErrEvalDatasetChangedDuringCopy)
	_, total, err := svc.List(ctx, configstore.EvalDatasetListParams{Search: "members (copy)"})
	require.NoError(t, err)
	assert.Zero(t, total, "the copy is removed")
}

// txReadRecordingStore records each GetEvalDataset made while a WithEvalDataset fn runs: a
// second pool connection taken while the transaction holds one.
type txReadRecordingStore struct {
	Store
	inTx      bool
	readsInTx int
}

func (s *txReadRecordingStore) WithEvalDataset(ctx context.Context, datasetID string, fn func(tx configstore.EvalDatasetTx) error) error {
	return s.Store.WithEvalDataset(ctx, datasetID, func(tx configstore.EvalDatasetTx) error {
		s.inTx = true
		defer func() { s.inTx = false }()
		return fn(tx)
	})
}

func (s *txReadRecordingStore) GetEvalDataset(ctx context.Context, id string) (*tables.TableEvalDataset, error) {
	if s.inTx {
		s.readsInTx++
	}
	return s.Store.GetEvalDataset(ctx, id)
}

// A copy reads nothing through the pool while its own row transaction is open, so
// concurrent copies cannot each hold a connection and wait for another.
func TestServiceDuplicateReadsNothingInsideItsTransaction(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "pooled", Template: "qa"})
	require.NoError(t, err)
	rows := make([]map[string]string, writeBatch+1)
	for i := range rows {
		rows[i] = map[string]string{"input": fmt.Sprint(i)}
	}
	_, err = svc.AddRows(ctx, view.ID, rows, "", "tester")
	require.NoError(t, err)

	real := svc.store
	recording := &txReadRecordingStore{Store: real}
	svc.store = recording
	dup, err := svc.Duplicate(ctx, view.ID, "", "tester")
	svc.store = real
	require.NoError(t, err)
	assert.EqualValues(t, writeBatch+1, dup.RowCount)
	assert.Zero(t, recording.readsInTx, "no dataset read runs inside the copy's transaction")
}

// splitEditingStore runs edit before the second page of the split's rows is read, as a
// membership change landing in the middle of a copy would.
type splitEditingStore struct {
	Store
	splitID string
	edit    func() error
	done    bool
}

func (s *splitEditingStore) ListEvalDatasetRows(ctx context.Context, params configstore.EvalDatasetRowListParams) ([]tables.TableEvalDatasetRow, error) {
	if params.SplitID == s.splitID && params.AfterSeq > 0 && !s.done {
		s.done = true
		if err := s.edit(); err != nil {
			return nil, err
		}
	}
	return s.Store.ListEvalDatasetRows(ctx, params)
}

// retypingRowsStore runs retype before the first row page of the source is read, as a
// column edit landing in the middle of a copy would.
type retypingRowsStore struct {
	Store
	sourceID string
	retype   func() error
	done     bool
}

func (s *retypingRowsStore) ListEvalDatasetRows(ctx context.Context, params configstore.EvalDatasetRowListParams) ([]tables.TableEvalDatasetRow, error) {
	if params.DatasetID == s.sourceID && !s.done {
		s.done = true
		if err := s.retype(); err != nil {
			return nil, err
		}
	}
	return s.Store.ListEvalDatasetRows(ctx, params)
}

// A file uploaded and referenced after the copy listed the source's files is still
// copied: the copy's cell must name a file the copy owns, so deleting the source leaves it
// readable.
func TestServiceDuplicateCopiesAFileTheListingMissed(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "late file", Template: "qa", Columns: []ColumnSpec{{Name: "image", Type: ColumnFile}}})
	require.NoError(t, err)
	ref, err := svc.UploadFile(ctx, view.ID, "cat.png", "image/png", []byte("png bytes"), "tester")
	require.NoError(t, err)
	cell, err := json.Marshal([]FileRef{*ref})
	require.NoError(t, err)
	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"input": "late", "image": string(cell)}}, "", "tester")
	require.NoError(t, err)

	// The listing ran before the upload, so it saw no file.
	real := svc.store
	svc.store = emptyFileListStore{Store: real}
	copied, err := svc.Duplicate(ctx, view.ID, "", "tester")
	svc.store = real
	require.NoError(t, err)
	require.NoError(t, svc.Delete(ctx, view.ID))

	page, err := svc.Rows(ctx, copied.ID, RowQuery{})
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	refs, err := ParseFileCell(page.Rows[0].Cells[columnID(t, copied, "image")])
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.NotEqual(t, ref.ID, refs[0].ID, "the copy names a file of its own")
	assert.Equal(t, fileURL(copied.ID, refs[0].ID), refs[0].URL)
	_, data, err := svc.File(ctx, copied.ID, refs[0].ID)
	require.NoError(t, err, "the copy's file is readable after the source is gone")
	assert.Equal(t, "png bytes", string(data))
}

// firstPageHookStore runs hook once, after the first row page is read, as a delete or a
// prune landing in the middle of an export would.
type firstPageHookStore struct {
	Store
	hook func() error
	done bool
}

func (s *firstPageHookStore) ListEvalDatasetRows(ctx context.Context, params configstore.EvalDatasetRowListParams) ([]tables.TableEvalDatasetRow, error) {
	rows, err := s.Store.ListEvalDatasetRows(ctx, params)
	if err == nil && params.AfterSeq == 0 && !s.done {
		s.done = true
		if err := s.hook(); err != nil {
			return nil, err
		}
	}
	return rows, err
}

// emptyFileListStore lists no files, as a listing made before an upload would.
type emptyFileListStore struct{ Store }

func (emptyFileListStore) ListEvalDatasetFiles(context.Context, string) ([]tables.TableEvalDatasetFile, error) {
	return nil, nil
}

var errSplitCreate = errors.New("split create failed")

// failingSplitStore fails every split create.
type failingSplitStore struct{ Store }

func (failingSplitStore) CreateEvalDatasetSplit(context.Context, *tables.TableEvalDatasetSplit) error {
	return errSplitCreate
}

// A split membership change is one write: a remove that fails rolls the add back.
func TestServiceSetSplitRowsIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "membership", Template: "qa"})
	require.NoError(t, err)
	split, err := svc.CreateSplit(ctx, view.ID, SplitInput{Name: "smoke"})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "a"}, {"input": "b"}}, "", "tester")
	require.NoError(t, err)
	_, _, err = svc.SetSplitRows(ctx, view.ID, split.ID, []string{added.Rows[0].RowID}, nil)
	require.NoError(t, err)

	injected := errors.New("remove failed")
	db := svc.store.(interface{ DB() *gorm.DB }).DB()
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("fail_split_remove", func(tx *gorm.DB) {
		if tx.Statement.Table == "evals_dataset_split_rows" {
			_ = tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Delete().Remove("fail_split_remove") })
	_, _, err = svc.SetSplitRows(ctx, view.ID, split.ID, []string{added.Rows[1].RowID}, []string{added.Rows[0].RowID})
	require.ErrorIs(t, err, injected)
	members, err := svc.Rows(ctx, view.ID, RowQuery{SplitID: split.ID})
	require.NoError(t, err)
	require.Len(t, members.Rows, 1, "the add is rolled back with the failed remove")
	assert.Equal(t, added.Rows[0].RowID, members.Rows[0].RowID)
}

// A split is created only on a dataset the caller can see, as its edits are, so a hidden
// dataset gets no split and its split names are not revealed by a name clash.
func TestServiceCreateSplitHonorsTheQueryScope(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "scoped splits", Template: "qa"})
	require.NoError(t, err)
	_, err = svc.CreateSplit(ctx, view.ID, SplitInput{Name: "taken"})
	require.NoError(t, err)

	hidden := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("1 = 0") })
	_, err = svc.CreateSplit(hidden, view.ID, SplitInput{Name: "sneaky"})
	require.ErrorIs(t, err, configstore.ErrNotFound)
	_, err = svc.CreateSplit(hidden, view.ID, SplitInput{Name: "taken"})
	require.ErrorIs(t, err, configstore.ErrNotFound, "a name clash on a hidden dataset is not revealed")
	splits, err := svc.Splits(ctx, view.ID)
	require.NoError(t, err)
	require.Len(t, splits, 1, "no split is created on a hidden dataset")
}

// BenchmarkImportExport guards the memory cost of moving a large dataset. Import and
// export hold one batch at a time, so allocations grow with rows, not with a copy of
// the whole file.
func BenchmarkImportExport(b *testing.B) {
	const rows = 50000
	var file bytes.Buffer
	file.WriteString("input,expected_output\n")
	for i := range rows {
		fmt.Fprintf(&file, "what is the answer to question %d,the answer is %d\n", i, i)
	}
	ctx := context.Background()
	svc := newTestService(b, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		view, err := svc.Create(ctx, DatasetInput{Name: fmt.Sprintf("bench-%d", i), Template: "qa"})
		require.NoError(b, err)
		result, err := svc.Import(ctx, view.ID, bytes.NewReader(file.Bytes()), ImportOptions{HasHeader: true})
		require.NoError(b, err)
		require.Equal(b, rows, result.RowsAdded)
		require.NoError(b, svc.Export(ctx, view.ID, io.Discard, FormatCSV, 0, ""))
		// Each iteration starts from the same store size.
		b.StopTimer()
		require.NoError(b, svc.Delete(ctx, view.ID))
		b.StartTimer()
	}
}

func TestServiceFolders(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)

	_, err := svc.CreateFolder(ctx, FolderInput{Name: "  "})
	require.True(t, evals.IsInvalid(err))
	folder, err := svc.CreateFolder(ctx, FolderInput{Name: " Support ", Description: "tickets"})
	require.NoError(t, err)
	assert.Equal(t, "Support", folder.Name)
	_, err = svc.CreateFolder(ctx, FolderInput{Name: "Support"})
	require.ErrorIs(t, err, configstore.ErrEvalDatasetFolderNameExists)
	other, err := svc.CreateFolder(ctx, FolderInput{Name: "Agents"})
	require.NoError(t, err)

	// A dataset can be created in a folder, and moved between folders and out of them.
	_, err = svc.Create(ctx, DatasetInput{Name: "bad", FolderID: new("missing")})
	require.ErrorIs(t, err, configstore.ErrNotFound)
	view, err := svc.Create(ctx, DatasetInput{Name: "golden", Template: "qa", FolderID: new(folder.ID)})
	require.NoError(t, err)
	require.NotNil(t, view.FolderID)
	assert.Equal(t, folder.ID, *view.FolderID)

	// Editing a dataset's details leaves it in its folder.
	view, err = svc.Update(ctx, view.ID, DatasetInput{Name: "golden set"})
	require.NoError(t, err)
	require.NotNil(t, view.FolderID)

	view, err = svc.Move(ctx, view.ID, new(other.ID))
	require.NoError(t, err)
	assert.Equal(t, other.ID, *view.FolderID)
	_, err = svc.Move(ctx, view.ID, new("missing"))
	require.ErrorIs(t, err, configstore.ErrNotFound)

	renamed, err := svc.UpdateFolder(ctx, other.ID, FolderInput{Name: "Agent runs"})
	require.NoError(t, err)
	assert.Equal(t, "Agent runs", renamed.Name)
	_, err = svc.UpdateFolder(ctx, other.ID, FolderInput{Name: "Support"})
	require.ErrorIs(t, err, configstore.ErrEvalDatasetFolderNameExists)

	folders, err := svc.Folders(ctx)
	require.NoError(t, err)
	require.Len(t, folders, 2)
	assert.Equal(t, "Agent runs", folders[0].Name, "folders are listed by name")

	// Deleting a folder keeps its datasets: they move out of it.
	require.NoError(t, svc.DeleteFolder(ctx, other.ID))
	require.ErrorIs(t, svc.DeleteFolder(ctx, other.ID), configstore.ErrNotFound)
	kept, err := svc.Get(ctx, view.ID)
	require.NoError(t, err)
	assert.Nil(t, kept.FolderID)

	moved, err := svc.Move(ctx, view.ID, new(folder.ID))
	require.NoError(t, err)
	moved, err = svc.Move(ctx, moved.ID, nil)
	require.NoError(t, err)
	assert.Nil(t, moved.FolderID)

	// Folders nest: one can be created inside another, and moved in and out.
	child, err := svc.CreateFolder(ctx, FolderInput{Name: "Refunds", ParentID: new(folder.ID)})
	require.NoError(t, err)
	require.NotNil(t, child.ParentID)
	_, err = svc.CreateFolder(ctx, FolderInput{Name: "Lost", ParentID: new("missing")})
	require.ErrorIs(t, err, configstore.ErrNotFound)
	grandchild, err := svc.CreateFolder(ctx, FolderInput{Name: "Chargebacks", ParentID: new(child.ID)})
	require.NoError(t, err)

	// A folder cannot go into itself or into a folder below it.
	_, err = svc.MoveFolder(ctx, folder.ID, new(folder.ID))
	require.True(t, evals.IsInvalid(err))
	_, err = svc.MoveFolder(ctx, folder.ID, new(grandchild.ID))
	require.True(t, evals.IsInvalid(err))
	_, err = svc.MoveFolder(ctx, child.ID, new("missing"))
	require.ErrorIs(t, err, configstore.ErrNotFound)

	// Renaming a folder leaves it where it is.
	renamedChild, err := svc.UpdateFolder(ctx, child.ID, FolderInput{Name: "Refund cases"})
	require.NoError(t, err)
	require.NotNil(t, renamedChild.ParentID)
	assert.Equal(t, folder.ID, *renamedChild.ParentID)

	// Deleting a folder moves what it holds up one level: folders and datasets alike.
	inChild, err := svc.Move(ctx, view.ID, new(child.ID))
	require.NoError(t, err)
	require.NoError(t, svc.DeleteFolder(ctx, child.ID))
	lifted, err := svc.Get(ctx, inChild.ID)
	require.NoError(t, err)
	require.NotNil(t, lifted.FolderID)
	assert.Equal(t, folder.ID, *lifted.FolderID)
	folders, err = svc.Folders(ctx)
	require.NoError(t, err)
	require.Len(t, folders, 2)
	for _, item := range folders {
		if item.ID == grandchild.ID {
			require.NotNil(t, item.ParentID)
			assert.Equal(t, folder.ID, *item.ParentID)
		}
	}

	top, err := svc.MoveFolder(ctx, grandchild.ID, nil)
	require.NoError(t, err)
	assert.Nil(t, top.ParentID)
}

// memoryFiles is an in-memory object store for file tests.
type memoryFiles struct {
	objects map[string][]byte
}

func (m *memoryFiles) Put(_ context.Context, key string, data []byte, _ map[string]string) error {
	m.objects[key] = bytes.Clone(data)
	return nil
}

func (m *memoryFiles) Get(_ context.Context, key string) ([]byte, error) {
	data, ok := m.objects[key]
	if !ok {
		return nil, errors.New("no such object")
	}
	return data, nil
}

func (m *memoryFiles) DeleteBatch(_ context.Context, keys []string) error {
	for _, key := range keys {
		delete(m.objects, key)
	}
	return nil
}

func TestServiceFiles(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "vision", Template: "qa", Columns: []ColumnSpec{{Name: "image", Type: ColumnFile}}})
	require.NoError(t, err)

	_, err = svc.UploadFile(ctx, view.ID, " ", "image/png", []byte("x"), "tester")
	require.True(t, evals.IsInvalid(err), "a file needs a name")
	_, err = svc.UploadFile(ctx, view.ID, "empty.png", "image/png", nil, "tester")
	require.True(t, evals.IsInvalid(err), "a file needs content")
	_, err = svc.UploadFile(ctx, "missing", "a.png", "image/png", []byte("x"), "tester")
	require.ErrorIs(t, err, configstore.ErrNotFound)

	// Without an object store the file is kept in the database, up to a size cap.
	ref, err := svc.UploadFile(ctx, view.ID, "cat.png", "image/png", []byte("png bytes"), "tester")
	require.NoError(t, err)
	assert.Equal(t, "cat.png", ref.Name)
	assert.EqualValues(t, 9, ref.Size)
	assert.Equal(t, "/api/evals/datasets/"+view.ID+"/files/"+ref.ID, ref.URL)
	_, err = svc.UploadFile(ctx, view.ID, "big.bin", "", make([]byte, MaxInlineFileBytes+1), "tester")
	require.True(t, evals.IsInvalid(err))

	file, data, err := svc.File(ctx, view.ID, ref.ID)
	require.NoError(t, err)
	assert.Equal(t, "image/png", file.ContentType)
	assert.Equal(t, "png bytes", string(data))
	_, _, err = svc.File(ctx, view.ID, "missing")
	require.ErrorIs(t, err, configstore.ErrNotFound)
	// The dataset's read scope applies to its files: a caller who cannot see it gets none.
	hidden := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB { return db.Where("1 = 0") })
	_, _, err = svc.File(hidden, view.ID, ref.ID)
	require.ErrorIs(t, err, configstore.ErrNotFound)

	// A row's file cell lists files; a list of URLs is taken too.
	cell, err := json.Marshal([]FileRef{*ref})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{
		{"input": "what is in the picture", "image": string(cell)},
		{"input": "and here", "image": "https://example.com/dog.png"},
	}, "", "tester")
	require.NoError(t, err)
	refs, err := ParseFileCell(added.Rows[1].Cells[columnID(t, view, "image")])
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "dog.png", refs[0].Name)
	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"image": "nope"}}, "", "tester")
	require.True(t, evals.IsInvalid(err))

	// With an object store the bytes go there, and a larger file is fine.
	objects := &memoryFiles{objects: map[string][]byte{}}
	svc.SetFileStore(objects)
	large, err := svc.UploadFile(ctx, view.ID, "big.bin", "", make([]byte, MaxInlineFileBytes+1), "tester")
	require.NoError(t, err)
	assert.Equal(t, "application/octet-stream", large.ContentType)
	require.Len(t, objects.objects, 1)
	_, data, err = svc.File(ctx, view.ID, large.ID)
	require.NoError(t, err)
	assert.Len(t, data, MaxInlineFileBytes+1)
	// A file kept in the database before the object store was set is still readable.
	_, data, err = svc.File(ctx, view.ID, ref.ID)
	require.NoError(t, err)
	assert.Equal(t, "png bytes", string(data))

	// A record that fails to save removes the stored object, even when the request was cancelled
	// meanwhile.
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	strict := &cancelAwareFiles{memoryFiles: memoryFiles{objects: map[string][]byte{}}}
	failing := newTestService(t, nil)
	failingView, err := failing.Create(ctx, DatasetInput{Name: "vision", Template: "qa"})
	require.NoError(t, err)
	failing.SetFileStore(strict)
	failing.store = cancellingFileStore{Store: failing.store, cancel: cancel}
	_, err = failing.UploadFile(cancelCtx, failingView.ID, "late.png", "image/png", []byte("x"), "tester")
	require.ErrorIs(t, err, errFileRecord)
	assert.Empty(t, strict.objects, "the object is removed on a context the cancel does not reach")

	// A delete whose request is cancelled once the records are gone still removes the objects:
	// their keys go with the records, so nothing could find them later.
	deleteCtx, cancelDelete := context.WithCancel(ctx)
	defer cancelDelete()
	deleting := newTestService(t, nil)
	deletingView, err := deleting.Create(ctx, DatasetInput{Name: "vision", Template: "qa"})
	require.NoError(t, err)
	deletingFiles := &cancelAwareFiles{memoryFiles: memoryFiles{objects: map[string][]byte{}}}
	deleting.SetFileStore(deletingFiles)
	_, err = deleting.UploadFile(ctx, deletingView.ID, "kept.png", "image/png", []byte("x"), "tester")
	require.NoError(t, err)
	require.Len(t, deletingFiles.objects, 1)
	deleting.store = cancellingDeleteStore{Store: deleting.store, cancel: cancelDelete}
	require.NoError(t, deleting.Delete(deleteCtx, deletingView.ID))
	assert.Empty(t, deletingFiles.objects, "the objects are removed on a context the cancel does not reach")

	// A copy owns its files: its file cells point at its own records, so it keeps working after
	// the source is deleted.
	both, err := json.Marshal([]FileRef{*ref, *large})
	require.NoError(t, err)
	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"input": "two files", "image": string(both)}}, "", "tester")
	require.NoError(t, err)
	copied, err := svc.Duplicate(ctx, view.ID, "", "tester")
	require.NoError(t, err)

	// Deleting the dataset removes its files from both places.
	require.NoError(t, svc.Delete(ctx, view.ID))
	require.Len(t, objects.objects, 1, "only the copy's object is left")

	copyPage, err := svc.Rows(ctx, copied.ID, RowQuery{})
	require.NoError(t, err)
	var copyRefs []FileRef
	for _, row := range copyPage.Rows {
		if cell := row.Cells[columnID(t, copied, "image")]; strings.Contains(cell, `"id"`) {
			copyRefs, err = ParseFileCell(cell)
			require.NoError(t, err)
		}
	}
	require.Len(t, copyRefs, 2)
	for i, want := range []int{len("png bytes"), MaxInlineFileBytes + 1} {
		assert.Equal(t, fileURL(copied.ID, copyRefs[i].ID), copyRefs[i].URL)
		_, data, err := svc.File(ctx, copied.ID, copyRefs[i].ID)
		require.NoError(t, err, "the copy's file %d is readable after the source is gone", i)
		assert.Len(t, data, want)
	}
	require.NoError(t, svc.Delete(ctx, copied.ID))
	assert.Empty(t, objects.objects)
}

var errFileRecord = errors.New("file record failed")

// cancellingFileStore cancels the request and fails, as a client that goes away while the
// record is written would.
type cancellingFileStore struct {
	Store
	cancel context.CancelFunc
}

func (s cancellingFileStore) CreateEvalDatasetFile(context.Context, *tables.TableEvalDatasetFile) error {
	s.cancel()
	return errFileRecord
}

// cancellingDeleteStore deletes the dataset and then cancels the request, as a client that
// goes away right after the commit would.
type cancellingDeleteStore struct {
	Store
	cancel context.CancelFunc
}

func (s cancellingDeleteStore) DeleteEvalDataset(ctx context.Context, id string) ([]string, error) {
	keys, err := s.Store.DeleteEvalDataset(context.WithoutCancel(ctx), id)
	s.cancel()
	return keys, err
}

// cancelAwareFiles refuses work on a cancelled context, as a real object store client does.
type cancelAwareFiles struct{ memoryFiles }

func (c *cancelAwareFiles) DeleteBatch(ctx context.Context, keys []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.memoryFiles.DeleteBatch(ctx, keys)
}

// Lowering the version cap is one write with the prune it causes: when the prune fails, the
// old cap and every version stay.
func TestServiceUpdateCapRollsBackWithItsPrune(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "uncapped", Template: "qa"})
	require.NoError(t, err)
	added, err := svc.AddRows(ctx, view.ID, []map[string]string{{"input": "a"}}, "", "tester")
	require.NoError(t, err)
	for i, value := range []string{"b", "c", "d"} {
		_, err = svc.UpdateRow(ctx, view.ID, added.Rows[0].RowID, map[string]string{"input": value}, "tester")
		require.NoError(t, err)
		_, err = svc.SaveVersion(ctx, view.ID, fmt.Sprintf("v%d", i+1), "", "tester")
		require.NoError(t, err)
	}
	versionNumbers := func() []int {
		versions, err := svc.Versions(ctx, view.ID)
		require.NoError(t, err)
		numbers := make([]int, len(versions))
		for i := range versions {
			numbers[i] = versions[i].Number
		}
		return numbers
	}
	before := versionNumbers()
	require.Len(t, before, 3)
	injected := errors.New("prune failed")
	db := svc.store.(interface{ DB() *gorm.DB }).DB()
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("fail_version_prune", func(tx *gorm.DB) {
		if tx.Statement.Table == "evals_dataset_versions" {
			_ = tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Delete().Remove("fail_version_prune") })
	_, err = svc.Update(ctx, view.ID, DatasetInput{Name: "uncapped", MaxVersions: new(1)})
	require.ErrorIs(t, err, injected, "the update fails with the prune's error")
	got, err := svc.Get(ctx, view.ID)
	require.NoError(t, err)
	assert.Nil(t, got.MaxVersions, "the cap is not kept without its prune")
	assert.Equal(t, before, versionNumbers(), "every version survives the failed prune")
}

// A column changes to a structured type only when every draft cell fits the new type.
func TestServiceUpdateColumnChecksExistingCells(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "typed", Template: "qa", Columns: []ColumnSpec{{Name: "extra", Type: ColumnVariable}}})
	require.NoError(t, err)
	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"input": "q", "extra": "plain text"}}, "", "tester")
	require.NoError(t, err)
	_, err = svc.UpdateColumn(ctx, view.ID, columnID(t, view, "extra"), ColumnSpec{Name: "extra", Type: ColumnExpectedToolCalls}, "tester")
	require.True(t, evals.IsInvalid(err), "plain text is not a valid tool call list: %v", err)
	got, err := svc.Get(ctx, view.ID)
	require.NoError(t, err)
	for _, column := range got.Columns {
		if column.Name == "extra" {
			assert.EqualValues(t, ColumnVariable, column.Type, "the column keeps its type")
		}
	}
}

// Row search reads the values of the version's own columns: not a deleted column's leftovers,
// and text with quotes as it was typed.
func TestServiceRowSearchReadsTheColumnsValues(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, nil)
	view, err := svc.Create(ctx, DatasetInput{Name: "search", Template: "qa", Columns: []ColumnSpec{{Name: "notes", Type: ColumnVariable}}})
	require.NoError(t, err)
	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"input": `say "hi"`, "notes": "needle"}}, "", "tester")
	require.NoError(t, err)
	require.NoError(t, svc.DeleteColumn(ctx, view.ID, columnID(t, view, "notes"), "tester"))
	gone, err := svc.Rows(ctx, view.ID, RowQuery{Search: "needle"})
	require.NoError(t, err)
	assert.EqualValues(t, 0, gone.Total, "a deleted column's value does not match")
	quoted, err := svc.Rows(ctx, view.ID, RowQuery{Search: `"hi"`})
	require.NoError(t, err)
	assert.EqualValues(t, 1, quoted.Total, "text with quotes matches as typed")
}
