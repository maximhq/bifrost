// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
//
// This package handles the conversion of FastHTTP request contexts to Bifrost contexts,
// ensuring that important metadata and tracking information is preserved across the system.
// It supports propagation of both Prometheus metrics and Maxim tracing data through HTTP headers.
package lib

import (
	"context"
	"strings"

	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/transports/bifrost-http/tracking"
	"github.com/valyala/fasthttp"
)

// ConvertToBifrostContext converts a FastHTTP RequestCtx to a Bifrost context,
// preserving important header values for monitoring and tracing purposes.
//
// The function processes two types of special headers:
// 1. Prometheus Headers (x-bf-prom-*):
//   - All headers prefixed with 'x-bf-prom-' are copied to the context
//   - The prefix is stripped and the remainder becomes the context key
//   - Example: 'x-bf-prom-latency' becomes 'latency' in the context
//
// 2. Maxim Tracing Headers (x-bf-maxim-*):
//   - Specifically handles 'x-bf-maxim-traceID' and 'x-bf-maxim-generationID'
//   - These headers enable trace correlation across service boundaries
//   - Values are stored using Maxim's context keys for consistency
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
//	// bifrostCtx now contains any prometheus and maxim header values
func ConvertToBifrostContext(ctx *fasthttp.RequestCtx) *context.Context {
	bifrostCtx := context.Background()

	// Copy all prometheus header values to the new context
	ctx.Request.Header.VisitAll(func(key, value []byte) {
		keyStr := strings.ToLower(string(key))

		if strings.HasPrefix(keyStr, "x-bf-prom-") {
			labelName := strings.TrimPrefix(keyStr, "x-bf-prom-")
			bifrostCtx = context.WithValue(bifrostCtx, tracking.PrometheusContextKey(labelName), string(value))
		}

		if strings.HasPrefix(keyStr, "x-bf-maxim-") {
			labelName := strings.TrimPrefix(keyStr, "x-bf-maxim-")

			if labelName == string(maxim.GenerationIDKey) {
				bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(labelName), string(value))
			}

			if labelName == string(maxim.TraceIDKey) {
				bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(labelName), string(value))
			}
		}
	})

	return &bifrostCtx
}
