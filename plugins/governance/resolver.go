// Package governance provides the budget evaluation and decision engine
package governance

import (
	"fmt"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grants"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// Decision represents the result of governance evaluation
type Decision string

const (
	DecisionAllow           Decision = "allow"
	DecisionAccessNotFound  Decision = "access_not_found"
	DecisionAccessBlocked   Decision = "access_blocked"
	DecisionRateLimited     Decision = "rate_limited"
	DecisionBudgetExceeded  Decision = "budget_exceeded"
	DecisionTokenLimited    Decision = "token_limited"
	DecisionRequestLimited  Decision = "request_limited"
	DecisionModelBlocked    Decision = "model_blocked"
	DecisionProviderBlocked Decision = "provider_blocked"
	DecisionMCPToolBlocked  Decision = "mcp_tool_blocked"
	// DecisionAccessUnresolved means nothing was resolved about what the request may reach, so
	// there was nothing to evaluate it against. It is a wiring fault rather than a policy
	// decision: evaluation is reached only after access has been resolved for the request.
	DecisionAccessUnresolved Decision = "access_unresolved"
)

// EvaluationRequest contains the context for evaluating a request
type EvaluationRequest struct {
	RequestType schemas.RequestType   `json:"request_type"`
	Provider    schemas.ModelProvider `json:"provider"`
	Model       string                `json:"model"`
}

// EvaluationResult is a governance verdict: whether the request may proceed, and why not when it
// may not.
//
// The reason is the whole of the answer. Earlier versions also carried the rate limit, the budgets
// and the usage that produced the verdict, which nothing ever read — a caller that is refused needs
// to be told what refused it, and one that is allowed needs nothing at all.
type EvaluationResult struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason"`
}

// BudgetResolver provides decision logic for the new hierarchical governance system
type BudgetResolver struct {
	store                   GovernanceStore
	logger                  schemas.Logger
	modelCatalog            *modelcatalog.ModelCatalog
	governanceInMemoryStore InMemoryStore
}

// allLimitHolderKinds is every kind of holder whose limits this package enforces. A kind absent from
// here is a kind nothing checks, so a new holder is wired up by adding it once.
var allLimitHolderKinds = []grants.LimitHolderKind{
	LimitHolderProvider,
	LimitHolderModelConfig,
	LimitHolderScopedModelConfig,
	grants.LimitHolderVirtualKeyProviderConfig,
	grants.LimitHolderVirtualKey,
	grants.LimitHolderTeam,
	grants.LimitHolderCustomer,
}

// NewBudgetResolver creates a new budget-based governance resolver
func NewBudgetResolver(store GovernanceStore, modelCatalog *modelcatalog.ModelCatalog, logger schemas.Logger, governanceInMemoryStore InMemoryStore) *BudgetResolver {
	return &BudgetResolver{
		store:                   store,
		logger:                  logger,
		modelCatalog:            modelCatalog,
		governanceInMemoryStore: governanceInMemoryStore,
	}
}

// evaluateAccess applies what a request may reach: the provider gate, the model gate, and the
// provider-key restriction its grants imply. Every request goes through it, whatever granted the
// access — a virtual key, or whatever else the store answers for — so nothing can be reached
// through one kind of authentication that another would refuse.
//
// It reads nothing about the credential the request presented. Which of them granted the access, and
// whether one that resolved to nothing should be refused, are settled before this — see the funnel's
// identity step, which asks what was presented.
func (r *BudgetResolver) evaluateAccess(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest, ea *grants.EffectiveAccess) *EvaluationResult {
	// No access at all reaches here only from a store that answered with none for a request that
	// presented something — the funnel refuses that before this, so in practice this is unreachable. It
	// allows rather than refuses because an allowlist there is none of cannot deny anything, and a
	// request nothing granted is unrestricted, which is the shape such a request now carries.
	if ea == nil {
		return &EvaluationResult{
			Decision: DecisionAllow,
			Reason:   "No grants resolved for this request, skipping access checks",
		}
	}

	// Requests that are evaluated but never routed set this flag. Their provider is
	// whatever upstream the caller was already talking to, not a provider an operator
	// picked, so the provider allowlist carries no meaning for them.
	skipProviderCheck := bifrost.GetBoolFromContext(ctx, schemas.BifrostContextKeySkipProviderCheck)

	requestType, provider, model := evaluationRequest.RequestType, evaluationRequest.Provider, evaluationRequest.Model

	// A caller with no provider config for this provider also has no model allowlist for it,
	// since allowed models hang off a provider config. When the provider gate is skipped the
	// model gate has nothing left to check against, so it is skipped with it. A provider the
	// caller does configure keeps its model allowlist.
	providerUnconfigured := skipProviderCheck && !ea.IsProviderAllowed(provider)
	if !skipProviderCheck && evaluationRequest.RequestType != schemas.MCPToolExecutionRequest && requestType != schemas.ListModelsRequest && !ea.IsProviderAllowed(provider) {
		return &EvaluationResult{
			Decision: DecisionProviderBlocked,
			Reason:   denialReason(fmt.Sprintf("Provider '%s' is not allowed", provider), ea.DenyingGrant(provider, "")),
		}
	}
	// Most request types always carry a model and are always checked. Passthrough forwards raw
	// provider routes where a model may or may not be resolvable for some request types.
	isPassthrough := requestType == schemas.PassthroughRequest || requestType == schemas.PassthroughStreamRequest
	if !providerUnconfigured && (IsModelRequiredForRequest(requestType) || (isPassthrough && model != "")) && !ea.IsModelAllowed(provider, model) {
		return &EvaluationResult{
			Decision: DecisionModelBlocked,
			Reason:   denialReason(fmt.Sprintf("Model '%s' is not allowed", model), ea.DenyingGrant(provider, model)),
		}
	}

	// Publish the provider keys the request may use, when its access restricts them at all.
	// Downstream key selection consumes plain key ids, so it never has to know what a grant is.
	if keyIDs, restricted := ea.KeysFor(provider); restricted {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys, keyIDs)
	}

	return &EvaluationResult{
		Decision: DecisionAllow,
		Reason:   "Request allowed by the grants it carries",
	}
}

// evaluateLimits decides whether a request can be afforded: one check over every limit that covers
// this attempt, whoever holds it.
//
// The limits come from two places and are treated identically once gathered. What a holder is funded
// by travels on the grant, resolved before the request was routed. What this attempt draws on — the
// provider's own limits, the model configs that apply to its model — is resolved here, because it
// depends on the provider and model the attempt actually uses. Neither the gathering nor the check
// asks what kind of holder is paying, which is what lets a deployment add one by answering with a
// different grant.
func (r *BudgetResolver) evaluateLimits(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest, ea *grants.EffectiveAccess) *EvaluationResult {
	kinds := enforcedLimitHolderKinds(ctx)
	if len(kinds) == 0 {
		return &EvaluationResult{
			Decision: DecisionAllow,
			Reason:   "Request allowed by governance policy (spending checks skipped)",
		}
	}

	// The limits were resolved onto the access once this request's provider and model were known, so
	// this reads one list rather than combining several sources in the right order. A request carrying
	// no grant had nothing to resolve them onto and is still subject to the deployment's own, so those
	// are gathered here for it.
	budgets, rateLimits := ea.ResolvedBudgets(), ea.ResolvedRateLimits()
	if ea == nil {
		budgets, rateLimits = r.store.ProviderAndModelLimits(ctx, nil, evaluationRequest.Provider, evaluationRequest.Model)
	}
	budgets, rateLimits = grants.LimitsFrom(budgets, kinds...), grants.LimitsFrom(rateLimits, kinds...)

	// Rate limits before budgets, as they always have been: a rate limit refuses a request that
	// would otherwise have been affordable, and reporting the cheaper refusal first keeps the
	// message stable for a caller that is both throttled and out of money.
	if decision, err := r.store.CheckRateLimits(ctx, rateLimits, nil, nil); err != nil || isRateLimitViolation(decision) {
		return &EvaluationResult{
			Decision: decision,
			Reason:   fmt.Sprintf("Rate limit exceeded: %s", reasonFromErr(err, decision)),
		}
	}

	if decision, err := r.store.CheckBudgets(ctx, budgets, nil); err != nil || isBudgetViolation(decision) {
		return &EvaluationResult{
			Decision: decision,
			Reason:   fmt.Sprintf("Budget exceeded: %s", reasonFromErr(err, decision)),
		}
	}

	return &EvaluationResult{
		Decision: DecisionAllow,
		Reason:   "Request allowed by governance policy (spending checks passed)",
	}
}

// enforcedLimitHolderKinds lists the holder kinds this request is actually subject to, after the
// skip flags its caller set.
//
// Skipping is per holder kind rather than per call site. Read-only metadata calls skip spending
// entirely; a user-authenticated request skips the presented key's own limits, because what it may
// spend is the user's allowance and not the key's. Expressing both as "which kinds still apply"
// keeps one check for every request instead of a flag threaded through a check per holder.
func enforcedLimitHolderKinds(ctx *schemas.BifrostContext) []grants.LimitHolderKind {
	if bifrost.GetBoolFromContext(ctx, schemas.BifrostContextKeySkipBudgetAndRateLimits) {
		return nil
	}
	if userID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID); userID != "" {
		kinds := make([]grants.LimitHolderKind, 0, len(allLimitHolderKinds))
		for _, kind := range allLimitHolderKinds {
			if kind == grants.LimitHolderVirtualKey || kind == grants.LimitHolderVirtualKeyProviderConfig {
				continue
			}
			kinds = append(kinds, kind)
		}
		return kinds
	}
	return allLimitHolderKinds
}

// denialReason is what the caller is told when a request is refused: what was refused, and —
// when there is a named grant in the way — which of their grants refused it.
func denialReason(refusal string, denying *grants.Grant) string {
	if denying == nil || denying.Name == "" {
		return refusal
	}
	return fmt.Sprintf("%s for %s '%s'", refusal, denying.Type, denying.Name)
}

// isRateLimitViolation returns true if the decision indicates a rate limit violation
func isRateLimitViolation(decision Decision) bool {
	return decision == DecisionRateLimited || decision == DecisionTokenLimited || decision == DecisionRequestLimited
}

// isBudgetViolation returns true if the decision indicates a budget violation.
func isBudgetViolation(decision Decision) bool {
	return decision == DecisionBudgetExceeded
}

// reasonFromErr yields a non-nil-safe reason string. When the store returns a
// non-allow decision without an accompanying error, err.Error() would panic —
// fall back to a generic phrase that still names the decision.
func reasonFromErr(err error, decision Decision) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("policy violation (%s)", decision)
}
