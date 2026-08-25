package bedrock

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToolResultStatusFromIsError verifies that a tool message carrying
// IsError converts to a Converse toolResult with status "error", and that
// non-error results keep the "success" default. Before IsError existed on
// ChatToolMessage the status was hard-coded to "success", so failed tool
// calls replayed through Bedrock looked successful to the model.
func TestToolResultStatusFromIsError(t *testing.T) {
	msgs := []schemas.ChatMessage{
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
	}

	converted, err := convertToolMessages(context.Background(), msgs)
	if err != nil {
		t.Fatalf("convert tool messages: %v", err)
	}

	var results []*BedrockToolResult
	for _, block := range converted.Content {
		if block.ToolResult != nil {
			results = append(results, block.ToolResult)
		}
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 toolResult blocks, got %d", len(results))
	}

	if results[0].ToolUseID != "toolu_failed" {
		t.Fatalf("expected first toolResult for toolu_failed, got %s", results[0].ToolUseID)
	}
	if results[0].Status == nil || *results[0].Status != "error" {
		t.Fatalf("failed tool call must map to status \"error\", got %v", results[0].Status)
	}
	if results[1].Status == nil || *results[1].Status != "success" {
		t.Fatalf("non-error tool call must keep status \"success\", got %v", results[1].Status)
	}
}

func TestToolResultWithoutContentStillEmitsResult(t *testing.T) {
	converted, err := convertToolMessages(context.Background(), []schemas.ChatMessage{
		{
			Role:            schemas.ChatMessageRoleTool,
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_void")},
		},
	})
	if err != nil {
		t.Fatalf("convert content-less tool message: %v", err)
	}
	if len(converted.Content) != 1 || converted.Content[0].ToolResult == nil {
		t.Fatalf("content-less tool message must emit one toolResult, got %#v", converted.Content)
	}
	result := converted.Content[0].ToolResult
	if result.ToolUseID != "toolu_void" {
		t.Fatalf("expected toolu_void, got %q", result.ToolUseID)
	}
	if result.Content != nil {
		t.Fatalf("expected omitted tool-result content, got %#v", result.Content)
	}
}

func TestMalformedToolMessagesReturnErrors(t *testing.T) {
	tests := []struct {
		name    string
		message schemas.ChatMessage
		want    string
	}{
		{
			name:    "missing tool message",
			message: schemas.ChatMessage{Role: schemas.ChatMessageRoleTool},
			want:    "missing required ChatToolMessage",
		},
		{
			name: "missing tool call id",
			message: schemas.ChatMessage{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{},
			},
			want: "missing required ToolCallID",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := convertToolMessages(context.Background(), []schemas.ChatMessage{tc.message})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}
