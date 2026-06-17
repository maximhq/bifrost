// Package tei implements the Hugging Face Text Embeddings Inference provider.
package tei

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TEIProvider implements Hugging Face Text Embeddings Inference's native API.
type TEIProvider struct {
	logger               schemas.Logger
	client               *fasthttp.Client
	networkConfig        schemas.NetworkConfig
	customProviderConfig *schemas.CustomProviderConfig
	sendBackRawRequest   bool
	sendBackRawResponse  bool
}

// NewTEIProvider creates a new Hugging Face Text Embeddings Inference provider.
func NewTEIProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*TEIProvider, error) {
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
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &TEIProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for TEI.
func (provider *TEIProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.TEI, provider.customProviderConfig)
}

// Rerank performs a rerank request using TEI's /rerank API.
func (provider *TEIProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	jsonData, bifrostErr := buildTEIRerankRequestBody(ctx, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/rerank", schemas.RerankRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	req.SetBody(jsonData)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	statusCode := resp.StatusCode()
	if statusCode != fasthttp.StatusOK {
		responseBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, parseTEIError(resp), jsonData, responseBody, sendBackRawRequest, sendBackRawResponse)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, teiProviderResponseError(schemas.ErrProviderResponseDecode, err, jsonData, append([]byte(nil), resp.Body()...), sendBackRawRequest, sendBackRawResponse, ctx)
	}

	var teiResponse []teiRank
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &teiResponse, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	returnDocuments := request.Params != nil && request.Params.ReturnDocuments != nil && *request.Params.ReturnDocuments
	var topN *int
	if request.Params != nil {
		topN = request.Params.TopN
	}
	bifrostResponse, err := ToBifrostRerankResponse(teiResponse, request.Documents, returnDocuments, topN)
	if err != nil {
		return nil, teiProviderResponseError("error converting rerank response", err, jsonData, body, sendBackRawRequest, sendBackRawResponse, ctx)
	}

	bifrostResponse.Model = request.Model
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawRequest {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

func (provider *TEIProvider) buildRequestURL(ctx *schemas.BifrostContext, defaultPath string, requestType schemas.RequestType) string {
	path, isCompleteURL := providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
	if isCompleteURL {
		return path
	}
	return provider.networkConfig.BaseURL + path
}

func buildTEIRerankRequestBody(ctx *schemas.BifrostContext, request *schemas.BifrostRerankRequest) ([]byte, *schemas.BifrostError) {
	if !providerUtils.IsLargePayloadPassthroughEnabled(ctx) {
		return providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (providerUtils.RequestBodyWithExtraParams, error) {
				return ToTEIRerankRequest(request), nil
			})
	}

	convertedBody := ToTEIRerankRequest(request)
	if convertedBody == nil {
		return nil, providerUtils.NewBifrostOperationError("request body is not provided", nil)
	}

	jsonBody, err := providerUtils.MarshalSortedIndent(convertedBody, "", "  ")
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}

	if ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
		extraParams := convertedBody.GetExtraParams()
		if len(extraParams) > 0 {
			jsonBody, err = providerUtils.MergeExtraParamsIntoJSON(jsonBody, extraParams)
			if err != nil {
				return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
			}
		}
	}

	return jsonBody, nil
}

func parseTEIError(resp *fasthttp.Response) *schemas.BifrostError {
	statusCode := resp.StatusCode()
	body, err := providerUtils.CheckAndDecodeBody(resp)
	message := strings.TrimSpace(string(body))
	errorType := ""
	if err == nil {
		var teiErr teiErrorResponse
		if unmarshalErr := sonic.Unmarshal(body, &teiErr); unmarshalErr == nil && teiErr.Error != "" {
			message = teiErr.Error
			errorType = teiErr.ErrorType
		}
	}
	if message == "" {
		message = "provider API error"
	}

	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: message,
			Type:    &errorType,
		},
	}
}

// Unsupported operations.
func (provider *TEIProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ListModelsRequest, provider.GetProviderKey())
}

func (provider *TEIProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

func (provider *TEIProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

func (provider *TEIProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, provider.GetProviderKey())
}

func (provider *TEIProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionStreamRequest, provider.GetProviderKey())
}
