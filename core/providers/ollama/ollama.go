// Package ollama implements the Ollama provider using native Ollama APIs.
// This file contains the main provider implementation for Ollama's native API.
//
// Ollama API Documentation: https://github.com/ollama/ollama/blob/main/docs/api.md
//
// Supported endpoints:
//   - /api/chat - Chat completion
//   - /api/embed - Embeddings
//   - /api/tags - List models
//
// Key differences from OpenAI-compatible API:
//   - Native endpoints instead of /v1/* paths
//   - Newline-delimited JSON streaming instead of SSE
//   - Different request/response structure
//   - Options object for model parameters
package ollama

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// OllamaProvider implements the Provider interface for Ollama's native API.
type OllamaProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// Response pools for efficient memory usage
var (
	ollamaChatResponsePool = sync.Pool{
		New: func() interface{} {
			return &OllamaChatResponse{}
		},
	}
	ollamaEmbeddingResponsePool = sync.Pool{
		New: func() interface{} {
			return &OllamaEmbeddingResponse{}
		},
	}
)

// acquireOllamaChatResponse gets an Ollama chat response from the pool.
func acquireOllamaChatResponse() *OllamaChatResponse {
	resp := ollamaChatResponsePool.Get().(*OllamaChatResponse)
	*resp = OllamaChatResponse{} // Reset the struct
	return resp
}

// releaseOllamaChatResponse returns an Ollama chat response to the pool.
func releaseOllamaChatResponse(resp *OllamaChatResponse) {
	if resp != nil {
		ollamaChatResponsePool.Put(resp)
	}
}

// acquireOllamaEmbeddingResponse gets an Ollama embedding response from the pool.
func acquireOllamaEmbeddingResponse() *OllamaEmbeddingResponse {
	resp := ollamaEmbeddingResponsePool.Get().(*OllamaEmbeddingResponse)
	*resp = OllamaEmbeddingResponse{} // Reset the struct
	return resp
}

// releaseOllamaEmbeddingResponse returns an Ollama embedding response to the pool.
func releaseOllamaEmbeddingResponse(resp *OllamaEmbeddingResponse) {
	if resp != nil {
		ollamaEmbeddingResponsePool.Put(resp)
	}
}

// NewOllamaProvider creates a new Ollama provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewOllamaProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*OllamaProvider, error) {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:         time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:        time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     5000,
		MaxIdleConnDuration: 60 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	// Pre-warm response pools
	for i := 0; i < config.ConcurrencyAndBufferSize.Concurrency; i++ {
		ollamaChatResponsePool.Put(&OllamaChatResponse{})
		ollamaEmbeddingResponsePool.Put(&OllamaEmbeddingResponse{})
	}

	// Configure proxy if provided
	providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)

	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	// Set default BaseURL for local Ollama if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "http://localhost:11434"
	}

	return &OllamaProvider{
		logger:              logger,
		client:              client,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Ollama.
func (provider *OllamaProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Ollama
}

// completeRequest sends a request to Ollama's native API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *OllamaProvider) completeRequest(ctx context.Context, jsonData []byte, url string, key string) ([]byte, time.Duration, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	// Uses Authorization: Bearer <key> for Ollama Cloud / authenticated instances.
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	req.SetBody(jsonData)

	// Send the request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", provider.GetProviderKey(), string(resp.Body())))
		return nil, latency, parseOllamaError(resp, provider.GetProviderKey())
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	// Copy body before releasing response
	bodyCopy := append([]byte(nil), body...)

	return bodyCopy, latency, nil
}

// parseOllamaError parses an error response from Ollama's API.
func parseOllamaError(resp *fasthttp.Response, providerType schemas.ModelProvider) *schemas.BifrostError {
	statusCode := resp.StatusCode()
	body := resp.Body()

	var errorResp OllamaError
	if err := sonic.Unmarshal(body, &errorResp); err == nil && errorResp.Error != "" {
		return providerUtils.NewProviderAPIError(errorResp.Error, nil, statusCode, providerType, nil, nil)
	}

	return providerUtils.NewProviderAPIError(string(body), nil, statusCode, providerType, nil, nil)
}

// ListModels performs a list models request to Ollama's native API.
// Uses the /api/tags endpoint to fetch available models.
func (provider *OllamaProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	// Use first key if available, otherwise empty (for local Ollama)
	var key schemas.Key
	if len(keys) > 0 {
		key = keys[0]
	}

	return provider.listModelsByKey(ctx, key, request)
}

// listModelsByKey performs a list models request for a single key.
func (provider *OllamaProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Build URL - Ollama uses GET /api/tags
	// Use GetPathFromContext to support path overrides
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/api/tags"))
	req.Header.SetMethod(http.MethodGet)

	// Set API key if provided
	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseOllamaError(resp, provider.GetProviderKey())
	}

	// Decode response body (handles gzip, etc.)
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	// Parse response
	var ollamaResponse OllamaListModelsResponse
	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &ollamaResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost format
	response := ollamaResponse.ToBifrostListModelsResponse(provider.GetProviderKey(), key.Models)
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// TextCompletion is not directly supported by Ollama's native API.
// Use ChatCompletion instead for text generation.
func (provider *OllamaProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not directly supported by Ollama's native API.
// Use ChatCompletionStream instead for text generation.
func (provider *OllamaProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to Ollama's native API.
// Uses the /api/chat endpoint with stream=false.
func (provider *OllamaProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	// Convert to Ollama format
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			ollamaReq := ToOllamaChatRequest(request)
			if ollamaReq != nil {
				ollamaReq.Stream = schemas.Ptr(false) // Non-streaming request
			}
			return ollamaReq, nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Make request
	responseBody, latency, bifrostErr := provider.completeRequest(
		ctx,
		jsonData,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/api/chat"),
		key.Value,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Parse response
	response := acquireOllamaChatResponse()
	defer releaseOllamaChatResponse(response)

	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost format
	bifrostResponse := response.ToBifrostChatResponse(request.Model)

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ChatCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ChatCompletionStream performs a streaming chat completion request to Ollama's native API.
// Uses newline-delimited JSON streaming format (not SSE).
func (provider *OllamaProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if the request is a redirect from ResponsesStream to ChatCompletionStream
	isResponsesToChatCompletionsFallback := false
	var responsesStreamState *schemas.ChatToResponsesStreamState
	if ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback) != nil {
		isResponsesToChatCompletionsFallbackValue, ok := ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback).(bool)
		if ok && isResponsesToChatCompletionsFallbackValue {
			isResponsesToChatCompletionsFallback = true
			responsesStreamState = schemas.AcquireChatToResponsesStreamState()
			defer schemas.ReleaseChatToResponsesStreamState(responsesStreamState)
		}
	}

	// Convert to Ollama format with streaming enabled
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			ollamaReq := ToOllamaChatRequest(request)
			if ollamaReq != nil {
				ollamaReq.Stream = schemas.Ptr(true) // Enable streaming
			}
			return ollamaReq, nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true // Enable streaming
	defer fasthttp.ReleaseRequest(req)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + "/api/chat")
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	req.SetBody(jsonData)

	// Make the request with context support
	// NOTE: fasthttp does not natively support context cancellation for streaming requests.
	// MakeRequestWithContext only cancels waiting for the initial request, not the ongoing stream.
	// The scanner loop below includes context cancellation checks to exit early when ctx is cancelled.
	_, bifrostErr = providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, bifrostErr
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, parseOllamaError(resp, provider.GetProviderKey())
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer providerUtils.ReleaseStreamingResponse(resp)

		if resp.BodyStream() == nil {
			bifrostErr := providerUtils.NewBifrostOperationError(
				"Provider returned an empty response",
				fmt.Errorf("provider returned an empty response"),
				provider.GetProviderKey(),
			)
			ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
			return
		}

		scanner := bufio.NewScanner(resp.BodyStream())
		// Increase buffer size for large responses
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		chunkIndex := 0
		startTime := time.Now()
		lastChunkTime := startTime

		for {
			// Check for context cancellation before attempting to scan
			select {
			case <-ctx.Done():
				// Context was cancelled - exit the goroutine
				bifrostErr := &schemas.BifrostError{
					IsBifrostError: true,
					Error: &schemas.ErrorField{
						Type:    schemas.Ptr(schemas.RequestCancelled),
						Message: fmt.Sprintf("Stream cancelled or timed out by context: %v", ctx.Err()),
						Error:   ctx.Err(),
					},
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
				return
			default:
				// Continue to scanner.Scan()
			}

			// Attempt to scan next line
			if !scanner.Scan() {
				// Scanner reached end of stream or encountered an error
				break
			}

			line := scanner.Text()

			// Skip empty lines
			if line == "" {
				continue
			}

			// Parse the JSON chunk (Ollama uses newline-delimited JSON)
			var streamChunk OllamaStreamResponse
			if err := sonic.Unmarshal([]byte(line), &streamChunk); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse Ollama stream chunk: %v", err))
				continue
			}

			// Convert to Bifrost format
			bifrostResponse, isDone := streamChunk.ToBifrostStreamResponse(chunkIndex)
			if bifrostResponse != nil {
				bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
				bifrostResponse.ExtraFields.ModelRequested = request.Model
				bifrostResponse.ExtraFields.ChunkIndex = chunkIndex
				chunkLatencyMs := time.Since(lastChunkTime).Milliseconds()
				bifrostResponse.ExtraFields.Latency = chunkLatencyMs

				if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
					bifrostResponse.ExtraFields.RawResponse = line
				}

				lastChunkTime = time.Now()
				chunkIndex++

				// Handle Responses API fallback conversion
				if isResponsesToChatCompletionsFallback {
					// Convert chat completion stream to responses stream
					spreadResponses := bifrostResponse.ToBifrostResponsesStreamResponse(responsesStreamState)
					for _, responsesResponse := range spreadResponses {
						if responsesResponse == nil {
							continue
						}

						// Update ExtraFields for Responses API
						responsesResponse.ExtraFields.RequestType = schemas.ResponsesStreamRequest
						responsesResponse.ExtraFields.Provider = provider.GetProviderKey()
						responsesResponse.ExtraFields.ModelRequested = request.Model
						responsesResponse.ExtraFields.ChunkIndex = responsesResponse.SequenceNumber

						if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
							responsesResponse.ExtraFields.RawResponse = line
						}

						// Send response chunk
						if isDone && responsesResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
							responsesResponse.ExtraFields.Latency = time.Since(startTime).Milliseconds()
							ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
							providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, responsesResponse, nil, nil), responseChan)
							return
						}

						responsesResponse.ExtraFields.Latency = chunkLatencyMs
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, responsesResponse, nil, nil), responseChan)
					}
				} else {
					// Regular chat completion stream
					if isDone {
						bifrostResponse.ExtraFields.Latency = time.Since(startTime).Milliseconds()
						ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, bifrostResponse, nil, nil, nil), responseChan)
						return
					}

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, bifrostResponse, nil, nil, nil), responseChan)
				}
			}
		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading Ollama stream: %v", err))
			requestType := schemas.ChatCompletionStreamRequest
			if isResponsesToChatCompletionsFallback {
				requestType = schemas.ResponsesStreamRequest
			}
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, requestType, provider.GetProviderKey(), request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// Responses performs a responses request to Ollama's API.
// Falls back to ChatCompletion with conversion.
func (provider *OllamaProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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

// ResponsesStream performs a streaming responses request to Ollama's API.
// Falls back to ChatCompletionStream with conversion.
func (provider *OllamaProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return provider.ChatCompletionStream(
		ctx,
		postHookRunner,
		key,
		request.ToChatRequest(),
	)
}

// Embedding performs an embedding request to Ollama's native API.
// Uses the /api/embed endpoint.
func (provider *OllamaProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	// Convert to Ollama format
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToOllamaEmbeddingRequest(request), nil },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Make request
	responseBody, latency, bifrostErr := provider.completeRequest(
		ctx,
		jsonData,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/api/embed"),
		key.Value,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Parse response
	response := acquireOllamaEmbeddingResponse()
	defer releaseOllamaEmbeddingResponse(response)

	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost format
	bifrostResponse := response.ToBifrostEmbeddingResponse(request.Model)

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.EmbeddingRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// Speech is not supported by the Ollama provider.
func (provider *OllamaProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Ollama provider.
func (provider *OllamaProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Ollama provider.
func (provider *OllamaProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Ollama provider.
func (provider *OllamaProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by Ollama provider.
func (provider *OllamaProvider) BatchCreate(_ context.Context, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by Ollama provider.
func (provider *OllamaProvider) BatchList(_ context.Context, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by Ollama provider.
func (provider *OllamaProvider) BatchRetrieve(_ context.Context, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by Ollama provider.
func (provider *OllamaProvider) BatchCancel(_ context.Context, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchResults is not supported by Ollama provider.
func (provider *OllamaProvider) BatchResults(_ context.Context, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload is not supported by Ollama provider.
func (provider *OllamaProvider) FileUpload(_ context.Context, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by Ollama provider.
func (provider *OllamaProvider) FileList(_ context.Context, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by Ollama provider.
func (provider *OllamaProvider) FileRetrieve(_ context.Context, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by Ollama provider.
func (provider *OllamaProvider) FileDelete(_ context.Context, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by Ollama provider.
func (provider *OllamaProvider) FileContent(_ context.Context, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}
