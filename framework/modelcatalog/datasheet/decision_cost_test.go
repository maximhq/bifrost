package datasheet

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
)

// TestCalculateCostForUsage_TypesafeDecision pins that a decision is priced
// from the datasheet's "decisions" row (typesafe/jev-1.13.0: $0.042 per million
// input tokens, output free), so its spend is nonzero and covers input only.
func TestCalculateCostForUsage_TypesafeDecision(t *testing.T) {
	s := newTestStore()
	s.pricingData[makeKey("jev-1.13.0", "typesafe", "decisions")] = configstoreTables.TableModelPricing{
		Model: "jev-1.13.0", Provider: "typesafe", Mode: "decisions",
		InputCostPerToken:  bifrost.Ptr(4.2e-8),
		OutputCostPerToken: bifrost.Ptr(0.0),
	}
	usage := &schemas.BifrostLLMUsage{PromptTokens: 296, CompletionTokens: 20, TotalTokens: 316}
	got := s.CalculateCostForUsage(usage, schemas.Typesafe, "jev-1.13.0", schemas.DecisionRequest, nil)
	assert.InDelta(t, 296*4.2e-8, got, 1e-15)
	assert.Greater(t, got, 0.0)

	// A response without usage costs nothing.
	assert.Equal(t, 0.0, s.CalculateCostForUsage(nil, schemas.Typesafe, "jev-1.13.0", schemas.DecisionRequest, nil))
}

// TestCalculateCost_CreditPricedDecision pins that a hosted decision priced
// from credits keeps its provider-computed USD cost instead of token rates.
func TestCalculateCost_CreditPricedDecision(t *testing.T) {
	s := newTestStore()
	s.pricingData[makeKey("jev-1.13.0", "typesafe", "decisions")] = configstoreTables.TableModelPricing{
		Model: "jev-1.13.0", Provider: "typesafe", Mode: "decisions",
		InputCostPerToken:  bifrost.Ptr(4.2e-8),
		OutputCostPerToken: bifrost.Ptr(0.0),
	}
	usage := &schemas.BifrostLLMUsage{
		PromptTokens: 296, TotalTokens: 296, CreditCostUSD: "0.031",
		Cost: &schemas.BifrostCost{RequestCost: 0.031, TotalCost: 0.031},
	}
	assert.Equal(t, 0.031, s.CalculateCostForUsage(usage, schemas.Typesafe, "jev-1.13.0", schemas.DecisionRequest, nil))

	resp := &schemas.BifrostResponse{DecisionResponse: &schemas.BifrostDecisionResponse{Model: "jev-1.13.0", Usage: usage}}
	resp.PopulateExtraFields(schemas.DecisionRequest, schemas.Typesafe, "jev-1.13.0", "jev-1.13.0")
	assert.Equal(t, 0.031, s.CalculateCost(resp, nil))

	// Zero credits cost zero rather than falling back to token rates.
	zero := &schemas.BifrostLLMUsage{PromptTokens: 296, TotalTokens: 296, CreditCostUSD: "0", Cost: &schemas.BifrostCost{}}
	assert.Equal(t, 0.0, s.CalculateCostForUsage(zero, schemas.Typesafe, "jev-1.13.0", schemas.DecisionRequest, nil))
}
