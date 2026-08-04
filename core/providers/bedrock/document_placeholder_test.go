package bedrock_test

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Bedrock's Converse API rejects a message whose content holds a document block
// but no accompanying text block ("A text block must be included when using
// documents"). These tests pin the placeholder-injection fix mirroring the one
// already in place for the Anthropic-native path.

func TestToBedrockChatCompletionRequest_DocumentOnlyMessageGetsPlaceholderTextBlock(t *testing.T) {
	req := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{
						Type: schemas.ChatContentBlockTypeFile,
						File: &schemas.ChatInputFile{
							FileID:   new("file_abc123"),
							Filename: new("tiny.pdf"),
							FileType: new("application/pdf"),
						},
					},
				}},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, req)
	require.NoError(t, err)

	blocks := result.Messages[0].Content
	require.Len(t, blocks, 2, "expected placeholder text block plus document block")
	require.NotNil(t, blocks[0].Text, "expected leading placeholder text block")
	require.NotEmpty(t, strings.TrimSpace(*blocks[0].Text))
	require.NotNil(t, blocks[1].Document, "expected document block after placeholder")
}

func TestToBedrockChatCompletionRequest_DocumentWithTextDoesNotGetPlaceholder(t *testing.T) {
	req := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{
						Type: schemas.ChatContentBlockTypeText,
						Text: new("Read the attached PDF."),
					},
					{
						Type: schemas.ChatContentBlockTypeFile,
						File: &schemas.ChatInputFile{
							FileID:   new("file_abc123"),
							Filename: new("tiny.pdf"),
							FileType: new("application/pdf"),
						},
					},
				}},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, req)
	require.NoError(t, err)

	blocks := result.Messages[0].Content
	require.Len(t, blocks, 2, "expected no placeholder inserted")
	require.NotNil(t, blocks[0].Text)
	require.Equal(t, "Read the attached PDF.", *blocks[0].Text)
	require.NotNil(t, blocks[1].Document)
}

func TestToBedrockChatCompletionRequest_UserDocumentWithWhitespaceTextGetsPlaceholder(t *testing.T) {
	req := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{
						Type: schemas.ChatContentBlockTypeText,
						Text: new("   "),
					},
					{
						Type: schemas.ChatContentBlockTypeFile,
						File: &schemas.ChatInputFile{
							FileID:   new("file_abc123"),
							Filename: new("tiny.pdf"),
							FileType: new("application/pdf"),
						},
					},
				}},
			},
			{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: new("Understood.")},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result, err := bedrock.ToBedrockChatCompletionRequest(ctx, req)
	require.NoError(t, err)

	blocks := result.Messages[0].Content
	require.Len(t, blocks, 2)
	require.NotNil(t, blocks[0].Text)
	require.NotEmpty(t, strings.TrimSpace(*blocks[0].Text))
	require.NotNil(t, blocks[1].Document)
}
