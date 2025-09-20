package handlers

import (
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// BifrostHTTPMiddleware is a middleware function for the Bifrost HTTP transport
type BifrostHTTPMiddleware func(ctx *fasthttp.RequestCtx)

// CorsMiddleware handles CORS headers for localhost and configured allowed origins
func CorsMiddleware(config *lib.Config, next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		origin := string(ctx.Request.Header.Peek("Origin"))
		// Check if origin is allowed (localhost always allowed + configured origins)
		if IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins) {
			ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
		}
		// Setting headers
		ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
		ctx.Response.Header.Set("Access-Control-Max-Age", "86400")
		// Handle preflight OPTIONS requests
		if string(ctx.Method()) == "OPTIONS" {
			ctx.SetStatusCode(fasthttp.StatusOK)
			return
		}
		next(ctx)
	}
}

// ChainMiddlewares chains multiple middlewares together
func ChainMiddlewares(handler fasthttp.RequestHandler, middlewares ...BifrostHTTPMiddleware) fasthttp.RequestHandler {
	// If no middlewares, return the original handler
	if len(middlewares) == 0 {
		return handler
	}
	return func(ctx *fasthttp.RequestCtx) {
		// Execute all middlewares in order
		for _, middleware := range middlewares {
			middleware(ctx)
			// Check if the response is set
			if ctx.Response.StatusCode() != 0 {
				return
			}
		}
		// Execute the handler last
		handler(ctx)
	}
}
