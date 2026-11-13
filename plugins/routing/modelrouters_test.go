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

// newModelRouterTestStore opens an empty SQLite config store.
func newModelRouterTestStore(t *testing.T) configstore.ConfigStore {
	t.Helper()
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/config.db"},
	}, rules.NewMockLogger())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(ctx) })
	return store
}

// bootRoutingPlugin starts a routing plugin over a config store, as a gateway
// start does, with a decision model that answers every request COMPLEX.
func bootRoutingPlugin(t *testing.T, store configstore.ConfigStore) *RoutingPlugin {
	t.Helper()
	ctx := context.Background()
	logger := rules.NewMockLogger()
	ruleStore, err := rules.NewLocalStore(ctx, logger, store)
	require.NoError(t, err)
	plugin, err := InitFromStore(ctx, nil, logger, store, ruleStore, NewMockGovernance())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	plugin.SetDecisionRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		tier, confidence := complexity.TierComplex, 0.9
		return &schemas.BifrostDecisionResponse{Answers: []schemas.DecisionAnswer{
			{Type: schemas.DecisionTypeChoice, Name: bifrost.Ptr("complexity_tier"), Choice: &schemas.DecisionScalar{Str: &tier}, Confidence: &confidence},
		}}, nil
	})
	return plugin
}

// decisionRouterConfig is a router config that classifies with the decision
// model.
func decisionRouterConfig() complexity.AnalyzerConfig {
	return complexity.AnalyzerConfig{
		Classifier: complexity.ClassifierDecision,
		Keywords: complexity.EditableKeywordConfig{
			SimpleKeywords: []string{"papaya amber"}, MediumKeywords: []string{"cedar cobalt"}, ComplexKeywords: []string{"obsidian comet"},
		},
	}
}

// routeChatRequest sends a chat request asking for model through the routing
// hook and returns the model it ended on and the router that took it, "" when
// none did.
func routeChatRequest(t *testing.T, plugin *RoutingPlugin, model string) (routedModel, routerID string) {
	t.Helper()
	text := "design a sharded cache with a write-ahead log"
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI, Model: model,
			Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &text}}},
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(ctx, req))
	_, routedModel, _ = req.GetRequestFields()
	routerID, _ = ctx.Value(schemas.BifrostContextKeyModelRouterID).(string)
	return routedModel, routerID
}

// TestModelRouterStoreWritesReachLiveRouting walks two routers through every
// write the API makes, applying after each one what the HTTP server applies
// (the saved router, its targets, or its routing, read back from the store),
// and sends a request after every step. It pins that what an operator saves
// is what the next request is routed by, with nothing left over from the
// step before, and that a gateway restarted over the same store routes the
// same way.
func TestModelRouterStoreWritesReachLiveRouting(t *testing.T) {
	ctx := context.Background()
	store := newModelRouterTestStore(t)
	plugin := bootRoutingPlugin(t, store)

	saved := func(id string) *configstore.ModelRouter {
		t.Helper()
		router, err := store.GetModelRouter(ctx, id)
		require.NoError(t, err)
		return router
	}
	applyRouter := func(id string) { t.Helper(); require.NoError(t, plugin.ReloadModelRouter(saved(id))) }
	applyTargets := func(id string) { t.Helper(); require.NoError(t, plugin.SetModelRouterTargets(id, saved(id).Targets)) }
	applyRouting := func(id string) {
		t.Helper()
		require.NoError(t, plugin.SetModelRouterRouting(id, *saved(id).Routing))
	}
	expect := func(askedFor, wantModel, wantRouter, why string) {
		t.Helper()
		model, router := routeChatRequest(t, plugin, askedFor)
		assert.Equal(t, wantModel, model, why)
		assert.Equal(t, wantRouter, router, why)
	}

	// A new router is saved off: it is served, but takes nothing.
	require.NoError(t, store.CreateModelRouter(ctx, &configstore.ModelRouter{ID: "support", Name: "Support", ComplexityAnalyzerConfig: decisionRouterConfig()}))
	applyRouter("support")
	expect("support-assistant", "support-assistant", "", "a new router is off")

	// Saving its targets does not turn it on.
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", []configstoreTables.TableModelRouterTarget{
		{Tier: complexity.TierComplex, Provider: bifrost.Ptr("anthropic"), Model: bifrost.Ptr("claude-opus"), Weight: 1},
	}))
	applyTargets("support")
	expect("support-assistant", "support-assistant", "", "targets alone route nothing")

	// Turning its routing on for one model name routes that name only.
	require.NoError(t, store.SetModelRouterRouting(ctx, "support", configstore.ModelRouterRouting{
		Enabled: true, Scope: "global", CelExpression: `model == "support-assistant"`, Priority: saved("support").Routing.Priority,
	}))
	applyRouting("support")
	expect("support-assistant", "claude-opus", "support", "the router takes the name its routing matches")
	expect("gpt-4o", "gpt-4o", "", "and no other request")

	// A second router, created after it, catches everything else.
	require.NoError(t, store.CreateModelRouter(ctx, &configstore.ModelRouter{ID: "general", Name: "General", ComplexityAnalyzerConfig: decisionRouterConfig()}))
	applyRouter("general")
	require.Greater(t, saved("general").Routing.Priority, saved("support").Routing.Priority, "a new router goes after the routers already there")
	require.NoError(t, store.SetModelRouterTargets(ctx, "general", []configstoreTables.TableModelRouterTarget{
		{Tier: complexity.TierComplex, Model: bifrost.Ptr("gpt-4o-mini"), Weight: 1},
	}))
	applyTargets("general")
	catchAll := *saved("general").Routing
	catchAll.Enabled = true
	require.NoError(t, store.SetModelRouterRouting(ctx, "general", catchAll))
	applyRouting("general")
	expect("gpt-4o", "gpt-4o-mini", "general", "the catch-all router takes what the first does not")
	expect("support-assistant", "claude-opus", "support", "the earlier priority still goes first")

	// Swapping their priorities in one batch puts the catch-all first.
	supportRouting, generalRouting := *saved("support").Routing, *saved("general").Routing
	supportRouting.Priority, generalRouting.Priority = generalRouting.Priority, supportRouting.Priority
	require.NoError(t, store.SetModelRouterRoutings(ctx, []configstore.ModelRouterRoutingChange{
		{RouterID: "support", Routing: supportRouting}, {RouterID: "general", Routing: generalRouting},
	}))
	applyRouting("support")
	applyRouting("general")
	expect("support-assistant", "gpt-4o-mini", "general", "after the swap the catch-all is tried first")

	// Renaming and resetting a router keep its routing and targets live.
	renamed := *saved("general")
	renamed.Name = "Everything else"
	require.NoError(t, store.UpdateModelRouter(ctx, &renamed))
	applyRouter("general")
	defaults := complexity.DefaultAnalyzerConfig()
	_, err := store.ResetModelRouter(ctx, "general", &defaults)
	require.NoError(t, err)
	applyRouter("general")
	expect("gpt-4o", "gpt-4o-mini", "general", "a rename and a reset change neither routing nor targets")
	rt, ok := plugin.modelRouter("general")
	require.True(t, ok)
	assert.Equal(t, "Everything else", rt.displayName())

	// Replacing targets takes effect at once, and clearing them keeps the
	// request's model while the router still takes the request.
	require.NoError(t, store.SetModelRouterTargets(ctx, "general", []configstoreTables.TableModelRouterTarget{
		{Tier: complexity.TierComplex, Model: bifrost.Ptr("gpt-4.1"), Weight: 1},
	}))
	applyTargets("general")
	expect("gpt-4o", "gpt-4.1", "general", "new targets route the next request")
	require.NoError(t, store.SetModelRouterTargets(ctx, "general", []configstoreTables.TableModelRouterTarget{}))
	applyTargets("general")
	expect("gpt-4o", "gpt-4o", "general", "a router without targets keeps the model")

	// A gateway restarted over the same store routes the same way.
	restarted := bootRoutingPlugin(t, store)
	model, router := routeChatRequest(t, restarted, "gpt-4o")
	assert.Equal(t, "gpt-4o", model)
	assert.Equal(t, "general", router, "the restarted gateway serves the stored routing")

	// Deleting the catch-all hands its requests back to the router behind it.
	require.NoError(t, store.DeleteModelRouter(ctx, "general"))
	require.NoError(t, plugin.RemoveModelRouter("general"))
	expect("support-assistant", "claude-opus", "support", "the router behind a deleted one takes its requests")
	expect("gpt-4o", "gpt-4o", "", "and nothing takes the rest")

	// Turning the last router off leaves no route, so the step is skipped.
	off := *saved("support").Routing
	off.Enabled = false
	require.NoError(t, store.SetModelRouterRouting(ctx, "support", off))
	applyRouting("support")
	expect("support-assistant", "support-assistant", "", "a router turned off takes nothing")
	assert.False(t, plugin.routerMatcher.HasRoutes())
}

// TestRoutingPluginBootSkipsOnlyTheRouterItCannotServe pins that one bad
// stored router cannot keep the others from routing after a restart. A router
// whose stored condition no longer compiles is served for its status but
// takes no requests; a router that needs a KV store the gateway does not have
// is left out; and the healthy router behind both still routes.
func TestRoutingPluginBootSkipsOnlyTheRouterItCannotServe(t *testing.T) {
	ctx := context.Background()
	store := newModelRouterTestStore(t)

	// The store does not compile conditions, so one written by an older
	// version, or by hand, can be there.
	require.NoError(t, store.CreateModelRouter(ctx, &configstore.ModelRouter{
		ID: "stale", Name: "Stale", ComplexityAnalyzerConfig: decisionRouterConfig(),
		Routing: &configstore.ModelRouterRouting{Enabled: true, Scope: "global", Priority: 0, CelExpression: `complexity_tier == "COMPLEX"`},
	}))
	sessionConfig := decisionRouterConfig()
	sessionConfig.Session = &configstore.ComplexitySessionConfig{Enabled: true}
	require.NoError(t, store.CreateModelRouter(ctx, &configstore.ModelRouter{
		ID: "sessions", Name: "Sessions", ComplexityAnalyzerConfig: sessionConfig,
		Routing: &configstore.ModelRouterRouting{Enabled: true, Scope: "global", Priority: 1},
	}))
	require.NoError(t, store.CreateModelRouter(ctx, &configstore.ModelRouter{
		ID: "healthy", Name: "Healthy", ComplexityAnalyzerConfig: decisionRouterConfig(),
		Routing: &configstore.ModelRouterRouting{Enabled: true, Scope: "global", Priority: 2},
	}))
	for _, id := range []string{"stale", "sessions", "healthy"} {
		require.NoError(t, store.SetModelRouterTargets(ctx, id, []configstoreTables.TableModelRouterTarget{
			{Tier: complexity.TierComplex, Model: bifrost.Ptr(id + "-model"), Weight: 1},
		}))
	}

	plugin := bootRoutingPlugin(t, store)

	_, served := plugin.modelRouter("stale")
	assert.True(t, served, "a router whose condition does not compile is still served, so its status and tests work")
	_, served = plugin.modelRouter("sessions")
	assert.False(t, served, "a router that needs a KV store is not served without one")
	_, served = plugin.modelRouter("healthy")
	assert.True(t, served)

	model, router := routeChatRequest(t, plugin, "gpt-4o")
	assert.Equal(t, "healthy-model", model)
	assert.Equal(t, "healthy", router, "the routers that cannot take requests are passed over")
}
