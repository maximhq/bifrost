package huggingface

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// debug toggles extra debug logging for the Hugging Face provider. It can be
// enabled at runtime by setting the HUGGINGFACE_DEBUG environment variable to "true".
var debug = (os.Getenv("HUGGINGFACE_DEBUG") == "true")

// HuggingFaceProvider implements the Provider interface for Hugging Face's inference APIs.
type HuggingFaceProvider struct {
	logger               schemas.Logger
	client               *fasthttp.Client
	networkConfig        schemas.NetworkConfig
	sendBackRawResponse  bool
	customProviderConfig *schemas.CustomProviderConfig
}

var huggingFaceChatResponsePool = sync.Pool{
	New: func() any {
		return &HuggingFaceChatResponse{}
	},
}

var huggingFaceTranscriptionResponsePool = sync.Pool{
	New: func() any {
		return &HuggingFaceTranscriptionResponse{}
	},
}

var huggingFaceSpeechResponsePool = sync.Pool{
	New: func() any {
		return &HuggingFaceSpeechResponse{}
	},
}

func acquireHuggingFaceChatResponse() *HuggingFaceChatResponse {
	resp := huggingFaceChatResponsePool.Get().(*HuggingFaceChatResponse)
	*resp = HuggingFaceChatResponse{} // Reset the struct
	return resp
}

func releaseHuggingFaceChatResponse(resp *HuggingFaceChatResponse) {
	if resp != nil {
		huggingFaceChatResponsePool.Put(resp)
	}
}

func acquireHuggingFaceTranscriptionResponse() *HuggingFaceTranscriptionResponse {
	resp := huggingFaceTranscriptionResponsePool.Get().(*HuggingFaceTranscriptionResponse)
	*resp = HuggingFaceTranscriptionResponse{} // Reset the struct
	return resp
}

func releaseHuggingFaceTranscriptionResponse(resp *HuggingFaceTranscriptionResponse) {
	if resp != nil {
		huggingFaceTranscriptionResponsePool.Put(resp)
	}
}

func acquireHuggingFaceSpeechResponse() *HuggingFaceSpeechResponse {
	resp := huggingFaceSpeechResponsePool.Get().(*HuggingFaceSpeechResponse)
	*resp = HuggingFaceSpeechResponse{} // Reset the struct
	return resp
}

func releaseHuggingFaceSpeechResponse(resp *HuggingFaceSpeechResponse) {
	if resp != nil {
		huggingFaceSpeechResponsePool.Put(resp)
	}
}

// NewHuggingFaceProvider creates a new Hugging Face provider instance configured with the provided settings.
func NewHuggingFaceProvider(config *schemas.ProviderConfig, logger schemas.Logger) *HuggingFaceProvider {
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
		huggingFaceChatResponsePool.Put(&HuggingFaceChatResponse{})
		huggingFaceSpeechResponsePool.Put(&HuggingFaceSpeechResponse{})
		huggingFaceTranscriptionResponsePool.Put(&HuggingFaceTranscriptionResponse{})
	}

	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = defaultInferenceBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &HuggingFaceProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}
}

// GetProviderKey returns the provider key, taking custom providers into account.
func (provider *HuggingFaceProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.HuggingFace, provider.customProviderConfig)
}

// buildRequestURL composes the final request URL based on context overrides.
func (provider *HuggingFaceProvider) buildRequestURL(ctx context.Context, defaultPath string, requestType schemas.RequestType) string {
	return provider.networkConfig.BaseURL + providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
}

func (provider *HuggingFaceProvider) completeRequest(ctx context.Context, jsonData []byte, url string, key string) ([]byte, time.Duration, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	req.SetBody(jsonData)
	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] request URL: %s", url))
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] request body: %s", string(jsonData)))
	}

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] error response headers: %s", resp.Header.String()))
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] error response body: %s", string(resp.Body())))

		return nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		// Always log the status; include body when debug enabled
		if debug {
			provider.logger.Debug(fmt.Sprintf("error from %s provider: status=%d body=%s", provider.GetProviderKey(), resp.StatusCode(), string(resp.Body())))
			provider.logger.Debug(fmt.Sprintf("[huggingface debug] error response headers: %s", resp.Header.String()))
			provider.logger.Debug(fmt.Sprintf("[huggingface debug] error response body: %s", string(resp.Body())))
		} else {
			provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", provider.GetProviderKey(), string(resp.Body())))
		}

		var errorResp HuggingFaceResponseError

		bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
		bifrostErr.Type = &errorResp.Type
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = errorResp.Message

		return nil, latency, bifrostErr
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	// Read the response body and copy it before releasing the response
	// to avoid use-after-free since resp.Body() references fasthttp's internal buffer
	bodyCopy := append([]byte(nil), body...)

	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] response body (truncated 1024): %s", func() string {
			b := bodyCopy
			if len(b) > 1024 {
				return string(b[:1024]) + "..."
			}
			return string(b)
		}()))
	}

	return bodyCopy, latency, nil
}

func (provider *HuggingFaceProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	type providerResult struct {
		provider inferenceProvider
		response *HuggingFaceListModelsResponse
		latency  int64
		rawResp  map[string]interface{}
		err      *schemas.BifrostError
	}

	resultsChan := make(chan providerResult, len(INFERENCE_PROVIDERS))
	var wg sync.WaitGroup

	for _, infProvider := range INFERENCE_PROVIDERS {
		wg.Add(1)
		go func(inferProvider inferenceProvider) {
			defer wg.Done()

			req := fasthttp.AcquireRequest()
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseRequest(req)
			defer fasthttp.ReleaseResponse(resp)

			providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

			modelHubURL := provider.buildModelHubURL(request, inferProvider)
			req.SetRequestURI(modelHubURL)
			req.Header.SetMethod(http.MethodGet)
			req.Header.SetContentType("application/json")
			if key.Value != "" {
				req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value))
			}

			if debug {
				provider.logger.Debug(fmt.Sprintf("[huggingface debug] list models request URL for %s: %s", inferProvider, modelHubURL))
			}

			latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
			if bifrostErr != nil {
				resultsChan <- providerResult{provider: inferProvider, err: bifrostErr}
				return
			}

			if resp.StatusCode() != fasthttp.StatusOK {
				var errorResp HuggingFaceHubError
				bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
				bifrostErr.Error.Message = errorResp.Message
				resultsChan <- providerResult{provider: inferProvider, err: bifrostErr}
				return
			}

			body, err := providerUtils.CheckAndDecodeBody(resp)
			if err != nil {
				resultsChan <- providerResult{provider: inferProvider, err: providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)}
				return
			}

			if debug {
				provider.logger.Debug(fmt.Sprintf("[huggingface debug] list models response for %s (truncated 1024): %s", inferProvider, func() string {
					if len(body) > 1024 {
						return string(body[:1024]) + "..."
					}
					return string(body)
				}()))
			}

			var huggingfaceAPIResponse HuggingFaceListModelsResponse
			rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &huggingfaceAPIResponse, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
			if bifrostErr != nil {
				resultsChan <- providerResult{provider: inferProvider, err: bifrostErr}
				return
			}

			var rawRespMap map[string]interface{}
			if rawResponse != nil {
				if converted, ok := rawResponse.(map[string]interface{}); ok {
					rawRespMap = converted
				}
			}

			resultsChan <- providerResult{
				provider: inferProvider,
				response: &huggingfaceAPIResponse,
				latency:  latency.Milliseconds(),
				rawResp:  rawRespMap,
			}
		}(infProvider)
	}

	// Close results channel after all goroutines complete
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Aggregate results
	aggregatedResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0),
	}
	var totalLatency int64
	var successCount int
	var firstError *schemas.BifrostError
	var rawResponses []map[string]interface{}

	for result := range resultsChan {
		if result.err != nil {
			if debug {
				provider.logger.Debug(fmt.Sprintf("[huggingface debug] error fetching models for %s: %v", result.provider, result.err))
			}
			if firstError == nil {
				firstError = result.err
			}
			continue
		}

		if result.response != nil {
			providerResponse := result.response.ToBifrostListModelsResponse(providerName, result.provider)
			if providerResponse != nil {
				aggregatedResponse.Data = append(aggregatedResponse.Data, providerResponse.Data...)
				totalLatency += result.latency
				successCount++
				if result.rawResp != nil {
					rawResponses = append(rawResponses, result.rawResp)
				}
			}
		}
	}

	// If all requests failed, return the first error
	if successCount == 0 && firstError != nil {
		return nil, firstError
	}

	// Calculate average latency
	if successCount > 0 {
		aggregatedResponse.ExtraFields.Latency = totalLatency / int64(successCount)
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) && len(rawResponses) > 0 {
		// Combine all raw responses into a single map
		combinedRaw := make(map[string]interface{})
		for i, raw := range rawResponses {
			combinedRaw[fmt.Sprintf("provider_%d", i)] = raw
		}
		aggregatedResponse.ExtraFields.RawResponse = combinedRaw
	}

	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] aggregated %d models from %d providers", len(aggregatedResponse.Data), successCount))
	}

	return aggregatedResponse, nil
}

// ListModels queries the Hugging Face model hub API to list models served by the inference provider.
func (provider *HuggingFaceProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {

	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
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

func (provider *HuggingFaceProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

func (provider *HuggingFaceProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

func (provider *HuggingFaceProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] ChatCompletion entry: original model=%s", request.Model))
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, provider.GetProviderKey())
	}
	request.Model = fmt.Sprintf("%s:%s", modelName, inferenceProvider)

	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] ChatCompletion after split: inferenceProvider=%s, modelName=%s, request.Model=%s", inferenceProvider, modelName, request.Model))
	}

	jsonBody, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := ToHuggingFaceChatCompletionRequest(request)
			if reqBody != nil {
				reqBody.Stream = schemas.Ptr(false)
			}
			return reqBody, nil
		},
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] ChatCompletion jsonBody: %s", string(jsonBody)))
	}

	requestURL := provider.buildRequestURL(ctx, "/v1/chat/completions", schemas.ChatCompletionRequest)
	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] ChatCompletion request model=%s url=%s", request.Model, requestURL))
	}

	responseBody, latency, err := provider.completeRequest(ctx, jsonBody, requestURL, key.Value)
	if err != nil {
		return nil, err
	}

	response := acquireHuggingFaceChatResponse()
	defer releaseHuggingFaceChatResponse(response)

	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse, convErr := response.ToBifrostChatResponse(request.Model)
	if convErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, convErr, provider.GetProviderKey())
	}

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

func (provider *HuggingFaceProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

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

	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] ChatCompletionStream: starting request for model %s, isResponsesFallback=%v", request.Model, isResponsesToChatCompletionsFallback))
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionStreamRequest, provider.GetProviderKey())
	}
	request.Model = fmt.Sprintf("%s:%s", modelName, inferenceProvider)

	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] ChatCompletionStream: split model to provider=%s, model=%s", inferenceProvider, modelName))
	}

	jsonBody, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			reqBody := ToHuggingFaceChatCompletionRequest(request)
			if reqBody != nil {
				reqBody.Stream = schemas.Ptr(true)
			}
			return reqBody, nil
		},
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] ChatCompletionStream jsonBody: %s", string(jsonBody)))
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	streamURL := provider.buildRequestURL(ctx, "/v1/chat/completions", schemas.ChatCompletionStreamRequest)
	req.SetRequestURI(streamURL)
	req.Header.SetContentType("application/json")

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	// Set headers
	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	req.SetBody(jsonBody)
	if debug {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] ChatCompletionStream request model=%s url=%s", request.Model, streamURL))
	}

	// Make the request
	apiErr := provider.client.Do(req, resp)
	if apiErr != nil {
		defer providerUtils.ReleaseStreamingResponse(resp)
		if errors.Is(apiErr, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   apiErr,
				},
			}
		}
		if errors.Is(apiErr, fasthttp.ErrTimeout) || errors.Is(apiErr, context.DeadlineExceeded) {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, apiErr, providerName)
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, apiErr, providerName)
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

		for scanner.Scan() {
			// Check if context is done before processing
			select {
			case <-ctx.Done():
				if debug {
					provider.logger.Debug("[huggingface] context cancelled, stopping stream")
				}
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
				if debug {
					provider.logger.Debug("[huggingface] received [DONE] marker")
				}
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
			var errorResp HuggingFaceResponseError
			if err := sonic.Unmarshal([]byte(jsonData), &errorResp); err == nil {
				if errorResp.Message != "" {
					if debug {
						provider.logger.Debug(fmt.Sprintf("[huggingface] error in stream: %s", errorResp.Message))
					}
					bifrostErr := &schemas.BifrostError{
						Type:           &errorResp.Type,
						IsBifrostError: false,
						Error: &schemas.ErrorField{
							Message: errorResp.Message,
						},
						ExtraFields: schemas.BifrostErrorExtraFields{
							Provider:       providerName,
							ModelRequested: request.Model,
							RequestType:    schemas.ChatCompletionStreamRequest,
						},
					}
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
					return
				}
			}

			// Parse into HuggingFace stream response
			var streamResp HuggingFaceChatStreamResponse
			if err := sonic.Unmarshal([]byte(jsonData), &streamResp); err != nil {
				if debug {
					provider.logger.Debug(fmt.Sprintf("[huggingface] failed to parse chunk %d: %v", chunkIndex, err))
				}
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			// Convert to Bifrost response
			response := streamResp.ToBifrostChatStreamResponse()
			if response == nil {
				if debug {
					provider.logger.Debug(fmt.Sprintf("[huggingface] chunk %d: conversion returned nil, skipping", chunkIndex))
				}
				continue
			}

			// Set extra fields
			response.ExtraFields = schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ChatCompletionStreamRequest,
				Provider:       providerName,
				ModelRequested: request.Model,
				ChunkIndex:     chunkIndex,
				Latency:        time.Since(lastChunkTime).Milliseconds(),
			}

			if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
				response.ExtraFields.RawResponse = jsonData
			}

			// Check if this is the last chunk (has usage)
			if streamResp.Usage != nil {
				response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
				ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
			}

			lastChunkTime = time.Now()
			chunkIndex++

			// Handle conversion to ResponsesStream if needed
			if isResponsesToChatCompletionsFallback {
				// Workaround for combined usage and content in the same chunk:
				// If we have both usage and choices, split them into two separate processing steps
				// to ensure content events are generated before completion events.
				if response.Usage != nil && len(response.Choices) > 0 {
					// 1. Process content without usage
					contentResponse := *response
					contentResponse.Usage = nil

					responsesResponses := contentResponse.ToBifrostResponsesStreamResponse(responsesStreamState)
					for _, responsesResp := range responsesResponses {
						if responsesResp != nil {
							responsesResp.ExtraFields.RequestType = schemas.ResponsesStreamRequest
							bifrostStream := providerUtils.GetBifrostResponseForStreamResponse(nil, nil, responsesResp, nil, nil)
							providerUtils.ProcessAndSendResponse(ctx, postHookRunner, bifrostStream, responseChan)
						}
					}

					// 2. Process usage without choices (to trigger completion)
					usageResponse := *response
					usageResponse.Choices = nil

					responsesResponses = usageResponse.ToBifrostResponsesStreamResponse(responsesStreamState)
					for _, responsesResp := range responsesResponses {
						if responsesResp != nil {
							responsesResp.ExtraFields.RequestType = schemas.ResponsesStreamRequest
							bifrostStream := providerUtils.GetBifrostResponseForStreamResponse(nil, nil, responsesResp, nil, nil)
							providerUtils.ProcessAndSendResponse(ctx, postHookRunner, bifrostStream, responseChan)
						}
					}
				} else {
					// Convert chat streaming response to responses streaming responses
					responsesResponses := response.ToBifrostResponsesStreamResponse(responsesStreamState)

					for _, responsesResp := range responsesResponses {
						if responsesResp != nil {
							// Update RequestType for responses stream
							responsesResp.ExtraFields.RequestType = schemas.ResponsesStreamRequest
							bifrostStream := providerUtils.GetBifrostResponseForStreamResponse(nil, nil, responsesResp, nil, nil)
							providerUtils.ProcessAndSendResponse(ctx, postHookRunner, bifrostStream, responseChan)
						}
					}
				}
			} else {
				bifrostStream := providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, bifrostStream, responseChan)
			}
		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

func (provider *HuggingFaceProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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

func (provider *HuggingFaceProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return provider.ChatCompletionStream(
		ctx,
		postHookRunner,
		key,
		request.ToChatRequest(),
	)
}

func (provider *HuggingFaceProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	jsonBody, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToHuggingFaceEmbeddingRequest(request) },
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
	}

	providerMapping, err := provider.getModelInferenceProviderMapping(ctx, modelName)
	if err != nil {
		return nil, err
	}

	if providerMapping == nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
	}

	mapping, ok := providerMapping[inferenceProvider]
	if !ok || mapping.ProviderModelID == "" || mapping.ProviderTask != "feature-extraction" {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
	}

	// Use the provider-specific model id
	modelName = mapping.ProviderModelID

	url, urlErr := provider.getInferenceProviderRouteURL(ctx, inferenceProvider, modelName, schemas.EmbeddingRequest)
	if urlErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
	}
	responseBody, latency, err := provider.completeRequest(ctx, jsonBody, url, key.Value)
	if err != nil {
		return nil, err
	}

	var huggingfaceResponse HuggingFaceEmbeddingResponse

	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &huggingfaceResponse, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse, convErr := huggingfaceResponse.ToBifrostEmbeddingResponse(request.Model)
	if convErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, convErr, provider.GetProviderKey())
	}

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

func (provider *HuggingFaceProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	// Check if Speech is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
		return nil, err
	}

	// Prepare request body using Speech-specific function
	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToHuggingFaceSpeechRequest(request) },
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
	}

	providerMapping, err := provider.getModelInferenceProviderMapping(ctx, modelName)
	if err != nil {
		return nil, err
	}

	if providerMapping == nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
	}

	mapping, ok := providerMapping[inferenceProvider]
	if !ok || mapping.ProviderModelID == "" || mapping.ProviderTask != "text-to-speech" {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
	}

	// Use the provider-specific model id
	modelName = mapping.ProviderModelID

	url, urlErr := provider.getInferenceProviderRouteURL(ctx, inferenceProvider, modelName, schemas.SpeechRequest)
	if urlErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
	}
	responseBody, latency, err := provider.completeRequest(ctx, jsonData, url, key.Value)
	if err != nil {
		return nil, err
	}

	response := acquireHuggingFaceSpeechResponse()
	defer releaseHuggingFaceSpeechResponse(response)

	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Download the audio file from the URL
	audioData, downloadErr := provider.downloadAudioFromURL(ctx, response.Audio.URL)
	if downloadErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, downloadErr, provider.GetProviderKey())
	}

	bifrostResponse, convErr := response.ToBifrostSpeechResponse(request.Model, audioData)
	if convErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, convErr, provider.GetProviderKey())
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.SpeechRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

func (provider *HuggingFaceProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

func (provider *HuggingFaceProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	// Check if Transcription is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.TranscriptionRequest); err != nil {
		return nil, err
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
	}
	providerMapping, err := provider.getModelInferenceProviderMapping(ctx, modelName)
	if err != nil {
		return nil, err
	}

	if providerMapping == nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
	}

	mapping, ok := providerMapping[inferenceProvider]
	if !ok || mapping.ProviderModelID == "" || mapping.ProviderTask != "automatic-speech-recognition" {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
	}

	// Use the provider-specific model id
	modelName = mapping.ProviderModelID

	// Prepare request body using Transcription-specific function
	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToHuggingFaceTranscriptionRequest(request) },
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}
	url, urlErr := provider.getInferenceProviderRouteURL(ctx, inferenceProvider, modelName, schemas.TranscriptionRequest)
	if urlErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
	}
	responseBody, latency, err := provider.completeRequest(ctx, jsonData, url, key.Value)
	if err != nil {
		return nil, err
	}

	response := acquireHuggingFaceTranscriptionResponse()
	defer releaseHuggingFaceTranscriptionResponse(response)

	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse, convErr := response.ToBifrostTranscriptionResponse(request.Model)
	if convErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, convErr, provider.GetProviderKey())
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.TranscriptionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil

}

// TranscriptionStream is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}
