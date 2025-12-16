package gemini

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	BifrostContextKeyResponseFormat schemas.BifrostContextKey = "bifrost_context_key_response_format"
)

type GeminiProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawRequest   bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewGeminiProvider creates a new Gemini provider instance.
// It initializes the HTTP client with the provided configuration.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewGeminiProvider(config *schemas.ProviderConfig, logger schemas.Logger) *GeminiProvider {
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

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &GeminiProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Gemini.
func (provider *GeminiProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Gemini, provider.customProviderConfig)
}

// completeRequest handles the common HTTP request pattern for Gemini API calls
func (provider *GeminiProvider) completeRequest(ctx context.Context, model string, key schemas.Key, jsonBody []byte, endpoint string, meta *providerUtils.RequestMetadata) (*GenerateContentResponse, interface{}, time.Duration, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Use Gemini's generateContent endpoint
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/models/"+model+endpoint))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}

	req.SetBody(jsonBody)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, nil, latency, parseGeminiError(resp, meta)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	// Copy the response body before releasing the response
	// to avoid use-after-free since, respBody references fasthttp's internal buffer
	responseBody := append([]byte(nil), body...)

	// Parse Gemini's response
	var geminiResponse GenerateContentResponse
	if err := sonic.Unmarshal(responseBody, &geminiResponse); err != nil {
		return nil, nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	var rawResponse interface{}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
			return nil, nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
		}
	}

	return &geminiResponse, rawResponse, latency, nil
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *GeminiProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Build URL using centralized URL construction
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, fmt.Sprintf("/models?pageSize=%d", schemas.DefaultPageSize)))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    provider.GetProviderKey(),
			RequestType: schemas.ListModelsRequest,
		})
	}

	// Parse Gemini's response
	var geminiResponse GeminiListModelsResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &geminiResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := geminiResponse.ToBifrostListModelsResponse(providerName, key.Models)

	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ListModels performs a list models request to Gemini's API.
// Requests are made concurrently for improved performance.
func (provider *GeminiProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
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

// TextCompletion is not supported by the Gemini provider.
func (provider *GeminiProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream performs a streaming text completion request to Gemini's API.
// It formats the request, sends it to Gemini, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *GeminiProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to the Gemini API.
func (provider *GeminiProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToGeminiChatCompletionRequest(request), nil },
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	geminiResponse, rawResponse, latency, bifrostErr := provider.completeRequest(ctx, request.Model, key, jsonData, ":generateContent", &providerUtils.RequestMetadata{
		Provider:    providerName,
		Model:       request.Model,
		RequestType: schemas.ChatCompletionRequest,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse := geminiResponse.ToBifrostChatResponse()

	bifrostResponse.ExtraFields.RequestType = schemas.ChatCompletionRequest
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ChatCompletionStream performs a streaming chat completion request to the Gemini API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *GeminiProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if chat completion stream is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := ToGeminiChatCompletionRequest(request)
			if reqBody == nil {
				return nil, fmt.Errorf("chat completion request is not provided or could not be converted to Gemini format")
			}
			return reqBody, nil
		},
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	// Prepare Gemini headers
	headers := map[string]string{
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}
	if key.Value != "" {
		headers["x-goog-api-key"] = key.Value
	}

	// Use shared Gemini streaming logic
	return HandleGeminiChatCompletionStream(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/models/"+request.Model+":streamGenerateContent?alt=sse"),
		jsonData,
		headers,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		request.Model,
		postHookRunner,
		nil,
		provider.logger,
	)
}

// HandleGeminiChatCompletionStream handles streaming for Gemini-compatible APIs.
func HandleGeminiChatCompletionStream(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	jsonBody []byte,
	headers map[string]string,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	model string,
	postHookRunner schemas.PostHookRunner,
	postResponseConverter func(*schemas.BifrostChatResponse) *schemas.BifrostChatResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
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
	doErr := client.Do(req, resp)
	if doErr != nil {
		defer providerUtils.ReleaseStreamingResponse(resp)
		if errors.Is(doErr, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   doErr,
				},
			}
		}
		if errors.Is(doErr, fasthttp.ErrTimeout) || errors.Is(doErr, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, doErr, providerName)
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, doErr, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerName, resp.StatusCode()), fmt.Errorf("%s", string(resp.Body())), resp.StatusCode(), providerName, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer providerUtils.ReleaseStreamingResponse(resp)

		scanner := bufio.NewScanner(resp.BodyStream())
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		chunkIndex := 0
		startTime := time.Now()
		lastChunkTime := startTime

		var responseID string
		var modelName string

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE data
			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			eventData := strings.TrimPrefix(line, "data: ")

			// Skip empty data
			if strings.TrimSpace(eventData) == "" {
				continue
			}

			// Process chunk using shared function
			geminiResponse, err := processGeminiStreamChunk(eventData)
			if err != nil {
				if strings.Contains(err.Error(), "gemini api error") {
					// Handle API error
					bifrostErr := &schemas.BifrostError{
						Type:           schemas.Ptr("gemini_api_error"),
						IsBifrostError: false,
						Error: &schemas.ErrorField{
							Message: err.Error(),
							Error:   err,
						},
						ExtraFields: schemas.BifrostErrorExtraFields{
							RequestType:    schemas.ChatCompletionStreamRequest,
							Provider:       providerName,
							ModelRequested: model,
						},
					}
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
					return
				}
				logger.Warn(fmt.Sprintf("Failed to process chunk: %v", err))
				continue
			}

			// Track response ID and model
			if geminiResponse.ResponseID != "" && responseID == "" {
				responseID = geminiResponse.ResponseID
			}
			if geminiResponse.ModelVersion != "" && modelName == "" {
				modelName = geminiResponse.ModelVersion
			}

			// Convert to Bifrost stream response
			response, bifrostErr, isLastChunk := geminiResponse.ToBifrostChatCompletionStream()
			if bifrostErr != nil {
				bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
					RequestType:    schemas.ChatCompletionStreamRequest,
					Provider:       providerName,
					ModelRequested: model,
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				return
			}

			if response != nil {
				response.ID = responseID
				if modelName != "" {
					response.Model = modelName
				}
				response.ExtraFields = schemas.BifrostResponseExtraFields{
					RequestType:    schemas.ChatCompletionStreamRequest,
					Provider:       providerName,
					ModelRequested: model,
					ChunkIndex:     chunkIndex,
					Latency:        time.Since(lastChunkTime).Milliseconds(),
				}

				if postResponseConverter != nil {
					response = postResponseConverter(response)
					if response == nil {
						logger.Warn("postResponseConverter returned nil; skipping chunk")
						continue
					}
				}

				if sendBackRawResponse {
					response.ExtraFields.RawResponse = eventData
				}

				lastChunkTime = time.Now()
				chunkIndex++

				if isLastChunk {
					if sendBackRawRequest {
						providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
					}
					response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil), responseChan)
					break
				}

				// Process response through post-hooks and send to channel
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil), responseChan)
			}
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, model, logger)
		}
	}()

	return responseChan, nil
}

// Responses performs a chat completion request to Gemini's API.
// It formats the request, sends it to Gemini, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *GeminiProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	// Convert to Gemini format using the centralized converter
	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := ToGeminiResponsesRequest(request)
			if reqBody == nil {
				return nil, fmt.Errorf("responses input is not provided or could not be converted to Gemini format")
			}
			return reqBody, nil
		},
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	// Use struct directly for JSON marshaling
	geminiResponse, rawResponse, latency, bifrostErr := provider.completeRequest(ctx, request.Model, key, jsonData, ":generateContent", &providerUtils.RequestMetadata{
		Provider:    provider.GetProviderKey(),
		Model:       request.Model,
		RequestType: schemas.ResponsesRequest,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	bifrostResponse := geminiResponse.ToResponsesBifrostResponsesResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ResponsesRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ResponsesStream performs a streaming responses request to the Gemini API.
func (provider *GeminiProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if responses stream is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := ToGeminiResponsesRequest(request)
			if reqBody == nil {
				return nil, fmt.Errorf("responses input is not provided or could not be converted to Gemini format")
			}
			return reqBody, nil
		},
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	// Prepare Gemini headers
	headers := map[string]string{
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}
	if key.Value != "" {
		headers["x-goog-api-key"] = key.Value
	}

	return HandleGeminiResponsesStream(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/models/"+request.Model+":streamGenerateContent?alt=sse"),
		jsonData,
		headers,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		request.Model,
		postHookRunner,
		nil,
		provider.logger,
	)
}

// HandleGeminiResponsesStream handles streaming for Gemini-compatible APIs.
func HandleGeminiResponsesStream(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	jsonBody []byte,
	headers map[string]string,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	model string,
	postHookRunner schemas.PostHookRunner,
	postResponseConverter func(*schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
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
	doErr := client.Do(req, resp)
	if doErr != nil {
		defer providerUtils.ReleaseStreamingResponse(resp)
		if errors.Is(doErr, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   doErr,
				},
			}
		}
		if errors.Is(doErr, fasthttp.ErrTimeout) || errors.Is(doErr, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, doErr, providerName)
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, doErr, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("HTTP error from %s: %d", providerName, resp.StatusCode()), fmt.Errorf("%s", string(resp.Body())), resp.StatusCode(), providerName, nil, nil)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer providerUtils.ReleaseStreamingResponse(resp)

		scanner := bufio.NewScanner(resp.BodyStream())
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		chunkIndex := 0
		sequenceNumber := 0 // Track sequence across all events
		startTime := time.Now()
		lastChunkTime := startTime

		// Initialize stream state for responses lifecycle management
		streamState := acquireGeminiResponsesStreamState()
		defer releaseGeminiResponsesStreamState(streamState)

		var lastUsageMetadata *GenerateContentResponseUsageMetadata

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Parse SSE data
			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			eventData := strings.TrimPrefix(line, "data: ")

			// Skip empty data
			if strings.TrimSpace(eventData) == "" {
				continue
			}

			// Process chunk using shared function
			geminiResponse, err := processGeminiStreamChunk(eventData)
			if err != nil {
				if strings.Contains(err.Error(), "gemini api error") {
					// Handle API error
					bifrostErr := &schemas.BifrostError{
						Type:           schemas.Ptr("gemini_api_error"),
						IsBifrostError: false,
						Error: &schemas.ErrorField{
							Message: err.Error(),
							Error:   err,
						},
						ExtraFields: schemas.BifrostErrorExtraFields{
							RequestType:    schemas.ResponsesStreamRequest,
							Provider:       providerName,
							ModelRequested: model,
						},
					}
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
					return
				}
				logger.Warn(fmt.Sprintf("Failed to process chunk: %v", err))
				continue
			}

			// Track usage metadata from the latest chunk
			if geminiResponse.UsageMetadata != nil {
				lastUsageMetadata = geminiResponse.UsageMetadata
			}

			// Convert to Bifrost responses stream response
			responses, bifrostErr := geminiResponse.ToBifrostResponsesStream(sequenceNumber, streamState)
			if bifrostErr != nil {
				bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
					RequestType:    schemas.ResponsesStreamRequest,
					Provider:       providerName,
					ModelRequested: model,
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				return
			}

			for i, response := range responses {
				if response != nil {
					response.ExtraFields = schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ResponsesStreamRequest,
						Provider:       providerName,
						ModelRequested: model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					}

					if postResponseConverter != nil {
						response = postResponseConverter(response)
						if response == nil {
							logger.Warn("postResponseConverter returned nil; skipping chunk")
							continue
						}
					}

					// Only add raw response to the LAST response in the array
					if sendBackRawResponse && i == len(responses)-1 {
						response.ExtraFields.RawResponse = eventData
					}

					chunkIndex++
					sequenceNumber++ // Increment sequence number for each response

					// Check if this is the last chunk
					isLastChunk := false
					if response.Type == schemas.ResponsesStreamResponseTypeCompleted {
						isLastChunk = true
					}

					if isLastChunk {
						if sendBackRawRequest {
							providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
						}
						response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
						ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil), responseChan)
						return
					}

					// For multiple responses in one event, only update timing on the last one
					if i == len(responses)-1 {
						lastChunkTime = time.Now()
					}

					// Process response through post-hooks and send to channel
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil), responseChan)
				}
			}
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, model, logger)
		} else {
			// Finalize the stream by closing any open items
			finalResponses := FinalizeGeminiResponsesStream(streamState, lastUsageMetadata, sequenceNumber)
			for i, finalResponse := range finalResponses {
				finalResponse.ExtraFields = schemas.BifrostResponseExtraFields{
					RequestType:    schemas.ResponsesStreamRequest,
					Provider:       providerName,
					ModelRequested: model,
					ChunkIndex:     chunkIndex,
					Latency:        time.Since(lastChunkTime).Milliseconds(),
				}

				if postResponseConverter != nil {
					finalResponse = postResponseConverter(finalResponse)
					if finalResponse == nil {
						logger.Warn("postResponseConverter returned nil; skipping final response")
						continue
					}
				}

				chunkIndex++
				sequenceNumber++

				if sendBackRawResponse {
					finalResponse.ExtraFields.RawResponse = "{}" // Final event has no payload
				}

				// Set final latency on the last response (completed event)
				if i == len(finalResponses)-1 {
					finalResponse.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				}

				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, finalResponse, nil, nil), responseChan)
			}
		}
	}()

	return responseChan, nil
}

// Embedding performs an embedding request to the Gemini API.
func (provider *GeminiProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	// Check if embedding is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}
	// Use the shared embedding request handler
	return openai.HandleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/openai/embeddings"),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// Speech performs a speech synthesis request to the Gemini API.
func (provider *GeminiProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	// Check if speech is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
		return nil, err
	}

	// Prepare request body using speech-specific function
	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToGeminiSpeechRequest(request) },
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	// Use common request function
	geminiResponse, rawResponse, latency, bifrostErr := provider.completeRequest(ctx, request.Model, key, jsonData, ":generateContent", &providerUtils.RequestMetadata{
		Provider:    provider.GetProviderKey(),
		Model:       request.Model,
		RequestType: schemas.SpeechRequest,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	ctx = context.WithValue(ctx, BifrostContextKeyResponseFormat, request.Params.ResponseFormat)
	response, convErr := geminiResponse.ToBifrostSpeechResponse(ctx)
	if convErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, convErr, provider.GetProviderKey())
	}

	// Set ExtraFields
	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.SpeechRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonData)
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// SpeechStream performs a streaming speech synthesis request to the Gemini API.
func (provider *GeminiProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if speech stream is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.SpeechStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Prepare request body using speech-specific function
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToGeminiSpeechRequest(request) },
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
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/models/"+request.Model+":streamGenerateContent?alt=sse"))
	req.Header.SetContentType("application/json")

	// Set headers for streaming
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Set headers
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
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			Model:       request.Model,
			RequestType: schemas.SpeechStreamRequest,
		})
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer providerUtils.ReleaseStreamingResponse(resp)
		defer close(responseChan)

		scanner := bufio.NewScanner(resp.BodyStream())
		// Increase buffer size to handle large chunks (especially for audio data)
		buf := make([]byte, 0, 1024*1024) // 1MB initial buffer
		scanner.Buffer(buf, 10*1024*1024) // Allow up to 10MB tokens
		chunkIndex := -1
		usage := &schemas.SpeechUsage{}
		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()

			// Skip empty lines
			if line == "" {
				continue
			}

			var jsonData string
			// Parse SSE data
			if strings.HasPrefix(line, "data: ") {
				jsonData = strings.TrimPrefix(line, "data: ")
			} else {
				// Handle raw JSON errors (without "data: " prefix)
				jsonData = line
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// Process chunk using shared function
			geminiResponse, err := processGeminiStreamChunk(jsonData)
			if err != nil {
				if strings.Contains(err.Error(), "gemini api error") {
					// Handle API error
					bifrostErr := &schemas.BifrostError{
						Type:           schemas.Ptr("gemini_api_error"),
						IsBifrostError: false,
						Error: &schemas.ErrorField{
							Message: err.Error(),
							Error:   err,
						},
						ExtraFields: schemas.BifrostErrorExtraFields{
							RequestType:    schemas.SpeechStreamRequest,
							Provider:       providerName,
							ModelRequested: request.Model,
						},
					}
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
					return
				}
				provider.logger.Warn(fmt.Sprintf("Failed to process chunk: %v", err))
				continue
			}

			// Extract audio data from Gemini response for regular chunks
			var audioChunk []byte
			if len(geminiResponse.Candidates) > 0 {
				candidate := geminiResponse.Candidates[0]
				if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
					var buf []byte
					for _, part := range candidate.Content.Parts {
						if part.InlineData != nil && part.InlineData.Data != nil {
							buf = append(buf, part.InlineData.Data...)
						}
					}
					if len(buf) > 0 {
						audioChunk = buf
					}
				}
			}

			// Check if this is the final chunk (has finishReason)
			if len(geminiResponse.Candidates) > 0 && (geminiResponse.Candidates[0].FinishReason != "" || geminiResponse.UsageMetadata != nil) {
				// Extract usage metadata using shared function
				inputTokens, outputTokens, totalTokens := extractGeminiUsageMetadata(geminiResponse)
				usage.InputTokens = inputTokens
				usage.OutputTokens = outputTokens
				usage.TotalTokens = totalTokens
			}

			// Only send response if we have actual audio content
			if len(audioChunk) > 0 {
				chunkIndex++

				// Create Bifrost speech response for streaming
				response := &schemas.BifrostSpeechStreamResponse{
					Type:  schemas.SpeechStreamResponseTypeDelta,
					Audio: audioChunk,
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:    schemas.SpeechStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					},
				}
				lastChunkTime = time.Now()

				if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
					response.ExtraFields.RawResponse = jsonData
				}

				// Process response through post-hooks and send to channel
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, response, nil), responseChan)
			}
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.SpeechStreamRequest, providerName, request.Model, provider.logger)
		} else {
			response := &schemas.BifrostSpeechStreamResponse{
				Type:  schemas.SpeechStreamResponseTypeDone,
				Usage: usage,
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:    schemas.SpeechStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
					ChunkIndex:     chunkIndex + 1,
					Latency:        time.Since(startTime).Milliseconds(),
				},
			}

			// Set raw request if enabled
			if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
				providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
			}
			ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, response, nil), responseChan)
		}
	}()

	return responseChan, nil
}

// Transcription performs a speech-to-text request to the Gemini API.
func (provider *GeminiProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	// Check if transcription is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.TranscriptionRequest); err != nil {
		return nil, err
	}

	// Prepare request body using transcription-specific function
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToGeminiTranscriptionRequest(request), nil },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Use common request function
	geminiResponse, rawResponse, latency, bifrostErr := provider.completeRequest(ctx, request.Model, key, jsonData, ":generateContent", &providerUtils.RequestMetadata{
		Provider:    provider.GetProviderKey(),
		Model:       request.Model,
		RequestType: schemas.TranscriptionRequest,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := geminiResponse.ToBifrostTranscriptionResponse()

	// Set ExtraFields
	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.TranscriptionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonData)
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// TranscriptionStream performs a streaming speech-to-text request to the Gemini API.
func (provider *GeminiProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if transcription stream is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.TranscriptionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Prepare request body using transcription-specific function
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToGeminiTranscriptionRequest(request), nil },
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
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/models/"+request.Model+":streamGenerateContent?alt=sse"))
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Set headers for streaming
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

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
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName)
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, provider.GetProviderKey())
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			Model:       request.Model,
			RequestType: schemas.TranscriptionStreamRequest,
		})
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer providerUtils.ReleaseStreamingResponse(resp)

		scanner := bufio.NewScanner(resp.BodyStream())
		// Increase buffer size to handle large chunks (especially for audio data)
		buf := make([]byte, 0, 1024*1024) // 1MB initial buffer
		scanner.Buffer(buf, 10*1024*1024) // Allow up to 10MB tokens
		chunkIndex := -1
		usage := &schemas.TranscriptionUsage{}
		startTime := time.Now()
		lastChunkTime := startTime

		var fullTranscriptionText string

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()

			// Skip empty lines
			if line == "" {
				continue
			}
			var jsonData string
			// Parse SSE data
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				jsonData = after
			} else {
				// Handle raw JSON errors (without "data: " prefix)
				jsonData = line
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// First, check if this is an error response
			var errorCheck map[string]interface{}
			if err := sonic.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream data as JSON: %v", err))
				continue
			}

			// Handle error responses
			if _, hasError := errorCheck["error"]; hasError {
				bifrostErr := &schemas.BifrostError{
					Type:           schemas.Ptr("gemini_api_error"),
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Message: fmt.Sprintf("Gemini API error: %v", errorCheck["error"]),
						Error:   fmt.Errorf("stream error: %v", errorCheck["error"]),
					},
					ExtraFields: schemas.BifrostErrorExtraFields{
						RequestType:    schemas.TranscriptionStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
					},
				}
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
				return
			}

			// Parse Gemini streaming response
			var geminiResponse GenerateContentResponse
			if err := sonic.Unmarshal([]byte(jsonData), &geminiResponse); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse Gemini stream response: %v", err))
				continue
			}

			// Extract text from Gemini response for regular chunks
			var deltaText string
			if len(geminiResponse.Candidates) > 0 && geminiResponse.Candidates[0].Content != nil {
				if len(geminiResponse.Candidates[0].Content.Parts) > 0 {
					var sb strings.Builder
					for _, p := range geminiResponse.Candidates[0].Content.Parts {
						if p.Text != "" {
							sb.WriteString(p.Text)
						}
					}
					if sb.Len() > 0 {
						deltaText = sb.String()
						fullTranscriptionText += deltaText
					}
				}
			}

			// Check if this is the final chunk (has finishReason)
			if len(geminiResponse.Candidates) > 0 && (geminiResponse.Candidates[0].FinishReason != "" || geminiResponse.UsageMetadata != nil) {
				// Extract usage metadata from Gemini response
				inputTokens, outputTokens, totalTokens := extractGeminiUsageMetadata(&geminiResponse)
				usage.InputTokens = schemas.Ptr(inputTokens)
				usage.OutputTokens = schemas.Ptr(outputTokens)
				usage.TotalTokens = schemas.Ptr(totalTokens)
			}

			// Only send response if we have actual text content
			if deltaText != "" {
				chunkIndex++

				// Create Bifrost transcription response for streaming
				response := &schemas.BifrostTranscriptionStreamResponse{
					Type:  schemas.TranscriptionStreamResponseTypeDelta,
					Delta: &deltaText, // Delta text for this chunk
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:    schemas.TranscriptionStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					},
				}
				lastChunkTime = time.Now()

				if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
					response.ExtraFields.RawResponse = jsonData
				}

				// Process response through post-hooks and send to channel
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, response), responseChan)
			}
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.TranscriptionStreamRequest, providerName, request.Model, provider.logger)
		} else {
			response := &schemas.BifrostTranscriptionStreamResponse{
				Type: schemas.TranscriptionStreamResponseTypeDone,
				Text: fullTranscriptionText,
				Usage: &schemas.TranscriptionUsage{
					Type:         "tokens",
					InputTokens:  usage.InputTokens,
					OutputTokens: usage.OutputTokens,
					TotalTokens:  usage.TotalTokens,
				},
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:    schemas.TranscriptionStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
					ChunkIndex:     chunkIndex + 1,
					Latency:        time.Since(startTime).Milliseconds(),
				},
			}

			// Set raw request if enabled
			if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
				providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
			}
			ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, response), responseChan)
		}
	}()

	return responseChan, nil
}

// ==================== BATCH OPERATIONS ====================

// BatchCreate creates a new batch job for Gemini.
// Uses the asynchronous batchGenerateContent endpoint as per official documentation.
// Supports both inline requests and file-based input (via InputFileID).
func (provider *GeminiProvider) BatchCreate(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.BatchCreateRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Validate that either InputFileID or Requests is provided, but not both
	hasFileInput := request.InputFileID != ""
	hasInlineRequests := len(request.Requests) > 0

	if !hasFileInput && !hasInlineRequests {
		return nil, providerUtils.NewBifrostOperationError("either input_file_id or requests must be provided", nil, providerName)
	}

	if hasFileInput && hasInlineRequests {
		return nil, providerUtils.NewBifrostOperationError("cannot specify both input_file_id and requests", nil, providerName)
	}

	// Build the batch request with proper nested structure
	batchReq := &GeminiBatchCreateRequest{
		Batch: GeminiBatchConfig{
			DisplayName: fmt.Sprintf("bifrost-batch-%d", time.Now().UnixNano()),
		},
	}

	if hasFileInput {
		// File-based input: use file_name in input_config
		fileID := request.InputFileID
		// Ensure file ID has the "files/" prefix
		if !strings.HasPrefix(fileID, "files/") {
			fileID = "files/" + fileID
		}
		batchReq.Batch.InputConfig = GeminiBatchInputConfig{
			FileName: fileID,
		}
	} else {
		// Inline requests: use requests in input_config
		batchReq.Batch.InputConfig = GeminiBatchInputConfig{
			Requests: &GeminiBatchRequestsWrapper{
				Requests: buildBatchRequestItems(request.Requests),
			},
		}
	}

	jsonData, err := sonic.Marshal(batchReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}

	// Create HTTP request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL - use batchGenerateContent endpoint
	_, model := schemas.ParseModelString(request.Model, schemas.Gemini)
	if model == "" {
		model = "gemini-2.5-flash"
	}
	url := fmt.Sprintf("%s/models/%s:batchGenerateContent", provider.networkConfig.BaseURL, model)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.SetContentType("application/json")
	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			Model:       request.Model,
			RequestType: schemas.BatchCreateRequest,
		})
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Parse the batch job response
	var geminiResp GeminiBatchJobResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		provider.logger.Error("gemini batch create unmarshal error: " + err.Error())
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}
	// Check for metadata
	if geminiResp.Metadata == nil {
		return nil, providerUtils.NewBifrostOperationError("gemini batch response missing metadata", nil, providerName)
	}
	// Check for batch stats
	if geminiResp.Metadata.BatchStats == nil {
		return nil, providerUtils.NewBifrostOperationError("gemini batch response missing batch stats", nil, providerName)
	}
	// Calculate request counts based on response
	totalRequests := geminiResp.Metadata.BatchStats.RequestCount
	completedCount := 0
	failedCount := 0

	// If results are already available (fast completion), count them
	if geminiResp.Dest != nil && len(geminiResp.Dest.InlinedResponses) > 0 {
		for _, inlineResp := range geminiResp.Dest.InlinedResponses {
			if inlineResp.Error != nil {
				failedCount++
			} else if inlineResp.Response != nil {
				completedCount++
			}
		}
	} else {
		completedCount = geminiResp.Metadata.BatchStats.RequestCount - geminiResp.Metadata.BatchStats.PendingRequestCount
	}

	// Determine status
	status := ToBifrostBatchStatus(geminiResp.Metadata.State)

	// If state is empty but we have results, it's completed
	if geminiResp.Metadata.State == "" && geminiResp.Dest != nil && len(geminiResp.Dest.InlinedResponses) > 0 {
		status = schemas.BatchStatusCompleted
		completedCount = len(geminiResp.Dest.InlinedResponses) - failedCount
	}

	// Build response
	result := &schemas.BifrostBatchCreateResponse{
		ID:            geminiResp.Metadata.Name,
		Object:        "batch",
		Endpoint:      string(request.Endpoint),
		Status:        status,
		CreatedAt:     parseGeminiTimestamp(geminiResp.Metadata.CreateTime),
		OperationName: &geminiResp.Metadata.Name,
		RequestCounts: schemas.BatchRequestCounts{
			Total:     totalRequests,
			Completed: completedCount,
			Failed:    failedCount,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCreateRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	// Include InputFileID if file-based input was used
	if hasFileInput {
		result.InputFileID = request.InputFileID
	}

	// Include output file ID if results are in a file
	if geminiResp.Dest != nil && geminiResp.Dest.FileName != "" {
		result.OutputFileID = &geminiResp.Dest.FileName
	}

	return result, nil
}

// BatchList lists batch jobs for Gemini.
// Note: The consumer API may have limited list functionality.
func (provider *GeminiProvider) BatchList(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.BatchListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create HTTP request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL for listing batches
	baseURL := fmt.Sprintf("%s/batches", provider.networkConfig.BaseURL)
	values := url.Values{}
	if request.PageSize > 0 {
		values.Set("pageSize", fmt.Sprintf("%d", request.PageSize))
	} else if request.Limit > 0 {
		values.Set("pageSize", fmt.Sprintf("%d", request.Limit))
	}
	if request.PageToken != nil && *request.PageToken != "" {
		values.Set("pageToken", *request.PageToken)
	}
	requestURL := baseURL
	if encodedValues := values.Encode(); encodedValues != "" {
		requestURL += "?" + encodedValues
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.SetContentType("application/json")

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response - if listing is not supported, return empty list
	if resp.StatusCode() != fasthttp.StatusOK {
		// If 404 or method not allowed, batch listing may not be available
		if resp.StatusCode() == fasthttp.StatusNotFound || resp.StatusCode() == fasthttp.StatusMethodNotAllowed {
			provider.logger.Debug("gemini batch list not available, returning empty list")
			return &schemas.BifrostBatchListResponse{
				Object:  "list",
				Data:    []schemas.BifrostBatchRetrieveResponse{},
				HasMore: false,
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType: schemas.BatchListRequest,
					Provider:    providerName,
					Latency:     latency.Milliseconds(),
				},
			}, nil
		}
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			RequestType: schemas.BatchListRequest,
		})
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var geminiResp GeminiBatchListResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Convert to Bifrost format
	data := make([]schemas.BifrostBatchRetrieveResponse, 0, len(geminiResp.Operations))
	for _, batch := range geminiResp.Operations {
		data = append(data, schemas.BifrostBatchRetrieveResponse{
			ID:            extractBatchIDFromName(batch.Name),
			Object:        "batch",
			Status:        ToBifrostBatchStatus(batch.Metadata.State),
			CreatedAt:     parseGeminiTimestamp(batch.Metadata.CreateTime),
			OperationName: &batch.Name,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchListRequest,
				Provider:    providerName,
			},
		})
	}

	hasMore := geminiResp.NextPageToken != ""
	var nextCursor *string
	if hasMore {
		nextCursor = &geminiResp.NextPageToken
	}

	return &schemas.BifrostBatchListResponse{
		Object:     "list",
		Data:       data,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// BatchRetrieve retrieves a specific batch job for Gemini.
func (provider *GeminiProvider) BatchRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.BatchRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, providerName)
	}

	// Create HTTP request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL - batch ID might be full resource name or just the ID
	batchID := request.BatchID
	var url string
	if strings.HasPrefix(batchID, "batches/") {
		url = fmt.Sprintf("%s/%s", provider.networkConfig.BaseURL, batchID)
	} else {
		url = fmt.Sprintf("%s/batches/%s", provider.networkConfig.BaseURL, batchID)
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.SetContentType("application/json")

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			RequestType: schemas.BatchRetrieveRequest,
		})
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var geminiResp GeminiBatchJobResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	var completedCount, failedCount int

	completedCount = geminiResp.Metadata.BatchStats.RequestCount - geminiResp.Metadata.BatchStats.PendingRequestCount
	failedCount = completedCount - geminiResp.Metadata.BatchStats.SuccessfulRequestCount

	// Determine if job is done
	isDone := geminiResp.Metadata.State == GeminiBatchStateSucceeded ||
		geminiResp.Metadata.State == GeminiBatchStateFailed ||
		geminiResp.Metadata.State == GeminiBatchStateCancelled ||
		geminiResp.Metadata.State == GeminiBatchStateExpired

	return &schemas.BifrostBatchRetrieveResponse{
		ID:            geminiResp.Metadata.Name,
		Object:        "batch",
		Status:        ToBifrostBatchStatus(geminiResp.Metadata.State),
		CreatedAt:     parseGeminiTimestamp(geminiResp.Metadata.CreateTime),
		OperationName: &geminiResp.Metadata.Name,
		Done:          &isDone,
		RequestCounts: schemas.BatchRequestCounts{
			Completed: completedCount,
			Total:     geminiResp.Metadata.BatchStats.RequestCount,
			Succeeded: geminiResp.Metadata.BatchStats.SuccessfulRequestCount,
			Pending:   geminiResp.Metadata.BatchStats.PendingRequestCount,
			Failed:    failedCount,
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchRetrieveRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// BatchCancel cancels a batch job for Gemini.
// Note: Cancellation support depends on the API version and batch state.
func (provider *GeminiProvider) BatchCancel(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.BatchCancelRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, providerName)
	}

	// Create HTTP request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL for cancel operation
	batchID := request.BatchID
	var url string
	if strings.HasPrefix(batchID, "batches/") {
		url = fmt.Sprintf("%s/%s:cancel", provider.networkConfig.BaseURL, batchID)
	} else {
		url = fmt.Sprintf("%s/batches/%s:cancel", provider.networkConfig.BaseURL, batchID)
	}

	provider.logger.Debug("gemini batch cancel url: " + url)
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.SetContentType("application/json")

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle response
	if resp.StatusCode() != fasthttp.StatusOK {
		// If cancel is not supported, return appropriate status
		if resp.StatusCode() == fasthttp.StatusNotFound || resp.StatusCode() == fasthttp.StatusMethodNotAllowed {
			// 404 could mean batch not found or cancel not supported
			// Return the error instead of assuming completed
			return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
				Provider:    providerName,
				RequestType: schemas.BatchCancelRequest,
			})
		}
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			RequestType: schemas.BatchCancelRequest,
		})
	}

	now := time.Now().Unix()
	return &schemas.BifrostBatchCancelResponse{
		ID:           request.BatchID,
		Object:       "batch",
		Status:       schemas.BatchStatusCancelling,
		CancellingAt: &now,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCancelRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// processGeminiStreamChunk processes a single chunk from Gemini streaming response
func processGeminiStreamChunk(jsonData string) (*GenerateContentResponse, error) {
	// First, check if this is an error response
	var errorCheck map[string]interface{}
	if err := sonic.Unmarshal([]byte(jsonData), &errorCheck); err != nil {
		return nil, fmt.Errorf("failed to parse stream data as JSON: %v", err)
	}

	// Handle error responses
	if _, hasError := errorCheck["error"]; hasError {
		return nil, fmt.Errorf("gemini api error: %v", errorCheck["error"])
	}

	// Parse Gemini streaming response
	var geminiResponse GenerateContentResponse
	if err := sonic.Unmarshal([]byte(jsonData), &geminiResponse); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini stream response: %v", err)
	}

	return &geminiResponse, nil
}

// BatchResults retrieves batch results for Gemini.
// Results are extracted from dest.inlinedResponses for inline batches,
// or downloaded from dest.fileName for file-based batches.
func (provider *GeminiProvider) BatchResults(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.BatchResultsRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, providerName)
	}

	// First, retrieve the batch to get its results
	retrieveReq := &schemas.BifrostBatchRetrieveRequest{
		BatchID: request.BatchID,
	}

	// We need to get the full batch response with results, so make the API call directly
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL
	batchID := request.BatchID
	var url string
	if strings.HasPrefix(batchID, "batches/") {
		url = fmt.Sprintf("%s/%s", provider.networkConfig.BaseURL, batchID)
	} else {
		url = fmt.Sprintf("%s/batches/%s", provider.networkConfig.BaseURL, batchID)
	}

	provider.logger.Debug("gemini batch results url: " + url)
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.Header.SetContentType("application/json")

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			RequestType: schemas.BatchResultsRequest,
		})
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var geminiResp GeminiBatchJobResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Check if batch is still processing
	if geminiResp.Metadata.State == GeminiBatchStatePending || geminiResp.Metadata.State == GeminiBatchStateRunning {
		return nil, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("batch %s is still processing (state: %s), results not yet available", retrieveReq.BatchID, geminiResp.Metadata.State),
			nil,
			providerName,
		)
	}

	// Extract results - check for file-based results first, then inline responses
	var results []schemas.BatchResultItem
	var parseErrors []schemas.BatchError

	if geminiResp.Dest != nil && geminiResp.Dest.FileName != "" {
		// File-based results: download and parse the results file
		provider.logger.Debug("gemini batch results in file: " + geminiResp.Dest.FileName)
		fileResults, fileParseErrors, bifrostErr := provider.downloadBatchResultsFile(ctx, key, geminiResp.Dest.FileName)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		results = fileResults
		parseErrors = fileParseErrors
	} else if geminiResp.Dest != nil && len(geminiResp.Dest.InlinedResponses) > 0 {
		// Inline results: extract from inlinedResponses
		results = make([]schemas.BatchResultItem, 0, len(geminiResp.Dest.InlinedResponses))
		for i, inlineResp := range geminiResp.Dest.InlinedResponses {
			customID := fmt.Sprintf("request-%d", i)
			if inlineResp.Metadata != nil && inlineResp.Metadata.Key != "" {
				customID = inlineResp.Metadata.Key
			}

			resultItem := schemas.BatchResultItem{
				CustomID: customID,
			}

			if inlineResp.Error != nil {
				resultItem.Error = &schemas.BatchResultError{
					Code:    fmt.Sprintf("%d", inlineResp.Error.Code),
					Message: inlineResp.Error.Message,
				}
			} else if inlineResp.Response != nil {
				// Convert the response to a map for the Body field
				respBody := make(map[string]interface{})
				if len(inlineResp.Response.Candidates) > 0 {
					candidate := inlineResp.Response.Candidates[0]
					if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
						var textParts []string
						for _, part := range candidate.Content.Parts {
							if part.Text != "" {
								textParts = append(textParts, part.Text)
							}
						}
						if len(textParts) > 0 {
							respBody["text"] = strings.Join(textParts, "")
						}
					}
					respBody["finish_reason"] = string(candidate.FinishReason)
				}
				if inlineResp.Response.UsageMetadata != nil {
					respBody["usage"] = map[string]interface{}{
						"prompt_tokens":     inlineResp.Response.UsageMetadata.PromptTokenCount,
						"completion_tokens": inlineResp.Response.UsageMetadata.CandidatesTokenCount,
						"total_tokens":      inlineResp.Response.UsageMetadata.TotalTokenCount,
					}
				}

				resultItem.Response = &schemas.BatchResultResponse{
					StatusCode: 200,
					Body:       respBody,
				}
			}

			results = append(results, resultItem)
		}
	}

	// If no results found but job is complete, return info message
	if len(results) == 0 && (geminiResp.Metadata.State == GeminiBatchStateSucceeded || geminiResp.Metadata.State == GeminiBatchStateFailed) {
		results = []schemas.BatchResultItem{{
			CustomID: "info",
			Response: &schemas.BatchResultResponse{
				StatusCode: 200,
				Body: map[string]interface{}{
					"message": fmt.Sprintf("Batch completed with state: %s. No results available.", geminiResp.Metadata.State),
				},
			},
		}}
	}

	batchResultsResp := &schemas.BifrostBatchResultsResponse{
		BatchID: request.BatchID,
		Results: results,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchResultsRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if len(parseErrors) > 0 {
		batchResultsResp.ExtraFields.ParseErrors = parseErrors
	}

	return batchResultsResp, nil
}

// FileUpload uploads a file to Gemini.
func (provider *GeminiProvider) FileUpload(ctx context.Context, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.FileUploadRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("file content is required", nil, providerName)
	}

	// Create multipart request
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file metadata as JSON
	metadataField, err := writer.CreateFormField("metadata")
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create metadata field", err, providerName)
	}
	metadata := map[string]interface{}{
		"file": map[string]string{
			"displayName": request.Filename,
		},
	}
	metadataJSON, err := sonic.Marshal(metadata)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to marshal metadata", err, providerName)
	}
	if _, err := metadataField.Write(metadataJSON); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write metadata", err, providerName)
	}

	// Add file content
	filename := request.Filename
	if filename == "" {
		filename = "file.bin"
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create form file", err, providerName)
	}
	if _, err := part.Write(request.File); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write file content", err, providerName)
	}

	if err := writer.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to close multipart writer", err, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL - use upload endpoint
	baseURL := strings.Replace(provider.networkConfig.BaseURL, "/v1beta", "/upload/v1beta", 1)
	requestURL := fmt.Sprintf("%s/files", baseURL)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}
	req.SetBody(buf.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			RequestType: schemas.FileUploadRequest,
		})
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Parse response - wrapped in "file" object
	var responseWrapper struct {
		File GeminiFileResponse `json:"file"`
	}
	if err := sonic.Unmarshal(body, &responseWrapper); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	geminiResp := responseWrapper.File

	// Parse size
	var sizeBytes int64
	fmt.Sscanf(geminiResp.SizeBytes, "%d", &sizeBytes)

	// Parse creation time
	var createdAt int64
	if t, err := time.Parse(time.RFC3339, geminiResp.CreateTime); err == nil {
		createdAt = t.Unix()
	}

	// Parse expiration time
	var expiresAt *int64
	if geminiResp.ExpirationTime != "" {
		if t, err := time.Parse(time.RFC3339, geminiResp.ExpirationTime); err == nil {
			exp := t.Unix()
			expiresAt = &exp
		}
	}
	return &schemas.BifrostFileUploadResponse{
		ID:             geminiResp.Name,
		Object:         "file",
		Bytes:          sizeBytes,
		CreatedAt:      createdAt,
		Filename:       geminiResp.DisplayName,
		Purpose:        request.Purpose,
		Status:         ToBifrostFileStatus(geminiResp.State),
		StorageBackend: schemas.FileStorageAPI,
		StorageURI:     geminiResp.URI,
		ExpiresAt:      expiresAt,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileUploadRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// FileList lists files from Gemini.
func (provider *GeminiProvider) FileList(ctx context.Context, key schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.FileListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with pagination
	requestURL := fmt.Sprintf("%s/files", provider.networkConfig.BaseURL)
	values := url.Values{}
	if request.Limit > 0 {
		values.Set("pageSize", fmt.Sprintf("%d", request.Limit))
	}
	if request.After != nil && *request.After != "" {
		values.Set("pageToken", *request.After)
	}
	if encodedValues := values.Encode(); encodedValues != "" {
		requestURL += "?" + encodedValues
	}

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			RequestType: schemas.FileListRequest,
		})
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var geminiResp GeminiFileListResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  "list",
		Data:    make([]schemas.FileObject, len(geminiResp.Files)),
		HasMore: geminiResp.NextPageToken != "",
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if geminiResp.NextPageToken != "" {
		bifrostResp.After = &geminiResp.NextPageToken
	}

	for i, file := range geminiResp.Files {
		var sizeBytes int64
		fmt.Sscanf(file.SizeBytes, "%d", &sizeBytes)

		var createdAt int64
		if t, err := time.Parse(time.RFC3339, file.CreateTime); err == nil {
			createdAt = t.Unix()
		}

		var expiresAt *int64
		if file.ExpirationTime != "" {
			if t, err := time.Parse(time.RFC3339, file.ExpirationTime); err == nil {
				exp := t.Unix()
				expiresAt = &exp
			}
		}

		bifrostResp.Data[i] = schemas.FileObject{
			ID:        file.Name,
			Object:    "file",
			Bytes:     sizeBytes,
			CreatedAt: createdAt,
			Filename:  file.DisplayName,
			Purpose:   schemas.FilePurposeVision,
			Status:    ToBifrostFileStatus(file.State),
			ExpiresAt: expiresAt,
		}
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves file metadata from Gemini.
func (provider *GeminiProvider) FileRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.FileRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL - file ID is the full resource name (e.g., "files/abc123")
	fileID := request.FileID
	if !strings.HasPrefix(fileID, "files/") {
		fileID = "files/" + fileID
	}
	requestURL := fmt.Sprintf("%s/%s", provider.networkConfig.BaseURL, fileID)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			RequestType: schemas.FileRetrieveRequest,
		})
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var geminiResp GeminiFileResponse
	if err := sonic.Unmarshal(body, &geminiResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	var sizeBytes int64
	fmt.Sscanf(geminiResp.SizeBytes, "%d", &sizeBytes)

	var createdAt int64
	if t, err := time.Parse(time.RFC3339, geminiResp.CreateTime); err == nil {
		createdAt = t.Unix()
	}

	var expiresAt *int64
	if geminiResp.ExpirationTime != "" {
		if t, err := time.Parse(time.RFC3339, geminiResp.ExpirationTime); err == nil {
			exp := t.Unix()
			expiresAt = &exp
		}
	}

	return &schemas.BifrostFileRetrieveResponse{
		ID:             geminiResp.Name,
		Object:         "file",
		Bytes:          sizeBytes,
		CreatedAt:      createdAt,
		Filename:       geminiResp.DisplayName,
		Purpose:        schemas.FilePurposeVision,
		Status:         ToBifrostFileStatus(geminiResp.State),
		StorageBackend: schemas.FileStorageAPI,
		StorageURI:     geminiResp.URI,
		ExpiresAt:      expiresAt,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileRetrieveRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// FileDelete deletes a file from Gemini.
func (provider *GeminiProvider) FileDelete(ctx context.Context, key schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.FileDeleteRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL
	fileID := request.FileID
	if !strings.HasPrefix(fileID, "files/") {
		fileID = "files/" + fileID
	}
	requestURL := fmt.Sprintf("%s/%s", provider.networkConfig.BaseURL, fileID)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodDelete)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("x-goog-api-key", key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response - DELETE returns 200 with empty body on success
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusNoContent {
		return nil, parseGeminiError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			RequestType: schemas.FileDeleteRequest,
		})
	}

	return &schemas.BifrostFileDeleteResponse{
		ID:      request.FileID,
		Object:  "file",
		Deleted: true,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileDeleteRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// FileContent downloads file content from Gemini.
// Note: Gemini Files API doesn't support direct content download.
// Files are accessed via their URI in API requests.
func (provider *GeminiProvider) FileContent(ctx context.Context, key schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.FileContentRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Gemini doesn't support direct file content download
	// Files are referenced by their URI in requests
	return nil, providerUtils.NewBifrostOperationError(
		"Gemini Files API doesn't support direct content download. Use the file URI in your requests instead.",
		nil,
		providerName,
	)
}
