package deepgram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type DeepgramProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient      *fasthttp.Client              // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawRequest   bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewDeepgramProvider creates a new Deepgram provider instance.
func NewDeepgramProvider(config *schemas.ProviderConfig, logger schemas.Logger) *DeepgramProvider {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: 30 * time.Second,
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}

	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.deepgram.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &DeepgramProvider{
		logger:               logger,
		client:               client,
		streamingClient:      streamingClient,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Deepgram.
func (provider *DeepgramProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Deepgram, provider.customProviderConfig)
}

// setAuthHeader sets Deepgram's Authorization: Token <key> header.
func (provider *DeepgramProvider) setAuthHeader(req *fasthttp.Request, key schemas.Key) {
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Token "+key.Value.GetValue())
	}
}

// buildRequestURL joins the provider's base URL with a default path (honoring
// custom-provider path overrides) and appends the given query params.
func (provider *DeepgramProvider) buildRequestURL(ctx *schemas.BifrostContext, defaultPath string, requestType schemas.RequestType, query url.Values) string {
	requestPath, isCompleteURL := providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)

	var finalURL string
	if isCompleteURL {
		finalURL = requestPath
	} else {
		u, parseErr := url.Parse(provider.networkConfig.BaseURL)
		if parseErr != nil {
			finalURL = provider.networkConfig.BaseURL + requestPath
		} else {
			u.Path = path.Join(u.Path, requestPath)
			finalURL = u.String()
		}
	}

	u, parseErr := url.Parse(finalURL)
	if parseErr != nil {
		return finalURL
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

// ListModels is not supported by the Deepgram provider.
// Deepgram's GET /v1/models response schema hasn't been verified against
// Bifrost's ListModelsPipeline yet; deferred rather than guessed.
func (provider *DeepgramProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ListModelsRequest, provider.GetProviderKey())
}

// TextCompletion is not supported by the Deepgram provider.
func (provider *DeepgramProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the Deepgram provider.
func (provider *DeepgramProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, provider.GetProviderKey())
}

// ChatCompletionStream is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionStreamRequest, provider.GetProviderKey())
}

// Responses is not supported by the Deepgram provider.
func (provider *DeepgramProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesRequest, provider.GetProviderKey())
}

// ResponsesStream is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesStreamRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the Deepgram provider.
func (provider *DeepgramProvider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// Compaction is not supported by the Deepgram provider.
func (provider *DeepgramProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// Embedding is not supported by the Deepgram provider.
func (provider *DeepgramProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Rerank is not supported by the Deepgram provider.
func (provider *DeepgramProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// OCR is not supported by the Deepgram provider.
func (provider *DeepgramProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// Speech performs a text-to-speech request against Deepgram's POST /v1/speak.
func (provider *DeepgramProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Deepgram, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
		return nil, err
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	requestURL := provider.buildRequestURL(ctx, "/v1/speak", schemas.SpeechRequest, BuildSpeakQueryParams(request))
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	provider.setAuthHeader(req, key)

	speakBody := ToDeepgramSpeakRequest(request)
	if speakBody == nil {
		return nil, providerUtils.NewBifrostOperationError("speech input text is required", nil)
	}
	body, err := sonic.Marshal(speakBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to marshal speak request", err)
	}

	if !providerUtils.ApplyLargePayloadRequestBody(ctx, req) {
		req.SetBody(body)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, body, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseDeepgramError(resp), body, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), body, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	bifrostResponse := &schemas.BifrostSpeechResponse{
		Audio: responseBody,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
		},
	}

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, body)
	}

	return bifrostResponse, nil
}

// SpeechStream performs a streaming text-to-speech request against Deepgram's POST /v1/speak.
func (provider *DeepgramProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Deepgram, provider.customProviderConfig, schemas.SpeechStreamRequest); err != nil {
		return nil, err
	}

	speakBody := ToDeepgramSpeakRequest(request)
	if speakBody == nil {
		return nil, providerUtils.NewBifrostOperationError("speech input text is required", nil)
	}
	jsonBody, err := sonic.Marshal(speakBody)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to marshal speak request", err)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	requestURL := provider.buildRequestURL(ctx, "/v1/speak", schemas.SpeechStreamRequest, BuildSpeakQueryParams(request))
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	provider.setAuthHeader(req, key)

	if !providerUtils.ApplyLargePayloadRequestBody(ctx, req) {
		req.SetBody(jsonBody)
	}

	startTime := time.Now()
	doErr := provider.streamingClient.Do(req, resp)
	latency := time.Since(startTime)
	if doErr != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(doErr, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   doErr,
				},
			}, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
		}
		if errors.Is(doErr, fasthttp.ErrTimeout) || errors.Is(doErr, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, doErr), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, doErr), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, providerUtils.EnrichError(ctx, parseDeepgramError(resp), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonBody)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonBody)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)

		reader, releaseGzip := providerUtils.DecompressStreamBody(resp)
		defer releaseGzip()

		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(reader, resp.BodyStream(), providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), provider.logger)
		defer stopCancellation()
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)

		buffer := make([]byte, 4096)
		bodyStream := reader
		chunkIndex := -1
		lastChunkTime := time.Now()

		for {
			if ctx.Err() != nil {
				return
			}
			n, readErr := bodyStream.Read(buffer)
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				if readErr == io.EOF {
					break
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("Error reading stream: %v", readErr)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, provider.logger, postHookSpanFinalizer)
				return
			}

			if n > 0 {
				chunkIndex++
				audioChunk := make([]byte, n)
				copy(audioChunk, buffer[:n])

				chunkResponse := &schemas.BifrostSpeechStreamResponse{
					Type:  schemas.SpeechStreamResponseTypeDelta,
					Audio: audioChunk,
					ExtraFields: schemas.BifrostResponseExtraFields{
						ChunkIndex: chunkIndex,
						Latency:    time.Since(lastChunkTime).Milliseconds(),
					},
				}
				lastChunkTime = time.Now()

				if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
					chunkResponse.ExtraFields.RawResponse = audioChunk
				}

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, chunkResponse, nil, nil), responseChan, postHookSpanFinalizer)
			}
		}

		finalResponse := &schemas.BifrostSpeechStreamResponse{
			Type:  schemas.SpeechStreamResponseTypeDone,
			Audio: []byte{},
			ExtraFields: schemas.BifrostResponseExtraFields{
				ChunkIndex: chunkIndex + 1,
				Latency:    time.Since(startTime).Milliseconds(),
			},
		}
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			providerUtils.ParseAndSetRawRequest(&finalResponse.ExtraFields, jsonBody)
		}
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, finalResponse, nil, nil), responseChan, postHookSpanFinalizer)
	}()

	return responseChan, nil
}

// Transcription performs a transcription request against Deepgram's POST /v1/listen.
// Unlike OpenAI/ElevenLabs, Deepgram's REST Listen endpoint does not accept
// multipart/form-data: it expects either raw audio bytes with an audio/* Content-Type,
// or (when no file is present) a JSON {"url": "..."} body. Routing Deepgram through
// Bifrost's OpenAI-shaped multipart transcription handler is what produces the 400
// reported in issue #4414 — this converter avoids multipart entirely.
//
// Note: Bifrost's built-in /v1/audio/transcriptions HTTP handler currently requires a
// `file` form field unconditionally (transports/bifrost-http/handlers/inference.go),
// so the URL-body branch below is only reachable via direct BifrostTranscriptionRequest
// construction (e.g. SDK/library usage), not through that HTTP endpoint today.
func (provider *DeepgramProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Deepgram, provider.customProviderConfig, schemas.TranscriptionRequest); err != nil {
		return nil, err
	}

	if request == nil || request.Input == nil {
		return nil, providerUtils.NewBifrostOperationError("transcription request is not provided", nil)
	}

	hasFile := len(request.Input.File) > 0

	var audioURL string
	if !hasFile && request.Params != nil && request.Params.ExtraParams != nil {
		if u, ok := schemas.SafeExtractStringPointer(request.Params.ExtraParams["url"]); ok && strings.TrimSpace(*u) != "" {
			audioURL = *u
		}
	}

	if !hasFile && audioURL == "" {
		return nil, providerUtils.NewBifrostOperationError("either an audio file or a url must be provided", nil)
	}

	var body []byte
	var contentType string
	if hasFile {
		body = request.Input.File
		contentType = providerUtils.DetectAudioMimeType(request.Input.File)
	} else {
		urlBody, err := sonic.Marshal(DeepgramTranscribeURLRequest{URL: audioURL})
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to marshal transcription url request", err)
		}
		body = urlBody
		contentType = "application/json"
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	requestURL := provider.buildRequestURL(ctx, "/v1/listen", schemas.TranscriptionRequest, BuildTranscriptionQueryParams(request))
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(contentType)
	provider.setAuthHeader(req, key)
	req.SetBody(body)

	// Raw-request diagnostics (EnrichError/ParseAndSetRawRequest) store the
	// body as json.RawMessage, so only the JSON (URL-based) body is safe to
	// pass through — raw audio bytes (the file-upload path) are not valid
	// JSON and must never be embedded in a JSON response field.
	var rawRequestForDiagnostics []byte
	if contentType == "application/json" {
		rawRequestForDiagnostics = body
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, rawRequestForDiagnostics, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseDeepgramError(resp), rawRequestForDiagnostics, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), rawRequestForDiagnostics, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	var deepgramResp DeepgramTranscriptionResponse
	if err := sonic.Unmarshal(responseBody, &deepgramResp); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to parse Deepgram transcription response", err), rawRequestForDiagnostics, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	response := ToBifrostTranscriptionResponse(&deepgramResp)
	response.ExtraFields = schemas.BifrostResponseExtraFields{
		Latency:                 latency.Milliseconds(),
		ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
	}

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) && rawRequestForDiagnostics != nil {
		providerUtils.ParseAndSetRawRequest(&response.ExtraFields, rawRequestForDiagnostics)
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err != nil {
			rawResponse = string(responseBody)
		}
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// TranscriptionStream is not supported by the Deepgram provider.
// (Deepgram's realtime transcription is a WebSocket protocol, not an SSE/chunked
// REST stream — see the Voice Agent RealtimeProvider implementation in realtime.go.)
func (provider *DeepgramProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoGeneration is not supported by the Deepgram provider.
func (provider *DeepgramProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, provider.GetProviderKey())
}

// VideoRetrieve is not supported by the Deepgram provider.
func (provider *DeepgramProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, provider.GetProviderKey())
}

// VideoDownload is not supported by the Deepgram provider.
func (provider *DeepgramProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}

// VideoDelete is not supported by the Deepgram provider.
func (provider *DeepgramProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by the Deepgram provider.
func (provider *DeepgramProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by the Deepgram provider.
func (provider *DeepgramProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by the Deepgram provider.
func (provider *DeepgramProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by the Deepgram provider.
func (provider *DeepgramProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by the Deepgram provider.
func (provider *DeepgramProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by the Deepgram provider.
func (provider *DeepgramProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchDelete is not supported by the Deepgram provider.
func (provider *DeepgramProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults is not supported by the Deepgram provider.
func (provider *DeepgramProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload is not supported by the Deepgram provider.
func (provider *DeepgramProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by the Deepgram provider.
func (provider *DeepgramProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by the Deepgram provider.
func (provider *DeepgramProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by the Deepgram provider.
func (provider *DeepgramProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by the Deepgram provider.
func (provider *DeepgramProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// CachedContentCreate is not supported by the Deepgram provider.
func (provider *DeepgramProvider) CachedContentCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCachedContentCreateRequest) (*schemas.BifrostCachedContentCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentCreateRequest, provider.GetProviderKey())
}

// CachedContentList is not supported by the Deepgram provider.
func (provider *DeepgramProvider) CachedContentList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentListRequest) (*schemas.BifrostCachedContentListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentListRequest, provider.GetProviderKey())
}

// CachedContentRetrieve is not supported by the Deepgram provider.
func (provider *DeepgramProvider) CachedContentRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentRetrieveRequest) (*schemas.BifrostCachedContentRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentRetrieveRequest, provider.GetProviderKey())
}

// CachedContentUpdate is not supported by the Deepgram provider.
func (provider *DeepgramProvider) CachedContentUpdate(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentUpdateRequest) (*schemas.BifrostCachedContentUpdateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentUpdateRequest, provider.GetProviderKey())
}

// CachedContentDelete is not supported by the Deepgram provider.
func (provider *DeepgramProvider) CachedContentDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentDeleteRequest) (*schemas.BifrostCachedContentDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentDeleteRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Deepgram provider.
func (provider *DeepgramProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the Deepgram provider.
func (provider *DeepgramProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

// PassthroughStream is not supported by the Deepgram provider.
func (provider *DeepgramProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}
