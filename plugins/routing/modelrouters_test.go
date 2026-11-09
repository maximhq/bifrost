package routing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// TestComplexityStateKeysScopeModelRouters pins that each model router keeps
// its own session and continuation state, while the Complexity Router page's
// config, which has no router id, keeps the keys it had before routers
// existed.
func TestComplexityStateKeysScopeModelRouters(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySessionID, "session-1")

	require.Equal(t, bifrost.SessionStateKey(ctx, complexitySessionKind), complexitySessionKeyFor(ctx, ""))
	require.Equal(t, bifrost.SessionStateKey(ctx, complexityTurnTierKind), complexityTurnTierKeyFor(ctx, ""))
	require.Equal(t, complexitySessionKey(ctx), complexitySessionKeyFor(ctx, ""), "the complexity config keeps the old keys")

	support, billing := complexitySessionKeyFor(ctx, "support"), complexitySessionKeyFor(ctx, "billing")
	assert.NotEqual(t, complexitySessionKey(ctx), support)
	assert.NotEqual(t, support, billing)
	assert.NotEqual(t, complexityTurnTierKey(ctx), complexityTurnTierKeyFor(ctx, "support"))
}

// TestRoutingPluginServesStoredModelRouters pins the boot path: a plugin
// built over a config store serves every router it holds on a runtime of its
// own, with its targets, records the routing of the enabled ones, and leaves
// the Complexity Router page's config, which it serves on its own
// classifiers, as it was.
func TestRoutingPluginServesStoredModelRouters(t *testing.T) {
	ctx := context.Background()
	logger := rules.NewMockLogger()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/config.db"},
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(ctx) })

	keywords := complexity.EditableKeywordConfig{
		SimpleKeywords: []string{"papaya amber"}, MediumKeywords: []string{"cedar cobalt"}, ComplexKeywords: []string{"obsidian comet"},
	}
	require.NoError(t, store.CreateModelRouter(ctx, &configstore.ModelRouter{
		ID: "general", Name: "General",
		ComplexityAnalyzerConfig: complexity.AnalyzerConfig{Classifier: complexity.ClassifierDecision, Keywords: keywords},
	}))
	require.NoError(t, store.CreateModelRouter(ctx, &configstore.ModelRouter{
		ID: "support", Name: "Support",
		ComplexityAnalyzerConfig: complexity.AnalyzerConfig{Classifier: complexity.ClassifierDecision, Keywords: keywords},
		Routing:                  &configstore.ModelRouterRouting{Enabled: true, Priority: 5},
	}))
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", []configstoreTables.TableModelRouterTarget{
		{Tier: complexity.TierSimple, Model: bifrost.Ptr("gpt-4o-mini"), Weight: 1},
	}))

	ruleStore, err := rules.NewLocalStore(ctx, logger, store)
	require.NoError(t, err)
	plugin, err := InitFromStore(ctx, nil, logger, store, ruleStore, NewMockGovernance())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })

	rt, ok := plugin.modelRouter("support")
	require.True(t, ok)
	assert.Equal(t, "Support", rt.displayName())
	assert.Equal(t, complexity.ClassifierDecision, rt.config.Load().Classifier)
	require.Len(t, rt.loadTargets(), 1, "a router is served with its targets")
	assert.Equal(t, "gpt-4o-mini", *rt.loadTargets()[0].Model)
	_, ok = plugin.modelRouter("general")
	assert.True(t, ok, "a router that is off is still served, for its tests and status")
	assert.True(t, plugin.routerMatcher.HasRoutes(), "the enabled router's routing is recorded")
	assert.NotEqual(t, complexity.ClassifierDecision, plugin.complexityConfig.Load().Classifier, "routers never configure the complexity config")

	// Replacing a router's targets leaves its classifiers alone.
	config := rt.config.Load()
	require.NoError(t, plugin.SetModelRouterTargets("support", nil))
	assert.Empty(t, rt.loadTargets())
	assert.Same(t, config, rt.config.Load())
	assert.ErrorIs(t, plugin.SetModelRouterTargets("missing", nil), configstore.ErrNotFound)

	// Turning the router off forgets its routing and keeps it served.
	require.NoError(t, plugin.SetModelRouterRouting("support", configstore.ModelRouterRouting{Scope: "global", Priority: 5}))
	assert.False(t, plugin.routerMatcher.HasRoutes())
	assert.Same(t, config, rt.config.Load())
	assert.ErrorIs(t, plugin.SetModelRouterRouting("missing", configstore.ModelRouterRouting{}), configstore.ErrNotFound)

	require.NoError(t, plugin.RemoveModelRouter("support"))
	_, ok = plugin.modelRouter("support")
	assert.False(t, ok)
}

// TestPersistComplexityTurnTierKeepsEveryRouter pins that a turn classified
// both for a routing rule's complexity_tier and by the model router that took
// it saves both tiers, so a tool continuation resumes each on the tier it
// gave, not on the last one's.
func TestPersistComplexityTurnTierKeepsEveryRouter(t *testing.T) {
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	plugin := &RoutingPlugin{turnTierStore: &complexityTurnTierStore{store: store}, logger: rules.NewMockLogger()}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySessionID, "session-1")
	cacheRouterTier(ctx, "", &complexity.ComplexityResult{Tier: complexity.TierComplex})
	cacheRouterTier(ctx, "support", &complexity.ComplexityResult{Tier: complexity.TierSimple})
	cacheRouterTier(ctx, "billing", nil)

	plugin.persistComplexityTurnTier(ctx)

	for routerID, want := range map[string]string{"": complexity.TierComplex, "support": complexity.TierSimple} {
		tier, found, err := plugin.turnTierStore.load(complexityTurnTierKeyFor(ctx, routerID))
		require.NoError(t, err)
		require.True(t, found, routerID)
		assert.Equal(t, want, tier, routerID)
	}
	_, found, err := plugin.turnTierStore.load(complexityTurnTierKeyFor(ctx, "billing"))
	require.NoError(t, err)
	assert.False(t, found, "a router that produced no tier saves nothing")
}
