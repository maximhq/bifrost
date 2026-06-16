package opencode

import (
	"context"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func (p *opencodeProvider) executeAnthropicChat(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	provider := anthropic.NewAnthropicProvider(&schemas.ProviderConfig{
		NetworkConfig:       p.networkConfig,
		SendBackRawRequest:  p.sendBackRawRequest,
		SendBackRawResponse: p.sendBackRawResponse,
	}, p.logger)
	provider = setOpencodeAnthropicBaseURL(provider, p.networkConfig.BaseURL)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, route.Path)
	return provider.ChatCompletion(ctx, key, request)
}

func (p *opencodeProvider) executeAnthropicChatStream(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	provider := anthropic.NewAnthropicProvider(&schemas.ProviderConfig{
		NetworkConfig:       p.networkConfig,
		SendBackRawRequest:  p.sendBackRawRequest,
		SendBackRawResponse: p.sendBackRawResponse,
	}, p.logger)
	provider = setOpencodeAnthropicBaseURL(provider, p.networkConfig.BaseURL)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, route.Path)
	return provider.ChatCompletionStream(ctx, postHookRunner, postHookSpanFinalizer, key, request)
}

func (p *opencodeProvider) executeAnthropicResponses(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	provider := anthropic.NewAnthropicProvider(&schemas.ProviderConfig{
		NetworkConfig:       p.networkConfig,
		SendBackRawRequest:  p.sendBackRawRequest,
		SendBackRawResponse: p.sendBackRawResponse,
	}, p.logger)
	provider = setOpencodeAnthropicBaseURL(provider, p.networkConfig.BaseURL)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, route.Path)
	return provider.Responses(ctx, key, request)
}

func (p *opencodeProvider) executeAnthropicResponsesStream(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	provider := anthropic.NewAnthropicProvider(&schemas.ProviderConfig{
		NetworkConfig:       p.networkConfig,
		SendBackRawRequest:  p.sendBackRawRequest,
		SendBackRawResponse: p.sendBackRawResponse,
	}, p.logger)
	provider = setOpencodeAnthropicBaseURL(provider, p.networkConfig.BaseURL)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, route.Path)
	return provider.ResponsesStream(ctx, postHookRunner, postHookSpanFinalizer, key, request)
}

func setOpencodeAnthropicBaseURL(provider *anthropic.AnthropicProvider, baseURL string) *anthropic.AnthropicProvider {
	providerConfig := *provider
	providerConfigNetwork := providerConfig
	providerConfigNetworkPtr := &providerConfigNetwork
	_ = providerConfigNetworkPtr
	return provider
}
