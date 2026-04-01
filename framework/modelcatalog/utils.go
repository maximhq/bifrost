package modelcatalog

import (
	"strings"

	"github.com/bytedance/sonic"
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
	} else if strings.Contains(p, "runwayml") {
		return string(schemas.Runway)
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
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest, schemas.ImageVariationRequest:
		baseType = "image_generation"
	case schemas.ImageEditRequest, schemas.ImageEditStreamRequest:
		baseType = "image_edit"
	case schemas.VideoGenerationRequest, schemas.VideoRemixRequest:
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
	case schemas.ImageEditStreamRequest:
		return schemas.ImageEditRequest
	default:
		return rt
	}
}

// extractModelName extracts the model name from a model key that may be in provider/model format
func extractModelName(modelKey string) string {
	if strings.Contains(modelKey, "/") {
		parts := strings.Split(modelKey, "/")
		if len(parts) > 1 {
			return strings.Join(parts[1:], "/")
		}
	}
	return modelKey
}

// convertPricingDataToTableModelPricing converts the pricing data to a TableModelPricing struct
func convertPricingDataToTableModelPricing(modelKey string, entry PricingEntry) configstoreTables.TableModelPricing {
	provider := normalizeProvider(entry.Provider)
	modelName := extractModelName(modelKey)

	return configstoreTables.TableModelPricing{
		Model:           modelName,
		BaseModel:       entry.BaseModel,
		Provider:        provider,
		Mode:            entry.Mode,
		ContextLength:   entry.ContextLength,
		MaxInputTokens:  entry.MaxInputTokens,
		MaxOutputTokens: entry.MaxOutputTokens,
		Architecture:    entry.Architecture,

		// Costs - Text
		InputCostPerToken:                 entry.InputCostPerToken,
		OutputCostPerToken:                entry.OutputCostPerToken,
		InputCostPerTokenBatches:          entry.InputCostPerTokenBatches,
		OutputCostPerTokenBatches:         entry.OutputCostPerTokenBatches,
		InputCostPerTokenPriority:         entry.InputCostPerTokenPriority,
		OutputCostPerTokenPriority:        entry.OutputCostPerTokenPriority,
		InputCostPerTokenAbove200kTokens:  entry.InputCostPerTokenAbove200kTokens,
		OutputCostPerTokenAbove200kTokens: entry.OutputCostPerTokenAbove200kTokens,
		// Costs - Character
		InputCostPerCharacter: entry.InputCostPerCharacter,
		// Costs - 128k Tier
		InputCostPerTokenAbove128kTokens:          entry.InputCostPerTokenAbove128kTokens,
		InputCostPerImageAbove128kTokens:          entry.InputCostPerImageAbove128kTokens,
		InputCostPerVideoPerSecondAbove128kTokens: entry.InputCostPerVideoPerSecondAbove128kTokens,
		InputCostPerAudioPerSecondAbove128kTokens: entry.InputCostPerAudioPerSecondAbove128kTokens,
		OutputCostPerTokenAbove128kTokens:         entry.OutputCostPerTokenAbove128kTokens,

		// Costs - Cache
		CacheCreationInputTokenCost:                        entry.CacheCreationInputTokenCost,
		CacheReadInputTokenCost:                            entry.CacheReadInputTokenCost,
		CacheCreationInputTokenCostAbove200kTokens:         entry.CacheCreationInputTokenCostAbove200kTokens,
		CacheReadInputTokenCostAbove200kTokens:             entry.CacheReadInputTokenCostAbove200kTokens,
		CacheCreationInputTokenCostAbove1hr:                entry.CacheCreationInputTokenCostAbove1hr,
		CacheCreationInputTokenCostAbove1hrAbove200kTokens: entry.CacheCreationInputTokenCostAbove1hrAbove200kTokens,
		CacheCreationInputAudioTokenCost:                   entry.CacheCreationInputAudioTokenCost,
		CacheReadInputTokenCostPriority:                    entry.CacheReadInputTokenCostPriority,
		CacheReadInputImageTokenCost:                       entry.CacheReadInputImageTokenCost,

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
		OutputCostPerImageAbove2048x2048Pixels:        entry.OutputCostPerImageAbove2048x2048Pixels,
		OutputCostPerImageAbove4096x4096Pixels:        entry.OutputCostPerImageAbove4096x4096Pixels,
		OutputCostPerImageLowQuality:                  entry.OutputCostPerImageLowQuality,
		OutputCostPerImageMediumQuality:               entry.OutputCostPerImageMediumQuality,
		OutputCostPerImageHighQuality:                 entry.OutputCostPerImageHighQuality,
		OutputCostPerImageAutoQuality:                 entry.OutputCostPerImageAutoQuality,
		// Costs - Image Token
		InputCostPerImageToken:  entry.InputCostPerImageToken,
		OutputCostPerImageToken: entry.OutputCostPerImageToken,

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
	options := PricingOptions{
		// Costs - Text
		InputCostPerToken:                 pricing.InputCostPerToken,
		OutputCostPerToken:                pricing.OutputCostPerToken,
		InputCostPerTokenBatches:          pricing.InputCostPerTokenBatches,
		OutputCostPerTokenBatches:         pricing.OutputCostPerTokenBatches,
		InputCostPerTokenPriority:         pricing.InputCostPerTokenPriority,
		OutputCostPerTokenPriority:        pricing.OutputCostPerTokenPriority,
		InputCostPerTokenAbove200kTokens:  pricing.InputCostPerTokenAbove200kTokens,
		OutputCostPerTokenAbove200kTokens: pricing.OutputCostPerTokenAbove200kTokens,
		// Costs - Character
		InputCostPerCharacter: pricing.InputCostPerCharacter,
		// Costs - 128k Tier
		InputCostPerTokenAbove128kTokens:          pricing.InputCostPerTokenAbove128kTokens,
		InputCostPerImageAbove128kTokens:          pricing.InputCostPerImageAbove128kTokens,
		InputCostPerVideoPerSecondAbove128kTokens: pricing.InputCostPerVideoPerSecondAbove128kTokens,
		InputCostPerAudioPerSecondAbove128kTokens: pricing.InputCostPerAudioPerSecondAbove128kTokens,
		OutputCostPerTokenAbove128kTokens:         pricing.OutputCostPerTokenAbove128kTokens,

		// Costs - Cache
		CacheCreationInputTokenCost:                        pricing.CacheCreationInputTokenCost,
		CacheReadInputTokenCost:                            pricing.CacheReadInputTokenCost,
		CacheCreationInputTokenCostAbove200kTokens:         pricing.CacheCreationInputTokenCostAbove200kTokens,
		CacheReadInputTokenCostAbove200kTokens:             pricing.CacheReadInputTokenCostAbove200kTokens,
		CacheCreationInputTokenCostAbove1hr:                pricing.CacheCreationInputTokenCostAbove1hr,
		CacheCreationInputTokenCostAbove1hrAbove200kTokens: pricing.CacheCreationInputTokenCostAbove1hrAbove200kTokens,
		CacheCreationInputAudioTokenCost:                   pricing.CacheCreationInputAudioTokenCost,
		CacheReadInputTokenCostPriority:                    pricing.CacheReadInputTokenCostPriority,
		CacheReadInputImageTokenCost:                       pricing.CacheReadInputImageTokenCost,

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
		OutputCostPerImageAbove2048x2048Pixels:        pricing.OutputCostPerImageAbove2048x2048Pixels,
		OutputCostPerImageAbove4096x4096Pixels:        pricing.OutputCostPerImageAbove4096x4096Pixels,
		OutputCostPerImageLowQuality:                  pricing.OutputCostPerImageLowQuality,
		OutputCostPerImageMediumQuality:               pricing.OutputCostPerImageMediumQuality,
		OutputCostPerImageHighQuality:                 pricing.OutputCostPerImageHighQuality,
		OutputCostPerImageAutoQuality:                 pricing.OutputCostPerImageAutoQuality,
		// Costs - Image Token
		InputCostPerImageToken:  pricing.InputCostPerImageToken,
		OutputCostPerImageToken: pricing.OutputCostPerImageToken,

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
	return &PricingEntry{
		BaseModel:       pricing.BaseModel,
		Provider:        pricing.Provider,
		Mode:            pricing.Mode,
		ContextLength:   pricing.ContextLength,
		MaxInputTokens:  pricing.MaxInputTokens,
		MaxOutputTokens: pricing.MaxOutputTokens,
		Architecture:    pricing.Architecture,
		PricingOptions:  options,
	}
}

// convertTablePricingOverrideToPricingOverride converts a TablePricingOverride to a PricingOverride.
func convertTablePricingOverrideToPricingOverride(override *configstoreTables.TablePricingOverride) (PricingOverride, error) {
	var options PricingOptions
	if err := sonic.Unmarshal([]byte(override.PricingPatchJSON), &options); err != nil {
		return PricingOverride{}, err
	}
	return PricingOverride{
		ID:            override.ID,
		Name:          override.Name,
		ScopeKind:     ScopeKind(override.ScopeKind),
		VirtualKeyID:  override.VirtualKeyID,
		ProviderID:    override.ProviderID,
		ProviderKeyID: override.ProviderKeyID,
		MatchType:     MatchType(override.MatchType),
		Pattern:       override.Pattern,
		RequestTypes:  override.RequestTypes,
		Options:       options,
	}, nil
}
