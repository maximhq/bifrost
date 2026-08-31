// Inference operations served by both Databricks surfaces. Every method delegates to the
// shared OpenAI handlers; this file only supplies the resolved URL and Authorization header.
package databricks

import (
	"context"
	"maps"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// stripUnsupportedChatFields removes request extensions that Databricks'
// OpenAI-compatible Chat Completions surfaces reject. Work on a
// copy because the original request may be reused by fallbacks and post-hooks.
func stripUnsupportedChatFields(request *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	if request == nil || request.Params == nil {
		return request
	}
	_, hasExtraContextManagement := request.Params.ExtraParams["context_management"]
	_, hasExtraReasoningEffort := request.Params.ExtraParams["reasoning_effort"]
	hasReasoningEffort := request.Params.Reasoning != nil &&
		(request.Params.Reasoning.Effort != nil || request.Params.Reasoning.MaxTokens != nil)
	if len(request.Params.ContextManagement) == 0 && !hasExtraContextManagement &&
		!hasReasoningEffort && !hasExtraReasoningEffort {
		return request
	}

	requestCopy := *request
	paramsCopy := *request.Params
	paramsCopy.ContextManagement = nil
	if paramsCopy.Reasoning != nil {
		reasoningCopy := *paramsCopy.Reasoning
		reasoningCopy.Effort = nil
		// The shared OpenAI converter derives reasoning_effort from MaxTokens
		// when Effort is absent, so clear both inputs to guarantee omission.
		reasoningCopy.MaxTokens = nil
		paramsCopy.Reasoning = &reasoningCopy
	}
	if paramsCopy.ExtraParams != nil {
		paramsCopy.ExtraParams = maps.Clone(paramsCopy.ExtraParams)
		delete(paramsCopy.ExtraParams, "context_management")
		delete(paramsCopy.ExtraParams, "reasoning_effort")
	}
	requestCopy.Params = &paramsCopy
	return &requestCopy
}

// ChatCompletion performs a chat completion request against the resolved Databricks surface.
func (provider *DatabricksProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	request = stripUnsupportedChatFields(request)
	url, auth, bErr := provider.prepareRequest(ctx, key, request.Model, "/chat/completions")
	if bErr != nil {
		return nil, bErr
	}
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		url,
		request,
		auth,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request against the resolved
// Databricks surface. Both surfaces emit OpenAI-shaped Server-Sent Events.
func (provider *DatabricksProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	request = stripUnsupportedChatFields(request)
	url, auth, bErr := provider.prepareRequest(ctx, key, request.Model, "/chat/completions")
	if bErr != nil {
		return nil, bErr
	}
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.streamingClient,
		url,
		request,
		auth,
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		chatStreamOptionsFixup(request.Model),
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// Embedding performs an embedding request against the resolved Databricks surface.
func (provider *DatabricksProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	url, auth, bErr := provider.prepareRequest(ctx, key, request.Model, "/embeddings")
	if bErr != nil {
		return nil, bErr
	}
	return openai.HandleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		url,
		request,
		auth,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		nil,
		provider.logger,
	)
}

// Responses performs a responses request. Model Serving exposes the OpenAI Responses API
// natively at /serving-endpoints/responses. The Unity AI Gateway MLflow surface is chat-only,
// so there the request is emulated through chat completions.
func (provider *DatabricksProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if resolveAPIFormat(key, request.Model) == schemas.DatabricksAPIFormatAIGateway {
		chatResponse, bErr := provider.ChatCompletion(ctx, key, request.ToChatRequest())
		if bErr != nil {
			return nil, bErr
		}
		return chatResponse.ToBifrostResponsesResponse(), nil
	}

	url, auth, bErr := provider.prepareRequest(ctx, key, request.Model, "/responses")
	if bErr != nil {
		return nil, bErr
	}
	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.client,
		url,
		request,
		auth,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// ResponsesStream performs a streaming responses request. See Responses for how the two
// surfaces differ.
func (provider *DatabricksProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if resolveAPIFormat(key, request.Model) == schemas.DatabricksAPIFormatAIGateway {
		ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
		return provider.ChatCompletionStream(ctx, postHookRunner, postHookSpanFinalizer, key, request.ToChatRequest())
	}

	url, auth, bErr := provider.prepareRequest(ctx, key, request.Model, "/responses")
	if bErr != nil {
		return nil, bErr
	}
	return openai.HandleOpenAIResponsesStreaming(
		ctx,
		provider.streamingClient,
		url,
		request,
		auth,
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}
