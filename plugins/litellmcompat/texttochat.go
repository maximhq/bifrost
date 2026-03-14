package litellmcompat

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const (
	OriginalRequestTypeContextKey schemas.BifrostContextKey = "litellmcompat-original-request-type"
	OriginalModelContextKey       schemas.BifrostContextKey = "litellmcompat-original-model"
)

// transformTextToChatRequest determines whether a text request should be converted by core.
// It stores conversion intent in context; core performs the actual conversion.
func transformTextToChatRequest(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, mc *modelcatalog.ModelCatalog, logger schemas.Logger) *schemas.BifrostRequest {
	// Only process text completion requests
	if req.RequestType != schemas.TextCompletionRequest && req.RequestType != schemas.TextCompletionStreamRequest {
		return req
	}

	// Check if text completion request is present
	if req.TextCompletionRequest == nil {
		return req
	}

	// Check if the model supports text completion via model catalog
	if mc != nil {
		provider := req.TextCompletionRequest.Provider
		model := req.TextCompletionRequest.Model
		if mc.IsTextCompletionSupported(model, provider) {
			if ctx != nil {
				ctx.SetValue(schemas.BifrostContextKeyShouldConvertTextToChat, false)
			}
			if logger != nil {
				logger.Debug("litellmcompat: model %s/%s supports text completion, skipping conversion", provider, model)
			}
			return req
		}
	}

	// Track conversion intent. Core will do the actual conversion during provider dispatch.
	if ctx != nil {
		ctx.SetValue(schemas.BifrostContextKeyShouldConvertTextToChat, true)
		ctx.SetValue(OriginalRequestTypeContextKey, req.RequestType)
		ctx.SetValue(OriginalModelContextKey, req.TextCompletionRequest.Model)
	}

	if logger != nil {
		logger.Debug("litellmcompat: marked text completion for core text->chat conversion for model %s (text completion not supported)", req.TextCompletionRequest.Model)
	}

	return req
}

func getOriginalTextRequestMetadata(ctx *schemas.BifrostContext) (schemas.RequestType, string) {
	requestType := schemas.TextCompletionRequest
	if ctx == nil {
		return requestType, ""
	}
	if value, ok := ctx.Value(OriginalRequestTypeContextKey).(schemas.RequestType); ok {
		requestType = value
	}
	model, _ := ctx.Value(OriginalModelContextKey).(string)
	return requestType, model
}

// transformTextToChatResponse normalizes metadata on converted text-completion responses.
// Core performs the actual stream/non-stream payload conversion.
func transformTextToChatResponse(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, logger schemas.Logger) *schemas.BifrostResponse {
	if resp == nil || resp.TextCompletionResponse == nil || ctx == nil {
		return resp
	}

	shouldConvert, ok := ctx.Value(schemas.BifrostContextKeyShouldConvertTextToChat).(bool)
	if !ok || !shouldConvert {
		return resp
	}

	originalRequestType, originalModel := getOriginalTextRequestMetadata(ctx)
	resp.TextCompletionResponse.ExtraFields.RequestType = originalRequestType
	resp.TextCompletionResponse.ExtraFields.ModelRequested = originalModel
	resp.TextCompletionResponse.ExtraFields.LiteLLMCompat = true

	if logger != nil {
		logger.Debug("litellmcompat: normalized converted text completion metadata for model %s", originalModel)
	}

	return resp
}

// transformTextToChatError restores original text-completion metadata on errors
// generated from chat fallback execution.
func transformTextToChatError(ctx *schemas.BifrostContext, err *schemas.BifrostError) *schemas.BifrostError {
	if err == nil || ctx == nil {
		return err
	}
	shouldConvert, ok := ctx.Value(schemas.BifrostContextKeyShouldConvertTextToChat).(bool)
	if !ok || !shouldConvert {
		return err
	}

	requestType := err.ExtraFields.RequestType
	if requestType != schemas.ChatCompletionRequest && requestType != schemas.ChatCompletionStreamRequest {
		return err
	}

	originalRequestType, originalModel := getOriginalTextRequestMetadata(ctx)
	err.ExtraFields.RequestType = originalRequestType
	err.ExtraFields.ModelRequested = originalModel
	err.ExtraFields.LiteLLMCompat = true
	return err
}
