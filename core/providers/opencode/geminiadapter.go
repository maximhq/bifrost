package opencode

import (
	"context"

	"github.com/maximhq/bifrost/core/providers/gemini"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func (p *opencodeProvider) executeGeminiChat(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	provider := gemini.NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig:       p.networkConfig,
		SendBackRawRequest:  p.sendBackRawRequest,
		SendBackRawResponse: p.sendBackRawResponse,
	}, p.logger)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, route.Path+":generateContent")
	return provider.ChatCompletion(ctx, key, request)
}

func (p *opencodeProvider) executeGeminiChatStream(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	provider := gemini.NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig:       p.networkConfig,
		SendBackRawRequest:  p.sendBackRawRequest,
		SendBackRawResponse: p.sendBackRawResponse,
	}, p.logger)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, route.Path+":streamGenerateContent?alt=sse")
	return provider.ChatCompletionStream(ctx, postHookRunner, postHookSpanFinalizer, key, request)
}

func (p *opencodeProvider) executeGeminiResponses(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	provider := gemini.NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig:       p.networkConfig,
		SendBackRawRequest:  p.sendBackRawRequest,
		SendBackRawResponse: p.sendBackRawResponse,
	}, p.logger)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, route.Path+":generateContent")
	return provider.Responses(ctx, key, request)
}

func (p *opencodeProvider) executeGeminiResponsesStream(
	ctx *schemas.BifrostContext,
	route resolvedRoute,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	provider := gemini.NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig:       p.networkConfig,
		SendBackRawRequest:  p.sendBackRawRequest,
		SendBackRawResponse: p.sendBackRawResponse,
	}, p.logger)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, route.Path+":streamGenerateContent?alt=sse")
	return provider.ResponsesStream(ctx, postHookRunner, postHookSpanFinalizer, key, request)
}
