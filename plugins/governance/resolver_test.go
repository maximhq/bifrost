package governance

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBudgetResolver_EvaluateRequest_AllowedRequest tests happy path
func TestBudgetResolver_EvaluateRequest_AllowedRequest(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)

	assertDecision(t, DecisionAllow, result)
}

// TestBudgetResolver_EvaluateRequest_GracePeriodValue tests that a rotated-out
// value authenticates during the rotation grace period and is rejected after.
func TestBudgetResolver_EvaluateRequest_GracePeriodValue(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-current", "Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}
	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	vk.PreviousValue = *schemas.NewSecretVar("sk-bf-rotated-out")
	vk.PreviousValueExpiresAt = &exp
	vk.RotatedAt = &now

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-rotated-out")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-rotated-out", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionAllow, result)

	// Deterministically expire the grace window: expiry is evaluated lazily at
	// lookup time on the stored object, so aging the shared pointer replaces
	// the previous short-sleep pattern without any timing dependence.
	stored, found := store.GetVirtualKey(context.Background(), "sk-bf-current")
	require.True(t, found)
	past := now.Add(-time.Millisecond)
	stored.PreviousValueExpiresAt = &past

	// resolverCtx resolves access once, at call time, from whatever the store
	// currently reports for the presented value - so the expired grace window
	// needs a fresh context, not a re-evaluation of the one built above.
	expiredCtx := resolverCtx(store, "sk-bf-rotated-out")
	assert.Nil(t, expiredCtx.Grant().Access(), "the rotated-out value must resolve to nothing once its grace period has passed")

	ctx = resolverCtx(store, "sk-bf-current")
	result = evaluateVirtualKey(resolver, ctx, "sk-bf-current", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionAllow, result)
}

// TestBudgetResolver_EvaluateRequest_ProviderBlocked tests provider filtering
func TestBudgetResolver_EvaluateRequest_ProviderBlocked(t *testing.T) {
	logger := NewMockLogger()

	// VK with only Anthropic allowed
	providerConfigs := []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("anthropic", []string{"claude-3-sonnet"}),
	}
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", providerConfigs)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	// Try to use OpenAI (not allowed)
	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)

	assertDecision(t, DecisionProviderBlocked, result)
}

// TestBudgetResolver_EvaluateRequest_ListModelsBypassesProviderBlock verifies that a VK
// with a restricted provider allowlist does NOT block a ListModelsRequest for a provider
// outside its allowlist. List-models fans out across all configured providers and is
// filtered per-VK in the PostHook, so provider gating must be skipped here (resolver.go:275).
func TestBudgetResolver_EvaluateRequest_ListModelsBypassesProviderBlock(t *testing.T) {
	logger := NewMockLogger()

	// Same Anthropic-only VK as TestBudgetResolver_EvaluateRequest_ProviderBlocked.
	providerConfigs := []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("anthropic", []string{"claude-3-sonnet"}),
	}
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", providerConfigs)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	// OpenAI is not in the allowlist, but ListModelsRequest must not be provider-blocked.
	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ListModelsRequest, false, false)

	assert.NotEqual(t, DecisionProviderBlocked, result.Decision, "ListModelsRequest must bypass provider allowlist gating")
	assertDecision(t, DecisionAllow, result)
}

// TestBudgetResolver_EvaluateRequest_ModelBlocked tests model filtering
func TestBudgetResolver_EvaluateRequest_ModelBlocked(t *testing.T) {
	logger := NewMockLogger()

	// VK with specific models allowed
	providerConfigs := []configstoreTables.TableVirtualKeyProviderConfig{
		{
			Provider:      "openai",
			AllowedModels: []string{"gpt-4", "gpt-4-turbo"}, // Only these models
			Weight:        bifrost.Ptr(1.0),
			RateLimit:     nil,
			Keys:          []configstoreTables.TableKey{},
		},
	}
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", providerConfigs)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	// Try to use gpt-4o-mini (not in allowed list)
	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4o-mini", schemas.ChatCompletionRequest, false, false)

	assertDecision(t, DecisionModelBlocked, result)
}

// TestBudgetResolver_EvaluateRequest_SkipProviderCheckAllowsUnconfiguredProvider verifies
// that skipProviderCheck drops the provider allowlist, and drops the model allowlist with
// it when the VK has no config for that provider. Callers that are evaluated but never
// routed (the enterprise /inspect endpoint) set this: their provider is whatever upstream
// the caller was already talking to, so the allowlist has nothing to say about it. Without
// the model gate following along the request would just fail one step later on a model list
// that only exists inside a provider config the VK does not have.
func TestBudgetResolver_EvaluateRequest_SkipProviderCheckAllowsUnconfiguredProvider(t *testing.T) {
	logger := NewMockLogger()

	// Anthropic-only VK, same shape as TestBudgetResolver_EvaluateRequest_ProviderBlocked.
	providerConfigs := []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("anthropic", []string{"claude-3-sonnet"}),
	}
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", providerConfigs)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	// Baseline: without the flag this is a provider block.
	blocked := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionProviderBlocked, blocked)

	// With the flag the same request is allowed, and does not fall through to a model block.
	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, true)
	assert.NotEqual(t, DecisionProviderBlocked, result.Decision, "skipProviderCheck must bypass the provider allowlist")
	assert.NotEqual(t, DecisionModelBlocked, result.Decision, "an unconfigured provider carries no model allowlist to block on")
	assertDecision(t, DecisionAllow, result)
}

// TestBudgetResolver_EvaluateRequest_SkipProviderCheckKeepsModelAllowlist verifies that
// skipProviderCheck only relaxes the model gate for providers the VK has no config for.
// A provider the VK does configure keeps its model allowlist, so model governance stays
// intact on the paths that set the flag.
func TestBudgetResolver_EvaluateRequest_SkipProviderCheckKeepsModelAllowlist(t *testing.T) {
	logger := NewMockLogger()

	providerConfigs := []configstoreTables.TableVirtualKeyProviderConfig{
		{
			Provider:      "openai",
			AllowedModels: []string{"gpt-4", "gpt-4-turbo"},
			Weight:        bifrost.Ptr(1.0),
			RateLimit:     nil,
			Keys:          []configstoreTables.TableKey{},
		},
	}
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", providerConfigs)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	// openai IS configured, so gpt-4o-mini stays blocked even with the flag set.
	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4o-mini", schemas.ChatCompletionRequest, false, true)
	assertDecision(t, DecisionModelBlocked, result)

	// And an allowed model on that same configured provider still passes.
	allowed := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, true)
	assertDecision(t, DecisionAllow, allowed)
}

// TestGovernancePlugin_Evaluate_DirectKeySatisfiesMandatoryAuth verifies direct provider keys satisfy mandatory auth after transport validation.
func TestGovernancePlugin_Evaluate_DirectKeySatisfiesMandatoryAuth(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	mandatory := true
	plugin := &GovernancePlugin{
		store:         store,
		resolver:      NewBudgetResolver(store, nil, logger, nil),
		isVkMandatory: &mandatory,
		isEnterprise:  true,
	}

	ctx := emptyCtx()
	ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{
		ID:    "header-provided",
		Name:  "header-provided",
		Value: schemas.SecretVar{Val: "sk-real-openai-key"},
	})

	result, bifrostErr := plugin.Evaluate(ctx, &EvaluationRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
	})

	require.Nil(t, bifrostErr)
	assertDecision(t, DecisionAllow, result)
}

// TestGovernancePlugin_Evaluate_HeaderWithoutContextDoesNotSatisfyMandatoryAuth verifies callers cannot spoof direct-key auth with only a request header.
func TestGovernancePlugin_Evaluate_HeaderWithoutContextDoesNotSatisfyMandatoryAuth(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	mandatory := true
	plugin := &GovernancePlugin{
		store:         store,
		resolver:      NewBudgetResolver(store, nil, logger, nil),
		isVkMandatory: &mandatory,
		isEnterprise:  true,
	}

	_, bifrostErr := plugin.Evaluate(emptyCtx(), &EvaluationRequest{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		RequestType: schemas.PassthroughRequest,
	})

	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 401, *bifrostErr.StatusCode)
	assert.Equal(t, "authentication is required. Provide a virtual key (x-bf-vk), API key, or user token.", bifrostErr.Error.Message)
}

// TestHasDirectKeyAuth reads only the transport-owned direct-key context value.
func TestHasDirectKeyAuth(t *testing.T) {
	ctx := &schemas.BifrostContext{}
	assert.False(t, hasDirectKeyAuth(ctx))

	ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{
		ID:    "header-provided",
		Name:  "header-provided",
		Value: schemas.SecretVar{Val: "sk-real-openai-key"},
	})

	assert.True(t, hasDirectKeyAuth(ctx))
}

// TestBudgetResolver_EvaluateRequest_RateLimitExceeded_TokenLimit tests token limit
func TestBudgetResolver_EvaluateRequest_RateLimitExceeded_TokenLimit(t *testing.T) {
	logger := NewMockLogger()

	// VK with rate limit already at max
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 10000, 1000, 0) // Tokens at max
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)

	assertDecision(t, DecisionTokenLimited, result)
}

// TestBudgetResolver_EvaluateRequest_RateLimitExceeded_RequestLimit tests request limit
func TestBudgetResolver_EvaluateRequest_RateLimitExceeded_RequestLimit(t *testing.T) {
	logger := NewMockLogger()

	// VK with request limit already at max
	rateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 100, 100) // Requests at max
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)

	assertDecision(t, DecisionRequestLimited, result)
}

// TestBudgetResolver_EvaluateRequest_RateLimitExpired tests rate limit reset
func TestBudgetResolver_EvaluateRequest_RateLimitExpired(t *testing.T) {
	logger := NewMockLogger()

	// VK with rate limit that's expired (should be treated as reset)
	duration := "1m"
	rateLimit := &configstoreTables.TableRateLimit{
		ID:                   "rl1",
		TokenMaxLimit:        ptrInt64(10000),
		TokenCurrentUsage:    10000, // At limit
		TokenResetDuration:   &duration,
		TokenLastReset:       time.Now().Add(-2 * time.Minute), // Expired
		RequestMaxLimit:      ptrInt64(1000),
		RequestCurrentUsage:  0,
		RequestResetDuration: &duration,
		RequestLastReset:     time.Now(),
	}
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	// Reset expired rate limits (simulating ticker behavior)
	expiredRateLimits := store.ResetExpiredRateLimitsInMemory(context.Background(), true)
	err = store.ResetExpiredRateLimits(context.Background(), expiredRateLimits)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)

	// Should allow because rate limit was expired and has been reset
	assertDecision(t, DecisionAllow, result)
}

// TestBudgetResolver_EvaluateRequest_BudgetExceeded tests budget violation
func TestBudgetResolver_EvaluateRequest_BudgetExceeded(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 100.0, 100.0, "1d") // At limit
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)

	assertDecision(t, DecisionBudgetExceeded, result)
}

// TestBudgetResolver_EvaluateRequest_BudgetExpired tests expired budget (should be treated as reset)
func TestBudgetResolver_EvaluateRequest_BudgetExpired(t *testing.T) {
	logger := NewMockLogger()

	budget := &configstoreTables.TableBudget{
		ID:            "budget1",
		MaxLimit:      100.0,
		CurrentUsage:  100.0, // At limit
		ResetDuration: "1d",
		LastReset:     time.Now().Add(-48 * time.Hour), // Expired
	}
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)

	// Should allow because budget is expired (will be reset)
	assertDecision(t, DecisionAllow, result)
}

// TestBudgetResolver_EvaluateRequest_MultiLevelBudgetHierarchy tests hierarchy checking
func TestBudgetResolver_EvaluateRequest_MultiLevelBudgetHierarchy(t *testing.T) {
	logger := NewMockLogger()

	vkBudget := buildBudgetWithUsage("vk-budget", 100.0, 50.0, "1d")
	teamBudget := buildBudgetWithUsage("team-budget", 500.0, 200.0, "1d")
	customerBudget := buildBudgetWithUsage("customer-budget", 1000.0, 400.0, "1d")

	team := buildTeam("team1", "Team 1", teamBudget)
	customer := buildCustomer("customer1", "Customer 1", customerBudget)
	team.CustomerID = &customer.ID
	team.Customer = customer

	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", vkBudget)
	vk.TeamID = &team.ID
	vk.Team = team

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*vkBudget, *teamBudget, *customerBudget},
		Teams:       []configstoreTables.TableTeam{*team},
		Customers:   []configstoreTables.TableCustomer{*customer},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	// Test: All under limit should pass
	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionAllow, result)

	// Test: VK budget exceeds should fail
	// Get the governance data to update the budget directly
	governanceData := store.GetGovernanceData(context.Background())
	vkBudgetToUpdate := governanceData.Budgets["vk-budget"]
	if vkBudgetToUpdate != nil {
		vkBudgetToUpdate.CurrentUsage = 100.0
		store.budgets.Store("vk-budget", vkBudgetToUpdate)
	}
	result = evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionBudgetExceeded, result)
}

// TestBudgetResolver_EvaluateRequest_ProviderLevelRateLimit tests provider-specific rate limits
func TestBudgetResolver_EvaluateRequest_ProviderLevelRateLimit(t *testing.T) {
	logger := NewMockLogger()

	// Provider with rate limit at max
	providerRL := buildRateLimitWithUsage("provider-rl", 5000, 5000, 500, 0)
	providerConfig := buildProviderConfigWithRateLimit("openai", []string{"gpt-4"}, providerRL)
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{providerConfig})

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*providerRL},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)

	assertDecision(t, DecisionTokenLimited, result)
}

// TestBudgetResolver_CheckRateLimits_BothExceeded tests token and request limits simultaneously
func TestBudgetResolver_CheckRateLimits_BothExceeded(t *testing.T) {
	logger := NewMockLogger()

	// Rate limit with both token and request at max
	rateLimit := buildRateLimitWithUsage("rl1", 1000, 1000, 100, 100)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)

	assertDecision(t, DecisionRateLimited, result)
	assert.Contains(t, result.Reason, "rate limit")
}

// TestBudgetResolver_ContextPopulation tests context values are set correctly
func TestBudgetResolver_ContextPopulation(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}
	customer := buildCustomer("cust1", "Customer 1", nil)
	team := buildTeam("team1", "Team 1", nil)
	team.CustomerID = &customer.ID
	team.Customer = customer
	vk.TeamID = &team.ID
	vk.Team = team
	vk.CustomerID = &customer.ID

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Teams:       []configstoreTables.TableTeam{*team},
		Customers:   []configstoreTables.TableCustomer{*customer},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)

	assert.Equal(t, DecisionAllow, result.Decision)

	// Check context was populated
	vkID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string)
	teamID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceTeamID).(string)
	customerID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerID).(string)

	assert.Equal(t, "vk1", vkID)
	assert.Equal(t, "team1", teamID)
	assert.Equal(t, "cust1", customerID)
}

// TestBudgetResolver_EvaluateRequest_PassthroughModelFiltering verifies that passthrough requests
// enforce the VK's model allowlist only when a model is resolved: a disallowed model is blocked, an
// allowed model passes, and an absent model imposes no model restriction. Non-passthrough
// model-not-required types (e.g. batch) remain unfiltered, confirming the change is scoped.
func TestBudgetResolver_EvaluateRequest_PassthroughModelFiltering(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		requestType schemas.RequestType
		want        Decision
	}{
		{"passthrough disallowed model is blocked", "gpt-4o-mini", schemas.PassthroughRequest, DecisionModelBlocked},
		{"passthrough allowed model passes", "gpt-4", schemas.PassthroughRequest, DecisionAllow},
		{"passthrough without model has no restriction", "", schemas.PassthroughRequest, DecisionAllow},
		{"passthrough stream disallowed model is blocked", "gpt-4o-mini", schemas.PassthroughStreamRequest, DecisionModelBlocked},
		{"passthrough stream allowed model passes", "gpt-4", schemas.PassthroughStreamRequest, DecisionAllow},
		{"passthrough stream without model has no restriction", "", schemas.PassthroughStreamRequest, DecisionAllow},
		// Batch create carries no model of its own for a file-based batch, but an inline
		// one names a model per item and governance evaluates each — so the allowlist
		// applies whenever a model is actually present.
		{"batch with disallowed model is blocked", "gpt-4o-mini", schemas.BatchCreateRequest, DecisionModelBlocked},
		{"batch with allowed model passes", "gpt-4", schemas.BatchCreateRequest, DecisionAllow},
		{"batch without model has no restriction", "", schemas.BatchCreateRequest, DecisionAllow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := NewMockLogger()
			providerConfigs := []configstoreTables.TableVirtualKeyProviderConfig{
				buildProviderConfig("openai", []string{"gpt-4", "gpt-4-turbo"}),
			}
			vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", providerConfigs)

			store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
				VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
			}, nil, nil)
			require.NoError(t, err)

			resolver := NewBudgetResolver(store, nil, logger, nil)
			ctx := resolverCtx(store, "sk-bf-test")

			result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, tt.model, tt.requestType, false, false)
			assertDecision(t, tt.want, result)
		})
	}
}

// TestBudgetResolver_EvaluateVirtualKey_ActiveNoExpiry verifies that a VK
// with no expiry is allowed.
func TestBudgetResolver_EvaluateVirtualKey_ActiveNoExpiry(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionAllow, result)
}

// TestBudgetResolver_EvaluateVirtualKey_FutureExpiry verifies that a VK
// with a future expiry is allowed.
func TestBudgetResolver_EvaluateVirtualKey_FutureExpiry(t *testing.T) {
	logger := NewMockLogger()
	future := time.Now().UTC().Add(time.Hour)
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	vk.ExpiresAt = &future
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionAllow, result)
}

// A request that reaches evaluation with nothing resolved is refused, not evaluated. The
// resolver reads what a request may reach rather than working it out, so missing access is a
// wiring fault, and it must land in the deny branch, never be mistaken for "allowed".
//
// The shape below is exactly how that fault occurs in practice: a caller passes a key on the
// evaluation request that the context does not carry, so the key's limits would otherwise be
// checked against nobody's grants.

// The key an evaluation is asked about and the key the context carries are the same key. When
// they differ, the request is refused rather than silently evaluated: the same fail-closed
// branch as above, reached before any limit is charged.
// Evaluation answers from the access its context carries, and nothing looks a key up, so a caller
// cannot ask about one key while the request carries another. That used to be a wiring fault the
// funnel had to detect and refuse; removing the key from the request made it unrepresentable.
func TestBudgetResolver_EvaluateAnswersFromTheAccessOnTheContext(t *testing.T) {
	logger := NewMockLogger()
	first := buildVirtualKey("vk1", "sk-bf-first", "First VK", true)
	first.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}
	second := buildVirtualKey("vk2", "sk-bf-second", "Second VK", true)
	second.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("anthropic", []string{"*"}),
	}

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*first, *second},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)

	// The context carries the second key, which grants anthropic and not openai.
	ctx := resolverCtx(store, "sk-bf-second")

	assertDecision(t, DecisionAllow,
		evaluateVirtualKey(resolver, ctx, "sk-bf-second", schemas.Anthropic, "claude-3-5-sonnet", schemas.ChatCompletionRequest, false, false))
	assertDecision(t, DecisionProviderBlocked,
		evaluateVirtualKey(resolver, ctx, "sk-bf-second", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false))

	// Naming the other key changes nothing: the verdict comes from the access on the context, so the
	// first key's openai grant cannot be reached by asking for it.
	assertDecision(t, DecisionProviderBlocked,
		evaluateVirtualKey(resolver, ctx, "sk-bf-first", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false))
}

// keylessAccessCtx returns a context carrying the access a request holds without presenting a
// key: the state a store that grants access to something other than a key leaves behind.
func keylessAccessCtx(base schemas.Permit) *schemas.BifrostContext {
	ctx := emptyCtx()
	ctx.Grant().SetAccess(grant.NewAccess([]schemas.Permit{base}, nil, "", nil))
	return ctx
}

// A request that presented no key is still held to the grants it carries: the very same step a
// keyed request goes through, so access cannot be gained by choosing a different way in.
func TestBudgetResolver_evaluateAccessWithoutAKey(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)
	resolver := NewBudgetResolver(store, nil, logger, nil)

	granted := grant.NewPermit("other", "h1", "Holder", true, false, []schemas.ProviderPermit{{
		Provider:      "openai",
		AllowedModels: []string{"gpt-4o"},
		KeyIDs:        []string{"key-a"},
	}}, nil)

	t.Run("a granted provider and model is allowed", func(t *testing.T) {
		ctx := keylessAccessCtx(granted)

		result := resolver.evaluateAccess(ctx, &EvaluationRequest{RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI, Model: "gpt-4o"}, ctx.Grant().Access())

		assertDecision(t, DecisionAllow, result)
		// The key restriction the grant implies is published for downstream key selection, as
		// plain ids, the one thing a request holding grants without a key never got before.
		assert.Equal(t, []string{"key-a"}, ctx.Value(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys))
	})

	t.Run("an ungranted provider is blocked, naming the grant in the way", func(t *testing.T) {
		result := resolver.evaluateAccess(keylessAccessCtx(granted), &EvaluationRequest{RequestType: schemas.ChatCompletionRequest, Provider: schemas.Anthropic, Model: "claude-3-5-sonnet"}, keylessAccessCtx(granted).Grant().Access())

		assertDecision(t, DecisionProviderBlocked, result)
		assert.Contains(t, result.Reason, "Holder")
	})

	t.Run("an ungranted model is blocked", func(t *testing.T) {
		result := resolver.evaluateAccess(keylessAccessCtx(granted), &EvaluationRequest{RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI, Model: "gpt-4o-mini"}, keylessAccessCtx(granted).Grant().Access())

		assertDecision(t, DecisionModelBlocked, result)
	})

	// The pure key-based case: no key, no grants, nothing to enforce. This is the opposite of the
	// key path, where unresolved access is a fault rather than an absence.
	t.Run("nothing resolved is unrestricted", func(t *testing.T) {
		ctx := emptyCtx()

		result := resolver.evaluateAccess(ctx, &EvaluationRequest{RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI, Model: "gpt-4o"}, ctx.Grant().Access())

		assertDecision(t, DecisionAllow, result)
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys))
	})
}

// TestBudgetResolver_EvaluateRequest_SkipModelCheckAllowsBlockedModel verifies that
// the skip-model-check context flag drops the virtual key model allowlist even for a
// provider the key does configure. Callers that are evaluated but never routed (the
// enterprise /inspect endpoint) set the flag: they never reach a provider, so the model
// on the payload is the one the caller was already talking to, not a model an operator
// granted. skipProviderCheck alone is not enough here, because a configured provider
// still carries a model allowlist for the request to fail on one step later.
func TestBudgetResolver_EvaluateRequest_SkipModelCheckAllowsBlockedModel(t *testing.T) {
	logger := NewMockLogger()

	providerConfigs := []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"gpt-4"}),
	}
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", providerConfigs)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)

	// Baseline: without the flag the configured provider's model allowlist blocks.
	plain := resolverCtx(store, "sk-bf-test")
	blocked := evaluateVirtualKey(resolver, plain, "sk-bf-test", schemas.OpenAI, "gpt-4o-mini", schemas.ChatCompletionRequest, false, true)
	assertDecision(t, DecisionModelBlocked, blocked)

	// With the flag the same request is allowed.
	skipping := resolverCtx(store, "sk-bf-test")
	skipping.SetValue(schemas.BifrostContextKeySkipModelCheck, true)
	result := evaluateVirtualKey(resolver, skipping, "sk-bf-test", schemas.OpenAI, "gpt-4o-mini", schemas.ChatCompletionRequest, false, true)
	assert.NotEqual(t, DecisionModelBlocked, result.Decision, "skipModelCheck must bypass the virtual key model allowlist")
	assertDecision(t, DecisionAllow, result)
}

// Enforcement no longer names the holder kinds to check, so a deployment that funds requests from
// something this package has never heard of gets those limits enforced without registering anything.
// The alternative fails open: a limit gathered for the attempt, recorded against it, and then neither
// checked nor charged.
func TestEvaluateLimitsEnforcesAnUnfamiliarHolderKind(t *testing.T) {
	logger := NewMockLogger()
	exhausted := buildBudgetWithUsage("b-somewhere-else", 100.0, 100.0, "1d")
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Budgets: []configstoreTables.TableBudget{*exhausted},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := emptyCtx()

	// Limits funded by a holder kind declared nowhere in this package: what a deployment resolving
	// its own permits settles.
	limits := grant.NewLimits([]schemas.Limit{{
		ID:         exhausted.ID,
		HolderKind: "some_deployment_holder",
		HolderID:   "h1",
		HolderName: "Someone",
	}}, nil)
	ctx.Grant().SetLimits(limits)

	result := resolver.evaluateLimits(ctx, &EvaluationRequest{Provider: schemas.OpenAI, Model: "gpt-4o"}, limits)

	assert.Equal(t, DecisionBudgetExceeded, result.Decision,
		"a holder this package does not know still refuses a request it cannot afford")
	assert.Contains(t, result.Reason, "Budget exceeded")
}

// Which credential a request is governed as is settled before governance sees it: a request presenting
// both is resolved to one, or refused, by whoever authenticated it. So an identity on the context
// changes nothing about what the request answers to: the limits settled on its grant are the limits,
// whichever credential produced them.
func TestEvaluateLimitsEnforcesTheSettledLimitsWhicheverCredentialProducedThem(t *testing.T) {
	logger := NewMockLogger()
	exhausted := buildBudgetWithUsage("b-holder", 100.0, 100.0, "1d")
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Budgets: []configstoreTables.TableBudget{*exhausted},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	held := []schemas.Limit{{ID: exhausted.ID, HolderKind: string(grant.LimitHolderVirtualKey), HolderID: "vk1"}}

	evaluate := func(t *testing.T, stamp func(ctx *schemas.BifrostContext)) *EvaluationResult {
		t.Helper()
		ctx := emptyCtx()
		stamp(ctx)
		limits := grant.NewLimits(held, nil)
		ctx.Grant().SetLimits(limits)
		return resolver.evaluateLimits(ctx, &EvaluationRequest{Provider: schemas.OpenAI, Model: "gpt-4o"}, limits)
	}

	t.Run("an exhausted holder refuses the request", func(t *testing.T) {
		assert.Equal(t, DecisionBudgetExceeded, evaluate(t, func(*schemas.BifrostContext) {}).Decision)
	})

	t.Run("and still refuses it when an identity is on the context", func(t *testing.T) {
		// This used to exempt the key's own limits, from the days when a profile was mirrored onto a
		// key and the two were one allowance recorded twice. Dropping them now would simply not charge
		// a budget that is real.
		result := evaluate(t, func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyUserID, "user-1")
		})

		assert.Equal(t, DecisionBudgetExceeded, result.Decision)
	})

	t.Run("only asking for spending checks to be skipped exempts it", func(t *testing.T) {
		result := evaluate(t, func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeySkipBudgetAndRateLimits, true)
		})

		assert.Equal(t, DecisionAllow, result.Decision)
		assert.Contains(t, result.Reason, "spending checks skipped")
	})
}
