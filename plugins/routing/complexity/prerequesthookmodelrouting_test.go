package complexity_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	governancePlugin "github.com/maximhq/bifrost/plugins/governance"
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

// conditionalRouter is a router that classifies nothing: it sends every
// request its routing takes to the given targets, which name no tier, and
// takes no requests until it is given routing.
func conditionalRouter(id, name string, targets ...configstoreTables.TableModelRouterTarget) configstore.ModelRouter {
	router := decisionModelRouter(id, name, targets...)
	router.Mode = configstoreTables.ModelRouterModeConditional
	return router
}

// chained makes a routing chain: the routers are walked again on what its
// router chooses.
func chained(routing *configstore.ModelRouterRouting) *configstore.ModelRouterRouting {
	routing.Chain = true
	return routing
}

// passingOn makes a routing pass a request its classifier router cannot place
// to the next router, instead of keeping it.
func passingOn(routing *configstore.ModelRouterRouting) *configstore.ModelRouterRouting {
	routing.EmptyTarget = configstoreTables.ModelRouterEmptyTargetNextRouter
	return routing
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
	require.NoError(t, reloadRouter(plugin, &router))
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
// config, for the session store session tiers and tool continuations need, any
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
	ruleStore, err := rules.NewTestRules(ctx, t.TempDir(), logger)
	require.NoError(t, err)
	t.Cleanup(ruleStore.Close)
	for i := range routingRules {
		require.NoError(t, ruleStore.Put(ctx, &routingRules[i]))
	}
	plugin, err := routing.InitFromStore(ctx, config, logger, nil, ruleStore.Store, governance)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	testRuleStores.Store(plugin, ruleStore)
	t.Cleanup(func() { testRuleStores.Delete(plugin) })
	plugin.SetDecisionRequestExecutor(decide)
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(decisionAnalyzerTestConfig()))
	for i := range routers {
		require.NoError(t, reloadRouter(plugin, &routers[i]))
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

	require.NoError(t, testRulesOf(plugin).SaveModelRouterTargets(context.Background(), "catch-all", []configstoreTables.TableModelRouterTarget{
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

// TestModelRoutingOnLargePayloads pins the step on a large payload, whose body
// is not parsed. A conditional router routes it on its condition, as a routing
// rule does. A classifier router has no text to classify, so it routes only on
// a tier the session already remembers, and otherwise passes the request on
// whatever its routing says: keeping it would end the walk on a request the
// router never saw.
func TestModelRoutingOnLargePayloads(t *testing.T) {
	// routeLargePayload runs the hook on a large payload for gpt-4o and returns
	// the model it ends on.
	routeLargePayload := func(t *testing.T, plugin *routing.RoutingPlugin, ctx *schemas.BifrostContext) string {
		t.Helper()
		metadata := &schemas.LargePayloadMetadata{Model: "gpt-4o"}
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, metadata)
		require.NoError(t, plugin.PreRequestHook(ctx, &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest, ChatRequest: &schemas.BifrostChatRequest{}}))
		return metadata.Model
	}

	t.Run("a classifier router with nothing remembered leaves it alone", func(t *testing.T) {
		decision := modelRoutingDecision()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, nil)
		serveGeneralRouter(t, plugin, tierTarget(complexity.TierSimple, "anthropic", "claude-haiku"))
		ctx := newRequestContext()
		assert.Equal(t, "gpt-4o", routeLargePayload(t, plugin, ctx))
		assert.Zero(t, decision.calls())
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.NotContains(t, enginesUsed(ctx), schemas.RoutingEngineModelRouter)
	})

	t.Run("and passes it to the next router, though it keeps what it cannot place", func(t *testing.T) {
		decision := modelRoutingDecision()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, nil)
		serveGeneralRouter(t, plugin, tierTarget(complexity.TierSimple, "anthropic", "claude-haiku"))
		after := conditionalRouter("after", "After", tierTarget("", "", "gpt-4o-mini"))
		after.Routing = globalRouting(1, "")
		require.NoError(t, reloadRouter(plugin, &after))
		ctx := newRequestContext()
		assert.Equal(t, "gpt-4o-mini", routeLargePayload(t, plugin, ctx))
		assert.Zero(t, decision.calls())
		assert.Equal(t, "after", ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})

	t.Run("a conditional router routes it on its condition", func(t *testing.T) {
		mini := conditionalRouter("mini", "Mini", tierTarget("", "", "gpt-4o-mini"))
		mini.Routing = globalRouting(0, `model == "gpt-4o"`)
		other := conditionalRouter("other", "Other", tierTarget("", "", "never"))
		other.Routing = globalRouting(1, `model == "claude-opus"`)
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{mini, other})
		ctx := newRequestContext()
		assert.Equal(t, "gpt-4o-mini", routeLargePayload(t, plugin, ctx))
		assert.Equal(t, "mini", ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})

	t.Run("a classifier router routes it on the tier the session remembers", func(t *testing.T) {
		decision := modelRoutingDecision()
		a := decisionModelRouter("a", "A", everyTierTargets("a")...)
		a.Routing = globalRouting(0, "")
		plugin := modelRoutingTestPluginWithConfig(t, newTestKVStore(t), routing.NewMockGovernance(), decision.execute,
			nil, []configstore.ModelRouter{a})

		turn := complexitySessionContext("s1")
		req := runPreRequest(t, plugin, turn, mediumText)
		require.Equal(t, "a-medium", routedModel(req))
		_, _, err := plugin.PostLLMHook(turn, servedChatResponse(), nil)
		require.NoError(t, err)

		large := complexitySessionContext("s1")
		assert.Equal(t, "a-medium", routeLargePayload(t, plugin, large), "the session's turn stays on the model it started on")
		assert.Equal(t, "a", large.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Equal(t, 1, decision.calls(), "nothing is classified on a large payload")

		elsewhere := complexitySessionContext("s2")
		assert.Equal(t, "gpt-4o", routeLargePayload(t, plugin, elsewhere), "another session remembers nothing")
		assert.Equal(t, 1, decision.calls())
	})
}

// TestModelRouterReloadAndRemove pins the plugin's router lifecycle: a reload
// renames a served router, and a removed router stops being served and takes
// no more requests.
func TestModelRouterReloadAndRemove(t *testing.T) {
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{supportRouter("Support")})

	renamed := supportRouter("Customer support")
	require.NoError(t, reloadRouter(plugin, &renamed))
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

// scopedRouting turns a router on for the requests of one scope entity that
// cel holds for.
func scopedRouting(scope, scopeID string, priority int, cel string) *configstore.ModelRouterRouting {
	return &configstore.ModelRouterRouting{Enabled: true, Scope: scope, ScopeID: schemas.Ptr(scopeID), Priority: priority, CelExpression: cel}
}

// appRouting turns a router on, globally, for the requests of one app: those
// that send the x-app header with its name.
func appRouting(priority int, app string) *configstore.ModelRouterRouting {
	return globalRouting(priority, `headers["x-app"] == "`+app+`"`)
}

// appContext returns the context of a request an app sent.
func appContext(app string) *schemas.BifrostContext {
	ctx := newRequestContext()
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-app": app})
	return ctx
}

// appSessionContext returns the context of a request an app sent within a
// session.
func appSessionContext(sessionID, app string) *schemas.BifrostContext {
	ctx := complexitySessionContext(sessionID)
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-app": app})
	return ctx
}

// grantedAccess is the access of a request a virtual key granted the given
// providers, which is what gives a request a governance scope.
func grantedAccess(permits ...schemas.ProviderPermit) schemas.Access {
	return grant.NewAccess([]schemas.Permit{grant.NewPermit(grant.PermitVirtualKey, "vk-1", "vk", true, false, permits, nil)}, "", nil, grant.Union, nil)
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

// streamRequest is a streaming chat request for text.
func streamRequest(text string) *schemas.BifrostRequest {
	req := llmComplexityChatRequest(text)
	req.RequestType = schemas.ChatCompletionStreamRequest
	return req
}

// toolContinuationRequest is the follow-up a client sends after running a
// tool: the turn's user text, the assistant's tool call and the tool's result.
func toolContinuationRequest(text string) *schemas.BifrostRequest {
	req := llmComplexityChatRequest(text)
	req.ChatRequest.Input = append(req.ChatRequest.Input,
		schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: chatString("Calling the tool")},
		schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: chatString("Tool result received")},
	)
	return req
}

// servedChatResponse is the response of a chat request that was served, which
// is what makes the post hook save the turn's tiers.
func servedChatResponse() *schemas.BifrostResponse {
	return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ChatCompletionRequest},
	}}
}

// routedModel returns the model the request ended on.
func routedModel(req *schemas.BifrostRequest) string {
	_, model, _ := req.GetRequestFields()
	return model
}

// enginesUsed returns the routing engines the request recorded, in order.
func enginesUsed(ctx *schemas.BifrostContext) []string {
	engines, _ := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
	return engines
}

// llmRouterConfig is a router config whose classifier is the llm classifier.
func llmRouterConfig() complexity.AnalyzerConfig {
	config := llmFallbackAnalyzerTestConfig()
	config.Classifier = complexity.ClassifierLLM
	config.Semantic = nil
	return *config
}

// sessionRouterConfig is a decision-model router config with session routing
// on, so a session never drops below the highest tier the router gave it.
func sessionRouterConfig() complexity.AnalyzerConfig {
	config := decisionAnalyzerTestConfig()
	config.Session = &configstore.ComplexitySessionConfig{Enabled: true}
	return *config
}

// sessionConfigForTest returns a routing config over new session and warm tables, as
// the host wires them, and the session table session tiers and tool continuations are kept in.
func sessionConfigForTest(t *testing.T) (*routing.Config, *livestore.Table[string, string]) {
	t.Helper()
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	sessions, err := live.NewTable[string, string](context.Background(), "session_state", livestore.WithWriteTimes())
	require.NoError(t, err)
	warm, err := live.NewTable[string, string](context.Background(), routing.ComplexityWarmTable, livestore.WithWriteTimes())
	require.NoError(t, err)
	return &routing.Config{Sessions: sessions, Warm: warm}, sessions
}

// newTestKVStore returns a routing config over new session and warm tables.
func newTestKVStore(t *testing.T) *routing.Config {
	t.Helper()
	config, _ := sessionConfigForTest(t)
	return config
}

// quietGovernance is a governance stand-in that records nothing, so requests
// can run through the hook from many goroutines and in benchmarks.
type quietGovernance struct {
	*rules.MockGovernanceStore
}

func (quietGovernance) ResolveAccess(*schemas.BifrostContext) (schemas.Access, error) {
	return nil, nil
}
func (quietGovernance) PublishRoutingAllowlist(*schemas.BifrostContext, string) {}
func (quietGovernance) LoadBalanceProvider(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

// newQuietGovernance returns a governance stand-in safe for concurrent use.
func newQuietGovernance() quietGovernance {
	return quietGovernance{MockGovernanceStore: rules.NewMockGovernanceStore()}
}

// corruptSessions overwrites every entry the session table holds with a tier no version writes,
// standing in for session state the router cannot use in the middle of a session.
func corruptSessions(sessions *livestore.Table[string, string]) {
	var keys []string
	sessions.Range(func(key, _ string) bool {
		keys = append(keys, key)
		return true
	})
	for _, key := range keys {
		sessions.PutWithTTL(key, "UNREADABLE", time.Hour)
	}
}

// ---------------------------------------------------------------------------
// A gateway with no router
// ---------------------------------------------------------------------------

// TestGatewayWithNoRouterChangesNothing pins that the routing step leaves a
// request alone when no router is enabled: nothing is classified, recorded or
// logged, and the virtual key's own routing still runs.
func TestGatewayWithNoRouterChangesNothing(t *testing.T) {
	governance := routing.NewMockGovernance()
	decision := modelRoutingDecision()
	plugin := modelRoutingTestPlugin(t, governance, decision, nil, nil)

	ctx := newRequestContext()
	req := runPreRequest(t, plugin, ctx, complexText)

	provider, model, fallbacks := req.GetRequestFields()
	assert.Equal(t, schemas.OpenAI, provider)
	assert.Equal(t, "gpt-4o", model)
	assert.Empty(t, fallbacks)
	assert.Zero(t, decision.calls(), "nothing classifies a request nothing routes")
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	assert.Empty(t, enginesUsed(ctx))
	assert.Empty(t, ctx.GetRoutingEngineLogs())
	assert.Equal(t, []string{"gpt-4o"}, governance.LoadBalancedModels, "the virtual key's own routing still runs")
}

// TestModelRoutersWalkScopesNarrowestFirst pins, through the whole hook, which
// router takes a request when routers exist at every scope: the virtual
// key's, then the user's, the team's, the customer's, and the global one
// last. Another entity's router never takes the request, a narrow router
// whose condition fails passes it on, and a request nothing granted is
// unscoped, so only a global router can take it.
func TestModelRoutersWalkScopesNarrowestFirst(t *testing.T) {
	scopeRouter := func(id string, routing *configstore.ModelRouterRouting) configstore.ModelRouter {
		router := decisionModelRouter(id, id, tierTarget(complexity.TierComplex, "", id+"-model"))
		router.Routing = routing
		return router
	}
	everyScope := []configstore.ModelRouter{
		scopeRouter("global", globalRouting(0, "")),
		scopeRouter("customer", scopedRouting("customer", "customer-1", 0, "")),
		scopeRouter("team", scopedRouting("team", "team-1", 0, "")),
		scopeRouter("user", scopedRouting("user", "user-1", 0, "")),
		scopeRouter("vk", scopedRouting("virtual_key", "vk-1", 0, "")),
		scopeRouter("other-vk", scopedRouting("virtual_key", "vk-2", 0, "")),
	}
	granted := grantedAccess(schemas.ProviderPermit{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"*"}})

	cases := []struct {
		name                             string
		virtualKey, user, team, customer string
		wantRouter                       string
	}{
		{name: "the virtual key's router first", virtualKey: "vk-1", user: "user-1", team: "team-1", customer: "customer-1", wantRouter: "vk"},
		{name: "then the user's", user: "user-1", team: "team-1", customer: "customer-1", wantRouter: "user"},
		{name: "then the team's", team: "team-1", customer: "customer-1", wantRouter: "team"},
		{name: "then the customer's", customer: "customer-1", wantRouter: "customer"},
		{name: "the global router last", wantRouter: "global"},
		{name: "another entity's routers never take the request", virtualKey: "vk-9", user: "user-9", team: "team-9", customer: "customer-9", wantRouter: "global"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			governance := routing.NewMockGovernance()
			governance.Access = granted
			plugin := modelRoutingTestPlugin(t, governance, modelRoutingDecision(), nil, everyScope)
			ctx := scopedRequestContext(tc.virtualKey, tc.user, tc.team, tc.customer)
			req := runPreRequest(t, plugin, ctx, complexText)
			assert.Equal(t, tc.wantRouter, ctx.Value(schemas.BifrostContextKeyModelRouterID))
			assert.Equal(t, tc.wantRouter+"-model", routedModel(req))
		})
	}

	t.Run("a narrow router whose condition fails passes the request to the next scope", func(t *testing.T) {
		governance := routing.NewMockGovernance()
		governance.Access = granted
		routers := []configstore.ModelRouter{
			scopeRouter("vk", scopedRouting("virtual_key", "vk-1", 0, `model == "some-other-model"`)),
			scopeRouter("team", scopedRouting("team", "team-1", 0, "")),
			scopeRouter("global", globalRouting(0, "")),
		}
		plugin := modelRoutingTestPlugin(t, governance, modelRoutingDecision(), nil, routers)
		ctx := scopedRequestContext("vk-1", "", "team-1", "")
		runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "team", ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})

	t.Run("a request nothing granted is unscoped", func(t *testing.T) {
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, everyScope)
		ctx := scopedRequestContext("vk-1", "user-1", "team-1", "customer-1")
		runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "global", ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})
}

// ---------------------------------------------------------------------------
// Several model routers
// ---------------------------------------------------------------------------

// TestOnlyTheFirstMatchingRouterIsTried pins that a classifier router keeps
// the requests it takes unless its routing says otherwise. Once its routing
// matches, the request is its own: if it has no model for the tier, or cannot
// classify the request, the request keeps its model and the next router is
// not asked. A router whose condition fails, or fails to evaluate, passes the
// request on. TestClassifierRouterPassesARequestOnWhenToldTo covers the
// routing that passes on a request the router cannot place.
func TestOnlyTheFirstMatchingRouterIsTried(t *testing.T) {
	second := decisionModelRouter("second", "Second", everyTierTargets("second")...)
	second.Routing = globalRouting(1, "")

	t.Run("a first router without a model for the tier", func(t *testing.T) {
		decision := modelRoutingDecision()
		first := decisionModelRouter("first", "First", tierTarget(complexity.TierSimple, "", "first-simple"))
		first.Routing = globalRouting(0, "")
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{first, second})
		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "gpt-4o", routedModel(req))
		assert.Equal(t, "first", ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Equal(t, 1, decision.calls(), "the second router never classifies")
	})

	t.Run("a first router that cannot classify", func(t *testing.T) {
		decision := modelRoutingDecision()
		decision.err = &schemas.BifrostError{Error: &schemas.ErrorField{Message: "decision model unavailable"}}
		first := decisionModelRouter("first", "First", everyTierTargets("first")...)
		first.Routing = globalRouting(0, "")
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{first, second})
		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "gpt-4o", routedModel(req))
		assert.Equal(t, "first", ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Equal(t, 1, decision.calls())
		assert.Contains(t, routingLogs(ctx), "Model router 'First' produced no tier")
	})

	t.Run("a first router whose condition fails to evaluate is skipped", func(t *testing.T) {
		decision := modelRoutingDecision()
		first := decisionModelRouter("first", "First", everyTierTargets("first")...)
		first.Routing = globalRouting(0, `int(headers["x-n"]) > 3`)
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{first, second})
		ctx := newRequestContext()
		ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-n": "not-a-number"})
		req := runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "second-complex", routedModel(req))
		assert.Equal(t, "second", ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Contains(t, routingLogs(ctx), "Model router 'First' skipped: its condition failed to evaluate")

		// The same router takes the request once its condition can be evaluated.
		ctx = newRequestContext()
		ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-n": "7"})
		req = runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "first-complex", routedModel(req))
	})
}

// TestClassifierRouterPassesARequestOnWhenToldTo pins the routing setting for
// a request a classifier router cannot place. With it set, a router that has
// no model for the tier, or produces no tier, passes the request to the next
// router and is not recorded on it; the tiers it does have a model for stay
// its own. A request no later router takes keeps its model.
func TestClassifierRouterPassesARequestOnWhenToldTo(t *testing.T) {
	second := decisionModelRouter("second", "Second", everyTierTargets("second")...)
	second.Routing = globalRouting(1, "")

	t.Run("no model for the tier", func(t *testing.T) {
		decision := modelRoutingDecision()
		first := decisionModelRouter("first", "First", tierTarget(complexity.TierSimple, "", "first-simple"))
		first.Routing = passingOn(globalRouting(0, ""))
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{first, second})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "second-complex", routedModel(req))
		assert.Equal(t, "second", ctx.Value(schemas.BifrostContextKeyModelRouterID), "a router that passes a request on is not recorded on it")
		assert.Contains(t, routingLogs(ctx), "Model router 'First' has no model for tier=COMPLEX; trying the next router")

		ctx = newRequestContext()
		req = runPreRequest(t, plugin, ctx, simpleText)
		assert.Equal(t, "first-simple", routedModel(req), "a tier the router has a model for is its own")
		assert.Equal(t, "first", ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})

	t.Run("no tier", func(t *testing.T) {
		decision := modelRoutingDecision()
		decision.err = &schemas.BifrostError{Error: &schemas.ErrorField{Message: "decision model unavailable"}}
		first := decisionModelRouter("first", "First", everyTierTargets("first")...)
		first.Routing = passingOn(globalRouting(0, ""))
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{first, second})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "gpt-4o", routedModel(req))
		assert.Equal(t, "second", ctx.Value(schemas.BifrostContextKeyModelRouterID), "the second router keeps what it cannot place, and is the one recorded")
		logs := routingLogs(ctx)
		assert.Contains(t, logs, "Model router 'First' produced no tier; trying the next router")
		assert.Contains(t, logs, "Model router 'Second' produced no tier; keeping provider=openai, model=gpt-4o")
	})

	t.Run("no later router takes it", func(t *testing.T) {
		decision := modelRoutingDecision()
		only := decisionModelRouter("only", "Only", tierTarget(complexity.TierSimple, "", "only-simple"))
		only.Routing = passingOn(globalRouting(0, ""))
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{only})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "gpt-4o", routedModel(req))
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.NotContains(t, enginesUsed(ctx), schemas.RoutingEngineModelRouter)
		assert.Equal(t, complexity.TierComplex, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier), "the tier it classified is still recorded")
	})
}

// TestRoutersWithTheSameClassifierSettingsClassifyARequestOnce pins what a
// request costs when it passes through several classifier routers. Routers
// whose classifier settings are the same share one classification, as every
// routing rule shared the Complexity Router's one answer, and a classification
// that failed is not tried again by the next of them. Routers that classify
// differently, and a router with a suggestions block, classify for themselves.
// Session memory is not shared: each router applies its own to the answer.
func TestRoutersWithTheSameClassifierSettingsClassifyARequestOnce(t *testing.T) {
	// A router with a model for one tier only, which passes every other
	// request on: what a routing rule on one complexity_tier becomes.
	tierRouter := func(id, tier string, priority int) configstore.ModelRouter {
		router := decisionModelRouter(id, id, tierTarget(tier, "", id+"-model"))
		router.Routing = passingOn(globalRouting(priority, ""))
		return router
	}
	const classified = "Decision model complexity: tier="

	t.Run("one classification for every router the request passes through", func(t *testing.T) {
		decision := modelRoutingDecision()
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{
			tierRouter("simple", complexity.TierSimple, 0), tierRouter("medium", complexity.TierMedium, 1), tierRouter("complex", complexity.TierComplex, 2),
		})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "complex-model", routedModel(req))
		assert.Equal(t, "complex", ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Equal(t, 1, decision.calls(), "the three routers classify alike, so the request is classified once")
		logs := routingLogs(ctx)
		assert.Equal(t, 1, strings.Count(logs, classified), "the log shows one classification")
		assert.Contains(t, logs, "Model router 'simple' has no model for tier=COMPLEX; trying the next router")
		assert.Contains(t, logs, "Model router 'medium' has no model for tier=COMPLEX; trying the next router", "then what each router did with it")

		ctx = newRequestContext()
		req = runPreRequest(t, plugin, ctx, mediumText)
		assert.Equal(t, "medium-model", routedModel(req))
		assert.Equal(t, 2, decision.calls(), "an answer is reused within one request, never across requests")
		assert.Equal(t, 1, strings.Count(routingLogs(ctx), classified))
	})

	t.Run("a classification that failed is not tried again", func(t *testing.T) {
		decision := modelRoutingDecision()
		decision.err = &schemas.BifrostError{Error: &schemas.ErrorField{Message: "decision model unavailable"}}
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{
			tierRouter("simple", complexity.TierSimple, 0), tierRouter("medium", complexity.TierMedium, 1), tierRouter("complex", complexity.TierComplex, 2),
		})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "gpt-4o", routedModel(req))
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Equal(t, 1, decision.calls(), "the next routers would only fail the same way")
		assert.Contains(t, routingLogs(ctx), "Model router 'complex' produced no tier; trying the next router")
	})

	t.Run("routers that classify differently each classify", func(t *testing.T) {
		decision := modelRoutingDecision()
		other := tierRouter("medium", complexity.TierMedium, 1)
		other.Keywords.MediumKeywords = []string{"walnut indigo"}
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{
			tierRouter("simple", complexity.TierSimple, 0), other,
		})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, mediumText)
		assert.Equal(t, "medium-model", routedModel(req))
		assert.Equal(t, 2, decision.calls(), "one phrase apart is a different classifier")
	})

	t.Run("a router with a suggestions block classifies for itself", func(t *testing.T) {
		decision := modelRoutingDecision()
		suggesting := tierRouter("medium", complexity.TierMedium, 1)
		suggesting.Suggestions = &configstore.ComplexitySuggestionsConfig{}
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, []configstore.ModelRouter{
			tierRouter("simple", complexity.TierSimple, 0), suggesting,
		})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, mediumText)
		assert.Equal(t, "medium-model", routedModel(req))
		assert.Equal(t, 2, decision.calls(), "what it classifies is kept under its own name, so it shares with no one")
	})

	t.Run("each router applies its own session memory to the answer", func(t *testing.T) {
		decision := modelRoutingDecision()
		remembering := configstore.ModelRouter{
			ID: "remembering", Name: "Remembering", ComplexityAnalyzerConfig: sessionRouterConfig(),
			Targets: []configstoreTables.TableModelRouterTarget{tierTarget(complexity.TierSimple, "", "remembering-simple")},
		}
		remembering.Routing = passingOn(globalRouting(0, ""))
		plain := decisionModelRouter("plain", "Plain", everyTierTargets("plain")...)
		plain.Routing = globalRouting(1, "")
		plugin := modelRoutingTestPluginWithConfig(t, newTestKVStore(t), routing.NewMockGovernance(), decision.execute, nil,
			[]configstore.ModelRouter{remembering, plain})
		const heldAtMedium = "Model router 'Remembering' has no model for tier=MEDIUM; trying the next router"

		// The session starts at MEDIUM for the router that remembers.
		ctx := complexitySessionContext("s1")
		req := runPreRequest(t, plugin, ctx, mediumText)
		assert.Equal(t, "plain-medium", routedModel(req))
		assert.Contains(t, routingLogs(ctx), heldAtMedium)
		assert.Equal(t, 1, decision.calls(), "the session setting is not part of how a router classifies")

		// A simpler turn is classified once, as SIMPLE. The router that
		// remembers holds the session at MEDIUM, so its SIMPLE model is not
		// used; the router that does not takes the answer as it is.
		ctx = complexitySessionContext("s1")
		req = runPreRequest(t, plugin, ctx, simpleText)
		assert.Equal(t, "plain-simple", routedModel(req))
		assert.Contains(t, routingLogs(ctx), heldAtMedium)
		assert.Contains(t, routingLogs(ctx), "Session complexity held: effective=MEDIUM proposed=SIMPLE")
		assert.Equal(t, 2, decision.calls())
	})
}

// TestConditionalRouterRoutesWithoutClassifying pins a conditional router: it
// sends the requests its routing takes to its target, classifying nothing,
// and is recorded on the request as a classifier router is. With no target it
// passes the request to the next router, as a routing rule without one does.
// A target that names neither a provider nor a model keeps the request's own
// and still applies its fallbacks.
func TestConditionalRouterRoutesWithoutClassifying(t *testing.T) {
	t.Run("it routes on its condition alone", func(t *testing.T) {
		governance := routing.NewMockGovernance()
		decision := modelRoutingDecision()
		fast := conditionalRouter("fast-lane", "Fast lane", tierTarget("", "anthropic", "claude-haiku"))
		fast.Routing = globalRouting(0, `model == "gpt-4o"`)
		plugin := modelRoutingTestPlugin(t, governance, decision, nil, []configstore.ModelRouter{fast})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		provider, model, _ := req.GetRequestFields()
		assert.Equal(t, schemas.ModelProvider("anthropic"), provider)
		assert.Equal(t, "claude-haiku", model)
		assert.Equal(t, []string{"claude-haiku"}, governance.LoadBalancedModels, "load balancing sees the model the router chose")
		assert.Zero(t, decision.calls(), "a conditional router classifies nothing")
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
		assert.Equal(t, "fast-lane", ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Equal(t, "Fast lane", ctx.Value(schemas.BifrostContextKeyModelRouterName))
		assert.Contains(t, enginesUsed(ctx), schemas.RoutingEngineModelRouter)
		assert.Contains(t, routingLogs(ctx), "Model router 'Fast lane' → provider=anthropic, model=claude-haiku")
	})

	t.Run("it splits by weight across its targets", func(t *testing.T) {
		heavy, light := tierTarget("", "", "heavy"), tierTarget("", "", "light")
		heavy.Weight, light.Weight = 0.8, 0.2
		split := conditionalRouter("split", "Split", heavy, light)
		split.Routing = globalRouting(0, "")
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{split})

		picked := map[string]int{}
		for range 400 {
			picked[routedModel(runPreRequest(t, plugin, newRequestContext(), complexText))]++
		}
		assert.Len(t, picked, 2, "both targets are picked")
		assert.Greater(t, picked["heavy"], picked["light"])
	})

	t.Run("without a target it passes the request on", func(t *testing.T) {
		empty := conditionalRouter("empty", "Empty")
		empty.Routing = globalRouting(0, "")
		second := decisionModelRouter("second", "Second", everyTierTargets("second")...)
		second.Routing = globalRouting(1, "")
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{empty, second})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "second-complex", routedModel(req))
		assert.Equal(t, "second", ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Contains(t, routingLogs(ctx), "Model router 'Empty' has no target; trying the next router")
	})

	t.Run("its one fallback chain goes with whichever target is picked", func(t *testing.T) {
		// A routing rule kept one fallback list for all its targets, and so does a conditional router.
		left, right := tierTarget("", "openai", "left"), tierTarget("", "anthropic", "right")
		left.Weight, right.Weight = 0.5, 0.5
		split := conditionalRouter("split", "Split", left, right)
		split.Fallbacks = []configstoreTables.RoutingFallback{
			{Fallback: schemas.Fallback{Provider: schemas.Anthropic, Model: "right"}},
			{Fallback: schemas.Fallback{Provider: schemas.OpenAI, Model: "left"}},
		}
		split.Routing = globalRouting(0, "")
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{split})

		want := []schemas.Fallback{{Provider: schemas.Anthropic, Model: "right"}, {Provider: schemas.OpenAI, Model: "left"}}
		picked := map[string]int{}
		for range 200 {
			req := runPreRequest(t, plugin, newRequestContext(), complexText)
			_, model, fallbacks := req.GetRequestFields()
			picked[model]++
			require.Equal(t, want, fallbacks, "target %s", model)
		}
		assert.Positive(t, picked["left"])
		assert.Positive(t, picked["right"])
	})

	t.Run("a target can keep the request's provider and model", func(t *testing.T) {
		guarded := conditionalRouter("guarded", "Guarded", tierTarget("", "", ""))
		guarded.Fallbacks = []configstoreTables.RoutingFallback{{Fallback: schemas.Fallback{Provider: schemas.Anthropic, Model: "claude-sonnet"}}}
		guarded.Routing = globalRouting(0, "")
		after := conditionalRouter("after", "After", tierTarget("", "", "never"))
		after.Routing = globalRouting(1, "")
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{guarded, after})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		provider, model, fallbacks := req.GetRequestFields()
		assert.Equal(t, schemas.OpenAI, provider)
		assert.Equal(t, "gpt-4o", model)
		assert.Equal(t, []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-sonnet"}}, fallbacks)
		assert.Equal(t, "guarded", ctx.Value(schemas.BifrostContextKeyModelRouterID), "keeping the model is a choice, so the walk ends there")
	})
}

// TestModelRouterChain pins chaining, which follows the rules a routing rule
// with chain_rule does: the routers are walked again, from the top, on what a
// chained router chose; a chained router fires once in a request, and routers
// made from one routing rule fire as one; the walk repeats at most the
// configured depth; and only the last router's key, fallbacks and first-token
// deadline are applied, with a fallback that names no model keeping the model
// the request arrived with.
func TestModelRouterChain(t *testing.T) {
	t.Run("a chained router's choice is walked again from the top", func(t *testing.T) {
		// Ahead of the chained router in the walk, and matching only what it chooses.
		reroute := conditionalRouter("reroute", "Reroute", tierTarget("", "anthropic", "claude-opus"))
		reroute.Routing = globalRouting(0, `model == "gpt-4o-large"`)
		upgrade := conditionalRouter("upgrade", "Upgrade", tierTarget("", "", "gpt-4o-large"))
		upgrade.Routing = chained(globalRouting(1, `model == "gpt-4o"`))
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{reroute, upgrade})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		provider, model, _ := req.GetRequestFields()
		assert.Equal(t, schemas.ModelProvider("anthropic"), provider)
		assert.Equal(t, "claude-opus", model)
		assert.Equal(t, "reroute", ctx.Value(schemas.BifrostContextKeyModelRouterID), "the last router to take the request is the one recorded")
		logs := routingLogs(ctx)
		assert.Contains(t, logs, "Model router 'Upgrade' → provider=openai, model=gpt-4o-large [chain, continuing]")
		assert.Contains(t, logs, "Chain step 1: re-evaluating with provider=openai, model=gpt-4o-large")
	})

	t.Run("a router that is not chained ends the walk", func(t *testing.T) {
		upgrade := conditionalRouter("upgrade", "Upgrade", tierTarget("", "", "gpt-4o-large"))
		upgrade.Routing = globalRouting(0, `model == "gpt-4o"`)
		reroute := conditionalRouter("reroute", "Reroute", tierTarget("", "anthropic", "claude-opus"))
		reroute.Routing = globalRouting(1, `model == "gpt-4o-large"`)
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{upgrade, reroute})
		assert.Equal(t, "gpt-4o-large", routedModel(runPreRequest(t, plugin, newRequestContext(), complexText)))
	})

	t.Run("a chained router fires once", func(t *testing.T) {
		// It chooses the model it matches, so it would take the request on every pass.
		loop := conditionalRouter("loop", "Loop", tierTarget("", "", "gpt-4o"))
		loop.Routing = chained(globalRouting(0, ""))
		after := conditionalRouter("after", "After", tierTarget("", "", "gpt-4o-mini"))
		after.Routing = globalRouting(1, "")
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{loop, after})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "gpt-4o-mini", routedModel(req))
		assert.Equal(t, "after", ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})

	t.Run("routers made from one routing rule fire as one", func(t *testing.T) {
		fromRule := &configstoreTables.ModelRouterMeta{RoutingRuleIDs: []string{"rule-1"}}
		first := conditionalRouter("split", "Split", tierTarget("", "", "one"))
		first.Meta, first.Routing = fromRule, chained(globalRouting(0, ""))
		second := conditionalRouter("split-2", "Split 2", tierTarget("", "", "two"))
		second.Meta, second.Routing = fromRule, chained(globalRouting(1, ""))
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{first, second})
		assert.Equal(t, "one", routedModel(runPreRequest(t, plugin, newRequestContext(), complexText)),
			"the rule fired once, so its other router does not fire on the next pass")
	})

	t.Run("the walk repeats at most the configured depth", func(t *testing.T) {
		a := conditionalRouter("a", "A", tierTarget("", "", "step-1"))
		a.Routing = chained(globalRouting(0, `model == "gpt-4o"`))
		b := conditionalRouter("b", "B", tierTarget("", "", "step-2"))
		b.Routing = chained(globalRouting(1, `model == "step-1"`))
		c := conditionalRouter("c", "C", tierTarget("", "", "step-3"))
		c.Routing = chained(globalRouting(2, `model == "step-2"`))
		plugin := modelRoutingTestPluginWithConfig(t, &routing.Config{ChainMaxDepth: schemas.Ptr(2)}, routing.NewMockGovernance(),
			modelRoutingDecision().execute, nil, []configstore.ModelRouter{a, b, c})

		ctx := newRequestContext()
		req := runPreRequest(t, plugin, ctx, complexText)
		assert.Equal(t, "step-2", routedModel(req), "two passes ran, and the third router was never reached")
		assert.Contains(t, routingLogs(ctx), "Chain exceeded max depth (2)")
	})

	t.Run("only the last router's key, fallbacks and deadline apply", func(t *testing.T) {
		step := tierTarget("", "openai", "step")
		step.KeyID = schemas.Ptr("step-key")
		step.TTFTTimeoutMs = schemas.Ptr(1500)
		first := conditionalRouter("first", "First", step)
		first.Fallbacks = []configstoreTables.RoutingFallback{{Fallback: schemas.Fallback{Provider: schemas.Anthropic, Model: "claude-sonnet"}}}
		first.Routing = chained(globalRouting(0, `model == "gpt-4o"`))

		run := func(t *testing.T, final configstoreTables.TableModelRouterTarget, chain ...configstoreTables.RoutingFallback) (*schemas.BifrostContext, []schemas.Fallback) {
			t.Helper()
			last := conditionalRouter("last", "Last", final)
			last.Fallbacks = chain
			last.Routing = globalRouting(1, `model == "step"`)
			plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{first, last})
			ctx := newRequestContext()
			req := streamRequest(complexText)
			req.ChatRequest.Fallbacks = []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-4o-mini"}}
			require.NoError(t, plugin.PreRequestHook(ctx, req))
			require.Equal(t, "final", routedModel(req))
			_, _, fallbacks := req.GetRequestFields()
			return ctx, fallbacks
		}

		ctx, fallbacks := run(t, tierTarget("", "", "final"))
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID), "the first router's key is not the last router's")
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout))
		assert.Equal(t, []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-4o-mini"}}, fallbacks, "the request keeps its own chain")

		own := tierTarget("", "openai", "final")
		own.KeyID = schemas.Ptr("final-key")
		own.TTFTTimeoutMs = schemas.Ptr(900)
		ctx, fallbacks = run(t, own, configstoreTables.RoutingFallback{Fallback: schemas.Fallback{Provider: schemas.Azure}})
		assert.Equal(t, "final-key", ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID))
		assert.Equal(t, 900*time.Millisecond, ctx.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout))
		assert.Equal(t, []schemas.Fallback{{Provider: schemas.Azure, Model: "gpt-4o"}}, fallbacks,
			"a fallback that names no model keeps the model the request arrived with, not one a chained router chose")
	})
}

// TestModelRouterTargetFirstTokenDeadline pins a target's first-token
// deadline: it is set for a streaming request the target is applied to, and
// not for one that does not stream.
func TestModelRouterTargetFirstTokenDeadline(t *testing.T) {
	target := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	target.TTFTTimeoutMs = schemas.Ptr(1500)
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, nil)
	serveGeneralRouter(t, plugin, target)

	streaming := newRequestContext()
	require.NoError(t, plugin.PreRequestHook(streaming, streamRequest(complexText)))
	assert.Equal(t, 1500*time.Millisecond, streaming.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout))

	whole := newRequestContext()
	runPreRequest(t, plugin, whole, complexText)
	assert.Nil(t, whole.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout))
}

// TestModelRouterConditionVariables pins that a router's condition reads the
// same request facts a routing rule's does: headers, query parameters, the
// request type, the governance entities and the budget and rate limit usage.
func TestModelRouterConditionVariables(t *testing.T) {
	granted := grantedAccess(schemas.ProviderPermit{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"*"}})
	cases := []struct {
		name      string
		condition string
		prepare   func(ctx *schemas.BifrostContext, governance *routing.MockGovernance)
		stream    bool
		wantTaken bool
	}{
		{
			name: "a header, whatever case the condition writes its name in", condition: `headers["X-App"] == "helpdesk"`,
			prepare: func(ctx *schemas.BifrostContext, _ *routing.MockGovernance) {
				ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-app": "helpdesk"})
			},
			wantTaken: true,
		},
		{
			name: "another value of the header", condition: `headers["x-app"] == "helpdesk"`,
			prepare: func(ctx *schemas.BifrostContext, _ *routing.MockGovernance) {
				ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-app": "billing"})
			},
		},
		{name: "a header the request did not send", condition: `headers["x-app"] == "helpdesk"`},
		{
			name: "a query parameter", condition: `params["tenant"] == "acme"`,
			prepare: func(ctx *schemas.BifrostContext, _ *routing.MockGovernance) {
				ctx.SetValue(schemas.BifrostContextKeyRequestQuery, map[string]string{"tenant": "acme"})
			},
			wantTaken: true,
		},
		{name: "the request type", condition: `request_type == "chat_completion"`, wantTaken: true},
		{name: "a stream is another request type", condition: `request_type == "chat_completion"`, stream: true},
		{name: "a stream by its own type", condition: `request_type == "chat_completion_stream"`, stream: true, wantTaken: true},
		{
			name: "budget use over a threshold", condition: `budget_used > 80.0`,
			prepare: func(_ *schemas.BifrostContext, governance *routing.MockGovernance) {
				governance.Status = &governancePlugin.BudgetAndRateLimitStatus{BudgetPercentUsed: 91}
			},
			wantTaken: true,
		},
		{
			name: "budget use under the threshold", condition: `budget_used > 80.0`,
			prepare: func(_ *schemas.BifrostContext, governance *routing.MockGovernance) {
				governance.Status = &governancePlugin.BudgetAndRateLimitStatus{BudgetPercentUsed: 12}
			},
		},
		{
			name: "rate limit use", condition: `tokens_used > 50.0 || request > 50.0`,
			prepare: func(_ *schemas.BifrostContext, governance *routing.MockGovernance) {
				governance.Status = &governancePlugin.BudgetAndRateLimitStatus{RateLimitRequestPercentUsed: 75}
			},
			wantTaken: true,
		},
		{
			name: "the team's name", condition: `team_name == "platform"`,
			prepare: func(ctx *schemas.BifrostContext, governance *routing.MockGovernance) {
				governance.Access = granted
				ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamName, "platform")
			},
			wantTaken: true,
		},
		{
			name: "the virtual key's id", condition: `virtual_key_id == "vk-1"`,
			prepare: func(ctx *schemas.BifrostContext, governance *routing.MockGovernance) {
				governance.Access = granted
				ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-1")
			},
			wantTaken: true,
		},
		{name: "the provider and the model together", condition: `provider == "openai" && model.startsWith("gpt-4")`, wantTaken: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			governance := routing.NewMockGovernance()
			ctx := newRequestContext()
			if tc.prepare != nil {
				tc.prepare(ctx, governance)
			}
			router := decisionModelRouter("general", "General", everyTierTargets("general")...)
			router.Routing = globalRouting(0, tc.condition)
			plugin := modelRoutingTestPlugin(t, governance, modelRoutingDecision(), nil, []configstore.ModelRouter{router})

			req := llmComplexityChatRequest(complexText)
			if tc.stream {
				req = streamRequest(complexText)
			}
			require.NoError(t, plugin.PreRequestHook(ctx, req))
			if tc.wantTaken {
				assert.Equal(t, "general-complex", routedModel(req))
				assert.Equal(t, "general", ctx.Value(schemas.BifrostContextKeyModelRouterID))
			} else {
				assert.Equal(t, "gpt-4o", routedModel(req))
				assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
			}
		})
	}
}

// TestModelRoutersClassifyWithTheirOwnClassifier pins that routers are
// independent classifiers: three routers, one on the decision model, one on
// the llm classifier and one on embeddings, each classify the requests they
// take with their own config and nobody else's, and only the router that
// takes a request spends a classification on it.
func TestModelRoutersClassifyWithTheirOwnClassifier(t *testing.T) {
	decision := modelRoutingDecision()
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, nil, nil)
	var llmCalls, embeddingCalls atomic.Int32
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		llmCalls.Add(1)
		return llmClassifierChatResponse(`{"tier": "MEDIUM"}`), nil
	})
	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		embeddingCalls.Add(1)
		return testEmbeddingExecutor(ctx, req)
	})

	byDecision := decisionModelRouter("by-decision", "By decision", everyTierTargets("by-decision")...)
	byDecision.Routing = appRouting(0, "decision")
	byLLM := configstore.ModelRouter{ID: "by-llm", Name: "By llm", ComplexityAnalyzerConfig: llmRouterConfig(), Targets: everyTierTargets("by-llm")}
	byLLM.Routing = appRouting(1, "llm")
	semanticConfig := sessionAnalyzerConfig()
	semanticConfig.Session = nil
	bySemantic := configstore.ModelRouter{ID: "by-semantic", Name: "By semantic", ComplexityAnalyzerConfig: *semanticConfig, Targets: everyTierTargets("by-semantic")}
	bySemantic.Routing = appRouting(2, "semantic")
	for _, router := range []*configstore.ModelRouter{&byDecision, &byLLM, &bySemantic} {
		require.NoError(t, reloadRouter(plugin, router))
	}
	require.Eventually(t, func() bool {
		status, ok := plugin.ModelRouterSemanticStatus("by-semantic")
		return ok && status.State == complexity.SemanticStatusReady
	}, 5*time.Second, 10*time.Millisecond, "the semantic router's warmup should finish")
	warmupEmbeddings := embeddingCalls.Load()

	steps := []struct {
		app, text                                 string
		wantModel, wantTier, wantMechanism        string
		wantDecision, wantLLM, wantEmbeddingCalls int32
	}{
		{app: "decision", text: complexText, wantModel: "by-decision-complex", wantTier: complexity.TierComplex, wantMechanism: complexity.MechanismDecision, wantDecision: 1},
		{app: "llm", text: complexText, wantModel: "by-llm-medium", wantTier: complexity.TierMedium, wantMechanism: complexity.MechanismLLM, wantDecision: 1, wantLLM: 1},
		{app: "semantic", text: "a complex request", wantModel: "by-semantic-complex", wantTier: complexity.TierComplex, wantMechanism: complexity.MechanismSemantic, wantDecision: 1, wantLLM: 1, wantEmbeddingCalls: 1},
		{app: "decision", text: simpleText, wantModel: "by-decision-simple", wantTier: complexity.TierSimple, wantMechanism: complexity.MechanismDecision, wantDecision: 2, wantLLM: 1, wantEmbeddingCalls: 1},
	}
	for _, step := range steps {
		ctx := appContext(step.app)
		req := runPreRequest(t, plugin, ctx, step.text)
		assert.Equal(t, step.wantModel, routedModel(req), "app=%s", step.app)
		assert.Equal(t, step.wantTier, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier), "app=%s", step.app)
		assert.Equal(t, step.wantMechanism, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism), "app=%s", step.app)
		assert.EqualValues(t, step.wantDecision, decision.calls(), "decision model calls after app=%s", step.app)
		assert.Equal(t, step.wantLLM, llmCalls.Load(), "llm classifier calls after app=%s", step.app)
		assert.Equal(t, step.wantEmbeddingCalls, embeddingCalls.Load()-warmupEmbeddings, "request embeddings after app=%s", step.app)
	}
}

// TestModelRouterChangesApplyToTheNextRequest walks the edits an operator
// makes to live routers and pins that each one decides the very next request:
// reordering two routers, turning one off and on, moving one to a narrower
// scope, saving one again without routing, which keeps its saved routing, and
// changing a router's classifier.
func TestModelRouterChangesApplyToTheNextRequest(t *testing.T) {
	first := decisionModelRouter("first", "First", everyTierTargets("first")...)
	first.Routing = globalRouting(0, "")
	second := decisionModelRouter("second", "Second", everyTierTargets("second")...)
	second.Routing = globalRouting(1, "")
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{first, second})
	next := func() string {
		t.Helper()
		return routedModel(runPreRequest(t, plugin, newRequestContext(), complexText))
	}

	assert.Equal(t, "first-complex", next(), "the lower priority goes first")

	require.NoError(t, plugin.SetModelRouterRouting("first", configstore.ModelRouterRouting{Enabled: true, Scope: "global", Priority: 5}))
	assert.Equal(t, "second-complex", next(), "a router moved behind another no longer goes first")

	require.NoError(t, plugin.SetModelRouterRouting("second", configstore.ModelRouterRouting{Scope: "global", Priority: 1}))
	assert.Equal(t, "first-complex", next(), "a router turned off passes its requests on")
	require.NoError(t, plugin.SetModelRouterRouting("second", configstore.ModelRouterRouting{Enabled: true, Scope: "global", Priority: 1}))
	assert.Equal(t, "second-complex", next(), "and takes them again once it is back on")

	require.NoError(t, plugin.SetModelRouterRouting("second", *scopedRouting("team", "team-1", 1, "")))
	assert.Equal(t, "first-complex", next(), "a router moved to a team no longer takes a request outside it")

	withoutRouting := first
	withoutRouting.Routing = nil
	require.NoError(t, reloadRouter(plugin, &withoutRouting))
	assert.Equal(t, "first-complex", next(), "a router saved again without routing keeps its saved routing")

	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return llmClassifierChatResponse(`{"tier": "SIMPLE"}`), nil
	})
	asLLM := configstore.ModelRouter{ID: "first", Name: "First", ComplexityAnalyzerConfig: llmRouterConfig(), Targets: everyTierTargets("first")}
	asLLM.Routing = globalRouting(0, "")
	require.NoError(t, reloadRouter(plugin, &asLLM))
	ctx := newRequestContext()
	req := runPreRequest(t, plugin, ctx, complexText)
	assert.Equal(t, "first-simple", routedModel(req), "a router saved with another classifier classifies with it")
	assert.Equal(t, complexity.MechanismLLM, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}

// TestModelRouterSessionsStayApart pins that session routing belongs to each
// router: within one session, a router that reached COMPLEX holds its own
// requests there, while another router keeps classifying on its own ladder.
func TestModelRouterSessionsStayApart(t *testing.T) {
	decision := modelRoutingDecision()
	sessionRouter := func(id string, priority int) configstore.ModelRouter {
		router := configstore.ModelRouter{ID: id, Name: id, ComplexityAnalyzerConfig: sessionRouterConfig(), Targets: everyTierTargets(id)}
		router.Routing = appRouting(priority, id)
		return router
	}
	plugin := modelRoutingTestPluginWithConfig(t, newTestKVStore(t), routing.NewMockGovernance(), decision.execute,
		nil, []configstore.ModelRouter{sessionRouter("a", 0), sessionRouter("b", 1)})

	steps := []struct {
		session, app, text                 string
		wantModel, wantTier, wantMechanism string
		wantRouter                         any
		wantDecisionCalls                  int
	}{
		{session: "s1", app: "a", text: complexText, wantModel: "a-complex", wantTier: complexity.TierComplex, wantMechanism: complexity.MechanismDecision, wantRouter: "a", wantDecisionCalls: 1},
		// Router b starts its own ladder in the same session.
		{session: "s1", app: "b", text: simpleText, wantModel: "b-simple", wantTier: complexity.TierSimple, wantMechanism: complexity.MechanismDecision, wantRouter: "b", wantDecisionCalls: 2},
		// Router a is at its ceiling: it reuses COMPLEX without classifying.
		{session: "s1", app: "a", text: simpleText, wantModel: "a-complex", wantTier: complexity.TierComplex, wantMechanism: complexity.MechanismSession, wantRouter: "a", wantDecisionCalls: 2},
		{session: "s1", app: "b", text: mediumText, wantModel: "b-medium", wantTier: complexity.TierMedium, wantMechanism: complexity.MechanismDecision, wantRouter: "b", wantDecisionCalls: 3},
		// Router b holds MEDIUM against a simpler turn, never a's COMPLEX.
		{session: "s1", app: "b", text: simpleText, wantModel: "b-medium", wantTier: complexity.TierMedium, wantMechanism: complexity.MechanismSession, wantRouter: "b", wantDecisionCalls: 4},
		// Another session of router a starts from nothing.
		{session: "s2", app: "a", text: simpleText, wantModel: "a-simple", wantTier: complexity.TierSimple, wantMechanism: complexity.MechanismDecision, wantRouter: "a", wantDecisionCalls: 5},
	}
	for i, step := range steps {
		ctx := appSessionContext(step.session, step.app)
		req := runPreRequest(t, plugin, ctx, step.text)
		assert.Equal(t, step.wantModel, routedModel(req), "step %d", i)
		assert.Equal(t, step.wantTier, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier), "step %d", i)
		assert.Equal(t, step.wantMechanism, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism), "step %d", i)
		assert.Equal(t, step.wantRouter, ctx.Value(schemas.BifrostContextKeyModelRouterID), "step %d", i)
		assert.Equal(t, step.wantDecisionCalls, decision.calls(), "step %d", i)
	}
}

// TestModelRouterToolContinuation pins what happens to the follow-up request
// of a tool call, which carries no new user text to classify. The router that
// served the turn resumes on the tier it gave it, without classifying again;
// a routing rule's complexity_tier and the router that took the request each
// resume on their own tier; and a router that did not serve the turn has
// nothing to resume, so it classifies the turn's text itself.
func TestModelRouterToolContinuation(t *testing.T) {
	t.Run("the router that served the turn resumes on its tier", func(t *testing.T) {
		decision := modelRoutingDecision()
		a := decisionModelRouter("a", "A", everyTierTargets("a")...)
		a.Routing = appRouting(0, "a")
		b := decisionModelRouter("b", "B", everyTierTargets("b")...)
		b.Routing = appRouting(1, "b")
		plugin := modelRoutingTestPluginWithConfig(t, newTestKVStore(t), routing.NewMockGovernance(), decision.execute,
			nil, []configstore.ModelRouter{a, b})

		turn := appSessionContext("s1", "a")
		req := runPreRequest(t, plugin, turn, mediumText)
		require.Equal(t, "a-medium", routedModel(req))
		_, _, err := plugin.PostLLMHook(turn, servedChatResponse(), nil)
		require.NoError(t, err)
		require.Equal(t, 1, decision.calls())

		continuation := appSessionContext("s1", "a")
		req = toolContinuationRequest(complexText)
		require.NoError(t, plugin.PreRequestHook(continuation, req))
		assert.Equal(t, "a-medium", routedModel(req), "the tool loop stays on the model that started the turn")
		assert.Equal(t, complexity.MechanismSession, continuation.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
		assert.Equal(t, 1, decision.calls(), "a continuation is not classified again")
		assert.Contains(t, routingLogs(continuation), "Complexity reused this turn's tier: tier=MEDIUM reason=tool-continuation")

		other := appSessionContext("s1", "b")
		req = toolContinuationRequest(complexText)
		require.NoError(t, plugin.PreRequestHook(other, req))
		assert.Equal(t, "b-complex", routedModel(req), "a router that did not serve the turn classifies the turn's text")
		assert.Equal(t, 2, decision.calls())
	})
}

// TestModelRouterRequestTypes pins which requests the router step handles. A
// request with user text is classified whatever its API (chat, streaming
// chat, Responses). A request with nothing to classify, like an embedding, is
// still taken by the router whose routing matches, which records the router
// and leaves the model alone. A passthrough request and a request without a
// model never reach the step.
func TestModelRouterRequestTypes(t *testing.T) {
	build := func(t *testing.T) (*routing.RoutingPlugin, *decisionRecorder, *routing.MockGovernance) {
		t.Helper()
		decision := modelRoutingDecision()
		governance := routing.NewMockGovernance()
		router := decisionModelRouter("general", "General", everyTierTargets("general")...)
		router.Routing = globalRouting(0, "")
		return modelRoutingTestPlugin(t, governance, decision, nil, []configstore.ModelRouter{router}), decision, governance
	}

	t.Run("a streaming chat request is routed", func(t *testing.T) {
		plugin, _, _ := build(t)
		req := streamRequest(complexText)
		require.NoError(t, plugin.PreRequestHook(newRequestContext(), req))
		assert.Equal(t, "general-complex", routedModel(req))
	})

	t.Run("a Responses request is routed", func(t *testing.T) {
		plugin, decision, _ := build(t)
		userRole := schemas.ResponsesInputMessageRoleUser
		text := complexText
		req := &schemas.BifrostRequest{
			RequestType: schemas.ResponsesRequest,
			ResponsesRequest: &schemas.BifrostResponsesRequest{
				Provider: schemas.OpenAI, Model: "gpt-4o",
				Input: []schemas.ResponsesMessage{{Role: &userRole, Content: &schemas.ResponsesMessageContent{ContentStr: &text}}},
			},
		}
		ctx := newRequestContext()
		require.NoError(t, plugin.PreRequestHook(ctx, req))
		assert.Equal(t, "general-complex", req.ResponsesRequest.Model)
		assert.Equal(t, "general", ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Equal(t, 1, decision.calls())
	})

	t.Run("an embedding request is taken but has nothing to classify", func(t *testing.T) {
		plugin, decision, _ := build(t)
		req := &schemas.BifrostRequest{
			RequestType:      schemas.EmbeddingRequest,
			EmbeddingRequest: &schemas.BifrostEmbeddingRequest{Provider: schemas.OpenAI, Model: "text-embedding-3-small"},
		}
		ctx := newRequestContext()
		require.NoError(t, plugin.PreRequestHook(ctx, req))
		assert.Equal(t, "text-embedding-3-small", req.EmbeddingRequest.Model)
		assert.Zero(t, decision.calls())
		assert.Equal(t, "general", ctx.Value(schemas.BifrostContextKeyModelRouterID), "the router whose routing matched is recorded")
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
		assert.Equal(t, complexity.MechanismSkipped, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
		assert.Contains(t, routingLogs(ctx), "Model router 'General' produced no tier")
	})

	t.Run("a passthrough request never reaches the step", func(t *testing.T) {
		plugin, decision, governance := build(t)
		ctx := newRequestContext()
		require.NoError(t, plugin.PreRequestHook(ctx, &schemas.BifrostRequest{RequestType: schemas.PassthroughRequest}))
		assert.Zero(t, decision.calls())
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Empty(t, governance.LoadBalancedModels)
	})

	t.Run("a request without a model is left alone", func(t *testing.T) {
		plugin, decision, _ := build(t)
		req := llmComplexityChatRequest(complexText)
		req.ChatRequest.Model = ""
		ctx := newRequestContext()
		require.NoError(t, plugin.PreRequestHook(ctx, req))
		assert.Zero(t, decision.calls())
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})
}

// TestModelRouterChoiceReachesGovernance pins what the virtual key's own
// routing is handed after a router routed the request: the router's model,
// once, and a refusal from it fails the hook as it does without routers.
func TestModelRouterChoiceReachesGovernance(t *testing.T) {
	governance := routing.NewMockGovernance()
	governance.LoadBalanceErr = errors.New("no provider is allowed for this model")
	var balanced schemas.ModelProvider
	governance.OnLoadBalance = func(req *schemas.BifrostRequest) { balanced, _, _ = req.GetRequestFields() }
	router := decisionModelRouter("general", "General", tierTarget(complexity.TierComplex, "anthropic", "claude-opus"))
	router.Routing = globalRouting(0, "")
	plugin := modelRoutingTestPlugin(t, governance, modelRoutingDecision(), nil, []configstore.ModelRouter{router})

	err := plugin.PreRequestHook(newRequestContext(), llmComplexityChatRequest(complexText))
	require.ErrorIs(t, err, governance.LoadBalanceErr)
	assert.Equal(t, []string{"claude-opus"}, governance.AllowlistModels)
	assert.Equal(t, []string{"claude-opus"}, governance.LoadBalancedModels)
	assert.Equal(t, schemas.Anthropic, balanced, "load balancing starts from the router's provider")
}

// TestModelRouterWritesSurviveCoreRestrictedWrites runs the hook the way core
// runs it, with writes to reserved context keys blocked and the plugin's own
// scope on the context, and pins that everything a router decides still
// reaches the request context: the router, the tier, the key it pins and the
// engines used. A write to a reserved key would be dropped without an error.
func TestModelRouterWritesSurviveCoreRestrictedWrites(t *testing.T) {
	pinned := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	pinned.KeyID = schemas.Ptr("anthropic-key")
	router := decisionModelRouter("support", "Support", pinned)
	router.Routing = globalRouting(0, "")
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), modelRoutingDecision(), nil, []configstore.ModelRouter{router})

	root := newRequestContext()
	root.BlockRestrictedWrites()
	pluginName := routing.PluginName
	scoped := root.WithPluginScope(&pluginName)
	req := llmComplexityChatRequest(complexText)
	require.NoError(t, plugin.PreRequestHook(scoped, req))
	scoped.ReleasePluginScope()

	assert.Equal(t, "claude-opus", routedModel(req))
	assert.Equal(t, "support", root.Value(schemas.BifrostContextKeyModelRouterID))
	assert.Equal(t, "Support", root.Value(schemas.BifrostContextKeyModelRouterName))
	assert.Equal(t, complexity.TierComplex, root.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	assert.Equal(t, complexity.MechanismDecision, root.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	assert.Equal(t, "anthropic-key", root.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID))
	assert.Equal(t, []string{schemas.RoutingEngineModelRouter}, enginesUsed(root))
}

// TestModelRoutersChangeUnderTraffic changes routers while requests run:
// targets replaced, a router turned off and on, another removed and served
// again. Under -race it pins that the step reads and writes its state safely,
// and for every request that whichever router took it routed it to one of
// that router's own models, never another router's.
func TestModelRoutersChangeUnderTraffic(t *testing.T) {
	a := decisionModelRouter("a", "A", tierTarget(complexity.TierComplex, "", "a-model"))
	a.Routing = globalRouting(0, "")
	b := decisionModelRouter("b", "B", tierTarget(complexity.TierComplex, "", "b-model"))
	b.Routing = globalRouting(1, "")
	decide := func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		tier, confidence := complexity.TierComplex, 0.9
		return &schemas.BifrostDecisionResponse{Answers: []schemas.DecisionAnswer{
			{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("complexity_tier"), Choice: &schemas.DecisionScalar{Str: &tier}, Confidence: &confidence},
		}}, nil
	}
	plugin := modelRoutingTestPluginWithConfig(t, nil, newQuietGovernance(), decide, nil, []configstore.ModelRouter{a, b})

	modelsOf := map[any][]string{"a": {"a-model", "a-model-2"}, "b": {"b-model"}, nil: {"gpt-4o"}}
	stop := make(chan struct{})
	var writers, readers sync.WaitGroup
	edit := func(change func(i int)) {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
					change(i)
				}
			}
		}()
	}
	edit(func(i int) {
		model := "a-model"
		if i%2 == 1 {
			model = "a-model-2"
		}
		_ = testRulesOf(plugin).SaveModelRouterTargets(context.Background(), "a", []configstoreTables.TableModelRouterTarget{tierTarget(complexity.TierComplex, "", model)})
	})
	edit(func(i int) {
		_ = plugin.SetModelRouterRouting("a", configstore.ModelRouterRouting{Enabled: i%2 == 0, Scope: "global"})
	})
	edit(func(i int) {
		if i%2 == 0 {
			_ = plugin.RemoveModelRouter("b")
			return
		}
		served := b
		_ = reloadRouter(plugin, &served)
	})

	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 300 {
				ctx := newRequestContext()
				req := llmComplexityChatRequest(complexText)
				if err := plugin.PreRequestHook(ctx, req); err != nil {
					t.Errorf("the hook failed while routers changed: %v", err)
					return
				}
				router := ctx.Value(schemas.BifrostContextKeyModelRouterID)
				if !assert.Contains(t, modelsOf[router], routedModel(req), "router %v routed the request to a model that is not its own", router) {
					return
				}
			}
		}()
	}
	readers.Wait()
	close(stop)
	writers.Wait()
}

// ---------------------------------------------------------------------------
// Regressions: who a model router's decision is logged against
// ---------------------------------------------------------------------------

// TestModelRouterSessionStoreOutageLogsAsModelRouter pins which engine a
// router's classification is logged under when the session state cannot be
// used in the middle of a session. The router still routes the request on what
// its classifier just answered, and that decision is the router's: a request
// no routing rule matched must not show a routing-rule decision in its trail
// (see TestModelRouterClassificationLogEngine for the same rule without an
// outage).
func TestModelRouterSessionStoreOutageLogsAsModelRouter(t *testing.T) {
	config, sessions := sessionConfigForTest(t)
	router := configstore.ModelRouter{ID: "support", Name: "Support", ComplexityAnalyzerConfig: sessionRouterConfig(), Targets: everyTierTargets("support")}
	router.Routing = globalRouting(0, "")
	plugin := modelRoutingTestPluginWithConfig(t, config, routing.NewMockGovernance(), modelRoutingDecision().execute,
		nil, []configstore.ModelRouter{router})

	req := runPreRequest(t, plugin, complexitySessionContext("s1"), simpleText)
	require.Equal(t, "support-simple", routedModel(req))

	corruptSessions(sessions)
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

// TestRefusedRouterFallbacksAreReported pins who a "fallback will be refused
// by governance" warning names, so an operator looks in the right place: a
// fallback of a router's target that the virtual key refuses is reported
// against that router, under the model router engine.
func TestRefusedRouterFallbacksAreReported(t *testing.T) {
	governance := routing.NewMockGovernance()
	governance.Access = grantedAccess(
		schemas.ProviderPermit{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o", "gpt-4o-mini"}, KeyIDs: schemas.WhiteList{"*"}},
		schemas.ProviderPermit{Provider: "anthropic", AllowedModels: schemas.WhiteList{"claude-opus"}, KeyIDs: schemas.WhiteList{"*"}},
	)
	withRefusedFallback := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	withRefusedFallback.ParsedFallbacks = []configstoreTables.RoutingFallback{{Fallback: schemas.Fallback{Provider: schemas.Groq, Model: "llama-3.1-8b-instant"}}}
	router := decisionModelRouter("support", "Support", withRefusedFallback)
	router.Routing = globalRouting(0, "")
	plugin := modelRoutingTestPlugin(t, governance, modelRoutingDecision(), nil, []configstore.ModelRouter{router})

	ctx := scopedRequestContext("vk-1", "", "", "")
	req := runPreRequest(t, plugin, ctx, complexText)

	_, _, fallbacks := req.GetRequestFields()
	require.Equal(t, []schemas.Fallback{{Provider: schemas.Groq, Model: "llama-3.1-8b-instant"}}, fallbacks)
	const refused = "Model router 'Support': fallback groq/llama-3.1-8b-instant will be refused by governance: provider groq is not allowed for this request"
	engine := ""
	for _, entry := range ctx.GetRoutingEngineLogs() {
		if entry.Message == refused {
			engine = entry.Engine
		}
	}
	assert.Equal(t, schemas.RoutingEngineModelRouter, engine, "logs:\n%s", routingLogs(ctx))
}

// ---------------------------------------------------------------------------
// Cost of the model router step on the request path
// ---------------------------------------------------------------------------

// BenchmarkPreRequestHookModelRouters measures the whole routing hook, since
// the model router step sits on every request. "no router" is the common
// case and the one that must not regress: a gateway with no enabled router
// pays one empty-map check for the step. The other cases show what a router
// adds: a condition evaluation per router tried and, for the router that
// takes the request, a classification (here a fake that answers at once).
func BenchmarkPreRequestHookModelRouters(b *testing.B) {
	decide := func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		tier, confidence := complexity.TierComplex, 0.9
		return &schemas.BifrostDecisionResponse{Answers: []schemas.DecisionAnswer{
			{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("complexity_tier"), Choice: &schemas.DecisionScalar{Str: &tier}, Confidence: &confidence},
		}}, nil
	}
	router := func(id string, priority int, condition string) configstore.ModelRouter {
		r := decisionModelRouter(id, id, tierTarget(complexity.TierComplex, "anthropic", "claude-opus"))
		r.Routing = globalRouting(priority, condition)
		return r
	}
	conditional := func(id string, priority int, condition string) configstore.ModelRouter {
		r := conditionalRouter(id, id, tierTarget("", "anthropic", "claude-opus"))
		r.Routing = globalRouting(priority, condition)
		return r
	}
	// A gateway whose routing rules all became conditional routers: none but
	// the last takes the request, so every condition is evaluated.
	thousand := make([]configstore.ModelRouter, 0, 1000)
	for i := range 999 {
		thousand = append(thousand, conditional(fmt.Sprintf("router-%04d", i), i, fmt.Sprintf(`model == "model-%d"`, i)))
	}
	thousand = append(thousand, conditional("last", 999, `model == "gpt-4o"`))
	// Routing rules on complexity_tier that could not be merged: classifier
	// routers with the same settings, where every one but the last has no
	// model for the request's tier and passes it on.
	tierLadder := func(n int) []configstore.ModelRouter {
		ladder := make([]configstore.ModelRouter, 0, n)
		for i := range n - 1 {
			r := decisionModelRouter(fmt.Sprintf("tier-%03d", i), fmt.Sprintf("tier-%03d", i), tierTarget(complexity.TierSimple, "anthropic", "claude-haiku"))
			r.Routing = passingOn(globalRouting(i, ""))
			ladder = append(ladder, r)
		}
		last := decisionModelRouter("tier-last", "tier-last", tierTarget(complexity.TierComplex, "anthropic", "claude-opus"))
		last.Routing = globalRouting(n, "")
		return append(ladder, last)
	}
	// The routers a gateway's routing rules became: a thousand rules where only
	// the last takes the request, and a rule for each tier.
	thousandRules := make([]configstoreTables.TableRoutingRule, 0, 1000)
	for i := range 999 {
		thousandRules = append(thousandRules, migratingRule(fmt.Sprintf("rule-%04d", i), fmt.Sprintf(`model == "model-%d"`, i), i, "claude-opus"))
	}
	thousandRules = append(thousandRules, migratingRule("last", `model == "gpt-4o"`, 999, "claude-opus"))
	tierRules := []configstoreTables.TableRoutingRule{
		migratingRule("simple", `complexity_tier == "SIMPLE"`, 0, "claude-haiku"),
		migratingRule("medium", `complexity_tier == "MEDIUM"`, 1, "claude-sonnet"),
		migratingRule("complex", `complexity_tier == "COMPLEX"`, 2, "claude-opus"),
	}
	migrated := func(routingRules []configstoreTables.TableRoutingRule) []configstore.ModelRouter {
		migration, err := configstore.ModelRoutersFromRoutingRules(slices.Clone(routingRules), decisionAnalyzerTestConfig(), nil)
		if err != nil {
			b.Fatal(err)
		}
		return migration.Routers
	}
	cases := []struct {
		name    string
		routers []configstore.ModelRouter
	}{
		{name: "no router"},
		{name: "one router that is off", routers: []configstore.ModelRouter{decisionModelRouter("off", "off")}},
		{name: "three routers, none takes the request", routers: []configstore.ModelRouter{
			router("a", 0, `model == "a"`), router("b", 1, `headers["x-app"] == "b"`), router("c", 2, `provider == "c"`),
		}},
		{name: "a router takes the request", routers: []configstore.ModelRouter{router("a", 0, `model == "gpt-4o"`)}},
		{name: "a conditional router takes the request", routers: []configstore.ModelRouter{conditional("a", 0, `model == "gpt-4o"`)}},
		{name: "a chained conditional router, then another", routers: []configstore.ModelRouter{
			func() configstore.ModelRouter {
				r := conditionalRouter("first", "first", tierTarget("", "", "gpt-4o-large"))
				r.Routing = chained(globalRouting(0, `model == "gpt-4o"`))
				return r
			}(),
			conditional("second", 1, `model == "gpt-4o-large"`),
		}},
		{name: "a thousand conditional routers, the last takes the request", routers: thousand},
		{name: "three classifier routers with the same settings, the last takes the request", routers: tierLadder(3)},
		{name: "a hundred classifier routers with the same settings, the last takes the request", routers: tierLadder(100)},
		{name: "the routers a thousand routing rules become", routers: migrated(thousandRules)},
		{name: "the router a routing rule for each tier becomes", routers: migrated(tierRules)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			plugin := modelRoutingTestPluginWithConfig(b, nil, newQuietGovernance(), decide, nil, tc.routers)
			req := llmComplexityChatRequest(complexText)
			b.ReportAllocs()
			for b.Loop() {
				req.ChatRequest.Provider, req.ChatRequest.Model, req.ChatRequest.Fallbacks = schemas.OpenAI, "gpt-4o", nil
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				if err := plugin.PreRequestHook(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// testRuleStores maps each plugin modelRoutingTestPluginWithConfig built to the rule store its
// routers are saved in.
var testRuleStores sync.Map // *routing.RoutingPlugin -> *rules.TestRules

// testRulesOf returns the rule store plugin's routers are saved in.
func testRulesOf(plugin *routing.RoutingPlugin) *rules.TestRules {
	store, _ := testRuleStores.Load(plugin)
	return store.(*rules.TestRules)
}

// reloadRouter saves router and serves the record the save left, as the HTTP server does after a
// save, so a save without routing serves the routing the router was saved with.
func reloadRouter(plugin *routing.RoutingPlugin, router *configstore.ModelRouter) error {
	store := testRulesOf(plugin)
	if err := store.SaveModelRouter(context.Background(), router); err != nil {
		return err
	}
	record, ok := store.Live().Collections.ModelRouters.Get(router.ID)
	if !ok {
		return configstore.ErrNotFound
	}
	return plugin.ReloadModelRouter(&record.Router)
}

// ---------------------------------------------------------------------------
// Routers made from routing rules route as the rules did
//
// Routing rules became model routers (configstore.ModelRoutersFromRoutingRules). While the rule
// engine still ran, every rule set below was served by the rules and by the routers made from it,
// and each request had to come out of both the same. What the rules answered is written down here,
// so the routers are still held to it now that no request is routed by a rule.
// ---------------------------------------------------------------------------

// routedOutcome is everything the routing hook did to one request that the provider call, the
// client or a log row can see.
type routedOutcome struct {
	Provider     string
	Model        string
	Fallbacks    []string      // "provider/model@key", in order
	KeyPin       string        // the key the request is pinned to
	FirstToken   time.Duration // the first-token deadline of a stream
	Tier         string        // the complexity tier published on the request
	Mechanism    string        // how the tier was reached
	Allowlist    []string      // the model the virtual key's allowlist was read for
	LoadBalanced []string      // the model load balancing was handed
	TookBy       string        // the router that took the request
	Classified   int           // classifier calls the request cost
}

// migrationRequest is one request sent through the routers a rule set became.
type migrationRequest struct {
	text      string            // what the classifier reads; "" for a request with nothing to classify
	model     string            // "" is gpt-4o
	headers   map[string]string // request headers
	stream    bool              // a streaming request
	scoped    bool              // sent with virtual key vk-1 of team-1 and customer-1, as user-1
	large     bool              // a large payload, whose body is not parsed
	budget    float64           // percent of the budget used
	down      bool              // the classifier fails
	session   string            // the session the request belongs to
	fallbacks []schemas.Fallback
}

// String names the request in a failure.
func (r migrationRequest) String() string {
	return fmt.Sprintf("text=%q model=%q headers=%v stream=%v scoped=%v large=%v budget=%v down=%v session=%q fallbacks=%d",
		r.text, r.model, r.headers, r.stream, r.scoped, r.large, r.budget, r.down, r.session, len(r.fallbacks))
}

// migrationSide is a gateway serving the routers a rule set became.
type migrationSide struct {
	plugin     *routing.RoutingPlugin
	governance *routing.MockGovernance
	decision   *decisionRecorder
}

// route runs the routing hook on a request and returns what it did.
func (s *migrationSide) route(t testing.TB, r migrationRequest) (routedOutcome, *schemas.BifrostContext) {
	t.Helper()
	ctx := newRequestContext()
	s.governance.Access = nil
	if r.scoped {
		ctx = scopedRequestContext("vk-1", "user-1", "team-1", "customer-1")
		s.governance.Access = grantedAccess()
	}
	if r.headers != nil {
		ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, r.headers)
	}
	if r.session != "" {
		ctx.SetValue(schemas.BifrostContextKeySessionID, r.session)
	}
	s.governance.Status = nil
	if r.budget > 0 {
		s.governance.Status = &governancePlugin.BudgetAndRateLimitStatus{BudgetPercentUsed: r.budget}
	}
	s.governance.AllowlistModels, s.governance.LoadBalancedModels = nil, nil
	s.decision.mu.Lock()
	s.decision.err = nil
	if r.down {
		s.decision.err = &schemas.BifrostError{Error: &schemas.ErrorField{Message: "the classifier is down"}}
	}
	s.decision.mu.Unlock()

	model := r.model
	if model == "" {
		model = "gpt-4o"
	}
	req := llmComplexityChatRequest(r.text)
	req.ChatRequest.Model = model
	req.ChatRequest.Fallbacks = r.fallbacks
	if r.text == "" {
		req.ChatRequest.Input = nil
	}
	if r.stream {
		req.RequestType = schemas.ChatCompletionStreamRequest
	}
	var metadata *schemas.LargePayloadMetadata
	if r.large {
		metadata = &schemas.LargePayloadMetadata{Model: model}
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, metadata)
		req = &schemas.BifrostRequest{RequestType: req.RequestType, ChatRequest: &schemas.BifrostChatRequest{}}
	}

	before := s.decision.calls()
	require.NoError(t, s.plugin.PreRequestHook(ctx, req), "%s", r)

	provider, routed, fallbacks := req.GetRequestFields()
	out := routedOutcome{
		Provider:     string(provider),
		Model:        routed,
		Allowlist:    s.governance.AllowlistModels,
		LoadBalanced: s.governance.LoadBalancedModels,
		Classified:   s.decision.calls() - before,
	}
	if metadata != nil {
		out.Provider, out.Model = "", metadata.Model
	}
	for _, fallback := range fallbacks {
		out.Fallbacks = append(out.Fallbacks, fmt.Sprintf("%s/%s@%s", fallback.Provider, fallback.Model, fallback.KeyID))
	}
	out.KeyPin, _ = ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID).(string)
	out.FirstToken, _ = ctx.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout).(time.Duration)
	out.Tier, _ = ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier).(string)
	out.Mechanism, _ = ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism).(string)
	out.TookBy, _ = ctx.Value(schemas.BifrostContextKeyModelRouterID).(string)
	return out, ctx
}

// ruleMigration is a rule set served by the model routers it became.
type ruleMigration struct {
	migrationSide
	migration *configstore.RoutingRuleMigration
	rules     map[string][]string // router id -> the rules it was made from
}

// migrateRules serves the routers routingRules become, classifying with settings, beside the
// routers the gateway already has. config is the plugin config; nil is none.
func migrateRules(t testing.TB, config *routing.Config, settings *complexity.AnalyzerConfig, routingRules []configstoreTables.TableRoutingRule, existing []configstore.ModelRouter) *ruleMigration {
	t.Helper()
	migration, err := configstore.ModelRoutersFromRoutingRules(slices.Clone(routingRules), settings, slices.Clone(existing))
	require.NoError(t, err)

	routers := slices.Clone(existing)
	for _, moved := range migration.Moved {
		for i := range routers {
			if routers[i].ID == moved.RouterID {
				routing := moved.Routing
				routers[i].Routing = &routing
			}
		}
	}
	m := &ruleMigration{migration: migration, rules: map[string][]string{}}
	for _, note := range migration.Notes {
		for _, id := range note.RouterIDs {
			m.rules[id] = append(m.rules[id], note.RuleID)
		}
	}
	m.governance, m.decision = routing.NewMockGovernance(), modelRoutingDecision()
	m.plugin = modelRoutingTestPluginWithConfig(t, config, m.governance, m.decision.execute, nil, append(routers, migration.Routers...))
	return m
}

// migrationRequests is the request grid every rule set is routed on: each tier, two models,
// headers a condition can read, streams, a caller with every scope, and the requests that are
// routed differently from an ordinary one.
func migrationRequests() []migrationRequest {
	headerSets := []map[string]string{nil, {"x-app": "support"}, {"x-plan": "pro", "x-tier": "SIMPLE"}}
	var requests []migrationRequest
	for _, text := range []string{simpleText, mediumText, complexText} {
		for _, model := range []string{"gpt-4o", "o3"} {
			for _, headers := range headerSets {
				for _, stream := range []bool{false, true} {
					for _, scoped := range []bool{false, true} {
						requests = append(requests, migrationRequest{text: text, model: model, headers: headers, stream: stream, scoped: scoped})
					}
				}
			}
		}
	}
	return append(requests,
		migrationRequest{text: ""},
		migrationRequest{text: "", headers: map[string]string{"x-plan": "pro"}, scoped: true},
		migrationRequest{text: complexText, down: true},
		migrationRequest{text: complexText, down: true, headers: map[string]string{"x-app": "support"}, scoped: true},
		migrationRequest{text: complexText, large: true},
		migrationRequest{text: complexText, large: true, headers: map[string]string{"x-app": "support"}, scoped: true, stream: true},
		migrationRequest{text: simpleText, large: true, model: "o3", headers: map[string]string{"x-plan": "pro", "x-tier": "SIMPLE"}},
		migrationRequest{text: simpleText, budget: 95},
		migrationRequest{text: complexText, budget: 95, scoped: true, model: "o3"},
		migrationRequest{text: mediumText, fallbacks: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-haiku"}}},
		migrationRequest{text: mediumText, stream: true, model: "o3", fallbacks: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-haiku"}}},
	)
}

// String writes where a request ended up in one line: provider and model, then whatever else the
// routing hook set on it.
func (o routedOutcome) String() string {
	line := o.Provider + "/" + o.Model
	if o.KeyPin != "" {
		line += " key=" + o.KeyPin
	}
	if o.FirstToken > 0 {
		line += " first-token=" + o.FirstToken.String()
	}
	if len(o.Fallbacks) > 0 {
		line += " fallbacks=" + strings.Join(o.Fallbacks, ",")
	}
	if o.Tier != "" {
		line += " tier=" + o.Tier
	}
	return line
}

// migrationProbes are the requests whose answers are written down in full for every rule set of
// TestRoutersMadeFromRoutingRulesRouteAsTheRulesDid: each tier, another model, a caller in every
// scope with headers, a stream, a large payload and a classifier that fails.
func migrationProbes() []migrationRequest {
	return []migrationRequest{
		{text: simpleText},
		{text: mediumText},
		{text: complexText},
		{text: simpleText, model: "o3"},
		{text: complexText, model: "o3", headers: map[string]string{"x-plan": "pro", "x-tier": "SIMPLE"}, scoped: true},
		{text: mediumText, headers: map[string]string{"x-app": "support"}, scoped: true, stream: true},
		{text: simpleText, large: true},
		{text: complexText, down: true},
	}
}

// migratingRule is an enabled global routing rule that sends the requests its condition holds for
// to model, on the request's provider.
func migratingRule(id, condition string, priority int, model string) configstoreTables.TableRoutingRule {
	rule := ruleFor(id, condition, ruleTarget("", model))
	rule.Priority = priority
	rule.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return rule
}

// inScope moves a rule to one entity's scope.
func inScope(rule configstoreTables.TableRoutingRule, scope, scopeID string) configstoreTables.TableRoutingRule {
	rule.Scope, rule.ScopeID = scope, schemas.Ptr(scopeID)
	return rule
}

// chaining makes a rule chain: the rules are evaluated again on what it chose.
func chaining(rule configstoreTables.TableRoutingRule) configstoreTables.TableRoutingRule {
	rule.ChainRule = true
	return rule
}

// turnedOff turns a rule off.
func turnedOff(rule configstoreTables.TableRoutingRule) configstoreTables.TableRoutingRule {
	rule.Enabled = schemas.Ptr(false)
	return rule
}

// withFallbacks gives a rule a fallback chain, written as config.json writes it.
func withFallbacks(t testing.TB, rule configstoreTables.TableRoutingRule, fallbacks string) configstoreTables.TableRoutingRule {
	t.Helper()
	rule.ParsedFallbacks = nil
	require.NoError(t, json.Unmarshal([]byte(fallbacks), &rule.ParsedFallbacks))
	return rule
}

// withTargets replaces a rule's targets.
func withTargets(rule configstoreTables.TableRoutingRule, targets ...configstoreTables.TableRoutingTarget) configstoreTables.TableRoutingRule {
	rule.Targets = targets
	return rule
}

// migratedRouterAnswers is where each probe of migrationProbes ended up under the routing rules,
// for every rule set of TestRoutersMadeFromRoutingRulesRouteAsTheRulesDid, as routedOutcome.String
// writes it. A rule set whose routers classify less than its rules did is written without the
// tier.
var migratedRouterAnswers = map[string][]string{
	"a rule with no condition": {
		"openai/everything",
		"openai/everything",
		"openai/everything",
		"openai/everything",
		"openai/everything",
		"openai/everything",
		"/everything",
		"openai/everything",
	},
	"a condition on the model": {
		"openai/gpt-4o-mini",
		"openai/gpt-4o-mini",
		"openai/gpt-4o-mini",
		"openai/o3",
		"openai/o3",
		"openai/gpt-4o-mini",
		"/gpt-4o-mini",
		"openai/gpt-4o-mini",
	},
	"a condition on a header": {
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/o3",
		"openai/o3",
		"openai/support-model",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"a header that may be missing": {
		"openai/no-app",
		"openai/no-app",
		"openai/no-app",
		"openai/no-app",
		"openai/no-app",
		"openai/has-app",
		"/no-app",
		"openai/no-app",
	},
	"nested and, or and not": {
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/nested",
		"openai/nested",
		"openai/gpt-4o",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"conditions on who is calling": {
		"openai/for-nobody",
		"openai/for-nobody",
		"openai/for-nobody",
		"openai/for-nobody",
		"openai/for-team",
		"openai/for-team",
		"/for-nobody",
		"openai/for-nobody",
	},
	"a condition on the budget": {
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/o3",
		"openai/o3",
		"openai/gpt-4o",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"a condition on the request type": {
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/o3",
		"openai/o3",
		"openai/for-streams",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"blank space and a condition that does not compile": {
		"openai/works",
		"openai/works",
		"openai/works",
		"openai/o3",
		"openai/o3",
		"openai/works",
		"/works",
		"openai/works",
	},
	"the first rule that matches wins": {
		"openai/second",
		"openai/second",
		"openai/second",
		"openai/third",
		"openai/third",
		"openai/first",
		"/second",
		"openai/second",
	},
	"negative priorities": {
		"openai/zero",
		"openai/zero",
		"openai/zero",
		"openai/plus",
		"openai/plus",
		"openai/minus",
		"/zero",
		"openai/zero",
	},
	"rules with one priority": {
		"openai/newer",
		"openai/newer",
		"openai/newer",
		"openai/older",
		"openai/older",
		"openai/newer",
		"/newer",
		"openai/newer",
	},
	"a rule that is off": {
		"openai/on",
		"openai/on",
		"openai/on",
		"openai/on",
		"openai/on",
		"openai/on",
		"/on",
		"openai/on",
	},
	"narrower scopes come first": {
		"openai/global",
		"openai/global",
		"openai/global",
		"openai/global",
		"openai/user",
		"openai/key",
		"/global",
		"openai/global",
	},
	"a scope the request is not in": {
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/global",
		"openai/global",
		"openai/gpt-4o",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"the same name and priority in two scopes": {
		"openai/global-copy",
		"openai/global-copy",
		"openai/global-copy",
		"openai/o3",
		"openai/o3",
		"openai/team-copy",
		"/global-copy",
		"openai/global-copy",
	},
	"a target with a provider, a key and a deadline": {
		"azure/gpt-4o key=azure-key fallbacks=anthropic/claude-sonnet@,openai/gpt-4o@,openai/gpt-4o-mini@openai-key",
		"azure/gpt-4o key=azure-key fallbacks=anthropic/claude-sonnet@,openai/gpt-4o@,openai/gpt-4o-mini@openai-key",
		"azure/gpt-4o key=azure-key fallbacks=anthropic/claude-sonnet@,openai/gpt-4o@,openai/gpt-4o-mini@openai-key",
		"openai/o3",
		"openai/o3",
		"azure/gpt-4o key=azure-key first-token=1.5s fallbacks=anthropic/claude-sonnet@,openai/gpt-4o@,openai/gpt-4o-mini@openai-key",
		"/azure/gpt-4o key=azure-key",
		"azure/gpt-4o key=azure-key fallbacks=anthropic/claude-sonnet@,openai/gpt-4o@,openai/gpt-4o-mini@openai-key",
	},
	"a target that changes only the provider": {
		"azure/gpt-4o",
		"azure/gpt-4o",
		"azure/gpt-4o",
		"openai/o3",
		"openai/o3",
		"azure/gpt-4o",
		"/azure/gpt-4o",
		"azure/gpt-4o",
	},
	"a target that keeps the request and adds fallbacks": {
		"openai/gpt-4o fallbacks=anthropic/claude-haiku@",
		"openai/gpt-4o fallbacks=anthropic/claude-haiku@",
		"openai/gpt-4o fallbacks=anthropic/claude-haiku@",
		"openai/o3 fallbacks=anthropic/claude-haiku@",
		"openai/o3 fallbacks=anthropic/claude-haiku@",
		"openai/gpt-4o fallbacks=anthropic/claude-haiku@",
		"/gpt-4o",
		"openai/gpt-4o fallbacks=anthropic/claude-haiku@",
	},
	"a rule with no target goes on to the next rule": {
		"openai/next",
		"openai/next",
		"openai/next",
		"openai/next",
		"openai/next",
		"openai/next",
		"/next",
		"openai/next",
	},
	"odd weights with one target that can be picked": {
		"openai/always",
		"openai/always",
		"openai/always",
		"openai/always",
		"openai/always",
		"openai/always",
		"/always",
		"openai/always",
	},
	"ids with spaces, slashes and other scripts": {
		"openai/uuid",
		"openai/uuid",
		"openai/uuid",
		"openai/accented",
		"openai/accented",
		"openai/spaced",
		"/uuid",
		"openai/uuid",
	},
	"a chained rule, then a rule on what it chose": {
		"azure/after-azure",
		"azure/after-azure",
		"azure/after-azure",
		"openai/o3",
		"openai/o3",
		"azure/after-azure",
		"/azure/after-azure",
		"azure/after-azure",
	},
	"a chained rule nothing follows": {
		"azure/gpt-4o key=azure-key fallbacks=anthropic/claude-sonnet@",
		"azure/gpt-4o key=azure-key fallbacks=anthropic/claude-sonnet@",
		"azure/gpt-4o key=azure-key fallbacks=anthropic/claude-sonnet@",
		"openai/o3",
		"openai/o3",
		"azure/gpt-4o key=azure-key first-token=1.5s fallbacks=anthropic/claude-sonnet@",
		"/azure/gpt-4o key=azure-key",
		"azure/gpt-4o key=azure-key fallbacks=anthropic/claude-sonnet@",
	},
	"a chained rule that matches what it chose fires once": {
		"openai/after-loop",
		"openai/after-loop",
		"openai/after-loop",
		"openai/after-loop",
		"openai/after-loop",
		"openai/after-loop",
		"/after-loop",
		"openai/after-loop",
	},
	"two chained rules that feed each other": {
		"openai/pong",
		"openai/pong",
		"openai/pong",
		"openai/o3",
		"openai/o3",
		"openai/pong",
		"/pong",
		"openai/pong",
	},
	"a chain longer than it may run": {
		"openai/step-10",
		"openai/step-10",
		"openai/step-10",
		"openai/o3",
		"openai/o3",
		"openai/step-10",
		"/step-10",
		"openai/step-10",
	},
	"chains across scopes": {
		"openai/from-global-again",
		"openai/from-global-again",
		"openai/from-global-again",
		"openai/o3",
		"openai/o3",
		"openai/from-team",
		"/from-global-again",
		"openai/from-global-again",
	},
	"one rule on one tier": {
		"openai/cheap tier=SIMPLE",
		"openai/gpt-4o tier=MEDIUM",
		"openai/gpt-4o tier=COMPLEX",
		"openai/cheap tier=SIMPLE",
		"openai/o3 tier=COMPLEX",
		"openai/gpt-4o tier=MEDIUM",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"a rule for each tier": {
		"openai/tier-simple tier=SIMPLE",
		"openai/tier-medium tier=MEDIUM",
		"openai/tier-complex tier=COMPLEX",
		"openai/tier-simple tier=SIMPLE",
		"openai/tier-complex tier=COMPLEX",
		"openai/tier-medium tier=MEDIUM",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"a rule for each tier, then a rule for the rest": {
		"openai/tier-simple tier=SIMPLE",
		"openai/tier-medium tier=MEDIUM",
		"openai/tier-complex tier=COMPLEX",
		"openai/tier-simple tier=SIMPLE",
		"openai/tier-complex tier=COMPLEX",
		"openai/tier-medium tier=MEDIUM",
		"/rest",
		"openai/rest",
	},
	"a rule for each tier after a rule that takes some requests first": {
		"openai/tier-simple tier=SIMPLE",
		"openai/tier-medium tier=MEDIUM",
		"openai/tier-complex tier=COMPLEX",
		"openai/tier-simple tier=SIMPLE",
		"openai/tier-complex tier=COMPLEX",
		"openai/first",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"tier rules with other conditions beside the tier": {
		"openai/gpt-simple tier=SIMPLE",
		"openai/gpt-rest tier=MEDIUM",
		"openai/gpt-rest tier=COMPLEX",
		"openai/rest tier=SIMPLE",
		"openai/o3-complex tier=COMPLEX",
		"openai/gpt-rest tier=MEDIUM",
		"/rest",
		"openai/rest",
	},
	"two rules on the same tier": {
		"openai/second tier=SIMPLE",
		"openai/third tier=MEDIUM",
		"openai/gpt-4o tier=COMPLEX",
		"openai/first tier=SIMPLE",
		"openai/o3 tier=COMPLEX",
		"openai/third tier=MEDIUM",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"tier rules in every scope": {
		"openai/global-simple tier=SIMPLE",
		"openai/global-medium tier=MEDIUM",
		"openai/gpt-4o tier=COMPLEX",
		"openai/global-simple tier=SIMPLE",
		"openai/key-complex tier=COMPLEX",
		"openai/global-medium tier=MEDIUM",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"a tier rule that is off between two that are on": {
		"openai/simple tier=SIMPLE",
		"openai/rest tier=MEDIUM",
		"openai/complex tier=COMPLEX",
		"openai/simple tier=SIMPLE",
		"openai/complex tier=COMPLEX",
		"openai/rest tier=MEDIUM",
		"/rest",
		"openai/rest",
	},
	"a tier under an or": {
		"openai/rest tier=SIMPLE",
		"openai/rest tier=MEDIUM",
		"openai/big tier=COMPLEX",
		"openai/rest tier=SIMPLE",
		"openai/big",
		"openai/rest tier=MEDIUM",
		"/rest",
		"openai/rest",
	},
	"a tier under a not": {
		"openai/not",
		"openai/not",
		"openai/not",
		"openai/rest tier=SIMPLE",
		"openai/not tier=COMPLEX",
		"openai/not",
		"/not",
		"openai/not",
	},
	"a tier that picks the condition": {
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/either",
		"openai/o3",
		"openai/either",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"a tier compared with a header": {
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"/rest",
		"openai/rest",
	},
	"a tier that is always there once the request is classified": {
		"openai/known tier=SIMPLE",
		"openai/known tier=MEDIUM",
		"openai/known tier=COMPLEX",
		"openai/known tier=SIMPLE",
		"openai/known tier=COMPLEX",
		"openai/known tier=MEDIUM",
		"/rest",
		"openai/rest",
	},
	"a tier inside a loop": {
		"openai/loop tier=SIMPLE",
		"openai/loop tier=MEDIUM",
		"openai/rest tier=COMPLEX",
		"openai/rest",
		"openai/rest",
		"openai/loop tier=MEDIUM",
		"/rest",
		"openai/rest",
	},
	"a chained tier rule, then rules on what it chose": {
		"openai/small tier=SIMPLE",
		"openai/gpt-4o tier=MEDIUM",
		"openai/big-on-azure tier=COMPLEX",
		"openai/small tier=SIMPLE",
		"openai/big-on-azure tier=COMPLEX",
		"openai/gpt-4o tier=MEDIUM",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"a chained rule with a tier under an or": {
		"openai/gpt-4o tier=SIMPLE",
		"openai/gpt-4o tier=MEDIUM",
		"azure/big-model tier=COMPLEX",
		"openai/o3 tier=SIMPLE",
		"azure/big-model",
		"openai/gpt-4o tier=MEDIUM",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"a tier beside a header that is missing": {
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"/rest",
		"openai/rest",
	},
	"a tier in the wrong letter case": {
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/rest",
		"openai/rest",
		"openai/gpt-4o",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"a tier under an or with no target": {
		"openai/gpt-4o",
		"openai/gpt-4o",
		"openai/gpt-complex",
		"openai/rest",
		"openai/rest",
		"openai/gpt-4o",
		"/gpt-4o",
		"openai/gpt-4o",
	},
	"a condition no tier satisfies": {
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"openai/rest",
		"/rest",
		"openai/rest",
	},
}

// migratedRouterGrids is a fingerprint of everything the routing hook did to each request of
// migrationRequests, for every rule set of TestRoutersMadeFromRoutingRulesRouteAsTheRulesDid. It was
// taken while each request also had to come out of the routing rules the same.
var migratedRouterGrids = map[string]string{
	"a rule with no condition":                                         "7a789afc502acebc",
	"a condition on the model":                                         "d154c6d9ad564260",
	"a condition on a header":                                          "9055ed5def0001bd",
	"a header that may be missing":                                     "c694ac8b2b9e7dda",
	"nested and, or and not":                                           "6bda3bffb5b6498e",
	"conditions on who is calling":                                     "f89d54e817affaa1",
	"a condition on the budget":                                        "1b7d258596824904",
	"a condition on the request type":                                  "e97347ba9a4f4a0c",
	"blank space and a condition that does not compile":                "27bd7ad3a0859ebf",
	"the first rule that matches wins":                                 "0d95f40e6a4080fa",
	"negative priorities":                                              "7b5c24c0d3c901cc",
	"rules with one priority":                                          "24b8ab3b4d27920d",
	"a rule that is off":                                               "d9a57915441dc478",
	"narrower scopes come first":                                       "53b5d655a42b2b61",
	"a scope the request is not in":                                    "810e1b6af75e3ffc",
	"the same name and priority in two scopes":                         "e5d63169b4c18f68",
	"a target with a provider, a key and a deadline":                   "9f3960fdf65e3a54",
	"a target that changes only the provider":                          "d95376fc0e220802",
	"a target that keeps the request and adds fallbacks":               "0aece93a9098f833",
	"a rule with no target goes on to the next rule":                   "bae47c84be1c12ea",
	"odd weights with one target that can be picked":                   "51294b007e051540",
	"ids with spaces, slashes and other scripts":                       "8de1d0516669971c",
	"a chained rule, then a rule on what it chose":                     "9bd9c364ab1d6869",
	"a chained rule nothing follows":                                   "26d0877aa1c39a4a",
	"a chained rule that matches what it chose fires once":             "76bb973762047a71",
	"two chained rules that feed each other":                           "4dc23fcd707866cc",
	"a chain longer than it may run":                                   "3b844e8a34135114",
	"chains across scopes":                                             "86c3414f848f105f",
	"one rule on one tier":                                             "f96b9bca133fca2b",
	"a rule for each tier":                                             "5f72f0c62c59c82d",
	"a rule for each tier, then a rule for the rest":                   "791891089f5155c9",
	"a rule for each tier after a rule that takes some requests first": "131c4b5f9132a97a",
	"tier rules with other conditions beside the tier":                 "0692dfc160f52b51",
	"two rules on the same tier":                                       "1d2522389938fd1f",
	"tier rules in every scope":                                        "0ee0f9084a7246e0",
	"a tier rule that is off between two that are on":                  "ac821cc31566c6d3",
	"a tier under an or":                                               "c3d9ab4c16e545e7",
	"a tier under a not":                                               "f80ecf78d7273be8",
	"a tier that picks the condition":                                  "0a12c0c0be33aa73",
	"a tier compared with a header":                                    "e7c732057d53cfe7",
	"a tier that is always there once the request is classified":       "fcf175f6e5fa9027",
	"a tier inside a loop":                                             "c08edc0bb1be813c",
	"a chained tier rule, then rules on what it chose":                 "74109afb25be48e3",
	"a chained rule with a tier under an or":                           "334054f8c0c5c3b7",
	"a tier beside a header that is missing":                           "4aa7830c9c968fa2",
	"a tier in the wrong letter case":                                  "83dfe5cc1881e006",
	"a tier under an or with no target":                                "9b17459474582850",
	"a condition no tier satisfies":                                    "91a468e9d20c844d",
}

// TestRoutersMadeFromRoutingRulesRouteAsTheRulesDid holds the routers made from each rule set to
// what the routing rules answered: provider, model, fallbacks, key pin, first-token deadline and
// tier for the probes, written out in migratedRouterAnswers, and everything the routing hook does
// to each request of the full grid, as one fingerprint per rule set in migratedRouterGrids.
func TestRoutersMadeFromRoutingRulesRouteAsTheRulesDid(t *testing.T) {
	pinned := ruleTarget("azure", "gpt-4o")
	pinned.KeyID = schemas.Ptr("azure-key")
	pinned.TTFTTimeoutMs = schemas.Ptr(1500)
	ladder := func() []configstoreTables.TableRoutingRule {
		return []configstoreTables.TableRoutingRule{
			migratingRule("simple", `complexity_tier == "SIMPLE"`, 0, "tier-simple"),
			migratingRule("medium", `complexity_tier == "MEDIUM"`, 1, "tier-medium"),
			migratingRule("complex", `complexity_tier == "COMPLEX"`, 2, "tier-complex"),
		}
	}
	// Twelve rules that each chain to the next: more than the chain may run.
	var long []configstoreTables.TableRoutingRule
	for i := range 12 {
		from := "gpt-4o"
		if i > 0 {
			from = fmt.Sprintf("step-%d", i)
		}
		long = append(long, chaining(migratingRule(fmt.Sprintf("hop-%02d", i), fmt.Sprintf(`model == "%s"`, from), i, fmt.Sprintf("step-%d", i+1))))
	}

	cases := []struct {
		name           string
		rules          []configstoreTables.TableRoutingRule
		classifiesLess bool // its routers classify less than its rules did, so its answers are written without the tier
	}{
		// Conditions that do not read the tier.
		{name: "a rule with no condition", rules: []configstoreTables.TableRoutingRule{migratingRule("all", "", 0, "everything")}},
		{name: "a condition on the model", rules: []configstoreTables.TableRoutingRule{migratingRule("gpt", `model == "gpt-4o"`, 0, "gpt-4o-mini")}},
		{name: "a condition on a header", rules: []configstoreTables.TableRoutingRule{migratingRule("support", `headers["X-App"] == "support"`, 0, "support-model")}},
		{name: "a header that may be missing", rules: []configstoreTables.TableRoutingRule{
			migratingRule("has", `"x-app" in headers && headers["x-app"].startsWith("sup")`, 0, "has-app"),
			migratingRule("lacks", `!("x-app" in headers)`, 1, "no-app"),
		}},
		{name: "nested and, or and not", rules: []configstoreTables.TableRoutingRule{
			migratingRule("nested", `(model == "o3" || headers["x-plan"] == "pro") && !(request_type == "chat_completion_stream") && model in ["o3", "gpt-4o"]`, 0, "nested"),
		}},
		{name: "conditions on who is calling", rules: []configstoreTables.TableRoutingRule{
			migratingRule("team", `team_id == "team-1" && virtual_key_id == "vk-1"`, 0, "for-team"),
			migratingRule("user", `user_id == "user-1" || customer_id == "customer-9"`, 1, "for-user"),
			migratingRule("nobody", `virtual_key_id == ""`, 2, "for-nobody"),
		}},
		{name: "a condition on the budget", rules: []configstoreTables.TableRoutingRule{migratingRule("budget", `budget_used > 90.0`, 0, "cheap")}},
		{name: "a condition on the request type", rules: []configstoreTables.TableRoutingRule{migratingRule("streams", `request_type == "chat_completion_stream"`, 0, "for-streams")}},
		{name: "blank space and a condition that does not compile", rules: []configstoreTables.TableRoutingRule{
			migratingRule("blank", "  ", 0, "never-blank"),
			migratingRule("broken", `model == `, 1, "never-broken"),
			migratingRule("works", `model == "gpt-4o"`, 2, "works"),
		}},

		// Order.
		{name: "the first rule that matches wins", rules: []configstoreTables.TableRoutingRule{
			migratingRule("second", `model == "gpt-4o"`, 20, "second"),
			migratingRule("first", `headers["x-app"] == "support"`, 10, "first"),
			migratingRule("third", "", 4000, "third"),
		}},
		{name: "negative priorities", rules: []configstoreTables.TableRoutingRule{
			migratingRule("zero", `model == "gpt-4o"`, 0, "zero"),
			migratingRule("minus", `headers["x-app"] == "support"`, -7, "minus"),
			migratingRule("plus", "", 3, "plus"),
		}},
		{name: "rules with one priority", rules: func() []configstoreTables.TableRoutingRule {
			older := migratingRule("a-older", "", 5, "older")
			newer := migratingRule("z-newer", `model == "gpt-4o"`, 5, "newer")
			newer.CreatedAt = newer.CreatedAt.Add(time.Hour)
			twin := migratingRule("b-twin", `model == "o3"`, 5, "twin")
			return []configstoreTables.TableRoutingRule{older, newer, twin}
		}()},
		{name: "a rule that is off", rules: []configstoreTables.TableRoutingRule{
			turnedOff(migratingRule("off", `model == "gpt-4o"`, 0, "off")),
			migratingRule("on", "", 1, "on"),
		}},

		// Scopes.
		{name: "narrower scopes come first", rules: []configstoreTables.TableRoutingRule{
			migratingRule("global", "", 0, "global"),
			inScope(migratingRule("customer", `model == "gpt-4o"`, 0, "customer"), "customer", "customer-1"),
			inScope(migratingRule("team", `headers["x-app"] == "support"`, 0, "team"), "team", "team-1"),
			inScope(migratingRule("user", `model == "o3" && headers["x-plan"] == "pro"`, 0, "user"), "user", "user-1"),
			inScope(migratingRule("key", `request_type == "chat_completion_stream"`, 0, "key"), "virtual_key", "vk-1"),
		}},
		{name: "a scope the request is not in", rules: []configstoreTables.TableRoutingRule{
			inScope(migratingRule("other-team", "", 0, "other-team"), "team", "team-2"),
			inScope(migratingRule("other-key", "", 0, "other-key"), "virtual_key", "vk-2"),
			migratingRule("global", `model == "o3"`, 0, "global"),
		}},
		{name: "the same name and priority in two scopes", rules: func() []configstoreTables.TableRoutingRule {
			team := inScope(migratingRule("team-copy", `model == "gpt-4o"`, 0, "team-copy"), "team", "team-1")
			global := migratingRule("global-copy", `model == "gpt-4o"`, 0, "global-copy")
			team.Name, global.Name = "Same name", "Same name"
			return []configstoreTables.TableRoutingRule{team, global}
		}()},

		// Targets and fallbacks.
		{name: "a target with a provider, a key and a deadline", rules: []configstoreTables.TableRoutingRule{
			withFallbacks(t, withTargets(migratingRule("pinned", `model == "gpt-4o"`, 0, ""), pinned), `["anthropic/claude-sonnet", "openai/", {"provider": "openai", "model": "gpt-4o-mini", "key_id": "openai-key"}]`),
		}},
		{name: "a target that changes only the provider", rules: []configstoreTables.TableRoutingRule{
			withTargets(migratingRule("azure", `model == "gpt-4o"`, 0, ""), ruleTarget("azure", "")),
		}},
		{name: "a target that keeps the request and adds fallbacks", rules: []configstoreTables.TableRoutingRule{
			withFallbacks(t, withTargets(migratingRule("keep", "", 0, ""), ruleTarget("", "")), `["anthropic/claude-haiku"]`),
		}},
		{name: "a rule with no target goes on to the next rule", rules: []configstoreTables.TableRoutingRule{
			withTargets(migratingRule("empty", `model == "gpt-4o"`, 0, "")),
			withTargets(migratingRule("negative", `model == "gpt-4o"`, 1, ""), configstoreTables.TableRoutingTarget{Model: schemas.Ptr("never"), Weight: -1}),
			migratingRule("next", "", 2, "next"),
		}},
		{name: "odd weights with one target that can be picked", rules: []configstoreTables.TableRoutingRule{
			withTargets(migratingRule("odd", "", 0, ""),
				configstoreTables.TableRoutingTarget{Model: schemas.Ptr("never-zero"), Weight: 0},
				configstoreTables.TableRoutingTarget{Model: schemas.Ptr("always"), Weight: 7},
				configstoreTables.TableRoutingTarget{Model: schemas.Ptr("never-negative"), Weight: -3}),
		}},
		{name: "ids with spaces, slashes and other scripts", rules: []configstoreTables.TableRoutingRule{
			migratingRule("support rule/1", `headers["x-app"] == "support"`, 0, "spaced"),
			migratingRule("règle-été", `model == "o3"`, 1, "accented"),
			migratingRule("3f0c7a52-6a1e-4c59-9b54-1c2d3e4f5a6b", "", 2, "uuid"),
		}},

		// Chains.
		{name: "a chained rule, then a rule on what it chose", rules: []configstoreTables.TableRoutingRule{
			withFallbacks(t, chaining(withTargets(migratingRule("first", `model == "gpt-4o"`, 0, ""), pinned)), `["anthropic/claude-sonnet"]`),
			migratingRule("second", `provider == "azure"`, 1, "after-azure"),
		}},
		{name: "a chained rule nothing follows", rules: []configstoreTables.TableRoutingRule{
			withFallbacks(t, chaining(withTargets(migratingRule("only", `model == "gpt-4o"`, 0, ""), pinned)), `["anthropic/claude-sonnet"]`),
		}},
		{name: "a chained rule that matches what it chose fires once", rules: []configstoreTables.TableRoutingRule{
			chaining(migratingRule("loop", "", 0, "looped")),
			migratingRule("then", `model == "looped"`, 1, "after-loop"),
		}},
		{name: "two chained rules that feed each other", rules: []configstoreTables.TableRoutingRule{
			chaining(migratingRule("a", `model == "gpt-4o" || model == "pong"`, 0, "ping")),
			chaining(migratingRule("b", `model == "ping"`, 1, "pong")),
		}},
		{name: "a chain longer than it may run", rules: long},
		{name: "chains across scopes", rules: []configstoreTables.TableRoutingRule{
			chaining(migratingRule("global-first", `model == "gpt-4o"`, 0, "from-global")),
			inScope(migratingRule("team-then", `model == "from-global"`, 0, "from-team"), "team", "team-1"),
			migratingRule("global-then", `model == "from-global"`, 1, "from-global-again"),
		}},

		// The tier.
		{name: "one rule on one tier", rules: []configstoreTables.TableRoutingRule{migratingRule("simple", `complexity_tier == "SIMPLE"`, 0, "cheap")}},
		{name: "a rule for each tier", rules: ladder()},
		{name: "a rule for each tier, then a rule for the rest", rules: append(ladder(), migratingRule("rest", "", 9, "rest"))},
		{name: "a rule for each tier after a rule that takes some requests first", rules: append(ladder(), migratingRule("first", `headers["x-app"] == "support"`, -1, "first"))},
		{name: "tier rules with other conditions beside the tier", rules: []configstoreTables.TableRoutingRule{
			migratingRule("gpt-simple", `model == "gpt-4o" && complexity_tier == "SIMPLE"`, 0, "gpt-simple"),
			migratingRule("gpt-rest", `complexity_tier != "SIMPLE" && model == "gpt-4o"`, 1, "gpt-rest"),
			migratingRule("o3-complex", `model == "o3" && complexity_tier in ["COMPLEX"]`, 2, "o3-complex"),
			migratingRule("pro-medium", `"x-plan" in headers && complexity_tier == "MEDIUM"`, 3, "pro-medium"),
			migratingRule("rest", "", 4, "rest"),
		}},
		{name: "two rules on the same tier", rules: []configstoreTables.TableRoutingRule{
			migratingRule("first", `model == "o3" && complexity_tier == "SIMPLE"`, 0, "first"),
			migratingRule("second", `complexity_tier == "SIMPLE"`, 1, "second"),
			migratingRule("third", `complexity_tier in ["SIMPLE", "MEDIUM"]`, 2, "third"),
		}},
		{name: "tier rules in every scope", rules: []configstoreTables.TableRoutingRule{
			inScope(migratingRule("key-complex", `complexity_tier == "COMPLEX"`, 0, "key-complex"), "virtual_key", "vk-1"),
			inScope(migratingRule("team-simple", `complexity_tier == "SIMPLE"`, 0, "team-simple"), "team", "team-1"),
			inScope(migratingRule("other-team", `complexity_tier == "SIMPLE"`, 0, "other-team"), "team", "team-2"),
			migratingRule("global-medium", `complexity_tier == "MEDIUM"`, 0, "global-medium"),
			migratingRule("global-simple", `complexity_tier == "SIMPLE"`, 1, "global-simple"),
		}},
		{name: "a tier rule that is off between two that are on", rules: []configstoreTables.TableRoutingRule{
			migratingRule("simple", `complexity_tier == "SIMPLE"`, 0, "simple"),
			turnedOff(migratingRule("medium", `complexity_tier == "MEDIUM"`, 1, "medium")),
			migratingRule("complex", `complexity_tier == "COMPLEX"`, 2, "complex"),
			migratingRule("rest", "", 3, "rest"),
		}},
		{name: "a tier under an or", rules: []configstoreTables.TableRoutingRule{
			migratingRule("pro", `headers["x-plan"] == "pro" || complexity_tier == "COMPLEX"`, 0, "big"),
			migratingRule("rest", "", 1, "rest"),
		}},
		{name: "a tier under a not", rules: []configstoreTables.TableRoutingRule{
			migratingRule("not", `!(model == "o3" && complexity_tier == "SIMPLE")`, 0, "not"),
			migratingRule("rest", "", 1, "rest"),
		}},
		{name: "a tier that picks the condition", rules: []configstoreTables.TableRoutingRule{
			migratingRule("either", `complexity_tier == "SIMPLE" ? model == "o3" : headers["x-app"] == "support"`, 0, "either"),
		}, classifiesLess: true},
		{name: "a tier compared with a header", rules: []configstoreTables.TableRoutingRule{
			migratingRule("asked", `complexity_tier == headers["x-tier"]`, 0, "asked"),
			migratingRule("rest", "", 1, "rest"),
		}, classifiesLess: true},
		{name: "a tier that is always there once the request is classified", rules: []configstoreTables.TableRoutingRule{
			migratingRule("known", `complexity_tier != ""`, 0, "known"),
			migratingRule("rest", "", 1, "rest"),
		}},
		{name: "a tier inside a loop", rules: []configstoreTables.TableRoutingRule{
			migratingRule("loop", `["SIMPLE", "MEDIUM"].exists(t, t == complexity_tier) && model == "gpt-4o"`, 0, "loop"),
			migratingRule("rest", "", 1, "rest"),
		}},
		{name: "a chained tier rule, then rules on what it chose", rules: []configstoreTables.TableRoutingRule{
			chaining(migratingRule("complex", `complexity_tier == "COMPLEX"`, 0, "big")),
			migratingRule("simple", `complexity_tier == "SIMPLE"`, 1, "small"),
			migratingRule("azure", `model == "big" && complexity_tier == "COMPLEX"`, 2, "big-on-azure"),
		}},
		{name: "a chained rule with a tier under an or", rules: []configstoreTables.TableRoutingRule{
			chaining(migratingRule("pro", `headers["x-plan"] == "pro" || complexity_tier == "COMPLEX"`, 0, "big-model")),
			withTargets(migratingRule("azure", `model == "big-model"`, 1, ""), ruleTarget("azure", "")),
		}},

		// Rules whose routers may classify less: the rule classified, then did not match.
		{name: "a tier beside a header that is missing", rules: []configstoreTables.TableRoutingRule{
			migratingRule("support-simple", `headers["x-app"] == "support" && complexity_tier == "SIMPLE"`, 0, "support-simple"),
			migratingRule("rest", "", 1, "rest"),
		}, classifiesLess: true},
		{name: "a tier in the wrong letter case", rules: []configstoreTables.TableRoutingRule{
			migratingRule("lower", `complexity_tier == "simple"`, 0, "never"),
			migratingRule("rest", `model == "o3"`, 1, "rest"),
		}, classifiesLess: true},
		{name: "a tier under an or with no target", rules: []configstoreTables.TableRoutingRule{
			withTargets(migratingRule("idle", `model == "o3" || complexity_tier == "COMPLEX"`, 0, "")),
			withTargets(chaining(migratingRule("idle-chained", `headers["x-plan"] == "pro" || complexity_tier == "SIMPLE"`, 1, ""))),
			migratingRule("gpt-complex", `model == "gpt-4o" && complexity_tier == "COMPLEX"`, 2, "gpt-complex"),
			migratingRule("rest", `model == "o3"`, 3, "rest"),
		}, classifiesLess: true},
		{name: "a condition no tier satisfies", rules: []configstoreTables.TableRoutingRule{
			migratingRule("never", `complexity_tier == "SIMPLE" && complexity_tier == "MEDIUM"`, 0, "never"),
			migratingRule("rest", "", 1, "rest"),
		}, classifiesLess: true},
	}
	probes := migrationProbes()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := migrateRules(t, nil, decisionAnalyzerTestConfig(), tc.rules, nil)
			want, ok := migratedRouterAnswers[tc.name]
			require.True(t, ok, "no answers are written down for this rule set")
			require.Len(t, want, len(probes))
			for i, probe := range probes {
				got, _ := m.route(t, probe)
				if tc.classifiesLess {
					got.Tier = ""
				}
				assert.Equal(t, want[i], got.String(), "%s", probe)
			}
			grid := sha256.New()
			for _, request := range migrationRequests() {
				got, _ := m.route(t, request)
				fmt.Fprintf(grid, "%+v\n", got)
			}
			assert.Equal(t, migratedRouterGrids[tc.name], fmt.Sprintf("%x", grid.Sum(nil)[:8]), "a request of the grid is routed differently than it was")
		})
	}
}

// TestRoutersMadeFromTierRulesRouteEachTier pins the answers of the setup most gateways with tier
// rules had, a rule per tier and a rule for everything else. The three tier rules are one router,
// and a request is classified once.
func TestRoutersMadeFromTierRulesRouteEachTier(t *testing.T) {
	rules := []configstoreTables.TableRoutingRule{
		migratingRule("support", `headers["x-app"] == "support"`, 0, "support-model"),
		migratingRule("simple", `complexity_tier == "SIMPLE"`, 1, "gpt-4o-mini"),
		migratingRule("medium", `complexity_tier == "MEDIUM"`, 2, "gpt-4o"),
		withFallbacks(t, withTargets(migratingRule("complex", `complexity_tier == "COMPLEX"`, 3, ""), ruleTarget("anthropic", "claude-opus")), `["openai/gpt-4o"]`),
		migratingRule("rest", "", 4, "fallback-model"),
	}
	m := migrateRules(t, nil, decisionAnalyzerTestConfig(), rules, nil)
	require.Len(t, m.migration.Routers, 3, "the three tier rules are one classifier router between two conditional routers")

	cases := []struct {
		request migrationRequest
		want    routedOutcome
		rule    string // the rule that took such a request
	}{
		{migrationRequest{text: simpleText}, routedOutcome{Provider: "openai", Model: "gpt-4o-mini", Tier: complexity.TierSimple, Mechanism: complexity.MechanismDecision, Classified: 1}, "simple"},
		{migrationRequest{text: mediumText}, routedOutcome{Provider: "openai", Model: "gpt-4o", Tier: complexity.TierMedium, Mechanism: complexity.MechanismDecision, Classified: 1}, "medium"},
		{migrationRequest{text: complexText}, routedOutcome{Provider: "anthropic", Model: "claude-opus", Fallbacks: []string{"openai/gpt-4o@"}, Tier: complexity.TierComplex, Mechanism: complexity.MechanismDecision, Classified: 1}, "complex"},
		{migrationRequest{text: complexText, headers: map[string]string{"x-app": "support"}}, routedOutcome{Provider: "openai", Model: "support-model"}, "support"},
		{migrationRequest{text: complexText, down: true}, routedOutcome{Provider: "openai", Model: "fallback-model", Mechanism: complexity.MechanismSkipped, Classified: 1}, "rest"},
		{migrationRequest{text: ""}, routedOutcome{Provider: "openai", Model: "fallback-model", Mechanism: complexity.MechanismSkipped}, "rest"},
	}
	for _, tc := range cases {
		t.Run(tc.request.String(), func(t *testing.T) {
			got, _ := m.route(t, tc.request)
			assert.Contains(t, m.rules[got.TookBy], tc.rule, "the router that takes it is the one made from that rule")
			// Rules that share a router show under the first one's id.
			tc.want.TookBy = got.TookBy
			tc.want.Allowlist, tc.want.LoadBalanced = []string{tc.want.Model}, []string{tc.want.Model}
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestRoutersMadeFromRoutingRulesKeepTargetWeights proves a rule's weighted targets are picked
// from as before. The pick is random, so the same request is routed many times: it must reach the
// targets the rule could pick, each about as often as its weight said, whatever the weights
// summed to.
func TestRoutersMadeFromRoutingRulesKeepTargetWeights(t *testing.T) {
	cases := []struct {
		name    string
		targets []configstoreTables.TableRoutingTarget
		shares  map[string]float64 // model -> the share of requests it gets
	}{
		{"weights that sum to 1", []configstoreTables.TableRoutingTarget{
			{Model: schemas.Ptr("large"), Weight: 0.75}, {Model: schemas.Ptr("small"), Weight: 0.25},
		}, map[string]float64{"large": 0.75, "small": 0.25}},
		{"weights that do not", []configstoreTables.TableRoutingTarget{
			{Model: schemas.Ptr("large"), Weight: 6}, {Model: schemas.Ptr("small"), Weight: 2},
		}, map[string]float64{"large": 0.75, "small": 0.25}},
		{"weights that are all 0", []configstoreTables.TableRoutingTarget{
			{Model: schemas.Ptr("one"), Weight: 0}, {Model: schemas.Ptr("other"), Weight: 0},
		}, map[string]float64{"one": 0.5, "other": 0.5}},
		{"a weight of 0 beside others", []configstoreTables.TableRoutingTarget{
			{Model: schemas.Ptr("never"), Weight: 0}, {Model: schemas.Ptr("half"), Weight: 1}, {Model: schemas.Ptr("other-half"), Weight: 1},
		}, map[string]float64{"half": 0.5, "other-half": 0.5}},
	}
	const draws = 800
	for _, tc := range cases {
		for _, tier := range []bool{false, true} {
			condition, name := "", tc.name
			if tier {
				condition, name = `complexity_tier == "SIMPLE"`, tc.name+", on a tier"
			}
			t.Run(name, func(t *testing.T) {
				m := migrateRules(t, nil, decisionAnalyzerTestConfig(), []configstoreTables.TableRoutingRule{withTargets(migratingRule("weighted", condition, 0, ""), tc.targets...)}, nil)
				counts := map[string]int{}
				for range draws {
					out, _ := m.route(t, migrationRequest{text: simpleText})
					counts[out.Model]++
				}
				require.Len(t, counts, len(tc.shares), "reached %v", counts)
				for model, share := range tc.shares {
					assert.InDelta(t, share, float64(counts[model])/draws, 0.1, "share of %s", model)
				}
			})
		}
	}
}

// TestRoutersMadeFromRoutingRulesClassifyLess pins the first difference from routing rules. A rule
// classified a request whenever its condition was undecided without the tier, which includes a
// condition that fails to evaluate, such as one that reads a header the request does not have. A
// router classifies only once its own condition holds. The request is routed the same; it is not
// classified, so it carries no tier and costs no classifier call.
func TestRoutersMadeFromRoutingRulesClassifyLess(t *testing.T) {
	rules := []configstoreTables.TableRoutingRule{
		migratingRule("support-simple", `headers["x-app"] == "support" && complexity_tier == "SIMPLE"`, 0, "support-simple"),
		migratingRule("rest", "", 1, "rest"),
	}
	m := migrateRules(t, nil, decisionAnalyzerTestConfig(), rules, nil)

	// The header is missing. The rule could not match on any tier, and classified to find out.
	got, _ := m.route(t, migrationRequest{text: complexText})
	assert.Zero(t, got.Classified)
	assert.Empty(t, got.Tier)
	assert.Empty(t, got.Mechanism)
	assert.Equal(t, "rest", got.Model)

	// The header is there: the request is classified, once.
	got, _ = m.route(t, migrationRequest{text: simpleText, headers: map[string]string{"x-app": "support"}})
	assert.Equal(t, 1, got.Classified)
	assert.Equal(t, "support-simple", got.Model)
}

// TestRoutersMadeFromRoutingRulesAcrossSessionTurns covers session memory, which keeps a session
// on the highest tier it has reached. Tier rules that became one router, which is every set of
// tier rules with the same condition, remember exactly as the rules did. Tier rules with different
// conditions stay separate routers, and each remembers for itself: that is the second difference
// from routing rules, pinned in the last subtest.
func TestRoutersMadeFromRoutingRulesAcrossSessionTurns(t *testing.T) {
	settings := sessionRouterConfig()
	type turn struct {
		request migrationRequest
		want    string
	}
	play := func(t *testing.T, m *ruleMigration, turns []turn) {
		t.Helper()
		for i, turn := range turns {
			got, _ := m.route(t, turn.request)
			assert.Equal(t, turn.want, got.Model, "turn %d: %s", i, turn.request)
		}
	}

	t.Run("a rule for each tier remembers as before", func(t *testing.T) {
		m := migrateRules(t, newTestKVStore(t), &settings, []configstoreTables.TableRoutingRule{
			migratingRule("simple", `complexity_tier == "SIMPLE"`, 0, "tier-simple"),
			migratingRule("medium", `complexity_tier == "MEDIUM"`, 1, "tier-medium"),
			migratingRule("complex", `complexity_tier == "COMPLEX"`, 2, "tier-complex"),
		}, nil)
		require.Len(t, m.migration.Routers, 1)
		play(t, m, []turn{
			{migrationRequest{text: simpleText, session: "s1"}, "tier-simple"},
			{migrationRequest{text: mediumText, session: "s1"}, "tier-medium"},
			{migrationRequest{text: simpleText, session: "s1"}, "tier-medium"}, // the session stays on the highest tier it reached
			{migrationRequest{text: complexText, session: "s1"}, "tier-complex"},
			{migrationRequest{text: simpleText, session: "s1"}, "tier-complex"},
			{migrationRequest{text: mediumText, session: "s1"}, "tier-complex"},
			{migrationRequest{text: simpleText, session: "s2"}, "tier-simple"}, // another session starts over
			{migrationRequest{text: simpleText}, "tier-simple"},                // a request outside any session is classified on its own
		})
	})

	t.Run("tier rules with the same condition remember as before", func(t *testing.T) {
		m := migrateRules(t, newTestKVStore(t), &settings, []configstoreTables.TableRoutingRule{
			migratingRule("cheap", `model == "gpt-4o" && complexity_tier in ["SIMPLE", "MEDIUM"]`, 0, "cheap"),
			migratingRule("big", `model == "gpt-4o" && complexity_tier == "COMPLEX"`, 1, "big"),
			migratingRule("rest", "", 2, "rest"),
		}, nil)
		play(t, m, []turn{
			{migrationRequest{text: simpleText, session: "s1"}, "cheap"},
			{migrationRequest{text: complexText, session: "s1"}, "big"},
			{migrationRequest{text: simpleText, session: "s1"}, "big"},
			{migrationRequest{text: simpleText, session: "s1", model: "o3"}, "rest"},
			{migrationRequest{text: mediumText, session: "s1"}, "big"},
		})
	})

	t.Run("tier rules with different conditions each remember for themselves", func(t *testing.T) {
		m := migrateRules(t, newTestKVStore(t), &settings, []configstoreTables.TableRoutingRule{
			migratingRule("o3-simple", `model == "o3" && complexity_tier == "SIMPLE"`, 0, "o3-small"),
			migratingRule("complex", `complexity_tier == "COMPLEX"`, 1, "big"),
		}, nil)
		require.Len(t, m.migration.Routers, 2)
		play(t, m, []turn{
			// The session reaches COMPLEX on a request only the second rule's condition held for.
			{migrationRequest{text: complexText, session: "s1"}, "big"},
			// A simple request for o3 follows. The rules kept one memory, so it was still COMPLEX and
			// went to the big model. The first router never saw the COMPLEX turn: it classifies the
			// request on its own, as SIMPLE.
			{migrationRequest{text: simpleText, session: "s1", model: "o3"}, "o3-small"},
		})
	})
}

// TestRoutersMadeFromRoutingRulesEndTheWalkBeforeExistingRouters pins the third difference from
// routing rules, which only a gateway that had both rules and model routers can see. A rule routed
// the request and a router then routed it again. The rule is now a router in the same walk, so
// when it takes the request the walk ends there. A request no rule took still reaches the routers
// that were there.
func TestRoutersMadeFromRoutingRulesEndTheWalkBeforeExistingRouters(t *testing.T) {
	existing := conditionalRouter("existing", "Existing", tierTarget("", "azure", ""))
	existing.Routing = globalRouting(0, "")
	rules := []configstoreTables.TableRoutingRule{migratingRule("gpt", `model == "gpt-4o"`, 0, "gpt-4o-mini")}
	m := migrateRules(t, nil, decisionAnalyzerTestConfig(), rules, []configstore.ModelRouter{existing})
	require.Len(t, m.migration.Moved, 1, "the existing router moves behind the rule's router, whose priority it had")

	got, _ := m.route(t, migrationRequest{text: simpleText})
	assert.Equal(t, "openai/gpt-4o-mini", got.Provider+"/"+got.Model, "the rule's router took the request, so the existing router is not reached; under rules it then moved the request to azure")
	assert.Equal(t, "gpt", got.TookBy)

	got, _ = m.route(t, migrationRequest{text: simpleText, model: "o3"})
	assert.Equal(t, "azure/o3", got.Provider+"/"+got.Model)
	assert.Equal(t, "existing", got.TookBy)
}
