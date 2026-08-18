package runware

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ExtractRunwarePassthroughUsage reads Runware's per-task cost from a passthrough response body and
// surfaces it as a provider-reported cost. Runware includes a `cost` field on each element of the
// top-level `data` array when the request sets includeCost:true; the costs are summed across tasks.
//
// Returns nil when no cost is present (includeCost not set, or an error/queued response), so billing
// falls through to $0 — there is no datasheet rate for arbitrary passthrough task types.
func ExtractRunwarePassthroughUsage(body []byte) *schemas.BifrostPassthroughUsage {
	costs := providerUtils.GetJSONField(body, "data.#.cost")
	if !costs.Exists() {
		return nil
	}
	var total float64
	for _, c := range costs.Array() {
		total += c.Float()
	}
	if total <= 0 {
		return nil
	}
	return &schemas.BifrostPassthroughUsage{Cost: &schemas.BifrostCost{TotalCost: total}}
}
