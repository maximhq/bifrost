package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	openai "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// mantleAnthropicVersion is the Anthropic API version sent as an HTTP header on the
// Bedrock Mantle native-Anthropic endpoint (unlike bedrock-runtime, which carries the
// version as an "anthropic_version" body field).
const mantleAnthropicVersion = "2023-06-01"

// isMantleModel reports whether a model is servable by the Bedrock Mantle endpoint.
// OpenAI-family (gpt-*) and Gemma 4 models use the OpenAI-compatible paths; Anthropic-family
// (Claude) models use the native Anthropic Messages path. The per-family split is handled
// inside each mantle* dispatcher. Gemma 3 is intentionally excluded: it only supports Chat
// (not Responses) on mantle, and the non-mantle path serves both APIs via Converse, so
// forcing it to mantle would break Responses.
//
// This reports mantle *capability* only. The actual routing decision for Claude is made by
// shouldRouteToMantle, which gates Claude behind the per-key/per-alias toggle so the default
// stays on the Converse API.
func isMantleModel(ctx *schemas.BifrostContext, model string) bool {
	return schemas.IsOpenAIModelFamily(ctx, model) || schemas.IsAnthropicModelFamily(ctx, model) || strings.Contains(model, "gemma-4")
}

// shouldRouteToMantle decides whether a request should be dispatched to the Bedrock Mantle
// endpoint. OpenAI-family and Gemma 4 models always use Mantle (their only endpoint). Claude
// (Anthropic-family) models use Mantle's native-Anthropic Messages path only when the
// per-key/per-alias UseAnthropicMessagesAPI toggle is enabled; otherwise they stay on the
// default Bedrock Converse API. Defaults to Converse so existing Claude-on-Bedrock integrations
// keep their guardrails, IAM scope, and request shape unless explicitly opted in.
//
// The decision is made on the canonical (alias-resolved) model — matching the canonical id that
// mantleOpenAIURL already uses for path gating — so the "gemma-4" substring gate in isMantleModel
// matches the underlying model id rather than a routing-alias literal. (The family checks resolve
// via the alias config regardless, but the substring gate needs the canonical id.) The wire
// request still carries the original request.Model.
func (provider *BedrockProvider) shouldRouteToMantle(ctx *schemas.BifrostContext, key schemas.Key, model string) bool {
	capModel := schemas.ResolveCanonicalModel(ctx, model)
	if schemas.IsAnthropicModelFamily(ctx, capModel) {
		return resolveBedrockUseClaudeMessagesAPI(ctx, key)
	}
	return isMantleModel(ctx, capModel)
}

// mantleOpenAIURL builds the Bedrock Mantle OpenAI-compatible endpoint URL for the given
// region, model, and API path (e.g. "chat/completions", "responses"). The native-Anthropic
// path is built separately by mantleAnthropicURL. Pass the canonical (capability-resolved)
// model for correct path gating; the request body still carries the wire request.Model.
// Frontier families (closed gpt-5.x, Gemma 4) live under the "openai/v1" base path; gpt-oss
// uses the bare "v1" path.
func mantleOpenAIURL(region, model, path string) string {
	base := "v1"
	if strings.Contains(model, "gpt-5") || strings.Contains(model, "gemma-4") {
		base = "openai/v1"
	}
	return fmt.Sprintf("https://bedrock-mantle.%s.api.aws/%s/%s", region, base, path)
}

// mantleAnthropicURL builds the Bedrock Mantle native-Anthropic Messages endpoint URL.
func mantleAnthropicURL(region string) string {
	return fmt.Sprintf("https://bedrock-mantle.%s.api.aws/anthropic/v1/messages", region)
}

// mantleSigV4Headers computes SigV4 auth headers for a mantle request by signing a dummy
// net/http.Request. jsonData must be the exact bytes that will be sent. accept must match
// the Accept header the actual request will send, since SigV4 signs all request headers.
func (provider *BedrockProvider) mantleSigV4Headers(
	ctx *schemas.BifrostContext,
	jsonData []byte,
	requestURL, accept string,
	key schemas.Key,
	region string,
) (map[string]string, *schemas.BifrostError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create signing request", err)
	}
	req.Header.Set("Accept", accept)
	if bifrostErr := signAWSRequestFromKey(ctx, req, key.BedrockKeyConfig, region, bedrockMantleSigningService); bifrostErr != nil {
		return nil, bifrostErr
	}
	headers := map[string]string{
		"Authorization":        req.Header.Get("Authorization"),
		"X-Amz-Date":           req.Header.Get("X-Amz-Date"),
		"x-amz-content-sha256": req.Header.Get("x-amz-content-sha256"),
		"Accept":               accept,
	}
	if token := req.Header.Get("X-Amz-Security-Token"); token != "" {
		headers["X-Amz-Security-Token"] = token
	}
	return headers, nil
}

// mantleAnthropicHeaders builds the auth and version headers for a native-Anthropic
// mantle request. A Bedrock API key authenticates via the Authorization: Bearer header —
// the same scheme the co-located OpenAI-compatible mantle paths use on the bedrock-mantle
// host, since the credential is an AWS Bedrock API key rather than an Anthropic x-api-key.
// Otherwise the request is SigV4-signed for the bedrock-mantle service. jsonData and accept
// must match the bytes and Accept header actually sent, since SigV4 signs over them.
func (provider *BedrockProvider) mantleAnthropicHeaders(
	ctx *schemas.BifrostContext,
	jsonData []byte,
	requestURL, accept string,
	key schemas.Key,
	region string,
) (map[string]string, *schemas.BifrostError) {
	headers := map[string]string{
		"anthropic-version": mantleAnthropicVersion,
		"Accept":            accept,
	}
	if key.Value.GetValue() != "" {
		maps.Copy(headers, openai.BearerAuthHeader(key))
		return headers, nil
	}
	sigHeaders, bifrostErr := provider.mantleSigV4Headers(ctx, jsonData, requestURL, accept, key, region)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	maps.Copy(headers, sigHeaders)
	return headers, nil
}

// mantleChatCompletions dispatches non-streaming chat completions on the Bedrock Mantle
// endpoint by model family: Anthropic-family (Claude) models use the native Anthropic
// Messages path; all other (OpenAI-family) models use the OpenAI-compatible path.
func (provider *BedrockProvider) mantleChatCompletions(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if schemas.IsAnthropicModelFamily(ctx, request.Model) {
		return provider.mantleChatCompletionsAnthropic(ctx, key, request)
	}
	return provider.mantleChatCompletionsOpenAI(ctx, key, request)
}

// mantleChatCompletionsAnthropic handles non-streaming chat completions for Claude
// models via the Bedrock Mantle native-Anthropic Messages endpoint.
func (provider *BedrockProvider) mantleChatCompletionsAnthropic(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	_, bareModel := parseBedrockRegionAndModel(request.Model)
	url := mantleAnthropicURL(region)

	config := anthropic.AnthropicRequestBuildConfig{
		Provider:                  schemas.Bedrock,
		Model:                     bareModel,
		BetaHeaderOverrides:       provider.networkConfig.BetaHeaderOverrides,
		ShouldSendBackRawRequest:  provider.sendBackRawRequest,
		ShouldSendBackRawResponse: provider.sendBackRawResponse,
	}

	// Pre-build the body so SigV4 can sign it; HandleAnthropicChatCompletionRequest rebuilds the
	// same bytes (deterministic marshaling), so the signature stays valid.
	jsonData, bifrostErr := anthropic.BuildAnthropicChatRequestBody(ctx, request, config)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers, bifrostErr := provider.mantleAnthropicHeaders(ctx, jsonData, url, "application/json", key, region)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return anthropic.HandleAnthropicChatCompletionRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		config,
		headers,
		provider.networkConfig.ExtraHeaders,
		provider.logger,
	)
}

// mantleChatCompletionsOpenAI handles non-streaming chat completions for OpenAI-family
// (gpt-*) models via the Bedrock Mantle OpenAI-compatible endpoint.
func (provider *BedrockProvider) mantleChatCompletionsOpenAI(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "chat/completions")

	// Build extraHeaders: always start with network-config headers, then overlay SigV4 if needed.
	// Allocate explicitly so maps.Copy never writes into a nil map.
	extraHeaders := make(map[string]string, len(provider.networkConfig.ExtraHeaders))
	maps.Copy(extraHeaders, provider.networkConfig.ExtraHeaders)
	if key.Value.GetValue() == "" {
		// SigV4: pre-build body for signing. HandleOpenAIChatCompletionRequest rebuilds the
		// same bytes (deterministic marshaling), so the signature stays valid.
		jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
			return openai.ToOpenAIChatRequest(ctx, request), nil
		})
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		sigHeaders, bifrostErr := provider.mantleSigV4Headers(ctx, jsonData, url, "application/json", key, region)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		maps.Copy(extraHeaders, sigHeaders)
	}

	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		extraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil, nil,
		provider.logger,
	)
}

// mantleChatCompletionsStream dispatches streaming chat completions on the Bedrock
// Mantle endpoint by model family: Anthropic-family (Claude) models use the native
// Anthropic Messages path; all other (OpenAI-family) models use the OpenAI-compatible path.
func (provider *BedrockProvider) mantleChatCompletionsStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if schemas.IsAnthropicModelFamily(ctx, request.Model) {
		return provider.mantleChatCompletionsStreamAnthropic(ctx, postHookRunner, postHookSpanFinalizer, key, request)
	}
	return provider.mantleChatCompletionsStreamOpenAI(ctx, postHookRunner, postHookSpanFinalizer, key, request)
}

// mantleChatCompletionsStreamAnthropic handles streaming chat completions for Claude
// models via the Bedrock Mantle native-Anthropic Messages endpoint. The endpoint returns
// native Anthropic SSE, so it reuses the shared Anthropic streaming handler.
func (provider *BedrockProvider) mantleChatCompletionsStreamAnthropic(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	_, bareModel := parseBedrockRegionAndModel(request.Model)
	url := mantleAnthropicURL(region)

	jsonData, bifrostErr := anthropic.BuildAnthropicChatRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
		Provider:                  schemas.Bedrock,
		Model:                     bareModel,
		IsStreaming:               true,
		ShouldSendBackRawRequest:  provider.sendBackRawRequest,
		ShouldSendBackRawResponse: provider.sendBackRawResponse,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers, bifrostErr := provider.mantleAnthropicHeaders(ctx, jsonData, url, "text/event-stream", key, region)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return anthropic.HandleAnthropicChatCompletionStreaming(
		ctx,
		provider.mantleStreamingClient,
		url,
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
		provider.logger,
		postHookSpanFinalizer,
	)
}

// mantleChatCompletionsStreamOpenAI handles streaming chat completions for OpenAI-family
// (gpt-*) models via the Bedrock Mantle OpenAI-compatible endpoint.
func (provider *BedrockProvider) mantleChatCompletionsStreamOpenAI(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostChatRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "chat/completions")

	// Bearer: identical to Groq / any OpenAI-compatible provider.
	if key.Value.GetValue() != "" {
		authHeader := map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
		return openai.HandleOpenAIChatCompletionStreaming(
			ctx, provider.mantleStreamingClient, url, request,
			authHeader, provider.networkConfig.ExtraHeaders,
			provider.networkConfig.StreamIdleTimeoutInSeconds,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(), postHookRunner,
			nil, nil, nil, nil, nil,
			provider.logger, postHookSpanFinalizer,
		)
	}

	// SigV4: pre-build body to sign, then pass it via customRequestConverter so the handler
	// sends the exact same bytes we signed.
	openaiReq := openai.ToOpenAIChatRequest(ctx, request)
	openaiReq.Stream = schemas.Ptr(true)
	openaiReq.StreamOptions = &schemas.ChatStreamOptions{IncludeUsage: schemas.Ptr(true)}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
		return openaiReq, nil
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	authHeader, bifrostErr := provider.mantleSigV4Headers(ctx, jsonData, url, "text/event-stream", key, region)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openai.HandleOpenAIChatCompletionStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		authHeader, provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner,
		func(_ *schemas.BifrostChatRequest) (providerUtils.RequestBodyWithExtraParams, error) {
			return openaiReq, nil
		},
		nil, nil, nil, nil,
		provider.logger, postHookSpanFinalizer,
	)
}

// mantleResponses dispatches non-streaming Responses API requests on the Bedrock Mantle
// endpoint by model family: Anthropic-family (Claude) models use the native Anthropic
// Messages path; all other (OpenAI-family) models use the OpenAI-compatible path.
func (provider *BedrockProvider) mantleResponses(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if schemas.IsAnthropicModelFamily(ctx, request.Model) {
		return provider.mantleResponsesAnthropic(ctx, key, request)
	}
	return provider.mantleResponsesOpenAI(ctx, key, request)
}

// mantleResponsesAnthropic handles non-streaming Responses API requests for Claude
// models via the Bedrock Mantle native-Anthropic Messages endpoint.
func (provider *BedrockProvider) mantleResponsesAnthropic(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	_, bareModel := parseBedrockRegionAndModel(request.Model)
	url := mantleAnthropicURL(region)

	config := anthropic.AnthropicRequestBuildConfig{
		Provider:                  schemas.Bedrock,
		Model:                     bareModel,
		ValidateTools:             true,
		BetaHeaderOverrides:       provider.networkConfig.BetaHeaderOverrides,
		ShouldSendBackRawRequest:  provider.sendBackRawRequest,
		ShouldSendBackRawResponse: provider.sendBackRawResponse,
	}

	// Pre-build the body so SigV4 can sign it; HandleAnthropicResponsesRequest rebuilds the
	// same bytes (deterministic marshaling), so the signature stays valid.
	jsonData, bifrostErr := anthropic.BuildAnthropicResponsesRequestBody(ctx, request, config)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers, bifrostErr := provider.mantleAnthropicHeaders(ctx, jsonData, url, "application/json", key, region)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return anthropic.HandleAnthropicResponsesRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		config,
		headers,
		provider.networkConfig.ExtraHeaders,
		provider.logger,
	)
}

// mantleResponsesOpenAI handles non-streaming Responses API requests for OpenAI-family
// (gpt-*) models via the Bedrock Mantle OpenAI-compatible endpoint.
func (provider *BedrockProvider) mantleResponsesOpenAI(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "responses")

	extraHeaders := make(map[string]string, len(provider.networkConfig.ExtraHeaders))
	maps.Copy(extraHeaders, provider.networkConfig.ExtraHeaders)
	if key.Value.GetValue() == "" {
		jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
			return openai.ToOpenAIResponsesRequest(ctx, request), nil
		})
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		sigHeaders, bifrostErr := provider.mantleSigV4Headers(ctx, jsonData, url, "application/json", key, region)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		maps.Copy(extraHeaders, sigHeaders)
	}

	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		openai.BearerAuthHeader(key),
		extraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil, nil,
		provider.logger,
	)
}

// mantleResponsesStream dispatches streaming Responses API requests on the Bedrock
// Mantle endpoint by model family: Anthropic-family (Claude) models use the native
// Anthropic Messages path; all other (OpenAI-family) models use the OpenAI-compatible path.
func (provider *BedrockProvider) mantleResponsesStream(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if schemas.IsAnthropicModelFamily(ctx, request.Model) {
		return provider.mantleResponsesStreamAnthropic(ctx, postHookRunner, postHookSpanFinalizer, key, request)
	}
	return provider.mantleResponsesStreamOpenAI(ctx, postHookRunner, postHookSpanFinalizer, key, request)
}

// mantleResponsesStreamAnthropic handles streaming Responses API requests for Claude
// models via the Bedrock Mantle native-Anthropic Messages endpoint.
func (provider *BedrockProvider) mantleResponsesStreamAnthropic(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	_, bareModel := parseBedrockRegionAndModel(request.Model)
	url := mantleAnthropicURL(region)

	jsonData, bifrostErr := anthropic.BuildAnthropicResponsesRequestBody(ctx, request, anthropic.AnthropicRequestBuildConfig{
		Provider:                  schemas.Bedrock,
		Model:                     bareModel,
		IsStreaming:               true,
		ValidateTools:             true,
		ShouldSendBackRawRequest:  provider.sendBackRawRequest,
		ShouldSendBackRawResponse: provider.sendBackRawResponse,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	headers, bifrostErr := provider.mantleAnthropicHeaders(ctx, jsonData, url, "text/event-stream", key, region)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return anthropic.HandleAnthropicResponsesStream(
		ctx,
		provider.mantleStreamingClient,
		url,
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
		provider.logger,
		postHookSpanFinalizer,
	)
}

// mantleResponsesStreamOpenAI handles streaming Responses API requests for OpenAI-family
// (gpt-*) models via the Bedrock Mantle OpenAI-compatible endpoint.
func (provider *BedrockProvider) mantleResponsesStreamOpenAI(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	postHookSpanFinalizer func(context.Context),
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	region := resolveBedrockRegion(ctx, key, request.Model)
	url := mantleOpenAIURL(region, schemas.ResolveCanonicalModel(ctx, request.Model), "responses")

	// Bearer: identical to Groq / any OpenAI-compatible provider.
	if key.Value.GetValue() != "" {
		authHeader := map[string]string{"Authorization": "Bearer " + key.Value.GetValue()}
		return openai.HandleOpenAIResponsesStreaming(
			ctx, provider.mantleStreamingClient, url, request,
			authHeader, provider.networkConfig.ExtraHeaders,
			provider.networkConfig.StreamIdleTimeoutInSeconds,
			providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
			provider.GetProviderKey(), postHookRunner,
			nil, nil, nil, nil,
			provider.logger, postHookSpanFinalizer,
		)
	}

	// SigV4: pre-build body to sign.
	openaiReq := openai.ToOpenAIResponsesRequest(ctx, request)
	openaiReq.Stream = schemas.Ptr(true)

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
		return openaiReq, nil
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	authHeader, bifrostErr := provider.mantleSigV4Headers(ctx, jsonData, url, "text/event-stream", key, region)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	return openai.HandleOpenAIResponsesStreaming(
		ctx, provider.mantleStreamingClient, url, request,
		authHeader, provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(), postHookRunner,
		nil, nil,
		func(_ *openai.OpenAIResponsesRequest) *openai.OpenAIResponsesRequest {
			return openaiReq
		},
		nil,
		provider.logger, postHookSpanFinalizer,
	)
}
