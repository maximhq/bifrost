// Package minimax implements MiniMax's native T2A v2 API.
package minimax

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

var _ schemas.Provider = (*MiniMaxProvider)(nil)

type MiniMaxProvider struct {
	logger               schemas.Logger
	client               *fasthttp.Client
	streamingClient      *fasthttp.Client
	networkConfig        schemas.NetworkConfig
	customProviderConfig *schemas.CustomProviderConfig
	authType             schemas.MiniMaxAuthType
	sendBackRawRequest   bool
	sendBackRawResponse  bool
}

func NewMiniMaxProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*MiniMaxProvider, error) {
	config.CheckAndSetDefaults()

	authType := schemas.MiniMaxAuthTypeBearer
	if config.MiniMaxConfig != nil && config.MiniMaxConfig.AuthType != "" {
		authType = config.MiniMaxConfig.AuthType
	}
	if authType != schemas.MiniMaxAuthTypeBearer && authType != schemas.MiniMaxAuthTypeXKey {
		return nil, fmt.Errorf("unsupported MiniMax auth_type %q", authType)
	}

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
	streamingClient := providerUtils.BuildStreamingClient(client)

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = defaultBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &MiniMaxProvider{
		logger:               logger,
		client:               client,
		streamingClient:      streamingClient,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		authType:             authType,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
	}, nil
}

func (provider *MiniMaxProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.MiniMax, provider.customProviderConfig)
}

func (provider *MiniMaxProvider) buildRequestURL(ctx *schemas.BifrostContext, requestType schemas.RequestType) string {
	path, fullURL := providerUtils.GetRequestPath(ctx, defaultSpeechPath, provider.customProviderConfig, requestType)
	if fullURL {
		return path
	}
	return provider.networkConfig.BaseURL + path
}

func (provider *MiniMaxProvider) setAuth(req *fasthttp.Request, key schemas.Key) {
	value := key.Value.GetValue()
	if value == "" {
		return
	}
	if provider.authType == schemas.MiniMaxAuthTypeXKey {
		req.Header.Set("x-key", value)
		return
	}
	req.Header.Set("Authorization", "Bearer "+value)
}

func (provider *MiniMaxProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.MiniMax, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	unfiltered := request != nil && request.Unfiltered
	if len(keys) == 0 {
		response := staticModelsResponse(provider.GetProviderKey(), schemas.Key{Models: schemas.WhiteList{"*"}}, unfiltered)
		if request == nil {
			return response, nil
		}
		return response.ApplyPagination(request.PageSize, request.PageToken), nil
	}

	combined := &schemas.BifrostListModelsResponse{Data: []schemas.Model{}}
	seen := make(map[string]struct{})
	for _, key := range keys {
		response := staticModelsResponse(provider.GetProviderKey(), key, unfiltered)
		for _, model := range response.Data {
			if _, ok := seen[model.ID]; ok {
				continue
			}
			seen[model.ID] = struct{}{}
			combined.Data = append(combined.Data, model)
		}
	}
	if request == nil {
		return combined, nil
	}
	return combined.ApplyPagination(request.PageSize, request.PageToken), nil
}

func (provider *MiniMaxProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.MiniMax, provider.customProviderConfig, schemas.SpeechRequest); err != nil {
		return nil, err
	}

	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
		return ToMiniMaxSpeechRequest(request, false)
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildRequestURL(ctx, schemas.SpeechRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	provider.setAuth(req, key)
	req.SetBody(jsonBody)

	latency, requestErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if requestErr != nil {
		return nil, providerUtils.EnrichError(ctx, requestErr, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	providerHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseMiniMaxHTTPError(resp), jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	var upstream MiniMaxSpeechResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, resp.Body(), &upstream, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	if businessErr := miniMaxBusinessError(&upstream, resp.StatusCode()); businessErr != nil {
		return nil, providerUtils.EnrichError(ctx, businessErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	if upstream.Data == nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, errors.New("MiniMax response data is null")), jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	if upstream.Data.Status != 2 {
		statusErr := providerUtils.NewProviderAPIError(fmt.Sprintf("MiniMax non-streaming response is incomplete (status %d)", upstream.Data.Status), nil, fasthttp.StatusBadGateway, nil, schemas.Ptr(upstream.TraceID))
		return nil, providerUtils.EnrichError(ctx, statusErr, jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	audio, err := decodeMiniMaxAudio(upstream.Data.Audio)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	if len(audio) == 0 {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, errors.New("MiniMax response contained no audio")), jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	result := &schemas.BifrostSpeechResponse{
		Audio:        audio,
		Usage:        miniMaxUsage(upstream.ExtraInfo, request),
		SubtitleFile: miniMaxSubtitleFile(upstream.Data),
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerHeaders,
		},
	}
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		result.ExtraFields.RawRequest = rawRequest
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		result.ExtraFields.RawResponse = rawResponse
	}
	return result, nil
}

func (provider *MiniMaxProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.MiniMax, provider.customProviderConfig, schemas.SpeechStreamRequest); err != nil {
		return nil, err
	}
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
		return ToMiniMaxSpeechRequest(request, true)
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildRequestURL(ctx, schemas.SpeechStreamRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "text/event-stream")
	provider.setAuth(req, key)
	req.SetBody(jsonBody)

	startTime := time.Now()
	if err := providerUtils.DoStreamingRequest(ctx, provider.streamingClient, req, resp); err != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		latency := time.Since(startTime)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{IsBifrostError: false, Error: &schemas.ErrorField{Type: schemas.Ptr(schemas.RequestCancelled), Message: schemas.ErrRequestCancelled, Error: err}}, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		return nil, providerUtils.EnrichError(ctx, parseMiniMaxHTTPError(resp), jsonBody, resp.Body(), provider.sendBackRawRequest, provider.sendBackRawResponse, time.Since(startTime))
	}

	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	go provider.consumeSpeechStream(ctx, resp, request, jsonBody, startTime, responseChan, postHookRunner, postHookSpanFinalizer)
	return responseChan, nil
}

func (provider *MiniMaxProvider) consumeSpeechStream(ctx *schemas.BifrostContext, resp *fasthttp.Response, request *schemas.BifrostSpeechRequest, requestBody []byte, startTime time.Time, responseChan chan *schemas.BifrostStreamChunk, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context)) {
	defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
	defer func() {
		if ctx.Err() == context.Canceled {
			providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, requestBody)
		} else if ctx.Err() == context.DeadlineExceeded {
			providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, requestBody)
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
	chunkIndex := 0
	lastChunkTime := startTime
	receivedAudio := false

	for {
		if ctx.Err() != nil {
			return
		}
		data, err := sseReader.ReadDataLine()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err != io.EOF {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
				return
			}
			break
		}

		var upstream MiniMaxSpeechResponse
		parseStart := time.Now()
		unmarshalErr := sonic.Unmarshal(data, &upstream)
		schemas.AddStreamParse(ctx, time.Since(parseStart))
		if unmarshalErr != nil {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, fmt.Errorf("failed to parse MiniMax stream chunk: %w", unmarshalErr), responseChan, provider.logger, postHookSpanFinalizer)
			return
		}
		if businessErr := miniMaxBusinessError(&upstream, fasthttp.StatusOK); businessErr != nil {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, businessErr, requestBody, data, provider.sendBackRawRequest, provider.sendBackRawResponse, time.Since(startTime)), responseChan, provider.logger, postHookSpanFinalizer)
			return
		}

		if upstream.Data != nil && upstream.Data.Audio != "" {
			audio, decodeErr := decodeMiniMaxAudio(upstream.Data.Audio)
			if decodeErr != nil {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendError(ctx, postHookRunner, decodeErr, responseChan, provider.logger, postHookSpanFinalizer)
				return
			}
			if len(audio) > 0 {
				receivedAudio = true
				delta := &schemas.BifrostSpeechStreamResponse{
					Type:  schemas.SpeechStreamResponseTypeDelta,
					Audio: audio,
					ExtraFields: schemas.BifrostResponseExtraFields{
						ChunkIndex: chunkIndex,
						Latency:    time.Since(lastChunkTime).Milliseconds(),
					},
				}
				if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
					delta.ExtraFields.RawResponse = string(data)
				}
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, delta, nil, nil), responseChan, postHookSpanFinalizer)
				chunkIndex++
				lastChunkTime = time.Now()
			}
		}

		if upstream.Data != nil && upstream.Data.Status == 2 {
			if !receivedAudio {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				emptyErr := providerUtils.NewProviderAPIError("MiniMax stream completed without audio data", nil, fasthttp.StatusBadGateway, nil, schemas.Ptr(upstream.TraceID))
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, emptyErr, requestBody, data, provider.sendBackRawRequest, provider.sendBackRawResponse, time.Since(startTime)), responseChan, provider.logger, postHookSpanFinalizer)
				return
			}
			done := &schemas.BifrostSpeechStreamResponse{
				Type:         schemas.SpeechStreamResponseTypeDone,
				Audio:        []byte{},
				Usage:        miniMaxUsage(upstream.ExtraInfo, request),
				SubtitleFile: miniMaxSubtitleFile(upstream.Data),
				ExtraFields: schemas.BifrostResponseExtraFields{
					ChunkIndex: chunkIndex,
					Latency:    time.Since(startTime).Milliseconds(),
				},
			}
			if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
				providerUtils.ParseAndSetRawRequest(&done.ExtraFields, requestBody)
			}
			if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
				done.ExtraFields.RawResponse = string(data)
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, done, nil, nil), responseChan, postHookSpanFinalizer)
			return
		}
	}

	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	message := "MiniMax stream ended before completion status"
	if !receivedAudio {
		message = "MiniMax stream ended without audio data"
	}
	streamErr := providerUtils.NewProviderAPIError(message, io.ErrUnexpectedEOF, fasthttp.StatusBadGateway, nil, nil)
	providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, streamErr, requestBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, time.Since(startTime)), responseChan, provider.logger, postHookSpanFinalizer)
}
