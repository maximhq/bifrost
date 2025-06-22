// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
//
// This package handles the conversion of FastHTTP request contexts to Bifrost contexts,
// ensuring that important metadata and tracking information is preserved across the system.
// It supports propagation of Prometheus metrics data through HTTP headers.
package lib

import (
	"context"
	"strings"

	"github.com/maximhq/bifrost/transports/bifrost-http/tracking"
	"github.com/valyala/fasthttp"
)

// ConvertToBifrostContext converts a FastHTTP RequestCtx to a Bifrost context,
// preserving important header values for monitoring and tracing purposes.
//
// The function processes Prometheus headers (x-bf-prom-*):
// - All headers prefixed with 'x-bf-prom-' are copied to the context
// - The prefix is stripped and the remainder becomes the context key
// - Example: 'x-bf-prom-latency' becomes 'latency' in the context
//
// Parameters:
//   - ctx: The FastHTTP request context containing the original headers
//
// Returns:
//   - *context.Context: A new context.Context containing the propagated values
//
// Example Usage:
//
//	fastCtx := &fasthttp.RequestCtx{...}
//	bifrostCtx := ConvertToBifrostContext(fastCtx)
//	// bifrostCtx now contains any prometheus header values
func ConvertToBifrostContext(ctx *fasthttp.RequestCtx) *context.Context {
	bifrostCtx := context.Background()

	// Process all headers and extract relevant ones
	ctx.Request.Header.VisitAll(func(key, value []byte) {
		keyStr := string(key)
		valueStr := string(value)

		// Handle Prometheus headers (x-bf-prom-*)
		if strings.HasPrefix(strings.ToLower(keyStr), "x-bf-prom-") {
			// Remove the prefix and use the remainder as the context key
			prometheusKey := strings.TrimPrefix(strings.ToLower(keyStr), "x-bf-prom-")
			bifrostCtx = context.WithValue(bifrostCtx, tracking.PrometheusContextKey(prometheusKey), valueStr)
		}
	})

	return &bifrostCtx
}
