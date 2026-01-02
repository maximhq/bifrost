package modelcatalog

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// CalculateCost calculates the cost of a Bifrost response
func (mc *ModelCatalog) CalculateCost(result *schemas.BifrostResponse) float64 {
	if result == nil {
		return 0.0
	}

	var usage *schemas.BifrostLLMUsage
	var audioSeconds *int
	var audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails

	//TODO: Detect batch operations
	isBatch := false

	switch {
	case result.TextCompletionResponse != nil && result.TextCompletionResponse.Usage != nil:
		usage = result.TextCompletionResponse.Usage
	case result.ChatResponse != nil && result.ChatResponse.Usage != nil:
		usage = result.ChatResponse.Usage
	case result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil:
		usage = &schemas.BifrostLLMUsage{
			PromptTokens:     result.ResponsesResponse.Usage.InputTokens,
			CompletionTokens: result.ResponsesResponse.Usage.OutputTokens,
			TotalTokens:      result.ResponsesResponse.Usage.TotalTokens,
		}
	case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil && result.ResponsesStreamResponse.Response.Usage != nil:
		usage = &schemas.BifrostLLMUsage{
			PromptTokens:     result.ResponsesStreamResponse.Response.Usage.InputTokens,
			CompletionTokens: result.ResponsesStreamResponse.Response.Usage.OutputTokens,
			TotalTokens:      result.ResponsesStreamResponse.Response.Usage.TotalTokens,
		}
	case result.EmbeddingResponse != nil && result.EmbeddingResponse.Usage != nil:
		usage = result.EmbeddingResponse.Usage
	case result.SpeechResponse != nil:
		if result.SpeechResponse.Usage != nil {
			usage = &schemas.BifrostLLMUsage{
				PromptTokens:     result.SpeechResponse.Usage.InputTokens,
				CompletionTokens: result.SpeechResponse.Usage.OutputTokens,
				TotalTokens:      result.SpeechResponse.Usage.TotalTokens,
			}
		} else {
			return 0
		}
	case result.SpeechStreamResponse != nil && result.SpeechStreamResponse.Usage != nil:
		usage = &schemas.BifrostLLMUsage{
			PromptTokens:     result.SpeechStreamResponse.Usage.InputTokens,
			CompletionTokens: result.SpeechStreamResponse.Usage.OutputTokens,
			TotalTokens:      result.SpeechStreamResponse.Usage.TotalTokens,
		}
	case result.TranscriptionResponse != nil && result.TranscriptionResponse.Usage != nil:
		usage = &schemas.BifrostLLMUsage{}
		if result.TranscriptionResponse.Usage.InputTokens != nil {
			usage.PromptTokens = *result.TranscriptionResponse.Usage.InputTokens
		}
		if result.TranscriptionResponse.Usage.OutputTokens != nil {
			usage.CompletionTokens = *result.TranscriptionResponse.Usage.OutputTokens
		}
		if result.TranscriptionResponse.Usage.TotalTokens != nil {
			usage.TotalTokens = *result.TranscriptionResponse.Usage.TotalTokens
		} else {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
		if result.TranscriptionResponse.Usage.InputTokenDetails != nil {
			audioTokenDetails = &schemas.TranscriptionUsageInputTokenDetails{}
			audioTokenDetails.AudioTokens = result.TranscriptionResponse.Usage.InputTokenDetails.AudioTokens
			audioTokenDetails.TextTokens = result.TranscriptionResponse.Usage.InputTokenDetails.TextTokens
		}
		if result.TranscriptionResponse.Usage.Seconds != nil {
			audioSeconds = result.TranscriptionResponse.Usage.Seconds
		}
	case result.TranscriptionStreamResponse != nil && result.TranscriptionStreamResponse.Usage != nil:
		usage = &schemas.BifrostLLMUsage{}
		if result.TranscriptionStreamResponse.Usage.InputTokens != nil {
			usage.PromptTokens = *result.TranscriptionStreamResponse.Usage.InputTokens
		}
		if result.TranscriptionStreamResponse.Usage.OutputTokens != nil {
			usage.CompletionTokens = *result.TranscriptionStreamResponse.Usage.OutputTokens
		}
		if result.TranscriptionStreamResponse.Usage.TotalTokens != nil {
			usage.TotalTokens = *result.TranscriptionStreamResponse.Usage.TotalTokens
		} else {
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
		if result.TranscriptionStreamResponse.Usage.InputTokenDetails != nil {
			audioTokenDetails = &schemas.TranscriptionUsageInputTokenDetails{}
			audioTokenDetails.AudioTokens = result.TranscriptionStreamResponse.Usage.InputTokenDetails.AudioTokens
			audioTokenDetails.TextTokens = result.TranscriptionStreamResponse.Usage.InputTokenDetails.TextTokens
		}
		if result.TranscriptionStreamResponse.Usage.Seconds != nil {
			audioSeconds = result.TranscriptionStreamResponse.Usage.Seconds
		}
	default:
		return 0
	}

	cost := 0.0
	if usage != nil || audioSeconds != nil || audioTokenDetails != nil {
		extraFields := result.GetExtraFields()
		cost = mc.CalculateCostFromUsage(string(extraFields.Provider), extraFields.ModelRequested, extraFields.ModelDeployment, usage, extraFields.RequestType, isBatch, audioSeconds, audioTokenDetails)
	}

	return cost
}

// CalculateCostWithCacheDebug calculates the cost of a Bifrost response with cache debug information
func (mc *ModelCatalog) CalculateCostWithCacheDebug(result *schemas.BifrostResponse) float64 {
	if result == nil {
		return 0.0
	}
	cacheDebug := result.GetExtraFields().CacheDebug
	if cacheDebug != nil {
		if cacheDebug.CacheHit {
			if cacheDebug.HitType != nil && *cacheDebug.HitType == "direct" {
				return 0
			} else if cacheDebug.ProviderUsed != nil && cacheDebug.ModelUsed != nil && cacheDebug.InputTokens != nil {
				return mc.CalculateCostFromUsage(*cacheDebug.ProviderUsed, *cacheDebug.ModelUsed, "", &schemas.BifrostLLMUsage{
					PromptTokens:     *cacheDebug.InputTokens,
					CompletionTokens: 0,
					TotalTokens:      *cacheDebug.InputTokens,
				}, schemas.EmbeddingRequest, false, nil, nil)
			}

			// Don't over-bill cache hits if fields are missing.
			return 0
		} else {
			baseCost := mc.CalculateCost(result)
			var semanticCacheCost float64
			if cacheDebug.ProviderUsed != nil && cacheDebug.ModelUsed != nil && cacheDebug.InputTokens != nil {
				semanticCacheCost = mc.CalculateCostFromUsage(*cacheDebug.ProviderUsed, *cacheDebug.ModelUsed, "", &schemas.BifrostLLMUsage{
					PromptTokens:     *cacheDebug.InputTokens,
					CompletionTokens: 0,
					TotalTokens:      *cacheDebug.InputTokens,
				}, schemas.EmbeddingRequest, false, nil, nil)
			}

			return baseCost + semanticCacheCost
		}
	}

	return mc.CalculateCost(result)
}

// CalculateCostFromUsage calculates cost in dollars using pricing manager and usage data with conditional pricing
func (mc *ModelCatalog) CalculateCostFromUsage(provider string, model string, deployment string, usage *schemas.BifrostLLMUsage, requestType schemas.RequestType, isBatch bool, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails) float64 {
	// Allow audio-only flows by only returning early if we have no usage data at all
	if usage == nil && audioSeconds == nil && audioTokenDetails == nil {
		return 0.0
	}

	if usage.Cost != nil && usage.Cost.TotalCost > 0 {
		return usage.Cost.TotalCost
	}

	mc.logger.Debug("looking up pricing for model %s and provider %s of request type %s", model, provider, normalizeRequestType(requestType))
	// Get pricing for the model
	pricing, exists := mc.getPricing(model, provider, requestType)
	if !exists {
		if deployment != "" {
			mc.logger.Debug("pricing not found for model %s and provider %s of request type %s, trying with deployment %s", model, provider, normalizeRequestType(requestType), deployment)
			pricing, exists = mc.getPricing(deployment, provider, requestType)
			if !exists {
				mc.logger.Debug("pricing not found for deployment %s and provider %s of request type %s, skipping cost calculation", deployment, provider, normalizeRequestType(requestType))
				return 0.0
			}
		} else {
			mc.logger.Debug("pricing not found for model %s and provider %s of request type %s, skipping cost calculation", model, provider, normalizeRequestType(requestType))
			return 0.0
		}
	}

	var inputCost, outputCost float64

	// Helper function to safely get token counts with zero defaults
	safeTokenCount := func(usage *schemas.BifrostLLMUsage, getter func(*schemas.BifrostLLMUsage) int) int {
		if usage == nil {
			return 0
		}
		return getter(usage)
	}

	totalTokens := safeTokenCount(usage, func(u *schemas.BifrostLLMUsage) int { return u.TotalTokens })
	promptTokens := safeTokenCount(usage, func(u *schemas.BifrostLLMUsage) int {
		return u.PromptTokens
	})
	completionTokens := safeTokenCount(usage, func(u *schemas.BifrostLLMUsage) int {
		return u.CompletionTokens
	})
	cachedPromptTokens := safeTokenCount(usage, func(u *schemas.BifrostLLMUsage) int {
		if u.PromptTokensDetails != nil {
			return u.PromptTokensDetails.CachedTokens
		}
		return 0
	})
	cachedCompletionTokens := safeTokenCount(usage, func(u *schemas.BifrostLLMUsage) int {
		if u.CompletionTokensDetails != nil {
			return u.CompletionTokensDetails.CachedTokens
		}
		return 0
	})

	// Special handling for audio operations with duration-based pricing
	if (requestType == schemas.SpeechRequest || requestType == schemas.TranscriptionRequest) && audioSeconds != nil && *audioSeconds > 0 {
		// Determine if this is above TokenTierAbove128K for pricing tier selection
		isAbove128k := totalTokens > TokenTierAbove128K

		// Use duration-based pricing for audio when available
		var audioPerSecondRate *float64
		if isAbove128k && pricing.InputCostPerAudioPerSecondAbove128kTokens != nil {
			audioPerSecondRate = pricing.InputCostPerAudioPerSecondAbove128kTokens
		} else if pricing.InputCostPerAudioPerSecond != nil {
			audioPerSecondRate = pricing.InputCostPerAudioPerSecond
		}

		if audioPerSecondRate != nil {
			inputCost = float64(*audioSeconds) * *audioPerSecondRate
		} else {
			// Fall back to token-based pricing
			inputCost = float64(promptTokens) * pricing.InputCostPerToken
		}

		// For audio operations, output cost is typically based on tokens (if any)
		outputCost = float64(completionTokens) * pricing.OutputCostPerToken

		return inputCost + outputCost
	}

	// Handle audio token details if available (for token-based audio pricing)
	if audioTokenDetails != nil && (requestType == schemas.SpeechRequest || requestType == schemas.TranscriptionRequest) {
		// Use audio-specific token pricing if available
		audioTokens := float64(audioTokenDetails.AudioTokens)
		textTokens := float64(audioTokenDetails.TextTokens)
		isAbove200k := totalTokens > TokenTierAbove200K
		isAbove128k := totalTokens > TokenTierAbove128K

		// Determine the appropriate token pricing rates
		var inputTokenRate, outputTokenRate float64

		if isAbove200k {
			inputTokenRate = getSafeFloat64(pricing.InputCostPerTokenAbove200kTokens, pricing.InputCostPerToken)
			outputTokenRate = getSafeFloat64(pricing.OutputCostPerTokenAbove200kTokens, pricing.OutputCostPerToken)
		} else if isAbove128k {
			inputTokenRate = getSafeFloat64(pricing.InputCostPerTokenAbove128kTokens, pricing.InputCostPerToken)
			outputTokenRate = getSafeFloat64(pricing.OutputCostPerTokenAbove128kTokens, pricing.OutputCostPerToken)
		} else {
			inputTokenRate = pricing.InputCostPerToken
			outputTokenRate = pricing.OutputCostPerToken
		}

		// Calculate costs using token-based pricing with audio/text breakdown
		inputCost = audioTokens*inputTokenRate + textTokens*inputTokenRate
		outputCost = float64(completionTokens) * outputTokenRate

		return inputCost + outputCost
	}

	// Use conditional pricing based on request characteristics
	if isBatch {
		// Use batch pricing if available, otherwise fall back to regular pricing
		if pricing.InputCostPerTokenBatches != nil {
			inputCost = float64(promptTokens) * *pricing.InputCostPerTokenBatches
		} else {
			inputCost = float64(promptTokens) * pricing.InputCostPerToken
		}

		if pricing.OutputCostPerTokenBatches != nil {
			outputCost = float64(completionTokens) * *pricing.OutputCostPerTokenBatches
		} else {
			outputCost = float64(completionTokens) * pricing.OutputCostPerToken
		}
	} else {
		// Use regular pricing
		inputCost = float64(promptTokens-cachedPromptTokens) * pricing.InputCostPerToken
		if pricing.CacheReadInputTokenCost != nil {
			inputCost += float64(cachedPromptTokens) * *pricing.CacheReadInputTokenCost
		}
		outputCost = float64(completionTokens-cachedCompletionTokens) * pricing.OutputCostPerToken
		if pricing.CacheCreationInputTokenCost != nil {
			outputCost += float64(cachedCompletionTokens) * *pricing.CacheCreationInputTokenCost
		}
	}

	totalCost := inputCost + outputCost

	return totalCost
}

// getPricing returns pricing information for a model (thread-safe)
func (mc *ModelCatalog) getPricing(model, provider string, requestType schemas.RequestType) (*configstoreTables.TableModelPricing, bool) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	pricing, ok := mc.pricingData[makeKey(model, provider, normalizeRequestType(requestType))]
	if !ok {
		// Lookup in vertex if gemini not found
		if provider == string(schemas.Gemini) {
			mc.logger.Debug("primary lookup failed, trying vertex provider for the same model")
			pricing, ok = mc.pricingData[makeKey(model, "vertex", normalizeRequestType(requestType))]
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
				pricing, ok = mc.pricingData[makeKey(modelWithoutProvider, "vertex", normalizeRequestType(requestType))]
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
				pricing, ok = mc.pricingData[makeKey("anthropic."+model, provider, normalizeRequestType(requestType))]
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
	return &pricing, true
}
