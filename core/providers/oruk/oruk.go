// Package oruk implements the Oruk Speech API provider (Fourier 2B English ASR).
// Oruk exposes an OpenAI-compatible /v1/audio/transcriptions endpoint with strict
// request-field validation, so this provider sanitizes requests and delegates HTTP
// execution to the shared OpenAI transcription handler.
package oruk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// defaultBaseURL is Oruk's Speech API root. Request paths add the /v1/... suffix.
const defaultBaseURL = "https://speech-api.oruk.ai"

// defaultModel is the canonical Fourier 2B speech-to-text model ID.
const defaultModel = "oruk-fourier-2b"

// maxAudioBytes matches Oruk's 30 MiB upload ceiling.
const maxAudioBytes = 30 * 1024 * 1024

// maxMultipartOverheadBytes bounds non-file multipart headers and form fields
// when inspecting a streamed large-payload upload.
const maxMultipartOverheadBytes = 1 * 1024 * 1024

type catalogModel struct {
	ID   string
	Name string
}

var knownOrukModels = []catalogModel{
	{
		ID:   defaultModel,
		Name: "Oruk Fourier 2B",
	},
}

// OrukProvider implements the Provider interface for Oruk's Speech API.
type OrukProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for unary API requests
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewOrukProvider creates a new Oruk provider instance.
// It initializes the HTTP client with the provided configuration, proxy, dialer, and TLS settings.
func NewOrukProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*OrukProvider, error) {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}

	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = defaultBaseURL
	}
	baseURL := strings.TrimRight(config.NetworkConfig.BaseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	config.NetworkConfig.BaseURL = baseURL

	return &OrukProvider{
		logger:              logger,
		client:              client,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Oruk.
func (provider *OrukProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Oruk
}

func (provider *OrukProvider) listModelsByKey(_ *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	unfiltered := request != nil && request.Unfiltered
	bifrostResponse := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(knownOrukModels)),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     key.Models,
		BlacklistedModels: key.BlacklistedModels,
		Aliases:           key.Aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       provider.GetProviderKey(),
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return bifrostResponse, nil
	}

	included := make(map[string]bool)
	for _, model := range knownOrukModels {
		for _, result := range pipeline.FilterModel(model.ID) {
			name := model.Name
			entry := schemas.Model{
				ID:   string(provider.GetProviderKey()) + "/" + result.ResolvedID,
				Name: &name,
			}
			if result.AliasValue != "" {
				alias := result.AliasValue
				entry.Alias = &alias
			}
			bifrostResponse.Data = append(bifrostResponse.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}

	bifrostResponse.Data = append(bifrostResponse.Data, pipeline.BackfillModels(included)...)
	return bifrostResponse, nil
}

// ListModels returns the speech-to-text models exposed by the Oruk Speech API.
func (provider *OrukProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}

func newBadRequestError(message string) *schemas.BifrostError {
	status := http.StatusBadRequest
	errType := "invalid_request_error"
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &status,
		Error: &schemas.ErrorField{
			Type:    &errType,
			Message: message,
		},
	}
}

func newPayloadTooLargeError(message string) *schemas.BifrostError {
	status := http.StatusRequestEntityTooLarge
	errType := "invalid_request_error"
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &status,
		Error: &schemas.ErrorField{
			Type:    &errType,
			Message: message,
		},
	}
}

func normalizeOrukLanguage(raw string) (string, *schemas.BifrostError) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	switch strings.ToLower(strings.ReplaceAll(trimmed, "_", "-")) {
	case "en", "english":
		return "en", nil
	case "en-us":
		return "en-US", nil
	case "en-gb":
		return "en-GB", nil
	case "auto":
		return "auto", nil
	default:
		return "", newBadRequestError(fmt.Sprintf("oruk only supports English transcription (en, en-US, en-GB, auto); got %q", raw))
	}
}

func normalizeOrukResponseFormat(raw string) (string, *schemas.BifrostError) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	// Require exact lowercase values matching schemas.IsPlainTextTranscriptionFormat
	// and OpenAI's case-sensitive response_format enum so core backfill stays consistent.
	switch trimmed {
	case "json", "verbose_json", "text":
		return trimmed, nil
	default:
		return "", newBadRequestError(fmt.Sprintf("oruk supports lowercase response_format values json, verbose_json, or text; got %q", raw))
	}
}

// sanitizeTranscriptionRequest validates Oruk-specific constraints and builds a copy
// containing only the fields accepted by Oruk's strict /v1/audio/transcriptions schema
// (file, model, response_format, and optional English language).
func sanitizeTranscriptionRequest(request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionRequest, *schemas.BifrostError) {
	if request == nil || request.Input == nil || len(request.Input.File) == 0 {
		return nil, newBadRequestError("transcription input is not provided")
	}
	if len(request.Input.File) > maxAudioBytes {
		return nil, newPayloadTooLargeError(fmt.Sprintf("audio file exceeds Oruk's 30 MiB limit (%d bytes)", len(request.Input.File)))
	}

	sanitized := *request
	if strings.TrimSpace(sanitized.Model) == "" {
		sanitized.Model = defaultModel
	}

	if request.Params != nil {
		cleanParams := &schemas.TranscriptionParameters{}
		if request.Params.Language != nil {
			lang, err := normalizeOrukLanguage(*request.Params.Language)
			if err != nil {
				return nil, err
			}
			if lang != "" {
				cleanParams.Language = &lang
			}
		}
		if request.Params.ResponseFormat != nil {
			format, err := normalizeOrukResponseFormat(*request.Params.ResponseFormat)
			if err != nil {
				return nil, err
			}
			if format != "" {
				cleanParams.ResponseFormat = &format
			}
		}
		sanitized.Params = cleanParams
	}

	return &sanitized, nil
}

// sanitizeLargePayloadMultipart enforces Oruk's 30 MiB ceiling and strict field allowlist
// (file, model, language, response_format) when Bifrost large-payload mode streams the
// multipart request body via context rather than populating request.Input.File.
func sanitizeLargePayloadMultipart(ctx *schemas.BifrostContext, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionRequest, *schemas.BifrostError) {
	if contentLength, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadContentLength).(int); ok && contentLength > maxAudioBytes+maxMultipartOverheadBytes {
		return nil, newPayloadTooLargeError(fmt.Sprintf("audio payload exceeds Oruk's 30 MiB limit (%d bytes)", contentLength))
	}

	contentType, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadContentType).(string)
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(strings.ToLower(mediaType), "multipart/") || params["boundary"] == "" {
		return nil, newBadRequestError("oruk transcription requires a multipart/form-data request body")
	}

	reader, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadReader).(io.Reader)
	mr := multipart.NewReader(io.LimitReader(reader, maxAudioBytes+maxMultipartOverheadBytes+1), params["boundary"])

	var (
		model          string
		language       string
		responseFormat string
		filename       string
		fileBytes      []byte
		hasFile        bool
	)

	for {
		part, nextErr := mr.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, newBadRequestError(fmt.Sprintf("failed to read multipart transcription body: %v", nextErr))
		}

		name := part.FormName()
		switch name {
		case "file":
			data, readErr := io.ReadAll(io.LimitReader(part, maxAudioBytes+1))
			_ = part.Close()
			if readErr != nil {
				return nil, newBadRequestError(fmt.Sprintf("failed to read audio file part: %v", readErr))
			}
			if len(data) > maxAudioBytes {
				return nil, newPayloadTooLargeError("audio file exceeds Oruk's 30 MiB limit")
			}
			fileBytes = data
			filename = part.FileName()
			hasFile = true
		case "model":
			data, _ := io.ReadAll(io.LimitReader(part, 1024))
			_ = part.Close()
			rawModel := strings.TrimSpace(string(data))
			if rawModel != "" {
				_, parsedModel := schemas.ParseModelString(rawModel, schemas.Oruk)
				if parsedModel != "" {
					model = parsedModel
				} else {
					model = rawModel
				}
			}
		case "language":
			data, _ := io.ReadAll(io.LimitReader(part, 256))
			_ = part.Close()
			normalized, bErr := normalizeOrukLanguage(string(data))
			if bErr != nil {
				return nil, bErr
			}
			language = normalized
		case "response_format":
			data, _ := io.ReadAll(io.LimitReader(part, 256))
			_ = part.Close()
			normalized, bErr := normalizeOrukResponseFormat(string(data))
			if bErr != nil {
				return nil, bErr
			}
			responseFormat = normalized
		default:
			// Drop fields not accepted by Oruk's strict Fourier2bRequest schema.
			_, _ = io.Copy(io.Discard, io.LimitReader(part, maxMultipartOverheadBytes))
			_ = part.Close()
		}
	}

	if !hasFile || len(fileBytes) == 0 {
		return nil, newBadRequestError("transcription input is not provided")
	}

	if model == "" {
		if request != nil && strings.TrimSpace(request.Model) != "" {
			_, parsedModel := schemas.ParseModelString(strings.TrimSpace(request.Model), schemas.Oruk)
			if parsedModel != "" {
				model = parsedModel
			} else {
				model = strings.TrimSpace(request.Model)
			}
		} else {
			model = defaultModel
		}
	}

	var sanitizedBuf bytes.Buffer
	outWriter := multipart.NewWriter(&sanitizedBuf)
	if err := outWriter.WriteField("model", model); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write model field", err)
	}
	if language != "" {
		if err := outWriter.WriteField("language", language); err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to write language field", err)
		}
	}
	if responseFormat != "" {
		if err := outWriter.WriteField("response_format", responseFormat); err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to write response_format field", err)
		}
	}
	if filename == "" {
		filename = providerUtils.AudioFilenameFromBytes(fileBytes)
	}
	filePart, err := outWriter.CreateFormFile("file", filename)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create form file", err)
	}
	if _, err := filePart.Write(fileBytes); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write audio bytes", err)
	}
	if err := outWriter.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to close multipart writer", err)
	}

	// Update context with the sanitized multipart stream so large-payload passthrough
	// forwards strictly allowed Oruk fields with normalized model name.
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, bytes.NewReader(sanitizedBuf.Bytes()))
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, sanitizedBuf.Len())
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentType, outWriter.FormDataContentType())
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, nil)

	var outReq schemas.BifrostTranscriptionRequest
	if request != nil {
		outReq = *request
	}
	outReq.Provider = schemas.Oruk
	outReq.Model = model
	if language != "" || responseFormat != "" {
		params := &schemas.TranscriptionParameters{}
		if language != "" {
			params.Language = &language
		}
		if responseFormat != "" {
			params.ResponseFormat = &responseFormat
		}
		outReq.Params = params
	}
	if outReq.Params != nil && schemas.IsPlainTextTranscriptionFormat(outReq.Params.ResponseFormat) {
		outReq.Input = &schemas.TranscriptionInput{
			File:     fileBytes,
			Filename: filename,
		}
	}
	return &outReq, nil
}

// backfillDurationUsage ensures duration-only Oruk responses populate Usage.Seconds
// so Bifrost's model catalog CalculateCost bills per-second audio duration accurately.
func backfillDurationUsage(resp *schemas.BifrostTranscriptionResponse) {
	if resp == nil || resp.Duration == nil {
		return
	}
	if resp.Usage == nil {
		seconds := *resp.Duration
		resp.Usage = &schemas.TranscriptionUsage{
			Type:    "duration",
			Seconds: &seconds,
		}
		return
	}
	if resp.Usage.Seconds == nil {
		seconds := *resp.Duration
		resp.Usage.Seconds = &seconds
		if resp.Usage.Type == "" {
			resp.Usage.Type = "duration"
		}
	}
}

// Transcription performs batch English speech-to-text transcription using Oruk's
// OpenAI-compatible /v1/audio/transcriptions endpoint.
func (provider *OrukProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	var (
		sanitized *schemas.BifrostTranscriptionRequest
		err       *schemas.BifrostError
	)
	if providerUtils.IsLargePayloadPassthroughEnabled(ctx) {
		sanitized, err = sanitizeLargePayloadMultipart(ctx, request)
		if err != nil {
			return nil, err
		}
		// HandleOpenAITranscriptionRequest's large-payload branch JSON-unmarshals the
		// upstream body; temporarily route plain-text responses through the standard
		// path using the sanitized input so text output is preserved verbatim.
		if sanitized.Params != nil && schemas.IsPlainTextTranscriptionFormat(sanitized.Params.ResponseFormat) {
			ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, false)
			defer ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
		}
	} else {
		sanitized, err = sanitizeTranscriptionRequest(request)
		if err != nil {
			return nil, err
		}
	}

	resp, bifrostErr := openai.HandleOpenAITranscriptionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/v1/audio/transcriptions"),
		sanitized,
		key,
		provider.networkConfig.ExtraHeaders,
		nil,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		nil,
		provider.logger,
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	backfillDurationUsage(resp)
	return resp, nil
}

// TranscriptionStream is not supported by the Oruk provider.
func (provider *OrukProvider) TranscriptionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// TextCompletion is not supported by the Oruk provider.
func (provider *OrukProvider) TextCompletion(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the Oruk provider.
func (provider *OrukProvider) TextCompletionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion is not supported by the Oruk provider.
func (provider *OrukProvider) ChatCompletion(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, provider.GetProviderKey())
}

// ChatCompletionStream is not supported by the Oruk provider.
func (provider *OrukProvider) ChatCompletionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionStreamRequest, provider.GetProviderKey())
}

// Responses is not supported by the Oruk provider.
func (provider *OrukProvider) Responses(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesRequest, provider.GetProviderKey())
}

// ResponsesStream is not supported by the Oruk provider.
func (provider *OrukProvider) ResponsesStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesStreamRequest, provider.GetProviderKey())
}

// Embedding is not supported by the Oruk provider.
func (provider *OrukProvider) Embedding(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Speech is not supported by the Oruk provider.
func (provider *OrukProvider) Speech(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Oruk provider.
func (provider *OrukProvider) SpeechStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Rerank is not supported by the Oruk provider.
func (provider *OrukProvider) Rerank(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// Decision is not supported by the Oruk provider.
func (provider *OrukProvider) Decision(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.DecisionRequest, provider.GetProviderKey())
}

// OCR is not supported by the Oruk provider.
func (provider *OrukProvider) OCR(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// ImageGeneration is not supported by the Oruk provider.
func (provider *OrukProvider) ImageGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the Oruk provider.
func (provider *OrukProvider) ImageGenerationStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit is not supported by the Oruk provider.
func (provider *OrukProvider) ImageEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the Oruk provider.
func (provider *OrukProvider) ImageEditStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the Oruk provider.
func (provider *OrukProvider) ImageVariation(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoGeneration is not supported by the Oruk provider.
func (provider *OrukProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, provider.GetProviderKey())
}

// VideoRetrieve is not supported by the Oruk provider.
func (provider *OrukProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, provider.GetProviderKey())
}

// VideoDownload is not supported by the Oruk provider.
func (provider *OrukProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}

// VideoDelete is not supported by the Oruk provider.
func (provider *OrukProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by the Oruk provider.
func (provider *OrukProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoEdit is not supported by the Oruk provider.
func (provider *OrukProvider) VideoEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoEditRequest) (*schemas.BifrostVideoEditResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoEditRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by the Oruk provider.
func (provider *OrukProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// FileUpload is not supported by the Oruk provider.
func (provider *OrukProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by the Oruk provider.
func (provider *OrukProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by the Oruk provider.
func (provider *OrukProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by the Oruk provider.
func (provider *OrukProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by the Oruk provider.
func (provider *OrukProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by the Oruk provider.
func (provider *OrukProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by the Oruk provider.
func (provider *OrukProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by the Oruk provider.
func (provider *OrukProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by the Oruk provider.
func (provider *OrukProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchDelete is not supported by the Oruk provider.
func (provider *OrukProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults is not supported by the Oruk provider.
func (provider *OrukProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the Oruk provider.
func (provider *OrukProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// ModelRetrieve is not supported by the Oruk provider.
func (provider *OrukProvider) ModelRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostModelRetrieveRequest) (*schemas.BifrostModelRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ModelRetrieveRequest, provider.GetProviderKey())
}

// Compaction is not supported by the Oruk provider.
func (provider *OrukProvider) Compaction(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Oruk provider.
func (provider *OrukProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Oruk provider.
func (provider *OrukProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Oruk provider.
func (provider *OrukProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Oruk provider.
func (provider *OrukProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Oruk provider.
func (provider *OrukProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Oruk provider.
func (provider *OrukProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Oruk provider.
func (provider *OrukProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Oruk provider.
func (provider *OrukProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Oruk provider.
func (provider *OrukProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the Oruk provider.
func (provider *OrukProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

// PassthroughStream is not supported by the Oruk provider.
func (provider *OrukProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}
