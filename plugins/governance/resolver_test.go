package governance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/modelcatalog"
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
		// applies whenever a model is actually present. File-based batches are covered by
		// TestBudgetResolver_EvaluateRequest_FileBatchWithoutModel.
		{"batch with disallowed model is blocked", "gpt-4o-mini", schemas.BatchCreateRequest, DecisionModelBlocked},
		{"batch with allowed model passes", "gpt-4", schemas.BatchCreateRequest, DecisionAllow},
		{"inline batch without a top-level model has no restriction of its own", "", schemas.BatchCreateRequest, DecisionAllow},
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

// TestBudgetResolver_EvaluateRequest_FileBatchWithoutModel: a file- or blob-based batch is held to
// what the provider binds. A key allowing every model (and blacklisting none) has nothing to apply
// and lets it through. A key that restricts models is refused on a provider whose batch API runs the
// rows' own models (OpenAI, Azure) whether or not a hint is present, and on a provider that binds the
// request's model to the job (Gemini) only when that model is missing or disallowed.
func TestBudgetResolver_EvaluateRequest_FileBatchWithoutModel(t *testing.T) {
	tests := []struct {
		name          string
		provider      schemas.ModelProvider
		allowedModels []string
		blacklisted   []string
		model         string
		want          Decision
		reason        string
	}{
		{"openai, every model allowed, no hint", schemas.OpenAI, []string{"*"}, nil, "", DecisionAllow, ""},
		{"openai, every model allowed, hint", schemas.OpenAI, []string{"*"}, nil, "gpt-4", DecisionAllow, ""},
		{"openai, restricted allowlist, no hint", schemas.OpenAI, []string{"gpt-4"}, nil, "", DecisionModelBlocked, "allows every model"},
		{"openai, restricted allowlist, allowed hint is not enough", schemas.OpenAI, []string{"gpt-4"}, nil, "gpt-4", DecisionModelBlocked, "allows every model"},
		{"openai, wildcard with a blacklist", schemas.OpenAI, []string{"*"}, []string{"gpt-4o"}, "", DecisionModelBlocked, "allows every model"},
		{"azure, restricted allowlist, allowed hint is not enough", schemas.Azure, []string{"gpt-4"}, nil, "gpt-4", DecisionModelBlocked, "allows every model"},
		{"gemini, restricted allowlist, no hint", schemas.Gemini, []string{"gemini-2.5-flash"}, nil, "", DecisionModelBlocked, "requires a model"},
		{"gemini, restricted allowlist, allowed hint", schemas.Gemini, []string{"gemini-2.5-flash"}, nil, "gemini-2.5-flash", DecisionAllow, ""},
		{"gemini, restricted allowlist, disallowed hint", schemas.Gemini, []string{"gemini-2.5-flash"}, nil, "gemini-2.5-pro", DecisionModelBlocked, "not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := NewMockLogger()
			providerConfig := buildProviderConfig(string(tt.provider), tt.allowedModels)
			providerConfig.BlacklistedModels = tt.blacklisted
			vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{providerConfig})

			store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
				VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
			}, nil, nil)
			require.NoError(t, err)

			resolver := NewBudgetResolver(store, nil, logger, nil)
			ctx := resolverCtx(store, "sk-bf-test")

			result := evaluateOpaqueBatch(resolver, ctx, tt.provider, tt.model)
			assertDecision(t, tt.want, result)
			if tt.reason != "" {
				assert.Contains(t, result.Reason, tt.reason)
			}
		})
	}
}

// A batch defined by an uploaded file or blob names its models only inside that input. On
// providers whose batch API does not bind the request's model to the job (OpenAI, Azure), the hint
// on the request is not what runs, so a model-restricted key is refused even with an allowed hint.
// Providers that bind the model to the job (Gemini, Vertex, Bedrock) keep the hint check, and
// inline batches and unrestricted keys are unaffected.
func TestPreLLMHookRefusesOpaqueFileBatchesUnderModelRestrictedKeys(t *testing.T) {
	restricted := buildVirtualKeyWithProviders("vk-batch", "sk-bf-batch", "Batch VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"gpt-4"}),
		buildProviderConfig("azure", []string{"gpt-4"}),
		buildProviderConfig("gemini", []string{"gemini-2.5-flash"}),
	})
	unrestricted := buildVirtualKeyWithProviders("vk-batch", "sk-bf-batch", "Batch VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	})
	customRestricted := buildVirtualKeyWithProviders("vk-batch", "sk-bf-batch", "Batch VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("acme-openai", []string{"gpt-4"}),
		buildProviderConfig("acme-azure", []string{"gpt-4"}),
		buildProviderConfig("acme-gemini", []string{"gemini-2.5-flash"}),
	})
	customProviders := map[schemas.ModelProvider]configstore.ProviderConfig{
		"acme-openai": {CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI}},
		"acme-azure":  {CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Azure}},
		"acme-gemini": {CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Gemini}},
	}
	fileBatch := func(provider schemas.ModelProvider, model string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{
			Provider: provider, Model: schemas.Ptr(model), InputFileID: "file-x",
		}}
	}
	fileBatchWithItems := func(provider schemas.ModelProvider, model string) *schemas.BifrostRequest {
		req := fileBatch(provider, model)
		req.BatchCreateRequest.Requests = []schemas.BatchRequestItem{{CustomID: "decoy", Body: map[string]any{"model": model}}}
		return req
	}
	cases := []struct {
		name string
		vk   *configstoreTables.TableVirtualKey
		req  *schemas.BifrostRequest
		want Decision
	}{
		{"openai file batch, allowed hint, restricted key", restricted, fileBatch(schemas.OpenAI, "gpt-4"), DecisionModelBlocked},
		{"azure blob batch, allowed hint, restricted key", restricted, &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{Provider: schemas.Azure, Model: schemas.Ptr("gpt-4"), InputBlob: schemas.Ptr("https://blob.example/in.jsonl")}}, DecisionModelBlocked},
		{"gemini file batch, allowed hint, restricted key", restricted, fileBatch(schemas.Gemini, "gemini-2.5-flash"), DecisionAllow},
		{"gemini file batch, disallowed hint, restricted key", restricted, fileBatch(schemas.Gemini, "gemini-2.5-pro"), DecisionModelBlocked},
		{"openai file batch, unrestricted key", unrestricted, fileBatch(schemas.OpenAI, "gpt-4"), DecisionAllow},
		{"openai inline batch, allowed models, restricted key", restricted, &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{Provider: schemas.OpenAI, Requests: []schemas.BatchRequestItem{{Body: map[string]any{"model": "gpt-4"}}}}}, DecisionAllow},
		// A file beside inline items is still a file batch: no provider runs the inline items when a
		// file is named, OpenAI, Azure and Bedrock submit the file, Gemini and Vertex refuse the mix.
		{"openai file batch beside inline items naming allowed models, restricted key", restricted, fileBatchWithItems(schemas.OpenAI, "gpt-4"), DecisionModelBlocked},
		{"azure file batch beside inline items naming allowed models, restricted key", restricted, fileBatchWithItems(schemas.Azure, "gpt-4"), DecisionModelBlocked},
		{"openai file batch beside inline items, unrestricted key", unrestricted, fileBatchWithItems(schemas.OpenAI, "gpt-4"), DecisionAllow},
		// A custom provider runs its base provider's batch API, so it binds what the base binds.
		{"custom provider on openai, file batch, allowed hint, restricted key", customRestricted, fileBatch("acme-openai", "gpt-4"), DecisionModelBlocked},
		{"custom provider on azure, file batch, allowed hint, restricted key", customRestricted, fileBatch("acme-azure", "gpt-4"), DecisionModelBlocked},
		{"custom provider on openai, inline batch, allowed models, restricted key", customRestricted, &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{Provider: "acme-openai", Requests: []schemas.BatchRequestItem{{Body: map[string]any{"model": "gpt-4"}}}}}, DecisionAllow},
		{"custom provider on gemini, file batch, allowed hint, restricted key", customRestricted, fileBatch("acme-gemini", "gemini-2.5-flash"), DecisionAllow},
		{"custom provider on gemini, file batch, disallowed hint, restricted key", customRestricted, fileBatch("acme-gemini", "gemini-2.5-pro"), DecisionModelBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plugin := newAccessTestPlugin(t, tc.vk, nil)
			if tc.vk == customRestricted {
				plugin = newCustomProviderTestPlugin(t, tc.vk, customProviders)
			}
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx("sk-bf-batch"), tc.req)
			require.NoError(t, err)
			assertShortCircuitDecision(t, tc.want, shortCircuit)
		})
	}
}

// assertShortCircuitDecision checks a PreLLMHook outcome against the decision a case expects: an
// allow is no short circuit at all, and a refusal carries an error typed with the decision, so a
// short circuit that lost its type cannot pass as an allow.
func assertShortCircuitDecision(t *testing.T, want Decision, sc *schemas.LLMPluginShortCircuit) {
	t.Helper()
	if want == DecisionAllow {
		if sc != nil {
			t.Fatalf("want an allow, got a short circuit: %+v", sc.Error)
		}
		return
	}
	require.NotNil(t, sc, "want %s, got an allow", want)
	require.NotNil(t, sc.Error, "want %s, the short circuit carries no error", want)
	require.NotNil(t, sc.Error.Type, "want %s, the short circuit's error has no type: %s", want, sc.Error.Error)
	assert.Equal(t, string(want), *sc.Error.Type)
}

// newCustomProviderTestPlugin is newAccessTestPlugin over a governance plugin whose in-memory
// store knows the given configured providers, the way the transport's does.
func newCustomProviderTestPlugin(t *testing.T, vk *configstoreTables.TableVirtualKey, providers map[schemas.ModelProvider]configstore.ProviderConfig) *GovernancePlugin {
	t.Helper()
	logger := NewMockLogger()
	inMemory := &mockInMemoryStore{configuredProviders: providers}
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, inMemory)
	require.NoError(t, err)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, inMemory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin
}

// A passthrough body is forwarded as the provider reads it, and a Runware task array names a
// model per task that the request itself does not carry. Under a key that restricts models on
// the provider, every model the body names is checked, and a body naming none is refused:
// nothing visible to governance may stand in for what the provider will run. A key allowing
// every model of the provider is untouched.
func TestPreLLMHookChecksEveryModelAPassthroughBodyNames(t *testing.T) {
	restricted := buildVirtualKeyWithProviders("vk-pt", "sk-bf-pt", "Passthrough VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("runware", []string{"runware:100@1"}),
	})
	unrestricted := buildVirtualKeyWithProviders("vk-pt", "sk-bf-pt", "Passthrough VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("runware", []string{"*"}),
	})
	passthrough := func(requestType schemas.RequestType, model, body string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{RequestType: requestType, PassthroughRequest: &schemas.BifrostPassthroughRequest{
			Provider: schemas.Runware, Model: model, Method: "POST", Path: "/v1", Body: []byte(body),
		}}
	}
	const allowedOnly = `[{"taskType":"imageInference","model":"runware:100@1"}]`
	const disallowedOnly = `[{"taskType":"imageInference","model":"runware:101@1"}]`
	const mixed = `[{"taskType":"imageInference","model":"runware:100@1"},{"taskType":"imageInference","model":"runware:101@1"}]`
	const modelless = `[{"taskType":"getResponse","taskUUID":"00000000-0000-0000-0000-000000000000"}]`

	cases := []struct {
		name string
		vk   *configstoreTables.TableVirtualKey
		req  *schemas.BifrostRequest
		want Decision
	}{
		// The router hands the array's single model up as the request model.
		{"array naming the allowed model, restricted key", restricted, passthrough(schemas.PassthroughRequest, "runware:100@1", allowedOnly), DecisionAllow},
		{"array naming a disallowed model, restricted key", restricted, passthrough(schemas.PassthroughRequest, "runware:101@1", disallowedOnly), DecisionModelBlocked},
		// Tasks naming different models leave the request model empty; the body is read.
		{"mixed array with the allowed model first, restricted key", restricted, passthrough(schemas.PassthroughRequest, "", mixed), DecisionModelBlocked},
		{"mixed array, streaming, restricted key", restricted, passthrough(schemas.PassthroughStreamRequest, "", mixed), DecisionModelBlocked},
		// Nothing names a model: the restriction cannot be checked, so it is not skipped.
		{"model-less array, restricted key", restricted, passthrough(schemas.PassthroughRequest, "", modelless), DecisionModelBlocked},
		{"object body naming no model, restricted key", restricted, passthrough(schemas.PassthroughRequest, "", `{"prompt":"x"}`), DecisionModelBlocked},
		{"array naming a disallowed model, unrestricted key", unrestricted, passthrough(schemas.PassthroughRequest, "runware:101@1", disallowedOnly), DecisionAllow},
		{"mixed array, unrestricted key", unrestricted, passthrough(schemas.PassthroughRequest, "", mixed), DecisionAllow},
		{"model-less array, unrestricted key", unrestricted, passthrough(schemas.PassthroughRequest, "", modelless), DecisionAllow},
		{"object body naming no model, unrestricted key", unrestricted, passthrough(schemas.PassthroughRequest, "", `{"prompt":"x"}`), DecisionAllow},
	}
	// GenAI and Vertex bodies name a model by its resource name ("models/<id>",
	// "publishers/google/models/<id>"), which the router reduces to the bare id it hands up as the
	// request model. The body is read the same way, so an allowed model is not refused for being
	// spelled as a resource, and a resource naming another model is still that other model.
	geminiKey := buildVirtualKeyWithProviders("vk-pt", "sk-bf-pt", "Passthrough VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("gemini", []string{"gemini-embedding-001"}),
		buildProviderConfig("vertex", []string{"gemini-embedding-001"}),
	})
	genai := func(provider schemas.ModelProvider, model, body string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{RequestType: schemas.PassthroughRequest, PassthroughRequest: &schemas.BifrostPassthroughRequest{
			Provider: provider, Model: model, Method: "POST", Path: "/v1beta/models/" + model + ":embedContent", Body: []byte(body),
		}}
	}
	cases = append(cases, []struct {
		name string
		vk   *configstoreTables.TableVirtualKey
		req  *schemas.BifrostRequest
		want Decision
	}{
		{"genai body naming the allowed model as a resource", geminiKey, genai(schemas.Gemini, "gemini-embedding-001", `{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"x"}]}}`), DecisionAllow},
		{"vertex body naming the allowed model as a publisher resource", geminiKey, genai(schemas.Vertex, "gemini-embedding-001", `{"model":"projects/p/locations/l/publishers/google/models/gemini-embedding-001"}`), DecisionAllow},
		{"genai body naming another model as a resource", geminiKey, genai(schemas.Gemini, "gemini-embedding-001", `{"model":"models/gemini-2.5-pro"}`), DecisionModelBlocked},
	}...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plugin := newAccessTestPlugin(t, tc.vk, nil)
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx("sk-bf-pt"), tc.req)
			require.NoError(t, err)
			assertShortCircuitDecision(t, tc.want, shortCircuit)
		})
	}

	// No virtual key presented: passthrough stays unrestricted, as every request type is.
	plugin := newAccessTestPlugin(t, restricted, nil)
	_, shortCircuit, err := plugin.PreLLMHook(emptyCtx(), passthrough(schemas.PassthroughRequest, "", mixed))
	require.NoError(t, err)
	assert.Nil(t, shortCircuit)
}

// An advisor tool names a second model that the provider runs and bills within the same request,
// so that model is held to the key's model restriction just as the request's own model is. Each
// ingress that can carry the tool is driven: the native Messages body (converted the way the
// /anthropic/v1/messages route converts it), the neutral Responses tool (/v1/responses and the Go
// SDK), and an Anthropic passthrough body. A request without the tool is untouched.
func TestPreLLMHookChecksModelsNamedByServerTools(t *testing.T) {
	const executor = "claude-haiku-4-5-20251001"
	const advisor = "claude-opus-4-8"
	restricted := buildVirtualKeyWithProviders("vk-adv", "sk-bf-adv", "Advisor VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("anthropic", []string{executor}),
	})
	permitsBoth := buildVirtualKeyWithProviders("vk-adv", "sk-bf-adv", "Advisor VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("anthropic", []string{executor, advisor}),
	})

	messages := func(t *testing.T, advisorModel string) *schemas.BifrostRequest {
		t.Helper()
		body := `{"model":"anthropic/` + executor + `","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]`
		if advisorModel != "" {
			body += `,"tools":[{"type":"advisor_20260301","name":"advisor","model":"` + advisorModel + `"}]`
		}
		body += `}`
		var native anthropic.AnthropicMessageRequest
		require.NoError(t, json.Unmarshal([]byte(body), &native))
		converted := native.ToBifrostResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
		require.NotNil(t, converted)
		return &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: converted}
	}
	responses := func(t *testing.T, advisorModel string) *schemas.BifrostRequest {
		t.Helper()
		req := &schemas.BifrostResponsesRequest{Provider: schemas.Anthropic, Model: executor, Params: &schemas.ResponsesParameters{}}
		if advisorModel != "" {
			require.NoError(t, json.Unmarshal([]byte(`[{"type":"advisor","model":"`+advisorModel+`"}]`), &req.Params.Tools))
		}
		return &schemas.BifrostRequest{RequestType: schemas.ResponsesStreamRequest, ResponsesRequest: req}
	}
	passthrough := func(_ *testing.T, advisorModel string) *schemas.BifrostRequest {
		body := `{"model":"` + executor + `","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"advisor_20260301","name":"advisor","model":"` + advisorModel + `"}]}`
		return &schemas.BifrostRequest{RequestType: schemas.PassthroughRequest, PassthroughRequest: &schemas.BifrostPassthroughRequest{
			Provider: schemas.Anthropic, Model: executor, Method: "POST", Path: "/v1/messages", Body: []byte(body),
		}}
	}

	advisorTool := func(advisorModel string) map[string]any {
		return map[string]any{"type": "advisor_20260301", "name": "advisor", "model": advisorModel}
	}
	// x-bf-passthrough-extra-params merges ExtraParams["tools"] onto the wire after governance runs.
	responsesExtraParams := func(_ *testing.T, advisorModel string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic, Model: executor,
			Params: &schemas.ResponsesParameters{ExtraParams: map[string]any{"tools": []any{advisorTool(advisorModel)}}},
		}}
	}
	chatExtraParams := func(_ *testing.T, advisorModel string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic, Model: executor,
			Params: &schemas.ChatParameters{ExtraParams: map[string]any{"tools": json.RawMessage(`[{"type":"advisor_20260301","name":"advisor","model":"` + advisorModel + `"}]`)}},
		}}
	}
	// Raw forwarding sends RawRequestBody, not the parsed parameters; a tool the parser does not
	// map (a later advisor version here) reaches the provider only through the raw body.
	rawTools := func(advisorModel string) string {
		return `[{"type":"advisor_20270101","name":"advisor","model":"` + advisorModel + `"}]`
	}
	responsesRawBody := func(_ *testing.T, advisorModel string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{RequestType: schemas.ResponsesStreamRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic, Model: executor, Params: &schemas.ResponsesParameters{},
			RawRequestBody: []byte(`{"model":"` + executor + `","max_tokens":1024,"messages":[],"tools":` + rawTools(advisorModel) + `}`),
		}}
	}
	chatRawBody := func(_ *testing.T, advisorModel string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic, Model: executor,
			RawRequestBody: []byte(`{"model":"` + executor + `","messages":[],"tools":` + rawTools(advisorModel) + `}`),
		}}
	}
	// A JSON parser that keeps the last duplicate key sees the second tools array and the second
	// model, so every occurrence is checked; escaped key and type spellings decode to the same names.
	passthroughDuplicateKeys := func(_ *testing.T, advisorModel string) *schemas.BifrostRequest {
		body := `{"model":"` + executor + `","messages":[],"tools":[],"t\u006fols":[{"type":"\u0061dvisor_20260301","name":"advisor","model":"` + executor + `","model":"` + advisorModel + `"}]}`
		return &schemas.BifrostRequest{RequestType: schemas.PassthroughRequest, PassthroughRequest: &schemas.BifrostPassthroughRequest{
			Provider: schemas.Anthropic, Model: executor, Method: "POST", Path: "/v1/messages", Body: []byte(body),
		}}
	}
	inlineBatch := func(_ *testing.T, advisorModel string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{
			Provider: schemas.Anthropic,
			Requests: []schemas.BatchRequestItem{{CustomID: "a", Params: map[string]any{
				"model": executor, "max_tokens": 1024, "messages": []any{}, "tools": []any{advisorTool(advisorModel)},
			}}},
		}}
	}
	batchRawBody := func(_ *testing.T, advisorModel string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{
			Provider:       schemas.Anthropic,
			Requests:       []schemas.BatchRequestItem{{CustomID: "a", Params: map[string]any{"model": executor}}},
			RawRequestBody: []byte(`{"requests":[{"custom_id":"a","params":{"model":"` + executor + `","max_tokens":1024,"messages":[],"tools":` + rawTools(advisorModel) + `}}]}`),
		}}
	}

	routes := []struct {
		name  string
		build func(t *testing.T, advisorModel string) *schemas.BifrostRequest
	}{
		{"anthropic messages", messages},
		{"responses", responses},
		{"anthropic passthrough", passthrough},
		{"responses extra params", responsesExtraParams},
		{"chat extra params", chatExtraParams},
		{"responses raw body", responsesRawBody},
		{"chat raw body", chatRawBody},
		{"passthrough duplicate and escaped keys", passthroughDuplicateKeys},
		{"anthropic inline batch", inlineBatch},
		{"anthropic batch raw body", batchRawBody},
	}
	for _, route := range routes {
		t.Run(route.name+": advisor model outside the key's models is refused", func(t *testing.T) {
			plugin := newAccessTestPlugin(t, restricted, nil)
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx("sk-bf-adv"), route.build(t, advisor))
			require.NoError(t, err)
			assertShortCircuitDecision(t, DecisionModelBlocked, shortCircuit)
			assert.Contains(t, shortCircuit.Error.Error.Message, advisor)
		})
		t.Run(route.name+": provider-prefixed advisor model is judged on its bare id", func(t *testing.T) {
			plugin := newAccessTestPlugin(t, restricted, nil)
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx("sk-bf-adv"), route.build(t, "anthropic/"+advisor))
			require.NoError(t, err)
			assertShortCircuitDecision(t, DecisionModelBlocked, shortCircuit)
		})
		t.Run(route.name+": advisor model the key permits is allowed", func(t *testing.T) {
			plugin := newAccessTestPlugin(t, permitsBoth, nil)
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx("sk-bf-adv"), route.build(t, advisor))
			require.NoError(t, err)
			assertShortCircuitDecision(t, DecisionAllow, shortCircuit)
		})
		t.Run(route.name+": advisor naming the executor model is allowed", func(t *testing.T) {
			plugin := newAccessTestPlugin(t, restricted, nil)
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx("sk-bf-adv"), route.build(t, executor))
			require.NoError(t, err)
			assertShortCircuitDecision(t, DecisionAllow, shortCircuit)
		})
	}
	for _, route := range routes[:2] {
		t.Run(route.name+": no server tool, restricted key", func(t *testing.T) {
			plugin := newAccessTestPlugin(t, restricted, nil)
			_, shortCircuit, err := plugin.PreLLMHook(presentCtx("sk-bf-adv"), route.build(t, ""))
			require.NoError(t, err)
			assertShortCircuitDecision(t, DecisionAllow, shortCircuit)
		})
	}
}

// An advisor model with a budget or rate limit of its own is refused rather than checked and never
// charged: the request's usage is charged against the executor model's limits alone.
func TestPreLLMHookRefusesServerToolModelsWithTheirOwnLimits(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk-adv", "sk-bf-adv", "Advisor VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("anthropic", []string{"*"}),
	})
	limited := buildRateLimit("rl-opus", 1000, 1000)
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		ModelConfigs: []configstoreTables.TableModelConfig{*buildModelConfig("mc-opus", "claude-opus-4-8", nil, nil, limited)},
		RateLimits:   []configstoreTables.TableRateLimit{*limited},
	}, nil, nil)
	require.NoError(t, err)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })

	req := &schemas.BifrostResponsesRequest{Provider: schemas.Anthropic, Model: "claude-haiku-4-5-20251001", Params: &schemas.ResponsesParameters{}}
	require.NoError(t, json.Unmarshal([]byte(`[{"type":"advisor","model":"claude-opus-4-8"}]`), &req.Params.Tools))
	_, shortCircuit, err := plugin.PreLLMHook(presentCtx("sk-bf-adv"), &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: req})
	require.NoError(t, err)
	assertShortCircuitDecision(t, DecisionModelLimitsUnattributable, shortCircuit)
	assert.Contains(t, shortCircuit.Error.Error.Message, "claude-opus-4-8")
}

// BenchmarkPreLLMHookServerToolModels measures the governance pre-hook on Responses requests with no
// tools, with a function tool only (the common case, which must not pay for the server-tool scan),
// and with an advisor tool naming a second model.
func BenchmarkPreLLMHookServerToolModels(b *testing.B) {
	vk := buildVirtualKeyWithProviders("vk-adv", "sk-bf-adv", "Advisor VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("anthropic", []string{"*"}),
	})
	plugin := newAccessTestPlugin(b, vk, nil)
	const functionTool = `[{"type":"function","name":"f","parameters":{"type":"object"}}]`
	rawBody := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1024,"messages":[{"role":"user","content":"` +
		strings.Repeat("x", 4096) + `"}],"tools":[{"name":"f","input_schema":{"type":"object"}}]}`)
	cases := []struct {
		name, tools string
		raw         []byte
	}{
		{"no_tools", "", nil},
		{"function_tool", functionTool, nil},
		{"advisor_tool", `[{"type":"function","name":"f","parameters":{"type":"object"}},{"type":"advisor","model":"claude-opus-4-8"}]`, nil},
		// Raw forwarding (native route with OAuth or Claude Code passthrough) also scans the body it sends.
		{"raw_body_function_tool", functionTool, rawBody},
	}
	for _, tc := range cases {
		params := &schemas.ResponsesParameters{}
		if tc.tools != "" {
			if err := json.Unmarshal([]byte(tc.tools), &params.Tools); err != nil {
				b.Fatal(err)
			}
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				req := &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{
					Provider: schemas.Anthropic, Model: "claude-haiku-4-5-20251001", Params: params, RawRequestBody: tc.raw,
				}}
				if _, _, err := plugin.PreLLMHook(presentCtx("sk-bf-adv"), req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestBudgetResolver_EvaluateVirtualKey_ActiveNoExpiry verifies that a VK
// with no expiry is allowed.
// A mixed Runware task array leaves the request model empty, so the request is charged against
// the limits its own empty model settles. A body model with limits of its own (a model config's
// budget or rate limit) would then be checked but never charged, so such a body is refused and the
// caller sends one model per request; bodies whose models carry no limits of their own still pass.
func TestPreLLMHookRefusesMixedPassthroughModelsWithTheirOwnLimits(t *testing.T) {
	newPlugin := func(t *testing.T, modelConfigs []configstoreTables.TableModelConfig, rateLimits []configstoreTables.TableRateLimit) *GovernancePlugin {
		t.Helper()
		vk := buildVirtualKeyWithProviders("vk-pt", "sk-bf-pt", "Passthrough VK", []configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("runware", []string{"*"}),
		})
		logger := NewMockLogger()
		store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
			VirtualKeys: []configstoreTables.TableVirtualKey{*vk}, ModelConfigs: modelConfigs, RateLimits: rateLimits,
		}, nil, nil)
		require.NoError(t, err)
		plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
		return plugin
	}
	passthrough := func(model, body string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{RequestType: schemas.PassthroughRequest, PassthroughRequest: &schemas.BifrostPassthroughRequest{
			Provider: schemas.Runware, Model: model, Method: "POST", Path: "/v1", Body: []byte(body),
		}}
	}
	const mixed = `[{"taskType":"imageInference","model":"runware:100@1"},{"taskType":"imageInference","model":"runware:101@1"}]`
	const single = `[{"taskType":"imageInference","model":"runware:101@1"}]`

	limited := buildRateLimit("rl-101", 1000, 1000)
	withModelLimit := newPlugin(t, []configstoreTables.TableModelConfig{*buildModelConfig("mc-101", "runware:101@1", nil, nil, limited)}, []configstoreTables.TableRateLimit{*limited})
	withoutModelLimit := newPlugin(t, nil, nil)

	_, shortCircuit, err := withModelLimit.PreLLMHook(presentCtx("sk-bf-pt"), passthrough("", mixed))
	require.NoError(t, err)
	assertShortCircuitDecision(t, DecisionModelLimitsUnattributable, shortCircuit)
	assert.Contains(t, shortCircuit.Error.Error.Message, "runware:101@1", "the refusal names the model whose limits could not be charged")

	_, shortCircuit, err = withModelLimit.PreLLMHook(presentCtx("sk-bf-pt"), passthrough("runware:101@1", single))
	require.NoError(t, err)
	assertShortCircuitDecision(t, DecisionAllow, shortCircuit)

	_, shortCircuit, err = withoutModelLimit.PreLLMHook(presentCtx("sk-bf-pt"), passthrough("", mixed))
	require.NoError(t, err)
	assertShortCircuitDecision(t, DecisionAllow, shortCircuit)

	// A model config scoped to the key itself is the model's own limit just the same.
	keyLimited := buildRateLimit("rl-101-vk", 1000, 1000)
	keyScoped := buildModelConfig("mc-101-vk", "runware:101@1", nil, nil, keyLimited)
	keyScoped.Scope = configstoreTables.ModelConfigScopeVirtualKey
	keyScoped.ScopeID = bifrost.Ptr("vk-pt")
	withKeyModelLimit := newPlugin(t, []configstoreTables.TableModelConfig{*keyScoped}, []configstoreTables.TableRateLimit{*keyLimited})
	_, shortCircuit, err = withKeyModelLimit.PreLLMHook(presentCtx("sk-bf-pt"), passthrough("", mixed))
	require.NoError(t, err)
	assertShortCircuitDecision(t, DecisionModelLimitsUnattributable, shortCircuit)
}

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

// BenchmarkPreLLMHookBatchCreate measures the governance pre-hook for batch creates under a
// virtual key: a file-based batch, whose models are not visible on the request, and an inline
// batch whose items are each evaluated.
// providerLookupCountingStore counts how a model check reads the configured providers: the whole
// map, or the one provider it needs.
type providerLookupCountingStore struct {
	mockInMemoryStore
	wholeMap, single int
}

func (s *providerLookupCountingStore) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	s.wholeMap++
	return s.mockInMemoryStore.GetConfiguredProviders()
}

func (s *providerLookupCountingStore) GetConfiguredProvider(provider schemas.ModelProvider) (configstore.ProviderConfig, bool) {
	s.single++
	config, ok := s.configuredProviders[provider]
	return config, ok
}

// A model check runs on every request a virtual key with an allowed-models list makes, and needs
// one provider's configuration. A store that can hand back that one provider is asked for it, so
// the check never copies the whole provider map.
func TestModelMatcherReadsOnlyTheProviderItChecks(t *testing.T) {
	store := &providerLookupCountingStore{mockInMemoryStore: mockInMemoryStore{configuredProviders: map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {}, schemas.Anthropic: {}, schemas.Gemini: {},
	}}}
	plugin := &GovernancePlugin{modelCatalog: modelcatalog.NewTestCatalog(nil), inMemoryStore: store}

	matcher := plugin.modelMatcher()
	require.NotNil(t, matcher)
	assert.True(t, matcher("openai", "gpt-4o", []string{"gpt-4o"}))
	assert.Zero(t, store.wholeMap, "a model check does not read the whole provider map")
	assert.Equal(t, 1, store.single, "a model check reads the one provider it checks")

	// Asking whether a provider is built on Vertex needs that provider's base type only.
	assert.False(t, plugin.builtOnVertex("openai"))
	assert.Zero(t, store.wholeMap, "the Vertex check does not read the whole provider map")

	// So does asking whether a passthrough body names models by resource name.
	assert.True(t, plugin.namesModelsAsResources(schemas.Gemini))
	assert.Zero(t, store.wholeMap, "the resource-name check does not read the whole provider map")
}

// A chat request is the common case the provider snapshot must not tax. Through the whole hook
// chain, with a model catalog and a key whose allowed-models list makes the model check run, the
// configured providers are read one at a time and the whole map is never copied.
func TestChatRequestNeverCopiesTheProviderMap(t *testing.T) {
	store := &providerLookupCountingStore{mockInMemoryStore: mockInMemoryStore{configuredProviders: map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {}, schemas.Anthropic: {}, schemas.Gemini: {},
	}}}
	vk := buildVirtualKeyWithProviders("vk-chat", mcpTestVKValue, "chat-vk", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"gpt-4o"}),
	})
	logger := NewMockLogger()
	local, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, store)
	require.NoError(t, err)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, local, nil, modelcatalog.NewTestCatalog(nil), nil, store)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })

	ctx := presentCtx(mcpTestVKValue)
	req := &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o"}}
	require.NoError(t, plugin.PreRequestHook(ctx, req))
	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "an allowed model is admitted")
	_, _, err = plugin.PostLLMHook(ctx, &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI},
	}}, nil)
	require.NoError(t, err)

	assert.Positive(t, store.single, "the model check read the provider it checks")
	assert.Zero(t, store.wholeMap, "a chat request never copies the whole provider map")
}

func BenchmarkPreLLMHookBatchCreate(b *testing.B) {
	plugin := newAccessTestPlugin(b, buildVKForMCPStamping(nil), nil)
	model := "gpt-4o"
	cases := []struct {
		name  string
		build func() *schemas.BifrostRequest
	}{
		{"file_batch", func() *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{Provider: schemas.OpenAI, Model: &model, InputFileID: "file-bench"}}
		}},
		{"inline_batch", func() *schemas.BifrostRequest {
			return &schemas.BifrostRequest{RequestType: schemas.BatchCreateRequest, BatchCreateRequest: &schemas.BifrostBatchCreateRequest{Provider: schemas.OpenAI, Model: &model, Requests: []schemas.BatchRequestItem{
				{CustomID: "a", Body: map[string]any{"model": "gpt-4o"}},
				{CustomID: "b", Body: map[string]any{"model": "gpt-4o-mini"}},
			}}}
		}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, _, err := plugin.PreLLMHook(presentCtx(mcpTestVKValue), tc.build()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPreLLMHookPassthroughBodyModels measures the governance pre-hook for passthrough
// requests whose body names the models the provider runs: a Runware task array with one model,
// one mixing two models, and a GenAI body naming its model as a resource.
func BenchmarkPreLLMHookPassthroughBodyModels(b *testing.B) {
	vk := buildVirtualKeyWithProviders("vk-pt", "sk-bf-pt", "Passthrough VK", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("runware", []string{"*"}),
		buildProviderConfig("gemini", []string{"*"}),
	})
	plugin := newAccessTestPlugin(b, vk, nil)
	cases := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		body     string
	}{
		{"runware_single_model", schemas.Runware, "runware:100@1", `[{"taskType":"imageInference","taskUUID":"00000000-0000-0000-0000-000000000001","model":"runware:100@1","positivePrompt":"x","width":512,"height":512}]`},
		{"runware_mixed_models", schemas.Runware, "", `[{"taskType":"imageInference","model":"runware:100@1","positivePrompt":"x"},{"taskType":"imageInference","model":"runware:101@1","positivePrompt":"y"}]`},
		{"genai_resource_model", schemas.Gemini, "gemini-embedding-001", `{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"x"}]}}`},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				req := &schemas.BifrostRequest{RequestType: schemas.PassthroughRequest, PassthroughRequest: &schemas.BifrostPassthroughRequest{Provider: tc.provider, Model: tc.model, Method: "POST", Path: "/v1", Body: []byte(tc.body)}}
				if _, _, err := plugin.PreLLMHook(presentCtx("sk-bf-pt"), req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
