// Package litellmcompat provides LiteLLM-compatible request type conversion decisions
// for the Bifrost gateway. It marks requests that should be converted by core provider
// dispatch for models that don't natively support the requested endpoint type.
//
// When enabled, this plugin:
//   - Decides whether text_completion() should be converted to chat
//   - Decides whether chat_completion() should be converted to responses
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
// It also converts chat completion requests to responses for models that only support
// the responses endpoint.
type LiteLLMCompatPlugin struct {
	config       Config
	logger       schemas.Logger
	modelCatalog *modelcatalog.ModelCatalog
}

// Init creates a new litellmcompat plugin instance with model catalog support.
// The model catalog is used to determine if a model supports text completion or chat completion natively.
// If the model catalog is nil, the plugin will convert ALL text completion requests to chat completion
// and ALL chat completion requests to responses.
func Init(config Config, logger schemas.Logger, mc *modelcatalog.ModelCatalog) (*LiteLLMCompatPlugin, error) {
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
// For chat completion requests on models that don't support chat completion,
// it marks the request so core can convert at provider dispatch time.
func (p *LiteLLMCompatPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	// Reset context keys
	if ctx != nil {
		ctx.SetValue(schemas.BifrostContextKeyShouldConvertTextToChat, false)
		ctx.SetValue(schemas.BifrostContextKeyShouldConvertChatToResponses, false)
	}

	// Apply request transforms in sequence
	req = transformTextToChatRequest(ctx, req, p.modelCatalog, p.logger)
	req = transformChatToResponsesRequest(ctx, req, p.modelCatalog, p.logger)

	// Compute unsupported parameters to drop based on model catalog allowlist
	if ctx != nil && p.modelCatalog != nil {
		model := getModelFromRequest(req)
		if model != "" {
			if supportedParams := p.modelCatalog.GetSupportedParameters(model); supportedParams != nil {
				droppedParams := computeUnsupportedParams(req, supportedParams)
				if len(droppedParams) > 0 {
					ctx.SetValue(schemas.BifrostContextKeyLiteLLMCompatDroppedParams, droppedParams)
				}
			}
		}
	}

	return req, nil, nil
}

// PostLLMHook normalizes metadata on converted responses/errors
// when this plugin requested type conversion in PreLLMHook.
func (p *LiteLLMCompatPlugin) PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if result != nil {
		result = transformTextToChatResponse(ctx, result, p.logger)
		result = transformChatToResponsesResponse(ctx, result, p.logger)
	}
	if bifrostErr != nil {
		bifrostErr = transformTextToChatError(ctx, bifrostErr)
		bifrostErr = transformChatToResponsesError(ctx, bifrostErr)
	}
	return result, bifrostErr, nil
}

// getModelFromRequest extracts the model name from a BifrostRequest,
// checking each request type in order.
func getModelFromRequest(req *schemas.BifrostRequest) string {
	if req == nil {
		return ""
	}
	if req.ChatRequest != nil {
		return req.ChatRequest.Model
	}
	if req.ResponsesRequest != nil {
		return req.ResponsesRequest.Model
	}
	if req.TextCompletionRequest != nil {
		return req.TextCompletionRequest.Model
	}
	return ""
}

// Cleanup performs plugin cleanup
func (p *LiteLLMCompatPlugin) Cleanup() error {
	return nil
}