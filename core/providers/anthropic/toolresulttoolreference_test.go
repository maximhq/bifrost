package anthropic

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Client-side tool search (Claude Code's ToolSearch with ENABLE_TOOL_SEARCH)
// answers with a plain tool_result whose content is tool_reference blocks:
//
//	{"type":"tool_result","tool_use_id":"...","content":[{"type":"tool_reference","tool_name":"get_weather"}]}
//
// Anthropic expands each reference into the deferred tool's definition. Both
// anthropic->responses converters used to drop tool_reference, so the request
// reached the provider with an empty tool_result and the model never saw the
// tool it had just loaded. These tests pin the block surviving the round trip.
func toolResultWithToolReference() []AnthropicContentBlock {
	return []AnthropicContentBlock{
		{
			Type:      AnthropicContentBlockTypeToolResult,
			ToolUseID: schemas.Ptr("toolu_test_1"),
			Content: &AnthropicContent{
				ContentBlocks: []AnthropicContentBlock{
					{
						Type:     AnthropicContentBlockTypeToolReference,
						ToolName: schemas.Ptr("get_weather"),
					},
				},
			},
		},
	}
}

func assertToolReferenceRoundTrips(t *testing.T, msgs []schemas.ResponsesMessage) {
	t.Helper()

	var out *schemas.ResponsesMessage
	for i := range msgs {
		if msgs[i].Type != nil && *msgs[i].Type == schemas.ResponsesMessageTypeFunctionCallOutput {
			out = &msgs[i]
			break
		}
	}
	if out == nil {
		t.Fatalf("expected a function_call_output message, got %d messages", len(msgs))
	}
	if out.ResponsesToolMessage == nil || out.ResponsesToolMessage.Output == nil {
		t.Fatal("function_call_output has no tool message output")
	}

	blocks := out.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks
	if len(blocks) != 1 {
		t.Fatalf("expected 1 tool output block, got %d (tool_reference dropped)", len(blocks))
	}
	if blocks[0].Type != schemas.ResponsesInputMessageContentBlockTypeToolReference {
		t.Fatalf("expected block type %q, got %q", schemas.ResponsesInputMessageContentBlockTypeToolReference, blocks[0].Type)
	}
	if blocks[0].ToolName == nil || *blocks[0].ToolName != "get_weather" {
		t.Fatalf("expected tool_name get_weather, got %v", blocks[0].ToolName)
	}

	back := convertBifrostFunctionCallOutputToAnthropicToolResultBlock(out)
	if back == nil || back.Content == nil || len(back.Content.ContentBlocks) != 1 {
		t.Fatalf("tool_result lost its content on the way back to Anthropic: %+v", back)
	}
	ref := back.Content.ContentBlocks[0]
	if ref.Type != AnthropicContentBlockTypeToolReference {
		t.Fatalf("expected Anthropic block type %q, got %q", AnthropicContentBlockTypeToolReference, ref.Type)
	}
	if ref.ToolName == nil || *ref.ToolName != "get_weather" {
		t.Fatalf("expected Anthropic tool_name get_weather, got %v", ref.ToolName)
	}
}

func TestToolResultToolReferenceSurvivesGroupedConversion(t *testing.T) {
	role := schemas.ResponsesInputMessageRoleUser
	msgs := convertAnthropicContentBlocksToResponsesMessagesGrouped(toolResultWithToolReference(), &role, false)
	assertToolReferenceRoundTrips(t, msgs)
}

func TestToolResultToolReferenceSurvivesConversion(t *testing.T) {
	role := schemas.ResponsesInputMessageRoleUser
	msgs := convertAnthropicContentBlocksToResponsesMessages(nil, toolResultWithToolReference(), &role, false, "")
	assertToolReferenceRoundTrips(t, msgs)
}
