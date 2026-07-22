package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToolResultIsErrorReachesAnthropicWire verifies that a tool message
// carrying IsError converts to an Anthropic tool_result block with is_error
// set. Claude is trained to treat errored tool results differently, so
// dropping the marker silently changes model behavior on replayed histories.
func TestToolResultIsErrorReachesAnthropicWire(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	req := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4-5",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("run the tool")},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							ID:       schemas.Ptr("toolu_failed"),
							Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("run"), Arguments: "{}"},
						},
						{
							ID:       schemas.Ptr("toolu_ok"),
							Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("run"), Arguments: "{}"},
						},
					},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_failed"), IsError: schemas.Ptr(true)},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("command exited with code 1")},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_ok")},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("done")},
			},
		},
	}

	result, err := ToAnthropicChatRequest(ctx, req)
	if err != nil {
		t.Fatalf("convert to Anthropic request: %v", err)
	}

	var toolResults []AnthropicContentBlock
	for _, msg := range result.Messages {
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == AnthropicContentBlockTypeToolResult {
				toolResults = append(toolResults, block)
			}
		}
	}
	if len(toolResults) != 2 {
		t.Fatalf("expected 2 tool_result blocks, got %d", len(toolResults))
	}

	failed := toolResults[0]
	if failed.ToolUseID == nil || *failed.ToolUseID != "toolu_failed" {
		t.Fatalf("expected first tool_result for toolu_failed, got %v", failed.ToolUseID)
	}
	if failed.IsError == nil || !*failed.IsError {
		t.Fatal("tool_result for the failed call must carry is_error: true")
	}

	ok := toolResults[1]
	if ok.IsError != nil {
		t.Fatalf("tool_result without IsError must omit is_error, got %v", *ok.IsError)
	}
}
