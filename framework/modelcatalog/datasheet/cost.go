package datasheet

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// CalculateCost calculates the cost of a Bifrost response.
// It handles all request types, cache debug billing, and tiered pricing.
// If scopes is nil, an empty LookupScopes is used; global and provider-scoped
// overrides may still apply since the provider is derived from the response.
func (s *Store) CalculateCost(result *schemas.BifrostResponse, scopes *LookupScopes) float64 {
	if result == nil {
		return 0
	}

	var lookupScopes LookupScopes
	if scopes != nil {
		lookupScopes = *scopes
	}

	// Handle semantic cache billing
	cacheDebug := result.GetExtraFields().CacheDebug
	if cacheDebug != nil {
		return s.calculateCostWithCache(result, cacheDebug, lookupScopes)
	}

	return s.calculateBaseCost(result, lookupScopes)
}

// CalculateCostForUsage computes the dollar cost from a bare usage object plus
// provider / model / request type, for cases where no full BifrostResponse
// exists. The primary use is billing partial usage carried on a failed or
// cancelled request via BifrostError.ExtraFields.BilledUsage: the
// provider consumed tokens, so we must charge for them even though there is no
// success response to read. It mirrors CalculateCost's compute path so success
// and failure billing use identical rates. Returns 0 when usage is nil.
func (s *Store) CalculateCostForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *LookupScopes) float64 {
	if usage == nil {
		return 0
	}

	var lookupScopes LookupScopes
	if scopes != nil {
		lookupScopes = *scopes
	}

	// If the provider already computed cost, trust it (matches calculateBaseCost).
	if usage.Cost != nil && usage.Cost.TotalCost > 0 {
		return usage.Cost.TotalCost
	}

	// Apply the served tier (fast mode / data residency) carried on the usage so
	// cancelled/failed fast or US-residency streams keep their multiplier.
	input := costInput{usage: usage}
	input.tier = tierFromResponse(nil, usage.Speed, usage.InferenceGeo)

	return s.computeCostFromInput(
		input,
		schemas.RoutingInfo{
			Provider:                provider,
			Model:                   model,
			ServerSideFallbackModel: usage.ServerSideFallbackModel,
		},
		normalizeStreamRequestType(requestType),
		lookupScopes,
	)
}

// calculateCostWithCache handles cost calculation when semantic cache debug info is present.
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

// computeCacheEmbeddingCost calculates the embedding cost for a semantic cache lookup.
func (s *Store) computeCacheEmbeddingCost(cacheDebug *schemas.BifrostCacheDebug, scopes LookupScopes) float64 {
	if cacheDebug == nil || cacheDebug.ProviderUsed == nil || cacheDebug.ModelUsed == nil || cacheDebug.InputTokens == nil {
		return 0
	}
	if scopes.Provider == "" {
		scopes.Provider = *cacheDebug.ProviderUsed
	}
	// Cache-debug pricing has only a single model identifier (whatever the
	// cache recorded). Maps to RoutingInfo.Model — no alias resolution
	// context exists for the cache-replayed request.
	pricing := s.resolvePricing(schemas.RoutingInfo{
		Provider: schemas.ModelProvider(*cacheDebug.ProviderUsed),
		Model:    *cacheDebug.ModelUsed,
	}, schemas.EmbeddingRequest, scopes)
	if pricing == nil {
		return 0
	}
	return float64(*cacheDebug.InputTokens) * tieredInputRate(pricing, *cacheDebug.InputTokens, serviceTier{})
}

// computeContainerCreationCost returns the cost for creating a container from an already-resolved pricing entry.
func computeContainerCreationCost(pricing *configstoreTables.TableModelPricing) float64 {
	if pricing == nil || pricing.CodeInterpreterCostPerSession == nil {
		return 0
	}
	return *pricing.CodeInterpreterCostPerSession
}

// calculateBaseCost extracts usage from the response and routes to the appropriate compute function.
func (s *Store) calculateBaseCost(result *schemas.BifrostResponse, scopes LookupScopes) float64 {
	extraFields := result.GetExtraFields()
	if extraFields == nil {
		return 0
	}

	// Read routing info populated by core.bifrost at request time.
	//
	// Backward-compat fallback: when the caller (e.g. LoggerPlugin's
	// RecalculateCosts replaying logs written before RoutingInfo existed,
	// or third-party plugins still on the legacy ExtraFields shape) leaves
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

	// Extract usage data from the response (passthrough and native paths unified)
	input := extractCostInput(result)

	// If provider already computed cost, use it
	if input.usage != nil && input.usage.Cost != nil && input.usage.Cost.TotalCost > 0 {
		return input.usage.Cost.TotalCost
	}

	// If no usage data at all, nothing to price
	if input.usage == nil && input.audioSeconds == nil && input.audioTokenDetails == nil && input.imageUsage == nil && input.videoSeconds == nil && input.audioTextInputChars == 0 && input.ocrProcessedPages == nil && input.containerIdentifierString == "" {
		return 0
	}

	if result.PassthroughResponse != nil {
		// Infer request type from usage fields + path; passthrough bypasses stream normalization.
		requestType = inferPassthroughRequestType(routingInfo.Provider, extraFields.PassthroughPath, result.PassthroughResponse.PassthroughUsage)
	} else {
		// Normalize stream request types to their base type for pricing lookup
		requestType = normalizeStreamRequestType(requestType)
	}

	// Azure Model Router bills a flat per-input-token surcharge on top of the
	// real cost of whatever underlying model Azure actually routed to. The
	// response's own model field carries that real model, distinct from the
	// "model-router" deployment name on RoutingInfo.Model.
	if result.PassthroughResponse == nil && routingInfo.Provider == schemas.Azure && schemas.IsAzureModelRouter(routingInfo.Model) &&
		(requestType == schemas.TextCompletionRequest || requestType == schemas.ChatCompletionRequest || requestType == schemas.ResponsesRequest) {
		return s.calculateAzureModelRouterCost(result, input, routingInfo, requestType, scopes)
	}

	return s.computeCostFromInput(input, routingInfo, requestType, scopes)
}

// calculateAzureModelRouterCost bills the Model Router deployment's own
// pricing row (the flat per-input-token surcharge) plus the real cost of the
// model it actually routed to, looked up fresh under the served model name so
// regular per-token/tiered pricing applies to it exactly as if it had been
// called directly.
func (s *Store) calculateAzureModelRouterCost(result *schemas.BifrostResponse, input costInput, routingInfo schemas.RoutingInfo, requestType schemas.RequestType, scopes LookupScopes) float64 {
	pricingRequestType := requestType
	if pricingRequestType == schemas.TextCompletionRequest {
		pricingRequestType = schemas.ChatCompletionRequest
	}

	cost := s.computeCostFromInput(input, routingInfo, pricingRequestType, scopes)

	if servedModel := azureModelRouterServedModel(result); servedModel != "" && servedModel != routingInfo.Model {
		underlyingRoutingInfo := schemas.RoutingInfo{
			Provider: routingInfo.Provider,
			Model:    servedModel,
		}
		cost += s.computeCostFromInput(input, underlyingRoutingInfo, pricingRequestType, scopes)
	}

	return cost
}

// azureModelRouterServedModel reads the model Azure Model Router actually
// routed to off the response body's own model field separate from the "model-router"
// deployment name carried on RoutingInfo.Model.
func azureModelRouterServedModel(result *schemas.BifrostResponse) string {
	switch {
	case result.ChatResponse != nil:
		return result.ChatResponse.Model
	case result.ResponsesResponse != nil:
		return result.ResponsesResponse.Model
	case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil:
		return result.ResponsesStreamResponse.Response.Model
	case result.TextCompletionResponse != nil:
		return result.TextCompletionResponse.Model
	default:
		return ""
	}
}

// computeCostFromInput resolves pricing for the given routing info + request
// type and routes the extracted usage to the appropriate per-modality compute
// function. Shared by calculateBaseCost (response-driven) and
// CalculateCostForUsage (bare-usage-driven, for failed/cancelled requests).
func (s *Store) computeCostFromInput(input costInput, routingInfo schemas.RoutingInfo, requestType schemas.RequestType, scopes LookupScopes) float64 {
	// When a pricing model override is set (e.g. container creates always look
	// up "container"), it replaces the lookup hierarchy entirely. Build a
	// synthetic RoutingInfo that reuses Provider but pins the model fields to
	// the container identifier — the lookup tries it as ModelName, the
	// override key is the container identifier so per-container overrides
	// stay addressable.
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

	// Route to the appropriate compute function
	switch requestType {
	case schemas.ChatCompletionRequest, schemas.TextCompletionRequest, schemas.ResponsesRequest, schemas.RealtimeRequest, schemas.CompactionRequest:
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
		input.tier = tierFromResponse(result.ChatResponse.ServiceTier, result.ChatResponse.Speed, result.ChatResponse.InferenceGeo)

	case result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil:
		input.usage = responsesUsageToBifrostUsage(result.ResponsesResponse.Usage)
		input.tier = tierFromResponse(result.ResponsesResponse.ServiceTier, result.ResponsesResponse.Speed, result.ResponsesResponse.InferenceGeo)

	case result.CompactionResponse != nil && result.CompactionResponse.Usage != nil:
		input.usage = responsesUsageToBifrostUsage(result.CompactionResponse.Usage)

	case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil && result.ResponsesStreamResponse.Response.Usage != nil:
		input.usage = responsesUsageToBifrostUsage(result.ResponsesStreamResponse.Response.Usage)
		input.tier = tierFromResponse(result.ResponsesStreamResponse.Response.ServiceTier, result.ResponsesStreamResponse.Response.Speed, result.ResponsesStreamResponse.Response.InferenceGeo)

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
		// Defensive copy: populateOutputImageCount writes into imageUsage,
		// and we must not mutate the caller's BifrostResponse during what is
		// otherwise a pure read path.
		if result.ImageGenerationResponse.Usage != nil {
			input.imageUsage = result.ImageGenerationResponse.Usage.DeepCopy()
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
		// Defensive copy mirrors the non-stream path so CalculateCost never
		// aliases the caller's response — keeps the read-only invariant
		// uniform and prevents accidental mutation if image-count derivation
		// is later added on this branch.
		if result.ImageGenerationStreamResponse.Usage != nil {
			input.imageUsage = result.ImageGenerationStreamResponse.Usage.DeepCopy()
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
	// Map token details for cache and search query pricing
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

	var audioSeconds *int
	if u.Seconds != nil {
		audioSeconds = new(int(*u.Seconds))
	}

	return usage, audioSeconds, audioTokenDetails
}

// ---------------------------------------------------------------------------
// Per-request-type cost computation
// ---------------------------------------------------------------------------

// computeTextCost handles chat, text completion, and responses requests.
func computeTextCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) float64 {
	if usage == nil {
		return 0
	}

	promptTokens := usage.PromptTokens
	completionTokens := usage.CompletionTokens

	// Extract cached token counts
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

	// Long-context pricing tiers are selected by input context size. Once
	// selected, the tier's input/cache/output rates apply to their respective
	// billed token categories for the request.
	tierTokens := promptTokens
	inputRate := tieredInputRate(pricing, tierTokens, tier)
	outputRate := tieredOutputRate(pricing, tierTokens, tier)
	cacheReadInputRate := tieredCacheReadInputTokenRate(pricing, tierTokens, tier)
	cacheCreationInputRate := tieredCacheCreationInputTokenRate(pricing, tierTokens, tier)
	cacheCreationInputAbove1hrInputRate := tieredCacheCreationInputAbove1hrTokenRate(pricing, tierTokens, tier)

	// Clamp cached token counts to avoid negative billing on malformed provider payloads
	if cachedReadTokens > promptTokens {
		cachedReadTokens = promptTokens
	}
	if cachedWriteTokens > promptTokens-cachedReadTokens {
		cachedWriteTokens = promptTokens - cachedReadTokens
	}
	// Should not happen, but just in case
	if cachedWriteTokensAbove1hr > cachedWriteTokens {
		cachedWriteTokensAbove1hr = cachedWriteTokens
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
		if cachedWriteTokensAbove1hr > 0 {
			inputCost += float64(cachedWriteTokensAbove1hr) * cacheCreationInputAbove1hrInputRate
		}
		inputCost += float64(cachedWriteTokens-cachedWriteTokensAbove1hr) * cacheCreationInputRate
	}

	outputCost := float64(completionTokens) * outputRate

	// Audio token cost: when token details include audio tokens, price them
	// at the dedicated audio rate and subtract from the text token costs above.
	// Realtime and audio-enabled chat models report audio tokens in details.
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
		// Subtract audio tokens charged at text rate, add at audio rate.
		audioCost += float64(inputAudioTokens) * (*pricing.InputCostPerAudioToken - inputRate)
	}
	if outputAudioTokens > 0 && pricing.OutputCostPerAudioToken != nil {
		audioCost += float64(outputAudioTokens) * (*pricing.OutputCostPerAudioToken - outputRate)
	}

	// Search query cost
	searchCost := 0.0
	if pricing.SearchContextCostPerQuery != nil && usage.CompletionTokensDetails != nil && usage.CompletionTokensDetails.NumSearchQueries != nil {
		searchCost = float64(*usage.CompletionTokensDetails.NumSearchQueries) * *pricing.SearchContextCostPerQuery
	}

	// Data residency (Anthropic inference_geo:"us") scales all token/cache costs
	// by a flat multiplier; the per-search fee is not a token category, so it is
	// excluded.
	tokenCost := inputCost + outputCost + audioCost
	if tier.inferenceGeoUS && pricing.InferenceGeoUSMultiplier != nil {
		tokenCost *= *pricing.InferenceGeoUSMultiplier
	}

	return tokenCost + searchCost
}

// computeEmbeddingCost handles embedding requests (input-only).
func computeEmbeddingCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) float64 {
	if usage == nil {
		return 0
	}
	return float64(usage.PromptTokens) * tieredInputRate(pricing, usage.PromptTokens, tier)
}

// computeRerankCost handles rerank requests.
func computeRerankCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) float64 {
	if usage == nil {
		return 0
	}
	tierTokens := usage.PromptTokens
	inputCost := float64(usage.PromptTokens) * tieredInputRate(pricing, tierTokens, tier)
	outputCost := float64(usage.CompletionTokens) * tieredOutputRate(pricing, tierTokens, tier)

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
func computeSpeechCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTextInputChars int, tier serviceTier) float64 {
	tierTokens := inputTierTokens(usage)

	// Input: per-character rate takes precedence for TTS/audio models
	inputCost := 0.0
	if audioTextInputChars > 0 {
		if pricing.InputCostPerCharacter != nil {
			inputCost = float64(audioTextInputChars) * *pricing.InputCostPerCharacter
		} else {
			inputCost = float64(audioTextInputChars) * tieredInputRate(pricing, tierTokens, tier)
		}
	} else if usage != nil && usage.PromptTokens > 0 {
		inputCost = float64(usage.PromptTokens) * tieredInputRate(pricing, tierTokens, tier)
	}

	// Output: audio tokens first, then per-second fallback
	outputCost := computeAudioOutputCost(pricing, usage, audioSeconds, tierTokens, tier)

	return inputCost + outputCost
}

// computeTranscriptionCost handles transcription (STT) requests.
// Input is audio, output is text (CompletionTokens).
// Input and output are calculated independently — tokens first, then per-second fallback.
func computeTranscriptionCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails, tier serviceTier) float64 {
	tierTokens := inputTierTokens(usage)

	// Input: audio tokens/details first, then per-second fallback
	inputCost := computeAudioInputCost(pricing, usage, audioSeconds, audioTokenDetails, tierTokens, tier)

	// Output: text tokens
	outputCost := 0.0
	if usage != nil && usage.CompletionTokens > 0 {
		outputCost = float64(usage.CompletionTokens) * tieredOutputRate(pricing, tierTokens, tier)
	}

	return inputCost + outputCost
}

// computeAudioInputCost calculates input cost for audio: audio token details first,
// then generic input tokens, then per-second duration fallback.
func computeAudioInputCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails, totalTokens int, tier serviceTier) float64 {
	// Audio token detail pricing (audio + text token breakdown)
	if audioTokenDetails != nil && (audioTokenDetails.AudioTokens > 0 || audioTokenDetails.TextTokens > 0) {
		return float64(audioTokenDetails.AudioTokens)*tieredAudioTokenInputRate(pricing, totalTokens, tier) +
			float64(audioTokenDetails.TextTokens)*tieredInputRate(pricing, totalTokens, tier)
	}

	// Generic input tokens
	if usage != nil && usage.PromptTokens > 0 {
		return float64(usage.PromptTokens) * tieredInputRate(pricing, totalTokens, tier)
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
func computeAudioOutputCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, totalTokens int, tier serviceTier) float64 {
	// Audio-specific output tokens
	if usage != nil && usage.CompletionTokens > 0 {
		return float64(usage.CompletionTokens) * tieredAudioTokenOutputRate(pricing, totalTokens, tier)
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
func computeImageCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, imageSize string, imageQuality string, tier serviceTier) float64 {
	if imageUsage == nil {
		return 0
	}

	tierTokens := imageInputTierTokens(imageUsage)
	pixels := parseImagePixels(imageSize)
	inputCost := computeImageInputCost(pricing, imageUsage, tierTokens, pixels, tier)
	outputCost := computeImageOutputCost(pricing, imageUsage, tierTokens, pixels, imageQuality, tier)

	return inputCost + outputCost
}

// computeImageInputCost calculates input cost: tokens first, then per-pixel, then per-image count fallback.
func computeImageInputCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, totalTokens int, pixels int, tier serviceTier) float64 {
	// Try token-based pricing first
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
func computeImageOutputCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, totalTokens int, pixels int, imageQuality string, tier serviceTier) float64 {
	// Try token-based pricing first
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
func computeVideoCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, videoSeconds *int, tier serviceTier) float64 {
	tierTokens := inputTierTokens(usage)

	// Input: text prompt tokens first, then per-second fallback
	inputCost := 0.0
	if usage != nil && usage.PromptTokens > 0 {
		inputCost = float64(usage.PromptTokens) * tieredInputRate(pricing, tierTokens, tier)
	} else if videoSeconds != nil && *videoSeconds > 0 {
		if rate := tieredVideoInputPerSecondRate(pricing, tierTokens); rate > 0 {
			inputCost = float64(*videoSeconds) * rate
		}
	}

	// Output: completion tokens first, then per-second fallback
	outputCost := 0.0
	if usage != nil && usage.CompletionTokens > 0 {
		outputCost = float64(usage.CompletionTokens) * tieredOutputRate(pricing, tierTokens, tier)
	} else if videoSeconds != nil && *videoSeconds > 0 {
		if pricing.OutputCostPerVideoPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerVideoPerSecond
		} else if pricing.OutputCostPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerSecond
		}
	}

	return inputCost + outputCost
}

// computeOCRCost handles OCR requests, billing per page processed.
// ocr_cost_per_page covers base processing; annotation_cost_per_page is added when set.
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
// Helpers
// ---------------------------------------------------------------------------

// tierFromResponse builds a serviceTier from a response's billing-relevant
// fields: the OpenAI service_tier (priority/flex) and the Anthropic speed
// (fast mode). speed == "fast" means fast mode was actually served — the
// provider echoes the served speed, so stripped/fell-back requests report
// "standard" and bill at standard rates.
func tierFromResponse(s *schemas.BifrostServiceTier, speed *string, inferenceGeo *string) serviceTier {
	var tier serviceTier
	if s != nil {
		switch *s {
		case schemas.BifrostServiceTierPriority:
			tier.isPriority = true
		case schemas.BifrostServiceTierFlex:
			tier.isFlex = true
		}
	}
	tier.isFast = speed != nil && *speed == "fast"
	tier.inferenceGeoUS = inferenceGeo != nil && strings.EqualFold(*inferenceGeo, "us")
	return tier
}

// tieredInputRate returns the effective per-token input rate based on total token count.
// Flex applies a flat rate. Priority-specific tier rates are preferred where available.
func tieredInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	// Fast mode (Anthropic) is a flat rate across the full context window — it
	// takes precedence over the token-count tiers below.
	if tier.isFast && pricing.InputCostPerTokenFast != nil {
		return *pricing.InputCostPerTokenFast
	}
	if tier.isFlex {
		if totalTokens > TokenTierAbove272K && pricing.InputCostPerTokenFlexAbove272kTokens != nil {
			return *pricing.InputCostPerTokenFlexAbove272kTokens
		}
		if pricing.InputCostPerTokenFlex != nil {
			return *pricing.InputCostPerTokenFlex
		}
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

// tieredOutputRate returns the effective per-token output rate based on total token count.
// Flex applies a flat rate. Priority-specific tier rates are preferred where available.
func tieredOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	// Fast mode (Anthropic) is a flat rate across the full context window — it
	// takes precedence over the token-count tiers below.
	if tier.isFast && pricing.OutputCostPerTokenFast != nil {
		return *pricing.OutputCostPerTokenFast
	}
	if tier.isFlex {
		if totalTokens > TokenTierAbove272K && pricing.OutputCostPerTokenFlexAbove272kTokens != nil {
			return *pricing.OutputCostPerTokenFlexAbove272kTokens
		}
		if pricing.OutputCostPerTokenFlex != nil {
			return *pricing.OutputCostPerTokenFlex
		}
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

// tieredImageInputRate returns the effective rate for image tokens on the input side.
// Falls back to the general tieredInputRate when no image-specific rate is configured.
func tieredImageInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerImageAbove128kTokens != nil {
		return *pricing.InputCostPerImageAbove128kTokens
	}
	if pricing.InputCostPerImageToken != nil {
		return *pricing.InputCostPerImageToken
	}
	return tieredInputRate(pricing, totalTokens, tier)
}

// tieredImageOutputRate returns the effective rate for image tokens on the output side.
// Falls back to the general tieredOutputRate when no image-specific rate is configured.
func tieredImageOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if pricing.OutputCostPerImageToken != nil {
		return *pricing.OutputCostPerImageToken
	}
	return tieredOutputRate(pricing, totalTokens, tier)
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
func tieredAudioTokenInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if pricing.InputCostPerAudioToken != nil {
		return *pricing.InputCostPerAudioToken
	}
	return tieredInputRate(pricing, totalTokens, tier)
}

// tieredAudioTokenOutputRate returns the effective per-token rate for audio output tokens.
// Falls back to the general tieredOutputRate when no audio-specific rate is configured.
func tieredAudioTokenOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if pricing.OutputCostPerAudioToken != nil {
		return *pricing.OutputCostPerAudioToken
	}
	return tieredOutputRate(pricing, totalTokens, tier)
}

func tieredCacheReadInputTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	// Fast mode (Anthropic) is a flat rate across the full context window.
	if tier.isFast && pricing.CacheReadInputTokenCostFast != nil {
		return *pricing.CacheReadInputTokenCostFast
	}
	if tier.isFlex {
		if totalTokens > TokenTierAbove272K && pricing.CacheReadInputTokenCostFlexAbove272kTokens != nil {
			return *pricing.CacheReadInputTokenCostFlexAbove272kTokens
		}
		if pricing.CacheReadInputTokenCostFlex != nil {
			return *pricing.CacheReadInputTokenCostFlex
		}
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

// OpenAI introduced cache-write (cache-creation) pricing with gpt-5.6, tiered by
// service tier (flex/priority) and by the 272k context window; Anthropic uses the
// flat fast rate. Precedence mirrors tieredCacheReadInputTokenRate.
func tieredCacheCreationInputTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	// Fast mode (Anthropic) is a flat rate across the full context window.
	if tier.isFast && pricing.CacheCreationInputTokenCostFast != nil {
		return *pricing.CacheCreationInputTokenCostFast
	}
	if tier.isFlex {
		if totalTokens > TokenTierAbove272K && pricing.CacheCreationInputTokenCostFlexAbove272kTokens != nil {
			return *pricing.CacheCreationInputTokenCostFlexAbove272kTokens
		}
		if pricing.CacheCreationInputTokenCostFlex != nil {
			return *pricing.CacheCreationInputTokenCostFlex
		}
	}
	// Priority has no long context: OpenAI does not offer priority >272k, and billing
	// uses the served tier (response.service_tier), so an actual-priority request is
	// always ≤272k. Its cache-write rate is flat, so it takes precedence over the
	// standard context tiers below (which would otherwise capture the 200k–272k band).
	if tier.isPriority && pricing.CacheCreationInputTokenCostPriority != nil {
		return *pricing.CacheCreationInputTokenCostPriority
	}
	if totalTokens > TokenTierAbove272K && pricing.CacheCreationInputTokenCostAbove272kTokens != nil {
		return *pricing.CacheCreationInputTokenCostAbove272kTokens
	}
	if totalTokens > TokenTierAbove200K && pricing.CacheCreationInputTokenCostAbove200kTokens != nil {
		return *pricing.CacheCreationInputTokenCostAbove200kTokens
	}
	if pricing.CacheCreationInputTokenCost != nil {
		return *pricing.CacheCreationInputTokenCost
	}
	return tieredInputRate(pricing, totalTokens, tier)
}

func tieredCacheCreationInputAbove1hrTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	// Fast mode (Anthropic) is a flat rate across the full context window.
	if tier.isFast && pricing.CacheCreationInputTokenCostAbove1hrFast != nil {
		return *pricing.CacheCreationInputTokenCostAbove1hrFast
	}
	if totalTokens > TokenTierAbove200K && pricing.CacheCreationInputTokenCostAbove1hrAbove200kTokens != nil {
		return *pricing.CacheCreationInputTokenCostAbove1hrAbove200kTokens
	}
	if pricing.CacheCreationInputTokenCostAbove1hr != nil {
		return *pricing.CacheCreationInputTokenCostAbove1hr
	}
	return tieredCacheCreationInputTokenRate(pricing, totalTokens, tier)
}

func inputTierTokens(usage *schemas.BifrostLLMUsage) int {
	if usage == nil {
		return 0
	}
	return usage.PromptTokens
}

func imageInputTierTokens(usage *schemas.ImageUsage) int {
	if usage == nil {
		return 0
	}
	if usage.InputTokensDetails != nil {
		return usage.InputTokensDetails.TextTokens + usage.InputTokensDetails.ImageTokens
	}
	if usage.InputTokens > 0 {
		return usage.InputTokens
	}

	// Some older/provider-specific image adapters only report TotalTokens.
	// Derive input from total-output when output is known, but do not treat a
	// bare total as input: total includes generated output tokens.
	outputTokens := imageOutputTokens(usage)
	if usage.TotalTokens > outputTokens {
		return usage.TotalTokens - outputTokens
	}
	return 0
}

func imageOutputTokens(usage *schemas.ImageUsage) int {
	if usage == nil {
		return 0
	}
	if usage.OutputTokensDetails != nil {
		return usage.OutputTokensDetails.TextTokens + usage.OutputTokensDetails.ImageTokens
	}
	return usage.OutputTokens
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

// resolvePricing resolves the pricing entry for a request directly from the
// RoutingInfo populated on the response/error by core.bifrost at request time.
//
// Lookup precedence — ServerSideFallbackModel → AliasModelName → AliasModelID →
// ModelName. Each non-empty candidate is tried against the base catalog in
// order; the first hit wins.
//
//   - ServerSideFallbackModel is the model that produced the response when the
//     provider swapped models inside one call (Anthropic server-side fallback).
//     Ranked first: the tokens being priced are its own. Nil on ordinary responses.
//   - AliasModelName (RoutingInfo.ResolvedKeyAlias.ModelName) is the canonical
//     model name the admin tagged on the matched alias. Catches the
//     opaque-deployment-ID case where the wire model wouldn't hit the catalog
//     on its own.
//   - AliasModelID (RoutingInfo.ResolvedKeyAlias.ModelID) is the wire model
//     when an alias matched. nil/empty otherwise.
//   - ModelName (RoutingInfo.Model) is the model string the caller sent — the
//     alias key when an alias matched, or the raw user input when none did.
//
// Overrides are applied keyed by the wire model (ServerSideFallbackModel when the
// provider handed off mid-call, else AliasModelID when an alias matched, otherwise
// ModelName) so per-deployment override pricing stays addressable in either flow.
// A fallback-served turn keys on the serving model so base rates and overrides
// agree: an override negotiated for the model that actually ran is the one that
// applies.
func (s *Store) resolvePricing(routingInfo schemas.RoutingInfo, requestType schemas.RequestType, scopes LookupScopes) *configstoreTables.TableModelPricing {
	provider := string(routingInfo.Provider)
	var aliasModelID, aliasModelName string
	if rka := routingInfo.ResolvedKeyAlias; rka != nil {
		aliasModelID = rka.ModelID
		if rka.ModelName != nil {
			aliasModelName = *rka.ModelName
		}
	}
	var serverSideFallbackModel string
	if routingInfo.ServerSideFallbackModel != nil {
		serverSideFallbackModel = *routingInfo.ServerSideFallbackModel
	}
	overrideKey := serverSideFallbackModel
	if overrideKey == "" {
		overrideKey = aliasModelID
	}
	if overrideKey == "" {
		overrideKey = routingInfo.Model
	}
	s.logger.Debug("looking up pricing for wire model %s and provider %s of request type %s", overrideKey, provider, normalizeRequestType(requestType))

	if scopes.Provider == "" {
		scopes.Provider = provider
	}

	for _, candidate := range []string{serverSideFallbackModel, aliasModelName, aliasModelID, routingInfo.Model} {
		if candidate == "" {
			continue
		}
		base, exists := s.getBasePricing(candidate, provider, requestType)
		if exists && base != nil {
			result, _ := s.applyPricingOverrides(overrideKey, requestType, *base, scopes)
			return &result
		}
		s.logger.Debug("pricing not found for %s, trying next candidate", candidate)
	}

	// No base catalog entry found; still try overrides in case the user defined
	// override-only pricing for a model not in the built-in catalog.
	s.logger.Debug("pricing not found for any candidate (provider %s), trying override-only pricing keyed by %s", provider, overrideKey)
	result, applied := s.applyPricingOverrides(overrideKey, requestType, configstoreTables.TableModelPricing{}, scopes)
	if applied {
		return &result
	}
	s.logger.Debug("no pricing found for wire model %s and provider %s, skipping cost calculation", overrideKey, provider)
	return nil
}

// getBasePricing looks up catalog pricing for the given model, provider, and request type.
// It applies a provider-specific fallback chain when an exact match is not found:
//
//   - Gemini: retries under the "vertex" provider, then falls back to the counterpart chat/responses mode.
//   - Vertex: strips the "provider/model" prefix and retries, then falls back to the counterpart chat/responses mode.
//   - Bedrock: prepends the vendor namespace ("anthropic.", "openai.", "google.", "xai.") inferred from the model family, then falls back to the counterpart chat/responses mode.
//   - Bedrock Mantle: folded onto the "bedrock" provider up front (datasheet rows for all Bedrock variants are stored there), so it shares every Bedrock fallback.
//   - All providers: chat and responses requests retry in each other's mode, since a model served over both APIs often has a datasheet row under only one of them.
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
func (s *Store) getBasePricing(model, provider string, requestType schemas.RequestType) (*configstoreTables.TableModelPricing, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	mode := normalizeRequestType(requestType)
	fallbackMode, hasFallbackMode := chatResponsesFallbackMode(requestType)

	// Datasheet rows for all Bedrock variants are stored under the "bedrock"
	// provider (normalizeProvider folds bedrock_* onto "bedrock"), so
	// bedrock_mantle lookups run entirely against "bedrock".
	if provider == string(schemas.BedrockMantle) {
		provider = string(schemas.Bedrock)
	}

	pricing, ok := s.pricingData[makeKey(model, provider, mode)]
	if ok {
		return &pricing, true
	}

	// Lookup in vertex if gemini not found
	if provider == string(schemas.Gemini) {
		s.logger.Debug("primary lookup failed, trying vertex provider for the same model")
		pricing, ok = s.pricingData[makeKey(model, "vertex", mode)]
		if ok {
			return &pricing, true
		}

		// Lookup in the counterpart chat/responses mode if this model's row is filed under the other one
		if hasFallbackMode {
			s.logger.Debug("secondary lookup failed, trying vertex provider for the same model in %s mode", fallbackMode)
			pricing, ok = s.pricingData[makeKey(model, "vertex", fallbackMode)]
			if ok {
				return &pricing, true
			}
		}
	}

	if provider == string(schemas.Vertex) {
		// Vertex models can be of the form "provider/model", so try to lookup the model without the provider prefix and keep the original provider
		if strings.Contains(model, "/") {
			modelWithoutProvider := strings.SplitN(model, "/", 2)[1]
			s.logger.Debug("primary lookup failed, trying vertex provider for the same model with provider/model format %s", modelWithoutProvider)
			pricing, ok = s.pricingData[makeKey(modelWithoutProvider, "vertex", mode)]
			if ok {
				return &pricing, true
			}

			// Lookup in the counterpart chat/responses mode if this model's row is filed under the other one
			if hasFallbackMode {
				s.logger.Debug("secondary lookup failed, trying vertex provider for the same model in %s mode", fallbackMode)
				pricing, ok = s.pricingData[makeKey(modelWithoutProvider, "vertex", fallbackMode)]
				if ok {
					return &pricing, true
				}
			}
		}
	}

	if provider == string(schemas.Bedrock) {
		// Bedrock model IDs carry a vendor namespace ("anthropic.claude-*",
		// "openai.gpt-oss-*", "google.gemma-*", "xai.grok-*"). When the caller
		// sends the bare model name, retry with the namespace inferred from
		// the model family.
		var vendorPrefix string
		switch {
		case !strings.Contains(model, "anthropic.") && schemas.IsAnthropicModel(model):
			vendorPrefix = "anthropic."
		case !strings.Contains(model, "openai.") && schemas.IsOpenAIModel(model):
			vendorPrefix = "openai."
		case !strings.Contains(model, "google.") && (schemas.IsGemmaModel(model) || schemas.IsGeminiModel(model)):
			vendorPrefix = "google."
		case !strings.Contains(model, "xai.") && schemas.IsGrokModel(model):
			vendorPrefix = "xai."
		}
		if vendorPrefix != "" {
			s.logger.Debug("primary lookup failed, trying with %s prefix for the same model", vendorPrefix)
			pricing, ok = s.pricingData[makeKey(vendorPrefix+model, provider, mode)]
			if ok {
				return &pricing, true
			}

			// Lookup in the counterpart chat/responses mode if this model's row is filed under the other one
			if hasFallbackMode {
				s.logger.Debug("secondary lookup failed, trying the same prefixed model in %s mode", fallbackMode)
				pricing, ok = s.pricingData[makeKey(vendorPrefix+model, provider, fallbackMode)]
				if ok {
					return &pricing, true
				}
			}
		}
	}

	// Lookup in the counterpart chat/responses mode if this model's row is filed under the other one
	if hasFallbackMode {
		s.logger.Debug("primary lookup failed, trying the same model in %s mode", fallbackMode)
		pricing, ok = s.pricingData[makeKey(model, provider, fallbackMode)]
		if ok {
			return &pricing, true
		}
	}

	// Lookup in image generation if image edit not found
	if requestType == schemas.ImageEditRequest ||
		requestType == schemas.ImageEditStreamRequest ||
		requestType == schemas.ImageVariationRequest {
		s.logger.Debug("primary lookup failed, trying image generation provider for the same model")
		pricing, ok = s.pricingData[makeKey(model, provider, normalizeRequestType(schemas.ImageGenerationRequest))]
		if ok {
			return &pricing, true
		}
	}

	// Lookup fallback chain for container_create:
	// 1. Try chat mode for the same model (e.g. "container-1g" in chat mode)
	// 2. Try the base "container" model in chat mode (default rate when no memory-specific entry exists)
	if requestType == schemas.ContainerCreateRequest {
		s.logger.Debug("primary lookup failed, trying chat mode for container create pricing")
		pricing, ok = s.pricingData[makeKey(model, provider, normalizeRequestType(schemas.ChatCompletionRequest))]
		if ok {
			return &pricing, true
		}
		if model != "container" {
			s.logger.Debug("memory-specific container pricing not found, falling back to base container entry")
			pricing, ok = s.pricingData[makeKey("container", provider, normalizeRequestType(schemas.ChatCompletionRequest))]
			if ok {
				return &pricing, true
			}
		}
	}

	return nil, false
}

// UpsertModelPricingAttributes writes the additional_attributes column for
// every pricing row that matches (model, provider), then reloads the pricing
// cache so the new values are immediately visible to list-models. Returns
// the number of rows updated (0 = no such pricing row, which callers must
// surface as a validation error). An empty/nil attrs map clears the column.
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

// detectPassthroughRequestType maps a provider + stripped path to a RequestType.
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
		case strings.HasSuffix(path, "/responses/compact"):
			return schemas.CompactionRequest
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
		// Interactions API paths carry no colon action suffix.
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

// inferPassthroughRequestType determines the request type from usage fields (primary)
// and falls back to path detection for text/embedding/responses where LLMUsage is ambiguous.
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

// passthroughUsageToCostInput converts BifrostPassthroughUsage into costInput.
func passthroughUsageToCostInput(su *schemas.BifrostPassthroughUsage) costInput {
	var input costInput
	if su.LLMUsage != nil {
		input.usage = su.LLMUsage
	}
	input.tier = tierFromResponse(su.ServiceTier, su.Speed, su.InferenceGeo)
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
