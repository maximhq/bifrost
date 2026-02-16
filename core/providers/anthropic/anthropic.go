// Package anthropic implements the Anthropic provider for the Bifrost API.
package anthropic

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
	sendBackRawRequest   bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// anthropicMessageResponsePool provides a pool for Anthropic chat response objects.
var anthropicMessageResponsePool = sync.Pool{
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

// AcquireAnthropicMessageResponse gets an Anthropic chat response from the pool.
func AcquireAnthropicMessageResponse() *AnthropicMessageResponse {
	resp := anthropicMessageResponsePool.Get().(*AnthropicMessageResponse)
	*resp = AnthropicMessageResponse{} // Reset the struct
	return resp
}

// ReleaseAnthropicMessageResponse returns an Anthropic chat response to the pool.
func ReleaseAnthropicMessageResponse(resp *AnthropicMessageResponse) {
	if resp != nil {
		anthropicMessageResponsePool.Put(resp)
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
		MaxIdleConnDuration: 30 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	// Pre-warm response pools
	for i := 0; i < config.ConcurrencyAndBufferSize.Concurrency; i++ {
		anthropicTextResponsePool.Put(&AnthropicTextResponse{})
		anthropicMessageResponsePool.Put(&AnthropicMessageResponse{})
	}

	// Configure proxy and retry policy
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client)
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
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}
}

// GetProviderKey returns the provider identifier for Anthropic.
func (provider *AnthropicProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Anthropic, provider.customProviderConfig)
}

// buildRequestURL constructs the full request URL using the provider's configuration.
func (provider *AnthropicProvider) buildRequestURL(ctx *schemas.BifrostContext, defaultPath string, requestType schemas.RequestType) string {
	path, isCompleteURL := providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
	if isCompleteURL {
		return path
	}
	return provider.networkConfig.BaseURL + path
}

// completeRequest sends a request to Anthropic's API and handles the response.
// It constructs the API URL, sets up authentication, and processes the response.
// Returns the response body or an error if the request fails.
func (provider *AnthropicProvider) completeRequest(ctx *schemas.BifrostContext, jsonData []byte, url string, key string, meta *providerUtils.RequestMetadata) ([]byte, time.Duration, map[string]string, *schemas.BifrostError) {
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
	// Here we can avoid this - in case of passthrough completely
	if key != "" && !IsClaudeCodeMaxMode(ctx) {
		req.Header.Set("x-api-key", key)
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	req.SetBody(jsonData)

	// Send the request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, latency, nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", provider.GetProviderKey(), string(resp.Body()))
		return nil, latency, nil, parseAnthropicError(resp, meta)
	}

	// Extract provider response headers before releasing the response
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, latency, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	// Read the response body and copy it before releasing the response
	// to avoid use-after-free since respBody references fasthttp's internal buffer
	bodyCopy := append([]byte(nil), body...)

	return bodyCopy, latency, providerResponseHeaders, nil
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *AnthropicProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
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
	if key.Value.GetValue() != "" {
		req.Header.Set("x-api-key", key.Value.GetValue())
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseAnthropicError(resp, &providerUtils.RequestMetadata{
			Provider:    provider.GetProviderKey(),
			RequestType: schemas.ListModelsRequest,
		})
	}

	// Parse Anthropic's response
	var anthropicResponse AnthropicListModelsResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &anthropicResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	response := anthropicResponse.ToBifrostListModelsResponse(provider.GetProviderKey(), key.Models)
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

// ListModels performs a list models request to Anthropic's API.
// It fetches models using all provided keys and aggregates the results.
// Uses a best-effort approach: continues with remaining keys even if some fail.
// Requests are made concurrently for improved performance.
func (provider *AnthropicProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
		return providerUtils.HandleKeylessListModelsRequest(schemas.Anthropic, func() (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
			return provider.listModelsByKey(ctx, schemas.Key{}, request)
		})
	}
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}

// TextCompletion performs a text completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.TextCompletionRequest); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToAnthropicTextCompletionRequest(request), nil
		},
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	// Use struct directly for JSON marshaling (no beta headers for text completion)
	responseBody, latency, providerResponseHeaders, err := provider.completeRequest(ctx, jsonData, provider.buildRequestURL(ctx, "/v1/complete", schemas.TextCompletionRequest), key.Value.GetValue(), &providerUtils.RequestMetadata{
		Provider:    provider.GetProviderKey(),
		Model:       request.Model,
		RequestType: schemas.TextCompletionRequest,
	})
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Create response object from pool
	response := acquireAnthropicTextResponse()
	defer releaseAnthropicTextResponse(response)

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	bifrostResponse := response.ToBifrostTextCompletionResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.TextCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// TextCompletionStream performs a streaming text completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a channel of BifrostStreamChunk objects or an error if the request fails.
func (provider *AnthropicProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}
	// Convert to Anthropic format and get required beta headers
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			anthropicReq, convErr := ToAnthropicChatRequest(ctx, request)
			if convErr != nil {
				return nil, convErr
			}
			addMissingBetaHeadersToContext(ctx, anthropicReq)
			return anthropicReq, nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Use struct directly for JSON marshaling
	responseBody, latency, providerResponseHeaders, err := provider.completeRequest(ctx, jsonData, provider.buildRequestURL(ctx, "/v1/messages", schemas.ChatCompletionRequest), key.Value.GetValue(), &providerUtils.RequestMetadata{
		Provider:    provider.GetProviderKey(),
		Model:       request.Model,
		RequestType: schemas.ChatCompletionRequest,
	})
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Create response object from pool
	response := AcquireAnthropicMessageResponse()
	defer ReleaseAnthropicMessageResponse(response)

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	// Create final response
	bifrostResponse := response.ToBifrostChatResponse(ctx)

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ChatCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ChatCompletionStream performs a streaming chat completion request to the Anthropic API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Returns a channel containing BifrostStreamChunk objects representing the stream or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	// Convert to Anthropic format and get required beta headers
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			anthropicReq, convErr := ToAnthropicChatRequest(ctx, request)
			if convErr != nil {
				return nil, convErr
			}
			anthropicReq.Stream = schemas.Ptr(true)
			addMissingBetaHeadersToContext(ctx, anthropicReq)
			return anthropicReq, nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Prepare Anthropic headers
	headers := map[string]string{
		"Content-Type":      "application/json",
		"anthropic-version": provider.apiVersion,
		"Accept":            "text/event-stream",
		"Cache-Control":     "no-cache",
	}

	if key.Value.GetValue() != "" && !IsClaudeCodeMaxMode(ctx) {
		headers["x-api-key"] = key.Value.GetValue()
	}

	// Use shared Anthropic streaming logic
	return HandleAnthropicChatCompletionStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/messages", schemas.ChatCompletionStreamRequest),
		jsonData,
		headers,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		provider.logger,
		&providerUtils.RequestMetadata{
			Provider:    provider.GetProviderKey(),
			Model:       request.Model,
			RequestType: schemas.ChatCompletionStreamRequest,
		},
	)
}

// HandleAnthropicChatCompletionStreaming handles streaming for Anthropic-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE event format.
func HandleAnthropicChatCompletionStreaming(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	jsonBody []byte,
	headers map[string]string,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	postResponseConverter func(*schemas.BifrostChatResponse) *schemas.BifrostChatResponse,
	logger schemas.Logger,
	meta *providerUtils.RequestMetadata,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true // Initialize for streaming
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	req.SetBody(jsonBody)

	// Make the request
	err := client.Do(req, resp)
	if err != nil {
		defer providerUtils.ReleaseStreamingResponse(resp)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, providerUtils.EnrichError(ctx, parseAnthropicError(resp, meta), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Store provider response headers in context for transport layer
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			model := "unknown"
			if meta != nil {
				model = meta.Model
			}
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, model, schemas.ChatCompletionStreamRequest, logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, model, schemas.ChatCompletionStreamRequest, logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)

		if resp.BodyStream() == nil {
			bifrostErr := providerUtils.NewBifrostOperationError(
				"Provider returned an empty response",
				fmt.Errorf("provider returned an empty response"),
				providerName,
			)
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse), responseChan, logger)
			return
		}

		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		scanner := bufio.NewScanner(resp.BodyStream())
		bufPtr := providerUtils.AcquireScannerBuf()
		scanner.Buffer((*bufPtr)[:0], 10*1024*1024)
		defer providerUtils.ReleaseScannerBuf(bufPtr)

		chunkIndex := 0

		startTime := time.Now()
		lastChunkTime := startTime

		// Track minimal state needed for response format
		var messageID string
		var modelName string
		var finishReason *string

		usage := &schemas.BifrostLLMUsage{}

		// Track SSE event parsing state
		var eventType string
		var eventData string

		// Check for structured output tool name and track state
		var structuredOutputToolName string
		var isAccumulatingStructuredOutput bool
		if toolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
			structuredOutputToolName = toolName
		}

		for scanner.Scan() {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
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
				logger.Warn("Failed to parse message_start event: %v", err)
				continue
			}
			if event.Type == AnthropicStreamEventTypeMessageStart && event.Message != nil && event.Message.ID != "" {
				messageID = event.Message.ID
			}
			// Check for usage in both top-level event.Usage and nested event.Message.Usage
			// message_start events have usage nested in message.usage, while message_delta has it at top level
			var usageToProcess *AnthropicUsage
			if event.Usage != nil {
				usageToProcess = event.Usage
			} else if event.Message != nil && event.Message.Usage != nil {
				usageToProcess = event.Message.Usage
			}
			if usageToProcess != nil {
				// Collect usage information and send at the end of the stream
				// Here in some cases usage comes before final message
				// So we need to check if the response.Usage is nil and then if usage != nil
				// then add up all tokens
				if usageToProcess.InputTokens > usage.PromptTokens {
					usage.PromptTokens = usageToProcess.InputTokens
				}
				if usageToProcess.OutputTokens > usage.CompletionTokens {
					usage.CompletionTokens = usageToProcess.OutputTokens
				}
				calculatedTotal := usage.PromptTokens + usage.CompletionTokens
				if calculatedTotal > usage.TotalTokens {
					usage.TotalTokens = calculatedTotal
				}
				// Handle cached tokens if present
				if usageToProcess.CacheReadInputTokens > 0 {
					if usage.PromptTokensDetails == nil {
						usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
					}
					if usageToProcess.CacheReadInputTokens > usage.PromptTokensDetails.CachedTokens {
						usage.PromptTokensDetails.CachedTokens = usageToProcess.CacheReadInputTokens
					}
				}
				// Handle cached tokens if present
				if usageToProcess.CacheCreationInputTokens > 0 {
					if usage.CompletionTokensDetails == nil {
						usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{}
					}
					if usageToProcess.CacheCreationInputTokens > usage.CompletionTokensDetails.CachedTokens {
						usage.CompletionTokensDetails.CachedTokens = usageToProcess.CacheCreationInputTokens
					}
				}
			}
			if event.Message != nil {
				// Handle different event types
				modelName = event.Message.Model
			}

			// Extract finish reason from event delta
			if event.Delta != nil && event.Delta.StopReason != nil {
				mappedReason := ConvertAnthropicFinishReasonToBifrost(*event.Delta.StopReason)
				finishReason = &mappedReason

				// Override finish reason for structured output
				// When structured output is used, tool_use stop reason should appear as "stop" to the client
				if structuredOutputToolName != "" && *finishReason == string(schemas.BifrostFinishReasonToolCalls) {
					stopReason := string(schemas.BifrostFinishReasonStop)
					finishReason = &stopReason
				}
			}

			// Handle structured output: intercept tool calls for the structured output tool
			// and convert them to content instead of forwarding as tool calls
			if structuredOutputToolName != "" {
				// Check for tool use start event
				if event.Type == AnthropicStreamEventTypeContentBlockStart {
					if event.ContentBlock != nil && event.ContentBlock.Type == AnthropicContentBlockTypeToolUse {
						if event.ContentBlock.Name != nil && *event.ContentBlock.Name == structuredOutputToolName {
							isAccumulatingStructuredOutput = true
							continue
						}
					}
				}

				// Check for tool use delta event
				if event.Type == AnthropicStreamEventTypeContentBlockDelta && isAccumulatingStructuredOutput {
					if event.Delta != nil && event.Delta.Type == AnthropicStreamDeltaTypeInputJSON && event.Delta.PartialJSON != nil {
						// Convert tool use delta to content delta
						content := *event.Delta.PartialJSON
						response := &schemas.BifrostChatResponse{
							ID:     messageID,
							Object: "chat.completion.chunk",
							Choices: []schemas.BifrostResponseChoice{
								{
									Index: 0,
									ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
										Delta: &schemas.ChatStreamResponseChoiceDelta{
											Content: &content,
										},
									},
								},
							},
							ExtraFields: schemas.BifrostResponseExtraFields{
								RequestType:    schemas.ChatCompletionStreamRequest,
								Provider:       providerName,
								ModelRequested: modelName,
								ChunkIndex:     chunkIndex,
								Latency:        time.Since(lastChunkTime).Milliseconds(),
							},
						}
						lastChunkTime = time.Now()
						chunkIndex++

						if sendBackRawResponse {
							response.ExtraFields.RawResponse = eventData
						}

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
						continue
					}
				}

				// Check for content block stop
				if event.Type == AnthropicStreamEventTypeContentBlockStop && isAccumulatingStructuredOutput {
					isAccumulatingStructuredOutput = false
					continue
				}
			}

			response, bifrostErr, isLastChunk := event.ToBifrostChatCompletionStream(ctx, structuredOutputToolName)
			if bifrostErr != nil {
				bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
					RequestType:    schemas.ChatCompletionStreamRequest,
					Provider:       providerName,
					ModelRequested: modelName,
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				break
			}
			if response != nil {
				response.ExtraFields = schemas.BifrostResponseExtraFields{
					RequestType:    schemas.ChatCompletionStreamRequest,
					Provider:       providerName,
					ModelRequested: modelName,
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
				response.ID = messageID
				lastChunkTime = time.Now()
				chunkIndex++

				if sendBackRawResponse {
					response.ExtraFields.RawResponse = eventData
				}

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
			}
			if isLastChunk {
				break
			}
			// Reset for next event
			eventType = ""
			eventData = ""
		}
		if err := scanner.Err(); err != nil {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			logger.Warn("Error reading %s stream: %v", providerName, err)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, modelName, logger)
			return
		}
		response := providerUtils.CreateBifrostChatCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, schemas.ChatCompletionStreamRequest, providerName, modelName)
		if postResponseConverter != nil {
			response = postResponseConverter(response)
			if response == nil {
				logger.Warn("postResponseConverter returned nil; skipping chunk")
				// Setting error on the context to signal to the defer that we need to close the stream
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				return
			}
		}
		// Set raw request if enabled
		if sendBackRawRequest {
			providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
		}
		response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
	}()

	return responseChan, nil
}

// Responses performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}
	jsonBody, err := getRequestBodyForResponses(ctx, request, provider.GetProviderKey(), false)
	if err != nil {
		return nil, err
	}

	// Use struct directly for JSON marshaling
	responseBody, latency, providerResponseHeaders, err := provider.completeRequest(ctx, jsonBody, provider.buildRequestURL(ctx, "/v1/messages", schemas.ResponsesRequest), key.Value.GetValue(), &providerUtils.RequestMetadata{
		Provider:    provider.GetProviderKey(),
		Model:       request.Model,
		RequestType: schemas.ResponsesRequest,
	})
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Create response object from pool
	response := AcquireAnthropicMessageResponse()
	defer ReleaseAnthropicMessageResponse(response)

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Create final response
	bifrostResponse := response.ToBifrostResponsesResponse(ctx)

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ResponsesRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ResponsesStream performs a streaming responses request to the Anthropic API.
func (provider *AnthropicProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	jsonBody, err := getRequestBodyForResponses(ctx, request, provider.GetProviderKey(), true)
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

	if key.Value.GetValue() != "" && !IsClaudeCodeMaxMode(ctx) {
		headers["x-api-key"] = key.Value.GetValue()
	}

	return HandleAnthropicResponsesStream(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/messages", schemas.ResponsesStreamRequest),
		jsonBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		provider.logger,
		&providerUtils.RequestMetadata{
			Provider:    provider.GetProviderKey(),
			Model:       request.Model,
			RequestType: schemas.ResponsesStreamRequest,
		},
	)
}

// HandleAnthropicResponsesStream handles streaming for Anthropic-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE event format.
func HandleAnthropicResponsesStream(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	jsonBody []byte,
	headers map[string]string,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	postResponseConverter func(*schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse,
	logger schemas.Logger,
	meta *providerUtils.RequestMetadata,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	// Set headers, merging anthropic-beta with any user-supplied values
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Set body
	req.SetBody(jsonBody)

	// Make the request
	err := client.Do(req, resp)
	if err != nil {
		defer providerUtils.ReleaseStreamingResponse(resp)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, providerUtils.EnrichError(ctx, parseAnthropicError(resp, meta), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Store provider response headers in context for transport layer
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			model := "<unknown>"
			if meta != nil {
				model = meta.Model
			}
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, model, schemas.ResponsesStreamRequest, logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, model, schemas.ResponsesStreamRequest, logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()
		// If body stream is nil, return an error
		if resp.BodyStream() == nil {
			bifrostErr := providerUtils.NewBifrostOperationError(
				"Provider returned an empty response",
				fmt.Errorf("provider returned an empty response"),
				providerName,
			)
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse), responseChan, logger)
			return
		}

		scanner := bufio.NewScanner(resp.BodyStream())
		chunkIndex := 0

		startTime := time.Now()
		lastChunkTime := startTime

		// Track minimal state needed for response format
		usage := &schemas.ResponsesResponseUsage{}

		// Create stream state for stateful conversions
		streamState := acquireAnthropicResponsesStreamState()
		defer releaseAnthropicResponsesStreamState(streamState)

		// Set structured output tool name if present
		if toolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
			streamState.StructuredOutputToolName = toolName
		}

		// Track SSE event parsing state
		var eventType string
		var eventData string
		var modelName string

		for scanner.Scan() {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
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
				logger.Warn("Failed to parse message_start event: %v", err)
				continue
			}
			if event.Message != nil && modelName == "" {
				modelName = event.Message.Model
			}
			// Note: response.created and response.in_progress are now emitted by ToBifrostResponsesStream
			// from the message_start event, so we don't need to call them manually here

			// Check for usage in both top-level event.Usage and nested event.Message.Usage
			// message_start events have usage nested in message.usage, while message_delta has it at top level
			var usageToProcess *AnthropicUsage
			if event.Usage != nil {
				usageToProcess = event.Usage
			} else if event.Message != nil && event.Message.Usage != nil {
				usageToProcess = event.Message.Usage
			}

			if usageToProcess != nil {
				// Collect usage information and send at the end of the stream
				// Here in some cases usage comes before final message
				// So we need to check if the response.Usage is nil and then if usage != nil
				// then add up all tokens
				if usageToProcess.InputTokens > usage.InputTokens {
					usage.InputTokens = usageToProcess.InputTokens
				}
				if usageToProcess.OutputTokens > usage.OutputTokens {
					usage.OutputTokens = usageToProcess.OutputTokens
				}
				calculatedTotal := usage.InputTokens + usage.OutputTokens
				if calculatedTotal > usage.TotalTokens {
					usage.TotalTokens = calculatedTotal
				}
				// Handle cached tokens if present
				if usageToProcess.CacheReadInputTokens > 0 {
					if usage.InputTokensDetails == nil {
						usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
					}
					if usageToProcess.CacheReadInputTokens > usage.InputTokensDetails.CachedTokens {
						usage.InputTokensDetails.CachedTokens = usageToProcess.CacheReadInputTokens
					}
				}
				// Handle cached tokens if present
				if usageToProcess.CacheCreationInputTokens > 0 {
					if usage.OutputTokensDetails == nil {
						usage.OutputTokensDetails = &schemas.ResponsesResponseOutputTokens{}
					}
					if usageToProcess.CacheCreationInputTokens > usage.OutputTokensDetails.CachedTokens {
						usage.OutputTokensDetails.CachedTokens = usageToProcess.CacheCreationInputTokens
					}
				}
			}

			responses, bifrostErr, isLastChunk := event.ToBifrostResponsesStream(ctx, chunkIndex, streamState)
			if bifrostErr != nil {
				bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
					RequestType:    schemas.ResponsesStreamRequest,
					Provider:       providerName,
					ModelRequested: modelName,
				}
				// If context was cancelled/timed out, let defer handle it
				if ctx.Err() != nil {
					return
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				break
			}
			// Handle each response in the slice
			for i, response := range responses {
				if response != nil {
					response.ExtraFields = schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ResponsesStreamRequest,
						Provider:       providerName,
						ModelRequested: modelName,
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
					lastChunkTime = time.Now()
					chunkIndex++

					// Only add raw response to the last chunk of the incoming event
					if providerUtils.ShouldSendBackRawResponse(ctx, sendBackRawResponse) && i == len(responses)-1 {
						response.ExtraFields.RawResponse = eventData
					}

					if isLastChunk && i == len(responses)-1 {
						if response.Response == nil {
							response.Response = &schemas.BifrostResponsesResponse{}
						}
						response.Response.Usage = usage
						// Set raw request if enabled
						if sendBackRawRequest {
							providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
						}
						response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan)
						return
					}
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan)
				}
			}

			// Reset for next event
			eventType = ""
			eventData = ""
		}
		if err := scanner.Err(); err != nil {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			logger.Warn("Error reading %s stream: %v", providerName, err)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, modelName, logger)
		}
	}()

	return responseChan, nil
}

// BatchCreate creates a new batch job.
func (provider *AnthropicProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchCreateRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(request.Requests) == 0 {
		return nil, providerUtils.NewBifrostOperationError("requests array is required for Anthropic batch API", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/messages/batches", schemas.BatchCreateRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("x-api-key", key.Value.GetValue())
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Build request body
	anthropicReq := &AnthropicBatchCreateRequest{
		Requests: make([]AnthropicBatchRequestItem, len(request.Requests)),
	}

	for i, r := range request.Requests {
		anthropicReq.Requests[i] = AnthropicBatchRequestItem{
			CustomID: r.CustomID,
			Params:   r.Params,
		}
		// Use Body if Params is empty
		if anthropicReq.Requests[i].Params == nil && r.Body != nil {
			anthropicReq.Requests[i].Params = r.Body
		}
	}

	jsonData, err := sonic.Marshal(anthropicReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}
	req.SetBody(jsonData)

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, ParseAnthropicError(resp, schemas.BatchCreateRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	var anthropicResp AnthropicBatchResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse)
	}

	return anthropicResp.ToBifrostBatchCreateResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// BatchList lists batch jobs using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *AnthropicProvider) BatchList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// Initialize serial pagination helper (Anthropic uses AfterID for pagination)
	helper, err := providerUtils.NewSerialListHelper(keys, request.AfterID, provider.logger)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err, providerName)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostBatchListResponse{
			Object:  "list",
			Data:    []schemas.BifrostBatchRetrieveResponse{},
			HasMore: false,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchListRequest,
				Provider:    providerName,
			},
		}, nil
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := provider.buildRequestURL(ctx, "/v1/messages/batches", schemas.BatchListRequest)
	values := url.Values{}
	if request.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	if request.BeforeID != nil && *request.BeforeID != "" {
		values.Set("before_id", *request.BeforeID)
	}
	// Use native cursor from serial helper instead of request.AfterID
	if nativeCursor != "" {
		values.Set("after_id", nativeCursor)
	}
	requestURL := baseURL
	if encodedValues := values.Encode(); encodedValues != "" {
		requestURL += "?" + encodedValues
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("x-api-key", key.Value.GetValue())
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, ParseAnthropicError(resp, schemas.BatchListRequest, providerName, "")
	}

	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr, providerName)
	}

	var anthropicResp AnthropicBatchListResponse
	_, _, bifrostErr = providerUtils.HandleProviderResponse(body, &anthropicResp, nil, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert batches to Bifrost format
	batches := make([]schemas.BifrostBatchRetrieveResponse, 0, len(anthropicResp.Data))
	var lastBatchID string
	for _, batch := range anthropicResp.Data {
		batches = append(batches, *batch.ToBifrostBatchRetrieveResponse(providerName, latency, false, false, nil, nil))
		lastBatchID = batch.ID
	}

	// Build cursor for next request
	// Anthropic uses LastID as the cursor for pagination
	nextCursor, hasMore := helper.BuildNextCursor(anthropicResp.HasMore, lastBatchID)

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostBatchListResponse{
		Object:  "list",
		Data:    batches,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}
	if nextCursor != "" {
		bifrostResp.NextCursor = &nextCursor
	}

	return bifrostResp, nil
}

// BatchRetrieve retrieves a specific batch job by trying each key until found.
func (provider *AnthropicProvider) BatchRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchRetrieveRequest); err != nil {
		return nil, err
	}

	// batch id is required
	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, schemas.Anthropic)
	}

	providerName := provider.GetProviderKey()
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.buildRequestURL(
			ctx,
			"/v1/messages/batches/"+url.PathEscape(request.BatchID),
			schemas.BatchRetrieveRequest,
		))
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = ParseAnthropicError(resp, schemas.BatchRetrieveRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		var anthropicResp AnthropicBatchResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := anthropicResp.ToBifrostBatchRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse)
		result.ExtraFields.RequestType = schemas.BatchRetrieveRequest
		return result, nil
	}

	return nil, lastErr
}

// BatchCancel cancels a batch job by trying each key until successful.
func (provider *AnthropicProvider) BatchCancel(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchCancelRequest); err != nil {
		return nil, err
	}

	// batch id is required
	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, schemas.Anthropic)
	}

	providerName := provider.GetProviderKey()
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/messages/batches/" + request.BatchID + "/cancel")
		req.Header.SetMethod(http.MethodPost)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = ParseAnthropicError(resp, schemas.BatchCancelRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		var anthropicResp AnthropicBatchResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := &schemas.BifrostBatchCancelResponse{
			ID:     anthropicResp.ID,
			Object: anthropicResp.Type,
			Status: ToBifrostBatchStatus(anthropicResp.ProcessingStatus),
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchCancelRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}

		if sendBackRawRequest {
			result.ExtraFields.RawRequest = rawRequest
		}

		if anthropicResp.CancelInitiatedAt != nil {
			cancellingAt := parseAnthropicTimestamp(*anthropicResp.CancelInitiatedAt)
			result.CancellingAt = &cancellingAt
		}

		if anthropicResp.RequestCounts != nil {
			result.RequestCounts = schemas.BatchRequestCounts{
				Total:     anthropicResp.RequestCounts.Processing + anthropicResp.RequestCounts.Succeeded + anthropicResp.RequestCounts.Errored + anthropicResp.RequestCounts.Canceled + anthropicResp.RequestCounts.Expired,
				Completed: anthropicResp.RequestCounts.Succeeded,
				Failed:    anthropicResp.RequestCounts.Errored,
			}
		}

		if sendBackRawResponse {
			result.ExtraFields.RawResponse = rawResponse
		}

		return result, nil
	}

	return nil, lastErr
}

// BatchResults retrieves batch results by trying each key until found.
func (provider *AnthropicProvider) BatchResults(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchResultsRequest); err != nil {
		return nil, err
	}

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, schemas.Anthropic)
	}

	providerName := provider.GetProviderKey()

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/messages/batches/" + request.BatchID + "/results")
		req.Header.SetMethod(http.MethodGet)

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = ParseAnthropicError(resp, schemas.BatchResultsRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		// Parse JSONL content - each line is a separate result
		var results []schemas.BatchResultItem

		parseResult := providerUtils.ParseJSONL(body, func(line []byte) error {
			var anthropicResult AnthropicBatchResultItem
			if err := sonic.Unmarshal(line, &anthropicResult); err != nil {
				provider.logger.Warn("failed to parse batch result line: %v", err)
				return err
			}

			// Convert to Bifrost format
			resultItem := schemas.BatchResultItem{
				CustomID: anthropicResult.CustomID,
				Result: &schemas.BatchResultData{
					Type:    anthropicResult.Result.Type,
					Message: anthropicResult.Result.Message,
				},
			}

			if anthropicResult.Result.Error != nil {
				resultItem.Error = &schemas.BatchResultError{
					Code:    anthropicResult.Result.Error.Type,
					Message: anthropicResult.Result.Error.Message,
				}
			}

			results = append(results, resultItem)
			return nil
		})

		batchResultsResp := &schemas.BifrostBatchResultsResponse{
			BatchID: request.BatchID,
			Results: results,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchResultsRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}

		if len(parseResult.Errors) > 0 {
			batchResultsResp.ExtraFields.ParseErrors = parseResult.Errors
		}

		return batchResultsResp, nil
	}

	return nil, lastErr
}

// splitJSONL splits JSONL content into individual lines.
func splitJSONL(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// Embedding is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, input *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Speech is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// FileUpload uploads a file to Anthropic's Files API.
func (provider *AnthropicProvider) FileUpload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileUploadRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("file content is required", nil, providerName)
	}

	// Create multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file field
	filename := request.Filename
	if filename == "" {
		filename = "file"
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

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/files", schemas.FileUploadRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())

	if key.Value.GetValue() != "" {
		req.Header.Set("x-api-key", key.Value.GetValue())
	}
	req.Header.Set("anthropic-version", provider.apiVersion)
	appendBetaHeader(req, AnthropicFilesAPIBetaHeader)
	req.SetBody(buf.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, ParseAnthropicError(resp, schemas.FileUploadRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var anthropicResp AnthropicFileResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return anthropicResp.ToBifrostFileUploadResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// FileList lists files from all provided keys and aggregates results.
// FileList lists files using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *AnthropicProvider) FileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// Initialize serial pagination helper
	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err, providerName)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostFileListResponse{
			Object:  "list",
			Data:    []schemas.FileObject{},
			HasMore: false,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileListRequest,
				Provider:    providerName,
			},
		}, nil
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	requestURL := provider.buildRequestURL(ctx, "/v1/files", schemas.FileListRequest)
	values := url.Values{}
	if request.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from serial helper instead of request.After
	if nativeCursor != "" {
		values.Set("after_id", nativeCursor)
	}
	if encodedValues := values.Encode(); encodedValues != "" {
		requestURL += "?" + encodedValues
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("x-api-key", key.Value.GetValue())
	}
	req.Header.Set("anthropic-version", provider.apiVersion)
	appendBetaHeader(req, AnthropicFilesAPIBetaHeader)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, ParseAnthropicError(resp, schemas.FileListRequest, providerName, "")
	}

	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr, providerName)
	}

	var anthropicResp AnthropicFileListResponse
	_, _, bifrostErr = providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert files to Bifrost format
	files := make([]schemas.FileObject, 0, len(anthropicResp.Data))
	var lastFileID string
	for _, file := range anthropicResp.Data {
		files = append(files, schemas.FileObject{
			ID:        file.ID,
			Object:    file.Type,
			Bytes:     file.SizeBytes,
			CreatedAt: parseAnthropicFileTimestamp(file.CreatedAt),
			Filename:  file.Filename,
			Purpose:   schemas.FilePurposeBatch,
			Status:    schemas.FileStatusProcessed,
		})
		lastFileID = file.ID
	}

	// Build cursor for next request
	// Anthropic uses LastID as the cursor for pagination
	nextCursor, hasMore := helper.BuildNextCursor(anthropicResp.HasMore, lastFileID)

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  "list",
		Data:    files,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}
	if nextCursor != "" {
		bifrostResp.After = &nextCursor
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves file metadata from Anthropic's Files API by trying each key until found.
func (provider *AnthropicProvider) FileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.buildRequestURL(
			ctx,
			"/v1/files/"+url.PathEscape(request.FileID),
			schemas.FileRetrieveRequest,
		))
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)
		appendBetaHeader(req, AnthropicFilesAPIBetaHeader)

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = ParseAnthropicError(resp, schemas.FileRetrieveRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		var anthropicResp AnthropicFileResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		return anthropicResp.ToBifrostFileRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
	}

	return nil, lastErr
}

// FileDelete deletes a file from Anthropic's Files API by trying each key until successful.
func (provider *AnthropicProvider) FileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileDeleteRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files/" + request.FileID)
		req.Header.SetMethod(http.MethodDelete)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)
		appendBetaHeader(req, AnthropicFilesAPIBetaHeader)

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusNoContent {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = ParseAnthropicError(resp, schemas.FileDeleteRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		// For 204 No Content, return success without parsing body
		if resp.StatusCode() == fasthttp.StatusNoContent {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
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

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		var anthropicResp AnthropicFileDeleteResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := &schemas.BifrostFileDeleteResponse{
			ID:      anthropicResp.ID,
			Object:  "file",
			Deleted: anthropicResp.Type == "file_deleted",
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileDeleteRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}

		if sendBackRawRequest {
			result.ExtraFields.RawRequest = rawRequest
		}

		if sendBackRawResponse {
			result.ExtraFields.RawResponse = rawResponse
		}

		return result, nil
	}

	return nil, lastErr
}

// FileContent downloads file content from Anthropic's Files API by trying each key until found.
// Note: Only files created by skills or the code execution tool can be downloaded.
func (provider *AnthropicProvider) FileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileContentRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files/" + request.FileID + "/content")
		req.Header.SetMethod(http.MethodGet)

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)
		appendBetaHeader(req, AnthropicFilesAPIBetaHeader)
		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = ParseAnthropicError(resp, schemas.FileContentRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		// Get content type from response
		contentType := string(resp.Header.ContentType())
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		content := append([]byte(nil), body...)
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		return &schemas.BifrostFileContentResponse{
			FileID:      request.FileID,
			Content:     content,
			ContentType: contentType,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileContentRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}, nil
	}

	return nil, lastErr
}

// CountTokens counts tokens for a given request using Anthropic's API.
func (provider *AnthropicProvider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.CountTokensRequest); err != nil {
		return nil, err
	}

	// Convert to Anthropic format and get required beta headers
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			anthropicReq, convErr := ToAnthropicResponsesRequest(ctx, request)
			if convErr != nil {
				return nil, convErr
			}
			addMissingBetaHeadersToContext(ctx, anthropicReq)
			return anthropicReq, nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Remove max_tokens and temperature for count_tokens endpoint
	var payload map[string]any
	if err := sonic.Unmarshal(jsonData, &payload); err == nil {
		delete(payload, "max_tokens")
		delete(payload, "temperature")
		newData, err := sonic.Marshal(payload)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, provider.GetProviderKey())
		}
		jsonData = newData
	}

	responseBody, latency, providerResponseHeaders, bifrostErr := provider.completeRequest(ctx, jsonData, provider.buildRequestURL(ctx, "/v1/messages/count_tokens", schemas.CountTokensRequest), key.Value.GetValue(), &providerUtils.RequestMetadata{
		Provider:    provider.GetProviderKey(),
		Model:       request.Model,
		RequestType: schemas.CountTokensRequest,
	})
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	anthropicResponse := &AnthropicCountTokensResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(
		responseBody,
		anthropicResponse,
		jsonData,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
	)

	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	response := anthropicResponse.ToBifrostCountTokensResponse(request.Model)
	response.Model = request.Model

	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.RequestType = schemas.CountTokensRequest
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ContainerCreate is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}
