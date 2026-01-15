package plugins

import (
	"plugin"

	"github.com/maximhq/bifrost/core/schemas"
)

// DynamicPlugin is the interface for a dynamic plugin
type DynamicPlugin struct {
	Enabled bool
	Path    string

	Config any

	filename string
	plugin   *plugin.Plugin

	getName               func() string
	httpTransportPreHook  func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)
	httpTransportPostHook func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error
	preHook               func(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error)
	postHook              func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error)
	cleanup               func() error
}

// GetName returns the name of the plugin
func (dp *DynamicPlugin) GetName() string {
	return dp.getName()
}

// HTTPTransportPreHook intercepts HTTP requests at the transport layer before entering Bifrost core
func (dp *DynamicPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if dp.httpTransportPreHook == nil {
		return nil, nil
	}
	return dp.httpTransportPreHook(ctx, req)
}

// HTTPTransportPostHook intercepts HTTP responses at the transport layer after exiting Bifrost core
func (dp *DynamicPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	if dp.httpTransportPostHook == nil {
		return nil
	}
	return dp.httpTransportPostHook(ctx, req, resp)
}

// PreHook is not used for dynamic plugins
func (dp *DynamicPlugin) PreHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	return dp.preHook(ctx, req)
}

// PostHook is not used for dynamic plugins
func (dp *DynamicPlugin) PostHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return dp.postHook(ctx, resp, bifrostErr)
}

// Cleanup is not used for dynamic plugins
func (dp *DynamicPlugin) Cleanup() error {
	return dp.cleanup()
}
