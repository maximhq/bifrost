package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/temptoken"
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

// clientForwardedIP returns the client-supplied originating IP from reverse-proxy
// headers, or "" if none are present. X-Forwarded-For may be a comma-separated list
// (client, proxy1, proxy2); the leftmost entry is the original client.
//
// These headers are caller-controlled and unauthenticated unless Bifrost sits behind
// a trusted proxy that overwrites them. The value is logged as http.forwarded_for —
// separate from the authoritative http.remote_addr (the real TCP peer) — so a forged
// header cannot mask the true peer in the access log.
func clientForwardedIP(ctx *fasthttp.RequestCtx) string {
	if xff := strings.TrimSpace(string(ctx.Request.Header.Peek("X-Forwarded-For"))); xff != "" {
		if first, _, found := strings.Cut(xff, ","); found {
			return strings.TrimSpace(first)
		}
		return xff
	}
	if xrip := strings.TrimSpace(string(ctx.Request.Header.Peek("X-Real-IP"))); xrip != "" {
		return xrip
	}
	return ""
}

// corsMiddlewareConfig is an immutable snapshot of the CORS-relevant client config.
// The slices are cloned at construction so a hot reload mutating the source
// ClientConfig in place cannot race with in-flight requests reading these fields.
type corsMiddlewareConfig struct {
	dumpErrorsInConsoleLogs bool
	allowedOrigins          []string
	allowedHeaders          []string
}

// newCorsMiddlewareConfig builds an immutable snapshot from the live config,
// cloning the slices so the snapshot never aliases the shared ClientConfig.
func newCorsMiddlewareConfig(config *lib.Config) *corsMiddlewareConfig {
	if config == nil || config.ClientConfig == nil {
		return nil
	}
	return &corsMiddlewareConfig{
		dumpErrorsInConsoleLogs: config.ClientConfig.DumpErrorsInConsoleLogs,
		allowedOrigins:          slices.Clone(config.ClientConfig.AllowedOrigins),
		allowedHeaders:          slices.Clone(config.ClientConfig.AllowedHeaders),
	}
}

// CorsMiddleware handles CORS headers for localhost and configured allowed origins.
// The snapshot is held in an atomic.Pointer so UpdateConfig can swap it at runtime
// without racing in-flight requests, which read the pointer concurrently. Because the
// snapshot is immutable (slices cloned), readers never observe a torn or half-updated
// config even while a reload swaps in a new one.
type CorsMiddleware struct {
	config atomic.Pointer[corsMiddlewareConfig]
}

func NewCorsMiddleware(config *lib.Config) *CorsMiddleware {
	c := &CorsMiddleware{}
	c.config.Store(newCorsMiddlewareConfig(config))
	return c
}

// UpdateConfig atomically swaps in a fresh immutable snapshot of the configuration.
// In-flight requests reading the pointer observe either the old or the new snapshot,
// never a torn value. ReloadClientConfigFromConfigStore must call this whenever the
// client config is refreshed, mirroring how AuthMiddleware is updated.
func (c *CorsMiddleware) UpdateConfig(config *lib.Config) {
	c.config.Store(newCorsMiddlewareConfig(config))
}

// CorsMiddleware handles CORS headers for localhost and configured allowed origins
func (c *CorsMiddleware) Middleware() schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// Snapshot the config once per request so a concurrent UpdateConfig swap
			// cannot apply two different configs within a single response.
			cfg := c.config.Load()
			if cfg == nil {
				SendError(ctx, fasthttp.StatusInternalServerError, "CORS middleware configuration not loaded")
				return
			}
			shouldLog := slices.IndexFunc(loggingSkipPaths, func(path string) bool {
				return strings.HasPrefix(string(ctx.RequestURI()), path)
			}) == -1
			if shouldLog {
				startTime := time.Now()
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
					if forwarded := clientForwardedIP(ctx); forwarded != "" {
						logBuilder = logBuilder.Str("http.forwarded_for", forwarded)
					}
					if traceID, ok := ctx.UserValue(schemas.BifrostContextKeyTraceID).(string); ok && traceID != "" {
						logBuilder = logBuilder.Str("trace_id", traceID)
					}
					// Emit the request ID alongside trace_id
					if requestID := string(ctx.Request.Header.Peek("x-request-id")); requestID != "" {
						logBuilder = logBuilder.Str("request_id", requestID)
					}
					if cfg.dumpErrorsInConsoleLogs {
						if statusCode >= 400 && !ctx.Response.IsBodyStream() {
							if body := ctx.Response.Body(); len(body) > 0 {
								logBuilder = logBuilder.Str("http.error", string(body))
							}
						}
					}
					logBuilder.Send()
				}()
			}
			origin := string(ctx.Request.Header.Peek("Origin"))
			allowed := IsOriginAllowed(origin, cfg.allowedOrigins)
			// Credentialed responses are sent when the origin is not matched solely by a
			// wildcard AllowedOrigins — i.e. the origin is localhost or explicitly listed.
			credentialed := !slices.Contains(cfg.allowedOrigins, "*") ||
				isLocalhostOrigin(origin) ||
				slices.Contains(cfg.allowedOrigins, origin)

			allowedHeaders := []string{"Content-Type", "Authorization", "X-Requested-With", "X-Stainless-Timeout", "X-Api-Key", "X-OpenAI-Agents-SDK", "X-Operation-ID"}
			if slices.Contains(cfg.allowedHeaders, "*") {
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
			} else if len(cfg.allowedHeaders) > 0 {
				// append allowed headers from config to the default headers
				for _, header := range cfg.allowedHeaders {
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
			// The streaming handler calls this BEFORE reader.Done() so that errors can
			// still be sent as SSE events. applyResponse=false because the response is
			// already on the wire and mutating ctx.Response would corrupt the chunked stream.
			//
			// IMPORTANT: The callback must NOT access ctx — fasthttp recycles RequestCtx
			// after the response body stream completes. All needed data is eagerly captured
			// here (while ctx is still valid) and passed through the closure.
			if deferred, ok := ctx.UserValue(schemas.BifrostContextKeyDeferTraceCompletion).(bool); ok && deferred {
				// Verify the completer slot exists before allocating pooled snapshots.
				// The streaming handler pre-allocates this *atomic.Value; if absent,
				// skip work to avoid leaking pooled HTTPRequest/HTTPResponse objects.
				slot, ok := ctx.UserValue(schemas.BifrostContextKeyTransportPostHookCompleter).(*atomic.Value)
				if !ok {
					return
				}

				// Eagerly snapshot request/response from ctx before it can be recycled.
				capturedReq := lib.BuildHTTPRequestFromFastHTTP(ctx)
				capturedResp := lib.BuildHTTPResponseFromFastHTTP(ctx)
				// Snapshot pre-hook transport plugin logs already accumulated on ctx.
				var preHookLogs []schemas.PluginLogEntry
				if logs, ok := ctx.UserValue(schemas.BifrostContextKeyTransportPluginLogs).([]schemas.PluginLogEntry); ok {
					preHookLogs = logs
				}

				completer := func() ([]schemas.PluginLogEntry, error) {
					defer schemas.ReleaseHTTPRequest(capturedReq)
					defer schemas.ReleaseHTTPResponse(capturedResp)
					postHookLogs, err := runTransportPostHooksCaptured(capturedReq, capturedResp, plugins, bifrostCtx)
					allLogs := preHookLogs
					if len(postHookLogs) > 0 {
						allLogs = append(allLogs, postHookLogs...)
					}
					return allLogs, err
				}

				// Store the completer in the atomic.Value slot that the streaming handler
				// placed on ctx. The goroutine reads from its closure-captured copy of
				// the slot, avoiding any ctx access after the handler returns.
				slot.Store(completer)
				return
			}

			_ = runTransportPostHooks(ctx, plugins, bifrostCtx, true)
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
func runTransportPostHooks(ctx *fasthttp.RequestCtx, plugins []schemas.HTTPTransportPlugin, bifrostCtx *schemas.BifrostContext, applyResponse bool) error {
	shouldApplyShortCircuit := applyResponse
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
			if shouldApplyShortCircuit {
				applyHTTPResponseToCtx(ctx, httpResp)
			}
			return fmt.Errorf("transport post-hook plugin %s: %w", pluginName, err)
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
	if shouldApplyShortCircuit {
		applyHTTPResponseToCtx(ctx, httpResp)
	}
	return nil
}

// runTransportPostHooksCaptured is the goroutine-safe variant of runTransportPostHooks.
// It uses pre-captured HTTPRequest and HTTPResponse snapshots instead of reading from
// a fasthttp RequestCtx, which may have been recycled by the time this runs in a
// streaming goroutine. Returns accumulated plugin logs (instead of writing them to
// ctx.UserValue) so the caller can forward them to the trace completer.
func runTransportPostHooksCaptured(capturedReq *schemas.HTTPRequest, capturedResp *schemas.HTTPResponse, plugins []schemas.HTTPTransportPlugin, bifrostCtx *schemas.BifrostContext) ([]schemas.PluginLogEntry, error) {
	// Clone into fresh pooled objects so plugins can mutate without affecting the snapshots.
	req := schemas.AcquireHTTPRequest()
	defer schemas.ReleaseHTTPRequest(req)
	req.Method = capturedReq.Method
	req.Path = capturedReq.Path
	for k, v := range capturedReq.Headers {
		req.Headers[k] = v
	}
	for k, v := range capturedReq.Query {
		req.Query[k] = v
	}
	for k, v := range capturedReq.PathParams {
		req.PathParams[k] = v
	}

	httpResp := schemas.AcquireHTTPResponse()
	defer schemas.ReleaseHTTPResponse(httpResp)
	httpResp.StatusCode = capturedResp.StatusCode
	for k, v := range capturedResp.Headers {
		httpResp.Headers[k] = v
	}

	var allLogs []schemas.PluginLogEntry

	// Run http post-hooks in reverse order
	for i := len(plugins) - 1; i >= 0; i-- {
		plugin := plugins[i]
		pluginName := plugin.GetName()
		pluginCtx := bifrostCtx.WithPluginScope(&pluginName)
		err := plugin.HTTPTransportPostHook(pluginCtx, req, httpResp)
		pluginCtx.ReleasePluginScope()
		if err != nil {
			logger.Warn("error in HTTPTransportPostHook for plugin %s: %s", pluginName, err.Error())
			if postHookLogs := bifrostCtx.DrainPluginLogs(); len(postHookLogs) > 0 {
				allLogs = append(allLogs, postHookLogs...)
			}
			return allLogs, fmt.Errorf("transport post-hook plugin %s: %w", pluginName, err)
		}
	}
	// Drain post-hook plugin logs
	if postHookLogs := bifrostCtx.DrainPluginLogs(); len(postHookLogs) > 0 {
		allLogs = append(allLogs, postHookLogs...)
	}
	return allLogs, nil
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
	// Skip response body copy for streaming (SSE) responses — the body is an active
	// io.Reader consumed by fasthttp's writeBodyChunked. Calling Body() would race
	// with the chunked writer (Body() drains and closes the bodyStream).
	if deferred, ok := ctx.UserValue(schemas.BifrostContextKeyDeferTraceCompletion).(bool); ok && deferred {
		return
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
	store             configstore.ConfigStore
	whitelistedRoutes atomic.Pointer[[]string]
	authConfig        atomic.Pointer[configstore.AuthConfig]
	wsTicketStore     *WSTicketStore
	tempTokensService *temptoken.Service // optional; when nil, temp-token fallback is disabled
	tempTokensEnabled atomic.Bool
}

// InitAuthMiddleware initializes the auth middleware. The tempTokens service
// is optional and still gated by client config — when nil or disabled, the
// temp-token fallback path is skipped.
func InitAuthMiddleware(store configstore.ConfigStore, wsTicketStore *WSTicketStore, tempTokensService *temptoken.Service) (*AuthMiddleware, error) {
	if store == nil {
		return nil, fmt.Errorf("store is not present")
	}
	authConfig, err := store.GetAuthConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get auth config from store: %v", err)
	}
	am := &AuthMiddleware{
		store:             store,
		authConfig:        atomic.Pointer[configstore.AuthConfig]{},
		wsTicketStore:     wsTicketStore,
		tempTokensService: tempTokensService,
	}

	am.authConfig.Store(authConfig)

	// Load whitelisted routes from client config
	clientConfig, err := store.GetClientConfig(context.Background())
	if err == nil && clientConfig != nil {
		am.whitelistedRoutes.Store(&clientConfig.WhitelistedRoutes)
		am.tempTokensEnabled.Store(clientConfig.MCPEnableTempTokenAuth)
	} else {
		emptyRoutes := []string{}
		am.whitelistedRoutes.Store(&emptyRoutes)
		am.tempTokensEnabled.Store(false)
	}

	return am, nil
}

func (m *AuthMiddleware) UpdateAuthConfig(authConfig *configstore.AuthConfig) {
	m.authConfig.Store(authConfig)
}

// UpdateWhitelistedRoutes updates the configured whitelisted routes that bypass auth middleware.
func (m *AuthMiddleware) UpdateWhitelistedRoutes(routes []string) {
	m.whitelistedRoutes.Store(&routes)
}

// UpdateTempTokenAuthEnabled updates whether scoped temp-token fallback auth is accepted.
func (m *AuthMiddleware) UpdateTempTokenAuthEnabled(enabled bool) {
	m.tempTokensEnabled.Store(enabled)
}

// tryTempTokenOrUnauthorized is the last-resort auth path: a request that
// failed every conventional credential check (no Authorization header, no
// valid cookie) is given one more chance to present an X-Bifrost-Temp-Token
// header that authorizes the specific (method, path) being requested. On
// success the validated scope and resource_id are attached to ctx for
// handler-side defense-in-depth checks, and the next handler runs. On
// failure (no header, expired, route-mismatch, etc.) a 401 is written.
//
// Temp-token validation is intentionally *not* attempted when an
// Authorization header or session cookie is present — those paths have
// their own success/failure semantics and silently rescuing a bad password
// with a temp token would be surprising.
func (m *AuthMiddleware) tryTempTokenOrUnauthorized(ctx *fasthttp.RequestCtx, next fasthttp.RequestHandler) {
	if m.tempTokensService != nil && m.tempTokensEnabled.Load() {
		token := string(ctx.Request.Header.Peek("X-Bifrost-Temp-Token"))
		if token != "" {
			validated, err := m.tempTokensService.Validate(ctx, token, string(ctx.Method()), string(ctx.Path()))
			if err == nil && validated != nil {
				ctx.SetUserValue(schemas.BifrostContextKeyTempTokenScope, validated.Scope)
				ctx.SetUserValue(schemas.BifrostContextKeyTempTokenResourceID, validated.ResourceID)
				next(ctx)
				return
			}
		}
	}
	SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
}

// InferenceMiddleware is for inference requests (including MCP routes). It always
// passes the request through — inference authentication is owned entirely by the
// governance plugin, not by this dashboard-auth middleware.
//
// Governance runs downstream on every inference request type (via RunLLMPreHooks) and
// is the authoritative virtual-key validator: it rejects missing/unknown/revoked keys
// and enforces whether a VK is mandatory (driven by ClientConfig.EnforceAuthOnInference).
// In OSS it is loaded unconditionally and cannot be disabled, so delegating to it never
// leaves inference ungated. Keeping admin-password checks out of this path is deliberate:
// gating inference on dashboard credentials would reject virtual-key callers, since a VK
// is not an admin/session credential. Admin-password auth therefore stays exclusive to
// dashboard/API routes (APIMiddleware) and is never required for inference.
func (m *AuthMiddleware) InferenceMiddleware() schemas.BifrostHTTPMiddleware {
	return m.middleware(func(authConfig *configstore.AuthConfig, url string) bool {
		return true
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
	systemWhitelistedRoutes := []string{
		"/api/session/is-auth-enabled",
		"/api/session/login",
		"/api/oauth/callback",
		"/health",
		"/login",
		"/favicon.ico",
		"/assets/*",
		"/api/scim/oauth/config",
		"/api/scim/oauth/callback",
		"/api/scim/oauth/refresh",
		"/api/scim/oauth/logout",
		"/health",
		"/api/version",
	}
	whitelistedPrefixes := []string{
		// "/api/oauth/callback" is also in systemWhitelistedRoutes above as an
		// exact match — that's the only OAuth route that must be public (it's
		// hit by the browser after the upstream provider redirects back, with
		// no cookie context). DO NOT add a broad "/api/oauth" prefix here:
		// it would whitelist /api/oauth/per-user/* (auth-via-temp-token) and
		// /api/oauth/config/* (admin-only) and bypass the temp-token fallback
		// in tryTempTokenOrUnauthorized.
		"/api/dev",
		// Skills serving endpoints are public — marketplace URLs cannot carry
		// credentials securely. Management endpoints under /api/skills (without
		// /serve/) remain authenticated.
		"/api/skills/serve/",
		// OAuth2 discovery endpoints (RFC 8414 AS metadata, RFC 9728 protected
		// resource metadata, RFC 7517 JWKS) must be reachable without auth so
		// clients can bootstrap the flow. Each handler still gates availability
		// behind discoveryEnabled() and serves 404 when OAuth mode is off.
		"/.well-known/",
	}
	return m.middleware(func(authConfig *configstore.AuthConfig, url string) bool {
		if slices.Contains(systemWhitelistedRoutes, url) ||
			slices.IndexFunc(whitelistedPrefixes, func(prefix string) bool {
				return strings.HasPrefix(url, prefix)
			}) != -1 {
			return true
		}
		// Check user-configured whitelisted routes
		if configuredRoutes := m.whitelistedRoutes.Load(); configuredRoutes != nil {
			if slices.Contains(*configuredRoutes, url) || slices.IndexFunc(*configuredRoutes, func(route string) bool {
				if before, ok := strings.CutSuffix(route, "*"); ok {
					return strings.HasPrefix(url, before)
				}
				return false
			}) != -1 {
				return true
			}
		}
		return false
	})
}

// middleware is the core authentication middleware that checks if the request should be authenticated or not.
func (m *AuthMiddleware) middleware(shouldSkip func(*configstore.AuthConfig, string) bool) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// We will first check if its API key auth
			// If yes; we will skip this middleware
			if isAPIKeyAuth, ok := ctx.UserValue(schemas.IsAPIKeyAuthContextKey).(bool); ok && isAPIKeyAuth {
				next(ctx)
				return
			}
			authConfig := m.authConfig.Load()
			if authConfig == nil || !authConfig.IsEnabled {
				// logger.Debug("auth middleware is disabled because auth config is not present or not enabled")
				ctx.SetUserValue(schemas.BifrostContextKeySessionToken, "")
				// Mark as local admin so downstream RBAC bypasses cleanly when
				// auth is fully disabled; otherwise RBAC 401s and the UI enters
				// a logout/login redirect loop.
				ctx.SetUserValue(schemas.IsLocalAdminContextKey, true)
				next(ctx)
				return
			}
			// Match the whitelist against the path only
			url := string(ctx.Path())
			// We skip authorization for the login route
			if shouldSkip(authConfig, url) {
				next(ctx)
				return
			}
			if isRealtimeTransportEndpoint(url) {
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
					ctx.SetUserValue(schemas.IsLocalAdminContextKey, true)
					next(ctx)
					return
				}
				// Last-resort: a scoped temp token (e.g. for the MCP per-user
				// OAuth auth page accessed by a non-admin browser) can rescue
				// this request when it targets a route the token authorizes.
				m.tryTempTokenOrUnauthorized(ctx, next)
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
				// We are checking for API keys first; it it seems like a valid Bifrost API key

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
					// Mark as local admin for RBAC bypass
					ctx.SetUserValue(schemas.IsLocalAdminContextKey, true)
					// Continue with the next handler
					next(ctx)
					return
				}
				// setting up session in the request
				ctx.SetUserValue(schemas.BifrostContextKeySessionToken, token)
				ctx.SetUserValue(schemas.IsLocalAdminContextKey, true)
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

// collectDimensionHeaders gathers x-bf-dim-* request headers into a map keyed by
// the bare dimension name. TracingMiddleware runs before ConvertToBifrostContext,
// so it reads the headers directly rather than BifrostContextKeyDimensions.
func collectDimensionHeaders(ctx *fasthttp.RequestCtx) map[string]string {
	if ctx == nil {
		return nil
	}
	var dims map[string]string
	ctx.Request.Header.All()(func(key, value []byte) bool {
		keyStr := strings.ToLower(string(key))
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-dim-"); ok && labelName != "" {
			if dims == nil {
				dims = make(map[string]string)
			}
			dims[labelName] = string(value)
		}
		return true
	})
	return dims
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
			// Pin the tracer for the lifetime of this request so that a concurrent
			// SetTracer() swap cannot split a trace across two instances.
			tracer := m.tracer.Load()
			if tracer == nil {
				next(ctx)
				return
			}
			requestID := lib.NormalizeRequestID(string(ctx.Request.Header.Peek("x-request-id")))
			// Inject the normalized ID back so every downstream middleware sees
			// the same value, including when the caller sent an all-zero sentinel.
			ctx.Request.Header.Set("x-request-id", requestID)
			// Extract trace ID from W3C traceparent header (if present)
			// This is the 32-char trace ID that links all spans in a distributed trace
			inheritedTraceID := tracing.ExtractParentID(&ctx.Request.Header)
			// Create trace in store - only ID returned (trace data stays in store)
			traceID := tracer.CreateTrace(inheritedTraceID, requestID)
			// Surface correlation IDs back to the caller so a request can be pivoted
			// into its logs (Loki) and trace (Tempo) in Grafana and similar stacks.
			ctx.Response.Header.Set("x-request-id", requestID)
			ctx.Response.Header.Set("x-bifrost-trace-id", traceID)
			// Store dimensions and session ID at the trace level (not as span
			// attributes) so connectors like BigQuery can export them without
			// changing the OTEL/Datadog span payloads.
			dimensions := collectDimensionHeaders(ctx)
			if len(dimensions) > 0 {
				// Hand the trace its own copy — connectors read the attribute
				// asynchronously and must never observe later mutations.
				tracer.SetTraceAttribute(traceID, schemas.TraceAttrDimensions, maps.Clone(dimensions))
			}
			if sessionID := strings.TrimSpace(string(ctx.Request.Header.Peek("x-bf-session-id"))); sessionID != "" {
				tracer.SetTraceAttribute(traceID, schemas.TraceAttrSessionID, sessionID)
			}
			// Only trace ID goes into context (lightweight, no bloat)
			ctx.SetUserValue(schemas.BifrostContextKeyTraceID, traceID)
			// Extract parent span ID from W3C traceparent header (if present)
			// This is the 16-char span ID from the upstream service that should be
			// set as the ParentID of our root span for proper trace linking in Datadog/etc.
			parentSpanID := tracing.ExtractTraceParentSpanID(&ctx.Request.Header)
			if parentSpanID != "" {
				ctx.SetUserValue(schemas.BifrostContextKeyParentSpanID, parentSpanID)
			}
			// Store a trace completion callback for streaming handlers to use.
			// Accepts transport plugin logs as a parameter so it never reads from
			// ctx.UserValue — ctx may be recycled by the time this runs in a goroutine.
			ctx.SetUserValue(schemas.BifrostContextKeyTraceCompleter, func(transportLogs []schemas.PluginLogEntry) {
				if len(transportLogs) > 0 {
					tracer.AttachPluginLogs(traceID, transportLogs)
				}
				// End the root HTTP span now that the stream has fully drained, so its
				// latency covers the entire streamed response. For deferred (streaming)
				// requests the TracingMiddleware defer below intentionally leaves the root
				// span open; ending it here keeps the parent from closing before its child
				// llm.call span (which is ended by completeDeferredSpan on the final chunk).
				// Status is always Ok: deferral is only set after the stream was set up with
				// HTTP 200, and mid-stream failures surface as SSE error frames / on the
				// llm.call span, not as an HTTP error on the root.
				if rootHandle := tracer.GetSpanHandleByID(traceID, nil); rootHandle != nil {
					tracer.EndSpan(rootHandle, schemas.SpanStatusOk, "")
				}
				tracer.CompleteAndFlushTrace(traceID)
				// Guaranteed end-of-stream backstop: force-reap the stream accumulator
				// now that the stream has fully drained and the trace is flushed. This
				// covers streams that ended without a clean terminal chunk (client abort,
				// broken SSE write, or a multi-plugin refcount imbalance), which would
				// otherwise leak their accumulated (deep-copied) chunks until the TTL
				// sweep. Safe after CompleteAndFlushTrace: span completion reads the
				// separately stored accumulated response, not the live accumulator.
				tracer.ForceCleanupStreamAccumulator(traceID)
			})
			// Create root span for the HTTP request
			spanCtx, rootSpan := tracer.StartSpan(ctx, string(ctx.RequestURI()), schemas.SpanKindHTTPRequest)
			if rootSpan != nil {
				for name, value := range dimensions {
					// "path" and "method" stay reserved for the standard http.* attributes.
					if name != "path" && name != "method" {
						tracer.SetAttribute(rootSpan, name, value)
					}
				}
				tracer.SetAttribute(rootSpan, "http.method", string(ctx.Method()))
				tracer.SetAttribute(rootSpan, "http.url", string(ctx.RequestURI()))
				tracer.SetAttribute(rootSpan, "http.user_agent", string(ctx.Request.Header.UserAgent()))
				// Set root span ID in context for child span creation
				if spanID, ok := spanCtx.Value(schemas.BifrostContextKeySpanID).(string); ok {
					ctx.SetUserValue(schemas.BifrostContextKeySpanID, spanID)
				}
			}
			// Capture request headers onto the trace when a connector has opted in.
			// Gated so there is no overhead when no observability plugin wants headers.
			if tracer.ShouldCaptureRequestHeaders() {
				headers := make(map[string]string)
				ctx.Request.Header.All()(func(key, value []byte) bool {
					headers[strings.ToLower(string(key))] = string(value)
					return true
				})
				tracer.SetTraceRequestHeaders(traceID, headers)
			}
			defer func() {
				deferred, _ := ctx.UserValue(schemas.BifrostContextKeyDeferTraceCompletion).(bool)
				// Record response status on the root span
				if rootSpan != nil {
					tracer.SetAttribute(rootSpan, "http.status_code", ctx.Response.StatusCode())
					// For deferred (streaming) requests, the trace completer ends the root
					// span after the stream fully drains, so its latency reflects the whole
					// streamed response. Ending it here (at handler return) would close the
					// parent before the deferred llm.call span finishes, making the child
					// span appear longer than its parent in trace viewers.
					if !deferred {
						if ctx.Response.StatusCode() >= 400 {
							tracer.EndSpan(rootSpan, schemas.SpanStatusError, fmt.Sprintf("HTTP %d", ctx.Response.StatusCode()))
						} else {
							tracer.EndSpan(rootSpan, schemas.SpanStatusOk, "")
						}
					}
				}
				// Check if trace completion is deferred (for streaming requests)
				// If deferred, the streaming handler will complete the trace (and end the
				// root span via the trace completer) after the stream ends.
				if deferred {
					return
				}
				// Attach transport plugin logs to trace before completion
				if transportLogs, ok := ctx.UserValue(schemas.BifrostContextKeyTransportPluginLogs).([]schemas.PluginLogEntry); ok && len(transportLogs) > 0 {
					tracer.AttachPluginLogs(traceID, transportLogs)
				}
				// After response written - async flush
				tracer.CompleteAndFlushTrace(traceID)
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
