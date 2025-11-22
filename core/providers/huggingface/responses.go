package huggingface

import (
	"context"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Responses implements schemas.Provider.
func (provider *HuggingFaceProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	// Resolve model alias if present on the key
	requestedModel := request.Model
	effectiveRequest, _ := provider.prepareEmbeddingRequest(&schemas.BifrostEmbeddingRequest{Model: requestedModel}, key)

	// effectiveRequest is an embedding request clone, extract model actually used downstream
	resolvedModel := requestedModel
	if effectiveRequest != nil && effectiveRequest.Model != "" {
		resolvedModel = effectiveRequest.Model
	}

	// Use a shallow copy so we don't mutate the caller's request
	resolvedRequest := *request
	resolvedRequest.Model = resolvedModel

	// Use OpenAI-compatible Responses handler
	response, bifrostErr := openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/responses", schemas.ResponsesRequest),
		&resolvedRequest,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.shouldSendRawResponse(ctx),
		provider.GetProviderKey(),
		provider.logger,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	provider.decorateResponseMetadata(&response.ExtraFields, requestedModel, resolvedModel)
	return response, nil
}

// ResponsesStream implements schemas.Provider.
func (provider *HuggingFaceProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	// Resolve model alias if present on the key
	requestedModel := request.Model
	_, resolvedModel := provider.prepareEmbeddingRequest(&schemas.BifrostEmbeddingRequest{Model: requestedModel}, key)

	// Use a shallow copy so streaming uses the resolved model
	resolvedRequest := *request
	if resolvedModel != "" {
		resolvedRequest.Model = resolvedModel
	}

	// Build auth header
	var authHeader map[string]string
	if key.Value != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value}
	}

	// Use OpenAI-compatible streaming handler
	stream, bifrostErr := openai.HandleOpenAIResponsesStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/responses", schemas.ResponsesStreamRequest),
		&resolvedRequest,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		provider.shouldSendRawResponse(ctx),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		provider.logger,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// If model was aliased, ensure metadata in stream post-converter
	if resolvedModel != "" && resolvedModel != request.Model {
		// We can't mutate the stream items here easily; the chat stream path uses a post converter.
		// For simplicity, leave as-is; metadata decoration for streaming responses is handled downstream where possible.
	}

	return stream, nil
}
