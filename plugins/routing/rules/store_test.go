package rules

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRuleStore_CreateAndRetrieve tests creating and retrieving routing rules
func TestRuleStore_CreateAndRetrieve(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	// Create a global routing rule
	rule1 := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Global Rule",
		Description:   "Test global routing rule",
		Enabled:       bifrost.Ptr(true),
		CelExpression: "model == 'gpt-4o'",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai"), Model: bifrost.Ptr("gpt-4"), Weight: 1.0},
		},
		Fallbacks:       nil,
		ParsedFallbacks: []string{"azure/gpt-4-turbo"},
		Scope:           "global",
		ScopeID:         nil,
		Priority:        10,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	// Create a team-scoped routing rule
	teamID := "team-123"
	rule2 := &configstoreTables.TableRoutingRule{
		ID:            "2",
		Name:          "Team Rule",
		Description:   "Test team routing rule",
		Enabled:       bifrost.Ptr(true),
		CelExpression: "model in ['gpt-4o', 'gpt-4-turbo']",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("azure"), Weight: 1.0},
		},
		Fallbacks:       nil,
		ParsedFallbacks: []string{"groq/mixtral-8x7b"},
		Scope:           "team",
		ScopeID:         &teamID,
		Priority:        20,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	// Store rules in memory
	err = store.UpsertRule(context.Background(), rule1)
	require.NoError(t, err)
	err = store.UpsertRule(context.Background(), rule2)
	require.NoError(t, err)

	// Test retrieval by scope
	globalRules := store.GetScopedRules(context.Background(), "global", "")
	assert.Equal(t, 1, len(globalRules))
	assert.Equal(t, "Global Rule", globalRules[0].Name)

	teamRules := store.GetScopedRules(context.Background(), "team", teamID)
	assert.Equal(t, 1, len(teamRules))
	assert.Equal(t, "Team Rule", teamRules[0].Name)

	// Test ListRoutingRules
	allRules := store.GetAllRules(context.Background())
	assert.Equal(t, 2, len(allRules))
}

// TestRuleStore_PriorityOrdering tests that rules are sorted by priority
func TestRuleStore_PriorityOrdering(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	// Create rules with different priorities
	rules := []*configstoreTables.TableRoutingRule{
		{
			ID:       "1",
			Name:     "Priority 5",
			Priority: 5,
			Scope:    "global",
			ScopeID:  nil,
			Enabled:  bifrost.Ptr(true),
		},
		{
			ID:       "2",
			Name:     "Priority 20",
			Priority: 20,
			Scope:    "global",
			ScopeID:  nil,
			Enabled:  bifrost.Ptr(true),
		},
		{
			ID:       "3",
			Name:     "Priority 10",
			Priority: 10,
			Scope:    "global",
			ScopeID:  nil,
			Enabled:  bifrost.Ptr(true),
		},
	}

	for _, rule := range rules {
		err := store.UpsertRule(context.Background(), rule)
		require.NoError(t, err)
	}

	// Retrieve and verify ordering (sorted by priority ASC, so lower numbers first)
	retrieved := store.GetScopedRules(context.Background(), "global", "")
	assert.Equal(t, 3, len(retrieved))
	assert.Equal(t, 5, retrieved[0].Priority)
	assert.Equal(t, 10, retrieved[1].Priority)
	assert.Equal(t, 20, retrieved[2].Priority)
}

// TestRuleStore_DisabledRulesFiltered tests that disabled rules are filtered out
func TestRuleStore_DisabledRulesFiltered(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	enabledRule := &configstoreTables.TableRoutingRule{
		ID:      "1",
		Name:    "Enabled Rule",
		Enabled: bifrost.Ptr(true),
		Scope:   "global",
		ScopeID: nil,
	}

	disabledRule := &configstoreTables.TableRoutingRule{
		ID:      "2",
		Name:    "Disabled Rule",
		Enabled: bifrost.Ptr(false),
		Scope:   "global",
		ScopeID: nil,
	}

	err = store.UpsertRule(context.Background(), enabledRule)
	require.NoError(t, err)
	err = store.UpsertRule(context.Background(), disabledRule)
	require.NoError(t, err)

	// Only enabled rules should be returned
	retrieved := store.GetScopedRules(context.Background(), "global", "")
	assert.Equal(t, 1, len(retrieved))
	assert.Equal(t, "Enabled Rule", retrieved[0].Name)
}

// TestRuleStore_DeleteRule tests deleting a routing rule
func TestRuleStore_DeleteRule(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:      "1",
		Name:    "Test Rule",
		Enabled: bifrost.Ptr(true),
		Scope:   "global",
		ScopeID: nil,
	}

	// Add rule
	err = store.UpsertRule(context.Background(), rule)
	require.NoError(t, err)

	retrieved := store.GetScopedRules(context.Background(), "global", "")
	assert.Equal(t, 1, len(retrieved))

	// Delete rule
	err = store.DeleteRule(context.Background(), rule.ID)
	require.NoError(t, err)

	// Verify deletion
	retrieved = store.GetScopedRules(context.Background(), "global", "")
	assert.Equal(t, 0, len(retrieved))
}

// TestRuleStore_MultipleScopes tests rules with multiple scopes
func TestRuleStore_MultipleScopes(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	customerID := "cust-123"
	teamID := "team-456"

	// Create rules for different scopes
	globalRule := &configstoreTables.TableRoutingRule{
		ID: "1", Name: "Global", Scope: "global", ScopeID: nil, Priority: 10, Enabled: bifrost.Ptr(true),
	}
	customerRule := &configstoreTables.TableRoutingRule{
		ID: "2", Name: "Customer", Scope: "customer", ScopeID: &customerID, Priority: 20, Enabled: bifrost.Ptr(true),
	}
	teamRule := &configstoreTables.TableRoutingRule{
		ID: "3", Name: "Team", Scope: "team", ScopeID: &teamID, Priority: 30, Enabled: bifrost.Ptr(true),
	}

	require.NoError(t, store.UpsertRule(context.Background(), globalRule))
	require.NoError(t, store.UpsertRule(context.Background(), customerRule))
	require.NoError(t, store.UpsertRule(context.Background(), teamRule))

	// Test global scope
	globalRules := store.GetScopedRules(context.Background(), "global", "")
	assert.Equal(t, 1, len(globalRules))
	assert.Equal(t, "Global", globalRules[0].Name)

	// Test customer scope
	custRules := store.GetScopedRules(context.Background(), "customer", customerID)
	assert.Equal(t, 1, len(custRules))
	assert.Equal(t, "Customer", custRules[0].Name)

	// Test team scope
	teamRules := store.GetScopedRules(context.Background(), "team", teamID)
	assert.Equal(t, 1, len(teamRules))
	assert.Equal(t, "Team", teamRules[0].Name)

	// ListAll should return all rules sorted by priority ASC (lower numbers = higher priority)
	allRules := store.GetAllRules(context.Background())
	assert.Equal(t, 3, len(allRules))
	assert.Equal(t, 10, allRules[0].Priority) // Global (highest)
	assert.Equal(t, 20, allRules[1].Priority) // Customer
	assert.Equal(t, 30, allRules[2].Priority) // Team (lowest)
}

// TestCompileAndCacheProgram tests CEL program compilation and caching
func TestCompileAndCacheProgram(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "rule-1",
		Name:          "Test Rule",
		CelExpression: "model == 'gpt-4o' && tokens_used < 80.0",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai")},
		},
		Enabled: bifrost.Ptr(true),
	}

	// First compilation
	program1, err := store.GetProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program1)

	// Verify it's cached - second call should return cached program
	program2, err := store.GetProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program2)

	// Both should be the same cached instance
	assert.Equal(t, program1, program2)
}

// TestCompileAndCacheProgram_InvalidExpression tests error handling for invalid CEL
func TestCompileAndCacheProgram_InvalidExpression(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "rule-invalid",
		Name:          "Invalid Rule",
		CelExpression: "model == gpt-4o'", // Syntax error
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai")},
		},
		Enabled: bifrost.Ptr(true),
	}

	_, err = store.GetProgram(context.Background(), rule)
	assert.Error(t, err)

	// Invalid rule should not be cached - attempting to get it again should fail
	_, err = store.GetProgram(context.Background(), rule)
	assert.Error(t, err)
}

// TestCompileAndCacheProgram_CacheInvalidation tests cache invalidation on rule update
func TestCompileAndCacheProgram_CacheInvalidation(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "rule-update",
		Name:          "Update Rule",
		CelExpression: "model == 'gpt-4o'",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai")},
		},
		Enabled: bifrost.Ptr(true),
		Scope:   "global",
	}

	// Compile and cache
	program1, err := store.GetProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program1)

	// Update rule in memory (should invalidate cache)
	rule.CelExpression = "model == 'gpt-4-turbo'"
	err = store.UpsertRule(context.Background(), rule)
	require.NoError(t, err)

	// Recompile should work
	program2, err := store.GetProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program2)
}

// TestCompileAndCacheProgram_CacheInvalidationOnDelete tests cache invalidation on rule deletion
func TestCompileAndCacheProgram_CacheInvalidationOnDelete(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "rule-delete",
		Name:          "Delete Rule",
		CelExpression: "provider == 'openai'",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai")},
		},
		Enabled: bifrost.Ptr(true),
		Scope:   "global",
	}

	// Compile and cache
	_, err = store.GetProgram(context.Background(), rule)
	require.NoError(t, err)

	// Delete rule (should invalidate cache)
	err = store.DeleteRule(context.Background(), rule.ID)
	require.NoError(t, err)

	// After deletion, we can't verify cache directly, but the rule is gone from storage
}

// TestCompileAndCacheProgram_EmptyExpression tests compilation of empty CEL expression (defaults to "true")
func TestCompileAndCacheProgram_EmptyExpression(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "rule-empty",
		Name:          "Empty Rule",
		CelExpression: "",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai")},
		},
		Enabled: bifrost.Ptr(true),
	}

	program, err := store.GetProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program)

	// Verify caching works - second call should return same program
	program2, err := store.GetProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program2)
	assert.Equal(t, program, program2)
}

// TestRuleStore_GetAllRulesOrdersEqualPriorityNewestFirst pins the ordering GetAllRules
// documents. Rules are ordered by priority ascending, and rules sharing a priority come back
// newest first. This behavior carried over unchanged from the governance rule cache this store
// replaced, so it is pinned here rather than adjusted.
func TestRuleStore_GetAllRulesOrdersEqualPriorityNewestFirst(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)

	older := &configstoreTables.TableRoutingRule{
		ID: "older", Name: "Older", Scope: "global", Priority: 5, Enabled: bifrost.Ptr(true),
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	newer := &configstoreTables.TableRoutingRule{
		ID: "newer", Name: "Newer", Scope: "global", Priority: 5, Enabled: bifrost.Ptr(true),
		CreatedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	highPriority := &configstoreTables.TableRoutingRule{
		ID: "high", Name: "High", Scope: "global", Priority: 0, Enabled: bifrost.Ptr(true),
		CreatedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}

	require.NoError(t, store.UpsertRule(context.Background(), older))
	require.NoError(t, store.UpsertRule(context.Background(), newer))
	require.NoError(t, store.UpsertRule(context.Background(), highPriority))

	got := store.GetAllRules(context.Background())
	require.Len(t, got, 3)

	ids := []string{got[0].ID, got[1].ID, got[2].ID}
	assert.Equal(t, []string{"high", "newer", "older"}, ids,
		"priority ascending first, then newest first within the same priority")
}
