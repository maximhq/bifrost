package huggingface

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

// HuggingFaceProvider implements the Provider interface for Hugging Face's inference APIs.
type HuggingFaceProvider struct {
	logger                    schemas.Logger
	client                    *fasthttp.Client
	networkConfig             schemas.NetworkConfig
	sendBackRawResponse       bool
	sendBackRawRequest        bool
	customProviderConfig      *schemas.CustomProviderConfig
	modelProviderMappingCache *sync.Map
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
		logger:                    logger,
		client:                    client,
		networkConfig:             config.NetworkConfig,
		sendBackRawResponse:       config.SendBackRawResponse,
		sendBackRawRequest:        config.SendBackRawRequest,
		customProviderConfig:      config.CustomProviderConfig,
		modelProviderMappingCache: &sync.Map{},
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

// completeRequestWithRetry performs a request and retries once on 404 by clearing the cache and refetching model info
func (provider *HuggingFaceProvider) completeRequestWithRetry(
	ctx context.Context,
	jsonData []byte,
	key string,
	isHFInferenceAudioRequest bool,
	inferenceProvider inferenceProvider,
	originalModelName string,
	requiredTask string,
	requestType schemas.RequestType,
) ([]byte, time.Duration, *schemas.BifrostError) {

	// Build URL with original model name
	url, urlErr := provider.getInferenceProviderRouteURL(ctx, inferenceProvider, originalModelName, requestType)
	if urlErr != nil {
		return nil, 0, providerUtils.NewUnsupportedOperationError(requestType, provider.GetProviderKey())
	}

	modelName, err := provider.getValidatedProviderModelID(ctx, inferenceProvider, originalModelName, requiredTask, requestType)
	if err != nil {
		return nil, 0, err
	}

	// Update the model field in the JSON body if it's not an audio request
	updatedJSONData := jsonData
	if !isHFInferenceAudioRequest && requestType == schemas.EmbeddingRequest {
		// Parse, update model field, and re-encode for embedding requests
		var reqBody map[string]interface{}
		if err := sonic.Unmarshal(jsonData, &reqBody); err == nil {
			reqBody["model"] = modelName
			if newJSON, err := sonic.Marshal(reqBody); err == nil {
				updatedJSONData = newJSON
			}
		}
	}

	// Make the request
	responseBody, latency, err := provider.completeRequest(ctx, updatedJSONData, url, key, isHFInferenceAudioRequest)
	if err != nil {
		// If we got a 404, clear cache and retry once
		if err.StatusCode != nil && *err.StatusCode == 404 {
			provider.modelProviderMappingCache.Delete(originalModelName)

			// Retry: re-fetch the validated model ID
			modelName, retryErr := provider.getValidatedProviderModelID(ctx, inferenceProvider, originalModelName, requiredTask, requestType)
			if retryErr != nil {
				return nil, 0, retryErr
			}

			// Update the model field in the JSON body for retry
			if !isHFInferenceAudioRequest && requestType == schemas.EmbeddingRequest {
				var reqBody map[string]interface{}
				if err := sonic.Unmarshal(jsonData, &reqBody); err == nil {
					reqBody["model"] = modelName
					if newJSON, err := sonic.Marshal(reqBody); err == nil {
						updatedJSONData = newJSON
					}
				}
			}

			// Rebuild URL with new model name
			url, urlErr = provider.getInferenceProviderRouteURL(ctx, inferenceProvider, modelName, requestType)
			if urlErr != nil {
				return nil, 0, providerUtils.NewUnsupportedOperationError(requestType, provider.GetProviderKey())
			}

			// Retry the request
			responseBody, latency, err = provider.completeRequest(ctx, updatedJSONData, url, key, isHFInferenceAudioRequest)
			if err != nil {
				return nil, 0, err
			}
		} else {
			return nil, 0, err
		}
	}

	return responseBody, latency, nil
}

func (provider *HuggingFaceProvider) completeRequest(ctx context.Context, jsonData []byte, url string, key string, isHFInferenceAudioRequest bool) ([]byte, time.Duration, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)

	if isHFInferenceAudioRequest {
		audioType := providerUtils.DetectAudioMimeType(jsonData)
		mimeType := getMimeTypeForAudioType(audioType)
		req.Header.Set("Content-Type", mimeType)
	} else {
		req.Header.SetContentType("application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	req.SetBody(jsonData)

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] error response headers: %s", resp.Header.String()))
		provider.logger.Debug(fmt.Sprintf("[huggingface debug] error response body: %s", string(resp.Body())))

		return nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {

		var errorResp HuggingFaceResponseError

		bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
		if strings.TrimSpace(errorResp.Type) != "" {
			typeCopy := errorResp.Type
			bifrostErr.Type = &typeCopy
		}
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		if strings.TrimSpace(errorResp.Message) != "" {
			bifrostErr.Error.Message = errorResp.Message
		}

		return nil, latency, bifrostErr
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	// Read the response body and copy it before releasing the response
	// to avoid use-after-free since resp.Body() references fasthttp's internal buffer
	bodyCopy := append([]byte(nil), body...)

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

			latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
			if bifrostErr != nil {
				resultsChan <- providerResult{provider: inferProvider, err: bifrostErr}
				return
			}

			if resp.StatusCode() != fasthttp.StatusOK {
				var errorResp HuggingFaceHubError
				bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
				if bifrostErr.Error == nil {
					bifrostErr.Error = &schemas.ErrorField{}
				}
				if strings.TrimSpace(errorResp.Message) != "" {
					bifrostErr.Error.Message = errorResp.Message
				}
				resultsChan <- providerResult{provider: inferProvider, err: bifrostErr}
				return
			}

			body, err := providerUtils.CheckAndDecodeBody(resp)
			if err != nil {
				resultsChan <- providerResult{provider: inferProvider, err: providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)}
				return
			}

			var huggingfaceAPIResponse HuggingFaceListModelsResponse
			var rawResponse interface{}
			var rawRequest interface{}
			rawRequest, rawResponse, bifrostErr = providerUtils.HandleProviderResponse(body, &huggingfaceAPIResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
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
			// If raw request was requested, attach it to the raw response map
			if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) && rawRequest != nil {
				if rawRespMap == nil {
					rawRespMap = make(map[string]interface{})
				}
				rawRespMap["raw_request"] = rawRequest
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
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

func (provider *HuggingFaceProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

func (provider *HuggingFaceProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: nameErr.Error(),
				Error:   nameErr,
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				Provider:    provider.GetProviderKey(),
				RequestType: schemas.ChatCompletionRequest,
			},
		}
	}
	request.Model = fmt.Sprintf("%s:%s", modelName, inferenceProvider)

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

	requestURL := provider.buildRequestURL(ctx, "/v1/chat/completions", schemas.ChatCompletionRequest)

	responseBody, latency, err := provider.completeRequest(ctx, jsonBody, requestURL, key.Value, false)
	if err != nil {
		return nil, err
	}

	response := acquireHuggingFaceChatResponse()
	defer releaseHuggingFaceChatResponse(response)

	var rawResponse interface{}
	var rawRequest interface{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
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

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}

	return bifrostResponse, nil
}

func (provider *HuggingFaceProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionStreamRequest, provider.GetProviderKey())
	}
	request.Model = fmt.Sprintf("%s:%s", modelName, inferenceProvider)

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
			var errorResp HuggingFaceResponseError
			if err := sonic.Unmarshal([]byte(jsonData), &errorResp); err == nil {
				if errorResp.Message != "" {
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
				provider.logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			// Convert to Bifrost response
			response := streamResp.ToBifrostChatStreamResponse()
			if response == nil {
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

			bifrostStream := providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, bifrostStream, responseChan)

		}

		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ChatCompletionStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

func (provider *HuggingFaceProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: nameErr.Error(),
				Error:   nameErr,
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				Provider:    provider.GetProviderKey(),
				RequestType: schemas.ResponsesRequest,
			},
		}
	}
	request.Model = fmt.Sprintf("%s:%s", modelName, inferenceProvider)

	jsonBody, err := getRequestBodyForResponses(ctx, request, provider.GetProviderKey(), false)
	if err != nil {
		return nil, err
	}

	requestURL := provider.buildRequestURL(ctx, "/v1/chat/completions", schemas.ResponsesRequest)

	responseBody, latency, err := provider.completeRequest(ctx, jsonBody, requestURL, key.Value, false)
	if err != nil {
		return nil, err
	}

	response := acquireHuggingFaceChatResponse()
	defer releaseHuggingFaceChatResponse(response)

	var rawResponse interface{}
	var rawRequest interface{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResponse, convErr := ToBifrostResponsesResponseFromHuggingFace(response, request.Model)
	if convErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, convErr, provider.GetProviderKey())
	}

	if bifrostResponse == nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, fmt.Errorf("empty HuggingFace response"), provider.GetProviderKey())
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ResponsesRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}

	return bifrostResponse, nil
}

func (provider *HuggingFaceProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesStreamRequest, provider.GetProviderKey())
	}
	request.Model = fmt.Sprintf("%s:%s", modelName, inferenceProvider)

	jsonBody, err := getRequestBodyForResponses(ctx, request, provider.GetProviderKey(), true)
	if err != nil {
		return nil, err
	}

	// Prepare headers to pass to the shared handler
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
		"Cache-Control": "no-cache",
	}
	if key.Value != "" {
		headers["Authorization"] = "Bearer " + key.Value
	}

	// Call the extracted streaming handler
	return HandleHuggingFaceResponsesStream(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/chat/completions", schemas.ResponsesStreamRequest),
		jsonBody,
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

// HandleHuggingFaceResponsesStream contains the streaming logic for ResponsesStream requests.
func HandleHuggingFaceResponsesStream(
	ctx context.Context,
	client *fasthttp.Client,
	url string,
	jsonBody []byte,
	headers map[string]string,
	extraHeaders map[string]string,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	modelRequested string,
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

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	req.SetBody(jsonBody)

	// Make the request
	apiErr := client.Do(req, resp)
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

		responsesStreamState := schemas.AcquireChatToResponsesStreamState()
		defer schemas.ReleaseChatToResponsesStreamState(responsesStreamState)

		scanner := bufio.NewScanner(resp.BodyStream())
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		chunkIndex := 0
		startTime := time.Now()
		lastChunkTime := startTime

		for scanner.Scan() {
			// Check if context is done before processing
			if ctx.Err() != nil {
				providerUtils.ProcessAndSendError(ctx, postHookRunner, ctx.Err(), responseChan, schemas.ResponsesStreamRequest, providerName, modelRequested, logger)
				return
			}

			line := scanner.Text()
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
			var errorResp HuggingFaceResponseError
			if err := sonic.Unmarshal([]byte(jsonData), &errorResp); err == nil {
				if errorResp.Message != "" {
					bifrostErr := &schemas.BifrostError{
						Type:           &errorResp.Type,
						IsBifrostError: false,
						Error: &schemas.ErrorField{
							Message: errorResp.Message,
						},
						ExtraFields: schemas.BifrostErrorExtraFields{
							Provider:       providerName,
							ModelRequested: modelRequested,
							RequestType:    schemas.ResponsesStreamRequest,
						},
					}
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
					return
				}
			}

			// Parse into HuggingFace stream response
			var streamResp HuggingFaceChatStreamResponse
			if err := sonic.Unmarshal([]byte(jsonData), &streamResp); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse stream response: %v", err))
				continue
			}

			// Convert to Bifrost Responses stream responses
			responsesResponses := streamResp.ToBifrostResponsesStreamResponses(responsesStreamState)
			if len(responsesResponses) == 0 {
				continue
			}

			for _, responsesResp := range responsesResponses {
				if responsesResp == nil {
					continue
				}

				// Set extra fields
				if responsesResp.ExtraFields.RequestType == "" {
					responsesResp.ExtraFields.RequestType = schemas.ResponsesStreamRequest
				}
				responsesResp.ExtraFields.Provider = providerName
				responsesResp.ExtraFields.ModelRequested = modelRequested
				responsesResp.ExtraFields.ChunkIndex = chunkIndex
				responsesResp.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()

				if providerUtils.ShouldSendBackRawResponse(ctx, sendBackRawResponse) {
					responsesResp.ExtraFields.RawResponse = jsonData
				}

				// If this chunk has usage (end), update latency to total and mark stream end
				if streamResp.Usage != nil {
					responsesResp.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
				}

				if postResponseConverter != nil {
					responsesResp = postResponseConverter(responsesResp)
					if responsesResp == nil {
						logger.Warn("postResponseConverter returned nil; skipping chunk")
						continue
					}
				}

				bifrostStream := providerUtils.GetBifrostResponseForStreamResponse(nil, nil, responsesResp, nil, nil)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, bifrostStream, responseChan)
			}

			lastChunkTime = time.Now()
			chunkIndex++
		}

		if err := scanner.Err(); err != nil {
			logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ResponsesStreamRequest, providerName, modelRequested, logger)
		}
	}()

	return responseChan, nil
}

func (provider *HuggingFaceProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
	}

	jsonBody, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			req, err := ToHuggingFaceEmbeddingRequest(request)
			return req, err
		},
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	responseBody, latency, err := provider.completeRequestWithRetry(
		ctx,
		jsonBody,
		key.Value,
		false,
		inferenceProvider,
		modelName,
		"feature-extraction",
		schemas.EmbeddingRequest,
	)
	if err != nil {
		return nil, err
	}

	var huggingfaceResponse HuggingFaceEmbeddingResponse

	var rawResponse interface{}
	var rawRequest interface{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &huggingfaceResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
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

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}

	return bifrostResponse, nil
}

func (provider *HuggingFaceProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	// Check if Speech is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
		return nil, err
	}

	inferenceProvider, modelName, nameErr := splitIntoModelProvider(request.Model)
	if nameErr != nil {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
	}

	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToHuggingFaceSpeechRequest(request) },
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	responseBody, latency, err := provider.completeRequestWithRetry(
		ctx,
		jsonData,
		key.Value,
		false,
		inferenceProvider,
		modelName,
		"text-to-speech",
		schemas.SpeechRequest,
	)
	if err != nil {
		return nil, err
	}

	response := acquireHuggingFaceSpeechResponse()
	defer releaseHuggingFaceSpeechResponse(response)

	var rawResponse interface{}
	var rawRequest interface{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Download the audio file from the URL
	audioData, downloadErr := provider.downloadAudioFromURL(response.Audio.URL)
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

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
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

	var jsonData []byte
	var err *schemas.BifrostError
	// hf-inference expects raw audio bytes with an audio content type instead of JSON
	isHFInferenceAudioRequest := inferenceProvider == hfInference
	if inferenceProvider == hfInference {
		jsonData = request.Input.File
	} else {
		// Prepare request body using Transcription-specific function
		jsonData, err = providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (any, error) { return ToHuggingFaceTranscriptionRequest(request) },
			provider.GetProviderKey())
		if err != nil {
			return nil, err
		}
	}

	responseBody, latency, err := provider.completeRequestWithRetry(
		ctx,
		jsonData,
		key.Value,
		isHFInferenceAudioRequest,
		inferenceProvider,
		modelName,
		"automatic-speech-recognition",
		schemas.TranscriptionRequest,
	)
	if err != nil {
		return nil, err
	}

	response := acquireHuggingFaceTranscriptionResponse()
	defer releaseHuggingFaceTranscriptionResponse(response)

	var rawResponse interface{}
	var rawRequest interface{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
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

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}

	return bifrostResponse, nil

}

// TranscriptionStream is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}
