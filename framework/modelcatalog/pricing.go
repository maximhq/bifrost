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
	videoSeconds      *int
}

// CalculateCost calculates the cost of a Bifrost response.
// It handles all request types, cache debug billing, and tiered pricing.
func (mc *ModelCatalog) CalculateCost(result *schemas.BifrostResponse) float64 {
	return mc.CalculateCostWithScopes(result, PricingLookupScopes{})
}

func (mc *ModelCatalog) CalculateCostWithScopes(result *schemas.BifrostResponse, scopes PricingLookupScopes) float64 {
	if result == nil {
		return 0
	}

	// Handle semantic cache billing
	cacheDebug := result.GetExtraFields().CacheDebug
	if cacheDebug != nil {
		return mc.calculateCostWithCache(result, cacheDebug, scopes)
	}

	return mc.calculateBaseCost(result, scopes)
}

// calculateCostWithCache handles cost calculation when semantic cache debug info is present.
func (mc *ModelCatalog) calculateCostWithCache(result *schemas.BifrostResponse, cacheDebug *schemas.BifrostCacheDebug, scopes PricingLookupScopes) float64 {
	if cacheDebug.CacheHit {
		// Direct cache hit — no LLM call, no cost
		if cacheDebug.HitType != nil && *cacheDebug.HitType == "direct" {
			return 0
		}
		// Semantic cache hit — only the embedding lookup cost
		if cacheDebug.ProviderUsed != nil && cacheDebug.ModelUsed != nil && cacheDebug.InputTokens != nil {
			return mc.computeCacheEmbeddingCost(cacheDebug, scopes)
		}
		return 0
	}

	// Cache miss — full LLM cost + embedding lookup cost
	baseCost := mc.calculateBaseCost(result, scopes)
	embeddingCost := mc.computeCacheEmbeddingCost(cacheDebug, scopes)
	return baseCost + embeddingCost
}

// computeCacheEmbeddingCost calculates the embedding cost for a semantic cache lookup.
func (mc *ModelCatalog) computeCacheEmbeddingCost(cacheDebug *schemas.BifrostCacheDebug, scopes PricingLookupScopes) float64 {
	if cacheDebug == nil || cacheDebug.ProviderUsed == nil || cacheDebug.ModelUsed == nil || cacheDebug.InputTokens == nil {
		return 0
	}
	if scopes.ProviderID == "" {
		scopes.ProviderID = *cacheDebug.ProviderUsed
	}
	pricing, exists := mc.getPricingWithScopes(*cacheDebug.ModelUsed, *cacheDebug.ProviderUsed, schemas.EmbeddingRequest, scopes)
	if !exists {
		return 0
	}
	return float64(*cacheDebug.InputTokens) * pricing.InputCostPerToken
}

// calculateBaseCost extracts usage from the response and routes to the appropriate compute function.
func (mc *ModelCatalog) calculateBaseCost(result *schemas.BifrostResponse, scopes PricingLookupScopes) float64 {
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
	pricing := mc.resolvePricing(provider, model, deployment, requestType, scopes)
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
		return computeImageCost(pricing, input.imageUsage)
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

	case result.ImageGenerationResponse != nil && result.ImageGenerationResponse.Usage != nil:
		input.imageUsage = result.ImageGenerationResponse.Usage

	case result.ImageGenerationStreamResponse != nil && result.ImageGenerationStreamResponse.Usage != nil:
		input.imageUsage = result.ImageGenerationStreamResponse.Usage

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
			CachedTokens: u.InputTokensDetails.CachedTokens,
			TextTokens:   u.InputTokensDetails.TextTokens,
			AudioTokens:  u.InputTokensDetails.AudioTokens,
			ImageTokens:  u.InputTokensDetails.ImageTokens,
		}
	}
	if u.OutputTokensDetails != nil {
		usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{
			ReasoningTokens: u.OutputTokensDetails.ReasoningTokens,
			CachedTokens:    u.OutputTokensDetails.CachedTokens,
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
	cachedPromptTokens := 0
	if usage.PromptTokensDetails != nil {
		cachedPromptTokens = usage.PromptTokensDetails.CachedTokens
	}
	cachedCompletionTokens := 0
	if usage.CompletionTokensDetails != nil {
		cachedCompletionTokens = usage.CompletionTokensDetails.CachedTokens
	}

	inputRate := tieredInputRate(pricing, totalTokens)
	outputRate := tieredOutputRate(pricing, totalTokens)

	// Input cost: non-cached tokens at regular rate
	nonCachedPrompt := promptTokens - cachedPromptTokens
	inputCost := float64(nonCachedPrompt) * inputRate

	// Add cached prompt tokens at cache read rate
	if cachedPromptTokens > 0 && pricing.CacheReadInputTokenCost != nil {
		inputCost += float64(cachedPromptTokens) * *pricing.CacheReadInputTokenCost
	}

	// Output cost: non-cached completion tokens at regular rate
	nonCachedCompletion := completionTokens - cachedCompletionTokens
	outputCost := float64(nonCachedCompletion) * outputRate

	// Add cached completion tokens at cache creation rate
	if cachedCompletionTokens > 0 && pricing.CacheCreationInputTokenCost != nil {
		outputCost += float64(cachedCompletionTokens) * *pricing.CacheCreationInputTokenCost
	}

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
// Prefers duration-based pricing, falls back to token-based.
func computeSpeechCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int) float64 {
	// Duration-based pricing
	if audioSeconds != nil && *audioSeconds > 0 {
		var rate *float64
		if pricing.InputCostPerAudioPerSecond != nil {
			rate = pricing.InputCostPerAudioPerSecond
		} else if pricing.InputCostPerSecond != nil {
			rate = pricing.InputCostPerSecond
		}
		if rate != nil {
			inputCost := float64(*audioSeconds) * *rate
			outputCost := 0.0
			if usage != nil {
				outputCost = float64(usage.CompletionTokens) * pricing.OutputCostPerToken
			}
			return inputCost + outputCost
		}
	}

	// Token-based fallback
	if usage == nil {
		return 0
	}
	inputRate := tieredInputRate(pricing, usage.TotalTokens)
	outputRate := tieredOutputRate(pricing, usage.TotalTokens)
	return float64(usage.PromptTokens)*inputRate + float64(usage.CompletionTokens)*outputRate
}

// computeTranscriptionCost handles transcription (STT) requests.
// Prefers duration-based pricing, then audio token details, falls back to generic tokens.
func computeTranscriptionCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails) float64 {
	// Duration-based pricing
	if audioSeconds != nil && *audioSeconds > 0 {
		var rate *float64
		if pricing.InputCostPerAudioPerSecond != nil {
			rate = pricing.InputCostPerAudioPerSecond
		} else if pricing.InputCostPerSecond != nil {
			rate = pricing.InputCostPerSecond
		}
		if rate != nil {
			inputCost := float64(*audioSeconds) * *rate
			outputCost := 0.0
			if usage != nil {
				outputCost = float64(usage.CompletionTokens) * pricing.OutputCostPerToken
			}
			return inputCost + outputCost
		}
	}

	// Audio token detail pricing (audio + text token breakdown)
	if audioTokenDetails != nil {
		inputRate := tieredInputRate(pricing, safeTotalTokens(usage))
		// Use audio-specific token rate if available
		audioRate := inputRate
		if pricing.InputCostPerAudioToken != nil {
			audioRate = *pricing.InputCostPerAudioToken
		}
		inputCost := float64(audioTokenDetails.AudioTokens)*audioRate + float64(audioTokenDetails.TextTokens)*inputRate
		outputCost := 0.0
		if usage != nil {
			outputCost = float64(usage.CompletionTokens) * tieredOutputRate(pricing, usage.TotalTokens)
		}
		return inputCost + outputCost
	}

	// Generic token-based fallback
	if usage == nil {
		return 0
	}
	inputRate := tieredInputRate(pricing, usage.TotalTokens)
	outputRate := tieredOutputRate(pricing, usage.TotalTokens)
	return float64(usage.PromptTokens)*inputRate + float64(usage.CompletionTokens)*outputRate
}

// computeImageCost handles image generation requests.
// Uses per-image pricing when token counts are zero, otherwise token-based.
func computeImageCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage) float64 {
	if imageUsage == nil {
		return 0
	}

	// Per-image pricing when tokens are zero
	if imageUsage.TotalTokens == 0 && imageUsage.InputTokens == 0 && imageUsage.OutputTokens == 0 {
		numImages := 1
		if imageUsage.OutputTokensDetails != nil && imageUsage.OutputTokensDetails.NImages > 0 {
			numImages = imageUsage.OutputTokensDetails.NImages
		} else if imageUsage.InputTokensDetails != nil && imageUsage.InputTokensDetails.NImages > 0 {
			numImages = imageUsage.InputTokensDetails.NImages
		}

		inputCost := 0.0
		if pricing.InputCostPerImage != nil {
			inputCost = float64(numImages) * *pricing.InputCostPerImage
		}
		outputCost := 0.0
		if pricing.OutputCostPerImage != nil {
			outputCost = float64(numImages) * *pricing.OutputCostPerImage
		}

		if pricing.InputCostPerImage != nil || pricing.OutputCostPerImage != nil {
			return inputCost + outputCost
		}
		// Fall through to token-based if per-image pricing not available
	}

	// Token-based pricing with text/image breakdown
	var inputTextTokens, outputTextTokens int
	var inputImageTokens, outputImageTokens int

	if imageUsage.InputTokensDetails != nil {
		inputImageTokens = imageUsage.InputTokensDetails.ImageTokens
		inputTextTokens = imageUsage.InputTokensDetails.TextTokens
	} else {
		inputTextTokens = imageUsage.InputTokens
	}

	if imageUsage.OutputTokensDetails != nil {
		outputImageTokens = imageUsage.OutputTokensDetails.ImageTokens
		outputTextTokens = imageUsage.OutputTokensDetails.TextTokens
	} else {
		outputImageTokens = imageUsage.OutputTokens
	}

	// Text token rates (tiered)
	totalTokens := imageUsage.TotalTokens
	inputTokenRate := tieredInputRate(pricing, totalTokens)
	outputTokenRate := tieredOutputRate(pricing, totalTokens)

	inputCost := float64(inputTextTokens)*inputTokenRate + float64(inputImageTokens)*inputTokenRate
	outputCost := float64(outputTextTokens)*outputTokenRate + float64(outputImageTokens)*outputTokenRate

	return inputCost + outputCost
}

// computeVideoCost handles video generation requests.
// Uses duration-based output pricing with optional token-based input.
func computeVideoCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, videoSeconds *int) float64 {
	outputCost := 0.0
	if videoSeconds != nil && *videoSeconds > 0 {
		if pricing.OutputCostPerVideoPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerVideoPerSecond
		} else if pricing.OutputCostPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerSecond
		}
	}

	inputCost := 0.0
	if usage != nil && usage.PromptTokens > 0 {
		inputCost = float64(usage.PromptTokens) * pricing.InputCostPerToken
	}

	return inputCost + outputCost
}

// ---------------------------------------------------------------------------
// Tiered rate helpers
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

func safeTotalTokens(usage *schemas.BifrostLLMUsage) int {
	if usage == nil {
		return 0
	}
	return usage.TotalTokens
}

// ---------------------------------------------------------------------------
// Pricing resolution
// ---------------------------------------------------------------------------

// resolvePricing resolves the pricing entry for a model, trying deployment as fallback.
func (mc *ModelCatalog) resolvePricing(provider, model, deployment string, requestType schemas.RequestType, scopes PricingLookupScopes) *configstoreTables.TableModelPricing {
	mc.logger.Debug("looking up pricing for model %s and provider %s of request type %s", model, provider, normalizeRequestType(requestType))

	if scopes.ProviderID == "" {
		scopes.ProviderID = provider
	}

	pricing, exists := mc.getPricingWithScopes(model, provider, requestType, scopes)
	if exists {
		return pricing
	}

	if deployment != "" {
		mc.logger.Debug("pricing not found for model %s, trying deployment %s", model, deployment)
		pricing, exists = mc.getPricingWithScopesAndMatchModel(deployment, model, provider, requestType, scopes)
		if exists {
			return pricing
		}
	}

	mc.logger.Debug("pricing not found for model %s and provider %s, skipping cost calculation", model, provider)
	return nil
}

// getPricing returns pricing information for a model (thread-safe)
func (mc *ModelCatalog) getPricing(model, provider string, requestType schemas.RequestType) (*configstoreTables.TableModelPricing, bool) {
	return mc.getPricingWithScopes(model, provider, requestType, PricingLookupScopes{ProviderID: provider})
}

func (mc *ModelCatalog) getPricingWithScopes(model, provider string, requestType schemas.RequestType, scopes PricingLookupScopes) (*configstoreTables.TableModelPricing, bool) {
	return mc.getPricingWithScopesAndMatchModel(model, model, provider, requestType, scopes)
}

func (mc *ModelCatalog) getPricingWithScopesAndMatchModel(lookupModel, matchModel, provider string, requestType schemas.RequestType, scopes PricingLookupScopes) (*configstoreTables.TableModelPricing, bool) {
	mc.mu.RLock()
	pricing, ok := mc.resolvePricingEntryLocked(lookupModel, matchModel, provider, requestType, scopes)
	mc.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return &pricing, true
}

// resolvePricingEntryLocked resolves pricing data including scoped overrides.
// Caller must hold mc.mu read lock.
func (mc *ModelCatalog) resolvePricingEntryLocked(lookupModel, matchModel, provider string, requestType schemas.RequestType, scopes PricingLookupScopes) (configstoreTables.TableModelPricing, bool) {
	pricing, ok := mc.resolveBasePricingEntryLocked(lookupModel, provider, requestType)
	if !ok {
		return configstoreTables.TableModelPricing{}, false
	}
	return mc.applyScopedPricingOverrides(matchModel, requestType, pricing, scopes), true
}

// resolveBasePricingEntryLocked resolves pricing data from the base catalog including all fallback logic.
// Caller must hold mc.mu read lock.
func (mc *ModelCatalog) resolveBasePricingEntryLocked(model, provider string, requestType schemas.RequestType) (configstoreTables.TableModelPricing, bool) {
	mode := normalizeRequestType(requestType)

	pricing, ok := mc.pricingData[makeKey(model, provider, mode)]
	if ok {
		return pricing, true
	}

	// Lookup in vertex if gemini not found
	if provider == string(schemas.Gemini) {
		mc.logger.Debug("primary lookup failed, trying vertex provider for the same model")
		pricing, ok = mc.pricingData[makeKey(model, "vertex", mode)]
		if ok {
			return pricing, true
		}

		// Lookup in chat if responses not found
		if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest {
			mc.logger.Debug("secondary lookup failed, trying vertex provider for the same model in chat completion")
			pricing, ok = mc.pricingData[makeKey(model, "vertex", normalizeRequestType(schemas.ChatCompletionRequest))]
			if ok {
				return pricing, true
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
				return pricing, true
			}

			// Lookup in chat if responses not found
			if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest {
				mc.logger.Debug("secondary lookup failed, trying vertex provider for the same model in chat completion")
				pricing, ok = mc.pricingData[makeKey(modelWithoutProvider, "vertex", normalizeRequestType(schemas.ChatCompletionRequest))]
				if ok {
					return pricing, true
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
				return pricing, true
			}

			// Lookup in chat if responses not found
			if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest {
				mc.logger.Debug("secondary lookup failed, trying chat provider for the same model in chat completion")
				pricing, ok = mc.pricingData[makeKey("anthropic."+model, provider, normalizeRequestType(schemas.ChatCompletionRequest))]
				if ok {
					return pricing, true
				}
			}
		}
	}

	// Lookup in chat if responses not found
	if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest {
		mc.logger.Debug("primary lookup failed, trying chat provider for the same model in chat completion")
		pricing, ok = mc.pricingData[makeKey(model, provider, normalizeRequestType(schemas.ChatCompletionRequest))]
		if ok {
			return pricing, true
		}
	}

	return configstoreTables.TableModelPricing{}, false
}
