package logging

import (
	"math"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// requestCostsSnapshot copies the amounts already calculated for logging. The
// trace-scoped pending logs own the ledger; contexts carry only the trace ID.
// Exact request IDs identify attempts within that scope, including fallbacks.
func (p *LoggerPlugin) requestCostsSnapshot(entries []*logstore.Log) *schemas.RequestCosts {
	if !p.includeRequestCosts {
		return nil
	}
	costs := &schemas.RequestCosts{Version: 1, Currency: "USD", IsComplete: len(entries) > 0, Requests: make([]schemas.RequestCost, 0, len(entries))}
	indices := make(map[string]int, len(entries))
	for _, entry := range entries {
		if entry == nil {
			costs.IsComplete = false
			continue
		}
		cost := schemas.RequestCost{
			RequestID: entry.ID, Provider: entry.Provider, Model: entry.Model,
			IsComplete: entry.CostIsComplete && entry.NumberOfRetries == 0 && entry.ID != "",
		}
		if entry.ParentRequestID != nil {
			cost.ParentRequestID = *entry.ParentRequestID
		}
		if entry.Cost != nil && *entry.Cost >= 0 && !math.IsNaN(*entry.Cost) && !math.IsInf(*entry.Cost, 0) {
			amount := *entry.Cost
			cost.AmountUSD = &amount
		} else {
			cost.IsComplete = false
		}
		if index, exists := indices[entry.ID]; exists {
			costs.Requests[index] = cost
		} else {
			indices[entry.ID] = len(costs.Requests)
			costs.Requests = append(costs.Requests, cost)
		}
	}
	for _, cost := range costs.Requests {
		costs.IsComplete = costs.IsComplete && cost.IsComplete
	}
	return costs
}

func attachRequestCosts(result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, costs *schemas.RequestCosts) {
	if result != nil {
		if extra := result.GetExtraFields(); extra != nil {
			extra.RequestCosts = costs
		}
	}
	if bifrostErr != nil {
		bifrostErr.ExtraFields.RequestCosts = costs
	}
}
