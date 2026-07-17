package datasheet

import (
	"encoding/json"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// chatPricing returns a TableModelPricing with the given per-token rates.
func chatPricing(input, output float64) configstoreTables.TableModelPricing {
	return configstoreTables.TableModelPricing{
		Model:              "test-model",
		Provider:           "test-provider",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(input),
		OutputCostPerToken: bifrost.Ptr(output),
	}
}

// testStoreWithPricing creates a catalog pre-loaded with the given pricing entries.
func testStoreWithPricing(entries map[string]configstoreTables.TableModelPricing) *Store {
	s := newTestStore()

	for k, v := range entries {
		s.pricingData[k] = v
	}
	return s
}

// routingInfoFor builds a minimal RoutingInfo populated by core.bifrost for a
// non-aliased request — the form pricing reads from.
func routingInfoFor(provider schemas.ModelProvider, model string) schemas.RoutingInfo {
	return schemas.RoutingInfo{Provider: provider, Model: model}
}

// makeChatResponse builds a minimal BifrostResponse for a chat completion.
func makeChatResponse(provider schemas.ModelProvider, model string, usage *schemas.BifrostLLMUsage) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: routingInfoFor(provider, model),
			},
		},
	}
}

// makeEmbeddingResponse builds a minimal BifrostResponse for an embedding request.
func makeEmbeddingResponse(provider schemas.ModelProvider, model string, usage *schemas.BifrostLLMUsage) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		EmbeddingResponse: &schemas.BifrostEmbeddingResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.EmbeddingRequest,
				RoutingInfo: routingInfoFor(provider, model),
			},
		},
	}
}

// makeRerankResponse builds a minimal BifrostResponse for a rerank request.
func makeRerankResponse(provider schemas.ModelProvider, model string, usage *schemas.BifrostLLMUsage) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		RerankResponse: &schemas.BifrostRerankResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.RerankRequest,
				RoutingInfo: routingInfoFor(provider, model),
			},
		},
	}
}

// makeImageResponse builds a minimal BifrostResponse for an image generation request.
func makeImageResponse(provider schemas.ModelProvider, model string, usage *schemas.ImageUsage) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ImageGenerationResponse: &schemas.BifrostImageGenerationResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ImageGenerationRequest,
				RoutingInfo: routingInfoFor(provider, model),
			},
		},
	}
}

func derefF(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// =========================================================================
// 1. computeTextCost — unit tests (pure function, no catalog)
// =========================================================================

func TestComputeTextCost_BasicInputOutput(t *testing.T) {
	// GPT-4o: $5/M input, $15/M output
	p := chatPricing(0.000005, 0.000015)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}
	cost := computeTextCost(&p, usage, serviceTier{})
	// 1000 * 0.000005 + 500 * 0.000015 = 0.005 + 0.0075 = 0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestComputeTextCost_NilUsage(t *testing.T) {
	p := chatPricing(0.000005, 0.000015)
	assert.Equal(t, 0.0, computeTextCost(&p, nil, serviceTier{}))
}

func TestComputeTextCost_ZeroTokens(t *testing.T) {
	p := chatPricing(0.000005, 0.000015)
	usage := &schemas.BifrostLLMUsage{}
	assert.Equal(t, 0.0, computeTextCost(&p, usage, serviceTier{}))
}

func TestComputeTextCost_WithCachedPromptTokens(t *testing.T) {
	// Claude 3.5 Sonnet (Bedrock): input=$3/M, output=$15/M, cache_read=$0.3/M, cache_creation=$3.75/M
	p := chatPricing(0.000003, 0.000015)
	p.CacheReadInputTokenCost = bifrost.Ptr(0.0000003)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.00000375)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 500,
		TotalTokens:      2500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  1500, // 1500 read from cache
			CachedWriteTokens: 200,  // 200 cache creation tokens
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Both cached read and write tokens are input-side deductions from promptTokens.
	// Input: (2000-1500-200)*0.000003 + 1500*0.0000003 + 200*0.00000375 = 0.0009 + 0.00045 + 0.00075 = 0.0021
	// Output: 500*0.000015 = 0.0075
	// Total: 0.0021 + 0.0075 = 0.0096
	assert.InDelta(t, 0.0096, cost, 1e-12)
}

// gpt56SolPricing returns the full tiered pricing for gpt-5.6-sol (per the OpenAI
// pricing page), including flex/priority and the 272k context tier used to
// exercise the cache-write (cache-creation) tiering added with gpt-5.6.
func gpt56SolPricing() configstoreTables.TableModelPricing {
	p := chatPricing(0.000005, 0.00003) // standard input $5/M, output $30/M
	p.InputCostPerTokenAbove272kTokens = bifrost.Ptr(0.00001)
	p.InputCostPerTokenFlex = bifrost.Ptr(0.0000025)
	p.InputCostPerTokenFlexAbove272kTokens = bifrost.Ptr(0.000005)
	p.InputCostPerTokenPriority = bifrost.Ptr(0.00001)
	p.OutputCostPerTokenAbove272kTokens = bifrost.Ptr(0.000045)
	p.OutputCostPerTokenFlex = bifrost.Ptr(0.000015)
	p.OutputCostPerTokenFlexAbove272kTokens = bifrost.Ptr(0.0000225)
	p.OutputCostPerTokenPriority = bifrost.Ptr(0.00006)
	p.CacheReadInputTokenCost = bifrost.Ptr(0.0000005)
	p.CacheReadInputTokenCostAbove272kTokens = bifrost.Ptr(0.000001)
	p.CacheReadInputTokenCostFlex = bifrost.Ptr(0.00000025)
	p.CacheReadInputTokenCostFlexAbove272kTokens = bifrost.Ptr(0.0000005)
	p.CacheReadInputTokenCostPriority = bifrost.Ptr(0.000001)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.00000625)
	p.CacheCreationInputTokenCostAbove272kTokens = bifrost.Ptr(0.0000125)
	p.CacheCreationInputTokenCostFlex = bifrost.Ptr(0.000003125)
	p.CacheCreationInputTokenCostFlexAbove272kTokens = bifrost.Ptr(0.00000625)
	p.CacheCreationInputTokenCostPriority = bifrost.Ptr(0.0000125)
	return p
}

// The reported bug scenario: a fresh-cache gpt-5.6-sol turn now returns and bills
// cache-write tokens at the base cache-creation rate.
func TestComputeTextCost_GPT56_StandardCacheWrite(t *testing.T) {
	p := gpt56SolPricing()
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     28006,
		CompletionTokens: 443,
		TotalTokens:      28449,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  0,
			CachedWriteTokens: 28003,
		},
	}
	cost := computeTextCost(&p, usage, serviceTier{})
	// input: (28006-28003)*0.000005 = 0.000015
	// write: 28003*0.00000625 = 0.17501875
	// output: 443*0.00003 = 0.01329
	assert.InDelta(t, 0.000015+0.17501875+0.01329, cost, 1e-9)
}

func TestComputeTextCost_GPT56_StandardCacheWriteAbove272k(t *testing.T) {
	p := gpt56SolPricing()
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:        300000,
		CompletionTokens:    1000,
		TotalTokens:         301000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedWriteTokens: 100000},
	}
	cost := computeTextCost(&p, usage, serviceTier{})
	// input: 200000*0.00001 = 2.0; write: 100000*0.0000125 = 1.25; output: 1000*0.000045 = 0.045
	assert.InDelta(t, 2.0+1.25+0.045, cost, 1e-9)
}

func TestComputeTextCost_GPT56_FlexTier(t *testing.T) {
	p := gpt56SolPricing()
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     10000,
		CompletionTokens: 1000,
		TotalTokens:      11000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  4000,
			CachedWriteTokens: 2000,
		},
	}
	cost := computeTextCost(&p, usage, serviceTier{isFlex: true})
	// input: 4000*0.0000025 = 0.01; read: 4000*0.00000025 = 0.001
	// write: 2000*0.000003125 = 0.00625; output: 1000*0.000015 = 0.015
	assert.InDelta(t, 0.01+0.001+0.00625+0.015, cost, 1e-12)
}

func TestComputeTextCost_GPT56_FlexTierAbove272k(t *testing.T) {
	p := gpt56SolPricing()
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     300000,
		CompletionTokens: 1000,
		TotalTokens:      301000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  100000,
			CachedWriteTokens: 50000,
		},
	}
	cost := computeTextCost(&p, usage, serviceTier{isFlex: true})
	// input: 150000*0.000005 = 0.75; read: 100000*0.0000005 = 0.05
	// write: 50000*0.00000625 = 0.3125; output: 1000*0.0000225 = 0.0225
	assert.InDelta(t, 0.75+0.05+0.3125+0.0225, cost, 1e-9)
}

func TestComputeTextCost_GPT56_PriorityTier(t *testing.T) {
	p := gpt56SolPricing()
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     10000,
		CompletionTokens: 1000,
		TotalTokens:      11000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  4000,
			CachedWriteTokens: 2000,
		},
	}
	cost := computeTextCost(&p, usage, serviceTier{isPriority: true})
	// input: 4000*0.00001 = 0.04; read: 4000*0.000001 = 0.004
	// write: 2000*0.0000125 = 0.025; output: 1000*0.00006 = 0.06
	assert.InDelta(t, 0.04+0.004+0.025+0.06, cost, 1e-12)
}

// Regression: a flex model without a flex-272k column keeps the flat flex rate
// above 272k (no new tiering leaks into existing flex models).
func TestComputeTextCost_FlexFlatAbove272kWhenNoFlexTierColumn(t *testing.T) {
	p := chatPricing(0.000005, 0.00003)
	p.InputCostPerTokenFlex = bifrost.Ptr(0.0000025)
	usage := &schemas.BifrostLLMUsage{PromptTokens: 300000, TotalTokens: 300000}
	cost := computeTextCost(&p, usage, serviceTier{isFlex: true})
	assert.InDelta(t, 300000*0.0000025, cost, 1e-9)
}

func TestComputeTextCost_FastMode(t *testing.T) {
	// Opus 4.8: standard $5/$25, fast $10/$50 per MTok.
	p := chatPricing(0.000005, 0.000025)
	p.InputCostPerTokenFast = bifrost.Ptr(0.00001)
	p.OutputCostPerTokenFast = bifrost.Ptr(0.00005)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}

	// Standard speed → standard rates.
	standard := computeTextCost(&p, usage, serviceTier{})
	assert.InDelta(t, 1000*0.000005+500*0.000025, standard, 1e-12)

	// Fast speed → fast rates.
	fast := computeTextCost(&p, usage, serviceTier{isFast: true})
	assert.InDelta(t, 1000*0.00001+500*0.00005, fast, 1e-12)
}

func TestComputeTextCost_FastMode_FlatAcrossContextWindow(t *testing.T) {
	// Fast mode is flat across the full window — it must ignore the 200k tier rate.
	p := chatPricing(0.000005, 0.000025)
	p.InputCostPerTokenFast = bifrost.Ptr(0.00001)
	p.OutputCostPerTokenFast = bifrost.Ptr(0.00005)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.0000075)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.0000375)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     250000,
		CompletionTokens: 1000,
		TotalTokens:      251000, // above the 200k tier
	}

	fast := computeTextCost(&p, usage, serviceTier{isFast: true})
	// Flat fast rate, not the above-200k rate.
	assert.InDelta(t, 250000*0.00001+1000*0.00005, fast, 1e-9)
}

func TestComputeTextCost_FastMode_FallsBackWhenUnconfigured(t *testing.T) {
	// Model without fast columns (e.g. non-Opus) → fast flag is a no-op, standard rates apply.
	p := chatPricing(0.000005, 0.000025)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}
	fast := computeTextCost(&p, usage, serviceTier{isFast: true})
	assert.InDelta(t, 1000*0.000005+500*0.000025, fast, 1e-12)
}

func TestComputeTextCost_FastMode_UsesFastCacheRates(t *testing.T) {
	// Fast mode has dedicated cache columns; when set, cache tokens bill at the
	// _fast rate, not the standard rate.
	p := chatPricing(0.000005, 0.000025)
	p.InputCostPerTokenFast = bifrost.Ptr(0.00001)
	p.OutputCostPerTokenFast = bifrost.Ptr(0.00005)
	p.CacheReadInputTokenCost = bifrost.Ptr(0.0000005)         // standard read (ignored in fast)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.00000625)    // standard 5m write (ignored in fast)
	p.CacheReadInputTokenCostFast = bifrost.Ptr(0.000001)      // fast read
	p.CacheCreationInputTokenCostFast = bifrost.Ptr(0.0000125) // fast 5m write

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 500,
		TotalTokens:      2500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  1500,
			CachedWriteTokens: 200,
		},
	}

	fast := computeTextCost(&p, usage, serviceTier{isFast: true})
	// non-cached 300*fast + read 1500*fastRead + write 200*fastWrite + output 500*fast
	expected := 300*0.00001 + 1500*0.000001 + 200*0.0000125 + 500*0.00005
	assert.InDelta(t, expected, fast, 1e-12)
}

func TestComputeTextCost_FastMode_CacheFallsBackToStandardWhenFastUnset(t *testing.T) {
	// When the _fast cache columns are absent, cache tokens fall back to standard
	// cache rates (mirrors the input/output fast fallback) — the flag is a no-op.
	p := chatPricing(0.000005, 0.000025)
	p.InputCostPerTokenFast = bifrost.Ptr(0.00001)
	p.OutputCostPerTokenFast = bifrost.Ptr(0.00005)
	p.CacheReadInputTokenCost = bifrost.Ptr(0.0000005)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.00000625)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 500,
		TotalTokens:      2500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  1500,
			CachedWriteTokens: 200,
		},
	}

	fast := computeTextCost(&p, usage, serviceTier{isFast: true})
	// input/output use fast; cache uses standard.
	expected := 300*0.00001 + 1500*0.0000005 + 200*0.00000625 + 500*0.00005
	assert.InDelta(t, expected, fast, 1e-12)
}

// TestComputeTextCost_FastMode_Opus48CacheRegression pins the reported real-world
// miscalculation: fast mode + cache_control billed cache creation at the standard
// rate. Opus 4.8: fast $10/$50, fast 5m cache write $12.50 per MTok.
func TestComputeTextCost_FastMode_Opus48CacheRegression(t *testing.T) {
	p := chatPricing(0.000005, 0.000025)
	p.InputCostPerTokenFast = bifrost.Ptr(0.00001)
	p.OutputCostPerTokenFast = bifrost.Ptr(0.00005)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.00000625)    // standard 5m write (ignored in fast)
	p.CacheCreationInputTokenCostFast = bifrost.Ptr(0.0000125) // fast 5m write

	// input_tokens=2, cache_creation=44667 (all 5m), output=135. PromptTokens
	// carries the cache-creation tokens (Anthropic responses usage mapping).
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     44669,
		CompletionTokens: 135,
		TotalTokens:      44804,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedWriteTokens: 44667,
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: 44667,
			},
		},
	}

	fast := computeTextCost(&p, usage, serviceTier{isFast: true})
	// 2*$10/M + 44667*$12.50/M (fast 5m cache) + 135*$50/M = $0.565108
	expected := 2*0.00001 + 44667*0.0000125 + 135*0.00005
	assert.InDelta(t, expected, fast, 1e-9)
}

// TestComputeTextCost_InferenceGeoUS_AppliesMultiplier verifies the Anthropic
// data-residency multiplier (inference_geo:"us") scales every token/cache cost by
// 1.1x while leaving the flat per-search fee untouched.
func TestComputeTextCost_InferenceGeoUS_AppliesMultiplier(t *testing.T) {
	p := chatPricing(0.00001, 0.00005)
	p.CacheReadInputTokenCost = bifrost.Ptr(0.000001)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.0000125)
	p.SearchContextCostPerQuery = bifrost.Ptr(0.01)
	p.InferenceGeoUSMultiplier = bifrost.Ptr(1.1)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000, // 500 non-cached + 200 read + 300 write
		CompletionTokens: 100,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  200,
			CachedWriteTokens: 300,
		},
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			NumSearchQueries: bifrost.Ptr(2),
		},
	}

	tokenCost := 500*0.00001 + 200*0.000001 + 300*0.0000125 + 100*0.00005
	searchCost := 2 * 0.01

	got := computeTextCost(&p, usage, serviceTier{inferenceGeoUS: true})
	assert.InDelta(t, tokenCost*1.1+searchCost, got, 1e-9)

	// Without US residency the multiplier is a no-op; the search fee is identical.
	base := computeTextCost(&p, usage, serviceTier{})
	assert.InDelta(t, tokenCost+searchCost, base, 1e-9)
}

// TestComputeTextCost_InferenceGeoUS_NoMultiplierColumn verifies US residency is a
// safe no-op until the datasheet populates the multiplier column upstream.
func TestComputeTextCost_InferenceGeoUS_NoMultiplierColumn(t *testing.T) {
	p := chatPricing(0.00001, 0.00005)
	usage := &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 100}
	withUS := computeTextCost(&p, usage, serviceTier{inferenceGeoUS: true})
	without := computeTextCost(&p, usage, serviceTier{})
	assert.InDelta(t, without, withUS, 1e-9)
}

func TestTierFromResponse_Speed(t *testing.T) {
	assert.False(t, tierFromResponse(nil, nil, nil).isFast)
	assert.False(t, tierFromResponse(nil, bifrost.Ptr("standard"), nil).isFast)
	assert.True(t, tierFromResponse(nil, bifrost.Ptr("fast"), nil).isFast)
}

func TestTierFromResponse_InferenceGeo(t *testing.T) {
	assert.False(t, tierFromResponse(nil, nil, nil).inferenceGeoUS)
	assert.False(t, tierFromResponse(nil, nil, bifrost.Ptr("global")).inferenceGeoUS)
	assert.True(t, tierFromResponse(nil, nil, bifrost.Ptr("us")).inferenceGeoUS)
	assert.True(t, tierFromResponse(nil, nil, bifrost.Ptr("US")).inferenceGeoUS)
}

func TestComputeTextCost_With1hrCacheCreationTokens(t *testing.T) {
	// claude-3-5-sonnet-20241022-v2:0 on Bedrock:
	// input=$3/M, output=$15/M, cache_creation=$3.75/M, cache_creation_1hr=$7.50/M, cache_read=$0.3/M
	p := chatPricing(0.000003, 0.000015)
	p.CacheReadInputTokenCost = bifrost.Ptr(3e-7)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.00000375)
	p.CacheCreationInputTokenCostAbove1hr = bifrost.Ptr(0.0000075)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 500,
		TotalTokens:      2500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedWriteTokens: 1000,
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens1h: 600, // 600 at 1hr rate, 400 at standard rate
			},
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Input (non-cached): (2000-1000)*0.000003 = 0.003
	// Cache creation (1hr): 600*0.0000075 = 0.0045
	// Cache creation (standard): 400*0.00000375 = 0.0015
	// Output: 500*0.000015 = 0.0075
	// Total: 0.003 + 0.0045 + 0.0015 + 0.0075 = 0.0165
	assert.InDelta(t, 0.0165, cost, 1e-12)
}

func TestComputeTextCost_StandardCacheCreationPricingLesserThan1hr(t *testing.T) {
	// Standard (5-min TTL) cache creation is cheaper than 1hr TTL cache creation.
	// 1hr rate ($7.50/M) is 2x the standard rate ($3.75/M).
	p := chatPricing(0.000003, 0.000015)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.00000375)
	p.CacheCreationInputTokenCostAbove1hr = bifrost.Ptr(0.0000075)

	base := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 500,
		TotalTokens:      2500,
	}

	usageStandard := *base
	usageStandard.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
		CachedWriteTokens: 1000,
	}

	usage1hr := *base
	usage1hr.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
		CachedWriteTokens: 1000,
		CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
			CachedWriteTokens1h: 1000, // all 1000 tokens at 1hr rate
		},
	}

	costStandard := computeTextCost(&p, &usageStandard, serviceTier{})
	cost1hr := computeTextCost(&p, &usage1hr, serviceTier{})

	assert.Less(t, costStandard, cost1hr, "standard cache creation should cost less than 1hr cache creation")
}

func TestComputeTextCost_1hrCacheCreationFallsBackToStandardWhenAbove1hrRateAbsent(t *testing.T) {
	// claude-3-5-haiku on Bedrock has no cache_creation_input_token_cost_above_1hr entry.
	// Tokens marked as 1hr cache writes must fall back to the standard cache creation rate.
	p := chatPricing(8e-7, 0.000004)
	p.CacheReadInputTokenCost = bifrost.Ptr(8e-8)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.000001)
	// CacheCreationInputTokenCostAbove1hr intentionally left nil

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 500,
		TotalTokens:      2500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedWriteTokens: 1000,
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens1h: 1000, // all 1hr tokens, but no above_1hr rate configured
			},
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Input (non-cached): (2000-1000)*8e-7 = 0.0008
	// Cache creation (1hr fallback → standard 0.000001): 1000*0.000001 = 0.001
	// Output: 500*0.000004 = 0.002
	// Total: 0.0008 + 0.001 + 0.002 = 0.0038
	assert.InDelta(t, 0.0038, cost, 1e-12)
}

func TestComputeTextCost_CacheWriteTokenDetailsNil_FallsBackToStandardCreationRate(t *testing.T) {
	// CachedWriteTokens is set but CachedWriteTokenDetails is nil.
	// All write tokens must use the standard cache creation rate even though above_1hr is configured.
	p := chatPricing(0.000003, 0.000015)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.00000375)
	p.CacheCreationInputTokenCostAbove1hr = bifrost.Ptr(0.0000075) // present but must not be used

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 500,
		TotalTokens:      2500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedWriteTokens:       1000,
			CachedWriteTokenDetails: nil,
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Input (non-cached): (2000-1000)*0.000003 = 0.003
	// Cache creation (standard, no 1hr details): 1000*0.00000375 = 0.00375
	// Output: 500*0.000015 = 0.0075
	// Total: 0.003 + 0.00375 + 0.0075 = 0.01425
	assert.InDelta(t, 0.01425, cost, 1e-12)
}

func TestComputeTextCost_CacheWriteTokenDetails1hZero_FallsBackToStandardCreationRate(t *testing.T) {
	// CachedWriteTokenDetails is present but CachedWriteTokens1h is 0 (e.g. all tokens
	// used 5-min TTL). All write tokens must use the standard cache creation rate.
	p := chatPricing(0.000003, 0.000015)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.00000375)
	p.CacheCreationInputTokenCostAbove1hr = bifrost.Ptr(0.0000075) // present but must not be used

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 500,
		TotalTokens:      2500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedWriteTokens: 1000,
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens1h: 0,
			},
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Input (non-cached): (2000-1000)*0.000003 = 0.003
	// Cache creation (standard, 1h count is 0): 1000*0.00000375 = 0.00375
	// Output: 500*0.000015 = 0.0075
	// Total: 0.003 + 0.00375 + 0.0075 = 0.01425
	assert.InDelta(t, 0.01425, cost, 1e-12)
}

func TestComputeTextCost_1hrCacheCreationAbove200k_UsesAbove1hrAbove200kRate(t *testing.T) {
	// claude-3-5-sonnet-20241022-v2:0 on Bedrock has all four cache creation tiers.
	// When totalTokens > 200k and CachedWriteTokens1h > 0, the above_1hr_above_200k rate
	// ($15/M) must be used — the most specific tier wins.
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.00000375)
	p.CacheCreationInputTokenCostAbove200kTokens = bifrost.Ptr(0.0000075)
	p.CacheCreationInputTokenCostAbove1hr = bifrost.Ptr(0.0000075)
	p.CacheCreationInputTokenCostAbove1hrAbove200kTokens = bifrost.Ptr(0.000015)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     210000,
		CompletionTokens: 25000,
		TotalTokens:      235000, // input is above 200k threshold
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedWriteTokens: 10000,
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens1h: 10000,
			},
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Input rate (>200k): 0.000006; output rate (>200k): 0.00003
	// Input (non-cached): (210000-10000)*0.000006 = 200000*0.000006 = 1.20
	// Cache creation 1hr above 200k: 10000*0.000015 = 0.15
	// Output: 25000*0.00003 = 0.75
	// Total: 1.20 + 0.15 + 0.75 = 2.10
	assert.InDelta(t, 2.10, cost, 1e-9)
}

func TestComputeTextCost_1hrCacheCreationAbove200k_FallsBackToAbove1hrWhenAbove200kRateAbsent(t *testing.T) {
	// When CacheCreationInputTokenCostAbove1hrAbove200kTokens is absent but
	// CacheCreationInputTokenCostAbove1hr is present, the 1hr rate must be used
	// even for >200k requests.
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.00000375)
	p.CacheCreationInputTokenCostAbove200kTokens = bifrost.Ptr(0.0000075)
	p.CacheCreationInputTokenCostAbove1hr = bifrost.Ptr(0.000009) // distinct from above_200k to make fallback unambiguous
	// CacheCreationInputTokenCostAbove1hrAbove200kTokens intentionally left nil

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     210000,
		CompletionTokens: 25000,
		TotalTokens:      235000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedWriteTokens: 10000,
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens1h: 10000,
			},
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Cache creation 1hr (no above_200k_1hr rate, uses above_1hr): 10000*0.000009 = 0.09
	// Input (non-cached): 200000*0.000006 = 1.20
	// Output: 25000*0.00003 = 0.75
	// Total: 1.20 + 0.09 + 0.75 = 2.04
	assert.InDelta(t, 2.04, cost, 1e-9)
}

func TestComputeTextCost_1hrCacheCreationAbove200k_FallsBackToStandardAbove200kWhenNo1hrRates(t *testing.T) {
	// When neither above_1hr field is present, 1hr tokens on a >200k request fall back
	// to the standard above_200k cache creation rate.
	p := chatPricing(8e-7, 0.000004)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.0000016)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.000008)
	p.CacheCreationInputTokenCost = bifrost.Ptr(0.000001)
	p.CacheCreationInputTokenCostAbove200kTokens = bifrost.Ptr(0.000002)
	// Neither CacheCreationInputTokenCostAbove1hr nor Above1hrAbove200k is set

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     210000,
		CompletionTokens: 25000,
		TotalTokens:      235000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedWriteTokens: 10000,
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens1h: 10000,
			},
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Cache creation (1hr → no 1hr rates → standard above_200k): 10000*0.000002 = 0.02
	// Input (non-cached): 200000*0.0000016 = 0.32
	// Output: 25000*0.000008 = 0.2
	// Total: 0.32 + 0.02 + 0.2 = 0.54
	assert.InDelta(t, 0.54, cost, 1e-9)
}

func TestComputeTextCost_Tiered200k(t *testing.T) {
	// Claude 3.5 Sonnet Bedrock 200k tier: input=$6/M, output=$30/M
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     210000,
		CompletionTokens: 30000,
		TotalTokens:      240000, // input is above 200k threshold
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Uses tiered rate since input > 200k
	// 210000 * 0.000006 + 30000 * 0.00003 = 1.26 + 0.90 = 2.16
	assert.InDelta(t, 2.16, cost, 1e-9)
}

func TestComputeTextCost_Below200kUsesBaseRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500, // Below 200k
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Uses base rate since input < 200k
	// 1000 * 0.000003 + 500 * 0.000015 = 0.003 + 0.0075 = 0.0105
	assert.InDelta(t, 0.0105, cost, 1e-12)
}

func TestComputeTextCost_TotalAbove200kButInputBelow200kUsesBaseRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     180000,
		CompletionTokens: 30000,
		TotalTokens:      210000, // total is above 200k, input is not
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Uses base rates because long-context tiers are selected by input tokens.
	// 180000 * 0.000003 + 30000 * 0.000015 = 0.54 + 0.45 = 0.99
	assert.InDelta(t, 0.99, cost, 1e-9)
}

func TestComputeTextCost_Tiered272k(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = new(0.000006)
	p.OutputCostPerTokenAbove200kTokens = new(0.00003)
	p.InputCostPerTokenAbove272kTokens = new(0.000009)
	p.OutputCostPerTokenAbove272kTokens = new(0.000045)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     280000,
		CompletionTokens: 30000,
		TotalTokens:      310000, // input is above 272k threshold
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Uses 272k tiered rate since input > 272k
	// 280000 * 0.000009 + 30000 * 0.000045 = 2.52 + 1.35 = 3.87
	assert.InDelta(t, 3.87, cost, 1e-9)
}

func TestComputeTextCost_Between200kAnd272kUses200kRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = new(0.000006)
	p.OutputCostPerTokenAbove200kTokens = new(0.00003)
	p.InputCostPerTokenAbove272kTokens = new(0.000009)
	p.OutputCostPerTokenAbove272kTokens = new(0.000045)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     230000,
		CompletionTokens: 30000,
		TotalTokens:      260000, // input is between 200k and 272k
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Uses 200k tiered rate since input > 200k but <= 272k
	// 230000 * 0.000006 + 30000 * 0.00003 = 1.38 + 0.90 = 2.28
	assert.InDelta(t, 2.28, cost, 1e-9)
}

func TestComputeTextCost_272kTierWithCacheRead(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove272kTokens = new(0.000009)
	p.OutputCostPerTokenAbove272kTokens = new(0.000045)
	p.CacheReadInputTokenCost = new(0.0000003)
	p.CacheReadInputTokenCostAbove272kTokens = new(0.0000009)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     280000,
		CompletionTokens: 30000,
		TotalTokens:      310000, // input is above 272k
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens: 50000,
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Non-cached input: (280000-50000) * 0.000009 = 230000 * 0.000009 = 2.07
	// Cached read: 50000 * 0.0000009 = 0.045
	// Output: 30000 * 0.000045 = 1.35
	// Total: 2.07 + 0.045 + 1.35 = 3.465
	assert.InDelta(t, 3.465, cost, 1e-9)
}

func TestComputeTextCost_SearchQueryCost(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.SearchContextCostPerQuery = bifrost.Ptr(0.01) // $0.01 per search query

	numQueries := 3
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			NumSearchQueries: &numQueries,
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// 1000*0.000003 + 500*0.000015 + 3*0.01 = 0.003 + 0.0075 + 0.03 = 0.0405
	assert.InDelta(t, 0.0405, cost, 1e-12)
}

func TestComputeTextCost_NoCacheRateFallsBackToBaseInputRate(t *testing.T) {
	// If cache rate fields are nil, tieredCacheReadInputTokenRate falls back to base InputCostPerToken
	p := chatPricing(0.000005, 0.000015)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens: 400,
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Non-cached prompt: (1000-400)*0.000005 = 600*0.000005 = 0.003
	// Cached prompt: 400 tokens at base input rate (no cache rate set) = 400*0.000005 = 0.002
	// Output: 500*0.000015 = 0.0075
	// Total: 0.003 + 0.002 + 0.0075 = 0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

// =========================================================================
// 2. computeEmbeddingCost — unit tests
// =========================================================================

func TestComputeEmbeddingCost_Basic(t *testing.T) {
	// Titan Embed Text v1: $0.1/M input
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  bifrost.Ptr(0.0000001),
		OutputCostPerToken: bifrost.Ptr(0.0),
	}
	usage := &schemas.BifrostLLMUsage{
		PromptTokens: 5000,
		TotalTokens:  5000,
	}
	cost := computeEmbeddingCost(&p, usage, serviceTier{})
	// 5000 * 0.0000001 = 0.0005
	assert.InDelta(t, 0.0005, cost, 1e-12)
}

func TestComputeEmbeddingCost_TotalAbove200kButInputBelow200kUsesBaseRate(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:                bifrost.Ptr(0.000003),
		InputCostPerTokenAbove200kTokens: bifrost.Ptr(0.000006),
	}
	usage := &schemas.BifrostLLMUsage{
		PromptTokens: 180000,
		TotalTokens:  210000,
	}

	cost := computeEmbeddingCost(&p, usage, serviceTier{})

	assert.InDelta(t, 180000*0.000003, cost, 1e-9)
}

func TestComputeEmbeddingCost_NilUsage(t *testing.T) {
	p := configstoreTables.TableModelPricing{InputCostPerToken: new(0.0000001)}
	assert.Equal(t, 0.0, computeEmbeddingCost(&p, nil, serviceTier{}))
}

// =========================================================================
// 3. computeRerankCost — unit tests
// =========================================================================

func TestComputeRerankCost_Basic(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  bifrost.Ptr(0.000001),
		OutputCostPerToken: bifrost.Ptr(0.000002),
	}
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 100,
		TotalTokens:      2100,
	}
	cost := computeRerankCost(&p, usage, serviceTier{})
	// 2000*0.000001 + 100*0.000002 = 0.002 + 0.0002 = 0.0022
	assert.InDelta(t, 0.0022, cost, 1e-12)
}

func TestComputeRerankCost_TotalAbove200kButInputBelow200kUsesBaseRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     180000,
		CompletionTokens: 30000,
		TotalTokens:      210000,
	}

	cost := computeRerankCost(&p, usage, serviceTier{})

	assert.InDelta(t, 180000*0.000003+30000*0.000015, cost, 1e-9)
}

func TestComputeRerankCost_WithSearchCost(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:         bifrost.Ptr(0.0),
		OutputCostPerToken:        bifrost.Ptr(0.0),
		SearchContextCostPerQuery: bifrost.Ptr(0.001),
	}
	numQueries := 5
	usage := &schemas.BifrostLLMUsage{
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			NumSearchQueries: &numQueries,
		},
	}
	cost := computeRerankCost(&p, usage, serviceTier{})
	assert.InDelta(t, 0.005, cost, 1e-12)
}

func TestComputeRerankCost_NilUsage(t *testing.T) {
	p := configstoreTables.TableModelPricing{InputCostPerToken: new(0.001)}
	assert.Equal(t, 0.0, computeRerankCost(&p, nil, serviceTier{}))
}

// =========================================================================
// 4. computeSpeechCost — unit tests
// =========================================================================

func TestComputeSpeechCost_TokensPreferredOverDuration(t *testing.T) {
	// TTS: input=text tokens, output=audio tokens (preferred over per-second)
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:   bifrost.Ptr(0.0000025),
		OutputCostPerToken:  bifrost.Ptr(0.00001),
		OutputCostPerSecond: bifrost.Ptr(0.00025),
	}
	seconds := 60
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     100,
		CompletionTokens: 200,
		TotalTokens:      300,
	}
	cost := computeSpeechCost(&p, usage, &seconds, 0, serviceTier{})
	// Input: 100 text tokens * $0.0000025 = $0.00025
	// Output: 200 audio tokens present → uses token rate $0.00001, NOT per-second
	//         200 * $0.00001 = $0.002
	// Total: $0.00225
	assert.InDelta(t, 0.00225, cost, 1e-12)
}

func TestComputeSpeechCost_OutputFallsBackToPerSecond(t *testing.T) {
	// TTS: no output tokens → falls back to per-second output pricing
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:   bifrost.Ptr(0.000001),
		OutputCostPerToken:  bifrost.Ptr(0.000002),
		OutputCostPerSecond: bifrost.Ptr(0.0001),
	}
	seconds := 120
	usage := &schemas.BifrostLLMUsage{PromptTokens: 500}
	cost := computeSpeechCost(&p, usage, &seconds, 0, serviceTier{})
	// Input: 500 * $0.000001 = $0.0005
	// Output: no CompletionTokens → falls back to 120 * $0.0001 = $0.012
	// Total: $0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestComputeSpeechCost_OutputAudioTokenRate(t *testing.T) {
	// TTS: output uses OutputCostPerAudioToken when available
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:       bifrost.Ptr(0.000001),
		OutputCostPerToken:      bifrost.Ptr(0.000002),
		OutputCostPerAudioToken: bifrost.Ptr(0.00005),
	}
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     200,
		CompletionTokens: 100,
		TotalTokens:      300,
	}
	cost := computeSpeechCost(&p, usage, nil, 0, serviceTier{})
	// Input: 200 * $0.000001 = $0.0002
	// Output: 100 * $0.00005 = $0.005 (OutputCostPerAudioToken preferred)
	// Total: $0.0052
	assert.InDelta(t, 0.0052, cost, 1e-12)
}

func TestComputeSpeechCost_TokenFallback(t *testing.T) {
	p := chatPricing(0.000005, 0.000015)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}
	cost := computeSpeechCost(&p, usage, nil, 0, serviceTier{}) // No audio seconds → token fallback
	// 1000*0.000005 + 500*0.000015 = 0.005 + 0.0075 = 0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestComputeSpeechCost_TotalAbove200kButInputBelow200kUsesBaseRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     180000,
		CompletionTokens: 30000,
		TotalTokens:      210000,
	}

	cost := computeSpeechCost(&p, usage, nil, 0, serviceTier{})

	assert.InDelta(t, 180000*0.000003+30000*0.000015, cost, 1e-9)
}

func TestComputeSpeechCost_NilUsageNilSeconds(t *testing.T) {
	p := chatPricing(0.000005, 0.000015)
	assert.Equal(t, 0.0, computeSpeechCost(&p, nil, nil, 0, serviceTier{}))
}

// =========================================================================
// 5. computeTranscriptionCost — unit tests
// =========================================================================

func TestComputeTranscriptionCost_DurationBased(t *testing.T) {
	// assemblyai/nano: input_cost_per_second=0.00010278
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  bifrost.Ptr(0.0),
		OutputCostPerToken: bifrost.Ptr(0.0),
		InputCostPerSecond: bifrost.Ptr(0.00010278),
	}
	seconds := 300 // 5 minutes
	cost := computeTranscriptionCost(&p, nil, &seconds, nil, serviceTier{})
	// 300 * 0.00010278 = 0.030834
	assert.InDelta(t, 0.030834, cost, 1e-9)
}

func TestComputeTranscriptionCost_AudioTokenDetails(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:      bifrost.Ptr(0.000005),
		OutputCostPerToken:     bifrost.Ptr(0.000015),
		InputCostPerAudioToken: bifrost.Ptr(0.00001),
	}
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 500,
		TotalTokens:      2500,
	}
	audioDetails := &schemas.TranscriptionUsageInputTokenDetails{
		AudioTokens: 1500,
		TextTokens:  500,
	}
	cost := computeTranscriptionCost(&p, usage, nil, audioDetails, serviceTier{})
	// Audio: 1500*0.00001 = 0.015
	// Text:  500*0.000005 = 0.0025
	// Output: 500*0.000015 = 0.0075
	// Total: 0.025
	assert.InDelta(t, 0.025, cost, 1e-12)
}

func TestComputeTranscriptionCost_TokenFallback(t *testing.T) {
	p := chatPricing(0.000005, 0.000015)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 200,
		TotalTokens:      1200,
	}
	cost := computeTranscriptionCost(&p, usage, nil, nil, serviceTier{})
	// 1000*0.000005 + 200*0.000015 = 0.005 + 0.003 = 0.008
	assert.InDelta(t, 0.008, cost, 1e-12)
}

func TestComputeTranscriptionCost_TotalAbove200kButInputBelow200kUsesBaseRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     180000,
		CompletionTokens: 30000,
		TotalTokens:      210000,
	}

	cost := computeTranscriptionCost(&p, usage, nil, nil, serviceTier{})

	assert.InDelta(t, 180000*0.000003+30000*0.000015, cost, 1e-9)
}

func TestComputeTranscriptionCost_TokenDetailsPreferredOverDuration(t *testing.T) {
	// STT: audio token details present → uses tokens, not per-second
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:          bifrost.Ptr(0.000005),
		OutputCostPerToken:         bifrost.Ptr(0.0),
		InputCostPerAudioPerSecond: bifrost.Ptr(0.0001),
		InputCostPerAudioToken:     bifrost.Ptr(0.00001),
	}
	seconds := 60
	audioDetails := &schemas.TranscriptionUsageInputTokenDetails{
		AudioTokens: 5000,
		TextTokens:  1000,
	}
	cost := computeTranscriptionCost(&p, nil, &seconds, audioDetails, serviceTier{})
	// Input: audio token details present → tokens preferred over per-second
	//   5000 audio * $0.00001 = $0.05
	//   1000 text  * $0.000005 = $0.005
	// Output: nil usage → $0
	// Total: $0.055
	assert.InDelta(t, 0.055, cost, 1e-12)
}

func TestComputeTranscriptionCost_DurationFallbackWhenNoTokens(t *testing.T) {
	// STT: no audio token details, no prompt tokens → falls back to per-second
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:          bifrost.Ptr(0.000005),
		OutputCostPerToken:         bifrost.Ptr(0.000015),
		InputCostPerAudioPerSecond: bifrost.Ptr(0.0001),
	}
	seconds := 60
	usage := &schemas.BifrostLLMUsage{
		CompletionTokens: 200,
		TotalTokens:      200,
	}
	cost := computeTranscriptionCost(&p, usage, &seconds, nil, serviceTier{})
	// Input: no audio details, PromptTokens=0 → falls back to 60 * $0.0001 = $0.006
	// Output: 200 * $0.000015 = $0.003
	// Total: $0.009
	assert.InDelta(t, 0.009, cost, 1e-12)
}

// =========================================================================
// 6. computeImageCost — unit tests
// =========================================================================

func TestComputeImageCost_PerImage(t *testing.T) {
	// dall-e-3 (aiml): output_cost_per_image=$0.052
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  bifrost.Ptr(0.0),
		OutputCostPerToken: bifrost.Ptr(0.0),
		OutputCostPerImage: bifrost.Ptr(0.052),
	}
	usage := &schemas.ImageUsage{
		OutputTokensDetails: &schemas.ImageTokenDetails{
			NImages: 2,
		},
	}
	cost := computeImageCost(&p, usage, "", "", serviceTier{})
	// 2 * 0.052 = 0.104
	assert.InDelta(t, 0.104, cost, 1e-12)
}

func TestComputeImageCost_PerImageDefaultsToOne(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		OutputCostPerImage: bifrost.Ptr(0.052),
	}
	usage := &schemas.ImageUsage{} // No token details → defaults to 1 image
	cost := computeImageCost(&p, usage, "", "", serviceTier{})
	assert.InDelta(t, 0.052, cost, 1e-12)
}

func TestComputeImageCost_TokenBased(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  bifrost.Ptr(0.000005),
		OutputCostPerToken: bifrost.Ptr(0.000015),
	}
	usage := &schemas.ImageUsage{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
	}
	cost := computeImageCost(&p, usage, "", "", serviceTier{})
	// 1000*0.000005 + 500*0.000015 = 0.005 + 0.0075 = 0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestComputeImageCost_TotalAbove200kButInputBelow200kUsesBaseRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)
	usage := &schemas.ImageUsage{
		InputTokens:  180000,
		OutputTokens: 30000,
		TotalTokens:  210000,
	}

	cost := computeImageCost(&p, usage, "", "", serviceTier{})

	assert.InDelta(t, 180000*0.000003+30000*0.000015, cost, 1e-9)
}

func TestComputeImageCost_DerivesTierTokensFromTotalMinusOutputWhenInputMissing(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)
	usage := &schemas.ImageUsage{
		OutputTokens: 30000,
		TotalTokens:  240000, // derived input = 210000, so output uses long-context rate
	}

	cost := computeImageCost(&p, usage, "", "", serviceTier{})

	assert.InDelta(t, 30000*0.00003, cost, 1e-9)
}

func TestComputeImageCost_DoesNotUseBareTotalTokensAsInputTierTokens(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.OutputCostPerImage = bifrost.Ptr(0.05)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)
	usage := &schemas.ImageUsage{
		TotalTokens: 210000, // no input/output split; total includes output, so do not use it as input
	}

	cost := computeImageCost(&p, usage, "", "", serviceTier{})

	assert.InDelta(t, 0.05, cost, 1e-9)
}

func TestComputeImageCost_TokenBasedWithDetails(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  bifrost.Ptr(0.000005),
		OutputCostPerToken: bifrost.Ptr(0.000015),
	}
	usage := &schemas.ImageUsage{
		InputTokens:  2000,
		OutputTokens: 1000,
		TotalTokens:  3000,
		InputTokensDetails: &schemas.ImageTokenDetails{
			TextTokens:  500,
			ImageTokens: 1500,
		},
		OutputTokensDetails: &schemas.ImageTokenDetails{
			TextTokens:  200,
			ImageTokens: 800,
		},
	}
	cost := computeImageCost(&p, usage, "", "", serviceTier{})
	// Input: (500+1500)*0.000005 = 2000*0.000005 = 0.01
	// Output: (200+800)*0.000015 = 1000*0.000015 = 0.015
	// Total: 0.025
	assert.InDelta(t, 0.025, cost, 1e-12)
}

func TestComputeImageCost_NilUsage(t *testing.T) {
	p := configstoreTables.TableModelPricing{OutputCostPerImage: new(0.05)}
	assert.Equal(t, 0.0, computeImageCost(&p, nil, "", "", serviceTier{}))
}

func TestComputeImageCost_InputAndOutputPerImage(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerImage:  bifrost.Ptr(0.01),
		OutputCostPerImage: bifrost.Ptr(0.05),
	}
	usage := &schemas.ImageUsage{
		NumInputImages:      3,
		OutputTokensDetails: &schemas.ImageTokenDetails{NImages: 2},
	}
	cost := computeImageCost(&p, usage, "", "", serviceTier{})
	// 3 input * $0.01 + 2 output * $0.05 = $0.03 + $0.10 = $0.13
	assert.InDelta(t, 0.13, cost, 1e-12)
}

func TestComputeImageCost_PerPixelOutput(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		OutputCostPerPixel: bifrost.Ptr(0.000000019), // ~$0.02 for 1024x1024
	}
	usage := &schemas.ImageUsage{
		OutputTokensDetails: &schemas.ImageTokenDetails{NImages: 1},
	}
	cost := computeImageCost(&p, usage, "1024x1024", "", serviceTier{})
	// 1024*1024 * 1 * 0.000000019 = 1048576 * 0.000000019 ≈ 0.01992
	assert.InDelta(t, 1048576*0.000000019, cost, 1e-12)
}

func TestComputeImageCost_PerPixelInputAndOutput(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerPixel:  bifrost.Ptr(0.00000001),
		OutputCostPerPixel: bifrost.Ptr(0.00000002),
	}
	usage := &schemas.ImageUsage{
		NumInputImages:      2,
		OutputTokensDetails: &schemas.ImageTokenDetails{NImages: 3},
	}
	cost := computeImageCost(&p, usage, "512x512", "", serviceTier{})
	pixels := 512 * 512 // 262144
	// Input: 262144 * 2 * 0.00000001 = 0.00524288
	// Output: 262144 * 3 * 0.00000002 = 0.01572864
	expected := float64(pixels*2)*0.00000001 + float64(pixels*3)*0.00000002
	assert.InDelta(t, expected, cost, 1e-12)
}

func TestComputeImageCost_TokensPreferredOverPixels(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  bifrost.Ptr(0.000005),
		OutputCostPerToken: bifrost.Ptr(0.000015),
		InputCostPerPixel:  bifrost.Ptr(0.00000001),
		OutputCostPerPixel: bifrost.Ptr(0.00000002),
	}
	usage := &schemas.ImageUsage{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
	}
	cost := computeImageCost(&p, usage, "1024x1024", "", serviceTier{})
	// Tokens should win: 1000*0.000005 + 500*0.000015 = 0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestComputeImageCost_PixelsPreferredOverPerImage(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		OutputCostPerPixel: bifrost.Ptr(0.00000002),
		OutputCostPerImage: bifrost.Ptr(999.0), // should not be used
	}
	usage := &schemas.ImageUsage{
		OutputTokensDetails: &schemas.ImageTokenDetails{NImages: 1},
	}
	cost := computeImageCost(&p, usage, "256x256", "", serviceTier{})
	// Per-pixel should win: 65536 * 1 * 0.00000002 = 0.00131072
	assert.InDelta(t, 65536*0.00000002, cost, 1e-12)
}

func TestComputeImageCost_PerPixelFallsBackToPerImage_WhenNoSize(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		OutputCostPerPixel: bifrost.Ptr(0.00000002),
		OutputCostPerImage: bifrost.Ptr(0.05),
	}
	usage := &schemas.ImageUsage{
		OutputTokensDetails: &schemas.ImageTokenDetails{NImages: 2},
	}
	cost := computeImageCost(&p, usage, "", "", serviceTier{})
	// No size → pixels=0, falls through to per-image: 2 * $0.05 = $0.10
	assert.InDelta(t, 0.10, cost, 1e-12)
}

func TestComputeImageCost_QualityBasedRates(t *testing.T) {
	usage := &schemas.ImageUsage{
		OutputTokensDetails: &schemas.ImageTokenDetails{NImages: 1},
	}
	// Quality-specific rates take precedence over base/size-tier
	p := configstoreTables.TableModelPricing{
		OutputCostPerImage:              bifrost.Ptr(0.01),
		OutputCostPerImageLowQuality:    bifrost.Ptr(0.02),
		OutputCostPerImageMediumQuality: bifrost.Ptr(0.03),
		OutputCostPerImageHighQuality:   bifrost.Ptr(0.04),
		OutputCostPerImageAutoQuality:   bifrost.Ptr(0.05),
	}
	assert.InDelta(t, 0.02, computeImageCost(&p, usage, "", "low", serviceTier{}), 1e-12)
	assert.InDelta(t, 0.03, computeImageCost(&p, usage, "", "medium", serviceTier{}), 1e-12)
	assert.InDelta(t, 0.04, computeImageCost(&p, usage, "", "high", serviceTier{}), 1e-12)
	assert.InDelta(t, 0.05, computeImageCost(&p, usage, "", "auto", serviceTier{}), 1e-12)
	// "hd" does not match any quality case so perImageRate stays nil → size/base fallback.
	assert.InDelta(t, 0.01, computeImageCost(&p, usage, "", "hd", serviceTier{}), 1e-12)
	// Empty quality is treated as auto
	assert.InDelta(t, 0.05, computeImageCost(&p, usage, "", "", serviceTier{}), 1e-12)
}

func TestParseImagePixels(t *testing.T) {
	assert.Equal(t, 1048576, parseImagePixels("1024x1024"))
	assert.Equal(t, 262144, parseImagePixels("512x512"))
	assert.Equal(t, 1835008, parseImagePixels("1792x1024"))
	assert.Equal(t, 0, parseImagePixels(""))
	assert.Equal(t, 0, parseImagePixels("invalid"))
	assert.Equal(t, 0, parseImagePixels("1024"))
	assert.Equal(t, 0, parseImagePixels("0x1024"))
	assert.Equal(t, 0, parseImagePixels("-1x1024"))
}

// =========================================================================
// 7. computeVideoCost — unit tests
// =========================================================================

func TestComputeVideoCost_DurationBased(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:           bifrost.Ptr(0.000001),
		OutputCostPerToken:          bifrost.Ptr(0.0),
		OutputCostPerVideoPerSecond: bifrost.Ptr(0.001),
	}
	seconds := 30
	usage := &schemas.BifrostLLMUsage{PromptTokens: 500, TotalTokens: 500}
	cost := computeVideoCost(&p, usage, &seconds, serviceTier{})
	// Output: 30 * 0.001 = 0.03
	// Input:  500 * 0.000001 = 0.0005
	// Total:  0.0305
	assert.InDelta(t, 0.0305, cost, 1e-12)
}

func TestComputeVideoCost_TotalAbove200kButInputBelow200kUsesBaseRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = bifrost.Ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = bifrost.Ptr(0.00003)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     180000,
		CompletionTokens: 30000,
		TotalTokens:      210000,
	}

	cost := computeVideoCost(&p, usage, nil, serviceTier{})

	assert.InDelta(t, 180000*0.000003+30000*0.000015, cost, 1e-9)
}

func TestComputeVideoCost_OutputCostPerSecondFallback(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:   bifrost.Ptr(0.0),
		OutputCostPerToken:  bifrost.Ptr(0.0),
		OutputCostPerSecond: bifrost.Ptr(0.002),
	}
	seconds := 10
	cost := computeVideoCost(&p, nil, &seconds, serviceTier{})
	assert.InDelta(t, 0.02, cost, 1e-12)
}

func TestComputeVideoCost_NilSeconds(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:           bifrost.Ptr(0.000001),
		OutputCostPerVideoPerSecond: bifrost.Ptr(0.001),
	}
	usage := &schemas.BifrostLLMUsage{PromptTokens: 1000}
	cost := computeVideoCost(&p, usage, nil, serviceTier{})
	// Only input tokens: 1000 * 0.000001 = 0.001
	assert.InDelta(t, 0.001, cost, 1e-12)
}

// =========================================================================
// 8. tieredInputRate / tieredOutputRate
// =========================================================================

func TestTieredInputRate_BelowThreshold(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:                bifrost.Ptr(0.000003),
		InputCostPerTokenAbove200kTokens: bifrost.Ptr(0.000006),
	}
	assert.Equal(t, 0.000003, tieredInputRate(&p, 100000, serviceTier{}))
}

func TestTieredInputRate_AboveThreshold(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:                bifrost.Ptr(0.000003),
		InputCostPerTokenAbove200kTokens: bifrost.Ptr(0.000006),
	}
	assert.Equal(t, 0.000006, tieredInputRate(&p, 210000, serviceTier{}))
}

func TestTieredInputRate_AboveThresholdNoTieredRate(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken: bifrost.Ptr(0.000003),
	}
	// Falls back to base rate when tiered field is nil
	assert.Equal(t, 0.000003, tieredInputRate(&p, 300000, serviceTier{}))
}

func TestTieredOutputRate_AboveThreshold(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		OutputCostPerToken:                bifrost.Ptr(0.000015),
		OutputCostPerTokenAbove200kTokens: bifrost.Ptr(0.00003),
	}
	assert.Equal(t, 0.00003, tieredOutputRate(&p, 250000, serviceTier{}))
}

// =========================================================================
// 9. extractCostInput — usage extraction
// =========================================================================

func TestExtractCostInput_ChatResponse(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{Usage: usage},
	}
	input := extractCostInput(resp)
	require.NotNil(t, input.usage)
	assert.Equal(t, 100, input.usage.PromptTokens)
	assert.Equal(t, 50, input.usage.CompletionTokens)
}

func TestExtractCostInput_EmbeddingResponse(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{PromptTokens: 200, TotalTokens: 200}
	resp := &schemas.BifrostResponse{
		EmbeddingResponse: &schemas.BifrostEmbeddingResponse{Usage: usage},
	}
	input := extractCostInput(resp)
	require.NotNil(t, input.usage)
	assert.Equal(t, 200, input.usage.PromptTokens)
}

func TestExtractCostInput_ImageResponse(t *testing.T) {
	imgUsage := &schemas.ImageUsage{InputTokens: 100, OutputTokens: 200, TotalTokens: 300}
	resp := &schemas.BifrostResponse{
		ImageGenerationResponse: &schemas.BifrostImageGenerationResponse{Usage: imgUsage},
	}
	input := extractCostInput(resp)
	assert.Nil(t, input.usage)
	require.NotNil(t, input.imageUsage)
	assert.Equal(t, 300, input.imageUsage.TotalTokens)
}

func TestExtractCostInput_TranscriptionWithSeconds(t *testing.T) {
	sec := 60.0
	resp := &schemas.BifrostResponse{
		TranscriptionResponse: &schemas.BifrostTranscriptionResponse{
			Usage: &schemas.TranscriptionUsage{
				Seconds:      &sec,
				InputTokens:  bifrost.Ptr(1000),
				OutputTokens: bifrost.Ptr(200),
				TotalTokens:  bifrost.Ptr(1200),
			},
		},
	}
	input := extractCostInput(resp)
	require.NotNil(t, input.usage)
	require.NotNil(t, input.audioSeconds)
	assert.Equal(t, 60, *input.audioSeconds)
	assert.Equal(t, 1000, input.usage.PromptTokens)
}

func TestExtractCostInput_SpeechResponse(t *testing.T) {
	resp := &schemas.BifrostResponse{
		SpeechResponse: &schemas.BifrostSpeechResponse{
			Usage: &schemas.SpeechUsage{
				InputTokens:  100,
				OutputTokens: 500,
				TotalTokens:  600,
			},
		},
	}
	input := extractCostInput(resp)
	require.NotNil(t, input.usage)
	assert.Equal(t, 100, input.usage.PromptTokens)
	assert.Equal(t, 500, input.usage.CompletionTokens)
	assert.Equal(t, 600, input.usage.TotalTokens)
}

func TestExtractCostInput_VideoResponse(t *testing.T) {
	sec := "15"
	resp := &schemas.BifrostResponse{
		VideoGenerationResponse: &schemas.BifrostVideoGenerationResponse{
			Seconds: &sec,
		},
	}
	input := extractCostInput(resp)
	require.NotNil(t, input.videoSeconds)
	assert.Equal(t, 15, *input.videoSeconds)
}

func TestExtractCostInput_VideoResponseInvalidSeconds(t *testing.T) {
	sec := "not-a-number"
	resp := &schemas.BifrostResponse{
		VideoGenerationResponse: &schemas.BifrostVideoGenerationResponse{
			Seconds: &sec,
		},
	}
	input := extractCostInput(resp)
	assert.Nil(t, input.videoSeconds)
}

// =========================================================================
// 10. Semantic cache billing (calculateCostWithCache)
// =========================================================================

func TestCalculateCost_SemanticCacheDirectHit(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): {
			Model: "gpt-4o", Provider: "openai", Mode: "chat",
			InputCostPerToken: bifrost.Ptr(0.000005), OutputCostPerToken: bifrost.Ptr(0.000015),
		},
	})

	hitType := "direct"
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-4o"),
				CacheDebug: &schemas.BifrostCacheDebug{
					CacheHit: true,
					HitType:  &hitType,
				},
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	assert.Equal(t, 0.0, cost)
}

func TestCalculateCost_SemanticCacheSemanticHit(t *testing.T) {
	embProvider := "openai"
	embModel := "text-embedding-3-small"
	embTokens := 500

	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): {
			Model: "gpt-4o", Provider: "openai", Mode: "chat",
			InputCostPerToken: bifrost.Ptr(0.000005), OutputCostPerToken: bifrost.Ptr(0.000015),
		},
		makeKey("text-embedding-3-small", "openai", "embedding"): {
			Model: "text-embedding-3-small", Provider: "openai", Mode: "embedding",
			InputCostPerToken: bifrost.Ptr(0.00000002),
		},
	})

	hitType := "semantic"
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-4o"),
				CacheDebug: &schemas.BifrostCacheDebug{
					CacheHit:     true,
					HitType:      &hitType,
					ProviderUsed: &embProvider,
					ModelUsed:    &embModel,
					InputTokens:  &embTokens,
				},
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// Only embedding cost: 500 * 0.00000002 = 0.00001
	assert.InDelta(t, 0.00001, cost, 1e-12)
}

func TestCalculateCost_SemanticCacheMiss(t *testing.T) {
	embProvider := "openai"
	embModel := "text-embedding-3-small"
	embTokens := 500

	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): {
			Model: "gpt-4o", Provider: "openai", Mode: "chat",
			InputCostPerToken: bifrost.Ptr(0.000005), OutputCostPerToken: bifrost.Ptr(0.000015),
		},
		makeKey("text-embedding-3-small", "openai", "embedding"): {
			Model: "text-embedding-3-small", Provider: "openai", Mode: "embedding",
			InputCostPerToken: bifrost.Ptr(0.00000002),
		},
	})

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-4o"),
				CacheDebug: &schemas.BifrostCacheDebug{
					CacheHit:     false,
					ProviderUsed: &embProvider,
					ModelUsed:    &embModel,
					InputTokens:  &embTokens,
				},
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// Base cost: 1000*0.000005 + 500*0.000015 = 0.005 + 0.0075 = 0.0125
	// Embedding cost: 500 * 0.00000002 = 0.00001
	// Total: 0.01251
	assert.InDelta(t, 0.01251, cost, 1e-12)
}

func TestCalculateCost_SemanticCacheHitNoEmbeddingInfo(t *testing.T) {
	s := testStoreWithPricing(nil)

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				CacheDebug: &schemas.BifrostCacheDebug{
					CacheHit: true,
					// No ProviderUsed, ModelUsed, InputTokens
				},
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	assert.Equal(t, 0.0, cost)
}

// =========================================================================
// 11. CalculateCost integration — end-to-end
// =========================================================================

func TestCalculateCost_NilResponse(t *testing.T) {
	s := testStoreWithPricing(nil)
	assert.Equal(t, 0.0, s.CalculateCost(nil, nil))
}

func TestCalculateCost_ProviderComputedCostPassthrough(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	resp := makeChatResponse(schemas.OpenAI, "gpt-4o", &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		Cost: &schemas.BifrostCost{
			TotalCost: 0.99, // Provider already calculated
		},
	})

	cost := s.CalculateCost(resp, nil)
	assert.Equal(t, 0.99, cost)
}

func TestCalculateCost_NoUsageData(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	resp := makeChatResponse(schemas.OpenAI, "gpt-4o", nil)
	cost := s.CalculateCost(resp, nil)
	assert.Equal(t, 0.0, cost)
}

func TestCalculateCost_ChatCompletion_GPT4o(t *testing.T) {
	// GPT-4o: $5/M input, $15/M output, cache_read=$0.5/M
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): {
			Model: "gpt-4o", Provider: "openai", Mode: "chat",
			InputCostPerToken:       bifrost.Ptr(0.000005),
			OutputCostPerToken:      bifrost.Ptr(0.000015),
			CacheReadInputTokenCost: bifrost.Ptr(0.0000005),
		},
	})

	resp := makeChatResponse(schemas.OpenAI, "gpt-4o", &schemas.BifrostLLMUsage{
		PromptTokens:     10000,
		CompletionTokens: 2000,
		TotalTokens:      12000,
	})

	cost := s.CalculateCost(resp, nil)
	// 10000*0.000005 + 2000*0.000015 = 0.05 + 0.03 = 0.08
	assert.InDelta(t, 0.08, cost, 1e-12)
}

func TestCalculateCost_ChatCompletion_Claude35Sonnet_WithCache(t *testing.T) {
	// Claude 3.5 Sonnet (Bedrock): $3/M input, $15/M output, cache_read=$0.3/M, cache_creation=$3.75/M
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("anthropic.claude-3-5-sonnet-20241022-v2:0", "bedrock", "chat"): {
			Model: "anthropic.claude-3-5-sonnet-20241022-v2:0", Provider: "bedrock", Mode: "chat",
			InputCostPerToken:                 bifrost.Ptr(0.000003),
			OutputCostPerToken:                bifrost.Ptr(0.000015),
			CacheReadInputTokenCost:           bifrost.Ptr(0.0000003),
			CacheCreationInputTokenCost:       bifrost.Ptr(0.00000375),
			InputCostPerTokenAbove200kTokens:  bifrost.Ptr(0.000006),
			OutputCostPerTokenAbove200kTokens: bifrost.Ptr(0.00003),
		},
	})

	resp := makeChatResponse(schemas.Bedrock, "anthropic.claude-3-5-sonnet-20241022-v2:0", &schemas.BifrostLLMUsage{
		PromptTokens:     5000,
		CompletionTokens: 1000,
		TotalTokens:      6000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  3000, // 3000 cache read tokens
			CachedWriteTokens: 500,  // 500 cache creation tokens
		},
	})

	cost := s.CalculateCost(resp, nil)
	// Both cached read and write tokens are input-side deductions from promptTokens.
	// Input: (5000-3000-500)*0.000003 + 3000*0.0000003 + 500*0.00000375 = 0.0045 + 0.0009 + 0.001875 = 0.007275
	// Output: 1000*0.000015 = 0.015
	// Total: 0.007275 + 0.015 = 0.022275
	assert.InDelta(t, 0.022275, cost, 1e-12)
}

func TestCalculateCost_Embedding(t *testing.T) {
	// Titan Embed Text v1: $0.1/M input
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("amazon.titan-embed-text-v1", "bedrock", "embedding"): {
			Model: "amazon.titan-embed-text-v1", Provider: "bedrock", Mode: "embedding",
			InputCostPerToken:  bifrost.Ptr(0.0000001),
			OutputCostPerToken: bifrost.Ptr(0.0),
		},
	})

	resp := makeEmbeddingResponse(schemas.Bedrock, "amazon.titan-embed-text-v1", &schemas.BifrostLLMUsage{
		PromptTokens: 10000,
		TotalTokens:  10000,
	})

	cost := s.CalculateCost(resp, nil)
	// 10000 * 0.0000001 = 0.001
	assert.InDelta(t, 0.001, cost, 1e-12)
}

func TestCalculateCost_Rerank(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("amazon.rerank-v1:0", "bedrock", "rerank"): {
			Model: "amazon.rerank-v1:0", Provider: "bedrock", Mode: "rerank",
			InputCostPerToken:  bifrost.Ptr(0.0),
			OutputCostPerToken: bifrost.Ptr(0.0),
		},
	})

	resp := makeRerankResponse(schemas.Bedrock, "amazon.rerank-v1:0", &schemas.BifrostLLMUsage{
		PromptTokens: 500,
		TotalTokens:  500,
	})

	cost := s.CalculateCost(resp, nil)
	assert.Equal(t, 0.0, cost)
}

func TestCalculateCost_ImageGeneration(t *testing.T) {
	// dall-e-3 via aiml: output_cost_per_image=$0.052
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("dall-e-3", "aiml", "image_generation"): {
			Model: "dall-e-3", Provider: "aiml", Mode: "image_generation",
			OutputCostPerImage: bifrost.Ptr(0.052),
		},
	})

	resp := makeImageResponse("aiml", "dall-e-3", &schemas.ImageUsage{
		OutputTokensDetails: &schemas.ImageTokenDetails{NImages: 3},
	})

	cost := s.CalculateCost(resp, nil)
	// 3 * 0.052 = 0.156
	assert.InDelta(t, 0.156, cost, 1e-12)
}

func TestCalculateCost_StreamRequestTypeNormalized(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	// Stream request type should be normalized to base type
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionStreamRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-4o"),
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestCalculateCost_WebSocketResponsesFallsBackToChatPricing(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	resp := &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Response: &schemas.BifrostResponsesResponse{
				Usage: &schemas.ResponsesResponseUsage{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.WebSocketResponsesRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-4o"),
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestCalculateCost_NoPricingData(t *testing.T) {
	s := testStoreWithPricing(nil)
	resp := makeChatResponse(schemas.OpenAI, "unknown-model", &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
	})
	cost := s.CalculateCost(resp, nil)
	assert.Equal(t, 0.0, cost)
}

// =========================================================================
// 12. Pricing resolution — getPricing fallback logic
// =========================================================================

func TestGetPricing_DirectLookup(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	assert.Equal(t, 0.000005, derefF(p.InputCostPerToken))
}

func TestGetPricing_GeminiFallsBackToVertex(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gemini-2.0-flash", "vertex", "chat"): {
			Model: "gemini-2.0-flash", Provider: "vertex", Mode: "chat",
			InputCostPerToken: bifrost.Ptr(0.0000001), OutputCostPerToken: bifrost.Ptr(0.0000004),
		},
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "gemini", Model: "gemini-2.0-flash"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "gemini"})
	assert.Equal(t, 0.0000001, derefF(p.InputCostPerToken))
}

func TestGetPricing_VertexStripsProviderPrefix(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gemini-2.0-flash", "vertex", "chat"): chatPricing(0.0000001, 0.0000004),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "vertex", Model: "google/gemini-2.0-flash"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "vertex"})
	assert.Equal(t, 0.0000001, derefF(p.InputCostPerToken))
}

func TestGetPricing_BedrockAddsAnthropicPrefix(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("anthropic.claude-3-5-sonnet-20241022-v2:0", "bedrock", "chat"): chatPricing(0.000003, 0.000015),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "bedrock", Model: "claude-3-5-sonnet-20241022-v2:0"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "bedrock"})
	assert.Equal(t, 0.000003, derefF(p.InputCostPerToken))
}

func TestGetPricing_BedrockAddsOpenAIPrefix(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("openai.gpt-oss-120b", "bedrock", "chat"): chatPricing(0.00000015, 0.0000006),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "bedrock", Model: "gpt-oss-120b"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "bedrock"})
	require.NotNil(t, p)
	assert.Equal(t, 0.00000015, derefF(p.InputCostPerToken))
}

func TestGetPricing_BedrockAddsGooglePrefix(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("google.gemma-4-31b", "bedrock", "chat"): chatPricing(0.00000014, 0.0000004),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "bedrock", Model: "gemma-4-31b"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "bedrock"})
	require.NotNil(t, p)
	assert.Equal(t, 0.00000014, derefF(p.InputCostPerToken))
}

func TestGetPricing_BedrockAddsXAIPrefix(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("xai.grok-4.3", "bedrock", "chat"): chatPricing(0.00000125, 0.0000025),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "bedrock", Model: "grok-4.3"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "bedrock"})
	require.NotNil(t, p)
	assert.Equal(t, 0.00000125, derefF(p.InputCostPerToken))
}

func TestGetPricing_BedrockMantleFallsBackToBedrock(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("openai.gpt-oss-120b", "bedrock", "chat"): chatPricing(0.00000015, 0.0000006),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "bedrock_mantle", Model: "openai.gpt-oss-120b"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "bedrock_mantle"})
	require.NotNil(t, p)
	assert.Equal(t, 0.00000015, derefF(p.InputCostPerToken))
}

func TestGetPricing_BedrockMantleResponsesFallsBackToBedrockChat(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("openai.gpt-oss-120b", "bedrock", "chat"): chatPricing(0.00000015, 0.0000006),
	})
	// bedrock_mantle provider + responses request → try bedrock + responses → try bedrock + chat
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "bedrock_mantle", Model: "openai.gpt-oss-120b"}, schemas.ResponsesRequest, LookupScopes{Provider: "bedrock_mantle"})
	require.NotNil(t, p)
	assert.Equal(t, 0.00000015, derefF(p.InputCostPerToken))
}

func TestGetPricing_BedrockMantleAddsAnthropicPrefix(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("anthropic.claude-3-5-sonnet-20241022-v2:0", "bedrock", "chat"): chatPricing(0.000003, 0.000015),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "bedrock_mantle", Model: "claude-3-5-sonnet-20241022-v2:0"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "bedrock_mantle"})
	require.NotNil(t, p)
	assert.Equal(t, 0.000003, derefF(p.InputCostPerToken))
}

func TestGetPricing_BedrockMantleAddsOpenAIPrefix(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("openai.gpt-oss-120b", "bedrock", "chat"): chatPricing(0.00000015, 0.0000006),
	})
	// bedrock_mantle folds onto bedrock, then the openai. prefix retry fires
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "bedrock_mantle", Model: "gpt-oss-120b"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "bedrock_mantle"})
	require.NotNil(t, p)
	assert.Equal(t, 0.00000015, derefF(p.InputCostPerToken))
}

func TestGetPricing_BedrockMantleResponsesAddsOpenAIPrefix(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("openai.gpt-5.5", "bedrock", "responses"): chatPricing(0.0000055, 0.000033),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "bedrock_mantle", Model: "gpt-5.5"}, schemas.ResponsesRequest, LookupScopes{Provider: "bedrock_mantle"})
	require.NotNil(t, p)
	assert.Equal(t, 0.0000055, derefF(p.InputCostPerToken))
}

func TestGetPricing_ResponsesFallsBackToChat(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ResponsesRequest, LookupScopes{Provider: "openai"})
	assert.Equal(t, 0.000005, derefF(p.InputCostPerToken))
}

func TestGetPricing_ResponsesStreamFallsBackToChat(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ResponsesStreamRequest, LookupScopes{Provider: "openai"})
	assert.Equal(t, 0.000005, derefF(p.InputCostPerToken))
}

func TestGetPricing_RealtimeFallsBackToChat(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.RealtimeRequest, LookupScopes{Provider: "openai"})
	assert.Equal(t, 0.000005, derefF(p.InputCostPerToken))
}

func TestGetPricing_ChatFallsBackToResponses(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "responses"): chatPricing(0.000005, 0.000015),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, p)
	assert.Equal(t, 0.000005, derefF(p.InputCostPerToken))
}

func TestGetPricing_ChatStreamFallsBackToResponses(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "responses"): chatPricing(0.000005, 0.000015),
	})
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ChatCompletionStreamRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, p)
	assert.Equal(t, 0.000005, derefF(p.InputCostPerToken))
}

func TestGetPricing_BedrockMantleChatStreamFallsBackToBedrockResponses(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("openai.gpt-5.5", "bedrock", "responses"): chatPricing(0.0000055, 0.000033),
	})
	// The datasheet files openai.gpt-5.5 as responses-only, but bedrock serves it
	// over chat completions too: bedrock_mantle folds onto bedrock, chat mode
	// misses, and the responses row is used.
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "bedrock_mantle", Model: "openai.gpt-5.5"}, schemas.ChatCompletionStreamRequest, LookupScopes{Provider: "bedrock_mantle"})
	require.NotNil(t, p)
	assert.Equal(t, 0.0000055, derefF(p.InputCostPerToken))
	assert.Equal(t, 0.000033, derefF(p.OutputCostPerToken))
}

func TestGetPricing_BedrockChatAddsOpenAIPrefixThenFallsBackToResponses(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("openai.gpt-5.5", "bedrock", "responses"): chatPricing(0.0000055, 0.000033),
	})
	// Bare model name + chat request: the openai. prefix retry fires, then the
	// prefixed key falls back to responses mode.
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "bedrock", Model: "gpt-5.5"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "bedrock"})
	require.NotNil(t, p)
	assert.Equal(t, 0.0000055, derefF(p.InputCostPerToken))
}

func TestGetPricing_ExactModeWinsOverFallback(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"):      chatPricing(0.000005, 0.000015),
		makeKey("gpt-4o", "openai", "responses"): chatPricing(0.000009, 0.000036),
	})
	chat := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, chat)
	assert.Equal(t, 0.000005, derefF(chat.InputCostPerToken))

	responses := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ResponsesRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, responses)
	assert.Equal(t, 0.000009, derefF(responses.InputCostPerToken))
}

func TestGetPricing_GeminiChatFallsBackToVertexResponses(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gemini-2.0-flash", "vertex", "responses"): chatPricing(0.0000001, 0.0000004),
	})
	// gemini provider + chat request → try vertex + chat → try vertex + responses
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "gemini", Model: "gemini-2.0-flash"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "gemini"})
	require.NotNil(t, p)
	assert.Equal(t, 0.0000001, derefF(p.InputCostPerToken))
}

func TestGetPricing_GeminiResponsesFallsBackToVertexChat(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gemini-2.0-flash", "vertex", "chat"): chatPricing(0.0000001, 0.0000004),
	})
	// gemini provider + responses request → try vertex + responses → try vertex + chat
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "gemini", Model: "gemini-2.0-flash"}, schemas.ResponsesRequest, LookupScopes{Provider: "gemini"})
	assert.Equal(t, 0.0000001, derefF(p.InputCostPerToken))
}

func TestGetPricing_NotFound(t *testing.T) {
	s := testStoreWithPricing(nil)
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "nonexistent"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	assert.Nil(t, p)
}

// =========================================================================
// 13. resolvePricing — deployment fallback
// =========================================================================

func TestResolvePricing_DeploymentFallback(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("my-deployment", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	// Model not found directly, but deployment matches
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o-custom", ResolvedKeyAlias: &schemas.ResolvedKeyAlias{ModelID: "my-deployment"}}, schemas.ChatCompletionRequest, LookupScopes{})
	require.NotNil(t, p)
	assert.Equal(t, 0.000005, derefF(p.InputCostPerToken))
}

func TestResolvePricing_ResolvedModelHasPriority(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"):        chatPricing(0.000005, 0.000015),
		makeKey("my-deployment", "openai", "chat"): chatPricing(0.000001, 0.000002),
	})

	// Resolved model ("my-deployment") is looked up first and has priority
	// over the originally requested model ("gpt-4o").
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o", ResolvedKeyAlias: &schemas.ResolvedKeyAlias{ModelID: "my-deployment"}}, schemas.ChatCompletionRequest, LookupScopes{})
	require.NotNil(t, p)
	assert.Equal(t, 0.000001, derefF(p.InputCostPerToken))
}

func TestResolvePricing_NothingFound(t *testing.T) {
	s := testStoreWithPricing(nil)
	p := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "unknown"}, schemas.ChatCompletionRequest, LookupScopes{})
	assert.Nil(t, p)
}

// =========================================================================
// 14. normalizeStreamRequestType
// =========================================================================

func TestNormalizeStreamRequestType(t *testing.T) {
	tests := []struct {
		input    schemas.RequestType
		expected schemas.RequestType
	}{
		{schemas.ChatCompletionStreamRequest, schemas.ChatCompletionRequest},
		{schemas.TextCompletionStreamRequest, schemas.TextCompletionRequest},
		{schemas.ResponsesStreamRequest, schemas.ResponsesRequest},
		{schemas.SpeechStreamRequest, schemas.SpeechRequest},
		{schemas.TranscriptionStreamRequest, schemas.TranscriptionRequest},
		{schemas.ImageGenerationStreamRequest, schemas.ImageGenerationRequest},
		{schemas.ImageEditStreamRequest, schemas.ImageEditRequest},
		{schemas.RealtimeRequest, schemas.RealtimeRequest},             // realtime is its own base type
		{schemas.ChatCompletionRequest, schemas.ChatCompletionRequest}, // non-stream unchanged
		{schemas.EmbeddingRequest, schemas.EmbeddingRequest},           // non-stream unchanged
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, normalizeStreamRequestType(tt.input), "for input %s", tt.input)
	}
}

// =========================================================================
// 15. responsesUsageToBifrostUsage
// =========================================================================

func TestResponsesUsageToBifrostUsage_Basic(t *testing.T) {
	u := &schemas.ResponsesResponseUsage{
		InputTokens:  100,
		OutputTokens: 50,
		TotalTokens:  150,
	}
	result := responsesUsageToBifrostUsage(u)
	assert.Equal(t, 100, result.PromptTokens)
	assert.Equal(t, 50, result.CompletionTokens)
	assert.Equal(t, 150, result.TotalTokens)
	assert.Nil(t, result.PromptTokensDetails)
	assert.Nil(t, result.CompletionTokensDetails)
}

func TestResponsesUsageToBifrostUsage_WithTokenDetails(t *testing.T) {
	numQueries := 2
	u := &schemas.ResponsesResponseUsage{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
		InputTokensDetails: &schemas.ResponsesResponseInputTokens{
			CachedReadTokens:  300,
			CachedWriteTokens: 50,
			TextTokens:        600,
			AudioTokens:       50,
			ImageTokens:       50,
		},
		OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{
			ReasoningTokens:  100,
			NumSearchQueries: &numQueries,
		},
	}
	result := responsesUsageToBifrostUsage(u)

	require.NotNil(t, result.PromptTokensDetails)
	assert.Equal(t, 300, result.PromptTokensDetails.CachedReadTokens)
	assert.Equal(t, 50, result.PromptTokensDetails.CachedWriteTokens)
	assert.Equal(t, 600, result.PromptTokensDetails.TextTokens)
	assert.Equal(t, 50, result.PromptTokensDetails.AudioTokens)
	assert.Equal(t, 50, result.PromptTokensDetails.ImageTokens)

	require.NotNil(t, result.CompletionTokensDetails)
	assert.Equal(t, 100, result.CompletionTokensDetails.ReasoningTokens)
	require.NotNil(t, result.CompletionTokensDetails.NumSearchQueries)
	assert.Equal(t, 2, *result.CompletionTokensDetails.NumSearchQueries)
}

// =========================================================================
// 16. Edge cases
// =========================================================================

func TestCalculateCost_200kTier_EndToEnd(t *testing.T) {
	// Claude 3.5 Sonnet Bedrock with 200k tier pricing
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("anthropic.claude-3-5-sonnet-20240620-v1:0", "bedrock", "chat"): {
			Model: "anthropic.claude-3-5-sonnet-20240620-v1:0", Provider: "bedrock", Mode: "chat",
			InputCostPerToken:                          bifrost.Ptr(0.000003),
			OutputCostPerToken:                         bifrost.Ptr(0.000015),
			InputCostPerTokenAbove200kTokens:           bifrost.Ptr(0.000006),
			OutputCostPerTokenAbove200kTokens:          bifrost.Ptr(0.00003),
			CacheReadInputTokenCost:                    bifrost.Ptr(0.0000003),
			CacheCreationInputTokenCost:                bifrost.Ptr(0.00000375),
			CacheReadInputTokenCostAbove200kTokens:     bifrost.Ptr(0.0000006),
			CacheCreationInputTokenCostAbove200kTokens: bifrost.Ptr(0.0000075),
		},
	})

	resp := makeChatResponse(schemas.Bedrock, "anthropic.claude-3-5-sonnet-20240620-v1:0", &schemas.BifrostLLMUsage{
		PromptTokens:     210000,
		CompletionTokens: 20000,
		TotalTokens:      230000, // input is above 200k
	})

	cost := s.CalculateCost(resp, nil)
	// Tiered rate: input=0.000006, output=0.00003
	// 210000*0.000006 + 20000*0.00003 = 1.26 + 0.6 = 1.86
	assert.InDelta(t, 1.86, cost, 1e-9)
}

func TestCalculateCost_272kTier_EndToEnd(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("claude-3-7-sonnet", "anthropic", "chat"): {
			Model:                                  "claude-3-7-sonnet",
			Provider:                               "anthropic",
			Mode:                                   "chat",
			InputCostPerToken:                      new(0.000003),
			OutputCostPerToken:                     new(0.000015),
			InputCostPerTokenAbove200kTokens:       new(0.000006),
			OutputCostPerTokenAbove200kTokens:      new(0.00003),
			InputCostPerTokenAbove272kTokens:       new(0.000009),
			OutputCostPerTokenAbove272kTokens:      new(0.000045),
			CacheReadInputTokenCost:                new(0.0000003),
			CacheReadInputTokenCostAbove200kTokens: new(0.0000006),
			CacheReadInputTokenCostAbove272kTokens: new(0.0000009),
		},
	})

	resp := makeChatResponse(schemas.Anthropic, "claude-3-7-sonnet", &schemas.BifrostLLMUsage{
		PromptTokens:     280000,
		CompletionTokens: 30000,
		TotalTokens:      310000, // input is above 272k
	})

	cost := s.CalculateCost(resp, nil)
	// Tiered rate: input=0.000009, output=0.000045
	// 280000*0.000009 + 30000*0.000045 = 2.52 + 1.35 = 3.87
	assert.InDelta(t, 3.87, cost, 1e-9)
}

func TestCalculateCost_272kTier_CacheReadFallbackChain(t *testing.T) {
	// Verifies the 272k cache read rate takes precedence over 200k and base rates
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("claude-3-7-sonnet", "anthropic", "chat"): {
			Model:                                  "claude-3-7-sonnet",
			Provider:                               "anthropic",
			Mode:                                   "chat",
			InputCostPerToken:                      new(0.000003),
			OutputCostPerToken:                     new(0.000015),
			InputCostPerTokenAbove272kTokens:       new(0.000009),
			OutputCostPerTokenAbove272kTokens:      new(0.000045),
			CacheReadInputTokenCost:                new(0.0000003),
			CacheReadInputTokenCostAbove200kTokens: new(0.0000006),
			CacheReadInputTokenCostAbove272kTokens: new(0.0000009),
		},
	})

	resp := makeChatResponse(schemas.Anthropic, "claude-3-7-sonnet", &schemas.BifrostLLMUsage{
		PromptTokens:     280000,
		CompletionTokens: 30000,
		TotalTokens:      310000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens: 50000,
		},
	})

	cost := s.CalculateCost(resp, nil)
	// Non-cached input: (280000-50000) * 0.000009 = 230000 * 0.000009 = 2.07
	// Cached read (272k rate): 50000 * 0.0000009 = 0.045
	// Output: 30000 * 0.000045 = 1.35
	// Total: 2.07 + 0.045 + 1.35 = 3.465
	assert.InDelta(t, 3.465, cost, 1e-9)
}

// =========================================================================
// Priority tier tests
// =========================================================================

func TestComputeTextCost_PriorityUsesInputOutputPriorityRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenPriority = new(0.000006)
	p.OutputCostPerTokenPriority = new(0.00003)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}

	cost := computeTextCost(&p, usage, serviceTier{isPriority: true})

	// Uses priority rates: 1000*0.000006 + 500*0.00003 = 0.006 + 0.015 = 0.021
	assert.InDelta(t, 0.021, cost, 1e-12)
}

func TestComputeTextCost_NonPriorityIgnoresPriorityRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenPriority = new(0.000006)
	p.OutputCostPerTokenPriority = new(0.00003)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Uses base rates, ignores priority fields: 1000*0.000003 + 500*0.000015 = 0.003 + 0.0075 = 0.0105
	assert.InDelta(t, 0.0105, cost, 1e-12)
}

func TestComputeTextCost_Priority272kTier(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenPriority = new(0.000006)
	p.OutputCostPerTokenPriority = new(0.00003)
	p.InputCostPerTokenAbove272kTokens = new(0.000009)
	p.InputCostPerTokenAbove272kTokensPriority = new(0.000012)
	p.OutputCostPerTokenAbove272kTokens = new(0.000045)
	p.OutputCostPerTokenAbove272kTokensPriority = new(0.00006)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     280000,
		CompletionTokens: 30000,
		TotalTokens:      310000,
	}

	cost := computeTextCost(&p, usage, serviceTier{isPriority: true})

	// Uses 272k priority rates: 280000*0.000012 + 30000*0.00006 = 3.36 + 1.80 = 5.16
	assert.InDelta(t, 5.16, cost, 1e-9)
}

func TestComputeTextCost_Priority272kTierFallsBackToNonPriority272k(t *testing.T) {
	// Priority flag set but no priority-specific 272k rate — fall back to non-priority 272k
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove272kTokens = new(0.000009)
	p.OutputCostPerTokenAbove272kTokens = new(0.000045)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     280000,
		CompletionTokens: 30000,
		TotalTokens:      310000,
	}

	cost := computeTextCost(&p, usage, serviceTier{isPriority: true})

	// Falls back to non-priority 272k rate: 280000*0.000009 + 30000*0.000045 = 2.52 + 1.35 = 3.87
	assert.InDelta(t, 3.87, cost, 1e-9)
}

func TestComputeTextCost_PriorityCacheReadRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenPriority = new(0.000006)
	p.OutputCostPerTokenPriority = new(0.00003)
	p.CacheReadInputTokenCost = new(0.0000003)
	p.CacheReadInputTokenCostPriority = new(0.0000006)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens: 400,
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{isPriority: true})

	// Non-cached input: (1000-400)*0.000006 = 600*0.000006 = 0.0036
	// Cached read (priority rate): 400*0.0000006 = 0.00024
	// Output: 500*0.00003 = 0.015
	// Total: 0.0036 + 0.00024 + 0.015 = 0.01884
	assert.InDelta(t, 0.01884, cost, 1e-12)
}

func TestCalculateCost_PriorityTier_EndToEnd(t *testing.T) {
	tier := schemas.BifrostServiceTierPriority
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): {
			Model:                      "gpt-4o",
			Provider:                   "openai",
			Mode:                       "chat",
			InputCostPerToken:          new(0.000005),
			OutputCostPerToken:         new(0.000015),
			InputCostPerTokenPriority:  new(0.000010),
			OutputCostPerTokenPriority: new(0.000030),
		},
	})

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ServiceTier: &tier,
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-4o"),
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// Priority rates: 1000*0.000010 + 500*0.000030 = 0.010 + 0.015 = 0.025
	assert.InDelta(t, 0.025, cost, 1e-12)
}

func TestCalculateCost_NonPriorityServiceTier_UsesBaseRate(t *testing.T) {
	tier := schemas.BifrostServiceTierAuto
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): {
			Model:                      "gpt-4o",
			Provider:                   "openai",
			Mode:                       "chat",
			InputCostPerToken:          new(0.000005),
			OutputCostPerToken:         new(0.000015),
			InputCostPerTokenPriority:  new(0.000010),
			OutputCostPerTokenPriority: new(0.000030),
		},
	})

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ServiceTier: &tier,
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-4o"),
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// Base rates (not priority): 1000*0.000005 + 500*0.000015 = 0.005 + 0.0075 = 0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestTieredCacheReadRate_FallbackOrder(t *testing.T) {
	// 272k rate takes precedence over 200k, 200k over base, base over input rate
	t.Run("uses_272k_when_above_272k", func(t *testing.T) {
		p := chatPricing(0.000003, 0.000015)
		p.CacheReadInputTokenCost = new(0.0000003)
		p.CacheReadInputTokenCostAbove200kTokens = new(0.0000006)
		p.CacheReadInputTokenCostAbove272kTokens = new(0.0000009)
		assert.Equal(t, 0.0000009, tieredCacheReadInputTokenRate(&p, 280000, serviceTier{}))
	})
	t.Run("uses_200k_when_between_200k_and_272k", func(t *testing.T) {
		p := chatPricing(0.000003, 0.000015)
		p.CacheReadInputTokenCost = new(0.0000003)
		p.CacheReadInputTokenCostAbove200kTokens = new(0.0000006)
		p.CacheReadInputTokenCostAbove272kTokens = new(0.0000009)
		assert.Equal(t, 0.0000006, tieredCacheReadInputTokenRate(&p, 230000, serviceTier{}))
	})
	t.Run("uses_base_cache_rate_when_below_200k", func(t *testing.T) {
		p := chatPricing(0.000003, 0.000015)
		p.CacheReadInputTokenCost = new(0.0000003)
		p.CacheReadInputTokenCostAbove200kTokens = new(0.0000006)
		p.CacheReadInputTokenCostAbove272kTokens = new(0.0000009)
		assert.Equal(t, 0.0000003, tieredCacheReadInputTokenRate(&p, 1500, serviceTier{}))
	})
	t.Run("falls_back_to_input_rate_when_no_cache_rate_set", func(t *testing.T) {
		p := chatPricing(0.000003, 0.000015)
		// No cache rates set at all
		assert.Equal(t, 0.000003, tieredCacheReadInputTokenRate(&p, 280000, serviceTier{}))
	})
	t.Run("priority_uses_272k_priority_rate", func(t *testing.T) {
		p := chatPricing(0.000003, 0.000015)
		p.CacheReadInputTokenCost = new(0.0000003)
		p.CacheReadInputTokenCostPriority = new(0.0000006)
		p.CacheReadInputTokenCostAbove272kTokens = new(0.0000009)
		p.CacheReadInputTokenCostAbove272kTokensPriority = new(0.0000012)
		assert.Equal(t, 0.0000012, tieredCacheReadInputTokenRate(&p, 280000, serviceTier{isPriority: true}))
	})
	t.Run("priority_falls_back_to_272k_non_priority_when_priority_rate_missing", func(t *testing.T) {
		p := chatPricing(0.000003, 0.000015)
		p.CacheReadInputTokenCostAbove272kTokens = new(0.0000009)
		assert.Equal(t, 0.0000009, tieredCacheReadInputTokenRate(&p, 280000, serviceTier{isPriority: true}))
	})
	t.Run("priority_uses_priority_base_cache_rate_below_tiers", func(t *testing.T) {
		p := chatPricing(0.000003, 0.000015)
		p.CacheReadInputTokenCost = new(0.0000003)
		p.CacheReadInputTokenCostPriority = new(0.0000006)
		assert.Equal(t, 0.0000006, tieredCacheReadInputTokenRate(&p, 1500, serviceTier{isPriority: true}))
	})
	t.Run("flex_uses_flex_cache_rate", func(t *testing.T) {
		p := chatPricing(0.000003, 0.000015)
		p.CacheReadInputTokenCost = new(0.0000003)
		p.CacheReadInputTokenCostFlex = new(0.0000005)
		assert.Equal(t, 0.0000005, tieredCacheReadInputTokenRate(&p, 1500, serviceTier{isFlex: true}))
	})
	t.Run("flex_uses_flex_cache_rate_regardless_of_token_count", func(t *testing.T) {
		p := chatPricing(0.000003, 0.000015)
		p.CacheReadInputTokenCost = new(0.0000003)
		p.CacheReadInputTokenCostFlex = new(0.0000005)
		p.CacheReadInputTokenCostAbove272kTokens = new(0.0000009)
		// Even above 272k, flex flat rate takes precedence
		assert.Equal(t, 0.0000005, tieredCacheReadInputTokenRate(&p, 280000, serviceTier{isFlex: true}))
	})
	t.Run("flex_falls_back_to_base_cache_rate_when_no_flex_cache_rate", func(t *testing.T) {
		p := chatPricing(0.000003, 0.000015)
		p.CacheReadInputTokenCost = new(0.0000003)
		// No flex cache rate — falls back to base cache rate
		assert.Equal(t, 0.0000003, tieredCacheReadInputTokenRate(&p, 1500, serviceTier{isFlex: true}))
	})
	t.Run("flex_wins_over_272k_priority_and_priority_base_when_all_present", func(t *testing.T) {
		p := chatPricing(0.000003, 0.000015)
		p.CacheReadInputTokenCostAbove272kTokens = new(5e-7)
		p.CacheReadInputTokenCostFlex = new(1.3e-7)
		p.CacheReadInputTokenCostPriority = new(5e-7)
		p.CacheReadInputTokenCostAbove272kTokensPriority = new(0.000001)
		// token count exceeds 272k — but flex flat rate should still win
		assert.Equal(t, 1.3e-7, tieredCacheReadInputTokenRate(&p, 280000, serviceTier{isFlex: true}))
	})
}

// =========================================================================
// tierFromResponse tests
// =========================================================================

func TestTierFromResponse_Priority(t *testing.T) {
	s := schemas.BifrostServiceTierPriority
	tier := tierFromResponse(&s, nil, nil)
	assert.True(t, tier.isPriority)
	assert.False(t, tier.isFlex)
}

func TestTierFromResponse_Flex(t *testing.T) {
	s := schemas.BifrostServiceTierFlex
	tier := tierFromResponse(&s, nil, nil)
	assert.False(t, tier.isPriority)
	assert.True(t, tier.isFlex)
}

func TestTierFromResponse_Default(t *testing.T) {
	for _, s := range []schemas.BifrostServiceTier{schemas.BifrostServiceTierAuto, schemas.BifrostServiceTierDefault, ""} {
		tier := tierFromResponse(&s, nil, nil)
		assert.False(t, tier.isPriority, "expected no priority for %q", s)
		assert.False(t, tier.isFlex, "expected no flex for %q", s)
	}
}

func TestTierFromResponse_Nil(t *testing.T) {
	tier := tierFromResponse(nil, nil, nil)
	assert.False(t, tier.isPriority)
	assert.False(t, tier.isFlex)
}

// =========================================================================
// Flex tier tests
// =========================================================================

func TestComputeTextCost_FlexUsesFlexRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenFlex = new(0.0000015)
	p.OutputCostPerTokenFlex = new(0.0000075)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}

	cost := computeTextCost(&p, usage, serviceTier{isFlex: true})

	// Flex rates: 1000*0.0000015 + 500*0.0000075 = 0.0015 + 0.00375 = 0.00525
	assert.InDelta(t, 0.00525, cost, 1e-12)
}

func TestComputeTextCost_NonFlexIgnoresFlexRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenFlex = new(0.0000015)
	p.OutputCostPerTokenFlex = new(0.0000075)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}

	cost := computeTextCost(&p, usage, serviceTier{})

	// Base rates, flex fields ignored: 1000*0.000003 + 500*0.000015 = 0.003 + 0.0075 = 0.0105
	assert.InDelta(t, 0.0105, cost, 1e-12)
}

func TestComputeTextCost_FlexIgnoresTokenTiers(t *testing.T) {
	// Flex is a flat rate — token-count tiers (272k, 200k, 128k) do not apply.
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenFlex = new(0.0000015)
	p.OutputCostPerTokenFlex = new(0.0000075)
	p.InputCostPerTokenAbove272kTokens = new(0.000009)
	p.OutputCostPerTokenAbove272kTokens = new(0.000045)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     250000,
		CompletionTokens: 30000,
		TotalTokens:      280000,
	}

	cost := computeTextCost(&p, usage, serviceTier{isFlex: true})

	// Flex flat rate overrides 272k tier: 250000*0.0000015 + 30000*0.0000075 = 0.375 + 0.225 = 0.60
	assert.InDelta(t, 0.60, cost, 1e-9)
}

func TestComputeTextCost_FlexCacheReadRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenFlex = new(0.0000015)
	p.OutputCostPerTokenFlex = new(0.0000075)
	p.CacheReadInputTokenCost = new(0.0000003)
	p.CacheReadInputTokenCostFlex = new(0.0000006)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens: 400,
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{isFlex: true})

	// Non-cached input: (1000-400)*0.0000015 = 600*0.0000015 = 0.0009
	// Cached read (flex rate): 400*0.0000006 = 0.00024
	// Output: 500*0.0000075 = 0.00375
	// Total: 0.0009 + 0.00024 + 0.00375 = 0.00489
	assert.InDelta(t, 0.00489, cost, 1e-12)
}

func TestComputeTextCost_FlexFallsBackToBaseWhenNoFlexRate(t *testing.T) {
	// isFlex set but no flex fields configured — falls back to base rates.
	p := chatPricing(0.000003, 0.000015)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}

	cost := computeTextCost(&p, usage, serviceTier{isFlex: true})

	// Base rates used as fallback: 1000*0.000003 + 500*0.000015 = 0.003 + 0.0075 = 0.0105
	assert.InDelta(t, 0.0105, cost, 1e-12)
}

func TestCalculateCost_FlexTier_EndToEnd(t *testing.T) {
	tier := schemas.BifrostServiceTierFlex
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): {
			Model:                  "gpt-4o",
			Provider:               "openai",
			Mode:                   "chat",
			InputCostPerToken:      new(0.000005),
			OutputCostPerToken:     new(0.000015),
			InputCostPerTokenFlex:  new(0.0000025),
			OutputCostPerTokenFlex: new(0.0000075),
		},
	})

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ServiceTier: &tier,
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-4o"),
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// Flex rates: 1000*0.0000025 + 500*0.0000075 = 0.0025 + 0.00375 = 0.00625
	assert.InDelta(t, 0.00625, cost, 1e-12)
}

func TestCalculateCost_FlexTier_FallsBackToBaseWhenNoFlexRate(t *testing.T) {
	tier := schemas.BifrostServiceTierFlex
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ServiceTier: &tier,
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-4o"),
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// No flex rates configured — falls back to base: 1000*0.000005 + 500*0.000015 = 0.005 + 0.0075 = 0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestCalculateCost_ProviderCostZeroTotalStillCalculates(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	// Provider cost present but TotalCost is 0 → our calculation runs
	resp := makeChatResponse(schemas.OpenAI, "gpt-4o", &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		Cost: &schemas.BifrostCost{
			TotalCost: 0,
		},
	})

	cost := s.CalculateCost(resp, nil)
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestCalculateCost_AllCachedTokens(t *testing.T) {
	// All prompt tokens are from cache
	p := chatPricing(0.000005, 0.000015)
	p.CacheReadInputTokenCost = bifrost.Ptr(0.0000005)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 0,
		TotalTokens:      1000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens: 1000, // All cached
		},
	}

	cost := computeTextCost(&p, usage, serviceTier{})
	// Non-cached: 0, cached: 1000*0.0000005 = 0.0005
	assert.InDelta(t, 0.0005, cost, 1e-12)
}

// =========================================================================
// Nil usage fallbacks — per-unit pricing when no token data is reported
// =========================================================================

func TestCalculateCost_ImageGeneration_NilUsage_PerImagePricing(t *testing.T) {
	// Image response exists but Usage is nil — should default to 1 image with per-image pricing
	pricing := configstoreTables.TableModelPricing{
		Model:              "dall-e-3",
		Provider:           "openai",
		Mode:               "image_generation",
		InputCostPerToken:  bifrost.Ptr(0.0),
		OutputCostPerImage: bifrost.Ptr(0.04),
	}

	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("dall-e-3", "openai", "image_generation"): pricing,
	})

	resp := makeImageResponse("openai", "dall-e-3", nil)
	cost := s.CalculateCost(resp, nil)
	// 1 image * $0.04 = $0.04
	assert.InDelta(t, 0.04, cost, 1e-12)
}

func TestCalculateCost_ImageGeneration_NilUsage_InputAndOutputPerImage(t *testing.T) {
	// Both input and output per-image pricing, but no NumInputImages set
	pricing := configstoreTables.TableModelPricing{
		Model:              "test-image-model",
		Provider:           "test",
		Mode:               "image_generation",
		InputCostPerImage:  bifrost.Ptr(0.01),
		OutputCostPerImage: bifrost.Ptr(0.04),
	}

	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("test-image-model", "test", "image_generation"): pricing,
	})

	resp := makeImageResponse("test", "test-image-model", nil)
	cost := s.CalculateCost(resp, nil)
	// NumInputImages is 0 (not populated from request), so only output pricing applies
	// 1 output image * $0.04 = $0.04
	assert.InDelta(t, 0.04, cost, 1e-12)
}

func TestCalculateCost_ImageGeneration_WithInputImages(t *testing.T) {
	// Input + output per-image pricing with NumInputImages populated from request
	pricing := configstoreTables.TableModelPricing{
		Model:              "gpt-image-1",
		Provider:           "openai",
		Mode:               "image_generation",
		InputCostPerImage:  bifrost.Ptr(0.01),
		OutputCostPerImage: bifrost.Ptr(0.04),
	}

	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-image-1", "openai", "image_generation"): pricing,
	})

	resp := makeImageResponse("openai", "gpt-image-1", &schemas.ImageUsage{
		NumInputImages: 2,
	})
	cost := s.CalculateCost(resp, nil)
	// 2 input images * $0.01 + 1 output image * $0.04 = $0.06
	assert.InDelta(t, 0.06, cost, 1e-12)
}

func TestCalculateCost_ImageGeneration_OutputCountFromData(t *testing.T) {
	// Output image count derived from len(Data) via populateOutputImageCount
	pricing := configstoreTables.TableModelPricing{
		Model:              "dall-e-3",
		Provider:           "openai",
		Mode:               "image_generation",
		OutputCostPerImage: bifrost.Ptr(0.04),
	}

	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("dall-e-3", "openai", "image_generation"): pricing,
	})

	resp := &schemas.BifrostResponse{
		ImageGenerationResponse: &schemas.BifrostImageGenerationResponse{
			Data: []schemas.ImageData{
				{URL: "https://example.com/img1.png", Index: 0},
				{URL: "https://example.com/img2.png", Index: 1},
				{URL: "https://example.com/img3.png", Index: 2},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ImageGenerationRequest,
				RoutingInfo: routingInfoFor("openai", "dall-e-3"),
			},
		},
	}
	cost := s.CalculateCost(resp, nil)
	// 3 output images * $0.04 = $0.12
	assert.InDelta(t, 0.12, cost, 1e-12)
}

func TestCalculateCost_ImageGeneration_NilUsage_NoPerImagePricing(t *testing.T) {
	// No per-image pricing and no tokens — should return 0
	pricing := configstoreTables.TableModelPricing{
		Model:              "token-only-model",
		Provider:           "test",
		Mode:               "image_generation",
		InputCostPerToken:  bifrost.Ptr(0.000001),
		OutputCostPerToken: bifrost.Ptr(0.000002),
	}

	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("token-only-model", "test", "image_generation"): pricing,
	})

	resp := makeImageResponse("test", "token-only-model", nil)
	cost := s.CalculateCost(resp, nil)
	// No per-image pricing and all tokens are zero → 0
	assert.InDelta(t, 0.0, cost, 1e-12)
}

func TestCalculateCost_ImageGeneration_EmptyUsage_PerImagePricing(t *testing.T) {
	// Usage exists but all fields are zero — same as nil usage, should use per-image pricing
	pricing := configstoreTables.TableModelPricing{
		Model:              "dall-e-3",
		Provider:           "openai",
		Mode:               "image_generation",
		OutputCostPerImage: bifrost.Ptr(0.04),
	}

	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("dall-e-3", "openai", "image_generation"): pricing,
	})

	resp := makeImageResponse("openai", "dall-e-3", &schemas.ImageUsage{})
	cost := s.CalculateCost(resp, nil)
	assert.InDelta(t, 0.04, cost, 1e-12)
}

func TestComputeImageCost_MixedInputTokensOutputPerImage(t *testing.T) {
	// Input has tokens (text prompt), output has no tokens but per-image pricing
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  bifrost.Ptr(0.000005),
		OutputCostPerToken: bifrost.Ptr(0.000015),
		OutputCostPerImage: bifrost.Ptr(0.04),
	}
	usage := &schemas.ImageUsage{
		InputTokens:         500,
		OutputTokensDetails: &schemas.ImageTokenDetails{NImages: 2},
	}
	cost := computeImageCost(&p, usage, "", "", serviceTier{})
	// Input: 500 tokens * $0.000005 = $0.0025
	// Output: no output tokens → falls back to 2 images * $0.04 = $0.08
	assert.InDelta(t, 0.0825, cost, 1e-12)
}

func TestComputeImageCost_MixedInputPerImageOutputTokens(t *testing.T) {
	// Input has no tokens but per-image count, output has tokens
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  bifrost.Ptr(0.000005),
		OutputCostPerToken: bifrost.Ptr(0.000015),
		InputCostPerImage:  bifrost.Ptr(0.01),
	}
	usage := &schemas.ImageUsage{
		NumInputImages: 3,
		OutputTokens:   1000,
	}
	cost := computeImageCost(&p, usage, "", "", serviceTier{})
	// Input: no input tokens → falls back to 3 images * $0.01 = $0.03
	// Output: 1000 tokens * $0.000015 = $0.015
	assert.InDelta(t, 0.045, cost, 1e-12)
}

func TestComputeImageCost_BothHaveTokens_IgnoresPerImage(t *testing.T) {
	// Both sides have tokens — per-image pricing is ignored
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  bifrost.Ptr(0.000005),
		OutputCostPerToken: bifrost.Ptr(0.000015),
		InputCostPerImage:  bifrost.Ptr(0.01),
		OutputCostPerImage: bifrost.Ptr(0.04),
	}
	usage := &schemas.ImageUsage{
		InputTokens:    200,
		OutputTokens:   800,
		TotalTokens:    1000,
		NumInputImages: 3,
	}
	cost := computeImageCost(&p, usage, "", "", serviceTier{})
	// Input: 200 * $0.000005 = $0.001 (tokens present, per-image ignored)
	// Output: 800 * $0.000015 = $0.012 (tokens present, per-image ignored)
	assert.InDelta(t, 0.013, cost, 1e-12)
}

func TestCalculateCost_ResponsesWithCodeInterpreter(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4.1", "openai", "chat"): chatPricing(0.000002, 0.000008),
	})

	ciType := schemas.ResponsesMessageTypeCodeInterpreterCall
	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  579,
				OutputTokens: 334,
				TotalTokens:  913,
			},
			Output: []schemas.ResponsesMessage{
				{Type: &ciType},
				{Type: &ciType},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ResponsesRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-4.1"),
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// Token cost only: 579*0.000002 + 334*0.000008 = 0.001158 + 0.002672 = 0.003830
	// Session cost is now tracked via ContainerCreateRequest, not per-response
	assert.InDelta(t, 0.003830, cost, 1e-6)
}

// ---------------------------------------------------------------------------
// computeContainerCreationCost
// ---------------------------------------------------------------------------

func TestComputeContainerCreationCost_Basic(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		Model:                         "container",
		Provider:                      "openai",
		Mode:                          "chat",
		CodeInterpreterCostPerSession: bifrost.Ptr(0.03),
	}
	assert.InDelta(t, 0.03, computeContainerCreationCost(&p), 1e-12)
}

func TestComputeContainerCreationCost_NilPricing(t *testing.T) {
	assert.Equal(t, 0.0, computeContainerCreationCost(nil))
}

func TestComputeContainerCreationCost_NilRate(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		Model:    "container",
		Provider: "openai",
		Mode:     "chat",
	}
	assert.Equal(t, 0.0, computeContainerCreationCost(&p))
}

// ---------------------------------------------------------------------------
// ContainerCreateRequest end-to-end via CalculateCost
// ---------------------------------------------------------------------------

func TestCalculateCost_ContainerCreate_NoMemoryLimit(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("container", "openai", "chat"): {
			Model:                         "container",
			Provider:                      "openai",
			Mode:                          "chat",
			CodeInterpreterCostPerSession: bifrost.Ptr(0.03),
		},
	})

	resp := &schemas.BifrostResponse{
		ContainerCreateResponse: &schemas.BifrostContainerCreateResponse{
			ID:   "cntr_abc123",
			Name: "test-container",
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ContainerCreateRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI},
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	assert.InDelta(t, 0.03, cost, 1e-12)
}

func TestCalculateCost_ContainerCreate_MemorySpecificEntry(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("container", "openai", "chat"): {
			Model:                         "container",
			Provider:                      "openai",
			Mode:                          "chat",
			CodeInterpreterCostPerSession: bifrost.Ptr(0.03),
		},
		makeKey("container-4g", "openai", "chat"): {
			Model:                         "container-4g",
			Provider:                      "openai",
			Mode:                          "chat",
			CodeInterpreterCostPerSession: bifrost.Ptr(0.12),
		},
	})

	resp := &schemas.BifrostResponse{
		ContainerCreateResponse: &schemas.BifrostContainerCreateResponse{
			ID:          "cntr_abc123",
			Name:        "test-container",
			MemoryLimit: "4g",
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ContainerCreateRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI},
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	assert.InDelta(t, 0.12, cost, 1e-12)
}

func TestCalculateCost_ContainerCreate_FallsBackToBaseEntry(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("container", "openai", "chat"): {
			Model:                         "container",
			Provider:                      "openai",
			Mode:                          "chat",
			CodeInterpreterCostPerSession: bifrost.Ptr(0.03),
		},
	})

	resp := &schemas.BifrostResponse{
		ContainerCreateResponse: &schemas.BifrostContainerCreateResponse{
			ID:          "cntr_abc123",
			Name:        "test-container",
			MemoryLimit: "4g",
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ContainerCreateRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI},
			},
		},
	}

	// No container-4g entry — should fall back to base "container" rate
	cost := s.CalculateCost(resp, nil)
	assert.InDelta(t, 0.03, cost, 1e-12)
}

func TestCalculateCost_ContainerCreate_NoEntry(t *testing.T) {
	s := testStoreWithPricing(nil)

	resp := &schemas.BifrostResponse{
		ContainerCreateResponse: &schemas.BifrostContainerCreateResponse{
			ID:   "cntr_abc123",
			Name: "test-container",
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ContainerCreateRequest,
				RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI},
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	assert.Equal(t, 0.0, cost)
}

// ---------------------------------------------------------------------------
// Backward-compat: RoutingInfo missing → synthesize from deprecated triplet
//
// Covers callers stuck on the legacy ExtraFields shape:
//   - LoggerPlugin.RecalculateCosts replaying logs written before RoutingInfo existed
//   - Third-party plugins / SDK users that haven't migrated to RoutingInfo
//
// The fallback only fires when RoutingInfo is fully empty (zero Provider,
// zero Model, nil ResolvedKeyAlias). Any partial population is trusted.
// ---------------------------------------------------------------------------

func TestCalculateCost_BackCompat_LegacyFieldsOnly_NoAlias(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	// Caller populates only the deprecated triplet — no RoutingInfo.
	// Pricing should fall back to Provider + OriginalModelRequested.
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4o",
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// 1000 * 0.000005 + 500 * 0.000015 = 0.005 + 0.0075 = 0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestCalculateCost_BackCompat_LegacyFieldsOnly_WithAlias(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("my-deployment", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	// Caller populates only the deprecated triplet with a distinct
	// ResolvedModelUsed (i.e. an alias was matched at original request time
	// and the wire model differs from the caller-facing name). The fallback
	// should route ResolvedModelUsed into ResolvedKeyAlias.ModelID so the
	// catalog lookup hits the deployment-keyed entry.
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "my-alias-name",
				ResolvedModelUsed:      "my-deployment",
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// 1000 * 0.000005 + 500 * 0.000015 = 0.0125, charged via the deployment-keyed entry
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestCalculateCost_BackCompat_RoutingInfoWinsOverLegacyFields(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
		makeKey("gemini-2.0-flash", "gemini", "chat"): {
			Model:              "gemini-2.0-flash",
			Provider:           "gemini",
			Mode:               "chat",
			InputCostPerToken:  bifrost.Ptr(0.0000001),
			OutputCostPerToken: bifrost.Ptr(0.0000004),
		},
	})

	// Both populated. The modern fields (RoutingInfo) must win — the
	// fallback only fires when RoutingInfo is fully unset.
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				RoutingInfo:            routingInfoFor(schemas.OpenAI, "gpt-4o"),
				Provider:               schemas.Gemini,
				OriginalModelRequested: "gemini-2.0-flash",
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// Priced via RoutingInfo → openai/gpt-4o → 0.0125 (not the gemini rate).
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestCalculateCost_BackCompat_BothEmpty_ReturnsZero(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	// Neither RoutingInfo nor the deprecated triplet are populated.
	// Pricing has no way to identify the model; cost is 0.
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	assert.Equal(t, 0.0, cost)
}

func TestCalculateCost_BackCompat_PartialRoutingInfo_NoFallback(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	// RoutingInfo has Model but no Provider. The legacy Provider field is
	// also set. The fallback MUST NOT fire — partial RoutingInfo means the
	// caller intended to use RoutingInfo. With Provider unset on RoutingInfo,
	// the catalog lookup fails and cost is 0. (This guards the trigger
	// against false positives.)
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: schemas.RoutingInfo{Model: "gpt-4o"},
				Provider:    schemas.OpenAI,
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	assert.Equal(t, 0.0, cost)
}

// TestCalculateCostForUsage_MatchesCalculateCost verifies the cost-from-usage helper:
// billing a bare usage object (the failure/cancel path) yields exactly the same
// cost as billing a full BifrostResponse carrying that usage (the success path),
// so failed-but-token-consuming requests are charged at identical rates.
func TestCalculateCostForUsage_MatchesCalculateCost(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	usage := &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-4o"),
			},
		},
	}

	respCost := s.CalculateCost(resp, nil)
	usageCost := s.CalculateCostForUsage(usage, schemas.OpenAI, "gpt-4o", schemas.ChatCompletionRequest, nil)

	assert.Greater(t, respCost, 0.0, "response cost should be positive")
	assert.Equal(t, respCost, usageCost, "bare-usage cost must equal full-response cost")
}

// TestCalculateCostForUsage_NilUsageIsZero verifies the helper bills nothing
// when there is no usage (e.g. a failure that consumed no tokens).
func TestCalculateCostForUsage_NilUsageIsZero(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})
	assert.Equal(t, 0.0, s.CalculateCostForUsage(nil, schemas.OpenAI, "gpt-4o", schemas.ChatCompletionRequest, nil))
}

// TestCalculateCostForUsage_AppliesServedTier verifies the cancel/failure billing
// path honors the served Anthropic tier carried internally on the usage (fast mode
// + data residency), so interrupted fast or US-residency streams keep their
// multiplier instead of being billed as standard/global.
func TestCalculateCostForUsage_AppliesServedTier(t *testing.T) {
	p := chatPricing(0.000005, 0.000015) // base $5/$15 per MTok
	p.InputCostPerTokenFast = bifrost.Ptr(0.00001)
	p.OutputCostPerTokenFast = bifrost.Ptr(0.00003)
	p.InferenceGeoUSMultiplier = bifrost.Ptr(1.1)
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("claude-x", "anthropic", "chat"): p,
	})
	mk := func(speed, geo *string) *schemas.BifrostLLMUsage {
		return &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500, Speed: speed, InferenceGeo: geo}
	}
	cost := func(u *schemas.BifrostLLMUsage) float64 {
		return s.CalculateCostForUsage(u, schemas.Anthropic, "claude-x", schemas.ChatCompletionRequest, nil)
	}
	fast, us := "fast", "us"

	// No served tier → base rate.
	assert.InDelta(t, 1000*0.000005+500*0.000015, cost(mk(nil, nil)), 1e-12)
	// speed:"fast" → flat fast rate.
	assert.InDelta(t, 1000*0.00001+500*0.00003, cost(mk(&fast, nil)), 1e-12)
	// inference_geo:"us" → 1.1x on base.
	assert.InDelta(t, (1000*0.000005+500*0.000015)*1.1, cost(mk(nil, &us)), 1e-12)
}

// ===========================================================================
// Golden per-model OpenAI pricing tests
//
// These feed the exact published OpenAI rates through the real datasheet
// JSON -> TableModelPricing conversion (convertEntryToTablePricing) and assert
// the invoice cost for every service tier and context window. They pin two
// things at once: the rate-selection ladders in cost.go, and the field wiring
// that carries each new rate from the datasheet JSON into the pricing row.
//
// NOTE: the numbers below are the source of truth for *expected billing*,
// transcribed from the OpenAI pricing page. The live per-model values arrive
// via the datasheet sync (not this repo), so these tests validate the compute
// engine + wiring, not the sync payload.
// ===========================================================================

// pricingRowFromDatasheetJSON parses a datasheet entry (the shape stored in the
// synced pricing catalog) and runs the production JSON -> row conversion, so a
// dropped json tag or a missing conversion-map line would fail these tests.
func pricingRowFromDatasheetJSON(t *testing.T, modelKey, blob string) configstoreTables.TableModelPricing {
	t.Helper()
	var entry Entry
	require.NoError(t, json.Unmarshal([]byte(blob), &entry))
	return convertEntryToTablePricing(modelKey, entry)
}

// Datasheet entries (pricing fields only) for the gpt-5.6 flagship family.
// Cache writes are the cache_creation_* fields. Short/long context are the
// base vs _above_272k rates.
const (
	gpt56SolDatasheet = `{
		"provider": "openai", "mode": "chat", "base_model": "gpt-5.6-sol",
		"input_cost_per_token": 0.000005,
		"input_cost_per_token_above_272k_tokens": 0.00001,
		"input_cost_per_token_flex": 0.0000025,
		"input_cost_per_token_flex_above_272k_tokens": 0.000005,
		"input_cost_per_token_priority": 0.00001,
		"output_cost_per_token": 0.00003,
		"output_cost_per_token_above_272k_tokens": 0.000045,
		"output_cost_per_token_flex": 0.000015,
		"output_cost_per_token_flex_above_272k_tokens": 0.0000225,
		"output_cost_per_token_priority": 0.00006,
		"cache_read_input_token_cost": 0.0000005,
		"cache_read_input_token_cost_above_272k_tokens": 0.000001,
		"cache_read_input_token_cost_flex": 0.00000025,
		"cache_read_input_token_cost_flex_above_272k_tokens": 0.0000005,
		"cache_read_input_token_cost_priority": 0.000001,
		"cache_creation_input_token_cost": 0.00000625,
		"cache_creation_input_token_cost_above_272k_tokens": 0.0000125,
		"cache_creation_input_token_cost_flex": 0.000003125,
		"cache_creation_input_token_cost_flex_above_272k_tokens": 0.00000625,
		"cache_creation_input_token_cost_priority": 0.0000125
	}`
	gpt56TerraDatasheet = `{
		"provider": "openai", "mode": "chat", "base_model": "gpt-5.6-terra",
		"input_cost_per_token": 0.0000025,
		"input_cost_per_token_above_272k_tokens": 0.000005,
		"input_cost_per_token_flex": 0.00000125,
		"input_cost_per_token_flex_above_272k_tokens": 0.0000025,
		"input_cost_per_token_priority": 0.000005,
		"output_cost_per_token": 0.000015,
		"output_cost_per_token_above_272k_tokens": 0.0000225,
		"output_cost_per_token_flex": 0.0000075,
		"output_cost_per_token_flex_above_272k_tokens": 0.00001125,
		"output_cost_per_token_priority": 0.00003,
		"cache_read_input_token_cost": 0.00000025,
		"cache_read_input_token_cost_above_272k_tokens": 0.0000005,
		"cache_read_input_token_cost_flex": 0.000000125,
		"cache_read_input_token_cost_flex_above_272k_tokens": 0.00000025,
		"cache_read_input_token_cost_priority": 0.0000005,
		"cache_creation_input_token_cost": 0.000003125,
		"cache_creation_input_token_cost_above_272k_tokens": 0.00000625,
		"cache_creation_input_token_cost_flex": 0.0000015625,
		"cache_creation_input_token_cost_flex_above_272k_tokens": 0.000003125,
		"cache_creation_input_token_cost_priority": 0.00000625
	}`
	gpt56LunaDatasheet = `{
		"provider": "openai", "mode": "chat", "base_model": "gpt-5.6-luna",
		"input_cost_per_token": 0.000001,
		"input_cost_per_token_above_272k_tokens": 0.000002,
		"input_cost_per_token_flex": 0.0000005,
		"input_cost_per_token_flex_above_272k_tokens": 0.000001,
		"input_cost_per_token_priority": 0.000002,
		"output_cost_per_token": 0.000006,
		"output_cost_per_token_above_272k_tokens": 0.000009,
		"output_cost_per_token_flex": 0.000003,
		"output_cost_per_token_flex_above_272k_tokens": 0.0000045,
		"output_cost_per_token_priority": 0.000012,
		"cache_read_input_token_cost": 0.0000001,
		"cache_read_input_token_cost_above_272k_tokens": 0.0000002,
		"cache_read_input_token_cost_flex": 0.00000005,
		"cache_read_input_token_cost_flex_above_272k_tokens": 0.0000001,
		"cache_read_input_token_cost_priority": 0.0000002,
		"cache_creation_input_token_cost": 0.00000125,
		"cache_creation_input_token_cost_above_272k_tokens": 0.0000025,
		"cache_creation_input_token_cost_flex": 0.000000625,
		"cache_creation_input_token_cost_flex_above_272k_tokens": 0.00000125,
		"cache_creation_input_token_cost_priority": 0.0000025
	}`
	// gpt-5.5: long-context tiering but NO published cache-write rate ("-").
	gpt55Datasheet = `{
		"provider": "openai", "mode": "chat", "base_model": "gpt-5.5",
		"input_cost_per_token": 0.000005,
		"input_cost_per_token_above_272k_tokens": 0.00001,
		"input_cost_per_token_flex": 0.0000025,
		"input_cost_per_token_flex_above_272k_tokens": 0.000005,
		"input_cost_per_token_priority": 0.0000125,
		"output_cost_per_token": 0.00003,
		"output_cost_per_token_above_272k_tokens": 0.000045,
		"output_cost_per_token_flex": 0.000015,
		"output_cost_per_token_flex_above_272k_tokens": 0.0000225,
		"output_cost_per_token_priority": 0.000075,
		"cache_read_input_token_cost": 0.0000005,
		"cache_read_input_token_cost_above_272k_tokens": 0.000001,
		"cache_read_input_token_cost_flex": 0.00000025,
		"cache_read_input_token_cost_flex_above_272k_tokens": 0.0000005,
		"cache_read_input_token_cost_priority": 0.00000125
	}`
	// gpt-5.4-mini: NO long-context tier and NO cache-write rate.
	gpt54MiniDatasheet = `{
		"provider": "openai", "mode": "chat", "base_model": "gpt-5.4-mini",
		"input_cost_per_token": 0.00000075,
		"input_cost_per_token_flex": 0.000000375,
		"input_cost_per_token_priority": 0.0000015,
		"output_cost_per_token": 0.0000045,
		"output_cost_per_token_flex": 0.00000225,
		"output_cost_per_token_priority": 0.000009,
		"cache_read_input_token_cost": 0.000000075,
		"cache_read_input_token_cost_flex": 0.0000000375,
		"cache_read_input_token_cost_priority": 0.00000015
	}`
)

// TestGoldenOpenAIPricing_GPT56Family asserts the exact invoice cost for every
// (tier x context) cell of the gpt-5.6 pricing tables, including cache read and
// cache write. Each row hardcodes the rate that *should* apply, so a mis-selected
// tier or context bucket fails the assertion.
func TestGoldenOpenAIPricing_GPT56Family(t *testing.T) {
	sol := pricingRowFromDatasheetJSON(t, "gpt-5.6-sol", gpt56SolDatasheet)
	terra := pricingRowFromDatasheetJSON(t, "gpt-5.6-terra", gpt56TerraDatasheet)
	luna := pricingRowFromDatasheetJSON(t, "gpt-5.6-luna", gpt56LunaDatasheet)

	// Short context stays under 272k; long context crosses it.
	const (
		shortPrompt, shortRead, shortWrite, shortOut = 10000, 4000, 2000, 1000
		longPrompt, longRead, longWrite, longOut     = 300000, 100000, 50000, 1000
	)

	type rates struct{ in, cacheRead, cacheWrite, out float64 }
	cases := []struct {
		name    string
		pricing configstoreTables.TableModelPricing
		tier    serviceTier
		long    bool
		r       rates
	}{
		// gpt-5.6-sol
		{"sol/standard/short", sol, serviceTier{}, false, rates{0.000005, 0.0000005, 0.00000625, 0.00003}},
		{"sol/standard/long", sol, serviceTier{}, true, rates{0.00001, 0.000001, 0.0000125, 0.000045}},
		{"sol/flex/short", sol, serviceTier{isFlex: true}, false, rates{0.0000025, 0.00000025, 0.000003125, 0.000015}},
		{"sol/flex/long", sol, serviceTier{isFlex: true}, true, rates{0.000005, 0.0000005, 0.00000625, 0.0000225}},
		{"sol/priority/short", sol, serviceTier{isPriority: true}, false, rates{0.00001, 0.000001, 0.0000125, 0.00006}},
		// gpt-5.6-terra
		{"terra/standard/short", terra, serviceTier{}, false, rates{0.0000025, 0.00000025, 0.000003125, 0.000015}},
		{"terra/standard/long", terra, serviceTier{}, true, rates{0.000005, 0.0000005, 0.00000625, 0.0000225}},
		{"terra/flex/short", terra, serviceTier{isFlex: true}, false, rates{0.00000125, 0.000000125, 0.0000015625, 0.0000075}},
		{"terra/flex/long", terra, serviceTier{isFlex: true}, true, rates{0.0000025, 0.00000025, 0.000003125, 0.00001125}},
		{"terra/priority/short", terra, serviceTier{isPriority: true}, false, rates{0.000005, 0.0000005, 0.00000625, 0.00003}},
		// gpt-5.6-luna
		{"luna/standard/short", luna, serviceTier{}, false, rates{0.000001, 0.0000001, 0.00000125, 0.000006}},
		{"luna/standard/long", luna, serviceTier{}, true, rates{0.000002, 0.0000002, 0.0000025, 0.000009}},
		{"luna/flex/short", luna, serviceTier{isFlex: true}, false, rates{0.0000005, 0.00000005, 0.000000625, 0.000003}},
		{"luna/flex/long", luna, serviceTier{isFlex: true}, true, rates{0.000001, 0.0000001, 0.00000125, 0.0000045}},
		{"luna/priority/short", luna, serviceTier{isPriority: true}, false, rates{0.000002, 0.0000002, 0.0000025, 0.000012}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prompt, read, write, out := shortPrompt, shortRead, shortWrite, shortOut
			if tc.long {
				prompt, read, write, out = longPrompt, longRead, longWrite, longOut
			}
			usage := &schemas.BifrostLLMUsage{
				PromptTokens:     prompt,
				CompletionTokens: out,
				TotalTokens:      prompt + out,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					CachedReadTokens:  read,
					CachedWriteTokens: write,
				},
			}
			p := tc.pricing
			cost := computeTextCost(&p, usage, tc.tier)
			nonCached := prompt - read - write
			want := float64(nonCached)*tc.r.in + float64(read)*tc.r.cacheRead + float64(write)*tc.r.cacheWrite + float64(out)*tc.r.out
			assert.InDelta(t, want, cost, 1e-9)
		})
	}
}

// TestGoldenOpenAIPricing_NoCacheWriteModels covers models that OpenAI prices
// without a cache-write rate (gpt-5.5) and without a long-context tier
// (gpt-5.4-mini): cache-write tokens fall back to the input rate, and a >272k
// request on a model with no long-context rate stays on the base rate.
func TestGoldenOpenAIPricing_NoCacheWriteModels(t *testing.T) {
	gpt55 := pricingRowFromDatasheetJSON(t, "gpt-5.5", gpt55Datasheet)
	gpt54mini := pricingRowFromDatasheetJSON(t, "gpt-5.4-mini", gpt54MiniDatasheet)

	// gpt-5.5 has no cache-write rate: write tokens bill at the (long-context) input rate.
	t.Run("gpt-5.5/standard/long: cache-write falls back to input rate", func(t *testing.T) {
		usage := &schemas.BifrostLLMUsage{
			PromptTokens: 300000, CompletionTokens: 1000, TotalTokens: 301000,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedReadTokens: 100000, CachedWriteTokens: 50000},
		}
		p := gpt55
		cost := computeTextCost(&p, usage, serviceTier{})
		// non-cached 150000*0.00001 + read 100000*0.000001 + write 50000*0.00001 (input fallback) + out 1000*0.000045
		want := 150000*0.00001 + 100000*0.000001 + 50000*0.00001 + 1000*0.000045
		assert.InDelta(t, want, cost, 1e-9)
	})

	t.Run("gpt-5.5/flex/short", func(t *testing.T) {
		usage := &schemas.BifrostLLMUsage{
			PromptTokens: 10000, CompletionTokens: 1000, TotalTokens: 11000,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedReadTokens: 4000},
		}
		p := gpt55
		cost := computeTextCost(&p, usage, serviceTier{isFlex: true})
		want := 6000*0.0000025 + 4000*0.00000025 + 1000*0.000015
		assert.InDelta(t, want, cost, 1e-12)
	})

	t.Run("gpt-5.5/priority/short", func(t *testing.T) {
		usage := &schemas.BifrostLLMUsage{
			PromptTokens: 10000, CompletionTokens: 1000, TotalTokens: 11000,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedReadTokens: 4000},
		}
		p := gpt55
		cost := computeTextCost(&p, usage, serviceTier{isPriority: true})
		want := 6000*0.0000125 + 4000*0.00000125 + 1000*0.000075
		assert.InDelta(t, want, cost, 1e-9)
	})

	// gpt-5.4-mini has no long-context rate: a >272k request must use the base rate.
	t.Run("gpt-5.4-mini/standard/above-272k uses base rate", func(t *testing.T) {
		usage := &schemas.BifrostLLMUsage{
			PromptTokens: 300000, CompletionTokens: 1000, TotalTokens: 301000,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedReadTokens: 50000},
		}
		p := gpt54mini
		cost := computeTextCost(&p, usage, serviceTier{})
		want := 250000*0.00000075 + 50000*0.000000075 + 1000*0.0000045
		assert.InDelta(t, want, cost, 1e-9)
	})
}

// TestCalculateCost_GPT56_Responses_FlexLongContext_EndToEnd drives the full
// pipeline for a gpt-5.6 Responses call: service_tier=flex + a >272k prompt with
// cache writes, through tier detection, usage mapping, pricing lookup, and cost.
func TestCalculateCost_GPT56_Responses_FlexLongContext_EndToEnd(t *testing.T) {
	sol := pricingRowFromDatasheetJSON(t, "gpt-5.6-sol", gpt56SolDatasheet)
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-5.6-sol", "openai", "responses"): sol,
	})
	tier := schemas.BifrostServiceTierFlex
	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			ServiceTier: &tier,
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  300000,
				OutputTokens: 1000,
				TotalTokens:  301000,
				InputTokensDetails: &schemas.ResponsesResponseInputTokens{
					CachedReadTokens:  100000,
					CachedWriteTokens: 50000,
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ResponsesRequest,
				RoutingInfo: routingInfoFor(schemas.OpenAI, "gpt-5.6-sol"),
			},
		},
	}
	cost := s.CalculateCost(resp, nil)
	// flex long-context: 150000*0.000005 + 100000*0.0000005 + 50000*0.00000625 + 1000*0.0000225
	want := 150000*0.000005 + 100000*0.0000005 + 50000*0.00000625 + 1000*0.0000225
	assert.InDelta(t, want, cost, 1e-9)
}

// TestTieredCacheCreationRate_PriorityWinsOver200kBand verifies a priority cache-write
// request in the 200k–272k band uses the flat priority rate, not the standard >200k
// rate. Priority has no long context (OpenAI does not offer priority >272k), so its
// flat rate must take precedence over the standard context tiers.
func TestTieredCacheCreationRate_PriorityWinsOver200kBand(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		CacheCreationInputTokenCost:                bifrost.Ptr(0.000001),
		CacheCreationInputTokenCostAbove200kTokens: bifrost.Ptr(0.000002), // standard >200k — must NOT win for priority
		CacheCreationInputTokenCostPriority:        bifrost.Ptr(0.000005), // flat priority — must win
	}
	// 250k tokens: >200k and ≤272k.
	assert.Equal(t, 0.000005, tieredCacheCreationInputTokenRate(&p, 250000, serviceTier{isPriority: true}))
	// Non-priority at the same size still uses the standard >200k rate.
	assert.Equal(t, 0.000002, tieredCacheCreationInputTokenRate(&p, 250000, serviceTier{}))
}

// ---------------------------------------------------------------------------
// Anthropic server-side fallback: price the model that actually served
// ---------------------------------------------------------------------------

// seedFallbackPricing loads the real published Fable 5 / Opus 4.8 rates. Fable 5
// is exactly 2x Opus 4.8 on both input and output, which is what makes pricing a
// fallback-served turn against the requested model a clean doubling.
func seedFallbackPricing(s *Store) {
	s.pricingData[makeKey("claude-fable-5", "anthropic", "responses")] = configstoreTables.TableModelPricing{
		Model: "claude-fable-5", Provider: "anthropic", Mode: "responses",
		InputCostPerToken:  bifrost.Ptr(10.0 / 1_000_000),
		OutputCostPerToken: bifrost.Ptr(50.0 / 1_000_000),
	}
	s.pricingData[makeKey("claude-opus-4-8", "anthropic", "responses")] = configstoreTables.TableModelPricing{
		Model: "claude-opus-4-8", Provider: "anthropic", Mode: "responses",
		InputCostPerToken:  bifrost.Ptr(5.0 / 1_000_000),
		OutputCostPerToken: bifrost.Ptr(25.0 / 1_000_000),
	}
}

// fallbackResponse mirrors a real server-side-fallback payload: the caller asked
// for claude-fable-5, claude-opus-4-8 served, and the top-level usage mirrors the
// serving attempt.
func fallbackResponse(servingModel *string, in, out int) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Model: "claude-opus-4-8",
			Usage: &schemas.ResponsesResponseUsage{InputTokens: in, OutputTokens: out},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ResponsesRequest,
				RoutingInfo: schemas.RoutingInfo{
					Provider:                schemas.Anthropic,
					Model:                   "claude-fable-5",
					ServerSideFallbackModel: servingModel,
				},
			},
		},
	}
}

func TestCalculateCost_ServerSideFallback_PricesServingModel(t *testing.T) {
	s := newTestStore()
	seedFallbackPricing(s)

	// Real payload: 31 in / 257 out, served by Opus after Fable declined.
	// Before the fix this returned the Fable figure, which is exactly double.
	resp := fallbackResponse(bifrost.Ptr("claude-opus-4-8"), 31, 257)
	assert.InDelta(t, 31*(5.0/1_000_000)+257*(25.0/1_000_000), s.CalculateCost(resp, nil), 1e-12,
		"expected Opus 4.8 rates (the serving model), not Fable 5")
}

// No handoff — the overwhelming majority of responses — must be untouched, even
// though the response's own model field names a different model.
func TestCalculateCost_NoServerSideFallback_UsesRoutingModel(t *testing.T) {
	s := newTestStore()
	seedFallbackPricing(s)

	resp := fallbackResponse(nil, 31, 257)
	assert.InDelta(t, 31*(10.0/1_000_000)+257*(50.0/1_000_000), s.CalculateCost(resp, nil), 1e-12,
		"without a recorded handoff, pricing must stay on the requested model")
}

// An unpriceable serving model falls through the candidate chain to the
// requested model rather than collapsing the cost to zero.
func TestCalculateCost_ServerSideFallback_UnknownServingModelFallsThrough(t *testing.T) {
	s := newTestStore()
	seedFallbackPricing(s)

	resp := fallbackResponse(bifrost.Ptr("claude-not-in-catalog"), 31, 257)
	cost := s.CalculateCost(resp, nil)
	assert.InDelta(t, 31*(10.0/1_000_000)+257*(50.0/1_000_000), cost, 1e-12)
	assert.NotZero(t, cost, "an unknown serving model must not zero the cost")
}

// A key alias would otherwise win the lookup; the serving model outranks it.
func TestCalculateCost_ServerSideFallback_OutranksKeyAlias(t *testing.T) {
	s := newTestStore()
	seedFallbackPricing(s)

	resp := fallbackResponse(bifrost.Ptr("claude-opus-4-8"), 31, 257)
	resp.ResponsesResponse.ExtraFields.RoutingInfo.Model = "my-fast-alias"
	resp.ResponsesResponse.ExtraFields.RoutingInfo.ResolvedKeyAlias = &schemas.ResolvedKeyAlias{
		ModelID:   "claude-fable-5",
		ModelName: bifrost.Ptr("claude-fable-5"),
	}

	assert.InDelta(t, 31*(5.0/1_000_000)+257*(25.0/1_000_000), s.CalculateCost(resp, nil), 1e-12,
		"the alias must not outrank the model that actually served")
}

// Overrides follow the serving model, so a negotiated rate for the model that
// actually ran is the one that applies.
func TestCalculateCost_ServerSideFallback_OverridesKeyOnServingModel(t *testing.T) {
	s := newTestStore()
	seedFallbackPricing(s)
	providerID := "anthropic"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{{
		ID:               "opus-negotiated-rate",
		ScopeKind:        string(ScopeKindProvider),
		ProviderID:       &providerID,
		MatchType:        string(MatchTypeExact),
		Pattern:          "claude-opus-4-8",
		RequestTypes:     []schemas.RequestType{schemas.ResponsesRequest},
		PricingPatchJSON: `{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}`,
	}}))

	resp := fallbackResponse(bifrost.Ptr("claude-opus-4-8"), 31, 257)
	assert.InDelta(t, 31*0.000001+257*0.000002, s.CalculateCost(resp, nil), 1e-12,
		"an override for the serving model must apply to a fallback-served turn")
}

// Streaming is billed through CalculateCostForUsage, which receives only
// (usage, provider, requestedModel) and never sees RoutingInfo. A live
// fallback-served stream (38 in / 345 out, served by Opus after Fable declined)
// was reported at $0.01763 — exactly the Fable figure, i.e. double.
func TestCalculateCostForUsage_ServerSideFallback_PricesServingModel(t *testing.T) {
	s := newTestStore()
	seedFallbackPricing(s)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:            38,
		CompletionTokens:        345,
		TotalTokens:             383,
		ServerSideFallbackModel: bifrost.Ptr("claude-opus-4-8"),
	}
	got := s.CalculateCostForUsage(usage, schemas.Anthropic, "claude-fable-5", schemas.ResponsesRequest, nil)
	assert.InDelta(t, 38*(5.0/1_000_000)+345*(25.0/1_000_000), got, 1e-12,
		"expected Opus 4.8 rates for a fallback-served stream")
	assert.InDelta(t, 0.008815, got, 1e-6, "expected the Opus figure, not the $0.01763 Fable one")
}

// Sticky-routed stream: a single fallback_message iteration, 38 in / 320 out,
// reported live at $0.01638 (Fable). Should be $0.00819.
func TestCalculateCostForUsage_ServerSideFallback_StickyStream(t *testing.T) {
	s := newTestStore()
	seedFallbackPricing(s)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens: 38, CompletionTokens: 320, TotalTokens: 358,
		ServerSideFallbackModel: bifrost.Ptr("claude-opus-4-8"),
	}
	assert.InDelta(t, 0.00819,
		s.CalculateCostForUsage(usage, schemas.Anthropic, "claude-fable-5", schemas.ResponsesRequest, nil), 1e-6)
}

// No handoff: the bare-usage path must keep pricing the requested model.
func TestCalculateCostForUsage_NoServerSideFallback_Unchanged(t *testing.T) {
	s := newTestStore()
	seedFallbackPricing(s)

	usage := &schemas.BifrostLLMUsage{PromptTokens: 38, CompletionTokens: 345, TotalTokens: 383}
	assert.InDelta(t, 38*(10.0/1_000_000)+345*(50.0/1_000_000),
		s.CalculateCostForUsage(usage, schemas.Anthropic, "claude-fable-5", schemas.ResponsesRequest, nil), 1e-12)
}

// TestGetBasePricing_RegionPrefixedSelection verifies OpenAI regional-processing
// pricing: when RoutingInfo.Region is set, the cost path prefers the
// region-prefixed model row (e.g. "eu.gpt-5.4") and falls back to the bare model
// when the datasheet has no regional entry — so listed models get the uplift and
// unlisted ones keep default pricing (no extrapolation). The wire model sent to
// the provider stays "gpt-5.4"; only pricing resolution changes.
func TestGetBasePricing_RegionPrefixedSelection(t *testing.T) {
	base := chatPricing(0.0000025, 0.000015) // gpt-5.4 US
	base.Model, base.Provider = "gpt-5.4", "openai"
	eu := chatPricing(0.00000275, 0.0000165) // gpt-5.4 EU (+10%)
	eu.Model, eu.Provider = "eu.gpt-5.4", "openai"
	other := chatPricing(0.000005, 0.000015) // gpt-4o US, no EU row
	other.Model, other.Provider = "gpt-4o", "openai"

	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-5.4", "openai", "chat"):    base,
		makeKey("eu.gpt-5.4", "openai", "chat"): eu,
		makeKey("gpt-4o", "openai", "chat"):     other,
	})

	costFor := func(model, region string) float64 {
		resp := &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				Usage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType: schemas.ChatCompletionRequest,
					RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: model, Region: region},
				},
			},
		}
		return s.CalculateCost(resp, nil)
	}

	usCost := 1000*0.0000025 + 500*0.000015
	euCost := 1000*0.00000275 + 500*0.0000165

	// Default region → the bare (US) row.
	assert.InDelta(t, usCost, costFor("gpt-5.4", ""), 1e-12)
	// EU region → the eu.<model> row (uplifted rates).
	assert.InDelta(t, euCost, costFor("gpt-5.4", "eu"), 1e-12)
	// EU pricing is exactly US pricing +10%.
	assert.InDelta(t, usCost*1.1, costFor("gpt-5.4", "eu"), 1e-9)
	// Model with no eu.<model> row → falls back to the US row (no extrapolation).
	assert.InDelta(t, 1000*0.000005+500*0.000015, costFor("gpt-4o", "eu"), 1e-12)
	// A caller that addressed the region-prefixed model directly is not double-prefixed.
	assert.InDelta(t, euCost, costFor("eu.gpt-5.4", "eu"), 1e-12)
}
