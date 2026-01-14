package lib

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ChainMiddlewares chains multiple middlewares together
// Middlewares are applied in order: the first middleware wraps the second, etc.
// This allows earlier middlewares to short-circuit by not calling next(ctx)
func ChainMiddlewares(handler fasthttp.RequestHandler, middlewares ...schemas.BifrostHTTPMiddleware) fasthttp.RequestHandler {
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
