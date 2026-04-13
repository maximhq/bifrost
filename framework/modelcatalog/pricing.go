package modelcatalog

import (
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// Default sync interval and config key
const (
	TokenTierAbove272K = 272000
	TokenTierAbove200K = 200000
	TokenTierAbove128K = 128000
)

// PricingEntry represents a single model's pricing information.
// Field names and JSON tags match the datasheet schema exactly.
type PricingEntry struct {
	BaseModel string `json:"base_model,omitempty"`
	Provider  string `json:"provider"`
	Mode      string `json:"mode"`

	ContextLength   *int                  `json:"context_length,omitempty"`
	MaxInputTokens  *int                  `json:"max_input_tokens,omitempty"`
	MaxOutputTokens *int                  `json:"max_output_tokens,omitempty"`
	Architecture    *schemas.Architecture `json:"architecture,omitempty"`

	PricingOptions
}

// UnmarshalJSON implements json.Unmarshaler for PricingEntry.
// It handles the special case where search_context_cost_per_query may arrive as either
// a plain float64 or a tiered object {"search_context_size_low":…,
// "search_context_size_medium":…, "search_context_size_high":…}.
func (p *PricingEntry) UnmarshalJSON(data []byte) error {
	// Type alias breaks the UnmarshalJSON recursion while keeping all other fields.
	type PricingEntryAlias PricingEntry
	var raw struct {
		PricingEntryAlias
		SearchContextCostPerQuery *struct {
			Low    *float64 `json:"search_context_size_low"`
			Medium *float64 `json:"search_context_size_medium"`
			High   *float64 `json:"search_context_size_high"`
		} `json:"search_context_cost_per_query,omitempty"`
	}
	if err := sonic.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = PricingEntry(raw.PricingEntryAlias)

	// search_context_cost_per_query arrives as a tiered object – all three values are
	// equal for non-Perplexity providers; we prefer medium, then low, then high.
	// Perplexity always returns a pre-computed total_cost so the per-query rate is
	// never consumed for that provider.
	if q := raw.SearchContextCostPerQuery; q != nil {
		switch {
		case q.Medium != nil:
			p.SearchContextCostPerQuery = q.Medium
		case q.Low != nil:
			p.SearchContextCostPerQuery = q.Low
		case q.High != nil:
			p.SearchContextCostPerQuery = q.High
		}
	}
	return nil
}

type PricingOptions struct {
	// Costs - Text
	InputCostPerToken          *float64 `json:"input_cost_per_token,omitempty"`
	OutputCostPerToken         *float64 `json:"output_cost_per_token,omitempty"`
	InputCostPerTokenBatches   *float64 `json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches  *float64 `json:"output_cost_per_token_batches,omitempty"`
	InputCostPerTokenPriority  *float64 `json:"input_cost_per_token_priority,omitempty"`
	OutputCostPerTokenPriority *float64 `json:"output_cost_per_token_priority,omitempty"`
	InputCostPerTokenFlex      *float64 `json:"input_cost_per_token_flex,omitempty"`
	OutputCostPerTokenFlex     *float64 `json:"output_cost_per_token_flex,omitempty"`
	InputCostPerCharacter      *float64 `json:"input_cost_per_character,omitempty"`
	// Costs - 128k Tier
	InputCostPerTokenAbove128kTokens          *float64 `json:"input_cost_per_token_above_128k_tokens,omitempty"`
	InputCostPerImageAbove128kTokens          *float64 `json:"input_cost_per_image_above_128k_tokens,omitempty"`
	InputCostPerVideoPerSecondAbove128kTokens *float64 `json:"input_cost_per_video_per_second_above_128k_tokens,omitempty"`
	InputCostPerAudioPerSecondAbove128kTokens *float64 `json:"input_cost_per_audio_per_second_above_128k_tokens,omitempty"`
	OutputCostPerTokenAbove128kTokens         *float64 `json:"output_cost_per_token_above_128k_tokens,omitempty"`
	// Costs - 200k Tier
	InputCostPerTokenAbove200kTokens          *float64 `json:"input_cost_per_token_above_200k_tokens,omitempty"`
	InputCostPerTokenAbove200kTokensPriority  *float64 `json:"input_cost_per_token_above_200k_tokens_priority,omitempty"`
	OutputCostPerTokenAbove200kTokens         *float64 `json:"output_cost_per_token_above_200k_tokens,omitempty"`
	OutputCostPerTokenAbove200kTokensPriority *float64 `json:"output_cost_per_token_above_200k_tokens_priority,omitempty"`
	// Costs - 272k Tier
	InputCostPerTokenAbove272kTokens          *float64 `json:"input_cost_per_token_above_272k_tokens,omitempty"`
	InputCostPerTokenAbove272kTokensPriority  *float64 `json:"input_cost_per_token_above_272k_tokens_priority,omitempty"`
	OutputCostPerTokenAbove272kTokens         *float64 `json:"output_cost_per_token_above_272k_tokens,omitempty"`
	OutputCostPerTokenAbove272kTokensPriority *float64 `json:"output_cost_per_token_above_272k_tokens_priority,omitempty"`

	// Costs - Cache
	CacheCreationInputTokenCost                        *float64 `json:"cache_creation_input_token_cost,omitempty"`
	CacheReadInputTokenCost                            *float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheCreationInputTokenCostAbove200kTokens         *float64 `json:"cache_creation_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokens             *float64 `json:"cache_read_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokensPriority     *float64 `json:"cache_read_input_token_cost_above_200k_tokens_priority,omitempty"`
	CacheCreationInputTokenCostAbove1hr                *float64 `json:"cache_creation_input_token_cost_above_1hr,omitempty"`
	CacheCreationInputTokenCostAbove1hrAbove200kTokens *float64 `json:"cache_creation_input_token_cost_above_1hr_above_200k_tokens,omitempty"`
	CacheCreationInputAudioTokenCost                   *float64 `json:"cache_creation_input_audio_token_cost,omitempty"`
	CacheReadInputTokenCostPriority                    *float64 `json:"cache_read_input_token_cost_priority,omitempty"`
	CacheReadInputTokenCostFlex                        *float64 `json:"cache_read_input_token_cost_flex,omitempty"`
	CacheReadInputImageTokenCost                       *float64 `json:"cache_read_input_image_token_cost,omitempty"`
	CacheReadInputTokenCostAbove272kTokens             *float64 `json:"cache_read_input_token_cost_above_272k_tokens,omitempty"`
	CacheReadInputTokenCostAbove272kTokensPriority     *float64 `json:"cache_read_input_token_cost_above_272k_tokens_priority,omitempty"`

	// Costs - Image
	InputCostPerImage                             *float64 `json:"input_cost_per_image,omitempty"`
	InputCostPerPixel                             *float64 `json:"input_cost_per_pixel,omitempty"`
	OutputCostPerImage                            *float64 `json:"output_cost_per_image,omitempty"`
	OutputCostPerPixel                            *float64 `json:"output_cost_per_pixel,omitempty"`
	OutputCostPerImagePremiumImage                *float64 `json:"output_cost_per_image_premium_image,omitempty"`
	OutputCostPerImageAbove512x512Pixels          *float64 `json:"output_cost_per_image_above_512_and_512_pixels,omitempty"`
	OutputCostPerImageAbove512x512PixelsPremium   *float64 `json:"output_cost_per_image_above_512_and_512_pixels_and_premium_image,omitempty"`
	OutputCostPerImageAbove1024x1024Pixels        *float64 `json:"output_cost_per_image_above_1024_and_1024_pixels,omitempty"`
	OutputCostPerImageAbove1024x1024PixelsPremium *float64 `json:"output_cost_per_image_above_1024_and_1024_pixels_and_premium_image,omitempty"`
	OutputCostPerImageAbove2048x2048Pixels        *float64 `json:"output_cost_per_image_above_2048_and_2048_pixels,omitempty"`
	OutputCostPerImageAbove4096x4096Pixels        *float64 `json:"output_cost_per_image_above_4096_and_4096_pixels,omitempty"`
	OutputCostPerImageLowQuality                  *float64 `json:"output_cost_per_image_low_quality,omitempty"`
	OutputCostPerImageMediumQuality               *float64 `json:"output_cost_per_image_medium_quality,omitempty"`
	OutputCostPerImageHighQuality                 *float64 `json:"output_cost_per_image_high_quality,omitempty"`
	OutputCostPerImageAutoQuality                 *float64 `json:"output_cost_per_image_auto_quality,omitempty"`
	InputCostPerImageToken                        *float64 `json:"input_cost_per_image_token,omitempty"`
	OutputCostPerImageToken                       *float64 `json:"output_cost_per_image_token,omitempty"`

	// Costs - Audio/Video
	InputCostPerAudioToken      *float64 `json:"input_cost_per_audio_token,omitempty"`
	InputCostPerAudioPerSecond  *float64 `json:"input_cost_per_audio_per_second,omitempty"`
	InputCostPerSecond          *float64 `json:"input_cost_per_second,omitempty"`
	InputCostPerVideoPerSecond  *float64 `json:"input_cost_per_video_per_second,omitempty"`
	OutputCostPerAudioToken     *float64 `json:"output_cost_per_audio_token,omitempty"`
	OutputCostPerVideoPerSecond *float64 `json:"output_cost_per_video_per_second,omitempty"`
	OutputCostPerSecond         *float64 `json:"output_cost_per_second,omitempty"`

	// Costs - Other
	//
	// SearchContextCostPerQuery is stored as a single float64, but the pricing datasheet
	// represents it as a tiered object with three keys: search_context_size_low,
	// search_context_size_medium, and search_context_size_high.  For every provider except
	// Perplexity the three tier values are identical, so we collapse the object to its
	// medium tier value (falling back to low then high).  Perplexity always returns a
	// pre-computed total_cost in its usage response, so the per-query rate is never
	// consumed for that provider; the collapsed value is therefore correct in all cases.
	// See UnmarshalJSON below for the custom decoding logic.
	SearchContextCostPerQuery     *float64 `json:"search_context_cost_per_query,omitempty"`
	CodeInterpreterCostPerSession *float64 `json:"code_interpreter_cost_per_session,omitempty"`
}

// serviceTier captures the OpenAI service_tier value from a response.
// Add new tier flags here as OpenAI introduces them.
type serviceTier struct {
	isPriority bool // true when service_tier == "priority"
	isFlex     bool // true when service_tier == "flex"
}

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
	tier                serviceTier
}

// GetPricingEntryForModel returns the pricing data
func (mc *ModelCatalog) GetPricingEntryForModel(model string, provider schemas.ModelProvider) *PricingEntry {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	// Check all modes
	for _, mode := range []schemas.RequestType{
		schemas.TextCompletionRequest,
		schemas.ChatCompletionRequest,
		schemas.ResponsesRequest,
		schemas.EmbeddingRequest,
		schemas.RerankRequest,
		schemas.SpeechRequest,
		schemas.TranscriptionRequest,
		schemas.ImageGenerationRequest,
		schemas.ImageEditRequest,
		schemas.ImageVariationRequest,
		schemas.VideoGenerationRequest,
	} {
		key := makeKey(model, string(provider), normalizeRequestType(mode))
		pricing, ok := mc.pricingData[key]
		if ok {
			return convertTableModelPricingToPricingData(&pricing)
		}
	}
	return nil
}

// CalculateCost calculates the cost of a Bifrost response.
// It handles all request types, cache debug billing, and tiered pricing.
// If scopes is nil, an empty PricingLookupScopes is used; global and provider-scoped
// overrides may still apply since the provider is derived from the response.
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
	return float64(*cacheDebug.InputTokens) * tieredInputRate(pricing, *cacheDebug.InputTokens, serviceTier{})
}

// calculateBaseCost extracts usage from the response and routes to the appropriate compute function.
func (mc *ModelCatalog) calculateBaseCost(result *schemas.BifrostResponse, scopes PricingLookupScopes) float64 {
	extraFields := result.GetExtraFields()
	if extraFields == nil {
		return 0
	}

	provider := string(extraFields.Provider)
	originalModelRequested := extraFields.OriginalModelRequested
	resolvedModelUsed := extraFields.ResolvedModelUsed
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
	pricing := mc.resolvePricing(provider, originalModelRequested, resolvedModelUsed, requestType, scopes)
	if pricing == nil {
		return 0
	}

	// Route to the appropriate compute function
	switch requestType {
	case schemas.ChatCompletionRequest, schemas.TextCompletionRequest, schemas.ResponsesRequest:
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
func computeTextCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) float64 {
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

	inputRate := tieredInputRate(pricing, totalTokens, tier)
	outputRate := tieredOutputRate(pricing, totalTokens, tier)
	cacheReadInputRate := tieredCacheReadInputTokenRate(pricing, totalTokens, tier)
	cacheCreationInputRate := tieredCacheCreationInputTokenRate(pricing, totalTokens, tier)

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
func computeEmbeddingCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) float64 {
	if usage == nil {
		return 0
	}
	return float64(usage.PromptTokens) * tieredInputRate(pricing, usage.TotalTokens, tier)
}

// computeRerankCost handles rerank requests.
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

// computeSpeechCost handles speech (TTS) requests.
// Input is text (PromptTokens), output is audio (CompletionTokens).
//
// Per-character pricing (InputCostPerCharacter) is used as first-class support for TTS/audio
// models — providers such as OpenAI TTS, ElevenLabs, and AWS Polly bill per character of
// input text rather than per token. PromptTokens from usage is treated as the character count
// since TTS providers report their billable unit in that field.
// Output falls back to per-second duration when no audio token rate is configured.
func computeSpeechCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTextInputChars int, tier serviceTier) float64 {
	totalTokens := safeTotalTokens(usage)

	// Input: per-character rate takes precedence for TTS/audio models
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

	// Output: audio tokens first, then per-second fallback
	outputCost := computeAudioOutputCost(pricing, usage, audioSeconds, totalTokens, tier)

	return inputCost + outputCost
}

// computeTranscriptionCost handles transcription (STT) requests.
// Input is audio, output is text (CompletionTokens).
// Input and output are calculated independently — tokens first, then per-second fallback.
func computeTranscriptionCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails, tier serviceTier) float64 {
	totalTokens := safeTotalTokens(usage)

	// Input: audio tokens/details first, then per-second fallback
	inputCost := computeAudioInputCost(pricing, usage, audioSeconds, audioTokenDetails, totalTokens, tier)

	// Output: text tokens
	outputCost := 0.0
	if usage != nil && usage.CompletionTokens > 0 {
		outputCost = float64(usage.CompletionTokens) * tieredOutputRate(pricing, totalTokens, tier)
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

	totalTokens := imageUsage.TotalTokens
	pixels := parseImagePixels(imageSize)
	inputCost := computeImageInputCost(pricing, imageUsage, totalTokens, pixels, tier)
	outputCost := computeImageOutputCost(pricing, imageUsage, totalTokens, pixels, imageQuality, tier)

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
	totalTokens := safeTotalTokens(usage)

	// Input: text prompt tokens first, then per-second fallback
	inputCost := 0.0
	if usage != nil && usage.PromptTokens > 0 {
		inputCost = float64(usage.PromptTokens) * tieredInputRate(pricing, totalTokens, tier)
	} else if videoSeconds != nil && *videoSeconds > 0 {
		if rate := tieredVideoInputPerSecondRate(pricing, totalTokens); rate > 0 {
			inputCost = float64(*videoSeconds) * rate
		}
	}

	// Output: completion tokens first, then per-second fallback
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// tierFromString constructs a serviceTier from an OpenAI service_tier response value.
func tierFromString(s *string) serviceTier {
	if s == nil {
		return serviceTier{}
	}
	switch *s {
	case "priority":
		return serviceTier{isPriority: true}
	case "flex":
		return serviceTier{isFlex: true}
	default:
		return serviceTier{}
	}
}

// tieredInputRate returns the effective per-token input rate based on total token count.
// Flex applies a flat rate. Priority-specific tier rates are preferred where available.
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

// tieredOutputRate returns the effective per-token output rate based on total token count.
// Flex applies a flat rate. Priority-specific tier rates are preferred where available.
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

// Note: flex tier is not checked here because cache creation is not a concept in
// OpenAI's pricing model (the only provider that uses flex tier). Only cache read
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
func (mc *ModelCatalog) resolvePricing(provider, originalModelRequested, resolvedModelUsed string, requestType schemas.RequestType, scopes PricingLookupScopes) *configstoreTables.TableModelPricing {
	if resolvedModelUsed == "" {
		resolvedModelUsed = originalModelRequested
	}
	mc.logger.Debug("looking up pricing for resolved model %s and provider %s of request type %s", resolvedModelUsed, provider, normalizeRequestType(requestType))

	if scopes.Provider == "" {
		scopes.Provider = provider
	}

	base, exists := mc.getBasePricing(resolvedModelUsed, provider, requestType)
	if exists && base != nil {
		result, _ := mc.applyPricingOverrides(resolvedModelUsed, requestType, *base, scopes)
		return &result
	}

	mc.logger.Debug("pricing not found for resolved model %s, trying alias %s", resolvedModelUsed, originalModelRequested)
	base, exists = mc.getBasePricing(originalModelRequested, provider, requestType)
	if exists && base != nil {
		// Apply overrides using the resolved model name, not the alias
		result, _ := mc.applyPricingOverrides(resolvedModelUsed, requestType, *base, scopes)
		return &result
	}

	// No base catalog entry found; still try overrides in case the user defined
	// override-only pricing for a model not in the built-in catalog.
	mc.logger.Debug("pricing not found for resolved model %s and provider %s, trying override-only pricing", resolvedModelUsed, provider)
	result, applied := mc.applyPricingOverrides(resolvedModelUsed, requestType, configstoreTables.TableModelPricing{}, scopes)
	if applied {
		return &result
	}
	mc.logger.Debug("no pricing found for resolved model %s and provider %s, skipping cost calculation", resolvedModelUsed, provider)
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
func (mc *ModelCatalog) getBasePricing(model, provider string, requestType schemas.RequestType) (*configstoreTables.TableModelPricing, bool) {
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
		if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest || requestType == schemas.RealtimeRequest {
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
			if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest || requestType == schemas.RealtimeRequest {
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
			if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest || requestType == schemas.RealtimeRequest {
				mc.logger.Debug("secondary lookup failed, trying chat provider for the same model in chat completion")
				pricing, ok = mc.pricingData[makeKey("anthropic."+model, provider, normalizeRequestType(schemas.ChatCompletionRequest))]
				if ok {
					return &pricing, true
				}
			}
		}
	}

	// Lookup in chat if responses not found
	if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest || requestType == schemas.RealtimeRequest {
		mc.logger.Debug("primary lookup failed, trying chat provider for the same model in chat completion")
		pricing, ok = mc.pricingData[makeKey(model, provider, normalizeRequestType(schemas.ChatCompletionRequest))]
		if ok {
			return &pricing, true
		}
	}

	// Lookup in image generation if image edit not found
	if requestType == schemas.ImageEditRequest ||
		requestType == schemas.ImageEditStreamRequest ||
		requestType == schemas.ImageVariationRequest {
		mc.logger.Debug("primary lookup failed, trying image generation provider for the same model")
		pricing, ok = mc.pricingData[makeKey(model, provider, normalizeRequestType(schemas.ImageGenerationRequest))]
		if ok {
			return &pricing, true
		}
	}

	return nil, false
}
