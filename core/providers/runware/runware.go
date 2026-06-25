// Package runware implements the Runware provider for Bifrost.
// Runware exposes a single synchronous endpoint that accepts an array of tasks; this
// provider supports its image operations (text-to-image, image-to-image, inpainting, outpainting),
// all of which use the "imageInference" task type. It also exposes an OpenAI-compatible LLM
// API, wired up via the shared OpenAI handlers (chat, streaming, model listing).
package runware

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// RunwareProvider implements the Provider interface for Runware's API.
type RunwareProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for unary API requests
	streamingClient     *fasthttp.Client      // HTTP client for streaming chat completions
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewRunwareProvider creates a new Runware provider instance.
func NewRunwareProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*RunwareProvider, error) {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: 60 * time.Second, // Image generation can be slow; keep connections warm longer.
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}

	// Configure proxy if provided
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)

	// Set default BaseURL if not provided. Runware's single endpoint already includes /v1.
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.runware.ai/v1"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	streamingClient := providerUtils.BuildStreamingClient(client)

	return &RunwareProvider{
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Runware.
func (provider *RunwareProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Runware
}

// runwareModelEntry embeds schemas.Model and additionally captures Runware's top-level
// input/output modalities, which schemas.Model nests under Architecture.
type runwareModelEntry struct {
	schemas.Model
	InputModalities  []string `json:"input_modalities,omitempty"`
	OutputModalities []string `json:"output_modalities,omitempty"`
}

// runwareModelsResponse is the envelope returned by Runware's /v1/models.
type runwareModelsResponse struct {
	Data []runwareModelEntry `json:"data"`
}

// listModelsByKey fetches Runware's /v1/models and maps the per-model pricing, context, and modality metadata onto the Bifrost model schema.
func (provider *RunwareProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, "/models"))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if keyValue := key.Value.GetValue(); keyValue != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", keyValue))
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, openai.ParseOpenAIError(resp)
	}

	responseBody := append([]byte(nil), resp.Body()...)

	var runwareResp runwareModelsResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, &runwareResp, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	response := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(runwareResp.Data)),
	}
	for i := range runwareResp.Data {
		model := runwareResp.Data[i].Model
		if len(runwareResp.Data[i].InputModalities) > 0 || len(runwareResp.Data[i].OutputModalities) > 0 {
			model.Architecture = &schemas.Architecture{
				InputModalities:  runwareResp.Data[i].InputModalities,
				OutputModalities: runwareResp.Data[i].OutputModalities,
			}
		}
		response.Data = append(response.Data, model)
	}

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	// Runware returns model IDs without the "runware/" prefix; strip it from allowed/blacklist/alias entries before filtering, then re-add it.
	providerPrefix := string(schemas.Runware) + "/"
	stripPrefix := func(s string) string {
		if strings.HasPrefix(strings.ToLower(s), strings.ToLower(providerPrefix)) {
			return s[len(providerPrefix):]
		}
		return s
	}

	normalizedAllowed := make(schemas.WhiteList, 0, len(key.Models))
	for _, m := range key.Models {
		normalizedAllowed = append(normalizedAllowed, stripPrefix(m))
	}
	normalizedBlacklist := make(schemas.BlackList, 0, len(key.BlacklistedModels))
	for _, m := range key.BlacklistedModels {
		normalizedBlacklist = append(normalizedBlacklist, stripPrefix(m))
	}
	normalizedAliases := make(schemas.KeyAliases, len(key.Aliases))
	for k, v := range key.Aliases {
		cfg := v
		cfg.ModelID = stripPrefix(v.ModelID)
		normalizedAliases[stripPrefix(k)] = cfg
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     normalizedAllowed,
		BlacklistedModels: normalizedBlacklist,
		Aliases:           normalizedAliases,
		Unfiltered:        request.Unfiltered,
		ProviderKey:       schemas.Runware,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}

	if pipeline.ShouldEarlyExit() {
		response.Data = make([]schemas.Model, 0)
	} else {
		included := make(map[string]bool)
		filteredData := make([]schemas.Model, 0, len(response.Data))
		for i := range response.Data {
			rawID := response.Data[i].ID
			for _, result := range pipeline.FilterModel(rawID) {
				entry := response.Data[i]
				entry.ID = providerPrefix + result.ResolvedID
				if result.AliasValue != "" {
					entry.Alias = schemas.Ptr(result.AliasValue)
				} else {
					entry.Alias = nil
				}
				filteredData = append(filteredData, entry)
				included[strings.ToLower(result.ResolvedID)] = true
			}
		}
		filteredData = append(filteredData, pipeline.BackfillModels(included)...)
		response.Data = filteredData
	}

	response.ExtraFields.Latency = latency.Milliseconds()

	return response, nil
}

// ListModels lists Runware's models via /v1/models, fanning out across the provided keys.
func (provider *RunwareProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}

// TextCompletion is not supported by the Runware provider.
func (provider *RunwareProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the Runware provider.
func (provider *RunwareProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to Runware's OpenAI-compatible API.
func (provider *RunwareProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, "/chat/completions"),
		request,
		key,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		nil,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to Runware's OpenAI-compatible API.
func (provider *RunwareProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	var authHeader map[string]string
	if v := key.Value.GetValue(); v != "" {
		authHeader = map[string]string{"Authorization": "Bearer " + v}
	}
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.streamingClient,
		provider.networkConfig.BaseURL+"/chat/completions",
		request,
		authHeader,
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

// Responses performs a responses request to Runware by translating it into a chat completion.
func (provider *RunwareProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	chatResponse, err := provider.ChatCompletion(ctx, key, request.ToChatRequest())
	if err != nil {
		return nil, err
	}

	return chatResponse.ToBifrostResponsesResponse(), nil
}

// ResponsesStream performs a streaming responses request to Runware by translating it into a streaming chat completion.
func (provider *RunwareProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return provider.ChatCompletionStream(
		ctx,
		postHookRunner,
		postHookSpanFinalizer,
		key,
		request.ToChatRequest(),
	)
}

// Embedding is not supported by the Runware provider.
func (provider *RunwareProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Speech is not supported by the Runware provider.
func (provider *RunwareProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Runware provider.
func (provider *RunwareProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Runware provider.
func (provider *RunwareProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Runware provider.
func (provider *RunwareProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration performs a text-to-image (or image-to-image) request to Runware's API.
func (provider *RunwareProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, bifrostReq *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToRunwareImageGenerationRequest(bifrostReq)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return provider.handleImageInference(ctx, key, bifrostReq.Model, jsonData)
}

// ImageEdit performs an image edit (image-to-image, inpainting, or outpainting) request to Runware's API.
func (provider *RunwareProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, bifrostReq *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToRunwareImageEditRequest(bifrostReq)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return provider.handleImageInference(ctx, key, bifrostReq.Model, jsonData)
}

// handleImageInference wraps a single imageInference task in the array Runware expects, posts it
// to the unified endpoint, and converts the synchronous response into a Bifrost image response.
func (provider *RunwareProvider) handleImageInference(ctx *schemas.BifrostContext, key schemas.Key, model string, jsonData []byte) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)

	// Runware expects an array of tasks; wrap the single marshalled task object.
	body := make([]byte, 0, len(jsonData)+2)
	body = append(body, '[')
	body = append(body, jsonData...)
	body = append(body, ']')

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, ""))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	req.SetBody(body)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseRunwareError(resp), body, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Decode response body
	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), body, rawErrBody, sendBackRawRequest, sendBackRawResponse)
	}

	// Parse response envelope
	var runwareResp RunwareResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(respBody, &runwareResp, body, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	bifrostResp, bifrostErr := ToBifrostImageGenerationResponse(&runwareResp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, body, respBody, sendBackRawRequest, sendBackRawResponse)
	}

	bifrostResp.Model = model
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()

	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// Rerank is not supported by the Runware provider.
func (provider *RunwareProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// OCR is not supported by the Runware provider.
func (provider *RunwareProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the Runware provider.
func (provider *RunwareProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the Runware provider.
func (provider *RunwareProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the Runware provider.
func (provider *RunwareProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// sendTaskArray wraps a single task object in the Runware array envelope, posts it to the
// unified endpoint, and returns the wrapped request body, decoded response body, and latency.
func (provider *RunwareProvider) sendTaskArray(ctx *schemas.BifrostContext, key schemas.Key, jsonData []byte) (reqBody []byte, respBody []byte, latency time.Duration, bifrostErr *schemas.BifrostError) {
	reqBody = make([]byte, 0, len(jsonData)+2)
	reqBody = append(reqBody, '[')
	reqBody = append(reqBody, jsonData...)
	reqBody = append(reqBody, ']')

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, ""))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	req.SetBody(reqBody)

	lat, bErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bErr != nil {
		return reqBody, nil, 0, bErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return reqBody, nil, 0, parseRunwareError(resp)
	}
	decoded, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return reqBody, nil, 0, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}
	// Copy out: the fasthttp response buffer is released when this function returns.
	return reqBody, append([]byte(nil), decoded...), lat, nil
}

// VideoGeneration submits an async videoInference task and returns the queued job.
// The caller polls VideoRetrieve to fetch the finished video.
func (provider *RunwareProvider) VideoGeneration(ctx *schemas.BifrostContext, key schemas.Key, bifrostReq *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		bifrostReq,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToRunwareVideoGenerationRequest(bifrostReq)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	reqBody, respBody, latency, bifrostErr := provider.sendTaskArray(ctx, key, jsonData)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, reqBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	var videoResp RunwareResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(respBody, &videoResp, reqBody, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	result, bifrostErr := firstVideoResult(&videoResp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, reqBody, respBody, sendBackRawRequest, sendBackRawResponse)
	}

	bifrostResp := ToBifrostVideoGenerationResponse(result)
	bifrostResp.ID = providerUtils.AddVideoIDProviderSuffix(result.TaskUUID, providerName)
	bifrostResp.Model = bifrostReq.Model
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// VideoRetrieve polls a previously submitted videoInference task via a getResponse task.
func (provider *RunwareProvider) VideoRetrieve(ctx *schemas.BifrostContext, key schemas.Key, bifrostReq *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	taskID := providerUtils.StripVideoIDProviderSuffix(bifrostReq.ID, providerName)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	jsonData, err := providerUtils.MarshalSorted(RunwareGetResponseRequest{TaskType: taskTypeGetResponse, TaskUUID: taskID})
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}

	reqBody, respBody, latency, bifrostErr := provider.sendTaskArray(ctx, key, jsonData)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, reqBody, nil, sendBackRawRequest, sendBackRawResponse)
	}

	var videoResp RunwareResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(respBody, &videoResp, reqBody, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	result, bifrostErr := firstVideoResult(&videoResp)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, reqBody, respBody, sendBackRawRequest, sendBackRawResponse)
	}

	bifrostResp := ToBifrostVideoGenerationResponse(result)
	bifrostResp.ID = providerUtils.AddVideoIDProviderSuffix(taskID, providerName)
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// VideoDownload retrieves the task, then downloads the finished video from its URL.
func (provider *RunwareProvider) VideoDownload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	taskDetails, bifrostErr := provider.VideoRetrieve(ctx, key, &schemas.BifrostVideoRetrieveRequest{Provider: request.Provider, ID: request.ID})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if taskDetails.Status != schemas.VideoStatusCompleted {
		return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("video not ready, current status: %s", taskDetails.Status), nil)
	}
	if len(taskDetails.Videos) == 0 || taskDetails.Videos[0].URL == nil || *taskDetails.Videos[0].URL == "" {
		return nil, providerUtils.NewBifrostOperationError("video URL not available", nil)
	}
	videoURL := *taskDetails.Videos[0].URL

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI(videoURL)
	req.Header.SetMethod(http.MethodGet)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("failed to download video: HTTP %d", resp.StatusCode()), nil)
	}
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
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

// firstVideoResult returns the first video task result, surfacing task-level errors.
func firstVideoResult(resp *RunwareResponse) (*RunwareResult, *schemas.BifrostError) {
	if len(resp.Data) == 0 {
		if msg := firstRunwareErrorMessage(resp.Errors); msg != "" {
			return nil, providerUtils.NewBifrostOperationError(msg, nil)
		}
		return nil, providerUtils.NewBifrostOperationError("runware returned no video task", nil)
	}
	return &resp.Data[0], nil
}

// VideoDelete is not supported by the Runware provider.
func (provider *RunwareProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by the Runware provider.
func (provider *RunwareProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by the Runware provider.
func (provider *RunwareProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// FileUpload is not supported by Runware provider.
func (provider *RunwareProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by Runware provider.
func (provider *RunwareProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by Runware provider.
func (provider *RunwareProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by Runware provider.
func (provider *RunwareProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by Runware provider.
func (provider *RunwareProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by Runware provider.
func (provider *RunwareProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by Runware provider.
func (provider *RunwareProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by Runware provider.
func (provider *RunwareProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by Runware provider.
func (provider *RunwareProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchDelete is not supported by Runware provider.
func (provider *RunwareProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults is not supported by Runware provider.
func (provider *RunwareProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the Runware provider.
func (provider *RunwareProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// Compaction is not supported by the Runware provider.
func (provider *RunwareProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Runware provider.
func (provider *RunwareProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Runware provider.
func (provider *RunwareProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Runware provider.
func (provider *RunwareProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Runware provider.
func (provider *RunwareProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Runware provider.
func (provider *RunwareProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Runware provider.
func (provider *RunwareProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Runware provider.
func (provider *RunwareProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Runware provider.
func (provider *RunwareProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Runware provider.
func (provider *RunwareProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the Runware provider.
func (provider *RunwareProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

// PassthroughStream is not supported by the Runware provider.
func (provider *RunwareProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}
