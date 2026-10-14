package datasheet

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// creditsUsage mimics a provider (e.g. Cortecs) that reports usage.cost in its own units.
func creditsUsage() *schemas.BifrostLLMUsage {
	return &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		Cost:             &schemas.BifrostCost{TotalCost: 1067},
	}
}

func providerCostTestStore() *Store {
	return testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})
}

func TestCalculateCost_ProviderCostTrustedByDefault(t *testing.T) {
	s := providerCostTestStore()
	cost := s.CalculateCost(makeChatResponse(schemas.OpenAI, "gpt-4o", creditsUsage()), nil)
	assert.InDelta(t, 1067, cost, 1e-12)
}

func TestCalculateCost_IgnoreProviderCostUsesCatalog(t *testing.T) {
	s := providerCostTestStore()
	s.SetIgnoreProviderCost(schemas.OpenAI, true)

	resp := makeChatResponse(schemas.OpenAI, "gpt-4o", creditsUsage())
	breakdown := s.CalculateCostBreakdown(resp, nil)
	require.NotNil(t, breakdown)
	// 1000*0.000005 + 500*0.000015
	assert.InDelta(t, 0.0125, breakdown.TotalCost, 1e-12)
	assert.InDelta(t, 0.005, breakdown.InputCost, 1e-12)
	assert.InDelta(t, 0.0075, breakdown.OutputCost, 1e-12)
	// The client-facing usage is untouched.
	assert.InDelta(t, 1067, resp.ChatResponse.Usage.Cost.TotalCost, 1e-12)

	s.SetIgnoreProviderCost(schemas.OpenAI, false)
	assert.InDelta(t, 1067, s.CalculateCost(resp, nil), 1e-12)
}

func TestCalculateCost_IgnoreProviderCostIsPerProvider(t *testing.T) {
	s := providerCostTestStore()
	s.SetIgnoreProviderCost(schemas.Anthropic, true)
	cost := s.CalculateCost(makeChatResponse(schemas.OpenAI, "gpt-4o", creditsUsage()), nil)
	assert.InDelta(t, 1067, cost, 1e-12)
}

func TestCalculateCost_IgnoreProviderCostAppliesOverride(t *testing.T) {
	s := providerCostTestStore()
	s.SetIgnoreProviderCost(schemas.OpenAI, true)
	providerID := string(schemas.OpenAI)
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{{
		ID:               "cortecs-style-override",
		ScopeKind:        string(ScopeKindProvider),
		ProviderID:       &providerID,
		MatchType:        string(MatchTypeExact),
		Pattern:          "gpt-4o",
		RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
		PricingPatchJSON: `{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}`,
	}}))

	cost := s.CalculateCost(makeChatResponse(schemas.OpenAI, "gpt-4o", creditsUsage()), &LookupScopes{Provider: string(schemas.OpenAI)})
	// 1000*0.000001 + 500*0.000002
	assert.InDelta(t, 0.002, cost, 1e-12)
}

func TestCalculateCostBreakdownForUsage_IgnoreProviderCost(t *testing.T) {
	s := providerCostTestStore()
	assert.InDelta(t, 1067, s.CalculateCostBreakdownForUsage(creditsUsage(), schemas.OpenAI, "gpt-4o", schemas.ChatCompletionRequest, nil).TotalCost, 1e-12)

	s.SetIgnoreProviderCost(schemas.OpenAI, true)
	bd := s.CalculateCostBreakdownForUsage(creditsUsage(), schemas.OpenAI, "gpt-4o", schemas.ChatCompletionRequest, nil)
	require.NotNil(t, bd)
	assert.InDelta(t, 0.0125, bd.TotalCost, 1e-12)
}

func TestCalculateBatchCostDetailsForUsage_IgnoreProviderCost(t *testing.T) {
	s := providerCostTestStore()
	s.SetIgnoreProviderCost(schemas.OpenAI, true)
	details := s.CalculateBatchCostDetailsForUsage(creditsUsage(), schemas.OpenAI, "gpt-4o", schemas.ChatCompletionRequest, nil)
	assert.False(t, details.ProviderCostUsed)
	assert.True(t, details.Priced)
	assert.Less(t, details.Cost, 1.0)
}

func TestCalculateVideoCostDetails_IgnoreProviderCost(t *testing.T) {
	store := newVideoDimensionTestStore(t)
	store.SetIgnoreProviderCost(schemas.OpenAI, true)
	seconds := 8
	providerCost := 0.42
	details := store.CalculateVideoCostDetails(VideoPricingDimensions{
		Model:        "sora-2-pro",
		RequestType:  schemas.VideoGenerationRequest,
		Seconds:      &seconds,
		Size:         "1920x1080",
		OutputCount:  1,
		ProviderCost: &providerCost,
	}, schemas.OpenAI, nil)
	assert.False(t, details.ProviderCostUsed)
	assert.True(t, details.Priced)
	assert.NotEqual(t, 0.42, details.Cost)
}

func TestReplaceIgnoreProviderCost(t *testing.T) {
	s := newTestStore()
	s.SetIgnoreProviderCost(schemas.OpenAI, true)
	s.ReplaceIgnoreProviderCost([]schemas.ModelProvider{schemas.Anthropic})
	assert.False(t, s.IsProviderCostIgnored(schemas.OpenAI))
	assert.True(t, s.IsProviderCostIgnored(schemas.Anthropic))
}

// A completed video that carries both a provider cost and a duration must still
// price from the catalog when the provider's cost is ignored.
func TestCalculateCost_IgnoreProviderCostPricesVideoFromDuration(t *testing.T) {
	store := newVideoDimensionTestStore(t)
	seconds := "8"
	resp := &schemas.BifrostResponse{
		VideoGenerationResponse: &schemas.BifrostVideoGenerationResponse{
			Status:  schemas.VideoStatusCompleted,
			Seconds: &seconds,
			Size:    "1920x1080",
			Usage:   &schemas.VideoUsage{Cost: &schemas.BifrostCost{TotalCost: 0.42}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.VideoGenerationRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "sora-2-pro"),
			},
		},
	}
	assert.InDelta(t, 0.42, store.CalculateCost(resp, nil), 1e-12)

	store.SetIgnoreProviderCost(schemas.OpenAI, true)
	cost := store.CalculateCost(resp, nil)
	assert.Positive(t, cost)
	assert.NotEqual(t, 0.42, cost)
}

// With the provider cost ignored and no catalog price for the model, the model
// call itself contributes nothing, but a budget-counted routing call is still
// charged. The provider docs state this exception.
func TestCalculateCost_IgnoreProviderCostUnpricedModelKeepsRoutingCost(t *testing.T) {
	s := routingCostTestStore()
	provider := schemas.ModelProvider("cortecs")
	s.SetIgnoreProviderCost(provider, true)

	resp := makeChatResponse(provider, "unpriced-model", creditsUsage())
	assert.Nil(t, s.CalculateCostBreakdown(resp, nil), "unpriced model with ignored provider cost has no cost of its own")

	resp.ChatResponse.ExtraFields.RoutingMetadata = &schemas.BifrostRoutingMetadata{
		Calls: []schemas.BifrostRoutingCall{embedRoutingCall(200, true)},
	}
	assert.InDelta(t, 200*0.00000002, s.CalculateCost(resp, nil), 1e-12)
}

func TestGetBasePricing_FallsBackToLowercaseModel(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4.1-flash", "azure", "chat"): chatPricing(0.0000003, 0.0000012),
	})
	resp := makeChatResponse(schemas.Azure, "DeepSeek-V4.1-Flash", &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
	})
	// 1000*0.0000003 + 500*0.0000012
	assert.InDelta(t, 0.0009, s.CalculateCost(resp, nil), 1e-12)
}

func TestGetBasePricing_ExactCaseWinsOverLowercase(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("deepseek-v4.1-flash", "azure", "chat"): chatPricing(0.0000003, 0.0000012),
		makeKey("DeepSeek-V4.1-Flash", "azure", "chat"): chatPricing(0.000001, 0.000002),
	})
	resp := makeChatResponse(schemas.Azure, "DeepSeek-V4.1-Flash", &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
	})
	// 1000*0.000001 + 500*0.000002
	assert.InDelta(t, 0.002, s.CalculateCost(resp, nil), 1e-12)
}
