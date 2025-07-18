// Package providers implements various LLM providers and their utility functions.
// This file contains the SGL provider implementation.
package providers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// SGLResponse represents the response structure from the SGL API.
type SGLResponse struct {
	ID      string                          `json:"id"`
	Object  string                          `json:"object"`
	Choices []schemas.BifrostResponseChoice `json:"choices"`
	Model   string                          `json:"model"`
	Created int                             `json:"created"`
	Usage   schemas.LLMUsage                `json:"usage"`
}

// sglResponsePool provides a pool for SGL response objects.
var sglResponsePool = sync.Pool{
	New: func() interface{} {
		return &SGLResponse{}
	},
}

// acquireSGLResponse gets a SGL response from the pool and resets it.
func acquireSGLResponse() *SGLResponse {
	resp := sglResponsePool.Get().(*SGLResponse)
	*resp = SGLResponse{} // Reset the struct
	return resp
}

// releaseSGLResponse returns a SGL response to the pool.
func releaseSGLResponse(resp *SGLResponse) {
	if resp != nil {
		sglResponsePool.Put(resp)
	}
}

// SGLProvider implements the Provider interface for SGL's API.
type SGLProvider struct {
	logger        schemas.Logger        // Logger for provider operations
	client        *fasthttp.Client      // HTTP client for API requests
	streamClient  *http.Client          // HTTP client for streaming requests
	networkConfig schemas.NetworkConfig // Network configuration including extra headers
}

// NewSGLProvider creates a new SGL provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewSGLProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*SGLProvider, error) {
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
		sglResponsePool.Put(&SGLResponse{})
	}

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	// BaseURL is required for SGLang
	if config.NetworkConfig.BaseURL == "" {
		return nil, fmt.Errorf("base_url is required for sgl provider")
	}

	return &SGLProvider{
		logger:        logger,
		client:        client,
		streamClient:  streamClient,
		networkConfig: config.NetworkConfig,
	}, nil
}

// GetProviderKey returns the provider identifier for SGL.
func (provider *SGLProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.SGL
}

// TextCompletion is not supported by the SGL provider.
func (provider *SGLProvider) TextCompletion(ctx context.Context, model string, key schemas.Key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion", "sgl")
}

// ChatCompletion performs a chat completion request to the SGL API.
func (provider *SGLProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: schemas.ErrorField{
				Message: schemas.ErrProviderJSONMarshaling,
				Error:   err,
			},
		}
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
	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	req.SetBody(jsonBody)

	// Make request
	bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from sgl provider: %s", string(resp.Body())))

		var errorResp map[string]interface{}
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = fmt.Sprintf("SGL error: %v", errorResp)
		return nil, bifrostErr
	}

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
	response := acquireSGLResponse()
	defer releaseSGLResponse(response)

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	bifrostResponse := &schemas.BifrostResponse{
		ID:      response.ID,
		Object:  response.Object,
		Choices: response.Choices,
		Model:   response.Model,
		Created: response.Created,
		Usage:   &response.Usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    schemas.SGL,
			RawResponse: rawResponse,
		},
	}

	if params != nil {
		bifrostResponse.ExtraFields.Params = *params
	}

	return bifrostResponse, nil
}

// Embedding is not supported by the SGL provider.
func (provider *SGLProvider) Embedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("embedding", "sgl")
}

// ChatCompletionStream performs a streaming chat completion request to the SGL API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses SGL's OpenAI-compatible streaming format.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *SGLProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
		"stream":   true,
	}, preparedParams)

	// Prepare SGL headers (SGL typically doesn't require authorization, but we include it if provided)
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	// Only add Authorization header if key is provided (SGL can run without auth)
	if key.Value != "" {
		headers["Authorization"] = "Bearer " + key.Value
	}

	// Use shared OpenAI-compatible streaming logic
	return handleOpenAIStreaming(
		ctx,
		provider.streamClient,
		provider.networkConfig.BaseURL+"/v1/chat/completions",
		requestBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		schemas.SGL,
		params,
		postHookRunner,
		provider.logger,
	)
}
