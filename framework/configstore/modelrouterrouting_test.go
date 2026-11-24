package configstore

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelRouterDefaultRouting pins what a router created without routing
// gets: off, global, and after every global router before it, so turning it
// on never puts it ahead of one already serving.
func TestModelRouterDefaultRouting(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
	billing, err := store.GetModelRouter(ctx, "billing")
	require.NoError(t, err)
	assert.Equal(t, &ModelRouterRouting{Scope: "global", Priority: 0}, billing.Routing)

	support := testModelRouter("support", "Support")
	support.Routing = &ModelRouterRouting{Enabled: true, Priority: 7}
	require.NoError(t, store.CreateModelRouter(ctx, support))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("sales", "Sales")))
	sales, err := store.GetModelRouter(ctx, "sales")
	require.NoError(t, err)
	assert.False(t, sales.Routing.Enabled)
	assert.Equal(t, 8, sales.Routing.Priority, "a new router goes after the last global one")

	clash := testModelRouter("ops", "Ops")
	clash.Routing = &ModelRouterRouting{Priority: 7}
	require.ErrorIs(t, store.CreateModelRouter(ctx, clash), ErrModelRouterPriorityTaken)
}

// TestSetModelRouterRouting pins the routing save: it is validated and
// normalized, refuses a priority another router in the same scope holds, and
// is the only write that changes routing.
func TestSetModelRouterRouting(t *testing.T) {
	ctx := context.Background()
	teamRouting := ModelRouterRouting{
		Enabled:       true,
		Scope:         "team",
		ScopeID:       bifrost.Ptr(" team-1 "),
		CelExpression: ` model == "gpt-4o" `,
		Query:         map[string]any{"combinator": "and"},
		Priority:      3,
	}

	t.Run("saves and keeps it through config saves and resets", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", teamRouting))

		want := &ModelRouterRouting{
			Enabled:       true,
			Scope:         "team",
			ScopeID:       bifrost.Ptr("team-1"),
			CelExpression: `model == "gpt-4o"`,
			Query:         map[string]any{"combinator": "and"},
			Priority:      3,
		}
		got, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, want, got.Routing)

		update := testModelRouter("support", "Support desk")
		update.Routing = &ModelRouterRouting{Scope: "global"}
		require.NoError(t, store.UpdateModelRouter(ctx, update))
		got, err = store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, want, got.Routing, "a config save leaves routing alone")

		reset, err := store.ResetModelRouter(ctx, "support", testComplexityAnalyzerConfig())
		require.NoError(t, err)
		assert.Equal(t, want, reset.Routing, "a reset leaves routing alone")
	})

	t.Run("priorities are unique within a scope", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", teamRouting))

		require.ErrorIs(t, store.SetModelRouterRouting(ctx, "billing", teamRouting), ErrModelRouterPriorityTaken)
		otherTeam := teamRouting
		otherTeam.ScopeID = bifrost.Ptr("team-2")
		require.NoError(t, store.SetModelRouterRouting(ctx, "billing", otherTeam), "another team's routers have their own priorities")
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", teamRouting), "a router keeps its own priority")
	})

	t.Run("validation", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		cases := map[string]ModelRouterRouting{
			"unknown scope":     {Scope: "org"},
			"team without id":   {Scope: "team"},
			"negative priority": {Scope: "global", Priority: -1},
			"blank team id":     {Scope: "team", ScopeID: bifrost.Ptr("  ")},
		}
		for name, routing := range cases {
			t.Run(name, func(t *testing.T) {
				require.Error(t, store.SetModelRouterRouting(ctx, "support", routing))
			})
		}
		require.ErrorIs(t, store.SetModelRouterRouting(ctx, "missing", ModelRouterRouting{}), ErrNotFound)

		global := ModelRouterRouting{ScopeID: bifrost.Ptr("ignored"), Priority: 2}
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", global))
		got, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, &ModelRouterRouting{Scope: "global", Priority: 2}, got.Routing, "an empty scope is global, which has no scope id")
	})
}

// TestSetModelRouterRoutings pins the batch write config.json sync uses: two
// routers can swap priorities because the check runs on the final state, and
// a duplicate in that state, or a router that does not exist, writes nothing.
func TestSetModelRouterRoutings(t *testing.T) {
	ctx := context.Background()
	global := func(priority int) ModelRouterRouting {
		return ModelRouterRouting{Enabled: true, Scope: "global", Priority: priority}
	}
	setup := func(t *testing.T) *RDBConfigStore {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
		// Created at 0 and 1; billing moves first so support can take 1.
		require.NoError(t, store.SetModelRouterRouting(ctx, "billing", global(2)))
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", global(1)))
		return store
	}
	priority := func(t *testing.T, store *RDBConfigStore, id string) int {
		router, err := store.GetModelRouter(ctx, id)
		require.NoError(t, err)
		return router.Routing.Priority
	}

	t.Run("a swap applies", func(t *testing.T) {
		store := setup(t)
		require.NoError(t, store.SetModelRouterRoutings(ctx, []ModelRouterRoutingChange{
			{RouterID: "support", Routing: global(2)},
			{RouterID: "billing", Routing: global(1)},
		}))
		assert.Equal(t, 2, priority(t, store, "support"))
		assert.Equal(t, 1, priority(t, store, "billing"))
	})

	t.Run("a duplicate in the final state writes nothing", func(t *testing.T) {
		store := setup(t)
		err := store.SetModelRouterRoutings(ctx, []ModelRouterRoutingChange{
			{RouterID: "support", Routing: global(5)},
			{RouterID: "billing", Routing: global(5)},
		})
		require.ErrorIs(t, err, ErrModelRouterPriorityTaken)
		assert.Equal(t, 1, priority(t, store, "support"))
		assert.Equal(t, 2, priority(t, store, "billing"))
	})

	t.Run("a missing router writes nothing", func(t *testing.T) {
		store := setup(t)
		err := store.SetModelRouterRoutings(ctx, []ModelRouterRoutingChange{
			{RouterID: "support", Routing: global(7)},
			{RouterID: "missing", Routing: global(8)},
		})
		require.ErrorIs(t, err, ErrNotFound)
		assert.Equal(t, 1, priority(t, store, "support"))
	})
}

// TestGenerateModelRouterHashRouting pins that a config.json entry's routing
// is part of its hash only when the entry declares it, and that the
// condition builder state, which is never evaluated, is not.
func TestGenerateModelRouterHashRouting(t *testing.T) {
	base := *testModelRouter("support", "Support")
	withRouting := base
	withRouting.Routing = &ModelRouterRouting{Scope: "global", Priority: 1}
	assert.NotEqual(t, GenerateModelRouterHash(base), GenerateModelRouterHash(withRouting))

	enabled := withRouting
	enabled.Routing = &ModelRouterRouting{Enabled: true, Scope: "global", Priority: 1}
	assert.NotEqual(t, GenerateModelRouterHash(withRouting), GenerateModelRouterHash(enabled))

	withQuery := withRouting
	withQuery.Routing = &ModelRouterRouting{Scope: "global", Priority: 1, Query: map[string]any{"combinator": "and"}}
	assert.Equal(t, GenerateModelRouterHash(withRouting), GenerateModelRouterHash(withQuery))
}

// createTestModelRouters saves one router per id, each with the default
// routing: off, global and one priority after the router before it.
func createTestModelRouters(t *testing.T, store *RDBConfigStore, ids ...string) {
	t.Helper()
	for _, id := range ids {
		require.NoError(t, store.CreateModelRouter(context.Background(), testModelRouter(id, "Router "+id)))
	}
}

// testModelRouterWithRouting is testModelRouter with routing to create it with.
func testModelRouterWithRouting(id, name string, routing ModelRouterRouting) *ModelRouter {
	router := testModelRouter(id, name)
	router.Routing = &routing
	return router
}

// storedModelRouterRouting reads the routing a router has in the store.
func storedModelRouterRouting(t *testing.T, store *RDBConfigStore, id string) ModelRouterRouting {
	t.Helper()
	router, err := store.GetModelRouter(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, router.Routing)
	return *router.Routing
}

// TestValidateModelRouterRouting pins the parts of the routing check the store
// tests do not reach: every scope a routing rule can take is accepted, the
// scope is trimmed but matched exactly as written, and a missing routing is an
// error rather than a panic.
func TestValidateModelRouterRouting(t *testing.T) {
	require.Error(t, ValidateModelRouterRouting(nil))

	for _, scope := range []string{"team", "customer", "virtual_key", "user"} {
		t.Run(scope+" scope", func(t *testing.T) {
			routing := ModelRouterRouting{Scope: " " + scope + " ", ScopeID: bifrost.Ptr("entity-1"), Priority: 0}
			require.NoError(t, ValidateModelRouterRouting(&routing))
			assert.Equal(t, ModelRouterRouting{Scope: scope, ScopeID: bifrost.Ptr("entity-1")}, routing)
		})
	}

	t.Run("scope in another letter case", func(t *testing.T) {
		require.ErrorContains(t, ValidateModelRouterRouting(&ModelRouterRouting{Scope: "Global"}), `invalid scope "Global"`)
		require.Error(t, ValidateModelRouterRouting(&ModelRouterRouting{Scope: "TEAM", ScopeID: bifrost.Ptr("team-1")}))
	})
}

// TestModelRouterDisabledRouterHoldsItsPriority pins that turning a router off
// does not give its place away: no other router in the scope can take its
// priority, so turning it back on never collides. The same number stays free
// in every other scope, including another kind of scope with the same id.
func TestModelRouterDisabledRouterHoldsItsPriority(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	createTestModelRouters(t, store, "billing", "support", "sales")
	require.NoError(t, store.SetModelRouterRouting(ctx, "billing", ModelRouterRouting{Scope: "global", Priority: 4}))
	require.False(t, storedModelRouterRouting(t, store, "billing").Enabled)

	before := storedModelRouterRouting(t, store, "support")
	err := store.SetModelRouterRouting(ctx, "support", ModelRouterRouting{Enabled: true, Scope: "global", Priority: 4})
	require.ErrorIs(t, err, ErrModelRouterPriorityTaken)
	assert.ErrorIs(t, err, ErrAlreadyExists, "a taken priority is a conflict, as a taken routing rule priority is")
	assert.Equal(t, before, storedModelRouterRouting(t, store, "support"), "a refused save leaves the routing as it was")

	acmeTeam := ModelRouterRouting{Enabled: true, Scope: "team", ScopeID: bifrost.Ptr("acme"), Priority: 4}
	require.NoError(t, store.SetModelRouterRouting(ctx, "support", acmeTeam), "the same priority is free in another scope")
	acmeCustomer := ModelRouterRouting{Scope: "customer", ScopeID: bifrost.Ptr("acme"), Priority: 4}
	require.NoError(t, store.SetModelRouterRouting(ctx, "sales", acmeCustomer), "a customer and a team with the same id are different scopes")

	// Turning a serving router off keeps its place in its scope too.
	acmeTeam.Enabled = false
	require.NoError(t, store.SetModelRouterRouting(ctx, "support", acmeTeam))
	acmeTeam.Enabled = true
	require.ErrorIs(t, store.SetModelRouterRouting(ctx, "sales", acmeTeam), ErrModelRouterPriorityTaken)
	assert.Equal(t, acmeCustomer, storedModelRouterRouting(t, store, "sales"))
}

// TestSetModelRouterRoutingReplacesTheWholeRouting pins that a routing save is
// a replace, not a merge: moving a scoped, conditioned, enabled router back to
// a bare global routing clears its scope id, condition, builder state and
// enabled flag, even though each new value is a zero value.
func TestSetModelRouterRoutingReplacesTheWholeRouting(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	createTestModelRouters(t, store, "support")
	require.NoError(t, store.SetModelRouterRouting(ctx, " support ", ModelRouterRouting{
		Enabled:       true,
		Scope:         "team",
		ScopeID:       bifrost.Ptr("team-1"),
		CelExpression: `model == "gpt-4o"`,
		Query:         map[string]any{"combinator": "and"},
		Priority:      3,
	}), "the router id is trimmed")
	require.Equal(t, "team", storedModelRouterRouting(t, store, "support").Scope)

	require.NoError(t, store.SetModelRouterRouting(ctx, "support", ModelRouterRouting{Scope: "global"}))
	assert.Equal(t, ModelRouterRouting{Scope: "global"}, storedModelRouterRouting(t, store, "support"))

	var row tables.TableModelRouter
	require.NoError(t, store.DB().First(&row, "id = ?", "support").Error)
	assert.Nil(t, row.ScopeID, "a global router's scope id is NULL, which is what the priority check matches on")
	assert.Nil(t, row.Query)
}

// TestSetModelRouterRoutingLeavesClassifierAndTargets pins that changing which
// requests a router takes, one router at a time or in a batch, never changes
// how it classifies them or which model each tier gets.
func TestSetModelRouterRoutingLeavesClassifierAndTargets(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	createTestModelRouters(t, store, "support")
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", testModelRouterTargets()))
	before, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)

	team := ModelRouterRouting{Enabled: true, Scope: "team", ScopeID: bifrost.Ptr("team-1"), Priority: 3}
	require.NoError(t, store.SetModelRouterRouting(ctx, "support", team))
	require.NoError(t, store.SetModelRouterRoutings(ctx, []ModelRouterRoutingChange{
		{RouterID: "support", Routing: ModelRouterRouting{Enabled: true, Scope: "user", ScopeID: bifrost.Ptr("user-1"), Priority: 8}},
	}))

	after, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "user", after.Routing.Scope)
	assert.Equal(t, before.Name, after.Name)
	assert.Equal(t, before.Description, after.Description)
	assert.Equal(t, before.ComplexityAnalyzerConfig, after.ComplexityAnalyzerConfig)
	assert.Equal(t, before.Targets, after.Targets)
}

// TestCreateModelRouterWithRouting pins what creating a router with routing of
// its own does: the routing is stored as given once normalized, exactly as a
// routing save would store it, and a routing that is invalid or asks for a
// taken priority creates no router at all.
func TestCreateModelRouterWithRouting(t *testing.T) {
	ctx := context.Background()
	countRouters := func(t *testing.T, store *RDBConfigStore) int64 {
		var rows int64
		require.NoError(t, store.DB().Model(&tables.TableModelRouter{}).Count(&rows).Error)
		return rows
	}

	t.Run("stored as given", func(t *testing.T) {
		store := setupRDBTestStore(t)
		routing := ModelRouterRouting{
			Enabled:       true,
			Scope:         "virtual_key",
			ScopeID:       bifrost.Ptr("vk-1"),
			CelExpression: `model.startsWith("gpt-")`,
			Query:         map[string]any{"combinator": "or"},
			Priority:      12,
		}
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouterWithRouting("support", "Support", routing)))
		assert.Equal(t, routing, storedModelRouterRouting(t, store, "support"))
	})

	t.Run("normalized", func(t *testing.T) {
		cases := map[string]struct{ given, want ModelRouterRouting }{
			"a blank scope is global and drops the scope id": {
				given: ModelRouterRouting{Scope: "  ", ScopeID: bifrost.Ptr("team-1"), Priority: 2},
				want:  ModelRouterRouting{Scope: "global", Priority: 2},
			},
			"scope, scope id and condition are trimmed": {
				given: ModelRouterRouting{Scope: " customer ", ScopeID: bifrost.Ptr(" cust-1 "), CelExpression: " true ", Priority: 1},
				want:  ModelRouterRouting{Scope: "customer", ScopeID: bifrost.Ptr("cust-1"), CelExpression: "true", Priority: 1},
			},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				store := setupRDBTestStore(t)
				require.NoError(t, store.CreateModelRouter(ctx, testModelRouterWithRouting("support", "Support", tc.given)))
				assert.Equal(t, tc.want, storedModelRouterRouting(t, store, "support"))
			})
		}
	})

	t.Run("a taken priority is refused and nothing is created", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouterWithRouting("support", "Support", ModelRouterRouting{Scope: "global", Priority: 3})))

		err := store.CreateModelRouter(ctx, testModelRouterWithRouting("billing", "Billing", ModelRouterRouting{Enabled: true, Scope: "global", Priority: 3}))
		require.ErrorIs(t, err, ErrModelRouterPriorityTaken)
		assert.ErrorIs(t, err, ErrAlreadyExists)
		_, err = store.GetModelRouter(ctx, "billing")
		require.ErrorIs(t, err, ErrNotFound)
		assert.EqualValues(t, 1, countRouters(t, store))

		// A taken name is a conflict of its own, not a priority clash.
		err = store.CreateModelRouter(ctx, testModelRouterWithRouting("other", "Support", ModelRouterRouting{Scope: "global", Priority: 9}))
		require.ErrorIs(t, err, ErrAlreadyExists)
		assert.NotErrorIs(t, err, ErrModelRouterPriorityTaken)

		team := ModelRouterRouting{Enabled: true, Scope: "team", ScopeID: bifrost.Ptr("team-1"), Priority: 3}
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouterWithRouting("billing", "Billing", team)),
			"the refused id and name are still free, and the priority is free in another scope")
		assert.Equal(t, team, storedModelRouterRouting(t, store, "billing"))
	})

	t.Run("invalid routing is refused and nothing is created", func(t *testing.T) {
		store := setupRDBTestStore(t)
		cases := map[string]ModelRouterRouting{
			"unknown scope":         {Scope: "org"},
			"user without an id":    {Scope: "user"},
			"blank virtual key id":  {Scope: "virtual_key", ScopeID: bifrost.Ptr(" ")},
			"negative priority":     {Scope: "global", Priority: -1},
			"negative in a scope":   {Scope: "team", ScopeID: bifrost.Ptr("team-1"), Priority: -3},
			"scope in another case": {Scope: "Team", ScopeID: bifrost.Ptr("team-1")},
		}
		for name, routing := range cases {
			t.Run(name, func(t *testing.T) {
				require.Error(t, store.CreateModelRouter(ctx, testModelRouterWithRouting("support", "Support", routing)))
			})
		}
		assert.Zero(t, countRouters(t, store))
	})
}

// TestModelRouterDefaultRoutingPriority pins where a router created without
// routing lands as the routers around it change: one after the highest global
// priority there is right now, whoever holds it and whether or not it is on,
// and never moved by a router scoped to a team, customer, key or user.
func TestModelRouterDefaultRoutingPriority(t *testing.T) {
	ctx := context.Background()
	created := func(t *testing.T, store *RDBConfigStore, id string) ModelRouterRouting {
		createTestModelRouters(t, store, id)
		return storedModelRouterRouting(t, store, id)
	}

	t.Run("the highest priority is given out again once its router is deleted", func(t *testing.T) {
		store := setupRDBTestStore(t)
		createTestModelRouters(t, store, "first", "second", "third")
		require.Equal(t, 2, storedModelRouterRouting(t, store, "third").Priority)

		require.NoError(t, store.DeleteModelRouter(ctx, "third"))
		assert.Equal(t, ModelRouterRouting{Scope: "global", Priority: 2}, created(t, store, "fourth"))

		// A gap below the highest is left alone.
		require.NoError(t, store.DeleteModelRouter(ctx, "first"))
		assert.Equal(t, 3, created(t, store, "fifth").Priority)

		for _, id := range []string{"second", "fourth", "fifth"} {
			require.NoError(t, store.DeleteModelRouter(ctx, id))
		}
		assert.Equal(t, 0, created(t, store, "sixth").Priority, "with no router left the count starts over")
	})

	t.Run("scoped routers never shift it", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouterWithRouting("team-router", "Team router",
			ModelRouterRouting{Enabled: true, Scope: "team", ScopeID: bifrost.Ptr("team-1"), Priority: 50})))
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouterWithRouting("customer-router", "Customer router",
			ModelRouterRouting{Enabled: true, Scope: "customer", ScopeID: bifrost.Ptr("cust-1"), Priority: 90})))

		assert.Equal(t, ModelRouterRouting{Scope: "global", Priority: 0}, created(t, store, "first"), "the first global router starts at 0")
		assert.Equal(t, 1, created(t, store, "second").Priority)

		// Moving a global router into a scope takes its priority out of the count.
		require.NoError(t, store.SetModelRouterRouting(ctx, "second", ModelRouterRouting{Scope: "user", ScopeID: bifrost.Ptr("user-1"), Priority: 70}))
		assert.Equal(t, 1, created(t, store, "third").Priority)
	})

	t.Run("a disabled router counts", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouterWithRouting("serving", "Serving", ModelRouterRouting{Enabled: true, Scope: "global", Priority: 1})))
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouterWithRouting("parked", "Parked", ModelRouterRouting{Scope: "global", Priority: 9})))
		assert.Equal(t, ModelRouterRouting{Scope: "global", Priority: 10}, created(t, store, "new"))
	})
}

// TestSetModelRouterRoutingsReordersAndMoves pins the batch write beyond a
// two-way swap: any reordering that ends with unique priorities applies in one
// go, routers can trade scopes, only the final state is checked, including
// against routers the batch does not name, and a refused batch writes nothing.
func TestSetModelRouterRoutingsReordersAndMoves(t *testing.T) {
	ctx := context.Background()
	global := func(priority int) ModelRouterRouting {
		return ModelRouterRouting{Enabled: true, Scope: "global", Priority: priority}
	}
	team := func(id string, priority int) ModelRouterRouting {
		return ModelRouterRouting{Enabled: true, Scope: "team", ScopeID: bifrost.Ptr(id), Priority: priority}
	}
	ids := []string{"billing", "sales", "support"}
	// billing, sales and support serve globally at 1, 2 and 3.
	setup := func(t *testing.T) *RDBConfigStore {
		store := setupRDBTestStore(t)
		createTestModelRouters(t, store, ids...)
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", global(3)))
		require.NoError(t, store.SetModelRouterRouting(ctx, "sales", global(2)))
		require.NoError(t, store.SetModelRouterRouting(ctx, "billing", global(1)))
		return store
	}
	routings := func(t *testing.T, store *RDBConfigStore) map[string]ModelRouterRouting {
		stored := make(map[string]ModelRouterRouting, len(ids))
		for _, id := range ids {
			stored[id] = storedModelRouterRouting(t, store, id)
		}
		return stored
	}
	seeded := map[string]ModelRouterRouting{"billing": global(1), "sales": global(2), "support": global(3)}

	t.Run("a three-way rotation applies", func(t *testing.T) {
		store := setup(t)
		require.NoError(t, store.SetModelRouterRoutings(ctx, []ModelRouterRoutingChange{
			{RouterID: "billing", Routing: global(2)},
			{RouterID: "sales", Routing: global(3)},
			{RouterID: "support", Routing: global(1)},
		}))
		assert.Equal(t, map[string]ModelRouterRouting{"billing": global(2), "sales": global(3), "support": global(1)}, routings(t, store))
	})

	t.Run("routers trade scopes", func(t *testing.T) {
		store := setup(t)
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", team("team-1", 1)))
		require.NoError(t, store.SetModelRouterRoutings(ctx, []ModelRouterRoutingChange{
			{RouterID: "billing", Routing: team("team-1", 1)},
			{RouterID: "support", Routing: global(1)},
		}))
		assert.Equal(t, map[string]ModelRouterRouting{"billing": team("team-1", 1), "sales": global(2), "support": global(1)}, routings(t, store))
	})

	t.Run("a router listed twice takes its last entry", func(t *testing.T) {
		store := setup(t)
		// The first entry asks for the priority sales holds; only the final state is checked.
		require.NoError(t, store.SetModelRouterRoutings(ctx, []ModelRouterRoutingChange{
			{RouterID: "billing", Routing: global(2)},
			{RouterID: "billing", Routing: team("team-1", 5)},
		}))
		assert.Equal(t, map[string]ModelRouterRouting{"billing": team("team-1", 5), "sales": global(2), "support": global(3)}, routings(t, store))
	})

	t.Run("a router listed twice is refused when its last entry clashes", func(t *testing.T) {
		store := setup(t)
		err := store.SetModelRouterRoutings(ctx, []ModelRouterRoutingChange{
			{RouterID: "billing", Routing: team("team-1", 5)},
			{RouterID: "billing", Routing: global(2)},
		})
		require.ErrorIs(t, err, ErrModelRouterPriorityTaken)
		assert.Equal(t, seeded, routings(t, store))
	})

	t.Run("a priority held by a router outside the batch writes nothing", func(t *testing.T) {
		store := setup(t)
		err := store.SetModelRouterRoutings(ctx, []ModelRouterRoutingChange{
			{RouterID: "billing", Routing: global(9)},
			{RouterID: "sales", Routing: global(3)},
		})
		require.ErrorIs(t, err, ErrModelRouterPriorityTaken)
		assert.ErrorIs(t, err, ErrAlreadyExists)
		assert.Equal(t, seeded, routings(t, store))
	})

	t.Run("an invalid routing writes nothing", func(t *testing.T) {
		store := setup(t)
		err := store.SetModelRouterRoutings(ctx, []ModelRouterRoutingChange{
			{RouterID: "billing", Routing: global(9)},
			{RouterID: "sales", Routing: ModelRouterRouting{Scope: "team", Priority: 1}},
		})
		require.ErrorContains(t, err, `model router "sales"`, "the error names the router whose routing is invalid")
		assert.Equal(t, seeded, routings(t, store))
	})

	t.Run("each routing is normalized", func(t *testing.T) {
		store := setup(t)
		require.NoError(t, store.SetModelRouterRoutings(ctx, []ModelRouterRoutingChange{
			{RouterID: " billing ", Routing: ModelRouterRouting{Scope: " ", ScopeID: bifrost.Ptr("ignored"), CelExpression: " true ", Priority: 9}},
			{RouterID: "sales", Routing: ModelRouterRouting{Enabled: true, Scope: " team ", ScopeID: bifrost.Ptr(" team-1 "), Priority: 0}},
		}))
		assert.Equal(t, map[string]ModelRouterRouting{
			"billing": {Scope: "global", CelExpression: "true", Priority: 9},
			"sales":   team("team-1", 0),
			"support": global(3),
		}, routings(t, store))
	})

	t.Run("no changes is a no-op", func(t *testing.T) {
		store := setup(t)
		require.NoError(t, store.SetModelRouterRoutings(ctx, nil))
		assert.Equal(t, seeded, routings(t, store))
	})
}
