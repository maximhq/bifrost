package bifrost

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// Send back cost: when enabled, the final response or error of a request
// carries Bifrost's own computed cost in ExtraFields.Cost.
//
// The cost is priced once, after every PostLLMHook has run, because post-hooks
// stamp billable metadata (semantic cache, routing, guardrails) that the price
// includes. It is written only on a copy: plugins may still read the object
// they were given from their own goroutines. Plugins keep pricing on their own
// for logs, budgets and metrics; this does not change what they record.

// responseCostEnabled reports whether this request's result should carry its
// cost: the per-request override when present, else the global setting.
func (bifrost *Bifrost) responseCostEnabled(ctx *schemas.BifrostContext) bool {
	if override, ok := ctx.Value(schemas.BifrostContextKeySendBackCost).(bool); ok {
		return override
	}
	return bifrost.sendBackCost.Load()
}

// withResponseCost returns resp with its cost attached, or resp unchanged when
// the setting is off, the cost is already set, or nothing is billable.
func (bifrost *Bifrost) withResponseCost(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse) *schemas.BifrostResponse {
	if resp == nil || !bifrost.responseCostEnabled(ctx) || resp.GetExtraFields().Cost != nil {
		return resp
	}
	cost := ctx.CalculateCostBreakdown(withRequestRoutingMetadata(ctx, resp))
	if cost == nil || cost.TotalCost <= 0 {
		return resp
	}
	return resp.WithCost(cost)
}

// withRequestRoutingMetadata returns the response to price, so the routing classifier,
// which runs once per request, is priced once. Its metadata is stamped only on the
// first attempt's response, so a request served by a retry or a fallback is priced as
// a copy carrying the request's routing metadata. A later turn of an MCP agent loop
// shares the first turn's context and the loop sums its turns, so such a turn is
// priced as a copy without it. resp and the response returned to the caller are
// never changed.
func withRequestRoutingMetadata(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse) *schemas.BifrostResponse {
	stamped := resp.GetExtraFields().RoutingMetadata != nil
	if agentContinuationRoute(ctx) != nil {
		if !stamped {
			return resp
		}
		priced := resp.WithCost(nil) // a copy of the variant holding ExtraFields
		priced.GetExtraFields().RoutingMetadata = nil
		return priced
	}
	if stamped {
		return resp
	}
	metadata, ok := schemas.RoutingMetadataFromContext(ctx)
	if !ok {
		return resp
	}
	priced := resp.WithCost(nil) // a copy of the variant holding ExtraFields
	priced.GetExtraFields().RoutingMetadata = metadata
	return priced
}

// withErrorCost returns err with its cost attached, or err unchanged when the
// setting is off, the cost is already set, or nothing is billable.
func (bifrost *Bifrost) withErrorCost(ctx *schemas.BifrostContext, err *schemas.BifrostError) *schemas.BifrostError {
	if err == nil || !bifrost.responseCostEnabled(ctx) || err.ExtraFields.Cost != nil {
		return err
	}
	cost := ctx.CalculateErrorCostBreakdown(err)
	if cost == nil || cost.TotalCost <= 0 {
		return err
	}
	return err.WithCost(cost)
}
