// Package fishaudio implements the Bifrost provider for Fish Audio
// (https://fish.audio) text-to-speech.
//
// Synthesis runs over Fish's /v1/tts/live WebSocket, which exchanges
// MessagePack-encoded events: the client sends Start (config) + Text + Stop and
// the server streams audio frames until finish(stop). Both Speech (one-shot,
// accumulated) and SpeechStream (incremental deltas) drive this same WebSocket;
// the msgpack wire protocol is fully encapsulated here, so the rest of Bifrost
// treats Fish like any other TTS provider. ListModels uses the HTTP /model
// endpoint.
package fishaudio

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

type FishAudioProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for unary API requests (ListModels)
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawRequest   bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewFishAudioProvider creates a new Fish Audio provider instance.
func NewFishAudioProvider(config *schemas.ProviderConfig, logger schemas.Logger) *FishAudioProvider {
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

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = fishDefaultBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &FishAudioProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
	}
}

// GetProviderKey returns the provider identifier for Fish Audio.
func (provider *FishAudioProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.FishAudio, provider.customProviderConfig)
}

// resolveModel resolves the Fish model header value, defaulting to s2-pro.
func (provider *FishAudioProvider) resolveModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return fishDefaultModel
	}
	return model
}

// requestTimeout returns the configured per-request timeout (used for WS writes).
func (provider *FishAudioProvider) requestTimeout() time.Duration {
	timeout := time.Duration(provider.networkConfig.DefaultRequestTimeoutInSeconds) * time.Second
	if timeout <= 0 {
		return 30 * time.Second
	}
	return timeout
}

// readIdleTimeout bounds the wait for each audio frame from the WebSocket.
func (provider *FishAudioProvider) readIdleTimeout() time.Duration {
	if provider.networkConfig.StreamIdleTimeoutInSeconds > 0 {
		return time.Duration(provider.networkConfig.StreamIdleTimeoutInSeconds) * time.Second
	}
	return 30 * time.Second
}

// --- ListModels ---

// listModelsByKey lists voice models for a single key, paginating through
// Fish's /model endpoint.
//
// By default it lists the account's own voices (self=true), which is bounded.
// Callers can list public voices by passing self=false via ExtraParams (e.g.
// the query string ?self=false), and may narrow the (very large) public catalog
// with Fish filters — title, language, tag, sort_by — also forwarded from
// ExtraParams. Pagination is capped at schemas.MaxPaginationRequests pages.
func (provider *FishAudioProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	aggregated := &FishAudioListModelsResponse{}
	var totalLatency time.Duration
	var providerHeaders map[string]string

	// self defaults to "true" (own voices); callers override via ExtraParams.
	selfValue := "true"
	if request.ExtraParams != nil {
		if v, ok := request.ExtraParams["self"]; ok {
			selfValue = fmt.Sprintf("%v", v)
		}
	}

	for page := 1; page <= schemas.MaxPaginationRequests; page++ {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/model"))
		args := req.URI().QueryArgs()
		args.Set("self", selfValue)
		args.Set("page_size", strconv.Itoa(fishModelsPageSize))
		args.Set("page_number", strconv.Itoa(page))
		// Forward any other provider-specific filters (title, language, tag,
		// sort_by, ...) so callers can narrow a public listing.
		for k, v := range request.ExtraParams {
			if k == "self" {
				continue
			}
			args.Set(k, fmt.Sprintf("%v", v))
		}

		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")
		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return nil, bifrostErr
		}
		totalLatency += latency
		providerHeaders = providerUtils.ExtractProviderResponseHeaders(resp)
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerHeaders)

		if resp.StatusCode() != fasthttp.StatusOK {
			bifrostErr := parseFishError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return nil, bifrostErr
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		}

		var pageResponse FishAudioListModelsResponse
		if err := sonic.Unmarshal(body, &pageResponse); err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return nil, providerUtils.NewBifrostOperationError("failed to parse fish audio models response", err)
		}

		aggregated.Items = append(aggregated.Items, pageResponse.Items...)

		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		// Stop when the API reports no more pages or returns a short page.
		if !pageResponse.HasMore || len(pageResponse.Items) < fishModelsPageSize {
			break
		}
		// Cap reached but more remain (typical for public listings) — stop and
		// tell the caller how to narrow it.
		if page == schemas.MaxPaginationRequests {
			provider.logger.Warn("fish audio model list truncated at %d models (%d pages); narrow with title/language filters or use self=true",
				len(aggregated.Items), schemas.MaxPaginationRequests)
		}
	}

	response := aggregated.ToBifrostListModelsResponse(provider.GetProviderKey(), key.Models, key.BlacklistedModels, key.Aliases, request.Unfiltered)
	response.ExtraFields.Latency = totalLatency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerHeaders

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = aggregated
	}

	return response, nil
}

// ListModels lists the account's voice models, concurrently across keys.
func (provider *FishAudioProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.FishAudio, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	return providerUtils.HandleMultipleListModelsRequests(ctx, keys, request, provider.listModelsByKey)
}

// --- Speech ---

// Speech synthesizes the full utterance by driving the tts-live WebSocket to
// completion and concatenating the audio frames.
func (provider *FishAudioProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.FishAudio, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
		return nil, err
	}
	if request.Input == nil || strings.TrimSpace(request.Input.Input) == "" {
		return nil, providerUtils.NewBifrostOperationError("speech input text must not be empty", nil)
	}

	fishReq := ToFishAudioSpeechRequest(request)
	headers := provider.buildWSHeaders(key, provider.resolveModel(request.Model))
	wsURL := fishWebSocketURL(provider.networkConfig.BaseURL)

	startTime := time.Now()
	conn, bifrostErr := provider.dial(wsURL, headers)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	defer conn.Close()

	var audio []byte
	bifrostErr = runSynthesis(conn, fishReq, request.Input.Input, provider.requestTimeout(), provider.readIdleTimeout(), func(frame []byte) *schemas.BifrostError {
		audio = append(audio, frame...)
		return nil
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := &schemas.BifrostSpeechResponse{
		Audio: audio,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 time.Since(startTime).Milliseconds(),
			ProviderResponseHeaders: nil,
		},
	}
	return response, nil
}

// --- SpeechStream ---

// SpeechStream synthesizes incrementally, emitting each audio frame from the
// tts-live WebSocket as a delta and a final done marker.
func (provider *FishAudioProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.FishAudio, provider.customProviderConfig, schemas.SpeechStreamRequest); err != nil {
		return nil, err
	}
	if request.Input == nil || strings.TrimSpace(request.Input.Input) == "" {
		return nil, providerUtils.NewBifrostOperationError("speech input text must not be empty", nil)
	}

	fishReq := ToFishAudioSpeechRequest(request)
	headers := provider.buildWSHeaders(key, provider.resolveModel(request.Model))
	wsURL := fishWebSocketURL(provider.networkConfig.BaseURL)
	text := request.Input.Input

	startTime := time.Now()
	conn, bifrostErr := provider.dial(wsURL, headers)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	go func() {
		defer close(responseChan)
		defer conn.Close()
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)

		// Close the connection when the request context is cancelled/expires so
		// the blocking ReadMessage inside runSynthesis returns promptly.
		stopWatcher := make(chan struct{})
		defer close(stopWatcher)
		go func() {
			select {
			case <-ctx.Done():
				_ = conn.Close()
			case <-stopWatcher:
			}
		}()

		chunkIndex := -1
		lastChunkTime := time.Now()

		synthErr := runSynthesis(conn, fishReq, text, provider.requestTimeout(), provider.readIdleTimeout(), func(frame []byte) *schemas.BifrostError {
			chunkIndex++
			audioChunk := make([]byte, len(frame))
			copy(audioChunk, frame)

			response := &schemas.BifrostSpeechStreamResponse{
				Type:  schemas.SpeechStreamResponseTypeDelta,
				Audio: audioChunk,
				ExtraFields: schemas.BifrostResponseExtraFields{
					ChunkIndex: chunkIndex,
					Latency:    time.Since(lastChunkTime).Milliseconds(),
				},
			}
			lastChunkTime = time.Now()

			if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
				response.ExtraFields.RawResponse = audioChunk
			}

			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, response, nil, nil), responseChan, postHookSpanFinalizer)
			return nil
		})

		if synthErr != nil {
			// Prefer the context's own cause so logs reflect cancel/timeout
			// rather than the resulting socket read failure.
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, nil)
				return
			}
			if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, nil)
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, synthErr, responseChan, provider.logger, postHookSpanFinalizer)
			return
		}

		finalResponse := &schemas.BifrostSpeechStreamResponse{
			Type:  schemas.SpeechStreamResponseTypeDone,
			Audio: []byte{},
			ExtraFields: schemas.BifrostResponseExtraFields{
				ChunkIndex: chunkIndex + 1,
				Latency:    time.Since(startTime).Milliseconds(),
			},
		}
		// Populate usage (input char/byte counts) on the terminal chunk; unlike
		// the non-streaming path, streaming has no central BackfillParams call.
		finalResponse.BackfillParams(request)
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, finalResponse, nil, nil), responseChan, postHookSpanFinalizer)
	}()

	return responseChan, nil
}

// --- Unsupported operations ---

func (provider *FishAudioProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionStreamRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesStreamRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

func (provider *FishAudioProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}

// Compaction is not supported by the Fish Audio provider.
func (provider *FishAudioProvider) Compaction(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// Decision is not supported by the Fish Audio provider.
func (provider *FishAudioProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.DecisionRequest, provider.GetProviderKey())
}
