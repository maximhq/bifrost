package gigachat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	openaiProvider "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

var _ schemas.Provider = (*GigaChatProvider)(nil)

// GigaChatProvider implements the Provider interface for GigaChat's API.
type GigaChatProvider struct {
	logger               schemas.Logger
	client               *fasthttp.Client
	streamingClient      *fasthttp.Client
	networkConfig        schemas.NetworkConfig
	sendBackRawRequest   bool
	sendBackRawResponse  bool
	customProviderConfig *schemas.CustomProviderConfig
	tokenCache           *gigaChatTokenCache
	tlsClientCache       *gigaChatTLSClientCache
	attachmentCache      *gigaChatAttachmentCacheManager
}

// gigaChatPassthroughReadCloser keeps stream finalization aligned with the
// transport-owned large-response reader. The provider returns before the HTTP
// transport consumes that reader, so finalizing in the provider method would
// incorrectly mark a successful passthrough stream as incomplete.
type gigaChatPassthroughReadCloser struct {
	io.ReadCloser
	ctx                   *schemas.BifrostContext
	postHookSpanFinalizer func(context.Context)
	completed             atomic.Bool
	closeOnce             sync.Once
	closeErr              error
}

func (reader *gigaChatPassthroughReadCloser) Read(buffer []byte) (int, error) {
	read, err := reader.ReadCloser.Read(buffer)
	if errors.Is(err, io.EOF) {
		reader.completed.Store(true)
	}
	return read, err
}

func (reader *gigaChatPassthroughReadCloser) Close() error {
	reader.closeOnce.Do(func() {
		defer func() {
			providerUtils.EnsureStreamFinalizerCalled(reader.ctx, reader.postHookSpanFinalizer)
		}()
		if reader.completed.Load() {
			reader.ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		}
		reader.closeErr = reader.ReadCloser.Close()
	})
	return reader.closeErr
}

func wrapGigaChatPassthroughFinalizer(ctx *schemas.BifrostContext, postHookSpanFinalizer func(context.Context)) {
	reader, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(io.ReadCloser)
	if !ok || reader == nil {
		providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		return
	}
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseReader, &gigaChatPassthroughReadCloser{
		ReadCloser:            reader,
		ctx:                   ctx,
		postHookSpanFinalizer: postHookSpanFinalizer,
	})
}

// NewGigaChatProvider creates a new GigaChat provider instance.
func NewGigaChatProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*GigaChatProvider, error) {
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
		config.NetworkConfig.BaseURL = gigaChatDefaultBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &GigaChatProvider{
		logger:               logger,
		client:               client,
		streamingClient:      streamingClient,
		networkConfig:        config.NetworkConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
		tokenCache:           newGigaChatTokenCache(time.Now),
		tlsClientCache:       newGigaChatTLSClientCache(),
		attachmentCache:      newGigaChatAttachmentCacheManager(),
	}, nil
}

// GetProviderKey returns the provider identifier for GigaChat.
func (provider *GigaChatProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.GigaChat, provider.customProviderConfig)
}

func (provider *GigaChatProvider) chatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest, forceRefresh bool) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	ctx = ensureGigaChatContext(ctx)
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("chat completion request is nil", nil)
	}
	preparedRequest, bifrostErr := provider.prepareGigaChatChatAttachments(ctx, key, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	request = preparedRequest

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToGigaChatChatRequest(ctx, request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers, bifrostErr := provider.buildAuthHeadersWithRefresh(ctx, key, forceRefresh)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	client, clientErr := provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, key.GigaChatKeyConfig)
	if clientErr != nil {
		return nil, newGigaChatConfigurationError(clientErr.Error())
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	for headerName, headerValue := range headers {
		req.Header.Set(headerName, headerValue)
	}
	req.SetRequestURI(buildGigaChatRequestURL(ctx, resolveBaseURL(key, provider.networkConfig), gigaChatAPIVersionV1, "/chat/completions", provider.customProviderConfig, schemas.ChatCompletionRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBody(jsonData)

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	defer wait()
	if bifrostErr != nil {
		bifrostErr.ExtraFields.Provider = provider.GetProviderKey()
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		bifrostErr := ParseGigaChatError(resp, provider.GetProviderKey())
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		bifrostErr := newGigaChatProviderResponseError("failed to decode GigaChat chat completion response", err)
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, resp.Body(), sendBackRawRequest, sendBackRawResponse)
	}

	gigaChatResponse := &GigaChatChatResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, gigaChatResponse, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, responseBody, sendBackRawRequest, sendBackRawResponse)
	}

	response := ToBifrostChatResponse(provider.GetProviderKey(), gigaChatResponse)
	if response == nil {
		return nil, newGigaChatProviderResponseError("GigaChat chat completion response is empty", nil)
	}
	response.BackfillParams(request)
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

func (provider *GigaChatProvider) chatCompletionStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
	forceRefresh bool,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx = ensureGigaChatContext(ctx)
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("chat completion request is nil", nil)
	}
	preparedRequest, bifrostErr := provider.prepareGigaChatChatAttachments(ctx, key, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	request = preparedRequest

	headers, bifrostErr := provider.buildAuthHeadersWithRefresh(ctx, key, forceRefresh)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	client, clientErr := provider.getGigaChatTLSClient(provider.streamingClient, gigaChatTLSClientCacheStreaming, key.GigaChatKeyConfig)
	if clientErr != nil {
		return nil, newGigaChatConfigurationError(clientErr.Error())
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	providerName := provider.GetProviderKey()

	responseChan, bifrostErr := openaiProvider.HandleOpenAIChatCompletionStreaming(
		ctx,
		client,
		buildGigaChatRequestURL(ctx, resolveBaseURL(key, provider.networkConfig), gigaChatAPIVersionV1, "/chat/completions", provider.customProviderConfig, schemas.ChatCompletionStreamRequest),
		request,
		headers,
		nil,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		sendBackRawRequest,
		sendBackRawResponse,
		providerName,
		postHookRunner,
		func(request *schemas.BifrostChatRequest) (providerUtils.RequestBodyWithExtraParams, error) {
			return ToGigaChatChatStreamRequest(ctx, request)
		},
		handleGigaChatChatStreamResponse(providerName),
		func(resp *fasthttp.Response) *schemas.BifrostError {
			return ParseGigaChatError(resp, providerName)
		},
		nil,
		withGigaChatChatResponseProvider(providerName),
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
	if bifrostErr == nil {
		if isPassthrough, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); isPassthrough {
			wrapGigaChatPassthroughFinalizer(ctx, postHookSpanFinalizer)
		}
	}
	return responseChan, bifrostErr
}

func ensureGigaChatContext(ctx *schemas.BifrostContext) *schemas.BifrostContext {
	if ctx != nil {
		return ctx
	}
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func isGigaChatUnauthorizedError(bifrostErr *schemas.BifrostError) bool {
	return bifrostErr != nil && bifrostErr.StatusCode != nil && *bifrostErr.StatusCode == http.StatusUnauthorized
}

func (provider *GigaChatProvider) unsupported(requestType schemas.RequestType) *schemas.BifrostError {
	return providerUtils.NewUnsupportedOperationError(requestType, provider.GetProviderKey())
}

func (provider *GigaChatProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	response, bifrostErr := provider.listModelsByKeyWithRefresh(ctx, key, request, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		return provider.listModelsByKeyWithRefresh(ctx, key, request, true)
	}
	return response, bifrostErr
}

func (provider *GigaChatProvider) listModelsByKeyWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest, forceRefresh bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	ctx = ensureGigaChatContext(ctx)

	headers, bifrostErr := provider.buildAuthHeadersWithRefresh(ctx, key, forceRefresh)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	client, clientErr := provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, key.GigaChatKeyConfig)
	if clientErr != nil {
		return nil, newGigaChatConfigurationError(clientErr.Error())
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	for headerName, headerValue := range headers {
		req.Header.Set(headerName, headerValue)
	}
	req.SetRequestURI(buildGigaChatRequestURL(ctx, resolveBaseURL(key, provider.networkConfig), gigaChatAPIVersionV1, "/models", provider.customProviderConfig, schemas.ListModelsRequest))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "application/json")

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	defer wait()
	if bifrostErr != nil {
		bifrostErr.ExtraFields.Provider = provider.GetProviderKey()
		return nil, enrichGigaChatError(ctx, bifrostErr, nil, nil, sendBackRawRequest, sendBackRawResponse)
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		bifrostErr := ParseGigaChatError(resp, provider.GetProviderKey())
		return nil, enrichGigaChatError(ctx, bifrostErr, nil, resp.Body(), sendBackRawRequest, sendBackRawResponse)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		bifrostErr := newGigaChatProviderResponseError("failed to decode GigaChat models response", err)
		return nil, enrichGigaChatError(ctx, bifrostErr, nil, resp.Body(), sendBackRawRequest, sendBackRawResponse)
	}

	gigaChatResponse := &GigaChatListModelsResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, gigaChatResponse, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, enrichGigaChatError(ctx, bifrostErr, nil, responseBody, sendBackRawRequest, sendBackRawResponse)
	}

	response := gigaChatResponse.ToBifrostListModelsResponse(provider.GetProviderKey(), key.Models, key.BlacklistedModels, key.Aliases, request.Unfiltered)
	if response == nil {
		return nil, newGigaChatProviderResponseError("GigaChat models response is empty", nil)
	}
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

func (provider *GigaChatProvider) embeddingWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest, forceRefresh bool) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	ctx = ensureGigaChatContext(ctx)
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("embedding request is nil", nil)
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToGigaChatEmbeddingRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers, bifrostErr := provider.buildAuthHeadersWithRefresh(ctx, key, forceRefresh)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	client, clientErr := provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, key.GigaChatKeyConfig)
	if clientErr != nil {
		return nil, newGigaChatConfigurationError(clientErr.Error())
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	for headerName, headerValue := range headers {
		req.Header.Set(headerName, headerValue)
	}
	req.SetRequestURI(buildGigaChatRequestURL(ctx, resolveBaseURL(key, provider.networkConfig), gigaChatAPIVersionV1, "/embeddings", provider.customProviderConfig, schemas.EmbeddingRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBody(jsonData)

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	defer wait()
	if bifrostErr != nil {
		bifrostErr.ExtraFields.Provider = provider.GetProviderKey()
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		bifrostErr := ParseGigaChatError(resp, provider.GetProviderKey())
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, resp.Body(), sendBackRawRequest, sendBackRawResponse)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		bifrostErr := newGigaChatProviderResponseError("failed to decode GigaChat embeddings response", err)
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, resp.Body(), sendBackRawRequest, sendBackRawResponse)
	}

	gigaChatResponse := &GigaChatEmbeddingResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, gigaChatResponse, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, responseBody, sendBackRawRequest, sendBackRawResponse)
	}

	response := ToBifrostEmbeddingResponse(provider.GetProviderKey(), gigaChatResponse)
	if response == nil {
		return nil, newGigaChatProviderResponseError("GigaChat embeddings response is empty", nil)
	}
	if err := applyGigaChatEmbeddingEncodingFormat(response, request.Params); err != nil {
		return nil, enrichGigaChatError(ctx, newGigaChatProviderResponseError("failed to encode GigaChat embeddings response", err), jsonData, responseBody, sendBackRawRequest, sendBackRawResponse)
	}
	response.BackfillParams(request)
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

func (provider *GigaChatProvider) responsesWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest, forceRefresh bool) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	ctx = ensureGigaChatContext(ctx)
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("responses request is nil", nil)
	}
	preparedRequest, bifrostErr := provider.prepareGigaChatResponsesAttachments(ctx, key, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	request = preparedRequest

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToGigaChatResponsesRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers, bifrostErr := provider.buildAuthHeadersWithRefresh(ctx, key, forceRefresh)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	client, clientErr := provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, key.GigaChatKeyConfig)
	if clientErr != nil {
		return nil, newGigaChatConfigurationError(clientErr.Error())
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	for headerName, headerValue := range headers {
		req.Header.Set(headerName, headerValue)
	}
	req.SetRequestURI(buildGigaChatRequestURL(ctx, resolveBaseURL(key, provider.networkConfig), gigaChatAPIVersionV2, "/chat/completions", provider.customProviderConfig, schemas.ResponsesRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBody(jsonData)

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	defer wait()
	if bifrostErr != nil {
		bifrostErr.ExtraFields.Provider = provider.GetProviderKey()
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		bifrostErr := ParseGigaChatError(resp, provider.GetProviderKey())
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, resp.Body(), sendBackRawRequest, sendBackRawResponse)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		bifrostErr := newGigaChatProviderResponseError("failed to decode GigaChat Responses response", err)
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, resp.Body(), sendBackRawRequest, sendBackRawResponse)
	}

	gigaChatResponse := &GigaChatResponsesResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, gigaChatResponse, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, responseBody, sendBackRawRequest, sendBackRawResponse)
	}

	response := ToBifrostResponsesResponse(provider.GetProviderKey(), gigaChatResponse)
	if response == nil {
		return nil, newGigaChatProviderResponseError("GigaChat Responses response is empty", nil)
	}
	response.BackfillParams(request)
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

func (provider *GigaChatProvider) countTokensWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest, forceRefresh bool) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	ctx = ensureGigaChatContext(ctx)
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("count tokens request is nil", nil)
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToGigaChatCountTokensRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers, bifrostErr := provider.buildAuthHeadersWithRefresh(ctx, key, forceRefresh)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	client, clientErr := provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheDefault, key.GigaChatKeyConfig)
	if clientErr != nil {
		return nil, newGigaChatConfigurationError(clientErr.Error())
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	for headerName, headerValue := range headers {
		req.Header.Set(headerName, headerValue)
	}
	req.SetRequestURI(buildGigaChatRequestURL(ctx, resolveBaseURL(key, provider.networkConfig), gigaChatAPIVersionV1, "/tokens/count", provider.customProviderConfig, schemas.CountTokensRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBody(jsonData)

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	defer wait()
	if bifrostErr != nil {
		bifrostErr.ExtraFields.Provider = provider.GetProviderKey()
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		bifrostErr := ParseGigaChatError(resp, provider.GetProviderKey())
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, resp.Body(), sendBackRawRequest, sendBackRawResponse)
	}

	responseBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		bifrostErr := newGigaChatProviderResponseError("failed to decode GigaChat count tokens response", err)
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, resp.Body(), sendBackRawRequest, sendBackRawResponse)
	}

	gigaChatResponse := &GigaChatCountTokensResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, gigaChatResponse, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, responseBody, sendBackRawRequest, sendBackRawResponse)
	}

	response := ToBifrostCountTokensResponse(provider.GetProviderKey(), gigaChatResponse, request.Model)
	if response == nil {
		return nil, newGigaChatProviderResponseError("GigaChat count tokens response is empty", nil)
	}
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		response.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

func (provider *GigaChatProvider) responsesStreamWithRefresh(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
	forceRefresh bool,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx = ensureGigaChatContext(ctx)
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("responses stream request is nil", nil)
	}
	preparedRequest, bifrostErr := provider.prepareGigaChatResponsesAttachments(ctx, key, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	request = preparedRequest
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToGigaChatResponsesStreamRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers, bifrostErr := provider.buildAuthHeadersWithRefresh(ctx, key, forceRefresh)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	client, clientErr := provider.getGigaChatTLSClient(provider.streamingClient, gigaChatTLSClientCacheStreaming, key.GigaChatKeyConfig)
	if clientErr != nil {
		return nil, newGigaChatConfigurationError(clientErr.Error())
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	for headerName, headerValue := range headers {
		req.Header.Set(headerName, headerValue)
	}
	req.SetRequestURI(buildGigaChatRequestURL(ctx, resolveBaseURL(key, provider.networkConfig), gigaChatAPIVersionV2, "/chat/completions", provider.customProviderConfig, schemas.ResponsesStreamRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.SetBody(jsonData)

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	activeClient := providerUtils.PrepareResponseStreaming(ctx, client, resp)
	if err := activeClient.Do(req, resp); err != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, redactGigaChatRawPayload(jsonData), nil, sendBackRawRequest, sendBackRawResponse)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, enrichGigaChatError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, enrichGigaChatError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}
	startTime := time.Now()

	providerName := provider.GetProviderKey()
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		bifrostErr := ParseGigaChatError(resp, providerName)
		return nil, enrichGigaChatError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	if providerUtils.SetupStreamingPassthrough(ctx, resp) {
		responseChan := make(chan *schemas.BifrostStreamChunk)
		wrapGigaChatPassthroughFinalizer(ctx, postHookSpanFinalizer)
		providerUtils.CloseStream(ctx, responseChan)
		return responseChan, nil
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

		reader, drained := providerUtils.DrainNonSSEStreamReader(resp, reader)
		if drained {
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendError(ctx, postHookRunner, errors.New("provider returned non-SSE response for streaming request"), responseChan, provider.logger, postHookSpanFinalizer)
			return
		}

		sseReader := providerUtils.GetSSEEventReader(ctx, reader)
		streamState := schemas.AcquireChatToResponsesStreamState()
		defer schemas.ReleaseChatToResponsesStreamState(streamState)

		usage := &schemas.BifrostLLMUsage{}
		usageSeen := false
		lastChunkTime := startTime
		var pendingFinalEvent *schemas.BifrostResponsesStreamResponse
		streamEndedSemantically := false

		for {
			if ctx.Err() != nil {
				return
			}

			eventType, data, readErr := sseReader.ReadEvent()
			if readErr != nil {
				if ctx.Err() != nil {
					return
				}
				if readErr != io.EOF {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					if provider.logger != nil {
						provider.logger.Warn("Error reading stream: %v", readErr)
					}
					providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, provider.logger, postHookSpanFinalizer)
					return
				}
				break
			}
			if isGigaChatResponsesStreamDoneMarker(data) {
				streamEndedSemantically = true
				break
			}
			if len(data) == 0 {
				if isGigaChatResponsesStreamTerminalEvent(eventType, nil) {
					streamEndedSemantically = true
					break
				}
				continue
			}

			if bifrostErr := parseGigaChatStreamError(data, providerName); bifrostErr != nil {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, enrichGigaChatError(ctx, bifrostErr, jsonData, data, sendBackRawRequest, sendBackRawResponse), responseChan, provider.logger, postHookSpanFinalizer)
				return
			}

			var gigaChatResponse GigaChatResponsesResponse
			_, rawResponse, handlerErr := providerUtils.HandleProviderResponse(data, &gigaChatResponse, nil, false, sendBackRawResponse)
			if handlerErr != nil {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, enrichGigaChatError(ctx, handlerErr, jsonData, data, sendBackRawRequest, sendBackRawResponse), responseChan, provider.logger, postHookSpanFinalizer)
				return
			}

			if gigaChatResponse.Usage != nil {
				usageSeen = true
				updateGigaChatResponsesStreamUsage(usage, toBifrostGigaChatUsage(gigaChatResponse.Usage))
			}

			responses := ToBifrostResponsesStreamResponse(providerName, &gigaChatResponse, streamState)
			for _, response := range responses {
				if response == nil {
					continue
				}
				response.ExtraFields.ChunkIndex = response.SequenceNumber
				response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
				if sendBackRawResponse {
					response.ExtraFields.RawResponse = rawResponse
				}

				if response.Type == schemas.ResponsesStreamResponseTypeCompleted || response.Type == schemas.ResponsesStreamResponseTypeIncomplete {
					pendingFinalEvent = response
					continue
				}

				response.ExtraFields.Latency = time.Since(lastChunkTime).Milliseconds()
				lastChunkTime = time.Now()
				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan, postHookSpanFinalizer)
			}
			if isGigaChatResponsesStreamTerminalEvent(eventType, &gigaChatResponse) {
				streamEndedSemantically = true
				break
			}
		}

		if pendingFinalEvent != nil {
			if usageSeen && pendingFinalEvent.Response != nil {
				pendingFinalEvent.Response.Usage = usage.ToResponsesResponseUsage()
			}
			if sendBackRawRequest {
				providerUtils.ParseAndSetRawRequest(&pendingFinalEvent.ExtraFields, jsonData)
			}
			pendingFinalEvent.ExtraFields.Latency = time.Since(startTime).Milliseconds()
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, pendingFinalEvent, nil, nil, nil), responseChan, postHookSpanFinalizer)
		}
		if streamEndedSemantically {
			closeGigaChatSemanticStream(ctx, resp.BodyStream())
		}
	}()

	return responseChan, nil
}

type gigaChatStreamCloserWithError interface {
	CloseWithError(error) error
}

func isGigaChatResponsesStreamDoneMarker(data []byte) bool {
	return strings.TrimSpace(string(data)) == "[DONE]"
}

func isGigaChatResponsesStreamTerminalEvent(eventType string, response *GigaChatResponsesResponse) bool {
	if isGigaChatResponsesStreamTerminalEventName(eventType) {
		return true
	}
	if response == nil {
		return false
	}
	if response.Event != nil && isGigaChatResponsesStreamTerminalEventName(*response.Event) {
		return true
	}
	return response.FinishReason != nil && len(response.Messages) == 0 && len(response.Choices) == 0
}

func isGigaChatResponsesStreamTerminalEventName(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "done", "response.done", "response.completed", "response.message.done":
		return true
	default:
		return false
	}
}

func closeGigaChatSemanticStream(ctx *schemas.BifrostContext, bodyStream io.Reader) {
	if bodyStream == nil {
		return
	}
	if closed, ok := ctx.Value(schemas.BifrostContextKeyConnectionClosed).(bool); ok && closed {
		return
	}
	if closer, ok := bodyStream.(io.Closer); ok {
		ctx.SetValue(schemas.BifrostContextKeyConnectionClosed, true)
		_ = closer.Close()
		return
	}
	if closer, ok := bodyStream.(gigaChatStreamCloserWithError); ok {
		ctx.SetValue(schemas.BifrostContextKeyConnectionClosed, true)
		_ = closer.CloseWithError(io.EOF)
	}
}

// ListModels performs a v1 models request to GigaChat.
func (provider *GigaChatProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	if request == nil {
		request = &schemas.BifrostListModelsRequest{Provider: provider.GetProviderKey()}
	} else if request.Provider == "" {
		requestCopy := *request
		requestCopy.Provider = provider.GetProviderKey()
		request = &requestCopy
	}
	if len(keys) == 0 {
		return providerUtils.HandleKeylessListModelsRequest(provider.GetProviderKey(), func() (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
			return provider.listModelsByKey(ctx, schemas.Key{}, request)
		})
	}
	return providerUtils.HandleMultipleListModelsRequests(ctx, keys, request, provider.listModelsByKey)
}

// TextCompletion is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) TextCompletion(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.TextCompletionRequest)
}

// TextCompletionStream is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) TextCompletionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.TextCompletionStreamRequest)
}

// ChatCompletion sends a non-streaming v1 chat completions request to GigaChat.
func (provider *GigaChatProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	response, bifrostErr := provider.chatCompletion(ctx, key, request, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		return provider.chatCompletion(ctx, key, request, true)
	}
	return response, bifrostErr
}

// ChatCompletionStream sends a streaming v1 chat completions request to GigaChat.
func (provider *GigaChatProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	responseChan, bifrostErr := provider.chatCompletionStream(ctx, postHookRunner, postHookSpanFinalizer, key, request, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		return provider.chatCompletionStream(ctx, postHookRunner, postHookSpanFinalizer, key, request, true)
	}
	return responseChan, bifrostErr
}

// Responses sends a non-streaming v2 chat completions request to GigaChat.
func (provider *GigaChatProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	response, bifrostErr := provider.responsesWithRefresh(ctx, key, request, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		return provider.responsesWithRefresh(ctx, key, request, true)
	}
	return response, bifrostErr
}

// ResponsesStream sends a streaming v2 chat completions request to GigaChat.
func (provider *GigaChatProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	responseChan, bifrostErr := provider.responsesStreamWithRefresh(ctx, postHookRunner, postHookSpanFinalizer, key, request, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		return provider.responsesStreamWithRefresh(ctx, postHookRunner, postHookSpanFinalizer, key, request, true)
	}
	return responseChan, bifrostErr
}

// CountTokens sends a v1 tokens/count request to GigaChat.
func (provider *GigaChatProvider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.CountTokensRequest); err != nil {
		return nil, err
	}

	response, bifrostErr := provider.countTokensWithRefresh(ctx, key, request, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		return provider.countTokensWithRefresh(ctx, key, request, true)
	}
	return response, bifrostErr
}

// Compaction is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) Compaction(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.CompactionRequest)
}

// Embedding sends a non-streaming v1 embeddings request to GigaChat.
func (provider *GigaChatProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	response, bifrostErr := provider.embeddingWithRefresh(ctx, key, request, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		return provider.embeddingWithRefresh(ctx, key, request, true)
	}
	return response, bifrostErr
}

// Rerank is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) Rerank(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.RerankRequest)
}

// OCR is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) OCR(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.OCRRequest)
}

// Speech is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) Speech(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.SpeechRequest)
}

// SpeechStream is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) SpeechStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.SpeechStreamRequest)
}

// Transcription is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) Transcription(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.TranscriptionRequest)
}

// TranscriptionStream is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) TranscriptionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.TranscriptionStreamRequest)
}

// ImageGeneration is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ImageGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ImageGenerationRequest)
}

// ImageGenerationStream is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ImageGenerationStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ImageGenerationStreamRequest)
}

// ImageEdit is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ImageEdit(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ImageEditRequest)
}

// ImageEditStream is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ImageEditStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ImageEditStreamRequest)
}

// ImageVariation is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ImageVariation(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ImageVariationRequest)
}

// VideoGeneration is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.VideoGenerationRequest)
}

// VideoRetrieve is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.VideoRetrieveRequest)
}

// VideoDownload is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.VideoDownloadRequest)
}

// VideoDelete is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.VideoDeleteRequest)
}

// VideoList is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.VideoListRequest)
}

// VideoRemix is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.VideoRemixRequest)
}

// BatchCreate creates a GigaChat batch job.
func (provider *GigaChatProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.BatchCreateRequest); err != nil {
		return nil, err
	}

	response, bifrostErr := provider.batchCreateWithRefresh(ctx, key, request, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		return provider.batchCreateWithRefresh(ctx, key, request, true)
	}
	return response, bifrostErr
}

// BatchList lists GigaChat batch jobs.
func (provider *GigaChatProvider) BatchList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.BatchListRequest); err != nil {
		return nil, err
	}
	if request == nil {
		request = &schemas.BifrostBatchListRequest{Provider: provider.GetProviderKey()}
	}
	if bifrostErr := validateGigaChatBatchListRequest(request); bifrostErr != nil {
		return nil, bifrostErr
	}
	if len(keys) == 0 {
		keys = []schemas.Key{{}}
	}

	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger, false)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err)
	}
	key, _, ok := helper.GetCurrentKey()
	if !ok {
		return &schemas.BifrostBatchListResponse{
			Object: "list",
			Data:   []schemas.BifrostBatchRetrieveResponse{},
		}, nil
	}

	response, bifrostErr := provider.batchListWithRefresh(ctx, key, request, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		response, bifrostErr = provider.batchListWithRefresh(ctx, key, request, true)
	}
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	nextCursor, hasMore := helper.BuildNextCursor(false, "")
	response.HasMore = hasMore
	if nextCursor != "" {
		response.NextCursor = &nextCursor
	}
	return response, nil
}

// BatchRetrieve retrieves a GigaChat batch job.
func (provider *GigaChatProvider) BatchRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.BatchRetrieveRequest); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("batch retrieve request is nil", nil)
	}
	if strings.TrimSpace(request.BatchID) == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil)
	}
	if len(keys) == 0 {
		keys = []schemas.Key{{}}
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		response, bifrostErr := provider.batchRetrieveWithRefresh(ctx, key, request, false)
		if isGigaChatUnauthorizedError(bifrostErr) {
			response, bifrostErr = provider.batchRetrieveWithRefresh(ctx, key, request, true)
		}
		if bifrostErr == nil {
			return response, nil
		}
		lastErr = bifrostErr
	}
	return nil, lastErr
}

// BatchCancel is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.BatchCancelRequest)
}

// BatchDelete is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) BatchDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.BatchDeleteRequest)
}

// BatchResults retrieves completed GigaChat batch results through the Files API.
func (provider *GigaChatProvider) BatchResults(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.BatchResultsRequest); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("batch results request is nil", nil)
	}
	if strings.TrimSpace(request.BatchID) == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil)
	}
	if len(keys) == 0 {
		keys = []schemas.Key{{}}
	}

	batchResponse, bifrostErr := provider.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		Model:    request.Model,
		BatchID:  strings.TrimSpace(request.BatchID),
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if batchResponse.OutputFileID == nil || strings.TrimSpace(*batchResponse.OutputFileID) == "" {
		return nil, providerUtils.NewBifrostOperationError("batch results not available: GigaChat did not return output_file_id or result_file_id (batch may not be completed yet)", nil)
	}

	outputFileID := strings.TrimSpace(*batchResponse.OutputFileID)
	fileContentResponse, bifrostErr := provider.readGigaChatBatchOutputFile(ctx, keys, request, outputFileID)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	results, parseErrors := parseGigaChatBatchResultsJSONL(fileContentResponse.Content, provider.logger)
	providerExtraFields := cloneGigaChatBatchProviderExtraFields(batchResponse.ProviderExtraFields)
	if providerExtraFields == nil {
		providerExtraFields = make(map[string]interface{})
	}
	providerExtraFields["gigachat_batch_output_file_id"] = outputFileID

	response := &schemas.BifrostBatchResultsResponse{
		BatchID:             strings.TrimSpace(request.BatchID),
		Results:             results,
		ProviderExtraFields: providerExtraFields,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:                provider.GetProviderKey(),
			Latency:                 fileContentResponse.ExtraFields.Latency,
			ProviderResponseHeaders: fileContentResponse.ExtraFields.ProviderResponseHeaders,
		},
	}
	if len(parseErrors) > 0 {
		response.ExtraFields.ParseErrors = parseErrors
	}
	return response, nil
}

// FileUpload uploads a file to GigaChat.
func (provider *GigaChatProvider) FileUpload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.FileUploadRequest); err != nil {
		return nil, err
	}

	response, bifrostErr := provider.fileUploadWithRefresh(ctx, key, request, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		return provider.fileUploadWithRefresh(ctx, key, request, true)
	}
	return response, bifrostErr
}

// FileList lists files available to the configured GigaChat account.
func (provider *GigaChatProvider) FileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.FileListRequest); err != nil {
		return nil, err
	}
	if request == nil {
		request = &schemas.BifrostFileListRequest{Provider: provider.GetProviderKey()}
	}
	if request.Order != nil && strings.TrimSpace(*request.Order) != "" {
		return nil, providerUtils.NewBifrostOperationError("GigaChat file list does not support order sorting", nil)
	}
	if request.Limit != 0 {
		return nil, providerUtils.NewBifrostOperationError("GigaChat file list does not support limit pagination", nil)
	}
	if len(keys) == 0 {
		keys = []schemas.Key{{}}
	}

	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger, false)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err)
	}
	key, _, ok := helper.GetCurrentKey()
	if !ok {
		return &schemas.BifrostFileListResponse{
			Object: "list",
			Data:   []schemas.FileObject{},
		}, nil
	}

	response, bifrostErr := provider.fileListWithRefresh(ctx, key, request, false)
	if isGigaChatUnauthorizedError(bifrostErr) {
		response, bifrostErr = provider.fileListWithRefresh(ctx, key, request, true)
	}
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	nextCursor, hasMore := helper.BuildNextCursor(false, "")
	response.HasMore = hasMore
	if nextCursor != "" {
		response.After = &nextCursor
	}
	return response, nil
}

// FileRetrieve retrieves GigaChat file metadata.
func (provider *GigaChatProvider) FileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.FileRetrieveRequest); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("file retrieve request is nil", nil)
	}
	if strings.TrimSpace(request.FileID) == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil)
	}
	if len(keys) == 0 {
		keys = []schemas.Key{{}}
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		response, bifrostErr := provider.fileRetrieveWithRefresh(ctx, key, request, false)
		if isGigaChatUnauthorizedError(bifrostErr) {
			response, bifrostErr = provider.fileRetrieveWithRefresh(ctx, key, request, true)
		}
		if bifrostErr == nil {
			return response, nil
		}
		lastErr = bifrostErr
	}
	return nil, lastErr
}

// FileDelete deletes a GigaChat file.
func (provider *GigaChatProvider) FileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.FileDeleteRequest); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("file delete request is nil", nil)
	}
	if strings.TrimSpace(request.FileID) == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil)
	}
	if len(keys) == 0 {
		keys = []schemas.Key{{}}
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		response, bifrostErr := provider.fileDeleteWithRefresh(ctx, key, request, false)
		if isGigaChatUnauthorizedError(bifrostErr) {
			response, bifrostErr = provider.fileDeleteWithRefresh(ctx, key, request, true)
		}
		if bifrostErr == nil {
			return response, nil
		}
		lastErr = bifrostErr
	}
	return nil, lastErr
}

// FileContent downloads GigaChat file content.
func (provider *GigaChatProvider) FileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.GigaChat, provider.customProviderConfig, schemas.FileContentRequest); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("file content request is nil", nil)
	}
	if strings.TrimSpace(request.FileID) == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil)
	}
	if len(keys) == 0 {
		keys = []schemas.Key{{}}
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		response, bifrostErr := provider.fileContentWithRefresh(ctx, key, request, false)
		if isGigaChatUnauthorizedError(bifrostErr) {
			response, bifrostErr = provider.fileContentWithRefresh(ctx, key, request, true)
		}
		if bifrostErr == nil {
			return response, nil
		}
		lastErr = bifrostErr
	}
	return nil, lastErr
}

// CachedContentCreate is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) CachedContentCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostCachedContentCreateRequest) (*schemas.BifrostCachedContentCreateResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.CachedContentCreateRequest)
}

// CachedContentList is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) CachedContentList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentListRequest) (*schemas.BifrostCachedContentListResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.CachedContentListRequest)
}

// CachedContentRetrieve is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) CachedContentRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentRetrieveRequest) (*schemas.BifrostCachedContentRetrieveResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.CachedContentRetrieveRequest)
}

// CachedContentUpdate is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) CachedContentUpdate(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentUpdateRequest) (*schemas.BifrostCachedContentUpdateResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.CachedContentUpdateRequest)
}

// CachedContentDelete is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) CachedContentDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostCachedContentDeleteRequest) (*schemas.BifrostCachedContentDeleteResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.CachedContentDeleteRequest)
}

// ContainerCreate is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ContainerCreateRequest)
}

// ContainerList is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ContainerListRequest)
}

// ContainerRetrieve is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ContainerRetrieveRequest)
}

// ContainerDelete is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ContainerDeleteRequest)
}

// ContainerFileCreate is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ContainerFileCreateRequest)
}

// ContainerFileList is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ContainerFileListRequest)
}

// ContainerFileRetrieve is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ContainerFileRetrieveRequest)
}

// ContainerFileContent is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ContainerFileContentRequest)
}

// ContainerFileDelete is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.ContainerFileDeleteRequest)
}

// Passthrough is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.PassthroughRequest)
}

// PassthroughStream is not supported by the GigaChat provider skeleton.
func (provider *GigaChatProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, provider.unsupported(schemas.PassthroughStreamRequest)
}
