// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains common utility functions used across all handlers.
package handlers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// resolvePeriod converts a relative period string ("1h","6h","24h","7d","30d") to concrete
// start/end time pointers computed from the current server time. Returns nil, nil for
// unrecognised values. When used in filter parsing, period takes precedence over any explicit
// start_time/end_time query parameters so every poll always covers the intended window.
func resolvePeriod(period string) (start, end *time.Time) {
	now := time.Now()
	var from time.Time
	switch period {
	case "1h":
		from = now.Add(-time.Hour)
	case "6h":
		from = now.Add(-6 * time.Hour)
	case "24h":
		from = now.Add(-24 * time.Hour)
	case "7d":
		from = now.AddDate(0, 0, -7)
	case "30d":
		from = now.AddDate(0, 0, -30)
	default:
		return nil, nil
	}
	return &from, &now
}

// pluginDisabledKey is a dedicated context key type for marking a plugin as disabled
// rather than removed. Using a named type instead of a raw string follows Go best practices.
type pluginDisabledKey struct{}

// PluginDisabledKey is the context key used to indicate a plugin is being disabled.
var PluginDisabledKey pluginDisabledKey

// badRequestError wraps a client input validation error so that outer handlers
// can distinguish it from internal server errors and return HTTP 400.
type badRequestError struct{ err error }

func (e *badRequestError) Error() string { return e.err.Error() }
func (e *badRequestError) Unwrap() error { return e.err }

// SendJSON sends a JSON response with 200 OK status
func SendJSON(ctx *fasthttp.RequestCtx, data interface{}) {
	ctx.SetContentType("application/json")
	if err := json.NewEncoder(ctx).Encode(data); err != nil {
		logger.Warn(fmt.Sprintf("Failed to encode JSON response: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to encode response: %v", err))
	}
}

// SendJSONWithStatus sends a JSON response with a custom status code
func SendJSONWithStatus(ctx *fasthttp.RequestCtx, data interface{}, statusCode int) {
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(statusCode)
	if err := json.NewEncoder(ctx).Encode(data); err != nil {
		logger.Warn(fmt.Sprintf("Failed to encode JSON response: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to encode response: %v", err))
	}
}

// SendError sends a BifrostError response
func SendError(ctx *fasthttp.RequestCtx, statusCode int, message string) {
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: message,
		},
	}
	SendBifrostError(ctx, bifrostErr)
}

// SendBifrostError sends a BifrostError response
func SendBifrostError(ctx *fasthttp.RequestCtx, bifrostErr *schemas.BifrostError) {
	if bifrostErr.StatusCode != nil {
		ctx.SetStatusCode(*bifrostErr.StatusCode)
	} else if !bifrostErr.IsBifrostError {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
	} else {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
	}

	ctx.SetContentType("application/json")
	if encodeErr := json.NewEncoder(ctx).Encode(bifrostErr); encodeErr != nil {
		logger.Warn(fmt.Sprintf("Failed to encode error response: %v", encodeErr))
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetBodyString(fmt.Sprintf("Failed to encode error response: %v", encodeErr))
	}
}

// streamLargeResponseIfActive checks if large response mode was activated by the provider
// and streams the response directly to the client. Returns true if the response was handled
// (caller should return), false if normal response handling should continue.
func streamLargeResponseIfActive(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) bool {
	isLargeResponse, ok := bifrostCtx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool)
	if !ok || !isLargeResponse {
		return false
	}
	if !lib.StreamLargeResponseBody(ctx, bifrostCtx) {
		SendError(ctx, fasthttp.StatusInternalServerError, "Large response reader not available")
	}
	return true
}

// SendSSEError sends an error in Server-Sent Events format
func SendSSEError(ctx *fasthttp.RequestCtx, bifrostErr *schemas.BifrostError) {
	errorJSON, err := json.Marshal(map[string]interface{}{
		"error": bifrostErr,
	})
	if err != nil {
		logger.Error("failed to marshal error for SSE: %v", err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		return
	}

	if _, err := fmt.Fprintf(ctx, "data: %s\n\n", errorJSON); err != nil {
		logger.Warn(fmt.Sprintf("Failed to write SSE error: %v", err))
	}
}

// IsOriginAllowed checks if the given origin is allowed based on localhost rules and configured allowed origins.
// Localhost origins are always allowed. Additional origins can be configured in allowedOrigins.
// Supports wildcard patterns like *.example.com to match any subdomain.
func IsOriginAllowed(origin string, allowedOrigins []string) bool {
	// Always allow localhost origins
	if isLocalhostOrigin(origin) {
		return true
	}

	// Check configured allowed origins
	for _, allowedOrigin := range allowedOrigins {
		// Check for exact match first
		if allowedOrigin == origin {
			return true
		}

		if allowedOrigin == "*" {
			return true
		}

		// Check for wildcard pattern
		if strings.Contains(allowedOrigin, "*") {
			if matchesWildcardPattern(origin, allowedOrigin) {
				return true
			}
		}
	}

	return false
}

// isLocalhostOrigin checks if the given origin is a localhost origin
func isLocalhostOrigin(origin string) bool {
	return strings.HasPrefix(origin, "http://localhost:") ||
		strings.HasPrefix(origin, "https://localhost:") ||
		strings.HasPrefix(origin, "http://127.0.0.1:") ||
		strings.HasPrefix(origin, "http://0.0.0.0:") ||
		strings.HasPrefix(origin, "https://127.0.0.1:")
}

// matchesWildcardPattern checks if an origin matches a wildcard pattern.
// Supports patterns like *.example.com, https://*.example.com, or http://*.example.com
func matchesWildcardPattern(origin string, pattern string) bool {
	// Convert wildcard pattern to regex pattern
	// Escape special regex characters except *
	regexPattern := regexp.QuoteMeta(pattern)
	// Replace escaped \* with regex pattern for subdomain matching
	// \* should match one or more characters that are not dots (to match a subdomain)
	regexPattern = strings.ReplaceAll(regexPattern, `\*`, `[^/.]+`)
	// Anchor the pattern to match the entire origin
	regexPattern = "^" + regexPattern + "$"

	// Compile and test the regex
	re, err := regexp.Compile(regexPattern)
	if err != nil {
		return false
	}

	return re.MatchString(origin)
}

// ParseModel parses a model string in the format "provider/model" or "provider/nested/model"
// Returns the provider and full model name after the first slash
func ParseModel(model string) (string, string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", "", fmt.Errorf("model cannot be empty")
	}

	parts := strings.SplitN(model, "/", 2)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("model must be in the format 'provider/model'")
	}

	provider := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	if provider == "" || name == "" {
		return "", "", fmt.Errorf("model must be in the format 'provider/model' with non-empty provider and model")
	}
	return provider, name, nil
}

// ClampPaginationParams applies default/max bounds to limit and offset so that
// the handler response matches the values the store actually uses.
func ClampPaginationParams(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 25
	} else if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// fuzzyMatch checks if all characters in query appear in text in order (case-insensitive)
// Example: "gpt4" matches "gpt-4", "gpt-4-turbo", etc.
func fuzzyMatch(text, query string) bool {
	if query == "" {
		return true
	}

	text = strings.ToLower(text)
	query = strings.ToLower(query)

	queryIndex := 0
	queryRunes := []rune(query)

	for _, textChar := range text {
		if queryIndex < len(queryRunes) && textChar == queryRunes[queryIndex] {
			queryIndex++
		}
	}

	return queryIndex == len(queryRunes)
}
