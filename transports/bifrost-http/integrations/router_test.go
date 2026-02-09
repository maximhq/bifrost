package integrations

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		req := chatRoute.GetRequestTypeInstance()
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
		req := chatRoute.GetRequestTypeInstance()
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
	req := chatRoute.GetRequestTypeInstance() // returns interface{}
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
