package promptcompression

import "github.com/maximhq/bifrost/core/schemas"

// PluginName is the name under which the plugin is registered.
const PluginName = "prompt_compression"

// Plugin compresses old tool outputs in agent-loop requests.
type Plugin struct {
	s      settings
	logger schemas.Logger
}

// Init creates the plugin. A nil config means all defaults.
func Init(config *Config, logger schemas.Logger) (schemas.LLMPlugin, error) {
	s, err := config.resolve()
	if err != nil {
		return nil, err
	}
	return &Plugin{s: s, logger: logger}, nil
}

// GetName returns the plugin name.
func (p *Plugin) GetName() string { return PluginName }

// Cleanup is a no-op; the plugin holds no resources.
func (p *Plugin) Cleanup() error { return nil }

// PreRequestHook is a pass-through.
func (p *Plugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook is a pass-through (compression is added in a later task).
func (p *Plugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook is a pass-through.
func (p *Plugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}
