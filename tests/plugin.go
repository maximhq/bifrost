// Package tests provides test utilities and configurations for the Bifrost system.
// It includes test implementations of interfaces, mock objects, and helper functions
// for testing the Bifrost functionality with various AI providers.
package tests

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/interfaces"

	"github.com/maximhq/maxim-go"
	"github.com/maximhq/maxim-go/logging"
)

// This file contains the Plugin implementation using maxim's logger for testing purposes.
// If you wish to not use maxim's logger, you pass nil in the Plugins field when initializing Bifrost in tests/setup.go, or implement your own Plugin.

// contextKey is a custom type for context keys to prevent key collisions in the context.
// It provides type safety for context values and ensures that context keys are unique
// across different packages.
type contextKey string

// traceIDKey is the context key used to store and retrieve trace IDs.
// This constant provides a consistent key for tracking request traces
// throughout the request/response lifecycle.
const (
	traceIDKey contextKey = "traceID"
)

// Plugin implements the interfaces.Plugin interface for testing purposes.
// It provides request and response tracing functionality using the Maxim logger,
// allowing detailed tracking of requests and responses during testing.
//
// Fields:
//   - logger: A Maxim logger instance used for tracing requests and responses
type Plugin struct {
	logger *logging.Logger
}

// PreHook is called before a request is processed by Bifrost.
// It creates a new trace for the incoming request and stores the trace ID in the context.
// The trace includes request details that can be used for debugging and monitoring.
//
// Parameters:
//   - ctx: Pointer to the context.Context that will store the trace ID
//   - req: The incoming Bifrost request to be traced
//
// Returns:
//   - *interfaces.BifrostRequest: The original request, unmodified
//   - error: Always returns nil as this implementation doesn't produce errors
//
// The trace ID format is "YYYYMMDD_HHmmssSSS" based on the current time.
// If the context is nil, tracing information will still be logged but not stored in context.
func (plugin *Plugin) PreHook(ctx *context.Context, req *interfaces.BifrostRequest) (*interfaces.BifrostRequest, error) {
	traceID := time.Now().Format("20060102_150405000")

	trace := plugin.logger.Trace(&logging.TraceConfig{
		Id:   traceID,
		Name: maxim.StrPtr("bifrost"),
	})

	trace.SetInput(fmt.Sprintf("New Request Incoming: %v", req))

	if ctx != nil {
		// Store traceID in context
		*ctx = context.WithValue(*ctx, traceIDKey, traceID)
	}

	return req, nil
}

// PostHook is called after a request has been processed by Bifrost.
// It retrieves the trace ID from the context and logs the response details.
// This completes the request trace by adding response information.
//
// Parameters:
//   - ctxRef: Pointer to the context.Context containing the trace ID
//   - res: The Bifrost response to be traced
//
// Returns:
//   - *interfaces.BifrostResponse: The original response, unmodified
//   - error: Returns an error if the trace ID cannot be retrieved from the context
//
// If the context is nil or the trace ID is not found, an error will be returned
// but the response will still be passed through unmodified.
func (plugin *Plugin) PostHook(ctxRef *context.Context, res *interfaces.BifrostResponse) (*interfaces.BifrostResponse, error) {
	// Get traceID from context
	if ctxRef != nil {
		ctx := *ctxRef
		traceID, ok := ctx.Value(traceIDKey).(string)
		if !ok {
			return res, fmt.Errorf("traceID not found in context")
		}

		plugin.logger.SetTraceOutput(traceID, fmt.Sprintf("Response: %v", res))
	}

	return res, nil
}
