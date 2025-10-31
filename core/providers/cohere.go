// Package providers implements various LLM providers and their utility functions.
// This file contains the Cohere provider implementation.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"net/http"
	"net/url"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	cohere "github.com/maximhq/bifrost/core/schemas/providers/cohere"
	"github.com/valyala/fasthttp"
)

// cohereResponsePool provides a pool for Cohere v2 response objects.
var cohereResponsePool = sync.Pool{
	New: func() interface{} {
		return &cohere.CohereChatResponse{}
	},
}

// cohereEmbeddingResponsePool provides a pool for Cohere embedding response objects.
var cohereEmbeddingResponsePool = sync.Pool{
	New: func() interface{} {
		return &cohere.CohereEmbeddingResponse{}
	},
}

// acquireCohereEmbeddingResponse gets a Cohere embedding response from the pool and resets it.
func acquireCohereEmbeddingResponse() *cohere.CohereEmbeddingResponse {
	resp := cohereEmbeddingResponsePool.Get().(*cohere.CohereEmbeddingResponse)
	*resp = cohere.CohereEmbeddingResponse{} // Reset the struct
	return resp
}

// releaseCohereEmbeddingResponse returns a Cohere embedding response to the pool.
func releaseCohereEmbeddingResponse(resp *cohere.CohereEmbeddingResponse) {
	if resp != nil {
		cohereEmbeddingResponsePool.Put(resp)
	}
}

// acquireCohereResponse gets a Cohere v2 response from the pool and resets it.
func acquireCohereResponse() *cohere.CohereChatResponse {
	resp := cohereResponsePool.Get().(*cohere.CohereChatResponse)
	*resp = cohere.CohereChatResponse{} // Reset the struct
	return resp
}

// releaseCohereResponse returns a Cohere v2 response to the pool.
func releaseCohereResponse(resp *cohere.CohereChatResponse) {
	if resp != nil {
		cohereResponsePool.Put(resp)
	}
}

// CohereProvider implements the Provider interface for Cohere.
type CohereProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	streamClient         *http.Client                  // HTTP client for streaming requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewCohereProvider creates a new Cohere provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts and connection limits.
func NewCohereProvider(config *schemas.ProviderConfig, logger schemas.Logger) *CohereProvider {
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
		cohereResponsePool.Put(&cohere.CohereChatResponse{})
		cohereEmbeddingResponsePool.Put(&cohere.CohereEmbeddingResponse{})
	}

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.cohere.ai"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &CohereProvider{
		logger:               logger,
		client:               client,
		streamClient:         streamClient,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Cohere.
func (provider *CohereProvider) GetProviderKey() schemas.ModelProvider {
	return getProviderName(schemas.Cohere, provider.customProviderConfig)
}

// completeRequest sends a request to Cohere's API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *CohereProvider) completeRequest(ctx context.Context, requestBody interface{}, url string, key string) ([]byte, time.Duration, *schemas.BifrostError) {
	// Marshal the request body
	jsonData, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, 0, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, provider.GetProviderKey())
	}

	// Create the request with the JSON body
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	req.SetBody(jsonData)

	// Send the request
	latency, bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", provider.GetProviderKey(), string(resp.Body())))

		var errorResp cohere.CohereError

		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Type = &errorResp.Type
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = errorResp.Message
		if errorResp.Code != nil {
			bifrostErr.Error.Code = errorResp.Code
		}

		return nil, latency, bifrostErr
	}

	// Read the response body and copy it before releasing the response
	// to avoid use-after-free since resp.Body() references fasthttp's internal buffer
	bodyCopy := append([]byte(nil), resp.Body()...)

	return bodyCopy, latency, nil
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *CohereProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	setExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	// Build query parameters
	params := url.Values{}
	params.Set("page_size", strconv.Itoa(schemas.DefaultPageSize))
	if request.ExtraParams != nil {
		if endpoint, ok := request.ExtraParams["endpoint"].(string); ok && endpoint != "" {
			params.Set("endpoint", endpoint)
		}
		if defaultOnly, ok := request.ExtraParams["default_only"].(bool); ok && defaultOnly {
			params.Set("default_only", "true")
		}
	}

	// Build URL
	req.SetRequestURI(fmt.Sprintf("%s/v1/models?%s", provider.networkConfig.BaseURL, params.Encode()))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)

	// Make request
	latency, bifrostErr := makeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		var errorResp cohere.CohereError
		bifrostErr := handleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Message = errorResp.Message
		return nil, bifrostErr
	}

	// Copy response body before releasing
	responseBody := append([]byte(nil), resp.Body()...)

	// Parse Cohere list models response
	var cohereResponse cohere.CohereListModelsResponse
	rawResponse, bifrostErr := handleProviderResponse(responseBody, &cohereResponse, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert Cohere v2 response to Bifrost response
	response := cohereResponse.ToBifrostListModelsResponse(providerName)

	response.ExtraFields.Latency = latency.Milliseconds()

	if provider.sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ListModels performs a list models request to Cohere's API.
// Requests are made concurrently for improved performance.
func (provider *CohereProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := checkOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	return handleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
		provider.logger,
	)
}

// TextCompletion is not supported by the Cohere provider.
// Returns an error indicating that text completion is not supported.
func (provider *CohereProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion", "cohere")
}

// TextCompletionStream performs a streaming text completion request to Cohere's API.
// It formats the request, sends it to Cohere, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *CohereProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("text completion stream", "cohere")
}

// ChatCompletion performs a chat completion request to the Cohere API using v2 converter.
// It formats the request, sends it to Cohere, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *CohereProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed
	if err := checkOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Convert to Cohere v2 request
	reqBody := cohere.ToCohereChatCompletionRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("chat completion input is not provided", nil, providerName)
	}

	responseBody, latency, err := provider.completeRequest(ctx, reqBody, provider.networkConfig.BaseURL+"/v2/chat", key.Value)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireCohereResponse()
	defer releaseCohereResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse := response.ToBifrostChatResponse(request.Model)

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ChatCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ChatCompletionStream performs a streaming chat completion request to the Cohere API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *CohereProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if chat completion stream is allowed
	if err := checkOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	// Convert to Cohere v2 request and add streaming
	reqBody := cohere.ToCohereChatCompletionRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("chat completion input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.networkConfig.BaseURL+"/v2/chat", bytes.NewReader(jsonBody))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Make the request
	resp, err := provider.streamClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerName, resp.StatusCode), fmt.Errorf("%s", string(body)), resp.StatusCode, providerName, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		chunkIndex := 0

		startTime := time.Now()
		lastChunkTime := startTime

		var responseID string

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				jsonData := strings.TrimPrefix(line, "data: ")

				// Handle [DONE] marker
				if strings.TrimSpace(jsonData) == "[DONE]" {
					provider.logger.Debug("Received [DONE] marker, ending stream")
					return
				}

				// Parse the unified streaming event
				var event cohere.CohereStreamEvent
				if err := sonic.Unmarshal([]byte(jsonData), &event); err != nil {
					provider.logger.Warn(fmt.Sprintf("Failed to parse stream event: %v", err))
					continue
				}

				chunkIndex++

				// Extract response ID from message-start events
				if event.Type == cohere.StreamEventMessageStart && event.ID != nil {
					responseID = *event.ID
				}

				response, bifrostErr, isLastChunk := event.ToBifrostChatCompletionStream()
				if response != nil {
					response.ID = responseID
					response.ExtraFields = schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ChatCompletionStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					}
					lastChunkTime = time.Now()
					chunkIndex++

					if provider.sendBackRawResponse {
						response.ExtraFields.RawResponse = jsonData
					}

					processAndSendResponse(ctx, postHookRunner, getBifrostResponseForStreamResponse(nil, response, nil, nil, nil), responseChan, provider.logger)
					if isLastChunk {
						break
					}
				}
				if bifrostErr != nil {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						RequestType:    schemas.ChatCompletionStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
					}

					processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
					break
				}
			}
		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			processAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// Responses performs a responses request to the Cohere API using v2 converter.
func (provider *CohereProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed
	if err := checkOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Convert to Cohere v2 request
	reqBody := cohere.ToCohereResponsesRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("responses input is not provided", nil, providerName)
	}

	responseBody, latency, err := provider.completeRequest(ctx, reqBody, provider.networkConfig.BaseURL+"/v2/chat", key.Value)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireCohereResponse()
	defer releaseCohereResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse := response.ToBifrostResponsesResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ResponsesRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ResponsesStream performs a streaming responses request to the Cohere API.
func (provider *CohereProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if responses stream is allowed
	if err := checkOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	// Convert to Cohere v2 request and add streaming
	reqBody := cohere.ToCohereResponsesRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("responses input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, newBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerName)
	}

	// Create HTTP request for streaming
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.networkConfig.BaseURL+"/v2/chat", bytes.NewReader(jsonBody))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, newBifrostOperationError(schemas.ErrProviderRequest, err, providerName)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key.Value)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Set any extra headers from network config
	setExtraHeadersHTTP(req, provider.networkConfig.ExtraHeaders, nil)

	// Make the request
	resp, err := provider.streamClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderRequest,
				Error:   err,
			},
		}
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerName, resp.StatusCode), fmt.Errorf("%s", string(body)), resp.StatusCode, providerName, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		chunkIndex := 0

		startTime := time.Now()
		lastChunkTime := startTime

		// Track SSE event parsing state
		var eventData string

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE event - track event data
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				eventData = after
			} else {
				continue
			}

			// Skip if we don't have event data
			if eventData == "" {
				continue
			}

			// Handle [DONE] marker
			if strings.TrimSpace(eventData) == "[DONE]" {
				provider.logger.Debug("Received [DONE] marker, ending stream")
				return
			}

			// Parse the unified streaming event
			var event cohere.CohereStreamEvent
			if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream event: %v", err))
				continue
			}

			if chunkIndex == 0 {
				sendCreatedEventResponsesChunk(ctx, postHookRunner, providerName, request.Model, startTime, responseChan, provider.logger)
				sendInProgressEventResponsesChunk(ctx, postHookRunner, providerName, request.Model, startTime, responseChan, provider.logger)
				chunkIndex = 2
			}

			response, bifrostErr, isLastChunk := event.ToBifrostResponsesStream(chunkIndex)
			if response != nil {
				response.ExtraFields = schemas.BifrostResponseExtraFields{
					RequestType:    schemas.ResponsesStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
					ChunkIndex:     chunkIndex,
					Latency:        time.Since(lastChunkTime).Milliseconds(),
				}

				lastChunkTime = time.Now()
				chunkIndex++

				if provider.sendBackRawResponse {
					response.ExtraFields.RawResponse = eventData
				}

				if isLastChunk {
					if response.Response == nil {
						response.Response = &schemas.BifrostResponsesResponse{}
					}
					response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					handleStreamEndWithSuccess(ctx, getBifrostResponseForStreamResponse(nil, nil, response, nil, nil), postHookRunner, responseChan, provider.logger)
					break
				}
				processAndSendResponse(ctx, postHookRunner, getBifrostResponseForStreamResponse(nil, nil, response, nil, nil), responseChan, provider.logger)
			}
			if bifrostErr != nil {
				bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
					RequestType:    schemas.ResponsesStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
				}

				processAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
				break
			}
		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading %s stream: %v", providerName, err))
			processAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// Embedding generates embeddings for the given input text(s) using the Cohere API.
// Supports Cohere's embedding models and returns a BifrostResponse containing the embedding(s).
func (provider *CohereProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	// Check if embedding is allowed
	if err := checkOperationAllowed(schemas.Cohere, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create Bifrost request for conversion
	reqBody := cohere.ToCohereEmbeddingRequest(request)
	if reqBody == nil {
		return nil, newBifrostOperationError("embedding input is not provided", nil, providerName)
	}

	responseBody, latency, err := provider.completeRequest(ctx, reqBody, provider.networkConfig.BaseURL+"/v2/embed", key.Value)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireCohereEmbeddingResponse()
	defer releaseCohereEmbeddingResponse(response)

	rawResponse, bifrostErr := handleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse := response.ToBifrostEmbeddingResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.EmbeddingRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// Speech is not supported by the Cohere provider.
func (provider *CohereProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech", "cohere")
}

// SpeechStream is not supported by the Cohere provider.
func (provider *CohereProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("speech stream", "cohere")
}

// Transcription is not supported by the Cohere provider.
func (provider *CohereProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription", "cohere")
}

// TranscriptionStream is not supported by the Cohere provider.
func (provider *CohereProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, newUnsupportedOperationError("transcription stream", "cohere")
}
