// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
//
// This package handles the conversion of FastHTTP request contexts to Bifrost contexts,
// ensuring that important metadata and tracking information is preserved across the system.
// It supports propagation of both Prometheus metrics and Maxim tracing data through HTTP headers.
package lib

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"github.com/valyala/fasthttp"
)

const (
	// FastHTTPUserValueBifrostContext stores the active *schemas.BifrostContext on fasthttp.RequestCtx.
	// This allows transport middleware and request handlers to share the same context instance.
	FastHTTPUserValueBifrostContext = "__bifrost_context"
	// FastHTTPUserValueBifrostCancel stores the cancel func for the active shared Bifrost context.
	FastHTTPUserValueBifrostCancel = "__bifrost_context_cancel"
	// FastHTTPUserValueLargeResponseMode marks requests that streamed a large response body.
	// It is used by transport middleware to avoid re-buffering response bodies for post-hooks.
	FastHTTPUserValueLargeResponseMode = "__bifrost_large_response_mode"
	// FastHTTPUserValueModelCatalogResolution stores model catalog resolution metadata
	// set by prepare*Request functions when a provider was auto-resolved. Picked up
	// centrally in ConvertToBifrostContext to add the routing engine log.
	FastHTTPUserValueModelCatalogResolution = "__bifrost_model_catalog_resolution"
)

// ModelCatalogResolution carries the result of an automatic provider lookup so
// that ConvertToBifrostContext can emit the routing engine log in one place.
type ModelCatalogResolution struct {
	Model            string
	ResolvedProvider schemas.ModelProvider
	AllProviders     []schemas.ModelProvider
}

// ParseSessionIDFromBaggage extracts the session-id baggage member value.
// It supports simple W3C baggage parsing sufficient for log grouping.
func ParseSessionIDFromBaggage(header string) string {
	for _, member := range strings.Split(header, ",") {
		member = strings.TrimSpace(member)
		if member == "" {
			continue
		}

		parts := strings.SplitN(member, ";", 2)
		kv := strings.SplitN(strings.TrimSpace(parts[0]), "=", 2)
		if len(kv) != 2 {
			continue
		}

		key := strings.ToLower(strings.TrimSpace(kv[0]))
		value := strings.TrimSpace(kv[1])
		if key != "session-id" || value == "" {
			continue
		}
		if len(value) > 255 {
			if logger != nil {
				logger.Warn("session-id exceeds 255 chars, ignoring: length=%d, prefix=%s", len(value), value[:255])
			}
			continue
		}
		return value
	}
	return ""
}

// ConvertToBifrostContext converts a FastHTTP RequestCtx to a Bifrost context,
// preserving important header values for monitoring and tracing purposes.
//
// The function processes several types of special headers:
// 1. Dimension Headers (x-bf-dim-*):
//   - All headers prefixed with 'x-bf-dim-' are collected into a map[string]string stored under
//     schemas.BifrostContextKeyDimensions and are forwarded to all observability integrations
//     (internal logs, OTEL spans, Prometheus custom labels, Datadog, etc.).
//   - The prefix is stripped and the remainder becomes the dimension key.
//   - Example: 'x-bf-dim-environment' with value 'production' stores {"environment": "production"}.
//
// 1a. Prometheus Headers (x-bf-prom-*) [DEPRECATED — use x-bf-dim-* instead]:
//   - All headers prefixed with 'x-bf-prom-' are still accepted for backward compatibility.
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
//
// 8. Session Stickiness Headers:
//   - x-bf-session-id: Session identifier for key binding (reuse same key across requests)
//   - x-bf-session-ttl: Per-request TTL override (duration string e.g. "30m" or seconds integer)
//
// 9. Raw Capture Headers (per-request override of provider config; accepts "true" or "false"):
//   - x-bf-send-back-raw-request: include raw provider request in the BifrostResponse returned to the caller
//   - x-bf-send-back-raw-response: include raw provider response in the BifrostResponse returned to the caller
//   - x-bf-store-raw-request-response: capture raw request/response for logging only (stripped from client response)

// Parameters:
//   - ctx: The FastHTTP request context containing the original headers
//   - store: HandlerStore providing per-request policy flags and header matchers
//
// Returns:
//   - *schemas.BifrostContext: A new cancellable context containing the propagated values
//   - context.CancelFunc: Function to cancel the context (should be called when request completes)
//
// Example Usage:
//
//	fastCtx := &fasthttp.RequestCtx{...}
//	bifrostCtx, cancel := ConvertToBifrostContext(fastCtx, handlerStore)
//	defer cancel() // Ensure cleanup
//	// bifrostCtx now contains propagated header values including Prometheus metrics,
//	// Maxim tracing data, MCP filters, governance keys, API keys, cache settings,
//	// session stickiness, and extra headers

func ConvertToBifrostContext(ctx *fasthttp.RequestCtx, store HandlerStore) (*schemas.BifrostContext, context.CancelFunc) {
	var matcher *HeaderMatcher
	mcpHeaderCombinedAllowlist := schemas.WhiteList{}
	allowPerRequestStorageOverride := false
	allowPerRequestRawOverride := false
	if store != nil {
		matcher = store.GetHeaderMatcher()
		mcpHeaderCombinedAllowlist = store.GetMCPHeaderCombinedAllowlist()
		allowPerRequestStorageOverride = store.ShouldAllowPerRequestStorageOverride()
		allowPerRequestRawOverride = store.ShouldAllowPerRequestRawOverride()
	}
	// Reuse a shared request-scoped context when available.
	var bifrostCtx *schemas.BifrostContext
	var cancel context.CancelFunc
	if existing, ok := ctx.UserValue(FastHTTPUserValueBifrostContext).(*schemas.BifrostContext); ok && existing != nil {
		if existingCancel, ok := ctx.UserValue(FastHTTPUserValueBifrostCancel).(context.CancelFunc); ok && existingCancel != nil {
			bifrostCtx = existing
			cancel = existingCancel
		} else {
			// Create one cancellable child context and promote it as the shared context.
			bifrostCtx, cancel = schemas.NewBifrostContextWithCancel(existing)
			ctx.SetUserValue(FastHTTPUserValueBifrostContext, bifrostCtx)
			ctx.SetUserValue(FastHTTPUserValueBifrostCancel, cancel)
		}
	}
	if bifrostCtx == nil {
		// Create cancellable context for requests that don't have a shared context yet.
		parent := context.Context(ctx)
		func() {
			// Zero-value fasthttp.RequestCtx can panic on Done(); fall back safely.
			defer func() {
				if recover() != nil {
					parent = context.Background()
				}
			}()
			_ = ctx.Done()
		}()
		bifrostCtx, cancel = schemas.NewBifrostContextWithCancel(parent)
		ctx.SetUserValue(FastHTTPUserValueBifrostContext, bifrostCtx)
		ctx.SetUserValue(FastHTTPUserValueBifrostCancel, cancel)
	}

	// Preserve existing request-id if already present on the shared context.
	if existingRequestID, ok := bifrostCtx.Value(schemas.BifrostContextKeyRequestID).(string); !ok || existingRequestID == "" {
		// First, check if x-request-id header exists
		requestID := string(ctx.Request.Header.Peek("x-request-id"))
		if requestID == "" {
			requestID = uuid.New().String()
		}
		bifrostCtx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	}
	// Populating all user values from the request context
	ctx.VisitUserValuesAll(func(key, value any) {
		bifrostCtx.SetValue(key, value)
	})

	// When a prepare*Request function resolved a provider via the model catalog,
	// it stores the resolution info on the fasthttp context. Emit the routing
	// engine log and mark the engine as used centrally here.
	if res, ok := ctx.UserValue(FastHTTPUserValueModelCatalogResolution).(*ModelCatalogResolution); ok && res != nil {
		providerStrs := make([]string, len(res.AllProviders))
		for i, p := range res.AllProviders {
			providerStrs[i] = string(p)
		}
		bifrostCtx.AppendRoutingEngineLog(schemas.RoutingEngineModelCatalog, schemas.LogLevelInfo, fmt.Sprintf(
			"No provider specified for model %s, found %d options in model catalog: [%s], selecting first: %s",
			res.Model, len(res.AllProviders), strings.Join(providerStrs, ", "), res.ResolvedProvider,
		))
		schemas.AppendToContextList(bifrostCtx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineModelCatalog)
	}

	// Initialize tags map for collecting maxim tags
	maximTags := make(map[string]string)
	// Initialize dimensions map for x-bf-dim-* headers
	dimensions := make(map[string]string)
	// Initialize extra headers map for headers prefixed with x-bf-eh-
	extraHeaders := make(map[string][]string)
	// Initialize extra headers map for headers in the mcp header combined allowlist
	mcpExtraHeaders := make(map[string][]string)
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
		"x-api-key":       true,
		"x-goog-api-key":  true,
		"x-bf-api-key":    true,
		"x-bf-api-key-id": true,
		"x-bf-vk":         true,
	}

	// Debug: Log header matcher state
	if logger != nil {
		if matcher != nil {
			logger.Debug("headerMatcher hasAllowlist=%v, hasDenylist=%v", matcher.HasAllowlist(), matcher.hasDenylist)
		} else {
			logger.Debug("headerMatcher is nil (allow all)")
		}
	}

	// Then process other headers
	ctx.Request.Header.All()(func(key, value []byte) bool {
		keyStr := strings.ToLower(string(key))
		if keyStr == "baggage" {
			if sessionID := ParseSessionIDFromBaggage(string(value)); sessionID != "" {
				bifrostCtx.SetValue(schemas.BifrostContextKeyParentRequestID, sessionID)
			}
			return true
		}
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-dim-"); ok && labelName != "" {
			if labelName != "path" && labelName != "method" {
				dimensions[labelName] = string(value)
			}
			return true
		}
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-prom-"); ok && labelName != "" {
			// x-bf-prom-* is Prometheus-only and must not flow into unified dimensions
			// (logs/OTEL/Maxim/etc). Prometheus plugin reads headers directly.
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
				} else {
					parsedValues = []string{""}
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
		if keyStr == "x-bf-api-key-id" {
			if keyID := strings.TrimSpace(string(value)); keyID != "" {
				bifrostCtx.SetValue(schemas.BifrostContextKeyAPIKeyID, keyID)
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
		// Session stickiness: session ID for key binding
		if keyStr == "x-bf-session-id" {
			if valueStr := strings.TrimSpace(string(value)); valueStr != "" {
				bifrostCtx.SetValue(schemas.BifrostContextKeySessionID, valueStr)
			}
			return true
		}
		// Session stickiness: per-request TTL override (duration string or seconds integer)
		if keyStr == "x-bf-session-ttl" {
			valueStr := strings.TrimSpace(string(value))
			var ttlDuration time.Duration
			var err error
			if ttlDuration, err = time.ParseDuration(valueStr); err != nil {
				if seconds, parseErr := strconv.Atoi(valueStr); parseErr == nil && seconds > 0 {
					ttlDuration = time.Duration(seconds) * time.Second
					err = nil
				}
			}
			if err == nil && ttlDuration > 0 {
				bifrostCtx.SetValue(schemas.BifrostContextKeySessionTTL, ttlDuration)
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
			if matcher != nil && !matcher.ShouldAllow(labelName) {
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
		if matcher != nil && matcher.HasAllowlist() {
			if matcher.MatchesAllow(keyStr) {
				// Skip reserved x-bf-* headers (handled separately)
				if strings.HasPrefix(keyStr, "x-bf-") {
					return true
				}
				// Validate against security denylist (always enforced)
				if securityDenylist[keyStr] {
					return true
				}
				// Check denylist
				if matcher.MatchesDeny(keyStr) {
					return true
				}
				// Forward the header directly with its original name
				if logger != nil {
					logger.Debug("forwarding header via allowlist: %s", keyStr)
				}
				extraHeaders[keyStr] = append(extraHeaders[keyStr], string(value))
				return true
			}
		}
		// Handle MCP extra headers
		if mcpHeaderCombinedAllowlist.IsAllowed(keyStr) {
			mcpExtraHeaders[keyStr] = append(mcpExtraHeaders[keyStr], string(value))
			return true
		}
		// Raw capture headers — all three support "true"/"false" to fully override the
		// provider-level config for this request.
		if keyStr == "x-bf-send-back-raw-request" {
			if b, err := strconv.ParseBool(string(value)); err == nil {
				bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawRequest, b)
			}
			return true
		}
		if keyStr == "x-bf-send-back-raw-response" {
			if b, err := strconv.ParseBool(string(value)); err == nil {
				bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawResponse, b)
			}
			return true
		}
		if keyStr == "x-bf-store-raw-request-response" {
			if b, err := strconv.ParseBool(string(value)); err == nil {
				bifrostCtx.SetValue(schemas.BifrostContextKeyStoreRawRequestResponse, b)
			}
			return true
		}
		if keyStr == "x-bf-disable-content-logging" {
			if b, err := strconv.ParseBool(string(value)); err == nil {
				bifrostCtx.SetValue(schemas.BifrostContextKeyDisableContentLogging, b)
			}
			return true
		}
		// Parent request ID header (for linking MCP tool calls to parent LLM requests)
		if keyStr == "x-bf-parent-request-id" {
			if valueStr := strings.TrimSpace(string(value)); valueStr != "" {
				bifrostCtx.SetValue(schemas.BifrostMCPAgentOriginalRequestID, valueStr)
			}
			return true
		}
		// Add passthrough extra params header support
		if keyStr == "x-bf-passthrough-extra-params" {
			if valueStr := string(value); valueStr == "true" {
				bifrostCtx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
			}
			return true
		}
		if keyStr == "x-bf-disable-content-logging" {
			if b, err := strconv.ParseBool(string(value)); err == nil {
				bifrostCtx.SetValue(schemas.BifrostContextKeyDisableContentLogging, b)
			}
			return true
		}

		// Compat header: per-request override of compat plugin settings.
		// Accepts: "true" (enable all), JSON array of feature names, or ["*"] (enable all).
		// An empty array [] or absent header means no overrides.
		if keyStr == "x-bf-compat" {
			bifrostCtx.ClearValue(schemas.BifrostContextKeyCompatConvertTextToChat)
			bifrostCtx.ClearValue(schemas.BifrostContextKeyCompatConvertChatToResponses)
			bifrostCtx.ClearValue(schemas.BifrostContextKeyCompatShouldDropParams)
			bifrostCtx.ClearValue(schemas.BifrostContextKeyCompatShouldConvertParams)
			valueStr := strings.TrimSpace(string(value))
			if valueStr == "true" {
				bifrostCtx.SetValue(schemas.BifrostContextKeyCompatConvertTextToChat, true)
				bifrostCtx.SetValue(schemas.BifrostContextKeyCompatConvertChatToResponses, true)
				bifrostCtx.SetValue(schemas.BifrostContextKeyCompatShouldDropParams, true)
				bifrostCtx.SetValue(schemas.BifrostContextKeyCompatShouldConvertParams, true)
			} else if strings.HasPrefix(valueStr, "[") {
				var features []string
				if err := json.Unmarshal([]byte(valueStr), &features); err == nil {
					if len(features) == 1 && features[0] == "*" {
						bifrostCtx.SetValue(schemas.BifrostContextKeyCompatConvertTextToChat, true)
						bifrostCtx.SetValue(schemas.BifrostContextKeyCompatConvertChatToResponses, true)
						bifrostCtx.SetValue(schemas.BifrostContextKeyCompatShouldDropParams, true)
						bifrostCtx.SetValue(schemas.BifrostContextKeyCompatShouldConvertParams, true)
					} else {
						for _, f := range features {
							switch f {
							case "convert_text_to_chat":
								bifrostCtx.SetValue(schemas.BifrostContextKeyCompatConvertTextToChat, true)
							case "convert_chat_to_responses":
								bifrostCtx.SetValue(schemas.BifrostContextKeyCompatConvertChatToResponses, true)
							case "should_drop_params":
								bifrostCtx.SetValue(schemas.BifrostContextKeyCompatShouldDropParams, true)
							case "should_convert_params":
								bifrostCtx.SetValue(schemas.BifrostContextKeyCompatShouldConvertParams, true)
							}
						}
					}
				}
			}
			return true
		}
		return true
	})

	// Store the collected maxim tags in the context
	if len(maximTags) > 0 {
		bifrostCtx.SetValue(schemas.BifrostContextKey(maxim.TagsKey), maximTags)
	}

	// Store collected dimensions (x-bf-dim-* only) in the context
	if len(dimensions) > 0 {
		bifrostCtx.SetValue(schemas.BifrostContextKeyDimensions, dimensions)
	}

	// Store collected extra headers in the context if any were found
	if len(extraHeaders) > 0 {
		bifrostCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, extraHeaders)
	}

	// Store collected MCP extra headers in the context if any were found
	if len(mcpExtraHeaders) > 0 {
		bifrostCtx.SetValue(schemas.BifrostContextKeyMCPExtraHeaders, mcpExtraHeaders)
	}

	// Collect all request headers for downstream use (e.g., governance required headers check)
	// Keys are lowercased for case-insensitive lookup
	allHeaders := make(map[string]string)
	ctx.Request.Header.All()(func(key, value []byte) bool {
		allHeaders[strings.ToLower(string(key))] = string(value)
		return true
	})
	bifrostCtx.SetValue(schemas.BifrostContextKeyRequestHeaders, allHeaders)

	// Extract per-user MCP OAuth user identifier from X-Bf-User-Id header
	if mcpUserID := string(ctx.Request.Header.Peek("X-Bf-User-Id")); mcpUserID != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyMCPUserID, mcpUserID)
	}

	// Build and set OAuth redirect URI for per-user OAuth flows. Bifrost is acting as
	// the OAuth client to upstream MCP servers here, so use the client-side override.
	var externalClientURL string
	if store != nil {
		externalClientURL = store.GetMCPExternalClientURL()
	}
	baseURL := BuildBaseURL(ctx, externalClientURL)
	if baseURL != "" {
		bifrostCtx.SetValue(schemas.BifrostContextKeyOAuthRedirectURI, baseURL+"/api/oauth/callback")
	}

	bifrostCtx.SetValue(schemas.BifrostContextKeyAllowPerRequestStorageOverride, allowPerRequestStorageOverride)
	bifrostCtx.SetValue(schemas.BifrostContextKeyAllowPerRequestRawOverride, allowPerRequestRawOverride)
	return bifrostCtx, cancel
}

// BuildBaseURL returns the effective base URL for OAuth callbacks and metadata discovery.
// When externalBaseURL is non-empty (set via config/UI/API), it takes priority so that
// deployments behind a reverse proxy advertise the proxy's public URL rather than the
// internal Host header seen by Bifrost.
func BuildBaseURL(ctx *fasthttp.RequestCtx, externalBaseURL string) string {
	if override := strings.TrimRight(strings.TrimSpace(externalBaseURL), "/"); override != "" {
		if parsed, err := url.Parse(override); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return override
		}
	}
	scheme := "http"
	xfProto := strings.ToLower(strings.TrimSpace(string(ctx.Request.Header.Peek("X-Forwarded-Proto"))))
	if comma := strings.IndexByte(xfProto, ','); comma >= 0 {
		xfProto = strings.TrimSpace(xfProto[:comma])
	}
	if ctx.IsTLS() || xfProto == "https" {
		scheme = "https"
	}
	host := string(ctx.Host())
	if host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s", scheme, host)
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

// BuildHTTPResponseFromFastHTTP creates an HTTPResponse snapshot from fasthttp context.
// Only captures status code and headers — body is skipped because for streaming
// responses it is an active io.Reader that cannot be materialized.
// The returned response should be released with schemas.ReleaseHTTPResponse when done.
func BuildHTTPResponseFromFastHTTP(ctx *fasthttp.RequestCtx) *schemas.HTTPResponse {
	resp := schemas.AcquireHTTPResponse()
	resp.StatusCode = ctx.Response.StatusCode()
	for key, value := range ctx.Response.Header.All() {
		resp.Headers[string(key)] = string(value)
	}
	return resp
}
