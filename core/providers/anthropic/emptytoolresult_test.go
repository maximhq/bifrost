package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestConvertToolResultWithEmptyContent verifies that an Anthropic tool_result
// block with an empty content array converts to a function_call_output whose
// Output serializes cleanly. An all-nil output struct fails MarshalJSON and
// poisons every enclosing structure (conversation histories, log rows).
func TestConvertToolResultWithEmptyContent(t *testing.T) {
	role := schemas.ResponsesInputMessageRoleUser
	blocks := []AnthropicContentBlock{
		{
			Type:      AnthropicContentBlockTypeToolResult,
			ToolUseID: schemas.Ptr("toolu_empty"),
			Content:   &AnthropicContent{ContentBlocks: []AnthropicContentBlock{}},
		},
	}

	msgs := convertAnthropicContentBlocksToResponsesMessagesGrouped(blocks, &role, false)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 converted message, got %d", len(msgs))
	}
	out := msgs[0].ResponsesToolMessage.Output
	if out == nil {
		t.Fatal("expected non-nil tool message output")
	}
	if _, err := schemas.MarshalSorted(out); err != nil {
		t.Fatalf("converted empty tool_result output must marshal, got: %v", err)
	}
	if _, err := schemas.MarshalSorted(msgs); err != nil {
		t.Fatalf("converted messages must marshal as a slice, got: %v", err)
	}
}

// TestConvertToolResultWithUnsupportedBlocks verifies tool_result content made
// solely of block types the converter does not map (e.g. document) still
// yields a serializable output.
func TestConvertToolResultWithUnsupportedBlocks(t *testing.T) {
	role := schemas.ResponsesInputMessageRoleUser
	blocks := []AnthropicContentBlock{
		{
			Type:      AnthropicContentBlockTypeToolResult,
			ToolUseID: schemas.Ptr("toolu_doc"),
			Content: &AnthropicContent{ContentBlocks: []AnthropicContentBlock{
				{Type: AnthropicContentBlockTypeDocument},
			}},
		},
	}

	msgs := convertAnthropicContentBlocksToResponsesMessagesGrouped(blocks, &role, false)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 converted message, got %d", len(msgs))
	}
	if _, err := schemas.MarshalSorted(msgs); err != nil {
		t.Fatalf("converted messages must marshal, got: %v", err)
	}
}
