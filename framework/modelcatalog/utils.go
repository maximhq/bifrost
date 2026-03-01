package modelcatalog

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// makeKey creates a unique key for a model, provider, and mode for pricingData map
func makeKey(model, provider, mode string) string { return model + "|" + provider + "|" + mode }

// normalizeProvider normalizes the provider name to a consistent format
func normalizeProvider(p string) string {
	if strings.Contains(p, "vertex_ai") || p == "google-vertex" {
		return string(schemas.Vertex)
	} else if strings.Contains(p, "bedrock") {
		return string(schemas.Bedrock)
	} else if strings.Contains(p, "cohere") {
		return string(schemas.Cohere)
	} else {
		return p
	}
}

// normalizeRequestType normalizes the request type to a consistent format
func normalizeRequestType(reqType schemas.RequestType) string {
	baseType := "unknown"

	switch reqType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		baseType = "completion"
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		baseType = "chat"
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		baseType = "responses"
	case schemas.EmbeddingRequest:
		baseType = "embedding"
	case schemas.RerankRequest:
		baseType = "rerank"
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		baseType = "audio_speech"
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		baseType = "audio_transcription"
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
		baseType = "image_generation"
	case schemas.VideoGenerationRequest:
		baseType = "video_generation"
	}

	return baseType
}

// normalizeStreamRequestType normalizes the stream request type to a consistent format
// It returns the base request type for the stream request type.
func normalizeStreamRequestType(rt schemas.RequestType) schemas.RequestType {
	switch rt {
	case schemas.TextCompletionStreamRequest:
		return schemas.TextCompletionRequest
	case schemas.ChatCompletionStreamRequest:
		return schemas.ChatCompletionRequest
	case schemas.ResponsesStreamRequest:
		return schemas.ResponsesRequest
	case schemas.SpeechStreamRequest:
		return schemas.SpeechRequest
	case schemas.TranscriptionStreamRequest:
		return schemas.TranscriptionRequest
	case schemas.ImageGenerationStreamRequest:
		return schemas.ImageGenerationRequest
	default:
		return rt
	}
}

// convertPricingDataToTableModelPricing converts the pricing data to a TableModelPricing struct
func convertPricingDataToTableModelPricing(modelKey string, entry PricingEntry) configstoreTables.TableModelPricing {
	provider := normalizeProvider(entry.Provider)

	// Handle provider/model format - extract just the model name
	modelName := modelKey
	if strings.Contains(modelKey, "/") {
		parts := strings.Split(modelKey, "/")
		if len(parts) > 1 {
			modelName = strings.Join(parts[1:], "/")
		}
	}

	return configstoreTables.TableModelPricing{
		Model:     modelName,
		BaseModel: entry.BaseModel,
		Provider:  provider,
		Mode:      entry.Mode,

		// Costs - Text
		InputCostPerToken:                 entry.InputCostPerToken,
		OutputCostPerToken:                entry.OutputCostPerToken,
		InputCostPerTokenBatches:          entry.InputCostPerTokenBatches,
		OutputCostPerTokenBatches:         entry.OutputCostPerTokenBatches,
		InputCostPerTokenPriority:         entry.InputCostPerTokenPriority,
		OutputCostPerTokenPriority:        entry.OutputCostPerTokenPriority,
		InputCostPerTokenAbove200kTokens:  entry.InputCostPerTokenAbove200kTokens,
		OutputCostPerTokenAbove200kTokens: entry.OutputCostPerTokenAbove200kTokens,

		// Costs - Cache
		CacheCreationInputTokenCost:                        entry.CacheCreationInputTokenCost,
		CacheReadInputTokenCost:                            entry.CacheReadInputTokenCost,
		CacheCreationInputTokenCostAbove200kTokens:         entry.CacheCreationInputTokenCostAbove200kTokens,
		CacheReadInputTokenCostAbove200kTokens:             entry.CacheReadInputTokenCostAbove200kTokens,
		CacheCreationInputTokenCostAbove1hr:                entry.CacheCreationInputTokenCostAbove1hr,
		CacheCreationInputTokenCostAbove1hrAbove200kTokens: entry.CacheCreationInputTokenCostAbove1hrAbove200kTokens,
		CacheCreationInputAudioTokenCost:                   entry.CacheCreationInputAudioTokenCost,
		CacheReadInputTokenCostPriority:                    entry.CacheReadInputTokenCostPriority,

		// Costs - Image
		InputCostPerImage:                             entry.InputCostPerImage,
		InputCostPerPixel:                             entry.InputCostPerPixel,
		OutputCostPerImage:                            entry.OutputCostPerImage,
		OutputCostPerPixel:                            entry.OutputCostPerPixel,
		OutputCostPerImagePremiumImage:                entry.OutputCostPerImagePremiumImage,
		OutputCostPerImageAbove512x512Pixels:          entry.OutputCostPerImageAbove512x512Pixels,
		OutputCostPerImageAbove512x512PixelsPremium:   entry.OutputCostPerImageAbove512x512PixelsPremium,
		OutputCostPerImageAbove1024x1024Pixels:        entry.OutputCostPerImageAbove1024x1024Pixels,
		OutputCostPerImageAbove1024x1024PixelsPremium: entry.OutputCostPerImageAbove1024x1024PixelsPremium,

		// Costs - Audio/Video
		InputCostPerAudioToken:      entry.InputCostPerAudioToken,
		InputCostPerAudioPerSecond:  entry.InputCostPerAudioPerSecond,
		InputCostPerSecond:          entry.InputCostPerSecond,
		InputCostPerVideoPerSecond:  entry.InputCostPerVideoPerSecond,
		OutputCostPerAudioToken:     entry.OutputCostPerAudioToken,
		OutputCostPerVideoPerSecond: entry.OutputCostPerVideoPerSecond,
		OutputCostPerSecond:         entry.OutputCostPerSecond,

		// Costs - Other
		SearchContextCostPerQuery:     entry.SearchContextCostPerQuery,
		CodeInterpreterCostPerSession: entry.CodeInterpreterCostPerSession,
	}
}

// convertTableModelPricingToPricingData converts the TableModelPricing struct to a PricingEntry struct
func convertTableModelPricingToPricingData(pricing *configstoreTables.TableModelPricing) *PricingEntry {
	return &PricingEntry{
		BaseModel: pricing.BaseModel,
		Provider:  pricing.Provider,
		Mode:      pricing.Mode,

		// Costs - Text
		InputCostPerToken:                 pricing.InputCostPerToken,
		OutputCostPerToken:                pricing.OutputCostPerToken,
		InputCostPerTokenBatches:          pricing.InputCostPerTokenBatches,
		OutputCostPerTokenBatches:         pricing.OutputCostPerTokenBatches,
		InputCostPerTokenPriority:         pricing.InputCostPerTokenPriority,
		OutputCostPerTokenPriority:        pricing.OutputCostPerTokenPriority,
		InputCostPerTokenAbove200kTokens:  pricing.InputCostPerTokenAbove200kTokens,
		OutputCostPerTokenAbove200kTokens: pricing.OutputCostPerTokenAbove200kTokens,

		// Costs - Cache
		CacheCreationInputTokenCost:                        pricing.CacheCreationInputTokenCost,
		CacheReadInputTokenCost:                            pricing.CacheReadInputTokenCost,
		CacheCreationInputTokenCostAbove200kTokens:         pricing.CacheCreationInputTokenCostAbove200kTokens,
		CacheReadInputTokenCostAbove200kTokens:             pricing.CacheReadInputTokenCostAbove200kTokens,
		CacheCreationInputTokenCostAbove1hr:                pricing.CacheCreationInputTokenCostAbove1hr,
		CacheCreationInputTokenCostAbove1hrAbove200kTokens: pricing.CacheCreationInputTokenCostAbove1hrAbove200kTokens,
		CacheCreationInputAudioTokenCost:                   pricing.CacheCreationInputAudioTokenCost,
		CacheReadInputTokenCostPriority:                    pricing.CacheReadInputTokenCostPriority,

		// Costs - Image
		InputCostPerImage:                             pricing.InputCostPerImage,
		InputCostPerPixel:                             pricing.InputCostPerPixel,
		OutputCostPerImage:                            pricing.OutputCostPerImage,
		OutputCostPerPixel:                            pricing.OutputCostPerPixel,
		OutputCostPerImagePremiumImage:                pricing.OutputCostPerImagePremiumImage,
		OutputCostPerImageAbove512x512Pixels:          pricing.OutputCostPerImageAbove512x512Pixels,
		OutputCostPerImageAbove512x512PixelsPremium:   pricing.OutputCostPerImageAbove512x512PixelsPremium,
		OutputCostPerImageAbove1024x1024Pixels:        pricing.OutputCostPerImageAbove1024x1024Pixels,
		OutputCostPerImageAbove1024x1024PixelsPremium: pricing.OutputCostPerImageAbove1024x1024PixelsPremium,

		// Costs - Audio/Video
		InputCostPerAudioToken:      pricing.InputCostPerAudioToken,
		InputCostPerAudioPerSecond:  pricing.InputCostPerAudioPerSecond,
		InputCostPerSecond:          pricing.InputCostPerSecond,
		InputCostPerVideoPerSecond:  pricing.InputCostPerVideoPerSecond,
		OutputCostPerAudioToken:     pricing.OutputCostPerAudioToken,
		OutputCostPerVideoPerSecond: pricing.OutputCostPerVideoPerSecond,
		OutputCostPerSecond:         pricing.OutputCostPerSecond,

		// Costs - Other
		SearchContextCostPerQuery:     pricing.SearchContextCostPerQuery,
		CodeInterpreterCostPerSession: pricing.CodeInterpreterCostPerSession,
	}
}
