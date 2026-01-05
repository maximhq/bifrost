// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"context"
	"strings"
	"sync"
)

// PluginStatus constants
const (
	PluginStatusActive        = "active"
	PluginStatusError         = "error"
	PluginStatusDisabled      = "disabled"
	PluginStatusLoading       = "loading"
	PluginStatusUninitialized = "uninitialized"
	PluginStatusUnloaded      = "unloaded"
	PluginStatusLoaded        = "loaded"
)

// PluginStatus represents the status of a plugin.
type PluginStatus struct {
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Logs   []string `json:"logs"`
}

// HTTPRequest is a serializable representation of an HTTP request.
// Used for plugin HTTP transport interception (supports both native .so and WASM plugins).
// This type is pooled for allocation control - use AcquireHTTPRequest and ReleaseHTTPRequest.
type HTTPRequest struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Headers    map[string]string `json:"headers"`
	Query      map[string]string `json:"query"`
	Body       []byte            `json:"body"`
	PathParams map[string]string `json:"path_params"` // Path variables extracted from the URL pattern (e.g., {model})
}

// CaseInsensitiveHeaderLookup looks up a header key in a case-insensitive manner
func (req *HTTPRequest) CaseInsensitiveHeaderLookup(key string) string {
	return caseInsensitiveLookup(req.Headers, key)
}

// CaseInsensitiveQueryLookup looks up a query key in a case-insensitive manner
func (req *HTTPRequest) CaseInsensitiveQueryLookup(key string) string {
	return caseInsensitiveLookup(req.Query, key)
}

// CaseInsensitivePathParamLookup looks up a path parameter key in a case-insensitive manner
func (req *HTTPRequest) CaseInsensitivePathParamLookup(key string) string {
	return caseInsensitiveLookup(req.PathParams, key)
}

// CaseInsensitiveLookup looks up a key in a case-insensitive manner for a map of strings
// Returns the value if found, otherwise an empty string
func caseInsensitiveLookup(data map[string]string, key string) string {
	if data == nil || key == "" {
		return ""
	}
	// exact match
	if v, ok := data[key]; ok {
		return v
	}
	// lower key checks
	lowerKey := strings.ToLower(key)
	if v, ok := data[lowerKey]; ok {
		return v
	}
	// case-insensitive iteration
	for k, v := range data {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// HTTPResponse is a serializable representation of an HTTP response.
// Used for short-circuit responses in plugin HTTP transport interception.
type HTTPResponse struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       []byte            `json:"body"`
}

// httpRequestPool is the pool for HTTPRequest objects to reduce allocations.
var httpRequestPool = sync.Pool{
	New: func() any {
		return &HTTPRequest{
			Headers:    make(map[string]string, 16),
			Query:      make(map[string]string, 8),
			PathParams: make(map[string]string, 4),
		}
	},
}

// AcquireHTTPRequest gets an HTTPRequest from the pool.
// The returned HTTPRequest is ready to use with pre-allocated maps.
// Call ReleaseHTTPRequest when done to return it to the pool.
func AcquireHTTPRequest() *HTTPRequest {
	return httpRequestPool.Get().(*HTTPRequest)
}

// ReleaseHTTPRequest returns an HTTPRequest to the pool.
// The HTTPRequest is reset before being returned to the pool.
// Do not use the HTTPRequest after calling this function.
func ReleaseHTTPRequest(req *HTTPRequest) {
	if req == nil {
		return
	}
	// Clear the maps
	clear(req.Headers)
	clear(req.Query)
	clear(req.PathParams)
	// Reset fields
	req.Method = ""
	req.Path = ""
	req.Body = nil
	httpRequestPool.Put(req)
}

// Plugin defines the interface for Bifrost plugins.
// Plugins can intercept and modify requests and responses at different stages
// of the processing pipeline.
// User can provide multiple plugins in the BifrostConfig.
// PreHooks are executed in the order they are registered.
// PostHooks are executed in the reverse order of PreHooks.
//
// Execution order:
// 1. HTTPTransportIntercept (HTTP transport only, modifies raw headers/body before entering Bifrost core)
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

	// HTTPTransportIntercept is called at the HTTP transport layer before requests enter Bifrost core.
	// It receives a serializable HTTPRequest and allows plugins to modify it in-place.
	// Only invoked when using HTTP transport (bifrost-http), not when using Bifrost as a Go SDK directly.
	// Works with both native .so plugins and WASM plugins due to serializable types.
	//
	// Return values:
	// - (nil, nil): Continue to next plugin/handler, request modifications are applied
	// - (*HTTPResponse, nil): Short-circuit with this response, skip remaining plugins and provider call
	// - (nil, error): Short-circuit with error response
	//
	// Return nil for both values if the plugin doesn't need HTTP transport interception.
	HTTPTransportIntercept(ctx *BifrostContext, req *HTTPRequest) (*HTTPResponse, error)

	// PreHook is called before a request is processed by a provider.
	// It allows plugins to modify the request before it is sent to the provider.
	// The context parameter can be used to maintain state across plugin calls.
	// Returns the modified request, an optional short-circuit decision, and any error that occurred during processing.
	PreHook(ctx *BifrostContext, req *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error)

	// PostHook is called after a response is received from a provider or a PreHook short-circuit.
	// It allows plugins to modify the response and/or error before it is returned to the caller.
	// Plugins can recover from errors (set error to nil and provide a response), or invalidate a response (set response to nil and provide an error).
	// Returns the modified response, bifrost error, and any error that occurred during processing.
	PostHook(ctx *BifrostContext, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError, error)

	// Cleanup is called on bifrost shutdown.
	// It allows plugins to clean up any resources they have allocated.
	// Returns any error that occurred during cleanup, which will be logged as a warning by the Bifrost instance.
	Cleanup() error
}

// PluginConfig is the configuration for a plugin.
// It contains the name of the plugin, whether it is enabled, and the configuration for the plugin.
type PluginConfig struct {
	Enabled bool    `json:"enabled"`
	Name    string  `json:"name"`
	Path    *string `json:"path,omitempty"`
	Version *int16  `json:"version,omitempty"`
	Config  any     `json:"config,omitempty"`
}

// ObservabilityPlugin is an interface for plugins that receive completed traces
// for forwarding to observability backends (e.g., OTEL collectors, Datadog, etc.)
//
// ObservabilityPlugins are called asynchronously after the HTTP response has been
// written to the wire, ensuring they don't add latency to the client response.
//
// Plugins implementing this interface will:
// 1. Continue to work as regular plugins via PreHook/PostHook
// 2. Additionally receive completed traces via the Inject method
//
// Example backends: OpenTelemetry collectors, Datadog, Jaeger, Maxim, etc.
//
// Note: Go type assertion (plugin.(ObservabilityPlugin)) is used to identify
// plugins implementing this interface - no marker method is needed.
type ObservabilityPlugin interface {
	Plugin

	// Inject receives a completed trace for forwarding to observability backends.
	// This method is called asynchronously after the response has been written to the client.
	// The trace contains all spans that were added during request processing.
	//
	// Implementations should:
	// - Convert the trace to their backend's format
	// - Send the trace to the backend (can be async)
	// - Handle errors gracefully (log and continue)
	//
	// The context passed is a fresh background context, not the request context.
	Inject(ctx context.Context, trace *Trace) error
}
