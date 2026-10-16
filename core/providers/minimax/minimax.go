// Package minimax implements the MiniMax provider.
package minimax

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// MiniMaxProvider implements the Provider interface for MiniMax's API.
type MiniMaxProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient     *fasthttp.Client      // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewMiniMaxProvider creates a new MiniMax provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewMiniMaxProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*MiniMaxProvider, error) {
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

	// Configure proxy and retry policy
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)
	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.minimax.io"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &MiniMaxProvider{
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for MiniMax.
func (provider *MiniMaxProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.MiniMax
}

// ListModels performs a list models request to MiniMax's API.
func (provider *MiniMaxProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return openai.HandleOpenAIListModelsRequest(
		ctx,
		provider.client,
		request,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/v1/models"),
		keys,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
	)
}

// TextCompletion is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to MiniMax's OpenAI-compatible API.
func (provider *MiniMaxProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/v1/chat/completions"),
		applyMiniMaxReasoningSplit(request),
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		miniMaxChatResponseHandler,
		parseMiniMaxError,
		nil,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to MiniMax's OpenAI-compatible API.
func (provider *MiniMaxProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.streamingClient,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/v1/chat/completions"),
		applyMiniMaxReasoningSplit(request),
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		parseMiniMaxError,
		nil,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// applyMiniMaxReasoningSplit asks MiniMax to return thinking in reasoning_content instead of inline <think> tags, unless the caller set reasoning_split.
func applyMiniMaxReasoningSplit(request *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	if request == nil {
		return request
	}
	var paramsCopy schemas.ChatParameters
	if request.Params != nil {
		if _, ok := request.Params.ExtraParams["reasoning_split"]; ok {
			return request
		}
		paramsCopy = *request.Params
	}
	extraParams := make(map[string]any, len(paramsCopy.ExtraParams)+1)
	maps.Copy(extraParams, paramsCopy.ExtraParams)
	extraParams["reasoning_split"] = true
	paramsCopy.ExtraParams = extraParams

	requestCopy := *request
	requestCopy.Params = &paramsCopy
	return &requestCopy
}

// miniMaxChatResponseHandler surfaces a non-zero base_resp on a 200 as an error before decoding the chat body.
func miniMaxChatResponseHandler(body []byte, response *schemas.BifrostChatResponse, requestBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) (interface{}, interface{}, *schemas.BifrostError) {
	if bifrostErr := miniMaxBaseRespErrorFromBody(body); bifrostErr != nil {
		return nil, nil, bifrostErr
	}
	return providerUtils.HandleProviderResponse(body, response, requestBody, sendBackRawRequest, sendBackRawResponse)
}

// doMiniMaxRequest sends a JSON request to MiniMax and returns a copy of the decoded body of a 2xx response.
func (provider *MiniMaxProvider) doMiniMaxRequest(ctx *schemas.BifrostContext, key schemas.Key, method, path string, jsonData []byte, sendBackRawRequest, sendBackRawResponse bool) ([]byte, time.Duration, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, path))
	req.Header.SetMethod(method)
	if value := key.Value.GetValue(); value != "" {
		req.Header.Set("Authorization", "Bearer "+value)
	}
	if jsonData != nil {
		req.Header.SetContentType("application/json")
		req.SetBody(jsonData)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, latency, bifrostErr
	}
	if resp.StatusCode() < fasthttp.StatusOK || resp.StatusCode() >= fasthttp.StatusMultipleChoices {
		return nil, latency, providerUtils.EnrichError(ctx, parseMiniMaxError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	ft, fh := providerUtils.StartPhaseSpan(ctx, "response-finalize")
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if ft != nil {
		if err != nil {
			ft.EndSpan(fh, schemas.SpanStatusError, err.Error())
		} else {
			ft.EndSpan(fh, schemas.SpanStatusOk, "")
		}
	}
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, latency, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}
	body = append([]byte(nil), body...)
	if bifrostErr := miniMaxBaseRespErrorFromBody(body); bifrostErr != nil {
		return nil, latency, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}
	return body, latency, nil
}

// ImageGeneration performs an image generation request to MiniMax's /v1/image_generation API.
func (provider *MiniMaxProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToMiniMaxImageGenerationRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	body, latency, bifrostErr := provider.doMiniMaxRequest(ctx, key, http.MethodPost, "/v1/image_generation", jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var minimaxResp MiniMaxImageGenerationResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, body, &minimaxResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency)
	}

	bifrostResp, bifrostErr := ToBifrostImageGenerationResponse(&minimaxResp, request.Model)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResp, nil
}

// VideoGeneration creates a MiniMax H3 video generation task.
func (provider *MiniMaxProvider) VideoGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToMiniMaxVideoGenerationRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	body, latency, bifrostErr := provider.doMiniMaxRequest(ctx, key, http.MethodPost, "/v2/video_generation", jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var taskResp MiniMaxVideoTaskCreationResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, body, &taskResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency)
	}
	if taskResp.TaskID == "" {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("minimax returned no task_id", nil), jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp := &schemas.BifrostVideoGenerationResponse{
		ID:        providerUtils.AddVideoIDProviderSuffix(taskResp.TaskID, providerName),
		Model:     request.Model,
		Object:    "video",
		Status:    schemas.VideoStatusQueued,
		CreatedAt: time.Now().Unix(),
	}
	if duration := providerUtils.GetJSONField(jsonData, "duration").Int(); duration > 0 {
		bifrostResp.Seconds = schemas.Ptr(strconv.FormatInt(duration, 10))
	}
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResp, nil
}

// VideoRetrieve queries the status of a MiniMax video generation task.
func (provider *MiniMaxProvider) VideoRetrieve(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	taskID := providerUtils.StripVideoIDProviderSuffix(request.ID, providerName)
	escapedTaskID, idErr := providerUtils.EscapeResourceID(taskID, "video_id")
	if idErr != nil {
		return nil, idErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	body, latency, bifrostErr := provider.doMiniMaxRequest(ctx, key, http.MethodGet, "/v2/query/video_generation/"+escapedTaskID, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var taskResp MiniMaxVideoTaskResponse
	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, body, &taskResp, nil, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency)
	}

	bifrostResp, bifrostErr := ToBifrostVideoGenerationResponse(taskResp.Task)
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency)
	}
	if bifrostResp.ID == "" {
		bifrostResp.ID = taskID
	}
	bifrostResp.ID = providerUtils.AddVideoIDProviderSuffix(bifrostResp.ID, providerName)
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResp, nil
}

// VideoDownload fetches the generated video from the time-limited URL on a completed task.
func (provider *MiniMaxProvider) VideoDownload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	task, bifrostErr := provider.VideoRetrieve(ctx, key, &schemas.BifrostVideoRetrieveRequest{Provider: request.Provider, ID: request.ID})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if task.Status != schemas.VideoStatusCompleted {
		return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("video not ready, current status: %s", task.Status), nil)
	}
	if len(task.Videos) == 0 || task.Videos[0].URL == nil || *task.Videos[0].URL == "" {
		return nil, providerUtils.NewBifrostOperationError("video URL not available", nil)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI(*task.Videos[0].URL)
	req.Header.SetMethod(http.MethodGet)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(fmt.Sprintf("failed to download video: HTTP %d", resp.StatusCode()), nil), latency)
	}
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), latency)
	}
	contentType := string(resp.Header.ContentType())
	if contentType == "" {
		contentType = "video/mp4"
	}
	bifrostResp := &schemas.BifrostVideoDownloadResponse{
		VideoID:     request.ID,
		Content:     append([]byte(nil), body...),
		ContentType: contentType,
	}
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	return bifrostResp, nil
}

// VideoDelete cancels a queued MiniMax video task or deletes a finished one.
func (provider *MiniMaxProvider) VideoDelete(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	if request.ID == "" {
		return nil, providerUtils.NewBifrostOperationError("video_id is required", nil)
	}
	taskID := providerUtils.StripVideoIDProviderSuffix(request.ID, provider.GetProviderKey())
	escapedTaskID, idErr := providerUtils.EscapeResourceID(taskID, "video_id")
	if idErr != nil {
		return nil, idErr
	}

	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	body, latency, bifrostErr := provider.doMiniMaxRequest(ctx, key, http.MethodDelete, "/v2/video_generation/"+escapedTaskID, nil, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var deleteResp MiniMaxVideoDeleteResponse
	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, body, &deleteResp, nil, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency)
	}

	bifrostResp := &schemas.BifrostVideoDeleteResponse{
		ID:      request.ID,
		Object:  "video.deleted",
		Deleted: true,
	}
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResp, nil
}

// Responses performs a responses request to the MiniMax API by bridging it through chat completions.
func (provider *MiniMaxProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	chatResponse, err := provider.ChatCompletion(ctx, key, request.ToChatRequest())
	if err != nil {
		return nil, err
	}

	response := chatResponse.ToBifrostResponsesResponse()

	return response, nil
}

// ResponsesStream performs a streaming responses request to the MiniMax API.
func (provider *MiniMaxProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return provider.ChatCompletionStream(
		ctx,
		postHookRunner,
		postHookSpanFinalizer,
		key,
		request.ToChatRequest(),
	)
}

// Embedding is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Rerank is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// Decision is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.DecisionRequest, provider.GetProviderKey())
}

// OCR is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoList is not supported by MiniMax provider.
func (provider *MiniMaxProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoEdit is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) VideoEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoEditRequest) (*schemas.BifrostVideoEditResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoEditRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by MiniMax provider.
func (provider *MiniMaxProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// FileUpload is not supported by MiniMax provider.
func (provider *MiniMaxProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by MiniMax provider.
func (provider *MiniMaxProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by MiniMax provider.
func (provider *MiniMaxProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by MiniMax provider.
func (provider *MiniMaxProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by MiniMax provider.
func (provider *MiniMaxProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by MiniMax provider.
func (provider *MiniMaxProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by MiniMax provider.
func (provider *MiniMaxProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by MiniMax provider.
func (provider *MiniMaxProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by MiniMax provider.
func (provider *MiniMaxProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchDelete is not supported by MiniMax provider.
func (provider *MiniMaxProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults is not supported by MiniMax provider.
func (provider *MiniMaxProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// ModelRetrieve is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ModelRetrieve(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostModelRetrieveRequest) (*schemas.BifrostModelRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ModelRetrieveRequest, provider.GetProviderKey())
}

// Compaction is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the MiniMax provider.
func (provider *MiniMaxProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

func (provider *MiniMaxProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}

// Speech performs a text-to-speech request to MiniMax's /v1/t2a_v2 API.
func (provider *MiniMaxProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToMiniMaxSpeechRequest(request, false)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	body, latency, bifrostErr := provider.doMiniMaxRequest(ctx, key, http.MethodPost, "/v1/t2a_v2", jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var minimaxResp MiniMaxSpeechResponse
	if err := sonic.Unmarshal(body, &minimaxResp); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err), jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}
	bifrostResp, bifrostErr := ToBifrostSpeechResponse(&minimaxResp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}
	if bifrostResp.Usage == nil {
		bifrostResp.BackfillParams(request)
	}
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawRequest {
		providerUtils.ParseAndSetRawRequest(&bifrostResp.ExtraFields, jsonData)
	}
	if sendBackRawResponse {
		// The hex audio would dominate the raw payload, so it is left out.
		minimaxResp.Data.Audio = ""
		bifrostResp.ExtraFields.RawResponse = minimaxResp
	}
	return bifrostResp, nil
}

// SpeechStream performs a streaming text-to-speech request to MiniMax's /v1/t2a_v2 API.
// Status-1 chunks carry incremental hex audio; the status-2 chunk carries usage and, with exclude_aggregated_audio, no audio.
func (provider *MiniMaxProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToMiniMaxSpeechRequest(request, true)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return provider.streamMiniMaxSSE(ctx, postHookRunner, postHookSpanFinalizer, key, "/v1/t2a_v2", "application/json", nil, jsonData, jsonData,
		func(data []byte, ef schemas.BifrostResponseExtraFields) (*schemas.BifrostResponse, bool, *schemas.BifrostError) {
			var chunk MiniMaxSpeechResponse
			if err := sonic.Unmarshal(data, &chunk); err != nil {
				return nil, false, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
			}
			if bifrostErr := miniMaxBaseRespError(chunk.BaseResp); bifrostErr != nil {
				return nil, true, bifrostErr
			}
			if chunk.Data == nil {
				return nil, false, nil
			}
			if chunk.Data.Status == 2 {
				done := &schemas.BifrostSpeechStreamResponse{Type: schemas.SpeechStreamResponseTypeDone, Audio: []byte{}, Usage: miniMaxSpeechUsage(&chunk), ExtraFields: ef}
				if done.Usage == nil {
					done.BackfillParams(request)
				}
				return providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, done, nil, nil), true, nil
			}
			if chunk.Data.Audio == "" {
				return nil, false, nil
			}
			audio, err := hex.DecodeString(chunk.Data.Audio)
			if err != nil {
				return nil, true, providerUtils.NewBifrostOperationError("failed to decode minimax hex audio", err)
			}
			delta := &schemas.BifrostSpeechStreamResponse{Type: schemas.SpeechStreamResponseTypeDelta, Audio: audio, ExtraFields: ef}
			return providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, delta, nil, nil), false, nil
		})
}

// Transcription performs a speech-to-text request to MiniMax's /v1/speech_to_text API.
func (provider *MiniMaxProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	minimaxReq, err := toMiniMaxTranscriptionRequest(request, false)
	if err != nil {
		return nil, providerUtils.NewBifrostBadRequestError(err.Error())
	}
	body, contentType, bifrostErr := buildMiniMaxTranscriptionBody(minimaxReq)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	respBody, latency, bifrostErr := provider.doMiniMaxMultipartRequest(ctx, key, "/v1/speech_to_text", contentType, minimaxReq.Language, body, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var bifrostResp *schemas.BifrostTranscriptionResponse
	var rawResponse interface{} = string(respBody)
	if minimaxReq.ResponseFormat == "srt" || minimaxReq.ResponseFormat == "vtt" {
		bifrostResp = &schemas.BifrostTranscriptionResponse{Text: string(respBody), ResponseFormat: schemas.Ptr(minimaxReq.ResponseFormat)}
	} else {
		var minimaxResp MiniMaxTranscriptionResponse
		if err := sonic.Unmarshal(respBody, &minimaxResp); err != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err), nil, respBody, false, sendBackRawResponse, latency)
		}
		bifrostResp = ToBifrostTranscriptionResponse(&minimaxResp, minimaxReq.TimestampLevel)
		rawResponse = minimaxResp
		bifrostResp.BackfillParams(request)
	}
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResp, nil
}

// TranscriptionStream performs a streaming speech-to-text request; MiniMax streams {index, delta, finish} events.
func (provider *MiniMaxProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	minimaxReq, err := toMiniMaxTranscriptionRequest(request, true)
	if err != nil {
		return nil, providerUtils.NewBifrostBadRequestError(err.Error())
	}
	body, contentType, bifrostErr := buildMiniMaxTranscriptionBody(minimaxReq)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	var headers map[string]string
	if minimaxReq.Language != "" {
		headers = map[string]string{"language": minimaxReq.Language}
	}

	var text strings.Builder
	return provider.streamMiniMaxSSE(ctx, postHookRunner, postHookSpanFinalizer, key, "/v1/speech_to_text", contentType, headers, body, nil,
		func(data []byte, ef schemas.BifrostResponseExtraFields) (*schemas.BifrostResponse, bool, *schemas.BifrostError) {
			var event MiniMaxTranscriptionStreamEvent
			if err := sonic.Unmarshal(data, &event); err != nil {
				return nil, false, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
			}
			text.WriteString(event.Delta)
			if !event.Finish {
				delta := &schemas.BifrostTranscriptionStreamResponse{Type: schemas.TranscriptionStreamResponseTypeDelta, Delta: schemas.Ptr(event.Delta), ExtraFields: ef}
				return providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, delta, nil), false, nil
			}
			// The final event can still carry text, so it is folded into the done event's full text.
			done := &schemas.BifrostTranscriptionStreamResponse{Type: schemas.TranscriptionStreamResponseTypeDone, Text: text.String(), ExtraFields: ef}
			if event.Delta != "" {
				done.Delta = schemas.Ptr(event.Delta)
			}
			if event.Duration != nil {
				done.Usage = &schemas.TranscriptionUsage{Type: "duration", Seconds: event.Duration}
			}
			return providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, done, nil), true, nil
		})
}

func buildMiniMaxTranscriptionBody(req *miniMaxTranscriptionRequest) ([]byte, string, *schemas.BifrostError) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if bifrostErr := writeMiniMaxTranscriptionMultipart(writer, req); bifrostErr != nil {
		return nil, "", bifrostErr
	}
	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return nil, "", providerUtils.NewBifrostOperationError("failed to finalize multipart transcription request", err)
	}
	return body.Bytes(), contentType, nil
}

// doMiniMaxMultipartRequest posts a multipart body and returns a copy of the decoded body of a 2xx response.
func (provider *MiniMaxProvider) doMiniMaxMultipartRequest(ctx *schemas.BifrostContext, key schemas.Key, path, contentType, language string, body []byte, sendBackRawResponse bool) ([]byte, time.Duration, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, path))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(contentType)
	if value := key.Value.GetValue(); value != "" {
		req.Header.Set("Authorization", "Bearer "+value)
	}
	if language != "" {
		req.Header.Set("language", language)
	}
	req.SetBody(body)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, latency, bifrostErr
	}
	if resp.StatusCode() < fasthttp.StatusOK || resp.StatusCode() >= fasthttp.StatusMultipleChoices {
		return nil, latency, providerUtils.EnrichError(ctx, parseMiniMaxError(resp), nil, nil, false, sendBackRawResponse, latency)
	}
	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, latency, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), nil, nil, false, sendBackRawResponse, latency)
	}
	respBody = append([]byte(nil), respBody...)
	if bifrostErr := miniMaxBaseRespErrorFromBody(respBody); bifrostErr != nil {
		return nil, latency, providerUtils.EnrichError(ctx, bifrostErr, nil, respBody, false, sendBackRawResponse, latency)
	}
	return respBody, latency, nil
}

// miniMaxStreamHandler turns one SSE data payload into a Bifrost stream response; done ends the stream.
type miniMaxStreamHandler func(data []byte, extraFields schemas.BifrostResponseExtraFields) (resp *schemas.BifrostResponse, done bool, bifrostErr *schemas.BifrostError)

// streamMiniMaxSSE posts body to path and feeds each SSE data line to handle. rawRequest is the JSON body to echo
// back when raw requests are on (nil for multipart). A 200 that is not SSE but carries a non-zero base_resp
// is reported as that MiniMax error rather than a generic non-SSE failure.
func (provider *MiniMaxProvider) streamMiniMaxSSE(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, path, contentType string, headers map[string]string, body, rawRequest []byte, handle miniMaxStreamHandler) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) && rawRequest != nil
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, path))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(contentType)
	req.Header.Set("Accept", "text/event-stream")
	if value := key.Value.GetValue(); value != "" {
		req.Header.Set("Authorization", "Bearer "+value)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	req.SetBody(body)

	startTime := time.Now()
	err := providerUtils.DoStreamingRequest(ctx, provider.streamingClient, req, resp)
	latency := time.Since(startTime)
	if err != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				Error: &schemas.ErrorField{Type: schemas.Ptr(schemas.RequestCancelled), Message: schemas.ErrRequestCancelled, Error: err},
			}, rawRequest, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), rawRequest, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), rawRequest, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		return nil, providerUtils.EnrichError(ctx, parseMiniMaxError(resp), rawRequest, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, rawRequest)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, rawRequest)
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

		reader, nonSSE := providerUtils.DrainNonSSEStreamReader(resp, reader)
		if nonSSE != nil {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			if bifrostErr := miniMaxBaseRespErrorFromBody(nonSSE.Sample); bifrostErr != nil {
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, rawRequest, nonSSE.Sample, sendBackRawRequest, sendBackRawResponse, latency), responseChan, provider.logger, postHookSpanFinalizer)
				return
			}
			providerUtils.ProcessAndSendNonSSEStreamError(ctx, postHookRunner, nonSSE, responseChan, provider.logger, postHookSpanFinalizer)
			return
		}

		sseReader := providerUtils.GetSSEDataReader(ctx, reader)
		chunkIndex := -1
		lastChunkTime := startTime
		for {
			if ctx.Err() != nil {
				return
			}
			data, readErr := sseReader.ReadDataLine()
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				if readErr != io.EOF {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					provider.logger.Warn("Error reading minimax stream: %v", readErr)
					providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, provider.logger, postHookSpanFinalizer)
				}
				break
			}

			chunkIndex++
			ef := schemas.BifrostResponseExtraFields{ChunkIndex: chunkIndex, Latency: time.Since(lastChunkTime).Milliseconds()}
			if sendBackRawResponse {
				ef.RawResponse = string(data)
			}
			parseStart := time.Now()
			bifrostResp, done, bifrostErr := handle(data, ef)
			schemas.AddStreamParse(ctx, time.Since(parseStart))
			if bifrostErr != nil {
				if !done {
					provider.logger.Warn("Failed to parse minimax stream chunk: %v", bifrostErr.Error.Message)
					continue
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, rawRequest, nil, sendBackRawRequest, sendBackRawResponse, latency), responseChan, provider.logger, postHookSpanFinalizer)
				return
			}
			if bifrostResp == nil {
				chunkIndex--
				continue
			}
			lastChunkTime = time.Now()
			if done {
				setMiniMaxFinalStreamFields(bifrostResp, time.Since(startTime), sendBackRawRequest, rawRequest)
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, bifrostResp, responseChan, postHookSpanFinalizer)
				return
			}
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, bifrostResp, responseChan, postHookSpanFinalizer)
		}

		// A healthy MiniMax stream ends on its final (status 2 / finish) event, so falling out of the loop means it was cut short.
		if ended, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool); !ended {
			providerUtils.SendStreamTruncatedError(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, rawRequest)
		}
	}()
	return responseChan, nil
}

// setMiniMaxFinalStreamFields stamps total latency and the raw request on the terminal stream event.
func setMiniMaxFinalStreamFields(resp *schemas.BifrostResponse, total time.Duration, sendBackRawRequest bool, rawRequest []byte) {
	var ef *schemas.BifrostResponseExtraFields
	switch {
	case resp.SpeechStreamResponse != nil:
		ef = &resp.SpeechStreamResponse.ExtraFields
	case resp.TranscriptionStreamResponse != nil:
		ef = &resp.TranscriptionStreamResponse.ExtraFields
	default:
		return
	}
	ef.Latency = total.Milliseconds()
	if sendBackRawRequest {
		providerUtils.ParseAndSetRawRequest(ef, rawRequest)
	}
}
