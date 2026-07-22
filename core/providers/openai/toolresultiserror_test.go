package openai

import (
	"strings"
	"testing"

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
