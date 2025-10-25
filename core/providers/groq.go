// Package providers implements various LLM providers and their utility functions.
// This file contains the Groq provider implementation.
package providers

import (
	"context"
	"net/http"
	"strings"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// GroqProvider implements the Provider interface for Groq's API.
type GroqProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	streamClient        *http.Client          // HTTP client for streaming requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewGroqProvider creates a new Groq provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewGroqProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*GroqProvider, error) {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.BufferSize,
	}

	// Initialize streaming HTTP client
	streamClient := &http.Client{
		Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
	}

	// // Pre-warm response pools
	// for range config.ConcurrencyAndBufferSize.Concurrency {
	// 	groqResponsePool.Put(&schemas.BifrostResponse{})
	// }

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.groq.com/openai"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &GroqProvider{
		logger:              logger,
		client:              client,
		streamClient:        streamClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Groq.
func (provider *GroqProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Groq
}

// ListModels performs a list models request to Groq's API.
func (provider *GroqProvider) ListModels(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return handleOpenAIListModelsRequest(
		ctx,
		provider.client,
		request,
		provider.networkConfig.BaseURL+"/v1/models",
		key,
		provider.networkConfig.ExtraHeaders,
		schemas.Groq,
		provider.sendBackRawResponse,
		provider.logger,
	)
}

// TextCompletion is not supported by the Groq provider.
func (provider *GroqProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	// Checking if litellm fallback is set
	if _, ok := ctx.Value(schemas.BifrostContextKey("x-litellm-fallback")).(string); !ok {
		return nil, newUnsupportedOperationError("text completion", "groq")
	}
	// Here we will call the chat.completions endpoint and mock it as a text-completion response
	chatRequest := request.ToBifrostChatRequest()
	if chatRequest == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "invalid text completion request: missing or empty prompt",
			},
		}
	}
	chatResponse, err := provider.ChatCompletion(ctx, key, chatRequest)
	if err != nil {
		return nil, err
	}
	response := chatResponse.ToTextCompletionResponse()
	response.ExtraFields.RequestType = schemas.TextCompletionRequest
	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.ModelRequested = request.Model
	return response, nil
}

// TextCompletionStream performs a streaming text completion request to Groq's API.
// It formats the request, sends it to Groq, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *GroqProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Checking if litellm fallback is set
	if _, ok := ctx.Value(schemas.BifrostContextKey("x-litellm-fallback")).(string); !ok {
		return nil, newUnsupportedOperationError("text completion", "groq")
	}
	// Here we will call the chat.completions endpoint and mock it as a text-completion stream response
	chatRequest := request.ToBifrostChatRequest()
	if chatRequest == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "invalid text completion request: missing or empty prompt",
			},
		}
	}
	response, err := provider.ChatCompletionStream(ctx, postHookRunner, key, chatRequest)
	if err != nil {
		return nil, err
	}
	// Creating a converter from chat completion stream to text completion stream
	responseChan := make(chan *schemas.BifrostStream, 1)
	go func() {
		defer close(responseChan)
		for response := range response {
			if response.BifrostError != nil {
				responseChan <- response
				continue
			}
			if response.BifrostChatResponse != nil {
				textCompletionResponse := response.BifrostChatResponse.ToTextCompletionResponse()
				if textCompletionResponse != nil {
					textCompletionResponse.ExtraFields.RequestType = schemas.TextCompletionRequest
					textCompletionResponse.ExtraFields.Provider = provider.GetProviderKey()
					textCompletionResponse.ExtraFields.ModelRequested = request.Model

					responseChan <- &schemas.BifrostStream{
						BifrostTextCompletionResponse: textCompletionResponse,
					}
				}
			}
		}
	}()
	return responseChan, nil
}

// ChatCompletion performs a chat completion request to the Groq API.
func (provider *GroqProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return handleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/v1/chat/completions",
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		provider.GetProviderKey(),
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to the Groq API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Groq's OpenAI-compatible streaming format.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *GroqProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Use shared OpenAI-compatible streaming logic
	return handleOpenAIChatCompletionStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/v1/chat/completions",
		request,
		map[string]string{"Authorization": "Bearer " + key.Value},
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
		schemas.Groq,
		postHookRunner,
		provider.logger,
	)
}

// Responses performs a responses request to the Groq API.
func (provider *GroqProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	chatResponse, err := provider.ChatCompletion(ctx, key, request.ToChatRequest())
	if err != nil {
		return nil, err
	}

	response := chatResponse.ToBifrostResponsesResponse()
	response.ExtraFields.RequestType = schemas.ResponsesRequest
	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.ModelRequested = request.Model

	return response, nil
}

// ResponsesStream performs a streaming responses request to the Groq API.
func (provider *GroqProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return provider.ChatCompletionStream(
		ctx,
		getResponsesChunkConverterCombinedPostHookRunner(postHookRunner),
		key,
		request.ToChatRequest(),
	)
}

// Embedding is not supported by the Groq provider.
func (provider *GroqProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("embedding", "groq")
}

// Speech is not supported by the Groq provider.
func (provider *GroqProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "groq")
}

// SpeechStream is not supported by the Groq provider.
func (provider *GroqProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "groq")
}

// Transcription is not supported by the Groq provider.
func (provider *GroqProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "groq")
}

// TranscriptionStream is not supported by the Groq provider.
func (provider *GroqProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "groq")
}
