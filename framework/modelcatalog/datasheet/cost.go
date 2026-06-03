package datasheet

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// CalculateCost computes the dollar cost for a Bifrost response. Handles all
// request types, cache-debug billing, and tiered pricing. If scopes is nil,
// an empty LookupScopes is used; global and provider-scoped overrides may
// still apply since the provider is derived from the response.
func (s *Store) CalculateCost(result *schemas.BifrostResponse, scopes *LookupScopes) float64 {
	if result == nil {
		return 0
	}

	var lookupScopes LookupScopes
	if scopes != nil {
		lookupScopes = *scopes
	}

	cacheDebug := result.GetExtraFields().CacheDebug
	if cacheDebug != nil {
		return s.calculateCostWithCache(result, cacheDebug, lookupScopes)
	}
	return s.calculateBaseCost(result, lookupScopes)
}

func (s *Store) calculateCostWithCache(result *schemas.BifrostResponse, cacheDebug *schemas.BifrostCacheDebug, scopes LookupScopes) float64 {
	if cacheDebug.CacheHit {
		// Direct cache hit — no LLM call, no cost
		if cacheDebug.HitType != nil && *cacheDebug.HitType == "direct" {
			return 0
		}
		// Semantic cache hit — only the embedding lookup cost
		if cacheDebug.ProviderUsed != nil && cacheDebug.ModelUsed != nil && cacheDebug.InputTokens != nil {
			return s.computeCacheEmbeddingCost(cacheDebug, scopes)
		}
		return 0
	}
	// Cache miss — full LLM cost + embedding lookup cost
	baseCost := s.calculateBaseCost(result, scopes)
	embeddingCost := s.computeCacheEmbeddingCost(cacheDebug, scopes)
	return baseCost + embeddingCost
}

func (s *Store) computeCacheEmbeddingCost(cacheDebug *schemas.BifrostCacheDebug, scopes LookupScopes) float64 {
	if cacheDebug == nil || cacheDebug.ProviderUsed == nil || cacheDebug.ModelUsed == nil || cacheDebug.InputTokens == nil {
		return 0
	}
	if scopes.Provider == "" {
		scopes.Provider = *cacheDebug.ProviderUsed
	}
	// Cache-debug pricing only carries a single model identifier (whatever the
	// cache recorded). Maps to RoutingInfo.Model — no alias resolution context
	// exists for the cache-replayed request.
	pricing := s.resolvePricing(schemas.RoutingInfo{
		Provider: schemas.ModelProvider(*cacheDebug.ProviderUsed),
		Model:    *cacheDebug.ModelUsed,
	}, schemas.EmbeddingRequest, scopes)
	if pricing == nil {
		return 0
	}
	return float64(*cacheDebug.InputTokens) * tieredInputRate(pricing, *cacheDebug.InputTokens, serviceTier{})
}

func computeContainerCreationCost(pricing *configstoreTables.TableModelPricing) float64 {
	if pricing == nil || pricing.CodeInterpreterCostPerSession == nil {
		return 0
	}
	return *pricing.CodeInterpreterCostPerSession
}

func (s *Store) calculateBaseCost(result *schemas.BifrostResponse, scopes LookupScopes) float64 {
	extraFields := result.GetExtraFields()
	if extraFields == nil {
		return 0
	}

	// Backward-compat fallback: when the caller (e.g. LoggerPlugin's
	// RecalculateCosts replaying logs written before RoutingInfo existed, or
	// third-party plugins still on the legacy ExtraFields shape) leaves
	// RoutingInfo empty, synthesise one from the deprecated triplet so
	// pricing keeps working. Triggered only when RoutingInfo is fully
	// unset — partial population is trusted as-is.
	routingInfo := extraFields.RoutingInfo
	if routingInfo.Provider == "" && routingInfo.Model == "" && routingInfo.ResolvedKeyAlias == nil {
		routingInfo.Provider = extraFields.Provider
		routingInfo.Model = extraFields.OriginalModelRequested
		if r := extraFields.ResolvedModelUsed; r != "" && r != extraFields.OriginalModelRequested {
			routingInfo.ResolvedKeyAlias = &schemas.ResolvedKeyAlias{ModelID: r}
		}
	}
	requestType := extraFields.RequestType

	input := extractCostInput(result)

	// Provider-computed cost wins when present.
	if input.usage != nil && input.usage.Cost != nil && input.usage.Cost.TotalCost > 0 {
		return input.usage.Cost.TotalCost
	}

	// Nothing to price.
	if input.usage == nil && input.audioSeconds == nil && input.audioTokenDetails == nil && input.imageUsage == nil && input.videoSeconds == nil && input.audioTextInputChars == 0 && input.ocrProcessedPages == nil && input.containerIdentifierString == "" {
		return 0
	}

	if result.PassthroughResponse != nil {
		requestType = inferPassthroughRequestType(routingInfo.Provider, extraFields.PassthroughPath, result.PassthroughResponse.PassthroughUsage)
	} else {
		requestType = normalizeStreamRequestType(requestType)
	}

	// When a pricing model override is set (e.g. container creates always look
	// up "container"), it replaces the lookup hierarchy entirely. Build a
	// synthetic RoutingInfo that reuses Provider but pins the model fields to
	// the container identifier so per-container overrides stay addressable.
	if input.containerIdentifierString != "" {
		routingInfo = schemas.RoutingInfo{
			Provider: routingInfo.Provider,
			Model:    input.containerIdentifierString,
		}
	}

	pricing := s.resolvePricing(routingInfo, requestType, scopes)
	if pricing == nil {
		return 0
	}

	switch requestType {
	case schemas.ChatCompletionRequest, schemas.TextCompletionRequest, schemas.ResponsesRequest, schemas.RealtimeRequest:
		return computeTextCost(pricing, input.usage, input.tier)
	case schemas.EmbeddingRequest:
		return computeEmbeddingCost(pricing, input.usage, input.tier)
	case schemas.RerankRequest:
		return computeRerankCost(pricing, input.usage, input.tier)
	case schemas.SpeechRequest:
		return computeSpeechCost(pricing, input.usage, input.audioSeconds, input.audioTextInputChars, input.tier)
	case schemas.TranscriptionRequest:
		return computeTranscriptionCost(pricing, input.usage, input.audioSeconds, input.audioTokenDetails, input.tier)
	case schemas.ImageGenerationRequest, schemas.ImageEditRequest, schemas.ImageVariationRequest:
		return computeImageCost(pricing, input.imageUsage, input.imageSize, input.imageQuality, input.tier)
	case schemas.VideoGenerationRequest, schemas.VideoRemixRequest:
		return computeVideoCost(pricing, input.usage, input.videoSeconds, input.tier)
	case schemas.OCRRequest:
		return computeOCRCost(pricing, input.ocrProcessedPages, input.ocrIsAnnotated)
	case schemas.ContainerCreateRequest:
		return computeContainerCreationCost(pricing)
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
	case result.PassthroughResponse != nil && result.PassthroughResponse.PassthroughUsage != nil:
		return passthroughUsageToCostInput(result.PassthroughResponse.PassthroughUsage)

	case result.TextCompletionResponse != nil && result.TextCompletionResponse.Usage != nil:
		input.usage = result.TextCompletionResponse.Usage

	case result.ChatResponse != nil && result.ChatResponse.Usage != nil:
		input.usage = result.ChatResponse.Usage
		input.tier = tierFromString(result.ChatResponse.ServiceTier)

	case result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil:
		input.usage = responsesUsageToBifrostUsage(result.ResponsesResponse.Usage)
		input.tier = tierFromString(result.ResponsesResponse.ServiceTier)

	case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil && result.ResponsesStreamResponse.Response.Usage != nil:
		input.usage = responsesUsageToBifrostUsage(result.ResponsesStreamResponse.Response.Usage)
		input.tier = tierFromString(result.ResponsesStreamResponse.Response.ServiceTier)

	case result.EmbeddingResponse != nil && result.EmbeddingResponse.Usage != nil:
		input.usage = result.EmbeddingResponse.Usage

	case result.RerankResponse != nil && result.RerankResponse.Usage != nil:
		input.usage = result.RerankResponse.Usage

	case result.SpeechResponse != nil && result.SpeechResponse.Usage != nil:
		input.usage = speechUsageToBifrostUsage(result.SpeechResponse.Usage)
		input.audioTextInputChars = result.SpeechResponse.Usage.InputChars

	case result.SpeechStreamResponse != nil && result.SpeechStreamResponse.Usage != nil:
		input.usage = speechUsageToBifrostUsage(result.SpeechStreamResponse.Usage)
		input.audioTextInputChars = result.SpeechStreamResponse.Usage.InputChars

	case result.TranscriptionResponse != nil && result.TranscriptionResponse.Usage != nil:
		input.usage, input.audioSeconds, input.audioTokenDetails = extractTranscriptionUsage(result.TranscriptionResponse.Usage)

	case result.TranscriptionStreamResponse != nil && result.TranscriptionStreamResponse.Usage != nil:
		input.usage, input.audioSeconds, input.audioTokenDetails = extractTranscriptionUsage(result.TranscriptionStreamResponse.Usage)

	case result.ImageGenerationResponse != nil:
		if result.ImageGenerationResponse.Usage != nil {
			input.imageUsage = result.ImageGenerationResponse.Usage
		} else {
			input.imageUsage = &schemas.ImageUsage{}
		}
		populateOutputImageCount(input.imageUsage, len(result.ImageGenerationResponse.Data))
		if result.ImageGenerationResponse.ImageGenerationResponseParameters != nil {
			input.imageSize = result.ImageGenerationResponse.ImageGenerationResponseParameters.Size
			input.imageQuality = result.ImageGenerationResponse.ImageGenerationResponseParameters.Quality
		}

	case result.ImageGenerationStreamResponse != nil:
		if result.ImageGenerationStreamResponse.Usage != nil {
			input.imageUsage = result.ImageGenerationStreamResponse.Usage
		} else {
			input.imageUsage = &schemas.ImageUsage{}
		}
		input.imageSize = result.ImageGenerationStreamResponse.Size
		input.imageQuality = result.ImageGenerationStreamResponse.Quality

	case result.VideoGenerationResponse != nil && result.VideoGenerationResponse.Seconds != nil:
		seconds, err := strconv.Atoi(*result.VideoGenerationResponse.Seconds)
		if err == nil {
			input.videoSeconds = &seconds
		}

	case result.OCRResponse != nil:
		pages := len(result.OCRResponse.Pages)
		if result.OCRResponse.UsageInfo != nil && result.OCRResponse.UsageInfo.PagesProcessed > 0 {
			pages = result.OCRResponse.UsageInfo.PagesProcessed
		}
		input.ocrProcessedPages = &pages
		isAnnotated := result.OCRResponse.DocumentAnnotation != nil && *result.OCRResponse.DocumentAnnotation != ""
		input.ocrIsAnnotated = &isAnnotated

	case result.ContainerCreateResponse != nil:
		if memLimit := result.ContainerCreateResponse.MemoryLimit; memLimit != "" {
			input.containerIdentifierString = "container-" + memLimit
		} else {
			input.containerIdentifierString = "container"
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
	if u.InputTokensDetails != nil {
		usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			TextTokens:              u.InputTokensDetails.TextTokens,
			AudioTokens:             u.InputTokensDetails.AudioTokens,
			ImageTokens:             u.InputTokensDetails.ImageTokens,
			CachedReadTokens:        u.InputTokensDetails.CachedReadTokens,
			CachedWriteTokens:       u.InputTokensDetails.CachedWriteTokens,
			CachedWriteTokenDetails: u.InputTokensDetails.CachedWriteTokenDetails,
		}
	}
	if u.OutputTokensDetails != nil {
		usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{
			ReasoningTokens: u.OutputTokensDetails.ReasoningTokens,
			AudioTokens:     u.OutputTokensDetails.AudioTokens,
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

func computeTextCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) float64 {
	if usage == nil {
		return 0
	}

	totalTokens := usage.TotalTokens
	promptTokens := usage.PromptTokens
	completionTokens := usage.CompletionTokens

	cachedReadTokens := 0
	cachedWriteTokens := 0
	cachedWriteTokensAbove1hr := 0
	if usage.PromptTokensDetails != nil {
		cachedReadTokens = usage.PromptTokensDetails.CachedReadTokens
		cachedWriteTokens = usage.PromptTokensDetails.CachedWriteTokens
		if usage.PromptTokensDetails.CachedWriteTokenDetails != nil {
			cachedWriteTokensAbove1hr = usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h
		}
	}

	inputRate := tieredInputRate(pricing, totalTokens, tier)
	outputRate := tieredOutputRate(pricing, totalTokens, tier)
	cacheReadInputRate := tieredCacheReadInputTokenRate(pricing, totalTokens, tier)
	cacheCreationInputRate := tieredCacheCreationInputTokenRate(pricing, totalTokens, tier)
	cacheCreationInputAbove1hrInputRate := tieredCacheCreationInputAbove1hrTokenRate(pricing, totalTokens, tier)

	// Clamp cached token counts to avoid negative billing on malformed provider payloads.
	if cachedReadTokens > promptTokens {
		cachedReadTokens = promptTokens
	}
	if cachedWriteTokens > promptTokens-cachedReadTokens {
		cachedWriteTokens = promptTokens - cachedReadTokens
	}
	if cachedWriteTokensAbove1hr > cachedWriteTokens {
		cachedWriteTokensAbove1hr = cachedWriteTokens
	}

	nonCachedPrompt := promptTokens - cachedReadTokens - cachedWriteTokens
	inputCost := float64(nonCachedPrompt) * inputRate
	if cachedReadTokens > 0 {
		inputCost += float64(cachedReadTokens) * cacheReadInputRate
	}
	if cachedWriteTokens > 0 {
		if cachedWriteTokensAbove1hr > 0 {
			inputCost += float64(cachedWriteTokensAbove1hr) * cacheCreationInputAbove1hrInputRate
		}
		inputCost += float64(cachedWriteTokens-cachedWriteTokensAbove1hr) * cacheCreationInputRate
	}

	outputCost := float64(completionTokens) * outputRate

	// Audio token cost: when token details include audio tokens, price them at
	// the dedicated audio rate and subtract from the text token costs above.
	audioCost := 0.0
	inputAudioTokens := 0
	outputAudioTokens := 0
	if usage.PromptTokensDetails != nil {
		inputAudioTokens = usage.PromptTokensDetails.AudioTokens
	}
	if usage.CompletionTokensDetails != nil {
		outputAudioTokens = usage.CompletionTokensDetails.AudioTokens
	}
	if inputAudioTokens < 0 {
		inputAudioTokens = 0
	} else if inputAudioTokens > promptTokens {
		inputAudioTokens = promptTokens
	}
	if outputAudioTokens < 0 {
		outputAudioTokens = 0
	} else if outputAudioTokens > completionTokens {
		outputAudioTokens = completionTokens
	}
	if inputAudioTokens > 0 && pricing.InputCostPerAudioToken != nil {
		audioCost += float64(inputAudioTokens) * (*pricing.InputCostPerAudioToken - inputRate)
	}
	if outputAudioTokens > 0 && pricing.OutputCostPerAudioToken != nil {
		audioCost += float64(outputAudioTokens) * (*pricing.OutputCostPerAudioToken - outputRate)
	}

	searchCost := 0.0
	if pricing.SearchContextCostPerQuery != nil && usage.CompletionTokensDetails != nil && usage.CompletionTokensDetails.NumSearchQueries != nil {
		searchCost = float64(*usage.CompletionTokensDetails.NumSearchQueries) * *pricing.SearchContextCostPerQuery
	}

	return inputCost + outputCost + audioCost + searchCost
}

func computeEmbeddingCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) float64 {
	if usage == nil {
		return 0
	}
	return float64(usage.PromptTokens) * tieredInputRate(pricing, usage.TotalTokens, tier)
}

func computeRerankCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) float64 {
	if usage == nil {
		return 0
	}
	inputCost := float64(usage.PromptTokens) * tieredInputRate(pricing, usage.TotalTokens, tier)
	outputCost := float64(usage.CompletionTokens) * tieredOutputRate(pricing, usage.TotalTokens, tier)
	searchCost := 0.0
	if pricing.SearchContextCostPerQuery != nil && usage.CompletionTokensDetails != nil && usage.CompletionTokensDetails.NumSearchQueries != nil {
		searchCost = float64(*usage.CompletionTokensDetails.NumSearchQueries) * *pricing.SearchContextCostPerQuery
	}
	return inputCost + outputCost + searchCost
}

// computeSpeechCost handles speech (TTS) requests. Per-character pricing
// (InputCostPerCharacter) is first-class — providers like OpenAI TTS,
// ElevenLabs, and AWS Polly bill per character of input text. PromptTokens
// is treated as the character count since TTS providers report their
// billable unit in that field. Output falls back to per-second duration
// when no audio token rate is configured.
func computeSpeechCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTextInputChars int, tier serviceTier) float64 {
	totalTokens := safeTotalTokens(usage)
	inputCost := 0.0
	if audioTextInputChars > 0 {
		if pricing.InputCostPerCharacter != nil {
			inputCost = float64(audioTextInputChars) * *pricing.InputCostPerCharacter
		} else {
			inputCost = float64(audioTextInputChars) * tieredInputRate(pricing, totalTokens, tier)
		}
	} else if usage != nil && usage.PromptTokens > 0 {
		inputCost = float64(usage.PromptTokens) * tieredInputRate(pricing, totalTokens, tier)
	}
	outputCost := computeAudioOutputCost(pricing, usage, audioSeconds, totalTokens, tier)
	return inputCost + outputCost
}

func computeTranscriptionCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails, tier serviceTier) float64 {
	totalTokens := safeTotalTokens(usage)
	inputCost := computeAudioInputCost(pricing, usage, audioSeconds, audioTokenDetails, totalTokens, tier)
	outputCost := 0.0
	if usage != nil && usage.CompletionTokens > 0 {
		outputCost = float64(usage.CompletionTokens) * tieredOutputRate(pricing, totalTokens, tier)
	}
	return inputCost + outputCost
}

func computeAudioInputCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails, totalTokens int, tier serviceTier) float64 {
	if audioTokenDetails != nil && (audioTokenDetails.AudioTokens > 0 || audioTokenDetails.TextTokens > 0) {
		return float64(audioTokenDetails.AudioTokens)*tieredAudioTokenInputRate(pricing, totalTokens, tier) +
			float64(audioTokenDetails.TextTokens)*tieredInputRate(pricing, totalTokens, tier)
	}
	if usage != nil && usage.PromptTokens > 0 {
		return float64(usage.PromptTokens) * tieredInputRate(pricing, totalTokens, tier)
	}
	if audioSeconds != nil && *audioSeconds > 0 {
		if rate := tieredAudioInputPerSecondRate(pricing, totalTokens); rate > 0 {
			return float64(*audioSeconds) * rate
		}
	}
	return 0
}

func computeAudioOutputCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, totalTokens int, tier serviceTier) float64 {
	if usage != nil && usage.CompletionTokens > 0 {
		return float64(usage.CompletionTokens) * tieredAudioTokenOutputRate(pricing, totalTokens, tier)
	}
	if audioSeconds != nil && *audioSeconds > 0 {
		if pricing.OutputCostPerSecond != nil {
			return float64(*audioSeconds) * *pricing.OutputCostPerSecond
		}
	}
	return 0
}

// computeImageCost handles image generation. Input and output are independent —
// each tries token-based pricing first, then per-pixel, then per-image fallback.
// imageQuality must be "low"/"medium"/"high"/"auto" to use quality-specific rates.
func computeImageCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, imageSize string, imageQuality string, tier serviceTier) float64 {
	if imageUsage == nil {
		return 0
	}
	totalTokens := imageUsage.TotalTokens
	pixels := parseImagePixels(imageSize)
	inputCost := computeImageInputCost(pricing, imageUsage, totalTokens, pixels, tier)
	outputCost := computeImageOutputCost(pricing, imageUsage, totalTokens, pixels, imageQuality, tier)
	return inputCost + outputCost
}

func computeImageInputCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, totalTokens int, pixels int, tier serviceTier) float64 {
	var inputTextTokens, inputImageTokens int
	if imageUsage.InputTokensDetails != nil {
		inputImageTokens = imageUsage.InputTokensDetails.ImageTokens
		inputTextTokens = imageUsage.InputTokensDetails.TextTokens
	} else {
		inputTextTokens = imageUsage.InputTokens
	}
	if inputTextTokens > 0 || inputImageTokens > 0 {
		return float64(inputTextTokens)*tieredInputRate(pricing, totalTokens, tier) +
			float64(inputImageTokens)*tieredImageInputRate(pricing, totalTokens, tier)
	}
	if pricing.InputCostPerPixel != nil && pixels > 0 && imageUsage.NumInputImages > 0 {
		return float64(pixels*imageUsage.NumInputImages) * *pricing.InputCostPerPixel
	}
	if pricing.InputCostPerImage != nil && imageUsage.NumInputImages > 0 {
		return float64(imageUsage.NumInputImages) * *pricing.InputCostPerImage
	}
	return 0
}

func computeImageOutputCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, totalTokens int, pixels int, imageQuality string, tier serviceTier) float64 {
	var outputTextTokens, outputImageTokens int
	if imageUsage.OutputTokensDetails != nil {
		outputImageTokens = imageUsage.OutputTokensDetails.ImageTokens
		outputTextTokens = imageUsage.OutputTokensDetails.TextTokens
	} else {
		outputImageTokens = imageUsage.OutputTokens
	}
	if outputTextTokens > 0 || outputImageTokens > 0 {
		return float64(outputTextTokens)*tieredOutputRate(pricing, totalTokens, tier) +
			float64(outputImageTokens)*tieredImageOutputRate(pricing, totalTokens, tier)
	}
	if pricing.OutputCostPerPixel != nil && pixels > 0 {
		numOutputImages := 1
		if imageUsage.OutputTokensDetails != nil && imageUsage.OutputTokensDetails.NImages > 0 {
			numOutputImages = imageUsage.OutputTokensDetails.NImages
		}
		return float64(pixels*numOutputImages) * *pricing.OutputCostPerPixel
	}

	numOutputImages := 1
	if imageUsage.OutputTokensDetails != nil && imageUsage.OutputTokensDetails.NImages > 0 {
		numOutputImages = imageUsage.OutputTokensDetails.NImages
	}
	var perImageRate *float64
	q := imageQuality
	if q == "" {
		q = "auto"
	}
	switch q {
	case "low":
		if pricing.OutputCostPerImageLowQuality != nil {
			perImageRate = pricing.OutputCostPerImageLowQuality
		}
	case "medium":
		if pricing.OutputCostPerImageMediumQuality != nil {
			perImageRate = pricing.OutputCostPerImageMediumQuality
		}
	case "high":
		if pricing.OutputCostPerImageHighQuality != nil {
			perImageRate = pricing.OutputCostPerImageHighQuality
		}
	case "auto":
		if pricing.OutputCostPerImageAutoQuality != nil {
			perImageRate = pricing.OutputCostPerImageAutoQuality
		}
	}
	if perImageRate == nil {
		const pixels512x512 = 512 * 512
		const pixels1024x1024 = 1024 * 1024
		const pixels2048x2048 = 2048 * 2048
		const pixels4096x4096 = 4096 * 4096
		switch {
		case pixels >= pixels4096x4096 && pricing.OutputCostPerImageAbove4096x4096Pixels != nil:
			perImageRate = pricing.OutputCostPerImageAbove4096x4096Pixels
		case pixels >= pixels2048x2048 && pricing.OutputCostPerImageAbove2048x2048Pixels != nil:
			perImageRate = pricing.OutputCostPerImageAbove2048x2048Pixels
		case pixels >= pixels1024x1024 && pricing.OutputCostPerImageAbove1024x1024Pixels != nil:
			perImageRate = pricing.OutputCostPerImageAbove1024x1024Pixels
		case pixels >= pixels512x512 && pricing.OutputCostPerImageAbove512x512Pixels != nil:
			perImageRate = pricing.OutputCostPerImageAbove512x512Pixels
		default:
			perImageRate = pricing.OutputCostPerImage
		}
	}
	if perImageRate != nil {
		return float64(numOutputImages) * *perImageRate
	}
	return 0
}

func computeVideoCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, videoSeconds *int, tier serviceTier) float64 {
	totalTokens := safeTotalTokens(usage)

	inputCost := 0.0
	if usage != nil && usage.PromptTokens > 0 {
		inputCost = float64(usage.PromptTokens) * tieredInputRate(pricing, totalTokens, tier)
	} else if videoSeconds != nil && *videoSeconds > 0 {
		if rate := tieredVideoInputPerSecondRate(pricing, totalTokens); rate > 0 {
			inputCost = float64(*videoSeconds) * rate
		}
	}

	outputCost := 0.0
	if usage != nil && usage.CompletionTokens > 0 {
		outputCost = float64(usage.CompletionTokens) * tieredOutputRate(pricing, totalTokens, tier)
	} else if videoSeconds != nil && *videoSeconds > 0 {
		if pricing.OutputCostPerVideoPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerVideoPerSecond
		} else if pricing.OutputCostPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerSecond
		}
	}
	return inputCost + outputCost
}

func computeOCRCost(pricing *configstoreTables.TableModelPricing, ocrProcessedPages *int, ocrIsAnnotated *bool) float64 {
	if ocrProcessedPages == nil {
		return 0
	}
	pages := float64(*ocrProcessedPages)
	cost := 0.0
	if pricing.OCRCostPerPage != nil {
		cost += pages * *pricing.OCRCostPerPage
	}
	if ocrIsAnnotated != nil && *ocrIsAnnotated && pricing.AnnotationCostPerPage != nil {
		cost += pages * *pricing.AnnotationCostPerPage
	}
	return cost
}

// ---------------------------------------------------------------------------
// Tier resolution and rate selectors
// ---------------------------------------------------------------------------

func tierFromString(s *schemas.BifrostServiceTier) serviceTier {
	if s == nil {
		return serviceTier{}
	}
	switch *s {
	case schemas.BifrostServiceTierPriority:
		return serviceTier{isPriority: true}
	case schemas.BifrostServiceTierFlex:
		return serviceTier{isFlex: true}
	default:
		return serviceTier{}
	}
}

func tieredInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if tier.isFlex && pricing.InputCostPerTokenFlex != nil {
		return *pricing.InputCostPerTokenFlex
	}
	if totalTokens > TokenTierAbove272K {
		if tier.isPriority && pricing.InputCostPerTokenAbove272kTokensPriority != nil {
			return *pricing.InputCostPerTokenAbove272kTokensPriority
		}
		if pricing.InputCostPerTokenAbove272kTokens != nil {
			return *pricing.InputCostPerTokenAbove272kTokens
		}
	}
	if totalTokens > TokenTierAbove200K {
		if tier.isPriority && pricing.InputCostPerTokenAbove200kTokensPriority != nil {
			return *pricing.InputCostPerTokenAbove200kTokensPriority
		}
		if pricing.InputCostPerTokenAbove200kTokens != nil {
			return *pricing.InputCostPerTokenAbove200kTokens
		}
	}
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerTokenAbove128kTokens != nil {
		return *pricing.InputCostPerTokenAbove128kTokens
	}
	if tier.isPriority && pricing.InputCostPerTokenPriority != nil {
		return *pricing.InputCostPerTokenPriority
	}
	if pricing.InputCostPerToken != nil {
		return *pricing.InputCostPerToken
	}
	return 0
}

func tieredOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if tier.isFlex && pricing.OutputCostPerTokenFlex != nil {
		return *pricing.OutputCostPerTokenFlex
	}
	if totalTokens > TokenTierAbove272K {
		if tier.isPriority && pricing.OutputCostPerTokenAbove272kTokensPriority != nil {
			return *pricing.OutputCostPerTokenAbove272kTokensPriority
		}
		if pricing.OutputCostPerTokenAbove272kTokens != nil {
			return *pricing.OutputCostPerTokenAbove272kTokens
		}
	}
	if totalTokens > TokenTierAbove200K {
		if tier.isPriority && pricing.OutputCostPerTokenAbove200kTokensPriority != nil {
			return *pricing.OutputCostPerTokenAbove200kTokensPriority
		}
		if pricing.OutputCostPerTokenAbove200kTokens != nil {
			return *pricing.OutputCostPerTokenAbove200kTokens
		}
	}
	if totalTokens > TokenTierAbove128K && pricing.OutputCostPerTokenAbove128kTokens != nil {
		return *pricing.OutputCostPerTokenAbove128kTokens
	}
	if tier.isPriority && pricing.OutputCostPerTokenPriority != nil {
		return *pricing.OutputCostPerTokenPriority
	}
	if pricing.OutputCostPerToken != nil {
		return *pricing.OutputCostPerToken
	}
	return 0
}

func tieredImageInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerImageAbove128kTokens != nil {
		return *pricing.InputCostPerImageAbove128kTokens
	}
	if pricing.InputCostPerImageToken != nil {
		return *pricing.InputCostPerImageToken
	}
	return tieredInputRate(pricing, totalTokens, tier)
}

func tieredImageOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if pricing.OutputCostPerImageToken != nil {
		return *pricing.OutputCostPerImageToken
	}
	return tieredOutputRate(pricing, totalTokens, tier)
}

func tieredAudioInputPerSecondRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerAudioPerSecondAbove128kTokens != nil {
		return *pricing.InputCostPerAudioPerSecondAbove128kTokens
	}
	if pricing.InputCostPerAudioPerSecond != nil {
		return *pricing.InputCostPerAudioPerSecond
	}
	if pricing.InputCostPerSecond != nil {
		return *pricing.InputCostPerSecond
	}
	return 0
}

func tieredVideoInputPerSecondRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerVideoPerSecondAbove128kTokens != nil {
		return *pricing.InputCostPerVideoPerSecondAbove128kTokens
	}
	if pricing.InputCostPerVideoPerSecond != nil {
		return *pricing.InputCostPerVideoPerSecond
	}
	return 0
}

func tieredAudioTokenInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if pricing.InputCostPerAudioToken != nil {
		return *pricing.InputCostPerAudioToken
	}
	return tieredInputRate(pricing, totalTokens, tier)
}

func tieredAudioTokenOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if pricing.OutputCostPerAudioToken != nil {
		return *pricing.OutputCostPerAudioToken
	}
	return tieredOutputRate(pricing, totalTokens, tier)
}

func tieredCacheReadInputTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if tier.isFlex && pricing.CacheReadInputTokenCostFlex != nil {
		return *pricing.CacheReadInputTokenCostFlex
	}
	if totalTokens > TokenTierAbove272K {
		if tier.isPriority && pricing.CacheReadInputTokenCostAbove272kTokensPriority != nil {
			return *pricing.CacheReadInputTokenCostAbove272kTokensPriority
		}
		if pricing.CacheReadInputTokenCostAbove272kTokens != nil {
			return *pricing.CacheReadInputTokenCostAbove272kTokens
		}
	}
	if totalTokens > TokenTierAbove200K {
		if tier.isPriority && pricing.CacheReadInputTokenCostAbove200kTokensPriority != nil {
			return *pricing.CacheReadInputTokenCostAbove200kTokensPriority
		}
		if pricing.CacheReadInputTokenCostAbove200kTokens != nil {
			return *pricing.CacheReadInputTokenCostAbove200kTokens
		}
	}
	if tier.isPriority && pricing.CacheReadInputTokenCostPriority != nil {
		return *pricing.CacheReadInputTokenCostPriority
	}
	if pricing.CacheReadInputTokenCost != nil {
		return *pricing.CacheReadInputTokenCost
	}
	return tieredInputRate(pricing, totalTokens, tier)
}

// Note: flex tier is not checked here because cache creation isn't a concept
// in OpenAI's pricing model (the only flex-tier provider). Only cache read
// has a flex-specific rate.
func tieredCacheCreationInputTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if totalTokens > TokenTierAbove200K && pricing.CacheCreationInputTokenCostAbove200kTokens != nil {
		return *pricing.CacheCreationInputTokenCostAbove200kTokens
	}
	if pricing.CacheCreationInputTokenCost != nil {
		return *pricing.CacheCreationInputTokenCost
	}
	return tieredInputRate(pricing, totalTokens, tier)
}

func tieredCacheCreationInputAbove1hrTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if totalTokens > TokenTierAbove200K && pricing.CacheCreationInputTokenCostAbove1hrAbove200kTokens != nil {
		return *pricing.CacheCreationInputTokenCostAbove1hrAbove200kTokens
	}
	if pricing.CacheCreationInputTokenCostAbove1hr != nil {
		return *pricing.CacheCreationInputTokenCostAbove1hr
	}
	return tieredCacheCreationInputTokenRate(pricing, totalTokens, tier)
}

func safeTotalTokens(usage *schemas.BifrostLLMUsage) int {
	if usage == nil {
		return 0
	}
	return usage.TotalTokens
}

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

// resolvePricing resolves the pricing entry for a request directly from the
// RoutingInfo populated on the response/error by core.bifrost at request time.
//
// Lookup precedence — AliasModelName → AliasModelID → ModelName. Each
// non-empty candidate is tried against the base catalog in order; the first
// hit wins.
//
//   - AliasModelName (RoutingInfo.ResolvedKeyAlias.ModelName) is the canonical
//     model name the admin tagged on the matched alias. Catches the
//     opaque-deployment-ID case where the wire model wouldn't hit the catalog
//     on its own.
//   - AliasModelID (RoutingInfo.ResolvedKeyAlias.ModelID) is the wire model
//     when an alias matched. nil/empty otherwise.
//   - ModelName (RoutingInfo.Model) is the model string the caller sent — the
//     alias key when an alias matched, or the raw user input when none did.
//
// Overrides are applied keyed by the wire model (AliasModelID when an alias
// matched, otherwise ModelName) so per-deployment override pricing stays
// addressable in either flow.
func (s *Store) resolvePricing(routingInfo schemas.RoutingInfo, requestType schemas.RequestType, scopes LookupScopes) *configstoreTables.TableModelPricing {
	provider := string(routingInfo.Provider)
	var aliasModelID, aliasModelName string
	if rka := routingInfo.ResolvedKeyAlias; rka != nil {
		aliasModelID = rka.ModelID
		if rka.ModelName != nil {
			aliasModelName = *rka.ModelName
		}
	}
	overrideKey := aliasModelID
	if overrideKey == "" {
		overrideKey = routingInfo.Model
	}
	if s.logger != nil {
		s.logger.Debug("looking up pricing for wire model %s and provider %s of request type %s", overrideKey, provider, normalizeRequestType(requestType))
	}

	if scopes.Provider == "" {
		scopes.Provider = provider
	}

	for _, candidate := range []string{aliasModelName, aliasModelID, routingInfo.Model} {
		if candidate == "" {
			continue
		}
		base, exists := s.getBasePricing(candidate, provider, requestType)
		if exists && base != nil {
			result, _ := s.applyPricingOverrides(overrideKey, requestType, *base, scopes)
			return &result
		}
		if s.logger != nil {
			s.logger.Debug("pricing not found for %s, trying next candidate", candidate)
		}
	}

	// No base catalog entry found — still try overrides in case the user
	// defined override-only pricing for a model outside the built-in catalog.
	if s.logger != nil {
		s.logger.Debug("pricing not found for any candidate (provider %s), trying override-only pricing keyed by %s", provider, overrideKey)
	}
	result, applied := s.applyPricingOverrides(overrideKey, requestType, configstoreTables.TableModelPricing{}, scopes)
	if applied {
		return &result
	}
	if s.logger != nil {
		s.logger.Debug("no pricing found for wire model %s and provider %s, skipping cost calculation", overrideKey, provider)
	}
	return nil
}

// getBasePricing looks up catalog pricing for (model, provider, requestType)
// with provider-specific fallback chains:
//
//   - Gemini: retries under "vertex", then chat-mode fallback for Responses.
//   - Vertex: strips "provider/model" prefix and retries, then chat-mode for Responses.
//   - Bedrock: prepends "anthropic." for Claude models, then chat-mode for Responses.
//   - All providers: Responses/ResponsesStream falls back to chat mode.
//   - All providers: ImageEdit/ImageVariation falls back to image-generation mode.
//   - ContainerCreate: chat mode for the model, then base "container" entry.
func (s *Store) getBasePricing(model, provider string, requestType schemas.RequestType) (*configstoreTables.TableModelPricing, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	mode := normalizeRequestType(requestType)

	pricing, ok := s.pricingData[makeKey(model, provider, mode)]
	if ok {
		return &pricing, true
	}

	if provider == string(schemas.Gemini) {
		if s.logger != nil {
			s.logger.Debug("primary lookup failed, trying vertex provider for the same model")
		}
		pricing, ok = s.pricingData[makeKey(model, "vertex", mode)]
		if ok {
			return &pricing, true
		}
		if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest || requestType == schemas.WebSocketResponsesRequest || requestType == schemas.RealtimeRequest {
			if s.logger != nil {
				s.logger.Debug("secondary lookup failed, trying vertex provider for the same model in chat completion")
			}
			pricing, ok = s.pricingData[makeKey(model, "vertex", normalizeRequestType(schemas.ChatCompletionRequest))]
			if ok {
				return &pricing, true
			}
		}
	}

	if provider == string(schemas.Vertex) {
		// Vertex models can be of the form "provider/model" — try without the
		// provider prefix, keeping the original provider.
		if strings.Contains(model, "/") {
			modelWithoutProvider := strings.SplitN(model, "/", 2)[1]
			if s.logger != nil {
				s.logger.Debug("primary lookup failed, trying vertex provider for model with provider/model format %s", modelWithoutProvider)
			}
			pricing, ok = s.pricingData[makeKey(modelWithoutProvider, "vertex", mode)]
			if ok {
				return &pricing, true
			}
			if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest || requestType == schemas.WebSocketResponsesRequest || requestType == schemas.RealtimeRequest {
				if s.logger != nil {
					s.logger.Debug("secondary lookup failed, trying vertex provider for the same model in chat completion")
				}
				pricing, ok = s.pricingData[makeKey(modelWithoutProvider, "vertex", normalizeRequestType(schemas.ChatCompletionRequest))]
				if ok {
					return &pricing, true
				}
			}
		}
	}

	if provider == string(schemas.Bedrock) {
		if !strings.Contains(model, "anthropic.") && schemas.IsAnthropicModel(model) {
			if s.logger != nil {
				s.logger.Debug("primary lookup failed, trying with anthropic. prefix for the same model")
			}
			pricing, ok = s.pricingData[makeKey("anthropic."+model, provider, mode)]
			if ok {
				return &pricing, true
			}
			if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest || requestType == schemas.WebSocketResponsesRequest || requestType == schemas.RealtimeRequest {
				if s.logger != nil {
					s.logger.Debug("secondary lookup failed, trying chat provider for the same model in chat completion")
				}
				pricing, ok = s.pricingData[makeKey("anthropic."+model, provider, normalizeRequestType(schemas.ChatCompletionRequest))]
				if ok {
					return &pricing, true
				}
			}
		}
	}

	if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest || requestType == schemas.WebSocketResponsesRequest || requestType == schemas.RealtimeRequest {
		if s.logger != nil {
			s.logger.Debug("primary lookup failed, trying chat provider for the same model in chat completion")
		}
		pricing, ok = s.pricingData[makeKey(model, provider, normalizeRequestType(schemas.ChatCompletionRequest))]
		if ok {
			return &pricing, true
		}
	}

	if requestType == schemas.ImageEditRequest ||
		requestType == schemas.ImageEditStreamRequest ||
		requestType == schemas.ImageVariationRequest {
		if s.logger != nil {
			s.logger.Debug("primary lookup failed, trying image generation provider for the same model")
		}
		pricing, ok = s.pricingData[makeKey(model, provider, normalizeRequestType(schemas.ImageGenerationRequest))]
		if ok {
			return &pricing, true
		}
	}

	if requestType == schemas.ContainerCreateRequest {
		if s.logger != nil {
			s.logger.Debug("primary lookup failed, trying chat mode for container create pricing")
		}
		pricing, ok = s.pricingData[makeKey(model, provider, normalizeRequestType(schemas.ChatCompletionRequest))]
		if ok {
			return &pricing, true
		}
		if model != "container" {
			if s.logger != nil {
				s.logger.Debug("memory-specific container pricing not found, falling back to base container entry")
			}
			pricing, ok = s.pricingData[makeKey("container", provider, normalizeRequestType(schemas.ChatCompletionRequest))]
			if ok {
				return &pricing, true
			}
		}
	}

	return nil, false
}

// UpsertModelPricingAttributes writes the additional_attributes column for
// every pricing row matching (model, provider), then reloads the pricing
// cache so the new values are immediately visible. Returns the number of
// rows updated (0 = no such pricing row, which callers must surface as a
// validation error). An empty/nil attrs map clears the column.
func (s *Store) UpsertModelPricingAttributes(ctx context.Context, model string, provider schemas.ModelProvider, attrs map[string]string) (int64, error) {
	if s.configStore == nil {
		return 0, fmt.Errorf("model catalog requires a config store")
	}
	rows, err := s.configStore.UpsertModelPricingAttributes(ctx, model, string(provider), attrs)
	if err != nil {
		return 0, err
	}
	if rows == 0 {
		return 0, nil
	}
	if err := s.LoadFromDB(ctx); err != nil {
		return rows, fmt.Errorf("failed to reload pricing cache after attribute write: %w", err)
	}
	return rows, nil
}

// ---------------------------------------------------------------------------
// Passthrough pricing helpers
// ---------------------------------------------------------------------------

func detectPassthroughRequestType(provider schemas.ModelProvider, path string) schemas.RequestType {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	path = strings.TrimRight(path, "/")
	switch provider {
	case schemas.OpenAI, schemas.Azure:
		switch {
		case strings.HasSuffix(path, "/chat/completions"):
			return schemas.ChatCompletionRequest
		case strings.HasSuffix(path, "/completions"):
			return schemas.TextCompletionRequest
		case strings.HasSuffix(path, "/embeddings"):
			return schemas.EmbeddingRequest
		case strings.HasSuffix(path, "/responses"):
			return schemas.ResponsesRequest
		case strings.HasSuffix(path, "/images/generations"):
			return schemas.ImageGenerationRequest
		case strings.HasSuffix(path, "/images/edits"):
			return schemas.ImageEditRequest
		case strings.HasSuffix(path, "/images/variations"):
			return schemas.ImageVariationRequest
		case strings.HasSuffix(path, "/audio/speech"):
			return schemas.SpeechRequest
		case strings.HasSuffix(path, "/audio/transcriptions"),
			strings.HasSuffix(path, "/audio/translations"):
			return schemas.TranscriptionRequest
		case strings.HasSuffix(path, "/containers"):
			return schemas.ContainerCreateRequest
		case strings.Contains(path, "/video"):
			return schemas.VideoGenerationRequest
		default:
			return schemas.ChatCompletionRequest
		}
	case schemas.Gemini, schemas.Vertex:
		if strings.Contains(path, "/interactions") {
			return schemas.ResponsesRequest
		}
		colonIdx := strings.LastIndexByte(path, ':')
		if colonIdx < 0 {
			return schemas.ChatCompletionRequest
		}
		switch path[colonIdx+1:] {
		case "generateContent", "streamGenerateContent":
			return schemas.ResponsesRequest
		case "embedContent", "batchEmbedContents":
			return schemas.EmbeddingRequest
		case "generateImages":
			return schemas.ImageGenerationRequest
		case "predict":
			return schemas.EmbeddingRequest
		case "predictLongRunning":
			return schemas.VideoGenerationRequest
		default:
			return schemas.ChatCompletionRequest
		}
	case schemas.Anthropic:
		switch {
		case strings.HasSuffix(path, "/messages"):
			return schemas.ResponsesRequest
		case strings.HasSuffix(path, "/complete"):
			return schemas.TextCompletionRequest
		default:
			return schemas.ResponsesRequest
		}
	default:
		return schemas.ChatCompletionRequest
	}
}

// inferPassthroughRequestType determines the request type from usage fields
// (primary), falling back to path detection for text/embedding/responses
// where LLMUsage is ambiguous.
func inferPassthroughRequestType(provider schemas.ModelProvider, path string, su *schemas.BifrostPassthroughUsage) schemas.RequestType {
	if su != nil {
		if su.ContainerIdentifier != "" {
			return schemas.ContainerCreateRequest
		}
		if su.ImageUsage != nil {
			return schemas.ImageGenerationRequest
		}
		if su.AudioInputChars > 0 {
			return schemas.SpeechRequest
		}
		if su.AudioTokenDetails != nil || su.AudioSeconds != nil {
			return schemas.TranscriptionRequest
		}
		if su.VideoSeconds != nil {
			return schemas.VideoGenerationRequest
		}
	}
	return detectPassthroughRequestType(provider, path)
}

func passthroughUsageToCostInput(su *schemas.BifrostPassthroughUsage) costInput {
	var input costInput
	if su.LLMUsage != nil {
		input.usage = su.LLMUsage
	}
	if su.ServiceTier != nil {
		input.tier = tierFromString(su.ServiceTier)
	}
	if su.ImageUsage != nil {
		input.imageUsage = su.ImageUsage
		input.imageSize = su.ImageSize
		input.imageQuality = su.ImageQuality
	}
	if su.AudioInputChars > 0 {
		input.audioTextInputChars = su.AudioInputChars
	}
	if su.AudioSeconds != nil {
		input.audioSeconds = su.AudioSeconds
	}
	if su.AudioTokenDetails != nil {
		input.audioTokenDetails = su.AudioTokenDetails
	}
	if su.VideoSeconds != nil {
		input.videoSeconds = su.VideoSeconds
	}
	if su.ContainerIdentifier != "" {
		input.containerIdentifierString = su.ContainerIdentifier
	}
	return input
}
