package logging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/streaming"
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

	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update); err != nil {
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

	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update); err != nil {
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
	disableContentLogging := true
	plugin := &LoggerPlugin{
		store:                 store,
		logger:                testLogger{},
		disableContentLogging: &disableContentLogging,
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

	if err := plugin.updateLogEntry(context.Background(), requestID, "", "", 10, "", "", "", "", 0, nil, "", update); err != nil {
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

func TestUpdateStreamingLogEntryPreservesResponsesInputContentSummary(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		store:  store,
		logger: testLogger{},
	}

	requestID := "req-stream"
	now := time.Now().UTC()
	inputText := "stream request-side text"
	initial := &InitialLogData{
		Object:   "responses_stream",
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

	responsesText := "streamed response text"
	streamResponse := &streaming.ProcessedStreamResponse{
		Data: &streaming.AccumulatedData{
			Latency: 25,
			TokenUsage: &schemas.BifrostLLMUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
			OutputMessages: []schemas.ResponsesMessage{{
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &responsesText,
				},
			}},
		},
	}

	if err := plugin.updateStreamingLogEntry(context.Background(), requestID, "", "", "", "", "", "", 0, nil, "", streamResponse, true, false, false); err != nil {
		t.Fatalf("updateStreamingLogEntry() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), requestID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.TokenUsageParsed == nil || logEntry.TokenUsageParsed.TotalTokens != 15 {
		t.Fatalf("expected token usage to be updated, got %+v", logEntry.TokenUsageParsed)
	}
	if !strings.Contains(logEntry.ContentSummary, inputText) {
		t.Fatalf("expected content summary to preserve responses input, got %q", logEntry.ContentSummary)
	}
	if strings.Contains(logEntry.ContentSummary, responsesText) {
		t.Fatalf("expected content summary to avoid overwriting with streamed responses output-only data, got %q", logEntry.ContentSummary)
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

	plugin.applyRealtimeOutputToEntry(entry, result)
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

	plugin.applyRealtimeOutputToEntry(entry, result)
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

	plugin.applyRealtimeOutputToEntry(entry, result)
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

	plugin.applyRealtimeOutputToEntry(entry, result)
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

	plugin.applyRealtimeOutputToEntry(entry, result)

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

	plugin.applyRealtimeOutputToEntry(entry, result)
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
