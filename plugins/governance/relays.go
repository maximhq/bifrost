package governance

import (
	"context"
	"errors"
	"fmt"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/jobaccounting"
)

var _ schemas.RelayPlugin = (*GovernancePlugin)(nil)
var _ jobaccounting.RelayUsageReporter = (*GovernancePlugin)(nil)

// PreRelayHook governs a relay call the way a tool execution is
// governed: the same evaluation (identity, then every budget and rate limit the caller's holders
// carry), with no provider or model gate, since an endpoint has neither. Routing rules, provider
// configs and model budgets therefore never apply. What evaluation cannot answer is the endpoint
// itself, which the access decides: a key holding no grant for it is refused before any upstream
// effect. A request carrying no access is unrestricted, as it is for MCP tools.
func (p *GovernancePlugin) PreRelayHook(ctx *schemas.BifrostContext, req *schemas.BifrostRelayRequest) (*schemas.BifrostRelayRequest, *schemas.RelayPluginShortCircuit, error) {
	if req == nil {
		return req, nil, nil
	}
	if headerErr := p.validateRequiredHeaders(ctx); headerErr != nil {
		return req, &schemas.RelayPluginShortCircuit{Error: headerErr}, nil
	}
	if _, bifrostErr := p.Evaluate(ctx, &EvaluationRequest{RequestType: schemas.RelayRequest}); bifrostErr != nil {
		return req, &schemas.RelayPluginShortCircuit{Error: bifrostErr}, nil
	}
	access := ctx.Grant().Access()
	if access != nil && !access.IsRelayAllowed(req.RelayName) {
		ctx.SetValue(governanceRejectedContextKey, true)
		return req, &schemas.RelayPluginShortCircuit{Error: &schemas.BifrostError{
			IsBifrostError: true,
			Type:           bifrost.Ptr(string(DecisionRelayBlocked)),
			StatusCode:     bifrost.Ptr(403),
			Error: &schemas.ErrorField{
				Message: denialReason(fmt.Sprintf("Relay '%s' is not allowed", req.RelayName), access.DeniedPermitsForRelay(req.RelayName)),
			},
		}}, nil
	}
	return req, nil, nil
}

// PostRelayHook counts and charges a relayed call against the limits settled when the call was
// evaluated, which are the holders' overall limits only. Every reply counts one request against
// the request rate limits, as every MCP tool execution does, whether or not the route is billable:
// a limit on calls bounds calls. Only a counted call (a billable route answered 2xx) carries a
// cost, which lands on the budgets. A stream the caller abandoned still reports what was
// forwarded, so it is charged too, and it counts: the upstream answered, and hanging up early
// must not be a way around a request limit. A call that never got a reply is neither counted nor
// charged.
func (p *GovernancePlugin) PostRelayHook(ctx *schemas.BifrostContext, resp *schemas.BifrostRelayResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostRelayResponse, *schemas.BifrostError, error) {
	if _, rejected := ctx.Value(governanceRejectedContextKey).(bool); rejected {
		return resp, bifrostErr, nil
	}
	if resp == nil {
		return resp, bifrostErr, nil
	}
	cost := 0.0
	if resp.Usage != nil {
		cost = resp.Usage.Cost
	}
	var budgets, rateLimits []schemas.Limit
	if limits := ctx.Grant().Limits(); limits != nil {
		budgets, rateLimits = limits.Budgets(), limits.RateLimits()
	}
	if bifrost.GetBoolFromContext(ctx, schemas.BifrostContextKeySkipVirtualKeyUsageTracking) {
		budgets = grant.LimitsFrom(budgets, untrackedHolderKinds...)
		rateLimits = grant.LimitsFrom(rateLimits, untrackedHolderKinds...)
	}
	// Recorded for the logging plugin, which writes them on the Relay Logs row and on the job
	// row a job route starts, so a settlement charges the same limits the start call did.
	if budgetIDs := limitIDsOf(budgets); len(budgetIDs) > 0 {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceBudgetIDs, budgetIDs)
	}
	if rateLimitIDs := limitIDsOf(rateLimits); len(rateLimitIDs) > 0 {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceRateLimitIDs, rateLimitIDs)
	}
	usageUpdate := &UsageUpdate{
		Success:      true, // a reply was received; the error, if any, is how the stream ended
		Cost:         cost,
		RequestID:    bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRequestID),
		BillingNonce: bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyBillingNonce),
		IsFinalChunk: true,
		HasUsageData: cost > 0,
		Budgets:      budgets,
		RateLimits:   rateLimits,
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.tracker.UpdateUsage(p.ctx, usageUpdate)
	}()
	return resp, bifrostErr, nil
}

// ReportRelayUsage charges a settled relay job's cost to the budgets its start
// call was charged against. It counts no request: the start call already did. Retries within one
// process are deduped on the report's request id; across processes the sweeper's
// governance_reported_at mark is what prevents a second charge, so a crash between the bump and
// that mark can charge a settlement twice.
func (p *GovernancePlugin) ReportRelayUsage(ctx context.Context, report jobaccounting.RelayUsageReport) error {
	if report.Cost <= 0 {
		return nil
	}
	var errs []error
	for _, budgetID := range report.BudgetIDs {
		billingKey := fmt.Sprintf("%s:budget:%s", report.RequestID, budgetID)
		if report.RequestID != "" && !p.tracker.tryClaimBatchBilling(billingKey) {
			continue
		}
		if err := p.store.BumpBudgetUsage(ctx, budgetID, report.Cost); err != nil {
			p.tracker.releaseBatchBilling(billingKey)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
