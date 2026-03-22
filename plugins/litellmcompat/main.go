// Package litellmcompat provides LiteLLM-compatible text-to-chat conversion decisions
// for the Bifrost gateway. It marks text completion requests that should be converted
// by core provider dispatch for models that only support chat completions.
//
// When enabled, this plugin:
//   - Decides whether text_completion() should be converted to chat
//   - Stores the decision in context for core request dispatch
package litellmcompat

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const (
	PluginName = "litellmcompat"
)

// Config defines the configuration for the litellmcompat plugin
type Config struct {
	Enabled bool `json:"enabled"`
}

// LiteLLMCompatPlugin provides LiteLLM-compatible request/response transformations.
// When enabled, it automatically converts text completion requests to chat completion
// requests for models that only support chat completions, matching LiteLLM's behavior.
type LiteLLMCompatPlugin struct {
	config       Config
	logger       schemas.Logger
	modelCatalog *modelcatalog.ModelCatalog
}

// Init creates a new litellmcompat plugin instance
func Init(config Config, logger schemas.Logger) (*LiteLLMCompatPlugin, error) {
	return &LiteLLMCompatPlugin{
		config: config,
		logger: logger,
	}, nil
}

// InitWithModelCatalog creates a new litellmcompat plugin instance with model catalog support.
// The model catalog is used to determine if a model supports text completion natively.
// If the model catalog is nil, the plugin will convert ALL text completion requests.
func InitWithModelCatalog(config Config, logger schemas.Logger, mc *modelcatalog.ModelCatalog) (*LiteLLMCompatPlugin, error) {
	return &LiteLLMCompatPlugin{
		config:       config,
		logger:       logger,
		modelCatalog: mc,
	}, nil
}

// SetModelCatalog sets the model catalog for checking text completion support.
// This can be called after initialization to add model catalog support.
func (p *LiteLLMCompatPlugin) SetModelCatalog(mc *modelcatalog.ModelCatalog) {
	p.modelCatalog = mc
}

// GetName returns the plugin name
func (p *LiteLLMCompatPlugin) GetName() string {
	return PluginName
}

// HTTPTransportPreHook is not used for this plugin
func (p *LiteLLMCompatPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPostHook is not used for this plugin
func (p *LiteLLMCompatPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *LiteLLMCompatPlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// PreLLMHook intercepts requests and applies LiteLLM-compatible transformation intent.
// For text completion requests on models that don't support text completion,
// it marks the request so core can convert at provider dispatch time.
func (p *LiteLLMCompatPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	// Apply request transforms in sequence
	req = transformTextToChatRequest(ctx, req, p.modelCatalog, p.logger)
	return req, nil, nil
}

// PostLLMHook normalizes metadata on converted responses/errors
// when this plugin requested text->chat conversion in PreLLMHook.
func (p *LiteLLMCompatPlugin) PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if result != nil {
		result = transformTextToChatResponse(ctx, result, p.logger)
	}
	if bifrostErr != nil {
		bifrostErr = transformTextToChatError(ctx, bifrostErr)
	}
	return result, bifrostErr, nil
}

// Cleanup performs plugin cleanup
func (p *LiteLLMCompatPlugin) Cleanup() error {
	return nil
}
