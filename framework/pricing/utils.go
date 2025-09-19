package pricing

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// makeKey creates a unique key for a model, provider, and mode for pricingData map
func makeKey(model, provider, mode string) string { return model + "|" + provider + "|" + mode }

// isBatchRequest checks if the request is for batch processing
func isBatchRequest(req *schemas.BifrostRequest) bool {
	// Check for batch endpoints or batch-specific headers
	// This could be detected via specific endpoint patterns or headers
	// For now, return false
	return false
}

// isCacheReadRequest checks if the request involves cache reading
func isCacheReadRequest(req *schemas.BifrostRequest, headers map[string]string) bool {
	// Check for cache-related headers or request parameters
	if cacheHeader := headers["x-cache-read"]; cacheHeader == "true" {
		return true
	}

	// Check for anthropic cache headers
	if cacheControl := headers["anthropic-beta"]; cacheControl != "" {
		return true
	}

	// TODO: Add message-level cache control detection when BifrostMessage schema supports it
	// For now, cache detection relies on headers only

	return false
}

// normalizeProvider normalizes the provider name to a consistent format
func normalizeProvider(p string) string {
	if strings.Contains(p, "vertex_ai") || p == "google-vertex" {
		return string(schemas.Vertex)
	} else {
		return p
	}
}

// normalizeRequestType normalizes the request type to a consistent format
func normalizeRequestType(reqType schemas.RequestType) string {
	baseType := "unknown"

	switch reqType {
	case schemas.TextCompletionRequest:
		baseType = "completion"
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		baseType = "chat"
	case schemas.EmbeddingRequest:
		baseType = "embedding"
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		baseType = "audio_speech"
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		baseType = "audio_transcription"
	}

	// TODO: Check for batch processing indicators
	// if isBatchRequest(reqType) {
	// 	return baseType + "_batch"
	// }

	return baseType
}

// convertPricingDataToTableModelPricing converts the pricing data to a TableModelPricing struct
func convertPricingDataToTableModelPricing(modelKey string, entry PricingEntry) configstore.TableModelPricing {
	provider := normalizeProvider(entry.Provider)
	modelName := modelKey

	// For openrouter, the model key from the pricing JSON is the canonical model name.
	// e.g., "openrouter/google/gemini-2.5-flash"
	// We should also ensure the provider is set correctly.
	if strings.HasPrefix(modelKey, "openrouter/") {
		provider = "openrouter"
	} else if strings.Contains(modelKey, "/") {
		// For other providers, e.g., "google/gemma-7b", the model name is "gemma-7b".
		parts := strings.Split(modelKey, "/")
		if len(parts) > 1 {
			// Take the last part of the split.
			modelName = parts[len(parts)-1]
		}
	}

	pricing := configstore.TableModelPricing{
		Model:              modelName,
		Provider:           provider,
		InputCostPerToken:  entry.InputCostPerToken,
		OutputCostPerToken: entry.OutputCostPerToken,
		Mode:               entry.Mode,

		// Additional pricing for media
		InputCostPerImage:          entry.InputCostPerImage,
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

		// Cache and batch pricing
		CacheReadInputTokenCost:   entry.CacheReadInputTokenCost,
		InputCostPerTokenBatches:  entry.InputCostPerTokenBatches,
		OutputCostPerTokenBatches: entry.OutputCostPerTokenBatches,
	}

	return pricing
}

// getSafeFloat64 returns the value of a float64 pointer or fallback if nil
func getSafeFloat64(ptr *float64, fallback float64) float64 {
	if ptr != nil {
		return *ptr
	}
	return fallback
}
