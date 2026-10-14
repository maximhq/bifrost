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

	// A free idempotent replay carries no usage and costs nothing.
	assert.Equal(t, 0.0, s.CalculateCostForUsage(nil, schemas.Typesafe, "jev-1.13.0", schemas.DecisionRequest, nil))
}
