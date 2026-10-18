package bedrock

import (
	"context"
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

	converted, err := convertToolMessages(context.Background(), "anthropic.claude-sonnet-4-5-20250929-v1:0", msgs)
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

// TestToolResultEmptyKeyChatSurface verifies that a string tool result whose
// JSON carries an empty-string object key converts to a text block on the
// chat-completions surface. Converse rejects such a document in the json
// field ("The format of the value at ...toolResult.content.0.json is
// invalid"), and convertToolMessages now shares tryParseJSONIntoContentBlock
// with the Responses path, so both surfaces get the same fallback.
func TestToolResultEmptyKeyChatSurface(t *testing.T) {
	payload := `{"success":{"fullSubtreeExtensionCounts":{"":2,".md":1}}}`
	msgs := []schemas.ChatMessage{
		{
			Role:            schemas.ChatMessageRoleTool,
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_emptykey")},
			Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(payload)},
		},
	}

	converted, err := convertToolMessages(context.Background(), "anthropic.claude-sonnet-4-5-20250929-v1:0", msgs)
	if err != nil {
		t.Fatalf("convert tool messages: %v", err)
	}
	if len(converted.Content) != 1 || converted.Content[0].ToolResult == nil {
		t.Fatalf("expected a single toolResult block, got %#v", converted.Content)
	}
	content := converted.Content[0].ToolResult.Content
	if len(content) != 1 {
		t.Fatalf("expected 1 tool result content block, got %d", len(content))
	}
	if content[0].JSON != nil {
		t.Fatalf("empty-key payload must not be sent as a json block, got %s", string(content[0].JSON))
	}
	if content[0].Text == nil || *content[0].Text != payload {
		t.Fatalf("expected text fallback carrying the original payload, got %v", content[0].Text)
	}
}

// TestToolResultDuplicateKeyChatSurface verifies that a string tool result whose
// JSON repeats a key within one object converts to a text block on the
// chat-completions surface. Converse rejects such a document in the json field
// with the same "...toolResult.content.0.json is invalid" error as an empty key,
// but json.Compact accepts duplicates, so the shared helper must catch them.
func TestToolResultDuplicateKeyChatSurface(t *testing.T) {
	payload := `{"result":{"id":1,"id":2}}`
	msgs := []schemas.ChatMessage{
		{
			Role:            schemas.ChatMessageRoleTool,
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_dupkey")},
			Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(payload)},
		},
	}

	converted, err := convertToolMessages(context.Background(), "anthropic.claude-sonnet-4-5-20250929-v1:0", msgs)
	if err != nil {
		t.Fatalf("convert tool messages: %v", err)
	}
	if len(converted.Content) != 1 || converted.Content[0].ToolResult == nil {
		t.Fatalf("expected a single toolResult block, got %#v", converted.Content)
	}
	content := converted.Content[0].ToolResult.Content
	if len(content) != 1 {
		t.Fatalf("expected 1 tool result content block, got %d", len(content))
	}
	if content[0].JSON != nil {
		t.Fatalf("duplicate-key payload must not be sent as a json block, got %s", string(content[0].JSON))
	}
	if content[0].Text == nil || *content[0].Text != payload {
		t.Fatalf("expected text fallback carrying the original payload, got %v", content[0].Text)
	}
}

// BenchmarkTryParseJSONIntoContentBlock measures the per-tool-result cost of
// classifying a string tool result, including the Converse-unsafe key scan.
func BenchmarkTryParseJSONIntoContentBlock(b *testing.B) {
	payloads := map[string]string{
		"SmallObject":  `{"status":"ok","count":3,"path":"/repo/main.go"}`,
		"NestedObject": `{"success":{"files":[{"name":"a.go","size":120,"mode":"0644"},{"name":"b.go","size":98,"mode":"0644"}],"fullSubtreeExtensionCounts":{".go":2,".md":1},"numFiles":3}}`,
		"PlainText":    `command exited with code 0`,
	}
	for name, payload := range payloads {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = tryParseJSONIntoContentBlock(payload)
			}
		})
	}
}
