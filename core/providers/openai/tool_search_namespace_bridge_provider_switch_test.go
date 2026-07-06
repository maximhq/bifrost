package openai

import (
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToolSearchNamespaceBridge_SwitchToOpenAIBackend is the explicit
// provider-switch check: the same session payload (tools[] still carrying
// Bifrost's reserved tool_search bridge namespace, exactly as an
// OpenAI-Responses-shaped caller would send it) is now routed to an OpenAI
// backend instead of Anthropic. The tools[] OpenAI actually receives must:
//  1. Contain zero Anthropic-native tokens (tool_search_tool_bm25/_regex,
//     server_tool_use, tool_search_tool_result) -- nothing Anthropic-specific
//     should ever reach an OpenAI-bound request.
//  2. Still contain the namespace/function_search declarations verbatim --
//     OpenAI already natively accepts "namespace" and "function" tool types
//     (filterUnsupportedTools keeps both), so no conversion is even needed on
//     this path; the model literally never sees anything Anthropic-flavored.
func TestToolSearchNamespaceBridge_SwitchToOpenAIBackend(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-5.2",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("find a weather tool")},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather")},
				{
					Type: schemas.ResponsesToolTypeNamespace,
					Name: schemas.Ptr(schemas.ToolSearchBridgeNamespaceID),
					ResponsesToolNamespace: &schemas.ResponsesToolNamespace{
						Tools: []schemas.ResponsesTool{
							{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr(schemas.ToolSearchBridgeFuncBM25)},
							{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr(schemas.ToolSearchBridgeFuncRegex)},
						},
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	openAIReq := ToOpenAIResponsesRequest(ctx, bifrostReq)
	if openAIReq == nil {
		t.Fatal("ToOpenAIResponsesRequest returned nil")
	}

	wire, err := openAIReq.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(wire)

	// (1) No Anthropic-native leakage whatsoever.
	for _, forbidden := range []string{
		"tool_search_tool_bm25", "tool_search_tool_regex",
		"server_tool_use", "tool_search_tool_result",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("Anthropic-native token %q leaked into the OpenAI-bound request: %s", forbidden, raw)
		}
	}

	// (2) The bridge namespace shape passes straight through, untouched --
	// it's already valid native OpenAI wire format.
	for _, want := range []string{
		`"type":"namespace"`, schemas.ToolSearchBridgeNamespaceID,
		schemas.ToolSearchBridgeFuncBM25, schemas.ToolSearchBridgeFuncRegex,
		"get_weather",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("expected %q to survive untouched in the OpenAI-bound request, got: %s", want, raw)
		}
	}
}
