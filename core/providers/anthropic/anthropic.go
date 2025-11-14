package anthropic

import (
	"bufio"
	"context"
	"errors"
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

// AnthropicProvider implements the Provider interface for Anthropic's Claude API.
type AnthropicProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	apiVersion           string                        // API version for the provider
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// anthropicChatResponsePool provides a pool for Anthropic chat response objects.
var anthropicChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicMessageResponse{}
	},
}

// anthropicTextResponsePool provides a pool for Anthropic text response objects.
var anthropicTextResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicTextResponse{}
	},
}

// AcquireAnthropicChatResponse gets an Anthropic chat response from the pool.
func AcquireAnthropicChatResponse() *AnthropicMessageResponse {
	resp := anthropicChatResponsePool.Get().(*AnthropicMessageResponse)
	*resp = AnthropicMessageResponse{} // Reset the struct
	return resp
}

// ReleaseAnthropicChatResponse returns an Anthropic chat response to the pool.
func ReleaseAnthropicChatResponse(resp *AnthropicMessageResponse) {
	if resp != nil {
		anthropicChatResponsePool.Put(resp)
	}
}

// acquireAnthropicTextResponse gets an Anthropic text response from the pool.
func acquireAnthropicTextResponse() *AnthropicTextResponse {
	resp := anthropicTextResponsePool.Get().(*AnthropicTextResponse)
	*resp = AnthropicTextResponse{} // Reset the struct
	return resp
}

// releaseAnthropicTextResponse returns an Anthropic text response to the pool.
func releaseAnthropicTextResponse(resp *AnthropicTextResponse) {
	if resp != nil {
		anthropicTextResponsePool.Put(resp)
	}
}

// NewAnthropicProvider creates a new Anthropic provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewAnthropicProvider(config *schemas.ProviderConfig, logger schemas.Logger) *AnthropicProvider {
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
		anthropicTextResponsePool.Put(&AnthropicTextResponse{})
		anthropicChatResponsePool.Put(&AnthropicMessageResponse{})
	}

	// Configure proxy if provided
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.anthropic.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &AnthropicProvider{
		logger:               logger,
		client:               client,
		apiVersion:           "2023-06-01",
		networkConfig:        config.NetworkConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}
}

// GetProviderKey returns the provider identifier for Anthropic.
func (provider *AnthropicProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Anthropic, provider.customProviderConfig)
}

// buildRequestURL constructs the full request URL using the provider's configuration.
func (provider *AnthropicProvider) buildRequestURL(ctx context.Context, defaultPath string, requestType schemas.RequestType) string {
	return provider.networkConfig.BaseURL + providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
}

// completeRequest sends a request to Anthropic's API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *AnthropicProvider) completeRequest(ctx context.Context, jsonData []byte, url string, key string) ([]byte, time.Duration, *schemas.BifrostError) {
	// Create the request with the JSON body
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	// Can be empty in case of passthrough or keyless custom provider
	if key != "" {
		req.Header.Set("x-api-key", key)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)
	req.SetBody(jsonData)

	// Send the request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", provider.GetProviderKey(), string(resp.Body())))

		var errorResp AnthropicError

		bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Type = &errorResp.Error.Type
		bifrostErr.Error.Message = errorResp.Error.Message

		return nil, latency, bifrostErr
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	// Read the response body and copy it before releasing the response
	// to avoid use-after-free since respBody references fasthttp's internal buffer
	bodyCopy := append([]byte(nil), body...)

	return bodyCopy, latency, nil
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *AnthropicProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Build URL using centralized URL construction
	req.SetRequestURI(provider.buildRequestURL(ctx, fmt.Sprintf("/v1/models?limit=%d", schemas.DefaultPageSize), schemas.ListModelsRequest))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("x-api-key", key.Value)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		var errorResp AnthropicError
		bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
		bifrostErr.Error.Type = &errorResp.Error.Type
		bifrostErr.Error.Message = errorResp.Error.Message
		return nil, bifrostErr
	}

	// Parse Anthropic's response
	var anthropicResponse AnthropicListModelsResponse
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &anthropicResponse, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	response := anthropicResponse.ToBifrostListModelsResponse(provider.GetProviderKey())
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ListModels performs a list models request to Anthropic's API.
// It fetches models using all provided keys and aggregates the results.
// Uses a best-effort approach: continues with remaining keys even if some fail.
// Requests are made concurrently for improved performance.
func (provider *AnthropicProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
		return provider.listModelsByKey(ctx, schemas.Key{}, request)
	}
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
		provider.logger,
	)
}

// TextCompletion performs a text completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.TextCompletionRequest); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToAnthropicTextCompletionRequest(request), nil },
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	// Use struct directly for JSON marshaling
	responseBody, latency, err := provider.completeRequest(ctx, jsonData, provider.buildRequestURL(ctx, "/v1/complete", schemas.TextCompletionRequest), key.Value)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := acquireAnthropicTextResponse()
	defer releaseAnthropicTextResponse(response)

	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse := response.ToBifrostTextCompletionResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.TextCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// TextCompletionStream performs a streaming text completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *AnthropicProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToAnthropicChatCompletionRequest(request), nil },
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	// Use struct directly for JSON marshaling
	responseBody, latency, err := provider.completeRequest(ctx, jsonData, provider.buildRequestURL(ctx, "/v1/messages", schemas.ChatCompletionRequest), key.Value)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := AcquireAnthropicChatResponse()
	defer ReleaseAnthropicChatResponse(response)

	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	bifrostResponse := response.ToBifrostChatResponse()

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

// ChatCompletionStream performs a streaming chat completion request to the Anthropic API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := ToAnthropicChatCompletionRequest(request)
			if reqBody != nil {
				reqBody.Stream = schemas.Ptr(true)
			}
			return reqBody, nil
		},
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	// Prepare Anthropic headers
	headers := map[string]string{
		"Content-Type":      "application/json",
		"anthropic-version": provider.apiVersion,
		"Accept":            "text/event-stream",
		"Cache-Control":     "no-cache",
	}
	if key.Value != "" {
		headers["x-api-key"] = key.Value
	}

	// Use shared Anthropic streaming logic
	return HandleAnthropicChatCompletionStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/messages", schemas.ChatCompletionStreamRequest),
		jsonData,
		headers,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		provider.logger,
	)
}

// HandleAnthropicChatCompletionStreaming handles streaming for Anthropic-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE event format.
func HandleAnthropicChatCompletionStreaming(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	jsonBody []byte,
	headers map[string]string,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerType schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	var err error
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true // Initialize for streaming
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	req.SetBody(jsonBody)

	// Make the request
	err = client.Do(req, resp)
	if err != nil {
		defer providerUtils.ReleaseStreamingResponse(resp)
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
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerType)
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerType)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, parseStreamAnthropicError(resp, providerType)
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
				providerType,
			)
			ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
			return
		}

		scanner := bufio.NewScanner(resp.BodyStream())
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		chunkIndex := 0

		startTime := time.Now()
		lastChunkTime := startTime

		// Track minimal state needed for response format
		var messageID string
		var modelName string
		var usage *schemas.BifrostLLMUsage
		var finishReason *string

		// Track SSE event parsing state
		var eventType string
		var eventData string

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE event - track event type and data separately
			if after, ok := strings.CutPrefix(line, "event: "); ok {
				eventType = after
				continue
			} else if strings.HasPrefix(line, "data: ") {
				eventData = strings.TrimPrefix(line, "data: ")
			} else {
				continue
			}

			// Skip if we don't have both event type and data
			if eventType == "" || eventData == "" {
				continue
			}

			var event AnthropicStreamEvent
			if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse message_start event: %v", err))
				continue
			}

			if event.Type == AnthropicStreamEventTypeMessageStart && event.Message != nil && event.Message.ID != "" {
				messageID = event.Message.ID
			}

			if event.Usage != nil {
				usage = &schemas.BifrostLLMUsage{
					PromptTokens:     event.Usage.InputTokens,
					CompletionTokens: event.Usage.OutputTokens,
					TotalTokens:      event.Usage.InputTokens + event.Usage.OutputTokens,
				}
			}
			if event.Delta != nil && event.Delta.StopReason != nil {
				mappedReason := ConvertAnthropicFinishReasonToBifrost(*event.Delta.StopReason)
				finishReason = &mappedReason
			}
			if event.Message != nil {
				// Handle different event types
				modelName = event.Message.Model
			}

			response, bifrostErr, isLastChunk := event.ToBifrostChatCompletionStream()
			if bifrostErr != nil {
				bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
					RequestType:    schemas.ChatCompletionStreamRequest,
					Provider:       providerType,
					ModelRequested: modelName,
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				break
			}
			if response != nil {
				response.ID = messageID
				response.ExtraFields = schemas.BifrostResponseExtraFields{
					RequestType:    schemas.ChatCompletionStreamRequest,
					Provider:       providerType,
					ModelRequested: modelName,
					ChunkIndex:     chunkIndex,
					Latency:        time.Since(lastChunkTime).Milliseconds(),
				}
				lastChunkTime = time.Now()
				chunkIndex++

				if sendBackRawResponse {
					response.ExtraFields.RawResponse = eventData
				}

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil), responseChan)
			}

			if isLastChunk {
				break
			}

			// Reset for next event
			eventType = ""
			eventData = ""
		}

		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading %s stream: %v", providerType, err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerType, modelName, logger)
		} else {
			response := providerUtils.CreateBifrostChatCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, schemas.ChatCompletionStreamRequest, providerType, modelName)
			response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
			ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil), responseChan)
		}
	}()

	return responseChan, nil
}

// Responses performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToAnthropicResponsesRequest(request), nil },
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	// Use struct directly for JSON marshaling
	responseBody, latency, err := provider.completeRequest(ctx, jsonData, provider.buildRequestURL(ctx, "/v1/messages", schemas.ResponsesRequest), key.Value)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := AcquireAnthropicChatResponse()
	defer ReleaseAnthropicChatResponse(response)

	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	bifrostResponse := response.ToBifrostResponsesResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ResponsesRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ResponsesStream performs a streaming responses request to the Anthropic API.
func (provider *AnthropicProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := ToAnthropicResponsesRequest(request)
			if reqBody != nil {
				reqBody.Stream = schemas.Ptr(true)
			}
			return reqBody, nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true

	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/messages", schemas.ResponsesStreamRequest))
	req.Header.SetContentType("application/json")
	req.Header.Set("anthropic-version", provider.apiVersion)
	if key.Value != "" {
		req.Header.Set("x-api-key", key.Value)
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	// Set body
	req.SetBody(jsonBody)

	// Make the request
	err := provider.client.Do(req, resp)
	if err != nil {
		defer providerUtils.ReleaseStreamingResponse(resp)
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
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, provider.GetProviderKey())
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, provider.GetProviderKey())
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, parseStreamAnthropicError(resp, provider.GetProviderKey())
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer providerUtils.ReleaseStreamingResponse(resp)
		defer close(responseChan)

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
		chunkIndex := 0

		startTime := time.Now()
		lastChunkTime := startTime

		// Track minimal state needed for response format
		var usage *schemas.ResponsesResponseUsage

		// Create stream state for stateful conversions
		streamState := acquireAnthropicResponsesStreamState()
		defer releaseAnthropicResponsesStreamState(streamState)

		// Track SSE event parsing state
		var eventType string
		var eventData string

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE event - track event type and data separately
			if after, ok := strings.CutPrefix(line, "event: "); ok {
				eventType = after
				continue
			} else if strings.HasPrefix(line, "data: ") {
				eventData = strings.TrimPrefix(line, "data: ")
			} else {
				continue
			}

			// Skip if we don't have both event type and data
			if eventType == "" || eventData == "" {
				continue
			}

			var event AnthropicStreamEvent
			if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse message_start event: %v", err))
				continue
			}

			// Note: response.created and response.in_progress are now emitted by ToBifrostResponsesStream
			// from the message_start event, so we don't need to call them manually here

			if event.Usage != nil {
				usage = &schemas.ResponsesResponseUsage{
					InputTokens:  event.Usage.InputTokens,
					OutputTokens: event.Usage.OutputTokens,
					TotalTokens:  event.Usage.InputTokens + event.Usage.OutputTokens,
				}
			}

			responses, bifrostErr, isLastChunk := event.ToBifrostResponsesStream(chunkIndex, streamState)
			if bifrostErr != nil {
				bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
					RequestType:    schemas.ResponsesStreamRequest,
					Provider:       provider.GetProviderKey(),
					ModelRequested: request.Model,
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
				break
			}
			// Handle each response in the slice
			for i, response := range responses {
				if response != nil {
					response.ExtraFields = schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ResponsesStreamRequest,
						Provider:       provider.GetProviderKey(),
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					}
					lastChunkTime = time.Now()
					chunkIndex++

					if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
						response.ExtraFields.RawResponse = eventData
					}

					if isLastChunk && i == len(responses)-1 {
						if response.Response == nil {
							response.Response = &schemas.BifrostResponsesResponse{}
						}
						if usage != nil {
							response.Response.Usage = usage
						}
						response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
						ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil), responseChan)
						return
					}
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil), responseChan)
				}
			}

			// Reset for next event
			eventType = ""
			eventData = ""
		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading %s stream: %v", provider.GetProviderKey(), err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, provider.GetProviderKey(), request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// Embedding is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Embedding(ctx context.Context, key schemas.Key, input *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Speech is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// parseStreamAnthropicError parses Anthropic streaming error responses.
func parseStreamAnthropicError(resp *fasthttp.Response, providerType schemas.ModelProvider) *schemas.BifrostError {
	statusCode := resp.StatusCode()
	body := resp.Body()
	return providerUtils.NewProviderAPIError(string(body), nil, statusCode, providerType, nil, nil)
}
