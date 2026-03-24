// Package compat provides LiteLLM-compatible request type conversion decisions
// for the Bifrost gateway. It marks requests that should be converted by core provider
// dispatch for models that don't natively support the requested endpoint type.
//
// When enabled, this plugin:
//   - Decides whether text_completion() should be converted to chat
//   - Decides whether chat_completion() should be converted to responses
//   - Stores the decision in context for core request dispatch
package compat

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const PluginName = "compat"

// Config defines the configuration for the compat plugin
type Config struct {
	ConvertTextToChat      bool `json:"convert_text_to_chat"`
	ConvertChatToResponses bool `json:"convert_chat_to_responses"`
	ShouldDropParams       bool `json:"should_drop_params"`
}

// IsEnabled returns true if any compat feature is enabled
func (c Config) IsEnabled() bool {
	return c.ConvertTextToChat || c.ConvertChatToResponses || c.ShouldDropParams
}

// CompatPlugin provides LiteLLM-compatible request/response transformations.
// When enabled, it automatically converts text completion requests to chat completion
// requests for models that only support chat completions, matching LiteLLM's behavior.
// It also converts chat completion requests to responses for models that only support
// the responses endpoint.
type CompatPlugin struct {
	config       Config
	logger       schemas.Logger
	modelCatalog *modelcatalog.ModelCatalog
}

// Init creates a new compat plugin instance with model catalog support.
// The model catalog is used to determine if a model supports text completion or chat completion natively.
// If the model catalog is nil, the plugin will convert ALL text completion requests to chat completion
// and ALL chat completion requests to responses.
func Init(config Config, logger schemas.Logger, mc *modelcatalog.ModelCatalog) (*CompatPlugin, error) {
	return &CompatPlugin{
		config:       config,
		logger:       logger,
		modelCatalog: mc,
	}, nil
}

// GetName returns the plugin name
func (p *CompatPlugin) GetName() string {
	return PluginName
}

// HTTPTransportPreHook is not used for this plugin
func (p *CompatPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPostHook is not used for this plugin
func (p *CompatPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *CompatPlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// markForConversion checks if the model supports the current request type; if not, mark for conversion
func (p *CompatPlugin) markForConversion(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, currentType schemas.RequestType, targetType schemas.RequestType) {
	shouldConvert := true

	// If no model catalog — we should mark currentType to targetType for conversion
	if p.modelCatalog != nil {
		if p.modelCatalog.IsRequestTypeSupported(model, provider, currentType) {
			p.logger.Debug("compat: model %s/%s supports %v, skipping conversion", provider, model, currentType)
			shouldConvert = false
		}
	}

	if shouldConvert {
		ctx.SetValue(schemas.BifrostContextKeyChangeRequestType, targetType)
		p.logger.Debug("compat: marked %v for core conversion to %v for model %s", currentType, targetType, model)
	}
}

// PreLLMHook intercepts requests and applies LiteLLM-compatible transformation intent.
// For text completion or chat completion requests on models that don't support text completion
// or chat completion, it marks the request so core can convert at provider dispatch time.
func (p *CompatPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if ctx == nil {
		return req, nil, nil
	}

	// Text completion → chat conversion
	if p.config.ConvertTextToChat {
		if (req.RequestType == schemas.TextCompletionRequest || req.RequestType == schemas.TextCompletionStreamRequest) && req.TextCompletionRequest != nil {
			p.markForConversion(ctx, req.TextCompletionRequest.Provider, req.TextCompletionRequest.Model, schemas.TextCompletionRequest, schemas.ChatCompletionRequest)
		}
	}

	// Chat completion → responses conversion
	if p.config.ConvertChatToResponses {
		if (req.RequestType == schemas.ChatCompletionRequest || req.RequestType == schemas.ChatCompletionStreamRequest) && req.ChatRequest != nil {
			p.markForConversion(ctx, req.ChatRequest.Provider, req.ChatRequest.Model, schemas.ChatCompletionRequest, schemas.ResponsesRequest)
		}
	}

	// Compute unsupported parameters to drop based on model catalog allowlist
	if p.config.ShouldDropParams && p.modelCatalog != nil {
		_, model, _ := req.GetRequestFields()
		if model != "" {
			if supportedParams := p.modelCatalog.GetSupportedParameters(model); supportedParams != nil {
				droppedParams := computeUnsupportedParams(req, supportedParams)
				if len(droppedParams) > 0 {
					ctx.SetValue(schemas.BifrostContextKeyCompatPluginDroppedParams, droppedParams)
				}
			}
		}
	}

	return req, nil, nil
}

// PostLLMHook normalizes metadata on converted responses/errors
// when this plugin requested type conversion in PreLLMHook.
func (p *CompatPlugin) PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		return result, bifrostErr, nil
	}

	droppedParams, ok := ctx.Value(schemas.BifrostContextKeyCompatPluginDroppedParams).([]string)
	if !ok {
		return result, bifrostErr, nil
	}

	if len(droppedParams) > 0 {
		if result != nil {
			result.GetExtraFields().DroppedCompatPluginParams = droppedParams
		}

		if bifrostErr != nil {
			bifrostErr.ExtraFields.DroppedCompatPluginParams = droppedParams
		}

	}

	return result, bifrostErr, nil
}

// Cleanup performs plugin cleanup
func (p *CompatPlugin) Cleanup() error {
	return nil
}