package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestGroupedConversion_ClientToolNamedLikeServerTool verifies that a client
// tool_use whose name collides with an Anthropic server tool (web_search,
// web_fetch) stays a function_call. Converting it to a web_search_call /
// web_fetch_call orphans the matching tool_result, and Bedrock rejects the
// replay with "The number of toolResult blocks ... exceeds the number of
// toolUse blocks of previous turn".
func TestGroupedConversion_ClientToolNamedLikeServerTool(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: string(AnthropicToolNameWebSearch), input: `{"query":"bifrost"}`},
		{name: string(AnthropicToolNameWebFetch), input: `{"url":"https://example.com"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assistant := schemas.ResponsesMessageRoleType(AnthropicMessageRoleAssistant)
			msgs := convertAnthropicContentBlocksToResponsesMessagesGrouped([]AnthropicContentBlock{
				{
					Type:  AnthropicContentBlockTypeToolUse,
					ID:    schemas.Ptr("toolu_client"),
					Name:  schemas.Ptr(tc.name),
					Input: json.RawMessage(tc.input),
				},
			}, &assistant, true)

			if len(msgs) != 1 {
				t.Fatalf("expected 1 converted message, got %d", len(msgs))
			}
			msg := msgs[0]
			if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeFunctionCall {
				t.Fatalf("expected function_call, got %s", messageType(msg))
			}
			if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.Name == nil || *msg.ResponsesToolMessage.Name != tc.name {
				t.Fatalf("expected function name %q to be preserved", tc.name)
			}
			if msg.ResponsesToolMessage.Arguments == nil || *msg.ResponsesToolMessage.Arguments != tc.input {
				t.Fatalf("expected arguments %q to be preserved, got %v", tc.input, msg.ResponsesToolMessage.Arguments)
			}
		})
	}
}

// TestGroupedConversion_ServerWebSearchStillMapped guards the server tool path:
// a server_tool_use web_search must still become a web_search_call.
func TestGroupedConversion_ServerWebSearchStillMapped(t *testing.T) {
	assistant := schemas.ResponsesMessageRoleType(AnthropicMessageRoleAssistant)
	msgs := convertAnthropicContentBlocksToResponsesMessagesGrouped([]AnthropicContentBlock{
		{
			Type:  AnthropicContentBlockTypeServerToolUse,
			ID:    schemas.Ptr("srvtoolu_server"),
			Name:  schemas.Ptr(string(AnthropicToolNameWebSearch)),
			Input: json.RawMessage(`{"query":"bifrost"}`),
		},
	}, &assistant, true)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 converted message, got %d", len(msgs))
	}
	if msgs[0].Type == nil || *msgs[0].Type != schemas.ResponsesMessageTypeWebSearchCall {
		t.Fatalf("expected web_search_call, got %s", messageType(msgs[0]))
	}
}

func messageType(msg schemas.ResponsesMessage) string {
	if msg.Type == nil {
		return "<nil>"
	}
	return string(*msg.Type)
}
