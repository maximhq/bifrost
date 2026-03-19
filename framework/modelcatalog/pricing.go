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
	usage               *schemas.BifrostLLMUsage
	audioTextInputChars int
	audioSeconds        *int
	audioTokenDetails   *schemas.TranscriptionUsageInputTokenDetails
	imageUsage          *schemas.ImageUsage
	imageSize           string // e.g. "1024x1024", used for per-pixel pricing
	imageQuality        string // "low", "medium", "high", "auto" (gpt-image-1.5); empty = use base rate
	videoSeconds        *int
}

// CalculateCost calculates the cost of a Bifrost response.
// It handles all request types, cache debug billing, and tiered pricing.
// If scopes is nil, no scoped overrides are applied.
func (mc *ModelCatalog) CalculateCost(result *schemas.BifrostResponse, scopes *PricingLookupScopes) float64 {
	if result == nil {
		return 0
	}

	var s PricingLookupScopes
	if scopes != nil {
		s = *scopes
	}

	// Handle semantic cache billing
	cacheDebug := result.GetExtraFields().CacheDebug
	if cacheDebug != nil {
		return mc.calculateCostWithCache(result, cacheDebug, s)
	}

	return mc.calculateBaseCost(result, s)
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
	if scopes.Provider == "" {
		scopes.Provider = *cacheDebug.ProviderUsed
	}
	pricing := mc.resolvePricing(*cacheDebug.ProviderUsed, *cacheDebug.ModelUsed, "", schemas.EmbeddingRequest, scopes)
	if pricing == nil {
		return 0
	}
	return float64(*cacheDebug.InputTokens) * tieredInputRate(pricing, *cacheDebug.InputTokens)
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
	if input.usage == nil && input.audioSeconds == nil && input.audioTokenDetails == nil && input.imageUsage == nil && input.videoSeconds == nil && input.audioTextInputChars == 0 {
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
		return computeSpeechCost(pricing, input.usage, input.audioSeconds, input.audioTextInputChars)
	case schemas.TranscriptionRequest:
		return computeTranscriptionCost(pricing, input.usage, input.audioSeconds, input.audioTokenDetails)
	case schemas.ImageGenerationRequest, schemas.ImageEditRequest, schemas.ImageVariationRequest:
		return computeImageCost(pricing, input.imageUsage, input.imageSize, input.imageQuality)
	case schemas.VideoGenerationRequest, schemas.VideoRemixRequest:
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
			// No usage data but response exists — default to empty so per-image pricing can apply
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
	cacheReadInputRate := tieredCacheReadInputTokenRate(pricing, totalTokens)
	cacheCreationInputRate := tieredCacheCreationInputTokenRate(pricing, totalTokens)

	// Clamp cached token counts to avoid negative billing on malformed provider payloads
	if cachedReadTokens > promptTokens {
		cachedReadTokens = promptTokens
	}
	if cachedWriteTokens > promptTokens-cachedReadTokens {
		cachedWriteTokens = promptTokens - cachedReadTokens
	}

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
	return float64(usage.PromptTokens) * tieredInputRate(pricing, usage.TotalTokens)
}

// computeRerankCost handles rerank requests.
func computeRerankCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage) float64 {
	if usage == nil {
		return 0
	}
	inputCost := float64(usage.PromptTokens) * tieredInputRate(pricing, usage.TotalTokens)
	outputCost := float64(usage.CompletionTokens) * tieredOutputRate(pricing, usage.TotalTokens)

	searchCost := 0.0
	if pricing.SearchContextCostPerQuery != nil && usage.CompletionTokensDetails != nil && usage.CompletionTokensDetails.NumSearchQueries != nil {
		searchCost = float64(*usage.CompletionTokensDetails.NumSearchQueries) * *pricing.SearchContextCostPerQuery
	}

	return inputCost + outputCost + searchCost
}

// computeSpeechCost handles speech (TTS) requests.
// Input is text (PromptTokens), output is audio (CompletionTokens).
//
// Per-character pricing (InputCostPerCharacter) is used as first-class support for TTS/audio
// models — providers such as OpenAI TTS, ElevenLabs, and AWS Polly bill per character of
// input text rather than per token. PromptTokens from usage is treated as the character count
// since TTS providers report their billable unit in that field.
// Output falls back to per-second duration when no audio token rate is configured.
func computeSpeechCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTextInputChars int) float64 {
	totalTokens := safeTotalTokens(usage)

	// Input: per-character rate takes precedence for TTS/audio models
	inputCost := 0.0
	if audioTextInputChars > 0 {
		if pricing.InputCostPerCharacter != nil {
			inputCost = float64(audioTextInputChars) * *pricing.InputCostPerCharacter
		} else {
			inputCost = float64(audioTextInputChars) * tieredInputRate(pricing, totalTokens)
		}
	} else if usage != nil && usage.PromptTokens > 0 {
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
		return float64(audioTokenDetails.AudioTokens)*tieredAudioTokenInputRate(pricing, totalTokens) +
			float64(audioTokenDetails.TextTokens)*tieredInputRate(pricing, totalTokens)
	}

	// Generic input tokens
	if usage != nil && usage.PromptTokens > 0 {
		return float64(usage.PromptTokens) * tieredInputRate(pricing, totalTokens)
	}

	// Per-second duration fallback
	if audioSeconds != nil && *audioSeconds > 0 {
		if rate := tieredAudioInputPerSecondRate(pricing, totalTokens); rate > 0 {
			return float64(*audioSeconds) * rate
		}
	}

	return 0
}

// computeAudioOutputCost calculates output cost for audio: audio tokens first,
// then generic output tokens, then per-second duration fallback.
func computeAudioOutputCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, totalTokens int) float64 {
	// Audio-specific output tokens
	if usage != nil && usage.CompletionTokens > 0 {
		return float64(usage.CompletionTokens) * tieredAudioTokenOutputRate(pricing, totalTokens)
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
// imageQuality must be one of "low", "medium", "high", "auto" to use quality-specific rates; other values use base rates.
func computeImageCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, imageSize string, imageQuality string) float64 {
	if imageUsage == nil {
		return 0
	}

	totalTokens := imageUsage.TotalTokens
	pixels := parseImagePixels(imageSize)
	inputCost := computeImageInputCost(pricing, imageUsage, totalTokens, pixels)
	outputCost := computeImageOutputCost(pricing, imageUsage, totalTokens, pixels, imageQuality)

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
		return float64(inputTextTokens)*tieredInputRate(pricing, totalTokens) +
			float64(inputImageTokens)*tieredImageInputRate(pricing, totalTokens)
	}

	// Per-pixel pricing fallback
	if pricing.InputCostPerPixel != nil && pixels > 0 && imageUsage.NumInputImages > 0 {
		return float64(pixels*imageUsage.NumInputImages) * *pricing.InputCostPerPixel
	}

	// Fall back to per-image count pricing
	if pricing.InputCostPerImage != nil && imageUsage.NumInputImages > 0 {
		return float64(imageUsage.NumInputImages) * *pricing.InputCostPerImage
	}

	return 0
}

// computeImageOutputCost calculates output cost: tokens first, then per-pixel, then per-image count fallback.
// imageQuality: "low", "medium", "high", "auto" use quality-specific rates when available; other values use base/size-tier rates.
func computeImageOutputCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, totalTokens int, pixels int, imageQuality string) float64 {
	// Try token-based pricing first
	var outputTextTokens, outputImageTokens int
	if imageUsage.OutputTokensDetails != nil {
		outputImageTokens = imageUsage.OutputTokensDetails.ImageTokens
		outputTextTokens = imageUsage.OutputTokensDetails.TextTokens
	} else {
		outputImageTokens = imageUsage.OutputTokens
	}

	if outputTextTokens > 0 || outputImageTokens > 0 {
		return float64(outputTextTokens)*tieredOutputRate(pricing, totalTokens) +
			float64(outputImageTokens)*tieredImageOutputRate(pricing, totalTokens)
	}

	// Per-pixel pricing fallback
	if pricing.OutputCostPerPixel != nil && pixels > 0 {
		numOutputImages := 1
		if imageUsage.OutputTokensDetails != nil && imageUsage.OutputTokensDetails.NImages > 0 {
			numOutputImages = imageUsage.OutputTokensDetails.NImages
		}
		return float64(pixels*numOutputImages) * *pricing.OutputCostPerPixel
	}

	// Fall back to per-image count pricing with size-tier selection
	// TODO: handle premium image flag when it becomes available in imageUsage
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

// computeVideoCost handles video generation requests.
// Input and output are calculated independently — tokens first, then per-second fallback.
func computeVideoCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, videoSeconds *int) float64 {
	totalTokens := safeTotalTokens(usage)

	// Input: text prompt tokens first, then per-second fallback
	inputCost := 0.0
	if usage != nil && usage.PromptTokens > 0 {
		inputCost = float64(usage.PromptTokens) * tieredInputRate(pricing, totalTokens)
	} else if videoSeconds != nil && *videoSeconds > 0 {
		if rate := tieredVideoInputPerSecondRate(pricing, totalTokens); rate > 0 {
			inputCost = float64(*videoSeconds) * rate
		}
	}

	// Output: completion tokens first, then per-second fallback
	outputCost := 0.0
	if usage != nil && usage.CompletionTokens > 0 {
		outputCost = float64(usage.CompletionTokens) * tieredOutputRate(pricing, totalTokens)
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

// tieredInputRate returns the effective per-token input rate based on total token count.
func tieredInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove200K && pricing.InputCostPerTokenAbove200kTokens != nil {
		return *pricing.InputCostPerTokenAbove200kTokens
	}
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerTokenAbove128kTokens != nil {
		return *pricing.InputCostPerTokenAbove128kTokens
	}
	return pricing.InputCostPerToken
}

// tieredOutputRate returns the effective per-token output rate based on total token count.
func tieredOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove200K && pricing.OutputCostPerTokenAbove200kTokens != nil {
		return *pricing.OutputCostPerTokenAbove200kTokens
	}
	if totalTokens > TokenTierAbove128K && pricing.OutputCostPerTokenAbove128kTokens != nil {
		return *pricing.OutputCostPerTokenAbove128kTokens
	}
	return pricing.OutputCostPerToken
}

// tieredImageInputRate returns the effective rate for image tokens on the input side.
// Falls back to the general tieredInputRate when no image-specific rate is configured.
func tieredImageInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerImageAbove128kTokens != nil {
		return *pricing.InputCostPerImageAbove128kTokens
	}
	if pricing.InputCostPerImageToken != nil {
		return *pricing.InputCostPerImageToken
	}
	return tieredInputRate(pricing, totalTokens)
}

// tieredImageOutputRate returns the effective rate for image tokens on the output side.
// Falls back to the general tieredOutputRate when no image-specific rate is configured.
func tieredImageOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if pricing.OutputCostPerImageToken != nil {
		return *pricing.OutputCostPerImageToken
	}
	return tieredOutputRate(pricing, totalTokens)
}

// tieredAudioInputPerSecondRate returns the effective per-second rate for audio input.
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

// tieredVideoInputPerSecondRate returns the effective per-second rate for video input.
func tieredVideoInputPerSecondRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerVideoPerSecondAbove128kTokens != nil {
		return *pricing.InputCostPerVideoPerSecondAbove128kTokens
	}
	if pricing.InputCostPerVideoPerSecond != nil {
		return *pricing.InputCostPerVideoPerSecond
	}
	return 0
}

// tieredAudioTokenInputRate returns the effective per-token rate for audio input tokens.
// Falls back to the general tieredInputRate when no audio-specific rate is configured.
func tieredAudioTokenInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if pricing.InputCostPerAudioToken != nil {
		return *pricing.InputCostPerAudioToken
	}
	return tieredInputRate(pricing, totalTokens)
}

// tieredAudioTokenOutputRate returns the effective per-token rate for audio output tokens.
// Falls back to the general tieredOutputRate when no audio-specific rate is configured.
func tieredAudioTokenOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if pricing.OutputCostPerAudioToken != nil {
		return *pricing.OutputCostPerAudioToken
	}
	return tieredOutputRate(pricing, totalTokens)
}

func tieredCacheReadInputTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove200K && pricing.CacheReadInputTokenCostAbove200kTokens != nil {
		return *pricing.CacheReadInputTokenCostAbove200kTokens
	}
	if pricing.CacheReadInputTokenCost != nil {
		return *pricing.CacheReadInputTokenCost
	}
	return tieredInputRate(pricing, totalTokens)
}

func tieredCacheCreationInputTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove200K && pricing.CacheCreationInputTokenCostAbove200kTokens != nil {
		return *pricing.CacheCreationInputTokenCostAbove200kTokens
	}
	if pricing.CacheCreationInputTokenCost != nil {
		return *pricing.CacheCreationInputTokenCost
	}
	return tieredInputRate(pricing, totalTokens)
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
func (mc *ModelCatalog) resolvePricing(provider, model, deployment string, requestType schemas.RequestType, scopes PricingLookupScopes) *configstoreTables.TableModelPricing {
	mc.logger.Debug("looking up pricing for model %s and provider %s of request type %s", model, provider, normalizeRequestType(requestType))

	if scopes.Provider == "" {
		scopes.Provider = provider
	}

	base, exists := mc.getBasePricing(model, provider, requestType)
	if exists {
		result := mc.applyPricingOverrides(model, requestType, base, scopes)
		return &result
	}

	if deployment != "" {
		mc.logger.Debug("pricing not found for model %s, trying deployment %s", model, deployment)
		base, exists = mc.getBasePricing(deployment, provider, requestType)
		if exists {
			// Apply overrides using the requested model name, not the deployment name
			result := mc.applyPricingOverrides(model, requestType, base, scopes)
			return &result
		}
	}

	mc.logger.Debug("pricing not found for model %s and provider %s, skipping cost calculation", model, provider)
	return nil
}

// getBasePricing looks up catalog pricing for the given model, provider, and request type.
// It applies a provider-specific fallback chain when an exact match is not found:
//
//   - Gemini: retries under the "vertex" provider, then falls back to chat mode for Responses requests.
//   - Vertex: strips the "provider/model" prefix and retries, then falls back to chat mode for Responses requests.
//   - Bedrock: prepends the "anthropic." namespace for Claude models, then falls back to chat mode for Responses requests.
//   - All providers: for Responses/ResponsesStream requests, retries the lookup in chat mode.
//   - All providers: for ImageEdit/ImageVariation requests, retries the lookup in image-generation mode.
//
// The method acquires a read lock for the duration of the lookup.
//
// Input:  model       — exact model name to look up.
//
//	provider    — provider identifier (e.g. "openai", "anthropic").
//	requestType — the request type used to derive the pricing mode.
//
// Output: TableModelPricing — the matched pricing row (zero value when not found).
//
//	bool              — true when a pricing entry was found, false otherwise.
func (mc *ModelCatalog) getBasePricing(model, provider string, requestType schemas.RequestType) (configstoreTables.TableModelPricing, bool) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

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

	// Lookup in image generation if image edit not found
	if requestType == schemas.ImageEditRequest ||
		requestType == schemas.ImageEditStreamRequest ||
		requestType == schemas.ImageVariationRequest {
		mc.logger.Debug("primary lookup failed, trying image generation provider for the same model")
		pricing, ok = mc.pricingData[makeKey(model, provider, normalizeRequestType(schemas.ImageGenerationRequest))]
		if ok {
			return pricing, true
		}
	}

	return configstoreTables.TableModelPricing{}, false
}
