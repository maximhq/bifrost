package modelcatalog

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func ptr(v float64) *float64 { return &v }
func intPtr(v int) *int      { return &v }

// chatPricing returns a TableModelPricing with the given per-token rates.
func chatPricing(input, output float64) configstoreTables.TableModelPricing {
	return configstoreTables.TableModelPricing{
		Model:              "test-model",
		Provider:           "test-provider",
		Mode:               "chat",
		InputCostPerToken:  input,
		OutputCostPerToken: output,
	}
}

// testCatalogWithPricing creates a catalog pre-loaded with the given pricing entries.
func testCatalogWithPricing(entries map[string]configstoreTables.TableModelPricing) *ModelCatalog {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	for k, v := range entries {
		mc.pricingData[k] = v
	}
	return mc
}

// makeChatResponse builds a minimal BifrostResponse for a chat completion.
func makeChatResponse(provider schemas.ModelProvider, model string, usage *schemas.BifrostLLMUsage) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ChatCompletionRequest,
				Provider:       provider,
				ModelRequested: model,
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
				RequestType:    schemas.EmbeddingRequest,
				Provider:       provider,
				ModelRequested: model,
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
				RequestType:    schemas.RerankRequest,
				Provider:       provider,
				ModelRequested: model,
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
				RequestType:    schemas.ImageGenerationRequest,
				Provider:       provider,
				ModelRequested: model,
			},
		},
	}
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
	cost := computeTextCost(&p, usage)
	// 1000 * 0.000005 + 500 * 0.000015 = 0.005 + 0.0075 = 0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestComputeTextCost_NilUsage(t *testing.T) {
	p := chatPricing(0.000005, 0.000015)
	assert.Equal(t, 0.0, computeTextCost(&p, nil))
}

func TestComputeTextCost_ZeroTokens(t *testing.T) {
	p := chatPricing(0.000005, 0.000015)
	usage := &schemas.BifrostLLMUsage{}
	assert.Equal(t, 0.0, computeTextCost(&p, usage))
}

func TestComputeTextCost_WithCachedPromptTokens(t *testing.T) {
	// Claude 3.5 Sonnet (Bedrock): input=$3/M, output=$15/M, cache_read=$0.3/M, cache_creation=$3.75/M
	p := chatPricing(0.000003, 0.000015)
	p.CacheReadInputTokenCost = ptr(0.0000003)
	p.CacheCreationInputTokenCost = ptr(0.00000375)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 500,
		TotalTokens:      2500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedTokens: 1500, // 1500 read from cache
		},
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			CachedTokens: 200, // 200 cache creation tokens
		},
	}

	cost := computeTextCost(&p, usage)

	// Input: (2000-1500)*0.000003 + 1500*0.0000003 = 0.0015 + 0.00045 = 0.00195
	// Output: (500-200)*0.000015 + 200*0.00000375 = 0.0045 + 0.00075 = 0.00525
	// Total: 0.00195 + 0.00525 = 0.0072
	assert.InDelta(t, 0.0072, cost, 1e-12)
}

func TestComputeTextCost_Tiered200k(t *testing.T) {
	// Claude 3.5 Sonnet Bedrock 200k tier: input=$6/M, output=$30/M
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = ptr(0.00003)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     180000,
		CompletionTokens: 30000,
		TotalTokens:      210000, // Above 200k threshold
	}

	cost := computeTextCost(&p, usage)

	// Uses tiered rate since total > 200k
	// 180000 * 0.000006 + 30000 * 0.00003 = 1.08 + 0.90 = 1.98
	assert.InDelta(t, 1.98, cost, 1e-9)
}

func TestComputeTextCost_Below200kUsesBaseRate(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.InputCostPerTokenAbove200kTokens = ptr(0.000006)
	p.OutputCostPerTokenAbove200kTokens = ptr(0.00003)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500, // Below 200k
	}

	cost := computeTextCost(&p, usage)

	// Uses base rate since total < 200k
	// 1000 * 0.000003 + 500 * 0.000015 = 0.003 + 0.0075 = 0.0105
	assert.InDelta(t, 0.0105, cost, 1e-12)
}

func TestComputeTextCost_SearchQueryCost(t *testing.T) {
	p := chatPricing(0.000003, 0.000015)
	p.SearchContextCostPerQuery = ptr(0.01) // $0.01 per search query

	numQueries := 3
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			NumSearchQueries: &numQueries,
		},
	}

	cost := computeTextCost(&p, usage)

	// 1000*0.000003 + 500*0.000015 + 3*0.01 = 0.003 + 0.0075 + 0.03 = 0.0405
	assert.InDelta(t, 0.0405, cost, 1e-12)
}

func TestComputeTextCost_NoCacheRateDoesNotChargeForCachedTokens(t *testing.T) {
	// If cache rate fields are nil, cached tokens are subtracted from prompt but not charged
	p := chatPricing(0.000005, 0.000015)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedTokens: 400,
		},
	}

	cost := computeTextCost(&p, usage)

	// Non-cached prompt: (1000-400)*0.000005 = 600*0.000005 = 0.003
	// Cached prompt: 400 tokens, no cache rate → not charged
	// Output: 500*0.000015 = 0.0075
	// Total: 0.003 + 0.0075 = 0.0105
	assert.InDelta(t, 0.0105, cost, 1e-12)
}

// =========================================================================
// 2. computeEmbeddingCost — unit tests
// =========================================================================

func TestComputeEmbeddingCost_Basic(t *testing.T) {
	// Titan Embed Text v1: $0.1/M input
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  0.0000001,
		OutputCostPerToken: 0,
	}
	usage := &schemas.BifrostLLMUsage{
		PromptTokens: 5000,
		TotalTokens:  5000,
	}
	cost := computeEmbeddingCost(&p, usage)
	// 5000 * 0.0000001 = 0.0005
	assert.InDelta(t, 0.0005, cost, 1e-12)
}

func TestComputeEmbeddingCost_NilUsage(t *testing.T) {
	p := configstoreTables.TableModelPricing{InputCostPerToken: 0.0000001}
	assert.Equal(t, 0.0, computeEmbeddingCost(&p, nil))
}

// =========================================================================
// 3. computeRerankCost — unit tests
// =========================================================================

func TestComputeRerankCost_Basic(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  0.000001,
		OutputCostPerToken: 0.000002,
	}
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     2000,
		CompletionTokens: 100,
		TotalTokens:      2100,
	}
	cost := computeRerankCost(&p, usage)
	// 2000*0.000001 + 100*0.000002 = 0.002 + 0.0002 = 0.0022
	assert.InDelta(t, 0.0022, cost, 1e-12)
}

func TestComputeRerankCost_WithSearchCost(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:         0,
		OutputCostPerToken:        0,
		SearchContextCostPerQuery: ptr(0.001),
	}
	numQueries := 5
	usage := &schemas.BifrostLLMUsage{
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			NumSearchQueries: &numQueries,
		},
	}
	cost := computeRerankCost(&p, usage)
	assert.InDelta(t, 0.005, cost, 1e-12)
}

func TestComputeRerankCost_NilUsage(t *testing.T) {
	p := configstoreTables.TableModelPricing{InputCostPerToken: 0.001}
	assert.Equal(t, 0.0, computeRerankCost(&p, nil))
}

// =========================================================================
// 4. computeSpeechCost — unit tests
// =========================================================================

func TestComputeSpeechCost_DurationBased(t *testing.T) {
	// azure/gpt-4o-mini-tts: output_cost_per_second=0.00025
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:   0.0000025,
		OutputCostPerToken:  0.00001,
		InputCostPerSecond:  ptr(0.00025),
		OutputCostPerSecond: ptr(0.00025),
	}
	seconds := 60
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     100,
		CompletionTokens: 200,
		TotalTokens:      300,
	}
	cost := computeSpeechCost(&p, usage, &seconds)
	// Duration-based: 60 * 0.00025 = 0.015 (uses InputCostPerSecond)
	// + output tokens: 200 * 0.00001 = 0.002
	// Total: 0.017
	assert.InDelta(t, 0.017, cost, 1e-12)
}

func TestComputeSpeechCost_AudioPerSecondRate(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:          0.000001,
		OutputCostPerToken:         0.000002,
		InputCostPerAudioPerSecond: ptr(0.0001),
	}
	seconds := 120
	usage := &schemas.BifrostLLMUsage{CompletionTokens: 50}
	cost := computeSpeechCost(&p, usage, &seconds)
	// 120 * 0.0001 = 0.012 + 50*0.000002 = 0.0001
	assert.InDelta(t, 0.0121, cost, 1e-12)
}

func TestComputeSpeechCost_TokenFallback(t *testing.T) {
	p := chatPricing(0.000005, 0.000015)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}
	cost := computeSpeechCost(&p, usage, nil) // No audio seconds → token fallback
	// 1000*0.000005 + 500*0.000015 = 0.005 + 0.0075 = 0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestComputeSpeechCost_NilUsageNilSeconds(t *testing.T) {
	p := chatPricing(0.000005, 0.000015)
	assert.Equal(t, 0.0, computeSpeechCost(&p, nil, nil))
}

// =========================================================================
// 5. computeTranscriptionCost — unit tests
// =========================================================================

func TestComputeTranscriptionCost_DurationBased(t *testing.T) {
	// assemblyai/nano: input_cost_per_second=0.00010278
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  0,
		OutputCostPerToken: 0,
		InputCostPerSecond: ptr(0.00010278),
	}
	seconds := 300 // 5 minutes
	cost := computeTranscriptionCost(&p, nil, &seconds, nil)
	// 300 * 0.00010278 = 0.030834
	assert.InDelta(t, 0.030834, cost, 1e-9)
}

func TestComputeTranscriptionCost_AudioTokenDetails(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:      0.000005,
		OutputCostPerToken:     0.000015,
		InputCostPerAudioToken: ptr(0.00001),
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
	cost := computeTranscriptionCost(&p, usage, nil, audioDetails)
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
	cost := computeTranscriptionCost(&p, usage, nil, nil)
	// 1000*0.000005 + 200*0.000015 = 0.005 + 0.003 = 0.008
	assert.InDelta(t, 0.008, cost, 1e-12)
}

func TestComputeTranscriptionCost_DurationPreferredOverTokenDetails(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:          0.000005,
		OutputCostPerToken:         0,
		InputCostPerAudioPerSecond: ptr(0.0001),
	}
	seconds := 60
	audioDetails := &schemas.TranscriptionUsageInputTokenDetails{
		AudioTokens: 5000,
		TextTokens:  1000,
	}
	cost := computeTranscriptionCost(&p, nil, &seconds, audioDetails)
	// Duration-based wins: 60 * 0.0001 = 0.006
	assert.InDelta(t, 0.006, cost, 1e-12)
}

// =========================================================================
// 6. computeImageCost — unit tests
// =========================================================================

func TestComputeImageCost_PerImage(t *testing.T) {
	// dall-e-3 (aiml): output_cost_per_image=$0.052
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  0,
		OutputCostPerToken: 0,
		OutputCostPerImage: ptr(0.052),
	}
	usage := &schemas.ImageUsage{
		OutputTokensDetails: &schemas.ImageTokenDetails{
			NImages: 2,
		},
	}
	cost := computeImageCost(&p, usage)
	// 2 * 0.052 = 0.104
	assert.InDelta(t, 0.104, cost, 1e-12)
}

func TestComputeImageCost_PerImageDefaultsToOne(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		OutputCostPerImage: ptr(0.052),
	}
	usage := &schemas.ImageUsage{} // No token details → defaults to 1 image
	cost := computeImageCost(&p, usage)
	assert.InDelta(t, 0.052, cost, 1e-12)
}

func TestComputeImageCost_TokenBased(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  0.000005,
		OutputCostPerToken: 0.000015,
	}
	usage := &schemas.ImageUsage{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
	}
	cost := computeImageCost(&p, usage)
	// 1000*0.000005 + 500*0.000015 = 0.005 + 0.0075 = 0.0125
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestComputeImageCost_TokenBasedWithDetails(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:  0.000005,
		OutputCostPerToken: 0.000015,
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
	cost := computeImageCost(&p, usage)
	// Input: (500+1500)*0.000005 = 2000*0.000005 = 0.01
	// Output: (200+800)*0.000015 = 1000*0.000015 = 0.015
	// Total: 0.025
	assert.InDelta(t, 0.025, cost, 1e-12)
}

func TestComputeImageCost_NilUsage(t *testing.T) {
	p := configstoreTables.TableModelPricing{OutputCostPerImage: ptr(0.05)}
	assert.Equal(t, 0.0, computeImageCost(&p, nil))
}

func TestComputeImageCost_InputAndOutputPerImage(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerImage:  ptr(0.01),
		OutputCostPerImage: ptr(0.05),
	}
	usage := &schemas.ImageUsage{
		InputTokensDetails: &schemas.ImageTokenDetails{NImages: 3},
	}
	cost := computeImageCost(&p, usage)
	// 3 * 0.01 + 3 * 0.05 = 0.03 + 0.15 = 0.18
	assert.InDelta(t, 0.18, cost, 1e-12)
}

// =========================================================================
// 7. computeVideoCost — unit tests
// =========================================================================

func TestComputeVideoCost_DurationBased(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:           0.000001,
		OutputCostPerToken:          0,
		OutputCostPerVideoPerSecond: ptr(0.001),
	}
	seconds := 30
	usage := &schemas.BifrostLLMUsage{PromptTokens: 500, TotalTokens: 500}
	cost := computeVideoCost(&p, usage, &seconds)
	// Output: 30 * 0.001 = 0.03
	// Input:  500 * 0.000001 = 0.0005
	// Total:  0.0305
	assert.InDelta(t, 0.0305, cost, 1e-12)
}

func TestComputeVideoCost_OutputCostPerSecondFallback(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:   0,
		OutputCostPerToken:  0,
		OutputCostPerSecond: ptr(0.002),
	}
	seconds := 10
	cost := computeVideoCost(&p, nil, &seconds)
	assert.InDelta(t, 0.02, cost, 1e-12)
}

func TestComputeVideoCost_NilSeconds(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:           0.000001,
		OutputCostPerVideoPerSecond: ptr(0.001),
	}
	usage := &schemas.BifrostLLMUsage{PromptTokens: 1000}
	cost := computeVideoCost(&p, usage, nil)
	// Only input tokens: 1000 * 0.000001 = 0.001
	assert.InDelta(t, 0.001, cost, 1e-12)
}

// =========================================================================
// 8. tieredInputRate / tieredOutputRate
// =========================================================================

func TestTieredInputRate_BelowThreshold(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:                0.000003,
		InputCostPerTokenAbove200kTokens: ptr(0.000006),
	}
	assert.Equal(t, 0.000003, tieredInputRate(&p, 100000))
}

func TestTieredInputRate_AboveThreshold(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken:                0.000003,
		InputCostPerTokenAbove200kTokens: ptr(0.000006),
	}
	assert.Equal(t, 0.000006, tieredInputRate(&p, 210000))
}

func TestTieredInputRate_AboveThresholdNoTieredRate(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		InputCostPerToken: 0.000003,
	}
	// Falls back to base rate when tiered field is nil
	assert.Equal(t, 0.000003, tieredInputRate(&p, 300000))
}

func TestTieredOutputRate_AboveThreshold(t *testing.T) {
	p := configstoreTables.TableModelPricing{
		OutputCostPerToken:                0.000015,
		OutputCostPerTokenAbove200kTokens: ptr(0.00003),
	}
	assert.Equal(t, 0.00003, tieredOutputRate(&p, 250000))
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
	sec := 60
	resp := &schemas.BifrostResponse{
		TranscriptionResponse: &schemas.BifrostTranscriptionResponse{
			Usage: &schemas.TranscriptionUsage{
				Seconds:      &sec,
				InputTokens:  intPtr(1000),
				OutputTokens: intPtr(200),
				TotalTokens:  intPtr(1200),
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
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): {
			Model: "gpt-4o", Provider: "openai", Mode: "chat",
			InputCostPerToken: 0.000005, OutputCostPerToken: 0.000015,
		},
	})

	hitType := "direct"
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ChatCompletionRequest,
				Provider:       schemas.OpenAI,
				ModelRequested: "gpt-4o",
				CacheDebug: &schemas.BifrostCacheDebug{
					CacheHit: true,
					HitType:  &hitType,
				},
			},
		},
	}

	cost := mc.CalculateCost(resp)
	assert.Equal(t, 0.0, cost)
}

func TestCalculateCost_SemanticCacheSemanticHit(t *testing.T) {
	embProvider := "openai"
	embModel := "text-embedding-3-small"
	embTokens := 500

	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): {
			Model: "gpt-4o", Provider: "openai", Mode: "chat",
			InputCostPerToken: 0.000005, OutputCostPerToken: 0.000015,
		},
		makeKey("text-embedding-3-small", "openai", "embedding"): {
			Model: "text-embedding-3-small", Provider: "openai", Mode: "embedding",
			InputCostPerToken: 0.00000002,
		},
	})

	hitType := "semantic"
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ChatCompletionRequest,
				Provider:       schemas.OpenAI,
				ModelRequested: "gpt-4o",
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

	cost := mc.CalculateCost(resp)
	// Only embedding cost: 500 * 0.00000002 = 0.00001
	assert.InDelta(t, 0.00001, cost, 1e-12)
}

func TestCalculateCost_SemanticCacheMiss(t *testing.T) {
	embProvider := "openai"
	embModel := "text-embedding-3-small"
	embTokens := 500

	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): {
			Model: "gpt-4o", Provider: "openai", Mode: "chat",
			InputCostPerToken: 0.000005, OutputCostPerToken: 0.000015,
		},
		makeKey("text-embedding-3-small", "openai", "embedding"): {
			Model: "text-embedding-3-small", Provider: "openai", Mode: "embedding",
			InputCostPerToken: 0.00000002,
		},
	})

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ChatCompletionRequest,
				Provider:       schemas.OpenAI,
				ModelRequested: "gpt-4o",
				CacheDebug: &schemas.BifrostCacheDebug{
					CacheHit:     false,
					ProviderUsed: &embProvider,
					ModelUsed:    &embModel,
					InputTokens:  &embTokens,
				},
			},
		},
	}

	cost := mc.CalculateCost(resp)
	// Base cost: 1000*0.000005 + 500*0.000015 = 0.005 + 0.0075 = 0.0125
	// Embedding cost: 500 * 0.00000002 = 0.00001
	// Total: 0.01251
	assert.InDelta(t, 0.01251, cost, 1e-12)
}

func TestCalculateCost_SemanticCacheHitNoEmbeddingInfo(t *testing.T) {
	mc := testCatalogWithPricing(nil)

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

	cost := mc.CalculateCost(resp)
	assert.Equal(t, 0.0, cost)
}

// =========================================================================
// 11. CalculateCost integration — end-to-end
// =========================================================================

func TestCalculateCost_NilResponse(t *testing.T) {
	mc := testCatalogWithPricing(nil)
	assert.Equal(t, 0.0, mc.CalculateCost(nil))
}

func TestCalculateCost_ProviderComputedCostPassthrough(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
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

	cost := mc.CalculateCost(resp)
	assert.Equal(t, 0.99, cost)
}

func TestCalculateCost_NoUsageData(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	resp := makeChatResponse(schemas.OpenAI, "gpt-4o", nil)
	cost := mc.CalculateCost(resp)
	assert.Equal(t, 0.0, cost)
}

func TestCalculateCost_ChatCompletion_GPT4o(t *testing.T) {
	// GPT-4o: $5/M input, $15/M output, cache_read=$0.5/M
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): {
			Model: "gpt-4o", Provider: "openai", Mode: "chat",
			InputCostPerToken:       0.000005,
			OutputCostPerToken:      0.000015,
			CacheReadInputTokenCost: ptr(0.0000005),
		},
	})

	resp := makeChatResponse(schemas.OpenAI, "gpt-4o", &schemas.BifrostLLMUsage{
		PromptTokens:     10000,
		CompletionTokens: 2000,
		TotalTokens:      12000,
	})

	cost := mc.CalculateCost(resp)
	// 10000*0.000005 + 2000*0.000015 = 0.05 + 0.03 = 0.08
	assert.InDelta(t, 0.08, cost, 1e-12)
}

func TestCalculateCost_ChatCompletion_Claude35Sonnet_WithCache(t *testing.T) {
	// Claude 3.5 Sonnet (Bedrock): $3/M input, $15/M output, cache_read=$0.3/M, cache_creation=$3.75/M
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("anthropic.claude-3-5-sonnet-20241022-v2:0", "bedrock", "chat"): {
			Model: "anthropic.claude-3-5-sonnet-20241022-v2:0", Provider: "bedrock", Mode: "chat",
			InputCostPerToken:                 0.000003,
			OutputCostPerToken:                0.000015,
			CacheReadInputTokenCost:           ptr(0.0000003),
			CacheCreationInputTokenCost:       ptr(0.00000375),
			InputCostPerTokenAbove200kTokens:  ptr(0.000006),
			OutputCostPerTokenAbove200kTokens: ptr(0.00003),
		},
	})

	resp := makeChatResponse(schemas.Bedrock, "anthropic.claude-3-5-sonnet-20241022-v2:0", &schemas.BifrostLLMUsage{
		PromptTokens:     5000,
		CompletionTokens: 1000,
		TotalTokens:      6000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedTokens: 3000, // 3000 cache read tokens
		},
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			CachedTokens: 500, // 500 cache creation tokens
		},
	})

	cost := mc.CalculateCost(resp)
	// Input: (5000-3000)*0.000003 + 3000*0.0000003 = 0.006 + 0.0009 = 0.0069
	// Output: (1000-500)*0.000015 + 500*0.00000375 = 0.0075 + 0.001875 = 0.009375
	// Total: 0.0069 + 0.009375 = 0.016275
	assert.InDelta(t, 0.016275, cost, 1e-12)
}

func TestCalculateCost_Embedding(t *testing.T) {
	// Titan Embed Text v1: $0.1/M input
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("amazon.titan-embed-text-v1", "bedrock", "embedding"): {
			Model: "amazon.titan-embed-text-v1", Provider: "bedrock", Mode: "embedding",
			InputCostPerToken:  0.0000001,
			OutputCostPerToken: 0,
		},
	})

	resp := makeEmbeddingResponse(schemas.Bedrock, "amazon.titan-embed-text-v1", &schemas.BifrostLLMUsage{
		PromptTokens: 10000,
		TotalTokens:  10000,
	})

	cost := mc.CalculateCost(resp)
	// 10000 * 0.0000001 = 0.001
	assert.InDelta(t, 0.001, cost, 1e-12)
}

func TestCalculateCost_Rerank(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("amazon.rerank-v1:0", "bedrock", "rerank"): {
			Model: "amazon.rerank-v1:0", Provider: "bedrock", Mode: "rerank",
			InputCostPerToken:  0,
			OutputCostPerToken: 0,
		},
	})

	resp := makeRerankResponse(schemas.Bedrock, "amazon.rerank-v1:0", &schemas.BifrostLLMUsage{
		PromptTokens: 500,
		TotalTokens:  500,
	})

	cost := mc.CalculateCost(resp)
	assert.Equal(t, 0.0, cost)
}

func TestCalculateCost_ImageGeneration(t *testing.T) {
	// dall-e-3 via aiml: output_cost_per_image=$0.052
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("dall-e-3", "aiml", "image_generation"): {
			Model: "dall-e-3", Provider: "aiml", Mode: "image_generation",
			OutputCostPerImage: ptr(0.052),
		},
	})

	resp := makeImageResponse("aiml", "dall-e-3", &schemas.ImageUsage{
		OutputTokensDetails: &schemas.ImageTokenDetails{NImages: 3},
	})

	cost := mc.CalculateCost(resp)
	// 3 * 0.052 = 0.156
	assert.InDelta(t, 0.156, cost, 1e-12)
}

func TestCalculateCost_StreamRequestTypeNormalized(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	// Stream request type should be normalized to base type
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ChatCompletionStreamRequest,
				Provider:       schemas.OpenAI,
				ModelRequested: "gpt-4o",
			},
		},
	}

	cost := mc.CalculateCost(resp)
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestCalculateCost_NoPricingData(t *testing.T) {
	mc := testCatalogWithPricing(nil)
	resp := makeChatResponse(schemas.OpenAI, "unknown-model", &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
	})
	cost := mc.CalculateCost(resp)
	assert.Equal(t, 0.0, cost)
}

// =========================================================================
// 12. Pricing resolution — getPricing fallback logic
// =========================================================================

func TestGetPricing_DirectLookup(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})
	p, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest)
	require.True(t, ok)
	assert.Equal(t, 0.000005, p.InputCostPerToken)
}

func TestGetPricing_GeminiFallsBackToVertex(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gemini-2.0-flash", "vertex", "chat"): {
			Model: "gemini-2.0-flash", Provider: "vertex", Mode: "chat",
			InputCostPerToken: 0.0000001, OutputCostPerToken: 0.0000004,
		},
	})
	p, ok := mc.getPricing("gemini-2.0-flash", "gemini", schemas.ChatCompletionRequest)
	require.True(t, ok)
	assert.Equal(t, 0.0000001, p.InputCostPerToken)
}

func TestGetPricing_VertexStripsProviderPrefix(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gemini-2.0-flash", "vertex", "chat"): chatPricing(0.0000001, 0.0000004),
	})
	p, ok := mc.getPricing("google/gemini-2.0-flash", "vertex", schemas.ChatCompletionRequest)
	require.True(t, ok)
	assert.Equal(t, 0.0000001, p.InputCostPerToken)
}

func TestGetPricing_BedrockAddsAnthropicPrefix(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("anthropic.claude-3-5-sonnet-20241022-v2:0", "bedrock", "chat"): chatPricing(0.000003, 0.000015),
	})
	p, ok := mc.getPricing("claude-3-5-sonnet-20241022-v2:0", "bedrock", schemas.ChatCompletionRequest)
	require.True(t, ok)
	assert.Equal(t, 0.000003, p.InputCostPerToken)
}

func TestGetPricing_ResponsesFallsBackToChat(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})
	p, ok := mc.getPricing("gpt-4o", "openai", schemas.ResponsesRequest)
	require.True(t, ok)
	assert.Equal(t, 0.000005, p.InputCostPerToken)
}

func TestGetPricing_ResponsesStreamFallsBackToChat(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})
	p, ok := mc.getPricing("gpt-4o", "openai", schemas.ResponsesStreamRequest)
	require.True(t, ok)
	assert.Equal(t, 0.000005, p.InputCostPerToken)
}

func TestGetPricing_GeminiResponsesFallsBackToVertexChat(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gemini-2.0-flash", "vertex", "chat"): chatPricing(0.0000001, 0.0000004),
	})
	// gemini provider + responses request → try vertex + responses → try vertex + chat
	p, ok := mc.getPricing("gemini-2.0-flash", "gemini", schemas.ResponsesRequest)
	require.True(t, ok)
	assert.Equal(t, 0.0000001, p.InputCostPerToken)
}

func TestGetPricing_NotFound(t *testing.T) {
	mc := testCatalogWithPricing(nil)
	_, ok := mc.getPricing("nonexistent", "openai", schemas.ChatCompletionRequest)
	assert.False(t, ok)
}

// =========================================================================
// 13. resolvePricing — deployment fallback
// =========================================================================

func TestResolvePricing_DeploymentFallback(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("my-deployment", "openai", "chat"): chatPricing(0.000005, 0.000015),
	})

	// Model not found directly, but deployment matches
	p := mc.resolvePricing("openai", "gpt-4o-custom", "my-deployment", schemas.ChatCompletionRequest)
	require.NotNil(t, p)
	assert.Equal(t, 0.000005, p.InputCostPerToken)
}

func TestResolvePricing_ModelFoundDirectly(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("gpt-4o", "openai", "chat"):        chatPricing(0.000005, 0.000015),
		makeKey("my-deployment", "openai", "chat"): chatPricing(0.000001, 0.000002),
	})

	// Model found directly — doesn't fall back to deployment
	p := mc.resolvePricing("openai", "gpt-4o", "my-deployment", schemas.ChatCompletionRequest)
	require.NotNil(t, p)
	assert.Equal(t, 0.000005, p.InputCostPerToken)
}

func TestResolvePricing_NothingFound(t *testing.T) {
	mc := testCatalogWithPricing(nil)
	p := mc.resolvePricing("openai", "unknown", "", schemas.ChatCompletionRequest)
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
			CachedTokens: 300,
			TextTokens:   600,
			AudioTokens:  50,
			ImageTokens:  50,
		},
		OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{
			ReasoningTokens:  100,
			CachedTokens:     50,
			NumSearchQueries: &numQueries,
		},
	}
	result := responsesUsageToBifrostUsage(u)

	require.NotNil(t, result.PromptTokensDetails)
	assert.Equal(t, 300, result.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 600, result.PromptTokensDetails.TextTokens)
	assert.Equal(t, 50, result.PromptTokensDetails.AudioTokens)
	assert.Equal(t, 50, result.PromptTokensDetails.ImageTokens)

	require.NotNil(t, result.CompletionTokensDetails)
	assert.Equal(t, 100, result.CompletionTokensDetails.ReasoningTokens)
	assert.Equal(t, 50, result.CompletionTokensDetails.CachedTokens)
	require.NotNil(t, result.CompletionTokensDetails.NumSearchQueries)
	assert.Equal(t, 2, *result.CompletionTokensDetails.NumSearchQueries)
}

// =========================================================================
// 16. Edge cases
// =========================================================================

func TestCalculateCost_200kTier_EndToEnd(t *testing.T) {
	// Claude 3.5 Sonnet Bedrock with 200k tier pricing
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("anthropic.claude-3-5-sonnet-20240620-v1:0", "bedrock", "chat"): {
			Model: "anthropic.claude-3-5-sonnet-20240620-v1:0", Provider: "bedrock", Mode: "chat",
			InputCostPerToken:                          0.000003,
			OutputCostPerToken:                         0.000015,
			InputCostPerTokenAbove200kTokens:           ptr(0.000006),
			OutputCostPerTokenAbove200kTokens:          ptr(0.00003),
			CacheReadInputTokenCost:                    ptr(0.0000003),
			CacheCreationInputTokenCost:                ptr(0.00000375),
			CacheReadInputTokenCostAbove200kTokens:     ptr(0.0000006),
			CacheCreationInputTokenCostAbove200kTokens: ptr(0.0000075),
		},
	})

	resp := makeChatResponse(schemas.Bedrock, "anthropic.claude-3-5-sonnet-20240620-v1:0", &schemas.BifrostLLMUsage{
		PromptTokens:     190000,
		CompletionTokens: 20000,
		TotalTokens:      210000, // Above 200k
	})

	cost := mc.CalculateCost(resp)
	// Tiered rate: input=0.000006, output=0.00003
	// 190000*0.000006 + 20000*0.00003 = 1.14 + 0.6 = 1.74
	assert.InDelta(t, 1.74, cost, 1e-9)
}

func TestCalculateCost_ProviderCostZeroTotalStillCalculates(t *testing.T) {
	mc := testCatalogWithPricing(map[string]configstoreTables.TableModelPricing{
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

	cost := mc.CalculateCost(resp)
	assert.InDelta(t, 0.0125, cost, 1e-12)
}

func TestCalculateCost_AllCachedTokens(t *testing.T) {
	// All prompt tokens are from cache
	p := chatPricing(0.000005, 0.000015)
	p.CacheReadInputTokenCost = ptr(0.0000005)

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 0,
		TotalTokens:      1000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedTokens: 1000, // All cached
		},
	}

	cost := computeTextCost(&p, usage)
	// Non-cached: 0, cached: 1000*0.0000005 = 0.0005
	assert.InDelta(t, 0.0005, cost, 1e-12)
}
