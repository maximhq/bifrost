package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hybridTestLogger struct{}

func (hybridTestLogger) Debug(string, ...any)                                  {}
func (hybridTestLogger) Info(string, ...any)                                   {}
func (hybridTestLogger) Warn(string, ...any)                                   {}
func (hybridTestLogger) Error(string, ...any)                                  {}
func (hybridTestLogger) Fatal(string, ...any)                                  {}
func (hybridTestLogger) SetLevel(schemas.LogLevel)                             {}
func (hybridTestLogger) SetOutputType(schemas.LoggerOutputType)                {}
func (hybridTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func newTestHybrid(t *testing.T) (*HybridLogStore, LogStore, *objectstore.InMemoryObjectStore) {
	t.Helper()
	ctx := context.Background()

	// Create SQLite inner store.
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: ":memory:"}, hybridTestLogger{})
	require.NoError(t, err)

	objStore := objectstore.NewInMemoryObjectStore()
	hybrid := newHybridLogStore(inner, objStore, "test", hybridTestLogger{})
	return hybrid, inner, objStore
}

func waitForUploads(h *HybridLogStore) {
	// Give upload workers a moment to process queued work.
	// This is a test helper; in production, Close() drains the queue.
	time.Sleep(200 * time.Millisecond)
}

func TestHybrid_CreateAndFindByID(t *testing.T) {
	hybrid, _, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	inputContent := "Hello, how are you?"
	entry := &Log{
		ID:        "log-1",
		Timestamp: time.Now().UTC(),
		Provider:  "anthropic",
		Model:     "claude-3-sonnet",
		Status:    "success",
		Object:    "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &inputContent}},
		},
		OutputMessageParsed: &schemas.ChatMessage{
			Content: &schemas.ChatMessageContent{ContentStr: strPtr("I'm fine, thanks!")},
		},
	}

	// Serialize fields so TEXT columns are populated (simulating what GORM BeforeCreate does).
	require.NoError(t, entry.SerializeFields())

	err := hybrid.CreateIfNotExists(ctx, entry)
	require.NoError(t, err)

	waitForUploads(hybrid)

	// Verify object was uploaded.
	assert.Equal(t, 1, objStore.Len(), "expected 1 object in store")

	// FindByID should return hydrated log with payload.
	found, err := hybrid.FindByID(ctx, "log-1")
	require.NoError(t, err)
	assert.Equal(t, "log-1", found.ID)
	assert.True(t, found.HasObject)
	assert.NotEmpty(t, found.InputHistory, "InputHistory should be hydrated from S3")
	assert.NotEmpty(t, found.OutputMessage, "OutputMessage should be hydrated from S3")

	// Content summary should contain input text but the output should be in the payload.
	assert.Contains(t, found.ContentSummary, "Hello, how are you?")
}

func TestHybrid_EmptyPayloadSkipsUpload(t *testing.T) {
	hybrid, _, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := &Log{
		ID:        "log-processing",
		Timestamp: time.Now().UTC(),
		Provider:  "openai",
		Model:     "gpt-4",
		Status:    "processing",
		Object:    "chat.completion",
	}

	err := hybrid.CreateIfNotExists(ctx, entry)
	require.NoError(t, err)

	waitForUploads(hybrid)

	// No upload when all payload fields are empty (e.g. initial "processing" entries).
	assert.Equal(t, 0, objStore.Len(), "empty-payload entries should not be uploaded")
}

func TestHybrid_BatchCreateIfNotExists(t *testing.T) {
	hybrid, _, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entries := make([]*Log, 3)
	for i := 0; i < 3; i++ {
		content := "input message"
		entries[i] = &Log{
			ID:        "batch-" + string(rune('a'+i)),
			Timestamp: time.Now().UTC(),
			Provider:  "anthropic",
			Model:     "claude-3",
			Status:    "success",
			Object:    "chat.completion",
			InputHistoryParsed: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &content}},
			},
		}
		require.NoError(t, entries[i].SerializeFields())
	}

	err := hybrid.BatchCreateIfNotExists(ctx, entries)
	require.NoError(t, err)

	waitForUploads(hybrid)
	assert.Equal(t, 3, objStore.Len())
}

func TestHybrid_FindByID_NoObject(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	// Insert directly into inner store (simulating legacy data without object).
	entry := &Log{
		ID:           "legacy-1",
		Timestamp:    time.Now().UTC(),
		Provider:     "openai",
		Model:        "gpt-4",
		Status:       "success",
		Object:       "chat.completion",
		InputHistory: `[{"role":"user","content":"legacy input"}]`,
		HasObject:    false,
	}
	require.NoError(t, inner.CreateIfNotExists(ctx, entry))

	found, err := hybrid.FindByID(ctx, "legacy-1")
	require.NoError(t, err)
	assert.False(t, found.HasObject)
	// Legacy data: payload is in DB.
	assert.NotEmpty(t, found.InputHistory)
}

func TestHybrid_FindByID_GracefulDegradation(t *testing.T) {
	hybrid, _, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	content := "test input"
	entry := &Log{
		ID:        "degrade-1",
		Timestamp: time.Now().UTC(),
		Provider:  "anthropic",
		Model:     "claude-3",
		Status:    "success",
		Object:    "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &content}},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForUploads(hybrid)

	// Simulate S3 failure.
	objStore.GetErr = assert.AnError

	found, err := hybrid.FindByID(ctx, "degrade-1")
	require.NoError(t, err, "FindByID should succeed even when S3 fails")
	assert.True(t, found.HasObject)
	// When S3 fails, the DB data is returned. The DB retains the last message
	// in input_history for list views, so it won't be empty.
	assert.NotEmpty(t, found.InputHistory, "last message should be retained in DB")
	// But other payload fields (output_message, params, etc.) should be empty.
	assert.Empty(t, found.OutputMessage, "output should be empty when S3 fails")
}

func TestHybrid_PutFailureDropsUpload(t *testing.T) {
	hybrid, _, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	// Simulate S3 write failure.
	objStore.PutErr = assert.AnError

	content := "important input"
	entry := &Log{
		ID:        "put-fail-1",
		Timestamp: time.Now().UTC(),
		Provider:  "anthropic",
		Model:     "claude-3",
		Status:    "success",
		Object:    "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &content}},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForUploads(hybrid)

	// Upload should have been dropped.
	assert.Equal(t, 0, objStore.Len(), "no object should be stored when Put fails")
	assert.Equal(t, int64(1), hybrid.DroppedUploads(), "dropped upload should be counted")

	// DB row exists but has_object remains false since the upload failed.
	found, err := hybrid.FindByID(ctx, "put-fail-1")
	require.NoError(t, err)
	assert.False(t, found.HasObject, "has_object should remain false when upload fails")
}

func TestHybrid_DeleteLog(t *testing.T) {
	hybrid, _, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := &Log{
		ID:           "del-1",
		Timestamp:    time.Now().UTC(),
		Provider:     "anthropic",
		Model:        "claude-3",
		Status:       "success",
		Object:       "chat.completion",
		InputHistory: `[{"role":"user","content":"delete me"}]`,
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForUploads(hybrid)
	assert.Equal(t, 1, objStore.Len())

	err := hybrid.DeleteLog(ctx, "del-1")
	require.NoError(t, err)

	// Object should be deleted from S3.
	assert.Equal(t, 0, objStore.Len())

	// DB should also be empty.
	_, err = hybrid.FindByID(ctx, "del-1")
	assert.Error(t, err)
}

func TestHybrid_Tags(t *testing.T) {
	hybrid, _, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	ts := time.Date(2026, 4, 3, 14, 30, 0, 0, time.UTC)
	vkID := "vk_test"
	entry := &Log{
		ID:           "tag-1",
		Timestamp:    ts,
		Provider:     "anthropic",
		Model:        "claude-3",
		Status:       "error",
		Object:       "chat.completion",
		VirtualKeyID: &vkID,
		Stream:       true,
		InputHistory: `[{"role":"user","content":"test"}]`,
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForUploads(hybrid)

	key := ObjectKey("test", ts, "tag-1")
	tags := objStore.GetTags(key)
	assert.Equal(t, "anthropic", tags["provider"])
	assert.Equal(t, "error", tags["status"])
	assert.Equal(t, "true", tags["has_error"])
	assert.Equal(t, "true", tags["stream"])
	assert.Equal(t, "vk_test", tags["virtual_key_id"])
	assert.Equal(t, "2026-04-03", tags["date"])
}

func TestHybrid_ContentSummaryIsInputOnly(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	inputText := "What is the capital of France?"
	outputText := "The capital of France is Paris."
	entry := &Log{
		ID:        "summary-1",
		Timestamp: time.Now().UTC(),
		Provider:  "anthropic",
		Model:     "claude-3",
		Status:    "success",
		Object:    "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &inputText}},
		},
		OutputMessageParsed: &schemas.ChatMessage{
			Content: &schemas.ChatMessageContent{ContentStr: &outputText},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))

	// Read from inner DB to check content_summary.
	dbLog, err := inner.FindByID(ctx, "summary-1")
	require.NoError(t, err)
	assert.Contains(t, dbLog.ContentSummary, "capital of France")
	assert.NotContains(t, dbLog.ContentSummary, "Paris", "content_summary should not contain output text")
}
