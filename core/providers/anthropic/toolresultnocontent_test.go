package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Anthropic's Messages API marks `content` optional on a tool_result block --
// only `type` and `tool_use_id` are required
// (https://platform.claude.com/docs/en/api/messages, ToolResultBlockParam).
// A void tool, a tool that returned nothing, and an is_error result with no
// body all legitimately arrive with no `content` key at all.
//
// Both Responses-surface converters used to gate the whole conversion on
// `block.Content != nil`, so those blocks were dropped on the floor rather than
// becoming a function_call_output. Losing the tool result loses the entire user
// turn that carried it, which is what wedges a Bedrock replay: with no user turn
// between them, every assistant turn merges into one (see
// TestAnthropicIngressBedrockReplayKeepsTurnBoundaries in the bedrock package).
//
// The empty-content-array shape is already covered by
// TestConvertToolResultWithEmptyContent; this asserts the absent/null/is_error
// shapes behave identically to it.
func TestConvertToolResultWithoutContentField(t *testing.T) {
	cases := []struct {
		name    string
		block   AnthropicContentBlock
		isError bool
	}{
		{
			name: "content key absent",
			block: AnthropicContentBlock{
				Type:      AnthropicContentBlockTypeToolResult,
				ToolUseID: schemas.Ptr("toolu_void"),
			},
		},
		{
			name: "is_error with no content",
			block: AnthropicContentBlock{
				Type:      AnthropicContentBlockTypeToolResult,
				ToolUseID: schemas.Ptr("toolu_failed"),
				IsError:   schemas.Ptr(true),
			},
			isError: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			role := schemas.ResponsesInputMessageRoleUser
			blocks := []AnthropicContentBlock{tc.block}

			// keepToolsGrouped path (every `bedrock/` request takes this one).
			grouped := convertAnthropicContentBlocksToResponsesMessagesGrouped(blocks, &role, false)
			assertSingleFunctionCallOutput(t, "grouped", grouped, *tc.block.ToolUseID)

			// Default path (native Anthropic, Vertex, Azure ingress).
			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			plain := convertAnthropicContentBlocksToResponsesMessages(ctx, blocks, &role, false, "")
			assertSingleFunctionCallOutput(t, "default", plain, *tc.block.ToolUseID)
		})
	}
}

func assertSingleFunctionCallOutput(t *testing.T, path string, msgs []schemas.ResponsesMessage, wantCallID string) {
	t.Helper()

	if len(msgs) != 1 {
		t.Fatalf("%s path: a content-less tool_result must still convert to one function_call_output, got %d messages", path, len(msgs))
	}
	msg := msgs[0]
	if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeFunctionCallOutput {
		t.Fatalf("%s path: expected type function_call_output, got %v", path, msg.Type)
	}
	if msg.ResponsesToolMessage == nil || msg.ResponsesToolMessage.CallID == nil {
		t.Fatalf("%s path: converted message carries no call id", path)
	}
	if *msg.ResponsesToolMessage.CallID != wantCallID {
		t.Fatalf("%s path: expected call id %q, got %q", path, wantCallID, *msg.ResponsesToolMessage.CallID)
	}
	if msg.ResponsesToolMessage.Output == nil {
		t.Fatalf("%s path: output struct must be non-nil so downstream converters emit a tool result", path)
	}
	if _, err := schemas.MarshalSorted(msgs); err != nil {
		t.Fatalf("%s path: converted messages must marshal, got: %v", path, err)
	}
}

// The grouped converter used to buffer every text block and every tool_use block
// to the end of the turn while emitting thinking blocks in place. For an
// interleaved turn that reordered the stream into
// [reasoning, reasoning, message(text+text), function_call, function_call],
// which relocates the second thinking block in front of the tool call it was
// produced after. Items must come out in wire order, with only *consecutive*
// text blocks coalesced.
func TestGroupedConverterPreservesInterleavedBlockOrder(t *testing.T) {
	role := schemas.ResponsesInputMessageRoleAssistant
	blocks := []AnthropicContentBlock{
		{Type: AnthropicContentBlockTypeThinking, Thinking: schemas.Ptr("t0"), Signature: schemas.Ptr("SIG_0")},
		{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("step 0")},
		{Type: AnthropicContentBlockTypeToolUse, ID: schemas.Ptr("toolu_0"), Name: schemas.Ptr("get_weather"), Input: []byte(`{"city":"sf"}`)},
		{Type: AnthropicContentBlockTypeThinking, Thinking: schemas.Ptr("t1"), Signature: schemas.Ptr("SIG_1")},
		{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("step 1")},
		{Type: AnthropicContentBlockTypeToolUse, ID: schemas.Ptr("toolu_1"), Name: schemas.Ptr("get_weather"), Input: []byte(`{"city":"la"}`)},
	}

	msgs := convertAnthropicContentBlocksToResponsesMessagesGrouped(blocks, &role, true)

	want := []schemas.ResponsesMessageType{
		schemas.ResponsesMessageTypeReasoning,
		schemas.ResponsesMessageTypeMessage,
		schemas.ResponsesMessageTypeFunctionCall,
		schemas.ResponsesMessageTypeReasoning,
		schemas.ResponsesMessageTypeMessage,
		schemas.ResponsesMessageTypeFunctionCall,
	}
	got := make([]schemas.ResponsesMessageType, 0, len(msgs))
	for _, m := range msgs {
		if m.Type == nil {
			got = append(got, "<nil>")
			continue
		}
		got = append(got, *m.Type)
	}
	if len(got) != len(want) {
		t.Fatalf("item order: want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item order: want %v, got %v", want, got)
		}
	}
}
