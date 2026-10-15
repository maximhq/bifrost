package logstore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestBatchCreateIfNotExists_WriterSizedBatch pins that a batch as large as the
// log writer's default (DefaultWriterMaxBatchSize rows) is stored in full. A single
// multi-row INSERT of that size binds rows x columns parameters, which exceeds the
// per-statement parameter limit of both Postgres (65,535) and SQLite (32,766).
func TestBatchCreateIfNotExists_WriterSizedBatch(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "batch.db")}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	now := time.Now().UTC()
	entries := make([]*Log, DefaultWriterMaxBatchSize)
	for i := range entries {
		entries[i] = &Log{
			ID:        fmt.Sprintf("batch-%04d", i),
			Timestamp: now,
			Provider:  "openai",
			Model:     "gpt-4o",
			Status:    "success",
			Object:    "chat.completion",
		}
	}
	require.NoError(t, store.BatchCreateIfNotExists(ctx, entries))
	// Re-inserting the same batch is a no-op thanks to ON CONFLICT DO NOTHING.
	require.NoError(t, store.BatchCreateIfNotExists(ctx, entries))

	var count int64
	require.NoError(t, store.db.Model(&Log{}).Count(&count).Error)
	require.Equal(t, int64(DefaultWriterMaxBatchSize), count)
}

// TestBatchCreateMCPToolLogsIfNotExists_WriterSizedBatch is the MCP tool log
// counterpart of TestBatchCreateIfNotExists_WriterSizedBatch.
func TestBatchCreateMCPToolLogsIfNotExists_WriterSizedBatch(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "batchmcp.db")}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	now := time.Now().UTC()
	entries := make([]*MCPToolLog, DefaultWriterMaxBatchSize)
	for i := range entries {
		entries[i] = &MCPToolLog{
			ID:        fmt.Sprintf("mcp-batch-%04d", i),
			Timestamp: now,
			ToolName:  "search_web",
			Status:    "success",
			CreatedAt: now,
		}
	}
	require.NoError(t, store.BatchCreateMCPToolLogsIfNotExists(ctx, entries))

	var count int64
	require.NoError(t, store.db.Model(&MCPToolLog{}).Count(&count).Error)
	require.Equal(t, int64(DefaultWriterMaxBatchSize), count)
}

// TestInsertBatchSize_StaysUnderParameterLimit pins that the computed chunk size
// keeps every multi-row INSERT under the smallest supported parameter limit.
func TestInsertBatchSize_StaysUnderParameterLimit(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "size.db")}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	for _, model := range []any{&Log{}, &MCPToolLog{}} {
		size, columns := insertBatchSize(store.db, model)
		require.Positive(t, size)
		require.LessOrEqual(t, size*columns, maxInsertBindParams, "%T", model)
	}
}

// TestBatchCreateIfNotExists_WriterSizedBatchPostgres is the Postgres pin for
// issue #7843: a writer-sized batch (DefaultWriterMaxBatchSize rows x 118 bound
// columns) used to be sent as one INSERT, which pgx rejects with "extended
// protocol limited to 65535 parameters", so every full batch fell back to
// row-by-row inserts. The batch must land in full with no error.
func TestBatchCreateIfNotExists_WriterSizedBatchPostgres(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available")
	}
	ctx := context.Background()
	dropAllManagedMatViews(db)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS async_jobs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS webhook_deliveries CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS logs CASCADE").Error)
	require.NoError(t, db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("DELETE FROM migrations").Error)
	require.NoError(t, triggerMigrations(ctx, db, testLogger{}))
	t.Cleanup(func() {
		_ = db.Exec("TRUNCATE logs").Error
		_ = db.Exec("TRUNCATE mcp_tool_logs").Error
	})
	store := &RDBLogStore{db: db, logger: testLogger{}}

	now := time.Now().UTC()
	entries := make([]*Log, DefaultWriterMaxBatchSize)
	for i := range entries {
		entries[i] = &Log{
			ID:        fmt.Sprintf("pg-batch-%04d", i),
			Timestamp: now,
			Provider:  "openai",
			Model:     "gpt-4o",
			Status:    "success",
			Object:    "chat.completion",
		}
	}
	require.NoError(t, store.BatchCreateIfNotExists(ctx, entries), "a writer-sized batch must not exceed the Postgres bind-parameter limit")
	// Idempotent: ON CONFLICT DO NOTHING must survive chunking.
	require.NoError(t, store.BatchCreateIfNotExists(ctx, entries))

	var count int64
	require.NoError(t, store.db.Model(&Log{}).Count(&count).Error)
	require.Equal(t, int64(DefaultWriterMaxBatchSize), count)

	// MCP tool logs stay under the limit at the default batch size (43 columns)
	// but must also be chunked for any configured max_batch_size.
	mcp := make([]*MCPToolLog, 2000)
	for i := range mcp {
		mcp[i] = &MCPToolLog{
			ID:        fmt.Sprintf("pg-mcp-batch-%04d", i),
			Timestamp: now,
			ToolName:  "search_web",
			Status:    "success",
			CreatedAt: now,
		}
	}
	require.NoError(t, store.BatchCreateMCPToolLogsIfNotExists(ctx, mcp), "a 2000-row MCP tool log batch must not exceed the Postgres bind-parameter limit")
	require.NoError(t, store.db.Model(&MCPToolLog{}).Count(&count).Error)
	require.Equal(t, int64(2000), count)
}

// TestBatchUpdateSkipsMissingRows pins the semantics the logging plugin's
// evicted-partial reconcile relies on: BatchUpdate writes each entry's
// non-zero fields onto the present row, leaves content_summary as the row has
// it, and silently skips absent ids instead of inserting or failing the batch.
func TestBatchUpdateSkipsMissingRows(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "update.db")}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	now := time.Now().UTC()
	require.NoError(t, store.CreateIfNotExists(ctx, &Log{
		ID: "present-row", Timestamp: now, CreatedAt: now, Object: "chat.completion", Provider: "openai", Model: "gpt-4o",
		Status: "processing", ContentSummary: "the input summary",
	}))

	require.NoError(t, store.BatchUpdate(ctx, []*Log{
		{ID: "present-row", TotalTokens: 42},
		{ID: "present-row", Status: "success"},
		{ID: "absent-row", Status: "success", TotalTokens: 7},
		nil,
	}))
	require.NoError(t, store.BatchUpdate(ctx, nil))

	row, err := store.FindByID(ctx, "present-row")
	require.NoError(t, err)
	require.Equal(t, "success", row.Status, "later entries for the same id apply too")
	require.Equal(t, 42, row.TotalTokens)
	require.Equal(t, "openai", row.Provider, "zero fields leave existing columns alone")
	require.Equal(t, "the input summary", row.ContentSummary, "content_summary is not regenerated from an output-only entry")

	_, err = store.FindByID(ctx, "absent-row")
	require.ErrorIs(t, err, ErrNotFound, "BatchUpdate must not insert")
}

// TestBatchUpdateWritesFinalVisibility: content_hidden is the one column a
// reconcile must be able to clear, so BatchUpdate writes it from every entry
// even when false.
func TestBatchUpdateWritesFinalVisibility(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "visibility.db")}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	now := time.Now().UTC()
	for _, id := range []string{"to-visible", "to-hidden"} {
		require.NoError(t, store.CreateIfNotExists(ctx, &Log{
			ID: id, Timestamp: now, CreatedAt: now, Object: "chat.completion", Provider: "openai", Model: "gpt-4o",
			Status: "processing", ContentHidden: id == "to-visible",
		}))
	}
	require.NoError(t, store.BatchUpdate(ctx, []*Log{
		{ID: "to-visible", Status: "success", ContentHidden: false},
		{ID: "to-hidden", Status: "success", ContentHidden: true},
	}))

	visible, err := store.FindByID(ctx, "to-visible")
	require.NoError(t, err)
	require.False(t, visible.ContentHidden, "a final visible decision clears the flag")
	hidden, err := store.FindByID(ctx, "to-hidden")
	require.NoError(t, err)
	require.True(t, hidden.ContentHidden, "a final hidden decision sets the flag")
}
