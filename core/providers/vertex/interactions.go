package vertex

import (
	"context"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Vertex serves the Interactions API at
// https://{host}/v1beta1/projects/{project}/locations/{region}/interactions: v1 answers 404, and an
// API key is refused, so the key's service-account credentials mint an OAuth token. Google
// documents only the global location today; Bifrost sends the key's own region (global when none
// is set), so a regional key gets Google's answer for that region rather than a silent reroute.

const vertexInteractionsAPIVersion = "v1beta1"

// useVertexInteractions reports whether a Responses request runs on Interactions: the shared rule,
// limited to the models Vertex serves there (Gemini, and the agents).
func useVertexInteractions(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) bool {
	if !gemini.ShouldUseGeminiInteractions(ctx, request) {
		return false
	}
	return gemini.IsGeminiInteractionsAgent(request.Model) || schemas.IsGeminiModelFamily(ctx, request.Model)
}

// interactionsTarget is the Vertex Interactions endpoint for key, authenticated with an OAuth token
// from the key's credentials.
func (provider *VertexProvider) interactionsTarget(ctx *schemas.BifrostContext, key schemas.Key) (*gemini.GeminiInteractionsTarget, *schemas.BifrostError) {
	projectID := strings.TrimSpace(resolveVertexProjectID(ctx, key))
	if projectID == "" {
		return nil, providerUtils.NewConfigurationError("project ID is not set")
	}
	target := &gemini.GeminiInteractionsTarget{
		CollectionURL:              vertexInteractionsCollectionURL(resolveVertexRegion(ctx, key), projectID),
		Headers:                    map[string]string{},
		ExtraHeaders:               provider.networkConfig.ExtraHeaders,
		Client:                     provider.client,
		StreamingClient:            provider.streamingClient,
		StreamIdleTimeoutInSeconds: provider.networkConfig.StreamIdleTimeoutInSeconds,
		ProviderName:               provider.GetProviderKey(),
		SendBackRawRequest:         providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		SendBackRawResponse:        providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		Logger:                     provider.logger,
		PollAPIVersion:             vertexInteractionsAPIVersion,
	}
	tokenSource, err := provider.getAuthTokenSource(key)
	if err != nil {
		provider.removeVertexClient(key)
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
	}
	token, err := tokenSource.Token()
	if err != nil {
		provider.removeVertexClient(key)
		return nil, providerUtils.NewBifrostOperationError("error getting token", err)
	}
	target.Headers["Authorization"] = "Bearer " + token.AccessToken
	return target, nil
}

// evictOnInteractionsAuthError drops the key's cached token source when Google refused the call
// with 401 or 403, as the generateContent paths do, so the next call mints a fresh token instead of
// reusing the rejected one. It returns bifrostErr unchanged.
func (provider *VertexProvider) evictOnInteractionsAuthError(key schemas.Key, bifrostErr *schemas.BifrostError) *schemas.BifrostError {
	if bifrostErr != nil && bifrostErr.StatusCode != nil && (*bifrostErr.StatusCode == fasthttp.StatusUnauthorized || *bifrostErr.StatusCode == fasthttp.StatusForbidden) {
		provider.removeVertexClient(key)
	}
	return bifrostErr
}

// vertexInteractionsCollectionURL is the Interactions collection for a project and region (global
// when the key names none).
func vertexInteractionsCollectionURL(region, projectID string) string {
	if region == "" {
		region = "global"
	}
	return fmt.Sprintf("%s/interactions", getVertexProjectLocationURL(region, vertexInteractionsAPIVersion, projectID))
}

func (provider *VertexProvider) interactionsResponses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	target, bifrostErr := provider.interactionsTarget(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	resp, bifrostErr := gemini.InteractionsResponses(ctx, target, request)
	return resp, provider.evictOnInteractionsAuthError(key, bifrostErr)
}

func (provider *VertexProvider) interactionsResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	target, bifrostErr := provider.interactionsTarget(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	stream, bifrostErr := gemini.InteractionsResponsesStream(ctx, target, postHookRunner, postHookSpanFinalizer, request)
	return stream, provider.evictOnInteractionsAuthError(key, bifrostErr)
}

// ResponsesRetrieve implements schemas.ResponsesLifecycleProvider (GET /interactions/{id}).
func (provider *VertexProvider) ResponsesRetrieve(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesRetrieveRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	target, bifrostErr := provider.interactionsTarget(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	resp, bifrostErr := gemini.InteractionsRetrieve(ctx, target, req)
	return resp, provider.evictOnInteractionsAuthError(key, bifrostErr)
}

// ResponsesRetrieveStream implements schemas.ResponsesLifecycleProvider (GET
// /interactions/{id}?stream=true).
func (provider *VertexProvider) ResponsesRetrieveStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, req *schemas.BifrostResponsesRetrieveRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	target, bifrostErr := provider.interactionsTarget(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	stream, bifrostErr := gemini.InteractionsRetrieveStream(ctx, target, postHookRunner, postHookSpanFinalizer, req)
	return stream, provider.evictOnInteractionsAuthError(key, bifrostErr)
}

// ResponsesDelete implements schemas.ResponsesLifecycleProvider (DELETE /interactions/{id}).
func (provider *VertexProvider) ResponsesDelete(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesDeleteRequest) (*schemas.BifrostResponsesDeleteResponse, *schemas.BifrostError) {
	target, bifrostErr := provider.interactionsTarget(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	resp, bifrostErr := gemini.InteractionsDelete(ctx, target, req)
	return resp, provider.evictOnInteractionsAuthError(key, bifrostErr)
}

// ResponsesCancel implements schemas.ResponsesLifecycleProvider (POST /interactions/{id}/cancel).
func (provider *VertexProvider) ResponsesCancel(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesCancelRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	target, bifrostErr := provider.interactionsTarget(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	resp, bifrostErr := gemini.InteractionsCancel(ctx, target, req)
	return resp, provider.evictOnInteractionsAuthError(key, bifrostErr)
}

// ResponsesInputItems implements schemas.ResponsesLifecycleProvider from the input steps of GET
// /interactions/{id}.
func (provider *VertexProvider) ResponsesInputItems(ctx *schemas.BifrostContext, key schemas.Key, req *schemas.BifrostResponsesInputItemsRequest) (*schemas.BifrostResponsesInputItemsResponse, *schemas.BifrostError) {
	target, bifrostErr := provider.interactionsTarget(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	resp, bifrostErr := gemini.InteractionsInputItems(ctx, target, req)
	return resp, provider.evictOnInteractionsAuthError(key, bifrostErr)
}
