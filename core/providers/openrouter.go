// Package providers implements various LLM providers and their utility functions.
// This file contains the OpenRouter provider implementation.
package providers

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/openai"
	"github.com/valyala/fasthttp"
)

// OpenRouterProvider implements the Provider interface for OpenRouter's API.
type OpenRouterProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	streamClient        *http.Client          // HTTP client for streaming requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// openRouterTextCompletionResponsePool provides a pool for OpenRouter text completion response objects.
var openRouterTextCompletionResponsePool = sync.Pool{
	New: func() interface{} {
		return &openai.OpenAITextCompletionResponse{}
	},
}

// acquireOpenRouterTextResponse gets an OpenRouter text completion response from the pool and resets it.
func acquireOpenRouterTextResponse() *openai.OpenAITextCompletionResponse {
	resp := openRouterTextCompletionResponsePool.Get().(*openai.OpenAITextCompletionResponse)
	*resp = openai.OpenAITextCompletionResponse{} // Reset the struct
	return resp
}

// releaseOpenRouterTextResponse returns an OpenRouter text completion response to the pool.
func releaseOpenRouterTextResponse(resp *openai.OpenAITextCompletionResponse) {
	if resp != nil {
		openRouterTextCompletionResponsePool.Put(resp)
	}
}

// NewOpenRouterProvider creates a new OpenRouter provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewOpenRouterProvider(config *schemas.ProviderConfig, logger schemas.Logger) *OpenRouterProvider {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:     time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:    time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost: config.ConcurrencyAndBufferSize.Concurrency,
	}

	// Initialize streaming HTTP client
	streamClient := &http.Client{
		Timeout: time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
	}

	// Pre-warm response pools
	for i := 0; i < config.ConcurrencyAndBufferSize.Concurrency; i++ {
		openRouterTextCompletionResponsePool.Put(&openai.OpenAITextCompletionResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://openrouter.ai/api"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &OpenRouterProvider{
		logger:              logger,
		client:              client,
		streamClient:        streamClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawResponse: config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for OpenRouter.
func (provider *OpenRouterProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.OpenRouter
}

// TextCompletion performs a text completion request to the OpenRouter API.
func (provider *OpenRouterProvider) TextCompletion(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return handleOpenAITextCompletionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/v1/completions",
		input,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		provider.sendBackRawResponse,
		provider.logger,
		acquireOpenRouterTextResponse,
		releaseOpenRouterTextResponse,
	)
}

// ChatCompletion performs a chat completion request to the OpenRouter API.
func (provider *OpenRouterProvider) ChatCompletion(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return handleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/v1/chat/completions",
		input,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		provider.sendBackRawResponse,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to the OpenRouter API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses OpenRouter's OpenAI-compatible streaming format.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *OpenRouterProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, input *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Use centralized OpenAI converter since OpenRouter is OpenAI-compatible
	reqBody := openai.ToOpenAIChatCompletionRequest(input)
	reqBody.Stream = schemas.Ptr(true)

	// Prepare OpenRouter headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + key.Value,
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	// Use shared OpenAI-compatible streaming logic
	return handleOpenAIStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/v1/chat/completions",
		reqBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		schemas.OpenRouter,
		input.Params,
		postHookRunner,
		provider.logger,
	)
}

func (provider *OpenRouterProvider) Embedding(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("embedding", "openrouter")
}

func (provider *OpenRouterProvider) Speech(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "openrouter")
}

func (provider *OpenRouterProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, input *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "openrouter")
}

func (provider *OpenRouterProvider) Transcription(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "openrouter")
}

func (provider *OpenRouterProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, input *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "openrouter")
}

func (provider *OpenRouterProvider) Responses(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("responses", "openrouter")
}

func (provider *OpenRouterProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, input *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("responses stream", "openrouter")
}
