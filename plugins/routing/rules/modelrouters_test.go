package rules

import (
	"context"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// routerRouting builds enabled routing in a scope.
func routerRouting(scope, scopeID string, priority int, cel string) configstore.ModelRouterRouting {
	routing := configstore.ModelRouterRouting{Enabled: true, Scope: scope, Priority: priority, CelExpression: cel}
	if scopeID != "" {
		routing.ScopeID = bifrost.Ptr(scopeID)
	}
	return routing
}

// newTestModelRouterMatcher builds an empty matcher.
func newTestModelRouterMatcher(t *testing.T) *ModelRouterMatcher {
	t.Helper()
	matcher, err := NewModelRouterMatcher()
	require.NoError(t, err)
	return matcher
}

// matchID returns the id of the router that takes the request, or "".
func matchID(t *testing.T, matcher *ModelRouterMatcher, evaluation *EvaluationContext) string {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	id, err := matcher.Match(ctx, evaluation)
	require.NoError(t, err)
	return id
}

// TestModelRouterMatcherOrder pins that routers are walked in the routing
// rules' order: narrowest scope first, then priority, the first match only.
func TestModelRouterMatcherOrder(t *testing.T) {
	matcher := newTestModelRouterMatcher(t)
	require.NoError(t, matcher.Set("global-late", "Global late", routerRouting("global", "", 5, "")))
	require.NoError(t, matcher.Set("global-early", "Global early", routerRouting("global", "", 1, "")))
	require.NoError(t, matcher.Set("team", "Team", routerRouting("team", "team-1", 9, "")))
	require.NoError(t, matcher.Set("vk", "VK", routerRouting("virtual_key", "vk-1", 9, `model == "never"`)))

	assert.Equal(t, "global-early", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o"}), "priority orders a scope")
	assert.Equal(t, "team", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o", Scope: GovernanceScope{TeamID: "team-1"}}),
		"a narrower scope wins whatever its priority")
	assert.Equal(t, "team", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o", Scope: GovernanceScope{VirtualKeyID: "vk-1", TeamID: "team-1"}}),
		"a narrower router whose condition fails passes to the next scope")
	assert.Equal(t, "global-early", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o", Scope: GovernanceScope{TeamID: "team-2"}}),
		"another team's router never matches")
}

// TestModelRouterMatcherConditions pins how conditions are read: an empty one
// takes everything in scope, a condition sees the model the routing rules
// settled on, and a disabled router never matches.
func TestModelRouterMatcherConditions(t *testing.T) {
	matcher := newTestModelRouterMatcher(t)
	require.NoError(t, matcher.Set("mini-only", "Mini only", routerRouting("global", "", 0, `model == "gpt-4o-mini"`)))
	disabled := routerRouting("global", "", 1, "")
	disabled.Enabled = false
	require.NoError(t, matcher.Set("disabled", "Disabled", disabled))
	require.NoError(t, matcher.Set("header", "Header", routerRouting("global", "", 2, `headers["x-app"] == "helpdesk"`)))

	assert.Equal(t, "mini-only", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o-mini"}))
	assert.Equal(t, "", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o"}), "a missing header is a non-match, not an error")
	assert.Equal(t, "header", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o", Headers: map[string]string{"x-app": "helpdesk"}}))
}

// TestModelRouterMatcherSetAndDelete pins that a router moves with its scope,
// turning it off forgets it, a bad condition leaves the recorded routing as
// it was, and HasRoutes follows what is recorded.
func TestModelRouterMatcherSetAndDelete(t *testing.T) {
	matcher := newTestModelRouterMatcher(t)
	assert.False(t, matcher.HasRoutes())
	var nilMatcher *ModelRouterMatcher
	assert.False(t, nilMatcher.HasRoutes(), "a plugin built without the matcher skips the step")

	require.NoError(t, matcher.Set("support", "Support", routerRouting("team", "team-1", 0, "")))
	assert.True(t, matcher.HasRoutes())
	require.NoError(t, matcher.Set("support", "Support", routerRouting("global", "", 0, "")))
	assert.Equal(t, "support", matchID(t, matcher, &EvaluationContext{Model: "m"}), "the router moved to global")
	_, inTeam := matcher.routes.Load(scopeKey("team", "team-1"))
	assert.False(t, inTeam, "and left the team's scope")

	require.Error(t, matcher.Set("support", "Support", routerRouting("global", "", 0, `model ==`)))
	assert.Equal(t, "support", matchID(t, matcher, &EvaluationContext{Model: "m"}), "a failed update keeps the recorded routing")

	off := routerRouting("global", "", 0, "")
	off.Enabled = false
	require.NoError(t, matcher.Set("support", "Support", off))
	assert.False(t, matcher.HasRoutes(), "a router turned off is forgotten")

	require.NoError(t, matcher.Set("support", "Support", routerRouting("global", "", 0, "")))
	matcher.Delete("support")
	assert.False(t, matcher.HasRoutes())
	assert.Equal(t, "", matchID(t, matcher, &EvaluationContext{Model: "m"}))
	matcher.Delete("support")
}

// TestValidateModelRouterCEL pins that a router's condition compiles like a
// routing rule's but cannot use complexity_tier.
func TestValidateModelRouterCEL(t *testing.T) {
	assert.NoError(t, ValidateModelRouterCEL(""))
	assert.NoError(t, ValidateModelRouterCEL(`model == "gpt-4o" && headers["x-app"] == "helpdesk"`))
	assert.Error(t, ValidateModelRouterCEL(`model ==`))
	assert.ErrorContains(t, ValidateModelRouterCEL(`complexity_tier == "COMPLEX"`), "complexity_tier")

	matcher := newTestModelRouterMatcher(t)
	require.Error(t, matcher.Set("tier", "Tier", routerRouting("global", "", 0, `complexity_tier == "SIMPLE"`)))
	assert.False(t, matcher.HasRoutes())
}

// TestSelectModelRouterTarget pins target selection: only the tier's targets
// are candidates, a single one always wins, and weights split the rest.
func TestSelectModelRouterTarget(t *testing.T) {
	targets := []configstoreTables.TableModelRouterTarget{
		{Tier: configstoreTables.ModelRouterTierSimple, Model: bifrost.Ptr("mini"), Weight: 1},
		{Tier: configstoreTables.ModelRouterTierComplex, Model: bifrost.Ptr("opus"), Weight: 0.5},
		{Tier: configstoreTables.ModelRouterTierComplex, Model: bifrost.Ptr("sonnet"), Weight: 0.5},
	}
	got, ok := SelectModelRouterTarget(targets, configstoreTables.ModelRouterTierSimple)
	require.True(t, ok)
	assert.Equal(t, "mini", *got.Model)

	_, ok = SelectModelRouterTarget(targets, configstoreTables.ModelRouterTierMedium)
	assert.False(t, ok, "a tier without targets keeps the request's model")

	seen := map[string]bool{}
	for range 200 {
		got, ok := SelectModelRouterTarget(targets, configstoreTables.ModelRouterTierComplex)
		require.True(t, ok)
		seen[*got.Model] = true
	}
	assert.Equal(t, map[string]bool{"opus": true, "sonnet": true}, seen, "both weighted targets are used and no other tier's")
}

// BenchmarkModelRouterMatcher measures the per-request model router step's
// gate and match: a gateway with no enabled router pays only HasRoutes; one
// with routers pays the scope walk and, when a router has a condition, one
// CEL evaluation per router tried.
func BenchmarkModelRouterMatcher(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	evaluation := &EvaluationContext{
		Scope:    GovernanceScope{TeamID: "team-1", VirtualKeyID: "vk-1"},
		Provider: schemas.OpenAI, Model: "support-assistant", RequestType: "chat_completion",
		Headers: map[string]string{"x-app": "helpdesk"},
	}
	b.Run("no enabled router", func(b *testing.B) {
		matcher, err := NewModelRouterMatcher()
		require.NoError(b, err)
		b.ReportAllocs()
		for b.Loop() {
			if matcher.HasRoutes() {
				b.Fatal("an empty matcher reports routes")
			}
		}
	})
	b.Run("global router with a condition matches", func(b *testing.B) {
		matcher, err := NewModelRouterMatcher()
		require.NoError(b, err)
		require.NoError(b, matcher.Set("support", "Support", routerRouting("global", "", 0, `model == "support-assistant"`)))
		b.ReportAllocs()
		for b.Loop() {
			if !matcher.HasRoutes() {
				b.Fatal("no routes")
			}
			if id, _ := matcher.Match(ctx, evaluation); id != "support" {
				b.Fatalf("matched %q", id)
			}
		}
	})
	b.Run("three scopes, none matches", func(b *testing.B) {
		matcher, err := NewModelRouterMatcher()
		require.NoError(b, err)
		require.NoError(b, matcher.Set("vk", "VK", routerRouting("virtual_key", "vk-1", 0, `model == "never"`)))
		require.NoError(b, matcher.Set("team", "Team", routerRouting("team", "team-1", 0, `headers["x-app"] == "other"`)))
		require.NoError(b, matcher.Set("global", "Global", routerRouting("global", "", 0, `model == "gpt-4o-mini"`)))
		b.ReportAllocs()
		for b.Loop() {
			if id, _ := matcher.Match(ctx, evaluation); id != "" {
				b.Fatalf("matched %q", id)
			}
		}
	})
}

// TestModelRouterMatcherWalksEveryScope pins the whole scope chain, including
// the user and customer levels: virtual key, user, team, customer, global.
// Priorities are compared only within a scope, so the same priority in two
// scopes never ties.
func TestModelRouterMatcherWalksEveryScope(t *testing.T) {
	matcher := newTestModelRouterMatcher(t)
	require.NoError(t, matcher.Set("global", "Global", routerRouting("global", "", 0, "")))
	require.NoError(t, matcher.Set("customer", "Customer", routerRouting("customer", "customer-1", 0, "")))
	require.NoError(t, matcher.Set("team", "Team", routerRouting("team", "team-1", 0, "")))
	require.NoError(t, matcher.Set("user", "User", routerRouting("user", "user-1", 0, "")))
	require.NoError(t, matcher.Set("vk", "VK", routerRouting("virtual_key", "vk-1", 0, "")))

	everyone := GovernanceScope{VirtualKeyID: "vk-1", UserID: "user-1", TeamID: "team-1", CustomerID: "customer-1"}
	steps := []struct {
		drop func(scope *GovernanceScope)
		want string
	}{
		{drop: func(*GovernanceScope) {}, want: "vk"},
		{drop: func(scope *GovernanceScope) { scope.VirtualKeyID = "" }, want: "user"},
		{drop: func(scope *GovernanceScope) { scope.UserID = "" }, want: "team"},
		{drop: func(scope *GovernanceScope) { scope.TeamID = "" }, want: "customer"},
		{drop: func(scope *GovernanceScope) { scope.CustomerID = "" }, want: "global"},
	}
	scope := everyone
	for _, step := range steps {
		step.drop(&scope)
		assert.Equal(t, step.want, matchID(t, matcher, &EvaluationContext{Model: "gpt-4o", Scope: scope}))
	}

	// A scope id is only read at its own level: a team's id never selects a
	// customer's router, even when the two ids are the same string.
	require.NoError(t, matcher.Set("customer", "Customer", routerRouting("customer", "shared-id", 0, "")))
	assert.Equal(t, "global", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o", Scope: GovernanceScope{TeamID: "shared-id"}}))
	assert.Equal(t, "customer", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o", Scope: GovernanceScope{CustomerID: "shared-id"}}))
}

// TestModelRouterMatcherSkipsConditionsThatFail pins what a condition that
// fails to evaluate costs: only its own router. The request is told which
// router was skipped, by the name it has now, and the next router is tried.
func TestModelRouterMatcherSkipsConditionsThatFail(t *testing.T) {
	matcher := newTestModelRouterMatcher(t)
	failing := routerRouting("global", "", 0, `int(headers["x-n"]) > 3`)
	require.NoError(t, matcher.Set("fragile", "Fragile", failing))
	require.NoError(t, matcher.Set("catch-all", "Catch all", routerRouting("global", "", 1, "")))
	evaluation := &EvaluationContext{Model: "gpt-4o", Headers: map[string]string{"x-n": "not-a-number"}}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	id, err := matcher.Match(ctx, evaluation)
	require.NoError(t, err, "a failing condition is not a failed match")
	assert.Equal(t, "catch-all", id)
	logs := ctx.GetRoutingEngineLogs()
	require.Len(t, logs, 1)
	assert.Equal(t, schemas.RoutingEngineModelRouter, logs[0].Engine)
	assert.Equal(t, schemas.LogLevelWarn, logs[0].Level)
	assert.Contains(t, logs[0].Message, "Model router 'Fragile' skipped: its condition failed to evaluate")

	// A rename reaches the log with the routing it is saved beside.
	require.NoError(t, matcher.Set("fragile", "Renamed", failing))
	ctx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, err = matcher.Match(ctx, evaluation)
	require.NoError(t, err)
	require.Len(t, ctx.GetRoutingEngineLogs(), 1)
	assert.Contains(t, ctx.GetRoutingEngineLogs()[0].Message, "Model router 'Renamed' skipped")

	// With a value the condition can read, the same router takes the request.
	assert.Equal(t, "fragile", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o", Headers: map[string]string{"x-n": "7"}}))

	// A nil request context is tolerated: the skip is just not logged.
	id, err = matcher.Match(nil, evaluation)
	require.NoError(t, err)
	assert.Equal(t, "catch-all", id)
}

// TestModelRouterMatcherReplacesARoutersRoute pins that a router only ever
// has one route: saving its routing again, with another priority or another
// condition, replaces the old route instead of adding a second one.
func TestModelRouterMatcherReplacesARoutersRoute(t *testing.T) {
	matcher := newTestModelRouterMatcher(t)
	require.NoError(t, matcher.Set("first", "First", routerRouting("global", "", 0, "")))
	require.NoError(t, matcher.Set("second", "Second", routerRouting("global", "", 1, "")))
	assert.Equal(t, "first", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o"}))

	require.NoError(t, matcher.Set("first", "First", routerRouting("global", "", 9, "")))
	assert.Equal(t, "second", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o"}), "the router moved behind the other")
	value, _ := matcher.routes.Load(scopeKey("global", ""))
	assert.Len(t, value.([]modelRouterRoute), 2, "a router is listed once, whatever was saved for it")

	require.NoError(t, matcher.Set("second", "Second", routerRouting("global", "", 1, `model == "gpt-4o-mini"`)))
	assert.Equal(t, "first", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o"}), "the old condition no longer applies")
	assert.Equal(t, "second", matchID(t, matcher, &EvaluationContext{Model: "gpt-4o-mini"}))

	matcher.Delete("first")
	matcher.Delete("second")
	assert.False(t, matcher.HasRoutes(), "an emptied scope is forgotten, so the step is skipped again")
}

// TestSelectModelRouterTargetFollowsWeights pins that a tier's targets split
// its requests by weight, not evenly: nine in ten for a 0.9 target. The
// bounds are seven standard deviations wide, so the test does not flake.
func TestSelectModelRouterTargetFollowsWeights(t *testing.T) {
	targets := []configstoreTables.TableModelRouterTarget{
		{Tier: configstoreTables.ModelRouterTierComplex, Model: bifrost.Ptr("mostly"), Weight: 0.9},
		{Tier: configstoreTables.ModelRouterTierComplex, Model: bifrost.Ptr("rarely"), Weight: 0.1},
		{Tier: configstoreTables.ModelRouterTierSimple, Model: bifrost.Ptr("other-tier"), Weight: 1},
	}
	const requests = 4000
	picked := map[string]int{}
	for range requests {
		got, ok := SelectModelRouterTarget(targets, configstoreTables.ModelRouterTierComplex)
		require.True(t, ok)
		picked[*got.Model]++
	}
	assert.Zero(t, picked["other-tier"])
	assert.InDelta(t, 0.9*requests, picked["mostly"], 0.035*requests)
	assert.Equal(t, requests, picked["mostly"]+picked["rarely"])

	_, ok := SelectModelRouterTarget(nil, configstoreTables.ModelRouterTierComplex)
	assert.False(t, ok, "a router without targets keeps every request's model")
}

// TestModelRouterMatcherConcurrentWrites saves and removes routes while
// requests are matched. Under -race it pins that a match never reads a slice
// a save is writing, and that every match returns a router that exists.
func TestModelRouterMatcherConcurrentWrites(t *testing.T) {
	matcher := newTestModelRouterMatcher(t)
	require.NoError(t, matcher.Set("steady", "Steady", routerRouting("global", "", 100, "")))
	known := map[string]bool{"steady": true, "a": true, "b": true, "c": true}

	stop := make(chan struct{})
	var writers, readers sync.WaitGroup
	for index, id := range []string{"a", "b", "c"} {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				switch i % 3 {
				case 0:
					_ = matcher.Set(id, id, routerRouting("global", "", index, `model == "gpt-4o"`))
				case 1:
					_ = matcher.Set(id, id, routerRouting("team", "team-1", index, ""))
				default:
					matcher.Delete(id)
				}
			}
		}()
	}
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			evaluation := &EvaluationContext{Model: "gpt-4o", Scope: GovernanceScope{TeamID: "team-1"}}
			for range 500 {
				id, err := matcher.Match(nil, evaluation)
				if err != nil || !known[id] {
					t.Errorf("matched %q, err %v", id, err)
					return
				}
			}
		}()
	}
	readers.Wait()
	close(stop)
	writers.Wait()
}
