package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestAnthropicRoute_ToolsLessOmitsToolsOnWire locks in the fix for issue #5179:
// an Anthropic-protocol request with no (or an empty) tools list must be converted
// without emitting "tools": [] on the wire. Strict OpenAI-compatible backends
// (e.g. vLLM >= 0.20) reject an empty tools array with a 400, so the field must be
// omitted entirely. The /anthropic/v1/messages route builds a Responses request,
// which for a chat-only backend falls back to a Chat request via ToChatRequest().
//
// This test exercises the full conversion chain for both the chat-fallback wire
// and the direct /v1/responses wire.
func TestAnthropicRoute_ToolsLessOmitsToolsOnWire(t *testing.T) {
	cases := map[string]string{
		"no_tools_field": `{"model":"myvllm/deepseek","max_tokens":32,"messages":[{"role":"user","content":"say ok"}]}`,
		"empty_tools":    `{"model":"myvllm/deepseek","max_tokens":32,"tools":[],"messages":[{"role":"user","content":"say ok"}]}`,
		"tool_choice_only": `{"model":"myvllm/deepseek","max_tokens":32,"tool_choice":{"type":"auto"},` +
			`"messages":[{"role":"user","content":"say ok"}]}`,
	}

	ctx := &schemas.BifrostContext{}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var areq AnthropicMessageRequest
			if err := json.Unmarshal([]byte(body), &areq); err != nil {
				t.Fatalf("unmarshal anthropic request: %v", err)
			}

			respReq := areq.ToBifrostResponsesRequest(ctx)
			if respReq.Params != nil && len(respReq.Params.Tools) != 0 {
				t.Errorf("ToBifrostResponsesRequest set %d tools, want none", len(respReq.Params.Tools))
			}

			// Chat-completion fallback wire (vLLM / custom OpenAI-compatible providers).
			chatWire, err := openai.ToOpenAIChatRequest(ctx, respReq.ToChatRequest()).MarshalJSON()
			if err != nil {
				t.Fatalf("marshal chat wire: %v", err)
			}
			if strings.Contains(string(chatWire), `"tools"`) {
				t.Errorf("chat wire contains a tools field, want it omitted: %s", string(chatWire))
			}

			// Direct /v1/responses wire.
			respWire, err := json.Marshal(openai.ToOpenAIResponsesRequest(ctx, respReq))
			if err != nil {
				t.Fatalf("marshal responses wire: %v", err)
			}
			if strings.Contains(string(respWire), `"tools"`) {
				t.Errorf("responses wire contains a tools field, want it omitted: %s", string(respWire))
			}
		})
	}
}
