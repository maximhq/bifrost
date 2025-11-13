package elevenlabs

import (
	"bytes"
	"context"
	"fmt"
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

// buildRequestURL constructs the full request URL using the provider's configuration.
// TODO: instead of taking request, take query / raw query
func (provider *ElevenlabsProvider) buildRequestURL(ctx context.Context, defaultPath string, requestType schemas.RequestType, request *schemas.BifrostSpeechRequest) string {
	baseURL := provider.networkConfig.BaseURL
	requestPath := providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)

	u, parseErr := url.Parse(baseURL)
	if parseErr != nil {
		return baseURL + requestPath
	}

	// TODO: move query handling to providers
	u.Path = path.Join(u.Path, requestPath)
	q := u.Query()

	if request.Params != nil {
		if request.Params.EnableLogging != nil {
			q.Set("enable_logging", strconv.FormatBool(*request.Params.EnableLogging))
		}

		if request.Params.OutputFormat != nil {
			q.Set("output_format", string(*request.Params.OutputFormat))
		}

		if request.Params.OptimizeStreamingLatency != nil {
			q.Set("optimize_streaming_latency", strconv.FormatBool(*request.Params.OptimizeStreamingLatency))
		}
	}

	u.RawQuery = q.Encode()
	return u.String()
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

	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/text-to-speech/"+*request.Params.VoiceConfig.Voice, schemas.SpeechRequest, request))

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

// --- TODO ---
func (provider *ElevenlabsProvider) SpeechStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

func (provider *ElevenlabsProvider) TranscriptionStream(ctx context.Context, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// --- UNSUPPORTED METHODS ---
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