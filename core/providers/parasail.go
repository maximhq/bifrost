// Package providers implements various LLM providers and their utility functions.
// This file contains the Parasail provider implementation.
package providers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parasailResponsePool provides a pool for Parasail response objects.
var parasailResponsePool = sync.Pool{
	New: func() interface{} {
		return &schemas.BifrostResponse{}
	},
}

// acquireParasailResponse gets a Parasail response from the pool and resets it.
func acquireParasailResponse() *schemas.BifrostResponse {
	resp := parasailResponsePool.Get().(*schemas.BifrostResponse)
	*resp = schemas.BifrostResponse{} // Reset the struct
	return resp
}

// releaseParasailResponse returns a Parasail response to the pool.
func releaseParasailResponse(resp *schemas.BifrostResponse) {
	if resp != nil {
		parasailResponsePool.Put(resp)
	}
}

// ParasailProvider implements the Provider interface for Parasail's API.
type ParasailProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	streamClient        *http.Client          // HTTP client for streaming requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewParasailProvider creates a new Parasail provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewParasailProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*ParasailProvider, error) {
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

	// Pre-warm response pools
	for range config.ConcurrencyAndBufferSize.Concurrency {
		parasailResponsePool.Put(&schemas.BifrostResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.parasail.io"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &ParasailProvider{
		logger:              logger,
		client:              client,
		streamClient:        streamClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Parasail.
func (provider *ParasailProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Parasail
}

// TextCompletion is not supported by the Parasail provider.
func (provider *ParasailProvider) TextCompletion(ctx context.Context, model string, key schemas.Key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion", "parasail")
}

// ChatCompletion performs a chat completion request to the Parasail API.
func (provider *ParasailProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.Parasail)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/chat/completions")
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)

	req.SetBody(jsonBody)

	// Make request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from parasail provider: %s", string(resp.Body())))

		var errorResp map[string]interface{}
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = fmt.Sprintf("Parasail error: %v", errorResp)
		return nil, bifrostErr
	}

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
response := acquireParasailResponse()
	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	response.ExtraFields.Provider = schemas.Parasail

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		response.ExtraFields.Params = *params
	}

	return response, nil
}

// Embedding is not supported by the Parasail provider.
func (provider *ParasailProvider) Embedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("embedding", "parasail")
}

// ChatCompletionStream performs a streaming chat completion request to the Parasail API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Parasail's OpenAI-compatible streaming format.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *ParasailProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
		"stream":   true,
	}, preparedParams)

	// Prepare Parasail headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	headers["Authorization"] = "Bearer " + key.Value

	// Use shared OpenAI-compatible streaming logic
	return handleOpenAIStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/v1/chat/completions",
		requestBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		schemas.Parasail,
		params,
		postHookRunner,
		provider.logger,
	)
}

// Speech is not supported by the Parasail provider.
func (provider *ParasailProvider) Speech(ctx context.Context, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "parasail")
}

// SpeechStream is not supported by the Parasail provider.
func (provider *ParasailProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "parasail")
}

// Transcription is not supported by the Parasail provider.
func (provider *ParasailProvider) Transcription(ctx context.Context, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "parasail")
}

// TranscriptionStream is not supported by the Parasail provider.
func (provider *ParasailProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "parasail")
}
