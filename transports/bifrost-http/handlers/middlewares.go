package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

var loggingSkipPaths = []string{"/health", "/_next", "/api/dev"}
var realtimeTransportPaths = buildRealtimeTransportPathSet()

// SecurityHeadersMiddleware sets security-related HTTP headers on every response.
// This should wrap the outermost handler so all responses (API, UI, errors) include these headers.
func SecurityHeadersMiddleware() schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			ctx.Response.Header.Set("X-Frame-Options", "DENY")
			ctx.Response.Header.Set("X-Content-Type-Options", "nosniff")
			ctx.Response.Header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			ctx.Response.Header.Set("Content-Security-Policy", "frame-ancestors 'none'")
			ctx.Response.Header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			// Only set HSTS when serving over HTTPS (detected via reverse proxy header or direct TLS)
			if string(ctx.Request.Header.Peek("X-Forwarded-Proto")) == "https" || ctx.IsTLS() {
				ctx.Response.Header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next(ctx)
		}
	}
}

// CorsMiddleware handles CORS headers for localhost and configured allowed origins
func CorsMiddleware(config *lib.Config) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			startTime := time.Now()
			// skip logging if it's a /health check request
			if slices.IndexFunc(loggingSkipPaths, func(path string) bool {
				return strings.HasPrefix(string(ctx.RequestURI()), path)
			}) != -1 {
				goto corsFlow
			}
			defer func() {
				statusCode := ctx.Response.Header.StatusCode()
				level := schemas.LogLevelInfo
				if statusCode >= 500 {
					level = schemas.LogLevelError
				} else if statusCode >= 400 {
					level = schemas.LogLevelWarn
				}
				logBuilder := logger.LogHTTPRequest(level, "request completed").
					Str("http.method", string(ctx.Method())).
					Str("http.target", string(ctx.RequestURI())).
					Int("http.status_code", statusCode).
					Int64("http.request_duration_ms", time.Since(startTime).Milliseconds()).
					Str("http.remote_addr", ctx.RemoteAddr().String()).
					Str("http.user_agent", string(ctx.Request.Header.UserAgent()))
				if traceID, ok := ctx.UserValue(schemas.BifrostContextKeyTraceID).(string); ok && traceID != "" {
					logBuilder = logBuilder.Str("trace_id", traceID)
				}
				logBuilder.Send()
			}()
		corsFlow:
			origin := string(ctx.Request.Header.Peek("Origin"))
			allowed := IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins)
			// Credentialed responses are sent when the origin is not matched solely by a
			// wildcard AllowedOrigins — i.e. the origin is localhost or explicitly listed.
			credentialed := !slices.Contains(config.ClientConfig.AllowedOrigins, "*") ||
				isLocalhostOrigin(origin) ||
				slices.Contains(config.ClientConfig.AllowedOrigins, origin)

			allowedHeaders := []string{"Content-Type", "Authorization", "X-Requested-With", "X-Stainless-Timeout", "X-Api-Key", "X-OpenAI-Agents-SDK"}
			if slices.Contains(config.ClientConfig.AllowedHeaders, "*") {
				if credentialed {
					// Per the Fetch spec, Access-Control-Allow-Headers: * is NOT treated as a
					// wildcard when Access-Control-Allow-Credentials: true is set — browsers
					// interpret it as a literal header name. For credentialed preflight requests,
					// reflect back the requested headers instead.
					if requestedHeaders := string(ctx.Request.Header.Peek("Access-Control-Request-Headers")); requestedHeaders != "" {
						allowedHeaders = []string{requestedHeaders}
					}
					// For non-preflight requests (no Access-Control-Request-Headers), keep defaults.
				} else {
					allowedHeaders = []string{"*"}
				}
			} else if len(config.ClientConfig.AllowedHeaders) > 0 {
				// append allowed headers from config to the default headers
				for _, header := range config.ClientConfig.AllowedHeaders {
					if !slices.Contains(allowedHeaders, header) {
						allowedHeaders = append(allowedHeaders, header)
					}
				}
			}
			// Check if origin is allowed (localhost always allowed + configured origins)
			if allowed {
				ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
				ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD")
				ctx.Response.Header.Set("Access-Control-Allow-Headers", strings.Join(allowedHeaders, ", "))
				if credentialed {
					ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
				}
				ctx.Response.Header.Set("Access-Control-Max-Age", "86400")
				// Vary: Origin tells caches that the response varies based on the Origin
				// request header, preventing incorrect CORS headers from being served.
				ctx.Response.Header.Set("Vary", "Origin")
			}
			// Handle preflight OPTIONS requests
			if string(ctx.Method()) == "OPTIONS" {
				if allowed {
					ctx.SetStatusCode(fasthttp.StatusOK)
				} else {
					ctx.SetStatusCode(fasthttp.StatusForbidden)
				}
				return
			}
			next(ctx)
		}
	}
}

// RequestDecompressionMiddleware transparently decompresses compressed request bodies.
// Two paths based on compressed Content-Length:
//   - Large or chunked (CL > threshold or CL unknown): streaming decompression via
//     SetBodyStream, avoiding full body materialization. Uses pooled gzip readers
//     matching the response-side pattern in core/providers/utils.
//   - Small (CL ≤ threshold): buffered decompression via io.ReadAll + SetBodyRaw,
//     with decompression bomb protection via MaxRequestBodySizeMB.
func RequestDecompressionMiddleware(config *lib.Config) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			if len(ctx.Request.Header.ContentEncoding()) == 0 {
				next(ctx)
				return
			}

			if shouldStreamDecompress(config, ctx) {
				cleanup, applied, err := streamingDecompress(ctx)
				if err != nil {
					SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid compressed request body: %v", err))
					return
				}
				if applied {
					next(ctx)
					cleanup()
					return
				}
				// No body stream available (StreamRequestBody not enabled) — fall
				// through to the buffered decompression path below.
			}

			// Buffered path: small compressed request — materialize fully.
			maxRequestBodyBytes := 100 * 1024 * 1024 // default 100 MB (matches decodeRequestBodyWithLimit fallback)
			if config != nil && config.ClientConfig.MaxRequestBodySizeMB > 0 {
				maxRequestBodyBytes = config.ClientConfig.MaxRequestBodySizeMB * 1024 * 1024
			}

			body, err := decodeRequestBodyWithLimit(&ctx.Request, maxRequestBodyBytes)
			if errors.Is(err, errRequestBodyTooLarge) {
				SendError(ctx, fasthttp.StatusRequestEntityTooLarge, fmt.Sprintf("decompressed request body exceeds max allowed size of %d bytes", maxRequestBodyBytes))
				return
			}
			if err != nil {
				SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid compressed request body: %v", err))
				return
			}

			ctx.Request.SetBodyRaw(body)
			ctx.Request.Header.Del(fasthttp.HeaderContentEncoding)
			ctx.Request.Header.Del(fasthttp.HeaderContentLength)
			next(ctx)
		}
	}
}

// shouldStreamDecompress returns true when the compressed request body should
// use streaming decompression rather than full materialization. Uses the
// config threshold (set by enterprise from LargePayloadConfig.RequestThresholdBytes)
// or falls back to DefaultLargePayloadRequestThresholdBytes.
// Chunked requests (unknown size) always stream to be safe.
func shouldStreamDecompress(config *lib.Config, ctx *fasthttp.RequestCtx) bool {
	contentLength := ctx.Request.Header.ContentLength()
	// Chunked transfer encoding: fasthttp reports -1. Size unknown, stream to be safe.
	if contentLength < 0 {
		return true
	}
	var threshold int64 = schemas.DefaultLargePayloadRequestThresholdBytes
	if config != nil && config.StreamingDecompressThreshold > 0 {
		threshold = config.StreamingDecompressThreshold
	}
	return int64(contentLength) > threshold
}

// streamingDecompress wraps the request body stream with a streaming decompression
// reader, avoiding full body materialization for large compressed requests.
// Returns (cleanup, applied, err):
//   - applied=true: body stream was wrapped; caller must invoke cleanup after the
//     handler chain completes and the body is fully consumed.
//   - applied=false: no body stream available (StreamRequestBody not enabled on the
//     server). Caller should fall back to the buffered decompression path.
func streamingDecompress(ctx *fasthttp.RequestCtx) (cleanup func(), applied bool, err error) {
	bodyStream := ctx.RequestBodyStream()
	if bodyStream == nil {
		return func() {}, false, nil
	}

	encoding := strings.ToLower(strings.TrimSpace(
		string(ctx.Request.Header.ContentEncoding()),
	))

	decompReader, cleanup, err := newDecompressReader(bodyStream, encoding)
	if err != nil {
		return nil, false, err
	}

	ctx.Request.SetBodyStream(decompReader, -1)
	ctx.Request.Header.Del(fasthttp.HeaderContentEncoding)
	ctx.Request.Header.Del(fasthttp.HeaderContentLength)

	return cleanup, true, nil
}

var errRequestBodyTooLarge = errors.New("decompressed request body exceeds max allowed size")

// decodeRequestBodyWithLimit decodes the request body with a limit on the size of the body.
func decodeRequestBodyWithLimit(req *fasthttp.Request, maxRequestBodyBytes int) ([]byte, error) {
	encoding := strings.ToLower(strings.TrimSpace(string(req.Header.ContentEncoding())))
	bodyReader := bytes.NewReader(req.Body())

	var reader io.Reader = bodyReader
	cleanup := func() {}
	if encoding != "" {
		var err error
		reader, cleanup, err = newDecompressReader(bodyReader, encoding)
		if err != nil {
			return nil, err
		}
	}
	defer cleanup()

	if maxRequestBodyBytes <= 0 {
		maxRequestBodyBytes = 100 * 1024 * 1024 // 100 MB hard cap
	}

	limitedReader := &io.LimitedReader{R: reader, N: int64(maxRequestBodyBytes + 1)}
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}
	if len(body) > maxRequestBodyBytes {
		return nil, errRequestBodyTooLarge
	}
	return body, nil
}

// newDecompressReader wraps r with a decompression reader for the given encoding.
// All encodings use pooled readers from core/providers/utils. The returned cleanup
// function must be called when the reader is no longer needed.
func newDecompressReader(r io.Reader, encoding string) (io.Reader, func(), error) {
	switch encoding {
	case "gzip":
		gz, err := providerUtils.AcquireGzipReader(r)
		if err != nil {
			return nil, nil, err
		}
		return gz, func() { providerUtils.ReleaseGzipReader(gz) }, nil
	case "deflate":
		fr, err := providerUtils.AcquireFlateReader(r)
		if err != nil {
			return nil, nil, err
		}
		return fr, func() { providerUtils.ReleaseFlateReader(fr) }, nil
	case "br":
		br := providerUtils.AcquireBrotliReader(r)
		return br, func() { providerUtils.ReleaseBrotliReader(br) }, nil
	case "zstd":
		dec, err := providerUtils.AcquireZstdDecoder(r)
		if err != nil {
			return nil, nil, err
		}
		return dec, func() { providerUtils.ReleaseZstdDecoder(dec) }, nil
	default:
		return nil, nil, fmt.Errorf("%w: %q", fasthttp.ErrContentEncodingUnsupported, encoding)
	}
}

// TransportInterceptorMiddleware runs all plugin HTTP transport interceptors.
// It converts the fasthttp request to a serializable HTTPRequest, runs all plugin interceptors,
// and applies any modifications back to the fasthttp context.
func TransportInterceptorMiddleware(config *lib.Config) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			plugins := config.GetLoadedHTTPTransportPlugins()
			if len(plugins) == 0 {
				next(ctx)
				return
			}
			// Get or create BifrostContext from fasthttp context
			bifrostCtx := getBifrostContextFromFastHTTP(ctx)
			// Acquire pooled request
			req := schemas.AcquireHTTPRequest()
			defer schemas.ReleaseHTTPRequest(req)
			fasthttpToHTTPRequest(ctx, req)
			// Run plugin interceptors
			for _, plugin := range plugins {
				pluginName := plugin.GetName()
				pluginCtx := bifrostCtx.WithPluginScope(&pluginName)
				resp, err := plugin.HTTPTransportPreHook(pluginCtx, req)
				pluginCtx.ReleasePluginScope()
				if err != nil {
					// Short-circuit with error — drain plugin logs before returning
					if logs := bifrostCtx.DrainPluginLogs(); len(logs) > 0 {
						ctx.SetUserValue(schemas.BifrostContextKeyTransportPluginLogs, logs)
					}
					ctx.SetStatusCode(fasthttp.StatusInternalServerError)
					ctx.SetBodyString(err.Error())
					return
				}
				if resp != nil {
					// Short-circuit with response — drain plugin logs before returning
					if logs := bifrostCtx.DrainPluginLogs(); len(logs) > 0 {
						ctx.SetUserValue(schemas.BifrostContextKeyTransportPluginLogs, logs)
					}
					applyHTTPResponseToCtx(ctx, resp)
					return
				}
				// If we got here, the plugin may have modified req in-place
			}
			// Drain pre-hook plugin logs and store on fasthttp context for trace attachment
			if preHookLogs := bifrostCtx.DrainPluginLogs(); len(preHookLogs) > 0 {
				ctx.SetUserValue(schemas.BifrostContextKeyTransportPluginLogs, preHookLogs)
			}
			// Apply modifications back to fasthttp context
			applyHTTPRequestToCtx(ctx, req)
			// Adding user values
			for key, value := range bifrostCtx.GetUserValues() {
				ctx.SetUserValue(key, value)
			}
			next(ctx)

			// For streaming responses, store a callback to run post-hooks after the stream ends.
			// The streaming handler calls this before traceCompleter.
			if deferred, ok := ctx.UserValue(schemas.BifrostContextKeyDeferTraceCompletion).(bool); ok && deferred {
				ctx.SetUserValue(schemas.BifrostContextKeyTransportPostHookCompleter, func() {
					runTransportPostHooks(ctx, plugins, bifrostCtx)
				})
				return
			}

			runTransportPostHooks(ctx, plugins, bifrostCtx)
		}
	}
}

// runTransportPostHooks runs HTTPTransportPostHook for all plugins in reverse order,
// drains plugin logs, and applies the response back to the fasthttp context.
// Used for both non-streaming (inline) and streaming (deferred callback) paths.
//
// Transport-level plugin logs are stored in fasthttp UserValues (keyed by
// BifrostContextKeyTransportPluginLogs) rather than directly on BifrostContext,
// because transport hooks operate at the fasthttp layer before/after the core
// BifrostContext lifecycle. These logs are merged into the trace by the
// TracingMiddleware at trace completion, alongside core-level plugin logs
// which travel through BifrostContext → Trace → AttachPluginLogs.
func runTransportPostHooks(ctx *fasthttp.RequestCtx, plugins []schemas.HTTPTransportPlugin, bifrostCtx *schemas.BifrostContext) {
	httpResp := schemas.AcquireHTTPResponse()
	defer schemas.ReleaseHTTPResponse(httpResp)
	fasthttpResponseToHTTPResponse(ctx, httpResp)

	// Build request from current fasthttp state (original pooled req may have been released)
	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	fasthttpToHTTPRequest(ctx, req)

	// Run http post-hooks in reverse order
	for i := len(plugins) - 1; i >= 0; i-- {
		plugin := plugins[i]
		pluginName := plugin.GetName()
		pluginCtx := bifrostCtx.WithPluginScope(&pluginName)
		err := plugin.HTTPTransportPostHook(pluginCtx, req, httpResp)
		pluginCtx.ReleasePluginScope()
		if err != nil {
			logger.Warn("error in HTTPTransportPostHook for plugin %s: %s", pluginName, err.Error())
			// Drain plugin logs before returning on error
			if postHookLogs := bifrostCtx.DrainPluginLogs(); len(postHookLogs) > 0 {
				if existing, ok := ctx.UserValue(schemas.BifrostContextKeyTransportPluginLogs).([]schemas.PluginLogEntry); ok {
					ctx.SetUserValue(schemas.BifrostContextKeyTransportPluginLogs, append(existing, postHookLogs...))
				} else {
					ctx.SetUserValue(schemas.BifrostContextKeyTransportPluginLogs, postHookLogs)
				}
			}
			applyHTTPResponseToCtx(ctx, httpResp)
			return
		}
	}
	// Drain post-hook plugin logs and merge with pre-hook logs
	if postHookLogs := bifrostCtx.DrainPluginLogs(); len(postHookLogs) > 0 {
		if existing, ok := ctx.UserValue(schemas.BifrostContextKeyTransportPluginLogs).([]schemas.PluginLogEntry); ok {
			ctx.SetUserValue(schemas.BifrostContextKeyTransportPluginLogs, append(existing, postHookLogs...))
		} else {
			ctx.SetUserValue(schemas.BifrostContextKeyTransportPluginLogs, postHookLogs)
		}
	}
	applyHTTPResponseToCtx(ctx, httpResp)
}

// getBifrostContextFromFastHTTP gets or creates a BifrostContext from fasthttp context.
func getBifrostContextFromFastHTTP(ctx *fasthttp.RequestCtx) *schemas.BifrostContext {
	return schemas.NewBifrostContext(ctx, schemas.NoDeadline)
}

// fasthttpToHTTPRequest populates a pooled HTTPRequest from fasthttp context.
func fasthttpToHTTPRequest(ctx *fasthttp.RequestCtx, req *schemas.HTTPRequest) {
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
	// The fasthttp router stores path variables (like {file_id}, {model}) as user values
	// We extract all string user values that are likely path parameters
	ctx.VisitUserValuesAll(func(key, value any) {
		// Only process string keys and string values
		keyStr, keyIsString := key.(string)
		valueStr, valueIsString := value.(string)
		if !keyIsString || !valueIsString {
			return
		}
		// Skip internal Bifrost system keys and tracing keys
		if strings.HasPrefix(keyStr, "bifrost-") ||
			keyStr == "BifrostContextKeyRequestID" ||
			keyStr == "trace_id" ||
			keyStr == "span_id" {
			return
		}
		// Store as path parameter
		req.PathParams[keyStr] = valueStr
	})

	// Skip body copy for large payloads.
	// Check threshold first (set by RequestThresholdMiddleware before this middleware runs)
	// because the large-payload-mode flag is only set later inside the handler hook.
	if threshold, ok := ctx.UserValue(schemas.BifrostContextKeyLargePayloadRequestThreshold).(int64); ok && threshold > 0 {
		cl := int64(ctx.Request.Header.ContentLength())
		// Skip body copy when CL exceeds threshold OR CL is unknown (streaming/
		// chunked, e.g. after streaming decompression deletes the header).
		if cl > threshold || cl < 0 {
			return
		}
	}
	if isLargePayload, ok := ctx.UserValue(schemas.BifrostContextKeyLargePayloadMode).(bool); ok && isLargePayload {
		return
	}
	body := ctx.Request.Body()
	if len(body) > 0 {
		req.Body = make([]byte, len(body))
		copy(req.Body, body)
	}
}

// applyHTTPRequestToCtx applies modifications from HTTPRequest back to fasthttp context.
func applyHTTPRequestToCtx(ctx *fasthttp.RequestCtx, req *schemas.HTTPRequest) {
	// If path/method is different, throw error
	if req.Method != string(ctx.Method()) || req.Path != string(ctx.Path()) {
		logger.Error("request method/path mismatch: %s %s != %s %s", req.Method, req.Path, string(ctx.Method()), string(ctx.Path()))
		SendError(ctx, fasthttp.StatusConflict, "request method/path was modified by a plugin, this is not allowed")
		return
	}
	// Apply headers
	for key, value := range req.Headers {
		ctx.Request.Header.Set(key, value)
	}
	// Apply query params
	for key, value := range req.Query {
		ctx.Request.URI().QueryArgs().Set(key, value)
	}
	// Apply body if set
	if req.Body != nil {
		ctx.Request.SetBody(req.Body)
	}
}

// applyHTTPResponseToCtx writes a short-circuit response to fasthttp context.
func applyHTTPResponseToCtx(ctx *fasthttp.RequestCtx, resp *schemas.HTTPResponse) {
	ctx.SetStatusCode(resp.StatusCode)
	for key, value := range resp.Headers {
		ctx.Response.Header.Set(key, value)
	}
	if resp.Body != nil {
		ctx.SetBody(resp.Body)
	}
}

// fasthttpResponseToHTTPResponse populates a pooled HTTPResponse from fasthttp context.
func fasthttpResponseToHTTPResponse(ctx *fasthttp.RequestCtx, resp *schemas.HTTPResponse) {
	resp.StatusCode = ctx.Response.StatusCode()
	for key, value := range ctx.Response.Header.All() {
		resp.Headers[string(key)] = string(value)
	}
	// Skip response body copy when large payload/response mode is active — the response is
	// streamed directly to the client and materializing it here would spike memory.
	if isLargePayload, ok := ctx.UserValue(schemas.BifrostContextKeyLargePayloadMode).(bool); ok && isLargePayload {
		return
	}
	if isLargeResponse, ok := ctx.UserValue(lib.FastHTTPUserValueLargeResponseMode).(bool); ok && isLargeResponse {
		return
	}
	// Also skip if response Content-Length exceeds the configured response threshold.
	if threshold, ok := ctx.UserValue(schemas.BifrostContextKeyLargeResponseThreshold).(int64); ok && threshold > 0 {
		if int64(ctx.Response.Header.ContentLength()) > threshold {
			return
		}
	}
	body := ctx.Response.Body()
	if len(body) > 0 {
		resp.Body = make([]byte, len(body))
		copy(resp.Body, body)
	}
}

// validateSession checks if a session token is valid
func validateSession(_ *fasthttp.RequestCtx, store configstore.ConfigStore, token string) bool {
	session, err := store.GetSession(context.Background(), token)
	if err != nil || session == nil {
		return false
	}
	if session.ExpiresAt.Before(time.Now()) {
		return false
	}
	return true
}

// isInferenceWSEndpoint returns true for WebSocket endpoints that should use
// standard inference auth (Bearer/Basic/VK) rather than dashboard session tokens.
func isInferenceWSEndpoint(path string) bool {
	for strings.HasPrefix(path, "/openai/") {
		path = strings.TrimPrefix(path, "/openai")
	}

	switch path {
	case "/v1/responses",
		"/responses",
		"/v1/realtime",
		"/realtime":
		return true
	default:
		return false
	}
}

func buildRealtimeTransportPathSet() map[string]struct{} {
	paths := map[string]struct{}{}
	for _, path := range integrations.OpenAIRealtimePaths("") {
		paths[path] = struct{}{}
	}
	for _, path := range integrations.OpenAIRealtimePaths("/openai") {
		paths[path] = struct{}{}
	}
	for _, path := range integrations.OpenAIRealtimeWebRTCCallsPaths("") {
		paths[path] = struct{}{}
	}
	for _, path := range integrations.OpenAIRealtimeWebRTCCallsPaths("/openai") {
		paths[path] = struct{}{}
	}
	return paths
}

func isRealtimeTransportEndpoint(path string) bool {
	_, ok := realtimeTransportPaths[path]
	return ok
}

// AuthMiddleware is a middleware that handles authentication for the API.
type AuthMiddleware struct {
	store         configstore.ConfigStore
	authConfig    atomic.Pointer[configstore.AuthConfig]
	wsTicketStore *WSTicketStore
}

// InitAuthMiddleware initializes the auth middleware.
func InitAuthMiddleware(store configstore.ConfigStore, wsTicketStore *WSTicketStore) (*AuthMiddleware, error) {
	if store == nil {
		return nil, fmt.Errorf("store is not present")
	}
	authConfig, err := store.GetAuthConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get auth config from store: %v", err)
	}
	am := &AuthMiddleware{
		store:         store,
		authConfig:    atomic.Pointer[configstore.AuthConfig]{},
		wsTicketStore: wsTicketStore,
	}
	am.authConfig.Store(authConfig)
	return am, nil
}

func (m *AuthMiddleware) UpdateAuthConfig(authConfig *configstore.AuthConfig) {
	m.authConfig.Store(authConfig)
}

// InferenceMiddleware is for inference requests (including MCP routes) if authConfig is set, it will skip authentication if disableAuthOnInference is true.
func (m *AuthMiddleware) InferenceMiddleware() schemas.BifrostHTTPMiddleware {
	return m.middleware(func(authConfig *configstore.AuthConfig, url string) bool {
		return authConfig.DisableAuthOnInference
	})
}

// APIMiddleware is for API requests if authConfig is set, it will verify authentication based on the request type.
// Three authentication methods are supported:
//   - Basic auth: Uses username + password validation (no session tracking). Used for inference API calls.
//   - Bearer token: Uses session validation via validateSession(). Used for dashboard calls.
//   - WebSocket: Uses session validation via validateSession() with token from query parameters.
//
// Basic auth may be acceptable for limited use cases, while Bearer and WebSocket flows provide
// session-based authentication suitable for production environments.
func (m *AuthMiddleware) APIMiddleware() schemas.BifrostHTTPMiddleware {
	whitelistedRoutes := []string{
		"/api/session/is-auth-enabled",
		"/api/session/login",
		"/api/oauth/callback",
		"/health",
	}
	whitelistedPrefixes := []string{
		"/api/oauth/callback",
	}
	return m.middleware(func(authConfig *configstore.AuthConfig, url string) bool {
		if slices.Contains(whitelistedRoutes, url) ||
			slices.IndexFunc(whitelistedPrefixes, func(prefix string) bool {
				return strings.HasPrefix(url, prefix)
			}) != -1 {
			return true
		}
		return false
	})
}

// middleware is the core authentication middleware that checks if the request should be authenticated or not.
func (m *AuthMiddleware) middleware(shouldSkip func(*configstore.AuthConfig, string) bool) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			authConfig := m.authConfig.Load()
			if authConfig == nil || !authConfig.IsEnabled {
				logger.Debug("auth middleware is disabled because auth config is not present or not enabled")
				ctx.SetUserValue(schemas.BifrostContextKeySessionToken, "")
				next(ctx)
				return
			}
			url := string(ctx.Request.URI().RequestURI())
			// We skip authorization for the login route
			if shouldSkip(authConfig, url) {
				next(ctx)
				return
			}
			if isRealtimeTransportEndpoint(string(ctx.Path())) {
				next(ctx)
				return
			}
			// If inference is disabled, we skip authorization
			// Get the authorization header
			authorization := string(ctx.Request.Header.Peek("Authorization"))
			if authorization == "" {
				if string(ctx.Request.Header.Peek("Upgrade")) == "websocket" {
					path := string(ctx.Path())
					if isInferenceWSEndpoint(path) {
						// Inference WS endpoints (/v1/responses, /v1/realtime) use the same
						// auth as HTTP inference: Bearer/Basic headers or governance VK validation.
						// If no Authorization header, fall through to return 401 below
						// (or the shouldSkip check above already passed them through).
					} else {
						// Prefer short-lived ticket-based auth (from POST /api/session/ws-ticket)
						ticket := string(ctx.Request.URI().QueryArgs().Peek("ticket"))
						if ticket != "" && m.wsTicketStore != nil {
							sessionToken := m.wsTicketStore.Consume(ticket)
							if sessionToken != "" && validateSession(ctx, m.store, sessionToken) {
								ctx.SetUserValue(schemas.BifrostContextKeySessionToken, sessionToken)
								next(ctx)
								return
							}
							SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
							return
						}
						// Fallback: legacy ?token= param (for backward compatibility)
						token := string(ctx.Request.URI().QueryArgs().Peek("token"))
						if token != "" {
							if validateSession(ctx, m.store, token) {
								ctx.SetUserValue(schemas.BifrostContextKeySessionToken, token)
								next(ctx)
								return
							}
							SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
							return
						}
						// Fallback: cookie-based WS auth
						cookieToken := string(ctx.Request.Header.Cookie("token"))
						if cookieToken != "" && validateSession(ctx, m.store, cookieToken) {
							ctx.SetUserValue(schemas.BifrostContextKeySessionToken, cookieToken)
							next(ctx)
							return
						}
						SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
						return
					}
				}
				// Cookie-based auth fallback: if no Authorization header, check for the HTTPOnly session cookie.
				// This supports the dashboard which relies on cookies instead of localStorage tokens.
				cookieToken := string(ctx.Request.Header.Cookie("token"))
				if cookieToken != "" && validateSession(ctx, m.store, cookieToken) {
					ctx.SetUserValue(schemas.BifrostContextKeySessionToken, cookieToken)
					next(ctx)
					return
				}
				SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
				return
			}
			// Split the authorization header into the scheme and the token
			scheme, token, ok := strings.Cut(authorization, " ")
			if !ok {
				SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
				return
			}
			// Checking basic auth for inference calls
			if scheme == "Basic" {
				// Decode the base64 token
				decodedBytes, err := base64.StdEncoding.DecodeString(token)
				if err != nil {
					SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
					return
				}
				// Split the decoded token into the username and password
				username, password, ok := strings.Cut(string(decodedBytes), ":")
				if !ok {
					SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
					return
				}
				// Verify the username and password
				if authConfig.AdminUserName == nil || username != authConfig.AdminUserName.GetValue() {
					SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
					return
				}
				if authConfig.AdminPassword == nil {
					SendError(ctx, fasthttp.StatusInternalServerError, "Authentication not properly configured")
					return
				}
				compare, err := encrypt.CompareHash(authConfig.AdminPassword.GetValue(), password)
				if err != nil {
					SendError(ctx, fasthttp.StatusInternalServerError, "Internal Server Error")
					return
				}
				if !compare {
					SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
					return
				}
				// Continue with the next handler
				next(ctx)
				return
			}
			// Checking bearer auth for dashboard calls
			if scheme == "Bearer" {
				// Verify the session
				if !validateSession(ctx, m.store, token) {
					// Here we will check if its the base64 of username:password
					// This is for backward compatibility with the old auth system
					decodedBytes, err := base64.StdEncoding.DecodeString(token)
					if err != nil {
						SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
						return
					}
					username, password, ok := strings.Cut(string(decodedBytes), ":")
					if !ok {
						SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
						return
					}
					// Verify the username and password
					if authConfig.AdminUserName == nil || username != authConfig.AdminUserName.GetValue() {
						SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
						return
					}
					if authConfig.AdminPassword == nil {
						SendError(ctx, fasthttp.StatusInternalServerError, "Authentication not properly configured")
						return
					}
					compare, err := encrypt.CompareHash(authConfig.AdminPassword.GetValue(), password)
					if err != nil {
						SendError(ctx, fasthttp.StatusInternalServerError, "Internal Server Error")
						return
					}
					if !compare {
						SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
						return
					}
					// Continue with the next handler
					next(ctx)
					return
				}
				// setting up session in the request
				ctx.SetUserValue(schemas.BifrostContextKeySessionToken, token)
				// Continue with the next handler
				next(ctx)
				return
			}
			SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
		}
	}
}

// TracingMiddleware creates distributed traces for requests and forwards completed traces
// to observability plugins after the response has been written.
//
// The middleware:
// 1. Extracts parent trace ID from incoming W3C traceparent header (if present)
// 2. Creates a new trace in the store (only the lightweight trace ID is stored in context)
// 3. Calls the next handler to process the request
// 4. After response is written, asynchronously completes the trace and forwards it to observability plugins
//
// This middleware should be placed early in the middleware chain to capture the full request lifecycle.
type TracingMiddleware struct {
	tracer atomic.Pointer[tracing.Tracer]
}

// NewTracingMiddleware creates a new tracing middleware
func NewTracingMiddleware(tracer *tracing.Tracer) *TracingMiddleware {
	tm := &TracingMiddleware{
		tracer: atomic.Pointer[tracing.Tracer]{},
	}
	tm.tracer.Store(tracer)
	return tm
}

// SetObservabilityPlugins sets the observability plugins for the tracing middleware
func (m *TracingMiddleware) SetObservabilityPlugins(obsPlugins []schemas.ObservabilityPlugin) {
	if tracer := m.tracer.Load(); tracer != nil {
		tracer.SetObservabilityPlugins(obsPlugins)
	}
}

// SetTracer sets the tracer for the tracing middleware
func (m *TracingMiddleware) SetTracer(tracer *tracing.Tracer) {
	m.tracer.Store(tracer)
}

// Middleware returns the middleware function that creates distributed traces for requests and forwards completed traces
func (m *TracingMiddleware) Middleware() schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// Skip if store is nil
			if m.tracer.Load() == nil {
				next(ctx)
				return
			}
			requestID := string(ctx.Request.Header.Peek("x-request-id"))
			if requestID == "" {
				requestID = uuid.New().String()
				// Injecting this back to be picked up by the next middleware
				ctx.Request.Header.Set("x-request-id", requestID)
			}
			// Extract trace ID from W3C traceparent header (if present)
			// This is the 32-char trace ID that links all spans in a distributed trace
			inheritedTraceID := tracing.ExtractParentID(&ctx.Request.Header)
			// Create trace in store - only ID returned (trace data stays in store)
			traceID := m.tracer.Load().CreateTrace(inheritedTraceID, requestID)
			// Only trace ID goes into context (lightweight, no bloat)
			ctx.SetUserValue(schemas.BifrostContextKeyTraceID, traceID)
			// Extract parent span ID from W3C traceparent header (if present)
			// This is the 16-char span ID from the upstream service that should be
			// set as the ParentID of our root span for proper trace linking in Datadog/etc.
			parentSpanID := tracing.ExtractTraceParentSpanID(&ctx.Request.Header)
			if parentSpanID != "" {
				ctx.SetUserValue(schemas.BifrostContextKeyParentSpanID, parentSpanID)
			}

			// Store a trace completion callback for streaming handlers to use
			ctx.SetUserValue(schemas.BifrostContextKeyTraceCompleter, func() {
				// Run deferred HTTPTransportPostHook for streaming responses
				if postHookCompleter, ok := ctx.UserValue(schemas.BifrostContextKeyTransportPostHookCompleter).(func()); ok {
					postHookCompleter()
				}
				// Attach transport plugin logs before completing the trace (streaming path)
				if transportLogs, ok := ctx.UserValue(schemas.BifrostContextKeyTransportPluginLogs).([]schemas.PluginLogEntry); ok && len(transportLogs) > 0 {
					m.tracer.Load().AttachPluginLogs(traceID, transportLogs)
				}
				m.tracer.Load().CompleteAndFlushTrace(traceID)
			})
			// Create root span for the HTTP request
			spanCtx, rootSpan := m.tracer.Load().StartSpan(ctx, string(ctx.RequestURI()), schemas.SpanKindHTTPRequest)
			if rootSpan != nil {
				m.tracer.Load().SetAttribute(rootSpan, "http.method", string(ctx.Method()))
				m.tracer.Load().SetAttribute(rootSpan, "http.url", string(ctx.RequestURI()))
				m.tracer.Load().SetAttribute(rootSpan, "http.user_agent", string(ctx.Request.Header.UserAgent()))
				// Set root span ID in context for child span creation
				if spanID, ok := spanCtx.Value(schemas.BifrostContextKeySpanID).(string); ok {
					ctx.SetUserValue(schemas.BifrostContextKeySpanID, spanID)
				}
			}
			defer func() {
				// Record response status on the root span
				if rootSpan != nil {
					m.tracer.Load().SetAttribute(rootSpan, "http.status_code", ctx.Response.StatusCode())
					if ctx.Response.StatusCode() >= 400 {
						m.tracer.Load().EndSpan(rootSpan, schemas.SpanStatusError, fmt.Sprintf("HTTP %d", ctx.Response.StatusCode()))
					} else {
						m.tracer.Load().EndSpan(rootSpan, schemas.SpanStatusOk, "")
					}
				}
				// Check if trace completion is deferred (for streaming requests)
				// If deferred, the streaming handler will complete the trace after stream ends
				if deferred, ok := ctx.UserValue(schemas.BifrostContextKeyDeferTraceCompletion).(bool); ok && deferred {
					return
				}
				// Attach transport plugin logs to trace before completion
				if transportLogs, ok := ctx.UserValue(schemas.BifrostContextKeyTransportPluginLogs).([]schemas.PluginLogEntry); ok && len(transportLogs) > 0 {
					m.tracer.Load().AttachPluginLogs(traceID, transportLogs)
				}
				// After response written - async flush
				m.tracer.Load().CompleteAndFlushTrace(traceID)
			}()

			next(ctx)
		}
	}
}

// GetTracer returns the tracer instance for use by streaming handlers
func (m *TracingMiddleware) GetTracer() *tracing.Tracer {
	return m.tracer.Load()
}

// GetObservabilityPlugins filters and returns only observability plugins from a list of plugins.
// Uses Go type assertion to identify plugins implementing the ObservabilityPlugin interface.
func GetObservabilityPlugins(plugins []schemas.BasePlugin) []schemas.ObservabilityPlugin {
	if len(plugins) == 0 {
		return nil
	}

	obsPlugins := make([]schemas.ObservabilityPlugin, 0)
	for _, plugin := range plugins {
		if obsPlugin, ok := plugin.(schemas.ObservabilityPlugin); ok {
			obsPlugins = append(obsPlugins, obsPlugin)
		}
	}

	return obsPlugins
}
