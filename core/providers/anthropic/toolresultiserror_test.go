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

func TestAnthropicToolResultBuildersPreserveStructuredErrors(t *testing.T) {
	callID := "call_1"
	emptyLegacy := ""
	output := "provider output"

	builders := []struct {
		name  string
		build func(*schemas.ResponsesMessage) *AnthropicContentBlock
	}{
		{name: "function", build: convertBifrostFunctionCallOutputToAnthropicToolResultBlock},
		{name: "computer", build: convertBifrostComputerCallOutputToAnthropicToolResultBlock},
		{name: "mcp", build: convertBifrostMCPCallOutputToAnthropicToolResultBlock},
	}

	for _, builder := range builders {
		t.Run(builder.name+" structured error", func(t *testing.T) {
			block := builder.build(&schemas.ResponsesMessage{ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &callID,
				Error: &schemas.ResponsesToolMessageError{
					ResponsesToolMessageErrorStruct: &schemas.ResponsesToolMessageErrorStruct{},
				},
				Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &output},
			}})
			if block == nil || block.IsError == nil || !*block.IsError {
				t.Fatalf("structured error was not preserved: %#v", block)
			}
			if block.Content == nil || block.Content.ContentStr == nil || *block.Content.ContentStr != output {
				t.Fatalf("existing output was not preserved: %#v", block.Content)
			}
		})

		t.Run(builder.name+" empty structured error fallback", func(t *testing.T) {
			block := builder.build(&schemas.ResponsesMessage{ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &callID,
				Error: &schemas.ResponsesToolMessageError{
					ResponsesToolMessageErrorStruct: &schemas.ResponsesToolMessageErrorStruct{},
				},
			}})
			if block == nil || block.Content == nil || block.Content.ContentStr == nil || *block.Content.ContentStr != "tool call returned an error" {
				t.Fatalf("missing structured error fallback: %#v", block)
			}
		})

		t.Run(builder.name+" empty legacy error", func(t *testing.T) {
			block := builder.build(&schemas.ResponsesMessage{ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &callID,
				Error:  &schemas.ResponsesToolMessageError{ResponsesToolMessageErrorStr: &emptyLegacy},
			}})
			if block == nil {
				t.Fatal("builder returned nil")
			}
			if block.IsError != nil {
				t.Fatalf("empty legacy error changed behavior: %#v", block.IsError)
			}
		})
	}
}
