// Package mistral implements the Mistral provider.
package mistral

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// MistralProvider implements the Provider interface for Mistral's API.
type MistralProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for API requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewMistralProvider creates a new Mistral provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewMistralProvider(config *schemas.ProviderConfig, logger schemas.Logger) *MistralProvider {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:         time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:        time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     5000,
		MaxIdleConnDuration: 60 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	// Pre-warm response pools
	// for range config.ConcurrencyAndBufferSize.Concurrency {
	// 	mistralResponsePool.Put(&schemas.BifrostResponse{})
	// }

	// Configure proxy if provided
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)

	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.mistral.ai"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &MistralProvider{
		logger:              logger,
		client:              client,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Mistral.
func (provider *MistralProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Mistral
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *MistralProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/v1/models"))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		bifrostErr := openai.ParseOpenAIError(resp, schemas.ListModelsRequest, providerName, "")
		return nil, bifrostErr
	}

	// Copy response body before releasing
	responseBody := append([]byte(nil), resp.Body()...)

	// Parse Mistral's response
	var mistralResponse MistralListModelsResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &mistralResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	response := mistralResponse.ToBifrostListModelsResponse(key.Models)

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

// ListModels performs a list models request to Mistral's API.
// Requests are made concurrently for improved performance.
func (provider *MistralProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
		provider.logger,
	)
}

// TextCompletion is not supported by the Mistral provider.
func (provider *MistralProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream performs a streaming text completion request to Mistral's API.
// It formats the request, sends it to Mistral, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *MistralProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to the Mistral API.
func (provider *MistralProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/v1/chat/completions"),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to the Mistral API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Uses Mistral's OpenAI-compatible streaming format.
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *MistralProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	var authHeader map[string]string
	if key.Value != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + key.Value}
	}
	// Use shared OpenAI-compatible streaming logic
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+"/v1/chat/completions",
		request,
		authHeader,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		schemas.Mistral,
		postHookRunner,
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// Responses performs a responses request to the Mistral API.
func (provider *MistralProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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

// ResponsesStream performs a streaming responses request to the Mistral API.
func (provider *MistralProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return provider.ChatCompletionStream(
		ctx,
		postHookRunner,
		key,
		request.ToChatRequest(),
	)
}

// Embedding generates embeddings for the given input text(s) using the Mistral API.
// Supports Mistral's embedding models and returns a BifrostResponse containing the embedding(s).
func (provider *MistralProvider) Embedding(ctx context.Context, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	// Use the shared embedding request handler
	return openai.HandleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/v1/embeddings"),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		schemas.Mistral,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.logger,
	)
}

// Speech is not supported by the Mistral provider.
func (provider *MistralProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Mistral provider.
func (provider *MistralProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription performs an audio transcription request to the Mistral API.
// It creates a multipart form with the audio file and sends it to Mistral's transcription endpoint.
// Returns the transcribed text and metadata, or an error if the request fails.
func (provider *MistralProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Convert Bifrost request to Mistral format
	mistralReq := ToMistralTranscriptionRequest(request)
	if mistralReq == nil {
		return nil, providerUtils.NewBifrostOperationError("transcription input is not provided", nil, providerName)
	}

	// Create multipart form body
	body, contentType, bifrostErr := createMistralTranscriptionMultipartBody(mistralReq, providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create HTTP request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/v1/audio/transcriptions"))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(contentType)
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
		return nil, openai.ParseOpenAIError(resp, schemas.TranscriptionRequest, providerName, request.Model)
	}

	// Copy response body before releasing
	responseBody := append([]byte(nil), resp.Body()...)

	// Parse Mistral's transcription response
	var mistralResponse MistralTranscriptionResponse
	if err := sonic.Unmarshal(responseBody, &mistralResponse); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Convert to Bifrost format
	response := mistralResponse.ToBifrostTranscriptionResponse()
	if response == nil {
		return nil, providerUtils.NewBifrostOperationError("failed to convert transcription response", nil, providerName)
	}

	// Set extra fields
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.RequestType = schemas.TranscriptionRequest
	response.ExtraFields.Provider = providerName
	response.ExtraFields.ModelRequested = request.Model

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err == nil {
			response.ExtraFields.RawResponse = rawResponse
		}
	}

	return response, nil
}

// TranscriptionStream performs a streaming transcription request to Mistral's API.
// It creates a multipart form with the audio file and streams transcription events.
// Returns a channel of BifrostStream objects containing transcription deltas.
func (provider *MistralProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Convert Bifrost request to Mistral format
	mistralReq := ToMistralTranscriptionRequest(request)
	if mistralReq == nil {
		return nil, providerUtils.NewBifrostOperationError("transcription input is not provided", nil, providerName)
	}

	// Create multipart form body with stream=true
	body, contentType, bifrostErr := createMistralTranscriptionStreamMultipartBody(mistralReq, providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Prepare headers for streaming
	headers := map[string]string{
		"Content-Type":  contentType,
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
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/v1/audio/transcriptions"))

	// Set headers
	for headerKey, value := range headers {
		req.Header.Set(headerKey, value)
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
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, openai.ParseOpenAIError(resp, schemas.TranscriptionStreamRequest, providerName, request.Model)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer close(responseChan)
		defer providerUtils.ReleaseStreamingResponse(resp)

		scanner := bufio.NewScanner(resp.BodyStream())
		// Increase buffer size to handle large chunks
		buf := make([]byte, 0, 64*1024) // 64KB initial buffer
		scanner.Buffer(buf, 1024*1024)  // Allow up to 1MB tokens
		chunkIndex := -1

		startTime := time.Now()
		lastChunkTime := startTime

		var currentEvent string
		var currentData string

		for scanner.Scan() {
			// Check if context is done before processing
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Text()

			// Skip empty lines (event delimiter)
			if line == "" {
				// Process accumulated event if we have both event and data
				if currentEvent != "" && currentData != "" {
					chunkIndex++
					provider.processStreamEvent(ctx, postHookRunner, currentEvent, currentData, request.Model, providerName, chunkIndex, startTime, &lastChunkTime, responseChan)
				}
				// Reset for next event
				currentEvent = ""
				currentData = ""
				continue
			}

			// Parse SSE format
			if strings.HasPrefix(line, "event: ") {
				currentEvent = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				currentData = strings.TrimPrefix(line, "data: ")
			}
		}

		// Process any remaining event
		if currentEvent != "" && currentData != "" {
			chunkIndex++
			provider.processStreamEvent(ctx, postHookRunner, currentEvent, currentData, request.Model, providerName, chunkIndex, startTime, &lastChunkTime, responseChan)
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.TranscriptionStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// processStreamEvent processes a single SSE event and sends it to the response channel.
func (provider *MistralProvider) processStreamEvent(
	ctx context.Context,
	postHookRunner schemas.PostHookRunner,
	eventType string,
	jsonData string,
	model string,
	providerName schemas.ModelProvider,
	chunkIndex int,
	startTime time.Time,
	lastChunkTime *time.Time,
	responseChan chan *schemas.BifrostStream,
) {
	// Skip empty data
	if strings.TrimSpace(jsonData) == "" {
		return
	}

	// First, check if this is an error response
	var bifrostErr schemas.BifrostError
	if err := sonic.Unmarshal([]byte(jsonData), &bifrostErr); err == nil {
		if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
			bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
				Provider:       providerName,
				ModelRequested: model,
				RequestType:    schemas.TranscriptionStreamRequest,
			}
			ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &bifrostErr, responseChan, provider.logger)
			return
		}
	}

	// Parse the event data
	var eventData MistralTranscriptionStreamData
	if err := sonic.Unmarshal([]byte(jsonData), &eventData); err != nil {
		provider.logger.Warn(fmt.Sprintf("Failed to parse stream event data: %v", err))
		return
	}

	// Create the stream event
	streamEvent := &MistralTranscriptionStreamEvent{
		Event: eventType,
		Data:  &eventData,
	}

	// Convert to Bifrost format
	response := streamEvent.ToBifrostTranscriptionStreamResponse()
	if response == nil {
		return
	}

	// Set extra fields
	response.ExtraFields = schemas.BifrostResponseExtraFields{
		RequestType:    schemas.TranscriptionStreamRequest,
		Provider:       providerName,
		ModelRequested: model,
		ChunkIndex:     chunkIndex,
		Latency:        time.Since(*lastChunkTime).Milliseconds(),
	}
	*lastChunkTime = time.Now()

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = jsonData
	}

	// Check for done event
	if MistralTranscriptionStreamEventType(eventType) == MistralTranscriptionStreamEventDone {
		response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
	}

	providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, response), responseChan)
}

// BatchCreate is not supported by Mistral provider.
func (provider *MistralProvider) BatchCreate(_ context.Context, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by Mistral provider.
func (provider *MistralProvider) BatchList(_ context.Context, _ schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by Mistral provider.
func (provider *MistralProvider) BatchRetrieve(_ context.Context, _ schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by Mistral provider.
func (provider *MistralProvider) BatchCancel(_ context.Context, _ schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchResults is not supported by Mistral provider.
func (provider *MistralProvider) BatchResults(_ context.Context, _ schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload is not supported by Mistral provider.
func (provider *MistralProvider) FileUpload(_ context.Context, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by Mistral provider.
func (provider *MistralProvider) FileList(_ context.Context, _ schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by Mistral provider.
func (provider *MistralProvider) FileRetrieve(_ context.Context, _ schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by Mistral provider.
func (provider *MistralProvider) FileDelete(_ context.Context, _ schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by Mistral provider.
func (provider *MistralProvider) FileContent(_ context.Context, _ schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}
