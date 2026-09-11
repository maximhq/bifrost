package bedrock

import (
	"context"
	"fmt"

	openai "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// runtimeOpenAIURL builds the bedrock-runtime OpenAI-compatible endpoint URL for the given
// region and API path (e.g. "responses"). Only the frontier families reach this surface, and
// they all live under "openai/v1"; gpt-oss carries a bare id and so routes to mantle instead.
// Unlike the Converse paths the model is not part of the URL — it rides in the body.
func runtimeOpenAIURL(endpoints *schemas.BedrockEndpoints, region, path string) string {
	return fmt.Sprintf("https://%s/openai/v1/%s", resolveBedrockHost(endpoints, bedrockServiceRuntime, region), path)
}

// runtimeResponses handles non-streaming Responses requests on bedrock-runtime's
// OpenAI-compatible surface. Payloads and SSE follow the OpenAI Responses spec, so the shared
// OpenAI handler does the work and only the URL and SigV4 scope differ from mantle.
func (provider *BedrockProvider) runtimeResponses(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := runtimeOpenAIURL(bedrockEndpoints(key.BedrockKeyConfig), region, "responses")
	_, request.Model = parseBedrockRegionAndModel(request.Model)

	// SigV4 (empty key value): sign the exact body the handler builds via a signer closure.
	// Bearer (key has a value): no signer; auth flows through the Authorization header.
	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return signOpenAIV4Headers(ctx, body, url, "application/json", key, region, provider.networkConfig.ExtraHeaders, bedrockSigningService)
		}
	}

	// bedrock-runtime is not project-scoped, so no project header is sent here.
	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		signer,
		provider.logger,
	)
}

// runtimeResponsesStream handles streaming Responses requests on bedrock-runtime's
// OpenAI-compatible surface.
func (provider *BedrockProvider) runtimeResponsesStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := runtimeOpenAIURL(bedrockEndpoints(key.BedrockKeyConfig), region, "responses")
	_, request.Model = parseBedrockRegionAndModel(request.Model)

	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return signOpenAIV4Headers(ctx, body, url, "text/event-stream", key, region, provider.networkConfig.ExtraHeaders, bedrockSigningService)
		}
	}

	return openai.HandleOpenAIResponsesStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		openai.BearerAuthHeader(key), provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner,
		nil,
		nil,
		nil,
		nil,
		signer,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// runtimeChatCompletions handles non-streaming chat requests on bedrock-runtime's
// OpenAI-compatible surface. Reached only when the operator opts in.
func (provider *BedrockProvider) runtimeChatCompletions(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := runtimeOpenAIURL(bedrockEndpoints(key.BedrockKeyConfig), region, "chat/completions")
	_, request.Model = parseBedrockRegionAndModel(request.Model)

	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return signOpenAIV4Headers(ctx, body, url, "application/json", key, region, provider.networkConfig.ExtraHeaders, bedrockSigningService)
		}
	}

	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		signer,
		provider.logger,
	)
}

// runtimeChatCompletionsStream handles streaming chat requests on bedrock-runtime's
// OpenAI-compatible surface.
func (provider *BedrockProvider) runtimeChatCompletionsStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := runtimeOpenAIURL(bedrockEndpoints(key.BedrockKeyConfig), region, "chat/completions")
	_, request.Model = parseBedrockRegionAndModel(request.Model)

	var signer providerUtils.BodySigner
	if key.Value.GetValue() == "" {
		signer = func(body []byte) (map[string]string, *schemas.BifrostError) {
			return signOpenAIV4Headers(ctx, body, url, "text/event-stream", key, region, provider.networkConfig.ExtraHeaders, bedrockSigningService)
		}
	}

	return openai.HandleOpenAIChatCompletionStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		openai.BearerAuthHeader(key), provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner,
		nil,
		nil,
		nil,
		nil,
		nil,
		signer,
		provider.logger,
		postHookSpanFinalizer,
	)
}
