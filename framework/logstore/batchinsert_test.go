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
