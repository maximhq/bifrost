package datasets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLogs is a log store holding a fixed set of logs.
type fakeLogs struct {
	logs     map[string]*logstore.Log
	sessions map[string][]string // session ID to log IDs, oldest first
}

func (f *fakeLogs) FindByID(_ context.Context, id string) (*logstore.Log, error) {
	log, ok := f.logs[id]
	if !ok {
		return nil, fmt.Errorf("log %s not found", id)
	}
	return log, nil
}

func (f *fakeLogs) SearchLogs(_ context.Context, filters logstore.SearchFilters, pagination logstore.PaginationOptions) (*logstore.SearchResult, error) {
	ids := f.sessions[filters.SessionID]
	result := &logstore.SearchResult{}
	for i := pagination.Offset; i < len(ids) && i < pagination.Offset+pagination.Limit; i++ {
		// A listing carries no content, as the real one does not.
		result.Logs = append(result.Logs, logstore.Log{ID: ids[i]})
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
		{input: "where is my invoice"},
	}, "", "tester")
	require.NoError(t, err)
	require.Len(t, added.Rows, 2)
	assert.Nil(t, added.Version, "two rows stay in the draft")

	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"nope": "x"}}, "", "tester")
	require.True(t, evals.IsInvalid(err), "an unknown column is rejected")
	_, err = svc.AddRows(ctx, view.ID, []map[string]string{{"labels": "not json"}}, "", "tester")
	require.True(t, evals.IsInvalid(err), "a structured column takes JSON only")

	rowID := added.Rows[0].RowID
	updated, err := svc.UpdateRow(ctx, view.ID, rowID, map[string]string{"context": "", "expected_output": "use the reset link"}, "tester")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{input: "how do I reset my password", columnID(t, view, "expected_output"): "use the reset link"}, updated.Rows[0].Cells,
		"a patch changes the named cells only, and an empty value clears a cell")

	variables, err := svc.RowVariables(ctx, view.ID, rowID, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"input": "how do I reset my password", "expected_output": "use the reset link"}, variables)

	// A deleted column's cells stop showing up without any row being rewritten.
	require.NoError(t, svc.DeleteColumn(ctx, view.ID, contextCol, "tester"))
	page, err := svc.Rows(ctx, view.ID, RowQuery{})
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

	// Deleting the dataset removes its files from both places.
	require.NoError(t, svc.Delete(ctx, view.ID))
	assert.Empty(t, objects.objects)
}
