package elevenlabs

import (
	"bytes"
	"context"
	// "encoding/base64"
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
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewElevenlabsProvider creates a new Gemini provider instance.
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
		sendBackRawResponse:  config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Gemini.
func (provider *ElevenlabsProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Elevenlabs, provider.customProviderConfig)
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *ElevenlabsProvider) listModelsByKey(ctx context.Context, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
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

	if key.Value != "" {
		req.Header.Set("xi-api-key", key.Value)
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if resp.StatusCode() != fasthttp.StatusOK {
		bifrostErr := parseElevenlabsError(providerName, resp)
		return nil, bifrostErr
	}

	var elevenlabsResponse ElevenlabsListModelsResponse
	rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &elevenlabsResponse, providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := elevenlabsResponse.ToBifrostListModelsResponse(providerName)

	response.ExtraFields.Latency = latency.Milliseconds()

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ListModels performs a list models request to Elevenlabs' API.
// Requests are made concurrently for improved performance.
func (provider *ElevenlabsProvider) ListModels(ctx context.Context, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gemini, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
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

// buildSpeechRequestURL constructs the full request URL using the provider's configuration for speech.
func (provider *ElevenlabsProvider) buildSpeechRequestURL(ctx context.Context, defaultPath string, requestType schemas.RequestType, request *schemas.BifrostSpeechRequest) string {
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

		var responseFormat string
		switch request.Params.ResponseFormat {
		case "", "mp3":
			responseFormat = "mp3_44100_128"
		case "opus":
			responseFormat = "opus_48000_128"
		case "wav":
			responseFormat = "pcm_44100"
		case "pcm":
			responseFormat = "pcm_44100"
		}

		request.Params.ResponseFormat = responseFormat

		if request.Params.OptimizeStreamingLatency != nil {
			q.Set("optimize_streaming_latency", strconv.FormatBool(*request.Params.OptimizeStreamingLatency))
		}
	}

	u.RawQuery = q.Encode()
	return u.String()
}

// buildTranscriptionRequestURL constructs the full request URL using the provider's configuration for transcription.
func (provider *ElevenlabsProvider) buildTranscriptionRequestURL(ctx context.Context) string {
	baseURL := provider.networkConfig.BaseURL
	requestPath := providerUtils.GetRequestPath(ctx, "/v1/speech-to-text", provider.customProviderConfig, schemas.TranscriptionRequest)

	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return baseURL + requestPath
	}

	parsedURL.Path = path.Join(parsedURL.Path, requestPath)

	return parsedURL.String()
}

func parseElevenlabsError(providerName schemas.ModelProvider, resp *fasthttp.Response) *schemas.BifrostError {
	body := resp.Body()

	// Try to parse as Elevenlabs validation error first
	var errorResp ElevenlabsValidationError
	if err := sonic.Unmarshal(body, &errorResp); err == nil && errorResp.Detail != nil && len(errorResp.Detail) > 0 {
		var messages []string
		for _, detail := range errorResp.Detail {
			location := "unknown"
			if len(detail.Loc) > 0 {
				location = strings.Join(detail.Loc, ".")
			}
			messages = append(messages, fmt.Sprintf("[%s] %s (%s)", location, detail.Msg, detail.Type))
		}

		errorMessage := strings.Join(messages, "; ")

		bifrostErr := &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode()),
			Error: &schemas.ErrorField{
				Code:    schemas.Ptr("validation_error"),
				Message: fmt.Sprintf("Elevenlabs validation error: %s", errorMessage),
			},
		}
		return bifrostErr
	}

	// Try to parse as generic Elevenlabs error
	var genericError ElevenlabsGenericError
	if err := sonic.Unmarshal(body, &genericError); err == nil && genericError.Detail.Message != "" {
		bifrostErr := &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode()),
			Error: &schemas.ErrorField{
				Code:    schemas.Ptr(genericError.Detail.Status),
				Message: genericError.Detail.Message,
			},
		}
		return bifrostErr
	}

	// Fallback to raw response parsing
	var rawResponse map[string]interface{}
	if err := sonic.Unmarshal(body, &rawResponse); err != nil {
		return providerUtils.NewBifrostOperationError("failed to parse error response", err, providerName)
	}

	return providerUtils.NewBifrostOperationError(fmt.Sprintf("Elevenlabs error: %v", rawResponse), fmt.Errorf("HTTP %d", resp.StatusCode()), providerName)
}

// TextCompletion is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) TextCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) TextCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) ChatCompletion(ctx context.Context, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, provider.GetProviderKey())
}

// ChatCompletionStream is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) ChatCompletionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionStreamRequest, provider.GetProviderKey())
}

// Responses is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) Responses(ctx context.Context, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesRequest, provider.GetProviderKey())
}

// ResponsesStream is not supported by the Elevenlabs provider
func (provider *ElevenlabsProvider) ResponsesStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesStreamRequest, provider.GetProviderKey())
}

// Embedding is not supported by the Elevenlabs provider.
func (provider *ElevenlabsProvider) Embedding(ctx context.Context, key schemas.Key, input *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Speech performs a text to speech request
func (provider *ElevenlabsProvider) Speech(ctx context.Context, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
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

	// Determine if timestamps are requested
	withTimestamps := request.Params != nil && request.Params.WithTimestamps != nil && *request.Params.WithTimestamps

	voice := ""
	if request.Params != nil && request.Params.VoiceConfig != nil && request.Params.VoiceConfig.Voice != nil {
		voice = *request.Params.VoiceConfig.Voice
	}

	if voice == "" {
		return nil, providerUtils.NewBifrostOperationError("voice parameter is required", nil, providerName)
	}

	var endpoint string
	if withTimestamps {
		endpoint = "/v1/text-to-speech/" + voice + "/with-timestamps"
	} else {
		endpoint = "/v1/text-to-speech/" + voice
	}

	requestURL := provider.buildSpeechRequestURL(ctx, endpoint, schemas.SpeechRequest, request)
	provider.logger.Debug(fmt.Sprintf("Speech request URL: %s, withTimestamps: %v", requestURL, withTimestamps))
	req.SetRequestURI(requestURL)

	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("xi-api-key", key.Value)
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
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, parseElevenlabsError(providerName, resp)
	}

	// Get the response body
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
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

	fmt.Println("withTimestamps: ", withTimestamps)
	if withTimestamps {
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
func (provider *ElevenlabsProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
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

	req.SetRequestURI(provider.buildSpeechRequestURL(ctx, "/v1/text-to-speech/"+*request.Params.VoiceConfig.Voice+"/stream", schemas.SpeechStreamRequest, request))

	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value != "" {
		req.Header.Set("xi-api-key", key.Value)
	}

	req.SetBody(jsonBody)

	// Make request
	startTime := time.Now()
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
		return nil, parseElevenlabsError(providerName, resp)
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStream, schemas.DefaultStreamBufferSize)

	go func() {
		defer close(responseChan)
		defer providerUtils.ReleaseStreamingResponse(resp)

		chunkIndex := -1
		lastChunkTime := time.Now()

		// read binary audio chunks from the stream
		// 4KB buffer for reading chunks
		buffer := make([]byte, 4096)
		bodyStream := resp.BodyStream()

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, err := bodyStream.Read(buffer)
			if err != nil {
				if err == io.EOF {
					break
				}
				provider.logger.Warn(fmt.Sprintf("Error reading stream: %v", err))
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

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, response, nil), responseChan)
			}
		}

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

		ctx = context.WithValue(ctx, schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, finalResponse, nil), responseChan)
	}()

	return responseChan, nil
}

// Transcription performs a transcription request
func (provider *ElevenlabsProvider) Transcription(ctx context.Context, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Elevenlabs, provider.customProviderConfig, schemas.TranscriptionRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	reqBody := ToElevenlabsTranscriptionRequest(request)
	if reqBody == nil {
		return nil, providerUtils.NewBifrostOperationError("transcription request is not provided", nil, providerName)
	}

	if strings.TrimSpace(reqBody.ModelID) == "" {
		return nil, providerUtils.NewBifrostOperationError("model_id is required for Elevenlabs transcription", nil, providerName)
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

	req.SetRequestURI(provider.buildTranscriptionRequestURL(ctx))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(contentType)
	if key.Value != "" {
		req.Header.Set("xi-api-key", key.Value)
	}
	req.SetBody(body.Bytes())

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	statusCode := resp.StatusCode()
	if statusCode == fasthttp.StatusAccepted {
		responseBody, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
		}

		var webhookResp ElevenlabsSpeechToTextWebhookResponse
		if err := sonic.Unmarshal(responseBody, &webhookResp); err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to parse async transcription response", err, providerName)
		}

		message := webhookResp.Message
		if strings.TrimSpace(message) == "" {
			message = "Elevenlabs transcription request accepted for asynchronous processing"
		}

		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(statusCode),
			Error: &schemas.ErrorField{
				Message: message,
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				Provider:    providerName,
				RequestType: schemas.TranscriptionRequest,
			},
		}
	}

	if statusCode != fasthttp.StatusOK {
		provider.logger.Debug(fmt.Sprintf("error from %s provider: %s", providerName, string(resp.Body())))
		return nil, parseElevenlabsError(providerName, resp)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	sendRaw := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	var rawResponse interface{}
	if sendRaw {
		if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRawResponseUnmarshal, err, providerName)
		}
	}

	chunks, err := parseTranscriptionResponse(responseBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(err.Error(), nil, providerName)
	}

	text, words, logProbs, language, duration := convertChunksToBifrost(chunks)

	modelRequested := ""
	if request != nil {
		modelRequested = request.Model
	}

	response := &schemas.BifrostTranscriptionResponse{
		Text:     text,
		Words:    words,
		LogProbs: logProbs,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.TranscriptionRequest,
			Provider:       providerName,
			ModelRequested: modelRequested,
			Latency:        latency.Milliseconds(),
		},
	}

	if language != nil {
		response.Language = language
	}

	if duration != nil {
		response.Duration = duration
	}

	if sendRaw {
		response.ExtraFields.RawResponse = rawResponse
	}

	response.Task = schemas.Ptr("transcribe")

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

	if reqBody.Temparature != nil {
		if err := writer.WriteField("temperature", strconv.FormatFloat(*reqBody.Temparature, 'f', -1, 64)); err != nil {
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
func (provider *ElevenlabsProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}