package bedrock_test

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Bedrock's Converse API rejects a message whose content field is null:
// "Value null at 'messages.N.member.content' ... Member must not be null".
// convertContent/convertContentBlock already skip individual blank text blocks
// (see #2765's fix for the Anthropic-only regression), but nothing filled the
// resulting empty slice back in, and BedrockMessage.Content has no `omitempty` —
// so an assistant turn with no text and no tool calls serializes as literal
// JSON null instead of an omitted or empty field. maximhq/bifrost#2765.

func TestToBedrockChatCompletionRequest_EmptyAssistantMessageGetsPlaceholderContent(t *testing.T) {
	req := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hello")}},
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: new("")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("follow up")}},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, req)
	require.NoError(t, err)

	blocks := result.Messages[1].Content
	require.NotNil(t, blocks, "content must never serialize as null")
	require.NotEmpty(t, blocks, "content must never serialize as an empty array either")
}

func TestToBedrockChatCompletionRequest_EmptyToolCallAssistantMessageGetsPlaceholderContent(t *testing.T) {
	req := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hello")}},
			{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: new("")},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, req)
	require.NoError(t, err)

	blocks := result.Messages[1].Content
	require.NotNil(t, blocks, "content must never serialize as null")
	require.NotEmpty(t, blocks, "content must never serialize as an empty array either")
}
