package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToolSearchNamespaceBridge_DeclarationEgress verifies that an OpenAI
// Responses-shaped caller declaring Bifrost's reserved tool_search bridge
// namespace produces real Anthropic tool_search_tool_bm25/_regex tool
// declarations when the request is egressed to an Anthropic backend --
// exercising the wiring added to ToAnthropicResponsesRequest.
func TestToolSearchNamespaceBridge_DeclarationEgress(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-8",
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

	anthropicReq, err := ToAnthropicResponsesRequest(nil, bifrostReq)
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}

	var sawBM25, sawRegex, sawFunction, sawNamespaceLeak bool
	for _, tool := range anthropicReq.Tools {
		switch {
		case tool.Type != nil && strings.Contains(string(*tool.Type), "tool_search_tool_bm25"):
			sawBM25 = true
		case tool.Type != nil && strings.Contains(string(*tool.Type), "tool_search_tool_regex"):
			sawRegex = true
		case tool.Name == "get_weather":
			sawFunction = true
		}
		// The synthetic bridge namespace must never leak onto the Anthropic
		// wire -- Anthropic has no "namespace" tool concept at all.
		if tool.Type != nil && strings.Contains(string(*tool.Type), "namespace") {
			sawNamespaceLeak = true
		}
	}

	if !sawBM25 {
		t.Errorf("expected a native tool_search_tool_bm25 declaration, got tools=%+v", anthropicReq.Tools)
	}
	if !sawRegex {
		t.Errorf("expected a native tool_search_tool_regex declaration, got tools=%+v", anthropicReq.Tools)
	}
	if !sawFunction {
		t.Errorf("unrelated function tool must survive, got tools=%+v", anthropicReq.Tools)
	}
	if sawNamespaceLeak {
		t.Errorf("bridge namespace type string must never reach the Anthropic wire, got tools=%+v", anthropicReq.Tools)
	}
}

// TestToolSearchNamespaceBridge_ItemEgress verifies that a namespace-tagged
// function_call/function_call_output pair (Bifrost's own caller-facing
// representation of a completed tool_search bridge call) in prior-turn
// history egresses to Anthropic's native server_tool_use +
// tool_search_tool_result block pair.
func TestToolSearchNamespaceBridge_ItemEgress(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-8",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("find a weather tool")},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ID:   schemas.Ptr("srvtoolu_bridge1"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("srvtoolu_bridge1"),
					Name:      schemas.Ptr(schemas.ToolSearchBridgeFuncRegex),
					Namespace: schemas.Ptr(schemas.ToolSearchBridgeNamespaceID),
					Arguments: schemas.Ptr(`{"query":"^GET .*"}`),
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("srvtoolu_bridge1"),
					Namespace: schemas.Ptr(schemas.ToolSearchBridgeNamespaceID),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: schemas.Ptr(`["get_request"]`),
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	anthropicReq, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}

	var sawServerToolUse, sawResult bool
	for _, msg := range anthropicReq.Messages {
		for _, block := range msg.Content.ContentBlocks {
			switch block.Type {
			case AnthropicContentBlockTypeServerToolUse:
				if block.Name == nil || *block.Name != "tool_search_tool_regex" {
					t.Errorf("expected regex sub-tool name preserved through the bridge, got %v", block.Name)
				}
				if block.ID == nil || *block.ID != "srvtoolu_bridge1" {
					t.Errorf("expected call id preserved, got %v", block.ID)
				}
				sawServerToolUse = true
			case AnthropicContentBlockTypeToolSearchToolResult:
				if block.ToolUseID == nil || *block.ToolUseID != "srvtoolu_bridge1" {
					t.Errorf("expected tool_use_id preserved, got %v", block.ToolUseID)
				}
				var names []string
				for _, ref := range toolSearchResultReferences(&block) {
					if ref.ToolName != nil {
						names = append(names, *ref.ToolName)
					}
				}
				if len(names) != 1 || names[0] != "get_request" {
					t.Errorf("expected discovered tool name to survive the bridge, got %v", names)
				}
				sawResult = true
			}
		}
	}
	if !sawServerToolUse {
		t.Error("expected a reconstructed server_tool_use block from the namespace bridge call, got none")
	}
	if !sawResult {
		t.Error("expected a reconstructed tool_search_tool_result block from the namespace bridge output, got none")
	}
}

// TestToolSearchNamespaceBridge_ProviderSwitchNoAnthropicLeakage is the
// explicit provider-switch regression: the same bifrostReq.Params.Tools slice
// (still in its original namespace-bridge shape -- the Anthropic-egress
// expansion in ToAnthropicResponsesRequest operates on a derived copy and
// must never mutate the shared request) must contain zero Anthropic-native
// type strings when that same slice is what a caller/second dispatch attempt
// would see if routed to OpenAI instead.
func TestToolSearchNamespaceBridge_ProviderSwitchNoAnthropicLeakage(t *testing.T) {
	tools := []schemas.ResponsesTool{
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
	}

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-8",
		Params:   &schemas.ResponsesParameters{Tools: tools},
	}

	// Dispatch to Anthropic once (this is what mutates/derives a copy inside
	// ToAnthropicResponsesRequest).
	if _, err := ToAnthropicResponsesRequest(nil, bifrostReq); err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}

	// The original slice referenced by bifrostReq.Params.Tools -- which a
	// fallback/second attempt against a different backend (e.g. OpenAI) would
	// read from -- must be completely unaffected: still exactly 2 entries,
	// still the bridge namespace shape, no tool_search_tool_bm25/_regex
	// entries spliced in.
	if len(bifrostReq.Params.Tools) != 2 {
		t.Fatalf("Anthropic egress must not mutate the shared request's tools slice, got %d entries: %+v",
			len(bifrostReq.Params.Tools), bifrostReq.Params.Tools)
	}
	for _, tool := range bifrostReq.Params.Tools {
		if tool.Type == schemas.ResponsesToolTypeToolSearch {
			t.Fatalf("Anthropic-native tool_search declaration leaked back into the shared request meant for other backends: %+v", tool)
		}
	}
	if bifrostReq.Params.Tools[1].Name == nil || !schemas.IsToolSearchBridgeNamespace(bifrostReq.Params.Tools[1].Name) {
		t.Fatalf("bridge namespace declaration must still be present, untouched, for a subsequent OpenAI-backend dispatch: %+v",
			bifrostReq.Params.Tools[1])
	}
}

// TestToolSearchNamespaceBridge_ResponseEgress is the full round trip: a
// caller declares tool_search via the namespace bridge (ingest sets
// schemas.BifrostContextKeyToolSearchBridgeActive on the context), Anthropic
// completes a real tool_search_tool_bm25 call, and the response converted
// back via ToBifrostResponsesResponse must show the caller the
// namespace-disguised function_call/function_call_output pair -- not the
// internal neutral tool_search_tool_call hub type -- so a strict
// OpenAI-Responses-spec client (which has no concept of
// "tool_search_tool_call") can parse it.
func TestToolSearchNamespaceBridge_ResponseEgress(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-8",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeNamespace,
					Name: schemas.Ptr(schemas.ToolSearchBridgeNamespaceID),
					ResponsesToolNamespace: &schemas.ResponsesToolNamespace{
						Tools: []schemas.ResponsesTool{
							{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr(schemas.ToolSearchBridgeFuncBM25)},
						},
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	if _, err := ToAnthropicResponsesRequest(ctx, bifrostReq); err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}

	anthropicResp := &AnthropicMessageResponse{
		ID:    "msg_1",
		Model: "claude-opus-4-8",
		Content: []AnthropicContentBlock{
			{
				Type:  AnthropicContentBlockTypeServerToolUse,
				ID:    schemas.Ptr("srvtoolu_bm25_1"),
				Name:  schemas.Ptr("tool_search_tool_bm25"),
				Input: json.RawMessage(`{"query":"weather lookup"}`),
			},
			{
				Type:      AnthropicContentBlockTypeToolSearchToolResult,
				ToolUseID: schemas.Ptr("srvtoolu_bm25_1"),
				Content: &AnthropicContent{
					ContentObj: &AnthropicContentBlock{
						Type: AnthropicContentBlockTypeToolSearchToolSearchResult,
						ToolReferences: []AnthropicContentBlock{
							{ToolName: schemas.Ptr("get_weather")},
						},
					},
				},
			},
		},
	}

	bifrostResp := anthropicResp.ToBifrostResponsesResponse(ctx)
	if bifrostResp == nil {
		t.Fatal("ToBifrostResponsesResponse returned nil")
	}

	var sawFunctionCall, sawFunctionCallOutput, sawRawHubLeak bool
	for _, msg := range bifrostResp.Output {
		if msg.Type == nil {
			continue
		}
		switch *msg.Type {
		case schemas.ResponsesMessageTypeAnthropicToolSearchCall:
			sawRawHubLeak = true
		case schemas.ResponsesMessageTypeFunctionCall:
			if msg.ResponsesToolMessage == nil || !schemas.IsToolSearchBridgeNamespace(msg.ResponsesToolMessage.Namespace) {
				t.Errorf("expected function_call tagged with the bridge namespace, got %+v", msg.ResponsesToolMessage)
			}
			if msg.ResponsesToolMessage.Name == nil || *msg.ResponsesToolMessage.Name != schemas.ToolSearchBridgeFuncBM25 {
				t.Errorf("expected the bridge function name tool_search_bm25, got %+v", msg.ResponsesToolMessage.Name)
			}
			sawFunctionCall = true
		case schemas.ResponsesMessageTypeFunctionCallOutput:
			if msg.ResponsesToolMessage == nil || !schemas.IsToolSearchBridgeNamespace(msg.ResponsesToolMessage.Namespace) {
				t.Errorf("expected function_call_output tagged with the bridge namespace, got %+v", msg.ResponsesToolMessage)
			}
			sawFunctionCallOutput = true
		}
	}

	if sawRawHubLeak {
		t.Error("internal tool_search_tool_call hub type must not reach the caller when the bridge is active")
	}
	if !sawFunctionCall {
		t.Errorf("expected a namespace-tagged function_call item in the response, got %+v", bifrostResp.Output)
	}
	if !sawFunctionCallOutput {
		t.Errorf("expected a namespace-tagged function_call_output item in the response, got %+v", bifrostResp.Output)
	}
}

// TestToolSearchNamespaceBridge_ResponseEgress_InactiveWhenNotDeclared
// verifies the collapse only fires when the caller actually used the bridge
// namespace on ingest -- a caller declaring tool_search natively (not via the
// bridge) must keep seeing the raw neutral hub type unchanged.
func TestToolSearchNamespaceBridge_ResponseEgress_InactiveWhenNotDeclared(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-8",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeToolSearch, Name: schemas.Ptr("tool_search_tool_bm25")},
			},
		},
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	if _, err := ToAnthropicResponsesRequest(ctx, bifrostReq); err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}

	anthropicResp := &AnthropicMessageResponse{
		ID:    "msg_1",
		Model: "claude-opus-4-8",
		Content: []AnthropicContentBlock{
			{
				Type:  AnthropicContentBlockTypeServerToolUse,
				ID:    schemas.Ptr("srvtoolu_bm25_1"),
				Name:  schemas.Ptr("tool_search_tool_bm25"),
				Input: json.RawMessage(`{"query":"weather lookup"}`),
			},
			{
				Type:      AnthropicContentBlockTypeToolSearchToolResult,
				ToolUseID: schemas.Ptr("srvtoolu_bm25_1"),
				Content: &AnthropicContent{
					ContentObj: &AnthropicContentBlock{
						Type: AnthropicContentBlockTypeToolSearchToolSearchResult,
						ToolReferences: []AnthropicContentBlock{
							{ToolName: schemas.Ptr("get_weather")},
						},
					},
				},
			},
		},
	}

	bifrostResp := anthropicResp.ToBifrostResponsesResponse(ctx)
	var sawRawHub bool
	for _, msg := range bifrostResp.Output {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeAnthropicToolSearchCall {
			sawRawHub = true
		}
	}
	if !sawRawHub {
		t.Errorf("expected the raw neutral tool_search_tool_call item when the bridge wasn't declared, got %+v", bifrostResp.Output)
	}
}
