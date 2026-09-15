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
	attempts := make(map[string]*logstore.Log, len(entries))
	for _, entry := range entries {
		if entry == nil {
			costs.IsComplete = false
			continue
		}
		attempts[entry.ID] = entry
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
	// A prior completed attempt can expire while a fallback keeps streaming.
	// Validate each parent's sequence after request-ID deduplication so another
	// request, or a repeated record, cannot hide a missing billed attempt.
	sequences := make(map[string]map[int]struct{})
	for _, entry := range attempts {
		parentID := entry.ID
		if entry.FallbackIndex < 0 {
			costs.IsComplete = false
			continue
		}
		if entry.FallbackIndex > 0 {
			if entry.ParentRequestID == nil || *entry.ParentRequestID == "" {
				costs.IsComplete = false
				continue
			}
			parentID = *entry.ParentRequestID
		}
		if sequences[parentID] == nil {
			sequences[parentID] = make(map[int]struct{})
		}
		sequence := sequences[parentID]
		if _, exists := sequence[entry.FallbackIndex]; exists {
			costs.IsComplete = false
		}
		sequence[entry.FallbackIndex] = struct{}{}
	}
	for _, sequence := range sequences {
		for index := range sequence {
			// N distinct nonnegative indices cover 0..N-1 only if all are < N.
			if index >= len(sequence) {
				costs.IsComplete = false
			}
		}
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
