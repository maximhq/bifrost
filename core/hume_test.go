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
	name               string
	recoverError       bool
	preOrder           *[]string
	postOrder          *[]string
	postFinalState     *[]bool
	sawConstraintError *bool
}

func (p *humeConstraintPlugin) GetName() string { return p.name }
func (p *humeConstraintPlugin) Cleanup() error  { return nil }
func (p *humeConstraintPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}
func (p *humeConstraintPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	*p.preOrder = append(*p.preOrder, p.name)
	return req, nil, nil
}
func (p *humeConstraintPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	*p.postOrder = append(*p.postOrder, p.name)
	*p.postFinalState = append(*p.postFinalState, IsFinalChunk(ctx))
	if bifrostErr != nil && bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
		*p.sawConstraintError = true
	}
	ctx.Log(schemas.LogLevelInfo, "handled Hume constraint error")
	if !p.recoverError {
		return resp, bifrostErr, nil
	}
	return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{ID: "hume-post-hook-recovery"}}, nil, nil
}

func newHumeConstraintTestClient(t *testing.T, tracer schemas.Tracer, plugins ...schemas.LLMPlugin) *Bifrost {
	t.Helper()
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     NewNoOpLogger(),
		Tracer:     tracer,
		LLMPlugins: plugins,
		ModelCatalog: &humeModelCatalogStub{models: map[schemas.ModelProvider]map[string]*schemas.Model{
			schemas.OpenAI: {
				"o3-mini": {SupportedParameters: []string{"tools"}},
			},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
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

func TestEnforceHumeSingleToolConstraint(t *testing.T) {
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
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, humeIntegrationType)
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
		require.Nil(t, (&Bifrost{}).enforceHumeSingleToolConstraint(ctx, req))
		require.NotNil(t, req.ChatRequest.Params.ParallelToolCalls)
		assert.False(t, *req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("Anthropic is translated by its request converter", func(t *testing.T) {
		req := newRequest(schemas.Anthropic, "claude-sonnet-4-5")
		require.Nil(t, (&Bifrost{}).enforceHumeSingleToolConstraint(ctx, req))
		require.NotNil(t, req.ChatRequest.Params.ParallelToolCalls)
		assert.False(t, *req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("unsupported OpenAI model is rejected instead of receiving an invalid field", func(t *testing.T) {
		req := newRequest(schemas.OpenAI, "o3-mini")
		err := (&Bifrost{}).enforceHumeSingleToolConstraint(ctx, req)
		require.NotNil(t, err)
		assert.Contains(t, err.Error.Message, "cannot guarantee")
		assert.Equal(t, schemas.Ptr(400), err.StatusCode)
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("native provider without a wire mapping is rejected", func(t *testing.T) {
		req := newRequest(schemas.Gemini, "gemini-test")
		err := (&Bifrost{}).enforceHumeSingleToolConstraint(ctx, req)
		require.NotNil(t, err)
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("tools injected into the final request are constrained", func(t *testing.T) {
		req := newRequest(schemas.OpenAI, "gpt-4o-mini")
		req.ChatRequest.Params.ParallelToolCalls = nil
		require.Nil(t, (&Bifrost{}).enforceHumeSingleToolConstraint(ctx, req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("custom OpenAI-compatible provider uses its base provider capability", func(t *testing.T) {
		customProvider := schemas.ModelProvider("custom-openai")
		account := NewMockAccount()
		account.configs[customProvider] = &schemas.ProviderConfig{CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: schemas.OpenAI,
		}}
		req := newRequest(customProvider, "gpt-4o-mini")
		require.Nil(t, (&Bifrost{account: account}).enforceHumeSingleToolConstraint(ctx, req))
		assert.Equal(t, schemas.Ptr(false), req.ChatRequest.Params.ParallelToolCalls)
	})

	t.Run("other integrations are unchanged", func(t *testing.T) {
		otherCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		otherCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "openai")
		req := newRequest(schemas.OpenAI, "gpt-4o-mini")
		require.Nil(t, (&Bifrost{}).enforceHumeSingleToolConstraint(otherCtx, req))
		assert.Nil(t, req.ChatRequest.Params.ParallelToolCalls)
	})
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
			var preOrder []string
			var postOrder []string
			var postFinalState []bool
			var sawConstraintError bool
			tracer := &humeConstraintTracer{}
			outer := &humeConstraintPlugin{
				name:               "outer",
				recoverError:       true,
				preOrder:           &preOrder,
				postOrder:          &postOrder,
				postFinalState:     &postFinalState,
				sawConstraintError: &sawConstraintError,
			}
			inner := &humeConstraintPlugin{
				name:               "inner",
				preOrder:           &preOrder,
				postOrder:          &postOrder,
				postFinalState:     &postFinalState,
				sawConstraintError: &sawConstraintError,
			}
			client := newHumeConstraintTestClient(t, tracer, outer, inner)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyIntegrationType, humeIntegrationType)
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

			assert.Equal(t, []string{"outer", "inner"}, preOrder)
			assert.Equal(t, []string{"inner", "outer"}, postOrder)
			assert.True(t, sawConstraintError)
			assert.Equal(t, []bool{tt.streaming, tt.streaming}, postFinalState)
			require.Len(t, tracer.logs, 2)
			assert.Nil(t, ctx.GetPluginLogs())
		})
	}
}
