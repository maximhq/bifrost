package integrations

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestHumeChatRequestParsesMetadataAndOpenAIFields(t *testing.T) {
	raw := []byte(`{
		"model":"anthropic/claude-sonnet-4-5",
		"stream":true,
		"fallbacks":["openai/gpt-4o-mini"],
		"messages":[
			{"role":"user","content":"weather?","time":{"begin":10.5,"end":900},"models":{"prosody":{"scores":{"Joy":0.2,"Interest":0.9}}}},
			{"role":"assistant","content":null,"time":{"end":950},"tool_calls":[{"id":"call-1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]}
		],
		"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}],
		"tool_choice":"auto"
	}`)

	var request HumeChatRequest
	require.NoError(t, sonic.Unmarshal(raw, &request))
	assert.Equal(t, "anthropic/claude-sonnet-4-5", request.OpenAIRequest.Model)
	assert.True(t, request.IsStreamingRequested())
	require.Len(t, request.OpenAIRequest.Messages, 2)
	require.NotNil(t, request.OpenAIRequest.Messages[1].OpenAIChatAssistantMessage)
	require.Len(t, request.OpenAIRequest.Messages[1].ToolCalls, 1)
	require.Len(t, request.OpenAIRequest.Tools, 1)
	assert.Equal(t, []string{"openai/gpt-4o-mini"}, request.Fallbacks)
	require.Len(t, request.messageMetadata, 2)
	require.NotNil(t, request.messageMetadata[0].Time.Begin)
	require.NotNil(t, request.messageMetadata[0].Time.End)
	assert.Equal(t, 10.5, *request.messageMetadata[0].Time.Begin)
	assert.Equal(t, 900.0, *request.messageMetadata[0].Time.End)
	assert.Equal(t, 0.9, request.messageMetadata[0].ProsodyScores["Interest"])
	require.NotNil(t, request.messageMetadata[1].Time)
	assert.Nil(t, request.messageMetadata[1].Time.Begin)
	require.NotNil(t, request.messageMetadata[1].Time.End)
	assert.Equal(t, 950.0, *request.messageMetadata[1].Time.End)
}

func TestHumeChatRequestRejectsMalformedMetadata(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "invalid time",
			body: `{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello","time":"invalid"}]}`,
		},
		{
			name: "invalid prosody scores",
			body: `{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello","models":{"prosody":{"scores":[]}}}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var request HumeChatRequest
			require.Error(t, sonic.Unmarshal([]byte(test.body), &request))
		})
	}
}

func TestHumePreCallbackModelSessionAndStreamingDefaults(t *testing.T) {
	config := lib.NewDefaultHumeConfig()
	config.DefaultModel = "openai/gpt-4o-mini"
	request := &HumeChatRequest{}
	request.OpenAIRequest.Tools = []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction}}
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	var httpCtx fasthttp.RequestCtx
	httpCtx.Request.SetRequestURI("/hume/v1/chat/completions?custom_session_id=session-123")

	require.NoError(t, humePreCallback(config)(&httpCtx, bifrostCtx, request))
	assert.Equal(t, "openai/gpt-4o-mini", request.OpenAIRequest.Model)
	require.NotNil(t, request.OpenAIRequest.Stream)
	assert.True(t, *request.OpenAIRequest.Stream)
	require.NotNil(t, request.OpenAIRequest.N)
	assert.Equal(t, 1, *request.OpenAIRequest.N)
	require.NotNil(t, request.OpenAIRequest.ParallelToolCalls)
	assert.False(t, *request.OpenAIRequest.ParallelToolCalls)
	assert.Equal(t, "session-123", request.customSessionID)
	assert.Equal(t, "session-123", bifrostCtx.Value(humeSessionContextKey{}))
}

func TestHumePreCallbackGeneratesSessionID(t *testing.T) {
	request := &HumeChatRequest{OpenAIRequest: openAIRequestWithModel("openai/gpt-4o-mini")}
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	var httpCtx fasthttp.RequestCtx
	config := lib.NewDefaultHumeConfig()
	config.DefaultModel = "anthropic/claude-sonnet-4-5"

	require.NoError(t, humePreCallback(config)(&httpCtx, bifrostCtx, request))
	assert.Equal(t, "openai/gpt-4o-mini", request.OpenAIRequest.Model)
	_, err := uuid.Parse(request.customSessionID)
	require.NoError(t, err)
	assert.Equal(t, request.customSessionID, bifrostCtx.Value(humeSessionContextKey{}))
}

func TestHumePreCallbackValidation(t *testing.T) {
	falseValue := false
	two := 2
	tests := []struct {
		name       string
		config     *lib.HumeConfig
		request    HumeChatRequest
		requestURI string
		contains   string
	}{
		{
			name:     "explicit non-streaming request",
			config:   &lib.HumeConfig{DefaultModel: "openai/gpt-4o-mini"},
			request:  HumeChatRequest{OpenAIRequest: openAIRequestWithStream(&falseValue)},
			contains: "must use streaming",
		},
		{
			name:     "multiple completions",
			config:   &lib.HumeConfig{DefaultModel: "openai/gpt-4o-mini"},
			request:  HumeChatRequest{OpenAIRequest: openAIRequestWithN(&two)},
			contains: "exactly one completion",
		},
		{
			name:     "missing model",
			config:   lib.NewDefaultHumeConfig(),
			request:  HumeChatRequest{},
			contains: "identifier is missing",
		},
		{
			name:       "session ID too long",
			config:     &lib.HumeConfig{DefaultModel: "openai/gpt-4o-mini"},
			request:    HumeChatRequest{},
			requestURI: "/hume/chat/completions?custom_session_id=" + strings.Repeat("a", humeMaxSessionIDBytes+1),
			contains:   "must not exceed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			var httpCtx fasthttp.RequestCtx
			httpCtx.Request.SetRequestURI(test.requestURI)
			err := humePreCallback(test.config)(&httpCtx, bifrostCtx, &test.request)
			require.ErrorContains(t, err, test.contains)
		})
	}

	t.Run("invalid UTF-8 session ID", func(t *testing.T) {
		request := &HumeChatRequest{}
		bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		var httpCtx fasthttp.RequestCtx
		httpCtx.QueryArgs().SetBytesV("custom_session_id", []byte{0xff})
		err := humePreCallback(&lib.HumeConfig{DefaultModel: "openai/gpt-4o-mini"})(&httpCtx, bifrostCtx, request)
		require.ErrorContains(t, err, "valid UTF-8")
	})
}

func TestHumeRequestConverterExposesMetadataToLLMPlugins(t *testing.T) {
	route := CreateHumeRouteConfigs(lib.NewDefaultHumeConfig())[0]
	request := parseHumeRequest(t, `{
		"model":"openai/gpt-4o-mini",
		"messages":[{"role":"user","content":"hello","time":{"begin":0,"end":100},"models":{"prosody":{"scores":{"Joy":0.8}}}}]
	}`)
	request.customSessionID = "session-123"
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	converted, err := route.RequestConverter(bifrostCtx, request)
	require.NoError(t, err)
	probe := &humeMetadataProbePlugin{}
	_, shortCircuit, pluginErr := probe.PreLLMHook(bifrostCtx, converted)
	require.NoError(t, pluginErr)
	assert.Nil(t, shortCircuit)
	require.NotNil(t, probe.metadata)
	assert.Equal(t, "session-123", probe.metadata.CustomSessionID)
	require.Len(t, probe.metadata.Messages, 1)
	require.Contains(t, probe.metadata.Messages, 0)
	assert.Equal(t, "hello", *converted.ChatRequest.Input[0].Content.ContentStr)
}

func TestHumeProsodyPromptModesAndDeterminism(t *testing.T) {
	maxThree := 3
	messages := []schemas.ChatMessage{
		chatMessage(schemas.ChatMessageRoleSystem, "base prompt"),
		chatMessage(schemas.ChatMessageRoleUser, "first"),
		chatMessage(schemas.ChatMessageRoleAssistant, "answer"),
		chatMessage(schemas.ChatMessageRoleUser, "second"),
	}
	metadata := map[int]schemas.HumeMessageMetadata{
		1: {ProsodyScores: map[string]float64{"Joy": 0.7}},
		3: {ProsodyScores: map[string]float64{"Sadness": 0.23456, "Interest": 0.91, "Joy": 0.91, "Calmness": 0.5}},
	}
	config := &lib.HumeProsodyPromptConfig{Enabled: true, Scope: lib.HumeProsodyPromptScopeLatestUser, MaxEmotions: &maxThree}

	converted, convertedMetadata := injectHumeProsody(messages, metadata, config)
	require.Len(t, converted, 5)
	assert.Equal(t, schemas.ChatMessageRoleSystem, converted[1].Role)
	assert.Equal(t, humeProsodyInstruction, *converted[1].Content.ContentStr)
	assert.NotContains(t, *converted[2].Content.ContentStr, "hume_vocal_expression")
	assert.Contains(t, *converted[4].Content.ContentStr, `<hume_vocal_expression>`)
	assert.Contains(t, *converted[4].Content.ContentStr, `{"label":"Interest","score":0.91}`)
	assert.Contains(t, *converted[4].Content.ContentStr, `{"label":"Joy","score":0.91}`)
	assert.Contains(t, *converted[4].Content.ContentStr, `{"label":"Calmness","score":0.5}`)
	assert.NotContains(t, *converted[4].Content.ContentStr, "Sadness")
	require.Contains(t, convertedMetadata, 2)
	require.Contains(t, convertedMetadata, 4)
	assert.NotContains(t, convertedMetadata, 1)
	assert.NotContains(t, convertedMetadata, 3)

	again, _ := injectHumeProsody(messages, metadata, config)
	firstJSON, err := schemas.MarshalSorted(converted)
	require.NoError(t, err)
	secondJSON, err := schemas.MarshalSorted(again)
	require.NoError(t, err)
	assert.Equal(t, string(firstJSON), string(secondJSON))

	allScores := 0
	config.Scope = lib.HumeProsodyPromptScopeAllUserMessages
	config.MaxEmotions = &allScores
	allMessages, _ := injectHumeProsody(messages, metadata, config)
	assert.Contains(t, *allMessages[2].Content.ContentStr, "Joy")
	assert.Contains(t, *allMessages[4].Content.ContentStr, "Sadness")
}

func TestHumeLatestUserProsodyDoesNotReuseOlderScores(t *testing.T) {
	messages := []schemas.ChatMessage{
		chatMessage(schemas.ChatMessageRoleUser, "older scored turn"),
		chatMessage(schemas.ChatMessageRoleAssistant, "answer"),
		chatMessage(schemas.ChatMessageRoleUser, "latest unscored turn"),
	}
	metadata := map[int]schemas.HumeMessageMetadata{
		0: {ProsodyScores: map[string]float64{"Joy": 0.9}},
	}
	config := &lib.HumeProsodyPromptConfig{
		Enabled: true,
		Scope:   lib.HumeProsodyPromptScopeLatestUser,
	}

	converted, convertedMetadata := injectHumeProsody(messages, metadata, config)

	assert.Equal(t, messages, converted)
	assert.Equal(t, metadata, convertedMetadata)
}

func TestHumeStreamConverterOverridesFingerprintAndSanitizesChunk(t *testing.T) {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(humeSessionContextKey{}, "hume-session")
	role := "assistant"
	content := "hello"
	toolType := "function"
	toolID := "call-1"
	toolName := "lookup"
	finishReason := "tool_calls"
	resp := &schemas.BifrostChatResponse{
		ID:                "chatcmpl-1",
		Created:           123,
		Model:             "gpt-4o-mini",
		Object:            "chat.completion.chunk",
		SystemFingerprint: "upstream-fingerprint",
		Choices: []schemas.BifrostResponseChoice{{
			Index:        0,
			FinishReason: &finishReason,
			ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{
				Role:         &role,
				Content:      &content,
				Reasoning:    schemas.Ptr("provider-only reasoning"),
				ExtraContent: json.RawMessage(`{"thought_signature":"secret"}`),
				ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					Index: 0,
					Type:  &toolType,
					ID:    &toolID,
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      &toolName,
						Arguments: `{"query":"x"}`,
					},
					ExtraContent: json.RawMessage(`{"provider":"secret"}`),
				}},
			}},
		}},
		Usage:       &schemas.BifrostLLMUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3, Cost: &schemas.BifrostCost{}},
		ExtraFields: schemas.BifrostResponseExtraFields{RawResponse: json.RawMessage(`{"system_fingerprint":"raw-upstream"}`)},
	}

	_, converted, err := humeChatStreamResponseConverter(bifrostCtx, resp)
	require.NoError(t, err)
	body, err := sonic.Marshal(converted)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"system_fingerprint":"hume-session"`)
	assert.Contains(t, string(body), `"tool_calls"`)
	assert.NotContains(t, string(body), "upstream-fingerprint")
	assert.NotContains(t, string(body), "raw-upstream")
	assert.NotContains(t, string(body), "extra_fields")
	assert.NotContains(t, string(body), "provider-only reasoning")
	assert.NotContains(t, string(body), "thought_signature")
	assert.NotContains(t, string(body), "cost")
}

func TestHumeStreamConverterPreservesWrappedNonStreamMessage(t *testing.T) {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(humeSessionContextKey{}, "hume-session")
	toolType := "function"
	toolID := "call-1"
	toolName := "lookup"
	secondToolID := "call-2"
	secondToolName := "search"
	refusal := "cannot comply"
	firstText := "hello "
	secondText := "world"
	resp := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{{
			Index: 0,
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{
				Role: schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: &firstText},
					{Type: schemas.ChatContentBlockTypeText, Text: &secondText},
				}},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					Refusal: &refusal,
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							Type: &toolType,
							ID:   &toolID,
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      &toolName,
								Arguments: `{"query":"x"}`,
							},
						},
						{
							Type: &toolType,
							ID:   &secondToolID,
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      &secondToolName,
								Arguments: `{"query":"y"}`,
							},
						},
					},
				},
			}},
		}},
	}

	_, converted, err := humeChatStreamResponseConverter(bifrostCtx, resp)
	require.NoError(t, err)
	chunk, ok := converted.(*humeStreamChunk)
	require.True(t, ok)
	require.Len(t, chunk.Choices, 1)
	delta := chunk.Choices[0].Delta
	require.NotNil(t, delta.Role)
	assert.Equal(t, "assistant", *delta.Role)
	require.NotNil(t, delta.Content)
	assert.Equal(t, "hello world", *delta.Content)
	assert.Equal(t, &refusal, delta.Refusal)
	require.Len(t, delta.ToolCalls, 2)
	assert.Equal(t, uint16(0), delta.ToolCalls[0].Index)
	assert.Equal(t, &toolID, delta.ToolCalls[0].ID)
	assert.Equal(t, uint16(1), delta.ToolCalls[1].Index)
	assert.Equal(t, &secondToolID, delta.ToolCalls[1].ID)
}

func TestHumeStreamConverterRejectsNonStreamToolCallIndexOverflow(t *testing.T) {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(humeSessionContextKey{}, "hume-session")

	for _, test := range []struct {
		name  string
		count int
	}{
		{name: "65536 tool calls", count: 65_536},
		{name: "65537 tool calls", count: 65_537},
	} {
		t.Run(test.name, func(t *testing.T) {
			resp := &schemas.BifrostChatResponse{
				Choices: []schemas.BifrostResponseChoice{{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: make([]schemas.ChatAssistantMessageToolCall, test.count),
						},
					}},
				}},
			}

			_, converted, err := humeChatStreamResponseConverter(bifrostCtx, resp)
			require.EqualError(t, err, "hume non-stream response cannot contain 65536 or more tool calls")
			assert.Nil(t, converted)
		})
	}
}

func TestHumeRoutesDisableLargePayloadPassthrough(t *testing.T) {
	for _, route := range CreateHumeRouteConfigs(lib.NewDefaultHumeConfig()) {
		assert.True(t, route.DisableLargePayloadMode, route.Path)
	}
}

func TestHumeRouteStreamsOpenAISSEAndDoneMarker(t *testing.T) {
	route := CreateHumeRouteConfigs(lib.NewDefaultHumeConfig())[0]
	router := NewGenericRouter(nil, &mockHandlerStore{}, []RouteConfig{route}, nil, &testLogger{})
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(humeSessionContextKey{}, "session-sse")
	content := "hello"
	stream := make(chan *schemas.BifrostStreamChunk, 1)
	stream <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
		ID:      "chatcmpl-sse",
		Created: 123,
		Model:   "gpt-4o-mini",
		Object:  "chat.completion.chunk",
		Choices: []schemas.BifrostResponseChoice{{
			Index: 0,
			ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{
				Content: &content,
			}},
		}},
	}}
	close(stream)

	var httpCtx fasthttp.RequestCtx
	httpCtx.SetContentType("text/event-stream")
	router.handleStreaming(&httpCtx, bifrostCtx, route, stream, func() {})
	body := string(httpCtx.Response.Body())
	assert.Equal(t, "text/event-stream", string(httpCtx.Response.Header.ContentType()))
	assert.Contains(t, body, `data: {"id":"chatcmpl-sse"`)
	assert.Contains(t, body, `"system_fingerprint":"session-sse"`)
	assert.Contains(t, body, `"content":"hello"`)
	assert.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"), body)
}

func TestHumeRoutesAreAlwaysRegistered(t *testing.T) {
	r := router.New()
	humeRouter := NewHumeRouter(nil, &mockHandlerStore{}, &testLogger{})
	humeRouter.RegisterRoutes(r, func(_ fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			if string(ctx.Request.Header.Peek("Authorization")) != "Bearer bf-virtual-key" {
				ctx.SetStatusCode(fasthttp.StatusUnauthorized)
				return
			}
			ctx.SetStatusCode(fasthttp.StatusNoContent)
		}
	})

	for _, path := range []string{"/hume/v1/chat/completions", "/hume/chat/completions"} {
		var ctx fasthttp.RequestCtx
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		ctx.Request.Header.Set("Authorization", "Bearer bf-virtual-key")
		ctx.Request.SetRequestURI(path)
		r.Handler(&ctx)
		assert.Equal(t, fasthttp.StatusNoContent, ctx.Response.StatusCode(), path)
	}
}

func TestHumeMissingModelReturnsBadRequestBeforeInference(t *testing.T) {
	r := router.New()
	humeRouter := NewHumeRouter(nil, &mockHandlerStore{}, &testLogger{})
	humeRouter.RegisterRoutes(r)

	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.SetRequestURI("/hume/v1/chat/completions")
	ctx.Request.SetBodyString(`{"messages":[{"role":"user","content":"hello"}]}`)
	r.Handler(&ctx)

	assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	assert.Equal(t, "application/json", string(ctx.Response.Header.ContentType()))
	assert.Contains(t, string(ctx.Response.Body()), "identifier is missing")
}

func parseHumeRequest(t *testing.T, raw string) *HumeChatRequest {
	t.Helper()
	var request HumeChatRequest
	require.NoError(t, sonic.Unmarshal([]byte(raw), &request))
	return &request
}

func chatMessage(role schemas.ChatMessageRole, content string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: &content}}
}

func openAIRequestWithModel(model string) openai.OpenAIChatRequest {
	return openai.OpenAIChatRequest{Model: model}
}

func openAIRequestWithStream(stream *bool) openai.OpenAIChatRequest {
	return openai.OpenAIChatRequest{Stream: stream}
}

func openAIRequestWithN(n *int) openai.OpenAIChatRequest {
	return openai.OpenAIChatRequest{ChatParameters: schemas.ChatParameters{N: n}}
}

type humeMetadataProbePlugin struct {
	metadata *schemas.HumeChatRequestMetadata
}

var _ schemas.LLMPlugin = (*humeMetadataProbePlugin)(nil)

func (p *humeMetadataProbePlugin) GetName() string { return "hume-metadata-probe" }
func (p *humeMetadataProbePlugin) Cleanup() error  { return nil }

func (p *humeMetadataProbePlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *humeMetadataProbePlugin) PreLLMHook(_ *schemas.BifrostContext, request *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	p.metadata, _ = schemas.GetChatIntegrationMetadata[*schemas.HumeChatRequestMetadata](request.ChatRequest)
	return request, nil, nil
}

func (p *humeMetadataProbePlugin) PostLLMHook(_ *schemas.BifrostContext, response *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return response, bifrostErr, nil
}
