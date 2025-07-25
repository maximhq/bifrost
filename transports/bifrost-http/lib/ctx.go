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

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/plugins/maxim"
	"github.com/maximhq/bifrost/transports/bifrost-http/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/plugins/telemetry"
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
// 3. MCP Headers (x-bf-mcp-*):
//   - Specifically handles 'x-bf-mcp-include-clients', 'x-bf-mcp-exclude-clients', 'x-bf-mcp-include-tools', and 'x-bf-mcp-exclude-tools'
//   - These headers enable MCP client and tool filtering
//   - Values are stored using MCP context keys for consistency
//
// 4. Governance Headers:
//   - x-bf-vk: Virtual key for governance (required for governance to work)
//   - x-bf-team: Team identifier for team-based governance rules
//   - x-bf-user: User identifier for user-based governance rules
//   - x-bf-customer: Customer identifier for customer-based governance rules
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

type ContextKey string

func ConvertToBifrostContext(ctx *fasthttp.RequestCtx) *context.Context {
	bifrostCtx := context.Background()

	// First, check if x-request-id header exists
	requestID := string(ctx.Request.Header.Peek("x-request-id"))
	if requestID == "" {
		requestID = uuid.New().String()
	}
	bifrostCtx = context.WithValue(bifrostCtx, logging.ContextKey("request-id"), requestID)

	// Then process other headers
	ctx.Request.Header.VisitAll(func(key, value []byte) {
		keyStr := strings.ToLower(string(key))

		if strings.HasPrefix(keyStr, "x-bf-prom-") {
			labelName := strings.TrimPrefix(keyStr, "x-bf-prom-")
			bifrostCtx = context.WithValue(bifrostCtx, telemetry.PrometheusContextKey(labelName), string(value))
		}

		if strings.HasPrefix(keyStr, "x-bf-maxim-") {
			labelName := strings.TrimPrefix(keyStr, "x-bf-maxim-")

			if labelName == string(maxim.GenerationIDKey) {
				bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(labelName), string(value))
			}

			if labelName == string(maxim.TraceIDKey) {
				bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(labelName), string(value))
			}

			if labelName == string(maxim.SessionIDKey) {
				bifrostCtx = context.WithValue(bifrostCtx, maxim.ContextKey(labelName), string(value))
			}
		}

		if strings.HasPrefix(keyStr, "x-bf-mcp-") {
			labelName := strings.TrimPrefix(keyStr, "x-bf-mcp-")

			if labelName == "include-clients" || labelName == "exclude-clients" || labelName == "include-tools" || labelName == "exclude-tools" {
				bifrostCtx = context.WithValue(bifrostCtx, ContextKey("mcp-"+labelName), string(value))
				return
			}
		}

		// Handle governance headers (x-bf-team, x-bf-user, x-bf-customer)
		if keyStr == "x-bf-team" || keyStr == "x-bf-user" || keyStr == "x-bf-customer" {
			bifrostCtx = context.WithValue(bifrostCtx, ContextKey(keyStr), string(value))
		}

		// Handle virtual key header (x-bf-vk)
		if keyStr == "x-bf-vk" {
			bifrostCtx = context.WithValue(bifrostCtx, ContextKey(keyStr), string(value))
		}
	})

	return &bifrostCtx
}
