package modelcatalog

import (
	"strconv"
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
	var imageUsage *schemas.ImageUsage
	var videoSeconds *int
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
			Cost:             result.ResponsesResponse.Usage.Cost,
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
	case result.ImageGenerationResponse != nil && result.ImageGenerationResponse.Usage != nil:
		imageUsage = result.ImageGenerationResponse.Usage
	case result.ImageGenerationStreamResponse != nil && result.ImageGenerationStreamResponse.Usage != nil:
		imageUsage = result.ImageGenerationStreamResponse.Usage
	case result.VideoGenerationResponse != nil && result.VideoGenerationResponse.Seconds != nil:
		seconds, err := strconv.Atoi(*result.VideoGenerationResponse.Seconds)
		if err != nil {
			mc.logger.Warn("failed to convert video seconds to int: %v", err)
			videoSeconds = nil
		} else {
			videoSeconds = &seconds
		}
	default:
		return 0
	}

	cost := 0.0
	if usage != nil || audioSeconds != nil || audioTokenDetails != nil || imageUsage != nil || videoSeconds != nil {
		extraFields := result.GetExtraFields()
		requestType := extraFields.RequestType
		// Normalize stream request types to their base request type for pricing
		// CalculateCostFromUsage treats ImageGenerationRequest as image pricing, so normalize stream requests
		// This ensures ImageGenerationStreamResponse is correctly priced as image generation
		if imageUsage != nil && requestType == schemas.ImageGenerationStreamRequest {
			requestType = schemas.ImageGenerationRequest
		}
		cost = mc.CalculateCostFromUsage(string(extraFields.Provider), extraFields.ModelRequested, extraFields.ModelDeployment, usage, requestType, isBatch, audioSeconds, audioTokenDetails, imageUsage, videoSeconds)
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
				}, schemas.EmbeddingRequest, false, nil, nil, nil, nil)
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
				}, schemas.EmbeddingRequest, false, nil, nil, nil, nil)
			}

			return baseCost + semanticCacheCost
		}
	}

	return mc.CalculateCost(result)
}

// CalculateCostFromUsage calculates cost in dollars using pricing manager and usage data with conditional pricing
func (mc *ModelCatalog) CalculateCostFromUsage(provider string, model string, deployment string, usage *schemas.BifrostLLMUsage, requestType schemas.RequestType, isBatch bool, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails, imageUsage *schemas.ImageUsage, videoSeconds *int) float64 {
	// Allow audio-only and image-only flows by only returning early if we have no usage data at all
	if usage == nil && audioSeconds == nil && audioTokenDetails == nil && imageUsage == nil && videoSeconds == nil {
		return 0.0
	}

	if usage != nil && usage.Cost != nil && usage.Cost.TotalCost > 0 {
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

	// Handle image generation if available (for token-based image generation pricing)
	if imageUsage != nil && requestType == schemas.ImageGenerationRequest {
		// Use imageUsage.TotalTokens for tier determination
		imageTotalTokens := imageUsage.TotalTokens

		// Check if tokens are zero/nil - if so, use per-image pricing
		if imageTotalTokens == 0 && imageUsage.InputTokens == 0 && imageUsage.OutputTokens == 0 {
			// Use per-image pricing when tokens are nil/zero
			// Extract number of images from ImageTokenDetails if available
			numImages := 1
			if imageUsage.OutputTokensDetails != nil && imageUsage.OutputTokensDetails.NImages > 0 {
				numImages = imageUsage.OutputTokensDetails.NImages
			} else if imageUsage.InputTokensDetails != nil && imageUsage.InputTokensDetails.NImages > 0 {
				numImages = imageUsage.InputTokensDetails.NImages
			}

			isAbove128k := imageTotalTokens > TokenTierAbove128K

			var inputPerImageRate, outputPerImageRate *float64
			if isAbove128k {
				inputPerImageRate = pricing.InputCostPerImageAbove128kTokens
				// Note: OutputCostPerImageAbove128kTokens may not exist in TableModelPricing
				// For now, use regular OutputCostPerImage even above 128k
			} else {
				inputPerImageRate = pricing.InputCostPerImage
			}
			// Use OutputCostPerImage if available
			outputPerImageRate = pricing.OutputCostPerImage

			// Calculate costs
			if inputPerImageRate != nil {
				inputCost = float64(numImages) * *inputPerImageRate
			}
			if outputPerImageRate != nil {
				outputCost = float64(numImages) * *outputPerImageRate
			} else {
				outputCost = 0.0
			}

			if inputPerImageRate != nil || outputPerImageRate != nil {
				return inputCost + outputCost
			}
			// Fall through to token-based pricing if per-image pricing is not available
		}

		// Use token-based pricing when tokens are present
		isAbove200k := imageTotalTokens > TokenTierAbove200K
		isAbove128k := imageTotalTokens > TokenTierAbove128K

		// Extract token counts with breakdown if available
		var inputImageTokens, inputTextTokens, outputImageTokens, outputTextTokens int

		if imageUsage.InputTokensDetails != nil {
			inputImageTokens = imageUsage.InputTokensDetails.ImageTokens
			inputTextTokens = imageUsage.InputTokensDetails.TextTokens
		} else {
			// If no details, InputTokens is text tokens (per comment in ImageUsage)
			inputTextTokens = imageUsage.InputTokens
		}

		if imageUsage.OutputTokensDetails != nil {
			outputImageTokens = imageUsage.OutputTokensDetails.ImageTokens
			outputTextTokens = imageUsage.OutputTokensDetails.TextTokens
		} else {
			// If no details, OutputTokens is image tokens (per comment in ImageUsage)
			outputImageTokens = imageUsage.OutputTokens
		}

		// Determine the appropriate token pricing rates
		// Prefer image-specific token rates when available, fall back to generic token rates
		var inputTokenRate, outputTokenRate float64
		var inputImageTokenRate, outputImageTokenRate float64

		// Determine generic token rates (for text tokens)
		if isAbove200k {
			if pricing.InputCostPerTokenAbove200kTokens != nil {
				inputTokenRate = *pricing.InputCostPerTokenAbove200kTokens
			} else {
				inputTokenRate = pricing.InputCostPerToken
			}
			if pricing.OutputCostPerTokenAbove200kTokens != nil {
				outputTokenRate = *pricing.OutputCostPerTokenAbove200kTokens
			} else {
				outputTokenRate = pricing.OutputCostPerToken
			}
		} else if isAbove128k {
			if pricing.InputCostPerTokenAbove128kTokens != nil {
				inputTokenRate = *pricing.InputCostPerTokenAbove128kTokens
			} else {
				inputTokenRate = pricing.InputCostPerToken
			}
			if pricing.OutputCostPerTokenAbove128kTokens != nil {
				outputTokenRate = *pricing.OutputCostPerTokenAbove128kTokens
			} else {
				outputTokenRate = pricing.OutputCostPerToken
			}
		} else {
			inputTokenRate = pricing.InputCostPerToken
			outputTokenRate = pricing.OutputCostPerToken
		}

		// Determine image-specific token rates, with tiered pricing support
		// Check for image token pricing fields and fall back to generic rates if not available
		if isAbove200k {
			// Prefer tiered image token pricing above 200k, fall back to base image token rate, then generic rate
			// Note: InputCostPerImageTokenAbove200kTokens and OutputCostPerImageTokenAbove200kTokens
			// may not exist in TableModelPricing yet, so we check base image token rate as fallback
			if pricing.InputCostPerImageToken != nil {
				inputImageTokenRate = *pricing.InputCostPerImageToken
			} else {
				inputImageTokenRate = inputTokenRate
			}
			if pricing.OutputCostPerImageToken != nil {
				outputImageTokenRate = *pricing.OutputCostPerImageToken
			} else {
				outputImageTokenRate = outputTokenRate
			}
		} else if isAbove128k {
			// Prefer tiered image token pricing above 128k, fall back to base image token rate, then generic rate
			// Note: InputCostPerImageTokenAbove128kTokens and OutputCostPerImageTokenAbove128kTokens
			// may not exist in TableModelPricing yet, so we check base image token rate as fallback
			if pricing.InputCostPerImageToken != nil {
				inputImageTokenRate = *pricing.InputCostPerImageToken
			} else {
				inputImageTokenRate = inputTokenRate
			}
			if pricing.OutputCostPerImageToken != nil {
				outputImageTokenRate = *pricing.OutputCostPerImageToken
			} else {
				outputImageTokenRate = outputTokenRate
			}
		} else {
			// Use base image token rates if available, otherwise fall back to generic rates
			if pricing.InputCostPerImageToken != nil {
				inputImageTokenRate = *pricing.InputCostPerImageToken
			} else {
				inputImageTokenRate = inputTokenRate
			}
			if pricing.OutputCostPerImageToken != nil {
				outputImageTokenRate = *pricing.OutputCostPerImageToken
			} else {
				outputImageTokenRate = outputTokenRate
			}
		}

		// Calculate costs: separate text tokens and image tokens with their respective rates
		inputCost = float64(inputTextTokens)*inputTokenRate + float64(inputImageTokens)*inputImageTokenRate
		outputCost = float64(outputTextTokens)*outputTokenRate + float64(outputImageTokens)*outputImageTokenRate

		return inputCost + outputCost
	}

	// Handle video generation if available (for duration-based video generation pricing)
	if videoSeconds != nil && requestType == schemas.VideoGenerationRequest {
		// Use duration-based pricing for video output when available
		if pricing.OutputCostPerVideoPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerVideoPerSecond
		} else if pricing.OutputCostPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerSecond
		} else {
			mc.logger.Debug("no output cost per video per second found for model %s and provider %s", model, provider)
			outputCost = 0.0
		}

		// Input cost is typically zero for video generation, but check if there's input media
		inputCost = 0.0
		if usage != nil && promptTokens > 0 {
			inputCost = float64(promptTokens) * pricing.InputCostPerToken
		}

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
