package bedrock

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// A Chat Completions conversation that ends in a tool result marked with cache_control (Cursor's
// agent loop) goes through ToResponsesMessages before the Converse request is built. The marker has
// to come out as a cachePoint right after the toolResult; otherwise every agent step is billed in full.
func TestChatToolMessageCacheControlBecomesCachePointAfterToolResult(t *testing.T) {
	callID := "call_1"
	toolName := "read_file"
	question := "read the file"
	output := "file contents"
	chat := []schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &question}},
		{
			Role: schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{{
				ID:       &callID,
				Type:     schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{Name: &toolName, Arguments: "{}"},
			}}},
		},
		{
			Role:            schemas.ChatMessageRoleTool,
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: &callID},
			Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: &output, CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}},
			}},
		},
	}
	var items []schemas.ResponsesMessage
	for i := range chat {
		items = append(items, chat[i].ToResponsesMessages()...)
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), "anthropic.claude-sonnet-5", items, false)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	for _, msg := range messages {
		for i, block := range msg.Content {
			if block.ToolResult == nil {
				continue
			}
			if i+1 >= len(msg.Content) || msg.Content[i+1].CachePoint == nil {
				t.Fatalf("no cachePoint after the toolResult: %+v", msg.Content)
			}
			return
		}
	}
	t.Fatalf("no toolResult in the Converse messages: %+v", messages)
}
