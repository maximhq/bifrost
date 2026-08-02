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
	}, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")

	hasCustomerBudget := func(ctx context.Context) bool {
		for _, b := range store.collectBudgetsFromHierarchy(ctx, vk, schemas.OpenAI)["Customer"] {
			if b.ID == "customer-budget" {
				return true
			}
		}
		return false
	}
	hasCustomerRateLimit := func(ctx context.Context) bool {
		for _, rl := range store.collectRateLimitsFromHierarchy(ctx, vk, schemas.OpenAI)["Customer"] {
			if rl.ID == "customer-rl" {
				return true
			}
		}
		return false
	}

	scopedCtx := func(id string) context.Context {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceScopedCustomerID, id)
		return ctx
	}

	t.Run("no scope charges the scalar team customer", func(t *testing.T) {
		assert.True(t, hasCustomerBudget(context.Background()))
		assert.True(t, hasCustomerRateLimit(context.Background()))
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

// TestEvaluateGovernanceRequest_ScopedCustomerSkipsScalarTeamCustomerEnforcement pins
// the request-time enforcement gate (EvaluateGovernanceRequest) — the path the
// store-level collect test does not exercise. The scalar team.CustomerID customer has
// an exceeded budget; when the request is scoped to a *different* customer the guard
// (customerFromTeam && scopedAway) must skip enforcing it (DecisionAllow), while no
// scope or a matching scope still enforces it (DecisionBudgetExceeded).
func TestEvaluateGovernanceRequest_ScopedCustomerSkipsScalarTeamCustomerEnforcement(t *testing.T) {
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
	}, nil)
	require.NoError(t, err)

	p := &GovernancePlugin{
		logger:   logger,
		store:    store,
		resolver: NewBudgetResolver(store, nil, logger, nil),
	}

	evaluate := func(scope string) *EvaluationResult {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		if scope != "" {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceScopedCustomerID, scope)
		}
		res, _ := p.EvaluateGovernanceRequest(ctx, &EvaluationRequest{
			VirtualKey: "sk-bf-test",
			Provider:   schemas.OpenAI,
			Model:      "gpt-4o",
		}, schemas.ChatCompletionRequest)
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
