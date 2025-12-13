// Package nebius implements the Nebius Token Factory LLM provider.
// Nebius Token Factory provides an OpenAI-compatible API for accessing various LLM models.
package nebius

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// NebiusProvider implements the Provider interface for Nebius Token Factory API.
// It leverages the OpenAI-compatible API endpoints provided by Nebius.
type NebiusProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewNebiusProvider creates a new Nebius provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewNebiusProvider(config *schemas.ProviderConfig, logger schemas.Logger) *NebiusProvider {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:         time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:        time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     5000,
		MaxIdleConnDuration: 60 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	// Configure proxy if provided
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided - Nebius Token Factory API endpoint
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.studio.nebius.ai/v1"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &NebiusProvider{
		logger:              logger,
		client:              client,
		networkConfig:       config.NetworkConfig,
		sendBackRawResponse: config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Nebius.
func (provider *NebiusProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Nebius
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *NebiusProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/models"))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value))
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		bifrostErr := openai.ParseOpenAIError(resp, schemas.ListModelsRequest, providerName, "")
		return nil, bifrostErr
	}

	// Copy response body before releasing
	responseBody := append([]byte(nil), resp.Body()...)

	var nebiusResponse schemas.BifrostListModelsResponse
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &nebiusResponse, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Prefix model IDs with provider name for consistency
	for i := range nebiusResponse.Data {
		nebiusResponse.Data[i].ID = string(schemas.Nebius) + "/" + nebiusResponse.Data[i].ID
	}

	nebiusResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		nebiusResponse.ExtraFields.RawResponse = rawResponse
	}

	return &nebiusResponse, nil
}

// ListModels performs a list models request to Nebius's API.
// Requests are made concurrently for improved performance.
func (provider *NebiusProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
		provider.logger,
	)
}

// TextCompletion performs a text completion request to the Nebius API.
// Uses OpenAI-compatible endpoint format.
func (provider *NebiusProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return openai.HandleOpenAITextCompletionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/completions"),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// TextCompletionStream performs a streaming text completion request to Nebius's API.
// It formats the request, sends it to Nebius, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *NebiusProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	var authHeader map[string]string
	if key.Value != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value}
	}
	return openai.HandleOpenAITextCompletionStreaming(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/completions",
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		provider.logger,
	)
}

// ChatCompletion performs a chat completion request to the Nebius API.
// Uses OpenAI-compatible chat completions endpoint.
func (provider *NebiusProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/chat/completions"),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to the Nebius API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Nebius's OpenAI-compatible streaming format.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *NebiusProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	var authHeader map[string]string
	if key.Value != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value}
	}
	// Use shared OpenAI-compatible streaming logic
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/chat/completions"),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		schemas.Nebius,
		postHookRunner,
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// Responses performs a responses request to the Nebius API.
// Converts the request to chat completion format for compatibility.
func (provider *NebiusProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	// Nebius uses OpenAI-compatible API, so we convert responses to chat completion
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

// ResponsesStream performs a streaming responses request to the Nebius API.
// Converts the request to chat completion stream for compatibility.
func (provider *NebiusProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return provider.ChatCompletionStream(
		ctx,
		postHookRunner,
		key,
		request.ToChatRequest(),
	)
}

// Embedding performs an embedding request to the Nebius API.
// Uses OpenAI-compatible embeddings endpoint.
func (provider *NebiusProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return openai.HandleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/embeddings"),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// Speech is not supported by the Nebius provider.
func (provider *NebiusProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Nebius provider.
func (provider *NebiusProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Nebius provider.
func (provider *NebiusProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Nebius provider.
func (provider *NebiusProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

