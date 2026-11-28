package routing

import (
	"fmt"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// modelRouterTiersKey keys the per-request record of each classifier's answer,
// keyed by runtime id ("" for the Complexity Router page's config). A request
// can be classified by several routers on its way through the walk; the record
// keeps each answer, so each router has its own continuation tier persisted
// (persistComplexityTurnTier), and a classifier asked again within the request
// answers the same.
type modelRouterTiersKey struct{}

// routerTier is one classifier's answer for a request, as it was published.
type routerTier struct {
	result    *complexity.ComplexityResult
	mechanism string
	score     *float64
}

// cachedRouterTier returns the classifier's earlier answer for this request.
func cachedRouterTier(ctx *schemas.BifrostContext, routerID string) (routerTier, bool) {
	tiers, _ := ctx.Value(modelRouterTiersKey{}).(map[string]routerTier)
	entry, ok := tiers[routerID]
	return entry, ok
}

// cacheRouterTier records the classifier's answer, reading the mechanism and
// score it published alongside the result.
func cacheRouterTier(ctx *schemas.BifrostContext, routerID string, result *complexity.ComplexityResult) {
	entry := routerTier{result: result}
	entry.mechanism, _ = ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism).(string)
	if score, ok := ctx.Value(schemas.BifrostContextKeyGovernanceComplexityScore).(float64); ok {
		entry.score = &score
	}
	tiers, _ := ctx.Value(modelRouterTiersKey{}).(map[string]routerTier)
	if tiers == nil {
		tiers = make(map[string]routerTier, 1)
		ctx.SetValue(modelRouterTiersKey{}, tiers)
	}
	tiers[routerID] = entry
}

// routerStep is what one router did with a request during the walk.
type routerStep int

const (
	routerPassed  routerStep = iota // decided nothing: the walk goes on to the next router
	routerKept                      // keeps the request as it is, and ends the walk
	routerApplied                   // chose a target
	routerMissing                   // matched but is not served: the walk ends and nothing is recorded
)

// routerChoice is the target a router chose for a request, with the provider
// and model the request has once it is applied. A target field left unset
// keeps what the request had when the router took it.
type routerChoice struct {
	routerName string
	fireKey    string // what a chained router is remembered by once it has fired: see routerFireKey
	chain      bool   // the routers are walked again on the provider and model chosen
	provider   schemas.ModelProvider
	model      string
	target     configstoreTables.TableModelRouterTarget
	// fallbacks is the chain the choice brings: a conditional router's own, for whichever target
	// was picked, or the picked target's on a classifier router.
	fallbacks []configstoreTables.RoutingFallback
}

// applyModelRouters is the routing step. The
// enabled routers whose routing matches are walked in order (see
// rules.ModelRouterMatcher.Walk), and each decides on the request:
//
//   - a conditional router picks one of its targets, and passes the request
//     to the next router when it has none;
//   - a classifier router classifies the request and picks its target for
//     that tier. When it produces no tier, or has no target for it, it keeps
//     the request as it is and ends the walk, or passes the request on when
//     its routing says so (ModelRouterRouting.EmptyTarget is "next_router").
//
// A router that picks a target ends the walk, unless it is chained. A chained
// router's choice becomes the provider and model the routers are walked on
// again, from the top. It fires once per request (routers made from one
// routing rule count as one), the walk is repeated at most the configured chain
// depth, and only the last router's key, fallbacks and first-token deadline are
// applied.
//
// largePayload is the path that routes a request whose body is not parsed.
// There is nothing to classify on it, so a classifier router decides only
// from a tier the session already remembers, and otherwise always passes the
// request on: a router that kept it would end the walk on a request it never
// saw.
//
// A gateway with no enabled router pays one counter read. chainFrom is the
// name of the router whose target replaced the request's fallback chain, and
// "" when the chain is still the caller's, so a fallback governance will
// refuse is reported against whoever wrote it.
func (p *RoutingPlugin) applyModelRouters(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, scope rules.GovernanceScope, largePayload bool) (chainFrom string) {
	if !p.routerMatcher.HasRoutes() {
		return ""
	}
	arrivedProvider, arrivedModel, _ := req.GetRequestFields()
	if arrivedModel == "" {
		return ""
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	queryParams, _ := ctx.Value(schemas.BifrostContextKeyRequestQuery).(map[string]string)

	provider, model := arrivedProvider, arrivedModel
	var (
		last  *routerChoice       // the last router's choice, the one applied
		fired map[string]struct{} // chained routers that have chosen already, by fire key
		took  bool                // a router chose a target or kept the request
	)
	maxDepth := p.routerChainMaxDepth()
	for step := 0; ; step++ {
		if step >= maxDepth {
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelWarn,
				fmt.Sprintf("Chain exceeded max depth (%d) at step %d, stopping. Final resolved: provider=%s, model=%s", maxDepth, step, provider, model))
			break
		}
		if step > 0 {
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelInfo,
				fmt.Sprintf("Chain step %d: re-evaluating with provider=%s, model=%s", step, provider, model))
		}
		evaluation := &rules.EvaluationContext{
			Scope:       scope,
			Provider:    provider,
			Model:       model,
			RequestType: string(req.RequestType),
			Headers:     headers,
			QueryParams: queryParams,
			// Read for the provider and model of this pass, so a chained
			// router's choice is tested against its own budget and limits.
			BudgetAndRateLimitStatus: p.governance.GetBudgetAndRateLimitStatus(ctx, provider, model, nil, nil, nil),
		}
		var skip func(routerID string) bool
		if len(fired) > 0 {
			skip = func(routerID string) bool {
				_, done := fired[p.routerFireKeyByID(routerID)]
				return done
			}
		}
		var choice *routerChoice
		err := p.routerMatcher.Walk(ctx, evaluation, skip, func(routerID string) bool {
			var outcome routerStep
			choice, outcome = p.visitModelRouter(ctx, req, routerID, provider, model, largePayload)
			if outcome == routerPassed {
				return false
			}
			took = took || outcome != routerMissing
			return true
		})
		if err != nil {
			p.logger.Error("failed to match model routers: %v", err)
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelError, fmt.Sprintf("Model router matching error: %v", err))
			break
		}
		if choice == nil {
			break
		}
		last, provider, model = choice, choice.provider, choice.model
		if !choice.chain {
			break
		}
		if fired == nil {
			fired = make(map[string]struct{}, 2)
		}
		fired[choice.fireKey] = struct{}{}
	}
	if took {
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineModelRouter)
	}
	if last == nil {
		return ""
	}
	if p.applyModelRouterChoice(ctx, req, last, arrivedProvider, arrivedModel) {
		return last.routerName
	}
	return ""
}

// visitModelRouter lets one router whose routing matched decide on the
// request, which has provider and model at this point of the walk. It returns
// the router's choice when it picked a target, and what the walk does next.
// A router that takes the request, by choosing a target or by keeping it, is
// recorded on the request; one that passes it on is not.
func (p *RoutingPlugin) visitModelRouter(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostRequest,
	routerID string,
	provider schemas.ModelProvider,
	model string,
	largePayload bool,
) (*routerChoice, routerStep) {
	// A classifier router is served by its runtime, and read from its saved
	// record for its name, targets and routing. One whose record the live
	// store does not hold still classifies, as a router with no targets. A
	// conditional router is served from its record alone.
	router := &configstore.ModelRouter{ID: routerID}
	if record, ok := p.routerRecord(routerID); ok {
		router = &record.Router
	}
	conditional := isConditionalRouter(router)
	rt, served := p.modelRouter(routerID)
	if !conditional && !served {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelWarn,
			fmt.Sprintf("Model router %q matched but is not loaded; keeping provider=%s, model=%s", routerID, provider, model))
		return nil, routerMissing
	}
	name := router.Name
	if name == "" {
		name = routerID
	}
	passesOn := conditional || largePayload || (router.Routing != nil && router.Routing.EmptyTarget == configstoreTables.ModelRouterEmptyTargetNextRouter)

	tier := ""
	if !conditional {
		result := p.computeRouterComplexity(ctx, req, rt)
		if result == nil {
			if passesOn {
				ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelInfo,
					fmt.Sprintf("Model router '%s' produced no tier; trying the next router", name))
				return nil, routerPassed
			}
			recordModelRouter(ctx, routerID, name)
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelInfo,
				fmt.Sprintf("Model router '%s' produced no tier; keeping provider=%s, model=%s", name, provider, model))
			return nil, routerKept
		}
		tier = result.Tier
	}

	target, ok := rules.SelectModelRouterTarget(router.Targets, tier)
	if !ok {
		switch {
		case conditional:
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelInfo,
				fmt.Sprintf("Model router '%s' has no target; trying the next router", name))
			return nil, routerPassed
		case passesOn:
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelInfo,
				fmt.Sprintf("Model router '%s' has no model for tier=%s; trying the next router", name, tier))
			return nil, routerPassed
		}
		recordModelRouter(ctx, routerID, name)
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelInfo,
			fmt.Sprintf("Model router '%s' has no model for tier=%s; keeping provider=%s, model=%s", name, tier, provider, model))
		return nil, routerKept
	}

	choice := &routerChoice{
		routerName: name,
		fireKey:    routerFireKey(router),
		chain:      router.Routing != nil && router.Routing.Chain,
		provider:   provider,
		model:      model,
		target:     target,
		fallbacks:  target.ParsedFallbacks,
	}
	if conditional {
		choice.fallbacks = router.Fallbacks
	}
	if target.Provider != nil && strings.TrimSpace(*target.Provider) != "" {
		choice.provider = schemas.ModelProvider(strings.TrimSpace(*target.Provider))
	}
	if target.Model != nil && strings.TrimSpace(*target.Model) != "" {
		choice.model = strings.TrimSpace(*target.Model)
	}
	recordModelRouter(ctx, routerID, name)
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelInfo, matchedRouterLine(router, name, tier, choice))
	return choice, routerApplied
}

// matchedRouterLine is the routing trail's line for a router that chose a
// target, written as a matched routing rule's was: the router and its
// condition, the tier a classifier router gave the request, then the target's
// weight, the provider and model the request has once it is applied, and the
// fallbacks and first-token deadline the choice brings.
func matchedRouterLine(router *configstore.ModelRouter, name, tier string, choice *routerChoice) string {
	condition := ""
	if router.Routing != nil && strings.TrimSpace(router.Routing.CelExpression) != "" {
		condition = " [" + router.Routing.CelExpression + "]"
	}
	if tier != "" {
		tier = "tier=" + tier + ", "
	}
	deadline := ""
	if ms := choice.target.TTFTTimeoutMs; ms != nil && *ms > 0 {
		deadline = fmt.Sprintf(", ttft_timeout=%s", time.Duration(*ms)*time.Millisecond)
	}
	chained := ""
	if choice.chain {
		chained = " [chain, continuing]"
	}
	return fmt.Sprintf("Model router '%s'%s → matched, %sselected target (weight=%.2f): provider=%s, model=%s, fallbacks=%v%s%s",
		name, condition, tier, choice.target.Weight, choice.provider, choice.model, configstoreTables.RoutingFallbackStrings(choice.fallbacks), deadline, chained)
}

// applyModelRouterChoice writes the walk's last choice onto the request and
// reports whether its fallbacks replaced the request's chain. provider and
// model are what the request had when the walk began.
//
// A provider change clears a key the request was pinned to, since it names a
// key of the old provider, unless the target pins its own. A first-token
// deadline set for the request as it arrived is dropped once it moves to
// another provider or model, and the target's own deadline, when it has one,
// is set for a streaming request. The target's fallbacks replace the chain only
// when it has some; one that omits its model keeps the model the walk began
// with.
func (p *RoutingPlugin) applyModelRouterChoice(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostRequest,
	choice *routerChoice,
	provider schemas.ModelProvider,
	model string,
) (replacedChain bool) {
	if choice.provider != provider {
		// A key pinned before the walk belongs to the provider the request had.
		ctx.ClearValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID)
		ctx.ClearValue(schemas.BifrostContextKeyStreamFirstTokenTimeout)
		req.SetProvider(choice.provider)
	}
	if choice.model != model {
		ctx.ClearValue(schemas.BifrostContextKeyStreamFirstTokenTimeout)
		req.SetModel(choice.model)
	}
	target := choice.target
	if target.KeyID != nil && strings.TrimSpace(*target.KeyID) != "" {
		ctx.SetValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID, strings.TrimSpace(*target.KeyID))
	}
	// Core applies the deadline to every attempt but the last, so it only
	// takes effect when the request has fallbacks.
	if ms := target.TTFTTimeoutMs; ms != nil && *ms > 0 && bifrost.IsStreamRequestType(req.RequestType) {
		ctx.SetValue(schemas.BifrostContextKeyStreamFirstTokenTimeout, time.Duration(*ms)*time.Millisecond)
	}
	if len(choice.fallbacks) > 0 {
		req.SetFallbacks(resolveRuleFallbacks(ctx, schemas.RoutingEngineModelRouter, "Model router '"+choice.routerName+"'", choice.fallbacks, model))
		return true
	}
	return false
}

// recordModelRouter records on the request the router that took it. In a
// chain the last router to take it is the one recorded, as the last routing
// rule is.
func recordModelRouter(ctx *schemas.BifrostContext, routerID, name string) {
	ctx.SetValue(schemas.BifrostContextKeyModelRouterID, routerID)
	ctx.SetValue(schemas.BifrostContextKeyModelRouterName, name)
}

// isConditionalRouter reports whether a router routes on its condition alone,
// without classifying.
func isConditionalRouter(router *configstore.ModelRouter) bool {
	return router.Mode == configstoreTables.ModelRouterModeConditional
}

// routerFireKey is what a chained router is remembered by once it has fired
// in a request, so it does not fire again: the routing rule it was made from,
// or its own id. Routers made from one rule share the key, since the rule
// fired once however many routers it became.
func routerFireKey(router *configstore.ModelRouter) string {
	if router.Meta != nil && len(router.Meta.RoutingRuleIDs) > 0 {
		return "rule:" + router.Meta.RoutingRuleIDs[0]
	}
	return "router:" + router.ID
}

// routerFireKeyByID returns the fire key of the saved router with the given
// id, and the id's own key for one the live store does not hold.
func (p *RoutingPlugin) routerFireKeyByID(routerID string) string {
	if record, ok := p.routerRecord(routerID); ok {
		return routerFireKey(&record.Router)
	}
	return "router:" + routerID
}

// routerRecord returns the saved router with the given id as the live store
// holds it. The record is shared: callers must not change it.
func (p *RoutingPlugin) routerRecord(id string) (*registry.ModelRouter, bool) {
	if p.routerRecords == nil {
		return nil, false
	}
	return p.routerRecords.Get(id)
}

// routerChainMaxDepth is how many times a chain of routers may be walked: the
// configured chain depth, or the default when none is configured.
func (p *RoutingPlugin) routerChainMaxDepth() int {
	if p.chainMaxDepth != nil && *p.chainMaxDepth > 0 {
		return *p.chainMaxDepth
	}
	return rules.DefaultChainMaxDepth
}

// resolveRuleFallbacks turns a rule's fallback chain into the request's. A
// fallback that omits its model keeps model, the one the step received, as
// "provider/" documents; one that names no provider is skipped and logged
// under engine with the rule's label.
func resolveRuleFallbacks(ctx *schemas.BifrostContext, engine, label string, fallbacks []configstoreTables.RoutingFallback, model string) []schemas.Fallback {
	resolved := make([]schemas.Fallback, 0, len(fallbacks))
	for _, fb := range fallbacks {
		fallback := fb.Resolved()
		fallback.Provider = schemas.ModelProvider(strings.TrimSpace(string(fallback.Provider)))
		fallback.Model = strings.TrimSpace(fallback.Model)
		fallback.KeyID = strings.TrimSpace(fallback.KeyID)
		if fallback.Provider == "" {
			ctx.AppendRoutingEngineLog(engine, schemas.LogLevelWarn, fmt.Sprintf("%s: fallback %q skipped: it does not name a known provider", label, fb.String()))
			continue
		}
		if fallback.Model == "" {
			fallback.Model = model
		}
		resolved = append(resolved, fallback)
	}
	return resolved
}
