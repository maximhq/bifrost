package huggingface

import (
	"context"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ChatCompletionStream forwards streaming chat responses while keeping alias metadata aligned.
func (provider *HuggingFaceProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	effectiveRequest, resolvedModel := provider.prepareChatRequest(request, key)

	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/chat/completions", schemas.ChatCompletionStreamRequest),
		effectiveRequest,
		provider.buildAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		provider.shouldSendRawResponse(ctx),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		provider.chatStreamPostConverter(request.Model, resolvedModel),
		provider.logger,
	)
}
