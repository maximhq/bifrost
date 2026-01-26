// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
//
// This package handles the conversion of FastHTTP request contexts to Bifrost contexts,
// ensuring that important metadata and tracking information is preserved across the system.
// It supports propagation of both Prometheus metrics and Maxim tracing data through HTTP headers.
package lib

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/valyala/fasthttp"
)

// ConvertToBifrostContext converts a FastHTTP RequestCtx to a Bifrost context,
// preserving important header values for monitoring and tracing purposes.
//
// The function processes several types of special headers:
// 1. Prometheus Headers (x-bf-prom-*):
//   - All headers prefixed with 'x-bf-prom-' are copied to the context
//   - The prefix is stripped and the remainder becomes the context key
//   - Example: 'x-bf-prom-latency' becomes 'latency' in the context
//
// 2. Maxim Tracing Headers (x-bf-maxim-*):
//   - Specifically handles 'x-bf-maxim-traceID' and 'x-bf-maxim-generationID'
//   - These headers enable trace correlation across service boundaries
//   - Values are stored using Maxim's context keys for consistency
//
// 3. MCP Headers (x-bf-mcp-*):
//   - Specifically handles 'x-bf-mcp-include-clients' and 'x-bf-mcp-include-tools' (include-only filtering)
//   - These headers enable MCP client and tool filtering
//   - Values are stored using MCP context keys for consistency
//
// 4. Governance Headers:
//   - x-bf-vk: Virtual key for governance (required for governance to work)
//
// 5. API Key Headers:
//   - Authorization: Bearer token format only (e.g., "Bearer sk-...") - OpenAI style
//   - x-api-key: Direct API key value - Anthropic style
//   - x-goog-api-key: Direct API key value - Google Gemini style
// 	 - x-bf-api-key references a stored API key name rather than the raw secret.
//   - Keys are extracted and stored in the context using schemas.BifrostContextKey
//   - This enables explicit key usage for requests via headers
//
// 6. Cancellable Context:
//   - Creates a cancellable context that can be used to cancel upstream requests when clients disconnect
//   - This is critical for streaming requests where write errors indicate client disconnects
//   - Also useful for non-streaming requests to allow provider-level cancellation
//
// 7. Extra Headers (x-bf-eh-*):
//   - Any header starting with 'x-bf-eh-' is collected and added to the map stored under schemas.BifrostContextKeyExtraHeaders
//   - The prefix is stripped, the remainder is lower-cased, and duplicate names append values
//   - This allows callers to send arbitrary context metadata without needing to extend the public schema

// Parameters:
//   - ctx: The FastHTTP request context containing the original headers
//   - allowDirectKeys: Whether to allow direct API key usage from headers
//
// Returns:
//   - *context.Context: A new cancellable context.Context containing the propagated values
//   - context.CancelFunc: Function to cancel the context (should be called when request completes)
//
// Example Usage:
//
//	fastCtx := &fasthttp.RequestCtx{...}
//	bifrostCtx, cancel := ConvertToBifrostContext(fastCtx, true, nil)
//	defer cancel() // Ensure cleanup
//	// bifrostCtx now contains propagated header values including Prometheus metrics,
//	// Maxim tracing data, MCP filters, governance keys, API keys, cache settings, and extra headers

func ConvertToBifrostContext(ctx *fasthttp.RequestCtx, allowDirectKeys bool, headerFilterConfig *configstoreTables.GlobalHeaderFilterConfig) (*schemas.BifrostContext, context.CancelFunc) {
	// Create cancellable context for all requests
	// This enables proper cleanup when clients disconnect or requests are cancelled
	bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(ctx)

	// First, check if x-request-id header exists
	requestID := string(ctx.Request.Header.Peek("x-request-id"))
	if requestID == "" {
		requestID = uuid.New().String()
	}
	bifrostCtx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	// Populating all user values from the request context
	ctx.VisitUserValuesAll(func(key, value any) {
		bifrostCtx.SetValue(key, value)
	})
	// Initialize tags map for collecting maxim tags
	maximTags := make(map[string]string)
	// Initialize extra headers map for headers prefixed with x-bf-eh-
	extraHeaders := make(map[string][]string)
	// Security denylist of header names that should never be accepted (case-insensitive)
	// This denylist is always enforced regardless of user configuration
	securityDenylist := map[string]bool{
		"proxy-authorization": true,
		"cookie":              true,
		"host":                true,
		"content-length":      true,
		"connection":          true,
		"transfer-encoding":   true,

		// prevent auth/key overrides via x-bf-eh-*
		"x-api-key":      true,
		"x-goog-api-key": true,
		"x-bf-api-key":   true,
		"x-bf-vk":        true,
	}

	// shouldAllowHeader determines if a header should be forwarded based on
	// the configurable header filter config (separate from security denylist)
	// Filter logic:
	// 1. If allowlist is non-empty, header must be in allowlist
	// 2. If denylist is non-empty, header must not be in denylist
	// 3. If both are non-empty, allowlist takes precedence first, then denylist filters
	// 4. If both are empty, header is allowed
	shouldAllowHeader := func(headerName string) bool {
		if headerFilterConfig == nil {
			return true
		}
		hasAllowlist := len(headerFilterConfig.Allowlist) > 0
		hasDenylist := len(headerFilterConfig.Denylist) > 0
		// If allowlist is non-empty, header must be in allowlist
		if hasAllowlist {
			if !slices.ContainsFunc(headerFilterConfig.Allowlist, func(s string) bool {
				return strings.EqualFold(s, headerName)
			}) {
				return false
			}
		}
		// If denylist is non-empty, header must not be in denylist
		if hasDenylist {
			if slices.ContainsFunc(headerFilterConfig.Denylist, func(s string) bool {
				return strings.EqualFold(s, headerName)
			}) {
				return false

			}
		}
		return true
	}

	// Debug: Log header filter config
	if headerFilterConfig != nil {
		logger.Debug("headerFilterConfig allowlist: %v, denylist: %v", headerFilterConfig.Allowlist, headerFilterConfig.Denylist)
	} else {
		logger.Debug("headerFilterConfig is nil")
	}

	// Then process other headers
	ctx.Request.Header.All()(func(key, value []byte) bool {
		keyStr := strings.ToLower(string(key))
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-prom-"); ok {
			bifrostCtx.SetValue(schemas.BifrostContextKey(labelName), string(value))
			return true
		}
		// Checking for maxim headers
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-maxim-"); ok {
			switch labelName {
			case string(maxim.GenerationIDKey):
				bifrostCtx.SetValue(schemas.BifrostContextKey(labelName), string(value))
			case string(maxim.TraceIDKey):
				bifrostCtx.SetValue(schemas.BifrostContextKey(labelName), string(value))
			case string(maxim.SessionIDKey):
				bifrostCtx.SetValue(schemas.BifrostContextKey(labelName), string(value))
			case string(maxim.TraceNameKey):
				bifrostCtx.SetValue(schemas.BifrostContextKey(labelName), string(value))
			case string(maxim.GenerationNameKey):
				bifrostCtx.SetValue(schemas.BifrostContextKey(labelName), string(value))
			case string(maxim.LogRepoIDKey):
				bifrostCtx.SetValue(schemas.BifrostContextKey(labelName), string(value))
			default:
				// apart from these all headers starting with x-bf-maxim- are keys for tags
				// collect them in the maximTags map
				maximTags[labelName] = string(value)
			}
			return true
		}
		// MCP control headers (include-only filtering)
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-mcp-"); ok {
			switch labelName {
			case "include-clients":
				fallthrough
			case "include-tools":
				// Parse comma-separated values into []string
				valueStr := string(value)
				var parsedValues []string
				if valueStr != "" {
					// Split by comma and trim whitespace
					for _, v := range strings.Split(valueStr, ",") {
						if trimmed := strings.TrimSpace(v); trimmed != "" {
							parsedValues = append(parsedValues, trimmed)
						}
					}
				}
				bifrostCtx.SetValue(schemas.BifrostContextKey("mcp-"+labelName), parsedValues)
				return true
			}
		}
		// Handle virtual key header (x-bf-vk, authorization, x-api-key, x-goog-api-key headers)
		if keyStr == string(schemas.BifrostContextKeyVirtualKey) {
			bifrostCtx.SetValue(schemas.BifrostContextKeyVirtualKey, string(value))
			return true
		}
		if keyStr == "authorization" {
			valueStr := string(value)
			// Only accept Bearer token format: "Bearer ..."
			if strings.HasPrefix(strings.ToLower(valueStr), "bearer ") {
				authHeaderValue := strings.TrimSpace(valueStr[7:]) // Remove "Bearer " prefix
				if authHeaderValue != "" && strings.HasPrefix(strings.ToLower(authHeaderValue), governance.VirtualKeyPrefix) {
					bifrostCtx.SetValue(schemas.BifrostContextKeyVirtualKey, authHeaderValue)
					return true
				}
			}
		}
		if keyStr == "x-api-key" && strings.HasPrefix(strings.ToLower(string(value)), governance.VirtualKeyPrefix) {
			bifrostCtx.SetValue(schemas.BifrostContextKeyVirtualKey, string(value))
			return true
		}
		if keyStr == "x-goog-api-key" && strings.HasPrefix(strings.ToLower(string(value)), governance.VirtualKeyPrefix) {
			bifrostCtx.SetValue(schemas.BifrostContextKeyVirtualKey, string(value))
			return true
		}
		if keyStr == "x-bf-api-key" {
			if keyName := strings.TrimSpace(string(value)); keyName != "" {
				bifrostCtx.SetValue(schemas.BifrostContextKeyAPIKeyName, keyName)
			}
			return true
		}
		// Handle cache key header (x-bf-cache-key)
		if keyStr == "x-bf-cache-key" {
			bifrostCtx.SetValue(semanticcache.CacheKey, string(value))
			return true
		}
		// Handle cache TTL header (x-bf-cache-ttl)
		if keyStr == "x-bf-cache-ttl" {
			valueStr := string(value)
			var ttlDuration time.Duration
			var err error

			// First try to parse as duration (e.g., "30s", "5m", "1h")
			if ttlDuration, err = time.ParseDuration(valueStr); err != nil {
				// If that fails, try to parse as plain number and treat as seconds
				if seconds, parseErr := strconv.Atoi(valueStr); parseErr == nil && seconds > 0 {
					ttlDuration = time.Duration(seconds) * time.Second
					err = nil // Reset error since we successfully parsed as seconds
				}
			}

			if err == nil {
				bifrostCtx.SetValue(semanticcache.CacheTTLKey, ttlDuration)
			}
			// If both parsing attempts fail, we silently ignore the header and use default TTL
			return true
		}
		// Cache threshold header
		if keyStr == "x-bf-cache-threshold" {
			threshold, err := strconv.ParseFloat(string(value), 64)
			if err == nil {
				// Clamp threshold to the inclusive range [0.0, 1.0]
				if threshold < 0.0 {
					threshold = 0.0
				} else if threshold > 1.0 {
					threshold = 1.0
				}
				bifrostCtx.SetValue(semanticcache.CacheThresholdKey, threshold)
			}
			// If parsing fails, silently ignore the header (no context value set)
			return true
		}
		// Cache type header
		if keyStr == "x-bf-cache-type" {
			bifrostCtx.SetValue(semanticcache.CacheTypeKey, semanticcache.CacheType(string(value)))
			return true
		}
		// Cache no store header
		if keyStr == "x-bf-cache-no-store" {
			if valueStr := string(value); valueStr == "true" {
				bifrostCtx.SetValue(semanticcache.CacheNoStoreKey, true)
			}
			return true
		}
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-eh-"); ok {
			// Skip empty header names after prefix removal
			if labelName == "" {
				return true
			}
			// Normalize header name to lowercase
			labelName = strings.ToLower(labelName)
			// Validate against security denylist (always enforced)
			if securityDenylist[labelName] {
				return true
			}
			// Apply configurable header filter
			if !shouldAllowHeader(labelName) {
				return true
			}
			// Append header value (allow multiple values for the same header)
			extraHeaders[labelName] = append(extraHeaders[labelName], string(value))
			return true
		}
		// Direct header forwarding: when allowlist is configured, any header explicitly
		// in the allowlist can be forwarded directly without the x-bf-eh- prefix.
		// This enables forwarding arbitrary headers like "anthropic-beta" directly.
		// Only applies when allowlist is non-empty (backward compatible).
		if headerFilterConfig != nil && len(headerFilterConfig.Allowlist) > 0 {
			// Check if this header is explicitly in the allowlist (case-insensitive)
			if slices.ContainsFunc(headerFilterConfig.Allowlist, func(s string) bool {
				return strings.EqualFold(s, keyStr)
			}) {
				// Skip reserved x-bf-* headers (handled separately)
				if strings.HasPrefix(keyStr, "x-bf-") {
					return true
				}
				// Validate against security denylist (always enforced)
				if securityDenylist[keyStr] {
					return true
				}
				// Check denylist (allowlist check already passed by being in allowlist, case-insensitive)
				if len(headerFilterConfig.Denylist) > 0 && slices.ContainsFunc(headerFilterConfig.Denylist, func(s string) bool {
					return strings.EqualFold(s, keyStr)
				}) {
					return true
				}
				// Forward the header directly with its original name
				logger.Debug("forwarding header via allowlist: %s = %s", keyStr, string(value))
				extraHeaders[keyStr] = append(extraHeaders[keyStr], string(value))
				return true
			}
		}
		// Send back raw response header
		if keyStr == "x-bf-send-back-raw-response" {
			if valueStr := string(value); valueStr == "true" {
				bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
			}
			return true
		}
		return true
	})

	// Store the collected maxim tags in the context
	if len(maximTags) > 0 {
		bifrostCtx.SetValue(schemas.BifrostContextKey(maxim.TagsKey), maximTags)
	}

	// Store collected extra headers in the context if any were found
	if len(extraHeaders) > 0 {
		bifrostCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, extraHeaders)
	}

	if allowDirectKeys {
		// Extract API key from Authorization header (Bearer format), x-api-key, or x-goog-api-key header
		var apiKey string

		// TODO: fix plugin data leak
		// Check Authorization header (Bearer format only - OpenAI style)
		authHeader := string(ctx.Request.Header.Peek("Authorization"))
		if authHeader != "" {
			// Only accept Bearer token format: "Bearer ..."
			if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				authHeaderValue := strings.TrimSpace(authHeader[7:]) // Remove "Bearer " prefix
				if authHeaderValue != "" && !strings.HasPrefix(strings.ToLower(authHeaderValue), governance.VirtualKeyPrefix) {
					apiKey = authHeaderValue
				}
			} else {
				apiKey = authHeader
			}
		}

		if apiKey == "" {
			// Check x-api-key (Anthropic style) header if no valid Authorization header found
			xAPIKey := string(ctx.Request.Header.Peek("x-api-key"))
			if xAPIKey != "" && !strings.HasPrefix(strings.ToLower(xAPIKey), governance.VirtualKeyPrefix) {
				apiKey = strings.TrimSpace(xAPIKey)
			} else {
				// Check x-goog-api-key (Google Gemini style) header if no valid Authorization header found
				xGoogleAPIKey := string(ctx.Request.Header.Peek("x-goog-api-key"))
				if xGoogleAPIKey != "" && !strings.HasPrefix(strings.ToLower(xGoogleAPIKey), governance.VirtualKeyPrefix) {
					apiKey = strings.TrimSpace(xGoogleAPIKey)
				}
			}
		}

		// If we found an API key, create a Key object and store it in context
		if apiKey != "" {
			key := schemas.Key{
				ID:     "header-provided", // Identifier for header-provided keys
				Value:  *schemas.NewEnvVar(apiKey),
				Models: []string{}, // Empty models list - will be validated by provider
				Weight: 1.0,        // Default weight
			}
			bifrostCtx.SetValue(schemas.BifrostContextKeyDirectKey, key)
		}
	}
	return bifrostCtx, cancel
}

// BuildHTTPRequestFromFastHTTP creates an HTTPRequest from fasthttp context for streaming handlers.
// The returned request should be released with schemas.ReleaseHTTPRequest when done.
// Note: Body is not copied for streaming (body was already consumed for the request).
func BuildHTTPRequestFromFastHTTP(ctx *fasthttp.RequestCtx) *schemas.HTTPRequest {
	req := schemas.AcquireHTTPRequest()
	req.Method = string(ctx.Method())
	req.Path = string(ctx.Path())

	// Copy headers
	for key, value := range ctx.Request.Header.All() {
		req.Headers[string(key)] = string(value)
	}

	// Copy query params
	for key, value := range ctx.Request.URI().QueryArgs().All() {
		req.Query[string(key)] = string(value)
	}

	// Copy path parameters from user values
	ctx.VisitUserValuesAll(func(key, value any) {
		keyStr, keyIsString := key.(string)
		valueStr, valueIsString := value.(string)
		if !keyIsString || !valueIsString {
			return
		}
		if strings.HasPrefix(keyStr, "bifrost-") ||
			keyStr == "BifrostContextKeyRequestID" ||
			keyStr == "trace_id" ||
			keyStr == "span_id" {
			return
		}
		req.PathParams[keyStr] = valueStr
	})

	// Note: Body not copied - for streaming, body was already consumed
	return req
}
