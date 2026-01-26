package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// CorsMiddleware handles CORS headers for localhost and configured allowed origins
func CorsMiddleware(config *lib.Config) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			origin := string(ctx.Request.Header.Peek("Origin"))
			allowed := IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins)
			allowedHeaders := []string{"Content-Type", "Authorization", "X-Requested-With", "X-Stainless-Timeout"}
			if len(config.ClientConfig.AllowedHeaders) > 0 {
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
				ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
				ctx.Response.Header.Set("Access-Control-Max-Age", "86400")
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

// TransportInterceptorMiddleware runs all plugin HTTP transport interceptors.
// It converts the fasthttp request to a serializable HTTPRequest, runs all plugin interceptors,
// and applies any modifications back to the fasthttp context.
func TransportInterceptorMiddleware(config *lib.Config) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			plugins := config.GetLoadedPlugins()
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
				resp, err := plugin.HTTPTransportPreHook(bifrostCtx, req)
				if err != nil {
					// Short-circuit with error
					ctx.SetStatusCode(fasthttp.StatusInternalServerError)
					ctx.SetBodyString(err.Error())
					return
				}
				if resp != nil {
					// Short-circuit with response
					applyHTTPResponseToCtx(ctx, resp)
					return
				}
				// If we got here, the plugin may have modified req in-place
			}
			// Apply modifications back to fasthttp context
			applyHTTPRequestToCtx(ctx, req)
			// Adding user values
			for key, value := range bifrostCtx.GetUserValues() {
				ctx.SetUserValue(key, value)
			}
			next(ctx)

			// Skip HTTPTransportPostHook for streaming responses
			// Streaming handlers set DeferTraceCompletion and use StreamChunkInterceptor for per-chunk hooks
			if deferred, ok := ctx.UserValue(schemas.BifrostContextKeyDeferTraceCompletion).(bool); ok && deferred {
				return
			}

			// Acquire pooled response for post-hooks (non-streaming only)
			httpResp := schemas.AcquireHTTPResponse()
			defer schemas.ReleaseHTTPResponse(httpResp)
			fasthttpResponseToHTTPResponse(ctx, httpResp)
			// Run http post-hooks in reverse order
			for i := len(plugins) - 1; i >= 0; i-- {
				plugin := plugins[i]
				err := plugin.HTTPTransportPostHook(bifrostCtx, req, httpResp)
				if err != nil {
					logger.Warn("error in HTTPTransportPostHook for plugin %s: %s", plugin.GetName(), err.Error())
					// Short-circuit with response
					applyHTTPResponseToCtx(ctx, httpResp)
					return
				}
			}
			// Apply modifications back to fasthttp context
			applyHTTPResponseToCtx(ctx, httpResp)
		}
	}
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

	// Copy body
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

type AuthMiddleware struct {
	store      configstore.ConfigStore
	authConfig atomic.Pointer[configstore.AuthConfig]
}

func InitAuthMiddleware(store configstore.ConfigStore) (*AuthMiddleware, error) {
	if store == nil {
		return nil, fmt.Errorf("store is not present")
	}
	authConfig, err := store.GetAuthConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get auth config from store: %v", err)
	}
	am := &AuthMiddleware{
		store:      store,
		authConfig: atomic.Pointer[configstore.AuthConfig]{},
	}
	am.authConfig.Store(authConfig)
	return am, nil
}

func (m *AuthMiddleware) UpdateAuthConfig(authConfig *configstore.AuthConfig) {
	m.authConfig.Store(authConfig)
}

// InferenceMiddleware is for inference requests if authConfig is set, it will skip authentication if disableAuthOnInference is true.
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
		"/api/session/logout",
		"/health",
	}
	return m.middleware(func(authConfig *configstore.AuthConfig, url string) bool {
		return slices.Contains(whitelistedRoutes, url)
	})
}

func (m *AuthMiddleware) middleware(shouldSkip func(*configstore.AuthConfig, string) bool) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			authConfig := m.authConfig.Load()
			if authConfig == nil || !authConfig.IsEnabled {
				logger.Debug("auth middleware is disabled because auth config is not present or not enabled")
				next(ctx)
				return
			}
			url := string(ctx.Request.URI().RequestURI())
			// We skip authorization for the login route
			if shouldSkip(authConfig, url) {
				next(ctx)
				return
			}
			// If inference is disabled, we skip authorization
			// Get the authorization header
			authorization := string(ctx.Request.Header.Peek("Authorization"))
			if authorization == "" {
				// Check if its a websocket 101 upgrade request
				if string(ctx.Request.Header.Peek("Upgrade")) == "websocket" {
					// Here we get the token from query params
					token := string(ctx.Request.URI().QueryArgs().Peek("token"))
					if token == "" {
						SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
						return
					}
					// Verify the session
					if !validateSession(ctx, m.store, token) {
						SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
						return
					}
					// Continue with the next handler
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
				if username != authConfig.AdminUserName {
					SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
					return
				}
				compare, err := encrypt.CompareHash(authConfig.AdminPassword, password)
				if err != nil {
					SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to compare password: %v", err))
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
					SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
					return
				}
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
	tracer     atomic.Pointer[tracing.Tracer]
	obsPlugins atomic.Pointer[[]schemas.ObservabilityPlugin]
}

// NewTracingMiddleware creates a new tracing middleware
func NewTracingMiddleware(tracer *tracing.Tracer, obsPlugins []schemas.ObservabilityPlugin) *TracingMiddleware {
	tm := &TracingMiddleware{
		tracer:     atomic.Pointer[tracing.Tracer]{},
		obsPlugins: atomic.Pointer[[]schemas.ObservabilityPlugin]{},
	}
	tm.tracer.Store(tracer)
	tm.obsPlugins.Store(&obsPlugins)
	return tm
}

// SetObservabilityPlugins sets the observability plugins for the tracing middleware
func (m *TracingMiddleware) SetObservabilityPlugins(obsPlugins []schemas.ObservabilityPlugin) {
	m.obsPlugins.Store(&obsPlugins)
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
			// Extract trace ID from W3C traceparent header (if present)
			// This is the 32-char trace ID that links all spans in a distributed trace
			inheritedTraceID := tracing.ExtractParentID(&ctx.Request.Header)
			// Create trace in store - only ID returned (trace data stays in store)
			traceID := m.tracer.Load().CreateTrace(inheritedTraceID)
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
				m.completeAndFlushTrace(traceID)
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
				// After response written - async flush
				m.completeAndFlushTrace(traceID)
			}()

			next(ctx)
		}
	}
}

// completeAndFlushTrace completes the trace and forwards it to observability plugins.
// This is called either by the middleware defer (for non-streaming) or by streaming handlers.
func (m *TracingMiddleware) completeAndFlushTrace(traceID string) {
	go func() {
		// Clean up the stream accumulator for this trace

		// Get completed trace from store
		completedTrace := m.tracer.Load().EndTrace(traceID)
		if completedTrace == nil {
			return
		}
		// Forward to all observability plugins
		for _, plugin := range *m.obsPlugins.Load() {
			if plugin == nil {
				continue
			}
			// Call inject with a background context (request context is done)
			if err := plugin.Inject(context.Background(), completedTrace); err != nil {
				logger.Warn("observability plugin %s failed to inject trace: %v", plugin.GetName(), err)
			}
		}
		// Return trace to pool for reuse
		m.tracer.Load().ReleaseTrace(completedTrace)
	}()
}

// GetTracer returns the tracer instance for use by streaming handlers
func (m *TracingMiddleware) GetTracer() *tracing.Tracer {
	return m.tracer.Load()
}

// GetObservabilityPlugins filters and returns only observability plugins from a list of plugins.
// Uses Go type assertion to identify plugins implementing the ObservabilityPlugin interface.
func GetObservabilityPlugins(plugins []schemas.Plugin) []schemas.ObservabilityPlugin {
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
