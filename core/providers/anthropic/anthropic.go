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
		MaxConnsPerHost:     10000,
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

// completeRequest sends a request to Anthropic's API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *AnthropicProvider) completeRequest(ctx context.Context, requestBody interface{}, url string, key string) ([]byte, time.Duration, *schemas.BifrostError) {
	// Marshal the request body
	jsonData, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, 0, providerUtils.NewBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, provider.GetProviderKey())
	}

	// Create the request with the JSON body
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("x-api-key", key)
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

	// Read the response body and copy it before releasing the response
	// to avoid use-after-free since resp.Body() references fasthttp's internal buffer
	bodyCopy := append([]byte(nil), resp.Body()...)

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
	providerUtils.SetExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)

	// Build URL using centralized URL construction
	req.SetRequestURI(fmt.Sprintf("%s/v1/models?limit=%d", provider.networkConfig.BaseURL, schemas.DefaultPageSize))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	req.Header.Set("x-api-key", key.Value)
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
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &anthropicResponse, provider.sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	response := anthropicResponse.ToBifrostListModelsResponse(provider.GetProviderKey())
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if provider.sendBackRawResponse {
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
	reqBody := ToAnthropicTextCompletionRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("text completion input is not provided", nil, provider.GetProviderKey())
	}

	// Use struct directly for JSON marshaling
	responseBody, latency, err := provider.completeRequest(ctx, reqBody, provider.networkConfig.BaseURL+"/v1/complete", key.Value)
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
	if provider.sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// TextCompletionStream performs a streaming text completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *AnthropicProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("text completion stream", string(provider.GetProviderKey()))
}

// ChatCompletion performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	reqBody := ToAnthropicChatCompletionRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("chat completion input is not provided", nil, provider.GetProviderKey())
	}

	// Use struct directly for JSON marshaling
	responseBody, latency, err := provider.completeRequest(ctx, reqBody, provider.networkConfig.BaseURL+"/v1/messages", key.Value)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := AcquireAnthropicChatResponse()
	defer ReleaseAnthropicChatResponse(response)

	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, provider.sendBackRawResponse)
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
	if provider.sendBackRawResponse {
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
	reqBody := ToAnthropicChatCompletionRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("failed to convert request", fmt.Errorf("conversion returned nil"), provider.GetProviderKey())
	}
	reqBody.Stream = schemas.Ptr(true)

	// Prepare Anthropic headers
	headers := map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         key.Value,
		"anthropic-version": provider.apiVersion,
		"Accept":            "text/event-stream",
		"Cache-Control":     "no-cache",
	}

	// Use shared Anthropic streaming logic
	return HandleAnthropicChatCompletionStreaming(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/v1/messages",
		reqBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		provider.sendBackRawResponse,
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
	requestBody interface{},
	headers map[string]string,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerType schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	jsonBody, err := sonic.Marshal(requestBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, providerType)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true // Initialize for streaming
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(req, extraHeaders, nil)

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	req.SetBody(jsonBody)

	// Make the request
	err = client.Do(req, resp)
	if err != nil {
		defer fasthttp.ReleaseResponse(resp)
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
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequest, err, providerType)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer fasthttp.ReleaseResponse(resp)
		return nil, parseStreamAnthropicError(resp, providerType)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer fasthttp.ReleaseResponse(resp)

		if resp.BodyStream() == nil {
			bifrostErr := providerUtils.NewBifrostOperationError(
				"Provider returned an empty response",
				fmt.Errorf("provider returned an empty response"),
				providerType,
			)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
			return
		}

		scanner := bufio.NewScanner(resp.BodyStream())
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

				if isLastChunk {
					break
				}
			}
			if bifrostErr != nil {
				bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
					RequestType:    schemas.ChatCompletionStreamRequest,
					Provider:       providerType,
					ModelRequested: modelName,
				}

				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
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
			providerUtils.HandleStreamEndWithSuccess(ctx, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil), postHookRunner, responseChan)
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
	reqBody := ToAnthropicResponsesRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("responses input is not provided", nil, provider.GetProviderKey())
	}

	// Use struct directly for JSON marshaling
	responseBody, latency, err := provider.completeRequest(ctx, reqBody, provider.networkConfig.BaseURL+"/v1/messages", key.Value)
	if err != nil {
		return nil, err
	}

	// Create response object from pool
	response := AcquireAnthropicChatResponse()
	defer ReleaseAnthropicChatResponse(response)

	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, provider.sendBackRawResponse)
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
	if provider.sendBackRawResponse {
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
	reqBody := ToAnthropicResponsesRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("failed to convert request", fmt.Errorf("conversion returned nil"), provider.GetProviderKey())
	}
	reqBody.Stream = schemas.Ptr(true)

	// Prepare Anthropic headers
	headers := map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         key.Value,
		"anthropic-version": provider.apiVersion,
		"Accept":            "text/event-stream",
		"Cache-Control":     "no-cache",
	}

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderJSONMarshaling, err, provider.GetProviderKey())
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true

	defer fasthttp.ReleaseRequest(req)

	url := fmt.Sprintf("%s/v1/messages", provider.networkConfig.BaseURL)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(req, provider.networkConfig.ExtraHeaders, nil)
	// Set body
	req.SetBody(jsonBody)

	// Make the request
	_, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		defer fasthttp.ReleaseResponse(resp)
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
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequest, err, provider.GetProviderKey())
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer fasthttp.ReleaseResponse(resp)
		return nil, parseStreamAnthropicError(resp, provider.GetProviderKey())
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer fasthttp.ReleaseResponse(resp)
		defer close(responseChan)

		if resp.BodyStream() == nil {
			bifrostErr := providerUtils.NewBifrostOperationError(
				"Provider returned an empty response",
				fmt.Errorf("provider returned an empty response"),
				provider.GetProviderKey(),
			)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
			return
		}

		scanner := bufio.NewScanner(resp.BodyStream())
		chunkIndex := 0

		startTime := time.Now()
		lastChunkTime := startTime

		// Track minimal state needed for response format
		var usage *schemas.ResponsesResponseUsage

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

			if chunkIndex == 0 {
				providerUtils.SendCreatedEventResponsesChunk(ctx, postHookRunner, provider.GetProviderKey(), request.Model, startTime, responseChan)
				providerUtils.SendInProgressEventResponsesChunk(ctx, postHookRunner, provider.GetProviderKey(), request.Model, startTime, responseChan)
				chunkIndex = 2
			}

			if event.Usage != nil {
				usage = &schemas.ResponsesResponseUsage{
					InputTokens:  event.Usage.InputTokens,
					OutputTokens: event.Usage.OutputTokens,
					TotalTokens:  event.Usage.InputTokens + event.Usage.OutputTokens,
				}
			}

			response, bifrostErr, isLastChunk := event.ToBifrostResponsesStream(chunkIndex)
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

				if provider.sendBackRawResponse {
					response.ExtraFields.RawResponse = eventData
				}

				if isLastChunk {
					if response.Response == nil {
						response.Response = &schemas.BifrostResponsesResponse{}
					}
					if usage != nil {
						response.Response.Usage = usage
					}
					response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					providerUtils.HandleStreamEndWithSuccess(ctx, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil), postHookRunner, responseChan)
					break
				}
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil), responseChan)
			}
			if bifrostErr != nil {
				bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
					RequestType:    schemas.ResponsesStreamRequest,
					Provider:       provider.GetProviderKey(),
					ModelRequested: request.Model,
				}

				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
				break
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
	return nil, providerUtils.NewUnsupportedOperationError("embedding", string(provider.GetProviderKey()))
}

// Speech is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("speech", string(provider.GetProviderKey()))
}

// SpeechStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("speech stream", string(provider.GetProviderKey()))
}

// Transcription is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("transcription", string(provider.GetProviderKey()))
}

// TranscriptionStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError("transcription stream", string(provider.GetProviderKey()))
}

// parseStreamAnthropicError parses Anthropic streaming error responses.
func parseStreamAnthropicError(resp *fasthttp.Response, providerType schemas.ModelProvider) *schemas.BifrostError {
	statusCode := resp.StatusCode()
	body := resp.Body()
	return providerUtils.NewProviderAPIError(string(body), nil, statusCode, providerType, nil, nil)
}
