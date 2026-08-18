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
