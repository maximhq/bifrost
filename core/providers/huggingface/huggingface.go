package huggingface

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
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
func (provider *HuggingFaceProvider) buildRequestURL(ctx *schemas.BifrostContext, defaultPath string, requestType schemas.RequestType) string {
	return provider.networkConfig.BaseURL + providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
}

// completeRequestWithModelAliasCache performs a request and retries once on 404 by clearing the cache and refetching model info
func (provider *HuggingFaceProvider) completeRequestWithModelAliasCache(
	ctx *schemas.BifrostContext,
	jsonData []byte,
	key string,
	isHFInferenceAudioRequest bool,
	isHFInferenceImageRequest bool,
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

	// For fal-ai, nebius, and together image generation, skip validation (model format is already correct)
	skipValidation := (inferenceProvider == falAI || inferenceProvider == nebius || inferenceProvider == together) && requestType == schemas.ImageGenerationRequest
	var modelName string
	var err *schemas.BifrostError
	if skipValidation {
		// Use original model name for validation skip case (though we won't use it for these providers)
		modelName = originalModelName
	} else {
		modelName, err = provider.getValidatedProviderModelID(ctx, inferenceProvider, originalModelName, requiredTask, requestType)
		if err != nil {
			return nil, 0, err
		}
	}

	// Update the model field in the JSON body if it's not an audio request
	updatedJSONData := jsonData
	// Skip body modification for fal-ai, nebius, and together image generation - they have special requirements
	skipBodyModification := (inferenceProvider == falAI || inferenceProvider == nebius || inferenceProvider == together) && requestType == schemas.ImageGenerationRequest
	if !isHFInferenceAudioRequest && !skipBodyModification && (requestType == schemas.EmbeddingRequest || requestType == schemas.ImageGenerationRequest) {
		// Parse, update model field, and re-encode for embedding and image generation requests
		// NOTE: For fal-ai image generation, model is in URL path, not in body
		// For nebius and together image generation, use original model name (already set in ToHuggingFaceImageGenerationRequest)
		var reqBody map[string]interface{}
		if err := sonic.Unmarshal(jsonData, &reqBody); err == nil {
			// For other providers (embeddings, hf-inference images), use validated model ID
			reqBody["model"] = modelName
			if newJSON, err := sonic.Marshal(reqBody); err == nil {
				updatedJSONData = newJSON
			}
		}
	}

	// Make the request
	responseBody, latency, err := provider.completeRequest(ctx, updatedJSONData, url, key, isHFInferenceAudioRequest, isHFInferenceImageRequest)
	if err != nil {
		// If we got a 404, clear cache and retry once
		if err.StatusCode != nil && *err.StatusCode == 404 {
			provider.modelProviderMappingCache.Delete(originalModelName)

			// Retry: re-fetch the validated model ID (skip validation for fal-ai, nebius, and together image generation)
			if skipValidation {
				// Keep original model name for validation skip case
				modelName = originalModelName
			} else {
				var retryErr *schemas.BifrostError
				modelName, retryErr = provider.getValidatedProviderModelID(ctx, inferenceProvider, originalModelName, requiredTask, requestType)
				if retryErr != nil {
					return nil, 0, retryErr
				}
			}

			// Update the model field in the JSON body for retry
			// Skip body modification for fal-ai, nebius, and together image generation - they have special requirements
			if !isHFInferenceAudioRequest && !skipBodyModification && (requestType == schemas.EmbeddingRequest || requestType == schemas.ImageGenerationRequest) {
				var reqBody map[string]interface{}
				if err := sonic.Unmarshal(jsonData, &reqBody); err == nil {
					// For other providers (embeddings, hf-inference images), use validated model ID
					reqBody["model"] = modelName
					if newJSON, err := sonic.Marshal(reqBody); err == nil {
						updatedJSONData = newJSON
					}
				}
			}

			// Rebuild URL with new model name (use original for fal-ai, nebius, and together since validation is skipped)
			retryModelName := originalModelName
			if !skipValidation {
				retryModelName = modelName
			}
			url, urlErr = provider.getInferenceProviderRouteURL(ctx, inferenceProvider, retryModelName, requestType)
			if urlErr != nil {
				return nil, 0, providerUtils.NewUnsupportedOperationError(requestType, provider.GetProviderKey())
			}

			// Retry the request
			responseBody, latency, err = provider.completeRequest(ctx, updatedJSONData, url, key, isHFInferenceAudioRequest, isHFInferenceImageRequest)
			if err != nil {
				return nil, 0, err
			}
		} else {
			return nil, 0, err
		}
	}

	return responseBody, latency, nil
}

func (provider *HuggingFaceProvider) completeRequest(ctx *schemas.BifrostContext, jsonData []byte, url string, key string, isHFInferenceAudioRequest bool, isHFInferenceImageRequest bool) ([]byte, time.Duration, *schemas.BifrostError) {
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
		return nil, latency, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, latency, parseHuggingFaceImageError(resp, nil)
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

func (provider *HuggingFaceProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
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
			if key.Value.GetValue() != "" {
				req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value.GetValue()))
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
func (provider *HuggingFaceProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {

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

func (provider *HuggingFaceProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

func (provider *HuggingFaceProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

func (provider *HuggingFaceProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
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
			reqBody, err := ToHuggingFaceChatCompletionRequest(request)
			if err != nil {
				return nil, err
			}
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

	responseBody, latency, err := provider.completeRequest(ctx, jsonBody, requestURL, key.Value.GetValue(), false, false)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	bifrostResponse := &schemas.BifrostChatResponse{}

	var rawResponse interface{}
	var rawRequest interface{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, bifrostResponse, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Ensure model is set correctly
	if bifrostResponse.Model == "" {
		bifrostResponse.Model = request.Model
	}

	// Set object if not already set
	if bifrostResponse.Object == "" {
		bifrostResponse.Object = "chat.completion"
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

func (provider *HuggingFaceProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
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
				RequestType: schemas.ChatCompletionStreamRequest,
			},
		}
	}
	request.Model = fmt.Sprintf("%s:%s", modelName, inferenceProvider)

	var authHeader map[string]string
	if key.Value.GetValue() != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
	}

	customRequestConverter := func(request *schemas.BifrostChatRequest) (any, error) {
		reqBody, err := ToHuggingFaceChatCompletionRequest(request)
		if err != nil {
			return nil, err
		}
		if reqBody != nil {
			reqBody.Stream = schemas.Ptr(true)
		}
		return reqBody, nil
	}

	// Use shared OpenAI-compatible streaming logic
	return openai.HandleOpenAIChatCompletionStreaming(
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
		customRequestConverter,
		nil,
		nil,
		nil,
		provider.logger,
	)
}

func (provider *HuggingFaceProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

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

func (provider *HuggingFaceProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return provider.ChatCompletionStream(
		ctx,
		postHookRunner,
		key,
		request.ToChatRequest(),
	)
}

func (provider *HuggingFaceProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
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
				RequestType: schemas.EmbeddingRequest,
			},
		}
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

	responseBody, latency, err := provider.completeRequestWithModelAliasCache(
		ctx,
		jsonBody,
		key.Value.GetValue(),
		false,
		false,
		inferenceProvider,
		modelName,
		"feature-extraction",
		schemas.EmbeddingRequest,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Handle raw request/response for tracking
	var rawResponse interface{}
	var rawRequest interface{}
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		if err := sonic.Unmarshal(jsonBody, &rawRequest); err != nil {
			rawRequest = string(jsonBody)
		}
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
			rawResponse = string(responseBody)
		}
	}

	// Unmarshal directly to BifrostEmbeddingResponse with custom logic
	bifrostResponse, convErr := UnmarshalHuggingFaceEmbeddingResponse(responseBody, request.Model)
	if convErr != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, convErr, provider.GetProviderKey()), jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
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

func (provider *HuggingFaceProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	// Check if Speech is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
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
				RequestType: schemas.SpeechRequest,
			},
		}
	}

	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToHuggingFaceSpeechRequest(request) },
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	responseBody, latency, err := provider.completeRequestWithModelAliasCache(
		ctx,
		jsonData,
		key.Value.GetValue(),
		false,
		false,
		inferenceProvider,
		modelName,
		"text-to-speech",
		schemas.SpeechRequest,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	response := acquireHuggingFaceSpeechResponse()
	defer releaseHuggingFaceSpeechResponse(response)

	var rawResponse interface{}
	var rawRequest interface{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
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

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}

	return bifrostResponse, nil
}

func (provider *HuggingFaceProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

func (provider *HuggingFaceProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	// Check if Transcription is allowed for this provider
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.TranscriptionRequest); err != nil {
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
				RequestType: schemas.TranscriptionRequest,
			},
		}
	}

	var jsonData []byte
	var err *schemas.BifrostError
	// hf-inference expects raw audio bytes with an audio content type instead of JSON
	isHFInferenceAudioRequest := inferenceProvider == hfInference
	if inferenceProvider == hfInference {
		if request.Input == nil || len(request.Input.File) == 0 {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderCreateRequest, fmt.Errorf("input file data is required for hf-inference transcription requests"), provider.GetProviderKey())
		}
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

	responseBody, latency, err := provider.completeRequestWithModelAliasCache(
		ctx,
		jsonData,
		key.Value.GetValue(),
		isHFInferenceAudioRequest,
		false,
		inferenceProvider,
		modelName,
		"automatic-speech-recognition",
		schemas.TranscriptionRequest,
	)
	if err != nil {
		// Don't wrap raw audio bytes (when isHFInferenceAudioRequest is true)
		if !isHFInferenceAudioRequest {
			return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		return nil, err
	}

	response := acquireHuggingFaceTranscriptionResponse()
	defer releaseHuggingFaceTranscriptionResponse(response)

	var rawResponse interface{}
	var rawRequest interface{}
	// Only pass jsonData if it's not raw audio bytes
	var requestBodyForHandling []byte
	if !isHFInferenceAudioRequest {
		requestBodyForHandling = jsonData
	}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, requestBodyForHandling, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		if !isHFInferenceAudioRequest {
			return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
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
func (provider *HuggingFaceProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

func (provider *HuggingFaceProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ImageGenerationRequest); err != nil {
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
				RequestType: schemas.ImageGenerationRequest,
			},
		}
	}

	jsonBody, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) {
			req, err := ToHuggingFaceImageGenerationRequest(request)
			return req, err
		},
		provider.GetProviderKey())
	if err != nil {
		return nil, err
	}

	responseBody, latency, err := provider.completeRequestWithModelAliasCache(
		ctx,
		jsonBody,
		key.Value.GetValue(),
		false,
		true,
		inferenceProvider,
		modelName,
		"text-to-image",
		schemas.ImageGenerationRequest,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Handle raw request/response for tracking
	var rawResponse interface{}
	var rawRequest interface{}
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		if err := sonic.Unmarshal(jsonBody, &rawRequest); err != nil {
			rawRequest = string(jsonBody)
		}
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
			rawResponse = string(responseBody)
		}
	}

	// Unmarshal response using Nebius converter
	bifrostResponse, convErr := UnmarshalHuggingFaceImageGenerationResponse(responseBody, request.Model)
	if convErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, convErr, provider.GetProviderKey())
	}

	bifrostResponse.Created = time.Now().Unix()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.ImageGenerationRequest
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

// ImageGenerationStream handles streaming for fal-ai image generation.
// Only fal-ai inference provider supports streaming for HuggingFace.
func (provider *HuggingFaceProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.HuggingFace, provider.customProviderConfig, schemas.ImageGenerationStreamRequest); err != nil {
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
				RequestType: schemas.ImageGenerationStreamRequest,
			},
		}
	}

	// Only fal-ai supports streaming for HuggingFace
	if inferenceProvider != falAI {
		return nil, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("image generation streaming is only supported for fal-ai inference provider, got: %s", inferenceProvider),
			nil,
			provider.GetProviderKey(),
		)
	}

	var authHeader map[string]string

	if value := key.Value.GetValue(); value != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + value}
	}

	// Build streaming URL - append /stream to the fal-ai route, honoring path overrides
	defaultPath := fmt.Sprintf("/fal-ai/%s/stream", modelName)
	streamPath := providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, schemas.ImageGenerationStreamRequest)
	streamURL := provider.networkConfig.BaseURL + streamPath

	return HandleHuggingFaceImageGenerationStreaming(
		ctx,
		provider.client,
		streamURL,
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		provider.logger,
	)
}

// HandleHuggingFaceImageGenerationStreaming handles image generation streaming for fal-ai through HuggingFace router.
func HandleHuggingFaceImageGenerationStreaming(
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
	logger schemas.Logger,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {

	// Set headers
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
			return ToHuggingFaceImageStreamRequest(request)
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

	// Setup request
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
		return nil, providerUtils.EnrichError(ctx, parseHuggingFaceImageError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			Model:       request.Model,
			RequestType: schemas.ImageGenerationStreamRequest,
		}), jsonBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer providerUtils.ReleaseStreamingResponse(resp)
		defer close(responseChan)

		if resp.BodyStream() == nil {
			bifrostErr := providerUtils.NewBifrostOperationError(
				"Provider returned an empty response",
				fmt.Errorf("provider returned an empty response"),
				providerName,
			)
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
			return
		}

		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		scanner := bufio.NewScanner(resp.BodyStream())
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		lastChunkTime := startTime
		chunkIndex := 0
		var lastB64Data, lastURLData, lastJsonData string
		var lastIndex int

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

			// Skip event type lines
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

			// Try to parse as error first
			var errorResp HuggingFaceResponseError
			if err := sonic.Unmarshal([]byte(jsonData), &errorResp); err == nil {
				if errorResp.Error != "" || errorResp.Message != "" {
					bifrostErr := &schemas.BifrostError{
						IsBifrostError: false,
						Error: &schemas.ErrorField{
							Message: errorResp.Message,
						},
						ExtraFields: schemas.BifrostErrorExtraFields{
							Provider:       providerName,
							ModelRequested: request.Model,
							RequestType:    schemas.ImageGenerationStreamRequest,
						},
					}
					if errorResp.Error != "" {
						bifrostErr.Error.Message = errorResp.Error
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
					return
				}
			}

			// Parse fal-ai response
			var response HuggingFaceFalAIImageStreamResponse
			if err := sonic.Unmarshal([]byte(jsonData), &response); err != nil {
				logger.Warn(fmt.Sprintf("Failed to parse fal-ai stream response: %v", err))
				continue
			}
			// Process each image in the response
			for i, img := range response.Images {
				// Create a fresh chunk for each image to avoid data race
				chunk := &schemas.BifrostImageGenerationStreamResponse{
					Type: schemas.ImageGenerationEventTypePartial,
					ExtraFields: schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ImageGenerationStreamRequest,
						Provider:       providerName,
						ModelRequested: request.Model,
						ChunkIndex:     chunkIndex,
						Latency:        time.Since(lastChunkTime).Milliseconds(),
					},
				}

				if img.URL != "" {
					chunk.URL = img.URL
				} else if img.B64JSON != "" {
					chunk.B64JSON = img.B64JSON
				}
				chunk.Index = i

				if chunk.CreatedAt == 0 {
					chunk.CreatedAt = time.Now().Unix()
				}
				// Set raw response if enabled
				if sendBackRawResponse {
					chunk.ExtraFields.RawResponse = jsonData
				}

				lastChunkTime = time.Now()
				chunkIndex++

				// Track last chunk data for completion
				lastURLData = img.URL
				lastB64Data = img.B64JSON
				lastIndex = i
				lastJsonData = jsonData

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
					providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, chunk),
					responseChan)
			}
		}

		// Stream closed - send completion chunk
		if chunkIndex > 0 {
			finalChunk := &schemas.BifrostImageGenerationStreamResponse{
				Type:  schemas.ImageGenerationEventTypeCompleted,
				Index: lastIndex,
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:    schemas.ImageGenerationStreamRequest,
					Provider:       providerName,
					ModelRequested: request.Model,
					ChunkIndex:     chunkIndex,
					Latency:        time.Since(startTime).Milliseconds(),
				},
			}
			if lastURLData != "" {
				finalChunk.URL = lastURLData
			} else if lastB64Data != "" {
				finalChunk.B64JSON = lastB64Data
			}
			if finalChunk.CreatedAt == 0 {
				finalChunk.CreatedAt = time.Now().Unix()
			}
			if sendBackRawRequest {
				providerUtils.ParseAndSetRawRequest(&finalChunk.ExtraFields, jsonBody)
			}
			if sendBackRawResponse {
				finalChunk.ExtraFields.RawResponse = lastJsonData
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
				providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, finalChunk),
				responseChan)

		}

		if err := scanner.Err(); err != nil {
			bifrostErr := providerUtils.NewBifrostOperationError(
				fmt.Sprintf("Error reading fal-ai stream: %v", err),
				err,
				providerName,
			)
			bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
				Provider:       providerName,
				ModelRequested: request.Model,
				RequestType:    schemas.ImageGenerationStreamRequest,
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger)
		}
	}()

	return responseChan, nil
}

// BatchCreate is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchResults is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Hugging Face provider.
func (provider *HuggingFaceProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}
