// Package openai provides the OpenAI provider implementation for the Bifrost framework.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// OpenAIProvider implements the Provider interface for OpenAI's GPT API.
type OpenAIProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawRequest   bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewOpenAIProvider creates a new OpenAI provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewOpenAIProvider(config *schemas.ProviderConfig, logger schemas.Logger) *OpenAIProvider {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:         time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:        time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     5000,
		MaxIdleConnDuration: 60 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	// // Pre-warm response pools
	// for range config.ConcurrencyAndBufferSize.Concurrency {
	// 	openAIResponsePool.Put(&schemas.BifrostResponse{})
	// }

	// Configure proxy if provided
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.openai.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &OpenAIProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}
}

// GetProviderKey returns the provider identifier for OpenAI.
func (provider *OpenAIProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.OpenAI, provider.customProviderConfig)
}

// buildRequestURL constructs the full request URL using the provider's configuration.
func (provider *OpenAIProvider) buildRequestURL(ctx *schemas.BifrostContext, defaultPath string, requestType schemas.RequestType) string {
	return provider.networkConfig.BaseURL + providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
}

func (provider *OpenAIProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
		return listModelsByKey(
			ctx,
			provider.client,
			provider.buildRequestURL(ctx, "/v1/models", schemas.ListModelsRequest),
			schemas.Key{},
			provider.networkConfig.ExtraHeaders,
			providerName,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		)
	}

	return HandleOpenAIListModelsRequest(ctx,
		provider.client,
		request,
		provider.buildRequestURL(ctx, "/v1/models", schemas.ListModelsRequest),
		keys,
		provider.networkConfig.ExtraHeaders,
		providerName,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func listModelsByKey(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		bifrostErr := ParseOpenAIError(resp, schemas.ListModelsRequest, providerName, "")
		return nil, bifrostErr
	}

	// Copy response body before releasing
	responseBody := append([]byte(nil), resp.Body()...)

	openaiResponse := &OpenAIListModelsResponse{}

	// Use enhanced response handler with pre-allocated response
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, openaiResponse, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := openaiResponse.ToBifrostListModelsResponse(providerName, key.Models)

	response.ExtraFields.Provider = providerName
	response.ExtraFields.RequestType = schemas.ListModelsRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// HandleOpenAIListModelsRequest handles a list models request to OpenAI's API.
func HandleOpenAIListModelsRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	request *schemas.BifrostListModelsRequest,
	url string,
	keys []schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	logger schemas.Logger,
) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return listModelsByKey(ctx, client, url, schemas.Key{}, extraHeaders, providerName, sendBackRawRequest, sendBackRawResponse)
	}
	listModelsByKeyWrapper := func(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
		return listModelsByKey(ctx, client, url, key, extraHeaders, providerName, sendBackRawRequest, sendBackRawResponse)
	}
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		listModelsByKeyWrapper,
		logger,
	)
}

// TextCompletion is not supported by the OpenAI provider.
// Returns an error indicating that text completion is not available.
func (provider *OpenAIProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.TextCompletionRequest); err != nil {
		return nil, err
	}
	return HandleOpenAITextCompletionRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/completions", schemas.TextCompletionRequest),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		nil,
		provider.logger,
	)
}

// HandleOpenAITextCompletionRequest handles a text completion request to OpenAI's API.
func HandleOpenAITextCompletionRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostTextCompletionRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	customErrorConverter ErrorConverter,
	logger schemas.Logger,
) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToOpenAITextCompletionRequest(request), nil },
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		if customErrorConverter != nil {
			return nil, providerUtils.EnrichError(ctx, customErrorConverter(resp, schemas.TextCompletionRequest, providerName, request.Model), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.TextCompletionRequest, providerName, request.Model), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	response := &schemas.BifrostTextCompletionResponse{}

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse)
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.TextCompletionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// TextCompletionStream performs a streaming text completion request to OpenAI's API.
// It formats the request, sends it to OpenAI, and processes the response.
// Returns a channel of BifrostStreamChunk objects or an error if the request fails.
func (provider *OpenAIProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.TextCompletionStreamRequest); err != nil {
		return nil, err
	}
	var authHeader map[string]string
	if key.Value.GetValue() != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
	}
	return HandleOpenAITextCompletionStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/completions", schemas.TextCompletionStreamRequest),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		postHookRunner,
		nil,
		provider.logger,
	)
}

// HandleOpenAITextCompletionStreaming handles text completion streaming for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE format.
func HandleOpenAITextCompletionStreaming(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostTextCompletionRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	customErrorConverter ErrorConverter,
	postHookRunner schemas.PostHookRunner,
	postResponseConverter func(*schemas.BifrostTextCompletionResponse) *schemas.BifrostTextCompletionResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if authHeader != nil {
		maps.Copy(headers, authHeader)
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := ToOpenAITextCompletionRequest(request)
			if reqBody != nil {
				reqBody.Stream = schemas.Ptr(true)
				reqBody.StreamOptions = &schemas.ChatStreamOptions{
					IncludeUsage: schemas.Ptr(true),
				}
			}
			return reqBody, nil
		},
		providerName)

	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	// Set headers
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
		if customErrorConverter != nil {
			return nil, providerUtils.EnrichError(ctx, customErrorConverter(resp, schemas.TextCompletionStreamRequest, providerName, request.Model), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.TextCompletionStreamRequest, providerName, request.Model), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.TextCompletionStreamRequest, logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.TextCompletionStreamRequest, logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		scanner := bufio.NewScanner(resp.BodyStream())
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		chunkIndex := -1
		usage := &schemas.BifrostLLMUsage{}

		var finishReason *string
		var messageID string
		startTime := time.Now()
		lastChunkTime := startTime

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

			// Check for end of stream
			if line == "data: [DONE]" {
				break
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
			var bifrostErr schemas.BifrostError
			if err := sonic.Unmarshal([]byte(jsonData), &bifrostErr); err == nil {
				if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						Provider:       providerName,
						ModelRequested: request.Model,
						RequestType:    schemas.TextCompletionStreamRequest,
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, &bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse), responseChan, logger)
					return
				}
			}

			// Parse into bifrost response
			var response schemas.BifrostTextCompletionResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn("Failed to parse stream response: %v", err)
				continue
			}

			if postResponseConverter != nil {
				if converted := postResponseConverter(&response); converted != nil {
					response = *converted
				} else {
					logger.Warn("postResponseConverter returned nil; leaving chunk unmodified")
				}
			}

			// Handle usage-only chunks (when stream_options include_usage is true)
			if response.Usage != nil {
				// Collect usage information and send at the end of the stream
				// Here in some cases usage comes before final message
				// So we need to check if the response.Usage is nil and then if usage != nil
				// then add up all tokens
				if response.Usage.PromptTokens > usage.PromptTokens {
					usage.PromptTokens = response.Usage.PromptTokens
				}
				if response.Usage.CompletionTokens > usage.CompletionTokens {
					usage.CompletionTokens = response.Usage.CompletionTokens
				}
				if response.Usage.TotalTokens > usage.TotalTokens {
					usage.TotalTokens = response.Usage.TotalTokens
				}
				calculatedTotal := usage.PromptTokens + usage.CompletionTokens
				if calculatedTotal > usage.TotalTokens {
					usage.TotalTokens = calculatedTotal
				}
				if response.Usage.CompletionTokensDetails != nil {
					usage.CompletionTokensDetails = response.Usage.CompletionTokensDetails
				}
				if response.Usage.PromptTokensDetails != nil {
					usage.PromptTokensDetails = response.Usage.PromptTokensDetails
				}
				response.Usage = nil
			}

			// Skip empty responses or responses without choices
			if len(response.Choices) == 0 {
				continue
			}

			// Handle finish reason, usually in the final chunk
			choice := response.Choices[0]
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				// Collect finish reason and send at the end of the stream
				finishReason = choice.FinishReason
				response.Choices[0].FinishReason = nil
			}

			if response.ID != "" && messageID == "" {
				messageID = response.ID
			}

			// Handle regular content chunks
			if choice.TextCompletionResponseChoice != nil && choice.TextCompletionResponseChoice.Text != nil {
				chunkIndex++

				response.ExtraFields.RequestType = schemas.TextCompletionStreamRequest
				response.ExtraFields.Provider = providerName
				response.ExtraFields.ModelRequested = request.Model
				response.ExtraFields.ChunkIndex = chunkIndex
				response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
				lastChunkTime = time.Now()

				if sendBackRawResponse {
					response.ExtraFields.RawResponse = jsonData
				}

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(&response, nil, nil, nil, nil, nil), responseChan)
			}

			// For providers that don't send [DONE] marker break on finish_reason
			if !providerUtils.ProviderSendsDoneMarker(providerName) && finishReason != nil {
				break
			}
		}

		// Handle scanner errors first
		if err := scanner.Err(); err != nil {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			logger.Warn("Error reading stream: %v", err)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.TextCompletionStreamRequest, providerName, request.Model, logger)
			return
		}

		response := providerUtils.CreateBifrostTextCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, schemas.TextCompletionStreamRequest, providerName, request.Model)
		if postResponseConverter != nil {
			response = postResponseConverter(response)
			if response == nil {
				logger.Warn("postResponseConverter returned nil; leaving chunk unmodified")
				return
			}
		}
		// Set raw request if enabled
		if sendBackRawRequest {
			providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
		}
		response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(response, nil, nil, nil, nil, nil), responseChan)
	}()

	return responseChan, nil
}

// ChatCompletion performs a chat completion request to the OpenAI API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *OpenAIProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	return HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/chat/completions", schemas.ChatCompletionRequest),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		provider.logger,
	)
}

// HandleOpenAIChatCompletionRequest handles a chat completion request to OpenAI's API.
func HandleOpenAIChatCompletionRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostChatRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	customErrorConverter ErrorConverter,
	logger schemas.Logger,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToOpenAIChatRequest(ctx, request), nil },
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		if customErrorConverter != nil {
			return nil, providerUtils.EnrichError(ctx, customErrorConverter(resp, schemas.ChatCompletionRequest, providerName, request.Model), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.ChatCompletionRequest, providerName, request.Model), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	response := &schemas.BifrostChatResponse{}

	// Use enhanced response handler with pre-allocated response
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse)
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ChatCompletionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ChatCompletionStream handles streaming for OpenAI chat completions.
// It formats messages, prepares request body, and uses shared streaming logic.
// Returns a channel for streaming responses and any error that occurred.
func (provider *OpenAIProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	// Check if chat completion stream is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}
	var authHeader map[string]string
	if key.Value.GetValue() != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
	}
	// Use shared streaming logic
	return HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/chat/completions", schemas.ChatCompletionStreamRequest),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// HandleOpenAIChatCompletionStreaming handles streaming for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE format.
func HandleOpenAIChatCompletionStreaming(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostChatRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	customRequestConverter func(*schemas.BifrostChatRequest) (any, error),
	customErrorConverter ErrorConverter,
	postRequestConverter func(*OpenAIChatRequest) *OpenAIChatRequest,
	postResponseConverter func(*schemas.BifrostChatResponse) *schemas.BifrostChatResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	// Check if the request is a redirect from ResponsesStream to ChatCompletionStream
	isResponsesToChatCompletionsFallback := false
	var responsesStreamState *schemas.ChatToResponsesStreamState
	if ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback) != nil {
		isResponsesToChatCompletionsFallbackValue, ok := ctx.Value(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback).(bool)
		if ok && isResponsesToChatCompletionsFallbackValue {
			isResponsesToChatCompletionsFallback = true
			responsesStreamState = schemas.AcquireChatToResponsesStreamState()
		}
	}

	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if authHeader != nil {
		// Copy auth header to headers
		maps.Copy(headers, authHeader)
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			if customRequestConverter != nil {
				return customRequestConverter(request)
			}
			reqBody := ToOpenAIChatRequest(ctx, request)
			if reqBody != nil {
				reqBody.Stream = schemas.Ptr(true)
				reqBody.StreamOptions = &schemas.ChatStreamOptions{
					IncludeUsage: schemas.Ptr(true),
				}
				if postRequestConverter != nil {
					reqBody = postRequestConverter(reqBody)
				}
			}
			return reqBody, nil
		},
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	// Updating request
	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	// Set headers
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
		if customErrorConverter != nil {
			return nil, providerUtils.EnrichError(ctx, customErrorConverter(resp, schemas.ChatCompletionStreamRequest, providerName, request.Model), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.ChatCompletionStreamRequest, providerName, request.Model), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Determine request type for cleanup
	streamRequestType := schemas.ChatCompletionStreamRequest
	if isResponsesToChatCompletionsFallback {
		streamRequestType = schemas.ResponsesStreamRequest
	}

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, streamRequestType, logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, streamRequestType, logger)
			}
			// Release the responses stream state if it was acquired (for ResponsesToChatCompletions fallback)
			schemas.ReleaseChatToResponsesStreamState(responsesStreamState)
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		scanner := bufio.NewScanner(resp.BodyStream())
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		chunkIndex := -1
		usage := &schemas.BifrostLLMUsage{}

		startTime := time.Now()
		lastChunkTime := startTime

		var finishReason *string
		var messageID string

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

			// Check for end of stream
			if line == "data: [DONE]" {
				break
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
			var bifrostErr schemas.BifrostError
			if err := sonic.Unmarshal([]byte(jsonData), &bifrostErr); err == nil {
				if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						Provider:       providerName,
						ModelRequested: request.Model,
						RequestType:    streamRequestType,
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, &bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse), responseChan, logger)
					return
				}
			}

			// Parse into bifrost response
			var response schemas.BifrostChatResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn("Failed to parse stream response: %v", err)
				continue
			}

			if isResponsesToChatCompletionsFallback {
				spreadResponses := response.ToBifrostResponsesStreamResponse(responsesStreamState)
				for _, response := range spreadResponses {
					if response.Type == schemas.ResponsesStreamResponseTypeError {
						bifrostErr := &schemas.BifrostError{
							Type:           schemas.Ptr(string(schemas.ResponsesStreamResponseTypeError)),
							IsBifrostError: false,
							Error:          &schemas.ErrorField{},
							ExtraFields: schemas.BifrostErrorExtraFields{
								RequestType:    streamRequestType,
								Provider:       providerName,
								ModelRequested: request.Model,
							},
						}

						if response.Message != nil {
							bifrostErr.Error.Message = *response.Message
						}
						if response.Param != nil {
							bifrostErr.Error.Param = *response.Param
						}
						if response.Code != nil {
							bifrostErr.Error.Code = response.Code
						}

						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse), responseChan, logger)
						return
					}

					response.ExtraFields.RequestType = streamRequestType
					response.ExtraFields.Provider = providerName
					response.ExtraFields.ModelRequested = request.Model
					response.ExtraFields.ChunkIndex = response.SequenceNumber

					if sendBackRawResponse {
						response.ExtraFields.RawResponse = jsonData
					}

					if response.Type == schemas.ResponsesStreamResponseTypeCompleted {
						// Set raw request if enabled
						if sendBackRawRequest {
							providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
						}
						response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan)
						return
					}

					response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
					lastChunkTime = time.Now()

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan)
				}
			} else {
				if postResponseConverter != nil {
					if converted := postResponseConverter(&response); converted != nil {
						response = *converted
					} else {
						logger.Warn("postResponseConverter returned nil; leaving chunk unmodified")
					}
				}

				// Handle usage-only chunks (when stream_options include_usage is true)
				if response.Usage != nil {
					// Collect usage information and send at the end of the stream
					// Here in some cases usage comes before final message
					// So we need to check if the response.Usage is nil and then if usage != nil
					// then add up all tokens
					if response.Usage.PromptTokens > usage.PromptTokens {
						usage.PromptTokens = response.Usage.PromptTokens
					}
					if response.Usage.CompletionTokens > usage.CompletionTokens {
						usage.CompletionTokens = response.Usage.CompletionTokens
					}
					if response.Usage.TotalTokens > usage.TotalTokens {
						usage.TotalTokens = response.Usage.TotalTokens
					}
					calculatedTotal := usage.PromptTokens + usage.CompletionTokens
					if calculatedTotal > usage.TotalTokens {
						usage.TotalTokens = calculatedTotal
					}
					if response.Usage.PromptTokensDetails != nil {
						usage.PromptTokensDetails = response.Usage.PromptTokensDetails
					}
					if response.Usage.CompletionTokensDetails != nil {
						usage.CompletionTokensDetails = response.Usage.CompletionTokensDetails
					}
					if response.Usage.Cost != nil {
						usage.Cost = response.Usage.Cost
					}
					response.Usage = nil
				}

				// Skip empty responses or responses without choices
				if len(response.Choices) == 0 {
					continue
				}

				// Handle finish reason, usually in the final chunk
				choice := response.Choices[0]
				if choice.FinishReason != nil && *choice.FinishReason != "" {
					// Collect finish reason and send at the end of the stream
					finishReason = choice.FinishReason
					response.Choices[0].FinishReason = nil
				}

				if response.ID != "" && messageID == "" {
					messageID = response.ID
				}

				// Handle regular content chunks, including reasoning
				if choice.ChatStreamResponseChoice != nil &&
					choice.ChatStreamResponseChoice.Delta != nil &&
					(choice.ChatStreamResponseChoice.Delta.Content != nil ||
						choice.ChatStreamResponseChoice.Delta.Reasoning != nil ||
						len(choice.ChatStreamResponseChoice.Delta.ReasoningDetails) > 0 ||
						choice.ChatStreamResponseChoice.Delta.Audio != nil ||
						len(choice.ChatStreamResponseChoice.Delta.ToolCalls) > 0) {
					chunkIndex++

					response.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
					response.ExtraFields.Provider = providerName
					response.ExtraFields.ModelRequested = request.Model
					response.ExtraFields.ChunkIndex = chunkIndex
					response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
					lastChunkTime = time.Now()

					if sendBackRawResponse {
						response.ExtraFields.RawResponse = jsonData
					}

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, &response, nil, nil, nil, nil), responseChan)
				}

				// For providers that don't send [DONE] marker break on finish_reason
				if !providerUtils.ProviderSendsDoneMarker(providerName) && finishReason != nil {
					break
				}
			}
		}

		// Handle scanner errors first
		if err := scanner.Err(); err != nil {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			logger.Warn("Error reading stream: %v", err)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, streamRequestType, providerName, request.Model, logger)
			return
		}

		if !isResponsesToChatCompletionsFallback {
			response := providerUtils.CreateBifrostChatCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, streamRequestType, providerName, request.Model)
			if postResponseConverter != nil {
				response = postResponseConverter(response)
			}
			// Set raw request if enabled
			if sendBackRawRequest {
				providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
			}
			response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan)
		}
	}()

	return responseChan, nil
}

// Responses performs a responses request to the OpenAI API.
func (provider *OpenAIProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	// Check if chat completion is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	return HandleOpenAIResponsesRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/responses", schemas.ResponsesRequest),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		provider.logger,
	)
}

// HandleOpenAIResponsesRequest handles a responses request to OpenAI's API.
func HandleOpenAIResponsesRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostResponsesRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	customErrorConverter ErrorConverter,
	logger schemas.Logger,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Use centralized converter
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToOpenAIResponsesRequest(request), nil },
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		if customErrorConverter != nil {
			return nil, providerUtils.EnrichError(ctx, customErrorConverter(resp, schemas.ResponsesRequest, providerName, request.Model), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.ResponsesRequest, providerName, request.Model), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	response := &schemas.BifrostResponsesResponse{}

	// Use enhanced response handler with pre-allocated response
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse)
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ResponsesRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ResponsesStream performs a streaming responses request to the OpenAI API.
func (provider *OpenAIProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	// Check if chat completion stream is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}
	var authHeader map[string]string
	if key.Value.GetValue() != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
	}
	// Use shared streaming logic
	return HandleOpenAIResponsesStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/responses", schemas.ResponsesStreamRequest),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// HandleOpenAIResponsesStreaming handles streaming for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE format.
func HandleOpenAIResponsesStreaming(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostResponsesRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	customErrorConverter ErrorConverter,
	postRequestConverter func(*OpenAIResponsesRequest) *OpenAIResponsesRequest,
	postResponseConverter func(*schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	// Prepare SGL headers (SGL typically doesn't require authorization, but we include it if provided)
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if authHeader != nil {
		// Copy auth header to headers
		maps.Copy(headers, authHeader)
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := ToOpenAIResponsesRequest(request)
			if reqBody != nil {
				reqBody.Stream = schemas.Ptr(true)
				if postRequestConverter != nil {
					reqBody = postRequestConverter(reqBody)
				}
			}
			return reqBody, nil
		},
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	// Set headers
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
		if customErrorConverter != nil {
			return nil, providerUtils.EnrichError(ctx, customErrorConverter(resp, schemas.ResponsesStreamRequest, providerName, request.Model), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.ResponsesStreamRequest, providerName, request.Model), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ResponsesStreamRequest, logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ResponsesStreamRequest, logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		scanner := bufio.NewScanner(resp.BodyStream())
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			line := scanner.Text()

			// Skip empty lines, comments, and event lines
			if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
				continue
			}

			// Check for end of stream
			if line == "data: [DONE]" {
				break
			}

			var jsonData string

			// Parse SSE data
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				jsonData = after
			} else if !strings.HasPrefix(line, "event:") {
				// Handle raw JSON errors (without "data: " prefix) but skip event lines
				jsonData = line
			} else {
				// This is an event line, skip it
				continue
			}

			// Skip empty data
			if strings.TrimSpace(jsonData) == "" {
				continue
			}

			// Parse into bifrost response
			var response schemas.BifrostResponsesStreamResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn("Failed to parse stream response: %v", err)
				continue
			}

			if postResponseConverter != nil {
				if converted := postResponseConverter(&response); converted != nil {
					response = *converted
				} else {
					logger.Warn("postResponseConverter returned nil; leaving chunk unmodified")
				}
			}

			if sendBackRawResponse {
				response.ExtraFields.RawResponse = jsonData
			}

			if response.Type == schemas.ResponsesStreamResponseTypeError {
				bifrostErr := &schemas.BifrostError{
					Type:           schemas.Ptr(string(schemas.ResponsesStreamResponseTypeError)),
					IsBifrostError: false,
					Error:          &schemas.ErrorField{},
					ExtraFields: schemas.BifrostErrorExtraFields{
						RequestType:    schemas.ResponsesStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
					},
				}

				if response.Message != nil {
					bifrostErr.Error.Message = *response.Message
				}
				if response.Param != nil {
					bifrostErr.Error.Param = *response.Param
				}
				if response.Code != nil {
					bifrostErr.Error.Code = response.Code
				}

				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse), responseChan, logger)
				return
			}

			response.ExtraFields.RequestType = schemas.ResponsesStreamRequest
			response.ExtraFields.Provider = providerName
			response.ExtraFields.ModelRequested = request.Model
			response.ExtraFields.ChunkIndex = response.SequenceNumber

			if response.Type == schemas.ResponsesStreamResponseTypeCompleted || response.Type == schemas.ResponsesStreamResponseTypeIncomplete {
				// Set raw request if enabled
				if sendBackRawRequest {
					providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
				}
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, &response, nil, nil, nil), responseChan)
				return
			}

			response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
			lastChunkTime = time.Now()

			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, &response, nil, nil, nil), responseChan)
		}
		// Handle scanner errors first
		if err := scanner.Err(); err != nil {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			logger.Warn("Error reading stream: %v", err)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, request.Model, logger)
		}
	}()

	return responseChan, nil
}

// Embedding generates embeddings for the given input text(s).
// The input can be either a single string or a slice of strings for batch embedding.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *OpenAIProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	// Check if embedding is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	// Use the shared embedding request handler
	return HandleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/embeddings", schemas.EmbeddingRequest),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// HandleOpenAIEmbeddingRequest handles embedding requests for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same embedding request format.
func HandleOpenAIEmbeddingRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostEmbeddingRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	logger schemas.Logger,
) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Use centralized converter
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToOpenAIEmbeddingRequest(request), nil },
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.EmbeddingRequest, providerName, request.Model), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	response := &schemas.BifrostEmbeddingResponse{}

	// Use enhanced response handler with pre-allocated response
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse)
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.EmbeddingRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// Speech handles non-streaming speech synthesis requests.
// It formats the request body, makes the API call, and returns the response.
// Returns the response and any error that occurred.
func (provider *OpenAIProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
		return nil, err
	}

	return HandleOpenAISpeechRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/audio/speech", schemas.SpeechRequest),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// HandleOpenAISpeechRequest handles speech requests for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same speech request format.
func HandleOpenAISpeechRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostSpeechRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	logger schemas.Logger,
) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToOpenAISpeechRequest(request), nil },
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.SpeechRequest, providerName, request.Model), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Get the binary audio data from the response body
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Create final response with the audio data
	// Note: For speech synthesis, we return the binary audio data in the raw response
	// The audio data is typically in MP3, WAV, or other audio formats as specified by response_format
	bifrostResponse := &schemas.BifrostSpeechResponse{
		Audio: body,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.SpeechRequest,
			Provider:       providerName,
			ModelRequested: request.Model,
			Latency:        latency.Milliseconds(),
		},
	}

	if sendBackRawRequest {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	return bifrostResponse, nil
}

// SpeechStream handles streaming for speech synthesis.
// It formats the request body, creates HTTP request, and uses shared streaming logic.
// Returns a channel for streaming responses and any error that occurred.
func (provider *OpenAIProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.SpeechStreamRequest); err != nil {
		return nil, err
	}

	for _, model := range providerUtils.UnsupportedSpeechStreamModels {
		if model == request.Model {
			return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("model %s is not supported for streaming speech synthesis", model), nil, provider.GetProviderKey())
		}
	}

	var authHeader map[string]string
	if key.Value.GetValue() != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
	}

	return HandleOpenAISpeechStreamRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/audio/speech", schemas.SpeechStreamRequest),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		provider.logger,
	)
}

// HandleOpenAISpeechStreamRequest handles speech stream requests for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same speech stream request format.
func HandleOpenAISpeechStreamRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostSpeechRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	postRequestConverter func(*OpenAISpeechRequest) *OpenAISpeechRequest,
	postResponseConverter func(*schemas.BifrostSpeechStreamResponse) *schemas.BifrostSpeechStreamResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	// Prepare OpenAI headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if authHeader != nil {
		maps.Copy(headers, authHeader)
	}

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	// Set any extra headers from network config
	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Use centralized converter
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := ToOpenAISpeechRequest(request)
			if reqBody != nil {
				reqBody.StreamFormat = schemas.Ptr("sse")
				if postRequestConverter != nil {
					reqBody = postRequestConverter(reqBody)
				}
			}
			return reqBody, nil
		},
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
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
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.SpeechStreamRequest, providerName, request.Model), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.SpeechStreamRequest, logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.SpeechStreamRequest, logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		scanner := bufio.NewScanner(resp.BodyStream())
		chunkIndex := -1

		startTime := time.Now()
		lastChunkTime := startTime

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

			// Check for end of stream
			if line == "data: [DONE]" {
				break
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

			// First, check if this is an error response
			var bifrostErr schemas.BifrostError
			if err := sonic.Unmarshal([]byte(jsonData), &bifrostErr); err == nil {
				if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						Provider:       providerName,
						ModelRequested: request.Model,
						RequestType:    schemas.SpeechStreamRequest,
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, &bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse), responseChan, logger)
					return
				}
			}

			// Parse into bifrost response
			var response schemas.BifrostSpeechStreamResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn("Failed to parse stream response: %v", err)
				continue
			}

			if postResponseConverter != nil {
				if converted := postResponseConverter(&response); converted != nil {
					response = *converted
				} else {
					logger.Warn("postResponseConverter returned nil; leaving chunk unmodified")
				}
			}

			chunkIndex++

			response.ExtraFields = schemas.BifrostResponseExtraFields{
				RequestType:    schemas.SpeechStreamRequest,
				Provider:       providerName,
				ModelRequested: request.Model,
				ChunkIndex:     chunkIndex,
				Latency:        time.Since(lastChunkTime).Milliseconds(),
			}
			lastChunkTime = time.Now()

			if sendBackRawResponse {
				response.ExtraFields.RawResponse = jsonData
			}

			if response.Usage != nil {
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				if sendBackRawRequest {
					providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, &response, nil, nil), responseChan)
				return
			}

			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, &response, nil, nil), responseChan)
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			logger.Warn("Error reading stream: %v", err)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.SpeechStreamRequest, providerName, request.Model, logger)
		}
	}()

	return responseChan, nil
}

// Transcription handles non-streaming transcription requests.
// It creates a multipart form, adds fields, makes the API call, and returns the response.
// Returns the response and any error that occurred.
func (provider *OpenAIProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.TranscriptionRequest); err != nil {
		return nil, err
	}

	return HandleOpenAITranscriptionRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/audio/transcriptions", schemas.TranscriptionRequest),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

func HandleOpenAITranscriptionRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostTranscriptionRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawResponse bool,
	logger schemas.Logger,
) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Use centralized converter
	reqBody := ToOpenAITranscriptionRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("transcription input is not provided", nil, providerName)
	}

	// Create multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := parseTranscriptionFormDataBodyFromRequest(writer, reqBody, providerName); err != nil {
		return nil, err
	}

	req.Header.SetContentType(writer.FormDataContentType()) // This sets multipart/form-data with boundary
	req.SetBody(body.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, ParseOpenAIError(resp, schemas.TranscriptionRequest, providerName, request.Model)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Check for empty response
	trimmed := strings.TrimSpace(string(responseBody))
	if len(trimmed) == 0 {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseEmpty,
			},
		}
	}

	copiedResponseBody := append([]byte(nil), responseBody...)

	// Parse OpenAI's transcription response directly into BifrostTranscribe
	response := &schemas.BifrostTranscriptionResponse{}

	if err := sonic.Unmarshal(copiedResponseBody, response); err != nil {
		// Check if it's an HTML response
		if providerUtils.IsHTMLResponse(resp, copiedResponseBody) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderResponseHTML,
					Error:   errors.New(string(copiedResponseBody)),
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// TODO: add HandleProviderResponse here

	// Parse raw response for RawResponse field
	var rawResponse interface{}
	if sendBackRawResponse {
		if err := sonic.Unmarshal(copiedResponseBody, &rawResponse); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRawResponseUnmarshal, err, providerName)
		}
	}

	response.ExtraFields = schemas.BifrostResponseExtraFields{
		RequestType:    schemas.TranscriptionRequest,
		Provider:       providerName,
		ModelRequested: request.Model,
		Latency:        latency.Milliseconds(),
	}

	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// TranscriptionStream performs a streaming transcription request to the OpenAI API.
func (provider *OpenAIProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.TranscriptionStreamRequest); err != nil {
		return nil, err
	}

	var authHeader map[string]string
	if key.Value.GetValue() != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
	}

	return HandleOpenAITranscriptionStreamRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/audio/transcriptions", schemas.TranscriptionStreamRequest),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		provider.logger,
	)
}

// HandleOpenAITranscriptionStreamRequest handles transcription stream requests for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same transcription stream request format.
func HandleOpenAITranscriptionStreamRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostTranscriptionRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	postRequestConverter func(*OpenAITranscriptionRequest) *OpenAITranscriptionRequest,
	postResponseConverter func(*schemas.BifrostTranscriptionStreamResponse) *schemas.BifrostTranscriptionStreamResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	// Use centralized converter
	reqBody := ToOpenAITranscriptionRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("transcription input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)
	if postRequestConverter != nil {
		reqBody = postRequestConverter(reqBody)
	}

	// Create multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if bifrostErr := parseTranscriptionFormDataBodyFromRequest(writer, reqBody, providerName); bifrostErr != nil {
		return nil, bifrostErr
	}

	// Prepare OpenAI headers
	headers := map[string]string{
		"Content-Type":  writer.FormDataContentType(),
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if authHeader != nil {
		maps.Copy(headers, authHeader)
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	req.SetBody(body.Bytes())

	// Make the request
	err := client.Do(req, resp)
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
		return nil, ParseOpenAIError(resp, schemas.TranscriptionStreamRequest, providerName, request.Model)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.TranscriptionStreamRequest, logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.TranscriptionStreamRequest, logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		scanner := bufio.NewScanner(resp.BodyStream())
		chunkIndex := -1

		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}

			line := scanner.Text()

			// Skip empty lines and comments
			if line == "" {
				continue
			}

			// Check for end of stream
			if line == "data: [DONE]" {
				break
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

			// First, check if this is an error response
			var bifrostErr schemas.BifrostError
			if err := sonic.Unmarshal([]byte(jsonData), &bifrostErr); err == nil {
				if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						Provider:       providerName,
						ModelRequested: request.Model,
						RequestType:    schemas.TranscriptionStreamRequest,
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, logger)
					return
				}
			}

			var response schemas.BifrostTranscriptionStreamResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn("Failed to parse stream response: %v", err)
				continue
			}

			if postResponseConverter != nil {
				if converted := postResponseConverter(&response); converted != nil {
					response = *converted
				} else {
					logger.Warn("postResponseConverter returned nil; leaving chunk unmodified")
				}
			}

			chunkIndex++

			response.ExtraFields = schemas.BifrostResponseExtraFields{
				RequestType:    schemas.TranscriptionStreamRequest,
				Provider:       providerName,
				ModelRequested: request.Model,
				ChunkIndex:     chunkIndex,
				Latency:        time.Since(lastChunkTime).Milliseconds(),
			}
			lastChunkTime = time.Now()

			if sendBackRawResponse {
				response.ExtraFields.RawResponse = jsonData
			}

			if response.Usage != nil {
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, &response, nil), responseChan)
				return
			}

			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, &response, nil), responseChan)
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			logger.Warn("Error reading stream: %v", err)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.TranscriptionStreamRequest, providerName, request.Model, logger)
		}
	}()

	return responseChan, nil
}

// ImageGeneration performs an Image Generation request to OpenAI's API.
// It formats the request, sends it to OpenAI, and processes the response.
// Returns a BifrostResponse containing the bifrost response or an error if the request fails.
func (provider *OpenAIProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key,
	req *schemas.BifrostImageGenerationRequest,
) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ImageGenerationRequest); err != nil {
		return nil, err
	}

	return HandleOpenAIImageGenerationRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/images/generations", schemas.ImageGenerationRequest),
		req,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// HandleOpenAIImageGenerationRequest handles image generation requests for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same image generation request format.
func HandleOpenAIImageGenerationRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostImageGenerationRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	logger schemas.Logger,
) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if value := key.Value.GetValue(); value != "" {
		req.Header.Set("Authorization", "Bearer "+value)
	}

	// Use centralized converter
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToOpenAIImageGenerationRequest(request), nil },
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.ImageGenerationRequest, providerName, request.Model), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	response := &schemas.BifrostImageGenerationResponse{}

	// Use enhanced response handler with pre-allocated response
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ImageGenerationRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ImageGenerationStream handles streaming for image generation.
// It formats the request body, creates HTTP request, and uses shared streaming logic.
// Returns a channel for streaming responses and any error that occurred.
func (provider *OpenAIProvider) ImageGenerationStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	key schemas.Key,
	request *schemas.BifrostImageGenerationRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil, provider.GetProviderKey())
	}

	// Check if image generation stream is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ImageGenerationStreamRequest); err != nil {
		return nil, err
	}

	var authHeader map[string]string
	if value := key.Value.GetValue(); value != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + value}
	}
	// Use shared streaming logic
	return HandleOpenAIImageGenerationStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/images/generations", schemas.ImageGenerationStreamRequest),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		provider.logger,
	)
}

func HandleOpenAIImageGenerationStreaming(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostImageGenerationRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	customRequestConverter func(*schemas.BifrostImageGenerationRequest) (any, error),
	postRequestConverter func(*OpenAIImageGenerationRequest) *OpenAIImageGenerationRequest,
	postResponseConverter func(*schemas.BifrostImageGenerationStreamResponse) *schemas.BifrostImageGenerationStreamResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	// Set headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if authHeader != nil {
		// Copy auth header to headers
		maps.Copy(headers, authHeader)
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			if customRequestConverter != nil {
				return customRequestConverter(request)
			}
			reqBody := ToOpenAIImageGenerationRequest(request)
			if reqBody != nil {
				reqBody.Stream = schemas.Ptr(true)
				if postRequestConverter != nil {
					reqBody = postRequestConverter(reqBody)
				}
			}
			return reqBody, nil
		},
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	// Updating request
	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	req.SetBody(jsonBody)

	// Capture start time before making the HTTP request for latency calculation
	startTime := time.Now()

	// Make the request
	err := client.Do(req, resp)
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
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.ImageGenerationStreamRequest, providerName, request.Model), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ImageGenerationStreamRequest, logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ImageGenerationStreamRequest, logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		scanner := bufio.NewScanner(resp.BodyStream())
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		lastChunkTime := startTime
		var collectedUsage *schemas.ImageUsage
		// Track chunk indices per image - similar to how speech/transcription track chunkIndex
		imageChunkIndices := make(map[int]int) // image index -> chunk index
		// Track images that have started (via partial chunks) but not yet completed
		// This allows us to correctly match completed events to images even if chunks are interleaved
		incompleteImages := make(map[int]bool)
		maxImageIndex := -1 // Track maximum image index for NImages calculation

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

			// Skip event type lines (event type is in the data payload)
			if strings.HasPrefix(line, "event:") {
				continue
			}

			// Parse data line
			if !strings.HasPrefix(line, "data:") {
				continue
			}

			jsonData := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if jsonData == "" {
				continue
			}

			var bifrostErr schemas.BifrostError
			if err := sonic.Unmarshal([]byte(jsonData), &bifrostErr); err == nil {
				if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						Provider:       providerName,
						ModelRequested: request.Model,
						RequestType:    schemas.ImageGenerationStreamRequest,
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, logger)
					return
				}
			}

			// Parse minimally to extract usage and check for errors
			var response OpenAIImageStreamResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn("Failed to parse stream response: %v", err)
				continue
			}

			// Check if response type indicates an error
			if response.Type == "error" {
				bifrostErr := &schemas.BifrostError{
					IsBifrostError: false,
					Error:          &schemas.ErrorField{},
					ExtraFields: schemas.BifrostErrorExtraFields{
						Provider:       providerName,
						ModelRequested: request.Model,
						RequestType:    schemas.ImageGenerationStreamRequest,
					},
				}
				// Guard access to response.Error fields
				if response.Error != nil {
					bifrostErr.Error.Message = response.Error.Message
					if response.Error.Code != nil {
						bifrostErr.Error.Code = response.Error.Code
					}
					if response.Error.Param != nil {
						bifrostErr.Error.Param = response.Error.Param
					}
					if response.Error.Type != nil {
						bifrostErr.Error.Type = response.Error.Type
					}
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				return
			}

			// Collect usage from completed event
			if response.Usage != nil {
				collectedUsage = &schemas.ImageUsage{
					InputTokens:  response.Usage.InputTokens,
					OutputTokens: response.Usage.OutputTokens,
					TotalTokens:  response.Usage.TotalTokens,
				}
			}

			// Determine if this is the final chunk
			isCompleted := response.Type == schemas.ImageGenerationEventTypeCompleted

			// Determine image index with robust tracking for interleaved chunks
			// Both partial and completed chunks should use PartialImageIndex when available
			var imageIndex int
			if response.PartialImageIndex != nil {
				// Use explicit index from response
				imageIndex = *response.PartialImageIndex
				if isCompleted {
					// Mark this image as completed
					delete(incompleteImages, imageIndex)
				} else {
					// Mark this image as started (incomplete)
					incompleteImages[imageIndex] = true
				}
			} else {
				// Fallback: PartialImageIndex is nil, use tracked state
				if isCompleted {
					// For completed chunks, match to the oldest incomplete image
					// This handles interleaved chunks correctly
					if len(incompleteImages) == 0 {
						// Fallback: if no incomplete images tracked, this shouldn't happen in normal flow
						// but we'll default to 0 to prevent panics
						imageIndex = 0
						logger.Warn("Received completed event but no incomplete images tracked, defaulting to index 0")
					} else {
						// Find the minimum (oldest) incomplete image index
						// Completed events should match the oldest image that was started
						minIndex := -1
						for idx := range incompleteImages {
							if minIndex == -1 || idx < minIndex {
								minIndex = idx
							}
						}
						imageIndex = minIndex
						// Mark this image as completed
						delete(incompleteImages, imageIndex)
						logger.Warn("Completed event missing PartialImageIndex, using oldest incomplete image index %d", imageIndex)
					}
				} else {
					// For partial chunks without PartialImageIndex, allocate a new unique index
					// Use maxImageIndex + 1 to ensure uniqueness
					imageIndex = maxImageIndex + 1
					// Mark this image as started (incomplete)
					incompleteImages[imageIndex] = true
				}
			}

			// Update maximum image index for NImages calculation
			if imageIndex > maxImageIndex {
				maxImageIndex = imageIndex
			}

			// Increment chunk index for this image
			if _, exists := imageChunkIndices[imageIndex]; !exists {
				imageChunkIndices[imageIndex] = 0
			} else {
				imageChunkIndices[imageIndex]++
			}
			chunkIndex := imageChunkIndices[imageIndex]
			// Build chunk with all OpenAI fields
			chunk := &schemas.BifrostImageGenerationStreamResponse{
				Type:         response.Type,
				Index:        imageIndex, // Which image (0-N)
				ChunkIndex:   chunkIndex, // Chunk order within this image (top-level)
				CreatedAt:    response.CreatedAt,
				Size:         response.Size,
				Quality:      response.Quality,
				Background:   response.Background,
				OutputFormat: response.OutputFormat,
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:    schemas.ImageGenerationStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
					ChunkIndex:     chunkIndex, // Chunk order within this image
					Latency:        time.Since(lastChunkTime).Milliseconds(),
				},
			}

			if postResponseConverter != nil {
				if converted := postResponseConverter(chunk); converted != nil {
					chunk = converted
				} else {
					logger.Warn("postResponseConverter returned nil; leaving chunk unmodified")
				}
			}

			// Only set PartialImageIndex for partial images, not for completed events
			if !isCompleted {
				chunk.PartialImageIndex = response.PartialImageIndex
			}
			// Set SequenceNumber if present
			if response.SequenceNumber != nil {
				chunk.SequenceNumber = *response.SequenceNumber
			}
			lastChunkTime = time.Now()

			// Copy b64_json if present
			if response.B64JSON != nil {
				chunk.B64JSON = *response.B64JSON
			}

			// Set raw response on every chunk if enabled
			if sendBackRawResponse {
				chunk.ExtraFields.RawResponse = jsonData
			}

			if isCompleted {
				if collectedUsage != nil {
					// Set NImages based on maximum image index seen (maxImageIndex + 1 since indices are 0-based)
					if maxImageIndex >= 0 {
						nImages := maxImageIndex + 1
						collectedUsage.OutputTokensDetails = &schemas.ImageTokenDetails{
							NImages: nImages,
						}
					}
					chunk.Usage = collectedUsage
				}
				// For completed chunk, use total latency from start
				chunk.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				// Set raw request only on final chunk if enabled
				if sendBackRawRequest {
					providerUtils.ParseAndSetRawRequest(&chunk.ExtraFields, jsonBody)
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			}

			providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
				providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, chunk),
				responseChan)

			if isCompleted {
				return
			}
		}

		if err := scanner.Err(); err != nil {
			logger.Warn("Error reading stream: %v", err)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ImageGenerationStreamRequest, providerName, request.Model, logger)
		}
	}()

	return responseChan, nil
}

// CountTokens performs a count tokens request to the OpenAI API.
func (provider *OpenAIProvider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.CountTokensRequest); err != nil {
		return nil, err
	}

	return HandleOpenAICountTokensRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/responses/input_tokens", schemas.CountTokensRequest),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		provider.logger,
	)
}

// HandleOpenAICountTokensRequest handles a count tokens request to OpenAI's API.
func HandleOpenAICountTokensRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostResponsesRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	logger schemas.Logger,
) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToOpenAIResponsesRequest(request), nil },
		providerName,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.CountTokensRequest, providerName, request.Model), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	response := &schemas.BifrostCountTokensResponse{}

	// Use enhanced response handler with pre-allocated response
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.Model = request.Model
	response.ExtraFields.Provider = providerName
	response.ExtraFields.RequestType = schemas.CountTokensRequest
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.Latency = latency.Milliseconds()

	if providerUtils.ShouldSendBackRawRequest(ctx, sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ImageEdit performs image editing via the OpenAI Images API.
func (provider *OpenAIProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ImageEditRequest); err != nil {
		return nil, err
	}

	return HandleOpenAIImageEditRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/images/edits", schemas.ImageEditRequest),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		false,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		provider.logger,
	)
}

func HandleOpenAIImageEditRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostImageEditRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	logger schemas.Logger,
) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {

	openaiReq := ToOpenAIImageEditRequest(request)
	if openaiReq == nil {
		return nil, providerUtils.NewBifrostOperationError("failed to convert request to OpenAI format", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	req.Header.Set("Content-Type", "multipart/form-data")

	// Create multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := parseImageEditFormDataBodyFromRequest(writer, openaiReq, providerName); err != nil {
		return nil, err
	}

	req.Header.SetContentType(writer.FormDataContentType())
	bodyData := body.Bytes()
	req.SetBody(bodyData)

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, bodyData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.ImageEditRequest, providerName, request.Model), bodyData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	bodyBytes, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	response := &schemas.BifrostImageGenerationResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(bodyBytes, response, bodyData, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ImageEditRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// ImageEditStream streams image edits via the OpenAI Images API.
func (provider *OpenAIProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	// Check if image generation stream is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ImageEditStreamRequest); err != nil {
		return nil, err
	}

	var authHeader map[string]string
	if value := key.Value.GetValue(); value != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + value}
	}

	return HandleOpenAIImageEditStreamRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/images/edits", schemas.ImageEditStreamRequest),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		false,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		provider.logger,
	)
}

func HandleOpenAIImageEditStreamRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostImageEditRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	customRequestConverter func(*schemas.BifrostImageEditRequest) (any, error),
	postRequestConverter func(*OpenAIImageEditRequest) *OpenAIImageEditRequest,
	postResponseConverter func(*schemas.BifrostImageGenerationStreamResponse) *schemas.BifrostImageGenerationStreamResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {

	reqBody := ToOpenAIImageEditRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("image edit input is not provided", nil, providerName)
	}

	reqBody.Stream = schemas.Ptr(true)
	if postRequestConverter != nil {
		reqBody = postRequestConverter(reqBody)
	}
	// Create multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if bifrostErr := parseImageEditFormDataBodyFromRequest(writer, reqBody, providerName); bifrostErr != nil {
		return nil, bifrostErr
	}

	// Prepare OpenAI headers
	headers := map[string]string{
		"Content-Type":  writer.FormDataContentType(),
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if authHeader != nil {
		maps.Copy(headers, authHeader)
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	req.SetBody(body.Bytes())

	// Make the request
	err := client.Do(req, resp)
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
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.ImageEditStreamRequest, providerName, request.Model), body.Bytes(), nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ImageEditStreamRequest, logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ImageEditStreamRequest, logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		scanner := bufio.NewScanner(resp.BodyStream())
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		lastChunkTime := time.Now()
		var collectedUsage *schemas.ImageUsage
		// Track chunk indices per image - similar to how speech/transcription track chunkIndex
		imageChunkIndices := make(map[int]int) // image index -> chunk index
		// Track images that have started (via partial chunks) but not yet completed
		// This allows us to correctly match completed events to images even if chunks are interleaved
		incompleteImages := make(map[int]bool)
		maxImageIndex := -1 // Track maximum image index for NImages calculation

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

			// Skip event type lines (event type is in the data payload)
			if strings.HasPrefix(line, "event:") {
				continue
			}

			// Parse data line
			if !strings.HasPrefix(line, "data:") {
				continue
			}

			jsonData := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if jsonData == "" {
				continue
			}

			var bifrostErr schemas.BifrostError
			if err := sonic.Unmarshal([]byte(jsonData), &bifrostErr); err == nil {
				if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						Provider:       providerName,
						ModelRequested: request.Model,
						RequestType:    schemas.ImageEditStreamRequest,
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, logger)
					return
				}
			}

			// Parse minimally to extract usage and check for errors
			var response OpenAIImageStreamResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn("Failed to parse stream response: %v", err)
				continue
			}

			// Check if response type indicates an error
			if response.Type == "error" {
				bifrostErr := &schemas.BifrostError{
					IsBifrostError: false,
					Error:          &schemas.ErrorField{},
					ExtraFields: schemas.BifrostErrorExtraFields{
						Provider:       providerName,
						ModelRequested: request.Model,
						RequestType:    schemas.ImageEditStreamRequest,
					},
				}
				// Guard access to response.Error fields
				if response.Error != nil {
					bifrostErr.Error.Message = response.Error.Message
					if response.Error.Code != nil {
						bifrostErr.Error.Code = response.Error.Code
					}
					if response.Error.Param != nil {
						bifrostErr.Error.Param = response.Error.Param
					}
					if response.Error.Type != nil {
						bifrostErr.Error.Type = response.Error.Type
					}
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				return
			}

			// Collect usage from completed event
			if response.Usage != nil {
				collectedUsage = &schemas.ImageUsage{
					InputTokens:  response.Usage.InputTokens,
					OutputTokens: response.Usage.OutputTokens,
					TotalTokens:  response.Usage.TotalTokens,
				}
			}

			// Determine if this is the final chunk
			isCompleted := response.Type == schemas.ImageGenerationEventTypeCompleted || response.Type == schemas.ImageEditEventTypeCompleted

			// Determine image index with robust tracking for interleaved chunks
			// Both partial and completed chunks should use PartialImageIndex when available
			var imageIndex int
			if response.PartialImageIndex != nil {
				// Use explicit index from response
				imageIndex = *response.PartialImageIndex
				if isCompleted {
					// Mark this image as completed
					delete(incompleteImages, imageIndex)
				} else {
					// Mark this image as started (incomplete)
					incompleteImages[imageIndex] = true
				}
			} else {
				// Fallback: PartialImageIndex is nil, use tracked state
				if isCompleted {
					// For completed chunks, match to the oldest incomplete image
					// This handles interleaved chunks correctly
					if len(incompleteImages) == 0 {
						// Fallback: if no incomplete images tracked, this shouldn't happen in normal flow
						// but we'll default to 0 to prevent panics
						imageIndex = 0
						logger.Warn("Received completed event but no incomplete images tracked, defaulting to index 0")
					} else {
						// Find the minimum (oldest) incomplete image index
						// Completed events should match the oldest image that was started
						minIndex := -1
						for idx := range incompleteImages {
							if minIndex == -1 || idx < minIndex {
								minIndex = idx
							}
						}
						imageIndex = minIndex
						// Mark this image as completed
						delete(incompleteImages, imageIndex)
						logger.Warn(fmt.Sprintf("Completed event missing PartialImageIndex, using oldest incomplete image index %d", imageIndex))
					}
				} else {
					// For partial chunks without PartialImageIndex, allocate a new unique index
					// Use maxImageIndex + 1 to ensure uniqueness
					imageIndex = maxImageIndex + 1
					// Mark this image as started (incomplete)
					incompleteImages[imageIndex] = true
				}
			}

			// Update maximum image index for NImages calculation
			if imageIndex > maxImageIndex {
				maxImageIndex = imageIndex
			}

			// Increment chunk index for this image
			if _, exists := imageChunkIndices[imageIndex]; !exists {
				imageChunkIndices[imageIndex] = 0
			} else {
				imageChunkIndices[imageIndex]++
			}
			chunkIndex := imageChunkIndices[imageIndex]
			// Build chunk with all OpenAI fields
			chunk := &schemas.BifrostImageGenerationStreamResponse{
				Type:         response.Type,
				Index:        imageIndex, // Which image (0-N)
				ChunkIndex:   chunkIndex, // Chunk order within this image (top-level)
				CreatedAt:    response.CreatedAt,
				Size:         response.Size,
				Quality:      response.Quality,
				Background:   response.Background,
				OutputFormat: response.OutputFormat,
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:    schemas.ImageEditStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
					ChunkIndex:     chunkIndex, // Chunk order within this image
					Latency:        time.Since(lastChunkTime).Milliseconds(),
				},
			}

			if postResponseConverter != nil {
				if converted := postResponseConverter(chunk); converted != nil {
					chunk = converted
				} else {
					logger.Warn("postResponseConverter returned nil; leaving chunk unmodified")
				}
			}

			// Only set PartialImageIndex for partial images, not for completed events
			if !isCompleted {
				chunk.PartialImageIndex = response.PartialImageIndex
			}
			// Set SequenceNumber if present
			if response.SequenceNumber != nil {
				chunk.SequenceNumber = *response.SequenceNumber
			}
			lastChunkTime = time.Now()

			// Copy b64_json if present
			if response.B64JSON != nil {
				chunk.B64JSON = *response.B64JSON
			}

			// Set raw response on every chunk if enabled
			if sendBackRawResponse {
				chunk.ExtraFields.RawResponse = jsonData
			}

			if isCompleted {
				if collectedUsage != nil {
					// Set NImages based on maximum image index seen (maxImageIndex + 1 since indices are 0-based)
					if maxImageIndex >= 0 {
						nImages := maxImageIndex + 1
						collectedUsage.OutputTokensDetails = &schemas.ImageTokenDetails{
							NImages: nImages,
						}
					}
					chunk.Usage = collectedUsage
				}
				// For completed chunk, use total latency from start
				chunk.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			}

			providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
				providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, chunk),
				responseChan)

			if isCompleted {
				return
			}
		}

		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ImageEditStreamRequest, providerName, request.Model, logger)
		}
	}()

	return responseChan, nil
}

// ImageVariation performs an image variation request to openai's images api.
func (provider *OpenAIProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ImageVariationRequest); err != nil {
		return nil, err
	}

	response, err := HandleOpenAIImageVariationRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/images/variations", schemas.ImageVariationRequest),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		false,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		provider.logger,
	)
	return response, err
}

// ImageVariation performs an image variation request
// HandleOpenAIImageVariationRequest handles image variation requests for OpenAI-compatible providers
func HandleOpenAIImageVariationRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostImageVariationRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	logger schemas.Logger,
) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	openaiReq := ToOpenAIImageVariationRequest(request)
	if openaiReq == nil {
		return nil, providerUtils.NewBifrostOperationError("failed to convert request to OpenAI format", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Create multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := parseImageVariationFormDataBodyFromRequest(writer, openaiReq, providerName); err != nil {
		return nil, err
	}

	req.Header.SetContentType(writer.FormDataContentType())
	bodyData := body.Bytes()
	req.SetBody(bodyData)

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, bodyData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.ImageVariationRequest, providerName, request.Model), bodyData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	bodyBytes, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	response := &schemas.BifrostImageGenerationResponse{}
	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(bodyBytes, response, bodyData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ImageVariationRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// FileUpload uploads a file to OpenAI.
func (provider *OpenAIProvider) FileUpload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.FileUploadRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("file content is required", nil, providerName)
	}

	if request.Purpose == "" {
		return nil, providerUtils.NewBifrostOperationError("purpose is required", nil, providerName)
	}

	// Create multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add purpose field
	if err := writer.WriteField("purpose", string(request.Purpose)); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write purpose field", err, providerName)
	}

	// Add file field
	filename := request.Filename
	if filename == "" {
		filename = "file.jsonl"
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
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	req.SetBody(buf.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, ParseOpenAIError(resp, schemas.FileUploadRequest, providerName, "")
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var openAIResp OpenAIFileResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openAIResp.ToBifrostFileUploadResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// FileList lists files using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *OpenAIProvider) FileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.FileListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)

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
	if request.Purpose != "" {
		values.Set("purpose", string(request.Purpose))
	}
	if request.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from serial helper instead of request.After
	if nativeCursor != "" {
		values.Set("after", nativeCursor)
	}
	if request.Order != nil && *request.Order != "" {
		values.Set("order", *request.Order)
	}
	if encoded := values.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, ParseOpenAIError(resp, schemas.FileListRequest, providerName, "")
	}

	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr, providerName)
	}

	var openAIResp OpenAIFileListResponse
	_, _, bifrostErr = providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert files to Bifrost format
	files := make([]schemas.FileObject, 0, len(openAIResp.Data))
	var lastFileID string
	for _, file := range openAIResp.Data {
		files = append(files, schemas.FileObject{
			ID:            file.ID,
			Object:        file.Object,
			Bytes:         file.Bytes,
			CreatedAt:     file.CreatedAt,
			Filename:      file.Filename,
			Purpose:       schemas.FilePurpose(file.Purpose),
			Status:        ToBifrostFileStatus(file.Status),
			StatusDetails: file.StatusDetails,
		})
		lastFileID = file.ID
	}

	// Build cursor for next request
	// OpenAI uses LastID as the cursor for pagination
	nextCursor, hasMore := helper.BuildNextCursor(openAIResp.HasMore, lastFileID)

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

// FileRetrieve retrieves file metadata from OpenAI by trying each key until found.
func (provider *OpenAIProvider) FileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.FileRetrieveRequest); err != nil {
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
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

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
			lastErr = ParseOpenAIError(resp, schemas.FileRetrieveRequest, providerName, "")
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

		var openAIResp OpenAIFileResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		return openAIResp.ToBifrostFileRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
	}

	return nil, lastErr
}

// FileDelete deletes a file from OpenAI by trying each key until successful.
func (provider *OpenAIProvider) FileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.FileDeleteRequest); err != nil {
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
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

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
			lastErr = ParseOpenAIError(resp, schemas.FileDeleteRequest, providerName, "")
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

		var openAIResp OpenAIFileDeleteResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := &schemas.BifrostFileDeleteResponse{
			ID:      openAIResp.ID,
			Object:  openAIResp.Object,
			Deleted: openAIResp.Deleted,
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

// FileContent downloads file content from OpenAI by trying each key until found.
func (provider *OpenAIProvider) FileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.FileContentRequest); err != nil {
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
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

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
			lastErr = ParseOpenAIError(resp, schemas.FileContentRequest, providerName, "")
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

// BatchCreate creates a new batch job.
func (provider *OpenAIProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.BatchCreateRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	inputFileID := request.InputFileID

	// If no file_id provided but inline requests are available, upload them first
	if inputFileID == "" && len(request.Requests) > 0 {
		// Convert inline requests to JSONL format
		jsonlData, err := ConvertRequestsToJSONL(request.Requests)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to convert requests to JSONL", err, providerName)
		}

		// Upload the file with purpose "batch"
		uploadResp, bifrostErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{
			Provider: schemas.OpenAI,
			File:     jsonlData,
			Filename: "batch_requests.jsonl",
			Purpose:  "batch",
		})
		if bifrostErr != nil {
			return nil, bifrostErr
		}

		inputFileID = uploadResp.ID
	}

	// Validate that we have a file ID (either provided or uploaded)
	if inputFileID == "" {
		return nil, providerUtils.NewBifrostOperationError("either input_file_id or requests array is required for OpenAI batch API", nil, providerName)
	}

	// Validate that we have an endpoint
	if request.Endpoint == "" {
		return nil, providerUtils.NewBifrostOperationError("endpoint is required for OpenAI batch API", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/batches", schemas.BatchCreateRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Build request body
	openAIReq := &OpenAIBatchRequest{
		InputFileID:      inputFileID,
		Endpoint:         string(request.Endpoint),
		CompletionWindow: request.CompletionWindow,
		Metadata:         request.Metadata,
	}

	// Set default completion window if not provided
	if openAIReq.CompletionWindow == "" {
		openAIReq.CompletionWindow = "24h"
	}

	jsonData, err := sonic.Marshal(openAIReq)
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
		return nil, providerUtils.EnrichError(ctx, ParseOpenAIError(resp, schemas.BatchCreateRequest, providerName, ""), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	var openAIResp OpenAIBatchResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse)
	}

	return openAIResp.ToBifrostBatchCreateResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// BatchList lists batch jobs using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *OpenAIProvider) BatchList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.BatchListRequest); err != nil {
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
	baseURL := provider.buildRequestURL(ctx, "/v1/batches", schemas.BatchListRequest)
	values := url.Values{}
	if request.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from serial helper instead of request.After
	if nativeCursor != "" {
		values.Set("after", nativeCursor)
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
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, ParseOpenAIError(resp, schemas.BatchListRequest, providerName, "")
	}

	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr, providerName)
	}

	var openAIResp OpenAIBatchListResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert batches to Bifrost format
	batches := make([]schemas.BifrostBatchRetrieveResponse, 0, len(openAIResp.Data))
	var lastBatchID string
	for _, batch := range openAIResp.Data {
		batches = append(batches, *batch.ToBifrostBatchRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse))
		lastBatchID = batch.ID
	}

	// Build cursor for next request
	// OpenAI uses LastID as the cursor for pagination
	nextCursor, hasMore := helper.BuildNextCursor(openAIResp.HasMore, lastBatchID)

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
func (provider *OpenAIProvider) BatchRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.BatchRetrieveRequest); err != nil {
		return nil, err
	}

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, request.Provider)
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
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/batches/" + request.BatchID)
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

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
			lastErr = ParseOpenAIError(resp, schemas.BatchRetrieveRequest, providerName, "")
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

		var openAIResp OpenAIBatchResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := openAIResp.ToBifrostBatchRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse)
		result.ExtraFields.RequestType = schemas.BatchRetrieveRequest
		return result, nil
	}

	return nil, lastErr
}

// BatchCancel cancels a batch job by trying each key until successful.
func (provider *OpenAIProvider) BatchCancel(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.BatchCancelRequest); err != nil {
		return nil, err
	}

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, schemas.OpenAI)
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
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/batches/" + request.BatchID + "/cancel")
		req.Header.SetMethod(http.MethodPost)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

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
			lastErr = ParseOpenAIError(resp, schemas.BatchCancelRequest, providerName, "")
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

		var openAIResp OpenAIBatchResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &openAIResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := &schemas.BifrostBatchCancelResponse{
			ID:           openAIResp.ID,
			Object:       openAIResp.Object,
			Status:       ToBifrostBatchStatus(openAIResp.Status),
			CancellingAt: openAIResp.CancellingAt,
			CancelledAt:  openAIResp.CancelledAt,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchCancelRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}

		if openAIResp.RequestCounts != nil {
			result.RequestCounts = schemas.BatchRequestCounts{
				Total:     openAIResp.RequestCounts.Total,
				Completed: openAIResp.RequestCounts.Completed,
				Failed:    openAIResp.RequestCounts.Failed,
			}
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

// BatchResults retrieves batch results by trying each key until successful.
// Note: For OpenAI, batch results are obtained by downloading the output_file_id.
// This method returns the file content parsed as batch results.
func (provider *OpenAIProvider) BatchResults(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.BatchResultsRequest); err != nil {
		return nil, err
	}

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil, schemas.OpenAI)
	}

	providerName := provider.GetProviderKey()

	// First, retrieve the batch to get the output_file_id (this already iterates over keys)
	batchResp, bifrostErr := provider.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		BatchID:  request.BatchID,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if batchResp.OutputFileID == nil || *batchResp.OutputFileID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch results not available: output_file_id is empty (batch may not be completed)", nil, providerName)
	}

	// Download the output file - try each key
	var lastErr *schemas.BifrostError
	for _, key := range keys {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files/" + *batchResp.OutputFileID + "/content")
		req.Header.SetMethod(http.MethodGet)

		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

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
			lastErr = ParseOpenAIError(resp, schemas.BatchResultsRequest, providerName, "")
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
			var resultItem schemas.BatchResultItem
			if err := sonic.Unmarshal(line, &resultItem); err != nil {
				provider.logger.Warn("failed to parse batch result line: %v", err)
				return err
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

// ContainerCreate creates a new container via OpenAI's API.
func (provider *OpenAIProvider) ContainerCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ContainerCreateRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil, providerName)
	}

	if request.Name == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: name is required", nil, providerName)
	}

	// Build request body
	reqBody := map[string]interface{}{
		"name": request.Name,
	}

	if request.ExpiresAfter != nil {
		reqBody["expires_after"] = map[string]interface{}{
			"anchor":  request.ExpiresAfter.Anchor,
			"minutes": request.ExpiresAfter.Minutes,
		}
	}

	if len(request.FileIDs) > 0 {
		reqBody["file_ids"] = request.FileIDs
	}

	if request.MemoryLimit != "" {
		reqBody["memory_limit"] = request.MemoryLimit
	}

	if len(request.Metadata) > 0 {
		reqBody["metadata"] = request.Metadata
	}

	// Merge ExtraParams into reqBody (do not overwrite mandatory keys)
	for k, v := range request.ExtraParams {
		if _, exists := reqBody[k]; !exists {
			reqBody[k] = v
		}
	}

	jsonBody, err := sonic.Marshal(reqBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/containers", schemas.ContainerCreateRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.SetBody(jsonBody)

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		return nil, ParseOpenAIError(resp, schemas.ContainerCreateRequest, providerName, "")
	}

	// Parse response
	responseBody := append([]byte(nil), resp.Body()...)

	var containerResp struct {
		ID           string                         `json:"id"`
		Object       string                         `json:"object"`
		Name         string                         `json:"name"`
		CreatedAt    int64                          `json:"created_at"`
		Status       schemas.ContainerStatus        `json:"status"`
		ExpiresAfter *schemas.ContainerExpiresAfter `json:"expires_after"`
		LastActiveAt *int64                         `json:"last_active_at"`
		MemoryLimit  string                         `json:"memory_limit"`
		Metadata     map[string]string              `json:"metadata"`
	}

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &containerResp, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := &schemas.BifrostContainerCreateResponse{
		ID:           containerResp.ID,
		Object:       containerResp.Object,
		Name:         containerResp.Name,
		CreatedAt:    containerResp.CreatedAt,
		Status:       containerResp.Status,
		ExpiresAfter: containerResp.ExpiresAfter,
		LastActiveAt: containerResp.LastActiveAt,
		MemoryLimit:  containerResp.MemoryLimit,
		Metadata:     containerResp.Metadata,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    providerName,
			RequestType: schemas.ContainerCreateRequest,
			Latency:     latency.Milliseconds(),
		},
	}

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ContainerList lists containers via OpenAI's API.
// Uses SerialListHelper for multi-key pagination - exhausts all pages from one key before moving to next.
func (provider *OpenAIProvider) ContainerList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil, providerName)
	}
	if len(keys) == 0 {
		if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
			keys = []schemas.Key{{}}
		} else {
			return nil, providerUtils.NewBifrostOperationError("provider config not found", nil, providerName)
		}
	}

	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ContainerListRequest); err != nil {
		return nil, err
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// Initialize serial pagination helper for multi-key support
	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err, providerName)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostContainerListResponse{
			Object:  "list",
			Data:    []schemas.ContainerObject{},
			HasMore: false,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:    providerName,
				RequestType: schemas.ContainerListRequest,
			},
		}, nil
	}

	// Build query string
	queryParams := url.Values{}
	if request.Limit > 0 {
		queryParams.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from helper instead of request.After
	if nativeCursor != "" {
		queryParams.Set("after", nativeCursor)
	}
	if request.Order != nil {
		queryParams.Set("order", *request.Order)
	}

	requestURL := provider.buildRequestURL(ctx, "/v1/containers", schemas.ContainerListRequest)
	if len(queryParams) > 0 {
		requestURL += "?" + queryParams.Encode()
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, ParseOpenAIError(resp, schemas.ContainerListRequest, providerName, "")
	}

	// Parse response
	responseBody := append([]byte(nil), resp.Body()...)

	var listResp struct {
		Object  string                    `json:"object"`
		Data    []schemas.ContainerObject `json:"data"`
		FirstID *string                   `json:"first_id"`
		LastID  *string                   `json:"last_id"`
		HasMore bool                      `json:"has_more"`
	}

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &listResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Track last container ID for pagination cursor
	var lastContainerID string
	for _, container := range listResp.Data {
		lastContainerID = container.ID
	}

	// Build cursor for next request (handles cross-key pagination)
	nextCursor, hasMore := helper.BuildNextCursor(listResp.HasMore, lastContainerID)

	response := &schemas.BifrostContainerListResponse{
		Object:  listResp.Object,
		Data:    listResp.Data,
		FirstID: listResp.FirstID,
		LastID:  listResp.LastID,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    providerName,
			RequestType: schemas.ContainerListRequest,
			Latency:     latency.Milliseconds(),
		},
	}

	// Set encoded cursor for next page
	if nextCursor != "" {
		response.After = &nextCursor
	}

	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ContainerRetrieve retrieves a specific container via OpenAI's API.
func (provider *OpenAIProvider) ContainerRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil, providerName)
	}
	if len(keys) == 0 {
		if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
			keys = []schemas.Key{{}}
		} else {
			return nil, providerUtils.NewBifrostOperationError("provider config not found", nil, providerName)
		}
	}
	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("container_id is required", nil, providerName)
	}

	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ContainerRetrieveRequest); err != nil {
		return nil, err
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

		req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/containers/"+request.ContainerID, schemas.ContainerRetrieveRequest))
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

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
			lastErr = ParseOpenAIError(resp, schemas.ContainerRetrieveRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		// Parse response
		responseBody := append([]byte(nil), resp.Body()...)

		var containerResp struct {
			ID           string                         `json:"id"`
			Object       string                         `json:"object"`
			Name         string                         `json:"name"`
			CreatedAt    int64                          `json:"created_at"`
			Status       schemas.ContainerStatus        `json:"status"`
			ExpiresAfter *schemas.ContainerExpiresAfter `json:"expires_after"`
			LastActiveAt *int64                         `json:"last_active_at"`
			MemoryLimit  string                         `json:"memory_limit"`
			Metadata     map[string]string              `json:"metadata"`
		}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &containerResp, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		response := &schemas.BifrostContainerRetrieveResponse{
			ID:           containerResp.ID,
			Object:       containerResp.Object,
			Name:         containerResp.Name,
			CreatedAt:    containerResp.CreatedAt,
			Status:       containerResp.Status,
			ExpiresAfter: containerResp.ExpiresAfter,
			LastActiveAt: containerResp.LastActiveAt,
			MemoryLimit:  containerResp.MemoryLimit,
			Metadata:     containerResp.Metadata,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:    providerName,
				RequestType: schemas.ContainerRetrieveRequest,
				Latency:     latency.Milliseconds(),
			},
		}

		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return response, nil
	}

	return nil, lastErr
}

// ContainerDelete deletes a container via OpenAI's API.
func (provider *OpenAIProvider) ContainerDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil, providerName)
	}
	if len(keys) == 0 {
		if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
			keys = []schemas.Key{{}}
		} else {
			return nil, providerUtils.NewBifrostOperationError("provider config not found", nil, providerName)
		}
	}
	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("container_id is required", nil, providerName)
	}

	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ContainerDeleteRequest); err != nil {
		return nil, err
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

		req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/containers/"+request.ContainerID, schemas.ContainerDeleteRequest))
		req.Header.SetMethod(http.MethodDelete)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

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
			lastErr = ParseOpenAIError(resp, schemas.ContainerDeleteRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		// Parse response
		responseBody := append([]byte(nil), resp.Body()...)

		var deleteResp struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Deleted bool   `json:"deleted"`
		}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &deleteResp, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		response := &schemas.BifrostContainerDeleteResponse{
			ID:      deleteResp.ID,
			Object:  deleteResp.Object,
			Deleted: deleteResp.Deleted,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:    providerName,
				RequestType: schemas.ContainerDeleteRequest,
				Latency:     latency.Milliseconds(),
			},
		}

		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			response.ExtraFields.RawRequest = rawRequest
		}
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			response.ExtraFields.RawResponse = rawResponse
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return response, nil
	}

	return nil, lastErr
}

// =============================================================================
// CONTAINER FILES API
// =============================================================================

// ContainerFileCreate creates a file in a container via OpenAI's API.
func (provider *OpenAIProvider) ContainerFileCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ContainerFileCreateRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil, providerName)
	}

	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: container_id is required", nil, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	endpoint := fmt.Sprintf("/v1/containers/%s/files", request.ContainerID)
	req.SetRequestURI(provider.buildRequestURL(ctx, endpoint, schemas.ContainerFileCreateRequest))
	req.Header.SetMethod(http.MethodPost)

	// Handle file upload (multipart only)
	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("invalid request: file is required", nil, providerName)
	}

	// Multipart file upload
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Add file
	part, err := writer.CreateFormFile("file", "file")
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create multipart form", err, providerName)
	}
	if _, err = part.Write(request.File); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write file to multipart form", err, providerName)
	}
	if err := writer.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to close multipart form", err, providerName)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetBody(body.Bytes())

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() >= 400 {
		return nil, ParseOpenAIError(resp, schemas.ContainerFileCreateRequest, providerName, "")
	}

	// Decode response body (handles content-encoding like gzip)
	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var fileResp struct {
		ID          string `json:"id"`
		Object      string `json:"object"`
		Bytes       int64  `json:"bytes"`
		CreatedAt   int64  `json:"created_at"`
		ContainerID string `json:"container_id"`
		Path        string `json:"path"`
		Source      string `json:"source"`
	}

	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &fileResp, nil, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	containerFileCreateResponse := &schemas.BifrostContainerFileCreateResponse{
		ID:          fileResp.ID,
		Object:      fileResp.Object,
		Bytes:       fileResp.Bytes,
		CreatedAt:   fileResp.CreatedAt,
		ContainerID: fileResp.ContainerID,
		Path:        fileResp.Path,
		Source:      fileResp.Source,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    providerName,
			RequestType: schemas.ContainerFileCreateRequest,
			Latency:     latency.Milliseconds(),
		},
	}

	// We don't capture payload for security reasons
	if sendBackRawRequest {
		containerFileCreateResponse.ExtraFields.RawRequest = "<REDACTED>"
	}
	if sendBackRawResponse {
		containerFileCreateResponse.ExtraFields.RawResponse = rawResponse
	}

	return containerFileCreateResponse, nil
}

// ContainerFileList lists files in a container via OpenAI's API.
// Uses SerialListHelper for multi-key pagination - exhausts all pages from one key before moving to next.
func (provider *OpenAIProvider) ContainerFileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil, providerName)
	}

	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: container_id is required", nil, providerName)
	}

	if len(keys) == 0 {
		if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
			keys = []schemas.Key{{}}
		} else {
			return nil, providerUtils.NewBifrostOperationError("no keys provided", nil, providerName)
		}
	}

	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ContainerFileListRequest); err != nil {
		return nil, err
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// Initialize serial pagination helper for multi-key support
	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err, providerName)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostContainerFileListResponse{
			Object:  "list",
			Data:    []schemas.ContainerFileObject{},
			HasMore: false,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:    providerName,
				RequestType: schemas.ContainerFileListRequest,
			},
		}, nil
	}

	// Build URL with query parameters
	endpoint := fmt.Sprintf("/v1/containers/%s/files", request.ContainerID)
	requestURL := provider.buildRequestURL(ctx, endpoint, schemas.ContainerFileListRequest)

	// Add query parameters
	queryParams := url.Values{}
	if request.Limit > 0 {
		queryParams.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from helper instead of request.After
	if nativeCursor != "" {
		queryParams.Set("after", nativeCursor)
	}
	if request.Order != nil {
		queryParams.Set("order", *request.Order)
	}
	if len(queryParams) > 0 {
		requestURL = requestURL + "?" + queryParams.Encode()
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if resp.StatusCode() >= 400 {
		return nil, ParseOpenAIError(resp, schemas.ContainerFileListRequest, providerName, "")
	}

	// Decode response body (handles content-encoding like gzip)
	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var listResp struct {
		Object  string                        `json:"object"`
		Data    []schemas.ContainerFileObject `json:"data"`
		FirstID *string                       `json:"first_id"`
		LastID  *string                       `json:"last_id"`
		HasMore bool                          `json:"has_more"`
	}

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &listResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Track last file ID for pagination cursor
	var lastFileID string
	for _, file := range listResp.Data {
		lastFileID = file.ID
	}

	// Build cursor for next request (handles cross-key pagination)
	nextCursor, hasMore := helper.BuildNextCursor(listResp.HasMore, lastFileID)

	containerFileListResponse := &schemas.BifrostContainerFileListResponse{
		Object:  listResp.Object,
		Data:    listResp.Data,
		FirstID: listResp.FirstID,
		LastID:  listResp.LastID,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    providerName,
			RequestType: schemas.ContainerFileListRequest,
			Latency:     latency.Milliseconds(),
		},
	}

	// Set encoded cursor for next page
	if nextCursor != "" {
		containerFileListResponse.After = &nextCursor
	}

	if sendBackRawRequest {
		containerFileListResponse.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		containerFileListResponse.ExtraFields.RawResponse = rawResponse
	}

	return containerFileListResponse, nil
}

// ContainerFileRetrieve retrieves a file from a container via OpenAI's API.
func (provider *OpenAIProvider) ContainerFileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if len(keys) == 0 {
		if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
			keys = []schemas.Key{{}}
		} else {
			return nil, providerUtils.NewBifrostOperationError("no keys provided", nil, providerName)
		}
	}

	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ContainerFileRetrieveRequest); err != nil {
		return nil, err
	}

	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil, providerName)
	}

	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: container_id is required", nil, providerName)
	}

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: file_id is required", nil, providerName)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

		endpoint := fmt.Sprintf("/v1/containers/%s/files/%s", request.ContainerID, request.FileID)
		req.SetRequestURI(provider.buildRequestURL(ctx, endpoint, schemas.ContainerFileRetrieveRequest))
		req.Header.SetMethod(http.MethodGet)

		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			lastErr = bifrostErr
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		if resp.StatusCode() >= 400 {
			lastErr = ParseOpenAIError(resp, schemas.ContainerFileRetrieveRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		// Decode response body (handles content-encoding like gzip)
		responseBody, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}
		sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
		sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

		var fileResp struct {
			ID          string `json:"id"`
			Object      string `json:"object"`
			Bytes       int64  `json:"bytes"`
			CreatedAt   int64  `json:"created_at"`
			ContainerID string `json:"container_id"`
			Path        string `json:"path"`
			Source      string `json:"source"`
		}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &fileResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			lastErr = bifrostErr
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		containerFileRetrieveResponse := &schemas.BifrostContainerFileRetrieveResponse{
			ID:          fileResp.ID,
			Object:      fileResp.Object,
			Bytes:       fileResp.Bytes,
			CreatedAt:   fileResp.CreatedAt,
			ContainerID: fileResp.ContainerID,
			Path:        fileResp.Path,
			Source:      fileResp.Source,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:    providerName,
				RequestType: schemas.ContainerFileRetrieveRequest,
				Latency:     latency.Milliseconds(),
			},
		}

		if sendBackRawRequest {
			containerFileRetrieveResponse.ExtraFields.RawRequest = rawRequest
		}
		if sendBackRawResponse {
			containerFileRetrieveResponse.ExtraFields.RawResponse = rawResponse
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return containerFileRetrieveResponse, nil
	}

	return nil, lastErr
}

// ContainerFileContent retrieves the content of a file from a container via OpenAI's API.
func (provider *OpenAIProvider) ContainerFileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if len(keys) == 0 {
		if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
			keys = []schemas.Key{{}}
		} else {
			return nil, providerUtils.NewBifrostOperationError("no keys provided", nil, providerName)
		}
	}

	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ContainerFileContentRequest); err != nil {
		return nil, err
	}

	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil, providerName)
	}

	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: container_id is required", nil, providerName)
	}

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: file_id is required", nil, providerName)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

		endpoint := fmt.Sprintf("/v1/containers/%s/files/%s/content", request.ContainerID, request.FileID)
		req.SetRequestURI(provider.buildRequestURL(ctx, endpoint, schemas.ContainerFileContentRequest))
		req.Header.SetMethod(http.MethodGet)

		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			lastErr = bifrostErr
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		if resp.StatusCode() >= 400 {
			lastErr = ParseOpenAIError(resp, schemas.ContainerFileContentRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		// Get content type from response header
		contentType := string(resp.Header.ContentType())
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		// Decode response body (handles content-encoding like gzip)
		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}
		content := append([]byte(nil), body...)

		containerFileContentResponse := &schemas.BifrostContainerFileContentResponse{
			Content:     content,
			ContentType: contentType,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:    providerName,
				RequestType: schemas.ContainerFileContentRequest,
				Latency:     latency.Milliseconds(),
			},
		}

		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			containerFileContentResponse.ExtraFields.RawRequest = map[string]string{
				"container_id": request.ContainerID,
				"file_id":      request.FileID,
			}
		}
		if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
			containerFileContentResponse.ExtraFields.RawResponse = "<REDACTED>"
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return containerFileContentResponse, nil
	}

	return nil, lastErr
}

// ContainerFileDelete deletes a file from a container via OpenAI's API.
func (provider *OpenAIProvider) ContainerFileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if len(keys) == 0 {
		if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
			keys = []schemas.Key{{}}
		} else {
			return nil, providerUtils.NewBifrostOperationError("no keys provided", nil, providerName)
		}
	}

	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ContainerFileDeleteRequest); err != nil {
		return nil, err
	}

	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("invalid request: nil", nil, providerName)
	}

	if request.ContainerID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: container_id is required", nil, providerName)
	}

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid request: file_id is required", nil, providerName)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

		endpoint := fmt.Sprintf("/v1/containers/%s/files/%s", request.ContainerID, request.FileID)
		req.SetRequestURI(provider.buildRequestURL(ctx, endpoint, schemas.ContainerFileDeleteRequest))
		req.Header.SetMethod(http.MethodDelete)

		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			lastErr = bifrostErr
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		if resp.StatusCode() >= 400 {
			lastErr = ParseOpenAIError(resp, schemas.ContainerFileDeleteRequest, providerName, "")
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		// Decode response body (handles content-encoding like gzip)
		responseBody, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}
		sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
		sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

		var deleteResp struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Deleted bool   `json:"deleted"`
		}

		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &deleteResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			lastErr = bifrostErr
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		containerFileDeleteResponse := &schemas.BifrostContainerFileDeleteResponse{
			ID:      deleteResp.ID,
			Object:  deleteResp.Object,
			Deleted: deleteResp.Deleted,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:    providerName,
				RequestType: schemas.ContainerFileDeleteRequest,
				Latency:     latency.Milliseconds(),
			},
		}

		if sendBackRawRequest {
			containerFileDeleteResponse.ExtraFields.RawRequest = rawRequest
		}
		if sendBackRawResponse {
			containerFileDeleteResponse.ExtraFields.RawResponse = rawResponse
		}

		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return containerFileDeleteResponse, nil
	}

	return nil, lastErr
}
