package litellmcompat

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// transformResponsesToChatRequest applies the Responses -> Chat compatibility bridge.
// This path is intentionally best-effort rather than fully lossless; keep request shaping
// behind schemas.ToChatFallbackRequest so schema evolution has one compatibility seam.
func transformResponsesToChatRequest(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, logger schemas.Logger) *schemas.BifrostRequest {
	if req.RequestType != schemas.ResponsesRequest && req.RequestType != schemas.ResponsesStreamRequest {
		return req
	}

	if req.ResponsesRequest == nil {
		return req
	}

	metadata, ok := schemas.GetCustomProviderContextMetadata(ctx)
	if !ok || metadata == nil || metadata.BaseProviderType != schemas.OpenAI {
		return req
	}

	if metadata.SupportsResponsesAPI != nil && *metadata.SupportsResponsesAPI {
		return req
	}

	chatRequest := req.ResponsesRequest.ToChatFallbackRequest()
	if chatRequest == nil {
		return req
	}

	fallbackRequestType := schemas.ChatCompletionRequest
	if req.RequestType == schemas.ResponsesStreamRequest {
		fallbackRequestType = schemas.ChatCompletionStreamRequest
	}

	state := &schemas.ResponsesToChatCompletionCompatState{
		OriginalRequestType: req.RequestType,
		OriginalModel:       req.ResponsesRequest.Model,
		IsStreaming:         req.RequestType == schemas.ResponsesStreamRequest,
		FallbackRequest: &schemas.BifrostRequest{
			RequestType: fallbackRequestType,
			ChatRequest: chatRequest,
		},
		Warnings: req.ResponsesRequest.ChatFallbackWarnings(),
	}
	schemas.SetResponsesToChatCompletionCompatState(ctx, state)

	if metadata.SupportsResponsesAPI == nil {
		state.RetryEligible = true
		state.RetryPolicy = schemas.DefaultResponsesToChatCompletionRetryPolicy()
		return req
	}

	if _, activated := schemas.ActivateResponsesToChatCompletionCompatState(ctx, schemas.ResponsesToChatCompletionFallbackReasonConfiguredUnsupported); !activated {
		return req
	}
	logResponsesToChatFallback(logger, state.OriginalModel, state.FallbackReason, state.Warnings)

	return state.FallbackRequest
}

func transformResponsesToChatResponse(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, logger schemas.Logger) *schemas.BifrostResponse {
	state, ok := schemas.GetResponsesToChatCompletionCompatState(ctx)
	if !ok || state == nil || !state.Active || resp == nil || state.IsStreaming || resp.ChatResponse == nil {
		return resp
	}

	responsesResponse := resp.ChatResponse.ToBifrostResponsesResponse()
	if responsesResponse == nil {
		return resp
	}

	responsesResponse.ExtraFields.RequestType = state.OriginalRequestType
	responsesResponse.ExtraFields.ModelRequested = state.OriginalModel
	responsesResponse.ExtraFields.LiteLLMCompat = true

	if logger != nil {
		logger.Debug("litellmcompat: converted chat response back to responses for model %s (reason=%s)", state.OriginalModel, state.FallbackReason)
	}

	return &schemas.BifrostResponse{
		ResponsesResponse: responsesResponse,
	}
}

func transformResponsesToChatError(ctx *schemas.BifrostContext, err *schemas.BifrostError) *schemas.BifrostError {
	state, ok := schemas.GetResponsesToChatCompletionCompatState(ctx)
	if !ok || state == nil || err == nil || !state.Active {
		return err
	}

	err.ExtraFields.RequestType = state.OriginalRequestType
	err.ExtraFields.ModelRequested = state.OriginalModel
	err.ExtraFields.LiteLLMCompat = true

	return err
}

func logResponsesToChatFallback(logger schemas.Logger, model string, reason schemas.ResponsesToChatCompletionFallbackReason, warnings []string) {
	if logger == nil {
		return
	}

	logger.Info("litellmcompat: applied responses->chat completion fallback for model %s (reason=%s)", model, reason)
	if len(warnings) == 0 {
		return
	}

	logger.Warn("litellmcompat: responses->chat completion fallback for model %s is compatibility-only: %s", model, strings.Join(warnings, "; "))
}
