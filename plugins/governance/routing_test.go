package governance

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/cel-go/cel"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildScopeChain_GlobalOnly tests scope chain with no VirtualKey
func TestBuildScopeChain_GlobalOnly(t *testing.T) {
	chain := buildScopeChain(nil)

	require.Equal(t, 1, len(chain))
	assert.Equal(t, "global", chain[0].ScopeName)
	assert.Equal(t, "", chain[0].ScopeID)
}

// TestBuildScopeChain_VirtualKeyOnly tests scope chain with only VirtualKey
func TestBuildScopeChain_VirtualKeyOnly(t *testing.T) {
	vk := &configstoreTables.TableVirtualKey{
		ID:   "vk-123",
		Name: "test-vk",
	}

	chain := buildScopeChain(vk)

	require.Equal(t, 2, len(chain))
	assert.Equal(t, "virtual_key", chain[0].ScopeName)
	assert.Equal(t, "vk-123", chain[0].ScopeID)
	assert.Equal(t, "global", chain[1].ScopeName)
	assert.Equal(t, "", chain[1].ScopeID)
}

// TestBuildScopeChain_WithTeam tests scope chain with VirtualKey and Team
func TestBuildScopeChain_WithTeam(t *testing.T) {
	team := &configstoreTables.TableTeam{
		ID:   "team-456",
		Name: "premium-team",
	}

	vk := &configstoreTables.TableVirtualKey{
		ID:   "vk-123",
		Name: "test-vk",
		Team: team,
	}

	chain := buildScopeChain(vk)

	require.Equal(t, 3, len(chain))
	assert.Equal(t, "virtual_key", chain[0].ScopeName)
	assert.Equal(t, "vk-123", chain[0].ScopeID)
	assert.Equal(t, "team", chain[1].ScopeName)
	assert.Equal(t, "team-456", chain[1].ScopeID)
	assert.Equal(t, "global", chain[2].ScopeName)
}

// TestBuildScopeChain_FullHierarchy tests scope chain with full hierarchy
func TestBuildScopeChain_FullHierarchy(t *testing.T) {
	customer := &configstoreTables.TableCustomer{
		ID:   "cust-789",
		Name: "acme-corp",
	}

	team := &configstoreTables.TableTeam{
		ID:       "team-456",
		Name:     "premium-team",
		Customer: customer,
	}

	vk := &configstoreTables.TableVirtualKey{
		ID:   "vk-123",
		Name: "test-vk",
		Team: team,
	}

	chain := buildScopeChain(vk)

	require.Equal(t, 4, len(chain))
	assert.Equal(t, "virtual_key", chain[0].ScopeName)
	assert.Equal(t, "vk-123", chain[0].ScopeID)
	assert.Equal(t, "team", chain[1].ScopeName)
	assert.Equal(t, "team-456", chain[1].ScopeID)
	assert.Equal(t, "customer", chain[2].ScopeName)
	assert.Equal(t, "cust-789", chain[2].ScopeID)
	assert.Equal(t, "global", chain[3].ScopeName)
	assert.Equal(t, "", chain[3].ScopeID)
}

// TestGetDefaultRouting tests getting default routing from context
func TestGetDefaultRouting(t *testing.T) {
	ctx := &RoutingContext{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
	}

	decision := getDefaultRouting(ctx)

	assert.NotNil(t, decision)
	assert.Equal(t, "openai", decision.Provider)
	assert.Equal(t, "gpt-4o", decision.Model)
	assert.Equal(t, 0, len(decision.Fallbacks))
	assert.Equal(t, "0", decision.MatchedRuleID)
}

// TestGetDefaultRouting_NilContext tests GetDefaultRouting with nil context
func TestGetDefaultRouting_NilContext(t *testing.T) {
	decision := getDefaultRouting(nil)
	assert.Nil(t, decision)
}

// TestApplyRoutingDecision tests applying a routing decision to context
func TestApplyRoutingDecision(t *testing.T) {
	ctx := &RoutingContext{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
	}

	decision := &RoutingDecision{
		Provider: "azure",
		Model:    "gpt-4-turbo",
	}

	updated := applyRoutingDecision(ctx, decision)

	assert.Equal(t, schemas.Azure, updated.Provider)
	assert.Equal(t, "gpt-4-turbo", updated.Model)
	// Original context should not be modified
	assert.Equal(t, schemas.OpenAI, ctx.Provider)
	assert.Equal(t, "gpt-4o", ctx.Model)
}

// TestApplyRoutingDecision_NilDecision tests applyRoutingDecision with nil decision
func TestApplyRoutingDecision_NilDecision(t *testing.T) {
	ctx := &RoutingContext{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
	}

	updated := applyRoutingDecision(ctx, nil)
	assert.Equal(t, ctx, updated)
}

// TestValidateRoutingDecision_Valid tests validating a valid decision
func TestValidateRoutingDecision_Valid(t *testing.T) {
	decision := &RoutingDecision{
		Provider: "openai",
		Model:    "gpt-4o",
	}

	err := validateRoutingDecision(decision)
	assert.NoError(t, err)
}

// TestValidateRoutingDecision_NilDecision tests validating nil decision
func TestValidateRoutingDecision_NilDecision(t *testing.T) {
	err := validateRoutingDecision(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")
}

// TestValidateRoutingDecision_MissingProvider tests validating with missing provider
func TestValidateRoutingDecision_MissingProvider(t *testing.T) {
	decision := &RoutingDecision{
		Model: "gpt-4o",
	}

	err := validateRoutingDecision(decision)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "provider")
}

// TestValidateRoutingDecision_MissingModel tests validating with missing model
func TestValidateRoutingDecision_MissingModel(t *testing.T) {
	decision := &RoutingDecision{
		Provider: "openai",
	}

	err := validateRoutingDecision(decision)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "model")
}

// TestEvaluateRoutingRules_NilContext tests EvaluateRoutingRules with nil context
func TestEvaluateRoutingRules_NilContext(t *testing.T) {
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	engine, err := NewRoutingEngine(store, NewMockLogger())
	require.NoError(t, err)

	_, err = engine.EvaluateRoutingRules(&schemas.BifrostContext{}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context cannot be nil")
}

// TestEvaluateRoutingRules_NoRulesMatch tests EvaluateRoutingRules when no rules match
func TestEvaluateRoutingRules_NoRulesMatch(t *testing.T) {
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	engine, err := NewRoutingEngine(store, NewMockLogger())
	require.NoError(t, err)

	ctx := &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{},
		QueryParams: map[string]string{},
	}

	decision, err := engine.EvaluateRoutingRules(&schemas.BifrostContext{}, ctx)
	assert.NoError(t, err)
	assert.Nil(t, decision)
}

// TestEvaluateRoutingRules_GlobalRuleMatches tests global scope rule matching
func TestEvaluateRoutingRules_GlobalRuleMatches(t *testing.T) {
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)
	bgCtx := &schemas.BifrostContext{}

	engine, err := NewRoutingEngine(store, NewMockLogger())
	require.NoError(t, err)

	// Create a global routing rule
	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Global Rule",
		CelExpression: "model == 'gpt-4o'",
		Provider:      "azure",
		Model:         "gpt-4-turbo",
		Enabled:       true,
		Scope:         "global",
		Priority:      0,
	}

	// Store the rule
	require.NoError(t, store.UpdateRoutingRuleInMemory(rule))

	// Create routing context
	ctx := &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{},
		QueryParams: map[string]string{},
	}

	// Evaluate rules
	decision, err := engine.EvaluateRoutingRules(bgCtx, ctx)
	require.NoError(t, err)
	require.NotNil(t, decision)

	assert.Equal(t, "azure", decision.Provider)
	assert.Equal(t, "gpt-4-turbo", decision.Model)
	assert.Equal(t, "1", decision.MatchedRuleID)
	assert.Equal(t, "Global Rule", decision.MatchedRuleName)
}

// TestEvaluateRoutingRules_ScopePrecedence tests virtual_key scope takes precedence over global
func TestEvaluateRoutingRules_ScopePrecedence(t *testing.T) {
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)
	bgCtx := &schemas.BifrostContext{}

	engine, err := NewRoutingEngine(store, NewMockLogger())
	require.NoError(t, err)

	// Create global rule
	globalRule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Global Rule",
		CelExpression: "model == 'gpt-4o'",
		Provider:      "openai",
		Model:         "gpt-4o",
		Enabled:       true,
		Scope:         "global",
		Priority:      0,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(globalRule))

	// Create VK-specific rule (should take precedence)
	vkRule := &configstoreTables.TableRoutingRule{
		ID:            "2",
		Name:          "VK Rule",
		CelExpression: "model == 'gpt-4o'",
		Provider:      "azure",
		Model:         "gpt-4-turbo",
		Enabled:       true,
		Scope:         "virtual_key",
		ScopeID:       bifrost.Ptr("vk-123"),
		Priority:      10,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(vkRule))

	// Create routing context with VirtualKey
	vk := &configstoreTables.TableVirtualKey{
		ID:   "vk-123",
		Name: "test-vk",
	}

	ctx := &RoutingContext{
		VirtualKey:  vk,
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{},
		QueryParams: map[string]string{},
	}

	// Evaluate rules
	decision, err := engine.EvaluateRoutingRules(bgCtx, ctx)
	require.NoError(t, err)
	require.NotNil(t, decision)

	// Should match VK rule, not global
	assert.Equal(t, "azure", decision.Provider)
	assert.Equal(t, "gpt-4-turbo", decision.Model)
	assert.Equal(t, "2", decision.MatchedRuleID)
}

// TestEvaluateRoutingRules_PriorityOrdering tests rules within scope are evaluated by priority.
// Lower numeric Priority is higher precedence (model/UI semantics); rules are ordered ASC.
func TestEvaluateRoutingRules_PriorityOrdering(t *testing.T) {
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)
	bgCtx := &schemas.BifrostContext{}

	engine, err := NewRoutingEngine(store, NewMockLogger())
	require.NoError(t, err)

	// Low precedence rule (evaluated second): higher priority number
	rule1 := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Low Priority",
		CelExpression: "true",
		Provider:      "openai",
		Model:         "gpt-4o",
		Enabled:       true,
		Scope:         "global",
		Priority:      10,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(rule1))

	// High precedence rule (evaluated first): lower priority number
	rule2 := &configstoreTables.TableRoutingRule{
		ID:            "2",
		Name:          "High Priority",
		CelExpression: "true",
		Provider:      "azure",
		Model:         "gpt-4-turbo",
		Enabled:       true,
		Scope:         "global",
		Priority:      0,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(rule2))

	ctx := &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{},
		QueryParams: map[string]string{},
	}

	decision, err := engine.EvaluateRoutingRules(bgCtx, ctx)
	require.NoError(t, err)
	require.NotNil(t, decision)

	// Lower Priority value = higher precedence: "High Priority" (0) is evaluated first and matches
	assert.Equal(t, "azure", decision.Provider)
	assert.Equal(t, "2", decision.MatchedRuleID)
}

// TestResolveRoutingWithFallback_RuleMatches tests resolving with matching rule
func TestResolveRoutingWithFallback_RuleMatches(t *testing.T) {
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)
	bgCtx := &schemas.BifrostContext{}

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Test Rule",
		CelExpression: "model == 'gpt-4o'",
		Provider:      "azure",
		Model:         "gpt-4-turbo",
		Enabled:       true,
		Scope:         "global",
		Priority:      0,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(rule))

	ctx := &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{},
		QueryParams: map[string]string{},
	}

	engine, err := NewRoutingEngine(store, NewMockLogger())
	require.NoError(t, err)

	decision, err := resolveRoutingWithFallback(bgCtx, ctx, engine)
	require.NoError(t, err)
	require.NotNil(t, decision)

	assert.Equal(t, "azure", decision.Provider)
	assert.Equal(t, "gpt-4-turbo", decision.Model)
}

// TestResolveRoutingWithFallback_NoMatch tests resolving when no rule matches
func TestResolveRoutingWithFallback_NoMatch(t *testing.T) {
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	ctx := &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{},
		QueryParams: map[string]string{},
	}

	engine, err := NewRoutingEngine(store, NewMockLogger())
	require.NoError(t, err)

	decision, err := resolveRoutingWithFallback(&schemas.BifrostContext{}, ctx, engine)
	require.NoError(t, err)
	require.NotNil(t, decision)

	// Should return default routing
	assert.Equal(t, "openai", decision.Provider)
	assert.Equal(t, "gpt-4o", decision.Model)
	assert.Equal(t, "0", decision.MatchedRuleID)
}

// TestEvaluateRoutingRules_DisabledRulesIgnored tests that disabled rules are ignored
func TestEvaluateRoutingRules_DisabledRulesIgnored(t *testing.T) {
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)
	bgCtx := &schemas.BifrostContext{}

	engine, err := NewRoutingEngine(store, NewMockLogger())
	require.NoError(t, err)

	// Create disabled rule
	disabledRule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Disabled Rule",
		CelExpression: "model == 'gpt-4o'",
		Provider:      "azure",
		Model:         "gpt-4-turbo",
		Enabled:       false,
		Scope:         "global",
		Priority:      10,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(disabledRule))

	// Create enabled rule
	enabledRule := &configstoreTables.TableRoutingRule{
		ID:            "2",
		Name:          "Enabled Rule",
		CelExpression: "model == 'gpt-4o'",
		Provider:      "openai",
		Model:         "gpt-4o",
		Enabled:       true,
		Scope:         "global",
		Priority:      0,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(enabledRule))

	ctx := &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{},
		QueryParams: map[string]string{},
	}

	decision, err := engine.EvaluateRoutingRules(bgCtx, ctx)
	require.NoError(t, err)
	require.NotNil(t, decision)

	// Should match enabled rule, not disabled
	assert.Equal(t, "2", decision.MatchedRuleID)
}

// TestEvaluateRoutingRules_ComplexExpression tests evaluation with complex CEL expression
func TestEvaluateRoutingRules_ComplexExpression(t *testing.T) {
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)
	bgCtx := &schemas.BifrostContext{}

	engine, err := NewRoutingEngine(store, NewMockLogger())
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Complex Rule",
		CelExpression: "model == 'gpt-4o' && headers['x-tier'] == 'premium'",
		Provider:      "azure",
		Model:         "gpt-4-turbo",
		Enabled:       true,
		Scope:         "global",
		Priority:      0,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(rule))

	// Test with matching headers
	ctx := &RoutingContext{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Headers: map[string]string{
			"x-tier": "premium",
		},
		QueryParams: map[string]string{},
	}

	decision, err := engine.EvaluateRoutingRules(bgCtx, ctx)
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, "azure", decision.Provider)

	// Test with non-matching headers
	ctx.Headers["x-tier"] = "free"
	decision, err = engine.EvaluateRoutingRules(bgCtx, ctx)
	require.NoError(t, err)
	assert.Nil(t, decision)
}

// TestEvaluateRoutingRules_NilVirtualKey tests evaluation without VirtualKey
func TestEvaluateRoutingRules_NilVirtualKey(t *testing.T) {
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)
	bgCtx := &schemas.BifrostContext{}

	engine, err := NewRoutingEngine(store, NewMockLogger())
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Global Rule",
		CelExpression: "true",
		Provider:      "azure",
		Model:         "gpt-4-turbo",
		Enabled:       true,
		Scope:         "global",
		Priority:      0,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(rule))

	ctx := &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{},
		QueryParams: map[string]string{},
	}

	decision, err := engine.EvaluateRoutingRules(bgCtx, ctx)
	require.NoError(t, err)
	require.NotNil(t, decision)

	assert.Equal(t, "azure", decision.Provider)
}

// TestEvaluateRoutingRules_MissingHeaderGracefully tests that missing headers don't cause evaluation errors
func TestEvaluateRoutingRules_MissingHeaderGracefully(t *testing.T) {
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)
	bgCtx := &schemas.BifrostContext{}

	engine, err := NewRoutingEngine(store, NewMockLogger())
	require.NoError(t, err)

	// Create a rule that checks for a header that may not be present
	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Header Check Rule",
		CelExpression: "headers[\"x-custom-header\"] == \"premium\"",
		Provider:      "azure",
		Model:         "gpt-4-turbo",
		Enabled:       true,
		Scope:         "global",
		Priority:      0,
	}
	require.NoError(t, store.UpdateRoutingRuleInMemory(rule))

	// Create context WITHOUT the header
	ctx := &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{}, // No x-custom-header
		QueryParams: map[string]string{},
	}

	// Should not error, should just not match
	decision, err := engine.EvaluateRoutingRules(bgCtx, ctx)
	require.NoError(t, err)
	assert.Nil(t, decision) // Rule didn't match because header was missing

	// Now provide the header with correct value
	ctx.Headers = map[string]string{"x-custom-header": "premium"}
	decision, err = engine.EvaluateRoutingRules(bgCtx, ctx)
	require.NoError(t, err)
	assert.NotNil(t, decision) // Rule matches now
	assert.Equal(t, "azure", decision.Provider)
}

// TestCompileAndCacheProgram_ValidExpression_Routing tests compiling and caching a valid CEL expression
func TestCompileAndCacheProgram_ValidExpression_Routing(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Test Rule",
		CelExpression: "model == 'gpt-4o'",
		Provider:      "openai",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program)

	// Verify caching works - second call should return cached program
	cached, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, cached)
}

// TestCompileAndCacheProgram_EmptyExpression_Routing tests compiling with empty expression (defaults to "true")
func TestCompileAndCacheProgram_EmptyExpression_Routing(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Default Rule",
		CelExpression: "",
		Provider:      "openai",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program)
}

// TestCompileAndCacheProgram_InvalidExpression_Routing tests compiling an invalid expression
func TestCompileAndCacheProgram_InvalidExpression_Routing(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Invalid Rule",
		CelExpression: "model == gpt-4o'", // Missing opening quote
		Provider:      "openai",
		Enabled:       true,
	}

	_, err = store.GetRoutingProgram(rule)
	assert.Error(t, err)
}

// TestCompileAndCacheProgram_NilRule tests compiling nil rule
func TestCompileAndCacheProgram_NilRule(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	_, err = store.GetRoutingProgram(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")
}

// TestCompileAndCacheProgram_ListExpression tests compiling list membership expression
func TestCompileAndCacheProgram_ListExpression(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "List Rule",
		CelExpression: "model in ['gpt-4o', 'gpt-4-turbo']",
		Provider:      "openai",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program)
}

// TestCompileAndCacheProgram_RegexExpression tests compiling regex expression
func TestCompileAndCacheProgram_RegexExpression(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Regex Rule",
		CelExpression: "model.matches('^gpt-4.*')",
		Provider:      "openai",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program)
}

// TestCompileAndCacheProgram_HeaderExpression tests compiling header-based expression
func TestCompileAndCacheProgram_HeaderExpression(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Header Rule",
		CelExpression: "headers['x-tier'] == 'premium'",
		Provider:      "openai",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program)
}

// TestCompileAndCacheProgram_RateLimitExpression tests compiling rate limit expression
func TestCompileAndCacheProgram_RateLimitExpression(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Rate Limit Rule",
		CelExpression: "tokens_used >= 80.0",
		Provider:      "azure",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program)
}

// TestCompileAndCacheProgram_BudgetExpression tests compiling budget expression
func TestCompileAndCacheProgram_BudgetExpression(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Budget Rule",
		CelExpression: "budget_used < 100.0",
		Provider:      "azure",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program)
}

// TestCompileAndCacheProgram_ComplexExpression tests compiling complex expression
func TestCompileAndCacheProgram_ComplexExpression(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Complex Rule",
		CelExpression: "model == 'gpt-4o' && team_name == 'premium' && tokens_used >= 80.0",
		Provider:      "azure",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program)
}

// TestValidateCELExpression_Valid tests validating valid expressions
func TestValidateCELExpression_Valid(t *testing.T) {
	tests := []string{
		"model == 'gpt-4o'",
		"model in ['gpt-4o', 'gpt-4-turbo']",
		"model.matches('^gpt.*')",
		"headers['x-tier'] == 'premium'",
		"rate_limit['openai'].percent_used >= 80",
		"!budget['openai'].is_exhausted",
		"true",
		"false",
		"",
	}

	for _, expr := range tests {
		err := validateCELExpression(expr)
		assert.NoError(t, err, "expression should be valid: %s", expr)
	}
}

// TestValidateCELExpression_Invalid tests validating invalid expressions
func TestValidateCELExpression_Invalid(t *testing.T) {
	tests := []string{
		"somevariable", // No operator
		"model",        // No operator
		"gpt-4o",       // No operator
	}

	for _, expr := range tests {
		err := validateCELExpression(expr)
		assert.Error(t, err, "expression should be invalid: %s", expr)
	}
}

// TestEvaluateCELExpression_TrueResult tests evaluating expression that returns true
func TestEvaluateCELExpression_TrueResult(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		CelExpression: "model == 'gpt-4o'",
		Provider:      "openai",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)

	variables := map[string]interface{}{
		"model":    "gpt-4o",
		"provider": "openai",
		"headers":  map[string]string{},
		"params":   map[string]string{},
	}

	result, err := evaluateCELExpression(program, variables)
	require.NoError(t, err)
	assert.True(t, result)
}

// TestEvaluateCELExpression_FalseResult tests evaluating expression that returns false
func TestEvaluateCELExpression_FalseResult(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		CelExpression: "model == 'gpt-4o'",
		Provider:      "openai",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)

	variables := map[string]interface{}{
		"model":    "gpt-4-turbo",
		"provider": "openai",
		"headers":  map[string]string{},
		"params":   map[string]string{},
	}

	result, err := evaluateCELExpression(program, variables)
	require.NoError(t, err)
	assert.False(t, result)
}

// TestEvaluateCELExpression_ListMembership tests list membership evaluation
func TestEvaluateCELExpression_ListMembership(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		CelExpression: "model in ['gpt-4o', 'gpt-4-turbo']",
		Provider:      "openai",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)

	// Test: model in list
	variables := map[string]interface{}{
		"model":    "gpt-4o",
		"provider": "openai",
		"headers":  map[string]string{},
		"params":   map[string]string{},
	}

	result, err := evaluateCELExpression(program, variables)
	require.NoError(t, err)
	assert.True(t, result)

	// Test: model not in list
	variables["model"] = "claude-3"
	result, err = evaluateCELExpression(program, variables)
	require.NoError(t, err)
	assert.False(t, result)
}

// TestEvaluateCELExpression_HeaderAccess tests accessing headers
func TestEvaluateCELExpression_HeaderAccess(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "1",
		CelExpression: "headers['x-tier'] == 'premium'",
		Provider:      "openai",
		Enabled:       true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)

	variables := map[string]interface{}{
		"model":    "gpt-4o",
		"provider": "openai",
		"headers": map[string]string{
			"x-tier": "premium",
		},
		"params": map[string]string{},
	}

	result, err := evaluateCELExpression(program, variables)
	require.NoError(t, err)
	assert.True(t, result)
}

// TestEvaluateCELExpression_NilProgram tests evaluating nil program
func TestEvaluateCELExpression_NilProgram(t *testing.T) {
	_, err := evaluateCELExpression(nil, map[string]interface{}{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// TestCreateCELEnvironment tests creating CEL environment
func TestCreateCELEnvironment(t *testing.T) {
	env, err := cel.NewEnv(
		cel.Variable("model", cel.StringType),
		cel.Variable("provider", cel.StringType),
		cel.Variable("headers", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("params", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("virtual_key_id", cel.StringType),
		cel.Variable("virtual_key_name", cel.StringType),
		cel.Variable("team_id", cel.StringType),
		cel.Variable("team_name", cel.StringType),
		cel.Variable("customer_id", cel.StringType),
		cel.Variable("customer_name", cel.StringType),
		cel.Variable("rate_limit", cel.MapType(cel.StringType, cel.AnyType)),
		cel.Variable("budget", cel.MapType(cel.StringType, cel.AnyType)),
	)
	require.NoError(t, err)
	require.NotNil(t, env)
}

// TestExtractRoutingVariables_BasicContext tests extracting variables from basic context
func TestExtractRoutingVariables_BasicContext(t *testing.T) {
	ctx := &RoutingContext{
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{"x-tier": "premium"},
		QueryParams: map[string]string{"key": "value"},
	}

	variables, err := extractRoutingVariables(ctx)
	require.NoError(t, err)

	assert.Equal(t, "gpt-4o", variables["model"])
	assert.Equal(t, "openai", variables["provider"])
	assert.Equal(t, map[string]string{"x-tier": "premium"}, variables["headers"])
	assert.Equal(t, map[string]string{"key": "value"}, variables["params"])
}

// TestExtractRoutingVariables_WithVirtualKey tests extracting with VirtualKey context
func TestExtractRoutingVariables_WithVirtualKey(t *testing.T) {
	vk := &configstoreTables.TableVirtualKey{
		ID:   "vk-123",
		Name: "test-vk",
	}

	ctx := &RoutingContext{
		VirtualKey: vk,
		Provider:   schemas.OpenAI,
		Model:      "gpt-4o",
	}

	variables, err := extractRoutingVariables(ctx)
	require.NoError(t, err)

	assert.Equal(t, "vk-123", variables["virtual_key_id"])
	assert.Equal(t, "test-vk", variables["virtual_key_name"])
	assert.Equal(t, "", variables["team_id"])
	assert.Equal(t, "", variables["team_name"])
}

// TestExtractRoutingVariables_WithTeam tests extracting with Team context
func TestExtractRoutingVariables_WithTeam(t *testing.T) {
	team := &configstoreTables.TableTeam{
		ID:   "team-456",
		Name: "premium-team",
	}

	vk := &configstoreTables.TableVirtualKey{
		ID:   "vk-123",
		Name: "test-vk",
		Team: team,
	}

	ctx := &RoutingContext{
		VirtualKey: vk,
		Provider:   schemas.OpenAI,
		Model:      "gpt-4o",
	}

	variables, err := extractRoutingVariables(ctx)
	require.NoError(t, err)

	assert.Equal(t, "vk-123", variables["virtual_key_id"])
	assert.Equal(t, "team-456", variables["team_id"])
	assert.Equal(t, "premium-team", variables["team_name"])
}

// TestExtractRoutingVariables_WithCustomer tests extracting with Customer context
func TestExtractRoutingVariables_WithCustomer(t *testing.T) {
	customer := &configstoreTables.TableCustomer{
		ID:   "cust-789",
		Name: "acme-corp",
	}

	team := &configstoreTables.TableTeam{
		ID:       "team-456",
		Name:     "premium-team",
		Customer: customer,
	}

	vk := &configstoreTables.TableVirtualKey{
		ID:   "vk-123",
		Name: "test-vk",
		Team: team,
	}

	ctx := &RoutingContext{
		VirtualKey: vk,
		Provider:   schemas.OpenAI,
		Model:      "gpt-4o",
	}

	variables, err := extractRoutingVariables(ctx)
	require.NoError(t, err)

	assert.Equal(t, "cust-789", variables["customer_id"])
	assert.Equal(t, "acme-corp", variables["customer_name"])
}

// TestExtractRoutingVariables_WithRateLimits tests extracting with rate limit data
func TestExtractRoutingVariables_WithRateLimits(t *testing.T) {
	ctx := &RoutingContext{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		BudgetAndRateLimitStatus: &BudgetAndRateLimitStatus{
			BudgetPercentUsed:           75.5,
			RateLimitTokenPercentUsed:   75.5,
			RateLimitRequestPercentUsed: 75.5,
		},
	}

	variables, err := extractRoutingVariables(ctx)
	require.NoError(t, err)

	// Variables should contain budget and rate limit percentages
	assert.Equal(t, 75.5, variables["budget_used"])
	assert.Equal(t, 75.5, variables["tokens_used"])
	assert.Equal(t, 75.5, variables["request"])
}

// TestExtractRoutingVariables_WithBudgets tests extracting with budget data
func TestExtractRoutingVariables_WithBudgets(t *testing.T) {
	ctx := &RoutingContext{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		BudgetAndRateLimitStatus: &BudgetAndRateLimitStatus{
			BudgetPercentUsed:           45.0,
			RateLimitTokenPercentUsed:   45.0,
			RateLimitRequestPercentUsed: 45.0,
		},
	}

	variables, err := extractRoutingVariables(ctx)
	require.NoError(t, err)

	// Variables should contain budget percentage
	assert.Equal(t, 45.0, variables["budget_used"])
}

// TestExtractRoutingVariables_NilContext tests with nil context
func TestExtractRoutingVariables_NilContext(t *testing.T) {
	_, err := extractRoutingVariables(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")
}

// TestExtractRoutingVariables_NilMaps tests with nil maps in context
func TestExtractRoutingVariables_NilMaps(t *testing.T) {
	ctx := &RoutingContext{
		Provider:                 schemas.OpenAI,
		Model:                    "gpt-4o",
		Headers:                  nil,
		QueryParams:              nil,
		BudgetAndRateLimitStatus: nil,
	}

	variables, err := extractRoutingVariables(ctx)
	require.NoError(t, err)

	assert.NotNil(t, variables["headers"])
	assert.NotNil(t, variables["params"])

	// Headers and params should be empty maps
	assert.Equal(t, 0, len(variables["headers"].(map[string]string)))
	assert.Equal(t, 0, len(variables["params"].(map[string]string)))

	// Budget and rate limit defaults should be 0.0
	assert.Equal(t, 0.0, variables["budget_used"])
	assert.Equal(t, 0.0, variables["tokens_used"])
	assert.Equal(t, 0.0, variables["request"])
}

// TestExtractRoutingVariables_MultipleProviders tests with multiple rate limits
func TestExtractRoutingVariables_MultipleProviders(t *testing.T) {
	ctx := &RoutingContext{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		BudgetAndRateLimitStatus: &BudgetAndRateLimitStatus{
			BudgetPercentUsed:           25.0,
			RateLimitTokenPercentUsed:   25.0,
			RateLimitRequestPercentUsed: 25.0,
		},
	}

	variables, err := extractRoutingVariables(ctx)
	require.NoError(t, err)

	// Should contain budget and rate limit percentages
	assert.Equal(t, 25.0, variables["budget_used"])
	assert.Equal(t, 25.0, variables["tokens_used"])
	assert.Equal(t, 25.0, variables["request"])
}

// TestBuildRoutingContext tests the convenience builder function
func TestBuildRoutingContext(t *testing.T) {
	vk := &configstoreTables.TableVirtualKey{
		ID:   "vk-123",
		Name: "test-vk",
	}

	headers := map[string]string{"x-tier": "premium"}
	params := map[string]string{"org": "test"}

	ctx := &RoutingContext{
		VirtualKey:               vk,
		Provider:                 schemas.OpenAI,
		Model:                    "gpt-4o",
		Headers:                  headers,
		QueryParams:              params,
		BudgetAndRateLimitStatus: &BudgetAndRateLimitStatus{},
	}

	assert.Equal(t, vk, ctx.VirtualKey)
	assert.Equal(t, schemas.OpenAI, ctx.Provider)
	assert.Equal(t, "gpt-4o", ctx.Model)
	assert.Equal(t, headers, ctx.Headers)
	assert.Equal(t, params, ctx.QueryParams)
}

// TestExtractRoutingVariables_ComplexHierarchy tests full organizational hierarchy
func TestExtractRoutingVariables_ComplexHierarchy(t *testing.T) {
	customer := &configstoreTables.TableCustomer{
		ID:   "cust-789",
		Name: "acme-corp",
	}

	team := &configstoreTables.TableTeam{
		ID:       "team-456",
		Name:     "premium-team",
		Customer: customer,
	}

	vk := &configstoreTables.TableVirtualKey{
		ID:   "vk-123",
		Name: "test-vk",
		Team: team,
	}

	ctx := &RoutingContext{
		VirtualKey:  vk,
		Provider:    schemas.OpenAI,
		Model:       "gpt-4o",
		Headers:     map[string]string{"x-tier": "premium", "x-org": "acme"},
		QueryParams: map[string]string{"region": "us-east-1"},
		BudgetAndRateLimitStatus: &BudgetAndRateLimitStatus{
			BudgetPercentUsed:           60.0,
			RateLimitTokenPercentUsed:   60.0,
			RateLimitRequestPercentUsed: 60.0,
		},
	}

	variables, err := extractRoutingVariables(ctx)
	require.NoError(t, err)

	// Verify all hierarchy levels
	assert.Equal(t, "vk-123", variables["virtual_key_id"])
	assert.Equal(t, "test-vk", variables["virtual_key_name"])
	assert.Equal(t, "team-456", variables["team_id"])
	assert.Equal(t, "premium-team", variables["team_name"])
	assert.Equal(t, "cust-789", variables["customer_id"])
	assert.Equal(t, "acme-corp", variables["customer_name"])

	// Verify request context
	assert.Equal(t, "gpt-4o", variables["model"])
	assert.Equal(t, "openai", variables["provider"])

	// Verify dynamic data
	headers := variables["headers"].(map[string]string)
	assert.Equal(t, "premium", headers["x-tier"])
	assert.Equal(t, "acme", headers["x-org"])

	params := variables["params"].(map[string]string)
	assert.Equal(t, "us-east-1", params["region"])

	// Verify capacity metrics
	assert.Equal(t, 60.0, variables["budget_used"])
	assert.Equal(t, 60.0, variables["tokens_used"])
	assert.Equal(t, 60.0, variables["request"])
}

// resolveRoutingWithFallback evaluates routing rules and returns decision with fallback chain
// If primary rule doesn't match, attempts fallback providers in order
func resolveRoutingWithFallback(
	ctx *schemas.BifrostContext,
	routingCtx *RoutingContext,
	engine *RoutingEngine,
) (*RoutingDecision, error) {
	if routingCtx == nil {
		return nil, fmt.Errorf("routing context cannot be nil")
	}

	// Evaluate routing rules for primary decision
	decision, err := engine.EvaluateRoutingRules(ctx, routingCtx)
	if err != nil {
		return nil, err
	}

	// If a rule matched, return the decision
	if decision != nil {
		return decision, nil
	}

	// No rule matched - use default routing
	decision = getDefaultRouting(routingCtx)
	if decision == nil {
		return nil, fmt.Errorf("failed to create default routing decision")
	}

	return decision, nil
}

// applyRoutingDecision applies a routing decision by modifying the routing context
// Returns updated context with new provider/model/fallbacks
func applyRoutingDecision(ctx *RoutingContext, decision *RoutingDecision) *RoutingContext {
	if ctx == nil || decision == nil {
		return ctx
	}

	// Create a copy of the context
	updated := *ctx

	// Apply routing decision
	updated.Provider = schemas.ModelProvider(decision.Provider)
	updated.Model = decision.Model

	return &updated
}

// validateRoutingDecision validates that a routing decision has required fields
func validateRoutingDecision(decision *RoutingDecision) error {
	if decision == nil {
		return fmt.Errorf("routing decision cannot be nil")
	}

	if decision.Provider == "" {
		return fmt.Errorf("routing decision provider cannot be empty")
	}

	if decision.Model == "" {
		return fmt.Errorf("routing decision model cannot be empty")
	}

	return nil
}

// getDefaultRouting returns a default routing decision using provider/model from context
// Used when no routing rule matches
func getDefaultRouting(ctx *RoutingContext) *RoutingDecision {
	if ctx == nil {
		return nil
	}

	return &RoutingDecision{
		Provider:      string(ctx.Provider),
		Model:         ctx.Model,
		Fallbacks:     ctx.Fallbacks,
		MatchedRuleID: "0",
	}
}
