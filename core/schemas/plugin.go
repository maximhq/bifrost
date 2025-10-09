// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import "context"

// PluginShortCircuit represents a plugin's decision to short-circuit the normal flow.
// It can contain either a response (success short-circuit), a stream (streaming short-circuit), or an error (error short-circuit).
type PluginShortCircuit struct {
	Response *BifrostResponse    // If set, short-circuit with this response (skips provider call)
	Stream   chan *BifrostStream // If set, short-circuit with this stream (skips provider call)
	Error    *BifrostError       // If set, short-circuit with this error (can set AllowFallbacks field)
}

// Plugin defines the interface for Bifrost plugins.
// Plugins can intercept and modify requests and responses at different stages
// of the processing pipeline.
// User can provide multiple plugins in the BifrostConfig.
// PreHooks are executed in the order they are registered.
// PostHooks are executed in the reverse order of PreHooks.
//
// Execution order:
// 1. TransportInterceptor (HTTP transport only, modifies raw headers/body before entering Bifrost core)
// 2. PreHook (executed in registration order)
// 3. Provider call
// 4. PostHook (executed in reverse order of PreHooks)
//
// Common use cases: rate limiting, caching, logging, monitoring, request transformation, governance.
//
// Plugin error handling:
// - No Plugin errors are returned to the caller; they are logged as warnings by the Bifrost instance.
// - PreHook and PostHook can both modify the request/response and the error. Plugins can recover from errors (set error to nil and provide a response), or invalidate a response (set response to nil and provide an error).
// - PostHook is always called with both the current response and error, and should handle either being nil.
// - Only truly empty errors (no message, no error, no status code, no type) are treated as recoveries by the pipeline.
// - If a PreHook returns a PluginShortCircuit, the provider call may be skipped and only the PostHook methods of plugins that had their PreHook executed are called in reverse order.
// - The plugin pipeline ensures symmetry: for every PreHook executed, the corresponding PostHook will be called in reverse order.
//
// IMPORTANT: When returning BifrostError from PreHook or PostHook:
// - You can set the AllowFallbacks field to control fallback behavior
// - AllowFallbacks = &true: Allow Bifrost to try fallback providers
// - AllowFallbacks = &false: Do not try fallbacks, return error immediately
// - AllowFallbacks = nil: Treated as true by default (allow fallbacks for resilience)
//
// Plugin authors should ensure their hooks are robust to both response and error being nil, and should not assume either is always present.

type Plugin interface {
	// GetName returns the name of the plugin.
	GetName() string

	// TransportInterceptor is called at the HTTP transport layer before requests enter Bifrost core.
	// It allows plugins to modify raw HTTP headers and body before transformation into BifrostRequest.
	// Only invoked when using HTTP transport (bifrost-http), not when using Bifrost as a Go SDK directly.
	// Returns modified headers, modified body, and any error that occurred during interception.
	TransportInterceptor(url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error)

	// PreHook is called before a request is processed by a provider.
	// It allows plugins to modify the request before it is sent to the provider.
	// The context parameter can be used to maintain state across plugin calls.
	// Returns the modified request, an optional short-circuit decision, and any error that occurred during processing.
	PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error)

	// PostHook is called after a response is received from a provider or a PreHook short-circuit.
	// It allows plugins to modify the response and/or error before it is returned to the caller.
	// Plugins can recover from errors (set error to nil and provide a response), or invalidate a response (set response to nil and provide an error).
	// Returns the modified response, bifrost error, and any error that occurred during processing.
	PostHook(ctx *context.Context, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError, error)

	// Cleanup is called on bifrost shutdown.
	// It allows plugins to clean up any resources they have allocated.
	// Returns any error that occurred during cleanup, which will be logged as a warning by the Bifrost instance.
	Cleanup() error
}

// PluginConfig is the configuration for a plugin.
// It contains the name of the plugin, whether it is enabled, and the configuration for the plugin.
type PluginConfig struct {
	Enabled bool   `json:"enabled"`
	Name    string `json:"name"`
	Config  any    `json:"config,omitempty"`
}
