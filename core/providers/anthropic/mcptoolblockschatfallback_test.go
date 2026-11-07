package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestMCPToolBlocksDroppedOnChatFallback pins #9721: an Anthropic Messages request whose
// history holds mcp_tool_use / mcp_tool_result blocks, routed to a chat-only
// (OpenAI-compatible) provider, must not produce a chat message without a role.
func TestMCPToolBlocksDroppedOnChatFallback(t *testing.T) {
	var req AnthropicMessageRequest
	require.NoError(t, schemas.Unmarshal([]byte(`{
		"model": "deepseek/deepseek-flash",
		"max_tokens": 30,
		"messages": [
			{"role": "user", "content": "look"},
			{"role": "assistant", "content": [
				{"type": "mcp_tool_use", "id": "call_1", "name": "analyze_image",
				 "input": {"prompt": "desc"}, "server_name": "4_5v_mcp"}
			]},
			{"role": "user", "content": [
				{"type": "mcp_tool_result", "tool_use_id": "call_1",
				 "content": [{"type": "text", "text": "a red pixel"}]}
			]},
			{"role": "user", "content": "reply OK"}
		]
	}`), &req))

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	chatReq := req.ToBifrostResponsesRequest(ctx).ToChatRequest()
	require.NotNil(t, chatReq)
	require.NotEmpty(t, chatReq.Input)
	for i, msg := range chatReq.Input {
		require.NotEmpty(t, msg.Role, "chat message %d has no role: %+v", i, msg)
	}
}
