package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestConvertBifrostMessages_OrphanToolResultBecomesUserText verifies that a
// function_call_output whose call_id has no matching function_call (e.g. the
// OpenAI Responses previous_response_id pattern) is converted to a user text
// message instead of an invalid tool_result block, which Anthropic rejects with:
// "Each tool_result block must have a corresponding tool_use block in the
// previous message."
func TestConvertBifrostMessages_OrphanToolResultBecomesUserText(t *testing.T) {
	t.Parallel()

	const model = "claude-3-5-sonnet-20241022"
	provider := schemas.Anthropic
	ptr := func(s string) *string { return &s }

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	functionCall := func(callID, name, args string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    ptr(callID),
				Name:      ptr(name),
				Arguments: ptr(args),
			},
		}
	}
	functionOutput := func(callID, output string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: ptr(callID),
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: ptr(output),
				},
			},
		}
	}

	hasToolResult := func(msgs []AnthropicMessage, toolUseID string) bool {
		for _, m := range msgs {
			for _, b := range m.Content.ContentBlocks {
				if b.Type == AnthropicContentBlockTypeToolResult && b.ToolUseID != nil && *b.ToolUseID == toolUseID {
					return true
				}
			}
		}
		return false
	}
	hasUserText := func(msgs []AnthropicMessage, substr string) bool {
		for _, m := range msgs {
			if m.Role != AnthropicMessageRoleUser {
				continue
			}
			for _, b := range m.Content.ContentBlocks {
				if b.Type == AnthropicContentBlockTypeText && b.Text != nil && strings.Contains(*b.Text, substr) {
					return true
				}
			}
		}
		return false
	}
	hasToolUse := func(msgs []AnthropicMessage, toolUseID string) bool {
		for _, m := range msgs {
			for _, b := range m.Content.ContentBlocks {
				if b.Type == AnthropicContentBlockTypeToolUse && b.ID != nil && *b.ID == toolUseID {
					return true
				}
			}
		}
		return false
	}

	// (a) Orphan only: a function_call_output with no preceding function_call.
	t.Run("orphan_only", func(t *testing.T) {
		msgs, err := ConvertBifrostMessagesToAnthropicMessages(ctx,
			[]schemas.ResponsesMessage{functionOutput("toolu_orphan", "Sunny, 72F")},
			true, provider, model)
		if err != nil {
			t.Fatalf("ConvertBifrostMessagesToAnthropicMessages() error = %v", err)
		}

		if len(msgs) != 1 {
			t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
		}
		if msgs[0].Role != AnthropicMessageRoleUser {
			t.Fatalf("expected user message, got %s", msgs[0].Role)
		}
		if len(msgs[0].Content.ContentBlocks) == 0 ||
			msgs[0].Content.ContentBlocks[0].Type != AnthropicContentBlockTypeText {
			t.Fatalf("expected first content block to be text, got %+v", msgs[0].Content.ContentBlocks)
		}
		if !hasUserText(msgs, "Sunny, 72F") {
			t.Fatal("expected orphan output text to be preserved as user text")
		}
		if hasToolResult(msgs, "toolu_orphan") {
			t.Fatal("expected NO tool_result block for an orphaned output")
		}
	})

	// (b) Matched pair still produces tool_use + tool_result (no regression).
	t.Run("matched_pair", func(t *testing.T) {
		msgs, err := ConvertBifrostMessagesToAnthropicMessages(ctx,
			[]schemas.ResponsesMessage{
				functionCall("call_match", "get_weather", `{"location":"SF"}`),
				functionOutput("call_match", "Sunny, 72F"),
			},
			true, provider, model)
		if err != nil {
			t.Fatalf("ConvertBifrostMessagesToAnthropicMessages() error = %v", err)
		}

		if !hasToolUse(msgs, "call_match") {
			t.Fatal("expected a tool_use block for the matched call")
		}
		if !hasToolResult(msgs, "call_match") {
			t.Fatal("expected a matched tool_result block (regression)")
		}
	})

	// (c) Mixed: matched output stays a tool_result, orphan becomes user text.
	t.Run("mixed", func(t *testing.T) {
		msgs, err := ConvertBifrostMessagesToAnthropicMessages(ctx,
			[]schemas.ResponsesMessage{
				functionCall("call_match", "get_weather", `{"location":"SF"}`),
				functionOutput("call_match", "Sunny, 72F"),
				functionOutput("toolu_orphan", "Orphaned result"),
			},
			true, provider, model)
		if err != nil {
			t.Fatalf("ConvertBifrostMessagesToAnthropicMessages() error = %v", err)
		}

		if !hasToolResult(msgs, "call_match") {
			t.Fatal("expected matched output to remain a tool_result")
		}
		if hasToolResult(msgs, "toolu_orphan") {
			t.Fatal("orphan must not be emitted as a tool_result")
		}
		if !hasUserText(msgs, "Orphaned result") {
			t.Fatal("expected orphan output to become user text")
		}
	})
}
