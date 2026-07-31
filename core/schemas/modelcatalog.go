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
}
