package logstore

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBackfillHybrid(t *testing.T, excludeFields []string) (*HybridLogStore, LogStore, *objectstore.InMemoryObjectStore, func()) {
	t.Helper()
	return newBackfillHybridWithOptions(t, excludeFields, nil)
}

func newBackfillHybridWithOptions(t *testing.T, excludeFields, excludeRequestTypes []string) (*HybridLogStore, LogStore, *objectstore.InMemoryObjectStore, func()) {
	t.Helper()
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "backfill.db")}, hybridTestLogger{})
	require.NoError(t, err)
	objStore := objectstore.NewInMemoryObjectStore()
	hybrid := newHybridLogStore(inner, objStore, "bf", hybridTestLogger{}, excludeFields, excludeRequestTypes)
	cleanup := func() { _ = hybrid.Close(context.Background()) }
	return hybrid, inner, objStore, cleanup
}

type blockingBackfillObjectStore struct {
	*objectstore.InMemoryObjectStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingBackfillObjectStore) Put(ctx context.Context, key string, data []byte, tags map[string]string) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return s.InMemoryObjectStore.Put(ctx, key, data, tags)
}

type stalledBackfillReadStore struct {
	*RDBLogStore
}

func (s *stalledBackfillReadStore) FindByID(ctx context.Context, id string) (*Log, error) {
	<-ctx.Done()
	return nil, ctx.Err()
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

func TestBackfill_ClearsAllOffloadedPayloadColumns(t *testing.T) {
	cases := []struct {
		name     string
		excluded []string
		resident bool
	}{
		{name: "offloaded"},
		{name: "excluded", excluded: []string{"embedding_input", "live_session", "plugin_logs"}, resident: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hybrid, inner, objStore, cleanup := newBackfillHybrid(t, tc.excluded)
			defer cleanup()
			ctx := context.Background()

			entry := seedLegacyLog(t, inner, "bf-all-payloads-"+tc.name, time.Now().UTC().Add(-24*time.Hour), false)
			require.NoError(t, inner.Update(ctx, entry.ID, map[string]interface{}{
				"embedding_input": `["embedding-secret"]`,
				"live_session":    `{"session":"live-secret"}`,
				"plugin_logs":     `{"plugin":"plugin-secret"}`,
			}))

			result, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
			require.NoError(t, err)
			assert.Equal(t, int64(1), result.Migrated)

			row, err := inner.FindByID(ctx, entry.ID)
			require.NoError(t, err)
			raw, err := objStore.Get(ctx, ObjectKey("bf", entry.Timestamp, entry.ID))
			require.NoError(t, err)
			if tc.resident {
				assert.Contains(t, row.EmbeddingInput, "embedding-secret")
				assert.Contains(t, row.LiveSession, "live-secret")
				assert.Contains(t, row.PluginLogs, "plugin-secret")
				assert.NotContains(t, string(raw), "embedding-secret")
				assert.NotContains(t, string(raw), "live-secret")
				assert.NotContains(t, string(raw), "plugin-secret")
			} else {
				assert.Empty(t, row.EmbeddingInput)
				assert.Empty(t, row.LiveSession)
				assert.Empty(t, row.PluginLogs)
				assert.Contains(t, string(raw), "embedding-secret")
				assert.Contains(t, string(raw), "live-secret")
				assert.Contains(t, string(raw), "plugin-secret")
			}
		})
	}
}

func TestBackfill_RespectsExcludedRequestTypes(t *testing.T) {
	hybrid, inner, objStore, cleanup := newBackfillHybridWithOptions(t, nil, []string{"chat.completion"})
	defer cleanup()
	ctx := context.Background()

	seedLegacyLog(t, inner, "bf-excluded-type", time.Now().UTC().Add(-24*time.Hour), false)
	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Skipped)
	assert.Zero(t, result.Migrated)
	assert.Zero(t, objStore.Len())

	row, err := inner.FindByID(ctx, "bf-excluded-type")
	require.NoError(t, err)
	assert.False(t, row.HasObject)
	assert.NotEmpty(t, row.RawRequest)
	assert.NotEmpty(t, row.OutputMessage)
}

func TestBackfill_MovesErrorRawBodiesToObject(t *testing.T) {
	hybrid, inner, objStore, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	entry := &Log{
		ID:           "bf-error-raw",
		Timestamp:    time.Now().UTC().Add(-24 * time.Hour),
		Provider:     "openai",
		Model:        "gpt-4o",
		Status:       "error",
		Object:       "chat.completion",
		ErrorDetails: `{"error":{"message":"provider failed"},"extra_fields":{"raw_request":"secret-request","raw_response":"secret-response"}}`,
	}
	require.NoError(t, inner.Create(ctx, entry))

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Migrated)

	row, err := inner.FindByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.True(t, row.HasObject)
	assert.Contains(t, row.ErrorDetails, "provider failed")
	assert.NotContains(t, row.ErrorDetails, "secret-request")
	assert.NotContains(t, row.ErrorDetails, "secret-response")

	raw, err := objStore.Get(ctx, ObjectKey("bf", entry.Timestamp, entry.ID))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "secret-request")
	assert.Contains(t, string(raw), "secret-response")

	hydrated, err := hybrid.FindByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Contains(t, hydrated.ErrorDetails, "secret-request")
	assert.Contains(t, hydrated.ErrorDetails, "secret-response")
}

func TestBackfill_HandlesContentHiddenRows(t *testing.T) {
	hybrid, inner, objStore, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()

	old := time.Now().UTC().Add(-24 * time.Hour)
	seedLegacyLog(t, inner, "bf-hidden", old, true)

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Migrated)
	require.Equal(t, 1, objStore.Len())

	// The object holds the FULL payload (hidden rows are never filtered).
	raw, err := objStore.Get(ctx, ObjectKey("bf", old, "bf-hidden"))
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

func TestBackfill_ProgressReportsOnlyLatestError(t *testing.T) {
	hybrid, inner, objStore, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx := context.Background()
	objStore.PutErr = assert.AnError

	for i := 0; i < 3; i++ {
		seedLegacyLog(t, inner, fmt.Sprintf("bf-progress-fail-%d", i), time.Now().UTC().Add(-24*time.Hour), false)
	}

	errorCallbacks := 0
	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{
		Concurrency: 3,
		Progress: func(progress BackfillResult) {
			assert.LessOrEqual(t, len(progress.Errors), 1)
			if len(progress.Errors) == 1 {
				errorCallbacks++
			}
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), result.Failed)
	assert.Len(t, result.Errors, 3)
	assert.Equal(t, 3, errorCallbacks)
}

func TestBackfill_ContextCancellationFinishesAcceptedWork(t *testing.T) {
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "cancel.db")}, hybridTestLogger{})
	require.NoError(t, err)
	objects := &blockingBackfillObjectStore{
		InMemoryObjectStore: objectstore.NewInMemoryObjectStore(),
		started:             make(chan struct{}),
		release:             make(chan struct{}),
	}
	hybrid := newHybridLogStore(inner, objects, "bf", hybridTestLogger{}, nil, nil)
	defer hybrid.Close(context.Background())

	ts := time.Now().UTC().Add(-24 * time.Hour)
	seedLegacyLog(t, inner, "bf-cancel-1", ts, false)
	seedLegacyLog(t, inner, "bf-cancel-2", ts.Add(time.Second), false)

	runCtx, cancel := context.WithCancel(ctx)
	type outcome struct {
		result *BackfillResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := hybrid.BackfillObjects(runCtx, BackfillOptions{BatchSize: 2, Concurrency: 1})
		done <- outcome{result: result, err: err}
	}()

	select {
	case <-objects.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first upload")
	}
	cancel()
	close(objects.release)

	select {
	case got := <-done:
		require.ErrorIs(t, got.err, context.Canceled)
		require.NotNil(t, got.result)
		assert.Equal(t, int64(1), got.result.Migrated)
		assert.Zero(t, got.result.Failed, "accepted work must finish without cancellation failures")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cancellation")
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

func TestBackfill_ZeroTimestampCursorAdvances(t *testing.T) {
	hybrid, inner, _, cleanup := newBackfillHybrid(t, nil)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i := 0; i < 2; i++ {
		entry := &Log{
			ID:        fmt.Sprintf("bf-zero-%d", i),
			Timestamp: time.Now().UTC(),
			Provider:  "openai",
			Model:     "gpt-4o",
			Status:    "success",
			Object:    "chat.completion",
		}
		require.NoError(t, inner.Create(ctx, entry))
		require.NoError(t, inner.Update(ctx, entry.ID, map[string]interface{}{"timestamp": time.Time{}}))
	}

	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{BatchSize: 1, Concurrency: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(2), result.Skipped)
	assert.Zero(t, result.Migrated)
}

func TestBackfill_ReadTimeoutBoundsAcceptedWork(t *testing.T) {
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "read-timeout.db")}, hybridTestLogger{})
	require.NoError(t, err)
	seedLegacyLog(t, inner, "bf-read-timeout", time.Now().UTC().Add(-24*time.Hour), false)

	objects := objectstore.NewInMemoryObjectStore()
	hybrid := newHybridLogStore(&stalledBackfillReadStore{RDBLogStore: inner}, objects, "bf", hybridTestLogger{}, nil, nil)
	defer hybrid.Close(context.Background())

	originalTimeout := backfillReadTimeout
	backfillReadTimeout = 20 * time.Millisecond
	defer func() { backfillReadTimeout = originalTimeout }()

	started := time.Now()
	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{Concurrency: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Failed)
	require.Len(t, result.Errors, 1)
	assert.ErrorIs(t, result.Errors[0].Err, context.DeadlineExceeded)
	assert.Less(t, time.Since(started), time.Second)
}

func TestBackfill_ClickHouseDryRunCursorKeepsMilliseconds(t *testing.T) {
	store := trySetupClickHouseStore(t)
	objects := objectstore.NewInMemoryObjectStore()
	hybrid := newHybridLogStore(store, objects, "bf", hybridTestLogger{}, nil, nil)
	t.Cleanup(func() { _ = hybrid.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	second := time.Now().UTC().Truncate(time.Second)
	for i, offset := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond} {
		entry := chTestLog(fmt.Sprintf("bf-ch-cursor-%d", i), second.Add(offset))
		require.NoError(t, store.CreateIfNotExists(ctx, entry))
	}
	includeMCP := false
	result, err := hybrid.BackfillObjects(ctx, BackfillOptions{
		BatchSize:   1,
		Concurrency: 1,
		DryRun:      true,
		IncludeMCP:  &includeMCP,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), result.Skipped)
	assert.Zero(t, result.Migrated)
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
