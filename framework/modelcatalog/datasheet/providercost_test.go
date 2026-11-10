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
		makeKeyNormalized("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
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
		makeKeyNormalized("deepseek-v4.1-flash", "azure", "chat"): chatPricing(0.0000003, 0.0000012),
	})
	resp := makeChatResponse(schemas.Azure, "DeepSeek-V4.1-Flash", &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
	})
	// 1000*0.0000003 + 500*0.0000012
	assert.InDelta(t, 0.0009, s.CalculateCost(resp, nil), 1e-12)
}

// azureChatRow builds a row carrying its own identity, the shape setPricingRowsUnsafe resolves.
func azureChatRow(model string, input, output float64) configstoreTables.TableModelPricing {
	row := chatPricing(input, output)
	row.Model, row.Provider, row.Mode = model, "azure", "chat"
	return row
}

// The live feed lists azure/deepseek-v4-pro and azure/DeepSeek-V4-Pro with
// different rates. Every casing prices from the lowercase row, whichever row
// the database returned first.
func TestGetBasePricing_CaseVariantRowsPriceFromLowercaseRow(t *testing.T) {
	lower := azureChatRow("deepseek-v4-pro", 0.0000003, 0.0000012)
	mixed := azureChatRow("DeepSeek-V4-Pro", 0.000001, 0.000002)
	for _, rows := range [][]configstoreTables.TableModelPricing{{lower, mixed}, {mixed, lower}} {
		s := newTestStore()
		s.setPricingRowsUnsafe(rows)
		for _, model := range []string{"deepseek-v4-pro", "DeepSeek-V4-Pro", "DEEPSEEK-V4-PRO"} {
			resp := makeChatResponse(schemas.Azure, model, &schemas.BifrostLLMUsage{
				PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
			})
			// 1000*0.0000003 + 500*0.0000012
			assert.InDelta(t, 0.0009, s.CalculateCost(resp, nil), 1e-12, model)
		}
	}
}

func TestGetBasePricing_LowercaseModelMatchesMixedCaseRow(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKeyNormalized("Qwen3-235B-A22B", "azure", "chat"): chatPricing(0.0000003, 0.0000012),
	})
	resp := makeChatResponse(schemas.Azure, "qwen3-235b-a22b", &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
	})
	// 1000*0.0000003 + 500*0.0000012
	assert.InDelta(t, 0.0009, s.CalculateCost(resp, nil), 1e-12)
}

func TestGetBasePricing_CaseInsensitiveOnVertexPrefixFallback(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKeyNormalized("gemini-2.5-pro", "vertex", "chat"): chatPricing(0.0000003, 0.0000012),
	})
	resp := makeChatResponse(schemas.Vertex, "google/Gemini-2.5-Pro", &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
	})
	// 1000*0.0000003 + 500*0.0000012
	assert.InDelta(t, 0.0009, s.CalculateCost(resp, nil), 1e-12)
}

func BenchmarkGetBasePricing_Casing(b *testing.B) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKeyNormalized("deepseek-v4.1-flash", "azure", "chat"): chatPricing(0.0000003, 0.0000012),
		makeKeyNormalized("Qwen3-235B-A22B", "azure", "chat"):     chatPricing(0.0000003, 0.0000012),
	})
	for _, tc := range []struct{ name, model string }{
		{"exact", "deepseek-v4.1-flash"},
		{"mixed_request_lower_row", "DeepSeek-V4.1-Flash"},
		{"lower_request_mixed_row", "qwen3-235b-a22b"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				s.getBasePricing(tc.model, "azure", schemas.ChatCompletionRequest)
			}
		})
	}
}

func TestSetPricingRows_PicksLowercaseThenSmallest(t *testing.T) {
	titled, upper, lower := azureChatRow("Model-X", 1, 1), azureChatRow("MODEL-X", 2, 2), azureChatRow("model-x", 3, 3)
	key := makeKeyNormalized("model-x", "azure", "chat")
	for _, tc := range []struct {
		rows []configstoreTables.TableModelPricing
		want string
	}{
		{[]configstoreTables.TableModelPricing{titled, upper}, "MODEL-X"},
		{[]configstoreTables.TableModelPricing{upper, titled}, "MODEL-X"},
		{[]configstoreTables.TableModelPricing{titled, upper, lower}, "model-x"},
		{[]configstoreTables.TableModelPricing{lower, upper, titled}, "model-x"},
	} {
		s := newTestStore()
		s.setPricingRowsUnsafe(tc.rows)
		assert.Equal(t, tc.want, s.pricingData[key].Model)
		assert.Len(t, s.pricingData, 1)
	}
}

// Rows with the same model name can still differ by provider or mode casing, and
// the database returns them in no fixed order.
func TestSetPricingRows_ProviderAndModeCasingPicksLowercaseRow(t *testing.T) {
	lower := azureChatRow("model-x", 1, 1)
	upperProvider := azureChatRow("model-x", 2, 2)
	upperProvider.Provider = "Azure"
	upperMode := azureChatRow("model-x", 3, 3)
	upperMode.Mode = "Chat"
	key := makeKeyNormalized("model-x", "azure", "chat")
	for _, tc := range []struct {
		rows         []configstoreTables.TableModelPricing
		wantProvider string
		wantMode     string
	}{
		{[]configstoreTables.TableModelPricing{lower, upperProvider, upperMode}, "azure", "chat"},
		{[]configstoreTables.TableModelPricing{upperMode, upperProvider, lower}, "azure", "chat"},
		{[]configstoreTables.TableModelPricing{upperProvider, upperMode}, "Azure", "chat"},
		{[]configstoreTables.TableModelPricing{upperMode, upperProvider}, "Azure", "chat"},
	} {
		s := newTestStore()
		s.setPricingRowsUnsafe(tc.rows)
		assert.Equal(t, tc.wantProvider, s.pricingData[key].Provider)
		assert.Equal(t, tc.wantMode, s.pricingData[key].Mode)
	}
}

// Every fallback in getBasePricing reads through makeKeyNormalized, so each one must find
// a row whose casing differs from the request. Each row carries its
// own input rate so the test also proves which fallback answered.
func TestGetBasePricing_CaseInsensitiveOnEveryFallback(t *testing.T) {
	row := func(model, provider, mode string, input float64) (string, configstoreTables.TableModelPricing) {
		p := chatPricing(input, input)
		p.Model, p.Provider, p.Mode = model, provider, mode
		return makeKeyNormalized(model, provider, mode), p
	}
	entries := map[string]configstoreTables.TableModelPricing{}
	for _, r := range []struct {
		model, provider, mode string
		input                 float64
	}{
		{"anthropic.Claude-Sonnet-4", "bedrock", "chat", 1},
		{"anthropic.Claude-Haiku-4", "bedrock", "responses", 2},
		{"Gemini-2.5-Flash", "vertex", "chat", 3},
		{"Gemini-2.5-Flash-Lite", "vertex", "responses", 4},
		{"GPT-5-Pro", "openai", "responses", 5},
		{"GPT-Realtime", "openai", "realtime", 6},
		{"GPT-Live", "openai", "realtime", 7},
		{"Imagen-4", "vertex", "image_generation", 8},
		{"Gemini-2.5-Flash-Image", "gemini", "image_generation", 9},
		{"Container-1G", "openai", "chat", 10},
		{"Container", "azure", "chat", 11},
		{"openai.gpt-oss-120b", "bedrock", "chat", 12},
		{"google.gemma-3-27b", "bedrock", "chat", 13},
		{"xai.grok-4", "bedrock", "chat", 14},
	} {
		k, v := row(r.model, r.provider, r.mode, r.input)
		entries[k] = v
	}
	s := testStoreWithPricing(entries)

	for _, tc := range []struct {
		name, model, provider string
		requestType           schemas.RequestType
		want                  float64
	}{
		{"bedrock vendor prefix", "claude-sonnet-4", "bedrock", schemas.ChatCompletionRequest, 1},
		{"bedrock_mantle folds onto bedrock", "claude-sonnet-4", "bedrock_mantle", schemas.ChatCompletionRequest, 1},
		{"bedrock vendor prefix in counterpart mode", "claude-haiku-4", "bedrock", schemas.ChatCompletionRequest, 2},
		{"bedrock vendor prefix for mixed-case anthropic request", "Claude-Sonnet-4", "bedrock", schemas.ChatCompletionRequest, 1},
		{"bedrock vendor prefix for mixed-case openai request", "GPT-OSS-120B", "bedrock", schemas.ChatCompletionRequest, 12},
		{"bedrock vendor prefix for mixed-case google request", "Gemma-3-27B", "bedrock", schemas.ChatCompletionRequest, 13},
		{"bedrock vendor prefix for mixed-case xai request", "Grok-4", "bedrock", schemas.ChatCompletionRequest, 14},
		{"mixed-case request already carrying the namespace", "Anthropic.Claude-Sonnet-4", "bedrock", schemas.ChatCompletionRequest, 1},
		{"gemini retries under vertex", "gemini-2.5-flash", "gemini", schemas.ChatCompletionRequest, 3},
		{"gemini under vertex in counterpart mode", "gemini-2.5-flash-lite", "gemini", schemas.ChatCompletionRequest, 4},
		{"chat falls back to responses row", "gpt-5-pro", "openai", schemas.ChatCompletionRequest, 5},
		{"realtime row", "gpt-realtime", "openai", schemas.RealtimeRequest, 6},
		{"live falls back to realtime row", "gpt-live", "openai", schemas.LiveRequest, 7},
		{"image edit falls back to image generation", "imagen-4", "vertex", schemas.ImageEditRequest, 8},
		{"vertex publisher form image fallback", "google/imagen-4", "vertex", schemas.ChatCompletionRequest, 8},
		{"image model over chat", "gemini-2.5-flash-image", "gemini", schemas.ChatCompletionRequest, 9},
		{"container create falls back to chat row", "container-1g", "openai", schemas.ContainerCreateRequest, 10},
		{"container create falls back to base container", "container-4g", "azure", schemas.ContainerCreateRequest, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pricing, ok := s.getBasePricing(tc.model, tc.provider, tc.requestType)
			require.True(t, ok, "expected %s on %s to be priced", tc.model, tc.provider)
			require.NotNil(t, pricing.InputCostPerToken)
			assert.Equal(t, tc.want, *pricing.InputCostPerToken)
		})
	}
}

// TestCalculateCostForUsage_EstimatedUsagePrecedence pins how a cancelled stream's
// billed usage is priced: a usable provider-reported cost wins, and only without
// one are the tokens (provider-reported or locally estimated) priced at catalog
// rates, exactly as reported tokens would be.
func TestCalculateCostForUsage_EstimatedUsagePrecedence(t *testing.T) {
	s := providerCostTestStore()
	estimated := func(cost *schemas.BifrostCost) *schemas.BifrostLLMUsage {
		return &schemas.BifrostLLMUsage{
			PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
			Estimation: &schemas.TokenUsageEstimation{Prompt: true, Completion: true, Tokenizer: "o200k_base"},
			Cost:       cost,
		}
	}
	price := func(u *schemas.BifrostLLMUsage) *schemas.BifrostCost {
		return s.CalculateCostBreakdownForUsage(u, schemas.OpenAI, "gpt-4o", schemas.ChatCompletionStreamRequest, nil)
	}

	// No provider cost: estimated tokens are priced like reported ones.
	bd := price(estimated(nil))
	require.NotNil(t, bd)
	assert.InDelta(t, 0.005, bd.InputCost, 1e-12)
	assert.InDelta(t, 0.0075, bd.OutputCost, 1e-12)
	assert.InDelta(t, 0.0125, bd.TotalCost, 1e-12)
	reported := &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}
	assert.Equal(t, s.CalculateCostForUsage(reported, schemas.OpenAI, "gpt-4o", schemas.ChatCompletionStreamRequest, nil), bd.TotalCost)

	// A provider-reported cost is preferred over pricing the estimate.
	bd = price(estimated(&schemas.BifrostCost{TotalCost: 1067}))
	require.NotNil(t, bd)
	assert.InDelta(t, 1067, bd.TotalCost, 1e-12)

	// ignore_provider_cost falls back to the catalog price of the estimate.
	s.SetIgnoreProviderCost(schemas.OpenAI, true)
	t.Cleanup(func() { s.SetIgnoreProviderCost(schemas.OpenAI, false) })
	bd = price(estimated(&schemas.BifrostCost{TotalCost: 1067}))
	require.NotNil(t, bd)
	assert.InDelta(t, 0.0125, bd.TotalCost, 1e-12)
}
