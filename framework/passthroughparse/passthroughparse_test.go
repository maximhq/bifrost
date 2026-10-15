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

func TestParseStreamLeavesResponseForReassembly(t *testing.T) {
	r, err := Parse(Input{Provider: schemas.OpenAI, Model: "gpt-4o", Method: "POST", Path: "/v1/chat/completions", StatusCode: 200, Stream: true, RequestBody: []byte(openAIChatRequest), ResponseBody: []byte("data: {}\n\n")})
	require.NoError(t, err)
	require.NotNil(t, r)
	require.NotNil(t, r.Request)
	require.Nil(t, r.Response)
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
