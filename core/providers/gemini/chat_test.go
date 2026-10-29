package gemini_test

import (
	"math"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

// TestToBifrostChatCompletionStream_ErrorFinishReasonIsNotSplit pins that a chunk
// mixing text and inline media on a candidate whose finish reason is an error (here a
// safety block) is not split into pieces: the converter must return the single error
// response and suppress every part, exactly as it does for an unmixed chunk, instead of
// leaking the text or the image ahead of the content-filter error.
func TestToBifrostChatCompletionStream_ErrorFinishReasonIsNotSplit(t *testing.T) {
	tests := []struct {
		name         string
		finishReason gemini.FinishReason
	}{
		{name: "safety", finishReason: gemini.FinishReasonSafety},
		{name: "image safety", finishReason: gemini.FinishReasonImageSafety},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &gemini.GenerateContentResponse{
				ResponseID:   "blocked-mixed-stream-test",
				ModelVersion: "gemini-2.5-flash-image",
				Candidates: []*gemini.Candidate{
					{
						FinishReason: tt.finishReason,
						Content: &gemini.Content{
							Role: string(gemini.RoleModel),
							Parts: []*gemini.Part{
								{Text: "Here is your image:"},
								{InlineData: &gemini.Blob{MIMEType: "image/png", Data: "YmxvY2tlZA=="}},
							},
						},
					},
				},
				UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{CandidatesTokenCount: 12},
			}

			chunks, bifrostErr, isLast := response.ToBifrostChatCompletionStream(gemini.NewGeminiStreamState())
			require.Nil(t, bifrostErr)
			assert.True(t, isLast)
			require.Len(t, chunks, 1, "a filtered candidate must yield the single error response, not split pieces")
			require.Len(t, chunks[0].Choices, 1)

			choice := chunks[0].Choices[0]
			require.NotNil(t, choice.FinishReason)
			assert.Equal(t, gemini.ConvertGeminiFinishReasonToBifrost(tt.finishReason), *choice.FinishReason)
			if delta := choice.ChatStreamResponseChoice.Delta; delta != nil && delta.Content != nil {
				assert.NotContains(t, *delta.Content, "data:image/", "the blocked image must not leak")
				assert.NotContains(t, *delta.Content, "Here is your image", "the blocked text must not leak")
			}
		})
	}
}

// TestToGeminiChatCompletionRequest_MidConversationSystemInlined pins the Chat Completions path
// to what the Responses path already does (inlineGeminiSystemReminder): a role:"system" message
// that arrives after the conversation has started is inlined at its position as a user turn, not
// hoisted into systemInstruction. Gemini's implicit cache is prefix-based, so a systemInstruction
// that grows by one reminder per turn invalidates the whole cached conversation behind it.
func TestToGeminiChatCompletionRequest_MidConversationSystemInlined(t *testing.T) {
	str := func(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
		return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}}
	}
	const reminder = "<total_tokens>15000000 tokens left</total_tokens>"
	turn1 := []schemas.ChatMessage{
		str(schemas.ChatMessageRoleSystem, "You are Claude Code."),
		str(schemas.ChatMessageRoleUser, "first user turn"),
		str(schemas.ChatMessageRoleSystem, "Available agent types for the Agent tool: claude, Explore, Plan."),
		str(schemas.ChatMessageRoleAssistant, "ok"),
		str(schemas.ChatMessageRoleUser, "second user turn"),
		str(schemas.ChatMessageRoleSystem, reminder),
	}
	turn2 := append(append([]schemas.ChatMessage{}, turn1...),
		str(schemas.ChatMessageRoleAssistant, "done"),
		str(schemas.ChatMessageRoleUser, "third user turn"),
		str(schemas.ChatMessageRoleSystem, reminder),
	)
	convert := func(msgs []schemas.ChatMessage) *gemini.GeminiGenerationRequest {
		req, err := gemini.ToGeminiChatCompletionRequest(&schemas.BifrostContext{}, &schemas.BifrostChatRequest{
			Provider: schemas.Gemini, Model: "gemini-2.5-flash", Input: msgs,
		})
		require.NoError(t, err)
		return req
	}
	r1, r2 := convert(turn1), convert(turn2)

	require.NotNil(t, r1.SystemInstruction)
	require.Len(t, r1.SystemInstruction.Parts, 1, "only the leading system prompt belongs in systemInstruction")
	assert.Equal(t, r1.SystemInstruction, r2.SystemInstruction, "turn N+1 must not grow systemInstruction with the new trailing reminder")

	require.GreaterOrEqual(t, len(r2.Contents), len(r1.Contents))
	assert.Equal(t, r1.Contents, r2.Contents[:len(r1.Contents)], "contents of turn N must be a prefix of turn N+1")

	inline := 0
	for _, c := range r2.Contents {
		for _, p := range c.Parts {
			if strings.Contains(p.Text, "<system-reminder>\n<total_tokens>") {
				assert.Equal(t, "user", c.Role, "inlined reminder must be a user turn")
				inline++
			}
		}
	}
	assert.Equal(t, 2, inline, "both trailing reminders must be inlined in place, none hoisted")
}

func TestToGeminiChatCompletionRequest_NMapsToCandidateCount(t *testing.T) {
	cases := []struct {
		name    string
		n       int
		want    int32
		wantErr string
	}{
		{name: "normal", n: 3, want: 3},
		{name: "maximum int32", n: math.MaxInt32, want: math.MaxInt32},
		{name: "above maximum int32", n: math.MaxInt32 + 1, wantErr: "n must be between 1 and 2147483647"},
		{name: "zero", n: 0, wantErr: "n must be between 1 and 2147483647"},
		{name: "negative", n: -1, wantErr: "n must be between 1 and 2147483647"},
	}

	for _, provider := range []schemas.ModelProvider{schemas.Gemini, schemas.Vertex} {
		for _, tt := range cases {
			t.Run(string(provider)+"/"+tt.name, func(t *testing.T) {
				result, err := gemini.ToGeminiChatCompletionRequest(nil, &schemas.BifrostChatRequest{
					Provider: provider,
					Model:    "gemini-2.5-flash",
					Input: []schemas.ChatMessage{{
						Role:    schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")},
					}},
					Params: &schemas.ChatParameters{N: schemas.Ptr(tt.n)},
				})
				if tt.wantErr != "" {
					require.ErrorContains(t, err, tt.wantErr)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, tt.want, result.GenerationConfig.CandidateCount)
			})
		}
	}
}

// chatToolResultHasRefKey reports whether any object at any depth of raw has a "$ref" key.
// Gemini reads {"$ref": "<displayName>"} inside function_response.response as a pointer to
// a multimodal part, so a caller's JSON Schema / OpenAPI tool output must never reach the
// wire with one (#7694).
func chatToolResultHasRefKey(raw []byte) bool {
	var found bool
	var walk func(v gjson.Result)
	walk = func(v gjson.Result) {
		v.ForEach(func(key, value gjson.Result) bool {
			if v.IsObject() && key.String() == "$ref" {
				found = true
				return false
			}
			if value.IsObject() || value.IsArray() {
				walk(value)
			}
			return !found
		})
	}
	walk(gjson.ParseBytes(raw))
	return found
}

// TestToGeminiChatCompletionRequest_ToolResultRefKeyStaysOpaque pins that a role:"tool"
// message whose content is a JSON object containing a "$ref" key anywhere is sent to
// Gemini as opaque string content ({"content": "<verbatim text>"}), never as a structured
// function_response.response. Gemini reserves "$ref" there for multimodal part references
// and rejects the request with 400 "does not match to a display_name in the
// function_response.parts" otherwise (#7694). Tool output without "$ref" keeps the
// structured fast path.
func TestToGeminiChatCompletionRequest_ToolResultRefKeyStaysOpaque(t *testing.T) {
	const refOutput = `{"schema":{"$ref":"#/components/schemas/SOM_computer_post_response"}}`
	const plainOutput = `{"temperature":22,"condition":"sunny"}`

	build := func(content *schemas.ChatMessageContent) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.Gemini,
			Model:    "gemini-flash-latest",
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Fetch the spec, then reply ok.")},
				},
				{
					Role: schemas.ChatMessageRoleAssistant,
					ChatAssistantMessage: &schemas.ChatAssistantMessage{
						ToolCalls: []schemas.ChatAssistantMessageToolCall{{
							ID:   schemas.Ptr("c1"),
							Type: schemas.Ptr("function"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"command":"cat spec.json"}`,
							},
						}},
					},
				},
				{
					Role:            schemas.ChatMessageRoleTool,
					ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("c1")},
					Content:         content,
				},
			},
		}
	}

	functionResponse := func(t *testing.T, req *schemas.BifrostChatRequest) []byte {
		t.Helper()
		out, err := gemini.ToGeminiChatCompletionRequest(nil, req)
		require.NoError(t, err)
		require.Len(t, out.Contents, 3)
		require.Len(t, out.Contents[2].Parts, 1)
		require.NotNil(t, out.Contents[2].Parts[0].FunctionResponse)
		return out.Contents[2].Parts[0].FunctionResponse.Response
	}

	t.Run("string content with nested $ref is wrapped as opaque text", func(t *testing.T) {
		resp := functionResponse(t, build(&schemas.ChatMessageContent{ContentStr: schemas.Ptr(refOutput)}))
		assert.False(t, chatToolResultHasRefKey(resp), "function_response.response must not carry a $ref key: %s", resp)
		assert.Equal(t, refOutput, gjson.GetBytes(resp, "content").String(), "tool text must survive verbatim under \"content\"")
	})

	t.Run("text block content with nested $ref is wrapped as opaque text", func(t *testing.T) {
		resp := functionResponse(t, build(&schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{
			Type: schemas.ChatContentBlockTypeText,
			Text: schemas.Ptr(refOutput),
		}}}))
		assert.False(t, chatToolResultHasRefKey(resp), "function_response.response must not carry a $ref key: %s", resp)
		assert.Equal(t, refOutput, gjson.GetBytes(resp, "content").String(), "tool text must survive verbatim under \"content\"")
	})

	t.Run("top-level $ref is wrapped as opaque text", func(t *testing.T) {
		const topLevel = `{"$ref":"#/definitions/Thing"}`
		resp := functionResponse(t, build(&schemas.ChatMessageContent{ContentStr: schemas.Ptr(topLevel)}))
		assert.False(t, chatToolResultHasRefKey(resp), "function_response.response must not carry a $ref key: %s", resp)
		assert.Equal(t, topLevel, gjson.GetBytes(resp, "content").String())
	})

	t.Run("unicode-escaped $ref key is wrapped as opaque text", func(t *testing.T) {
		// JSON allows any character of a key to be escaped; "$r\u0065f" decodes to "$ref".
		escaped := `{"schema":{"$r\u0065f":"#/x"}}`
		resp := functionResponse(t, build(&schemas.ChatMessageContent{ContentStr: schemas.Ptr(escaped)}))
		assert.False(t, chatToolResultHasRefKey(resp), "function_response.response must not carry a $ref key: %s", resp)
		assert.Equal(t, escaped, gjson.GetBytes(resp, "content").String())
	})

	t.Run("JSON object without $ref keeps the structured fast path", func(t *testing.T) {
		resp := functionResponse(t, build(&schemas.ChatMessageContent{ContentStr: schemas.Ptr(plainOutput)}))
		assert.JSONEq(t, plainOutput, string(resp), "plain JSON tool output must still be forwarded as a structured object")
	})
}

// Gemini's function response is text: media a tool returned is named, not silently dropped.
func TestToGeminiChatCompletionRequest_ToolResultNamesMedia(t *testing.T) {
	out, err := gemini.ToGeminiChatCompletionRequest(nil, &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-flash-latest",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("plot it")}},
			{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{{
				ID: schemas.Ptr("c1"), Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("plot"), Arguments: `{}`},
			}}}},
			{Role: schemas.ChatMessageRoleTool, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("c1")}, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
		{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("chart below")},
		{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "data:image/png;base64,iVBORw0KGgo="}},
		{Type: schemas.ChatContentBlockTypeInputAudio, InputAudio: &schemas.ChatInputAudio{Data: "SUQz", Format: schemas.Ptr("mp3")}},
	}}},
		},
	})
	require.NoError(t, err)
	response := gjson.GetBytes(out.Contents[2].Parts[0].FunctionResponse.Response, "content").String()
	assert.Contains(t, response, "chart below")
	assert.Contains(t, response, "[image/png")
	assert.Contains(t, response, "[audio/mp3")
}

// TestToBifrostChatCompletionStream_StreamedFunctionCallArguments pins the chat side of
// Vertex streamFunctionCallArguments: a function call arriving as partialArgs leaves must
// reach the client as OpenAI tool_call argument deltas while the call is still being
// generated, and the deltas for each call must concatenate to its arguments.
//
// Per call it asserts: one stable index, the id and name on the first delta only (with
// the thought signature embedded the same way as for a complete call), argument text that
// parses as exactly the value the leaves describe (so no leaf text is dropped, repeated or
// reordered), and argument text emitted no later than the chunk carrying the first leaf
// (well before the call ends). It also pins that no nameless tool call is invented for the
// empty functionCall end marker and that finish_reason is tool_calls.
func TestToBifrostChatCompletionStream_StreamedFunctionCallArguments(t *testing.T) {
	for _, fixture := range gemini.StreamFunctionCallArgsFixtures() {
		t.Run(fixture, func(t *testing.T) {
			chunks := gemini.LoadStreamFunctionCallArgsFixture(t, fixture)
			expected := gemini.ExpectedStreamedCalls(t, chunks)
			require.NotEmpty(t, expected)

			type gotCall struct {
				id, name        string
				args            strings.Builder
				deltas          int
				firstArgsChunk  int
				lastDeltaChunk  int
				reasoningSigned bool
			}
			calls := map[uint16]*gotCall{}
			var order []uint16
			var finishReason string
			state := gemini.NewGeminiStreamState()
			for i, chunk := range chunks {
				responses, bifrostErr, _ := chunk.ToBifrostChatCompletionStream(state)
				require.Nil(t, bifrostErr)
				for _, resp := range responses {
					require.Len(t, resp.Choices, 1)
					choice := resp.Choices[0]
					if choice.FinishReason != nil {
						finishReason = *choice.FinishReason
					}
					delta := choice.ChatStreamResponseChoice.Delta
					for _, tc := range delta.ToolCalls {
						c, seen := calls[tc.Index]
						if !seen {
							require.NotNil(t, tc.ID, "first delta of tool call %d carries no id", tc.Index)
							require.NotNil(t, tc.Function.Name, "first delta of tool call %d carries no name", tc.Index)
							require.NotEmpty(t, *tc.Function.Name, "a nameless tool call was emitted (empty functionCall end marker?)")
							c = &gotCall{id: *tc.ID, name: *tc.Function.Name, firstArgsChunk: -1}
							calls[tc.Index] = c
							order = append(order, tc.Index)
						} else {
							assert.Nil(t, tc.ID, "tool call %d repeats its id after the first delta", tc.Index)
							assert.Nil(t, tc.Function.Name, "tool call %d repeats its name after the first delta", tc.Index)
						}
						if tc.Function.Arguments != "" {
							c.args.WriteString(tc.Function.Arguments)
							c.deltas++
							c.lastDeltaChunk = i
							if c.firstArgsChunk < 0 {
								c.firstArgsChunk = i
							}
						}
					}
				}
				// An encrypted reasoning detail with ID tool_call_<base id> carries the signature
				// the next turn needs; match it to the call whose id it prefixes.
				for _, resp := range responses {
					for _, detail := range resp.Choices[0].ChatStreamResponseChoice.Delta.ReasoningDetails {
						if detail.ID == nil || detail.Signature == nil {
							continue
						}
						base := strings.TrimPrefix(*detail.ID, "tool_call_")
						for _, c := range calls {
							if strings.HasPrefix(c.id, base+"_ts_") {
								c.reasoningSigned = true
							}
						}
					}
				}
			}

			require.Len(t, order, len(expected), "tool call count")
			for n, idx := range order {
				want := expected[n]
				got := calls[idx]
				assert.Equal(t, uint16(n), idx, "tool call indices must be 0,1,2... in call order")
				assert.Equal(t, want.Name, got.name)
				assert.True(t, strings.HasPrefix(got.id, want.ID), "id %q does not start with Vertex id %q", got.id, want.ID)
				if len(want.Signature) > 0 {
					assert.Contains(t, got.id, "_ts_", "thought signature not embedded in the call id")
					assert.True(t, got.reasoningSigned, "thought signature not emitted as an encrypted reasoning detail")
				}
				assert.Equal(t, want.Args, gemini.DecodeArgs(t, got.args.String()), "call %d (%s): concatenated arguments %s", n, want.Name, got.args.String())
				if want.Streamed && want.FirstLeafChunk >= 0 {
					assert.LessOrEqual(t, got.firstArgsChunk, want.FirstLeafChunk, "call %d: argument text held back past the chunk carrying the first leaf", n)
					if want.EndChunk > want.FirstLeafChunk {
						assert.Greater(t, got.deltas, 1, "call %d: arguments arrived in one burst", n)
					}
				}
			}
			assert.Equal(t, "tool_calls", finishReason)
		})
	}
}

// TestToBifrostChatCompletionStream_CompleteFunctionCallUnchanged pins that a call Vertex
// sends complete (no streamFunctionCallArguments) still produces exactly one tool call
// delta carrying the raw args bytes, as before streamed calls were supported.
func TestToBifrostChatCompletionStream_CompleteFunctionCallUnchanged(t *testing.T) {
	chunk := &gemini.GenerateContentResponse{
		ResponseID: "complete",
		Candidates: []*gemini.Candidate{{
			FinishReason: gemini.FinishReasonStop,
			Content: &gemini.Content{Role: "model", Parts: []*gemini.Part{
				{FunctionCall: &gemini.FunctionCall{Name: "get_weather", ID: "w1", Args: []byte(`{"city":"Paris","days":3}`)}},
				{FunctionCall: &gemini.FunctionCall{Name: "noop"}},
			}},
		}},
		UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{TotalTokenCount: 3},
	}
	responses, bifrostErr, isLast := chunk.ToBifrostChatCompletionStream(gemini.NewGeminiStreamState())
	require.Nil(t, bifrostErr)
	require.True(t, isLast)
	require.Len(t, responses, 1)
	toolCalls := responses[0].Choices[0].ChatStreamResponseChoice.Delta.ToolCalls
	require.Len(t, toolCalls, 2)
	assert.Equal(t, uint16(0), toolCalls[0].Index)
	assert.Equal(t, "w1", *toolCalls[0].ID)
	assert.Equal(t, `{"city":"Paris","days":3}`, toolCalls[0].Function.Arguments)
	assert.Equal(t, uint16(1), toolCalls[1].Index)
	assert.Equal(t, "noop", *toolCalls[1].ID)
	assert.Equal(t, "", toolCalls[1].Function.Arguments)
}

// TestToBifrostChatCompletionStream_StreamedCallEndings pins how a call Vertex is streaming
// as partialArgs ends when its own end marker never comes. A clean end (STOP, or other
// content starting) completes the JSON; a truncated one (MAX_TOKENS) leaves the arguments
// exactly as far as they got, like OpenAI does, so a client cannot mistake a cut-off Write
// for a complete one. Content after the call closes it, so later stray leaves are dropped.
func TestToBifrostChatCompletionStream_StreamedCallEndings(t *testing.T) {
	yes := true
	str := func(s string) *string { return &s }
	start := func(leaves ...*gemini.PartialArg) *gemini.Part {
		return &gemini.Part{FunctionCall: &gemini.FunctionCall{Name: "write", ID: "c1", PartialArgs: leaves, WillContinue: &yes}}
	}
	chunk := func(finish gemini.FinishReason, parts ...*gemini.Part) *gemini.GenerateContentResponse {
		resp := &gemini.GenerateContentResponse{Candidates: []*gemini.Candidate{{FinishReason: finish, Content: &gemini.Content{Role: "model", Parts: parts}}}}
		if finish != "" {
			resp.UsageMetadata = &gemini.GenerateContentResponseUsageMetadata{TotalTokenCount: 5}
		}
		return resp
	}
	tests := []struct {
		name       string
		chunks     []*gemini.GenerateContentResponse
		wantArgs   string
		wantFinish string
	}{
		{
			name: "MAX_TOKENS leaves the arguments truncated",
			chunks: []*gemini.GenerateContentResponse{
				chunk("", start(&gemini.PartialArg{JSONPath: "$.path", StringValue: str("/etc/x")}, &gemini.PartialArg{JSONPath: "$.content", StringValue: str("half of the fi"), WillContinue: &yes})),
				chunk(gemini.FinishReasonMaxTokens, &gemini.Part{Text: ""}),
			},
			wantArgs:   `{"path":"/etc/x","content":"half of the fi`,
			wantFinish: "length",
		},
		{
			name: "STOP completes a call still open",
			chunks: []*gemini.GenerateContentResponse{
				chunk("", start(&gemini.PartialArg{JSONPath: "$.content", StringValue: str("all"), WillContinue: &yes})),
				chunk(gemini.FinishReasonStop, &gemini.Part{Text: ""}),
			},
			wantArgs:   `{"content":"all"}`,
			wantFinish: "tool_calls",
		},
		{
			name: "complete args on a part that also says willContinue win",
			chunks: []*gemini.GenerateContentResponse{
				chunk("", &gemini.Part{FunctionCall: &gemini.FunctionCall{Name: "write", ID: "c1", Args: []byte(`{"a":1}`), WillContinue: &yes}}),
				chunk("", &gemini.Part{FunctionCall: &gemini.FunctionCall{}}),
				chunk(gemini.FinishReasonStop, &gemini.Part{Text: ""}),
			},
			wantArgs:   `{"a":1}`,
			wantFinish: "tool_calls",
		},
		{
			name: "text after the call closes it and later leaves are dropped",
			chunks: []*gemini.GenerateContentResponse{
				chunk("", start(&gemini.PartialArg{JSONPath: "$.a", StringValue: str("x"), WillContinue: &yes})),
				chunk("", &gemini.Part{Text: "done."}),
				chunk("", &gemini.Part{FunctionCall: &gemini.FunctionCall{PartialArgs: []*gemini.PartialArg{{JSONPath: "$.b", NumberValue: []byte("1")}}}}),
				chunk(gemini.FinishReasonStop, &gemini.Part{Text: ""}),
			},
			wantArgs:   `{"a":"x"}`,
			wantFinish: "tool_calls",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := gemini.NewGeminiStreamState()
			var args strings.Builder
			finish := ""
			for _, c := range tt.chunks {
				responses, bifrostErr, _ := c.ToBifrostChatCompletionStream(state)
				require.Nil(t, bifrostErr)
				for _, resp := range responses {
					for _, tc := range resp.Choices[0].ChatStreamResponseChoice.Delta.ToolCalls {
						require.Equal(t, uint16(0), tc.Index)
						args.WriteString(tc.Function.Arguments)
					}
					if resp.Choices[0].FinishReason != nil {
						finish = *resp.Choices[0].FinishReason
					}
				}
			}
			assert.Equal(t, tt.wantArgs, args.String())
			assert.Equal(t, tt.wantFinish, finish)
		})
	}
}
