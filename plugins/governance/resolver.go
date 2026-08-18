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
	// provider routes where a model may or may not be resolvable for some request types. Video
	// edit's model is itself optional (e.g. OpenAI infers it from the source video ID), so it is
	// checked only when the caller actually supplied one, same as passthrough.
	// Requests that are evaluated but never routed set the skip-model-check flag on the
	// context. Their model is whatever upstream model the caller was already talking to,
	// not a model an operator granted this key, so the allowlist carries no meaning for
	// them. This is separate from skipProviderCheck: a provider the key does configure
	// still carries a model allowlist that would deny the request one step later.
	skipModelCheck := bifrost.GetBoolFromContext(ctx, schemas.BifrostContextKeySkipModelCheck)
	checkModelIfPresent := IsModelCheckedWhenPresent(requestType)
	if !skipModelCheck && !providerUnconfigured && (IsModelRequiredForRequest(requestType) || (checkModelIfPresent && model != "")) && !ea.IsModelAllowed(provider, model) {
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
	if spendingChecksSkipped(ctx) {
		return &EvaluationResult{
			Decision: DecisionAllow,
			Reason:   "Request allowed by governance policy (spending checks skipped)",
		}
	}

	// Everything the request answers to, with nothing filtered out. The limits were resolved onto the
	// access once its provider and model were known, and this reads that one list — every request has an
	// access, so there is no second source to fall back to and no case where what was enforced was
	// assembled somewhere else.
	//
	// No filtering by holder kind, which is what makes a holder this package has never heard of enforced
	// by construction rather than by being registered somewhere. Which credential a request is governed as
	// is settled before governance sees it, by whoever resolves its access.
	budgets := ea.ResolvedBudgets()
	rateLimits := ea.ResolvedRateLimits()

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

// untrackedHolderKinds are the kinds still billed when a caller has asked for the holder's usage not to
// be counted: what the deployment imposes, and what the user who made the request answers to.
//
// This names what to keep rather than what to drop, because the set it names is closed and belongs to
// this package, while what a holder funds is open — so a kind nobody here has heard of is a holder's,
// and leaving it out is exactly what the caller asked for. Note this filters only who gets billed, never
// who gets checked: everything on the access is enforced, so asking for usage not to be counted cannot
// buy a request past a limit it could not otherwise afford.
var untrackedHolderKinds = []grants.LimitHolderKind{
	grants.LimitHolderProvider,
	grants.LimitHolderModelConfig,
	grants.LimitHolderUserModelConfig,
}

// spendingChecksSkipped reports whether this request was told not to be checked against anything it
// answers to. Read-only metadata calls set it: they spend nothing, so there is nothing to afford.
func spendingChecksSkipped(ctx *schemas.BifrostContext) bool {
	return bifrost.GetBoolFromContext(ctx, schemas.BifrostContextKeySkipBudgetAndRateLimits)
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
