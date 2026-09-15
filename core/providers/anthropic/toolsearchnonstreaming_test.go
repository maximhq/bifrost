package anthropic

import (
	"context"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Tool_search content-block coverage for the NON-STREAMING path: a plain
// /v1/messages response, and replayed conversation history on a follow-up
// request. toolsearch_test.go covers the streaming SSE path.

const (
	tsnServerToolUseID  = "srvtoolu_tsn_1"
	tsnCallerToolID     = "toolu_caller_tsn_1"
	tsnDiscoveredTool   = "OpenMeteoMCP-weather_forecast"
	tsnDiscoveredCallID = "toolu_weather_tsn_1"
)

// toolSearchNonStreamingBlocks builds the three-block sequence Anthropic emits
// for a server-side tool_search: server_tool_use(tool_search variant) ->
// tool_search_tool_result(tool_references) -> tool_use(discovered tool).
func toolSearchNonStreamingBlocks(toolName string) []AnthropicContentBlock {
	return []AnthropicContentBlock{
		{
			Type:  AnthropicContentBlockTypeServerToolUse,
			ID:    schemas.Ptr(tsnServerToolUseID),
			Name:  schemas.Ptr(toolName),
			Input: []byte(`{"query":"weather"}`),
			Caller: &AnthropicToolCaller{
				Type:   AnthropicToolCallerTypeCodeExecution20250825,
				ToolID: schemas.Ptr(tsnCallerToolID),
			},
		},
		{
			// The wire nests the references under a single
			// tool_search_tool_search_result content object (live-verified
			// against api.anthropic.com, 2026-08-26).
			Type:      AnthropicContentBlockTypeToolSearchToolResult,
			ToolUseID: schemas.Ptr(tsnServerToolUseID),
			Content: &AnthropicContent{ContentObj: &AnthropicContentBlock{
				Type: AnthropicContentBlockType("tool_search_tool_search_result"),
				ToolReferences: []AnthropicContentBlock{
					{Type: AnthropicContentBlockTypeToolReference, ToolName: schemas.Ptr(tsnDiscoveredTool)},
				},
			}},
		},
		{
			Type:  AnthropicContentBlockTypeToolUse,
			ID:    schemas.Ptr(tsnDiscoveredCallID),
			Name:  schemas.Ptr(tsnDiscoveredTool),
			Input: []byte(`{"location":"Tokyo"}`),
		},
	}
}

// findToolSearchCall returns the tool_search_call message in msgs, if any.
func findToolSearchCall(msgs []schemas.ResponsesMessage) *schemas.ResponsesMessage {
	for i := range msgs {
		if msgs[i].Type != nil && *msgs[i].Type == schemas.ResponsesMessageTypeToolSearchCall {
			return &msgs[i]
		}
	}
	return nil
}

// TestToolSearch_NonStreamingResponseForwardsToolReferences asserts a plain
// (non-streaming) AnthropicMessageResponse containing server_tool_use(tool_search)
// + tool_search_tool_result survives ToBifrostResponsesResponse as a
// tool_search_call carrying the discovered tool_references, instead of being
// silently dropped. Fails on the unpatched provider (Output has no
// tool_search_call at all).
func TestToolSearch_NonStreamingResponseForwardsToolReferences(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		toolName string
	}{
		{"regex", string(AnthropicToolNameToolSearchRegex)},
		{"bm25", string(AnthropicToolNameToolSearchBM25)},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			resp := &AnthropicMessageResponse{
				ID:      "msg_tsn_test",
				Type:    "message",
				Role:    string(AnthropicMessageRoleAssistant),
				Content: toolSearchNonStreamingBlocks(tc.toolName),
				Model:   "claude-sonnet-4-6",
				Usage:   &AnthropicUsage{InputTokens: 10, OutputTokens: 5},
			}

			bifrostResp := resp.ToBifrostResponsesResponse(ctx)
			if bifrostResp == nil {
				t.Fatal("ToBifrostResponsesResponse returned nil")
			}

			call := findToolSearchCall(bifrostResp.Output)
			if call == nil {
				t.Fatal("no tool_search_call in Output — tool_search_tool_result was dropped on the non-streaming response path")
			}
			if call.ID == nil || *call.ID != tsnServerToolUseID {
				t.Fatalf("tool_search_call ID = %v, want %q", call.ID, tsnServerToolUseID)
			}
			if call.ResponsesToolMessage == nil || call.ResponsesToolMessage.ResponsesToolSearchCall == nil {
				t.Fatal("tool_search_call carries no ResponsesToolSearchCall payload")
			}
			refs := call.ResponsesToolMessage.ResponsesToolSearchCall.ToolReferences
			if len(refs) != 1 || refs[0] != tsnDiscoveredTool {
				t.Fatalf("tool_references = %v, want [%q]", refs, tsnDiscoveredTool)
			}
			if call.ResponsesToolMessage.Name == nil || *call.ResponsesToolMessage.Name != tc.toolName {
				t.Fatalf("tool_search_call Name = %v, want %q", call.ResponsesToolMessage.Name, tc.toolName)
			}
		})
	}
}

// TestToolSearch_NonStreamingRoundTripPreservesBlocks feeds a non-streaming
// Anthropic response through the full forward-then-reverse round trip
// (ToBifrostResponsesResponse -> ConvertBifrostMessagesToAnthropicMessages) and
// asserts the server_tool_use + tool_search_tool_result blocks come back with
// the same id, tool name and tool_references the upstream response carried —
// the exact contract Anthropic's continuation flow requires. This is the
// non-streaming counterpart to TestToolSearch_ReverseRebuildsAnthropicBlocks,
// which starts from a hand-built canonical message rather than a parsed
// response and so did not catch this gap.
func TestToolSearch_NonStreamingRoundTripPreservesBlocks(t *testing.T) {
	t.Parallel()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	resp := &AnthropicMessageResponse{
		ID:      "msg_tsn_rt",
		Type:    "message",
		Role:    string(AnthropicMessageRoleAssistant),
		Content: toolSearchNonStreamingBlocks(string(AnthropicToolNameToolSearchBM25)),
		Model:   "claude-sonnet-4-6",
		Usage:   &AnthropicUsage{InputTokens: 10, OutputTokens: 5},
	}

	bifrostResp := resp.ToBifrostResponsesResponse(ctx)
	if bifrostResp == nil || len(bifrostResp.Output) == 0 {
		t.Fatal("ToBifrostResponsesResponse produced no output")
	}

	msgs, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, bifrostResp.Output, true,
		schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-6"))

	var serverToolUse, resultBlock *AnthropicContentBlock
	for mi := range msgs {
		for bi := range msgs[mi].Content.ContentBlocks {
			b := &msgs[mi].Content.ContentBlocks[bi]
			switch b.Type {
			case AnthropicContentBlockTypeServerToolUse:
				if b.Name != nil && *b.Name == string(AnthropicToolNameToolSearchBM25) {
					serverToolUse = b
				}
			case AnthropicContentBlockTypeToolSearchToolResult:
				resultBlock = b
			}
		}
	}

	if serverToolUse == nil {
		t.Fatal("round trip dropped tool_search_call — no server_tool_use(tool_search) block rebuilt")
	}
	if serverToolUse.ID == nil || *serverToolUse.ID != tsnServerToolUseID {
		t.Fatalf("server_tool_use ID = %v, want %q", serverToolUse.ID, tsnServerToolUseID)
	}
	if serverToolUse.Caller == nil || serverToolUse.Caller.Type != AnthropicToolCallerTypeCodeExecution20250825 ||
		serverToolUse.Caller.ToolID == nil || *serverToolUse.Caller.ToolID != tsnCallerToolID {
		t.Fatalf("round trip dropped the caller: %+v", serverToolUse.Caller)
	}

	if string(serverToolUse.Input) != `{"query":"weather"}` {
		t.Fatalf("round trip lost the input query: %s", serverToolUse.Input)
	}
	if resultBlock == nil {
		t.Fatal("round trip dropped the tool_search_tool_result block")
	}
	if resultBlock.Caller == nil || resultBlock.Caller.Type != AnthropicToolCallerTypeCodeExecution20250825 {
		t.Fatalf("round trip dropped the caller on the result block: %+v", resultBlock.Caller)
	}
	if resultBlock.ToolUseID == nil || *resultBlock.ToolUseID != tsnServerToolUseID {
		t.Fatalf("tool_search_tool_result tool_use_id = %v, want %q", resultBlock.ToolUseID, tsnServerToolUseID)
	}
	refs := rebuiltToolRefs(resultBlock)
	if len(refs) != 1 || refs[0].ToolName == nil || *refs[0].ToolName != tsnDiscoveredTool {
		t.Fatalf("round-tripped tool_references = %+v, want one nested ref to %q", refs, tsnDiscoveredTool)
	}
}

// TestToolSearch_RequestHistoryForwardsToolReferences asserts that when a
// client replays a previous assistant turn's server_tool_use(tool_search) +
// tool_search_tool_result blocks as conversation history on a follow-up
// request — exactly what Anthropic's continuation contract requires — Bifrost
// preserves them into the outbound request instead of silently dropping them
// before forwarding to Anthropic. This is the request-side half of the
// harness#189 defect: ToBifrostResponsesRequest funnels history through the
// same convertAnthropicContentBlocksToResponsesMessages converter as the
// response path.
func TestToolSearch_RequestHistoryForwardsToolReferences(t *testing.T) {
	t.Parallel()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	req := &AnthropicMessageRequest{
		Model:     "anthropic/claude-sonnet-4-6",
		MaxTokens: 1024,
		Messages: []AnthropicMessage{
			{
				Role: AnthropicMessageRoleAssistant,
				Content: AnthropicContent{
					ContentBlocks: toolSearchNonStreamingBlocks(string(AnthropicToolNameToolSearchRegex)),
				},
			},
			{
				Role: AnthropicMessageRoleUser,
				Content: AnthropicContent{
					ContentStr: schemas.Ptr("what's next?"),
				},
			},
		},
	}

	bifrostReq := req.ToBifrostResponsesRequest(ctx)
	if bifrostReq == nil {
		t.Fatal("ToBifrostResponsesRequest returned nil")
	}

	call := findToolSearchCall(bifrostReq.Input)
	if call == nil {
		t.Fatal("no tool_search_call in the parsed request history — tool_search_tool_result was dropped before forwarding to Anthropic")
	}
	if call.ResponsesToolMessage == nil || call.ResponsesToolMessage.ResponsesToolSearchCall == nil {
		t.Fatal("history tool_search_call carries no ResponsesToolSearchCall payload")
	}
	refs := call.ResponsesToolMessage.ResponsesToolSearchCall.ToolReferences
	if len(refs) != 1 || refs[0] != tsnDiscoveredTool {
		t.Fatalf("history tool_references = %v, want [%q]", refs, tsnDiscoveredTool)
	}
}

// rebuiltToolRefs extracts the tool_reference blocks from a rebuilt
// tool_search_tool_result block's nested content object.
func rebuiltToolRefs(b *AnthropicContentBlock) []AnthropicContentBlock {
	if b == nil || b.Content == nil {
		return nil
	}
	inner := b.Content.ContentObj
	if inner == nil && len(b.Content.ContentBlocks) > 0 {
		inner = &b.Content.ContentBlocks[0]
	}
	if inner == nil {
		return nil
	}
	return inner.ToolReferences
}
