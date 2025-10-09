package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// BifrostHTTPMiddleware is a middleware function for the Bifrost HTTP transport
// It follows the standard pattern: receives the next handler and returns a new handler
type BifrostHTTPMiddleware func(next fasthttp.RequestHandler) fasthttp.RequestHandler

// CorsMiddleware handles CORS headers for localhost and configured allowed origins
func CorsMiddleware(config *lib.Config) BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			origin := string(ctx.Request.Header.Peek("Origin"))
			allowed := IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins)
			// Check if origin is allowed (localhost always allowed + configured origins)
			if allowed {
				ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
				ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
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

func TransportInterceptorMiddleware(config *lib.Config) BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// Get plugins from config
			plugins := config.LoadedPlugins
			if len(plugins) == 0 {
				next(ctx)
				return
			}

			// If governance plugin is not loaded, skip interception
			if !config.LoadedPluginsMap[governance.PluginName] {
				next(ctx)
				return
			}

			// Parse headers
			headers := make(map[string]string)
			ctx.Request.Header.VisitAll(func(key, value []byte) {
				headers[string(key)] = string(value)
			})

			// Unmarshal request body
			requestBody := make(map[string]any)
			bodyBytes := ctx.Request.Body()
			if len(bodyBytes) > 0 {
				if err := json.Unmarshal(bodyBytes, &requestBody); err != nil {
					// If body is not valid JSON, log warning and continue without interception
					logger.Warn(fmt.Sprintf("TransportInterceptor: Failed to unmarshal request body: %v", err))
					next(ctx)
					return
				}
			}

			// Call TransportInterceptor on all plugins
			for _, plugin := range plugins {
				modifiedHeaders, modifiedBody, err := plugin.TransportInterceptor(string(ctx.Request.URI().RequestURI()), headers, requestBody)
				if err != nil {
					logger.Warn(fmt.Sprintf("TransportInterceptor: Plugin '%s' returned error: %v", plugin.GetName(), err))
					// Continue with unmodified headers/body
					continue
				}
				// Update headers and body with modifications
				headers = modifiedHeaders
				requestBody = modifiedBody
			}

			// Marshal the body back to JSON
			if len(requestBody) > 0 {
				updatedBody, err := json.Marshal(requestBody)
				if err != nil {
					SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("TransportInterceptor: Failed to marshal request body: %v", err), logger)
					return
				}
				ctx.Request.SetBody(updatedBody)
			}

			// Set modified headers back on the request
			for key, value := range headers {
				ctx.Request.Header.Set(key, value)
			}

			next(ctx)
		}
	}
}

// ChainMiddlewares chains multiple middlewares together
// Middlewares are applied in order: the first middleware wraps the second, etc.
// This allows earlier middlewares to short-circuit by not calling next(ctx)
func ChainMiddlewares(handler fasthttp.RequestHandler, middlewares ...BifrostHTTPMiddleware) fasthttp.RequestHandler {
	// If no middlewares, return the original handler
	if len(middlewares) == 0 {
		return handler
	}
	// Build the chain from right to left (last middleware wraps the handler)
	// This ensures execution order is left to right (first middleware executes first)
	chained := handler
	for i := len(middlewares) - 1; i >= 0; i-- {
		chained = middlewares[i](chained)
	}
	return chained
}
