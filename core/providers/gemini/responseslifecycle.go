package gemini

import (
	"context"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// The Responses lifecycle verbs map onto stored interactions: a response id Bifrost returns for a
// Gemini run is the interaction id, so retrieve, cancel and delete address /interactions/{id}.
// Pin key selection on these calls (x-bf-api-key-id) when several keys are configured, so they
// reach the account that created the run.

// interactionsTarget is the Gemini API's Interactions endpoint for key.
func (provider *GeminiProvider) interactionsTarget(ctx *schemas.BifrostContext, key schemas.Key) *GeminiInteractionsTarget {
	headers := map[string]string{}
	if apiKey := key.Value.GetValue(); apiKey != "" {
		headers["x-goog-api-key"] = apiKey
	}
	return &GeminiInteractionsTarget{
		CollectionURL:              provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, geminiInteractionsPath),
		Headers:                    headers,
		ExtraHeaders:               provider.networkConfig.ExtraHeaders,
		Client:                     provider.client,
		StreamingClient:            provider.streamingClient,
		StreamIdleTimeoutInSeconds: provider.networkConfig.StreamIdleTimeoutInSeconds,
		ProviderName:               provider.GetProviderKey(),
		SendBackRawRequest:         providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		SendBackRawResponse:        providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		Logger:                     provider.logger,
	}
}

// ResponsesRetrieve implements schemas.ResponsesLifecycleProvider (GET /interactions/{id}).
func (provider *GeminiProvider) ResponsesRetrieve(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesRetrieveRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ResponsesRetrieveRequest); err != nil {
		return nil, err
	}
	return InteractionsRetrieve(ctx, provider.interactionsTarget(ctx, key), req)
}

// ResponsesRetrieveStream implements schemas.ResponsesLifecycleProvider (GET
// /interactions/{id}?stream=true).
func (provider *GeminiProvider) ResponsesRetrieveStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, req *schemas.BifrostResponsesRetrieveRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ResponsesRetrieveStreamRequest); err != nil {
		return nil, err
	}
	return InteractionsRetrieveStream(ctx, provider.interactionsTarget(ctx, key), postHookRunner, postHookSpanFinalizer, req)
}

// ResponsesDelete implements schemas.ResponsesLifecycleProvider (DELETE /interactions/{id}).
func (provider *GeminiProvider) ResponsesDelete(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesDeleteRequest) (*schemas.BifrostResponsesDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ResponsesDeleteRequest); err != nil {
		return nil, err
	}
	return InteractionsDelete(ctx, provider.interactionsTarget(ctx, key), req)
}

// ResponsesCancel implements schemas.ResponsesLifecycleProvider (POST /interactions/{id}/cancel).
func (provider *GeminiProvider) ResponsesCancel(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesCancelRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ResponsesCancelRequest); err != nil {
		return nil, err
	}
	return InteractionsCancel(ctx, provider.interactionsTarget(ctx, key), req)
}

// ResponsesInputItems implements schemas.ResponsesLifecycleProvider from the user_input and
// function_result steps of GET /interactions/{id}.
func (provider *GeminiProvider) ResponsesInputItems(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesInputItemsRequest) (*schemas.BifrostResponsesInputItemsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ResponsesInputItemsRequest); err != nil {
		return nil, err
	}
	return InteractionsInputItems(ctx, provider.interactionsTarget(ctx, key), req)
}
