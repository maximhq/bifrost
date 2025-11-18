package huggingface

import (
	"context"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	// According to https://huggingface.co/docs/inference-providers/en/tasks/chat-completion the
	// OpenAI-compatible router lives under the /v1 prefix, so we wire that in as the default base URL.
	defaultInferenceBaseURL = "https://router.huggingface.co/v1"
	modelHubBaseURL         = "https://huggingface.co"
)

// HuggingFaceProvider implements the Provider interface for Hugging Face's inference APIs.
type HuggingFaceProvider struct {
	logger               schemas.Logger
	client               *fasthttp.Client
	networkConfig        schemas.NetworkConfig
	sendBackRawResponse  bool
	customProviderConfig *schemas.CustomProviderConfig
}

// NewHuggingFaceProvider creates a new Hugging Face provider instance configured with the provided settings.
func NewHuggingFaceProvider(config *schemas.ProviderConfig, logger schemas.Logger) *HuggingFaceProvider {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:         time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:        time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     5000,
		MaxIdleConnDuration: 60 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = defaultInferenceBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &HuggingFaceProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}
}

// GetProviderKey returns the provider key, taking custom providers into account.
func (provider *HuggingFaceProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.HuggingFace, provider.customProviderConfig)
}

// buildRequestURL composes the final request URL based on context overrides.
func (provider *HuggingFaceProvider) buildRequestURL(ctx context.Context, defaultPath string, requestType schemas.RequestType) string {
	return provider.networkConfig.BaseURL + providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
}

func (provider *HuggingFaceProvider) shouldSendRawResponse(ctx context.Context) bool {
	return providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
}

func (provider *HuggingFaceProvider) buildAuthHeader(key schemas.Key) map[string]string {
	if key.Value == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + key.Value}
}

// resolveModelAlias returns the actual Hub identifier for the requested model, if an alias is configured on the key.
func (provider *HuggingFaceProvider) resolveModelAlias(key schemas.Key, requestedModel string) (string, bool) {
	if key.HuggingFaceKeyConfig == nil || len(key.HuggingFaceKeyConfig.Deployments) == 0 {
		return requestedModel, false
	}

	if deployment, ok := key.HuggingFaceKeyConfig.Deployments[requestedModel]; ok && strings.TrimSpace(deployment) != "" {
		return deployment, deployment != requestedModel
	}

	return requestedModel, false
}

func (provider *HuggingFaceProvider) prepareChatRequest(request *schemas.BifrostChatRequest, key schemas.Key) (*schemas.BifrostChatRequest, string) {
	resolvedModel, changed := provider.resolveModelAlias(key, request.Model)
	if !changed {
		return request, resolvedModel
	}

	clone := *request
	clone.Model = resolvedModel
	return &clone, resolvedModel
}

func (provider *HuggingFaceProvider) prepareTextRequest(request *schemas.BifrostTextCompletionRequest, key schemas.Key) (*schemas.BifrostTextCompletionRequest, string) {
	resolvedModel, changed := provider.resolveModelAlias(key, request.Model)
	if !changed {
		return request, resolvedModel
	}

	clone := *request
	clone.Model = resolvedModel
	return &clone, resolvedModel
}

func (provider *HuggingFaceProvider) prepareEmbeddingRequest(request *schemas.BifrostEmbeddingRequest, key schemas.Key) (*schemas.BifrostEmbeddingRequest, string) {
	resolvedModel, changed := provider.resolveModelAlias(key, request.Model)
	if !changed {
		return request, resolvedModel
	}

	clone := *request
	clone.Model = resolvedModel
	return &clone, resolvedModel
}

func (provider *HuggingFaceProvider) decorateResponseMetadata(extra *schemas.BifrostResponseExtraFields, requestedModel, resolvedModel string) {
	if extra == nil {
		return
	}

	if requestedModel != "" {
		extra.ModelRequested = requestedModel
	}

	if resolvedModel != "" && resolvedModel != requestedModel {
		extra.ModelDeployment = resolvedModel
	}
}

func (provider *HuggingFaceProvider) chatStreamPostConverter(requestedModel, resolvedModel string) func(*schemas.BifrostChatResponse) *schemas.BifrostChatResponse {
	if requestedModel == "" || requestedModel == resolvedModel {
		return nil
	}

	return func(resp *schemas.BifrostChatResponse) *schemas.BifrostChatResponse {
		if resp == nil {
			return nil
		}
		provider.decorateResponseMetadata(&resp.ExtraFields, requestedModel, resolvedModel)
		return resp
	}
}

func (provider *HuggingFaceProvider) textStreamPostConverter(requestedModel, resolvedModel string) func(*schemas.BifrostTextCompletionResponse) *schemas.BifrostTextCompletionResponse {
	if requestedModel == "" || requestedModel == resolvedModel {
		return nil
	}

	return func(resp *schemas.BifrostTextCompletionResponse) *schemas.BifrostTextCompletionResponse {
		if resp == nil {
			return nil
		}
		provider.decorateResponseMetadata(&resp.ExtraFields, requestedModel, resolvedModel)
		return resp
	}
}

// convertTextToChatRequest converts a text completion request to a chat completion request
func (provider *HuggingFaceProvider) convertTextToChatRequest(textReq *schemas.BifrostTextCompletionRequest) *schemas.BifrostChatRequest {
	// Use the built-in conversion method
	return textReq.ToBifrostChatRequest()
}

// convertChatToTextResponse converts a chat completion response to a text completion response
func (provider *HuggingFaceProvider) convertChatToTextResponse(chatResp *schemas.BifrostChatResponse, requestedModel, resolvedModel string) *schemas.BifrostTextCompletionResponse {
	if chatResp == nil {
		return nil
	}

	textResp := &schemas.BifrostTextCompletionResponse{
		ID:                chatResp.ID,
		Object:            "text_completion",
		Model:             chatResp.Model,
		SystemFingerprint: chatResp.SystemFingerprint,
		Choices:           make([]schemas.BifrostResponseChoice, len(chatResp.Choices)),
		Usage:             chatResp.Usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:        chatResp.ExtraFields.Provider,
			ModelRequested:  requestedModel,
			ModelDeployment: resolvedModel,
			RequestType:     schemas.TextCompletionRequest,
			Latency:         chatResp.ExtraFields.Latency,
			RawResponse:     chatResp.ExtraFields.RawResponse,
		},
	}

	for i, choice := range chatResp.Choices {
		text := ""
		switch {
		case choice.ChatNonStreamResponseChoice != nil && choice.ChatNonStreamResponseChoice.Message != nil &&
			choice.ChatNonStreamResponseChoice.Message.Content != nil && choice.ChatNonStreamResponseChoice.Message.Content.ContentStr != nil:
			text = *choice.ChatNonStreamResponseChoice.Message.Content.ContentStr
		case choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil &&
			choice.ChatStreamResponseChoice.Delta.Content != nil:
			text = *choice.ChatStreamResponseChoice.Delta.Content
		}

		textResp.Choices[i] = schemas.BifrostResponseChoice{
			Index:        choice.Index,
			FinishReason: choice.FinishReason,
			LogProbs:     choice.LogProbs,
			TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{
				Text: &text,
			},
		}
	}

	return textResp
}

// chatToTextStreamConverter converts streaming chat responses to text completion responses
func (provider *HuggingFaceProvider) chatToTextStreamConverter(requestedModel, resolvedModel string) func(*schemas.BifrostChatResponse) *schemas.BifrostChatResponse {
	return func(chatResp *schemas.BifrostChatResponse) *schemas.BifrostChatResponse {
		if chatResp == nil {
			return nil
		}

		// Update metadata for text completion
		chatResp.Object = "text_completion"
		chatResp.ExtraFields.RequestType = schemas.TextCompletionStreamRequest
		provider.decorateResponseMetadata(&chatResp.ExtraFields, requestedModel, resolvedModel)

		// Convert delta content to text format
		for i := range chatResp.Choices {
			if chatResp.Choices[i].ChatStreamResponseChoice != nil &&
				chatResp.Choices[i].ChatStreamResponseChoice.Delta != nil &&
				chatResp.Choices[i].ChatStreamResponseChoice.Delta.Content != nil {

				text := *chatResp.Choices[i].ChatStreamResponseChoice.Delta.Content

				// Replace the choice with a text completion choice
				chatResp.Choices[i] = schemas.BifrostResponseChoice{
					Index:        chatResp.Choices[i].Index,
					FinishReason: chatResp.Choices[i].FinishReason,
					LogProbs:     chatResp.Choices[i].LogProbs,
					TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{
						Text: &text,
					},
				}
			} else if chatResp.Choices[i].ChatStreamResponseChoice != nil {
				emptyText := ""
				chatResp.Choices[i] = schemas.BifrostResponseChoice{
					Index:        chatResp.Choices[i].Index,
					FinishReason: chatResp.Choices[i].FinishReason,
					LogProbs:     chatResp.Choices[i].LogProbs,
					TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{
						Text: &emptyText,
					},
				}
			}
		}

		return chatResp
	}
}

// Speech is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}
