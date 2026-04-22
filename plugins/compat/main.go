// Package compat provides LiteLLM-compatible request normalization for the
// Bifrost gateway. It drops unsupported model params first, then rewrites
// requests to a compatible endpoint type when the target model does not support
// the caller's original request type.
package compat

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const PluginName = "compat"

// Config defines the configuration for the compat plugin.
type Config struct {
	ConvertTextToChat      bool `json:"convert_text_to_chat"`
	ConvertChatToResponses bool `json:"convert_chat_to_responses"`
	ShouldDropParams       bool `json:"should_drop_params"`
	ShouldConvertParams    bool `json:"should_convert_params"`
}

// IsEnabled returns true if any compat feature is enabled
func (c Config) IsEnabled() bool {
	return c.ConvertTextToChat || c.ConvertChatToResponses || c.ShouldDropParams || c.ShouldConvertParams
}

// CompatPlugin provides LiteLLM-compatible request/response transformations.
// When enabled, it automatically converts text completion requests to chat
// completion requests for models that only support chat completions, matching
// LiteLLM's behavior. It also converts chat completion requests to responses
// for models that only support the responses endpoint.
type CompatPlugin struct {
	config        Config
	logger        schemas.Logger
	modelCatalog  *modelcatalog.ModelCatalog
	droppedParams []string
}

// Init creates a new compat plugin instance with model catalog support.
// The model catalog is used to determine if a model supports text completion or
// chat completion natively. If the model catalog is nil, the plugin will
// convert all text completion requests to chat completion and all chat
// completion requests to responses.
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

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged.
func (p *CompatPlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// PreLLMHook intercepts requests and applies LiteLLM-compatible request normalization.
func (p *CompatPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if ctx == nil || req == nil {
		return req, nil, nil
	}

	convertTextToChatOverride, convertTextToChatOverrideEnabled := ctx.Value(schemas.BifrostContextKeyCompatConvertTextToChat).(bool)
	convertChatToResponsesOverride, convertChatToResponsesOverrideEnabled := ctx.Value(schemas.BifrostContextKeyCompatConvertChatToResponses).(bool)
	shouldDropParamsOverride, shouldDropParamsOverrideEnabled := ctx.Value(schemas.BifrostContextKeyCompatShouldDropParams).(bool)
	shouldConvertParamsOverride, shouldConvertParamsOverrideEnabled := ctx.Value(schemas.BifrostContextKeyCompatShouldConvertParams).(bool)

	modifiedReq := req
	if (shouldDropParamsOverrideEnabled && shouldDropParamsOverride) || (shouldConvertParamsOverrideEnabled && shouldDropParamsOverride) || p.config.ShouldConvertParams || p.config.ShouldDropParams {
		modifiedReq = cloneBifrostReq(req)
	}
	p.droppedParams = nil

	// Text completion → chat conversion
	if (convertTextToChatOverrideEnabled && convertTextToChatOverride) || p.config.ConvertTextToChat {
		if (modifiedReq.RequestType == schemas.TextCompletionRequest || modifiedReq.RequestType == schemas.TextCompletionStreamRequest) && modifiedReq.TextCompletionRequest != nil {
			p.markForConversion(ctx, modifiedReq.TextCompletionRequest.Provider, modifiedReq.TextCompletionRequest.Model, schemas.TextCompletionRequest, schemas.ChatCompletionRequest)
		}
	}

	// Chat completion → responses conversion
	if (convertChatToResponsesOverrideEnabled && convertChatToResponsesOverride) || p.config.ConvertChatToResponses {
		if (modifiedReq.RequestType == schemas.ChatCompletionRequest || modifiedReq.RequestType == schemas.ChatCompletionStreamRequest) && modifiedReq.ChatRequest != nil {
			p.markForConversion(ctx, modifiedReq.ChatRequest.Provider, modifiedReq.ChatRequest.Model, schemas.ChatCompletionRequest, schemas.ResponsesRequest)
		}
	}

	// Compute unsupported parameters to drop based on model catalog allowlist
	if ((shouldDropParamsOverrideEnabled && shouldDropParamsOverride) || p.config.ShouldDropParams) && p.modelCatalog != nil {
		_, model, _ := modifiedReq.GetRequestFields()
		if model != "" {
			if supportedParams := p.modelCatalog.GetSupportedParameters(model); supportedParams != nil {
				droppedParams := dropUnsupportedParams(modifiedReq, supportedParams)
				if len(droppedParams) > 0 {
					p.droppedParams = droppedParams
				}
			}
		}
	}

	if (shouldConvertParamsOverride && shouldConvertParamsOverrideEnabled) || p.config.ShouldConvertParams {
		applyParameterConversion(modifiedReq)
	}

	return modifiedReq, nil, nil
}

// PostLLMHook converts provider responses back to the caller-facing shape
func (p *CompatPlugin) PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		return result, bifrostErr, nil
	}

	if changeType, ok := ctx.Value(schemas.BifrostContextKeyChangeRequestType).(schemas.RequestType); ok {
		if result != nil {
			extraFields := result.GetExtraFields()
			if extraFields != nil {
				extraFields.ConvertedRequestType = changeType
			}
		}
		if bifrostErr != nil {
			bifrostErr.ExtraFields.ConvertedRequestType = changeType
		}
	}

	if result != nil {
		if extraFields := result.GetExtraFields(); extraFields != nil {
			extraFields.DroppedCompatPluginParams = p.droppedParams
		}
	}

	return result, bifrostErr, nil
}

// Cleanup performs plugin cleanup.
func (p *CompatPlugin) Cleanup() error {
	return nil
}

// markForConversion checks if the model supports the current request type; if not, mark for conversion
func (p *CompatPlugin) markForConversion(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, currentType schemas.RequestType, targetType schemas.RequestType) {
	shouldConvert := false
	if p.modelCatalog != nil {
		if !p.modelCatalog.IsRequestTypeSupported(model, provider, currentType) && p.modelCatalog.IsRequestTypeSupported(model, provider, targetType) {
			shouldConvert = true
		}
	} else {
		p.logger.Debug("compat: model calalog is nil")
	}

	if shouldConvert {
		ctx.SetValue(schemas.BifrostContextKeyChangeRequestType, targetType)
	}
}
