// Package governance provides utility functions for the governance plugin
package governance

import (
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// parseVirtualKeyFromHTTPRequest parses the virtual key from HTTP request headers.
// It checks multiple headers in order: x-bf-vk, Authorization (Bearer token), x-api-key, and x-goog-api-key.
// Parameters:
//   - req: The HTTP request containing headers to parse
//
// Returns:
//   - *string: The virtual key if found, nil otherwise
func parseVirtualKeyFromHTTPRequest(req *schemas.HTTPRequest) *string {
	var virtualKeyValue string
	vkHeader := req.CaseInsensitiveHeaderLookup("x-bf-vk")
	if vkHeader != "" {
		return bifrost.Ptr(vkHeader)
	}
	authHeader := req.CaseInsensitiveHeaderLookup("Authorization")
	if authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			authHeaderValue := strings.TrimSpace(authHeader[7:]) // Remove "Bearer " prefix
			if authHeaderValue != "" && strings.HasPrefix(strings.ToLower(authHeaderValue), VirtualKeyPrefix) {
				virtualKeyValue = authHeaderValue
			}
		}
	}
	if virtualKeyValue != "" {
		return bifrost.Ptr(virtualKeyValue)
	}
	xAPIKey := req.CaseInsensitiveHeaderLookup("x-api-key")
	if xAPIKey != "" && strings.HasPrefix(strings.ToLower(xAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xAPIKey)
	}
	// Checking x-goog-api-key header
	xGoogleAPIKey := req.CaseInsensitiveHeaderLookup("x-goog-api-key")
	if xGoogleAPIKey != "" && strings.HasPrefix(strings.ToLower(xGoogleAPIKey), VirtualKeyPrefix) {
		return bifrost.Ptr(xGoogleAPIKey)
	}
	return nil
}

// equalPtr compares two pointers of comparable type for value equality
// Returns true if both are nil or both are non-nil with equal values
func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// getWeight safely dereferences a *float64 weight pointer, returning 1.0 as default if nil.
// This allows distinguishing between "not set" (nil -> 1.0) and "explicitly set to 0" (0.0).
func getWeight(w *float64) float64 {
	if w == nil {
		return 1.0
	}
	return *w
}
