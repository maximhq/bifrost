package integrations

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/providers/cohere"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestParsePassthroughBody_MultipartExtractsModelAfterFilePart(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	fileWriter, err := writer.CreateFormFile("file", "sample.mp3")
	require.NoError(t, err)
	_, err = fileWriter.Write([]byte("audio-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.WriteField("model", "openai/whisper-1"))
	require.NoError(t, writer.WriteField("stream", "true"))
	require.NoError(t, writer.Close())

	model, stream := parsePassthroughBody(writer.FormDataContentType(), body.Bytes())
	assert.Equal(t, "openai/whisper-1", model)
	assert.True(t, stream)
}

func TestRequestWithSettableExtraParams_OpenAIChatRequest(t *testing.T) {
	t.Run("SetExtraParams populates both standalone and embedded ExtraParams", func(t *testing.T) {
		req := &openai.OpenAIChatRequest{}
		extra := map[string]interface{}{
			"guardrailConfig": map[string]interface{}{
				"guardrailIdentifier": "xxx",
				"guardrailVersion":    "1",
			},
		}

		rws, ok := interface{}(req).(RequestWithSettableExtraParams)
		require.True(t, ok, "OpenAIChatRequest should implement RequestWithSettableExtraParams")

		rws.SetExtraParams(extra)

		assert.Equal(t, extra, req.GetExtraParams())
		assert.Equal(t, extra, req.ChatParameters.ExtraParams, "embedded ChatParameters.ExtraParams should also be set")
	})

	t.Run("extra params propagate through ToBifrostChatRequest", func(t *testing.T) {
		req := &openai.OpenAIChatRequest{
			Model:    "bedrock/claude-4-5-sonnet-global",
			Messages: []openai.OpenAIMessage{},
		}
		extra := map[string]interface{}{
			"guardrailConfig": map[string]interface{}{
				"guardrailIdentifier": "test-id",
				"guardrailVersion":    "1",
			},
		}

		rws := interface{}(req).(RequestWithSettableExtraParams)
		rws.SetExtraParams(extra)

		ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		bifrostReq := req.ToBifrostChatRequest(ctx)

		require.NotNil(t, bifrostReq)
		require.NotNil(t, bifrostReq.Params)
		assert.Contains(t, bifrostReq.Params.ExtraParams, "guardrailConfig")
	})
}

func TestRequestWithSettableExtraParams_AllOpenAIRequestTypes(t *testing.T) {
	tests := []struct {
		name string
		req  interface{}
	}{
		{"OpenAIChatRequest", &openai.OpenAIChatRequest{}},
		{"OpenAITextCompletionRequest", &openai.OpenAITextCompletionRequest{}},
		{"OpenAIResponsesRequest", &openai.OpenAIResponsesRequest{}},
		{"OpenAIEmbeddingRequest", &openai.OpenAIEmbeddingRequest{}},
		{"OpenAISpeechRequest", &openai.OpenAISpeechRequest{}},
		{"OpenAIImageGenerationRequest", &openai.OpenAIImageGenerationRequest{}},
		{"OpenAIImageEditRequest", &openai.OpenAIImageEditRequest{}},
		{"OpenAIImageVariationRequest", &openai.OpenAIImageVariationRequest{}},
	}

	for _, tt := range tests {
		t.Run(tt.name+" implements RequestWithSettableExtraParams", func(t *testing.T) {
			rws, ok := tt.req.(RequestWithSettableExtraParams)
			require.True(t, ok, "%s should implement RequestWithSettableExtraParams", tt.name)

			extra := map[string]interface{}{"test_key": "test_value"}
			rws.SetExtraParams(extra)

			getter, ok := tt.req.(interface{ GetExtraParams() map[string]interface{} })
			require.True(t, ok, "%s should implement GetExtraParams", tt.name)
			assert.Equal(t, extra, getter.GetExtraParams())
		})
	}
}

func TestRequestWithSettableExtraParams_AllProviderRequestTypes(t *testing.T) {
	tests := []struct {
		name string
		req  interface{}
	}{
		{"AnthropicMessageRequest", &anthropic.AnthropicMessageRequest{}},
		{"AnthropicTextRequest", &anthropic.AnthropicTextRequest{}},
		{"BedrockConverseRequest", &bedrock.BedrockConverseRequest{}},
		{"BedrockTextCompletionRequest", &bedrock.BedrockTextCompletionRequest{}},
		{"BedrockTitanEmbeddingRequest", &bedrock.BedrockTitanEmbeddingRequest{}},
		{"BedrockCohereEmbeddingRequest", &bedrock.BedrockCohereEmbeddingRequest{}},
		{"BedrockImageGenerationRequest", &bedrock.BedrockImageGenerationRequest{}},
		{"BedrockImageVariationRequest", &bedrock.BedrockImageVariationRequest{}},
		{"BedrockImageEditRequest", &bedrock.BedrockImageEditRequest{}},
		{"StabilityAIImageGenerationRequest", &bedrock.StabilityAIImageGenerationRequest{}},
		{"StabilityAIImageEditRequest", &bedrock.StabilityAIImageEditRequest{}},
		{"BedrockInvokeRequest", &bedrock.BedrockInvokeRequest{}},
		{"BedrockRerankRequest", &bedrock.BedrockRerankRequest{}},
		{"GeminiGenerationRequest", &gemini.GeminiGenerationRequest{}},
		{"GeminiBatchEmbeddingRequest", &gemini.GeminiBatchEmbeddingRequest{}},
		{"GeminiEmbeddingRequest", &gemini.GeminiEmbeddingRequest{}},
		{"GeminiImagenRequest", &gemini.GeminiImagenRequest{}},
		{"GeminiVideoGenerationRequest", &gemini.GeminiVideoGenerationRequest{}},
		{"CohereChatRequest", &cohere.CohereChatRequest{}},
		{"CohereCountTokensRequest", &cohere.CohereCountTokensRequest{}},
		{"CohereEmbeddingRequest", &cohere.CohereEmbeddingRequest{}},
		{"CohereRerankRequest", &cohere.CohereRerankRequest{}},
	}

	for _, tt := range tests {
		t.Run(tt.name+" implements RequestWithSettableExtraParams", func(t *testing.T) {
			rws, ok := tt.req.(RequestWithSettableExtraParams)
			require.True(t, ok, "%s should implement RequestWithSettableExtraParams", tt.name)

			extra := map[string]interface{}{"test_key": "test_value"}
			rws.SetExtraParams(extra)

			getter, ok := tt.req.(interface{ GetExtraParams() map[string]interface{} })
			require.True(t, ok, "%s should implement GetExtraParams", tt.name)
			assert.Equal(t, extra, getter.GetExtraParams())
		})
	}
}

func TestExtraParamsRequiresPassthroughHeader(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	routes := CreateOpenAIRouteConfigs("/openai", handlerStore)

	var chatRoute *RouteConfig
	for i := range routes {
		if routes[i].Path == "/openai/v1/chat/completions" {
			chatRoute = &routes[i]
			break
		}
	}
	require.NotNil(t, chatRoute, "should find /openai/v1/chat/completions route")

	rawBody := []byte(`{
		"model": "bedrock/claude-4-5-sonnet-global",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
		"extra_params": {
			"guardrailConfig": {
				"guardrailIdentifier": "my-guardrail",
				"guardrailVersion": "1",
				"trace": "disabled"
			}
		}
	}`)

	t.Run("extra_params NOT extracted without passthrough header", func(t *testing.T) {
		req := chatRoute.GetRequestTypeInstance(context.Background())
		err := sonic.Unmarshal(rawBody, req)
		require.NoError(t, err)

		bifrostCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		// Header not set -- simulate router logic
		if bifrostCtx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
			if rws, ok := req.(RequestWithSettableExtraParams); ok {
				var wrapper struct {
					ExtraParams map[string]interface{} `json:"extra_params"`
				}
				if err := sonic.Unmarshal(rawBody, &wrapper); err == nil && len(wrapper.ExtraParams) > 0 {
					rws.SetExtraParams(wrapper.ExtraParams)
				}
				_ = rws
			}
		}

		openaiReq, ok := req.(*openai.OpenAIChatRequest)
		require.True(t, ok)
		assert.Empty(t, openaiReq.ChatParameters.ExtraParams,
			"ExtraParams should be empty when passthrough header is not set")
	})

	t.Run("extra_params extracted with passthrough header", func(t *testing.T) {
		req := chatRoute.GetRequestTypeInstance(context.Background())
		err := sonic.Unmarshal(rawBody, req)
		require.NoError(t, err)

		bifrostCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		bifrostCtx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

		if bifrostCtx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
			if rws, ok := req.(RequestWithSettableExtraParams); ok {
				var wrapper struct {
					ExtraParams map[string]interface{} `json:"extra_params"`
				}
				if err := sonic.Unmarshal(rawBody, &wrapper); err == nil && len(wrapper.ExtraParams) > 0 {
					rws.SetExtraParams(wrapper.ExtraParams)
				}
			}
		}

		openaiReq, ok := req.(*openai.OpenAIChatRequest)
		require.True(t, ok)
		require.Contains(t, openaiReq.ChatParameters.ExtraParams, "guardrailConfig",
			"guardrailConfig should be in ExtraParams when passthrough header is set")

		gc, ok := openaiReq.ChatParameters.ExtraParams["guardrailConfig"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "my-guardrail", gc["guardrailIdentifier"])
		assert.Equal(t, "1", gc["guardrailVersion"])
		assert.Equal(t, "disabled", gc["trace"])
	})
}

func TestExtraParamsPassthrough_NestedStructures(t *testing.T) {
	rawBody := []byte(`{
		"model": "openai/gpt-4o-mini",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
		"extra_params": {
			"custom_param": "value",
			"another_param": 123,
			"nested": {
				"deep_field": "deep_value",
				"deeper": {"level": 3}
			}
		}
	}`)

	req := &openai.OpenAIChatRequest{}
	err := sonic.Unmarshal(rawBody, req)
	require.NoError(t, err)

	bifrostCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostCtx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	if bifrostCtx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
		if rws, ok := interface{}(req).(RequestWithSettableExtraParams); ok {
			var wrapper struct {
				ExtraParams map[string]interface{} `json:"extra_params"`
			}
			if err := sonic.Unmarshal(rawBody, &wrapper); err == nil && len(wrapper.ExtraParams) > 0 {
				rws.SetExtraParams(wrapper.ExtraParams)
			}
		}
	}

	require.Len(t, req.ChatParameters.ExtraParams, 3)
	assert.Equal(t, "value", req.ChatParameters.ExtraParams["custom_param"])
	assert.Equal(t, float64(123), req.ChatParameters.ExtraParams["another_param"])

	nested, ok := req.ChatParameters.ExtraParams["nested"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "deep_value", nested["deep_field"])
}

func TestExtraParamsPassthrough_EndToEnd(t *testing.T) {
	rawJSON := []byte(`{
		"model": "bedrock/claude-4-5-sonnet-global",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
		"stream": false,
		"temperature": 0.7,
		"extra_params": {
			"guardrailConfig": {
				"guardrailIdentifier": "my-guardrail",
				"guardrailVersion": "1",
				"trace": "disabled"
			}
		}
	}`)

	req := &openai.OpenAIChatRequest{}
	err := sonic.Unmarshal(rawJSON, req)
	require.NoError(t, err)
	assert.Equal(t, "bedrock/claude-4-5-sonnet-global", req.Model)

	bifrostCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostCtx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	if bifrostCtx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
		if rws, ok := interface{}(req).(RequestWithSettableExtraParams); ok {
			var wrapper struct {
				ExtraParams map[string]interface{} `json:"extra_params"`
			}
			if err := sonic.Unmarshal(rawJSON, &wrapper); err == nil && len(wrapper.ExtraParams) > 0 {
				rws.SetExtraParams(wrapper.ExtraParams)
			}
		}
	}

	bifrostReq := req.ToBifrostChatRequest(bifrostCtx)

	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.Params)
	require.Contains(t, bifrostReq.Params.ExtraParams, "guardrailConfig")

	gc, ok := bifrostReq.Params.ExtraParams["guardrailConfig"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "my-guardrail", gc["guardrailIdentifier"])
	assert.Equal(t, "1", gc["guardrailVersion"])
	assert.Equal(t, "disabled", gc["trace"])

	assert.NotContains(t, bifrostReq.Params.ExtraParams, "model")
	assert.NotContains(t, bifrostReq.Params.ExtraParams, "messages")
	assert.NotContains(t, bifrostReq.Params.ExtraParams, "stream")
	assert.NotContains(t, bifrostReq.Params.ExtraParams, "temperature")
}

func TestExtraParamsPassthrough_NoExtraParamsKey(t *testing.T) {
	rawBody := []byte(`{
		"model": "openai/gpt-4o-mini",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}]
	}`)

	req := &openai.OpenAIChatRequest{}
	err := sonic.Unmarshal(rawBody, req)
	require.NoError(t, err)

	bifrostCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostCtx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	if bifrostCtx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
		if rws, ok := interface{}(req).(RequestWithSettableExtraParams); ok {
			var wrapper struct {
				ExtraParams map[string]interface{} `json:"extra_params"`
			}
			if err := sonic.Unmarshal(rawBody, &wrapper); err == nil && len(wrapper.ExtraParams) > 0 {
				rws.SetExtraParams(wrapper.ExtraParams)
			}
			_ = rws
		}
	}

	assert.Empty(t, req.ChatParameters.ExtraParams,
		"ExtraParams should be empty when extra_params key is absent from JSON")
}

func TestOpenAIChatStructuredOutputRequestParserAndConverter(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	routes := CreateOpenAIRouteConfigs("", handlerStore)

	var chatRoute *RouteConfig
	for i := range routes {
		if routes[i].Path == "/v1/chat/completions" {
			chatRoute = &routes[i]
			break
		}
	}
	require.NotNil(t, chatRoute)

	rawBody := []byte(`{
		"model": "gemini/gemini-2.5-flash",
		"messages": [
			{
				"role": "user",
				"content": "Extract city/country/population for Paris."
			}
		],
		"stream": false,
		"response_format": {
			"type": "json_schema",
			"json_schema": {
				"name": "city",
				"strict": true,
				"schema": {
					"type": "object",
					"properties": {
						"city": {"type": "string"},
						"country": {"type": "string"},
						"population": {"type": "number"}
					},
					"required": ["city", "country", "population"],
					"additionalProperties": false
				}
			}
		}
	}`)

	req := chatRoute.GetRequestTypeInstance(context.Background())
	require.NoError(t, parseJSONRequestBody(rawBody, req))

	bifrostCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq, err := chatRoute.RequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.ChatRequest)
	require.NotNil(t, bifrostReq.ChatRequest.Params)
	require.NotNil(t, bifrostReq.ChatRequest.Params.ResponseFormat)

	assert.Equal(t, schemas.Gemini, bifrostReq.ChatRequest.Provider)
	assert.Equal(t, "gemini-2.5-flash", bifrostReq.ChatRequest.Model)
	assert.False(t, req.(*openai.OpenAIChatRequest).IsStreamingRequested())

	responseFormat, ok := (*bifrostReq.ChatRequest.Params.ResponseFormat).(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "json_schema", responseFormat["type"])
	assert.Contains(t, responseFormat, "json_schema")
}

// TestCreateHandler_AnthropicRouteSetsPassthroughFlags verifies that a Claude
// Code request on the Anthropic route is marked for raw-body passthrough by the
// checkAnthropicPassthrough pre-callback, and that the flags are still set at
// converter time. The router does not clear them when the model later resolves
// to a non-native provider (e.g. Bedrock) — that happens per attempt in core
// (clearAnthropicPassthroughForNonNativeProvider), after final provider
// resolution.
func TestCreateHandler_AnthropicRouteSetsPassthroughFlags(t *testing.T) {
	handlerStore := &mockHandlerStore{}

	var capturedUseRaw interface{}
	var capturedSendRawResponse interface{}
	var capturedPassthroughOverrides interface{}
	route := RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   "/v1/messages",
		Method: fasthttp.MethodPost,
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.ResponsesRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &anthropic.AnthropicMessageRequest{}
		},
		PreCallback: checkAnthropicPassthrough,
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			capturedUseRaw = ctx.Value(schemas.BifrostContextKeyUseRawRequestBody)
			capturedSendRawResponse = ctx.Value(schemas.BifrostContextKeySendBackRawResponse)
			capturedPassthroughOverrides = ctx.Value(schemas.BifrostContextKeyPassthroughOverridesPresent)
			return nil, fmt.Errorf("stop before bifrost execution")
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
	}

	router := NewGenericRouter(nil, handlerStore, nil, nil, nil)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.Header.Set("user-agent", "claude-code/1.0")
	ctx.Request.SetBodyString(`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)

	router.createHandler(route)(ctx)

	// Non-Bifrost errors without an explicit status code map to 400 (see
	// GenericRouter.sendError), so the converter's sentinel error surfaces as one.
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	require.Equal(t, true, capturedUseRaw, "UseRawRequestBody should be set for a Claude Code request")
	require.Equal(t, true, capturedSendRawResponse, "SendBackRawResponse should be set for a Claude Code request")
	require.Equal(t, true, capturedPassthroughOverrides, "PassthroughOverridesPresent should be set for a Claude Code request")
}

func TestCreateHandler_CustomParserFailureClosesConnection(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	converterCalled := false
	route := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/v1/chat/completions",
		Method: fasthttp.MethodPost,
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.ChatCompletionRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &openai.OpenAIChatRequest{}
		},
		RequestParser: func(ctx *fasthttp.RequestCtx, req interface{}) error {
			return parseJSONRequestBody(ctx.Request.Body(), req)
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			converterCalled = true
			return nil, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
	}

	router := NewGenericRouter(nil, handlerStore, nil, nil, nil)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetBodyString(`{"model":"gemini/gemini-2.5-flash","messages":[]}}`)

	router.createHandler(route)(ctx)

	assert.False(t, converterCalled)
	assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	assert.True(t, ctx.Response.ConnectionClose())
	assert.Contains(t, string(ctx.Response.Body()), "invalid JSON request body")
	assert.Contains(t, string(ctx.Response.Body()), "length")
}

func TestCreateHandler_DefaultJSONParserFailureClosesConnection(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/v1/test",
		Method: fasthttp.MethodPost,
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.ChatCompletionRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &openai.OpenAIChatRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			return nil, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
	}

	router := NewGenericRouter(nil, handlerStore, nil, nil, nil)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetBodyString(`{"model":"gemini/gemini-2.5-flash","messages":[]}x`)

	router.createHandler(route)(ctx)

	assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	assert.True(t, ctx.Response.ConnectionClose())
	assert.Contains(t, string(ctx.Response.Body()), "invalid JSON request body")
}

func TestCreateHandler_ParseFailureClosesKeepAliveSocket(t *testing.T) {
	route := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/v1/chat/completions",
		Method: fasthttp.MethodPost,
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.ChatCompletionRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &openai.OpenAIChatRequest{}
		},
		ShortCircuit: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) (bool, error) {
			ctx.SetStatusCode(fasthttp.StatusOK)
			ctx.SetBodyString(`{"ok":true}`)
			return true, nil
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			t.Fatal("RequestConverter should not run when ShortCircuit handles the request")
			return nil, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
	}
	router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil)
	server := &fasthttp.Server{
		Handler: router.createHandler(route),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		_ = server.Serve(ln)
	}()
	defer server.Shutdown()

	t.Run("valid keep-alive requests reuse the socket", func(t *testing.T) {
		conn, err := net.Dial("tcp", ln.Addr().String())
		require.NoError(t, err)
		defer conn.Close()
		reader := bufio.NewReader(conn)
		body := `{"model":"gemini/gemini-2.5-flash","messages":[]}`
		req := fmt.Sprintf("POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nConnection: keep-alive\r\nContent-Length: %d\r\n\r\n%s", len(body), body)

		_, err = conn.Write([]byte(req + req))
		require.NoError(t, err)
		resp1, err := http.ReadResponse(reader, nil)
		require.NoError(t, err)
		_, err = io.ReadAll(resp1.Body)
		require.NoError(t, err)
		require.NoError(t, resp1.Body.Close())
		assert.Equal(t, http.StatusOK, resp1.StatusCode)
		assert.False(t, resp1.Close)

		resp2, err := http.ReadResponse(reader, nil)
		require.NoError(t, err)
		_, err = io.ReadAll(resp2.Body)
		require.NoError(t, err)
		require.NoError(t, resp2.Body.Close())
		assert.Equal(t, http.StatusOK, resp2.StatusCode)
		assert.False(t, resp2.Close)
	})

	t.Run("malformed request closes the socket", func(t *testing.T) {
		conn, err := net.Dial("tcp", ln.Addr().String())
		require.NoError(t, err)
		defer conn.Close()
		reader := bufio.NewReader(conn)
		body := `{"model":"gemini/gemini-2.5-flash","messages":[]}x`
		req := fmt.Sprintf("POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nConnection: keep-alive\r\nContent-Length: %d\r\n\r\n%s", len(body), body)

		_, err = conn.Write([]byte(req))
		require.NoError(t, err)
		resp, err := http.ReadResponse(reader, nil)
		require.NoError(t, err)
		_, err = io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.True(t, resp.Close)

		require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
		_, err = reader.Peek(1)
		require.Error(t, err)
	})
}

// TestExtraParamsSetViaInterfaceMutatesOriginalReq verifies that setting extra
// params through the RequestWithSettableExtraParams interface assertion mutates
// the original req (interface{}) value. This matters because createHandler
// passes req to config.RequestConverter after the extra params block -- both
// variables must reference the same underlying struct via pointer semantics.
func TestExtraParamsSetViaInterfaceMutatesOriginalReq(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	routes := CreateOpenAIRouteConfigs("/openai", handlerStore)

	var chatRoute *RouteConfig
	for i := range routes {
		if routes[i].Path == "/openai/v1/chat/completions" {
			chatRoute = &routes[i]
			break
		}
	}
	require.NotNil(t, chatRoute)

	rawBody := []byte(`{
		"model": "bedrock/claude-4-5-sonnet-global",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
		"extra_params": {
			"guardrailConfig": {
				"guardrailIdentifier": "my-guardrail",
				"guardrailVersion": "1"
			}
		}
	}`)

	// Simulate the exact flow in createHandler:
	// 1. req is created via GetRequestTypeInstance (returns interface{})
	// 2. JSON is unmarshalled into req
	// 3. rws type assertion is used to call SetExtraParams
	// 4. req (not rws) is passed to RequestConverter downstream
	req := chatRoute.GetRequestTypeInstance(context.Background()) // returns interface{}
	err := sonic.Unmarshal(rawBody, req)
	require.NoError(t, err)

	// Type-assert and set extra params (same as router code)
	if rws, ok := req.(RequestWithSettableExtraParams); ok {
		var wrapper struct {
			ExtraParams map[string]interface{} `json:"extra_params"`
		}
		if err := sonic.Unmarshal(rawBody, &wrapper); err == nil && len(wrapper.ExtraParams) > 0 {
			rws.SetExtraParams(wrapper.ExtraParams)
		}
	}

	// Verify that req (the original interface{} variable) was mutated
	openaiReq, ok := req.(*openai.OpenAIChatRequest)
	require.True(t, ok)
	require.Contains(t, openaiReq.ChatParameters.ExtraParams, "guardrailConfig",
		"original req should be mutated via pointer semantics")

	// Verify the full downstream path: RequestConverter uses req
	bifrostCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq, err := chatRoute.RequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.ChatRequest)
	require.NotNil(t, bifrostReq.ChatRequest.Params)
	assert.Contains(t, bifrostReq.ChatRequest.Params.ExtraParams, "guardrailConfig",
		"extra params should propagate through RequestConverter to BifrostChatRequest")
}

// TestExtractModelFromPath covers model extraction across provider path styles: GenAI
// models/tunedModels (with :action suffixes), Vertex fully-qualified publisher paths, and
// Azure OpenAI deployments/{deployment} (where the deployment name is the model identifier).
func TestExtractModelFromPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"azure deployment chat", "/openai/deployments/my-gpt4o/chat/completions", "my-gpt4o"},
		{"azure deployment leading-stripped", "openai/deployments/prod-embed-3/embeddings", "prod-embed-3"},
		{"genai models with action", "/v1beta/models/gemini-2.5-pro:generateContent", "gemini-2.5-pro"},
		{"genai models stream action", "/models/gemini-2.5-flash:streamGenerateContent", "gemini-2.5-flash"},
		{"genai tunedModels", "/v1beta/tunedModels/my-tuned-1:generateContent", "my-tuned-1"},
		{"vertex fully-qualified", "/projects/p/locations/us-central1/publishers/google/models/gemini-3-pro:streamGenerateContent", "gemini-3-pro"},
		{"no model segment", "/v1/chat/completions", ""},
		{"deployments with no trailing segment", "/openai/deployments", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractModelFromPath(tt.path); got != tt.want {
				t.Fatalf("extractModelFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestExtractPassthroughModel verifies the path value wins when present, and the body model is
// used as a fallback — notably for Azure deployment routes where the body usually omits "model".
func TestExtractPassthroughModel(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		bodyModel string
		want      string
	}{
		{"azure deployment path overrides empty body", "/openai/deployments/my-gpt4o/chat/completions", "", "my-gpt4o"},
		{"body fallback when path has no model", "/openai/v1/chat/completions", "gpt-4o", "gpt-4o"},
		{"path wins over body", "/openai/deployments/dep-a/chat/completions", "ignored-body-model", "dep-a"},
		{"both empty", "/v1/chat/completions", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractPassthroughModel(tt.path, tt.bodyModel); got != tt.want {
				t.Fatalf("extractPassthroughModel(%q, %q) = %q, want %q", tt.path, tt.bodyModel, got, tt.want)
			}
		})
	}
}
