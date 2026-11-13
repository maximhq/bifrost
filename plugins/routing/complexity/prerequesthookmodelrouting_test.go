package complexity_test

import (
	"context"
	"errors"
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
	"github.com/maximhq/bifrost/framework/kvstore"
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

// pinnedStreamingRule is a routing rule that sets everything a rule can set:
// it moves gpt-4o to openai/gpt-4o-mini, pins a key, gives the request a
// fallback chain and, for streams, a first-token deadline.
func pinnedStreamingRule(t *testing.T) configstoreTables.TableRoutingRule {
	t.Helper()
	target := ruleTarget("openai", "gpt-4o-mini")
	target.KeyID = schemas.Ptr("openai-key")
	target.TTFTTimeoutMs = schemas.Ptr(2000)
	rule := ruleFor("to-mini", `model == "gpt-4o"`, target)
	rule.Fallbacks = schemas.Ptr(`["anthropic/claude-sonnet"]`)
	require.NoError(t, rule.AfterFind(nil))
	return rule
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

// newTestKVStore returns the in-memory KV store session tiers and tool
// continuations are kept in.
func newTestKVStore(t *testing.T) *kvstore.Store {
	t.Helper()
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
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
// Routing rules, then model routers: the two steps together
// ---------------------------------------------------------------------------

// TestModelRouterStepLeavesUntakenRequestsAlone pins the two cases where the
// model router step must change nothing: a gateway with no rule and no
// router, and a request a routing rule routed that no router takes. The
// second is the guard for every routing rule already in production: its
// model, key pin, fallbacks and first-token deadline all survive the step.
func TestModelRouterStepLeavesUntakenRequestsAlone(t *testing.T) {
	t.Run("no routing rule and no router", func(t *testing.T) {
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
	})

	t.Run("a routing rule's result survives routers that do not take the request", func(t *testing.T) {
		decision := modelRoutingDecision()
		withFallbacks := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
		withFallbacks.KeyID = schemas.Ptr("anthropic-key")
		withFallbacks.ParsedFallbacks = []configstoreTables.RoutingFallback{{Fallback: schemas.Fallback{Provider: schemas.Azure, Model: "gpt-4o"}}}
		elsewhere := decisionModelRouter("elsewhere", "Elsewhere", withFallbacks)
		elsewhere.Routing = globalRouting(0, `model == "some-other-model"`)
		plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision,
			[]configstoreTables.TableRoutingRule{pinnedStreamingRule(t)}, []configstore.ModelRouter{elsewhere})

		ctx := newRequestContext()
		req := streamRequest(complexText)
		require.NoError(t, plugin.PreRequestHook(ctx, req))

		provider, model, fallbacks := req.GetRequestFields()
		assert.Equal(t, schemas.OpenAI, provider)
		assert.Equal(t, "gpt-4o-mini", model)
		assert.Equal(t, []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-sonnet"}}, fallbacks)
		assert.Equal(t, "openai-key", ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID))
		assert.Equal(t, 2*time.Second, ctx.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout))
		assert.Zero(t, decision.calls(), "a router that does not take the request never classifies it")
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Equal(t, []string{schemas.RoutingEngineRoutingRule}, enginesUsed(ctx))
	})
}

// TestModelRouterMatchesOnWhatRoutingRulesChose pins what a router's routing
// is matched against: the request as the routing rules left it, never as the
// app sent it. An operator writing a router's condition reads the provider
// and model at the end of the rule chain.
func TestModelRouterMatchesOnWhatRoutingRulesChose(t *testing.T) {
	toMini := ruleFor("to-mini", `model == "gpt-4o"`, ruleTarget("", "gpt-4o-mini"))
	toAnthropic := ruleFor("to-anthropic", `model == "gpt-4o"`, ruleTarget("anthropic", "claude-sonnet"))
	chainFirst := ruleFor("chain-first", `model == "gpt-4o"`, ruleTarget("", "gpt-4o-mini"))
	chainFirst.ChainRule = true
	chainSecond := ruleFor("chain-second", `model == "gpt-4o-mini"`, ruleTarget("", "gpt-4.1"))
	chainSecond.Priority = 1
	neverMatches := ruleFor("never", `model == "no-such-model"`, ruleTarget("", "gpt-4o-mini"))
	failsToEvaluate := ruleFor("fails", `int(headers["x-n"]) > 3`, ruleTarget("", "gpt-4o-mini"))

	cases := []struct {
		name         string
		rules        []configstoreTables.TableRoutingRule
		condition    string
		headers      map[string]string
		wantRouter   bool
		wantProvider schemas.ModelProvider
		wantModel    string
		wantEngines  []string
	}{
		{
			name:  "the model a rule chose is the model the condition sees",
			rules: []configstoreTables.TableRoutingRule{toMini}, condition: `model == "gpt-4o-mini"`,
			wantRouter: true, wantProvider: schemas.OpenAI, wantModel: "router-model",
			wantEngines: []string{schemas.RoutingEngineRoutingRule, schemas.RoutingEngineModelRouter},
		},
		{
			name:  "the name the app sent no longer matches once a rule replaced it",
			rules: []configstoreTables.TableRoutingRule{toMini}, condition: `model == "gpt-4o"`,
			wantProvider: schemas.OpenAI, wantModel: "gpt-4o-mini",
			wantEngines: []string{schemas.RoutingEngineRoutingRule},
		},
		{
			name:  "the provider a rule chose is the provider the condition sees",
			rules: []configstoreTables.TableRoutingRule{toAnthropic}, condition: `provider == "anthropic"`,
			wantRouter: true, wantProvider: schemas.Anthropic, wantModel: "router-model",
			wantEngines: []string{schemas.RoutingEngineRoutingRule, schemas.RoutingEngineModelRouter},
		},
		{
			name:  "the end of a rule chain is what the condition sees",
			rules: []configstoreTables.TableRoutingRule{chainFirst, chainSecond}, condition: `model == "gpt-4.1"`,
			wantRouter: true, wantProvider: schemas.OpenAI, wantModel: "router-model",
			wantEngines: []string{schemas.RoutingEngineRoutingRule, schemas.RoutingEngineModelRouter},
		},
		{
			name:  "a router takes a request no rule matched",
			rules: []configstoreTables.TableRoutingRule{neverMatches}, condition: "",
			wantRouter: true, wantProvider: schemas.OpenAI, wantModel: "router-model",
			wantEngines: []string{schemas.RoutingEngineModelRouter},
		},
		{
			name:  "a rule that fails to evaluate does not stop the router step",
			rules: []configstoreTables.TableRoutingRule{failsToEvaluate}, condition: "", headers: map[string]string{"x-n": "not-a-number"},
			wantRouter: true, wantProvider: schemas.OpenAI, wantModel: "router-model",
			wantEngines: []string{schemas.RoutingEngineModelRouter},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := modelRoutingDecision()
			router := decisionModelRouter("general", "General", tierTarget(complexity.TierComplex, "", "router-model"))
			router.Routing = globalRouting(0, tc.condition)
			plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, tc.rules, []configstore.ModelRouter{router})

			ctx := newRequestContext()
			if tc.headers != nil {
				ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, tc.headers)
			}
			req := runPreRequest(t, plugin, ctx, complexText)

			provider, model, _ := req.GetRequestFields()
			assert.Equal(t, tc.wantProvider, provider)
			assert.Equal(t, tc.wantModel, model)
			assert.Equal(t, tc.wantEngines, enginesUsed(ctx))
			if tc.wantRouter {
				assert.Equal(t, "general", ctx.Value(schemas.BifrostContextKeyModelRouterID))
				assert.Equal(t, 1, decision.calls())
			} else {
				assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
				assert.Zero(t, decision.calls())
			}
		})
	}
}

// TestModelRouterTargetOverARoutingRule runs a router after a routing rule
// that set everything a rule can set (model, key pin, fallbacks, first-token
// deadline) and pins, for each kind of target, what the router replaces and
// what it leaves as the rule set it. A tier without a target and a request
// the router cannot classify keep the rule's result whole.
func TestModelRouterTargetOverARoutingRule(t *testing.T) {
	ruleFallbacks := []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-sonnet"}}
	withKey := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	withKey.KeyID = schemas.Ptr("anthropic-key")
	withFallbacks := tierTarget(complexity.TierComplex, "anthropic", "claude-opus")
	withFallbacks.ParsedFallbacks = []configstoreTables.RoutingFallback{
		{Fallback: schemas.Fallback{Provider: schemas.Azure}},
		{Fallback: schemas.Fallback{Provider: schemas.Vertex, Model: "gemini-pro"}},
	}

	cases := []struct {
		name           string
		targets        []configstoreTables.TableModelRouterTarget
		classifierDown bool
		wantProvider   schemas.ModelProvider
		wantModel      string
		wantPin        any
		wantDeadline   bool
		wantFallbacks  []schemas.Fallback
		wantTier       any
		wantLog        string
	}{
		{
			name:         "another model on the rule's provider keeps the key pin",
			targets:      []configstoreTables.TableModelRouterTarget{tierTarget(complexity.TierComplex, "", "gpt-4.1")},
			wantProvider: schemas.OpenAI, wantModel: "gpt-4.1", wantPin: "openai-key",
			wantFallbacks: ruleFallbacks, wantTier: complexity.TierComplex, wantLog: "tier=COMPLEX → provider=openai, model=gpt-4.1",
		},
		{
			name:         "another provider drops the rule's key pin",
			targets:      []configstoreTables.TableModelRouterTarget{tierTarget(complexity.TierComplex, "anthropic", "claude-opus")},
			wantProvider: schemas.Anthropic, wantModel: "claude-opus", wantPin: nil,
			wantFallbacks: ruleFallbacks, wantTier: complexity.TierComplex, wantLog: "tier=COMPLEX → provider=anthropic, model=claude-opus",
		},
		{
			name:         "a target's own key replaces the rule's",
			targets:      []configstoreTables.TableModelRouterTarget{withKey},
			wantProvider: schemas.Anthropic, wantModel: "claude-opus", wantPin: "anthropic-key",
			wantFallbacks: ruleFallbacks, wantTier: complexity.TierComplex, wantLog: "tier=COMPLEX → provider=anthropic, model=claude-opus",
		},
		{
			name:         "the rule's own provider and model keep its pin and deadline",
			targets:      []configstoreTables.TableModelRouterTarget{tierTarget(complexity.TierComplex, "openai", "gpt-4o-mini")},
			wantProvider: schemas.OpenAI, wantModel: "gpt-4o-mini", wantPin: "openai-key", wantDeadline: true,
			wantFallbacks: ruleFallbacks, wantTier: complexity.TierComplex, wantLog: "tier=COMPLEX → provider=openai, model=gpt-4o-mini",
		},
		{
			name:         "a target's fallbacks replace the rule's, and one without a model takes the rule's model",
			targets:      []configstoreTables.TableModelRouterTarget{withFallbacks},
			wantProvider: schemas.Anthropic, wantModel: "claude-opus", wantPin: nil,
			wantFallbacks: []schemas.Fallback{{Provider: schemas.Azure, Model: "gpt-4o-mini"}, {Provider: schemas.Vertex, Model: "gemini-pro"}},
			wantTier:      complexity.TierComplex, wantLog: "tier=COMPLEX → provider=anthropic, model=claude-opus",
		},
		{
			name:         "a tier without a model keeps everything the rule set",
			targets:      []configstoreTables.TableModelRouterTarget{tierTarget(complexity.TierSimple, "anthropic", "claude-haiku")},
			wantProvider: schemas.OpenAI, wantModel: "gpt-4o-mini", wantPin: "openai-key", wantDeadline: true,
			wantFallbacks: ruleFallbacks, wantTier: complexity.TierComplex, wantLog: "has no model for tier=COMPLEX; keeping provider=openai, model=gpt-4o-mini",
		},
		{
			name:           "a request the router cannot classify keeps everything the rule set",
			targets:        []configstoreTables.TableModelRouterTarget{tierTarget(complexity.TierComplex, "anthropic", "claude-opus")},
			classifierDown: true,
			wantProvider:   schemas.OpenAI, wantModel: "gpt-4o-mini", wantPin: "openai-key", wantDeadline: true,
			wantFallbacks: ruleFallbacks, wantTier: nil, wantLog: "produced no tier; keeping provider=openai, model=gpt-4o-mini",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := modelRoutingDecision()
			if tc.classifierDown {
				decision.err = &schemas.BifrostError{Error: &schemas.ErrorField{Message: "decision model unavailable"}}
			}
			router := decisionModelRouter("general", "General", tc.targets...)
			router.Routing = globalRouting(0, `model == "gpt-4o-mini"`)
			plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision,
				[]configstoreTables.TableRoutingRule{pinnedStreamingRule(t)}, []configstore.ModelRouter{router})

			ctx := newRequestContext()
			req := streamRequest(complexText)
			require.NoError(t, plugin.PreRequestHook(ctx, req))

			provider, model, fallbacks := req.GetRequestFields()
			assert.Equal(t, tc.wantProvider, provider)
			assert.Equal(t, tc.wantModel, model)
			assert.Equal(t, tc.wantFallbacks, fallbacks)
			assert.Equal(t, tc.wantPin, ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID))
			if tc.wantDeadline {
				assert.Equal(t, 2*time.Second, ctx.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout))
			} else {
				assert.Nil(t, ctx.Value(schemas.BifrostContextKeyStreamFirstTokenTimeout), "the deadline was set for the rule's target")
			}
			assert.Equal(t, tc.wantTier, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
			assert.Equal(t, "general", ctx.Value(schemas.BifrostContextKeyModelRouterID), "the router that took the request is recorded whatever it did")
			assert.Equal(t, []string{schemas.RoutingEngineRoutingRule, schemas.RoutingEngineModelRouter}, enginesUsed(ctx))
			assert.Contains(t, routingLogs(ctx), tc.wantLog)
		})
	}
}

// TestRoutingRuleTierAndRouterTierAreSeparate pins what a request records
// when a routing rule read complexity_tier and a router then classified the
// request differently. Each asks its own classifier once, the rule routes on
// the Complexity Router page's tier, the router routes on its own, and the
// tier, mechanism and router the request records are the router's, since it
// decided last.
func TestRoutingRuleTierAndRouterTierAreSeparate(t *testing.T) {
	decision := modelRoutingDecision()
	complexToMini := ruleFor("complex-to-mini", `complexity_tier == "COMPLEX"`, ruleTarget("", "gpt-4o-mini"))
	byLLM := configstore.ModelRouter{ID: "by-llm", Name: "By llm", ComplexityAnalyzerConfig: llmRouterConfig(), Targets: everyTierTargets("by-llm")}
	byLLM.Routing = globalRouting(0, `model == "gpt-4o-mini"`)
	plugin := modelRoutingTestPlugin(t, routing.NewMockGovernance(), decision, []configstoreTables.TableRoutingRule{complexToMini}, nil)
	var llmCalls atomic.Int32
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		llmCalls.Add(1)
		return llmClassifierChatResponse(`{"tier": "SIMPLE"}`), nil
	})
	require.NoError(t, plugin.ReloadModelRouter(&byLLM))

	ctx := newRequestContext()
	req := runPreRequest(t, plugin, ctx, complexText)

	assert.Equal(t, "by-llm-simple", routedModel(req), "the rule matched on COMPLEX, and the router that took its model routed on its own SIMPLE")
	assert.Equal(t, 1, decision.calls(), "the complexity config classified once, for the rule")
	assert.EqualValues(t, 1, llmCalls.Load(), "the router classified once, for itself")
	assert.Equal(t, complexity.TierSimple, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier), "the request records the tier of the router that took it")
	assert.Equal(t, complexity.MechanismLLM, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	assert.Equal(t, "by-llm", ctx.Value(schemas.BifrostContextKeyModelRouterID))
	assert.Equal(t, []string{schemas.RoutingEngineRoutingRule, schemas.RoutingEngineModelRouter}, enginesUsed(ctx))
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

// TestRoutingRuleScopeAndRouterScopeAreIndependent pins that each step
// resolves its own scope: a virtual key's routing rule feeds a global router
// only for that key's requests, and a global rule feeds a virtual key's
// router only for that key's requests.
func TestRoutingRuleScopeAndRouterScopeAreIndependent(t *testing.T) {
	granted := grantedAccess(schemas.ProviderPermit{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, KeyIDs: schemas.WhiteList{"*"}})
	run := func(t *testing.T, rule configstoreTables.TableRoutingRule, router configstore.ModelRouter, virtualKey string) (*schemas.BifrostContext, string) {
		t.Helper()
		governance := routing.NewMockGovernance()
		governance.Access = granted
		plugin := modelRoutingTestPlugin(t, governance, modelRoutingDecision(),
			[]configstoreTables.TableRoutingRule{rule}, []configstore.ModelRouter{router})
		ctx := scopedRequestContext(virtualKey, "", "", "")
		req := runPreRequest(t, plugin, ctx, complexText)
		return ctx, routedModel(req)
	}
	onMini := decisionModelRouter("on-mini", "On mini", tierTarget(complexity.TierComplex, "", "router-model"))

	t.Run("a virtual key's rule, then a global router", func(t *testing.T) {
		rule := ruleFor("vk-to-mini", `model == "gpt-4o"`, ruleTarget("", "gpt-4o-mini"))
		rule.Scope, rule.ScopeID = "virtual_key", schemas.Ptr("vk-1")
		router := onMini
		router.Routing = globalRouting(0, `model == "gpt-4o-mini"`)

		ctx, model := run(t, rule, router, "vk-1")
		assert.Equal(t, "router-model", model)
		assert.Equal(t, "on-mini", ctx.Value(schemas.BifrostContextKeyModelRouterID))

		ctx, model = run(t, rule, router, "vk-2")
		assert.Equal(t, "gpt-4o", model, "another key's request is not rewritten, so the router's condition does not hold")
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})

	t.Run("a global rule, then a virtual key's router", func(t *testing.T) {
		rule := ruleFor("to-mini", `model == "gpt-4o"`, ruleTarget("", "gpt-4o-mini"))
		router := onMini
		router.Routing = scopedRouting("virtual_key", "vk-1", 0, `model == "gpt-4o-mini"`)

		ctx, model := run(t, rule, router, "vk-1")
		assert.Equal(t, "router-model", model)
		assert.Equal(t, "on-mini", ctx.Value(schemas.BifrostContextKeyModelRouterID))

		ctx, model = run(t, rule, router, "vk-2")
		assert.Equal(t, "gpt-4o-mini", model, "the rule still applies; the other key's router does not")
		assert.Nil(t, ctx.Value(schemas.BifrostContextKeyModelRouterID))
	})
}

// ---------------------------------------------------------------------------
// Several model routers
// ---------------------------------------------------------------------------

// TestOnlyTheFirstMatchingRouterIsTried pins that exactly one router gets a
// request. Once a router's routing matches, the request is its own: if it has
// no model for the tier, or cannot classify the request, the request keeps
// its model and the next router is not asked. Only a router whose condition
// fails, or fails to evaluate, passes the request on.
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
		require.NoError(t, plugin.ReloadModelRouter(router))
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
// scope, saving one without routing, and changing a router's classifier.
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
	require.NoError(t, plugin.ReloadModelRouter(&withoutRouting))
	assert.Equal(t, "gpt-4o", next(), "a router saved without routing takes nothing")

	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return llmClassifierChatResponse(`{"tier": "SIMPLE"}`), nil
	})
	asLLM := configstore.ModelRouter{ID: "first", Name: "First", ComplexityAnalyzerConfig: llmRouterConfig(), Targets: everyTierTargets("first")}
	asLLM.Routing = globalRouting(0, "")
	require.NoError(t, plugin.ReloadModelRouter(&asLLM))
	ctx := newRequestContext()
	req := runPreRequest(t, plugin, ctx, complexText)
	assert.Equal(t, "first-simple", routedModel(req), "a router saved with another classifier classifies with it")
	assert.Equal(t, complexity.MechanismLLM, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}

// TestModelRouterSessionsStayApart pins that session routing belongs to each
// router: within one session, a router that reached COMPLEX holds its own
// requests there, while another router, and the Complexity Router page's
// config that routing rules read, keep classifying on their own ladder.
func TestModelRouterSessionsStayApart(t *testing.T) {
	decision := modelRoutingDecision()
	sessionRouter := func(id string, priority int) configstore.ModelRouter {
		router := configstore.ModelRouter{ID: id, Name: id, ComplexityAnalyzerConfig: sessionRouterConfig(), Targets: everyTierTargets(id)}
		router.Routing = appRouting(priority, id)
		return router
	}
	pageRule := ruleFor("page-complex", `headers["x-app"] == "page" && complexity_tier == "COMPLEX"`, ruleTarget("", "page-complex"))
	plugin := modelRoutingTestPluginWithConfig(t, &routing.Config{KVStore: newTestKVStore(t)}, routing.NewMockGovernance(), decision.execute,
		[]configstoreTables.TableRoutingRule{pageRule}, []configstore.ModelRouter{sessionRouter("a", 0), sessionRouter("b", 1)})
	pageConfig := sessionRouterConfig()
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(&pageConfig))

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
		// The complexity config's session was raised by neither router.
		{session: "s1", app: "page", text: simpleText, wantModel: "gpt-4o", wantTier: complexity.TierSimple, wantMechanism: complexity.MechanismDecision, wantRouter: nil, wantDecisionCalls: 5},
		// Another session of router a starts from nothing.
		{session: "s2", app: "a", text: simpleText, wantModel: "a-simple", wantTier: complexity.TierSimple, wantMechanism: complexity.MechanismDecision, wantRouter: "a", wantDecisionCalls: 6},
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
		plugin := modelRoutingTestPluginWithConfig(t, &routing.Config{KVStore: newTestKVStore(t)}, routing.NewMockGovernance(), decision.execute,
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

	t.Run("a routing rule's tier and the router's tier each resume", func(t *testing.T) {
		decision := modelRoutingDecision()
		mediumRule := ruleFor("medium", `complexity_tier == "MEDIUM"`, ruleTarget("", "rule-medium"))
		byLLM := configstore.ModelRouter{ID: "by-llm", Name: "By llm", ComplexityAnalyzerConfig: llmRouterConfig(), Targets: everyTierTargets("by-llm")}
		byLLM.Routing = globalRouting(0, `model == "rule-medium"`)
		plugin := modelRoutingTestPluginWithConfig(t, &routing.Config{KVStore: newTestKVStore(t)}, routing.NewMockGovernance(), decision.execute,
			[]configstoreTables.TableRoutingRule{mediumRule}, nil)
		var llmCalls atomic.Int32
		plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			llmCalls.Add(1)
			return llmClassifierChatResponse(`{"tier": "SIMPLE"}`), nil
		})
		require.NoError(t, plugin.ReloadModelRouter(&byLLM))

		turn := complexitySessionContext("s1")
		req := runPreRequest(t, plugin, turn, mediumText)
		require.Equal(t, "by-llm-simple", routedModel(req), "the rule matched on MEDIUM and the router routed on its own SIMPLE")
		_, _, err := plugin.PostLLMHook(turn, servedChatResponse(), nil)
		require.NoError(t, err)

		continuation := complexitySessionContext("s1")
		req = toolContinuationRequest(complexText)
		require.NoError(t, plugin.PreRequestHook(continuation, req))
		assert.Equal(t, "by-llm-simple", routedModel(req), "the rule resumes on MEDIUM, so the router takes the request again and resumes on SIMPLE")
		assert.Equal(t, "by-llm", continuation.Value(schemas.BifrostContextKeyModelRouterID))
		assert.Equal(t, 1, decision.calls(), "the complexity config does not classify the continuation")
		assert.EqualValues(t, 1, llmCalls.Load(), "nor does the router")
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
		_ = plugin.SetModelRouterTargets("a", []configstoreTables.TableModelRouterTarget{tierTarget(complexity.TierComplex, "", model)})
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
		_ = plugin.ReloadModelRouter(&served)
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
