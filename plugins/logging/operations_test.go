package logging

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func newTestStore(t *testing.T) logstore.LogStore {
	t.Helper()

	store, err := logstore.NewLogStore(context.Background(), &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{
			Path: filepath.Join(t.TempDir(), "logging.db"),
		},
	}, testLogger{})
	if err != nil {
		t.Fatalf("NewLogStore() error = %v", err)
	}
	return store
}

// TestMCPHooksDeferDBWriteUntilPostHookBatch verifies MCP logs are kept in
// memory after PreMCPHook and persisted by the batch writer after PostMCPHook.
func TestMCPHooksDeferDBWriteUntilPostHookBatch(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-mcp-batch")
	ctx.SetValue(schemas.BifrostContextKeyMCPLogID, "mcp-batch-flow")

	toolName := "docs-search"
	_, _, err = plugin.PreMCPHook(ctx, &schemas.BifrostMCPRequest{
		RequestType: schemas.MCPRequestTypeChatToolCall,
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      &toolName,
				Arguments: `{"query":"find this"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("PreMCPHook() error = %v", err)
	}

	if _, err := store.FindMCPToolLog(context.Background(), "mcp-batch-flow"); !errors.Is(err, logstore.ErrNotFound) {
		t.Fatalf("expected MCP log to stay in memory before PostMCPHook, got err=%v", err)
	}

	result := `{"answer":"done"}`
	_, _, err = plugin.PostMCPHook(ctx, &schemas.BifrostMCPResponse{
		ChatMessage: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleTool,
			Content: &schemas.ChatMessageContent{ContentStr: &result},
		},
		ExtraFields: schemas.BifrostMCPResponseExtraFields{
			ClientName: "docs",
			ToolName:   "search",
			Latency:    42,
		},
	}, nil)
	if err != nil {
		t.Fatalf("PostMCPHook() error = %v", err)
	}

	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	logEntry, err := store.FindMCPToolLog(context.Background(), "mcp-batch-flow")
	if err != nil {
		t.Fatalf("FindMCPToolLog() error = %v", err)
	}
	if logEntry.Status != "success" {
		t.Fatalf("expected status success, got %q", logEntry.Status)
	}
	if logEntry.ArgumentsParsed == nil {
		t.Fatalf("expected arguments to be persisted")
	}
	resultMap, ok := logEntry.ResultParsed.(map[string]interface{})
	if !ok || resultMap["answer"] != "done" {
		t.Fatalf("expected parsed result to be persisted, got %#v", logEntry.ResultParsed)
	}
	if logEntry.Latency == nil || *logEntry.Latency != 42 {
		t.Fatalf("expected latency 42, got %#v", logEntry.Latency)
	}
}

// TestCleanupStalePendingMCPLogsPersistsErrorFallback verifies stale pending
// MCP logs are committed as terminal errors instead of being silently dropped.
func TestCleanupStalePendingMCPLogsPersistsErrorFallback(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	staleCreatedAt := time.Now().Add(-pendingLogTTL - time.Minute)
	plugin.pendingMCPLogsToInject.Store("mcp-stale", &logstore.MCPToolLog{
		ID:          "mcp-stale",
		RequestID:   "req-stale",
		Timestamp:   staleCreatedAt,
		ToolName:    "search",
		ServerLabel: "docs",
		Status:      "processing",
		CreatedAt:   staleCreatedAt,
		ArgumentsParsed: map[string]interface{}{
			"query": "stale input",
		},
	})

	plugin.cleanupStalePendingLogs()

	if _, ok := plugin.pendingMCPLogsToInject.Load("mcp-stale"); ok {
		t.Fatal("expected stale MCP pending log to be removed from memory")
	}
	if _, err := store.FindMCPToolLog(context.Background(), "mcp-stale"); !errors.Is(err, logstore.ErrNotFound) {
		t.Fatalf("expected stale MCP log to be queued before batch flush, got err=%v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	logEntry, err := store.FindMCPToolLog(context.Background(), "mcp-stale")
	if err != nil {
		t.Fatalf("FindMCPToolLog() error = %v", err)
	}
	if logEntry.Status != "error" {
		t.Fatalf("expected status error, got %q", logEntry.Status)
	}
	if logEntry.ArgumentsParsed == nil {
		t.Fatal("expected stale MCP input arguments to be persisted")
	}
	if logEntry.ResultParsed != nil || logEntry.Result != "" {
		t.Fatalf("expected stale MCP log to have no result, got parsed=%#v raw=%q", logEntry.ResultParsed, logEntry.Result)
	}
	if logEntry.ErrorDetailsParsed == nil || logEntry.ErrorDetailsParsed.Error == nil {
		t.Fatalf("expected stale MCP error details, got %#v", logEntry.ErrorDetailsParsed)
	}
	if !strings.Contains(logEntry.ErrorDetailsParsed.Error.Message, "pending log TTL") {
		t.Fatalf("expected stale MCP timeout message, got %q", logEntry.ErrorDetailsParsed.Error.Message)
	}
}

func TestUpdateLogEntryPreservesResponsesInputContentSummary(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:  store,
		logger: testLogger{},
	}

	requestID := "req-1"
	now := time.Now().UTC()
	inputText := "request-side text"
	initial := &InitialLogData{
		Object:   "responses",
		Provider: "openai",
		Model:    "gpt-4o-mini",
		ResponsesInputHistory: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{
				ContentStr: &inputText,
			},
		}},
	}

	if err := plugin.insertInitialLogEntry(context.Background(), requestID, "", now, 0, nil, initial); err != nil {
		t.Fatalf("insertInitialLogEntry() error = %v", err)
	}

	responsesText := "responses output"
	update := &UpdateLogData{
		Status: "success",
		ResponsesOutput: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{
				ContentStr: &responsesText,
			},
		}},
	}

	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update, true); err != nil {
		t.Fatalf("updateLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if !strings.Contains(logEntry.ContentSummary, inputText) {
		t.Fatalf("expected content summary to preserve responses input, got %q", logEntry.ContentSummary)
	}
	if strings.Contains(logEntry.ContentSummary, responsesText) {
		t.Fatalf("expected content summary to avoid overwriting with responses output-only data, got %q", logEntry.ContentSummary)
	}
}

func TestUpdateLogEntryUpdatesContentSummaryForChatOutput(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:  store,
		logger: testLogger{},
	}

	requestID := "req-chat"
	now := time.Now().UTC()
	initial := &InitialLogData{
		Object:   "chat_completion",
		Provider: "openai",
		Model:    "gpt-4o-mini",
	}

	if err := plugin.insertInitialLogEntry(context.Background(), requestID, "", now, 0, nil, initial); err != nil {
		t.Fatalf("insertInitialLogEntry() error = %v", err)
	}

	chatText := "assistant output"
	update := &UpdateLogData{
		Status: "success",
		ChatOutput: &schemas.ChatMessage{
			Role: schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{
				ContentStr: &chatText,
			},
		},
	}

	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update, true); err != nil {
		t.Fatalf("updateLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if !strings.Contains(logEntry.ContentSummary, chatText) {
		t.Fatalf("expected content summary to include chat output, got %q", logEntry.ContentSummary)
	}
}

func TestUpdateLogEntrySuppressesChatOutputWhenContentLoggingDisabled(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:  store,
		logger: testLogger{},
	}

	requestID := "req-chat-disabled"
	now := time.Now().UTC()
	initial := &InitialLogData{
		Object:   "chat_completion",
		Provider: "openai",
		Model:    "gpt-4o-mini",
	}

	if err := plugin.insertInitialLogEntry(context.Background(), requestID, "", now, 0, nil, initial); err != nil {
		t.Fatalf("insertInitialLogEntry() error = %v", err)
	}

	chatText := "assistant output should not be logged"
	update := &UpdateLogData{
		Status: "success",
		ChatOutput: &schemas.ChatMessage{
			Role: schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{
				ContentStr: &chatText,
			},
		},
	}

	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update, false); err != nil {
		t.Fatalf("updateLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.OutputMessage != "" {
		t.Fatalf("expected output_message to be suppressed, got %q", logEntry.OutputMessage)
	}
	if strings.Contains(logEntry.ContentSummary, chatText) {
		t.Fatalf("expected content summary to suppress chat output, got %q", logEntry.ContentSummary)
	}
}

func TestStoreOrEnqueueRetryPreservesAllEntries(t *testing.T) {
	// Simulate fallback/retry scenario where multiple PostLLMHook calls
	// store entries under the same traceID. All entries must be preserved.
	plugin := &LoggerPlugin{
		logger:     testLogger{},
		writeQueue: make(chan *writeQueueEntry, 10),
	}

	traceID := "trace-retry-test"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, traceID)

	// Simulate 3 retry attempts storing entries under the same traceID
	entry1 := &logstore.Log{ID: "req-attempt-1", Model: "gpt-4o"}
	entry2 := &logstore.Log{ID: "req-attempt-2", Model: "gpt-4o"}
	entry3 := &logstore.Log{ID: "req-attempt-3", Model: "claude-3-5-sonnet"}

	plugin.storeOrEnqueueEntry(ctx, entry1, nil)
	plugin.storeOrEnqueueEntry(ctx, entry2, nil)
	plugin.storeOrEnqueueEntry(ctx, entry3, nil)

	// Verify all 3 entries are stored
	val, ok := plugin.pendingLogsToInject.Load(traceID)
	if !ok {
		t.Fatal("expected pending entries for traceID, got none")
	}
	pending, ok := val.(*pendingInjectEntries)
	if !ok {
		t.Fatal("expected *pendingInjectEntries type")
	}
	if len(pending.entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(pending.entries))
	}
	if pending.entries[0].ID != "req-attempt-1" || pending.entries[1].ID != "req-attempt-2" || pending.entries[2].ID != "req-attempt-3" {
		t.Fatalf("entries not in expected order: %v, %v, %v", pending.entries[0].ID, pending.entries[1].ID, pending.entries[2].ID)
	}

	// Now test Inject flushes all entries with plugin logs attached
	trace := &schemas.Trace{
		TraceID: traceID,
		PluginLogs: []schemas.PluginLogEntry{
			{PluginName: "hello-world", Level: schemas.LogLevelInfo, Message: "test log"},
		},
	}

	if err := plugin.Inject(context.Background(), trace); err != nil {
		t.Fatalf("Inject() error = %v", err)
	}

	// Verify all 3 entries were enqueued to writeQueue
	if len(plugin.writeQueue) != 3 {
		t.Fatalf("expected 3 entries in writeQueue, got %d", len(plugin.writeQueue))
	}

	// Verify plugin logs were attached to each entry
	for i := 0; i < 3; i++ {
		qe := <-plugin.writeQueue
		if qe.log.PluginLogs == "" {
			t.Fatalf("entry %d: expected PluginLogs to be set", i)
		}
	}

	// Verify pendingLogsToInject was cleaned up
	if _, ok := plugin.pendingLogsToInject.Load(traceID); ok {
		t.Fatal("expected pendingLogsToInject to be cleaned up after Inject")
	}
}

func TestConvertToProcessedStreamResponseUsesResponsesStreamTypeForWebSocketResponses(t *testing.T) {
	result := &schemas.StreamAccumulatorResult{
		RequestID:      "req-ws-3000",
		RequestedModel: "gpt-4o-mini",
		ResolvedModel:  "gpt-4o-mini",
		Provider:       schemas.OpenAI,
		Status:         "success",
	}

	processed := convertToProcessedStreamResponse(result, schemas.WebSocketResponsesRequest)
	if processed == nil {
		t.Fatal("expected processed stream response, got nil")
	}
	if processed.StreamType != "responses" {
		t.Fatalf("expected stream type responses, got %s", processed.StreamType)
	}
}

func TestApplyRealtimeOutputToEntryBackfillsUserTranscriptFromRawRequest(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "Hello!"
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest:  `{"type":"conversation.item.input_audio_transcription.completed","transcript":"Hello."}`,
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 1 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 1", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Role != schemas.ChatMessageRoleUser {
		t.Fatalf("InputHistoryParsed[0].Role = %q, want user", entry.InputHistoryParsed[0].Role)
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != "Hello." {
		t.Fatalf("InputHistoryParsed[0] = %+v, want transcript", entry.InputHistoryParsed[0])
	}
	if entry.OutputMessageParsed == nil || entry.OutputMessageParsed.Content == nil || entry.OutputMessageParsed.Content.ContentStr == nil || *entry.OutputMessageParsed.Content.ContentStr != assistantText {
		t.Fatalf("OutputMessageParsed = %+v, want assistant text", entry.OutputMessageParsed)
	}
	if !strings.Contains(entry.ContentSummary, "Hello.") {
		t.Fatalf("ContentSummary = %q, want user transcript", entry.ContentSummary)
	}
	if !strings.Contains(entry.ContentSummary, "Hello!") {
		t.Fatalf("ContentSummary = %q, want assistant text", entry.ContentSummary)
	}
}

func TestApplyRealtimeOutputToEntryBackfillsMissingTranscriptPlaceholder(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "Hi there!"
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest:  `{"type":"conversation.item.input_audio_transcription.completed","transcript":""}`,
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 1 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 1", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != realtimeMissingTranscriptText {
		t.Fatalf("InputHistoryParsed[0] = %+v, want missing transcript placeholder", entry.InputHistoryParsed[0])
	}
	if !strings.Contains(entry.ContentSummary, realtimeMissingTranscriptText) {
		t.Fatalf("ContentSummary = %q, want missing transcript placeholder", entry.ContentSummary)
	}
}

func TestApplyRealtimeOutputToEntryBackfillsDoneMissingTranscriptPlaceholder(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "Hi there!"
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest:  `{"type":"conversation.item.done","item":{"id":"item_user","type":"message","role":"user","status":"completed","content":[{"type":"input_audio","transcript":null}]}}`,
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 1 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 1", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != realtimeMissingTranscriptText {
		t.Fatalf("InputHistoryParsed[0] = %+v, want missing transcript placeholder", entry.InputHistoryParsed[0])
	}
}

func TestApplyRealtimeOutputToEntryBackfillsRetrievedUserAndToolHistory(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "I checked that for you."
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest: strings.Join([]string{
					`{"type":"conversation.item.retrieved","item":{"id":"item_user","type":"message","role":"user","status":"completed","content":[{"type":"input_text","text":"Where is my order?"}]}}`,
					`{"type":"conversation.item.retrieved","item":{"id":"item_tool","type":"function_call_output","call_id":"call_123","status":"completed","output":"{\"status\":\"delivered\"}"}}`,
				}, "\n\n"),
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 2 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 2", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Role != schemas.ChatMessageRoleUser {
		t.Fatalf("InputHistoryParsed[0].Role = %q, want user", entry.InputHistoryParsed[0].Role)
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != "Where is my order?" {
		t.Fatalf("InputHistoryParsed[0] = %+v, want user content", entry.InputHistoryParsed[0])
	}
	if entry.InputHistoryParsed[1].Role != schemas.ChatMessageRoleTool {
		t.Fatalf("InputHistoryParsed[1].Role = %q, want tool", entry.InputHistoryParsed[1].Role)
	}
	if entry.InputHistoryParsed[1].Content == nil || entry.InputHistoryParsed[1].Content.ContentStr == nil || *entry.InputHistoryParsed[1].Content.ContentStr != `{"status":"delivered"}` {
		t.Fatalf("InputHistoryParsed[1] = %+v, want tool content", entry.InputHistoryParsed[1])
	}
	if entry.InputHistoryParsed[1].ChatToolMessage == nil || entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID == nil || *entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID != "call_123" {
		t.Fatalf("InputHistoryParsed[1].ChatToolMessage = %+v, want tool call id", entry.InputHistoryParsed[1].ChatToolMessage)
	}
}

func TestApplyRealtimeOutputToEntryBackfillsCreatedUserAndToolHistory(t *testing.T) {
	t.Parallel()

	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RawRequest: strings.Join([]string{
					`{"type":"conversation.item.created","item":{"id":"item_user","type":"message","role":"user","status":"completed","content":[{"type":"input_text","text":"I need help"}]}}`,
					`{"type":"conversation.item.created","item":{"id":"item_tool","type":"function_call_output","call_id":"call_456","status":"completed","output":"{\"status\":\"ok\"}"}}`,
				}, "\n\n"),
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)

	if len(entry.InputHistoryParsed) != 2 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 2", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Role != schemas.ChatMessageRoleUser {
		t.Fatalf("InputHistoryParsed[0].Role = %q, want user", entry.InputHistoryParsed[0].Role)
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != "I need help" {
		t.Fatalf("InputHistoryParsed[0] = %+v, want user content", entry.InputHistoryParsed[0])
	}
	if entry.InputHistoryParsed[1].Role != schemas.ChatMessageRoleTool {
		t.Fatalf("InputHistoryParsed[1].Role = %q, want tool", entry.InputHistoryParsed[1].Role)
	}
	if entry.InputHistoryParsed[1].Content == nil || entry.InputHistoryParsed[1].Content.ContentStr == nil || *entry.InputHistoryParsed[1].Content.ContentStr != `{"status":"ok"}` {
		t.Fatalf("InputHistoryParsed[1] = %+v, want tool content", entry.InputHistoryParsed[1])
	}
	if entry.InputHistoryParsed[1].ChatToolMessage == nil || entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID == nil || *entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID != "call_456" {
		t.Fatalf("InputHistoryParsed[1].ChatToolMessage = %+v, want tool call id", entry.InputHistoryParsed[1].ChatToolMessage)
	}
}

func TestApplyRealtimeOutputToEntryBackfillsAddedUserAndToolHistory(t *testing.T) {
	t.Parallel()

	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "Done."
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest: strings.Join([]string{
					`{"type":"conversation.item.added","item":{"id":"item_user","type":"message","role":"user","status":"completed","content":[{"type":"input_text","text":"hello from added item"}]}}`,
					`{"type":"conversation.item.added","item":{"id":"item_tool","type":"function_call_output","call_id":"call_added","status":"completed","output":"{\"status\":\"ok\"}"}}`,
				}, "\n\n"),
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 2 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 2", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != "hello from added item" {
		t.Fatalf("InputHistoryParsed[0] = %+v, want added user content", entry.InputHistoryParsed[0])
	}
	if entry.InputHistoryParsed[1].ChatToolMessage == nil || entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID == nil || *entry.InputHistoryParsed[1].ChatToolMessage.ToolCallID != "call_added" {
		t.Fatalf("InputHistoryParsed[1].ChatToolMessage = %+v, want added tool call id", entry.InputHistoryParsed[1].ChatToolMessage)
	}
}

func TestApplyRealtimeOutputToEntryMergesRawTranscriptIntoStructuredRealtimeHistory(t *testing.T) {
	t.Parallel()

	plugin := &LoggerPlugin{}
	entry := &logstore.Log{
		InputHistoryParsed: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Can you help with my ticket?"),
				},
			},
			{
				Role: schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr(`{"status":"open"}`),
				},
				ChatToolMessage: &schemas.ChatToolMessage{
					ToolCallID: schemas.Ptr("call_789"),
				},
			},
		},
	}

	assistantText := "Let me check."
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest: strings.Join([]string{
					`{"type":"conversation.item.input_audio_transcription.completed","transcript":"Hello."}`,
					`{"type":"conversation.item.retrieved","item":{"id":"item_tool","type":"function_call_output","call_id":"call_789","status":"completed","output":"{\"status\":\"open\"}"}}`,
				}, "\n\n"),
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, true, true)
	if err := entry.SerializeFields(); err != nil {
		t.Fatalf("SerializeFields() error = %v", err)
	}

	if len(entry.InputHistoryParsed) != 3 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 3", len(entry.InputHistoryParsed))
	}
	if entry.InputHistoryParsed[0].Content == nil || entry.InputHistoryParsed[0].Content.ContentStr == nil || *entry.InputHistoryParsed[0].Content.ContentStr != "Can you help with my ticket?" {
		t.Fatalf("InputHistoryParsed[0] = %+v, want structured user content", entry.InputHistoryParsed[0])
	}
	if entry.InputHistoryParsed[1].Role != schemas.ChatMessageRoleUser {
		t.Fatalf("InputHistoryParsed[1].Role = %q, want user", entry.InputHistoryParsed[1].Role)
	}
	if entry.InputHistoryParsed[1].Content == nil || entry.InputHistoryParsed[1].Content.ContentStr == nil || *entry.InputHistoryParsed[1].Content.ContentStr != "Hello." {
		t.Fatalf("InputHistoryParsed[1] = %+v, want raw transcript merge", entry.InputHistoryParsed[1])
	}
	if entry.InputHistoryParsed[2].Role != schemas.ChatMessageRoleTool {
		t.Fatalf("InputHistoryParsed[2].Role = %q, want tool", entry.InputHistoryParsed[2].Role)
	}
	if entry.InputHistoryParsed[2].ChatToolMessage == nil || entry.InputHistoryParsed[2].ChatToolMessage.ToolCallID == nil || *entry.InputHistoryParsed[2].ChatToolMessage.ToolCallID != "call_789" {
		t.Fatalf("InputHistoryParsed[2].ChatToolMessage = %+v, want original tool call id", entry.InputHistoryParsed[2].ChatToolMessage)
	}
	if strings.Count(entry.ContentSummary, "Hello.") != 1 {
		t.Fatalf("ContentSummary = %q, want one merged transcript", entry.ContentSummary)
	}
}

func TestApplyRealtimeOutputToEntryDoesNotPersistRawWhenShouldStoreRawFalse(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	assistantText := "Hello!"
	messageType := schemas.ResponsesMessageTypeMessage
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	result := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Output: []schemas.ResponsesMessage{{
				Type: &messageType,
				Role: &assistantRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &assistantText,
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RealtimeRequest,
				RawRequest:  `{"type":"conversation.item.input_audio_transcription.completed","transcript":"Hello."}`,
				RawResponse: `{"type":"response.done"}`,
			},
		},
	}

	plugin.applyRealtimeOutputToEntry(entry, result, false, true)

	if entry.RawRequest != "" {
		t.Fatalf("expected RawRequest to remain empty when shouldStoreRaw=false, got %q", entry.RawRequest)
	}
	if entry.RawResponse != "" {
		t.Fatalf("expected RawResponse to remain empty when shouldStoreRaw=false, got %q", entry.RawResponse)
	}
	if len(entry.InputHistoryParsed) == 0 {
		t.Fatal("expected InputHistoryParsed to still be backfilled when shouldStoreRaw=false")
	}
	if entry.InputHistoryParsed[0].Role != schemas.ChatMessageRoleUser {
		t.Fatalf("InputHistoryParsed[0].Role = %q, want user", entry.InputHistoryParsed[0].Role)
	}
}

// TestContentLoggingEnabledHelper verifies precedence: ctx override > global config > default-enabled.
func TestContentLoggingEnabledHelper(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name          string
		globalDisable *bool
		ctxOverride   *bool // nil = don't set the key
		want          bool
	}{
		{"no config no override → enabled", nil, nil, true},
		{"global disable=false no override → enabled", boolPtr(false), nil, true},
		{"global disable=true no override → disabled", boolPtr(true), nil, false},
		{"ctx override=false global disable=true → enabled", boolPtr(true), boolPtr(false), true},
		{"ctx override=true global disable=false → disabled", boolPtr(false), boolPtr(true), false},
		{"ctx override=true nil global → disabled", nil, boolPtr(true), false},
		{"ctx override=false nil global → enabled", nil, boolPtr(false), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &LoggerPlugin{disableContentLogging: tc.globalDisable}

			var ctx *schemas.BifrostContext
			if tc.ctxOverride != nil {
				ctx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				ctx.SetValue(schemas.BifrostContextKeyAllowPerRequestStorageOverride, true)
				ctx.SetValue(schemas.BifrostContextKeyDisableContentLogging, *tc.ctxOverride)
			}

			got := p.contentLoggingEnabled(ctx)
			if got != tc.want {
				t.Errorf("contentLoggingEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestContentLoggingEnabledHelperNilCtx verifies nil context falls back to global config.
func TestContentLoggingEnabledHelperNilCtx(t *testing.T) {
	disabled := true
	p := &LoggerPlugin{disableContentLogging: &disabled}
	if p.contentLoggingEnabled(nil) {
		t.Error("expected false with nil ctx and global disable=true")
	}
}

// TestUpdateLogEntryPerRequestOverrideEnablesContent verifies that passing contentLoggingEnabled=true
// to updateLogEntry stores output even when the plugin's global toggle is disabled.
func TestUpdateLogEntryPerRequestOverrideEnablesContent(t *testing.T) {
	store := newTestStore(t)
	disabled := true
	plugin := &LoggerPlugin{
		store:                 store,
		logger:                testLogger{},
		disableContentLogging: &disabled, // global: off
	}

	requestID := "req-per-request-enable"
	now := time.Now().UTC()
	if err := plugin.insertInitialLogEntry(context.Background(), requestID, "", now, 0, nil, &InitialLogData{
		Object:   "chat_completion",
		Provider: "openai",
		Model:    "gpt-4o-mini",
	}); err != nil {
		t.Fatalf("insertInitialLogEntry() error = %v", err)
	}

	chatText := "should be stored via per-request override"
	update := &UpdateLogData{
		Status: "success",
		ChatOutput: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: &chatText},
		},
	}

	// Explicitly pass true — simulates the per-request ctx override enabling content logging
	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update, true); err != nil {
		t.Fatalf("updateLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.OutputMessage == "" {
		t.Error("expected output_message to be stored when contentLoggingEnabled=true override is used")
	}
}

// TestUpdateLogEntryPerRequestOverrideDisablesContent verifies that passing contentLoggingEnabled=false
// suppresses output even when the plugin's global toggle is enabled.
func TestUpdateLogEntryPerRequestOverrideDisablesContent(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:  store,
		logger: testLogger{},
		// global: nil → content logging on by default
	}

	requestID := "req-per-request-disable"
	now := time.Now().UTC()
	if err := plugin.insertInitialLogEntry(context.Background(), requestID, "", now, 0, nil, &InitialLogData{
		Object:   "chat_completion",
		Provider: "openai",
		Model:    "gpt-4o-mini",
	}); err != nil {
		t.Fatalf("insertInitialLogEntry() error = %v", err)
	}

	chatText := "should NOT be stored"
	update := &UpdateLogData{
		Status: "success",
		ChatOutput: &schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: &chatText},
		},
	}

	// Explicitly pass false — simulates x-bf-disable-content-logging: true on this request
	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update, false); err != nil {
		t.Fatalf("updateLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.OutputMessage != "" {
		t.Errorf("expected output_message to be suppressed, got %q", logEntry.OutputMessage)
	}
}

// TestApplyNonStreamingOutputToEntryContentLoggingDisabled verifies that output fields are
// suppressed when contentLoggingEnabled=false.
func TestApplyNonStreamingOutputToEntryContentLoggingDisabled(t *testing.T) {
	plugin := &LoggerPlugin{}
	entry := &logstore.Log{}

	chatText := "should not appear"
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role:    schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{ContentStr: &chatText},
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
			},
		},
	}

	plugin.applyNonStreamingOutputToEntry(entry, result, false, false)

	if entry.OutputMessageParsed != nil {
		t.Error("expected OutputMessageParsed to be nil when contentLoggingEnabled=false")
	}
}

// TestApplyNonStreamingOutputToEntryContentLoggingEnabled verifies that output fields are
// stored when contentLoggingEnabled=true regardless of the global plugin config.
func TestApplyNonStreamingOutputToEntryContentLoggingEnabled(t *testing.T) {
	disabled := true
	plugin := &LoggerPlugin{disableContentLogging: &disabled} // global off, but explicit true passed
	entry := &logstore.Log{}

	chatText := "should appear"
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role:    schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{ContentStr: &chatText},
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
			},
		},
	}

	plugin.applyNonStreamingOutputToEntry(entry, result, false, true)

	if entry.OutputMessageParsed == nil {
		t.Error("expected OutputMessageParsed to be set when contentLoggingEnabled=true")
	}
}
