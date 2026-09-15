package datasheet

import (
	"math"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// costStatus is local to one calculation. LookupScopes copies carry its pointer
// through cache and fallback pricing without changing the legacy float API.
type costStatus struct {
	incomplete bool
	invalid    bool
}

// CalculateCostWithStatus returns the total from the same breakdown calculation
// used by the logging and billing paths, together with its completeness.
func (s *Store) CalculateCostWithStatus(result *schemas.BifrostResponse, scopes *LookupScopes) schemas.CostCalculation {
	_, calculation := s.CalculateCostBreakdownWithStatus(result, scopes)
	return calculation
}

// CalculateCostBreakdownWithStatus computes the breakdown and completeness in
// one pass. The breakdown is unchanged from CalculateCostBreakdown; callers
// must check AmountUSD before treating its total as an established cost.
func (s *Store) CalculateCostBreakdownWithStatus(result *schemas.BifrostResponse, scopes *LookupScopes) (*schemas.BifrostCost, schemas.CostCalculation) {
	tracked := trackedCostScopes(scopes)
	breakdown := s.CalculateCostBreakdown(result, &tracked)
	return breakdown, tracked.costStatus.breakdownResult(breakdown)
}

// CalculateCostForUsageWithStatus prices bare usage, preserving a known partial
// amount when only some required pricing is available.
func (s *Store) CalculateCostForUsageWithStatus(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *LookupScopes) schemas.CostCalculation {
	_, calculation := s.CalculateCostBreakdownForUsageWithStatus(usage, provider, model, requestType, scopes)
	return calculation
}

// CalculateCostBreakdownForUsageWithStatus prices bare usage once and returns
// both its breakdown and whether the total can be established completely.
func (s *Store) CalculateCostBreakdownForUsageWithStatus(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *LookupScopes) (*schemas.BifrostCost, schemas.CostCalculation) {
	tracked := trackedCostScopes(scopes)
	breakdown := s.CalculateCostBreakdownForUsage(usage, provider, model, requestType, &tracked)
	return breakdown, tracked.costStatus.breakdownResult(breakdown)
}

func (status *costStatus) breakdownResult(breakdown *schemas.BifrostCost) schemas.CostCalculation {
	var amount float64
	if breakdown != nil {
		amount = breakdown.TotalCost
	}
	return status.result(amount)
}

func trackedCostScopes(scopes *LookupScopes) LookupScopes {
	var tracked LookupScopes
	if scopes != nil {
		tracked = *scopes
	}
	tracked.costStatus = &costStatus{}
	return tracked
}

func (status *costStatus) missing() {
	if status != nil {
		status.incomplete = true
	}
}

func (status *costStatus) invalidate() {
	if status != nil {
		status.invalid = true
	}
}

func (status *costStatus) result(amount float64) schemas.CostCalculation {
	if status.invalid || !validCostValue(amount) || (status.incomplete && amount == 0) {
		return schemas.CostCalculation{}
	}
	return schemas.CostCalculation{AmountUSD: &amount, IsComplete: !status.incomplete}
}

func validCostValue(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (status *costStatus) rate(value *float64) {
	if value == nil {
		status.missing()
	} else if !validCostValue(*value) {
		status.invalid = true
	}
}

func (status *costStatus) validateUsage(usage *schemas.BifrostLLMUsage) bool {
	if usage == nil {
		status.missing()
		return false
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 ||
		usage.PromptTokens > usage.TotalTokens || usage.TotalTokens-usage.PromptTokens != usage.CompletionTokens {
		status.invalid = true
		return false
	}
	return true
}

func (status *costStatus) validateInput(pricing *configstoreTables.TableModelPricing, input costInput, requestType schemas.RequestType) {
	if pricing.CostPerRequest != nil {
		status.rate(pricing.CostPerRequest)
	}
	switch requestType {
	case schemas.ChatCompletionRequest, schemas.TextCompletionRequest, schemas.ResponsesRequest, schemas.CompactionRequest:
		status.validateText(pricing, input.usage, input.tier)
	case schemas.EmbeddingRequest:
		if status.validateUsage(input.usage) {
			if input.usage.CompletionTokens != 0 {
				status.invalid = true
			}
			if input.usage.PromptTokens > 0 {
				status.rate(tieredInputRateValue(pricing, input.usage.PromptTokens, input.tier))
			}
		}
	default:
		// Other modalities retain their existing amount, but require independent
		// unit/rate completeness validation before being declared complete.
		status.missing()
	}
}

func (status *costStatus) validateText(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) {
	if !status.validateUsage(usage) {
		return
	}
	prompt := usage.PromptTokens
	read, write, write1h := 0, 0, 0
	if details := usage.PromptTokensDetails; details != nil {
		read, write = details.CachedReadTokens, details.CachedWriteTokens
		if read < 0 || write < 0 || read > prompt || write > prompt-read ||
			details.TextTokens < 0 || details.TextTokens > prompt ||
			details.AudioTokens < 0 || details.AudioTokens > prompt ||
			details.ImageTokens < 0 || details.ImageTokens > prompt {
			status.invalid = true
			return
		}
		if cached := details.CachedWriteTokenDetails; cached != nil {
			write1h = cached.CachedWriteTokens1h
			if write1h < 0 || write1h > write || cached.CachedWriteTokens5m < 0 || cached.CachedWriteTokens5m > write-write1h {
				status.invalid = true
				return
			}
		}
		if details.AudioTokens > 0 || details.ImageTokens > 0 {
			status.missing()
		}
		if details.AudioTokens > 0 && pricing.InputCostPerAudioToken != nil {
			status.rate(pricing.InputCostPerAudioToken)
		}
	}
	if prompt-read-write > 0 {
		status.rate(tieredInputRateValue(pricing, prompt, tier))
	}
	if usage.CompletionTokens > 0 {
		status.rate(tieredOutputRateValue(pricing, prompt, tier))
	}
	if read > 0 {
		status.rate(tieredCacheReadInputTokenRateValue(pricing, prompt, tier))
	}
	if write-write1h > 0 {
		status.rate(tieredCacheCreationInputTokenRateValue(pricing, prompt, tier))
	}
	if write1h > 0 {
		status.rate(tieredCacheCreationInputAbove1hrTokenRateValue(pricing, prompt, tier))
	}
	if tier.inferenceGeoUS {
		status.rate(pricing.InferenceGeoUSMultiplier)
	}
	if details := usage.CompletionTokensDetails; details != nil {
		if details.AudioTokens < 0 || details.AudioTokens > usage.CompletionTokens ||
			details.ReasoningTokens < 0 || details.ReasoningTokens > usage.CompletionTokens {
			status.invalid = true
		}
		if details.AudioTokens > 0 || details.ImageTokens != nil && *details.ImageTokens > 0 {
			status.missing()
		}
		if details.AudioTokens > 0 && pricing.OutputCostPerAudioToken != nil {
			status.rate(pricing.OutputCostPerAudioToken)
		}
		if details.ImageTokens != nil && (*details.ImageTokens < 0 || *details.ImageTokens > usage.CompletionTokens) {
			status.invalid = true
		}
		if queries := details.NumSearchQueries; queries != nil {
			if *queries < 0 {
				status.invalid = true
			} else if *queries > 0 {
				status.rate(pricing.SearchContextCostPerQuery)
			}
		}
	}
}
