package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestConvertBifrostFunctionCallToAnthropicToolUse_Input verifies that the
// tool_use block always carries an "input" object, defaulting to "{}" when the
// function call has nil or empty arguments (tools that take no arguments).
func TestConvertBifrostFunctionCallToAnthropicToolUse_Input(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	tests := []struct {
		name      string
		arguments *string
		wantInput string
	}{
		{name: "nil arguments", arguments: nil, wantInput: "{}"},
		{name: "empty arguments", arguments: schemas.Ptr(""), wantInput: "{}"},
		{name: "populated arguments", arguments: schemas.Ptr(`{"foo":"bar"}`), wantInput: `{"foo":"bar"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			msg := &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("toolu_fn_test"),
					Name:      schemas.Ptr("get_workspace_id"),
					Arguments: tt.arguments,
				},
			}

			block := convertBifrostFunctionCallToAnthropicToolUse(ctx, msg)
			if block == nil {
				t.Fatal("expected non-nil tool_use block")
			}
			if block.Type != AnthropicContentBlockTypeToolUse {
				t.Errorf("block.Type = %v, want %v", block.Type, AnthropicContentBlockTypeToolUse)
			}
			if block.Input == nil {
				t.Fatal("expected non-nil Input")
			}
			if string(block.Input) != tt.wantInput {
				t.Errorf("Input = %s, want %s", block.Input, tt.wantInput)
			}
		})
	}
}

// TestConvertBifrostMCPCallToAnthropicToolUse_Input verifies that the
// mcp_tool_use block always carries an "input" object, defaulting to "{}" when
// the MCP call has nil or empty arguments.
func TestConvertBifrostMCPCallToAnthropicToolUse_Input(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments *string
		wantInput string
	}{
		{name: "nil arguments", arguments: nil, wantInput: "{}"},
		{name: "empty arguments", arguments: schemas.Ptr(""), wantInput: "{}"},
		{name: "populated arguments", arguments: schemas.Ptr(`{"foo":"bar"}`), wantInput: `{"foo":"bar"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			msg := &schemas.ResponsesMessage{
				ID:   schemas.Ptr("mcp_call_test"),
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      schemas.Ptr("maximsse-get-maxim-workspace-id"),
					Arguments: tt.arguments,
					ResponsesMCPToolCall: &schemas.ResponsesMCPToolCall{
						ServerLabel: "maximsse",
					},
				},
			}

			block := convertBifrostMCPCallToAnthropicToolUse(msg)
			if block == nil {
				t.Fatal("expected non-nil mcp_tool_use block")
			}
			if block.Type != AnthropicContentBlockTypeMCPToolUse {
				t.Errorf("block.Type = %v, want %v", block.Type, AnthropicContentBlockTypeMCPToolUse)
			}
			if block.Input == nil {
				t.Fatal("expected non-nil Input")
			}
			if string(block.Input) != tt.wantInput {
				t.Errorf("Input = %s, want %s", block.Input, tt.wantInput)
			}
		})
	}
}

// TestConvertBifrostMCPApprovalToAnthropicToolUse_Input verifies that the
// mcp_tool_use block produced for an MCP approval request always carries an
// "input" object, defaulting to "{}" when arguments are nil or empty.
func TestConvertBifrostMCPApprovalToAnthropicToolUse_Input(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments *string
		wantInput string
	}{
		{name: "nil arguments", arguments: nil, wantInput: "{}"},
		{name: "empty arguments", arguments: schemas.Ptr(""), wantInput: "{}"},
		{name: "populated arguments", arguments: schemas.Ptr(`{"foo":"bar"}`), wantInput: `{"foo":"bar"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			msg := &schemas.ResponsesMessage{
				ID:   schemas.Ptr("mcp_approval_test"),
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPApprovalRequest),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      schemas.Ptr("maximsse-get-maxim-workspace-id"),
					Arguments: tt.arguments,
					ResponsesMCPToolCall: &schemas.ResponsesMCPToolCall{
						ServerLabel: "maximsse",
					},
				},
			}

			block := convertBifrostMCPApprovalToAnthropicToolUse(msg)
			if block == nil {
				t.Fatal("expected non-nil mcp_tool_use block")
			}
			if block.Type != AnthropicContentBlockTypeMCPToolUse {
				t.Errorf("block.Type = %v, want %v", block.Type, AnthropicContentBlockTypeMCPToolUse)
			}
			if block.Input == nil {
				t.Fatal("expected non-nil Input")
			}
			if string(block.Input) != tt.wantInput {
				t.Errorf("Input = %s, want %s", block.Input, tt.wantInput)
			}
		})
	}
}
