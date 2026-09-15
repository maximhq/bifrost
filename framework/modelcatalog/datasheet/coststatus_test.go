package datasheet

import (
	"math"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func TestCalculateCostWithStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pricing  *configstoreTables.TableModelPricing
		usage    *schemas.BifrostLLMUsage
		amount   *float64
		complete bool
	}{
		{name: "known cost", pricing: schemas.Ptr(chatPricing(0.01, 0.02)), usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}, amount: schemas.Ptr(0.14), complete: true},
		{name: "free model", pricing: schemas.Ptr(chatPricing(0, 0)), usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}, amount: schemas.Ptr(0.0), complete: true},
		{name: "no catalog pricing", usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}},
		{name: "no usage", pricing: schemas.Ptr(chatPricing(0.01, 0.02))},
		{name: "missing all rates", pricing: &configstoreTables.TableModelPricing{}, usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}},
		{name: "partial input cost", pricing: &configstoreTables.TableModelPricing{InputCostPerToken: schemas.Ptr(0.01)}, usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}, amount: schemas.Ptr(0.1)},
		{name: "unpriced output with free input", pricing: &configstoreTables.TableModelPricing{InputCostPerToken: schemas.Ptr(0.0)}, usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}},
		{name: "unused output rate", pricing: &configstoreTables.TableModelPricing{InputCostPerToken: schemas.Ptr(0.01)}, usage: &schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10}, amount: schemas.Ptr(0.1), complete: true},
		{name: "zero usage", pricing: schemas.Ptr(chatPricing(0.01, 0.02)), usage: &schemas.BifrostLLMUsage{}, amount: schemas.Ptr(0.0), complete: true},
		{name: "negative input", pricing: schemas.Ptr(chatPricing(0.01, 0.02)), usage: &schemas.BifrostLLMUsage{PromptTokens: -1, CompletionTokens: 10, TotalTokens: 9}},
		{name: "inconsistent total", pricing: schemas.Ptr(chatPricing(0.01, 0.02)), usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 99}},
		{name: "negative rate", pricing: schemas.Ptr(chatPricing(-0.01, 0.02)), usage: &schemas.BifrostLLMUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}},
		{name: "nonfinite rate", pricing: schemas.Ptr(chatPricing(math.NaN(), 0.02)), usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}},
		{name: "negative audio rate", pricing: &configstoreTables.TableModelPricing{InputCostPerToken: schemas.Ptr(0.01), InputCostPerAudioToken: schemas.Ptr(-0.01)}, usage: &schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10, PromptTokensDetails: &schemas.ChatPromptTokensDetails{AudioTokens: 1}}},
		{name: "provider calculated cost", usage: &schemas.BifrostLLMUsage{Cost: &schemas.BifrostCost{TotalCost: 0.5}}, amount: schemas.Ptr(0.5), complete: true},
		{name: "nonfinite provider cost", usage: &schemas.BifrostLLMUsage{Cost: &schemas.BifrostCost{TotalCost: math.Inf(1)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := map[string]configstoreTables.TableModelPricing{}
			if tc.pricing != nil {
				entries[makeKey("test-model", "test-provider", "chat")] = *tc.pricing
			}
			store := testStoreWithPricing(entries)
			response := makeChatResponse("test-provider", "test-model", tc.usage)
			result := store.CalculateCostWithStatus(response, nil)
			requireCostCalculation(t, result, tc.amount, tc.complete)
			bare := store.CalculateCostForUsageWithStatus(tc.usage, "test-provider", "test-model", schemas.ChatCompletionStreamRequest, nil)
			require.Equal(t, result, bare)
			if result.AmountUSD != nil {
				require.Equal(t, store.CalculateCost(response, nil), *result.AmountUSD)
			}
		})
	}
}

func TestCalculateCostWithStatusCacheComponents(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hit        bool
		direct     bool
		chatPrice  bool
		embedPrice bool
		amount     *float64
		complete   bool
	}{
		{name: "direct hit without catalog", hit: true, direct: true, amount: schemas.Ptr(0.0), complete: true},
		{name: "semantic hit", hit: true, embedPrice: true, amount: schemas.Ptr(0.02), complete: true},
		{name: "semantic hit unpriced", hit: true},
		{name: "miss all priced", chatPrice: true, embedPrice: true, amount: schemas.Ptr(0.16), complete: true},
		{name: "miss unpriced embedding", chatPrice: true, amount: schemas.Ptr(0.14)},
		{name: "miss unpriced model", embedPrice: true, amount: schemas.Ptr(0.02)},
		{name: "miss both unpriced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := map[string]configstoreTables.TableModelPricing{}
			if tc.chatPrice {
				entries[makeKey("test-model", "test-provider", "chat")] = chatPricing(0.01, 0.02)
			}
			if tc.embedPrice {
				entries[makeKey("embedding", "test-provider", "embedding")] = configstoreTables.TableModelPricing{InputCostPerToken: schemas.Ptr(0.001)}
			}
			store := testStoreWithPricing(entries)
			response := makeChatResponse("test-provider", "test-model", &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})
			response.ChatResponse.ExtraFields.CacheDebug = &schemas.BifrostCacheMetadata{CacheHit: tc.hit, ProviderUsed: schemas.Ptr("test-provider"), ModelUsed: schemas.Ptr("embedding"), InputTokens: schemas.Ptr(20)}
			if tc.direct {
				response.ChatResponse.ExtraFields.CacheDebug.HitType = schemas.Ptr("direct")
				store = nil
			}
			result := store.CalculateCostWithStatus(response, nil)
			requireCostCalculation(t, result, tc.amount, tc.complete)
			if result.AmountUSD != nil {
				require.Equal(t, store.CalculateCost(response, nil), *result.AmountUSD)
			}
		})
	}
}

func TestCalculateCostWithStatusSelectedRates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pricing configstoreTables.TableModelPricing
		usage   schemas.BifrostLLMUsage
		tier    *schemas.BifrostServiceTier
		speed   *string
	}{
		{name: "free long context rate", pricing: configstoreTables.TableModelPricing{InputCostPerTokenAbove200kTokens: schemas.Ptr(0.0)}, usage: schemas.BifrostLLMUsage{PromptTokens: 210000, TotalTokens: 210000}},
		{name: "free priority rate", pricing: configstoreTables.TableModelPricing{InputCostPerTokenPriority: schemas.Ptr(0.0)}, usage: schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10}, tier: schemas.Ptr(schemas.BifrostServiceTierPriority)},
		{name: "free flex rate", pricing: configstoreTables.TableModelPricing{InputCostPerTokenFlex: schemas.Ptr(0.0)}, usage: schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10}, tier: schemas.Ptr(schemas.BifrostServiceTierFlex)},
		{name: "free fast rate", pricing: configstoreTables.TableModelPricing{InputCostPerTokenFast: schemas.Ptr(0.0)}, usage: schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10}, speed: schemas.Ptr("fast")},
		{name: "free ultrafast rate", pricing: configstoreTables.TableModelPricing{InputCostPerTokenUltrafast: schemas.Ptr(0.0)}, usage: schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10}, tier: schemas.Ptr(schemas.BifrostServiceTierUltrafast)},
		{name: "free cache read", pricing: configstoreTables.TableModelPricing{CacheReadInputTokenCost: schemas.Ptr(0.0)}, usage: schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10, PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedReadTokens: 10}}},
		{name: "free cache write", pricing: configstoreTables.TableModelPricing{CacheCreationInputTokenCost: schemas.Ptr(0.0)}, usage: schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10, PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedWriteTokens: 10}}},
		{name: "free hour cache write", pricing: configstoreTables.TableModelPricing{CacheCreationInputTokenCostAbove1hr: schemas.Ptr(0.0)}, usage: schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10, PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedWriteTokens: 10, CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{CachedWriteTokens1h: 10}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{makeKey("test-model", "test-provider", "chat"): tc.pricing})
			response := makeChatResponse("test-provider", "test-model", &tc.usage)
			response.ChatResponse.ServiceTier, response.ChatResponse.Speed = tc.tier, tc.speed
			requireCostCalculation(t, store.CalculateCostWithStatus(response, nil), schemas.Ptr(0.0), true)
		})
	}
}

func TestCalculateCostWithStatusMalformedCacheTokens(t *testing.T) {
	for _, details := range []*schemas.ChatPromptTokensDetails{
		{CachedReadTokens: -1},
		{CachedReadTokens: 11},
		{CachedWriteTokens: -1},
		{CachedReadTokens: 5, CachedWriteTokens: 6},
		{CachedWriteTokens: 5, CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{CachedWriteTokens1h: 6}},
	} {
		store := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{makeKey("test-model", "test-provider", "chat"): chatPricing(0.01, 0.02)})
		response := makeChatResponse("test-provider", "test-model", &schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10, PromptTokensDetails: details})
		requireCostCalculation(t, store.CalculateCostWithStatus(response, nil), nil, false)
	}
}

func TestCalculateCostWithStatusUnavailableInput(t *testing.T) {
	var store *Store
	requireCostCalculation(t, store.CalculateCostWithStatus(nil, nil), nil, false)
	requireCostCalculation(t, store.CalculateCostWithStatus(&schemas.BifrostResponse{}, nil), nil, false)
	response := makeChatResponse("test-provider", "test-model", &schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10})
	requireCostCalculation(t, store.CalculateCostWithStatus(response, nil), nil, false)
}

func requireCostCalculation(t *testing.T, result schemas.CostCalculation, amount *float64, complete bool) {
	t.Helper()
	require.Equal(t, complete, result.IsComplete)
	if amount == nil {
		require.Nil(t, result.AmountUSD)
	} else {
		require.NotNil(t, result.AmountUSD)
		require.InDelta(t, *amount, *result.AmountUSD, 1e-12)
	}
}

func TestCalculateCostWithStatusEmbedding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rate     *float64
		usage    schemas.BifrostLLMUsage
		amount   *float64
		complete bool
	}{
		{name: "priced", rate: schemas.Ptr(0.01), usage: schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10}, amount: schemas.Ptr(0.1), complete: true},
		{name: "free", rate: schemas.Ptr(0.0), usage: schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10}, amount: schemas.Ptr(0.0), complete: true},
		{name: "missing rate", usage: schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10}},
		{name: "negative tokens", rate: schemas.Ptr(0.01), usage: schemas.BifrostLLMUsage{PromptTokens: -10, TotalTokens: -10}},
		{name: "output tokens cannot be ignored", rate: schemas.Ptr(0.01), usage: schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{makeKey("embedding", "test-provider", "embedding"): {InputCostPerToken: tc.rate}})
			response := makeEmbeddingResponse("test-provider", "embedding", &tc.usage)
			requireCostCalculation(t, store.CalculateCostWithStatus(response, nil), tc.amount, tc.complete)
			requireCostCalculation(t, store.CalculateCostForUsageWithStatus(&tc.usage, "test-provider", "embedding", schemas.EmbeddingRequest, nil), tc.amount, tc.complete)
		})
	}
}

func TestCalculateCostWithStatusMissingSemanticMetadata(t *testing.T) {
	store := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{makeKey("test-model", "test-provider", "chat"): chatPricing(0.01, 0.02)})
	response := makeChatResponse("test-provider", "test-model", &schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10})
	response.ChatResponse.ExtraFields.CacheDebug = &schemas.BifrostCacheMetadata{CacheHit: true}
	requireCostCalculation(t, store.CalculateCostWithStatus(response, nil), nil, false)
	response.ChatResponse.ExtraFields.CacheDebug = &schemas.BifrostCacheMetadata{CacheHit: false, CacheID: schemas.Ptr("direct-cache-key")}
	requireCostCalculation(t, store.CalculateCostWithStatus(response, nil), schemas.Ptr(0.1), true)
	response.ChatResponse.ExtraFields.CacheDebug.ProviderUsed = schemas.Ptr("test-provider")
	requireCostCalculation(t, store.CalculateCostWithStatus(response, nil), schemas.Ptr(0.1), false)
}

func TestCalculateCostWithStatusOverrideOnlyPricing(t *testing.T) {
	store := newTestStore()
	require.NoError(t, store.SetOverrides([]configstoreTables.TablePricingOverride{{
		ID: "free-user", ScopeKind: string(ScopeKindUser), UserID: schemas.Ptr("user-one"),
		MatchType: string(MatchTypeExact), Pattern: "custom-model", RequestTypes: []schemas.RequestType{schemas.ChatCompletionRequest},
		PricingPatchJSON: `{"input_cost_per_token":0,"output_cost_per_token":0}`,
	}}))
	response := makeChatResponse("test-provider", "custom-model", &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})
	scopes := &LookupScopes{UserID: "user-one"}
	requireCostCalculation(t, store.CalculateCostWithStatus(response, scopes), schemas.Ptr(0.0), true)
	require.Nil(t, scopes.costStatus, "the tracker must not escape into the caller's reusable scopes")
	requireCostCalculation(t, store.CalculateCostWithStatus(response, nil), nil, false)
}

func TestCalculateCostWithStatusUnsupportedModalityRetainsAmount(t *testing.T) {
	store := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{makeKey("reranker", "test-provider", "rerank"): chatPricing(0.01, 0.02)})
	response := makeRerankResponse("test-provider", "reranker", &schemas.BifrostLLMUsage{PromptTokens: 10, TotalTokens: 10})
	requireCostCalculation(t, store.CalculateCostWithStatus(response, nil), schemas.Ptr(0.1), false)
}

func TestCalculateCostWithStatusModelRouterMissingServedModel(t *testing.T) {
	store := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{makeKey("model-router", "azure", "chat"): azureModelRouterPricing()})
	response := makeChatResponse(schemas.Azure, "model-router", &schemas.BifrostLLMUsage{PromptTokens: 10000, TotalTokens: 10000})
	requireCostCalculation(t, store.CalculateCostWithStatus(response, nil), schemas.Ptr(0.0014), false)
}

func TestCalculateCostBreakdownWithStatusPreservesDiscountAndFlatFee(t *testing.T) {
	pricing := chatPricing(0.01, 0.02)
	pricing.OffPeakCostMultiplier = schemas.Ptr(0.5)
	pricing.PeakHours = deepSeekPeakHours()
	pricing.CostPerRequest = schemas.Ptr(0.03)
	store := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{makeKey("test-model", "test-provider", "chat"): pricing})
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}
	response := makeChatResponse("test-provider", "test-model", usage)
	scopes := &LookupScopes{BilledAt: utc(t, "2026-08-17T05:00:00Z")}

	breakdown, calculation := store.CalculateCostBreakdownWithStatus(response, scopes)
	requireCostCalculation(t, calculation, schemas.Ptr(0.1), true)
	require.Equal(t, store.CalculateCostBreakdown(response, scopes), breakdown)
	require.InDelta(t, 0.08, breakdown.InputCost, 1e-12)
	require.InDelta(t, 0.02, breakdown.OutputCost, 1e-12)
	require.InDelta(t, 0.03, breakdown.InputCostDetails.RequestCost, 1e-12)
	require.Equal(t, breakdown.TotalCost, *calculation.AmountUSD)
	bareBreakdown, bareCalculation := store.CalculateCostBreakdownForUsageWithStatus(usage, "test-provider", "test-model", schemas.ChatCompletionStreamRequest, scopes)
	require.Equal(t, breakdown, bareBreakdown)
	require.Equal(t, calculation, bareCalculation)
	require.Nil(t, usage.Cost, "calculating a receipt must not replace client usage with log-owned cost")
	require.Nil(t, scopes.costStatus)
}

func TestCalculateCostBreakdownWithStatusSidecars(t *testing.T) {
	judge := schemas.BifrostGuardrailJudgeCall{JudgeProvider: schemas.Anthropic, JudgeModel: "claude-haiku-4-5", PromptTokens: 30, CompletionTokens: 8, TotalTokens: 38}
	for _, tc := range []struct {
		name      string
		judge     *schemas.BifrostGuardrailJudgeCall
		routing   []schemas.BifrostRoutingCall
		direct    bool
		guardrail float64
		route     float64
		complete  bool
	}{
		{name: "all calls priced", judge: &judge, routing: []schemas.BifrostRoutingCall{embedRoutingCall(200, true), llmRoutingCall(30, 8, true)}, guardrail: 0.00007, route: 0.000074, complete: true},
		{name: "missing judge model", judge: &schemas.BifrostGuardrailJudgeCall{JudgeProvider: schemas.Anthropic, PromptTokens: 30, TotalTokens: 30}},
		{name: "unpriced judge", judge: &schemas.BifrostGuardrailJudgeCall{JudgeProvider: schemas.Anthropic, JudgeModel: "unpriced", PromptTokens: 30, TotalTokens: 30}},
		{name: "missing opted in routing metadata", routing: []schemas.BifrostRoutingCall{{CountTowardBudgets: true}}},
		{name: "excluded routing metadata", routing: []schemas.BifrostRoutingCall{{}}, complete: true},
		{name: "direct hit still bills judge", judge: &judge, direct: true, guardrail: 0.00007, complete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := routingCostTestStore()
			store.pricingData[makeKey("test-model", "test-provider", "chat")] = chatPricing(0.01, 0.02)
			response := makeChatResponse("test-provider", "test-model", &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})
			extra := response.GetExtraFields()
			if tc.judge != nil {
				extra.GuardrailDebug = &schemas.BifrostGuardrailMetadata{JudgeCalls: []schemas.BifrostGuardrailJudgeCall{*tc.judge}}
			}
			extra.RoutingMetadata = &schemas.BifrostRoutingMetadata{Calls: tc.routing}
			base := 0.14
			if tc.direct {
				extra.CacheDebug = &schemas.BifrostCacheMetadata{CacheHit: true, HitType: schemas.Ptr("direct")}
				base = 0
			}
			breakdown, calculation := store.CalculateCostBreakdownWithStatus(response, nil)
			requireCostCalculation(t, calculation, schemas.Ptr(base+tc.guardrail+tc.route), tc.complete)
			require.Equal(t, store.CalculateCostBreakdown(response, nil), breakdown)
			require.Equal(t, *calculation.AmountUSD, breakdown.TotalCost)
			if tc.guardrail+tc.route > 0 {
				require.InDelta(t, tc.guardrail, breakdown.AdditionalCostDetails.GuardrailCost, 1e-12)
				require.InDelta(t, tc.route, breakdown.AdditionalCostDetails.RoutingCost, 1e-12)
			}
		})
	}
}

func TestCalculateCostWithStatusInvalidRequestFee(t *testing.T) {
	for _, fee := range []float64{-0.01, math.Inf(1), math.NaN()} {
		pricing := chatPricing(0.01, 0.02)
		pricing.CostPerRequest = schemas.Ptr(fee)
		store := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{makeKey("test-model", "test-provider", "chat"): pricing})
		response := makeChatResponse("test-provider", "test-model", &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})
		requireCostCalculation(t, store.CalculateCostWithStatus(response, nil), nil, false)
	}
}
