package schemas

// ModelInfoProvider is the narrow slice of the model catalog that core exposes
// to plugins. The catalog itself lives in framework/modelcatalog, which core
// cannot import (framework depends on core, not the other way round), so the
// concrete instance arrives as an interface value stamped onto the request
// context under BifrostContextKeyModelCatalog.
//
// This mirrors how Tracer is wired: interface declared here, implemented in
// framework, reached from BifrostContext via a lookup-and-delegate accessor.
//
// Implemented by *modelcatalog.ModelCatalog.
type ModelInfoProvider interface {
	// GetModelInfo returns pricing and capability metadata for a
	// (provider, model) pair, or nil when the catalog has no entry.
	GetModelInfo(provider ModelProvider, model string) *Model

	// CalculateRequestCost returns the dollar cost of a completed response,
	// resolving governance pricing overrides from ctx.
	//
	// Named CalculateRequestCost rather than CalculateCost because
	// *modelcatalog.ModelCatalog already has a CalculateCost method with a
	// different signature; a plugin-facing ctx.CalculateCost wraps this.
	CalculateRequestCost(ctx *BifrostContext, resp *BifrostResponse) float64

	// CalculateRequestCostBreakdown returns the per-category cost breakdown of
	// a completed response: input (text, audio, image, cache read, cache
	// write, per-request surcharge), output (text, audio, image, reasoning,
	// citation, search queries) and additional (guardrail, MCP, semantic cache,
	// routing), resolving governance pricing overrides from ctx. TotalCost
	// equals CalculateRequestCost for the same response. Returns nil when
	// there is nothing billable.
	//
	// The returned value is owned by the caller. Carries the Request infix
	// for the same reason as CalculateRequestCost: *modelcatalog.ModelCatalog
	// already has a CalculateCostBreakdown with a different signature; the
	// plugin-facing ctx.CalculateCostBreakdown wraps this.
	CalculateRequestCostBreakdown(ctx *BifrostContext, resp *BifrostResponse) *BifrostCost
}
