package litellmcompat

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const (
	ChatToResponsesOriginalRequestTypeContextKey schemas.BifrostContextKey = "litellmcompat-chat-to-responses-original-request-type"
	ChatToResponsesOriginalModelContextKey       schemas.BifrostContextKey = "litellmcompat-chat-to-responses-original-model"
)

// transformChatToResponsesRequest determines whether a chat request should be converted
// to a responses request by core. It stores conversion intent in context; core performs
// the actual conversion.
func transformChatToResponsesRequest(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, mc *modelcatalog.ModelCatalog, logger schemas.Logger) *schemas.BifrostRequest {
	// Only process chat completion requests
	if req.RequestType != schemas.ChatCompletionRequest && req.RequestType != schemas.ChatCompletionStreamRequest {
		return req
	}

	// Check if chat completion request is present
	if req.ChatRequest == nil {
		return req
	}

	// Need model catalog to determine endpoint support
	if mc == nil {
		return req
	}

	provider := req.ChatRequest.Provider
	model := req.ChatRequest.Model

	// If the model supports chat completion natively, no conversion needed
	if mc.IsChatCompletionSupported(model, provider) {
		return req
	}

	// Check if the model supports responses as a fallback
	if !mc.IsResponsesSupported(model, provider) {
		if logger != nil {
			logger.Debug("litellmcompat: model %s/%s supports neither chat nor responses, skipping conversion", provider, model)
		}
		return req
	}

	// Track conversion intent. Core will do the actual conversion during provider dispatch.
	if ctx != nil {
		ctx.SetValue(schemas.BifrostContextKeyShouldConvertChatToResponses, true)
		ctx.SetValue(ChatToResponsesOriginalRequestTypeContextKey, req.RequestType)
		ctx.SetValue(ChatToResponsesOriginalModelContextKey, req.ChatRequest.Model)
	}

	if logger != nil {
		logger.Debug("litellmcompat: marked chat completion for core chat->responses conversion for model %s (chat completion not supported, responses supported)", model)
	}

	return req
}

func getOriginalChatRequestMetadata(ctx *schemas.BifrostContext) (schemas.RequestType, string) {
	requestType := schemas.ChatCompletionRequest
	if ctx == nil {
		return requestType, ""
	}
	if value, ok := ctx.Value(ChatToResponsesOriginalRequestTypeContextKey).(schemas.RequestType); ok {
		requestType = value
	}
	model, _ := ctx.Value(ChatToResponsesOriginalModelContextKey).(string)
	return requestType, model
}

// transformChatToResponsesResponse normalizes metadata on converted chat-completion responses.
// Core performs the actual stream/non-stream payload conversion.
func transformChatToResponsesResponse(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, logger schemas.Logger) *schemas.BifrostResponse {
	if resp == nil || resp.ChatResponse == nil || ctx == nil {
		return resp
	}

	shouldConvert, ok := ctx.Value(schemas.BifrostContextKeyShouldConvertChatToResponses).(bool)
	if !ok || !shouldConvert {
		return resp
	}

	originalRequestType, originalModel := getOriginalChatRequestMetadata(ctx)
	resp.ChatResponse.ExtraFields.RequestType = originalRequestType
	resp.ChatResponse.ExtraFields.ModelRequested = originalModel
	resp.ChatResponse.ExtraFields.LiteLLMCompat = true

	if logger != nil {
		logger.Debug("litellmcompat: normalized converted chat completion metadata for model %s", originalModel)
	}

	return resp
}

// transformChatToResponsesError restores original chat-completion metadata on errors
// generated from responses fallback execution.
func transformChatToResponsesError(ctx *schemas.BifrostContext, err *schemas.BifrostError) *schemas.BifrostError {
	if err == nil || ctx == nil {
		return err
	}
	shouldConvert, ok := ctx.Value(schemas.BifrostContextKeyShouldConvertChatToResponses).(bool)
	if !ok || !shouldConvert {
		return err
	}

	requestType := err.ExtraFields.RequestType
	if requestType != schemas.ResponsesRequest && requestType != schemas.ResponsesStreamRequest {
		return err
	}

	originalRequestType, originalModel := getOriginalChatRequestMetadata(ctx)
	err.ExtraFields.RequestType = originalRequestType
	err.ExtraFields.ModelRequested = originalModel
	err.ExtraFields.LiteLLMCompat = true
	return err
}
