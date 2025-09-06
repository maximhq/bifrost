// Package governance provides utility functions for the governance plugin
package governance

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
)

type ContextKey string

// extractHeadersFromContext extracts governance headers from context (standalone version)
func extractHeadersFromContext(ctx context.Context) map[string]string {
	headers := make(map[string]string)

	// Extract governance headers using lib.ContextKey
	if teamID := getStringFromContext(ctx, ContextKey("x-bf-team")); teamID != "" {
		headers["x-bf-team"] = teamID
	}
	if userID := getStringFromContext(ctx, ContextKey("x-bf-user")); userID != "" {
		headers["x-bf-user"] = userID
	}
	if customerID := getStringFromContext(ctx, ContextKey("x-bf-customer")); customerID != "" {
		headers["x-bf-customer"] = customerID
	}

	return headers
}

// getStringFromContext safely extracts a string value from context
func getStringFromContext(ctx context.Context, key any) string {
	if value := ctx.Value(key); value != nil {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

// hasUsageData checks if the response contains actual usage information
func hasUsageData(result *schemas.BifrostResponse) bool {
	if result == nil {
		return false
	}

	// Check main usage field
	if result.Usage != nil {
		return true
	}

	// Check speech usage
	if result.Speech != nil && result.Speech.Usage != nil {
		return true
	}

	// Check transcribe usage
	if result.Transcribe != nil && result.Transcribe.Usage != nil {
		return true
	}

	return false
}
