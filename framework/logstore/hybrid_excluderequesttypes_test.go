package logstore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestHybridWithExcludedRequestTypes creates a hybrid store that keeps the selected request
// types in the database.
func newTestHybridWithExcludedRequestTypes(t *testing.T, requestTypes []string) (*HybridLogStore, LogStore, *objectstore.InMemoryObjectStore) {
	t.Helper()
	inner, err := newSqliteLogStore(context.Background(), &SQLiteConfig{Path: filepath.Join(t.TempDir(), "hybrid.db")}, hybridTestLogger{})
	require.NoError(t, err)
	objStore := objectstore.NewInMemoryObjectStore()
	hybrid, err := newHybridLogStore(inner, objStore, "test", hybridTestLogger{}, nil, requestTypes)
	require.NoError(t, err)
	return hybrid, inner, objStore
}

func newRequestTypeTestLog(t *testing.T, id, object string) *Log {
	t.Helper()
	input := "hello " + id
	entry := &Log{
		ID:        id,
		Timestamp: time.Now().UTC(),
		Provider:  "openai",
		Model:     "gpt-4o",
		Status:    "success",
		Object:    object,
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &input}},
		},
		OutputMessageParsed: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: strPtr("output " + id)},
		},
		RawResponse: `{"data":[{"id":"gpt-4o"}]}`,
	}
	require.NoError(t, entry.SerializeFields())
	return entry
}

// assertKeptInDB checks the row was written in full and never offloaded.
func assertKeptInDB(t *testing.T, hybrid *HybridLogStore, inner LogStore, id string) {
	t.Helper()
	ctx := context.Background()
	row, err := inner.FindByID(ctx, id)
	require.NoError(t, err)
	assert.False(t, row.HasObject, "excluded request type must not be offloaded")
	assert.Equal(t, `{"data":[{"id":"gpt-4o"}]}`, row.RawResponse)

	found, err := hybrid.FindByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, found.OutputMessageParsed)
	require.NotNil(t, found.OutputMessageParsed.Content.ContentStr)
	assert.Equal(t, "output "+id, *found.OutputMessageParsed.Content.ContentStr)
}

func TestHybrid_ExcludeRequestTypes_Create(t *testing.T) {
	hybrid, inner, objStore := newTestHybridWithExcludedRequestTypes(t, []string{" list_models ", ""})
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	require.NoError(t, hybrid.Create(ctx, newRequestTypeTestLog(t, "lm-1", string(schemas.ListModelsRequest))))
	require.NoError(t, hybrid.CreateIfNotExists(ctx, newRequestTypeTestLog(t, "lm-2", string(schemas.ListModelsRequest))))
	require.NoError(t, hybrid.Create(ctx, newRequestTypeTestLog(t, "chat-1", string(schemas.ChatCompletionRequest))))

	waitForOffload(t, inner, "chat-1")
	assert.Equal(t, 1, objStore.Len(), "only the non-excluded log should reach object storage")

	assertKeptInDB(t, hybrid, inner, "lm-1")
	assertKeptInDB(t, hybrid, inner, "lm-2")

	chatRow, err := inner.FindByID(ctx, "chat-1")
	require.NoError(t, err)
	assert.Empty(t, chatRow.RawResponse, "non-excluded rows still have their payload offloaded")
}

func TestHybrid_ExcludeRequestTypes_BatchCreate(t *testing.T) {
	hybrid, inner, objStore := newTestHybridWithExcludedRequestTypes(t, []string{string(schemas.ListModelsRequest)})
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	chat1 := newRequestTypeTestLog(t, "chat-1", string(schemas.ChatCompletionRequest))
	chat2 := newRequestTypeTestLog(t, "chat-2", string(schemas.ChatCompletionRequest))
	entries := []*Log{
		newRequestTypeTestLog(t, "lm-1", string(schemas.ListModelsRequest)),
		chat1,
		nil,
		newRequestTypeTestLog(t, "lm-2", string(schemas.ListModelsRequest)),
		chat2,
	}
	require.NoError(t, hybrid.BatchCreateIfNotExists(ctx, entries))

	// Summaries are copied back to the matching caller entries, not shifted by the skipped rows.
	assert.Equal(t, "hello chat-1", chat1.ContentSummary)
	assert.Equal(t, "hello chat-2", chat2.ContentSummary)

	waitForOffload(t, inner, "chat-1")
	waitForOffload(t, inner, "chat-2")
	assert.Equal(t, 2, objStore.Len())
	for _, key := range objStore.Keys() {
		var payload map[string]string
		data, err := objStore.Get(ctx, key)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &payload))
		assert.NotContains(t, payload["output_message"], "lm-", "list_models payload must not be uploaded")
	}

	assertKeptInDB(t, hybrid, inner, "lm-1")
	assertKeptInDB(t, hybrid, inner, "lm-2")
}

func TestConfig_UnmarshalObjectStorageExcludeRequestTypes(t *testing.T) {
	var cfg Config
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":false,"object_storage_exclude_request_types":["list_models"]}`), &cfg))
	assert.Equal(t, []string{"list_models"}, cfg.ObjectStorageExcludeRequestTypes)
}

func TestHybrid_ExcludeRequestTypes_VisibleLogKeepsSearchSummary(t *testing.T) {
	hybrid, inner, _ := newTestHybridWithExcludedRequestTypes(t, []string{string(schemas.ListModelsRequest)})
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	require.NoError(t, hybrid.Create(ctx, newRequestTypeTestLog(t, "lm-1", string(schemas.ListModelsRequest))))
	require.NoError(t, hybrid.BatchCreateIfNotExists(ctx, []*Log{newRequestTypeTestLog(t, "lm-2", string(schemas.ListModelsRequest))}))

	for _, id := range []string{"lm-1", "lm-2"} {
		row, err := inner.FindByID(ctx, id)
		require.NoError(t, err)
		assert.Contains(t, row.ContentSummary, "hello "+id, "excluded logs must stay searchable by message text")
	}
}

// Hidden logs ignore the request-type exclusion, as they ignore the field
// exclusion list: content is offloaded as hidden and never kept in the DB row.
func TestHybrid_ExcludeRequestTypes_HiddenContentStillOffloaded(t *testing.T) {
	hybrid, inner, objStore := newTestHybridWithExcludedRequestTypes(t, []string{"chat.completion"})
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	single := newContentHiddenTestEntry("hidden-1")
	single.ContentHidden = true
	require.NoError(t, hybrid.Create(ctx, single))
	batched := newContentHiddenTestEntry("hidden-2")
	batched.ContentHidden = true
	require.NoError(t, hybrid.BatchCreateIfNotExists(ctx, []*Log{batched}))

	for _, id := range []string{"hidden-1", "hidden-2"} {
		waitForOffload(t, inner, id)
		row, err := inner.FindByID(ctx, id)
		require.NoError(t, err)
		assert.True(t, row.ContentHidden)
		assert.True(t, row.HasObject, "hidden content must be retained in object storage")
		assert.Empty(t, row.InputHistory)
		assert.Empty(t, row.OutputMessage)
		assert.Empty(t, row.EmbeddingInput)
		assert.Empty(t, row.ContentSummary)
	}
	assert.Equal(t, 2, objStore.Len())
	for _, key := range objStore.Keys() {
		data, err := objStore.Get(ctx, key)
		require.NoError(t, err)
		var payload map[string]string
		require.NoError(t, json.Unmarshal(data, &payload))
		assert.NotEmpty(t, payload["output_message"], "hidden payload must be uploaded in full")
	}
	// The caller's entry is not mutated.
	assert.NotNil(t, single.OutputMessageParsed)
}
