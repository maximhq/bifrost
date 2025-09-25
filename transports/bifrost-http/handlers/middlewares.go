package handlers

import (
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
