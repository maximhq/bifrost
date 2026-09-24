package bedrock

import (
	"testing"

	"github.com/bytedance/sonic"
)

// The Anthropic Messages API accepts a message's content either as an array of
// content blocks or as a plain string, and clients mix the two freely: a
// rolling cache breakpoint tends to produce block arrays for the turns that
// carry cache_control and bare strings for the rest.
//
// BedrockMessage.Content is typed []BedrockContentBlock, so one bare string
// anywhere in messages[] fails the standard unmarshal and diverts the whole
// request into the AI21 fallback. That branch rebuilt the messages but did not
// translate cache_control into cache points, so prompt caching went silently
// off for every message in the request, not only the string one.
//
// Ref: https://github.com/maximhq/bifrost/issues/7336

func cachePointCount(msg BedrockMessage) int {
	n := 0
	for _, block := range msg.Content {
		if block.CachePoint != nil {
			n++
		}
	}
	return n
}

func unmarshalInvoke(t *testing.T, payload string) *BedrockInvokeRequest {
	t.Helper()
	req := &BedrockInvokeRequest{}
	if err := sonic.Unmarshal([]byte(payload), req); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	return req
}

// TestInvokeUnmarshal_CacheControlSurvivesAStringContentMessage is the reported
// failure: the string message is what diverts the request, and the cache point
// belongs to a different message entirely.
func TestInvokeUnmarshal_CacheControlSurvivesAStringContentMessage(t *testing.T) {
	req := unmarshalInvoke(t, `{
		"anthropic_version": "bedrock-2023-05-31",
		"messages": [
			{"role": "user", "content": "an earlier turn, stored as a plain string"},
			{"role": "assistant", "content": [{"type": "text", "text": "reply"}]},
			{"role": "user", "content": [
				{"type": "text", "text": "the turn that carries the breakpoint",
				 "cache_control": {"type": "ephemeral"}}
			]}
		]
	}`)

	if len(req.Messages) != 3 {
		t.Fatalf("parsed %d messages, want 3", len(req.Messages))
	}
	if got := cachePointCount(req.Messages[2]); got != 1 {
		t.Errorf("cache points on the breakpoint message = %d, want 1: cache_control was dropped", got)
	}
	if got := cachePointCount(req.Messages[0]); got != 0 {
		t.Errorf("cache points on the string message = %d, want 0", got)
	}
	if req.Messages[0].Content[0].Text == nil ||
		*req.Messages[0].Content[0].Text != "an earlier turn, stored as a plain string" {
		t.Errorf("the string message lost its text: %+v", req.Messages[0].Content)
	}
}

// TestInvokeUnmarshal_CacheControlOnAStringOnlyRequest pins that a request with
// no block arrays at all still parses and grows no cache points out of nowhere.
func TestInvokeUnmarshal_CacheControlOnAStringOnlyRequest(t *testing.T) {
	req := unmarshalInvoke(t, `{
		"messages": [
			{"role": "user", "content": "one"},
			{"role": "assistant", "content": "two"}
		]
	}`)

	if len(req.Messages) != 2 {
		t.Fatalf("parsed %d messages, want 2", len(req.Messages))
	}
	for i, msg := range req.Messages {
		if got := cachePointCount(msg); got != 0 {
			t.Errorf("message %d has %d cache points, want 0", i, got)
		}
		if len(msg.Content) != 1 || msg.Content[0].Text == nil {
			t.Errorf("message %d content = %+v, want one text block", i, msg.Content)
		}
	}
}

// TestInvokeUnmarshal_CacheControlStillWorksWithoutAStringMessage is the
// control: the standard path was never broken, and must stay that way.
func TestInvokeUnmarshal_CacheControlStillWorksWithoutAStringMessage(t *testing.T) {
	req := unmarshalInvoke(t, `{
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "hello", "cache_control": {"type": "ephemeral"}}
			]}
		]
	}`)

	if got := cachePointCount(req.Messages[0]); got != 1 {
		t.Errorf("cache points = %d, want 1", got)
	}
}

// TestInvokeUnmarshal_EveryBreakpointSurvivesAStringMessage covers the shape a
// rolling breakpoint actually produces: several marked turns, with strings
// interleaved. Losing any one of them costs the cache.
func TestInvokeUnmarshal_EveryBreakpointSurvivesAStringMessage(t *testing.T) {
	req := unmarshalInvoke(t, `{
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "first", "cache_control": {"type": "ephemeral"}}
			]},
			{"role": "assistant", "content": "a plain string in the middle"},
			{"role": "user", "content": [
				{"type": "text", "text": "second", "cache_control": {"type": "ephemeral"}}
			]}
		]
	}`)

	if len(req.Messages) != 3 {
		t.Fatalf("parsed %d messages, want 3", len(req.Messages))
	}
	for _, i := range []int{0, 2} {
		if got := cachePointCount(req.Messages[i]); got != 1 {
			t.Errorf("cache points on message %d = %d, want 1", i, got)
		}
	}
}

// TestInvokeUnmarshal_ToolResultCacheControlSurvivesAStringMessage pins the
// nested case, since injectContentBlockCachePoints recurses into tool results
// and the fallback path skipped that recursion along with everything else.
func TestInvokeUnmarshal_ToolResultCacheControlSurvivesAStringMessage(t *testing.T) {
	req := unmarshalInvoke(t, `{
		"messages": [
			{"role": "user", "content": "a plain string that diverts the parse"},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call_1", "content": [
					{"type": "text", "text": "tool output", "cache_control": {"type": "ephemeral"}}
				]}
			]}
		]
	}`)

	if len(req.Messages) != 2 {
		t.Fatalf("parsed %d messages, want 2", len(req.Messages))
	}
	blocks := req.Messages[1].Content
	if len(blocks) == 0 || blocks[0].ToolResult == nil {
		t.Fatalf("second message lost its tool_result: %+v", blocks)
	}
	found := 0
	for _, inner := range blocks[0].ToolResult.Content {
		if inner.CachePoint != nil {
			found++
		}
	}
	if found != 1 {
		t.Errorf("cache points inside the tool result = %d, want 1", found)
	}
}
