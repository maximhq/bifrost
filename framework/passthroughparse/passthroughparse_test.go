package passthroughparse

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
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

func TestParseUnknownRoutesReturnNil(t *testing.T) {
	tests := []struct {
		name string
		in   Input
	}{
		{"openai list models", Input{Provider: schemas.OpenAI, Method: "GET", Path: "/v1/models", StatusCode: 200, ResponseBody: []byte(`{"data":[]}`)}},
		{"anthropic batches", Input{Provider: schemas.Anthropic, Method: "POST", Path: "/v1/messages/batches", StatusCode: 200, RequestBody: []byte(`{"requests":[]}`)}},
		{"gemini interactions", Input{Provider: schemas.Gemini, Method: "POST", Path: "/interactions", StatusCode: 200, RequestBody: []byte(`{}`)}},
		{"bedrock agent route", Input{Provider: schemas.Bedrock, Method: "POST", Path: "/agents/a/agentAliases/b/sessions/c/text", StatusCode: 200, RequestBody: []byte(`{"inputText":"hi"}`)}},
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
