package handlers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
)

func TestShouldLogRealtimeTurn(t *testing.T) {
	t.Run("ei", func(t *testing.T) {
		if !shouldLogRealtimeTurn(realtimeTurnSourceEI, &schemas.BifrostRealtimeEvent{
			Type: schemas.RTEventConversationItemCreate,
			Item: &schemas.RealtimeItem{Role: "user", Type: "message"},
		}) {
			t.Fatal("expected conversation.item.create to be logged as EI turn")
		}
		if shouldLogRealtimeTurn(realtimeTurnSourceEI, &schemas.BifrostRealtimeEvent{Type: schemas.RTEventResponseTextDelta}) {
			t.Fatal("did not expect response.text.delta to be logged as EI turn")
		}
	})

	t.Run("lm", func(t *testing.T) {
		if !shouldLogRealtimeTurn(realtimeTurnSourceLM, &schemas.BifrostRealtimeEvent{Type: schemas.RTEventResponseDone}) {
			t.Fatal("expected response.done to be logged as LM turn")
		}
		if shouldLogRealtimeTurn(realtimeTurnSourceLM, &schemas.BifrostRealtimeEvent{Type: schemas.RTEventResponseTextDelta}) {
			t.Fatal("did not expect response.text.delta to be logged as LM turn")
		}
	})
}

func TestShouldAccumulateRealtimeOutput(t *testing.T) {
	provider := &openai.OpenAIProvider{}
	if !provider.ShouldAccumulateRealtimeOutput(schemas.RTEventResponseTextDelta) {
		t.Fatal("expected response.text.delta to accumulate output text")
	}
	if !provider.ShouldAccumulateRealtimeOutput(schemas.RTEventResponseAudioTransDelta) {
		t.Fatal("expected response.audio_transcript.delta to accumulate output transcript")
	}
	if provider.ShouldAccumulateRealtimeOutput(schemas.RTEventInputAudioTransDelta) {
		t.Fatal("did not expect input audio transcription delta to accumulate assistant output")
	}
}

func TestExtractRealtimeTurnSummary(t *testing.T) {
	event := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreate,
		Item: &schemas.RealtimeItem{
			Content: []byte(`[{"type":"input_text","text":"hello from realtime"}]`),
		},
	}

	got := extractRealtimeTurnSummary(event, "")
	if got != "hello from realtime" {
		t.Fatalf("extractRealtimeTurnSummary() = %q, want %q", got, "hello from realtime")
	}
}

func TestRealtimeTurnLoggerLogTurnPersistsParentRequestID(t *testing.T) {
	store := newTestRealtimeLogStore(t)
	defer store.Close(context.Background())

	notified := make(chan *logstore.Log, 1)
	turnLogger := newRealtimeTurnLogger(&lib.Config{
		ClientConfig: configstore.ClientConfig{EnableLogging: schemas.Ptr(true)},
		LogsStore:    store,
	}, func(entry *logstore.Log) {
		notified <- entry
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk_test")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyName, "Test VK")

	session := bfws.NewSession(nil)
	session.SetProviderSessionID("provider-session-1")

	event := &schemas.BifrostRealtimeEvent{
		Type:    schemas.RTEventResponseDone,
		EventID: "evt_123",
	}

	key := schemas.Key{ID: "key_123", Name: "Realtime Key"}
	rawResponse := []byte(`{
		"type":"response.done",
		"response":{
			"output":[
				{
					"id":"item_message_123",
					"type":"message",
					"content":[
						{
							"type":"audio",
							"transcript":"assistant turn text"
						}
					]
				},
				{
					"id":"item_function_123",
					"type":"function_call",
					"name":"getNextResponseFromSupervisor",
					"call_id":"call_123",
					"arguments":"{\"relevantContextFromLastUserMessage\":\"\"}"
				}
			],
			"usage":{
				"total_tokens":26,
				"input_tokens":17,
				"output_tokens":9,
				"input_token_details":{
					"text_tokens":12,
					"audio_tokens":5,
					"image_tokens":0,
					"cached_tokens":4
				},
				"output_token_details":{
					"text_tokens":7,
					"audio_tokens":2
				}
			}
		}
	}`)
	turnLogger.logTurn(ctx, session, realtimeTurnSourceLM, &openai.OpenAIProvider{}, schemas.OpenAI, "gpt-4o-realtime-preview", key, event, rawResponse, "hello from user", `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello from user"}`, []bfws.RealtimeToolOutput{{Summary: `{"result":"tool output"}`, Raw: `{"type":"conversation.item.create","item":{"type":"function_call_output"}}`}}, "assistant turn text")

	entry, err := store.FindFirst(context.Background(), map[string]interface{}{"id": session.ID()})
	if err == nil || entry != nil {
		t.Fatal("expected no log row with session id as primary id")
	}

	result, err := store.SearchLogs(context.Background(), logstore.SearchFilters{
		Objects: []string{realtimeTurnObject},
	}, logstore.PaginationOptions{Limit: 10, Offset: 0, SortBy: "timestamp", Order: "desc"})
	if err != nil {
		t.Fatalf("SearchLogs() error = %v", err)
	}
	if len(result.Logs) != 1 {
		t.Fatalf("len(result.Logs) = %d, want 1", len(result.Logs))
	}

	logEntry := result.Logs[0]
	fullLogEntry, err := store.FindFirst(context.Background(), map[string]interface{}{"id": logEntry.ID})
	if err != nil {
		t.Fatalf("FindFirst(full log) error = %v", err)
	}
	if logEntry.ParentRequestID == nil || *logEntry.ParentRequestID != session.ID() {
		t.Fatalf("ParentRequestID = %v, want %q", logEntry.ParentRequestID, session.ID())
	}
	if len(fullLogEntry.InputHistoryParsed) != 2 {
		t.Fatalf("len(InputHistoryParsed) = %d, want 2", len(fullLogEntry.InputHistoryParsed))
	}
	if fullLogEntry.InputHistoryParsed[0].Role != schemas.ChatMessageRoleTool || fullLogEntry.InputHistoryParsed[0].Content == nil || fullLogEntry.InputHistoryParsed[0].Content.ContentStr == nil || *fullLogEntry.InputHistoryParsed[0].Content.ContentStr != `{"result":"tool output"}` {
		t.Fatalf("InputHistoryParsed[0] = %+v, want tool output", fullLogEntry.InputHistoryParsed[0])
	}
	if fullLogEntry.InputHistoryParsed[1].Content == nil || fullLogEntry.InputHistoryParsed[1].Content.ContentStr == nil || *fullLogEntry.InputHistoryParsed[1].Content.ContentStr != "hello from user" {
		t.Fatalf("InputHistoryParsed[1] = %+v, want user turn", fullLogEntry.InputHistoryParsed[1])
	}
	if fullLogEntry.OutputMessageParsed == nil || fullLogEntry.OutputMessageParsed.Content == nil || fullLogEntry.OutputMessageParsed.Content.ContentStr == nil || *fullLogEntry.OutputMessageParsed.Content.ContentStr != "assistant turn text" {
		t.Fatalf("OutputMessageParsed = %+v, want assistant turn", fullLogEntry.OutputMessageParsed)
	}
	if fullLogEntry.TokenUsageParsed == nil {
		t.Fatal("expected TokenUsageParsed to be persisted on LM turn log")
	}
	if fullLogEntry.TokenUsageParsed.PromptTokens != 17 || fullLogEntry.TokenUsageParsed.CompletionTokens != 9 || fullLogEntry.TokenUsageParsed.TotalTokens != 26 {
		t.Fatalf("TokenUsageParsed = %+v, want prompt=17 completion=9 total=26", fullLogEntry.TokenUsageParsed)
	}
	if fullLogEntry.OutputMessageParsed == nil || fullLogEntry.OutputMessageParsed.ChatAssistantMessage == nil || len(fullLogEntry.OutputMessageParsed.ChatAssistantMessage.ToolCalls) != 1 {
		t.Fatalf("OutputMessageParsed.ToolCalls = %+v, want 1 tool call", fullLogEntry.OutputMessageParsed)
	}
	toolCall := fullLogEntry.OutputMessageParsed.ChatAssistantMessage.ToolCalls[0]
	if toolCall.Function.Name == nil || *toolCall.Function.Name != "getNextResponseFromSupervisor" {
		t.Fatalf("toolCall.Function.Name = %+v, want getNextResponseFromSupervisor", toolCall.Function.Name)
	}
	if toolCall.ID == nil || *toolCall.ID != "call_123" {
		t.Fatalf("toolCall.ID = %+v, want call_123", toolCall.ID)
	}
	if toolCall.Function.Arguments != `{"relevantContextFromLastUserMessage":""}` {
		t.Fatalf("toolCall.Function.Arguments = %q, want arguments payload", toolCall.Function.Arguments)
	}
	if fullLogEntry.RawRequest == "" {
		t.Fatal("expected RawRequest to be preserved on LM turn log")
	}
	if fullLogEntry.RawResponse == "" {
		t.Fatal("expected RawResponse to be preserved on LM turn log")
	}
	if fullLogEntry.MetadataParsed["realtime_source"] != string(realtimeTurnSourceLM) {
		t.Fatalf("realtime_source = %v, want %q", fullLogEntry.MetadataParsed["realtime_source"], realtimeTurnSourceLM)
	}
	if fullLogEntry.MetadataParsed["provider_session_id"] != "provider-session-1" {
		t.Fatalf("provider_session_id = %v, want %q", fullLogEntry.MetadataParsed["provider_session_id"], "provider-session-1")
	}

	select {
	case notifiedEntry := <-notified:
		if notifiedEntry.ParentRequestID == nil || *notifiedEntry.ParentRequestID != session.ID() {
			t.Fatalf("notified ParentRequestID = %v, want %q", notifiedEntry.ParentRequestID, session.ID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected realtime turn log notification")
	}
}

func TestRealtimeTurnLoggerSkipsEmptySummaryEIEvents(t *testing.T) {
	store := newTestRealtimeLogStore(t)
	defer store.Close(context.Background())

	turnLogger := newRealtimeTurnLogger(&lib.Config{
		ClientConfig: configstore.ClientConfig{EnableLogging: schemas.Ptr(true)},
		LogsStore:    store,
	}, nil)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	session := bfws.NewSession(nil)

	turnLogger.logTurn(
		ctx,
		session,
		realtimeTurnSourceEI,
		&openai.OpenAIProvider{},
		schemas.OpenAI,
		"gpt-4o-realtime-preview",
		schemas.Key{},
		&schemas.BifrostRealtimeEvent{Type: schemas.RTEventResponseCreate},
		[]byte(`{"type":"response.create"}`),
		"",
		"",
		nil,
		"",
	)

	result, err := store.SearchLogs(context.Background(), logstore.SearchFilters{
		Objects: []string{realtimeTurnObject},
	}, logstore.PaginationOptions{Limit: 10, Offset: 0, SortBy: "timestamp", Order: "desc"})
	if err != nil {
		t.Fatalf("SearchLogs() error = %v", err)
	}
	if len(result.Logs) != 0 {
		t.Fatalf("len(result.Logs) = %d, want 0", len(result.Logs))
	}
}

func TestFinalizedRealtimeInputSummary(t *testing.T) {
	userCreate := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreate,
		Item: &schemas.RealtimeItem{
			Role:    "user",
			Content: []byte(`[{"type":"input_text","text":"hello from browser"}]`),
		},
	}
	if got := finalizedRealtimeInputSummary(userCreate); got != "hello from browser" {
		t.Fatalf("finalizedRealtimeInputSummary(user create) = %q, want %q", got, "hello from browser")
	}

	inputTranscript := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventInputAudioTransCompleted,
		ExtraParams: map[string]json.RawMessage{
			"transcript": json.RawMessage(`"spoken user turn"`),
		},
	}
	if got := finalizedRealtimeInputSummary(inputTranscript); got != "spoken user turn" {
		t.Fatalf("finalizedRealtimeInputSummary(input transcript) = %q, want %q", got, "spoken user turn")
	}

	emptyInputTranscript := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventInputAudioTransCompleted,
		ExtraParams: map[string]json.RawMessage{
			"transcript": json.RawMessage(`""`),
		},
		RawData: []byte(`{"type":"conversation.item.input_audio_transcription.completed","transcript":"","usage":{"total_tokens":11}}`),
	}
	if got := finalizedRealtimeInputSummary(emptyInputTranscript); got != realtimeMissingTranscriptText {
		t.Fatalf("finalizedRealtimeInputSummary(empty input transcript) = %q, want %q", got, realtimeMissingTranscriptText)
	}

	missingInputTranscript := &schemas.BifrostRealtimeEvent{
		Type:    schemas.RTEventInputAudioTransCompleted,
		RawData: []byte(`{"type":"conversation.item.input_audio_transcription.completed","usage":{"total_tokens":11}}`),
	}
	if got := finalizedRealtimeInputSummary(missingInputTranscript); got != realtimeMissingTranscriptText {
		t.Fatalf("finalizedRealtimeInputSummary(missing input transcript) = %q, want %q", got, realtimeMissingTranscriptText)
	}

	assistantCreate := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreate,
		Item: &schemas.RealtimeItem{
			Role:    "assistant",
			Content: []byte(`[{"type":"text","text":"assistant text"}]`),
		},
	}
	if got := finalizedRealtimeInputSummary(assistantCreate); got != "" {
		t.Fatalf("finalizedRealtimeInputSummary(assistant create) = %q, want empty", got)
	}
}

func TestShouldLogRealtimeTurnSkipsFunctionCallOutputEITurn(t *testing.T) {
	event := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreate,
		Item: &schemas.RealtimeItem{
			Type: "function_call_output",
			Role: "user",
		},
	}
	if shouldLogRealtimeTurn(realtimeTurnSourceEI, event) {
		t.Fatal("expected function_call_output conversation.item.create to be skipped as EI turn")
	}
}

func TestFinalizedRealtimeToolOutputSummary(t *testing.T) {
	event := &schemas.BifrostRealtimeEvent{
		Type: schemas.RTEventConversationItemCreate,
		Item: &schemas.RealtimeItem{
			Type:   "function_call_output",
			Output: `{"nextResponse":"tool result"}`,
		},
	}
	if got := finalizedRealtimeToolOutputSummary(event); got != `{"nextResponse":"tool result"}` {
		t.Fatalf("finalizedRealtimeToolOutputSummary() = %q, want %q", got, `{"nextResponse":"tool result"}`)
	}
}

func TestBuildRealtimeTurnPostResponseUsesFullResponseDonePayload(t *testing.T) {
	rawRequest := `{"type":"conversation.item.input_audio_transcription.completed","transcript":""}`
	rawResponse := []byte(`{
		"type":"response.done",
		"response":{
			"output":[
				{
					"id":"item_message_123",
					"type":"message",
					"content":[
						{
							"type":"audio",
							"transcript":"assistant turn text"
						}
					]
				}
			],
			"usage":{
				"total_tokens":26,
				"input_tokens":17,
				"output_tokens":9,
				"input_token_details":{
					"text_tokens":12,
					"audio_tokens":5,
					"image_tokens":0,
					"cached_tokens":4
				},
				"output_token_details":{
					"text_tokens":7,
					"audio_tokens":2
				}
			}
		}
	}`)

	resp := buildRealtimeTurnPostResponse(&openai.OpenAIProvider{}, schemas.OpenAI, "gpt-4o-realtime-preview-2025-06-03", rawRequest, rawResponse, "")
	if resp == nil || resp.ResponsesResponse == nil {
		t.Fatal("expected realtime post response to be built")
	}
	if resp.ResponsesResponse.Usage == nil || resp.ResponsesResponse.Usage.InputTokens != 17 || resp.ResponsesResponse.Usage.OutputTokens != 9 || resp.ResponsesResponse.Usage.TotalTokens != 26 {
		t.Fatalf("Usage = %+v, want input=17 output=9 total=26", resp.ResponsesResponse.Usage)
	}
	if len(resp.ResponsesResponse.Output) != 1 {
		t.Fatalf("len(Output) = %d, want 1", len(resp.ResponsesResponse.Output))
	}
	if resp.ResponsesResponse.Output[0].Content == nil || resp.ResponsesResponse.Output[0].Content.ContentStr == nil || *resp.ResponsesResponse.Output[0].Content.ContentStr != "assistant turn text" {
		t.Fatalf("Output[0].Content = %+v, want assistant turn text", resp.ResponsesResponse.Output[0].Content)
	}
	if got, ok := resp.ResponsesResponse.ExtraFields.RawRequest.(string); !ok || got != rawRequest {
		t.Fatalf("RawRequest = %#v, want %q", resp.ResponsesResponse.ExtraFields.RawRequest, rawRequest)
	}
	if got, ok := resp.ResponsesResponse.ExtraFields.RawResponse.(string); !ok || got == "" {
		t.Fatalf("RawResponse = %#v, want raw response string", resp.ResponsesResponse.ExtraFields.RawResponse)
	}
}

func newTestRealtimeLogStore(t *testing.T) logstore.LogStore {
	t.Helper()

	store, err := logstore.NewLogStore(context.Background(), &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{
			Path: filepath.Join(t.TempDir(), "realtime-logs.db"),
		},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewLogStore() error = %v", err)
	}
	return store
}
