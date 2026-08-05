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

// TestBudgetResolver_EvaluateRequest_AllowedRequest tests happy path
func TestBudgetResolver_EvaluateRequest_AllowedRequest(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionAllow, result)
	assertVirtualKeyFound(t, result)
}

// TestBudgetResolver_EvaluateRequest_VirtualKeyNotFound tests missing VK
func TestBudgetResolver_EvaluateRequest_VirtualKeyNotFound(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-nonexistent", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionVirtualKeyNotFound, result)
}

// TestBudgetResolver_EvaluateRequest_VirtualKeyBlocked tests inactive VK
func TestBudgetResolver_EvaluateRequest_VirtualKeyBlocked(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", false) // Inactive

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionVirtualKeyBlocked, result)
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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	// Try to use OpenAI (not allowed)
	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionProviderBlocked, result)
	assertVirtualKeyFound(t, result)
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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	// OpenAI is not in the allowlist, but ListModelsRequest must not be provider-blocked.
	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ListModelsRequest, false)

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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	// Try to use gpt-4o-mini (not in allowed list)
	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4o-mini", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionModelBlocked, result)
}

// TestGovernancePlugin_EvaluateGovernanceRequest_DirectKeySatisfiesMandatoryAuth verifies direct provider keys satisfy mandatory auth after transport validation.
func TestGovernancePlugin_EvaluateGovernanceRequest_DirectKeySatisfiesMandatoryAuth(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	mandatory := true
	plugin := &GovernancePlugin{
		store:         store,
		resolver:      NewBudgetResolver(store, nil, logger, nil),
		isVkMandatory: &mandatory,
		isEnterprise:  true,
	}

	ctx := &schemas.BifrostContext{}
	ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{
		ID:    "header-provided",
		Name:  "header-provided",
		Value: schemas.SecretVar{Val: "sk-real-openai-key"},
	})

	result, bifrostErr := plugin.EvaluateGovernanceRequest(ctx, &EvaluationRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
	}, schemas.PassthroughRequest)

	require.Nil(t, bifrostErr)
	assertDecision(t, DecisionAllow, result)
}

// TestGovernancePlugin_EvaluateGovernanceRequest_HeaderWithoutContextDoesNotSatisfyMandatoryAuth verifies callers cannot spoof direct-key auth with only a request header.
func TestGovernancePlugin_EvaluateGovernanceRequest_HeaderWithoutContextDoesNotSatisfyMandatoryAuth(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	mandatory := true
	plugin := &GovernancePlugin{
		store:         store,
		resolver:      NewBudgetResolver(store, nil, logger, nil),
		isVkMandatory: &mandatory,
		isEnterprise:  true,
	}

	_, bifrostErr := plugin.EvaluateGovernanceRequest(&schemas.BifrostContext{}, &EvaluationRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
	}, schemas.PassthroughRequest)

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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionTokenLimited, result)
	assertRateLimitInfo(t, result)
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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

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
	}, nil)
	require.NoError(t, err)

	// Reset expired rate limits (simulating ticker behavior)
	expiredRateLimits := store.ResetExpiredRateLimitsInMemory(context.Background(), true)
	err = store.ResetExpiredRateLimits(context.Background(), expiredRateLimits)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	// Test: All under limit should pass
	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
	assertDecision(t, DecisionAllow, result)

	// Test: VK budget exceeds should fail
	// Get the governance data to update the budget directly
	governanceData := store.GetGovernanceData(context.Background())
	vkBudgetToUpdate := governanceData.Budgets["vk-budget"]
	if vkBudgetToUpdate != nil {
		vkBudgetToUpdate.CurrentUsage = 100.0
		store.budgets.Store("vk-budget", vkBudgetToUpdate)
	}
	result = resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionTokenLimited, result)
	assertRateLimitInfo(t, result)
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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

	assertDecision(t, DecisionRateLimited, result)
	assert.Contains(t, result.Reason, "rate limit")
}

// TestBudgetResolver_IsProviderAllowed tests provider filtering logic
func TestBudgetResolver_IsProviderAllowed(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)

	tests := []struct {
		name            string
		vk              *configstoreTables.TableVirtualKey
		provider        schemas.ModelProvider
		shouldBeAllowed bool
	}{
		{
			name:            "No provider configs (none allowed - deny-by-default)",
			vk:              buildVirtualKey("vk1", "sk-bf-test", "Test", true),
			provider:        schemas.OpenAI,
			shouldBeAllowed: false,
		},
		{
			name: "Provider in allowlist",
			vk: buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test",
				[]configstoreTables.TableVirtualKeyProviderConfig{
					buildProviderConfig("openai", []string{"gpt-4"}),
				}),
			provider:        schemas.OpenAI,
			shouldBeAllowed: true,
		},
		{
			name: "Provider not in allowlist",
			vk: buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test",
				[]configstoreTables.TableVirtualKeyProviderConfig{
					buildProviderConfig("anthropic", []string{"claude-3-sonnet"}),
				}),
			provider:        schemas.OpenAI,
			shouldBeAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed := resolver.isProviderAllowed(tt.vk, tt.provider)
			assert.Equal(t, tt.shouldBeAllowed, allowed)
		})
	}
}

// TestBudgetResolver_IsModelAllowed tests model filtering logic
func TestBudgetResolver_IsModelAllowed(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)

	tests := []struct {
		name            string
		vk              *configstoreTables.TableVirtualKey
		provider        schemas.ModelProvider
		model           string
		shouldBeAllowed bool
	}{
		{
			name:            "No provider configs (no models allowed - deny-by-default)",
			vk:              buildVirtualKey("vk1", "sk-bf-test", "Test", true),
			provider:        schemas.OpenAI,
			model:           "gpt-4",
			shouldBeAllowed: false,
		},
		{
			name: "Wildcard allowed models (all models allowed)",
			vk: buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test",
				[]configstoreTables.TableVirtualKeyProviderConfig{
					buildProviderConfig("openai", []string{"*"}), // ["*"] = allow all
				}),
			provider:        schemas.OpenAI,
			model:           "gpt-4",
			shouldBeAllowed: true,
		},
		{
			name: "Empty allowed models (deny all)",
			vk: buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test",
				[]configstoreTables.TableVirtualKeyProviderConfig{
					buildProviderConfig("openai", []string{}), // [] = deny all
				}),
			provider:        schemas.OpenAI,
			model:           "gpt-4",
			shouldBeAllowed: false,
		},
		{
			name: "Model in allowlist",
			vk: buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test",
				[]configstoreTables.TableVirtualKeyProviderConfig{
					buildProviderConfig("openai", []string{"gpt-4", "gpt-4-turbo"}),
				}),
			provider:        schemas.OpenAI,
			model:           "gpt-4",
			shouldBeAllowed: true,
		},
		{
			name: "Model not in allowlist",
			vk: buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test",
				[]configstoreTables.TableVirtualKeyProviderConfig{
					buildProviderConfig("openai", []string{"gpt-4", "gpt-4-turbo"}),
				}),
			provider:        schemas.OpenAI,
			model:           "gpt-4o-mini",
			shouldBeAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed := resolver.isModelAllowed(tt.vk, tt.provider, tt.model)
			assert.Equal(t, tt.shouldBeAllowed, allowed)
		})
	}
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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)

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
		// Scoping guard: batch is model-not-required and not passthrough, so its model is never
		// filtered even when set to a disallowed value (behavior unchanged by the passthrough fix).
		{"batch with disallowed model is not filtered", "gpt-4o-mini", schemas.BatchCreateRequest, DecisionAllow},
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
			}, nil)
			require.NoError(t, err)

			resolver := NewBudgetResolver(store, nil, logger, nil)
			ctx := &schemas.BifrostContext{}

			result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, tt.model, tt.requestType, false)
			assertDecision(t, tt.want, result)
		})
	}
}

// TestBudgetResolver_EvaluateVirtualKeyRequest_ActiveNoExpiry verifies that a VK
// with no expiry is allowed.
func TestBudgetResolver_EvaluateVirtualKeyRequest_ActiveNoExpiry(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
	assertDecision(t, DecisionAllow, result)
}

// TestBudgetResolver_EvaluateVirtualKeyRequest_FutureExpiry verifies that a VK
// with a future expiry is allowed.
func TestBudgetResolver_EvaluateVirtualKeyRequest_FutureExpiry(t *testing.T) {
	logger := NewMockLogger()
	future := time.Now().UTC().Add(time.Hour)
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	vk.ExpiresAt = &future
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
	assertDecision(t, DecisionAllow, result)
}

// TestBudgetResolver_EvaluateVirtualKeyRequest_ExpiredKey verifies that an
// active VK with a past expiry is blocked with DecisionVirtualKeyBlocked.
func TestBudgetResolver_EvaluateVirtualKeyRequest_ExpiredKey(t *testing.T) {
	logger := NewMockLogger()
	past := time.Now().UTC().Add(-time.Second)
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	vk.ExpiresAt = &past
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
	assertDecision(t, DecisionVirtualKeyBlocked, result)
	assert.Contains(t, result.Reason, "expired")
}

// TestBudgetResolver_EvaluateVirtualKeyRequest_InactiveWithFutureExpiry verifies
// that an inactive VK is blocked as inactive, not expired, even when it has a
// future expiry.
func TestBudgetResolver_EvaluateVirtualKeyRequest_InactiveWithFutureExpiry(t *testing.T) {
	logger := NewMockLogger()
	future := time.Now().UTC().Add(time.Hour)
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", false)
	vk.ExpiresAt = &future

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
	assertDecision(t, DecisionVirtualKeyBlocked, result)
	assert.Contains(t, result.Reason, "inactive")
}
