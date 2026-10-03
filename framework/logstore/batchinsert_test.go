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
