package opencode

import (
	"context"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func (p *opencodeProvider) executeOpenAIChat(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		p.client,
		p.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, route.Path),
		request,
		key,
		p.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.GetProviderKey(),
		nil,
		parseOpencodeError,
		p.logger,
	)
}

func (p *opencodeProvider) executeOpenAIChatStream(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	var authHeader map[string]string
	if v := key.Value.GetValue(); v != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + v}
	}
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		p.streamingClient,
		p.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, route.Path),
		request,
		authHeader,
		p.networkConfig.ExtraHeaders,
		p.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.providerKey,
		postHookRunner,
		nil,
		nil,
		parseOpencodeError,
		nil,
		nil,
		p.logger,
		postHookSpanFinalizer,
	)
}

func (p *opencodeProvider) executeOpenAIResponses(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return openai.HandleOpenAIResponsesRequest(
		ctx,
		p.client,
		p.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, route.Path),
		request,
		key,
		p.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.GetProviderKey(),
		nil,
		parseOpencodeError,
		p.logger,
	)
}

func (p *opencodeProvider) executeOpenAIResponsesStream(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	var authHeader map[string]string
	if v := key.Value.GetValue(); v != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + v}
	}
	return openai.HandleOpenAIResponsesStreaming(
		ctx,
		p.streamingClient,
		p.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, route.Path),
		request,
		authHeader,
		p.networkConfig.ExtraHeaders,
		p.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, p.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, p.sendBackRawResponse),
		p.GetProviderKey(),
		postHookRunner,
		nil,
		parseOpencodeError,
		nil,
		nil,
		p.logger,
		postHookSpanFinalizer,
	)
}
