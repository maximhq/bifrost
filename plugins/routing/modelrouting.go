package routing

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// modelRouterTiersKey keys the per-request record of each classifier's answer,
// keyed by runtime id ("" for the Complexity Router page's config). A request
// can be classified twice, once for a routing rule's complexity_tier and once
// by the model router that takes it; the record keeps both, so each has its
// own continuation tier persisted (persistComplexityTurnTier), and a
// classifier asked again within the request answers the same.
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

// applyModelRouters is the model router step, after the routing rules. The
// first enabled router whose routing matches takes the request (see
// rules.ModelRouterMatcher.Match); it classifies the request, and its target
// for that tier replaces the provider and model. A request no router takes,
// one the router cannot classify, and one whose tier has no target keep what
// the routing rules chose; no other router is tried.
//
// Applying a target follows the agreed rules: a provider change clears a key
// the routing rules pinned, since it names a key of the old provider, unless
// the target pins its own; and the target's fallbacks replace the chain only
// when it has some. A gateway with no enabled router pays one empty-map check.
//
// chainFrom is the name of the router whose target replaced the request's
// fallback chain, and "" when the chain is still the routing rules' or the
// caller's, so a fallback governance will refuse is reported against whoever
// wrote it.
func (p *RoutingPlugin) applyModelRouters(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, scope rules.GovernanceScope) (chainFrom string) {
	if !p.routerMatcher.HasRoutes() {
		return ""
	}
	provider, model, _ := req.GetRequestFields()
	if model == "" {
		return ""
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	queryParams, _ := ctx.Value(schemas.BifrostContextKeyRequestQuery).(map[string]string)
	evaluation := &rules.EvaluationContext{
		Scope:                    scope,
		Provider:                 provider,
		Model:                    model,
		RequestType:              string(req.RequestType),
		Headers:                  headers,
		QueryParams:              queryParams,
		BudgetAndRateLimitStatus: p.governance.GetBudgetAndRateLimitStatus(ctx, provider, model, nil, nil, nil),
	}
	routerID, err := p.routerMatcher.Match(ctx, evaluation)
	if err != nil {
		p.logger.Error("failed to match model routers: %v", err)
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelError, fmt.Sprintf("Model router matching error: %v", err))
		return ""
	}
	if routerID == "" {
		return ""
	}
	rt, ok := p.modelRouter(routerID)
	if !ok {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelWarn,
			fmt.Sprintf("Model router %q matched but is not loaded; keeping provider=%s, model=%s", routerID, provider, model))
		return ""
	}
	ctx.SetValue(schemas.BifrostContextKeyModelRouterID, rt.id)
	ctx.SetValue(schemas.BifrostContextKeyModelRouterName, rt.displayName())
	schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineModelRouter)

	result := p.computeRouterComplexity(ctx, req, rt)
	if result == nil {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelInfo,
			fmt.Sprintf("Model router '%s' produced no tier; keeping provider=%s, model=%s", rt.displayName(), provider, model))
		return ""
	}
	target, ok := rules.SelectModelRouterTarget(rt.loadTargets(), result.Tier)
	if !ok {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelInfo,
			fmt.Sprintf("Model router '%s' has no model for tier=%s; keeping provider=%s, model=%s", rt.displayName(), result.Tier, provider, model))
		return ""
	}
	replacedChain := p.applyModelRouterTarget(ctx, req, rt, target, provider, model)
	routedProvider, routedModel, _ := req.GetRequestFields()
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelInfo,
		fmt.Sprintf("Model router '%s' tier=%s → provider=%s, model=%s", rt.displayName(), result.Tier, routedProvider, routedModel))
	if replacedChain {
		return rt.displayName()
	}
	return ""
}

// applyModelRouterTarget writes a tier target onto the request and reports
// whether the target's fallbacks replaced the request's chain. provider and
// model are what the routing rules settled on; a target field left unset
// keeps them. A first-token deadline a routing rule set was set for the rule's
// target, so it is dropped once the router moves the request to another
// provider or model.
func (p *RoutingPlugin) applyModelRouterTarget(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostRequest,
	rt *modelRouterRuntime,
	target configstoreTables.TableModelRouterTarget,
	provider schemas.ModelProvider,
	model string,
) (replacedChain bool) {
	if target.Provider != nil && strings.TrimSpace(*target.Provider) != "" {
		if next := schemas.ModelProvider(strings.TrimSpace(*target.Provider)); next != provider {
			// A key the routing rules pinned belongs to their provider.
			ctx.ClearValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID)
			ctx.ClearValue(schemas.BifrostContextKeyStreamFirstTokenTimeout)
			req.SetProvider(next)
		}
	}
	if target.Model != nil && strings.TrimSpace(*target.Model) != "" {
		if next := strings.TrimSpace(*target.Model); next != model {
			ctx.ClearValue(schemas.BifrostContextKeyStreamFirstTokenTimeout)
			req.SetModel(next)
		}
	}
	if target.KeyID != nil && strings.TrimSpace(*target.KeyID) != "" {
		ctx.SetValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID, strings.TrimSpace(*target.KeyID))
	}
	if len(target.ParsedFallbacks) > 0 {
		req.SetFallbacks(resolveRuleFallbacks(ctx, schemas.RoutingEngineModelRouter, "Model router '"+rt.displayName()+"'", target.ParsedFallbacks, model))
		return true
	}
	return false
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
