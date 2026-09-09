package gemini_test

import (
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Gemini image-generation models (e.g. gemini-2.5-flash-image) return the
// generated image as a Part carrying only InlineData, no text. Bifrost's own
// Responses converter and image converter already preserve this field; the
// Chat Completions converters must too, on both the unary and streaming path.

func TestToBifrostChatResponse_InlineDataImage(t *testing.T) {
	response := &gemini.GenerateContentResponse{
		ResponseID:   "inline-image-test",
		ModelVersion: "gemini-2.5-flash-image",
		Candidates: []*gemini.Candidate{
			{
				FinishReason: gemini.FinishReasonStop,
				Content: &gemini.Content{
					Role: string(gemini.RoleModel),
					Parts: []*gemini.Part{
						{InlineData: &gemini.Blob{MIMEType: "image/png", Data: "cGluZ3BvbmdpbWFnZWJ5dGVz"}},
					},
				},
			},
		},
		UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{
			CandidatesTokenCount: 1290,
		},
	}

	bifrostResp := response.ToBifrostChatResponse()
	require.NotNil(t, bifrostResp)
	require.Len(t, bifrostResp.Choices, 1)

	message := bifrostResp.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, message)
	require.NotNil(t, message.Content)
	require.Len(t, message.Content.ContentBlocks, 1,
		"the generated image must appear as a content block instead of being silently dropped")

	block := message.Content.ContentBlocks[0]
	assert.Equal(t, schemas.ChatContentBlockTypeImage, block.Type)
	require.NotNil(t, block.ImageURLStruct)
	assert.Equal(t, "data:image/png;base64,cGluZ3BvbmdpbWFnZWJ5dGVz", block.ImageURLStruct.URL)
}

func TestToBifrostChatResponse_InlineDataAudio(t *testing.T) {
	response := &gemini.GenerateContentResponse{
		ResponseID:   "inline-audio-test",
		ModelVersion: "gemini-2.5-flash-native-audio",
		Candidates: []*gemini.Candidate{
			{
				FinishReason: gemini.FinishReasonStop,
				Content: &gemini.Content{
					Role: string(gemini.RoleModel),
					Parts: []*gemini.Part{
						{InlineData: &gemini.Blob{MIMEType: "audio/wav", Data: "d2F2Ynl0ZXM="}},
					},
				},
			},
		},
	}

	bifrostResp := response.ToBifrostChatResponse()
	require.NotNil(t, bifrostResp)
	message := bifrostResp.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, message.Content)
	require.Len(t, message.Content.ContentBlocks, 1)

	block := message.Content.ContentBlocks[0]
	assert.Equal(t, schemas.ChatContentBlockTypeInputAudio, block.Type)
	require.NotNil(t, block.InputAudio)
	assert.Equal(t, "d2F2Ynl0ZXM=", block.InputAudio.Data)
	require.NotNil(t, block.InputAudio.Format)
	assert.Equal(t, "wav", *block.InputAudio.Format)
}

// A caption alongside the image (observed in live traffic: separate text parts
// precede a solo InlineData part) must keep both the text and the image, not
// collapse to one or drop the other.
func TestToBifrostChatResponse_InlineDataWithPrecedingText(t *testing.T) {
	response := &gemini.GenerateContentResponse{
		ResponseID:   "inline-image-with-text-test",
		ModelVersion: "gemini-2.5-flash-image",
		Candidates: []*gemini.Candidate{
			{
				FinishReason: gemini.FinishReasonStop,
				Content: &gemini.Content{
					Role: string(gemini.RoleModel),
					Parts: []*gemini.Part{
						{Text: "Here is your image:"},
						{InlineData: &gemini.Blob{MIMEType: "image/png", Data: "aW1hZ2VieXRlcw=="}},
					},
				},
			},
		},
	}

	bifrostResp := response.ToBifrostChatResponse()
	message := bifrostResp.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, message.Content)
	require.Len(t, message.Content.ContentBlocks, 2)
	assert.Equal(t, schemas.ChatContentBlockTypeText, message.Content.ContentBlocks[0].Type)
	assert.Equal(t, schemas.ChatContentBlockTypeImage, message.Content.ContentBlocks[1].Type)
}

func TestToBifrostChatCompletionStream_InlineDataImage(t *testing.T) {
	response := &gemini.GenerateContentResponse{
		ResponseID:   "inline-image-stream-test",
		ModelVersion: "gemini-2.5-flash-image",
		Candidates: []*gemini.Candidate{
			{
				Content: &gemini.Content{
					Role: string(gemini.RoleModel),
					Parts: []*gemini.Part{
						{InlineData: &gemini.Blob{MIMEType: "image/png", Data: "c3RyZWFtaW1hZ2U="}},
					},
				},
			},
		},
	}

	state := gemini.NewGeminiStreamState()
	bifrostResp, bifrostErr, isLast := response.ToBifrostChatCompletionStream(state)
	require.Nil(t, bifrostErr)
	require.NotNil(t, bifrostResp, "the chunk carrying the image must not be silently skipped")
	assert.False(t, isLast)

	require.Len(t, bifrostResp.Choices, 1)
	delta := bifrostResp.Choices[0].ChatStreamResponseChoice.Delta
	require.NotNil(t, delta)
	require.NotNil(t, delta.Content, "the image must be recoverable from the streamed delta")
	assert.Equal(t, "data:image/png;base64,c3RyZWFtaW1hZ2U=", *delta.Content)
}

func TestToBifrostChatCompletionStream_InlineDataAudio(t *testing.T) {
	response := &gemini.GenerateContentResponse{
		ResponseID:   "inline-audio-stream-test",
		ModelVersion: "gemini-2.5-flash-native-audio",
		Candidates: []*gemini.Candidate{
			{
				Content: &gemini.Content{
					Role: string(gemini.RoleModel),
					Parts: []*gemini.Part{
						{InlineData: &gemini.Blob{MIMEType: "audio/wav", Data: "c3RyZWFtYXVkaW8="}},
					},
				},
			},
		},
	}

	state := gemini.NewGeminiStreamState()
	bifrostResp, bifrostErr, isLast := response.ToBifrostChatCompletionStream(state)
	require.Nil(t, bifrostErr)
	require.NotNil(t, bifrostResp, "a chunk carrying only inline audio must not be skipped by the has-content guard")
	assert.False(t, isLast)

	delta := bifrostResp.Choices[0].ChatStreamResponseChoice.Delta
	require.NotNil(t, delta)
	require.NotNil(t, delta.Audio, "the audio must be recoverable from the streamed delta")
	assert.Equal(t, "c3RyZWFtYXVkaW8=", delta.Audio.Data)
}
