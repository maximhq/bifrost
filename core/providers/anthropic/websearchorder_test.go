package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// A replayed assistant turn that interleaves thinking with several web_search
// server-tool pairs must leave /anthropic/v1/messages in the order the client
// sent it. A thinking block's signature binds to the content before it, so a
// turn rebuilt with its thinking hoisted ahead of an earlier search is rejected
// by Anthropic with "thinking blocks in the latest assistant message cannot be
// modified" (maximhq/bifrost#10451).
const multiSearchTurnRequest = `{
 "model": "claude-opus-5-5", "max_tokens": 1024, "stream": true, "thinking": {"type": "adaptive"},
 "tools": [
  {"type": "web_search_20250305", "name": "web_search", "max_uses": 5},
  {"name": "get_weather", "description": "Weather for a city",
   "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}
 ],
 "messages": [
  {"role": "user", "content": "Which ESXi build fixes VMSA-2025-0013, and what is the weather in Paris?"},
  {"role": "assistant", "content": [
   {"type": "thinking", "thinking": "th0 first search", "signature": "sig_0"},
   {"type": "server_tool_use", "id": "srvtoolu_1", "name": "web_search", "input": {"query": "VMSA-2025-0013"}},
   {"type": "web_search_tool_result", "tool_use_id": "srvtoolu_1", "content": [{"type": "web_search_result", "title": "r1", "url": "https://example.com/1", "encrypted_content": "AAAA", "page_age": null}]},
   {"type": "thinking", "thinking": "th3 second search", "signature": "sig_3"},
   {"type": "server_tool_use", "id": "srvtoolu_4", "name": "web_search", "input": {"query": "ESXi 8.0 U3f build"}},
   {"type": "web_search_tool_result", "tool_use_id": "srvtoolu_4", "content": [{"type": "web_search_result", "title": "r4", "url": "https://example.com/4", "encrypted_content": "AAAA", "page_age": null}]},
   {"type": "thinking", "thinking": "th6 call the tool", "signature": "sig_6"},
   {"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "Paris"}}
  ]},
  {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": "18C, clear"}]}
 ]
}`

// roundTripAnthropicRequest runs a Messages request through the ingress and
// egress converters and returns the rebuilt request.
func roundTripAnthropicRequest(t *testing.T, body string) *AnthropicMessageRequest {
	t.Helper()
	var req AnthropicMessageRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	out, err := ToAnthropicResponsesRequest(ctx, req.ToBifrostResponsesRequest(ctx))
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	return out
}

// blockSummary renders content blocks as "type[:thinking]" so a reorder shows
// up as a readable diff.
func blockSummary(blocks []AnthropicContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		s := string(b.Type)
		if b.Thinking != nil {
			s += ":" + *b.Thinking
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " | ")
}

func TestToAnthropicResponsesRequest_KeepsThinkingInPlaceAcrossMultipleWebSearches(t *testing.T) {
	out := roundTripAnthropicRequest(t, multiSearchTurnRequest)
	if len(out.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (user, assistant, tool_result)", len(out.Messages))
	}
	assistant := out.Messages[1]
	if assistant.Role != AnthropicMessageRoleAssistant {
		t.Fatalf("messages[1].role = %q, want assistant", assistant.Role)
	}
	want := "thinking:th0 first search | server_tool_use | web_search_tool_result | " +
		"thinking:th3 second search | server_tool_use | web_search_tool_result | " +
		"thinking:th6 call the tool | tool_use"
	if got := blockSummary(assistant.Content.ContentBlocks); got != want {
		t.Fatalf("assistant turn reordered on egress\n got: %s\nwant: %s", got, want)
	}
}
