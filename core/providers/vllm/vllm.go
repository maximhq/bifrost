// Package vllm implements the vLLM LLM provider (OpenAI-compatible).
package vllm

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// VLLMProvider implements the Provider interface for vLLM's OpenAI-compatible API.
type VLLMProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewVLLMProvider creates a new vLLM provider instance.
func NewVLLMProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*VLLMProvider, error) {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:         time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:        time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     5000,
		MaxIdleConnDuration: 60 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client)
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	// BaseURL is optional when keys have vllm_key_config with per-key URLs
	return &VLLMProvider{
		logger:              logger,
		client:              client,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for vLLM.
func (provider *VLLMProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.VLLM
}

// getBaseURL resolves the base URL for a request from the per-key vllm_key_config.
// Each vLLM key must have its own URL configured — there is no provider-level fallback.
func (provider *VLLMProvider) getBaseURL(key schemas.Key) string {
	if key.VLLMKeyConfig != nil && key.VLLMKeyConfig.URL.GetValue() != "" {
		return strings.TrimRight(key.VLLMKeyConfig.URL.GetValue(), "/")
	}
	return ""
}

// baseURLOrError returns the resolved base URL or a BifrostError when none is configured.
func (provider *VLLMProvider) baseURLOrError(key schemas.Key) (string, *schemas.BifrostError) {
	u := provider.getBaseURL(key)
	if u == "" {
		return "", providerUtils.NewBifrostOperationError(
			"no base URL configured: set vllm_key_config.url on the key",
			nil,
			provider.GetProviderKey(),
		)
	}
	return u, nil
}

// listModelsByKey performs a list models request for a single vLLM key,
// resolving the per-key URL so each backend is queried individually.
func (provider *VLLMProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	url := baseURL + providerUtils.GetPathFromContext(ctx, "/v1/models")
	return openai.ListModelsByKey(
		ctx,
		provider.client,
		url,
		key,
		request.Unfiltered,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
	)
}

// ListModels performs a list models request to vLLM's API.
// Requests are made concurrently per key so that each backend is queried
// with its own URL (from vllm_key_config).
func (provider *VLLMProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}

// TextCompletion performs a text completion request to vLLM's API.
func (provider *VLLMProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAITextCompletionRequest(
		ctx,
		provider.client,
		baseURL+providerUtils.GetPathFromContext(ctx, "/v1/completions"),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		HandleVLLMResponse,
		nil,
		provider.logger,
	)
}

// TextCompletionStream performs a streaming text completion request to vLLM's API.
func (provider *VLLMProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAITextCompletionStreaming(
		ctx,
		provider.client,
		baseURL+providerUtils.GetPathFromContext(ctx, "/v1/completions"),
		request,
		nil,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		postHookRunner,
		HandleVLLMResponse,
		nil,
		provider.logger,
	)
}

// ChatCompletion performs a chat completion request to vLLM's API.
func (provider *VLLMProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		baseURL+providerUtils.GetPathFromContext(ctx, "/v1/chat/completions"),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		HandleVLLMResponse,
		nil,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to vLLM's API.
func (provider *VLLMProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.client,
		baseURL+providerUtils.GetPathFromContext(ctx, "/v1/chat/completions"),
		request,
		nil,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		HandleVLLMResponse,
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// Embedding performs an embedding request to vLLM's API.
func (provider *VLLMProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		baseURL+providerUtils.GetPathFromContext(ctx, "/v1/embeddings"),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		HandleVLLMResponse,
		provider.logger,
	)
}

// Responses performs a responses request to vLLM's API (via chat completion).
func (provider *VLLMProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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

// ResponsesStream performs a streaming responses request to vLLM's API (via chat completion stream).
func (provider *VLLMProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return provider.ChatCompletionStream(
		ctx,
		postHookRunner,
		key,
		request.ToChatRequest(),
	)
}

// Speech is not supported by the vLLM provider.
func (provider *VLLMProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

func isRerankFallbackStatus(statusCode int) bool {
	// vLLM deployments may return 501 for unimplemented routes.
	// We fallback on 501 in addition to 404/405 for compatibility.
	return statusCode == fasthttp.StatusNotFound ||
		statusCode == fasthttp.StatusMethodNotAllowed ||
		statusCode == fasthttp.StatusNotImplemented
}

func (provider *VLLMProvider) callVLLMRerankEndpoint(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostRerankRequest,
	endpointPath string,
	jsonData []byte,
) (map[string]interface{}, interface{}, interface{}, []byte, int, time.Duration, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, nil, nil, nil, 0, 0, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(baseURL + endpointPath)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	req.SetBody(jsonData)

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, nil, nil, nil, 0, latency, bifrostErr
	}

	statusCode := resp.StatusCode()
	if statusCode != fasthttp.StatusOK {
		return nil, nil, nil, nil, statusCode, latency, openai.ParseOpenAIError(resp, schemas.RerankRequest, provider.GetProviderKey(), request.Model)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, nil, nil, nil, statusCode, latency, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, provider.GetProviderKey())
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	responsePayload := make(map[string]interface{})
	rawRequest, rawResponse, bifrostErr := HandleVLLMResponse(body, &responsePayload, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, nil, nil, body, statusCode, latency, bifrostErr
	}

	return responsePayload, rawRequest, rawResponse, body, statusCode, latency, nil
}

// Rerank performs a rerank request to vLLM's API.
func (provider *VLLMProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToVLLMRerankRequest(request), nil
		},
		providerName,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	resolvedPath := providerUtils.GetPathFromContext(ctx, "")
	hasPathOverride := resolvedPath != ""
	if !hasPathOverride {
		resolvedPath = "/v1/rerank"
	} else if !strings.HasPrefix(resolvedPath, "/") {
		resolvedPath = "/" + resolvedPath
	}

	responsePayload, rawRequest, rawResponse, responseBody, statusCode, latency, bifrostErr := provider.callVLLMRerankEndpoint(ctx, key, request, resolvedPath, jsonData)
	if bifrostErr != nil && !hasPathOverride && isRerankFallbackStatus(statusCode) {
		var fallbackLatency time.Duration
		responsePayload, rawRequest, rawResponse, responseBody, statusCode, fallbackLatency, bifrostErr = provider.callVLLMRerankEndpoint(ctx, key, request, "/rerank", jsonData)
		latency += fallbackLatency
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	returnDocuments := request.Params != nil && request.Params.ReturnDocuments != nil && *request.Params.ReturnDocuments
	bifrostResponse, err := ToBifrostRerankResponse(responsePayload, request.Documents, returnDocuments)
	if err != nil {
		return nil, providerUtils.EnrichError(
			ctx,
			providerUtils.NewBifrostOperationError("error converting rerank response", err, providerName),
			jsonData,
			responseBody,
			provider.sendBackRawRequest,
			provider.sendBackRawResponse,
		)
	}

	// Keep requested model as the canonical model in Bifrost response.
	bifrostResponse.Model = request.Model
	bifrostResponse.ExtraFields.Provider = providerName
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.RequestType = schemas.RerankRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// SpeechStream is not supported by the vLLM provider.
func (provider *VLLMProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription performs a transcription request to vLLM's API.
func (provider *VLLMProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAITranscriptionRequest(
		ctx,
		provider.client,
		baseURL+providerUtils.GetPathFromContext(ctx, "/v1/audio/transcriptions"),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		HandleVLLMResponse,
		provider.logger,
	)
}

// TranscriptionStream performs a streaming transcription request to vLLM's API.
func (provider *VLLMProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	{
		logger := provider.logger
		providerName := provider.GetProviderKey()
		// Use centralized converter
		reqBody := openai.ToOpenAITranscriptionRequest(request)
		if reqBody == nil {
			return nil, providerUtils.NewBifrostOperationError("transcription input is not provided", nil, providerName)
		}
		reqBody.Stream = schemas.Ptr(true)

		// Create multipart form
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)

		if bifrostErr := openai.ParseTranscriptionFormDataBodyFromRequest(writer, reqBody, providerName); bifrostErr != nil {
			return nil, bifrostErr
		}

		// Prepare OpenAI headers
		headers := map[string]string{
			"Content-Type":  writer.FormDataContentType(),
			"Accept":        "text/event-stream",
			"Cache-Control": "no-cache",
		}

		// Create HTTP request for streaming
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		resp.StreamBody = true
		defer fasthttp.ReleaseRequest(req)

		// Set any extra headers from network config
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

		req.Header.SetMethod(http.MethodPost)
		req.SetRequestURI(baseURL + providerUtils.GetPathFromContext(ctx, "/v1/audio/transcriptions"))

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
			return nil, openai.ParseOpenAIError(resp, schemas.TranscriptionStreamRequest, providerName, request.Model)
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
			// Decompress gzip-encoded streams transparently (no-op for non-gzip)
			reader, releaseGzip := providerUtils.DecompressStreamBody(resp)
			defer releaseGzip()

			// Setup cancellation handler to close the raw network stream on ctx cancellation,
			// which immediately unblocks any in-progress read (including reads blocked inside a gzip decompression layer).
			stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
			defer stopCancellation()

			scanner := bufio.NewScanner(reader)
			chunkIndex := -1

			startTime := time.Now()
			lastChunkTime := startTime
			var fullTranscriptionText strings.Builder

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

				var response schemas.BifrostTranscriptionStreamResponse
				var bifrostErr *schemas.BifrostError

				_, _, bifrostErr = HandleVLLMResponse([]byte(jsonData), &response, nil, false, false)
				if bifrostErr != nil {
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						Provider:       providerName,
						ModelRequested: request.Model,
						RequestType:    schemas.TranscriptionStreamRequest,
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, body.Bytes(), []byte(jsonData), false, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)), responseChan, logger)
					return
				}

				customChunk, ok := parseVLLMTranscriptionStreamChunk([]byte(jsonData))
				if !ok || customChunk == nil {
					logger.Warn("customChunkParser returned no chunk")
					continue
				}
				response = *customChunk

				chunkIndex++
				if response.Delta != nil {
					fullTranscriptionText.WriteString(*response.Delta)
				}

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
				if response.Usage != nil || response.Type == schemas.TranscriptionStreamResponseTypeDone {
					response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
					response.Text = fullTranscriptionText.String()
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
}

// ImageGeneration is not supported by the vLLM provider.
func (provider *VLLMProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the vLLM provider.
func (provider *VLLMProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit is not supported by the vLLM provider.
func (provider *VLLMProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the vLLM provider.
func (provider *VLLMProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the vLLM provider.
func (provider *VLLMProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoGeneration is not supported by the vLLM provider.
func (provider *VLLMProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, provider.GetProviderKey())
}

// VideoRetrieve is not supported by the vLLM provider.
func (provider *VLLMProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, provider.GetProviderKey())
}

// VideoDownload is not supported by the vLLM provider.
func (provider *VLLMProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}

// VideoDelete is not supported by the vLLM provider.
func (provider *VLLMProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by the vLLM provider.
func (provider *VLLMProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by the vLLM provider.
func (provider *VLLMProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// FileUpload is not supported by the vLLM provider.
func (provider *VLLMProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by the vLLM provider.
func (provider *VLLMProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by the vLLM provider.
func (provider *VLLMProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by the vLLM provider.
func (provider *VLLMProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by the vLLM provider.
func (provider *VLLMProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by the vLLM provider.
func (provider *VLLMProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by the vLLM provider.
func (provider *VLLMProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by the vLLM provider.
func (provider *VLLMProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by the vLLM provider.
func (provider *VLLMProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchDelete is not supported by the vLLM provider.
func (provider *VLLMProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults is not supported by the vLLM provider.
func (provider *VLLMProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the vLLM provider.
func (provider *VLLMProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the vLLM provider.
func (provider *VLLMProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the vLLM provider.
func (provider *VLLMProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the vLLM provider.
func (provider *VLLMProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the vLLM provider.
func (provider *VLLMProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the vLLM provider.
func (provider *VLLMProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the vLLM provider.
func (provider *VLLMProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the vLLM provider.
func (provider *VLLMProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the vLLM provider.
func (provider *VLLMProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the vLLM provider.
func (provider *VLLMProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the vLLM provider.
func (provider *VLLMProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.PassthroughRequest) (*fasthttp.Response, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughOperationRequest, provider.GetProviderKey())
}
