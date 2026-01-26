package elevenlabs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type ElevenlabsProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawRequest   bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewElevenlabsProvider creates a new Elevenlabs provider instance.
// It initializes the HTTP client with the provided configuration.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewElevenlabsProvider(config *schemas.ProviderConfig, logger schemas.Logger) *ElevenlabsProvider {
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
		config.NetworkConfig.BaseURL = "https://api.elevenlabs.io"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &ElevenlabsProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Elevenlabs.
func (provider *ElevenlabsProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Elevenlabs, provider.customProviderConfig)
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *ElevenlabsProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Build URL using centralized URL construction
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/v1/models"))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("xi-api-key", key.Value.GetValue())
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, parseElevenlabsError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			RequestType: schemas.ListModelsRequest,
		})
	}

	var elevenlabsResponse ElevenlabsListModelsResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &elevenlabsResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := elevenlabsResponse.ToBifrostListModelsResponse(providerName, key.Models)

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

// ListModels performs a list models request to Elevenlabs' API.
// Requests are made concurrently for improved performance.
func (provider *ElevenlabsProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Elevenlabs, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
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

// TextCompletion is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, provider.GetProviderKey())
}

// ChatCompletionStream is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionStreamRequest, provider.GetProviderKey())
}

// Responses is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesRequest, provider.GetProviderKey())
}

// ResponsesStream is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesStreamRequest, provider.GetProviderKey())
}

// Embedding is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, input *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Speech performs a text to speech request
func (provider *ElevenlabsProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Elevenlabs, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
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

	withTimestampsRequest := request.Params != nil && request.Params.WithTimestamps != nil && *request.Params.WithTimestamps

	var endpoint string
	if request.Params != nil && request.Params.VoiceConfig != nil && request.Params.VoiceConfig.Voice != nil {
		voice := *request.Params.VoiceConfig.Voice
		// Determine if timestamps are requested
		if withTimestampsRequest {
			endpoint = "/v1/text-to-speech/" + voice + "/with-timestamps"
		} else {
			endpoint = "/v1/text-to-speech/" + voice
		}
	} else {
		return nil, providerUtils.NewBifrostOperationError("voice parameter is required", nil, providerName)
	}

	requestURL := provider.buildBaseSpeechRequestURL(ctx, endpoint, schemas.SpeechRequest, request)
	req.SetRequestURI(requestURL)

	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("xi-api-key", key.Value.GetValue())
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToElevenlabsSpeechRequest(request), nil },
		providerName)

	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req.SetBody(jsonData)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, providerUtils.EnrichError(ctx, parseElevenlabsError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			Model:       request.Model,
			RequestType: schemas.SpeechRequest,
		}), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Get the response body
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Create response based on whether timestamps were requested
	bifrostResponse := &schemas.BifrostSpeechResponse{
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.SpeechRequest,
			Provider:       providerName,
			ModelRequested: request.Model,
			Latency:        latency.Milliseconds(),
		},
	}

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	if withTimestampsRequest {
		var timestampResponse ElevenlabsSpeechWithTimestampsResponse
		if err := sonic.Unmarshal(body, &timestampResponse); err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to parse with-timestamps response", err, providerName)
		}

		bifrostResponse.AudioBase64 = &timestampResponse.AudioBase64

		if timestampResponse.Alignment != nil {
			bifrostResponse.Alignment = &schemas.SpeechAlignment{
				CharStartTimesMs: timestampResponse.Alignment.CharStartTimesMs,
				CharEndTimesMs:   timestampResponse.Alignment.CharEndTimesMs,
				Characters:       timestampResponse.Alignment.Characters,
			}
		}

		if timestampResponse.NormalizedAlignment != nil {
			bifrostResponse.NormalizedAlignment = &schemas.SpeechAlignment{
				CharStartTimesMs: timestampResponse.NormalizedAlignment.CharStartTimesMs,
				CharEndTimesMs:   timestampResponse.NormalizedAlignment.CharEndTimesMs,
				Characters:       timestampResponse.NormalizedAlignment.Characters,
			}
		}

		return bifrostResponse, nil
	}

	bifrostResponse.Audio = body
	return bifrostResponse, nil
}

// SpeechStream performs a text to speech stream request
func (provider *ElevenlabsProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Elevenlabs, provider.customProviderConfig, schemas.SpeechStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	reqBody := ToElevenlabsSpeechRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("speech input is not provided", nil, providerName)
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (any, error) { return ToElevenlabsSpeechRequest(request), nil },
		providerName)

	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create HTTP request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	if request.Params == nil || request.Params.VoiceConfig == nil || request.Params.VoiceConfig.Voice == nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("voice parameter is required", nil, providerName), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	req.SetRequestURI(provider.buildBaseSpeechRequestURL(ctx, "/v1/text-to-speech/"+*request.Params.VoiceConfig.Voice+"/stream", schemas.SpeechStreamRequest, request))

	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("xi-api-key", key.Value.GetValue())
	}

	req.SetBody(jsonBody)

	// Make request
	startTime := time.Now()
	err := provider.client.Do(req, resp)
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
			}, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, providerName), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		return nil, providerUtils.EnrichError(ctx, parseElevenlabsError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			Model:       request.Model,
			RequestType: schemas.SpeechStreamRequest,
		}), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.SpeechStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.SpeechStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), provider.logger)
		defer stopCancellation()

		// read binary audio chunks from the stream
		// 4KB buffer for reading chunks
		buffer := make([]byte, 4096)
		bodyStream := resp.BodyStream()
		chunkIndex := -1
		lastChunkTime := time.Now()

		for {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			n, err := bodyStream.Read(buffer)
			if err != nil {
				// If context was cancelled/timed out, let defer handle it
				if ctx.Err() != nil {
					return
				}
				if err == io.EOF {
					break
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("Error reading stream: %v", err)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.SpeechStreamRequest, providerName, request.Model, provider.logger)
				return
			}

			if n > 0 {
				chunkIndex++
				audioChunk := make([]byte, n)
				copy(audioChunk, buffer[:n])

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
					response.ExtraFields.RawResponse = audioChunk
				}

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, response, nil, nil), responseChan)
			}
		}

		// Send final response after natural loop termination (similar to Gemini pattern)
		finalResponse := &schemas.BifrostSpeechStreamResponse{
			Type:  schemas.SpeechStreamResponseTypeDone,
			Audio: []byte{},
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
			providerUtils.ParseAndSetRawRequest(&finalResponse.ExtraFields, jsonBody)
		}
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, finalResponse, nil, nil), responseChan)
	}()

	return responseChan, nil
}

// Transcription performs a transcription request
func (provider *ElevenlabsProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Elevenlabs, provider.customProviderConfig, schemas.TranscriptionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	reqBody := ToElevenlabsTranscriptionRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("transcription request is not provided", nil, providerName)
	}

	hasFile := len(reqBody.File) > 0
	hasURL := reqBody.CloudStorageURL != nil && strings.TrimSpace(*reqBody.CloudStorageURL) != ""
	if hasFile && hasURL {
		return nil, providerUtils.NewBifrostOperationError("provide either a file or cloud_storage_url, not both", nil, providerName)
	}
	if !hasFile && !hasURL {
		return nil, providerUtils.NewBifrostOperationError("either a transcription file or cloud_storage_url must be provided", nil, providerName)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if bifrostErr := writeTranscriptionMultipart(writer, reqBody, providerName); bifrostErr != nil {
		return nil, bifrostErr
	}

	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to finalize multipart transcription request", err, providerName)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetRequestPath(ctx, "/v1/speech-to-text", provider.customProviderConfig, schemas.TranscriptionRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(contentType)
	if key.Value.GetValue() != "" {
		req.Header.Set("xi-api-key", key.Value.GetValue())
	}
	req.SetBody(body.Bytes())

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, parseElevenlabsError(resp, &providerUtils.RequestMetadata{
			Provider:    providerName,
			Model:       request.Model,
			RequestType: schemas.TranscriptionRequest,
		})
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

	chunks, err := parseTranscriptionResponse(responseBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(err.Error(), nil, providerName)
	}

	if len(chunks) == 0 {
		return nil, providerUtils.NewBifrostOperationError("no chunks found in transcription response", nil, providerName)
	}

	response := ToBifrostTranscriptionResponse(chunks)
	response.ExtraFields = schemas.BifrostResponseExtraFields{
		RequestType:    schemas.TranscriptionRequest,
		Provider:       providerName,
		ModelRequested: request.Model,
		Latency:        latency.Milliseconds(),
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRawResponseUnmarshal, err, providerName)
		}
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

func writeTranscriptionMultipart(writer *multipart.Writer, reqBody *ElevenlabsTranscriptionRequest, providerName schemas.ModelProvider) *schemas.BifrostError {
	if err := writer.WriteField("model_id", reqBody.ModelID); err != nil {
		return providerUtils.NewBifrostOperationError("failed to write model_id field", err, providerName)
	}

	if len(reqBody.File) > 0 {
		fileWriter, err := writer.CreateFormFile("file", "audio.wav")
		if err != nil {
			return providerUtils.NewBifrostOperationError("failed to create file field", err, providerName)
		}
		if _, err := fileWriter.Write(reqBody.File); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write file data", err, providerName)
		}
	}

	if reqBody.CloudStorageURL != nil && strings.TrimSpace(*reqBody.CloudStorageURL) != "" {
		if err := writer.WriteField("cloud_storage_url", *reqBody.CloudStorageURL); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write cloud_storage_url field", err, providerName)
		}
	}

	if reqBody.LanguageCode != nil && strings.TrimSpace(*reqBody.LanguageCode) != "" {
		if err := writer.WriteField("language_code", *reqBody.LanguageCode); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write language_code field", err, providerName)
		}
	}

	if reqBody.TagAudioEvents != nil {
		if err := writer.WriteField("tag_audio_events", strconv.FormatBool(*reqBody.TagAudioEvents)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write tag_audio_events field", err, providerName)
		}
	}

	if reqBody.NumSpeakers != nil && *reqBody.NumSpeakers > 0 {
		if err := writer.WriteField("num_speakers", strconv.Itoa(*reqBody.NumSpeakers)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write num_speakers field", err, providerName)
		}
	}

	if reqBody.TimestampsGranularity != nil && *reqBody.TimestampsGranularity != "" {
		if err := writer.WriteField("timestamps_granularity", string(*reqBody.TimestampsGranularity)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write timestamps_granularity field", err, providerName)
		}
	}

	if reqBody.Diarize != nil {
		if err := writer.WriteField("diarize", strconv.FormatBool(*reqBody.Diarize)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write diarize field", err, providerName)
		}
	}

	if reqBody.DiarizationThreshold != nil {
		if err := writer.WriteField("diarization_threshold", strconv.FormatFloat(*reqBody.DiarizationThreshold, 'f', -1, 64)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write diarization_threshold field", err, providerName)
		}
	}

	if len(reqBody.AdditionalFormats) > 0 {
		payload, err := sonic.Marshal(reqBody.AdditionalFormats)
		if err != nil {
			return providerUtils.NewBifrostOperationError("failed to marshal additional_formats", err, providerName)
		}
		if err := writer.WriteField("additional_formats", string(payload)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write additional_formats field", err, providerName)
		}
	}

	if reqBody.FileFormat != nil && *reqBody.FileFormat != "" {
		if err := writer.WriteField("file_format", string(*reqBody.FileFormat)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write file_format field", err, providerName)
		}
	}

	if reqBody.Webhook != nil {
		if err := writer.WriteField("webhook", strconv.FormatBool(*reqBody.Webhook)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write webhook field", err, providerName)
		}
	}

	if reqBody.WebhookID != nil && strings.TrimSpace(*reqBody.WebhookID) != "" {
		if err := writer.WriteField("webhook_id", *reqBody.WebhookID); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write webhook_id field", err, providerName)
		}
	}

	if reqBody.Temperature != nil {
		if err := writer.WriteField("temperature", strconv.FormatFloat(*reqBody.Temperature, 'f', -1, 64)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write temperature field", err, providerName)
		}
	}

	if reqBody.Seed != nil {
		if err := writer.WriteField("seed", strconv.Itoa(*reqBody.Seed)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write seed field", err, providerName)
		}
	}

	if reqBody.UseMultiChannel != nil {
		if err := writer.WriteField("use_multi_channel", strconv.FormatBool(*reqBody.UseMultiChannel)); err != nil {
			return providerUtils.NewBifrostOperationError("failed to write use_multi_channel field", err, providerName)
		}
	}

	if reqBody.WebhookMetadata != nil {
		switch v := reqBody.WebhookMetadata.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				if err := writer.WriteField("webhook_metadata", v); err != nil {
					return providerUtils.NewBifrostOperationError("failed to write webhook_metadata field", err, providerName)
				}
			}
		default:
			payload, err := sonic.Marshal(v)
			if err != nil {
				return providerUtils.NewBifrostOperationError("failed to marshal webhook_metadata", err, providerName)
			}
			if err := writer.WriteField("webhook_metadata", string(payload)); err != nil {
				return providerUtils.NewBifrostOperationError("failed to write webhook_metadata field", err, providerName)
			}
		}
	}

	return nil
}

// TranscriptionStream is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// buildSpeechRequestURL constructs the full request URL using the provider's configuration for speech.
func (provider *ElevenlabsProvider) buildBaseSpeechRequestURL(ctx *schemas.BifrostContext, defaultPath string, requestType schemas.RequestType, request *schemas.BifrostSpeechRequest) string {
	baseURL := provider.networkConfig.BaseURL
	requestPath := providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)

	u, parseErr := url.Parse(baseURL)
	if parseErr != nil {
		return baseURL + requestPath
	}

	u.Path = path.Join(u.Path, requestPath)
	q := u.Query()

	if request.Params != nil {
		if request.Params.EnableLogging != nil {
			q.Set("enable_logging", strconv.FormatBool(*request.Params.EnableLogging))
		}

		convertedFormat := ConvertBifrostSpeechFormatToElevenlabs(request.Params.ResponseFormat)
		if convertedFormat != "" {
			q.Set("output_format", convertedFormat)
		}

		if request.Params.OptimizeStreamingLatency != nil {
			q.Set("optimize_streaming_latency", strconv.FormatBool(*request.Params.OptimizeStreamingLatency))
		}
	}

	u.RawQuery = q.Encode()
	return u.String()
}

// BatchCreate is not supported by Elevenlabs provider.
func (provider *ElevenlabsProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by Elevenlabs provider.
func (provider *ElevenlabsProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by Elevenlabs provider.
func (provider *ElevenlabsProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by Elevenlabs provider.
func (provider *ElevenlabsProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchResults is not supported by Elevenlabs provider.
func (provider *ElevenlabsProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload is not supported by Elevenlabs provider.
func (provider *ElevenlabsProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by Elevenlabs provider.
func (provider *ElevenlabsProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by Elevenlabs provider.
func (provider *ElevenlabsProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by Elevenlabs provider.
func (provider *ElevenlabsProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by Elevenlabs provider.
func (provider *ElevenlabsProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}
