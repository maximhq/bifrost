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
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}
}

// GetProviderKey returns the provider identifier for OpenAI.
func (provider *OpenAIProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.OpenAI, provider.customProviderConfig)
}

// buildRequestURL constructs the full request URL using the provider's configuration.
func (provider *OpenAIProvider) buildRequestURL(ctx context.Context, defaultPath string, requestType schemas.RequestType) string {
	return provider.networkConfig.BaseURL + providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
}

func (provider *OpenAIProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
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
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func listModelsByKey(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
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

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
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
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, openaiResponse, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := openaiResponse.ToBifrostListModelsResponse(providerName, key.Models)

	response.ExtraFields.Provider = providerName
	response.ExtraFields.RequestType = schemas.ListModelsRequest
	response.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// HandleOpenAIListModelsRequest handles a list models request to OpenAI's API.
func HandleOpenAIListModelsRequest(
	ctx context.Context,
	client *fasthttp.Client,
	request *schemas.BifrostListModelsRequest,
	url string,
	keys []schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawResponse bool,
	logger schemas.Logger,
) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if len(keys) == 0 {
		return listModelsByKey(ctx, client, url, schemas.Key{}, extraHeaders, providerName, sendBackRawResponse)
	}
	listModelsByKeyWrapper := func(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
		return listModelsByKey(ctx, client, url, key, extraHeaders, providerName, sendBackRawResponse)
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
func (provider *OpenAIProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
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
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// HandleOpenAITextCompletionRequest handles a text completion request to OpenAI's API.
func HandleOpenAITextCompletionRequest(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostTextCompletionRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	sendBackRawResponse bool,
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

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
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
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, ParseOpenAIError(resp, schemas.TextCompletionRequest, providerName, request.Model)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	response := &schemas.BifrostTextCompletionResponse{}

	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.TextCompletionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// TextCompletionStream performs a streaming text completion request to OpenAI's API.
// It formats the request, sends it to OpenAI, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *OpenAIProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.TextCompletionStreamRequest); err != nil {
		return nil, err
	}
	var authHeader map[string]string
	if key.Value != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value}
	}
	return HandleOpenAITextCompletionStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/completions", schemas.TextCompletionStreamRequest),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		provider.logger,
	)
}

// HandleOpenAITextCompletionStreaming handles text completion streaming for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE format.
func HandleOpenAITextCompletionStreaming(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostTextCompletionRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	postResponseConverter func(*schemas.BifrostTextCompletionResponse) *schemas.BifrostTextCompletionResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
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
		return nil, ParseOpenAIError(resp, schemas.TextCompletionStreamRequest, providerName, request.Model)
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

		chunkIndex := -1
		usage := &schemas.BifrostLLMUsage{}

		var finishReason *string
		var messageID string
		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			// Check if context is done before processing
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
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, logger)
					return
				}
			}

			// Parse into bifrost response
			var response schemas.BifrostTextCompletionResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
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

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(&response, nil, nil, nil, nil), responseChan)
			}

			// For providers that don't send [DONE] marker break on finish_reason
			if !providerUtils.ProviderSendsDoneMarker(providerName) && finishReason != nil {
				break
			}
		}

		// Handle scanner errors first
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.TextCompletionStreamRequest, providerName, request.Model, logger)
		} else {
			response := providerUtils.CreateBifrostTextCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, schemas.TextCompletionStreamRequest, providerName, request.Model)
			if postResponseConverter != nil {
				response = postResponseConverter(response)
			}
			response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
			ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(response, nil, nil, nil, nil), responseChan)
		}
	}()

	return responseChan, nil
}

// ChatCompletion performs a chat completion request to the OpenAI API.
// It supports both text and image content in messages.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *OpenAIProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
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
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		provider.logger,
	)
}

// HandleOpenAIChatCompletionRequest handles a chat completion request to OpenAI's API.
func HandleOpenAIChatCompletionRequest(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostChatRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
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

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToOpenAIChatRequest(request), nil },
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.ChatCompletionRequest, providerName, request.Model)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	response := &schemas.BifrostChatResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ChatCompletionRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	return response, nil
}

// ChatCompletionStream handles streaming for OpenAI chat completions.
// It formats messages, prepares request body, and uses shared streaming logic.
// Returns a channel for streaming responses and any error that occurred.
func (provider *OpenAIProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if chat completion stream is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}
	var authHeader map[string]string
	if key.Value != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value}
	}
	// Use shared streaming logic
	return HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/chat/completions", schemas.ChatCompletionStreamRequest),
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// HandleOpenAIChatCompletionStreaming handles streaming for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE format.
func HandleOpenAIChatCompletionStreaming(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostChatRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	customRequestConverter func(*schemas.BifrostChatRequest) (any, error),
	postRequestConverter func(*OpenAIChatRequest) *OpenAIChatRequest,
	postResponseConverter func(*schemas.BifrostChatResponse) *schemas.BifrostChatResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
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
			reqBody := ToOpenAIChatRequest(request)
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
		return nil, ParseOpenAIError(resp, schemas.ChatCompletionStreamRequest, providerName, request.Model)
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

		chunkIndex := -1
		usage := &schemas.BifrostLLMUsage{}

		startTime := time.Now()
		lastChunkTime := startTime

		var finishReason *string
		var messageID string

		for scanner.Scan() {
			// Check if context is done before processing
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
						RequestType:    schemas.ChatCompletionStreamRequest,
					}
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, logger)
					return
				}
			}

			// Parse into bifrost response
			var response schemas.BifrostChatResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
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

						ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
						return
					}

					response.ExtraFields.RequestType = schemas.ResponsesStreamRequest
					response.ExtraFields.Provider = providerName
					response.ExtraFields.ModelRequested = request.Model
					response.ExtraFields.ChunkIndex = response.SequenceNumber

					if sendBackRawResponse {
						response.ExtraFields.RawResponse = jsonData
					}

					if response.Type == schemas.ResponsesStreamResponseTypeCompleted {
						response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
						ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil), responseChan)
						return
					}

					response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
					lastChunkTime = time.Now()

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil), responseChan)
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
				if choice.ChatStreamResponseChoice != nil &&
					choice.ChatStreamResponseChoice.Delta != nil &&
					(choice.ChatStreamResponseChoice.Delta.Content != nil ||
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

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, &response, nil, nil, nil), responseChan)
				}

				// For providers that don't send [DONE] marker break on finish_reason
				if !providerUtils.ProviderSendsDoneMarker(providerName) && finishReason != nil {
					break
				}
			}
		}

		// Handle scanner errors first
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, logger)
		} else if !isResponsesToChatCompletionsFallback {
			response := providerUtils.CreateBifrostChatCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, schemas.ChatCompletionStreamRequest, providerName, request.Model)
			if postResponseConverter != nil {
				response = postResponseConverter(response)
			}
			response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
			ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil), responseChan)
		}
	}()

	return responseChan, nil
}

// Responses performs a responses request to the OpenAI API.
func (provider *OpenAIProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		provider.logger,
	)
}

// HandleOpenAIResponsesRequest handles a responses request to OpenAI's API.
func HandleOpenAIResponsesRequest(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostResponsesRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
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

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
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
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.ResponsesRequest, providerName, request.Model)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	response := &schemas.BifrostResponsesResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Set raw response if enabled
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.ResponsesRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	return response, nil
}

// ResponsesStream performs a streaming responses request to the OpenAI API.
func (provider *OpenAIProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	// Check if chat completion stream is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}
	var authHeader map[string]string
	if key.Value != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value}
	}
	// Use shared streaming logic
	return HandleOpenAIResponsesStreaming(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/responses", schemas.ResponsesStreamRequest),
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

// HandleOpenAIResponsesStreaming handles streaming for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE format.
func HandleOpenAIResponsesStreaming(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostResponsesRequest,
	authHeader map[string]string,
	extraHeaders map[string]string,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	postRequestConverter func(*OpenAIResponsesRequest) *OpenAIResponsesRequest,
	postResponseConverter func(*schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse,
	logger schemas.Logger,
) (chan *schemas.BifrostStream, *schemas.BifrostError) {
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
		return nil, ParseOpenAIError(resp, schemas.ResponsesStreamRequest, providerName, request.Model)
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

		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			// Check if context is done before processing
			select {
			case <-ctx.Done():
				return
			default:
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
				logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
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

				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
				return
			}

			response.ExtraFields.RequestType = schemas.ResponsesStreamRequest
			response.ExtraFields.Provider = providerName
			response.ExtraFields.ModelRequested = request.Model
			response.ExtraFields.ChunkIndex = response.SequenceNumber

			if response.Type == schemas.ResponsesStreamResponseTypeCompleted || response.Type == schemas.ResponsesStreamResponseTypeIncomplete {
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, &response, nil, nil), responseChan)
				return
			}

			response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
			lastChunkTime = time.Now()

			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, &response, nil, nil), responseChan)
		}
		// Handle scanner errors first
		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, request.Model, logger)
		}
	}()

	return responseChan, nil
}

// Embedding generates embeddings for the given input text(s).
// The input can be either a single string or a slice of strings for batch embedding.
// Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *OpenAIProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
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
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// HandleOpenAIEmbeddingRequest handles embedding requests for OpenAI-compatible APIs.
// This shared function reduces code duplication between providers that use the same embedding request format.
func HandleOpenAIEmbeddingRequest(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostEmbeddingRequest,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
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

	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
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
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.EmbeddingRequest, providerName, request.Model)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	response := &schemas.BifrostEmbeddingResponse{}

	// Use enhanced response handler with pre-allocated response
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, response, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.RequestType = schemas.EmbeddingRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// Speech handles non-streaming speech synthesis requests.
// It formats the request body, makes the API call, and returns the response.
// Returns the response and any error that occurred.
func (provider *OpenAIProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/audio/speech", schemas.SpeechRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
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
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.SpeechRequest, providerName, request.Model)
	}

	// Get the binary audio data from the response body
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
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

	return bifrostResponse, nil
}

// SpeechStream handles streaming for speech synthesis.
// It formats the request body, creates HTTP request, and uses shared streaming logic.
// Returns a channel for streaming responses and any error that occurred.
func (provider *OpenAIProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.SpeechStreamRequest); err != nil {
		return nil, err
	}

	for _, model := range providerUtils.UnsupportedSpeechStreamModels {
		if model == request.Model {
			return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("model %s is not supported for streaming speech synthesis", model), nil, provider.GetProviderKey())
		}
	}

	providerName := provider.GetProviderKey()
	// Use centralized converter
	reqBody := ToOpenAISpeechRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("speech input is not provided", nil, providerName)
	}
	reqBody.StreamFormat = schemas.Ptr("sse")

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := ToOpenAISpeechRequest(request)
			if reqBody != nil {
				reqBody.StreamFormat = schemas.Ptr("sse")
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

	// Prepare OpenAI headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}

	if key.Value != "" {
		headers["Authorization"] = "Bearer " + key.Value
	}

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/audio/speech", schemas.SpeechStreamRequest))
	req.Header.SetContentType("application/json")

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Set any extra headers from network config
	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

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
		return nil, ParseOpenAIError(resp, schemas.SpeechStreamRequest, providerName, request.Model)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer providerUtils.ReleaseStreamingResponse(resp)

		scanner := bufio.NewScanner(resp.BodyStream())
		chunkIndex := -1

		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			// Check if context is done before processing
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
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, provider.logger)
					return
				}
			}

			// Parse into bifrost response
			var response schemas.BifrostSpeechStreamResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
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

			if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
				response.ExtraFields.RawResponse = jsonData
			}

			if response.Usage != nil {
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, &response, nil), responseChan)
				return
			}

			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, &response, nil), responseChan)
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.SpeechStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// Transcription handles non-streaming transcription requests.
// It creates a multipart form, adds fields, makes the API call, and returns the response.
// Returns the response and any error that occurred.
func (provider *OpenAIProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.TranscriptionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

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

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/audio/transcriptions", schemas.TranscriptionRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType()) // This sets multipart/form-data with boundary
	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	req.SetBody(body.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, ParseOpenAIError(resp, schemas.TranscriptionRequest, providerName, request.Model)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	// Parse OpenAI's transcription response directly into BifrostTranscribe
	response := &schemas.BifrostTranscriptionResponse{}

	if err := sonic.Unmarshal(responseBody, response); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Parse raw response for RawResponse field
	var rawResponse interface{}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRawResponseUnmarshal, err, providerName)
		}
	}

	response.ExtraFields = schemas.BifrostResponseExtraFields{
		RequestType:    schemas.TranscriptionRequest,
		Provider:       providerName,
		ModelRequested: request.Model,
		Latency:        latency.Milliseconds(),
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil

}

// TranscriptionStream performs a streaming transcription request to the OpenAI API.
func (provider *OpenAIProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.OpenAI, provider.customProviderConfig, schemas.TranscriptionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// Use centralized converter
	reqBody := ToOpenAITranscriptionRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("transcription input is not provided", nil, providerName)
	}
	reqBody.Stream = schemas.Ptr(true)

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

	if key.Value != "" {
		headers["Authorization"] = "Bearer " + key.Value
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/audio/transcriptions", schemas.TranscriptionStreamRequest))
	req.Header.SetContentType("application/json")

	// Set headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	req.SetBody(body.Bytes())

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
		return nil, ParseOpenAIError(resp, schemas.TranscriptionStreamRequest, providerName, request.Model)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer providerUtils.ReleaseStreamingResponse(resp)

		scanner := bufio.NewScanner(resp.BodyStream())
		chunkIndex := -1

		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			// Check if context is done before processing
			select {
			case <-ctx.Done():
				return
			default:
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
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, provider.logger)
					return
				}
			}

			var response schemas.BifrostTranscriptionStreamResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
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

			if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
				response.ExtraFields.RawResponse = jsonData
			}

			if response.Usage != nil {
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, &response), responseChan)
				return
			}

			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, &response), responseChan)
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.TranscriptionStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// parseTranscriptionFormDataBodyFromRequest parses the transcription request and writes it to the multipart form.
func parseTranscriptionFormDataBodyFromRequest(writer *multipart.Writer, openaiReq *OpenAITranscriptionRequest, providerName schemas.ModelProvider) *schemas.BifrostError {
	// Add file field
	fileWriter, err := writer.CreateFormFile("file", "audio.mp3") // OpenAI requires a filename
	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to create form file", err, providerName)
	}
	if _, err := fileWriter.Write(openaiReq.File); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write file data", err, providerName)
	}

	// Add model field
	if err := writer.WriteField("model", openaiReq.Model); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write model field", err, providerName)
	}

	// Add optional fields
	if openaiReq.Language != nil {
		if err := writer.WriteField("language", *openaiReq.Language); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write language field", err, providerName)
		}
	}

	if openaiReq.Prompt != nil {
		if err := writer.WriteField("prompt", *openaiReq.Prompt); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write prompt field", err, providerName)
		}
	}

	if openaiReq.ResponseFormat != nil {
		if err := writer.WriteField("response_format", *openaiReq.ResponseFormat); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write response_format field", err, providerName)
		}
	}

	if openaiReq.Stream != nil && *openaiReq.Stream {
		if err := writer.WriteField("stream", "true"); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write stream field", err, providerName)
		}
	}

	// Close the multipart writer
	if err := writer.Close(); err != nil {
		return providerUtils.NewBifrostOperationError("failed to close multipart writer", err, providerName)
	}

	return nil
}
