// Package byteplus implements the BytePlus ModelArk provider.
package byteplus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// BytePlusProvider implements the Provider interface for BytePlus ModelArk's API.
type BytePlusProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient     *fasthttp.Client      // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewBytePlusProvider creates a new BytePlus ModelArk provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
// The default base URL is the ap-southeast region; paths carry /api/v3, so another region
// (ark.eu-west.bytepluses.com) or Volcengine (ark.cn-beijing.volces.com) only changes the host.
func NewBytePlusProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*BytePlusProvider, error) {
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
		config.NetworkConfig.BaseURL = "https://ark.ap-southeast.bytepluses.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &BytePlusProvider{
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for BytePlus.
func (provider *BytePlusProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.BytePlus
}

// ListModels lists models for BytePlus from each key's configured models and aliases.
// Ark's data plane has no Bearer-authenticated /models endpoint (model discovery is a
// signed AK/SK control-plane API), so models are sourced from config, as Fireworks does.
func (provider *BytePlusProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(ctx, keys, request, provider.listModelsByKey)
}

// listModelsByKey builds the model list for a single key from its configured models and aliases.
// The filtered pipeline path is forced for the same reason as Fireworks: an unfiltered request
// skips config backfill and would otherwise return an empty catalog.
func (provider *BytePlusProvider) listModelsByKey(_ *schemas.BifrostContext, key schemas.Key, _ *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return (&openai.OpenAIListModelsResponse{}).ToBifrostListModelsResponse(
		schemas.BytePlus,
		key.Models,
		key.BlacklistedModels,
		key.Aliases,
		false,
	), nil
}

// TextCompletion is not supported by the BytePlus provider.
func (provider *BytePlusProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the BytePlus provider.
func (provider *BytePlusProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to Ark's OpenAI-compatible API.
// Extra params pass through so Ark-only fields such as thinking reach the wire.
func (provider *BytePlusProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/api/v3/chat/completions"),
		request,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to Ark's OpenAI-compatible API.
func (provider *BytePlusProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.streamingClient,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/api/v3/chat/completions"),
		request,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// Responses performs a request to Ark's native Responses API.
func (provider *BytePlusProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/api/v3/responses"),
		request,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		nil,
		provider.logger,
	)
}

// ResponsesStream performs a streaming request to Ark's native Responses API.
func (provider *BytePlusProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	return openai.HandleOpenAIResponsesStreaming(
		ctx,
		provider.streamingClient,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/api/v3/responses"),
		request,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		nil,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// doBytePlusRequest sends a JSON request to Ark and returns a copy of the decoded body of a 2xx response.
func (provider *BytePlusProvider) doBytePlusRequest(ctx *schemas.BifrostContext, key schemas.Key, method, path, rawQuery string, jsonData []byte, sendBackRawRequest, sendBackRawResponse bool) ([]byte, time.Duration, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	requestURL := provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, path)
	if rawQuery != "" {
		requestURL += "?" + rawQuery
	}
	req.SetRequestURI(requestURL)
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
		return nil, latency, providerUtils.EnrichError(ctx, openai.ParseOpenAIError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
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
	return append([]byte(nil), body...), latency, nil
}

// ImageGeneration performs a Seedream image generation request to Ark's /images/generations API.
// Extra params pass through so Seedream-only fields such as background reach the wire.
func (provider *BytePlusProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBytePlusImageGenerationRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return provider.doImageRequest(ctx, key, jsonData, request.Model)
}

// ImageEdit performs a Seedream image-to-image request. Seedream edits through the same
// /images/generations endpoint, with the source images sent as reference images.
func (provider *BytePlusProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBytePlusImageEditRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return provider.doImageRequest(ctx, key, jsonData, request.Model)
}

// doImageRequest posts a Seedream body and converts the response.
func (provider *BytePlusProvider) doImageRequest(ctx *schemas.BifrostContext, key schemas.Key, jsonData []byte, model string) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	body, latency, bifrostErr := provider.doBytePlusRequest(ctx, key, http.MethodPost, "/api/v3/images/generations", "", jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var bytePlusResp BytePlusImageGenerationResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, body, &bytePlusResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency)
	}

	bifrostResp, bifrostErr := ToBifrostImageGenerationResponse(&bytePlusResp, model)
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

// VideoGeneration creates a Seedance video generation task. Extra params pass through so Seedance
// task options such as draft, service_tier and return_last_frame reach the wire.
func (provider *BytePlusProvider) VideoGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBytePlusVideoGenerationRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	body, latency, bifrostErr := provider.doBytePlusRequest(ctx, key, http.MethodPost, "/api/v3/contents/generations/tasks", "", jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var taskResp BytePlusVideoTaskCreationResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, body, &taskResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency)
	}
	if taskResp.ID == "" {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("byteplus returned no task id", nil), jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp := &schemas.BifrostVideoGenerationResponse{
		ID:        providerUtils.AddVideoIDProviderSuffix(taskResp.ID, providerName),
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

// VideoRetrieve queries the status of a Seedance video generation task.
func (provider *BytePlusProvider) VideoRetrieve(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	taskID := providerUtils.StripVideoIDProviderSuffix(request.ID, providerName)
	escapedTaskID, idErr := providerUtils.EscapeResourceID(taskID, "video_id")
	if idErr != nil {
		return nil, idErr
	}

	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	body, latency, bifrostErr := provider.doBytePlusRequest(ctx, key, http.MethodGet, "/api/v3/contents/generations/tasks/"+escapedTaskID, "", nil, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var task BytePlusVideoTask
	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, body, &task, nil, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency)
	}

	bifrostResp, bifrostErr := ToBifrostVideoGenerationResponse(&task)
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

// VideoDownload fetches the generated video from the time-limited URL on a succeeded task.
func (provider *BytePlusProvider) VideoDownload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
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

// VideoDelete cancels a queued Seedance task or deletes the record of a finished one.
func (provider *BytePlusProvider) VideoDelete(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	taskID := providerUtils.StripVideoIDProviderSuffix(request.ID, provider.GetProviderKey())
	escapedTaskID, idErr := providerUtils.EscapeResourceID(taskID, "video_id")
	if idErr != nil {
		return nil, idErr
	}

	_, latency, bifrostErr := provider.doBytePlusRequest(ctx, key, http.MethodDelete, "/api/v3/contents/generations/tasks/"+escapedTaskID, "", nil, false, false)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResp := &schemas.BifrostVideoDeleteResponse{
		ID:      request.ID,
		Object:  "video.deleted",
		Deleted: true,
	}
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	return bifrostResp, nil
}

// VideoList lists Seedance tasks. Ark pages by number rather than by cursor, so only the first
// page of size limit is reachable; an after cursor is rejected instead of being silently ignored,
// which would make a paging client loop on the same page.
func (provider *BytePlusProvider) VideoList(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	if request.After != nil && *request.After != "" {
		return nil, providerUtils.NewBifrostBadRequestError("byteplus video list pages by number and does not support an after cursor")
	}
	values := url.Values{}
	values.Set("page_num", "1")
	if request.Limit != nil && *request.Limit > 0 {
		values.Set("page_size", strconv.Itoa(*request.Limit))
	}

	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	body, latency, bifrostErr := provider.doBytePlusRequest(ctx, key, http.MethodGet, "/api/v3/contents/generations/tasks", values.Encode(), nil, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	var listResp BytePlusVideoTaskListResponse
	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, body, &listResp, nil, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency)
	}

	providerName := provider.GetProviderKey()
	bifrostResp := &schemas.BifrostVideoListResponse{
		Object: "list",
		Data:   make([]schemas.VideoObject, 0, len(listResp.Items)),
	}
	for i := range listResp.Items {
		bifrostResp.Data = append(bifrostResp.Data, ToBifrostVideoObject(&listResp.Items[i], providerName))
	}
	if n := len(bifrostResp.Data); n > 0 {
		bifrostResp.FirstID = schemas.Ptr(bifrostResp.Data[0].ID)
		bifrostResp.LastID = schemas.Ptr(bifrostResp.Data[n-1].ID)
	}
	// Only page 1 is reachable (an after cursor is rejected above), so there is never a next page to offer.
	bifrostResp.HasMore = schemas.Ptr(false)
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResp, nil
}

// Speech is not supported by the BytePlus provider.
func (provider *BytePlusProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// Rerank is not supported by the BytePlus provider.
func (provider *BytePlusProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// Decision is not supported by the BytePlus provider.
func (provider *BytePlusProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.DecisionRequest, provider.GetProviderKey())
}

// OCR is not supported by the BytePlus provider.
func (provider *BytePlusProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the BytePlus provider.
func (provider *BytePlusProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the BytePlus provider.
func (provider *BytePlusProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the BytePlus provider.
func (provider *BytePlusProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGenerationStream streams a Seedream generation, one event per finished image. Ark streams
// only on Seedream 5.0 lite, 4.5 and 4.0; other models reject the stream field with a 400.
func (provider *BytePlusProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			req, err := ToBytePlusImageGenerationRequest(request)
			if req != nil {
				req.Stream = schemas.Ptr(true)
			}
			return req, err
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return provider.doImageStream(ctx, postHookRunner, postHookSpanFinalizer, key, jsonData, &schemas.BifrostRequest{ImageGenerationRequest: request},
		schemas.ImageGenerationEventTypePartial, schemas.ImageGenerationEventTypeCompleted)
}

// ImageEditStream streams a Seedream image-to-image generation through the same endpoint as ImageGenerationStream.
func (provider *BytePlusProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			req, err := ToBytePlusImageEditRequest(request)
			if req != nil {
				req.Stream = schemas.Ptr(true)
			}
			return req, err
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return provider.doImageStream(ctx, postHookRunner, postHookSpanFinalizer, key, jsonData, &schemas.BifrostRequest{ImageEditRequest: request},
		schemas.ImageEditEventTypePartial, schemas.ImageEditEventTypeCompleted)
}

// doImageStream posts a streaming Seedream body and relays its events. Every image arrives whole, so each
// becomes one partial chunk; Ark's usage-only completed event becomes the completed chunk, which repeats
// the last image the way other providers' completed chunks carry the final one. A failed image is skipped
// unless Ark reports an internal error, which ends the stream, or no image succeeds at all.
func (provider *BytePlusProvider) doImageStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, jsonData []byte, bifrostReq *schemas.BifrostRequest, partialType, completedType schemas.ImageEventType) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/api/v3/images/generations"))
	req.Header.SetContentType("application/json")
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if value := key.Value.GetValue(); value != "" {
		req.Header.Set("Authorization", "Bearer "+value)
	}
	req.SetBody(jsonData)

	startTime := time.Now()
	err := providerUtils.DoStreamingRequest(ctx, provider.streamingClient, req, resp)
	latency := time.Since(startTime)
	if err != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, latency)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
		}
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), latency)
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		return nil, providerUtils.EnrichError(ctx, openai.ParseOpenAIError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonData)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonData)
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
			providerUtils.ProcessAndSendNonSSEStreamError(ctx, postHookRunner, nonSSE, responseChan, provider.logger, postHookSpanFinalizer)
			return
		}
		sseReader := providerUtils.GetSSEDataReader(ctx, reader)

		sendError := func(bifrostErr *schemas.BifrostError) {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency), responseChan, provider.logger, postHookSpanFinalizer)
		}

		lastChunkTime := startTime
		chunkIndex := 0
		var last *schemas.BifrostImageGenerationStreamResponse
		var firstFailure *BytePlusError
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
					providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, provider.logger, postHookSpanFinalizer)
					return
				}
				break
			}

			var event BytePlusImageStreamEvent
			parseStart := time.Now()
			umErr := sonic.Unmarshal(data, &event)
			schemas.AddStreamParse(ctx, time.Since(parseStart))
			if umErr != nil {
				provider.logger.Warn("failed to parse byteplus image stream event: %v", umErr)
				continue
			}

			switch event.Type {
			case bytePlusImageStreamPartialSucceeded:
				chunk := &schemas.BifrostImageGenerationStreamResponse{
					Type:      partialType,
					Index:     chunkIndex,
					URL:       event.URL,
					B64JSON:   event.B64JSON,
					Size:      event.Size,
					CreatedAt: event.Created,
					ExtraFields: schemas.BifrostResponseExtraFields{
						ChunkIndex: chunkIndex,
						Latency:    time.Since(lastChunkTime).Milliseconds(),
					},
				}
				if event.ImageIndex != nil {
					chunk.Index = *event.ImageIndex
				}
				if sendBackRawResponse {
					chunk.ExtraFields.RawResponse = string(data)
				}
				lastChunkTime = time.Now()
				chunkIndex++
				last = chunk
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
					providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, chunk),
					responseChan, postHookSpanFinalizer)

			case bytePlusImageStreamPartialFailed:
				if event.Error != nil && event.Error.Code == bytePlusInternalServiceError {
					sendError(bytePlusImageStreamError(event.Error, true))
					return
				}
				if firstFailure == nil {
					firstFailure = event.Error
				}

			case bytePlusImageStreamCompleted:
				if last == nil {
					sendError(bytePlusImageStreamError(firstFailure, true))
					return
				}
				final := &schemas.BifrostImageGenerationStreamResponse{
					Type:      completedType,
					Index:     last.Index,
					URL:       last.URL,
					B64JSON:   last.B64JSON,
					Size:      last.Size,
					CreatedAt: event.Created,
					Usage:     ToBifrostImageUsage(event.Usage),
					ExtraFields: schemas.BifrostResponseExtraFields{
						ChunkIndex: chunkIndex,
						Latency:    time.Since(startTime).Milliseconds(),
					},
				}
				final.BackfillParams(bifrostReq)
				if sendBackRawRequest {
					providerUtils.ParseAndSetRawRequest(&final.ExtraFields, jsonData)
				}
				if sendBackRawResponse {
					final.ExtraFields.RawResponse = string(data)
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
					providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, final),
					responseChan, postHookSpanFinalizer)
				return

			default:
				if event.Error != nil && event.Error.Message != "" {
					sendError(bytePlusImageStreamError(event.Error, false))
					return
				}
			}
		}

		// The loop returns on completed, so falling out means the body ended before Ark finished.
		if ended, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool); !ended && !providerUtils.SSEStreamEndedOnMarker(sseReader) {
			providerUtils.SendStreamTruncatedError(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonData)
		}
	}()

	return responseChan, nil
}

// ImageVariation is not supported by the BytePlus provider.
func (provider *BytePlusProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoEdit is not supported by the BytePlus provider.
func (provider *BytePlusProvider) VideoEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoEditRequest) (*schemas.BifrostVideoEditResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoEditRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by the BytePlus provider.
func (provider *BytePlusProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by the BytePlus provider.
func (provider *BytePlusProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by the BytePlus provider.
func (provider *BytePlusProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by the BytePlus provider.
func (provider *BytePlusProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by the BytePlus provider.
func (provider *BytePlusProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchDelete is not supported by the BytePlus provider.
func (provider *BytePlusProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults is not supported by the BytePlus provider.
func (provider *BytePlusProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload is not supported by the BytePlus provider.
func (provider *BytePlusProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by the BytePlus provider.
func (provider *BytePlusProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by the BytePlus provider.
func (provider *BytePlusProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by the BytePlus provider.
func (provider *BytePlusProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by the BytePlus provider.
func (provider *BytePlusProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the BytePlus provider.
func (provider *BytePlusProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// ModelRetrieve is not supported by the BytePlus provider.
func (provider *BytePlusProvider) ModelRetrieve(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostModelRetrieveRequest) (*schemas.BifrostModelRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ModelRetrieveRequest, provider.GetProviderKey())
}

// Compaction is not supported by the BytePlus provider.
func (provider *BytePlusProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the BytePlus provider.
func (provider *BytePlusProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the BytePlus provider.
func (provider *BytePlusProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the BytePlus provider.
func (provider *BytePlusProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the BytePlus provider.
func (provider *BytePlusProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the BytePlus provider.
func (provider *BytePlusProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the BytePlus provider.
func (provider *BytePlusProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the BytePlus provider.
func (provider *BytePlusProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the BytePlus provider.
func (provider *BytePlusProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the BytePlus provider.
func (provider *BytePlusProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the BytePlus provider.
func (provider *BytePlusProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

// PassthroughStream is not supported by the BytePlus provider.
func (provider *BytePlusProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}
