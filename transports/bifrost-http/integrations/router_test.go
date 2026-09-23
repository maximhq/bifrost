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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/anthropic"
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

func TestChatGPTPassthroughRouterRegistersCodexResponsesPost(t *testing.T) {
	r := router.New()
	passthroughRouter := NewChatGPTPassthroughRouter(nil, &mockHandlerStore{}, nil, &testLogger{})
	passthroughRouter.RegisterRoutes(r, func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			ctx.SetStatusCode(fasthttp.StatusNoContent)
		}
	})

	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/chatgpt_passthrough/backend-api/codex/responses")

	r.Handler(&ctx)

	require.Equal(t, fasthttp.StatusNoContent, ctx.Response.StatusCode())
}

func TestRunwarePassthroughRouterRegistersCatchAll(t *testing.T) {
	r := router.New()
	passthroughRouter := NewRunwarePassthroughRouter(nil, &mockHandlerStore{}, nil, &testLogger{})
	passthroughRouter.RegisterRoutes(r, func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			ctx.SetStatusCode(fasthttp.StatusNoContent)
		}
	})

	// Runware is a single-endpoint API, so the passthrough router forwards any path under the
	// prefix; /runware_passthrough/v1 is the canonical way clients hit the base endpoint.
	for _, uri := range []string{"/runware_passthrough/v1", "/runware_passthrough/v1/anything"} {
		var ctx fasthttp.RequestCtx
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		ctx.Request.SetRequestURI(uri)

		r.Handler(&ctx)

		require.Equal(t, fasthttp.StatusNoContent, ctx.Response.StatusCode(), "POST %s should match a registered route", uri)
	}
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
						"population": {"type": "number", "multipleOf": 1.50}
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

	// Object-valued response_format is carried as json.RawMessage now, so that the client's
	// exact bytes (numeric literals included) survive to the provider. Read it through the
	// shared accessor rather than asserting one concrete representation.
	responseFormat, ok := schemas.ParseChatResponseFormat(bifrostReq.ChatRequest.Params.ResponseFormat)
	require.True(t, ok)
	assert.Equal(t, "json_schema", responseFormat.Type)
	assert.True(t, responseFormat.HasJSONSchema(), "the json_schema payload must survive the conversion")

	name, ok := responseFormat.Name()
	require.True(t, ok)
	assert.Equal(t, "city", name)

	// HasJSONSchema only proves json_schema is an object and Name only reads its name, so
	// neither would notice conversion dropping or replacing json_schema.schema. Assert the
	// retained schema payload itself, which is the contract the comment above claims.
	rawSchema := responseFormat.RawSchema()
	require.NotEmpty(t, rawSchema, "the json_schema.schema payload must survive the conversion")

	schemaStr := string(rawSchema)
	assert.Contains(t, schemaStr, `"population"`, "every declared property must survive")
	assert.Contains(t, schemaStr, `"additionalProperties"`, "schema keywords must survive, not just properties")

	// Property order is the thing this PR exists to preserve, so pin it rather than just
	// membership: city before country before population, as the client wrote them.
	iCity := strings.Index(schemaStr, `"city"`)
	iCountry := strings.Index(schemaStr, `"country"`)
	iPopulation := strings.Index(schemaStr, `"population"`)
	require.True(t, iCity >= 0 && iCountry >= 0 && iPopulation >= 0, "schema: %s", schemaStr)
	assert.Less(t, iCity, iCountry, "client property order must survive; schema: %s", schemaStr)
	assert.Less(t, iCountry, iPopulation, "client property order must survive; schema: %s", schemaStr)

	// The numeric literal is the only thing in this payload a re-encoder can actually
	// damage: "number" above is a string, so without a real numeric token the assertions
	// above would pass even if 1.50 arrived as 1.5. This is what makes the byte-preservation
	// claim in the comment at the top of this block testable rather than aspirational.
	assert.Contains(t, schemaStr, "1.50",
		"the client's exact numeric literal must survive; a re-encode would render it 1.5; schema: %s", schemaStr)
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

	router := NewGenericRouter(nil, handlerStore, nil, nil, nil, nil)
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

// anthropicThreadTestRoute mirrors the production /v1/messages route config
// (checkAnthropicPassthrough + anthropicRefuseThreadContinue) with a sentinel
// converter so the request never reaches a nil bifrost client.
func anthropicThreadTestRoute(converterCalled *bool) RouteConfig {
	return RouteConfig{
		Type:   RouteConfigTypeAnthropic,
		Path:   "/v1/messages",
		Method: fasthttp.MethodPost,
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.ResponsesRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &anthropic.AnthropicMessageRequest{}
		},
		PreCallback:  checkAnthropicPassthrough,
		ShortCircuit: anthropicRefuseThreadContinue,
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			*converterCalled = true
			return nil, fmt.Errorf("stop before bifrost execution")
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return anthropic.ToAnthropicChatCompletionError(err)
		},
	}
}

// TestCreateHandler_AnthropicThreadContinueRefused verifies the stateless thread
// handling: a `thread: {"type": "continue"}` request carries only the conversation
// delta, which Bifrost cannot serve because thread state is bound to the upstream
// account that created it. The request is refused before the Bifrost flow with the
// thread_unsupported_request error code, which makes the client resend the turn in
// full and drop the thread field for the rest of the session.
func TestCreateHandler_AnthropicThreadContinueRefused(t *testing.T) {
	converterCalled := false
	router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, nil)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.Header.Set("user-agent", "claude-code/1.0")
	ctx.Request.SetBodyString(`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"thread":{"type":"continue","previous_message_id":"msg_123"}}`)

	router.createHandler(anthropicThreadTestRoute(&converterCalled))(ctx)

	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	require.False(t, converterCalled, "a refused continuation must never reach the Bifrost flow")
	require.Equal(t, "application/json", string(ctx.Response.Header.ContentType()))

	var envelope anthropic.AnthropicMessageError
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &envelope))
	require.Equal(t, "error", envelope.Type)
	require.Equal(t, "invalid_request_error", envelope.Error.Type)
	require.NotNil(t, envelope.Error.Details)
	require.Equal(t, "thread_unsupported_request", envelope.Error.Details.ErrorCode)
	require.NotEmpty(t, envelope.Error.Message)
}

// TestCreateHandler_AnthropicThreadContinueRefusedStreaming pins that the refusal
// is a plain JSON response even when the client requested a stream: the
// short-circuit runs before any SSE stream starts, matching how the upstream
// returns pre-stream errors.
func TestCreateHandler_AnthropicThreadContinueRefusedStreaming(t *testing.T) {
	converterCalled := false
	router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, nil)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.Header.Set("user-agent", "claude-code/1.0")
	ctx.Request.SetBodyString(`{"model":"claude-opus-4-8","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}],"thread":{"type":"continue","previous_message_id":"msg_123"}}`)

	router.createHandler(anthropicThreadTestRoute(&converterCalled))(ctx)

	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	require.False(t, converterCalled)
	body := string(ctx.Response.Body())
	require.Contains(t, body, "thread_unsupported_request")
	require.NotContains(t, body, "event:", "the refusal must be plain JSON, not SSE framing")
}

// TestCreateHandler_AnthropicThreadCreateProceeds verifies a thread create request
// is not refused: it carries the full conversation, so it proceeds into the Bifrost
// flow (where the provider's raw-body path strips the field before the wire).
func TestCreateHandler_AnthropicThreadCreateProceeds(t *testing.T) {
	converterCalled := false
	router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, nil)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.Header.Set("user-agent", "claude-code/1.0")
	ctx.Request.SetBodyString(`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"thread":{"type":"create"}}`)

	router.createHandler(anthropicThreadTestRoute(&converterCalled))(ctx)

	require.True(t, converterCalled, "a thread create request must proceed into the Bifrost flow")
	require.NotContains(t, string(ctx.Response.Body()), "thread_unsupported_request")
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

	router := NewGenericRouter(nil, handlerStore, nil, nil, nil, nil)
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

	router := NewGenericRouter(nil, handlerStore, nil, nil, nil, nil)
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
	router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, nil)
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
		// Vertex/GenAI bodies (cachedContents, batch jobs) name the model as a resource path;
		// governance and key selection match bare ids, so it must be reduced to one.
		{"vertex resource body model", "/projects/p/locations/global/cachedContents", "projects/p/locations/global/publishers/google/models/gemini-3.7-flash", "gemini-3.7-flash"},
		{"genai resource body model", "/v1beta/cachedContents", "models/gemini-2.5-flash", "gemini-2.5-flash"},
		{"slashed non-resource model untouched", "/api/v1/chat/completions", "openai/gpt-4o", "openai/gpt-4o"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractPassthroughModel(tt.path, tt.bodyModel); got != tt.want {
				t.Fatalf("extractPassthroughModel(%q, %q) = %q, want %q", tt.path, tt.bodyModel, got, tt.want)
			}
		})
	}
}

// Caller-auth forwarding for passthrough routes: OAuth/JWT bearer tokens are the
// upstream credential (Claude Code, ChatGPT/Codex) and must survive to the provider,
// while plain API keys keep the strip-and-inject behavior.
func TestApplyPassthroughCallerAuth_AnthropicOAuthForwarded(t *testing.T) {
	bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	safeHeaders := map[string]string{}
	applyPassthroughCallerAuth(bifrostCtx, safeHeaders, schemas.Anthropic, "Bearer sk-ant-oat01-caller-token", "")
	if got := safeHeaders["authorization"]; got != "Bearer sk-ant-oat01-caller-token" {
		t.Fatalf("expected OAuth token forwarded in safe headers, got %q", got)
	}
	if skip, _ := bifrostCtx.Value(schemas.BifrostContextKeySkipKeySelection).(bool); !skip {
		t.Fatal("expected SkipKeySelection to be set for OAuth passthrough")
	}
}

func TestApplyPassthroughCallerAuth_OpenAIJWTForwarded(t *testing.T) {
	bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	safeHeaders := map[string]string{}
	jwt := "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJjb2RleCJ9.c2ln"
	applyPassthroughCallerAuth(bifrostCtx, safeHeaders, schemas.OpenAI, jwt, "https://chatgpt.com")
	if got := safeHeaders["authorization"]; got != jwt {
		t.Fatalf("expected JWT forwarded in safe headers, got %q", got)
	}
	if skip, _ := bifrostCtx.Value(schemas.BifrostContextKeySkipKeySelection).(bool); !skip {
		t.Fatal("expected SkipKeySelection to be set for JWT passthrough")
	}
}

func TestApplyPassthroughCallerAuth_APIKeysStayStripped(t *testing.T) {
	for name, tc := range map[string]struct {
		provider    schemas.ModelProvider
		auth        string
		upstreamURL string
	}{
		"openai plain api key":      {schemas.OpenAI, "Bearer sk-plain-api-key", ""},
		"anthropic api key bearer":  {schemas.Anthropic, "Bearer sk-ant-api03-key", ""},
		"provider override bedrock": {schemas.Bedrock, "Bearer sk-ant-oat01-caller-token", ""},
		"openai two-segment token":  {schemas.OpenAI, "Bearer eyJhbGciOiJSUzI1NiJ9.c2ln", ""},
		"http upstream override":    {schemas.OpenAI, "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJjb2RleCJ9.c2ln", "http://mock.local"},
	} {
		t.Run(name, func(t *testing.T) {
			bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			safeHeaders := map[string]string{}
			applyPassthroughCallerAuth(bifrostCtx, safeHeaders, tc.provider, tc.auth, tc.upstreamURL)
			if _, ok := safeHeaders["authorization"]; ok {
				t.Fatal("authorization must stay stripped")
			}
			if _, ok := bifrostCtx.Value(schemas.BifrostContextKeySkipKeySelection).(bool); ok {
				t.Fatal("SkipKeySelection must not be set")
			}
		})
	}
}

// Test_handleStreaming_RetentionCancelsAndLeavesNoGoroutine is the regression test for the
// client-disconnect watcher leak found in a production heap dump.
//
// ConvertToBifrostContext starts one lib.startClientDisconnectWatcher goroutine per
// request. That goroutine's only exits are an explicit cancel or the client socket dying;
// its parent is fasthttp's RequestCtx, whose Done fires only on server shutdown. So a
// handler that returns without cancelling leaves the watcher polling the socket every
// 500ms forever, pinning the whole request-scoped BifrostContext with it.
//
// handleStreaming used to cancel ONLY on write errors ("client disconnected"), which meant
// every SUCCESSFUL stream leaked a watcher plus its context until the client's keep-alive
// connection closed. Two production pods showed 596 and 546 watcher goroutines behind just
// 34 and 38 fasthttp connection goroutines -- roughly 15 leaked contexts per open
// connection, holding about 2.0 GB of a 2.66 GB heap that GC could not reclaim because it
// was all genuinely reachable.
//
// A stream that ends normally must cancel, exactly as the client-disconnect and
// passthrough paths already do.
func Test_handleStreaming_RetentionCancelsAndLeavesNoGoroutine(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk)
	router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, bifrost.NewNoOpLogger())
	ctx := &fasthttp.RequestCtx{}

	rec := newCancelRecorder()
	router.handleStreaming(ctx, nil, RouteConfig{}, stream, rec.cancel)

	// Drain the response body so the producer's SendEvent calls succeed and it reaches
	// its normal end-of-stream path rather than the write-error path (which has always
	// cancelled, and would make this test pass for the wrong reason).
	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(ctx.Response.BodyStream())
		readDone <- err
	}()

	stream <- &schemas.BifrostStreamChunk{}
	close(stream) // normal completion: no write ever failed

	select {
	case err := <-readDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("response body stream never closed")
	}

	rec.requireCancelled(t, "handleStreaming returned without cancelling the request context on "+
		"normal stream completion: the client-disconnect watcher goroutine and the whole "+
		"BifrostContext it captures leak until the client closes its connection")

	// Deliberately no process-wide goroutine count here. runtime.NumGoroutine() sweeps up
	// fasthttp workers and TCP teardown, which flap in a shared test binary, and a flaky
	// release gate trains people to rerun until green. The outcome this mechanism protects
	// is asserted precisely, by name, in
	// lib.TestClientDisconnectWatcher_RetentionNoGoroutineLeak against a real socket.
}

// cancelRecorder captures the cancel func handed to handleStreaming.
//
// handleStreaming cancels from its producer goroutine's deferred cleanup, which runs after
// the response body stream has closed. A test that samples a plain bool right after
// io.ReadAll therefore both races that goroutine (caught by -race) and can read it before
// the cancel lands. Recording into a channel fixes both.
type cancelRecorder struct {
	once sync.Once
	done chan struct{}
}

func newCancelRecorder() *cancelRecorder {
	return &cancelRecorder{done: make(chan struct{})}
}

// cancel is the context.CancelFunc stand-in passed into handleStreaming.
func (c *cancelRecorder) cancel() { c.once.Do(func() { close(c.done) }) }

// requireCancelled fails the test unless cancel lands within the timeout.
func (c *cancelRecorder) requireCancelled(t *testing.T, msg string) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal(msg)
	}
}
