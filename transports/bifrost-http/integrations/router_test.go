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
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/tracing"
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

	model, stream, err := parsePassthroughBody(writer.FormDataContentType(), body.Bytes())
	require.NoError(t, err)
	assert.Equal(t, "openai/whisper-1", model)
	assert.True(t, stream)
}

// TestParsePassthroughBody_JSONModelIsStrict: the model governance checks must be
// the one the upstream reads from the same bytes, so the JSON branch accepts exactly
// one, exactly-spelled, string-typed model key and refuses anything a different
// decoder could read differently. An unrelated field with the wrong type no longer
// drops the model, and a body with no model at all is still fine.
func TestParsePassthroughBody_JSONModelIsStrict(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		wantModel   string
		wantStream  bool
		wantErr     string
	}{
		{name: "plain body", contentType: "application/json", body: `{"model":" gpt-4o ","stream":true}`, wantModel: "gpt-4o", wantStream: true},
		{name: "no model key", contentType: "application/json", body: `{"input":"x"}`},
		{name: "unrelated field of wrong type keeps the model", contentType: "application/json", body: `{"model":"gpt-4o","stream":"yes"}`, wantModel: "gpt-4o"},
		{name: "duplicate model keys", contentType: "application/json", body: `{"model":"blocked","model":"allowed"}`, wantErr: "at most once"},
		{name: "case-variant duplicate", contentType: "application/json", body: `{"model":"blocked","MODEL":"allowed"}`, wantErr: "at most once"},
		{name: "case-variant only", contentType: "application/json", body: `{"Model":"gpt-4o"}`, wantErr: "spelled"},
		{name: "non-string model", contentType: "application/json", body: `{"model":["gpt-4o"]}`, wantErr: "must be a string"},
		{name: "nested model is not the request model", contentType: "application/json", body: `{"input":{"model":"inner"}}`},
		{name: "invalid JSON declared as JSON", contentType: "application/json; charset=utf-8", body: `{"model":"gpt-4o",`, wantErr: "not valid JSON"},
		{name: "invalid JSON with another content type is ignored", contentType: "text/plain", body: `model=gpt-4o`},
		{name: "valid JSON with no content type is still read", contentType: "", body: `{"model":"gpt-4o"}`, wantModel: "gpt-4o"},
		// A task array (Runware) names a model per task. One agreed model is the request's; tasks
		// naming different models leave it empty, and governance reads them from the body instead.
		{name: "single-task array names its model", contentType: "application/json", body: `[{"taskType":"imageInference","model":"runware:101@1"}]`, wantModel: "runware:101@1"},
		{name: "array agreeing on one model names it", contentType: "application/json", body: `[{"model":"runware:101@1"},{"taskType":"getResponse"},{"model":" runware:101@1 "}]`, wantModel: "runware:101@1"},
		{name: "array naming different models has no single model", contentType: "application/json", body: `[{"model":"runware:100@1"},{"model":"runware:101@1"}]`},
		{name: "array task with a non-string model", contentType: "application/json", body: `[{"model":["runware:101@1"]}]`, wantErr: "must be a string"},
		{name: "array task with duplicate model keys", contentType: "application/json", body: `[{"model":"blocked","model":"allowed"}]`, wantErr: "at most once"},
		{name: "array task with duplicate model keys after a disagreement", contentType: "application/json", body: `[{"model":"a"},{"model":"b"},{"model":"a","model":"c"}]`, wantErr: "at most once"},
		{name: "array of non-objects has no model", contentType: "application/json", body: `["runware:101@1"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, stream, err := parsePassthroughBody(tc.contentType, []byte(tc.body))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Empty(t, model)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantModel, model)
			assert.Equal(t, tc.wantStream, stream)
		})
	}
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

func TestAnthropicExtraParamsReachBedrock(t *testing.T) {
	for _, prefix := range []string{"/anthropic", "/pydanticai"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/passthrough=%t", prefix, enabled), func(t *testing.T) {
				route := createAnthropicMessagesRouteConfig(prefix, &testLogger{})[0]
				rawBody := []byte(`{
					"model": "bedrock/anthropic.claude-sonnet-4-5-20250929-v1:0",
					"max_tokens": 32,
					"messages": [{"role": "user", "content": "Hello"}],
					"extra_params": {"requestMetadata": {"tenant_slug": "test-tenant", "trace_id": "test-trace"}}
				}`)
				req := route.GetRequestTypeInstance(context.Background())
				require.NoError(t, sonic.Unmarshal(rawBody, req))
				ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
				ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, enabled)
				// Match the integration router's opt-in extra_params extraction.
				if enabled {
					setter, ok := req.(RequestWithSettableExtraParams)
					require.True(t, ok, "Anthropic requests must accept extra_params")
					var wrapper struct {
						ExtraParams map[string]interface{} `json:"extra_params"`
					}
					require.NoError(t, sonic.Unmarshal(rawBody, &wrapper))
					setter.SetExtraParams(wrapper.ExtraParams)
				}
				converted, err := route.RequestConverter(ctx, req)
				require.NoError(t, err)
				outgoing, err := bedrock.ToBedrockResponsesRequest(ctx, converted.ResponsesRequest)
				require.NoError(t, err)
				body, err := providerUtils.MarshalSorted(outgoing)
				require.NoError(t, err)
				var wire map[string]interface{}
				require.NoError(t, sonic.Unmarshal(body, &wire))
				if enabled {
					assert.Equal(t, map[string]interface{}{"tenant_slug": "test-tenant", "trace_id": "test-trace"}, wire["requestMetadata"])
				} else {
					assert.NotContains(t, wire, "requestMetadata")
				}
				assert.NotContains(t, wire, "extra_params")
			})
		}
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
	router.handleStreaming(ctx, nil, RouteConfig{}, "", stream, rec.cancel)

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

// passthroughTestAccount configures a single Anthropic provider whose BaseURL points at a
// local test upstream, so passthrough tests can observe exactly what (if anything) reaches it.
// Anthropic-shaped custom providers the passthrough tests reach through x-model-provider:
// one opted in with a passthrough policy, one never did.
const (
	passthroughSidecar    schemas.ModelProvider = "passthrough-sidecar"
	passthroughSidecarOff schemas.ModelProvider = "passthrough-sidecar-off"
)

type passthroughTestAccount struct {
	baseURL              string
	anthropicPassthrough *schemas.PassthroughPolicy
}

func (a passthroughTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.Anthropic, passthroughSidecar, passthroughSidecarOff}, nil
}

func (a passthroughTestAccount) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	value := "sk-ant-test-operator-key"
	switch p {
	case schemas.Anthropic:
	case passthroughSidecar, passthroughSidecarOff:
		value = "sk-" + string(p)
	default:
		return nil, nil
	}
	return []schemas.Key{{
		ID:     "passthrough-test-key-" + string(p),
		Value:  *schemas.NewSecretVar(value),
		Models: schemas.WhiteList{"*"},
		Weight: 1.0,
	}}, nil
}

func (a passthroughTestAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	nc := schemas.DefaultNetworkConfig
	nc.BaseURL = a.baseURL
	config := &schemas.ProviderConfig{
		NetworkConfig:            nc,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}
	switch p {
	case schemas.Anthropic:
		config.Passthrough = a.anthropicPassthrough
	case passthroughSidecar:
		config.CustomProviderConfig = &schemas.CustomProviderConfig{BaseProviderType: schemas.Anthropic}
		config.Passthrough = &schemas.PassthroughPolicy{AllowedRoutes: []schemas.PassthroughRouteRule{
			{Methods: []string{fasthttp.MethodPost}, Path: "/v1/models/*/predict"},
		}}
	case passthroughSidecarOff:
		config.CustomProviderConfig = &schemas.CustomProviderConfig{BaseProviderType: schemas.Anthropic}
	default:
		return nil, fmt.Errorf("unsupported provider %s", p)
	}
	return config, nil
}

// passthroughTestStore reports the test account's custom providers the way lib.Config does.
func passthroughTestStore() *mockHandlerStore {
	return &mockHandlerStore{customProviders: map[schemas.ModelProvider]mockCustomProvider{
		passthroughSidecar:    {base: schemas.Anthropic, passthrough: true},
		passthroughSidecarOff: {base: schemas.Anthropic},
	}}
}

type passthroughUpstreamHit struct {
	path   string
	apiKey string
}

func TestStripPassthroughPrefix(t *testing.T) {
	genai := []string{"/genai_passthrough/v1beta1", "/genai_passthrough/v1beta", "/genai_passthrough/v1", "/genai_passthrough"}
	tests := []struct {
		name        string
		path        string
		prefixes    []string
		want        string
		wantVersion string
		wantOK      bool
	}{
		{name: "versioned prefix at boundary", path: "/genai_passthrough/v1beta/models/x", prefixes: genai, want: "/models/x", wantVersion: "v1beta", wantOK: true},
		{name: "v1 prefix at boundary", path: "/genai_passthrough/v1/models/x", prefixes: genai, want: "/models/x", wantVersion: "v1", wantOK: true},
		{name: "v1beta1 prefix keeps its version for vertex", path: "/genai_passthrough/v1beta1/projects/p/locations/global/interactions", prefixes: genai, want: "/projects/p/locations/global/interactions", wantVersion: "v1beta1", wantOK: true},
		{name: "bare prefix at boundary", path: "/genai_passthrough/files/abc", prefixes: genai, want: "/files/abc", wantOK: true},
		{name: "exact prefix yields root", path: "/genai_passthrough/v1beta", prefixes: genai, want: "/", wantVersion: "v1beta", wantOK: true},
		{name: "longer first segment falls through to shorter prefix", path: "/genai_passthrough/v1beta1foo/x", prefixes: genai, want: "/v1beta1foo/x", wantOK: true},
		{name: "userinfo separator after prefix falls through to shorter prefix", path: "/genai_passthrough/v1@127.0.0.1/x", prefixes: genai, want: "/v1@127.0.0.1/x", wantOK: true},
		{name: "single prefix with userinfo separator does not match", path: "/anthropic_passthrough@evil.example/x", prefixes: []string{"/anthropic_passthrough"}, wantOK: false},
		{name: "unrelated path does not match", path: "/openai_passthrough/v1/models", prefixes: []string{"/anthropic_passthrough"}, wantOK: false},
		{name: "runware v1 remainder kept", path: "/runware_passthrough/v1", prefixes: []string{"/runware_passthrough"}, want: "/v1", wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, version, ok := stripPassthroughPrefix(tc.path, tc.prefixes)
			require.Equal(t, tc.wantOK, ok)
			if ok {
				require.Equal(t, tc.want, got)
				require.Equal(t, tc.wantVersion, version)
			}
		})
	}
}

func TestValidatePassthroughPath(t *testing.T) {
	valid := []string{
		"/",
		"/v1/messages",
		"/models/gemini-2.5-pro:generateContent",
		"/v1/projects/p/locations/global/publishers/anthropic/models/claude-sonnet-4-5@20250929:streamRawPredict",
		"/v1/files/file%2Fabc",
		"/openai/deployments/gpt-4o/chat/completions",
	}
	for _, p := range valid {
		require.NoError(t, validatePassthroughPath(p), "path %q should be accepted", p)
	}

	invalid := []string{
		"",
		"v1/messages",
		"@evil.example/v1/messages",
		"/@evil.example/v1/messages",
		"/v1@evil.example/messages",
		"//evil.example/x",
		"/%2F%2Fevil.example/x",
		"/%40evil.example/x",
		"/v1/../admin",
		"/v1/%2e%2e/admin",
		"/v1\\messages",
		"/v1/https://evil.example/x",
		"/v1/messages\r\nX-Injected: 1",
		"/v1/%0d%0amessages",
		"/v1/%zz",
	}
	for _, p := range invalid {
		require.Error(t, validatePassthroughPath(p), "path %q should be rejected", p)
	}
}

// TestHandlePassthrough_PathAndProviderGuards drives the passthrough catch-all end to end
// against a real Bifrost instance and a local upstream. Malformed remainders and unknown
// providers must be rejected by the router with no upstream request at all, because any
// request that reaches the provider carries the operator's key. Well-formed paths must
// still be forwarded with the prefix stripped at a segment boundary.
func TestHandlePassthrough_PathAndProviderGuards(t *testing.T) {
	var mu sync.Mutex
	var hits []passthroughUpstreamHit
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, passthroughUpstreamHit{path: r.URL.RequestURI(), apiKey: r.Header.Get("x-api-key")})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	takeHits := func() []passthroughUpstreamHit {
		mu.Lock()
		defer mu.Unlock()
		out := hits
		hits = nil
		return out
	}

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: passthroughTestAccount{baseURL: upstream.URL},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	defer client.Shutdown()

	r := router.New()
	identity := func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next }
	store := passthroughTestStore()
	NewGenAIPassthroughRouter(client, store, nil, &testLogger{}).RegisterRoutes(r, identity)
	NewAnthropicPassthroughRouter(client, store, nil, &testLogger{}).RegisterRoutes(r, identity)

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	tests := []struct {
		name         string
		method       string
		uri          string
		provider     string
		wantStatus   int
		wantUpstream string // expected upstream request URI; empty means no upstream request allowed
		wantBodyHas  string
		wantStreamed bool
		wantKey      string // upstream credential; empty means the anthropic operator key
	}{
		{
			// "/genai_passthrough/v1" no longer matches past the boundary, so the bare prefix
			// strips to "/v1@host/x" and the first-segment rule rejects it before dispatch.
			name:        "userinfo separator after a versioned prefix is rejected",
			uri:         "/genai_passthrough/v1@" + upstreamHost + "/x",
			provider:    "anthropic",
			wantStatus:  fasthttp.StatusBadRequest,
			wantBodyHas: "invalid passthrough path",
		},
		{
			name:        "percent-encoded userinfo separator is rejected",
			uri:         "/genai_passthrough/v1%40" + upstreamHost + "/x",
			provider:    "anthropic",
			wantStatus:  fasthttp.StatusBadRequest,
			wantBodyHas: "invalid passthrough path",
		},
		{
			name:        "userinfo separator in the first segment is rejected",
			uri:         "/anthropic_passthrough/@evil.example/v1/messages",
			wantStatus:  fasthttp.StatusBadRequest,
			wantBodyHas: "invalid passthrough path",
		},
		{
			name:        "unknown x-model-provider is rejected",
			uri:         "/anthropic_passthrough/v1/messages",
			provider:    "not-a-provider",
			wantStatus:  fasthttp.StatusBadRequest,
			wantBodyHas: "x-model-provider",
		},
		{
			name: "account operation is forwarded", uri: "/anthropic_passthrough/v1/files", wantStatus: fasthttp.StatusOK, wantUpstream: "/v1/files",
		},
		{
			name: "batch operation is forwarded", uri: "/anthropic_passthrough/v1/messages/batches", wantStatus: fasthttp.StatusOK, wantUpstream: "/v1/messages/batches",
		},
		{
			name: "non-POST operation is forwarded", method: fasthttp.MethodGet, uri: "/anthropic_passthrough/v1/files/file_1", wantStatus: fasthttp.StatusOK, wantUpstream: "/v1/files/file_1",
		},
		{
			// Resuming an interaction: GET .../interactions/{id}?stream=true answers with SSE.
			name: "GET asking for a stream in its query is streamed", method: fasthttp.MethodGet, uri: "/anthropic_passthrough/v1/files/file_1?stream=true", wantStatus: fasthttp.StatusOK, wantUpstream: "/v1/files/file_1?stream=true", wantStreamed: true,
		},
		{
			name:         "well-formed anthropic path is forwarded",
			uri:          "/anthropic_passthrough/v1/messages?beta=true",
			wantStatus:   fasthttp.StatusOK,
			wantUpstream: "/v1/messages?beta=true",
		},
		{
			name:        "versioned genai prefix is stripped at the boundary",
			uri:         "/genai_passthrough/v1beta/models/claude:generateContent",
			provider:    "anthropic",
			wantStatus:  fasthttp.StatusBadRequest,
			wantBodyHas: "provider does not match",
		},
		{
			name:        "longer first segment falls back to the shorter prefix",
			uri:         "/genai_passthrough/v1beta1foo/x",
			provider:    "anthropic",
			wantStatus:  fasthttp.StatusBadRequest,
			wantBodyHas: "provider does not match",
		},
		{
			name:         "custom provider with a passthrough policy is reachable on its base route",
			uri:          "/anthropic_passthrough/v1/models/m1/predict",
			provider:     string(passthroughSidecar),
			wantStatus:   fasthttp.StatusOK,
			wantUpstream: "/v1/models/m1/predict",
			wantKey:      "sk-" + string(passthroughSidecar),
		},
		{
			name:        "route outside a custom provider's policy is refused",
			uri:         "/anthropic_passthrough/v1/files",
			provider:    string(passthroughSidecar),
			wantStatus:  fasthttp.StatusForbidden,
			wantBodyHas: "passthrough route POST /v1/files is not allowed for provider " + string(passthroughSidecar),
		},
		{
			name:        "custom provider without a passthrough policy is refused",
			uri:         "/anthropic_passthrough/v1/messages",
			provider:    string(passthroughSidecarOff),
			wantStatus:  fasthttp.StatusForbidden,
			wantBodyHas: "passthrough is not enabled for provider " + string(passthroughSidecarOff),
		},
		{
			name:        "custom provider on another wire format's route is refused",
			uri:         "/genai_passthrough/v1beta/models/m1:generateContent",
			provider:    string(passthroughSidecar),
			wantStatus:  fasthttp.StatusBadRequest,
			wantBodyHas: "provider does not match",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			takeHits()
			var ctx fasthttp.RequestCtx
			method := fasthttp.MethodPost
			if tc.method != "" {
				method = tc.method
			}
			ctx.Request.Header.SetMethod(method)
			ctx.Request.SetRequestURI(tc.uri)
			ctx.Request.Header.SetContentType("application/json")
			if tc.provider != "" {
				ctx.Request.Header.Set("x-model-provider", tc.provider)
			}
			ctx.Request.SetBodyString(`{}`)

			r.Handler(&ctx)

			got := takeHits()
			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
			if tc.wantBodyHas != "" {
				require.Contains(t, string(ctx.Response.Body()), tc.wantBodyHas)
			}
			require.Equal(t, tc.wantStreamed, string(ctx.Response.Header.Peek("X-Accel-Buffering")) == "no", "streaming handler")
			if tc.wantUpstream == "" {
				require.Empty(t, got, "no request may reach the upstream for %s", tc.uri)
				return
			}
			require.Len(t, got, 1)
			require.Equal(t, tc.wantUpstream, got[0].path)
			wantKey := tc.wantKey
			if wantKey == "" {
				wantKey = "sk-ant-test-operator-key"
			}
			require.Equal(t, wantKey, got[0].apiKey)
		})
	}
}

// TestHandlePassthrough_ProviderPolicy drives a built-in provider whose passthrough policy allows
// only POST /v1/messages: that route is forwarded, every other route or method is refused with 403
// and an error naming the route, and nothing refused reaches the upstream.
func TestHandlePassthrough_ProviderPolicy(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: passthroughTestAccount{baseURL: upstream.URL, anthropicPassthrough: &schemas.PassthroughPolicy{
			AllowedRoutes: []schemas.PassthroughRouteRule{{Methods: []string{"POST"}, Path: "/v1/messages"}},
		}},
		Logger: bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	defer client.Shutdown()

	r := router.New()
	NewAnthropicPassthroughRouter(client, passthroughTestStore(), nil, &testLogger{}).RegisterRoutes(r, func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next })

	for _, tc := range []struct {
		method, uri string
		wantStatus  int
		wantBodyHas string
	}{
		{fasthttp.MethodPost, "/anthropic_passthrough/v1/messages", fasthttp.StatusOK, ""},
		{fasthttp.MethodPost, "/anthropic_passthrough/v1/files", fasthttp.StatusForbidden, "passthrough route POST /v1/files is not allowed for provider anthropic"},
		{fasthttp.MethodGet, "/anthropic_passthrough/v1/messages", fasthttp.StatusForbidden, "passthrough route GET /v1/messages is not allowed"},
		{fasthttp.MethodPost, "/anthropic_passthrough/v1/messages/batches", fasthttp.StatusForbidden, "add it to the provider's passthrough allowed routes"},
	} {
		var ctx fasthttp.RequestCtx
		ctx.Request.Header.SetMethod(tc.method)
		ctx.Request.SetRequestURI(tc.uri)
		ctx.Request.Header.SetContentType("application/json")
		ctx.Request.SetBodyString(`{}`)
		r.Handler(&ctx)
		require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), "%s %s: %s", tc.method, tc.uri, ctx.Response.Body())
		require.Contains(t, string(ctx.Response.Body()), tc.wantBodyHas, "%s %s", tc.method, tc.uri)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"POST /v1/messages"}, hits, "only the allowed route may reach the upstream")
}

// directKeyHandlerStore is the shared mock store with direct keys allowed.
type directKeyHandlerStore struct{ *mockHandlerStore }

func (directKeyHandlerStore) ShouldAllowDirectKeys() bool { return true }

// directKeyTestAccount configures OpenAI and Anthropic, each against its own upstream.
type directKeyTestAccount struct{ openAIURL, anthropicURL string }

func (a directKeyTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}, nil
}

func (a directKeyTestAccount) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: "operator-" + string(p), Value: *schemas.NewSecretVar("sk-operator-" + string(p)), Models: schemas.WhiteList{"*"}, Weight: 1.0}}, nil
}

func (a directKeyTestAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	nc := schemas.DefaultNetworkConfig
	switch p {
	case schemas.OpenAI:
		nc.BaseURL = a.openAIURL
	case schemas.Anthropic:
		nc.BaseURL = a.anthropicURL
	default:
		return nil, fmt.Errorf("unsupported provider %s", p)
	}
	return &schemas.ProviderConfig{NetworkConfig: nc, ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize}, nil
}

// A direct key is the caller's credential for the provider whose SDK called the route. A drop-in
// model listing that names no provider used to ask every configured provider and send the key to
// each of them; it now asks only the route's own provider, refuses when that provider is not
// configured, and refuses an explicit "all".
func TestListModelsDirectKeyStaysOnTheRouteProvider(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][]string{}
	recording := func(name string, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[name] = append(seen[name], r.Header.Get("Authorization")+r.Header.Get("x-api-key"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
	}
	openAISrv := recording("openai", `{"object":"list","data":[{"id":"gpt-4o-mini","object":"model","created":1700000000,"owned_by":"test"}]}`)
	defer openAISrv.Close()
	anthropicSrv := recording("anthropic", `{"data":[{"id":"claude-test","type":"model","display_name":"Claude Test","created_at":"2026-01-01T00:00:00Z"}],"has_more":false}`)
	defer anthropicSrv.Close()
	take := func() map[string][]string {
		mu.Lock()
		defer mu.Unlock()
		out := seen
		seen = map[string][]string{}
		return out
	}

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: directKeyTestAccount{openAIURL: openAISrv.URL, anthropicURL: anthropicSrv.URL},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	defer client.Shutdown()

	r := router.New()
	identity := func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next }
	store := directKeyHandlerStore{&mockHandlerStore{availableProviders: []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}}}
	NewOpenAIRouter(client, store, nil, &testLogger{}).RegisterRoutes(r, identity)
	NewAnthropicRouter(client, store, nil, &testLogger{}).RegisterRoutes(r, identity)

	list := func(uri string, headers map[string]string) *fasthttp.RequestCtx {
		var ctx fasthttp.RequestCtx
		ctx.Request.Header.SetMethod(fasthttp.MethodGet)
		ctx.Request.SetRequestURI(uri)
		ctx.Request.Header.Set("x-bf-direct-key", "true")
		for k, v := range headers {
			ctx.Request.Header.Set(k, v)
		}
		r.Handler(&ctx)
		return &ctx
	}

	ctx := list("/openai/v1/models", map[string]string{"Authorization": "Bearer sk-caller-openai"})
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
	require.Equal(t, map[string][]string{"openai": {"Bearer sk-caller-openai"}}, take(), "the caller's OpenAI key must reach OpenAI only")

	ctx = list("/anthropic/v1/models", map[string]string{"x-api-key": "sk-caller-anthropic"})
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
	require.Equal(t, map[string][]string{"anthropic": {"sk-caller-anthropic"}}, take(), "the caller's Anthropic key must reach Anthropic only")

	ctx = list("/openai/v1/models", map[string]string{"Authorization": "Bearer sk-caller-openai", "x-bf-model-provider": "all"})
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
	require.Contains(t, string(ctx.Response.Body()), "must name its provider")
	require.Empty(t, take(), "a refused listing must reach no provider")

	// The same route on a gateway where its provider is not configured has no provider to ask.
	anthropicOnly := router.New()
	NewOpenAIRouter(client, directKeyHandlerStore{&mockHandlerStore{availableProviders: []schemas.ModelProvider{schemas.Anthropic}}}, nil, &testLogger{}).RegisterRoutes(anthropicOnly, identity)
	var unconfigured fasthttp.RequestCtx
	unconfigured.Request.Header.SetMethod(fasthttp.MethodGet)
	unconfigured.Request.SetRequestURI("/openai/v1/models")
	unconfigured.Request.Header.Set("x-bf-direct-key", "true")
	unconfigured.Request.Header.Set("Authorization", "Bearer sk-caller-openai")
	anthropicOnly.Handler(&unconfigured)
	require.Equal(t, fasthttp.StatusBadRequest, unconfigured.Response.StatusCode(), "body: %s", unconfigured.Response.Body())
	require.Contains(t, string(unconfigured.Response.Body()), "provider openai is not configured")
	require.Empty(t, take(), "a listing for an unconfigured provider must reach no provider")
}

// The Bedrock passthrough is a restricted route: it targets the Bedrock provider and is mounted
// at /bedrock_passthrough, so it can never be mistaken for the native /bedrock integration.
func TestBedrockPassthroughRouterTargetsBedrockAtItsOwnPrefix(t *testing.T) {
	r := NewBedrockPassthroughRouter(nil, &mockHandlerStore{}, nil, &testLogger{})
	require.NotNil(t, r.passthroughCfg)
	assert.Equal(t, schemas.Bedrock, r.passthroughCfg.Provider)
	assert.Equal(t, []string{"/bedrock_passthrough"}, r.passthroughCfg.StripPrefix)
	assert.Empty(t, r.passthroughCfg.AllowedRoutes, "the operation allow-list lives in the provider, which also holds the signing credentials")
}

// The Bedrock passthrough streams exactly the operation whose AWS response is an event stream.
// InvokeAgent's path and body carry no "stream" marker, so the router needs the provider's word for it.
func TestBedrockPassthroughRouterStreamsOnlyInvokeAgent(t *testing.T) {
	cfg := NewBedrockPassthroughRouter(nil, &mockHandlerStore{}, nil, &testLogger{}).passthroughCfg
	require.NotNil(t, cfg.StreamingPath, "the router must know InvokeAgent streams")

	const invokeAgent = "/agents/AGENT12345/agentAliases/ALIAS12345/sessions/s1/text"
	assert.True(t, cfg.streams(http.MethodPost, invokeAgent, false), "InvokeAgent answers with an event stream")
	assert.False(t, cfg.streams(http.MethodPost, "/knowledgebases/KB12345678/retrieve", false), "Retrieve is a plain request/response call")
	assert.False(t, cfg.streams(http.MethodPost, "/guardrail/gr1a2b3c4d5e/version/DRAFT/apply", false), "ApplyGuardrail is a plain request/response call")
	assert.False(t, cfg.streams(http.MethodGet, invokeAgent, false), "only the POST that the provider forwards streams")
	assert.False(t, cfg.streams(http.MethodPost, "/model/amazon.titan-text-express-v1/invoke", false), "a route outside the allow-list is not special-cased")

	// The provider's word is final: an accepted identifier that happens to contain "stream", or a body
	// that says stream: true, must not move a request/response operation onto the streaming handler.
	assert.False(t, cfg.streams(http.MethodPost, "/knowledgebases/STREAMKB01/retrieve", false), "a knowledge-base id containing the word stream is still a plain Retrieve")
	assert.False(t, cfg.streams(http.MethodPost, "/guardrail/streamguard1/version/DRAFT/apply", false), "a guardrail id containing the word stream is still a plain ApplyGuardrail")
	assert.False(t, cfg.streams(http.MethodPost, "/knowledgebases/KB12345678/retrieve", true), "stream: true in a Retrieve body does not make it stream")
	assert.True(t, cfg.streams(http.MethodPost, "/agents/AGENT12345/agentAliases/ALIAS12345/sessions/s1/text", false), "InvokeAgent still streams")
}

// The generic stream decision is unchanged for every other passthrough router.
func TestPassthroughConfigStreamsDecision(t *testing.T) {
	plain := &PassthroughConfig{}
	assert.True(t, plain.streams(http.MethodPost, "/v1beta/models/x:streamGenerateContent", false), "a stream marker in the path")
	assert.True(t, plain.streams(http.MethodPost, "/v1/messages", true), "stream: true in the body")
	assert.False(t, plain.streams(http.MethodPost, "/v1/messages", false))

	custom := &PassthroughConfig{StreamingPath: func(method, path string) bool { return method == http.MethodPost && path == "/always" }}
	assert.True(t, custom.streams(http.MethodPost, "/always", false), "the provider hook marks the route as streaming")
	assert.False(t, custom.streams(http.MethodPost, "/other", false))
	assert.False(t, custom.streams(http.MethodPost, "/stream/other", false), "when the provider supplies a route predicate it replaces the generic path marker")
	assert.False(t, custom.streams(http.MethodPost, "/other", true), "and the generic body marker")
}

func TestPassthroughQueryStreams(t *testing.T) {
	assert.True(t, passthroughQueryStreams(http.MethodGet, "stream=true"))
	assert.True(t, passthroughQueryStreams(http.MethodGet, "last_event_id=evt-1&stream=TRUE"))
	assert.False(t, passthroughQueryStreams(http.MethodGet, "stream=false"))
	assert.False(t, passthroughQueryStreams(http.MethodGet, "include_input=true"))
	assert.False(t, passthroughQueryStreams(http.MethodGet, ""))
	assert.False(t, passthroughQueryStreams(http.MethodPost, "stream=true"), "a POST declares streaming in its body")
}

// After provider-injected tools ran, a non-stream response is assembled from several
// model turns, while RawResponse holds only the last one. Every integration whose
// converter answers with the raw upstream bytes (OpenAI, Bedrock, Cohere, GenAI,
// Typesafe, Anthropic) must get the assembled response instead. The raw bytes stay on
// the original for logging.
func TestResponseForConverterDropsRawAfterInjectedTools(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	chat := &schemas.BifrostChatResponse{
		Choices:     []schemas.BifrostResponseChoice{{ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Turn 1 text. Sunny.")}}}}},
		ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI, RawResponse: `{"raw":"turn 2 only"}`},
	}
	responses := &schemas.BifrostResponsesResponse{
		ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI, RawResponse: `{"raw":"turn 2 only"}`},
	}

	if got := chatResponseForConverter(ctx, chat); got.ExtraFields.RawResponse == nil {
		t.Fatal("a single-turn reply keeps its raw bytes for raw-preferring converters")
	}

	ctx.SetValue(schemas.BifrostContextKeyInjectedToolsExecuted, true)
	if got := chatResponseForConverter(ctx, chat); got.ExtraFields.RawResponse != nil {
		t.Fatal("chat: after injected tools ran, the converter must not see the last turn's raw reply")
	}
	if got := responsesResponseForConverter(ctx, responses); got.ExtraFields.RawResponse != nil {
		t.Fatal("responses: after injected tools ran, the converter must not see the last turn's raw reply")
	}
	if chat.ExtraFields.RawResponse == nil || responses.ExtraFields.RawResponse == nil {
		t.Fatal("the original keeps its raw bytes for logging")
	}

	var openAIChat ChatResponseConverter
	for _, route := range CreateOpenAIRouteConfigs("/openai", nil) {
		if route.ChatResponseConverter != nil {
			openAIChat = route.ChatResponseConverter
			break
		}
	}
	if openAIChat == nil {
		t.Fatal("no OpenAI chat route")
	}
	body, err := openAIChat(ctx, chatResponseForConverter(ctx, chat))
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := body.(string); ok && s == `{"raw":"turn 2 only"}` {
		t.Fatal("the OpenAI route answered with the last turn's raw bytes")
	}
}

// codexPassthroughAccount initializes only the provider needed by the synthetic relay test.
type codexPassthroughAccount struct{}

// GetConfiguredProviders keeps the test independent of installed provider configuration.
func (codexPassthroughAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

// GetKeysForProvider supplies a synthetic key that must never replace the caller's subscription token.
func (codexPassthroughAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: "synthetic-key", Value: *schemas.NewSecretVar("synthetic-key"), Models: schemas.WhiteList{"*"}, Weight: 1}}, nil
}

// GetConfigForProvider uses defaults; the short circuit guarantees no live upstream is contacted.
func (codexPassthroughAccount) GetConfigForProvider(schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{}, nil
}

// codexPassthroughReceipt witnesses the real router-to-core request before an explicit synthetic policy denial.
type codexPassthroughReceipt struct {
	request          *schemas.BifrostRequest
	skipKeySelection bool
}

// GetName identifies the isolated test plugin.
func (*codexPassthroughReceipt) GetName() string { return "synthetic-codex-receipt" }

// Cleanup has no resources to release.
func (*codexPassthroughReceipt) Cleanup() error { return nil }

// PreRequestHook leaves native passthrough state unchanged.
func (*codexPassthroughReceipt) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook captures native dispatch and rejects before any live upstream or operator key can be used.
func (p *codexPassthroughReceipt) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	snapshot := *req
	p.request = &snapshot
	p.skipKeySelection, _ = ctx.Value(schemas.BifrostContextKeySkipKeySelection).(bool)
	status := 403
	return req, &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{StatusCode: &status, Error: &schemas.ErrorField{Message: "synthetic policy denial"}}}, nil
}

// PostLLMHook preserves the synthetic receipt unchanged.
func (*codexPassthroughReceipt) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

// TestChatGPTPassthroughStreamsWithoutBodyFlag reproduces a server buffering a Codex WebSocket bridge's native response when stream is omitted.
// The reported logs contain no raw body: these requests and the policy denial are synthetic, not captured production payloads.
func TestChatGPTPassthroughStreamsWithoutBodyFlag(t *testing.T) {
	for _, flag := range []string{"", `,"stream":true`, `,"stream":false`} {
		t.Run(flag, func(t *testing.T) {
			plugin := &codexPassthroughReceipt{}
			client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: codexPassthroughAccount{}, LLMPlugins: []schemas.LLMPlugin{plugin}, Logger: bifrost.NewNoOpLogger()})
			require.NoError(t, err)
			defer client.Shutdown()
			r := router.New()
			NewChatGPTPassthroughRouter(client, &mockHandlerStore{}, nil, &testLogger{}).RegisterRoutes(r)
			body := `{"model":"gpt-5.5","input":[],"future":9007199254740993` + flag + `}`
			var ctx fasthttp.RequestCtx
			ctx.Request.Header.SetMethod("POST")
			ctx.Request.SetRequestURI("/chatgpt_passthrough/backend-api/codex/responses")
			ctx.Request.Header.SetContentType("application/json")
			ctx.Request.Header.Set("Authorization", "Bearer eyJhbGciOiJub25lIn0.eyJzdWIiOiJzeW50aGV0aWMifQ.signature")
			ctx.Request.SetBodyString(body)
			r.Handler(&ctx)
			require.Equal(t, 403, ctx.Response.StatusCode())
			require.Contains(t, string(ctx.Response.Body()), "synthetic policy denial")
			require.NotNil(t, plugin.request)
			require.Equal(t, schemas.PassthroughStreamRequest, plugin.request.RequestType, "native Codex responses must not use the buffered API path")
			require.True(t, plugin.skipKeySelection)
			require.Equal(t, "https://chatgpt.com", plugin.request.PassthroughRequest.UpstreamURL)
			require.Equal(t, "/backend-api/codex/responses", plugin.request.PassthroughRequest.Path)
			require.Equal(t, body, string(plugin.request.PassthroughRequest.Body))
			require.Equal(t, "Bearer eyJhbGciOiJub25lIn0.eyJzdWIiOiJzeW50aGV0aWMifQ.signature", plugin.request.PassthroughRequest.SafeHeaders["authorization"])
		})
	}
}

// BenchmarkChatGPTPassthroughStreamDecision measures the route predicate without touching provider execution.
func BenchmarkChatGPTPassthroughStreamDecision(b *testing.B) {
	cfg := NewChatGPTPassthroughRouter(nil, nil, nil, &testLogger{}).passthroughCfg
	b.ReportAllocs()
	for b.Loop() {
		_ = cfg.streams("POST", "/backend-api/codex/responses", false)
	}
}

// TestChatGPTPassthroughStreamEndsWithResponseFailedOnUpstreamError covers an upstream that answers
// 200 text/event-stream and then fails mid-body. The relay used to stop on the error chunk without
// writing anything, so Codex saw the stream close with no terminal event, retried the same request
// blind and reported "stream closed before response.completed" instead of the upstream's error.
func TestChatGPTPassthroughStreamEndsWithResponseFailedOnUpstreamError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = http.ReadRequest(bufio.NewReader(conn))
		event := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n"
		// Chunked body with no terminating chunk: the connection drops mid-stream.
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(event), event)
	}()

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: codexPassthroughAccount{}, Logger: bifrost.NewNoOpLogger()})
	require.NoError(t, err)
	defer client.Shutdown()
	g := NewGenericRouter(client, &mockHandlerStore{}, nil, nil, nil, bifrost.NewNoOpLogger())

	var ctx fasthttp.RequestCtx
	bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	g.handlePassthroughStream(&ctx, bifrostCtx, cancel, schemas.OpenAI, &schemas.BifrostPassthroughRequest{
		Method:      "POST",
		Path:        "/backend-api/codex/responses",
		UpstreamURL: "http://" + ln.Addr().String(),
		Body:        []byte(`{"model":"gpt-5.5","input":[],"stream":true}`),
		SafeHeaders: map[string]string{"content-type": "application/json"},
	})
	require.Equal(t, 200, ctx.Response.StatusCode())

	bodyStream := ctx.Response.BodyStream()
	require.NotNil(t, bodyStream)
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(bodyStream)
		done <- b
	}()
	var body string
	select {
	case b := <-done:
		body = string(b)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out reading the relayed stream")
	}

	require.Contains(t, body, `"type":"response.created"`, "events before the failure are relayed unchanged")
	require.Contains(t, body, "event: response.failed\n", "a stream that fails mid-body must end with a terminal event: %q", body)
	failed := body[strings.LastIndex(body, "data: ")+len("data: "):]
	var event struct {
		Type     string `json:"type"`
		Response struct {
			Status string `json:"status"`
			Error  struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	require.NoError(t, sonic.UnmarshalString(strings.TrimSpace(failed), &event), "terminal frame must be JSON: %q", failed)
	assert.Equal(t, "response.failed", event.Type)
	assert.Equal(t, "failed", event.Response.Status)
	assert.NotEmpty(t, event.Response.Error.Code)
	assert.NotEmpty(t, event.Response.Error.Message)
}

// TestPassthroughFailedFrameOnlyForResponsesSSE pins the gate: only an SSE stream on an OpenAI-shaped
// Responses path speaks response.failed, so every other passthrough keeps ending without a frame.
func TestPassthroughFailedFrameOnlyForResponsesSSE(t *testing.T) {
	cases := []struct {
		name        string
		provider    schemas.ModelProvider
		path        string
		contentType string
		want        bool
	}{
		{"codex responses", schemas.OpenAI, "/backend-api/codex/responses", "text/event-stream", true},
		{"openai responses", schemas.OpenAI, "/v1/responses", "text/event-stream; charset=utf-8", true},
		{"azure responses", schemas.Azure, "/openai/v1/responses", "text/event-stream", true},
		{"openai chat", schemas.OpenAI, "/v1/chat/completions", "text/event-stream", false},
		{"non sse", schemas.OpenAI, "/v1/responses", "application/json", false},
		{"anthropic messages", schemas.Anthropic, "/v1/messages", "text/event-stream", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, passthroughEndsWithResponsesFailed(nil, tc.provider, tc.path, tc.contentType))
		})
	}
}

// TestPassthroughResponsesFailedFrameHidesGatewayReadErrors keeps a gateway-side read failure out of
// the client's response.failed: ProcessAndSendError puts the raw read error ("read tcp 10.0.0.5:...")
// in the message, which names internal addresses. An upstream's own error still reaches the client.
func TestPassthroughResponsesFailedFrameHidesGatewayReadErrors(t *testing.T) {
	failedError := func(frame []byte) (code, message string) {
		data := strings.TrimSpace(string(frame)[strings.Index(string(frame), "data: ")+len("data: "):])
		var event struct {
			Response struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		require.NoError(t, sonic.UnmarshalString(data, &event), "frame must be JSON: %q", frame)
		return event.Response.Error.Code, event.Response.Error.Message
	}

	t.Run("gateway read error", func(t *testing.T) {
		frame := passthroughResponsesFailedFrame(&schemas.BifrostError{
			IsBifrostError: true,
			Error:          &schemas.ErrorField{Message: "Error reading stream: read tcp 10.0.0.5:51234->104.18.32.47:443: read: connection reset by peer"},
		}, "", 0)
		require.NotNil(t, frame)
		assert.NotContains(t, string(frame), "10.0.0.5")
		assert.NotContains(t, string(frame), "read tcp")
		code, message := failedError(frame)
		assert.Equal(t, "server_error", code)
		assert.Equal(t, "upstream stream failed", message)
	})

	t.Run("upstream error", func(t *testing.T) {
		frame := passthroughResponsesFailedFrame(&schemas.BifrostError{
			Error: &schemas.ErrorField{Code: schemas.Ptr("context_length_exceeded"), Message: "Your input exceeds the context window of this model."},
		}, "", 0)
		require.NotNil(t, frame)
		code, message := failedError(frame)
		assert.Equal(t, "context_length_exceeded", code)
		assert.Equal(t, "Your input exceeds the context window of this model.", message)
	})
}

// relayResponsesPassthrough runs handlePassthroughStream for a Codex Responses request against a raw
// TCP upstream scripted by upstream, and returns every byte the client received.
func relayResponsesPassthrough(t *testing.T, upstream func(conn net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = http.ReadRequest(bufio.NewReader(conn))
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		upstream(conn)
	}()

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: codexPassthroughAccount{}, Logger: bifrost.NewNoOpLogger()})
	require.NoError(t, err)
	defer client.Shutdown()
	g := NewGenericRouter(client, &mockHandlerStore{}, nil, nil, nil, bifrost.NewNoOpLogger())

	var ctx fasthttp.RequestCtx
	bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	g.handlePassthroughStream(&ctx, bifrostCtx, cancel, schemas.OpenAI, &schemas.BifrostPassthroughRequest{
		Method:      "POST",
		Path:        "/backend-api/codex/responses",
		UpstreamURL: "http://" + ln.Addr().String(),
		Body:        []byte(`{"model":"gpt-5.5","input":[],"stream":true}`),
		SafeHeaders: map[string]string{"content-type": "application/json"},
	})
	require.Equal(t, 200, ctx.Response.StatusCode())
	bodyStream := ctx.Response.BodyStream()
	require.NotNil(t, bodyStream)
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(bodyStream)
		done <- b
	}()
	select {
	case b := <-done:
		return string(b)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out reading the relayed stream")
		return ""
	}
}

// writeChunk writes one HTTP/1.1 chunk and pauses so the relay reads it on its own.
func writeChunk(conn net.Conn, data string) {
	_, _ = fmt.Fprintf(conn, "%x\r\n%s\r\n", len(data), data)
	time.Sleep(50 * time.Millisecond)
}

const relayCreatedFrame = "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n"

// TestResponsesPassthroughDropsPartialFrameBeforeFailure covers an upstream that drops inside a data
// line. The half-sent line must never reach the client: SSE has no way to cancel a partial line, so
// the failure frame would be glued onto it and neither would parse.
func TestResponsesPassthroughDropsPartialFrameBeforeFailure(t *testing.T) {
	body := relayResponsesPassthrough(t, func(conn net.Conn) {
		writeChunk(conn, relayCreatedFrame+"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"del")
	})
	require.True(t, strings.HasPrefix(body, relayCreatedFrame), "complete frames are relayed unchanged: %q", body)
	assert.NotContains(t, body, "response.output_text.delta", "the partial frame must not reach the client")
	rest := strings.TrimPrefix(body, relayCreatedFrame)
	require.True(t, strings.HasPrefix(rest, "event: response.failed\ndata: "), "the failure frame starts on a fresh line: %q", rest)
	require.True(t, strings.HasSuffix(rest, "\n\n"), "the failure frame is terminated: %q", rest)
	var event struct {
		Type string `json:"type"`
	}
	require.NoError(t, sonic.UnmarshalString(strings.TrimSpace(strings.TrimPrefix(rest, "event: response.failed\ndata: ")), &event))
	assert.Equal(t, "response.failed", event.Type)
	assert.Contains(t, rest, `"message":"upstream closed the stream before response.completed"`, "a clean close names the truncation")
}

// TestResponsesPassthroughHealthyStreamIsByteExact keeps a stream that completes untouched: frames
// split across reads come out byte for byte, and no failure frame is added after response.completed.
func TestResponsesPassthroughHealthyStreamIsByteExact(t *testing.T) {
	completed := "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n"
	split := len(completed) / 2
	body := relayResponsesPassthrough(t, func(conn net.Conn) {
		writeChunk(conn, relayCreatedFrame+completed[:split])
		writeChunk(conn, completed[split:])
		_, _ = io.WriteString(conn, "0\r\n\r\n")
	})
	assert.Equal(t, relayCreatedFrame+completed, body)
}

// TestResponsesPassthroughConnectionResetEndsWithGenericFailure takes the error-chunk path end to end:
// a TCP reset surfaces as a gateway read error, which ends the stream with response.failed carrying
// the generic message, never the read error and its addresses.
func TestResponsesPassthroughConnectionResetEndsWithGenericFailure(t *testing.T) {
	body := relayResponsesPassthrough(t, func(conn net.Conn) {
		writeChunk(conn, relayCreatedFrame)
		time.Sleep(100 * time.Millisecond)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
	})
	require.True(t, strings.HasPrefix(body, relayCreatedFrame), "complete frames are relayed unchanged: %q", body)
	assert.Contains(t, body, "event: response.failed\n")
	assert.Contains(t, body, `"message":"upstream stream failed"`)
	assert.NotContains(t, body, "127.0.0.1")
	assert.NotContains(t, body, "connection reset")
}

// BenchmarkResponsesFrameRelay measures the per-chunk cost the gated Responses passthrough pays for
// whole-frame forwarding: one text delta frame per read, the common shape of a streamed turn.
func BenchmarkResponsesFrameRelay(b *testing.B) {
	delta := []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":7,\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello world\"}\n\n")
	relay := &responsesFrameRelay{}
	b.ReportAllocs()
	for b.Loop() {
		_ = relay.complete(delta)
	}
}

// TestResponsesFrameRelayCapsUnterminatedFrame bounds the relay's buffer: an upstream that never
// terminates a frame must not grow pending past core's native per-event ceiling.
func TestResponsesFrameRelayCapsUnterminatedFrame(t *testing.T) {
	relay := &responsesFrameRelay{}
	piece := []byte("data: " + strings.Repeat("x", 4090))
	for sent := 0; sent <= responsesFrameRelayMaxPending; sent += len(piece) {
		require.Empty(t, relay.complete(piece), "an unterminated frame is never forwarded")
	}
	assert.True(t, relay.overflowed, "passing the cap must mark the relay overflowed")
	assert.Empty(t, relay.pending, "the unterminated tail is discarded, not retained")
}

// TestResponsesFrameRelayLargeEventIsByteExact keeps a large legitimate event (below the cap) intact
// when it arrives in 4 KiB reads, the way the passthrough producer reads the upstream.
func TestResponsesFrameRelayLargeEventIsByteExact(t *testing.T) {
	event := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"blob\":\"" + strings.Repeat("y", 9<<20) + "\"}\n\n")
	relay := &responsesFrameRelay{}
	var out []byte
	for off := 0; off < len(event); off += 4096 {
		out = append(out, relay.complete(event[off:min(off+4096, len(event))])...)
	}
	assert.Equal(t, len(event), len(out))
	assert.True(t, bytes.Equal(event, out), "a large event must come out byte-exact")
	assert.True(t, relay.sawTerminal)
	assert.False(t, relay.overflowed)
}

// TestResponsesFrameRelayFindsDelimiterSplitAcrossChunks pins the overlap the incremental scan keeps:
// a CRLF blank line split across two reads still completes the frame.
func TestResponsesFrameRelayFindsDelimiterSplitAcrossChunks(t *testing.T) {
	relay := &responsesFrameRelay{}
	assert.Empty(t, relay.complete([]byte("event: response.created\r\ndata: {\"type\":\"response.created\"}\r\n\r")))
	assert.Equal(t, "event: response.created\r\ndata: {\"type\":\"response.created\"}\r\n\r\n", string(relay.complete([]byte("\n"))))
}

// TestResponsesPassthroughOverflowEndsWithOneFailure fails closed end to end: once an unterminated
// frame passes the cap the relay stops forwarding and the client gets exactly one response.failed.
func TestResponsesPassthroughOverflowEndsWithOneFailure(t *testing.T) {
	body := relayResponsesPassthrough(t, func(conn net.Conn) {
		writeChunk(conn, relayCreatedFrame)
		piece := "data: " + strings.Repeat("z", 64<<10)
		for sent := 0; sent <= responsesFrameRelayMaxPending+len(piece); sent += len(piece) {
			if _, err := fmt.Fprintf(conn, "%x\r\n%s\r\n", len(piece), piece); err != nil {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	})
	require.True(t, strings.HasPrefix(body, relayCreatedFrame), "complete frames are relayed unchanged")
	rest := strings.TrimPrefix(body, relayCreatedFrame)
	assert.NotContains(t, rest, "zzzz", "no partial frame bytes reach the client")
	assert.Equal(t, 1, strings.Count(rest, "event: response.failed\n"), "exactly one failure frame: %.300q", rest)
	assert.Contains(t, rest, "upstream event exceeded the 10 MB frame limit")
}

// BenchmarkResponsesFrameRelayLargeEvent measures a 4 MiB event arriving in 4 KiB reads; the scan
// covers only the new bytes, so the cost grows linearly with the event.
func BenchmarkResponsesFrameRelayLargeEvent(b *testing.B) {
	event := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"blob\":\"" + strings.Repeat("y", 4<<20) + "\"}\n\n")
	b.ReportAllocs()
	b.SetBytes(int64(len(event)))
	for b.Loop() {
		relay := &responsesFrameRelay{}
		for off := 0; off < len(event); off += 4096 {
			_ = relay.complete(event[off:min(off+4096, len(event))])
		}
	}
}

// relayedFailedEvent parses the response.failed frame that ends a relayed body.
func relayedFailedEvent(t *testing.T, body string) (id string, seq int, hasSeq bool) {
	t.Helper()
	i := strings.LastIndex(body, "event: response.failed\ndata: ")
	require.GreaterOrEqual(t, i, 0, "no response.failed frame: %q", body)
	var event struct {
		SequenceNumber *int `json:"sequence_number"`
		Response       struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	require.NoError(t, sonic.UnmarshalString(strings.TrimSpace(body[i+len("event: response.failed\ndata: "):]), &event))
	if event.SequenceNumber != nil {
		return event.Response.ID, *event.SequenceNumber, true
	}
	return event.Response.ID, 0, false
}

// TestResponsesPassthroughFailureCarriesUpstreamIdentity ties the synthetic response.failed to the
// response the client already saw: the id from response.created and the next sequence number, the
// way an upstream's own response.failed would (developers.openai.com/api/reference/resources/responses/streaming-events).
func TestResponsesPassthroughFailureCarriesUpstreamIdentity(t *testing.T) {
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_up_42\",\"status\":\"in_progress\"}}\n\n"
	delta := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":5,\"item_id\":\"msg_1\",\"delta\":\"hi\"}\n\n"
	body := relayResponsesPassthrough(t, func(conn net.Conn) {
		writeChunk(conn, created+delta+"event: response.output_text.delta\ndata: {\"type\":\"resp")
	})
	id, seq, hasSeq := relayedFailedEvent(t, body)
	assert.Equal(t, "resp_up_42", id, "response.failed names the response the client already received")
	require.True(t, hasSeq)
	assert.Equal(t, 6, seq, "response.failed continues the upstream sequence")
}

// TestResponsesPassthroughFailureBeforeCreatedHasNoIdentity covers a failure before response.created:
// there is no id to repeat, so none is invented, and the sequence starts at 0.
func TestResponsesPassthroughFailureBeforeCreatedHasNoIdentity(t *testing.T) {
	body := relayResponsesPassthrough(t, func(conn net.Conn) {
		writeChunk(conn, "event: response.created\ndata: {\"type\":\"resp")
	})
	id, seq, hasSeq := relayedFailedEvent(t, body)
	assert.Empty(t, id)
	require.True(t, hasSeq)
	assert.Equal(t, 0, seq)
}

// holdUntilFinalPlugin mirrors a buffering output guardrail: it pauses the stream on the
// first chunk and only resumes it once the final chunk has been seen and evaluated.
type holdUntilFinalPlugin struct{ paused bool }

func (p *holdUntilFinalPlugin) GetName() string { return "hold-until-final" }
func (p *holdUntilFinalPlugin) Cleanup() error  { return nil }
func (p *holdUntilFinalPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (p *holdUntilFinalPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (p *holdUntilFinalPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if !p.paused {
		p.paused = true
		ctx.PauseStream()
	}
	if final, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool); final {
		ctx.ResumeStream()
	}
	return resp, bifrostErr, nil
}

// blockAtFinalPlugin mirrors a buffering output guardrail that rejects the response once it
// has seen all of it: it drops the held chunks and ends the stream with an error.
type blockAtFinalPlugin struct{ holdUntilFinalPlugin }

func (p *blockAtFinalPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if !p.paused {
		p.paused = true
		ctx.PauseStream()
	}
	if final, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool); final {
		_ = ctx.ClearPausedStreamBuffer()
		ctx.ResumeStream()
		return nil, &schemas.BifrostError{StatusCode: schemas.Ptr(446), Error: &schemas.ErrorField{Message: "blocked by guardrail"}}, nil
	}
	return resp, bifrostErr, nil
}

type heldStreamResult struct {
	committedAfter time.Duration
	status         int
	body           string
	upstreamHits   int
}

// runHeldStream sends one streaming request through a real Bifrost whose plugin holds the
// stream, against an Anthropic upstream that pauses for hold mid-generation.
func runHeldStream(t *testing.T, plugin schemas.LLMPlugin, hold time.Duration, uri, body string) heldStreamResult {
	t.Helper()
	return runHeldStreamWith(t, plugin, hold, uri, body, func(send func(event, data string)) {
		send("message_start", heldMessageStart)
		send("content_block_start", heldTextBlockStart)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
		time.Sleep(hold)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`)
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`)
		send("message_stop", `{"type":"message_stop"}`)
	})
}

const (
	heldMessageStart   = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}`
	heldTextBlockStart = `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
)

// runHeldStreamWith is runHeldStream with the upstream's SSE events scripted by events.
func runHeldStreamWith(t *testing.T, plugin schemas.LLMPlugin, hold time.Duration, uri, body string, events func(send func(event, data string))) heldStreamResult {
	t.Helper()
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		events(func(event, data string) {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
			flusher.Flush()
		})
	}))
	t.Cleanup(upstream.Close)

	store := tracing.NewTraceStore(5*time.Minute, nil)
	t.Cleanup(store.Stop)
	tracer := tracing.NewTracer(store, nil, nil)
	t.Cleanup(tracer.Stop)
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:    passthroughTestAccount{baseURL: upstream.URL},
		LLMPlugins: []schemas.LLMPlugin{plugin},
		Tracer:     tracer,
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)

	r := router.New()
	identity := func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next }
	NewAnthropicRouter(client, &mockHandlerStore{}, nil, &testLogger{}).RegisterRoutes(r, identity)
	NewOpenAIRouter(client, &mockHandlerStore{}, nil, &testLogger{}).RegisterRoutes(r, identity)

	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI(uri)
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.SetBodyString(body)

	start := time.Now()
	returned := make(chan struct{})
	go func() {
		r.Handler(&ctx)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2*hold + 5*time.Second):
		t.Fatal("handler never returned")
	}
	res := heldStreamResult{committedAfter: time.Since(start), status: ctx.Response.StatusCode()}
	if bs := ctx.Response.BodyStream(); bs != nil {
		raw, _ := io.ReadAll(bs)
		res.body = string(raw)
	} else {
		res.body = string(ctx.Response.Body())
	}
	res.upstreamHits = int(hits.Load())
	return res
}

// A plugin that holds a stream (a buffering output guardrail) keeps every chunk out of the
// provider channel until the upstream finishes. The handler must still commit the SSE response
// and start heartbeats once the upstream has produced output; otherwise a proxy in front of
// Bifrost (Cloudflare's 125s read timeout) sees zero bytes for the whole generation and cuts the
// request with a 524, which Bifrost then logs as "client disconnected". Once committed, the
// client gets heartbeats during the hold and then every held chunk in order, with no trace of
// the internal commit marker.
func TestAnthropicHeldStreamDeliversHeartbeatsThenContent(t *testing.T) {
	const hold = 2500 * time.Millisecond
	res := runHeldStream(t, &holdUntilFinalPlugin{}, hold, "/anthropic/v1/messages",
		`{"model":"anthropic/claude-test","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	require.Equal(t, fasthttp.StatusOK, res.status, "body: %s", res.body)
	require.Less(t, res.committedAfter, hold/2, "response committed only after %s", res.committedAfter)
	heartbeat := strings.Index(res.body, ": heartbeat")
	start := strings.Index(res.body, "event: message_start")
	require.NotEqual(t, -1, heartbeat, "no heartbeat reached the client during the hold: %s", res.body)
	require.Less(t, heartbeat, start, "heartbeats must precede the held content: %s", res.body)
	hello := strings.Index(res.body, `"text":"Hello"`)
	world := strings.Index(res.body, `"text":" world"`)
	require.True(t, hello != -1 && world > hello, "held text missing or out of order: %s", res.body)
	require.Contains(t, res.body, "event: message_stop")
	require.NotContains(t, res.body, "data: {}", "the commit marker leaked to the client")
}

// The neutral OpenAI-shaped route reaches Anthropic through the same core path and must
// commit a held stream just as early.
func TestOpenAIChatHeldStreamCommitsEarly(t *testing.T) {
	const hold = 2500 * time.Millisecond
	res := runHeldStream(t, &holdUntilFinalPlugin{}, hold, "/openai/v1/chat/completions",
		`{"model":"anthropic/claude-test","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	require.Equal(t, fasthttp.StatusOK, res.status, "body: %s", res.body)
	require.Less(t, res.committedAfter, hold/2, "response committed only after %s", res.committedAfter)
	require.Contains(t, res.body, ": heartbeat")
	require.Contains(t, res.body, `"content":"Hello"`)
	require.Contains(t, res.body, "data: [DONE]")
	require.NotContains(t, res.body, "data: {}", "the commit marker leaked to the client")
}

// A guardrail that rejects a held stream at its end now reports it as an in-stream error on
// the already committed response, and the upstream is not called again for it.
func TestAnthropicHeldStreamBlockedAtEndIsInStreamError(t *testing.T) {
	const hold = 1500 * time.Millisecond
	res := runHeldStream(t, &blockAtFinalPlugin{}, hold, "/anthropic/v1/messages",
		`{"model":"anthropic/claude-test","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	require.Equal(t, fasthttp.StatusOK, res.status, "body: %s", res.body)
	require.Less(t, res.committedAfter, hold/2, "response committed only after %s", res.committedAfter)
	require.Contains(t, res.body, "event: error")
	require.Contains(t, res.body, "blocked by guardrail")
	require.NotContains(t, res.body, `"text":"Hello"`, "blocked content reached the client")
	require.Equal(t, 1, res.upstreamHits, "a guardrail block must not re-run the upstream")
}

// When the upstream's first events carry no output (an OpenAI-family model, whose startup
// events the retry loop withholds while it checks for a startup error), a held stream must not
// commit on them: committing ends that check, along with any first-token deadline. It commits
// once the first real output is held instead.
func TestOpenAIChatHeldStreamCommitsOnOutputNotStartupEvents(t *testing.T) {
	const hold = 1500 * time.Millisecond
	res := runHeldStreamWith(t, &holdUntilFinalPlugin{}, hold, "/openai/v1/chat/completions",
		`{"model":"anthropic/gpt-4o-test","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		func(send func(event, data string)) {
			send("message_start", heldMessageStart)
			time.Sleep(hold) // startup only: nothing generated yet
			send("content_block_start", heldTextBlockStart)
			send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
			time.Sleep(hold) // a long generation still in progress
			send("content_block_stop", `{"type":"content_block_stop","index":0}`)
			send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`)
			send("message_stop", `{"type":"message_stop"}`)
		})

	require.Equal(t, fasthttp.StatusOK, res.status, "body: %s", res.body)
	require.Greater(t, res.committedAfter, hold/2,
		"the response was committed after %s, on the startup event alone, before any output existed", res.committedAfter)
	require.Less(t, res.committedAfter, hold+hold/2,
		"the response was committed only after %s: the held output did not commit it", res.committedAfter)
	require.Contains(t, res.body, `"content":"Hello"`)
	require.Contains(t, res.body, "data: [DONE]")
	require.NotContains(t, res.body, "data: {}", "the commit marker leaked to the client")
}

// heldDeadlineAccount serves an OpenAI primary and an Anthropic fallback from two test upstreams.
type heldDeadlineAccount struct{ primaryURL, fallbackURL string }

func (a heldDeadlineAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}, nil
}

func (a heldDeadlineAccount) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: string(p) + "-key", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 1.0}}, nil
}

func (a heldDeadlineAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	nc := schemas.DefaultNetworkConfig
	switch p {
	case schemas.OpenAI:
		nc.BaseURL = a.primaryURL
	case schemas.Anthropic:
		nc.BaseURL = a.fallbackURL
	default:
		return nil, fmt.Errorf("unsupported provider %s", p)
	}
	return &schemas.ProviderConfig{NetworkConfig: nc, ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize}, nil
}

// A first-token deadline still governs a held stream. The startup check only learns that a held
// attempt produced output through the commit marker, and the marker disarms the deadline: an
// attempt whose first token came in time must not be cut off, or fall back, while a plugin holds
// the rest of it past the deadline.
func TestHeldStreamFirstTokenDeadline(t *testing.T) {
	const deadline = time.Second
	openAIChunk := func(w http.ResponseWriter, data string) {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		w.(http.Flusher).Flush()
	}
	stall := func(r *http.Request, d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-r.Context().Done():
			return false
		}
	}
	cases := []struct {
		name         string
		primary      func(w http.ResponseWriter, r *http.Request)
		wantContent  string
		wantFallback int32
	}{
		{
			// Output before the deadline, then a hold that outlasts it: the primary made its
			// first token in time and must not be cut off while the plugin holds the rest.
			name: "primary with output in time survives a long hold",
			primary: func(w http.ResponseWriter, r *http.Request) {
				openAIChunk(w, `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"primary"}}]}`)
				if !stall(r, 3*deadline) {
					return
				}
				openAIChunk(w, `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
				openAIChunk(w, "[DONE]")
			},
			wantContent:  "primary",
			wantFallback: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var primaryHits, fallbackHits atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryHits.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				tc.primary(w, r)
			}))
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackHits.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(event, data string) {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
					w.(http.Flusher).Flush()
				}
				send("message_start", heldMessageStart)
				send("content_block_start", heldTextBlockStart)
				send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`)
				send("content_block_stop", `{"type":"content_block_stop","index":0}`)
				send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`)
				send("message_stop", `{"type":"message_stop"}`)
			}))
			t.Cleanup(func() {
				primary.CloseClientConnections()
				fallback.CloseClientConnections()
				primary.Close()
				fallback.Close()
			})

			store := tracing.NewTraceStore(5*time.Minute, nil)
			t.Cleanup(store.Stop)
			tracer := tracing.NewTracer(store, nil, nil)
			t.Cleanup(tracer.Stop)
			client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
				Account:    heldDeadlineAccount{primaryURL: primary.URL, fallbackURL: fallback.URL},
				LLMPlugins: []schemas.LLMPlugin{&holdUntilFinalPlugin{}},
				Tracer:     tracer,
				Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
			})
			require.NoError(t, err)
			t.Cleanup(client.Shutdown)

			ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(30*time.Second))
			ctx.SetValue(schemas.BifrostContextKeyStreamFirstTokenTimeout, deadline)
			stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
				Provider:  schemas.OpenAI,
				Model:     "gpt-4o-mini",
				Input:     []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
				Fallbacks: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-test"}},
			})
			require.Nil(t, bifrostErr, "stream failed: %+v", bifrostErr)

			var content strings.Builder
			done := make(chan struct{})
			go func() {
				defer close(done)
				for chunk := range stream {
					require.False(t, schemas.IsStreamCommitMarker(chunk), "the commit marker leaked to the caller")
					if chunk.BifrostError != nil {
						t.Errorf("stream error: %+v", chunk.BifrostError.Error)
						continue
					}
					if r := chunk.BifrostChatResponse; r != nil {
						for _, c := range r.Choices {
							if c.ChatStreamResponseChoice != nil && c.ChatStreamResponseChoice.Delta != nil && c.ChatStreamResponseChoice.Delta.Content != nil {
								content.WriteString(*c.ChatStreamResponseChoice.Delta.Content)
							}
						}
					}
				}
			}()
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				t.Fatal("the stream never ended")
			}

			require.Equal(t, tc.wantContent, content.String())
			require.Equal(t, int32(1), primaryHits.Load(), "a first-token miss must not retry the primary")
			require.Equal(t, tc.wantFallback, fallbackHits.Load())
		})
	}
}
