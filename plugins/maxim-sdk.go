// Package plugins provides plugins for the Bifrost system.
// This file contains the Plugin implementation using maxim's logger plugin for bifrost.
package plugins

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/maximhq/maxim-go"
	"github.com/maximhq/maxim-go/logging"
)

// NewMaximLogger initializes and returns a Plugin instance for Maxim's logger.
//
// Parameters:
//   - apiKey: API key for Maxim SDK authentication
//   - loggerId: ID for the Maxim logger instance
//
// Returns:
//   - schemas.Plugin: A configured plugin instance for request/response tracing
//   - error: Any error that occurred during plugin initialization
func NewMaximLoggerPlugin(apiKey string, loggerId string) (schemas.Plugin, error) {
	// check if Maxim Logger variables are set
	if apiKey == "" {
		return nil, fmt.Errorf("apiKey is not set")
	}

	if loggerId == "" {
		return nil, fmt.Errorf("loggerId is not set")
	}

	mx := maxim.Init(&maxim.MaximSDKConfig{ApiKey: apiKey})

	logger, err := mx.GetLogger(&logging.LoggerConfig{Id: loggerId})
	if err != nil {
		return nil, err
	}

	plugin := &Plugin{logger}

	return plugin, nil
}

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

// Plugin implements the schemas.Plugin interface for Maxim's logger.
// It provides request and response tracing functionality using the Maxim logger,
// allowing detailed tracking of requests and responses.
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
//   - *schemas.BifrostRequest: The original request, unmodified
//   - error: Always returns nil as this implementation doesn't produce errors
//
// The trace ID format is "YYYYMMDD_HHmmssSSS" based on the current time.
// If the context is nil, tracing information will still be logged but not stored in context.
func (plugin *Plugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, error) {
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
//   - *schemas.BifrostResponse: The original response, unmodified
//   - error: Returns an error if the trace ID cannot be retrieved from the context
//
// If the context is nil or the trace ID is not found, an error will be returned
// but the response will still be passed through unmodified.
func (plugin *Plugin) PostHook(ctxRef *context.Context, res *schemas.BifrostResponse) (*schemas.BifrostResponse, error) {
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
