package plugins

import (
	"plugin"

	"github.com/maximhq/bifrost/core/schemas"
)

// DynamicPlugin is the interface for a dynamic plugin
type DynamicLLMPlugin struct {
	Enabled bool
	Path    string

	Config any

	filename string
	plugin   *plugin.Plugin

	getName               func() string
	httpTransportPreHook  func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)
	httpTransportPostHook func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error
	preHook               func(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error)
	postHook              func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)
	cleanup               func() error
}

// GetName returns the name of the plugin
func (dp *DynamicLLMPlugin) GetName() string {
	return dp.getName()
}

// HTTPTransportPreHook intercepts HTTP requests at the transport layer before entering Bifrost core
func (dp *DynamicLLMPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return dp.httpTransportPreHook(ctx, req)
}

// HTTPTransportPostHook intercepts HTTP responses at the transport layer after exiting Bifrost core
func (dp *DynamicLLMPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return dp.httpTransportPostHook(ctx, req, resp)
}

// PreLLMHook is not used for dynamic plugins
func (dp *DynamicLLMPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return dp.preHook(ctx, req)
}

// PostLLMHook is invoked by PluginPipeline.RunPostHooks in core/bifrost.go
func (dp *DynamicLLMPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return dp.postHook(ctx, resp, bifrostErr)
}

// Cleanup is invoked by core/bifrost.go during plugin unload, reload, and shutdown
func (dp *DynamicLLMPlugin) Cleanup() error {
	return dp.cleanup()
}
