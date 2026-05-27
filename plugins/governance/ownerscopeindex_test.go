package governance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOwnerScopeTestStore returns an empty in-memory store for owner-scope index tests.
func newOwnerScopeTestStore(t *testing.T) *LocalGovernanceStore {
	t.Helper()
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)
	return store
}

func ownerStrPtr(s string) *string { return &s }
func ownerI64Ptr(v int64) *int64   { return &v }

// collectBudgetScopes runs EachBudgetForScopes and returns budgetID -> reported owner scope.
func collectBudgetScopes(gs *LocalGovernanceStore, scopes []OwnerScope) map[string]OwnerScope {
	out := make(map[string]OwnerScope)
	gs.EachBudgetForScopes(scopes, func(budgetID string, currentUsage, maxLimit float64, ownerScopeType, ownerScopeID string) {
		out[budgetID] = OwnerScope{Type: ownerScopeType, ID: ownerScopeID}
	})
	return out
}

// TestEachBudgetForScopes_BucketsByOwnerScope verifies the reverse index attributes
// each budget to the scope derived from its FK columns and returns only the
// budgets owned by the requested scopes.
func TestEachBudgetForScopes_BucketsByOwnerScope(t *testing.T) {
	gs := newOwnerScopeTestStore(t)
	gs.budgets.Store("b-vk", &configstoreTables.TableBudget{ID: "b-vk", MaxLimit: 100, CurrentUsage: 90, VirtualKeyID: ownerStrPtr("vk1")})
	gs.budgets.Store("b-team", &configstoreTables.TableBudget{ID: "b-team", MaxLimit: 100, CurrentUsage: 50, TeamID: ownerStrPtr("team1")})
	gs.budgets.Store("b-cust", &configstoreTables.TableBudget{ID: "b-cust", MaxLimit: 100, CurrentUsage: 10, CustomerID: ownerStrPtr("cust1")})
	gs.budgets.Store("b-global", &configstoreTables.TableBudget{ID: "b-global", MaxLimit: 100, CurrentUsage: 30})

	t.Run("single vk scope", func(t *testing.T) {
		got := collectBudgetScopes(gs, []OwnerScope{{Type: "virtual_key", ID: "vk1"}})
		require.Len(t, got, 1)
		assert.Equal(t, OwnerScope{Type: "virtual_key", ID: "vk1"}, got["b-vk"])
	})

	t.Run("global scope", func(t *testing.T) {
		got := collectBudgetScopes(gs, []OwnerScope{{Type: "global", ID: ""}})
		require.Len(t, got, 1)
		assert.Equal(t, OwnerScope{Type: "global", ID: ""}, got["b-global"])
	})

	t.Run("multiple scopes", func(t *testing.T) {
		got := collectBudgetScopes(gs, []OwnerScope{
			{Type: "virtual_key", ID: "vk1"},
			{Type: "team", ID: "team1"},
			{Type: "customer", ID: "cust1"},
			{Type: "global", ID: ""},
		})
		assert.Len(t, got, 4)
	})

	t.Run("unrelated scope returns nothing", func(t *testing.T) {
		got := collectBudgetScopes(gs, []OwnerScope{{Type: "virtual_key", ID: "vk-absent"}})
		assert.Empty(t, got)
	})

	t.Run("duplicate scopes evaluate each budget once", func(t *testing.T) {
		count := 0
		gs.EachBudgetForScopes([]OwnerScope{{Type: "team", ID: "team1"}, {Type: "team", ID: "team1"}},
			func(budgetID string, currentUsage, maxLimit float64, ownerScopeType, ownerScopeID string) { count++ })
		assert.Equal(t, 1, count)
	})
}

// TestEachBudgetForScopes_ReadsLiveUsage verifies usage mutations are reflected
// even when the cached index is reused (IDs are resolved live from the hot map).
func TestEachBudgetForScopes_ReadsLiveUsage(t *testing.T) {
	gs := newOwnerScopeTestStore(t)
	b := &configstoreTables.TableBudget{ID: "b-vk", MaxLimit: 100, CurrentUsage: 10, VirtualKeyID: ownerStrPtr("vk1")}
	gs.budgets.Store("b-vk", b)

	// Build the index once.
	got := collectBudgetScopes(gs, []OwnerScope{{Type: "virtual_key", ID: "vk1"}})
	require.Len(t, got, 1)

	// Mutate live usage (in-place clone replacement, as the usage path does).
	gs.budgets.Store("b-vk", &configstoreTables.TableBudget{ID: "b-vk", MaxLimit: 100, CurrentUsage: 95, VirtualKeyID: ownerStrPtr("vk1")})

	var seenUsage float64
	gs.EachBudgetForScopes([]OwnerScope{{Type: "virtual_key", ID: "vk1"}},
		func(budgetID string, currentUsage, maxLimit float64, ownerScopeType, ownerScopeID string) {
			seenUsage = currentUsage
		})
	assert.Equal(t, 95.0, seenUsage, "live usage must be read from the hot map, not the index")
}

// TestEachBudgetForScopes_RebuildsWhenStale verifies that a newly added budget is
// picked up after the index TTL forces a rebuild.
func TestEachBudgetForScopes_RebuildsWhenStale(t *testing.T) {
	gs := newOwnerScopeTestStore(t)
	gs.budgets.Store("b-1", &configstoreTables.TableBudget{ID: "b-1", MaxLimit: 100, VirtualKeyID: ownerStrPtr("vk1")})

	// Prime the index.
	require.Len(t, collectBudgetScopes(gs, []OwnerScope{{Type: "virtual_key", ID: "vk1"}}), 1)

	// Add a second budget for the same scope, then force the index stale.
	gs.budgets.Store("b-2", &configstoreTables.TableBudget{ID: "b-2", MaxLimit: 100, VirtualKeyID: ownerStrPtr("vk1")})
	gs.ownerScopeIndexMu.Lock()
	gs.ownerScopeIndexBuiltAt = time.Now().Add(-2 * ownerScopeIndexTTL)
	gs.ownerScopeIndexMu.Unlock()

	assert.Len(t, collectBudgetScopes(gs, []OwnerScope{{Type: "virtual_key", ID: "vk1"}}), 2)
}

// TestEachRateLimitForScopes_TokenAndRequest verifies token- and request-limited
// rate limits are bucketed and surfaced through their respective iterators using
// owner rows instead of synthetic rate-limit owner fields.
func TestEachRateLimitForScopes_TokenAndRequest(t *testing.T) {
	gs := newOwnerScopeTestStore(t)
	gs.rateLimits.Store("rl-token", &configstoreTables.TableRateLimit{ID: "rl-token", TokenMaxLimit: ownerI64Ptr(1000), TokenCurrentUsage: 900})
	gs.rateLimits.Store("rl-req", &configstoreTables.TableRateLimit{ID: "rl-req", RequestMaxLimit: ownerI64Ptr(100), RequestCurrentUsage: 80})
	gs.rateLimits.Store("rl-customer", &configstoreTables.TableRateLimit{ID: "rl-customer", TokenMaxLimit: ownerI64Ptr(250), TokenCurrentUsage: 175})
	gs.rateLimits.Store("rl-provider", &configstoreTables.TableRateLimit{ID: "rl-provider", TokenMaxLimit: ownerI64Ptr(500), TokenCurrentUsage: 275})

	gs.virtualKeys.Store("vk1", &configstoreTables.TableVirtualKey{ID: "vk1", RateLimitID: ownerStrPtr("rl-token")})
	gs.teams.Store("team1", &configstoreTables.TableTeam{ID: "team1", RateLimitID: ownerStrPtr("rl-req")})
	gs.customers.Store("cust1", &configstoreTables.TableCustomer{ID: "cust1", RateLimitID: ownerStrPtr("rl-customer")})
	gs.virtualKeys.Store("vk-provider", &configstoreTables.TableVirtualKey{
		ID: "vk-provider",
		ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{{
			ID: 1, VirtualKeyID: "vk-provider", Provider: "openai", RateLimitID: ownerStrPtr("rl-provider"),
		}},
	})

	t.Run("token limit only", func(t *testing.T) {
		ids := map[string]int64{}
		gs.EachRateLimitForScopes([]OwnerScope{{Type: "virtual_key", ID: "vk1"}, {Type: "customer", ID: "cust1"}, {Type: "provider", ID: "openai"}},
			func(id string, cur, max int64, st, sid string) { ids[id] = max })
		require.Len(t, ids, 3)
		assert.Equal(t, int64(1000), ids["rl-token"])
		assert.Equal(t, int64(250), ids["rl-customer"])
		assert.Equal(t, int64(500), ids["rl-provider"])
	})

	t.Run("request limit only", func(t *testing.T) {
		ids := map[string]int64{}
		gs.EachRequestLimitForScopes([]OwnerScope{{Type: "virtual_key", ID: "vk1"}, {Type: "team", ID: "team1"}, {Type: "customer", ID: "cust1"}},
			func(id string, cur, max int64, st, sid string) { ids[id] = max })
		require.Len(t, ids, 1)
		assert.Equal(t, int64(100), ids["rl-req"])
	})
}

// seedScaleBudgets populates the store with n VK-owned budgets (one budget per
// distinct virtual_key scope) for scaling benchmarks.
func seedScaleBudgets(gs *LocalGovernanceStore, n int) {
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("b-%d", i)
		vk := fmt.Sprintf("vk-%d", i)
		gs.budgets.Store(id, &configstoreTables.TableBudget{
			ID: id, MaxLimit: 100, CurrentUsage: float64(i % 100), VirtualKeyID: ownerStrPtr(vk),
		})
	}
}

// benchScaleSizes are the budget/VK counts swept by the scaling benchmarks.
var benchScaleSizes = []int{1000, 10000, 25000, 50000}

// BenchmarkEachBudgetForScopes_Targeted measures the scope-targeted path: a usage
// update visits only the budgets owned by the scopes it touched. Cost is
// independent of the total budget count, modeling the alerting hot path.
func BenchmarkEachBudgetForScopes_Targeted(b *testing.B) {
	scopes := []OwnerScope{
		{Type: "global", ID: ""},
		{Type: "provider", ID: "openai"},
		{Type: "virtual_key", ID: "vk-123"},
	}
	for _, n := range benchScaleSizes {
		gs, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
		require.NoError(b, err)
		seedScaleBudgets(gs, n)
		b.Run(fmt.Sprintf("budgets=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var sum float64
				gs.EachBudgetForScopes(scopes, func(_ string, currentUsage, _ float64, _, _ string) { sum += currentUsage })
				_ = sum
			}
		})
	}
}

// BenchmarkOwnerScopeIndexRebuild measures the cost of a single full index
// rebuild, which the store amortizes to at most once per ownerScopeIndexTTL
// regardless of request rate.
func BenchmarkOwnerScopeIndexRebuild(b *testing.B) {
	for _, n := range benchScaleSizes {
		gs, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
		require.NoError(b, err)
		seedScaleBudgets(gs, n)
		b.Run(fmt.Sprintf("budgets=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				gs.ownerScopeIndexMu.Lock()
				gs.rebuildOwnerScopeIndexLocked()
				gs.ownerScopeIndexMu.Unlock()
			}
		})
	}
}

// TestEachBudgetForScopes_EmptyInputs verifies the guard clauses are no-ops.
func TestEachBudgetForScopes_EmptyInputs(t *testing.T) {
	gs := newOwnerScopeTestStore(t)
	gs.budgets.Store("b-1", &configstoreTables.TableBudget{ID: "b-1", MaxLimit: 100, VirtualKeyID: ownerStrPtr("vk1")})

	assert.NotPanics(t, func() {
		gs.EachBudgetForScopes(nil, func(string, float64, float64, string, string) { t.Fatal("fn must not be called") })
		gs.EachBudgetForScopes([]OwnerScope{{Type: "virtual_key", ID: "vk1"}}, nil)
	})
}
