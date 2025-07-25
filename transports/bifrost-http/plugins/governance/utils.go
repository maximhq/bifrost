// Package governance provides utility functions for the governance plugin
package governance

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"gorm.io/gorm"
)

// autoMigrateGovernanceTables ensures all governance tables exist
func autoMigrateGovernanceTables(db *gorm.DB) error {
	// List of all governance models to migrate (new hierarchical system)
	models := []interface{}{
		&Budget{},
		&RateLimit{},
		&Customer{},
		&Team{},
		&VirtualKey{},
		&Config{},
		&ModelPricing{},
	}

	for _, model := range models {
		if err := db.AutoMigrate(model); err != nil {
			return fmt.Errorf("failed to migrate model %T: %w", model, err)
		}
	}

	return nil
}

// Standalone utility functions for use across the governance plugin

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
	if value := ctx.Value(lib.ContextKey(key)); value != nil {
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
