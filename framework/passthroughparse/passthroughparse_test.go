package passthroughparse

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	openAIChatRequest  = `{"model":"gpt-4o","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"weather in Paris?"}],"temperature":0.2,"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`
	openAIChatResponse = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-4o","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`

	openAIResponsesRequest  = `{"model":"gpt-5","input":"say hi","instructions":"be terse","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`
	openAIResponsesResponse = `{"id":"resp_1","object":"response","created_at":1,"model":"gpt-5","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}`

	openAITextRequest  = `{"model":"gpt-3.5-turbo-instruct","prompt":"once upon a","max_tokens":5}`
	openAITextResponse = `{"id":"cmpl-1","object":"text_completion","created":1,"model":"gpt-3.5-turbo-instruct","choices":[{"index":0,"text":" time","finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`

	anthropicMessagesRequest  = `{"model":"claude-sonnet-4-5","max_tokens":1024,"system":"be brief","messages":[{"role":"user","content":"weather in Paris?"}],"tools":[{"name":"get_weather","description":"weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
	anthropicMessagesResponse = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"thinking","thinking":"need the tool","signature":"sig"},{"type":"text","text":"Checking."},{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}],"stop_reason":"tool_use","usage":{"input_tokens":30,"output_tokens":12}}`

	anthropicCountRequest  = `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"count me"}]}`
	anthropicCountResponse = `{"input_tokens":9}`

	anthropicCompleteRequest  = `{"model":"claude-2.1","prompt":"\n\nHuman: hi\n\nAssistant:","max_tokens_to_sample":10}`
	anthropicCompleteResponse = `{"type":"completion","id":"compl_1","completion":" Hello!","stop_reason":"stop_sequence","model":"claude-2.1"}`

	geminiRequest  = `{"systemInstruction":{"parts":[{"text":"be brief"}]},"contents":[{"role":"user","parts":[{"text":"weather in Paris?"}]}],"generationConfig":{"temperature":0.3},"tools":[{"functionDeclarations":[{"name":"get_weather","parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"}}}}]}]}`
	geminiResponse = `{"candidates":[{"content":{"role":"model","parts":[{"text":"Sunny."}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12},"modelVersion":"gemini-2.5-flash"}`

	geminiCountRequest  = `{"contents":[{"role":"user","parts":[{"text":"count me"}]}]}`
	geminiCountResponse = `{"totalTokens":4,"promptTokensDetails":[{"modality":"TEXT","tokenCount":4}]}`
)

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		in       Input
		wantType schemas.RequestType
		check    func(t *testing.T, r *Result)
	}{
		{
			name:     "openai chat completions",
			in:       Input{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(openAIChatResponse)},
			wantType: schemas.ChatCompletionRequest,
			check:    checkChatToolCall,
		},
		{
			name:     "azure openai deployment chat completions",
			in:       Input{Provider: schemas.Azure, Model: "gpt-4o", Method: "POST", Path: "/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21", StatusCode: 200, RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(openAIChatResponse)},
			wantType: schemas.ChatCompletionRequest,
			check:    checkChatToolCall,
		},
		{
			name:     "vertex mistral rawPredict speaks openai chat",
			in:       Input{Provider: schemas.Vertex, Model: "mistral-large", Method: "POST", Path: "/projects/p/locations/us-central1/publishers/mistralai/models/mistral-large:rawPredict", StatusCode: 200, RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(openAIChatResponse)},
			wantType: schemas.ChatCompletionRequest,
			check:    checkChatToolCall,
		},
		{
			name:     "vertex openapi endpoint chat completions",
			in:       Input{Provider: schemas.Vertex, Model: "meta/llama-3.3-70b", Method: "POST", Path: "/projects/p/locations/us-central1/endpoints/openapi/chat/completions", StatusCode: 200, RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(openAIChatResponse)},
			wantType: schemas.ChatCompletionRequest,
			check:    checkChatToolCall,
		},
		{
			name:     "openai responses",
			in:       Input{Provider: schemas.OpenAI, Model: "gpt-5", Method: "POST", Path: "/v1/responses", StatusCode: 200, RequestBody: []byte(openAIResponsesRequest), ResponseBody: []byte(openAIResponsesResponse)},
			wantType: schemas.ResponsesRequest,
			check:    checkOpenAIResponses,
		},
		{
			name:     "chatgpt codex responses",
			in:       Input{Provider: schemas.OpenAI, Model: "gpt-5", Method: "POST", Path: "/backend-api/codex/responses", StatusCode: 200, RequestBody: []byte(openAIResponsesRequest), ResponseBody: []byte(openAIResponsesResponse)},
			wantType: schemas.ResponsesRequest,
			check:    checkOpenAIResponses,
		},
		{
			name:     "openai legacy completions",
			in:       Input{Provider: schemas.OpenAI, Model: "gpt-3.5-turbo-instruct", Method: "POST", Path: "/v1/completions", StatusCode: 200, RequestBody: []byte(openAITextRequest), ResponseBody: []byte(openAITextResponse)},
			wantType: schemas.TextCompletionRequest,
			check: func(t *testing.T, r *Result) {
				require.NotNil(t, r.Request.TextCompletionRequest)
				require.NotNil(t, r.Request.TextCompletionRequest.Input.PromptStr)
				require.Equal(t, "once upon a", *r.Request.TextCompletionRequest.Input.PromptStr)
				require.NotNil(t, r.Response.TextCompletionResponse)
				require.Len(t, r.Response.TextCompletionResponse.Choices, 1)
				require.Equal(t, " time", *r.Response.TextCompletionResponse.Choices[0].TextCompletionResponseChoice.Text)
			},
		},
		{
			name:     "anthropic messages with thinking and tool use",
			in:       Input{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Method: "POST", Path: "/v1/messages", StatusCode: 200, RequestBody: []byte(anthropicMessagesRequest), ResponseBody: []byte(anthropicMessagesResponse)},
			wantType: schemas.ResponsesRequest,
			check:    checkAnthropicMessages,
		},
		{
			name:     "azure foundry anthropic messages",
			in:       Input{Provider: schemas.Azure, Model: "claude-sonnet-4-5", Method: "POST", Path: "/anthropic/v1/messages", StatusCode: 200, RequestBody: []byte(anthropicMessagesRequest), ResponseBody: []byte(anthropicMessagesResponse)},
			wantType: schemas.ResponsesRequest,
			check:    checkAnthropicMessages,
		},
		{
			name:     "vertex claude rawPredict",
			in:       Input{Provider: schemas.Vertex, Model: "claude-sonnet-4-5", Method: "POST", Path: "/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-5:rawPredict", StatusCode: 200, RequestBody: []byte(anthropicMessagesRequest), ResponseBody: []byte(anthropicMessagesResponse)},
			wantType: schemas.ResponsesRequest,
			check:    checkAnthropicMessages,
		},
		{
			name:     "anthropic count tokens",
			in:       Input{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Method: "POST", Path: "/v1/messages/count_tokens", StatusCode: 200, RequestBody: []byte(anthropicCountRequest), ResponseBody: []byte(anthropicCountResponse)},
			wantType: schemas.CountTokensRequest,
			check: func(t *testing.T, r *Result) {
				require.NotNil(t, r.Request.CountTokensRequest)
				require.Len(t, r.Request.CountTokensRequest.Input, 1)
				require.NotNil(t, r.Response.CountTokensResponse)
				require.Equal(t, 9, r.Response.CountTokensResponse.InputTokens)
			},
		},
		{
			name:     "anthropic legacy complete",
			in:       Input{Provider: schemas.Anthropic, Model: "claude-2.1", Method: "POST", Path: "/v1/complete", StatusCode: 200, RequestBody: []byte(anthropicCompleteRequest), ResponseBody: []byte(anthropicCompleteResponse)},
			wantType: schemas.TextCompletionRequest,
			check: func(t *testing.T, r *Result) {
				require.NotNil(t, r.Request.TextCompletionRequest)
				require.NotNil(t, r.Response.TextCompletionResponse)
				require.Len(t, r.Response.TextCompletionResponse.Choices, 1)
				require.Equal(t, " Hello!", *r.Response.TextCompletionResponse.Choices[0].TextCompletionResponseChoice.Text)
			},
		},
		{
			name:     "gemini generateContent takes the model from the path",
			in:       Input{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Method: "POST", Path: "/models/gemini-2.5-flash:generateContent", StatusCode: 200, RequestBody: []byte(geminiRequest), ResponseBody: []byte(geminiResponse)},
			wantType: schemas.ResponsesRequest,
			check:    checkGemini,
		},
		{
			name:     "vertex gemini generateContent",
			in:       Input{Provider: schemas.Vertex, Model: "gemini-2.5-flash", Method: "POST", Path: "/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent", StatusCode: 200, RequestBody: []byte(geminiRequest), ResponseBody: []byte(geminiResponse)},
			wantType: schemas.ResponsesRequest,
			check:    checkGemini,
		},
		{
			name:     "gemini count tokens",
			in:       Input{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Method: "POST", Path: "/models/gemini-2.5-flash:countTokens", StatusCode: 200, RequestBody: []byte(geminiCountRequest), ResponseBody: []byte(geminiCountResponse)},
			wantType: schemas.CountTokensRequest,
			check: func(t *testing.T, r *Result) {
				require.NotNil(t, r.Request.CountTokensRequest)
				require.Len(t, r.Request.CountTokensRequest.Input, 1)
				require.NotNil(t, r.Response.CountTokensResponse)
				require.Equal(t, 4, r.Response.CountTokensResponse.InputTokens)
				require.Equal(t, 4, *r.Response.CountTokensResponse.TotalTokens)
			},
		},
		{
			name:     "upstream error body is not parsed as a response",
			in:       Input{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions", StatusCode: 400, RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(`{"error":{"message":"bad","type":"invalid_request_error"}}`)},
			wantType: schemas.ChatCompletionRequest,
			check: func(t *testing.T, r *Result) {
				require.NotNil(t, r.Request.ChatRequest)
				require.Nil(t, r.Response)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Parse(tt.in)
			require.NoError(t, err)
			require.NotNil(t, r)
			require.Equal(t, tt.wantType, r.RequestType)
			require.NotNil(t, r.Request)
			provider, model, _ := r.Request.GetRequestFields()
			require.Equal(t, tt.in.Provider, provider)
			require.Equal(t, tt.in.Model, model)
			tt.check(t, r)
		})
	}
}

func checkChatToolCall(t *testing.T, r *Result) {
	t.Helper()
	req := r.Request.ChatRequest
	require.NotNil(t, req)
	require.Len(t, req.Input, 2)
	require.Equal(t, schemas.ChatMessageRoleSystem, req.Input[0].Role)
	require.Equal(t, "weather in Paris?", *req.Input[1].Content.ContentStr)
	require.NotNil(t, req.Params)
	require.NotNil(t, req.Params.Temperature)
	require.Len(t, req.Params.Tools, 1)
	require.Equal(t, "get_weather", req.Params.Tools[0].Function.Name)

	require.NotNil(t, r.Response)
	resp := r.Response.ChatResponse
	require.NotNil(t, resp)
	require.Len(t, resp.Choices, 1)
	msg := resp.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, msg.ChatAssistantMessage)
	require.Len(t, msg.ChatAssistantMessage.ToolCalls, 1)
	require.Equal(t, "get_weather", *msg.ChatAssistantMessage.ToolCalls[0].Function.Name)
	require.Equal(t, "tool_calls", *resp.Choices[0].FinishReason)
}

func checkOpenAIResponses(t *testing.T, r *Result) {
	t.Helper()
	req := r.Request.ResponsesRequest
	require.NotNil(t, req)
	require.Len(t, req.Input, 1)
	require.Equal(t, "say hi", *req.Input[0].Content.ContentStr)
	require.NotNil(t, req.Params)
	require.Equal(t, "be terse", *req.Params.Instructions)
	require.Len(t, req.Params.Tools, 1)

	require.NotNil(t, r.Response)
	resp := r.Response.ResponsesResponse
	require.NotNil(t, resp)
	require.Len(t, resp.Output, 1)
	require.Equal(t, "hi", *resp.Output[0].Content.ContentBlocks[0].Text)
}

func checkAnthropicMessages(t *testing.T, r *Result) {
	t.Helper()
	req := r.Request.ResponsesRequest
	require.NotNil(t, req)
	// system prompt + user turn
	require.Len(t, req.Input, 2)
	require.NotNil(t, req.Params)
	require.Len(t, req.Params.Tools, 1)
	require.NotNil(t, req.Params.Tools[0].Name)
	require.Equal(t, "get_weather", *req.Params.Tools[0].Name)

	require.NotNil(t, r.Response)
	resp := r.Response.ResponsesResponse
	require.NotNil(t, resp)
	var sawReasoning, sawText, sawToolCall bool
	for _, item := range resp.Output {
		if item.Type == nil {
			continue
		}
		switch *item.Type {
		case schemas.ResponsesMessageTypeReasoning:
			sawReasoning = true
		case schemas.ResponsesMessageTypeMessage:
			sawText = true
		case schemas.ResponsesMessageTypeFunctionCall:
			sawToolCall = true
			require.Equal(t, "get_weather", *item.ResponsesToolMessage.Name)
		}
	}
	require.True(t, sawReasoning, "thinking block should map to a reasoning item")
	require.True(t, sawText, "text block should map to a message item")
	require.True(t, sawToolCall, "tool_use block should map to a function_call item")
}

func checkGemini(t *testing.T, r *Result) {
	t.Helper()
	req := r.Request.ResponsesRequest
	require.NotNil(t, req)
	require.Len(t, req.Input, 2) // system instruction + user turn
	require.NotNil(t, req.Params)
	require.NotEmpty(t, req.Params.Tools)

	require.NotNil(t, r.Response)
	resp := r.Response.ResponsesResponse
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Output)
	require.Equal(t, "Sunny.", *resp.Output[0].Content.ContentBlocks[0].Text)
}

// TestParseCompactionAndCachedContentRequests verifies a compaction body parses into its own request type, and a
// Gemini cached content create into a Responses-shaped request under its own request type.
func TestParseCompactionAndCachedContentRequests(t *testing.T) {
	compaction, err := Parse(Input{Provider: schemas.OpenAI, Model: "gpt-5", Method: "POST", Path: "/v1/responses/compact", RequestBody: []byte(`{"model":"gpt-5","instructions":"be brief","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)})
	require.NoError(t, err)
	require.NotNil(t, compaction)
	require.Equal(t, schemas.CompactionRequest, compaction.RequestType)
	require.NotNil(t, compaction.Request.CompactionRequest)
	require.Len(t, compaction.Request.CompactionRequest.Input, 1)
	require.NotNil(t, compaction.Request.CompactionRequest.Instructions)
	require.Equal(t, "be brief", *compaction.Request.CompactionRequest.Instructions)

	cached, err := Parse(Input{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Method: "POST", Path: "/v1beta/cachedContents", RequestBody: []byte(`{"model":"models/gemini-2.5-flash","ttl":"300s","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)})
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, schemas.CachedContentCreateRequest, cached.RequestType)
	require.NotNil(t, cached.Request.ResponsesRequest)
	require.Len(t, cached.Request.ResponsesRequest.Input, 1)
}

func TestParseUnknownRoutesReturnNil(t *testing.T) {
	tests := []struct {
		name string
		in   Input
	}{
		{"openai list models", Input{Provider: schemas.OpenAI, Method: "GET", Path: "/v1/models", StatusCode: 200, ResponseBody: []byte(`{"data":[]}`)}},
		{"anthropic batches", Input{Provider: schemas.Anthropic, Method: "POST", Path: "/v1/messages/batches", StatusCode: 200, RequestBody: []byte(`{"requests":[]}`)}},
		{"provider without passthrough", Input{Provider: schemas.Groq, Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, RequestBody: []byte(openAIChatRequest)}},
		// The parser keys off the wire format, so a custom provider's own name is not enough;
		// the caller resolves it to its BaseProviderType first (see TestParseCustomProviderBaseType).
		{"custom provider name without base type", Input{Provider: "my-openai", Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, RequestBody: []byte(openAIChatRequest)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Parse(tt.in)
			require.NoError(t, err)
			require.Nil(t, r)
		})
	}
}

// TestParseCustomProviderBaseType pins that a custom provider parses through the wire format of its
// base type: the caller passes the BaseProviderType, and the parsed request then reports it.
func TestParseCustomProviderBaseType(t *testing.T) {
	r, err := Parse(Input{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Method: "POST", Path: "/v1/messages", StatusCode: 200, RequestBody: []byte(anthropicMessagesRequest), ResponseBody: []byte(anthropicMessagesResponse)})
	require.NoError(t, err)
	require.NotNil(t, r)
	checkAnthropicMessages(t, r)
}

func TestParseKeepsTheSideThatDecoded(t *testing.T) {
	r, err := Parse(Input{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, RequestBody: []byte(`{"messages": [`), ResponseBody: []byte(openAIChatResponse)})
	require.Error(t, err)
	require.NotNil(t, r)
	require.Nil(t, r.Request)
	require.NotNil(t, r.Response)
	require.NotNil(t, r.Response.ChatResponse)

	r, err = Parse(Input{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, RequestBody: []byte(`{`), ResponseBody: []byte(`{`)})
	require.Error(t, err)
	require.Nil(t, r)
}

func TestParseMedia(t *testing.T) {
	tests := []struct {
		name     string
		in       Input
		wantType schemas.RequestType
		check    func(t *testing.T, r *Result)
	}{
		{
			name:     "openai embeddings",
			in:       Input{Provider: schemas.OpenAI, Model: "text-embedding-3-small", Method: "POST", Path: "/v1/embeddings", StatusCode: 200, RequestBody: []byte(`{"model":"text-embedding-3-small","input":["hello","world"]}`), ResponseBody: []byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]},{"object":"embedding","index":1,"embedding":[0.3,0.4]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":2,"total_tokens":2}}`)},
			wantType: schemas.EmbeddingRequest,
			check: func(t *testing.T, r *Result) {
				require.NotNil(t, r.Request.EmbeddingRequest)
				require.Len(t, r.Request.EmbeddingRequest.Input, 2)
				require.NotNil(t, r.Response.EmbeddingResponse)
				require.Len(t, r.Response.EmbeddingResponse.Data, 2)
			},
		},
		{
			name:     "openai speech parses the input text; the audio response is binary",
			in:       Input{Provider: schemas.OpenAI, Model: "gpt-4o-mini-tts", Method: "POST", Path: "/v1/audio/speech", StatusCode: 200, RequestBody: []byte(`{"model":"gpt-4o-mini-tts","input":"Hi there","voice":"alloy"}`), ResponseBody: []byte("<binary content omitted: 1234 bytes>")},
			wantType: schemas.SpeechRequest,
			check: func(t *testing.T, r *Result) {
				require.NotNil(t, r.Request.SpeechRequest)
				require.NotNil(t, r.Request.SpeechRequest.Input)
				require.Equal(t, "Hi there", r.Request.SpeechRequest.Input.Input)
				require.Nil(t, r.Response)
			},
		},
		{
			name:     "openai image generation",
			in:       Input{Provider: schemas.OpenAI, Model: "gpt-image-1", Method: "POST", Path: "/v1/images/generations", StatusCode: 200, RequestBody: []byte(`{"model":"gpt-image-1","prompt":"a red fox","size":"1024x1024"}`), ResponseBody: []byte(`{"created":1,"data":[{"b64_json":"aGk="}]}`)},
			wantType: schemas.ImageGenerationRequest,
			check: func(t *testing.T, r *Result) {
				require.NotNil(t, r.Request.ImageGenerationRequest)
				require.Equal(t, "a red fox", r.Request.ImageGenerationRequest.Input.Prompt)
				require.NotNil(t, r.Response.ImageGenerationResponse)
				require.Len(t, r.Response.ImageGenerationResponse.Data, 1)
			},
		},
		{
			name:     "gemini embedContent",
			in:       Input{Provider: schemas.Gemini, Model: "text-embedding-004", Method: "POST", Path: "/models/text-embedding-004:embedContent", StatusCode: 200, RequestBody: []byte(`{"content":{"parts":[{"text":"hello"}]}}`), ResponseBody: []byte(`{"embedding":{"values":[0.1,0.2,0.3]}}`)},
			wantType: schemas.EmbeddingRequest,
			check: func(t *testing.T, r *Result) {
				require.NotNil(t, r.Request.EmbeddingRequest)
				require.NotEmpty(t, r.Request.EmbeddingRequest.Input)
				require.NotNil(t, r.Response.EmbeddingResponse)
				require.Len(t, r.Response.EmbeddingResponse.Data, 1)
			},
		},
		{
			name:     "gemini batchEmbedContents",
			in:       Input{Provider: schemas.Gemini, Model: "text-embedding-004", Method: "POST", Path: "/models/text-embedding-004:batchEmbedContents", StatusCode: 200, RequestBody: []byte(`{"requests":[{"model":"models/text-embedding-004","content":{"parts":[{"text":"a"}]}},{"model":"models/text-embedding-004","content":{"parts":[{"text":"b"}]}}]}`), ResponseBody: []byte(`{"embeddings":[{"values":[0.1]},{"values":[0.2]}]}`)},
			wantType: schemas.EmbeddingRequest,
			check: func(t *testing.T, r *Result) {
				require.NotNil(t, r.Request.EmbeddingRequest)
				require.Len(t, r.Request.EmbeddingRequest.Input, 2)
				require.Len(t, r.Response.EmbeddingResponse.Data, 2)
			},
		},
		{
			name:     "vertex imagen predict",
			in:       Input{Provider: schemas.Vertex, Model: "imagen-4.0-generate-001", Method: "POST", Path: "/projects/p/locations/us-central1/publishers/google/models/imagen-4.0-generate-001:predict", StatusCode: 200, RequestBody: []byte(`{"instances":[{"prompt":"a red fox"}],"parameters":{"sampleCount":1}}`), ResponseBody: []byte(`{"predictions":[{"bytesBase64Encoded":"aGk=","mimeType":"image/png"}]}`)},
			wantType: schemas.ImageGenerationRequest,
			check: func(t *testing.T, r *Result) {
				require.NotNil(t, r.Request.ImageGenerationRequest)
				require.Equal(t, "a red fox", r.Request.ImageGenerationRequest.Input.Prompt)
				require.NotNil(t, r.Response.ImageGenerationResponse)
				require.Len(t, r.Response.ImageGenerationResponse.Data, 1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Parse(tt.in)
			require.NoError(t, err)
			require.NotNil(t, r)
			require.Equal(t, tt.wantType, r.RequestType)
			require.NotNil(t, r.Request)
			tt.check(t, r)
		})
	}
}

// TestParseGeminiEmbedContentMentioningRequests pins that a single :embedContent is told apart from
// :batchEmbedContents by its top-level requests key, not by the word appearing anywhere in the body.
func TestParseGeminiEmbedContentMentioningRequests(t *testing.T) {
	r, err := Parse(Input{Provider: schemas.Gemini, Model: "text-embedding-004", Method: "POST", Path: "/models/text-embedding-004:embedContent", StatusCode: 200,
		RequestBody:  []byte(`{"content":{"parts":[{"text":"requests"}]}}`),
		ResponseBody: []byte(`{"embedding":{"values":[0.1,0.2]}}`)})
	require.NoError(t, err)
	require.NotNil(t, r)
	require.NotNil(t, r.Request.EmbeddingRequest)
	require.Len(t, r.Request.EmbeddingRequest.Input, 1)
	require.Equal(t, "requests", *r.Request.EmbeddingRequest.Input[0].Content[0].Text)
}

// TestParseMediaResponseOnly covers routes whose request is not JSON (multipart uploads) or whose
// response is the only typed side: the response still parses on its own.
func TestParseMediaResponseOnly(t *testing.T) {
	r, err := Parse(Input{Provider: schemas.OpenAI, Model: "whisper-1", Method: "POST", Path: "/v1/audio/transcriptions", StatusCode: 200, ResponseBody: []byte(`{"text":"hello world"}`)})
	require.NoError(t, err)
	require.NotNil(t, r)
	require.Nil(t, r.Request)
	require.NotNil(t, r.Response.TranscriptionResponse)
	require.Equal(t, "hello world", r.Response.TranscriptionResponse.Text)

	// response_format=text returns plain text: nothing to decode, and that is not a failure.
	r, err = Parse(Input{Provider: schemas.OpenAI, Model: "whisper-1", Method: "POST", Path: "/v1/audio/transcriptions", StatusCode: 200, ResponseBody: []byte("hello world")})
	require.NoError(t, err)
	require.Nil(t, r)

	// :predict on an Imagen model that returned no images is not an image response.
	r, err = Parse(Input{Provider: schemas.Vertex, Model: "imagen-4.0-generate-001", Method: "POST", Path: "/projects/p/locations/l/publishers/google/models/imagen-4.0-generate-001:predict", StatusCode: 200, ResponseBody: []byte(`{"predictions":[]}`)})
	require.NoError(t, err)
	require.Nil(t, r)
}

// TestParseVertexPredictEmbeddings covers Vertex embedding models served on :predict: the
// instances/predictions shape, for text-embedding-* (content) and multimodalembedding@* (text).
func TestParseVertexPredictEmbeddings(t *testing.T) {
	r, err := Parse(Input{Provider: schemas.Vertex, Model: "text-embedding-005", Method: "POST", Path: "/projects/p/locations/l/publishers/google/models/text-embedding-005:predict", StatusCode: 200,
		RequestBody:  []byte(`{"instances":[{"content":"hi","task_type":"RETRIEVAL_QUERY"},{"content":"there"}]}`),
		ResponseBody: []byte(`{"predictions":[{"embeddings":{"values":[0.1,0.2],"statistics":{"token_count":1}}},{"embeddings":{"values":[0.3,0.4],"statistics":{"token_count":1}}}]}`)})
	require.NoError(t, err)
	require.NotNil(t, r)
	require.Equal(t, schemas.EmbeddingRequest, r.RequestType)
	require.NotNil(t, r.Request.EmbeddingRequest)
	require.Len(t, r.Request.EmbeddingRequest.Input, 2)
	require.Equal(t, "hi", *r.Request.EmbeddingRequest.Input[0].Content[0].Text)
	require.NotNil(t, r.Response.EmbeddingResponse)
	require.Len(t, r.Response.EmbeddingResponse.Data, 2)

	r, err = Parse(Input{Provider: schemas.Vertex, Model: "multimodalembedding@001", Method: "POST", Path: "/projects/p/locations/l/publishers/google/models/multimodalembedding@001:predict", StatusCode: 200,
		RequestBody:  []byte(`{"instances":[{"text":"a red fox"}]}`),
		ResponseBody: []byte(`{"predictions":[{"textEmbedding":[0.1,0.2]}]}`)})
	require.NoError(t, err)
	require.NotNil(t, r)
	require.Equal(t, "a red fox", *r.Request.EmbeddingRequest.Input[0].Content[0].Text)
	require.NotNil(t, r.Response.EmbeddingResponse)
	require.NotEmpty(t, r.Response.EmbeddingResponse.Data)
}

// TestParseIsBestEffort pins that parsing never fails loudly: every known route, fed bodies that are
// truncated, of the wrong shape, binary or empty, returns without panicking.
func TestParseIsBestEffort(t *testing.T) {
	routes := []Input{
		{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions"},
		{Provider: schemas.OpenAI, Model: "gpt-5", Method: "POST", Path: "/v1/responses"},
		{Provider: schemas.OpenAI, Model: "gpt-3.5-turbo-instruct", Method: "POST", Path: "/v1/completions"},
		{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Method: "POST", Path: "/v1/messages"},
		{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Method: "POST", Path: "/v1/messages/count_tokens"},
		{Provider: schemas.Anthropic, Model: "claude-2.1", Method: "POST", Path: "/v1/complete"},
		{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Method: "POST", Path: "/models/gemini-2.5-flash:generateContent"},
		{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Method: "POST", Path: "/models/gemini-2.5-flash:countTokens"},
		{Provider: schemas.Vertex, Model: "claude-sonnet-4-5", Method: "POST", Path: "/projects/p/locations/l/publishers/anthropic/models/claude-sonnet-4-5:rawPredict"},
		{Provider: schemas.OpenAI, Model: "text-embedding-3-small", Method: "POST", Path: "/v1/embeddings"},
		{Provider: schemas.OpenAI, Model: "gpt-4o-mini-tts", Method: "POST", Path: "/v1/audio/speech"},
		{Provider: schemas.OpenAI, Model: "whisper-1", Method: "POST", Path: "/v1/audio/transcriptions"},
		{Provider: schemas.OpenAI, Model: "gpt-image-1", Method: "POST", Path: "/v1/images/generations"},
		{Provider: schemas.Gemini, Model: "text-embedding-004", Method: "POST", Path: "/models/text-embedding-004:embedContent"},
		{Provider: schemas.Gemini, Model: "text-embedding-004", Method: "POST", Path: "/models/text-embedding-004:batchEmbedContents"},
		{Provider: schemas.Vertex, Model: "imagen-4.0-generate-001", Method: "POST", Path: "/projects/p/locations/l/publishers/google/models/imagen-4.0-generate-001:predict"},
	}
	bodies := []string{
		"", "{", "[]", "null", `"str"`, "42", `{"messages":"nope"}`, `{"contents":{}}`, `{"content":[{"type":"tool_use","input":"x"}]}`,
		`{"choices":[{"message":null}]}`, `{"candidates":[null]}`, `{"predictions":[null]}`, `{"requests":[null]}`, `{"data":[null]}`, `{"instances":[]}`, "\x00\xff\xfe", "data: {\n\n", "event: x\ndata: [1,2\n\n",
		anthropicStream[:len(anthropicStream)/2], openAIChatStream[:len(openAIChatStream)/3], openAIResponsesStream, geminiStreamChunk1,
	}
	for _, route := range routes {
		for _, stream := range []bool{false, true} {
			for _, body := range bodies {
				in := route
				in.StatusCode = 200
				in.Stream = stream
				in.RequestBody = []byte(body)
				in.ResponseBody = []byte(body)
				require.NotPanics(t, func() { _, _ = Parse(in) }, "route %s stream=%v body=%q", route.Path, stream, body)
			}
		}
	}
}

func TestSafeParseRecoversConverterPanic(t *testing.T) {
	out, err := safeParse(func() (*schemas.BifrostResponse, error) {
		var resp *schemas.BifrostChatResponse
		_ = resp.Choices // nil dereference, as a converter hitting an unexpected shape would
		return nil, nil
	})
	require.Nil(t, out)
	require.ErrorContains(t, err, "converter panicked")
}

// BenchmarkParse records the per-row cost the logging writer pays for a typical parsed call. It
// runs on the writer goroutine, never on the request path.
func BenchmarkParse(b *testing.B) {
	cases := []struct {
		name string
		in   Input
	}{
		{"openai_chat", Input{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(openAIChatResponse)}},
		{"anthropic_messages", Input{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Method: "POST", Path: "/v1/messages", StatusCode: 200, RequestBody: []byte(anthropicMessagesRequest), ResponseBody: []byte(anthropicMessagesResponse)}},
		{"gemini_generate", Input{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Method: "POST", Path: "/models/gemini-2.5-flash:generateContent", StatusCode: 200, RequestBody: []byte(geminiRequest), ResponseBody: []byte(geminiResponse)}},
		{"openai_chat_stream", Input{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, Stream: true, RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(openAIChatStream)}},
		{"anthropic_messages_stream", Input{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Method: "POST", Path: "/v1/messages", StatusCode: 200, Stream: true, RequestBody: []byte(anthropicMessagesRequest), ResponseBody: []byte(anthropicStream)}},
		{"unknown_route", Input{Provider: schemas.OpenAI, Method: "GET", Path: "/v1/models", StatusCode: 200, ResponseBody: []byte(`{"data":[]}`)}},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = Parse(c.in)
			}
		})
	}
}

// TestParseMethods pins every HTTP method on every parsed route of every provider: only POST
// generates, so any other method (a GET listing stored completions, a DELETE, a preflight OPTIONS)
// is not the typed request and parses to nothing, whatever its bodies look like.
func TestParseMethods(t *testing.T) {
	routes := []Input{
		{Provider: schemas.OpenAI, Model: "gpt-4o", Path: "/v1/chat/completions", RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(openAIChatResponse)},
		{Provider: schemas.OpenAI, Model: "gpt-5", Path: "/v1/responses", RequestBody: []byte(openAIResponsesRequest), ResponseBody: []byte(openAIResponsesResponse)},
		{Provider: schemas.OpenAI, Model: "gpt-5", Path: "/backend-api/codex/responses", RequestBody: []byte(openAIResponsesRequest), ResponseBody: []byte(openAIResponsesResponse)},
		{Provider: schemas.OpenAI, Model: "gpt-3.5-turbo-instruct", Path: "/v1/completions", RequestBody: []byte(openAITextRequest), ResponseBody: []byte(openAITextResponse)},
		{Provider: schemas.Azure, Model: "gpt-4o", Path: "/openai/deployments/gpt-4o/chat/completions", RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(openAIChatResponse)},
		{Provider: schemas.Azure, Model: "claude-sonnet-4-5", Path: "/anthropic/v1/messages", RequestBody: []byte(anthropicMessagesRequest), ResponseBody: []byte(anthropicMessagesResponse)},
		{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Path: "/v1/messages", RequestBody: []byte(anthropicMessagesRequest), ResponseBody: []byte(anthropicMessagesResponse)},
		{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Path: "/v1/messages/count_tokens", RequestBody: []byte(anthropicCountRequest), ResponseBody: []byte(anthropicCountResponse)},
		{Provider: schemas.Anthropic, Model: "claude-2.1", Path: "/v1/complete", RequestBody: []byte(anthropicCompleteRequest), ResponseBody: []byte(anthropicCompleteResponse)},
		{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Path: "/models/gemini-2.5-flash:generateContent", RequestBody: []byte(geminiRequest), ResponseBody: []byte(geminiResponse)},
		{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Path: "/models/gemini-2.5-flash:countTokens", RequestBody: []byte(geminiCountRequest), ResponseBody: []byte(geminiCountResponse)},
		{Provider: schemas.Vertex, Model: "gemini-2.5-flash", Path: "/projects/p/locations/l/publishers/google/models/gemini-2.5-flash:generateContent", RequestBody: []byte(geminiRequest), ResponseBody: []byte(geminiResponse)},
		{Provider: schemas.Vertex, Model: "claude-sonnet-4-5", Path: "/projects/p/locations/l/publishers/anthropic/models/claude-sonnet-4-5:rawPredict", RequestBody: []byte(anthropicMessagesRequest), ResponseBody: []byte(anthropicMessagesResponse)},
		{Provider: schemas.Vertex, Model: "mistral-large", Path: "/projects/p/locations/l/publishers/mistralai/models/mistral-large:rawPredict", RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(openAIChatResponse)},
		{Provider: schemas.Vertex, Model: "meta/llama-3.3-70b", Path: "/projects/p/locations/l/endpoints/openapi/chat/completions", RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(openAIChatResponse)},
	}
	for _, route := range routes {
		for _, method := range []string{"POST", "post", "GET", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", ""} {
			in := route
			in.Method = method
			in.StatusCode = 200
			t.Run(string(route.Provider)+" "+method+" "+route.Path, func(t *testing.T) {
				r, err := Parse(in)
				require.NoError(t, err)
				if strings.EqualFold(method, "POST") {
					require.NotNil(t, r, "POST is the typed request")
					require.NotNil(t, r.Request)
					return
				}
				require.Nil(t, r, "a %q call is not the typed request", method)
			})
		}
	}
}

const openAIChatStream = "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hel\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\"\"}}]},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\":\\\"Paris\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":7,\"total_tokens\":27}}\n\n" +
	"data: [DONE]\n\n"

const openAITextStream = "data: {\"id\":\"cmpl-1\",\"object\":\"text_completion\",\"created\":1,\"model\":\"gpt-3.5-turbo-instruct\",\"choices\":[{\"index\":0,\"text\":\" ti\",\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"cmpl-1\",\"object\":\"text_completion\",\"created\":1,\"model\":\"gpt-3.5-turbo-instruct\",\"choices\":[{\"index\":0,\"text\":\"me\",\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

const openAIResponsesStream = "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1,\"status\":\"in_progress\",\"model\":\"gpt-5\",\"output\":[]}}\n\n" +
	"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"h\"}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"i\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"gpt-5\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":1,\"total_tokens\":6}}}\n\n"

const anthropicStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":30,\"output_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: ping\ndata: {\"type\":\"ping\"}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Check\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ing.\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"get_weather\",\"input\":{}}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Paris\\\"}\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":12}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const geminiStreamChunk1 = `{"candidates":[{"content":{"role":"model","parts":[{"text":"Sun"}]},"index":0}],"modelVersion":"gemini-2.5-flash"}`
const geminiStreamChunk2 = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ny."}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12},"modelVersion":"gemini-2.5-flash"}`

func TestParseStream(t *testing.T) {
	anthropicCheck := func(t *testing.T, resp *schemas.BifrostResponse) {
		t.Helper()
		require.NotNil(t, resp.ResponsesResponse)
		text, calls := responsesTextAndCalls(resp.ResponsesResponse.Output)
		require.Equal(t, "Checking.", text)
		require.Len(t, calls, 1)
		require.Equal(t, "get_weather", *calls[0].Name)
		require.NotNil(t, calls[0].Arguments)
		require.JSONEq(t, `{"city":"Paris"}`, *calls[0].Arguments)
		require.Equal(t, "tool_calls", *resp.ResponsesResponse.StopReason)
	}
	geminiCheck := func(t *testing.T, resp *schemas.BifrostResponse) {
		t.Helper()
		require.NotNil(t, resp.ResponsesResponse)
		text, _ := responsesTextAndCalls(resp.ResponsesResponse.Output)
		require.Equal(t, "Sunny.", text)
		require.Equal(t, "stop", *resp.ResponsesResponse.StopReason)
	}

	tests := []struct {
		name  string
		in    Input
		check func(t *testing.T, resp *schemas.BifrostResponse)
	}{
		{
			name: "openai chat completions stream",
			in:   Input{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, Stream: true, RequestBody: []byte(openAIChatRequest), ResponseBody: []byte(openAIChatStream)},
			check: func(t *testing.T, resp *schemas.BifrostResponse) {
				require.NotNil(t, resp.ChatResponse)
				require.Len(t, resp.ChatResponse.Choices, 1)
				choice := resp.ChatResponse.Choices[0]
				require.Equal(t, "tool_calls", *choice.FinishReason)
				msg := choice.ChatNonStreamResponseChoice.Message
				require.Equal(t, "Hello", *msg.Content.ContentStr)
				require.Len(t, msg.ChatAssistantMessage.ToolCalls, 1)
				require.Equal(t, "get_weather", *msg.ChatAssistantMessage.ToolCalls[0].Function.Name)
				require.JSONEq(t, `{"city":"Paris"}`, msg.ChatAssistantMessage.ToolCalls[0].Function.Arguments)
				require.NotNil(t, resp.ChatResponse.Usage)
				require.Equal(t, 27, resp.ChatResponse.Usage.TotalTokens)
			},
		},
		{
			name: "openai legacy completions stream",
			in:   Input{Provider: schemas.OpenAI, Model: "gpt-3.5-turbo-instruct", Method: "POST", Path: "/v1/completions", StatusCode: 200, Stream: true, RequestBody: []byte(openAITextRequest), ResponseBody: []byte(openAITextStream)},
			check: func(t *testing.T, resp *schemas.BifrostResponse) {
				require.NotNil(t, resp.TextCompletionResponse)
				choice := resp.TextCompletionResponse.Choices[0]
				require.Equal(t, " time", *choice.TextCompletionResponseChoice.Text)
				require.Equal(t, "stop", *choice.FinishReason)
			},
		},
		{
			name: "openai responses stream",
			in:   Input{Provider: schemas.OpenAI, Model: "gpt-5", Method: "POST", Path: "/v1/responses", StatusCode: 200, Stream: true, RequestBody: []byte(openAIResponsesRequest), ResponseBody: []byte(openAIResponsesStream)},
			check: func(t *testing.T, resp *schemas.BifrostResponse) {
				require.NotNil(t, resp.ResponsesResponse)
				text, _ := responsesTextAndCalls(resp.ResponsesResponse.Output)
				require.Equal(t, "hi", text)
				require.Equal(t, "completed", *resp.ResponsesResponse.Status)
				require.NotNil(t, resp.ResponsesResponse.Usage)
			},
		},
		{
			name: "chatgpt codex responses stream",
			in:   Input{Provider: schemas.OpenAI, Model: "gpt-5", Method: "POST", Path: "/backend-api/codex/responses", StatusCode: 200, Stream: true, RequestBody: []byte(openAIResponsesRequest), ResponseBody: []byte(openAIResponsesStream)},
			check: func(t *testing.T, resp *schemas.BifrostResponse) {
				text, _ := responsesTextAndCalls(resp.ResponsesResponse.Output)
				require.Equal(t, "hi", text)
			},
		},
		{
			name:  "anthropic messages stream",
			in:    Input{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Method: "POST", Path: "/v1/messages", StatusCode: 200, Stream: true, RequestBody: []byte(anthropicMessagesRequest), ResponseBody: []byte(anthropicStream)},
			check: anthropicCheck,
		},
		{
			name:  "vertex claude streamRawPredict",
			in:    Input{Provider: schemas.Vertex, Model: "claude-sonnet-4-5", Method: "POST", Path: "/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-5:streamRawPredict", StatusCode: 200, Stream: true, RequestBody: []byte(anthropicMessagesRequest), ResponseBody: []byte(anthropicStream)},
			check: anthropicCheck,
		},
		{
			name:  "gemini streamGenerateContent alt=sse",
			in:    Input{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Method: "POST", Path: "/models/gemini-2.5-flash:streamGenerateContent?alt=sse", StatusCode: 200, Stream: true, RequestBody: []byte(geminiRequest), ResponseBody: []byte("data: " + geminiStreamChunk1 + "\r\n\r\ndata: " + geminiStreamChunk2 + "\r\n\r\n")},
			check: geminiCheck,
		},
		{
			name:  "gemini streamGenerateContent json array",
			in:    Input{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Method: "POST", Path: "/models/gemini-2.5-flash:streamGenerateContent", StatusCode: 200, Stream: true, RequestBody: []byte(geminiRequest), ResponseBody: []byte("[" + geminiStreamChunk1 + ",\n" + geminiStreamChunk2 + "]")},
			check: geminiCheck,
		},
		{
			name:  "vertex gemini streamGenerateContent",
			in:    Input{Provider: schemas.Vertex, Model: "gemini-2.5-flash", Method: "POST", Path: "/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent?alt=sse", StatusCode: 200, Stream: true, RequestBody: []byte(geminiRequest), ResponseBody: []byte("data: " + geminiStreamChunk1 + "\n\ndata: " + geminiStreamChunk2 + "\n\n")},
			check: geminiCheck,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Parse(tt.in)
			require.NoError(t, err)
			require.NotNil(t, r)
			require.NotNil(t, r.Request)
			require.NotNil(t, r.Response)
			tt.check(t, r.Response)
		})
	}
}

// TestParseStreamTruncated pins that a stream cut off mid-way still yields what arrived, alongside
// an error describing the bad frame.
func TestParseStreamTruncated(t *testing.T) {
	body := openAIChatStream[:strings.Index(openAIChatStream, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\"")] + "data: {\"id\":\"chatcmpl-1\",\"choi\n\n"
	r, err := Parse(Input{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, Stream: true, ResponseBody: []byte(body)})
	require.Error(t, err)
	require.NotNil(t, r)
	require.NotNil(t, r.Response)
	require.Equal(t, "Hello", *r.Response.ChatResponse.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)
}

func responsesTextAndCalls(output []schemas.ResponsesMessage) (string, []*schemas.ResponsesToolMessage) {
	var text strings.Builder
	var calls []*schemas.ResponsesToolMessage
	for _, item := range output {
		if item.Type == nil {
			continue
		}
		switch *item.Type {
		case schemas.ResponsesMessageTypeMessage:
			if item.Content == nil {
				continue
			}
			if item.Content.ContentStr != nil {
				text.WriteString(*item.Content.ContentStr)
			}
			for _, block := range item.Content.ContentBlocks {
				if block.Text != nil {
					text.WriteString(*block.Text)
				}
			}
		case schemas.ResponsesMessageTypeFunctionCall:
			calls = append(calls, item.ResponsesToolMessage)
		}
	}
	return text.String(), calls
}

// TestParseStreamMultipleChoices pins n>1 streams: OpenAI interleaves events for different choice
// indexes, and each choice must assemble from its own deltas and keep its own finish reason.
func TestParseStreamMultipleChoices(t *testing.T) {
	chat := "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"A\"}}]}\n\n" +
		"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":1,\"delta\":{\"role\":\"assistant\",\"content\":\"B\"}}]}\n\n" +
		"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"0\"}},{\"index\":1,\"delta\":{\"content\":\"1\"}}]}\n\n" +
		"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":1,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
		"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	r, err := Parse(Input{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, Stream: true, ResponseBody: []byte(chat)})
	require.NoError(t, err)
	choices := r.Response.ChatResponse.Choices
	require.Len(t, choices, 2)
	require.Equal(t, 0, choices[0].Index)
	require.Equal(t, "A0", *choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)
	require.Equal(t, "stop", *choices[0].FinishReason)
	require.Equal(t, 1, choices[1].Index)
	require.Equal(t, "B1", *choices[1].ChatNonStreamResponseChoice.Message.Content.ContentStr)
	require.Equal(t, "length", *choices[1].FinishReason)

	text := "data: {\"id\":\"t\",\"object\":\"text_completion\",\"choices\":[{\"index\":1,\"text\":\"B\"}]}\n\n" +
		"data: {\"id\":\"t\",\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"A\"}]}\n\n" +
		"data: {\"id\":\"t\",\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"0\",\"finish_reason\":\"stop\"},{\"index\":1,\"text\":\"1\",\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"
	r, err = Parse(Input{Provider: schemas.OpenAI, Model: "gpt-3.5-turbo-instruct", Method: "POST", Path: "/v1/completions", StatusCode: 200, Stream: true, ResponseBody: []byte(text)})
	require.NoError(t, err)
	tc := r.Response.TextCompletionResponse.Choices
	require.Len(t, tc, 2)
	require.Equal(t, 0, tc[0].Index)
	require.Equal(t, "A0", *tc[0].TextCompletionResponseChoice.Text)
	require.Equal(t, "stop", *tc[0].FinishReason)
	require.Equal(t, 1, tc[1].Index)
	require.Equal(t, "B1", *tc[1].TextCompletionResponseChoice.Text)
	require.Equal(t, "length", *tc[1].FinishReason)
}

// TestParseStreamCutKeepsWhatArrived pins two truncated streams: an Anthropic stream cut after the
// tool input deltas but before content_block_stop keeps the arguments (the converter emits one
// argument delta per input_json_delta, nothing is held back), and a Gemini stream cut before its
// finishReason chunk keeps its text without inventing a terminal status the upstream never sent.
func TestParseStreamCutKeepsWhatArrived(t *testing.T) {
	cut := anthropicStream[:strings.Index(anthropicStream, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}")]
	r, err := Parse(Input{Provider: schemas.Anthropic, Model: "claude-sonnet-4-5", Method: "POST", Path: "/v1/messages", StatusCode: 200, Stream: true, ResponseBody: []byte(cut)})
	require.NoError(t, err)
	_, calls := responsesTextAndCalls(r.Response.ResponsesResponse.Output)
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].Arguments)
	require.JSONEq(t, `{"city":"Paris"}`, *calls[0].Arguments)

	r, err = Parse(Input{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Method: "POST", Path: "/models/gemini-2.5-flash:streamGenerateContent?alt=sse", StatusCode: 200, Stream: true, ResponseBody: []byte("data: " + geminiStreamChunk1 + "\n\n")})
	require.NoError(t, err)
	text, _ := responsesTextAndCalls(r.Response.ResponsesResponse.Output)
	require.Equal(t, "Sun", text)
	require.Nil(t, r.Response.ResponsesResponse.Status, "a cut stream is not reported as completed")
	require.Nil(t, r.Response.ResponsesResponse.StopReason)
}

// TestParseGeminiJSONArrayStreamCut pins that a Gemini alt=json stream (one JSON array of chunks)
// cut part-way, or followed by trailing data, keeps every chunk that arrived whole, alongside an
// error naming the bad part, just like the SSE path.
func TestParseGeminiJSONArrayStreamCut(t *testing.T) {
	in := Input{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Method: "POST", Path: "/models/gemini-2.5-flash:streamGenerateContent", StatusCode: 200, Stream: true}

	in.ResponseBody = []byte("[" + geminiStreamChunk1 + ",\n" + geminiStreamChunk2[:40])
	r, err := Parse(in)
	require.Error(t, err)
	require.NotNil(t, r)
	require.NotNil(t, r.Response)
	text, _ := responsesTextAndCalls(r.Response.ResponsesResponse.Output)
	require.Equal(t, "Sun", text)

	in.ResponseBody = []byte("[" + geminiStreamChunk1 + "," + geminiStreamChunk2 + "] trailing")
	r, err = Parse(in)
	require.Error(t, err)
	require.NotNil(t, r)
	text, _ = responsesTextAndCalls(r.Response.ResponsesResponse.Output)
	require.Equal(t, "Sunny.", text)

	in.ResponseBody = []byte("[{\"candidates\": [")
	r, err = Parse(in)
	require.Error(t, err)
	require.Nil(t, r, "nothing decoded, nothing to keep")
}

func TestInferRequestType(t *testing.T) {
	tests := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		method   string
		path     string
		stream   bool
		want     schemas.RequestType
	}{
		{"openai chat", schemas.OpenAI, "gpt-4o", "POST", "/v1/chat/completions", false, schemas.ChatCompletionRequest},
		{"openai chat stream", schemas.OpenAI, "gpt-4o", "POST", "/v1/chat/completions", true, schemas.ChatCompletionStreamRequest},
		{"codex responses stream", schemas.OpenAI, "gpt-5", "POST", "/backend-api/codex/responses", true, schemas.ResponsesStreamRequest},
		{"openai embeddings stream has no stream variant", schemas.OpenAI, "text-embedding-3-small", "POST", "/v1/embeddings", true, schemas.EmbeddingRequest},
		{"anthropic messages", schemas.Anthropic, "claude-sonnet-4-5", "POST", "/v1/messages", false, schemas.ResponsesRequest},
		{"azure foundry anthropic", schemas.Azure, "claude-sonnet-4-5", "POST", "/anthropic/v1/messages", false, schemas.ResponsesRequest},
		{"vertex claude streamRawPredict", schemas.Vertex, "claude-sonnet-4-5", "POST", "/projects/p/locations/l/publishers/anthropic/models/claude-sonnet-4-5:streamRawPredict", true, schemas.ResponsesStreamRequest},
		{"vertex mistral", schemas.Vertex, "mistral-large", "POST", "/projects/p/locations/l/publishers/mistralai/models/mistral-large:rawPredict", false, schemas.ChatCompletionRequest},
		{"gemini generate stream", schemas.Gemini, "gemini-2.5-flash", "POST", "/models/gemini-2.5-flash:streamGenerateContent?alt=sse", true, schemas.ResponsesStreamRequest},
		{"openai fine-tuning has no typed equivalent", schemas.OpenAI, "", "POST", "/v1/fine-tuning/jobs", false, ""},
		{"bedrock agent route", schemas.Bedrock, "", "POST", "/agents/a/agentAliases/b/sessions/c/text", false, ""},
		{"custom provider name is not a wire format", "my-openai", "gpt-4o", "POST", "/v1/chat/completions", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, InferRequestType(tt.provider, tt.model, tt.method, tt.path, tt.stream))
		})
	}
}

func BenchmarkInferRequestType(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = InferRequestType(schemas.OpenAI, "gpt-4o", "POST", "/v1/chat/completions", true)
	}
}

// TestInferRequestTypeMethods pins row typing under every HTTP method on every typed route: only POST
// types a passthrough row, every other method leaves it passthrough, and the one method-dependent
// collection, OpenAI's /containers, is a create on POST and a list on GET.
func TestInferRequestTypeMethods(t *testing.T) {
	routes := []struct {
		provider schemas.ModelProvider
		model    string
		path     string
		onPost   schemas.RequestType
	}{
		{schemas.OpenAI, "gpt-4o", "/v1/chat/completions", schemas.ChatCompletionRequest},
		{schemas.OpenAI, "gpt-5", "/v1/responses", schemas.ResponsesRequest},
		{schemas.OpenAI, "text-embedding-3-small", "/v1/embeddings", schemas.EmbeddingRequest},
		{schemas.OpenAI, "gpt-4o-mini-tts", "/v1/audio/speech", schemas.SpeechRequest},
		{schemas.OpenAI, "whisper-1", "/v1/audio/transcriptions", schemas.TranscriptionRequest},
		{schemas.OpenAI, "gpt-image-1", "/v1/images/edits", schemas.ImageEditRequest},
		{schemas.OpenAI, "", "/v1/containers", schemas.ContainerCreateRequest},
		{schemas.Azure, "gpt-4o", "/openai/deployments/gpt-4o/chat/completions", schemas.ChatCompletionRequest},
		{schemas.Azure, "claude-sonnet-4-5", "/anthropic/v1/messages", schemas.ResponsesRequest},
		{schemas.Anthropic, "claude-sonnet-4-5", "/v1/messages", schemas.ResponsesRequest},
		{schemas.Anthropic, "claude-sonnet-4-5", "/v1/messages/count_tokens", schemas.CountTokensRequest},
		{schemas.Gemini, "gemini-2.5-flash", "/models/gemini-2.5-flash:generateContent", schemas.ResponsesRequest},
		{schemas.Gemini, "gemini-embedding-001", "/models/gemini-embedding-001:embedContent", schemas.EmbeddingRequest},
		{schemas.Vertex, "text-embedding-005", "/projects/p/locations/l/publishers/google/models/text-embedding-005:predict", schemas.EmbeddingRequest},
		{schemas.Vertex, "claude-sonnet-4-5", "/projects/p/locations/l/publishers/anthropic/models/claude-sonnet-4-5:rawPredict", schemas.ResponsesRequest},
	}
	for _, route := range routes {
		for _, method := range []string{"POST", "post", "GET", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", ""} {
			want := schemas.RequestType("")
			switch {
			case strings.EqualFold(method, "POST"):
				want = route.onPost
			case method == "GET" && route.path == "/v1/containers":
				want = schemas.ContainerListRequest
			}
			t.Run(string(route.provider)+" "+method+" "+route.path, func(t *testing.T) {
				require.Equal(t, want, InferRequestType(route.provider, route.model, method, route.path, false))
			})
		}
	}
}

// TestClassify pins the API shape and request kind of each route family, including providers whose route speaks
// another provider's API (Claude on Azure and Vertex speaks Anthropic, Mistral on Vertex speaks OpenAI, Codex on the
// ChatGPT backend speaks Responses). Routes without a typed equivalent, and non-POST calls, return the zero Route.
func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		provider schemas.ModelProvider
		method   string
		path     string
		want     Route
	}{
		{"openai chat", schemas.OpenAI, "POST", "/v1/chat/completions", Route{schemas.OpenAI, schemas.ChatCompletionRequest}},
		{"openai responses", schemas.OpenAI, "POST", "/v1/responses", Route{schemas.OpenAI, schemas.ResponsesRequest}},
		{"codex responses", schemas.OpenAI, "POST", "/backend-api/codex/responses", Route{schemas.OpenAI, schemas.ResponsesRequest}},
		{"openai text completions", schemas.OpenAI, "POST", "/v1/completions", Route{schemas.OpenAI, schemas.TextCompletionRequest}},
		{"anthropic messages", schemas.Anthropic, "POST", "/v1/messages", Route{schemas.Anthropic, schemas.ResponsesRequest}},
		{"anthropic count tokens", schemas.Anthropic, "POST", "/v1/messages/count_tokens", Route{schemas.Anthropic, schemas.CountTokensRequest}},
		{"azure claude", schemas.Azure, "POST", "/anthropic/v1/messages", Route{schemas.Anthropic, schemas.ResponsesRequest}},
		{"azure openai chat", schemas.Azure, "POST", "/openai/deployments/gpt-4o/chat/completions", Route{schemas.OpenAI, schemas.ChatCompletionRequest}},
		{"vertex claude", schemas.Vertex, "POST", "/projects/p/locations/l/publishers/anthropic/models/claude-sonnet-4-5:rawPredict", Route{schemas.Anthropic, schemas.ResponsesRequest}},
		{"vertex mistral", schemas.Vertex, "POST", "/projects/p/locations/l/publishers/mistralai/models/mistral-large:rawPredict", Route{schemas.OpenAI, schemas.ChatCompletionRequest}},
		{"gemini stream", schemas.Gemini, "POST", "/models/gemini-2.5-flash:streamGenerateContent", Route{schemas.Gemini, schemas.ResponsesRequest}},
		{"gemini count tokens", schemas.Gemini, "POST", "/models/gemini-2.5-flash:countTokens", Route{schemas.Gemini, schemas.CountTokensRequest}},
		{"openai compaction", schemas.OpenAI, "POST", "/v1/responses/compact", Route{schemas.OpenAI, schemas.CompactionRequest}},
		{"azure compaction", schemas.Azure, "POST", "/openai/v1/responses/compact", Route{schemas.OpenAI, schemas.CompactionRequest}},
		{"gemini cached content", schemas.Gemini, "POST", "/v1beta/cachedContents", Route{schemas.Gemini, schemas.CachedContentCreateRequest}},
		{"vertex cached content", schemas.Vertex, "POST", "/projects/p/locations/l/cachedContents", Route{schemas.Gemini, schemas.CachedContentCreateRequest}},
		{"gemini cached content list", schemas.Gemini, "GET", "/v1beta/cachedContents", Route{}},
		{"gemini interactions create", schemas.Gemini, "POST", "/v1beta/interactions", Route{WireGeminiInteractions, schemas.ResponsesRequest}},
		{"gemini interactions poll", schemas.Gemini, "GET", "/v1beta/interactions/v1_abc", Route{WireGeminiInteractions, schemas.ResponsesRetrieveRequest}},
		{"gemini interactions delete", schemas.Gemini, "DELETE", "/v1beta/interactions/v1_abc", Route{}},
		{"gemini interactions cancel", schemas.Gemini, "POST", "/v1beta/interactions/v1_abc/cancel", Route{}},
		{"vertex interactions create", schemas.Vertex, "POST", "/projects/p/locations/l/interactions", Route{WireGeminiInteractions, schemas.ResponsesRequest}},
		{"runware task endpoint", schemas.Runware, "POST", "/v1", Route{Wire: schemas.Runware}},
		{"runware root", schemas.Runware, "POST", "", Route{Wire: schemas.Runware}},
		{"runware openai chat", schemas.Runware, "POST", "/v1/chat/completions", Route{schemas.OpenAI, schemas.ChatCompletionRequest}},
		{"runware anthropic messages", schemas.Runware, "POST", "/v1/messages", Route{schemas.Anthropic, schemas.ResponsesRequest}},
		{"runware unknown path", schemas.Runware, "POST", "/v1/files", Route{}},
		{"sagemaker invocations", schemas.SageMaker, "POST", "/endpoints/my-tgi/invocations", Route{Wire: schemas.SageMaker}},
		{"sagemaker stream", schemas.SageMaker, "POST", "/endpoints/my-tgi/invocations-response-stream", Route{Wire: schemas.SageMaker}},
		{"bedrock invoke agent", schemas.Bedrock, "POST", "/agents/AGENT1/agentAliases/ALIAS1/sessions/s-1/text", Route{Wire: schemas.Bedrock}},
		{"bedrock knowledge base retrieve", schemas.Bedrock, "POST", "/knowledgebases/KB1/retrieve", Route{}},
		{"untyped route", schemas.Anthropic, "POST", "/v1/files", Route{}},
		{"non-POST call", schemas.Anthropic, "GET", "/v1/messages", Route{}},
		{"provider without a parser", schemas.Bedrock, "POST", "/model/anthropic.claude-sonnet-4-5/invoke", Route{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, Classify(tc.provider, "", tc.method, tc.path))
		})
	}
}

// TestRouteStreamFormat verifies a route has a stream format exactly when Parse can assemble its stream.
func TestRouteStreamFormat(t *testing.T) {
	wires := map[schemas.ModelProvider]wireFormat{schemas.OpenAI: wireOpenAI, schemas.Anthropic: wireAnthropic, schemas.Gemini: wireGemini}
	cases := []struct {
		route Route
		want  schemas.RawResponseFormat
	}{
		{Route{schemas.OpenAI, schemas.ChatCompletionRequest}, schemas.RawResponseFormatOpenAIChatCompletionChunk},
		{Route{schemas.OpenAI, schemas.TextCompletionRequest}, schemas.RawResponseFormatOpenAITextCompletionChunk},
		{Route{schemas.OpenAI, schemas.ResponsesRequest}, schemas.RawResponseFormatOpenAIResponsesStreamEvent},
		{Route{schemas.Anthropic, schemas.ResponsesRequest}, schemas.RawResponseFormatAnthropicMessagesStreamEvent},
		{Route{schemas.Gemini, schemas.ResponsesRequest}, schemas.RawResponseFormatGeminiStreamChunk},
		{Route{schemas.OpenAI, schemas.ImageGenerationRequest}, ""},
		{Route{schemas.OpenAI, schemas.SpeechRequest}, ""},
		{Route{schemas.Anthropic, schemas.TextCompletionRequest}, ""},
		{Route{}, ""},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, tc.route.StreamFormat(), tc.route)
		_, err := parseStreamResponse(wires[tc.route.Wire], tc.route.RequestType, nil)
		require.Equal(t, tc.want != "", err != nil, "Parse assembles the stream of %v exactly when it has a format", tc.route)
	}
}

// TestParseStreamEndingOnUpstreamError verifies a stream the upstream ended with an error event is reported as
// ErrUpstreamStreamFailed, with everything that streamed before it.
func TestParseStreamEndingOnUpstreamError(t *testing.T) {
	body := anthropicStream[:strings.Index(anthropicStream, "event: content_block_stop")] +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"

	result, err := Parse(Input{Provider: schemas.Anthropic, Method: "POST", Path: "/v1/messages", StatusCode: 200, Stream: true, ResponseBody: []byte(body)})

	require.True(t, errors.Is(err, ErrUpstreamStreamFailed), err)
	require.NotNil(t, result.Response)
	text, _ := responsesTextAndCalls(result.Response.ResponsesResponse.Output)
	require.Equal(t, "Checking.", text)
}

// TestRedactStreamBody verifies values split across events are redacted in place while framing, events without text,
// reasoning, and every other byte are kept.
func TestRedactStreamBody(t *testing.T) {
	replacements := map[string]string{"alex@example.com": "[EMAIL-1]"}
	cases := []struct {
		name   string
		format schemas.RawResponseFormat
		body   string
		want   *strings.Replacer
	}{
		{
			name:   "openai chat with comments, CRLF, and data: without a space",
			format: schemas.RawResponseFormatOpenAIChatCompletionChunk,
			body: ": keep-alive\r\n\r\n" +
				`data: {"choices":[{"index":0,"delta":{"content":"mail alex@"}}]}` + "\r\n\r\n" +
				`data:{"choices":[{"index":0,"delta":{"content":"exam"}}]}` + "\n\n" +
				"data: not json\n\n" +
				`data: {"choices":[{"index":0,"delta":{"content":"ple.com now"}}]}` + "\n\n" +
				"data: [DONE]\n\n",
			want: strings.NewReplacer(`"mail alex@"`, `"mail "`, `"exam"`, `""`, `"ple.com now"`, `"[EMAIL-1] now"`),
		},
		{
			name:   "anthropic text with thinking and signature untouched",
			format: schemas.RawResponseFormatAnthropicMessagesStreamEvent,
			body: "event: ping\ndata: {\"type\":\"ping\"}\n\n" +
				"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"user is alex@example.com"}}` + "\n\n" +
				"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"c2lnbmF0dXJl"}}` + "\n\n" +
				"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"mail alex@exa"}}` + "\n\n" +
				"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"mple.com"}}` + "\n\n",
			want: strings.NewReplacer(`"mail alex@exa"`, `"mail "`, `"mple.com"`, `"[EMAIL-1]"`),
		},
		{
			name:   "responses deltas and closing events with reasoning untouched",
			format: schemas.RawResponseFormatOpenAIResponsesStreamEvent,
			body: "event: response.reasoning_summary_text.delta\n" + `data: {"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"alex@example.com"}` + "\n\n" +
				"event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"mail alex@"}` + "\n\n" +
				"event: response.output_text.delta\n" + `data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"example.com"}` + "\n\n" +
				"event: response.output_text.done\n" + `data: {"type":"response.output_text.done","output_index":1,"content_index":0,"text":"mail alex@example.com"}` + "\n\n",
			want: strings.NewReplacer(`"mail alex@"`, `"mail "`, `"delta":"example.com"`, `"delta":"[EMAIL-1]"`, `"text":"mail alex@example.com"`, `"text":"mail [EMAIL-1]"`),
		},
		{
			name:   "gemini pretty-printed JSON array",
			format: schemas.RawResponseFormatGeminiStreamChunk,
			body: "[{\n  \"candidates\": [{\"content\": {\"role\": \"model\", \"parts\": [{\"text\": \"mail alex@\"}]}}]\n}\r\n,\r\n" +
				"{\n  \"candidates\": [{\"content\": {\"role\": \"model\", \"parts\": [{\"text\": \"example.com\"}]}, \"finishReason\": \"STOP\"}]\n}\n]",
			want: strings.NewReplacer(`"mail alex@"`, `"mail "`, `"example.com"`, `"[EMAIL-1]"`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			redacted, ok := RedactStreamBody([]byte(tc.body), tc.format, replacements)

			require.True(t, ok)
			require.Equal(t, tc.want.Replace(tc.body), string(redacted))
		})
	}
}

// TestRedactStreamBodyReturnsBodyItCannotChange verifies a body with nothing to redact comes back as the same slice,
// and an event whose data spans several lines is reported unhandled.
func TestRedactStreamBodyReturnsBodyItCannotChange(t *testing.T) {
	replacements := map[string]string{"alex@example.com": "[EMAIL-1]"}
	clean := []byte(`data: {"choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n\n")
	redacted, ok := RedactStreamBody(clean, schemas.RawResponseFormatOpenAIChatCompletionChunk, replacements)
	require.True(t, ok)
	require.Same(t, &clean[0], &redacted[0])

	multiline := []byte("data: {\"choices\":[{\"index\":0,\ndata: \"delta\":{\"content\":\"alex@example.com\"}}]}\n\n")
	redacted, ok = RedactStreamBody(multiline, schemas.RawResponseFormatOpenAIChatCompletionChunk, replacements)
	require.False(t, ok)
	require.Equal(t, multiline, redacted)
}

// BenchmarkRedactStreamBody measures redacting a 1 MB chat stream with one value split across two events.
func BenchmarkRedactStreamBody(b *testing.B) {
	var body strings.Builder
	for body.Len() < 1<<20 {
		body.WriteString(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"some streamed words "}}]}` + "\n\n")
	}
	body.WriteString(`data: {"choices":[{"index":0,"delta":{"content":"mail alex@"}}]}` + "\n\n")
	body.WriteString(`data: {"choices":[{"index":0,"delta":{"content":"example.com"}}]}` + "\n\ndata: [DONE]\n\n")
	raw := []byte(body.String())
	replacements := map[string]string{"alex@example.com": "[EMAIL-1]"}
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := RedactStreamBody(raw, schemas.RawResponseFormatOpenAIChatCompletionChunk, replacements); !ok {
			b.Fatal("unhandled")
		}
	}
}

// TestStreamRedactionMatchesWhatTheReaderReads verifies, for every stream shape the SageMaker and Interactions readers
// accept, that RedactStreamBody rewrites exactly the text Parse hands guardrails: a value the reader read, however the
// stream split it, reads back with only that value replaced. A value in a part neither reads (a thought, a special
// token) is left alone by both. Guardrails run this same check before releasing a rewritten stream. Synthetic payloads.
func TestStreamRedactionMatchesWhatTheReaderReads(t *testing.T) {
	const secret = "alice@example.com"
	sse := func(events ...string) string {
		var b strings.Builder
		for _, event := range events {
			b.WriteString("data:" + event + "\n\n")
		}
		return b.String()
	}
	lines := func(events ...string) string { return strings.Join(events, "\n") + "\n" }
	steps := func(events ...string) string {
		var b strings.Builder
		for _, event := range events {
			b.WriteString("event: " + gjson.Get(event, "event_type").String() + "\ndata: " + event + "\n\n")
		}
		return b.String()
	}
	sageMaker := Input{Provider: schemas.SageMaker, Method: "POST", Path: "/endpoints/e/invocations-response-stream"}
	gemini := Input{Provider: schemas.Gemini, Method: "POST", Path: "/v1beta/interactions"}
	vertex := Input{Provider: schemas.Vertex, Method: "POST", Path: "/v1beta1/projects/p/locations/global/interactions"}
	tgiToken1 := `{"index":1,"token":{"id":1,"text":"mail alice@","logprob":-0.1,"special":false},"generated_text":null,"details":null}`
	tgiToken2 := `{"index":2,"token":{"id":2,"text":"example.com","logprob":-0.1,"special":false},"generated_text":null,"details":null}`
	callStart := `{"index":0,"step":{"type":"function_call","id":"c1","name":"send"},"event_type":"step.start"}`
	cases := []struct {
		name string
		in   Input
		body string
		mask string
	}{
		{"tgi tokens and closing text", sageMaker, sse(tgiToken1, tgiToken2,
			`{"index":3,"token":{"id":0,"text":"</s>","logprob":0,"special":true},"generated_text":"mail alice@example.com","details":{"finish_reason":"eos_token","generated_tokens":3}}`), "[EMAIL]"},
		{"tgi tokens cut before the closing text", sageMaker, sse(tgiToken1, tgiToken2), "[EMAIL]"},
		{"tgi failure after tokens", sageMaker, sse(tgiToken1, tgiToken2, `{"error":"Request failed during generation","error_type":"generation"}`), "[EMAIL]"},
		{"tgi chat labelled text_completion", sageMaker, sse(
			`{"object":"text_completion","choices":[{"index":0,"delta":{"role":"assistant","content":"mail alice@"}}]}`,
			`{"object":"text_completion","choices":[{"index":0,"delta":{"content":"example.com"},"finish_reason":"stop"}]}`), "[EMAIL]"},
		{"openai chat tool call arguments", sageMaker, sse(
			`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"send","arguments":"{\"to\":\"alice@"}}]}}]}`,
			`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"example.com\"}"}}]},"finish_reason":"tool_calls"}]}`), "[EMAIL]"},
		{"openai completion chunks", sageMaker, sse(
			`{"object":"text_completion","choices":[{"index":0,"text":"mail alice@"}]}`,
			`{"object":"text_completion","choices":[{"index":0,"text":"example.com","finish_reason":"stop"}]}`), "[EMAIL]"},
		{"lmi tokens and closing text", sageMaker, lines(
			`{"token": {"id": 1, "text": "mail alice@", "log_prob": -0.1}}`,
			`{"token": {"id": 2, "text": "example.com", "log_prob": -0.1}, "generated_text": "mail alice@example.com"}`), "[EMAIL]"},
		{"lmi special_token between tokens", sageMaker, lines(
			`{"token": {"text": "mail alice@"}}`, `{"token": {"text": "<|eot_id|>", "special_token": true}}`, `{"token": {"text": "example.com"}}`), "[EMAIL]"},
		{"lmi closing text with the prompt, deleted by redaction", sageMaker, lines(
			`{"token": {"id": 1, "text": "example.com"}, "generated_text": "alice@example.com"}`), ""},
		{"lmi outputs", sageMaker, lines(`{"outputs": ["mail alice@"]}`, `{"outputs": ["example.com"]}`), "[EMAIL]"},
		{"lmi outputs in the second prompt", sageMaker, lines(`{"outputs": ["hi", "mail alice@"]}`, `{"outputs": [" there", "example.com"]}`), "[EMAIL]"},
		{"lmi outputs with a frame that is not a list", sageMaker, lines(
			`{"outputs": ["mail alice@"]}`, `{"outputs": "metadata"}`, `{"outputs": ["example.com"]}`), "[EMAIL]"},
		{"lmi outputs with items that are not text", sageMaker, lines(
			`{"outputs": [{"label": "x"}, "mail alice@"]}`, `{"outputs": [{"label": "y"}, "example.com"]}`), "[EMAIL]"},
		{"whole chat reply on the stream route", sageMaker, lines(
			`{"object":"chat.completion","id":"","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"mail alice@example.com"},"finish_reason":"stop"}]}`), "[EMAIL]"},
		{"whole chat reply with a refusal on the stream route", sageMaker, lines(
			`{"object":"chat.completion","id":"","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"refusal":"cannot mail alice@example.com"},"finish_reason":"stop"}]}`), "[EMAIL]"},
		{"whole labelled tgi reply with a tool call on the stream route", sageMaker, lines(
			`{"object":"text_completion","id":"","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok","tool_calls":[{"id":"c1","type":"function","function":{"name":"send","arguments":"{\"to\":\"alice@example.com\"}"}}]},"finish_reason":"stop"}]}`), "[EMAIL]"},
		{"interactions model output", gemini, steps(
			`{"index":0,"step":{"type":"model_output","content":[{"type":"text","text":"mail ali"}]},"event_type":"step.start"}`,
			`{"index":0,"delta":{"type":"text","text":"ce@example.com"},"event_type":"step.delta"}`), "[EMAIL]"},
		{"interactions function call arguments", gemini, steps(callStart,
			`{"index":0,"delta":{"type":"arguments_delta","arguments":"{\"to\":\"alice@"},"event_type":"step.delta"}`,
			`{"index":0,"delta":{"type":"arguments_delta","arguments":"example.com\"}"},"event_type":"step.delta"}`), "[EMAIL]"},
		{"vertex interactions partial_arguments", vertex, steps(callStart,
			`{"index":0,"delta":{"type":"arguments_delta","partial_arguments":"{\"to\":\"alice@"},"event_type":"step.delta"}`,
			`{"index":0,"delta":{"type":"arguments_delta","partial_arguments":"example.com\"}"},"event_type":"step.delta"}`), "[EMAIL]"},
		{"interactions function call arguments on step.start", gemini, steps(
			`{"index":0,"step":{"type":"function_call","id":"c1","name":"send","arguments":{"to":"alice@example.com"}},"event_type":"step.start"}`), "[EMAIL]"},
		{"interactions empty start arguments then deltas", gemini, steps(
			`{"index":0,"step":{"type":"function_call","id":"c1","name":"send","arguments":{}},"event_type":"step.start"}`,
			`{"index":0,"delta":{"type":"arguments_delta","arguments":"{\"to\":\"alice@example.com\"}"},"event_type":"step.delta"}`), "[EMAIL]"},
		{"interactions text and a function call", gemini, steps(
			`{"index":0,"step":{"type":"model_output","content":[{"type":"text","text":"mail alice@example.com"}]},"event_type":"step.start"}`,
			`{"index":1,"step":{"type":"function_call","id":"c1","name":"send"},"event_type":"step.start"}`,
			`{"index":1,"delta":{"type":"arguments_delta","arguments":"{\"to\":\"alice@example.com\"}"},"event_type":"step.delta"}`), "[EMAIL]"},
		{"interactions stream resumed after a function call started", gemini, steps(
			`{"index":0,"delta":{"type":"arguments_delta","arguments":"{\"to\":\"alice@"},"event_id":"evt_2","event_type":"step.delta"}`,
			`{"index":0,"delta":{"type":"arguments_delta","arguments":"example.com\"}"},"event_id":"evt_3","event_type":"step.delta"}`,
			`{"index":0,"event_type":"step.stop"}`), "[EMAIL]"},
		{"interactions failure after text", gemini, steps(
			`{"index":0,"step":{"type":"model_output","content":[{"type":"text","text":"mail alice@"}]},"event_type":"step.start"}`,
			`{"index":0,"delta":{"type":"text","text":"example.com"},"event_type":"step.delta"}`,
			`{"interaction_id":"v1_1","status":"failed","event_type":"interaction.status_update"}`), "[EMAIL]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			format := Classify(tc.in.Provider, "", tc.in.Method, tc.in.Path).StreamFormat()
			require.NotEmpty(t, format)
			read := func(body []byte) []string {
				in := tc.in
				in.StatusCode, in.ResponseBody, in.Stream = 200, body, true
				result, err := Parse(in)
				require.True(t, err == nil || errors.Is(err, ErrUpstreamStreamFailed), "%v", err)
				require.NotNil(t, result)
				require.NotNil(t, result.Response)
				return jsonStringLeaves(t, result.Response)
			}
			before := read([]byte(tc.body))
			require.True(t, strings.Contains(strings.Join(before, "\n"), secret), "the reader reads the value: %q", before)
			redacted, ok := RedactStreamBody([]byte(tc.body), format, map[string]string{secret: tc.mask})
			require.True(t, ok)
			want := make([]string, len(before))
			for i, text := range before {
				want[i] = strings.ReplaceAll(text, secret, tc.mask)
			}
			require.Equal(t, want, read(redacted), "the redacted stream reads back as the reader's text with only the value replaced")
		})
	}

	// The reader reads TGI and LMI token streams from their closing text when one comes, so the token fragments a
	// streaming client receives are checked on the raw frames: both copies carry the mask.
	for _, tc := range cases {
		if tc.name != "tgi tokens and closing text" && tc.name != "lmi tokens and closing text" {
			continue
		}
		t.Run(tc.name+", raw frames", func(t *testing.T) {
			redacted, ok := RedactStreamBody([]byte(tc.body), schemas.RawResponseFormatSageMakerStreamEvent, map[string]string{secret: tc.mask})
			require.True(t, ok)
			var tokens strings.Builder
			closing := ""
			require.NoError(t, forEachSSEEvent(redacted, func(_ string, data []byte) error {
				event := gjson.ParseBytes(data)
				if token := event.Get("token"); !token.Get("special").Bool() {
					tokens.WriteString(token.Get("text").String())
				}
				if whole := event.Get("generated_text"); whole.Type == gjson.String {
					closing = whole.Str
				}
				return nil
			}))
			require.Equal(t, "mail "+tc.mask, tokens.String(), "the token fragments carry the mask")
			require.Equal(t, "mail "+tc.mask, closing, "the closing text carries the mask")
		})
	}

	unread := []struct {
		name string
		in   Input
		body string
	}{
		{"interactions thought summary", gemini, steps(
			`{"index":0,"step":{"type":"thought"},"event_type":"step.start"}`,
			`{"index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"mail alice@example.com"}},"event_type":"step.delta"}`,
			`{"index":1,"step":{"type":"model_output","content":[{"type":"text","text":"done"}]},"event_type":"step.start"}`)},
		{"tgi special token", sageMaker, sse(
			`{"index":1,"token":{"id":1,"text":"done","special":false},"generated_text":null}`,
			`{"index":2,"token":{"id":0,"text":"alice@example.com","special":true},"generated_text":null}`)},
	}
	for _, tc := range unread {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.StatusCode, in.ResponseBody, in.Stream = 200, []byte(tc.body), true
			result, err := Parse(in)
			require.NoError(t, err)
			require.NotContains(t, strings.Join(jsonStringLeaves(t, result.Response), "\n"), secret, "the reader does not read it")
			format := Classify(tc.in.Provider, "", tc.in.Method, tc.in.Path).StreamFormat()
			redacted, ok := RedactStreamBody([]byte(tc.body), format, map[string]string{secret: "[EMAIL]"})
			require.True(t, ok)
			require.Equal(t, tc.body, string(redacted), "the redactor leaves it alone too")
		})
	}
}

// jsonStringLeaves returns every string value of v's JSON form, in document order, so two reads of a parsed result
// can be compared whatever type the reader produced.
func jsonStringLeaves(t *testing.T, v any) []string {
	t.Helper()
	encoded, err := json.Marshal(v)
	require.NoError(t, err)
	var leaves []string
	var walk func(gjson.Result)
	walk = func(value gjson.Result) {
		switch {
		case value.Type == gjson.String:
			leaves = append(leaves, value.Str)
		case value.IsObject() || value.IsArray():
			value.ForEach(func(_, child gjson.Result) bool {
				walk(child)
				return true
			})
		}
	}
	walk(gjson.ParseBytes(encoded))
	return leaves
}

// TestBodyDecidedRoutesKeepPassthroughRows verifies a route whose request type the body names infers no request type
// (its log row stays a passthrough one) while Parse names the type the body made.
func TestBodyDecidedRoutesKeepPassthroughRows(t *testing.T) {
	require.Empty(t, InferRequestType(schemas.Runware, "", "POST", "/v1", false))
	require.Empty(t, InferRequestType(schemas.SageMaker, "", "POST", "/endpoints/e/invocations", true))
	require.Empty(t, InferRequestType(schemas.Bedrock, "", "POST", "/agents/A/agentAliases/B/sessions/c/text", true))
	require.Equal(t, schemas.ResponsesRetrieveStreamRequest, InferRequestType(schemas.Gemini, "", "GET", "/v1beta/interactions/v1_abc", true))

	r, err := Parse(Input{Provider: schemas.Runware, Method: "POST", Path: "/v1", RequestBody: []byte(`[{"taskType":"imageInference","positivePrompt":"a red fox"}]`)})
	require.NoError(t, err)
	require.NotNil(t, r)
	require.Equal(t, schemas.ImageGenerationRequest, r.RequestType)
}

// TestParseRunware verifies a Runware task array reads as the request its task types name, with every text a guardrail
// or log reads, and a task response's text results as chat choices. Synthetic payloads.
func TestParseRunware(t *testing.T) {
	parse := func(body string) *Result {
		t.Helper()
		r, err := Parse(Input{Provider: schemas.Runware, Method: "POST", Path: "/v1", RequestBody: []byte(body)})
		require.NoError(t, err)
		return r
	}

	r := parse(`[{"taskType":"textInference","settings":{"systemPrompt":"be brief"},"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}],"futureField":1},` +
		`{"taskType":"imageInference","positivePrompt":"a red fox","negativePrompt":"blur"}]`)
	require.NotNil(t, r)
	require.Equal(t, schemas.ChatCompletionRequest, r.RequestType)
	var texts []string
	for _, message := range r.Request.ChatRequest.Input {
		texts = append(texts, string(message.Role)+":"+*message.Content.ContentStr)
	}
	require.Equal(t, []string{"system:be brief", "user:hi", "assistant:hello", "user:a red fox"}, texts)

	r = parse(`{"taskType":"videoInference","positivePrompt":"waves at dusk"}`)
	require.Equal(t, schemas.VideoGenerationRequest, r.RequestType)
	require.Equal(t, "waves at dusk", r.Request.VideoGenerationRequest.Input.Prompt)

	require.Nil(t, parse(`[{"taskType":"imageUpscale","inputImage":"img"}]`), "tasks without text have no text form")

	resp, err := Parse(Input{Provider: schemas.Runware, Method: "POST", Path: "/v1", StatusCode: 200,
		ResponseBody: []byte(`{"data":[{"taskType":"textInference","taskUUID":"t1","text":"Ocean breathes","reasoningContent":"think","finishReason":"stop"},{"taskType":"imageInference","imageURL":"https://x"}]}`)})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Response.ChatResponse.Choices, 1)
	require.Equal(t, "Ocean breathes", *resp.Response.ChatResponse.Choices[0].Message.Content.ContentStr)

	// Caption and prompt-enhance results are text a model wrote, read with text results; a caption can come back
	// labelled imageCaption.
	resp, err = Parse(Input{Provider: schemas.Runware, Method: "POST", Path: "/v1", StatusCode: 200, ResponseBody: []byte(
		`{"data":[{"taskType":"textInference","taskUUID":"t1","text":"ok","finishReason":"stop"},` +
			`{"taskType":"promptEnhance","taskUUID":"t2","text":"cinematic portrait"},{"taskType":"caption","taskUUID":"t3","text":"a red fox"},` +
			`{"taskType":"imageCaption","taskUUID":"t4","text":"a blue jay"}]}`)})
	require.NoError(t, err)
	require.Len(t, resp.Response.ChatResponse.Choices, 4)
	require.Equal(t, "a blue jay", *resp.Response.ChatResponse.Choices[3].Message.Content.ContentStr)
	require.Equal(t, "a red fox", *resp.Response.ChatResponse.Choices[2].Message.Content.ContentStr)

	resp, err = Parse(Input{Provider: schemas.Runware, Method: "POST", Path: "/v1", StatusCode: 200, ResponseBody: []byte(`{"data":[{"taskType":"imageInference","imageURL":"https://x"}]}`)})
	require.NoError(t, err)
	require.Nil(t, resp, "an image-only response has no text form")
}

// TestParseSageMaker verifies a SageMaker invocation reads by the shape its container accepts and returns: OpenAI chat
// and completion bodies, TGI/LMI generate inputs and generated text, TEI inputs and vectors, and SSE or JSON-lines token
// streams. Synthetic payloads.
func TestParseSageMaker(t *testing.T) {
	in := func(request, response string, stream bool) Input {
		return Input{Provider: schemas.SageMaker, Method: "POST", Path: "/endpoints/e/invocations", StatusCode: 200,
			RequestBody: []byte(request), ResponseBody: []byte(response), Stream: stream}
	}

	r, err := Parse(in(`{"inputs":"Deep learning is","parameters":{"max_new_tokens":8}}`, `[{"generated_text":"a field of ML"}]`, false))
	require.NoError(t, err)
	require.Equal(t, schemas.TextCompletionRequest, r.RequestType)
	require.Equal(t, "Deep learning is", *r.Request.TextCompletionRequest.Input.PromptStr)
	require.Equal(t, "a field of ML", *r.Response.TextCompletionResponse.Choices[0].Text)

	r, err = Parse(in(`{"inputs":["a","b"]}`, `[[0.1,0.2],[0.3,0.4]]`, false))
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, r.Request.TextCompletionRequest.Input.PromptArray)
	require.NotNil(t, r.Response.EmbeddingResponse, "embedding vectors carry no text")

	r, err = Parse(in(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":8}`, `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`, false))
	require.NoError(t, err)
	require.Equal(t, schemas.ChatCompletionRequest, r.RequestType)
	require.Equal(t, "hello", *r.Response.ChatResponse.Choices[0].Message.Content.ContentStr)

	r, err = Parse(in(`{"query":"q","texts":["a"]}`, `[{"index":0,"score":0.9}]`, false))
	require.NoError(t, err)
	require.Nil(t, r, "a rerank body has no text form")

	tgi := "data:{\"index\":1,\"token\":{\"id\":1,\"text\":\"Deep \",\"logprob\":-0.1,\"special\":false},\"generated_text\":null,\"details\":null}\n\n" +
		"data:{\"index\":2,\"token\":{\"id\":2,\"text\":\"learning\",\"logprob\":-0.1,\"special\":false},\"generated_text\":null,\"details\":null}\n\n" +
		"data:{\"index\":3,\"token\":{\"id\":0,\"text\":\"</s>\",\"logprob\":0,\"special\":true},\"generated_text\":null,\"details\":null}\n\n"
	r, err = Parse(in(`{"inputs":"x","stream":true}`, tgi, true))
	require.NoError(t, err)
	require.Equal(t, "Deep learning", *r.Response.TextCompletionResponse.Choices[0].Text)

	lmi := "{\"token\": {\"id\": 304, \"text\": \"Deep \", \"log_prob\": -0.05}}\n" +
		"{\"token\": {\"id\": 305, \"text\": \"Learning\", \"log_prob\": -0.05}, \"generated_text\": \"Deep Learning\"}\n"
	r, err = Parse(in(`{"inputs":"x","stream":true}`, lmi, true))
	require.NoError(t, err)
	require.Equal(t, "Deep Learning", *r.Response.TextCompletionResponse.Choices[0].Text)

	chunks := "{\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"}}]}\n" +
		"{\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n"
	r, err = Parse(in(`{"messages":[{"role":"user","content":"hi"}],"stream":true}`, chunks, true))
	require.NoError(t, err)
	require.Equal(t, "hello", *r.Response.ChatResponse.Choices[0].Message.Content.ContentStr)

	// TGI 1.4 to 2.0 label chat replies and chunks text_completion; what the choice carries decides.
	r, err = Parse(in(`{"messages":[{"role":"user","content":"hi"}]}`, `{"object":"text_completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`, false))
	require.NoError(t, err)
	require.Equal(t, "hello", *r.Response.ChatResponse.Choices[0].Message.Content.ContentStr)
	labelled := strings.ReplaceAll(chunks, "chat.completion.chunk", "text_completion")
	r, err = Parse(in(`{"messages":[{"role":"user","content":"hi"}],"stream":true}`, labelled, true))
	require.NoError(t, err)
	require.Equal(t, "hello", *r.Response.ChatResponse.Choices[0].Message.Content.ContentStr)
	r, err = Parse(in(`{"prompt":"say"}`, `{"object":"text_completion","choices":[{"index":0,"text":"done","finish_reason":"stop"}]}`, false))
	require.NoError(t, err)
	require.Equal(t, "done", *r.Response.TextCompletionResponse.Choices[0].Text)
	r, err = Parse(in(`{"prompt":"say","stream":true}`, "{\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"do\"}]}\n{\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"text\":\"ne\"}]}\n", true))
	require.NoError(t, err)
	require.Equal(t, "done", *r.Response.TextCompletionResponse.Choices[0].Text)

	// LMI keeps its 200 when generation fails mid-stream and ends on finish_reason "error": an upstream failure, with
	// the text that streamed before it.
	failed := "{\"token\": {\"id\": 304, \"text\": \"Deep \", \"log_prob\": -0.05}}\n" +
		"{\"token\": {\"id\": -1, \"text\": \"\", \"log_prob\": -1, \"special_token\": true}, \"generated_text\": \"\", \"details\": {\"finish_reason\": \"error\", \"generated_tokens\": null, \"inputs\": null}}\n"
	r, err = Parse(in(`{"inputs":"x","stream":true}`, failed, true))
	require.ErrorIs(t, err, ErrUpstreamStreamFailed)
	require.Equal(t, "Deep ", *r.Response.TextCompletionResponse.Choices[0].Text)

	// A cut stream's unfinished last event is an error, and the text before it is kept.
	r, err = Parse(in(`{"inputs":"x","stream":true}`, tgi+"data:{\"index\":4,\"token\":{\"id\":3,\"text\":\" rocks", true))
	require.ErrorIs(t, err, errInvalidEvent)
	require.Equal(t, "Deep learning", *r.Response.TextCompletionResponse.Choices[0].Text)
	// TGI keeps its 200 when generation fails mid-stream and sends an error event: an upstream failure, with the text
	// that streamed before it.
	r, err = Parse(in(`{"inputs":"x","stream":true}`, tgi+"data:{\"error\":\"Request failed during generation: Server error: out of memory\",\"error_type\":\"generation\"}\n\n", true))
	require.ErrorIs(t, err, ErrUpstreamStreamFailed)
	require.Equal(t, "Deep learning", *r.Response.TextCompletionResponse.Choices[0].Text)

	// LMI dynamic batching streams each prompt's next piece in outputs, by prompt position.
	r, err = Parse(in(`{"inputs":"x","stream":true}`, "{\"outputs\": [\"This\"]}\n{\"outputs\": [\" opportunity\"]}\n", true))
	require.NoError(t, err)
	require.Equal(t, "This opportunity", *r.Response.TextCompletionResponse.Choices[0].Text)
	r, err = Parse(in(`{"inputs":["a","b"],"stream":true}`, "{\"outputs\": [\"a1\", \"b1\"]}\n{\"outputs\": [\"a2\", \"b2\"]}\n", true))
	require.NoError(t, err)
	require.Len(t, r.Response.TextCompletionResponse.Choices, 2)
	require.Equal(t, "a1a2", *r.Response.TextCompletionResponse.Choices[0].Text)
	require.Equal(t, "b1b2", *r.Response.TextCompletionResponse.Choices[1].Text)

	// LMI's own formatter marks special tokens special_token; with no closing generated_text the tokens are joined.
	native := "{\"token\": {\"id\": 1, \"text\": \"Deep \", \"log_prob\": -0.1}}\n" +
		"{\"token\": {\"id\": 2, \"text\": \"learning\", \"log_prob\": -0.1}}\n" +
		"{\"token\": {\"id\": 0, \"text\": \"</s>\", \"log_prob\": 0, \"special_token\": true}}\n"
	r, err = Parse(in(`{"inputs":"x","stream":true}`, native, true))
	require.NoError(t, err)
	require.Equal(t, "Deep learning", *r.Response.TextCompletionResponse.Choices[0].Text)

	// Only string outputs are LMI's text: other items are skipped and each prompt keeps its position.
	r, err = Parse(in(`{"inputs":["a","b"],"stream":true}`, "{\"outputs\": [{\"label\": \"x\"}, \"b1\"]}\n{\"outputs\": [{\"label\": \"y\"}, \"b2\"]}\n", true))
	require.NoError(t, err)
	require.Len(t, r.Response.TextCompletionResponse.Choices, 2)
	require.Equal(t, "", *r.Response.TextCompletionResponse.Choices[0].Text)
	require.Equal(t, "b1b2", *r.Response.TextCompletionResponse.Choices[1].Text)
	r, err = Parse(in(`{"inputs":"x","stream":true}`, "{\"outputs\": [{\"label\": \"private\"}]}\n", true))
	require.ErrorIs(t, err, errNoEvents, "outputs with no text are not LMI's, so the stream is unreadable")
	require.True(t, r == nil || r.Response == nil)

	// A first event that is not JSON is reported as an invalid event, not as a stream with no events.
	r, err = Parse(in(`{"inputs":"x","stream":true}`, "data:oops\n\n"+tgi, true))
	require.ErrorIs(t, err, errInvalidEvent)
	require.True(t, r == nil || r.Response == nil)

	// A token whose text is not a string, or a closing text of null alone, is no text: such a stream is unreadable.
	r, err = Parse(in(`{"inputs":"x","stream":true}`, "{\"token\": {\"text\": {\"label\": \"internal\"}}}\n", true))
	require.ErrorIs(t, err, errNoEvents)
	require.True(t, r == nil || r.Response == nil)
	r, err = Parse(in(`{"inputs":"x","stream":true}`, "{\"generated_text\": null}\n", true))
	require.ErrorIs(t, err, errNoEvents)
	require.True(t, r == nil || r.Response == nil)

	// An error after frames of no shape the readers know (LMI's Bedrock-compatible output) leaves the stream unreadable,
	// rather than an empty failed reply.
	r, err = Parse(in(`{"inputs":"x","stream":true}`, "{\"body\": {\"generation\": \"mail\"}}\n{\"body\": {\"generation\": \"\"}, \"error\": {\"error_code\": 400, \"error_msg\": \"oom\"}}\n", true))
	require.ErrorIs(t, err, errNoEvents)
	require.True(t, r == nil || r.Response == nil)

	// An object of inputs (a TEI similarity call) is no text this reader reads.
	r, err = Parse(in(`{"inputs":{"source_sentence":"mail alice@example.com","sentences":["x"]}}`, `[0.9]`, false))
	require.NoError(t, err)
	require.True(t, r == nil || r.Request == nil)

	// A repeated inputs key reads as its last value, as LMI's Python decoder takes it.
	r, err = Parse(in(`{"inputs":"hello","inputs":"mail alice@example.com"}`, `[{"generated_text":"ok"}]`, false))
	require.NoError(t, err)
	require.Equal(t, "mail alice@example.com", *r.Request.TextCompletionRequest.Input.PromptStr)

	// best_of alternatives come back beside the answer and are read as choices of their own.
	r, err = Parse(in(`{"inputs":"x","parameters":{"best_of":2,"details":true}}`,
		`[{"generated_text":"ok","details":{"finish_reason":"length","best_of_sequences":[{"generated_text":"mail alice@example.com","finish_reason":"length"}]}}]`, false))
	require.NoError(t, err)
	require.Len(t, r.Response.TextCompletionResponse.Choices, 2)
	require.Equal(t, "mail alice@example.com", *r.Response.TextCompletionResponse.Choices[1].Text)

	// A stream with no event the readers know, such as a custom container's, is unreadable rather than empty text.
	r, err = Parse(in(`{"inputs":"x","stream":true}`, "{\"chunk\": \"hello\"}\n{\"chunk\": \" world\"}\n", true))
	require.ErrorIs(t, err, errNoEvents)
	require.True(t, r == nil || r.Response == nil, "no response is invented for an unknown stream")
}

// TestParseGeminiInteractions verifies an Interactions create reads its system instruction and every input shape
// (replayed function calls included), an
// interaction reads its model output and function calls (not thoughts or the user input a GET repeats), and a stream
// reads each step's start text and deltas in step order. Synthetic payloads.
func TestParseGeminiInteractions(t *testing.T) {
	request := `{"model":"gemini-3.8-flash","system_instruction":"be brief","input":[` +
		`{"type":"user_input","content":[{"type":"text","text":"hi"}]},` +
		`{"type":"model_output","content":[{"type":"text","text":"hello"}]},` +
		`{"type":"function_call","id":"c1","name":"lookup","arguments":{"q":"answer"}},` +
		`{"type":"function_result","name":"lookup","call_id":"c1","result":[{"type":"text","text":"42"}]},` +
		`{"type":"future_step","text":"ignored"}]}`
	r, err := Parse(Input{Provider: schemas.Gemini, Method: "POST", Path: "/v1beta/interactions", RequestBody: []byte(request)})
	require.NoError(t, err)
	require.Equal(t, schemas.ResponsesRequest, r.RequestType)
	require.Equal(t, "be brief", *r.Request.ResponsesRequest.Params.Instructions)
	input := r.Request.ResponsesRequest.Input
	require.Len(t, input, 4)
	require.Equal(t, "hi", *input[0].Content.ContentStr)
	require.Equal(t, schemas.ResponsesInputMessageRoleAssistant, *input[1].Role)
	require.Equal(t, `{"q":"answer"}`, *input[2].ResponsesToolMessage.Arguments)
	require.Equal(t, "lookup", *input[2].ResponsesToolMessage.Name)
	require.Equal(t, "42", *input[3].ResponsesToolMessage.Output.ResponsesToolCallOutputStr)

	r, err = Parse(Input{Provider: schemas.Gemini, Method: "POST", Path: "/v1beta/interactions", RequestBody: []byte(`{"model":"m","input":"plain"}`)})
	require.NoError(t, err)
	require.Equal(t, "plain", *r.Request.ResponsesRequest.Input[0].Content.ContentStr)

	interaction := `{"id":"v1_1","object":"interaction","status":"completed","steps":[` +
		`{"type":"user_input","content":[{"type":"text","text":"question"}]},` +
		`{"type":"thought","summary":[{"type":"text","text":"thinking"}]},` +
		`{"type":"function_call","id":"c1","name":"lookup","arguments":{"q":"x"}},` +
		`{"type":"model_output","content":[{"type":"text","text":"answer"}]}]}`
	r, err = Parse(Input{Provider: schemas.Gemini, Method: "GET", Path: "/v1beta/interactions/v1_1", StatusCode: 200, ResponseBody: []byte(interaction)})
	require.NoError(t, err)
	output := r.Response.ResponsesResponse.Output
	require.Len(t, output, 2)
	require.Equal(t, `{"q":"x"}`, *output[0].ResponsesToolMessage.Arguments)
	require.Equal(t, "answer", *output[1].Content.ContentBlocks[0].Text)

	stream := "event: interaction.created\ndata: {\"interaction\":{\"id\":\"v1_1\",\"status\":\"in_progress\"},\"event_type\":\"interaction.created\"}\n\n" +
		"event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"thought\"},\"event_type\":\"step.start\"}\n\n" +
		"event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"thought_summary\",\"content\":{\"type\":\"text\",\"text\":\"hmm\"}},\"event_type\":\"step.delta\"}\n\n" +
		"event: step.start\ndata: {\"index\":1,\"step\":{\"type\":\"model_output\",\"content\":[{\"type\":\"text\",\"text\":\"1, \"}]},\"event_type\":\"step.start\"}\n\n" +
		"event: step.delta\ndata: {\"index\":1,\"delta\":{\"type\":\"text\",\"text\":\"2, 3\"},\"event_type\":\"step.delta\"}\n\n" +
		"event: interaction.completed\ndata: {\"interaction\":{\"id\":\"v1_1\",\"status\":\"completed\"},\"event_type\":\"interaction.completed\"}\n\n" +
		"event: done\ndata: [DONE]\n\n"
	r, err = Parse(Input{Provider: schemas.Gemini, Method: "POST", Path: "/v1beta/interactions", StatusCode: 200, ResponseBody: []byte(stream), Stream: true})
	require.NoError(t, err)
	output = r.Response.ResponsesResponse.Output
	require.Len(t, output, 1)
	require.Equal(t, "1, 2, 3", *output[0].Content.ContentBlocks[0].Text)

	// Vertex sends a function call's argument fragments as partial_arguments.
	vertex := "event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"c1\",\"name\":\"send\"},\"event_type\":\"step.start\"}\n\n" +
		"event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"partial_arguments\":\"{\\\"to\\\":\\\"al\"},\"event_type\":\"step.delta\"}\n\n" +
		"event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"partial_arguments\":\"ex@example.com\\\"}\"},\"event_type\":\"step.delta\"}\n\n"
	r, err = Parse(Input{Provider: schemas.Vertex, Method: "POST", Path: "/v1beta1/projects/p/locations/global/interactions", StatusCode: 200, ResponseBody: []byte(vertex), Stream: true})
	require.NoError(t, err)
	require.Equal(t, "send", *r.Response.ResponsesResponse.Output[0].ResponsesToolMessage.Name)
	require.Equal(t, `{"to":"alex@example.com"}`, *r.Response.ResponsesResponse.Output[0].ResponsesToolMessage.Arguments)

	// A function call can start with its arguments whole and no delta after them.
	startOnly := "event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"c1\",\"name\":\"send\",\"arguments\":{\"to\":\"alex@example.com\"}},\"event_type\":\"step.start\"}\n\n" +
		"event: step.stop\ndata: {\"index\":0,\"event_type\":\"step.stop\"}\n\n"
	r, err = Parse(Input{Provider: schemas.Gemini, Method: "POST", Path: "/v1beta/interactions", StatusCode: 200, ResponseBody: []byte(startOnly), Stream: true})
	require.NoError(t, err)
	require.Equal(t, `{"to":"alex@example.com"}`, *r.Response.ResponsesResponse.Output[0].ResponsesToolMessage.Arguments)

	// A failed status ends the stream as an upstream failure, with the text before it.
	failed := stream[:strings.Index(stream, "event: interaction.completed")] +
		"event: interaction.status_update\ndata: {\"interaction_id\":\"v1_1\",\"status\":\"failed\",\"event_type\":\"interaction.status_update\"}\n\n"
	r, err = Parse(Input{Provider: schemas.Gemini, Method: "POST", Path: "/v1beta/interactions", StatusCode: 200, ResponseBody: []byte(failed), Stream: true})
	require.ErrorIs(t, err, ErrUpstreamStreamFailed)
	require.Equal(t, "1, 2, 3", *r.Response.ResponsesResponse.Output[0].Content.ContentBlocks[0].Text)

	// A cut stream's unfinished last event is an error, and the text before it is kept.
	cut := stream[:strings.Index(stream, "event: interaction.completed")] + "event: step.delta\ndata: {\"index\":1,\"delta\":{\"type\":\"text\",\"text\":\", 4\n\n"
	r, err = Parse(Input{Provider: schemas.Gemini, Method: "POST", Path: "/v1beta/interactions", StatusCode: 200, ResponseBody: []byte(cut), Stream: true})
	require.ErrorIs(t, err, errInvalidEvent)
	require.Equal(t, "1, 2, 3", *r.Response.ResponsesResponse.Output[0].Content.ContentBlocks[0].Text)
}

// TestParseBedrockAgent verifies an InvokeAgent body reads as a chat turn of its history, returned tool results and
// input text, and that its event-stream answer has no text form. Synthetic payloads.
func TestParseBedrockAgent(t *testing.T) {
	body := `{"inputText":"what now","sessionState":{"sessionAttributes":{"k":"v"},` +
		`"conversationHistory":{"messages":[{"role":"user","content":[{"text":"earlier"}]},{"role":"assistant","content":[{"text":"reply"}]}]},` +
		`"returnControlInvocationResults":[{"functionResult":{"actionGroup":"g","function":"f","responseBody":{"TEXT":{"body":"rainy"}}}}]}}`
	r, err := Parse(Input{Provider: schemas.Bedrock, Method: "POST", Path: "/agents/A/agentAliases/B/sessions/c/text", RequestBody: []byte(body),
		StatusCode: 200, ResponseBody: []byte("\x00\x00\x00\x10binary"), Stream: true})
	require.NoError(t, err)
	require.Equal(t, schemas.ChatCompletionRequest, r.RequestType)
	var texts []string
	for _, message := range r.Request.ChatRequest.Input {
		texts = append(texts, string(message.Role)+":"+*message.Content.ContentStr)
	}
	require.Equal(t, []string{"user:earlier", "assistant:reply", "tool:rainy", "user:what now"}, texts)
	require.Nil(t, r.Response, "the agent's event-stream answer has no text form")
}

// TestRedactStreamBodyNewFormats verifies SageMaker token streams (SSE and JSON lines) and Interactions streams are
// redacted across the events a value is split over, and read back as the redacted text. Synthetic payloads.
func TestRedactStreamBodyNewFormats(t *testing.T) {
	replacements := map[string]string{"alice@example.com": "[EMAIL]"}
	cases := []struct {
		name    string
		in      Input
		format  schemas.RawResponseFormat
		body    string
		want    string
		readOut func(*Result) string
	}{
		{
			name:   "sagemaker tgi sse",
			in:     Input{Provider: schemas.SageMaker, Method: "POST", Path: "/endpoints/e/invocations-response-stream"},
			format: schemas.RawResponseFormatSageMakerStreamEvent,
			body: "data:{\"token\":{\"text\":\"mail alice@\",\"special\":false},\"generated_text\":null}\n\n" +
				"data:{\"token\":{\"text\":\"example.com\",\"special\":false},\"generated_text\":null}\n\n",
			want:    "mail [EMAIL]",
			readOut: func(r *Result) string { return *r.Response.TextCompletionResponse.Choices[0].Text },
		},
		{
			name:   "sagemaker lmi json lines",
			in:     Input{Provider: schemas.SageMaker, Method: "POST", Path: "/endpoints/e/invocations-response-stream"},
			format: schemas.RawResponseFormatSageMakerStreamEvent,
			body: "{\"token\": {\"text\": \"mail alice@\"}}\n" +
				"{\"token\": {\"text\": \"example.com\"}, \"generated_text\": \"mail alice@example.com\"}\n",
			want:    "mail [EMAIL]",
			readOut: func(r *Result) string { return *r.Response.TextCompletionResponse.Choices[0].Text },
		},
		{
			name:   "sagemaker tgi chat labelled text_completion",
			in:     Input{Provider: schemas.SageMaker, Method: "POST", Path: "/endpoints/e/invocations-response-stream"},
			format: schemas.RawResponseFormatSageMakerStreamEvent,
			body: "data:{\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"mail alice@\"}}]}\n\n" +
				"data:{\"object\":\"text_completion\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"example.com\"},\"finish_reason\":\"stop\"}]}\n\n",
			want:    "mail [EMAIL]",
			readOut: func(r *Result) string { return *r.Response.ChatResponse.Choices[0].Message.Content.ContentStr },
		},
		{
			name:    "sagemaker lmi outputs",
			in:      Input{Provider: schemas.SageMaker, Method: "POST", Path: "/endpoints/e/invocations-response-stream"},
			format:  schemas.RawResponseFormatSageMakerStreamEvent,
			body:    "{\"outputs\": [\"mail alice@\"]}\n{\"outputs\": [\"example.com\"]}\n",
			want:    "mail [EMAIL]",
			readOut: func(r *Result) string { return *r.Response.TextCompletionResponse.Choices[0].Text },
		},
		{
			name:   "sagemaker lmi native special token",
			in:     Input{Provider: schemas.SageMaker, Method: "POST", Path: "/endpoints/e/invocations-response-stream"},
			format: schemas.RawResponseFormatSageMakerStreamEvent,
			body: "{\"token\": {\"text\": \"mail alice@\"}}\n" +
				"{\"token\": {\"text\": \"<|eot_id|>\", \"special_token\": true}}\n" +
				"{\"token\": {\"text\": \"example.com\"}}\n",
			want:    "mail [EMAIL]",
			readOut: func(r *Result) string { return *r.Response.TextCompletionResponse.Choices[0].Text },
		},
		{
			name:   "vertex interactions partial_arguments",
			in:     Input{Provider: schemas.Vertex, Method: "POST", Path: "/v1beta1/projects/p/locations/global/interactions"},
			format: schemas.RawResponseFormatGeminiInteractionsStreamEvent,
			body: "event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"c1\",\"name\":\"send\"},\"event_type\":\"step.start\"}\n\n" +
				"event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"partial_arguments\":\"{\\\"to\\\":\\\"alice@\"},\"event_type\":\"step.delta\"}\n\n" +
				"event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"partial_arguments\":\"example.com\\\"}\"},\"event_type\":\"step.delta\"}\n\n",
			want:    `{"to":"[EMAIL]"}`,
			readOut: func(r *Result) string { return *r.Response.ResponsesResponse.Output[0].ResponsesToolMessage.Arguments },
		},
		{
			name:    "interactions arguments on step.start",
			in:      Input{Provider: schemas.Gemini, Method: "POST", Path: "/v1beta/interactions"},
			format:  schemas.RawResponseFormatGeminiInteractionsStreamEvent,
			body:    "event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"c1\",\"name\":\"send\",\"arguments\":{\"to\":\"alice@example.com\"}},\"event_type\":\"step.start\"}\n\n",
			want:    `{"to":"[EMAIL]"}`,
			readOut: func(r *Result) string { return *r.Response.ResponsesResponse.Output[0].ResponsesToolMessage.Arguments },
		},
		{
			name:   "gemini interactions",
			in:     Input{Provider: schemas.Gemini, Method: "POST", Path: "/v1beta/interactions"},
			format: schemas.RawResponseFormatGeminiInteractionsStreamEvent,
			body: "event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"model_output\",\"content\":[{\"type\":\"text\",\"text\":\"mail ali\"}]},\"event_type\":\"step.start\"}\n\n" +
				"event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"ce@example.com\"},\"event_type\":\"step.delta\"}\n\n",
			want:    "mail [EMAIL]",
			readOut: func(r *Result) string { return *r.Response.ResponsesResponse.Output[0].Content.ContentBlocks[0].Text },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.format, Classify(tc.in.Provider, "", tc.in.Method, tc.in.Path).StreamFormat())
			redacted, ok := RedactStreamBody([]byte(tc.body), tc.format, replacements)
			require.True(t, ok)
			require.NotContains(t, string(redacted), "alice@")
			tc.in.StatusCode, tc.in.ResponseBody, tc.in.Stream = 200, redacted, true
			r, err := Parse(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want, tc.readOut(r))
		})
	}
}
