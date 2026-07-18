package anthropic

import (
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// rawExtendedThinkingToolCallResponse mirrors issue #3802: an Anthropic Messages API
// response with extended thinking enabled, where the assistant turn contains both a
// thinking block and a tool_use block. The bug report claimed reasoning_content is
// dropped when this turn is routed onward to a custom Anthropic-base provider (Kimi)
// that requires it to be echoed back on the next turn — reproduced here by chaining
// Bifrost's real Anthropic ingest conversion into the real Responses->Chat bridge that
// such a custom OpenAI-compatible provider would consume.
const rawExtendedThinkingToolCallResponse = `{
  "model": "claude-opus-4-8",
  "id": "msg_01ExtendedThinking",
  "type": "message",
  "role": "assistant",
  "content": [
    { "type": "thinking", "thinking": "I should look up the current weather before answering.", "signature": "sig_abc123" },
    { "type": "tool_use", "id": "toolu_01Weather", "name": "get_weather", "input": {"location": "SF"} }
  ],
  "stop_reason": "tool_use",
  "usage": { "input_tokens": 120, "output_tokens": 45 }
}`

// TestExtendedThinkingToolCallTurn_ReasoningSurvivesResponsesToChatBridge is the
// end-to-end regression test for #3802. It reproduces the full reported pipeline:
// Anthropic response (thinking + tool_use in the same turn) -> Bifrost's Responses-shaped
// intermediate (ToBifrostResponsesResponse) -> the Responses->Chat fallback bridge
// (schemas.ToChatMessages) that a custom Anthropic-base provider like Kimi would receive
// its history through. If reasoning is dropped anywhere in this chain, the assistant
// tool-call message that reaches Kimi on the next turn would be missing reasoning_content.
func TestExtendedThinkingToolCallTurn_ReasoningSurvivesResponsesToChatBridge(t *testing.T) {
	var resp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(rawExtendedThinkingToolCallResponse), &resp); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})

	bifrostResp := resp.ToBifrostResponsesResponse(ctx)
	if bifrostResp == nil {
		t.Fatal("ToBifrostResponsesResponse returned nil")
	}

	var sawReasoning, sawFunctionCall bool
	for _, out := range bifrostResp.Output {
		if out.Type == nil {
			continue
		}
		switch *out.Type {
		case schemas.ResponsesMessageTypeReasoning:
			sawReasoning = true
		case schemas.ResponsesMessageTypeFunctionCall:
			sawFunctionCall = true
		}
	}
	if !sawReasoning {
		t.Fatal("expected a reasoning output item from the Anthropic thinking block, got none")
	}
	if !sawFunctionCall {
		t.Fatal("expected a function_call output item from the Anthropic tool_use block, got none")
	}

	// Feed the Responses-shaped output through the same Responses->Chat bridge a
	// custom OpenAI-compatible provider (Kimi) would use to build its wire history.
	chatMessages := schemas.ToChatMessages(bifrostResp.Output)

	var found bool
	for _, cm := range chatMessages {
		if cm.ChatAssistantMessage == nil || len(cm.ChatAssistantMessage.ToolCalls) == 0 {
			continue
		}
		found = true
		if cm.ChatAssistantMessage.Reasoning == nil {
			t.Fatal("reasoning_content dropped on the assistant tool-call turn (#3802) — Reasoning is nil")
		}
		if *cm.ChatAssistantMessage.Reasoning == "" {
			t.Fatal("reasoning_content dropped on the assistant tool-call turn (#3802) — Reasoning is empty")
		}
	}
	if !found {
		t.Fatal("expected an assistant message with tool calls in the bridged chat messages")
	}
}
