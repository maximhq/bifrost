package anthropic

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// rawToolSearchNonStreamingResponse mirrors a non-streaming Anthropic Messages API
// response containing a tool_search call and its paired result. Regression fixture
// for the non-streaming counterpart of #4780 (the streaming path was fixed first;
// this proves the non-streaming ingest path recognizes tool_search too, and that a
// later turn replaying the item back to Anthropic reconstructs an equivalent pair —
// found missing by an independent review of the streaming-only fix).
const rawToolSearchNonStreamingResponse = `{
  "model": "claude-opus-4-8",
  "id": "msg_01ToolSearchNonStreaming",
  "type": "message",
  "role": "assistant",
  "content": [
    { "type": "server_tool_use", "id": "srvtoolu_ns1", "name": "tool_search_tool_bm25", "input": {"query": "weather"} },
    { "type": "tool_search_tool_result", "tool_use_id": "srvtoolu_ns1", "content": {"type": "tool_search_tool_search_result", "tool_references": [{"type": "tool_reference", "tool_name": "get_weather"}, {"type": "tool_reference", "tool_name": "get_forecast"}]} },
    { "type": "text", "text": "I found the get_weather tool." }
  ],
  "stop_reason": "end_turn",
  "usage": { "input_tokens": 20, "output_tokens": 10 }
}`

// TestToolSearch_NonStreamingIngestAndEgressRoundTrip verifies that a non-streaming
// Anthropic response containing a tool_search call+result (a) ingests into a
// tool_search_tool_call Bifrost item carrying the discovered tool names and the
// original query, and (b) that item converts back into an equivalent
// server_tool_use + tool_search_tool_result pair when replayed to Anthropic on a
// later turn (egress).
func TestToolSearch_NonStreamingIngestAndEgressRoundTrip(t *testing.T) {
	var resp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(rawToolSearchNonStreamingResponse), &resp); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	bifrostResp := resp.ToBifrostResponsesResponse(ctx)
	if bifrostResp == nil {
		t.Fatal("ToBifrostResponsesResponse returned nil")
	}

	var toolSearchItem *schemas.ResponsesMessage
	for i := range bifrostResp.Output {
		out := &bifrostResp.Output[i]
		if out.Type != nil && *out.Type == schemas.ResponsesMessageTypeAnthropicToolSearchCall {
			toolSearchItem = out
		}
	}
	if toolSearchItem == nil {
		t.Fatal("expected a tool_search_tool_call output item, got none")
	}
	if toolSearchItem.Status == nil || *toolSearchItem.Status != "completed" {
		t.Errorf("ingested item status = %v, want completed", toolSearchItem.Status)
	}
	if toolSearchItem.ResponsesToolMessage == nil || toolSearchItem.ResponsesToolMessage.Output == nil ||
		toolSearchItem.ResponsesToolMessage.Output.ResponsesToolCallOutputStr == nil {
		t.Fatal("ingested item missing Output")
	}
	out := *toolSearchItem.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
	if !strings.Contains(out, "get_weather") || !strings.Contains(out, "get_forecast") {
		t.Errorf("ingested output = %q, want it to contain both discovered tool names", out)
	}
	if toolSearchItem.ResponsesToolMessage.Arguments == nil || !strings.Contains(*toolSearchItem.ResponsesToolMessage.Arguments, "weather") {
		t.Errorf("ingested item did not preserve the original call input (query), got Arguments = %v", toolSearchItem.ResponsesToolMessage.Arguments)
	}

	// Simulate the real multi-turn path: Bifrost serializes this item back to the
	// client as JSON, the client stores it and resends it verbatim on the next
	// turn, and Bifrost decodes it via ResponsesMessage's custom UnmarshalJSON.
	// This matters because a DIFFERENT type string ("tool_search_call", used by
	// Codex/OpenAI's own client-executed tool_search meta-tool) is intercepted by
	// isToolSearchItem and preserved as raw bytes rather than populating
	// ResponsesToolMessage at all — confirming our distinct "tool_search_tool_call"
	// type does NOT fall into that trap and egress still sees a populated item.
	rawItem, err := sonic.Marshal(toolSearchItem)
	if err != nil {
		t.Fatalf("marshal ingested item: %v", err)
	}
	var decodedItem schemas.ResponsesMessage
	if err := sonic.Unmarshal(rawItem, &decodedItem); err != nil {
		t.Fatalf("unmarshal ingested item: %v", err)
	}
	if decodedItem.ResponsesToolMessage == nil {
		t.Fatal("decoded item lost its ResponsesToolMessage — the type string collided with Codex's raw tool_search preservation path")
	}

	// Egress: replay the JSON-round-tripped item back to Anthropic as if it were
	// prior-turn history.
	anthropicMessages, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, []schemas.ResponsesMessage{decodedItem}, true, schemas.Anthropic, "claude-opus-4-8")
	if len(anthropicMessages) == 0 {
		t.Fatal("expected at least one reconstructed Anthropic message, got none")
	}

	var sawServerToolUse, sawResult bool
	for _, m := range anthropicMessages {
		for _, block := range m.Content.ContentBlocks {
			switch block.Type {
			case AnthropicContentBlockTypeServerToolUse:
				if block.Name == nil || *block.Name != "tool_search_tool_bm25" {
					t.Errorf("reconstructed server_tool_use name = %v, want tool_search_tool_bm25", block.Name)
				}
				if block.ID == nil || *block.ID != "srvtoolu_ns1" {
					t.Errorf("reconstructed server_tool_use id = %v, want srvtoolu_ns1", block.ID)
				}
				sawServerToolUse = true
			case AnthropicContentBlockTypeToolSearchToolResult:
				if block.ToolUseID == nil || *block.ToolUseID != "srvtoolu_ns1" {
					t.Errorf("reconstructed tool_search_tool_result tool_use_id = %v, want srvtoolu_ns1", block.ToolUseID)
				}
				var names []string
				for _, ref := range toolSearchResultReferences(&block) {
					if ref.ToolName != nil {
						names = append(names, *ref.ToolName)
					}
				}
				if !slices.Contains(names, "get_weather") || !slices.Contains(names, "get_forecast") {
					t.Errorf("reconstructed tool_references = %v, want both discovered tool names", names)
				}
				sawResult = true
			}
		}
	}
	if !sawServerToolUse {
		t.Error("expected a reconstructed server_tool_use block, got none — the call is not round-trippable")
	}
	if !sawResult {
		t.Error("expected a reconstructed tool_search_tool_result block, got none — the result is not round-trippable")
	}
}

// rawToolSearchWithCallerResponse is like rawToolSearchNonStreamingResponse but
// the tool_search call carries a "caller" (set when the search was spawned
// from inside a code execution sandbox — programmatic tool calling).
// Regression fixture: web_search preserves caller both ways, tool_search
// initially did not (found in a second-round Codex review).
const rawToolSearchWithCallerResponse = `{
  "model": "claude-opus-4-8",
  "id": "msg_01ToolSearchCaller",
  "type": "message",
  "role": "assistant",
  "content": [
    { "type": "server_tool_use", "id": "srvtoolu_caller1", "name": "tool_search_tool_bm25", "input": {"query": "weather"}, "caller": {"type": "code_execution_20250825", "tool_id": "srvtoolu_codeexec1"} },
    { "type": "tool_search_tool_result", "tool_use_id": "srvtoolu_caller1", "content": {"type": "tool_search_tool_search_result", "tool_references": [{"type": "tool_reference", "tool_name": "get_weather"}]} }
  ],
  "stop_reason": "end_turn",
  "usage": { "input_tokens": 20, "output_tokens": 10 }
}`

// TestToolSearch_CallerPreservedIngestAndEgress verifies the "caller" (set when
// a tool_search call is spawned from inside code execution) survives both
// non-streaming ingest and egress replay, matching web_search's existing
// caller-preservation behavior.
func TestToolSearch_CallerPreservedIngestAndEgress(t *testing.T) {
	var resp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(rawToolSearchWithCallerResponse), &resp); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	bifrostResp := resp.ToBifrostResponsesResponse(ctx)
	if bifrostResp == nil {
		t.Fatal("ToBifrostResponsesResponse returned nil")
	}

	var toolSearchItem *schemas.ResponsesMessage
	for i := range bifrostResp.Output {
		out := &bifrostResp.Output[i]
		if out.Type != nil && *out.Type == schemas.ResponsesMessageTypeAnthropicToolSearchCall {
			toolSearchItem = out
		}
	}
	if toolSearchItem == nil {
		t.Fatal("expected a tool_search_tool_call output item, got none")
	}
	if toolSearchItem.ResponsesToolMessage == nil || toolSearchItem.ResponsesToolMessage.Caller == nil {
		t.Fatal("ingested item did not preserve caller")
	}
	if toolSearchItem.ResponsesToolMessage.Caller.ToolID == nil || *toolSearchItem.ResponsesToolMessage.Caller.ToolID != "srvtoolu_codeexec1" {
		t.Errorf("ingested caller.ToolID = %v, want srvtoolu_codeexec1", toolSearchItem.ResponsesToolMessage.Caller.ToolID)
	}

	anthropicMessages, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, []schemas.ResponsesMessage{*toolSearchItem}, true, schemas.Anthropic, "claude-opus-4-8")
	if len(anthropicMessages) == 0 {
		t.Fatal("expected at least one reconstructed Anthropic message, got none")
	}

	var sawCallerOnCall, sawCallerOnResult bool
	for _, m := range anthropicMessages {
		for _, block := range m.Content.ContentBlocks {
			switch block.Type {
			case AnthropicContentBlockTypeServerToolUse:
				if block.Caller != nil && block.Caller.ToolID != nil && *block.Caller.ToolID == "srvtoolu_codeexec1" {
					sawCallerOnCall = true
				}
			case AnthropicContentBlockTypeToolSearchToolResult:
				if block.Caller != nil && block.Caller.ToolID != nil && *block.Caller.ToolID == "srvtoolu_codeexec1" {
					sawCallerOnResult = true
				}
			}
		}
	}
	if !sawCallerOnCall {
		t.Error("reconstructed server_tool_use block is missing caller")
	}
	if !sawCallerOnResult {
		t.Error("reconstructed tool_search_tool_result block is missing caller")
	}
}
