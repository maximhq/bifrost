package integrations

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestMustConvertInPassthrough pins the passthrough routing decision that fixes
// the Claude Code advisor/server-tool streaming bug: server tools (advisor,
// web_search, web_fetch, code_execution) expand one Responses item into several
// Anthropic content blocks with re-numbered indices, so their frames — and every
// output_item.added (to keep the converter's block counter in lockstep) — must be
// rendered by the converter instead of forwarded raw. Computer, plain messages,
// and function/mcp tool calls stream one block each and stay on the raw path.
//
// core/providers/anthropic passthroughstream_test.go mirrors this rule for its
// end-to-end index-consistency test; keep the two in sync.
func TestMustConvertInPassthrough(t *testing.T) {
	itemDone := func(mt schemas.ResponsesMessageType) *schemas.BifrostResponsesStreamResponse {
		return &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemDone,
			Item: &schemas.ResponsesMessage{Type: &mt},
		}
	}
	typed := func(rt schemas.ResponsesStreamResponseType) *schemas.BifrostResponsesStreamResponse {
		return &schemas.BifrostResponsesStreamResponse{Type: rt}
	}

	cases := []struct {
		name string
		resp *schemas.BifrostResponsesStreamResponse
		want bool
	}{
		// output_item.added always converts (keeps the block counter in lockstep).
		{"added_message", &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
			Item: &schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage)},
		}, true},
		{"added_advisor", &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
			Item: &schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeAdvisorCall)},
		}, true},
		{"added_nil_item", typed(schemas.ResponsesStreamResponseTypeOutputItemAdded), true},

		// output_item.done: only result-block-synthesizing server tools convert.
		{"done_advisor", itemDone(schemas.ResponsesMessageTypeAdvisorCall), true},
		{"done_web_search", itemDone(schemas.ResponsesMessageTypeWebSearchCall), true},
		{"done_web_fetch", itemDone(schemas.ResponsesMessageTypeWebFetchCall), true},
		{"done_code_interpreter", itemDone(schemas.ResponsesMessageTypeCodeInterpreterCall), true},
		{"done_computer", itemDone(schemas.ResponsesMessageTypeComputerCall), false},
		{"done_message", itemDone(schemas.ResponsesMessageTypeMessage), false},
		{"done_function_call", itemDone(schemas.ResponsesMessageTypeFunctionCall), false},
		{"done_mcp_call", itemDone(schemas.ResponsesMessageTypeMCPCall), false},
		{"done_nil_item", typed(schemas.ResponsesStreamResponseTypeOutputItemDone), false},

		// Server-tool lifecycle events convert (they collapse to nothing, dropping
		// the duplicate raw content_block frame they would otherwise carry).
		{"web_search_in_progress", typed(schemas.ResponsesStreamResponseTypeWebSearchCallInProgress), true},
		{"web_search_completed", typed(schemas.ResponsesStreamResponseTypeWebSearchCallCompleted), true},
		{"web_fetch_completed", typed(schemas.ResponsesStreamResponseTypeWebFetchCallCompleted), true},
		{"code_interpreter_code_done", typed(schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDone), true},
		{"code_interpreter_completed", typed(schemas.ResponsesStreamResponseTypeCodeInterpreterCallCompleted), true},

		// Everything else stays on the raw passthrough path.
		{"text_delta", typed(schemas.ResponsesStreamResponseTypeOutputTextDelta), false},
		{"function_args_delta", typed(schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta), false},
		{"content_part_added", typed(schemas.ResponsesStreamResponseTypeContentPartAdded), false},
		{"created", typed(schemas.ResponsesStreamResponseTypeCreated), false},
		{"completed", typed(schemas.ResponsesStreamResponseTypeCompleted), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustConvertInPassthrough(tc.resp); got != tc.want {
				t.Errorf("mustConvertInPassthrough(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
