package complexity_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// Texts the decision fake answers with a fixed tier.
const (
	complexText = "design a sharded cache with a write-ahead log"
	mediumText  = "refactor this module to remove the global state"
	simpleText  = "what does this regex match"
)

// modelRoutingDecision answers complexText, mediumText and simpleText with
// their tiers.
func modelRoutingDecision() *decisionRecorder {
	return &decisionRecorder{tiers: map[string]string{
		complexText: complexity.TierComplex,
		mediumText:  complexity.TierMedium,
		simpleText:  complexity.TierSimple,
	}}
}

// decisionModelRouter is a router that classifies with the decision model,
// sends each tier to the given targets, and takes no requests until it is
// given routing.
func decisionModelRouter(id, name string, targets ...configstoreTables.TableModelRouterTarget) configstore.ModelRouter {
	return configstore.ModelRouter{ID: id, Name: name, ComplexityAnalyzerConfig: *decisionAnalyzerTestConfig(), Targets: targets}
}

// globalRouting turns a router on for every request in the global scope that
// cel holds for.
func globalRouting(priority int, cel string) *configstore.ModelRouterRouting {
	return &configstore.ModelRouterRouting{Enabled: true, Scope: "global", Priority: priority, CelExpression: cel}
}

// tierTarget is one target of a model router.
func tierTarget(tier, provider, model string) configstoreTables.TableModelRouterTarget {
	target := configstoreTables.TableModelRouterTarget{Tier: tier, Weight: 1}
	if provider != "" {
		target.Provider = schemas.Ptr(provider)
	}
	if model != "" {
		target.Model = schemas.Ptr(model)
	}
	return target
}

// supportRouter takes every request, and sends SIMPLE to gpt-4o-mini and
// COMPLEX to claude-opus, leaving MEDIUM on the request's model.
func supportRouter(name string) configstore.ModelRouter {
	router := decisionModelRouter("support", name,
		tierTarget(complexity.TierSimple, "openai", "gpt-4o-mini"),
		tierTarget(complexity.TierComplex, "anthropic", "claude-opus"))
	router.Routing = globalRouting(0, "")
	return router
}

// serveGeneralRouter serves a router with the targets that takes every
// request.
func serveGeneralRouter(t *testing.T, plugin *routing.RoutingPlugin, targets ...configstoreTables.TableModelRouterTarget) {
	t.Helper()
	router := decisionModelRouter("general", "General", targets...)
	router.Routing = globalRouting(0, "")
	require.NoError(t, plugin.ReloadModelRouter(&router))
}

// modelRoutingTestPlugin builds a routing plugin whose routers, and the
// complexity config routing rules read complexity_tier from, all classify
// with the decision fake, serving the given routing rules and routers.
func modelRoutingTestPlugin(
	t *testing.T,
	governance *routing.MockGovernance,
	decision *decisionRecorder,
	routingRules []configstoreTables.TableRoutingRule,
	routers []configstore.ModelRouter,
) *routing.RoutingPlugin {
	t.Helper()
	return modelRoutingTestPluginWithConfig(t, nil, governance, decision.execute, routingRules, routers)
}

// modelRoutingTestPluginWithConfig is modelRoutingTestPlugin with a plugin
// config, for the KV store session tiers and tool continuations need, any
// governance, and any decision executor.
func modelRoutingTestPluginWithConfig(
	t testing.TB,
	config *routing.Config,
	governance routing.Governance,
	decide routing.DecisionRequestExecutor,
	routingRules []configstoreTables.TableRoutingRule,
	routers []configstore.ModelRouter,
) *routing.RoutingPlugin {
	t.Helper()
	ctx := context.Background()
	logger := rules.NewMockLogger()
	ruleStore, err := rules.NewLocalStore(ctx, logger, nil)
	require.NoError(t, err)
	for i := range routingRules {
		require.NoError(t, ruleStore.UpsertRule(ctx, &routingRules[i]))
	}
	plugin, err := routing.InitFromStore(ctx, config, logger, nil, ruleStore, governance)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	plugin.SetDecisionRequestExecutor(decide)
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(decisionAnalyzerTestConfig()))
	for i := range routers {
		require.NoError(t, plugin.ReloadModelRouter(&routers[i]))
	}
	return plugin
}

// runPreRequest runs the routing hook on a chat request for text.
func runPreRequest(t *testing.T, plugin *routing.RoutingPlugin, ctx *schemas.BifrostContext, text string) *schemas.BifrostRequest {
	t.Helper()
	req := llmComplexityChatRequest(text)
	require.NoError(t, plugin.PreRequestHook(ctx, req))
	return req
}

// newRequestContext returns an empty request context.
func newRequestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// TestModelRouterRoutesByTier proves the step end to end: the router that
// takes the request classifies it, its target for that tier replaces the
// provider and model before the virtual key allowlist and load balancing see
// them, and the request records the router and the tier.
func TestModelRouterRoutesByTier(t *testing.T) {
	governance := routing.NewMockGovernance()
	decision := modelRoutingDecision()
	plugin := modelRoutingTestPlugin(t, governance, decision, nil, []configstore.ModelRouter{supportRouter("Support")})

	ctx := newRequestContext()
	req := runPreRequest(t, plugin, ctx, complexText)

	provider, model, _ := req.GetRequestFields()
	assert.Equal(t, schemas.ModelProvider("anthropic"), provider)
	assert.Equal(t, "claude-opus", model)
	assert.Equal(t, []string{"claude-opus"}, governance.AllowlistModels, "the allowlist sees the model the router chose")
	assert.Equal(t, []string{"claude-opus"}, governance.LoadBalancedModels)

	assert.Equal(t, complexity.TierComplex, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	assert.Equal(t, complexity.MechanismDecision, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	assert.Equal(t, "support", ctx.Value(schemas.BifrostContextKeyModelRouterID))
	assert.Equal(t, "Support", ctx.Value(schemas.BifrostContextKeyModelRouterName))
	engines, _ := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
	assert.Contains(t, engines, schemas.RoutingEngineModelRouter)
	assert.Contains(t, routingLogs(ctx), "Model router 'Support' tier=COMPLEX → provider=anthropic, model=claude-opus")
}

// TestModelRoutersMatchInOrder pins how the router that takes a request is
// picked: priority orders the routers, a condition can match the model name
// an app asks for, only the first match classifies, a router turned off
// takes nothing, and new targets apply without touching the routing.
func TestModelRoutersMatchInOrder(t *testing.T) {
	byName := decisionModelRouter("by-name", "By name", tierTarget(complexity.TierSimple, "openai", "gpt-4o-mini"))
	byName.Routing = globalRouting(1, `model == "support-assistant"`)
	catchAll := decisionModelRouter("catch-all", "Catch all", tierTarget(complexity.TierSimple, "anthropic", "claude-haiku"))
	catchAll.Routing = globalRouting(2, "")
	decision := modelRoutingDecision()
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{byName, catchAll})

	run := func(model string) (*schemas.BifrostContext, string) {
		t.Helper()
		ctx := newRequestContext()
		req := llmComplexityChatRequest(simpleText)
		req.ChatRequest.Model = model
		require.NoError(t, plugin.PreRequestHook(ctx, req))
		_, routed, _ := req.GetRequestFields()
		return ctx, routed
	}

	ctx, model := run("support-assistant")
	assert.Equal(t, "gpt-4o-mini", model, "the router's SIMPLE model replaces the name the app asked for")
	assert.Equal(t, "by-name", ctx.Value(schemas.BifrostContextKeyModelRouterID))
	assert.Equal(t, 1, decision.calls(), "only the router that takes the request classifies it")

	ctx, model = run("gpt-4o")
	assert.Equal(t, "claude-haiku", model)
	assert.Equal(t, "catch-all", ctx.Value(schemas.BifrostContextKeyModelRouterID))

	require.NoError(t, plugin.SetModelRouterTargets("catch-all", []configstoreTables.TableModelRouterTarget{
		tierTarget(complexity.TierSimple, "anthropic", "claude-sonnet"),
	}))
	_, model = run("gpt-4o")
	assert.Equal(t, "claude-sonnet", model)

	off := *catchAll.Routing
	off.Enabled = false
	require.NoError(t, plugin.SetModelRouterRouting("catch-all", off))
	ctx, model = run("gpt-4o")
	assert.Equal(t, "gpt-4o", model, "a router turned off takes nothing")
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
}

// TestModelRouterKeepsModel pins the cases that leave the request on the
// model the routing rules chose: a tier the router has no target for, and no
// router on.
func TestModelRouterKeepsModel(t *testing.T) {
	t.Run("tier without a model", func(t *testing.T) {
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{supportRouter("Support")})
		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, mediumText)
		_, model, _ := req.GetRequestFields()
		assert.Equal(t, "gpt-4o", model)
		assert.Equal(t, complexity.TierMedium, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier), "the tier is still recorded")
		assert.Equal(t, "support", ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Contains(t, routingLogs(ctx), "has no model for tier=MEDIUM")
	})

	t.Run("no router on", func(t *testing.T) {
		decision := modelRoutingDecision()
		off := supportRouter("Support")
		off.Routing = nil
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{off})
		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		_, model, _ := req.GetRequestFields()
		assert.Equal(t, "gpt-4o", model)
		assert.Zero(t, decision.calls(), "nothing asks a router to classify")
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})
}

// TestModelRouterKeyPin pins the agreed pin rule: moving to another provider
// clears a key pinned for the old one, a target may pin its own, and staying
// on the provider keeps the pin.
func TestModelRouterKeyPin(t *testing.T) {
	run := func(t *testing.T, target configstoreTables.TableModelRouterTarget) *schemas.BifrostContext {
		t.Helper()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, nil)
		serveGeneralRouter(t, plugin, target)
		ctx := newRequestContext()
		ctx.SetValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID, "openai-key")
		runPreRequest(t, plugin, ctx, complexText)
		return ctx
	}

	ctx := run(t, tierTarget(complexity.TierComplex, "anthropic", "claude-opus"))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID), "an openai key cannot serve anthropic")

	pinned := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	pinned.KeyID = schemas.Ptr("anthropic-key")
	ctx = run(t, pinned)
	assert.Equal(t, "anthropic-key", ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID))

	ctx = run(t, tierTarget(complexity.TierComplex, "", "gpt-4o-mini"))
	assert.Equal(t, "openai-key", ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID), "the same provider keeps its pin")
}

// TestModelRouterClearsRuleTTFT pins that a routing rule's first-token
// deadline, set for the rule's target, does not follow the request to another
// provider or model a router picks, and stays when the router keeps the
// rule's target.
func TestModelRouterClearsRuleTTFT(t *testing.T) {
	run := func(t *testing.T, target configstoreTables.TableModelRouterTarget) *schemas.BifrostContext {
		t.Helper()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, nil)
		serveGeneralRouter(t, plugin, target)
		ctx := newRequestContext()
		ctx.SetValue(schemas.BifrostContextKeyStreamFirstTokenTimeout, 2*time.Second)
		runPreRequest(t, plugin, ctx, complexText)
		return ctx
	}

	ctx := run(t, tierTarget(complexity.TierComplex, "", "gpt-4o-mini"))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout), "another model drops the rule's deadline")

	ctx = run(t, tierTarget(complexity.TierComplex, "anthropic", ""))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout), "another provider drops the rule's deadline")

	ctx = run(t, tierTarget(complexity.TierComplex, "openai", "gpt-4o"))
	assert.Equal(t, 2*time.Second, ctx.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout), "the rule's own target keeps it")
}

// TestModelRouterClassificationLogEngine pins that a router's classifier logs
// are tagged with the model router engine, so a request no routing rule
// matched shows no routing-rule decision logs.
func TestModelRouterClassificationLogEngine(t *testing.T) {
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, nil)
	serveGeneralRouter(t, plugin, tierTarget(complexity.TierComplex, "", "gpt-4o-mini"))
	ctx := newRequestContext()
	runPreRequest(t, plugin, ctx, complexText)

	logs := ctx.GetRoutingEngineLogs()
	require.NotEmpty(t, logs)
	for _, entry := range logs {
		assert.Equal(t, schemas.RoutingEngineModelRouter, entry.Engine, "log %q", entry.Message)
	}
}

// TestModelRouterFallbacks pins the agreed fallback rule: a target's
// fallbacks replace the request's chain, and a target without any keeps it.
// A fallback without a model keeps the incoming one, as for routing rules.
func TestModelRouterFallbacks(t *testing.T) {
	run := func(t *testing.T, target configstoreTables.TableModelRouterTarget) []schemas.Fallback {
		t.Helper()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, nil)
		serveGeneralRouter(t, plugin, target)
		req := llmComplexityChatRequest(complexText)
		req.ChatRequest.Fallbacks = []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-4o-mini"}}
		require.NoError(t, plugin.PreRequestHook(newRequestContext(), req))
		_, _, fallbacks := req.GetRequestFields()
		return fallbacks
	}

	withFallbacks := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	withFallbacks.ParsedFallbacks = []configstoreTables.RoutingFallback{{Fallback: schemas.Fallback{Provider: schemas.Anthropic, Model: "claude-sonnet"}}}
	assert.Equal(t, []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-sonnet"}}, run(t, withFallbacks))
	assert.Equal(t, []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-4o-mini"}},
		run(t, tierTarget(complexity.TierComplex, "anthropic", "claude-opus")))

	modelless := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	modelless.ParsedFallbacks = []configstoreTables.RoutingFallback{{Fallback: schemas.Fallback{Provider: schemas.Azure}}}
	assert.Equal(t, []schemas.Fallback{{Provider: schemas.Azure, Model: "gpt-4o"}}, run(t, modelless))
}

// TestModelRouterRunsAfterRoutingRules pins the order of the two steps: a
// router's condition sees the model a routing rule chose.
func TestModelRouterRunsAfterRoutingRules(t *testing.T) {
	routeToMini := configstoreTables.TableRoutingRule{
		ID: "to-mini", Name: "to-mini", CelExpression: `model == "gpt-4o"`, Enabled: schemas.Ptr(true), Scope: "global",
		Targets: []configstoreTables.TableRoutingTarget{{Model: schemas.Ptr("gpt-4o-mini"), Weight: 1}},
	}
	onMini := decisionModelRouter("general", "General", tierTarget(complexity.TierComplex, "anthropic", "claude-opus"))
	onMini.Routing = globalRouting(0, `model == "gpt-4o-mini"`)

	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(),
		[]configstoreTables.TableRoutingRule{routeToMini}, []configstore.ModelRouter{onMini})
	req := runPreRequest(t, plugin, newRequestContext(), complexText)
	_, model, _ := req.GetRequestFields()
	assert.Equal(t, "claude-opus", model)

	plugin = modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{onMini})
	req = runPreRequest(t, plugin, newRequestContext(), complexText)
	_, model, _ = req.GetRequestFields()
	assert.Equal(t, "gpt-4o", model, "without the routing rule the condition does not hold")
}

// TestComplexityTierStaysWithTheComplexityConfig pins that routers and a
// routing rule's complexity_tier are separate: complexity_tier is classified
// with the Complexity Router page's config, which records no router, and a
// router that then takes the request classifies it again on its own.
func TestComplexityTierStaysWithTheComplexityConfig(t *testing.T) {
	complexToMini := configstoreTables.TableRoutingRule{
		ID: "complex-to-mini", Name: "complex-to-mini", CelExpression: `complexity_tier == "COMPLEX"`, Enabled: schemas.Ptr(true), Scope: "global",
		Targets: []configstoreTables.TableRoutingTarget{{Model: schemas.Ptr("gpt-4o-mini"), Weight: 1}},
	}

	t.Run("no router on", func(t *testing.T) {
		decision := modelRoutingDecision()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, []configstoreTables.TableRoutingRule{complexToMini}, nil)
		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		_, model, _ := req.GetRequestFields()
		assert.Equal(t, "gpt-4o-mini", model, "the complexity config's tier drives the routing rule")
		assert.Equal(t, 1, decision.calls())
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID), "complexity_tier records no router")
	})

	t.Run("a router takes the request after", func(t *testing.T) {
		decision := modelRoutingDecision()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision,
			[]configstoreTables.TableRoutingRule{complexToMini}, []configstore.ModelRouter{supportRouter("Support")})
		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		_, model, _ := req.GetRequestFields()
		assert.Equal(t, "claude-opus", model, "the router sees the routing rule's model and replaces it")
		assert.Equal(t, 2, decision.calls(), "each classifies once")
		assert.Equal(t, "support", ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})
}

// TestModelRoutingSkipsLargePayload pins that a large payload, whose body is
// not parsed, has no text for a router and skips the step.
func TestModelRoutingSkipsLargePayload(t *testing.T) {
	decision := modelRoutingDecision()
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, nil)
	serveGeneralRouter(t, plugin, tierTarget(complexity.TierSimple, "anthropic", "claude-haiku"))
	ctx := newRequestContext()
	metadata := &schemas.LargePayloadMetadata{Model: "gpt-4o"}
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, metadata)
	require.NoError(t, plugin.PreRequestHook(ctx, &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{}}))
	assert.Equal(t, "gpt-4o", metadata.Model)
	assert.Zero(t, decision.calls())
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
}

// TestModelRouterReloadAndRemove pins the plugin's router lifecycle: a reload
// renames a served router, and a removed router stops being served and takes
// no more requests.
func TestModelRouterReloadAndRemove(t *testing.T) {
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{supportRouter("Support")})

	renamed := supportRouter("Customer support")
	require.NoError(t, plugin.ReloadModelRouter(&renamed))
	ctx := newRequestContext()
	runPreRequest(t, plugin, ctx, complexText)
	assert.Equal(t, "Customer support", ctx.Value(schemas.BifrostContextKeyModelRouterName))

	badCondition := supportRouter("Customer support")
	badCondition.Routing = globalRouting(0, `model ==`)
	require.Error(t, plugin.ReloadModelRouter(&badCondition), "a condition that does not compile is refused")

	_, ok := plugin.ModelRouterSemanticStatus("support")
	assert.True(t, ok)
	require.NoError(t, plugin.RemoveModelRouter("support"))
	_, ok = plugin.ModelRouterSemanticStatus("support")
	assert.False(t, ok)
	ctx = newRequestContext()
	req := runPreRequest(t, plugin, ctx, complexText)
	_, model, _ := req.GetRequestFields()
	assert.Equal(t, "gpt-4o", model, "a removed router takes no requests")
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))

	require.NoError(t, plugin.RemoveModelRouter("missing"), "removing a router not served is a no-op")
}

// ---------------------------------------------------------------------------
// Routing rules, then model routers: fixtures for the combined scenarios
// ---------------------------------------------------------------------------

// ruleTarget is a routing rule target.
func ruleTarget(provider, model string) configstoreTables.TableRoutingTarget {
	target := configstoreTables.TableRoutingTarget{Weight: 1}
	if provider != "" {
		target.Provider = schemas.Ptr(provider)
	}
	if model != "" {
		target.Model = schemas.Ptr(model)
	}
	return target
}

// ruleFor is an enabled global routing rule that sends the requests cel holds
// for to target.
func ruleFor(id, cel string, target configstoreTables.TableRoutingTarget) configstoreTables.TableRoutingRule {
	return configstoreTables.TableRoutingRule{
		ID: id, Name: id, CelExpression: cel, Enabled: schemas.Ptr(true), Scope: "global",
		Targets: []configstoreTables.TableRoutingTarget{target},
	}
}

// grantedAccess is the access of a request a virtual key granted the given
// providers, which is what gives a request a governance scope.
func grantedAccess(permits ...schemas.ProviderPermit) schemas.Access {
	return grant.NewAccess([]schemas.Permit{grant.NewPermit(grant.PermitVirtualKey, "vk-1", "vk", true, false, permits, nil)}, nil, grant.Union, nil)
}

// scopedRequestContext returns a request context stamped with the governance
// ids the routing hook reads the request's scope from. An empty id is left
// unset.
func scopedRequestContext(virtualKeyID, userID, teamID, customerID string) *schemas.BifrostContext {
	ctx := newRequestContext()
	for key, value := range map[schemas.BifrostContextKey]string{
		schemas.BifrostContextKeyGovernanceVirtualKeyID: virtualKeyID,
		schemas.BifrostContextKeyUserID:                 userID,
		schemas.BifrostContextKeyGovernanceTeamID:       teamID,
		schemas.BifrostContextKeyGovernanceCustomerID:   customerID,
	} {
		if value != "" {
			ctx.SetValue(key, value)
		}
	}
	return ctx
}

// everyTierTargets gives each tier of a router a model named after the router
// and the tier, on the request's provider, so a test can tell both which
// router routed a request and on which tier.
func everyTierTargets(router string) []configstoreTables.TableModelRouterTarget {
	return []configstoreTables.TableModelRouterTarget{
		tierTarget(complexity.TierSimple, "", router+"-simple"),
		tierTarget(complexity.TierMedium, "", router+"-medium"),
		tierTarget(complexity.TierComplex, "", router+"-complex"),
	}
}

// routedModel returns the model the request ended on.
func routedModel(req *schemas.BifrostRequest) string {
	_, model, _ := req.GetRequestFields()
	return model
}

// sessionRouterConfig is a decision-model router config with session routing
// on, so a session never drops below the highest tier the router gave it.
func sessionRouterConfig() complexity.AnalyzerConfig {
	config := decisionAnalyzerTestConfig()
	config.Session = &configstore.ComplexitySessionConfig{Enabled: true}
	return *config
}

// newTestKVStore returns the in-memory KV store session tiers and tool
// continuations are kept in.
func newTestKVStore(t *testing.T) *kvstore.Store {
	t.Helper()
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

// flakyKVStore is a KV store whose writes can be made to fail, standing in
// for a state store outage in the middle of a session.
type flakyKVStore struct {
	schemas.KVStore
	failWrites atomic.Bool
}

// SetWithTTL fails while the outage is on.
func (s *flakyKVStore) SetWithTTL(key string, value any, ttl time.Duration) error {
	if s.failWrites.Load() {
		return errors.New("state store unavailable")
	}
	return s.KVStore.SetWithTTL(key, value, ttl)
}

// ---------------------------------------------------------------------------
// Regressions: who a model router's decision is logged against
// ---------------------------------------------------------------------------

// TestModelRouterSessionStoreOutageLogsAsModelRouter pins which engine a
// router's classification is logged under when the session state store fails
// in the middle of a session. The router still routes the request on what
// its classifier just answered, and that decision is the router's: a request
// no routing rule matched must not show a routing-rule decision in its trail
// (see TestModelRouterClassificationLogEngine for the same rule without an
// outage).
func TestModelRouterSessionStoreOutageLogsAsModelRouter(t *testing.T) {
	store := &flakyKVStore{KVStore: newTestKVStore(t)}
	router := configstore.ModelRouter{ID: "support", Name: "Support", ComplexityAnalyzerConfig: sessionRouterConfig(), Targets: everyTierTargets("support")}
	router.Routing = globalRouting(0, "")
	plugin := modelRoutingTestPluginWithConfig(t, &routing.Config{KVStore: store}, routing.NewMockGovernance(), modelRoutingDecision().execute,
		nil, []configstore.ModelRouter{router})

	req := runPreRequest(t, plugin, complexitySessionContext("s1"), simpleText)
	require.Equal(t, "support-simple", routedModel(req))

	store.failWrites.Store(true)
	ctx := complexitySessionContext("s1")
	req = runPreRequest(t, plugin, ctx, complexText)
	assert.Equal(t, "support-complex", routedModel(req), "the classifier's answer still routes the request")
	assert.Equal(t, complexity.TierComplex, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))

	logs := ctx.GetRoutingEngineLogs()
	require.NotEmpty(t, logs)
	for _, entry := range logs {
		assert.Equal(t, schemas.RoutingEngineModelRouter, entry.Engine, "log %q", entry.Message)
	}
}

// TestRefusedFallbacksNameWhoWroteThem pins who a "fallback will be refused by
// governance" warning names, so an operator looks in the right place. When a
// router's target replaced the chain, a fallback the virtual key refuses is
// the router's and is reported against it, under the model router engine,
// whether or not a routing rule matched first. When the router's target has
// no fallbacks, the chain is still the routing rule's and the warning still
// names the rule.
func TestRefusedFallbacksNameWhoWroteThem(t *testing.T) {
	access := grantedAccess(
		schemas.ProviderPermit{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o", "gpt-4o-mini"}, KeyIDs: schemas.WhiteList{"*"}},
		schemas.ProviderPermit{Provider: "anthropic", AllowedModels: schemas.WhiteList{"claude-opus"}, KeyIDs: schemas.WhiteList{"*"}},
	)
	ruleWithFallbacks := func(t *testing.T, fallbacks string) configstoreTables.TableRoutingRule {
		t.Helper()
		rule := ruleFor("to-mini", `model == "gpt-4o"`, ruleTarget("openai", "gpt-4o-mini"))
		rule.Fallbacks = schemas.Ptr(fallbacks)
		require.NoError(t, rule.AfterFind(nil))
		return rule
	}
	withRefusedFallback := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	withRefusedFallback.ParsedFallbacks = []configstoreTables.RoutingFallback{{Fallback: schemas.Fallback{Provider: schemas.Groq, Model: "llama-3.1-8b-instant"}}}
	const refusedByRouter = "Model router 'Support': fallback groq/llama-3.1-8b-instant will be refused by governance: provider groq is not allowed for this request"

	run := func(t *testing.T, routingRules []configstoreTables.TableRoutingRule, target configstoreTables.TableModelRouterTarget, condition string) (*schemas.BifrostContext, []schemas.Fallback) {
		t.Helper()
		governance := routing.NewMockGovernance()
		governance.Access = access
		router := decisionModelRouter("support", "Support", target)
		router.Routing = globalRouting(0, condition)
		plugin := modelRoutingTestPlugin(t, governance, modelRoutingDecision(), routingRules, []configstore.ModelRouter{router})
		ctx := scopedRequestContext("vk-1", "", "", "")
		req := runPreRequest(t, plugin, ctx, complexText)
		_, _, fallbacks := req.GetRequestFields()
		return ctx, fallbacks
	}
	engineOf := func(ctx *schemas.BifrostContext, message string) string {
		for _, entry := range ctx.GetRoutingEngineLogs() {
			if entry.Message == message {
				return entry.Engine
			}
		}
		return ""
	}

	t.Run("a router's chain that replaced the rule's is reported against the router", func(t *testing.T) {
		rule := ruleWithFallbacks(t, `["openai/gpt-4o"]`)
		ctx, fallbacks := run(t, []configstoreTables.TableRoutingRule{rule}, withRefusedFallback, `model == "gpt-4o-mini"`)
		require.Equal(t, []schemas.Fallback{{Provider: schemas.Groq, Model: "llama-3.1-8b-instant"}}, fallbacks, "the router's chain replaced the rule's")
		assert.NotContains(t, routingLogs(ctx), "Rule 'to-mini': fallback groq/llama-3.1-8b-instant",
			"the rule never wrote the groq fallback; the model router 'Support' did")
		assert.Equal(t, schemas.RoutingEngineModelRouter, engineOf(ctx, refusedByRouter), "logs:\n%s", routingLogs(ctx))
	})

	t.Run("a router's chain is reported with no routing rule at all", func(t *testing.T) {
		ctx, _ := run(t, nil, withRefusedFallback, "")
		assert.Equal(t, schemas.RoutingEngineModelRouter, engineOf(ctx, refusedByRouter), "logs:\n%s", routingLogs(ctx))
	})

	t.Run("a rule's chain the router kept is still reported against the rule", func(t *testing.T) {
		rule := ruleWithFallbacks(t, `["groq/llama-3.1-8b-instant"]`)
		ctx, fallbacks := run(t, []configstoreTables.TableRoutingRule{rule}, tierTarget(complexity.TierComplex, "anthropic", "claude-opus"), `model == "gpt-4o-mini"`)
		require.Equal(t, []schemas.Fallback{{Provider: schemas.Groq, Model: "llama-3.1-8b-instant"}}, fallbacks, "a target without fallbacks keeps the rule's chain")
		assert.Equal(t, schemas.RoutingEngineRoutingRule,
			engineOf(ctx, "Rule 'to-mini': fallback groq/llama-3.1-8b-instant will be refused by governance: provider groq is not allowed for this request"), "logs:\n%s", routingLogs(ctx))
		assert.NotContains(t, routingLogs(ctx), "Model router 'Support': fallback")
	})
}
