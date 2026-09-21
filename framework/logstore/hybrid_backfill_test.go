package logstore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBackfillHybrid(t *testing.T, excludeFields []string) (*HybridLogStore, LogStore, *objectstore.InMemoryObjectStore, func()) {
	t.Helper()
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "backfill.db")}, hybridTestLogger{})
	require.NoError(t, err)
	objStore := objectstore.NewInMemoryObjectStore()
	hybrid := newHybridLogStore(inner, objStore, "bf", hybridTestLogger{}, excludeFields)
	cleanup := func() { _ = hybrid.Close(context.Background()) }
	return hybrid, inner, objStore, cleanup
}

// seedLegacyLog inserts a full-payload row straight through the inner store so
// it looks like a row written before hybrid storage was enabled.
func seedLegacyLog(t *testing.T, inner LogStore, id string, ts time.Time, contentHidden bool) *Log {
	t.Helper()
	prompt := "migrate me " + id
	reply := "ok " + id
	entry := &Log{
		ID:            id,
		Timestamp:     ts,
		Provider:      "anthropic",
		Model:         "claude-sonnet-4-20250514",
		Status:        "success",
		Object:        "chat.completion",
		ContentHidden: contentHidden,
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &prompt}},
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &reply}},
		},
		OutputMessageParsed: &schemas.ChatMessage{
			Content: &schemas.ChatMessageContent{ContentStr: &reply},
		},
		RawRequest:  `{"model":"claude"}`,
		RawResponse: `{"id":"resp_1"}`,
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, inner.Create(context.Background(), entry))
	return entry
}

func TestBackfill_MigratesLegacyRow(t *testing.T) {
	hybrid, inner, objStore, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	old := time.Now().UTC().Add(-48 * time.Hour)
	seedLegacyLog(t, inner, "bf-1", old, false)
	require.Equal(t, 0, objStore.Len(), "precondition: nothing in object storage yet")

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Migrated)
	assert.Zero(t, result.Failed)
	assert.NotZero(t, result.BytesOffloaded)

	// The object exists under the standard key and holds the full payload.
	require.Equal(t, 1, objStore.Len())
	key := ObjectKey("bf", old, "bf-1")
	raw, err := objStore.Get(ctx, key)
	require.NoError(t, err)
	require.Contains(t, string(raw), "migrate me bf-1", "the full input history must be in the object")

	// The DB row is now the lightweight hybrid form.
	row, err := inner.FindByID(ctx, "bf-1")
	require.NoError(t, err)
	assert.True(t, row.HasObject)
	assert.Empty(t, row.RawRequest)
	assert.Empty(t, row.RawResponse)
	assert.Empty(t, row.OutputMessage)
	// Only the last user message remains in the DB row.
	assert.Contains(t, row.InputHistory, "migrate me bf-1")
	assert.NotContains(t, row.InputHistory, "ok bf-1", "assistant history must not stay DB-resident")
	assert.NotEmpty(t, row.ContentSummary)

	// A normal read hydrates the full payload back.
	found, err := hybrid.FindByID(ctx, "bf-1")
	require.NoError(t, err)
	assert.Contains(t, found.InputHistory, "ok bf-1", "full history restored from object storage")
	assert.NotEmpty(t, found.OutputMessage)
}

func TestBackfill_DryRunChangesNothing(t *testing.T) {
	hybrid, inner, objStore, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	seedLegacyLog(t, inner, "bf-dry", time.Now().UTC().Add(-24*time.Hour), false)

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Migrated, "dry run should count what would migrate")
	assert.Equal(t, 0, objStore.Len(), "dry run must not upload")

	row, err := inner.FindByID(ctx, "bf-dry")
	require.NoError(t, err)
	assert.False(t, row.HasObject)
	assert.NotEmpty(t, row.RawRequest, "dry run must not modify rows")
}

func TestBackfill_IsIdempotent(t *testing.T) {
	hybrid, inner, _, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	seedLegacyLog(t, inner, "bf-idem", time.Now().UTC().Add(-24*time.Hour), false)

	first, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), first.Migrated)

	second, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	require.NoError(t, err)
	assert.Zero(t, second.Migrated, "already-migrated rows must be skipped on re-run")
	assert.Zero(t, second.Failed)

	// Row state is still coherent after the second run.
	row, err := inner.FindByID(ctx, "bf-idem")
	require.NoError(t, err)
	assert.True(t, row.HasObject)
}

func TestBackfill_SkipsRowsWithNothingToOffload(t *testing.T) {
	hybrid, inner, _, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	// A metadata-only row (e.g. an aggregate or a row whose payload was never written).
	bare := &Log{
		ID: "bf-bare", Timestamp: time.Now().UTC().Add(-24 * time.Hour),
		Provider: "openai", Model: "gpt-4o", Status: "success", Object: "chat.completion",
	}
	require.NoError(t, bare.SerializeFields())
	require.NoError(t, inner.Create(ctx, bare))

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Skipped, "rows without payload should be skipped, not failed")
	assert.Zero(t, result.Migrated)
}

func TestBackfill_RespectsExcludedFields(t *testing.T) {
	hybrid, inner, objStore, cleanup := newBackfillHybrid(t, []string{"raw_request"})
	defer cleanup()
	ctx := context.Background()

	seedLegacyLog(t, inner, "bf-excl", time.Now().UTC().Add(-24*time.Hour), false)

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Migrated)

	row, err := inner.FindByID(ctx, "bf-excl")
	require.NoError(t, err)
	assert.True(t, row.HasObject)
	assert.NotEmpty(t, row.RawRequest, "excluded field must stay DB-resident")
	assert.Empty(t, row.RawResponse, "non-excluded payload must be cleared")

	// The object must NOT contain the excluded field.
	raw, err := objStore.Get(ctx, ObjectKey("bf", row.Timestamp, "bf-excl"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"raw_request"`)
}

func TestBackfill_HandlesContentHiddenRows(t *testing.T) {
	hybrid, inner, objStore, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	seedLegacyLog(t, inner, "bf-hidden", time.Now().UTC().Add(-24*time.Hour), true)

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Migrated)
	require.Equal(t, 1, objStore.Len())

	// The object holds the FULL payload (hidden rows are never filtered).
	raw, err := objStore.Get(ctx, ObjectKey("bf", time.Now().UTC().Add(-24*time.Hour).Truncate(time.Hour), "bf-hidden"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "migrate me bf-hidden")

	// Reads must never hydrate hidden rows back.
	found, err := hybrid.FindByID(ctx, "bf-hidden")
	require.NoError(t, err)
	assert.Empty(t, found.InputHistory, "content-hidden rows are metadata-only on read")
}

func TestBackfill_OlderThanProtection(t *testing.T) {
	hybrid, inner, _, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	seedLegacyLog(t, inner, "bf-old", time.Now().UTC().Add(-48*time.Hour), false)
	seedLegacyLog(t, inner, "bf-new", time.Now().UTC().Add(-30*time.Minute), false)

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{OlderThan: 24 * time.Hour})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Migrated, "only the old row migrates")

	oldRow, err := inner.FindByID(ctx, "bf-old")
	require.NoError(t, err)
	assert.True(t, oldRow.HasObject)

	newRow, err := inner.FindByID(ctx, "bf-new")
	require.NoError(t, err)
	assert.False(t, newRow.HasObject, "recent rows stay untouched")
}

func TestBackfill_UploadFailureLeavesRowIntact(t *testing.T) {
	hybrid, inner, objStore, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	objStore.PutErr = assert.AnError
	seedLegacyLog(t, inner, "bf-fail", time.Now().UTC().Add(-24*time.Hour), false)

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	require.NoError(t, err, "row failures are reported, not fatal")
	assert.Equal(t, int64(1), result.Failed)
	require.Len(t, result.Errors, 1)

	// DB row untouched: nothing is lost.
	row, err := inner.FindByID(ctx, "bf-fail")
	require.NoError(t, err)
	assert.False(t, row.HasObject)
	assert.NotEmpty(t, row.RawRequest, "payload stays in the DB when upload fails")
}

func TestBackfill_MCPToolLogs(t *testing.T) {
	hybrid, inner, objStore, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	old := time.Now().UTC().Add(-24 * time.Hour)
	mcp := &MCPToolLog{
		ID:        "bf-mcp-1",
		Timestamp: old,
		ToolName:  "search",
		Status:    "success",
		ArgumentsParsed: map[string]any{
			"query": "hello",
		},
		ResultParsed: map[string]any{
			"hits": 42,
		},
	}
	require.NoError(t, mcp.SerializeFields())
	require.NoError(t, inner.CreateMCPToolLog(ctx, mcp))

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Migrated)
	require.Equal(t, 1, objStore.Len())

	// Object holds the full log.
	raw, err := objStore.Get(ctx, MCPToolObjectKey("bf", old, "bf-mcp-1"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"hits":42`)

	// DB row is the lightweight form.
	row, err := inner.FindMCPToolLog(ctx, "bf-mcp-1")
	require.NoError(t, err)
	assert.True(t, row.HasObject)
	assert.Empty(t, row.Result, "result must be cleared from the DB row")
	assert.NotEmpty(t, row.Arguments, "a short arguments preview stays for the table")

	// Reads hydrate the full result back.
	found, err := hybrid.FindMCPToolLog(ctx, "bf-mcp-1")
	require.NoError(t, err)
	assert.Contains(t, found.Result, `"hits":42`)
}

func TestBackfill_ProgressAndSummary(t *testing.T) {
	hybrid, inner, _, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		seedLegacyLog(t, inner, "bf-many-"+string(rune('a'+i)), time.Now().UTC().Add(-24*time.Hour), false)
	}

	var lastResult BackfillResult
	calls := 0
	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{
		BatchSize: 2,
		Progress: func(r BackfillResult) {
			lastResult = r
			calls++
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(5), result.Migrated)
	assert.Equal(t, int64(5), lastResult.Migrated, "progress callback reports running totals")
	assert.Greater(t, calls, 0)
}

func TestBackfill_ContextCancellation(t *testing.T) {
	hybrid, inner, _, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	seedLegacyLog(t, inner, "bf-cancel", time.Now().UTC().Add(-24*time.Hour), false)

	ctx, cancel := context.WithCancel(ctx)
	cancel()

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	// Cancellation is graceful: either a clean result or a context error, never
	// a corrupt state. The row either fully migrated or stayed untouched.
	if err == nil {
		assert.Zero(t, result.Failed)
	}
	row, err2 := inner.FindByID(context.Background(), "bf-cancel")
	require.NoError(t, err2)
	if row.HasObject {
		t.Log("row migrated before cancellation took effect")
	}
}

func TestBackfill_DryRunDoesNotModifyMCPLogs(t *testing.T) {
	hybrid, inner, objStore, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	mcp := &MCPToolLog{
		ID:        "bf-mcp-dry",
		Timestamp: time.Now().UTC().Add(-24 * time.Hour),
		ToolName:  "search",
		Status:    "success",
		ArgumentsParsed: map[string]any{
			"query": "hello",
		},
		ResultParsed: map[string]any{
			"hits": 42,
		},
	}
	require.NoError(t, inner.CreateMCPToolLog(ctx, mcp))

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Migrated)
	assert.Zero(t, objStore.Len(), "dry-run must not upload MCP payloads")

	row, err := inner.FindMCPToolLog(ctx, mcp.ID)
	require.NoError(t, err)
	assert.False(t, row.HasObject)
	assert.NotEmpty(t, row.Result, "dry-run must leave MCP payloads in the DB")
}

func TestBackfill_PaginatesRowsWithIdenticalTimestamps(t *testing.T) {
	hybrid, inner, _, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	ts := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	for i := 0; i < 5; i++ {
		seedLegacyLog(t, inner, fmt.Sprintf("bf-tie-%d", i), ts, false)
	}

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{BatchSize: 2, Concurrency: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(5), result.Migrated)
	assert.Zero(t, result.Failed)
	for i := 0; i < 5; i++ {
		row, err := inner.FindByID(ctx, fmt.Sprintf("bf-tie-%d", i))
		require.NoError(t, err)
		assert.True(t, row.HasObject)
	}
}

func TestBackfill_ConditionalRewriteDoesNotClobberMigratedRow(t *testing.T) {
	hybrid, inner, _, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	entry := seedLegacyLog(t, inner, "bf-race", time.Now().UTC().Add(-24*time.Hour), false)
	stale := *entry
	require.NoError(t, stale.DeserializeFields())
	prepareDBEntry(&stale, nil)

	// Simulate the online offload path winning after the backfill read.
	require.NoError(t, inner.Update(ctx, entry.ID, map[string]interface{}{
		"has_object":   true,
		"raw_response": `{"newer":true}`,
	}))

	updated, err := hybrid.rewriteLogRow(ctx, entry.ID, &stale)
	require.NoError(t, err)
	assert.False(t, updated)

	row, err := inner.FindByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, `{"newer":true}`, row.RawResponse, "stale backfill data must not overwrite the winning writer")
}
