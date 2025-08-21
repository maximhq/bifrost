// Package governance provides utility functions for the governance plugin
package governance

import (
	"context"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

type ContextKey string

// extractHeadersFromContext extracts governance headers from context (standalone version)
func extractHeadersFromContext(ctx context.Context) map[string]string {
	headers := make(map[string]string)

	// Extract governance headers using lib.ContextKey
	if teamID := getStringFromContext(ctx, "x-bf-team"); teamID != "" {
		headers["x-bf-team"] = teamID
	}
	if userID := getStringFromContext(ctx, "x-bf-user"); userID != "" {
		headers["x-bf-user"] = userID
	}
	if customerID := getStringFromContext(ctx, "x-bf-customer"); customerID != "" {
		headers["x-bf-customer"] = customerID
	}

	return headers
}

// getStringFromContext safely extracts a string value from context
func getStringFromContext(ctx context.Context, key string) string {
	if value := ctx.Value(key); value != nil {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

// getRequestType returns the type of request with batch/cache detection
func getRequestType(req *schemas.BifrostRequest) string {
	baseType := "unknown"

	if req.Input.TextCompletionInput != nil || req.Input.ChatCompletionInput != nil {
		baseType = "chat"
	} else if req.Input.EmbeddingInput != nil {
		baseType = "embedding"
	} else if req.Input.SpeechInput != nil {
		baseType = "audio_speech"
	} else if req.Input.TranscriptionInput != nil {
		baseType = "audio_transcription"
	}

	// Check for batch processing indicators
	if isBatchRequest(req) {
		return baseType + "_batch"
	}

	return baseType
}

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
	switch p {
	case "vertex_ai-language-models", "vertex_ai", "google-vertex":
		return "vertex"
	default:
		return p
	}
}

// convertPricingDataToTableModelPricing converts the pricing data to a TableModelPricing struct
func convertPricingDataToTableModelPricing(modelKey string, entry PricingEntry) configstore.TableModelPricing {
	provider := normalizeProvider(entry.Provider)

	// Handle provider/model format - extract just the model name
	modelName := modelKey
	if strings.Contains(modelKey, "/") {
		parts := strings.Split(modelKey, "/")
		if len(parts) > 1 {
			modelName = parts[1]
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
