package modelcatalog

import (
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// costInput holds the extracted usage data from a BifrostResponse,
// normalized for the pricing engine.
type costInput struct {
	usage             *schemas.BifrostLLMUsage
	audioSeconds      *int
	audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails
	imageUsage        *schemas.ImageUsage
	imageSize         string // e.g. "1024x1024", used for per-pixel pricing
	videoSeconds      *int
}

// CalculateCost calculates the cost of a Bifrost response.
// It handles all request types, cache debug billing, and tiered pricing.
func (mc *ModelCatalog) CalculateCost(result *schemas.BifrostResponse) float64 {
	if result == nil {
		return 0
	}

	// Handle semantic cache billing
	cacheDebug := result.GetExtraFields().CacheDebug
	if cacheDebug != nil {
		return mc.calculateCostWithCache(result, cacheDebug)
	}

	return mc.calculateBaseCost(result)
}

// calculateCostWithCache handles cost calculation when semantic cache debug info is present.
func (mc *ModelCatalog) calculateCostWithCache(result *schemas.BifrostResponse, cacheDebug *schemas.BifrostCacheDebug) float64 {
	if cacheDebug.CacheHit {
		// Direct cache hit — no LLM call, no cost
		if cacheDebug.HitType != nil && *cacheDebug.HitType == "direct" {
			return 0
		}
		// Semantic cache hit — only the embedding lookup cost
		if cacheDebug.ProviderUsed != nil && cacheDebug.ModelUsed != nil && cacheDebug.InputTokens != nil {
			return mc.computeCacheEmbeddingCost(cacheDebug)
		}
		return 0
	}

	// Cache miss — full LLM cost + embedding lookup cost
	baseCost := mc.calculateBaseCost(result)
	embeddingCost := mc.computeCacheEmbeddingCost(cacheDebug)
	return baseCost + embeddingCost
}

// computeCacheEmbeddingCost calculates the embedding cost for a semantic cache lookup.
func (mc *ModelCatalog) computeCacheEmbeddingCost(cacheDebug *schemas.BifrostCacheDebug) float64 {
	if cacheDebug == nil || cacheDebug.ProviderUsed == nil || cacheDebug.ModelUsed == nil || cacheDebug.InputTokens == nil {
		return 0
	}
	pricing, exists := mc.getPricing(*cacheDebug.ModelUsed, *cacheDebug.ProviderUsed, schemas.EmbeddingRequest)
	if !exists {
		return 0
	}
	return float64(*cacheDebug.InputTokens) * pricing.InputCostPerToken
}

// calculateBaseCost extracts usage from the response and routes to the appropriate compute function.
func (mc *ModelCatalog) calculateBaseCost(result *schemas.BifrostResponse) float64 {
	extraFields := result.GetExtraFields()
	if extraFields == nil {
		return 0
	}

	provider := string(extraFields.Provider)
	model := extraFields.ModelRequested
	deployment := extraFields.ModelDeployment
	requestType := extraFields.RequestType

	// Extract usage data from the response
	input := extractCostInput(result)

	// If provider already computed cost, use it
	if input.usage != nil && input.usage.Cost != nil && input.usage.Cost.TotalCost > 0 {
		return input.usage.Cost.TotalCost
	}

	// If no usage data at all, nothing to price
	if input.usage == nil && input.audioSeconds == nil && input.audioTokenDetails == nil && input.imageUsage == nil && input.videoSeconds == nil {
		return 0
	}

	// Normalize stream request types to their base type for pricing lookup
	requestType = normalizeStreamRequestType(requestType)

	// Resolve pricing entry with deployment fallback
	pricing := mc.resolvePricing(provider, model, deployment, requestType)
	if pricing == nil {
		return 0
	}

	// Route to the appropriate compute function
	switch requestType {
	case schemas.ChatCompletionRequest, schemas.TextCompletionRequest, schemas.ResponsesRequest:
		return computeTextCost(pricing, input.usage)
	case schemas.EmbeddingRequest:
		return computeEmbeddingCost(pricing, input.usage)
	case schemas.RerankRequest:
		return computeRerankCost(pricing, input.usage)
	case schemas.SpeechRequest:
		return computeSpeechCost(pricing, input.usage, input.audioSeconds)
	case schemas.TranscriptionRequest:
		return computeTranscriptionCost(pricing, input.usage, input.audioSeconds, input.audioTokenDetails)
	case schemas.ImageGenerationRequest:
		return computeImageCost(pricing, input.imageUsage, input.imageSize)
	case schemas.VideoGenerationRequest:
		return computeVideoCost(pricing, input.usage, input.videoSeconds)
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// Usage extraction
// ---------------------------------------------------------------------------

func extractCostInput(result *schemas.BifrostResponse) costInput {
	var input costInput

	switch {
	case result.TextCompletionResponse != nil && result.TextCompletionResponse.Usage != nil:
		input.usage = result.TextCompletionResponse.Usage

	case result.ChatResponse != nil && result.ChatResponse.Usage != nil:
		input.usage = result.ChatResponse.Usage

	case result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil:
		input.usage = responsesUsageToBifrostUsage(result.ResponsesResponse.Usage)

	case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil && result.ResponsesStreamResponse.Response.Usage != nil:
		input.usage = responsesUsageToBifrostUsage(result.ResponsesStreamResponse.Response.Usage)

	case result.EmbeddingResponse != nil && result.EmbeddingResponse.Usage != nil:
		input.usage = result.EmbeddingResponse.Usage

	case result.RerankResponse != nil && result.RerankResponse.Usage != nil:
		input.usage = result.RerankResponse.Usage

	case result.SpeechResponse != nil && result.SpeechResponse.Usage != nil:
		input.usage = speechUsageToBifrostUsage(result.SpeechResponse.Usage)

	case result.SpeechStreamResponse != nil && result.SpeechStreamResponse.Usage != nil:
		input.usage = speechUsageToBifrostUsage(result.SpeechStreamResponse.Usage)

	case result.TranscriptionResponse != nil && result.TranscriptionResponse.Usage != nil:
		input.usage, input.audioSeconds, input.audioTokenDetails = extractTranscriptionUsage(result.TranscriptionResponse.Usage)

	case result.TranscriptionStreamResponse != nil && result.TranscriptionStreamResponse.Usage != nil:
		input.usage, input.audioSeconds, input.audioTokenDetails = extractTranscriptionUsage(result.TranscriptionStreamResponse.Usage)

	case result.ImageGenerationResponse != nil:
		if result.ImageGenerationResponse.Usage != nil {
			input.imageUsage = result.ImageGenerationResponse.Usage
		} else {
			// No usage data but response exists — default to empty so per-image pricing can apply
			input.imageUsage = &schemas.ImageUsage{}
		}
		populateOutputImageCount(input.imageUsage, len(result.ImageGenerationResponse.Data))
		if result.ImageGenerationResponse.ImageGenerationResponseParameters != nil {
			input.imageSize = result.ImageGenerationResponse.ImageGenerationResponseParameters.Size
		}

	case result.ImageGenerationStreamResponse != nil:
		if result.ImageGenerationStreamResponse.Usage != nil {
			input.imageUsage = result.ImageGenerationStreamResponse.Usage
		} else {
			input.imageUsage = &schemas.ImageUsage{}
		}
		input.imageSize = result.ImageGenerationStreamResponse.Size

	case result.VideoGenerationResponse != nil && result.VideoGenerationResponse.Seconds != nil:
		seconds, err := strconv.Atoi(*result.VideoGenerationResponse.Seconds)
		if err == nil {
			input.videoSeconds = &seconds
		}
	}

	return input
}

func responsesUsageToBifrostUsage(u *schemas.ResponsesResponseUsage) *schemas.BifrostLLMUsage {
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
		Cost:             u.Cost,
	}
	// Map token details for cache and search query pricing
	if u.InputTokensDetails != nil {
		usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			TextTokens:        u.InputTokensDetails.TextTokens,
			AudioTokens:       u.InputTokensDetails.AudioTokens,
			ImageTokens:       u.InputTokensDetails.ImageTokens,
			CachedReadTokens:  u.InputTokensDetails.CachedReadTokens,
			CachedWriteTokens: u.InputTokensDetails.CachedWriteTokens,
		}
	}
	if u.OutputTokensDetails != nil {
		usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{
			ReasoningTokens: u.OutputTokensDetails.ReasoningTokens,
		}
		if u.OutputTokensDetails.NumSearchQueries != nil {
			usage.CompletionTokensDetails.NumSearchQueries = u.OutputTokensDetails.NumSearchQueries
		}
	}
	return usage
}

func speechUsageToBifrostUsage(u *schemas.SpeechUsage) *schemas.BifrostLLMUsage {
	return &schemas.BifrostLLMUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
}

func extractTranscriptionUsage(u *schemas.TranscriptionUsage) (*schemas.BifrostLLMUsage, *int, *schemas.TranscriptionUsageInputTokenDetails) {
	usage := &schemas.BifrostLLMUsage{}
	if u.InputTokens != nil {
		usage.PromptTokens = *u.InputTokens
	}
	if u.OutputTokens != nil {
		usage.CompletionTokens = *u.OutputTokens
	}
	if u.TotalTokens != nil {
		usage.TotalTokens = *u.TotalTokens
	} else {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	var audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails
	if u.InputTokenDetails != nil {
		audioTokenDetails = &schemas.TranscriptionUsageInputTokenDetails{
			AudioTokens: u.InputTokenDetails.AudioTokens,
			TextTokens:  u.InputTokenDetails.TextTokens,
		}
	}

	return usage, u.Seconds, audioTokenDetails
}

// ---------------------------------------------------------------------------
// Per-request-type cost computation
// ---------------------------------------------------------------------------

// computeTextCost handles chat, text completion, and responses requests.
func computeTextCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage) float64 {
	if usage == nil {
		return 0
	}

	totalTokens := usage.TotalTokens
	promptTokens := usage.PromptTokens
	completionTokens := usage.CompletionTokens

	// Extract cached token counts
	cachedReadTokens := 0
	cachedWriteTokens := 0
	if usage.PromptTokensDetails != nil {
		cachedReadTokens = usage.PromptTokensDetails.CachedReadTokens
		cachedWriteTokens = usage.PromptTokensDetails.CachedWriteTokens
	}

	inputRate := tieredInputRate(pricing, totalTokens)
	outputRate := tieredOutputRate(pricing, totalTokens)
	cacheReadInputRate := tieredCacheReadInputTokenRate(pricing, cachedReadTokens)
	cacheCreationInputRate := tieredCacheCreationInputTokenRate(pricing, cachedWriteTokens)

	// Input cost: non-cached tokens at regular rate
	nonCachedPrompt := promptTokens - cachedReadTokens - cachedWriteTokens
	inputCost := float64(nonCachedPrompt) * inputRate

	// Add cached prompt tokens at cache read rate
	if cachedReadTokens > 0 {
		inputCost += float64(cachedReadTokens) * cacheReadInputRate
	}

	// Add cached write tokens at cache creation rate
	if cachedWriteTokens > 0 {
		inputCost += float64(cachedWriteTokens) * cacheCreationInputRate
	}

	outputCost := float64(completionTokens) * outputRate

	// Search query cost
	searchCost := 0.0
	if pricing.SearchContextCostPerQuery != nil && usage.CompletionTokensDetails != nil && usage.CompletionTokensDetails.NumSearchQueries != nil {
		searchCost = float64(*usage.CompletionTokensDetails.NumSearchQueries) * *pricing.SearchContextCostPerQuery
	}

	return inputCost + outputCost + searchCost
}

// computeEmbeddingCost handles embedding requests (input-only).
func computeEmbeddingCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage) float64 {
	if usage == nil {
		return 0
	}
	return float64(usage.PromptTokens) * pricing.InputCostPerToken
}

// computeRerankCost handles rerank requests.
func computeRerankCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage) float64 {
	if usage == nil {
		return 0
	}
	inputCost := float64(usage.PromptTokens) * pricing.InputCostPerToken
	outputCost := float64(usage.CompletionTokens) * pricing.OutputCostPerToken

	searchCost := 0.0
	if pricing.SearchContextCostPerQuery != nil && usage.CompletionTokensDetails != nil && usage.CompletionTokensDetails.NumSearchQueries != nil {
		searchCost = float64(*usage.CompletionTokensDetails.NumSearchQueries) * *pricing.SearchContextCostPerQuery
	}

	return inputCost + outputCost + searchCost
}

// computeSpeechCost handles speech (TTS) requests.
// Input is text (PromptTokens), output is audio (CompletionTokens).
// Input and output are calculated independently — tokens first, then per-second fallback.
func computeSpeechCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int) float64 {
	totalTokens := safeTotalTokens(usage)

	// Input: text prompt tokens
	inputCost := 0.0
	if usage != nil && usage.PromptTokens > 0 {
		inputCost = float64(usage.PromptTokens) * tieredInputRate(pricing, totalTokens)
	}

	// Output: audio tokens first, then per-second fallback
	outputCost := computeAudioOutputCost(pricing, usage, audioSeconds, totalTokens)

	return inputCost + outputCost
}

// computeTranscriptionCost handles transcription (STT) requests.
// Input is audio, output is text (CompletionTokens).
// Input and output are calculated independently — tokens first, then per-second fallback.
func computeTranscriptionCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails) float64 {
	totalTokens := safeTotalTokens(usage)

	// Input: audio tokens/details first, then per-second fallback
	inputCost := computeAudioInputCost(pricing, usage, audioSeconds, audioTokenDetails, totalTokens)

	// Output: text tokens
	outputCost := 0.0
	if usage != nil && usage.CompletionTokens > 0 {
		outputCost = float64(usage.CompletionTokens) * tieredOutputRate(pricing, totalTokens)
	}

	return inputCost + outputCost
}

// computeAudioInputCost calculates input cost for audio: audio token details first,
// then generic input tokens, then per-second duration fallback.
func computeAudioInputCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails, totalTokens int) float64 {
	// Audio token detail pricing (audio + text token breakdown)
	if audioTokenDetails != nil && (audioTokenDetails.AudioTokens > 0 || audioTokenDetails.TextTokens > 0) {
		inputRate := tieredInputRate(pricing, totalTokens)
		audioRate := inputRate
		if pricing.InputCostPerAudioToken != nil {
			audioRate = *pricing.InputCostPerAudioToken
		}
		return float64(audioTokenDetails.AudioTokens)*audioRate + float64(audioTokenDetails.TextTokens)*inputRate
	}

	// Generic input tokens
	if usage != nil && usage.PromptTokens > 0 {
		return float64(usage.PromptTokens) * tieredInputRate(pricing, totalTokens)
	}

	// Per-second duration fallback
	if audioSeconds != nil && *audioSeconds > 0 {
		if pricing.InputCostPerAudioPerSecond != nil {
			return float64(*audioSeconds) * *pricing.InputCostPerAudioPerSecond
		}
		if pricing.InputCostPerSecond != nil {
			return float64(*audioSeconds) * *pricing.InputCostPerSecond
		}
	}

	return 0
}

// computeAudioOutputCost calculates output cost for audio: audio tokens first,
// then generic output tokens, then per-second duration fallback.
func computeAudioOutputCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, totalTokens int) float64 {
	// Audio-specific output tokens
	if usage != nil && usage.CompletionTokens > 0 {
		if pricing.OutputCostPerAudioToken != nil {
			return float64(usage.CompletionTokens) * *pricing.OutputCostPerAudioToken
		}
		return float64(usage.CompletionTokens) * tieredOutputRate(pricing, totalTokens)
	}

	// Per-second duration fallback
	if audioSeconds != nil && *audioSeconds > 0 {
		if pricing.OutputCostPerSecond != nil {
			return float64(*audioSeconds) * *pricing.OutputCostPerSecond
		}
	}

	return 0
}

// computeImageCost handles image generation requests.
// Input and output are calculated independently — each tries token-based pricing first,
// then per-pixel pricing, falling back to per-image count pricing.
func computeImageCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, imageSize string) float64 {
	if imageUsage == nil {
		return 0
	}

	totalTokens := imageUsage.TotalTokens
	pixels := parseImagePixels(imageSize)
	inputCost := computeImageInputCost(pricing, imageUsage, totalTokens, pixels)
	outputCost := computeImageOutputCost(pricing, imageUsage, totalTokens, pixels)

	return inputCost + outputCost
}

// computeImageInputCost calculates input cost: tokens first, then per-pixel, then per-image count fallback.
func computeImageInputCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, totalTokens int, pixels int) float64 {
	// Try token-based pricing first
	var inputTextTokens, inputImageTokens int
	if imageUsage.InputTokensDetails != nil {
		inputImageTokens = imageUsage.InputTokensDetails.ImageTokens
		inputTextTokens = imageUsage.InputTokensDetails.TextTokens
	} else {
		inputTextTokens = imageUsage.InputTokens
	}

	if inputTextTokens > 0 || inputImageTokens > 0 {
		inputRate := tieredInputRate(pricing, totalTokens)
		return float64(inputTextTokens)*inputRate + float64(inputImageTokens)*inputRate
	}

	// Per-pixel pricing fallback
	if pricing.InputCostPerPixel != nil && pixels > 0 {
		numImages := imageUsage.NumInputImages
		if numImages == 0 {
			numImages = 1
		}
		return float64(pixels*numImages) * *pricing.InputCostPerPixel
	}

	// Fall back to per-image count pricing
	if pricing.InputCostPerImage != nil && imageUsage.NumInputImages > 0 {
		return float64(imageUsage.NumInputImages) * *pricing.InputCostPerImage
	}

	return 0
}

// computeImageOutputCost calculates output cost: tokens first, then per-pixel, then per-image count fallback.
func computeImageOutputCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, totalTokens int, pixels int) float64 {
	// Try token-based pricing first
	var outputTextTokens, outputImageTokens int
	if imageUsage.OutputTokensDetails != nil {
		outputImageTokens = imageUsage.OutputTokensDetails.ImageTokens
		outputTextTokens = imageUsage.OutputTokensDetails.TextTokens
	} else {
		outputImageTokens = imageUsage.OutputTokens
	}

	if outputTextTokens > 0 || outputImageTokens > 0 {
		outputRate := tieredOutputRate(pricing, totalTokens)
		return float64(outputTextTokens)*outputRate + float64(outputImageTokens)*outputRate
	}

	// Per-pixel pricing fallback
	if pricing.OutputCostPerPixel != nil && pixels > 0 {
		numOutputImages := 1
		if imageUsage.OutputTokensDetails != nil && imageUsage.OutputTokensDetails.NImages > 0 {
			numOutputImages = imageUsage.OutputTokensDetails.NImages
		}
		return float64(pixels*numOutputImages) * *pricing.OutputCostPerPixel
	}

	// Fall back to per-image count pricing
	if pricing.OutputCostPerImage != nil {
		numOutputImages := 1
		if imageUsage.OutputTokensDetails != nil && imageUsage.OutputTokensDetails.NImages > 0 {
			numOutputImages = imageUsage.OutputTokensDetails.NImages
		}
		return float64(numOutputImages) * *pricing.OutputCostPerImage
	}

	return 0
}

// computeVideoCost handles video generation requests.
// Input and output are calculated independently — tokens first, then per-second fallback.
func computeVideoCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, videoSeconds *int) float64 {
	// Input: text prompt tokens first, then per-second fallback
	inputCost := 0.0
	if usage != nil && usage.PromptTokens > 0 {
		inputCost = float64(usage.PromptTokens) * pricing.InputCostPerToken
	} else if videoSeconds != nil && *videoSeconds > 0 && pricing.InputCostPerVideoPerSecond != nil {
		inputCost = float64(*videoSeconds) * *pricing.InputCostPerVideoPerSecond
	}

	// Output: completion tokens first, then per-second fallback
	outputCost := 0.0
	if usage != nil && usage.CompletionTokens > 0 {
		outputCost = float64(usage.CompletionTokens) * pricing.OutputCostPerToken
	} else if videoSeconds != nil && *videoSeconds > 0 {
		if pricing.OutputCostPerVideoPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerVideoPerSecond
		} else if pricing.OutputCostPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerSecond
		}
	}

	return inputCost + outputCost
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func tieredInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove200K && pricing.InputCostPerTokenAbove200kTokens != nil {
		return *pricing.InputCostPerTokenAbove200kTokens
	}
	return pricing.InputCostPerToken
}

func tieredOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove200K && pricing.OutputCostPerTokenAbove200kTokens != nil {
		return *pricing.OutputCostPerTokenAbove200kTokens
	}
	return pricing.OutputCostPerToken
}

func tieredCacheReadInputTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove200K && pricing.CacheReadInputTokenCostAbove200kTokens != nil {
		return *pricing.CacheReadInputTokenCostAbove200kTokens
	}
	if pricing.CacheReadInputTokenCost != nil {
		return *pricing.CacheReadInputTokenCost
	}
	return pricing.InputCostPerToken
}

func tieredCacheCreationInputTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove200K && pricing.CacheCreationInputTokenCostAbove200kTokens != nil {
		return *pricing.CacheCreationInputTokenCostAbove200kTokens
	}
	if pricing.CacheCreationInputTokenCost != nil {
		return *pricing.CacheCreationInputTokenCost
	}
	return pricing.InputCostPerToken
}

func safeTotalTokens(usage *schemas.BifrostLLMUsage) int {
	if usage == nil {
		return 0
	}
	return usage.TotalTokens
}

// parseImagePixels parses a size string like "1024x1024" into total pixel count.
// Returns 0 if the size string is empty or malformed.
func parseImagePixels(size string) int {
	if size == "" {
		return 0
	}
	parts := strings.SplitN(size, "x", 2)
	if len(parts) != 2 {
		return 0
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil || w <= 0 {
		return 0
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil || h <= 0 {
		return 0
	}
	return w * h
}

// populateOutputImageCount sets the output image count on ImageUsage from len(Data)
// when OutputTokensDetails.NImages is not already populated.
func populateOutputImageCount(imageUsage *schemas.ImageUsage, dataLen int) {
	if imageUsage == nil || dataLen == 0 {
		return
	}
	if imageUsage.OutputTokensDetails == nil {
		imageUsage.OutputTokensDetails = &schemas.ImageTokenDetails{}
	}
	if imageUsage.OutputTokensDetails.NImages == 0 {
		imageUsage.OutputTokensDetails.NImages = dataLen
	}
}

// ---------------------------------------------------------------------------
// Pricing resolution
// ---------------------------------------------------------------------------

// resolvePricing resolves the pricing entry for a model, trying deployment as fallback.
func (mc *ModelCatalog) resolvePricing(provider, model, deployment string, requestType schemas.RequestType) *configstoreTables.TableModelPricing {
	mc.logger.Debug("looking up pricing for model %s and provider %s of request type %s", model, provider, normalizeRequestType(requestType))

	pricing, exists := mc.getPricing(model, provider, requestType)
	if exists {
		return pricing
	}

	if deployment != "" {
		mc.logger.Debug("pricing not found for model %s, trying deployment %s", model, deployment)
		pricing, exists = mc.getPricing(deployment, provider, requestType)
		if exists {
			return pricing
		}
	}

	mc.logger.Debug("pricing not found for model %s and provider %s, skipping cost calculation", model, provider)
	return nil
}

// getPricing returns pricing information for a model (thread-safe)
func (mc *ModelCatalog) getPricing(model, provider string, requestType schemas.RequestType) (*configstoreTables.TableModelPricing, bool) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	mode := normalizeRequestType(requestType)

	pricing, ok := mc.pricingData[makeKey(model, provider, mode)]
	if ok {
		return &pricing, true
	}

	// Lookup in vertex if gemini not found
	if provider == string(schemas.Gemini) {
		mc.logger.Debug("primary lookup failed, trying vertex provider for the same model")
		pricing, ok = mc.pricingData[makeKey(model, "vertex", mode)]
		if ok {
			return &pricing, true
		}

		// Lookup in chat if responses not found
		if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest {
			mc.logger.Debug("secondary lookup failed, trying vertex provider for the same model in chat completion")
			pricing, ok = mc.pricingData[makeKey(model, "vertex", normalizeRequestType(schemas.ChatCompletionRequest))]
			if ok {
				return &pricing, true
			}
		}
	}

	if provider == string(schemas.Vertex) {
		// Vertex models can be of the form "provider/model", so try to lookup the model without the provider prefix and keep the original provider
		if strings.Contains(model, "/") {
			modelWithoutProvider := strings.SplitN(model, "/", 2)[1]
			mc.logger.Debug("primary lookup failed, trying vertex provider for the same model with provider/model format %s", modelWithoutProvider)
			pricing, ok = mc.pricingData[makeKey(modelWithoutProvider, "vertex", mode)]
			if ok {
				return &pricing, true
			}

			// Lookup in chat if responses not found
			if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest {
				mc.logger.Debug("secondary lookup failed, trying vertex provider for the same model in chat completion")
				pricing, ok = mc.pricingData[makeKey(modelWithoutProvider, "vertex", normalizeRequestType(schemas.ChatCompletionRequest))]
				if ok {
					return &pricing, true
				}
			}
		}
	}

	if provider == string(schemas.Bedrock) {
		// If model is claude without "anthropic." prefix, try with "anthropic." prefix
		if !strings.Contains(model, "anthropic.") && schemas.IsAnthropicModel(model) {
			mc.logger.Debug("primary lookup failed, trying with anthropic. prefix for the same model")
			pricing, ok = mc.pricingData[makeKey("anthropic."+model, provider, mode)]
			if ok {
				return &pricing, true
			}

			// Lookup in chat if responses not found
			if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest {
				mc.logger.Debug("secondary lookup failed, trying chat provider for the same model in chat completion")
				pricing, ok = mc.pricingData[makeKey("anthropic."+model, provider, normalizeRequestType(schemas.ChatCompletionRequest))]
				if ok {
					return &pricing, true
				}
			}
		}
	}

	// Lookup in chat if responses not found
	if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest {
		mc.logger.Debug("primary lookup failed, trying chat provider for the same model in chat completion")
		pricing, ok = mc.pricingData[makeKey(model, provider, normalizeRequestType(schemas.ChatCompletionRequest))]
		if ok {
			return &pricing, true
		}
	}

	return nil, false
}
