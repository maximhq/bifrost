package integrations

import (
	"context"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

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
		{"OpenAIVideoGenerationRequest", &openai.OpenAIVideoGenerationRequest{}},
		{"OpenAIVideoRemixRequest", &openai.OpenAIVideoRemixRequest{}},
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

func TestRequestWithSettableExtraParams_OpenAIVideoGenerationRequest(t *testing.T) {
	req := &openai.OpenAIVideoGenerationRequest{}
	extra := map[string]interface{}{"guided_json": map[string]interface{}{"type": "object"}}

	rws, ok := interface{}(req).(RequestWithSettableExtraParams)
	require.True(t, ok, "OpenAIVideoGenerationRequest should implement RequestWithSettableExtraParams")
	rws.SetExtraParams(extra)

	assert.Equal(t, extra, req.GetExtraParams())
	assert.Equal(t, extra, req.VideoGenerationParameters.ExtraParams)
}

func TestExtraParamsRequiresPassthroughHeader(t *testing.T) {
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
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

func TestExtraBodyPassthrough(t *testing.T) {
	t.Run("extra_body fields propagate to ExtraParams with passthrough enabled", func(t *testing.T) {
		rawBody := []byte(`{
			"model": "vllm/meta-llama/Llama-3-8b",
			"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
			"extra_body": {
				"guided_json": {"type": "object"},
				"min_tokens": 10
			}
		}`)

		req := &openai.OpenAIChatRequest{}
		err := sonic.Unmarshal(rawBody, req)
		require.NoError(t, err)

		merged, extraBodyErr, extraParamsErr := extractSDKPassthroughParams(rawBody, req)
		require.NoError(t, extraBodyErr)
		require.NoError(t, extraParamsErr)
		interface{}(req).(RequestWithSettableExtraParams).SetExtraParams(merged)

		require.Contains(t, req.ChatParameters.ExtraParams, "guided_json")
		require.Contains(t, req.ChatParameters.ExtraParams, "min_tokens")
		assert.Equal(t, float64(10), req.ChatParameters.ExtraParams["min_tokens"])
	})

	t.Run("extra_body control fields are filtered out", func(t *testing.T) {
		rawBody := []byte(`{
			"model": "vllm/meta-llama/Llama-3-8b",
			"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
			"extra_body": {
				"provider": "should-be-filtered",
				"fallbacks": ["also-filtered"],
				"extra_params": {"nested": true},
				"guided_json": {"type": "object"}
			}
		}`)

		req := &openai.OpenAIChatRequest{}
		err := sonic.Unmarshal(rawBody, req)
		require.NoError(t, err)

		merged, extraBodyErr, extraParamsErr := extractSDKPassthroughParams(rawBody, req)
		require.NoError(t, extraBodyErr)
		require.NoError(t, extraParamsErr)
		interface{}(req).(RequestWithSettableExtraParams).SetExtraParams(merged)

		assert.NotContains(t, req.ChatParameters.ExtraParams, "provider")
		assert.NotContains(t, req.ChatParameters.ExtraParams, "fallbacks")
		assert.NotContains(t, req.ChatParameters.ExtraParams, "extra_params")
		assert.Contains(t, req.ChatParameters.ExtraParams, "guided_json")
	})

	t.Run("extra_body cannot override canonical request fields", func(t *testing.T) {
		rawBody := []byte(`{
			"model": "vllm/meta-llama/Llama-3-8b",
			"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
			"extra_body": {
				"model": "should-not-pass-through",
				"messages": [{"role": "system", "content": [{"type": "text", "text": "override"}]}],
				"stream": true,
				"guided_json": {"type": "object"}
			}
		}`)

		req := &openai.OpenAIChatRequest{}
		err := sonic.Unmarshal(rawBody, req)
		require.NoError(t, err)

		merged, extraBodyErr, extraParamsErr := extractSDKPassthroughParams(rawBody, req)
		require.NoError(t, extraBodyErr)
		require.NoError(t, extraParamsErr)
		interface{}(req).(RequestWithSettableExtraParams).SetExtraParams(merged)

		assert.NotContains(t, req.ChatParameters.ExtraParams, "model")
		assert.NotContains(t, req.ChatParameters.ExtraParams, "messages")
		assert.NotContains(t, req.ChatParameters.ExtraParams, "stream")
		assert.Contains(t, req.ChatParameters.ExtraParams, "guided_json")
	})

	t.Run("extra_params cannot override canonical request fields", func(t *testing.T) {
		rawBody := []byte(`{
			"model": "vllm/meta-llama/Llama-3-8b",
			"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
			"stream": false,
			"extra_params": {
				"model": "should-not-pass-through",
				"messages": [{"role": "system", "content": [{"type": "text", "text": "override"}]}],
				"stream": true,
				"guided_json": {"type": "object"}
			}
		}`)

		req := &openai.OpenAIChatRequest{}
		err := sonic.Unmarshal(rawBody, req)
		require.NoError(t, err)

		merged, extraBodyErr, extraParamsErr := extractSDKPassthroughParams(rawBody, req)
		require.NoError(t, extraBodyErr)
		require.NoError(t, extraParamsErr)
		interface{}(req).(RequestWithSettableExtraParams).SetExtraParams(merged)

		assert.NotContains(t, req.ChatParameters.ExtraParams, "model")
		assert.NotContains(t, req.ChatParameters.ExtraParams, "messages")
		assert.NotContains(t, req.ChatParameters.ExtraParams, "stream")
		assert.Contains(t, req.ChatParameters.ExtraParams, "guided_json")
	})

	t.Run("extra_params takes precedence over extra_body for non-canonical keys", func(t *testing.T) {
		rawBody := []byte(`{
			"model": "vllm/meta-llama/Llama-3-8b",
			"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
			"extra_body": {
				"guided_json": {"from": "extra_body"},
				"min_tokens": 5
			},
			"extra_params": {
				"guided_json": {"from": "extra_params"}
			}
		}`)

		req := &openai.OpenAIChatRequest{}
		err := sonic.Unmarshal(rawBody, req)
		require.NoError(t, err)

		merged, extraBodyErr, extraParamsErr := extractSDKPassthroughParams(rawBody, req)
		require.NoError(t, extraBodyErr)
		require.NoError(t, extraParamsErr)
		interface{}(req).(RequestWithSettableExtraParams).SetExtraParams(merged)

		// extra_params should win
		gj, ok := req.ChatParameters.ExtraParams["guided_json"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "extra_params", gj["from"])
		// extra_body-only field should still be present
		assert.Equal(t, float64(5), req.ChatParameters.ExtraParams["min_tokens"])
	})

	t.Run("extra_body without passthrough header does nothing", func(t *testing.T) {
		rawBody := []byte(`{
			"model": "vllm/meta-llama/Llama-3-8b",
			"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
			"extra_body": {
				"guided_json": {"type": "object"}
			}
		}`)

		req := &openai.OpenAIChatRequest{}
		err := sonic.Unmarshal(rawBody, req)
		require.NoError(t, err)

		bifrostCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		// Header not set -- simulate router logic
		if bifrostCtx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
			merged, extraBodyErr, extraParamsErr := extractSDKPassthroughParams(rawBody, req)
			require.NoError(t, extraBodyErr)
			require.NoError(t, extraParamsErr)
			interface{}(req).(RequestWithSettableExtraParams).SetExtraParams(merged)
		}

		assert.Empty(t, req.ChatParameters.ExtraParams,
			"ExtraParams should be empty when passthrough header is not set")
	})

	t.Run("malformed extra_body does not block valid extra_params", func(t *testing.T) {
		rawBody := []byte(`{
			"model": "vllm/meta-llama/Llama-3-8b",
			"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
			"extra_body": "not-an-object",
			"extra_params": {
				"guided_json": {"type": "object"}
			}
		}`)

		req := &openai.OpenAIChatRequest{}
		err := sonic.Unmarshal(rawBody, req)
		require.NoError(t, err)

		merged, extraBodyErr, extraParamsErr := extractSDKPassthroughParams(rawBody, req)
		require.Error(t, extraBodyErr)
		require.NoError(t, extraParamsErr)
		interface{}(req).(RequestWithSettableExtraParams).SetExtraParams(merged)

		assert.Contains(t, req.ChatParameters.ExtraParams, "guided_json")
	})
}

func TestExtraBodyPropagatesThroughChatRouteRequestConverter(t *testing.T) {
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
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
		"model": "openai/gpt-4o-mini",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
		"extra_body": {
			"guided_json": {"type": "object"},
			"model": "should-not-pass-through"
		}
	}`)

	req := chatRoute.GetRequestTypeInstance(context.Background())
	err := sonic.Unmarshal(rawBody, req)
	require.NoError(t, err)

	merged, extraBodyErr, extraParamsErr := extractSDKPassthroughParams(rawBody, req)
	require.NoError(t, extraBodyErr)
	require.NoError(t, extraParamsErr)
	interface{}(req).(RequestWithSettableExtraParams).SetExtraParams(merged)

	bifrostCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq, err := chatRoute.RequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.ChatRequest)
	require.NotNil(t, bifrostReq.ChatRequest.Params)
	assert.Contains(t, bifrostReq.ChatRequest.Params.ExtraParams, "guided_json")
	assert.NotContains(t, bifrostReq.ChatRequest.Params.ExtraParams, "model")
}

func TestExtraBodyPropagatesThroughVideoRemixRouteRequestConverter(t *testing.T) {
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
	routes := CreateOpenAIRouteConfigs("/openai", handlerStore)

	var remixRoute *RouteConfig
	for i := range routes {
		if routes[i].Path == "/openai/v1/videos/{video_id}/remix" {
			remixRoute = &routes[i]
			break
		}
	}
	require.NotNil(t, remixRoute, "should find /openai/v1/videos/{video_id}/remix route")

	rawBody := []byte(`{
		"prompt": "add dramatic lighting",
		"extra_body": {
			"guided_json": {"type": "object"}
		}
	}`)

	req := remixRoute.GetRequestTypeInstance(context.Background())
	err := sonic.Unmarshal(rawBody, req)
	require.NoError(t, err)

	merged, extraBodyErr, extraParamsErr := extractSDKPassthroughParams(rawBody, req)
	require.NoError(t, extraBodyErr)
	require.NoError(t, extraParamsErr)

	rws, ok := req.(RequestWithSettableExtraParams)
	require.True(t, ok, "video remix request should implement RequestWithSettableExtraParams")
	rws.SetExtraParams(merged)

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("video_id", "video_123:openai")
	bifrostCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	err = remixRoute.PreCallback(ctx, bifrostCtx, req)
	require.NoError(t, err)

	bifrostReq, err := remixRoute.RequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.VideoRemixRequest)
	assert.Equal(t, schemas.OpenAI, bifrostReq.VideoRemixRequest.Provider)
	assert.Equal(t, "video_123", bifrostReq.VideoRemixRequest.ID)
	assert.Contains(t, bifrostReq.VideoRemixRequest.ExtraParams, "guided_json")
}

// TestExtraParamsSetViaInterfaceMutatesOriginalReq verifies that setting extra
// params through the RequestWithSettableExtraParams interface assertion mutates
// the original req (interface{}) value. This matters because createHandler
// passes req to config.RequestConverter after the extra params block -- both
// variables must reference the same underlying struct via pointer semantics.
func TestExtraParamsSetViaInterfaceMutatesOriginalReq(t *testing.T) {
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
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
