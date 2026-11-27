package vertex

import (
	"context"
	"strings"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// openAIEndpointURL keeps the transport method separate from the served model.
// Only explicitly configured endpoints take this path; existing publisher and
// tuned-Gemini routing is unchanged. No endpoint or credential state is built here.
func openAIEndpointURL(ctx *schemas.BifrostContext, key schemas.Key, endpoint *schemas.VertexEndpoint, stream bool) (string, *schemas.BifrostError) {
	if err := endpoint.Validate(); err != nil {
		return "", providerUtils.NewConfigurationError(err.Error())
	}
	project := resolveVertexProjectID(ctx, key)
	region := resolveVertexRegion(ctx, key)
	if project == "" || region == "" {
		return "", providerUtils.NewConfigurationError("project ID and region are required for a Vertex custom endpoint")
	}
	if !vertexRegionRe.MatchString(region) || region == "global" || region == "us" || region == "eu" {
		return "", providerUtils.NewConfigurationError("a Vertex custom endpoint requires a regional location such as us-west1")
	}
	project, err := providerUtils.EscapeResourceID(project, "project_id")
	if err != nil {
		return "", err
	}
	if endpoint.Method == schemas.VertexEndpointChatCompletions {
		return getVertexEndpointURL(region, "v1beta1", project, endpoint.EndpointID+"/chat/completions", ""), nil
	}
	suffix := ":rawPredict"
	if stream {
		suffix = ":streamRawPredict"
	}
	return getVertexEndpointURL(region, "v1", project, endpoint.EndpointID, suffix), nil
}

func (provider *VertexProvider) openAIEndpointAuth(key schemas.Key) (map[string]string, *schemas.BifrostError) {
	if key.VertexKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("vertex key config is required")
	}
	source, err := provider.getAuthTokenSource(key)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating auth token source", err)
	}
	token, err := source.Token()
	if err != nil {
		provider.removeVertexClient(key)
		return nil, providerUtils.NewBifrostOperationError("error getting token", err)
	}
	return map[string]string{"Authorization": "Bearer " + token.AccessToken}, nil
}

// openAIEndpointError handles errors from both the Vertex front door and the
// model container, preserving the existing credential eviction on 401/403.
func (provider *VertexProvider) openAIEndpointError(key schemas.Key) openai.ErrorConverter {
	return func(resp *fasthttp.Response) *schemas.BifrostError {
		if resp.StatusCode() == fasthttp.StatusUnauthorized || resp.StatusCode() == fasthttp.StatusForbidden {
			provider.removeVertexClient(key)
		}
		return parseVertexError(resp)
	}
}

func (provider *VertexProvider) openAIChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest, endpoint *schemas.VertexEndpoint) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	url, err := openAIEndpointURL(ctx, key, endpoint, false)
	if err != nil {
		return nil, err
	}
	headers, err := provider.openAIEndpointAuth(key)
	if err != nil {
		return nil, err
	}
	return openai.HandleOpenAIChatCompletionRequest(
		ctx, provider.client, url, request, headers, provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), nil, provider.openAIEndpointError(key), nil, provider.logger,
	)
}

func (provider *VertexProvider) openAIChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest, endpoint *schemas.VertexEndpoint) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	url, err := openAIEndpointURL(ctx, key, endpoint, true)
	if err != nil {
		return nil, err
	}
	headers, err := provider.openAIEndpointAuth(key)
	if err != nil {
		return nil, err
	}
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx, provider.streamingClient, url, request, headers, provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner, nil, nil, provider.openAIEndpointError(key), nil, nil, nil,
		provider.logger, postHookSpanFinalizer,
	)
}

// isOpenAIPublisherModel reports whether model is served by the openai publisher's
// Responses endpoint (GPT-6 and later). gpt-oss stays on the openapi MaaS endpoint.
func isOpenAIPublisherModel(ctx *schemas.BifrostContext, model string) bool {
	return schemas.IsOpenAIModelFamily(ctx, model) &&
		!strings.Contains(strings.ToLower(schemas.ResolveCanonicalModel(ctx, model)), "gpt-oss")
}

// openAIPublisherResponsesURL is the openai publisher's native Responses endpoint.
func openAIPublisherResponsesURL(ctx *schemas.BifrostContext, key schemas.Key) (string, *schemas.BifrostError) {
	projectID := resolveVertexProjectID(ctx, key)
	if projectID == "" {
		return "", providerUtils.NewConfigurationError("project ID is not set")
	}
	region := resolveVertexRegion(ctx, key)
	if region == "" {
		return "", providerUtils.NewConfigurationError("region is not set in key config")
	}
	return getVertexProjectLocationURL(region, "v1", projectID) + "/publishers/openai/v1/responses", nil
}

// withBareOpenAIModel strips an "openai/" prefix, since the endpoint takes the bare model
// name. The caller's request is copied rather than changed.
func withBareOpenAIModel(request *schemas.BifrostResponsesRequest) *schemas.BifrostResponsesRequest {
	model, ok := strings.CutPrefix(request.Model, "openai/")
	if !ok {
		return request
	}
	bare := *request
	bare.Model = model
	return &bare
}

func (provider *VertexProvider) openAIPublisherResponses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	url, err := openAIPublisherResponsesURL(ctx, key)
	if err != nil {
		return nil, err
	}
	headers, err := provider.openAIEndpointAuth(key)
	if err != nil {
		return nil, err
	}
	return openai.HandleOpenAIResponsesRequest(
		ctx, provider.client, url, withBareOpenAIModel(request), headers, provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), nil, provider.openAIEndpointError(key), nil, provider.logger,
	)
}

func (provider *VertexProvider) openAIPublisherResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	url, err := openAIPublisherResponsesURL(ctx, key)
	if err != nil {
		return nil, err
	}
	headers, err := provider.openAIEndpointAuth(key)
	if err != nil {
		return nil, err
	}
	return openai.HandleOpenAIResponsesStreaming(
		ctx, provider.streamingClient, url, withBareOpenAIModel(request), headers, provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner, nil, provider.openAIEndpointError(key), nil, nil, nil,
		provider.logger, postHookSpanFinalizer,
	)
}
