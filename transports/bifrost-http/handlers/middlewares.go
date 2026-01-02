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
			logger.Debug("CorsMiddleware: %s", string(ctx.Path()))
			origin := string(ctx.Request.Header.Peek("Origin"))
			allowed := IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins)
			// Check if origin is allowed (localhost always allowed + configured origins)
			if allowed {
				ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
				ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD")
				ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-Stainless-Timeout")
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

// TransportInterceptorMiddleware collects all plugin HTTP transport middleware and chains them.
func TransportInterceptorMiddleware(config *lib.Config) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			plugins := config.GetLoadedPlugins()
			if len(plugins) == 0 {
				next(ctx)
				return
			}
			pluginsMiddlewareChain := []schemas.BifrostHTTPMiddleware{}
			for _, plugin := range plugins {
				middleware := plugin.HTTPTransportMiddleware()
				// Collect plugin HTTP transport middleware
				if middleware == nil {
					continue
				}
				pluginsMiddlewareChain = append(pluginsMiddlewareChain, middleware)
			}
			lib.ChainMiddlewares(next, pluginsMiddlewareChain...)(ctx)
		}
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

// AuthMiddleware if authConfig is set, it will verify the auth cookie in the header
// This uses basic auth style username + password based authentication
// No session tracking is used, so this is not suitable for production environments
// These basicauth routes are only used for the dashboard and API routes
func AuthMiddleware(store configstore.ConfigStore) schemas.BifrostHTTPMiddleware {
	if store == nil {
		logger.Info("auth middleware is disabled because store is not present")
		return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
			return next
		}
	}
	authConfig, err := store.GetAuthConfig(context.Background())
	if err != nil || authConfig == nil || !authConfig.IsEnabled {
		return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
			return next
		}
	}
	whitelistedRoutes := []string{
		"/api/session/is-auth-enabled",
		"/api/session/login",
		"/api/session/logout",
		"/health",
	}
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// We skip authorization for the login route
			if slices.Contains(whitelistedRoutes, string(ctx.Request.URI().RequestURI())) {
				next(ctx)
				return
			}
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
					if !validateSession(ctx, store, token) {
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
				if !validateSession(ctx, store, token) {
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
		// This must happen before EndTrace to ensure all accumulated data is available
		m.tracer.Load().CleanupStreamAccumulator(traceID)

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
