package logstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type hybridTestLogger struct{}

func (hybridTestLogger) Debug(string, ...any)                   {}
func (hybridTestLogger) Info(string, ...any)                    {}
func (hybridTestLogger) Warn(string, ...any)                    {}
func (hybridTestLogger) Error(string, ...any)                   {}
func (hybridTestLogger) Fatal(string, ...any)                   {}
func (hybridTestLogger) SetLevel(schemas.LogLevel)              {}
func (hybridTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (hybridTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func agentLogsCreateError(_ []string, err error) error {
	return err
}

// newTestHybrid creates a hybrid store with file-backed SQLite and in-memory object storage for
// asynchronous upload tests.
func newTestHybrid(t *testing.T) (*HybridLogStore, LogStore, *objectstore.InMemoryObjectStore) {
	t.Helper()
	ctx := context.Background()

	// Use a temp file instead of :memory: so async upload workers that use a
	// separate DB connection see the same schema.
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "hybrid.db")}, hybridTestLogger{})
	require.NoError(t, err)

	objStore := objectstore.NewInMemoryObjectStore()
	hybrid, err := newHybridLogStore(inner, objStore, "test", hybridTestLogger{}, nil, nil)
	require.NoError(t, err)
	return hybrid, inner, objStore
}

func waitForUploads(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for upload state")
}

// waitForOffload waits until the offload is fully complete for id: the payload is in
// object storage AND the row's has_object flag has been committed.
//
// processUpload does the Put first and only then updates has_object (with retries), so
// waiting on the object store alone returns while the DB flag is still false. Any test
// that reads has_object — or exercises a code path that branches on it, such as billing
// hydration — must wait for the flag, not just the object.
func waitForOffload(t *testing.T, inner LogStore, id string) {
	t.Helper()
	waitForUploads(t, func() bool {
		row, err := inner.FindByID(context.Background(), id)
		return err == nil && row.HasObject
	})
}

// waitForMCPOffload is waitForOffload for MCP tool logs: it waits for the row's
// has_object flag, which processUpload commits only after the object Put.
func waitForMCPOffload(t *testing.T, inner LogStore, id string) {
	t.Helper()
	waitForUploads(t, func() bool {
		row, err := inner.FindMCPToolLog(context.Background(), id)
		return err == nil && row.HasObject
	})
}

func TestHybridScopedDBDelegatesToInnerRDBStore(t *testing.T) {
	hybrid, _, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())

	scoped, ok := interface{}(hybrid).(scopedDBLogStore)
	require.True(t, ok, "hybrid logstore should preserve the RDB ScopedDB surface")

	called := false
	scope := queryscope.QueryScope(func(db *gorm.DB) *gorm.DB {
		called = true
		return db.Where("status = ?", "success")
	})
	ctx := queryscope.WithQueryScope(context.Background(), scope)

	got := scoped.ScopedDB(ctx)
	require.NotNil(t, got)
	stmt := got.Session(&gorm.Session{DryRun: true}).Table("logs").Find(&struct{}{}).Statement
	assert.True(t, called, "ScopedDB should invoke the scope from ctx")
	assert.Contains(t, stmt.SQL.String(), "status = ?")
}

func TestHybrid_CreateAndFindByID(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
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

	waitForOffload(t, inner, "log-1")

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

// error_type, error_code and status_code rankings and filters read the
// error_details column. Offloaded with the rest of the payload, it was blank
// on every row, so on a deployment with object storage on every error
// breakdown came back empty - 697 failed requests and no error types at all.
// It is small and is not request content, so it stays in the database.
func TestHybrid_ErrorDetailsStayQueryable(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	input := "hello"
	now := time.Now().UTC()
	entry := &Log{
		ID: "log-error", Timestamp: now, Provider: "openai", Model: "gpt-4o", Status: "error", Object: "chat.completion",
		InputHistoryParsed:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &input}}},
		OutputMessageParsed: &schemas.ChatMessage{Content: &schemas.ChatMessageContent{ContentStr: strPtr("partial reply")}},
		ErrorDetailsParsed: &schemas.BifrostError{
			StatusCode: new(429),
			Error:      &schemas.ErrorField{Type: new("rate_limit_error"), Code: new("rate_limit_exceeded"), Message: "slow down"},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForOffload(t, inner, "log-error")

	row, err := inner.FindByID(ctx, "log-error")
	require.NoError(t, err)
	assert.Contains(t, row.ErrorDetails, "rate_limit_error", "error_details must stay in the database row")
	assert.Empty(t, row.OutputMessage, "content is still offloaded")

	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	rankings, err := hybrid.GetDimensionRankings(ctx, SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionErrorType)
	require.NoError(t, err)
	require.Len(t, rankings.Rankings, 1)
	assert.Equal(t, "rate_limit_error", rankings.Rankings[0].ID)

	found, err := hybrid.SearchLogs(ctx, SearchFilters{StartTime: &start, EndTime: &end, ErrorTypes: []string{"rate_limit_error"}}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, found.Logs, 1)
}

// With content logging and raw storage on, error_details carries the provider's
// raw request and response bodies. Keeping error_details in the database for
// the error breakdowns must not keep those bodies there too: the row keeps the
// queryable error fields, object storage keeps the bodies, and a read returns
// both. Listing error_details in object_storage_exclude_fields still keeps it
// whole in the database, as that setting promises.
func TestHybrid_ErrorDetailsRawBodiesGoToObjectStorage(t *testing.T) {
	newEntry := func(id string) *Log {
		input := "hello"
		entry := &Log{
			ID: id, Timestamp: time.Now().UTC(), Provider: "openai", Model: "gpt-4o", Status: "error", Object: "chat.completion",
			InputHistoryParsed: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &input}}},
			ErrorDetailsParsed: &schemas.BifrostError{
				StatusCode: new(429),
				Error:      &schemas.ErrorField{Type: new("rate_limit_error"), Message: "slow down"},
				ExtraFields: schemas.BifrostErrorExtraFields{
					RawRequest:  "RAW-REQUEST-BODY",
					RawResponse: "RAW-RESPONSE-BODY",
				},
			},
		}
		require.NoError(t, entry.SerializeFields())
		return entry
	}

	t.Run("default keeps the bodies out of the row", func(t *testing.T) {
		hybrid, inner, objStore := newTestHybrid(t)
		defer hybrid.Close(context.Background())
		ctx := context.Background()

		entry := newEntry("log-error-raw")
		require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
		waitForOffload(t, inner, "log-error-raw")

		row, err := inner.FindByID(ctx, "log-error-raw")
		require.NoError(t, err)
		assert.Contains(t, row.ErrorDetails, "rate_limit_error", "the queryable error fields stay in the row")
		assert.NotContains(t, row.ErrorDetails, "RAW-REQUEST-BODY", "the raw request body must leave the row")
		assert.NotContains(t, row.ErrorDetails, "RAW-RESPONSE-BODY", "the raw response body must leave the row")

		object, err := objStore.Get(ctx, ObjectKey("test", entry.Timestamp, "log-error-raw"))
		require.NoError(t, err)
		assert.Contains(t, string(object), "RAW-REQUEST-BODY", "object storage keeps the raw request body")
		assert.Contains(t, string(object), "RAW-RESPONSE-BODY", "object storage keeps the raw response body")

		found, err := hybrid.FindByID(ctx, "log-error-raw")
		require.NoError(t, err)
		require.NotNil(t, found.ErrorDetailsParsed)
		assert.Equal(t, "RAW-REQUEST-BODY", found.ErrorDetailsParsed.ExtraFields.RawRequest)
		assert.Equal(t, "RAW-RESPONSE-BODY", found.ErrorDetailsParsed.ExtraFields.RawResponse)
		assert.Equal(t, "rate_limit_error", *found.ErrorDetailsParsed.Error.Type)
	})

	t.Run("excluding error_details keeps it whole in the row", func(t *testing.T) {
		hybrid, inner, _ := newTestHybridWithExclude(t, []string{"error_details"})
		defer hybrid.Close(context.Background())
		ctx := context.Background()

		require.NoError(t, hybrid.CreateIfNotExists(ctx, newEntry("log-error-raw-kept")))
		waitForOffload(t, inner, "log-error-raw-kept")

		row, err := inner.FindByID(ctx, "log-error-raw-kept")
		require.NoError(t, err)
		assert.Contains(t, row.ErrorDetails, "RAW-REQUEST-BODY")
		assert.Contains(t, row.ErrorDetails, "RAW-RESPONSE-BODY")
	})
}

// TestHybrid_EmbeddingInputOffloaded pins that embedding_input leaves the DB row and is hydrated from the object.
func TestHybrid_EmbeddingInputOffloaded(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	text := "embed me"
	entry := &Log{
		ID:        "emb-1",
		Timestamp: time.Now().UTC(),
		Provider:  "openai",
		Model:     "text-embedding-3-small",
		Status:    "success",
		Object:    "embedding",
		EmbeddingInputParsed: []schemas.EmbeddingInputItem{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForOffload(t, inner, "emb-1")

	dbRow, err := inner.FindByID(ctx, "emb-1")
	require.NoError(t, err)
	assert.Empty(t, dbRow.EmbeddingInput, "embedding_input must be offloaded, not kept in the DB row")

	found, err := hybrid.FindByID(ctx, "emb-1")
	require.NoError(t, err)
	require.Len(t, found.EmbeddingInputParsed, 1, "embedding_input should be hydrated from object storage")
	assert.Equal(t, text, *found.EmbeddingInputParsed[0].Content[0].Text)
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

	waitForUploads(t, func() bool { return len(hybrid.uploadQueue) == 0 })

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

	waitForUploads(t, func() bool { return objStore.Len() == 3 })
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
	hybrid, inner, objStore := newTestHybrid(t)
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
	waitForOffload(t, inner, "degrade-1")

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

func TestHybrid_CreateAndFindMCPToolLog(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	longInput := ""
	for i := 0; i < 260; i++ {
		longInput += "a"
	}
	entry := &MCPToolLog{
		ID:          "mcp-1",
		RequestID:   "req-1",
		Timestamp:   time.Now().UTC(),
		ToolName:    "echo",
		ServerLabel: "local",
		Status:      "success",
		ArgumentsParsed: map[string]any{
			"input": longInput,
		},
		ResultParsed: map[string]any{
			"ok": true,
		},
		RedactionMapping: `plain:{"input":{"EMAIL-1":"private@example.com"}}`,
	}

	require.NoError(t, hybrid.CreateMCPToolLog(ctx, entry))
	waitForUploads(t, func() bool { return objStore.Len() == 1 })

	dbOnly, err := inner.FindMCPToolLog(ctx, "mcp-1")
	require.NoError(t, err)
	assert.True(t, dbOnly.HasObject)
	assert.Empty(t, dbOnly.Result)
	assert.Nil(t, dbOnly.ResultParsed)
	assert.Equal(t, entry.RedactionMapping, dbOnly.RedactionMapping)
	preview, ok := dbOnly.ArgumentsParsed.(string)
	require.True(t, ok)
	assert.Len(t, []rune(preview), 200)

	found, err := hybrid.FindMCPToolLog(ctx, "mcp-1")
	require.NoError(t, err)
	assert.True(t, found.HasObject)
	assert.Equal(t, longInput, found.ArgumentsParsed.(map[string]interface{})["input"])
	assert.Equal(t, true, found.ResultParsed.(map[string]interface{})["ok"])
	assert.Equal(t, entry.RedactionMapping, found.RedactionMapping)
}

func TestHybrid_BatchCreateMCPToolLogsIfNotExists(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	longInput := ""
	for i := 0; i < 260; i++ {
		longInput += "b"
	}
	entries := []*MCPToolLog{
		{
			ID:          "mcp-batch-1",
			RequestID:   "req-batch-1",
			Timestamp:   time.Now().UTC(),
			ToolName:    "search",
			ServerLabel: "docs",
			Status:      "success",
			ArgumentsParsed: map[string]any{
				"query": longInput,
			},
			ResultParsed: map[string]any{
				"answer": "done",
			},
		},
		{
			ID:          "mcp-batch-2",
			RequestID:   "req-batch-2",
			Timestamp:   time.Now().UTC(),
			ToolName:    "echo",
			ServerLabel: "local",
			Status:      "error",
			ArgumentsParsed: map[string]any{
				"input": "short",
			},
			ErrorDetailsParsed: &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: "failed",
				},
			},
		},
	}

	require.NoError(t, hybrid.BatchCreateMCPToolLogsIfNotExists(ctx, entries))
	waitForMCPOffload(t, inner, "mcp-batch-1")
	waitForMCPOffload(t, inner, "mcp-batch-2")

	dbOnly, err := inner.FindMCPToolLog(ctx, "mcp-batch-1")
	require.NoError(t, err)
	assert.True(t, dbOnly.HasObject)
	assert.Empty(t, dbOnly.Result)
	assert.Empty(t, dbOnly.ErrorDetails)
	preview, ok := dbOnly.ArgumentsParsed.(string)
	require.True(t, ok)
	assert.Len(t, []rune(preview), 200)

	found, err := hybrid.FindMCPToolLog(ctx, "mcp-batch-1")
	require.NoError(t, err)
	assert.Equal(t, longInput, found.ArgumentsParsed.(map[string]interface{})["query"])
	assert.Equal(t, "done", found.ResultParsed.(map[string]interface{})["answer"])

	foundError, err := hybrid.FindMCPToolLog(ctx, "mcp-batch-2")
	require.NoError(t, err)
	require.NotNil(t, foundError.ErrorDetailsParsed)
	assert.Equal(t, "failed", foundError.ErrorDetailsParsed.Error.Message)

	require.NoError(t, hybrid.BatchCreateMCPToolLogsIfNotExists(ctx, entries))
	var count int64
	require.NoError(t, inner.(*RDBLogStore).db.WithContext(ctx).Model(&MCPToolLog{}).Where("id IN ?", []string{"mcp-batch-1", "mcp-batch-2"}).Count(&count).Error)
	assert.Equal(t, int64(2), count)
}

func TestHybrid_UpdateMCPToolLogOffloadsFullLog(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := &MCPToolLog{
		ID:          "mcp-update",
		RequestID:   "req-update",
		Timestamp:   time.Now().UTC(),
		ToolName:    "search",
		ServerLabel: "docs",
		Status:      "processing",
		ArgumentsParsed: map[string]any{
			"query": "find this",
		},
	}
	require.NoError(t, hybrid.CreateMCPToolLog(ctx, entry))
	waitForMCPOffload(t, inner, entry.ID)

	require.NoError(t, hybrid.UpdateMCPToolLog(ctx, entry.ID, MCPToolLog{
		Status:           "success",
		RedactionMapping: `plain:{"output":{"EMAIL-2":"result@example.com"}}`,
		ResultParsed: map[string]any{
			"answer": "done",
		},
	}))

	waitForUploads(t, func() bool {
		found, err := hybrid.FindMCPToolLog(ctx, entry.ID)
		if err != nil || found.ResultParsed == nil {
			return false
		}
		result, ok := found.ResultParsed.(map[string]interface{})
		return ok && result["answer"] == "done"
	})

	dbOnly, err := inner.FindMCPToolLog(ctx, entry.ID)
	require.NoError(t, err)
	assert.True(t, dbOnly.HasObject)
	assert.Equal(t, "success", dbOnly.Status)
	assert.Empty(t, dbOnly.Result)
	assert.Nil(t, dbOnly.ResultParsed)
	assert.Contains(t, dbOnly.RedactionMapping, "result@example.com")

	found, err := hybrid.FindMCPToolLog(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, "find this", found.ArgumentsParsed.(map[string]interface{})["query"])
	assert.Equal(t, "done", found.ResultParsed.(map[string]interface{})["answer"])
	assert.Equal(t, dbOnly.RedactionMapping, found.RedactionMapping)
}

func TestHybrid_UpdateMCPToolLogRequiresObjectHydration(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := &MCPToolLog{
		ID:          "mcp-hydration-fail",
		RequestID:   "req-hydration-fail",
		Timestamp:   time.Now().UTC(),
		ToolName:    "search",
		ServerLabel: "docs",
		Status:      "success",
		ArgumentsParsed: map[string]any{
			"query": "full input",
		},
		ResultParsed: map[string]any{
			"answer": "original",
		},
	}
	require.NoError(t, hybrid.CreateMCPToolLog(ctx, entry))
	waitForMCPOffload(t, inner, entry.ID)

	objStore.GetErr = assert.AnError
	err := hybrid.UpdateMCPToolLog(ctx, entry.ID, MCPToolLog{
		Status: "success",
		ResultParsed: map[string]any{
			"answer": "updated",
		},
	})
	require.Error(t, err)
	objStore.GetErr = nil

	found, err := hybrid.FindMCPToolLog(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, "full input", found.ArgumentsParsed.(map[string]interface{})["query"])
	assert.Equal(t, "original", found.ResultParsed.(map[string]interface{})["answer"])

	dbOnly, err := inner.FindMCPToolLog(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, "success", dbOnly.Status)
}

func TestHybrid_UpdateMCPToolLogHydratesObjectBeforeHasObjectMarker(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	longInput := ""
	for i := 0; i < 260; i++ {
		longInput += "q"
	}
	entry := &MCPToolLog{
		ID:          "mcp-has-object-false",
		RequestID:   "req-has-object-false",
		Timestamp:   time.Now().UTC(),
		ToolName:    "search",
		ServerLabel: "docs",
		Status:      "processing",
		ArgumentsParsed: map[string]any{
			"query": longInput,
		},
	}
	payload, err := MarshalMCPToolLogPayload(entry)
	require.NoError(t, err)
	dbEntry := *entry
	PrepareMCPToolDBEntry(&dbEntry)
	require.NoError(t, inner.CreateMCPToolLog(ctx, &dbEntry))
	require.NoError(t, objStore.Put(ctx, MCPToolObjectKey(hybrid.prefix, entry.Timestamp, entry.ID), payload, BuildMCPToolTags(entry)))

	require.NoError(t, hybrid.UpdateMCPToolLog(ctx, entry.ID, MCPToolLog{
		Status: "success",
		ResultParsed: map[string]any{
			"answer": "done",
		},
	}))
	waitForUploads(t, func() bool {
		found, err := hybrid.FindMCPToolLog(ctx, entry.ID)
		if err != nil || found.ResultParsed == nil {
			return false
		}
		result, ok := found.ResultParsed.(map[string]interface{})
		return ok && result["answer"] == "done"
	})

	found, err := hybrid.FindMCPToolLog(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, longInput, found.ArgumentsParsed.(map[string]interface{})["query"])
	assert.Equal(t, "done", found.ResultParsed.(map[string]interface{})["answer"])
}

// TestHybridMCPUpdateHandlesRemotePayloadState checks absent payloads, hydration
// before the database marker, and read failures with or without an outbox.
func TestHybridMCPUpdateHandlesRemotePayloadState(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, hasObject := range []bool{false, true} {
			for _, state := range []struct {
				name    string
				present bool
				getErr  error
			}{
				{name: "missing"},
				{name: "present", present: true},
				{name: "read-failure", present: true, getErr: assert.AnError},
				{name: "unrelated-not-found", getErr: fmt.Errorf("credentials file not found")},
			} {
				t.Run(fmt.Sprintf("outbox=%t/has-object=%t/%s", enabled, hasObject, state.name), func(t *testing.T) {
					dir := ""
					if enabled {
						dir = t.TempDir()
					}
					t.Setenv(uploadOutboxDirEnv, dir)
					t.Setenv(uploadOutboxMaxBytesEnv, "")
					hybrid, inner, objects := newTestHybrid(t)
					ctx := context.Background()
					defer hybrid.Close(ctx)
					entry := &MCPToolLog{
						ID: "mcp-remote-payload", Timestamp: time.Now().UTC(), ToolName: "search", Status: "processing",
						ArgumentsParsed: map[string]any{"input": strings.Repeat("full input ", 100)},
					}
					payload, err := MarshalMCPToolLogPayload(entry)
					require.NoError(t, err)
					dbEntry := *entry
					PrepareMCPToolDBEntry(&dbEntry)
					dbEntry.HasObject = hasObject
					require.NoError(t, inner.CreateMCPToolLog(ctx, &dbEntry))
					if state.present {
						require.NoError(t, objects.Put(ctx, MCPToolObjectKey(hybrid.prefix, entry.Timestamp, entry.ID), payload, nil))
					}
					objects.GetErr = state.getErr
					result := map[string]any{"answer": "completed"}
					err = hybrid.UpdateMCPToolLog(ctx, entry.ID, MCPToolLog{Status: "success", ResultParsed: result})
					if state.getErr != nil {
						require.ErrorIs(t, err, state.getErr, "errors other than a missing object must propagate")
						row, err := inner.FindMCPToolLog(ctx, entry.ID)
						require.NoError(t, err)
						assert.Equal(t, "processing", row.Status)
						assert.Empty(t, row.Result)
						return
					}
					require.NoError(t, err, "a confirmed missing remote payload must not block the update")
					waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
					found, err := hybrid.FindMCPToolLog(ctx, entry.ID)
					require.NoError(t, err)
					assert.Equal(t, "success", found.Status)
					assert.Equal(t, result, found.ResultParsed)
					if state.present {
						assert.Equal(t, entry.ArgumentsParsed, found.ArgumentsParsed, "remote hydration must not depend on HasObject")
					} else {
						assert.Equal(t, dbEntry.Arguments, found.Arguments, "missing payloads retain the available database preview")
					}
				})
			}
		}
	}
}

func TestHybrid_A2ACorrelationReconciliationUsesInnerStore(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()
	now := time.Now().UTC()
	taskID, contextID := "task-hybrid", "context-hybrid"
	request := &AgentLog{ID: "hybrid-request", Timestamp: now, RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "hybrid-request-id"}
	event := &AgentLog{ID: "hybrid-event", Timestamp: now.Add(time.Second), RecordKind: "event", Status: "success", AgentName: "fixture", RequestID: "hybrid-request-id", TaskID: &taskID, ContextID: &contextID}

	_, err := hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{request})
	require.NoError(t, err)
	require.NoError(t, hybrid.ReconcileAgentCorrelation(ctx, []*AgentLog{request}))
	_, err = hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{event})
	require.NoError(t, err)
	require.NoError(t, hybrid.ReconcileAgentCorrelation(ctx, []*AgentLog{event}))

	found, err := inner.FindAgentLog(ctx, request.ID)
	require.NoError(t, err)
	require.Equal(t, taskID, *found.TaskID)
	require.Equal(t, contextID, *found.ContextID)
}

func TestHybrid_AgentLogPayloadRoundTrip(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()
	ts := time.Date(2026, 7, 20, 13, 14, 15, 0, time.UTC)
	requestBody := `{"jsonrpc":"2.0","method":"SendMessage"}`
	responseBody := `{"jsonrpc":"2.0","result":{"taskId":"task-1"}}`
	pluginLogs := `[{"plugin_name":"audit"}]`
	errorDetails := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream failed"}}
	entry := &AgentLog{
		ID: "a2a-1", Timestamp: ts, RecordKind: "request",
		Operation: "SendMessage", Status: "success", AgentName: "fixture", RequestID: "req-a2a-1",
		RequestBody: &requestBody, ResponseBody: &responseBody, PluginLogs: pluginLogs, ErrorDetailsParsed: errorDetails,
	}

	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{entry})))
	waitForUploads(t, func() bool {
		row, err := inner.FindAgentLog(ctx, entry.ID)
		return err == nil && row.HasObject
	})

	dbOnly, err := inner.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, dbOnly.RequestBody)
	require.NotNil(t, dbOnly.ResponseBody)
	assert.Equal(t, requestBody, *dbOnly.RequestBody)
	assert.Equal(t, responseBody, *dbOnly.ResponseBody)
	require.NotNil(t, dbOnly.PayloadReference)
	assert.Equal(t, AgentLogObjectKey("test", ts, entry.ID), *dbOnly.PayloadReference)
	assert.Equal(t, "SendMessage", dbOnly.Operation)

	found, err := hybrid.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, found.RequestBody)
	require.NotNil(t, found.ResponseBody)
	assert.Equal(t, requestBody, *found.RequestBody)
	assert.Equal(t, responseBody, *found.ResponseBody)

	operation, err := hybrid.FindAgentLogOperation(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, pluginLogs, operation.PluginLogs)
	require.NotNil(t, operation.ErrorDetails)
	assert.Equal(t, "upstream failed", operation.ErrorDetails.Error.Message)
}

func TestHybrid_HiddenAgentLogRetainsObjectWithoutHydration(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()
	requestBody := `{"message":"secret"}`
	entry := &AgentLog{
		ID: "agent-hidden", Timestamp: time.Now().UTC(), RecordKind: "request",
		Operation: "SendMessage", Status: "success", AgentName: "fixture", RequestID: "req-agent-hidden",
		RequestBody: &requestBody, ContentHidden: true,
	}

	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{entry})))
	waitForUploads(t, func() bool {
		row, err := inner.FindAgentLog(ctx, entry.ID)
		return err == nil && row.HasObject
	})

	dbOnly, err := inner.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	require.True(t, dbOnly.ContentHidden)
	require.Nil(t, dbOnly.RequestBody)
	require.NotNil(t, dbOnly.PayloadReference)
	payload, err := objStore.Get(ctx, *dbOnly.PayloadReference)
	require.NoError(t, err)
	hydratedPayload := &AgentLog{}
	require.NoError(t, MergeAgentLogPayloadFromJSON(hydratedPayload, payload))
	require.NotNil(t, hydratedPayload.RequestBody)
	require.Equal(t, requestBody, *hydratedPayload.RequestBody)

	found, err := hybrid.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	require.True(t, found.ContentHidden)
	require.Nil(t, found.RequestBody)
}

func TestHybrid_UpdateAgentLogOffloadsMergedPayload(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()
	ts := time.Date(2026, 7, 20, 13, 14, 15, 0, time.UTC)
	requestBody := `{"request":"original"}`
	responseBody := `{"response":"original"}`
	updatedResponseBody := `{"response":"updated"}`
	entry := &AgentLog{
		ID: "agent-update", Timestamp: ts, RecordKind: "request",
		Operation: "SendMessage", Status: "success", AgentName: "fixture", RequestID: "req-agent-update",
		RequestBody: &requestBody, ResponseBody: &responseBody,
	}

	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{entry})))
	waitForUploads(t, func() bool {
		row, err := inner.FindAgentLog(ctx, entry.ID)
		return err == nil && row.HasObject
	})

	require.NoError(t, hybrid.UpdateAgentLog(ctx, entry.ID, map[string]interface{}{
		"response_body": updatedResponseBody,
	}))
	waitForUploads(t, func() bool {
		found, err := hybrid.FindAgentLog(ctx, entry.ID)
		return err == nil && found.ResponseBody != nil && *found.ResponseBody == updatedResponseBody
	})

	dbOnly, err := inner.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, dbOnly.RequestBody)
	require.NotNil(t, dbOnly.ResponseBody)
	assert.Equal(t, requestBody, *dbOnly.RequestBody)
	assert.Equal(t, updatedResponseBody, *dbOnly.ResponseBody)

	found, err := hybrid.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, found.RequestBody)
	require.NotNil(t, found.ResponseBody)
	assert.Equal(t, requestBody, *found.RequestBody)
	assert.Equal(t, updatedResponseBody, *found.ResponseBody)
}

func TestHybrid_UpdateAgentLogUploadFailureKeepsUpdatedDatabaseFallback(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()
	ts := time.Date(2026, 7, 20, 13, 14, 15, 0, time.UTC)
	originalResponseBody := `{"response":"original"}`
	updatedResponseBody := `{"response":"updated"}`
	secondUpdatedResponseBody := `{"response":"updated again"}`
	entry := &AgentLog{
		ID: "agent-update-upload-failure", Timestamp: ts, RecordKind: "request",
		Operation: "SendMessage", Status: "success", AgentName: "fixture", RequestID: "req-agent-update-upload-failure",
		ResponseBody: &originalResponseBody,
	}

	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{entry})))
	waitForUploads(t, func() bool {
		row, err := inner.FindAgentLog(ctx, entry.ID)
		return err == nil && row.HasObject
	})

	objStore.PutErr = assert.AnError
	require.NoError(t, hybrid.UpdateAgentLog(ctx, entry.ID, map[string]interface{}{
		"response_body": updatedResponseBody,
	}))
	waitForUploads(t, func() bool { return hybrid.DroppedUploads() == 1 })

	dbOnly, err := inner.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	assert.False(t, dbOnly.HasObject)
	require.NotNil(t, dbOnly.ResponseBody)
	assert.Equal(t, updatedResponseBody, *dbOnly.ResponseBody)

	found, err := hybrid.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, found.ResponseBody)
	assert.Equal(t, updatedResponseBody, *found.ResponseBody)

	require.NoError(t, hybrid.UpdateAgentLog(ctx, entry.ID, map[string]interface{}{
		"response_body": secondUpdatedResponseBody,
	}))
	waitForUploads(t, func() bool { return hybrid.DroppedUploads() == 2 })

	dbOnly, err = inner.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	assert.False(t, dbOnly.HasObject)
	require.NotNil(t, dbOnly.ResponseBody)
	assert.Equal(t, secondUpdatedResponseBody, *dbOnly.ResponseBody)
}

func TestHybrid_AgentLogDuplicateDoesNotOverwritePayload(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()
	ts := time.Date(2026, 7, 20, 13, 14, 15, 0, time.UTC)
	originalBody := `{"message":"original"}`
	duplicateBody := `{"message":"duplicate"}`
	original := &AgentLog{
		ID: "agent-duplicate", Timestamp: ts, RecordKind: "request",
		Operation: "SendMessage", Status: "success", AgentName: "fixture", RequestID: "req-agent-duplicate",
		RequestBody: &originalBody,
	}

	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{original})))
	waitForUploads(t, func() bool {
		row, err := inner.FindAgentLog(ctx, original.ID)
		return err == nil && row.HasObject
	})

	duplicate := *original
	duplicate.RequestBody = &duplicateBody
	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{&duplicate})))

	found, err := hybrid.FindAgentLog(ctx, original.ID)
	require.NoError(t, err)
	require.NotNil(t, found.RequestBody)
	assert.Equal(t, originalBody, *found.RequestBody)
}

func TestHybrid_AgentLogDuplicateInSameBatchDoesNotOverwritePayload(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()
	ts := time.Date(2026, 7, 20, 13, 14, 15, 0, time.UTC)
	originalBody := `{"message":"original"}`
	duplicateBody := `{"message":"duplicate"}`
	original := &AgentLog{
		ID: "agent-same-batch-duplicate", Timestamp: ts, RecordKind: "request",
		Operation: "SendMessage", Status: "success", AgentName: "fixture", RequestID: "req-agent-same-batch-duplicate",
		RequestBody: &originalBody,
	}
	duplicate := *original
	duplicate.RequestBody = &duplicateBody

	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{original, &duplicate})))
	waitForUploads(t, func() bool {
		row, err := inner.FindAgentLog(ctx, original.ID)
		return err == nil && row.HasObject
	})

	found, err := hybrid.FindAgentLog(ctx, original.ID)
	require.NoError(t, err)
	require.NotNil(t, found.RequestBody)
	assert.Equal(t, originalBody, *found.RequestBody)
}

func TestHybrid_DeleteAgentLogsRemovesCorrelatedObjects(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()
	ts := time.Date(2026, 7, 20, 13, 14, 15, 0, time.UTC)
	requestBody := `{"request":"body"}`
	eventBody := `{"event":"body"}`
	unrelatedBody := `{"unrelated":"body"}`
	entries := []*AgentLog{
		{ID: "delete-request", Timestamp: ts, RecordKind: "request", Operation: "SendStreamingMessage", Status: "success", AgentName: "fixture", RequestID: "delete-operation", RequestBody: &requestBody},
		{ID: "delete-event", Timestamp: ts.Add(time.Second), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "fixture", RequestID: "delete-operation", EventBody: &eventBody},
		{ID: "keep-request", Timestamp: ts.Add(2 * time.Second), RecordKind: "request", Operation: "SendMessage", Status: "success", AgentName: "fixture", RequestID: "keep-operation", RequestBody: &unrelatedBody},
	}
	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, entries)))
	for _, entry := range entries {
		entry := entry
		waitForUploads(t, func() bool {
			row, err := inner.FindAgentLog(ctx, entry.ID)
			return err == nil && row.HasObject
		})
	}

	require.NoError(t, hybrid.DeleteAgentLogs(ctx, []string{"delete-request"}))
	_, err := inner.FindAgentLog(ctx, "delete-request")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = inner.FindAgentLog(ctx, "delete-event")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = objStore.Get(ctx, AgentLogObjectKey("test", entries[0].Timestamp, entries[0].ID))
	require.Error(t, err)
	_, err = objStore.Get(ctx, AgentLogObjectKey("test", entries[1].Timestamp, entries[1].ID))
	require.Error(t, err)
	_, err = inner.FindAgentLog(ctx, "keep-request")
	require.NoError(t, err)
	_, err = objStore.Get(ctx, AgentLogObjectKey("test", entries[2].Timestamp, entries[2].ID))
	require.NoError(t, err)
}

func TestHybrid_A2AEventBodyRoundTripKeepsBoundedDatabasePreview(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()
	body := `{"message":"` + string(make([]byte, maxA2APayloadPreviewRunes+50)) + `"}`
	sequence := int64(1)
	entry := &AgentLog{
		ID: "a2a-event-1", Timestamp: time.Now().UTC(), RecordKind: "event",
		Operation: "SendStreamingMessage", Status: "success", AgentName: "fixture", RequestID: "operation-1",
		EventSequence: &sequence, EventBody: &body,
	}

	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{entry})))
	waitForUploads(t, func() bool {
		row, err := inner.FindAgentLog(ctx, entry.ID)
		return err == nil && row.HasObject
	})
	dbOnly, err := inner.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, dbOnly.EventBody)
	assert.LessOrEqual(t, len([]rune(*dbOnly.EventBody)), maxA2APayloadPreviewRunes)
	assert.Equal(t, int64(1), *dbOnly.EventSequence)

	found, err := hybrid.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, found.EventBody)
	assert.Equal(t, body, *found.EventBody)
	assert.Equal(t, 1, objStore.Len())
}

func TestHybrid_AgentLogUploadFailureRetainsBoundedDatabaseFallback(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	objStore.PutErr = assert.AnError
	requestBody := `{"message":"` + string(make([]byte, maxA2APayloadPreviewRunes+50)) + `"}`
	responseBody := `{"result":"` + string(make([]byte, maxA2APayloadPreviewRunes+50)) + `"}`
	entry := &AgentLog{
		ID: "a2a-put-failure", Timestamp: time.Now().UTC(), RecordKind: "request",
		Operation: "SendMessage", Status: "success", AgentName: "fixture", RequestID: "req-a2a-put-failure",
		RequestBody: &requestBody, ResponseBody: &responseBody,
	}

	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(context.Background(), []*AgentLog{entry})))
	waitForUploads(t, func() bool { return hybrid.DroppedUploads() == 1 })
	row, err := inner.FindAgentLog(context.Background(), entry.ID)
	require.NoError(t, err)
	assert.False(t, row.HasObject)
	require.NotNil(t, row.RequestBody)
	require.NotNil(t, row.ResponseBody)
	assert.LessOrEqual(t, len([]rune(*row.RequestBody)), maxA2APayloadPreviewRunes)
	assert.LessOrEqual(t, len([]rune(*row.ResponseBody)), maxA2APayloadPreviewRunes)
	assert.Equal(t, 0, objStore.Len())
}

func TestHybrid_AgentLogQueueRejectionRetainsBoundedDatabaseFallback(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	hybrid.pendingBytes.Store(defaultMaxUploadQueueBytes)
	requestBody := `{"id":"task-1"}`
	entry := &AgentLog{
		ID: "a2a-queue-drop", Timestamp: time.Now().UTC(), RecordKind: "request",
		Operation: "GetTask", Status: "success", AgentName: "fixture", RequestID: "req-a2a-queue-drop",
		RequestBody: &requestBody,
	}

	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(context.Background(), []*AgentLog{entry})))
	row, err := inner.FindAgentLog(context.Background(), entry.ID)
	require.NoError(t, err)
	assert.False(t, row.HasObject)
	require.NotNil(t, row.RequestBody)
	assert.Equal(t, requestBody, *row.RequestBody)
	assert.Equal(t, int64(1), hybrid.DroppedUploads())
	assert.Equal(t, 0, objStore.Len())
	hybrid.pendingBytes.Store(0)
}

func TestHybrid_AgentLogWithoutPayloadStaysInDatabaseOnly(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()
	entry := &AgentLog{
		ID: "a2a-metadata", Timestamp: time.Now().UTC(), RecordKind: "request",
		Operation: "GetTask", Status: "success", AgentName: "fixture", RequestID: "req-a2a-metadata",
	}

	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{entry})))
	row, err := inner.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	assert.False(t, row.HasObject)
	assert.Nil(t, row.PayloadReference)
	assert.Equal(t, 0, objStore.Len())
}

func TestHybrid_ProcessMCPUploadSkipsMissingRowsWithEmptyStatus(t *testing.T) {
	hybrid, _, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())

	hybrid.processUpload(&uploadWork{
		logID:     "mcp-missing-row",
		timestamp: time.Now().UTC(),
		key:       MCPToolObjectKey(hybrid.prefix, time.Now().UTC(), "mcp-missing-row"),
		kind:      uploadKindMCP,
		payload:   []byte(`{"id":"mcp-missing-row"}`),
	})

	assert.Equal(t, 0, objStore.Len())
	assert.Equal(t, int64(1), hybrid.DroppedUploads())
}

func TestHybrid_DeleteMCPToolLogsDeletesObjects(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := &MCPToolLog{
		ID:          "mcp-delete",
		RequestID:   "req-delete",
		Timestamp:   time.Now().UTC(),
		ToolName:    "echo",
		ServerLabel: "local",
		Status:      "success",
		ArgumentsParsed: map[string]any{
			"input": "delete me",
		},
	}
	require.NoError(t, hybrid.CreateMCPToolLog(ctx, entry))
	waitForMCPOffload(t, inner, entry.ID)

	require.NoError(t, hybrid.DeleteMCPToolLogs(ctx, []string{entry.ID}))
	assert.Equal(t, 0, objStore.Len())
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
	waitForUploads(t, func() bool { return hybrid.DroppedUploads() == 1 })

	// Upload should have been dropped.
	assert.Equal(t, 0, objStore.Len(), "no object should be stored when Put fails")
	assert.Equal(t, int64(1), hybrid.DroppedUploads(), "dropped upload should be counted")

	// DB row exists but has_object remains false since the upload failed.
	found, err := hybrid.FindByID(ctx, "put-fail-1")
	require.NoError(t, err)
	assert.False(t, found.HasObject, "has_object should remain false when upload fails")
}

// TestHybridFailedUploadPersistsDiskOutbox checks that failed uploads retain full payloads and file
// age until both upload and database marking succeed.
func TestHybridFailedUploadPersistsDiskOutbox(t *testing.T) {
	outboxDir := t.TempDir()
	t.Setenv("BIFROST_LOG_UPLOAD_OUTBOX_DIR", outboxDir)
	t.Setenv("BIFROST_LOG_UPLOAD_OUTBOX_MAX_BYTES", "")
	hybrid, inner, objects := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	objects.PutErr = assert.AnError
	entry := &Log{
		ID: "outbox-failed-upload", Timestamp: time.Now().UTC(),
		Provider: "openai", Model: "gpt-4o-mini", Status: "success",
		InputHistoryParsed:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("important input")}}},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: strPtr("important output")}},
	}
	require.NoError(t, hybrid.CreateIfNotExists(context.Background(), entry))
	var files []string
	waitForUploads(t, func() bool {
		files, _ = filepath.Glob(filepath.Join(outboxDir, "*.json"))
		return len(files) > 0 || hybrid.DroppedUploads() > 0
	})
	require.Len(t, files, 1, "a failed upload must persist its payload in the disk outbox")
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	assert.Contains(t, string(data), entry.ID)
	assert.Contains(t, string(data), "important input")
	assert.Contains(t, string(data), "important output")
	assert.Zero(t, hybrid.DroppedUploads(), "persisted uploads are pending, not dropped")
	row, err := inner.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	assert.False(t, row.HasObject, "has_object must only be set after a confirmed upload")
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	// Repeated failures retain the file, including its original age.
	before, err := os.Stat(files[0])
	require.NoError(t, err)
	hybrid.sweepUploadOutbox(context.Background())
	after, err := os.Stat(files[0])
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime())
	objects.PutErr = nil
	hybrid.sweepUploadOutbox(context.Background())
	_, err = os.Stat(files[0])
	assert.True(t, os.IsNotExist(err), "successful replay must remove the file")
	found, err := hybrid.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	assert.True(t, found.HasObject)
	assert.Contains(t, found.InputHistory, "important input")
	assert.Contains(t, found.OutputMessage, "important output")
	assert.Zero(t, hybrid.DroppedUploads())
}

// TestHybridOutboxSurvivesRestart checks that a reopened store recovers complete payloads and tags
// from the same outbox directory.
func TestHybridOutboxSurvivesRestart(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	dbPath := filepath.Join(t.TempDir(), "restart.db")
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: dbPath}, hybridTestLogger{})
	require.NoError(t, err)
	objects := objectstore.NewInMemoryObjectStore()
	objects.PutErr = assert.AnError
	hybrid, err := newHybridLogStore(inner, objects, "test", hybridTestLogger{}, nil, nil)
	require.NoError(t, err)
	entry := &Log{ID: "restart-pending", Timestamp: time.Now().UTC(), Provider: "openai", Model: "gpt-4o-mini", Status: "success",
		InputHistory:  `[{"role":"system","content":"earlier context"},{"role":"user","content":"latest input"}]`,
		OutputMessage: `{"role":"assistant","content":"recovered output"}`}
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	require.NoError(t, hybrid.Close(ctx)) // Drains the upload queue to disk.
	inner, err = newSqliteLogStore(ctx, &SQLiteConfig{Path: dbPath}, hybridTestLogger{})
	require.NoError(t, err)
	objects = objectstore.NewInMemoryObjectStore()
	hybrid, err = newHybridLogStore(inner, objects, "test", hybridTestLogger{}, nil, nil)
	require.NoError(t, err)
	defer hybrid.Close(ctx)
	hybrid.sweepUploadOutbox(ctx)
	found, err := hybrid.FindByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.True(t, found.HasObject)
	assert.Contains(t, found.InputHistory, "earlier context")
	assert.Contains(t, found.OutputMessage, "recovered output")
	assert.Equal(t, "openai", objects.GetTags(ObjectKey("test", entry.Timestamp, entry.ID))["provider"])
	files, _, err := hybrid.outbox.files()
	require.NoError(t, err)
	assert.Empty(t, files)
}

// TestHybridOutboxEvictsOldestBeforeRetry checks that quota enforcement removes the oldest upload
// and retains files within the budget.
func TestHybridOutboxEvictsOldestBeforeRetry(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "1")
	hybrid, _, objects := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	objects.PutErr = assert.AnError
	ctx := context.Background()
	for i := range 3 {
		entry := &Log{ID: fmt.Sprintf("evict-%d", i), Timestamp: time.Now().UTC(), Status: "success", InputHistory: `[{"role":"user","content":"keep data"}]`}
		require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	}
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	files, total, err := hybrid.outbox.files()
	require.NoError(t, err)
	require.Len(t, files, 3)
	for i, file := range files {
		age := time.Now().Add(time.Duration(i-3) * time.Hour)
		require.NoError(t, os.Chtimes(file.path, age, age))
	}
	hybrid.outbox.maxBytes = total - files[0].info.Size()
	hybrid.sweepUploadOutbox(ctx)
	_, err = os.Stat(files[0].path)
	assert.True(t, os.IsNotExist(err))
	remaining, size, err := hybrid.outbox.files()
	require.NoError(t, err)
	assert.Len(t, remaining, 2)
	assert.LessOrEqual(t, size, hybrid.outbox.maxBytes)
	assert.Equal(t, int64(1), hybrid.DroppedUploads())
}

// TestUploadOutboxEnvironment checks disabled mode, byte-limit defaults, invalid limits, and
// unusable directories.
func TestUploadOutboxEnvironment(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, "")
	t.Setenv(uploadOutboxMaxBytesEnv, "invalid")
	outbox, err := newUploadOutboxFromEnv()
	require.NoError(t, err)
	assert.Nil(t, outbox)
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	for _, value := range []string{"invalid", "0", "-1", "10000000000000000000000"} {
		t.Setenv(uploadOutboxMaxBytesEnv, value)
		_, err = newUploadOutboxFromEnv()
		require.ErrorContains(t, err, uploadOutboxMaxBytesEnv)
	}
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	outbox, err = newUploadOutboxFromEnv()
	require.NoError(t, err)
	assert.Equal(t, int64(10_000_000_000), outbox.maxBytes)
	t.Setenv(uploadOutboxMaxBytesEnv, "12345")
	outbox, err = newUploadOutboxFromEnv()
	require.NoError(t, err)
	assert.Equal(t, int64(12345), outbox.maxBytes)
	file := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("existing"), 0600))
	t.Setenv(uploadOutboxDirEnv, file)
	_, err = newUploadOutboxFromEnv()
	require.Error(t, err)
}

// TestUploadOutboxKeepsNewestVersion checks that stale saves and removals preserve a newer pending
// record and its private permissions.
func TestUploadOutboxKeepsNewestVersion(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	outbox, err := newUploadOutboxFromEnv()
	require.NoError(t, err)
	old := &uploadWork{logID: "version", key: "test/key", timestamp: time.Now().UTC(), queuedAt: time.Now().UTC(), payload: []byte(`{"value":"old"}`)}
	newer := *old
	newer.queuedAt = old.queuedAt.Add(time.Second)
	newer.payload = []byte(`{"value":"new"}`)
	require.NoError(t, outbox.save(&newer))
	require.NoError(t, outbox.save(old))
	require.NoError(t, outbox.removeThrough(old))
	got, err := outbox.read(outbox.path(old.key))
	require.NoError(t, err)
	assert.Equal(t, newer.payload, got.payload)
	info, err := os.Stat(outbox.path(old.key))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.NoError(t, outbox.removeThrough(&newer))
}

// TestHybridOutboxQueueOverflowAndDiskFailure checks persistence on queue or memory exhaustion and
// drop accounting when disk writes fail.
func TestHybridOutboxQueueOverflowAndDiskFailure(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	outbox, err := newUploadOutboxFromEnv()
	require.NoError(t, err)
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "overflow.db")}, hybridTestLogger{})
	require.NoError(t, err)
	defer inner.Close(ctx)
	for _, id := range []string{"queue-full", "memory-full", "disk-failure"} {
		require.NoError(t, inner.CreateIfNotExists(ctx, &Log{ID: id, Timestamp: time.Now().UTC(), Status: "success"}))
	}
	hybrid := &HybridLogStore{inner: inner, outbox: outbox, logger: hybridTestLogger{}, uploadQueue: make(chan *uploadWork)}
	hybrid.enqueueRawUpload("queue-full", time.Now().UTC(), "test/full", uploadKindLog, "", []byte(`{"content":"full"}`), nil)
	assert.Zero(t, hybrid.pendingBytes.Load())
	assert.Zero(t, hybrid.DroppedUploads())
	hybrid.pendingBytes.Store(defaultMaxUploadQueueBytes)
	hybrid.enqueueRawUpload("memory-full", time.Now().UTC(), "test/memory", uploadKindLog, "", []byte(`{"content":"memory"}`), nil)
	files, _, err := outbox.files()
	require.NoError(t, err)
	assert.Len(t, files, 2)
	require.NoError(t, os.RemoveAll(outbox.dir))
	hybrid.enqueueRawUpload("disk-failure", time.Now().UTC(), "test/disk", uploadKindLog, "", []byte(`{"content":"disk"}`), nil)
	assert.Equal(t, int64(1), hybrid.DroppedUploads())
}

// TestHybridOutboxRecoversMCPToolLog checks that a failed MCP upload becomes readable and its
// outbox record is removed after recovery.
func TestHybridOutboxRecoversMCPToolLog(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	hybrid, inner, objects := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	objects.PutErr = assert.AnError
	entry := &MCPToolLog{ID: "mcp-outbox", Timestamp: time.Now().UTC(), Status: "success", ToolName: "test-tool", Arguments: `{"input":"original"}`, Result: `{"output":"recovered"}`}
	require.NoError(t, hybrid.CreateMCPToolLog(context.Background(), entry))
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	files, _, err := hybrid.outbox.files()
	require.NoError(t, err)
	require.Len(t, files, 1)
	objects.PutErr = nil
	hybrid.sweepUploadOutbox(context.Background())
	row, err := inner.FindMCPToolLog(context.Background(), entry.ID)
	require.NoError(t, err)
	assert.True(t, row.HasObject)
	found, err := hybrid.FindMCPToolLog(context.Background(), entry.ID)
	require.NoError(t, err)
	assert.Contains(t, found.Result, "recovered")
	files, _, err = hybrid.outbox.files()
	require.NoError(t, err)
	assert.Empty(t, files)
}

// TestHybridOutboxRecoversAgentLogs checks agent payload recovery, updates to pending bodies, and
// deletion before retry.
func TestHybridOutboxRecoversAgentLogs(t *testing.T) {
	for _, action := range []string{"retry", "update", "delete"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv(uploadOutboxDirEnv, t.TempDir())
			t.Setenv(uploadOutboxMaxBytesEnv, "")
			hybrid, inner, objects := newTestHybrid(t)
			ctx := context.Background()
			defer hybrid.Close(ctx)
			objects.PutErr = assert.AnError
			requestBody := `{"input":"` + strings.Repeat("a", maxA2APayloadPreviewRunes+100) + `"}`
			responseBody := `{"output":"recovered"}`
			entry := &AgentLog{
				ID: "agent-outbox-" + action, Timestamp: time.Now().UTC(), RecordKind: "request",
				Status: "success", Operation: "SendMessage", AgentName: "fixture", RequestID: "request-" + action,
				RequestBody: &requestBody, ResponseBody: &responseBody,
			}
			require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{entry})))
			waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
			files, _, err := hybrid.outbox.files()
			require.NoError(t, err)
			require.Len(t, files, 1)
			pending, err := hybrid.outbox.read(files[0].path)
			require.NoError(t, err)
			assert.Equal(t, uploadKindAgent, pending.kind)
			switch action {
			case "update":
				responseBody = `{"output":"updated"}`
				require.NoError(t, hybrid.UpdateAgentLog(ctx, entry.ID, map[string]interface{}{"response_body": responseBody}))
				waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
			case "delete":
				require.NoError(t, hybrid.DeleteAgentLogs(ctx, []string{entry.ID}))
			}
			objects.PutErr = nil
			hybrid.sweepUploadOutbox(ctx)
			files, _, err = hybrid.outbox.files()
			require.NoError(t, err)
			assert.Empty(t, files)
			if action == "delete" {
				assert.Zero(t, objects.Len(), "deleted agent logs must not be uploaded")
				return
			}
			row, err := inner.FindAgentLog(ctx, entry.ID)
			require.NoError(t, err)
			assert.True(t, row.HasObject)
			found, err := hybrid.FindAgentLog(ctx, entry.ID)
			require.NoError(t, err)
			require.NotNil(t, found.RequestBody)
			require.NotNil(t, found.ResponseBody)
			assert.Equal(t, requestBody, *found.RequestBody, "updates must preserve the full pending request")
			assert.Equal(t, responseBody, *found.ResponseBody)
		})
	}
}

// TestHybridOutboxMCPUpdatePreservesPendingArguments checks that updating a failed MCP upload
// retains full arguments beyond the database preview.
func TestHybridOutboxMCPUpdatePreservesPendingArguments(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	hybrid, _, objects := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	objects.PutErr = assert.AnError
	longInput := strings.Repeat("a", 300)
	entry := &MCPToolLog{ID: "mcp-update-pending", Timestamp: time.Now().UTC(), Status: "processing", ToolName: "test-tool", ArgumentsParsed: map[string]any{"input": longInput}}
	require.NoError(t, hybrid.CreateMCPToolLog(context.Background(), entry))
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	require.NoError(t, hybrid.UpdateMCPToolLog(context.Background(), entry.ID, MCPToolLog{Status: "success", ResultParsed: map[string]any{"output": "recovered"}}))
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	objects.PutErr = nil
	hybrid.sweepUploadOutbox(context.Background())
	found, err := hybrid.FindMCPToolLog(context.Background(), entry.ID)
	require.NoError(t, err)
	assert.Equal(t, longInput, found.ArgumentsParsed.(map[string]interface{})["input"])
	assert.Contains(t, found.Result, "recovered")
}

type outboxMarkerFailStore struct {
	LogStore
	fail atomic.Bool
}

// Update injects failures when an upload attempts to mark its database row as having an object.
func (s *outboxMarkerFailStore) Update(ctx context.Context, id string, entry any) error {
	if updates, ok := entry.(map[string]interface{}); ok && updates["has_object"] == true && s.fail.Load() {
		return assert.AnError
	}
	return s.LogStore.Update(ctx, id, entry)
}

// TestHybridOutboxRetainsUploadUntilDatabaseMarkerSucceeds checks that a successful object write
// remains retryable until its database marker is saved.
func TestHybridOutboxRetainsUploadUntilDatabaseMarkerSucceeds(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	hybrid, inner, objects := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	store := &outboxMarkerFailStore{LogStore: inner}
	store.fail.Store(true)
	hybrid.inner = store
	entry := &Log{ID: "marker-failed", Timestamp: time.Now().UTC(), Status: "success", InputHistory: `[{"role":"user","content":"must remain recoverable"}]`}
	require.NoError(t, hybrid.CreateIfNotExists(context.Background(), entry))
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	assert.Equal(t, 1, objects.Len(), "S3 upload succeeded")
	files, _, err := hybrid.outbox.files()
	require.NoError(t, err)
	require.Len(t, files, 1, "do not delete until readers can hydrate the object")
	row, err := inner.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	assert.False(t, row.HasObject)
	store.fail.Store(false)
	hybrid.sweepUploadOutbox(context.Background())
	row, err = inner.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	assert.True(t, row.HasObject)
	files, _, err = hybrid.outbox.files()
	require.NoError(t, err)
	assert.Empty(t, files)
}

// TestHybridOutboxDoesNotReuploadDeletedLogs checks that deleting a pending log prevents its
// payload from being uploaded later.
func TestHybridOutboxDoesNotReuploadDeletedLogs(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	hybrid, _, objects := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	objects.PutErr = assert.AnError
	entry := &Log{ID: "deleted-pending", Timestamp: time.Now().UTC(), Status: "success", InputHistory: `[{"role":"user","content":"delete this"}]`}
	require.NoError(t, hybrid.CreateIfNotExists(context.Background(), entry))
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	require.NoError(t, hybrid.DeleteLog(context.Background(), entry.ID))
	objects.PutErr = nil
	hybrid.sweepUploadOutbox(context.Background())
	assert.Zero(t, objects.Len())
	files, _, err := hybrid.outbox.files()
	require.NoError(t, err)
	assert.Empty(t, files)
}

// TestHybridOutboxOlderQueuedFailureCannotOverwriteNewerUpload checks that an older queued failure
// cannot replace a newer successful upload after its disk record is gone.
func TestHybridOutboxOlderQueuedFailureCannotOverwriteNewerUpload(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "ordering.db")}, hybridTestLogger{})
	require.NoError(t, err)
	defer inner.Close(ctx)
	outbox, err := newUploadOutboxFromEnv()
	require.NoError(t, err)
	objects := objectstore.NewInMemoryObjectStore()
	hybrid := &HybridLogStore{inner: inner, objects: objects, prefix: "test", logger: hybridTestLogger{}, outbox: outbox, uploadQueue: make(chan *uploadWork, 2)}
	entry := &Log{ID: "overlapping-uploads", Timestamp: time.Now().UTC(), Status: "success"}
	require.NoError(t, inner.CreateIfNotExists(ctx, entry))
	key := ObjectKey(hybrid.prefix, entry.Timestamp, entry.ID)
	hybrid.enqueueRawUpload(entry.ID, entry.Timestamp, key, uploadKindLog, "", []byte(`{"output_message":"older"}`), nil)
	hybrid.enqueueRawUpload(entry.ID, entry.Timestamp, key, uploadKindLog, "", []byte(`{"output_message":"newer"}`), nil)
	older, newer := <-hybrid.uploadQueue, <-hybrid.uploadQueue
	// Workers can be scheduled in a different order after receiving queue jobs.
	hybrid.processUpload(newer)
	assert.Nil(t, outbox.active[key].work, "completed payloads must not outlive their byte reservation")
	objects.PutErr = assert.AnError
	hybrid.processUpload(older)
	objects.PutErr = nil
	hybrid.sweepUploadOutbox(ctx)
	payload, err := objects.Get(ctx, key)
	require.NoError(t, err)
	assert.JSONEq(t, `{"output_message":"newer"}`, string(payload))
	assert.Zero(t, hybrid.pendingBytes.Load())
	assert.Empty(t, outbox.active, "finished uploads must release their ordering metadata")
}

// TestHybridOutboxRetriesRespectUploadMemoryBudget checks that disk retries defer while live
// uploads reserve the full memory budget.
func TestHybridOutboxRetriesRespectUploadMemoryBudget(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	hybrid, inner, objects := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	objects.PutErr = assert.AnError
	entry := &Log{ID: "retry-budget", Timestamp: time.Now().UTC(), Status: "success", InputHistory: `[{"role":"user","content":"pending"}]`}
	require.NoError(t, hybrid.CreateIfNotExists(context.Background(), entry))
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	objects.PutErr = nil
	hybrid.pendingBytes.Store(defaultMaxUploadQueueBytes)
	hybrid.sweepUploadOutbox(context.Background())
	assert.Zero(t, objects.Len(), "disk retries must wait while live uploads consume the memory budget")
	files, _, err := hybrid.outbox.files()
	require.NoError(t, err)
	assert.Len(t, files, 1)
	hybrid.pendingBytes.Store(0)
	hybrid.sweepUploadOutbox(context.Background())
	row, err := inner.FindByID(context.Background(), entry.ID)
	require.NoError(t, err)
	assert.True(t, row.HasObject)
	assert.Zero(t, hybrid.pendingBytes.Load())
}

type outboxBlockingObjectStore struct {
	*objectstore.InMemoryObjectStore
	blockedKey string
	started    chan struct{}
	release    chan struct{}
}

// Put pauses the selected object write until the test releases it or the context is canceled.
func (s *outboxBlockingObjectStore) Put(ctx context.Context, key string, data []byte, tags map[string]string) error {
	if key == s.blockedKey {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.InMemoryObjectStore.Put(ctx, key, data, tags)
}

// TestHybridOutboxDisabledKeepsUploadsIndependent checks that disabled outboxes add no
// serialization between uploads sharing a lock stripe.
func TestHybridOutboxDisabledKeepsUploadsIndependent(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, "")
	hybrid, inner, objects := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ts := time.Now().UTC()
	firstKey := ObjectKey(hybrid.prefix, ts, "blocked-upload")
	otherID := ""
	for i := 0; ; i++ {
		candidate := fmt.Sprintf("independent-%d", i)
		if hybrid.uploadLock(ObjectKey(hybrid.prefix, ts, candidate)) == hybrid.uploadLock(firstKey) {
			otherID = candidate
			break
		}
	}
	blocked := &outboxBlockingObjectStore{InMemoryObjectStore: objects, blockedKey: firstKey, started: make(chan struct{}), release: make(chan struct{})}
	hybrid.objects = blocked
	defer close(blocked.release)
	for _, id := range []string{"blocked-upload", otherID} {
		entry := &Log{ID: id, Timestamp: ts, Status: "success", InputHistory: `[{"role":"user","content":"independent upload"}]`}
		require.NoError(t, hybrid.CreateIfNotExists(context.Background(), entry))
		if id == "blocked-upload" {
			select {
			case <-blocked.started:
			case <-time.After(time.Second):
				t.Fatal("first upload never started")
			}
		}
	}
	assert.Eventually(t, func() bool {
		row, err := inner.FindByID(context.Background(), otherID)
		return err == nil && row.HasObject
	}, time.Second, 10*time.Millisecond, "a blocked S3 upload must not serialize unrelated uploads when outbox is disabled")
}

// TestHybridOutboxCancelledRetryRetainsFileAndReleasesBudget checks that a canceled retry preserves
// pending work and releases its memory reservation.
func TestHybridOutboxCancelledRetryRetainsFileAndReleasesBudget(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	hybrid, _, objects := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	objects.PutErr = assert.AnError
	entry := &Log{ID: "cancel-retry", Timestamp: time.Now().UTC(), Status: "success", InputHistory: `[{"role":"user","content":"keep pending"}]`}
	require.NoError(t, hybrid.CreateIfNotExists(context.Background(), entry))
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	objects.PutErr = nil
	blocked := &outboxBlockingObjectStore{InMemoryObjectStore: objects, blockedKey: ObjectKey(hybrid.prefix, entry.Timestamp, entry.ID), started: make(chan struct{}), release: make(chan struct{})}
	hybrid.objects = blocked
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		hybrid.sweepUploadOutbox(ctx)
		close(done)
	}()
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("retry never reached object storage")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retry ignored cancellation")
	}
	files, _, err := hybrid.outbox.files()
	require.NoError(t, err)
	assert.Len(t, files, 1)
	assert.Zero(t, hybrid.pendingBytes.Load())
	assert.Zero(t, objects.Len())
}

func TestHybrid_DeleteLog(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
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
	waitForOffload(t, inner, entry.ID)
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
	waitForUploads(t, func() bool { return objStore.Len() == 1 })

	key := ObjectKey("test", ts, "tag-1")
	tags := objStore.GetTags(key)
	assert.Equal(t, "anthropic", tags["provider"])
	assert.Equal(t, "error", tags["status"])
	assert.Equal(t, "true", tags["has_error"])
	assert.Equal(t, "true", tags["stream"])
	assert.Equal(t, "vk_test", tags["virtual_key_id"])
	assert.Equal(t, "2026-04-03", tags["date"])
}

func TestHybrid_MetadataIsRetainedInDBAndWrittenToObjectPayload(t *testing.T) {
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	ts := time.Date(2026, 4, 3, 14, 30, 0, 0, time.UTC)
	inputContent := "Hello"
	entry := &Log{
		ID:        "metadata-1",
		Timestamp: ts,
		Provider:  "openai",
		Model:     "gpt-4",
		Status:    "success",
		Object:    "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &inputContent}},
		},
		MetadataParsed: map[string]interface{}{
			"cortex-user-id": "user-123",
			"team":           "payments",
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForUploads(t, func() bool { return objStore.Len() == 1 })

	dbLog, err := inner.FindByID(ctx, "metadata-1")
	require.NoError(t, err)
	require.NotNil(t, dbLog.Metadata)
	assert.Contains(t, *dbLog.Metadata, "cortex-user-id")

	// Metadata is DB-authoritative but a copy is written to the object store
	// snapshot so consumers reading objects directly see custom attributes.
	key := ObjectKey("test", ts, "metadata-1")
	rawPayload, err := objStore.Get(ctx, key)
	require.NoError(t, err)
	var payload map[string]string
	require.NoError(t, sonic.Unmarshal(rawPayload, &payload))
	require.Contains(t, payload, "metadata", "metadata must be written to the object store snapshot")
	assert.Contains(t, payload["metadata"], "cortex-user-id")
	assert.Contains(t, payload["metadata"], "payments")

	// Hydration still returns metadata, sourced from the DB row.
	found, err := hybrid.FindByID(ctx, "metadata-1")
	require.NoError(t, err)
	assert.Equal(t, "user-123", found.MetadataParsed["cortex-user-id"])
	assert.Equal(t, "payments", found.MetadataParsed["team"])
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

func TestHybrid_ResponsesInputHistoryPreservesLastUserMessage(t *testing.T) {
	// Responses API requests carry their history in responses_input_history
	// rather than input_history. After offload, the DB row must retain the last
	// user message there so the log list can render a preview instead of "-"
	// (the full history lives in object storage). Mirrors the input_history
	// behaviour for chat completions.
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	system := "You are a helpful assistant."
	userMsg := "Can you reason about pythagoras theorem?"
	entry := &Log{
		ID:        "resp-1",
		Timestamp: time.Now().UTC(),
		Provider:  "openai",
		Model:     "gpt-5.5",
		Status:    "success",
		Object:    "responses",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem), Content: &schemas.ResponsesMessageContent{ContentStr: &system}},
			{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: &userMsg}},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForOffload(t, inner, "resp-1")

	// DB row keeps only the last user message as a preview — not the system message.
	dbLog, err := inner.FindByID(ctx, "resp-1")
	require.NoError(t, err)
	assert.Contains(t, dbLog.ResponsesInputHistory, "pythagoras theorem", "last user message should be preserved in DB for the list preview")
	assert.NotContains(t, dbLog.ResponsesInputHistory, "helpful assistant", "only the last user message should be kept, not the full history")
	assert.Contains(t, dbLog.ContentSummary, "pythagoras theorem", "content_summary should contain the user text")

	// The full responses history (including the system message) lives in S3.
	key := ObjectKey("test", entry.Timestamp, "resp-1")
	rawPayload, err := objStore.Get(ctx, key)
	require.NoError(t, err)
	assert.Contains(t, string(rawPayload), "helpful assistant", "full responses history should be offloaded to object storage")

	// FindByID hydrates the full history back from S3.
	found, err := hybrid.FindByID(ctx, "resp-1")
	require.NoError(t, err)
	require.Len(t, found.ResponsesInputHistoryParsed, 2, "full responses history should be hydrated from S3")
}

func TestHybrid_AttachmentsStrippedFromChatPreview(t *testing.T) {
	// The last-user-message preview kept in the input_history DB column must not
	// carry attachment payloads (base64 images/files/audio) — they are replaced
	// with a placeholder. The full message, base64 included, lives only in
	// object storage and is hydrated back on FindByID.
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	imageData := "data:image/png;base64,FAKEB64IMAGEDATA"
	pdfData := "FAKEB64PDFDATA"
	entry := &Log{
		ID:        "chat-attach-1",
		Timestamp: time.Now().UTC(),
		Provider:  "anthropic",
		Model:     "claude-3",
		Status:    "success",
		Object:    "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("summarize this pdf")},
				{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: imageData}},
				{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileData: schemas.Ptr(pdfData), Filename: schemas.Ptr("report.pdf")}},
			}}},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForOffload(t, inner, "chat-attach-1")

	// DB preview keeps the text and block structure but not the payloads.
	dbLog, err := inner.FindByID(ctx, "chat-attach-1")
	require.NoError(t, err)
	assert.Contains(t, dbLog.InputHistory, "summarize this pdf")
	assert.Contains(t, dbLog.InputHistory, attachmentStrippedPlaceholder)
	assert.Contains(t, dbLog.InputHistory, "report.pdf", "filename should survive stripping")
	assert.NotContains(t, dbLog.InputHistory, "FAKEB64IMAGEDATA", "base64 image must not reach the DB row")
	assert.NotContains(t, dbLog.InputHistory, pdfData, "base64 file data must not reach the DB row")
	assert.Contains(t, dbLog.ContentSummary, "summarize this pdf")

	// Full payload, base64 included, is offloaded to object storage.
	key := ObjectKey("test", entry.Timestamp, "chat-attach-1")
	rawPayload, err := objStore.Get(ctx, key)
	require.NoError(t, err)
	assert.Contains(t, string(rawPayload), "FAKEB64IMAGEDATA")
	assert.Contains(t, string(rawPayload), pdfData)

	// Copy-on-write: the caller's parsed structs must be untouched.
	blocks := entry.InputHistoryParsed[0].Content.ContentBlocks
	assert.Equal(t, imageData, blocks[1].ImageURLStruct.URL)
	assert.Equal(t, pdfData, *blocks[2].File.FileData)

	// FindByID hydrates the full message back from object storage.
	found, err := hybrid.FindByID(ctx, "chat-attach-1")
	require.NoError(t, err)
	require.Len(t, found.InputHistoryParsed, 1)
	assert.Equal(t, imageData, found.InputHistoryParsed[0].Content.ContentBlocks[1].ImageURLStruct.URL)
}

func TestHybrid_AttachmentsStrippedFromResponsesPreview(t *testing.T) {
	// Mirrors TestHybrid_AttachmentsStrippedFromChatPreview for the Responses
	// API preview stored in responses_input_history.
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	imageData := "data:image/jpeg;base64,FAKEB64RESPIMAGE"
	pdfData := "FAKEB64RESPPDF"
	entry := &Log{
		ID:        "resp-attach-1",
		Timestamp: time.Now().UTC(),
		Provider:  "openai",
		Model:     "gpt-5.5",
		Status:    "success",
		Object:    "responses",
		ResponsesInputHistoryParsed: []schemas.ResponsesMessage{
			{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("what is in this file")},
				{Type: schemas.ResponsesInputMessageContentBlockTypeImage, ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: schemas.Ptr(imageData)}},
				{Type: schemas.ResponsesInputMessageContentBlockTypeFile, ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{FileData: schemas.Ptr(pdfData), Filename: schemas.Ptr("doc.pdf")}},
			}}},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForOffload(t, inner, "resp-attach-1")

	dbLog, err := inner.FindByID(ctx, "resp-attach-1")
	require.NoError(t, err)
	assert.Contains(t, dbLog.ResponsesInputHistory, "what is in this file")
	assert.Contains(t, dbLog.ResponsesInputHistory, attachmentStrippedPlaceholder)
	assert.Contains(t, dbLog.ResponsesInputHistory, "doc.pdf", "filename should survive stripping")
	assert.NotContains(t, dbLog.ResponsesInputHistory, "FAKEB64RESPIMAGE", "base64 image must not reach the DB row")
	assert.NotContains(t, dbLog.ResponsesInputHistory, pdfData, "base64 file data must not reach the DB row")
	assert.Contains(t, dbLog.ContentSummary, "what is in this file")

	key := ObjectKey("test", entry.Timestamp, "resp-attach-1")
	rawPayload, err := objStore.Get(ctx, key)
	require.NoError(t, err)
	assert.Contains(t, string(rawPayload), "FAKEB64RESPIMAGE")
	assert.Contains(t, string(rawPayload), pdfData)

	// Copy-on-write: the caller's parsed structs must be untouched.
	blocks := entry.ResponsesInputHistoryParsed[0].Content.ContentBlocks
	assert.Equal(t, imageData, *blocks[1].ImageURL)
	assert.Equal(t, pdfData, *blocks[2].FileData)

	found, err := hybrid.FindByID(ctx, "resp-attach-1")
	require.NoError(t, err)
	require.Len(t, found.ResponsesInputHistoryParsed, 1)
	assert.Equal(t, imageData, *found.ResponsesInputHistoryParsed[0].Content.ContentBlocks[1].ImageURL)
}

func TestHybrid_TokenUsageSummaryForListPreview(t *testing.T) {
	// Pricing metadata stays in the DB even when content is offloaded. List queries
	// may still use their lightweight projection and rebuild a usage summary from
	// denormalized counters.
	hybrid, inner, objStore := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := &Log{
		ID:        "chat-tokens-1",
		Timestamp: time.Now().UTC(),
		Provider:  "openai",
		Model:     "gpt-4",
		Status:    "success",
		Object:    "chat.completion",
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens:     120,
			CompletionTokens: 45,
			TotalTokens:      165,
		},
		CacheDebugParsed: &schemas.BifrostCacheMetadata{CacheHit: true},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForUploads(t, func() bool { return objStore.Len() == 1 })

	dbLog, err := inner.FindByID(ctx, "chat-tokens-1")
	require.NoError(t, err)
	assert.NotEmpty(t, dbLog.TokenUsage, "pricing metadata must stay in log_store")
	assert.NotEmpty(t, dbLog.CacheDebug, "cache pricing metadata must stay in log_store")
	assert.Equal(t, 120, dbLog.PromptTokens)
	assert.Equal(t, 45, dbLog.CompletionTokens)
	assert.Equal(t, 165, dbLog.TotalTokens)

	result, err := hybrid.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	listLog := result.Logs[0]
	require.NotNil(t, listLog.TokenUsageParsed, "list query should rebuild token_usage from denormalized columns")
	assert.Equal(t, 120, listLog.TokenUsageParsed.PromptTokens)
	assert.Equal(t, 45, listLog.TokenUsageParsed.CompletionTokens)
	assert.Equal(t, 165, listLog.TokenUsageParsed.TotalTokens)

	found, err := hybrid.FindByID(ctx, "chat-tokens-1")
	require.NoError(t, err)
	require.NotNil(t, found.TokenUsageParsed, "detail view should retain full token_usage")
	assert.Equal(t, 165, found.TokenUsageParsed.TotalTokens)
}

func TestHybrid_SpeechInputSummaryForListPreview(t *testing.T) {
	// /audio/speech (TTS) requests carry their text in speech_input, which is
	// offloaded to object storage and cleared from the DB row. The DB must still
	// retain a content_summary so the log list renders the text instead of "-"
	// (the UI uses content_summary as its display fallback once payload fields
	// are offloaded). Same gap exists for responses/image/video inputs.
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	speechText := "The quick brown fox jumps over the lazy dog."
	entry := &Log{
		ID:                "speech-1",
		Timestamp:         time.Now().UTC(),
		Provider:          "openai",
		Model:             "tts-1",
		Status:            "success",
		Object:            "audio.speech",
		SpeechInputParsed: &schemas.SpeechInput{Input: speechText},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForOffload(t, inner, "speech-1")

	// speech_input is offloaded to S3 and cleared from the DB row, but the
	// content_summary fallback retains the text for the list preview.
	dbLog, err := inner.FindByID(ctx, "speech-1")
	require.NoError(t, err)
	assert.Empty(t, dbLog.SpeechInput, "speech_input should be offloaded to object storage")
	assert.Contains(t, dbLog.ContentSummary, "quick brown fox", "content_summary should retain the speech text for the list preview")

	// FindByID hydrates the full speech_input back from S3.
	found, err := hybrid.FindByID(ctx, "speech-1")
	require.NoError(t, err)
	require.NotNil(t, found.SpeechInputParsed)
	assert.Equal(t, speechText, found.SpeechInputParsed.Input, "speech_input should be hydrated from S3")
}

// newTestHybridWithExclude creates a HybridLogStore with specific excluded fields.
func newTestHybridWithExclude(t *testing.T, excludeFields []string) (*HybridLogStore, LogStore, *objectstore.InMemoryObjectStore) {
	t.Helper()
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "hybrid.db")}, hybridTestLogger{})
	require.NoError(t, err)
	objStore := objectstore.NewInMemoryObjectStore()
	hybrid, err := newHybridLogStore(inner, objStore, "test", hybridTestLogger{}, excludeFields, nil)
	require.NoError(t, err)
	return hybrid, inner, objStore
}

func TestHybrid_ExcludeFields_RawRequestStaysInDB(t *testing.T) {
	hybrid, inner, objStore := newTestHybridWithExclude(t, []string{"raw_request", "raw_response"})
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	inputContent := "Hello"
	entry := &Log{
		ID:          "exc-1",
		Timestamp:   time.Now().UTC(),
		Provider:    "openai",
		Model:       "gpt-4",
		Status:      "success",
		Object:      "chat.completion",
		RawRequest:  `{"model":"gpt-4","messages":[]}`,
		RawResponse: `{"id":"chatcmpl-xxx"}`,
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &inputContent}},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForUploads(t, func() bool { return objStore.Len() == 1 })

	// The DB row should still carry raw_request and raw_response (they were excluded from S3).
	dbLog, err := inner.FindByID(ctx, "exc-1")
	require.NoError(t, err)
	assert.NotEmpty(t, dbLog.RawRequest, "raw_request should remain in DB when excluded from S3")
	assert.NotEmpty(t, dbLog.RawResponse, "raw_response should remain in DB when excluded from S3")

	// The S3 payload must NOT contain raw_request or raw_response.
	key := ObjectKey("test", entry.Timestamp, "exc-1")
	rawPayload, err := objStore.Get(ctx, key)
	require.NoError(t, err)
	assert.NotContains(t, string(rawPayload), `"raw_request":"`, "raw_request must not appear in S3 payload")
	assert.NotContains(t, string(rawPayload), `"raw_response":"`, "raw_response must not appear in S3 payload")
}

func TestHybrid_ExcludeFields_InputHistoryStaysFullInDB(t *testing.T) {
	// Excluding input_history means the full conversation is stored in DB,
	// not just the last user message. An output_message is included so the
	// S3 upload is not skipped (the payload would otherwise be empty).
	hybrid, inner, objStore := newTestHybridWithExclude(t, []string{"input_history"})
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	system := "You are a helpful assistant."
	user1 := "What is 2+2?"
	assistant1 := "4"
	user2 := "And 3+3?"
	outputText := "6"
	entry := &Log{
		ID:        "exc-ih-1",
		Timestamp: time.Now().UTC(),
		Provider:  "openai",
		Model:     "gpt-4",
		Status:    "success",
		Object:    "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: &system}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &user1}},
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &assistant1}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &user2}},
		},
		OutputMessageParsed: &schemas.ChatMessage{
			Content: &schemas.ChatMessageContent{ContentStr: &outputText},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForUploads(t, func() bool { return objStore.Len() == 1 })

	// DB should contain the FULL input_history (all 4 messages), not just the last user message.
	dbLog, err := inner.FindByID(ctx, "exc-ih-1")
	require.NoError(t, err)
	assert.Contains(t, dbLog.InputHistory, "What is 2+2?", "full history should be in DB")
	assert.Contains(t, dbLog.InputHistory, "You are a helpful assistant.", "system message should be in DB")

	// S3 payload must NOT contain input_history.
	key := ObjectKey("test", entry.Timestamp, "exc-ih-1")
	rawPayload, err := objStore.Get(ctx, key)
	require.NoError(t, err)
	assert.NotContains(t, string(rawPayload), `"input_history":"`, "input_history must not appear in S3 payload when excluded")
	// output_message (not excluded) should be in the payload.
	assert.Contains(t, string(rawPayload), "output_message", "output_message should be in S3 payload")
}

func TestHybrid_ExcludeFields_UnknownFieldIgnored(t *testing.T) {
	// Unknown field names in excludeFields are silently ignored.
	hybrid, _, objStore := newTestHybridWithExclude(t, []string{"nonexistent_field_xyz"})
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	content := "test"
	entry := &Log{
		ID:        "exc-noop-1",
		Timestamp: time.Now().UTC(),
		Provider:  "openai",
		Model:     "gpt-4",
		Status:    "success",
		Object:    "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &content}},
		},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForUploads(t, func() bool { return objStore.Len() == 1 })

	// Standard behaviour: one object uploaded, input_history offloaded.
	assert.Equal(t, 1, objStore.Len(), "upload should succeed with unknown exclude field")
}

type outboxAgentReadBarrier struct {
	LogStore
	blockNext atomic.Bool
	loaded    chan struct{}
	resume    chan struct{}
}

// FindAgentLog pauses one database read after loading the row so tests can interleave recovery and
// an update.
func (s *outboxAgentReadBarrier) FindAgentLog(ctx context.Context, id string) (*AgentLog, error) {
	row, err := s.LogStore.FindAgentLog(ctx, id)
	if err == nil && s.blockNext.CompareAndSwap(true, false) {
		close(s.loaded)
		select {
		case <-s.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return row, err
}

// TestHybridOutboxAgentUpdateRacingRecoveryPreservesFullRequest checks that an update rereads
// recovered state before merging a body change.
func TestHybridOutboxAgentUpdateRacingRecoveryPreservesFullRequest(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	hybrid, inner, objects := newTestHybrid(t)
	ctx := context.Background()
	defer hybrid.Close(ctx)
	objects.PutErr = assert.AnError
	requestBody := `{"input":"` + strings.Repeat("a", maxA2APayloadPreviewRunes+100) + `"}`
	responseBody := `{"output":"initial"}`
	entry := &AgentLog{ID: "review-recovery-update", Timestamp: time.Now().UTC(), RecordKind: "request", Status: "success", Operation: "SendMessage", AgentName: "fixture", RequestID: "review-request", RequestBody: &requestBody, ResponseBody: &responseBody}
	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{entry})))
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	barrier := &outboxAgentReadBarrier{LogStore: inner, loaded: make(chan struct{}), resume: make(chan struct{})}
	barrier.blockNext.Store(true)
	hybrid.inner = barrier
	objects.PutErr = nil
	updated := make(chan error, 1)
	go func() {
		updated <- hybrid.UpdateAgentLog(ctx, entry.ID, map[string]interface{}{"response_body": `{"output":"updated"}`})
	}()
	select {
	case <-barrier.loaded:
	case <-time.After(time.Second):
		close(barrier.resume)
		t.Fatal("update did not load its database snapshot")
	}
	// Recovery completes after the updater read has_object=false, before it reads the outbox.
	hybrid.sweepUploadOutbox(ctx)
	close(barrier.resume)
	require.NoError(t, <-updated)
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	found, err := hybrid.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, found.RequestBody)
	t.Logf("original request bytes=%d; recovered request bytes=%d", len(requestBody), len(*found.RequestBody))
	assert.Equal(t, requestBody, *found.RequestBody, "a concurrent update must not replace a recovered full request with its DB preview")
}

// TestHybridOutboxQueueOverflowSpoolsWithoutWaitingForUnrelatedS3Put checks that disk persistence
// proceeds while a colliding network upload is blocked.
func TestHybridOutboxQueueOverflowSpoolsWithoutWaitingForUnrelatedS3Put(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "review.db")}, hybridTestLogger{})
	require.NoError(t, err)
	defer inner.Close(ctx)
	outbox, err := newUploadOutboxFromEnv()
	require.NoError(t, err)
	objects := objectstore.NewInMemoryObjectStore()
	hybrid := &HybridLogStore{inner: inner, objects: objects, prefix: "test", logger: hybridTestLogger{}, outbox: outbox, uploadQueue: make(chan *uploadWork)}
	ts := time.Now().UTC()
	firstID := "review-blocked-put"
	firstKey := ObjectKey(hybrid.prefix, ts, firstID)
	otherID, otherKey := "", ""
	for i := 0; ; i++ {
		otherID = fmt.Sprintf("review-unrelated-%d", i)
		otherKey = ObjectKey(hybrid.prefix, ts, otherID)
		if hybrid.uploadLock(otherKey) == hybrid.uploadLock(firstKey) {
			break
		}
	}
	for _, id := range []string{firstID, otherID} {
		require.NoError(t, inner.CreateIfNotExists(ctx, &Log{ID: id, Timestamp: ts, Status: "success"}))
	}
	blocked := &outboxBlockingObjectStore{InMemoryObjectStore: objects, blockedKey: firstKey, started: make(chan struct{}), release: make(chan struct{})}
	hybrid.objects = blocked
	first := &uploadWork{logID: firstID, timestamp: ts, key: firstKey, kind: uploadKindLog, queuedAt: ts, payload: []byte(`{"input_history":"full original"}`)}
	outbox.trackUpload(first)
	hybrid.pendingBytes.Add(int64(len(first.payload)))
	firstDone := make(chan struct{})
	go func() { hybrid.processUpload(first); close(firstDone) }()
	<-blocked.started
	spooled := make(chan struct{})
	go func() {
		hybrid.enqueueRawUpload(otherID, ts, otherKey, uploadKindLog, "", []byte(`{"input_history":"must reach disk"}`), nil)
		close(spooled)
	}()
	completed := false
	select {
	case <-spooled:
		completed = true
	case <-time.After(200 * time.Millisecond):
	}
	close(blocked.release)
	<-firstDone
	if !completed {
		<-spooled
	}
	assert.True(t, completed, "queue-overflow spooling must not wait for an unrelated blocked network upload")
}

// TestHybridOutboxDeleteDuringRetryDoesNotResurrectObject checks remote cleanup when a log is
// deleted during an in-flight retry, including failed deletions.
func TestHybridOutboxDeleteDuringRetryDoesNotResurrectObject(t *testing.T) {
	for _, deleteFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete-fails=%t", deleteFails), func(t *testing.T) {
			t.Setenv(uploadOutboxDirEnv, t.TempDir())
			t.Setenv(uploadOutboxMaxBytesEnv, "")
			hybrid, _, objects := newTestHybrid(t)
			ctx := context.Background()
			defer hybrid.Close(ctx)
			objects.PutErr = assert.AnError
			entry := &Log{ID: "review-delete-during-retry", Timestamp: time.Now().UTC(), Status: "success", InputHistory: `[{"role":"user","content":"delete my content"}]`}
			require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
			waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
			objects.PutErr = nil
			blocked := &outboxBlockingObjectStore{InMemoryObjectStore: objects, blockedKey: ObjectKey(hybrid.prefix, entry.Timestamp, entry.ID), started: make(chan struct{}), release: make(chan struct{})}
			deletes := &outboxDeleteFailStore{ObjectStore: blocked}
			deletes.fail.Store(deleteFails)
			hybrid.objects = deletes
			retried := make(chan struct{})
			go func() { hybrid.sweepUploadOutbox(ctx); close(retried) }()
			<-blocked.started
			deleteErr := hybrid.DeleteLog(ctx, entry.ID)
			close(blocked.release)
			<-retried
			require.NoError(t, deleteErr)
			if deleteFails {
				assert.Equal(t, 1, objects.Len(), "failed remote deletion leaves an object to clean up")
				pending, err := hybrid.outbox.read(hybrid.outbox.deletionPath(blocked.blockedKey))
				require.NoError(t, err)
				assert.JSONEq(t, `null`, string(pending.payload), "cleanup must not retain deleted request content")
				deletes.fail.Store(false)
			}
			// Retry any failed remote cleanup without putting the deleted payload again.
			hybrid.sweepUploadOutbox(ctx)
			files, _, err := hybrid.outbox.files()
			require.NoError(t, err)
			assert.Empty(t, files)
			assert.Zero(t, objects.Len(), "a deleted log must not leave an object written by its disk retry")
		})
	}
}

// TestHybridOutboxAgentStatusUpdateKeepsPendingPayload checks that a scalar status update does not
// discard a pending agent body.
func TestHybridOutboxAgentStatusUpdateKeepsPendingPayload(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	hybrid, _, objects := newTestHybrid(t)
	ctx := context.Background()
	defer hybrid.Close(ctx)
	objects.PutErr = assert.AnError
	requestBody := `{"input":"` + strings.Repeat("a", maxA2APayloadPreviewRunes+100) + `"}`
	entry := &AgentLog{ID: "review-status-update", Timestamp: time.Now().UTC(), RecordKind: "request", Status: "processing", Operation: "SendMessage", AgentName: "fixture", RequestID: "review-status-request", RequestBody: &requestBody}
	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{entry})))
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	require.NoError(t, hybrid.UpdateAgentLog(ctx, entry.ID, map[string]interface{}{"status": "error"}))
	objects.PutErr = nil
	hybrid.sweepUploadOutbox(ctx)
	found, err := hybrid.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	assert.True(t, found.HasObject, "updating scalar status must not discard the only full request payload")
	assert.Equal(t, 1, objects.Len())
}

// Each upload occupies its worker until the sweep ends, simulating an outage
// whose slow failures exceed one cycle's retry budget.
type outboxSlowObjectStore struct {
	*objectstore.InMemoryObjectStore
	attempts chan string
}

// Put records an attempted key and blocks until the sweep ends to simulate slow storage failures.
func (s *outboxSlowObjectStore) Put(ctx context.Context, key string, data []byte, tags map[string]string) error {
	s.attempts <- key
	<-ctx.Done()
	return ctx.Err()
}

// TestHybridOutboxRetryRotationDoesNotStarveLaterFiles checks that successive sweeps reach later
// files when slow failures consume each cycle.
func TestHybridOutboxRetryRotationDoesNotStarveLaterFiles(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	hybrid, inner, objects := newTestHybrid(t)
	ctx := context.Background()
	defer hybrid.Close(ctx)
	slow := &outboxSlowObjectStore{InMemoryObjectStore: objects, attempts: make(chan string, 100)}
	hybrid.objects = slow
	ts := time.Now().UTC()
	locks := make(map[any]bool)
	for i := 0; len(locks) < 4*defaultUploadWorkers; i++ {
		id := fmt.Sprintf("rotation-%d", i)
		key := ObjectKey(hybrid.prefix, ts, id)
		lock := hybrid.uploadLock(key)
		if locks[lock] {
			continue
		}
		locks[lock] = true
		require.NoError(t, inner.CreateIfNotExists(ctx, &Log{ID: id, Timestamp: ts, Status: "success"}))
		require.NoError(t, hybrid.outbox.save(&uploadWork{logID: id, key: key, timestamp: ts, queuedAt: ts, payload: []byte(`{"input_history":"pending"}`)}))
	}
	seen := make(map[string]bool)
	for range 3 {
		cycle, cancel := context.WithTimeout(ctx, 3*time.Second)
		done := make(chan struct{})
		go func() { hybrid.sweepUploadOutbox(cycle); close(done) }()
		for range defaultUploadWorkers {
			select {
			case key := <-slow.attempts:
				seen[key] = true
			case <-cycle.Done():
				t.Error("retry workers did not reach object storage")
			}
		}
		cancel()
		<-done
		for len(slow.attempts) > 0 {
			seen[<-slow.attempts] = true
		}
	}
	assert.GreaterOrEqual(t, len(seen), 2*defaultUploadWorkers, "each sweep must resume after prior attempts, even when older uploads stay slow")
	files, _, err := hybrid.outbox.files()
	require.NoError(t, err)
	assert.Len(t, files, 4*defaultUploadWorkers, "failed retries must retain their files")
	assert.Zero(t, hybrid.pendingBytes.Load())
}

type outboxDeleteFailStore struct {
	objectstore.ObjectStore
	fail atomic.Bool
}

// Delete injects a remote deletion failure while enabled, then delegates to the wrapped object
// store.
func (s *outboxDeleteFailStore) Delete(ctx context.Context, key string) error {
	if s.fail.Load() {
		return assert.AnError
	}
	return s.ObjectStore.Delete(ctx, key)
}

// DeleteBatch injects a batch deletion failure while enabled, then delegates to the wrapped object
// store.
func (s *outboxDeleteFailStore) DeleteBatch(ctx context.Context, keys []string) error {
	if s.fail.Load() {
		return assert.AnError
	}
	return s.ObjectStore.DeleteBatch(ctx, keys)
}

// TestHybridOutboxExplicitDeletionRetiresPendingContent checks content removal and cleanup retries
// for regular, MCP, and agent log deletions.
func TestHybridOutboxExplicitDeletionRetiresPendingContent(t *testing.T) {
	for _, kind := range []string{"log", "logs", "mcp", "agent"} {
		for _, deleteFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/delete-fails=%t", kind, deleteFails), func(t *testing.T) {
				t.Setenv(uploadOutboxDirEnv, t.TempDir())
				t.Setenv(uploadOutboxMaxBytesEnv, "")
				hybrid, _, objects := newTestHybrid(t)
				ctx := context.Background()
				defer hybrid.Close(ctx)
				objects.PutErr = assert.AnError
				id, ts := "erase-pending", time.Now().UTC()
				secret := `{"private":"pending-sensitive-content"}`
				switch kind {
				case "log", "logs":
					require.NoError(t, hybrid.CreateIfNotExists(ctx, &Log{ID: id, Timestamp: ts, Status: "success", InputHistory: secret}))
				case "mcp":
					require.NoError(t, hybrid.CreateMCPToolLog(ctx, &MCPToolLog{ID: id, Timestamp: ts, Status: "success", ToolName: "private-tool", Arguments: secret}))
				case "agent":
					require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{{ID: id, Timestamp: ts, RecordKind: "request", Status: "success", Operation: "SendMessage", RequestBody: &secret}})))
				}
				waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
				deletes := &outboxDeleteFailStore{ObjectStore: objects}
				deletes.fail.Store(deleteFails)
				hybrid.objects = deletes
				switch kind {
				case "log":
					require.NoError(t, hybrid.DeleteLog(ctx, id))
				case "logs":
					require.NoError(t, hybrid.DeleteLogs(ctx, []string{id}))
				case "mcp":
					require.NoError(t, hybrid.DeleteMCPToolLogs(ctx, []string{id}))
				case "agent":
					require.NoError(t, hybrid.DeleteAgentLogs(ctx, []string{id}))
				}
				files, _, err := hybrid.outbox.files()
				require.NoError(t, err)
				if deleteFails {
					require.Len(t, files, 1, "failed remote cleanup must remain retryable")
					pending, err := hybrid.outbox.read(files[0].path)
					require.NoError(t, err)
					assert.JSONEq(t, `null`, string(pending.payload), "deletion must erase content immediately, retaining only cleanup metadata")
					hybrid.sweepUploadOutbox(ctx)
					files, _, err = hybrid.outbox.files()
					require.NoError(t, err)
					assert.Len(t, files, 1, "failed cleanup must survive another sweep")
				} else {
					assert.Empty(t, files, "successful deletion must immediately retire pending disk data")
				}
				deletes.fail.Store(false)
				hybrid.sweepUploadOutbox(ctx)
				files, _, err = hybrid.outbox.files()
				require.NoError(t, err)
				assert.Empty(t, files)
				assert.Zero(t, objects.Len())
			})
		}
	}
}

// TestHybridOutboxDeletionSurvivesEvictionAndRestart checks that cleanup identifiers survive
// payload eviction and restart until the remote object can be deleted.
func TestHybridOutboxDeletionSurvivesEvictionAndRestart(t *testing.T) {
	for _, kind := range []string{"log", "logs", "mcp", "agent"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv(uploadOutboxDirEnv, t.TempDir())
			t.Setenv(uploadOutboxMaxBytesEnv, "")
			ctx := context.Background()
			dbPath := filepath.Join(t.TempDir(), "cleanup.db")
			inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: dbPath}, hybridTestLogger{})
			require.NoError(t, err)
			objects := objectstore.NewInMemoryObjectStore()
			deletes := &outboxDeleteFailStore{ObjectStore: objects}
			hybrid, err := newHybridLogStore(inner, deletes, "test", hybridTestLogger{}, nil, nil)
			require.NoError(t, err)
			defer func() { hybrid.Close(ctx) }()
			id, ts := "deleted-before-overflow", time.Now().UTC()
			secret := `{"private":"content already uploaded to object storage"}`
			switch kind {
			case "log", "logs":
				require.NoError(t, hybrid.CreateIfNotExists(ctx, &Log{ID: id, Timestamp: ts, Status: "success", InputHistory: secret}))
			case "mcp":
				require.NoError(t, hybrid.CreateMCPToolLog(ctx, &MCPToolLog{ID: id, Timestamp: ts, Status: "success", ToolName: "private-tool", Arguments: secret}))
			case "agent":
				require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{{ID: id, Timestamp: ts, RecordKind: "request", Status: "success", Operation: "SendMessage", RequestBody: &secret}})))
			}
			waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
			require.Equal(t, 1, objects.Len(), "the deleted log must have a real remote object")
			deletes.fail.Store(true)
			switch kind {
			case "log":
				require.NoError(t, hybrid.DeleteLog(ctx, id))
			case "logs":
				require.NoError(t, hybrid.DeleteLogs(ctx, []string{id}))
			case "mcp":
				require.NoError(t, hybrid.DeleteMCPToolLogs(ctx, []string{id}))
			case "agent":
				require.NoError(t, hybrid.DeleteAgentLogs(ctx, []string{id}))
			}
			files, _, err := hybrid.outbox.files()
			require.NoError(t, err)
			require.Len(t, files, 1)
			cleanupPath := files[0].path
			cleanup, err := hybrid.outbox.read(cleanupPath)
			require.NoError(t, err)
			require.JSONEq(t, `null`, string(cleanup.payload))
			present, err := hybrid.uploadLogPresent(ctx, cleanup)
			require.NoError(t, err)
			require.False(t, present, "the database no longer retains the remote object's identifier")
			hybrid.outbox.maxBytes = 1
			hybrid.sweepUploadOutbox(ctx)
			assert.FileExists(t, cleanupPath, "even a cleanup record larger than the budget must be retained")
			age := ts.Add(-time.Hour)
			require.NoError(t, os.Chtimes(cleanupPath, age, age))

			// Fill the payload budget after deletion failed. Cleanup is the oldest
			// file, but only the older of these two payloads should be evicted.
			objects.PutErr = assert.AnError
			for i := range 2 {
				require.NoError(t, hybrid.CreateIfNotExists(ctx, &Log{ID: fmt.Sprintf("overflow-%d", i), Timestamp: ts, Status: "success", InputHistory: secret}))
			}
			waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
			oldPath := hybrid.outbox.path(ObjectKey("test", ts, "overflow-0"))
			newPath := hybrid.outbox.path(ObjectKey("test", ts, "overflow-1"))
			for i, path := range []string{oldPath, newPath} {
				mtime := age.Add(time.Duration(i+1) * time.Minute)
				require.NoError(t, os.Chtimes(path, mtime, mtime))
			}
			info, err := os.Stat(newPath)
			require.NoError(t, err)
			t.Setenv(uploadOutboxMaxBytesEnv, fmt.Sprint(info.Size()))
			hybrid.outbox.maxBytes = info.Size()
			hybrid.sweepUploadOutbox(ctx)
			assert.FileExists(t, cleanupPath, "payload eviction must retain failed remote deletion identifiers")
			assert.NoFileExists(t, oldPath)
			assert.FileExists(t, newPath)
			assert.Equal(t, int64(1), hybrid.DroppedUploads())

			require.NoError(t, hybrid.Close(ctx))
			inner, err = newSqliteLogStore(ctx, &SQLiteConfig{Path: dbPath}, hybridTestLogger{})
			require.NoError(t, err)
			hybrid, err = newHybridLogStore(inner, deletes, "test", hybridTestLogger{}, nil, nil)
			require.NoError(t, err)
			hybrid.sweepUploadOutbox(ctx)
			assert.FileExists(t, cleanupPath, "failed cleanup must survive restart and another quota sweep")
			assert.FileExists(t, newPath, "cleanup metadata must not consume the upload payload budget")
			require.Equal(t, 1, objects.Len())
			objects.PutErr = nil
			deletes.fail.Store(false)
			hybrid.sweepUploadOutbox(ctx)
			_, err = objects.Get(ctx, cleanup.key)
			assert.Error(t, err, "recovery must delete the object whose database row is gone")
			found, err := inner.FindByID(ctx, "overflow-1")
			require.NoError(t, err)
			assert.True(t, found.HasObject)
			assert.Equal(t, 1, objects.Len(), "only the surviving upload should remain in object storage")
			files, _, err = hybrid.outbox.files()
			require.NoError(t, err)
			assert.Empty(t, files)
		})
	}
}

// TestUploadOutboxDeletionCannotBeReplacedOrRemovedByUpload checks that late upload completions
// cannot erase or replace protected deletion metadata.
func TestUploadOutboxDeletionCannotBeReplacedOrRemovedByUpload(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "1")
	outbox, err := newUploadOutboxFromEnv()
	require.NoError(t, err)
	work := &uploadWork{logID: "deleted", timestamp: time.Now().UTC(), key: "test/deleted", payload: []byte(`{"secret":"must be erased"}`), status: "success", tags: map[string]string{"private": "tag"}}
	outbox.trackUpload(work)
	defer outbox.finishUpload(work)
	require.NoError(t, outbox.save(work))
	cleanup := *work
	cleanup.deleteOnly = true
	newer := *work
	newer.queuedAt = work.queuedAt.Add(time.Second)
	outbox.trackUpload(&newer)
	defer outbox.finishUpload(&newer)
	// Even a newer queued generation cannot prevent an explicit deletion.
	require.NoError(t, outbox.save(&cleanup))
	assert.NoFileExists(t, outbox.path(work.key))
	assert.Same(t, outbox.fileLock(outbox.path(work.key)), outbox.fileLock(outbox.deletionPath(work.key)))
	hybrid := &HybridLogStore{outbox: outbox}
	assert.Same(t, hybrid.uploadLock(work.key), hybrid.outboxFileLock(outbox.deletionPath(work.key)))
	// A delayed failure must not restore content; a delayed success must not
	// erase the outstanding cleanup record, regardless of its queue timestamp.
	require.NoError(t, outbox.save(&newer))
	require.NoError(t, outbox.removeThrough(&newer))
	pending, err := outbox.read(outbox.deletionPath(work.key))
	require.NoError(t, err)
	assert.True(t, pending.deleteOnly)
	assert.JSONEq(t, `null`, string(pending.payload))
	assert.Empty(t, pending.tags)
	assert.Empty(t, pending.status)
	files, size, err := outbox.files()
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.True(t, files[0].deleteOnly)
	assert.Zero(t, size, "deletion identifiers do not consume the upload budget")
	assert.Equal(t, os.FileMode(0600), files[0].info.Mode().Perm())
	require.NoError(t, outbox.removeThrough(pending))
	assert.NoFileExists(t, outbox.deletionPath(work.key))
}

// TestHybridOutboxSameKeyOverflowSurvivesOlderUpload checks that an older upload completion
// preserves a newer snapshot written by queue overflow.
func TestHybridOutboxSameKeyOverflowSurvivesOlderUpload(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("older-put-fails=%t", failed), func(t *testing.T) {
			t.Setenv(uploadOutboxDirEnv, t.TempDir())
			t.Setenv(uploadOutboxMaxBytesEnv, "")
			ctx := context.Background()
			inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "samekey.db")}, hybridTestLogger{})
			require.NoError(t, err)
			defer inner.Close(ctx)
			outbox, err := newUploadOutboxFromEnv()
			require.NoError(t, err)
			objects := objectstore.NewInMemoryObjectStore()
			if failed {
				objects.PutErr = assert.AnError
			}
			id, ts := "same-key-overflow", time.Now().UTC()
			key := ObjectKey("test", ts, id)
			require.NoError(t, inner.CreateIfNotExists(ctx, &Log{ID: id, Timestamp: ts, Status: "success"}))
			blocked := &outboxBlockingObjectStore{InMemoryObjectStore: objects, blockedKey: key, started: make(chan struct{}), release: make(chan struct{})}
			hybrid := &HybridLogStore{inner: inner, objects: blocked, prefix: "test", logger: hybridTestLogger{}, outbox: outbox, uploadQueue: make(chan *uploadWork)}
			older := &uploadWork{logID: id, timestamp: ts, queuedAt: ts, key: key, payload: []byte(`{"input_history":"older"}`)}
			outbox.trackUpload(older)
			hybrid.pendingBytes.Add(int64(len(older.payload)))
			done := make(chan struct{})
			go func() { hybrid.processUpload(older); close(done) }()
			<-blocked.started
			spooled := make(chan struct{})
			newer := []byte(`{"input_history":"newer full content"}`)
			go func() { hybrid.enqueueRawUpload(id, ts, key, uploadKindLog, "", newer, nil); close(spooled) }()
			select {
			case <-spooled:
			case <-time.After(time.Second):
				close(blocked.release)
				<-done
				<-spooled
				t.Fatal("same-key overflow waited for the network")
			}
			pending, readErr := outbox.read(outbox.path(key))
			close(blocked.release)
			<-done
			require.NoError(t, readErr)
			assert.JSONEq(t, string(newer), string(pending.payload))
			pending, err = outbox.read(outbox.path(key))
			require.NoError(t, err)
			assert.JSONEq(t, string(newer), string(pending.payload), "older completion must neither remove nor replace newer disk data")
			hybrid.objects = objects
			objects.PutErr = nil
			hybrid.sweepUploadOutbox(ctx)
			stored, err := objects.Get(ctx, key)
			require.NoError(t, err)
			assert.JSONEq(t, string(newer), string(stored))
			assert.Zero(t, hybrid.pendingBytes.Load())
			assert.Empty(t, outbox.active)
		})
	}
}

// TestHybridOutboxMCPStatusChangePreservesPayloadAndCurrentMetadata checks that retry combines full
// pending MCP arguments with the current database status.
func TestHybridOutboxMCPStatusChangePreservesPayloadAndCurrentMetadata(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	hybrid, inner, objects := newTestHybrid(t)
	ctx := context.Background()
	defer hybrid.Close(ctx)
	objects.PutErr = assert.AnError
	arguments := map[string]any{"input": strings.Repeat("full-argument-", 100)}
	entry := &MCPToolLog{ID: "mcp-status-change", Timestamp: time.Now().UTC(), Status: "processing", ToolName: "tool", ArgumentsParsed: arguments}
	require.NoError(t, hybrid.CreateMCPToolLog(ctx, entry))
	waitForUploads(t, func() bool { return hybrid.pendingBytes.Load() == 0 })
	// Represents a newer DB state committed before its replacement upload.
	require.NoError(t, inner.UpdateMCPToolLog(ctx, entry.ID, map[string]interface{}{"status": "error"}))
	objects.PutErr = nil
	hybrid.sweepUploadOutbox(ctx)
	found, err := hybrid.FindMCPToolLog(ctx, entry.ID)
	require.NoError(t, err)
	assert.True(t, found.HasObject)
	assert.Equal(t, "error", found.Status)
	assert.Equal(t, arguments, found.ArgumentsParsed)
}

// TestHybridOutboxAgentUpdateCannotReplaceQueuedFullPayloadWithPreview checks that successive agent
// updates merge queued full bodies even when the queue overflows.
func TestHybridOutboxAgentUpdateCannotReplaceQueuedFullPayloadWithPreview(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "queued.db")}, hybridTestLogger{})
	require.NoError(t, err)
	defer inner.Close(ctx)
	outbox, err := newUploadOutboxFromEnv()
	require.NoError(t, err)
	objects := objectstore.NewInMemoryObjectStore()
	hybrid := &HybridLogStore{inner: inner, objects: objects, prefix: "test", logger: hybridTestLogger{}, outbox: outbox, uploadQueue: make(chan *uploadWork, 2)}
	request := strings.Repeat("full request ", maxA2APayloadPreviewRunes)
	entry := &AgentLog{ID: "queued-agent", Timestamp: time.Now().UTC(), RecordKind: "request", Status: "processing", Operation: "SendMessage", RequestBody: &request}
	require.NoError(t, agentLogsCreateError(hybrid.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{entry})))
	// Both the initial request and its first update are still queued. The
	// next partial update must merge the newest complete in-memory snapshot.
	require.NoError(t, hybrid.UpdateAgentLog(ctx, entry.ID, map[string]interface{}{"status": "success", "response_body": `{"result":"done"}`}))
	require.NoError(t, hybrid.UpdateAgentLog(ctx, entry.ID, map[string]interface{}{"event_body": `{"event":"last"}`}))
	// The third version overflows the two-slot queue and reaches disk. Neither
	// superseded queued version may replace it or retain an unaccounted payload.
	hybrid.processUpload(<-hybrid.uploadQueue)
	hybrid.processUpload(<-hybrid.uploadQueue)
	assert.Empty(t, outbox.active)
	hybrid.sweepUploadOutbox(ctx)
	recovered, err := hybrid.FindAgentLog(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, recovered.RequestBody)
	assert.Equal(t, request, *recovered.RequestBody)
	assert.Equal(t, "success", recovered.Status)
	require.NotNil(t, recovered.ResponseBody)
	require.NotNil(t, recovered.EventBody)
	assert.JSONEq(t, `{"result":"done"}`, *recovered.ResponseBody)
	assert.JSONEq(t, `{"event":"last"}`, *recovered.EventBody)
}

// TestHybridOutboxCancelledRetryDoesNotWaitForBusyUploadLock checks that an already-canceled retry
// returns before waiting on network work.
func TestHybridOutboxCancelledRetryDoesNotWaitForBusyUploadLock(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	hybrid, _, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	key := ObjectKey(hybrid.prefix, time.Now().UTC(), "busy-key")
	lock := hybrid.uploadLock(key)
	lock.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { hybrid.retryOutboxFile(ctx, hybrid.outbox.path(key)); close(done) }()
	returned := false
	select {
	case <-done:
		returned = true
	case <-time.After(200 * time.Millisecond):
	}
	lock.Unlock()
	<-done
	assert.True(t, returned, "a cancelled sweep must not wait behind live network I/O")
}

// TestHybridOutboxMCPUpdatesMergeLatestQueuedPayload checks that MCP updates prefer newer queued
// content over an older disk snapshot.
func TestHybridOutboxMCPUpdatesMergeLatestQueuedPayload(t *testing.T) {
	t.Setenv(uploadOutboxDirEnv, t.TempDir())
	t.Setenv(uploadOutboxMaxBytesEnv, "")
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "queuedmcp.db")}, hybridTestLogger{})
	require.NoError(t, err)
	defer inner.Close(ctx)
	outbox, err := newUploadOutboxFromEnv()
	require.NoError(t, err)
	objects := objectstore.NewInMemoryObjectStore()
	hybrid := &HybridLogStore{inner: inner, objects: objects, prefix: "test", logger: hybridTestLogger{}, outbox: outbox, uploadQueue: make(chan *uploadWork, 2)}
	arguments := map[string]any{"input": strings.Repeat("full-argument-", 100)}
	entry := &MCPToolLog{ID: "queued-mcp", Timestamp: time.Now().UTC(), ToolName: "tool", Status: "processing", ArgumentsParsed: arguments}
	require.NoError(t, hybrid.CreateMCPToolLog(ctx, entry))
	original := <-hybrid.uploadQueue
	// An older disk snapshot can coexist with a newer queued update.
	require.NoError(t, outbox.save(original))
	hybrid.uploadQueue <- original
	result := map[string]any{"output": "complete result"}
	require.NoError(t, hybrid.UpdateMCPToolLog(ctx, entry.ID, MCPToolLog{Status: "success", ResultParsed: result}))
	require.NoError(t, hybrid.UpdateMCPToolLog(ctx, entry.ID, MCPToolLog{MetadataParsed: map[string]any{"phase": "finished"}}))
	hybrid.processUpload(<-hybrid.uploadQueue)
	hybrid.processUpload(<-hybrid.uploadQueue)
	hybrid.sweepUploadOutbox(ctx)
	recovered, err := hybrid.FindMCPToolLog(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, arguments, recovered.ArgumentsParsed)
	assert.Equal(t, result, recovered.ResultParsed)
	assert.Equal(t, "success", recovered.Status)
	assert.Equal(t, "finished", recovered.MetadataParsed["phase"])
	assert.Empty(t, outbox.active)
	assert.Zero(t, hybrid.pendingBytes.Load())
}
