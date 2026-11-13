package rules

import (
	"context"
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
