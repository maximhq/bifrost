package governance

import (
	"context"
	"testing"
	"time"

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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{})
	require.NoError(t, err)

	err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err, "Should allow when no provider config exists")
}

func TestStore_CheckProviderBudget_NoBudget(t *testing.T) {
	logger := NewMockLogger()
	provider := buildProviderWithGovernance("openai", nil, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
	})
	require.NoError(t, err)

	err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err, "Should allow when provider has no budget")
}

func TestStore_CheckProviderBudget_WithinLimit(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	provider := buildProviderWithGovernance("openai", budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
		Budgets:   []configstoreTables.TableBudget{*budget},
	})
	require.NoError(t, err)

	err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err, "Should allow when budget is within limit")
}

func TestStore_CheckProviderBudget_Exceeded(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	provider := buildProviderWithGovernance("openai", budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
		Budgets:   []configstoreTables.TableBudget{*budget},
	})
	require.NoError(t, err)

	err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	})
	require.NoError(t, err)

	// With baseline that would exceed limit
	baselines := map[string]float64{"budget1": 15.0}
	err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, baselines)
	assert.Error(t, err, "Should reject when current usage + baseline exceeds limit")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// ============================================================================
// Store Tests - Provider Rate Limit
// ============================================================================

func TestStore_CheckProviderRateLimit_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{})
	require.NoError(t, err)

	err, decision := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.NoError(t, err, "Should allow when no provider config exists")
	assert.Equal(t, DecisionAllow, decision)
}

func TestStore_CheckProviderRateLimit_NoRateLimit(t *testing.T) {
	logger := NewMockLogger()
	provider := buildProviderWithGovernance("openai", nil, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*provider},
	})
	require.NoError(t, err)

	err, decision := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
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
	})
	require.NoError(t, err)

	err, decision := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
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
	})
	require.NoError(t, err)

	err, decision := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
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
	})
	require.NoError(t, err)

	err, decision := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
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
	})
	require.NoError(t, err)

	err, decision := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.NoError(t, err, "Should allow when provider rate limits are within limits")
	assert.Equal(t, DecisionAllow, decision)
}

// ============================================================================
// Store Tests - Model Budget
// ============================================================================

func TestStore_CheckModelBudget_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.NoError(t, err, "Should allow when no model config exists")
}

func TestStore_CheckModelBudget_ModelOnly_WithinLimit(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.NoError(t, err, "Should allow when model budget is within limit")
}

func TestStore_CheckModelBudget_ModelOnly_Exceeded(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1h") // At limit
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.Error(t, err, "Should reject when model budget is exceeded")
	assert.Contains(t, err.Error(), "budget exceeded")
}

func TestStore_CheckModelBudget_ModelWithProvider_WithinLimit(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudget("budget1", 100.0, "1h")
	providerStr := "openai"
	modelConfig := buildModelConfig("mc1", "gpt-4", &providerStr, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
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
	})
	require.NoError(t, err)

	// Request with Azure (different provider) for same model should pass
	provider := schemas.Azure
	err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4o", Provider: provider}, nil)
	assert.NoError(t, err, "Should allow when model config is provider-specific and different provider is used")
}

// ============================================================================
// Store Tests - Model Rate Limit
// ============================================================================

func TestStore_CheckModelRateLimit_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
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
	})
	require.NoError(t, err)

	// Request with Azure (different provider) for same model should pass
	provider := schemas.Azure
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4o", Provider: provider}, nil, nil)
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
	})
	require.NoError(t, err)

	// Request with Azure (different provider) for same model should pass
	provider := schemas.Azure
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4o", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when model config is provider-specific and different provider is used (request limit)")
	assert.Equal(t, DecisionAllow, decision)
}

// ============================================================================
// Store Tests - Update Provider Budget Usage
// ============================================================================

func TestStore_UpdateProviderBudgetUsage_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{})
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
	})
	require.NoError(t, err)

	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "", schemas.OpenAI, 10.0)
	assert.NoError(t, err, "Should successfully update provider budget usage")

	// Verify usage was updated
	err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err, "Should still be within limit after first update")

	// Update again to exceed
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "", schemas.OpenAI, 95.0)
	assert.NoError(t, err, "Should successfully update provider budget usage even when exceeding")

	// Now should be exceeded
	err = store.CheckProviderBudget(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.Error(t, err, "Should be exceeded after second update")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// ============================================================================
// Store Tests - Update Provider Rate Limit Usage
// ============================================================================

func TestStore_UpdateProviderRateLimitUsage_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{})
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
	})
	require.NoError(t, err)

	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "", schemas.OpenAI, 5000, true, false)
	assert.NoError(t, err, "Should successfully update provider token usage")

	// Check that tokens were updated but requests were not
	err, decision := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.NoError(t, err, "Should still be within token limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update tokens to exceed
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "", schemas.OpenAI, 6000, true, false)
	assert.NoError(t, err, "Should successfully update provider token usage even when exceeding")

	// Now should be exceeded
	err, decision = store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
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
	})
	require.NoError(t, err)

	// Update requests 500 times
	for i := 0; i < 500; i++ {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "", schemas.OpenAI, 0, false, true)
		assert.NoError(t, err, "Should successfully update provider request usage")
	}

	// Should still be within limit
	err, decision := store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.NoError(t, err, "Should allow when provider request limit is within limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update 500 more times to exceed
	for i := 0; i < 500; i++ {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "", schemas.OpenAI, 0, false, true)
		assert.NoError(t, err, "Should successfully update provider request usage even when exceeding")
	}

	// Now should be exceeded
	err, decision = store.CheckProviderRateLimit(context.Background(), &EvaluationRequest{Provider: schemas.OpenAI}, nil, nil)
	assert.Error(t, err, "Should reject when provider request limit is exceeded after update")
	assert.Equal(t, DecisionRequestLimited, decision)
	assert.Contains(t, err.Error(), "request limit exceeded")
}

// ============================================================================
// Store Tests - Update Model Budget Usage
// ============================================================================

func TestStore_UpdateModelBudgetUsage_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{})
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "gpt-4", provider, 10.0)
	assert.NoError(t, err, "Should successfully update model budget usage")

	// Verify usage was updated
	err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.NoError(t, err, "Should still be within limit after first update")

	// Update again to exceed
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "gpt-4", provider, 95.0)
	assert.NoError(t, err, "Should successfully update model budget usage even when exceeding")

	// Now should be exceeded
	err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "gpt-4", provider, 10.0)
	assert.NoError(t, err, "Should successfully update both model-only and model+provider budget usage")

	// Both budgets should be updated
	// Check model-only budget
	err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.NoError(t, err, "Should still be within limit")

	// Update to exceed model-only budget
	err = store.UpdateProviderAndModelBudgetUsageInMemory(context.Background(), "gpt-4", provider, 95.0)
	assert.NoError(t, err, "Should successfully update model budget usage even when exceeding")

	// Now model-only budget should be exceeded
	err = store.CheckModelBudget(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil)
	assert.Error(t, err, "Should be exceeded when model-only budget is exceeded")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// ============================================================================
// Store Tests - Update Model Rate Limit Usage
// ============================================================================

func TestStore_UpdateModelRateLimitUsage_NoConfig(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{})
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 5000, true, false)
	assert.NoError(t, err, "Should successfully update model token usage")

	// Should still be within limit
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when model token limit is within limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update to exceed
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 6000, true, false)
	assert.NoError(t, err, "Should successfully update model token usage even when exceeding")

	// Now should be exceeded
	err, decision = store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 5000, true, false)
	assert.NoError(t, err, "Should successfully update both model-only and model+provider token usage")

	// Should still be within limit
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when both rate limits are within limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update to exceed model-only rate limit (should fail at model-only level)
	err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 6000, true, false)
	assert.NoError(t, err, "Should successfully update model token usage even when exceeding")

	// Now should be exceeded (model-only rate limit exceeded)
	err, decision = store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	// Update requests 500 times
	for range 500 {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 0, false, true)
		assert.NoError(t, err, "Should successfully update model request usage")
	}

	// Should still be within limit
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when model request limit is within limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update 500 more times to exceed
	for range 500 {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 0, false, true)
		assert.NoError(t, err, "Should successfully update model request usage even when exceeding")
	}

	// Now should be exceeded
	err, decision = store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
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
	})
	require.NoError(t, err)

	provider := schemas.OpenAI
	// Update requests 500 times (should update both model-only and model+provider)
	for range 500 {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 0, false, true)
		assert.NoError(t, err, "Should successfully update both model-only and model+provider request usage")
	}

	// Should still be within limit
	err, decision := store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.NoError(t, err, "Should allow when both rate limits are within limit")
	assert.Equal(t, DecisionAllow, decision)

	// Update 500 more times to exceed model-only rate limit
	for range 500 {
		err = store.UpdateProviderAndModelRateLimitUsageInMemory(context.Background(), "gpt-4", provider, 0, false, true)
		assert.NoError(t, err, "Should successfully update model request usage even when exceeding")
	}

	// Now should be exceeded (model-only rate limit exceeded)
	err, decision = store.CheckModelRateLimit(context.Background(), &EvaluationRequest{Model: "gpt-4", Provider: provider}, nil, nil)
	assert.Error(t, err, "Should reject when model-only request limit is exceeded after update")
	assert.Equal(t, DecisionRequestLimited, decision)
	assert.Contains(t, err.Error(), "request limit exceeded")
}

// ============================================================================
// Resolver Tests - EvaluateModelAndProviderRequest
// ============================================================================

func TestResolver_EvaluateModelAndProviderRequest_NoConfigs(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger)
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
	})
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
	})
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
	})
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
	})
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
	})
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
	})
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
	})
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
	})
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
	})
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
	})
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
	})
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
	})
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
	})
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	})
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

func TestPreLLMHook_ModelProviderPass_VirtualKeyNotFound(t *testing.T) {
	logger := NewMockLogger()
	// Model/provider checks pass (no limits)
	// Virtual key not found
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{})
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
	})
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
	})
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
	})
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
	})
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
	})
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
				RequestType:    schemas.ChatCompletionRequest,
				Provider:       schemas.OpenAI,
				ModelRequested: "gpt-4",
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
	})
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
				RequestType:    schemas.ChatCompletionRequest,
				Provider:       schemas.OpenAI,
				ModelRequested: "gpt-4",
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

func TestPostHook_UpdatesModelBudgetUsage_NoVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	// Set budget with initial usage close to limit to test the flow
	// Note: Without model catalog, cost will be 0, so we test the flow even if budget isn't actually updated
	budget := buildBudgetWithUsage("budget1", 100.0, 50.0, "1h")
	modelConfig := buildModelConfig("mc1", "gpt-4", nil, budget, nil)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*modelConfig},
		Budgets:      []configstoreTables.TableBudget{*budget},
	})
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
				RequestType:    schemas.ChatCompletionRequest,
				Provider:       schemas.OpenAI,
				ModelRequested: "gpt-4",
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
	})
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
				RequestType:    schemas.ChatCompletionRequest,
				Provider:       schemas.OpenAI,
				ModelRequested: "gpt-4",
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
