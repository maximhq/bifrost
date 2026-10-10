package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestToolResultIsErrorStrippedFromOpenAIWire verifies that IsError — an
// Anthropic-family marker with no OpenAI wire equivalent — never serializes
// into an OpenAI message. OpenAI-compatible providers reject unknown message
// parameters (see the Responses-path incident with `input[N].error`), so the
// converter must strip it rather than forward it.
func TestToolResultIsErrorStrippedFromOpenAIWire(t *testing.T) {
	original := &schemas.ChatToolMessage{
		ToolCallID: schemas.Ptr("call_1"),
		IsError:    schemas.Ptr(true),
	}
	messages := []schemas.ChatMessage{
		{
			Role:            schemas.ChatMessageRoleTool,
			ChatToolMessage: original,
			Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("command exited with code 1")},
		},
	}

	converted := ConvertBifrostMessagesToOpenAIMessages(messages)
	if len(converted) != 1 {
		t.Fatalf("expected 1 converted message, got %d", len(converted))
	}
	if converted[0].ChatToolMessage == nil {
		t.Fatal("expected tool message fields to survive conversion")
	}
	if converted[0].ChatToolMessage.IsError != nil {
		t.Fatal("IsError must be stripped from the OpenAI-wire message")
	}
	if converted[0].ChatToolMessage.ToolCallID == nil || *converted[0].ChatToolMessage.ToolCallID != "call_1" {
		t.Fatal("tool_call_id must survive the strip")
	}

	wire, err := schemas.MarshalSorted(converted[0])
	if err != nil {
		t.Fatalf("marshal converted message: %v", err)
	}
	if strings.Contains(string(wire), "is_error") {
		t.Fatalf("serialized OpenAI message must not contain is_error, got: %s", wire)
	}

	// The caller's input is shared; the strip must clone, never mutate.
	if original.IsError == nil || !*original.IsError {
		t.Fatal("caller's ChatToolMessage must not be mutated by the strip")
	}
}

// TestToolResultIsErrorSurvivesAnthropicToOpenAIResponsesRoundTrip walks a
// tool_result flagged is_error through the whole /anthropic/v1/messages ->
// /v1/responses path. The Anthropic converter carries the flag as
// status:"incomplete" on the function_call_output item, which is the only
// marker the Responses surface has for a failed tool result -- the chat path
// strips is_error on the OpenAI wire by design. Blanket-stripping status in
// ToOpenAIResponsesRequest made a failed tool call indistinguishable from a
// successful one.
func TestToolResultIsErrorSurvivesAnthropicToOpenAIResponsesRoundTrip(t *testing.T) {
	const body = `{
		"max_tokens": 64,
		"model": "openai/gpt-4o",
		"tools": [{"name":"run_cmd","description":"run a shell command","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}}],
		"messages": [
			{"role":"user","content":"Run ls /root"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"run_cmd","input":{"cmd":"ls /root"}},{"type":"tool_use","id":"toolu_2","name":"run_cmd","input":{"cmd":"ls /tmp"}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"permission denied","is_error":true},
				{"type":"tool_result","tool_use_id":"toolu_2","content":"ok"}
			]}
		]
	}`

	var anthropicReq anthropic.AnthropicMessageRequest
	if err := json.Unmarshal([]byte(body), &anthropicReq); err != nil {
		t.Fatalf("unmarshal anthropic request: %v", err)
	}

	bifrostReq := anthropicReq.ToBifrostResponsesRequest(nil)
	if bifrostReq == nil {
		t.Fatal("ToBifrostResponsesRequest returned nil")
	}

	openaiReq := ToOpenAIResponsesRequest(nil, bifrostReq)
	if openaiReq == nil || len(openaiReq.Input.OpenAIResponsesRequestInputArray) == 0 {
		t.Fatal("ToOpenAIResponsesRequest returned no input")
	}

	statusFor := func(callID string) (*schemas.ResponsesMessage, bool) {
		for i, item := range openaiReq.Input.OpenAIResponsesRequestInputArray {
			if item.Type == nil || *item.Type != schemas.ResponsesMessageTypeFunctionCallOutput {
				continue
			}
			if item.ResponsesToolMessage != nil && item.ResponsesToolMessage.CallID != nil &&
				*item.ResponsesToolMessage.CallID == callID {
				return &openaiReq.Input.OpenAIResponsesRequestInputArray[i], true
			}
		}
		return nil, false
	}

	failed, ok := statusFor("toolu_1")
	if !ok {
		t.Fatal("no function_call_output item for the failed tool result")
	}
	if failed.Status == nil || *failed.Status != "incomplete" {
		t.Fatalf("expected the failed tool result to keep status \"incomplete\", got %#v", failed.Status)
	}

	succeeded, ok := statusFor("toolu_2")
	if !ok {
		t.Fatal("no function_call_output item for the successful tool result")
	}
	if succeeded.Status != nil {
		t.Fatalf("expected no status on the successful tool result, got %q", *succeeded.Status)
	}

	// The marker has to reach the wire, not just the struct, and nothing else
	// may ride along: `error` on an input item is rejected by OpenAI.
	wire, err := schemas.MarshalSorted(failed)
	if err != nil {
		t.Fatalf("marshal function_call_output: %v", err)
	}
	if !strings.Contains(string(wire), `"status":"incomplete"`) {
		t.Fatalf("serialized function_call_output must carry the incomplete status, got: %s", wire)
	}
	if strings.Contains(string(wire), `"error"`) {
		t.Fatalf("serialized function_call_output must not carry an error field, got: %s", wire)
	}
}
