// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import "context"

// Plugin defines the interface for Bifrost plugins.
// Plugins can intercept and modify requests and responses at different stages
// of the processing pipeline.
// User can provide multiple plugins in the BifrostConfig.
// PreHooks are executed in the order they are registered.
// PostHooks are executed in the reverse order of PreHooks.

// PreHooks and PostHooks can be used to implement custom logic, such as:
// - Rate limiting
// - Caching
// - Logging
// - Monitoring

type Plugin interface {
	// PreHook is called before a request is processed by a provider.
	// It allows plugins to modify the request before it is sent to the provider.
	// The context parameter can be used to maintain state across plugin calls.
	// Returns the modified request and any error that occurred during processing.
	PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, error)

	// PostHook is called after a response is received from a provider.
	// It allows plugins to modify the response before it is returned to the caller.
	// Returns the modified response and any error that occurred during processing.
	PostHook(ctx *context.Context, result *BifrostResponse) (*BifrostResponse, error)
}
