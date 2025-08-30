// Package providers implements various LLM providers and their utility functions.
// This file contains the Gemini provider implementation.
package providers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type GeminiProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	streamClient        *http.Client          // HTTP client for streaming requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewGeminiProvider creates a new Gemini provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewGeminiProvider(config *schemas.ProviderConfig, logger schemas.Logger) *GeminiProvider {
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

	// Configure proxy if provided
	client = configureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://generativelanguage.googleapis.com/v1beta/openai/"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &GeminiProvider{
		logger:              logger,
		client:              client,
		streamClient:        streamClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawResponse: config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Gemini.
func (provider *GeminiProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Gemini
}

// TextCompletion is not supported by the Gemini provider.
func (provider *GeminiProvider) TextCompletion(ctx context.Context, model string, key schemas.Key, text string, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion", "Gemini")
}

// ChatCompletion performs a chat completion request to the Gemini API.
func (provider *GeminiProvider) ChatCompletion(ctx context.Context, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
	}, preparedParams)

	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, schemas.Gemini)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + "/chat/completions")
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
		provider.logger.Debug(fmt.Sprintf("error from Gemini provider: %s", string(resp.Body())))

		var errorResp map[string]interface{}
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = fmt.Sprintf("Gemini error: %v", errorResp)
		return nil, bifrostErr
	}

	responseBody := resp.Body()

	// Pre-allocate response structs from pools
	// response := acquireGeminiResponse()
	// defer releaseGeminiResponse(response)
	response := &schemas.BifrostResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = schemas.Gemini

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	if params != nil {
		response.ExtraFields.Params = *params
	}

	return response, nil
}

// ChatCompletionStream performs a streaming chat completion request to the Gemini API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Gemini's OpenAI-compatible streaming format.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *GeminiProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, messages []schemas.BifrostMessage, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	formattedMessages, preparedParams := prepareOpenAIChatRequest(messages, params)

	requestBody := mergeConfig(map[string]interface{}{
		"model":    model,
		"messages": formattedMessages,
		"stream":   true,
	}, preparedParams)

	// Prepare Gemini headers
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
		provider.networkConfig.BaseURL+"/chat/completions",
		requestBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		schemas.Gemini,
		params,
		postHookRunner,
		provider.logger,
	)
}

// Embedding performs an embedding request to the Gemini API.
func (provider *GeminiProvider) Embedding(ctx context.Context, model string, key schemas.Key, input *schemas.EmbeddingInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	// Prepare request body with base parameters
	requestBody := map[string]interface{}{
		"model": model,
		"input": input.Texts,
	}

	// Merge any additional parameters
	if params != nil {
		// Map standard parameters
		if params.EncodingFormat != nil {
			requestBody["encoding_format"] = *params.EncodingFormat
		}
		if params.Dimensions != nil {
			requestBody["dimensions"] = *params.Dimensions
		}
		if params.User != nil {
			requestBody["user"] = *params.User
		}

		// Merge any extra parameters
		if params.ExtraParams != nil {
			requestBody = mergeConfig(requestBody, params.ExtraParams)
		}
	}

	// Use the shared embedding request handler
	return handleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/embeddings",
		requestBody,
		key,
		params,
		provider.networkConfig.ExtraHeaders,
		schemas.Gemini,
		provider.sendBackRawResponse,
		provider.logger,
	)
}

func (provider *GeminiProvider) Speech(ctx context.Context, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "Gemini")
}

func (provider *GeminiProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.SpeechInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "Gemini")
}

func (provider *GeminiProvider) Transcription(ctx context.Context, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "Gemini")
}

func (provider *GeminiProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, model string, key schemas.Key, input *schemas.TranscriptionInput, params *schemas.ModelParameters) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "Gemini")
}
