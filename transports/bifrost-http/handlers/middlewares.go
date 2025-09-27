package handlers

import (
	"github.com/valyala/fasthttp"
)

// ChainMiddlewares chains multiple middlewares together
func ChainMiddlewares(handler fasthttp.RequestHandler, middlewares ...fasthttp.RequestHandler) fasthttp.RequestHandler {
	// If no middlewares, return the original handler
	if len(middlewares) == 0 {
		return handler
	}

	return func(ctx *fasthttp.RequestCtx) {
		// Execute all middlewares in order
		for _, middleware := range middlewares {
			middleware(ctx)
		}
		// Execute the handler last
		handler(ctx)
	}
}
