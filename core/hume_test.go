package bifrost

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type humeModelCatalogStub struct {
	models map[schemas.ModelProvider]map[string]*schemas.Model
}

func (s *humeModelCatalogStub) GetModelInfo(provider schemas.ModelProvider, model string) *schemas.Model {
	return s.models[provider][model]
}

func (s *humeModelCatalogStub) CalculateRequestCost(_ *schemas.BifrostContext, _ *schemas.BifrostResponse) float64 {
	return 0
}

type humeConstraintTracer struct {
	schemas.NoOpTracer
	logs []schemas.PluginLogEntry
}

func (t *humeConstraintTracer) AttachPluginLogs(_ string, logs []schemas.PluginLogEntry) {
	t.logs = append(t.logs, logs...)
}

type humeConstraintPlugin struct {
	name          string
	recoverError  bool
	suppressError bool
	addTool       *schemas.ChatTool
	recorder      *humeConstraintRecorder
}

type humeConstraintRecorder struct {
	preOrder           []string
	postOrder          []string
	postFinalState     []bool
	sawConstraintError bool
}

func (p *humeConstraintPlugin) GetName() string { return p.name }
func (p *humeConstraintPlugin) Cleanup() error  { return nil }
func (p *humeConstraintPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}
func (p *humeConstraintPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	p.recorder.preOrder = append(p.recorder.preOrder, p.name)
	if p.addTool != nil {
		if req.ChatRequest.Params == nil {
			req.ChatRequest.Params = &schemas.ChatParameters{}
		}
		req.ChatRequest.Params.Tools = append(req.ChatRequest.Params.Tools, *p.addTool)
	}
	return req, nil, nil
}
func (p *humeConstraintPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	p.recorder.postOrder = append(p.recorder.postOrder, p.name)
	p.recorder.postFinalState = append(p.recorder.postFinalState, IsFinalChunk(ctx))
	if bifrostErr != nil && bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
		p.recorder.sawConstraintError = true
	}
	ctx.Log(schemas.LogLevelInfo, "handled serial tool constraint error")
	if p.suppressError {
		return nil, nil, nil
	}
	if !p.recoverError {
		return resp, bifrostErr, nil
	}
	return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{ID: "hume-post-hook-recovery"}}, nil, nil
}

func newHumeConstraintTestClient(t *testing.T, tracer schemas.Tracer, plugins ...schemas.LLMPlugin) *Bifrost {
	t.Helper()
	client := newCatalogProbeClient(t, &humeModelCatalogStub{models: map[schemas.ModelProvider]map[string]*schemas.Model{
		schemas.OpenAI: {
			"o3-mini": {SupportedParameters: []string{"tools"}},
		},
	}}, plugins...)
	client.SetTracer(tracer)
	return client
}

func newRejectedHumeConstraintRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "o3-mini",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
		Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
			Type: schemas.ChatToolTypeFunction,
		}}},
	}
}

func TestEnforceSingleToolConstraint(t *testing.T) {
	newRequest := func(provider schemas.ModelProvider, model string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionStreamRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: provider,
				Model:    model,
				Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
					Type: schemas.ChatToolTypeFunction,
				}}},
			},
		}
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
	ctx.SetValue(schemas.BifrostContextKeyModelCatalog, &humeModelCatalogStub{models: map[schemas.ModelProvider]map[string]*schemas.Model{
		schemas.OpenAI: {
			"gpt-4o-mini": {SupportedParameters: []string{"parallel_tool_calls"}},
			"o3-mini":     {SupportedParameters: []string{"tools"}},
		},
		schemas.Gemini: {
			"gemini-test": {SupportedParameters: []string{"parallel_tool_calls"}},
		},
	}})

	t.Run("OpenAI model with wire support is constrained", func(t *testing.T) {
		req := newRequest(schemas.OpenAI, "gpt-4o-mini")
		require.Nil(t, (&Bifrost{}).enforceSingleToolConstraint(ctx, req))
		require.NotNil(t, req.ChatRequest.Params.ParallelToolCalls)
		assert.False(t, *req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("Anthropic is translated by its request converter", func(t *testing.T) {
		req := newRequest(schemas.Anthropic, "claude-sonnet-4-5")
		require.Nil(t, (&Bifrost{}).enforceSingleToolConstraint(ctx, req))
		require.NotNil(t, req.ChatRequest.Params.ParallelToolCalls)
		assert.False(t, *req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("unsupported OpenAI model is rejected instead of receiving an invalid field", func(t *testing.T) {
		req := newRequest(schemas.OpenAI, "o3-mini")
		err := (&Bifrost{}).enforceSingleToolConstraint(ctx, req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, "cannot guarantee")
		assert.Equal(t, schemas.Ptr(400), err.StatusCode)
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("native provider without a wire mapping is rejected", func(t *testing.T) {
		req := newRequest(schemas.Gemini, "gemini-test")
		err := (&Bifrost{}).enforceSingleToolConstraint(ctx, req)
		require.NotNil(t, err)
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("provider-bound tools are constrained", func(t *testing.T) {
		req := newRequest(schemas.OpenAI, "gpt-4o-mini")
		req.ChatRequest.Params.ParallelToolCalls = nil
		require.Nil(t, (&Bifrost{}).enforceSingleToolConstraint(ctx, req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("custom OpenAI-compatible provider uses its base provider capability", func(t *testing.T) {
		customProvider := schemas.ModelProvider("custom-openai")
		account := NewMockAccount()
		account.configs[customProvider] = &schemas.ProviderConfig{CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: schemas.OpenAI,
		}}
		req := newRequest(customProvider, "gpt-4o-mini")
		require.Nil(t, (&Bifrost{account: account}).enforceSingleToolConstraint(ctx, req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("catalog-absent OpenAI-compatible model is constrained", func(t *testing.T) {
		catalogAbsentCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		catalogAbsentCtx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
		req := newRequest(schemas.Ollama, "local-model")
		require.Nil(t, (&Bifrost{}).enforceSingleToolConstraint(catalogAbsentCtx, req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("Vertex all-digit endpoint IDs route to the Gemini builder and are rejected", func(t *testing.T) {
		req := newRequest(schemas.Vertex, "1234567890")
		err := (&Bifrost{}).enforceSingleToolConstraint(ctx, req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, "cannot guarantee")
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("Vertex non-Gemini names still use the OpenAI wire", func(t *testing.T) {
		req := newRequest(schemas.Vertex, "meta/llama-3.1-405b-instruct-maas")
		require.Nil(t, (&Bifrost{}).enforceSingleToolConstraint(ctx, req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	newAliasClient := func(provider schemas.ModelProvider, keys ...schemas.Key) *Bifrost {
		account := NewMockAccount()
		account.SetKeysForProvider(provider, keys)
		return &Bifrost{account: account}
	}
	aliasKey := func(id string, enabled bool, models schemas.WhiteList, aliases schemas.KeyAliases) schemas.Key {
		return schemas.Key{
			ID:      id,
			Value:   *schemas.NewSecretVar("sk-test"),
			Enabled: schemas.Ptr(enabled),
			Models:  models,
			Aliases: aliases,
		}
	}

	t.Run("key alias is checked against the model it resolves to", func(t *testing.T) {
		client := newAliasClient(schemas.OpenAI, aliasKey("openai-alias", true, schemas.WhiteList{"*"},
			schemas.KeyAliases{"voice": {ModelID: "o3-mini"}}))
		req := newRequest(schemas.OpenAI, "voice")
		err := client.enforceSingleToolConstraint(ctx, req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, `model "voice" cannot guarantee serial tool execution (key alias resolves to "o3-mini")`)
		assert.Equal(t, schemas.Ptr(400), err.StatusCode)
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("key alias to a supported model is constrained", func(t *testing.T) {
		client := newAliasClient(schemas.OpenAI, aliasKey("openai-alias", true, schemas.WhiteList{"*"},
			schemas.KeyAliases{"fast": {ModelID: "gpt-4o-mini"}}))
		req := newRequest(schemas.OpenAI, "fast")
		require.Nil(t, client.enforceSingleToolConstraint(ctx, req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("key alias to a Vertex Gemini model is rejected before the worker resolves it", func(t *testing.T) {
		client := newAliasClient(schemas.Vertex, aliasKey("vertex-alias", true, schemas.WhiteList{"*"},
			schemas.KeyAliases{"voice-fast": {ModelID: "gemini-2.5-flash"}}))
		req := newRequest(schemas.Vertex, "voice-fast")
		err := client.enforceSingleToolConstraint(ctx, req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, `key alias resolves to "gemini-2.5-flash"`)
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("aliases on keys that cannot serve the request are ignored", func(t *testing.T) {
		client := newAliasClient(schemas.OpenAI,
			aliasKey("disabled", false, schemas.WhiteList{"*"}, schemas.KeyAliases{"voice": {ModelID: "o3-mini"}}),
			aliasKey("other-models", true, schemas.WhiteList{"gpt-4o-mini"}, schemas.KeyAliases{"voice": {ModelID: "o3-mini"}}),
			aliasKey("serving", true, schemas.WhiteList{"*"}, schemas.KeyAliases{"voice": {ModelID: "gpt-4o-mini"}}),
		)
		req := newRequest(schemas.OpenAI, "voice")
		require.Nil(t, client.enforceSingleToolConstraint(ctx, req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("a direct key's alias takes precedence over configured keys", func(t *testing.T) {
		client := newAliasClient(schemas.OpenAI, aliasKey("configured", true, schemas.WhiteList{"*"},
			schemas.KeyAliases{"voice": {ModelID: "gpt-4o-mini"}}))
		directCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		directCtx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
		directCtx.SetValue(schemas.BifrostContextKeyModelCatalog, ctx.Value(schemas.BifrostContextKeyModelCatalog))
		directCtx.SetValue(schemas.BifrostContextKeyDirectKey, aliasKey("direct", true, schemas.WhiteList{"*"},
			schemas.KeyAliases{"voice": {ModelID: "o3-mini"}}))
		req := newRequest(schemas.OpenAI, "voice")
		err := client.enforceSingleToolConstraint(directCtx, req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, `key alias resolves to "o3-mini"`)
	})

	t.Run("requests without the policy are unchanged", func(t *testing.T) {
		otherCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		req := newRequest(schemas.OpenAI, "gpt-4o-mini")
		require.Nil(t, (&Bifrost{}).enforceSingleToolConstraint(otherCtx, req))
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})
}

func TestHumeConstraintAppliesToPluginAddedTools(t *testing.T) {
	recorder := &humeConstraintRecorder{}
	pluginTool := &schemas.ChatTool{
		Type:     schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{Name: "plugin_tool"},
	}
	plugin := &humeConstraintPlugin{
		name:     "tool-adder",
		addTool:  pluginTool,
		recorder: recorder,
	}
	client := newHumeConstraintTestClient(t, &humeConstraintTracer{}, plugin)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)

	request := newRejectedHumeConstraintRequest()
	request.Params.Tools = nil
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, request)

	assert.Nil(t, stream)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "cannot guarantee")
	assert.Equal(t, []string{"tool-adder"}, recorder.preOrder)
	assert.Equal(t, []string{"tool-adder"}, recorder.postOrder)
	assert.True(t, recorder.sawConstraintError)
	assert.Equal(t, []bool{true}, recorder.postFinalState)
}

func TestHumeConstraintErrorRunsPostHooks(t *testing.T) {
	tests := []struct {
		name      string
		streaming bool
	}{
		{name: "unary", streaming: false},
		{name: "streaming", streaming: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &humeConstraintRecorder{}
			tracer := &humeConstraintTracer{}
			outer := &humeConstraintPlugin{
				name:         "outer",
				recoverError: true,
				recorder:     recorder,
			}
			inner := &humeConstraintPlugin{
				name:     "inner",
				recorder: recorder,
			}
			client := newHumeConstraintTestClient(t, tracer, outer, inner)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)
			ctx.SetValue(schemas.BifrostContextKeyTraceID, "hume-constraint-trace")

			if tt.streaming {
				stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, newRejectedHumeConstraintRequest())
				require.Nil(t, bifrostErr)
				require.NotNil(t, stream)
				chunk, ok := <-stream
				require.True(t, ok)
				require.NotNil(t, chunk)
				require.NotNil(t, chunk.BifrostChatResponse)
				assert.Equal(t, "hume-post-hook-recovery", chunk.BifrostChatResponse.ID)
				_, ok = <-stream
				assert.False(t, ok)
			} else {
				resp, bifrostErr := client.ChatCompletionRequest(ctx, newRejectedHumeConstraintRequest())
				require.Nil(t, bifrostErr)
				require.NotNil(t, resp)
				assert.Equal(t, "hume-post-hook-recovery", resp.ID)
			}

			assert.Equal(t, []string{"outer", "inner"}, recorder.preOrder)
			assert.Equal(t, []string{"inner", "outer"}, recorder.postOrder)
			assert.True(t, recorder.sawConstraintError)
			assert.Equal(t, []bool{tt.streaming, tt.streaming}, recorder.postFinalState)
			require.Len(t, tracer.logs, 2)
			assert.Nil(t, ctx.GetPluginLogs())
		})
	}
}

func TestUnaryConstraintKeepsOriginalErrorWhenPostHookReturnsNilNil(t *testing.T) {
	recorder := &humeConstraintRecorder{}
	plugin := &humeConstraintPlugin{name: "suppressor", suppressError: true, recorder: recorder}
	client := newHumeConstraintTestClient(t, &humeConstraintTracer{}, plugin)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequireSerialToolCalls, true)

	resp, bifrostErr := client.ChatCompletionRequest(ctx, newRejectedHumeConstraintRequest())

	assert.Nil(t, resp)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "cannot guarantee serial tool execution")
}
