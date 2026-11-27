package routing

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
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

	plugin, err := Init(ctx, nil, logger, store, liveFrom(t, store), NewMockGovernance())
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

	// Replacing a router's targets, read from its record, leaves its classifiers alone.
	config := rt.config.Load()
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", nil))
	refreshRouter(t, plugin, "support")
	assert.Empty(t, rt.loadTargets())
	assert.Same(t, config, rt.config.Load())

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
	plugin := &RoutingPlugin{sessions: stringTableForTest(t), logger: rules.NewMockLogger()}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeySessionID, "session-1")
	cacheRouterTier(ctx, "", &complexity.ComplexityResult{Tier: complexity.TierComplex})
	cacheRouterTier(ctx, "support", &complexity.ComplexityResult{Tier: complexity.TierSimple})
	cacheRouterTier(ctx, "billing", nil)

	plugin.persistComplexityTurnTier(ctx)

	for routerID, want := range map[string]string{"": complexity.TierComplex, "support": complexity.TierSimple} {
		tier, found, err := plugin.loadTurnTier(complexityTurnTierKeyFor(ctx, routerID))
		require.NoError(t, err)
		require.True(t, found, routerID)
		assert.Equal(t, want, tier, routerID)
	}
	_, found, err := plugin.loadTurnTier(complexityTurnTierKeyFor(ctx, "billing"))
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
	plugin, err := Init(ctx, nil, logger, store, liveFrom(t, store), NewMockGovernance())
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
	applyRouter := func(id string) {
		t.Helper()
		refreshRouter(t, plugin, id)
		require.NoError(t, plugin.ReloadModelRouter(saved(id)))
	}
	applyTargets := func(id string) { t.Helper(); refreshRouter(t, plugin, id) }
	applyRouting := func(id string) {
		t.Helper()
		refreshRouter(t, plugin, id)
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
// takes no requests; a router that needs a session store the gateway does not have
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
	assert.False(t, served, "a router that needs a session store is not served without one")
	_, served = plugin.modelRouter("healthy")
	assert.True(t, served)

	model, router := routeChatRequest(t, plugin, "gpt-4o")
	assert.Equal(t, "healthy-model", model)
	assert.Equal(t, "healthy", router, "the routers that cannot take requests are passed over")
}

// liveFrom returns a live store loaded from store's database, closed when the test ends.
func liveFrom(t *testing.T, store configstore.ConfigStore) *livestore.Store {
	t.Helper()
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	require.NoError(t, live.LoadAll(context.Background(), store.DB()))
	return live
}

// refreshRouter brings router id's saved row into the plugin's live store, as the HTTP server does
// after a write.
func refreshRouter(t *testing.T, plugin *RoutingPlugin, id string) {
	t.Helper()
	require.NoError(t, plugin.rules.LiveStore().Refresh(context.Background(), []livestore.Ref{{Kind: livestore.KindModelRouter, ID: id}}))
}

// newTestRouterMatcher builds an empty model router matcher over a live store of its own.
func newTestRouterMatcher(t *testing.T) *rules.ModelRouterMatcher {
	t.Helper()
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	matcher, err := rules.NewModelRouterMatcher(live)
	require.NoError(t, err)
	return matcher
}

// embeddedTexts counts how often each text was embedded.
type embeddedTexts struct {
	mu     sync.Mutex
	counts map[string]int
}

// embed is classifyTestEmbeddings, counting every text it is given.
func (e *embeddedTexts) embed(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	e.mu.Lock()
	for _, text := range embeddingRequestTexts(req.Input) {
		e.counts[text]++
	}
	e.mu.Unlock()
	return classifyTestEmbeddings(ctx, req)
}

// count returns how often text was embedded.
func (e *embeddedTexts) count(text string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.counts[text]
}

// sharedClassifierTestPlugin is a routing plugin with a session table whose
// routers embed with classifyTestEmbeddings, and the count of what they
// embedded.
func sharedClassifierTestPlugin(t testing.TB) (*RoutingPlugin, *embeddedTexts) {
	t.Helper()
	logger := rules.NewMockLogger()
	ruleStore, err := rules.NewTestRules(context.Background(), t.TempDir(), logger)
	require.NoError(t, err)
	t.Cleanup(ruleStore.Close)
	p, err := InitFromStore(context.Background(), &Config{Sessions: stringTableForTest(t)}, logger, nil, ruleStore.Store, NewMockGovernance())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, p.Cleanup()) })
	embedded := &embeddedTexts{counts: map[string]int{}}
	p.SetEmbeddingRequestExecutor(embedded.embed)
	return p, embedded
}

// semanticRouterConfig is a router config that classifies semantically against
// one phrase a tier, with the given SIMPLE phrase.
func semanticRouterConfig(simplePhrase string) complexity.AnalyzerConfig {
	return complexity.AnalyzerConfig{
		Keywords: configstore.ComplexityEditableKeywordConfig{
			SimpleKeywords:  []string{simplePhrase},
			MediumKeywords:  []string{"an implementation detail question"},
			ComplexKeywords: []string{"a deep architectural tradeoff analysis"},
		},
		Semantic: &configstore.ComplexitySemanticConfig{Provider: "openai", EmbeddingModel: "test-embedding-model", MinSimilarity: 0.9},
	}
}

// serveReadyRouter serves a router with the config and waits for its phrase
// bank to be ready, returning its runtime.
func serveReadyRouter(t testing.TB, p *RoutingPlugin, id string, config complexity.AnalyzerConfig) *modelRouterRuntime {
	t.Helper()
	require.NoError(t, p.ReloadModelRouter(&configstore.ModelRouter{ID: id, Name: id, ComplexityAnalyzerConfig: config}))
	require.Eventually(t, func() bool {
		status, ok := p.ModelRouterSemanticStatus(id)
		return ok && status.State == complexity.SemanticStatusReady
	}, 5*time.Second, 10*time.Millisecond, "router %s should become ready", id)
	rt, ok := p.modelRouter(id)
	require.True(t, ok)
	return rt
}

// TestModelRoutersWithTheSameClassifierSettingsShareClassifiers pins what the
// plugin builds for routers that classify alike: one set of classifiers, built
// and warmed for the first and joined by the rest, so their phrases are
// embedded once however many routers there are. A router whose settings differ
// by one phrase, and a router with a suggestions block, get classifiers of
// their own. A change made to every classifier reaches a shared set once.
func TestModelRoutersWithTheSameClassifierSettingsShareClassifiers(t *testing.T) {
	p, embedded := sharedClassifierTestPlugin(t)
	const phrase = "a casual greeting"

	first := serveReadyRouter(t, p, "first", semanticRouterConfig(phrase))
	second := serveReadyRouter(t, p, "second", semanticRouterConfig(phrase))
	third := serveReadyRouter(t, p, "third", semanticRouterConfig(phrase))

	require.Same(t, first.semantic, second.semantic)
	require.Same(t, first.semantic, third.semantic)
	require.Same(t, first.llm, second.llm)
	require.NotSame(t, first, second, "each router still has a runtime of its own")
	require.Equal(t, 1, embedded.count(phrase), "the phrases are embedded once for the three routers")
	require.True(t, first.built)
	require.False(t, second.built)
	require.Equal(t, p.capture != nil, first.capture != nil, "the capture stays with the router the classifiers were built for")
	require.Nil(t, second.capture)
	require.Nil(t, third.capture)
	require.NotEmpty(t, first.classifierKey())
	require.Equal(t, first.classifierKey(), third.classifierKey())

	const otherPhrase = "a casual greeting between friends"
	different := serveReadyRouter(t, p, "different", semanticRouterConfig(otherPhrase))
	require.NotSame(t, first.semantic, different.semantic, "one phrase apart is a different phrase bank")
	require.NotEqual(t, first.classifierKey(), different.classifierKey())

	withSuggestions := semanticRouterConfig(phrase)
	withSuggestions.Suggestions = &configstore.ComplexitySuggestionsConfig{}
	suggesting := serveReadyRouter(t, p, "suggesting", withSuggestions)
	require.NotSame(t, first.semantic, suggesting.semantic, "what a router with a suggestions block captures is keyed by the router")
	require.Empty(t, suggesting.classifierKey())
	require.True(t, suggesting.built)

	withSession := semanticRouterConfig(phrase)
	withSession.Session = &configstore.ComplexitySessionConfig{Enabled: true}
	remembering := serveReadyRouter(t, p, "remembering", withSession)
	require.Same(t, first.semantic, remembering.semantic, "session memory is each router's own, not part of how it classifies")
	require.True(t, remembering.sessionEnabled.Load())
	require.False(t, first.sessionEnabled.Load())

	visited := 0
	p.forEachRouterClassifiers(func(*modelRouterRuntime) { visited++ })
	require.Equal(t, 3, visited, "the shared set, the different one and the suggesting one")
	require.Len(t, p.allClassifiers(), 4, "the Complexity Router page's, then one for each set")
}

// TestRoutersMadeFromTierRulesShareOneClassifier pins what a gateway with
// routing rules on complexity_tier runs once those rules are model routers.
// The rules all classified with one classifier, the Complexity Router's. Each
// router made from them is given those settings without a suggestions block,
// so however many routers the rules become, they still classify with one: the
// phrases are embedded once, and session memory stays each router's own.
func TestRoutersMadeFromTierRulesShareOneClassifier(t *testing.T) {
	p, embedded := sharedClassifierTestPlugin(t)
	const phrase = "a casual greeting"
	settings := semanticRouterConfig(phrase)
	settings.Session = &configstore.ComplexitySessionConfig{Enabled: true}
	// A suggestions block, on or off, stops a router sharing its classifiers.
	settings.Suggestions = &configstore.ComplexitySuggestionsConfig{Enabled: false}
	rule := func(id, condition string, priority int) configstoreTables.TableRoutingRule {
		return configstoreTables.TableRoutingRule{
			ID: id, Name: id, CelExpression: condition, Enabled: bifrost.Ptr(true), Scope: "global", Priority: priority,
			Targets: []configstoreTables.TableRoutingTarget{{Model: bifrost.Ptr("model-" + id), Weight: 1}},
		}
	}
	// Three rules that cannot become one router: their conditions differ.
	migration, err := configstore.ModelRoutersFromRoutingRules([]configstoreTables.TableRoutingRule{
		rule("simple-gpt", `model == "gpt-4o" && complexity_tier == "SIMPLE"`, 0),
		rule("complex", `complexity_tier == "COMPLEX"`, 1),
		rule("pro", `headers["x-plan"] == "pro" || complexity_tier == "MEDIUM"`, 2),
	}, &settings, nil)
	require.NoError(t, err)

	var runtimes []*modelRouterRuntime
	for _, router := range migration.Routers {
		if router.Mode == configstoreTables.ModelRouterModeClassifier {
			runtimes = append(runtimes, serveReadyRouter(t, p, router.ID, router.ComplexityAnalyzerConfig))
		}
	}
	require.Len(t, runtimes, 3)
	require.NotEmpty(t, runtimes[0].classifierKey(), "a router made from a tier rule can share its classifiers")
	for _, rt := range runtimes {
		require.Same(t, runtimes[0].semantic, rt.semantic, "router %s", rt.id)
		require.Equal(t, runtimes[0].classifierKey(), rt.classifierKey(), "router %s", rt.id)
		require.True(t, rt.sessionEnabled.Load(), "router %s keeps the session setting", rt.id)
	}
	require.Equal(t, 1, embedded.count(phrase), "the phrases are embedded once for every router the rules became")
}

// TestModelRouterMovesToOtherClassifiersOnlyWhenItMust walks the edits that
// change which classifiers a router uses. A router that shares with no one
// keeps its classifiers through every edit, as it did before routers shared,
// so its previous phrase bank serves while the new one warms. A router in a
// group moves when its classifier settings change, the routers it leaves keep
// theirs untouched, and an edit to what is the router's own moves nothing.
func TestModelRouterMovesToOtherClassifiersOnlyWhenItMust(t *testing.T) {
	const phrase, otherPhrase = "a casual greeting", "a casual greeting between friends"

	t.Run("a router that shares with no one keeps its classifiers", func(t *testing.T) {
		p, _ := sharedClassifierTestPlugin(t)
		solo := serveReadyRouter(t, p, "solo", semanticRouterConfig(phrase))
		classifier, key := solo.semantic, solo.classifierKey()

		edited := serveReadyRouter(t, p, "solo", semanticRouterConfig(otherPhrase))
		require.Same(t, solo, edited)
		require.Same(t, classifier, edited.semantic)
		require.NotEqual(t, key, edited.classifierKey(), "the key follows the settings")

		withSuggestions := semanticRouterConfig(otherPhrase)
		withSuggestions.Suggestions = &configstore.ComplexitySuggestionsConfig{}
		suggesting := serveReadyRouter(t, p, "solo", withSuggestions)
		require.Same(t, classifier, suggesting.semantic, "the classifiers were built for this router, so they can capture for it")
		require.Empty(t, suggesting.classifierKey())

		plain := serveReadyRouter(t, p, "solo", semanticRouterConfig(otherPhrase))
		require.Same(t, classifier, plain.semantic)
		require.NotEmpty(t, plain.classifierKey())
	})

	t.Run("a classifier edit moves one router and leaves the rest", func(t *testing.T) {
		p, embedded := sharedClassifierTestPlugin(t)
		first := serveReadyRouter(t, p, "first", semanticRouterConfig(phrase))
		serveReadyRouter(t, p, "second", semanticRouterConfig(phrase))
		shared := first.semantic

		moved := serveReadyRouter(t, p, "second", semanticRouterConfig(otherPhrase))
		require.NotSame(t, shared, moved.semantic)
		require.True(t, moved.built)
		kept, _ := p.modelRouter("first")
		require.Same(t, first, kept)
		require.True(t, shared.Serving(), "the router left behind keeps a serving phrase bank")
		require.Equal(t, 1, embedded.count(phrase), "and its phrases are not embedded again")

		own := moved.semantic
		back := serveReadyRouter(t, p, "second", semanticRouterConfig(phrase))
		require.Same(t, shared, back.semantic, "the same settings again join the classifiers that have them")
		require.False(t, own.Serving(), "the classifiers no router uses any more are closed")
		require.Equal(t, 1, embedded.count(phrase), "joining warms nothing")
	})

	t.Run("an edit to what is the router's own moves nothing", func(t *testing.T) {
		p, embedded := sharedClassifierTestPlugin(t)
		first := serveReadyRouter(t, p, "first", semanticRouterConfig(phrase))
		second := serveReadyRouter(t, p, "second", semanticRouterConfig(phrase))

		withSession := semanticRouterConfig(phrase)
		withSession.Session = &configstore.ComplexitySessionConfig{Enabled: true}
		edited := serveReadyRouter(t, p, "second", withSession)
		require.Same(t, second, edited)
		require.Same(t, first.semantic, edited.semantic)
		require.True(t, edited.sessionEnabled.Load())
		require.False(t, first.sessionEnabled.Load())
		require.Equal(t, 1, embedded.count(phrase), "the shared phrase bank is not warmed again for one router's session setting")
	})

	t.Run("a suggestions block gives a router in a group classifiers of its own", func(t *testing.T) {
		p, _ := sharedClassifierTestPlugin(t)
		first := serveReadyRouter(t, p, "first", semanticRouterConfig(phrase))
		second := serveReadyRouter(t, p, "second", semanticRouterConfig(phrase))
		shared := first.semantic
		withSuggestions := semanticRouterConfig(phrase)
		withSuggestions.Suggestions = &configstore.ComplexitySuggestionsConfig{}

		// The router that joined: the shared classifiers capture under the first router's name.
		suggesting := serveReadyRouter(t, p, "second", withSuggestions)
		require.NotSame(t, second, suggesting)
		require.NotSame(t, shared, suggesting.semantic)
		require.True(t, suggesting.built)
		require.True(t, shared.Serving())

		// The router they were built for, while a third still uses them.
		third := serveReadyRouter(t, p, "third", semanticRouterConfig(phrase))
		require.Same(t, shared, third.semantic)
		builder := serveReadyRouter(t, p, "first", withSuggestions)
		require.NotSame(t, shared, builder.semantic, "the router that changed is the one that moves")
		require.True(t, shared.Serving(), "the third router keeps the classifiers it joined")
		still, _ := p.modelRouter("third")
		require.Same(t, third, still)
	})
}

// TestSharedClassifiersCloseWithTheirLastRouter pins that removing a router
// closes its classifiers only when no other router classifies with them, the
// router they were built for included.
func TestSharedClassifiersCloseWithTheirLastRouter(t *testing.T) {
	p, _ := sharedClassifierTestPlugin(t)
	const phrase = "a casual greeting"
	first := serveReadyRouter(t, p, "first", semanticRouterConfig(phrase))
	serveReadyRouter(t, p, "second", semanticRouterConfig(phrase))
	shared := first.semantic

	require.NoError(t, p.RemoveModelRouter("first"))
	require.True(t, shared.Serving(), "the second router still classifies with them")
	status, ok := p.ModelRouterSemanticStatus("second")
	require.True(t, ok)
	require.Equal(t, complexity.SemanticStatusReady, status.State)

	require.NoError(t, p.RemoveModelRouter("second"))
	require.False(t, shared.Serving(), "no router is left to use them")
}

// BenchmarkServeModelRoutersWithTheSameSettings measures what a gateway holds
// for classifier routers made from one configuration, which is what routing
// rules on complexity_tier become: the heap each router adds once every one is
// ready, the goroutines the gateway runs for them, and how often their phrases
// were embedded. Run it with -benchtime=1x: one iteration serves every router.
func BenchmarkServeModelRoutersWithTheSameSettings(b *testing.B) {
	const phrase = "a casual greeting"
	for _, n := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("%d routers", n), func(b *testing.B) {
			for b.Loop() {
				p, embedded := sharedClassifierTestPlugin(b)
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				goroutines := runtime.NumGoroutine()
				for i := range n {
					require.NoError(b, p.ReloadModelRouter(&configstore.ModelRouter{
						ID: fmt.Sprintf("router-%04d", i), Name: fmt.Sprintf("router-%04d", i), ComplexityAnalyzerConfig: semanticRouterConfig(phrase),
					}))
				}
				require.Eventually(b, func() bool {
					for id := range p.modelRouters() {
						if status, _ := p.ModelRouterSemanticStatus(id); status.State != complexity.SemanticStatusReady {
							return false
						}
					}
					return true
				}, time.Minute, 20*time.Millisecond, "every router should become ready")
				runtime.GC()
				runtime.ReadMemStats(&after)
				b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/float64(n), "heap-bytes/router")
				b.ReportMetric(float64(runtime.NumGoroutine()-goroutines), "goroutines")
				b.ReportMetric(float64(embedded.count(phrase)), "phrase-embeddings")
			}
		})
	}
}
