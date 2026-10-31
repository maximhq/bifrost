// Package cloudflare implements the Cloudflare Workers AI provider.
// Chat completions and embeddings go to Workers AI's OpenAI-compatible endpoints.
package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// cloudflareModelsPageSize is the largest page Workers AI's model search serves.
const cloudflareModelsPageSize = 100

// CloudflareProvider implements the Provider interface for Cloudflare Workers AI.
type CloudflareProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient     *fasthttp.Client      // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewCloudflareProvider creates a new Cloudflare provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewCloudflareProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*CloudflareProvider, error) {
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
		config.NetworkConfig.BaseURL = "https://api.cloudflare.com/client/v4"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &CloudflareProvider{
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Cloudflare.
func (provider *CloudflareProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Cloudflare
}

// accountURLOrError returns the base URL scoped to the key's Cloudflare account, or a
// BifrostError when the key carries no account ID.
func (provider *CloudflareProvider) accountURLOrError(key schemas.Key) (string, *schemas.BifrostError) {
	if key.CloudflareKeyConfig == nil || key.CloudflareKeyConfig.AccountID.GetValue() == "" {
		return "", providerUtils.NewBifrostOperationError("no account ID configured: set cloudflare_key_config.account_id on the key", nil)
	}
	accountID, bifrostErr := providerUtils.EscapeResourceID(key.CloudflareKeyConfig.AccountID.GetValue(), "cloudflare_key_config.account_id")
	if bifrostErr != nil {
		return "", bifrostErr
	}
	return provider.networkConfig.BaseURL + "/accounts/" + accountID, nil
}

// listModelsByKey lists the models of a single key's Cloudflare account, reading every
// page of the Workers AI model search.
func (provider *CloudflareProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	accountURL, bifrostErr := provider.accountURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var allModels []CloudflareModel
	var rawResponses []interface{}
	var totalLatency time.Duration
	for page := 1; ; page++ {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(fmt.Sprintf("%s%s?per_page=%d&page=%d", accountURL, providerUtils.GetPathFromContext(ctx, "/ai/models/search"), cloudflareModelsPageSize, page))
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")
		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		totalLatency += latency
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return nil, bifrostErr
		}

		if resp.StatusCode() != fasthttp.StatusOK {
			bifrostErr := providerUtils.SetErrorLatency(parseCloudflareError(resp), totalLatency)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return nil, bifrostErr
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		if err != nil {
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), totalLatency)
		}
		var pageResponse CloudflareListModelsResponse
		_, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &pageResponse, nil, false, sendBackRawResponse)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		if sendBackRawResponse {
			rawResponses = append(rawResponses, rawResponse)
		}

		allModels = append(allModels, pageResponse.Result...)
		if len(pageResponse.Result) < cloudflareModelsPageSize {
			break
		}
	}

	aggregatedResponse := &CloudflareListModelsResponse{Result: allModels}
	response := aggregatedResponse.ToBifrostListModelsResponse(key.Models, key.BlacklistedModels, key.Aliases, request.Unfiltered)

	response.ExtraFields.Latency = totalLatency.Milliseconds()

	if sendBackRawResponse {
		response.ExtraFields.RawResponse = rawResponses
	}

	return response, nil
}

// ListModels performs a list models request to Cloudflare Workers AI.
// Requests are made concurrently per key so that each key is queried
// with its own account (from cloudflare_key_config).
func (provider *CloudflareProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}

// ChatCompletion performs a chat completion request to Cloudflare's OpenAI-compatible API.
func (provider *CloudflareProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	accountURL, bifrostErr := provider.accountURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		accountURL+providerUtils.GetPathFromContext(ctx, "/ai/v1/chat/completions"),
		request,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		parseCloudflareError,
		nil,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request to Cloudflare's
// OpenAI-compatible API, using Server-Sent Events (SSE).
// Returns a channel containing BifrostStreamChunk objects representing the stream or an error if the request fails.
func (provider *CloudflareProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	accountURL, bifrostErr := provider.accountURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.streamingClient,
		accountURL+providerUtils.GetPathFromContext(ctx, "/ai/v1/chat/completions"),
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
		parseCloudflareError,
		nil,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// Responses performs a responses request to Cloudflare through its chat completions API.
func (provider *CloudflareProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	chatResponse, err := provider.ChatCompletion(ctx, key, request.ToChatRequest())
	if err != nil {
		return nil, err
	}

	response := chatResponse.ToBifrostResponsesResponse()

	return response, nil
}

// ResponsesStream performs a streaming responses request to Cloudflare through its chat completions API.
func (provider *CloudflareProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
	return provider.ChatCompletionStream(
		ctx,
		postHookRunner,
		postHookSpanFinalizer,
		key,
		request.ToChatRequest(),
	)
}

// Embedding performs an embedding request to Cloudflare's OpenAI-compatible API.
func (provider *CloudflareProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	accountURL, bifrostErr := provider.accountURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return openai.HandleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		accountURL+providerUtils.GetPathFromContext(ctx, "/ai/v1/embeddings"),
		request,
		openai.BearerAuthHeader(key),
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		nil,
		parseCloudflareError,
		provider.logger,
	)
}

// Decision runs a Clef decision on Workers AI. Clef binds the model to the URL and requires
// the same bare name in the body. Every model is sent as is; Workers AI rejects one it does
// not serve, and that error is passed through.
func (provider *CloudflareProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	accountURL, bifrostErr := provider.accountURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToCloudflareDecisionRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(accountURL + providerUtils.GetPathFromContext(ctx, cloudflareDecisionPathPrefix+clefModelName(request.Model)))
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

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseCloudflareError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	var decisionResp CloudflareDecisionResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &decisionResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if !decisionResp.Success || decisionResp.Result == nil {
		return nil, providerUtils.EnrichError(ctx, parseCloudflareError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp, bifrostErr := typesafe.ToBifrostDecisionResponse(decisionResp.Result, request)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	bifrostResp.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}
