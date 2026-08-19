package governance

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAttributionTestPlugin(t *testing.T, governanceConfig *configstore.GovernanceConfig) (*GovernancePlugin, *LocalGovernanceStore) {
	t.Helper()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, governanceConfig, nil)
	require.NoError(t, err)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin, store
}

func attributionRequest(requestType schemas.RequestType, provider schemas.ModelProvider, model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: requestType,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: provider,
			Model:    model,
		},
	}
}

func streamAttributionResponse(provider schemas.ModelProvider, model string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionStreamRequest,
				Provider:               provider,
				OriginalModelRequested: model,
				ResolvedModelUsed:      model,
			},
		},
	}
}

func governanceIDs(t *testing.T, ctx *schemas.BifrostContext, key schemas.BifrostContextKey) []string {
	t.Helper()
	ids, ok := ctx.Value(key).([]string)
	require.True(t, ok, "context key %q must contain a typed []string snapshot", key)
	return ids
}

func TestPreLLMHookSnapshotsAllApplicableGovernanceIDs(t *testing.T) {
	providerBudget := buildBudget("provider-budget", 1000, "1h")
	providerRateLimit := buildRateLimit("provider-rate-limit", 100000, 1000)
	provider := buildProviderWithGovernance("openai", providerBudget, providerRateLimit)
	modelBudget := buildBudget("model-budget", 1000, "1h")
	modelRateLimit := buildRateLimit("model-rate-limit", 100000, 1000)
	modelConfig := buildModelConfig("model-config", "gpt-4", nil, modelBudget, modelRateLimit)
	vkBudget := buildBudget("vk-budget", 1000, "1h")
	vkRateLimit := buildRateLimit("vk-rate-limit", 100000, 1000)
	vk := buildVirtualKeyWithBudget("vk-id", "sk-bf-attribution", "Attribution VK", vkBudget)
	vk.RateLimit = vkRateLimit
	vk.RateLimitID = &vkRateLimit.ID

	plugin, _ := newAttributionTestPlugin(t, &configstore.GovernanceConfig{
		Providers:    []configstoreTables.TableProvider{*provider},
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		Budgets:      []configstoreTables.TableBudget{*providerBudget, *modelBudget, *vkBudget},
		RateLimits:   []configstoreTables.TableRateLimit{*providerRateLimit, *modelRateLimit, *vkRateLimit},
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-attribution")
	_, shortCircuit, err := plugin.PreLLMHook(ctx, attributionRequest(schemas.ChatCompletionStreamRequest, schemas.OpenAI, "gpt-4"))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)

	assert.ElementsMatch(t, []string{"provider-budget", "model-budget", "vk-budget"}, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceBudgetIDs))
	assert.ElementsMatch(t, []string{"provider-rate-limit", "model-rate-limit", "vk-rate-limit"}, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceRateLimitIDs))
}

func TestPreLLMHookReplacesAttributionAcrossFallbackAttempts(t *testing.T) {
	openAIBudget := buildBudget("openai-budget", 1000, "1h")
	anthropicBudget := buildBudget("anthropic-budget", 1000, "1h")
	plugin, _ := newAttributionTestPlugin(t, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{
			*buildProviderWithGovernance("openai", openAIBudget, nil),
			*buildProviderWithGovernance("anthropic", anthropicBudget, nil),
		},
		Budgets: []configstoreTables.TableBudget{*openAIBudget, *anthropicBudget},
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, attributionRequest(schemas.ChatCompletionStreamRequest, schemas.OpenAI, "gpt-4"))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assert.Equal(t, []string{"openai-budget"}, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceBudgetIDs))

	// Fallbacks rerun PreLLMHook on the same root context.
	_, shortCircuit, err = plugin.PreLLMHook(ctx, attributionRequest(schemas.ChatCompletionStreamRequest, schemas.Anthropic, "claude"))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assert.Equal(t, []string{"anthropic-budget"}, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceBudgetIDs))

	// An attempt without governance must shadow, not inherit, the prior IDs.
	_, shortCircuit, err = plugin.PreLLMHook(ctx, attributionRequest(schemas.ChatCompletionStreamRequest, schemas.Gemini, "gemini-pro"))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assert.Empty(t, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceBudgetIDs))
	assert.Empty(t, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceRateLimitIDs))

	// Rejected attempts must also clear attribution left by an earlier admitted
	// attempt; they do not represent billable governance usage.
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "missing-virtual-key")
	_, shortCircuit, err = plugin.PreLLMHook(ctx, attributionRequest(schemas.ChatCompletionStreamRequest, schemas.OpenAI, "gpt-4"))
	require.NoError(t, err)
	require.NotNil(t, shortCircuit)
	assert.Empty(t, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceBudgetIDs))
	assert.Empty(t, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceRateLimitIDs))
}

func TestPreLLMHookSnapshotHonorsSkipVirtualKeyUsageTracking(t *testing.T) {
	providerBudget := buildBudget("provider-budget", 1000, "1h")
	vkBudget := buildBudget("vk-budget", 1000, "1h")
	vk := buildVirtualKeyWithBudget("vk-id", "sk-bf-skip-vk", "Skip VK", vkBudget)
	plugin, _ := newAttributionTestPlugin(t, &configstore.GovernanceConfig{
		Providers:   []configstoreTables.TableProvider{*buildProviderWithGovernance("openai", providerBudget, nil)},
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*providerBudget, *vkBudget},
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-skip-vk")
	ctx.SetValue(schemas.BifrostContextKeySkipVirtualKeyUsageTracking, true)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, attributionRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4"))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)

	assert.Equal(t, []string{"provider-budget"}, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceBudgetIDs))
}

func TestPostLLMHookIntermediateChunksKeepAdmissionSnapshot(t *testing.T) {
	admissionBudget := buildBudget("admission-budget", 1000, "1h")
	plugin, store := newAttributionTestPlugin(t, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*buildProviderWithGovernance("openai", admissionBudget, nil)},
		Budgets:   []configstoreTables.TableBudget{*admissionBudget},
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "stream-attribution")
	_, shortCircuit, err := plugin.PreLLMHook(ctx, attributionRequest(schemas.ChatCompletionStreamRequest, schemas.OpenAI, "gpt-4"))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)

	// Simulate a hot reload while the stream is in flight. Neither intermediate
	// nor final PostLLMHook calls should replace the admission-time attribution.
	reloadedBudget := buildBudget("reloaded-budget", 1000, "1h")
	store.UpdateProviderInMemory(ctx, buildProviderWithGovernance("openai", reloadedBudget, nil))
	for i := 0; i < 10; i++ {
		result, bifrostErr, hookErr := plugin.PostLLMHook(ctx, streamAttributionResponse(schemas.OpenAI, "gpt-4"), nil)
		require.NoError(t, hookErr)
		require.NotNil(t, result)
		require.Nil(t, bifrostErr)
	}
	assert.Equal(t, []string{"admission-budget"}, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceBudgetIDs))

	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	_, _, err = plugin.PostLLMHook(ctx, streamAttributionResponse(schemas.OpenAI, "gpt-4"), nil)
	require.NoError(t, err)
	plugin.wg.Wait()
	assert.Equal(t, []string{"admission-budget"}, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceBudgetIDs))
}

func TestPostLLMHookStreamErrorWithoutFinalKeepsAdmissionSnapshot(t *testing.T) {
	admissionBudget := buildBudget("admission-budget", 1000, "1h")
	plugin, store := newAttributionTestPlugin(t, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*buildProviderWithGovernance("openai", admissionBudget, nil)},
		Budgets:   []configstoreTables.TableBudget{*admissionBudget},
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "stream-error-without-final")
	_, shortCircuit, err := plugin.PreLLMHook(ctx, attributionRequest(schemas.ChatCompletionStreamRequest, schemas.OpenAI, "gpt-4"))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	require.False(t, falseValue(ctx.Value(schemas.BifrostContextKeyStreamEndIndicator)))

	reloadedBudget := buildBudget("reloaded-budget", 1000, "1h")
	store.UpdateProviderInMemory(ctx, buildProviderWithGovernance("openai", reloadedBudget, nil))

	bifrostErr := &schemas.BifrostError{
		Error: &schemas.ErrorField{Message: "upstream stream error"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ChatCompletionStreamRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: "gpt-4",
			ResolvedModelUsed:      "gpt-4",
		},
	}
	_, returnedErr, hookErr := plugin.PostLLMHook(ctx, nil, bifrostErr)
	require.NoError(t, hookErr)
	require.Same(t, bifrostErr, returnedErr)
	assert.Equal(t, []string{"admission-budget"}, governanceIDs(t, ctx, schemas.BifrostContextKeyGovernanceBudgetIDs))
}

func falseValue(value any) bool {
	flag, _ := value.(bool)
	return flag
}
