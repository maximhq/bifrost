// Package anthropic implements the Anthropic provider for the Bifrost API.
package anthropic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// AnthropicProvider implements the Provider interface for Anthropic's Claude API.
type AnthropicProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient      *fasthttp.Client              // HTTP client for streaming API requests (no ReadTimeout; idle governed by NewIdleTimeoutReader)
	apiVersion           string                        // API version for the provider
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawRequest   bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// anthropicMessageResponsePool provides a pool for Anthropic chat response objects.
var anthropicMessageResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicMessageResponse{}
	},
}

// anthropicTextResponsePool provides a pool for Anthropic text response objects.
var anthropicTextResponsePool = sync.Pool{
	New: func() interface{} {
		return &AnthropicTextResponse{}
	},
}

// AcquireAnthropicMessageResponse gets an Anthropic chat response from the pool.
func AcquireAnthropicMessageResponse() *AnthropicMessageResponse {
	resp := anthropicMessageResponsePool.Get().(*AnthropicMessageResponse)
	*resp = AnthropicMessageResponse{} // Reset the struct
	return resp
}

// ReleaseAnthropicMessageResponse returns an Anthropic chat response to the pool.
func ReleaseAnthropicMessageResponse(resp *AnthropicMessageResponse) {
	if resp != nil {
		anthropicMessageResponsePool.Put(resp)
	}
}

// acquireAnthropicTextResponse gets an Anthropic text response from the pool.
func acquireAnthropicTextResponse() *AnthropicTextResponse {
	resp := anthropicTextResponsePool.Get().(*AnthropicTextResponse)
	*resp = AnthropicTextResponse{} // Reset the struct
	return resp
}

// releaseAnthropicTextResponse returns an Anthropic text response to the pool.
func releaseAnthropicTextResponse(resp *AnthropicTextResponse) {
	if resp != nil {
		anthropicTextResponsePool.Put(resp)
	}
}

// NewAnthropicProvider creates a new Anthropic provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewAnthropicProvider(config *schemas.ProviderConfig, logger schemas.Logger) *AnthropicProvider {
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

	// Pre-warm response pools
	for i := 0; i < config.ConcurrencyAndBufferSize.Concurrency; i++ {
		anthropicTextResponsePool.Put(&AnthropicTextResponse{})
		anthropicMessageResponsePool.Put(&AnthropicMessageResponse{})
	}

	// Configure proxy and retry policy
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)
	// Set default BaseURL if not provided
	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = "https://api.anthropic.com"
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &AnthropicProvider{
		logger:               logger,
		client:               client,
		streamingClient:      streamingClient,
		apiVersion:           "2023-06-01",
		networkConfig:        config.NetworkConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}
}

// GetProviderKey returns the provider identifier for Anthropic.
func (provider *AnthropicProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Anthropic, provider.customProviderConfig)
}

// buildRequestURL constructs the full request URL using the provider's configuration.
func (provider *AnthropicProvider) buildRequestURL(ctx *schemas.BifrostContext, defaultPath string, requestType schemas.RequestType) string {
	path, isCompleteURL := providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
	if isCompleteURL {
		return path
	}
	return provider.networkConfig.BaseURL + path
}

func setAnthropicRequestBody(ctx *schemas.BifrostContext, req *fasthttp.Request, body []byte) bool {
	// Keep one request-body path for both modes:
	// - normal mode: send converted JSON/multipart bytes
	// - large payload mode: stream original client body reader
	// Example failure prevented: duplicating large uploads in memory after passthrough
	// was already activated at transport layer.
	usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, schemas.Anthropic)
	if !usedLargePayloadBody {
		req.SetBody(body)
	}
	return usedLargePayloadBody
}

func extractAnthropicResponsesUsageFromPrefetch(data []byte) *schemas.ResponsesResponseUsage {
	node, err := sonic.Get(data, "usage")
	if err != nil {
		return nil
	}
	raw, _ := node.Raw()
	if raw == "" {
		return nil
	}
	var usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	if err := sonic.UnmarshalString(raw, &usage); err != nil {
		return nil
	}
	return &schemas.ResponsesResponseUsage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.InputTokens + usage.OutputTokens,
	}
}

// anthropicRequestHeaders builds the auth/version headers for an Anthropic request: the API
// version plus x-api-key when a key is present and Claude Code max-mode is off. Shared by the
// provider's unary (completeRequest / HandleAnthropic*Request) and streaming paths.
func (provider *AnthropicProvider) anthropicRequestHeaders(ctx *schemas.BifrostContext, key schemas.Key) map[string]string {
	headers := map[string]string{
		"anthropic-version": provider.apiVersion,
	}
	if key.Value.GetValue() != "" && !IsClaudeCodeMaxMode(ctx) {
		headers["x-api-key"] = key.Value.GetValue()
	}
	return headers
}

// completeRequest sends a non-streaming Anthropic Messages API request over fasthttp and
// returns the buffered response body, latency, and provider response headers. It is the single
// unary request core for the package: the exported HandleAnthropic*Request handlers call it
// (then parse), and the provider's TextCompletion / CountTokens paths call it directly. It
// mirrors the request shaping of HandleAnthropicChatCompletionStreaming (header application,
// beta filtering, large-payload request body, large-response detection) but reads a full
// response instead of a stream. Auth and version headers are supplied by the caller via the
// headers map. On a large response it returns a nil body and signals via the
// BifrostContextKeyLargeResponseMode context value (set by FinalizeResponseWithLargeDetection).
func completeRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	jsonBody []byte,
	headers map[string]string,
	extraHeaders map[string]string,
	betaHeaderOverrides map[string]bool,
	providerName schemas.ModelProvider,
	requestType schemas.RequestType,
	signer providerUtils.BodySigner,
	logger schemas.Logger,
) ([]byte, time.Duration, map[string]string, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)

	// Set network-config extra headers, excluding anthropic-beta (set explicitly below).
	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, []string{AnthropicBetaHeader})

	// Force JSON content type after extra headers so network config can't override it
	// (matches the original per-provider completeRequest ordering).
	req.Header.SetContentType("application/json")

	if betaHeaders := FilterBetaHeadersForProvider(MergeBetaHeaders(ctx, extraHeaders), providerName, betaHeaderOverrides); len(betaHeaders) > 0 {
		req.Header.Set(AnthropicBetaHeader, strings.Join(betaHeaders, ","))
	} else {
		req.Header.Del(AnthropicBetaHeader)
	}

	// Apply caller-supplied auth/version headers last so they win over network-config headers.
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	usedLargePayloadBody := setAnthropicRequestBody(ctx, req, jsonBody)

	// Sign the exact body bytes set on the request, when a signer is supplied (e.g. AWS SigV4
	// for Bedrock Mantle). Done after the body is set so the signature covers what is actually sent.
	if signer != nil {
		sigHeaders, bErr := signer(jsonBody)
		if bErr != nil {
			return nil, 0, nil, bErr
		}
		for k, v := range sigHeaders {
			req.Header.Set(k, v)
		}
	}

	requestClient := client
	responseThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseThreshold).(int64)
	isCountTokens := requestType == schemas.CountTokensRequest
	// Count-tokens responses are always tiny — skip the large-response streaming client so the
	// response is buffered normally.
	if responseThreshold > 0 && !isCountTokens {
		resp.StreamBody = true
		requestClient = providerUtils.BuildLargeResponseClient(client, responseThreshold)
	}

	// Send the request
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, requestClient, req, resp)
	defer wait()
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	if bifrostErr != nil {
		return nil, latency, nil, bifrostErr
	}

	// Extract provider response headers before status check so error responses also forward them
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)

	// Handle error response — materialize stream body for error parsing
	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, latency, providerResponseHeaders, providerUtils.SetErrorLatency(parseAnthropicError(resp), latency)
	}

	// Count-tokens uses a buffered response (large-response streaming skipped above).
	if isCountTokens {
		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			return nil, latency, providerResponseHeaders, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		}
		return body, latency, providerResponseHeaders, nil
	}

	// Delegate large response detection + normal buffered path to shared utility
	body, isLarge, respErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, logger)
	if respErr != nil {
		return nil, latency, providerResponseHeaders, respErr
	}
	if isLarge {
		respOwned = false
		return nil, latency, providerResponseHeaders, nil
	}
	return body, latency, providerResponseHeaders, nil
}

// listModelsByKey performs a list models request for a single key.
// Returns the response and latency, or an error if the request fails.
func (provider *AnthropicProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set any extra headers from network config
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Build URL using centralized URL construction
	req.SetRequestURI(provider.buildRequestURL(ctx, fmt.Sprintf("/v1/models?limit=%d", schemas.DefaultPageSize), schemas.ListModelsRequest))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("x-api-key", key.Value.GetValue())
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Make request
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Store provider response headers in context before status check so error responses also forward them
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.SetErrorLatency(parseAnthropicError(resp), latency)
	}

	// Parse Anthropic's response
	var anthropicResponse AnthropicListModelsResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(resp.Body(), &anthropicResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Create final response
	response := anthropicResponse.ToBifrostListModelsResponse(provider.GetProviderKey(), key.Models, key.BlacklistedModels, key.Aliases, request.Unfiltered)
	response.ExtraFields.Latency = latency.Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ListModels performs a list models request to Anthropic's API.
// It fetches models using all provided keys and aggregates the results.
// Uses a best-effort approach: continues with remaining keys even if some fail.
// Requests are made concurrently for improved performance.
func (provider *AnthropicProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
		return providerUtils.HandleKeylessListModelsRequest(schemas.Anthropic, func() (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
			return provider.listModelsByKey(ctx, schemas.Key{Models: schemas.WhiteList{"*"}}, request)
		})
	}
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}

// TextCompletion performs a text completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.TextCompletionRequest); err != nil {
		return nil, err
	}

	// Convert to Anthropic format using the centralized converter
	jsonData, err := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToAnthropicTextCompletionRequest(request), nil
		})
	if err != nil {
		return nil, err
	}

	// Use struct directly for JSON marshaling (no beta headers for text completion)
	responseBody, latency, providerResponseHeaders, err := completeRequest(ctx, provider.client, provider.buildRequestURL(ctx, "/v1/complete", schemas.TextCompletionRequest), jsonData, provider.anthropicRequestHeaders(ctx, key), provider.networkConfig.ExtraHeaders, provider.networkConfig.BetaHeaderOverrides, provider.GetProviderKey(), schemas.TextCompletionRequest, nil, provider.logger)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Large response mode: return lightweight response with metadata only
	if isLargeResp, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); isLargeResp {
		return &schemas.BifrostTextCompletionResponse{
			Model: request.Model,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerResponseHeaders,
			},
		}, nil
	}

	// Create response object from pool
	response := acquireAnthropicTextResponse()
	defer releaseAnthropicTextResponse(response)

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	bifrostResponse := response.ToBifrostTextCompletionResponse()

	// Set ExtraFields
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// TextCompletionStream performs a streaming text completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a channel of BifrostStreamChunk objects or an error if the request fails.
func (provider *AnthropicProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}
	return HandleAnthropicChatCompletionRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/messages", schemas.ChatCompletionRequest),
		request,
		AnthropicRequestBuildConfig{
			Provider:                  schemas.Anthropic,
			IsStreaming:               false,
			BetaHeaderOverrides:       provider.networkConfig.BetaHeaderOverrides,
			ShouldSendBackRawRequest:  provider.sendBackRawRequest,
			ShouldSendBackRawResponse: provider.sendBackRawResponse,
		},
		provider.anthropicRequestHeaders(ctx, key),
		provider.networkConfig.ExtraHeaders,
		nil,
		provider.logger,
	)
}

// HandleAnthropicChatCompletionRequest builds the Anthropic Messages chat request body from
// config, performs a non-streaming request, and parses the native Anthropic response into a
// BifrostChatResponse. It is the unary counterpart to HandleAnthropicChatCompletionStreaming,
// shared by the Anthropic, Azure, Vertex, and Bedrock providers. Callers supply the request,
// the per-provider build config (Provider, Model, ShouldSendBackRaw*, BetaHeaderOverrides),
// the request URL, and the auth/version headers (x-api-key, Bearer, SigV4, anthropic-version)
// via the headers map; beta headers are filtered for config.Provider.
func HandleAnthropicChatCompletionRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostChatRequest,
	config AnthropicRequestBuildConfig,
	headers map[string]string,
	extraHeaders map[string]string,
	signer providerUtils.BodySigner,
	logger schemas.Logger,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	jsonBody, bifrostErr := BuildAnthropicChatRequestBody(ctx, request, config)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Use struct directly for JSON marshaling
	responseBody, latency, providerResponseHeaders, bifrostErr := completeRequest(ctx, client, url, jsonBody, headers, extraHeaders, config.BetaHeaderOverrides, config.Provider, schemas.ChatCompletionRequest, signer, logger)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, config.ShouldSendBackRawRequest, config.ShouldSendBackRawResponse, latency)
	}

	// Large response mode: return lightweight response with metadata only.
	if isLargeResp, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); isLargeResp {
		return &schemas.BifrostChatResponse{
			Model: request.Model,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerResponseHeaders,
			},
		}, nil
	}

	// Create response object from pool
	response := AcquireAnthropicMessageResponse()
	defer ReleaseAnthropicMessageResponse(response)

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, config.ShouldSendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, config.ShouldSendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, config.ShouldSendBackRawRequest, config.ShouldSendBackRawResponse, latency)
	}
	// Create final response
	bifrostResponse := response.ToBifrostChatResponse(ctx)

	// Set ExtraFields
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, config.ShouldSendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}
	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, config.ShouldSendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResponse, nil
}

// ChatCompletionStream performs a streaming chat completion request to the Anthropic API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Returns a channel containing BifrostStreamChunk objects representing the stream or an error if the request fails.
func (provider *AnthropicProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	jsonData, bifrostErr := BuildAnthropicChatRequestBody(ctx, request, AnthropicRequestBuildConfig{
		Provider:                  schemas.Anthropic,
		IsStreaming:               true,
		ShouldSendBackRawRequest:  provider.sendBackRawRequest,
		ShouldSendBackRawResponse: provider.sendBackRawResponse,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Prepare Anthropic headers
	headers := map[string]string{
		"Content-Type":      "application/json",
		"anthropic-version": provider.apiVersion,
		"Accept":            "text/event-stream",
		"Cache-Control":     "no-cache",
	}

	if key.Value.GetValue() != "" && !IsClaudeCodeMaxMode(ctx) {
		headers["x-api-key"] = key.Value.GetValue()
	}

	// Use shared Anthropic streaming logic
	return HandleAnthropicChatCompletionStreaming(
		ctx,
		provider.streamingClient,
		provider.buildRequestURL(ctx, "/v1/messages", schemas.ChatCompletionStreamRequest),
		jsonData,
		headers,
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		provider.networkConfig.BetaHeaderOverrides,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// normalizeCachedUsage folds the accumulated cached read/write token counts into
// the top-level prompt and total counters. Anthropic reports these per-request
// totals only after the full stream, so the streaming accumulator must apply the
// same fold before billing - including on a mid-stream cancel/timeout. The += is
// not idempotent; callers guard with a flag to apply it exactly once.
//
// NOTE: this fold is ONLY correct when usage.PromptTokens still holds the
// PRELIMINARY message_start.input_tokens (the full prompt before the cache
// breakdown is known). When applyStreamUsageEvent has already processed a
// message_delta event, the cache breakdown is already folded into PromptTokens
// and the caller MUST skip this fold (else it double-counts). The streaming
// chat handler tracks this via the deltaProcessed flag.
func normalizeCachedUsage(usage *schemas.BifrostLLMUsage) {
	if usage == nil || usage.PromptTokensDetails == nil {
		return
	}
	cached := usage.PromptTokensDetails.CachedReadTokens + usage.PromptTokensDetails.CachedWriteTokens
	usage.PromptTokens += cached
	usage.TotalTokens += cached
}

// applyStreamUsageEvent folds a single Anthropic stream event's usage snapshot
// into the accumulating Bifrost usage and returns whether the event was a
// message_delta whose usage was applied as the authoritative final snapshot.
//
// Per the Anthropic Messages API streaming spec, a stream emits two usage
// snapshots:
//   - message_start: preliminary usage nested under message.usage; the full
//     prompt as input_tokens, cache fields typically still zero because the
//     cache breakdown is not yet computed.
//   - message_delta: authoritative post-cache breakdown; input_tokens is the
//     UNCACHED tail, and cache_creation_input_tokens + cache_read_input_tokens
//     decompose the cached portion of the same total.
//
// message_delta.usage OVERWRITES message_start.usage wholesale (it does NOT
// merge-with or add-to). The authoritative prompt total is
//
//	delta.input_tokens + delta.cache_creation_input_tokens + delta.cache_read_input_tokens.
//
// All input-side fields on both BifrostLLMUsage and AnthropicUsage are
// non-pointer Go int, so JSON deserialization cannot distinguish "field absent"
// from "field present-but-zero" — this is why the message_delta branch uses an
// EVENT-LEVEL unconditional overwrite rather than per-field `if != 0` checks
// (which would wrongly skip a legitimately-zero input_tokens on an
// all-cache-hit delta).
//
// startSnapshot is the message_start event's usage, captured by the caller; it
// backs the impossible-zero guard. A conformant Anthropic prompt always has
// PromptTokens >= 1 (input_tokens >= 1 for any non-empty request), so
// PromptTokens == 0 after the overwrite is a DEFINITIVE signal of a
// non-conformant Anthropic-compatible provider that emitted a partial-usage
// message_delta (input_tokens=0 with cache fields omitted, deserializing to
// zero for non-pointer Go int). The guard restores the start-side snapshot as
// the best available approximation. The guard is DEAD CODE for spec-conformant
// providers (zero behavior change).
//
// The caller MUST skip the end-of-stream normalizeCachedUsage fold when this
// returns true, because the cache breakdown is already folded into
// PromptTokens by the overwrite (folding again would double-count).
func applyStreamUsageEvent(
	usage *schemas.BifrostLLMUsage,
	eventType AnthropicStreamEventType,
	usageToProcess *AnthropicUsage,
	startSnapshot *AnthropicUsage,
) bool {
	if usage == nil || usageToProcess == nil {
		return false
	}

	if eventType == AnthropicStreamEventTypeMessageDelta {
		// Authoritative final snapshot: overwrite input-side fields wholesale.
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
		}
		usage.PromptTokensDetails.CachedReadTokens = usageToProcess.CacheReadInputTokens
		usage.PromptTokensDetails.CachedWriteTokens = usageToProcess.CacheCreationInputTokens
		if usageToProcess.CacheCreation.Ephemeral5mInputTokens > 0 || usageToProcess.CacheCreation.Ephemeral1hInputTokens > 0 {
			if usage.PromptTokensDetails.CachedWriteTokenDetails == nil {
				usage.PromptTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{}
			}
			if usageToProcess.CacheCreation.Ephemeral5mInputTokens > usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m {
				usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m = usageToProcess.CacheCreation.Ephemeral5mInputTokens
			}
			if usageToProcess.CacheCreation.Ephemeral1hInputTokens > usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h {
				usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h = usageToProcess.CacheCreation.Ephemeral1hInputTokens
			}
		}
		// Reconstruct the authoritative prompt total from delta's three
		// components (uncached tail + cache creation + cache read).
		usage.PromptTokens = usageToProcess.InputTokens + usageToProcess.CacheReadInputTokens + usageToProcess.CacheCreationInputTokens
		// OutputTokens on message_delta is the cumulative output count.
		if usageToProcess.OutputTokens > usage.CompletionTokens {
			usage.CompletionTokens = usageToProcess.OutputTokens
		}
		// Impossible-zero guard: restore start-side when a non-conformant
		// provider emits a partial-usage delta (input=0 + cache fields omitted).
		if usage.PromptTokens == 0 && startSnapshot != nil && startSnapshot.InputTokens > 0 {
			usage.PromptTokens = startSnapshot.InputTokens
			usage.PromptTokensDetails.CachedReadTokens = startSnapshot.CacheReadInputTokens
			usage.PromptTokensDetails.CachedWriteTokens = startSnapshot.CacheCreationInputTokens
		}
		calculatedTotal := usage.PromptTokens + usage.CompletionTokens
		if calculatedTotal > usage.TotalTokens {
			usage.TotalTokens = calculatedTotal
		}
		return true
	}

	// message_start (or any non-delta event carrying usage): max-keep capture.
	// This serves as the fallback when message_delta never arrives (early
	// stream termination); normalizeCachedUsage folds cache into prompt at
	// stream end for that case via the normalizeUsage closure.
	if usageToProcess.InputTokens > usage.PromptTokens {
		usage.PromptTokens = usageToProcess.InputTokens
	}
	if usageToProcess.OutputTokens > usage.CompletionTokens {
		usage.CompletionTokens = usageToProcess.OutputTokens
	}
	calculatedTotal := usage.PromptTokens + usage.CompletionTokens
	if calculatedTotal > usage.TotalTokens {
		usage.TotalTokens = calculatedTotal
	}
	// Handle cached tokens if present
	if usageToProcess.CacheReadInputTokens > 0 {
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
		}
		if usageToProcess.CacheReadInputTokens > usage.PromptTokensDetails.CachedReadTokens {
			usage.PromptTokensDetails.CachedReadTokens = usageToProcess.CacheReadInputTokens
		}
	}
	if usageToProcess.CacheCreationInputTokens > 0 {
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
		}
		if usageToProcess.CacheCreationInputTokens > usage.PromptTokensDetails.CachedWriteTokens {
			usage.PromptTokensDetails.CachedWriteTokens = usageToProcess.CacheCreationInputTokens
		}
		if usageToProcess.CacheCreation.Ephemeral5mInputTokens > 0 || usageToProcess.CacheCreation.Ephemeral1hInputTokens > 0 {
			if usage.PromptTokensDetails.CachedWriteTokenDetails == nil {
				usage.PromptTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{}
			}
			if usageToProcess.CacheCreation.Ephemeral5mInputTokens > usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m {
				usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m = usageToProcess.CacheCreation.Ephemeral5mInputTokens
			}
			if usageToProcess.CacheCreation.Ephemeral1hInputTokens > usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h {
				usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h = usageToProcess.CacheCreation.Ephemeral1hInputTokens
			}
		}
	}
	return false
}

func accumulateAnthropicResponsesUsage(usage *schemas.ResponsesResponseUsage, billedUsage *schemas.BifrostLLMUsage, usageToProcess *AnthropicUsage) {
	if usage == nil || usageToProcess == nil {
		return
	}
	// Web search request count → billed as search queries (server tool use). The
	// terminal chunk overwrites Response.Usage with this accumulator, so the count
	// must live here (not only on the per-event message_delta usage).
	if usageToProcess.ServerToolUse != nil && usageToProcess.ServerToolUse.WebSearchRequests > 0 {
		n := usageToProcess.ServerToolUse.WebSearchRequests
		if usage.OutputTokensDetails == nil {
			usage.OutputTokensDetails = &schemas.ResponsesResponseOutputTokens{}
		}
		if usage.OutputTokensDetails.NumSearchQueries == nil || n > *usage.OutputTokensDetails.NumSearchQueries {
			usage.OutputTokensDetails.NumSearchQueries = schemas.Ptr(n)
		}
		if billedUsage != nil {
			if billedUsage.CompletionTokensDetails == nil {
				billedUsage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{}
			}
			if billedUsage.CompletionTokensDetails.NumSearchQueries == nil || n > *billedUsage.CompletionTokensDetails.NumSearchQueries {
				billedUsage.CompletionTokensDetails.NumSearchQueries = schemas.Ptr(n)
			}
		}
	}
	if usageToProcess.InputTokens > usage.InputTokens {
		usage.InputTokens = usageToProcess.InputTokens
		if billedUsage != nil {
			billedUsage.PromptTokens = usageToProcess.InputTokens
		}
	}
	if usageToProcess.OutputTokens > usage.OutputTokens {
		usage.OutputTokens = usageToProcess.OutputTokens
		if billedUsage != nil {
			billedUsage.CompletionTokens = usageToProcess.OutputTokens
		}
	}
	calculatedTotal := usage.InputTokens + usage.OutputTokens
	if calculatedTotal > usage.TotalTokens {
		usage.TotalTokens = calculatedTotal
		if billedUsage != nil {
			billedUsage.TotalTokens = calculatedTotal
		}
	}
	// Handle cached tokens if present
	if usageToProcess.CacheReadInputTokens > 0 {
		if usage.InputTokensDetails == nil {
			usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
		}
		if billedUsage != nil && billedUsage.PromptTokensDetails == nil {
			billedUsage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
		}
		if usageToProcess.CacheReadInputTokens > usage.InputTokensDetails.CachedReadTokens {
			usage.InputTokensDetails.CachedReadTokens = usageToProcess.CacheReadInputTokens
			if billedUsage != nil {
				billedUsage.PromptTokensDetails.CachedReadTokens = usageToProcess.CacheReadInputTokens
			}
		}
	}
	// Handle cached tokens if present
	if usageToProcess.CacheCreationInputTokens > 0 {
		if usage.InputTokensDetails == nil {
			usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
		}
		if billedUsage != nil && billedUsage.PromptTokensDetails == nil {
			billedUsage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
		}
		if usageToProcess.CacheCreationInputTokens > usage.InputTokensDetails.CachedWriteTokens {
			usage.InputTokensDetails.CachedWriteTokens = usageToProcess.CacheCreationInputTokens
			if billedUsage != nil {
				billedUsage.PromptTokensDetails.CachedWriteTokens = usageToProcess.CacheCreationInputTokens
			}
		}
		if usageToProcess.CacheCreation.Ephemeral5mInputTokens > 0 || usageToProcess.CacheCreation.Ephemeral1hInputTokens > 0 {
			if usage.InputTokensDetails.CachedWriteTokenDetails == nil {
				usage.InputTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{}
			}
			if billedUsage != nil && billedUsage.PromptTokensDetails.CachedWriteTokenDetails == nil {
				billedUsage.PromptTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{}
			}
			if usageToProcess.CacheCreation.Ephemeral5mInputTokens > usage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m {
				usage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m = usageToProcess.CacheCreation.Ephemeral5mInputTokens
				if billedUsage != nil {
					billedUsage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m = usageToProcess.CacheCreation.Ephemeral5mInputTokens
				}
			}
			if usageToProcess.CacheCreation.Ephemeral1hInputTokens > usage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h {
				usage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h = usageToProcess.CacheCreation.Ephemeral1hInputTokens
				if billedUsage != nil {
					billedUsage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h = usageToProcess.CacheCreation.Ephemeral1hInputTokens
				}
			}
		}
	}
}

// HandleAnthropicChatCompletionStreaming handles streaming for Anthropic-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE event format.
func HandleAnthropicChatCompletionStreaming(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	jsonBody []byte,
	headers map[string]string,
	extraHeaders map[string]string,
	streamIdleTimeoutInSeconds int,
	betaHeaderOverrides map[string]bool,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	postResponseConverter func(*schemas.BifrostChatResponse) *schemas.BifrostChatResponse,
	signer providerUtils.BodySigner,
	logger schemas.Logger,
	postHookSpanFinalizer func(context.Context),
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, streamIdleTimeoutInSeconds)
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true // Initialize for streaming
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, []string{AnthropicBetaHeader})

	if betaHeaders := FilterBetaHeadersForProvider(MergeBetaHeaders(ctx, extraHeaders), providerName, betaHeaderOverrides); len(betaHeaders) > 0 {
		req.Header.Set(AnthropicBetaHeader, strings.Join(betaHeaders, ","))
	} else {
		req.Header.Del(AnthropicBetaHeader)
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	usedLargePayloadBody := setAnthropicRequestBody(ctx, req, jsonBody)

	// Sign the exact body bytes set on the request, when a signer is supplied (e.g. AWS SigV4
	// for Bedrock Mantle). Done after the body is set so the signature covers what is actually sent.
	if signer != nil {
		sigHeaders, bErr := signer(jsonBody)
		if bErr != nil {
			defer providerUtils.ReleaseStreamingResponse(ctx, resp)
			return nil, bErr
		}
		for k, v := range sigHeaders {
			req.Header.Set(k, v)
		}
	}

	// Close the connection after this streaming response instead of returning it to
	// the keep-alive pool. fasthttp can otherwise reuse a streaming connection whose
	// reader is still in a torn state (body not fully drained, or the idle-timeout/
	// consumer goroutine still attached), corrupting the next request's RoundTrip and
	// panicking with "slice bounds out of range" in mustPeekBuffered. Mirrors the
	// mitigation already used on Gemini/Azure/OpenAI streaming and anthropic.go:2716.
	req.Header.Set("Connection", "close")

	// Use a fresh streaming client per request so fasthttp's internal readerPool
	// cannot be poisoned across concurrent Anthropic streams by a non-idempotent
	// streaming-body close path. The cloned client preserves dialer/TLS/proxy
	// config but starts with empty reader/writer pools.
	requestClient := providerUtils.BuildStreamingClient(client)

	// Use streaming-aware client when large payload optimization is active — ensures
	// MaxResponseBodySize > 0 so ErrBodyTooLarge triggers StreamBody for Content-Length responses.
	activeClient := providerUtils.PrepareResponseStreaming(ctx, requestClient, resp)

	startTime := time.Now()
	// Make the request
	err := activeClient.Do(req, resp)
	latency := time.Since(startTime)
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	if err != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		// Request failed before the first response byte (server closed an idle/pooled connection,
		// broken pipe, connection refused, DNS failure, etc.). Surface as a retriable upstream
		// connection error (502) so executeRequestWithRetries honors max_retries, matching the
		// non-streaming path - see https://github.com/maximhq/bifrost/issues/4496.
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	// Store provider response headers in context before status check so error responses also forward them
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, providerUtils.EnrichError(ctx, parseAnthropicError(resp), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	// Large payload streaming passthrough — pipe raw upstream SSE to client
	if providerUtils.SetupStreamingPassthrough(ctx, resp) {
		responseChan := make(chan *schemas.BifrostStreamChunk)
		providerUtils.CloseStream(ctx, responseChan)
		return responseChan, nil
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, logger, postHookSpanFinalizer, jsonBody)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, logger, postHookSpanFinalizer, jsonBody)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)

		if resp.BodyStream() == nil {
			bifrostErr := providerUtils.NewBifrostOperationError(
				"Provider returned an empty response",
				fmt.Errorf("provider returned an empty response"),
			)
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency), responseChan, logger, postHookSpanFinalizer)
			return
		}

		// Decompress gzip-encoded streams transparently (no-op for non-gzip)
		reader, releaseGzip := providerUtils.DecompressStreamBody(resp)
		defer releaseGzip()

		// Wrap reader with idle timeout to detect stalled streams.
		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(reader, resp.BodyStream(), providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		// Setup cancellation handler to close the raw network stream on ctx cancellation,
		// which immediately unblocks any in-progress read (including reads blocked inside a gzip decompression layer).
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		sseReader := providerUtils.GetSSEEventReader(ctx, reader)

		chunkIndex := 0

		lastChunkTime := startTime

		// Track minimal state needed for response format
		var messageID string
		var modelName string
		var finishReason *string

		usage := &schemas.BifrostLLMUsage{}
		// startSnapshot captures the message_start event's preliminary usage; it
		// backs the impossible-zero guard in applyStreamUsageEvent (restoring the
		// start-side when a non-conformant provider emits a partial-usage delta)
		// and the delta-absent fallback when the stream ends before message_delta.
		var startSnapshot *AnthropicUsage
		// deltaProcessed is set true once applyStreamUsageEvent processes a
		// message_delta event (the authoritative final snapshot). When true the
		// normalizeUsage closure skips the normalizeCachedUsage fold because the
		// cache breakdown is already folded into PromptTokens by the overwrite.
		deltaProcessed := false
		// Served billing modifiers (top-level response fields, not usage) captured
		// across events and set on the final chunk: fast mode and data residency.
		var servedSpeed *string
		var servedInferenceGeo *string
		// Register the accumulating usage handle so a mid-stream cancel/timeout
		// can bill for tokens already processed Mutated in place below;
		// the deferred HandleStreamCancellation/Timeout reads it from context.
		ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, usage)

		// Fold cached tokens into prompt/total exactly once at stream end. The EOF
		// path calls normalizeUsage() after the loop; on a mid-stream cancel/timeout
		// the deferred call below runs first (LIFO, registered after the cancellation
		// handler at the top of the goroutine) so HandleStreamCancellation/Timeout
		// bills the normalized totals. The fold is SKIPPED when a message_delta
		// event already reconstructed PromptTokens from the authoritative cache
		// breakdown (deltaProcessed) — folding again would double-count.
		usageNormalized := false
		normalizeUsage := func() {
			if usageNormalized {
				return
			}
			usageNormalized = true
			if !deltaProcessed {
				normalizeCachedUsage(usage)
			}
		}
		defer func() {
			if ctx.Err() != nil {
				normalizeUsage()
			}
		}()

		// Check for structured output tool name and track state
		var structuredOutputToolName string
		var isAccumulatingStructuredOutput bool
		var consumedStructuredOutput bool // true once the SO tool block has been fully streamed as content
		if toolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
			structuredOutputToolName = toolName
		}

		// Per-response tool-call index state
		streamState := NewAnthropicStreamState()

		for {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			eventType, eventDataBytes, readErr := sseReader.ReadEvent()
			if readErr != nil {
				// Recheck context cancellation
				if ctx.Err() != nil {
					return
				}
				if readErr != io.EOF {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					logger.Warn("Error reading %s stream: %v", providerName, readErr)
					providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, logger, postHookSpanFinalizer)
					return
				}
				break
			}
			eventData := string(eventDataBytes)
			if eventType == "" || eventData == "" {
				continue
			}
			var event AnthropicStreamEvent
			if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
				logger.Warn("Failed to parse message_start event: %v", err)
				continue
			}
			if event.Type == AnthropicStreamEventTypeMessageStart && event.Message != nil && event.Message.ID != "" {
				messageID = event.Message.ID
			}
			// Check for usage in both top-level event.Usage and nested event.Message.Usage
			// message_start events have usage nested in message.usage, while message_delta has it at top level
			var usageToProcess *AnthropicUsage
			if event.Usage != nil {
				usageToProcess = event.Usage
			} else if event.Message != nil && event.Message.Usage != nil {
				usageToProcess = event.Message.Usage
			}
			// Capture the message_start usage snapshot; it backs the
			// impossible-zero guard in applyStreamUsageEvent for non-conformant
			// providers and the delta-absent fallback path.
			if event.Type == AnthropicStreamEventTypeMessageStart && usageToProcess != nil {
				startSnapshot = usageToProcess
			}
			if usageToProcess != nil {
				// Web search request count → billed as search queries (server tool use).
				if usageToProcess.ServerToolUse != nil && usageToProcess.ServerToolUse.WebSearchRequests > 0 {
					if usage.CompletionTokensDetails == nil {
						usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{}
					}
					if n := usageToProcess.ServerToolUse.WebSearchRequests; usage.CompletionTokensDetails.NumSearchQueries == nil || n > *usage.CompletionTokensDetails.NumSearchQueries {
						usage.CompletionTokensDetails.NumSearchQueries = &n
					}
				}
				// Capture served fast mode + inference geography (top-level response fields).
				// Mirror onto the billing usage handle so a mid-stream cancel/timeout can
				// still apply the served-tier multiplier (billed usage is otherwise bare).
				if usageToProcess.Speed != nil {
					servedSpeed = usageToProcess.Speed
					usage.Speed = usageToProcess.Speed
				}
				if usageToProcess.InferenceGeo != nil {
					servedInferenceGeo = usageToProcess.InferenceGeo
					usage.InferenceGeo = usageToProcess.InferenceGeo
				}
				// Fold the event's usage snapshot into the accumulator. On
				// message_delta this overwrites message_start wholesale and
				// reconstructs PromptTokens from the authoritative cache
				// breakdown (delta.input + cache_creation + cache_read); on
				// message_start (or any other event) it max-keeps the
				// preliminary values as the delta-absent fallback.
				if applyStreamUsageEvent(usage, event.Type, usageToProcess, startSnapshot) {
					deltaProcessed = true
				}
			}
			if event.Message != nil {
				modelName = event.Message.Model
			}

			// Extract finish reason from event delta
			if event.Delta != nil && event.Delta.StopReason != nil {
				mappedReason := ConvertAnthropicFinishReasonToBifrost(*event.Delta.StopReason)
				finishReason = &mappedReason

				// Override finish reason for structured output only when the SO tool
				// was consumed into content AND no real tool calls were also emitted.
				// streamState.nextToolCallIndex > 0 means real tool_use blocks were seen.
				if consumedStructuredOutput && streamState.nextToolCallIndex == 0 &&
					*finishReason == string(schemas.BifrostFinishReasonToolCalls) {
					stopReason := string(schemas.BifrostFinishReasonStop)
					finishReason = &stopReason
				}
			}

			// Handle structured output: intercept tool calls for the structured output tool
			// and convert them to content instead of forwarding as tool calls
			if structuredOutputToolName != "" {
				// Check for tool use start event
				if event.Type == AnthropicStreamEventTypeContentBlockStart {
					if event.ContentBlock != nil && event.ContentBlock.Type == AnthropicContentBlockTypeToolUse {
						if event.ContentBlock.Name != nil && *event.ContentBlock.Name == structuredOutputToolName {
							isAccumulatingStructuredOutput = true
							continue
						}
					}
				}

				// Check for tool use delta event
				if event.Type == AnthropicStreamEventTypeContentBlockDelta && isAccumulatingStructuredOutput {
					if event.Delta != nil && event.Delta.Type == AnthropicStreamDeltaTypeInputJSON && event.Delta.PartialJSON != nil {
						// Convert tool use delta to content delta
						content := *event.Delta.PartialJSON
						response := &schemas.BifrostChatResponse{
							ID:     messageID,
							Object: "chat.completion.chunk",
							Choices: []schemas.BifrostResponseChoice{
								{
									Index: 0,
									ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
										Delta: &schemas.ChatStreamResponseChoiceDelta{
											Content: &content,
										},
									},
								},
							},
							ExtraFields: schemas.BifrostResponseExtraFields{
								ChunkIndex: chunkIndex,
								Latency:    time.Since(lastChunkTime).Milliseconds(),
							},
						}
						lastChunkTime = time.Now()
						chunkIndex++

						if sendBackRawResponse {
							response.ExtraFields.RawResponse = eventData
						}

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
						continue
					}
				}

				// Check for content block stop
				if event.Type == AnthropicStreamEventTypeContentBlockStop && isAccumulatingStructuredOutput {
					isAccumulatingStructuredOutput = false
					consumedStructuredOutput = true
					continue
				}
			}

			response, bifrostErr, isLastChunk := event.ToBifrostChatCompletionStream(ctx, structuredOutputToolName, streamState)
			if bifrostErr != nil {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger, postHookSpanFinalizer)
				break
			}
			if response != nil {
				response.ExtraFields = schemas.BifrostResponseExtraFields{
					ChunkIndex: chunkIndex,
					Latency:    time.Since(lastChunkTime).Milliseconds(),
				}
				if postResponseConverter != nil {
					response = postResponseConverter(response)
					if response == nil {
						logger.Warn("postResponseConverter returned nil; skipping chunk")
						continue
					}
				}
				response.ID = messageID
				lastChunkTime = time.Now()
				chunkIndex++

				if sendBackRawResponse {
					response.ExtraFields.RawResponse = eventData
				}

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
			}
			if isLastChunk {
				break
			}
		}
		normalizeUsage()
		response := providerUtils.CreateBifrostChatCompletionChunkResponse(messageID, usage, finishReason, chunkIndex, modelName, 0)
		if postResponseConverter != nil {
			response = postResponseConverter(response)
			if response == nil {
				logger.Warn("postResponseConverter returned nil; skipping chunk")
				// Setting error on the context to signal to the defer that we need to close the stream
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				return
			}
		}
		// Forward served fast mode + data residency so the final chunk bills correctly.
		if servedSpeed != nil {
			response.Speed = servedSpeed
		}
		if servedInferenceGeo != nil {
			response.InferenceGeo = servedInferenceGeo
		}
		// Set raw request if enabled
		if sendBackRawRequest {
			providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
		}
		response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
	}()

	return responseChan, nil
}

// Responses performs a chat completion request to Anthropic's API.
// It formats the request, sends it to Anthropic, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *AnthropicProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}
	return HandleAnthropicResponsesRequest(
		ctx,
		provider.client,
		provider.buildRequestURL(ctx, "/v1/messages", schemas.ResponsesRequest),
		request,
		AnthropicRequestBuildConfig{
			Provider:                  schemas.Anthropic,
			IsStreaming:               false,
			BetaHeaderOverrides:       provider.networkConfig.BetaHeaderOverrides,
			ShouldSendBackRawRequest:  provider.sendBackRawRequest,
			ShouldSendBackRawResponse: provider.sendBackRawResponse,
		},
		provider.anthropicRequestHeaders(ctx, key),
		provider.networkConfig.ExtraHeaders,
		nil,
		provider.logger,
	)
}

// HandleAnthropicResponsesRequest is the Responses-API analogue of
// HandleAnthropicChatCompletionRequest: it builds the Anthropic Messages request body from
// config, performs a non-streaming request, and parses the native Anthropic response into a
// BifrostResponsesResponse. Shared by the Anthropic, Azure, Vertex, and Bedrock providers.
func HandleAnthropicResponsesRequest(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	request *schemas.BifrostResponsesRequest,
	config AnthropicRequestBuildConfig,
	headers map[string]string,
	extraHeaders map[string]string,
	signer providerUtils.BodySigner,
	logger schemas.Logger,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	jsonBody, bifrostErr := BuildAnthropicResponsesRequestBody(ctx, request, config)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	responseBody, latency, providerResponseHeaders, bifrostErr := completeRequest(ctx, client, url, jsonBody, headers, extraHeaders, config.BetaHeaderOverrides, config.Provider, schemas.ResponsesRequest, signer, logger)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, config.ShouldSendBackRawRequest, config.ShouldSendBackRawResponse, latency)
	}

	// Large response mode: return lightweight response with usage from the prefetch preview.
	if isLargeResp, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); isLargeResp {
		preview, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadResponsePreview).(string)
		return &schemas.BifrostResponsesResponse{
			ID:        schemas.Ptr("resp_" + providerUtils.GetRandomString(50)),
			Object:    "response",
			CreatedAt: int(time.Now().Unix()),
			Model:     request.Model,
			Usage:     extractAnthropicResponsesUsageFromPrefetch([]byte(preview)),
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerResponseHeaders,
			},
		}, nil
	}

	// Create response object from pool
	response := AcquireAnthropicMessageResponse()
	defer ReleaseAnthropicMessageResponse(response)

	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, response, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, config.ShouldSendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, config.ShouldSendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, config.ShouldSendBackRawRequest, config.ShouldSendBackRawResponse, latency)
	}

	// Create final response
	bifrostResponse := response.ToBifrostResponsesResponse(ctx)

	// Set ExtraFields
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, config.ShouldSendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}
	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, config.ShouldSendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResponse, nil
}

// ResponsesStream performs a streaming responses request to the Anthropic API.
func (provider *AnthropicProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	jsonBody, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
		Provider:                  schemas.Anthropic,
		IsStreaming:               true,
		ShouldSendBackRawRequest:  provider.sendBackRawRequest,
		ShouldSendBackRawResponse: provider.sendBackRawResponse,
	})
	if err != nil {
		return nil, err
	}

	// Prepare Anthropic headers
	headers := map[string]string{
		"Content-Type":      "application/json",
		"anthropic-version": provider.apiVersion,
		"Accept":            "text/event-stream",
		"Cache-Control":     "no-cache",
	}

	if key.Value.GetValue() != "" && !IsClaudeCodeMaxMode(ctx) {
		headers["x-api-key"] = key.Value.GetValue()
	}

	return HandleAnthropicResponsesStream(
		ctx,
		provider.streamingClient,
		provider.buildRequestURL(ctx, "/v1/messages", schemas.ResponsesStreamRequest),
		jsonBody,
		headers,
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		provider.networkConfig.BetaHeaderOverrides,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// HandleAnthropicResponsesStream handles streaming for Anthropic-compatible APIs.
// This shared function reduces code duplication between providers that use the same SSE event format.
func HandleAnthropicResponsesStream(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	jsonBody []byte,
	headers map[string]string,
	extraHeaders map[string]string,
	streamIdleTimeoutInSeconds int,
	betaHeaderOverrides map[string]bool,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
	providerName schemas.ModelProvider,
	postHookRunner schemas.PostHookRunner,
	postResponseConverter func(*schemas.BifrostResponsesStreamResponse) *schemas.BifrostResponsesStreamResponse,
	signer providerUtils.BodySigner,
	logger schemas.Logger,
	postHookSpanFinalizer func(context.Context),
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, streamIdleTimeoutInSeconds)
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodPost)
	req.SetRequestURI(url)
	req.Header.SetContentType("application/json")

	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, []string{AnthropicBetaHeader})

	if betaHeaders := FilterBetaHeadersForProvider(MergeBetaHeaders(ctx, extraHeaders), providerName, betaHeaderOverrides); len(betaHeaders) > 0 {
		req.Header.Set(AnthropicBetaHeader, strings.Join(betaHeaders, ","))
	} else {
		req.Header.Del(AnthropicBetaHeader)
	}

	// Set auth/static headers, applied after extra headers so they always win
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Set body
	usedLargePayloadBody := setAnthropicRequestBody(ctx, req, jsonBody)

	// Sign the exact body bytes set on the request, when a signer is supplied (e.g. AWS SigV4
	// for Bedrock Mantle). Done after the body is set so the signature covers what is actually sent.
	if signer != nil {
		sigHeaders, bErr := signer(jsonBody)
		if bErr != nil {
			defer providerUtils.ReleaseStreamingResponse(ctx, resp)
			return nil, bErr
		}
		for k, v := range sigHeaders {
			req.Header.Set(k, v)
		}
	}

	// Close the connection after this streaming response instead of returning it to
	// the keep-alive pool. fasthttp can otherwise reuse a streaming connection whose
	// reader is still in a torn state (body not fully drained, or the idle-timeout/
	// consumer goroutine still attached), corrupting the next request's RoundTrip and
	// panicking with "slice bounds out of range" in mustPeekBuffered. Mirrors the
	// mitigation already used on Gemini/Azure/OpenAI streaming and anthropic.go:2716.
	req.Header.Set("Connection", "close")

	// Use a fresh streaming client per request so fasthttp's internal readerPool
	// cannot be poisoned across concurrent Anthropic streams by a non-idempotent
	// streaming-body close path. The cloned client preserves dialer/TLS/proxy
	// config but starts with empty reader/writer pools.
	requestClient := providerUtils.BuildStreamingClient(client)

	// Use streaming-aware client when large payload optimization is active — ensures
	// MaxResponseBodySize > 0 so ErrBodyTooLarge triggers StreamBody for Content-Length responses.
	activeClient := providerUtils.PrepareResponseStreaming(ctx, requestClient, resp)

	startTime := time.Now()
	// Make the request
	err := activeClient.Do(req, resp)
	latency := time.Since(startTime)
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	if err != nil {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
		}
		// Request failed before the first response byte (server closed an idle/pooled connection,
		// broken pipe, connection refused, DNS failure, etc.). Surface as a retriable upstream
		// connection error (502) so executeRequestWithRetries honors max_retries, matching the
		// non-streaming path - see https://github.com/maximhq/bifrost/issues/4496.
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostUpstreamConnectionError(schemas.ErrProviderDoRequest, err), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	// Store provider response headers in context before status check so error responses also forward them
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, providerUtils.EnrichError(ctx, parseAnthropicError(resp), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	// Large payload streaming passthrough — pipe raw upstream SSE to client
	if providerUtils.SetupStreamingPassthrough(ctx, resp) {
		responseChan := make(chan *schemas.BifrostStreamChunk)
		providerUtils.CloseStream(ctx, responseChan)
		return responseChan, nil
	}

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, logger, postHookSpanFinalizer, jsonBody)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, logger, postHookSpanFinalizer, jsonBody)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(ctx, resp)
		// If body stream is nil, return an error
		if resp.BodyStream() == nil {
			bifrostErr := providerUtils.NewBifrostOperationError(
				"Provider returned an empty response",
				fmt.Errorf("provider returned an empty response"),
			)
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse), responseChan, logger, postHookSpanFinalizer)
			return
		}

		// Decompress gzip-encoded streams on-the-fly. Returns a reader that is either
		// the gzip reader (if gzip-encoded) or the original body stream. Does NOT modify
		// resp, so ReleaseStreamingResponse can properly drain the underlying connection.
		reader, releaseGzip := providerUtils.DecompressStreamBody(resp)
		defer releaseGzip()

		// Wrap reader with idle timeout to detect stalled streams.
		reader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(reader, resp.BodyStream(), providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		// Setup cancellation handler to close the raw network stream on ctx cancellation,
		// which immediately unblocks any in-progress read (including reads blocked inside a gzip decompression layer).
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), logger)
		defer stopCancellation()

		sseReader := providerUtils.GetSSEEventReader(ctx, reader)
		chunkIndex := 0

		lastChunkTime := startTime

		// Track minimal state needed for response format
		usage := &schemas.ResponsesResponseUsage{}
		billedUsage := &schemas.BifrostLLMUsage{}
		// Register the accumulating usage handle so a mid-stream cancel/timeout
		// can bill for Anthropic Responses usage already reported by message_start
		// or message_delta events before the stream was interrupted.
		ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, billedUsage)

		usageNormalized := false
		normalizeBilledUsage := func() {
			if usageNormalized {
				return
			}
			usageNormalized = true
			normalizeCachedUsage(billedUsage)
		}
		defer func() {
			if ctx.Err() != nil {
				normalizeBilledUsage()
			}
		}()

		// Create stream state for stateful conversions
		streamState := AcquireAnthropicResponsesStreamState()
		defer ReleaseAnthropicResponsesStreamState(streamState)

		// Set structured output tool name if present
		if toolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
			streamState.StructuredOutputToolName = toolName
		}

		var modelName string
		var servedSpeed *string
		var servedInferenceGeo *string

		for {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			eventType, eventDataBytes, readErr := sseReader.ReadEvent()
			if readErr != nil {
				// Recheck context cancellation
				if ctx.Err() != nil {
					return
				}
				if readErr != io.EOF {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					logger.Warn("Error reading %s stream: %v", providerName, readErr)
					providerUtils.ProcessAndSendError(ctx, postHookRunner, readErr, responseChan, logger, postHookSpanFinalizer)
				}
				break
			}
			eventData := string(eventDataBytes)
			if eventType == "" || eventData == "" {
				continue
			}
			var event AnthropicStreamEvent
			if err := sonic.Unmarshal([]byte(eventData), &event); err != nil {
				logger.Warn("Failed to parse message_start event: %v", err)
				continue
			}
			if event.Message != nil && modelName == "" {
				modelName = event.Message.Model
			}
			// Note: response.created and response.in_progress are now emitted by ToBifrostResponsesStream
			// from the message_start event, so we don't need to call them manually here

			// Check for usage in both top-level event.Usage and nested event.Message.Usage
			// message_start events have usage nested in message.usage, while message_delta has it at top level
			var usageToProcess *AnthropicUsage
			if event.Usage != nil {
				usageToProcess = event.Usage
			} else if event.Message != nil && event.Message.Usage != nil {
				usageToProcess = event.Message.Usage
			}

			if usageToProcess != nil {
				// Collect usage information and send at the end of the stream.
				// Also mirror it into billedUsage so cancellation/timeout paths can
				// charge for provider-reported usage before the final chunk arrives.
				accumulateAnthropicResponsesUsage(usage, billedUsage, usageToProcess)
				// Mirror served tier onto billedUsage so a mid-stream cancel/timeout can
				// still apply the served-tier multiplier (billed usage is otherwise bare).
				if usageToProcess.Speed != nil {
					servedSpeed = usageToProcess.Speed
					if billedUsage != nil {
						billedUsage.Speed = usageToProcess.Speed
					}
				}
				if usageToProcess.InferenceGeo != nil {
					servedInferenceGeo = usageToProcess.InferenceGeo
					if billedUsage != nil {
						billedUsage.InferenceGeo = usageToProcess.InferenceGeo
					}
				}
			}

			responses, bifrostErr, isLastChunk := event.ToBifrostResponsesStream(ctx, chunkIndex, streamState)
			// Propagate message_delta emission flag to context so the output converter
			// (ToAnthropicResponsesStreamResponse) can skip synthesizing a duplicate.
			if streamState.HasEmittedMessageDelta {
				ctx.SetValue(schemas.BifrostContextKeyHasEmittedMessageDelta, true)
			}
			if bifrostErr != nil {
				// If context was cancelled/timed out, let defer handle it
				if ctx.Err() != nil {
					return
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, logger, postHookSpanFinalizer)
				break
			}

			// Attach the upstream raw to exactly one bifrost response. Default to the last,
			// but for the message_start expansion ([created, in_progress]) attach it to
			// response.created
			rawIdx := len(responses) - 1
			for j, r := range responses {
				if r != nil && r.Type == schemas.ResponsesStreamResponseTypeCreated {
					rawIdx = j
					break
				}
			}
			// Handle each response in the slice
			for i, response := range responses {
				if response != nil {
					response.ExtraFields = schemas.BifrostResponseExtraFields{
						ChunkIndex: chunkIndex,
						Latency:    time.Since(lastChunkTime).Milliseconds(),
					}
					if postResponseConverter != nil {
						response = postResponseConverter(response)
						if response == nil {
							logger.Warn("postResponseConverter returned nil; skipping chunk")
							continue
						}
					}
					lastChunkTime = time.Now()
					chunkIndex++

					if providerUtils.ShouldSendBackRawResponse(ctx, sendBackRawResponse) && i == rawIdx {
						response.ExtraFields.RawResponse = eventData
					}

					if isLastChunk && i == len(responses)-1 {
						if response.Response == nil {
							response.Response = &schemas.BifrostResponsesResponse{}
						}
						if usage.InputTokensDetails != nil {
							usage.InputTokens = usage.InputTokens + usage.InputTokensDetails.CachedReadTokens + usage.InputTokensDetails.CachedWriteTokens
							usage.TotalTokens = usage.TotalTokens + usage.InputTokensDetails.CachedReadTokens + usage.InputTokensDetails.CachedWriteTokens
						}
						response.Response.Usage = usage
						if servedSpeed != nil {
							response.Response.Speed = servedSpeed
						}
						if servedInferenceGeo != nil {
							response.Response.InferenceGeo = servedInferenceGeo
						}
						// Set raw request if enabled
						if sendBackRawRequest {
							providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonBody)
						}
						response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan, postHookSpanFinalizer)
						return
					}
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan, postHookSpanFinalizer)
				}
			}

		}
	}()

	return responseChan, nil
}

// BatchCreate creates a new batch job.
func (provider *AnthropicProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchCreateRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(request.Requests) == 0 {
		return nil, providerUtils.NewBifrostOperationError("requests array is required for Anthropic batch API", nil)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/messages/batches", schemas.BatchCreateRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("x-api-key", key.Value.GetValue())
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Build request body
	anthropicReq := &AnthropicBatchCreateRequest{
		Requests: make([]AnthropicBatchRequestItem, len(request.Requests)),
	}

	for i, r := range request.Requests {
		anthropicReq.Requests[i] = AnthropicBatchRequestItem{
			CustomID: r.CustomID,
			Params:   r.Params,
		}
		// Use Body if Params is empty
		if anthropicReq.Requests[i].Params == nil && r.Body != nil {
			anthropicReq.Requests[i].Params = r.Body
		}
	}

	jsonData, err := providerUtils.MarshalSorted(anthropicReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	usedLargePayloadBody := setAnthropicRequestBody(ctx, req, jsonData)

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// Make request
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, providerUtils.SetErrorLatency(parseAnthropicError(resp), latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	var anthropicResp AnthropicBatchResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	return anthropicResp.ToBifrostBatchCreateResponse(latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// BatchList lists batch jobs using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *AnthropicProvider) BatchList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// Initialize serial pagination helper (Anthropic uses AfterID for pagination)
	helper, err := providerUtils.NewSerialListHelper(keys, request.AfterID, provider.logger, true)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostBatchListResponse{
			Object:  "list",
			Data:    []schemas.BifrostBatchRetrieveResponse{},
			HasMore: false,
		}, nil
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	baseURL := provider.buildRequestURL(ctx, "/v1/messages/batches", schemas.BatchListRequest)
	values := url.Values{}
	if request.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	if request.BeforeID != nil && *request.BeforeID != "" {
		values.Set("before_id", *request.BeforeID)
	}
	// Use native cursor from serial helper instead of request.AfterID
	if nativeCursor != "" {
		values.Set("after_id", nativeCursor)
	}
	requestURL := baseURL
	if encodedValues := values.Encode(); encodedValues != "" {
		requestURL += "?" + encodedValues
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("x-api-key", key.Value.GetValue())
	}
	req.Header.Set("anthropic-version", provider.apiVersion)

	// Make request
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, providerUtils.SetErrorLatency(parseAnthropicError(resp), latency)
	}

	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr)
	}

	var anthropicResp AnthropicBatchListResponse
	_, _, bifrostErr = providerUtils.HandleProviderResponse(body, &anthropicResp, nil, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert batches to Bifrost format
	batches := make([]schemas.BifrostBatchRetrieveResponse, 0, len(anthropicResp.Data))
	var lastBatchID string
	for _, batch := range anthropicResp.Data {
		batches = append(batches, *batch.ToBifrostBatchRetrieveResponse(latency, false, false, nil, nil))
		lastBatchID = batch.ID
	}

	// Build cursor for next request
	// Anthropic uses LastID as the cursor for pagination
	nextCursor, hasMore := helper.BuildNextCursor(anthropicResp.HasMore, lastBatchID)

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostBatchListResponse{
		Object:  "list",
		Data:    batches,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
	}
	if nextCursor != "" {
		bifrostResp.NextCursor = &nextCursor
	}

	return bifrostResp, nil
}

// BatchRetrieve retrieves a specific batch job by trying each key until found.
func (provider *AnthropicProvider) BatchRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchRetrieveRequest); err != nil {
		return nil, err
	}

	// batch id is required
	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil)
	}

	providerName := provider.GetProviderKey()
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.buildRequestURL(
			ctx,
			"/v1/messages/batches/"+url.PathEscape(request.BatchID),
			schemas.BatchRetrieveRequest,
		))
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)

		// Make request
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = parseAnthropicError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		var anthropicResp AnthropicBatchResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := anthropicResp.ToBifrostBatchRetrieveResponse(latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse)
		return result, nil
	}

	return nil, lastErr
}

// BatchCancel cancels a batch job by trying each key until successful.
func (provider *AnthropicProvider) BatchCancel(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchCancelRequest); err != nil {
		return nil, err
	}

	// batch id is required
	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil)
	}

	providerName := provider.GetProviderKey()
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/messages/batches/" + request.BatchID + "/cancel")
		req.Header.SetMethod(http.MethodPost)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)

		// Make request
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = parseAnthropicError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		var anthropicResp AnthropicBatchResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := &schemas.BifrostBatchCancelResponse{
			ID:     anthropicResp.ID,
			Object: anthropicResp.Type,
			Status: ToBifrostBatchStatus(anthropicResp.ProcessingStatus),
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
			},
		}

		if sendBackRawRequest {
			result.ExtraFields.RawRequest = rawRequest
		}

		if anthropicResp.CancelInitiatedAt != nil {
			cancellingAt := parseAnthropicTimestamp(*anthropicResp.CancelInitiatedAt)
			result.CancellingAt = &cancellingAt
		}

		if anthropicResp.RequestCounts != nil {
			result.RequestCounts = schemas.BatchRequestCounts{
				Total:     anthropicResp.RequestCounts.Processing + anthropicResp.RequestCounts.Succeeded + anthropicResp.RequestCounts.Errored + anthropicResp.RequestCounts.Canceled + anthropicResp.RequestCounts.Expired,
				Completed: anthropicResp.RequestCounts.Succeeded,
				Failed:    anthropicResp.RequestCounts.Errored,
			}
		}

		if sendBackRawResponse {
			result.ExtraFields.RawResponse = rawResponse
		}

		return result, nil
	}

	return nil, lastErr
}

// BatchDelete is not supported by the Anthropic provider.
func (provider *AnthropicProvider) BatchDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults retrieves batch results by trying each key until found.
func (provider *AnthropicProvider) BatchResults(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.BatchResultsRequest); err != nil {
		return nil, err
	}

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id is required", nil)
	}

	providerName := provider.GetProviderKey()

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/messages/batches/" + request.BatchID + "/results")
		req.Header.SetMethod(http.MethodGet)

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)

		// Make request
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = parseAnthropicError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		// Parse JSONL content - each line is a separate result
		var results []schemas.BatchResultItem

		parseResult := providerUtils.ParseJSONL(body, func(line []byte) error {
			var anthropicResult AnthropicBatchResultItem
			if err := sonic.Unmarshal(line, &anthropicResult); err != nil {
				provider.logger.Warn("failed to parse batch result line: %v", err)
				return err
			}

			// Convert to Bifrost format
			resultItem := schemas.BatchResultItem{
				CustomID: anthropicResult.CustomID,
				Result: &schemas.BatchResultData{
					Type:    anthropicResult.Result.Type,
					Message: anthropicResult.Result.Message,
				},
			}

			if anthropicResult.Result.Error != nil {
				resultItem.Error = &schemas.BatchResultError{
					Code:    anthropicResult.Result.Error.Type,
					Message: anthropicResult.Result.Error.Message,
				}
			}

			results = append(results, resultItem)
			return nil
		})

		batchResultsResp := &schemas.BifrostBatchResultsResponse{
			BatchID: request.BatchID,
			Results: results,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
			},
		}

		if len(parseResult.Errors) > 0 {
			batchResultsResp.ExtraFields.ParseErrors = parseResult.Errors
		}

		return batchResultsResp, nil
	}

	return nil, lastErr
}

// Embedding is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, input *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Speech is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// Rerank is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// OCR is not supported by the Anthropic provider.
func (provider *AnthropicProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// FileUpload uploads a file to Anthropic's Files API.
func (provider *AnthropicProvider) FileUpload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileUploadRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("file content is required", nil)
	}

	// Create multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file field
	filename := request.Filename
	if filename == "" {
		filename = "file"
	}
	contentType := ""
	if request.ContentType != nil {
		contentType = strings.TrimSpace(*request.ContentType)
	}
	if request.ContentType != nil {
		ct := strings.TrimSpace(*request.ContentType)
		if strings.ContainsAny(ct, "\r\n") {
			return nil, providerUtils.NewBifrostOperationError("invalid content type: %s", fmt.Errorf("contains CR or LF characters"))
		}
		contentType = ct
	}
	var part io.Writer
	var err error
	if contentType != "" {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", multipart.FileContentDisposition("file", filename))
		header.Set("Content-Type", contentType)
		part, err = writer.CreatePart(header)
	} else {
		part, err = writer.CreateFormFile("file", filename)
	}
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create form file", err)
	}
	if _, err := part.Write(request.File); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write file content", err)
	}

	if err := writer.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to close multipart writer", err)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/files", schemas.FileUploadRequest))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())

	if key.Value.GetValue() != "" {
		req.Header.Set("x-api-key", key.Value.GetValue())
	}
	req.Header.Set("anthropic-version", provider.apiVersion)
	appendBetaHeader(req, AnthropicFilesAPIBetaHeader)
	req.SetBody(buf.Bytes())

	// Make request
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, providerUtils.SetErrorLatency(parseAnthropicError(resp), latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}

	var anthropicResp AnthropicFileResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return anthropicResp.ToBifrostFileUploadResponse(latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
}

// FileList lists files from all provided keys and aggregates results.
// FileList lists files using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *AnthropicProvider) FileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	// Initialize serial pagination helper
	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger, true)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostFileListResponse{
			Object:  "list",
			Data:    []schemas.FileObject{},
			HasMore: false,
		}, nil
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	requestURL := provider.buildRequestURL(ctx, "/v1/files", schemas.FileListRequest)
	values := url.Values{}
	if request.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from serial helper instead of request.After
	if nativeCursor != "" {
		values.Set("after_id", nativeCursor)
	}
	if encodedValues := values.Encode(); encodedValues != "" {
		requestURL += "?" + encodedValues
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("x-api-key", key.Value.GetValue())
	}
	req.Header.Set("anthropic-version", provider.apiVersion)
	appendBetaHeader(req, AnthropicFilesAPIBetaHeader)

	// Make request
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, providerUtils.SetErrorLatency(parseAnthropicError(resp), latency)
	}

	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr)
	}

	var anthropicResp AnthropicFileListResponse
	_, _, bifrostErr = providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert files to Bifrost format
	files := make([]schemas.FileObject, 0, len(anthropicResp.Data))
	var lastFileID string
	for _, file := range anthropicResp.Data {
		files = append(files, schemas.FileObject{
			ID:        file.ID,
			Object:    file.Type,
			Bytes:     file.SizeBytes,
			CreatedAt: parseAnthropicFileTimestamp(file.CreatedAt),
			Filename:  file.Filename,
			Purpose:   schemas.FilePurposeBatch,
			Status:    schemas.FileStatusProcessed,
		})
		lastFileID = file.ID
	}

	// Build cursor for next request
	// Anthropic uses LastID as the cursor for pagination
	nextCursor, hasMore := helper.BuildNextCursor(anthropicResp.HasMore, lastFileID)

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  "list",
		Data:    files,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
	}
	if nextCursor != "" {
		bifrostResp.After = &nextCursor
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves file metadata from Anthropic's Files API by trying each key until found.
func (provider *AnthropicProvider) FileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.buildRequestURL(
			ctx,
			"/v1/files/"+url.PathEscape(request.FileID),
			schemas.FileRetrieveRequest,
		))
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)
		appendBetaHeader(req, AnthropicFilesAPIBetaHeader)

		// Make request
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = parseAnthropicError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		var anthropicResp AnthropicFileResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		return anthropicResp.ToBifrostFileRetrieveResponse(latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse), nil
	}

	return nil, lastErr
}

// FileDelete deletes a file from Anthropic's Files API by trying each key until successful.
func (provider *AnthropicProvider) FileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileDeleteRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files/" + request.FileID)
		req.Header.SetMethod(http.MethodDelete)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)
		appendBetaHeader(req, AnthropicFilesAPIBetaHeader)

		// Make request
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusNoContent {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = parseAnthropicError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		// For 204 No Content, return success without parsing body
		if resp.StatusCode() == fasthttp.StatusNoContent {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return &schemas.BifrostFileDeleteResponse{
				ID:      request.FileID,
				Object:  "file",
				Deleted: true,
				ExtraFields: schemas.BifrostResponseExtraFields{
					Latency: latency.Milliseconds(),
				},
			}, nil
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		var anthropicResp AnthropicFileDeleteResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &anthropicResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := &schemas.BifrostFileDeleteResponse{
			ID:      anthropicResp.ID,
			Object:  "file",
			Deleted: anthropicResp.Type == "file_deleted",
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
			},
		}

		if sendBackRawRequest {
			result.ExtraFields.RawRequest = rawRequest
		}

		if sendBackRawResponse {
			result.ExtraFields.RawResponse = rawResponse
		}

		return result, nil
	}

	return nil, lastErr
}

// FileContent downloads file content from Anthropic's Files API by trying each key until found.
// Note: Only files created by skills or the code execution tool can be downloaded.
func (provider *AnthropicProvider) FileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.FileContentRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files/" + request.FileID + "/content")
		req.Header.SetMethod(http.MethodGet)

		if key.Value.GetValue() != "" {
			req.Header.Set("x-api-key", key.Value.GetValue())
		}
		req.Header.Set("anthropic-version", provider.apiVersion)
		appendBetaHeader(req, AnthropicFilesAPIBetaHeader)
		// Make request
		latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = parseAnthropicError(resp)
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			wait()
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
			continue
		}

		// Get content type from response
		contentType := string(resp.Header.ContentType())
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		content := append([]byte(nil), body...)
		wait()
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		return &schemas.BifrostFileContentResponse{
			FileID:      request.FileID,
			Content:     content,
			ContentType: contentType,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
			},
		}, nil
	}

	return nil, lastErr
}

// CountTokens counts tokens for a given request using Anthropic's API.
func (provider *AnthropicProvider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.CountTokensRequest); err != nil {
		return nil, err
	}
	jsonBody, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
		Provider:                  schemas.Anthropic,
		IsStreaming:               false,
		ExcludeFields:             []string{"max_tokens", "temperature"},
		ShouldSendBackRawRequest:  provider.sendBackRawRequest,
		ShouldSendBackRawResponse: provider.sendBackRawResponse,
	})
	if err != nil {
		return nil, err
	}

	responseBody, latency, providerResponseHeaders, bifrostErr := completeRequest(ctx, provider.client, provider.buildRequestURL(ctx, "/v1/messages/count_tokens", schemas.CountTokensRequest), jsonBody, provider.anthropicRequestHeaders(ctx, key), provider.networkConfig.ExtraHeaders, provider.networkConfig.BetaHeaderOverrides, provider.GetProviderKey(), schemas.CountTokensRequest, nil, provider.logger)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	anthropicResponse := &AnthropicCountTokensResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(
		responseBody,
		anthropicResponse,
		jsonBody,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
	)

	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	response := anthropicResponse.ToBifrostCountTokensResponse(request.Model)
	response.Model = request.Model

	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// Compaction is not supported by the Anthropic provider.
func (provider *AnthropicProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// VideoGeneration is not supported by the Anthropic provider.
func (provider *AnthropicProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, provider.GetProviderKey())
}

// VideoRetrieve is not supported by the Anthropic provider.
func (provider *AnthropicProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, provider.GetProviderKey())
}

// VideoDownload is not supported by the Anthropic provider.
func (provider *AnthropicProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}

// VideoDelete is not supported by the Anthropic provider.
func (provider *AnthropicProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by the Anthropic provider.
func (provider *AnthropicProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by the Anthropic provider.
func (provider *AnthropicProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Anthropic provider.
func (provider *AnthropicProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

func (provider *AnthropicProvider) Passthrough(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	req *schemas.BifrostPassthroughRequest,
) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.PassthroughRequest); err != nil {
		return nil, err
	}

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

	for k, v := range req.SafeHeaders {
		fasthttpReq.Header.Set(k, v)
	}

	if key.Value.GetValue() != "" {
		fasthttpReq.Header.Set("x-api-key", key.Value.GetValue())
	}
	fasthttpReq.Header.Set("anthropic-version", provider.apiVersion)

	fasthttpReq.SetBody(req.Body)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, fasthttpReq, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers := providerUtils.ExtractPassthroughProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to decode response body", err)
	}

	var passthroughUsage *schemas.BifrostPassthroughUsage
	if resp.StatusCode() >= 200 && resp.StatusCode() < 300 {
		passthroughUsage = ExtractAnthropicPassthroughUsage(req.Path, req.Body, body)
	}

	bifrostResponse := &schemas.BifrostPassthroughResponse{
		StatusCode: resp.StatusCode(),
		Headers:    headers,
		Body:       body,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: headers,
			PassthroughPath:         req.Path,
		},
		PassthroughUsage: passthroughUsage,
	}

	return bifrostResponse, nil
}

func (provider *AnthropicProvider) PassthroughStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	req *schemas.BifrostPassthroughRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Anthropic, provider.customProviderConfig, schemas.PassthroughStreamRequest); err != nil {
		return nil, err
	}

	url := provider.networkConfig.BaseURL + req.Path
	if req.RawQuery != "" {
		url += "?" + req.RawQuery
	}

	startTime := time.Now()

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

	fasthttpReq.Header.Set("Connection", "close")

	if key.Value.GetValue() != "" {
		fasthttpReq.Header.Set("x-api-key", key.Value.GetValue())
	}
	fasthttpReq.Header.Set("anthropic-version", provider.apiVersion)

	fasthttpReq.SetBody(req.Body)

	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.streamingClient, resp)
	err := activeClient.Do(fasthttpReq, resp)
	latency := time.Since(startTime)
	if err != nil {
		providerUtils.ReleaseStreamingResponse(ctx, resp)
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

	headers := providerUtils.ExtractPassthroughProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)

	bodyStream := resp.BodyStream()
	if bodyStream == nil {
		providerUtils.ReleaseStreamingResponse(ctx, resp)
		return nil, providerUtils.NewBifrostOperationError(
			"provider returned an empty stream body",
			fmt.Errorf("provider returned an empty stream body"),
		)
	}

	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)
	strippedPath := req.Path
	if idx := strings.IndexByte(strippedPath, '?'); idx >= 0 {
		strippedPath = strippedPath[:idx]
	}
	var messagesUsage *AnthropicPassthroughStreamUsage
	if strings.HasSuffix(strippedPath, "/messages") {
		messagesUsage = &AnthropicPassthroughStreamUsage{}
	}
	return providerUtils.StreamPassthrough(
		ctx, postHookRunner, postHookSpanFinalizer, resp, bodyStream,
		providerUtils.PassthroughStreamParams{
			StatusCode:       resp.StatusCode(),
			Headers:          headers,
			Path:             req.Path,
			RawRequest:       req.Body,
			CancellationBody: req.Body,
			StartTime:        startTime,
			Logger:           provider.logger,
			HasUsage:         HasAnthropicPassthroughUsage,
			Observe: func(event []byte) *schemas.BifrostPassthroughUsage {
				if messagesUsage != nil {
					return messagesUsage.ObserveEvent(event)
				}
				return ExtractAnthropicPassthroughUsage(req.Path, req.Body, event)
			},
		},
	), nil
}
