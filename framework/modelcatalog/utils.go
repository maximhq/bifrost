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
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		baseType = "audio_speech"
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		baseType = "audio_transcription"
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
		baseType = "image_generation"
	}

	// TODO: Check for batch processing indicators
	// if isBatchRequest(reqType) {
	// 	return baseType + "_batch"
	// }

	return baseType
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

	pricing := configstoreTables.TableModelPricing{
		Model:              modelName,
		Provider:           provider,
		InputCostPerToken:  entry.InputCostPerToken,
		OutputCostPerToken: entry.OutputCostPerToken,
		Mode:               entry.Mode,

		// Additional pricing for media
		InputCostPerVideoPerSecond: entry.InputCostPerVideoPerSecond,
		InputCostPerAudioPerSecond: entry.InputCostPerAudioPerSecond,

		// Character-based pricing
		InputCostPerCharacter:  entry.InputCostPerCharacter,
		OutputCostPerCharacter: entry.OutputCostPerCharacter,

		// Pricing above 128k tokens
		InputCostPerTokenAbove128kTokens:          entry.InputCostPerTokenAbove128kTokens,
		InputCostPerCharacterAbove128kTokens:      entry.InputCostPerCharacterAbove128kTokens,
		InputCostPerImageAbove128kTokens:          entry.InputCostPerImageAbove128kTokens,
		InputCostPerVideoPerSecondAbove128kTokens: entry.InputCostPerVideoPerSecondAbove128kTokens,
		InputCostPerAudioPerSecondAbove128kTokens: entry.InputCostPerAudioPerSecondAbove128kTokens,
		OutputCostPerTokenAbove128kTokens:         entry.OutputCostPerTokenAbove128kTokens,
		OutputCostPerCharacterAbove128kTokens:     entry.OutputCostPerCharacterAbove128kTokens,

		//Pricing above 200k tokens (for gemini models)
		InputCostPerTokenAbove200kTokens:           entry.InputCostPerTokenAbove200kTokens,
		OutputCostPerTokenAbove200kTokens:          entry.OutputCostPerTokenAbove200kTokens,
		CacheCreationInputTokenCostAbove200kTokens: entry.CacheCreationInputTokenCostAbove200kTokens,
		CacheReadInputTokenCostAbove200kTokens:     entry.CacheReadInputTokenCostAbove200kTokens,

		// Cache and batch pricing
		CacheReadInputTokenCost:   entry.CacheReadInputTokenCost,
		InputCostPerTokenBatches:  entry.InputCostPerTokenBatches,
		OutputCostPerTokenBatches: entry.OutputCostPerTokenBatches,

		// Image generation pricing
		InputCostPerImageToken:       entry.InputCostPerImageToken,
		OutputCostPerImageToken:      entry.OutputCostPerImageToken,
		InputCostPerImage:            entry.InputCostPerImage,
		OutputCostPerImage:           entry.OutputCostPerImage,
		CacheReadInputImageTokenCost: entry.CacheReadInputImageTokenCost,
	}

	return pricing
}

// convertTableModelPricingToPricingData converts the TableModelPricing struct to a DataSheetPricingEntry struct
func convertTableModelPricingToPricingData(pricing *configstoreTables.TableModelPricing) *PricingEntry {
	return &PricingEntry{
		Provider:                                   pricing.Provider,
		Mode:                                       pricing.Mode,
		InputCostPerToken:                          pricing.InputCostPerToken,
		OutputCostPerToken:                         pricing.OutputCostPerToken,
		InputCostPerVideoPerSecond:                 pricing.InputCostPerVideoPerSecond,
		InputCostPerAudioPerSecond:                 pricing.InputCostPerAudioPerSecond,
		InputCostPerCharacter:                      pricing.InputCostPerCharacter,
		OutputCostPerCharacter:                     pricing.OutputCostPerCharacter,
		InputCostPerTokenAbove128kTokens:           pricing.InputCostPerTokenAbove128kTokens,
		InputCostPerCharacterAbove128kTokens:       pricing.InputCostPerCharacterAbove128kTokens,
		InputCostPerImageAbove128kTokens:           pricing.InputCostPerImageAbove128kTokens,
		InputCostPerVideoPerSecondAbove128kTokens:  pricing.InputCostPerVideoPerSecondAbove128kTokens,
		InputCostPerAudioPerSecondAbove128kTokens:  pricing.InputCostPerAudioPerSecondAbove128kTokens,
		OutputCostPerTokenAbove128kTokens:          pricing.OutputCostPerTokenAbove128kTokens,
		OutputCostPerCharacterAbove128kTokens:      pricing.OutputCostPerCharacterAbove128kTokens,
		InputCostPerTokenAbove200kTokens:           pricing.InputCostPerTokenAbove200kTokens,
		OutputCostPerTokenAbove200kTokens:          pricing.OutputCostPerTokenAbove200kTokens,
		CacheCreationInputTokenCostAbove200kTokens: pricing.CacheCreationInputTokenCostAbove200kTokens,
		CacheReadInputTokenCostAbove200kTokens:     pricing.CacheReadInputTokenCostAbove200kTokens,
		CacheReadInputTokenCost:                    pricing.CacheReadInputTokenCost,
		InputCostPerTokenBatches:                   pricing.InputCostPerTokenBatches,
		OutputCostPerTokenBatches:                  pricing.OutputCostPerTokenBatches,
		InputCostPerImageToken:                     pricing.InputCostPerImageToken,
		OutputCostPerImageToken:                    pricing.OutputCostPerImageToken,
		InputCostPerImage:                          pricing.InputCostPerImage,
		OutputCostPerImage:                         pricing.OutputCostPerImage,
		CacheReadInputImageTokenCost:               pricing.CacheReadInputImageTokenCost,
	}
}

// getSafeFloat64 returns the value of a float64 pointer or fallback if nil
func getSafeFloat64(ptr *float64, fallback float64) float64 {
	if ptr != nil {
		return *ptr
	}
	return fallback
}
