package deepseek_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func newOpenAIChatResponse() string {
	return `{"id":"chatcmpl_1","object":"chat.completion","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
}

func newOpenAIChatStreamResponse() string {
	return `data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n" +
		"data: [DONE]\n\n"
}

// captureDeepSeekChatBody drives request through DeepSeek's native
// OpenAI-compatible chat path and returns the decoded outbound wire body.
func captureDeepSeekChatBody(t *testing.T, request *schemas.BifrostChatRequest) map[string]any {
	t.Helper()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, newOpenAIChatResponse())
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}
	if _, bifrostErr := provider.ChatCompletion(ctx, key, request); bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	return captured
}

// captureDeepSeekChatStreamBody is the streaming twin of captureDeepSeekChatBody: it
// drains ChatCompletionStream and returns the decoded outbound wire body.
func captureDeepSeekChatStreamBody(t *testing.T, request *schemas.BifrostChatRequest) map[string]any {
	t.Helper()

	var mu sync.Mutex
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		mu.Lock()
		captured = decoded
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, newOpenAIChatStreamResponse())
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}
	postHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	stream, bifrostErr := provider.ChatCompletionStream(ctx, postHook, nil, key, request)
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream: %v", bifrostErr.Error.Message)
	}
	for range stream {
	}

	mu.Lock()
	defer mu.Unlock()
	return captured
}

// toolCallHistoryWithoutReasoning builds a history whose assistant tool-call turn replays
// no reasoning. With midLoop the tool result is the last message; otherwise a new user
// turn follows it.
func toolCallHistoryWithoutReasoning(midLoop bool) []schemas.ChatMessage {
	ask := "Read a.md and tell me the number inside."
	toolResult := "# a.md\nthe answer is 42"
	toolCallID := "call_1"
	toolName := "read"

	messages := []schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &ask}},
		{
			Role: schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					ID:       &toolCallID,
					Function: schemas.ChatAssistantMessageToolCallFunction{Name: &toolName, Arguments: `{"path":"a.md"}`},
				}},
			},
		},
		{
			Role:            schemas.ChatMessageRoleTool,
			Content:         &schemas.ChatMessageContent{ContentStr: &toolResult},
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: &toolCallID},
		},
	}
	if !midLoop {
		followUp := "State the number."
		messages = append(messages, schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &followUp}})
	}
	return messages
}

// assertEmptyReasoningReplayed fails unless the message at index i of the wire body
// carries reasoning_content as an empty string.
func assertEmptyReasoningReplayed(t *testing.T, captured map[string]any, i int) {
	t.Helper()
	got, ok := assistantMessageAt(t, captured, i)["reasoning_content"]
	if !ok {
		t.Fatalf("reasoning_content must be present on the assistant tool-call turn, got %#v", captured["messages"])
	}
	if got != "" {
		t.Fatalf("reasoning_content = %#v, want an empty string", got)
	}
}

// assistantMessageAt returns the decoded message at index i of the wire body.
func assistantMessageAt(t *testing.T, captured map[string]any, i int) map[string]any {
	t.Helper()
	messages, ok := captured["messages"].([]any)
	if !ok || len(messages) <= i {
		t.Fatalf("expected at least %d messages in wire payload, got %#v", i+1, captured["messages"])
	}
	msg, ok := messages[i].(map[string]any)
	if !ok {
		t.Fatalf("expected message object at index %d, got %#v", i, messages[i])
	}
	return msg
}

// TestChatCompletion_OpenAIEndpointKeepsThinkingForPlainMultiTurn reproduces #5887.
// DeepSeek enables thinking by default and does not require reasoning_content to be
// replayed when the history contains no tool calls, so Bifrost must not send
// thinking:{"type":"disabled"} for an ordinary multi-turn conversation.
func TestChatCompletion_OpenAIEndpointKeepsThinkingForPlainMultiTurn(t *testing.T) {
	t.Parallel()

	first := "What's the weather in Beijing?"
	answer := "Sunny, 32°C."
	followUp := "Which is bigger, 9.9 or 9.11? Think carefully."

	captured := captureDeepSeekChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &first}},
			{
				Role:                 schemas.ChatMessageRoleAssistant,
				Content:              &schemas.ChatMessageContent{ContentStr: &answer},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{},
			},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &followUp}},
		},
		// Params must be non-nil: the gate short-circuits on nil Params, so a
		// nil-Params request would pass this test without exercising anything.
		Params: &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(3000)},
	})

	if thinking, ok := captured["thinking"]; ok {
		t.Fatalf("thinking must be absent for plain multi-turn (DeepSeek defaults thinking on), got %#v", thinking)
	}
}

// TestChatCompletion_OpenAIEndpointKeepsThinkingWhenToolCallReasoningReplayed covers the
// tool-calling variation of #5887: the caller correctly replays reasoning_content on the
// assistant tool-call turn, so thinking must stay on AND the replayed reasoning_content
// must survive to the wire (DeepSeek 400s on a tool-call turn missing it).
func TestChatCompletion_OpenAIEndpointKeepsThinkingWhenToolCallReasoningReplayed(t *testing.T) {
	t.Parallel()

	chatTool := llmtests.GetSampleChatTool(llmtests.SampleToolTypeTime)
	if chatTool == nil {
		t.Fatal("GetSampleChatTool returned nil")
	}

	ask := "what time is it in UTC?"
	reasoning := "the user wants the current UTC time, call the tool"
	toolResult := "2026-08-05T12:00:00Z"
	answer := "It is 12:00 UTC."
	followUp := "Which is bigger, 9.9 or 9.11? Think carefully."
	toolCallID := "call_1"
	toolName := "get_current_time"

	captured := captureDeepSeekChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &ask}},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					Reasoning: &reasoning,
					ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID:       &toolCallID,
						Function: schemas.ChatAssistantMessageToolCallFunction{Name: &toolName, Arguments: `{}`},
					}},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				Content:         &schemas.ChatMessageContent{ContentStr: &toolResult},
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: &toolCallID},
			},
			{
				Role:                 schemas.ChatMessageRoleAssistant,
				Content:              &schemas.ChatMessageContent{ContentStr: &answer},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{},
			},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &followUp}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(3000),
			Tools:               []schemas.ChatTool{*chatTool},
		},
	})

	if thinking, ok := captured["thinking"]; ok {
		t.Fatalf("thinking must stay on when tool-call reasoning_content is replayed, got %#v", thinking)
	}
	toolCallMsg := assistantMessageAt(t, captured, 1)
	if got, ok := toolCallMsg["reasoning_content"].(string); !ok || got != reasoning {
		t.Fatalf("reasoning_content must be preserved on the assistant tool-call turn, got %#v", toolCallMsg["reasoning_content"])
	}
}

// TestChatCompletion_OpenAIEndpointKeepsThinkingForReasoningDetailsOnly covers callers that
// replay reasoning as reasoning_details rather than reasoning_content.
func TestChatCompletion_OpenAIEndpointKeepsThinkingForReasoningDetailsOnly(t *testing.T) {
	t.Parallel()

	answer := "Sunny, 32°C."
	followUp := "Which is bigger, 9.9 or 9.11? Think carefully."
	reasoning := "recalling Beijing weather"

	captured := captureDeepSeekChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: &answer},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ReasoningDetails: []schemas.ChatReasoningDetails{{
						Index: 0,
						Type:  schemas.BifrostReasoningDetailsTypeText,
						Text:  &reasoning,
					}},
				},
			},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &followUp}},
		},
		Params: &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(3000)},
	})

	if thinking, ok := captured["thinking"]; ok {
		t.Fatalf("thinking must stay on when reasoning is replayed as reasoning_details, got %#v", thinking)
	}
}

// TestChatCompletion_OpenAIEndpointKeepsThinkingForToolCallWithoutReasoning covers a request
// in the middle of a tool loop whose assistant tool-call turn has no reasoning to replay.
// Thinking must stay on (#7213). The turn goes out with an empty reasoning_content so
// DeepSeek's replay rule for tool-call turns is still met.
func TestChatCompletion_OpenAIEndpointKeepsThinkingForToolCallWithoutReasoning(t *testing.T) {
	t.Parallel()

	chatTool := llmtests.GetSampleChatTool(llmtests.SampleToolTypeTime)
	if chatTool == nil {
		t.Fatal("GetSampleChatTool returned nil")
	}

	captured := captureDeepSeekChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input:    toolCallHistoryWithoutReasoning(true),
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(3000),
			Tools:               []schemas.ChatTool{*chatTool},
		},
	})

	if thinking, ok := captured["thinking"]; ok {
		t.Fatalf("thinking must stay on for a tool-call turn without reasoning, got %#v", thinking)
	}
	assertEmptyReasoningReplayed(t, captured, 1)
}

// TestChatCompletion_OpenAIEndpointKeepsRequestedThinkingForToolCallWithoutReasoning
// reproduces #7213. The caller asks for thinking explicitly and replays an assistant
// tool-call turn that carries no reasoning_content. The outbound body must keep the
// requested thinking mode instead of rewriting it to disabled.
func TestChatCompletion_OpenAIEndpointKeepsRequestedThinkingForToolCallWithoutReasoning(t *testing.T) {
	t.Parallel()

	chatTool := llmtests.GetSampleChatTool(llmtests.SampleToolTypeTime)
	if chatTool == nil {
		t.Fatal("GetSampleChatTool returned nil")
	}

	ask := "Read a.md and tell me the number inside."
	toolResult := "# a.md\nthe answer is 42"
	followUp := "State the number."
	toolCallID := "call_1"
	toolName := "read"

	captured := captureDeepSeekChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &ask}},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID:       &toolCallID,
						Function: schemas.ChatAssistantMessageToolCallFunction{Name: &toolName, Arguments: `{"path":"a.md"}`},
					}},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				Content:         &schemas.ChatMessageContent{ContentStr: &toolResult},
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: &toolCallID},
			},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &followUp}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(800),
			Reasoning:           &schemas.ChatReasoning{Effort: schemas.Ptr("high")},
			Tools:               []schemas.ChatTool{*chatTool},
			ExtraParams:         map[string]any{"thinking": map[string]any{"type": "enabled"}},
		},
	})

	thinking, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected the caller's thinking block in outbound body, got %#v", captured["thinking"])
	}
	if got := thinking["type"]; got != "enabled" {
		t.Fatalf("thinking.type = %v, want enabled (caller asked for thinking, reasoning_effort = %v)", got, captured["reasoning_effort"])
	}
}

// TestChatCompletion_OpenAIEndpointKeepsThinkingForToolCallWithReasoningDetailsOnly
// covers a tool-call turn whose reasoning is carried only as reasoning_details.
// ReasoningDetails is inbound-only -- ConvertBifrostMessagesToOpenAIMessages copies
// Reasoning into reasoning_content and never populates reasoning_details (see
// core/providers/openai/types.go) -- so the turn is handled like one with no reasoning:
// thinking stays on and reasoning_content goes out empty.
func TestChatCompletion_OpenAIEndpointKeepsThinkingForToolCallWithReasoningDetailsOnly(t *testing.T) {
	t.Parallel()

	chatTool := llmtests.GetSampleChatTool(llmtests.SampleToolTypeTime)
	if chatTool == nil {
		t.Fatal("GetSampleChatTool returned nil")
	}

	ask := "what time is it in UTC?"
	reasoning := "the user wants the current UTC time, call the tool"
	toolResult := "2026-08-05T12:00:00Z"
	toolCallID := "call_1"
	toolName := "get_current_time"

	captured := captureDeepSeekChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &ask}},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID:       &toolCallID,
						Function: schemas.ChatAssistantMessageToolCallFunction{Name: &toolName, Arguments: `{}`},
					}},
					ReasoningDetails: []schemas.ChatReasoningDetails{{
						Index: 0,
						Type:  schemas.BifrostReasoningDetailsTypeText,
						Text:  &reasoning,
					}},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				Content:         &schemas.ChatMessageContent{ContentStr: &toolResult},
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: &toolCallID},
			},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: new(3000),
			Tools:               []schemas.ChatTool{*chatTool},
		},
	})

	if thinking, ok := captured["thinking"]; ok {
		t.Fatalf("thinking must stay on when reasoning is carried only as reasoning_details, got %#v", thinking)
	}
	assertEmptyReasoningReplayed(t, captured, 1)
}

// TestChatCompletion_OpenAIEndpointKeepsThinkingForToolCallWithoutDeclaredTools covers a
// history that replays an assistant tool-call turn while the request itself declares no
// tools -- the shape a caller produces when it drops the tool list on a final summarizing
// turn. The backfill follows the messages, not the tool declarations.
func TestChatCompletion_OpenAIEndpointKeepsThinkingForToolCallWithoutDeclaredTools(t *testing.T) {
	t.Parallel()

	captured := captureDeepSeekChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input:    toolCallHistoryWithoutReasoning(false),
		// Deliberately no Tools: the backfill follows the messages.
		Params: &schemas.ChatParameters{MaxCompletionTokens: new(3000)},
	})

	if thinking, ok := captured["thinking"]; ok {
		t.Fatalf("thinking must stay on for a tool-call turn without reasoning, got %#v", thinking)
	}
	assertEmptyReasoningReplayed(t, captured, 1)
}

// TestChatCompletion_OpenAIEndpointBackfillsToolCallReasoningWithNilParams covers a request
// with no Params at all. Thinking is on by default upstream, so the tool-call turn still
// needs its reasoning_content.
func TestChatCompletion_OpenAIEndpointBackfillsToolCallReasoningWithNilParams(t *testing.T) {
	t.Parallel()

	captured := captureDeepSeekChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input:    toolCallHistoryWithoutReasoning(true),
	})

	if thinking, ok := captured["thinking"]; ok {
		t.Fatalf("thinking must stay on for a tool-call turn without reasoning, got %#v", thinking)
	}
	assertEmptyReasoningReplayed(t, captured, 1)
}

// TestChatCompletionStream_OpenAIEndpointKeepsThinkingForToolCallWithoutReasoning pins the
// streaming call site, which applies the thinking shim separately from ChatCompletion.
func TestChatCompletionStream_OpenAIEndpointKeepsThinkingForToolCallWithoutReasoning(t *testing.T) {
	t.Parallel()

	chatTool := llmtests.GetSampleChatTool(llmtests.SampleToolTypeTime)
	if chatTool == nil {
		t.Fatal("GetSampleChatTool returned nil")
	}

	captured := captureDeepSeekChatStreamBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input:    toolCallHistoryWithoutReasoning(true),
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(3000),
			Tools:               []schemas.ChatTool{*chatTool},
			ExtraParams:         map[string]any{"thinking": map[string]any{"type": "enabled"}},
		},
	})

	thinking, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected the caller's thinking block in outbound body, got %#v", captured["thinking"])
	}
	if got := thinking["type"]; got != "enabled" {
		t.Fatalf("thinking.type = %v, want enabled", got)
	}
	assertEmptyReasoningReplayed(t, captured, 1)
}

// TestChatCompletion_OpenAIEndpointSkipsReasoningBackfillWhenThinkingIsOff guards the two
// cases where thinking is off on the wire: the caller disabled it, or a forced tool_choice
// made the shim disable it. The replay rule only applies in thinking mode, so the
// tool-call turn must go out untouched.
// This test is expected to pass both before and after the fix.
func TestChatCompletion_OpenAIEndpointSkipsReasoningBackfillWhenThinkingIsOff(t *testing.T) {
	t.Parallel()

	chatTool := llmtests.GetSampleChatTool(llmtests.SampleToolTypeTime)
	if chatTool == nil {
		t.Fatal("GetSampleChatTool returned nil")
	}
	requiredChoice := string(schemas.ChatToolChoiceTypeRequired)

	tests := []struct {
		name   string
		params *schemas.ChatParameters
	}{
		{
			name: "caller disabled thinking",
			params: &schemas.ChatParameters{
				Tools:       []schemas.ChatTool{*chatTool},
				ExtraParams: map[string]any{"thinking": map[string]any{"type": "disabled"}},
			},
		},
		{
			name: "caller disabled thinking with a typed map",
			params: &schemas.ChatParameters{
				Tools:       []schemas.ChatTool{*chatTool},
				ExtraParams: map[string]any{"thinking": map[string]string{"type": "disabled"}},
			},
		},
		{
			name: "forced tool_choice",
			params: &schemas.ChatParameters{
				Tools:      []schemas.ChatTool{*chatTool},
				ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: &requiredChoice},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			captured := captureDeepSeekChatBody(t, &schemas.BifrostChatRequest{
				Provider: schemas.DeepSeek,
				Model:    "deepseek-v4-flash",
				Input:    toolCallHistoryWithoutReasoning(true),
				Params:   tt.params,
			})

			thinking, ok := captured["thinking"].(map[string]any)
			if !ok {
				t.Fatalf("expected thinking block in outbound body, got %#v", captured)
			}
			if got := thinking["type"]; got != "disabled" {
				t.Fatalf("thinking.type = %v, want disabled", got)
			}
			if got, ok := assistantMessageAt(t, captured, 1)["reasoning_content"]; ok {
				t.Fatalf("reasoning_content must not be added while thinking is off, got %#v", got)
			}
		})
	}
}

// TestChatCompletion_DoesNotMutateCallerExtraParams ensures the thinking compatibility
// shim does not write through to the caller's shared ChatParameters, which would leak
// thinking:disabled into fallback attempts against other providers.
func TestChatCompletion_DoesNotMutateCallerExtraParams(t *testing.T) {
	t.Parallel()

	chatTool := llmtests.GetSampleChatTool(llmtests.SampleToolTypeTime)
	if chatTool == nil {
		t.Fatal("GetSampleChatTool returned nil")
	}

	ask := "what time is it in UTC?"
	requiredChoice := string(schemas.ChatToolChoiceTypeRequired)
	// ExtraParams must be non-nil here. The shim allocates a fresh map before adding
	// thinking, so a nil caller map would take that allocation path no matter what --
	// and an implementation that only allocates *when* nil, aliasing the caller's map
	// otherwise, would pass this test while corrupting every real request. The sentinel
	// forces the aliasing path to be the one under test.
	params := &schemas.ChatParameters{
		MaxCompletionTokens: schemas.Ptr(3000),
		Tools:               []schemas.ChatTool{*chatTool},
		ToolChoice:          &schemas.ChatToolChoice{ChatToolChoiceStr: &requiredChoice},
		ExtraParams:         map[string]any{"sentinel": "preserve"},
	}

	captured := captureDeepSeekChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &ask}},
		},
		Params: params,
	})

	// The shim must have fired, or the checks below prove nothing.
	if thinking, ok := captured["thinking"].(map[string]any); !ok || thinking["type"] != "disabled" {
		t.Fatalf("expected thinking disabled in outbound body, got %#v", captured["thinking"])
	}
	if got := params.ExtraParams["sentinel"]; got != "preserve" {
		t.Fatalf("caller ExtraParams was replaced or corrupted, got %#v", params.ExtraParams)
	}
	if _, ok := params.ExtraParams["thinking"]; ok {
		t.Fatalf("caller ChatParameters must not be mutated, got ExtraParams=%#v", params.ExtraParams)
	}
}

// TestChatCompletion_DoesNotMutateCallerMessages ensures the reasoning backfill does not
// write through to the caller's messages, which are shared with fallback attempts against
// other providers.
func TestChatCompletion_DoesNotMutateCallerMessages(t *testing.T) {
	t.Parallel()

	input := toolCallHistoryWithoutReasoning(true)
	captured := captureDeepSeekChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input:    input,
		Params:   &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(3000)},
	})

	// The backfill must have fired, or the check below proves nothing.
	assertEmptyReasoningReplayed(t, captured, 1)
	if got := input[1].ChatAssistantMessage.Reasoning; got != nil {
		t.Fatalf("caller message must not be mutated, got Reasoning=%q", *got)
	}
}

// TestResponses_OpenAIEndpointKeepsThinkingForFunctionCallWithoutReasoning covers the
// Responses API entry point, which reaches DeepSeek through the chat path. A replayed
// function_call item with no reasoning item before it becomes an assistant tool-call turn
// without reasoning, so the same rule applies: thinking stays on and reasoning_content
// goes out empty.
func TestResponses_OpenAIEndpointKeepsThinkingForFunctionCallWithoutReasoning(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, newOpenAIChatResponse())
	}))
	defer server.Close()

	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	responsesTool := llmtests.GetSampleResponsesTool(llmtests.SampleToolTypeTime)
	if responsesTool == nil {
		t.Fatal("GetSampleResponsesTool returned nil")
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Responses(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostResponsesRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ResponsesMessage{
			{
				Type:    new(schemas.ResponsesMessageTypeMessage),
				Role:    new(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: new("what time is it in UTC?")},
			},
			{
				Type: new(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    new("call_1"),
					Name:      new(string(llmtests.SampleToolTypeTime)),
					Arguments: new(`{"timezone":"UTC"}`),
				},
			},
			{
				Type: new(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: new("call_1"),
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: new("2026-08-05T12:00:00Z")},
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{*responsesTool},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("Responses: %v", bifrostErr.Error.Message)
	}

	if thinking, ok := captured["thinking"]; ok {
		t.Fatalf("thinking must stay on for a function_call without reasoning, got %#v", thinking)
	}
	assertEmptyReasoningReplayed(t, captured, 1)
}
