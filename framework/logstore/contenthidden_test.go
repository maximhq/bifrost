package logstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newContentHiddenTestEntry(id string) *Log {
	input := "sensitive question"
	return &Log{
		ID:        id,
		Timestamp: time.Now().UTC(),
		Provider:  "anthropic",
		Model:     "claude-3-sonnet",
		Status:    "success",
		Object:    "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &input}},
		},
		OutputMessageParsed: &schemas.ChatMessage{
			Content: &schemas.ChatMessageContent{ContentStr: strPtr("sensitive answer")},
		},
	}
}

func TestHybrid_ContentHiddenStripsDBRowAndSkipsHydration(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := newContentHiddenTestEntry("hidden-1")
	entry.ContentHidden = true
	require.NoError(t, entry.SerializeFields())

	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForUploads(t, func() bool { return objStore.Len() == 1 })

	// The DB row must hold no content at all: no payload fields, no summary,
	// no last-user-message preview.
	dbRow, err := inner.FindByID(ctx, "hidden-1")
	require.NoError(t, err)
	assert.True(t, dbRow.ContentHidden)
	assert.True(t, dbRow.HasObject)
	assert.Empty(t, dbRow.InputHistory)
	assert.Empty(t, dbRow.OutputMessage)
	assert.Empty(t, dbRow.ContentSummary)

	// The full payload must be in object storage.
	data, err := objStore.Get(ctx, ObjectKey("test", entry.Timestamp, "hidden-1"))
	require.NoError(t, err)
	var payload map[string]string
	require.NoError(t, sonic.Unmarshal(data, &payload))
	assert.Contains(t, payload["input_history"], "sensitive question")
	assert.Contains(t, payload["output_message"], "sensitive answer")

	// Reads through the hybrid store must not hydrate the payload back.
	found, err := hybrid.FindByID(ctx, "hidden-1")
	require.NoError(t, err)
	assert.True(t, found.ContentHidden)
	assert.Empty(t, found.InputHistory, "hidden log must not be hydrated")
	assert.Empty(t, found.OutputMessage, "hidden log must not be hydrated")
	assert.Empty(t, found.ContentSummary)
}

func TestHybrid_ContentHiddenIgnoresExclusionList(t *testing.T) {
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "hybrid.db")}, hybridTestLogger{})
	require.NoError(t, err)
	objStore := objectstore.NewInMemoryObjectStore()
	// params is configured to stay DB-resident and out of the object payload.
	hybrid := newHybridLogStore(inner, objStore, "test", hybridTestLogger{}, []string{"params"})
	defer hybrid.Close(ctx)

	normal := newContentHiddenTestEntry("normal-1")
	normal.ParamsParsed = map[string]any{"temperature": 0.5}
	require.NoError(t, normal.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, normal))

	hidden := newContentHiddenTestEntry("hidden-2")
	hidden.ContentHidden = true
	hidden.ParamsParsed = map[string]any{"temperature": 0.5}
	require.NoError(t, hidden.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, hidden))

	waitForUploads(t, func() bool { return objStore.Len() == 2 })

	// Normal entry: exclusion keeps params in the DB row and out of the object.
	normalRow, err := inner.FindByID(ctx, "normal-1")
	require.NoError(t, err)
	assert.NotEmpty(t, normalRow.Params)
	normalData, err := objStore.Get(ctx, ObjectKey("test", normal.Timestamp, "normal-1"))
	require.NoError(t, err)
	var normalPayload map[string]string
	require.NoError(t, sonic.Unmarshal(normalData, &normalPayload))
	assert.Empty(t, normalPayload["params"])

	// Hidden entry: the exclusion is ignored — params leaves the DB row and
	// lands in the object payload with everything else.
	hiddenRow, err := inner.FindByID(ctx, "hidden-2")
	require.NoError(t, err)
	assert.Empty(t, hiddenRow.Params)
	hiddenData, err := objStore.Get(ctx, ObjectKey("test", hidden.Timestamp, "hidden-2"))
	require.NoError(t, err)
	var hiddenPayload map[string]string
	require.NoError(t, sonic.Unmarshal(hiddenData, &hiddenPayload))
	assert.Contains(t, hiddenPayload["params"], "temperature")
}

func TestHybrid_ContentHiddenBatchMixed(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	visibleEntry := newContentHiddenTestEntry("batch-visible")
	hiddenEntry := newContentHiddenTestEntry("batch-hidden")
	hiddenEntry.ContentHidden = true
	require.NoError(t, visibleEntry.SerializeFields())
	require.NoError(t, hiddenEntry.SerializeFields())

	require.NoError(t, hybrid.BatchCreateIfNotExists(ctx, []*Log{visibleEntry, hiddenEntry}))
	waitForUploads(t, func() bool { return objStore.Len() == 2 })

	visibleRow, err := inner.FindByID(ctx, "batch-visible")
	require.NoError(t, err)
	assert.False(t, visibleRow.ContentHidden)
	assert.Contains(t, visibleRow.ContentSummary, "sensitive question")

	hiddenRow, err := inner.FindByID(ctx, "batch-hidden")
	require.NoError(t, err)
	assert.True(t, hiddenRow.ContentHidden)
	assert.Empty(t, hiddenRow.ContentSummary)
	assert.Empty(t, hiddenRow.InputHistory)

	// Hydrating reads: the visible log gets its payload back, the hidden one
	// stays metadata-only.
	foundVisible, err := hybrid.FindByID(ctx, "batch-visible")
	require.NoError(t, err)
	assert.NotEmpty(t, foundVisible.InputHistory)

	foundHidden, err := hybrid.FindByID(ctx, "batch-hidden")
	require.NoError(t, err)
	assert.Empty(t, foundHidden.InputHistory)
}

func TestHybrid_ContentHiddenProjectedReadsKeepFlag(t *testing.T) {
	hybrid, _, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := newContentHiddenTestEntry("hidden-proj")
	entry.ContentHidden = true
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForUploads(t, func() bool { return objStore.Len() == 1 })

	// A projection that requests payload fields but not content_hidden must
	// still not hydrate: ensureHydrationFields forces the flag into the
	// projection.
	found, err := hybrid.FindFirst(ctx, map[string]any{"id": "hidden-proj"}, "id", "input_history")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.True(t, found.ContentHidden)
	assert.Empty(t, found.InputHistory, "projected read of a hidden log must not hydrate")
}
