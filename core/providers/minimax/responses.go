package minimax

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const defaultResponsesPath = "/v1/responses"

// Responses performs a non-streaming request against MiniMax's native
// OpenAI-compatible Responses endpoint.
func (provider *MiniMaxProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.MiniMax, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}
	if err := validateMiniMaxResponsesRequest(request); err != nil {
		return nil, err
	}
	if err := rejectMiniMaxLargePayload(ctx); err != nil {
		return nil, err
	}

	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, defaultResponsesPath, schemas.ResponsesRequest),
		request,
		provider.authHeaders(key),
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		handleMiniMaxOpenAIResponse[schemas.BifrostResponsesResponse],
		parseMiniMaxHTTPError,
		nil,
		provider.logger,
	)
}

// ResponsesStream performs a streaming request against MiniMax's native
// OpenAI-compatible Responses endpoint.
func (provider *MiniMaxProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.MiniMax, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}
	if err := validateMiniMaxResponsesRequest(request); err != nil {
		return nil, err
	}
	if err := rejectMiniMaxLargePayload(ctx); err != nil {
		return nil, err
	}

	return openai.HandleOpenAIResponsesStreaming(
		ctx,
		provider.streamingClient,
		provider.buildRequestURL(ctx, defaultResponsesPath, schemas.ResponsesStreamRequest),
		request,
		provider.authHeaders(key),
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		handleMiniMaxOpenAIResponse[schemas.BifrostResponsesStreamResponse],
		parseMiniMaxHTTPError,
		nil,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

func validateMiniMaxResponsesRequest(request *schemas.BifrostResponsesRequest) *schemas.BifrostError {
	if request == nil || request.Params == nil {
		return nil
	}
	if request.Params.ToolChoice != nil && request.Params.ToolChoice.IsForced() {
		return providerUtils.NewBifrostBadRequestError(fmt.Sprintf("MiniMax Responses API supports only tool_choice values %q and %q", schemas.ResponsesToolChoiceTypeNone, schemas.ResponsesToolChoiceTypeAuto))
	}
	if request.Params.Reasoning != nil && request.Params.Reasoning.Effort != nil {
		switch *request.Params.Reasoning.Effort {
		case schemas.ReasoningEffortNone, schemas.ReasoningEffortMinimal, schemas.ReasoningEffortLow, schemas.ReasoningEffortMedium, schemas.ReasoningEffortHigh:
		default:
			return providerUtils.NewBifrostBadRequestError(fmt.Sprintf("unsupported MiniMax reasoning effort %q", *request.Params.Reasoning.Effort))
		}
	}
	return nil
}
