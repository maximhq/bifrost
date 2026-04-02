// Package chatgpt implements the ChatGPT provider as a pure passthrough relay.
// It forwards requests to chatgpt.com without any conversion — no path
// normalization, no body transformation. Auth (cookies/session tokens) flows
// through via SafeHeaders.
package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// #region agent log
func chatgptDebugLog(id, message string, data map[string]interface{}, hypothesisID string) {
	entry := map[string]interface{}{
		"sessionId":    "8579a9",
		"id":           id,
		"timestamp":    time.Now().UnixMilli(),
		"location":     "chatgpt.go(provider)",
		"message":      message,
		"data":         data,
		"hypothesisId": hypothesisID,
	}
	line, _ := json.Marshal(entry)
	f, err := os.OpenFile("/Users/akshay/Codebase/universe/bifrost/.cursor/debug-8579a9.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.Write(append(line, '\n'))
		f.Close()
	}
}

// #endregion

// ChatGPTProvider implements the Provider interface as a passthrough relay to chatgpt.com.
// Only Passthrough and PassthroughStream are implemented — all other operations
// return UnsupportedOperationError.
type ChatGPTProvider struct {
	logger              schemas.Logger
	client              *fasthttp.Client
	networkConfig       schemas.NetworkConfig
	sendBackRawRequest  bool
	sendBackRawResponse bool
}

// NewChatGPTProvider creates a new ChatGPT passthrough provider.
func NewChatGPTProvider(config *schemas.ProviderConfig, logger schemas.Logger) *ChatGPTProvider {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     5000,
		MaxIdleConnDuration: 30 * time.Second,
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}

	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://chatgpt.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &ChatGPTProvider{
		logger:              logger,
		client:              client,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}
}

func (provider *ChatGPTProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.ChatGPT
}

// --- Passthrough (the only supported operations) ---

// Passthrough forwards a request to chatgpt.com and returns the response.
// No path normalization — paths like /backend-api/conversation are forwarded as-is.
func (provider *ChatGPTProvider) Passthrough(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	req *schemas.BifrostPassthroughRequest,
) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	url := provider.networkConfig.BaseURL + req.Path
	if req.RawQuery != "" {
		url += "?" + req.RawQuery
	}

	fasthttpReq := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	defer fasthttp.ReleaseRequest(fasthttpReq)

	fasthttpReq.Header.SetMethod(req.Method)
	fasthttpReq.SetRequestURI(url)

	providerUtils.SetExtraHeaders(ctx, fasthttpReq, provider.networkConfig.ExtraHeaders, nil)

	// #region agent log
	incomingHeaderNames := make([]string, 0, len(req.SafeHeaders))
	incomingTotalBytes := 0
	for k, v := range req.SafeHeaders {
		incomingTotalBytes += len(k) + len(v) + 4
		incomingHeaderNames = append(incomingHeaderNames, fmt.Sprintf("%s(%d)", k, len(v)))
	}
	cookieVal := req.SafeHeaders["cookie"]
	isPerfectDup := false
	if len(cookieVal) > 4 {
		half := len(cookieVal) / 2
		if half > 1 && len(cookieVal) >= half*2+2 && cookieVal[:half] == cookieVal[half+2:] && cookieVal[half:half+2] == "; " {
			isPerfectDup = true
		}
	}
	chatgptDebugLog("incoming-safe-headers", "SafeHeaders from gateway", map[string]interface{}{
		"totalBytes":    incomingTotalBytes,
		"headerCount":   len(incomingHeaderNames),
		"headers":       incomingHeaderNames,
		"cookieLen":     len(cookieVal),
		"isPerfectDup":  isPerfectDup,
	}, "M")
	// #endregion

	// Forward all safe headers (includes cookies for ChatGPT auth)
	for k, v := range req.SafeHeaders {
		fasthttpReq.Header.Set(k, v)
	}

	// Signal to the Bifrost agent that this request originates from the gateway
	// and must be relayed directly to the origin, bypassing the TUN. Without this,
	// the agent's TUN intercepts the outbound connection and routes it back through
	// the gateway, creating an infinite loop.
	fasthttpReq.Header.Set("X-Bf-Agent-Direct", "1")

	// ChatGPT uses cookie-based auth, but if a key value is provided, set it as Bearer
	if key.Value.GetValue() != "" {
		fasthttpReq.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	fasthttpReq.SetBody(req.Body)

	// #region agent log
	headerNames := make([]string, 0)
	totalHeaderBytes := 0
	fasthttpReq.Header.All()(func(key, value []byte) bool {
		hSize := len(key) + len(value) + 4
		totalHeaderBytes += hSize
		headerNames = append(headerNames, fmt.Sprintf("%s(%d)", string(key), len(value)))
		return true
	})
	chatgptDebugLog("outbound-headers", "Headers being sent to chatgpt.com", map[string]interface{}{
		"totalBytes":  totalHeaderBytes,
		"headerCount": len(headerNames),
		"headers":     headerNames,
		"url":         url,
	}, "I")
	// #endregion

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, fasthttpReq, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers := providerUtils.ExtractProviderResponseHeaders(resp)

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to decode response body", err, provider.GetProviderKey())
	}

	// Remove wire-level encoding headers after decoding
	for k := range headers {
		if strings.EqualFold(k, "Content-Encoding") || strings.EqualFold(k, "Content-Length") {
			delete(headers, k)
		}
	}

	bifrostResponse := &schemas.BifrostPassthroughResponse{
		StatusCode: resp.StatusCode(),
		Headers:    headers,
		Body:       body,
	}

	bifrostResponse.ExtraFields.Provider = provider.GetProviderKey()
	bifrostResponse.ExtraFields.ModelRequested = req.Model
	bifrostResponse.ExtraFields.RequestType = schemas.PassthroughRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequestIfJSON(fasthttpReq, &bifrostResponse.ExtraFields)
	}

	return bifrostResponse, nil
}

// PassthroughStream forwards a streaming request to chatgpt.com.
func (provider *ChatGPTProvider) PassthroughStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	key schemas.Key,
	req *schemas.BifrostPassthroughRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	url := provider.networkConfig.BaseURL + req.Path
	if req.RawQuery != "" {
		url += "?" + req.RawQuery
	}

	fasthttpReq := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(fasthttpReq)

	fasthttpReq.Header.SetMethod(req.Method)
	fasthttpReq.SetRequestURI(url)

	providerUtils.SetExtraHeaders(ctx, fasthttpReq, provider.networkConfig.ExtraHeaders, nil)

	for k, v := range req.SafeHeaders {
		fasthttpReq.Header.Set(k, v)
	}

	fasthttpReq.Header.Set("X-Bf-Agent-Direct", "1")
	fasthttpReq.Header.Set("Connection", "close")

	if key.Value.GetValue() != "" {
		fasthttpReq.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	fasthttpReq.SetBody(req.Body)

	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.client, resp)

	startTime := time.Now()

	if err := activeClient.Do(fasthttpReq, resp); err != nil {
		providerUtils.ReleaseStreamingResponse(resp)
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
			return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, provider.GetProviderKey())
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, provider.GetProviderKey())
	}

	headers := make(map[string]string)
	resp.Header.All()(func(k, v []byte) bool {
		headers[string(k)] = string(v)
		return true
	})

	rawBodyStream := resp.BodyStream()
	if rawBodyStream == nil {
		providerUtils.ReleaseStreamingResponse(resp)
		return nil, providerUtils.NewBifrostOperationError(
			"provider returned an empty stream body",
			fmt.Errorf("provider returned an empty stream body"),
			provider.GetProviderKey(),
		)
	}

	bodyStream, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(rawBodyStream, rawBodyStream, providerUtils.GetStreamIdleTimeout(ctx))
	stopCancellation := providerUtils.SetupStreamCancellation(ctx, rawBodyStream, provider.logger)

	extraFields := schemas.BifrostResponseExtraFields{
		Provider:       provider.GetProviderKey(),
		ModelRequested: req.Model,
		RequestType:    schemas.PassthroughStreamRequest,
	}
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequestIfJSON(fasthttpReq, &extraFields)
	}
	statusCode := resp.StatusCode()

	ch := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, ch, provider.GetProviderKey(), req.Model, schemas.PassthroughStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, ch, provider.GetProviderKey(), req.Model, schemas.PassthroughStreamRequest, provider.logger)
			}
			close(ch)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)
		defer stopIdleTimeout()
		defer stopCancellation()

		buf := make([]byte, 4096)
		for {
			n, readErr := bodyStream.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case ch <- &schemas.BifrostStreamChunk{
					BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{
						StatusCode:  statusCode,
						Headers:     headers,
						Body:        chunk,
						ExtraFields: extraFields,
					},
				}:
				case <-ctx.Done():
					return
				}
			}
			if readErr == io.EOF {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				extraFields.Latency = time.Since(startTime).Milliseconds()
				finalResp := &schemas.BifrostResponse{
					PassthroughResponse: &schemas.BifrostPassthroughResponse{
						StatusCode:  statusCode,
						Headers:     headers,
						ExtraFields: extraFields,
					},
				}
				postHookRunner(ctx, finalResp, nil)
				if finalizer, ok := ctx.Value(schemas.BifrostContextKeyPostHookSpanFinalizer).(func(context.Context)); ok && finalizer != nil {
					finalizer(ctx)
				}
				return
			}
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				extraFields.Latency = time.Since(startTime).Milliseconds()
				providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, ch, schemas.PassthroughStreamRequest, provider.GetProviderKey(), req.Model, provider.logger)
				return
			}
		}
	}()
	return ch, nil
}

// --- All other operations are unsupported (passthrough-only provider) ---

func (p *ChatGPTProvider) ListModels(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ListModelsRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) TextCompletion(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) TextCompletionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ChatCompletion(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ChatCompletionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) Embedding(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) Responses(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ResponsesStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesStreamRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) Rerank(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ImageGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ImageGenerationStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ImageEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ImageEditStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ImageVariation(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) Speech(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) SpeechStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) Transcription(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) TranscriptionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, p.GetProviderKey())
}
func (p *ChatGPTProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, p.GetProviderKey())
}
