package gemini_test

import (
	"strings"
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

// TestToBifrostChatResponse_InlineDataImage pins that a unary response whose only
// part is an image InlineData blob yields an image_url content block carrying the
// image as a data URL instead of an empty message.
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

// TestToBifrostChatResponse_InlineDataAudio pins that a unary response whose only
// part is an audio InlineData blob yields an input_audio content block with the raw
// base64 payload and the format derived from the MIME type.
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

// TestToBifrostChatResponse_InlineDataWithPrecedingText pins that a caption beside
// the image (observed in live traffic as text parts preceding a solo InlineData part)
// keeps both the text block and the image block, in order, rather than collapsing to a
// string or dropping either.
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

// TestToBifrostChatCompletionStream_InlineDataImage pins that a streamed chunk whose
// only part is an image InlineData blob converts to exactly one delta whose Content
// carries the image as a data URL, instead of being silently skipped.
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
	chunks, bifrostErr, isLast := response.ToBifrostChatCompletionStream(state)
	require.Nil(t, bifrostErr)
	require.Len(t, chunks, 1, "the chunk carrying the image must not be silently skipped")
	assert.False(t, isLast)

	require.Len(t, chunks[0].Choices, 1)
	delta := chunks[0].Choices[0].ChatStreamResponseChoice.Delta
	require.NotNil(t, delta)
	require.NotNil(t, delta.Content, "the image must be recoverable from the streamed delta")
	assert.Equal(t, "data:image/png;base64,c3RyZWFtaW1hZ2U=", *delta.Content)
}

// TestToBifrostChatCompletionStream_InlineDataAudio pins that a streamed chunk whose
// only part is an audio InlineData blob converts to one delta carrying the payload in
// delta.Audio, and that the has-content guard does not skip an audio-only chunk.
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
	chunks, bifrostErr, isLast := response.ToBifrostChatCompletionStream(state)
	require.Nil(t, bifrostErr)
	require.Len(t, chunks, 1, "a chunk carrying only inline audio must not be skipped by the has-content guard")
	assert.False(t, isLast)

	delta := chunks[0].Choices[0].ChatStreamResponseChoice.Delta
	require.NotNil(t, delta)
	require.NotNil(t, delta.Audio, "the audio must be recoverable from the streamed delta")
	assert.Equal(t, "c3RyZWFtYXVkaW8=", delta.Audio.Data)
	assert.Nil(t, delta.Content, "audio travels in its own field, not in Content")
}

// TestToBifrostChatCompletionStream_SplitsMixedInlineMedia pins that a single Gemini
// chunk whose parts mix text with inline media is emitted as separate deltas in the
// original part order, one per inline-media part, so an image data URL is never fused
// into surrounding text. Consecutive text parts still share one delta, and the finish
// reason and usage land only on the final delta.
func TestToBifrostChatCompletionStream_SplitsMixedInlineMedia(t *testing.T) {
	const imageDataURL = "data:image/png;base64,bWl4ZWRpbWFnZQ=="
	image := &gemini.Part{InlineData: &gemini.Blob{MIMEType: "image/png", Data: "bWl4ZWRpbWFnZQ=="}}
	audio := &gemini.Part{InlineData: &gemini.Blob{MIMEType: "audio/wav", Data: "bWl4ZWRhdWRpbw=="}}

	type wantDelta struct {
		text      string
		audioData string
	}

	tests := []struct {
		name  string
		parts []*gemini.Part
		want  []wantDelta
	}{
		{
			name:  "text then image",
			parts: []*gemini.Part{{Text: "Here is your image:"}, image},
			want:  []wantDelta{{text: "Here is your image:"}, {text: imageDataURL}},
		},
		{
			name:  "image then text",
			parts: []*gemini.Part{image, {Text: "Done."}},
			want:  []wantDelta{{text: imageDataURL}, {text: "Done."}},
		},
		{
			name:  "text image text",
			parts: []*gemini.Part{{Text: "Before "}, image, {Text: " after"}},
			want:  []wantDelta{{text: "Before "}, {text: imageDataURL}, {text: " after"}},
		},
		{
			name:  "text then audio",
			parts: []*gemini.Part{{Text: "Listen:"}, audio},
			want:  []wantDelta{{text: "Listen:"}, {audioData: "bWl4ZWRhdWRpbw=="}},
		},
		{
			name:  "two text parts stay in one delta",
			parts: []*gemini.Part{{Text: "Hello, "}, {Text: "world"}},
			want:  []wantDelta{{text: "Hello, world"}},
		},
		{
			name:  "image alone needs no split",
			parts: []*gemini.Part{image},
			want:  []wantDelta{{text: imageDataURL}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &gemini.GenerateContentResponse{
				ResponseID:   "mixed-stream-test",
				ModelVersion: "gemini-2.5-flash-image",
				Candidates: []*gemini.Candidate{
					{
						FinishReason: gemini.FinishReasonStop,
						Content: &gemini.Content{
							Role:  string(gemini.RoleModel),
							Parts: tt.parts,
						},
					},
				},
				UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{
					CandidatesTokenCount: 1297,
				},
			}

			chunks, bifrostErr, isLast := response.ToBifrostChatCompletionStream(gemini.NewGeminiStreamState())
			require.Nil(t, bifrostErr)
			assert.True(t, isLast, "a finish reason with usage closes the stream")
			require.Len(t, chunks, len(tt.want), "each inline-media part must be its own delta")

			for i, want := range tt.want {
				require.Len(t, chunks[i].Choices, 1)
				choice := chunks[i].Choices[0]
				delta := choice.ChatStreamResponseChoice.Delta
				require.NotNil(t, delta)

				if want.audioData != "" {
					require.NotNil(t, delta.Audio, "delta %d must carry the audio", i)
					assert.Equal(t, want.audioData, delta.Audio.Data)
					assert.Nil(t, delta.Content, "delta %d must not put audio into Content", i)
				} else {
					require.NotNil(t, delta.Content, "delta %d must carry content", i)
					assert.Equal(t, want.text, *delta.Content)
					if strings.HasPrefix(*delta.Content, "data:image/") {
						assert.Equal(t, imageDataURL, *delta.Content,
							"an image delta must hold the data URL alone, with no text fused around it")
					} else {
						assert.NotContains(t, *delta.Content, "data:image/",
							"a text delta must not have image bytes appended to it")
					}
				}

				if i == len(tt.want)-1 {
					require.NotNil(t, choice.FinishReason, "the final delta carries the finish reason")
					assert.Equal(t, "stop", *choice.FinishReason)
					assert.NotNil(t, chunks[i].Usage, "the final delta carries usage")
				} else {
					assert.Nil(t, choice.FinishReason, "only the final delta may carry the finish reason")
					assert.Nil(t, chunks[i].Usage, "only the final delta may carry usage")
				}
			}
		})
	}
}
