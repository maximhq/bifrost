// Package providers implements various LLM providers and their utility functions.
// This file contains the Cerebras provider implementation.
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

// cerebrasTextResponsePool provides a pool for Cerebras text completion response objects.
var cerebrasTextResponsePool = sync.Pool{
	New: func() interface{} {
		return &openai.OpenAITextCompletionResponse{}
	},
}

// acquireCerebrasTextResponse gets a Cerebras text completion response from the pool and resets it.
func acquireCerebrasTextResponse() *openai.OpenAITextCompletionResponse {
	resp := cerebrasTextResponsePool.Get().(*openai.OpenAITextCompletionResponse)
	*resp = openai.OpenAITextCompletionResponse{} // Reset the struct
	return resp
}

// releaseCerebrasTextResponse returns a Cerebras text completion response to the pool.
func releaseCerebrasTextResponse(resp *openai.OpenAITextCompletionResponse) {
	if resp != nil {
		cerebrasTextResponsePool.Put(resp)
	}
}

// CerebrasProvider implements the Provider interface for Cerebras's API.
type CerebrasProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	streamClient        *http.Client          // HTTP client for streaming requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewCerebrasProvider creates a new Cerebras provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewCerebrasProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*CerebrasProvider, error) {
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
		// cerebrasChatResponsePool.Put(&schemas.BifrostResponse{})
		cerebrasTextResponsePool.Put(&openai.OpenAITextCompletionResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.cerebras.ai"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &CerebrasProvider{
		logger:              logger,
		client:              client,
		streamClient:        streamClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Cerebras.
func (provider *CerebrasProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Cerebras
}

// TextCompletion performs a text completion request to Cerebras's API.
// It formats the request, sends it to Cerebras, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *CerebrasProvider) TextCompletion(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Use centralized OpenAI text converter (Cerebras is OpenAI-compatible)
	reqBody := openai.ToOpenAITextCompletionRequest(input)

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.Cerebras)
	}

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/completions")
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
		provider.logger.Debug(fmt.Sprintf("error from cerebras provider: %s", string(resp.Body())))

		var errorResp map[string]interface{}
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = fmt.Sprintf("Cerebras error: %v", errorResp)
		return nil, bifrostErr
	}

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
	response := acquireCerebrasTextResponse()
	defer releaseCerebrasTextResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Use centralized OpenAI response converter (Cerebras is OpenAI-compatible)
	bifrostResponse := response.ToBifrostResponse()

	bifrostResponse.ExtraFields.Provider = schemas.Cerebras

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	if input.Params != nil {
		bifrostResponse.ExtraFields.Params = *input.Params
	}

	return bifrostResponse, nil
}

// ChatCompletion performs a chat completion request to the Cerebras API.
func (provider *CerebrasProvider) ChatCompletion(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Use centralized OpenAI converter since Cerebras is OpenAI-compatible
	reqBody := openai.ToOpenAIChatCompletionRequest(input)

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.Cerebras)
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
		provider.logger.Debug(fmt.Sprintf("error from cerebras provider: %s", string(resp.Body())))

		var errorResp map[string]interface{}
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = fmt.Sprintf("Cerebras error: %v", errorResp)
		return nil, bifrostErr
	}

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
	response := &schemas.BifrostResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	response.ExtraFields.Provider = schemas.Cerebras

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	if input.Params != nil {
		response.ExtraFields.Params = *input.Params
	}

	return response, nil
}

// Embedding is not supported by the Cerebras provider.
func (provider *CerebrasProvider) Embedding(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("embedding", "cerebras")
}

// ChatCompletionStream performs a streaming chat completion request to the Cerebras API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Cerebras's OpenAI-compatible streaming format.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *CerebrasProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, input *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {

	reqBody := openai.ToOpenAIChatCompletionRequest(input)
	reqBody.Stream = schemas.Ptr(true)

	// Prepare Cerebras headers
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
		reqBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		schemas.Cerebras,
		input.Params,
		postHookRunner,
		provider.logger,
	)
}

func (provider *CerebrasProvider) Speech(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "cerebras")
}

func (provider *CerebrasProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, input *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "cerebras")
}

func (provider *CerebrasProvider) Transcription(ctx context.Context, key schemas.Key, input *schemas.BifrostRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "cerebras")
}

func (provider *CerebrasProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, input *schemas.BifrostRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "cerebras")
}
