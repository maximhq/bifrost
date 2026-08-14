package bedrockmantle

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestApplyExplicitPromptCache_MarksFirstInputText(t *testing.T) {
	text := "hello"
	req := &schemas.BifrostResponsesRequest{
		Model: "openai.gpt-5.6-sol",
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesInputMessageContentBlockTypeText,
					Text: &text,
				}},
			},
		}},
	}

	applyExplicitPromptCache(req)

	require.NotNil(t, req.Params)
	require.NotNil(t, req.Params.PromptCacheOptions)
	require.Equal(t, explicitPromptCacheMode, *req.Params.PromptCacheOptions.Mode)
	require.NotNil(t, req.Input[0].Content.ContentBlocks[0].PromptCacheBreakpoint)
	require.Equal(t, explicitPromptCacheMode, *req.Input[0].Content.ContentBlocks[0].PromptCacheBreakpoint.Mode)
}

func TestApplyExplicitPromptCache_ConvertsContentString(t *testing.T) {
	text := "stable prefix"
	req := &schemas.BifrostResponsesRequest{
		Model: "us-east-1/openai.gpt-5.6-terra",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleDeveloper),
			Content: &schemas.ResponsesMessageContent{ContentStr: &text},
		}},
	}

	applyExplicitPromptCache(req)

	require.Nil(t, req.Input[0].Content.ContentStr)
	require.Len(t, req.Input[0].Content.ContentBlocks, 1)
	require.Equal(t, schemas.ResponsesInputMessageContentBlockTypeText, req.Input[0].Content.ContentBlocks[0].Type)
	require.Equal(t, text, *req.Input[0].Content.ContentBlocks[0].Text)
	require.NotNil(t, req.Input[0].Content.ContentBlocks[0].PromptCacheBreakpoint)
}

func TestApplyExplicitPromptCache_LeavesClientCacheAlone(t *testing.T) {
	text := "hello"
	mode := "implicit"
	req := &schemas.BifrostResponsesRequest{
		Model: "openai.gpt-5.6-sol",
		Params: &schemas.ResponsesParameters{
			PromptCacheOptions: &schemas.PromptCacheOptions{Mode: &mode},
		},
		Input: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesInputMessageContentBlockTypeText,
					Text: &text,
				}},
			},
		}},
	}

	applyExplicitPromptCache(req)

	require.Equal(t, "implicit", *req.Params.PromptCacheOptions.Mode)
	require.Nil(t, req.Input[0].Content.ContentBlocks[0].PromptCacheBreakpoint)
}

func TestApplyExplicitPromptCache_SkipsNonGPT56(t *testing.T) {
	text := "hello"
	req := &schemas.BifrostResponsesRequest{
		Model: "openai.gpt-5.5",
		Input: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesInputMessageContentBlockTypeText,
					Text: &text,
				}},
			},
		}},
	}

	applyExplicitPromptCache(req)

	require.Nil(t, req.Params)
	require.Nil(t, req.Input[0].Content.ContentBlocks[0].PromptCacheBreakpoint)
}

func TestApplyExplicitPromptCache_LeavesExistingBreakpointAlone(t *testing.T) {
	text := "hello"
	mode := explicitPromptCacheMode
	req := &schemas.BifrostResponsesRequest{
		Model: "openai.gpt-5.6-sol",
		Input: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type:                  schemas.ResponsesInputMessageContentBlockTypeText,
					Text:                  &text,
					PromptCacheBreakpoint: &schemas.PromptCacheBreakpoint{Mode: &mode},
				}},
			},
		}},
	}

	applyExplicitPromptCache(req)

	require.Nil(t, req.Params)
	require.Equal(t, explicitPromptCacheMode, *req.Input[0].Content.ContentBlocks[0].PromptCacheBreakpoint.Mode)
}

func TestApplyExplicitPromptCache_DoesNotMutateOriginalBlock(t *testing.T) {
	text := "hello"
	original := schemas.ResponsesMessage{
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeText,
				Text: &text,
			}},
		},
	}
	req := &schemas.BifrostResponsesRequest{
		Model: "openai.gpt-5.6-luna",
		Input: []schemas.ResponsesMessage{original},
	}

	applyExplicitPromptCache(req)

	require.Nil(t, original.Content.ContentBlocks[0].PromptCacheBreakpoint)
	require.NotNil(t, req.Input[0].Content.ContentBlocks[0].PromptCacheBreakpoint)
}
