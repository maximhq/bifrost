package governance

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Store Tests - Provider Budget
// ============================================================================

func TestStore_CheckProviderBudget_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	_, err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err, "Should allow when no provider config exists")
}

func TestStore_CheckProviderBudget_NoBudget(t *testing.T) {
	logger := NewMockLogger()
	provider := buildProviderWithGovernance("openai", nil, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
	}, nil)
	require.NoError(t, err)

	_, err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err, "Should allow when provider has no budget")
}

func TestStore_CheckProviderBudget_WithinLimit(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	provider := buildProviderWithGovernance("openai", budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
		Budgets:   []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	_, err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err, "Should allow when budget is within limit")
}

func TestStore_CheckProviderBudget_Exceeded(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	provider := buildProviderWithGovernance("openai", budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
		Budgets:   []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	_, err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.Error(t, err, "Should reject when budget is exceeded")
	assert.Contains(t, err.Error(), "budget exceeded")
}

func TestStore_CheckProviderBudget_WithBaseline(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 90.0, "1h")
	provider := buildProviderWithGovernance("openai", budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
		Budgets:   []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	// With baseline that would exceed limit
	baselines := map[string]float64{"budget1": 15.0}
	_, err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, baselines)
	assert.Error(t, err, "Should reject when current usage + baseline exceeds limit")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// ============================================================================
// Store Tests - Provider Rate Limit
// ============================================================================

func TestStore_CheckProviderRateLimit_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	decision, err := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.NoError(t, err, "Should allow when no provider config exists")
	assert.Equal(t, DecisionAllow, decision)
}

func TestStore_CheckProviderRateLimit_NoRateLimit(t *testing.T) {
	logger := NewMockLogger()
	provider := buildProviderWithGovernance("openai", nil, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
	}, nil)
	require.NoError(t, err)

	decision, err := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.NoError(t, err, "Should allow when provider has no rate limit")
	assert.Equal(t, DecisionAllow, decision)
}

func TestStore_CheckProviderRateLimit_TokenLimitExceeded(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	provider := buildProviderWithGovernance("openai", nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:  []configstoreTables.TableProvider{*provider},
		RateLimits: []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	decision, err := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.Error(t, err, "Should reject when provider token limit is exceeded")
	assert.Equal(t, DecisionTokenLimited, decision)
	assert.Contains(t, err.Error(), "token limit exceeded")
}

func TestStore_CheckProviderRateLimit_RequestLimitExceeded(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 1000) // Requests at max
	provider := buildProviderWithGovernance("openai", nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:  []configstoreTables.TableProvider{*provider},
		RateLimits: []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	decision, err := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.Error(t, err, "Should reject when provider request limit is exceeded")
	assert.Equal(t, DecisionRequestLimited, decision)
	assert.Contains(t, err.Error(), "request limit exceeded")
}

func TestStore_CheckProviderRateLimit_BothLimitsExceeded(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 1000) // Both at max
	provider := buildProviderWithGovernance("openai", nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:  []configstoreTables.TableProvider{*provider},
		RateLimits: []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	decision, err := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.Error(t, err, "Should reject when both provider token and request limits are exceeded")
	assert.Equal(t, DecisionRateLimited, decision) // General rate limited when both are exceeded
	assert.Contains(t, err.Error(), "rate limit")
}

func TestStore_CheckProviderRateLimit_WithinLimits(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 5000, 1000, 500) // Both within limits
	provider := buildProviderWithGovernance("openai", nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:  []configstoreTables.TableProvider{*provider},
		RateLimits: []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	decision, err := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.NoError(t, err, "Should allow when provider rate limits are within limits")
	assert.Equal(t, DecisionAllow, decision)
}

// ============================================================================
// Store Tests - Model Budget
// ============================================================================

func TestStore_CheckModelBudget_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.NoError(t, err, "Should allow when no model config exists")
}

func TestStore_CheckModelBudget_ModelOnly_WithinLimit(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.NoError(t, err, "Should allow when model budget is within limit")
}

func TestStore_CheckModelBudget_ModelOnly_Exceeded(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.Error(t, err, "Should reject when model budget is exceeded")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// buildModelConfigMultiBudget builds a global model config owning multiple budgets
// (via TableBudget.ModelConfigID), for multi-budget enforcement tests.
func buildModelConfigMultiBudget(id, model string, provider *string, budgets []*configstoreTables.TableBudget) *configstoreTables.TableModelConfig {
	mc := &configstoreTables.TableModelConfig{
		ID:        id,
		ModelName: model,
		Provider:  provider,
		Scope:     configstoreTables.ModelConfigScopeGlobal,
	}
	for _, b := range budgets {
		b.ModelConfigID = &mc.ID
		mc.Budgets = append(mc.Budgets, *b)
	}
	return mc
}

func TestStore_CheckModelBudget_MultiBudget_OneExceededBlocks(t *testing.T) {
	logger := NewMockLogger()
	within := buildBudget("b-day", 100.0, "1d")                  // plenty of headroom
	exceeded := buildBudgetWithUsage("b-hour", 10.0, 10.0, "1h") // at limit
	mc := buildModelConfigMultiBudget("mc-multi", "gpt-4", nil, []*configstoreTables.TableBudget{within, exceeded})
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*within, *exceeded},
	}, nil)
	require.NoError(t, err)

	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil)
	assert.Error(t, err, "the exceeded budget among several on one config must block")
	assert.Contains(t, err.Error(), "budget exceeded")
}

func TestStore_CheckModelBudget_MultiBudget_AllWithinPasses(t *testing.T) {
	logger := NewMockLogger()
	b1 := buildBudget("b-day", 100.0, "1d")
	b2 := buildBudget("b-hour", 10.0, "1h")
	mc := buildModelConfigMultiBudget("mc-multi", "gpt-4", nil, []*configstoreTables.TableBudget{b1, b2})
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*b1, *b2},
	}, nil)
	require.NoError(t, err)

	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err, "all budgets within limit should pass")
}

func TestStore_UpdateModelBudgetUsage_MultiBudget_BumpsAll(t *testing.T) {
	logger := NewMockLogger()
	b1 := buildBudget("b-day", 100.0, "1d")
	b2 := buildBudget("b-hour", 50.0, "1h")
	mc := buildModelConfigMultiBudget("mc-multi", "gpt-4", nil, []*configstoreTables.TableBudget{b1, b2})
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*b1, *b2},
	}, nil)
	require.NoError(t, err)

	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "gpt-4", schemas.OpenAI, 7.5)
	require.NoError(t, err)

	for _, id := range []string{"b-day", "b-hour"} {
		b := store.LoadBudget(context.Background(), id)
		require.NotNil(t, b, "budget %s should be loadable", id)
		assert.InDelta(t, 7.5, b.CurrentUsage, 0.001, "every budget on the config must be bumped (budget %s)", id)
	}
}

func TestStore_CheckModelBudget_ModelWithProvider_WithinLimit(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4", &providerStr, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.NoError(t, err, "Should allow when model+provider budget is within limit")
}

func TestStore_CheckModelBudget_ModelWithProvider_Exceeded(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4", &providerStr, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.Error(t, err, "Should reject when model+provider budget is exceeded")
	assert.Contains(t, err.Error(), "budget exceeded")
}

func TestStore_CheckModelBudget_BothModelAndModelProvider_ChecksBoth(t *testing.T) {
	logger := NewMockLogger()
	// Model-only budget (exceeded)
	budget1 := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h")
	modelConfig1 := buildModelConfig("mc1", "gpt-4", nil, budget1, nil)
	// Model+provider budget (within limit)
	budget2 := buildBudget("budget2", 200.0, "1h")
	providerStr := "openai"
	modelConfig2 := buildModelConfig("mc2", "gpt-4", &providerStr, budget2, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig1, *modelConfig2},
		Budgets:      []configstoreTables.TableBudget{*budget1, *budget2},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.Error(t, err, "Should reject when model-only budget is exceeded, even if model+provider budget is OK")
	assert.Contains(t, err.Error(), "budget exceeded")
}

func TestStore_CheckModelBudget_ProviderSpecific_DifferentProvider_Passes(t *testing.T) {
	logger := NewMockLogger()
	// OpenAI GPT-4O has budget (exceeded)
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4o", &providerStr, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	// Request with Azure (different provider) for same model should pass
	provider := schemas.Azure
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4o", Provider: provider}, nil)
	assert.NoError(t, err, "Should allow when model config is provider-specific and different provider is used")
}

// ============================================================================
// Store Tests - Model Rate Limit
// ============================================================================

func TestStore_CheckModelRateLimit_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when no model config exists")
	assert.Equal(t, DecisionAllow, decision)
}

func TestStore_CheckModelRateLimit_ModelOnly_TokenLimitExceeded(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.Error(t, err, "Should reject when model token limit is exceeded")
	assert.Equal(t, DecisionTokenLimited, decision)
	assert.Contains(t, err.Error(), "token limit exceeded")
}

func TestStore_CheckModelRateLimit_ModelOnly_RequestLimitExceeded(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 1000) // Requests at max
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.Error(t, err, "Should reject when model request limit is exceeded")
	assert.Equal(t, DecisionRequestLimited, decision)
	assert.Contains(t, err.Error(), "request limit exceeded")
}

func TestStore_CheckModelRateLimit_ModelWithProvider_WithinLimits(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 5000, 1000, 500) // Within limits
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4", &providerStr, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when model+provider rate limits are within limits")
	assert.Equal(t, DecisionAllow, decision)
}

func TestStore_CheckModelRateLimit_BothModelAndModelProvider_ChecksBoth(t *testing.T) {
	logger := NewMockLogger()
	// Model-only rate limit (exceeded)
	rateLimit1 := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	modelConfig1 := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit1)
	// Model+provider rate limit (within limit)
	rateLimit2 := buildRateLimitWithUsage("rl2", 20000, 5000, 2000, 500)
	providerStr := "openai"
	modelConfig2 := buildModelConfig("mc2", "gpt-4", &providerStr, nil, rateLimit2)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig1, *modelConfig2},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit1, *rateLimit2},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.Error(t, err, "Should reject when model-only rate limit is exceeded")
	assert.Equal(t, DecisionTokenLimited, decision)
	assert.Contains(t, err.Error(), "token limit exceeded")
}

func TestStore_CheckModelRateLimit_BothModelAndModelProvider_ChecksBoth_RequestLimit(t *testing.T) {
	logger := NewMockLogger()
	// Model-only rate limit (request limit exceeded)
	rateLimit1 := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 1000) // Requests at max
	modelConfig1 := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit1)
	// Model+provider rate limit (within limit)
	rateLimit2 := buildRateLimitWithUsage("rl2", 20000, 5000, 2000, 500)
	providerStr := "openai"
	modelConfig2 := buildModelConfig("mc2", "gpt-4", &providerStr, nil, rateLimit2)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig1, *modelConfig2},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit1, *rateLimit2},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.Error(t, err, "Should reject when model-only rate limit (request limit) is exceeded")
	assert.Equal(t, DecisionRequestLimited, decision)
	assert.Contains(t, err.Error(), "request limit exceeded")
}

func TestStore_CheckModelRateLimit_ProviderSpecific_DifferentProvider_Passes(t *testing.T) {
	logger := NewMockLogger()
	// OpenAI GPT-4O has rate limit (exceeded)
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4o", &providerStr, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	// Request with Azure (different provider) for same model should pass
	provider := schemas.Azure
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4o", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when model config is provider-specific and different provider is used")
	assert.Equal(t, DecisionAllow, decision)
}

func TestStore_CheckModelRateLimit_ProviderSpecific_DifferentProvider_Passes_RequestLimit(t *testing.T) {
	logger := NewMockLogger()
	// OpenAI GPT-4O has rate limit (request limit exceeded)
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 1000) // Requests at max
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4o", &providerStr, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	// Request with Azure (different provider) for same model should pass
	provider := schemas.Azure
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4o", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when model config is provider-specific and different provider is used (request limit)")
	assert.Equal(t, DecisionAllow, decision)
}

// ============================================================================
// Store Tests - Update Provider Budget Usage
// ============================================================================

func TestStore_UpdateProviderBudgetUsage_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "", schemas.OpenAI, 10.0)
	assert.NoError(t, err, "Should not error when no provider config exists")
}

func TestStore_UpdateProviderBudgetUsage_UpdatesUsage(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	provider := buildProviderWithGovernance("openai", budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
		Budgets:   []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "", schemas.OpenAI, 10.0)
	assert.NoError(t, err, "Should successfully update provider budget usage")

	// Verify usage was updated
	_, err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err, "Should still be within limit after first update")

	// Update again to exceed
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "", schemas.OpenAI, 95.0)
	assert.NoError(t, err, "Should successfully update provider budget usage even when exceeding")

	// Now should be exceeded
	_, err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.Error(t, err, "Should be exceeded after second update")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// ============================================================================
// Store Tests - Update Provider Rate Limit Usage
// ============================================================================

func TestStore_UpdateProviderRateLimitUsage_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "", schemas.OpenAI, 1000, true, true)
	assert.NoError(t, err, "Should not error when no provider config exists")
}

func TestStore_UpdateProviderRateLimitUsage_UpdatesTokens(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimit("rl1", 10000, 1000)
	provider := buildProviderWithGovernance("openai", nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:  []configstoreTables.TableProvider{*provider},
		RateLimits: []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "", schemas.OpenAI, 5000, true, false)
	assert.NoError(t, err, "Should successfully update provider token usage")

	// Check that tokens were updated but requests were not
	decision, err := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.NoError(t, err, "Should still be within token limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update tokens to exceed
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "", schemas.OpenAI, 6000, true, false)
	assert.NoError(t, err, "Should successfully update provider token usage even when exceeding")

	// Now should be exceeded
	decision, err = store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.Error(t, err, "Should reject when provider token limit is exceeded after update")
	assert.Equal(t, DecisionTokenLimited, decision)
	assert.Contains(t, err.Error(), "token limit exceeded")
}

func TestStore_UpdateProviderRateLimitUsage_UpdatesRequests(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimit("rl1", 10000, 1000)
	provider := buildProviderWithGovernance("openai", nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:  []configstoreTables.TableProvider{*provider},
		RateLimits: []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	// Update requests 500 times
	for i := 0; i < 500; i++ {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "", schemas.OpenAI, 0, false, true)
		assert.NoError(t, err, "Should successfully update provider request usage")
	}

	// Should still be within limit
	decision, err := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.NoError(t, err, "Should allow when provider request limit is within limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update 500 more times to exceed
	for i := 0; i < 500; i++ {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "", schemas.OpenAI, 0, false, true)
		assert.NoError(t, err, "Should successfully update provider request usage even when exceeding")
	}

	// Now should be exceeded
	decision, err = store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.Error(t, err, "Should reject when provider request limit is exceeded after update")
	assert.Equal(t, DecisionRequestLimited, decision)
	assert.Contains(t, err.Error(), "request limit exceeded")
}

// ============================================================================
// Store Tests - Update Model Budget Usage
// ============================================================================

func TestStore_UpdateModelBudgetUsage_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "gpt-4", provider, 10.0)
	assert.NoError(t, err, "Should not error when no model config exists")
}

func TestStore_UpdateModelBudgetUsage_ModelOnly_UpdatesUsage(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "gpt-4", provider, 10.0)
	assert.NoError(t, err, "Should successfully update model budget usage")

	// Verify usage was updated
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.NoError(t, err, "Should still be within limit after first update")

	// Update again to exceed
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "gpt-4", provider, 95.0)
	assert.NoError(t, err, "Should successfully update model budget usage even when exceeding")

	// Now should be exceeded
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.Error(t, err, "Should be exceeded after second update")
	assert.Contains(t, err.Error(), "budget exceeded")
}

func TestStore_UpdateModelBudgetUsage_ModelWithProvider_UpdatesBoth(t *testing.T) {
	logger := NewMockLogger()
	// Model-only budget
	budget1 := buildBudget("budget1", 100.0, "1h")
	modelConfig1 := buildModelConfig("mc1", "gpt-4", nil, budget1, nil)
	// Model+provider budget
	budget2 := buildBudget("budget2", 200.0, "1h")
	providerStr := "openai"
	modelConfig2 := buildModelConfig("mc2", "gpt-4", &providerStr, budget2, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig1, *modelConfig2},
		Budgets:      []configstoreTables.TableBudget{*budget1, *budget2},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "gpt-4", provider, 10.0)
	assert.NoError(t, err, "Should successfully update both model-only and model+provider budget usage")

	// Both budgets should be updated
	// Check model-only budget
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.NoError(t, err, "Should still be within limit")

	// Update to exceed model-only budget
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "gpt-4", provider, 95.0)
	assert.NoError(t, err, "Should successfully update model budget usage even when exceeding")

	// Now model-only budget should be exceeded
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.Error(t, err, "Should be exceeded when model-only budget is exceeded")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// ============================================================================
// Store Tests - Update Model Rate Limit Usage
// ============================================================================

func TestStore_UpdateModelRateLimitUsage_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 1000, true, true)
	assert.NoError(t, err, "Should not error when no model config exists")
}

func TestStore_UpdateModelRateLimitUsage_ModelOnly_UpdatesUsage(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimit("rl1", 10000, 1000)
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 5000, true, false)
	assert.NoError(t, err, "Should successfully update model token usage")

	// Should still be within limit
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when model token limit is within limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update to exceed
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 6000, true, false)
	assert.NoError(t, err, "Should successfully update model token usage even when exceeding")

	// Now should be exceeded
	decision, err = store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.Error(t, err, "Should reject when model token limit is exceeded after update")
	assert.Equal(t, DecisionTokenLimited, decision)
	assert.Contains(t, err.Error(), "token limit exceeded")
}

func TestStore_UpdateModelRateLimitUsage_ModelWithProvider_UpdatesUsage(t *testing.T) {
	logger := NewMockLogger()
	// Model-only rate limit
	rateLimit1 := buildRateLimit("rl1", 10000, 1000)
	modelConfig1 := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit1)
	// Model+provider rate limit
	rateLimit2 := buildRateLimit("rl2", 20000, 2000)
	providerStr := "openai"
	modelConfig2 := buildModelConfig("mc2", "gpt-4", &providerStr, nil, rateLimit2)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig1, *modelConfig2},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit1, *rateLimit2},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 5000, true, false)
	assert.NoError(t, err, "Should successfully update both model-only and model+provider token usage")

	// Should still be within limit
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when both rate limits are within limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update to exceed model-only rate limit (should fail at model-only level)
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 6000, true, false)
	assert.NoError(t, err, "Should successfully update model token usage even when exceeding")

	// Now should be exceeded (model-only rate limit exceeded)
	decision, err = store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.Error(t, err, "Should reject when model-only token limit is exceeded after update")
	assert.Equal(t, DecisionTokenLimited, decision)
	assert.Contains(t, err.Error(), "token limit exceeded")
}

func TestStore_UpdateModelRateLimitUsage_ModelOnly_UpdatesUsage_RequestLimit(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimit("rl1", 10000, 1000)
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	// Update requests 500 times
	for range 500 {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 0, false, true)
		assert.NoError(t, err, "Should successfully update model request usage")
	}

	// Should still be within limit
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when model request limit is within limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update 500 more times to exceed
	for range 500 {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 0, false, true)
		assert.NoError(t, err, "Should successfully update model request usage even when exceeding")
	}

	// Now should be exceeded
	decision, err = store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.Error(t, err, "Should reject when model request limit is exceeded after update")
	assert.Equal(t, DecisionRequestLimited, decision)
	assert.Contains(t, err.Error(), "request limit exceeded")
}

func TestStore_UpdateModelRateLimitUsage_ModelWithProvider_UpdatesUsage_RequestLimit(t *testing.T) {
	logger := NewMockLogger()
	// Model-only rate limit
	rateLimit1 := buildRateLimit("rl1", 10000, 1000)
	modelConfig1 := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit1)
	// Model+provider rate limit
	rateLimit2 := buildRateLimit("rl2", 20000, 2000)
	providerStr := "openai"
	modelConfig2 := buildModelConfig("mc2", "gpt-4", &providerStr, nil, rateLimit2)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig1, *modelConfig2},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit1, *rateLimit2},
	}, nil)
	require.NoError(t, err)

	provider := schemas.OpenAI
	// Update requests 500 times (should update both model-only and model+provider)
	for range 500 {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 0, false, true)
		assert.NoError(t, err, "Should successfully update both model-only and model+provider request usage")
	}

	// Should still be within limit
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when both rate limits are within limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update 500 more times to exceed model-only rate limit
	for range 500 {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 0, false, true)
		assert.NoError(t, err, "Should successfully update model request usage even when exceeding")
	}

	// Now should be exceeded (model-only rate limit exceeded)
	decision, err = store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.Error(t, err, "Should reject when model-only request limit is exceeded after update")
	assert.Equal(t, DecisionRequestLimited, decision)
	assert.Contains(t, err.Error(), "request limit exceeded")
}

// ============================================================================
// Resolver Tests - EvaluateModelAndProviderRequest
// ============================================================================

func TestResolver_EvaluateModelAndProviderRequest_NoConfigs(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.OpenAI, "gpt-4")
	assertDecision(t, DecisionAllow, result)
}

func TestResolver_EvaluateModelAndProviderRequest_ProviderBudgetExceeded(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	provider := buildProviderWithGovernance("openai", budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
		Budgets:   []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.OpenAI, "gpt-4")
	assertDecision(t, DecisionBudgetExceeded, result)
	assert.Contains(t, result.Reason, "Provider-level budget exceeded")
}

func TestResolver_EvaluateModelAndProviderRequest_ProviderRateLimitExceeded(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	provider := buildProviderWithGovernance("openai", nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:  []configstoreTables.TableProvider{*provider},
		RateLimits: []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.OpenAI, "gpt-4")
	assertDecision(t, DecisionTokenLimited, result)
	assert.Contains(t, result.Reason, "Provider-level rate limit check failed")
}

func TestResolver_EvaluateModelAndProviderRequest_ModelBudgetExceeded(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.OpenAI, "gpt-4")
	assertDecision(t, DecisionBudgetExceeded, result)
	assert.Contains(t, result.Reason, "Model-level budget exceeded")
}

func TestResolver_EvaluateModelAndProviderRequest_ModelRateLimitExceeded(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.OpenAI, "gpt-4")
	assertDecision(t, DecisionTokenLimited, result)
	assert.Contains(t, result.Reason, "Model-level rate limit check failed")
}

func TestResolver_EvaluateModelAndProviderRequest_ModelRateLimitExceeded_RequestLimit(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 1000) // Requests at max
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.OpenAI, "gpt-4")
	assertDecision(t, DecisionRequestLimited, result)
	assert.Contains(t, result.Reason, "Model-level rate limit check failed")
}

func TestResolver_EvaluateModelAndProviderRequest_ProviderBudgetThenModelBudget(t *testing.T) {
	logger := NewMockLogger()
	// Provider budget exceeded
	providerBudget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h")
	provider := buildProviderWithGovernance("openai", providerBudget, nil)
	// Model budget within limit
	modelBudget := buildBudget("budget2", 200.0, "1h")
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, modelBudget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:    []configstoreTables.TableProvider{*provider},
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*providerBudget, *modelBudget},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.OpenAI, "gpt-4")
	// Should fail at provider level (checked first)
	assertDecision(t, DecisionBudgetExceeded, result)
	assert.Contains(t, result.Reason, "Provider-level budget exceeded")
}

func TestResolver_EvaluateModelAndProviderRequest_ProviderRateLimitThenModelRateLimit(t *testing.T) {
	logger := NewMockLogger()
	// Provider rate limit exceeded
	providerRateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	provider := buildProviderWithGovernance("openai", nil, providerRateLimit)
	// Model rate limit within limit
	modelRateLimit := buildRateLimit("rl2", 20000, 2000)
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, nil, modelRateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:    []configstoreTables.TableProvider{*provider},
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*providerRateLimit, *modelRateLimit},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.OpenAI, "gpt-4")
	// Should fail at provider level (checked first)
	assertDecision(t, DecisionTokenLimited, result)
	assert.Contains(t, result.Reason, "Provider-level rate limit check failed")
}

func TestResolver_EvaluateModelAndProviderRequest_ProviderRateLimitThenModelRateLimit_RequestLimit(t *testing.T) {
	logger := NewMockLogger()
	// Provider rate limit exceeded (request limit)
	providerRateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 1000) // Requests at max
	provider := buildProviderWithGovernance("openai", nil, providerRateLimit)
	// Model rate limit within limit
	modelRateLimit := buildRateLimit("rl2", 20000, 2000)
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, nil, modelRateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:    []configstoreTables.TableProvider{*provider},
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*providerRateLimit, *modelRateLimit},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.OpenAI, "gpt-4")
	// Should fail at provider level (checked first)
	assertDecision(t, DecisionRequestLimited, result)
	assert.Contains(t, result.Reason, "Provider-level rate limit check failed")
}

func TestResolver_EvaluateModelAndProviderRequest_AllChecksPass(t *testing.T) {
	logger := NewMockLogger()
	// Provider budget and rate limit within limits
	providerBudget := buildBudget("budget1", 100.0, "1h")
	providerRateLimit := buildRateLimit("rl1", 10000, 1000)
	provider := buildProviderWithGovernance("openai", providerBudget, providerRateLimit)
	// Model budget and rate limit within limits
	modelBudget := buildBudget("budget2", 200.0, "1h")
	modelRateLimit := buildRateLimit("rl2", 20000, 2000)
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, modelBudget, modelRateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:    []configstoreTables.TableProvider{*provider},
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*providerBudget, *modelBudget},
		RateLimits:   []configstoreTables.TableRateLimit{*providerRateLimit, *modelRateLimit},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.OpenAI, "gpt-4")
	assertDecision(t, DecisionAllow, result)
	assert.Contains(t, result.Reason, "provider-level and model-level checks passed")
}

func TestResolver_EvaluateModelAndProviderRequest_ProviderOnly_NoModel(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	provider := buildProviderWithGovernance("openai", budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
		Budgets:   []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// No model provided
	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.OpenAI, "")
	assertDecision(t, DecisionAllow, result)
}

func TestResolver_EvaluateModelAndProviderRequest_ModelOnly_NoProvider(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// No provider provided
	result := resolver.EvaluateModelAndProviderRequest(ctx, "", "gpt-4")
	assertDecision(t, DecisionAllow, result)
}

func TestResolver_EvaluateModelAndProviderRequest_ProviderSpecificBudget_DifferentProvider_Passes(t *testing.T) {
	logger := NewMockLogger()
	// OpenAI GPT-4O has budget (exceeded)
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4o", &providerStr, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Request with Azure (different provider) for same model should pass
	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.Azure, "gpt-4o")
	assertDecision(t, DecisionAllow, result)
}

func TestResolver_EvaluateModelAndProviderRequest_ProviderSpecificRateLimit_DifferentProvider_Passes(t *testing.T) {
	logger := NewMockLogger()
	// OpenAI GPT-4O has rate limit (exceeded)
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4o", &providerStr, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Request with Azure (different provider) for same model should pass
	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.Azure, "gpt-4o")
	assertDecision(t, DecisionAllow, result)
}

func TestResolver_EvaluateModelAndProviderRequest_ProviderSpecificRateLimit_DifferentProvider_Passes_RequestLimit(t *testing.T) {
	logger := NewMockLogger()
	// OpenAI GPT-4O has rate limit (request limit exceeded)
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 1000) // Requests at max
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4o", &providerStr, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Request with Azure (different provider) for same model should pass
	result := resolver.EvaluateModelAndProviderRequest(ctx, schemas.Azure, "gpt-4o")
	assertDecision(t, DecisionAllow, result)
}

// ============================================================================
// End-to-End Tests - PreLLMHook Integration
// ============================================================================

func TestPreLLMHook_ProviderBudgetExceeded_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	provider := buildProviderWithGovernance("openai", budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
		Budgets:   []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when provider budget is exceeded")
	assert.Contains(t, shortCircuit.Error.Error.Message, "budget exceeded")
}

func TestPreLLMHook_ProviderRateLimitExceeded_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	provider := buildProviderWithGovernance("openai", nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:  []configstoreTables.TableProvider{*provider},
		RateLimits: []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when provider rate limit is exceeded")
	assert.Contains(t, shortCircuit.Error.Error.Message, "rate limit")
}

func TestPreLLMHook_ModelBudgetExceeded_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when model budget is exceeded")
	assert.Contains(t, shortCircuit.Error.Error.Message, "budget exceeded")
}

func TestPreLLMHook_ModelRateLimitExceeded_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when model rate limit is exceeded")
	assert.Contains(t, shortCircuit.Error.Error.Message, "rate limit")
}

func TestPreLLMHook_ModelRateLimitExceeded_NoVirtualKey_RequestLimit(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 1000) // Requests at max
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when model rate limit (request limit) is exceeded")
	assert.Contains(t, shortCircuit.Error.Error.Message, "rate limit")
}

func TestPreLLMHook_AllChecksPass_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	// Provider budget and rate limit within limits
	providerBudget := buildBudget("budget1", 100.0, "1h")
	providerRateLimit := buildRateLimit("rl1", 10000, 1000)
	provider := buildProviderWithGovernance("openai", providerBudget, providerRateLimit)
	// Model budget and rate limit within limits
	modelBudget := buildBudget("budget2", 200.0, "1h")
	modelRateLimit := buildRateLimit("rl2", 20000, 2000)
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, modelBudget, modelRateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:    []configstoreTables.TableProvider{*provider},
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*providerBudget, *modelBudget},
		RateLimits:   []configstoreTables.TableRateLimit{*providerRateLimit, *modelRateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	result, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.Nil(t, shortCircuit, "Should not short circuit when all checks pass")
	assert.NotNil(t, result)
}

func TestPreLLMHook_ProviderBudgetThenModelBudget_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	// Provider budget exceeded
	providerBudget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h")
	provider := buildProviderWithGovernance("openai", providerBudget, nil)
	// Model budget within limit
	modelBudget := buildBudget("budget2", 200.0, "1h")
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, modelBudget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:    []configstoreTables.TableProvider{*provider},
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*providerBudget, *modelBudget},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	// Should fail at provider level (checked first)
	assert.NotNil(t, shortCircuit, "Should short circuit when provider budget is exceeded")
	assert.Contains(t, shortCircuit.Error.Error.Message, "budget exceeded")
}

func TestPreLLMHook_ProviderSpecificModelBudget_DifferentProvider_Passes_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	// OpenAI GPT-4O has budget (exceeded)
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4o", &providerStr, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.Azure, // Different provider
			Model:    "gpt-4o",      // Same model
		},
	}

	result, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.Nil(t, shortCircuit, "Should not short circuit when model config is provider-specific and different provider is used")
	assert.NotNil(t, result)
}

func TestPreLLMHook_ProviderSpecificModelRateLimit_DifferentProvider_Passes_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	// OpenAI GPT-4O has rate limit (exceeded)
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4o", &providerStr, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.Azure, // Different provider
			Model:    "gpt-4o",      // Same model
		},
	}

	result, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.Nil(t, shortCircuit, "Should not short circuit when model config is provider-specific and different provider is used")
	assert.NotNil(t, result)
}

func TestPreLLMHook_ProviderSpecificModelRateLimit_DifferentProvider_Passes_NoVirtualKey_RequestLimit(t *testing.T) {
	logger := NewMockLogger()
	// OpenAI GPT-4O has rate limit (request limit exceeded)
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 1000) // Requests at max
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4o", &providerStr, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.Azure, // Different provider
			Model:    "gpt-4o",      // Same model
		},
	}

	result, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.Nil(t, shortCircuit, "Should not short circuit when model config is provider-specific and different provider is used (request limit)")
	assert.NotNil(t, result)
}

// ============================================================================
// End-to-End Tests - PreLLMHook Integration with Virtual Key Fallback
// ============================================================================

func TestPreLLMHook_ModelProviderPass_VirtualKeyBudgetExceeded(t *testing.T) {
	logger := NewMockLogger()
	// Model/provider checks pass (no limits)
	// Virtual key budget exceeded
	vkBudget := buildBudgetWithUsage("vk-budget1", 100.0, 100.1, "1h") // Over limit
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", vkBudget)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*vkBudget},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	parentCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	parentCtx = context.WithValue(parentCtx, schemas.BifrostContextKeyRequestID, "req-1")
	ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when model/provider pass but VK budget is exceeded")
	assert.Contains(t, shortCircuit.Error.Error.Message, "budget exceeded")
}

func TestPreLLMHook_ModelProviderPass_VirtualKeyRateLimitExceeded_Token(t *testing.T) {
	logger := NewMockLogger()
	// Model/provider checks pass (no limits)
	// Virtual key rate limit exceeded (token)
	vkRateLimit := buildRateLimitWithUsage("vk-rl1", 10000, 10000, 1000, 0) // Tokens at max
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", vkRateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*vkRateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	parentCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	parentCtx = context.WithValue(parentCtx, schemas.BifrostContextKeyRequestID, "req-1")
	ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when model/provider pass but VK token rate limit is exceeded")
	assert.Contains(t, shortCircuit.Error.Error.Message, "rate limit")
}

func TestPreLLMHook_ModelProviderPass_VirtualKeyRateLimitExceeded_Request(t *testing.T) {
	logger := NewMockLogger()
	// Model/provider checks pass (no limits)
	// Virtual key rate limit exceeded (request)
	vkRateLimit := buildRateLimitWithUsage("vk-rl1", 10000, 0, 1000, 1000) // Requests at max
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", vkRateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*vkRateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	parentCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	parentCtx = context.WithValue(parentCtx, schemas.BifrostContextKeyRequestID, "req-1")
	ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when model/provider pass but VK request rate limit is exceeded")
	assert.Contains(t, shortCircuit.Error.Error.Message, "rate limit")
}

func TestPreLLMHook_ModelProviderPass_VirtualKeyChecksPass(t *testing.T) {
	logger := NewMockLogger()
	// Model/provider checks pass (no limits)
	// Virtual key checks also pass
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	parentCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	parentCtx = context.WithValue(parentCtx, schemas.BifrostContextKeyRequestID, "req-1")
	ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	result, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.Nil(t, shortCircuit, "Should not short circuit when both model/provider and VK checks pass")
	assert.NotNil(t, result)
}

// TestPreLLMHook_SkipKeySelection verifies SkipKeySelection (Claude Code OAuth/max mode) does not
// bypass governance: the request is governed like any other. With a VK it is fully evaluated;
// keyless requests follow IsVkMandatory. The skip flag never changes the governance outcome — the
// OAuth token passthrough is handled independently in core key selection.
func TestPreLLMHook_SkipKeySelection(t *testing.T) {
	logger := NewMockLogger()
	// Valid VK whose budget is already exceeded, so enforcement is observable when it runs.
	vkBudget := buildBudgetWithUsage("vk-budget1", 100.0, 100.1, "1h")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", vkBudget)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*vkBudget},
	}, nil)
	require.NoError(t, err)

	tests := []struct {
		name               string
		skipKeySelection   bool
		virtualKey         string
		isVkMandatory      bool
		expectShortCircuit bool
		msgContains        string
	}{
		{name: "skip + no VK rejected when VK mandatory", skipKeySelection: true, virtualKey: "", isVkMandatory: true, expectShortCircuit: true, msgContains: "virtual key is required"},
		{name: "skip + no VK allowed when VK not mandatory", skipKeySelection: true, virtualKey: "", isVkMandatory: false, expectShortCircuit: false},
		{name: "skip + valid VK is enforced (budget exceeded)", skipKeySelection: true, virtualKey: "sk-bf-test", isVkMandatory: false, expectShortCircuit: true, msgContains: "budget exceeded"},
		{name: "skip + unknown VK is rejected", skipKeySelection: true, virtualKey: "sk-bf-unknown", isVkMandatory: false, expectShortCircuit: true, msgContains: "not found"},
		{name: "no skip + valid VK enforced identically", skipKeySelection: false, virtualKey: "sk-bf-test", isVkMandatory: false, expectShortCircuit: true, msgContains: "budget exceeded"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(tc.isVkMandatory)}, logger, store, nil, nil, nil, nil)
			require.NoError(t, err)

			parentCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "req-1")
			if tc.virtualKey != "" {
				parentCtx = context.WithValue(parentCtx, schemas.BifrostContextKeyVirtualKey, tc.virtualKey)
			}
			ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
			if tc.skipKeySelection {
				ctx.SetValue(schemas.BifrostContextKeySkipKeySelection, true)
			}
			req := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4",
				},
			}

			result, shortCircuit, err := plugin.PreLLMHook(ctx, req)
			assert.NoError(t, err)
			if tc.expectShortCircuit {
				require.NotNil(t, shortCircuit, "expected governance to short-circuit")
				assert.Contains(t, shortCircuit.Error.Error.Message, tc.msgContains)
			} else {
				assert.Nil(t, shortCircuit, "expected request to be allowed")
				assert.NotNil(t, result)
			}
		})
	}
}

// TestPreLLMHook_RequiredHeaders_SkipKeySelection covers required-headers enforcement on
// SkipKeySelection (OAuth/max-mode) requests. validateRequiredHeaders now runs unconditionally as
// the first check in PreLLMHook, so an OAuth request missing a configured required header must
// short-circuit — a path that was unreachable while SkipKeySelection bypassed governance.
func TestPreLLMHook_RequiredHeaders_SkipKeySelection(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{
		IsVkMandatory:   boolPtr(false),
		RequiredHeaders: &[]string{"x-required-header"},
	}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	tests := []struct {
		name               string
		headers            map[string]string
		expectShortCircuit bool
	}{
		{name: "skip request missing required header is rejected", headers: nil, expectShortCircuit: true},
		{name: "skip request with required header passes the check", headers: map[string]string{"x-required-header": "present"}, expectShortCircuit: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parentCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "req-1")
			ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeySkipKeySelection, true)
			if tc.headers != nil {
				ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, tc.headers)
			}
			req := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4",
				},
			}

			result, shortCircuit, err := plugin.PreLLMHook(ctx, req)
			assert.NoError(t, err)
			if tc.expectShortCircuit {
				require.NotNil(t, shortCircuit, "expected short-circuit on missing required header")
				assert.Contains(t, shortCircuit.Error.Error.Message, "missing required headers")
			} else {
				assert.Nil(t, shortCircuit, "expected required-headers check to pass")
				assert.NotNil(t, result)
			}
		})
	}
}

func TestPreLLMHook_ModelProviderPass_VirtualKeyNotFound(t *testing.T) {
	logger := NewMockLogger()
	// Model/provider checks pass (no limits)
	// Virtual key not found
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	parentCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyVirtualKey, "sk-bf-nonexistent")
	parentCtx = context.WithValue(parentCtx, schemas.BifrostContextKeyRequestID, "req-1")
	ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when model/provider pass but VK is not found")
	assert.Contains(t, shortCircuit.Error.Error.Message, "not found")
}

func TestPreLLMHook_ModelProviderPass_VirtualKeyBlocked(t *testing.T) {
	logger := NewMockLogger()
	// Model/provider checks pass (no limits)
	// Virtual key is inactive
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", false) // Inactive
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	parentCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	parentCtx = context.WithValue(parentCtx, schemas.BifrostContextKeyRequestID, "req-1")
	ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when model/provider pass but VK is inactive")
	assert.Contains(t, shortCircuit.Error.Error.Message, "inactive")
}

func TestPreLLMHook_ModelProviderPass_VirtualKeyProviderBlocked(t *testing.T) {
	logger := NewMockLogger()
	// Model/provider checks pass (no limits)
	// Virtual key blocks OpenAI provider
	providerConfigs := []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("anthropic", []string{"claude-3-sonnet"}), // Only Anthropic allowed
	}
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", providerConfigs)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	parentCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	parentCtx = context.WithValue(parentCtx, schemas.BifrostContextKeyRequestID, "req-1")
	ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI, // Not allowed by VK
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when model/provider pass but VK blocks provider")
	assert.Contains(t, shortCircuit.Error.Error.Message, "not allowed")
}

func TestPreLLMHook_ModelProviderPass_VirtualKeyModelBlocked(t *testing.T) {
	logger := NewMockLogger()
	// Model/provider checks pass (no limits)
	// Virtual key blocks specific model
	providerConfigs := []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"gpt-4", "gpt-4-turbo"}), // Only these models allowed
	}
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", providerConfigs)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	parentCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	parentCtx = context.WithValue(parentCtx, schemas.BifrostContextKeyRequestID, "req-1")
	ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-3.5-turbo", // Not in allowed list
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when model/provider pass but VK blocks model")
	assert.Contains(t, shortCircuit.Error.Error.Message, "not allowed")
}

func TestPreLLMHook_ModelProviderPass_VirtualKeyBudgetExceeded_WithModelProviderLimits(t *testing.T) {
	logger := NewMockLogger()
	// Model/provider checks pass (within limits)
	providerBudget := buildBudget("provider-budget1", 200.0, "1h")
	provider := buildProviderWithGovernance("openai", providerBudget, nil)
	modelBudget := buildBudget("model-budget1", 150.0, "1h")
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, modelBudget, nil)
	// Virtual key budget exceeded
	vkBudget := buildBudgetWithUsage("vk-budget1", 100.0, 100.1, "1h") // Over limit
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", vkBudget)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:    []configstoreTables.TableProvider{*provider},
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		Budgets:      []configstoreTables.TableBudget{*providerBudget, *modelBudget, *vkBudget},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	parentCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	parentCtx = context.WithValue(parentCtx, schemas.BifrostContextKeyRequestID, "req-1")
	ctx := schemas.NewBifrostContext(parentCtx, schemas.NoDeadline)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit, _ := plugin.PreLLMHook(ctx, req)
	assert.NotNil(t, shortCircuit, "Should short circuit when model/provider pass but VK budget is exceeded")
	assert.Contains(t, shortCircuit.Error.Error.Message, "budget exceeded")
}

// ============================================================================
// End-to-End Tests - PostHook Integration (Usage Tracking)
// ============================================================================

func TestPostHook_UpdatesProviderBudgetUsage_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	// Set budget with initial usage close to limit to test the flow
	// Note: Without model catalog, cost will be 0, so we test the flow even if budget isn't actually updated
	budget := buildBudgetWithUsage("budget1", 100.0, 50.0, "1h")
	provider := buildProviderWithGovernance("openai", budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
		Budgets:   []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	// First request: PreLLMHook should pass, PostHook updates usage
	parentCtx1 := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "req-1")
	ctx1 := schemas.NewBifrostContext(parentCtx1, schemas.NoDeadline)
	req1 := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit1, _ := plugin.PreLLMHook(ctx1, req1)
	assert.Nil(t, shortCircuit1, "First request should pass PreLLMHook")

	result1 := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: "gpt-4",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4",
			},
		},
	}

	_, _, err = plugin.PostLLMHook(ctx1, result1, nil)
	assert.NoError(t, err, "Should successfully process PostHook for provider budget usage update")

	// Wait for async processing to complete
	time.Sleep(200 * time.Millisecond)

	// Second request: Verify the flow works (budget check should still pass since cost is 0 without model catalog)
	parentCtx2 := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "req-2")
	ctx2 := schemas.NewBifrostContext(parentCtx2, schemas.NoDeadline)
	req2 := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit2, _ := plugin.PreLLMHook(ctx2, req2)
	// Without model catalog, cost is 0, so budget won't be exceeded
	// This test verifies the PostHook -> PreLLMHook flow works correctly
	assert.Nil(t, shortCircuit2, "Second request should pass PreLLMHook (cost is 0 without model catalog)")
}

func TestPostHook_UpdatesProviderRateLimitUsage_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	// Set rate limit: 10000 tokens, 1000 requests
	// First request: 10000 tokens, 1 request (brings usage to exactly the limit)
	// Second request: Should fail because we're already at the limit
	rateLimit := buildRateLimit("rl1", 10000, 1000)
	provider := buildProviderWithGovernance("openai", nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers:  []configstoreTables.TableProvider{*provider},
		RateLimits: []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	// First request: PreLLMHook should pass, PostHook updates usage to 10000
	parentCtx1 := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "req-1")
	ctx1 := schemas.NewBifrostContext(parentCtx1, schemas.NoDeadline)
	req1 := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit1, _ := plugin.PreLLMHook(ctx1, req1)
	assert.Nil(t, shortCircuit1, "First request should pass PreLLMHook")

	result1 := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: "gpt-4",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     6000,
				CompletionTokens: 4000,
				TotalTokens:      10000, // 10000 tokens used (exactly at limit)
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4",
			},
		},
	}

	_, _, err = plugin.PostLLMHook(ctx1, result1, nil)
	assert.NoError(t, err, "Should successfully process PostHook for provider rate limit usage update")

	// Wait for async processing to complete
	time.Sleep(200 * time.Millisecond)

	// Second request: Should fail because we're already at the token limit (10000/10000)
	parentCtx2 := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "req-2")
	ctx2 := schemas.NewBifrostContext(parentCtx2, schemas.NoDeadline)
	req2 := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit2, _ := plugin.PreLLMHook(ctx2, req2)
	assert.NotNil(t, shortCircuit2, "Second request should fail PreLLMHook due to token limit exceeded")
	assert.Contains(t, shortCircuit2.Error.Error.Message, "token limit exceeded", "Error should indicate token limit exceeded")
}

// TestPostHook_TracksVirtualKeyUsageWhenUserIDPresent verifies user attribution
// does not suppress VK usage tracking.
func TestPostHook_TracksVirtualKeyUsageWhenUserIDPresent(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("vk-rl", 10000, 0, 1000, 0)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	defer plugin.Cleanup()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	ctx.SetValue(schemas.BifrostContextKeyUserID, "user1")
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: "gpt-4",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     600,
				CompletionTokens: 400,
				TotalTokens:      1000,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4",
			},
		},
	}

	_, _, err = plugin.PostLLMHook(ctx, result, nil)
	require.NoError(t, err)
	plugin.wg.Wait()

	updated, exists := store.GetGovernanceData(context.Background()).RateLimits["vk-rl"]
	require.True(t, exists)
	assert.Equal(t, int64(1000), updated.TokenCurrentUsage)
	assert.Equal(t, int64(1), updated.RequestCurrentUsage)
}

// TestPostHook_SkipVirtualKeyUsageTrackingFlag verifies callers can explicitly
// suppress VK usage while keeping VK auth and user attribution on the context.
func TestPostHook_SkipVirtualKeyUsageTrackingFlag(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("vk-rl", 10000, 0, 1000, 0)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	defer plugin.Cleanup()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	ctx.SetValue(schemas.BifrostContextKeyUserID, "user1")
	ctx.SetValue(schemas.BifrostContextKeySkipVirtualKeyUsageTracking, true)
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: "gpt-4",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     600,
				CompletionTokens: 400,
				TotalTokens:      1000,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4",
			},
		},
	}

	_, _, err = plugin.PostLLMHook(ctx, result, nil)
	require.NoError(t, err)
	plugin.wg.Wait()

	updated, exists := store.GetGovernanceData(context.Background()).RateLimits["vk-rl"]
	require.True(t, exists)
	assert.Equal(t, int64(0), updated.TokenCurrentUsage)
	assert.Equal(t, int64(0), updated.RequestCurrentUsage)
	assert.Equal(t, "user1", bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID))
}

// TestPostMCPHook_TracksVirtualKeyUsageWhenUserIDPresent verifies user
// attribution does not suppress MCP VK request usage tracking.
func TestPostMCPHook_TracksVirtualKeyUsageWhenUserIDPresent(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("vk-rl", 10000, 0, 1000, 0)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	defer plugin.Cleanup()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	ctx.SetValue(schemas.BifrostContextKeyUserID, "user1")
	resp := &schemas.BifrostMCPResponse{
		ExtraFields: schemas.BifrostMCPResponseExtraFields{
			MCPRequestType: schemas.MCPRequestTypeExecuteTool,
			ClientName:     "client",
			ToolName:       "tool",
		},
	}

	_, _, err = plugin.PostMCPHook(ctx, resp, nil)
	require.NoError(t, err)
	plugin.wg.Wait()

	updated, exists := store.GetGovernanceData(context.Background()).RateLimits["vk-rl"]
	require.True(t, exists)
	assert.Equal(t, int64(0), updated.TokenCurrentUsage)
	assert.Equal(t, int64(1), updated.RequestCurrentUsage)
}

// TestPostMCPHook_SkipVirtualKeyUsageTrackingFlag verifies MCP callers can
// explicitly suppress VK usage while preserving user attribution.
func TestPostMCPHook_SkipVirtualKeyUsageTrackingFlag(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("vk-rl", 10000, 0, 1000, 0)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	defer plugin.Cleanup()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	ctx.SetValue(schemas.BifrostContextKeyUserID, "user1")
	ctx.SetValue(schemas.BifrostContextKeySkipVirtualKeyUsageTracking, true)
	resp := &schemas.BifrostMCPResponse{
		ExtraFields: schemas.BifrostMCPResponseExtraFields{
			MCPRequestType: schemas.MCPRequestTypeExecuteTool,
			ClientName:     "client",
			ToolName:       "tool",
		},
	}

	_, _, err = plugin.PostMCPHook(ctx, resp, nil)
	require.NoError(t, err)
	plugin.wg.Wait()

	updated, exists := store.GetGovernanceData(context.Background()).RateLimits["vk-rl"]
	require.True(t, exists)
	assert.Equal(t, int64(0), updated.TokenCurrentUsage)
	assert.Equal(t, int64(0), updated.RequestCurrentUsage)
	assert.Equal(t, "user1", bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID))
}

func TestPostHook_UpdatesModelBudgetUsage_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	// Set budget with initial usage close to limit to test the flow
	// Note: Without model catalog, cost will be 0, so we test the flow even if budget isn't actually updated
	budget := buildBudgetWithUsage("budget1", 100.0, 50.0, "1h")
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	// First request: PreLLMHook should pass, PostHook updates usage
	parentCtx1 := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "req-1")
	ctx1 := schemas.NewBifrostContext(parentCtx1, schemas.NoDeadline)
	req1 := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit1, _ := plugin.PreLLMHook(ctx1, req1)
	assert.Nil(t, shortCircuit1, "First request should pass PreLLMHook")

	result1 := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: "gpt-4",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4",
			},
		},
	}

	_, _, err = plugin.PostLLMHook(ctx1, result1, nil)
	assert.NoError(t, err, "Should successfully process PostHook for model budget usage update")

	// Wait for async processing to complete
	time.Sleep(200 * time.Millisecond)

	// Second request: Verify the flow works (budget check should still pass since cost is 0 without model catalog)
	parentCtx2 := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "req-2")
	ctx2 := schemas.NewBifrostContext(parentCtx2, schemas.NoDeadline)
	req2 := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit2, _ := plugin.PreLLMHook(ctx2, req2)
	// Without model catalog, cost is 0, so budget won't be exceeded
	// This test verifies the PostHook -> PreLLMHook flow works correctly
	assert.Nil(t, shortCircuit2, "Second request should pass PreLLMHook (cost is 0 without model catalog)")
}

func TestPostHook_UpdatesModelRateLimitUsage_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	// Set rate limit: 10000 tokens, 1000 requests
	// First request: 10000 tokens, 1 request (brings usage to exactly the limit)
	// Second request: Should fail because we're already at the limit
	rateLimit := buildRateLimit("rl1", 10000, 1000)
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)

	// First request: PreLLMHook should pass, PostHook updates usage to 10000
	parentCtx1 := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "req-1")
	ctx1 := schemas.NewBifrostContext(parentCtx1, schemas.NoDeadline)
	req1 := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit1, _ := plugin.PreLLMHook(ctx1, req1)
	assert.Nil(t, shortCircuit1, "First request should pass PreLLMHook")

	result1 := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: "gpt-4",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     6000,
				CompletionTokens: 4000,
				TotalTokens:      10000, // 10000 tokens used (exactly at limit)
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4",
			},
		},
	}

	_, _, err = plugin.PostLLMHook(ctx1, result1, nil)
	assert.NoError(t, err, "Should successfully process PostHook for model rate limit usage update")

	// Wait for async processing to complete
	time.Sleep(200 * time.Millisecond)

	// Second request: Should fail because we're already at the token limit (10000/10000)
	parentCtx2 := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "req-2")
	ctx2 := schemas.NewBifrostContext(parentCtx2, schemas.NoDeadline)
	req2 := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
		},
	}

	_, shortCircuit2, _ := plugin.PreLLMHook(ctx2, req2)
	assert.NotNil(t, shortCircuit2, "Second request should fail PreLLMHook due to token limit exceeded")
	assert.Contains(t, shortCircuit2.Error.Error.Message, "token limit exceeded", "Error should indicate token limit exceeded")
}

// ============================================================================
// Cross-Provider Model Matching Tests
// ============================================================================

// TestStore_CheckModelBudget_CrossProviderModelMatch tests that a model-only config
// for "gpt-4o" is matched when the request uses "openai/gpt-4o" (OpenRouter-style prefix).
func TestStore_CheckModelBudget_CrossProviderModelMatch(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	modelConfig := buildModelConfig("mc1", "gpt-4o", nil, budget, nil)

	mc := newTestModelCatalog(t)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, mc)
	require.NoError(t, err)

	// Request with provider-prefixed model name should match the "gpt-4o" config
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "openai/gpt-4o", Provider: schemas.OpenRouter}, nil)
	assert.Error(t, err, "Should reject: openai/gpt-4o should match model-only config for gpt-4o")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// TestStore_CheckModelBudget_CrossProviderModelMatch_WithinLimit tests that the match works
// and correctly allows requests within the budget.
func TestStore_CheckModelBudget_CrossProviderModelMatch_WithinLimit(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	modelConfig := buildModelConfig("mc1", "gpt-4o", nil, budget, nil)

	mc := newTestModelCatalog(t)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, mc)
	require.NoError(t, err)

	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "openai/gpt-4o", Provider: schemas.OpenRouter}, nil)
	assert.NoError(t, err, "Should allow: budget is within limit")
}

// TestStore_CheckModelRateLimit_CrossProviderModelMatch tests that a model-only rate limit config
// for "gpt-4o" is matched when the request uses "openai/gpt-4o".
func TestStore_CheckModelRateLimit_CrossProviderModelMatch(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Token limit at max
	modelConfig := buildModelConfig("mc1", "gpt-4o", nil, nil, rateLimit)

	mc := newTestModelCatalog(t)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, mc)
	require.NoError(t, err)

	decision, errResult := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "openai/gpt-4o", Provider: schemas.OpenRouter}, nil, nil)
	assert.Error(t, errResult, "Should reject: openai/gpt-4o should match model-only rate limit for gpt-4o")
	assert.Contains(t, errResult.Error(), "token limit exceeded")
	assert.NotEqual(t, DecisionAllow, decision)
}

// TestStore_UpdateModelBudgetUsage_CrossProviderModelMatch tests that usage for "openai/gpt-4o"
// is correctly attributed to the model-only config for "gpt-4o".
func TestStore_UpdateModelBudgetUsage_CrossProviderModelMatch(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	modelConfig := buildModelConfig("mc1", "gpt-4o", nil, budget, nil)

	mc := newTestModelCatalog(t)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, mc)
	require.NoError(t, err)

	// Update usage with prefixed model name
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "openai/gpt-4o", schemas.OpenRouter, 50.0)
	assert.NoError(t, err, "Should successfully update budget usage via cross-provider match")

	// Now exceed the budget
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "openai/gpt-4o", schemas.OpenRouter, 55.0)
	assert.NoError(t, err)

	// Budget should now be exceeded
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "openai/gpt-4o", Provider: schemas.OpenRouter}, nil)
	assert.Error(t, err, "Budget should be exceeded after usage updates via cross-provider match")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// TestStore_UpdateModelRateLimitUsage_CrossProviderModelMatch tests that rate limit usage
// for "openai/gpt-4o" is correctly attributed to the model-only config for "gpt-4o".
func TestStore_UpdateModelRateLimitUsage_CrossProviderModelMatch(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 100, 0, 1000, 0) // Low token limit
	modelConfig := buildModelConfig("mc1", "gpt-4o", nil, nil, rateLimit)

	mc := newTestModelCatalog(t)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, mc)
	require.NoError(t, err)

	// Update token usage with prefixed model name
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "openai/gpt-4o", schemas.OpenRouter, 100, true, false)
	assert.NoError(t, err, "Should successfully update rate limit via cross-provider match")

	// Rate limit should now be exceeded
	decision, errResult := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "openai/gpt-4o", Provider: schemas.OpenRouter}, nil, nil)
	assert.Error(t, errResult, "Token limit should be exceeded after usage update via cross-provider match")
	assert.Contains(t, errResult.Error(), "token limit exceeded")
	assert.NotEqual(t, DecisionAllow, decision)
}

// TestStore_CheckModelBudget_ModelWithProvider_ExactMatchOnly tests that model+provider configs
// (e.g., "gpt-4o:openai") use exact matching and do NOT fuzzy-match.
func TestStore_CheckModelBudget_ModelWithProvider_ExactMatchOnly(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4o", &providerStr, budget, nil)

	mc := newTestModelCatalog(t)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, mc)
	require.NoError(t, err)

	// Request with the exact matching model+provider should be rejected (budget exceeded)
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4o", Provider: schemas.OpenAI}, nil)
	assert.Error(t, err, "Exact model+provider match should apply budget")

	// Request with a different provider should NOT match the provider-specific config
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4o", Provider: schemas.OpenRouter}, nil)
	assert.NoError(t, err, "Different provider should not match provider-specific config")
}

// TestStore_CheckModelBudget_NoCatalog_NoMatch tests that without a model catalog,
// cross-provider matching does not happen (graceful degradation).
func TestStore_CheckModelBudget_NoCatalog_NoMatch(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	modelConfig := buildModelConfig("mc1", "gpt-4o", nil, budget, nil)

	// No model catalog passed (nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	// Without catalog, "openai/gpt-4o" won't match "gpt-4o" config
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "openai/gpt-4o", Provider: schemas.OpenRouter}, nil)
	assert.NoError(t, err, "Without model catalog, cross-provider matching should not happen")

	// Direct match should still work
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4o", Provider: schemas.OpenAI}, nil)
	assert.Error(t, err, "Direct match should still work without catalog")
}

// ============================================================================
// Store Tests - All-models ("*") wildcard tier (provider-level governance)
// ============================================================================

// TestStore_CheckModelBudget_AllModelsOnProvider_Exceeded verifies that an all-models
// wildcard config (provider=openai, model_name="*") — the migrated provider-level budget —
// applies to ANY model on that provider.
func TestStore_CheckModelBudget_AllModelsOnProvider_Exceeded(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("b1", 100.0, 100.0, "1h") // exceeded
	providerStr := "openai"
	mc := buildModelConfig("mc-provider", configstoreTables.ModelConfigAllModels, &providerStr, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	// A request for an arbitrary OpenAI model must be caught by the "*:openai" config.
	_, err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4o", Provider: schemas.OpenAI}, nil)
	assert.Error(t, err, "all-models budget for the provider should apply to any model on it")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// TestStore_CheckModelBudget_AllModelsOnProvider_OtherProviderPasses confirms the wildcard
// is provider-scoped: it must NOT affect a different provider.
func TestStore_CheckModelBudget_AllModelsOnProvider_OtherProviderPasses(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("b1", 100.0, 100.0, "1h") // exceeded
	providerStr := "openai"
	mc := buildModelConfig("mc-provider", configstoreTables.ModelConfigAllModels, &providerStr, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	decision, err := store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "claude-opus-4-7", Provider: schemas.Anthropic}, nil)
	assert.NoError(t, err, "an OpenAI all-models budget must not affect an Anthropic request")
	assert.Equal(t, DecisionAllow, decision)
}

// TestStore_UpdateProviderModelUsage_BumpsAllModelsWildcard verifies usage recording reaches
// the all-models wildcard config (record-then-check loop for provider-level governance).
func TestStore_UpdateProviderModelUsage_BumpsAllModelsWildcard(t *testing.T) {
	logger := NewMockLogger()
	rateLimit := buildRateLimitWithUsage("rl1", 100, 0, 1000000, 0) // 100-token cap
	providerStr := "openai"
	mc := buildModelConfig("mc-provider", configstoreTables.ModelConfigAllModels, &providerStr, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	// Within limit initially.
	decision, err := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4o", Provider: schemas.OpenAI}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision)

	// Record usage for a (different) model on the provider — must bump the "*:openai" config.
	require.NoError(t, store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4o", schemas.OpenAI, 150, true, true))

	// Now the all-models rate limit trips for any model on the provider.
	decision, err = store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4o-mini", Provider: schemas.OpenAI}, nil, nil)
	assert.Error(t, err)
	assert.Equal(t, DecisionTokenLimited, decision)
}

// ============================================================================
// Store Tests - Per-VK-Scoped Model Budget / Rate Limit
// ============================================================================

func TestStore_CheckVirtualKeyScopedModelBudget_NilVK(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	decision, err := store.CheckScopedModelBudget(context.Background(), "", "", &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision)
}

func TestStore_CheckVirtualKeyScopedModelBudget_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	vk := buildVirtualKey("vk1", "vk1-value", "vk1", true)
	decision, err := store.CheckScopedModelBudget(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision)
}

func TestStore_CheckVirtualKeyScopedModelBudget_WithinLimit(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "vk1-value", "vk1", true)
	budget := buildBudget("b1", 100.0, "1h")
	mc := buildVKScopedModelConfig("mc1", "gpt-4", nil, vk.ID, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	_, err = store.CheckScopedModelBudget(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err, "Should allow when per-VK model budget is within limit")
}

func TestStore_CheckVirtualKeyScopedModelBudget_Exceeded(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "vk1-value", "vk1", true)
	budget := buildBudgetWithUsage("b1", 100.0, 100.0, "1h") // At limit
	mc := buildVKScopedModelConfig("mc1", "gpt-4", nil, vk.ID, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	_, err = store.CheckScopedModelBudget(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil)
	assert.Error(t, err, "Should reject when per-VK model budget is exceeded")
	assert.Contains(t, err.Error(), "budget exceeded")
}

func TestStore_CheckVirtualKeyScopedModelBudget_OnlyAppliesToMatchingVK(t *testing.T) {
	logger := NewMockLogger()
	ownerVK := buildVirtualKey("vk1", "vk1-value", "vk1", true)
	otherVK := buildVirtualKey("vk2", "vk2-value", "vk2", true)
	budget := buildBudgetWithUsage("b1", 100.0, 100.0, "1h") // exceeded
	mc := buildVKScopedModelConfig("mc1", "gpt-4", nil, ownerVK.ID, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	// A request made with a DIFFERENT virtual key must not be affected by vk1's scoped config.
	decision, err := store.CheckScopedModelBudget(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, otherVK.ID, &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision)
}

func TestStore_CheckVirtualKeyScopedModelBudget_IgnoresGlobalConfig(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "vk1-value", "vk1", true)
	// A GLOBAL (scope defaults to global) model config that is exceeded. The per-VK scoped
	// check must ignore it — global is enforced separately by EvaluateModelAndProviderRequest,
	// so the scoped path must not double-count it.
	budget := buildBudgetWithUsage("b1", 100.0, 100.0, "1h")
	globalMC := buildModelConfig("mc-global", "gpt-4", nil, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*globalMC},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	decision, err := store.CheckScopedModelBudget(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err, "Scoped check must not pick up the global config")
	assert.Equal(t, DecisionAllow, decision)

	// Sanity: the global model check DOES still catch the exceeded global budget.
	_, gErr := store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil)
	assert.Error(t, gErr, "Global model check should catch the exceeded global budget")
}

func TestStore_CheckVirtualKeyScopedModelRateLimit_TokenLimitExceeded(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "vk1-value", "vk1", true)
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // tokens at max
	mc := buildVKScopedModelConfig("mc1", "gpt-4", nil, vk.ID, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	decision, err := store.CheckScopedModelRateLimit(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil, nil)
	assert.Error(t, err, "Should reject when per-VK model token limit is exceeded")
	assert.Equal(t, DecisionTokenLimited, decision)
}

func TestStore_CheckVirtualKeyScopedModelRateLimit_WithinLimit(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "vk1-value", "vk1", true)
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 100, 1000, 10) // well within limits
	mc := buildVKScopedModelConfig("mc1", "gpt-4", nil, vk.ID, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	decision, err := store.CheckScopedModelRateLimit(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision)
}

// TestStore_VirtualKeyScopedModel_RecordThenCheck_TokenLimitTrips reproduces the reported bug:
// recording usage against a per-VK scoped model config must increment the scoped counter so a
// subsequent check trips. (The original bug only wired the check, not the usage recording.)
func TestStore_VirtualKeyScopedModel_RecordThenCheck_TokenLimitTrips(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "vk1-value", "vk1", true)
	rateLimit := buildRateLimitWithUsage("rl1", 100, 0, 1000000, 0) // 100 token cap, request cap effectively unlimited
	mc := buildVKScopedModelConfig("mc1", "claude-opus-4-7", nil, vk.ID, nil, rateLimit)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		RateLimits:   []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	req := &EvaluationRequest{Model: "claude-opus-4-7", Provider: schemas.Anthropic}

	// Initially within limit.
	decision, err := store.CheckScopedModelRateLimit(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, req, nil, nil)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision)

	// Record usage above the limit (what the tracker does post-response). Provider differs from
	// the config's (which is all-providers), exercising the model-only scoped lookup.
	require.NoError(t, store.UpdateScopedModelRateLimitUsageInMemory(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, "claude-opus-4-7", schemas.Anthropic, 150, true, true))

	// Now the scoped check must trip.
	decision, err = store.CheckScopedModelRateLimit(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, req, nil, nil)
	assert.Error(t, err)
	assert.Equal(t, DecisionTokenLimited, decision)
}

func TestStore_VirtualKeyScopedModel_RecordThenCheck_BudgetTrips(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "vk1-value", "vk1", true)
	budget := buildBudget("b1", 10.0, "1h") // $10 cap, 0 usage
	mc := buildVKScopedModelConfig("mc1", "claude-opus-4-7", nil, vk.ID, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	req := &EvaluationRequest{Model: "claude-opus-4-7", Provider: schemas.Anthropic}

	decision, err := store.CheckScopedModelBudget(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, req, nil)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, decision)

	require.NoError(t, store.UpdateScopedModelBudgetUsageInMemory(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, "claude-opus-4-7", schemas.Anthropic, 15.0))

	_, err = store.CheckScopedModelBudget(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, req, nil)
	assert.Error(t, err, "scoped budget should trip once usage exceeds the cap")
}

// TestStore_VKGovernanceBudget_NoDoubleCount is the double-count guard: after the cutover a
// VK's budget lives only on its VK-scoped all-models wildcard model config (vk.Budgets is
// empty). The tracker invokes both the scoped-model path and the VK hierarchy path on every
// request; only the scoped path may charge the budget — never both.
func TestStore_VKGovernanceBudget_NoDoubleCount(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "vk1-value", "vk1", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{buildProviderConfig("openai", []string{"*"})}
	budget := buildBudget("vkb", 100.0, "1h")
	// Owned by the VK-scoped all-models wildcard (provider=nil), not by the VK directly.
	mc := buildVKScopedModelConfig("mc-vk", "*", nil, vk.ID, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	// Mirror tracker.UpdateUsage: scoped-model path + hierarchy path, same request/cost.
	require.NoError(t, store.UpdateScopedModelBudgetUsageInMemory(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, "gpt-4", schemas.OpenAI, 10.0))
	require.NoError(t, store.UpdateVirtualKeyBudgetUsageInMemory(context.Background(), vk, schemas.OpenAI, 10.0))

	b := store.LoadBudget(context.Background(), "vkb")
	require.NotNil(t, b)
	assert.InDelta(t, 10.0, b.CurrentUsage, 0.001, "VK governance budget must be charged exactly once (no hierarchy+scoped double count)")
}

// TestStore_CheckVirtualKeyScopedModelBudget_MultiBudget_OneExceededBlocks exercises the
// scope-chain path that production VK governance flows through after cutover, with multiple
// budgets on one VK-scoped wildcard config.
func TestStore_CheckVirtualKeyScopedModelBudget_MultiBudget_OneExceededBlocks(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "vk1-value", "vk1", true)
	within := buildBudget("b-day", 100.0, "1d")
	exceeded := buildBudgetWithUsage("b-hour", 10.0, 10.0, "1h")
	mcID := "mc-vk-multi"
	mc := &configstoreTables.TableModelConfig{
		ID:        mcID,
		ModelName: configstoreTables.ModelConfigAllModels,
		Scope:     configstoreTables.ModelConfigScopeVirtualKey,
		ScopeID:   &vk.ID,
	}
	for _, b := range []*configstoreTables.TableBudget{within, exceeded} {
		b.ModelConfigID = &mcID
		mc.Budgets = append(mc.Budgets, *b)
	}
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*within, *exceeded},
	}, nil)
	require.NoError(t, err)

	_, err = store.CheckScopedModelBudget(context.Background(), configstoreTables.ModelConfigScopeVirtualKey, vk.ID, &EvaluationRequest{Model: "gpt-4", Provider: schemas.OpenAI}, nil)
	assert.Error(t, err, "an exceeded budget among several on a VK-scoped config must block")
}
