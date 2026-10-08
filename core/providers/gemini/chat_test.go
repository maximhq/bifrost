package gemini_test

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
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

// This is the client request from #8149. Its signature is a byte-preservation
// fixture, not a token whose validity has been checked by a live Gemini model.
const googleThoughtSignatureReplayPayload = `{
 "model":"gemini/gemini-2.5-flash","max_tokens":50,
 "tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],
 "messages":[
  {"role":"user","content":"weather in Paris?"},
  {"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function",
     "function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"},
     "extra_content":{"google":{"thought_signature":"AQJyZWFsLXNpZ25hdHVyZS1ieXRlcwM="}}}]},
  {"role":"tool","tool_call_id":"call_1","content":"sunny"}]}`

func googleThoughtSignatureReplayRequest(t *testing.T) *schemas.BifrostChatRequest {
	t.Helper()
	var inbound struct {
		Model    string                `json:"model"`
		Messages []schemas.ChatMessage `json:"messages"`
	}
	require.NoError(t, schemas.Unmarshal([]byte(googleThoughtSignatureReplayPayload), &inbound))
	var params schemas.ChatParameters
	require.NoError(t, schemas.Unmarshal([]byte(googleThoughtSignatureReplayPayload), &params))
	return &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    strings.TrimPrefix(inbound.Model, "gemini/"),
		Input:    inbound.Messages,
		Params:   &params,
	}
}

func TestGeminiChatClientThoughtSignatureReplay(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{schemas.Gemini, schemas.Vertex} {
		t.Run(string(provider), func(t *testing.T) {
			request := googleThoughtSignatureReplayRequest(t)
			request.Provider = provider
			before, err := schemas.Marshal(request)
			require.NoError(t, err)
			var upstream *gemini.GeminiGenerationRequest
			if provider == schemas.Vertex {
				upstream, err = gemini.ToGeminiChatCompletionRequestWithImageURLSchemes(nil, request, "http", "https", "gs")
			} else {
				upstream, err = gemini.ToGeminiChatCompletionRequest(nil, request)
			}
			require.NoError(t, err)
			wire, err := schemas.Marshal(upstream)
			require.NoError(t, err)
			assert.Equal(t, "AQJyZWFsLXNpZ25hdHVyZS1ieXRlcwM=", gjson.GetBytes(wire, "contents.1.parts.0.thoughtSignature").String())
			assert.Equal(t, "call_1", gjson.GetBytes(wire, "contents.1.parts.0.functionCall.id").String())
			assert.Equal(t, "get_weather", gjson.GetBytes(wire, "contents.2.parts.0.functionResponse.name").String())
			after, err := schemas.Marshal(request)
			require.NoError(t, err)
			assert.Equal(t, before, after, "retries must see the original input and metadata")
		})
	}
}

func TestGeminiChatClientThoughtSignatureFallbacks(t *testing.T) {
	const sentinel = "skip_thought_signature_validator"
	extra := json.RawMessage(`{"google":{"thought_signature":"+/8="}}`)
	for _, tc := range []struct {
		name          string
		id            string
		reasoning     *string
		extra         json.RawMessage
		wantSignature []byte
	}{
		{name: "valid standard base64", id: "call_1", extra: extra, wantSignature: []byte{0xfb, 0xff}},
		{name: "unpadded standard base64", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":"+/8"}}`), wantSignature: []byte{0xfb, 0xff}},
		{name: "padded URL base64", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":"-_8="}}`), wantSignature: []byte{0xfb, 0xff}},
		{name: "unpadded URL base64", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":"-_8"}}`), wantSignature: []byte{0xfb, 0xff}},
		{name: "embedded ID takes priority", id: "call_1_ts_AQID", reasoning: schemas.Ptr("BAUG"), extra: extra, wantSignature: []byte{1, 2, 3}},
		{name: "reasoning takes priority", id: "call_1", reasoning: schemas.Ptr("BAUG"), extra: extra, wantSignature: []byte{4, 5, 6}},
		{name: "invalid ID recovers metadata", id: "call_1_ts_!", extra: extra, wantSignature: []byte{0xfb, 0xff}},
		{name: "invalid reasoning recovers metadata", id: "call_1", reasoning: schemas.Ptr("!"), extra: extra, wantSignature: []byte{0xfb, 0xff}},
		{name: "nil metadata", id: "call_1", wantSignature: []byte(sentinel)},
		{name: "null metadata", id: "call_1", extra: json.RawMessage(`null`), wantSignature: []byte(sentinel)},
		{name: "empty object", id: "call_1", extra: json.RawMessage(`{}`), wantSignature: []byte(sentinel)},
		{name: "other provider", id: "call_1", extra: json.RawMessage(`{"other":{"thought_signature":"AQID"}}`), wantSignature: []byte(sentinel)},
		{name: "null signature", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":null}}`), wantSignature: []byte(sentinel)},
		{name: "empty signature", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":""}}`), wantSignature: []byte(sentinel)},
		{name: "base64 without bytes", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":"\n"}}`), wantSignature: []byte(sentinel)},
		{name: "invalid base64", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":"!"}}`), wantSignature: []byte(sentinel)},
		{name: "numeric signature", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":1234}}`), wantSignature: []byte(sentinel)},
		{name: "boolean signature", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":true}}`), wantSignature: []byte(sentinel)},
		{name: "array signature", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":["AQID"]}}`), wantSignature: []byte(sentinel)},
		{name: "object signature", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":{"value":"AQID"}}}`), wantSignature: []byte(sentinel)},
		{name: "malformed metadata", id: "call_1", extra: json.RawMessage(`{"google":{"thought_signature":"AQID"}`), wantSignature: []byte(sentinel)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := googleThoughtSignatureReplayRequest(t)
			assistant := request.Input[1].ChatAssistantMessage
			assistant.ToolCalls[0].ID = schemas.Ptr(tc.id)
			assistant.ToolCalls[0].ExtraContent = tc.extra
			if tc.reasoning != nil {
				assistant.ReasoningDetails = []schemas.ChatReasoningDetails{{
					ID:        schemas.Ptr("tool_call_call_1"),
					Type:      schemas.BifrostReasoningDetailsTypeEncrypted,
					Signature: tc.reasoning,
				}}
			}
			upstream, err := gemini.ToGeminiChatCompletionRequest(nil, request)
			require.NoError(t, err)
			require.Len(t, upstream.Contents, 3)
			require.Len(t, upstream.Contents[1].Parts, 1)
			assert.Equal(t, tc.wantSignature, upstream.Contents[1].Parts[0].ThoughtSignature)
			assert.Equal(t, tc.extra, assistant.ToolCalls[0].ExtraContent)
		})
	}
}

func TestGeminiChatClientThoughtSignatureMultipleCalls(t *testing.T) {
	request := googleThoughtSignatureReplayRequest(t)
	assistant := request.Input[1].ChatAssistantMessage
	assistant.ToolCalls = append(assistant.ToolCalls, schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call_2"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("get_weather"),
			Arguments: `{"city":"London"}`,
		},
		ExtraContent: json.RawMessage(`{"google":{"thought_signature":"+/8="}}`),
	})
	request.Input = append(request.Input, schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_2")},
		Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("rainy")},
	})
	upstream, err := gemini.ToGeminiChatCompletionRequest(nil, request)
	require.NoError(t, err)
	require.Len(t, upstream.Contents[1].Parts, 2)
	wire, err := schemas.Marshal(upstream)
	require.NoError(t, err)
	assert.Equal(t, "AQJyZWFsLXNpZ25hdHVyZS1ieXRlcwM=", gjson.GetBytes(wire, "contents.1.parts.0.thoughtSignature").String())
	assert.Equal(t, "+/8=", gjson.GetBytes(wire, "contents.1.parts.1.thoughtSignature").String())
	assert.Equal(t, "call_2", upstream.Contents[1].Parts[1].FunctionCall.ID)
	// Decoding produces owned bytes even if the caller recycles its raw metadata.
	for i := range assistant.ToolCalls[1].ExtraContent {
		assistant.ToolCalls[1].ExtraContent[i] = ' '
	}
	assert.Equal(t, []byte{0xfb, 0xff}, upstream.Contents[1].Parts[1].ThoughtSignature)
}

func TestGeminiChatClientThoughtSignatureHTTPReplay(t *testing.T) {
	const fixtureResponse = `{"candidates":[{"content":{"role":"model","parts":[{"text":"sunny"}]},"finishReason":"STOP"}],"responseId":"fixture","modelVersion":"gemini-2.5-flash","usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	for _, streaming := range []bool{false, true} {
		name := "unary"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			type recordedRequest struct {
				path string
				body []byte
				err  error
			}
			recorded := make(chan recordedRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				recorded <- recordedRequest{path: r.URL.Path, body: body, err: err}
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+fixtureResponse+"\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, fixtureResponse)
				}
			}))
			defer server.Close()
			provider := gemini.NewGeminiProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta", AllowPrivateNetwork: true},
			}, bifrost.NewDefaultLogger(schemas.LogLevelError))
			parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
			key := schemas.Key{Value: *schemas.NewSecretVar("local-fixture-key")}
			request := googleThoughtSignatureReplayRequest(t)
			if streaming {
				postHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
					return result, err
				}
				chunks, bifrostErr := provider.ChatCompletionStream(ctx, postHook, func(context.Context) {}, key, request)
				require.Nil(t, bifrostErr)
				count := 0
			readStream:
				for {
					select {
					case chunk, open := <-chunks:
						if !open {
							break readStream
						}
						require.Nil(t, chunk.BifrostError)
						count++
					case <-parent.Done():
						t.Fatal("local fixture stream did not finish")
					}
				}
				assert.Positive(t, count)
			} else {
				response, bifrostErr := provider.ChatCompletion(ctx, key, request)
				require.Nil(t, bifrostErr)
				require.NotNil(t, response)
			}
			select {
			case got := <-recorded:
				require.NoError(t, got.err)
				endpoint := ":generateContent"
				if streaming {
					endpoint = ":streamGenerateContent"
				}
				assert.Equal(t, "/v1beta/models/gemini-2.5-flash"+endpoint, got.path)
				assert.Equal(t, "AQJyZWFsLXNpZ25hdHVyZS1ieXRlcwM=", gjson.GetBytes(got.body, "contents.1.parts.0.thoughtSignature").String())
			case <-parent.Done():
				t.Fatal("local fixture did not record the request")
			}
		})
	}
}
