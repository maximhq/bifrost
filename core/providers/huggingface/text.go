package huggingface

import (
	"context"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TextCompletion converts text completion requests to chat completion requests since
// Hugging Face's router API only supports chat completions at /v1/chat/completions.
func (provider *HuggingFaceProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.TextCompletionRequest); err != nil {
		return nil, err
	}

	// Convert text completion request to chat completion
	chatRequest := provider.convertTextToChatRequest(request)
	effectiveChatRequest, resolvedModel := provider.prepareChatRequest(chatRequest, key)

	// Make the chat completion request
	chatResponse, bifrostErr := openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/chat/completions", schemas.ChatCompletionRequest),
		effectiveChatRequest,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.shouldSendRawResponse(ctx),
		provider.GetProviderKey(),
		provider.logger,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert chat completion response back to text completion response
	textResponse := provider.convertChatToTextResponse(chatResponse, request.Model, resolvedModel)
	return textResponse, nil
}

// TextCompletionStream converts streaming text completion requests to streaming chat completion requests.
func (provider *HuggingFaceProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.TextCompletionStreamRequest); err != nil {
		return nil, err
	}

	// Convert text completion request to chat completion
	chatRequest := provider.convertTextToChatRequest(request)
	effectiveChatRequest, resolvedModel := provider.prepareChatRequest(chatRequest, key)

	// Make the streaming chat completion request
	chatStream, bifrostErr := openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/chat/completions", schemas.ChatCompletionStreamRequest),
		effectiveChatRequest,
		provider.buildAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		provider.shouldSendRawResponse(ctx),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		provider.logger,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bufferSize := cap(chatStream)
	if bufferSize == 0 {
		bufferSize = 1
	}

	textStream := make(chan *schemas.BifrostStream, bufferSize)

	go func() {
		defer close(textStream)
		for streamMsg := range chatStream {
			if streamMsg == nil {
				continue
			}

			if chatResp := streamMsg.BifrostChatResponse; chatResp != nil {
				textResp := provider.convertChatToTextResponse(chatResp, request.Model, resolvedModel)
				if textResp != nil {
					textResp.ExtraFields.RequestType = schemas.TextCompletionStreamRequest
					provider.decorateResponseMetadata(&textResp.ExtraFields, request.Model, resolvedModel)
					streamMsg.BifrostTextCompletionResponse = textResp
					streamMsg.BifrostChatResponse = nil
				}
			}

			textStream <- streamMsg
		}
	}()

	return textStream, nil
}
