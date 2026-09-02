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

// TestCollectHierarchy_ScopedCustomerSkipsScalarTeamCustomer pins the OSS half of
// the header-driven customer scope: when BifrostContextKeyGovernanceScopedCustomerID
// is set (by the enterprise plugin) and differs from the team's scalar
// team.CustomerID, that customer's budget and rate limit are NOT charged — the
// enterprise layer charges the scoped customer instead. With no scope, or a scope
// matching the team customer, behavior is unchanged.
func TestCollectHierarchy_ScopedCustomerSkipsScalarTeamCustomer(t *testing.T) {
	logger := NewMockLogger()

	teamBudget := buildBudgetWithUsage("team-budget", 500.0, 0.0, "1d")
	customerBudget := buildBudgetWithUsage("customer-budget", 1000.0, 0.0, "1d")
	vkBudget := buildBudgetWithUsage("vk-budget", 100.0, 0.0, "1d")

	customerRL := buildRateLimit("customer-rl", 1000, 1000)

	team := buildTeam("team1", "Team 1", teamBudget)
	customer := buildCustomer("customer1", "Customer 1", customerBudget)
	customer.RateLimitID = &customerRL.ID
	team.CustomerID = &customer.ID
	team.Customer = customer

	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", vkBudget)
	vk.TeamID = &team.ID
	vk.Team = team

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*vkBudget, *teamBudget, *customerBudget},
		RateLimits:  []configstoreTables.TableRateLimit{*customerRL},
		Teams:       []configstoreTables.TableTeam{*team},
		Customers:   []configstoreTables.TableCustomer{*customer},
	}, nil, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")

	// Which customer pays is decided when the holder's limits are resolved, so that is what this reads.
	holds := func(limits []schemas.Limit, id string) bool {
		for _, limit := range limits {
			if limit.ID == id {
				return true
			}
		}
		return false
	}
	holderLimits := func(ctx *schemas.BifrostContext) ([]schemas.Limit, []schemas.Limit) {
		return store.HolderLimits(ctx, store.permitForVirtualKey(ctx, vk))
	}
	hasCustomerBudget := func(ctx *schemas.BifrostContext) bool {
		budgets, _ := holderLimits(ctx)
		return holds(budgets, "customer-budget")
	}
	hasCustomerRateLimit := func(ctx *schemas.BifrostContext) bool {
		_, rateLimits := holderLimits(ctx)
		return holds(rateLimits, "customer-rl")
	}

	scopedCtx := func(id string) *schemas.BifrostContext {
		ctx := emptyCtx()
		ctx.SetValue(schemas.BifrostContextKeyGovernanceScopedCustomerID, id)
		return ctx
	}

	t.Run("no scope charges the scalar team customer", func(t *testing.T) {
		assert.True(t, hasCustomerBudget(emptyCtx()))
		assert.True(t, hasCustomerRateLimit(emptyCtx()))
	})

	t.Run("scope matching the team customer charges it", func(t *testing.T) {
		assert.True(t, hasCustomerBudget(scopedCtx("customer1")))
		assert.True(t, hasCustomerRateLimit(scopedCtx("customer1")))
	})

	t.Run("scope to a different customer skips the scalar team customer", func(t *testing.T) {
		assert.False(t, hasCustomerBudget(scopedCtx("other-customer")), "scalar customer budget must be skipped when scoped elsewhere")
		assert.False(t, hasCustomerRateLimit(scopedCtx("other-customer")), "scalar customer rate limit must be skipped when scoped elsewhere")
	})
}

// TestEvaluate_ScopedCustomerSkipsScalarTeamCustomerEnforcement pins
// the request-time enforcement gate (Evaluate), the path the
// store-level collect test does not exercise. The scalar team.CustomerID customer has
// an exceeded budget; when the request is scoped to a *different* customer the guard
// (customerFromTeam && scopedAway) must skip enforcing it (DecisionAllow), while no
// scope or a matching scope still enforces it (DecisionBudgetExceeded).
func TestEvaluate_ScopedCustomerSkipsScalarTeamCustomerEnforcement(t *testing.T) {
	logger := NewMockLogger()

	teamBudget := buildBudgetWithUsage("team-budget", 1000.0, 0.0, "1d")
	customerBudget := buildBudgetWithUsage("customer-budget", 50.0, 100.0, "1d") // exceeded
	vkBudget := buildBudgetWithUsage("vk-budget", 1000.0, 0.0, "1d")

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

	p := &GovernancePlugin{
		logger:   logger,
		store:    store,
		resolver: NewBudgetResolver(store, nil, logger, nil),
	}

	evaluate := func(scope string) *EvaluationResult {
		// The key the request presented is what it is evaluated as, settled the way the transport
		// settles it.
		ctx := presentCtx("sk-bf-test")
		if scope != "" {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceScopedCustomerID, scope)
		}
		res, _ := p.Evaluate(ctx, &EvaluationRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
		})
		return res
	}

	t.Run("no scope enforces the scalar team customer", func(t *testing.T) {
		assert.Equal(t, DecisionBudgetExceeded, evaluate("").Decision)
	})
	t.Run("scope matching the team customer enforces it", func(t *testing.T) {
		assert.Equal(t, DecisionBudgetExceeded, evaluate("customer1").Decision)
	})
	t.Run("scope to a different customer skips the scalar team customer", func(t *testing.T) {
		assert.Equal(t, DecisionAllow, evaluate("other-customer").Decision)
	})
}
