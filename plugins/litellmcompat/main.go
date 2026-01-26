// Package litellmcompat provides LiteLLM-compatible request/response transformations
// for the Bifrost gateway. It enables automatic conversion of text completion requests
// to chat completion requests for models that only support chat completions, matching
// LiteLLM's behavior.
//
// When enabled, this plugin:
//   - Silently converts text_completion() calls to chat completion format
//   - Routes to the chat completion endpoint
//   - Transforms the response back to text completion format
//   - Places content in choices[0].text instead of choices[0].message.content
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

// PreHook intercepts requests and applies LiteLLM-compatible transformations.
// For text completion requests on models that don't support text completion,
// it converts them to chat completion requests.
func (p *LiteLLMCompatPlugin) PreHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	tc := &TransformContext{}

	// Apply request transforms in sequence
	req = transformTextToChatRequest(ctx, req, tc, p.modelCatalog, p.logger)

	// Store the transform context for use in PostHook
	ctx.SetValue(TransformContextKey, tc)

	return req, nil, nil
}

// PostHook processes responses and applies LiteLLM-compatible transformations.
// If a text completion request was converted to chat, this converts the
// chat response back to text completion format.
func (p *LiteLLMCompatPlugin) PostHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	// Retrieve the transform context
	transformCtxValue := ctx.Value(TransformContextKey)
	if transformCtxValue == nil {
		return result, bifrostErr, nil
	}
	tc, ok := transformCtxValue.(*TransformContext)
	if !ok || tc == nil {
		return result, bifrostErr, nil
	}

	// Apply response transforms in sequence
	// Note: tool-call content runs before text-to-chat because text-to-chat may convert
	// the response type, and tool-call content needs to operate on chat responses
	if result != nil {
		result = transformTextToChatResponse(ctx, result, tc, p.logger)
	}

	// Transform error metadata if there's an error
	if bifrostErr != nil {
		bifrostErr = transformTextToChatError(ctx, bifrostErr, tc)
	}

	return result, bifrostErr, nil
}

// Cleanup performs plugin cleanup
func (p *LiteLLMCompatPlugin) Cleanup() error {
	return nil
}
